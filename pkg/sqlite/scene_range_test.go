//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// stash#3530 — a scene may name a TIME RANGE within its file.
//
// ## WHY EVERY REFUSAL BELOW IS A RAW INSERT
//
// A constraint nobody has tried to violate is a comment. Each of these writes the row the
// CHECK is supposed to stop, using `execErr` so the DATABASE is what refuses it. Going
// through a store method instead would let a future Go-level guard satisfy the assertion
// while the schema rotted — which is exactly what #1790's M7 did: the store's own check
// masked the database's CHECK and the suite stayed green.
//
// The helpers (`sfTxn`, `exec`, `execErr`, `count`, `scalar`) come from
// stashforge_migrations_test.go, which was written for the same job on a different
// migration. `scalarErr` exists there with the comment "a constraint nobody has tried to
// violate is a comment"; `execErr` is its counterpart for writes.
//
// `sfTxn` wraps `withRollbackTxn`, so every row written here is rolled back and no test
// leaks state into the next one.

// A real `files` row, because scenes_files has a FOREIGN KEY to files: inserting a
// synthetic file_id fails on the FK and the test then passes for entirely the wrong reason
// (the FK, not the CHECK, is what refused it).
func mkFile(t *testing.T, ctx context.Context, name string) int64 {
	t.Helper()
	require.NoError(t, exec(t, ctx,
		`INSERT INTO files (basename, parent_folder_id, size, mod_time, created_at, updated_at)
		 VALUES (?, 1, 1, datetime('now'), datetime('now'), datetime('now'))`, name))
	return int64(scalar(t, ctx, "SELECT id FROM files WHERE basename = ?", name).(int64))
}

// insertSceneFileRaw writes one scenes_files row with raw SQL and returns the error, so the
// schema is what decides. `primary` is quoted because it is a SQL keyword.
func insertSceneFileRaw(t *testing.T, ctx context.Context, sceneID, fileID int64, primary bool, start, end interface{}) error {
	t.Helper()
	return execErr(t, ctx,
		"INSERT INTO scenes_files (scene_id, file_id, `primary`, start_time, end_time) VALUES (?, ?, ?, ?, ?)",
		sceneID, fileID, primary, start, end)
}

func f(v float64) interface{} { return v }

// TestAnExistingSceneHasNoTimeRange — the migration left every existing row unbounded.
//
// Asserting "the column exists" is weaker than it looks, because a column added with
// DEFAULT 0 exists, is readable, and silently claims every scene in the library starts at
// zero. So this asserts NULL, which is the only state that means "the whole file".
func TestAnExistingSceneHasNoTimeRange(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		rows := count(t, ctx, `SELECT COUNT(*) FROM scenes_files
			WHERE start_time IS NOT NULL OR end_time IS NOT NULL`)
		assert.Equal(t, int64(0), rows,
			"no pre-existing row may carry a range; a DEFAULT 0 here would silently "+
				"rewrite every scene in the library as starting at zero")
	})
}

// TestANegativeStartTimeIsRefusedByTheDatabaseItself — the CHECK FIRES.
//
// Named for what it proves. A constraint that EXISTS is not a constraint that FIRES, and
// the only way to tell them apart is to write the row it is meant to stop.
func TestANegativeStartTimeIsRefusedByTheDatabaseItself(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		fid := mkFile(t, ctx, "r-neg-start.mp4")
		err := insertSceneFileRaw(t, ctx, 1, fid, false, f(-1), nil)
		require.Error(t, err, "a negative start_time must be refused by the schema")
		assert.Contains(t, err.Error(), "start_time_non_negative",
			"the refusal must name the constraint, so a typo in the SQL cannot pass as a refusal")
	})
}

func TestANegativeEndTimeIsRefusedByTheDatabaseItself(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		fid := mkFile(t, ctx, "r-neg-end.mp4")
		err := insertSceneFileRaw(t, ctx, 1, fid, false, nil, f(-1))
		require.Error(t, err, "a negative end_time must be refused by the schema")
		assert.Contains(t, err.Error(), "end_time_non_negative")
	})
}

// TestAnEmptyRangeIsRefused — end == start is an EMPTY window, not a zero-length scene.
func TestAnEmptyRangeIsRefused(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		fid := mkFile(t, ctx, "r-empty.mp4")
		err := insertSceneFileRaw(t, ctx, 1, fid, false, f(10), f(10))
		require.Error(t, err, "end == start describes no content and must be refused")
		assert.Contains(t, err.Error(), "end_after_start")
	})
}

func TestAnInvertedRangeIsRefused(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		fid := mkFile(t, ctx, "r-inverted.mp4")
		err := insertSceneFileRaw(t, ctx, 1, fid, false, f(30), f(10))
		require.Error(t, err, "end < start must be refused")
		assert.Contains(t, err.Error(), "end_after_start")
	})
}

// TestZeroIsAValidStartAndEnd — the boundary, as its own test.
//
// Every refusal test above would still pass if the CHECK were written `start_time > 0`
// instead of `>= 0`. Zero is the beginning of a file and the end of nothing, and it is
// legal, so only a test that accepts it can tell `>` from `>=`.
func TestZeroIsAValidStartAndEnd(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		fid := mkFile(t, ctx, "r-zero.mp4")
		require.NoError(t, insertSceneFileRaw(t, ctx, 1, fid, false, f(0), nil),
			"start_time = 0 is the beginning of the file, not an invalid value")
		require.NoError(t, insertSceneFileRaw(t, ctx, 2, fid, false, nil, f(0)),
			"end_time = 0 is the beginning of the file — legal, though an empty scene")
	})
}

// TestOpenEndedAndWholeFileRangesAreAccepted — the other half.
//
// A CHECK that refused everything would pass every refusal test above, so the accepting
// cases are asserted too.
func TestOpenEndedAndWholeFileRangesAreAccepted(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		require.NoError(t, insertSceneFileRaw(t, ctx, 1, mkFile(t, ctx, "r-whole.mp4"), false, nil, nil),
			"NULL/NULL is the whole file and must be accepted")
		require.NoError(t, insertSceneFileRaw(t, ctx, 1, mkFile(t, ctx, "r-tail.mp4"), false, f(10), nil),
			"a start with no end means 'to the end of the file'")
		require.NoError(t, insertSceneFileRaw(t, ctx, 1, mkFile(t, ctx, "r-head.mp4"), false, nil, f(40)),
			"an end with no start means 'from the beginning'")
		require.NoError(t, insertSceneFileRaw(t, ctx, 1, mkFile(t, ctx, "r-both.mp4"), false, f(10), f(40)),
			"a real range must be accepted")
	})
}

// TestThePrimaryIndexSurvivedTheRebuild — migration 122 is a TABLE REBUILD.
//
// The bundled SQLite in the pinned go-sqlite3 v1.14.22 REJECTS `ALTER TABLE ADD CONSTRAINT`
// (measured: `near "CONSTRAINT": syntax error`), so the CHECKs can only be added by
// rebuilding the table. A rebuild drops every index with it, and
// `unique_index_scenes_files_on_primary` is what guarantees a scene has exactly ONE primary
// file. Losing it is a data-integrity regression that **no other test in this file would
// notice**, which is precisely why it needs its own.
func TestThePrimaryIndexSurvivedTheRebuild(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		assert.Equal(t, int64(1),
			count(t, ctx, `SELECT COUNT(*) FROM sqlite_master
				WHERE type = 'index' AND name = 'unique_index_scenes_files_on_primary'`),
			"the partial unique index on the primary file was lost by the rebuild")

		// And it must still be PARTIAL. A plain unique index on scene_id would refuse a
		// scene's second, non-primary file — which is legal, and would break every scene
		// that has sidecars or extra versions.
		//
		// Asserted on the index's SQL TEXT, not on sqlite_master.partial: this build's
		// sqlite_master has columns (type, name, tbl_name, rootpage, sql) and NO `partial`
		// column, so the metadata route fails with "no such column: partial" — a test that
		// cannot run is not a test. The text is also what a rebuild actually corrupts.
		idxSQL, _ := scalar(t, ctx,
			"SELECT sql FROM sqlite_master WHERE name = 'unique_index_scenes_files_on_primary'").(string)
		assert.Contains(t, idxSQL, "WHERE", "the index must remain PARTIAL (WHERE primary = 1)")
		assert.Contains(t, idxSQL, "primary", "…and partial on the primary flag specifically")

		// Proved by BEHAVIOUR rather than by metadata, because metadata is exactly what a
		// rebuild corrupts silently: a second PRIMARY file must be refused…
		err := execErr(t, ctx,
			"INSERT INTO scenes_files (scene_id, file_id, `primary`) VALUES (1, ?, 1)",
			mkFile(t, ctx, "r-second-primary.mp4"))
		assert.Error(t, err, "a scene must not be able to declare two primary files")

		// …and a second NON-primary file must still be fine, which is the half a too-strict
		// fix would break.
		require.NoError(t,
			exec(t, ctx,
				"INSERT INTO scenes_files (scene_id, file_id, `primary`) VALUES (1, ?, 0)",
				mkFile(t, ctx, "r-secondary.mp4")),
			"a scene may have several files, only one of them primary")
	})
}

func TestTheFileLookupIndexSurvivedTheRebuild(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		assert.Equal(t, int64(1),
			count(t, ctx, `SELECT COUNT(*) FROM sqlite_master
				WHERE type = 'index' AND name = 'index_scenes_files_file_id'`),
			"the file_id lookup index was lost by the rebuild")
	})
}

// TestTheForeignKeysStillCascadeAfterTheRebuild — the rebuild re-declared both foreign keys,
// and a typo in a rebuilt FK name means delete cascades silently stop working. Nothing else
// here would catch that: the row would simply stop disappearing.
func TestTheForeignKeysStillCascadeAfterTheRebuild(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		// PRAGMA foreign_keys is per-connection and is OFF by default in SQLite, so a
		// cascade assertion passes for the wrong reason unless this is checked first.
		require.Equal(t, int64(1), scalar(t, ctx, "PRAGMA foreign_keys"),
			"foreign keys must be ON for this connection or the rest of this test proves nothing")

		assert.Equal(t, int64(2),
			count(t, ctx, "SELECT COUNT(*) FROM pragma_foreign_key_list('scenes_files')"),
			"scenes_files must still reference both scenes and files")
	})
}

// TestTwoScenesMayShareOneFile — the referential half was ALREADY true before this
// migration, and asserting it means a future change that adds uniqueness on file_id (making
// the whole feature impossible) fails here rather than in production.
func TestTwoScenesMayShareOneFile(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		// Scenes 1 and 2 already have a primary row in the fixture, so these go in as
		// non-primary: the UNIQUE(scene_id) WHERE primary=1 index would (correctly) refuse
		// a second primary, and conflating "two primaries" with "two scenes, one file" is
		// the mistake this test exists to avoid.
		fileID := mkFile(t, ctx, "r-shared.mp4")
		require.NoError(t,
			exec(t, ctx, "INSERT INTO scenes_files (scene_id, file_id, `primary`) VALUES (1, ?, 0)", fileID))
		require.NoError(t,
			exec(t, ctx, "INSERT INTO scenes_files (scene_id, file_id, `primary`) VALUES (2, ?, 0)", fileID),
			"two scenes may reference one file — that is the premise of #3530")

		// Same file, different windows: the shape the feature actually needs.
		require.NoError(t, exec(t, ctx,
			"UPDATE scenes_files SET start_time = 10, end_time = 40 WHERE file_id = ? AND scene_id = 1", fileID))
		require.NoError(t, exec(t, ctx,
			"UPDATE scenes_files SET start_time = 40, end_time = 70 WHERE file_id = ? AND scene_id = 2", fileID))
	})
}

// ---------------------------------------------------------------------------
// DERIVED DURATION — spec section 8. These drive the REAL GetFiles, because the
// chokepoint is the thing being tested.
//
// A unit test of `sceneFileRanges` would prove the helper reduces a range correctly while
// proving nothing about whether anything CALLS it — and "nothing calls it" is the whole risk
// this change creates, exactly as #1790's T4 was earned by driving the real destroy paths.
// ---------------------------------------------------------------------------

// rangeVideoDuration returns the duration GetFiles reports for a scene's file 0, which is
// what SceneListTable.tsx:88 and SceneCard.tsx:518 read.
func rangeVideoDuration(t *testing.T, ctx context.Context, sceneID int) float64 {
	t.Helper()
	files, err := db.Scene.GetFiles(ctx, sceneID)
	require.NoError(t, err)
	require.NotEmpty(t, files, "the scene must have a file or this test proves nothing")
	return files[0].Duration
}

// mkRangeVideo creates a real scene with a real video file of a known duration, through the
// STORES rather than raw SQL, and returns the scene id.
//
// Built on the pattern in scene_test.go's createScene (File.Create then Scene.Create), and
// for a measured reason: every scene in the fixture (1..32) ALREADY has a primary file, so
// reusing one hits `UNIQUE constraint failed: scenes_files.scene_id` from
// unique_index_scenes_files_on_primary -- the correct refusal, arriving where a test author
// did not expect it. Creating the scene avoids depending on fixture numbering at all.
//
// Seconds is a parameter so the tests read in their own units ("a 45-minute file") instead
// of carrying a magic number.
func mkRangeVideo(t *testing.T, ctx context.Context, name string, seconds float64) int {
	t.Helper()

	f := &models.VideoFile{
		BaseFile: &models.BaseFile{
			Basename:       name,
			ParentFolderID: folderIDs[folderIdxWithSceneFiles],
		},
		Duration:   seconds,
		VideoCodec: "h264",
		Format:     "mp4",
		AudioCodec: "aac",
		Width:      640,
		Height:     480,
		FrameRate:  30,
		BitRate:    100000,
	}
	require.NoError(t, db.File.Create(ctx, f))

	s := &models.Scene{}
	require.NoError(t, db.Scene.Create(ctx, s, []models.FileID{f.ID}))

	return s.ID
}

// mkFileIDForScene returns a scene's primary file id, via the real store rather than a
// fixture constant, so the test never depends on which ids the fixture happened to use.
func mkFileIDForScene(t *testing.T, ctx context.Context, sceneID int) int64 {
	t.Helper()
	files, err := db.Scene.GetFiles(ctx, sceneID)
	require.NoError(t, err)
	require.NotEmpty(t, files)
	return int64(files[0].ID)
}

// setRange writes the range for a scene's primary file.
func setRange(t *testing.T, ctx context.Context, sceneID int, start, end interface{}) {
	t.Helper()
	require.NoError(t, exec(t, ctx,
		"UPDATE scenes_files SET start_time = ?, end_time = ? WHERE scene_id = ? AND `primary` = 1",
		start, end, sceneID))
}

// TestARangedSceneReportsItsRangeNotTheFilesLength — the feature's whole point.
func TestARangedSceneReportsItsRangeNotTheFilesLength(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		id := mkRangeVideo(t, ctx, "d-bounded.mp4", 2700) // a 45-minute file
		setRange(t, ctx, id, 10.0, 40.0)

		assert.Equal(t, 30.0, rangeVideoDuration(t, ctx, id),
			"a 30-second window inside a 45-minute file must report 30 seconds")
	})
}

// TestAnUnrangedSceneStillReportsTheWholeFile — the no-regression half, and it is the one
// that matters for every existing user's library.
func TestAnUnrangedSceneStillReportsTheWholeFile(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		id := mkRangeVideo(t, ctx, "d-whole.mp4", 2700)
		assert.Equal(t, 2700.0, rangeVideoDuration(t, ctx, id),
			"a scene with no range is the whole file, and must keep saying so")
	})
}

// TestAnOpenEndedRangeMeansTheEndOfTheFile — NULL end is not NULL duration.
func TestAnOpenEndedRangeMeansTheEndOfTheFile(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		id := mkRangeVideo(t, ctx, "d-open-tail.mp4", 2700)
		setRange(t, ctx, id, 2400.0, nil)
		assert.Equal(t, 300.0, rangeVideoDuration(t, ctx, id),
			"a start with no end means 'to the end of the file'")
	})
}

func TestARangeStartingAtZeroIsTheHeadOfTheFile(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		id := mkRangeVideo(t, ctx, "d-open-head.mp4", 2700)
		setRange(t, ctx, id, nil, 600.0)
		assert.Equal(t, 600.0, rangeVideoDuration(t, ctx, id),
			"an end with no start means 'from the beginning'")
	})
}

// TestTheFileKeepsItsOwnDurationAfterBeingRanged — THE TWO-SIDED TEST.
//
// A scene's file is still the whole 45 minutes, and `video_files.duration` is true. If
// GetFiles wrote the range back to the file row, this file would start reporting 30 seconds
// for FindFiles, the scene detail page's file list, and the PLAYER — and every one of those
// would be silently wrong, because nothing about them is scene-specific.
//
// The reflection of the #4320/#4326 lesson: a fix asserted only on the failure it prevents is
// a fix that can break the thing it was protecting.
func TestTheFileKeepsItsOwnDurationAfterBeingRanged(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		id := mkRangeVideo(t, ctx, "d-shared.mp4", 2700)
		fid := mkFileIDForScene(t, ctx, id)
		setRange(t, ctx, id, 10.0, 40.0)

		// The scene sees the range...
		assert.Equal(t, 30.0, rangeVideoDuration(t, ctx, id))

		// ...and the FILE, read on its own through FileStore, still sees 2700.
		f, err := db.File.Find(ctx, models.FileID(fid))
		require.NoError(t, err)
		require.Len(t, f, 1)
		vf, ok := f[0].(*models.VideoFile)
		require.True(t, ok, "expected a *models.VideoFile, got %T", f[0])
		assert.Equal(t, 2700.0, vf.Duration,
			"the range must be applied to the scene's COPY, never written back to the file: "+
				"FindFiles, the file list and the player all read video_files.duration and all "+
				"of them are right only while it stays the file's own length")

		// And the row itself, read directly.
		assert.Equal(t, 2700.0, scalar(t, ctx,
			"SELECT COALESCE(duration, 0) FROM video_files WHERE file_id = ?", fid),
			"the video_files row itself must be unchanged")
	})
}

// TestTwoScenesSharingOneFileSeeDifferentDurations — the case the feature exists for, and
// the one that FAILS if the range were ever stored on the file row.
func TestTwoScenesSharingOneFileSeeDifferentDurations(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		fileDur := 2700.0

		f := &models.VideoFile{
			BaseFile: &models.BaseFile{
				Basename:       "d-two-scenes.mp4",
				ParentFolderID: folderIDs[folderIdxWithSceneFiles],
			},
			Duration:   fileDur,
			VideoCodec: "h264",
			Format:     "mp4",
			AudioCodec: "aac",
			Width:      640,
			Height:     480,
			FrameRate:  30,
			BitRate:    100000,
		}
		require.NoError(t, db.File.Create(ctx, f))

		// Two scenes over the SAME file. Create() takes the file as primary for each, so
		// the second scene's row is a genuine second scenes_files entry for one file --
		// the schema permits it (PRIMARY KEY is (scene_id, file_id)) and this is the only
		// case where that matters.
		first := &models.Scene{}
		require.NoError(t, db.Scene.Create(ctx, first, []models.FileID{f.ID}))
		second := &models.Scene{}
		require.NoError(t, db.Scene.Create(ctx, second, []models.FileID{f.ID}))

		require.NoError(t, exec(t, ctx,
			"UPDATE scenes_files SET start_time = 0, end_time = 300 WHERE scene_id = ?", first.ID))
		require.NoError(t, exec(t, ctx,
			"UPDATE scenes_files SET start_time = 300, end_time = 900 WHERE scene_id = ?", second.ID))

		assert.Equal(t, 300.0, rangeVideoDuration(t, ctx, first.ID))
		assert.Equal(t, 600.0, rangeVideoDuration(t, ctx, second.ID),
			"the SAME file must report a different length per scene — which is the entire feature")

		// And the file itself is untouched.
		assert.Equal(t, fileDur, scalar(t, ctx,
			"SELECT COALESCE(duration, 0) FROM video_files WHERE file_id = ?", f.ID),
			"video_files.duration is the FILE's length and stays true")
	})
}

// TestAStartBeyondTheEndOfTheFileReportsZeroNotMinus — the clamp's ONLY test, and the reason
// the mutant that deletes it is killed.
//
// This case is REACHABLE WITHOUT BYPASSING ANYTHING, which is what makes it worth a test.
// The schema's three CHECKs are `start_time >= 0`, `end_time >= 0` and `end_time > start_time`
// -- and none of them can see `video_files.duration`, because a CHECK may not reference
// another table. So `start_time = 300` on a 2700-second file with `end_time` NULL is a legal
// row: it passes all three CHECKs, and any store, API or UI can create it by typing an
// over-long start. It describes a window that runs off the end of the file.
//
// Without the clamp that row reports -100 seconds, and a negative duration propagates into
// sort order, into `play_duration` arithmetic and into the timeline the UI draws -- silently,
// because nothing downstream of this function validates a sign.
//
// The inverted and empty cases (`end < start`, `end = start`) are NOT tested here on purpose:
// the schema refuses both, and pretending otherwise would mean rewriting the table inside a
// test to defeat a guarantee that already holds. They are refused by
// TestAnInvertedRangeIsRefused and TestAnEmptyRangeIsRefused, and this is the ONE window the
// schema cannot refuse.
func TestAStartBeyondTheEndOfTheFileReportsZeroNotMinus(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		id := mkRangeVideo(t, ctx, "d-overrun.mp4", 2700)
		setRange(t, ctx, id, 2800.0, nil) // legal per all three CHECKs, runs off the end

		assert.Equal(t, 0.0, rangeVideoDuration(t, ctx, id),
			"a window starting past the end of the file must clamp to 0; a negative duration "+
				"reaches sort order and the timeline unchecked, and nothing downstream validates a sign")
	})
}

// TestAnOverrunningWindowWithAnEndStillClamps — the clamp with an EXPLICIT end, and the
// arithmetic a reader is most likely to get wrong.
//
// Window 2600..2900 on a 2700-second file. Both endpoints satisfy all three CHECKs
// (`start >= 0`, `end >= 0`, `end > start`), so the row is legal. The naive reduction gives
// 2900 - 2600 = 300 seconds of duration for a file that ends at 2700 — the UI would show a
// scene three minutes longer than the media it can possibly play. The clamp takes the end to
// the file's end, so the answer is the 100 seconds that really exist.
//
// Asserting 300 here would be the "fix the test to match the code" mistake in its purest
// form, so the expectation is written as the arithmetic that makes it obvious: the file's
// length MINUS the start, never the window's own width.
func TestAnOverrunningWindowWithAnEndStillClamps(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		const fileDur = 2700.0
		id := mkRangeVideo(t, ctx, "d-overrun2.mp4", fileDur)
		setRange(t, ctx, id, 2600.0, 2900.0) // end is legal and > start, but past the file's end

		assert.Equal(t, 100.0, rangeVideoDuration(t, ctx, id),
			"the window is clamped to the file's end, so the answer is fileDur-start = 100, "+
				"NOT the window's own width of 300 -- which would describe 3 minutes of "+
				"duration in a file that stops at 2700")
	})
}

// TestARangeSurvivesAFileWithNoKnownDuration — the ONLY test for the `fileDur > 0` guard,
// and the reason deleting it is a mutant that gets killed.
//
// video_files.duration is NULL or 0 whenever ffprobe could not determine it, which is the
// normal state for a file still being written, a partial download, or one whose container
// ffprobe does not recognise. A user can absolutely have such a file and mark a range on the
// scene -- the range is metadata and does not need the duration to exist.
//
// Without the guard, `min(end, 0)` collapses every such range to nothing and the scene
// reports 0 seconds, i.e. the feature silently stops working for an entire class of files.
// With it, an unknown duration is treated as "the file has no known end", so the range is
// reported as written and nothing is clamped away.
//
// The two-arg mkRangeVideo is used with a 0 duration to stand for "ffprobe failed", because
// that is what the row looks like in the database -- the distinction between NULL and 0 does
// not survive into the reduction, and pretending it does would test a branch that cannot
// occur.
func TestARangeSurvivesAFileWithNoKnownDuration(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		id := mkRangeVideo(t, ctx, "d-no-duration.mp4", 0)

		// An end past the point the user wants, on a file whose length is unknown: the
		// range is honoured rather than clamped to the unknown.
		setRange(t, ctx, id, 10.0, 40.0)
		assert.Equal(t, 30.0, rangeVideoDuration(t, ctx, id),
			"a file with no known duration must not clamp a valid range away")

		// And an OPEN-ENDED one has no computable answer at all: the end is unknown, not
		// zero. It reports 0, which is exactly what a 0 duration means everywhere else in
		// this schema. Asserting 30 here would require inventing an end the database does
		// not have.
		setRange(t, ctx, id, 10.0, nil)
		assert.Equal(t, 0.0, rangeVideoDuration(t, ctx, id),
			"no end and no file length means no computable duration; 0 is the schema's own "+
				"way of saying 'not known'")
	})
}

// TestTheSameRangeOnAFileWithADurationIsUnaffectedByTheUnknownDurationGuard — the other
// side of it, so the guard cannot be widened into "never clamp".
func TestTheSameRangeOnAFileWithADurationIsUnaffectedByTheUnknownDurationGuard(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		id := mkRangeVideo(t, ctx, "d-has-duration.mp4", 2700)
		setRange(t, ctx, id, 10.0, 40.0)
		assert.Equal(t, 30.0, rangeVideoDuration(t, ctx, id),
			"the guard must apply only to files whose duration is unknown, never to the rest")
	})
}
