//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// Scene time ranges (migration 122) make a file hold several scenes, and the phash belongs
// to the FILE -- so two scenes over one file have an IDENTICAL hash and identical duration.
// Both FindDuplicates branches therefore reported every scene of a split file as a duplicate
// of every other one, feeding the duplicate checker and its cleanup tooling.
//
// These tests are DIFFERENTIAL in the same way the sort tests are, because a positional
// assertion here is satisfied by the fixture: the library already contains real duplicate
// groups ([1 31] and [2 32] as measured), so "no duplicates at all" would be the wrong
// assertion. Each test therefore names the SPECIFIC pair it added and asks whether that pair
// is in any group.

// dupFixture creates `scenes` scenes over ONE file of `seconds`, split into equal windows,
// all sharing one phash. Returns the scene ids and the file id.
func dupFixture(t *testing.T, ctx context.Context, name string, seconds float64, scenes int, phash int64) ([]int, models.FileID) {
	t.Helper()

	f := mkRangeVideoFile(t, ctx, name, seconds)
	require.NoError(t, exec(t, ctx,
		"INSERT OR REPLACE INTO files_fingerprints (file_id, type, fingerprint) VALUES (?, 'phash', ?)",
		f.ID, phash))

	window := seconds / float64(scenes)
	ids := make([]int, 0, scenes)
	for i := 0; i < scenes; i++ {
		s := newSceneOver(t, ctx, f)
		require.NoError(t, exec(t, ctx,
			"UPDATE scenes_files SET start_time = ?, end_time = ? WHERE scene_id = ?",
			float64(i)*window, float64(i+1)*window, s))
		ids = append(ids, s)
	}
	return ids, f.ID
}

// pairFound reports whether a and b appear in the SAME duplicate group.
func pairFound(groups [][]int, a, b int) bool {
	for _, g := range groups {
		hasA, hasB := false, false
		for _, id := range g {
			if id == a {
				hasA = true
			}
			if id == b {
				hasB = true
			}
		}
		if hasA && hasB {
			return true
		}
	}
	return false
}

func sceneIDsOf(groups [][]*models.Scene) [][]int {
	out := make([][]int, 0, len(groups))
	for _, g := range groups {
		row := make([]int, 0, len(g))
		for _, s := range g {
			row = append(row, s.ID)
		}
		out = append(out, row)
	}
	return out
}

// TestTwoScenesOfOneFileAreNotDuplicates — the defect, on BOTH branches.
//
// Measured before the fix: one 1800s file split into two windows, one phash, produced the
// group [33 34] -- the two segments of the one file.
func TestTwoScenesOfOneFileAreNotDuplicates(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		ids, _ := dupFixture(t, ctx, "split-dup.mp4", 1800, 2, 1234567890123456789)
		require.Len(t, ids, 2)

		// distance == 0 -> the SQL branch
		sqlGroups, err := db.Scene.FindDuplicates(ctx, 0, -1, nil)
		require.NoError(t, err)
		assert.False(t, pairFound(sceneIDsOf(sqlGroups), ids[0], ids[1]),
			"two scenes over ONE file must not be duplicates of each other: the phash is the "+
				"FILE's, so their distance is 0 by construction. Groups were %v", sceneIDsOf(sqlGroups))

		// distance > 0 -> the in-memory branch
		memGroups, err := db.Scene.FindDuplicates(ctx, 10, -1, nil)
		require.NoError(t, err)
		assert.False(t, pairFound(sceneIDsOf(memGroups), ids[0], ids[1]),
			"the in-memory branch must skip same-file neighbours too. Groups were %v",
			sceneIDsOf(memGroups))
	})
}

// TestManyScenesOfOneFileAreNotDuplicates — the same, at three, because the SQL branch's
// `COUNT(DISTINCT file_id)` and the in-memory branch's neighbour skip are different mechanisms
// and a bug in either shows at a different count.
func TestManyScenesOfOneFileAreNotDuplicates(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		ids, _ := dupFixture(t, ctx, "split3-dup.mp4", 1800, 3, 1234567890123456789)
		require.Len(t, ids, 3)

		sqlGroups, err := db.Scene.FindDuplicates(ctx, 0, -1, nil)
		require.NoError(t, err)
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				assert.False(t, pairFound(sceneIDsOf(sqlGroups), ids[i], ids[j]),
					"scenes %d and %d are two windows of ONE file and must not be duplicates",
					ids[i], ids[j])
			}
		}

		memGroups, err := db.Scene.FindDuplicates(ctx, 10, -1, nil)
		require.NoError(t, err)
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				assert.False(t, pairFound(sceneIDsOf(memGroups), ids[i], ids[j]),
					"in-memory branch: scenes %d and %d are two windows of ONE file",
					ids[i], ids[j])
			}
		}
	})
}

// TestTwoIdenticalCopiesAreStillDuplicates — THE TWO-SIDED HALF, and the reason the fix is
// `COUNT(DISTINCT file_id)` rather than "distance must be > 0".
//
// Two byte-identical COPIES of the same video are the single most common real duplicate, and
// a fix that required a non-zero hash distance would silently stop finding it -- while every
// same-file test above kept passing. A file that legitimately appears twice in a library is
// invisible to the split-file test and must be pinned separately.
func TestTwoIdenticalCopiesAreStillDuplicates(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		const phash = 987654321098765432
		const seconds = 1800.0

		// Two SEPARATE files (a copy), each with its own scene, sharing one phash.
		copy1 := mkRangeVideoFile(t, ctx, "copy1-dup.mp4", seconds)
		copy2 := mkRangeVideoFile(t, ctx, "copy2-dup.mp4", seconds)
		for _, id := range []models.FileID{copy1.ID, copy2.ID} {
			require.NoError(t, exec(t, ctx,
				"INSERT OR REPLACE INTO files_fingerprints (file_id, type, fingerprint) VALUES (?, 'phash', ?)",
				id, phash))
		}

		s1 := newSceneOver(t, ctx, copy1)
		s2 := newSceneOver(t, ctx, copy2)

		sqlGroups, err := db.Scene.FindDuplicates(ctx, 0, -1, nil)
		require.NoError(t, err)
		assert.True(t, pairFound(sceneIDsOf(sqlGroups), s1, s2),
			"two IDENTICAL files with the same phash ARE duplicates and must still be found; "+
				"they differ in file_id, which is exactly what COUNT(DISTINCT file_id) tests. "+
				"Groups were %v", sceneIDsOf(sqlGroups))

		memGroups, err := db.Scene.FindDuplicates(ctx, 10, -1, nil)
		require.NoError(t, err)
		assert.True(t, pairFound(sceneIDsOf(memGroups), s1, s2),
			"the in-memory branch must still find two identical files. Groups were %v",
			sceneIDsOf(memGroups))
	})
}

// TestASplitFileAndACopyOfItInTheSameLibrary — the mixed case, and the one that decides the
// shape of the whole fix: three segments of one file, plus an untouched COPY of that file.
//
// This is where "just add HAVING COUNT(DISTINCT file_id) > 1" was measured to be insufficient
// (the group came back [33 34 35 36] with the three segments dragged along), and where
// "GROUP BY phash, file_id" was measured to break the copy pair (two singletons, both
// discarded). What actually works is GROUP BY phash plus a WHERE gate on the phash.
//
// ## THE LIMITATION THIS TEST PINS, RATHER THAN PRETENDS AWAY
//
// The returned group is [33 34 35 36] -- the segments AND the copy, together. The three
// segments are not duplicates OF EACH OTHER; they are duplicates OF THE COPY. The checker's
// data shape is `[][]*Scene`, one INDEPENDENT duplicate set per inner slice, and the UI
// (SceneDuplicateChecker) treats each set that way -- so a per-pair relation cannot be
// expressed at all. Two honest choices exist:
//
//	(a) return one set of four, telling the user these are duplicates of one another
//	(b) return nothing, hiding a real duplicate
//
// (a) is what ships. The user is shown four scenes and asked which to delete, which is the
// decision they actually have to make; (b) would silently lose a duplicate it can see.
//
// So the assertion is that the copy IS grouped with the segments, and the test's NAME says
// what the group means. Asserting the segments are absent would fail against correct code,
// and asserting nothing at all would let a regression hide a real duplicate.
func TestASplitFileAndACopyOfItInTheSameLibrary(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		const phash = 555555555555555555
		const seconds = 1800.0

		original := mkRangeVideoFile(t, ctx, "mixed-original.mp4", seconds)
		copied := mkRangeVideoFile(t, ctx, "mixed-copy.mp4", seconds)
		for _, id := range []models.FileID{original.ID, copied.ID} {
			require.NoError(t, exec(t, ctx,
				"INSERT OR REPLACE INTO files_fingerprints (file_id, type, fingerprint) VALUES (?, 'phash', ?)",
				id, phash))
		}

		// The original, split into three windows.
		window := seconds / 3
		segments := make([]int, 0, 3)
		for i := 0; i < 3; i++ {
			s := newSceneOver(t, ctx, original)
			require.NoError(t, exec(t, ctx,
				"UPDATE scenes_files SET start_time = ?, end_time = ? WHERE scene_id = ?",
				float64(i)*window, float64(i+1)*window, s))
			segments = append(segments, s)
		}

		// The untouched copy, as its own scene.
		copyScene := newSceneOver(t, ctx, copied)

		groups := sceneIDsOf(sqlGroups(t, ctx))

		// The copy is a real duplicate of every segment and MUST be reported -- this is the
		// half that stops a regression from quietly disabling duplicate detection.
		for _, seg := range segments {
			assert.True(t, pairFound(groups, seg, copyScene),
				"segment %d and the untouched copy %d ARE duplicates -- same phash, different "+
					"file_id -- and must still be reported. Dropping the group instead would "+
					"hide a real duplicate from the user. Groups were %v", seg, copyScene, groups)
		}

		// And the phash-level gate must be doing the work: a library with ONLY the split file
		// reports nothing (TestManyScenesOfOneFileAreNotDuplicates), so the gate is what makes
		// the difference between "one file split" and "two copies".
		assert.True(t, pairFound(groups, copyScene, segments[0]),
			"the copy and the first segment must share a group")
		assert.NotEqual(t, original.ID, copied.ID,
			"the fixture must hold TWO distinct files or this test measures nothing")
	})
}

// TestASplitFileAloneIsNotReportedEvenWithOtherFilesInTheLibrary — the gate's other half, and
// the one that would fail if the WHERE clause were removed and replaced by a bare
// `HAVING COUNT(phash) > 1`.
//
// The library here contains OTHER files (the fixture's, plus its own real duplicate pairs
// [1 31] and [2 32] as measured). The split file's phash must still be dropped, because no
// OTHER file carries it -- the gate asks "does this phash occur on more than one file", not
// "are there other files in the library".
func TestASplitFileAloneIsNotReportedEvenWithOtherFilesInTheLibrary(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		ids, _ := dupFixture(t, ctx, "lonely-split.mp4", 1800, 3, 424242424242424242)

		groups := sceneIDsOf(sqlGroups(t, ctx))
		for i := 0; i < len(ids); i++ {
			assert.False(t, containsScene(groups, ids[i]),
				"scene %d is a window of a file whose phash is unique in the library, so it must "+
					"not be reported at all -- other files existing is irrelevant. Groups were %v",
				ids[i], groups)
		}

		// Sanity: the fixture's REAL duplicate groups are still there, so the gate above is
		// not passing because everything is being suppressed.
		assert.NotEmpty(t, groups,
			"the fixture library contains real duplicate pairs, so a gate that suppresses "+
				"everything would show up here as an empty result rather than as a pass")
	})
}

// containsScene reports whether sceneID appears in ANY group.
func containsScene(groups [][]int, sceneID int) bool {
	for _, g := range groups {
		for _, id := range g {
			if id == sceneID {
				return true
			}
		}
	}
	return false
}

// sqlGroups is a tiny wrapper so the tests above read as one assertion block.
func sqlGroups(t *testing.T, ctx context.Context) [][]*models.Scene {
	t.Helper()
	g, err := db.Scene.FindDuplicates(ctx, 0, -1, nil)
	require.NoError(t, err)
	return g
}

// TestOneSceneOverTwoFilesWithTheSamePhashIsNotADuplicatePair — documents WHY
// `COUNT(DISTINCT scene_id) > 1` is redundant in the SQL, measured rather than assumed.
//
// The `phash IN (...)` gate asks whether a phash occurs under more than one FILE. That is not
// the same as two SCENES: one scene can have two files (a stitched video, a video plus an
// attached archive), and if both carry the same phash the gate passes while the group holds a
// single scene. Measured inner rows for that shape: two rows, ONE scene_id, gate satisfied.
//
// So the HAVING looks load-bearing. **It is not, and a mutation sweep proved it:** deleting
// `AND COUNT(DISTINCT scene_id) > 1` leaves every result in this file IDENTICAL.
//
// The real guard is in the Go below the SQL, and predates this work:
//
//	sceneIds = sliceutil.AppendUnique(sceneIds, intId)      // DISTINCT
//	...
//	if len(sceneIds) > 1 { dupeIds = append(dupeIds, sceneIds) }   // the guard
//
// `GROUP_CONCAT(DISTINCT scene_id)` collapses the two rows to the single value "33", and the
// `len > 1` check discards the group. So the singleton is already excluded and the SQL HAVING
// is defence in depth that nothing can distinguish from the Go check.
//
// The HAVING is KEPT anyway, and the reason is worth stating: it makes the SQL correct on its
// own, so the query is not silently dependent on a filter further down the same file. Removing
// it would save nothing and make the statement wrong if that Go check were refactored.
// **This test is the record of that decision** — without it, the next sweep to report the
// HAVING as a survivor would "clean it up".
func TestOneSceneOverTwoFilesWithTheSamePhashIsNotADuplicatePair(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		f1 := mkRangeVideoFile(t, ctx, "two-files-1.mp4", 600)
		f2 := mkRangeVideoFile(t, ctx, "two-files-2.mp4", 600)
		const phash = 112233445566778899
		for _, id := range []models.FileID{f1.ID, f2.ID} {
			require.NoError(t, exec(t, ctx,
				"INSERT OR REPLACE INTO files_fingerprints (file_id, type, fingerprint) VALUES (?, 'phash', ?)",
				id, phash))
		}

		// ONE scene, TWO files, same phash: the gate passes, the group is a single scene.
		s := newSceneOver(t, ctx, f1)
		require.NoError(t, exec(t, ctx,
			"INSERT INTO scenes_files (scene_id, file_id, `primary`) VALUES (?, ?, 0)", s, f2.ID))

		// Assert the premise, because the whole explanation rests on it: the inner query
		// really does produce two rows carrying ONE scene_id.
		rows := scalar(t, ctx, `SELECT group_concat(scene_id || ':' || file_id) FROM (
			SELECT scenes.id as scene_id, scenes_files.file_id as file_id,
			       files_fingerprints.fingerprint as phash
			FROM scenes
			LEFT JOIN scenes_files ON scenes.id = scenes_files.scene_id
			LEFT JOIN files ON scenes_files.file_id = files.id
			LEFT JOIN files_fingerprints ON scenes_files.file_id = files_fingerprints.file_id
			    AND files_fingerprints.type = 'phash'
			WHERE files_fingerprints.fingerprint = ?)`,
			int64(phash))
		require.Equal(t, fmt.Sprintf("%d:%d,%d:%d", s, f1.ID, s, f2.ID), rows,
			"the premise: one scene over two same-phash files must produce TWO rows with ONE "+
				"scene_id, or this test is not exercising the case it claims to")

		groups := sceneIDsOf(sqlGroups(t, ctx))
		assert.False(t, containsScene(groups, s),
			"one scene over two same-phash files is ONE scene and must not be reported as a "+
				"duplicate SET of itself; the phash gate sees two FILES and passes, but a group "+
				"holding a single scene tells the user nothing they can act on. Groups were %v",
			groups)
	})
}
