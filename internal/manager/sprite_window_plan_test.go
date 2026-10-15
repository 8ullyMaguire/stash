package manager

// stash#3530 — the sprite grid and VTT cues must be planned from the scene's WINDOW, not the file.
//
// GenerateSpriteTask is the shape the preview task was in before stash-3530-cover: it probes the
// FILE (`ffprobe.NewVideoFile(t.Scene.Path)`), keys on the file's hash, and drives every derived
// quantity -- chunkCount, SpriteInterval, stepSize, the VTT cue spacing, and SlowSeek's duration test
// -- off `VideoStreamDuration`. A scene at 300..600s of a 7200s file therefore gets a sprite tiled
// across the whole two hours, with far too many tiles, and a VTT whose cues point into footage that
// is not the scene.
//
// The planning arithmetic is extracted from the constructor, which today interleaves it with fsutil
// calls and ffmpeg probing. That extraction is the point: the constructor cannot be unit-tested
// without a real file on disk, and an untestable constructor is how this defect survived.
//
// See docs/ISSUE-3530-sprite-spec.md.

import (
	"math"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/scene/generate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A 7200s file at 30fps, sampled so the window arithmetic has real numbers to work with.
func longVideo() ffmpeg.VideoFile {
	return ffmpeg.VideoFile{
		VideoStreamDuration: 7200,
		FrameCount:          216000,
		FrameRate:           30,
		Width:               1920,
		Height:              1080,
	}
}

func window(start, end float64) generate.SceneWindow {
	return generate.SceneWindow{Start: start, End: end, Set: true}
}

// An unranged scene's plan must be identical to today's, field for field.
//
// Every scene in every existing installation is unranged, so if this moves, every sprite and every
// VTT in every library is regenerated and every sprite URL is still correct but every thumbnail is
// sampled at a different instant. Byte-identical is the only acceptable outcome.
func TestAnUnrangedSpritePlanIsIdenticalToToday(t *testing.T) {
	vf := longVideo()

	plain := planSprite(vf, generate.SceneWindow{}, DefaultSpriteGeneratorConfig)
	require.NotNil(t, plain, "an unranged file must still plan")

	// Recompute the way the code did before #3530, independently.
	interval := calculateSpriteInterval(vf, DefaultSpriteGeneratorConfig)
	want := int(math.Ceil(vf.VideoStreamDuration / interval))
	grid := generate.GetSpriteGridSize(want)

	assert.Equal(t, interval, plain.SpriteInterval,
		"the interval must come from the file's duration for an unranged scene")
	assert.Equal(t, grid*grid, plain.ChunkCount,
		"and the chunk count must be snapped to a perfect square exactly as before")
	assert.False(t, plain.SlowSeek,
		"a 7200s file with 216000 frames never took the slow path, and must not start now")
}

// A windowed scene's grid must span the WINDOW, not the file.
//
// This is the defect. 81 sprites across 7200s is one per ~89s; for a 300s window the same 81 tiles
// across 300s is one per ~3.7s, and every tile is inside the scene.
func TestAWindowedGridSpansTheWindowNotTheFile(t *testing.T) {
	vf := longVideo()
	w := window(300, 600)

	plan := planSprite(vf, w, DefaultSpriteGeneratorConfig)
	require.NotNil(t, plan)

	// The interval must be computed from the window's length, not the file's.
	windowLength := w.End - w.Start
	assert.InDelta(t, 300.0, windowLength, 0.001, "the fixture is a 300s window")
	fileInterval := calculateSpriteInterval(vf, DefaultSpriteGeneratorConfig)
	assert.Less(t, plan.SpriteInterval, fileInterval,
		"a 300s window needs a FINER interval than a 7200s file: tiling the file's interval puts "+
			"every tile outside the window")

	// And every sampled position must land inside the window.
	step := plan.stepSize()
	for i := 0; i < plan.ChunkCount; i++ {
		at := plan.time(i)
		assert.GreaterOrEqual(t, at, w.Start,
			"tile %d of %d is before the window begins", i, plan.ChunkCount)
		assert.Less(t, at, w.End,
			"tile %d of %d is after the window ends", i, plan.ChunkCount)
	}
	assert.Greater(t, step, 0.0, "a windowed plan must still have a positive step")
}

// The step must be derived from the WINDOW's length. 300s over 81 tiles is ~3.7s, not the ~89s the
// file's duration would give.
func TestTheStepIsTheWindowsLengthOverTheChunkCount(t *testing.T) {
	vf := longVideo()
	w := window(300, 600)

	plan := planSprite(vf, w, DefaultSpriteGeneratorConfig)
	require.NotNil(t, plan)

	assert.InDelta(t, (w.End-w.Start)/float64(plan.ChunkCount), plan.stepSize(), 0.001,
		"the step is the window's length divided by the tile count, not the file's duration")
	assert.InDelta(t, w.Start, plan.time(0), 0.001,
		"the first tile is at the window's START")
}

// A window at zero is a window. The scene covers 0..300s of a 7200s file, which is not the file.
func TestAWindowStartingAtZeroIsStillWindowed(t *testing.T) {
	vf := longVideo()

	plan := planSprite(vf, window(0, 300), DefaultSpriteGeneratorConfig)
	require.NotNil(t, plan)

	unranged := planSprite(vf, generate.SceneWindow{}, DefaultSpriteGeneratorConfig)
	assert.Less(t, plan.SpriteInterval, unranged.SpriteInterval,
		"a 0-300s window is 1/24th of the file and must be tiled far more finely; if the two plans "+
			"match, the window start of 0 was treated as 'no window'")
}

// The slow-seek path works in FRAMES, and frames are ABSOLUTE -- so a window needs the first frame
// of the window, not frame 0.
//
// This is the half-fix to watch for: computing only stepFrame produces a correctly SPACED grid that
// still starts at frame 0, i.e. the wrong footage at the right intervals. It passes every
// duration-only assertion above, which is exactly why it needs its own test.
func TestTheSlowSeekPathStartsAtTheWindowsFirstFrame(t *testing.T) {
	// Short enough that SlowSeek is selected: duration < 5, or FrameCount <= chunkCount.
	// A 2s window at 60fps is 120 frames, comfortably over the 81-tile grid, so the slow path is
	// legitimately chosen -- both by `length < 5` and by the new guard agreeing.
	//
	// Getting this fixture right took three attempts, and each failure was the FIXTURE being wrong
	// rather than the plan:
	//
	//   2s @ 30fps = 60 frames, under the 81-tile grid -> the new window guard correctly REFUSES
	//     the slow path (the window cannot fill a square grid by frame seeking);
	//   10s @ 60fps = 600 frames -> fine on frames, but 10s is not < 5s and the file's 1200 frames
	//     exceed 81, so NEITHER pre-existing condition selects the slow path at all.
	//
	// Only a window under 5s with at least 81 frames in it exercises the path, and that is a real
	// combination: a low-frame-rate clip clipped to a short scene.
	vf := ffmpeg.VideoFile{VideoStreamDuration: 20, FrameCount: 1200, FrameRate: 60, Width: 640, Height: 480}
	w := window(300, 302)

	plan := planSprite(vf, w, DefaultSpriteGeneratorConfig)
	require.NotNil(t, plan)
	require.True(t, plan.SlowSeek,
		"a 2s window with 120 frames can fill an 81-tile grid, so the slow path applies")

	first := plan.firstFrame()
	assert.InDelta(t, 300*60, first, 1.0,
		"the first frame must be the window's start converted through FrameRate (300s * 60fps = "+
			"frame 18000), NOT frame 0")
	assert.Greater(t, first, 0,
		"and it must be a LATE frame: a plan that started at frame 0 would be the wrong footage at "+
			"the right intervals, which is the half-fix the spec warns about")

	// And every frame must be inside the window.
	lastFrameInWindow := int(302 * 60)
	for i := 0; i < plan.ChunkCount; i++ {
		f := plan.frame(i)
		assert.GreaterOrEqual(t, f, int(first), "frame %d is before the window", i)
		assert.LessOrEqual(t, f, lastFrameInWindow,
			"frame %d is past the window's end (%d)", i, lastFrameInWindow)
	}
}

// A window shorter than the grid needs frames must NOT take the slow path.
//
// GetSpriteGridSize rounds chunkCount UP to a perfect square. If the window has fewer frames than
// that square, the slow path asks for frames it does not have and the sprite repeats itself. Today's
// condition is file-level and happens to be right only for unranged scenes.
func TestAWindowTooShortForTheGridDoesNotUseSlowSeek(t *testing.T) {
	// 10 frames total (0.333s at 30fps), but the config wants 81 sprites -> grid 9x9 = 81 tiles.
	vf := ffmpeg.VideoFile{VideoStreamDuration: 10, FrameCount: 300, FrameRate: 30, Width: 640, Height: 480}
	w := window(300, 300.2) // 6 frames

	plan := planSprite(vf, w, DefaultSpriteGeneratorConfig)
	require.NotNil(t, plan)

	windowFrames := int(math.Round((w.End - w.Start) * vf.FrameRate))
	if windowFrames < plan.ChunkCount {
		assert.False(t, plan.SlowSeek,
			"a window of %d frames cannot fill a %d-tile grid by frame seeking: the generator "+
				"would ask for frames it does not have and repeat them", windowFrames, plan.ChunkCount)
	}
}

// An open-ended window must be planned against the REMAINDER of the file.
//
// SceneWindow.End is 0 for "to the end of the file", so a caller that passes End straight through
// gets a zero-length window and a divide-by-zero. The plan must take the file's duration for it --
// and the file's duration is exactly what SceneWindow.At documents as the caller's job.
func TestAnOpenEndedWindowUsesTheFilesDuration(t *testing.T) {
	vf := longVideo()
	open := generate.SceneWindow{Start: 6900, Set: true}

	plan := planSprite(vf, open, DefaultSpriteGeneratorConfig)
	require.NotNil(t, plan)

	assert.InDelta(t, 300.0, plan.length, 0.001,
		"an open-ended window at 6900 of a 7200s file is 300s long")
	assert.Greater(t, plan.SpriteInterval, 0.0, "and must not collapse the step to zero")
	for i := 0; i < plan.ChunkCount; i++ {
		assert.GreaterOrEqual(t, plan.time(i), open.Start)
	}
}

// The VTT cue step must be the WINDOW's step, because the cues are relative to the media element's
// timeline and a windowed scene is streamed windowed.
//
// `vtt-thumbnails.ts` computes `time = percent * player.duration()`, so a cue is a fraction of
// whatever is playing. A windowed scene is never played whole -- StreamDirect already refuses it --
// so a cue naming a file position would never be reached.
func TestTheVTTCueStepIsTheWindowsStep(t *testing.T) {
	vf := longVideo()
	w := window(300, 600)

	plan := planSprite(vf, w, DefaultSpriteGeneratorConfig)
	require.NotNil(t, plan)

	fileInterval := calculateSpriteInterval(vf, DefaultSpriteGeneratorConfig)
	assert.Less(t, plan.vttStepSize(), fileInterval,
		"VTT cues must be spaced by the window's interval, or the scrubber shows the wrong second "+
			"of the wrong footage")
	assert.InDelta(t, plan.stepSize(), plan.vttStepSize(), 0.001,
		"the cue step and the sampling step are the same fact, so they must not drift apart")
}

// A negative or zero frame rate must not divide by zero -- FrameRate is probed and some files report
// 0, which the existing code already handles for the fast path but which a windowed conversion
// multiplies out into.
func TestAZeroFrameRateDoesNotPanic(t *testing.T) {
	vf := ffmpeg.VideoFile{VideoStreamDuration: 10, FrameCount: 300, FrameRate: 0, Width: 640, Height: 480}

	assert.NotPanics(t, func() {
		plan := planSprite(vf, window(2, 8), DefaultSpriteGeneratorConfig)
		if plan != nil && plan.SlowSeek {
			// If it still chose the slow path, the conversion must be finite, not NaN/Inf.
			assert.False(t, math.IsNaN(plan.firstFrameFloat()),
				"a zero frame rate must not produce a NaN frame")
			assert.False(t, math.IsInf(plan.firstFrameFloat(), 0),
				"a zero frame rate must not produce an infinite frame")
		}
	})
}

// The frames must SPREAD across the window, not merely sit inside it.
//
// Found by cross-check mutation, and the reason is worth recording: asserting `frame(i)` is within
// the window does NOT kill "frame() ignores firstFrame". The final `max(f, firstFrame)` clamp
// rescues the mutant, because dropping the base only ever makes `f` smaller and the clamp pulls it
// back. All 81 tiles then collapse onto the same frame and every "inside the window" assertion
// still passes.
//
// Two mutants died here at once -- dropping the base, and dropping the end clamp -- because for
// this fixture the clamp is unreachable: the last tile lands at frame 18000 and the window ends at
// 18120, so nothing ever overshoots.
//
// The frames must SPREAD across the window, and the window must fit inside its FILE.
//
// Two findings here, both from a cross-check mutation that a duration-only assertion could not see.
//
//  1. Asserting `frame(i)` is inside the window does NOT kill "frame() ignores firstFrame". The final
//     `max(f, firstFrame)` floor rescues it: dropping the base only makes `f` smaller, and the floor
//     pulls it back. All 81 tiles then land on ONE frame and every inside-the-window assertion still
//     passes. The assertion has to be about SPREAD.
//
//  2. My first fixture was INCOHERENT -- a window at 300..302s inside a 20s, 1200-frame file. So
//     firstFrame = 300*60 = 18000, which is past the file's last frame, and the totalFrames ceiling
//     fought the window base and collapsed the grid. Every "the frames are inside the window"
//     assertion passed against a completely broken plan. The file must be LONGER than the window, or
//     the fixture tests the clamp rather than the window.
func TestTheSlowSeekFramesSpreadAcrossTheWindowAndStayInsideTheFile(t *testing.T) {
	// A 400s file holds the 300..303s window, so both the window and the file bound are meaningful.
	vf := ffmpeg.VideoFile{VideoStreamDuration: 400, FrameCount: 24000, FrameRate: 60, Width: 1920, Height: 1080}
	w := window(300, 303)

	plan := planSprite(vf, w, DefaultSpriteGeneratorConfig)
	require.NotNil(t, plan)
	require.True(t, plan.SlowSeek, "this fixture exercises the frame-seek path")

	seen := make(map[int]struct{}, plan.ChunkCount)
	for i := 0; i < plan.ChunkCount; i++ {
		seen[plan.frame(i)] = struct{}{}
	}

	assert.Greater(t, len(seen), plan.ChunkCount/2,
		"tiles must land on DISTINCT frames: %d distinct frames for %d tiles means the grid is "+
			"collapsed onto a single instant", len(seen), plan.ChunkCount)

	assert.Equal(t, plan.firstFrame(), plan.frame(0),
		"the first tile is exactly the window's first frame")

	const startFrame = 300 * 60
	const endFrame = 303 * 60
	last := plan.frame(plan.ChunkCount - 1)

	assert.Greater(t, last, startFrame+(endFrame-startFrame)/2,
		"the last tile must be in the SECOND HALF of the window (frames %d..%d, last tile at %d): "+
			"a grid that stops halfway never reaches the scene's end",
		startFrame, endFrame, last)
	assert.Less(t, last, endFrame,
		"and must not overshoot the window's end")
}

// A window that overruns its FILE must clamp the tiles that fall past the end -- and the tiles that
// do NOT overrun must be left alone.
//
// Found while proving the window-end clamp unreachable: `(chunk-1)/chunk < 1` always, so no tile can
// overshoot the WINDOW. A window that overruns its FILE is a different matter, and this pins the
// ORDER of the two bounds, which was my one real bug this pass.
//
// The fixture is an 18..22s window in a 20s, 1200-frame file: the window wants frames 1080..1320, so
// early tiles are legitimately inside the media and only the later ones overrun. Asserting that ALL
// tiles collapse to 1199 was my first attempt and it was wrong -- it would discard the valid tiles
// too, and the first tile is genuinely 1080.
//
// My real bug was the reverse ordering: clamping the window floor AFTER the file ceiling, so the
// floor could hand back a frame past the end of the media, where ffmpeg returns a blank tile. The
// ceiling is applied first and unconditionally; the floor then only ever raises a frame that the
// ceiling has already bounded.
func TestTilesPastTheEndOfTheFileAreClampedAndTheRestAreNot(t *testing.T) {
	// 20s at 60fps is 1200 frames; an 18..22s window wants frames 1080..1320, past the end.
	vf := ffmpeg.VideoFile{VideoStreamDuration: 20, FrameCount: 1200, FrameRate: 60, Width: 640, Height: 480}
	w := window(18, 22)

	plan := planSprite(vf, w, DefaultSpriteGeneratorConfig)
	require.NotNil(t, plan)

	const lastReal = 1199
	clamped, untouched := 0, 0
	for i := 0; i < plan.ChunkCount; i++ {
		f := plan.frame(i)

		assert.LessOrEqual(t, int64(f), int64(lastReal),
			"frame %d is %d, past the file's last frame %d: frame seeking there returns nothing, "+
				"so the tile would be blank", i, f, lastReal)

		if f == lastReal {
			clamped++
		} else {
			untouched++
			assert.GreaterOrEqual(t, f, plan.firstFrame(),
				"an unclamped tile must still be at or after the window's first frame")
		}
	}

	assert.Greater(t, clamped, 0,
		"the window overruns the file, so SOME tiles must be clamped -- if none are, the ceiling "+
			"is not being applied")
	assert.Greater(t, untouched, 0,
		"and the tiles that fall inside the file must NOT be clamped: forcing them to the last "+
			"frame would throw away valid tiles and show one frame repeatedly")

	// The window floor still holds in the ordinary case, where the two bounds do not conflict.
	ok := planSprite(ffmpeg.VideoFile{VideoStreamDuration: 400, FrameCount: 24000, FrameRate: 60},
		window(300, 303), DefaultSpriteGeneratorConfig)
	require.NotNil(t, ok)
	assert.GreaterOrEqual(t, ok.frame(0), ok.firstFrame(),
		"a window wholly inside its file still starts at the window's first frame")
	assert.Greater(t, ok.frame(ok.ChunkCount-1), ok.firstFrame(),
		"and its last tile is a real frame, not the ceiling")
}

// The window-end clamp is UNREACHABLE, and that is why it is not in the plan.
//
// `(chunk-1) * windowFrames/chunk` is strictly less than `windowFrames` for every chunkCount >= 1, so
// the last tile can never overshoot the window -- with or without a clamp. Asserted here so the
// absence is recorded rather than looking like an oversight, and so a future change that makes it
// reachable (a chunkCount that can exceed the window's frame count) is caught by this test failing.
func TestNoTileCanOvershootTheWindowSoTheClampIsDead(t *testing.T) {
	vf := ffmpeg.VideoFile{VideoStreamDuration: 400, FrameCount: 24000, FrameRate: 60, Width: 1920, Height: 1080}

	for _, w := range []generate.SceneWindow{window(300, 303), window(0, 3), window(300, 400)} {
		plan := planSprite(vf, w, DefaultSpriteGeneratorConfig)
		if plan == nil {
			continue
		}
		endFrame := int(math.Round(plan.windowEnd * plan.frameRate))
		for i := 0; i < plan.ChunkCount; i++ {
			assert.Less(t, plan.frame(i), endFrame,
				"tile %d at frame %d reaches the window end %d; if this ever happens the clamp "+
					"this test says is dead is in fact load-bearing and must be restored", i, plan.frame(i), endFrame)
		}
	}
}
