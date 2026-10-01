//go:build integration

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/sqlite"
)

// Shadow governance log. The runtime rule is unchanged by all of this: flat
// quorum still decides every proposal, and these rows only record what the
// weighted path would have said.
//
// The tests that matter are the ones that would fail if the stored decision text
// were wrong, because the log's entire value is that a later operator reads it
// correctly. So the expectations are written as the literal words an operator
// will see, not in terms of the same function that wrote them -- a round-trip
// through String() and back proves the two agree with each other, which is not
// the same as proving either is right.

func newShadowStore() *sqlite.ShadowLogStore { return sqlite.NewShadowLogStore() }

// shadowProposal creates a real proposal so the log's foreign key holds.
//
// Not a bare id: the column REFERENCES edit_proposals(id), so a fabricated id is
// refused by the database. That refusal is correct behaviour and it is worth the
// two extra lines, because a log table that accepted orphan rows would be able to
// carry evaluation records for proposals that never existed.
func shadowProposal(t *testing.T, ctx context.Context, targetID int) int {
	t.Helper()
	author := mustCreateUser(ctx, t, "shadow-author")
	proposals := sqlite.NewEditProposalStore()

	created, err := proposals.Create(ctx, &models.EditProposal{
		TargetType: "scene", TargetID: targetID, Field: "title",
		NewValue: strptr("proposed"), AuthorID: author,
	})
	require.NoError(t, err)
	return created.ID
}

func shadowRec(proposalID int, flat, weighted collab.Decision) collab.ShadowRecord {
	return collab.ShadowRecord{
		ProposalID: proposalID,
		TargetType: "scene",
		Field:      "title",
		Flat:       flat,
		Weighted:   weighted,
		Net:        3,
		Voted:      3,
		Ballots:    3,
	}
}

func TestShadowLogRoundTripsDecisionsAsWordsNotDigits(t *testing.T) {
	// Decision is an int enum. If the store wrote string(d) rather than
	// d.String(), every row would hold a single rune -- "\x00" for pending,
	// "\x01" for accepted -- and reading the log back would quietly yield
	// "pending" for everything. go vet catches that conversion at build time;
	// this asserts the stored bytes, so the guarantee does not rest on vet.
	for _, tc := range []struct {
		name string
		d    collab.Decision
		want string
	}{
		{"pending", collab.DecisionPending, "pending"},
		{"accepted", collab.DecisionAccepted, "accepted"},
		{"rejected", collab.DecisionRejected, "rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runWithRollbackTxn(t, "shadow-roundtrip-"+tc.name, func(t *testing.T, ctx context.Context) {
				s := newShadowStore()
				require.NoError(t, s.Record(ctx,
					shadowRec(shadowProposal(t, ctx, 1), tc.d, tc.d)))

				var got string
				require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &got,
					"SELECT flat_decision FROM governance_shadow_log LIMIT 1"))
				assert.Equal(t, tc.want, got,
					"the column must hold the word, not a rune of the enum value")
			})
		})
	}
}

func TestShadowLogRefusesAnOrphanRow(t *testing.T) {
	// The FK is doing real work. Without it the log could carry evaluations for
	// proposals that never existed, and a governance decision nobody made would
	// be indistinguishable from a real one.
	runWithRollbackTxn(t, "shadow-orphan", func(t *testing.T, ctx context.Context) {
		s := newShadowStore()
		err := s.Record(ctx, shadowRec(999999, collab.DecisionPending, collab.DecisionAccepted))
		assert.Error(t, err,
			"a shadow row for a proposal that does not exist must be refused")
	})
}

func TestShadowLogDisagreementsFiltersAndClassifies(t *testing.T) {
	runWithRollbackTxn(t, "shadow-disagree", func(t *testing.T, ctx context.Context) {
		s := newShadowStore()
		pid := shadowProposal(t, ctx, 1)

		// Two agreements, then one disagreement in each direction.
		require.NoError(t, s.Record(ctx, shadowRec(pid, collab.DecisionPending, collab.DecisionPending)))
		require.NoError(t, s.Record(ctx, shadowRec(pid, collab.DecisionPending, collab.DecisionAccepted)))
		require.NoError(t, s.Record(ctx, shadowRec(pid, collab.DecisionAccepted, collab.DecisionPending)))
		require.NoError(t, s.Record(ctx, shadowRec(pid, collab.DecisionAccepted, collab.DecisionRejected)))
		require.NoError(t, s.Record(ctx, shadowRec(pid, collab.DecisionPending, collab.DecisionPending)))

		got, err := s.Disagreements(ctx, 0)
		require.NoError(t, err)
		require.Len(t, got, 3,
			"only the rows where the two functions differ belong in this list")

		// Newest first, so the most recent disagreement is the one an operator
		// sees. The rows were written in the order accept, hold, flip, so
		// newest-first yields flip, hold, accept.
		//
		// Note the tiebreak: all five rows share one CURRENT_TIMESTAMP to the
		// second, so `at DESC` alone would leave the order arbitrary and the
		// `id DESC` secondary key is what makes it deterministic. Relying on the
		// timestamp alone would make this test flaky by construction.
		assert.Equal(t, "weighted_would_flip", got[0].Direction(),
			"accepted vs rejected is a flip, not merely a hold")
		assert.Equal(t, "weighted_would_hold", got[1].Direction(),
			"flat accepted, weighted would have held")
		assert.Equal(t, "weighted_would_accept", got[2].Direction(),
			"flat held, weighted would have accepted")
	})
}

func TestShadowLogDisagreementDirectionsAreDistinct(t *testing.T) {
	// Pure logic, and the distinction matters: "weighted is stricter" is not a
	// thing this design guarantees, and a summary that conflated the three
	// directions would tell an operator to expect a bias that is not there.
	for _, tc := range []struct {
		flat, weighted collab.Decision
		want           string
	}{
		{collab.DecisionPending, collab.DecisionPending, ""},
		{collab.DecisionAccepted, collab.DecisionAccepted, ""},
		{collab.DecisionRejected, collab.DecisionRejected, ""},
		{collab.DecisionPending, collab.DecisionAccepted, "weighted_would_accept"},
		{collab.DecisionAccepted, collab.DecisionPending, "weighted_would_hold"},
		{collab.DecisionPending, collab.DecisionRejected, "weighted_would_accept"},
		{collab.DecisionRejected, collab.DecisionPending, "weighted_would_hold"},
		{collab.DecisionAccepted, collab.DecisionRejected, "weighted_would_flip"},
		{collab.DecisionRejected, collab.DecisionAccepted, "weighted_would_flip"},
	} {
		r := collab.ShadowRecord{Flat: tc.flat, Weighted: tc.weighted}
		assert.Equal(t, tc.want, r.Direction(),
			"flat=%v weighted=%v", tc.flat, tc.weighted)
		assert.Equal(t, tc.want == "", r.Agrees(),
			"Agrees must agree with Direction being empty")
	}
}

func TestShadowLogDisagreementsLimitIsOptional(t *testing.T) {
	// A limit of zero means "all", not "none". A moderation view that silently
	// showed nothing would look exactly like a healthy governance log.
	runWithRollbackTxn(t, "shadow-limit", func(t *testing.T, ctx context.Context) {
		s := newShadowStore()
		pid := shadowProposal(t, ctx, 1)
		for i := 0; i < 3; i++ {
			require.NoError(t, s.Record(ctx, shadowRec(pid, collab.DecisionPending, collab.DecisionAccepted)))
		}

		all, err := s.Disagreements(ctx, 0)
		require.NoError(t, err)
		assert.Len(t, all, 3, "limit 0 must mean every row")

		one, err := s.Disagreements(ctx, 1)
		require.NoError(t, err)
		assert.Len(t, one, 1, "a positive limit must be honoured")

		negative, err := s.Disagreements(ctx, -5)
		require.NoError(t, err)
		assert.Len(t, negative, 3, "a negative limit means unlimited, not zero rows")
	})
}

func TestShadowLogSummaryCountsEveryEvaluationNotJustDisagreements(t *testing.T) {
	// The denominator is the whole point. A log of only disagreements cannot
	// answer "how often would switching have changed things".
	runWithRollbackTxn(t, "shadow-summary", func(t *testing.T, ctx context.Context) {
		s := newShadowStore()
		pid := shadowProposal(t, ctx, 1)

		require.NoError(t, s.Record(ctx, shadowRec(pid, collab.DecisionPending, collab.DecisionPending)))
		require.NoError(t, s.Record(ctx, shadowRec(pid, collab.DecisionPending, collab.DecisionPending)))
		require.NoError(t, s.Record(ctx, shadowRec(pid, collab.DecisionPending, collab.DecisionAccepted)))
		require.NoError(t, s.Record(ctx, shadowRec(pid, collab.DecisionAccepted, collab.DecisionPending)))
		require.NoError(t, s.Record(ctx, shadowRec(pid, collab.DecisionAccepted, collab.DecisionRejected)))

		sum, err := s.Summary(ctx)
		require.NoError(t, err)

		assert.Equal(t, 5, sum.Evaluations, "every evaluation is recorded, agreeing or not")
		assert.Equal(t, 2, sum.Agreed)
		assert.Equal(t, 1, sum.WouldAccept, "weighted would have accepted where flat held")
		assert.Equal(t, 1, sum.WouldHold, "weighted would have held where flat accepted")
		assert.Equal(t, 1, sum.WouldFlip, "accepted -> rejected is a distinct direction")
		assert.Equal(t, 3, sum.Disagreements())
		assert.InDelta(t, 60.0, sum.Rate(), 0.001)
	})
}

func TestShadowLogSummaryOfAnEmptyLogIsZeroNotNaN(t *testing.T) {
	// An instance that has never held a vote has no disagreement rate, and NaN
	// in front of whoever is deciding to change governance is worse than zero.
	runWithRollbackTxn(t, "shadow-empty", func(t *testing.T, ctx context.Context) {
		sum, err := newShadowStore().Summary(ctx)
		require.NoError(t, err)
		assert.Equal(t, 0, sum.Evaluations)
		assert.Equal(t, 0, sum.Disagreements())
		assert.Equal(t, 0.0, sum.Rate())
	})
}

func TestShadowLogIsDeliberatelyNotHashChained(t *testing.T) {
	// A non-goal, asserted rather than left implicit. The shadow log is
	// observational: an attacker who could rewrite it would hide evidence, not
	// change a decision, because flat quorum is what applies. The decisions
	// themselves are recorded in collab_audit, which IS chained.
	//
	// This test exists so nobody later assumes the shadow table is
	// tamper-evident and builds something on that assumption.
	runWithRollbackTxn(t, "shadow-notchained", func(t *testing.T, ctx context.Context) {
		count := 0
		require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &count,
			`SELECT COUNT(*) FROM pragma_table_info('governance_shadow_log')
			 WHERE name IN ('row_hash', 'prev_hash')`))
		assert.Equal(t, 0, count,
			"the shadow log is intentionally not hash-chained; the applied decisions "+
				"are recorded through collab_audit, which is")
	})
}
