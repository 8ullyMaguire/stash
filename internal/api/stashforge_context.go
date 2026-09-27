package api

import (
	"context"
	"net/http"
)

// contextKey is StashForge's private key type. Distinct from the exported
// contextKey the API package already uses for entity injection, so a value set
// here cannot collide with a scene or a gallery id.
type stashforgeContextKey int

const (
	// sfResponseWriter carries the http.ResponseWriter into resolvers.
	//
	// gqlgen v0.17 does not put the writer in the context by default, and the
	// login mutation genuinely needs one: the session cookie has to be written
	// to the response, and returning the session id in the payload instead
	// would put a live credential one XSS away from being readable from
	// JavaScript.
	sfResponseWriter stashforgeContextKey = iota

	// sfRequest carries the *http.Request, for the User-Agent and for Logout,
	// which must read the session cookie to know which session to destroy.
	sfRequest
)

// responseWriterContextMiddleware injects the ResponseWriter and the Request
// into the request context, so resolvers can set cookies and read headers.
//
// Registered on the GraphQL handler only. It is deliberately not applied to the
// whole router: putting a ResponseWriter in a context that outlives the handler
// is how a background job ends up writing to a recycled buffer.
func responseWriterContextMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ctx = context.WithValue(ctx, sfResponseWriter, w)
		ctx = context.WithValue(ctx, sfRequest, r.WithContext(ctx))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func responseWriter(ctx context.Context) http.ResponseWriter {
	w, _ := ctx.Value(sfResponseWriter).(http.ResponseWriter)
	return w
}

func requestFrom(ctx context.Context) *http.Request {
	r, _ := ctx.Value(sfRequest).(*http.Request)
	return r
}
