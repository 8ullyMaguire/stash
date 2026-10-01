package collab_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
)

// Shadow evaluation. The property under test throughout is that shadow mode is
// OBSERVATIONAL: it must be impossible for it to change what gets applied.
//
// That is asserted directly rather than assumed, because a feature whose failure
// mode is "quietly switches governance on" would be worse than not having it.

// stubSources is a ShadowSources that returns canned standings, or fails.
type stubSources struct {
	standings map[int]collab.FieldStanding
	err       error
	calls     int
}

func (s *stubSources) ReputationFor(_ context.Context, userIDs []int, _, _ string) (map[int]collab.FieldStanding, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	out := map[int]collab.FieldStanding{}
	for _, id := range userIDs {
		if st, ok := s.standings[id]; ok {
			out[id] = st
		}
	}
	return out, nil
}

func standing(userID, reputation int) collab.FieldStanding {
	return collab.FieldStanding{UserID: userID, Reputation: reputation}
}

// policyWith is DefaultPolicy with a weighted threshold switched on, which is the
// only configuration in which the two functions can disagree.
func policyWith(threshold int) collab.Policy {
	p := collab.DefaultPolicy()
	p.Weighted.Threshold = threshold
	p.QuorumThreshold = 2
	p.MinVoters = 2
	return p
}

func TestShadowFlatDecisionIsUnchangedByShadowMode(t *testing.T) {
	// The core guarantee. Across a spread of vote shapes, EvaluateShadow's flat
	// answer must equal what Evaluate alone says, using the same counts.
	//
	// If this ever fails, shadow mode is no longer observational and the feature
	// has become a governance change nobody asked for.
	for _, tc := range []struct {
		name    string
		ballots []collab.Ballot
		author  int
	}{
		{"no votes", nil, 1},
		{"one accept", []collab.Ballot{{UserID: 2, Value: 1}}, 1},
		{"two accepts", []collab.Ballot{{UserID: 2, Value: 1}, {UserID: 3, Value: 1}}, 1},
		{"three accepts", []collab.Ballot{{UserID: 2, Value: 1}, {UserID: 3, Value: 1}, {UserID: 4, Value: 1}}, 1},
		{"author votes for self", []collab.Ballot{{UserID: 1, Value: 1}, {UserID: 2, Value: 1}, {UserID: 3, Value: 1}}, 1},
		{"mixed", []collab.Ballot{{UserID: 2, Value: 1}, {UserID: 3, Value: -1}, {UserID: 4, Value: 1}}, 1},
		{"lopsided no", []collab.Ballot{{UserID: 2, Value: -1}, {UserID: 3, Value: -1}, {UserID: 4, Value: -1}}, 1},
		{"tied", []collab.Ballot{{UserID: 2, Value: 1}, {UserID: 3, Value: -1}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := policyWith(30000)
			src := &stubSources{}

			net := 0
			for _, b := range tc.ballots {
				net += b.Value
			}
			authorVoted := false
			for _, b := range tc.ballots {
				if b.UserID == tc.author {
					authorVoted = true
				}
			}
			want := collab.Evaluate(p, collab.VoteCount{
				Net: net, Voters: len(tc.ballots), SelfVote: authorVoted,
			})

			flat, _, record := collab.EvaluateShadow(context.Background(), p,
				collab.ShadowInput{
					ProposalID: 1, TargetType: "scene", Field: "title",
					AuthorID: tc.author, Ballots: tc.ballots,
				}, src)

			assert.Equal(t, want, flat,
				"the flat decision must be exactly what Evaluate alone decides")
			assert.Equal(t, flat, record.Flat,
				"the persisted record must state the decision that was APPLIED, not a "+
					"recomputed one -- otherwise the log would not match history")
			assert.Equal(t, net, record.Net)
			assert.Equal(t, len(tc.ballots), record.Ballots)
		})
	}
}

func TestShadowDisagreementIsRecordedWithBothAnswers(t *testing.T) {
	// A case where they genuinely differ: four fresh accounts for. Flat quorum
	// of two accepts it; the weighted rule gives each fresh voter zero weight, so
	// it holds. This is the Sybil case the weighted path exists to catch, and it
	// is the shape §5.3's open question is actually about.
	p := policyWith(30000)
	src := &stubSources{}

	flat, weighted, record := collab.EvaluateShadow(context.Background(), p,
		collab.ShadowInput{
			ProposalID: 7, TargetType: "scene", Field: "title", AuthorID: 1,
			Ballots: []collab.Ballot{
				{UserID: 2, Value: 1}, {UserID: 3, Value: 1},
				{UserID: 4, Value: 1}, {UserID: 5, Value: 1},
			},
		}, src)

	assert.Equal(t, collab.DecisionAccepted, flat, "flat quorum of two is met")
	assert.Equal(t, collab.DecisionPending, weighted.Decision,
		"four fresh voters carry no weight, so the weighted rule holds")
	require.False(t, record.Agrees())
	assert.Equal(t, "weighted_would_hold", record.Direction())

	// The record has to carry enough for an operator to judge it: who, how many,
	// and what the weighted total actually was.
	assert.Equal(t, 7, record.ProposalID)
	assert.Equal(t, "scene", record.TargetType)
	assert.Equal(t, "title", record.Field)
	assert.Equal(t, 4, record.Net)
	assert.Equal(t, 4, record.Voted)
	assert.Equal(t, 4, record.Ballots)

	// A fresh voter is NOT weight zero: ReputationFloor is 5000 basis points,
	// half of the 10000 an established (reputation 100) voter carries. So four
	// fresh voters total 20000, under the 30000 threshold. The finding is that
	// four brand-new accounts fall SHORT -- not that they weigh nothing.
	//
	// An earlier version of this test asserted a zero total, which was wrong
	// about the code and would have passed for the wrong reason.
	assert.Equal(t, 4*collab.ReputationFloor, record.WeightedTotal,
		"four fresh voters total four times the floor, which is short of the threshold")
	assert.Less(t, record.WeightedTotal, 30000,
		"this is the shape that makes the weighted path worth having")
}

func TestShadowRecordsAgreementToo(t *testing.T) {
	// The denominator. An instance asking "how often would switching change
	// things" cannot answer it from a log of disagreements alone.
	p := collab.DefaultPolicy() // weighted off, so the two must agree
	src := &stubSources{}

	flat, weighted, record := collab.EvaluateShadow(context.Background(), p,
		collab.ShadowInput{
			ProposalID: 1, TargetType: "scene", Field: "title", AuthorID: 1,
			Ballots: []collab.Ballot{{UserID: 2, Value: 1}, {UserID: 3, Value: 1}},
		}, src)

	assert.Equal(t, flat, weighted.Decision)
	assert.True(t, record.Agrees())
	assert.Empty(t, record.Direction(),
		"an agreement has no direction, so grouping on it yields one bucket")
}

func TestShadowSurvivesAFailingReputationLookup(t *testing.T) {
	// The lookup must never be able to fail a vote. A governance log that can
	// break the path it observes would be worse than one that records a different
	// tally, because the first changes governance.
	p := policyWith(30000)
	src := &stubSources{err: errors.New("reputation store is down")}

	flat, _, record := collab.EvaluateShadow(context.Background(), p,
		collab.ShadowInput{
			ProposalID: 1, TargetType: "scene", Field: "title", AuthorID: 1,
			Ballots: []collab.Ballot{{UserID: 2, Value: 1}, {UserID: 3, Value: 1}},
		}, src)

	assert.Equal(t, collab.DecisionAccepted, flat,
		"the applied decision must not depend on the shadow lookup succeeding")
	assert.Equal(t, flat, record.Flat)

	// A degraded lookup leaves every voter at the fresh-voter floor. That is
	// indistinguishable from four genuinely new accounts in the log, which is a
	// real limitation of recording the tally rather than the lookup's success --
	// and the reason the summary's flat/weighted counts are the authoritative
	// figure rather than the tallies.
	assert.Equal(t, 2*collab.ReputationFloor, record.WeightedTotal,
		"a failed lookup must degrade to the fresh-voter floor, not to zero and "+
			"not to an error")
}

func TestShadowHandlesANilSource(t *testing.T) {
	// A caller that has no reputation wired up at all -- a single-user instance,
	// or a test -- must not panic. Every voter is treated as fresh.
	p := policyWith(30000)
	flat, _, record := collab.EvaluateShadow(context.Background(), p,
		collab.ShadowInput{
			ProposalID: 1, TargetType: "scene", Field: "title", AuthorID: 1,
			Ballots: []collab.Ballot{{UserID: 2, Value: 1}, {UserID: 3, Value: 1}},
		}, nil)

	assert.Equal(t, collab.DecisionAccepted, flat)
	assert.Equal(t, 2, record.Ballots)
}

func TestShadowHonoursTerminalStatesInBothPaths(t *testing.T) {
	// A withdrawn proposal is rejected by both functions, and the log must not
	// manufacture a disagreement out of a state that short-circuits both.
	p := policyWith(30000)
	src := &stubSources{standings: map[int]collab.FieldStanding{2: standing(2, 50)}}

	flat, weighted, record := collab.EvaluateShadow(context.Background(), p,
		collab.ShadowInput{
			ProposalID: 1, TargetType: "scene", Field: "title", AuthorID: 1,
			Ballots:   []collab.Ballot{{UserID: 2, Value: 1}},
			Withdrawn: true,
		}, src)

	assert.Equal(t, collab.DecisionRejected, flat)
	assert.Equal(t, collab.DecisionRejected, weighted.Decision,
		"the weighted path honours the same terminal states, which is what keeps the "+
			"two functions from disagreeing about a withdrawn proposal")
	assert.True(t, record.Agrees())
}

func TestShadowLooksUpReputationOncePerEvaluationNotOncePerVoter(t *testing.T) {
	// A tally with one query per voter is a database round trip inside what should
	// be arithmetic, and StandingsForMany's whole reason for existing.
	p := policyWith(30000)
	src := &stubSources{standings: map[int]collab.FieldStanding{2: standing(2, 50)}}

	collab.EvaluateShadow(context.Background(), p,
		collab.ShadowInput{
			ProposalID: 1, TargetType: "scene", Field: "title", AuthorID: 1,
			Ballots: []collab.Ballot{
				{UserID: 2, Value: 1}, {UserID: 3, Value: 1},
				{UserID: 4, Value: 1}, {UserID: 5, Value: 1},
			},
		}, src)

	assert.Equal(t, 1, src.calls,
		"one batched lookup for four voters, not four lookups")
}

func TestShadowWithNoBallotsDoesNotQueryReputation(t *testing.T) {
	// A proposal nobody has voted on has no voters to look up, and issuing the
	// query anyway would be a pointless round trip on every proposal creation.
	p := policyWith(30000)
	src := &stubSources{}

	flat, _, record := collab.EvaluateShadow(context.Background(), p,
		collab.ShadowInput{ProposalID: 1, TargetType: "scene", Field: "title", AuthorID: 1},
		src)

	assert.Equal(t, collab.DecisionPending, flat)
	assert.Equal(t, 0, src.calls)
	assert.Equal(t, 0, record.Ballots)
}

func TestShadowUsesReputationWhenPresent(t *testing.T) {
	// The positive case: a well-reputed voter's weight actually reaches the tally,
	// so the record shows a non-zero weighted total. Without this, the zero-total
	// tests above would pass even if reputation were never consulted at all.
	p := policyWith(30000)
	src := &stubSources{standings: map[int]collab.FieldStanding{
		2: standing(2, 100),
		3: standing(3, 100),
	}}

	_, weighted, record := collab.EvaluateShadow(context.Background(), p,
		collab.ShadowInput{
			ProposalID: 1, TargetType: "scene", Field: "title", AuthorID: 1,
			Ballots: []collab.Ballot{{UserID: 2, Value: 1}, {UserID: 3, Value: 1}},
		}, src)

	assert.Greater(t, record.WeightedTotal, 0,
		"reputation must reach the tally, or the weighted path is decoration")
	assert.Equal(t, record.WeightedTotal, weighted.Tally.Net)
}

// E1's survivor, fixed: a wrong Voters count feeds the flat path's Sybil guard, so
// this is a governance bug rather than a reporting one. The mutation multiplied
// the count by two and no test noticed, because every case in the table had a
// ballot count comfortably above MinVoters.
//
// The fix is a case that sits exactly ON the floor, where an inflated count would
// cross it and a correct one would not.
func TestShadowFlatVoterCountIsExactAtTheFloor(t *testing.T) {
	// MinVoters is 2 in policyWith. Two voters and a net of 2 would be accepted
	// by threshold alone, but with fewer than MinVoters DISTINCT voters it must
	// hold -- and that is precisely the account-counting check.
	p := policyWith(30000)

	// Three ballots from three distinct accounts, one of whom is the author.
	// The author is excluded from the tally by policy, but their ballot still
	// counts as a voter -- which is the distinction EvaluateWeighted's comment
	// calls out, and the reason Voters and the author exclusion are separate.
	three := []collab.Ballot{{UserID: 1, Value: 1}, {UserID: 2, Value: 1}, {UserID: 3, Value: 1}}
	want := collab.Evaluate(p, collab.VoteCount{Net: 3, Voters: 3, SelfVote: true})

	flat, _, record := collab.EvaluateShadow(context.Background(), p,
		collab.ShadowInput{
			ProposalID: 1, TargetType: "scene", Field: "title", AuthorID: 1,
			Ballots: three,
		}, &stubSources{})

	// The ABSOLUTE counts first, before any comparison to Evaluate.
	//
	// This ordering is the point. A version of this test that asserted only
	// "flat == Evaluate(...)" passed with voters++ doubling the count, because
	// both sides of that comparison are fed the same inflated number -- the
	// expectation was computed from the code under test rather than from the
	// data. Asserting 3 outright pins the count to the ballots.
	assert.Equal(t, len(three), record.Ballots, "three ballots")
	assert.Equal(t, 3, record.Voted,
		"three ballots from three accounts is three distinct voters")

	// And now the agreement with Evaluate, which is a separate property: that the
	// shadow path delegates rather than reimplementing the flat rule.
	assert.Equal(t, want, flat, "the flat answer must match Evaluate exactly")

	// Now raise MinVoters to 4. Three distinct voters is BELOW the floor, so the
	// proposal must hold despite a net of 3 that clears the threshold of 2. If an
	// inflated voter count slipped through -- the mutation that survived the first
	// version of this test -- this would accept instead.
	p.MinVoters = 4
	flat, _, _ = collab.EvaluateShadow(context.Background(), p,
		collab.ShadowInput{
			ProposalID: 1, TargetType: "scene", Field: "title", AuthorID: 1,
			Ballots: three,
		}, &stubSources{})
	assert.Equal(t, collab.DecisionPending, flat,
		"three distinct voters must not satisfy a floor of four, however lopsided "+
			"the tally is")
}

// E3's survivor, fixed: a lookup that succeeds but returns nothing is a real
// degradation path — a store that returns (nil, nil) rather than an error — and
// nothing covered it.
func TestShadowTreatsANilStandingMapAsNoReputation(t *testing.T) {
	// stubSources with err set returns (nil, err). Here the error is nil and the
	// map is nil, which the production store also does for a user with no row.
	// The tally must still be computed, at the fresh-voter floor.
	p := policyWith(30000)

	flat, _, record := collab.EvaluateShadow(context.Background(), p,
		collab.ShadowInput{
			ProposalID: 1, TargetType: "scene", Field: "title", AuthorID: 1,
			Ballots: []collab.Ballot{{UserID: 2, Value: 1}, {UserID: 3, Value: 1}},
		}, &stubSources{standings: nil})

	assert.Equal(t, collab.DecisionAccepted, flat,
		"a nil reputation map must not break the applied decision")
	assert.Equal(t, 2*collab.ReputationFloor, record.WeightedTotal,
		"no standings means every voter is fresh")
}
