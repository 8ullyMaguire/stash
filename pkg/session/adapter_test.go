package session_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/session"
)

var errNoSession = errors.New("no session")

// fakeResolver stands in for the database-backed auth store.
type fakeResolver struct {
	username string
	err      error
	calls    int
}

func (f *fakeResolver) ResolveRequest(ctx context.Context, r *http.Request) (string, error) {
	f.calls++
	return f.username, f.err
}

func newAdapter() (*session.HTTPAdapter, *fakeResolver) {
	res := &fakeResolver{username: "alice"}
	return session.NewHTTPAdapter(res, "stashforge_session", 3600), res
}

func TestHTTPAdapter_AuthenticateReturnsUsername(t *testing.T) {
	a, res := newAdapter()
	r := httptest.NewRequest("GET", "/", nil)

	user, err := a.Authenticate(httptest.NewRecorder(), r)
	require.NoError(t, err)
	assert.Equal(t, "alice", user)
	assert.Equal(t, 1, res.calls)
}

func TestHTTPAdapter_GetSessionUserIDIsAnonymousOnFailure(t *testing.T) {
	a, _ := newAdapter()
	res := &fakeResolver{err: errNoSession}
	a = session.NewHTTPAdapter(res, "stashforge_session", 3600)

	// "Not logged in" is the normal state of a request. Returning an error
	// here would make every anonymous request a 401 instead of only
	// authenticated ones; Authenticate is the method that turns it into one.
	user, err := a.GetSessionUserID(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	assert.NoError(t, err)
	assert.Empty(t, user, "an unauthenticated request must be anonymous, not an error")
}

func TestHTTPAdapter_LogoutClearsCookie(t *testing.T) {
	a, _ := newAdapter()
	w := httptest.NewRecorder()

	require.NoError(t, a.Logout(w, httptest.NewRequest("POST", "/logout", nil)))

	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1, "logout must write exactly one cookie")
	assert.Equal(t, "stashforge_session", cookies[0].Name)
	assert.Empty(t, cookies[0].Value)
	assert.Negative(t, cookies[0].MaxAge, "the cookie must be expired, not merely blanked")
	assert.True(t, cookies[0].HttpOnly, "the session cookie must stay HttpOnly")
}

func TestHTTPAdapter_LoginIsRefusedNotPanicked(t *testing.T) {
	a, _ := newAdapter()
	// The database store establishes sessions through the auth layer. If a
	// future refactor routes a login here, an error is the right outcome -- a
	// panic on the login page is not.
	err := a.Login(httptest.NewRecorder(), httptest.NewRequest("POST", "/login", nil))
	assert.ErrorIs(t, err, session.ErrLoginNotSupported)
}

func TestHTTPAdapter_MakePluginCookieIsNil(t *testing.T) {
	a, _ := newAdapter()
	// A nil cookie tells the plugin cache there is no per-session state, so it
	// skips the Set-Cookie header rather than emitting an empty one.
	assert.Nil(t, a.MakePluginCookie(context.Background()))
}

func TestHTTPAdapter_VisitedPluginHandlerPassesThrough(t *testing.T) {
	a, _ := newAdapter()
	called := 0
	h := a.VisitedPluginHandler()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	assert.Equal(t, 1, called, "the handler must reach the wrapped handler")
}

// The default cookie name must never be empty: an empty name means the adapter
// writes a cookie the browser cannot reliably replace.
func TestNewHTTPAdapter_DefaultsCookieName(t *testing.T) {
	a := session.NewHTTPAdapter(&fakeResolver{}, "", 0)
	assert.Equal(t, session.DefaultSessionCookieName, a.CookieName)
	assert.NotEmpty(t, a.CookieName)
}
