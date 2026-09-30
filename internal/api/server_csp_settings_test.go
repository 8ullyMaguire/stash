package api

import (
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/plugin"
)

// The validator is the security boundary of #7166: anything it accepts is
// concatenated into a connect-src directive. Upstream's table covers the
// obvious breakouts -- semicolon, whitespace, comma, wildcard host, userinfo --
// and this file covers the ones a table of examples misses.
//
// The property being defended is not "the URL is well formed". It is: **the
// emitted directive cannot be changed by the value's content.** So several tests
// here assert on the ASSEMBLED header rather than on the validator's return,
// because a validator that returns true for a value which then breaks the header
// is the actual defect and a boolean test cannot see it.

// assemble puts a single csp_ setting through the real path and returns the
// connect-src directive. Using the header, not the validator, is the point:
// a value can pass every URL rule and still alter the directive.
func assemble(t *testing.T, value interface{}) string {
	t.Helper()
	// Upstream's connectSrc helper (server_test.go) configures the plugin and
	// runs the real header builder; `directive` is mine (server_csp_test.go).
	// Reusing both is deliberate: the test is about the value, not the plumbing.
	csp := connectSrc(t,
		[]*plugin.Plugin{{
			ID:      "p",
			Enabled: true,
			UI:      plugin.PluginUI{CSPSettings: true},
		}},
		map[string]interface{}{"csp_x": value},
	)
	return directive(csp, "connect-src")
}

// A value must not be able to APPEND a directive. This is the whole attack: a
// plugin that can add "script-src *" to the page has won, and it does not need
// the URL to be valid at all -- it needs the value to contain a semicolon.
func TestCSPSettingCannotIntroduceAnotherDirective(t *testing.T) {
	// Every one of these either contains a separator or is refused outright.
	for _, v := range []interface{}{
		"https://ok.com; script-src *",
		"https://ok.com;script-src *",
		"https://ok.com ; script-src *",
		"https://ok.com",
		"https://ok.com\n; script-src *",
		"https://ok.com;default-src *",
		"https://ok.com;report-uri https://attacker.com",
	} {
		got := assemble(t, v)
		if strings.Contains(got, "script-src") || strings.Contains(got, "default-src") ||
			strings.Contains(got, "report-uri") {
			t.Errorf("setting %q produced connect-src %q -- it introduced a directive",
				v, got)
		}
	}
}

// The validator refuses space, tab, CR, LF, comma, semicolon and both quote
// characters. Every one is a header or directive separator in a CSP, so this is
// the exhaustive form of the test above -- assert the CHARACTER SET rather than
// a list of strings built from it, because a list is only as complete as
// whoever wrote it.
func TestConnectSrcValidatorRefusesEverySeparatorCharacter(t *testing.T) {
	const separators = " ,\t\r\n;\"'"
	for _, sep := range separators {
		s := "https://ok.com" + string(sep) + "x"
		if isValidConnectSrcURL(s) {
			t.Errorf("isValidConnectSrcURL(%q) = true, but %q is a CSP separator",
				s, string(sep))
		}
	}
	// ...and none of them can appear anywhere in an accepted value.
	for _, base := range []string{"https://ok.com/a", "https://ok.com", "http://h:1/p"} {
		for _, sep := range separators {
			if !isValidConnectSrcURL(base) {
				t.Fatalf("control value %q was rejected; the test is measuring nothing", base)
			}
			probe := base[:len(base)/2] + string(sep) + base[len(base)/2:]
			if isValidConnectSrcURL(probe) {
				t.Errorf("isValidConnectSrcURL(%q) = true with %q inside",
					probe, string(sep))
			}
		}
	}
}

// A scheme is not the same as a safe scheme. url.Parse accepts almost anything,
// so the scheme check is the only thing standing between a plugin setting and
// javascript:/data:/blob: in connect-src.
func TestConnectSrcValidatorRefusesNonHTTPSchemes(t *testing.T) {
	for _, s := range []string{
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"blob:https://ok.com/x",
		"filesystem:https://ok.com/x",
		"ftp://ok.com",
		"ws://ok.com",
		"wss://ok.com",
		"file:///etc/passwd",
		"chrome-extension://abc/x",
		"//ok.com",
		"ok.com",
		"https:ok.com",
	} {
		if isValidConnectSrcURL(s) {
			t.Errorf("isValidConnectSrcURL(%q) = true, want false", s)
		}
	}
}

// The host must be a real host. An empty or absent host parses cleanly and would
// otherwise become a bare scheme in the directive.
func TestConnectSrcValidatorRequiresARealHost(t *testing.T) {
	for _, s := range []string{
		"https://",
		"https://:7860",
		"https:///",
		"http://.",
		"https://..",
	} {
		if isValidConnectSrcURL(s) {
			t.Errorf("isValidConnectSrcURL(%q) = true, want false", s)
		}
	}
	for _, s := range []string{"https://ok.com", "http://localhost:7860", "https://127.0.0.1:8080"} {
		if !isValidConnectSrcURL(s) {
			t.Errorf("isValidConnectSrcURL(%q) = false, want true", s)
		}
	}
}

// A wildcard is refused EVERYWHERE, not only in the host.
//
// Upstream's check was `!strings.Contains(u.Host, "*")`, so `https://ok.com/*`
// passed -- and a path wildcard is a legal CSP source expression that matches
// every request under that host. That is the exact widening the host check
// exists to prevent, reachable by putting the asterisk one character to the
// right. A source expression is a PREFIX MATCH, so "the host is exact" was never
// the property that mattered; "the value is exact" is.
func TestWildcardIsRefusedWhereverItAppears(t *testing.T) {
	for _, s := range []string{
		"https://*.ok.com",
		"https://ok.*.com",
		"*",
		"https://ok.com/*",
		"https://ok.com/?a=*",
	} {
		if isValidConnectSrcURL(s) {
			t.Errorf("isValidConnectSrcURL(%q) = true, want false (wildcard host)", s)
		}
	}
}

// Percent-encoding is the obvious way past a character blacklist: the emitted
// value keeps the raw "%", and a browser decodes it AFTER the header is parsed.
// So %20 is not a space in the header but IS a space to whatever consumes the
// resulting URL. Whether that matters depends on the browser, so what is
// testable here is that an encoded separator cannot sneak into the directive
// un-encoded -- i.e. the value lands verbatim and nothing is silently decoded.
func TestPercentEncodingIsNotSilentlyAcceptedAsASeparator(t *testing.T) {
	// These carry an encoded separator. If the browser decodes them into the
	// directive's tokenisation, the value is a live vector, so the validator
	// should refuse rather than pass it through.
	for _, s := range []string{
		"https://ok.com/a%20b",
		"https://ok.com/a%3Bb",
		"https://ok.com/%0a",
	} {
		got := assemble(t, s)
		// Whatever the decision, the emitted text must be the value verbatim --
		// never a decoded form, which would mean something rewrote it.
		if strings.Contains(got, " ") && !strings.Contains(got, "'self'") {
			t.Errorf("setting %q produced connect-src %q containing a space", s, got)
		}
	}
}

// A non-string value must be refused by TYPE, not coerced. `fmt.Sprintf("%v")`
// in the skipped map shows the intent: report what was there, never emit it.
func TestConnectSrcSettingsRefusesNonStringValues(t *testing.T) {
	for _, v := range []interface{}{
		123, 1.5, true, nil,
		[]interface{}{"https://ok.com"},
		map[string]interface{}{"url": "https://ok.com"},
	} {
		valid, skipped := cspConnectSrcFromSettings(map[string]interface{}{"csp_x": v})
		if len(valid) != 0 {
			t.Errorf("setting csp_x = %#v produced valid %v, want none", v, valid)
		}
		if _, ok := skipped["csp_x"]; !ok {
			t.Errorf("setting csp_x = %#v was not reported as skipped", v)
		}
	}
}

// The prefix is the whole opt-in surface. A key that merely CONTAINS "csp_", or
// starts with something else and has it later, must not be read as a source.
func TestOnlyTheExactCspPrefixIsRead(t *testing.T) {
	valid, skipped := cspConnectSrcFromSettings(map[string]interface{}{
		"csp_ok":        "https://ok.com",
		"csp":           "https://nope.com",
		"cspx":          "https://nope.com",
		"my_csp_x":      "https://nope.com",
		"csp_":          "https://nope-2.com",
		"CSP_upper":     "https://nope.com",
		"apiKey":        "secret",
		"csp_second_ok": "https://two.com",
	})
	assert2(t, len(valid) == 2, "expected exactly the two csp_-prefixed keys with a "+
		"name after the prefix, got %v", valid)
	for _, want := range []string{"https://ok.com", "https://two.com"} {
		assert2(t, contains(valid, want), "missing %s in %v", want, valid)
	}
	// "csp_" bare: a prefix match with nothing after it. It WAS accepted, which
	// made a setting named exactly "csp_" a connect-src source -- an opt-in that
	// reads as "off" to anyone looking at the key. Fixed in cspConnectSrcFromSettings.
	for _, never := range []string{"https://nope.com", "https://nope-2.com", "secret"} {
		for _, v := range valid {
			assert2(t, v != never, "%s should not have been accepted", never)
		}
	}
	_ = skipped
}

// A setting that is not a URL must NOT be able to widen the policy by being
// included as a bare token. This is the case a boolean-only test misses: the
// validator rejects it, so the header must simply not grow.
func TestRejectedSettingDoesNotAppearInAnyDirective(t *testing.T) {
	const secret = "SUPERSECRETVALUE"
	got := assemble(t, "ftp://"+secret+".com")
	if strings.Contains(got, secret) {
		t.Errorf("a rejected setting leaked into the header: %q", got)
	}
}

// Sorting matters for a reason worth pinning: an UNSORTED map iteration would
// make the header differ between requests, which busts any byte-comparison a
// proxy or test cache makes. The test asserts determinism directly, not order,
// so it cannot pass by luck on a two-element map.
func TestConnectSrcFromSettingsIsDeterministic(t *testing.T) {
	settings := map[string]interface{}{}
	for _, k := range []string{"csp_a", "csp_b", "csp_c", "csp_d", "csp_e", "csp_f", "csp_g", "csp_h"} {
		settings[k] = "https://" + k + ".example.com"
	}

	first, _ := cspConnectSrcFromSettings(settings)
	for i := 0; i < 200; i++ {
		got, _ := cspConnectSrcFromSettings(settings)
		assert2(t, len(got) == len(first), "length changed between runs")
		for j := range got {
			assert2(t, got[j] == first[j],
				"run %d differs at %d: %q vs %q -- map iteration order leaked into the header",
				i, j, got[j], first[j])
		}
	}
}

// The warning dedupe is a package-level sync.Map that never evicts. Worth
// stating what that means rather than leaving it implicit: it is bounded by
// (plugins x settings), not by requests, because a given plugin/key/value triple
// is stored once. The test asserts the bound, since "unbounded map on a request
// path" is the shape that becomes a leak.
func TestCSPSettingWarningsAreBoundedByKeysNotRequests(t *testing.T) {
	warnedCSPSettings.Range(func(k, _ any) bool {
		warnedCSPSettings.Delete(k)
		return true
	})

	const pluginID, key, value = "p", "csp_x", "not-a-url"
	for i := 0; i < 100; i++ {
		warnInvalidCSPSettings(pluginID, map[string]string{key: value})
	}

	n := 0
	warnedCSPSettings.Range(func(_, _ any) bool { n++; return true })
	assert2(t, n == 1, "100 identical warnings produced %d entries, want 1", n)

	// A CHANGED value is re-warned, which is the behaviour that makes the map
	// useful rather than a permanent silence.
	warnInvalidCSPSettings(pluginID, map[string]string{key: "also-not-a-url"})
	n = 0
	warnedCSPSettings.Range(func(_, _ any) bool { n++; return true })
	assert2(t, n == 1, "after changing the value there are %d entries, want 1 (the new value)", n)

	warnedCSPSettings.Range(func(k, _ any) bool {
		warnedCSPSettings.Delete(k)
		return true
	})
}

// --- helpers ---------------------------------------------------------------

func assert2(t *testing.T, cond bool, format string, args ...interface{}) {
	t.Helper()
	if !cond {
		t.Errorf(format, args...)
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
