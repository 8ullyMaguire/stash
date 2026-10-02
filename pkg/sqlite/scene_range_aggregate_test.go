//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTheLibraryDurationDoesNotDoubleCountASplitFile — the promise every one of the five
// rewired sites makes.
//
// One 2700-second file, split into three scenes of 900s each:
//
//	before   the total grows by 2700 + 2700 + 2700 = 8100   -- three times the media
//	after    the total grows by  900 +  900 +  900 = 2700   -- the file's real length
//
// The SUM is over `scenes_files`, so a split file is counted once PER SCENE. That is the
// defect migration 122 made reachable, and it is why Duration() had to change rather than
// merely could.
//
// **The assertion is a DELTA, not an absolute.** Duration() sums the entire library, so an
// `== 2700` assertion would be measuring the fixture: it fails for the wrong reason here, and
// on a differently-seeded fixture it would pass for the wrong reason. The baseline is taken
// before the three scenes exist, so the delta isolates this file's contribution and nothing
// else.
//
// Paired with TestTwoScenesSharingOneFileSeeDifferentDurations, which covers the case a
// de-duplicating fix would break: one file legitimately backing several scenes with PARTIAL
// ranges must contribute the SUM of those ranges, not the file's length once.
func TestTheLibraryDurationDoesNotDoubleCountASplitFile(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		const fileDur = 2700.0

		baseline, err := db.Scene.Duration(ctx)
		require.NoError(t, err)

		// ONE file, THREE scenes over it, 900 seconds each.
		f := mkRangeVideoFile(t, ctx, "split.mp4", fileDur)

		// Each scene sees its own window, checked by ID: the aggregate above is the sum of
		// THESE numbers, so asserting the sum alone would pass on an aggregate that does
		// not match the rows the user sees.
		sceneIDs := make([]int, 0, 3)
		for i := 0; i < 3; i++ {
			s := newSceneOver(t, ctx, f)
			sceneIDs = append(sceneIDs, s)
			require.NoError(t, exec(t, ctx,
				"UPDATE scenes_files SET start_time = ?, end_time = ? WHERE scene_id = ?",
				float64(i*900), float64((i+1)*900), s))
			assert.Equal(t, 900.0, rangeVideoDuration(t, ctx, s),
				"scene %d must see its own 900s window before it is summed", i)
		}

		total, err := db.Scene.Duration(ctx)
		require.NoError(t, err)

		assert.Equal(t, fileDur, total-baseline,
			"a 2700s file split into three 900s scenes must add 2700s to the library total, "+
				"not 8100s: the SUM is over scenes_files, so without the range each scene "+
				"contributes the WHOLE file and the total triples")
	})
}

// TestAnUnsplitLibraryTotalsExactlyAsBefore — the regression half. Every existing library has
// NULL ranges, so the fragment must reduce to `video_files.duration` for all of it. This is
// the property that makes migration 122 safe to ship, and it is the thing that would silently
// break every existing install if the fragment's unranged branch were wrong.
func TestAnUnsplitLibraryTotalsExactlyAsBefore(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		// A fresh scene with no range at all, added to the fixture library.
		id := mkRangeVideo(t, ctx, "unsplit.mp4", 1234.5)

		withRange, err := db.Scene.Duration(ctx)
		require.NoError(t, err)

		// Set a range covering the whole file: the number must not move.
		require.NoError(t, exec(t, ctx,
			"UPDATE scenes_files SET start_time = 0, end_time = 1234.5 WHERE scene_id = ?", id))
		spelledOut, err := db.Scene.Duration(ctx)
		require.NoError(t, err)

		assert.Equal(t, withRange, spelledOut,
			"a range that spells out the whole file must total the same as no range at all -- "+
				"which is why the fragment's first branch returns duration unchanged")

		// And that the scene really was counted, so the comparison is not vacuous.
		assert.Greater(t, spelledOut, 1234.0,
			"the fixture library plus this file must exceed 1234s, or the equality above "+
				"would hold because nothing was summed at all")
	})
}
