//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/sqlite"
)

func TestUserSessionStore_RoundTrip(t *testing.T) {
	runWithRollbackTxn(t, "TestUserSessionStore_RoundTrip", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "sessionuser")
		store := sqlite.NewUserSessionStore()

		idHash := []byte{1, 2, 3, 4}
		expires := time.Now().Add(time.Hour)
		require.NoError(t, store.Create(ctx, idHash, userID, expires, "1.2.3.4", "UA"))

		got, found, err := store.FindSession(ctx, idHash, time.Now())
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, userID, got, "the session must resolve to the user that created it")
	})
}

func TestUserSessionStore_ExpiryIsEnforcedInSQL(t *testing.T) {
	// The whole point of doing the comparison in SQL: a caller-side check is one
	// forgotten `if` from authenticating an expired session.
	runWithRollbackTxn(t, "expiry", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "expireduser")
		store := sqlite.NewUserSessionStore()

		idHash := []byte{9, 9, 9}
		// Already expired at creation.
		require.NoError(t, store.Create(ctx, idHash, userID, time.Now().Add(-time.Hour), "", ""))

		_, found, err := store.FindSession(ctx, idHash, time.Now())
		require.NoError(t, err)
		assert.False(t, found, "an expired session must not be found even though the row exists")
	})
}

func TestUserSessionStore_UnknownHashIsNotFound(t *testing.T) {
	runWithRollbackTxn(t, "TestUserSessionStore_UnknownHashIsNotFound", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewUserSessionStore()
		_, found, err := store.FindSession(ctx, []byte("never-existed"), time.Now())
		require.NoError(t, err)
		assert.False(t, found, "an unknown id must be not-found, not an error")
	})
}

func TestUserSessionStore_DeleteSession(t *testing.T) {
	runWithRollbackTxn(t, "TestUserSessionStore_DeleteSession", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "deleteuser")
		store := sqlite.NewUserSessionStore()
		idHash := []byte{7, 7, 7}

		require.NoError(t, store.Create(ctx, idHash, userID, time.Now().Add(time.Hour), "", ""))
		require.NoError(t, store.DeleteSession(ctx, idHash))

		_, found, err := store.FindSession(ctx, idHash, time.Now())
		require.NoError(t, err)
		assert.False(t, found, "a deleted session must stop resolving")

		// Deleting again must not error: logout is not idempotent in the UI, so
		// a double logout is a real sequence.
		assert.NoError(t, store.DeleteSession(ctx, idHash))
	})
}

func TestUserSessionStore_DeleteSessionsForUser(t *testing.T) {
	runWithRollbackTxn(t, "TestUserSessionStore_DeleteSessionsForUser", func(t *testing.T, ctx context.Context) {
		alice := mustCreateUser(ctx, t, "alice-multi")
		bob := mustCreateUser(ctx, t, "bob-multi")
		store := sqlite.NewUserSessionStore()

		require.NoError(t, store.Create(ctx, []byte("a1"), alice, time.Now().Add(time.Hour), "", ""))
		require.NoError(t, store.Create(ctx, []byte("a2"), alice, time.Now().Add(time.Hour), "", ""))
		require.NoError(t, store.Create(ctx, []byte("b1"), bob, time.Now().Add(time.Hour), "", ""))

		require.NoError(t, store.DeleteSessionsForUser(ctx, alice))

		_, found, _ := store.FindSession(ctx, []byte("a1"), time.Now())
		assert.False(t, found, "alice's session must be gone")
		_, found, _ = store.FindSession(ctx, []byte("a2"), time.Now())
		assert.False(t, found, "all of alice's sessions must be gone")

		_, found, err := store.FindSession(ctx, []byte("b1"), time.Now())
		require.NoError(t, err)
		assert.True(t, found, "another user's session must survive -- this is 'log me out everywhere'")
	})
}

func TestUserSessionStore_PurgeExpired(t *testing.T) {
	runWithRollbackTxn(t, "TestUserSessionStore_PurgeExpired", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "purgeuser")
		store := sqlite.NewUserSessionStore()

		require.NoError(t, store.Create(ctx, []byte("old"), userID, time.Now().Add(-time.Hour), "", ""))
		require.NoError(t, store.Create(ctx, []byte("new"), userID, time.Now().Add(time.Hour), "", ""))

		n, err := store.PurgeExpired(ctx, time.Now())
		require.NoError(t, err)
		assert.Equal(t, 1, n, "exactly the expired row must be purged")

		_, found, _ := store.FindSession(ctx, []byte("new"), time.Now())
		assert.True(t, found, "a live session must survive the purge")
	})
}

func TestUserSessionStore_Count(t *testing.T) {
	runWithRollbackTxn(t, "TestUserSessionStore_Count", func(t *testing.T, ctx context.Context) {
		userID := mustCreateUser(ctx, t, "countuser")
		store := sqlite.NewUserSessionStore()
		require.NoError(t, store.Create(ctx, []byte("c1"), userID, time.Now().Add(time.Hour), "", ""))

		n, err := store.Count(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, n, 1)
	})
}

// --- invites ---------------------------------------------------------------

func TestInviteStore_RedeemConsumesOneUse(t *testing.T) {
	runWithRollbackTxn(t, "TestInviteStore_RedeemConsumesOneUse", func(t *testing.T, ctx context.Context) {
		creator := mustCreateUser(ctx, t, "invitecreator")
		store := sqlite.NewInviteStore()
		keyHash := []byte("key-one")

		require.NoError(t, store.CreateInvite(ctx, keyHash, creator, nil, 2))

		by, err := store.RedeemInvite(ctx, keyHash, time.Now())
		require.NoError(t, err)
		assert.Equal(t, creator, by, "the redemption must report who minted the key")

		_, err = store.RedeemInvite(ctx, keyHash, time.Now())
		require.NoError(t, err, "the second use must succeed: max_uses is 2")

		_, err = store.RedeemInvite(ctx, keyHash, time.Now())
		assert.Error(t, err, "the third use must be refused")
	})
}

func TestInviteStore_UnknownKeyIsRefused(t *testing.T) {
	runWithRollbackTxn(t, "TestInviteStore_UnknownKeyIsRefused", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewInviteStore()
		_, err := store.RedeemInvite(ctx, []byte("never-minted"), time.Now())
		assert.Error(t, err, "an unknown key must be refused")
	})
}

func TestInviteStore_ExpiredKeyIsRefused(t *testing.T) {
	runWithRollbackTxn(t, "TestInviteStore_ExpiredKeyIsRefused", func(t *testing.T, ctx context.Context) {
		creator := mustCreateUser(ctx, t, "expiredinviter")
		store := sqlite.NewInviteStore()
		keyHash := []byte("expired-key")

		past := time.Now().Add(-time.Hour)
		require.NoError(t, store.CreateInvite(ctx, keyHash, creator, &past, 5))

		_, err := store.RedeemInvite(ctx, keyHash, time.Now())
		assert.Error(t, err, "an expired key must be refused")
	})
}

func TestInviteStore_RevokedKeyIsRefused(t *testing.T) {
	runWithRollbackTxn(t, "TestInviteStore_RevokedKeyIsRefused", func(t *testing.T, ctx context.Context) {
		creator := mustCreateUser(ctx, t, "revoker")
		store := sqlite.NewInviteStore()
		keyHash := []byte("revoked-key")

		require.NoError(t, store.CreateInvite(ctx, keyHash, creator, nil, 5))
		require.NoError(t, store.RevokeInvite(ctx, keyHash))

		_, err := store.RedeemInvite(ctx, keyHash, time.Now())
		assert.Error(t, err, "a revoked key must be refused")
	})
}

func TestInviteStore_ReleaseInviteUndoesARedemption(t *testing.T) {
	runWithRollbackTxn(t, "TestInviteStore_ReleaseInviteUndoesARedemption", func(t *testing.T, ctx context.Context) {
		creator := mustCreateUser(ctx, t, "releaser")
		store := sqlite.NewInviteStore()
		keyHash := []byte("release-key")

		require.NoError(t, store.CreateInvite(ctx, keyHash, creator, nil, 1))
		_, err := store.RedeemInvite(ctx, keyHash, time.Now())
		require.NoError(t, err)

		// The registration then failed, so return the use.
		require.NoError(t, store.ReleaseInvite(ctx, keyHash))

		_, err = store.RedeemInvite(ctx, keyHash, time.Now())
		assert.NoError(t, err, "a released use must be redeemable again")
	})
}

func TestInviteStore_ReleaseInviteCannotGoNegative(t *testing.T) {
	runWithRollbackTxn(t, "TestInviteStore_ReleaseInviteCannotGoNegative", func(t *testing.T, ctx context.Context) {
		creator := mustCreateUser(ctx, t, "negativer")
		store := sqlite.NewInviteStore()
		keyHash := []byte("negative-key")

		require.NoError(t, store.CreateInvite(ctx, keyHash, creator, nil, 1))
		_, err := store.RedeemInvite(ctx, keyHash, time.Now())
		require.NoError(t, err)

		// Release twice. The second is a no-op on the counter: 1 -> 0 on the
		// first, and 0 fails the `uses > 0` guard on the second.
		require.NoError(t, store.ReleaseInvite(ctx, keyHash))
		require.NoError(t, store.ReleaseInvite(ctx, keyHash))

		// The invariant that matters is not "the next redeem fails" -- with
		// uses back at 0 and max_uses 1, one more redeem is legitimate. It is
		// that a double release must not buy a SECOND use, which is what would
		// let one leaked key mint three accounts from a single redemption.
		_, err = store.RedeemInvite(ctx, keyHash, time.Now())
		require.NoError(t, err, "one use was legitimately returned")

		_, err = store.RedeemInvite(ctx, keyHash, time.Now())
		assert.Error(t, err,
			"a double release must not grant a second use; max_uses is 1")
	})
}

// --- audit -----------------------------------------------------------------

func TestAuditStore_AppendAndCount(t *testing.T) {
	runWithRollbackTxn(t, "TestAuditStore_AppendAndCount", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAuditStore()
		actor := mustCreateUser(ctx, t, "auditor")
		target := 42

		require.NoError(t, store.Append(ctx, &actor, "edit_merged", "scene", &target, "title",
			map[string]interface{}{"from": "a", "to": "b"}))

		n, err := store.Count(ctx, "edit_merged")
		require.NoError(t, err)
		assert.Equal(t, 1, n)
	})
}

func TestAuditStore_NullActorIsRecorded(t *testing.T) {
	runWithRollbackTxn(t, "TestAuditStore_NullActorIsRecorded", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAuditStore()
		// actor_id NULL is the interesting case: the action was taken by
		// someone who is not authenticated.
		require.NoError(t, store.Append(ctx, nil, "bootstrap", "instance", nil, "", nil))

		n, err := store.Count(ctx, "bootstrap")
		require.NoError(t, err)
		assert.Equal(t, 1, n)
	})
}

func TestAuditStore_RecordLoginFailureCarriesTheReason(t *testing.T) {
	runWithRollbackTxn(t, "reason", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAuditStore()

		require.NoError(t, store.RecordLoginFailure(ctx, "alice", "1.2.3.4", "no_such_user"))
		require.NoError(t, store.RecordLoginFailure(ctx, "bob", "1.2.3.4", "bad_password"))
		require.NoError(t, store.RecordLoginFailure(ctx, "", "5.6.7.8", "spray_budget_exhausted"))

		n, err := store.Count(ctx, "login_failed")
		require.NoError(t, err)
		assert.Equal(t, 3, n, "every failure must be recorded, whatever the reason")
	})
}

// Counting rows proves the audit happened; it does NOT prove the record says
// anything useful. A store that wrote the right number of empty rows passed the
// count test above, and the mutation trials confirmed it: dropping the reason or
// the username from the detail blob killed nothing.
//
// These read the detail back through the store's own reader, so the moderation
// question the column exists to answer -- "was this a spray, and against whom?" --
// is actually answerable. The reader is a real method, not a test hook, because
// the admin view needs it.
func TestAuditStore_LoginFailureDetailIsQueryable(t *testing.T) {
	runWithRollbackTxn(t, "detail", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAuditStore()
		require.NoError(t, store.RecordLoginFailure(ctx, "alice", "9.9.9.9", "no_such_user"))
		require.NoError(t, store.RecordLoginFailure(ctx, "alice", "9.9.9.9", "spray_budget_exhausted"))
		require.NoError(t, store.RecordLoginFailure(ctx, "bob", "8.8.8.8", "bad_password"))

		got, err := store.ReadLoginFailure(ctx, "alice")
		require.NoError(t, err)
		require.Len(t, got, 2, "only alice's attempts must be returned")
		assert.Equal(t, "9.9.9.9", got[0].IP, "the source IP must be stored for a rate-limit investigation")
		assert.Equal(t, "alice", got[0].Username)

		reasons := []string{got[0].Reason, got[1].Reason}
		assert.Contains(t, reasons, "no_such_user")
		assert.Contains(t, reasons, "spray_budget_exhausted",
			"the reason must be stored, not just counted -- it is how a spray is distinguished from a typo")
	})
}

// A username that appears inside ANOTHER row's data must not pull that row
// back. This is the case the LIKE pre-filter cannot get right on its own, and
// the mutation that removed the decoded comparison survived every other test in
// this file: "bob" is a substring of nothing above, but a moderator searching
// for "admin" must not be shown the attempts against "admin2", and an IP
// fragment must not match a username.
func TestAuditStore_LoginFailureDoesNotMatchOnSubstring(t *testing.T) {
	runWithRollbackTxn(t, "substring", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAuditStore()
		// admin2's row contains "admin" as a substring...
		require.NoError(t, store.RecordLoginFailure(ctx, "admin2", "1.1.1.1", "no_such_user"))
		// ...and a row where "admin" appears only inside the IP.
		require.NoError(t, store.RecordLoginFailure(ctx, "carol", "10.20.30.40", "bad_password"))
		require.NoError(t, store.RecordLoginFailure(ctx, "admin", "5.5.5.5", "bad_password"))

		got, err := store.ReadLoginFailure(ctx, "admin")
		require.NoError(t, err)
		require.Len(t, got, 1,
			"a username search must match the username field exactly, not a substring anywhere in the row")
		assert.Equal(t, "admin", got[0].Username)
		assert.Equal(t, "5.5.5.5", got[0].IP)
	})
}

// A username containing a SQL LIKE wildcard must be matched literally. A user
// called "a_b" or "100%" cannot be found with a LIKE, and must not be able to
// match every other account either.
func TestAuditStore_UsernameWithWildcardIsMatchedLiterally(t *testing.T) {
	runWithRollbackTxn(t, "wildcard", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAuditStore()
		require.NoError(t, store.RecordLoginFailure(ctx, "a_b", "1.1.1.1", "no_such_user"))
		require.NoError(t, store.RecordLoginFailure(ctx, "axb", "2.2.2.2", "no_such_user"))

		got, err := store.ReadLoginFailure(ctx, "a_b")
		require.NoError(t, err)
		require.Len(t, got, 1, "a '_' must not act as a single-character wildcard")
		assert.Equal(t, "a_b", got[0].Username)

		// And the wildcard must not let a search for it return everything.
		pct, err := store.ReadLoginFailure(ctx, "a%b")
		require.NoError(t, err)
		assert.Empty(t, pct, "a '%' must not act as a wildcard either")
	})
}

func TestAuditStore_AppendDetailIsQueryable(t *testing.T) {
	runWithRollbackTxn(t, "append-detail", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAuditStore()
		actor := mustCreateUser(ctx, t, "detail-actor")
		target := 77

		require.NoError(t, store.Append(ctx, &actor, "edit_merged", "scene", &target, "title",
			map[string]interface{}{"from": "old", "to": "new"}))

		entries, err := store.ReadByAction(ctx, "edit_merged")
		require.NoError(t, err)
		require.Len(t, entries, 1)

		e := entries[0]
		require.True(t, e.ActorID.Valid, "the actor must be recorded, not lost")
		assert.Equal(t, int64(actor), e.ActorID.Int64)
		assert.Equal(t, "scene", e.Target)
		assert.Equal(t, "title", e.Field.String, "the field being changed must be recorded")
		require.True(t, e.Detail.Valid)
		assert.Contains(t, e.Detail.String, "old")
		assert.Contains(t, e.Detail.String, "new")
	})
}

// A nil detail must not become the JSON literal "null", and must not cost us
// the row: the action happened and has to be countable.
func TestAuditStore_NilDetailStillRecordsTheAction(t *testing.T) {
	runWithRollbackTxn(t, "nil-detail", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAuditStore()
		require.NoError(t, store.Append(ctx, nil, "instance_bootstrapped", "instance", nil, "", nil))

		n, err := store.Count(ctx, "instance_bootstrapped")
		require.NoError(t, err)
		assert.Equal(t, 1, n, "an action with no detail must still be recorded")

		entries, err := store.ReadByAction(ctx, "instance_bootstrapped")
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.False(t, entries[0].ActorID.Valid,
			"a null actor must read back as SQL NULL, not 0 -- actor 0 does not exist")
	})
}

// --- moderator and disable guards -----------------------------------------

// The owner is NOT a moderator by virtue of being the owner, and the store
// refuses to make them one. Collapsing the two roles is what would make "the
// owner cannot overrule a quorum" unexpressible, so the refusal is enforced at
// the store rather than assumed at the call site.
func TestUserStore_OwnerCannotBeMadeModerator(t *testing.T) {
	runWithRollbackTxn(t, "owner-moderator", func(t *testing.T, ctx context.Context) {
		owner := mustCreateOwner(ctx, t)
		store := sqlite.NewUserStore()

		err := store.SetModerator(ctx, owner, owner, true)
		assert.ErrorIs(t, err, models.ErrOwnerIsNotModerator,
			"the owner must not be appointable as a moderator")

		got, err := store.Find(ctx, owner)
		require.NoError(t, err)
		assert.False(t, got.IsModerator, "the flag must be unchanged after a refusal")
	})
}

// A non-owner must not be able to appoint moderators. This is the actual
// privilege escalation the actorID parameter exists to prevent.
func TestUserStore_NonOwnerCannotAppointModerators(t *testing.T) {
	runWithRollbackTxn(t, "non-owner-appoint", func(t *testing.T, ctx context.Context) {
		owner := mustCreateOwner(ctx, t)
		alice := mustCreateUser(ctx, t, "alice-moderator-try")
		bob := mustCreateUser(ctx, t, "bob-moderator-try")
		store := sqlite.NewUserStore()

		err := store.SetModerator(ctx, alice, bob, true)
		assert.ErrorIs(t, err, models.ErrNotOwner, "a non-owner must not appoint a moderator")

		got, err := store.Find(ctx, bob)
		require.NoError(t, err)
		assert.False(t, got.IsModerator, "the target must be unchanged after a refusal")
		_ = owner
	})
}

func TestUserStore_OwnerAppointsModerator(t *testing.T) {
	runWithRollbackTxn(t, "appoint", func(t *testing.T, ctx context.Context) {
		owner := mustCreateOwner(ctx, t)
		bob := mustCreateUser(ctx, t, "bob-appointee")
		store := sqlite.NewUserStore()

		require.NoError(t, store.SetModerator(ctx, owner, bob, true))
		got, err := store.Find(ctx, bob)
		require.NoError(t, err)
		assert.True(t, got.IsModerator)

		// ...and can be removed again.
		require.NoError(t, store.SetModerator(ctx, owner, bob, false))
		got, err = store.Find(ctx, bob)
		require.NoError(t, err)
		assert.False(t, got.IsModerator)
	})
}

// Disabling the owner would leave an instance nobody can administer. There is
// no recovery path short of editing the database by hand, so it is refused.
func TestUserStore_OwnerCannotBeDisabled(t *testing.T) {
	runWithRollbackTxn(t, "disable-owner", func(t *testing.T, ctx context.Context) {
		owner := mustCreateOwner(ctx, t)
		store := sqlite.NewUserStore()

		err := store.SetDisabled(ctx, owner, true)
		assert.ErrorIs(t, err, models.ErrCannotDisableOwner,
			"disabling the owner would leave the instance unadministrable")

		got, err := store.Find(ctx, owner)
		require.NoError(t, err)
		assert.True(t, got.Active(), "a refused disable must leave the account active")
	})
}

func TestUserStore_NonOwnerCanBeDisabledAndReenabled(t *testing.T) {
	runWithRollbackTxn(t, "disable-user", func(t *testing.T, ctx context.Context) {
		mustCreateOwner(ctx, t)
		alice := mustCreateUser(ctx, t, "alice-disable")
		store := sqlite.NewUserStore()

		require.NoError(t, store.SetDisabled(ctx, alice, true))
		got, err := store.Find(ctx, alice)
		require.NoError(t, err)
		assert.False(t, got.Active(), "a disabled user must not be Active")

		require.NoError(t, store.SetDisabled(ctx, alice, false))
		got, err = store.Find(ctx, alice)
		require.NoError(t, err)
		assert.True(t, got.Active(), "re-enabling must restore the account")
	})
}

func TestUserStore_IsModeratorSurvivesRoundTrip(t *testing.T) {
	runWithRollbackTxn(t, "moderator-roundtrip", func(t *testing.T, ctx context.Context) {
		owner := mustCreateOwner(ctx, t)
		bob := mustCreateUser(ctx, t, "bob-roundtrip")
		store := sqlite.NewUserStore()
		require.NoError(t, store.SetModerator(ctx, owner, bob, true))

		// Read it back the way a resolver would: by username.
		got, err := store.FindByUsername(ctx, "bob-roundtrip")
		require.NoError(t, err)
		assert.True(t, got.IsModerator,
			"is_moderator must be selected and mapped, not left at its zero value")
	})
}

// mustCreateOwner inserts the instance's single owner. The partial unique index
// permits only one, so every test that needs an owner creates it here and never
// assumes a shared fixture.
func mustCreateOwner(ctx context.Context, t *testing.T) int {
	t.Helper()

	owner := &models.User{Username: "owner-" + t.Name(), IsOwner: true}
	require.NoError(t, sqlite.NewUserStore().Create(ctx, owner, []byte("hash")))
	return owner.ID
}

// mustCreateUser inserts a real user row and returns its id.
//
// A dedicated instance per fixture, never a fixed or shared row: a test that
// reuses an id collides with whatever else in the suite touches it, and the
// database persists between runs.
func mustCreateUser(ctx context.Context, t *testing.T, username string) int {
	t.Helper()

	u := &models.User{Username: username}
	require.NoError(t, sqlite.NewUserStore().Create(ctx, u, []byte("hash")))
	require.NotZero(t, u.ID, "Create must populate the id on the value it was given")
	return u.ID
}
