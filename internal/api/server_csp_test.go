package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/plugin"
)

// setPageSecurityHeaders builds the CSP for a page, and nothing tests it.
//
// The function is security-relevant -- it decides what a plugin's page is
// allowed to load -- and it had zero coverage when #7196 added `media-src` to
// it. A one-line addition to a header builder is exactly the change a test
// exists for, because the failure mode is invisible: a directive that drops a
// plugin's origin makes that plugin's media silently fail to load, with nothing
// in any log.
//
// config.GetInstance() PANICS when uninitialised, and setPageSecurityHeaders
// calls it unconditionally, so every test here initialises the empty instance
// once. The tests do not depend on its contents -- they assert on what the
// PLUGIN contributes.

func init() {
	// InitializeEmpty sets the package singleton. Doing it in init means a test
	// in this file cannot forget it and panic instead of failing an assertion;
	// the first version of this file called it per-test and the ordering
	// dependency was invisible until two tests ran in either order.
	config.InitializeEmpty()
}

// cspOf runs the header builder and returns the media-src directive's sources.
func cspOf(t *testing.T, plugins []*plugin.Plugin) string {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	setPageSecurityHeaders(w, r, plugins)
	return w.Header().Get("Content-Security-Policy")
}

// directive pulls one directive's value out of a CSP header. Written by hand
// rather than with a CSP parser: there is no such dependency, and the thing
// under test is a string this function built, so splitting on ";" is enough.
func directive(csp, name string) string {
	for _, part := range strings.Split(csp, ";") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) >= 2 && fields[0] == name {
			return strings.Join(fields[1:], " ")
		}
	}
	return ""
}

func withMedia(media ...string) *plugin.Plugin {
	return &plugin.Plugin{
		Enabled: true,
		UI: plugin.PluginUI{
			CSP: plugin.PluginCSP{MediaSrc: media},
		},
	}
}

// The change #7196 makes: a plugin's media-src reaches the header.
func TestPluginMediaSrcReachesTheHeader(t *testing.T) {
	csp := cspOf(t, []*plugin.Plugin{
		withMedia("https://cdn.example.com"),
	})

	got := directive(csp, "media-src")
	for _, want := range []string{"blob:", "'self'", "https://cdn.example.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("media-src %q does not contain %q\nfull header: %s", got, want, csp)
		}
	}
}

// The base directive must survive. Without this, a test that only checked for
// the plugin's origin would pass even if media-src had been replaced wholesale.
func TestMediaSrcKeepsItsDefaults(t *testing.T) {
	got := directive(cspOf(t, nil), "media-src")
	if got != "blob: 'self'" {
		t.Errorf("media-src with no plugins = %q, want %q", got, "blob: 'self'")
	}
}

// Every source must arrive, in order -- a slice silently truncated to one
// element is the obvious way to break this.
func TestAllMediaSrcEntriesAreIncluded(t *testing.T) {
	csp := cspOf(t, []*plugin.Plugin{
		withMedia("https://a.example.com", "https://b.example.com", "https://c.example.com"),
	})
	got := directive(csp, "media-src")
	for _, want := range []string{"https://a.example.com", "https://b.example.com", "https://c.example.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("media-src %q is missing %q", got, want)
		}
	}
	if n := strings.Count(got, "https://"); n != 3 {
		t.Errorf("media-src carries %d plugin origins, want 3: %q", n, got)
	}
}

// Several plugins each contribute. This is where the implementation's SHAPE
// differs from its three siblings -- mediaSrc is a string built with `+=` inside
// the loop, while the others are slices joined once at the end -- so the
// accumulation is the thing that can be wrong.
func TestMediaSrcAccumulatesAcrossPlugins(t *testing.T) {
	got := directive(cspOf(t, []*plugin.Plugin{
		withMedia("https://one.example.com"),
		withMedia("https://two.example.com"),
	}), "media-src")

	for _, want := range []string{"https://one.example.com", "https://two.example.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("media-src %q is missing %q", got, want)
		}
	}
}

// A DISABLED plugin contributes nothing. Every other field is gated on
// `plugin.Enabled` and this one sits inside the same loop, so the gate is worth
// pinning: a disabled plugin's origin in the header is a hole.
func TestDisabledPluginContributesNoMediaSrc(t *testing.T) {
	disabled := withMedia("https://evil.example.com")
	disabled.Enabled = false

	got := directive(cspOf(t, []*plugin.Plugin{disabled}), "media-src")
	if strings.Contains(got, "evil.example.com") {
		t.Errorf("a DISABLED plugin's media-src reached the header: %q", got)
	}
}

// A plugin that configures no media-src must not disturb the defaults, and must
// not leave the directive empty. `mediaSrc += " " + strings.Join(nil, " ")` adds
// a bare space per such plugin, which is harmless to a browser but is worth
// seeing rather than assuming.
func TestPluginWithoutMediaSrcLeavesTheDefaultIntact(t *testing.T) {
	got := directive(cspOf(t, []*plugin.Plugin{
		withMedia(),
		withMedia("https://only.example.com"),
	}), "media-src")

	if !strings.Contains(got, "blob:") || !strings.Contains(got, "'self'") {
		t.Errorf("media-src lost its defaults: %q", got)
	}
	if !strings.Contains(got, "https://only.example.com") {
		t.Errorf("media-src lost the one configured origin: %q", got)
	}
}

// The siblings must be untouched. media-src is built differently from the other
// three, and a change to that string could disturb the shared header assembly.
func TestOtherDirectivesAreUnaffected(t *testing.T) {
	csp := cspOf(t, []*plugin.Plugin{
		withMedia("https://cdn.example.com"),
	})

	for name, mustContain := range map[string]string{
		"default-src": "data:",
		"connect-src": "wss:",
		"script-src":  "'self'",
		"style-src":   "'unsafe-inline'",
		"object-src":  "'none'",
		"child-src":   "'none'",
		"form-action": "'self'",
		"media-src":   "blob:",
		"worker-src":  "blob:",
	} {
		got := directive(csp, name)
		if got == "" {
			t.Errorf("directive %q is missing or empty from:\n%s", name, csp)
			continue
		}
		if mustContain != "" && !strings.Contains(got, mustContain) {
			t.Errorf("directive %q = %q, want it to contain %q", name, got, mustContain)
		}
	}
}

// The base header still holds for a page that is not the playground, since that
// branch appends to three of the directives.
func TestMediaSrcOnThePlaygroundPage(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, playgroundEndpoint, nil)
	setPageSecurityHeaders(w, r, []*plugin.Plugin{withMedia("https://cdn.example.com")})
	csp := w.Header().Get("Content-Security-Policy")

	if !strings.Contains(directive(csp, "media-src"), "https://cdn.example.com") {
		t.Errorf("the playground lost the plugin's media-src: %s", csp)
	}
	// The playground's own CDN entries must survive the plugin loop.
	if !strings.Contains(directive(csp, "script-src"), "cdn.jsdelivr.net") {
		t.Errorf("the playground lost its script-src CDN entry: %s", csp)
	}
}
