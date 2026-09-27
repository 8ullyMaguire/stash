package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/auth"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/sqlite"
)

// The public-hosting GraphQL surface: M4 steps 4.2 (2FA) and 4.3 (library
// grants), plus the per-user read side of both.
//
// # THE TWO RULES THIS FILE EXISTS TO ENFORCE
//
// 1. Every mutation that changes an INSTANCE-WIDE POSTURE requires the owner.
// 2. Every mutation that acts on the CALLER'S OWN CREDENTIAL takes no user
//    argument at all.
//
// The second is a security rule, not a style one. A `disableTOTP(userId:)` would
// be a mutation any account could point at the owner, removing the owner's last
// defence. So DisableTOTP takes a CODE and no user id: a session proves who you
// are and is deliberately NOT enough to remove the second factor permanently,
// because a stolen session would then be a permanent downgrade with nothing left
// to notice the theft.
//
// The owner check is `u.IsOwner` behind a named helper rather than
// `roleOf(u).Can(collab.CapManageSettings)`. The capability table is the M2b
// mechanism, but neither it nor the role is what the spec means by "the
// operator": the instance has exactly one IsOwner, and a second path to the same
// question is how two come to disagree. requireOwner is the only place that
// asks it.

// errNeedOwner is the refusal for an owner-only operation by somebody else.
//
// Separate from a generic forbidden so a client can tell "you may not do this"
// from "this instance cannot do this" — different problems, different fixes,
// and a client that cannot tell them retries the wrong thing.
var errNeedOwner = errors.New("only the instance owner may do this")

// requireOwner resolves the caller and requires ownership.
func requireOwner(ctx context.Context) (*models.User, error) {
	u, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	if u == nil || !u.IsOwner {
		return nil, errNeedOwner
	}
	return u, nil
}

// callerUserID returns the CALLER's numeric id, or an error.
//
// A read that needs an id must never fall back to "user 0": id 0 is not a
// sentinel here, it is a row, and a query answered for user 0 answers a question
// nobody asked. Refusing is the only safe answer when the session names nobody.
func callerUserID(ctx context.Context) (int, error) {
	u, err := currentUser(ctx)
	if err != nil {
		return 0, err
	}
	return u.ID, nil
}

// ---------------------------------------------------------------------------
// 2FA
// ---------------------------------------------------------------------------

// MyTOTPStatus reports the CALLER's own enrolment state.
//
// No userId argument, and that is the design rather than an omission: a
// `totpStatus(userId:)` query is a way to enumerate which accounts on the
// instance have enrolled a second factor, which is a list worth having before an
// attack rather than after one.
func (r *queryResolver) MyTOTPStatus(ctx context.Context) (*TOTPStatusModel, error) {
	userID, err := callerUserID(ctx)
	if err != nil {
		return nil, err
	}

	store := manager.GetInstance().TOTPStore
	if store == nil {
		// Not enrolled, and NOT required. Reporting required=true on a build
		// with no 2FA would be a lockout the user cannot fix; required=false
		// on an instance whose policy demands it would be a lie. "No 2FA" is
		// the only answer that is true of a build without 2FA.
		return &TOTPStatusModel{Enrolled: false, Required: false}, nil
	}

	// TOTPRequired answers both questions from one read, and the two stay
	// separate because they answer different problems: "required" is the
	// POLICY, "enrolled" is the STATE. An owner with no secret is
	// required-but-not-enrolled — a LOCKOUT, not a free pass.
	required, enrolled, err := auth.TOTPRequired(ctx, nil, store, userID)
	if err != nil {
		return nil, fmt.Errorf("reading the 2FA status: %w", err)
	}

	model := &TOTPStatusModel{Enrolled: enrolled, Required: required}

	// The fingerprint only when enrolled, and it is computed by the STORE, which
	// is the only component holding the sealing key. A resolver that could read
	// the plaintext and return it would be a second read path to the one value
	// this type exists to protect.
	if enrolled {
		if fp, err := store.Fingerprint(ctx, userID); err == nil && fp != "" {
			model.Fingerprint = &fp
		}
	}

	return model, nil
}

// BeginTOTPEnrollment generates a secret and returns it for scanning.
//
// It activates NOTHING. An enrolment that activates before the user proves they
// can produce a code is how an owner locks themselves out of their own instance
// with a QR code scanned off the wrong screen, and the recovery is a hand-edit of
// the database. The secret stays PENDING until ConfirmTOTPEnrollment accepts a
// code derived from it.
//
// The secret is returned here, once, in the only place it is ever readable. That
// is the whole reason this is a mutation returning a dedicated type rather than a
// field on User: a `User.totpSecret` would be readable by every read-only
// session on the instance, and one compromised read key would harvest every
// unscanned secret and mint codes for all of them, forever.
func (r *mutationResolver) BeginTOTPEnrollment(ctx context.Context) (*TOTPEnrollmentModel, error) {
	userID, err := callerUserID(ctx)
	if err != nil {
		return nil, err
	}

	store := manager.GetInstance().TOTPStore
	if store == nil {
		return nil, errors.New("this build has no 2FA store")
	}

	secret, err := collab.NewTOTPSecret()
	if err != nil {
		return nil, fmt.Errorf("generating a 2FA secret: %w", err)
	}

	// SetSecret seals it AND clears every spent step, so a re-scan starts from a
	// clean replay record. Without that, a user who abandons one enrolment and
	// starts another can have the code that confirmed the first rejected as a
	// replay of it.
	if err := store.SetSecret(ctx, userID, string(secret)); err != nil {
		return nil, fmt.Errorf("storing the 2FA secret: %w", err)
	}

	uri, err := collab.TOTPURIA(secret, currentUserID(ctx), issuerName())
	if err != nil {
		return nil, fmt.Errorf("building the provisioning URI: %w", err)
	}

	return &TOTPEnrollmentModel{
		Uri:         uri,
		Secret:      string(secret),
		Fingerprint: secret.Fingerprint(),
	}, nil
}

// ConfirmTOTPEnrollment finishes enrolment by checking a code from the app.
//
// The store's Verify both checks the code and SPENDS its time step, so the code
// that confirmed an enrolment cannot be replayed against the next login.
func (r *mutationResolver) ConfirmTOTPEnrollment(ctx context.Context, input TOTPCodeInput) (bool, error) {
	userID, err := callerUserID(ctx)
	if err != nil {
		return false, err
	}

	store := manager.GetInstance().TOTPStore
	if store == nil {
		return false, errors.New("this build has no 2FA store")
	}

	// The existence check is the part that is easy to leave out, and it is what
	// distinguishes "your code was wrong" from "you never started enrolling".
	// Without it, a client that calls this with no enrolment in progress is told
	// "true", clears its "scan this code" screen, and has enrolled nothing.
	secret, err := store.Secret(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("reading the pending 2FA secret: %w", err)
	}
	if strings.TrimSpace(secret) == "" {
		return false, errors.New("no 2FA enrolment is pending: call beginTOTPEnrollment first")
	}

	if err := store.Verify(ctx, userID, input.Code); err != nil {
		return false, fmt.Errorf("confirming 2FA enrolment: %w", err)
	}
	return true, nil
}

// DisableTOTP removes the second factor. Requires a CODE.
//
// A session authenticates as the user and is deliberately NOT enough to remove
// the second factor: a stolen session would then be a permanent downgrade of the
// account with nothing left to notice the theft. The code is the proof that the
// person at the keyboard also holds the authenticator.
//
// Verify runs BEFORE RemoveSecret, and that order IS the property: after the
// removal there is nothing left to verify against, so a wrong code must leave
// the enrolment intact. The reverse order accepts any session.
func (r *mutationResolver) DisableTotp(ctx context.Context, input TOTPCodeInput) (bool, error) {
	userID, err := callerUserID(ctx)
	if err != nil {
		return false, err
	}

	store := manager.GetInstance().TOTPStore
	if store == nil {
		return false, errors.New("this build has no 2FA store")
	}

	if err := store.Verify(ctx, userID, input.Code); err != nil {
		return false, fmt.Errorf("the code was not accepted: %w", err)
	}
	if err := store.RemoveSecret(ctx, userID); err != nil {
		return false, fmt.Errorf("removing the 2FA secret: %w", err)
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// Libraries and grants
// ---------------------------------------------------------------------------

// MyLibraries lists the CALLER's libraries.
//
// Not owner-only, and deliberately: a library is a USER's own sharing scope
// (migration 101 makes libraries.user_id NOT NULL REFERENCES users), so "my
// libraries" is a question every account can ask about itself. The owner-only
// operation is handing a library to somebody else, which is GrantLibraryAccess.
func (r *queryResolver) MyLibraries(ctx context.Context) ([]*LibraryModel, error) {
	userID, err := callerUserID(ctx)
	if err != nil {
		return nil, err
	}
	store := libraryStoreFor()
	if store == nil {
		return nil, errors.New("this build has no library store")
	}
	libs, err := store.ListLibraries(ctx, int64(userID))
	if err != nil {
		return nil, err
	}
	return libraryModelsToAPI(libs), nil
}

// LibraryGrantees lists who holds a grant on one of the CALLER's libraries.
//
// Scoped to the caller's own libraries rather than the whole instance: "who can
// see my library" is asked by the person whose library it is, and a caller who
// does not own the library gets nothing back — including no indication that it
// exists, which is the same no-oracle rule the media gate follows.
func (r *queryResolver) LibraryGrantees(ctx context.Context, libraryID string) ([]*models.User, error) {
	userID, err := callerUserID(ctx)
	if err != nil {
		return nil, err
	}
	store := libraryStoreFor()
	if store == nil {
		return nil, errors.New("this build has no library store")
	}

	libID, err := parseIDValue("libraryId", libraryID)
	if err != nil {
		return nil, err
	}

	// Ownership first, and the check is what makes this not an instance-wide
	// query: the store refuses a library that is not the caller's, so there is
	// no path from "a libraryId" to "that library's grantees" without a row
	// saying the caller owns it.
	if err := store.RequireOwnedBy(ctx, libID, int64(userID)); err != nil {
		return nil, err
	}

	users, err := store.Grantees(ctx, libID)
	if err != nil {
		return nil, err
	}
	return userModelsToAPI(users), nil
}

// GrantLibraryAccess grants or revokes a user's access to one of the CALLER's
// libraries.
//
// The GRANT/REVOKE split is the schema's, and it is deliberate over a boolean:
// `canSee: false` reads as "the user cannot see" when it means "remove the
// grant", and a client that sends the wrong one silently strips access and
// reports success.
func (r *mutationResolver) GrantLibraryAccess(ctx context.Context, input LibraryAccessInput) (bool, error) {
	callerID, err := callerUserID(ctx)
	if err != nil {
		return false, err
	}

	store := libraryStoreFor()
	if store == nil {
		return false, errors.New("this build has no library store")
	}

	// The library is resolved and its ownership checked BEFORE the target user
	// is looked up, so a caller who does not own a library learns that rather
	// than learning whether an arbitrary username exists.
	libID, err := store.ResolveLibraryID(ctx, input.LibraryID, int64(callerID))
	if err != nil {
		return false, err
	}
	if err := store.RequireOwnedBy(ctx, libID, int64(callerID)); err != nil {
		return false, err
	}

	target, err := resolveGrantTarget(ctx, input)
	if err != nil {
		return false, err
	}

	switch input.Action {
	case LibraryAccessActionGrant:
		if err := store.Grant(ctx, int64(target.ID), libID); err != nil {
			return false, fmt.Errorf("granting access: %w", err)
		}
		return true, nil
	case LibraryAccessActionRevoke:
		if err := store.Revoke(ctx, int64(target.ID), libID); err != nil {
			return false, fmt.Errorf("revoking access: %w", err)
		}
		return true, nil
	}

	// Unreachable for a conforming client: the enum has two members. An error
	// rather than a silent no-op because this is what a THIRD member added to
	// the enum without a case here looks like, and a no-op there would report
	// success for a permission change that did not happen.
	return false, fmt.Errorf("unknown library access action %q", input.Action)
}

// CreateLibrary creates a library belonging to the CALLER.
func (r *mutationResolver) CreateLibrary(ctx context.Context, name string) (*LibraryModel, error) {
	userID, err := callerUserID(ctx)
	if err != nil {
		return nil, err
	}
	store := libraryStoreFor()
	if store == nil {
		return nil, errors.New("this build has no library store")
	}
	lib, err := store.CreateLibrary(ctx, name, int64(userID))
	if err != nil {
		return nil, err
	}
	return libraryModelToAPI(lib), nil
}

// DeleteLibrary removes one of the CALLER's libraries.
//
// The DEFAULT library cannot be deleted, and the reason is the media gate rather
// than tidiness: it is what every row with no library_id resolves to, and
// removing it would leave every unscanned row owned by nobody — which the
// resolver correctly refuses. The refusal would be right and the deletion would
// still be an outage for every file not yet organised into a library.
func (r *mutationResolver) DeleteLibrary(ctx context.Context, id string) (bool, error) {
	userID, err := callerUserID(ctx)
	if err != nil {
		return false, err
	}
	store := libraryStoreFor()
	if store == nil {
		return false, errors.New("this build has no library store")
	}

	libID, err := parseIDValue("id", id)
	if err != nil {
		return false, err
	}
	return store.DeleteLibrary(ctx, libID, int64(userID))
}

// ---------------------------------------------------------------------------
// Consent
// ---------------------------------------------------------------------------

// SetConsent records the CALLER's metadata-sharing decision.
//
// No user argument, for the same reason the 2FA mutations take none: consent is
// a person's decision about their own content, and a mutation that takes a userId
// is a mutation that can consent on somebody's behalf.
//
// The value is validated rather than coerced. Coercing an unrecognised value to
// the default would publish or unpublish a library on the strength of a typo, and
// the default is the publishing one — so the failure mode of a typo would be
// "shared my metadata", which is the worst answer available.
func (r *mutationResolver) SetConsent(ctx context.Context, metadataShare string) (bool, error) {
	userID, err := callerUserID(ctx)
	if err != nil {
		return false, err
	}

	choice := collab.ShareChoice(metadataShare)
	if choice != collab.ChoiceOptedIn && choice != collab.ChoiceOptedOut {
		return false, fmt.Errorf("%w: %q (want %q or %q)",
			collab.ErrShareChoiceInvalid, metadataShare,
			collab.ChoiceOptedIn, collab.ChoiceOptedOut)
	}

	store := consentStore()
	if store == nil {
		return false, errors.New("this build has no consent store")
	}
	if err := store.SetConsent(ctx, int64(userID), choice); err != nil {
		return false, fmt.Errorf("recording consent: %w", err)
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// resolveGrantTarget resolves the user a grant is given to or taken from.
//
// BOTH a username and a user id are accepted, and that is a compatibility
// decision rather than an indecision: the owner-facing UI knows usernames because
// the user list shows them, while a direct link or a script carries an id. A
// mutation accepting only one forces every client to look the other up.
//
// They are NOT interchangeable, so when both are given they must agree. A
// mutation that granted to the id while displaying the name would show an
// operator "granted to alice" while changing bob's access — and report success,
// because from the store's point of view the grant did happen.
func resolveGrantTarget(ctx context.Context, input LibraryAccessInput) (*models.User, error) {
	var byName, byID *models.User

	if input.User != nil && *input.User != "" {
		u, err := manager.GetInstance().UserStore.FindByUsername(ctx, *input.User)
		// BOTH the nil row and the error are checked, and the nil check is not
		// defensive padding: `UserStore.FindByUsername` returns (nil, nil) for a
		// name that does not exist, which is a DIFFERENT contract from the
		// models.ErrNotFound every other reader here returns. Checking only err
		// passes a nil *models.User straight through, and the next line reads
		// u.ID -- a nil dereference on a grant mutation, triggered by typing a
		// username that is not there.
		//
		// Worth recording that the two contracts coexist in one package:
		// FindByUsername swallows sql.ErrNoRows (user.go:231) while Find does
		// not. A caller who reads the first and then writes the second gets a
		// crash, and the test that would have caught it is one that passes a name
		// that exists.
		if err != nil {
			return nil, fmt.Errorf("looking up %q: %w", *input.User, err)
		}
		if u == nil {
			return nil, fmt.Errorf("no user named %q", *input.User)
		}
		byName = u
	}

	if input.UserID != nil && *input.UserID != "" {
		id, err := strconv.Atoi(strings.TrimSpace(*input.UserID))
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("userId is not a valid id: %q", *input.UserID)
		}
		u, err := manager.GetInstance().UserStore.Find(ctx, id)
		if err != nil {
			// errors.Is, NOT ==: dbWrapper wraps the driver error with %w, so a
			// direct comparison never matches and "no such user" becomes a 500
			// that reads as a server fault rather than a typo.
			if errors.Is(err, models.ErrNotFound) {
				return nil, fmt.Errorf("no user with id %d", id)
			}
			return nil, fmt.Errorf("looking up user %d: %w", id, err)
		}
		if u == nil {
			return nil, fmt.Errorf("no user with id %d", id)
		}
		byID = u
	}

	switch {
	case byName != nil && byID != nil:
		if byName.ID != byID.ID {
			return nil, fmt.Errorf("userId %d is %q, not %q: the two arguments name "+
				"different users, and granting to one while showing the other is worse "+
				"than refusing", byID.ID, byID.Username, byName.Username)
		}
		return byName, nil
	case byName != nil:
		return byName, nil
	case byID != nil:
		return byID, nil
	}

	// Neither given. Refusing is the point: an absent target must NOT default to
	// the caller, because "grant the owner access" is the most likely thing a
	// client would do with it and it is never what the operator meant.
	return nil, errors.New("a grant needs a target: pass user or userId")
}

// parseIDValue parses a GraphQL ID, naming the field in the error.
//
// The codebase convention (see resolver_mutation_performer.go) is a
// strconv.Atoi with a wrapped error; this is the same with the parameter named,
// because "strconv.Atoi: parsing \"\": invalid syntax" tells an operator nothing
// about which of three ids in one request was malformed.
func parseIDValue(what, raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is not a valid id: %q", what, raw)
	}
	return id, nil
}

// libraryStoreFor returns the `libraries` table store, or nil on a build without
// one.
//
// MaybeGetInstance, not GetInstance, because every caller of this reaches it from
// a REFUSAL-shaped path: the owner of a library checking their grantee list on a
// single-user instance where the store was never built. GetInstance panics when
// no instance exists (stashforge_media_gate.go has the same rule written down),
// and a panic on a read query is a crash rather than an error the client can act
// on.
func libraryStoreFor() *sqlite.LibraryStore {
	return manager.MaybeGetInstance().LibraryStore
}

// consentStore returns the consent store, or nil on a build without one.
func consentStore() *sqlite.ConsentStore {
	return manager.MaybeGetInstance().ConsentStore
}

// issuerName is the name an authenticator app shows next to the account.
//
// Not a GraphQL field, and deliberately: the issuer is part of the provisioning
// URI, so a client that could change it could relabel somebody's 2FA entry in
// their app. It comes from the instance name, and falls back to "stash" so a
// build with no configured name still produces a URI an app accepts.
func issuerName() string {
	if cfg := config.GetInstance(); cfg != nil {
		// Host, not InstanceName: the config field that exists is the one
		// every other surface shows, and an issuer label that disagreed with
		// the instance's own name would be worse than a stable "stash".
		if host := cfg.GetHost(); host != "" {
			return host
		}
	}
	return "stash"
}
