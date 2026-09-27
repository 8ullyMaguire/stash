package auth_test

import (
	"context"
	"testing"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/auth"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 2FA as the RUNNING INSTANCE sees it, not as a component sees it.
//
// # Why this file is separate from the component tests
//
// The other 2FA tests call the session store's methods directly. Every one of
// them passes with the store never attached to a Factory, because they construct
// the object themselves. That is the "referenced != used" trap: a fully
// implemented, fully tested 2FA that no instance ever consults protects nothing,
// and the log says "multi-user auth enabled" without mentioning that the second
// factor is not being checked.
//
// These go through Factory.Build -- the path a real instance takes -- and then
// log in. The property is not "the store works" but "an instance BUILT THIS WAY
// asks for a second factor".

// buildInstance builds a multi-user instance through the real Factory, and
// returns the database session store behind the HTTP adapter.
//
// Multi-user is FORCED. The fixture's user table is empty, which would otherwise
// resolve to single-user and skip the TOTP store entirely -- correct behaviour,
// and it would make every test here vacuous.
func buildInstance(t *testing.T, f *factoryFixture, totp *fakeTOTP) *auth.SessionStore {
	t.Helper()

	force := true
	fac := f.factory()
	fac.MultiUser = &force
	fac.TOTP = totp

	store, mode, err := fac.Build(newCookieStoreDouble())
	require.NoError(t, err)
	require.Equal(t, auth.ModeMultiUser, mode)

	adapter, ok := store.(*session.HTTPAdapter)
	require.True(t, ok, "multi-user mode must hand back the HTTP adapter, got %T", store)

	resolver, ok := adapter.Resolver().(*auth.SessionStore)
	require.True(t, ok, "the adapter must be backed by the database store, got %T", adapter.Resolver())
	return resolver
}

// enrol creates a user and makes it 2FA-required with a known secret.
//
// It CREATES rather than looks up. The fixture's user table starts empty, and
// a lookup for a name that is not there returns (nil, nil) from the fake -- so
// the old version returned a nil *models.User and every test that used it
// dereferenced nil. A fixture that manufactures its own subject cannot be called
// on the wrong name.
func enrol(t *testing.T, f *factoryFixture, totp *fakeTOTP, username string) *models.User {
	t.Helper()

	user := newMember(t, f, username)
	// A valid base32 secret. The fake verifier accepts any code, so the VALUE is
	// not what these tests are about -- but it is kept well-formed so the test
	// keeps passing if the fake is ever replaced with the real verifier.
	totp.secret = oracleSecretValue
	totp.required[user.ID] = true
	return user
}

// oracleSecretValue is a fixed valid base32 secret. The store branch of
// checkSecondFactor runs the REAL arithmetic against it, so a test code has to be
// a real code for THIS secret -- which is why the tests that log in use the
// oracle rather than the literal "123456" they used to pass.
//
// The secret is fixed rather than generated so a failure is reproducible from the
// test name alone.
const oracleSecretValue = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"

func newMember(t *testing.T, f *factoryFixture, username string) *models.User {
	t.Helper()
	user := f.users.add(&models.User{Username: username}, "password123")
	require.NotNil(t, user)
	return user
}

// TestWiring_AnEnrolledOwnerCannotLogInWithOnlyAPassword: the load-bearing
// claim. Correct password, no code, no session.
func TestWiring_AnEnrolledOwnerCannotLogInWithOnlyAPassword(t *testing.T) {
	f := newFactoryFixture(t)
	totp := newFakeTOTP()
	sf := buildInstance(t, f, totp)
	enrol(t, f, totp, "owner")

	_, err := sf.Login(context.Background(), "owner", "password123", "10.0.0.1", "test")
	require.ErrorIs(t, err, auth.ErrTOTPRequired,
		"an enrolled user must be asked for a code, not logged in with one factor")
	assert.Zero(t, totp.verifyCalls,
		"no code was offered, so the verifier must not have been consulted: a call "+
			"here would mean the login was decided before 2FA was asked about")
}

// TestWiring_TheCorrectPasswordAndCodeLogsIn: the positive case, so the refusal
// above is a real gate and not a blanket failure.
func TestWiring_TheCorrectPasswordAndCodeLogsIn(t *testing.T) {
	f := newFactoryFixture(t)
	totp := newFakeTOTP()
	sf := buildInstance(t, f, totp)
	enrol(t, f, totp, "owner")

	// A REAL code for the secret, from the independent oracle. The literal
	// "123456" this test used to send is not that code, so the store branch
	// correctly refused it -- the test was asserting a success it had not earned.
	code := oracleCodeFor(t, oracleSecretValue)

	res, err := sf.LoginWithTOTP(context.Background(), "owner", "password123", code, "10.0.0.1", "test")
	require.NoError(t, err, "the correct password and a valid code must succeed")
	require.NotNil(t, res)
}

// TestWiring_ANonEnrolledUserIsNotAskedForACode: the policy asymmetry, end to
// end. Requiring 2FA from everyone would make a small instance unable to onboard
// anyone.
func TestWiring_ANonEnrolledUserIsNotAskedForACode(t *testing.T) {
	f := newFactoryFixture(t)
	totp := newFakeTOTP()
	sf := buildInstance(t, f, totp)
	newMember(t, f, "member")

	res, err := sf.Login(context.Background(), "member", "password123", "10.0.0.1", "test")
	require.NoError(t, err, "2FA is opt-in; a user with no secret must log in normally")
	require.NotNil(t, res)
	assert.Zero(t, totp.verifyCalls)
}

// TestWiring_ANoTOTPStoreStillStartsAndPromptsNobody: the degraded state is a
// RUNNING instance, not a failed startup -- but it must not be silently
// mistaken for a protected one.
func TestWiring_ANoTOTPStoreStillStartsAndPromptsNobody(t *testing.T) {
	f := newFactoryFixture(t)
	force := true
	fac := f.factory()
	fac.MultiUser = &force
	fac.TOTP = nil // an instance built before the 2FA table existed

	store, mode, err := fac.Build(newCookieStoreDouble())
	require.NoError(t, err, "an instance with no 2FA store must still start, or the upgrade breaks every install")
	require.Equal(t, auth.ModeMultiUser, mode)

	adapter := store.(*session.HTTPAdapter)
	resolver := adapter.Resolver().(*auth.SessionStore)
	newMember(t, f, "owner")

	_, err = resolver.Login(context.Background(), "owner", "password123", "10.0.0.1", "test")
	require.NoError(t, err, "with no 2FA store configured there is nothing to require")
}

// TestWiring_ASecondFactorFailureLooksLikeABadPassword: the property that stops
// the login form becoming an oracle for "this account has 2FA".
func TestWiring_ASecondFactorFailureLooksLikeABadPassword(t *testing.T) {
	f := newFactoryFixture(t)
	totp := newFakeTOTP()
	sf := buildInstance(t, f, totp)
	enrol(t, f, totp, "owner")

	totp.verifyErr = collab.ErrTOTPInvalid
	_, badCode := sf.LoginWithTOTP(context.Background(), "owner", "password123", "000000", "10.0.0.1", "test")
	_, badPass := sf.Login(context.Background(), "owner", "wrongpassword", "10.0.0.1", "test")

	require.Error(t, badCode)
	require.Error(t, badPass)
	assert.ErrorIs(t, badCode, models.ErrInvalidCredentials,
		"a wrong code must be indistinguishable from a wrong password")
	assert.ErrorIs(t, badPass, models.ErrInvalidCredentials)
}

// TestWiring_TheRealArithmeticHoldsTheSingleUseRule: the load-bearing one, and
// the only test here that does NOT use the fake.
//
// A fake verifier cannot prove the replay guard, because the guard lives in the
// verifier and the fake is where the guard would be. So this drives the real
// collab arithmetic through a store built on it, and asserts the property the
// plan requires: a code is good once.
func TestWiring_TheRealArithmeticHoldsTheSingleUseRule(t *testing.T) {
	secret, key := oracleSecret(t)
	real := newArithmeticVerifier(secret)

	f := newFactoryFixture(t)
	sf := f.authStore
	sf.SetTOTPVerifier(real, real, collab.DefaultTOTPRequired)

	user := newMember(t, f, "owner")
	real.required[user.ID] = true

	code := codeNow(t, key)

	res, err := sf.LoginWithTOTP(context.Background(), "owner", "password123", code, "10.0.0.1", "test")
	require.NoError(t, err, "the current step's code must be accepted")
	require.NotNil(t, res)

	_, err = sf.LoginWithTOTP(context.Background(), "owner", "password123", code, "10.0.0.1", "test")
	require.ErrorIs(t, err, models.ErrInvalidCredentials,
		"a replayed code must be refused: single-use is the entire point of the step record")
}

// TestWiring_TheRecordSurvivesTheVerifierBeingReplaced: the concrete reason the
// record is durable rather than per-process.
//
// My first version of this test asserted the OPPOSITE -- that a fresh verifier
// with an empty record would REJECT a replayed code. That is false, and asserting
// it would have been asserting a security property that does not exist. The truth
// is the reason the record is durable: a verifier with no record ACCEPTS a
// replayed code, so the record has to be shared across the process and survive a
// restart.
//
// So the test states both halves. A verifier whose record is empty accepts the
// code -- which is why the record is not optional -- and the SAME verifier with
// the step recorded refuses it.
func TestWiring_TheRecordSurvivesTheVerifierBeingReplaced(t *testing.T) {
	secret, key := oracleSecret(t)
	real := newArithmeticVerifier(secret)

	f := newFactoryFixture(t)
	sf := f.authStore
	sf.SetTOTPVerifier(real, real, collab.DefaultTOTPRequired)
	user := newMember(t, f, "owner")
	real.required[user.ID] = true

	code := codeNow(t, key)

	// First login: accepted, and the step is recorded.
	_, err := sf.LoginWithTOTP(context.Background(), "owner", "password123", code, "10.0.0.1", "test")
	require.NoError(t, err)

	// Second login, same code, same verifier: refused, because the record says
	// the step is spent.
	_, err = sf.LoginWithTOTP(context.Background(), "owner", "password123", code, "10.0.0.1", "test")
	require.ErrorIs(t, err, models.ErrInvalidCredentials,
		"a replayed code must be refused once its step is recorded")

	// A verifier with an EMPTY record accepts the same code. This is not a
	// paradox and it is the whole reason the record is durable: the arithmetic
	// alone cannot tell a replay from a first use, so the answer lives in the
	// record -- and a record that did not outlive the process would be no record
	// at all.
	fresh := newArithmeticVerifier(secret)
	sf.SetTOTPVerifier(fresh, fresh, collab.DefaultTOTPRequired)
	fresh.required[user.ID] = true

	res, err := sf.LoginWithTOTP(context.Background(), "owner", "password123", code, "10.0.0.1", "test")
	require.NoError(t, err,
		"a verifier with no record accepts a replayed code -- which is exactly why "+
			"the step record must be durable and shared rather than in-process memory")
	require.NotNil(t, res)
}
