package manager

import (
	"context"
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/file"
	"github.com/stashapp/stash/pkg/file/video"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/plugin"
)

// stash#3738 -- attaching a funscript to an already-scanned scene must bump the scene's Updated At.
//
// THE BUG, and why each existing mechanism missed it
//
// `Interactive` lives on the FILE row and is derived from whether `<name>.funscript` exists on disk
// (pkg/file/video/scan.go:42). Dropping a funscript next to a video does not touch the video, so:
//
//   - the scanner's `updated := !fileModTime.Equal(...) || basename || size` (scan.go:792) is false, so
//     it routes to onUnchangedFile rather than the full rescan path;
//   - onUnchangedFile DOES notice, because Decorator.IsMissingMetadata ends in
//     `interactive != vf.Interactive`, and setMissingMetadata corrects the file row;
//   - but the scene handler only runs when isHandlerRequired says so, and that consults
//     handlerRequiredFilter.Accept, which returned true only when a file had NO related objects.
//     One scene already exists, so the handler is skipped, the scene row is never touched, and
//     `updated_at` never moves.
//
// So the file row self-heals and the scene row silently does not. This filter is where that has to be
// caught, and the tests below are mostly about the four combinations that decide it.

// fakeFS is a models.FS that answers only Lstat, which is all interactiveDisagrees uses.
type fakeFS struct {
	// present maps a path to whether Lstat should succeed.
	present map[string]bool
}

func (f fakeFS) Stat(string) (os.FileInfo, error) {
	return nil, errors.New("not implemented")
}

func (f fakeFS) Lstat(name string) (os.FileInfo, error) {
	if f.present[name] {
		return nil, nil
	}
	return nil, os.ErrNotExist
}

func (f fakeFS) Open(string) (iofs.ReadDirFile, error) { return nil, errors.New("not implemented") }
func (f fakeFS) OpenZip(string, int64) (models.ZipFS, error) {
	return nil, errors.New("not implemented")
}
func (f fakeFS) IsPathCaseSensitive(string) (bool, error) { return true, nil }

// realFSOnDisk wraps the OS so the "funscript really is there" case can be checked against a genuine
// filesystem rather than a hand-maintained map, which is the shape that makes these tests lie.
type realFSOnDisk struct{ dir string }

func (d realFSOnDisk) Stat(name string) (os.FileInfo, error)      { return os.Stat(name) }
func (d realFSOnDisk) Lstat(name string) (os.FileInfo, error)     { return os.Lstat(name) }
func (d realFSOnDisk) Open(name string) (iofs.ReadDirFile, error) { return os.Open(name) }
func (d realFSOnDisk) OpenZip(string, int64) (models.ZipFS, error) {
	return nil, errors.New("not implemented")
}
func (d realFSOnDisk) IsPathCaseSensitive(string) (bool, error) { return true, nil }

func testFilter(fs models.FS) *handlerRequiredFilter {
	return &handlerRequiredFilter{FS: fs}
}

func testVideoFile(path string, interactive bool) *models.VideoFile {
	return &models.VideoFile{
		BaseFile:    &models.BaseFile{Path: path},
		Interactive: interactive,
	}
}

func TestInteractiveDisagreesWhenAFunscriptHasAppeared(t *testing.T) {
	// The reported case: a funscript is dropped next to an already-scanned video. The file row still
	// says Interactive=false, so the handler must run and the scene must be touched.
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "scene.mp4")
	if err := os.WriteFile(videoPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	funscript := video.GetFunscriptPath(videoPath)
	if err := os.WriteFile(funscript, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := testFilter(realFSOnDisk{dir: dir})
	if !f.interactiveDisagrees(context.Background(), testVideoFile(videoPath, false)) {
		t.Error("a funscript on disk with Interactive=false in the database requires the handler")
	}
}

func TestInteractiveDisagreesWhenAFunscriptHasBeenRemoved(t *testing.T) {
	// The same argument produces this case, and nothing else in the tree catches it: the file row says
	// Interactive=true, the funscript is gone, so the scene's interactive state is stale.
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "scene.mp4")
	if err := os.WriteFile(videoPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := testFilter(realFSOnDisk{dir: dir})
	if !f.interactiveDisagrees(context.Background(), testVideoFile(videoPath, true)) {
		t.Error("Interactive=true with no funscript on disk requires the handler")
	}
}

func TestInteractiveAgreesWhenBothSidesMatch(t *testing.T) {
	// The case that MUST NOT trigger the handler. Without this, every unchanged video file in the
	// library would re-run the scene handler on every scan, which is the cost the check is guarding
	// against.

	t.Run("funscript present and recorded", func(t *testing.T) {
		dir := t.TempDir()
		videoPath := filepath.Join(dir, "scene.mp4")
		if err := os.WriteFile(videoPath, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(video.GetFunscriptPath(videoPath), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}

		f := testFilter(realFSOnDisk{dir: dir})
		if f.interactiveDisagrees(context.Background(), testVideoFile(videoPath, true)) {
			t.Error("a funscript on disk already recorded must not require the handler again")
		}
	})

	t.Run("no funscript and not recorded", func(t *testing.T) {
		dir := t.TempDir()
		videoPath := filepath.Join(dir, "scene.mp4")
		if err := os.WriteFile(videoPath, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}

		f := testFilter(realFSOnDisk{dir: dir})
		if f.interactiveDisagrees(context.Background(), testVideoFile(videoPath, false)) {
			t.Error("an ordinary video with no funscript must not require the handler")
		}
	})
}

func TestInteractiveDisagreesIgnoresNonVideoFiles(t *testing.T) {
	// The filter is shared by all three scan handlers. An image has no funscript semantics, and
	// returning true for one would re-run the image handler on every unchanged image.
	f := testFilter(fakeFS{present: map[string]bool{}})
	img := &models.ImageFile{BaseFile: &models.BaseFile{Path: "/library/photo.jpg"}}

	if f.interactiveDisagrees(context.Background(), img) {
		t.Error("an image file must never require the handler for interactive state")
	}
}

func TestInteractiveDisagreesIsFalseWithoutAnFS(t *testing.T) {
	// Defensive: a zero-value filter must not claim disagreement, or every file would re-handle.
	f := &handlerRequiredFilter{}
	if f.interactiveDisagrees(context.Background(), testVideoFile("/library/x.mp4", false)) {
		t.Error("with no filesystem configured the check must not require the handler")
	}
}

func TestInteractiveDisagreesHandlesNilFile(t *testing.T) {
	// A type assertion on a nil interface must not panic; the filter runs on every scanned file.
	f := testFilter(fakeFS{present: map[string]bool{}})
	if f.interactiveDisagrees(context.Background(), nil) {
		t.Error("a nil file cannot disagree")
	}
}

// setupManagerForAccept installs the minimum global state that handlerRequiredFilter.Accept reads.
//
// Accept calls useAsVideo, which consults config.GetInstance() via isVideo (manager_tasks.go:43), and
// stash config via the package-level instance. Without these the extension checks panic or silently
// classify nothing as a video, and the wiring tests below would pass for the wrong reason -- which is
// exactly what happened on the first attempt at this file.
func setupManagerForAccept(t *testing.T, stashPaths config.StashConfigs, excludes []string) {
	t.Helper()

	prev := instance
	cfg := config.InitializeEmpty()
	cfg.SetInterface(config.Stash, stashPaths)
	cfg.SetInterface(config.Exclude, excludes)

	instance = &Manager{Config: cfg, PluginCache: plugin.NewCache(cfg)}
	t.Cleanup(func() { instance = prev })
}

// singleStash builds a library root containing one video file, which is the shape every Accept test
// needs.
func singleStash(t *testing.T) (config.StashConfigs, string) {
	t.Helper()

	dir := t.TempDir()
	videoPath := filepath.Join(dir, "scene.mp4")
	if err := os.WriteFile(videoPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.InitializeEmpty()
	cfg.SetInterface(config.Stash, []config.StashConfig{{Path: dir}})
	cfg.SetInterface(config.Exclude, []string{})

	return cfg.GetStashPaths(), videoPath
}

// --- The tests above cover interactiveDisagrees. These cover the WIRING, which is the actual fix. ---

// countingSceneFinder reports a fixed number of scenes for any file.
type countingSceneFinder struct{ n int }

func (c countingSceneFinder) CountByFileID(context.Context, models.FileID) (int, error) {
	return c.n, nil
}

func (c countingSceneFinder) FindByPrimaryFileID(context.Context, models.FileID) ([]*models.Scene, error) {
	return nil, nil
}

// TestAcceptRequiresTheHandlerWhenAFunscriptAppears is the test that actually pins #3738.
//
// The interactiveDisagrees tests above all passed with the fix DISABLED, because they call the helper
// directly. That is the trap this file was nearly another victim of: a unit test on a new helper proves
// the helper works and says nothing about whether anything calls it. So this goes through
// handlerRequiredFilter.Accept, which is the function the scanner actually consults at
// pkg/file/scan.go:908.
//
// Pre-fix, Accept returned false here — one scene already exists, so the "no related objects" branch
// did not fire and the scene handler was skipped, which is exactly why updated_at never moved.
func TestAcceptRequiresTheHandlerWhenAFunscriptAppears(t *testing.T) {
	stashPaths, videoPath := singleStash(t)
	if err := os.WriteFile(video.GetFunscriptPath(videoPath), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	setupManagerForAccept(t, stashPaths, []string{})

	f := &handlerRequiredFilter{
		extensionConfig: extensionConfig{vidExt: []string{"mp4"}},
		SceneFinder:     countingSceneFinder{n: 1}, // the scene already exists
		FS:              &file.OsFS{},
	}

	if !f.Accept(context.Background(), testVideoFile(videoPath, false)) {
		t.Error("Accept must return true when a funscript has appeared beside an already-scanned video, " +
			"or the scene handler is skipped and updated_at never moves")
	}
}

func TestAcceptDoesNotRequireTheHandlerForAnOrdinaryScene(t *testing.T) {
	// The cost guard. If Accept returned true for every unchanged video file, every scan would re-run
	// every scene handler, which is the scan-time regression this fix must not introduce.
	stashPaths, videoPath := singleStash(t)
	setupManagerForAccept(t, stashPaths, []string{})

	f := &handlerRequiredFilter{
		extensionConfig: extensionConfig{vidExt: []string{"mp4"}},
		SceneFinder:     countingSceneFinder{n: 1},
		FS:              &file.OsFS{},
	}

	if f.Accept(context.Background(), testVideoFile(videoPath, false)) {
		t.Error("an unchanged ordinary video with an existing scene must NOT require the handler")
	}
}

func TestAcceptStillRequiresTheHandlerWhenThereAreNoScenes(t *testing.T) {
	// The pre-existing behaviour must be intact: a file with no scenes always needs handling.
	stashPaths, videoPath := singleStash(t)
	setupManagerForAccept(t, stashPaths, []string{})

	f := &handlerRequiredFilter{
		extensionConfig: extensionConfig{vidExt: []string{"mp4"}},
		SceneFinder:     countingSceneFinder{n: 0},
		FS:              &file.OsFS{},
	}

	if !f.Accept(context.Background(), testVideoFile(videoPath, false)) {
		t.Error("a video with no scenes must still require the handler")
	}
}
