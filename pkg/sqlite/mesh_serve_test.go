//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/sqlite"
)

// M8 step 0 probe 3: can a node host replicas without becoming an unbounded
// liability? The answer implemented here is "yes, if the cap is enforced where
// the bytes leave".
//
// Each test names the clause it measures, because the obvious mistake is a test
// that measures the mechanism rather than the property -- and the mechanism here
// is a transaction, which every happy-path test exercises identically whether or
// not the budget is real.

const testHashA = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
const testHashB = "9b74c6897f6b2b7a6bfd1c6f2f6a1d3a4c5b6e7f8091a2b3c4d5e6f708192a3b"

var meshNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func TestAReplicaFetchIsRefusedWithNoBudgetRow(t *testing.T) {
	// The safe default. A node that never set a budget must serve NOTHING, not
	// everything -- "no budget means unlimited" is the reading that turns a fresh
	// install into an open relay, and it is the failure this clause exists to
	// prevent.
	runWithRollbackTxn(t, "no-budget", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewMeshServeStore(db)

		err := store.ServeReplica(ctx, "node-never-opted-in", testHashA, 1, meshNow)

		require.Error(t, err, "a node with no budget row must not serve")
		assert.ErrorIs(t, err, sqlite.ErrNoBudget,
			"this must be distinguishable from exhaustion: 'never opted in' and "+
				"'opted in and full' need different fixes")
		assert.NotErrorIs(t, err, sqlite.ErrBudgetExhausted,
			"no budget is not exhaustion, and an operator must be able to tell them apart")

		// And nothing may have been logged: a refused serve that still wrote a
		// row would charge the node for bytes it never sent.
		served, err := store.ServedBytes(ctx, "node-never-opted-in", sqlite.CurrentPeriod(meshNow))
		require.NoError(t, err)
		assert.Zero(t, served, "a refused serve must not appear in the log")
	})
}

func TestAReplicaFetchBeyondTheBudgetIsRefusedBeforeAnyBytesLeave(t *testing.T) {
	// The clause probe 3 names, and the test the probes doc requires to exist.
	// Budget 100, ask for 101.
	runWithRollbackTxn(t, "over-budget", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewMeshServeStore(db)
		require.NoError(t, store.SetBudget(ctx, "node-a", "2026-10", 100))

		err := store.ServeReplica(ctx, "node-a", testHashA, 101, meshNow)

		require.Error(t, err, "a fetch larger than the whole budget must be refused")
		assert.ErrorIs(t, err, sqlite.ErrBudgetExhausted)
		assert.NotErrorIs(t, err, sqlite.ErrNoBudget)

		served, err := store.ServedBytes(ctx, "node-a", "2026-10")
		require.NoError(t, err)
		assert.Zero(t, served, "the refusal must happen BEFORE the bytes are logged")
	})
}

func TestAServeThatExactlyFillsTheBudgetIsAllowed(t *testing.T) {
	// The boundary, because a cap implemented as `>=` refuses one byte too many
	// and nobody notices until a node sits permanently one byte under its limit.
	runWithRollbackTxn(t, "exact", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewMeshServeStore(db)
		require.NoError(t, store.SetBudget(ctx, "node-exact", "2026-10", 100))

		require.NoError(t, store.ServeReplica(ctx, "node-exact", testHashA, 100, meshNow),
			"a serve that exactly fills the budget is within it")

		served, err := store.ServedBytes(ctx, "node-exact", "2026-10")
		require.NoError(t, err)
		assert.EqualValues(t, 100, served)
	})
}

func TestTheRunningTotalIsTheSumOfServesAndRefillsNextPeriod(t *testing.T) {
	// The cap must be per PERIOD, not lifetime, or a node is throttled forever by
	// its own history. This is the property that makes the budget a monthly cap
	// rather than a permanent shutdown.
	runWithRollbackTxn(t, "period", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewMeshServeStore(db)
		require.NoError(t, store.SetBudget(ctx, "node-period", "2026-10", 100))
		require.NoError(t, store.SetBudget(ctx, "node-period", "2026-11", 100))

		require.NoError(t, store.ServeReplica(ctx, "node-period", testHashA, 100, meshNow))

		// October is full.
		require.ErrorIs(t,
			store.ServeReplica(ctx, "node-period", testHashA, 1, meshNow),
			sqlite.ErrBudgetExhausted)

		// November has not been used at all, so it serves.
		nov := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
		assert.Equal(t, "2026-11", sqlite.CurrentPeriod(nov), "periods are YYYY-MM in UTC")
		require.NoError(t, store.ServeReplica(ctx, "node-period", testHashA, 100, nov),
			"a new period must start empty")

		nov1, err := store.ServedBytes(ctx, "node-period", "2026-11")
		require.NoError(t, err)
		assert.EqualValues(t, 100, nov1)
		oct1, err := store.ServedBytes(ctx, "node-period", "2026-10")
		require.NoError(t, err)
		assert.EqualValues(t, 100, oct1, "October's total is unchanged by November's serve")
	})
}

func TestTheBudgetCanBeLoweredWhileTheMeshIsRunning(t *testing.T) {
	// An operator who notices they are being used as a relay must be able to stop
	// it. If SetBudget rejected a second write, the only remedy would be to
	// delete the node from the mesh.
	runWithRollbackTxn(t, "lower", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewMeshServeStore(db)
		require.NoError(t, store.SetBudget(ctx, "node-lower", "2026-10", 1000))
		require.NoError(t, store.SetBudget(ctx, "node-lower", "2026-10", 10),
			"a second write must replace, not conflict")

		budget, ok, err := store.BudgetFor(ctx, "node-lower", "2026-10")
		require.NoError(t, err)
		require.True(t, ok)
		assert.EqualValues(t, 10, budget, "the later value wins")

		require.ErrorIs(t,
			store.ServeReplica(ctx, "node-lower", testHashA, 11, meshNow),
			sqlite.ErrBudgetExhausted)
		require.NoError(t, store.ServeReplica(ctx, "node-lower", testHashA, 10, meshNow))
	})
}

func TestAServeWithNoContentHashIsRefused(t *testing.T) {
	// Probe 1 established that the transports disclose peer addresses, so
	// authorisation is POSSESSION OF THE CONTENT HASH and there is no identity to
	// check. An empty hash would therefore authorise anyone who says nothing --
	// it is the one input that turns "you must have the hash" into "you must
	// send a request".
	runWithRollbackTxn(t, "no-hash", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewMeshServeStore(db)
		require.NoError(t, store.SetBudget(ctx, "node-nohash", "2026-10", 1000))

		err := store.ServeReplica(ctx, "node-nohash", "", 1, meshNow)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "content hash")
	})
}

func TestNegativeBytesAndMissingNodeAreRefused(t *testing.T) {
	// Negative bytes would make the SUM smaller than reality, which is a way to
	// buy budget by serving nothing.
	runWithRollbackTxn(t, "negative", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewMeshServeStore(db)
		require.NoError(t, store.SetBudget(ctx, "node-neg", "2026-10", 100))

		require.Error(t, store.ServeReplica(ctx, "node-neg", testHashA, -1, meshNow),
			"negative bytes must not be recordable")
		require.Error(t, store.ServeReplica(ctx, "", testHashA, 1, meshNow),
			"a serve with no node id must not be recordable")

		served, err := store.ServedBytes(ctx, "node-neg", "2026-10")
		require.NoError(t, err)
		assert.Zero(t, served)
	})
}

func TestTheBudgetTotalIsComputedNotStored(t *testing.T) {
	// Non-negotiable #4, "computed, never stored", enforced on this table like
	// everywhere else: the running total must be a SUM over the log, so it cannot
	// drift from the serves that actually happened or be edited into agreement
	// with a lie.
	//
	// The check is STRUCTURAL rather than behavioural, because a behavioural check
	// ("the total is right after two serves") passes with a counter column too.
	// What must not exist is the column.
	runWithRollbackTxn(t, "no-counter", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewMeshServeStore(db)
		require.NoError(t, store.SetBudget(ctx, "node-struct", "2026-10", 100))
		require.NoError(t, store.ServeReplica(ctx, "node-struct", testHashA, 40, meshNow))

		cols := tableColumns(ctx, t, "mesh_replication_serve_log")
		for _, forbidden := range []string{"bytes_served", "served_total", "total_bytes", "counter"} {
			assert.NotContains(t, cols, forbidden,
				"a counter column on the serve log is a stored tally: the total "+
					"must be a SUM so it cannot disagree with the log")
		}

		// And the computed value agrees with what was actually served.
		served, err := store.ServedBytes(ctx, "node-struct", "2026-10")
		require.NoError(t, err)
		assert.EqualValues(t, 40, served)
	})
}
func TestASecondConcurrentFetchCannotBothFitInTheBudget(t *testing.T) {
	// WHY THIS TEST IS ABOUT CONCURRENCY: the cap is only real if the check and
	// the log write are in ONE transaction. Two fetches that each read the same
	// running total would both pass a check only one of them fits in, so a
	// sequential happy-path test proves nothing about the property the cap exists
	// for.
	//
	// AND WHY IT IS NOT INSIDE withRollbackTxn -- which is the mistake the first
	// version of this test made, and it produced a test that passed on two runs
	// of three and failed on the third. runWithRollbackTxn hands the body a
	// context that already carries a transaction, so:
	//
	//   1. ServeReplica's `if _, err := getTx(ctx); err != nil` sees a
	//      transaction, does NOT open its own, and so never gets the
	//      serialisation its own-transaction path depends on;
	//   2. both goroutines therefore share ONE connection and one snapshot;
	//   3. both read the same running total and both pass.
	//
	// So the harness removed the very property under test, and the failure was
	// intermittent -- which is the worst shape: it looks like flakiness rather
	// than like a test that cannot see the bug.
	//
	// Same reasoning as TestInviteStore_SingleUseUnderConcurrency: real
	// concurrency against the real database, setup committed, unique node id per
	// run so a persistent dev database cannot make a second run collide.
	suffix := time.Now().UnixNano()
	nodeID := "node-race-" + strconv.FormatInt(suffix, 10)

	store := sqlite.NewMeshServeStore(db)
	period := sqlite.CurrentPeriod(meshNow)
	require.NoError(t, inCommittedTxn(t, func(ctx context.Context) error {
		return store.SetBudget(ctx, nodeID, period, 100)
	}))

	type result struct{ err error }
	results := make(chan result, 2)
	start := make(chan struct{})

	for i := 0; i < 2; i++ {
		go func() {
			<-start
			// Each racer gets its OWN context, from Background: sharing the
			// test's ctx would share a transaction if one were open, which is the
			// bug above.
			results <- result{store.ServeReplica(context.Background(), nodeID, testHashA, 80, meshNow)}
		}()
	}
	close(start)

	var okCount, exhaustedCount int
	for i := 0; i < 2; i++ {
		r := <-results
		switch {
		case r.err == nil:
			okCount++
		case errors.Is(r.err, sqlite.ErrBudgetExhausted):
			exhaustedCount++
		case errors.Is(r.err, sqlite.ErrNoBudget):
			t.Fatalf("the budget was committed before the racers ran, so a "+
				"missing budget here means setup did not commit: %v", r.err)
		default:
			t.Fatalf("unexpected error from a concurrent serve: %v", r.err)
		}
	}

	assert.Equal(t, 1, okCount, "exactly one 80-byte serve fits in a 100-byte budget")
	assert.Equal(t, 1, exhaustedCount, "the other must be refused as exhausted")

	var served int64
	require.NoError(t, inCommittedTxn(t, func(ctx context.Context) error {
		var err error
		served, err = store.ServedBytes(ctx, nodeID, period)
		return err
	}))
	assert.EqualValues(t, 80, served,
		"the budget was not overshot: the log records only what was allowed")
}

// TestTheAppliedSchemaIsTheOneTheMigrationDeclares reads migration 110's DDL
// back OUT of the database the app migrated, rather than trusting the .sql file.
//
// The project has been burned five times by a COMMENT in a migration stating a
// constraint the DDL does not implement, and by TestStashForge_SchemaVersion
// MatchesAppSchemaVersion passing while the migration had not applied at all --
// it only proves the number moved. So this reads sqlite_master.
//
// Stated as a positive control: it asserts the tables EXIST and carry the
// columns the store's SQL names. A test that greps the .sql file instead proves
// only that a file contains some text.
func TestTheAppliedSchemaIsTheOneTheMigrationDeclares(t *testing.T) {
	runWithRollbackTxn(t, "applied-schema", func(t *testing.T, ctx context.Context) {
		for table, want := range map[string][]string{
			"mesh_replication_serve_log": {"id", "content_hash", "bytes", "served_at"},
			"mesh_replication_budget":    {"node_id", "period", "budget_bytes"},
		} {
			cols := tableColumns(ctx, t, table)
			for _, c := range want {
				assert.Contains(t, cols, c,
					"%s must carry %s: the store's SQL names it, so a schema "+
						"without it fails at runtime and not at compile time", table, c)
			}
		}

		// The CHECK constraints are the part a comment would get wrong, so they
		// are enforced by trying the violation rather than by reading the DDL.
		// A negative byte count must be refused BY THE DATABASE.
		require.NoError(t, exec(t, ctx,
			"INSERT INTO mesh_replication_serve_log (content_hash, bytes, served_at) "+
				"VALUES ('h', 1, '2026-10-01 00:00:00')"))
		assert.Error(t, execErr(t, ctx,
			"INSERT INTO mesh_replication_serve_log (content_hash, bytes, served_at) "+
				"VALUES ('h', -1, '2026-10-01 00:00:00')"),
			"negative bytes must be refused by the CHECK, not only by the store")
		assert.Error(t, execErr(t, ctx,
			"INSERT INTO mesh_replication_serve_log (content_hash, bytes, served_at) "+
				"VALUES (NULL, 1, '2026-10-01 00:00:00')"),
			"a NULL content hash must be refused: the hash is the capability")
	})
}
