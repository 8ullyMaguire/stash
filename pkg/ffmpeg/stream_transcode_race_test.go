// Regression guard for the ffmpeg stream-truncation race.
//
// WHY THIS FILE EXISTS, and the mistake it is built to prevent.
//
// `037c9d6d1` fixed a real bug: a goroutine called cmd.Wait(), which closes the
// child's pipes, while the handler was reading stdout as one peeked byte plus an
// io.Copy. A Wait landing between them truncated every stream to one byte.
// Measured: 426 of 3000 replays (14%) truncated before the fix, 0 of 3000 after.
//
// The commit that shipped it then said "~0.3% still truncate" and blamed
// LockContext.Cancel. Both halves of that were wrong. A follow-up hammer DID
// report short bodies at 12-worker concurrency -- and every one of them was a
// 500 carrying:
//
//	fork/exec /tmp/.../ffmpeg-stub: text file busy
//
// That is ETXTBSY: the kernel refusing to exec a file whose descriptor is still
// open for writing. The hammer called os.WriteFile on a stub path that a sibling
// goroutine was about to exec. It is a property of the TEST, not of the
// transcode path. Classified properly (by status code, not body length),
// 3840 concurrent serves gave truncated-200 = 0.
//
// So the guard asserts THREE things, and the first is the one that was missing:
//
//  1. CLASSIFICATION. A 200 with a short body is a truncation -- a failure. A
//     500 is a start-up failure -- a DIFFERENT failure. Asserting only on body
//     length is what produced the phantom 0.3%.
//
//  2. NO TRUNCATION, under concurrency, where the race actually appeared.
//
//  3. THE RACE IS GONE: the stderr goroutine must not reap the child. If a
//     future edit moves cmd.Wait() back beside the stderr drain, this fails even
//     if the timing happens to be lucky on that run.
//
// Usage:  go test ./pkg/ffmpeg/ -run TestStreamTranscode -count=1
//
//	node --test? no -- this is a Go test; run it with `go test`.
//
// Mutation:  docs/mutate_ffmpeg_wait.py
package ffmpeg

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// --- 1. classification -------------------------------------------------------

// The two failure modes are different and must never be counted together.
func TestStreamTranscodeClassifiesItsOutcomes(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		body     string
		wantKind string
	}{
		{"full stream", http.StatusOK, stubPayloadString(), "truncation"},
		{"truncated stream", http.StatusOK, stubPayloadString()[:1], "truncation"},
		{"empty 200", http.StatusOK, "", "truncation"},
		{"exec refused", http.StatusBadRequest, "fork/exec: text file busy", "startup"},
		{"server error", http.StatusInternalServerError, "boom", "startup"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyServe(tc.code, tc.body)
			if got != tc.wantKind {
				t.Errorf("classify(%d, %q) = %q, want %q", tc.code, tc.body, got, tc.wantKind)
			}
		})
	}
}

// ETXTBSY, spelled out. The claim "the fix leaves 0.3% broken" came from reading
// these as truncations, so the distinction is asserted rather than assumed.
func TestStreamTranscodeRecognisesETXTBSY(t *testing.T) {
	const msg = "fork/exec /tmp/x/ffmpeg-stub: text file busy"
	if !strings.Contains(msg, "text file busy") {
		t.Fatal("the fixture no longer contains the marker this test keys on")
	}
	// A start-up failure is NOT a truncation, and must never be counted as one.
	if classifyServe(http.StatusBadRequest, msg) == "truncation" {
		t.Error("ETXTBSY was classified as a truncation; that is the misreading " +
			"that produced a phantom 0.3% regression")
	}
}

func classifyServe(code int, body string) string {
	// A 200 (or any 2xx) means the handler streamed. Anything shorter than the
	// payload is a truncation. A non-2xx means the child never started.
	if code >= 200 && code < 300 {
		return "truncation"
	}
	return "startup"
}

// --- 2. no truncation under concurrency --------------------------------------

// The race needed concurrency to show up: 14% per replay, but only under load.
func TestStreamTranscodeDoesNotTruncateUnderConcurrency(t *testing.T) {
	const workers, per = 12, 40

	var mu sync.Mutex
	ok, truncations, startups := 0, 0, 0
	var truncDetail []string

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < per; j++ {
				// One stub per serve, written and closed before use, so the
				// harness does not manufacture its own ETXTBSY. Each serve owns
				// its own directory, so no two goroutines share a path.
				dir, err := os.MkdirTemp("", "ffmpeg-stub")
				if err != nil {
					mu.Lock()
					startups++
					mu.Unlock()
					continue
				}
				// Write the stub ONCE per serve, into a directory no other
				// goroutine uses, and close it before exec. The original hammer
				// shared a t.TempDir() across workers and wrote stubs the
				// siblings were already trying to exec, which is what produced
				// the ETXTBSY that got misread as a truncation.
				stub := filepath.Join(dir, "ffmpeg-stub")
				body := "#!/bin/sh\nprintf '" + stubPayload + "'\nexit 0\n"
				if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
					_ = os.RemoveAll(dir)
					mu.Lock()
					startups++
					mu.Unlock()
					continue
				}
				sm := newTestStreamManager(t, stub)
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodGet, "/scene/stream.mp4", nil)
				sm.ServeTranscode(w, r, testOptions())
				_ = os.RemoveAll(dir)

				full := len(stubPayloadString())
				mu.Lock()
				switch {
				case w.Code >= 200 && w.Code < 300 && w.Body.Len() == full:
					ok++
				case w.Code >= 200 && w.Code < 300:
					truncations++
					truncDetail = append(truncDetail,
						fmt.Sprintf("%d/%d bytes %q", w.Body.Len(), full, w.Body.String()))
				default:
					startups++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	total := ok + truncations + startups
	if truncations > 0 {
		t.Errorf("%d/%d serves returned a TRUNCATED 200: %s",
			truncations, total, strings.Join(truncDetail, " | "))
	}
	// A start-up failure is not the bug under test, but a flood of them means
	// the harness is broken, and a harness that cannot run proves nothing.
	if startups > total/10 {
		t.Errorf("%d/%d serves failed to start; the harness is the problem, not the code", startups, total)
	}
	t.Logf("ok=%d truncated=%d startup-failed=%d of %d", ok, truncations, startups, total)
}

// --- 3. the fix is still in place --------------------------------------------

// A guard on the source, not only on behaviour: the race needed load to appear,
// so a lucky run of the tests above proves less than it looks. This is the cheap
// check that the stderr goroutine still does not reap the child.
func TestStreamTranscodeDoesNotReapTheChildFromTheStderrGoroutine(t *testing.T) {
	src, err := os.ReadFile("stream_transcode.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	// The stderr drain goroutine must be a drain and nothing else.
	//
	// Anchor on the goroutine LITERAL, not on the comment above it. The first
	// version sliced from the comment banner to `go func()`, which spans six
	// lines of prose explaining why cmd.Wait() must not be there -- prose that
	// necessarily contains the string "cmd.Wait()". So the guard failed on a
	// correct file, reading its own documentation as if it were code. A source
	// guard has to be as careful about what it is looking at as the code it
	// guards.
	// Slice to the goroutine's OWN closing brace by brace-counting, not to a
	// textual terminator. `\n\t})()` was the second attempt and it ran past the
	// goroutine into the comment block below it -- which also contains
	// "Wait()" -- so a correct file failed again. Every textual boundary tried
	// here has been wrong because the surrounding prose legitimately names the
	// very thing being forbidden.
	goroutine := funcBody(text, "go func() {")
	if goroutine == "" {
		t.Fatal("could not find the stderr drain goroutine in stream_transcode.go")
	}
	if strings.Contains(goroutine, ".Wait(") {
		t.Errorf("the stderr goroutine calls Wait() again:\n%s\n"+
			"Wait closes the child's pipes, so it races the handler's "+
			"peek-then-copy and truncates the stream -- 426 of 3000 replays "+
			"before the fix", goroutine)
	}
	// And the reap must be owned by the handler, which is the only place that
	// knows when stdout is finished.
	if !strings.Contains(text, "defer reap()") {
		t.Error("the handler does not `defer reap()`; the child would be left " +
			"unreaped, or reaped too early")
	}
}

// funcBody returns the body of the FIRST `start`-anchored brace block, by
// counting braces. Textual terminators are unreliable here: the file's comments
// discuss cmd.Wait() at length, and any end-marker wide enough to reach the
// right place also picks up that prose.
func funcBody(s, start string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	open := strings.Index(s[i:], "{")
	depth := 0
	for j := open; j < len(s)-i; j++ {
		switch s[i+j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[i+open+1 : i+j]
			}
		}
	}
	return ""
}
