package collab

import "context"

// Shadow mode: run the reputation-weighted decision beside flat quorum, record
// where they disagree, and apply neither change.
//
// The reason this exists is that §5.3 leaves the weight table OPEN — it asks
// whether votes are weighted per field type or by a single weight, and does not
// answer. An instance that guesses wrong adopts a governance model nobody voted
// for, and the cost of being wrong is not a bad number, it is content landing
// or not landing on the strength of a coin flip.
//
// So: run both, log both, apply flat. The instance keeps its existing
// behaviour exactly, and accumulates the evidence needed to choose. When the
// operator decides, switching over is a configuration flag, not a rewrite —
// which is only true because the weighted path already existed and had never
// been wired in.

// ShadowRecord is one comparison between the two decision functions.
//
// Recorded for EVERY evaluation, agreeing or not, not just for disagreements.
// A log of only the disagreements cannot answer the question operators
// actually have, which is "how often would this have changed things, and which
// way" — that needs the denominator as much as the numerator. Logging only
// disagreements would make a 0.1% disagreement rate and a 60% one look alike if
// only the loud cases were kept.
type ShadowRecord struct {
	ProposalID int
	TargetType string
	Field      string

	// Flat is what was APPLIED. Not "what flat said" in the abstract: this is
	// the decision that actually took effect, so a later switch-over can be
	// checked against history.
	Flat Decision

	// Weighted is what governance v2 would have decided.
	Weighted Decision

	// Reason is the weighted function's explanation, carried through so an
	// operator reading the log does not have to re-derive it.
	Reason string

	// WeightedTotal is the weighted net in basis points, and Net is the flat
	// count. Both are kept because "they agreed" and "they agreed by luck, with
	// a total of 2 against a threshold of 30000" are different facts, and only
	// the numbers distinguish them.
	WeightedTotal int
	Net           int

	// Voted is how many distinct voters were on the ballot, and Ballots is how
	// many ballot rows there were. They differ when a re-vote updated a row, and
	// a disagreement caused by a thin ballot is a different finding from one
	// caused by a broad one.
	Voted   int
	Ballots int

	// Flagged is the count of ballots the Sybil detector marked as coordinated.
	// Carried because "they disagreed AND three ballots looked coordinated" is
	// the single most interesting row in the log.
	Flagged int
}

// Agrees reports whether the two decision functions reached the same verdict.
//
// A disagreement is recorded as an asymmetry rather than as "weighted is
// stricter", because it is not necessarily one. Weighted arithmetic can accept
// where flat quorum pends (a small number of heavily-reputed voters) and reject
// where flat accepts (a large number of fresh accounts, which is the Sybil case
// the weighted path exists to catch). Both directions are real and the log has
// to be able to tell them apart.
func (r ShadowRecord) Agrees() bool { return r.Flat == r.Weighted }

// Direction names the kind of disagreement, for grouping in a moderation view.
// Empty when the two agree.
func (r ShadowRecord) Direction() string {
	if r.Agrees() {
		return ""
	}
	if r.Flat == DecisionPending {
		return "weighted_would_accept"
	}
	if r.Weighted == DecisionPending {
		return "weighted_would_hold"
	}
	return "weighted_would_flip"
}

// ShadowStore persists shadow records.
//
// An interface, for the same reason ProposalStore and ReputationStore are: the
// governance rules must be testable without a database, and the resolver must
// not grow a SQL dependency.
type ShadowStore interface {
	// Record appends one comparison.
	Record(ctx context.Context, r ShadowRecord) error

	// Disagreements returns the records where the two functions differed,
	// newest first, limited to `limit` rows. A limit of zero or less means all
	// of them: a moderation view that silently shows the first page is worse
	// than one that shows everything, because it looks authoritative.
	Disagreements(ctx context.Context, limit int) ([]ShadowRecord, error)

	// Summary counts, over every record: how many evaluations ran, how many
	// agreed, and how the disagreements broke down by direction.
	//
	// Derived from the rows by SQL rather than kept as counters, for the reason
	// non-negotiable #4 gives: a counter is a second source of truth that can
	// disagree with the thing it counts.
	Summary(ctx context.Context) (ShadowSummary, error)
}

// ShadowSummary is the aggregate an operator needs to make the switch decision.
type ShadowSummary struct {
	Evaluations int
	Agreed      int

	// The three disagreement directions, counted.
	WouldAccept int
	WouldHold   int
	WouldFlip   int
}

// Disagreements is the number of evaluations where the two functions differed.
func (s ShadowSummary) Disagreements() int { return s.WouldAccept + s.WouldHold + s.WouldFlip }

// Rate is the fraction of evaluations that disagreed, as a percentage.
//
// Returns 0 when nothing has been evaluated rather than dividing by zero. An
// instance that has never held a vote has no disagreement rate, and reporting
// NaN would put a meaningless number in front of whoever is deciding.
func (s ShadowSummary) Rate() float64 {
	if s.Evaluations == 0 {
		return 0
	}
	return float64(s.Disagreements()) / float64(s.Evaluations) * 100
}
