//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/sqlite"
)

// R083, M8 step 8.1, spec §6b.3: the three-state sharing switch.
//
// §6b.3 GIVES A THREE-ROW TABLE and this test IS that table, swept against every
// state in the type. Not a representative example per state: the whole point of the
// three states is that the middle one differs from the other two in one column each,
// and a single comfortable example per state would pass against a boolean
// implementation of two of them.

func TestAutoAcquireMatchesTheSpecTable(t *testing.T) {
	runWithRollbackTxn(t, "acquire-table", func(t *testing.T, ctx context.Context) {
		// §6b.3's table, transcribed. Written out rather than derived from the type so
		// that changing the type does not silently rewrite the expectation.
		type row struct {
			state   collab.AutoAcquire
			fetches bool
			seeds   bool
			uploads bool
		}
		table := []row{
			{collab.AcquireOff, false, false, false},
			{collab.AcquireFetchOnly, true, false, false},
			{collab.AcquireFull, true, true, false},
		}

		s := sqlite.NewAutoAcquireStore()
		for _, r := range table {
			require.NoError(t, s.SetAutoAcquire(ctx, r.state), "state %q", r.state)
			got, err := s.AutoAcquire(ctx)
			require.NoError(t, err)
			require.Equal(t, r.state, got, "round trip of %q", r.state)

			assert.Equal(t, r.fetches, got.Fetches(), "%q fetches", r.state)
			assert.Equal(t, r.seeds, got.Seeds(), "%q seeds", r.state)
			assert.Equal(t, r.uploads, got.UploadsData(), "%q uploads data", r.state)
		}
	})
}

// THE DEFAULT IS fetch_only, and it is the load-bearing decision in the whole feature.
//
// §6b.3: an instance that fetches but never seeds "leaks no data outward and cannot be
// made into a source for someone else", so opting a new install in to the harmless
// half is not a leak. Non-negotiable #7 is about the publish path and fetch_only has
// none.
//
// Asserted on a FRESH install with no SetAutoAcquire call, because "the default" and
// "the value a user ends up with after choosing it" are different claims and the second
// is covered by the table test above.
func TestANewInstallDefaultsToFetchOnly(t *testing.T) {
	runWithRollbackTxn(t, "acquire-default", func(t *testing.T, ctx context.Context) {
		got, err := sqlite.NewAutoAcquireStore().AutoAcquire(ctx)
		require.NoError(t, err)
		assert.Equal(t, collab.AcquireFetchOnly, got,
			"§6b.3: a new install fetches and does not seed. Defaulting to `off` would "+
				"make preservation unreachable for everyone who never found a setting")
		assert.True(t, got.Fetches(), "so it fetches")
		assert.False(t, got.Seeds(), "and it does NOT seed -- this is the whole claim")
	})
}

// §6b.3 says of fetch_only that it "cannot be made into a source for someone else".
// That sentence is a claim about Seeds() returning false, and it is the privacy
// argument for the default, so it gets its own test rather than being left as a table
// cell.
func TestFetchOnlyCannotBeMadeIntoASourceForSomeoneElse(t *testing.T) {
	runWithRollbackTxn(t, "acquire-nosource", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAutoAcquireStore()
		require.NoError(t, s.SetAutoAcquire(ctx, collab.AcquireFetchOnly))

		got, err := s.AutoAcquire(ctx)
		require.NoError(t, err)
		assert.False(t, got.Seeds(),
			"an instance at fetch_only must not seed, or §6b.3's privacy argument for "+
				"the default is false while the code looks correct")
		assert.False(t, got.MaySeed(), "and MaySeed agrees with Seeds")
		assert.True(t, got.Fetches(), "while still fetching -- it is the middle state, "+
			"not the off state")
	})
}

// AN UNRECOGNISED VALUE FAILS CLOSED, in the type AND at the write. Coercing an
// unrecognised value to the schema default is the tempting repair and it turns a schema
// bug into a posture nobody chose.
func TestAnUnrecognisedAutoAcquireStateIsRefused(t *testing.T) {
	runWithRollbackTxn(t, "acquire-invalid", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAutoAcquireStore()

		// NOT including "": the empty value is the ZERO VALUE and resolves to `off`
		// rather than being refused, which is what makes plan step 8.1's "an unset
		// column is the safe state" true. `" "` IS here, because a single space is
		// somebody's typo rather than an omitted field, and the two are different
		// situations that get different answers on purpose.
		for _, bad := range []collab.AutoAcquire{
			" ", "OFF", "Opt_Out", "fetch-only", "fetchonly", "full ", "true", "1",
		} {
			assert.False(t, bad.Valid(), "%q must not be valid", string(bad))
			assert.False(t, bad.Fetches(), "%q must not acquire", string(bad))
			assert.False(t, bad.Seeds(), "%q must not seed -- an unrecognised value is "+
				"not permission to publish anything", string(bad))
			assert.Error(t, s.SetAutoAcquire(ctx, bad), "%q must be refused at the write", string(bad))
		}

		// And parsing reports the switch's own sentinel, not the metadata-share one.
		// A caller branching on ErrShareChoiceInvalid would go and ask a user to
		// re-answer a question that is not about their consent.
		// And the stored-column path still refuses it: an empty value can never be
		// STORED (migration 115's CHECK refuses it), so the store's validity check is
		// about wire and API values, and "nonsense" is one of those.
		_, err := collab.ParseAutoAcquire("nonsense")
		require.Error(t, err)
		assert.ErrorIs(t, err, collab.ErrAutoAcquireInvalid)
		assert.NotErrorIs(t, err, collab.ErrShareChoiceInvalid,
			"the two switches have opposite remedies -- a share refusal wants the user "+
				"asked again, an acquire refusal wants the value rejected")
	})
}

// TWO DEFENCES, AND THIS TEST PINS THE GO ONE — the same correction R080 needed.
//
// Removing the Go guard from SetAutoAcquire changed nothing observable, because
// migration 115's CHECK refuses the same values and the tests still passed. Both
// defences are kept and both are needed: the CHECK is the one place a peer cannot talk
// a caller out of, and the Go guard is the one that returns an error naming which of
// the three states was expected — a SQLite constraint message says "CHECK constraint
// failed" and nothing about auto_acquire at all.
//
// So this asserts the guard by its MESSAGE, which is what a caller actually reads. A
// CHECK failure and a guard failure are both "an error"; only one tells an operator
// what to type.
func TestTheAcquireGoGuardRefusesBeforeAnySQLRuns(t *testing.T) {
	runWithRollbackTxn(t, "acquire-guard", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAutoAcquireStore()

		err := s.SetAutoAcquire(ctx, "fetch-only")
		require.Error(t, err)
		assert.ErrorIs(t, err, collab.ErrAutoAcquireInvalid)
		assert.Contains(t, err.Error(), "fetch-only",
			"the guard names the value the caller supplied, which a CHECK failure "+
				"cannot: SQLite reports the constraint, not the argument")
		assert.Contains(t, err.Error(), "off",
			"and it lists the valid states, so the caller can see what to use instead")
		assert.NotContains(t, err.Error(), "CHECK constraint",
			"which proves the refusal happened in Go, before the UPDATE ran")

		// And a valid value still goes through, so the guard is not refusing
		// everything.
		require.NoError(t, s.SetAutoAcquire(ctx, collab.AcquireFull))
		got, err := s.AutoAcquire(ctx)
		require.NoError(t, err)
		require.Equal(t, collab.AcquireFull, got)
	})
}

// THE DATABASE REFUSES IT TOO, so a peer cannot talk a caller out of it. Asserted
// through raw SQL with the Go guard bypassed, which is the only way to reach the CHECK.
func TestTheSchemaRefusesAnUnrecognisedAutoAcquireState(t *testing.T) {
	runWithRollbackTxn(t, "acquire-dbcheck", func(t *testing.T, ctx context.Context) {
		for _, bad := range []string{"off ", "OFF", "", "partial", "fetch_only_x"} {
			_, err := sqlite.DbgAuditRawExec(ctx,
				"UPDATE instance_settings SET auto_acquire = ? WHERE id = 1", bad)
			assert.Error(t, err,
				"the CHECK must refuse %q even when the Go guard is bypassed, because "+
					"the schema is the one place a peer cannot talk a caller out of", bad)
		}
	})
}

// A STORED VALUE THE BUILD DOES NOT KNOW IS REFUSED RATHER THAN GUESSED, and the
// error names the column so the operator knows what to repair.
//
// The column is CHECKed, so this is an anomaly -- but returning fetch_only would let a
// corrupted row choose a posture, which is the mistake migration 103's header
// describes for `mode`.
func TestACorruptedStoredStateIsReportedNotGuessed(t *testing.T) {
	runWithRollbackTxn(t, "acquire-corrupt", func(t *testing.T, ctx context.Context) {
		// The CHECK blocks this write, so the corruption is simulated with a pragma --
		// which is the honest way to reach the state, because in production it means a
		// restored backup or a hand-edited row rather than a normal write.
		_, err := sqlite.DbgAuditRawExec(ctx, "PRAGMA ignore_check_constraints = ON")
		require.NoError(t, err, "the pragma is how a corrupted row is reached at all")
		defer func() {
			_, _ = sqlite.DbgAuditRawExec(ctx, "PRAGMA ignore_check_constraints = OFF")
		}()

		_, err = sqlite.DbgAuditRawExec(ctx,
			"UPDATE instance_settings SET auto_acquire = 'a_state_from_a_newer_core' WHERE id = 1")
		require.NoError(t, err, "with the CHECK disabled the write succeeds")

		got, err := sqlite.NewAutoAcquireStore().AutoAcquire(ctx)
		require.Error(t, err, "an unrecognised stored state must be reported, not coerced")
		assert.ErrorIs(t, err, collab.ErrAutoAcquireInvalid)
		assert.Contains(t, err.Error(), "auto_acquire", "and it names the column to repair")
		assert.Equal(t, collab.AcquireOff, got,
			"and the state returned is the refusing one, so a caller that ignores the "+
				"error still acquires nothing")
	})
}

// A MISSING ROW IS OFF, not fetch_only. Migration 115 seeds exactly one row, so a miss
// means a broken database rather than a user who declined anything -- and the
// difference matters because "fetch but never seed" cannot cause loss, so the
// conservative substitution is the one that takes no action.
func TestAMissingInstanceRowReadsAsOff(t *testing.T) {
	runWithRollbackTxn(t, "acquire-missing", func(t *testing.T, ctx context.Context) {
		_, err := sqlite.DbgAuditRawExec(ctx, "DELETE FROM instance_settings WHERE id = 1")
		require.NoError(t, err)

		got, err := sqlite.NewAutoAcquireStore().AutoAcquire(ctx)
		require.NoError(t, err, "a missing row is an anomaly, not an error to report")
		assert.Equal(t, collab.AcquireOff, got,
			"and it reads as the state that acquires nothing, because this store's "+
				"documented default for a MISSING ROW is off even though the COLUMN's "+
				"default is fetch_only -- a missing row is not a new install")
	})
}

// THE SWITCH IS READ FRESH EVERY TIME, so turning it off takes effect immediately.
// A cached copy would make "the operator turned it off and it stopped" true one
// request late or one session late, and the test that would catch that is exactly this.
func TestTurningTheSwitchOffTakesEffectOnTheNextRead(t *testing.T) {
	runWithRollbackTxn(t, "acquire-live", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAutoAcquireStore()

		require.NoError(t, s.SetAutoAcquire(ctx, collab.AcquireFull))
		before, err := s.AutoAcquire(ctx)
		require.NoError(t, err)
		require.True(t, before.Seeds(), "precondition: full seeds")

		require.NoError(t, s.SetAutoAcquire(ctx, collab.AcquireOff))
		after, err := s.AutoAcquire(ctx)
		require.NoError(t, err)
		assert.Equal(t, collab.AcquireOff, after, "and the next read sees it")
		assert.False(t, after.Seeds(), "so seeding stopped")
		assert.False(t, after.Fetches(), "and so did acquiring")
	})
}

// THE STORE EXPOSES NO FILTERABLE QUERY, and that absence is the design: §6b.3
// requires enforcement "at the point the permission is read ... and NOT by omitting
// rows from a query", because #7's own note is that a refactor dropping the filter must
// not silently resume publishing.
//
// Asserted on the METHOD SET rather than on behaviour, because a behavioural test would
// pass today and fail to notice a query added tomorrow. A store that grew an
// "acquirable items" method would be the enforcement moving into a WHERE clause, where
// one refactor can drop it silently.
func TestTheStoreCannotBeUsedToFilterAcquisitionByHidingTheSwitch(t *testing.T) {
	// Every exported method is one of the two permitted operations: read the switch,
	// write the switch. Anything else on this type is a query somebody could filter.
	typ := reflect.TypeOf(&sqlite.AutoAcquireStore{})
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i).Name
		assert.Contains(t, []string{"AutoAcquire", "SetAutoAcquire"}, m,
			"AutoAcquireStore exposes %q, and the switch is only enforceable at the "+
				"point it is read -- a query method would let enforcement move into a "+
				"WHERE clause that a refactor can drop", m)
	}
}

// THE THREE STATES ARE INDIVIDUALLY OBSERVABLE, so a fourth cannot be smuggled in as a
// boolean. The plan requires this explicitly, and it is the property that stops a
// later change from collapsing the middle state into "not full".
func TestTheThreeStatesAreIndividuallyObservable(t *testing.T) {
	states := collab.AcquireStates()
	require.Len(t, states, 3, "three states, not two")

	// Distinct values, and each valid.
	seen := map[collab.AutoAcquire]bool{}
	for _, s := range states {
		assert.True(t, s.Valid(), "%q is valid", s)
		assert.NotEmpty(t, s.String())
		assert.False(t, seen[s], "%q appears twice", s)
		seen[s] = true
	}

	// AND THE THREE ARE MUTUALLY DISTINGUISHABLE BY BEHAVIOUR, which is the property a
	// boolean cannot have. If any two states behaved identically on every capability,
	// the type would be three names for two behaviours.
	type behaviour struct{ fetches, seeds bool }
	byState := map[collab.AutoAcquire]behaviour{}
	for _, s := range states {
		byState[s] = behaviour{s.Fetches(), s.Seeds()}
	}
	a, b, c := byState[collab.AcquireOff], byState[collab.AcquireFetchOnly], byState[collab.AcquireFull]
	assert.NotEqual(t, a, b, "off and fetch_only must differ, or the middle state is fiction")
	assert.NotEqual(t, b, c, "fetch_only and full must differ, or the middle state is off")
	assert.NotEqual(t, a, c)
}
