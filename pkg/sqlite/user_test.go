//go:build integration
// +build integration

// UserStore integration tests.
//
// These run against the real migrated database via the shared harness, inside
// withRollbackTxn so each test has private rows. The behaviours worth testing
// here are the ones the application layer depends on but cannot enforce by
// itself: case-insensitive lookup, the owner's uniqueness, and the fact that a
// disabled user keeps their row.

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/auth"
	"github.com/stashapp/stash/pkg/models"
)

// createUser hashes a password and inserts the account, returning the created
// user. Hashing here rather than in the store is the point: the store has no
// code path that accepts a plaintext password.
func createUser(t *testing.T, ctx context.Context, name string, isOwner bool) *models.User {
	t.Helper()

	hash, err := auth.Hash("test-password")
	require.NoError(t, err)

	u := &models.User{Username: name, IsOwner: isOwner}
	require.NoError(t, db.User.Create(ctx, u, []byte(hash)))
	return u
}

func TestUserStore_CreateAndFind(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		u := createUser(t, ctx, "sfCreate", false)

		assert.NotZero(t, u.ID, "Create must populate the new object's id")
		assert.NotZero(t, u.CreatedAt, "Create must populate created_at from the DB")

		found, err := db.User.Find(ctx, u.ID)
		require.NoError(t, err)
		require.NotNil(t, found)
		assert.Equal(t, "sfCreate", found.Username)
		assert.False(t, found.IsOwner)
	})
}

// The COLLATE NOCASE index is what makes a case-insensitive login lookup
// unambiguous. Without it "Alice" and "alice" are two accounts and neither
// lookup can say which one it meant.
func TestUserStore_FindByUsernameIsCaseInsensitive(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		createUser(t, ctx, "sfAlice", false)

		for _, variant := range []string{"sfAlice", "SFALICE", "sfalice", "SfAlIcE"} {
			found, err := db.User.FindByUsername(ctx, variant)
			require.NoError(t, err, "variant %q", variant)
			require.NotNil(t, found, "variant %q must resolve to the account", variant)
			assert.Equal(t, "sfAlice", found.Username,
				"a case-insensitive lookup must return the stored spelling")
		}
	})
}

func TestUserStore_FindByUsernameMissingReturnsNilNotError(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		// Absence is the normal case on a login form, not a fault. An error
		// here would make the caller treat "no such user" as a server fault
		// and, worse, tempt it into distinguishing miss from wrong-password.
		found, err := db.User.FindByUsername(ctx, "nobody-here")
		require.NoError(t, err)
		assert.Nil(t, found)
	})
}

func TestUserStore_CreateRejectsCaseVariantUsername(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		createUser(t, ctx, "sfTaken", false)

		hash, err := auth.Hash("another")
		require.NoError(t, err)

		dup := &models.User{Username: "SFTAKEN"}
		err = db.User.Create(ctx, dup, []byte(hash))
		assert.ErrorIs(t, err, models.ErrUsernameTaken,
			"a case-variant duplicate must surface as ErrUsernameTaken, not a "+
				"raw driver error the API layer would have to string-match")
	})
}

func TestUserStore_ExactlyOneOwner(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		createUser(t, ctx, "sfOwner1", true)

		hash, err := auth.Hash("x")
		require.NoError(t, err)

		second := &models.User{Username: "sfOwner2", IsOwner: true}
		assert.Error(t, db.User.Create(ctx, second, []byte(hash)),
			"the partial unique index must refuse a second owner")

		owners, err := db.User.CountOwners(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, owners)
	})
}

// Non-owners must be freely creatable -- if the owner index were written as a
// plain unique index on is_owner, every non-owner would collide with every other
// one and the instance would hold exactly two users forever.
func TestUserStore_ManyNonOwners(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		for _, n := range []string{"sfN1", "sfN2", "sfN3", "sfN4", "sfN5"} {
			createUser(t, ctx, n, false)
		}

		all, err := db.User.FindAll(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(all), 5)

		n, err := db.User.Count(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, n, 5)
	})
}

func TestUserStore_PasswordHashRoundTrips(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		u := createUser(t, ctx, "sfHash", false)

		hash, err := db.User.FindPasswordHash(ctx, u.ID)
		require.NoError(t, err)
		require.NotEmpty(t, hash)

		assert.NoError(t, auth.Verify("test-password", string(hash)),
			"the stored value must still verify against the original password")

		assert.NotContains(t, string(hash), "test-password",
			"the stored value must be a hash, not the password")
	})
}

func TestUserStore_SetPasswordHash(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		u := createUser(t, ctx, "sfRotate", false)

		replacement, err := auth.Hash("new-password")
		require.NoError(t, err)
		require.NoError(t, db.User.SetPasswordHash(ctx, u.ID, []byte(replacement)))

		hash, err := db.User.FindPasswordHash(ctx, u.ID)
		require.NoError(t, err)
		assert.NoError(t, auth.Verify("new-password", string(hash)))
		assert.ErrorIs(t, auth.Verify("test-password", string(hash)),
			auth.ErrPasswordMismatch,
			"the old password must stop working after a rotation")
	})
}

// Update must not be able to reach the password hash or the owner flag, even if
// a caller hands it a struct claiming those values. Both are guarded by the
// column list in the UPDATE rather than by trusting the caller's struct.
func TestUserStore_UpdateCannotEscalateToOwner(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		u := createUser(t, ctx, "sfEscalate", false)

		u.IsOwner = true
		require.NoError(t, db.User.Update(ctx, u))

		reloaded, err := db.User.Find(ctx, u.ID)
		require.NoError(t, err)
		require.NotNil(t, reloaded)
		assert.False(t, reloaded.IsOwner,
			"Update must not be able to grant the owner role; that is the one "+
				"role the spec says must never be ambiguous")
	})
}

func TestUserStore_SetDisabledKeepsTheRow(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		u := createUser(t, ctx, "sfDisable", false)

		require.NoError(t, db.User.SetDisabled(ctx, u.ID, true))

		reloaded, err := db.User.Find(ctx, u.ID)
		require.NoError(t, err)
		require.NotNil(t, reloaded, "a disabled user must keep their row so audit history survives")
		assert.False(t, reloaded.Active(), "a disabled user must not be Active")
		assert.NotNil(t, reloaded.DisabledAt)

		// ...and re-enabling works, without leaving a second row.
		require.NoError(t, db.User.SetDisabled(ctx, u.ID, false))
		reloaded, err = db.User.Find(ctx, u.ID)
		require.NoError(t, err)
		assert.True(t, reloaded.Active())
	})
}

func TestUserStore_AddReputation(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		u := createUser(t, ctx, "sfRep", false)

		require.NoError(t, db.User.AddReputation(ctx, u.ID, 10))
		require.NoError(t, db.User.AddReputation(ctx, u.ID, -3))

		reloaded, err := db.User.Find(ctx, u.ID)
		require.NoError(t, err)
		assert.Equal(t, 7, reloaded.Reputation,
			"reputation must accumulate as a delta, and a negative delta must apply")
	})
}

func TestUserStore_FindAllOrderedByUsername(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		createUser(t, ctx, "sfZed", false)
		createUser(t, ctx, "sfAlpha", false)
		createUser(t, ctx, "sfMid", false)

		all, err := db.User.FindAll(ctx)
		require.NoError(t, err)

		// Isolate just our rows: the shared fixture DB has other users.
		var mine []string
		for _, u := range all {
			switch u.Username {
			case "sfZed", "sfAlpha", "sfMid":
				mine = append(mine, u.Username)
			}
		}
		assert.Equal(t, []string{"sfAlpha", "sfMid", "sfZed"}, mine,
			"FindAll must be ordered by username so a user list renders stably")
	})
}
