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

// The library-access store, against a real database. M4 step 4.3.
//
// The rules are proven in internal/collab/access_test.go. What can only be shown
// here is the SQL: the primary key's idempotency, the CASCADE when a user is
// deleted, and the granted_at refresh on re-grant -- a behaviour that is easy to
// specify and easy to get wrong with ON CONFLICT DO NOTHING.
//
// Every fixture creates its own user and library inside a rollback transaction,
// so these run in any order and twice.

// sfLibrary creates a library owned by a fresh user, and returns (userID, libID).
// Per-fixture, never a fixed id: a fixed id passes exactly once and then dies in
// the full suite, because the dev database persists between runs.
//
// is_private is left NULL rather than set. NULL is a deliberate third state --
// "defer to the owner's consent row" -- and a fixture that hardcodes a value is
// asserting a sharing decision these access tests are not about. My first version
// wrote `private` with no value and the schema, correctly, refused it: the column
// is `is_private`.
func sfLibrary(t *testing.T, ctx context.Context, name string) (int64, int64) {
	t.Helper()
	owner := int64(mustCreateUser(ctx, t, "sfLibOwner-"+name))
	require.NoError(t, exec(t, ctx,
		"INSERT INTO libraries (user_id, name) VALUES (?, ?)", owner, name))
	libID := scalar(t, ctx, "SELECT id FROM libraries WHERE user_id = ? AND name = ?", owner, name).(int64)
	return owner, libID
}

// TestLibraryAccess_GrantAndRevokeAreIdempotent: the primary key is (user,
// library), so a repeat is the same grant rather than a second row.
func TestLibraryAccess_GrantAndRevokeAreIdempotent(t *testing.T) {
	store := sqlite.NewLibraryAccessStore()

	runWithRollbackTxn(t, "grant twice, revoke twice", func(t *testing.T, ctx context.Context) {
		owner, lib := sfLibrary(t, ctx, "sfAccessIdempotent")
		user := int64(mustCreateUser(ctx, t, "sfAccessUserA"))

		has, err := store.HasAccess(ctx, user, lib)
		require.NoError(t, err)
		assert.False(t, has, "a fresh user has no grant")

		require.NoError(t, store.Grant(ctx, user, lib))
		require.NoError(t, store.Grant(ctx, user, lib), "re-granting must not error")

		assert.Equal(t, int64(1), count(t, ctx,
			"SELECT count(*) FROM user_library_access WHERE user_id = ? AND library_id = ?", user, lib),
			"a repeated grant must not create a second row")

		has, err = store.HasAccess(ctx, user, lib)
		require.NoError(t, err)
		assert.True(t, has)

		require.NoError(t, store.Revoke(ctx, user, lib))
		require.NoError(t, store.Revoke(ctx, user, lib), "revoking an absent grant must not error")

		has, err = store.HasAccess(ctx, user, lib)
		require.NoError(t, err)
		assert.False(t, has)
		_ = owner
	})
}

// TestLibraryAccess_RegrantRefreshesGrantedAt is the ON CONFLICT DO UPDATE
// behaviour. DO NOTHING would keep the old date, and "since when has this user
// had access" would then answer with a date from before a revocation.
func TestLibraryAccess_RegrantRefreshesGrantedAt(t *testing.T) {
	store := sqlite.NewLibraryAccessStore()

	runWithRollbackTxn(t, "regrant refreshes the date", func(t *testing.T, ctx context.Context) {
		_, lib := sfLibrary(t, ctx, "sfAccessRegrant")
		user := int64(mustCreateUser(ctx, t, "sfAccessUserB"))

		require.NoError(t, exec(t, ctx,
			"INSERT INTO user_library_access (user_id, library_id, granted_at) VALUES (?, ?, '2001-01-01 00:00:00')",
			user, lib))
		before := scalar(t, ctx, "SELECT granted_at FROM user_library_access WHERE user_id = ? AND library_id = ?", user, lib)

		require.NoError(t, store.Grant(ctx, user, lib))
		after := scalar(t, ctx, "SELECT granted_at FROM user_library_access WHERE user_id = ? AND library_id = ?", user, lib)

		assert.NotEqual(t, before, after, "a re-grant must refresh granted_at: an old date misreports how long access has been held")
	})
}

// TestLibraryAccess_DecideAppliesModeAndGrantTogether is §6.4 end to end
// against a real grant row. The plan's TestAccess_UngrantedUserGets404Not403 is
// a unit test of the rule; this proves the store supplies the grant answer that
// the rule consumes.
func TestLibraryAccess_DecideAppliesModeAndGrantTogether(t *testing.T) {
	store := sqlite.NewLibraryAccessStore()

	runWithRollbackTxn(t, "decide", func(t *testing.T, ctx context.Context) {
		_, lib := sfLibrary(t, ctx, "sfAccessDecide")
		granted := int64(mustCreateUser(ctx, t, "sfAccessGrantee"))
		ungranted := int64(mustCreateUser(ctx, t, "sfAccessDenied"))

		require.NoError(t, store.Grant(ctx, granted, lib))

		// Public + grant: allowed.
		require.NoError(t, store.Decide(ctx, collab.ModePublic, granted, lib))
		// Public, no grant: the single not-found refusal.
		err := store.Decide(ctx, collab.ModePublic, ungranted, lib)
		require.ErrorIs(t, err, collab.ErrNoLibraryAccess)
		// Contribute, WITH a grant: still refused. §6.4's default.
		require.ErrorIs(t, store.Decide(ctx, collab.ModeContribute, granted, lib), collab.ErrNoLibraryAccess)
		// Private: refused for everyone.
		require.ErrorIs(t, store.Decide(ctx, collab.ModePrivate, granted, lib), collab.ErrNoLibraryAccess)
		// And the refusals are indistinguishable.
		assert.Equal(t,
			store.Decide(ctx, collab.ModePublic, ungranted, lib).Error(),
			store.Decide(ctx, collab.ModeContribute, granted, lib).Error(),
			"a caller must not be able to tell 'no grant' from 'wrong mode'")
	})
}

// TestLibraryAccess_UsersWithAccessIsSortedAndComplete.
func TestLibraryAccess_UsersWithAccessIsSorted(t *testing.T) {
	store := sqlite.NewLibraryAccessStore()

	runWithRollbackTxn(t, "sorted grantees", func(t *testing.T, ctx context.Context) {
		_, lib := sfLibrary(t, ctx, "sfAccessList")
		// Deliberately created out of order, and each its own row.
		var want []int64
		for _, n := range []string{"sfAccessZed", "sfAccessAda", "sfAccessMid"} {
			u := int64(mustCreateUser(ctx, t, n))
			require.NoError(t, store.Grant(ctx, u, lib))
			want = append(want, u)
		}

		got, err := store.UsersWithAccess(ctx, lib)
		require.NoError(t, err)
		assert.Equal(t, collab.SortUserIDs(want), got, "the owner's view of their library must be sorted and stable")

		libs, err := store.LibrariesForUser(ctx, want[0])
		require.NoError(t, err)
		assert.Equal(t, []int64{lib}, libs)
	})
}

// TestLibraryAccess_GrantCascadesWhenTheUserIsDeleted: a grant for a user who no
// longer exists is removed, so the grantee list never returns a dangling id.
func TestLibraryAccess_GrantCascadesWhenTheUserIsDeleted(t *testing.T) {
	store := sqlite.NewLibraryAccessStore()

	runWithRollbackTxn(t, "cascade on user delete", func(t *testing.T, ctx context.Context) {
		_, lib := sfLibrary(t, ctx, "sfAccessCascade")
		user := int64(mustCreateUser(ctx, t, "sfAccessDoomed"))
		require.NoError(t, store.Grant(ctx, user, lib))
		require.Equal(t, int64(1), count(t, ctx, "SELECT count(*) FROM user_library_access WHERE library_id = ?", lib))

		require.NoError(t, exec(t, ctx, "DELETE FROM users WHERE id = ?", user))
		assert.Equal(t, int64(0), count(t, ctx, "SELECT count(*) FROM user_library_access WHERE library_id = ?", lib),
			"a grant for a deleted user must be removed by the CASCADE")

		got, err := store.UsersWithAccess(ctx, lib)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

// TestLibraryAccess_GrantCascadesWhenTheLibraryIsDeleted.
func TestLibraryAccess_GrantCascadesWhenTheLibraryIsDeleted(t *testing.T) {
	store := sqlite.NewLibraryAccessStore()

	runWithRollbackTxn(t, "cascade on library delete", func(t *testing.T, ctx context.Context) {
		_, lib := sfLibrary(t, ctx, "sfAccessDoomedLib")
		user := int64(mustCreateUser(ctx, t, "sfAccessSurvivor"))
		require.NoError(t, store.Grant(ctx, user, lib))

		require.NoError(t, exec(t, ctx, "DELETE FROM libraries WHERE id = ?", lib))
		assert.Equal(t, int64(0), count(t, ctx, "SELECT count(*) FROM user_library_access WHERE library_id = ?", lib))
	})
}

// TestLibraryAccess_GrantToAnotherUsersLibraryIsStoredNotDenied: the schema has
// no constraint that a grant must come from the library's owner, which is
// correct -- the owner is the only one who can CALL Grant, and a database
// constraint cannot know who the session belongs to. So the test pins that the
// store does not invent an ownership check that would break the legitimate
// case, and notes where the real check lives.
func TestLibraryAccess_GrantIsNotOwnerRestrictedAtTheSchema(t *testing.T) {
	store := sqlite.NewLibraryAccessStore()

	runWithRollbackTxn(t, "grant is stored", func(t *testing.T, ctx context.Context) {
		_, lib := sfLibrary(t, ctx, "sfAccessOwnerCheck")
		user := int64(mustCreateUser(ctx, t, "sfAccessGrantee2"))
		require.NoError(t, store.Grant(ctx, user, lib))
		// The OWNERSHIP check is the resolver's job, not the store's: the store
		// has no session and therefore no notion of who is asking. What matters
		// here is that a grant round-trips.
		has, err := store.HasAccess(ctx, user, lib)
		require.NoError(t, err)
		assert.True(t, has)
	})
}
