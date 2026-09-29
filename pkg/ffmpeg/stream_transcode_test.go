package ffmpeg

// Tests for #5683: a live transcode of a missing source file answered the
// request with "200 OK, Content-Type: video/mp4" and an empty body.
//
// A client that sees a successful stream which ends immediately re-requests
// it, so each retry spawned another ffmpeg. The reporter measured 60-80% of a
// core and a log with tens of thousands of identical lines, sustained for as
// long as the page stayed open.
//
// These tests drive the REAL handler over a REAL http.ResponseRecorder, with
// the encoder pointed at a stub script. That is possible because NewEncoder
// takes a binary path, and it is worth doing rather than mocking: the bug is
// a claim about the bytes on the wire and the status code returned, and a mock
// would only prove the mock agrees with itself.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/models"
)

// stubFFmpeg writes an executable script that stands in for ffmpeg and run it
// in a temp dir. The script's behaviour is supplied by the caller.
//
// The stub is a shell script rather than a Go binary because the encoder is
// constructed from a path and exec'd directly; a second binary would need
// building and would be far slower for no extra coverage.
//
// A note on the payload encoding, which cost a debugging cycle: the bytes a
// real transcoder emits are BINARY and routinely include NUL. A NUL cannot
// appear in a shell script at all -- it terminates the string -- so a payload
// written literally into the script body is silently truncated, and the test
// then fails with "0 bytes written" while blaming the code under test. The
// payload must be emitted as octal escapes inside a printf, which the shell
// can carry and which yields the exact bytes on stdout.
func stubFFmpeg(t *testing.T, body string) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the ffmpeg stub is a POSIX shell script")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "ffmpeg-stub")

	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("writing the ffmpeg stub: %v", err)
	}
	return path
}

// stubPayload is a stand-in for transcoded output. The octal escapes decode to
// the bytes shown in payloadOctal, which begin with the 0x00 0x00 0x00 0x18
// box size of an MP4 file type box -- chosen so the first byte is NUL, the
// value a naive "did we write anything" check is most likely to swallow.
const stubPayload = `\000\000\000\030ftypmp42`

func stubPayloadString() string {
	out := make([]byte, 0, len(stubPayload))
	for i := 0; i < len(stubPayload); i++ {
		if stubPayload[i] == '\\' && i+3 < len(stubPayload) {
			var v byte
			for _, c := range []byte(stubPayload[i+1 : i+4]) {
				v = v*8 + (c - '0')
			}
			out = append(out, v)
			i += 3
			continue
		}
		out = append(out, stubPayload[i])
	}
	return string(out)
}

// stubProducingPayload returns a stub that writes the binary payload above and
// exits successfully.
func stubProducingPayload(t *testing.T) string {
	return stubFFmpeg(t, "printf '"+stubPayload+"'\nexit 0\n")
}

// stubConfig is the minimum StreamManagerConfig that lets makeStreamArgs run.
// Hardware acceleration is off because it is the one setting that would make
// the handler shell out to anything other than the stub encoder.
type stubConfig struct{}

func (stubConfig) GetMaxStreamingTranscodeSize() models.StreamingResolutionEnum {
	return models.StreamingResolutionEnumLow
}
func (stubConfig) GetLiveTranscodeInputArgs() []string  { return nil }
func (stubConfig) GetLiveTranscodeOutputArgs() []string { return nil }
func (stubConfig) GetTranscodeHardwareAcceleration() bool {
	return false
}

// newTestStreamManager builds a StreamManager around a stub encoder, with
// every collaborator replaced by the smallest thing that lets the handler run.
func newTestStreamManager(t *testing.T, stub string) *StreamManager {
	t.Helper()

	sm := &StreamManager{
		encoder:        NewEncoder(stub),
		ffprobe:        NewFFProbe(stub),
		config:         stubConfig{},
		lockManager:    fsutil.NewReadLockManager(),
		runningStreams: make(map[string]*runningStream),
	}

	ctx, cancel := context.WithCancel(context.Background())
	sm.context = ctx
	sm.cancelFunc = cancel
	t.Cleanup(cancel)

	return sm
}

func testOptions() TranscodeOptions {
	return TranscodeOptions{
		StreamType: StreamTypeMP4,
		VideoFile: &models.VideoFile{
			BaseFile: &models.BaseFile{
				Path: "/media/gone/somewhere.mp4",
			},
			Duration: 10,
		},
	}
}

// TestATranscodeThatProducesNothingIsAnErrorNotAnEmptySuccess is the issue.
//
// The stub writes nothing to stdout and exits non-zero, which is what ffmpeg
// does for a source file that does not exist. Before the fix the response was
// 200 with the video content type; the client saw a broken stream and retried
// indefinitely.
func TestATranscodeThatProducesNothingIsAnErrorNotAnEmptySuccess(t *testing.T) {
	// exit 1 with a message on stderr, exactly as ffmpeg reports a missing
	// input file.
	stub := stubFFmpeg(t, `echo "/media/gone/somewhere.mp4: No such file or directory" >&2
exit 1
`)

	sm := newTestStreamManager(t, stub)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/scene/stream.mp4", nil)

	sm.ServeTranscode(w, r, testOptions())

	if w.Code != http.StatusInternalServerError {
		t.Errorf("a transcode that produced nothing must not answer 200, "+
			"or the client retries it forever (#5683); got %d, body %q",
			w.Code, w.Body.String())
	}
}

// The status alone is not the claim. The content type is the other half: a
// 200 that still advertises video/mp4 is the exact shape Firefox re-requests.
func TestAFailedTranscodeDoesNotAdvertiseAVideoContentType(t *testing.T) {
	stub := stubFFmpeg(t, `echo "boom" >&2
exit 1
`)

	sm := newTestStreamManager(t, stub)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/scene/stream.mp4", nil)

	sm.ServeTranscode(w, r, testOptions())

	if ct := w.Header().Get("Content-Type"); strings.Contains(ct, "video/") {
		t.Errorf("a failed transcode must not claim a video content type, "+
			"got %q", ct)
	}
}

// TestANonEmptyResponseDoesNotCloakTheStatus guards the fix against being
// "achieved" by breaking playback. A transcode that works must still be a 200
// with a video content type and the bytes ffmpeg produced.
func TestANonEmptyResponseDoesNotCloakTheStatus(t *testing.T) {
	// The payload is longer than one byte, so both the first-byte peek and the
	// subsequent copy are exercised.
	sm := newTestStreamManager(t, stubProducingPayload(t))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/scene/stream.mp4", nil)

	sm.ServeTranscode(w, r, testOptions())

	if w.Code != http.StatusOK {
		t.Fatalf("a working transcode must be 200, got %d (body %q)",
			w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "video/") {
		t.Errorf("a working transcode must advertise a video content type, got %q", ct)
	}

	payload := stubPayloadString()
	if got := w.Body.String(); got != payload {
		t.Errorf("the response body must be exactly what ffmpeg produced.\n"+
			"  want %d bytes %q\n  got  %d bytes %q",
			len(payload), payload, len(got), got)
	}
}

// TestTheBytePeekedToDetectStartupIsNotLost is the regression the fix itself
// introduces. Reading one byte to find out whether ffmpeg started, and then
// forgetting to write it, truncates every stream by its first byte. For MP4
// that silently corrupts the file header, so the symptom would be a video
// that downloads fine and will not play -- a worse bug than the one fixed.
func TestTheBytePeekedToDetectStartupIsNotLost(t *testing.T) {
	sm := newTestStreamManager(t, stubProducingPayload(t))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/scene/stream.mp4", nil)

	sm.ServeTranscode(w, r, testOptions())

	payload := stubPayloadString()
	if got := w.Body.String(); got != payload {
		t.Errorf("the first byte read to detect startup must still be written "+
			"to the client; the body is %d bytes, want %d (%q vs %q)",
			len(got), len(payload), got, payload)
	}
}

// A stream that is killed before it produces anything is the user closing the
// tab or the scene being stopped, not a server fault. It must not be reported
// as a 500, or every cancelled playback shows an error in the UI.
//
// The cancellation comes from the StreamManager's own context, which is what
// stopTranscode and the shutdown path cancel. Cancelling the *request* context
// instead would not reach ffmpeg at all -- the command is parented to
// sm.context -- so the test would pass without exercising the branch.
//
// The cancellation happens before the request, which is deterministic rather
// than racy: exec.CommandContext refuses to start a command whose context is
// already done, so the handler's first read fails with context.Canceled
// immediately. A stub that holds the pipe open would instead leave the test
// waiting on a subprocess, which is a 60-second test for the same branch.
func TestACancelledTranscodeIsNotReportedAsAServerFault(t *testing.T) {
	stub := stubFFmpeg(t, "exit 0\n")

	sm := newTestStreamManager(t, stub)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/scene/stream.mp4", nil)

	// The state a stopped or shut-down stream is in.
	sm.cancelFunc()

	sm.ServeTranscode(w, r, testOptions())

	if w.Code == http.StatusInternalServerError {
		t.Errorf("a cancelled stream is the client going away, not a server "+
			"fault; got %d. sm.context.Err()=%v", w.Code, sm.context.Err())
	}
}
