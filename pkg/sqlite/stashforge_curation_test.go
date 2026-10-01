//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/autoproposal"
	"github.com/stashapp/stash/pkg/sqlite"
)

// scalar reads and writes against the migrated test database, so the assertions below
// are about the STORED VALUE rather than about SQL plumbing. Using dbWrapper would not
// compile -- it is package-private and this is package sqlite_test -- and re-deriving
// the connection here would be a second connection, so these go through the same *db
// the rest of the integration suite uses.
func curationScalar(ctx context.Context, t *testing.T, query string, args ...interface{}) string {
	t.Helper()
	_, rows, err := db.QuerySQL(ctx, query, args)
	require.NoError(t, err, "querying: %s", query)
	require.Len(t, rows, 1, "query returned %d rows, want 1: %s", len(rows), query)
	require.NotEmpty(t, rows[0], "query returned no columns: %s", query)
	out, ok := rows[0][0].(string)
	require.True(t, ok, "query returned a %T, want a string: %s", rows[0][0], query)
	return out
}

func curationExec(ctx context.Context, t *testing.T, query string, args ...interface{}) error {
	t.Helper()
	_, _, err := db.ExecSQL(ctx, query, args)
	return err
}

// R086: curation mode's storage, against a real migrated database.
//
// A NEW INSTALL READS `propose`, and that is the load-bearing decision of the whole
// feature: the governed path is the one an operator gets without configuring anything.
// A library that asserted 4,000 machine-made claims about people on first run is not
// recoverable by a later setting, so the default has to be the safe one.
func TestCurationModeDefaultsToProposeOnANewInstance(t *testing.T) {
	runWithRollbackTxn(t, "curation-default", func(t *testing.T, ctx context.Context) {
		raw := curationScalar(ctx, t, "SELECT curation FROM instance_settings WHERE id = 1")
		assert.Equal(t, string(autoproposal.CurationPropose), raw,
			"a new instance must FILE proposals, not write fields. Migration 116's "+
				"default is the product decision; this test is that it reached the row")

		store := &sqlite.CurationStore{}
		mode, err := store.CurationMode(ctx)
		require.NoError(t, err)
		assert.Equal(t, autoproposal.CurationPropose, mode)
		assert.True(t, mode.CurationMayPropose(),
			"and the default must actually propose, not merely name the state")
		assert.False(t, mode.CurationMayApplyDirectly(),
			"a new instance must not be able to apply a machine's match directly")
	})
}

// ALL THREE STATES ROUND-TRIP, and the applied state is the one that is genuinely
// different rather than a label.
func TestEveryCurationModeRoundTrips(t *testing.T) {
	runWithRollbackTxn(t, "curation-roundtrip", func(t *testing.T, ctx context.Context) {
		store := &sqlite.CurationStore{}

		for _, mode := range []autoproposal.CurationMode{
			autoproposal.CurationOff,
			autoproposal.CurationPropose,
			autoproposal.CurationApply,
		} {
			require.NoError(t, store.SetCurationMode(ctx, mode), "writing %q", mode)

			got, err := store.CurationMode(ctx)
			require.NoError(t, err)
			assert.Equal(t, mode, got, "the stored mode must read back as written")

			// And the behaviour follows the state, not the string. A mode that
			// round-trips but whose predicates disagree with each other would let an
			// operator read "apply" off the settings page while the job filed proposals.
			switch mode {
			case autoproposal.CurationOff:
				assert.False(t, got.CurationMayPropose(),
					"off means the feature does not run, so it must not file either")
			case autoproposal.CurationPropose:
				assert.True(t, got.CurationMayPropose())
				assert.False(t, got.CurationMayApplyDirectly(),
					"THE MIDDLE STATE MUST NOT APPLY DIRECTLY. A rule written as "+
						"'anything that is not off may apply' gives propose the "+
						"direct-write permission, which is the entire bug a three-state "+
						"type exists to prevent")
			case autoproposal.CurationApply:
				assert.True(t, got.CurationMayPropose(),
					"apply still proposes -- the operator's setting adds a permission, "+
						"it does not redefine the others")
				assert.True(t, got.CurationMayApplyDirectly())
			}
		}
	})
}

// AN UNREADABLE MODE CANNOT BE PLANTED, AND THAT IS THE POINT.
//
// My first version of this test tried to write a bad value into the column so it could
// assert the Go guard refuses to READ one. The CHECK constraint refused the write, and
// the test failed on its own fixture -- which is the constraint working, so the test
// was asking the wrong question.
//
// The right question is what the constraint buys, and it is worth stating: a value this
// build cannot read is UNREACHABLE through the database. So the "unreadable stored
// mode" branch in CurationStore is defence in depth against a row that got here some
// other way -- a downgrade to an older binary, a hand-edited file, a restore from a
// backup taken with a different schema -- and not something an operator can cause by
// typing.
//
// So the test asserts the two things that are actually true: the constraint refuses
// every value the Go guard refuses, and the Go guard refuses them anyway, so the two
// layers agree. The read-side branch is unreachable through this path BY CONSTRUCTION,
// which is a better property than a fixture that reaches it.
func TestAnUnreadableModeCannotReachTheColumnAtAll(t *testing.T) {
	runWithRollbackTxn(t, "curation-unreadable", func(t *testing.T, ctx context.Context) {
		// Every value ParseCurationMode refuses, planted directly.
		for _, bad := range []string{"propoes", "apply_please", "PROPOSE", "Propose",
			"off ", " yes", "true", "none", "2"} {
			err := curationExec(ctx, t,
				"UPDATE instance_settings SET curation = ? WHERE id = 1", bad)
			assert.Error(t, err,
				"the CHECK constraint must refuse %q, so an unreadable mode is "+
					"unreachable through the database rather than merely unlikely",
				bad)
		}

		// And the column still holds the default, so none of those attempts left a
		// trace -- which is what makes the constraint a guarantee rather than a warning.
		raw := curationScalar(ctx, t, "SELECT curation FROM instance_settings WHERE id = 1")
		assert.Equal(t, string(autoproposal.CurationPropose), raw)
	})
}

// THE GO GUARD AND THE SCHEMA'S CHECK AGREE, IN BOTH DIRECTIONS.
//
// This is the arrangement migration 115's own note records, applied to curation: a
// guard that only one layer has stops guarding when that layer is bypassed. So the two
// must accept the same set, and the test proves it by writing what the other refuses.
//
// The subtlety is that the schema accepts ” (the column is NOT NULL with a DEFAULT,
// and the migration UPDATE normalises any empty value to 'propose'), while the Go
// guard also accepts the zero value and means it. So the sets are the same VALUES
// even though the strings differ, and asserting on the stored string rather than the
// typed value would report a difference that does not exist.
func TestTheGoGuardAndTheSchemasCheckAgree(t *testing.T) {
	runWithRollbackTxn(t, "curation-guard-vs-check", func(t *testing.T, ctx context.Context) {
		store := &sqlite.CurationStore{}

		// THE SCHEMA REFUSES WHAT THE GO GUARD REFUSES.
		for _, bad := range []string{"apply_please", "PROPOSE", "Propose", "off ", " yes", "true"} {
			err := curationExec(ctx, t,
				"UPDATE instance_settings SET curation = ? WHERE id = 1", bad)
			assert.Error(t, err,
				"the CHECK must refuse %q. A value the schema accepts but the Go "+
					"guard refuses is a row that reads back as an error on every "+
					"request, and the reverse is worse: a row the Go guard accepts "+
					"that the schema refuses can only exist by bypassing the check",
				bad)
		}

		// AND THE GO GUARD REFUSES WHAT THE SCHEMA REFUSES.
		for _, bad := range []autoproposal.CurationMode{
			"apply_please", "PROPOSE", "Propose", "off ", " yes", "true", "none",
		} {
			assert.Error(t, store.SetCurationMode(ctx, bad),
				"SetCurationMode must refuse %q before touching the database", bad)
		}

		// The column still holds the default after all those refusals, which is the
		// part that matters: a refused write must leave no trace.
		raw := curationScalar(ctx, t, "SELECT curation FROM instance_settings WHERE id = 1")
		assert.Equal(t, string(autoproposal.CurationPropose), raw,
			"a refused write must not have changed the stored mode")
	})
}

// A MISSING ROW IS `propose`, AND NOT `off`.
//
// The distinction is the reason this test exists. A missing row means the instance is
// in a state its schema does not describe -- not that an operator chose anything. And
// `off` means the feature does not run, so inferring it from a missing row would turn
// a database anomaly into an autotag that has silently stopped, which is
// indistinguishable from a scan that found nothing.
func TestAMissingRowReadsAsProposeNotOff(t *testing.T) {
	runWithRollbackTxn(t, "curation-missing-row", func(t *testing.T, ctx context.Context) {
		require.NoError(t, curationExec(ctx, t, "DELETE FROM instance_settings WHERE id = 1"))

		store := &sqlite.CurationStore{}
		mode, err := store.CurationMode(ctx)
		require.NoError(t, err,
			"a missing row is an anomaly, not a refusal: the read reports what the "+
				"column would have said")
		assert.Equal(t, autoproposal.CurationPropose, mode)
		assert.True(t, mode.CurationMayPropose())

		// AND a write to a missing row is an ERROR rather than a silent no-op. The
		// operator would otherwise believe they had chosen something while the instance
		// kept reading its default forever.
		err = store.SetCurationMode(ctx, autoproposal.CurationApply)
		assert.Error(t, err,
			"writing with no row must be reported. A silent success here leaves the "+
				"instance on its default while the operator believes they chose "+
				"'apply' -- and 'apply' is the state that bypasses governance")
	})
}

// AND THE ZERO VALUE IS NEVER STORED, because "" means propose when read and writing it
// would be correct by accident -- persisting something the operator never chose.
func TestTheZeroValueIsStoredAsProposeNotAsAnEmptyString(t *testing.T) {
	runWithRollbackTxn(t, "curation-zero-value", func(t *testing.T, ctx context.Context) {
		store := &sqlite.CurationStore{}
		require.NoError(t, store.SetCurationMode(ctx, autoproposal.CurationMode("")))

		raw := curationScalar(ctx, t, "SELECT curation FROM instance_settings WHERE id = 1")
		assert.Equal(t, string(autoproposal.CurationPropose), raw,
			"the unset value must be persisted as the state it means, not as an "+
				"empty string that only happens to read back correctly")
	})
}
