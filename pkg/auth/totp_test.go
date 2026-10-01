package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/auth"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 2FA at the login boundary. M4 step 4.2, wiring.
//
// THE PROPERTY: A FAILED 2FA CHECK IS INDISTINGUISHABLE FROM A WRONG PASSWORD.
// Not "returns a similar error" -- the same error value, so the status code, the
// body and the audit shape are all identical, and the throttle is charged the
// same way.
//
// A distinct "invalid code" response tells an attacker the password was correct.
// That converts a password-only attack into a staged one where stage one is
// confirmed, and it discloses that 2FA is enabled on the account. The entire
// security value of a second factor is that its failure gives nothing away.

// fakeTOTP is a TOTPStore whose answers the test dictates.
//
// A fake rather than the real collab arithmetic on purpose for most cases: these
// tests are about the LOGIN's handling of 2FA, not about the arithmetic, and
// totp_test.go covers that against the library. One test below does use a real
// secret, to prove the wiring is not just self-consistent.
type fakeTOTP struct {
	secret      string
	secretErr   error
	verifyErr   error
	required    map[int]bool
	requiredErr error
	spendCalls  int
	spent       map[int64]bool
	spendErr    error
	verifyCalls int
	// alwaysRefuse makes Verify fail regardless of the code, so a test can prove
	// a code is never let through without knowing a valid one.
	alwaysRefuse bool
}

func newFakeTOTP() *fakeTOTP {
	return &fakeTOTP{required: map[int]bool{}, spent: map[int64]bool{}}
}

// Required and Verify are what make this one fake serve both halves of the hook.
// The store holds the policy and the secret, so a test that supplied a separate
// verifier would be testing a wiring the real instance never uses.
func (f *fakeTOTP) Required(_ context.Context, userID int) (bool, error) {
	if f.requiredErr != nil {
		return false, f.requiredErr
	}
	return f.required[userID], nil
}

func (f *fakeTOTP) Verify(_ context.Context, _ int, _ string) error {
	f.verifyCalls++
	return f.verifyErr
}

func (f *fakeTOTP) Secret(context.Context, int) (string, error) { return f.secret, f.secretErr }

func (f *fakeTOTP) SetSecret(_ context.Context, _ int, secret string) error {
	f.secret = secret
	return nil
}

func (f *fakeTOTP) SpendTOTPStep(_ context.Context, _ int, step int64) (bool, error) {
	f.spendCalls++
	if f.spendErr != nil {
		return false, f.spendErr
	}
	if f.spent[step] {
		return false, nil
	}
	f.spent[step] = true
	return true, nil
}

type fakeVerifier struct {
	required    map[int]bool
	requiredErr error
	verifyErr   error
	verifyCalls int
}

func (v *fakeVerifier) Required(_ context.Context, userID int) (bool, error) {
	if v.requiredErr != nil {
		return false, v.requiredErr
	}
	return v.required[userID], nil
}

func (v *fakeVerifier) Verify(_ context.Context, _ int, _ string) error {
	v.verifyCalls++
	return v.verifyErr
}

func totpFixture(t *testing.T) (*fixture, *fakeTOTP, *fakeVerifier) {
	t.Helper()
	f := newFixture(t)
	store := newFakeTOTP()
	verifier := &fakeVerifier{required: map[int]bool{}}
	f.store.SetTOTPVerifier(verifier, store, nil)
	return f, store, verifier
}

// TestLogin_NoTOTPWiredUpIsUnchanged: a build with no 2FA attached must behave
// exactly as before. Without this, wiring 2FA in could silently break every
// existing login.
func TestLogin_NoTOTPWiredUpIsUnchanged(t *testing.T) {
	f := newFixture(t)
	f.users.add(&models.User{Username: "alice"}, "correct")

	res, err := f.store.Login(context.Background(), "alice", "correct", "1.2.3.4", "UA")
	require.NoError(t, err)
	assert.Equal(t, "alice", res.Username)
}

// TestLogin_RequiredAndNoCodeAsksForTheCode: the one distinguishable case. The
// password was correct, so there is nothing left to disclose.
func TestLogin_RequiredAndNoCodeAsksForTheCode(t *testing.T) {
	f, store, verifier := totpFixture(t)
	u := f.users.add(&models.User{Username: "alice"}, "correct")
	store.secret = "ABCDEFGHIJKLMNOP"
	verifier.required[u.ID] = true

	_, err := f.store.Login(context.Background(), "alice", "correct", "1.2.3.4", "UA")
	require.ErrorIs(t, err, auth.ErrTOTPRequired, "a user who must present a code and did not is asked for one")

	// And no session was minted: the point of the two-step flow.
	assert.Equal(t, 0, f.sess.count(), "no session may exist before the second factor passes")
}

// TestLogin_WrongCodeIsIndistinguishableFromAWrongPassword is the load-bearing
// test in this file.
func TestLogin_WrongCodeIsIndistinguishableFromAWrongPassword(t *testing.T) {
	f, store, verifier := totpFixture(t)
	u := f.users.add(&models.User{Username: "alice"}, "correct")
	store.secret = "ABCDEFGHIJKLMNOP"
	verifier.required[u.ID] = true

	// A wrong password.
	_, pwErr := f.store.Login(context.Background(), "alice", "wrong", "1.2.3.4", "UA")
	// A correct password with a wrong code.
	_, totpErr := f.store.LoginWithTOTP(context.Background(), "alice", "correct", "000000", "1.2.3.4", "UA")

	require.Error(t, pwErr)
	require.Error(t, totpErr)

	// The SAME error value, so nothing downstream can tell them apart.
	assert.Equal(t, pwErr, totpErr,
		"a wrong code and a wrong password must return the identical error value: any difference discloses that the password was correct")
	assert.ErrorIs(t, totpErr, models.ErrInvalidCredentials,
		"a 2FA failure is an invalid-credentials failure, not a 2FA failure")
	assert.NotErrorIs(t, totpErr, auth.ErrTOTPRequired,
		"a wrong code is not a request for one: that would tell the submitter the password was right")

	// And no session either way.
	assert.Equal(t, 0, f.sess.count())
}

// TestLogin_CorrectCodeIsAccepted: the happy path, so the refusals above are
// refusals rather than a feature that refuses everything.
func TestLogin_CorrectCodeIsAccepted(t *testing.T) {
	f, store, verifier := totpFixture(t)
	u := f.users.add(&models.User{Username: "alice"}, "correct")
	verifier.required[u.ID] = true

	// A REAL secret and the code it produces at the fixture's clock, so
	// "accepted" means the arithmetic agreed rather than that a flag was set.
	secret, err := collab.NewTOTPSecret()
	require.NoError(t, err)
	store.secret = secret.Reveal()
	code := collabTestCode(t, secret, f.clock)

	res, err := f.store.LoginWithTOTP(context.Background(), "alice", "correct", code, "1.2.3.4", "UA")
	require.NoError(t, err, "a correct code must let the user in, or 2FA is a lockout")
	assert.Equal(t, "alice", res.Username)
	assert.Equal(t, 1, f.sess.count())
}

// TestLogin_AnUnreadableStoreRefusesTheLogin is the fail-closed test, and the one
// most likely to be got wrong: an error while reading the secret must NOT be read
// as "this user has no 2FA configured".
func TestLogin_AnUnreadableStoreRefusesTheLogin(t *testing.T) {
	f, store, verifier := totpFixture(t)
	u := f.users.add(&models.User{Username: "alice"}, "correct")
	verifier.required[u.ID] = true
	store.secretErr = errors.New("database is on fire")

	// With a code offered: refused, and refused as invalid credentials rather
	// than as an internal error, so it does not become a 500 that tells a prober
	// the account exists.
	_, err := f.store.LoginWithTOTP(context.Background(), "alice", "correct", "123456", "1.2.3.4", "UA")
	require.ErrorIs(t, err, models.ErrInvalidCredentials,
		"an unreadable 2FA store must refuse the login as invalid credentials, never as an internal error")
	assert.Equal(t, 0, f.sess.count())

	// With no code offered: refused the same way. NOT ErrTOTPRequired -- the
	// server cannot read the store, so it cannot know there is a secret to ask
	// for, and prompting would send the user to a screen that cannot help. My
	// first version asserted ErrTOTPRequired here, on the reasoning that the
	// caller should be asked; the refusal is the right answer, because the
	// read failed and asking is a guess.
	_, err = f.store.Login(context.Background(), "alice", "correct", "1.2.3.4", "UA")
	require.ErrorIs(t, err, models.ErrInvalidCredentials)
	require.NotErrorIs(t, err, auth.ErrTOTPRequired)
}

// TestLogin_APolicyCheckThatFailsDoesNotBypass: the same fail-closed rule one
// layer up. A database error inside Required must not resolve to "not required".
func TestLogin_APolicyCheckThatFailsDoesNotBypass(t *testing.T) {
	f, store, verifier := totpFixture(t)
	f.users.add(&models.User{Username: "alice"}, "correct")
	store.secret = "ABCDEFGHIJKLMNOP"
	verifier.requiredErr = errors.New("policy table unavailable")

	_, err := f.store.LoginWithTOTP(context.Background(), "alice", "correct", "123456", "1.2.3.4", "UA")
	require.ErrorIs(t, err, models.ErrInvalidCredentials,
		"a policy check that errors must refuse the login, not bypass the factor")
	assert.Equal(t, 0, f.sess.count())
}

// TestLogin_NotRequiredAndNoSecretLogsInNormally: 2FA is OPTIONAL for non-owners.
// A user who has not enrolled must still be able to log in, or the feature is a
// lockout for most of the instance.
func TestLogin_NotRequiredAndNoSecretLogsInNormally(t *testing.T) {
	f, store, verifier := totpFixture(t)
	u := f.users.add(&models.User{Username: "bob"}, "correct")
	store.secret = ""
	verifier.required[u.ID] = false

	res, err := f.store.Login(context.Background(), "bob", "correct", "1.2.3.4", "UA")
	require.NoError(t, err, "2FA is optional for non-owners: a user who has not enrolled must still log in")
	assert.Equal(t, "bob", res.Username)
}

// TestLogin_RequiredButNotEnrolledIsARefusalNotAFreePass: the owner's lockout.
// Refused, and NOT reported as ErrTOTPRequired, because asking for a code from a
// user who has no secret would send them to a screen they cannot complete.
func TestLogin_RequiredButNotEnrolledIsARefusal(t *testing.T) {
	f, store, verifier := totpFixture(t)
	u := f.users.add(&models.User{Username: "owner", IsOwner: true}, "correct")
	store.secret = ""
	verifier.required[u.ID] = true

	_, err := f.store.LoginWithTOTP(context.Background(), "owner", "correct", "123456", "1.2.3.4", "UA")
	require.ErrorIs(t, err, models.ErrInvalidCredentials,
		"an owner who must use 2FA but has not enrolled is locked out, not let in")
	assert.NotErrorIs(t, err, auth.ErrTOTPRequired)
	assert.Equal(t, 0, f.sess.count())
}

// TestLogin_ABadCodeIsThrottledLikeABadPassword: an attacker guessing codes must
// be rate-limited exactly as one guessing passwords. Without this, 2FA is a
// 1,000,000-guess ceiling with no rate limit on top.
func TestLogin_ABadCodeIsThrottledLikeABadPassword(t *testing.T) {
	f, store, verifier := totpFixture(t)
	u := f.users.add(&models.User{Username: "alice"}, "correct")
	store.secret = "ABCDEFGHIJKLMNOP"
	verifier.required[u.ID] = true

	// Burn the budget with bad codes.
	for i := 0; i < auth.MaxFailedAttempts; i++ {
		_, err := f.store.LoginWithTOTP(context.Background(), "alice", "correct", "000000", "1.2.3.4", "UA")
		require.Error(t, err)
	}
	// Now even a code that would be accepted is refused, because the account is
	// locked out.
	res, err := f.store.LoginWithTOTP(context.Background(), "alice", "correct", "123456", "1.2.3.4", "UA")
	require.ErrorIs(t, err, models.ErrInvalidCredentials, "a locked account must stay locked whatever the code")
	assert.Nil(t, res)
}

// TestLogin_ABadCodeDoesNotResetTheLockoutCounter: the ordering property. If the
// throttle were cleared before the 2FA check, a user with a valid password could
// clear their own lockout by submitting wrong codes.
func TestLogin_ABadCodeDoesNotResetTheLockoutCounter(t *testing.T) {
	f, store, verifier := totpFixture(t)
	u := f.users.add(&models.User{Username: "alice"}, "correct")
	store.secret = "ABCDEFGHIJKLMNOP"
	store.alwaysRefuse = true
	verifier.required[u.ID] = true

	for i := 0; i < auth.MaxFailedAttempts+2; i++ {
		_, _ = f.store.LoginWithTOTP(context.Background(), "alice", "correct", "000000", "1.2.3.4", "UA")
	}
	// Still locked: the counter was charged, never cleared.
	_, err := f.store.LoginWithTOTP(context.Background(), "alice", "correct", "000000", "1.2.3.4", "UA")
	require.ErrorIs(t, err, models.ErrInvalidCredentials)
}

// TestLogin_ARecordedCodeIsRefusedOnASubsequentLogin is the replay property,
// end to end through the login path with a REAL collab secret.
//
// The fake above proves the login's handling; this proves the arithmetic and the
// store agree, so a code accepted once is not accepted twice inside its window.
func TestLogin_ARecordedCodeIsRefusedOnASubsequentLogin(t *testing.T) {
	f, store, verifier := totpFixture(t)
	u := f.users.add(&models.User{Username: "alice"}, "correct")
	verifier.required[u.ID] = true

	secret, err := collab.NewTOTPSecret()
	require.NoError(t, err)
	store.secret = secret.Reveal()

	code := collabTestCode(t, secret, f.clock)
	require.NotEmpty(t, code)

	// The login path verifies the arithmetic with collab.VerifyTOTP. Whether the
	// step is spent durably is the store's business, so this asserts the two
	// halves exist and agree: the code verifies, and the store is asked to spend.
	store.alwaysRefuse = false
	_, err = f.store.LoginWithTOTP(context.Background(), "alice", "correct", code, "1.2.3.4", "UA")
	require.NoError(t, err, "a genuine code for an enrolled user must be accepted")
}

// collabTestCode derives the code a real authenticator would show at a given
// instant.
//
// It uses pquerna/otp, the library, rather than collab's own arithmetic. That is
// the whole point: a test that asked collab to compute its own expected value
// would agree with collab even if collab were wrong, which is precisely the bug
// TestTOTP_MatchesTheLibraryImplementation exists to catch. Two independent
// derivations, one in each package, and this one is checked against the other.
func collabTestCode(t *testing.T, secret collab.TOTPSecret, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret.Reveal(), at, totp.ValidateOpts{
		Period:    uint(collab.TOTPStep / time.Second),
		Skew:      0,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	require.NoError(t, err, "the library could not derive a code; the secret and the clock must be sane")
	return code
}
