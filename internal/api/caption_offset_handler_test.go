package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/models"
)

// stash#4771 -- the caption offset must be applied BY THE HANDLER.
//
// ## WHY THIS FILE EXISTS AND WHY THE SIMPLER ONE IS NOT ENOUGH
//
// internal/api/caption_offset_test.go tests parseCaptionOffset and astisub's Subtitles.Add directly.
// Those tests all pass with the offset logic deleted from the request path — verified by mutation, not
// assumed: replacing `shiftCaptions(r, sub)` in the handler with a comment left the suite green.
//
// That is the same trap as the #7130 goroutine-leak test and the #3738 interactiveDisagrees helper. A
// unit test proves a helper works; it says nothing about whether the request path calls it. So this file
// drives sceneRoutes.Caption over HTTP and asserts on the bytes it writes.
//
// The transaction and caption finder are faked rather than stood up for real: both interfaces are four
// methods wide (txn.Manager, TxnDatabaseProvider, CaptionFinder), and a real database would make this a
// slower test of the same thing.

// --- fakes ---------------------------------------------------------------------------------------

type fakeTxnManager struct{}

func (fakeTxnManager) Begin(ctx context.Context, writable bool) (context.Context, error) {
	return ctx, nil
}
func (fakeTxnManager) Commit(context.Context) error   { return nil }
func (fakeTxnManager) Rollback(context.Context) error { return nil }
func (fakeTxnManager) IsLocked(error) bool            { return false }

type fakeCaptionFinder struct {
	captions []*models.VideoCaption
}

func (f fakeCaptionFinder) GetCaptions(context.Context, models.FileID) ([]*models.VideoCaption, error) {
	return f.captions, nil
}

// --- fixture -------------------------------------------------------------------------------------

// vttTimestamp matches the `HH:MM:SS.mmm --> HH:MM:SS.mmm` timing line of a WebVTT cue.
var vttTimestamp = regexp.MustCompile(`(\d+):(\d+):(\d+)\.(\d+) --> (\d+):(\d+):(\d+)\.(\d+)`)

func millisOf(m []string, hIdx, mIdx, sIdx, msIdx int) int64 {
	h, _ := strconv.ParseInt(m[hIdx], 10, 64)
	min, _ := strconv.ParseInt(m[mIdx], 10, 64)
	s, _ := strconv.ParseInt(m[sIdx], 10, 64)
	ms, _ := strconv.ParseInt(m[msIdx], 10, 64)
	return ((h*60+min)*60+s)*1000 + ms
}

// cueStarts returns the start time of every cue in a WebVTT body, in order.
//
// Parsing all of them rather than regex-matching a literal timestamp is the correction to an earlier
// version of this file: it asserted `00:00:0[67]` appears, which broke the moment a test used an offset
// that moved the second cue to 00:00:04.500. A regex written to fit one expected output tests the
// regex, not the code.
func cueStarts(t *testing.T, body string) []int64 {
	t.Helper()

	var out []int64
	for _, m := range vttTimestamp.FindAllStringSubmatch(body, -1) {
		out = append(out, millisOf(m, 1, 2, 3, 4))
	}
	return out
}

// writeSRT writes a two-cue SRT file with known timings, and returns the path.
func writeSRT(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "scene.en.srt")
	const body = "1\n00:00:01,000 --> 00:00:02,000\nfirst\n\n2\n00:00:05,000 --> 00:00:06,000\nsecond\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newCaptionTestRoutes(videoPath string, captions []*models.VideoCaption) sceneRoutes {
	return sceneRoutes{
		routes:        routes{txnManager: fakeTxnManager{}},
		captionFinder: fakeCaptionFinder{captions: captions},
	}
}

// --- the test ------------------------------------------------------------------------------------

func TestCaptionHandlerAppliesTheOffset(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "scene.mp4")
	if err := os.WriteFile(videoPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	srtPath := writeSRT(t, dir)

	captions := []*models.VideoCaption{
		{LanguageCode: "en", Filename: filepath.Base(srtPath), CaptionType: "srt"},
	}

	scene := &models.Scene{
		Path: videoPath,
		Files: models.NewRelatedVideoFiles([]*models.VideoFile{
			{BaseFile: &models.BaseFile{Path: videoPath}},
		}),
	}

	tests := []struct {
		name  string
		query string
		// Every cue's expected start, in order. Authored timings are 1000 and 5000.
		wantCueStarts []int64
	}{
		{
			// The baseline: no offset, cues at their authored times.
			name:          "no offset serves the authored timings",
			query:         "lang=en&type=srt",
			wantCueStarts: []int64{1000, 5000},
		},
		{
			name:          "a positive offset shifts every cue later",
			query:         "lang=en&type=srt&offset=2000",
			wantCueStarts: []int64{3000, 7000},
		},
		{
			name:          "a negative offset shifts every cue earlier",
			query:         "lang=en&type=srt&offset=-500",
			wantCueStarts: []int64{500, 4500},
		},
		{
			// The clamp: a cue straddling zero must start at 0, not at a negative timestamp. WebVTT has
			// no representation for one, and browsers discard the whole track rather than shift it.
			name:          "an offset past the start clamps to zero",
			query:         "lang=en&type=srt&offset=-1500",
			wantCueStarts: []int64{0, 3500},
		},
		{
			// Nonsense degrades to no offset rather than failing the request.
			name:          "an unparseable offset serves the track unchanged",
			query:         "lang=en&type=srt&offset=nonsense",
			wantCueStarts: []int64{1000, 5000},
		},
		{
			// Only the cue that would fall before zero is dropped; the rest survive. A viewer nudging
			// subtitles earlier must not lose the whole track. Authored cues are 1s and 5s, so -6s
			// sends the first to -5s (dropped) and the second to -1s (also dropped) -- -4s is the
			// value that splits them.
			name:          "a negative offset drops only the cues that fall before zero",
			query:         "lang=en&type=srt&offset=-4000",
			wantCueStarts: []int64{1000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := newCaptionTestRoutes(videoPath, captions)

			r := httptest.NewRequest("GET", "/scene/1/caption?"+tt.query, nil)
			r = r.WithContext(context.WithValue(r.Context(), sceneKey, scene))

			w := httptest.NewRecorder()
			rs.Caption(w, r, "en", "srt")

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); ct != "text/vtt" {
				t.Errorf("Content-Type = %q, want text/vtt", ct)
			}

			body := w.Body.String()
			starts := cueStarts(t, body)

			if len(starts) != len(tt.wantCueStarts) {
				t.Fatalf("got %d cues, want %d:\n%s", len(starts), len(tt.wantCueStarts), body)
			}
			for i := range starts {
				if starts[i] != tt.wantCueStarts[i] {
					t.Errorf("cue %d starts at %dms, want %dms\n---\n%s", i, starts[i], tt.wantCueStarts[i], body)
				}
			}
		})
	}
}

// TestCaptionHandlerServesAnEmptyTrackWhenEveryCueIsDropped is the regression test for a bug the
// offset feature introduced.
//
// Subtitles.Add drops cues pushed before zero. Nudge the captions far enough back and the track empties,
// at which point astisub returns ErrNoSubtitlesToWrite -- and the handler's original code turned that
// into a 500. So a purely cosmetic viewer preference produced a server error, and videojs logs it as a
// console error on every scene with that language selected.
//
// An empty WebVTT file is valid and means "no subtitles", which is the truth: there are no cues left
// inside the video's duration. The response is 200 with a header-only body, NOT a 500.
//
// This is deliberately distinct from a READ failure, which really is a 500: if the file cannot be parsed
// there is nothing to serve, whereas here it parsed fine.
func TestCaptionHandlerServesAnEmptyTrackWhenEveryCueIsDropped(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "scene.mp4")
	if err := os.WriteFile(videoPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	srtPath := writeSRT(t, dir)

	captions := []*models.VideoCaption{
		{LanguageCode: "en", Filename: filepath.Base(srtPath), CaptionType: "srt"},
	}
	scene := &models.Scene{
		Path: videoPath,
		Files: models.NewRelatedVideoFiles([]*models.VideoFile{
			{BaseFile: &models.BaseFile{Path: videoPath}},
		}),
	}

	rs := newCaptionTestRoutes(videoPath, captions)

	// -10s pushes both authored cues (1s and 5s) before zero.
	r := httptest.NewRequest("GET", "/scene/1/caption?lang=en&type=srt&offset=-10000", nil)
	r = r.WithContext(context.WithValue(r.Context(), sceneKey, scene))

	w := httptest.NewRecorder()
	rs.Caption(w, r, "en", "srt")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- an emptied track is not a server error (body: %s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/vtt" {
		t.Errorf("Content-Type = %q, want text/vtt", ct)
	}

	body := w.Body.String()
	if !strings.HasPrefix(body, "WEBVTT") {
		t.Errorf("an empty track must still be a valid WebVTT header, got:\n%s", body)
	}
	if starts := cueStarts(t, body); len(starts) != 0 {
		t.Errorf("got %d cues, want none:\n%s", len(starts), body)
	}

	// And the real invariant: never a negative timestamp, whatever the offset.
	if regexp.MustCompile(`-->\s*-`).MatchString(body) {
		t.Errorf("the body must not contain a negative timestamp:\n%s", body)
	}
}
