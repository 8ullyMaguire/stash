//go:build integration
// +build integration

package sqlite_test

// stash#3530 — a scene's window must survive an ORDINARY edit.
//
// # THE BUG THIS FINDS
//
// SceneStore.Update ends with:
//
//	if updatedObject.Files.Loaded() {
//	    fileIDs := ...ids only...
//	    scenesFilesTableMgr.replaceJoins(ctx, updatedObject.ID, fileIDs)
//	}
//
// and joinTable.replaceJoins is destroy-then-insert (table.go:264):
//
//	if err := t.destroy(ctx, []int{id}); err != nil { ... }
//	return t.insertJoins(ctx, id, foreignIDs)
//
// `scenes_files` is a joinTable, and #3530 put `start_time`/`end_time` ON that join table --
// which is the only place they can live, since there is no per-scene-file type in the runtime
// model. So any scene update whose input carries the file list DESTROYS the scenes_files rows
// and re-INSERTS them from ids alone. start_time and end_time are not in the insert, so they come
// back NULL: the window is silently erased, the scene reverts to the whole file, and every
// window-derived artefact (preview, sprite, VTT) that was generated for the windowed scene now
// disagrees with the scene.
//
// Nothing in the existing suite catches it, because every range test writes the window with raw
// SQL AFTER the scene is created and never performs a scene update afterwards. The window is
// only ever set, never edited-around. This file performs the edit.
//
// # WHY IT IS SILENT RATHER THAN AN ERROR
//
// The INSERT satisfies every constraint (the CHECKs permit NULL, which is exactly "no window"),
// and the ids are unchanged, so the transaction commits. The user renames a scene, presses save,
// and their range is gone. A scene title edit does not touch files at all, so the bug is
// invisible until something DOES carry the file list -- which is the resolver path for any
// update that includes primary_file_id.
//
// # WHAT THIS ESTABLISHES FOR THE UI WORK
//
// The UI's "set a range" form will submit through the existing scene update. If the window cannot
// survive that path, the feature is unusable the first time a user renames the scene afterwards,
// so preservation is a PRECONDITION of the feature rather than a nicety -- which is why it is
// proved here, before the resolver exists, instead of discovered after.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// loadScene returns the scene as the UPDATE path sees it: the full object, files included.
func loadSceneForUpdate(t *testing.T, ctx context.Context, sceneID int) *models.Scene {
	t.Helper()
	scene, err := db.Scene.Find(ctx, sceneID)
	require.NoError(t, err)
	return scene
}

// TestAnOrdinarySceneUpdateKeepsTheWindow — the defect, reproduced through the store.
func TestAnOrdinarySceneUpdateKeepsTheWindow(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		sceneID := mkRangeVideo(t, ctx, "rename-keeps-range.mp4", 1800)
		setRange(t, ctx, sceneID, 120.0, 480.0)

		// A rename. The most ordinary edit there is, and the one a user performs after
		// setting a range by hand.
		scene := loadSceneForUpdate(t, ctx, sceneID)
		scene.Title = "a new title"
		require.NoError(t, db.Scene.Update(ctx, scene))

		files, err := db.Scene.GetFiles(ctx, sceneID)
		require.NoError(t, err)
		require.Len(t, files, 1)

		assert.Equal(t, 360.0, files[0].Duration,
			"a rename must not change the scene's duration: the window is 120..480")
		assert.Equal(t, "a new title", mustFindScene(t, ctx, sceneID).Title,
			"the rename itself must have landed, or this test proves nothing")
	})
}

// TestTheWindowSurvivesAnExplicitFileListUpdate — the exact shape the UI's range form submits.
//
// The rename above may or may not carry Files.Loaded() depending on how the resolver populates
// it, so it is the weaker of the two. This one forces the file list to be present, which is the
// case that MUST wipe today and must not after the fix.
func TestTheWindowSurvivesAnExplicitFileListUpdate(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		f := mkRangeVideoFile(t, ctx, "explicit-files.mp4", 1800)
		sceneID := newSceneOver(t, ctx, f)
		setRange(t, ctx, sceneID, 60.0, 240.0)

		scene := loadSceneForUpdate(t, ctx, sceneID)
		// Force the branch: the same list, same order, one file.
		scene.Files = models.NewRelatedVideoFiles([]*models.VideoFile{{
			BaseFile: &models.BaseFile{ID: f.ID},
		}})
		require.True(t, scene.Files.Loaded(),
			"the fixture must actually load Files, or this test asserts nothing")

		require.NoError(t, db.Scene.Update(ctx, scene))

		files, err := db.Scene.GetFiles(ctx, sceneID)
		require.NoError(t, err)
		require.Len(t, files, 1)
		assert.Equal(t, 180.0, files[0].Duration,
			"an update that carries the file list must not erase the window: 60..240 is 180s")
	})
}

// TestEachSceneOfOneFileKeepsItsOwnWindow — the reason preservation is per-row and not global.
//
// Two scenes of one file is the case #3530 exists for. If preservation were implemented by
// rewriting every scenes_files row for the file, or by caching one window per file, the second
// scene's range would be overwritten by the first's -- the exact failure this window feature is
// meant to fix, reintroduced one layer down.
func TestEachSceneOfOneFileKeepsItsOwnWindow(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		f := mkRangeVideoFile(t, ctx, "two-scenes-one-file.mp4", 1800)

		first := newSceneOver(t, ctx, f)
		setRange(t, ctx, first, 0.0, 300.0)

		// A second scene over the same file needs the row inserted without the UNIQUE
		// constraint on the primary file biting, which is why this goes through raw SQL
		// rather than Scene.Create.
		//
		// ORDER MATTERS, and getting it wrong is silent: setRange filters on
		// `primary = 1`, so a scene demoted to primary=0 BEFORE its range is set has the
		// UPDATE match ZERO rows. SQLite reports that as success -- 0 rows changed, no
		// error -- so the test then asserts against a window that was never written and
		// blames the code under test. Range first, then demote.
		second := newSceneOver(t, ctx, f)
		setRange(t, ctx, second, 900.0, 1200.0)
		require.NoError(t, exec(t, ctx,
			"UPDATE scenes_files SET `primary` = 0 WHERE scene_id = ?", second))

		// Edit the FIRST scene. The second's row is untouched by any correct fix, and
		// this assertion is what catches a fix that is too broad.
		scene := loadSceneForUpdate(t, ctx, first)
		scene.Title = "edited"
		require.NoError(t, db.Scene.Update(ctx, scene))

		firstFiles, err := db.Scene.GetFiles(ctx, first)
		require.NoError(t, err)
		require.Len(t, firstFiles, 1)
		assert.Equal(t, 300.0, firstFiles[0].Duration,
			"the edited scene keeps its own 0..300 window")

		secondFiles, err := db.Scene.GetFiles(ctx, second)
		require.NoError(t, err)
		require.Len(t, secondFiles, 1)
		assert.Equal(t, 300.0, secondFiles[0].Duration,
			"the OTHER scene of the same file must be untouched: 900..1200 is also 300s")
	})
}

func mustFindScene(t *testing.T, ctx context.Context, sceneID int) *models.Scene {
	t.Helper()
	scene, err := db.Scene.Find(ctx, sceneID)
	require.NoError(t, err)
	return scene
}

// TestDetachingAFileKeepsTheOtherScenesWindow — the witness for SCENE-SCOPED deletion.
//
// The NAME matters here, and getting it wrong is the same trap this repo has hit three times:
// a `-run "Window|Range"` filter that does not match a test name silently excludes it, and
// "the suite passed" then means nothing was run. This test was originally called
// TestDetachingOneSceneOfAFileLeavesTheOthersAttached, which matches neither `Window` nor
// `Range`, so the mutation sweep reported P2 SURVIVED while this test was never executed.
// Renamed so the filter reaches it; docs/mutate_3530_preserve.py now kills P2.
//
// Added because a mutation DID survive: removing the `scene_id = ?` predicate from
// relatedFilesTable.destroyJoinsForScene left the suite green.
//
// The reason it survived is that no other test ever DETACHES a file. The rename test keeps the
// same file, the explicit-list test keeps the same file, and the two-scenes test edits one scene
// while its file list is unchanged -- so `drop` is empty in all of them and the delete statement
// is never executed. A whole branch of the fix was untested.
//
// This one actually detaches, which is the only way to reach it.
func TestDetachingAFileKeepsTheOtherScenesWindow(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		f := mkRangeVideoFile(t, ctx, "detach-one-scene.mp4", 1800)

		first := newSceneOver(t, ctx, f)
		setRange(t, ctx, first, 0.0, 300.0)

		second := newSceneOver(t, ctx, f)
		setRange(t, ctx, second, 900.0, 1200.0)
		require.NoError(t, exec(t, ctx,
			"UPDATE scenes_files SET `primary` = 0 WHERE scene_id = ?", second))

		// Detach the file from the FIRST scene only.
		other := mkRangeVideoFile(t, ctx, "detach-other-file.mp4", 600)
		scene := loadSceneForUpdate(t, ctx, first)
		scene.Files = models.NewRelatedVideoFiles([]*models.VideoFile{other})
		require.NoError(t, db.Scene.Update(ctx, scene))

		firstFiles, err := db.Scene.GetFiles(ctx, first)
		require.NoError(t, err)
		require.Len(t, firstFiles, 1)
		assert.Equal(t, models.FileID(other.ID), firstFiles[0].ID,
			"the detached scene must now point at the new file")

		// THE assertion. Without the scene_id predicate the delete above matched on
		// file_id alone and removed the SECOND scene's attachment too -- silently, since a
		// scene with no files is legal and every CHECK passes.
		secondFiles, err := db.Scene.GetFiles(ctx, second)
		require.NoError(t, err)
		require.Len(t, secondFiles, 1,
			"detaching the file from one scene must NOT detach it from the scene that shares it")
		assert.Equal(t, 300.0, secondFiles[0].Duration,
			"the other scene of the same file keeps its own 900..1200 window")
	})
}
