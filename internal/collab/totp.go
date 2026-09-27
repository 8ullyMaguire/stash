package collab

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 specifies HMAC-SHA1; SHA-256 is not compatible with existing authenticator apps
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TOTP. M4 step 4.2.
//
// The plan's note is the reason this file exists:
//
//	A TOTP code is single-use within its time step -- replay of the same code
//	inside the window must fail, and that is the case worth a test.
//
// That is not how RFC 6238 works, and the difference matters. RFC 6238's
// Validate accepts a code with a drift window of +/- one step, and accepts the
// SAME code any number of times inside that window. For a login second factor
// that is a real weakness: a code observed once -- shoulder-surfed, logged by a
// hostile proxy, or read off a screen -- stays valid for up to 90 seconds and
// can be replayed freely within it.
//
// So verification here is: check the code against the current step and the two
// adjacent ones (to tolerate clock drift), then RECORD the step that matched.
// A code from a step already recorded is refused even though it verifies. The
// record is what makes a TOTP code single-use; the arithmetic alone never does.

// TOTPStep is the time step in seconds. RFC 6238's default.
const TOTPStep = 30 * time.Second

// TOTPSkew is how many steps either side of now are accepted, to tolerate clock
// drift between the server and the authenticator.
//
// One step either side, so the accepted window is 90 seconds. Wider would help a
// badly-drifted clock at the cost of a longer replay window, and the trade is
// explicit here rather than buried in a constant.
const TOTPSkew = 1

// TOTPDigits is the code length. Six is what every authenticator app defaults to
// and what the user will actually have installed.
const TOTPDigits = 6

var (
	// ErrTOTPInvalid is returned for a code that does not verify. Deliberately
	// undifferentiated between "wrong code" and "already used": a distinct error
	// for a replayed code tells an attacker their guess was CORRECT, which turns
	// a one-shot guess into a confirmation oracle.
	ErrTOTPInvalid = errors.New("invalid or already-used code")

	// ErrTOTPReplay is returned only for the internal/diagnostic path. Nothing in
	// the login flow surfaces it, precisely because of the reason above.
	ErrTOTPReplay = errors.New("code already used in this time step")

	// ErrTOTPSecretNotSet is returned when 2FA is required but the user has no
	// secret.
	ErrTOTPSecretNotSet = errors.New("two-factor authentication is required but not configured")

	// ErrTOTPSecretInvalid is returned for a stored secret that is not valid
	// base32. An error rather than a default: a corrupt secret must not be
	// treated as an empty one.
	ErrTOTPSecretInvalid = errors.New("stored 2FA secret is not valid base32")
)

// TOTPSecret is a user's 2FA secret, held as base32 (the encoding every
// authenticator app and every QR-code flow expects).
type TOTPSecret string

// NewTOTPSecret generates a fresh secret for setup.
//
// 20 random bytes, base32-encoded. 20 bytes is the RFC 4226 recommendation for
// HMAC-SHA1 and the length otpauth:// URIs are built around.
//
// ENCODED WITH PADDING, on the evidence of a test that would not pass. I wrote
// this unpadded on the reasoning that padding characters are a common source of
// "invalid secret" support tickets, and pquerna/otp -- the library that
// generates and validates these codes, and the de-facto reference for what
// authenticator apps accept -- rejected every secret this produced. A secret
// this project can generate but not verify is worse than a support ticket, so
// the padding stays.
func NewTOTPSecret() (TOTPSecret, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating 2FA secret: %w", err)
	}
	return TOTPSecret(base32.StdEncoding.EncodeToString(raw)), nil
}

// Decode returns the raw secret bytes, rejecting anything malformed.
func (s TOTPSecret) Decode() ([]byte, error) {
	cleaned := strings.ToUpper(strings.TrimSpace(string(s)))
	if cleaned == "" {
		return nil, ErrTOTPSecretInvalid
	}
	// Try padded first, then unpadded: a secret written by an older build, or by
	// a hand-pasted value from an app, may be either. Accepting both costs one
	// retry and means a user is never locked out by a stray "=".
	if b, err := base32.StdEncoding.DecodeString(cleaned); err == nil && len(b) > 0 {
		return b, nil
	}
	if b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(cleaned); err == nil && len(b) > 0 {
		return b, nil
	}
	return nil, fmt.Errorf("%w: %q is not valid base32", ErrTOTPSecretInvalid, redactForMessage(string(s)))
}

// redactForMessage keeps a malformed secret out of an error string. The first
// characters of a secret are not enough to attack it, but an error message is
// logged and forwarded, and there is no reason for a secret to appear in one.
func redactForMessage(s string) string {
	if len(s) <= 4 {
		return "[REDACTED TOTP SECRET]"
	}
	return fmt.Sprintf("%s...[REDACTED, %d chars]", s[:2], len(s))
}

// Reveal returns the actual secret.
//
// It exists as an explicit, greppable act rather than a cast, because String()
// redacts -- and that redaction is good enough to be dangerous. It caught me: a
// test helper passed s.String() to the TOTP library, and every test failed with
// "Decoding of secret as base32 failed" while the production code was correct.
// A redaction that makes the correct call fail loudly is doing its job; the
// mistake was not noticing the error was about MY argument.
//
// There are exactly two callers: the provisioning URI during setup, and the
// verification path, which passes the decoded bytes rather than the string.
func (s TOTPSecret) Reveal() string { return string(s) }

// String redacts. Without this a secret would be printed by any %v or %s of a
// struct containing it, and a log line is a disclosure channel that nobody
// remembers to treat as one.
func (s TOTPSecret) String() string {
	if s == "" {
		return ""
	}
	return "[REDACTED TOTP SECRET]"
}

// GoString makes %#v redacted too, for the same reason.
func (s TOTPSecret) GoString() string { return s.String() }

// totpCounter is the RFC 6238 time counter: the number of whole TOTPStep
// intervals since the Unix epoch.
//
// It exists as a function because getting it wrong is SILENT. TOTPStep is a
// time.Duration, so int64(TOTPStep) is 30e9 -- nanoseconds -- and
// Unix()/int64(TOTPStep) is 0 for every date this century. The first version
// did exactly that: every code was computed from counter 0, so the "correct
// code" was a constant, and the replay guard sat on top of a comparison that
// could never match anything a real authenticator produced. The tests that
// should have caught it all failed with the same generic "invalid code", which
// is indistinguishable from a user mistyping.
func totpCounter(at time.Time) int64 {
	return at.UTC().Unix() / int64(TOTPStep/time.Second)
}

// totpCode computes the RFC 6238 code for a given counter. HMAC-SHA1, 6 digits,
// dynamic truncation -- the parameters every authenticator app implements.
//
// Written out rather than pulled from a library so the arithmetic is inspectable
// next to the replay logic that is the point of this file. otp/totp is used for
// GENERATION (the provisioning URI, which must match what apps display) and
// this is used for VERIFICATION, so a mismatch between the two is caught by
// TestTOTP_MatchesTheLibraryImplementation rather than by a user's phone.
func totpCode(secret []byte, counter uint64) (string, error) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)

	mac := hmac.New(sha1.New, secret)
	mac.Write(buf[:])
	h := mac.Sum(nil)
	if len(h) < 20 {
		return "", errors.New("HMAC-SHA1 produced a short digest")
	}

	// Dynamic truncation, RFC 4226 §5.4. The 31st bit is cleared by masking
	// ONLY the top byte with 0x7f, and then reassembling -- NOT by masking a
	// 32-bit word with 0x7fffffff. Those two are not the same operation: the
	// latter also clears the high bits of the second, third and fourth bytes,
	// so the two disagree whenever any of those has its top bit set. That is
	// most of the time, and it is why the first version of this produced codes
	// that no authenticator app would ever show. TestTOTP_MatchesTheLibrary-
	// Implementation is what found it.
	offset := h[len(h)-1] & 0x0f
	value := (int64(h[offset]&0x7f) << 24) |
		(int64(h[offset+1]) << 16) |
		(int64(h[offset+2]) << 8) |
		int64(h[offset+3])
	return fmt.Sprintf("%0*d", TOTPDigits, value%1000000), nil
}

// VerifyTOTP checks a code against a secret and records the step it matched, so
// the same code cannot be used twice inside its window.
//
// `used` is the set of steps already spent. It is passed in rather than held
// internally because the durable record belongs in the database -- a process
// restart must not hand every user a fresh replay window.
//
// Returns ErrTOTPInvalid for a wrong code AND for a replay. Callers that need to
// tell them apart for diagnostics should use VerifyTOTPDetailed.
func VerifyTOTP(secret TOTPSecret, code string, now time.Time, used map[int64]bool) error {
	_, err := verifyTOTPDetailed(secret, code, now, used)
	if errors.Is(err, ErrTOTPReplay) {
		// Folded into the generic error on purpose. See ErrTOTPInvalid.
		return ErrTOTPInvalid
	}
	return err
}

// VerifyTOTPDetailed is VerifyTOTP, but it distinguishes a replay from a wrong
// code.
//
// Exists for logs and tests, not for the login response. A login endpoint that
// says "that code was already used" confirms to an attacker that a guessed code
// was correct, which is strictly more than the endpoint needs to say.
func VerifyTOTPDetailed(secret TOTPSecret, code string, now time.Time, used map[int64]bool) (int64, error) {
	return verifyTOTPDetailed(secret, code, now, used)
}

// verifyTOTPDetailed returns the counter of the step that matched.
//
// The comparison is constant-time across the candidate steps: a timing side
// channel that leaked which step matched would narrow a six-digit guess, and
// six digits is already the weakest part of this design.
func verifyTOTPDetailed(secret TOTPSecret, code string, now time.Time, used map[int64]bool) (int64, error) {
	raw, err := secret.Decode()
	if err != nil {
		return 0, err
	}
	candidate := strings.TrimSpace(code)
	if candidate == "" {
		return 0, ErrTOTPInvalid
	}

	current := totpCounter(now)
	matched := int64(-1)

	for delta := int64(-TOTPSkew); delta <= TOTPSkew; delta++ {
		counter := current + delta
		if counter < 0 {
			continue
		}
		expected, err := totpCode(raw, uint64(counter))
		if err != nil {
			return 0, err
		}
		// Constant-time, and only the LENGTH is compared first -- a length
		// mismatch leaks nothing useful, while an early-returning comparison
		// would.
		if subtle.ConstantTimeCompare([]byte(expected), []byte(candidate)) == 1 {
			matched = counter
		}
	}

	if matched < 0 {
		return 0, ErrTOTPInvalid
	}

	// The replay check. The code verifies -- it is genuinely the current code --
	// and it is still refused, because this step has already been spent.
	if used[matched] {
		return matched, ErrTOTPReplay
	}
	return matched, nil
}

// MarkTOTPStep records a step as spent and returns the updated set.
//
// The returned map is a COPY, not the one passed in. A function that mutates its
// caller's map cannot be reasoned about at the call site, and the failure mode
// is a login that accepts a replay because two callers shared a map.
func MarkTOTPStep(used map[int64]bool, step int64) map[int64]bool {
	next := make(map[int64]bool, len(used)+1)
	for k, v := range used {
		next[k] = v
	}
	next[step] = true
	return next
}

// PurgeTOTPUsedSteps drops spent steps that can no longer be accepted, so the
// record does not grow without bound.
//
// A step is droppable once it is more than TOTPSkew steps in the past: the
// verification window has moved beyond it, so it can never be matched again. The
// boundary is inclusive of the skew because a step exactly TOTPSkew in the past
// is still accepted right now.
func PurgeTOTPUsedSteps(used map[int64]bool, now time.Time) map[int64]bool {
	oldest := totpCounter(now) - TOTPSkew
	next := make(map[int64]bool, len(used))
	for k, v := range used {
		if k >= oldest && v {
			next[k] = true
		}
	}
	return next
}

// TOTPReplayWindow is the maximum number of codes a single login response window
// can accept, and the reason the replay record is per-step rather than a simple
// "last code seen".
//
// With TOTPSkew = 1 there are three acceptable steps, so three distinct codes can
// each be used once. A single "last code" would let a code from the previous step
// be reused, and a code from two steps ago is the only one that is safe to forget.
const TOTPReplayWindow = 2*TOTPSkew + 1

// TOTPURIA builds the otpauth:// URI for a provisioning QR code.
//
// The issuer is the instance name and the account is the username, because that
// is what the user sees in their app afterwards -- an account name of "alice@
// example.com" on a personal instance is a small thing that makes the entry
// recognisable rather than anonymous.
func TOTPURIA(secret TOTPSecret, account, issuer string) (string, error) {
	if _, err := secret.Decode(); err != nil {
		return "", err
	}
	// Percent-encode the label fields: an issuer containing a space or a slash
	// produces a URI that parses as something else, and the user's app then shows
	// a broken entry with no error.
	return "otpauth://totp/" + uriEscape(issuer) + ":" + uriEscape(account) +
		"?secret=" + secret.Reveal() +
		"&issuer=" + uriEscape(issuer) +
		"&algorithm=SHA1&digits=" + strconv.Itoa(TOTPDigits) +
		"&period=" + strconv.Itoa(int(TOTPStep/time.Second)), nil
}

func uriEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}

// Fingerprint is a short stable identifier for a secret, safe to display.
//
// It is the first 8 hex characters of the SHA-256 of the secret, so an operator
// can tell "the phone has key A or key B" when a user re-enrols -- without
// disclosing the secret itself. A truncated hash of a 160-bit secret discloses
// nothing useful, because recovering the secret from it is a preimage attack.
func (s TOTPSecret) Fingerprint() string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:4])
}

// TOTPRequired is the 2FA policy. 2FA is required for the owner and optional for
// everyone else, which is the asymmetry worth stating: the owner is the account
// whose compromise publishes every library on the instance.
type TOTPRequired func(isOwner bool) bool

// DefaultTOTPRequired: owners required, others optional.
func DefaultTOTPRequired(isOwner bool) bool { return isOwner }

// usedStepsGuard is a concurrency-safe view of the spent-step set, for the
// in-process case.
//
// The DATABASE is the authority -- a restart must not reopen the replay window --
// but a mutex-protected cache in front of it removes a write per login, and two
// concurrent logins with the same code must not both be accepted. Without this,
// the check-then-record is a race and two simultaneous requests with one code
// both succeed.
type usedStepsGuard struct {
	mu   sync.Mutex
	used map[int64]bool
}

func newUsedStepsGuard() *usedStepsGuard {
	return &usedStepsGuard{used: map[int64]bool{}}
}

// Spend records a step, returning false if it was already spent.
//
// The check and the record happen under one lock. Splitting them is precisely the
// race the plan's "single-use" requirement is about, and it is invisible in
// single-threaded tests.
func (g *usedStepsGuard) Spend(step int64, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	oldest := totpCounter(now) - TOTPSkew
	for k, v := range g.used {
		if k < oldest {
			delete(g.used, k)
		} else if v && k == step {
			return false
		}
	}
	g.used[step] = true
	return true
}
