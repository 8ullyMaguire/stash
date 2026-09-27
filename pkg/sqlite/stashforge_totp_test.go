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

// The 2FA store, against a real database. M4 step 4.2.
//
// The load-bearing test here is TestTOTPStore_SpendIsAtomicUnderConcurrency.
// Everything else is plumbing; that one is the plan's requirement, and a fake
// TOTPStore cannot prove it -- a fake's Spend is a map write in one goroutine,
// and the race lives in SQL.

var totpTestKey = []byte("0123456789abcdef0123456789abcdef")

func newTOTPStore() *sqlite.TOTPStore {
	return sqlite.NewTOTPStore(
		func() ([]byte, error) { return totpTestKey, nil },
		collab.DefaultTOTPRequired,
	)
}

func sfEnrolledUser(t *testing.T, ctx context.Context, name string) (int, *collab.TOTPSecret) {
	t.Helper()
	userID := mustCreateUser(ctx, t, name)
	secret, err := collab.NewTOTPSecret()
	require.NoError(t, err)
	require.NoError(t, newTOTPStore().SetSecret(ctx, userID, secret.Reveal()))
	return userID, &secret
}

// TestTOTPStore_SecretIsSealedAtRest: the whole reason this store exists. The
// column must not contain the secret.
func TestTOTPStore_SecretIsSealedAtRest(t *testing.T) {
	store := newTOTPStore()

	runWithRollbackTxn(t, "secret is sealed", func(t *testing.T, ctx context.Context) {
		userID, secret := sfEnrolledUser(t, ctx, "sfTotpSealed")

		stored := scalar(t, ctx, "SELECT secret FROM user_totp WHERE user_id = ?", userID)
		raw, ok := stored.(string)
		require.True(t, ok, "secret should be a string, got %T", stored)

		assert.NotEqual(t, secret.Reveal(), raw, "the column holds the plaintext secret")
		assert.True(t, collab.IsEncryptedTOTPSecret(raw), "the column value is not marked sealed")
		assert.NotContains(t, raw, secret.Reveal())

		// And it round-trips through the store.
		got, err := store.Secret(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, secret.Reveal(), got)
	})
}

// TestTOTPStore_NotEnrolledIsEmptyNotAnError: the distinction the auth layer
// depends on. Empty means "one factor", and it is not an error.
func TestTOTPStore_NotEnrolledIsEmptyNotAnError(t *testing.T) {
	store := newTOTPStore()

	runWithRollbackTxn(t, "not enrolled", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "sfTotpNone")

		got, err := store.Secret(ctx, userID)
		require.NoError(t, err, "a user with no 2FA row is not an error: it is the one-factor case")
		assert.Empty(t, got)

		enrolled, err := store.Enrolled(ctx, userID)
		require.NoError(t, err)
		assert.False(t, enrolled)
	})
}

// TestTOTPStore_AnEmptyStoredSecretIsUnrepresentable: the other half of the
// "not enrolled" distinction.
//
// A row with an empty secret would be a CORRUPT enrolment, and reading it as "not
// enrolled" would let a user whose secret was wiped log in with one factor while
// believing they had two. The schema's CHECK (length(secret) > 0) makes it
// unrepresentable, which is stronger than the store's own guard -- so this tests
// the SCHEMA, going around the store, which is what a real test of a constraint
// has to do.
//
// My first version tried to write the row and asserted the store's error. It
// failed with "CHECK constraint failed: secret", which is the schema doing its
// job one layer earlier than the test assumed.
func TestTOTPStore_AnEmptyStoredSecretIsUnrepresentable(t *testing.T) {
	store := newTOTPStore()

	runWithRollbackTxn(t, "empty stored secret is refused", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "sfTotpEmpty")

		err := execErr(t, ctx,
			"INSERT INTO user_totp (user_id, secret, used_steps) VALUES (?, '', '')", userID)
		require.Error(t, err, "an empty stored secret must be unrepresentable, not merely unread")

		// And the store refuses to write one too, so the value cannot arrive by
		// either route.
		require.Error(t, store.SetSecret(ctx, userID, "  "),
			"the store must refuse an empty secret rather than sealing whitespace")
	})
}

// TestTOTPStore_SpendIsSingleUse is the plan's requirement, against real SQL.
func TestTOTPStore_SpendIsSingleUse(t *testing.T) {
	store := newTOTPStore()

	runWithRollbackTxn(t, "a step is spent once", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "sfTotpSpend")
		const step = int64(59683680)

		first, err := store.SpendTOTPStep(ctx, userID, step)
		require.NoError(t, err)
		assert.True(t, first, "the first use of a step must succeed")

		second, err := store.SpendTOTPStep(ctx, userID, step)
		require.NoError(t, err)
		assert.False(t, second, "the SAME step must be refused: this is the single-use rule")

		// A different step is unaffected.
		other, err := store.SpendTOTPStep(ctx, userID, step+1)
		require.NoError(t, err)
		assert.True(t, other, "a different step is a different code and must be accepted")

		steps, err := store.UsedSteps(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, []int64{step, step + 1}, steps)
	})
}

// TestTOTPStore_SpendIsAtomicUnderConcurrency is the test a fake cannot do.
//
// Twenty goroutines race to spend the SAME step against real SQL. If the check
// and the record are two statements, several of them read "not spent" and all of
// them succeed -- which is exactly the replay the plan forbids, and exactly what
// a single-threaded test can never see.
func TestTOTPStore_SpendIsAtomicUnderConcurrency(t *testing.T) {
	const racers = 20
	const step = int64(59683681)
	winners := make(chan bool, racers)

	// Each goroutine needs its own connection, so this cannot run inside the
	// shared rollback transaction -- which is the point: the race is between
	// transactions, not within one.
	runWithRollbackTxn(t, "prepare", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "sfTotpRace")
		_, err := newTOTPStore().SpendTOTPStep(ctx, userID, step-1)
		require.NoError(t, err)
	})

	// The user is created with its OWN transaction that COMMITS. It cannot live
	// inside runWithRollbackTxn, because that rolls its work back and the racing
	// goroutines -- each on its own connection -- would not see the row at all.
	// Every one of them would fail on a foreign key, and "0 accepted" would look
	// like a passing atomicity result rather than a missing fixture.
	userID := int64(0)
	require.NoError(t, withTxn(func(ctx context.Context) error {
		id, err := createUserIn(ctx, t, "sfTotpRaceUser")
		if err != nil {
			return err
		}
		userID = int64(id)
		return nil
	}))
	t.Cleanup(func() {
		_ = withTxn(func(ctx context.Context) error {
			_, _, err := db.ExecSQL(ctx, "DELETE FROM users WHERE id = ?", []interface{}{userID})
			return err
		})
	})

	done := make(chan struct{})
	for i := 0; i < racers; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			// A background context per goroutine: the shared one carries the
			// test's transaction, which is exactly what must NOT be shared here.
			// Each racer gets its OWN transaction, because every stash store call
			// requires one on the context (dbWrapper routes through getTx) and
			// because separate transactions contending for one row is the actual
			// race under test. Sharing the test's transaction would serialise them
			// and prove nothing.
			var ok bool
			err := withTxn(func(ctx context.Context) error {
				var e error
				ok, e = newTOTPStore().SpendTOTPStep(ctx, int(userID), step)
				return e
			})
			winners <- err == nil && ok
		}()
	}
	for i := 0; i < racers; i++ {
		<-done
	}
	close(winners)

	wins := 0
	for ok := range winners {
		if ok {
			wins++
		}
	}
	assert.Equal(t, 1, wins, "%d of %d concurrent uses of one step were accepted, want exactly 1: the check and the record must be one statement", wins, racers)
}

// TestTOTPStore_ReEnrolmentClearsTheSpentSteps: a new secret gets new codes, so
// keeping the old record would let a step number from the previous enrolment
// refuse the first login after re-scanning a QR code.
func TestTOTPStore_ReEnrolmentClearsTheSpentSteps(t *testing.T) {
	store := newTOTPStore()

	runWithRollbackTxn(t, "re-enrolment clears steps", func(t *testing.T, ctx context.Context) {
		userID, _ := sfEnrolledUser(t, ctx, "sfTotpReEnrol")
		const step = int64(59683682)

		ok, err := store.SpendTOTPStep(ctx, userID, step)
		require.NoError(t, err)
		require.True(t, ok)

		// Re-enrol.
		fresh, err := collab.NewTOTPSecret()
		require.NoError(t, err)
		require.NoError(t, store.SetSecret(ctx, userID, fresh.Reveal()))

		steps, err := store.UsedSteps(ctx, userID)
		require.NoError(t, err)
		assert.Empty(t, steps, "a new enrolment must not inherit the previous one's replay record")

		ok, err = store.SpendTOTPStep(ctx, userID, step)
		require.NoError(t, err)
		assert.True(t, ok, "the same step number under a new secret is a different code")
	})
}

// TestTOTPStore_OwnerIsRequiredAndOthersAreNot: the policy asymmetry.
func TestTOTPStore_OwnerIsRequiredAndOthersAreNot(t *testing.T) {
	store := newTOTPStore()

	runWithRollbackTxn(t, "policy", func(t *testing.T, ctx context.Context) {
		owner := mustCreateUser(ctx, t, "sfTotpOwner")
		member := mustCreateUser(ctx, t, "sfTotpMember")
		require.NoError(t, exec(t, ctx, "UPDATE users SET is_owner = 1 WHERE id = ?", owner))

		req, err := store.Required(ctx, owner)
		require.NoError(t, err)
		assert.True(t, req, "the owner must require 2FA: it is the account whose compromise publishes every library")

		req, err = store.Required(ctx, member)
		require.NoError(t, err)
		assert.False(t, req, "2FA is optional for non-owners, or a small instance cannot onboard anyone")

		// A user id that does not exist is an error, not "not required".
		_, err = store.Required(ctx, 999999)
		require.Error(t, err, "an unknown user must not read as 'not required'")
	})
}

// TestTOTPStore_RemoveSecretUnenrols: and removes the row rather than blanking
// it, so "not enrolled" and "enrolled with an empty secret" cannot be confused.
func TestTOTPStore_RemoveSecretUnenrols(t *testing.T) {
	store := newTOTPStore()

	runWithRollbackTxn(t, "unenrol", func(t *testing.T, ctx context.Context) {
		userID, _ := sfEnrolledUser(t, ctx, "sfTotpRemove")

		enrolled, err := store.Enrolled(ctx, userID)
		require.NoError(t, err)
		require.True(t, enrolled)

		require.NoError(t, store.RemoveSecret(ctx, userID))

		enrolled, err = store.Enrolled(ctx, userID)
		require.NoError(t, err)
		assert.False(t, enrolled)

		got, err := store.Secret(ctx, userID)
		require.NoError(t, err)
		assert.Empty(t, got, "after unenrolment the store must read as not-enrolled, not as corrupt")
	})
}

// TestTOTPStore_CascadesWhenTheUserIsDeleted: a 2FA row for a user who no longer
// exists is removed.
func TestTOTPStore_CascadesWhenTheUserIsDeleted(t *testing.T) {
	runWithRollbackTxn(t, "cascade", func(t *testing.T, ctx context.Context) {
		userID, _ := sfEnrolledUser(t, ctx, "sfTotpDoomed")
		require.Equal(t, int64(1), count(t, ctx, "SELECT count(*) FROM user_totp WHERE user_id = ?", userID))

		require.NoError(t, exec(t, ctx, "DELETE FROM users WHERE id = ?", userID))
		assert.Equal(t, int64(0), count(t, ctx, "SELECT count(*) FROM user_totp WHERE user_id = ?", userID))
	})
}

// TestTOTPStore_StoredSecretIsNotReadableWithAnotherInstanceKey: a stolen backup
// plus a guessed key is still a 2FA bypass, but at least the keys are not
// interchangeable between instances.
func TestTOTPStore_StoredSecretIsNotReadableWithAnotherInstanceKey(t *testing.T) {
	runWithRollbackTxn(t, "wrong key", func(t *testing.T, ctx context.Context) {
		userID, _ := sfEnrolledUser(t, ctx, "sfTotpWrongKey")

		other := sqlite.NewTOTPStore(
			func() ([]byte, error) { return []byte("ffffffffffffffffffffffffffffffff"), nil },
			collab.DefaultTOTPRequired)

		_, err := other.Secret(ctx, userID)
		require.ErrorIs(t, err, collab.ErrTOTPSecretCorrupt,
			"a secret sealed with another instance's key must not open")
	})
}
