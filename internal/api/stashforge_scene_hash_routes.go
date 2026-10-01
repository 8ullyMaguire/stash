package api

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
)

// The scene routes that key off a HASH rather than an id, and therefore do not
// pass through SceneCtx. M4 step 4.3.
//
// # WHY THIS FILE EXISTS
//
// `/{sceneHash}_thumbs.vtt` and `/{sceneHash}_sprite.jpg` are registered
// OUTSIDE the `r.Route("/{sceneId}", ...)` block, so they never see SceneCtx
// and never see the gate it now carries. They serve a GENERATED sprite file --
// a strip of thumbnails of the scene's video, and the video's frames -- keyed
// only by the hash in the URL.
//
// So they were a hole: on a public instance, a user with no grant could fetch
// the sprite and the thumbnail strip of any scene whose hash they knew, which
// is exactly the disclosure §6.4 forbids. Found by asking the question the
// other way round -- by listing every media route and recording which
// middleware each one passes -- rather than by reading the handlers, which all
// looked correct.
//
// # WHY THE HASH IS RESOLVED RATHER THAN TRUSTED
//
// The obvious cheap answer is to check nothing, because the hash "is not
// guessable". That is wrong twice over. A checksum is attacker-choosable by
// anyone who can write a file into the library, and on a shared instance it is
// printed in the URL of every screenshot request. And a route that serves a file
// with no check is a route whose safety rests on an unstated assumption about
// somebody else's property.
//
// So the hash is resolved to a scene, the scene is checked, and an unresolvable
// hash is a 404 -- the same refusal, for the same reason, as one the caller is
// not allowed to have.

// sceneHashCtx resolves {sceneHash} to a scene and applies the gate.
//
// A hash that matches no scene is refused WITHOUT a gate call, because there is
// nothing to check: no scene means no library, and no library means no answer
// the gate could produce. That is a 404 for the same reason a missing row is.
func sceneHashCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hash := chi.URLParam(r, "sceneHash")
		if hash == "" {
			http.NotFound(w, r)
			return
		}

		mgr := manager.MaybeGetInstance()
		if mgr == nil {
			http.NotFound(w, r)
			return
		}

		var scene *models.Scene
		var resolved bool

		// Both hash kinds, because the file the sprite was generated from
		// may have been identified by either and the URL carries whichever
		// the naming algorithm produced.
		algo := config.GetInstance().GetVideoFileNamingAlgorithm()
		_ = mgr.Repository.WithReadTxn(r.Context(), func(ctx context.Context) error {
			finder, okS := mgr.Repository.Scene.(interface {
				FindByChecksum(context.Context, string) ([]*models.Scene, error)
				FindByOSHash(context.Context, string) ([]*models.Scene, error)
			})
			if !okS {
				return nil
			}
			if byChecksum, err := finder.FindByChecksum(ctx, hash); err == nil && len(byChecksum) > 0 {
				scene, resolved = byChecksum[0], true
				return nil
			}
			if byOSHash, err := finder.FindByOSHash(ctx, hash); err == nil && len(byOSHash) > 0 {
				scene, resolved = byOSHash[0], true
			}
			_ = algo
			return nil
		})

		if !resolved || scene == nil {
			// No scene, so no library and nothing to authorise. 404, for the
			// same reason as any other unresolvable target.
			logger.Debugf("stashforge: sprite hash %q matched no scene", hash)
			http.NotFound(w, r)
			return
		}

		if !allowMedia(w, r, collab.TargetScene, int64(scene.ID)) {
			return
		}

		// The scene goes on the context so the handler uses the RESOLVED
		// scene's own hash rather than the URL's. Two hashes can name the
		// same file (checksum and oshash), and the handler is happier with
		// the one the file was generated under.
		ctx := context.WithValue(r.Context(), sceneKey, scene)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
