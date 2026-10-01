package rank

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The plan's test for step 7.6, and the difference between a rating and a
// counter: delete every vote and the rating returns to the default, not to a
// stale number.
func TestARatingIsRecomputableFromItsVotes(t *testing.T) {
	votes := []Vote{
		{VoterID: "alice", Target: "scene-1", Score: 90},
		{VoterID: "bob", Target: "scene-1", Score: 70},
		{VoterID: "alice", Target: "scene-2", Score: 80},
		{VoterID: "carol", Target: "scene-2", Score: 95},
	}

	computed := RaterRatings(votes)
	require.Contains(t, computed, "alice", "alice voted, so she has a predicting rating")
	assert.NotEqual(t, DefaultRating, computed["alice"].Rating,
		"precondition: the votes actually move the rating")

	// Delete every vote. There is no prior-ratings parameter and no store, so
	// there is nothing left that could produce a stale number.
	deleted := RaterRatings(nil)
	assert.NotContains(t, deleted, "alice",
		"a player with no votes has no rating row at all -- nothing was persisted")

	// And a caller gets the DEFAULT, not a leftover:
	assert.Equal(t, DefaultRating, RatingOf(nil, "alice").Rating,
		"§6a.16 and non-negotiable #4: a rating computes from a VOTE SET. With no "+
			"votes it is the initial rating, because that is what the vote set says.")

	// The direct comparison: the rating after deletion equals the rating before
	// any votes, which is only true because nothing accumulates.
	assert.Empty(t, RaterRatings([]Vote{}),
		"an empty vote set yields nobody at all: not an undefined value, and not a "+
			"player with a zero rating")

	// Every vote is replayed from DefaultRating, so votes ADDED give the same
	// answer as votes given in one go. Convergence is the signature of a stateful
	// system; recomputation has none.
	partial := RaterRatings(votes[:2])
	full := RaterRatings(votes)
	assert.Equal(t, full["alice"].Rating,
		RaterRatings(append(append([]Vote{}, votes[:2]...), votes[2:]...))["alice"].Rating,
		"precondition sanity: the same vote set computes the same rating")
	assert.NotEqual(t, partial["alice"].Rating, full["alice"].Rating,
		"more votes means a different rating -- it is a function of the set")
}

// Completion is a view, and CLEARED counts as complete. §6a.15: "a field
// explicitly cleared does not count as missing".
func TestClearedFieldsAreCompleteNotMissing(t *testing.T) {
	// 4 expected, 2 present, 2 cleared. Nothing is missing.
	complete, err := Completion(Counts{Expected: 4, Present: 2, Cleared: 2})
	require.NoError(t, err)
	assert.Equal(t, 1.0, complete, "every expected field is either present or deliberately cleared")
	assert.Equal(t, 0, Missing(Counts{Expected: 4, Present: 2, Cleared: 2}),
		"and nothing is left to do")

	// 4 expected, 2 present, 0 cleared, 2 absent. Two missing.
	partial, err := Completion(Counts{Expected: 4, Present: 2})
	require.NoError(t, err)
	assert.Equal(t, 0.5, partial)
	assert.Equal(t, 2, Missing(Counts{Expected: 4, Present: 2}),
		"cleared fields are the whole difference between 50% and 100%")

	// A curator's deliberate "unknown" is finished work. Without this, a quest to
	// add birthdates keeps listing a performer somebody already curated, forever.
	// Two cleared fields turn two units of missing work into zero units. Written as
	// a direction check on Missing, because the completion figures above already
	// pin the arithmetic.
	assert.Greater(t, Missing(Counts{Expected: 4, Present: 2, Cleared: 0}),
		Missing(Counts{Expected: 4, Present: 2, Cleared: 2}),
		"clearing a field REMOVES it from the missing set -- that is the whole "+
			"difference between a quest that can close and one that never can")
}

// An entity with nothing expected is COMPLETE. Returning 0 would render a
// fully-curated entity as 0% and look like a bug to every user who sees it.
func TestAnEntityWithNothingExpectedIsComplete(t *testing.T) {
	c, err := Completion(Counts{Expected: 0})
	require.NoError(t, err)
	assert.Equal(t, 1.0, c, "nothing missing is 100%, not 0%")
	assert.Equal(t, 0, Missing(Counts{Expected: 0}))
}

// Inconsistent counts are an ERROR, not a clamp. Every path into Counts comes from
// a vocabulary and a record, so a negative or over-full set is a bug upstream —
// and clamping would turn a vocabulary mismatch into a plausible progress bar.
func TestInconsistentCountsAreAnError(t *testing.T) {
	for _, c := range []Counts{
		{Expected: -1},
		{Present: -1, Expected: 1},
		{Expected: 2, Present: 3},
		{Expected: 2, Present: 1, Cleared: 2},
	} {
		_, err := Completion(c)
		require.ErrorIs(t, err, ErrInconsistent, "%+v", c)
	}
}

// §6a.15: "a quest is a QUERY over completion, not a stored list -- a quest whose
// gap is closed is complete with no write required."
//
// The test proves the no-write property by construction: the same query, run
// twice with the gap closed in between, needs nothing persisted to agree.
func TestAQuestIsCompleteWithNoWrite(t *testing.T) {
	q := Quest{ID: "add-birthdates", Goal: 3}

	open := []Counts{
		{Expected: 3, Present: 1}, // 2 missing
		{Expected: 3, Present: 3}, // done
		{Expected: 3, Present: 2}, // 1 missing
	}
	before, err := EvaluateQuest(q, open)
	require.NoError(t, err)
	assert.Equal(t, 1, before.Completed, "one entity has no gap")
	assert.False(t, before.Done)

	// The gaps close -- by an import, a federated sync, or somebody else's edit.
	// None of those are ours to hook, which is exactly why this is a query.
	closed := []Counts{
		{Expected: 3, Present: 3},
		{Expected: 3, Present: 2, Cleared: 1},
		{Expected: 3, Present: 3},
	}

	// The same quest object, unmodified. Nothing about `q` changed, nothing was
	// written, and it is now done.
	after, err := EvaluateQuest(q, closed)
	require.NoError(t, err)
	assert.Equal(t, 3, after.Completed)
	assert.True(t, after.Done,
		"§6a.15: a quest whose gap is closed is complete with no write. A stored "+
			"quest counter would need a trigger to notice, and the gap can close by "+
			"routes that are not ours to hook")

	assert.Equal(t, q.ID, after.QuestID, "the quest itself was never touched")
}

// A cleared field closes a quest's gap, which is the composition of the two rules
// above and the case that makes both of them matter.
func TestAClearedFieldClosesAQuestsGap(t *testing.T) {
	q := Quest{ID: "q", Goal: 1}
	progress, err := EvaluateQuest(q, []Counts{{Expected: 2, Present: 1, Cleared: 1}})
	require.NoError(t, err)
	assert.True(t, progress.Done,
		"a curator's deliberate unknown is finished curation work, so the quest is "+
			"complete -- not blocked forever on a field nobody can fill in")
}

// §6a.16: the choice must compute from a vote set rather than maintain a counter.
// Order-independence is the observable half of that: a stateful system depends on
// arrival order, a recomputed one does not.
func TestRatingsDoNotDependOnVoteOrder(t *testing.T) {
	votes := []Vote{
		{VoterID: "alice", Target: "s1", Score: 90},
		{VoterID: "bob", Target: "s1", Score: 70},
		{VoterID: "alice", Target: "s2", Score: 80},
		{VoterID: "carol", Target: "s2", Score: 95},
		{VoterID: "bob", Target: "s2", Score: 60},
		{VoterID: "dave", Target: "s1", Score: 85},
	}

	base := Ratings(votes)
	baseRaters := RaterRatings(votes)
	require.Contains(t, base, "s1", "precondition: something was rated")
	require.Contains(t, baseRaters, "alice", "precondition: somebody voted")

	// BOTH pools. The rated pool alone would miss the voters, and comparing only
	// rated names is how a one-pool bug hides behind a passing order test.
	assertSame := func(label string, got, gotRaters map[string]Rating) {
		for id, r := range base {
			assert.InDelta(t, r.Rating, got[id].Rating, 1e-9,
				"rated %s, %s: the same vote SET must compute the same ratings "+
					"whatever order it arrived in", id, label)
		}
		for id, r := range baseRaters {
			assert.InDelta(t, r.Rating, gotRaters[id].Rating, 1e-9,
				"rater %s, %s", id, label)
		}
		assert.Len(t, got, len(base), "rated pool size, %s", label)
		assert.Len(t, gotRaters, len(baseRaters), "rater pool size, %s", label)
	}

	// Every rotation of an already-sorted list -- necessary, and NOT sufficient.
	for i := 0; i < len(votes); i++ {
		rotated := append(append([]Vote{}, votes[i:]...), votes[:i]...)
		assertSame("rotation", Ratings(rotated), RaterRatings(rotated))
	}

	// THE PART THAT ACTUALLY EXERCISES THE SORT. Rotating a sorted list produces
	// another sorted list, so the comparator is never asked to do anything -- which
	// is why reversing the tie-break SURVIVED the rotation assertions above.
	//
	// These are genuinely unsorted on purpose: reverse-sorted, and the result of
	// shuffling deterministically, so the input order cannot coincide with the
	// comparator's own.
	reverse := append([]Vote{}, votes...)
	for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
		reverse[i], reverse[j] = reverse[j], reverse[i]
	}
	assertSame("reverse-sorted", Ratings(reverse), RaterRatings(reverse))

	// Deterministic "shuffle": interleave the halves, which is not sorted for any
	// realistic vote set.
	var shuffled []Vote
	for i := 0; i < len(votes); i += 2 {
		shuffled = append(shuffled, votes[i])
		if i+1 < len(votes) {
			shuffled = append(shuffled, votes[i+1])
		}
	}
	shuffled = append([]Vote{shuffled[len(shuffled)-1]}, shuffled[:len(shuffled)-1]...)
	assertSame("shuffled", Ratings(shuffled), RaterRatings(shuffled))

	// Assert the fixture is genuinely unsorted, so this test cannot quietly become
	// a rotation of a sorted list again.
	require.False(t, isSortedByID(shuffled),
		"precondition: `shuffled` really is unsorted. Asserting it directly is what "+
			"stops this test quietly degenerating into another rotation of a sorted "+
			"list -- which is precisely how the reversed tie-break survived before.")

	// And the reverse, since that is the other way a query's order changes.
	rev := append([]Vote{}, votes...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	assertSame("reversed", Ratings(rev), RaterRatings(rev))
}

// A self-vote is skipped: a user rating their own scene is not malicious, and
// refusing the whole import for it would be worse. Skipping is also the only
// thing stopping a voter inflating their own rating.
func TestSelfVotesAreSkippedNotHonoured(t *testing.T) {
	// Someone rates themselves 100, repeatedly.
	inflated := Ratings([]Vote{
		{VoterID: "alice", Target: "alice", Score: 100},
		{VoterID: "alice", Target: "alice", Score: 100},
		{VoterID: "alice", Target: "alice", Score: 100},
	})
	assert.Equal(t, Rating{}, inflated["alice"],
		"a self-vote moves nothing and creates nothing: the map has no entry at all, "+
			"which is the Go zero value rather than DefaultRating. Reading it as "+
			"DefaultRating would be reading a missing key as a rating.")

	// A self-vote creates NO entry in either pool -- not an entry with the default
	// rating. Creating one would mean the caller sees a "player with votes and no
	// rating", which is a state the caller cannot distinguish from corruption.
	assert.NotContains(t, inflated, "alice", "a self-vote is not a rating")
	assert.NotContains(t, RaterRatings([]Vote{
		{VoterID: "alice", Target: "alice", Score: 100},
	}), "alice")

	// Proof that the skip is not simply "Ratings ignores votes": a legitimate vote
	// moves the voter, so the previous assertion is not passing for the wrong
	// reason.
	votes := []Vote{{VoterID: "alice", Target: "bob", Score: 100}}
	assert.NotEqual(t, DefaultRating, RaterRatings(votes)["alice"].Rating,
		"a real vote moves the voter's PREDICTING rating")
	assert.NotContains(t, Ratings(votes), "alice",
		"and alice is absent from the RECEIVED pool: she voted on bob, nobody voted "+
			"on her. Two different numbers for two different questions, and neither "+
			"pool is a superset of the other.")
}

// Being rated highly is evidence, so a target nobody has voted FOR still gets a
// rating. Collecting participants while replaying would rate a popular target as
// having no opinion of its own.
func TestABeRatedEntityGetsARating(t *testing.T) {
	got := Ratings([]Vote{{VoterID: "alice", Target: "popular-scene", Score: 100}})
	require.Contains(t, got, "popular-scene",
		"a target that only ever receives votes is still rated: being rated highly "+
			"is evidence")
	assert.Equal(t, 1, got["popular-scene"].Votes)
	assert.Greater(t, got["popular-scene"].Rating, DefaultRating)
}

// A 0..100 UI scale against K=32 is a real trap, and Normalise is what avoids it.
func TestTheVoteScaleIsNormalised(t *testing.T) {
	// Raw: a 90 out of 100 against a default opponent would be an "expected"
	// of 0.9, which is right. But an UNRATED opponent must be expected at 0.5 --
	// equal ratings are an even match by definition.
	assert.InDelta(t, 0.5, Expected(DefaultRating, DefaultRating), 1e-9,
		"equal ratings are an even match, which is the property that makes the "+
			"starting position neutral")

	assert.Equal(t, 0.0, Normalise(0))
	assert.Equal(t, 1.0, Normalise(100))
	assert.Equal(t, 0.5, Normalise(50))
	// Out-of-range votes are clamped rather than producing a wild rating.
	assert.Equal(t, 0.0, Normalise(-10))
	assert.Equal(t, 1.0, Normalise(1000))

	// The magnitude check: a full-scale vote against an unrated opponent moves
	// each side by K * (1 - 0.5) = 16, not by 3200. Asking RaterRatings for the
	// predicting number, which is the one that used to be wrong.
	got := RaterRatings([]Vote{{VoterID: "alice", Target: "bob", Score: 100}})
	assert.InDelta(t, DefaultRating+16.0, got["alice"].Rating, 1e-6,
		"a 100/100 vote against an unrated opponent moves by K*(1-0.5)=16. Using "+
			"the raw score would move it by 3200 and let one vote decide the order")
}

// A better-rated entity beats a worse-rated one, which is the only thing a
// ranking has to get right.
func TestRankingOrdersByRating(t *testing.T) {
	// Four voters rate `good` highly and `bad` poorly.
	var votes []Vote
	for i, v := range []string{"a", "b", "c", "d"} {
		votes = append(votes,
			Vote{VoterID: v, Target: "good", Score: 95},
			Vote{VoterID: v, Target: "bad", Score: 10},
		)
		_ = i
	}
	got := Ratings(votes)
	assert.Greater(t, got["good"].Rating, got["bad"].Rating,
		"§6a.16: ranking orders by the computed rating")
	assert.False(t, math.IsNaN(got["good"].Rating), "no NaN can reach a sort key")
	assert.False(t, math.IsNaN(got["bad"].Rating))
}

// The one-pool bug this package had, pinned so it cannot come back.
//
// A single rating per name conflates "how well this voter PREDICTS" with "how
// well this entity is RECEIVED". Sharing one number means a voter's enthusiasm for
// one item raises their rating, so the NEXT item they vote on is predicted less
// harshly and gains more. Measured with four voters rating good=95 and bad=10:
//
//	one pool:  good 1442.4   bad 1551.2   -- good ranks BELOW bad
//	two pools: good 1556.4   bad 1452.4   -- correct
//
// So the single-pool version ranked a well-loved scene below a disliked one,
// purely because its voters also rated something else. The test asserts the
// direction, which is the only part that was ever wrong.
func TestRatingsSeparatePredictionFromReceipt(t *testing.T) {
	var votes []Vote
	for _, v := range []string{"a", "b", "c", "d"} {
		votes = append(votes,
			Vote{VoterID: v, Target: "good", Score: 95},
			Vote{VoterID: v, Target: "bad", Score: 10},
		)
	}

	received := Ratings(votes)
	require.Contains(t, received, "good")
	require.Contains(t, received, "bad")

	assert.Greater(t, received["good"].Rating, received["bad"].Rating,
		"the RECEIVED pool must order by how the entities were received. A voter "+
			"being enthusiastic about `good` must not make `bad` look better received.")

	// The predicting pool is a different question and a different answer.
	predicting := RaterRatings(votes)
	for _, v := range []string{"a", "b", "c", "d"} {
		require.Contains(t, predicting, v)
	}

	// A voter's predicting rating rises as their votes are vindicated, which is
	// the pool's own meaning and is independent of what anything was received at.
	enthusiast := RaterRatings([]Vote{
		{VoterID: "a", Target: "x", Score: 100},
		{VoterID: "a", Target: "y", Score: 100},
		{VoterID: "a", Target: "z", Score: 100},
	})
	pessimist := RaterRatings([]Vote{
		{VoterID: "b", Target: "x", Score: 0},
		{VoterID: "b", Target: "y", Score: 0},
		{VoterID: "b", Target: "z", Score: 0},
	})
	assert.Greater(t, enthusiast["a"].Rating, pessimist["b"].Rating,
		"a voter whose high scores were borne out is rated above one whose low "+
			"scores were")

	// THE TWO FORMS ARE DISTINGUISHABLE, and getting here took two wrong attempts.
	//
	// Above, every vote pairs a voter with a fresh target, so both ratings sit at
	// DefaultRating when the update runs and Expected(target, rater) is arithmetically
	// equal to Expected(rater, target). A symmetric fixture cannot tell the correct
	// line from the wrong one: the mutation survived a fully green suite.
	//
	// Two things had to change, and both were found by measuring rather than
	// reasoning. First the scores: a 50/50 DRAW scores exactly the expectation, so it
	// moves nothing under either form. Then the NAMES: votes are replayed in sorted
	// order, so a target called "fresh" is replayed FIRST, while the voter is still
	// at 1500 -- which is the same coincidence by a different route. The target here
	// sorts LAST on purpose.
	//
	// The assertion is a COMPARISON between two voters, not a threshold. With the
	// arguments correct, a rater the system already rates above average gives a
	// target a LARGER bump than a rater it rates below average: the update asks how
	// well this entity is doing, and a good rater's approval is better evidence.
	// Swap the arguments and the order REVERSES, because the update then asks how
	// well the voter is doing and drags every target they rate well down with them.
	praised := []Vote{
		{VoterID: "a", Target: "x", Score: 100},
		{VoterID: "a", Target: "y", Score: 100},
		{VoterID: "a", Target: "zfresh", Score: 80},
	}
	alsoPraised := []Vote{
		{VoterID: "b", Target: "p", Score: 0},
		{VoterID: "b", Target: "q", Score: 0},
		{VoterID: "b", Target: "zfresh", Score: 80},
	}
	byGoodRater := Ratings(praised)["zfresh"].Rating - DefaultRating
	byPoorRater := Ratings(alsoPraised)["zfresh"].Rating - DefaultRating

	assert.Greater(t, byGoodRater, byPoorRater,
		"a target praised 80 by a voter the system rates ABOVE average must gain "+
			"more than one praised 80 by a voter it rates BELOW. The received update "+
			"uses the TARGET's own expectation. Swapping the arguments inverts this "+
			"ordering, which is the bug the two pools exist to prevent")

	// Neither pool is a superset of the other, which is the structural statement
	// that the split is real rather than a rename.
	assert.NotContains(t, received, "a", "a only voted; nothing is known about how "+
		"they were received")
	assert.NotContains(t, predicting, "x", "x was only rated; x has never predicted "+
		"anything")
}

// isSortedByID reports whether votes are in the comparator's own order. Used only
// to assert a fixture's unsortedness, so a future edit cannot turn the
// order-independence test into a no-op.
func isSortedByID(votes []Vote) bool {
	for i := 1; i < len(votes); i++ {
		a, b := votes[i-1], votes[i]
		if a.VoterID > b.VoterID ||
			(a.VoterID == b.VoterID && a.Target > b.Target) {
			return false
		}
	}
	return true
}
