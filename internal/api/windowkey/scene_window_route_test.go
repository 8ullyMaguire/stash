package windowkey

// stash#3530 — the window-aware generated key must be used AT THE CALL SITES, not merely defined.
//
// This is a TEST-ONLY package, holding nothing but this file, and that is deliberate on two counts.
//
// It reads source files, so it must not live in internal/api: a test that belongs to no package is
// the cheapest thing in the tree to build, and internal/api drags in the whole gqlgen resolver --
// which on a loaded machine is the difference between seconds and half an hour.
//
// And it keeps the standard rule honest: a check on a package does not belong to that package.
//
// Found by mutation: reverting `routes_scene.go`'s Preview handler from GeneratedChecksum back to
// GetHash changed no test result. Every test for the key lived in pkg/models, and pkg/models cannot
// see internal/api -- so the function was thoroughly tested and the call site that uses it was not
// tested at all. A helper tested in isolation is a copy of the logic, and a copy is not a guard;
// the same trap as the tilePlan survivors, one layer up.
//
// This is a SOURCE-level assertion rather than a behavioural one. A behavioural test would have to
// stand up the whole HTTP route, a scene with files, the path manager and a generated file on disk
// -- for a fact that is a single identifier at a single line. Reading the source pins the same fact
// with a test that cannot rot into flakiness, and it fails loudly with the line number when someone
// reverts the wiring.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findHandler returns the body of the named handler in routes_scene.go.
func findHandler(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join("..", "routes_scene.go")
	src, err := os.ReadFile(path)
	require.NoError(t, err, "routes_scene.go must be readable; this test asserts on its source")

	body := string(src)
	start := strings.Index(body, "func (rs sceneRoutes) "+name+"(")
	require.NotEqual(t, -1, start, "%s must still exist in routes_scene.go", name)

	end := strings.Index(body[start:], "\n}\n")
	require.NotEqual(t, -1, end, "%s must be a complete top-level function", name)

	return body[start : start+end]
}

// The two handlers that serve a window-aware artefact must use the window-aware key.
//
// Preview and Webp are the ones made window-aware in 8f84c565f. The other generated handlers --
// sprite, VTT, screengrab, markers, funscript, export -- are NOT yet window-aware and must keep the
// plain hash, or they would rename files whose content has not changed. Those are asserted below,
// as a group, because the reason they are excluded is the reason the whole split exists.
func TestThePreviewRoutesUseTheWindowAwareKey(t *testing.T) {
	for _, handler := range []string{"Preview", "Webp"} {
		t.Run(handler, func(t *testing.T) {
			body := findHandler(t, handler)

			assert.Contains(t, body, "models.GeneratedChecksum(",
				"%s serves a preview generated from inside the scene's window, so it must key the "+
					"cache with GeneratedChecksum. Reverting this to GetHash makes two scenes of ONE "+
					"file share a preview, and nothing anywhere reports it.", handler)

			assert.NotContains(t, body, "scene.GetHash(",
				"%s must not fall back to the plain file hash: the file hash is identical for both "+
					"scenes, which is the bug", handler)
		})
	}
}

// The legacy half of the lookup must use the SAME key as the sharded half.
//
// A mismatched pair looks correct and resolves to the wrong file. For a suffixed checksum the
// legacy path is "" anyway (isValidGeneratedChecksum accepts only 16 or 32 hex chars), so this is
// mostly belt-and-braces -- but the pairing is a fact worth pinning, because it is the shape the
// bug would take if isValidGeneratedChecksum were ever relaxed.
func TestThePreviewRoutesPairShardedAndLegacyWithTheSameKey(t *testing.T) {
	for _, handler := range []string{"Preview", "Webp"} {
		t.Run(handler, func(t *testing.T) {
			body := findHandler(t, handler)

			// Both path calls must be keyed by the same variable, and that variable must be the
			// window-aware key. One variable used twice is the guarantee; two separate expressions
			// would be a place for them to drift apart.
			assert.Equal(t, 1, strings.Count(body, "sceneHash := models.GeneratedChecksum("),
				"%s must assign the key exactly once, so both path calls necessarily share it", handler)
			assert.Equal(t, 2, strings.Count(body, "sceneHash)"),
				"%s must pass the same sceneHash to BOTH the sharded and the legacy path", handler)
		})
	}
}

// The NOT-yet-window-aware handlers must keep the plain hash.
//
// This is the half that stops the change leaking. Sprite, VTT, screengrab, markers and funscript
// are still generated from the whole file, so suffixing their keys would rename files whose content
// has not changed -- pure churn, plus a broken bookmark for each one.
//
// A blanket "no route uses GeneratedChecksum except these two" would be the wrong shape, because new
// window-aware handlers will be added; naming the ones that must NOT have it is the assertion that
// keeps working.
func TestTheNotYetWindowAwareHandlersKeepThePlainHash(t *testing.T) {
	// The real names, from routes_scene.go. My first list was guessed from the URL paths
	// (sprite, screengrab, scene-marker) and six of the nine did not exist -- a reminder that
	// a guard naming things it invented is a guard that cannot fail.
	for _, handler := range []string{
		"VttChapter",  // 486
		"VttThumbs",   // 528
		"VttSprite",   // 543
		"Funscript",   // 557
		"Screenshot",  // 411 -- the on-demand cover, not the cached preview
		"Caption",     // 637
		"CaptionLang", // 637 area
		"InteractiveCSV",
		"InteractiveHeatmap",
	} {
		t.Run(handler, func(t *testing.T) {
			body := findHandler(t, handler)

			assert.NotContains(t, body, "models.GeneratedChecksum(",
				"%s is NOT window-aware yet, so its generated files must keep the plain hash: "+
					"suffixing them renames files whose content is unchanged and breaks any "+
					"bookmarked URL for no benefit. Making them window-aware is a separate piece of "+
					"work -- see docs/WHATS-LEFT.md", handler)
		})
	}
}

// The generate task must compute the key AFTER loading the scene's files.
//
// This ordering is the whole reason the previous state was a silent infinite regeneration. The task
// needs the window to build the key, and the window only exists once scene.Files is loaded, so a
// key computed at the top of Start cannot include it.
//
// Asserted on source because the failure it prevents has NO symptom: the preview would be written
// to the unwindowed path, the route would look for the windowed one, and the file would be
// regenerated on every request forever without an error anywhere.
func TestTheGenerateTaskComputesItsKeyAfterLoadingTheWindow(t *testing.T) {
	path := filepath.Join("..", "..", "manager", "task_generate_preview.go")
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	body := string(src)

	// The window is DERIVED from the scene's loaded files, not assigned to a field:
	// `if vf := t.Scene.Files.Primary(); vf != nil { ... }`. That block must come first.
	//
	// My first anchor was `t.Options.Window = window`, which is the point where the window is
	// HANDED to ffmpeg -- and it sits AFTER the key, so the assertion correctly failed and
	// incorrectly explained itself as a bug in the task. It was a bug in the test: asserting on
	// the consumer instead of the source of the fact.
	load := strings.Index(body, "t.Scene.Files.Primary()")
	require.NotEqual(t, -1, load,
		"the preview task must still read the window from the scene's primary file; this test "+
			"pins the ORDERING of that read against the key computation")

	key := strings.Index(body, "models.GeneratedChecksum(t.Scene, t.fileNamingAlgorithm)")
	require.NotEqual(t, -1, key,
		"the preview task must build its key with GeneratedChecksum, or it writes the preview to "+
			"the unwindowed path while the route looks for the windowed one")

	assert.Less(t, load, key,
		"the key MUST be computed after the window is read: before it, there is no window to "+
			"put in the key, and the result is a preview regenerated on every single request, "+
			"silently, with no error anywhere")
}
