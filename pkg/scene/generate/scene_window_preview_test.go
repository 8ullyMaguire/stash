package generate

// stash#3530 — the preview's tile grid must be anchored on the scene's window.
//
// The grid itself was already correct in SHAPE: getStepSizeAndOffset divides videoDuration, which
// #3530 made the window's length, so the tiles divide the SCENE. What was wrong is that each tile's
// `time` is an ABSOLUTE seek, and the grid was anchored at 0 of the FILE -- so every tile of a
// windowed scene came from before the scene began.
//
// The fix is one rebase of `offset`, not one per chunk: the loop computes `offset + i*stepSize`, so
// adding the window's start inside the loop would repeat the same arithmetic N times.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTheSegmentGridIsShiftedToTheWindow — the multi-segment path.
func TestTheSegmentGridIsShiftedToTheWindow(t *testing.T) {
	// A 300s window starting at 300s, previewed as 4 segments.
	opts := PreviewOptions{
		Segments:     4,
		Window:       SceneWindow{Start: 300, End: 600, Set: true},
		ExcludeStart: "0",
	}

	stepSize, offset := opts.getStepSizeAndOffset(300)
	assert.Equal(t, 75.0, stepSize, "300s of scene across 4 segments is 75s each")

	rebased := opts.Window.rebaseExclude(offset)
	assert.Equal(t, 300.0, rebased,
		"the FIRST tile must start at the window's start (300), not at 0 of the file. Without the "+
			"rebase the whole preview is the first 300s of the file rather than the scene")

	// Every subsequent tile follows from the same rebase, so the last one lands at the window's end.
	last := rebased + 3*stepSize
	assert.Equal(t, 525.0, last,
		"tile 3 of a 300..600 window starts at 525 and runs 75s, ending exactly at the window's end")
}

// TestAnExcludedHeadShiftsWithTheWindow — excludeStart is a PROPORTION ("5%") of videoDuration,
// which is now the window's length. So it skips the head of the SCENE, and it must rebase by the
// same rule as the grid's own offset.
//
// This is a second, subtler failure: with excludeStart "10%" on a window at 300, the skip should be
// 300 + 30 = 330, not 30.
func TestAnExcludedHeadShiftsWithTheWindow(t *testing.T) {
	opts := PreviewOptions{
		Segments:     4,
		Window:       SceneWindow{Start: 300, End: 600, Set: true},
		ExcludeStart: "10%",
	}

	_, offset := opts.getStepSizeAndOffset(300)
	assert.Equal(t, 30.0, offset, "10% of the 300s window is 30s, relative to the window")

	assert.Equal(t, 330.0, opts.Window.rebaseExclude(offset),
		"the excluded head must be the first 10%% OF THE SCENE (30s from its start, i.e. 330s in "+
			"the file), not the first 10%% of the file (30s)")
}

// TestAnUnrangedPreviewIsCompletelyUnchanged — the guarantee. An unranged scene's grid must be
// bit-identical to what it always was, or every preview in every existing installation moves.
func TestAnUnrangedPreviewIsCompletelyUnchanged(t *testing.T) {
	opts := PreviewOptions{
		Segments:     4,
		ExcludeStart: "10%",
		// no Window
	}

	stepSize, offset := opts.getStepSizeAndOffset(300)

	// ExcludeStart "10%" removes 30s, so the tiles divide 270s, not 300s.
	// (I wrote 300/4 = 75 here first; the excluded head is why it is 67.5.)
	assert.Equal(t, 67.5, stepSize, "10% of 300s is excluded, so 270s is divided into 4 tiles")
	assert.Equal(t, 30.0, offset)
	assert.Equal(t, 30.0, opts.Window.rebaseExclude(offset),
		"an unranged scene's offset must be untouched -- rebaseExclude is the identity when "+
			"there is no window")
	assert.Equal(t, 0.0, opts.Window.startOf(),
		"an unranged scene's preview still starts at 0")
}

// TestTheSingleChunkPreviewSeeksIntoTheWindow — the short-scene path, via singleChunkPlan.
//
// Two mutation survivors led here, and both were the same mistake as the tilePlan pair: the test
// asserted `startOf()` (a helper) instead of the chunk the production path actually builds. The
// single-chunk path has no grid to rebase, so if its one StartTime is wrong the preview of a short
// windowed scene is footage from before the scene begins — and nothing else in the suite would see
// it, because every other test uses the grid path.
//
// The numbers: 3 segments of 10s = a 30s budget, so a 5s scene takes the single path. (My first
// version used 10 segments of 10s against a 300s scene — a 100s budget and a scene LONGER than it,
// so it took the grid path and the assertion never applied.)
func TestTheSingleChunkPreviewSeeksIntoTheWindow(t *testing.T) {
	const sceneLength = 5.0
	opts := PreviewOptions{
		Segments:        3,
		SegmentDuration: 10,
		Window:          SceneWindow{Start: 300, End: 305, Set: true},
	}

	require.True(t, opts.isSingleChunk(sceneLength),
		"this test only applies to the single-chunk path: a %vs scene against a %vs budget",
		sceneLength, opts.SegmentDuration*float64(opts.Segments))

	chunk := opts.singleChunkPlan(sceneLength, "/tmp/out.mp4")

	assert.Equal(t, 300.0, chunk.StartTime,
		"a single-chunk preview of a window at 300 must seek to 300. With StartTime left at 0, a "+
			"5s scene at 300s previewed the first 5s of the FILE")
	assert.Equal(t, sceneLength, chunk.Duration,
		"the duration is the scene's length, which videoDuration already is")
	assert.Equal(t, "/tmp/out.mp4", chunk.OutputPath)
}

// TestAnUnrangedSingleChunkPreviewIsUnchanged — same guarantee, other direction.
func TestAnUnrangedSingleChunkPreviewIsUnchanged(t *testing.T) {
	opts := PreviewOptions{Segments: 3, SegmentDuration: 10}

	chunk := opts.singleChunkPlan(5, "/tmp/out.mp4")
	assert.Equal(t, 0.0, chunk.StartTime,
		"an unranged scene's preview still starts at 0, exactly as before #3530")
	assert.Equal(t, 5.0, chunk.Duration)
}

// TestTheSingleChunkBoundaryIsUnchanged — #2496's threshold, unchanged by this work. It is easy to
// shift a boundary while threading a window through, and the two sides behave differently.
func TestTheSingleChunkBoundaryIsUnchanged(t *testing.T) {
	opts := PreviewOptions{Segments: 3, SegmentDuration: 10}
	const budget = 30.0

	assert.True(t, opts.isSingleChunk(29.9), "just under the budget is a single chunk")
	assert.False(t, opts.isSingleChunk(30.0), "exactly at the budget is not (unchanged from #2496)")
	assert.False(t, opts.isSingleChunk(30.1), "over the budget gets the grid")
}

// TestAWindowAtZeroStillSeeksToZero — and says so.
//
// startOf() returns 0 for both an unranged scene and a window that starts at 0, so this test cannot
// distinguish them, and that is FINE for a seek (both begin at 0). It is not fine for At(), which is
// why At() takes a proportion and startOf() does not. Worth stating so nobody "unifies" the two.
func TestAWindowAtZeroStillSeeksToZero(t *testing.T) {
	assert.Equal(t, 0.0, SceneWindow{Set: true}.startOf())
	assert.Equal(t, 0.0, SceneWindow{}.startOf())

	// But At() DOES distinguish them: a window of 0..60 must sample inside itself.
	zeroStart := SceneWindow{Start: 0, End: 60, Set: true}
	assert.Equal(t, 12.0, zeroStart.At(0.2, 60),
		"20%% into a 0..60 window is 12s, and that is a different answer from the unranged 0.2*60 "+
			"only by accident -- the point is At() must use the window's own length")
}

// TestTheTilesAreSampledFromInsideTheWindow — the test that finally closed the two survivors.
//
// The earlier version of this test asserted the window HELPERS directly and then re-implemented the
// rebase in the test body. Both are the same mistake: the helpers were proven correct and the test
// never touched the code that runs, so reverting `previewVideo`'s calls changed no result and both
// mutants survived a sweep.
//
// So this goes through `options.tilePlan(...)` — the function the chunk loop calls — and reads
// `plan.time(i)`. Reverting the production call site is now impossible without failing this.
func TestTheTilesAreSampledFromInsideTheWindow(t *testing.T) {
	opts := PreviewOptions{
		Segments:     4,
		ExcludeStart: "0",
		Window:       SceneWindow{Start: 300, End: 600, Set: true},
	}

	plan := opts.tilePlan(300)
	assert.Equal(t, 75.0, plan.stepSize, "300s of scene across 4 tiles")

	var times []float64
	for i := 0; i < opts.Segments; i++ {
		times = append(times, plan.time(i))
	}

	assert.Equal(t, []float64{300, 375, 450, 525}, times,
		"every tile must be sampled from INSIDE the window. Un-rebased they would be "+
			"{0, 75, 150, 225} -- the first 300s of the FILE, none of which is the scene")

	assert.GreaterOrEqual(t, times[0], 300.0, "no tile may start before the window does")
	assert.LessOrEqual(t, times[len(times)-1]+plan.stepSize, 600.0,
		"and no tile may run past the window's end")

	// The unranged case must be untouched by the whole arrangement.
	plain := PreviewOptions{Segments: 4, ExcludeStart: "0"}
	plainPlan := plain.tilePlan(300)
	var plainTimes []float64
	for i := 0; i < plain.Segments; i++ {
		plainTimes = append(plainTimes, plainPlan.time(i))
	}
	assert.Equal(t, []float64{0, 75, 150, 225}, plainTimes,
		"an unranged scene must sample the file exactly as it always did")
}

// TestTheExcludedHeadShiftsInThePlanToo — same argument for excludeStart, through the plan.
//
// excludeStart is a PROPORTION ("10%") of videoDuration, which is now the window's length. It skips
// the head of the SCENE, so with a window at 300 it must skip to 330, not to 30.
func TestTheExcludedHeadShiftsInThePlanToo(t *testing.T) {
	opts := PreviewOptions{
		Segments:     4,
		ExcludeStart: "10%",
		Window:       SceneWindow{Start: 300, End: 600, Set: true},
	}

	plan := opts.tilePlan(300)
	assert.Equal(t, 330.0, plan.time(0),
		"the first tile must start at 300 + 10%% of the scene (30s) = 330s. Without the rebase it "+
			"starts at 30s, which is 270s before the scene begins")
	assert.Equal(t, 67.5, plan.stepSize,
		"and the excluded head is still excluded from the span: 270s across 4 tiles")
}
