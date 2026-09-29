package utils

// Tests for the local-image-path matcher behind #5538.
//
// The matcher decides whether a user-supplied image URL is a route this
// instance serves itself, and a URL that matches is read from the database
// without any HTTP request or authentication. That makes it a trust boundary:
// a path that matches when it should not reaches the database as though the
// user had typed a valid id, and a path that fails to match when it should
// breaks the feature.
//
// The tests are therefore split deliberately. The "must match" table is the
// feature. The "must NOT match" table is the security property, and it is
// longer on purpose -- it includes the shapes that a naive "does it contain
// /performer/ and end in /image" check would wave through.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStashImagePathsAreRecognised(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"performer image", "/performer/123/image", "/performer/123/image"},
		{"studio image", "/studio/1/image", "/studio/1/image"},
		{"tag image", "/tag/7/image", "/tag/7/image"},
		{"scene screenshot", "/scene/42/screenshot", "/scene/42/screenshot"},
		{"single digit id", "/performer/1/image", "/performer/1/image"},
		{"large id", "/performer/2147483647/image", "/performer/2147483647/image"},
		{
			"subpath prefix from a reverse proxy is stripped",
			"/stash/performer/123/image",
			"/performer/123/image",
		},
		{
			"deep subpath is stripped",
			"/a/b/c/performer/9/image",
			"/performer/9/image",
		},
		{
			"the LAST route wins when a path contains two",
			"/performer/1/image/performer/2/image",
			"/performer/2/image",
		},
		// The shapes a user actually pastes. The URL builder emits
		// {baseURL}/performer/123/image, and the host is whatever they browse
		// to -- so every one of these must resolve, and none of them may need
		// the host to be recognised.
		{
			"an absolute localhost URL, as the UI reports it",
			"http://localhost:9999/performer/123/image",
			"/performer/123/image",
		},
		{
			"an absolute URL on a domain",
			"https://stash.example.com/performer/123/image",
			"/performer/123/image",
		},
		{
			"an absolute URL behind a reverse proxy subpath",
			"https://example.com/stash/performer/123/image",
			"/performer/123/image",
		},
		{
			"an absolute scene screenshot URL",
			"https://example.com/scene/42/screenshot",
			"/scene/42/screenshot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := isLocalImagePath(tt.in)
			if !ok {
				t.Fatalf("isLocalImagePath(%q) = not-a-stash-image, want %q",
					tt.in, tt.want)
			}
			if got != tt.want {
				t.Errorf("isLocalImagePath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNonStashPathsAreNotResolvedLocally(t *testing.T) {
	// Each of these must fall through to a real HTTP request. A false positive
	// is the dangerous direction: it would read a database record for a path
	// the user never named, and silently serve it.
	tests := []struct {
		name string
		in   string
	}{
		{"an ordinary remote image", "https://example.com/photo.jpg"},
		{"no leading slash", "performer/123/image"},
		{"a bare id", "123"},
		{"empty", ""},
		{"a different endpoint", "/performer/123/alias"},
		{"a different entity", "/movie/123/image"},
		{"a gallery", "/gallery/123/image"},
		{"a path traversal between prefix and id", "/performer/../../etc/passwd/image"},
		{"a nested path between prefix and id", "/performer/1/2/image"},
		{"a non-numeric id", "/performer/abc/image"},
		{"a signed id is not a record id", "/performer/1;drop/image"},
		{"a leading zero id", "/performer/007/image"},
		{"a plus-signed id", "/performer/+1/image"},
		{"a space in the id", "/performer/1 2/image"},
		{"an id that is only a slash", "/performer//image"},
		{"the prefix with no id", "/performer/image"},
		{"a data URI", "data:image/png;base64,iVBORw0KGgo="},
		// The scheme cases below are specifically chosen so that the SCHEME is
		// the only thing rejecting them. A mutation harness caught that the
		// obvious choices -- file:///etc/passwd and javascript:alert(1) --
		// pass for the wrong reason: both have a path that fails the route
		// match anyway, so they would still be rejected with the scheme check
		// deleted entirely. These three have a path that DOES match a stash
		// image route, so only the scheme stops them.
		{"a file URL with a matching path", "file:///performer/123/image"},
		{"a javascript URL with a matching path", "javascript:/performer/123/image"},
		{"an ftp URL with a matching path", "ftp://example.com/performer/123/image"},
		{"a data URL with a matching path", "data:image/png,/performer/123/image"},
		{"a query string", "/performer/1/image?default=true"},
		{"a fragment", "/performer/1/image#x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := isLocalImagePath(tt.in); ok {
				t.Errorf("isLocalImagePath(%q) matched as %q, but must fall "+
					"through to a real HTTP request", tt.in, got)
			}
		})
	}
}

// A URL that is not local must reach the resolver's caller untouched, so it
// can be fetched normally. The flag is the signal and it must be false.
func TestANonLocalURLIsNotHandledLocally(t *testing.T) {
	called := false
	resolve := func(path string) ([]byte, error) {
		called = true
		return []byte("should not happen"), nil
	}

	data, local, err := ReadLocalImage("https://example.com/photo.jpg", resolve)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if local {
		t.Error("a remote URL must report local=false so the caller fetches it")
	}
	if called {
		t.Error("the resolver must not be invoked for a remote URL")
	}
	if data != nil {
		t.Errorf("no data should be returned for a remote URL, got %q", data)
	}
}

// The feature itself: a local URL is served by the resolver, and the path
// handed to it has the base prefix removed so it is a real route.
func TestALocalURLIsServedByTheResolverWithoutHTTP(t *testing.T) {
	var seen string
	resolve := func(path string) ([]byte, error) {
		seen = path
		return []byte("image-bytes"), nil
	}

	data, local, err := ReadLocalImage("http://localhost:9999/performer/123/image", resolve)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !local {
		t.Fatal("a stash image URL must be handled locally")
	}
	if seen != "/performer/123/image" {
		t.Errorf("the resolver received %q, want %q", seen, "/performer/123/image")
	}
	if string(data) != "image-bytes" {
		t.Errorf("got %q, want the resolver's bytes", data)
	}
}

// A resolver error must propagate, and must still be reported as local --
// falling back to HTTP there would re-introduce the very 401 this fixes, and
// would turn a missing record into a confusing network error.
func TestAResolverErrorPropagatesAndStaysLocal(t *testing.T) {
	sentinel := errors.New("no such performer")
	resolve := func(path string) ([]byte, error) { return nil, sentinel }

	_, local, err := ReadLocalImage("/performer/999/image", resolve)
	if !errors.Is(err, sentinel) {
		t.Errorf("got error %v, want the resolver's error", err)
	}
	if !local {
		t.Error("a resolver failure must still report local=true, so the " +
			"caller does not retry over HTTP")
	}
}

// The host is deliberately ignored, so a URL on an unrelated host whose PATH
// is a stash image route resolves to the LOCAL record.
//
// This is a decision, not an oversight, and the test exists so that anyone who
// reads it learns it was chosen. The reasoning, in one line: an instance is
// reachable as localhost, a LAN IP, a domain, a Tailscale address and through
// a reverse proxy, so any host check is either configuration that can go stale
// or a guess -- and a stale one silently reintroduces the 401 this fixes. The
// rejected alternative, attaching a session cookie or API key to the outbound
// request, sends a credential to a host chosen by whoever supplied the URL,
// which is a strictly worse failure than reading a local record.
//
// If this behaviour is ever changed, THIS is the test that will object.
func TestTheHostIsIgnoredAndOnlyThePathDecides(t *testing.T) {
	for _, in := range []string{
		"https://evil.example/performer/123/image",
		"http://192.168.1.50:9999/performer/123/image",
		"https://some.other.host/stash/tag/9/image",
	} {
		got, ok := isLocalImagePath(in)
		if !ok {
			t.Errorf("isLocalImagePath(%q) did not match; the host is "+
				"meant to be ignored so the feature works across every way "+
				"an instance is addressed", in)
			continue
		}
		if !strings.HasPrefix(got, "/") {
			t.Errorf("isLocalImagePath(%q) = %q, want a bare path", in, got)
		}
	}
}

// The end-to-end claim: ProcessImageInput serves a local URL without making
// an HTTP request at all. The test server counts requests, because "no
// request" is the property and asserting only the returned bytes would pass
// even if the code fetched over HTTP and got lucky.
func TestProcessImageInputServesAStashURLWithoutARequest(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte("http-served"))
	}))
	defer srv.Close()

	resolve := func(path string) ([]byte, error) {
		return []byte("locally-served:" + path), nil
	}

	// The URL the GraphQL image_path field hands out, pointing at the test
	// server so that if the code DID go over HTTP it would succeed -- and the
	// byte comparison would still fail, because the two paths differ.
	got, err := ProcessImageInput(context.Background(), srv.URL+"/performer/123/image", resolve)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if hits != 0 {
		t.Errorf("the image server was contacted %d times; a stash image URL "+
			"must be served in-process", hits)
	}
	want := "locally-served:/performer/123/image"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A non-local URL must still work, and must still be fetched over HTTP. The
// fix must not have narrowed the feature into uselessness.
func TestProcessImageInputStillFetchesARemoteURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("remote-image"))
	}))
	defer srv.Close()

	// A resolver that would serve the WRONG thing if it were consulted for
	// this URL, so a false positive is visible in the bytes.
	resolve := func(path string) ([]byte, error) { return []byte("WRONG"), nil }

	got, err := ProcessImageInput(context.Background(), srv.URL+"/photo.jpg", resolve)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "remote-image" {
		t.Errorf("got %q, want the bytes from the remote server", got)
	}
}

// A nil resolver must behave exactly as before the change, because two
// packages call ProcessImageInput with no repository available. If nil were
// mishandled this would panic rather than fall back.
func TestProcessImageInputWithANilResolverFallsBackToHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fallback-ok"))
	}))
	defer srv.Close()

	got, err := ProcessImageInput(context.Background(), srv.URL+"/photo.jpg", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "fallback-ok" {
		t.Errorf("got %q, want the bytes from the remote server", got)
	}
}

// A locally-served HTML page is not an image and must be rejected, exactly
// as a fetched one is. Serving the placeholder-as-image bug in #5538 was a
// silent wrong-value failure; a second one in the other direction is just as
// silent.
func TestLocallyServedHTMLIsRejected(t *testing.T) {
	resolve := func(path string) ([]byte, error) {
		return []byte("<html><body>not an image</body></html>"), nil
	}

	_, err := ProcessImageInput(context.Background(), "/performer/123/image", resolve)
	if err == nil {
		t.Fatal("HTML served by the local resolver must be rejected")
	}
	if !strings.Contains(err.Error(), "content type") {
		t.Errorf("error %q should name the content type as the reason", err)
	}
}

// A base64 data URI must not be treated as a URL. The base64 branch runs
// first and this asserts the ordering, because a data URI does contain a
// slash and could in principle look path-like.
func TestABase64DataURIIsNotTreatedAsALocalPath(t *testing.T) {
	// 1x1 transparent PNG.
	const png = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

	resolve := func(path string) ([]byte, error) {
		t.Errorf("the resolver must not be called for a data URI, got %q", path)
		return nil, nil
	}

	got, err := ProcessImageInput(context.Background(), png, resolve)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) == 0 {
		t.Error("a data URI must decode to image bytes")
	}
	if strings.HasPrefix(string(got), "data:") {
		t.Error("the data URI was stored verbatim instead of being decoded")
	}
}
