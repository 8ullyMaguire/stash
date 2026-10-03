package api

// Deciding, per request, whether `/scene/<segment>` names a scene by ID or a scene's sprite media
// by hash.
//
// # WHY THIS FILE EXISTS
//
// stash serves two families of URL whose first path segment has a completely different meaning:
//
//	/scene/42/stream              42 is a scene ID
//	/scene/<hash>_thumbs.vtt      <hash> is a file checksum, and the suffix is part of the SAME
//	                              segment (internal/api/urlbuilders/scene.go:49)
//
// chi routes one segment to one param, and refuses to mount two param routes on the same segment
// ("attempting to Mount() a handler on an existing path"). So these cannot be two routes. They are
// one route, and the meaning of the segment is decided here.
//
// That decision was previously implicit and wrong in a way that took the whole instance down:
//
//	r.Route("/{sceneId}", ...) { r.Use(SceneCtx) ... }
//	r.Route("/{sceneHash}*", ...) { r.Use(sceneHashCtx); r.Get("_thumbs.vtt", ...) }
//
// The second block's inner pattern had no leading slash, so chi panicked inside
// internal/api.Initialize -- after migrations had run, on every startup, for every user:
//
//	chi: routing pattern must begin with '/' in '_thumbs.vtt'
//
// Nothing in the unit suite caught it because no unit test builds the real chi router with both
// blocks in place, and the coverage test that did look at these routes
// (stashforge_media_gate_coverage_test.go) asserted the buggy source TEXT, so it was asserting the
// bug. It is updated in that file.
//
// # THE ORDER MATTERS
//
// The media check runs FIRST. A hash is a hex checksum, so it is not parseable as an ID and SceneCtx
// would 404 it -- but the reverse is not true of the decision: checking for a suffix cannot
// misclassify an id, because an id never carries `_thumbs.vtt`. Getting this backwards would mean a
// scene id that somehow ended in a media suffix skipped the library gate, which is the one thing
// sceneHashCtx exists to prevent.

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// sceneSegmentCtx is the middleware for /scene/{sceneId}. It applies SceneCtx or sceneHashCtx,
// whichever the segment calls for.
//
// It must run BEFORE SceneCtx in the chain, which is why it replaced SceneCtx on the route rather
// than sitting in front of it: SceneCtx parses sceneId as an integer and 404s a hash, so it can
// never see a media request.
func (rs sceneRoutes) sceneSegmentCtx(next http.Handler) http.Handler {
	mediaNext := sceneHashCtx(http.HandlerFunc(rs.sceneSegmentRoot))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSceneMediaSegment(chi.URLParam(r, "sceneId")) {
			mediaNext.ServeHTTP(w, r)
			return
		}
		rs.SceneCtx(next).ServeHTTP(w, r)
	})
}

// sceneSegmentRoot serves the bare `GET /scene/<segment>` -- which is ONLY ever a sprite media
// request, since an ordinary scene id is always followed by a sub-path (`/stream`, `/screenshot`,
// `/funscript`, ...). It exists so sceneSegmentCtx has a handler to hand a media request to, keeping
// the dispatch in one place instead of spreading it across route registrations.
func (rs sceneRoutes) sceneSegmentRoot(w http.ResponseWriter, r *http.Request) {
	media, _ := r.Context().Value(sceneMediaKey).(string)

	switch sceneMediaHandlerFor(media) {
	case "sprite":
		rs.VttSprite(w, r)
	case "vtt":
		rs.VttThumbs(w, r)
	default:
		// A bare /scene/<id> with no sub-path. Upstream has no such URL, so there is nothing to
		// serve; 404 rather than guessing.
		http.NotFound(w, r)
	}
}

// sceneMediaHandlerFor is the media-kind decision, as its own function so the dispatcher and the
// tests read the SAME table rather than each holding a copy of it.
//
// Returning a name rather than a handler is deliberate: the handlers reach manager.GetInstance(),
// which panics without a manager, so a test cannot invoke them. What a test CAN check -- and what
// actually goes wrong -- is which handler each suffix reaches, so that is what this returns.
func sceneMediaHandlerFor(media string) string {
	switch media {
	case spriteSuffix:
		return "sprite"
	case vttSuffix:
		return "vtt"
	default:
		return ""
	}
}

// isSceneMediaSegment reports whether a /scene/ first segment names sprite media rather than an id.
func isSceneMediaSegment(segment string) bool {
	_, _, ok := splitSceneHashMedia(segment)
	return ok
}
