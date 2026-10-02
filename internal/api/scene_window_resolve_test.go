package api

// stash#3530 — how a scene's window is resolved from the request.
//
// #3530 made a scene able to be a WINDOW of a file (scenes_files.start_time/end_time). The play
// URL has to honour it, but the player's scrubber ALSO sends ?start= as the user seeks within a
// scene, and the two cannot both be authoritative.
//
// The decision, and these tests exist to stop it being quietly reversed:
//
//	an ABSENT param falls back to the stored window (the default)
//	a PRESENT param wins (so seeking still works)
//
// Getting that backwards does not fail loudly. It plays the scene from its start every time the
// user seeks, which looks like a broken scrubber rather than a broken feature.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stashapp/stash/pkg/models"
)

// req builds a request with the given raw query string.
func req(query string) *http.Request {
	return httptest.NewRequest("GET", "/scene/1/stream.mp4"+query, nil)
}

// ranged builds a file with a stored window, or an unranged one when both are nil.
func ranged(start, end *float64) *models.VideoFile {
	return &models.VideoFile{
		BaseFile:  &models.BaseFile{Path: "/media/x.mp4"},
		Duration:  7200,
		StartTime: start,
		EndTime:   end,
	}
}

func f(v float64) *float64 { return &v }

// TestAnAbsentParamFallsBackToTheStoredWindow — the default. The FIRST request for a ranged scene
// carries no params, and it is the one that must honour the stored window.
func TestAnAbsentParamFallsBackToTheStoredWindow(t *testing.T) {
	start, end := resolveSceneWindow(req(""), ranged(f(60), f(300)))

	assert.Equal(t, 60.0, start, "no ?start= means the scene's own start")
	assert.Equal(t, 300.0, end, "no ?end= means the scene's own end")
}

// TestAParamOverridesTheStoredWindow — seeking. Reversing this is the failure mode above.
func TestAParamOverridesTheStoredWindow(t *testing.T) {
	start, end := resolveSceneWindow(req("?start=120&end=200"), ranged(f(60), f(300)))

	assert.Equal(t, 120.0, start, "an explicit ?start= must win, or seeking inside a ranged "+
		"scene snaps back to the window's start on every request")
	assert.Equal(t, 200.0, end, "an explicit ?end= must win too")
}

// TestOneParamOverridesAndTheOtherFallsBack — the mixed case, which a naive "if start is set use
// both params" implementation gets wrong.
func TestOneParamOverridesAndTheOtherFallsBack(t *testing.T) {
	start, end := resolveSceneWindow(req("?start=120"), ranged(f(60), f(300)))

	assert.Equal(t, 120.0, start, "the given param wins")
	assert.Equal(t, 300.0, end, "and the ABSENT one still falls back to the stored window, "+
		"rather than defaulting to the end of the file")

	start, end = resolveSceneWindow(req("?end=200"), ranged(f(60), f(300)))
	assert.Equal(t, 60.0, start)
	assert.Equal(t, 200.0, end)
}

// TestAnUnrangedSceneResolvesToTheWholeFile — nil must become 0/0, NOT the file's length.
// makeStreamArgs turns end 0 into the ABSENCE of -t, so 0 has to survive the trip.
func TestAnUnrangedSceneResolvesToTheWholeFile(t *testing.T) {
	start, end := resolveSceneWindow(req(""), ranged(nil, nil))

	assert.Equal(t, 0.0, start, "no stored window means the head of the file")
	assert.Equal(t, 0.0, end,
		"no stored window means the END IS UNKNOWN, so end must be 0 -- the sentinel for 'no -t' "+
			"-- and NOT the file's 7200s length, which would truncate every unranged scene to "+
			"its first two hours of nothing")
}

// TestAMalformedParamFallsBackRatherThanZeroing — the code this replaced was
// `ss, _ := strconv.ParseFloat(...)`, which turns "abc" into 0 and seeks to the head of the file.
// A silent wrong answer is worse than ignoring the param.
func TestAMalformedParamFallsBackRatherThanZeroing(t *testing.T) {
	start, end := resolveSceneWindow(req("?start=abc"), ranged(f(60), f(300)))

	assert.Equal(t, 60.0, start,
		"a malformed ?start= must fall back to the stored window, not silently become 0")
	assert.Equal(t, 300.0, end)
}

// TestANonNumericEndFallsBackToo — same rule for the other half. Written separately because the
// two params are parsed independently and one of them can regress alone.
func TestANonNumericEndFallsBackToo(t *testing.T) {
	start, end := resolveSceneWindow(req("?end=xyz"), ranged(f(60), f(300)))

	assert.Equal(t, 60.0, start)
	assert.Equal(t, 300.0, end, "a malformed ?end= must fall back to the stored end")
}

// TestAnInvertedParamIsLeftForTheArgBuilderToDrop — resolveSceneWindow does NOT normalise an
// inverted window. Doing it here as well would be the second implementation of the clamp that
// sceneFileRanges already owns, and the two would drift.
//
// The assertion is that the values arrive UNCHANGED, which is the opposite of what a reader
// expects from a function called "resolve" -- hence the note.
func TestAnInvertedParamIsLeftForTheArgBuilderToDrop(t *testing.T) {
	start, end := resolveSceneWindow(req("?start=300&end=60"), ranged(f(60), f(300)))

	assert.Equal(t, 300.0, start, "passed through unchanged")
	assert.Equal(t, 60.0, end,
		"passed through unchanged: makeStreamArgs owns the guard, so this function must not "+
			"quietly become a second place that clamps")
}

// TestANilFileResolvesToTheWholeFile — the handler calls this before the nil check on some paths,
// and a ranged scene whose file vanished must not panic.
func TestANilFileResolvesToTheWholeFile(t *testing.T) {
	assert.NotPanics(t, func() {
		start, end := resolveSceneWindow(req("?start=5"), nil)
		assert.Equal(t, 5.0, start, "an explicit param still works with no file")
		assert.Equal(t, 0.0, end)
	})

	assert.NotPanics(t, func() {
		start, end := resolveSceneWindow(req(""), nil)
		assert.Equal(t, 0.0, start)
		assert.Equal(t, 0.0, end)
	})
}

// TestAnExplicitZeroStartOverridesTheStoredWindow — found by a mutation sweep, not by reading the
// code: rewriting `err == nil` to `err == nil && v != 0` (the old zeroing behaviour) changed no
// result, so nothing tested a client seeking to 0 inside a ranged scene.
//
// It is a real case. The scrubber seeks to 0 when the user drags the playhead home, and on a
// scene whose window starts at 60s that request carries ?start=0 -- which the mutant ignored, so
// the player snapped back to 60s. The user cannot rewind a ranged scene to its beginning.
//
// Note that ?start=0 and "no ?start=" are genuinely different and must stay different: the first
// is an instruction, the second is an absence. `ParseFloat` returns 0 for both only when the param
// is absent vs. present-with-zero, and the `err == nil` check is what distinguishes them -- which
// is exactly the branch a "tidy up" edit would delete.
func TestAnExplicitZeroStartOverridesTheStoredWindow(t *testing.T) {
	start, end := resolveSceneWindow(req("?start=0"), ranged(f(60), f(300)))

	assert.Equal(t, 0.0, start,
		"?start=0 is an EXPLICIT request for the head of the file and must win over a stored "+
			"window of 60. Ignoring it means the user cannot rewind a ranged scene to its start, "+
			"because the scrubber sends 0 when the playhead is dragged home")
	assert.Equal(t, 300.0, end, "and the absent end still falls back to the stored window")
}
