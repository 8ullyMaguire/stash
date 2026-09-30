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
// WHAT IS ESTABLISHED HERE, and it is characterisation, not a fix.
//
// A requested path is mapped to a configured stash library with
// `GetStashFromDirPath`. When that returns nil the path is `continue`d --
// logged at Warn and dropped -- so the caller gets a SHORTER list and no error.
// `metadataScan(paths: ["/tmp/incoming.mp4"])` returns a job ID and scans
// nothing, and a MIXED request loses entries invisibly while the job reports
// success.
//
// WHETHER THAT IS A DEFECT IS NOT SETTLED BY THIS FILE, and these tests
// deliberately assert the CURRENT behaviour so the choice is visible rather
// than baked in silently. They are characterisation tests: the first two would
// go RED if the drop were changed to an error, which is what they are FOR --
// they pin the exact semantic a decision would have to replace. Every other
// test here is a positive control that must keep passing under either choice.
//
// The open question is recorded in docs/UPSTREAM-ISSUES.md for #6457: is
// "ignore a path outside every configured library" lenient-by-design, or is
// silence the wrong answer? Turning it into an error is a BEHAVIOUR CHANGE that
// could break scripted clients relying on the lenient reading, so it is the
// owner's call, not one to ship inside an issue-closing commit.
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

// CHARACTERISATION: a path outside every configured library is dropped, and the
// caller has no way to tell. Inverts if the drop becomes an error.
func TestGetScanPathsDropsAnUnconfiguredPath(t *testing.T) {
	withStashLibraries(t, "/library/a")

	got := getScanPaths([]string{"/tmp/incoming.mp4"})

	assert.Empty(t, got,
		"an explicitly requested path must not vanish: the caller named it, and a dropped "+
			"path is indistinguishable from a scanned one in the response")
}

// CHARACTERISATION: a MIX is the dangerous shape, and it is what a real caller
// produces -- one path scanned, one silently lost, job reports success.
func TestGetScanPathsMixedRequestIsSilentlyShortened(t *testing.T) {
	withStashLibraries(t, "/library/a")

	got := getScanPaths([]string{"/library/a/one.mp4", "/tmp/two.mp4"})

	require.Len(t, got, 1)
	assert.Equal(t, "/library/a/one.mp4", got[0].Path)
	t.Logf("asked for 2 paths, got %d, and nothing tells the caller which was dropped", len(got))
}

// POSITIVE CONTROL. Without it, a getScanPaths that returned nil for EVERY input
// would pass both tests above -- a guard matching zero things, which is the trap
// this project keeps falling into.
func TestGetScanPathsEmptyRequestScansEveryLibrary(t *testing.T) {
	withStashLibraries(t, "/library/a", "/library/b")

	got := getScanPaths(nil)

	require.Len(t, got, 2, "an empty request means everything, so the drop is distinguishable")
}

// POSITIVE CONTROL for the file case, and the clause #6457 actually names: a
// single file inside a library is accepted today. If this ever fails, clause (a)
// regressed even though the drop tests still pass.
func TestGetScanPathsAcceptsAFilePathInsideALibrary(t *testing.T) {
	withStashLibraries(t, "/library/a")

	got := getScanPaths([]string{"/library/a/sub/clip.mp4"})

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

	got := getScanPaths([]string{"/library/a/sub/clip.mp4"})

	require.Len(t, got, 1)
	assert.True(t, got[0].ExcludeVideo, "the narrowed copy must inherit the parent's settings")
	assert.Equal(t, "/library/a", cfg.GetStashPaths()[0].Path,
		"and the configured library must not be mutated in place")
}

// The most specific containing library wins, and the result is still narrowed to
// the request.
func TestGetScanPathsPrefersTheMostSpecificLibrary(t *testing.T) {
	withStashLibraries(t, "/library", "/library/a/deep")

	got := getScanPaths([]string{"/library/a/deep/x"})

	require.Len(t, got, 1)
	assert.Equal(t, "/library/a/deep/x", got[0].Path)
}

// A path that merely shares a PREFIX with a library must not match --
// `/library/a` must not claim `/library/abc`. `IsPathInDir` is a `filepath.Rel`
// test, not a `strings.HasPrefix` one, and this pins that.
func TestGetScanPathsDoesNotMatchOnAPrefixSibling(t *testing.T) {
	withStashLibraries(t, "/library/a")

	got := getScanPaths([]string{"/library/abc/clip.mp4"})

	assert.Empty(t, got, "a prefix sibling is outside the library")
}
