package ffmpeg

// stash#3530 — a ranged scene must play its WINDOW, not the whole file.
//
// #3530 added start_time/end_time to scenes_files. The database side of that is done: GetFiles
// returns the window's duration and the aggregates use it. The player was not touched, so a scene
// that is 60s of a 2-hour file played 2 hours.
//
// These tests drive `makeStreamArgs` directly. It is a pure function of the options (given the
// collaborators on the StreamManager) and it is the ONE place the window becomes ffmpeg arguments,
// which makes it the right place to pin the arithmetic. Testing it through the handler instead
// would assert on bytes that a stub encoder fabricates.
//
// #3530 already has tests at the handler level for a transcode that produces nothing; this file is
// about the ARGUMENTS, not the output.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// rangedVideoFile builds a video file long enough that a window is clearly a sub-range.
// A 2-hour file with a 60s window is the shape the issue reports.
func rangedVideoFile(t *testing.T) *models.VideoFile {
	t.Helper()
	return &models.VideoFile{
		BaseFile: &models.BaseFile{Path: "/media/a-long-video.mp4"},
		Duration: 7200,
	}
}

// windowOptions is a scene that plays 60s..300s of a 2-hour file.
func windowOptions(t *testing.T) TranscodeOptions {
	t.Helper()
	return TranscodeOptions{
		StreamType: StreamTypeMP4,
		VideoFile:  rangedVideoFile(t),
		StartTime:  60,
		EndTime:    300,
	}
}

// argValue returns the value that follows flag in args, and whether flag is present at all.
// Reading the pair rather than the whole slice is what lets a test say "the -t is 240" rather
// than eyeballing an arg list.
func argValue(t *testing.T, args Args, flag string) (string, bool) {
	t.Helper()
	for i, a := range args {
		if a == flag {
			require.Less(t, i+1, len(args),
				"%s is the LAST argument, so it carries no value; the args builder has appended "+
					"a flag where a flag+value pair was required", flag)
			return args[i+1], true
		}
	}
	return "", false
}

// TestAWindowBecomesASeekAndADuration is the core of #3530: the scene's window reaches ffmpeg.
func TestAWindowBecomesASeekAndADuration(t *testing.T) {
	sm := newTestStreamManager(t, stubProducingPayload(t))
	args := windowOptions(t).makeStreamArgs(sm)

	ss, ok := argValue(t, args, "-ss")
	require.True(t, ok, "a scene starting at 60s must seek: the window's start is not 0")
	assert.Equal(t, "60", ss, "the seek must be the window's START")

	t2, ok := argValue(t, args, "-t")
	require.True(t, ok,
		"a scene ending at 300s must bound the output with -t, or ffmpeg transcodes to the end of "+
			"the 2-hour file. Args were %v", args)
	assert.Equal(t, "240", t2,
		"-t must be the window's LENGTH (300-60), not its END. Passing 300 seeks 300s INTO the "+
			"file from a point already 60s in, and produces 60s of the wrong part of the video. "+
			"Args were %v", args)
}

// TestAnUnrangedSceneStillTranscodesTheWholeFile is the regression guard for the other direction:
// the fix must not bound a scene that was never ranged.
func TestAnUnrangedSceneStillTranscodesTheWholeFile(t *testing.T) {
	sm := newTestStreamManager(t, stubProducingPayload(t))
	args := TranscodeOptions{
		StreamType: StreamTypeMP4,
		VideoFile:  rangedVideoFile(t),
	}.makeStreamArgs(sm)

	_, ok := argValue(t, args, "-t")
	assert.False(t, ok,
		"a scene with no window must transcode the WHOLE file; -t would silently truncate every "+
			"unranged scene in the library. Args were %v", args)

	_, ok = argValue(t, args, "-ss")
	assert.False(t, ok,
		"an unranged scene starts at the beginning, so -ss is noise. Args were %v", args)
}

// TestAnOpenEndedWindowSeeksButDoesNotBound — start set, end unset (a scene from 60s to the end
// of the file). It must seek, and must NOT get -t: "to the end" is not a number, and guessing the
// file's duration here would be wrong for a file whose duration is unknown or wrong.
func TestAnOpenEndedWindowSeeksButDoesNotBound(t *testing.T) {
	sm := newTestStreamManager(t, stubProducingPayload(t))
	args := TranscodeOptions{
		StreamType: StreamTypeMP4,
		VideoFile:  rangedVideoFile(t),
		StartTime:  60,
	}.makeStreamArgs(sm)

	ss, ok := argValue(t, args, "-ss")
	require.True(t, ok, "an open-ended window still starts at 60s")
	assert.Equal(t, "60", ss)

	_, ok = argValue(t, args, "-t")
	assert.False(t, ok,
		"an open-ended window runs to the END of the file and needs no -t. Args were %v", args)
}

// TestABoundaryStartDoesNotSeek — start == 0 is the head of the file. The existing code already
// omits -ss when StartTime == 0, and that must stay: `StartTime != 0` is the guard, so a
// start of 0 with a real end still gets -t and skips -ss. Without this the two halves would each
// need their own special case.
func TestABoundaryStartDoesNotSeek(t *testing.T) {
	sm := newTestStreamManager(t, stubProducingPayload(t))
	args := TranscodeOptions{
		StreamType: StreamTypeMP4,
		VideoFile:  rangedVideoFile(t),
		EndTime:    300,
	}.makeStreamArgs(sm)

	_, ok := argValue(t, args, "-ss")
	assert.False(t, ok, "start 0 means the head of the file, so there is nothing to seek to")

	t2, ok := argValue(t, args, "-t")
	require.True(t, ok, "an end of 300 with no start is the first 300s of the file")
	assert.Equal(t, "300", t2,
		"with start 0 the length IS the end, so -t is 300. This is the case where the subtraction "+
			"looks redundant and is in fact required")
}

// TestAnInvertedWindowIsNotEmitted — end <= start. A client racing a seek can send this, and
// the spec says clamp rather than error, because an error would break the player over a value
// that is momentarily wrong.
//
// The clamp DIRECTION matters, and this assertion is deliberately unconditional. My first version
// wrapped it in `if ok { ... }` — which passes whether or not -t is emitted, so it was killed by
// neither of the two mutants that break this guard. A conditional assertion cannot fail: that is
// the whole point of it, and writing one here made the test vacuous while looking rigorous.
// (A CHECK that guards itself with an OR of exhaustive cases is the same mistake in SQL.)
func TestAnInvertedWindowIsNotEmitted(t *testing.T) {
	sm := newTestStreamManager(t, stubProducingPayload(t))
	args := TranscodeOptions{
		StreamType: StreamTypeMP4,
		VideoFile:  rangedVideoFile(t),
		StartTime:  300,
		EndTime:    60,
	}.makeStreamArgs(sm)

	t2, ok := argValue(t, args, "-t")
	assert.False(t, ok,
		"an inverted window (end 60, start 300) must emit NO -t. -t with a negative or zero "+
			"length transcodes nothing and answers 200 with an empty body, which is the exact "+
			"shape of #5683 — the client then re-requests in a loop. Args were %v", args)
	if ok {
		assert.NotEqual(t, "0", t2, "unreachable given the assertion above, but stated so the "+
			"failure message names the number that would break it")
	}
}

// TestAZeroLengthWindowIsNotEmitted — end == start is the boundary case of the guard above, and
// it is a SEPARATE mutant: the original code guarded with `>`, so relaxing it to `>=` survives
// TestAnInvertedWindowIsNotEmitted (60 < 300 is still inverted) and only dies here.
//
// -t 0 on its own is a well-formed ffmpeg invocation meaning "produce no output", so nothing in
// the arg-building layer objects. It is the response that breaks: 200 with an empty body.
func TestAZeroLengthWindowIsNotEmitted(t *testing.T) {
	sm := newTestStreamManager(t, stubProducingPayload(t))
	args := TranscodeOptions{
		StreamType: StreamTypeMP4,
		VideoFile:  rangedVideoFile(t),
		StartTime:  300,
		EndTime:    300,
	}.makeStreamArgs(sm)

	_, ok := argValue(t, args, "-t")
	assert.False(t, ok,
		"a zero-length window (end == start) must emit NO -t; -t 0 produces an empty stream that "+
			"the client treats as a failed stream and retries forever. Args were %v", args)
}
