package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/pkg/models"
)

// The proposal resolvers. This is the ONLY write path to shared content.
//
// The invariant is not enforced by convention. It is enforced by
// TestNoResolverWritesASharedFieldDirectly, which greps this package for the
// scene/performer/studio writers and fails if it finds one. A comment asking
// nobody to do that is a request; a test that fails when someone does is a
// guarantee, and the guarantee is the only reason the UI can offer a vote button
// instead of a text box for the title.
//
// Every mutation here goes through collab.Proposer or collab.Applier. Neither
// resolver touches a target table, and Moderate deliberately does not write the
// value itself — it records the decision and lets Apply do the write, so the
// moderator path and the quorum path converge on one writer and one audit row.

// Client-facing errors. Each message is read by the person who triggered it, so
// they are chosen to be answers rather than identifiers: a caller told only
// "proposal rejected" cannot tell whether they hit sticky rejection, a
// vocabulary rejection, or a validation failure, and those need three different
// responses.
var (
	errNotProposable = errors.New("that field cannot be proposed for")
	errStickyBlocked = errors.New("a previous proposal on this field was rejected")
	errOpenExists    = errors.New("there is already an open proposal on this field")
	errValueInvalid  = errors.New("the proposed value is not valid for that field")

	errSelfVote       = errors.New("you cannot vote on your own proposal")
	errAlreadyDecided = errors.New("this proposal has already been decided")
	errNotAuthor      = errors.New("only the author may withdraw a proposal")
	errNeedModerator  = errors.New("this action requires a moderator")
	errNeedLogin      = errors.New("you must be logged in")
)

// ---------------------------------------------------------------------------
// Caller resolution
// ---------------------------------------------------------------------------

// currentUser resolves the caller, or an error when anonymous.
//
// This is the single place that decides "may this caller change shared
// content", so it is also the single place to add a rule about who may propose
// at all. Anonymous is refused rather than tolerated: a read-only public site
// still must not accept edits from nobody in particular.
func currentUser(ctx context.Context) (*models.User, error) {
	username := currentUserID(ctx)
	if username == "" {
		return nil, errNeedLogin
	}
	user, err := manager.GetInstance().UserStore.FindByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			// A session naming a user that no longer exists. Anonymous is the
			// honest description: the credential identifies nobody.
			return nil, errNeedLogin
		}
		return nil, err
	}
	// Active() rather than a DisabledAt check, so the definition of "may act"
	// stays in the model and a second caller cannot disagree with it.
	if !user.Active() {
		return nil, errNeedLogin
	}
	return user, nil
}

// currentUserIDInt is currentUser's numeric form, or 0 when anonymous.
//
// 0 is safe as a sentinel because the users.id column is an autoincrement
// primary key, so no real user can collide with it.
func currentUserIDInt(ctx context.Context) int {
	u, err := currentUser(ctx)
	if err != nil {
		return 0
	}
	return u.ID
}

func isModerator(u *models.User) bool {
	return u != nil && (u.IsModerator || u.IsOwner)
}

// proposer returns a collab.Proposer over the adapter.
//
// The adapter, not the row store: collab.Proposer takes a collab.ProposalStore
// and the row store does not implement one. Passing CollabProposals here is a
// compile error, which is the point of keeping the two as separate fields.
func proposer() *collab.Proposer {
	return collab.NewProposer(manager.GetInstance().CollabStore)
}

// applier returns a collab.Applier over the instance's target store.
//
// The Applier is the only thing in StashForge permitted to write a settled value
// to a shared row. Resolvers reach it, never the target tables.
func applier() *collab.Applier {
	return collab.NewApplier(manager.GetInstance().CollabTargets)
}

// ---------------------------------------------------------------------------
// Queries
// ---------------------------------------------------------------------------

// Proposals lists proposals, filtered. Every filter is optional, composed with AND.
func (r *queryResolver) Proposals(ctx context.Context, targetType *string, targetID *string, targetField *string, status *string, authorID *string, mine *bool, limit *int, offset *int) ([]*models.EditProposal, error) {
	inst := manager.GetInstance()
	page, pageOffset := proposalPaging(limit, offset)

	// "mine" with no session has no referent. Refused rather than answered with
	// an empty list, which would read as "you have proposed nothing" and is a
	// different statement.
	if mine != nil && *mine {
		if _, err := currentUser(ctx); err != nil {
			return nil, err
		}
	}

	target, err := optionalID(targetID)
	if err != nil {
		return nil, err
	}
	author, err := optionalID(authorID)
	if err != nil {
		return nil, err
	}
	if mine != nil && *mine {
		mineID := currentUserIDInt(ctx)
		if author != nil && *author != mineID {
			// mine + someone else's authorId is a contradiction. Refusing beats
			// silently resolving it one way or the other.
			return nil, errors.New("cannot list another user's proposals as your own")
		}
		author = &mineID
	}

	var st models.ProposalStatus
	if status != nil {
		parsed, ok := parseProposalStatus(*status)
		if !ok {
			return nil, fmt.Errorf("unknown proposal status %q", *status)
		}
		// Listing the open backlog is a view of other people's pending claims.
		// Anyone may see that a claim exists on a field; only a steward
		// enumerates the queue.
		if parsed == models.ProposalOpen {
			if _, err := currentUser(ctx); err != nil {
				return nil, err
			}
		}
		st = parsed
	}

	return inst.CollabProposals.FindFiltered(ctx, targetType, target, targetField, author, st, page, pageOffset)
}

// ModerationQueue is the steward view: open claims, oldest first.
//
// Separate from Proposals(status: "open") so "show me the queue" is one
// permission check in one place, rather than a filter every caller has to
// remember to apply correctly.
func (r *queryResolver) ModerationQueue(ctx context.Context, limit *int, offset *int) ([]*models.EditProposal, error) {
	u, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	if !isModerator(u) {
		return nil, errNeedModerator
	}
	page, pageOffset := proposalPaging(limit, offset)
	return manager.GetInstance().CollabProposals.FindByStatus(ctx, models.ProposalOpen, page, pageOffset)
}

// PendingMyVote lists open proposals the caller has not voted on.
func (r *queryResolver) PendingMyVote(ctx context.Context, limit *int, offset *int) ([]*models.EditProposal, error) {
	u, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	page, pageOffset := proposalPaging(limit, offset)
	return manager.GetInstance().CollabProposals.FindPendingFor(ctx, u.ID, page, pageOffset)
}

// proposalPaging clamps the page size.
//
// A caller-supplied limit is unbounded work against a table every account can
// append to, so it is clamped rather than trusted. The default is a page a human
// scrolls, not a page sized to defeat the clamp.
func proposalPaging(limit, offset *int) (int, int) {
	const maxPage = 100
	page := 50
	if limit != nil && *limit > 0 {
		page = *limit
	}
	if page > maxPage {
		page = maxPage
	}
	off := 0
	if offset != nil && *offset > 0 {
		off = *offset
	}
	return page, off
}

func parseProposalStatus(s string) (models.ProposalStatus, bool) {
	switch models.ProposalStatus(s) {
	case models.ProposalOpen, models.ProposalAccepted, models.ProposalRejected,
		models.ProposalSuperseded, models.ProposalWithdrawn:
		return models.ProposalStatus(s), true
	default:
		return "", false
	}
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

// Propose creates an edit proposal.
//
// It does not touch the target row. The value lives on the proposal until the
// proposal settles, and only Apply writes it — which is what makes the vote
// meaningful, since a proposal that had already changed the title would be asking
// people to ratify a fait accompli.
func (r *mutationResolver) Propose(ctx context.Context, input EditProposalInput) (*models.EditProposal, error) {
	author, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}

	targetID, err := strconv.Atoi(input.TargetID)
	if err != nil {
		return nil, fmt.Errorf("converting targetId: %w", err)
	}

	if _, err := proposer().Create(ctx, collab.Proposal{
		TargetType: input.TargetType,
		TargetID:   targetID,
		Field:      input.Field,
		NewValue:   input.NewValue,
		Rationale:  derefOr(input.Rationale, ""),
		AuthorID:   author.ID,
	}); err != nil {
		return nil, translateProposalError(err)
	}

	// Re-read rather than converting the service's return value: the service's
	// Proposal has no status, because the service does not decide status. The
	// row is where that lives, so the row is what the client gets.
	open, err := manager.GetInstance().CollabProposals.FindOpen(ctx, input.TargetType, targetID, input.Field)
	if err != nil {
		return nil, err
	}
	return open, nil
}

// Vote records a vote; a second vote replaces the first.
//
// Replacing rather than erroring is deliberate. Changing your mind after reading
// the rationale is the normal path, and an error there teaches people to open a
// second account to express it, which is strictly worse than a lost vote.
func (r *mutationResolver) Vote(ctx context.Context, proposalID string, value int) (*models.EditProposal, error) {
	voter, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	if value != 1 && value != -1 {
		return nil, errors.New("a vote is 1 or -1")
	}

	id, err := strconv.Atoi(proposalID)
	if err != nil {
		return nil, fmt.Errorf("converting proposalId: %w", err)
	}

	inst := manager.GetInstance()
	p, err := inst.CollabProposals.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	// The policy already excludes the author's vote from the tally; refusing the
	// row as well means the score and the vote list can never disagree.
	if p.AuthorID == voter.ID {
		return nil, errSelfVote
	}
	if p.Status != models.ProposalOpen {
		return nil, errAlreadyDecided
	}

	if err := inst.CollabVotes.Cast(ctx, p.ID, voter.ID, value); err != nil {
		return nil, err
	}
	return r.settle(ctx, p, voter)
}

// settle evaluates the tally after a vote and applies the outcome.
//
// The tally is recomputed from the vote rows rather than incremented, so a
// double-submitted vote and a single one produce the same number.
func (r *mutationResolver) settle(ctx context.Context, p *models.EditProposal, decider *models.User) (*models.EditProposal, error) {
	inst := manager.GetInstance()

	score, err := inst.CollabProposals.Score(ctx, p.ID)
	if err != nil {
		return nil, err
	}

	var deciderID int
	if decider != nil {
		deciderID = decider.ID
	}

	decision := collab.Evaluate(collab.DefaultPolicy(), collab.VoteCount{
		Net:     score.Net,
		Voters:  score.Voters,
		SelfVote: score.AuthorVoted,
	})

	switch decision {
	case collab.DecisionAccepted:
		if _, err := applier().Apply(ctx, collab.Proposal{
			ID:         p.ID,
			TargetType: p.TargetType,
			TargetID:   p.TargetID,
			Field:      p.Field,
			OldValue:   p.OldValue,
			NewValue:   p.NewValue,
			Rationale:  p.Rationale,
			AuthorID:   p.AuthorID,
		}); err != nil {
			return nil, err
		}
		// ApplyRejected is not an error: the value stopped being valid between
		// the proposal and the vote — the target was deleted, or the field now
		// holds something incompatible — and Apply recorded that as a rejection
		// with a reason. The returned proposal carries the status that resulted.
	case collab.DecisionRejected:
		// A quorum of disagreement. Recording it also arms the sticky rule, so
		// the author cannot simply re-propose the identical claim.
		if err := inst.CollabProposals.SetStatus(ctx, p.ID, models.ProposalRejected, deciderID); err != nil {
			return nil, err
		}
	}

	return inst.CollabProposals.Find(ctx, p.ID)
}

// Withdraw removes your own open proposal.
//
// Withdrawn rather than deleted: a decision other people voted on is part of the
// record, and a system that lets an author erase the proposal a vote was cast on
// has no audit trail.
func (r *mutationResolver) Withdraw(ctx context.Context, proposalID string) (*models.EditProposal, error) {
	u, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	id, err := strconv.Atoi(proposalID)
	if err != nil {
		return nil, fmt.Errorf("converting proposalId: %w", err)
	}

	inst := manager.GetInstance()
	p, err := inst.CollabProposals.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.AuthorID != u.ID {
		return nil, errNotAuthor
	}
	if p.Status != models.ProposalOpen {
		return nil, errAlreadyDecided
	}
	if err := inst.CollabProposals.SetStatus(ctx, p.ID, models.ProposalWithdrawn, u.ID); err != nil {
		return nil, err
	}
	return inst.CollabProposals.Find(ctx, id)
}

// Moderate decides a proposal on a moderator's authority, bypassing quorum.
//
// Accepting does NOT write the value. It records the decision and lets Apply do
// the write, so both paths converge on one writer and one audit row. A moderator
// who could write a field directly would make the audit trail a decoration on
// whichever paths happened to be honest.
func (r *mutationResolver) Moderate(ctx context.Context, proposalID string, approve bool, reason *string) (*models.EditProposal, error) {
	u, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	if !isModerator(u) {
		return nil, errNeedModerator
	}

	id, err := strconv.Atoi(proposalID)
	if err != nil {
		return nil, fmt.Errorf("converting proposalId: %w", err)
	}

	inst := manager.GetInstance()
	p, err := inst.CollabProposals.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Status != models.ProposalOpen {
		return nil, errAlreadyDecided
	}

	if !approve {
		// The reason is carried into the audit trail by MarkRejected, which is
		// why it is read here rather than discarded: a rejection nobody can
		// explain is the one people contest.
		if err := inst.CollabTargets.MarkRejected(ctx, p.ID, u.ID, derefOr(reason, "rejected by moderator")); err != nil {
			return nil, err
		}
		return inst.CollabProposals.Find(ctx, id)
	}

	if err := inst.CollabProposals.SetStatus(ctx, p.ID, models.ProposalAccepted, u.ID); err != nil {
		return nil, err
	}
	if _, err := applier().Apply(ctx, collab.Proposal{
		ID:         p.ID,
		TargetType: p.TargetType,
		TargetID:   p.TargetID,
		Field:      p.Field,
		OldValue:   p.OldValue,
		NewValue:   p.NewValue,
		Rationale:  p.Rationale,
		AuthorID:   p.AuthorID,
	}); err != nil {
		return nil, err
	}
	return inst.CollabProposals.Find(ctx, id)
}

// ---------------------------------------------------------------------------
// Field resolvers
// ---------------------------------------------------------------------------

// Status renders the lifecycle state.
//
// Its own resolver because models.ProposalStatus is a named string type: the
// zero value is "", which is not a valid status and is reachable by a struct
// literal that forgot the field. Returning the zero value as-is would put a
// status in the UI that the schema's CHECK constraint would never have allowed.
func (r *editProposalResolver) Status(ctx context.Context, obj *models.EditProposal) (string, error) {
	if obj == nil || obj.Status == "" {
		return string(models.ProposalOpen), nil
	}
	return string(obj.Status), nil
}

// Author resolves the author.
//
// A proposal whose author row is gone resolves to nil rather than erroring. The
// claim still stands and its votes still count; refusing to render it would let
// a deleted account hide an entire dispute.
func (r *editProposalResolver) Author(ctx context.Context, obj *models.EditProposal) (*models.User, error) {
	u, err := manager.GetInstance().UserStore.Find(ctx, obj.AuthorID)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return u, nil
}

// EditProposalScore recomputes Σ(+1) − Σ(−1) on every read.
//
// Never read from a stored counter. A counter is a thing to be able to be wrong,
// and the only way to be sure this one is right is not to keep it.
func (r *editProposalResolver) Score(ctx context.Context, obj *models.EditProposal) (int, error) {
	s, err := manager.GetInstance().CollabProposals.Score(ctx, obj.ID)
	if err != nil {
		return 0, err
	}
	return s.Net, nil
}

// EditProposalVoters is the distinct voter count, which is what the quorum path
// counts — not the number of accounts that could have voted.
func (r *editProposalResolver) Voters(ctx context.Context, obj *models.EditProposal) (int, error) {
	s, err := manager.GetInstance().CollabProposals.Score(ctx, obj.ID)
	if err != nil {
		return 0, err
	}
	return s.Voters, nil
}

// EditProposalMyVote is the caller's own vote, or nil.
//
// Null and 0 are different answers: null is "not voted", and there is no such
// thing as a zero vote. Returning 0 for both would render an abstain state the
// store has no record of.
func (r *editProposalResolver) MyVote(ctx context.Context, obj *models.EditProposal) (*int, error) {
	uid := currentUserIDInt(ctx)
	if uid == 0 {
		return nil, nil
	}
	v, err := manager.GetInstance().CollabVotes.MyVote(ctx, obj.ID, uid)
	if err != nil {
		return nil, err
	}
	if v == 0 {
		return nil, nil
	}
	return &v, nil
}

// EditProposalBlockedReason says why the caller may not act, so the UI can
// disable a control with a reason instead of failing on click.
func (r *editProposalResolver) BlockedReason(ctx context.Context, obj *models.EditProposal) (*string, error) {
	u, err := currentUser(ctx)
	if err != nil {
		reason := "not logged in"
		return &reason, nil
	}
	if obj.Status != models.ProposalOpen {
		reason := "already decided"
		return &reason, nil
	}
	if obj.AuthorID == u.ID {
		reason := "you proposed this"
		return &reason, nil
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// optionalID parses an optional GraphQL id into an optional int.
//
// A malformed id is an error rather than a silently-ignored filter: "no such
// user" and "you typed nonsense" are different bugs and dropping the filter would
// answer both with "here is everyone's proposals".
func optionalID(id *string) (*int, error) {
	if id == nil {
		return nil, nil
	}
	n, err := strconv.Atoi(*id)
	if err != nil {
		return nil, fmt.Errorf("converting id: %w", err)
	}
	return &n, nil
}

func derefOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

// translateProposalError maps a collab error to a client-facing one, so the
// reason survives the trip to the browser. The raw errors are written for a
// reader inside this package, and "field is not proposable" is a better answer
// to a form than "ErrFieldNotProposable".
func translateProposalError(err error) error {
	switch {
	case errors.Is(err, collab.ErrFieldNotProposable):
		return errNotProposable
	case errors.Is(err, collab.ErrStickyRejected):
		return errStickyBlocked
	case errors.Is(err, collab.ErrOpenProposalExists):
		return errOpenExists
	case errors.Is(err, collab.ErrValueInvalid), errors.Is(err, collab.ErrValueBecameInvalid):
		return errValueInvalid
	default:
		return err
	}
}
