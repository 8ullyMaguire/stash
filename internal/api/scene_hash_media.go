package api

// Splitting `/scene/<hash>_thumbs.vtt` into its hash and its media kind.
//
// # WHY THIS EXISTS
//
// The two sprite URLs put the suffix in the SAME path segment as the hash:
//
//	internal/api/urlbuilders/scene.go:49   BaseURL + "/scene/" + checksum + "_thumbs.vtt"
//	internal/api/urlbuilders/scene.go:53   BaseURL + "/scene/" + checksum + "_sprite.jpg"
//
// There is no separating slash, which is the whole difficulty. chi cannot match a suffix inside a
// segment: `r.Get("_thumbs.vtt", ...)` panics with
//
//	chi: routing pattern must begin with '/' in '_thumbs.vtt'
//
// and `r.Get("/{sceneHash}_thumbs.vtt", ...)` does not work either -- chi treats `{name}` as
// spanning a whole segment, so a pattern with literal text after the brace never matches. So the
// route registers a wildcard and the suffix is interpreted here.
//
// This split was previously missing entirely, which is a second bug behind the panic: with the
// route unfixed the panic fired first, and once the route was fixed the middleware would still have
// looked a scene up by the literal string "<hash>_thumbs.vtt" and 404'd every sprite request. A
// wrong value that still looks like a hash is the kind of defect that survives a test asserting
// "the hash parameter is non-empty".

import "strings"

const (
	vttSuffix    = "_thumbs.vtt"
	spriteSuffix = "_sprite.jpg"
)

// sceneMediaKey carries the media kind from sceneHashCtx to VttSceneMedia. A request-private context
// value rather than a package variable: the two concurrent sprite requests for different scenes must
// not be able to see each other's kind.
type sceneMediaCtxKey struct{}

// sceneMediaKey is the context key. A distinct zero-size type, so it cannot collide with any other
// context key in the package -- including one a future file adds by accident.
var sceneMediaKey = sceneMediaCtxKey{}

// splitSceneHashMedia separates a scene-hash media path into the hash and which of the two media
// kinds it names.
//
// ok is false for a segment that carries no recognised suffix. That is a 404 rather than a
// fallback: a URL shape this function does not understand must not silently resolve to the
// thumbnail strip, because "the sprite URL returned a valid VTT" is exactly the kind of wrong that
// survives a smoke test.
//
// Order matters. `_sprite.jpg` is checked first even though neither suffix is a prefix of the
// other, because the day a third suffix shares a prefix with one of these, "first match wins" is
// the rule that decides whether the new kind works or is silently unreachable.
func splitSceneHashMedia(segment string) (hash string, media string, ok bool) {
	if i := strings.LastIndex(segment, spriteSuffix); i >= 0 {
		return segment[:i], spriteSuffix, true
	}
	if i := strings.LastIndex(segment, vttSuffix); i >= 0 {
		return segment[:i], vttSuffix, true
	}
	return "", "", false
}
