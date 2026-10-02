package generate

// stash#3530 — the cover and the previews must be taken from INSIDE the scene's window.
//
// #3530 added scenes_files.start_time/end_time, and the derived-duration change made
// videoFile.Duration mean the WINDOW's length. That is correct for a length and wrong for a
// POSITION, and the screenshot default is a position:
//
//	at = videoFile.Duration * 0.2        <- a proportion of the WINDOW
//	ScreenshotTime(input, at, ...)       <- args.Seek(at), an ABSOLUTE file offset
//
// For a scene at 300..600 of a 7200s file that is 60 -- 240 seconds before the scene begins. For a
// scene at 3600..3900 the cover is the first minute of the file.
//
// This is the mirror image of the `lastSegment` finding in stash-3530-hls: there, reading a
// derived length happened to be RIGHT; here it is actively wrong, because the number is used as a
// position. Same root cause, opposite sign, and only one of the two could be caught by looking at
// the value's name.

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stashapp/stash/pkg/models"
)

// TestTheCoverComesFromInsideTheWindow — the headline claim, and the arithmetic that was wrong.
func TestTheCoverComesFromInsideTheWindow(t *testing.T) {
	// A 300s window starting at 300s, in a 7200s file.
	w := SceneWindow{Start: 300, End: 600, Set: true}

	// 20% of the way through the window: 300 + 0.2*300 = 360.
	assert.Equal(t, 360.0, w.At(screenshotDurationProportion, 300),
		"20%% into a window of 300s that starts at 300s is 360s into the FILE. Computing "+
			"0.2*300 = 60 grabs a frame from 240s BEFORE the scene begins.")

	// The start of the window is a valid point and must land exactly on it.
	assert.Equal(t, 300.0, w.At(0, 300),
		"the beginning of the window IS the window's start, so 0% must not mean t=0 of the file")

	// The end of the window is a valid point (the last frame inside the scene).
	assert.Equal(t, 600.0, w.At(1, 300),
		"100%% of the window is the window's END, so the cover can come from the last moment of "+
			"the scene rather than the last moment of the file")
}

// TestAnUnrangedSceneIsUnchanged — the guarantee that matters most in practice.
//
// The unranged branch must reduce to the ORIGINAL expression exactly. Every scene in every
// existing installation is unranged, so if this changes, every cover in every library silently
// moves. That is the same "no existing row changes" guarantee migration 122 made, and it is the
// reason the branch is spelled out rather than left implicit.
func TestAnUnrangedSceneIsUnchanged(t *testing.T) {
	for _, duration := range []float64{10, 7200, 0.5, 3600.25} {
		assert.Equal(t, screenshotDurationProportion*duration, SceneWindow{}.At(screenshotDurationProportion, duration),
			"an unranged %vs scene must produce the same timestamp it always did (%v), or every "+
				"cover in every existing library silently moves",
			duration, screenshotDurationProportion*duration)
	}
}

// TestAnOpenEndedWindowUsesTheFileEndForTheLength — start set, end unset. The scene runs to the end
// of the file, so the caller supplies the FILE's duration as the length and `At` rebases it.
//
// This is the shape where a mistake is easiest: `length` is the window's length in the closed case
// and the FILE's length in the open one, because the open window's length is not knowable from the
// window alone. The contract is that the caller passes "how long the scene runs", whatever that is.
func TestAnOpenEndedWindowUsesTheFileEndForTheLength(t *testing.T) {
	w := SceneWindow{Start: 3600, Set: true}

	// The scene runs from 3600 to the end of a 7200s file, so it is 3600s long.
	assert.Equal(t, 4320.0, w.At(0.2, 7200-3600),
		"an open-ended window at 3600 in a 7200s file is 3600s long, so 20%% in is 3600+720 = 4320")

	// And the whole of it, which must be the file's end.
	assert.Equal(t, 7200.0, w.At(1, 7200-3600),
		"100%% of an open-ended window is the end of the FILE")
}

// TestAnExplicitTimestampStillWins — the user's choice must not be silently rebased.
//
// `sceneGenerateScreenshot(at: 400)` means "give me the frame at 400 seconds". If the scene's
// window starts at 300 and the code rebases 400 into the window, the user gets 520 and has no way
// to know. So the rule is: an explicit At is used verbatim; only the DEFAULT is window-relative.
func TestAnExplicitTimestampStillWins(t *testing.T) {
	w := SceneWindow{Start: 300, End: 600, Set: true}

	explicit := 400.0
	// resolveScreenshotAt is the seam that decides, and it is what the task calls.
	assert.Equal(t, 400.0, resolveScreenshotAt(&explicit, w, 300),
		"an explicit `at` is an instruction in FILE seconds and must be used verbatim")

	var absent *float64
	assert.Equal(t, 360.0, resolveScreenshotAt(absent, w, 300),
		"with no explicit `at` the default is 20%% into the WINDOW, i.e. 360")
}

// TestAWindowThatEndsAtTheFileEndBehavesLikeAnOpenOne — the two shapes coincide numerically, and
// that is worth pinning because a scene created as 0..7200 and one created as 0..NULL are the same
// thing to a viewer.
func TestAWindowThatEndsAtTheFileEndBehavesLikeAnOpenOne(t *testing.T) {
	const fileLength = 7200.0

	closed := SceneWindow{Start: 0, End: fileLength, Set: true}
	open := SceneWindow{Start: 0, Set: true}

	assert.Equal(t,
		closed.At(screenshotDurationProportion, fileLength),
		open.At(screenshotDurationProportion, fileLength),
		"a window covering the whole file must give the same answer as an open-ended one at 0, "+
			"since they describe the same scene")
}

// TestWindowOfReadsTheStoredWindow — WindowOf is the ONLY thing that converts models.VideoFile into
// a SceneWindow, so if it drops a field the window is silently wrong for every scene.
//
// Found by mutation: deleting the EndTime read changed no result. Nothing was asserting it, because
// every other test in this file CONSTRUCTS a SceneWindow by hand -- which proves the arithmetic and
// nothing about the conversion. That is the same shape of gap as the tilePlan survivors: a test that
// exercises the second half of a pair while the first half is untested.
func TestWindowOfReadsTheStoredWindow(t *testing.T) {
	for _, tc := range []struct {
		name               string
		start, end         *float64
		wantSet            bool
		wantStart, wantEnd float64
	}{
		{"no window at all", nil, nil, false, 0, 0},
		{"both ends", f64p(300), f64p(600), true, 300, 600},
		{"open at the end", f64p(60), nil, true, 60, 0},
		{"open at the start", nil, f64p(600), true, 0, 600},
		// A window starting at 0 is a WINDOW. WindowOf must not return the zero SceneWindow,
		// or every scene covering 0..N of a file silently reverts to whole-file behaviour.
		{"a window starting at zero", f64p(0), f64p(600), true, 0, 600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := WindowOf(&models.VideoFile{StartTime: tc.start, EndTime: tc.end})

			assert.Equal(t, tc.wantSet, got.Set,
				"Set must distinguish 'no window' from a window that happens to start at 0")
			assert.Equal(t, tc.wantStart, got.Start)
			assert.Equal(t, tc.wantEnd, got.End,
				"End must be read from the file: a scene whose window ends early has a real end, "+
					"and dropping it makes the window unbounded")
		})
	}
}

// TestWindowOfHandlesANilFile — the generator probes the file separately and the scene may have lost
// it, so a nil must be the no-window case rather than a panic.
func TestWindowOfHandlesANilFile(t *testing.T) {
	assert.NotPanics(t, func() {
		assert.Equal(t, SceneWindow{}, WindowOf(nil),
			"a nil file is a scene with no window, which is what the zero value means")
	})
}

func f64p(v float64) *float64 { return &v }
