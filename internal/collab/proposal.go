package collab

import (
	"context"
	"errors"
	"fmt"
)

// ProposalStore is the persistence the service needs, as an interface.
//
// An interface here rather than a concrete *sqlite.EditProposalStore because
// this is the layer the RESOLVER talks to, and the resolver's tests must not
// need a database to check the rules. The same reason governance.go has no I/O.
type ProposalStore interface {
	// Create inserts and returns the stored proposal, id populated.
	Create(ctx context.Context, p *Proposal) (*Proposal, error)

	// FindOpen returns the open proposal for a (target, field), if any.
	// Found is false rather than an error on a miss: an absent open proposal is
	// the normal state of a field nobody has disputed, not a fault.
	FindOpen(ctx context.Context, targetType string, targetID int, field string) (*Proposal, bool, error)

	// RejectionsFor returns the sticky-rejection keys currently in force for
	// this author. Scoped to one author so the map stays small: a rejection
	// binds (target, field, author), so keys for other authors can never match
	// a proposal this author is making.
	RejectionsFor(ctx context.Context, authorID int) (map[StickyRejectionKey]bool, error)

	// Supersede marks the existing open proposal on this (target, field) as
	// superseded and returns its id. The caller must create the replacement in
	// the same transaction.
	Supersede(ctx context.Context, targetType string, targetID int, field string) (int, bool, error)
}

// Proposal is the service's view of an edit proposal. It mirrors the database
// row rather than sharing the type, so this package stays independent of the
// persistence layer and a schema change does not force a change here.
type Proposal struct {
	ID         int
	TargetType string
	TargetID   int
	Field      string
	OldValue   *string
	NewValue   *string
	Rationale  string
	AuthorID   int
}

// ErrStickyRejected is returned when this author already had a proposal on this
// field rejected and has not been unblocked.
//
// A separate error from ErrFieldNotProposable because the two need different UI:
// one is "that is not allowed", the other is "you asked for that and it was
// turned down". Collapsing them would either hide the sticky rule from the
// author or make the vocabulary look arbitrary.
var ErrStickyRejected = errors.New("a previous proposal on this field was rejected")

// ErrOpenProposalExists is returned when a proposal is already open on the
// field and supersession was not requested.
var ErrOpenProposalExists = errors.New("a proposal is already open on this field")

// Proposer applies the rules that decide whether a change may be proposed.
type Proposer struct {
	store ProposalStore
}

func NewProposer(store ProposalStore) *Proposer { return &Proposer{store: store} }

// Create validates and files a new proposal.
//
// The order of the checks is the whole design:
//
//  1. vocabulary      — the field must be proposable at all
//  2. value           — it must parse now, not fail at apply time
//  3. sticky rejection — this author, this field, already refused
//  4. supersede       — replace the open proposal, or refuse
//
// Validation precedes the database entirely. A rejected vocabulary lookup must
// not cost a transaction, and more importantly the sticky check must not run for
// a field the author could never have proposed on anyway.
func (p *Proposer) Create(ctx context.Context, req Proposal) (*Proposal, error) {
	if err := validateProposalField(req.TargetType, req.Field, req.NewValue); err != nil {
		return nil, err
	}

	rejected, err := p.store.RejectionsFor(ctx, req.AuthorID)
	if err != nil {
		return nil, fmt.Errorf("loading rejections: %w", err)
	}
	key := NewStickyRejectionKey(req.TargetType, req.TargetID, req.Field, req.AuthorID)
	if IsBlocked(rejected, key) {
		return nil, ErrStickyRejected
	}

	existing, found, err := p.store.FindOpen(ctx, req.TargetType, req.TargetID, req.Field)
	if err != nil {
		return nil, fmt.Errorf("looking for an open proposal: %w", err)
	}
	if found {
		return nil, fmt.Errorf("%w: proposal %d on %s %d field %q is already open",
			ErrOpenProposalExists, existing.ID, req.TargetType, req.TargetID, req.Field)
	}

	created, err := p.store.Create(ctx, &req)
	if err != nil {
		return nil, fmt.Errorf("filing proposal: %w", err)
	}
	return created, nil
}

// Supersede replaces the open proposal on a field with a new one.
//
// Two behaviours are deliberate and both are tested:
//
// The REPLACEMENT is validated exactly as a fresh Create would be, including
// against the sticky-rejection rule. Superseding is not a way around a refusal.
// If it were, "propose again" would simply mean "propose again harder", and the
// sticky rule would be theatre.
//
// The SUPERSEDED proposal is not recorded as a rejection. Supersession means
// "a newer request replaced this one", which says nothing about the author, and
// recording it would let the first supersession permanently bar them from the
// field.
//
// Returns ErrOpenProposalExists when there is nothing to supersede. Silently
// creating a "replacement" for a proposal that does not exist would make the
// caller believe a decision was made about a request that was never on record.
func (p *Proposer) Supersede(ctx context.Context, req Proposal) (*Proposal, error) {
	if err := validateProposalField(req.TargetType, req.Field, req.NewValue); err != nil {
		return nil, err
	}

	rejected, err := p.store.RejectionsFor(ctx, req.AuthorID)
	if err != nil {
		return nil, fmt.Errorf("loading rejections: %w", err)
	}
	key := NewStickyRejectionKey(req.TargetType, req.TargetID, req.Field, req.AuthorID)
	if IsBlocked(rejected, key) {
		return nil, ErrStickyRejected
	}

	_, found, err := p.store.Supersede(ctx, req.TargetType, req.TargetID, req.Field)
	if err != nil {
		return nil, fmt.Errorf("superseding: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("%w: nothing is open on %s %d field %q to supersede",
			ErrOpenProposalExists, req.TargetType, req.TargetID, req.Field)
	}

	created, err := p.store.Create(ctx, &req)
	if err != nil {
		return nil, fmt.Errorf("filing replacement proposal: %w", err)
	}
	return created, nil
}
