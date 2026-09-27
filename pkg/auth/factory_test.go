package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/auth"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/session"
)

func TestFactory_LegacyInstallKeepsCookieStore(t *testing.T) {
	// No users in the database: a fresh or pre-fork install. It must keep
	// upstream's exact authentication behaviour.
	f := newFactoryFixture(t)
	// A legacy install: the credentials live in config, and the users table is
	// empty because no account has ever been created here.
	cookie := newCookieStoreDouble()

	store, mode, err := f.factory().Build(cookie)
	require.NoError(t, err)
	assert.Equal(t, auth.ModeSingleUser, mode)
	assert.Same(t, cookie, store,
		"a legacy install must keep the cookie store, not be silently promoted")
}

func TestFactory_EmptyUserTableStaysSingleUser(t *testing.T) {
	// The most important case in this file. A table that exists but is empty
	// means nobody has opted in yet, so the instance stays single-user --
	// promoting it would lock the config user out of their own library.
	f := newFactoryFixture(t)
	cookie := &session.HTTPAdapter{CookieName: "stash"}

	store, mode, err := f.factory().Build(cookie)
	require.NoError(t, err)
	assert.Equal(t, auth.ModeSingleUser, mode)
	assert.Same(t, cookie, store)
}

func TestFactory_PopulatedUserTablePromotesToMultiUser(t *testing.T) {
	// Once accounts exist, the mode is inferred from them, so there is no state
	// in which the config says one thing and the table says another.
	f := newFactoryFixture(t)
	f.users.add(&models.User{Username: "alice"}, "x")

	cookie := &session.HTTPAdapter{CookieName: "stash"}
	store, mode, err := f.factory().Build(cookie)
	require.NoError(t, err)
	assert.Equal(t, auth.ModeMultiUser, mode)
	assert.NotSame(t, session.Store(cookie), store, "the cookie store must be replaced")
	assert.NotNil(t, store)
}

func TestFactory_ExplicitOverrideWins(t *testing.T) {
	f := newFactoryFixture(t)
	f.users.add(&models.User{Username: "alice"}, "x")

	// Force single-user despite a populated table: the escape hatch for an
	// operator whose table has leftover rows.
	off := false
	fac := f.factory()
	fac.MultiUser = &off
	cookie := newCookieStoreDouble()

	_, mode, err := fac.Build(cookie)
	require.NoError(t, err)
	assert.Equal(t, auth.ModeSingleUser, mode)
}

func TestFactory_MissingDependencyIsAnErrorNotANoOp(t *testing.T) {
	// Each of these must fail loudly. Defaulting a nil repository to a no-op
	// would produce an instance that appears to work and records no audit
	// trail, which is the exact failure this project exists to prevent.
	tests := []struct {
		name  string
		build func(f *auth.Factory)
		want  string
	}{
		{"Users", func(f *auth.Factory) { f.Users = nil }, "Users"},
		{"Sessions", func(f *auth.Factory) { f.Sessions = nil }, "Sessions"},
		{"Invites", func(f *auth.Factory) { f.Invites = nil }, "Invites"},
		{"Audit", func(f *auth.Factory) { f.Audit = nil }, "Audit"},
		{"Config", func(f *auth.Factory) { f.Config = nil }, "Config"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newFactoryFixture(t)
			fixture.users.add(&models.User{Username: "alice"}, "x")

			f := fixture.factory()
			tt.build(f)
			// Force multi-user so the DEPENDENCY CHECK is what is under test.
			// Without it, a nil Users store resolves the mode to single-user and
			// returns the cookie store before reaching the switch, so the
			// subtest would pass for the wrong reason.
			on := true
			f.MultiUser = &on

			store, _, err := f.Build(newCookieStoreDouble())
			require.Error(t, err, "a missing %s must be an error", tt.name)
			assert.Contains(t, err.Error(), tt.want)

			// The returned store must be a TRUE nil interface, not a typed nil
			// pointer wrapped in one. assert.Nil is not enough here: testify's
			// isNil unwraps a typed nil and reports it as nil, so a
			// (*session.HTTPAdapter)(nil) returned as session.Store passes
			// assert.Nil and then panics at the caller's first method call.
			// This is the comparison a caller actually writes.
			assert.True(t, store == nil,
				"the error path must return a true nil interface, got %T", store)
		})
	}
}

func TestFactory_DatabaseFailureDoesNotFallBackToSingleUser(t *testing.T) {
	// If the user count cannot be read, the instance must fail. Falling back to
	// single-user would authenticate every visitor as the one shared config
	// user, which is the opposite of the safe direction.
	fixture := newFactoryFixture(t)
	fixture.users.countErr = errors.New("disk gone")

	_, mode, err := fixture.factory().Build(&session.HTTPAdapter{CookieName: "stash"})
	require.Error(t, err)
	assert.Equal(t, auth.ModeSingleUser, mode, "the mode is meaningless on error")
}

func TestFactory_ForcedMultiUserWithNoUsersStillChecksDependencies(t *testing.T) {
	// Forcing the mode must not skip validation. "Force" is about choosing the
	// mode, not about waiving the requirements.
	fixture := newFactoryFixture(t)
	on := true
	f := fixture.factory()
	f.MultiUser = &on
	f.Audit = nil

	_, _, err := f.Build(&session.HTTPAdapter{CookieName: "stash"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Audit")
}

func TestFactory_MultiUserStoreAuthenticates(t *testing.T) {
	// End-to-end through the factory: an account in the table logs in and its
	// session is accepted by the adapter the factory returned. This is the test
	// that would fail if the factory wired the wrong thing up.
	fixture := newFactoryFixture(t)
	u := fixture.users.add(&models.User{Username: "alice"}, "alicepass")

	store, mode, err := fixture.factory().Build(&session.HTTPAdapter{CookieName: "stash"})
	require.NoError(t, err)
	require.Equal(t, auth.ModeMultiUser, mode)

	ms, ok := store.(*session.HTTPAdapter)
	require.True(t, ok, "multi-user mode must return the adapter, got %T", store)

	res, err := fixture.authStore.Login(context.Background(), "alice", "alicepass", "127.0.0.1", "test-agent")
	require.NoError(t, err)
	require.Equal(t, u.ID, res.UserID)

	// The cookie the API layer would set on a real login.
	w := httptest.NewRecorder()
	fixture.authStore.SetSessionCookie(w, res)

	r := httptest.NewRequest("GET", "/", nil)
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}

	user, err := ms.Authenticate(nil, r)
	require.NoError(t, err, "a freshly created session must authenticate")
	assert.Equal(t, "alice", user)
}

func TestFactory_MultiUserSessionRejectsUnknownCookie(t *testing.T) {
	fixture := newFactoryFixture(t)
	fixture.users.add(&models.User{Username: "alice"}, "alicepass")

	store, _, err := fixture.factory().Build(&session.HTTPAdapter{CookieName: "stash"})
	require.NoError(t, err)

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "stashforge_session", Value: "not-a-real-session-id"})

	_, err = store.Authenticate(nil, r)
	assert.Error(t, err, "an unknown session id must not authenticate")
}

func TestMode_String(t *testing.T) {
	assert.Equal(t, "single-user", auth.ModeSingleUser.String())
	assert.Equal(t, "multi-user", auth.ModeMultiUser.String())
}
