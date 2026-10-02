package ecosystem

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R057's remaining two capabilities from §6a.19: "see rankings, contribute to preservation".
//
// Push (through the proposal path) and pull are done. These are the last two, and they are not
// the same kind of thing: one is a READ of something the mesh computed, the other is a WRITE
// into the mesh. Both are subject to the same hard constraint, which is why they live in one
// file: §6a.9's consent rule applies to the write, and §6a.16's applies to the read, and getting
// either one wrong is a way to leak or to launder influence.
//
// RANKINGS: §6a.16 -- "compute a rating from a VOTE SET rather than maintain a counter (#4)"
//
// THE RULE THIS ENFORCES. A rating derived from a counter cannot be audited and cannot be
// recomputed when the vote set changes. So RatingOf folds the votes every time and there is no
// cached score anywhere in the package: no `rating` field, no incremental update, nothing to
// fall out of step with the votes. A mutation that introduced a cache would have to be written
// from scratch, which is the point -- the shape of the code is what enforces the rule.

// THE POSITIVE CONTROL. A leaderboard where everything is refused would pass every refusal test.
func TestRankingsProduceScoresFromTheVoteSet(t *testing.T) {
	l := NewLeaderboard(ScopeMesh)

	require.NoError(t, l.Record(Vote{
		Voter: "peer-a", Left: "alpha", Right: "beta", Winner: "alpha",
	}))
	require.NoError(t, l.Record(Vote{
		Voter: "peer-b", Left: "beta", Right: "gamma", Winner: "beta",
	}))

	board, err := l.Board(context.Background(), 10)
	require.NoError(t, err)
	require.NotEmpty(t, board, "votes that were recorded must produce a leaderboard")

	// MY FIRST EXPECTATION HERE WAS WRONG and the code was right. I wrote that beta should lead
	// because it "won once and lost once". Beta: 2 games, 1 win -> (1+1)/(2+2) = 0.500. Alpha:
	// 1 game, 1 win -> (1+1)/(1+2) = 0.667. Alpha leads, and it should: a 1-0 record is better
	// than 1-1, which is the whole reason a rating is a function of the vote set rather than of
	// win COUNT -- a counter would have called it a tie.
	top := board[0]
	require.Equal(t, "alpha", top.PublicID,
		"alpha is 1-0 (0.667) and beta is 1-1 (0.500), so alpha leads; a counter keyed on wins "+
			"would have called them level and lost the ordering")
	assert.InDelta(t, 0.667, top.Rating, 0.001,
		"Laplace-smoothed: (wins+1)/(games+2), so a 1-0 record beats a 1-1 record")
	assert.GreaterOrEqual(t, top.Votes, 1, "a ranked entity must have played at least once")
}

// THE CENTRAL RULE. The score is a function of the vote set, so a vote that is REMOVED changes
// the score, and a vote that is ADDED changes it. A counter cannot do either.
func TestRatingsAreDerivedFromTheVotesNotACounter(t *testing.T) {
	l := NewLeaderboard(ScopeMesh)
	require.NoError(t, l.Record(Vote{Voter: "v1", Left: "alpha", Right: "beta", Winner: "alpha"}))
	require.NoError(t, l.Record(Vote{Voter: "v2", Left: "alpha", Right: "beta", Winner: "alpha"}))

	before, err := l.RatingOf("alpha")
	require.NoError(t, err)

	// A third vote for beta, added. A counter would be incremented on alpha and unchanged here.
	require.NoError(t, l.Record(Vote{Voter: "v3", Left: "alpha", Right: "beta", Winner: "beta"}))

	after, err := l.RatingOf("alpha")
	require.NoError(t, err)

	assert.NotEqual(t, before.Rating, after.Rating,
		"alpha's rating must change when a vote it played in is added; a rating that only ever "+
			"goes up is a counter, and §6a.16 (#4) requires the vote set to be the truth")
	assert.Less(t, after.Rating, before.Rating, "alpha lost the added vote, so its derived rating must drop")
}

// A vote set is a SET. The same pair from the same voter is one vote, recorded twice or ten
// times, or a counter is trivially inflatable by anyone with a script.
func TestDuplicateVotesDoNotInflateAScore(t *testing.T) {
	l := NewLeaderboard(ScopeMesh)
	for i := 0; i < 5; i++ {
		require.NoError(t, l.Record(Vote{Voter: "v1", Left: "alpha", Right: "beta", Winner: "alpha"}))
	}
	once, err := l.RatingOf("alpha")
	require.NoError(t, err)

	other := NewLeaderboard(ScopeMesh)
	require.NoError(t, other.Record(Vote{Voter: "v1", Left: "alpha", Right: "beta", Winner: "alpha"}))
	single, err := other.RatingOf("alpha")
	require.NoError(t, err)

	assert.Equal(t, single.Rating, once.Rating,
		"five identical votes from one voter must score the same as one; otherwise the score "+
			"is a counter keyed on a field that happens to be unique rather than on the vote set")
}

// §6a.16: "Time decay reflects current relevance." A vote from a year ago is not worth what a
// vote from today is, and a leaderboard that cannot decay is a leaderboard of 2019.
func TestRatingsDecayWithAge(t *testing.T) {
	// BOTH boards read the clock at TestNow when the rating is taken, and differ only in WHEN
	// the vote was recorded. A board whose clock is frozen in the past measures its own votes as
	// zero-age, so freezing the clock does not produce a stale vote -- I tried that first and it
	// gave two identical ratings, which is how the recordedAt bug above was found.
	now := TestNow
	fourYearsAgo := TestNow.AddDate(-4, 0, 0)

	// The old vote: recorded while the clock read four years ago, then the clock moves to now.
	oldBoard := NewLeaderboard(ScopeMesh)
	oldBoard.withClock(func() time.Time { return fourYearsAgo })
	require.NoError(t, oldBoard.Record(Vote{Voter: "v1", Left: "alpha", Right: "beta", Winner: "alpha"}))
	oldBoard.withClock(func() time.Time { return now })

	// The fresh vote: same matchup, recorded now.
	freshBoard := NewLeaderboard(ScopeMesh)
	oldBoard2 := freshBoard.withClock(func() time.Time { return now })
	require.NoError(t, oldBoard2.Record(Vote{Voter: "v1", Left: "alpha", Right: "beta", Winner: "alpha"}))

	fresh, err := freshBoard.RatingOf("alpha")
	require.NoError(t, err)
	stale, err := oldBoard.RatingOf("alpha")
	require.NoError(t, err)

	// Both boards hold exactly one vote, one win. The only difference is its age, so a rating
	// that ignores age gives the identical answer -- which is what the first version returned.
	assert.Greater(t, fresh.Rating, stale.Rating,
		"an identical vote from four years ago must score lower than one from today; 6a.16 says "+
			"time decay reflects current relevance, and with one vote each the age is the only "+
			"thing that can differ")
	// And the magnitude, computed rather than guessed. DecayHalfLife is 18 MONTHS, so four years
	// is 4*12/18 = 2.67 half-lives: w = 2^-2.67 = 0.1533, and (w+1)/(w+2) = 0.5356.
	//
	// I first asserted < 0.51 here, having mentally halved 18 MONTHS as if it were 18 YEARS and
	// computed ~8.5 half-lives. The code returned 0.5356 and was right; the bound was wrong. An
	// assertion whose expected value is a guess is a test that will be "fixed" to match whatever
	// the code happens to do, so this one states the arithmetic instead.
	//
	// The span MUST be the same one the test used to build the stale vote. My first version wrote
	// 4*12*30*24 days -- 360-day years -- while `AddDate(-4, 0, 0)` is four CALENDAR years, so the
	// two disagreed by 0.9e-3 and the assertion failed against correct code. The expected weight
	// is now derived from the same `fourYearsAgo` the vote was stamped with, which makes the
	// two impossible to disagree.
	age := now.Sub(fourYearsAgo).Hours()
	w := math.Pow(2, -age/DecayHalfLife.Hours())
	assert.InDelta(t, (w+1)/(w+2), stale.Rating, 1e-9,
		"the decayed rating must be exactly (w+1)/(w+2) for w=2^(-age/half-life); the Laplace "+
			"prior is what keeps it above 0.5 instead of collapsing to it")
	assert.Greater(t, w, 0.1, "and the weight is small but not negligible -- a vote has to be "+
		"several half-lives old before it stops mattering, which is what makes decay a "+
		"relevance signal rather than a cliff")
}

// A vote is only meaningful between two DIFFERENT entities, and only if exactly one of them won.
// A self-vote is not a comparison, and a vote with two winners (or none) is not a vote -- it is
// noise that would otherwise be silently scored as a draw.
func TestMalformedVotesAreRefused(t *testing.T) {
	l := NewLeaderboard(ScopeMesh)

	for _, tc := range []struct {
		name string
		v    Vote
	}{
		{"self vote", Vote{Voter: "v1", Left: "alpha", Right: "alpha", Winner: "alpha"}},
		{"both win", Vote{Voter: "v1", Left: "alpha", Right: "beta", Winner: "alpha,beta"}},
		{"neither wins", Vote{Voter: "v1", Left: "alpha", Right: "beta", Winner: ""}},
		{"winner not in the matchup", Vote{Voter: "v1", Left: "alpha", Right: "beta", Winner: "gamma"}},
		{"no voter", Vote{Voter: "", Left: "alpha", Right: "beta", Winner: "alpha"}},
		{"empty id", Vote{Voter: "v1", Left: "", Right: "beta", Winner: "alpha"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Error(t, l.Record(tc.v),
				"a malformed vote must be refused; scoring it as a draw would let a broken "+
					"client pad the leaderboard")
		})
	}
	// The positive control for the same loop, so refusal cannot pass as blanket rejection.
	require.NoError(t, l.Record(Vote{Voter: "v1", Left: "alpha", Right: "beta", Winner: "alpha"}))
}

// §6a.16: "Leaderboards are scoped local / mesh / global". A GLOBAL board that showed a peer's
// votes would be a scope the user never consented to, and a LOCAL board showing mesh votes
// would be a mesh fact presented as a local one.
func TestScopesDoNotLeakIntoEachOther(t *testing.T) {
	local := NewLeaderboard(ScopeLocal)
	mesh := NewLeaderboard(ScopeMesh)

	require.NoError(t, mesh.Record(Vote{Voter: "peer-a", Left: "alpha", Right: "beta", Winner: "alpha"}))

	_, err := local.RatingOf("alpha")
	assert.Error(t, err,
		"a local board must not know a rating that only exists on the mesh scope; presenting a "+
			"mesh fact as a local one is a scope lie")

	_, err = mesh.RatingOf("alpha")
	assert.NoError(t, err, "and the mesh board, which is where the vote was recorded, must know it")
}

// §6a.16: "Gamified voting -- streaks, daily matchups, bracket challenges -- is a retention
// mechanic and grants NOTHING (§6a.10)." Nothing here may grant a trust level or any access.
func TestRatingsGrantNothing(t *testing.T) {
	l := NewLeaderboard(ScopeMesh)
	for i := 0; i < 3; i++ {
		require.NoError(t, l.Record(Vote{
			Voter: "v" + string(rune('a'+i)), Left: "alpha", Right: "beta", Winner: "alpha",
		}))
	}

	entry, err := l.RatingOf("alpha")
	require.NoError(t, err)
	assert.False(t, entry.GrantsAccess,
		"a rating grants no access whatsoever (6a.16, 6a.10); a field that could carry a "+
			"trust level here would be a privilege escalation path through the leaderboard")
	assert.Empty(t, entry.TrustLevel,
		"and no trust level, for the same reason")

	// There is no way to spend a rating: the leaderboard's whole surface is this.
	for _, m := range methodNamesOf(l) {
		switch m {
		case "Record", "Board", "RatingOf", "withClock", "now":
		default:
			t.Errorf("Leaderboard exposes %q; a leaderboard that could redeem a rating is a "+
				"privilege escalation path, and §6a.16 says it grants nothing", m)
		}
	}
}

// PRESERVATION: §6a.9 -- "not a feature", a protocol. And this is the hard constraint:
//
//   "For a user who set metadata_share = 'opted-out', their scene is never a replication
//    subject, and no amount of mesh popularity or a preservation bounty changes that."
//
// So the consent check is not a filter applied to a contribution -- it is checked before the
// contribution can be REGISTERED, and it is checked against the SUBJECT's consent, which is
// the owner's decision, not the contributor's.

// THE POSITIVE CONTROL. A contribution path that refuses everything would pass every refusal
// test below.
func TestAnOptedInSubjectAcceptsAContribution(t *testing.T) {
	p := NewPreservation()

	require.NoError(t, p.Contribute(Contribution{
		Contributor:  "peer-a",
		SubjectID:    "scene-1",
		SubjectShare: "opted-in",
		ReplicaCount: 1,
	}), "a contribution for a subject whose owner opted in must be accepted")

	// The contribution asked for 1 replica, and the policy says 3. That is the SPEC, not a bug:
	// §6a.9 makes DefaultReplicas a floor. I first asserted 1 here on the reasoning that an
	// accepted contribution should be "visible", and the code correctly refused to honour it.
	// Acceptance is visible in the policy EXISTING -- a refused contribution leaves no policy
	// at all, which is what the next test checks.
	pol, err := p.PolicyFor("scene-1")
	require.NoError(t, err)
	assert.Equal(t, DefaultReplicas, pol.Replicas,
		"6a.9: the default is at least three instances, and a contribution claiming one cannot "+
			"lower that floor")
	assert.Equal(t, "scene-1", pol.SubjectID, "and the policy is for the contributed subject")
}

// THE HARD CONSTRAINT. This is §6a.9's non-negotiable, and the bounty is the part that makes it
// a trap: a contribution is a GIFT, and a gift is exactly the reasoning non-negotiable #7 exists
// to stop -- "it's a good cause" is not a reason to override a consent decision.
func TestAnOptedOutSubjectIsNeverAReplicationSubject(t *testing.T) {
	for _, tc := range []struct{ name, share string }{
		{"opted out", "opted-out"},
		{"unknown share state", "maybe"},
		{"empty share state", ""},
		{"the word opted-in with trailing space", "opted-in "},
		{"the word opted-in with different case", "OPTED-IN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPreservation()
			err := p.Contribute(Contribution{
				Contributor:  "peer-a",
				SubjectID:    "scene-1",
				SubjectShare: tc.share,
				ReplicaCount: 3,
			})
			assert.Error(t, err,
				"§6a.9: a scene whose owner did not opt in is NEVER a replication subject, and "+
					"no amount of mesh popularity or a preservation bounty changes that")

			_, err = p.PolicyFor("scene-1")
			assert.Error(t, err,
				"and nothing may be registered for it -- a refused contribution that still left "+
					"a policy behind would be a refusal in name only")
		})
	}
}

// A contribution is a CLAIM that a replica exists somewhere. It is not a request to place one.
// So the receiving side must be able to say "I will not hold this", and refusing is not a
// partial success (§6a.9: "refusing is not a partial success").
func TestAReplicaCanBeRefusedAndRefusalIsTotal(t *testing.T) {
	p := NewPreservation().withRefusals("peer-b")

	require.NoError(t, p.Contribute(Contribution{
		Contributor:  "peer-a",
		SubjectID:    "scene-1",
		SubjectShare: "opted-in",
		ReplicaCount: 1,
	}))

	err := p.AcceptReplica("scene-1", "peer-b")
	assert.Error(t, err,
		"an instance that must not hold a replica refuses it")

	// And the refusal must be TOTAL: not "accepted with a warning", not half a replica.
	_, err = p.HoldingReplica("scene-1", "peer-b")
	assert.Error(t, err,
		"§6a.9: refusing is not a partial success; an instance that refused a replica must "+
			"hold none of it")
}

// §6a.9: "Every content object carries a preservation policy, and the default is that a scene
// is hosted on at least three instances." The default is a floor, and a contribution can never
// be used to LOWER it -- a single well-meaning peer must not talk the mesh down to one replica.
func TestTheThreeReplicaDefaultIsAFloorNotATarget(t *testing.T) {
	p := NewPreservation()
	require.NoError(t, p.Contribute(Contribution{
		Contributor:  "peer-a",
		SubjectID:    "scene-1",
		SubjectShare: "opted-in",
		ReplicaCount: 1,
	}))

	pol, err := p.PolicyFor("scene-1")
	require.NoError(t, err)
	assert.Equal(t, DefaultReplicas, pol.Replicas,
		"§6a.9: a scene is hosted on at least three instances by default; a single contribution "+
			"must not be able to declare the object adequately preserved")
	assert.GreaterOrEqual(t, pol.Replicas, 3)
}

// A contribution is not a vote and grants nothing, for the same reason as a rating.
func TestContributionsGrantNothing(t *testing.T) {
	p := NewPreservation()
	require.NoError(t, p.Contribute(Contribution{
		Contributor: "peer-a", SubjectID: "scene-1", SubjectShare: "opted-in", ReplicaCount: 3,
	}))

	for _, m := range methodNamesOf(p) {
		switch m {
		case "Contribute", "PolicyFor", "AcceptReplica", "HoldingReplica", "withRefusals", "withClock", "now":
		default:
			t.Errorf("Preservation exposes %q; preservation grants no access (§6a.9, 6a.10)", m)
		}
	}
}

// THE SURVIVORS, AND WHICH WERE REAL. Nine mutations survived the first pass. Checking each
// rather than assuming produced four real gaps, four untested properties, and one mutant of mine
// that did not model what it was named for.

// (A) NOT A TEST GAP -- MY MUTANT WAS EQUIVALENT. "Duplicate votes no longer idempotent" appended
// recordedAt to the vote key, which SHOULD have made five votes five keys. It did not, because
// every vote on a frozen clock carries the same timestamp, so the suffix is constant and the key
// is unchanged. The suite was right to pass. A mutant that cannot differ from the original proves
// nothing about the test, and a REAL break needs a clock that moves -- which is what this is.
func TestIdempotenceSurvivesAClockThatMoves(t *testing.T) {
	build := func() *Leaderboard {
		l := NewLeaderboard(ScopeMesh)
		// The clock advances between records, so a key built from the timestamp WOULD differ.
		tick := TestNow
		l.withClock(func() time.Time { return tick })
		for i := 0; i < 5; i++ {
			require.NoError(t, l.Record(Vote{
				Voter: "v1", Left: "alpha", Right: "beta", Winner: "alpha",
			}))
			tick = tick.Add(time.Hour)
		}
		return l
	}
	five := build()
	one := NewLeaderboard(ScopeMesh)
	require.NoError(t, one.Record(Vote{Voter: "v1", Left: "alpha", Right: "beta", Winner: "alpha"}))

	gotFive, err := five.RatingOf("alpha")
	require.NoError(t, err)
	gotOne, err := one.RatingOf("alpha")
	require.NoError(t, err)

	// The VOTE COUNT is the idempotence assertion, not the rating. All five recordings are one
	// vote, but they were stamped an hour apart, so time decay legitimately gives the surviving
	// vote a weight of 2^-4/12960 rather than exactly 1 -- a 6e-6 difference. Asserting the two
	// ratings were bit-identical would be asserting that decay does not work.
	assert.Equal(t, 1, gotFive.Votes, "five recordings of the same vote are ONE game played")
	assert.Equal(t, 1, gotFive.Wins)
	assert.InDelta(t, gotOne.Rating, gotFive.Rating, 1e-4,
		"and the score must be the same to within decay's own resolution, not five times higher")
}

// (B) NOT A TEST GAP -- MY MUTANT WAS EQUIVALENT AGAIN. "Pair not canonicalised" removed the
// sort, so "alpha beat beta" and "beta beat alpha" would key differently. No test casts the same
// matchup in both orders, so nothing observed the difference -- which means the property IS
// untested, just not by the mutant I wrote. Casting both orders is the only way to see it.
func TestAMatchupIsOneVoteWhicheverOrderItIsCastIn(t *testing.T) {
	l := NewLeaderboard(ScopeMesh)
	require.NoError(t, l.Record(Vote{Voter: "v1", Left: "alpha", Right: "beta", Winner: "alpha"}))
	require.NoError(t, l.Record(Vote{Voter: "v1", Left: "beta", Right: "alpha", Winner: "alpha"}),
		"the same matchup cast in the other order is still well-formed")

	entry, err := l.RatingOf("alpha")
	require.NoError(t, err)
	assert.Equal(t, 1, entry.Votes,
		"one voter comparing the same pair twice, in either order, is ONE vote; otherwise the "+
			"voter can play both sides of a matchup to inflate both ratings")
	assert.Equal(t, 1, entry.Wins)
}

// (C) REAL. The scope check in RatingOf survived removal because no test asked an UNKNOWN scope.
// The local-vs-mesh test uses two scopes that both have vote sets, so it never reaches the guard.
func TestAnUnknownScopeHasNoVoteSet(t *testing.T) {
	l := NewLeaderboard(ScopeGlobal) // not accepted yet; see the ScopeGlobal comment
	require.NoError(t, l.Record(Vote{Voter: "v1", Left: "alpha", Right: "beta", Winner: "alpha"}),
		"the board accepts a vote so the scope guard is the thing under test")

	_, err := l.RatingOf("alpha")
	assert.Error(t, err,
		"a scope this package does not implement must not answer with a rating; returning one "+
			"would present a number with no vote set behind it")
}

// (D) REAL. Board(n) was only ever called with n=10, so the MaxPublicLimit clamp never engaged --
// a board with 200 entities returned all 200 when asked for 500.
func TestTheBoardIsBoundedByMaxPublicLimit(t *testing.T) {
	l := NewLeaderboard(ScopeMesh)
	for i := 0; i < MaxPublicLimit+20; i++ {
		a, b := fmt.Sprintf("e%03d", i), fmt.Sprintf("e%03d", i+1)
		require.NoError(t, l.Record(Vote{
			Voter: fmt.Sprintf("v%03d", i), Left: a, Right: b, Winner: a,
		}))
	}

	huge, err := l.Board(context.Background(), 10_000)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(huge), MaxPublicLimit,
		"a board must be bounded by MaxPublicLimit like any other public read; a leaderboard "+
			"that returns every entity in the mesh is an enumeration surface")

	// And a request for fewer than the cap gets what it asked for -- a bound, not a truncation.
	small, err := l.Board(context.Background(), 5)
	require.NoError(t, err)
	assert.Len(t, small, 5)
}

// (E) REAL. The sort's final tiebreak on id was untested, so the order was not proven total. A
// non-total sort makes equal rows reshuffle between calls, and "rank 1" is then not a fact.
func TestTheBoardOrderIsTotal(t *testing.T) {
	// Three entities, all 1-0, so every rating is identical and only the tiebreak orders them.
	l := NewLeaderboard(ScopeMesh)
	for i, id := range []string{"charlie", "alpha", "bravo"} {
		require.NoError(t, l.Record(Vote{
			Voter: fmt.Sprintf("v%d", i), Left: id, Right: "other", Winner: id,
		}))
	}

	var first []string
	for run := 0; run < 20; run++ {
		board, err := l.Board(context.Background(), 10)
		require.NoError(t, err)
		ids := make([]string, len(board))
		for i, e := range board {
			ids[i] = e.PublicID
		}
		if run == 0 {
			first = ids
			continue
		}
		require.Equal(t, first, ids,
			"identical ratings must produce an identical order on every call; a sort that is "+
				"not total reshuffles equal rows, and a developer testing rank 1 gets a "+
				"different answer for the same data")
	}
	// "other" LOST all three matches, so it is on the board too -- at 0.5 after smoothing, last.
	// The first three are all 1-0 and therefore tie at 0.667, which is the case the id tiebreak
	// exists for. My first expectation omitted "other" and failed against correct code.
	assert.Equal(t, []string{"alpha", "bravo", "charlie", "other"}, first,
		"the three 1-0 entities tie and the id orders them; the winless one is last")
}

// (F) REAL. A negative age -- a clock corrected backwards -- was rewarded above 1.0, so a
// "future" vote outranked a fresh one. Untested because every test's clock moves forwards.
func TestAVoteFromTheFutureIsNotRewarded(t *testing.T) {
	now := TestNow
	future := TestNow.AddDate(0, 0, 30)

	skewed := NewLeaderboard(ScopeMesh)
	skewed.withClock(func() time.Time { return future })
	require.NoError(t, skewed.Record(Vote{Voter: "v1", Left: "alpha", Right: "beta", Winner: "alpha"}))
	// The clock is corrected back -- a NTP fix, a VM restored from a snapshot.
	skewed.withClock(func() time.Time { return now })

	entry, err := skewed.RatingOf("alpha")
	require.NoError(t, err)
	// The exact zero-age value, (w+1)/(w+2) at w=1. Written as 2.0/3.0 rather than 0.667 because
	// a rounded literal with a 1e-9 tolerance is an assertion that fails against correct code --
	// I hit exactly that. A mutation rewarding negative age gives (w+1)/(w+2) with w>1, which is
	// strictly above 2/3, so this distinguishes the two.
	assert.Equal(t, 2.0/3.0, entry.Rating,
		"a negative age must count as zero age, not as a weight above 1; otherwise a clock that "+
			"jumps backwards hands the votes recorded around the jump a permanent bonus")

}

// (G) REAL. Contribute's own shape checks were untested -- zero replicas and a missing
// contributor both passed. A contribution claiming no replicas is not a contribution, and one
// with no contributor cannot be attributed or refused.
func TestAMalformedContributionIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    Contribution
	}{
		{"no contributor", Contribution{SubjectID: "scene-1", SubjectShare: OptedIn, ReplicaCount: 3}},
		{"no subject", Contribution{Contributor: "peer-a", SubjectShare: OptedIn, ReplicaCount: 3}},
		{"zero replicas", Contribution{Contributor: "peer-a", SubjectID: "scene-1", SubjectShare: OptedIn, ReplicaCount: 0}},
		{"negative replicas", Contribution{Contributor: "peer-a", SubjectID: "scene-1", SubjectShare: OptedIn, ReplicaCount: -3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPreservation()
			assert.Error(t, p.Contribute(tc.c))
			_, err := p.PolicyFor("scene-1")
			assert.Error(t, err, "a refused contribution must leave no policy behind")
		})
	}
	// The positive control, so the loop above cannot pass as blanket rejection.
	require.NoError(t, NewPreservation().Contribute(Contribution{
		Contributor: "peer-a", SubjectID: "scene-1", SubjectShare: OptedIn, ReplicaCount: 3,
	}))
}

// (H) REAL. PolicyFor returned a slice ALIASING the policy's own, so a caller appending to the
// returned Holders would write into the stored policy. The same aliasing hazard as a Go loop
// variable, and just as silent.
func TestTheReturnedPolicyDoesNotAliasStoredState(t *testing.T) {
	p := NewPreservation()
	require.NoError(t, p.Contribute(Contribution{
		Contributor: "peer-a", SubjectID: "scene-1", SubjectShare: OptedIn, ReplicaCount: 3,
	}))

	first, err := p.PolicyFor("scene-1")
	require.NoError(t, err)
	require.Empty(t, first.Holders)
	first.Holders = append(first.Holders, "peer-forged")

	second, err := p.PolicyFor("scene-1")
	require.NoError(t, err)
	assert.Empty(t, second.Holders,
		"a caller must not be able to add a holder by appending to the slice it was handed; "+
			"PolicyFor hands out a copy, and a mutation through it would be a replica that "+
			"exists in the policy and nowhere else")
}

// (I) REAL, and the interesting one. "Empty entity id accepted" SURVIVED, and reading the test
// explains why it looked covered: TestMalformedVotesAreRefused does include an empty-id case,
// but it pairs `Left: ""` with `Winner: "alpha"`, and that input is ALSO refused by the winner
// check. So the suite passed with the empty-id guard removed -- two guards, one test, and the
// test could not tell which one fired.
//
// I confirmed it with a throwaway probe before writing this: `Left: "", Right: "alpha",
// Winner: "alpha"` is ACCEPTED with the guard removed and refused with it restored. A leaderboard
// entry for a nameless entity, reachable by any peer. The lesson is the one from the Syncer
// field-writer guard in R057: when two guards can each catch the same input, a test that asserts
// only the outcome is testing their UNION, and deleting either one leaves the suite green.
func TestAVoteWithAnEmptyEntityIDIsRefusedEvenWhenTheWinnerIsValid(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    Vote
	}{
		{"empty left, winner is the right side", Vote{Voter: "v1", Left: "", Right: "alpha", Winner: "alpha"}},
		{"empty right, winner is the left side", Vote{Voter: "v1", Left: "alpha", Right: "", Winner: "alpha"}},
		{"whitespace-only entity id", Vote{Voter: "v1", Left: "  ", Right: "alpha", Winner: "alpha"}},
		{"empty left, empty right, empty winner", Vote{Voter: "v1", Left: "", Right: "", Winner: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := NewLeaderboard(ScopeMesh)
			assert.Error(t, l.Record(tc.v),
				"an entity with no id cannot be ranked, and a nameless row on a leaderboard is a "+
					"row no caller can look up again")
		})
	}
	// The positive control, since every case above must be refused for a DIFFERENT reason than
	// "this loop refuses everything".
	require.NoError(t, NewLeaderboard(ScopeMesh).Record(
		Vote{Voter: "v1", Left: "alpha", Right: "beta", Winner: "alpha"}))
}

// (J) REAL. RatingOf's own empty-name check was untested, and the "winner not in the matchup"
// case does not reach it -- an empty name never matches either side, so the winner check fires
// first. Same union-of-guards shape as (I).
func TestRatingOfAnUnnamedEntityIsAnError(t *testing.T) {
	l := NewLeaderboard(ScopeMesh)
	require.NoError(t, l.Record(Vote{Voter: "v1", Left: "alpha", Right: "beta", Winner: "alpha"}))

	// THIS STILL SURVIVED a version of this test that only asserted Error. With the name check
	// deleted, RatingOf("") scans for votes naming "", finds none, and returns the "no votes on
	// the scope" error -- a DIFFERENT error from a DIFFERENT guard, and indistinguishable to
	// assert.Error. Two guards on one bad input, and an outcome-only assertion tests their union.
	//
	// So this asserts the MESSAGE, which is what distinguishes them. A caller that handles
	// "you named nothing" differently from "that entity is unrated" -- and it should, since one
	// is a client bug and the other is a fact about the mesh -- depends on them being distinct.
	for _, query := range []string{"", "   "} {
		_, err := l.RatingOf(query)
		require.Error(t, err, "an unnamed entity has no rating")
		assert.Contains(t, err.Error(), "no entity named",
			"the error must be the one that says no entity was NAMED, not the one that says the "+
				"entity is unrated; a caller treats a client bug and a fact about the mesh "+
				"differently, and a bare Error assertion cannot tell them apart")
	}

	// And the contrast that makes the distinction real: a NAMED entity with no votes is a
	// different error, and it must stay different.
	_, err := l.RatingOf("never-played")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "no entity named",
		"a named but unrated entity is not the same condition as naming nothing")
}

// (K) REAL. The board's game-count tiebreak was dropped without a test noticing. It matters
// because it is the difference between "rated on one vote" and "rated on fifty" when the ratios
// are equal -- and a leaderboard that ranks a single lucky win above a consistent record is
// exactly the failure a derived rating is supposed to avoid.
func TestMoreGamesOutranksFewerAtAnEqualRating(t *testing.T) {
	l := NewLeaderboard(ScopeMesh)

	// A TIE SOLVED FOR, NOT GUESSED. Laplace smoothing makes (w+1)/(g+2) equal for two different
	// records only at specific pairs: 1-0 and 3-1 are both exactly 2/3. (I first used 2-0 and
	// 3-1 and asserted they were equal; they are 0.750 and 0.800, and the code was right.)
	//
	// THE NAMES ARE THE LOAD-BEARING PART, and I got that wrong twice. The test only distinguishes
	// the game-count tiebreak from the id tiebreak if the two point in OPPOSITE directions: the
	// better-attested entity must be the one whose id sorts LAST. My first attempt gave the
	// 4-game entity the id "aaa", which sorts FIRST -- so both tiebreaks put it first and a
	// mutation deleting the game-count tiebreak changed nothing. Hence "zzz" has the 4 games.
	const betterID, worseID = "zzz", "aaa"

	// 3-1 for zzz (three wins and one loss) and 1-0 for aaa.
	require.NoError(t, l.Record(Vote{Voter: "v0", Left: betterID, Right: "x", Winner: betterID}))
	require.NoError(t, l.Record(Vote{Voter: "v1", Left: betterID, Right: "y", Winner: betterID}))
	require.NoError(t, l.Record(Vote{Voter: "v2", Left: betterID, Right: "z", Winner: betterID}))
	require.NoError(t, l.Record(Vote{Voter: "v3", Left: betterID, Right: "w", Winner: "w"}),
		"zzz's one loss -- without it the record is 3-0, which smooths to 0.800, not 2/3")
	require.NoError(t, l.Record(Vote{Voter: "v4", Left: worseID, Right: "y", Winner: worseID}))

	better, err := l.RatingOf(betterID)
	require.NoError(t, err)
	worse, err := l.RatingOf(worseID)
	require.NoError(t, err)

	require.Equal(t, worse.Rating, better.Rating,
		"1-0 and 3-1 both smooth to exactly 2/3 ((w+1)/(g+2)), so the win ratio cannot break "+
			"this tie -- only the game count can")
	require.InDelta(t, 2.0/3.0, better.Rating, 1e-12, "and the shared value is exactly 2/3")
	require.Greater(t, better.Votes, worse.Votes, "zzz is the better-attested record")
	require.True(t, worseID < betterID,
		"PRECONDITION: the better-attested entity's id must sort LAST. If the ids agreed with the "+
			"game counts, this test would pass with the game-count tiebreak deleted")

	board, err := l.Board(context.Background(), 10)
	require.NoError(t, err)
	betterAt, worseAt := -1, -1
	for i, e := range board {
		switch e.PublicID {
		case betterID:
			betterAt = i
		case worseID:
			worseAt = i
		}
	}
	require.NotEqual(t, -1, betterAt, "both must be on the board")
	require.NotEqual(t, -1, worseAt)
	assert.Less(t, betterAt, worseAt,
		"at an equal rating the better-attested entity leads, even though its id sorts last; "+
			"without the game-count tiebreak the order falls to the id, a single lucky win "+
			"outranks a sustained record, and that is the failure a derived rating exists to "+
			"prevent")
}

// (L) REAL. PolicyFor handed out a slice ALIASING stored state. The test that would have caught
// it did not exist, so the copy line was untested -- and it is the same hazard as returning a
// loop variable, silent until two callers share state.
func TestTheReturnedPolicyDoesNotAliasTheStoredHolders(t *testing.T) {
	p := NewPreservation()
	require.NoError(t, p.Contribute(Contribution{
		Contributor: "peer-a", SubjectID: "scene-1", SubjectShare: OptedIn, ReplicaCount: 3,
	}))

	require.NoError(t, p.AcceptReplica("scene-1", "peer-b"),
		"a holder must exist FIRST: appending to a zero-length slice reallocates, so with an "+
			"empty Holders the aliased slice and the stored one are indistinguishable. My first "+
			"version read the policy while Holders was empty and passed with the copy removed.")

	pol, err := p.PolicyFor("scene-1")
	require.NoError(t, err)
	require.Len(t, pol.Holders, 1, "the accepted replica is visible in the policy")

	require.NoError(t, p.AcceptReplica("scene-1", "peer-c"))
	assert.Len(t, pol.Holders, 1,
		"the policy handed out BEFORE the second replica was accepted must not grow underneath "+
			"its caller; if it does, PolicyFor returned the stored slice, and a caller appending "+
			"to Holders would write a holder straight into the protocol")

	// THE ABOVE PASSES EVEN WITH THE COPY REMOVED, and the reason is Go's append: Holders is
	// built one element at a time from nil, so len == cap and appending reallocates instead of
	// writing through. The aliasing only becomes observable once the stored slice has SPARE
	// CAPACITY, which is what this asserts directly. I could not reach that state through the
	// public API -- there is no bulk accept -- so I build it by hand, which is the one thing a
	// package-internal test is for.
	stored := p.policies["scene-1"]
	require.NotNil(t, stored, "sanity: the policy exists")
	stored.Holders = append(make([]string, 0, 4), "peer-b")
	require.Less(t, len(stored.Holders), cap(stored.Holders),
		"the stored Holders now has spare capacity, which is the precondition for aliasing to "+
			"be visible at all")

	// RE-FETCH, because the slice handed out BEFORE the spare capacity existed was built by the
	// old append and has its own array. It is the FRESHLY returned slice that would alias, and
	// my first attempt here appended to the stale one -- so it could not see the aliasing and
	// the mutant survived again.
	fresh, err := p.PolicyFor("scene-1")
	require.NoError(t, err)
	require.Equal(t, 1, len(fresh.Holders))
	require.Less(t, len(stored.Holders), cap(stored.Holders),
		"the fetched copy must still be able to tell spare capacity from the store")

	//
	// AND HERE IS WHY THE OBVIOUS ASSERTION STILL MISSES IT, which took a probe to find. With
	// the copy removed, fresh.Holders and stored.Holders are the SAME array (cap 4, same
	// backing). But `fresh.Holders = append(fresh.Holders, ...)` writes index 1 of that array
	// and only extends FRESH's len -- stored.Holders keeps len 1, so `assert.Len(stored, 1)`
	// passes. The write is real; it is simply invisible from the stored slice's own length.
	//
	// The observable is the CONTENT at a shared index, which requires reading past the stored
	// slice's length. That is the only way to see a write that happened through an alias.
	fresh.Holders = append(fresh.Holders, "peer-forged")
	underlying := stored.Holders[:cap(stored.Holders)]
	assert.Equal(t, []string{"peer-b"},
		underlying[:1],
		"sanity: index 0 is the store's own entry")
	assert.NotContains(t, underlying,
		"peer-forged",
		"a value appended to what PolicyFor returned must not be present ANYWHERE in the "+
			"store's backing array; with the copy removed it is written at a shared index and "+
			"only the store's own len hides it")

	// And the same array identity, asserted directly -- this is the property the copy exists to
	// guarantee, and it is what makes every future holder count trustworthy.
	fresh2, err := p.PolicyFor("scene-1")
	require.NoError(t, err)
	require.NotEmpty(t, fresh2.Holders)
	assert.NotSame(t, &fresh2.Holders[0], &stored.Holders[0],
		"PolicyFor must hand out a slice with its own backing array; sharing one is what lets a "+
			"caller's append write into the protocol")
}
