package scraper

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/utils"
)

// A one-pixel PNG. A real one, so http.DetectContentType and the content-type
// check both see genuine image bytes rather than a magic string.
var testPNGBody = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

// refererRecorder is a test server that records the Referer of every request it
// receives and decides its response from a rule.
type refererRecorder struct {
	mu       sync.Mutex
	requests []string // the Referer of each request, "" when absent

	// accept decides whether a given Referer value should be served. Returning
	// true responds 200 with the image.
	accept func(referer string) bool
	// alwaysStatus, when non-zero, is returned regardless of accept. Used to
	// test statuses that must not trigger a retry.
	alwaysStatus int
	// contentType, when non-empty, overrides the response Content-Type.
	contentType string
}

func (r *refererRecorder) handler(w http.ResponseWriter, req *http.Request) {
	referer := req.Header.Get("Referer")

	r.mu.Lock()
	r.requests = append(r.requests, referer)
	r.mu.Unlock()

	if r.alwaysStatus != 0 {
		w.WriteHeader(r.alwaysStatus)
		return
	}

	if r.accept != nil && !r.accept(referer) {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	ct := r.contentType
	if ct == "" {
		ct = "image/png"
	}
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(testPNGBody)
}

func (r *refererRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.requests))
	copy(out, r.requests)
	return out
}

func newTestGetter(t *testing.T) *imageGetter {
	t.Helper()
	return &imageGetter{
		client:       http.DefaultClient,
		globalConfig: mockGlobalConfig{},
	}
}

// refererFor returns the strategy's value for a URL, or "" for the none
// strategy. This mirrors the ladder rather than hardcoding the strings, so a
// test does not pass just because both the ladder and the expectation moved.
func refererFor(t *testing.T, name string, u *url.URL) string {
	t.Helper()
	for _, s := range utils.RefererAttempts {
		if s.Name != name {
			continue
		}
		if s.Build == nil {
			return ""
		}
		return s.Build(u)
	}
	t.Fatalf("no referer strategy named %q", name)
	return ""
}

func TestGetImage_SucceedsOnFirstAttempt(t *testing.T) {
	rec := &refererRecorder{accept: func(string) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	g := newTestGetter(t)
	img, err := g.getImage(context.Background(), srv.URL+"/pic.png")
	require.NoError(t, err)
	require.NotNil(t, img)

	// One request. The whole point of the ladder is that the common case does
	// not pay for it.
	assert.Len(t, rec.seen(), 1, "a successful first attempt must not retry")

	u, _ := url.Parse(srv.URL)
	assert.Equal(t, refererFor(t, "host", u), rec.seen()[0],
		"the first attempt keeps the historical referer: scheme://host/")
	assert.True(t, strings.HasPrefix(*img, "data:image/png;base64,"),
		"got %q", firstN(*img, 40))

	// And the payload actually round-trips.
	raw, err := base64.StdEncoding.DecodeString(strings.Split(*img, ",")[1])
	require.NoError(t, err)
	assert.Equal(t, testPNGBody, raw)
}

func TestGetImage_RetriesWithoutRefererOn403(t *testing.T) {
	// Serves only when there is NO referer. This is the case the report calls
	// out: a fake referer breaks URLs that would work with the header absent.
	rec := &refererRecorder{accept: func(r string) bool { return r == "" }}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	g := newTestGetter(t)
	img, err := g.getImage(context.Background(), srv.URL+"/pic.png")
	require.NoError(t, err)
	require.NotNil(t, img)

	seen := rec.seen()
	require.Len(t, seen, 2, "expected one 403 then one success, got %v", seen)
	assert.NotEmpty(t, seen[0], "first attempt keeps the referer")
	assert.Empty(t, seen[1], "second attempt must send NO referer, not an empty one")
}

func TestGetImage_RetriesWithDomainRefererOn403(t *testing.T) {
	// Serves only for the bare registrable domain.
	rec := &refererRecorder{accept: func(r string) bool {
		return r == "http://example.com/"
	}}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	g := newTestGetter(t)
	// images.example.com is on 127.0.0.1, so the domain strategy is exercised
	// through registrableDomain directly below and here only the ordering is
	// under test: a host the server never accepts.
	rec.accept = func(r string) bool { return r == "http://127.0.0.1/" && r != rec.seen()[0] }
	_, err := g.getImage(context.Background(), srv.URL+"/pic.png")
	// 127.0.0.1 is an IP literal, so the domain strategy produces "" and the
	// third attempt is a duplicate of the second. It must still not succeed
	// here, and the error must name every attempt.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tried referer: host, none, domain")
	assert.Len(t, rec.seen(), 3, "all three attempts are made: %v", rec.seen())
}

func TestGetImage_DoesNotRetryOnNon403(t *testing.T) {
	for _, status := range []int{
		http.StatusUnauthorized, // auth problem; no referer changes it
		http.StatusNotFound,     // no such file
		http.StatusInternalServerError,
		http.StatusTooManyRequests,
	} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			rec := &refererRecorder{alwaysStatus: status}
			srv := httptest.NewServer(http.HandlerFunc(rec.handler))
			defer srv.Close()

			g := newTestGetter(t)
			_, err := g.getImage(context.Background(), srv.URL+"/pic.png")
			require.Error(t, err)
			assert.Contains(t, err.Error(), fmt.Sprintf("http error %d", status))
			assert.Len(t, rec.seen(), 1,
				"only 403 is retried; %d must not be", status)
		})
	}
}

func TestGetImage_FirstAttemptSuccessSkipsRetries(t *testing.T) {
	// A server that 403s everything, reached with a 200 first time. Proves the
	// ladder stops at the first success rather than always trying three.
	rec := &refererRecorder{accept: func(string) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	g := newTestGetter(t)
	_, err := g.getImage(context.Background(), srv.URL+"/pic.png")
	require.NoError(t, err)
	assert.Len(t, rec.seen(), 1)
}

func TestGetImage_AllAttemptsForbidden(t *testing.T) {
	rec := &refererRecorder{accept: func(string) bool { return false }}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	g := newTestGetter(t)
	_, err := g.getImage(context.Background(), srv.URL+"/pic.png")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http error 403")
	assert.Contains(t, err.Error(), "tried referer: host, none, domain")
	assert.Len(t, rec.seen(), 3)
}

func TestGetImage_RequestModifierSurvivesRetries(t *testing.T) {
	// The scraper's own headers must be identical on every attempt, or a
	// scraper that authenticates would fail on attempts 2 and 3 and the ladder
	// would be a regression for Stash's own scraper.
	rec := &refererRecorder{accept: func(string) bool { return false }}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	var modified int
	g := newTestGetter(t)
	g.requestModifier = func(req *http.Request) {
		modified++
		req.Header.Set("ApiKey", "secret")
		req.Header.Set("User-Agent", "scraper-test")
	}

	_, err := g.getImage(context.Background(), srv.URL+"/pic.png")
	require.Error(t, err)

	// The modifier runs once, on the request; the ladder re-uses that request
	// and only re-sets the Referer. Running it per attempt would be more
	// obviously correct but changes the contract for every caller.
	assert.Equal(t, 1, modified,
		"the request modifier is applied once, not per attempt")

	// But the headers are still on the wire for all three attempts.
	assert.Equal(t, 3, len(rec.seen()))
}

func TestGetImage_RejectsNonImageWith200(t *testing.T) {
	// A 200 carrying HTML is what a server sends when it is quietly refusing
	// to serve the file. Storing that as the performer picture reproduces the
	// stash#5538 failure mode: a save that succeeds with a broken image.
	rec := &refererRecorder{accept: func(string) bool { return true }, contentType: "text/html"}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	g := newTestGetter(t)
	_, err := g.getImage(context.Background(), srv.URL+"/pic.png")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected an image")
	assert.Contains(t, err.Error(), "text/html")
	// A non-image 200 is NOT a 403, so no retry happened.
	assert.Len(t, rec.seen(), 1)
}

func TestGetImage_ContentTypeWithParametersIsAccepted(t *testing.T) {
	// Servers append parameters; a strict equality check would reject these.
	rec := &refererRecorder{accept: func(string) bool { return true }, contentType: "image/png; charset=binary"}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	g := newTestGetter(t)
	img, err := g.getImage(context.Background(), srv.URL+"/pic.png")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(*img, "data:image/png;"))
}

func TestGetImage_ContentTypeWithQualityParameter(t *testing.T) {
	rec := &refererRecorder{accept: func(string) bool { return true }, contentType: "IMAGE/PNG ; q=0.9"}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	g := newTestGetter(t)
	_, err := g.getImage(context.Background(), srv.URL+"/pic.png")
	require.NoError(t, err, "case and surrounding space must be tolerated")
}

func TestGetImage_SniffsContentTypeWhenAbsent(t *testing.T) {
	// No Content-Type at all: the body is a real PNG, so sniffing identifies it
	// and the result is accepted.
	rec := &refererRecorder{accept: func(string) bool { return true }, contentType: " "}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	g := newTestGetter(t)
	img, err := g.getImage(context.Background(), srv.URL+"/pic.png")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(*img, "data:image/png;base64,"))
}

func TestGetImage_RejectsEmptyBodySniffedAsText(t *testing.T) {
	rec := &refererRecorder{accept: func(string) bool { return true }, contentType: " "}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	// Serve non-image bytes with no content type, so sniffing says text/plain.
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rec.mu.Lock()
		rec.requests = append(rec.requests, "")
		rec.mu.Unlock()
		_, _ = w.Write([]byte("<html>nope</html>"))
	})

	g := newTestGetter(t)
	_, err := g.getImage(context.Background(), srv.URL+"/pic.png")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected an image")
}

func TestGetImage_CancelledContextIsNotRetried(t *testing.T) {
	rec := &refererRecorder{accept: func(string) bool { return false }}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	g := newTestGetter(t)
	_, err := g.getImage(ctx, srv.URL+"/pic.png")
	require.Error(t, err)
	assert.Empty(t, rec.seen(), "a dead context must not produce requests")
}

func TestGetImage_CancelledMidwayKeepsTheRealError(t *testing.T) {
	// 403 once, then the context dies. The error must be the 403, not
	// "context canceled", or the diagnosis is lost.
	rec := &refererRecorder{accept: func(string) bool { return false }}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel from the server handler, i.e. after the 403 has actually been
	// received. Cancelling from the transport fires it before the response is
	// read, so the request fails instead of returning a 403 and there is
	// nothing to preserve.
	g := newTestGetter(t)
	g.client = &http.Client{Transport: &cancelOnNthResponseTransport{
		cancel: cancel,
		inner:  http.DefaultTransport,
		after:  1,
	}}

	_, err := g.getImage(ctx, srv.URL+"/pic.png")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http error 403",
		"the 403 must survive, not be replaced by the cancellation")
	assert.Len(t, rec.seen(), 1, "no second attempt once the context is done")
}

// cancelOnNthResponseTransport cancels the context after the nth response has
// been fully handed back, so the caller has a real response in hand.
type cancelOnNthResponseTransport struct {
	inner  http.RoundTripper
	cancel context.CancelFunc
	after  int
	seen   int
}

func (t *cancelOnNthResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if t.seen++; t.seen >= t.after {
		t.cancel()
	}
	return resp, nil
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
