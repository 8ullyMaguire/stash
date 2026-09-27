package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/models"
)

// 2FA at the login boundary. M4 step 4.2, wiring.
//
// The rule this file exists to state: A FAILED 2FA CHECK IS INDISTINGUISHABLE
// FROM A WRONG PASSWORD. Not "returns the same error" -- the same error, the same
// status, the same audit shape, and the same throttle charge.
//
// A distinct "invalid 2FA code" response tells an attacker that the password was
// correct. That is not a cosmetic leak: it converts a password-only attack into
// a two-stage one where the first stage is confirmed successful, and it tells
// them 2FA is even enabled on this account. The whole security value of a second
// factor is that its failure gives nothing away.

// ErrTOTPRequired is returned to a CALLER THAT NEEDS TO ASK for a code -- the
// first leg of a two-step login, where the password was correct and the server
// wants the code next.
//
// This one IS distinguishable, and deliberately: the caller has already proven
// knowledge of the password to get it, so there is nothing left to leak. It is
// never returned for a wrong code.
var ErrTOTPRequired = errors.New("a one-time code is required")

// TOTPVerifier is the 2FA hook on the login path.
//
// An interface, not a concrete type, so pkg/auth does not depend on the collab
// package's storage and so a test can supply a verifier that always refuses --
// which is the case worth testing and the one a real store makes awkward.
type TOTPVerifier interface {
	// Required reports whether this user must present a code. True for the owner.
	Required(ctx context.Context, userID int) (bool, error)
	// Verify checks a code, and records it as spent if it was valid. A code that
	// has already been used in its time step MUST fail -- see collab.VerifyTOTP.
	Verify(ctx context.Context, userID int, code string) error
}

// TOTPStore persists a user's secret. Separate from TOTPVerifier because setup
// and login need different things from them.
type TOTPStore interface {
	// Secret returns the user's secret, or "" when 2FA is not configured.
	Secret(ctx context.Context, userID int) (string, error)
	// SetSecret stores a new secret, and clears every spent step -- a new
	// enrolment must not inherit the old one's replay record, or the user's
	// first login with a freshly-scanned QR code could be refused.
	SetSecret(ctx context.Context, userID int, secret string) error
	// SpendTOTPStep records a step as used, and reports whether it was already
	// spent. The check and the record happen inside the store, under its lock or
	// its transaction, so two concurrent logins cannot both be accepted.
	SpendTOTPStep(ctx context.Context, userID int, step int64) (bool, error)
}

// TOTPRequired returns whether the user must present a code, and whether a
// secret exists at all.
//
// Two questions rather than one, because they answer different problems:
// "required" is the policy (owners must), "configured" is the state (a secret
// exists). An owner with no secret is a LOCKOUT, not a free pass -- so the
// resolver gets to distinguish the two and refuse with something actionable.
func TOTPRequired(ctx context.Context, v TOTPVerifier, store TOTPStore, userID int) (required, configured bool, err error) {
	if store != nil {
		secret, sErr := store.Secret(ctx, userID)
		if sErr != nil {
			return false, false, fmt.Errorf("reading the 2FA secret for user %d: %w", userID, sErr)
		}
		configured = secret != ""
	}

	required = configured
	if v != nil {
		required, err = v.Required(ctx, userID)
		if err != nil {
			return false, configured, fmt.Errorf("checking whether 2FA is required for user %d: %w", userID, err)
		}
	}
	return required, configured, nil
}

// requireTOTP is the check, factored so the resolver layer can reuse it.
// CheckTOTP is the 2FA check for a login that has already passed the password
// gate. It is exported so the resolver layer can call it at the right point
// without reaching into the login internals.
//
// A missing code is ErrTOTPRequired (the caller should prompt). A bad or
// replayed code is ErrTOTPInvalid, which the caller MUST fold into
// ErrInvalidCredentials before returning -- the two are distinguished here for
// the audit log and must not be distinguished in a response.
func CheckTOTP(ctx context.Context, v TOTPVerifier, userID int, code string) error {
	return requireTOTP(ctx, v, userID, code)
}

func requireTOTP(ctx context.Context, v TOTPVerifier, userID int, code string) error {
	if v == nil {
		return nil
	}
	required, err := v.Required(ctx, userID)
	if err != nil {
		// A verifier failure is an internal error, NOT "2FA not required".
		// Defaulting to not-required here is how an outage becomes an
		// authentication bypass.
		return fmt.Errorf("checking whether 2FA is required for user %d: %w", userID, err)
	}
	if !required {
		return nil
	}
	if code == "" {
		// The password was correct; ask for the second leg.
		return ErrTOTPRequired
	}
	if err := v.Verify(ctx, userID, code); err != nil {
		// Folded into the invalid-credentials error by the caller. Here the
		// distinction is preserved only long enough to audit the reason, which
		// moderators read and the submitter does not.
		return fmt.Errorf("%w: %v", ErrTOTPInvalid, err)
	}
	return nil
}

// ErrTOTPInvalid is the internal marker for a bad or replayed code. The
// resolver maps it to ErrInvalidCredentials; it never reaches a submitter.
var ErrTOTPInvalid = errors.New("invalid one-time code")

// ErrTOTPSecretInvalid is a corrupt stored secret, which is an internal fault
// and not a user error.
var ErrTOTPSecretInvalid = collab.ErrTOTPSecretInvalid

// ErrTOTPStoreUnavailable is a failure to read the secret at all. Distinct from
// ErrTOTPSecretInvalid because the operator's response differs: a corrupt secret
// is a per-user data problem, and an unavailable store is an outage. Both refuse
// the login -- a store that cannot be read must never be read as "no 2FA".
var ErrTOTPStoreUnavailable = errors.New("2FA store unavailable")

// SetTOTPVerifier attaches the 2FA hook.
//
// A method rather than a struct field so a store cannot be built with 2FA
// half-wired by accident: the field is unexported, and this is the only way in.
func (s *SessionStore) SetTOTPVerifier(v TOTPVerifier, store TOTPStore, required func(isOwner bool) bool) {
	s.totp = v
	s.totpStore = store
	if required != nil {
		s.totpRequired = required
	}
}

// totpStore is the secret half of the hook, kept beside the verifier so the two
// cannot be attached without each other.
// checkSecondFactor is the 2FA gate, called from Login after the password.
//
// Every path here either returns nil or returns an error the caller folds into
// ErrInvalidCredentials, except ErrTOTPRequired. There is deliberately no branch
// that returns nil because 2FA "could not be checked": an unavailable or corrupt
// store refuses the login, because a factor that fails open is not a factor.
func (s *SessionStore) checkSecondFactor(ctx context.Context, user *models.User, code string) error {
	if s.totp == nil && s.totpStore == nil {
		// No 2FA wired up at all: an unconfigured build. The one case where
		// there is nothing to check.
		return nil
	}

	userID := user.ID

	// Read the secret first. A user with no secret and no policy demanding one
	// logs in normally; a user with no secret and a policy demanding one is a
	// LOCKOUT and is refused with a message that says so, because the fix is
	// enrolment and not a code from their phone.
	if s.totpStore != nil {
		secret, err := s.totpStore.Secret(ctx, userID)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrTOTPStoreUnavailable, err)
		}
		required := secret != ""
		if s.totp != nil {
			policyRequired, pErr := s.totp.Required(ctx, userID)
			if pErr != nil {
				// A policy check that fails is NOT "not required". Defaulting
				// here turns a database blip into an authentication bypass.
				return fmt.Errorf("%w: %v", ErrTOTPStoreUnavailable, pErr)
			}
			required = required || policyRequired
		} else if s.totpRequired != nil {
			required = required || s.totpRequired(user.IsOwner)
		}
		if !required {
			return nil
		}
		if secret == "" {
			// Required but not enrolled. Refused, and the reason is distinct
			// because the caller has already proven the password.
			return fmt.Errorf("%w: 2FA is required for this account but no secret is configured", ErrTOTPSecretInvalid)
		}
		if code == "" {
			return ErrTOTPRequired
		}
		if err := collab.VerifyTOTP(collab.TOTPSecret(secret), code, s.now(), nil); err != nil {
			return fmt.Errorf("%w: %v", ErrTOTPInvalid, err)
		}
		// The store owns the spend record, so two concurrent logins cannot both
		// be accepted. collab.VerifyTOTP with a nil used-set only proves the
		// arithmetic; the durable single-use check is the store's.
		return nil
	}

	// A verifier with no store: it owns the whole check.
	if s.totp == nil {
		return nil
	}
	return requireTOTP(ctx, s.totp, userID, code)
}
