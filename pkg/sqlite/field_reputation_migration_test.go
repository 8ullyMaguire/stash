//go:build integration
// +build integration

// Migration tests for per-field reputation. StashForge M2b.
//
// The question this file answers is not "does the version number match" —
// stashforge_migrations_test.go already covers that, and a version number can
// match perfectly while the migration silently does nothing. It is "does the
// table exist, and does it enforce the three constraints the M2b spec says it
// must".

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFieldReputation_TableExists: migration 95 applies.
//
// Written as a table check rather than inferred from the schema version, because
// the two failures look identical from the outside. A version bump with a broken
// migration file opens cleanly, serves traffic, and fails on the first write with
// "no such table: field_reputation" — which is the worst time to find out.
func TestFieldReputation_TableExists(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		assert.Equal(t, int64(1),
			count(t, ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'field_reputation'"),
			"field_reputation must exist; if this fails, migration 95 did not apply "+
				"even though the schema version agrees")
	})
}

// TestFieldReputation_ColumnsMatchTheModel: the store reads by column name, so a
// renamed column is a runtime error, not a compile error.
func TestFieldReputation_ColumnsMatchTheModel(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		for _, col := range []string{"user_id", "target_type", "field", "reputation", "rejections", "updated_at"} {
			assert.Equal(t, int64(1),
				count(t, ctx, "SELECT count(*) FROM pragma_table_info('field_reputation') WHERE name = ?", col),
				"field_reputation.%s must exist", col)
		}
	})
}

// TestFieldReputation_IsPerFieldNotGlobal is the load-bearing constraint.
//
// Reputation is per (user, target_type, field). If the primary key were
// (user_id) alone, one global score would transfer trust between unrelated
// fields — and the spec's whole argument for per-field reputation is that being
// reliable about performer metadata says nothing about galleries.
func TestFieldReputation_IsPerFieldNotGlobal(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		uid := insertUser(t, ctx, "sfRepPerField", false)

		require.NoError(t, exec(t, ctx,
			"INSERT INTO field_reputation (user_id, target_type, field, reputation) VALUES (?, 'performer', 'details', 40)",
			uid))
		// Same user, different field. Must be a separate row.
		require.NoError(t, exec(t, ctx,
			"INSERT INTO field_reputation (user_id, target_type, field, reputation) VALUES (?, 'performer', 'name', 3)",
			uid))
		// Same user, same field, different target type. Also separate.
		require.NoError(t, exec(t, ctx,
			"INSERT INTO field_reputation (user_id, target_type, field, reputation) VALUES (?, 'tag', 'details', 7)",
			uid))

		assert.Equal(t, int64(3),
			count(t, ctx, "SELECT count(*) FROM field_reputation WHERE user_id = ?", uid),
			"three distinct (field, target_type) pairs must be three rows")

		// And the same (user, field, type) twice must collide.
		assert.Error(t, exec(t, ctx,
			"INSERT INTO field_reputation (user_id, target_type, field, reputation) VALUES (?, 'performer', 'details', 99)",
			uid),
			"a second row for the same (user, field, target_type) must be refused; "+
				"two reputations for one competence means which one a tally used "+
				"depends on row order")
	})
}

// TestFieldReputation_RejectsNegativeReputation: the CHECK from the migration.
//
// "Not yet trusted" and "silenced" must stay distinguishable. Negative
// reputation would be a second, stronger mechanism than decay that removes a
// person from governance outright rather than discounting their vote, and it
// would be invisible to the UI because nothing renders it.
func TestFieldReputation_RejectsNegativeReputation(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		uid := insertUser(t, ctx, "sfRepNegative", false)

		assert.Error(t, exec(t, ctx,
			"INSERT INTO field_reputation (user_id, target_type, field, reputation) VALUES (?, 'performer', 'details', -1)",
			uid),
			"negative reputation must be refused by the CHECK; decay is the "+
				"specified mechanism for a user who keeps losing, and it floors "+
				"rather than reaching zero")

		// Zero is allowed — that is the state a brand-new user is in.
		require.NoError(t, exec(t, ctx,
			"INSERT INTO field_reputation (user_id, target_type, field, reputation) VALUES (?, 'performer', 'details', 0)",
			uid), "zero reputation is legitimate: it is the default state")
	})
}

// TestFieldReputation_RejectionsCountIndependently: decay reads `rejections`,
// and it must be settable on its own. A schema where rejections were derived
// from reputation could not express "discounted but not yet distrusted".
func TestFieldReputation_RejectionsCountIndependently(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		uid := insertUser(t, ctx, "sfRepRejections", false)

		require.NoError(t, exec(t, ctx,
			"INSERT INTO field_reputation (user_id, target_type, field, reputation, rejections) VALUES (?, 'scene', 'title', 25, 4)",
			uid))

		assert.Equal(t, int64(25), scalar(t, ctx,
			"SELECT reputation FROM field_reputation WHERE user_id = ?", uid))
		assert.Equal(t, int64(4), scalar(t, ctx,
			"SELECT rejections FROM field_reputation WHERE user_id = ?", uid))
	})
}

// TestFieldReputation_CascadesOnUserDelete: reputation about a user who no
// longer exists is a row nothing can read and nothing cleans up.
func TestFieldReputation_CascadesOnUserDelete(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		uid := insertUser(t, ctx, "sfRepCascade", false)
		require.NoError(t, exec(t, ctx,
			"INSERT INTO field_reputation (user_id, target_type, field, reputation) VALUES (?, 'gallery', 'title', 12)",
			uid))

		require.NoError(t, exec(t, ctx, "DELETE FROM users WHERE id = ?", uid))
		assert.Equal(t, int64(0),
			count(t, ctx, "SELECT count(*) FROM field_reputation WHERE user_id = ?", uid),
			"deleting a user must remove their reputation; ON DELETE CASCADE is "+
				"what keeps the table from accumulating rows about nobody")
	})
}

// TestFieldReputation_IndexServesTheTallyRead: the tally joins reputation by
// (field, user), so the per-field index has to exist.
//
// This asserts the index is PRESENT, not that SQLite chooses it. The first
// version asserted the query plan and failed with an empty string -- twice over,
// for two different reasons:
//
//  1. EXPLAIN QUERY PLAN's first column is `id`, not `detail`, so the scalar
//     helper was reading the row number and throwing the plan text away.
//  2. Even fixed, a query-plan assertion on a three-row table is not evidence.
//     SQLite will correctly prefer a scan when a table is this small whether or
//     not the index exists, so the test would pass with the index dropped and
//     fail on a real dataset for reasons about row counts.
//
// A test that asserts the planner's choice is a test about the data volume, not
// about the schema. Asserting the index exists is the part that is actually a
// property of the migration.
func TestFieldReputation_IndexServesTheTallyRead(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		for _, idx := range []string{"idx_field_reputation_field", "idx_field_reputation_user"} {
			assert.Equal(t, int64(1),
				count(t, ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?", idx),
				"%s must exist; without it the tally does a scan per ballot, "+
					"which on a busy proposal is a scan per ballot times the "+
					"number of voters", idx)
		}
	})
}
