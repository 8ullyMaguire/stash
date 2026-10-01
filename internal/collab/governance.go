// Package collab implements StashForge's governance model: who may change
// shared content, and by what process.
//
// # Why this package has no database and no HTTP
//
// The rules in this file decide whether a change to shared metadata is
// accepted. They are the part of the system most worth testing exhaustively and
// the part most expensive to get wrong, so they are expressed as a pure function
// of (Policy, VoteCount) -> Decision with no I/O at all. Everything else in
// StashForge can be reached around; this cannot, because there is nothing here
// to reach around.
//
// The corollary is that the tests are table-shaped and fast, and that a caller
// cannot smuggle a side effect past a reviewer's eye by hiding it in here.
package collab

// Decision is the outcome of evaluating a proposal's votes.
type Decision int

const (
	// DecisionPending means the proposal has not reached quorum and no moderator
	// has ruled on it. It is the resting state of almost every proposal.
	DecisionPending Decision = iota

	// DecisionAccepted means the proposal's value will be applied to its target.
	DecisionAccepted

	// DecisionRejected means the proposal will not be applied. Rejection is
	// sticky per (target, field, author) — see StickyRejectionKey.
	DecisionRejected
)

func (d Decision) String() string {
	switch d {
	case DecisionAccepted:
		return "accepted"
	case DecisionRejected:
		return "rejected"
	default:
		return "pending"
	}
}

// Terminal reports whether a decision is final, i.e. whether re-evaluating with
// more votes could change it.
//
// This is a property of the state, not of the policy, and the distinction
// matters: a pending proposal keeps accumulating votes, a rejected one does not.
// A caller that re-evaluates a rejected proposal after a late vote arrives
// would resurrect it.
func (d Decision) Terminal() bool {
	return d != DecisionPending
}

// Policy is the configurable half of the governance rules.
type Policy struct {
	// QuorumThreshold is the net-vote count that auto-accepts a proposal.
	// Zero disables the quorum path entirely, leaving moderator-only.
	//
	// Negative values are treated as zero rather than as "accept immediately".
	// A threshold that auto-accepts everything is never what an operator meant,
	// and silently honouring it would make a typo in the config file disable
	// the entire governance system.
	QuorumThreshold int

	// MinVoters rejects a quorum decision reached by too few DISTINCT users, so
	// one account with several votes cannot cross a threshold.
	//
	// This is the single most important field in the struct. Without it, quorum
	// is not consensus — it is a number that one determined user, or a handful
	// of accounts they created, can cross at will. Ignored when
	// QuorumThreshold == 0.
	MinVoters int

	// AllowSelfAccept lets a proposal's author accept it with their own vote.
	// Off by default: an author-vote is not a second opinion.
	AllowSelfAccept bool

	// Weighted is the M2b arithmetic: reputation-weighted ballots, decay and
	// Sybil damping. Added in M2b and left at its zero value by DefaultPolicy,
	// which is why M2's behaviour is unchanged by its arrival.
	//
	// The two halves are independently optional. Weighted.Threshold == 0 keeps
	// flat counting; QuorumThreshold == 0 keeps moderator-only; both zero is
	// fully manual. Every combination is a supported mode, which is what makes
	// the migration possible: an instance that has not chosen a reputation
	// model has to be able to keep running the one it has.
	Weighted WeightedPolicy
}

// DefaultPolicy returns the proposed defaults, noted in the spec as
// QuorumThreshold=3, MinVoters=3.
//
// MinVoters is 3 and NOT 2 deliberately. Two distinct users agreeing is two
// people's opinions, which is what a vote is for; three is where it starts
// looking like agreement rather than a coincidence, and it means no single user
// can ever be half of a quorum.
func DefaultPolicy() Policy {
	return Policy{
		QuorumThreshold: 3,
		MinVoters:       3,
		AllowSelfAccept: false,
	}
}

// Normalised returns a copy with unusable values corrected.
//
// Called by Evaluate so that every caller gets the same treatment, rather than
// each call site remembering to clamp. A negative threshold becomes 0
// (moderator-only) and a MinVoters below 1 becomes 1, because a MinVoters of 0
// would make the distinct-voter check vacuous — which is the same Sybil hole as
// no check at all, wearing a setting that looks like it is doing something.
func (p Policy) Normalised() Policy {
	out := p
	if out.QuorumThreshold < 0 {
		out.QuorumThreshold = 0
	}
	if out.MinVoters < 1 {
		out.MinVoters = 1
	}
	return out
}

// VoteCount is the tally a proposal carries, computed from the vote rows rather
// than stored as a counter.
//
// No stored counters anywhere: scores are always Σ over the current vote rows.
// A counter is a cache that can disagree with its source, and the disagreement
// is invisible until it decides an election.
type VoteCount struct {
	// Net is Σ(+1) − Σ(−1).
	Net int

	// Voters is the number of DISTINCT users who have voted. Not the number of
	// vote rows, and not the number of accounts that could have voted.
	Voters int

	// SelfVote is true when the proposal's author is among the voters. Recorded
	// separately from Net because the author's vote may still count toward the
	// quorum tally even when it cannot, by itself, accept the proposal.
	SelfVote bool

	// ModeratorApproved is true when a moderator has explicitly approved.
	ModeratorApproved bool

	// Withdrawn and Superseded are terminal states set outside the vote tally:
	// an author withdrawing their own proposal, or a newer proposal on the same
	// (target, field) replacing this one. Checked before everything else,
	// because a proposal the author has withdrawn must not be resurrected by a
	// moderator clicking accept out of habit.
	Withdrawn  bool
	Superseded bool
}

// Evaluate decides a proposal. This is the whole governance rule, in order.
//
// The order is load-bearing and is fixed by spec §5:
//
//  1. Withdrawn or Superseded -> rejected, terminal.
//  2. Moderator approval -> accepted.
//  3. QuorumThreshold > 0 AND distinct voters >= MinVoters AND net >= threshold
//     -> accepted.
//  4. Otherwise pending.
//
// Step 1 comes before step 2 on purpose. A withdrawn proposal that a moderator
// approves afterwards is a bug, not a rescue: the author took it back, and the
// only correct reading of that is that it is not happening.
//
// Step 2 before step 3 because both paths always exist. Which one is reachable
// is a matter of threshold, not of precedence.
//
// Step 3 is a conjunction, and every clause is load-bearing. Dropping
// `Voters >= MinVoters` turns consensus back into a number one user can cross.
func Evaluate(p Policy, v VoteCount) Decision {
	p = p.Normalised()

	// 1. Terminal states the author or a newer proposal already decided.
	if v.Withdrawn || v.Superseded {
		return DecisionRejected
	}

	// 2. A moderator ruling is decisive on its own.
	if v.ModeratorApproved {
		return DecisionAccepted
	}

	// 3. Quorum. Zero threshold means the quorum path is switched off, which
	//    leaves moderator-only governance — a supported configuration, not a
	//    degenerate one.
	if p.QuorumThreshold == 0 {
		return DecisionPending
	}

	if v.Net < p.QuorumThreshold {
		return DecisionPending
	}

	// The Sybil guard. A proposal that clears the net threshold on too few
	// distinct users stays pending no matter how lopsided the tally is.
	if v.Voters < p.MinVoters {
		return DecisionPending
	}

	// 4. The author's own vote is not a second opinion unless the instance says
	//    it is. Checked last so that a self-accepting proposal is still subject
	//    to the quorum and voter-count rules — AllowSelfAccept removes the
	//    exclusion, it does not bypass the threshold.
	if v.SelfVote && !p.AllowSelfAccept {
		// The author's vote is excluded from the tally, which may drop Net below
		// the threshold. Recomputing the effective net is what makes "the
		// author's vote does not count" true rather than advisory.
		if v.Net-1 < p.QuorumThreshold {
			return DecisionPending
		}
	}

	return DecisionAccepted
}

// StickyRejectionKey identifies the (target, field, author) triple that a
// rejection is sticky against.
//
// Rejection sticks per AUTHOR, not per proposal: the same author proposing the
// same field change again after a rejection is the same request, and a proposal
// system that lets an author re-ask until they get their way is a queue, not a
// governance process. The key deliberately excludes the proposal id, because
// including it would make every retry a fresh key and the rule inert.
//
// Returned as a comparable struct rather than a formatted string so it can be
// used as a map key and as a database column value without a parsing step.
type StickyRejectionKey struct {
	TargetType string
	TargetID   int
	Field      string
	AuthorID   int
}

// NewStickyRejectionKey builds the key for a proposal.
func NewStickyRejectionKey(targetType string, targetID int, field string, authorID int) StickyRejectionKey {
	return StickyRejectionKey{
		TargetType: targetType,
		TargetID:   targetID,
		Field:      field,
		AuthorID:   authorID,
	}
}

// IsBlocked reports whether a new proposal on this key is blocked by a prior
// rejection of the same (target, field, author).
func IsBlocked(rejected map[StickyRejectionKey]bool, key StickyRejectionKey) bool {
	return rejected[key]
}

// RecordRejection marks a key as rejected. Returns the updated set so the
// caller cannot forget to store it.
//
// A superseded proposal is NOT recorded: supersession is a different terminal
// state from rejection, and conflating them would block the newer proposal's
// author from ever editing that field again.
func RecordRejection(rejected map[StickyRejectionKey]bool, key StickyRejectionKey, superseded bool) map[StickyRejectionKey]bool {
	if superseded {
		return rejected
	}
	if rejected == nil {
		rejected = make(map[StickyRejectionKey]bool)
	}
	rejected[key] = true
	return rejected
}
