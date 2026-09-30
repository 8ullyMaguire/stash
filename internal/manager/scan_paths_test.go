package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/manager/config"
)

// stash#6457 asks the API to "scan in file(s), add metadata on scan". The walk
// is NOT the obstacle: `walkDir` returns early when the root is not a directory,
// and `queueFiles` SymWalks whatever paths it is given. The obstacle is
// `getScanPaths`, the gate every requested path passes through first.
//
// stash#6457, clause (a): "scan in file(s)". THE DEFECT WAS SILENCE, NOT THE
// SKIP.
//
// getScanPaths maps each requested path to a configured library with
// GetStashFromDirPath. A path matching none is skipped -- correctly, since
// `paths` has always been a filter over configured libraries and erroring would
// break scripted callers that pass a superset. But the skipped path was then
// discarded, so a request naming one valid and one invalid path returned a job
// ID, scanned the valid one, and reported success, with nothing anywhere
// recording that the other was never looked at.
//
// The fix returns the skipped paths so the caller can report them. The skip
// stays lenient; only the silence goes.
//
// ON THE FILE-vs-DIRECTORY QUESTION, which I checked rather than assumed:
// `getScanPaths` calls `GetStashFromDirPath(p)` on the path AS GIVEN. Despite
// the name it does not take a directory: it requires
// `IsPathInDir(libRoot, p)`, and that holds when `filepath.Rel(root, p)` does
// not start with "..". For `/lib/a/sub/clip.mp4` against root `/lib/a` the rel
// is `sub/clip.mp4`, which passes. So a FILE inside a library is matched -- the
// naming is misleading, the behaviour is correct. What is NOT matched is a path
// outside every configured library, and that is the silent drop.

func withStashLibraries(t *testing.T, paths ...string) {
	t.Helper()
	cfg := config.InitializeEmpty()
	ss := make(config.StashConfigs, 0, len(paths))
	for _, p := range paths {
		ss = append(ss, &config.StashConfig{Path: p})
	}
	cfg.SetInterface(config.Stash, ss)
	t.Cleanup(func() { config.InitializeEmpty() })
}

// THE FIX: a path outside every configured library is still skipped, but it is
// REPORTED. This fails if the skipped path is discarded, which is the bug.
func TestGetScanPathsReportsAnUnconfiguredPath(t *testing.T) {
	withStashLibraries(t, "/library/a")

	got, skipped := getScanPaths([]string{"/tmp/incoming.mp4"})

	assert.Empty(t, got, "nothing is scanned for a path in no library")
	assert.Equal(t, []string{"/tmp/incoming.mp4"}, skipped,
		"a requested path must be REPORTED, not silently dropped: the caller named it, and "+
			"a dropped path is indistinguishable from a scanned one in the response")
}

// A MIX is the dangerous shape, and it is what a real caller produces: one path
// scanned, one skipped, job reports success. Both halves must be visible.
func TestGetScanPathsMixedRequestReportsOnlyTheSkippedPath(t *testing.T) {
	withStashLibraries(t, "/library/a")

	got, skipped := getScanPaths([]string{"/library/a/one.mp4", "/tmp/two.mp4"})

	require.Len(t, got, 1, "the valid path is still scanned -- the fix must not make this an error")
	assert.Equal(t, "/library/a/one.mp4", got[0].Path)
	assert.Equal(t, []string{"/tmp/two.mp4"}, skipped,
		"and the caller can now tell which of the two it asked for was not honoured")
}

// POSITIVE CONTROL. Without it, a getScanPaths that returned nil for EVERY input
// would pass both tests above -- a guard matching zero things, which is the trap
// this project keeps falling into.
func TestGetScanPathsEmptyRequestScansEveryLibrary(t *testing.T) {
	withStashLibraries(t, "/library/a", "/library/b")

	got, skipped := getScanPaths(nil)
	require.Empty(t, skipped, "nothing is skipped when the request is empty")

	require.Len(t, got, 2, "an empty request means everything, so the drop is distinguishable")
}

// POSITIVE CONTROL for the file case, and the clause #6457 actually names: a
// single file inside a library is accepted today. If this ever fails, clause (a)
// regressed even though the drop tests still pass.
func TestGetScanPathsAcceptsAFilePathInsideALibrary(t *testing.T) {
	withStashLibraries(t, "/library/a")

	got, skipped := getScanPaths([]string{"/library/a/sub/clip.mp4"})
	require.Empty(t, skipped, "a path inside a library is not skipped")

	require.Len(t, got, 1, "clause (a): a single file inside a library is scannable")
	assert.Equal(t, "/library/a/sub/clip.mp4", got[0].Path,
		"the library is narrowed to the requested path, not the whole root")
}

// The narrowed copy must inherit the parent's settings, or scanning one file
// silently changes the exclude rules for it.
func TestGetScanPathsNarrowedLibraryKeepsParentSettings(t *testing.T) {
	cfg := config.InitializeEmpty()
	cfg.SetInterface(config.Stash, config.StashConfigs{{Path: "/library/a", ExcludeVideo: true}})
	t.Cleanup(func() { config.InitializeEmpty() })

	got, skipped := getScanPaths([]string{"/library/a/sub/clip.mp4"})
	require.Empty(t, skipped, "a path inside a library is not skipped")

	require.Len(t, got, 1)
	assert.True(t, got[0].ExcludeVideo, "the narrowed copy must inherit the parent's settings")
	assert.Equal(t, "/library/a", cfg.GetStashPaths()[0].Path,
		"and the configured library must not be mutated in place")
}

// The most specific containing library wins, and the result is still narrowed to
// the request.
func TestGetScanPathsPrefersTheMostSpecificLibrary(t *testing.T) {
	withStashLibraries(t, "/library", "/library/a/deep")

	got, skipped := getScanPaths([]string{"/library/a/deep/x"})
	require.Empty(t, skipped)

	require.Len(t, got, 1)
	assert.Equal(t, "/library/a/deep/x", got[0].Path)
}

// A path that merely shares a PREFIX with a library must not match --
// `/library/a` must not claim `/library/abc`. `IsPathInDir` is a `filepath.Rel`
// test, not a `strings.HasPrefix` one, and this pins that.
func TestGetScanPathsDoesNotMatchOnAPrefixSibling(t *testing.T) {
	withStashLibraries(t, "/library/a")

	got, skipped := getScanPaths([]string{"/library/abc/clip.mp4"})

	assert.Empty(t, got, "a prefix sibling is outside the library")
	assert.Equal(t, []string{"/library/abc/clip.mp4"}, skipped,
		"and it is REPORTED as skipped, rather than vanishing")
}
