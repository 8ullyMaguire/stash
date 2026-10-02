package ffmpeg

// stash#3530 — the manifests must declare the SCENE's length, not the file's.
//
// Both handlers read probeResult.FileDuration, which is the FILE's length. That is invisible on
// ordinary content: for an unranged scene the file's length and the window's length are the same
// number, so a regression here only shows up on ranged scenes. Each test therefore asserts BOTH
// directions — a window shortens the playlist, and an unranged scene is untouched.
//
// The stub emits ffprobe JSON when it sees -print_format and behaves like ffmpeg otherwise,
// because newTestStreamManager hands the SAME path to both the encoder and the probe.

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// stubProbing builds a stub that answers ffprobe with a duration of fileDuration seconds and
// otherwise behaves like the payload stub.
func stubProbing(t *testing.T, fileDuration float64) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the ffmpeg stub is a POSIX shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "probe-stub")

	// Two things cost a debug cycle here, both worth writing down:
	//
	//   match on an argument ANYWHERE, not on $1 -- NewVideoFile passes `-v quiet -print_format
	//   json ...`, so $1 is "-v" and a $1 check silently never fires
	//
	//   and `case " $*" in` with a SPACE is `case " $"` -- a literal dollar sign, which matches
	//   nothing, so the fallback arm runs and the output is empty. The failure surfaced as
	//   "unexpected end of JSON input", which reads like corrupt data rather than an unmatched
	//   case. Verified by running the script by hand before trusting the test.
	// The duration is baked INTO the format string, not passed as printf's second argument.
	//
	// `printf '...%v...' 7200.0` looks right and is not: shell printf has no %v, so it emits
	// nothing for that spec and the JSON is truncated at `"duration":"`. The failure arrived as
	// "unexpected end of JSON input", which points at the JSON rather than at the format string.
	// Found by running the generated script BY HAND and looking at the bytes -- three debug
	// cycles went into the test before doing that.
	probeJSON := fmt.Sprintf(
		`{"format":{"duration":"%f"},"streams":[{"codec_type":"video","width":640,`+
			`"height":480,"avg_frame_rate":"24/1","codec_name":"h264","bit_rate":"1000000"}]}`,
		fileDuration)

	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *-print_format*)\n" +
		"  printf '%s' '" + probeJSON + "'\n" +
		"  exit 0 ;;\n" +
		"esac\n" +
		"printf '" + stubPayload + "'\n" +
		"exit 0\n"

	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// realFile creates an empty file so parse()'s stat succeeds, and returns a VideoFile over it.
// The manifest path stats the video before probing, so a nonexistent path fails the test for a
// reason that has nothing to do with the window.
func realFile(t *testing.T, start, end *float64, length float64) *models.VideoFile {
	t.Helper()
	path := filepath.Join(t.TempDir(), "video.mp4")
	require.NoError(t, os.WriteFile(path, []byte("not really a video"), 0o644))

	return &models.VideoFile{
		BaseFile:   &models.BaseFile{Path: path},
		Duration:   length,
		StartTime:  start,
		EndTime:    end,
		VideoCodec: "h264",
		Width:      640,
		Height:     480,
		FrameRate:  24,
	}
}

// hlsPlaylist drives the real serveHLSManifest and returns the playlist body.
func hlsPlaylist(t *testing.T, sm *StreamManager, vf *models.VideoFile) string {
	t.Helper()
	sm.cacheDir = t.TempDir()
	r := httptest.NewRequest("GET", "/scene/1/stream.m3u8", nil)
	w := httptest.NewRecorder()

	serveHLSManifest(sm, w, r, vf, "")
	require.Equal(t, 200, w.Code, "manifest should be served: %s", w.Body.String())
	return w.Body.String()
}

// segmentCount counts the segment URIs in a playlist.
func segmentCount(playlist string) int {
	return strings.Count(playlist, ".ts")
}

// TestTheHlsPlaylistIsAsLongAsTheWindow — the headline claim: a 6s scene of a 2-hour file gets a
// 6-second playlist, not a 2-hour one.
func TestTheHlsPlaylistIsAsLongAsTheWindow(t *testing.T) {
	sm := newTestStreamManager(t, stubProbing(t, 7200))

	// The window is 6s; GetFiles would have set Duration to 6.
	windowed := hlsPlaylist(t, sm, realFile(t, f64p(60), f64p(66), 6))
	assert.Equal(t, 3, segmentCount(windowed),
		"a 6s window at segmentLength %d is 3 segments. The stub reports a 7200s file, so a "+
			"playlist of 3599 segments means the manifest read the FILE's duration.\n%s",
		segmentLength, windowed)

	assert.Contains(t, windowed, "#EXT-X-ENDLIST",
		"the playlist must still be closed, or a player keeps requesting segments forever")

	// And the unranged case must be UNCHANGED — the same number as before #3530.
	unranged := hlsPlaylist(t, sm, realFile(t, nil, nil, 7200))
	assert.Equal(t, 3600, segmentCount(unranged),
		"an unranged scene must keep its full-length playlist exactly; the window must not "+
			"shorten ordinary content")
}

// TestTheDashManifestDeclaresTheWindow — the DASH side, which is a single duration string.
func TestTheDashManifestDeclaresTheWindow(t *testing.T) {
	sm := newTestStreamManager(t, stubProbing(t, 7200))
	sm.cacheDir = t.TempDir()

	r := httptest.NewRequest("GET", "/scene/1/stream.mpd", nil)
	w := httptest.NewRecorder()
	serveDASHManifest(sm, w, r, realFile(t, f64p(60), f64p(66), 6), "")
	require.Equal(t, 200, w.Code)

	body := w.Body.String()
	assert.Contains(t, body, "mediaPresentationDuration",
		"the manifest must declare a duration at all")
	assert.NotContains(t, body, "PT7200S",
		"the FILE's 7200s duration must not be declared for a 6s scene")
	assert.Contains(t, body, "PT6S",
		"the WINDOW's 6s duration must be declared. Body was:\n%s", body)
}
