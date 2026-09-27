package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/pkg/auth"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/session"
)

// errAccountsDisabled is returned on a single-user instance. It exists so the
// message can say WHY, which is the difference between "not available on your
// instance" and "something went wrong".
var errAccountsDisabled = errors.New(
	"this instance is running in single-user mode and has no accounts")

// currentUserID returns the authenticated username, or "" when anonymous.
//
// Anonymous is the normal state of most requests, so this is not an error.
func currentUserID(ctx context.Context) string {
	if u := session.GetCurrentUserID(ctx); u != nil {
		return *u
	}
	return ""
}

// requestIP returns the caller's IP, or "" when the middleware did not run.
// Never fatal: a missing IP must not stop a login, it only makes the audit row
// less useful, and refusing the request over a missing log field would be a
// self-inflicted outage.
func requestIP(ctx context.Context) string {
	if ip, err := getRequestIPFromCtx(ctx); err == nil {
		return ip.String()
	}
	return ""
}

// sessionStoreForLogout returns the raw session.Store, which is all Logout
// needs: it destroys the session the cookie names.
func sessionStoreForLogout() session.Store {
	return manager.GetInstance().SessionStore
}

// requireMultiUser returns the multi-user auth service, or an error on a
// single-user instance.
//
// A type assertion rather than a config lookup on purpose: the factory's chosen
// mode is the source of truth, and asking it a second, different question
// invites the two to disagree — which is precisely the class of bug that locked
// out an existing install earlier in this milestone.
func requireMultiUser() (*auth.SessionStore, error) {
	svc := manager.GetInstance().Auth
	if svc == nil {
		return nil, errAccountsDisabled
	}
	return svc, nil
}

// me resolves the currently authenticated user.
//
// A session row outliving its user, or a single-user instance where the
// "username" is a config value with no row, both resolve to nil rather than an
// error: the caller is anonymous, and that is a normal answer.
func (r *queryResolver) Me(ctx context.Context) (*models.User, error) {
	username := currentUserID(ctx)
	if username == "" {
		return nil, nil
	}

	user, err := manager.GetInstance().UserStore.FindByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return user, nil
}

// register creates an account and logs it in.
//
// The invite key is required on any instance that already has an account. On an
// instance with none, the first registration needs no key — otherwise a fresh
// install could never be bootstrapped, since there is nobody to issue one. That
// rule is enforced inside pkg/auth (which can count users), not here, so it
// lives with the code that has to be right about it.
func (r *mutationResolver) Register(ctx context.Context, input RegisterInput) (*AuthPayload, error) {
	svc, err := requireMultiUser()
	if err != nil {
		return nil, err
	}

	w := responseWriter(ctx)
	rq := requestFrom(ctx)

	// The key is optional in the schema and a plain string in the store; "" is
	// its absent form. An empty string is not a valid key, so the two are
	// indistinguishable downstream, which is what the store's bootstrap rule
	// needs.
	inviteKey := ""
	if input.InviteKey != nil {
		inviteKey = *input.InviteKey
	}

	user, err := svc.Register(ctx, normaliseUsername(input.Username), input.Password,
		inviteKey, requestIP(ctx), userAgentOf(rq))
	if err != nil {
		return nil, err
	}

	// A new account arrives with a session already established. Without this
	// the user sees a success message while still anonymous, which reads as a
	// failure to anyone who has registered on a normal site before.
	if err := svc.EstablishSessionForNewUser(w, rq, user.ID); err != nil {
		return nil, err
	}

	return &AuthPayload{User: user}, nil
}

// login authenticates and sets the session cookie.
//
// The session id is never in the response body. SetSessionCookie writes it
// HttpOnly, so a page rendering this response cannot read the session out of the
// DOM, out of localStorage, or out of a JavaScript variable — which is the
// difference between an XSS and a session theft.
func (r *mutationResolver) Login(ctx context.Context, input LoginInput) (*AuthPayload, error) {
	svc, err := requireMultiUser()
	if err != nil {
		return nil, err
	}

	w := responseWriter(ctx)
	rq := requestFrom(ctx)

	res, err := svc.Login(ctx, normaliseUsername(input.Username), input.Password,
		requestIP(ctx), userAgentOf(rq))
	if err != nil {
		// Every failure mode collapses to ErrInvalidCredentials inside
		// pkg/auth so this form cannot enumerate accounts. The GraphQL layer
		// must not re-introduce a distinction it was given in order to hide.
		return nil, err
	}

	svc.SetSessionCookie(w, res)

	user, err := manager.GetInstance().UserStore.Find(ctx, res.UserID)
	if err != nil {
		return nil, err
	}
	return &AuthPayload{User: user}, nil
}

// logout destroys the session and clears the cookie.
func (r *mutationResolver) Logout(ctx context.Context) (bool, error) {
	if currentUserID(ctx) == "" {
		// Logging out when already anonymous is a success, not a failure: the
		// caller wanted to be logged out, and they are.
		return true, nil
	}

	// No requireMultiUser here: a single-user instance's cookie store also has
	// a Logout, and refusing it would leave the login page unable to clear its
	// own cookie.
	if err := sessionStoreForLogout().Logout(responseWriter(ctx), requestFrom(ctx)); err != nil {
		return false, err
	}
	return true, nil
}

// normaliseUsername trims and lowercases, so " Alice " and "alice" are one
// account. The column is COLLATE NOCASE so lookups are case-insensitive either
// way; this is about not storing two shapes of the same name.
//
// Deliberately NOT applied to the password: leading and trailing spaces are
// legitimate password characters and silently stripping them makes a correct
// password fail with no way for the user to tell why.
func normaliseUsername(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func userAgentOf(r *http.Request) string {
	if r == nil {
		return ""
	}
	ua := r.UserAgent()
	if len(ua) > 255 {
		ua = ua[:255]
	}
	return ua
}
