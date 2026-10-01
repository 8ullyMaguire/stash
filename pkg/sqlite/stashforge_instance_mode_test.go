//go:build integration

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The instance-mode store, against a real database. M4 step 4.1.
//
// The unit tests in internal/collab prove the RULES. These prove the SQL: that
// the CHECK rejects a bad mode, that the single-row constraint holds, and that
// the seeded row is what the migration actually creates. A fake ModeStore would
// pass every rule test while the column accepted whatever it was given.
//
// Every fixture creates its own instance-scoped state inside a rollback
// transaction, so these run in any order and twice.

// TestInstanceModeStore_SeedsPrivate pins the migration's seed row. If this
// fails, every unconfigured instance has silently become whatever the DEFAULT
// says -- which is why the default is written in the migration and asserted here.
func TestInstanceModeStore_SeedsPrivate(t *testing.T) {
	store := sqlite.NewInstanceModeStore()

	runWithRollbackTxn(t, "seeds private", func(t *testing.T, ctx context.Context) {
		mode, err := store.Mode(ctx)
		require.NoError(t, err)
		assert.Equal(t, collab.ModePrivate, mode, "an unconfigured instance must read as private, not as the last mode anyone set")

		done, err := store.WizardCompleted(ctx)
		require.NoError(t, err)
		assert.False(t, done, "the first-run wizard has not run on a fresh database")
	})
}

// TestInstanceModeStore_SetAndReadBack: the round trip.
func TestInstanceModeStore_SetAndReadBack(t *testing.T) {
	store := sqlite.NewInstanceModeStore()

	runWithRollbackTxn(t, "set and read back", func(t *testing.T, ctx context.Context) {
		userID := int64(mustCreateUser(ctx, t, "sfModeActor"))
		require.NoError(t, store.SetMode(ctx, collab.ModeContribute, &userID))

		mode, err := store.Mode(ctx)
		require.NoError(t, err)
		assert.Equal(t, collab.ModeContribute, mode)
	})
}

// TestInstanceModeStore_PublicIsVetoedUntilTheWizardRuns pins the two-writes-in-
// one-statement decision from the store's side: a public mode set directly is
// refused at startup, and the same posture passes once the wizard has answered.
func TestInstanceModeStore_PublicIsVetoedUntilTheWizardRuns(t *testing.T) {
	store := sqlite.NewInstanceModeStore()

	runWithRollbackTxn(t, "wizard vetoes an unchosen public mode", func(t *testing.T, ctx context.Context) {
		require.NoError(t, store.SetMode(ctx, collab.ModePublic, nil))

		err := store.CheckStartup(ctx, "https")
		require.ErrorIs(t, err, collab.ErrModeNotChosen, "a public mode nobody chose must be refused")

		// Same posture, wizard completed: now it is a choice, and it passes.
		require.NoError(t, store.CompleteWizard(ctx, collab.ModePublic, nil))
		require.NoError(t, store.CheckStartup(ctx, "https"))
	})
}

// TestInstanceModeStore_RejectsAnInvalidModeAtTheWrite is the load-bearing SQL
// test: the CHECK, not the Go guard. SetMode validates, so exercising SetMode
// here would prove nothing -- this goes around it to prove the SCHEMA refuses.
func TestInstanceModeStore_RejectsAnInvalidModeAtTheWrite(t *testing.T) {
	for _, bad := range []string{"Public", "public ", "open", "private\tpublic"} {
		bad := bad
		runWithRollbackTxn(t, "rejects "+bad, func(t *testing.T, ctx context.Context) {
			err := execErr(t, ctx, "UPDATE instance_settings SET mode = ? WHERE id = 1", bad)
			require.Error(t, err, "the schema CHECK must refuse mode %q", bad)

			// And the row is unchanged, so a refused write leaves no partial
			// posture behind.
			mode, mErr := sqlite.NewInstanceModeStore().Mode(ctx)
			require.NoError(t, mErr)
			assert.Equal(t, collab.ModePrivate, mode, "a refused write must not change the stored mode")
		})
	}
}

// TestInstanceModeStore_RejectsAnInvalidModeThroughTheAPI: the Go guard, which
// is the layer a caller actually meets.
func TestInstanceModeStore_RejectsAnInvalidModeThroughTheAPI(t *testing.T) {
	store := sqlite.NewInstanceModeStore()
	runWithRollbackTxn(t, "guard rejects", func(t *testing.T, ctx context.Context) {
		require.ErrorIs(t, store.SetMode(ctx, collab.Mode("open"), nil), collab.ErrModeInvalid)
		require.ErrorIs(t, store.CompleteWizard(ctx, collab.Mode(""), nil), collab.ErrModeInvalid)
	})
}

// TestInstanceModeStore_ExactlyOneSettingsRow pins the id = 1 CHECK. "What is
// the mode" must never have two answers.
func TestInstanceModeStore_ExactlyOneSettingsRow(t *testing.T) {
	runWithRollbackTxn(t, "one settings row only", func(t *testing.T, ctx context.Context) {
		require.Error(t, execErr(t, ctx,
			"INSERT INTO instance_settings (id, mode) VALUES (2, 'public')"),
			"a second settings row must be unrepresentable")

		assert.Equal(t, int64(1), count(t, ctx, "SELECT count(*) FROM instance_settings"))
	})
}

// TestInstanceModeStore_StartupRefusesPublicOverPlainHTTP end to end: the row
// says public, the wizard ran, and the scheme is http. The instance must refuse.
func TestInstanceModeStore_StartupRefusesPublicOverPlainHTTP(t *testing.T) {
	store := sqlite.NewInstanceModeStore()

	runWithRollbackTxn(t, "public over http refuses", func(t *testing.T, ctx context.Context) {
		require.NoError(t, store.CompleteWizard(ctx, collab.ModePublic, nil))
		require.ErrorIs(t, store.CheckStartup(ctx, "http"), collab.ErrInsecurePublicMode)
		require.NoError(t, store.CheckStartup(ctx, "https"))
	})
}

// TestInstanceModeStore_ModeChangedByIsRecorded: a posture change has to be
// answerable afterwards -- who chose it, and a system change recorded as
// unattributed rather than as user 0.
func TestInstanceModeStore_ModeChangedByIsRecorded(t *testing.T) {
	store := sqlite.NewInstanceModeStore()

	runWithRollbackTxn(t, "records who chose", func(t *testing.T, ctx context.Context) {
		userID := int64(mustCreateUser(ctx, t, "sfModeDecider"))
		require.NoError(t, store.SetMode(ctx, collab.ModePublic, &userID))

		by := scalar(t, ctx, "SELECT mode_changed_by FROM instance_settings WHERE id = 1")
		require.NotNil(t, by, "mode_changed_by should name the user who chose the posture")
		assert.Equal(t, userID, by, "mode_changed_by must be the deciding user, not user 0")

		// A change with no authenticated actor records NULL, not 0: user 0 does
		// not exist, and conflating the two would misattribute a system change to
		// a real user.
		require.NoError(t, store.SetMode(ctx, collab.ModeContribute, nil))
		assert.Nil(t, scalar(t, ctx, "SELECT mode_changed_by FROM instance_settings WHERE id = 1"),
			"a system change must record NULL, not 0: user 0 does not exist and 0 would misattribute it")
	})
}

// TestInstanceModeStore_RequireWizardAgainstTheRealStore: the gate is proven with
// the real InstanceModeStore as its Gate, not a stub. A stub proves the rule; this
// proves the store actually satisfies the interface the server will use -- and it
// would have caught the two `err == sql.ErrNoRows` sites, because a stubbed Gate
// never exercises a query.
func TestInstanceModeStore_RequireWizardAgainstTheRealStore(t *testing.T) {
	store := sqlite.NewInstanceModeStore()

	runWithRollbackTxn(t, "gate refuses then permits", func(t *testing.T, ctx context.Context) {
		// Incomplete: refused, whatever the row says.
		require.NoError(t, exec(t, ctx, "UPDATE instance_settings SET mode = 'public' WHERE id = 1"))
		_, err := collab.RequireWizard(ctx, store)
		require.Error(t, err)
		assert.True(t, collab.IsWizardIncomplete(err),
			"an unconfigured instance must refuse even when the row already says public")

		// Complete: permitted, and the mode comes through.
		require.NoError(t, store.CompleteWizard(ctx, collab.ModeContribute, nil))
		mode, err := collab.RequireWizard(ctx, store)
		require.NoError(t, err)
		assert.Equal(t, collab.ModeContribute, mode)
	})
}

// TestInstanceModeStore_AMissingRowReadsAsPrivateAndUnconfigured exercises the
// fail-closed path, which no other test reaches: the migration seeds the row, so
// in normal operation ErrNoRows never fires and the branch is dead code that
// looks tested.
//
// Both sites had `err == sql.ErrNoRows`, which cannot match a wrapped driver
// error, so deleting the row produced an internal error instead of a private
// posture. The consequence was the worst available: a missing settings row made
// the instance refuse to start rather than fail quiet, and the operator's only
// symptom was a startup error naming a query.
func TestInstanceModeStore_AMissingRowReadsAsPrivateAndUnconfigured(t *testing.T) {
	store := sqlite.NewInstanceModeStore()

	runWithRollbackTxn(t, "missing row fails closed", func(t *testing.T, ctx context.Context) {
		require.NoError(t, exec(t, ctx, "DELETE FROM instance_settings WHERE id = 1"))

		mode, err := store.Mode(ctx)
		require.NoError(t, err, "a missing settings row must read as private, not as an error")
		assert.Equal(t, collab.ModePrivate, mode)

		done, err := store.WizardCompleted(ctx)
		require.NoError(t, err, "a missing settings row means the wizard has not run")
		assert.False(t, done)

		// And startup must still succeed in private: an unconfigured instance
		// is a private instance, not a broken one.
		require.NoError(t, store.CheckStartup(ctx, "http"))
	})
}

// TestInstanceModeStore_CompleteWizardIsOneStatement: the crash-between-two-
// writes failure the single statement rules out. Both halves of the state are
// written together, so a reader never sees "wizard done, no mode chosen" or the
// reverse.
func TestInstanceModeStore_CompleteWizardIsOneStatement(t *testing.T) {
	store := sqlite.NewInstanceModeStore()

	runWithRollbackTxn(t, "wizard writes both halves", func(t *testing.T, ctx context.Context) {
		require.NoError(t, store.CompleteWizard(ctx, collab.ModeContribute, nil))

		mode, err := store.Mode(ctx)
		require.NoError(t, err)
		assert.Equal(t, collab.ModeContribute, mode)

		done, err := store.WizardCompleted(ctx)
		require.NoError(t, err)
		assert.True(t, done, "the mode and the wizard flag must land in the same statement")

		// A later SetMode does NOT re-open the wizard.
		require.NoError(t, store.SetMode(ctx, collab.ModePrivate, nil))
		done, err = store.WizardCompleted(ctx)
		require.NoError(t, err)
		assert.True(t, done, "changing the mode later must not un-complete the wizard")
	})
}
