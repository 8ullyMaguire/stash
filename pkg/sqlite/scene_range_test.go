//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
