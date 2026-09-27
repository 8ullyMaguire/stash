package rpc

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// # WHY THE SCHEME SET IS DUPLICATED, AND WHY IT NEEDS A TEST
//
// The plugin cannot import core's LocatorScheme. The plugin is a separate Go
// module, and importing the core to share five string constants would put a
// `require` and a `replace` in the core's go.mod — which is precisely the thing
// M5 exists to prevent. So the set is written out twice, once in
// internal/collab/locator_scheme.go and once here.
//
// Duplication across a boundary nobody can type-check is a liability, so it gets
// a test. The two directions that matter are both unsafe, and neither is
// symmetric:
//
//   - A scheme CORE accepts and the PLUGIN refuses: the plugin rejects locators
//     core would have stored. Annoying, and safe.
//   - A scheme the PLUGIN accepts and CORE refuses: the plugin fetches something
//     the gate would not have permitted. That is the dangerous direction, and
//     it is the one this test exists to catch.
//
// So the two lists are compared as sets, and any difference at all fails —
// including the safe direction, because a divergence is a bug either way and
// the safe one is a bug that will be "fixed" in the unsafe direction by someone
// who assumes the plugin is the one that is wrong.

// coreLocatorSchemes is the list in internal/collab/locator_scheme.go, read from
// the source rather than restated here.
//
// Read from the file at test time rather than copied, because a COPIED list is
// exactly the thing that goes stale: it keeps passing after core adds a scheme,
// which is the failure it exists to catch. Reading the source means the test
// fails the day core changes, and fails with core's actual names in the message.
func coreLocatorSchemes(t *testing.T) []string {
	t.Helper()

	body := readCoreFile(t, "internal/collab/locator_scheme.go")

	// The one table both sides keep their schemes in.
	start := strings.Index(body, "var locatorSchemes = []LocatorScheme{")
	if start < 0 {
		t.Fatal("core's locatorSchemes table is gone. If it was renamed, point " +
			"this test at the new one rather than deleting the test — a duplicate " +
			"list with nothing checking it against the original is the failure " +
			"this test exists to prevent")
	}
	end := strings.Index(body[start:], "}")
	if end < 0 {
		t.Fatal("core's locatorSchemes table has no closing brace")
	}

	// Matched with a regex rather than by splitting on spaces, because core's
	// table is written across lines:
	//
	//	var locatorSchemes = []LocatorScheme{
	//		SchemeMagnet, SchemeTorrent, SchemeHTTP, SchemeHTTPS, SchemeED2K,
	//	}
	//
	// A per-line "starts with Scheme" check finds only the first entry, because
	// the rest share a line with it. That is a parser that silently undercounts
	// and then reports a scheme MISMATCH for a list that is in fact identical --
	// which is the worst failure mode for a test whose job is to be believed.
	// `\b` on the left, because "LocatorSchemes" contains "Schemes" and a
	// prefix-only match reads the table's own name as a sixth scheme. The
	// "Scheme" prefix is stripped so the two sides are compared as VALUES
	// ("magnet") rather than as Go identifiers ("SchemeMagnet") -- otherwise the
	// comparison is really two of them plus a naming convention, and a rename
	// in core reads as a scheme change.
	const prefix = "Scheme"
	var word = regexp.MustCompile(`\bScheme([A-Za-z0-9]+)`)

	var schemes []string
	for _, m := range word.FindAllStringSubmatch(body[start:start+end], -1) {
		schemes = append(schemes, strings.ToLower(m[1]))
	}
	return schemes
}

func without(list []string, drop string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != drop {
			out = append(out, v)
		}
	}
	return out
}

func readCoreFile(t *testing.T, rel string) string {
	t.Helper()

	// The test runs in the package directory (plugins/p2pdownloader/internal/rpc),
	// and core is four levels up: rpc -> internal -> p2pdownloader -> plugins.
	path := "../../../../" + rel
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading core's %s: %v\n\n"+
			"  The two scheme lists have to agree and this is what checks it. "+
			"Reading the SOURCE is deliberate: a list copied into this file "+
			"would keep passing after core changed, which is the failure it "+
			"exists to catch", rel, err)
	}
	return string(body)
}

// TestThePluginAndCoreAgreeOnLocatorSchemes is the guard on the duplication.
func TestThePluginAndCoreAgreeOnLocatorSchemes(t *testing.T) {
	core := coreLocatorSchemes(t)
	plugin := LocatorSchemeNames()

	// `torrent` is excluded explicitly, and NOT silently.
	//
	// A .torrent is a metainfo file FETCHED over http or https; `torrent:` is not
	// a scheme any handler speaks. Core keeps the name in its vocabulary because
	// there it describes a KIND of locator. The plugin has no such kind — it
	// either transfers or it does not — so it refuses the string, and listing it
	// as handled would be an error message that lies.
	//
	// This exclusion is the ONLY difference between the two sets, and it is
	// written out rather than filtered quietly because a test that hides its own
	// exceptions is a test whose next failure will be read as a bug in the code
	// rather than in the exception.
	if got := without(core, "torrent"); len(got) != len(core)-1 {
		t.Fatalf("core's list no longer contains a `torrent` entry, so the "+
			"exclusion this test applies is stale. Read core's table, decide "+
			"whether the plugin should follow, and update this exclusion: %v", core)
	}
	core = without(core, "torrent")

	if len(core) == 0 {
		t.Fatal("no schemes were read from core's source. An empty list compared " +
			"against a non-empty one would fail for the wrong reason, and an " +
			"empty list is always a parsing bug in this test")
	}

	sort.Strings(core)
	sorted := append([]string(nil), plugin...)
	sort.Strings(sorted)

	if len(core) != len(sorted) {
		t.Errorf("core handles %d schemes (%s) and the plugin handles %d (%s).\n\n"+
			"  A scheme core accepts and the plugin refuses is merely annoying. A "+
			"scheme the PLUGIN accepts and core refuses means the plugin fetches "+
			"something the consent gate would not have permitted, which is the "+
			"dangerous direction and the one this test exists to catch.",
			len(core), strings.Join(core, ", "),
			len(sorted), strings.Join(sorted, ", "))
		return
	}

	for i := range core {
		if core[i] != sorted[i] {
			t.Errorf("scheme %d: core has %q, the plugin has %q", i, core[i], sorted[i])
		}
	}
}

// TestTheDangerousDirectionIsRefusedEvenIfCoreWouldAcceptIt is the one that
// protects against a future edit that widens the plugin's list.
//
// If someone adds `file` to the plugin's set to "match a new core behaviour", the
// comparison above passes and the downloader reads local files. So the refusals
// are asserted directly, independently of what core's list happens to contain.
func TestTheDangerousDirectionIsRefusedEvenIfCoreWouldAcceptIt(t *testing.T) {
	for _, dangerous := range []struct{ locator, why string }{
		{"file:///etc/passwd", "a downloader with a file:// handler reads any file the user can read"},
		{"file://localhost/etc/shadow", "and file:// is a request to read, not to fetch"},
		{"ftp://example.invalid/x", "this build speaks no FTP"},
		{"gopher://example.invalid/", "gopher has been dead since 2009"},
		{"data:text/plain,hello", "a data: URL is inline content, not a locator to anything"},
		{"javascript:alert(1)", "and it is not a protocol handler at all"},
	} {
		scheme, err := LocatorSchemeOf(dangerous.locator)
		if err == nil {
			t.Errorf("%q was accepted as %q. %s", dangerous.locator, scheme, dangerous.why)
		}
	}
}

// TestTheSafeDirectionsAreAccepted, so the refusal list above cannot be satisfied
// by a function that refuses everything.
func TestTheSafeDirectionsAreAccepted(t *testing.T) {
	for _, ok := range []struct{ locator, want string }{
		{"magnet:?xt=urn:btih:0123456789abcdef", "magnet"},
		{"MAGNET:?xt=urn:btih:abc", "magnet"},     // scheme case is not significant
		{"  magnet:?xt=urn:btih:abc  ", "magnet"}, // surrounding space is not
		{"ed2k://|file|x.mkv|1234|0123456789abcdef0123456789abcdef01234567|/", "ed2k"},
		{"https://example.invalid/file.mkv", "https"},
		{"http://example.invalid/file.torrent", "http"},
		{"HTTPS://example.invalid/x", "https"},
	} {
		got, err := LocatorSchemeOf(ok.locator)
		if err != nil {
			t.Errorf("%q was refused: %v", ok.locator, err)
			continue
		}
		if string(got) != ok.want {
			t.Errorf("%q parsed as %q, expected %q", ok.locator, got, ok.want)
		}
	}
}

// TestASchemelessLocatorIsRefusedRatherThanGuessed is the transport decision that
// a downloader must not make on the user's behalf.
func TestASchemelessLocatorIsRefusedRatherThanGuessed(t *testing.T) {
	for _, bare := range []string{
		"example.invalid/file.torrent",
		"/var/lib/stash/file.mkv",
		"127.0.0.1:8080/x",
	} {
		if _, err := LocatorSchemeOf(bare); err == nil {
			t.Errorf("%q was accepted. Assuming https for a bare host is a "+
				"transport decision made by a guess, and guessing the transport "+
				"is how content gets fetched over the wrong one", bare)
		}
	}
}

// TestHTTPXIsNotHTTPS: the check is on the scheme the PARSER reports, not on a
// prefix, so a lookalike scheme is not mistaken for a real one.
func TestHTTPXIsNotHTTPS(t *testing.T) {
	if scheme, err := LocatorSchemeOf("httpx://example.invalid/x"); err == nil {
		t.Errorf("httpx:// was accepted as %q. The check must be on the parsed "+
			"scheme, not a prefix -- otherwise every lookalike scheme is a "+
			"transport confusion waiting to happen", scheme)
	}
}
