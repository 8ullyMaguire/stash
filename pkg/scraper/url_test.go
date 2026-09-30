package scraper

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/chromedp/cdproto/network"
)

// The fix itself, and the branch upstream's tests could not reach.
//
// The mime sniff and the tracker both have tests above, and between them they
// look like the fix is covered. They are not: the actual repair -- reading a
// JSON document's body instead of Chrome's HTML rendering of it -- was inline
// in a `chromedp.ActionFunc`, reachable only by running a browser. The harness
// recorded that as a SURVIVOR, and the fix was to extract the decision into
// `readMainDocument` so it could be driven here.
//
// Both accessors are stubs that RECORD what they were asked for, because the
// question is not "what did it return" but "which one did it ask".
func TestReadMainDocumentUsesTheRawBodyForAJSONDocument(t *testing.T) {
	const payload = `{"performers":["alice"]}`

	var askedForBody, askedForHTML bool
	body := func(_ context.Context, id network.RequestID) ([]byte, error) {
		askedForBody = true
		if id != "req-1" {
			return nil, fmt.Errorf("asked for the wrong request id: %q", id)
		}
		return []byte(payload), nil
	}
	html := func(context.Context) (string, error) {
		askedForHTML = true
		return "<html><body><pre>" + payload + "</pre></body></html>", nil
	}

	got, err := readMainDocument(context.Background(), body, html, "req-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != payload {
		t.Errorf("got %q, want the RAW body %q. Chrome's HTML wrapping is the bug "+
			"this closes: the caller parses JSON, and an HTML wrapper is a parse "+
			"failure that looks like a broken scraper", got, payload)
	}
	if !askedForBody {
		t.Error("the raw body was never requested; this reproduces the original bug " +
			"of always asking for OuterHTML")
	}
	if askedForHTML {
		t.Error("OuterHTML was requested for a JSON document; it is the path that " +
			"produces the HTML wrapper")
	}
}

// The control for the other direction, and the reason the assertion above is
// not a tautology: a "fix" that never used either accessor would satisfy every
// statement in it.
func TestReadMainDocumentUsesOuterHTMLForAnHTMLDocument(t *testing.T) {
	const markup = "<html><body>hello</body></html>"

	var askedForBody, askedForHTML bool
	body := func(context.Context, network.RequestID) ([]byte, error) {
		askedForBody = true
		return nil, errors.New("must not be called")
	}
	html := func(context.Context) (string, error) {
		askedForHTML = true
		return markup, nil
	}

	got, err := readMainDocument(context.Background(), body, html, "req-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != markup {
		t.Errorf("got %q, want %q", got, markup)
	}
	if !askedForHTML {
		t.Error("OuterHTML was not requested for an HTML document")
	}
	if askedForBody {
		t.Error("the raw body was requested for an HTML document; the DOM is the " +
			"right answer there and this is a wasted call that can fail")
	}
}

// The body's absence is a real case, not a theoretical one: Chrome evicts
// response bodies from its cache, and the pre-fix behaviour in that situation
// was the same broken OuterHTML. The fallback must still produce content, and
// must be the HTML path rather than an empty string.
func TestReadMainDocumentFallsBackToHTMLWhenTheBodyIsUnavailable(t *testing.T) {
	const markup = "<html><body>partial</body></html>"

	body := func(context.Context, network.RequestID) ([]byte, error) {
		return nil, errors.New("No resource with given identifier found")
	}
	html := func(context.Context) (string, error) { return markup, nil }

	got, err := readMainDocument(context.Background(), body, html, "req-1", true)
	if err != nil {
		t.Fatalf("a body that could not be read must not become an error; the "+
			"OuterHTML path is a better answer than failing: %v", err)
	}
	if got != markup {
		t.Errorf("got %q, want the OuterHTML fallback %q", got, markup)
	}
}

// An accessor failing must propagate, not be swallowed. A swallowed error here
// returns "" and the scraper fails with a confusing empty-result error instead
// of saying the DOM could not be read.
func TestReadMainDocumentPropagatesAnOuterHTMLFailure(t *testing.T) {
	boom := errors.New("cannot read the DOM")
	html := func(context.Context) (string, error) { return "", boom }

	_, err := readMainDocument(context.Background(),
		func(context.Context, network.RequestID) ([]byte, error) {
			t.Error("the body accessor was called for an HTML document")
			return nil, nil
		}, html, "req-1", false)

	if !errors.Is(err, boom) {
		t.Errorf("got %v, want the accessor's error %v; a swallowed error here "+
			"surfaces later as an empty scrape result", err, boom)
	}
}

func TestIsJSONMimeType(t *testing.T) {
	tests := []struct {
		name     string
		mimeType string
		want     bool
	}{
		{"plain json", "application/json", true},
		{"json with charset", "application/json; charset=utf-8", true},
		{"json with charset and spacing", "application/json;  charset=UTF-8", true},
		{"structured syntax suffix", "application/ld+json", true},
		{"uppercase", "APPLICATION/JSON", true},
		{"text/json", "text/json", true},
		{"html", "text/html", false},
		{"html with charset", "text/html; charset=utf-8", false},
		{"plain text", "text/plain", false},
		{"empty", "", false},
		{"contains json as substring but isn't json", "application/jsonp", false},
		{"contains json as substring but isn't json 2", "multipart/json-form-data", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isJSONMimeType(tt.mimeType)
			if got != tt.want {
				t.Errorf("isJSONMimeType(%q) = %v, want %v", tt.mimeType, got, tt.want)
			}
		})
	}
}

func TestJSONDocumentTrackerOnlyRecordsFirstDocument(t *testing.T) {
	t.Run("first document is JSON, later ones don't override it", func(t *testing.T) {
		var tracker jsonDocumentTracker

		tracker.markDocument("req-1", "application/json")
		tracker.markDocument("req-2", "application/json")
		tracker.markDocument("req-3", "text/html")

		if requestID, isJSON := tracker.mainDocument(); !isJSON || requestID != "req-1" {
			t.Fatalf("mainDocument() = (%q, %v), want (%q, true)", requestID, isJSON, "req-1")
		}
	})

	t.Run("first document is HTML, a later JSON response doesn't override it", func(t *testing.T) {
		// e.g. an iframe firing a later JSON Document response must not
		// override the main page's HTML.
		var tracker jsonDocumentTracker

		tracker.markDocument("req-main-page", "text/html")
		tracker.markDocument("req-iframe", "application/json")

		if requestID, isJSON := tracker.mainDocument(); isJSON || requestID != "req-main-page" {
			t.Fatalf("mainDocument() = (%q, %v), want (%q, false)", requestID, isJSON, "req-main-page")
		}
	})
}

// Mirrors urlFromCDP's usage: concurrent markDocument/mainDocument calls.
// Fails under `go test -race` without the mutex.
func TestJSONDocumentTrackerConcurrentAccess(t *testing.T) {
	var tracker jsonDocumentTracker

	const n = 200
	var wg sync.WaitGroup
	wg.Add(2 * n)

	for i := range n {
		go func() {
			defer wg.Done()
			tracker.markDocument(network.RequestID(fmt.Sprintf("req-%d", i)), "application/json")
		}()
		go func() {
			defer wg.Done()
			tracker.mainDocument()
		}()
	}

	wg.Wait()

	requestID, isJSON := tracker.mainDocument()
	if !isJSON {
		t.Fatal("expected a JSON document to have been recorded")
	}
	if requestID == "" {
		t.Fatal("expected a non-empty request ID to have been recorded")
	}
}
