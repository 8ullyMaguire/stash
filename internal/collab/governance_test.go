package collab_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stashapp/stash/internal/collab"
)

// Each test is named for the rule it protects, so a failure says which rule
// broke rather than just "governance is wrong".

func TestQuorum_AcceptsAtThreshold(t *testing.T) {
	// Net exactly equal to the threshold accepts. An off-by-one here would
	// either strand proposals that ought to pass or accept ones that should not,
	// and neither is visible without this exact case.
	got := collab.Evaluate(collab.DefaultPolicy(), collab.VoteCount{
		Net:    3,
		Voters: 3,
	})
	assert.Equal(t, collab.DecisionAccepted, got)
}

func TestQuorum_RejectsOneBelowThreshold(t *testing.T) {
	// The counterpart: one vote short stays pending, so an author watching a
	// proposal climb sees it hold at pending until it actually qualifies.
	got := collab.Evaluate(collab.DefaultPolicy(), collab.VoteCount{
		Net:    2,
		Voters: 3,
	})
	assert.Equal(t, collab.DecisionPending, got)
}

func TestQuorum_MinVotersBlocksOneAccountWithManyVotes(t *testing.T) {
	// THE SYBIL CASE, and the single most important test in this package.
	//
	// Net 4 comfortably clears the threshold of 3, but it came from ONE distinct
	// user. Without MinVoters, quorum is not consensus — it is a number that one
	// determined person crosses at will, and an author who keeps voting is the
	// cheapest possible attack on the system.
	//
	// This is a property of the DESIGN, not of a fixture: the net tally cannot
	// distinguish "four people agreed" from "one person voted four times", so
	// the only place that can is a separate count of distinct users.
	got := collab.Evaluate(collab.Policy{QuorumThreshold: 3, MinVoters: 3}, collab.VoteCount{
		Net:    4,
		Voters: 1,
	})
	assert.Equal(t, collab.DecisionPending, got,
		"a lopsided tally from too few distinct users must not accept; MinVoters is the Sybil guard")
}

func TestQuorum_MinVotersAlsoBlocksTwoUsers(t *testing.T) {
	// Two users is still short of MinVoters=3, and this is a realistic shape:
	// an author plus one ally. Net is 2 here, so it would fail the threshold
	// anyway — the point is that MinVoters is an INDEPENDENT clause, checked
	// because a policy with a low threshold and a high MinVoters is a legitimate
	// configuration where only this test exercises it.
	got := collab.Evaluate(collab.Policy{QuorumThreshold: 1, MinVoters: 3}, collab.VoteCount{
		Net:    2,
		Voters: 2,
	})
	assert.Equal(t, collab.DecisionPending, got,
		"MinVoters must be checked independently of the net threshold")
}

func TestQuorum_ZeroThresholdLeavesModeratorOnly(t *testing.T) {
	// A disabled quorum is a supported configuration, not a degenerate one: a
	// private instance may want every change moderated. What must NOT happen is
	// a unanimous vote auto-accepting, which is what a zero threshold would do
	// if the comparison were written as `net >= threshold`.
	p := collab.Policy{QuorumThreshold: 0, MinVoters: 3}
	v := collab.VoteCount{Net: 100, Voters: 50}

	assert.Equal(t, collab.DecisionPending, collab.Evaluate(p, v),
		"with the quorum path off, a unanimous vote must still not accept")
	assert.Equal(t, collab.DecisionAccepted, collab.Evaluate(p, collab.VoteCount{
		Net: 100, Voters: 50, ModeratorApproved: true,
	}), "moderator-only governance must still accept a moderator's approval")
}

func TestModerator_OverridesPendingQuorum(t *testing.T) {
	// The moderator path exists and is decisive, which is what makes
	// zero-threshold governance workable at all.
	got := collab.Evaluate(collab.DefaultPolicy(), collab.VoteCount{
		Net:               0,
		Voters:            0,
		ModeratorApproved: true,
	})
	assert.Equal(t, collab.DecisionAccepted, got)
}

func TestModerator_CannotResurrectWithdrawnOrSuperseded(t *testing.T) {
	// Order matters. A withdrawn proposal that a moderator "approves" out of
	// habit is a bug, not a rescue: the author took it back, and the only correct
	// reading is that it is not happening. Step 1 precedes step 2 for this reason.
	p := collab.DefaultPolicy()

	assert.Equal(t, collab.DecisionRejected, collab.Evaluate(p, collab.VoteCount{
		ModeratorApproved: true, Withdrawn: true,
	}), "a withdrawn proposal must stay withdrawn even if a moderator approves it")

	assert.Equal(t, collab.DecisionRejected, collab.Evaluate(p, collab.VoteCount{
		ModeratorApproved: true, Superseded: true,
	}), "a superseded proposal must not be applied by a stale approval")
}

func TestSelfAccept_RejectedByDefault(t *testing.T) {
	// The author voted for their own proposal, net is at the threshold, and there
	// are enough distinct voters — but the author's vote is not a second
	// opinion, so it does not count toward the tally. Removing one from Net
	// drops it below the threshold.
	got := collab.Evaluate(collab.Policy{QuorumThreshold: 3, MinVoters: 3}, collab.VoteCount{
		Net:      3,
		Voters:   3,
		SelfVote: true,
	})
	assert.Equal(t, collab.DecisionPending, got,
		"an author-vote must not count toward their own proposal's quorum")
}

func TestSelfAccept_StillSubjectToMinVoters(t *testing.T) {
	// AllowSelfAccept removes the author's EXCLUSION. It does not bypass the
	// threshold or the distinct-voter count. Without this test, a future
	// refactor could make AllowSelfAccept a blanket "auto-accept if the author
	// voted", which is the exact hole the flag's name would then invite.
	got := collab.Evaluate(collab.Policy{QuorumThreshold: 3, MinVoters: 3, AllowSelfAccept: true},
		collab.VoteCount{Net: 3, Voters: 1, SelfVote: true})
	assert.Equal(t, collab.DecisionPending, got,
		"AllowSelfAccept must not bypass MinVoters")
}

func TestSelfAccept_AllowedWhenConfigured(t *testing.T) {
	got := collab.Evaluate(collab.Policy{QuorumThreshold: 3, MinVoters: 3, AllowSelfAccept: true},
		collab.VoteCount{Net: 3, Voters: 3, SelfVote: true})
	assert.Equal(t, collab.DecisionAccepted, got,
		"an instance that permits author-votes must actually reach quorum through one")
}

func TestSelfAccept_AuthorVoteExcludedButOthersStillCount(t *testing.T) {
	// Net 4 with a self-vote: excluding the author's one leaves 3, which still
	// clears the threshold. This is the case that distinguishes "the author's
	// vote does not count" from "the author can never accept their own
	// proposal" — the latter would make the flag pointless and the former is
	// what the spec says.
	got := collab.Evaluate(collab.Policy{QuorumThreshold: 3, MinVoters: 3}, collab.VoteCount{
		Net:      4,
		Voters:   4,
		SelfVote: true,
	})
	assert.Equal(t, collab.DecisionAccepted, got,
		"excluding the author's vote must leave the OTHER votes countable")
}

func TestPolicy_NegativeThresholdDoesNotAutoAccept(t *testing.T) {
	// A typo in the config file must not disable governance. A negative
	// threshold normalised to zero is moderator-only; the alternative — honouring
	// it — would make every proposal auto-accept.
	p := collab.Policy{QuorumThreshold: -1, MinVoters: 3}
	got := collab.Evaluate(p, collab.VoteCount{Net: 0, Voters: 0})
	assert.Equal(t, collab.DecisionPending, got,
		"a negative threshold must be clamped to moderator-only, never to accept-everything")

	assert.Equal(t, 0, p.Normalised().QuorumThreshold)
}

func TestPolicy_MinVotersZeroIsNotAVacuousCheck(t *testing.T) {
	// MinVoters=0 would make `voters >= 0` always true, which is the same Sybil
	// hole as having no check at all — wearing a setting that looks like it does
	// something. Clamped to 1.
	p := collab.Policy{QuorumThreshold: 1, MinVoters: 0}
	assert.Equal(t, 1, p.Normalised().MinVoters)

	// And with the clamp, a single voter cannot satisfy MinVoters=1... actually
	// it can, which is correct: MinVoters=1 asked for one voter. The point of the
	// clamp is that it is not ZERO, i.e. not a check that passes on nobody.
	got := collab.Evaluate(p, collab.VoteCount{Net: 1, Voters: 0})
	assert.Equal(t, collab.DecisionPending, got,
		"a tally with zero distinct voters must never accept")
}

func TestDefaultPolicy_UsesTheAgreedNumbers(t *testing.T) {
	p := collab.DefaultPolicy()
	assert.Equal(t, 3, p.QuorumThreshold)
	assert.Equal(t, 3, p.MinVoters)
	assert.False(t, p.AllowSelfAccept)
}

func TestDecision_Terminal(t *testing.T) {
	assert.False(t, collab.DecisionPending.Terminal(),
		"a pending proposal keeps accumulating votes and must be re-evaluated")
	assert.True(t, collab.DecisionAccepted.Terminal())
	assert.True(t, collab.DecisionRejected.Terminal(),
		"a rejected proposal must not be resurrected by a late vote")

	assert.Equal(t, "pending", collab.DecisionPending.String())
	assert.Equal(t, "accepted", collab.DecisionAccepted.String())
	assert.Equal(t, "rejected", collab.DecisionRejected.String())
}
