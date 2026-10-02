package ffmpeg

// stash#3530 — HLS/DASH must honour a scene's window.
//
// The play URL was fixed for /stream.mp4|webm|mkv and /stream; HLS and DASH still played the whole
// file, and they are the paths the player prefers.
//
// Four sites needed changing, and ONE of them was already right for the wrong reason:
//
//	lastSegment  reads vf.Duration, and GetFiles returns a per-scene copy whose Duration is the
//	             WINDOW's length (the derived-duration change). So the segment COUNT was
//	             already correct -- ceil(window/2)-1, not ceil(file/2)-1. It reads a field that
//	             now means something new, which is why an unranged scene never revealed it.
//
// The three real changes: the seek base, the manifest's declared duration, and the cache key.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// windowFile builds a 2-hour file carrying the given window. The window's LENGTH is what
// GetFiles puts in Duration, which is what several of these tests actually read.
func windowFile(start, end *float64, length float64) *models.VideoFile {
	return &models.VideoFile{
		BaseFile:  &models.BaseFile{Path: "/media/two-hours.mp4"},
		Duration:  length,
		StartTime: start,
		EndTime:   end,
	}
}

// running builds a runningStream over the given file.
func running(vf *models.VideoFile, streamType *StreamType) *runningStream {
	return &runningStream{
		streamType: streamType,
		vf:         vf,
		outputDir:  "/tmp/out",
	}
}

// argVal reads the value after flag.
func argVal(t *testing.T, args Args, flag string) (string, bool) {
	t.Helper()
	for i, a := range args {
		if a == flag {
			require.Less(t, i+1, len(args), "%s has no value after it", flag)
			return args[i+1], true
		}
	}
	return "", false
}

// TestTheWindowDecidesTheSegmentCount — the already-correct-by-accident site, pinned so a future
// change to lastSegment does not silently undo it.
//
// A 6s window of a 7200s file at segmentLength 2 gives 3 segments. Reading the FILE's duration
// would give 3599, and every request past segment 2 would be accepted and transcode footage the
// scene does not contain.
func TestTheWindowDecidesTheSegmentCount(t *testing.T) {
	window := windowFile(f64p(60), f64p(66), 6)
	assert.Equal(t, 2, lastSegment(window),
		"a 6s window at segmentLength %d is 3 segments (0,1,2), NOT the file's %d — a scene must "+
			"not be able to request segment 3598 of its file", segmentLength, 3599)

	// An unranged scene is unchanged: its Duration IS the file's length.
	assert.Equal(t, 3, lastSegment(windowFile(nil, nil, 8)),
		"an unranged 8s file is 4 segments, exactly as before #3530")
}

// TestASeekedSegmentStartsAtTheWindow — the seek base. Without it, segment 1 of a scene whose
// window starts at 60s is transcoded from 2s, i.e. 58s of footage before the scene begins.
func TestASeekedSegmentStartsAtTheWindow(t *testing.T) {
	sm := newTestStreamManager(t, stubProducingPayload(t))
	s := running(windowFile(f64p(60), f64p(66), 6), StreamTypeHLS)

	ss, ok := argVal(t, s.makeStreamArgs(sm, 1), "-ss")
	require.True(t, ok, "segment 1 must seek")
	assert.Equal(t, "62", ss,
		"segment 1 of a window starting at 60s is 60 + 1*%d = 62, not 2. Without the base every "+
			"segment of a ranged scene is taken from the wrong part of the file", segmentLength)

	// Segment 0 of a ranged scene must ALSO seek: the window starts at 60, which is not the
	// head of the file. This is the case the old `if segment > 0` guard skipped entirely.
	ss, ok = argVal(t, s.makeStreamArgs(sm, 0), "-ss")
	require.True(t, ok,
		"segment 0 of a window starting at 60s MUST seek to 60; the old `segment > 0` guard "+
			"meant the first segment came from the head of the file")
	assert.Equal(t, "60", ss)
}

// TestTheSegmentProcessIsBoundedByTheWindow — -t is essential, not belt-and-braces.
//
// Without it ffmpeg runs to the end of the FILE, writes the right segments into the right
// filenames (so the playlist looks correct), and a player that requests one segment past the end
// gets real footage instead of a 404.
func TestTheSegmentProcessIsBoundedByTheWindow(t *testing.T) {
	sm := newTestStreamManager(t, stubProducingPayload(t))
	s := running(windowFile(f64p(60), f64p(66), 6), StreamTypeHLS)

	tt, ok := argVal(t, s.makeStreamArgs(sm, 1), "-t")
	require.True(t, ok,
		"a ranged scene's transcode must be bounded with -t, or ffmpeg runs past the window's end "+
			"into the rest of the file. Args were %v", s.makeStreamArgs(sm, 1))
	assert.Equal(t, "4", tt,
		"-t is measured from the current position (62), so it is the file's end (66) minus the "+
			"seek point, not the window's length (6)")

	// An unranged scene must NOT get -t.
	unranged := running(windowFile(nil, nil, 8), StreamTypeHLS)
	_, ok = argVal(t, unranged.makeStreamArgs(sm, 1), "-t")
	assert.False(t, ok,
		"an unranged scene must transcode the whole file; -t would truncate every segment of "+
			"every ordinary scene. Args were %v", unranged.makeStreamArgs(sm, 1))
}

// TestTheCacheKeyIncludesTheWindow — the site that is a BUG rather than a missing feature.
//
// FileDir keys on the scene hash, which is derived FROM THE FILE. So two scenes sharing one file
// share a cache directory, and whichever is transcoded first populates it for both. Harmless when
// a scene was a file; #3530 makes it wrong.
//
// The second half is the part that is easy to get wrong: an UNRANGED scene must keep its existing
// directory name, or this change invalidates every cached segment in every installation.
func TestTheCacheKeyIncludesTheWindow(t *testing.T) {
	unranged := windowFile(nil, nil, 8)
	a := windowFile(f64p(0), f64p(4), 4)
	b := windowFile(f64p(4), f64p(8), 4)

	base := StreamTypeHLS.FileDir("hash123", 0, windowKeyOf(unranged))
	assert.Equal(t, "hash123_hls", base,
		"an UNRANGED scene must keep its existing directory name exactly, or this change "+
			"invalidates every cached segment in every installation")

	assert.NotEqual(t,
		StreamTypeHLS.FileDir("hash123", 0, windowKeyOf(a)),
		StreamTypeHLS.FileDir("hash123", 0, windowKeyOf(b)),
		"two windows of the SAME FILE must not share a cache directory, or the first one "+
			"transcoded populates segments the other scene then serves")

	// A window starting at 0 is a WINDOW, not "no window": it has an end, so it plays only
	// part of the file. Collapsing it onto the unranged key is the bug this guards.
	assert.NotEqual(t, base, StreamTypeHLS.FileDir("hash123", 0, windowKeyOf(a)),
		"a window of 0-4s is not the whole file even though it starts at 0")
}

// TestAnOpenEndedWindowStillGetsItsOwnKey — start set, end unset. Distinct from both the
// unranged case and any bounded window.
func TestAnOpenEndedWindowStillGetsItsOwnKey(t *testing.T) {
	open := windowFile(f64p(60), nil, 7140)

	assert.NotEqual(t, StreamTypeHLS.FileDir("h", 0, windowKeyOf(windowFile(nil, nil, 8))),
		StreamTypeHLS.FileDir("h", 0, windowKeyOf(open)),
		"an open-ended window is still a window")
	assert.NotEqual(t, StreamTypeHLS.FileDir("h", 0, windowKeyOf(windowFile(f64p(60), f64p(66), 6))),
		StreamTypeHLS.FileDir("h", 0, windowKeyOf(open)),
		"an open-ended window is distinct from the bounded window that shares its start")
}

func f64p(v float64) *float64 { return &v }
