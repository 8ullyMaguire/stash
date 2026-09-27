package collab

import "testing"

// Tests for the weighted decision function.
//
// The load-bearing property across this file: the weighted path and the flat
// path must agree on precedence. A disagreement about whether a withdrawn
// proposal can be approved is a real bug — it resurrects something an author
// took back — so the two are tested against the same table rather than
// separately.

// weightedPolicy is the M2b configuration: weighting on, damping on, three
// distinct voters required.
func weightedPolicy(threshold int) Policy {
	return Policy{
		QuorumThreshold: 3, // flat path still present, unused when Weighted is on
		MinVoters:       3,
		Weighted: WeightedPolicy{
			Threshold: threshold,
			Decay:     DefaultDecay(),
			Sybil:     DefaultSybil(),
		},
	}
}

func TestEvaluateWeightedPrecedenceMatchesFlatPath(t *testing.T) {
	// The same precedence rules, run through both arithmetic paths. A
	// disagreement here means one path resurrects a withdrawn proposal or
	// ignores a moderator ruling.
	established := func(id int) WeightBasis { return basis(id, 1, 100) }

	tests := []struct {
		name  string
		in    WeightedInput
		want  Decision
	}{
		{
			name: "withdrawn is rejected before any arithmetic",
			in: WeightedInput{
				Votes:     []WeightBasis{established(1), established(2), established(3)},
				Withdrawn: true,
			},
			want: DecisionRejected,
		},
		{
			name: "superseded is rejected before any arithmetic",
			in: WeightedInput{
				Votes:      []WeightBasis{established(1), established(2), established(3)},
				Superseded: true,
			},
			want: DecisionRejected,
		},
		{
			// The one that matters most: a moderator clicking accept on
			// something the author withdrew is a bug, not a rescue.
			name: "moderator cannot resurrect a withdrawn proposal",
			in: WeightedInput{
				Votes:             []WeightBasis{established(1)},
				Withdrawn:         true,
				ModeratorApproved: true,
			},
			want: DecisionRejected,
		},
		{
			name: "moderator approval is decisive without any votes",
			in: WeightedInput{
				ModeratorApproved: true,
			},
			want: DecisionAccepted,
		},
	}

	// Threshold high enough that only the terminal and moderator rules can fire.
	p := weightedPolicy(1_000_000)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateWeighted(p, tc.in)
			if got.Decision != tc.want {
				t.Errorf("EvaluateWeighted = %v, want %v (reason: %s)",
					got.Decision, tc.want, got.Reason)
			}

			// And the flat path must agree. A bug in one is a bug in both.
			flat := Evaluate(p, flatVoteCount(tc.in))
			if flat != tc.want {
				t.Errorf("flat Evaluate = %v, want %v; the two paths must agree "+
					"on precedence or one of them is wrong", flat, tc.want)
			}
		})
	}
}

func TestEvaluateWeightedThreshold(t *testing.T) {
	// Ballots +1, -1, +1 from three established voters.
	//
	// The Sybil factor applies to runs of IDENTICAL values, and the -1 breaks
	// the run, so the two +1 ballots are each the first of their own group and
	// undamped:
	//
	//	For     = 10000 + 10000        = 20000
	//	Against = 10000                (a run of one, undamped)
	//	Net     = 10000
	//
	// (The first version of this test put 20000 here and wrote a comment
	// working out why, and was wrong about the damping. The comment was the
	// tell: a fixture whose arithmetic you have to argue about is one that will
	// be argued about again. The number is now derived above, and the test
	// asserts the tally as well as the decision so the derivation is checked
	// rather than trusted.)
	three := []WeightBasis{basis(1, 1, 100), basis(2, -1, 100), basis(3, 1, 100)}

	tests := []struct {
		name      string
		threshold int
		want      Decision
	}{
		{"threshold above the tally stays pending", 25000, DecisionPending},
		{"threshold at the tally accepts", 10000, DecisionAccepted},
		{"threshold below the tally accepts", 5000, DecisionAccepted},
	}
	p := Policy{MinVoters: 3}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p.Weighted = WeightedPolicy{Threshold: tc.threshold}
			got := EvaluateWeighted(p, WeightedInput{Votes: three})
			if got.Decision != tc.want {
				t.Errorf("threshold %d: decision %v, want %v (tally net %d, reason %q)",
					tc.threshold, got.Decision, tc.want, got.Tally.Net, got.Reason)
			}
			// Pin the tally the thresholds above are chosen against. Without
			// this the fixture could drift — a change to the damping constants
			// would silently move every threshold with it, and the test would
			// still pass on the assertion above.
			if got.Tally.Net != 10000 {
				t.Errorf("tally net = %d, want 10000; the thresholds in this "+
					"table are chosen against that number", got.Tally.Net)
			}
		})
	}
}

// TestEvaluateWeightedMinVotersStillApplies: weighting adds a second defence,
// it does not replace the distinct-voter floor. Dropping this would re-open the
// hole MinVoters exists to close, in the path that is now trusted more.
func TestEvaluateWeightedMinVotersStillApplies(t *testing.T) {
	// One very well-regarded voter, well over any sane threshold.
	votes := []WeightBasis{basis(1, 1, 10000)}
	p := Policy{
		MinVoters: 3,
		Weighted:  WeightedPolicy{Threshold: 1000},
	}

	got := EvaluateWeighted(p, WeightedInput{Votes: votes})
	if got.Decision != DecisionPending {
		t.Errorf("decision %v; a single voter must not settle a proposal, "+
			"however high their reputation", got.Decision)
	}
	if got.Tally.Net < 1000 {
		t.Errorf("tally net %d, expected the single voter to clear the threshold "+
			"so the voter-count guard is what blocked it", got.Tally.Net)
	}
	if got.Tally.Voters != 1 {
		t.Errorf("Voters = %d, want 1", got.Tally.Voters)
	}
}

// TestEvaluateWeightedSelfVoteIsExcluded is the exclusion, end to end.
func TestEvaluateWeightedSelfVoteIsExcluded(t *testing.T) {
	// Author (id 1) plus two others, all established, all in favour. The
	// author's own ballot must not contribute.
	votes := []WeightBasis{basis(1, 1, 100), basis(2, 1, 100), basis(3, 1, 100)}

	p := weightedPolicy(1)
	without := EvaluateWeighted(p, WeightedInput{Votes: votes, AuthorID: 1})
	with := EvaluateWeighted(p, WeightedInput{Votes: votes, AuthorID: 1, AllowSelfAccept: true})

	if without.Tally.Net >= with.Tally.Net {
		t.Errorf("excluding the author gave net %d, including it gave %d; the "+
			"author's ballot must be subtracted", without.Tally.Net, with.Tally.Net)
	}
	// The distinct-voter count must NOT drop when the author is excluded: an
	// author who voted is still a person who turned up, and dropping them would
	// make MinVoters a tool for excluding authors.
	if without.Tally.Voters != 3 {
		t.Errorf("Voters = %d after excluding the author's ballot, want 3; "+
			"excluding a ballot must not exclude the voter", without.Tally.Voters)
	}
}

// TestSelfAcceptDoesNotBypassTheThreshold: the flag removes the exclusion, not
// the rule. Same as the flat path, and for the same reason.
func TestSelfAcceptDoesNotBypassTheThreshold(t *testing.T) {
	// The author alone, with AllowSelfAccept. One voter, so MinVoters blocks it
	// regardless of weight.
	p := weightedPolicy(1)
	got := EvaluateWeighted(p, WeightedInput{
		Votes:           []WeightBasis{basis(1, 1, 100)},
		AuthorID:        1,
		AllowSelfAccept: true,
	})
	if got.Decision != DecisionPending {
		t.Errorf("decision %v; AllowSelfAccept must remove the exclusion, not the "+
			"distinct-voter floor", got.Decision)
	}
}

// TestWeightedThresholdZeroFallsBackToFlat is the fallback proof, from the other
// direction: a zero weighted threshold must reach M2's arithmetic and produce
// M2's answer.
func TestWeightedThresholdZeroFallsBackToFlat(t *testing.T) {
	// Flat: net 3 from 3 voters accepts.
	// Weighted path off, so this must accept on the flat rule alone.
	p := Policy{QuorumThreshold: 3, MinVoters: 3}
	in := WeightedInput{Votes: []WeightBasis{basis(1, 1, 1), basis(2, 1, 1), basis(3, 1, 1)}}

	got := EvaluateWeighted(p, in)
	if got.Decision != DecisionAccepted {
		t.Errorf("decision %v, want accepted; with Weighted.Threshold == 0 the "+
			"flat path must decide, and flat net 3 / 3 voters accepts", got.Decision)
	}

	// And a case the flat path rejects, to prove it is really the flat rule
	// running rather than the weighted one being lenient.
	p.QuorumThreshold = 5
	got = EvaluateWeighted(p, in)
	if got.Decision != DecisionPending {
		t.Errorf("decision %v, want pending; flat net 3 is below threshold 5", got.Decision)
	}
}

// TestBothPathsDisabledIsModeratorOnly: the fully manual mode must accept
// nothing on its own.
func TestBothPathsDisabledIsModeratorOnly(t *testing.T) {
	p := Policy{QuorumThreshold: 0, MinVoters: 3}
	in := WeightedInput{Votes: []WeightBasis{basis(1, 1, 100), basis(2, 1, 100), basis(3, 1, 100)}}

	got := EvaluateWeighted(p, in)
	if got.Decision != DecisionPending {
		t.Errorf("decision %v, want pending; with both thresholds zero nothing "+
			"should settle without a moderator", got.Decision)
	}
}

// TestWeightedDecisionExplainsItself: a governance system that cannot say why
// is one nobody can debug or appeal.
func TestWeightedDecisionExplainsItself(t *testing.T) {
	p := weightedPolicy(1_000_000)
	got := EvaluateWeighted(p, WeightedInput{Votes: []WeightBasis{basis(1, 1, 100)}})

	if got.Reason == "" {
		t.Error("a pending decision has no reason; a moderation view would show " +
			"a spinner instead of an explanation")
	}
	// The tally and weights must be populated on a non-terminal decision, since
	// they are what the reason is derived from.
	if got.Tally.Voters != 1 {
		t.Errorf("Voters = %d, want 1", got.Tally.Voters)
	}
	if len(got.Weights) != 1 {
		t.Errorf("got %d weights for 1 vote", len(got.Weights))
	}

	// An accepted decision has nothing to explain.
	ok := weightedPolicy(1)
	accepted := EvaluateWeighted(ok, WeightedInput{
		Votes: []WeightBasis{basis(1, 1, 100), basis(2, 1, 100), basis(3, 1, 100)},
	})
	if accepted.Decision == DecisionAccepted && accepted.Reason != "" {
		t.Errorf("accepted decision carries reason %q; it should carry none", accepted.Reason)
	}
}

// TestFlaggedBallotsAreNotSubtracted: the spec requires coordinated patterns be
// FLAGGED, not silently punished. If flagged ballots were removed from the
// tally, the flag would be doing the punishing.
func TestFlaggedBallotsAreNotSubtracted(t *testing.T) {
	// Six identical ballots from users with no reputation on this field: enough
	// to trip the correlation threshold.
	var votes []WeightBasis
	for i := 1; i <= 6; i++ {
		votes = append(votes, basis(i, 1, 0))
	}

	p := Policy{
		MinVoters: 3,
		Weighted: WeightedPolicy{
			Threshold: 1000,
			Sybil:     DefaultSybil(),
		},
	}
	got := EvaluateWeighted(p, WeightedInput{Votes: votes})

	if len(got.Flagged) == 0 {
		t.Error("six identical zero-reputation ballots were not flagged")
	}
	// They must still be counted, just damped.
	if got.Tally.Voters != 6 {
		t.Errorf("Voters = %d, want 6; flagging must not remove a voter from "+
			"the tally", got.Tally.Voters)
	}
	if got.Tally.Damped == 0 {
		t.Error("Damped = 0; the run should have been damped even though it " +
			"was flagged")
	}
}

// TestNoVotesIsPending: an empty ballot set must not accept under any
// configuration. A zero-tally bug that reads as "no opposition" is the classic
// way a governance system fails open.
func TestNoVotesIsPending(t *testing.T) {
	for _, threshold := range []int{0, 1, 1000} {
		p := Policy{QuorumThreshold: 3, MinVoters: 3, Weighted: WeightedPolicy{Threshold: threshold}}
		got := EvaluateWeighted(p, WeightedInput{})
		if got.Decision != DecisionPending {
			t.Errorf("threshold %d with no votes: decision %v, want pending",
				threshold, got.Decision)
		}
	}
}

func TestFlatThresholdAsWeighted(t *testing.T) {
	// Documented as a migration aid, not an equivalence. These assertions pin
	// the arithmetic so the doc comment cannot drift from the code.
	if got := FlatThresholdAsWeighted(0, 100); got != 0 {
		t.Errorf("a disabled flat threshold must convert to 0, got %d", got)
	}
	if got := FlatThresholdAsWeighted(3, 100); got != 30000 {
		t.Errorf("flat 3 with established voters = %d, want 30000", got)
	}
	// The whole reason the conversion is documented rather than automatic: a
	// fresh-voter instance and an established-voter instance need DIFFERENT
	// thresholds for the same effective quorum, and the difference is 4x.
	fresh := FlatThresholdAsWeighted(3, 0)
	established := FlatThresholdAsWeighted(3, 100)
	if established <= fresh {
		t.Errorf("established threshold %d should exceed fresh threshold %d; if "+
			"they were equal the conversion would be a false equivalence",
			established, fresh)
	}
}

func TestNormalisedClampsWeightedThreshold(t *testing.T) {
	// A negative threshold means "accept immediately" to a careless reader and
	// must not mean that.
	w := WeightedPolicy{Threshold: -100, Decay: DecayPolicy{Factor: 3}, Sybil: SybilPolicy{GroupWindow: 0}}
	got := w.Normalised()

	if got.Threshold != 0 {
		t.Errorf("Threshold = %d, want 0", got.Threshold)
	}
	if got.Decay.Factor != DefaultDecay().Factor {
		t.Errorf("Decay.Factor = %v, want %v", got.Decay.Factor, DefaultDecay().Factor)
	}
	if got.Sybil.GroupWindow < 2 {
		t.Errorf("Sybil.GroupWindow = %d, want at least 2", got.Sybil.GroupWindow)
	}
}
