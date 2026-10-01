package sqlite

import (
	"context"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/models"
)

// CollabProposalStore adapts the row-oriented sqlite store to the interface
// collab speaks.
//
// It exists as a separate type rather than as methods on EditProposalStore
// because the two disagree about what a "proposal" is. EditProposalStore works
// in *models.EditProposal, which is the GraphQL-facing type and carries the
// lifecycle status. collab.Proposal is the service's own view and carries no
// status at all, deliberately: the service decides what happens, and a type that
// carried a verdict would let the persistence layer make one.
//
// The awkward alternative — making collab import models — was rejected because
// it couples the pure governance logic to the database schema, and governance
// is the part of StashForge most worth being able to test without a database.

// Compile-time proof of both interfaces. A signature drift between the adapter
// and collab's expectations should fail here, at build time, rather than at the
// first call from a resolver.
var (
	_ collab.ProposalStore = (*CollabProposalStore)(nil)
	_ collab.TargetStore   = (*CollabTargetStore)(nil)
)

type CollabProposalStore struct {
	proposals *EditProposalStore
}

func NewCollabProposalStore() *CollabProposalStore {
	return &CollabProposalStore{proposals: NewEditProposalStore()}
}

// Create inserts a proposal from the service's view and returns it with its id
// populated.
func (s *CollabProposalStore) Create(ctx context.Context, p *collab.Proposal) (*collab.Proposal, error) {
	row := &models.EditProposal{
		TargetType: p.TargetType,
		TargetID:   p.TargetID,
		Field:      p.Field,
		OldValue:   p.OldValue,
		NewValue:   p.NewValue,
		Rationale:  p.Rationale,
		AuthorID:   p.AuthorID,
		Status:     models.ProposalOpen,
	}

	created, err := s.proposals.Create(ctx, row)
	if err != nil {
		return nil, err
	}
	return toCollabProposal(created), nil
}

// FindOpen returns the open proposal on (target, field), if any.
//
// found is false on a miss rather than an error, because a field nobody has
// disputed is the normal state and not a fault.
func (s *CollabProposalStore) FindOpen(ctx context.Context, targetType string, targetID int, field string) (*collab.Proposal, bool, error) {
	p, err := s.proposals.FindOpen(ctx, targetType, targetID, field)
	if err != nil {
		// A miss is ErrNotFound from the row store. Translating it to found=
		// false here is the whole point of the two-value return: the caller in
		// collab is deciding whether to supersede, and "no open proposal" is a
		// branch it takes, not an exception.
		if isNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return toCollabProposal(p), true, nil
}

// RejectionsFor returns the sticky-rejection keys in force for one author.
//
// Derived by reading the author's rejected proposals rather than from a
// separate sticky table. That is a deliberate choice: a second table would be a
// second thing to keep in sync, and the rule it encodes — a rejection binds
// (target, field, author) — is already readable from the rows that record the
// rejection. The cost is a scan of one author's rejected proposals, which is
// bounded by what that author has had rejected.
func (s *CollabProposalStore) RejectionsFor(ctx context.Context, authorID int) (map[collab.StickyRejectionKey]bool, error) {
	rows, err := s.proposals.FindRejectedByAuthor(ctx, authorID)
	if err != nil {
		return nil, err
	}

	// Every rejected row blocks its key, so the set is a union rather than a
	// last-write-wins map. Two rejections of the same field by the same author
	// are the same block, and dropping either changes nothing.
	out := make(map[collab.StickyRejectionKey]bool, len(rows))
	for _, r := range rows {
		out[collab.NewStickyRejectionKey(r.TargetType, r.TargetID, r.Field, authorID)] = true
	}
	return out, nil
}

// Supersede marks the open proposal on (target, field) superseded and returns
// its id.
//
// The superseded row is kept rather than deleted. A proposal people voted on is
// part of the record, and erasing it would make a later "why did this change"
// question unanswerable.
func (s *CollabProposalStore) Supersede(ctx context.Context, targetType string, targetID int, field string) (int, bool, error) {
	p, err := s.proposals.FindOpen(ctx, targetType, targetID, field)
	if err != nil {
		if isNotFound(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if err := s.proposals.SetStatus(ctx, p.ID, models.ProposalSuperseded, 0); err != nil {
		return 0, false, err
	}
	return p.ID, true, nil
}

// toCollabProposal narrows a row to the service's view.
func toCollabProposal(p *models.EditProposal) *collab.Proposal {
	if p == nil {
		return nil
	}
	return &collab.Proposal{
		ID:         p.ID,
		TargetType: p.TargetType,
		TargetID:   p.TargetID,
		Field:      p.Field,
		OldValue:   p.OldValue,
		NewValue:   p.NewValue,
		Rationale:  p.Rationale,
		AuthorID:   p.AuthorID,
	}
}

// isNotFound reports whether err is the row store's absence sentinel.
func isNotFound(err error) bool {
	return err == models.ErrNotFound
}
