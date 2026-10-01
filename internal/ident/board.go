// Package ident turns a solved identification into a governance proposal.
//
// M7 step 7.5 (R029–R033), spec §6a.13 — the identification board.
//
// THE ONE CONSTRAINT THIS PACKAGE EXISTS TO HOLD: a solved identification writes a
// TYPED FIELD PROPOSAL, not a row (§6a.13, non-negotiable #5). The board's
// evidence is community-sourced, so it is the least trustworthy input in the
// system; writing it straight onto a performer would let a solved query — which
// anyone can open and anyone can vote on — mutate the directory with no review,
// no author, and no audit row.
//
// The distinction is not stylistic. Everything else in this file exists to make
// sure the ONLY way out of Solve is Proposer.Create. There is deliberately no
// method that writes a field, no store that could, and no path that takes a
// target and mutates it — so "the board bypassed governance" is not a bug a
// later step can introduce, it is a function that does not exist.
package ident

import (
	"context"
	"errors"
	"fmt"

	"github.com/stashapp/stash/internal/collab"
)

// Query is one identification request.
type Query struct {
	ID string

	// TargetType and TargetID are what is being identified. They address a
	// PROPOSAL target, not a row to modify.
	TargetType string
	TargetID   int

	// Field is the typed field the solve asserts — "performer", "studio",
	// "date", "studio". Typed means it must be in the proposable vocabulary,
	// which collab.Proposer already enforces; this package passes the name
	// through rather than maintaining a second list that could drift.
	Field string

	// Value is the proposed value. A pointer because "clear this field" is a
	// distinct and legitimate proposal, and collapsing it into "" would make an
	// empty-string assertion indistinguishable from a deletion.
	Value *string

	// CurrentValue is what the field holds now, when the caller knows. Optional --
	// nil means "not read", and the board passes that through rather than
	// inventing an empty string, because a proposal whose old value is a guess
	// shows a reviewer a diff nobody can trust.
	CurrentValue *string

	// Evidence is what backs the solve: a snapshot collage, keyframes, a
	// matching description. It travels as proposal rationale and audit Detail, so
	// a reviewer deciding the proposal can see WHY it was filed without the board
	// being a separate thing to consult.
	Evidence []Evidence
}

// Evidence is one supporting item. Deliberately opaque: this package carries
// evidence to a reviewer and does not judge it. A collage that "looks right" is
// the board's problem and the governance path's, not the transport's.
type Evidence struct {
	// Kind is "snapshot", "keyframe", "description", "context". Free text rather
	// than a closed set, because evidence kinds are added by scrapers and closing
	// the set here would mean editing this package for every new scraper.
	Kind string

	// Ref is opaque to this package and never interpreted. It may be a path, a
	// frame offset, or a URL -- the board's callers know, and a peer could put
	// anything here, so nothing downstream may treat it as a local path. That is
	// the same posture as the replica_path rule in step 7.1: a path from a
	// stranger is the one input that can name a file outside the library.
	Ref string

	Note string
}

// Solve is a resolved query plus who resolved it.
type Solve struct {
	Query Query

	// ResolverID authors the proposal. It is NOT the same as the query's opener,
	// and it is not nil-able: a solve with no identifiable author is a solve that
	// cannot be reviewed, and an unreviewable proposal is a field edit wearing a
	// costume.
	ResolverID int
}

// Errors, kept separate because each is a different operator problem.
var (
	// ErrNoResolver means the solve has no author. Refused at the boundary.
	ErrNoResolver = errors.New("ident: a solve must name the user who resolved it")

	// ErrNoValue means the proposal asserts nothing. A solve with no value and no
	// evidence is a solve that would file an empty proposal, which the governance
	// path would then have to churn through.
	ErrNoValue = errors.New("ident: a solve must assert a value or carry evidence")

	// ErrNotProposable means collab.Proposer refused the field name. Passed
	// through rather than duplicated, so the vocabulary has exactly one owner.
	ErrNotProposable = errors.New("ident: field is not proposable")
)

// Board resolves queries into proposals.
type Board struct {
	proposer *collab.Proposer
}

// NewBoard wires a board to the governance path.
//
// THE ONLY DEPENDENCY. There is no field writer, no TargetStore, and nothing else
// that could reach a row: the type signature is the structural proof that the
// board cannot bypass governance, and adding such a dependency later would
// require changing this constructor.
func NewBoard(proposer *collab.Proposer) *Board {
	return &Board{proposer: proposer}
}

// Solve files a typed field proposal for a solved query. It does NOT write the
// field — that happens only when the ordinary governance path accepts the
// proposal, which is what makes this the board rather than a bypass.
//
// The order of checks, and why:
//
//  1. resolver  — an unidentified author cannot be reviewed, so there is no point
//     proceeding to build a proposal no reviewer could attribute.
//  2. value or evidence — a proposal asserting nothing is noise in the governance
//     queue, and a queue full of noise is a queue nobody reads.
//
// Only then does it touch the proposer, so collab's own validation (the
// vocabulary, the value parsing, the sticky rejection) is the last word rather
// than being pre-empted by a duplicate rule here.
func (b *Board) Solve(ctx context.Context, s Solve) (*collab.Proposal, error) {
	if s.ResolverID == 0 {
		return nil, fmt.Errorf("%w: query %q", ErrNoResolver, s.Query.ID)
	}
	if s.Query.Value == nil && len(s.Query.Evidence) == 0 {
		return nil, fmt.Errorf("%w: query %q", ErrNoValue, s.Query.ID)
	}
	if b.proposer == nil {
		return nil, errors.New("ident: board has no proposer wired, which cannot happen in production")
	}

	author := s.ResolverID
	prop, err := b.proposer.Create(ctx, collab.Proposal{
		TargetType: s.Query.TargetType,
		TargetID:   s.Query.TargetID,
		Field:      s.Query.Field,
		// The CURRENT value, which the board did not read and did not change. §5.3
		// proposals carry old and new so a reviewer sees the diff; a board that
		// files old=nil always makes every proposal look like "set from nothing",
		// which hides the actual disagreement -- exactly the case the reviewer is
		// there to judge.
		//
		// Left nil when nothing is known yet, which is honest: the board does not
		// own the target and must not pretend to. The proposer fills it in from
		// storage where it can.
		OldValue: s.Query.CurrentValue,
		NewValue: s.Query.Value,
		// The evidence IS the rationale. A reviewer deciding a proposal the board
		// filed needs to see why it was filed, and the alternative — evidence in a
		// side table the review screen does not read — is how the evidence stops
		// being available at the moment it is needed.
		Rationale: renderRationale(s.Query),
		AuthorID:  author,
	})
	if err != nil {
		// Passed through unwrapped where it is already a governance error, so a
		// caller can errors.Is against ErrFieldNotProposable or ErrStickyRejected
		// and get the precise reason. A board that re-wrapped these would force
		// every caller to string-match.
		if errors.Is(err, collab.ErrFieldNotProposable) {
			return nil, fmt.Errorf("%w: %q: %v", ErrNotProposable, s.Query.Field, err)
		}
		return nil, err
	}

	return prop, nil
}

// renderRationale turns evidence into the proposal's rationale text.
//
// It is TEXT and not a structured field, deliberately. The rationale is what a
// reviewer reads in the governance UI, and a reviewer needs a sentence; storing
// evidence as a blob here would mean the review screen has to parse it, and a
// review screen that cannot parse it is a review screen that ignores it.
func renderRationale(q Query) string {
	r := "identified via board"
	if len(q.Evidence) == 0 {
		// Reachable only when Value != nil and evidence is empty, which Solve
		// allows: an assertion with evidence attached by some other route is
		// legitimate, and refusing it would be inventing a requirement.
		return r
	}
	for _, e := range q.Evidence {
		r += fmt.Sprintf("\n- %s %s", e.Kind, e.Ref)
		if e.Note != "" {
			r += fmt.Sprintf(" (%s)", e.Note)
		}
	}
	return r
}
