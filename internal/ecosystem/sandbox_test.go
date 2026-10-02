package ecosystem

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R058 — "Developer sandbox with sample data" — and §6a.18, "Ecosystem": "a developer sandbox
// with sample data".
//
// WHAT THIS IS, and the decision that shaped it
//
// The spec gives the clause one line and the plan adds nothing, so the shape had to come from
// the requirement's own dependency: R058 depends_on R054 (the public API / SDK surface), which
// is not built. So the sandbox cannot be "a running instance with a REST endpoint" -- there is
// nothing to point a REST client at yet.
//
// What it CAN be, and is: SAMPLE DATA plus the guarantees a developer needs to trust it. The
// sample data is the deliverable; the guarantees are what make it safe to hand to a third party,
// and they are the part that actually needs testing. A sandbox whose only content is a list of
// fake scenes is a fixture; a sandbox that states what its data may and may not be used for is
// a contract.
//
// THE FOUR GUARANTEES, and why each one is a security property rather than a nicety
//
//  1. EVERY SAMPLE IS SYNTHETIC AND LABELLED AS SUCH. A developer who cannot tell sample data
//     from real data will eventually ship a sandbox fixture into production. The marker has to
//     travel WITH the entity, not live in a README, because the README is not what ends up in
//     their database.
//  2. NO REAL CONSENT STATE. §6a.2's consent is per-instance and per-owner. Sample data must not
//     carry a share choice that a developer could mistake for a real one -- and must never be
//     used to decide anything.
//  3. THE SAMPLE SET IS BOUNDED AND STABLE. §6a.16's rate-of-growth reasoning and the existing
//     MaxPublicLimit cap both say a public read is bounded; a sample set that grows without
//     limit is a way to enumerate.
//  4. SAMPLE IDS CANNOT COLLIDE WITH REAL ONES. This is the one that bites: if sample entities
//     use id 42 and a developer's real performer is also 42, a merged fixture is a data
//     corruption bug in the developer's own library.

// THE POSITIVE CONTROL. A suite where every sample is refused would pass every refusal test
// below while the sandbox shipped nothing.
func TestTheSandboxHasUsableSampleData(t *testing.T) {
	sb := NewSandbox()

	sample := sb.Sample()
	require.NotEmpty(t, sample.Entities, "a sandbox with no sample data is not a sandbox")

	assert.Equal(t, SandboxName, sample.Name, "the sample set must say what it is")
	assert.NotEmpty(t, sample.Entities[0].PublicID)
	assert.NotEmpty(t, sample.Entities[0].Title, "a sample with no title cannot exercise a "+
		"client's rendering, which is most of what a developer uses a sandbox for")
}

// Guarantee 1: every sample is synthetic AND labelled in the data, not only in a doc comment.
func TestEverySampleIsMarkedSynthetic(t *testing.T) {
	sb := NewSandbox()
	sample := sb.Sample()

	require.NotEmpty(t, sample.Entities)
	for _, e := range sample.Entities {
		assert.True(t, e.Synthetic,
			"entity %q must carry the synthetic marker in the data itself; a marker that lives "+
				"only in a README does not travel into the developer's database", e.PublicID)
		assert.True(t, strings.Contains(e.Title, SandboxSyntheticPrefix),
			"entity %q's TITLE must also say it is sample data -- the title is what a developer "+
				"sees in their own UI after importing, so that is where the warning has to be", e.PublicID)
	}
	assert.True(t, sample.Synthetic, "and the set itself must be marked, not only its members")
}

// Guarantee 2: no real consent state. A sample carrying a share choice is a sample that can be
// mistaken for a real decision -- and §6a.21's gate keys off exactly that field.
func TestTheSampleSetCarriesNoConsentState(t *testing.T) {
	sb := NewSandbox()
	sample := sb.Sample()

	require.NotEmpty(t, sample.Entities)
	for _, e := range sample.Entities {
		assert.Empty(t, e.Share,
			"sample entity %q must carry no share choice: consent is per-instance and "+
				"per-owner (§6a.2), and a sample that looks opted-in is a sample a developer can "+
				"mistake for a real decision", e.PublicID)
		assert.False(t, e.Published,
			"sample entity %q must not be marked published, or the SDK boundary has a ready-made "+
				"published row to return", e.PublicID)
	}
	// The public surface the sandbox feeds must therefore refuse everything, which is the
	// correct answer: sample data is for exercising a client's SHAPE, never its consent path.
	decision := DecideIndexing("opted-in", sample.Entities[0].Published)
	assert.False(t, decision.Indexable,
		"a sample entity can never be indexable -- the consent gate must not be satisfiable by "+
			"sample data no matter what a caller passes it")
}

// Guarantee 3: bounded. The sample set is a fixed corpus, not a generator, so a developer's
// test expectations do not shift between runs.
func TestTheSampleSetIsBoundedAndStable(t *testing.T) {
	sb := NewSandbox()

	first := sb.Sample()
	second := NewSandbox().Sample()

	require.NotEmpty(t, first.Entities)
	assert.LessOrEqual(t, len(first.Entities), MaxPublicLimit,
		"the sample set must respect the same cap as any public read (§6a.16); a sample set "+
			"larger than one page is not something a client can fetch in one request")

	// Stable across instances: a corpus that differs per call would make every developer test
	// that depends on "the third sample" intermittently wrong.
	require.Equal(t, len(first.Entities), len(second.Entities),
		"the sample set must be the same size every time it is asked for")
	for i := range first.Entities {
		assert.Equal(t, first.Entities[i].PublicID, second.Entities[i].PublicID,
			"sample %d differs between calls; a developer indexing into the corpus by position "+
				"would get a different entity on a different run", i)
		assert.Equal(t, first.Entities[i].Title, second.Entities[i].Title,
			"sample %d's title must be stable too", i)
	}
}

// Guarantee 4: sample ids cannot collide with real ones.
//
// This is the one that bites in practice: a developer who imports sample data into a real
// library and later imports real data with the same ids gets two different performers sharing
// id 42. So sample ids are namespaced, and the prefix is checked rather than assumed.
func TestSampleIDsAreNamespacedAwayFromRealOnes(t *testing.T) {
	sb := NewSandbox()
	sample := sb.Sample()

	require.NotEmpty(t, sample.Entities)
	for _, e := range sample.Entities {
		assert.True(t, strings.HasPrefix(e.PublicID, SandboxIDSamples),
			"sample id %q must be prefixed with %q; an unprefixed 42 is indistinguishable from a "+
				"real entity 42 and collides on import", e.PublicID, SandboxIDSamples)
		// And it must still survive the path escaping used by R060's canonical URLs.
		assert.NotContains(t, e.PublicID, "/",
			"a sample id must be a single path segment, or the canonical URL built from it "+
				"addresses a different resource")
	}
}

// The sandbox must be USABLE against the SDK boundary it exists to exercise. Without this the
// sandbox is a struct nobody can plug in, and R058's dependency on R054 would be unmet in
// spirit: the point is that a developer can point their SDK client at this data.
func TestTheSandboxSatisfiesTheSDKBoundary(t *testing.T) {
	var sdk SDK = NewSandbox()
	require.NotNil(t, sdk)

	got, err := sdk.Query(t.Context(), PublicQuery{Limit: 5})
	require.NoError(t, err, "the sandbox is what an SDK client is pointed at, so it must answer "+
		"the SDK's own query method")
	assert.NotEmpty(t, got, "a sandbox that answers with nothing gives a developer nothing to "+
		"develop against")

	// And it obeys the same bound, because a sample source that ignores MaxPublicLimit teaches
	// a developer that the cap does not exist.
	//
	// THIS ASSERTION WAS MEANINGLESS AND I ONLY FOUND OUT BY MUTATING IT. I first wrote
	// `assert.LessOrEqual(len(all), MaxPublicLimit)` against a 3-entity corpus, which passes
	// whether or not the bound is applied -- 3 is under 100 either way. A mutation removing
	// `q.bounded()` survived, correctly, because there was nothing for it to break.
	//
	// The bound is only OBSERVABLE on a corpus larger than the cap, or as a request for fewer
	// than the corpus holds. The second is what a developer actually does, so it is what is
	// tested here: asking for 2 must return 2, not the whole corpus.
	two, err := sdk.Query(t.Context(), PublicQuery{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, two, 2,
		"a limit smaller than the corpus must be honoured; a sandbox that ignores it teaches a "+
			"developer that paging does not exist")

	// And the cap itself, on a corpus built to exceed it. sandboxCorpusSize exists so this is
	// a real test rather than an arithmetic accident.
	big := NewSandbox().withCount(MaxPublicLimit + 1)
	many, err := big.Query(t.Context(), PublicQuery{Limit: 10_000})
	require.NoError(t, err)
	assert.Len(t, many, MaxPublicLimit,
		"a corpus larger than the cap must be clamped to it, or the cap is not a cap")
}

// The sandbox must not be able to WRITE. §6a.19's write-through rule is the same rule here: a
// sample-data source with a write path is a second way to create an entity that never went
// through governance.
func TestTheSandboxHasNoWritePath(t *testing.T) {
	sb := NewSandbox()

	//
	// THE ALLOWLIST IS POSITIVE, so adding withCount -- a method I added to make the bound
	// testable -- FAILED this test, and correctly. A guard that enumerated a set it did not
	// know about would not have noticed. So the entry is added deliberately, with the reason
	// it is not a write, rather than by widening the check to "anything that does not look
	// like a write".
	for _, m := range methodNamesOf(sb) {
		switch m {
		case "Sample", "Query":
			// the read surface
		case "withCount":
			// returns a NEW sandbox with a different corpus size; it writes no entity and
			// mutates nothing -- the receiver is a value the caller already owns.
		default:
			t.Errorf("Sandbox exposes %q; a sample-data source that can write is a second "+
				"bypass of §6a.19's proposal-only write path", m)
		}
	}
}

// oversizedSandbox observes MaxPublicLimit being applied: the real corpus is three entities,
// which is under the cap, so any clamp on it is untestable.
//
// THIS IS A NEW SANDBOX, NOT A RECOMPILED ONE, and that is the whole point. My first two
// attempts both failed the same way and the failure is worth recording:
//
//  1. An override of Sample on a type EMBEDDING Sandbox -- silently ineffective, because
//     Sandbox.Query calls Sandbox.Sample through the EMBEDDED receiver. It kept returning three
//     rows and the test failed for a reason that had nothing to do with the clamp.
//  2. A standalone type with its own Query -- green, but testing the FIXTURE's clamp, not
//     Sandbox's. A duplicated implementation of the logic under test is worse than no test.
//
// withCount is the third attempt and the one that works: the real Query runs, over a real
// corpus, of a size that exceeds the cap. An embedded override the outer type never calls is a
// fixture that does nothing -- the same shape as a guard enumerating a set it can see.

// The clamp must be a BOUND, not a truncation: a request for fewer rows than the corpus holds
// must get exactly what it asked for.
func TestTheSampleQueryClampsRatherThanTruncates(t *testing.T) {
	big := NewSandbox().withCount(MaxPublicLimit + 1)

	for _, tc := range []struct {
		limit, want int
	}{
		{limit: 0, want: MaxPublicLimit}, // non-positive becomes the cap
		{limit: 5, want: 5},
		{limit: 10_000, want: MaxPublicLimit},
	} {
		got, err := big.Query(t.Context(), PublicQuery{Limit: tc.limit})
		require.NoError(t, err)
		assert.Len(t, got, tc.want, "limit %d must yield %d rows", tc.limit, tc.want)
	}
}

// SURVIVORS I FOUND AND WHAT EACH ONE WAS. Three of the four were real, and the fourth was my
// mutant being wrong rather than the test being weak -- so I checked each instead of assuming.

// (1) A REAL GAP. The consent test above checks SyntheticEntity.Published, which is what Sample()
// returns. It never checked PublicEntity.Published, which is what Query() -- the SDK boundary a
// developer actually calls -- returns. A mutation making every Query row published SURVIVED
// because no test looked there. A consent rule enforced on one representation and not the other
// is not enforced at all, since the second is the one a client reads.
func TestQueryNeverReturnsAPublishedSample(t *testing.T) {
	sb := NewSandbox()
	// A corpus larger than one page, so a clamp or a page boundary cannot be what hides a
	// published row.
	got, err := sb.withCount(MaxPublicLimit+5).Query(t.Context(), PublicQuery{Limit: 10_000})
	require.NoError(t, err)
	require.NotEmpty(t, got)

	for _, e := range got {
		assert.False(t, e.Published,
			"Query returned a PUBLISHED sample entity %q; the SDK boundary is the surface a "+
				"developer's client reads, and a published sample row is one that client can "+
				"index as if it were real published content", e.PublicID)
		// And the synthetic marker must survive the conversion, not just exist on the source
		// entity -- a marker dropped in the projection is a marker the client never sees.
		assert.Contains(t, e.Title, SandboxSyntheticPrefix,
			"Query dropped the synthetic marker from %q's title; the projection is what the "+
				"client displays", e.PublicID)
		assert.Equal(t, SandboxName, e.Origin,
			"sample data came from no instance, so Origin must name the corpus -- pointing at a "+
				"real instance would make 6a.6's (instance, local) pair address something that "+
				"does not exist")
	}
}

// (2) A REAL GAP. Nothing passed a non-zero Offset, so paging was untested: a mutation ignoring
// the offset entirely survived. Paging is exactly what a developer writing against this boundary
// does first, and an ignored offset returns page one forever with no error.
func TestQueryPaginatesByOffset(t *testing.T) {
	sb := NewSandbox()
	all, err := sb.Query(t.Context(), PublicQuery{Limit: 10})
	require.NoError(t, err)
	require.Len(t, all, 3)

	// Each page must be a distinct window, and together they must reconstruct the corpus in
	// order. Reconstructing is the assertion that matters: distinct pages could still overlap
	// or skip, and a developer paging to the end would silently lose or repeat a row.
	var seen []string
	for _, off := range []int{0, 2, 4, 6, 100} {
		page, err := sb.Query(t.Context(), PublicQuery{Limit: 2, Offset: off})
		require.NoError(t, err)
		for _, e := range page {
			seen = append(seen, e.PublicID)
		}
	}
	want := []string{all[0].PublicID, all[1].PublicID, all[2].PublicID}
	assert.Equal(t, want, seen,
		"paging with a 2-row limit must yield every sample exactly once, in order; a skipped "+
			"or repeated row is what a developer sees as data loss or duplication")
}

// A page that starts past the end is an EMPTY page, not an error and not the whole corpus --
// the zero-row case an SDK client hits on its last page.
func TestAnOffsetPastTheEndYieldsAnEmptyPage(t *testing.T) {
	sb := NewSandbox()
	got, err := sb.Query(t.Context(), PublicQuery{Limit: 10, Offset: 99})
	require.NoError(t, err, "paging past the end is normal, not an error")
	assert.Empty(t, got, "an offset past the end must return nothing, not wrap around")
}

// (3) NOT A TEST GAP, A BAD MUTANT. My "corpus unstable" mutation was `n--`, which is
// deterministic: every call returns two rows instead of three, so the set is perfectly STABLE
// and the stability test is right to pass. A mutant that does not model the property it is named
// for proves nothing, and treating its survival as a finding would have been a false positive.
// A genuinely unstable corpus is a real counter, and THAT is killed -- see TestUnstableCorpusIsKilled
// in the mutation log. The stability assertion stands on the random mutant, which passed.

// (4) A CLAIM WITHOUT A TEST. sandbox.go says titles are distinct beyond the named three, with
// the reason "a corpus of identical rows would let a count-only test pass". Nothing asserted it.
// A comment stating an invariant is not the invariant, so here is the assertion.
func TestEverySampleHasADistinctIDAndTitle(t *testing.T) {
	sb := NewSandbox().withCount(MaxPublicLimit + 5)
	entities := sb.Sample().Entities
	require.Greater(t, len(entities), 10, "needs a corpus beyond the named three to be meaningful")

	ids := make(map[string]bool, len(entities))
	titles := make(map[string]bool, len(entities))
	for _, e := range entities {
		require.False(t, ids[e.PublicID], "duplicate sample id %q; a developer indexing into "+
			"the corpus by id would get the wrong entity", e.PublicID)
		require.False(t, titles[e.Title], "duplicate sample title %q", e.Title)
		ids[e.PublicID] = true
		titles[e.Title] = true
	}
	assert.Len(t, ids, len(entities), "every sample id must be unique across a large corpus")
	assert.Len(t, titles, len(entities), "every sample title must be unique across a large corpus")
}
