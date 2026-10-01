package models

import (
	"context"
	"time"
)

// ProposalStatus is the lifecycle state of an edit proposal.
type ProposalStatus string

const (
	// ProposalOpen is the resting state. Votes accumulate; the proposal has not
	// been decided.
	ProposalOpen ProposalStatus = "open"

	// ProposalAccepted means the value will be applied to the target.
	ProposalAccepted ProposalStatus = "accepted"

	// ProposalRejected means it will not be applied, and the rejection is
	// sticky for this (target, field, author) — see collab.StickyRejectionKey.
	ProposalRejected ProposalStatus = "rejected"

	// ProposalSuperseded means a newer proposal on the same (target, field)
	// replaced this one. Deliberately NOT the same as rejected: supersession is
	// not a judgement on the author and must not trigger sticky rejection.
	ProposalSuperseded ProposalStatus = "superseded"

	// ProposalWithdrawn means the author took it back.
	ProposalWithdrawn ProposalStatus = "withdrawn"
)

// IsTerminal reports whether the status is final.
func (s ProposalStatus) IsTerminal() bool {
	return s != ProposalOpen && s != ""
}

// EditProposal is a requested change to one field of one shared entity.
//
// A proposal is the ONLY way a shared field changes. Non-negotiable #5: no
// resolver edits a shared field directly, and an audit test enforces that. This
// type is where a change waits for governance to rule on it.
type EditProposal struct {
	ID         int
	TargetType string
	TargetID   int
	Field      string

	// OldValue and NewValue are pointers because NULL means "was unset", which
	// is a DIFFERENT fact from "was the empty string". Without this, "clear this
	// field" and "set this field to nothing" become the same edit, and one of
	// them becomes impossible to express.
	OldValue *string
	NewValue *string

	Rationale string
	AuthorID  int
	CreatedAt time.Time
	Status    ProposalStatus

	DecidedAt *time.Time
	DecidedBy *int
}

// Terminal reports whether the proposal's status is final.
func (p *EditProposal) Terminal() bool {
	return p.Status != ProposalOpen
}

// ProposalScore is a computed tally. Never stored — see the proposal_scores
// view.
type ProposalScore struct {
	Net         int
	Voters      int
	AuthorVoted bool
}

// EditProposalStore persists proposals and their votes.
type EditProposalStore interface {
	Create(ctx context.Context, p *EditProposal) (*EditProposal, error)
	Find(ctx context.Context, id int) (*EditProposal, error)
	FindOpen(ctx context.Context, targetType string, targetID int, field string) (*EditProposal, error)
	FindByAuthor(ctx context.Context, authorID int, limit int) ([]*EditProposal, error)
	CountOpenByAuthor(ctx context.Context, authorID int) (int, error)

	// SetStatus records a decision, stamping decided_at and decided_by.
	// A decided proposal must not be re-decided; that is refused.
	SetStatus(ctx context.Context, id int, status ProposalStatus, decidedBy int) error

	// Score computes the tally from the vote rows. The single source of truth
	// for a proposal's standing.
	Score(ctx context.Context, proposalID int) (ProposalScore, error)
}

// ProposalVoteStore persists votes.
type ProposalVoteStore interface {
	// Cast records or replaces a user's vote. Replacing rather than appending is
	// what keeps one account from contributing twice — the Sybil shape that
	// MinVoters exists to bound, enforced here by the composite primary key.
	Cast(ctx context.Context, proposalID, userID, value int) error
	Withdraw(ctx context.Context, proposalID, userID int) error
	List(ctx context.Context, proposalID int) ([]Vote, error)
}

// Vote is one user's opinion on a proposal.
type Vote struct {
	ProposalID int
	UserID     int
	Value      int
	CreatedAt  time.Time
}
