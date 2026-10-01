package collab

import (
	"context"
	"errors"
)

// ReputationStore is the persistence reputation-weighted voting needs.
//
// Same reasoning as ProposalStore: an interface, not a concrete
// *sqlite.FieldReputationStore, because this is the layer the resolver talks to
// and the resolver's tests must not need a database to check the rules.
//
// The methods are deliberately coarse. A finer interface — SetReputation,
// IncrementRejections, and so on — would push the arithmetic into the resolver,
// and the arithmetic is the part that has to be identical everywhere it runs.
// Two callers computing decay differently is exactly the bug M2b's
// reproducibility requirement exists to prevent.

// ErrNoReputation is returned when a user has no row for a field.
//
// A distinct error from a zero-valued reputation, because the two mean
// different things and the difference is visible: no row means "never voted on
// this field", and a row of 0 means "voted here and was on the losing side
// every time". Both render as 0 weight, but only the second is a signal about
// the user, and conflating them would make a user's first bad election
// indistinguishable from their first election.
var ErrNoReputation = errors.New("no reputation recorded for this user on this field")

// FieldStanding is one user's reputation on one field.
type FieldStanding struct {
	UserID int
	// TargetType and Field are the competence this reputation is about. It is a
	// claim about a SPECIFIC field, not a general rank, which is the whole
	// reason reputation is per-field (spec §8.2).
	TargetType string
	Field      string

	// Reputation is the weighted agreement count. Never negative: the schema
	// forbids it, and the mechanism for a user who keeps losing is decay, which
	// discounts their vote rather than excluding them.
	Reputation int

	// Rejections is how many accepted proposals on this field they were on the
	// losing side of. The input to decay, and stored separately because decay is
	// a judgement about a named user while reputation is a score.
	Rejections int
}

// WeightBasisFor renders a standing as the WeightBasis the tally consumes.
//
// The translation lives here rather than at each call site so there is one
// definition of "what reputation means as a weight" -- and one place to change
// it. ReputationToBasis is the whole policy: sub-linear, floored, capped.
func (s FieldStanding) WeightBasisFor(value int) WeightBasis {
	return WeightBasis{
		UserID:          s.UserID,
		Value:           value,
		Reputation:      s.Reputation,
		FieldRejections: s.Rejections,
	}
}

type ReputationStore interface {
	// Standing returns this user's reputation on one field.
	// ErrNoReputation when there is no row.
	Standing(ctx context.Context, userID int, targetType, field string) (*FieldStanding, error)

	// StandingsForMany returns the reputation for a whole ballot set in one
	// query.
	//
	// Batched rather than a loop over Standing on purpose. A tally on a busy
	// proposal has one query per voter, and that is a per-ballot database round
	// trip inside what should be arithmetic. The batch method also guarantees
	// the readers see one consistent snapshot, so two voters cannot observe
	// different reputation values while a tally is in flight.
	//
	// A user with no row is ABSENT from the result, not present with zeros. The
	// caller substitutes a fresh voter's weight, which is the same thing but
	// keeps "no row" distinguishable from "a row of zero" all the way through.
	StandingsForMany(ctx context.Context, userIDs []int, targetType, field string) (map[int]FieldStanding, error)

	// RecordOutcome credits or debits one user's reputation for one settled
	// proposal, and returns the new standing.
	//
	// Called ONCE per voter when a proposal is decided, from the decision
	// transaction -- not during the vote and not on a tally. Reputation that
	// moves as votes arrive is reputation that rewards campaigning, and a user
	// could farm it by opening proposals they expect to win narrowly.
	//
	// A row is created on first credit. A debit against a user who has no row
	// is a no-op returning ErrNoReputation: there is nothing to debit, and
	// inventing a negative row would put a value in the database that the
	// schema forbids.
	RecordOutcome(ctx context.Context, standing FieldStanding, agreed bool) (*FieldStanding, error)
}
