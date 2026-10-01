//go:build integration
// +build integration

package sqlite_test

import (
	"context"

	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/sqlite"
)

// Consent against a real database. M3 step 3.1.
//
// Why this file exists when internal/collab/consent_test.go already covers the
// logic: that file talks to a fake Queryer, and a fake cannot catch a mismatch
// between the interface collab declares and what the driver actually returns.
// It very nearly did not -- the first version of collab.Rows omitted Close(),
// and the fake was written to match the interface, so every fake test passed
// while the real adapter could not compile. A green fake suite beside a broken
// adapter is the exact shape of the bug that found the M2c store seam, so the
// rule here is: a fake proves the logic, only a real database proves the SQL.

// TestConsentStore_DefaultsToOptedInWithNoRow is the end-to-end version of the
// most consequential default: a real database, a real user, no consent row.
//
// The point is that NOTHING wrote a row. If a future migration seeds a consent
// row for every user, this fails -- correctly, because a seeded row is a row
// nobody answered, and the disclosure screen must be able to tell those apart.
func TestConsentStore_DefaultsToOptedInWithNoRow(t *testing.T) {
	runWithRollbackTxn(t, "defaults to opted in with no row", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "sfConsentNeverAsked")
		store := sqlite.NewConsentStore()

		optedIn, err := store.OptedIn(ctx, int64(userID))
		require.NoError(t, err)
		require.True(t, optedIn,
			"a user with no consent row must default to opted in (spec §6.1)")

		// The absence must be visible as absence, not flattened into a value:
		// the disclosure screen prompts only the never-asked.
		choice, found, err := collab.ReadConsent(ctx, store, int64(userID))
		require.NoError(t, err)
		require.False(t, found, "ReadConsent reported found=true for a user with no row")
		require.Equal(t, collab.ChoiceOptedIn, choice,
			"an absent row must return the documented default as the value")

		reprompt, err := store.NeedsDisclosureReprompt(ctx, int64(userID))
		require.NoError(t, err)
		require.True(t, reprompt, "a user who was never asked must be prompted")
	})
}

// TestConsentStore_OptOutIsStickyAcrossReads re-reads through the real store,
// because the fake-based test could not catch a store returning a single-use
// result set -- which is what an absent row looks like, and an absent row means
// opted in.
func TestConsentStore_OptOutIsStickyAcrossReads(t *testing.T) {
	runWithRollbackTxn(t, "opt out is sticky across reads", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "sfConsentOptOut")
		store := sqlite.NewConsentStore()

		require.NoError(t, store.Decide(ctx, int64(userID), collab.ChoiceOptedOut))

		// Three separate reads. If any reported opted in, a real publish would
		// go out for a user who declined.
		for i := 0; i < 3; i++ {
			optedIn, err := store.OptedIn(ctx, int64(userID))
			require.NoErrorf(t, err, "read %d", i)
			require.Falsef(t, optedIn, "read %d reported opted in for a user who opted out", i)
		}

		choice, found, err := collab.ReadConsent(ctx, store, int64(userID))
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, collab.ChoiceOptedOut, choice)

		reprompt, err := store.NeedsDisclosureReprompt(ctx, int64(userID))
		require.NoError(t, err)
		require.False(t, reprompt,
			"an opted-out user must not be re-prompted: the prompt is a dialog with a Share button")
	})
}

// TestConsentStore_DecideReplacesTheAnswer exercises the upsert, including the
// second call. A store that inserted without ON CONFLICT would fail here with a
// primary-key violation, which is the race the upsert exists to remove.
func TestConsentStore_DecideReplacesTheAnswer(t *testing.T) {
	runWithRollbackTxn(t, "decide replaces the answer", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "sfConsentFlip")
		store := sqlite.NewConsentStore()

		require.NoError(t, store.Decide(ctx, int64(userID), collab.ChoiceOptedOut))
		require.NoError(t, store.Decide(ctx, int64(userID), collab.ChoiceOptedIn),
			"the second Decide must upsert, not violate the primary key")

		optedIn, err := store.OptedIn(ctx, int64(userID))
		require.NoError(t, err)
		require.True(t, optedIn)

		// Exactly one row, not two. A second row would make "opted in or out"
		// a question with two answers.
		require.Equal(t, int64(1), count(t, ctx,
			"SELECT COUNT(*) FROM consent_preferences WHERE user_id = ?", userID),
			"one user must have exactly one consent row")
	})
}

// TestConsentStore_RecordsTheCurrentDisclosureVersion checks the write side of
// the re-prompt rule: answering bumps the version to current, so a user who has
// just seen the list is not immediately stale.
func TestConsentStore_RecordsTheCurrentDisclosureVersion(t *testing.T) {
	runWithRollbackTxn(t, "records the current disclosure version", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "sfConsentVersion")
		store := sqlite.NewConsentStore()

		require.NoError(t, store.Decide(ctx, int64(userID), collab.ChoiceOptedIn))

		require.Equal(t, int64(collab.CurrentDisclosureVersion), scalar(t, ctx,
			"SELECT disclosure_version FROM consent_preferences WHERE user_id = ?", userID),
			"answering must record the version the user was shown")

		reprompt, err := store.NeedsDisclosureReprompt(ctx, int64(userID))
		require.NoError(t, err)
		require.False(t, reprompt, "a user who just answered at the current version must not be re-prompted")
	})
}

// TestConsentStore_RefusesAnInvalidChoice proves the validation happens before
// the database, so the failure is a clear domain error rather than a CHECK
// constraint violation naming a column.
func TestConsentStore_RefusesAnInvalidChoice(t *testing.T) {
	runWithRollbackTxn(t, "refuses an invalid choice", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "sfConsentInvalid")
		store := sqlite.NewConsentStore()

		err := store.Decide(ctx, int64(userID), collab.ShareChoice("maybe"))
		require.Error(t, err, "Decide accepted an invalid choice")
		// The message comes from collab.SetConsent, which Decide delegates to --
		// that delegation is the point: the domain validates, so a caller cannot
		// reach the store's own duplicate check with a bad value. What the
		// message must NOT be is a driver/CHECK error, which is what a missing
		// domain check would produce here.
		require.NotContains(t, err.Error(), "CHECK constraint",
			"error %q should be the domain refusal, not a database constraint", err)
		require.NotContains(t, err.Error(), "sqlite",
			"error %q should be the domain refusal, not a driver error", err)

		require.Equal(t, int64(0), count(t, ctx,
			"SELECT COUNT(*) FROM consent_preferences WHERE user_id = ?", userID),
			"an invalid choice must write nothing")
	})
}

// TestConsentStore_RejectsAChoiceTheSchemaForbids goes one layer lower on
// purpose: it writes around the store to prove the CHECK constraint is actually
// load-bearing. A schema constraint nobody has tried to violate is a comment.
func TestConsentStore_RejectsAChoiceTheSchemaForbids(t *testing.T) {
	runWithRollbackTxn(t, "the schema CHECK is load-bearing", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "sfConsentCheck")

		err := execErr(t, ctx,
			"INSERT INTO consent_preferences (user_id, metadata_share) VALUES (?, ?)",
			userID, "yes-please")
		require.Error(t, err,
			"the schema accepted metadata_share='yes-please': the CHECK constraint is not doing its job")
	})
}

// TestConsentStore_ForeignKeyRefusesAnUnknownUser checks the referential
// integrity that lets the read path trust user_id, so a consent row can never
// belong to a user that does not exist.
func TestConsentStore_ForeignKeyRefusesAnUnknownUser(t *testing.T) {
	runWithRollbackTxn(t, "foreign key refuses an unknown user", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewConsentStore()
		err := store.Decide(ctx, 999999, collab.ChoiceOptedOut)
		require.Error(t, err, "a consent row was accepted for a user that does not exist")
	})
}

// TestConsentStore_ReadsInsideTheCallersTransaction is the property the publish
// path depends on: consent must be read and enforced in the SAME transaction as
// the publish, or a publish could commit against a consent row a concurrent
// writer revoked.
//
// The check is that the read sees the caller's uncommitted write, which is what
// "in the transaction" means in practice.
func TestConsentStore_ReadsInsideTheCallersTransaction(t *testing.T) {
	runWithRollbackTxn(t, "reads inside the caller's transaction", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "sfConsentTxn")
		store := sqlite.NewConsentStore()

		// Write directly, bypassing the store, so the only way to see the value
		// is through the caller's transaction.
		err := execErr(t, ctx,
			"INSERT INTO consent_preferences (user_id, metadata_share, decided_at, disclosure_version) VALUES (?, ?, ?, ?)",
			userID, string(collab.ChoiceOptedOut), time.Now(), collab.CurrentDisclosureVersion)
		require.NoError(t, err)

		optedIn, err := store.OptedIn(ctx, int64(userID))
		require.NoError(t, err)
		require.False(t, optedIn,
			"the store read outside the caller's transaction and fell through to the opted-in default")
	})
}
