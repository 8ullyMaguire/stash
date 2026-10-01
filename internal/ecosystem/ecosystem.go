// Package ecosystem holds the two outbound surfaces: sync that writes, and public
// indexing that must not.
//
// M7 step 7.7 (R054, R057, R058, R060). R055 (browser extension) and R056 (mobile
// app) are NOT here — §6a.22 puts both in separate repositories and out of core
// scope, so nothing in this package pretends to cover them.
//
// THE SHARED IDEA. Both surfaces take something from elsewhere and act on a
// user's library, which is exactly the shape §6a.13 and §6a.21 each refuse. Sync
// arrives as somebody else's edit; indexing makes a user's library readable from
// the open internet. Both are useful, both are dangerous in one specific way, and
// in both cases the safe behaviour is a refusal rather than a policy — so neither
// is expressed as a configurable option.

package ecosystem

import (
	"context"
	"errors"
	"fmt"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/internal/ident"
)

// IncomingEdit is an edit arriving from outside this instance — the Stash app's
// push direction, or another client's.
type IncomingEdit struct {
	TargetType   string
	TargetID     int
	Field        string
	Value        *string
	CurrentValue *string

	// Origin is who sent it: "stash-app", "stash-forge-peer", or a client name.
	// Recorded in the proposal's rationale, because §4.2's audit trail is the only
	// way an operator later finds out where a field's value came from.
	Origin string

	// AuthorID is the local user this edit is attributed to.
	//
	// NOT OPTIONAL, and this is the requirement §6a.19 states: sync pushes edits
	// "through the proposal path", so a pushed edit is filed by somebody. A push
	// with no attributable author is an unattributable proposal, which is the same
	// thing internal/ident already refuses to create.
	AuthorID int
}

// Errors, kept separate because each is a different operator problem.
var (
	// ErrNoOrigin means an incoming edit did not say where it came from. Refused:
	// an edit whose provenance is unknown cannot be reviewed, and the audit trail
	// is the whole point of filing a proposal rather than writing a field.
	ErrNoOrigin = errors.New("ecosystem: an incoming edit must name its origin")

	// ErrNoAuthor means an incoming edit has no local user to attribute it to.
	ErrNoAuthor = errors.New("ecosystem: an incoming edit must name an author")

	// ErrNotPublishable means the target may not be published, so neither may it
	// be indexed or written by a remote party. Shares §6a.2's consent state.
	ErrNotPublishable = errors.New("ecosystem: target is opted out and is neither synced nor indexed")
)

// Publishable reports whether an entity's owner permits it to leave this instance.
//
// §6a.21 calls public indexing "a NARROWER surface than the public read
// endpoint", governed by the same consent state. This is that consent state, and it
// is the same one preservation.IsReplicationSubject reads — which is the point: an
// entity opted out of sharing is not replicated, not indexed, and not written by a
// remote client, and three mechanisms reading one predicate is what keeps them
// consistent.
func Publishable(share collab.ShareChoice) bool {
	// Opted-IN only, not "not opted-out": an unrecognised value must not publish
	// somebody's library, for the same reason it must not replicate it.
	return share == collab.ChoiceOptedIn
}

// Syncer applies incoming edits from outside the instance.
//
// ITS ONLY DEPENDENCY IS THE IDENT BOARD, which is the write-through. There is no
// target writer and no field writer here for the same reason internal/ident has
// none: §6a.19 says sync pushes edits "through the proposal path", and a function
// that could write a field directly would make that statement untrue.
type Syncer struct {
	board *ident.Board
}

// NewSyncer wires a syncer to the proposal path.
func NewSyncer(board *ident.Board) *Syncer {
	return &Syncer{board: board}
}

// Push files an incoming edit as a proposal. It does NOT write the field — that
// happens only when the ordinary governance path accepts it, exactly as for the
// ident board.
//
// §6a.19: "push edits through the proposal path (§6a.13's rule applies to sync
// exactly as it does to the ident board)". So this is the ident board's Solve with
// the origin recorded, and it is deliberately the SAME function rather than a
// parallel implementation: two write-through paths would be two places for the
// governance bypass to hide.
func (s *Syncer) Push(ctx context.Context, e IncomingEdit) (*collab.Proposal, error) {
	if e.Origin == "" {
		return nil, fmt.Errorf("%w: target %s/%d field %q",
			ErrNoOrigin, e.TargetType, e.TargetID, e.Field)
	}
	if e.AuthorID == 0 {
		return nil, fmt.Errorf("%w: origin %q", ErrNoAuthor, e.Origin)
	}
	if s.board == nil {
		return nil, errors.New("ecosystem: syncer has no board wired, which cannot happen in production")
	}

	return s.board.Solve(ctx, ident.Solve{
		Query: ident.Query{
			// The query id carries the origin so a reviewer reading the proposal
			// can tell where it came from without joining against anything.
			ID:           fmt.Sprintf("%s:%s/%d", e.Origin, e.TargetType, e.TargetID),
			TargetType:   e.TargetType,
			TargetID:     e.TargetID,
			Field:        e.Field,
			Value:        e.Value,
			CurrentValue: e.CurrentValue,
			Evidence: []ident.Evidence{
				{Kind: "sync", Ref: e.Origin, Note: "pushed from outside this instance"},
			},
		},
		ResolverID: e.AuthorID,
	})
}

// IndexDecision is whether an entity may get a public, indexable page (§6a.21).
type IndexDecision struct {
	// Indexable means the page may be created and indexed.
	Indexable bool
	// Reason explains a refusal, and exists because an operator who cannot see why
	// something is not indexed will eventually index it by hand.
	Reason string
}

// DecideIndexing answers §6a.21: does this entity get a public page?
//
// THE RULE, and §6a.21 names the exact reasoning it exists to stop: "the temptation
// to treat 'it's metadata, not content' as a reason to index is exactly the
// reasoning non-negotiable #7 exists to stop". So consent is consulted FIRST and
// the entity's own fields are never examined — a scene whose owner opted out gets
// no page whether or not it is complete, popular, or already referenced by a
// neighbouring entity.
//
// A not-yet-published entity is also not indexed. §6a.21: "a page for an entity the
// owner has not published does not get created", and "published" is not the same
// question as "has metadata" — an entity exists locally from the moment it is
// imported, long before its owner agrees to any of it leaving the instance.
func DecideIndexing(share collab.ShareChoice, published bool) IndexDecision {
	if !Publishable(share) {
		return IndexDecision{
			Indexable: false,
			Reason: "the owner has not published this entity; §6a.21 makes public " +
				"indexing a narrower surface than the public read endpoint",
		}
	}
	if !published {
		return IndexDecision{
			Indexable: false,
			Reason:    "the entity exists locally but has not been published by its owner",
		}
	}
	return IndexDecision{Indexable: true}
}

// SDK is the shape an outbound integration needs, and it exists to make the
// REST/webhook requirement (R054) testable as an interface rather than as a claim.
//
// The interface is what this package CAN speak today. REST, SDKs and webhooks are
// not built; what is here is the boundary they will be built against, so the
// consent and ranking rules they must respect are stated once rather than three
// times.
type SDK interface {
	// Query executes a read against this instance's public surface.
	//
	// It takes a READ view, not the database. An SDK that could reach storage would
	// be able to return an opted-out entity, and R054's public API would then be a
	// hole in §6a.21 that nobody had to intend.
	Query(ctx context.Context, q PublicQuery) ([]PublicEntity, error)
}

// PublicQuery is a read against the public surface. Every field is a filter the
// public API will apply BEFORE consent is checked, never after.
type PublicQuery struct {
	// Limit is bounded here rather than at the transport, because an unbounded
	// public read on a federated mesh is a way to enumerate the whole library.
	Limit int
	// Offset pages through results.
	Offset int
}

// MaxPublicLimit caps a public read. A library with 100k scenes must not be
// enumerable in 100k requests, and the cap is in this package so every SDK, REST
// handler and webhook obeys the same one.
const MaxPublicLimit = 100

// PublicEntity is one entity as the outside world may see it.
//
// The shape is deliberately thin: no consent state, no internal ids beyond the
// public one, no storage path. An entity that reached the public surface carrying
// its own sharing decision would let a caller cache the answer and use it later —
// which is precisely the failure mode §6a.2's per-instance consent exists to
// prevent, since consent is revocable.
type PublicEntity struct {
	// PublicID is the instance-scoped identifier. §6a.6: across a node boundary an
	// id is an (instance, local) pair, and this is the local half.
	PublicID string
	Title    string
	// Published is carried so a consumer can cache the fact it was published
	// rather than re-asking. It is NOT the consent state -- consent is revocable
	// and must be re-checked, so it is deliberately absent.
	Published bool
}

// Query applies the public query's bounds.
//
// Bounded at the QUERY rather than at each transport, so the cap cannot be
// forgotten in one of three implementations.
func (q PublicQuery) bounded() PublicQuery {
	if q.Limit <= 0 {
		q.Limit = MaxPublicLimit
	}
	if q.Limit > MaxPublicLimit {
		q.Limit = MaxPublicLimit
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	return q
}

// BoundQuery is the exported form of the bound, for a transport that needs the
// effective limit to advertise in a response.
func BoundQuery(q PublicQuery) PublicQuery { return q.bounded() }
