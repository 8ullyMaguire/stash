package collab

// The weighted decision function: what replaces flat net-vote counting as the
// primary path, while leaving flat counting reachable as the moderator-only and
// migration mode.
//
// # Why Evaluate was extended rather than replaced
//
// M2's `Evaluate` is 40 lines, exhaustively tested, and the spec is explicit
// that M2 is not discarded. A second function would mean two decision functions
// and a config flag choosing between them, which is how an instance ends up
// with governance nobody can state in a sentence. So there is one function and
// one policy field that selects the arithmetic:
//
//	WeightedThreshold > 0  ->  weighted arithmetic
//	WeightedThreshold == 0 ->  flat arithmetic (M2, unchanged)
//	QuorumThreshold == 0   ->  moderator-only, neither arithmetic runs
//
// Every combination is a supported mode. That is not hedging; it is what makes
// the migration possible at all, because an instance that has not chosen a
// reputation model has to be able to keep running the one it has.

// WeightedPolicy is the weighted half of the decision rule.
//
// It is a separate struct rather than six more fields on Policy because the two
// halves are independently optional: an instance can weight votes without
// damping them, and an instance can damp without weighting. Folding them
// together would make each of those configurations inexpressible.
type WeightedPolicy struct {
	// Threshold is the weighted net total that auto-accepts, in basis points.
	// Zero disables the weighted path entirely, leaving flat counting.
	//
	// The unit matters: a threshold of 30000 is three fresh voters' worth of
	// support, and it is NOT comparable to a flat QuorumThreshold of 3. An
	// operator copying a flat threshold into this field gets a threshold 10000x
	// too large and concludes governance is broken. The field is in basis
	// points precisely so a flat threshold of N maps to N*10000 when every
	// voter is fresh, and Normalised does that conversion when asked.
	Threshold int

	// Decay and Sybil are the two adjustments, carried on the policy so a
	// decision can be reproduced from (Policy, VoteCount) alone — which is the
	// property that makes the audit row meaningful. A tally computed from state
	// that is not in the policy cannot be re-derived from the record.
	Decay DecayPolicy
	Sybil  SybilPolicy

	// MinVoters is re-used from Policy rather than redeclared, and the reason is
	// worth stating: a distinct-voter floor that exists for the flat path and
	// does not exist for the weighted one would be a hole exactly where the
	// arithmetic is more sophisticated and therefore more trusted.
}

// Normalised returns a copy with unusable values corrected.
//
// The threshold conversion is the part that is not obvious. A weighted threshold
// and a flat threshold are different units, and an operator who sets
// WeightedThreshold: 3 expecting M2's behaviour would otherwise get a system
// that never auto-accepts anything. Rather than trust the unit, the two are kept
// distinct in the struct and this function documents the mapping:
//
//	flat N      ->  weighted N * 10000   (N fresh voters)
//	flat N, all established ->  N * ReputationToBasis(100)
//
// The second is what an instance full of established users actually sees, and
// it is 10000x the first — so a threshold copied from an M2 config lands in the
// same place either way only if everyone is fresh. The honest statement is that
// the conversion is a migration aid, not an equivalence, and an operator
// deploying M2b over an established instance should set the threshold by
// observing a real tally rather than by arithmetic.
func (w WeightedPolicy) Normalised() WeightedPolicy {
	out := w
	if out.Threshold < 0 {
		out.Threshold = 0
	}
	out.Decay = out.Decay.Normalised()
	out.Sybil = out.Sybil.Normalised()
	return out
}

// FlatThresholdAsWeighted converts a flat net-vote threshold into the weighted
// equivalent, for migration.
//
// Documented rather than automatic on purpose. An automatic conversion is a
// guess presented as a fact, and a guess that silently changes what settles
// automatically is the worst kind: an instance would find that proposals it
// expected to auto-accept now do not, or the reverse, with no record of why.
func FlatThresholdAsWeighted(flat int, typicalReputation int) int {
	if flat <= 0 {
		return 0
	}
	return flat * ReputationToBasis(typicalReputation)
}

// WeightedInput is everything needed to decide a proposal under the weighted
// rule.
type WeightedInput struct {
	// Votes is the ballot set, in the order the votes were cast. Order matters
	// because Sybil damping groups identical runs, and a run is a temporal
	// property.
	Votes []WeightBasis

	// Carries the M2 terminal and moderator states, so the weighted path
	// honours exactly the same precedence rules. Duplicating these here rather
	// than taking a VoteCount would let the two paths disagree about whether a
	// withdrawn proposal can be approved, and that is a real bug rather than a
	// theoretical one.
	Withdrawn  bool
	Superseded bool
	ModeratorApproved bool

	// AuthorID is excluded from the tally unless AllowSelfAccept. The
	// exclusion is a subtraction from the weighted total, not a skip, so an
	// author's own vote still counts toward the distinct-voter floor.
	AuthorID int

	// AllowSelfAccept removes the exclusion. It does NOT bypass the threshold,
	// for the same reason it does not in the flat path: an author-vote is one
	// opinion among others, and letting it accept a proposal alone would make
	// the vote a formality.
	AllowSelfAccept bool
}

// WeightedDecision is the outcome plus the reasoning, because a governance
// system that cannot explain itself is one nobody trusts or debugs.
type WeightedDecision struct {
	Decision Decision

	// Tally is the weighted computation, retained so a caller can show the
	// numbers rather than just the verdict.
	Tally WeightedTally

	// Weights is the per-ballot computation, retained for the same reason.
	Weights []Weight

	// Reason is a short human-readable explanation, suitable for a moderation
	// view. Empty when accepted, where there is nothing to explain.
	Reason string

	// Flagged holds the indices of ballots that look coordinated. Reported
	// alongside the decision and never subtracted from it: the spec requires
	// coordinated patterns be flagged, not silently punished, and a flag that
	// changed the arithmetic would be the silent half.
	Flagged []int
}

// EvaluateWeighted decides a proposal under the weighted rule.
//
// The precedence order is identical to the flat path, and deliberately so:
//
//  1. Withdrawn or Superseded -> rejected, terminal.
//  2. Moderator approval -> accepted.
//  3. Weighted threshold met AND distinct voters >= MinVoters -> accepted.
//
// Step 1 before step 2 is not an oversight. A withdrawn proposal that a
// moderator approves afterwards is a bug, not a rescue: the author took it back
// and the only correct reading of that is that it is not happening. Carrying the
// two paths' precedence in step is what makes a bug in one a bug in both.
func EvaluateWeighted(p Policy, in WeightedInput) WeightedDecision {
	p = p.Normalised()
	wp := p.Weighted.Normalised()

	out := WeightedDecision{Decision: DecisionPending}

	// 1. Terminal states, checked before anything else and before any
	//    arithmetic, so a withdrawn proposal costs nothing to evaluate.
	if in.Withdrawn || in.Superseded {
		out.Decision = DecisionRejected
		out.Reason = "the proposal was withdrawn or superseded"
		return out
	}

	// 2. A moderator ruling is decisive. Evaluated before the tally so an
	//    approval does not depend on a reputation lookup succeeding — a
	//    moderator's decision must not be contingent on a weight computation.
	if in.ModeratorApproved {
		out.Decision = DecisionAccepted
		return out
	}

	// 3. The weighted path itself. A zero threshold means this instance runs
	//    flat counting; Evaluate handles it and this function defers, so there
	//    is exactly one implementation of the flat rule.
	if wp.Threshold == 0 {
		flat := Evaluate(p, flatVoteCount(in))
		out.Decision = flat
		if flat == DecisionPending {
			out.Reason = "weighted voting is off; the proposal awaits the flat threshold"
		}
		return out
	}

	// The author's ballot is excluded from the ARITHMETIC but not from the
	// distinct-voter count.
	//
	// These have to be separated, and the first version of this function got it
	// wrong: it filtered the ballot slice before tallying, so excluding the
	// author's vote also decremented Voters. The test caught it. The effect is
	// that MinVoters becomes a tool for excluding authors from their own
	// proposals' quorum — a proposer with a popular correction would need one
	// more supporter than a proposer nobody agrees with, which is precisely
	// backwards.
	//
	// This is the same defect as the one found in Tally itself (counting
	// ballots rather than people), one layer up. Both have the same shape: a
	// filter applied to the wrong collection, where the intent was to affect the
	// sum and not the headcount.
	basis := in.Votes
	if !in.AllowSelfAccept {
		basis = withoutAuthor(basis, in.AuthorID)
	}

	weights := ComputeWeights(basis, wp.Decay, wp.Sybil)
	values := make([]int, len(basis))
	for i, b := range basis {
		values[i] = b.Value
	}
	tally := Tally(weights, values)

	// Count voters from the UNFILTERED set, for the reason above.
	tally.Voters = distinctVoters(in.Votes)

	out.Weights = weights
	out.Tally = tally
	out.Flagged = FlagCorrelation(weights, basis, 5)

	switch {
	case tally.Net < wp.Threshold:
		out.Reason = "weighted support " + itoa(tally.Net) + " of " + itoa(wp.Threshold) + " required"
		return out
	case tally.Voters < p.MinVoters:
		// The same Sybil guard the flat path has. A proposal that clears the
		// threshold on too few DISTINCT people stays pending however lopsided
		// the tally is, and weighting does not replace this — it adds a second
		// defence rather than a better one.
		out.Reason = "only " + itoa(tally.Voters) + " distinct voters, " + itoa(p.MinVoters) + " required"
		return out
	}

	out.Decision = DecisionAccepted
	return out
}

// withoutAuthor returns the ballots that are not the author's.
//
// Returns the original slice when the author has not voted, so the common case
// allocates nothing.
func withoutAuthor(votes []WeightBasis, authorID int) []WeightBasis {
	found := false
	for _, v := range votes {
		if v.UserID == authorID {
			found = true
			break
		}
	}
	if !found {
		return votes
	}

	out := make([]WeightBasis, 0, len(votes))
	for _, v := range votes {
		if v.UserID != authorID {
			out = append(out, v)
		}
	}
	return out
}

// flatVoteCount projects a WeightedInput onto M2's VoteCount, so the flat path
// can be evaluated from the same input.
//
// The projection sums the ballots rather than reading a stored counter, which
// keeps the "recompute, never store" rule holding on both paths.
func flatVoteCount(in WeightedInput) VoteCount {
	v := VoteCount{
		Withdrawn:          in.Withdrawn,
		Superseded:         in.Superseded,
		ModeratorApproved: in.ModeratorApproved,
	}
	for _, b := range in.Votes {
		v.Net += b.Value
		if b.UserID == in.AuthorID {
			v.SelfVote = true
		}
	}
	v.Voters = distinctVoters(in.Votes)
	return v
}

func distinctVoters(votes []WeightBasis) int {
	seen := make(map[int]struct{}, len(votes))
	for _, v := range votes {
		seen[v.UserID] = struct{}{}
	}
	return len(seen)
}

// itoa avoids importing strconv into a file whose whole point is that it has no
// dependencies beyond math. Small enough to be obviously correct, which a
// hand-rolled conversion has to be.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
