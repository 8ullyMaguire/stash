//go:build integration
// +build integration

package sqlite_test

// stash#3530 — WHICH loader applies the window.
//
// The window (scenes_files.start_time/end_time) reaches models.VideoFile in exactly one place:
// SceneStore.GetFiles, which rewrites Duration to end-start and sets StartTime/EndTime on the
// per-scene copy. FileStore.Find -- which is what Scene.LoadPrimaryFile uses -- does NOT select
// those columns at all, so a file loaded that way reports no window whatever the row says.
//
// Every windowed site so far (cover, preview, preview key) needed the CLAMPED window, so they used
// GetFiles. This sprite task uses LoadPrimaryFile, because the sprite task has no scene id and is
// driven from a scan. If LoadPrimaryFile really does not apply the window, then a sprite generated
// through it is silently the UNWINDOWED one -- every test of the arithmetic above green, and a
// sprite of the wrong footage in every library.
//
// So this is measured, not assumed: it drives both stores over one ranged scene and compares what
// each reports. If LoadPrimaryFile DOES carry the window, this test documents it and the sprite
// task is sound.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

func TestWhichLoaderCarriesTheWindow(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		f := mkRangeVideoFile(t, ctx, "window-loader-probe.mp4", 1800)
		sceneID := newSceneOver(t, ctx, f)
		setRange(t, ctx, sceneID, 60.0, 300.0)

		// The loader the cover and preview use: GetFiles.
		files, err := db.Scene.GetFiles(ctx, sceneID)
		require.NoError(t, err)
		require.Len(t, files, 1)
		viaGetFiles := files[0]

		// The loader LoadPrimaryFile uses, driven through the model exactly as the task does.
		scene, err := db.Scene.Find(ctx, sceneID)
		require.NoError(t, err)
		require.NotNil(t, scene)
		require.NoError(t, scene.LoadPrimaryFile(ctx, db.File))
		viaPrimary := scene.Files.Primary()
		require.NotNil(t, viaPrimary)

		t.Run("GetFiles carries the clamped window", func(t *testing.T) {
			require.NotNil(t, viaGetFiles.StartTime, "GetFiles must carry the window's start")
			require.NotNil(t, viaGetFiles.EndTime, "GetFiles must carry the window's end")
			assert.Equal(t, 60.0, *viaGetFiles.StartTime)
			assert.Equal(t, 300.0, *viaGetFiles.EndTime)
			assert.Equal(t, 240.0, viaGetFiles.Duration, "GetFiles reports the window's length")
		})

		t.Run("LoadPrimaryFile reports the FILE, not the window", func(t *testing.T) {
			// THE MEASUREMENT. Written as an assertion about the current state rather than as
			// documentation, so that whoever fixes it has to change this test on purpose.
			assert.Equal(t, 1800.0, viaPrimary.Duration,
				"LoadPrimaryFile goes through FileStore.Find, which does not select "+
					"scenes_files.start_time/end_time -- so it reports the FILE's length. "+
					"If this assertion now fails, someone has taught File.Find to carry the "+
					"window, and the sprite task's use of LoadPrimaryFile is sound")
			assert.Nil(t, viaPrimary.StartTime,
				"and it carries no StartTime, because the columns are not selected at all")
		})
	})
}

// The consequence, stated as a test so it cannot be forgotten: a scene loaded the sprite task's way
// has NO window, so anything derived from WindowOf() on it is the whole file.
func TestASceneLoadedTheSpriteTasksWayHasNoWindow(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		f := mkRangeVideoFile(t, ctx, "window-sprite-task-probe.mp4", 1800)
		sceneID := newSceneOver(t, ctx, f)
		setRange(t, ctx, sceneID, 60.0, 300.0)

		scene, err := db.Scene.Find(ctx, sceneID)
		require.NoError(t, err)
		require.NoError(t, scene.LoadPrimaryFile(ctx, db.File))

		pf := scene.Files.Primary()
		require.NotNil(t, pf)

		var start, end float64
		var ok bool
		if pf.StartTime != nil {
			start = *pf.StartTime
		}
		if pf.EndTime != nil {
			end = *pf.EndTime
		}

		// What pkg/models.GeneratedWindow() would return for this scene.
		start, end, ok = scene.GeneratedWindow()

		assert.False(t, ok,
			"GeneratedWindow on a LoadPrimaryFile-loaded scene reports NO window, so the sprite "+
				"grid for this scene would tile all 1800s -- the defect #3530 set out to fix. "+
				"Fixing it means reading the window through SceneStore.GetFiles, not File.Find")
		assert.Zero(t, start)
		assert.Zero(t, end)
	})
}

// The fix's own requirement: the window must reach the sprite task, and the only store that
// applies it is GetFiles. This pins the SHAPE that makes that possible -- a file that carries a
// window is distinguishable from one that does not, so a caller can tell "no window" from
// "window at 0".
func TestADegenerateWindowCannotReachTheModelAtAll(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		// start == end is refused by the schema's `end > start` CHECK, so a ZERO-LENGTH window
		// cannot be stored. That is worth pinning because a zero-length window is what an
		// unclamped `end` on a file with no duration would produce, and a sprite generator handed
		// one divides by zero.
		f := mkRangeVideoFile(t, ctx, "window-degenerate.mp4", 1800)
		sceneID := newSceneOver(t, ctx, f)
		fileID := mkFileIDForScene(t, ctx, sceneID)

		err := execErr(t, ctx,
			"UPDATE scenes_files SET start_time = ?, end_time = ? WHERE scene_id = ? AND `primary` = 1",
			100.0, 100.0, sceneID)
		require.Error(t, err,
			"a zero-length window must be refused by the database: the sprite generator would "+
				"divide by its length, and a scene with duration 0 is broken everywhere else too")
		_ = fileID

		// A window at 0 IS legal, which is the case a `start > 0` shortcut would misread.
		require.NoError(t, exec(t, ctx,
			"UPDATE scenes_files SET start_time = ?, end_time = ? WHERE scene_id = ? AND `primary` = 1",
			0.0, 100.0, sceneID))

		files, err := db.Scene.GetFiles(ctx, sceneID)
		require.NoError(t, err)
		require.Len(t, files, 1)
		require.NotNil(t, files[0].StartTime, "a window at 0 is a WINDOW and must be reported as one")
		assert.Equal(t, 0.0, *files[0].StartTime)
		assert.Equal(t, 100.0, files[0].Duration)
	})
}

// The models-level helper the sprite task uses must report "no window" for a scene whose files were
// loaded WITHOUT the window -- and must report it distinctly from a window at 0. Two different
// states, one of which is the bug above.
func TestGeneratedWindowDistinguishesNoWindowFromZeroStart(t *testing.T) {
	unranged := models.Scene{ID: 1}
	_, _, ok := unranged.GeneratedWindow()
	assert.False(t, ok, "a scene whose files were never loaded has no window")

	zero := models.Scene{ID: 1, Files: models.NewRelatedVideoFiles([]*models.VideoFile{{
		BaseFile:  &models.BaseFile{Path: "/media/x.mp4"},
		StartTime: float64Ptr(0),
		EndTime:   float64Ptr(100),
	}})}
	start, end, ok := zero.GeneratedWindow()
	require.True(t, ok, "a window at 0 is a window")
	assert.Equal(t, 0.0, start)
	assert.Equal(t, 100.0, end)
}

func float64Ptr(v float64) *float64 { return &v }
