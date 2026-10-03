package generate

// stash#3530 — the sprite grid must describe the SCENE's window.
//
// These tests assert on a VALUE (SpritePlan), not on a helper the generator happens to call, and
// that distinction is this file's reason for existing. #3530's two earlier mutation sweeps each
// found survivors of exactly this shape: the preview's tilePlan helpers were tested while nothing
// proved previewVideo called them, and four of five aggregate call sites were unwired while the
// constant's own sweep read 5/5 killed. So the two things asserted separately here are:
//
//   - the arithmetic (Duration/StepSize/Time/Frame/VttStep), and
//   - that the generator READS the plan for all three of tile time, tile frame and cue spacing.
//
// The second is source-level, in generator_sprite_test.go, because proving it behaviourally means
// running ffmpeg eighty-one times.

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixtures are the measured shape from the spec: a scene at 300..600s of a 7200s file, 30fps.
const (
	fileDuration = 7200.0
	fileFrames   = int64(216000)
	frameRate    = 30.0
)

// The default config tiles 81 chunks, so the chunk counts below are that, and the grid is 9x9.
const chunks = 81

func windowed(start, end float64) SpritePlanOptions {
	return SpritePlanOptions{
		Window:       SceneWindow{Start: start, End: end, Set: true},
		FileDuration: fileDuration,
		FrameRate:    frameRate,
		FileFrames:   fileFrames,
		NthFrame:     int(fileFrames) / chunks,
		ChunkCount:   chunks,
	}
}

func unranged() SpritePlanOptions {
	o := windowed(0, 0)
	o.Window = SceneWindow{}
	return o
}

// A windowed grid spans the WINDOW, not the file.
//
// This is the defect in one assertion: before the change every quantity came from
// VideoStreamDuration, so a 300s window of a 2-hour file was tiled across 2 hours with 30x too many
// tiles 30x too far apart.
func TestTheWindowedGridSpansTheWindowNotTheFile(t *testing.T) {
	p := NewSpritePlan(windowed(300, 600))

	assert.Equal(t, 300.0, p.Duration(), "the grid must span the window's 300s, not the file's 7200s")
	assert.Equal(t, 300.0/chunks, p.StepSize(), "the step must be the window's step")
	assert.Equal(t, chunks, p.ChunkCount())
}

// Every tile must land INSIDE the window, and they must be evenly spaced across it.
//
// The bounds assertion is the one that would catch an unshifted grid, and the spacing assertion is
// the one that would catch a window-derived step with a file-derived start -- which is the
// half-fix the spec is most worried about.
func TestEveryTileFallsInsideTheWindow(t *testing.T) {
	p := NewSpritePlan(windowed(300, 600))

	assert.InDelta(t, 300.0, p.Time(0), 1e-9, "tile 0 is the window's first second, not the file's")
	assert.InDelta(t, 596.296, p.Time(80), 1e-3, "the last tile is one step before the window's end")

	for i := 0; i < chunks; i++ {
		tm := p.Time(i)
		assert.GreaterOrEqual(t, tm, 300.0, "tile %d starts before the window", i)
		assert.Less(t, tm, 600.0, "tile %d starts at or after the window's end", i)
	}

	for i := 1; i < chunks; i++ {
		assert.InDelta(t, p.StepSize(), p.Time(i)-p.Time(i-1), 1e-9,
			"tile %d is not evenly spaced from tile %d", i, i-1)
	}
}

// An UNRANGED scene must be byte-identical to what it was before #3530.
//
// Not "equivalent" -- identical, including (FrameCount-1). Every scene in every existing
// installation is unranged, so a sprite that moves is tens of thousands of regenerated files for no
// visible change. The unranged expressions are therefore copied, not rewritten.
func TestAnUnrangedSpriteIsUnchanged(t *testing.T) {
	p := NewSpritePlan(unranged())

	assert.Equal(t, fileDuration, p.Duration())
	assert.Equal(t, fileDuration/chunks, p.StepSize())

	for i := 0; i < chunks; i++ {
		// The pre-#3530 inline expression, verbatim.
		stepSize := fileDuration / float64(chunks)
		assert.InDelta(t, float64(i)*stepSize, p.Time(i), 1e-9,
			"tile %d must be i*VideoStreamDuration/ChunkCount for an unranged scene", i)

		// The pre-#3530 frame expression, verbatim: (FrameCount-1), not duration*frameRate.
		stepFrame := float64(fileFrames-1) / float64(chunks)
		assert.Equal(t, int64(math.Round(float64(i)*stepFrame)), p.Frame(i),
			"tile %d's frame must be round(i*(FrameCount-1)/ChunkCount) for an unranged scene", i)
	}
}

// A window's frames are ABSOLUTE, converted through the frame rate.
//
// This is the assertion that kills the half-fix named in the spec: computing only stepFrame gives a
// grid that is correctly SPACED and starts at frame 0 -- still the wrong footage, and every spacing
// assertion above still passes.
func TestAWindowedFrameIsAnAbsoluteIndexInsideTheWindow(t *testing.T) {
	p := NewSpritePlan(windowed(300, 600))

	// 300s at 30fps is frame 9000.
	assert.Equal(t, int64(9000), p.Frame(0), "the first tile must be the window's first FRAME, not frame 0")

	// Spacing is the window's, so tile 80 lands just inside the window's last frame.
	step := p.Frame(1) - p.Frame(0)
	assert.Equal(t, int64(math.Round(300*frameRate/chunks)), step,
		"frames must be spaced by the window's length in frames")

	firstFrame := p.Frame(0)
	lastFrame := p.Frame(chunks - 1)
	windowLastFrame := int64(600*frameRate) - 1
	assert.GreaterOrEqual(t, firstFrame, int64(300*frameRate))
	assert.LessOrEqual(t, lastFrame, windowLastFrame,
		"no tile's frame may fall after the window's last frame")
}

// A window NARROWER than the grid must not use frame seeking, or GetSpriteGridSize rounds the
// chunk count up past the window's frame count and the grid becomes duplicate frames.
//
// The fixture is a 3-second window at 1fps -- THREE frames -- inside a 90-frame file, with a grid
// of 81 tiles. That is precisely the case the file-level test gets wrong: the file passes
// upstream's `FrameCount <= ChunkCount` arm (90 > 81 does not, but 40 would, and the duration arm
// is made false so the assertion is about the frame rule alone).
func TestAWindowNarrowerThanTheGridSkipsFrameSeeking(t *testing.T) {
	// A 3-second window at 1fps = 3 frames, in a 90-frame file. The grid wants 81 tiles.
	o := windowed(0, 3)
	o.FileFrames = 40 // fewer than 81 tiles, so upstream's arm WOULD fire
	o.FrameRate = 1
	o.FileDuration = 7200 // long, so the duration arm does NOT

	// The premise, asserted rather than assumed: upstream's file-level condition WOULD have fired
	// for this file, and only the frame arm can be doing it.
	require.False(t, o.FileDuration < 5, "the fixture's duration must not trip the 'too short' arm")
	require.True(t, 0 < o.FileFrames && o.FileFrames <= int64(chunks),
		"the fixture's frame count must trip upstream's arm, or this test proves nothing")

	assert.False(t, NewSpritePlan(o).SlowSeek(),
		"a window with %d frames and a grid of %d tiles must seek by time; frame seeking would round "+
			"the chunk count past the window's frame count and produce duplicate frames", 3, chunks)
}

// A window short enough to NEED frame seeking, but with a frame per tile, must use it.
//
// Without this the previous test's rule would read as "a window never uses frame seeking", which
// would break the small-clip case the path exists for.
func TestAShortWindowWithAFramePerTileDoesSeekByFrame(t *testing.T) {
	// A 2-second window at 1fps, in a 4-frame file, with a grid of 2 tiles -- so the window has
	// exactly one frame per tile, which is the precondition the plan requires.
	o := windowed(0, 2)
	o.FrameRate = 1
	o.FileFrames = 4
	o.FileDuration = 4
	o.ChunkCount = 2

	p := NewSpritePlan(o)

	require.True(t, p.SlowSeek(), "a 2s window in a 4-frame file is exactly the short-file case")
	assert.Equal(t, 2, p.ChunkCount())

	// And the frames it asks for are inside the window, not spread over the file.
	assert.Equal(t, int64(0), p.Frame(0))
	assert.LessOrEqual(t, p.Frame(p.ChunkCount()-1), int64(1),
		"a 2-second window at 1fps holds frames 0 and 1, so no tile may ask for another")
}

// An unranged scene keeps upstream's own slow-seek condition, verbatim.
func TestTheUnrangedSlowSeekConditionIsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name     string
		duration float64
		frames   int64
		want     bool
	}{
		{"a long file with plenty of frames", 7200, fileFrames, false},
		{"a short file", 3, fileFrames, true},
		{"a file with fewer frames than tiles", 7200, 40, true},
		{"a file whose frame count is unknown", 3, 0, true},
		{"a long file with an unknown frame count", 7200, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := unranged()
			o.FileDuration = tc.duration
			o.FileFrames = tc.frames
			o.FrameRate = 1

			assert.Equal(t, tc.want, NewSpritePlan(o).SlowSeek())
		})
	}
}

// The VTT cues are a CONTRACT with the player, and their spacing is the window's.
//
// vtt-thumbnails.ts matches a cue against percent*player.duration(), so cues are relative to the
// media element's own timeline -- and a windowed scene plays a windowed stream. A cue spacing taken
// from the file would therefore point into footage the scene never plays: not a wrong thumbnail but
// a scrub bar that lands on the wrong second.
func TestTheVTTCuesAreSpacedByTheWindow(t *testing.T) {
	p := NewSpritePlan(windowed(300, 600))

	assert.InDelta(t, 300.0/chunks, p.VttStep(), 1e-12,
		"a windowed scene's cues must be spaced by the window's step")

	// And they must agree with the grid: cue i covers [i*step, (i+1)*step) of the windowed
	// stream, which is exactly where tile i was sampled relative to the window's start.
	step := p.VttStep()
	for i := 0; i < chunks; i++ {
		assert.InDelta(t, p.Time(i)-300.0, float64(i)*step, 1e-9,
			"cue %d and tile %d disagree about where they sit inside the window", i, i)
	}

	// Every cue is inside the window's own timeline.
	for i := 0; i < chunks; i++ {
		assert.GreaterOrEqual(t, float64(i)*step, 0.0)
		assert.LessOrEqual(t, float64(i+1)*step, 300.0+1e-9)
	}
}

// Both unranged cue expressions are upstream's own, verbatim.
//
// The second one exists for a real reason: a file with fewer frames than tiles has NthFrame == 0,
// and the fast path would then write every cue at 0.000 -- a VTT in which every tile claims the
// same instant.
func TestTheUnrangedCueSpacingIsUnchanged(t *testing.T) {
	t.Run("fast path uses NthFrame", func(t *testing.T) {
		o := unranged()
		o.FrameRate = frameRate

		assert.InDelta(t, float64(int(fileFrames)/chunks)/frameRate, NewSpritePlan(o).VttStep(), 1e-12)
	})

	t.Run("slow path recalculates from the frame count", func(t *testing.T) {
		o := unranged()
		o.FrameRate = frameRate
		o.FileFrames = 40 // fewer frames than tiles -> slow seek, NthFrame would be 0
		o.NthFrame = 0

		p := NewSpritePlan(o)
		require.True(t, p.SlowSeek())

		assert.InDelta(t, (float64(40-1)/float64(chunks))/frameRate, p.VttStep(), 1e-12,
			"a file with fewer frames than tiles must not write every cue at 0.000")
	})
}

// An open-ended window runs to the end of the file, and a window at 0 is still a window.
//
// Both are separate from the arithmetic on purpose. The open end is knowledge the caller has and
// SceneWindow cannot infer; the zero start is the case that a "start > 0 means windowed" shortcut
// would silently classify as unranged.
func TestOpenEndedAndZeroStartWindows(t *testing.T) {
	t.Run("an open-ended window runs to the end of the file", func(t *testing.T) {
		p := NewSpritePlan(windowed(600, 0))

		assert.Equal(t, 7200.0-600.0, p.Duration())
		assert.InDelta(t, 600.0, p.Time(0), 1e-9)
	})

	t.Run("a window starting at zero is a window", func(t *testing.T) {
		p := NewSpritePlan(windowed(0, 300))

		assert.Equal(t, 300.0, p.Duration(),
			"a 0-start window is a WINDOW: its length is 300s, not the file's 7200s")
		assert.NotEqual(t, fileDuration, p.Duration())
	})
}

// A window that the schema cannot refuse still must not produce a divide-by-zero.
//
// The schema's CHECKs are start >= 0, end >= 0 and end > start -- and a CHECK may not reference
// another table, so a window running off the END of the file is legal and satisfies all three. The
// sqlite layer clamps it (sceneFileRanges), so in practice an empty window cannot reach here from
// GetFiles. The plan still floors the chunk count at 1, because:
//
//   - the caller passes ChunkCount in, and a caller that computed it from a negative length gets a
//     NEGATIVE number (measured: ceil(-400/88.9) = -4, GetSpriteGridSize(-4) = math.MinInt, and
//     grid*grid overflows to 0, so the sprite loop would run zero times and write an empty grid);
//   - a floor costs nothing and makes the plan total.
//
// The generator is where the clamp belongs for the COUNT; this only asserts the plan does not
// itself divide by a non-positive count.
func TestANonPositiveChunkCountIsFlooredAtOne(t *testing.T) {
	for _, count := range []int{0, -1, -81} {
		o := windowed(0, 1)
		o.ChunkCount = count

		p := NewSpritePlan(o)
		assert.Equal(t, 1, p.ChunkCount(), "chunk count %d must be floored at 1", count)
		assert.NotPanics(t, func() { _ = p.StepSize() })
		assert.NotPanics(t, func() { _ = p.VttStep() })
		assert.NotPanics(t, func() { _ = p.Frame(0) })
	}
}

// A file whose frame rate could not be determined must not panic or produce negative frames.
//
// calculateFrameRate already logs that case and leaves FrameRate at 0, so windowFrames() is 0 and
// every computed frame is below 0 -- which the clamp turns into the window's first frame.
func TestAnUnknownFrameRateStillProducesValidFrames(t *testing.T) {
	o := windowed(300, 600)
	o.FrameRate = 0

	p := NewSpritePlan(o)
	require.NotPanics(t, func() {
		for i := 0; i < chunks; i++ {
			assert.GreaterOrEqual(t, p.Frame(i), int64(0))
		}
	})
}

// A frame index is clamped to the FILE too, not only to the window.
//
// A window whose end was rounded up past the file's duration must not ask ffmpeg for a frame that
// does not exist; ffmpeg exits non-zero and the whole sprite fails.
func TestAFrameIsClampedToTheFilesLastFrame(t *testing.T) {
	o := windowed(300, 900) // window runs 100s past a 7200s file? no -- start 300, end 900
	o.FileFrames = 1000
	o.FrameRate = 1
	o.FileDuration = 1000

	p := NewSpritePlan(o)

	assert.LessOrEqual(t, p.Frame(chunks-1), o.FileFrames-1,
		"no tile's frame may exceed the file's last frame")
}
