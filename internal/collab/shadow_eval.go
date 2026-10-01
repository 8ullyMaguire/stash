package collab

import "context"

// Shadow evaluation: run both decision functions, record the comparison, and
// apply neither change.
//
// Split from persistence on purpose. The interesting part of shadow mode is the
// bookkeeping — which disagreement is which, and what an operator needs in order
// to make the switch decision — and none of that needs a database. Keeping it here
// means the rules are testable without one, which is the same reason Evaluate and
// EvaluateWeighted are both in this package.

// Ballot is one vote, as shadow mode needs it.
//
// Distinct from WeightBasis deliberately. WeightBasis is the weighted path's input
// and already carries a reputation number that someone had to look up; the shadow
// comparison needs the vote itself, because the flat decision is computed from
// exactly this and the two must be describing the same set of people. Built from
// WeightBasis, a fresh voter and a zero-reputation voter would be
// indistinguishable in the log — and the log's whole job is to say who voted.
type Ballot struct {
	UserID int
	Value  int
}

// ShadowInput is everything both decision functions need.
//
// One struct rather than two arguments, because both functions must be given the
// SAME proposal state. Evaluating them against different vote sets would produce a
// comparison that looks like a governance finding and is actually a race.
type ShadowInput struct {
	ProposalID int
	TargetType string
	Field      string
	AuthorID   int

	Ballots []Ballot

	Withdrawn         bool
	Superseded        bool
	ModeratorApproved bool
	AllowSelfAccept   bool
}

// ShadowSources is the outside data shadow mode needs.
//
// An interface rather than a concrete store type, for the reason every other
// interface here is: the governance rules must be testable without a database,
// and the resolver must not grow a SQL dependency.
type ShadowSources interface {
	// ReputationFor returns the field standing for each of these users on one
	// (targetType, field), keyed by user id.
	//
	// Users with no row are absent from the map rather than present with a zero
	// value. ReputationStore documents why: "never voted here" and "voted here and
	// always lost" both render as weight zero, and only the second says anything
	// about the user.
	ReputationFor(ctx context.Context, userIDs []int, targetType, field string) (map[int]FieldStanding, error)
}

// EvaluateShadow runs flat quorum and the weighted rule over the same ballots and
// returns what each decided, plus the record to persist.
//
// Flat is returned FIRST, and it is what the caller must apply. Shadow mode is
// observational by construction, so the signature deliberately does not return a
// single Decision: a single return value would leave room for a caller to apply
// the wrong one, and the entire safety property of this feature is that a
// mis-wiring cannot change governance.
func EvaluateShadow(ctx context.Context, p Policy, in ShadowInput, src ShadowSources) (
	flat Decision, weighted WeightedDecision, record ShadowRecord,
) {
	// The flat decision, from the same ballots the weighted path will see.
	net := 0
	voters := 0
	authorVoted := false
	for _, b := range in.Ballots {
		net += b.Value
		voters++
		if b.UserID == in.AuthorID {
			authorVoted = true
		}
	}

	flat = Evaluate(p, VoteCount{
		Net:               net,
		Voters:            voters,
		SelfVote:          authorVoted,
		ModeratorApproved: in.ModeratorApproved,
		Withdrawn:         in.Withdrawn,
		Superseded:        in.Superseded,
	})

	// The weighted path.
	//
	// A reputation lookup failure degrades to fresh-voter weights rather than
	// failing the evaluation. Shadow mode must never be able to break the path it
	// is observing: a governance log that can fail a vote is worse than one that
	// records a different tally, because the first changes governance and the
	// second does not. The degradation is visible afterwards — a tally of all
	// zeros is obviously wrong to whoever reads the log.
	bases := make([]WeightBasis, 0, len(in.Ballots))
	standings := map[int]FieldStanding{}
	if len(in.Ballots) > 0 && src != nil {
		ids := make([]int, 0, len(in.Ballots))
		for _, b := range in.Ballots {
			ids = append(ids, b.UserID)
		}
		// The `got != nil` guard is NOT load-bearing and the mutation that removes
		// it survives, correctly: assigning a nil map to `standings` and leaving
		// the zero-value empty map in place are the same thing to every lookup
		// below. Noted because a surviving mutation is only acceptable once you
		// have established it is equivalent, and here it demonstrably is.
		if got, err := src.ReputationFor(ctx, ids, in.TargetType, in.Field); err == nil {
			standings = got
		}
	}
	for _, b := range in.Ballots {
		if s, ok := standings[b.UserID]; ok {
			bases = append(bases, s.WeightBasisFor(b.Value))
			continue
		}
		// No reputation row means a fresh voter: weight zero. Three of these and
		// the proposal cannot reach a weighted threshold, which is exactly what
		// makes the Sybil resistance work.
		bases = append(bases, WeightBasis{UserID: b.UserID, Value: b.Value})
	}

	weighted = EvaluateWeighted(p, WeightedInput{
		Votes:             bases,
		Withdrawn:         in.Withdrawn,
		Superseded:        in.Superseded,
		ModeratorApproved: in.ModeratorApproved,
		AuthorID:          in.AuthorID,
		AllowSelfAccept:   in.AllowSelfAccept,
	})

	record = ShadowRecord{
		ProposalID:    in.ProposalID,
		TargetType:    in.TargetType,
		Field:         in.Field,
		Flat:          flat,
		Weighted:      weighted.Decision,
		Reason:        weighted.Reason,
		WeightedTotal: weighted.Tally.Net,
		Net:           net,
		Voted:         weighted.Tally.Voters,
		Ballots:       len(in.Ballots),
		Flagged:       len(weighted.Flagged),
	}
	return flat, weighted, record
}
