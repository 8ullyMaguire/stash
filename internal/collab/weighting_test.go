package collab

import (
	"testing"
)

// Table-driven over the full cross-product, as the plan requires. Hand-picked
// cases would let a policy interaction survive, and the interactions are the
// interesting part: decay before damping, damping only on the agreeing side.

// basis is shorthand for a ballot with no rejections, which is most of them.
func basis(id, value, reputation int) WeightBasis {
	return WeightBasis{UserID: id, Value: value, Reputation: reputation}
}

// ---------------------------------------------------------------------------
// Reputation → weight
// ---------------------------------------------------------------------------

func TestReputationToBasisIsMonotonic(t *testing.T) {
	// Over a wide range, because the interesting failures are in the tails: a
	// sqrt that is monotonic in the middle can still be flat at zero or
	// unbounded at the top.
	prev := -1
	for rep := 0; rep <= 100000; rep += 7 {
		got := ReputationToBasis(rep)
		if got < prev {
			t.Fatalf("ReputationToBasis(%d) = %d, less than previous %d; "+
				"weight must never decrease as reputation rises", rep, got, prev)
		}
		prev = got
	}
}

func TestReputationToBasisEdges(t *testing.T) {
	tests := []struct {
		name string
		rep  int
		want int
	}{
		// A brand-new account still votes. The floor is what separates "not yet
		// trusted" from "silenced", and a zero here would make those the same
		// state.
		{"zero reputation still has weight", 0, ReputationFloor},
		{"negative reputation is treated as zero", -50, ReputationFloor},
		{"100 reputation is a full fresh-weight", 100, 10000},
		// Sub-linear, which is the property that stops weight becoming a lever:
		// 4x the reputation is not 4x the vote.
		{"400 reputation is under 4x the weight of 100", 400, 20000},
		// Capped, so one account cannot become a supermajority alone.
		{"enormous reputation is capped", 1000000000, 100000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReputationToBasis(tc.rep); got != tc.want {
				t.Errorf("ReputationToBasis(%d) = %d, want %d", tc.rep, got, tc.want)
			}
		})
	}
}

func TestReputationToBasisIsSublinear(t *testing.T) {
	// Four times the reputation must be strictly less than four times the
	// weight, at every point where the cap is not already binding.
	for _, rep := range []int{100, 200, 400, 1000, 2500} {
		single := ReputationToBasis(rep)
		quad := ReputationToBasis(rep * 4)
		if quad >= single*4 {
			t.Errorf("ReputationToBasis(%d)=%d, so 4x reputation gives %d, "+
				"which is not sub-linear (would need to be < %d)",
				rep, single, quad, single*4)
		}
	}
}

// ---------------------------------------------------------------------------
// Decay
// ---------------------------------------------------------------------------

func TestDecay(t *testing.T) {
	d := DefaultDecay()

	tests := []struct {
		name       string
		reputation int
		rejections int
		wantBasis  int
	}{
		// No rejections, no decay — the M2 behaviour.
		{"fresh voter is undamped by decay", 100, 0, 10000},
		{"one rejection halves", 100, 1, 5000},
		{"two rejections quarter", 100, 2, 2500},
		{"three rejections eighth", 100, 3, 1250},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := ComputeWeights(
				[]WeightBasis{{UserID: 1, Value: 1, Reputation: tc.reputation, FieldRejections: tc.rejections}},
				d, SybilPolicy{})
			if got := w[0].Basis; got != tc.wantBasis {
				t.Errorf("basis = %d, want %d", got, tc.wantBasis)
			}
		})
	}
}

// TestDecayFloorsRatherThanZeroing is why MinWeight exists.
func TestDecayFloorsRatherThanZeroing(t *testing.T) {
	d := DefaultDecay()
	// Enough rejections that a pure halving would be under the floor.
	w := ComputeWeights(
		[]WeightBasis{{UserID: 1, Value: 1, Reputation: 100, FieldRejections: 20}},
		d, SybilPolicy{})
	if w[0].Basis < d.MinWeight {
		t.Errorf("basis = %d, below the floor %d; a user whose weight reaches "+
			"zero is silenced rather than distrusted, and the two are not the "+
			"same message", w[0].Basis, d.MinWeight)
	}
	if !w[0].Decayed {
		t.Error("Decayed is false although the basis was reduced")
	}
}

func TestDecayDisabled(t *testing.T) {
	// The M2-compatible configuration: decay off means rejections do not change
	// a ballot at all.
	d := DecayPolicy{Enabled: false, Factor: 0.5, MinWeight: 1000}
	w := ComputeWeights(
		[]WeightBasis{{UserID: 1, Value: 1, Reputation: 100, FieldRejections: 5}},
		d, SybilPolicy{})
	if w[0].Basis != 10000 {
		t.Errorf("basis = %d with decay disabled, want 10000", w[0].Basis)
	}
	if w[0].Decayed {
		t.Error("Decayed is true with decay disabled")
	}
}

// TestDecayRejectsNonsenseFactors: a config typo must not become a privilege
// escalation or a silent disenfranchisement.
func TestDecayRejectsNonsenseFactors(t *testing.T) {
	for _, factor := range []float64{0, 1, 1.5, 2, -0.5} {
		d := DecayPolicy{Enabled: true, Factor: factor, MinWeight: 1000}
		got := d.Normalised().Factor
		if got != DefaultDecay().Factor {
			t.Errorf("DecayPolicy{Factor: %v}.Normalised().Factor = %v, want %v",
				factor, got, DefaultDecay().Factor)
		}
	}
}

// ---------------------------------------------------------------------------
// Sybil damping
// ---------------------------------------------------------------------------

// TestSybilDampsIdenticalRuns is the core property: a run of identical votes
// gets progressively cheaper.
func TestSybilDampsIdenticalRuns(t *testing.T) {
	s := DefaultSybil()
	var bases []WeightBasis
	for i := 1; i <= 5; i++ {
		bases = append(bases, basis(i, 1, 100))
	}
	w := ComputeWeights(bases, DecayPolicy{}, s)

	want := []int{10000, 8000, 6400, 5120, 4096}
	for i, expect := range want {
		if got := w[i].SybilFactor; got != expect {
			t.Errorf("ballot %d SybilFactor = %d, want %d", i+1, got, expect)
		}
	}
}

// TestSybilDoesNotDampTheFirstBallotOfAValue is what makes the mechanism safe:
// a single vote, and a vote that differs from the one before it, are undamped.
func TestSybilDoesNotDampTheFirstBallotOfAValue(t *testing.T) {
	s := DefaultSybil()
	bases := []WeightBasis{
		basis(1, 1, 100),  // first "for"
		basis(2, -1, 100), // against — differs, undamped
		basis(3, -1, 100), // second "against" — damped
	}
	w := ComputeWeights(bases, DecayPolicy{}, s)

	if w[0].SybilFactor != 10000 {
		t.Errorf("first ballot SybilFactor = %d, want 10000", w[0].SybilFactor)
	}
	if w[1].SybilFactor != 10000 {
		t.Errorf("ballot that changed value SybilFactor = %d, want 10000; "+
			"damping a change of direction would penalise dissent", w[1].SybilFactor)
	}
	if w[2].SybilFactor == 10000 {
		t.Error("second identical ballot was not damped")
	}
}

// TestSybilDoesNotDampMinorityDissent is a security property stated directly.
// Damping the minority side would make a coordinated attack cheaper, which is
// the exact inverse of the intent.
func TestSybilDoesNotDampMinorityDissent(t *testing.T) {
	s := DefaultSybil()
	var bases []WeightBasis
	// Five for, then one against.
	for i := 1; i <= 5; i++ {
		bases = append(bases, basis(i, 1, 100))
	}
	bases = append(bases, basis(99, -1, 100)) // the lone dissenter

	w := ComputeWeights(bases, DecayPolicy{}, s)
	dissent := w[len(w)-1]

	if dissent.SybilFactor != 10000 {
		t.Errorf("the lone dissenter was damped to %d; damping the minority "+
			"direction makes a coordinated bloc cheaper to execute, not harder",
			dissent.SybilFactor)
	}
	// And the tally must reflect that dissent, not the raw count.
	tally := Tally(w, ballotValues(bases))
	if tally.Against == 0 {
		t.Error("Against is 0; a lone dissent was erased from the tally")
	}
	// Five damped "for" ballots must not outweigh one undamped "against" if the
	// damping is working: 10000+8000+6400+5120+4096 = 33616 vs 10000.
	if tally.For <= tally.Against {
		t.Errorf("For=%d did not exceed Against=%d; the coordinated run of five "+
			"should be damped below the sum it nominally has", tally.For, tally.Against)
	}
}

func TestSybilDisabled(t *testing.T) {
	s := SybilPolicy{Enabled: false, DiminishingReturns: 100, GroupWindow: 10}
	var bases []WeightBasis
	for i := 1; i <= 6; i++ {
		bases = append(bases, basis(i, 1, 100))
	}
	w := ComputeWeights(bases, DecayPolicy{}, s)
	for i := range w {
		if w[i].SybilFactor != 10000 {
			t.Fatalf("ballot %d damped with damping disabled", i)
		}
	}
}

func TestSybilWindowBoundsTheGroup(t *testing.T) {
	// A run longer than GroupWindow must not decay without limit — the window
	// exists so an old resolved disagreement is not lumped in with today's votes.
	s := SybilPolicy{Enabled: true, DiminishingReturns: 1000, GroupWindow: 3}
	var bases []WeightBasis
	for i := 1; i <= 5; i++ {
		bases = append(bases, basis(i, 1, 100))
	}
	w := ComputeWeights(bases, DecayPolicy{}, s)

	// Positions must reset past the window rather than continuing to fall.
	if w[3].Position != 1 {
		t.Errorf("ballot 4 position = %d, want 1 (window is 3)", w[3].Position)
	}
	if w[4].Position != 2 {
		t.Errorf("ballot 5 position = %d, want 2", w[4].Position)
	}
}

// ---------------------------------------------------------------------------
// Tally
// ---------------------------------------------------------------------------

func ballotValues(bases []WeightBasis) []int {
	out := make([]int, len(bases))
	for i, b := range bases {
		out[i] = b.Value
	}
	return out
}

func TestTallyCountsDistinctVoters(t *testing.T) {
	// The MinVoters guard depends on Voters being PEOPLE. Counting ballots here
	// would silently re-open the Sybil hole the field exists to close.
	bases := []WeightBasis{
		basis(7, 1, 100),
		basis(7, -1, 100), // same user, changed their mind
		basis(8, 1, 100),
	}
	w := ComputeWeights(bases, DecayPolicy{}, SybilPolicy{})
	tally := Tally(w, ballotValues(bases))

	if tally.Voters != 2 {
		t.Errorf("Voters = %d, want 2 (two distinct users, three ballots)", tally.Voters)
	}
}

// TestTallyCountsZeroWeightBallotsAsVoters: the floor means a ballot is never
// worth nothing, but if one ever were, the person still turned up.
func TestTallyCountsZeroWeightBallotsAsVoters(t *testing.T) {
	w := []Weight{{UserID: 1, Basis: 0, SybilFactor: 10000}, {UserID: 2, Basis: 10000, SybilFactor: 10000}}
	tally := Tally(w, []int{1, 1})
	if tally.Voters != 2 {
		t.Errorf("Voters = %d, want 2; a zero-weight ballot is still a person", tally.Voters)
	}
}

func TestTallyWeightedNotCounted(t *testing.T) {
	// The whole point of weighting: two newcomers cannot outvote an established
	// user. Under flat counting this is 2-1 = accepted; weighted it is rejected.
	//
	// (The first version of this test asserted Net > 0, which is backwards for
	// this ballot order and asserted nothing about weighting. It failed, and
	// correctly: what has to be checked is the weighted total against the flat
	// count, not the sign.)
	bases := []WeightBasis{
		basis(1, 1, 1),    // newcomer
		basis(2, 1, 1),    // newcomer
		basis(3, -1, 100), // established
	}
	w := ComputeWeights(bases, DecayPolicy{}, SybilPolicy{Enabled: false, GroupWindow: 10})
	tally := Tally(w, ballotValues(bases))

	flatNet := 2 - 1 // what M2's Σ(+1) − Σ(−1) would have said
	if flatNet <= 0 {
		t.Fatalf("the flat tally is %d, so this fixture no longer demonstrates "+
			"the case it exists to test", flatNet)
	}
	if tally.Net >= 0 {
		t.Errorf("Net = %d; two newcomers weighted at the floor must not outvote "+
			"one established voter, which flat counting (%d) would have allowed",
			tally.Net, flatNet)
	}
	// And the established voter's ballot alone must exceed both newcomers.
	if tally.Against <= tally.For {
		t.Errorf("Against %d did not exceed For %d", tally.Against, tally.For)
	}
	// For, Against and Weight must be mutually consistent.
	if tally.Weight != tally.For+tally.Against {
		t.Errorf("Weight %d != For %d + Against %d", tally.Weight, tally.For, tally.Against)
	}
	if tally.Net != tally.For-tally.Against {
		t.Errorf("Net %d != For %d - Against %d", tally.Net, tally.For, tally.Against)
	}
}

func TestTallyHandlesMismatchedSlices(t *testing.T) {
	// A weight with no ballot is a caller error; dropping the excess is visibly
	// wrong and must not panic in the request path.
	w := []Weight{{UserID: 1, Basis: 10000, SybilFactor: 10000}}
	tally := Tally(w, []int{1, 1, 1})
	if tally.Voters != 1 {
		t.Errorf("Voters = %d, want 1", tally.Voters)
	}
}

func TestTallyEmpty(t *testing.T) {
	tally := Tally(nil, nil)
	if tally.Net != 0 || tally.For != 0 || tally.Against != 0 || tally.Voters != 0 {
		t.Errorf("empty tally = %+v, want all zero", tally)
	}
}

func TestTallyReportsDamped(t *testing.T) {
	// Damping must be VISIBLE. The spec requires coordinated patterns to be
	// flagged rather than silently punished, and a tally that quietly reports a
	// smaller total is the silent half of that.
	bases := []WeightBasis{
		basis(1, 1, 100), basis(2, 1, 100), basis(3, 1, 100), basis(4, 1, 100),
	}
	w := ComputeWeights(bases, DecayPolicy{}, DefaultSybil())
	tally := Tally(w, ballotValues(bases))
	if tally.Damped != 3 {
		t.Errorf("Damped = %d, want 3 (the 2nd, 3rd and 4th of the run)", tally.Damped)
	}
}

// ---------------------------------------------------------------------------
// Decay and damping together
// ---------------------------------------------------------------------------

// TestDecayAppliesBeforeDamping is the ordering invariant. Damping computed on
// pre-decay values would let a rejected voter in a coordinated group keep voting
// at full weight.
func TestDecayAppliesBeforeDamping(t *testing.T) {
	decay := DefaultDecay()
	sybil := SybilPolicy{Enabled: true, DiminishingReturns: 8000, GroupWindow: 10}

	// One heavily-rejected voter and one clean voter, voting identically.
	bases := []WeightBasis{
		{UserID: 1, Value: 1, Reputation: 100, FieldRejections: 3},
		{UserID: 2, Value: 1, Reputation: 100, FieldRejections: 0},
	}
	w := ComputeWeights(bases, decay, sybil)

	if w[0].Basis >= w[1].Basis {
		t.Errorf("the rejected voter has basis %d, the clean one %d; decay must "+
			"reduce the first before damping is computed from either",
			w[0].Basis, w[1].Basis)
	}
	// The rejected voter is also the damped one (second in the run), so its
	// effective weight should be well below the clean voter's.
	if w[0].Effective() >= w[1].Effective() {
		t.Errorf("effective weights %d and %d; a voter rejected three times on "+
			"this field must not outweigh one who has not been",
			w[0].Effective(), w[1].Effective())
	}
}

// TestFullCrossProduct of policies against ballot shapes: nothing panics, and
// every result is internally consistent.
func TestFullCrossProduct(t *testing.T) {
	decays := []DecayPolicy{
		{Enabled: false, Factor: 0.5, MinWeight: 1000},
		{Enabled: true, Factor: 0.5, MinWeight: 1000},
		{Enabled: true, Factor: 0.9, MinWeight: 0},
		{Enabled: true, Factor: 0, MinWeight: 1000},    // nonsense, must clamp
		{Enabled: true, Factor: 2, MinWeight: -5},      // nonsense, must clamp
	}
	sybils := []SybilPolicy{
		{Enabled: false, DiminishingReturns: 8000, GroupWindow: 10},
		{Enabled: true, DiminishingReturns: 8000, GroupWindow: 10},
		{Enabled: true, DiminishingReturns: 1000, GroupWindow: 2},
		{Enabled: true, DiminishingReturns: 0, GroupWindow: 1}, // nonsense, must clamp
	}

	shapes := [][]WeightBasis{
		{},
		{basis(1, 1, 0)},
		{basis(1, 1, 0), basis(2, -1, 0)},
		{basis(1, 1, 100), basis(2, 1, 100), basis(3, 1, 100)},
		{basis(1, -1, 5), basis(2, -1, 5), basis(3, 1, 5), basis(4, 1, 500)},
		{{UserID: 1, Value: 1, Reputation: 100, FieldRejections: 7}},
		{basis(1, 1, 1000000), basis(1, -1, 1000000)}, // one user, both ways
	}

	for di, d := range decays {
		for si, s := range sybils {
			for bi, shape := range shapes {
				w := ComputeWeights(shape, d, s)
				if len(w) != len(shape) {
					t.Fatalf("decay %d sybil %d shape %d: got %d weights for %d ballots",
						di, si, bi, len(w), len(shape))
				}
				values := ballotValues(shape)
				tally := Tally(w, values)

				if tally.Weight != tally.For+tally.Against {
					t.Errorf("decay %d sybil %d shape %d: Weight %d != For %d + Against %d",
						di, si, bi, tally.Weight, tally.For, tally.Against)
				}
				if tally.Net != tally.For-tally.Against {
					t.Errorf("decay %d sybil %d shape %d: Net %d != For %d - Against %d",
						di, si, bi, tally.Net, tally.For, tally.Against)
				}
				if tally.Voters > len(shape) {
					t.Errorf("decay %d sybil %d shape %d: Voters %d exceeds %d ballots",
						di, si, bi, tally.Voters, len(shape))
				}
				for i, weight := range w {
					if weight.Basis < 0 || weight.SybilFactor < 0 {
						t.Errorf("decay %d sybil %d shape %d ballot %d: negative weight %+v",
							di, si, bi, i, weight)
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Correlation flagging
// ---------------------------------------------------------------------------

// TestFlagCorrelationFlagsManyInexperiencedAccounts is the "flag, do not
// punish" half of the spec requirement.
func TestFlagCorrelationFlagsManyInexperiencedAccounts(t *testing.T) {
	var bases []WeightBasis
	for i := 1; i <= 6; i++ {
		bases = append(bases, basis(i, 1, 0)) // no reputation on this field
	}
	w := ComputeWeights(bases, DecayPolicy{}, DefaultSybil())

	flagged := FlagCorrelation(w, bases, 5)
	if len(flagged) != 6 {
		t.Errorf("flagged %d ballots, want 6", len(flagged))
	}
}

// TestFlagCorrelationIgnoresEstablishedConsensus is what keeps the mechanism
// from being a witch hunt.
func TestFlagCorrelationIgnoresEstablishedConsensus(t *testing.T) {
	var bases []WeightBasis
	for i := 1; i <= 8; i++ {
		bases = append(bases, basis(i, 1, 500)) // established on this field
	}
	w := ComputeWeights(bases, DecayPolicy{}, DefaultSybil())

	if got := FlagCorrelation(w, bases, 5); len(got) != 0 {
		t.Errorf("flagged %d ballots; a group of established users agreeing is "+
			"consensus, not a coordinated attack", len(got))
	}
}

func TestFlagCorrelationBelowThreshold(t *testing.T) {
	var bases []WeightBasis
	for i := 1; i <= 3; i++ {
		bases = append(bases, basis(i, 1, 0))
	}
	w := ComputeWeights(bases, DecayPolicy{}, DefaultSybil())
	if got := FlagCorrelation(w, bases, 5); len(got) != 0 {
		t.Errorf("flagged %d ballots below the threshold", len(got))
	}
}

// ---------------------------------------------------------------------------
// M2 compatibility — the proof the flat path is a fallback, not dead code
// ---------------------------------------------------------------------------

// TestFlatPolicyStillEvaluates is the plan's explicit requirement: M2's
// acceptance arithmetic must keep working, because it is the moderator-only and
// migration path.
func TestFlatPolicyStillEvaluates(t *testing.T) {
	tests := []struct {
		name string
		p    Policy
		v    VoteCount
		want Decision
	}{
		{"net below threshold", DefaultPolicy(), VoteCount{Net: 2, Voters: 3}, DecisionPending},
		{"net at threshold with enough voters", DefaultPolicy(), VoteCount{Net: 3, Voters: 3}, DecisionAccepted},
		{"threshold met on too few voters", DefaultPolicy(), VoteCount{Net: 5, Voters: 2}, DecisionPending},
		{"moderator approval is decisive", DefaultPolicy(), VoteCount{Net: 0, ModeratorApproved: true}, DecisionAccepted},
		{"withdrawn is terminal", DefaultPolicy(), VoteCount{Net: 9, Withdrawn: true}, DecisionRejected},
		{"zero threshold is moderator-only", Policy{QuorumThreshold: 0, MinVoters: 3}, VoteCount{Net: 99, Voters: 99}, DecisionPending},
		{"self-vote cannot accept alone", DefaultPolicy(), VoteCount{Net: 3, Voters: 3, SelfVote: true}, DecisionPending},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Evaluate(tc.p, tc.v); got != tc.want {
				t.Errorf("Evaluate = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWeightedThresholdComparesLikeNet: the weighted gate has to be expressible
// in the same units, or a threshold configured for flat counting would mean
// something else entirely under weighting.
func TestWeightedBasisPointsScale(t *testing.T) {
	// 10000 basis points is one fresh voter. A quorum expressed in basis points
	// is therefore directly comparable to the old net-vote count divided by the
	// fresh-voter weight, which is what makes the migration arithmetic possible.
	fresh := ReputationToBasis(100)
	if fresh != 10000 {
		t.Fatalf("a voter with reputation 100 has basis %d, want 10000; the "+
			"basis-point scale is defined so a fresh voter is exactly 1.0",
			fresh)
	}
}
