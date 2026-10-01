package auth_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stashapp/stash/internal/collab"
	"github.com/stretchr/testify/require"
)

// A verifier that does the REAL arithmetic and keeps a REAL spent-step record.
//
// # Why the codes come from pquerna/otp
//
// The single-use property of 2FA is the one thing a test in this package cannot
// prove with a fake, because the rule lives INSIDE the verifier. Testing a stub
// against itself is how a broken replay guard ships.
//
// So the verifier under test is the real collab arithmetic, and the CODES come
// from pquerna/otp -- a separate implementation of RFC 6238. That is the
// strongest thing available here:
//
//   - codes from the library under test would prove only that it agrees with
//     itself;
//   - codes hand-rolled in this file would be a third implementation, and a test
//     whose expected value comes from different code than the code under test
//     can agree while both are wrong.
//
// An independent implementation is an ORACLE. If collab's arithmetic and
// pquerna's ever disagree, these tests fail -- which is the point. The oracle
// cannot drift into a shared bug either, because the two were written against
// the RFC rather than against each other.
type arithmeticVerifier struct {
	secret collab.TOTPSecret

	mu       sync.Mutex
	spent    map[int64]bool
	required map[int]bool
}

func newArithmeticVerifier(secret collab.TOTPSecret) *arithmeticVerifier {
	return &arithmeticVerifier{
		secret:   secret,
		spent:    map[int64]bool{},
		required: map[int]bool{},
	}
}

func (a *arithmeticVerifier) Required(_ context.Context, userID int) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.required[userID], nil
}

func (a *arithmeticVerifier) Verify(_ context.Context, _ int, code string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	step, err := collab.VerifyTOTPDetailed(a.secret, code, time.Now(), a.spent)
	if err != nil {
		return err
	}

	// The spend happens AFTER the arithmetic, for the same reason the real store
	// does it there: spending first would burn a legitimate user's step on an
	// attacker's wrong guess, which is a denial of service against one account.
	a.spent[step] = true
	return nil
}

func (a *arithmeticVerifier) Secret(context.Context, int) (string, error) {
	return a.secret.Reveal(), nil
}

func (a *arithmeticVerifier) SetSecret(_ context.Context, _ int, secret string) error {
	a.secret = collab.TOTPSecret(secret)
	return nil
}

func (a *arithmeticVerifier) SpendTOTPStep(_ context.Context, _ int, step int64) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.spent[step] {
		return false, nil
	}
	a.spent[step] = true
	return true, nil
}

// oracleSecret generates a secret collab accepts, and returns it alongside the
// pquerna key that generates codes for it.
//
// The two MUST come from one generation. A secret built by one library and codes
// generated from a separately-derived value would be a test that passes for the
// wrong reason, or fails for one, and either way proves nothing.
func oracleSecret(t *testing.T) (collab.TOTPSecret, *otp.Key) {
	t.Helper()

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "StashForge",
		AccountName: "owner",
		Period:      30,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	require.NoError(t, err, "the oracle must be able to generate a key")

	return collab.TOTPSecret(key.Secret()), key
}

// codeAt is the code a real authenticator app shows at `at`, from the oracle.
//
// Skew is 0 on the GENERATION side deliberately: the generator asks for exactly
// one instant, and the SKEW belongs to the verifier, which is the thing under
// test. Generating a windowed code here would hide a verifier that accepted only
// one step where it should accept three.
func codeAt(t *testing.T, key *otp.Key, at time.Time) string {
	t.Helper()

	code, err := totp.GenerateCodeCustom(key.Secret(), at,
		totp.ValidateOpts{
			Period:    30,
			Skew:      0,
			Digits:    otp.DigitsSix,
			Algorithm: otp.AlgorithmSHA1,
		})
	require.NoError(t, err)
	return code
}

// codeNow is codeAt for the present, which is what the verifier checks against.
func codeNow(t *testing.T, key *otp.Key) string {
	t.Helper()
	return codeAt(t, key, time.Now())
}

// oracleCodeFor returns a valid code for a KNOWN secret, from the independent
// pquerna implementation.
//
// The key is reconstructed from the secret rather than generated, so a test can
// hold the secret as a constant and still get a code the verifier will accept.
// otp.NewKeyFromURL is used because it validates the secret the same way a real
// provisioning flow would -- a malformed constant fails here rather than
// producing codes that never match.
func oracleCodeFor(t *testing.T, secret string) string {
	t.Helper()

	key, err := otp.NewKeyFromURL(keyURL(secret))
	require.NoError(t, err, "the test secret must be valid base32")
	return codeNow(t, key)
}

// keyURL builds a provisioning URI for a known secret.
func keyURL(secret string) string {
	return "otpauth://totp/StashForge:owner?secret=" + secret +
		"&issuer=StashForge&algorithm=SHA1&digits=6&period=30"
}
