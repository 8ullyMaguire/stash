package ecosystem

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
)

// R060 — "SEO public pages for every entity" — and §6a.21, "SEO surfaces, and the line they
// may not cross".
//
// The consent gate (DecideIndexing) is already built and tested. What was missing is
// everything downstream of the decision: turning an entity into a page, and indexing it. This
// file covers that, and it is deliberately built so the gate cannot be bypassed by
// construction — the indexer takes a DECISION, not a share choice, so there is no path that
// renders a page without asking first.

// THE POSITIVE CONTROL. Without it, a suite in which nothing is ever indexable would pass
// every refusal test below while the feature did nothing at all.
func TestAPublishedOptedInEntityIsRenderedAndIndexed(t *testing.T) {
	page, decision := IndexEntity(PublicEntity{
		PublicID:  "scene-42",
		Title:     "A Public Title",
		Published: true,
	}, collab.ChoiceOptedIn, publicBaseURL)

	assert.True(t, decision.Indexable, "precondition: this entity must be indexable")
	require.False(t, page.Empty(), "an indexable entity must produce a page")

	// A page a crawler cannot find is not a page. The canonical link is what makes it
	// discoverable, and it must be absolute -- a relative canonical resolves against whatever
	// the fetch guessed, which for a crawler is often a staging host.
	assert.Contains(t, page.Canonical, publicBaseURL+"/",
		"the canonical URL must be absolute and rooted at the public base")
	assert.Contains(t, page.Canonical, "scene-42")

	// The page is indexable, so it must SAY so. A page with no robots directive is indexed by
	// default, which is right; what must not happen is a page that is emitted for an entity
	// and then hidden, because the two decisions are made in different places.
	// Asserted on the BODY, not only on page.Robots. A mutation run that changed the
	// <meta name="robots"> in the document while leaving the struct field alone SURVIVED this
	// assertion -- the field is metadata about the page, not the page, and the bytes a crawler
	// reads are the body. Both are checked, and the body is the one that matters.
	assert.NotContains(t, strings.ToLower(page.Body), "noindex",
		"an indexable page must not carry a noindex directive in the SERVED BODY")
	assert.NotContains(t, strings.ToLower(page.Robots), "noindex",
		"nor in the struct field, so a caller reading Page.Robots cannot be misled")

	assert.Equal(t, "A Public Title", page.Title, "the page's visible title is the entity's")
}

// The gate is the whole point of §6a.21, so it is tested per-branch and not only via the
// aggregate. The spec names the exact reasoning this stops: "it's metadata, not content" is
// not a reason to index.
func TestNoPageIsRenderedForAnUnindexableEntity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		share     collab.ShareChoice
		published bool
		why       string
	}{
		{"opted out", collab.ChoiceOptedOut, true,
			"an opted-out entity is neither indexed nor reachable; #7 is a hard stop"},
		{"not published", collab.ChoiceOptedIn, false,
			"the entity exists locally but its owner has not published it"},
		{"unpublished and opted out", collab.ChoiceOptedOut, false,
			"the stricter of the two refusals still refuses"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, decision := IndexEntity(PublicEntity{
				PublicID:  "scene-42",
				Title:     "Secret",
				Published: tc.published,
			}, tc.share, publicBaseURL)

			assert.False(t, decision.Indexable, tc.why)
			assert.True(t, page.Empty(), "no page may be produced for an unindexable entity -- "+
				"a rendered shell is still a page a crawler can fetch")
			assert.NotEmpty(t, decision.Reason,
				"a refusal must explain itself: an operator who cannot see why something is "+
					"not indexed will index it by hand")
		})
	}
}

// The rendered page must not become a side channel for the entity's existence.
//
// This is the failure mode the whole section guards: a page that says "not found" for an
// opted-out entity and "not found" for one that never existed is safe, but a page that renders
// an empty shell with a distinct status leaks the difference, and that difference is exactly
// what consent is protecting. Hence NoPage and the "does not exist" page must be the same
// artefact.
func TestARefusedEntityIsIndistinguishableFromOneThatNeverExisted(t *testing.T) {
	refused, decision := IndexEntity(PublicEntity{
		PublicID: "scene-42", Title: "Secret", Published: true,
	}, collab.ChoiceOptedOut, publicBaseURL)

	nonexistent, noneDecision := IndexEntity(PublicEntity{
		PublicID: "scene-9999", Title: "Never Existed", Published: false,
	}, collab.ChoiceOptedOut, publicBaseURL)

	require.False(t, decision.Indexable)
	require.False(t, noneDecision.Indexable)
	assert.Equal(t, refused, nonexistent,
		"an opted-out entity and a nonexistent one must render identically, or the difference "+
			"in what a client can observe IS the metadata consent protects")
	assert.NotContains(t, refused.Body, "Secret",
		"the refused entity's title must not appear anywhere in what is served -- not even "+
			"in a field, since Page is what gets serialised into a response")
}

// The indexer must ask, every time.
//
// The alternative — rendering first and checking later — is what makes a consent gate
// decorative, and it is invisible in review because both statements still exist in the same
// function. So the gate is asserted by construction: the ONLY entry point takes a share
// choice, and there is no second one that does not.
func TestTheOnlyEntryPointRequiresTheShareChoice(t *testing.T) {
	// Called with a share choice, the gate refuses a private entity. The point is that this
	// is reachable without any other API, so a caller cannot skip it.
	_, decision := IndexEntity(PublicEntity{
		PublicID: "scene-42", Title: "T", Published: true,
	}, collab.ChoiceOptedOut, publicBaseURL)
	assert.False(t, decision.Indexable)

	// And the shared predicate is the same one DecideIndexing uses, so a change to consent
	// semantics cannot leave the page path behind. This is the property that keeps the two
	// surfaces from drifting, which is the failure §6a.21 is written against.
	assert.Equal(t,
		DecideIndexing(collab.ChoiceOptedOut, true).Indexable,
		decision.Indexable,
		"the page path and the consent gate must agree, or SEO indexing becomes a wider "+
			"surface than the public read endpoint")
}

// Escaping is the whole safety property of a generated page. A title containing markup is the
// difference between a page and an injection into this instance's origin.
func TestTheTitleIsEscapedInEveryPlaceItAppears(t *testing.T) {
	hostile := `<script>alert(1)</script> & "quotes"`

	page, decision := IndexEntity(PublicEntity{
		PublicID: "scene-42", Title: hostile, Published: true,
	}, collab.ChoiceOptedIn, publicBaseURL)

	require.True(t, decision.Indexable)
	// Asserted against Body, not the struct: the struct's Title field deliberately holds the
	// RAW title, because a caller building a sitemap wants what the owner wrote, not escaped
	// markup. Asserting on the struct would have compared the raw title against escaped
	// expectations and failed for a reason that has nothing to do with the escaping.
	assert.NotContains(t, page.Body, "<script>", "a title must never be able to inject a tag")
	assert.Contains(t, page.Body, "&lt;script&gt;",
		"the title's markup must be escaped rather than stripped -- stripping changes the "+
			"title, escaping preserves what the owner actually wrote")
	assert.Contains(t, page.Body, "&amp;", "an ampersand in a title must be escaped too")
	assert.Contains(t, page.Body, "&#34;", "a double quote must be escaped, or it breaks out "+
		"of an HTML attribute")
	// The raw title is still available for non-HTML consumers.
	assert.Equal(t, hostile, page.Title,
		"Page.Title must carry the title as written, since a sitemap or OpenGraph card wants "+
			"the owner's text rather than escaped markup")
}

// A crawler-facing page needs a description and a title element, not just visible text.
func TestThePageCarriesTheMetadataACrawlerNeeds(t *testing.T) {
	page, decision := IndexEntity(PublicEntity{
		PublicID: "scene-42", Title: "A Public Title", Published: true,
	}, collab.ChoiceOptedIn, publicBaseURL)

	require.True(t, decision.Indexable)
	assert.Contains(t, page.Body, "<title>", "a page without a title element is not indexable")
	assert.Contains(t, page.Body, `name="description"`,
		"a description is what a search result shows; without it the page indexes to a blank line")
	assert.Contains(t, page.Body, `rel="canonical"`, "a canonical link is what resolves duplicates")
}

// A configured base URL ending in "/" must not produce "example.com//scene-42".
//
// This case is here because `publicBaseURL` has no trailing slash, which left the
// TrimRight untested: a mutation removing it survived, because with a slash-free base the
// result is identical. The doubled slash is not cosmetic -- it produces a canonical that does
// not match the URL the page is actually served at, and a canonical that disagrees with the
// served URL is how a page ends up indexed under a URL that 404s.
func TestABaseURLWithATrailingSlashDoesNotDoubleTheSeparator(t *testing.T) {
	page, decision := IndexEntity(PublicEntity{
		PublicID: "scene-42", Title: "T", Published: true,
	}, collab.ChoiceOptedIn, "https://public.example/")

	require.True(t, decision.Indexable)
	assert.Equal(t, "https://public.example/scene-42", page.Canonical,
		"a trailing slash on the configured base must be absorbed, not doubled")
	assert.NotContains(t, page.Canonical, "//scene",
		"the doubled separator is what makes the canonical disagree with the served URL")
}

// A PublicID is a path SEGMENT, so a "/" in it must be escaped rather than treated as a
// separator.
//
// Without escaping, an id like "a/../../b" produces a canonical that resolves outside its own
// entity -- the same class of bug as the XSS one, in the other direction: a canonical pointing
// at someone else's page. There was no test with a hostile PublicID at all, so the escaping
// helper was unexercised and a mutation removing it survived.
func TestAPublicIDCannotEscapeItsOwnPathSegment(t *testing.T) {
	page, decision := IndexEntity(PublicEntity{
		PublicID: "a/../../b", Title: "T", Published: true,
	}, collab.ChoiceOptedIn, publicBaseURL)

	require.True(t, decision.Indexable)
	assert.NotContains(t, page.Canonical, "/../",
		"a traversal in a PublicID must be escaped, or the canonical resolves to another entity")
	assert.Equal(t, "https://public.example/a%2F..%2F..%2Fb", page.Canonical,
		"the whole id is one segment, so its separators are encoded")
}

// The canonical URL is inside an HTML ATTRIBUTE, so it needs HTML escaping as well as path
// escaping -- two different contexts, and conflating them is the trap.
//
// A mutation that added a raw `&` to the href survived the traversal test above: that test's
// id contained only "/" and ".", neither of which HTML-escapes to anything. An id carrying "&"
// or a quote is what actually breaks out of href="...", so that is what is tested here.
//
// This is the same value passing through both escapers, which is why a single-escaper
// implementation cannot satisfy both tests.
func TestTheCanonicalURLIsSafeInAnHTMLAttribute(t *testing.T) {
	// An id with both a separator and an HTML-significant character.
	page, decision := IndexEntity(PublicEntity{
		PublicID: `x"&<y`, Title: "T", Published: true,
	}, collab.ChoiceOptedIn, publicBaseURL)

	require.True(t, decision.Indexable)

	// WHAT I EXPECTED, AND WHY IT WAS WRONG: I asserted the href would contain
	// `x%22&amp;%3Cy` -- percent-encoded quote, then an HTML-escaped ampersand. It does not,
	// and it should not: the PATH escaper already encodes "&" and "<" to %26 and %3C, so by
	// the time html.EscapeString runs there is nothing HTML-significant left to escape. The
	// expectation was written as though the two escapers did different jobs on the same
	// characters, when in fact the stricter one runs first and wins outright.
	//
	// The assertion that matters is the property, not the exact bytes: nothing in the value can
	// terminate the attribute. Over-specifying the encoding made the test fail for a reason
	// that was not a bug.
	assert.Contains(t, page.Body, `href="https://public.example/x%22%26%3Cy"`,
		"every character that could break out of href=\"...\" is percent-encoded by the path "+
			"escaper, so the HTML escaper has nothing left to do")

	// Independently: the raw quote must not appear unescaped inside the attribute.
	hrefStart := strings.Index(page.Body, `href="`) + len(`href="`)
	hrefEnd := strings.Index(page.Body[hrefStart:], `"`)
	require.Positive(t, hrefEnd, "precondition: the canonical link must be present")
	href := page.Body[hrefStart : hrefStart+hrefEnd]
	assert.NotContains(t, href, `"`, "the href value must not contain a raw quote")
	assert.NotContains(t, href, "<", "the href value must not contain a raw angle bracket")
}

// A literal "%" in a PublicID must be escaped, or the canonical becomes ambiguous with our
// OWN escaping.
//
// This is the same class of bug as the traversal one, one level down: we emit %XX for every
// unsafe byte, so a raw % in the input could be read back as the start of an escape sequence
// by anything that decodes the canonical. "a%2Fb" as an id and "a/b" arriving through a
// re-encode would produce the same canonical for two different entities.
//
// Nothing tested this: every hostile id so far used "/" or quote characters, none of which
// touch the escaping machinery itself. The mutation that allowed % through unescaped survived
// because of that gap, not because it was equivalent.
func TestAPercentSignInAPublicIDIsEscaped(t *testing.T) {
	page, decision := IndexEntity(PublicEntity{
		PublicID: "a%2Fb", Title: "T", Published: true,
	}, collab.ChoiceOptedIn, publicBaseURL)

	require.True(t, decision.Indexable)
	assert.Contains(t, page.Canonical, "a%252Fb",
		"a literal percent must be encoded as %25, or it is indistinguishable from an escape "+
			"this function itself emits")
	assert.NotContains(t, strings.TrimPrefix(page.Canonical, "https://public.example/"), "a%2Fb",
		"the raw %2F must not survive: it is exactly what a decoded 'a/b' would produce, so two "+
			"different entities would share a canonical")
}

// A ":" in a PublicID must be escaped.
//
// Allowing it produces the classic scheme-injection shape: an id of "javascript:alert(1)"
// reaches a crawler or a consumer of the canonical as something that looks like a URL with a
// scheme rather than a path segment. It also collides with the port separator, so an id of
// "x:80" and one of "x" could resolve identically.
//
// This is the third member of the same family -- "/" then "%" then ":" -- and it is the last
// one that matters, because ":" is the character that makes a path segment stop being one.
func TestAColonInAPublicIDIsEscaped(t *testing.T) {
	page, decision := IndexEntity(PublicEntity{
		PublicID: "javascript:alert(1)", Title: "T", Published: true,
	}, collab.ChoiceOptedIn, publicBaseURL)

	require.True(t, decision.Indexable)
	assert.NotContains(t, page.Canonical, "javascript:",
		"a colon in an id must not survive: it makes the value parse as a scheme, which is the "+
			"shape a scheme-injection payload needs")
	// The parentheses are escaped as well (%28 / %29). That is correct and I initially wrote the
	// expectation with bare parens, which failed -- worth noting because "only the character I
	// was thinking about should change" is a common wrong assumption about an allowlist: the
	// safe set is small on purpose, and everything outside it moves.
	assert.Contains(t, page.Canonical, "javascript%3Aalert%281%29",
		"the colon is percent-encoded (and so are the parens), so the whole id stays one "+
			"path segment")
}

// The rest of the path-terminating characters, as one table.
//
// "/" "%" and ":" each got their own test because each was a separate survivor found in a
// separate mutation round. "?" and "#" are the same bug with a different consequence: both
// terminate the PATH on the client, so an id containing one produces a canonical whose
// effective path is a PREFIX of what was encoded -- "scene?x" is fetched as the scene, and the
// "?x" is a query string nothing will ever see.
//
// Tabulating them makes the family visible: the safe set is the RFC 3986 unreserved set and
// nothing else, and every addition to it is a decision rather than an oversight.
//
// TWO OF MY FOUR EXPECTED STRINGS WERE WRONG on the first run: I wrote "scene%3Fx=1" and
// "a%5B::1%5D", reasoning that only the character under test should change. The implementation
// escapes "=" and ":" as well, correctly. That is the third time in this file I have assumed
// an allowlist leaves neighbouring characters alone, and it is the same assumption each time --
// the safe set is small on purpose, so "the one I am thinking about" is never the only one that
// moves.
func TestPathTerminatingCharactersInAPublicIDAreEscaped(t *testing.T) {
	for _, tc := range []struct{ name, id, escaped string }{
		{"question mark starts a query", "scene?x=1", "scene%3Fx%3D1"},
		{"hash starts a fragment", "scene#frag", "scene%23frag"},
		{"at sign is a userinfo separator", "user@host", "user%40host"},
		{"square brackets are IPv6 literal syntax", "a[::1]", "a%5B%3A%3A1%5D"},
		// "+" and space are in the table for a specific reason: both are the form-encoding
		// traps. url.QueryEscape renders a space as "+", which in a path means a LITERAL plus,
		// so an implementation that used QueryEscape would turn "a b" and "a+b" into the same
		// canonical. Each was its own survivor in the mutation run.
		{"plus is the form-encoding of a space", "a+b", "a%2Bb"},
		{"space must not become a plus", "a b", "a%20b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, decision := IndexEntity(PublicEntity{
				PublicID: tc.id, Title: "T", Published: true,
			}, collab.ChoiceOptedIn, publicBaseURL)

			require.True(t, decision.Indexable)
			assert.Contains(t, page.Canonical, tc.escaped,
				"the character must be escaped or the canonical addresses a different resource")
		})
	}
}

// THE PROPERTY, not a list of examples.
//
// The four survivors in the final mutation run were the quote, backslash, ">" and "*" -- all
// real gaps, all fixed by adding one more row to a table. That is the wrong shape of fix: the
// next character to be added to the safe set would need a seventh table entry, and the mutant
// would survive again. So this asserts the RULE instead -- exactly the RFC 3986 unreserved
// set survives, everything else is percent-encoded -- which kills every member of the family at
// once, including characters nobody thought to enumerate.
//
// The previous version of this test did assert the rule, as a switch listing safe characters,
// which is why the mutants had to widen THAT switch. Asserting on the encoding of a fixed
// character set instead means a widened allowlist is caught for any input, not just the ones
// already tabulated.
func TestOnlyTheRFC3986UnreservedSetSurvivesEscaping(t *testing.T) {
	const unreserved = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.~"

	for i := 0; i < 256; i++ {
		c := byte(i)
		wantSafe := strings.IndexByte(unreserved, c) >= 0
		gotSafe := isPathSegmentSafe(c)
		assert.Equal(t, wantSafe, gotSafe,
			"byte %d (%q) safe=%v, want %v -- the safe set must be exactly the RFC 3986 "+
				"unreserved set", c, c, gotSafe, wantSafe)
	}

	// And spot-check the consequence for a character from each failing family above, so this
	// test is about behaviour and not only about the helper's return value.
	for _, tc := range []struct{ id, escaped string }{
		{"a'b", "a%27b"},
		{"a\\b", "a%5Cb"},
		{"a>b", "a%3Eb"},
		{"a*b", "a%2Ab"},
	} {
		page, _ := IndexEntity(PublicEntity{
			PublicID: tc.id, Title: "T", Published: true,
		}, collab.ChoiceOptedIn, publicBaseURL)
		assert.Contains(t, page.Canonical, tc.escaped,
			"%q must be encoded as %s", tc.id, tc.escaped)
	}
}
