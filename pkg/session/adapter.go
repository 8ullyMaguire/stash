package session

import (
	"context"
	"errors"
	"net/http"
)

// HTTPAdapter presents a SessionResolver as a Store.
//
// # Why this exists
//
// Stash's Store methods take an http.ResponseWriter because the cookie store
// has to write and refresh the session cookie on every authenticated request.
// A database-backed store keeps no cookie state -- it reads an opaque id and
// looks up a row -- so its natural signature is (ctx, request) with no writer.
// Changing Store to drop the writer would mean touching the cookie
// implementation, which is the one thing that must keep working byte for byte
// on a legacy install.
//
// So the writer stays on the interface, and this adapter is where the two meet.
// It also owns the StashForge session cookie, which is the only HTTP concern the
// database store actually has: setting it on login and clearing it on logout.
type HTTPAdapter struct {
	resolver SessionResolver

	// CookieName is the cookie carrying the opaque session id.
	CookieName string
	// MaxAge is the session lifetime in seconds, used when the resolver hands
	// back a result without an explicit expiry.
	MaxAge int
}

// SessionResolver is the request-shaped authentication interface a
// database-backed store implements. It has no http.ResponseWriter because it
// never writes a response.
type SessionResolver interface {
	// ResolveRequest authenticates the request and returns the username, or
	// ErrUnauthorized.
	ResolveRequest(ctx context.Context, r *http.Request) (string, error)
}

// Authenticate resolves the caller. The writer is accepted to satisfy Store and
// deliberately unused: this store's cookie is set once at login, not refreshed
// per request, so there is nothing to write here.
func (a *HTTPAdapter) Authenticate(w http.ResponseWriter, r *http.Request) (string, error) {
	return a.resolver.ResolveRequest(r.Context(), r)
}

// GetSessionUserID returns the authenticated username, or "" when anonymous.
// An unauthorized result is not an error here: "not logged in" is the normal
// state of a request, and Authenticate is the method that turns it into a 401.
func (a *HTTPAdapter) GetSessionUserID(w http.ResponseWriter, r *http.Request) (string, error) {
	user, err := a.resolver.ResolveRequest(r.Context(), r)
	if err != nil {
		return "", nil
	}
	return user, nil
}

// Login is not implemented by the adapter: establishing a session needs a
// username and password, which the HTTP layer collects and validates before
// calling the store's own Login. The API handler for multi-user mode calls
// auth.SessionStore.Login directly and then uses SetSessionCookie, so this
// method exists only to satisfy the interface and must never be reached.
//
// Returning an error rather than panicking means that if a future refactor does
// route a login here, the failure is a clear message in the log rather than a
// crash on the login page.
func (a *HTTPAdapter) Login(w http.ResponseWriter, r *http.Request) error {
	return ErrLoginNotSupported
}

// Logout clears the browser cookie. Invalidating the server-side row is the
// resolver's job; this only stops the browser presenting the id again.
func (a *HTTPAdapter) Logout(w http.ResponseWriter, r *http.Request) error {
	a.ClearSessionCookie(w)
	return nil
}

// VisitedPluginHandler passes the request through untouched.
//
// Upstream's cookie store carries a visited-plugin-hooks cookie so a plugin
// does not re-run its hook on every request. That state lives in the signed
// cookie alongside the user id, and a database-backed session has no such
// cookie to read.
//
// Rather than fabricate an empty plugin state -- which would make every plugin
// hook re-execute on every request, with unknown side effects for hooks that
// write to the database -- this is a no-op pass-through. Plugin hook
// de-duplication therefore does not apply in multi-user mode. That is a real
// behavioural difference and it is recorded as a known gap rather than papered
// over; M2 revisits it, because the governance rules depend on a proposal hook
// running exactly once.
func (a *HTTPAdapter) VisitedPluginHandler() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return next
	}
}

// Resolver returns the underlying request-shaped resolver, or nil.
//
// The GraphQL register/login resolvers need Register and Login, which the
// Store interface deliberately does not expose (a session store that could
// create accounts would be a much larger thing to trust). Exposing the
// resolver behind a nil-safe accessor keeps that boundary: the caller must
// check for nil, which is what requireMultiUser in internal/api does.
func (a *HTTPAdapter) Resolver() SessionResolver {
	if a == nil {
		return nil
	}
	return a.resolver
}

// MakePluginCookie returns nil. A nil cookie tells the plugin cache there is no
// per-session state to carry, and the cache skips writing a Set-Cookie header
// rather than emitting an empty one.
func (a *HTTPAdapter) MakePluginCookie(ctx context.Context) *http.Cookie {
	return nil
}

// ClearSessionCookie expires the session cookie in the browser.
func (a *HTTPAdapter) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// NewHTTPAdapter wraps a request-shaped resolver as a Store.
//
// Constructed here rather than with a struct literal so the unexported resolver
// field stays unexported: a caller in another package must not be able to
// assemble an adapter with a nil resolver, which would authenticate nobody and
// look like a working install.
func NewHTTPAdapter(resolver SessionResolver, cookieName string, maxAgeSeconds int) *HTTPAdapter {
	if cookieName == "" {
		cookieName = DefaultSessionCookieName
	}
	return &HTTPAdapter{
		resolver:   resolver,
		CookieName: cookieName,
		MaxAge:     maxAgeSeconds,
	}
}

// DefaultSessionCookieName is the StashForge session cookie. Distinct from
// Stash's own cookie on purpose: if both are present during a migration, one
// install cannot silently read the other's identity.
const DefaultSessionCookieName = "stashforge_session"

// ErrLoginNotSupported is returned by HTTPAdapter.Login.
var ErrLoginNotSupported = errors.New(
	"multi-user sessions establish their session through the auth store, not the session store")

var _ Store = (*HTTPAdapter)(nil)
