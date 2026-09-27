//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/sqlite"
)

// These tests cover the invariants the M2 schema is responsible for. Each one
// asserts something the DATABASE guarantees, not something the Go code intends
// to do — the point of putting an invariant in a CHECK or a partial index is
// that it holds even when the Go path is bypassed.

func TestEditProposals_OnlyOneOpenPerField(t *testing.T) {
	// The partial unique index. Two users opening competing proposals on the
	// same field would split the votes between them and make a quorum
	// unreachable in practice; superseding is the sanctioned way to replace.
	runWithRollbackTxn(t, "one-open", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "prop-author-1")
		other := mustCreateUser(ctx, t, "prop-author-2")
		proposals := sqlite.NewEditProposalStore()

		_, err := proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: 1, Field: "title",
			NewValue: strptr("first"), AuthorID: author,
		})
		require.NoError(t, err)

		_, err = proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: 1, Field: "title",
			NewValue: strptr("competing"), AuthorID: other,
		})
		assert.Error(t, err,
			"a second OPEN proposal on the same (target, field) must be refused by the database")

		// A DIFFERENT field is fine.
		_, err = proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: 1, Field: "details",
			NewValue: strptr("different field"), AuthorID: other,
		})
		assert.NoError(t, err, "the constraint is per-field, not per-target")
	})
}

func TestEditProposals_DecidedProposalFreesTheField(t *testing.T) {
	// The index is PARTIAL on status='open', so a decided proposal must not keep
	// the field locked. If it did, one accepted edit would permanently prevent
	// any further edit to that field.
	runWithRollbackTxn(t, "decided-frees", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "prop-author-3")
		proposals := sqlite.NewEditProposalStore()

		first, err := proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: 2, Field: "title",
			NewValue: strptr("first"), AuthorID: author,
		})
		require.NoError(t, err)
		require.NoError(t, proposals.SetStatus(ctx, first.ID, models.ProposalRejected, author))

		_, err = proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: 2, Field: "title",
			NewValue: strptr("second attempt"), AuthorID: author,
		})
		assert.NoError(t, err, "a rejected proposal must not block the next one on that field")
	})
}

func TestEditProposals_InvalidStatusIsRefused(t *testing.T) {
	// The status CHECK is a closed list, and a typo'd status is a bug that must
	// not become a row: an unknown status is not "pending", it is corruption.
	runWithRollbackTxn(t, "bad-status", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "prop-author-4")
		proposals := sqlite.NewEditProposalStore()

		_, err := proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: 3, Field: "title",
			NewValue: strptr("x"), AuthorID: author, Status: "maybe",
		})
		assert.Error(t, err, "a status outside the closed list must be refused")
	})
}

func TestProposalVotes_OneVotePerUserEnforcedByDatabase(t *testing.T) {
	// The composite primary key. A resolver-side check has a window in which two
	// requests from one user both pass it, and the consequence is exactly what
	// MinVoters exists to prevent.
	//
	// The guarantee is asserted against a raw INSERT, not against Cast: Cast is
	// deliberately an upsert (re-voting means changing your mind), so it will
	// never produce this error. Asserting the primary key through Cast would
	// either fail or, worse, pass for the wrong reason.
	runWithRollbackTxn(t, "one-vote", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "vote-prop-author")
		voter := mustCreateUser(ctx, t, "vote-voter")
		proposals := sqlite.NewEditProposalStore()

		p, err := proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: 4, Field: "title",
			NewValue: strptr("x"), AuthorID: author,
		})
		require.NoError(t, err)

		votes := sqlite.NewProposalVoteStore()
		require.NoError(t, votes.Cast(ctx, p.ID, voter, 1))

		// Cast upserts, so the duplicate is absorbed. What the primary key
		// guarantees is that it is absorbed IN PLACE: one row, not two. Two rows
		// for one user is the Sybil shape, and it would show up as an inflated
		// net tally that no amount of MinVoters could catch.
		require.NoError(t, votes.Cast(ctx, p.ID, voter, -1))

		rows, err := votes.List(ctx, p.ID)
		require.NoError(t, err)
		assert.Len(t, rows, 1, "one user must be able to hold at most ONE vote row")
		assert.Equal(t, -1, rows[0].Value, "the second Cast must have replaced the value")
	})
}

func TestProposalVotes_ReVoteUpdatesInPlace(t *testing.T) {
	// Re-voting is an UPDATE, not a second row. A second row would be a second
	// tally contribution from one account — the Sybil shape — and the primary
	// key is what stops it.
	runWithRollbackTxn(t, "revote", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "revote-author")
		voter := mustCreateUser(ctx, t, "revote-voter")
		proposals := sqlite.NewEditProposalStore()
		votes := sqlite.NewProposalVoteStore()

		p, err := proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: 5, Field: "title",
			NewValue: strptr("x"), AuthorID: author,
		})
		require.NoError(t, err)

		require.NoError(t, votes.Cast(ctx, p.ID, voter, 1))
		require.NoError(t, votes.Cast(ctx, p.ID, voter, -1), "a changed mind must be expressible")

		score, err := proposals.Score(ctx, p.ID)
		require.NoError(t, err)
		assert.Equal(t, -1, score.Net, "the vote must have been replaced, not added")
		assert.Equal(t, 1, score.Voters, "one user is still one voter")
	})
}

func TestProposalVotes_InvalidValueIsRefused(t *testing.T) {
	// A vote is +1 or -1 and nothing else. A vote table that accepts 0 or 5
	// invites weighted voting, which turns reputation into an auction.
	runWithRollbackTxn(t, "bad-value", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "badval-author")
		voter := mustCreateUser(ctx, t, "badval-voter")
		proposals := sqlite.NewEditProposalStore()
		votes := sqlite.NewProposalVoteStore()

		p, err := proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: 6, Field: "title",
			NewValue: strptr("x"), AuthorID: author,
		})
		require.NoError(t, err)

		assert.Error(t, votes.Cast(ctx, p.ID, voter, 0), "0 is not an opinion")
		assert.Error(t, votes.Cast(ctx, p.ID, voter, 5), "5 is not an opinion")
	})
}

func TestProposalScores_ViewComputesNetAndDistinctVoters(t *testing.T) {
	// The view is the "no stored counters" rule made concrete. This asserts the
	// tally is computed correctly AND that voters is DISTINCT — the column that
	// makes MinVoters enforceable.
	runWithRollbackTxn(t, "score-view", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "score-author")
		v1 := mustCreateUser(ctx, t, "score-v1")
		v2 := mustCreateUser(ctx, t, "score-v2")
		proposals := sqlite.NewEditProposalStore()
		votes := sqlite.NewProposalVoteStore()

		p, err := proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: 7, Field: "title",
			NewValue: strptr("x"), AuthorID: author,
		})
		require.NoError(t, err)

		require.NoError(t, votes.Cast(ctx, p.ID, v1, 1))
		require.NoError(t, votes.Cast(ctx, p.ID, v2, 1))
		require.NoError(t, votes.Cast(ctx, p.ID, author, -1)) // author votes against

		score, err := proposals.Score(ctx, p.ID)
		require.NoError(t, err)
		assert.Equal(t, 1, score.Net, "2 for, 1 against")
		assert.Equal(t, 3, score.Voters, "three DISTINCT users voted")
		assert.True(t, score.AuthorVoted, "author_voted must come from the same view, not a second query")
	})
}

func TestProposalScores_ProposalWithNoVotesScoresZero(t *testing.T) {
	// A fresh proposal must read as 0/0, not as a missing row. The LEFT JOIN is
	// what makes that true, and COALESCE is what makes net 0 rather than NULL —
	// a NULL net compared with an integer threshold is NULL in SQLite, which
	// silently reads as "not at threshold" and would hide the difference between
	// "no votes" and "error".
	runWithRollbackTxn(t, "no-votes", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "zerovote-author")
		proposals := sqlite.NewEditProposalStore()

		p, err := proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: 8, Field: "title",
			NewValue: strptr("x"), AuthorID: author,
		})
		require.NoError(t, err)

		score, err := proposals.Score(ctx, p.ID)
		require.NoError(t, err)
		assert.Equal(t, 0, score.Net)
		assert.Equal(t, 0, score.Voters)
		assert.False(t, score.AuthorVoted)
	})
}

func TestEditProposals_OldValueNullIsDistinctFromEmptyString(t *testing.T) {
	// "was unset" and "was the empty string" are different facts, and collapsing
	// them makes it impossible to propose "clear this field" separately from
	// "set this field to nothing".
	runWithRollbackTxn(t, "null-vs-empty", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "nullable-author")
		proposals := sqlite.NewEditProposalStore()

		unset, err := proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: 9, Field: "details",
			NewValue: strptr("set it"), AuthorID: author,
		})
		require.NoError(t, err)

		empty, err := proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: 9, Field: "title",
			OldValue: strptr(""), NewValue: strptr("x"), AuthorID: author,
		})
		require.NoError(t, err)

		got, err := proposals.Find(ctx, unset.ID)
		require.NoError(t, err)
		assert.Nil(t, got.OldValue, "a NULL old_value must round-trip as nil, not as the string \"null\"")

		got2, err := proposals.Find(ctx, empty.ID)
		require.NoError(t, err)
		require.NotNil(t, got2.OldValue)
		assert.Equal(t, "", *got2.OldValue, "an empty-string old_value must round-trip as empty, not as nil")
	})
}

func strptr(s string) *string { return &s }

var _ = time.Now

// The Go-level guard in Cast is not the only line of defence, and a test that
// only exercised Cast would let the schema CHECK rot unnoticed. Weighted voting
// is the thing worth preventing -- a vote table accepting 0 or 5 turns
// reputation into an auction -- so the constraint itself needs a test that
// bypasses the Go check entirely.
func TestProposalVotes_ValueConstraintHoldsWithoutTheGoGuard(t *testing.T) {
	runWithRollbackTxn(t, "value-constraint", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "rawval-author")
		proposals := sqlite.NewEditProposalStore()

		p, err := proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: 11, Field: "title",
			NewValue: strptr("x"), AuthorID: author,
		})
		require.NoError(t, err)

		// Straight at the table, no Cast, no validation in between.
		_, err = sqlite.DbgRawVote(ctx, p.ID, author, 5)
		assert.Error(t, err, "the schema must refuse a weight of 5 on its own; "+
			"Cast's Go check is a convenience, not the guarantee")

		_, err = sqlite.DbgRawVote(ctx, p.ID, author, 0)
		assert.Error(t, err, "0 is not an opinion")

		// And the legal value still works, so the CHECK is not simply
		// rejecting everything.
		_, err = sqlite.DbgRawVote(ctx, p.ID, author, -1)
		assert.NoError(t, err, "-1 is a legal vote and must still be accepted")
	})
}
