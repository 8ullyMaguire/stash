package utils

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Referer ladder for outbound image requests. (stash#2540)
//
// These live in pkg/utils rather than next to the scraper because there are two
// outbound image paths -- ReadImageFromURL and the scraper's getImage -- and the
// bug was in both. Testing the shared implementation once is the only way to
// know the second caller is covered.

// onePixelPNG is a real PNG, so content sniffing sees genuine image bytes
// rather than a magic string that only looks like one.
var onePixelPNG = []byte{
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

// refererRecorder records the Referer of every request and decides its response
// from a rule.
type refererRecorder struct {
	mu       sync.Mutex
	requests []string

	// accept decides whether a given Referer should be served. nil serves
	// everything.
	accept func(referer string) bool
	// alwaysStatus, when non-zero, is returned regardless of accept.
	alwaysStatus int
	contentType  string
	// body overrides the response body. nil uses onePixelPNG.
	body []byte
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
	body := r.body
	if body == nil {
		body = onePixelPNG
	}
	_, _ = w.Write(body)
}

func (r *refererRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.requests))
	copy(out, r.requests)
	return out
}

func serve(t *testing.T, rec *refererRecorder) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	t.Cleanup(srv.Close)
	return srv
}

func TestReadImageFromURL_FirstAttemptSucceeds(t *testing.T) {
	rec := &refererRecorder{accept: func(string) bool { return true }}
	srv := serve(t, rec)

	body, err := ReadImageFromURL(context.Background(), srv.URL+"/pic.png")
	require.NoError(t, err)
	assert.Equal(t, onePixelPNG, body)

	// One request. The ladder must not cost the common case anything.
	assert.Len(t, rec.seen(), 1)

	u, _ := url.Parse(srv.URL)
	assert.Equal(t, refererFor(t, "host", u), rec.seen()[0],
		"the first attempt keeps the historical referer, scheme://host/")
}

func TestReadImageFromURL_RetriesWithoutRefererOn403(t *testing.T) {
	// The case the report describes: a fake referer breaks URLs that would work
	// with the header absent.
	rec := &refererRecorder{accept: func(r string) bool { return r == "" }}
	srv := serve(t, rec)

	body, err := ReadImageFromURL(context.Background(), srv.URL+"/pic.png")
	require.NoError(t, err)
	assert.Equal(t, onePixelPNG, body)

	seen := rec.seen()
	require.Len(t, seen, 2, "want one 403 then one success, got %v", seen)
	assert.NotEmpty(t, seen[0], "first attempt keeps the referer")
	assert.Empty(t, seen[1], "second attempt must send NO referer, not a blank one")
}

func TestReadImageFromURL_AllAttemptsForbiddenNamesThem(t *testing.T) {
	rec := &refererRecorder{accept: func(string) bool { return false }}
	srv := serve(t, rec)

	_, err := ReadImageFromURL(context.Background(), srv.URL+"/pic.png")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http error 403")
	// The error has to say what was tried, or a user reporting "images still
	// don't load" has nothing to act on.
	assert.Contains(t, err.Error(), "tried referer: host, none, domain")
	assert.Len(t, rec.seen(), 3)
}

func TestReadImageFromURL_DoesNotRetryOnNon403(t *testing.T) {
	for _, status := range []int{
		http.StatusUnauthorized, // auth problem; no referer changes it
		http.StatusNotFound,     // no such file
		http.StatusInternalServerError,
		http.StatusTooManyRequests,
	} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			rec := &refererRecorder{alwaysStatus: status}
			srv := serve(t, rec)

			_, err := ReadImageFromURL(context.Background(), srv.URL+"/pic.png")
			require.Error(t, err)
			assert.Contains(t, err.Error(), fmt.Sprintf("http error %d", status))
			assert.Len(t, rec.seen(), 1, "only 403 is retried; %d must not be", status)
		})
	}
}

func TestReadImageFromURL_HTMLBodyIsReturnedUnchanged(t *testing.T) {
	// A 200 carrying HTML is what a server sends when it is quietly refusing.
	// ReadImageFromURL returns raw bytes -- rejecting it is validateImageData's
	// job, and the ladder must not paper over it by retrying a successful
	// response. This pins both halves: the bytes come through, and only one
	// request is made.
	rec := &refererRecorder{accept: func(string) bool { return true }, body: []byte("<html>no</html>")}
	srv := serve(t, rec)

	body, err := ReadImageFromURL(context.Background(), srv.URL+"/pic.png")
	require.NoError(t, err)
	assert.Equal(t, "<html>no</html>", string(body))

	// One request only -- a second fetch here would be a test bug, not a retry,
	// and the two are indistinguishable from the count alone.
	assert.Len(t, rec.seen(), 1, "a 200 is a 200, whatever the body")
}

func TestReadImageFromURL_CancelledContextMakesNoRequest(t *testing.T) {
	rec := &refererRecorder{accept: func(string) bool { return false }}
	srv := serve(t, rec)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ReadImageFromURL(ctx, srv.URL+"/pic.png")
	require.Error(t, err)
	assert.Empty(t, rec.seen(), "a dead context must not produce requests")
}

func TestReadImageFromURL_CancelledMidwayKeepsTheRealError(t *testing.T) {
	// 403 once, then the context dies. The error must be the 403, not
	// "context canceled", or the diagnosis is lost.
	rec := &refererRecorder{accept: func(string) bool { return false }}
	srv := serve(t, rec)

	ctx, cancel := context.WithCancel(context.Background())

	client := &http.Client{Transport: &cancelOnNthResponseTransport{
		cancel: cancel,
		inner:  http.DefaultTransport,
		after:  1,
	}}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/pic.png", nil)
	require.NoError(t, err)
	req.Header.Set("User-Agent", getUserAgent())

	_, _, err = DoWithRefererLadder(ctx, client, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http error 403",
		"the 403 must survive, not be replaced by the cancellation")
	assert.Len(t, rec.seen(), 1, "no second attempt once the context is done")
}

func TestDoWithRefererLadder_TransportErrorIsNotRetried(t *testing.T) {
	// A transport error means the request never reached a server that could
	// object to the Referer, so the other strategies are not a second opinion
	// on anything. Retrying just triples the wait on an unreachable host.
	//
	// The count of RoundTrip calls is the assertion that matters here. Asserting
	// on what the SERVER saw is useless for a transport that never reaches one,
	// and asserting on the error alone passes whether the ladder retried or
	// not: the last error is the same either way.
	var attempts int
	client := &http.Client{Transport: countingFailingTransport{onCall: func() { attempts++ }}}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.invalid/pic.png", nil)
	require.NoError(t, err)

	_, _, err = DoWithRefererLadder(context.Background(), client, req)
	require.Error(t, err)
	assert.Equal(t, 1, attempts, "a transport error must be returned, not retried")
	assert.Contains(t, err.Error(), "dial failed")
}

type countingFailingTransport struct{ onCall func() }

func (t countingFailingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if t.onCall != nil {
		t.onCall()
	}
	return nil, fmt.Errorf("dial failed")
}

// cancelOnNthResponseTransport cancels the context after the nth response has
// been handed back, so the caller has a real response in hand. Cancelling from
// inside the request would fire it before the response is read.
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

func TestRefererAttempts_OrderIsHostNoneDomain(t *testing.T) {
	// The order is the fix. A test that checked each strategy in isolation
	// would pass with the order reversed, which would triple the requests for
	// the common case -- the one thing a fallback must not do.
	names := make([]string, 0, len(RefererAttempts))
	for _, s := range RefererAttempts {
		names = append(names, s.Name)
	}
	assert.Equal(t, []string{"host", "none", "domain"}, names)
}

func TestRefererStrategyApply_ClearsPreviousValue(t *testing.T) {
	// The "none" strategy is defined by the ABSENCE of the header. If a retry
	// inherited the previous attempt's value, it would silently repeat the
	// first attempt and the ladder would never work while looking like it did.
	u, _ := url.Parse("https://images.example.com/pic.png")
	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)

	RefererAttempts[0].Apply(req)
	require.NotEmpty(t, req.Header.Get("Referer"))

	RefererAttempts[1].Apply(req)
	_, present := req.Header["Referer"]
	assert.False(t, present, "the none strategy must delete the header, not blank it")

	RefererAttempts[2].Apply(req)
	assert.Equal(t, "https://example.com/", req.Header.Get("Referer"))
}

func TestRegistrableDomain(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// A bare hostname is already the registrable domain.
		{"example.com", "example.com"},
		{"EXAMPLE.COM", "example.com"},
		{"example.com.", "example.com"},

		// Subdomains collapse.
		{"images.example.com", "example.com"},
		{"a.b.c.example.com", "example.com"},

		// Two-part public suffixes keep three labels.
		{"cdn.example.co.uk", "example.co.uk"},
		{"example.com.au", "example.com.au"},
		{"a.b.example.co.jp", "example.co.jp"},
		{"www.example.co.in", "example.co.in"},

		// An IP has no registrable domain. Taking the last two labels of
		// "192.168.0.1" would give "0.1", which as a referer is nonsense and
		// would put part of a private address in a header sent to a third party.
		{"127.0.0.1", ""},
		{"192.168.0.1", ""},
		{"::1", ""},
		{"2001:db8::1", ""},
		// Brackets, in case a bracketed literal arrives directly.
		{"[::1]", ""},

		{"", ""},
		{".", ""},
	}

	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			assert.Equal(t, c.want, RegistrableDomain(c.in))
		})
	}
}

func TestIsImageContentType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"image/webp; charset=binary", true},
		{"IMAGE/PNG", true},
		{" image/gif ", true},
		{"", true}, // sniffed by the caller
		{"text/html", false},
		{"application/json", false},
		{"video/mp4", false},
		{"text/html; charset=utf-8", false},
	}

	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			assert.Equal(t, c.want, IsImageContentType(c.in))
		})
	}
}

// refererFor mirrors the ladder rather than hardcoding the expected strings, so
// a test cannot pass just because the ladder and the expectation moved together.
func refererFor(t *testing.T, name string, u *url.URL) string {
	t.Helper()
	for _, s := range RefererAttempts {
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

func TestRefererFor_HelperIsUsedConsistently(t *testing.T) {
	// Guards the helper itself: if it silently returned "" the assertions above
	// would compare "" to "" and pass.
	u, _ := url.Parse("https://images.example.com/pic.png")
	assert.Equal(t, "https://images.example.com/", refererFor(t, "host", u))
	assert.Equal(t, "", refererFor(t, "none", u))
	assert.Equal(t, "https://example.com/", refererFor(t, "domain", u))
}

func TestDomainStrategyNeverLeaksAnIP(t *testing.T) {
	// Belt and braces: whatever the referer ends up being, an IP address must
	// not appear in it, because that header goes to a third party.
	for _, raw := range []string{"https://10.1.2.3/pic.png", "http://192.168.0.1:9999/p.png", "https://[::1]/p.png"} {
		u, err := url.Parse(raw)
		require.NoError(t, err)

		referer := RefererAttempts[2].Build(u)
		assert.NotContains(t, referer, "10.1.2.3", raw)
		assert.NotContains(t, referer, "192.168.0.1", raw)
		assert.NotContains(t, referer, "::1", raw)
	}
}

func TestDoWithRefererLadder_UsesTheSameRequest(t *testing.T) {
	// Headers set by the caller must be identical on every attempt. Stash's own
	// scraper authenticates, so a ladder that rebuilt the request per attempt
	// would fail on attempts two and three -- a regression for the one scraper
	// guaranteed to be affected.
	rec := &refererRecorder{accept: func(string) bool { return false }}
	srv := serve(t, rec)

	var seen []string
	client := &http.Client{Transport: recordingTransport{inner: http.DefaultTransport, onReq: func(r *http.Request) {
		seen = append(seen, r.Header.Get("ApiKey")+"|"+r.Header.Get("User-Agent")+"|"+r.Header.Get("Referer"))
	}}}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/pic.png", nil)
	require.NoError(t, err)
	req.Header.Set("ApiKey", "secret")
	req.Header.Set("User-Agent", "stash-test")

	_, _, err = DoWithRefererLadder(context.Background(), client, req)
	require.Error(t, err)
	require.Len(t, seen, 3)

	for i, got := range seen {
		parts := strings.Split(got, "|")
		assert.Equal(t, "secret", parts[0], "attempt %d lost the ApiKey", i+1)
		assert.Equal(t, "stash-test", parts[1], "attempt %d lost the User-Agent", i+1)
	}
	// And the Referer is the only thing that differs.
	assert.NotEqual(t, seen[0], seen[1], "attempts 1 and 2 must differ in the Referer only")
}

type recordingTransport struct {
	inner http.RoundTripper
	onReq func(*http.Request)
}

func (t recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.onReq != nil {
		t.onReq(req)
	}
	return t.inner.RoundTrip(req)
}
