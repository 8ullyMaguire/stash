package rpc

import (
	"errors"
	"strings"
	"testing"

	"github.com/stashapp/stash-plugin-p2pdownloader/internal/ed2k"
)

// # WHAT THIS FILE IS FOR
//
// The plugin advertises `ed2k` in knownSchemes and in the manifest's
// description, and for a while it did so with nothing on the other side: an
// ed2k link went through LocatorSchemeOf, got a consent proposal, was granted,
// and arrived at the transfer stub. So a user could hand the plugin a link
// whose NAME was `../../etc/passwd` and the whole path would agree to it.
//
// The gate cannot catch that. Core's gate decides whether a locator may be
// stored against an object; it has no opinion about a filename, and
// `ed2k://|file|../../etc/passwd|1|<hash>|` is a well-formed locator by every
// test a consent gate could apply. The ed2k parser is the only layer that sees
// the name, so the parser is what runs, and it runs BEFORE the gate.
//
// # WHY "BEFORE" IS THE WHOLE ASSERTION
//
// Asserting that a hostile ed2k link produces an error would pass even if the
// parser ran AFTER a granting gate, because the stub refuses everything
// anyway. So each test here also asserts on `core.asked` — that core was never
// asked. An operator's trust is spent on a proposal, and a proposal about a
// link we already know is hostile is trust spent for nothing.

const (
	// A well-formed ed2k file link.
	goodED2K = "ed2k://|file|video.mkv|1024|" +
		"0123456789abcdef0123456789abcdef|"
	// A folder link, whose hash covers a FILE LIST rather than a file.
	goodED2KFolder = "ed2k://|folder|album|2048|" +
		"0123456789abcdef0123456789abcdef|"
)

// TestThePluginRefusesAnEscapingED2KNameBeforeAskingCore is the ordering test.
//
// Every name here is a way of writing outside the download root, and every one
// is well formed as a locator. The assertion is twofold and both halves matter:
// the download is refused, AND core was never asked about it.
func TestThePluginRefusesAnEscapingED2KNameBeforeAskingCore(t *testing.T) {
	for _, c := range []struct{ name, locator string }{
		{"a parent walk",
			"ed2k://|file|../../etc/passwd|1024|" +
				"0123456789abcdef0123456789abcdef|"},
		{"a bare traversal", "ed2k://|file|..|1024|" +
			"0123456789abcdef0123456789abcdef|"},
		{"an absolute path", "ed2k://|file|/etc/passwd|1024|" +
			"0123456789abcdef0123456789abcdef|"},
		{"a Windows drive letter", `ed2k://|file|C:\Windows\x|1024|` +
			"0123456789abcdef0123456789abcdef|"},
		{"a UNC path", `ed2k://|file|\\server\share\x|1024|` +
			"0123456789abcdef0123456789abcdef|"},
		{"a traversal in a FOLDER label",
			"ed2k://|folder|../album|2048|" +
				"0123456789abcdef0123456789abcdef|"},
		{"a traversal in upper case", `ed2k://|file|..\..\WINDOWS|1024|` +
			"0123456789abcdef0123456789abcdef|"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// A GRANTING core, so the only thing that can refuse is the parser.
			// A refusing core would make this test pass for the wrong reason.
			core := grantingProposer()

			_, err := runDownload(t, core, ArgsMap{"url": c.locator})
			if err == nil {
				t.Fatalf("the link %q was accepted. Its name escapes the "+
					"download root and a downloader that honours it writes "+
					"outside the directory the operator configured", c.locator)
			}
			if !errors.Is(err, ed2k.ErrEscapingName) {
				t.Errorf("err = %v, want it to wrap ed2k.ErrEscapingName, "+
					"which is the only one of the parser's errors that means "+
					"an ATTACK rather than a broken database", err)
			}
			if len(core.asked) != 0 {
				t.Errorf("core was asked about %d proposal(s) for a link "+
					"that is hostile on its face: %+v. The refusal has to "+
					"come BEFORE the gate, because a proposal spends an "+
					"operator's trust and this one has an obvious answer",
					len(core.asked), core.asked)
			}
		})
	}
}

// TestAMalformedED2KLinkIsRefusedBeforeAskingCore: the same ordering for the
// other two parser errors. A link with a 31-character hash names a different
// file than the one intended, so it is a mistake rather than an attack — and it
// is still not worth an operator's proposal.
func TestAMalformedED2KLinkIsRefusedBeforeAskingCore(t *testing.T) {
	for _, c := range []struct{ name, locator string }{
		{"a hash of the wrong length", "ed2k://|file|video.mkv|1024|" +
			"0123456789abcdef0123456789abcde|"},
		{"a hash that is not hex", "ed2k://|file|video.mkv|1024|" +
			"0123456789abcdef0123456789abcdeg|"},
		{"a zero size", "ed2k://|file|video.mkv|0|" +
			"0123456789abcdef0123456789abcdef|"},
		{"a non-numeric size", "ed2k://|file|video.mkv|lots|" +
			"0123456789abcdef0123456789abcdef|"},
		{"a name with a pipe, which is ambiguous",
			"ed2k://|file|a|b|1024|0123456789abcdef0123456789abcdef|"},
		{"an unknown kind", "ed2k://|server|1.2.3.4|4661|" +
			"0123456789abcdef0123456789abcdef|"},
	} {
		t.Run(c.name, func(t *testing.T) {
			core := grantingProposer()
			_, err := runDownload(t, core, ArgsMap{"url": c.locator})
			if err == nil {
				t.Fatalf("the link %q was accepted", c.locator)
			}
			if !errors.Is(err, ed2k.ErrMalformed) {
				t.Errorf("err = %v, want it to wrap ed2k.ErrMalformed", err)
			}
			if len(core.asked) != 0 {
				t.Errorf("core was asked about %d proposal(s) for a "+
					"malformed link: %+v", len(core.asked), core.asked)
			}
		})
	}
}

// TestAWellFormedED2KLinkReachesTheGate is the CONTROL, and without it the two
// tests above would be satisfied by a plugin that refuses every ed2k link.
//
// A well-formed link must get past the parser and be PROPOSED — and then
// refused, because the stub refuses, which is the correct ending and is not
// what this row is about. What this row is about is that core was asked.
func TestAWellFormedED2KLinkReachesTheGate(t *testing.T) {
	for _, locator := range []string{goodED2K, goodED2KFolder} {
		t.Run(locator, func(t *testing.T) {
			core := refusingProposer("the object is denied")
			_, err := runDownload(t, core, ArgsMap{"url": locator})
			if !errors.Is(err, ErrRefused) {
				t.Errorf("err = %v, want ErrRefused — a well-formed ed2k link "+
					"must get past the parser and be decided by core", err)
			}
			if len(core.asked) != 1 {
				t.Fatalf("core was asked about %d proposals, want exactly 1. "+
					"A plugin that refuses every ed2k link passes the two "+
					"tests above and is useless: %+v", len(core.asked),
					core.asked)
			}
			if got := core.asked[0].Locator; got != locator {
				t.Errorf("the proposal carried %q, want %q. The locator the "+
					"gate decided about has to be the one the user handed us",
					got, locator)
			}
		})
	}
}

// TestANonED2KLocatorIsUnaffected: the ed2k branch must not touch a magnet or
// an http URL, or the plugin would refuse ordinary downloads.
func TestANonED2KLocatorIsUnaffected(t *testing.T) {
	for _, locator := range []string{
		"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		"https://example.invalid/file.mkv",
	} {
		t.Run(locator, func(t *testing.T) {
			core := refusingProposer("the object is denied")
			_, err := runDownload(t, core, ArgsMap{"url": locator})
			if !errors.Is(err, ErrRefused) {
				t.Errorf("err = %v, want ErrRefused. The ed2k validation "+
					"must not change what happens to any other scheme", err)
			}
			if len(core.asked) != 1 {
				t.Errorf("core was asked about %d proposals, want 1", len(core.asked))
			}
		})
	}
}

// TestTheED2KSchemeIsRefusedByNameWhenTheLinkIsNotOne: an ed2k-schemed string
// that is not a link at all is refused by the SCHEME check, and the error says
// so rather than reporting a parse failure. Both are refusals; the difference is
// which layer said so, and a log line that names the wrong layer sends the next
// reader to the wrong file.
func TestTheED2KSchemeIsRefusedByNameWhenTheLinkIsNotOne(t *testing.T) {
	core := grantingProposer()
	_, err := runDownload(t, core, ArgsMap{"url": "ed2k://|list|a|1|" +
		"0123456789abcdef0123456789abcdef|"})
	if err == nil {
		t.Fatal("a |list| link was accepted")
	}
	if !strings.Contains(err.Error(), "neither") {
		t.Errorf("err = %q, want it to say the kind is neither file nor "+
			"folder — a folder link's hash covers a FILE LIST, and treating "+
			"one as the other sends a caller looking for a file that does "+
			"not exist", err)
	}
	if len(core.asked) != 0 {
		t.Errorf("core was asked about %d proposals", len(core.asked))
	}
}

// TestTheED2KPrefixIsRecognisedTheSameWayTwice keeps isED2KLocator and
// LocatorSchemeOf from drifting apart.
//
// They are two copies of one decision — a locator's scheme — and a copy that
// falls behind is worse than the duplicate it replaced, because it fails SILENTLY.
// The first version of the pre-gate ed2k validation called LocatorSchemeOf
// directly, which was correct; deduplicating it left a hand-written prefix test
// behind, and a wrong answer there means the ed2k parser stops running and
// nothing says so.
//
// So the agreement is asserted over a table that deliberately includes the
// cases where the two COULD disagree: mixed case, surrounding whitespace, a
// bare `ed2k:` with no slashes, and schemes that merely start with the same
// letters.
func TestTheED2KPrefixIsRecognisedTheSameWayTwice(t *testing.T) {
	for _, c := range []struct {
		name, locator string
		// want is what BOTH must say: true when LocatorSchemeOf classifies the
		// locator as ed2k, and isED2KLocator must agree.
		want bool
	}{
		{"a plain link", goodED2K, true},
		{"a folder link", goodED2KFolder, true},
		{"upper case", "ED2K://|file|video.mkv|1024|" +
			"0123456789abcdef0123456789abcdef|", true},
		{"mixed case", "Ed2K://|file|video.mkv|1024|" +
			"0123456789abcdef0123456789abcdef|", true},
		{"surrounding whitespace", "  " + goodED2K + "\n", true},
		{"a magnet", "magnet:?xt=urn:btih:0123456789abcdef", false},
		{"an http URL", "https://example.invalid/x.torrent", false},
		{"a schemeless path", "example.invalid/x.torrent", false},
		{"a refused scheme", "file:///etc/passwd", false},
		// The interesting one: `ed2k:foo` has no slashes, so it is NOT a link,
		// and LocatorSchemeOf hands it to url.Parse, which reads the scheme as
		// "ed2k" and then refuses it as unrecognised. Both must decline.
		{"ed2k with no slashes", "ed2k:foo", false},
		// And a locator that merely CONTAINS the prefix must not be claimed.
		{"https with ed2k in the path",
			"https://example.invalid/ed2k://|file|x|1|" +
				"0123456789abcdef0123456789abcdef|", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			scheme, err := LocatorSchemeOf(c.locator)
			got := isED2KLocator(c.locator)

			if scheme == SchemeED2K && err == nil && !got {
				t.Errorf("LocatorSchemeOf calls %q ed2k but isED2KLocator does "+
					"not, so the pre-gate ed2k validation would skip it. A "+
					"hostile ed2k link would reach the consent gate unparsed",
					c.locator)
			}
			if got && !(scheme == SchemeED2K && err == nil) {
				t.Errorf("isED2KLocator claims %q is ed2k but LocatorSchemeOf "+
					"said scheme=%q err=%v. A false positive runs a http URL "+
					"through the ed2k parser and refuses it for being http",
					c.locator, scheme, err)
			}
			if got != c.want {
				t.Errorf("isED2KLocator(%q) = %v, want %v", c.locator, got, c.want)
			}
		})
	}
}

// TestTheDownloadStubStillReportsTheTransferIsUnimplemented: the ed2k
// validation runs before the gate, so a reader could reasonably wonder
// whether it now REPLACES the stub's error. It does not: a granted, well-formed
// ed2k link still arrives at the transfer stub, because there is still no
// ed2k transport. That is the honest state, and this row says so.
func TestTheDownloadStubStillReportsTheTransferIsUnimplemented(t *testing.T) {
	core := grantingProposer()
	_, err := runDownload(t, core, ArgsMap{"url": goodED2K})
	if !errors.Is(err, ErrTransferNotImplemented) {
		t.Errorf("err = %v, want ErrTransferNotImplemented. A granted, "+
			"well-formed ed2k link passes validation and then reaches the "+
			"transfer stub, because the ed2k TRANSPORT does not exist yet — "+
			"and the error has to say that rather than implying a download "+
			"was attempted", err)
	}
	if len(core.asked) != 1 {
		t.Errorf("core was asked about %d proposals, want 1", len(core.asked))
	}
}
