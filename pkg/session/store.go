package session

import (
	"context"
	"net/http"
)

// Store is the authentication surface the rest of the application depends on.
//
// It is an interface, not the concrete cookie store, so StashForge can supply a
// database-backed multi-user implementation without touching the call sites:
//
//	internal/api/authentication.go:92   Authenticate
//	internal/api/session.go:126,151      Login, Logout
//	internal/api/server.go:129           VisitedPluginHandler
//	pkg/plugin/plugins.go:126,245        RegisterSessionStore, MakePluginCookie
//
// GetSessionUserID is in the interface even though only Authenticate calls it
// (internally, on the cookie store), because Authenticate's contract is defined
// in terms of it: an implementation that cannot answer the question directly is
// one that will be tempted to fake the answer inside Authenticate.
//
// The two plugin methods are here because internal/api/server.go installs
// VisitedPluginHandler unconditionally and the plugin cache calls
// MakePluginCookie on every hook execution. A multi-user store that quietly
// dropped them would break plugin hook re-entry for every user, not only for
// accounts created after the change -- the kind of regression that only shows
// up in production and only for plugins.
//
// The concrete cookie store remains the implementation for an install with no
// user database, so a legacy single-password instance keeps working untouched.
type Store interface {
	// Login authenticates a request's credentials and establishes a session.
	// It returns *InvalidCredentialsError on a bad username or password --
	// deliberately indistinguishable between the two, so the login form cannot
	// be used to enumerate accounts.
	Login(w http.ResponseWriter, r *http.Request) error

	// Logout destroys the current session.
	Logout(w http.ResponseWriter, r *http.Request) error

	// GetSessionUserID returns the currently authenticated user's identifier,
	// or "" when there is none. A missing or expired session is not an error:
	// it is the normal state of an anonymous request.
	GetSessionUserID(w http.ResponseWriter, r *http.Request) (string, error)

	// Authenticate resolves the caller, returning ErrUnauthorized when the
	// request carries no valid credential. That is the common case, not a
	// fault, and the HTTP layer turns it into a 401.
	Authenticate(w http.ResponseWriter, r *http.Request) (string, error)

	// VisitedPluginHandler is middleware carrying the visited-plugin-hooks
	// cookie through to plugin execution.
	VisitedPluginHandler() func(http.Handler) http.Handler

	// MakePluginCookie builds the cookie a plugin uses to record that it has
	// already run for this session.
	MakePluginCookie(ctx context.Context) *http.Cookie
}

// Compile-time proof that the existing cookie store satisfies the interface it
// is being abstracted behind. This is the check that makes the refactor safe:
// if a method is renamed or its signature drifts, this fails at build time
// rather than at the first login attempt in production.
var _ Store = (*CookieStore)(nil)
