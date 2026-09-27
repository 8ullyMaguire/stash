package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/session"
	"github.com/stashapp/stash/pkg/txn"
)

// The authenticated user's numeric id, on the request context.
//
// M4 step 4.3. The media gate needs an int64 -- `libraries.user_id` and
// `user_library_access.user_id` are both integers -- and what the request
// context carried was a USERNAME STRING.
//
// # WHY THIS IS A MIDDLEWARE AND NOT A LOOKUP AT THE CALL SITE
//
// Every media route needs the id: the image routes, the scene stream and its
// HLS/DASH segments, the gallery and performer and studio and tag images, the
// download route, and /custom. Resolving it per route would be a username
// lookup in a dozen places, and each one is a chance to forget the
// disabled-user check or to answer from a different source than the session
// did.
//
// So it is resolved ONCE, here, from the same store and under the same rules. A
// route reads it and never looks a user up.
//
// # WHY THE USERNAME IS STILL WHAT THE CONTEXT CARRIES
//
// session.SetCurrentUserID stores a string because that slot is shared with
// upstream's signed-URL path, which resolves a username from a signature rather
// than a cookie, and changing that would touch every existing call site. So the
// string stays and this adds the id beside it.
//
// The consequence, and it is the one to remember: a request that arrived by
// SIGNED URL has a username and NO id. Such a request is not refused here --
// that would break AirPlay and Chromecast, which is the path the signed URL
// exists for -- but the media gate treats a missing id as "nobody", which on a
// public instance means an ungranted user and therefore a 404. §6.4 applied
// literally: a device that cannot present a cookie cannot present a grant
// either.
//
// That is a real narrowing of what signed URLs can do, and it is recorded here
// rather than left to be discovered. The alternative -- resolving the signature
// to an id and honouring it -- is a small change to the same middleware, and
// it is deliberately NOT taken in this step because it would give a capability
// that bypasses the grant check to a URL that is handed to a TV. Widening it
// belongs with a decision about what a signed URL is for.

type userIDKey struct{}

// requestUserID returns the authenticated user's numeric id, or 0 for an
// anonymous or signed-URL request.
//
// 0 and "no user" are the same value on purpose: there is no id 0 in the
// database (users.id is AUTOINCREMENT from 1), so 0 can never collide with a
// real user. A collision here would be the worst shape of bug in this file --
// one user's media served to another because of an off-by-one in a lookup.
func requestUserID(ctx context.Context) int64 {
	if v, ok := ctx.Value(userIDKey{}).(int64); ok {
		return v
	}
	return 0
}

// withRequestUserID resolves the username the authentication middleware already
// established into a numeric id, and puts it on the context.
//
// It reads the id inside its OWN read transaction, because the user store needs
// one and the request context does not carry a transaction of its own -- routes
// open theirs inside their handlers. So this opens a short one, reads, and
// closes it. Doing it here rather than per-route is what keeps it to one query
// per request instead of one per route.
//
// # THE FAILURE CASE
//
// If the store cannot be read, the id is left ABSENT and the request CONTINUES.
// Both halves matter:
//
//   - Continuing keeps the blast radius right. Refusing the whole request would
//     take the UI down as well as the media, turning one broken lookup into an
//     instance that looks completely dead. The media gate refuses on its own;
//     everything else behaves exactly as it did before this middleware existed.
//   - Absent is not zero-filled-and-guessed. Both mean "not authorised" to the
//     gate, so neither serves media -- but they are different things
//     operationally, and the log line below is how an operator tells them apart.
func withRequestUserID(txns models.TxnManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			username := session.GetCurrentUserID(r.Context())
			if username == nil || *username == "" || txns == nil {
				next.ServeHTTP(w, r)
				return
			}

			mgr := manager.MaybeGetInstance()
			if mgr == nil || mgr.UserStore == nil {
				next.ServeHTTP(w, r)
				return
			}

			var id int64
			var found bool
			err := txn.WithReadTxn(r.Context(), txns, func(ctx context.Context) error {
				user, err := mgr.UserStore.FindByUsername(ctx, *username)
				if err != nil {
					// models.ErrNotFound is an ANSWER, not a failure: a
					// single-user instance whose "username" is a config
					// value has no users row, and that is the normal state
					// of an install that never registered anybody. Treating
					// it as an error would log a database problem on every
					// request of every such install.
					if errors.Is(err, models.ErrNotFound) {
						return nil
					}
					return err
				}
				if user == nil {
					return nil
				}
				id = int64(user.ID)
				found = true
				return nil
			})

			if err != nil {
				logger.Errorf("stashforge: resolving the user id for %q: %v", *username, err)
				next.ServeHTTP(w, r)
				return
			}

			if !found {
				// No row for this username. Either a single-user install
				// (no users at all) or a session whose user was deleted
				// between the two reads. Either way: no id, so the gate
				// treats the caller as anonymous.
				next.ServeHTTP(w, r)
				return
			}

			ctx := context.WithValue(r.Context(), userIDKey{}, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
