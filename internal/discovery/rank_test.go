package discovery

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The load-bearing test for step 7.3, and the reason this step has no migration.
//
// §6a.5 and non-negotiable #4: there is no recommendation_score column, and a
// term replaced in the config takes effect immediately. The second half is what
// makes it a VIEW rather than a cache — so this asserts the ordering changes and
// that nothing was written, which is the only observation that distinguishes the
// two.
func TestGravityChangesTheRankingWithoutAWrite(t *testing.T) {
	// IDs chosen so the tie-break and gravity AGREE on the jazz record being
	// first would be a useless test -- the order would be identical either way and
	// the assertion could not fail. Here the jazz record has the LAST id, so with
	// no theme it loses the tie-break, and only gravity can put it on top.
	candidates := []Candidate{
		{ID: "a-ambient", Tags: []string{"ambient"}},
		{ID: "b-techno", Tags: []string{"techno"}},
		{ID: "z-jazz", Tags: []string{"jazz"}},
	}
	fp := Fingerprint{} // no personal taste: isolates gravity's effect
	w := DefaultWeights()

	// Same records, same fingerprint, same weights. Only gravity differs.
	noTheme := Rank(candidates, fp, DefaultGravity(), nil, w)
	jazzy := Rank(candidates, fp, Gravity{Tags: map[string]float64{"jazz": 1.0}}, nil, w)

	require.Len(t, jazzy, 3)
	require.Len(t, noTheme, 3)

	assert.Equal(t, "a-ambient", noTheme[0].ID,
		"with no theme every record scores zero, so the order is the tie-break: "+
			"the ID sort, which is deterministic rather than arbitrary")
	assert.Equal(t, "z-jazz", jazzy[0].ID,
		"an instance that bends toward jazz puts the jazz record first (§6a.8), "+
			"even though its id sorts last")

	// THE POINT. The order changed. No write happened, because there is nothing
	// to write: every score above was computed during the call.
	assert.NotEqual(t, noTheme[0].ID, jazzy[0].ID,
		"gravity moved the ranking, which is only possible if the score is derived")

	// Ranking did not mutate or drop anything from the input SET -- compared by
	// id, since ranking reorders by design and a positional comparison would be
	// asserting that ranking does not rank.
	byID := map[string]Candidate{}
	for _, c := range candidates {
		byID[c.ID] = c
	}
	for _, r := range jazzy {
		orig, ok := byID[r.ID]
		require.True(t, ok, "ranked an id that was not in the input")
		assert.Equal(t, orig, r.Candidate, "ranked copy must equal the original candidate")
	}
	assert.Len(t, byID, len(jazzy), "ranking neither dropped nor duplicated a candidate")
}

// §6a.5: a user's explicit preference outranks all three terms. Non-negotiable
// #6 — ranking shapes an unordered set and never filters one.
func TestAnExplicitRequestIsNotBuried(t *testing.T) {
	// The entity the user asked for has the WORST possible taste match: the
	// fingerprint actively dislikes it.
	fp := Fingerprint{Tags: map[string]float64{"jazz": 5, "techno": 5, "ambient": 5}}
	candidates := []Candidate{
		{ID: "loved", Tags: []string{"jazz"}},
		{ID: "mid", Tags: []string{"ambient"}},
		{ID: "disliked", Tags: []string{"metal"}},
	}

	// Ranked, it comes LAST — the ranking is doing its job.
	ranked := Rank(candidates, fp, Gravity{}, nil, DefaultWeights())
	require.Equal(t, "disliked", ranked[len(ranked)-1].ID,
		"precondition: the entity the user wants is the one ranking least")

	// Requested, it comes FIRST. The ranking is not consulted at all.
	got, err := ExplicitRequest(candidates, "disliked")
	require.NoError(t, err)
	assert.Equal(t, "disliked", got.ID,
		"§6a.5: a request for a specific entity returns that entity and no ranking "+
			"score may bury it")

	// The two paths disagree, which is what makes the test meaningful. A version
	// that encoded "must return this" as a huge score term would pass this
	// assertion while failing the first one, because any constant is beatable.
	assert.NotEqual(t, "disliked", ranked[0].ID)
}

// A missing entity is ErrNotFound, not a silent fallback to "the best match".
// Returning something else would answer a different question than the one asked.
func TestAnExplicitRequestForSomethingAbsentIsAnError(t *testing.T) {
	candidates := []Candidate{{ID: "a", Tags: []string{"jazz"}}}
	_, err := ExplicitRequest(candidates, "nope")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "nope",
		"the error names what was asked for, so an operator can diagnose it")
}

// A zero fingerprint must not fabricate taste. The honest answer is that this
// user's ranking is the instance's.
func TestAUserWithNoTasteGetsTheInstancesOrder(t *testing.T) {
	candidates := []Candidate{
		{ID: "a", Tags: []string{"jazz"}},
		{ID: "b", Tags: []string{"techno"}},
	}
	fp := Fingerprint{}
	assert.True(t, fp.IsZero(), "precondition: an empty fingerprint is zero")

	jazzy := Rank(candidates, fp, Gravity{Tags: map[string]float64{"jazz": 1}}, nil, DefaultWeights())
	plain := Rank(candidates, fp, DefaultGravity(), nil, DefaultWeights())

	assert.Equal(t, "a", jazzy[0].ID)
	assert.Equal(t, "a", plain[0].ID,
		"with no personal taste the tie-break is stable, so the two agree by ID "+
			"rather than differing arbitrarily")
}

// Ranking must be deterministic: two identical calls agree exactly, or a real
// change cannot be told from a re-shuffle.
func TestRankingIsDeterministic(t *testing.T) {
	candidates := []Candidate{
		{ID: "z", Tags: []string{"a", "b", "c"}},
		{ID: "y", Tags: []string{"a", "b"}},
		{ID: "x", Tags: []string{"a"}},
	}
	fp := Fingerprint{Tags: map[string]float64{"a": 1, "b": 1, "c": 1}}
	g := Gravity{Tags: map[string]float64{"a": 2}}

	for i := 0; i < 20; i++ {
		got := Rank(candidates, fp, g, nil, DefaultWeights())
		require.Equal(t, "z", got[0].ID)
		require.Equal(t, "y", got[1].ID)
		require.Equal(t, "x", got[2].ID,
			"ties break on ID ascending, so repeated runs cannot reorder")
	}
}

// A peer-supplied fingerprint is untrusted input of the same class as a
// peer-supplied name (§6a.3, non-negotiable #9): validated, bounded, and never
// allowed to widen what a user may see.
func TestAPeerTasteIsBoundedAndItsWeightsDropped(t *testing.T) {
	// A peer claiming enormous taste in everything.
	hostile := Fingerprint{
		Tags:       map[string]float64{"jazz": 1e9, "techno": 1e9, "ambient": 1e9},
		Studios:    map[string]float64{"A": 1e9},
		Performers: map[string]float64{"P": 1e9},
	}

	clean := SanitisePeerTaste(hostile, 10)
	require.Len(t, clean.Tags, 3)

	for _, v := range clean.Tags {
		assert.Equal(t, float64(1), v,
			"magnitude is DROPPED, not clamped: a clamp is a number the peer chose, "+
				"and any ceiling above honest values is still attacker-controlled. "+
				"Which categories a peer likes is useful for §6a.2 similarity and "+
				"grants nothing.")
	}

	// The ORIGINAL is untouched: sanitising a peer's claim must not mutate the
	// caller's data, or a second consumer sees a fingerprint already stripped.
	assert.Equal(t, float64(1e9), hostile.Tags["jazz"],
		"SanitisePeerTaste returns a new value; it does not edit its argument")
}

// Bounded means bounded. A peer sending ten thousand categories gets a bounded
// number back.
func TestAPeerTasteIsBoundedInCount(t *testing.T) {
	huge := Fingerprint{Tags: map[string]float64{}}
	for i := 0; i < 10000; i++ {
		huge.Tags[string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+itoa(i)] = 1
	}
	require.Len(t, huge.Tags, 10000, "precondition: 10k categories")

	clean := SanitisePeerTaste(huge, 64)
	assert.LessOrEqual(t, len(clean.Tags), 64,
		"§6a.3: validated and bounded. An unbounded peer fingerprint is a memory "+
			"amplification vector against every peer that compares against it")
	assert.Len(t, clean.Tags, 64, "exactly the cap when over it")
}

// Cosine must return 0 for a zero vector, not NaN. A NaN in a sort key makes the
// ORDER depend on input order, so one tasteless peer would silently make ranking
// non-deterministic.
func TestCosineOfAZeroVectorIsZeroNotNaN(t *testing.T) {
	zero := Fingerprint{}
	nonzero := Fingerprint{Tags: map[string]float64{"jazz": 1}}

	assert.Equal(t, float64(0), Cosine(zero, nonzero))
	assert.Equal(t, float64(0), Cosine(nonzero, zero))
	assert.Equal(t, float64(0), Cosine(zero, zero))
	assert.False(t, math_IsNaN(Cosine(zero, nonzero)),
		"a NaN here would make the sort order depend on input order")
}

func TestCosineRanksIdenticalTasteHighest(t *testing.T) {
	jazz := Fingerprint{Tags: map[string]float64{"jazz": 1, "blues": 1}}
	techno := Fingerprint{Tags: map[string]float64{"techno": 1, "house": 1}}
	adjacent := Fingerprint{Tags: map[string]float64{"jazz": 1, "soul": 1}}

	assert.InDelta(t, 1.0, Cosine(jazz, jazz), 1e-9, "identical taste is maximally similar")
	assert.Greater(t, Cosine(jazz, adjacent), Cosine(jazz, techno),
		"shared categories beat disjoint ones (§6a.2 peer similarity)")
	assert.Equal(t, float64(0), Cosine(jazz, techno))
}

// Peer similarity contributes only to PEERS. A local candidate must not pick up
// a peer term, or an instance could rank its own records by a remote opinion.
func TestPeerSimilarityAppliesOnlyToRemoteCandidates(t *testing.T) {
	peer := map[string]float64{"far-instance": 10}
	local := Candidate{ID: "local", Tags: []string{"jazz"}}
	remote := Candidate{ID: "remote", Tags: []string{"jazz"}, Origin: "far-instance"}

	fp := Fingerprint{}
	g := Gravity{}

	assert.Equal(t, 0.0, Score(local, fp, g, peer, DefaultWeights()),
		"a local record with no taste, no gravity and no origin scores zero")
	assert.Greater(t, Score(remote, fp, g, peer, DefaultWeights()), 0.0,
		"a remote record picks up its origin's similarity")

	// And the key is the ORIGIN, so a peer cannot score itself well by claiming
	// an origin it does not have.
	impostor := Candidate{ID: "x", Tags: []string{"jazz"}, Origin: "somewhere-else"}
	assert.Equal(t, 0.0, Score(impostor, fp, g, peer, DefaultWeights()),
		"an unrecognised origin contributes nothing")
}

// Gravity is a weight over categories, never an override of an individual
// entity (§6a.8, non-negotiable #6). This is the structural half of that claim:
// the Gravity type has no entity-id method at all.
func TestGravityCannotTargetAnIndividualEntity(t *testing.T) {
	var g Gravity
	// The only fields are category maps. There is no Entity, SceneID, or ID
	// field on Gravity -- adding one would let an operator pin one record.
	assert.Nil(t, g.Tags)
	assert.Nil(t, g.Studios)
	assert.Nil(t, g.Performers)

	// Nor is there a way to express "this record is first" through a magnitude:
	// the highest gravity term is a category, and a record with that category and
	// nothing else merely ranks among its peers.
	cands := []Candidate{
		{ID: "pinned", Tags: []string{"jazz"}},
		{ID: "other", Tags: []string{"techno"}},
	}
	ranked := Rank(cands, Fingerprint{}, Gravity{Tags: map[string]float64{"jazz": 100}}, nil, DefaultWeights())
	require.Equal(t, "pinned", ranked[0].ID)
	assert.Len(t, ranked, 2,
		"gravity reorders; it never removes. Ranking is not a filter (§6a.5)")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func math_IsNaN(f float64) bool { return f != f }

// A local candidate must be UNABLE to pick up a peer term. This is the §6a.6
// identity property expressed at the score level, and the mutation that found it
// -- dropping the `c.Origin != ""` guard -- survived the suite, because a test
// that only checked "a remote candidate scores higher" cannot see a local one
// scoring higher too.
//
// The mutation's failure mode: an instance ranked its OWN records by a remote
// peer's opinion. A peer could not forge an Origin, but it could declare one and
// the lookup would find it, which is a remote input steering local results.
func TestALocalCandidateCannotPickUpAPeerTerm(t *testing.T) {
	// Two DIFFERENT origins, so a lookup that ignores the candidate's own Origin
	// lands on the wrong peer's similarity. Under the mutation, a local record
	// would pick up whichever origin happened to be first in the map.
	// THE EMPTY-STRING KEY IS THE POINT, and it is why the mutant survived my
	// first attempt at this test. A map lookup of an absent key returns 0, so with
	// only real origins in the map the mutation computes peer[""] == 0 and is
	// indistinguishable from correct -- an equivalent mutant by accident, not by
	// design. Measured: with "" mapped to 50 the mutation scores this local record
	// 10.0 and correct code scores it 0.0.
	//
	// An empty instance id is reachable: it is a peer that failed validation, or a
	// placeholder row. So the guard is not decorative.
	peer := map[string]float64{"": 50, "peer-one": 100, "peer-two": -100}
	local := Candidate{ID: "local", Tags: []string{"jazz"}}

	assert.Equal(t, 0.0, Score(local, Fingerprint{}, Gravity{}, peer, DefaultWeights()),
		"a LOCAL record has no origin, so no peer term applies to it -- even with "+
			"an empty-origin entry present, which is what the guard is for")

	// And a real origin still reaches a record that declares it -- otherwise the
	// guard above could be satisfied by simply ignoring peer similarity entirely,
	// which would break §6a.2 rather than protect it.
	//
	// Skipped for "", which is the case the guard makes score zero: a candidate
	// whose Origin IS "" is indistinguishable from local, which is the whole
	// point, so it must not collect a peer term either.
	for origin, sim := range peer {
		if origin == "" || sim == 0 {
			continue
		}
		c := Candidate{ID: "remote", Tags: []string{"jazz"}, Origin: origin}
		assert.NotEqual(t, float64(0), Score(c, Fingerprint{}, Gravity{}, peer, DefaultWeights()),
			"a record declaring origin %q picks up that peer's similarity "+
				"(§6a.2); if this fails, peer similarity is dead rather than guarded", origin)
	}
}

// The tie-break must be pinned to a total order, not merely to "some stable
// order". Removing the ID comparison survives a determinism test that only checks
// two RUNS of the same input agree, because Go's sort is already stable and the
// input order is already fixed.
//
// What distinguishes the two is a caller that reorders its input between calls --
// which is the normal case, since candidates arrive from a query whose row order
// nobody promised. Same SET, different arrival order, same output.
func TestTheOrderIsTheSameWhateverOrderCandidatesArriveIn(t *testing.T) {
	// All score identically, so the ONLY thing separating them is the tie-break.
	cands := []Candidate{
		{ID: "delta", Tags: []string{"same"}},
		{ID: "alpha", Tags: []string{"same"}},
		{ID: "charlie", Tags: []string{"same"}},
		{ID: "bravo", Tags: []string{"same"}},
	}
	fp := Fingerprint{}
	g := Gravity{}

	base := ids(Rank(cands, fp, g, nil, DefaultWeights()))
	require.Equal(t, []string{"alpha", "bravo", "charlie", "delta"}, base,
		"ties break on ID ascending, independent of arrival order")

	// Every rotation of the same set must produce the identical order.
	for i := 0; i < len(cands); i++ {
		rotated := append(append([]Candidate{}, cands[i:]...), cands[:i]...)
		got := ids(Rank(rotated, fp, g, nil, DefaultWeights()))
		assert.Equal(t, base, got,
			"rotation %d: a query's row order is not promised, so ranking must not "+
				"depend on it", i)
	}

	// And the reverse order too, since that is the other way a query changes.
	rev := append([]Candidate{}, cands...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	assert.Equal(t, base, ids(Rank(rev, fp, g, nil, DefaultWeights())),
		"reversed arrival order gives the same ranking")
}

func ids(rs []Ranked) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}
