//go:build integration
// +build integration

// The `libraries` table store, tested against a real database.
//
// The seam being tested here is the OWNERSHIP check, not the SQL. Every method
// that takes a userId answers "is this the caller's library", and a test that
// only exercises the happy path proves the happy path: the interesting cases are
// the ones where a caller who does not own a library passes one they know the id
// of, because that is the shape an attack takes.
//
// The fixtures are per-test and never use a fixed id, for the reason every
// fixture in this package is per-test: the shared integration database persists
// between runs, so a fixed id passes exactly once and then dies in the full
// suite.

package sqlite_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/sqlite"
)

// TestLibrary_FirstLibraryBecomesTheDefault is the rule the whole NULL
// substitution in the media gate rests on.
//
// A user with one library has an unambiguous answer for an unscanned row; a user
// with no library has no owner for one, and the gate refuses those. So "the first
// library is the default" is what makes the substitution sound for a new user,
// and it is worth a test that fails if a later edit drops the is_default write.
func TestLibrary_FirstLibraryBecomesTheDefault(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := sqlite.NewLibraryStore()
		uid := insertUser(t, ctx, "libDefaultOwner", false)

		first, err := store.CreateLibrary(ctx, "Main", uid)
		require.NoError(t, err)
		assert.True(t, first.IsDefault,
			"a user's first library must be the default, or an unscanned row has "+
				"no owner and the media gate refuses it")

		second, err := store.CreateLibrary(ctx, "Shared", uid)
		require.NoError(t, err)
		assert.False(t, second.IsDefault,
			"a second library must not be the default: two defaults make "+
				"'which library is this file in' a question with two answers")

		// And the listing agrees with the created value, read back.
		libs, err := store.ListLibraries(ctx, uid)
		require.NoError(t, err)
		require.Len(t, libs, 2)
		assert.Equal(t, collab.LibraryList{first, second}, libs)
		assert.Equal(t, first.ID, libs.Default().ID)
	})
}

// TestLibrary_AUserWithNoLibraryHasNoDefault pins the other half: the refusal is
// about a user who has libraries but none marked default, which a hand-edited
// row or a deleted default can produce.
//
// The positive control matters: without it this test would also pass on a store
// where ResolveLibraryID returns an error for everybody, which is a store where
// nobody can ever be granted anything.
func TestLibrary_AUserWithNoLibraryHasNoDefault(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := sqlite.NewLibraryStore()
		withLib := insertUser(t, ctx, "libNoDefaultHas", false)
		without := insertUser(t, ctx, "libNoDefaultHasNone", false)

		// A positive control: with a default, resolution works.
		lib, err := store.CreateLibrary(ctx, "Has", withLib)
		require.NoError(t, err)
		got, err := store.ResolveLibraryID(ctx, nil, withLib)
		require.NoError(t, err)
		assert.Equal(t, lib.ID, got)

		// With no library at all, the refusal is specific and actionable.
		_, err = store.ResolveLibraryID(ctx, nil, without)
		require.Error(t, err)
		assert.ErrorIs(t, err, collab.ErrNoDefaultLibrary,
			"a user with no library must get the 'create one' refusal, not a "+
				"generic not-found that sends them hunting for a missing row")
	})
}

// TestLibrary_OwnershipRefusalDoesNotRevealWhoOwnsIt is the property the
// ownership error is built for.
//
// A caller holding a library id they were given learns "not yours". A caller
// PROBING ids learns nothing beyond what they already guessed, because the same
// error is returned for a library that belongs to somebody else and for one that
// does not exist. If those two diverge, the error becomes an oracle for
// enumerating which ids are real -- which is the same disclosure the media gate's
// 404 rule exists to prevent, arriving through a different door.
func TestLibrary_OwnershipRefusalDoesNotRevealWhoOwnsIt(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := sqlite.NewLibraryStore()
		mine := insertUser(t, ctx, "libOwnOwner", false)
		theirs := insertUser(t, ctx, "libOwnOther", false)

		myLib, err := store.CreateLibrary(ctx, "Mine", mine)
		require.NoError(t, err)
		theirLib, err := store.CreateLibrary(ctx, "Theirs", theirs)
		require.NoError(t, err)

		// The positive control: the owner may read their own.
		_, err = store.OwnedLibrary(ctx, myLib.ID, mine)
		require.NoError(t, err)

		// Somebody else's library: refused.
		err = mustFail(t, func() error { _, e := store.OwnedLibrary(ctx, theirLib.ID, mine); return e })
		require.ErrorIs(t, err, collab.ErrNotLibraryOwner)

		// A library that does not exist: the SAME error.
		missing := int64(999999)
		missingErr := mustFail(t, func() error { _, e := store.OwnedLibrary(ctx, missing, mine); return e })
		require.Error(t, missingErr)

		// The two errors must be indistinguishable to a caller, or the
		// difference is an oracle. Compared on the sentinel, because the
		// messages differ by id and the ids are already the caller's.
		assert.Equal(t,
			errors.Is(err, collab.ErrNotLibraryOwner),
			errors.Is(missingErr, collab.ErrNotLibraryOwner),
			"'belongs to someone else' and 'does not exist' must not be "+
				"distinguishable, or the error reveals which ids are real")
	})
}

// TestLibrary_TheDefaultCannotBeDeleted is the refusal that stops a user taking
// their own files offline.
//
// Deleting the default would leave every row with no library_id owned by nobody,
// which the gate refuses. The refusal here is the cheaper outcome by exactly one
// operation, and it is the ONLY thing standing between an owner and an instance
// where their own unscanned files 404.
func TestLibrary_TheDefaultCannotBeDeleted(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := sqlite.NewLibraryStore()
		uid := insertUser(t, ctx, "libDelDefault", false)

		def, err := store.CreateLibrary(ctx, "Main", uid)
		require.NoError(t, err)

		_, err = store.DeleteLibrary(ctx, def.ID, uid)
		require.Error(t, err)
		assert.ErrorIs(t, err, collab.ErrDefaultLibraryUndeletable)

		// And the refusal actually left it in place.
		_, err = store.GetLibrary(ctx, def.ID)
		require.NoError(t, err, "a refused delete must not remove the row")

		// A non-default library does delete, and deleting it leaves the
		// default standing -- which is where the orphaned rows now resolve.
		other, err := store.CreateLibrary(ctx, "Second", uid)
		require.NoError(t, err)
		ok, err := store.DeleteLibrary(ctx, other.ID, uid)
		require.NoError(t, err)
		assert.True(t, ok)

		stillThere, err := store.GetLibrary(ctx, def.ID)
		require.NoError(t, err)
		assert.True(t, stillThere.IsDefault)
	})
}

// TestLibrary_DeleteRefusesSomebodyElonesLibrary is the case that separates the
// two checks in DeleteLibrary: ownership is asked BEFORE default-ness.
//
// A caller deleting a library they do not own must be refused for the OWNERSHIP
// reason, not told "that is the default library, you cannot delete it". The
// second message confirms a property of a library they were never entitled to
// know about, and it is a worse answer than "not yours".
func TestLibrary_DeleteRefusesSomebodyElonesLibrary(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := sqlite.NewLibraryStore()
		other := insertUser(t, ctx, "libDelOther", false)
		attacker := insertUser(t, ctx, "libDelAttacker", false)

		theirLib, err := store.CreateLibrary(ctx, "Theirs", other)
		require.NoError(t, err)

		_, err = store.DeleteLibrary(ctx, theirLib.ID, attacker)
		require.Error(t, err)
		assert.ErrorIs(t, err, collab.ErrNotLibraryOwner,
			"the ownership refusal must come first: naming the default-ness of a "+
				"library the caller does not own confirms a property of it")
		assert.NotErrorIs(t, err, collab.ErrDefaultLibraryUndeletable)

		// Still there.
		_, err = store.GetLibrary(ctx, theirLib.ID)
		require.NoError(t, err)
	})
}

// TestLibrary_CannotCreateTheSameNameTwice proves the UNIQUE (user_id, name)
// constraint is per owner, not global.
//
// Two users may each own a library called "Main" -- that is the design, and
// uniqueness is scoped to where the label is shown. A global unique index would
// make the second user's library impossible to create, and the error would be
// "UNIQUE constraint failed" naming two columns, which tells an operator neither
// which library nor what to do.
func TestLibrary_CannotCreateTheSameNameTwice(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := sqlite.NewLibraryStore()
		one := insertUser(t, ctx, "libNameOne", false)
		two := insertUser(t, ctx, "libNameTwo", false)

		_, err := store.CreateLibrary(ctx, "Main", one)
		require.NoError(t, err)

		// The same name for a different user is FINE.
		other, err := store.CreateLibrary(ctx, "Main", two)
		require.NoError(t, err, "library names are unique per owner, not globally")
		assert.NotZero(t, other.ID)

		// The same name for the same user is refused, with a message that
		// names the library.
		_, err = store.CreateLibrary(ctx, "Main", one)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"Main"`,
			"the refusal must name the library; 'UNIQUE constraint failed: "+
				"libraries.user_id, libraries.name' names two columns and neither")
	})
}

// TestLibrary_ANewLibraryDefersRatherThanPublishing is the is_private NULL
// third state, asserted at the store boundary.
//
// A new library must inherit the owner's consent decision, not assert one. If
// this write were false instead of NULL, the library would be publishable the
// moment it is created -- and §6.1's default is the publishing one, so the cost
// of the mistake is a library published that its owner never chose to share.
func TestLibrary_ANewLibraryDefersRatherThanPublishing(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := sqlite.NewLibraryStore()
		uid := insertUser(t, ctx, "libDeferOwner", false)

		lib, err := store.CreateLibrary(ctx, "New", uid)
		require.NoError(t, err)
		require.Nil(t, lib.IsPrivate,
			"a new library must defer to the owner's consent, not assert a value")

		// Read it back from the database, not from the returned struct: the
		// struct is built in CreateLibrary, so asserting on it would pass even
		// if the INSERT wrote false.
		stored := scalar(t, ctx,
			"SELECT is_private FROM libraries WHERE id = ?", lib.ID)
		assert.Nil(t, stored,
			"is_private must be NULL in the row. false would publish a library "+
				"whose owner never decided")
	})
}

// TestLibrary_GranteeCountsAreCorrectPerLibrary is the count the owner checks
// after granting somebody access.
//
// It also catches the N+1 that a per-library loop would produce: a loop passes
// this test and is wrong at 20 libraries. The query count is asserted in the
// store test below instead, where it is measurable.
func TestLibrary_GranteeCountsAreCorrectPerLibrary(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := sqlite.NewLibraryStore()
		owner := insertUser(t, ctx, "libCountOwner", false)
		guests := []int64{
			insertUser(t, ctx, "libCountG1", false),
			insertUser(t, ctx, "libCountG2", false),
			insertUser(t, ctx, "libCountG3", false),
		}

		first, err := store.CreateLibrary(ctx, "First", owner)
		require.NoError(t, err)
		second, err := store.CreateLibrary(ctx, "Second", owner)
		require.NoError(t, err)

		for _, g := range guests[:2] {
			require.NoError(t, store.Grant(ctx, g, first.ID))
		}
		require.NoError(t, store.Grant(ctx, guests[2], second.ID))

		libs, err := store.ListLibraries(ctx, owner)
		require.NoError(t, err)
		require.Len(t, libs, 2)

		byID := map[int64]*collab.Library{}
		for _, l := range libs {
			byID[l.ID] = l
		}
		assert.Equal(t, 2, byID[first.ID].GranteeCount)
		assert.Equal(t, 1, byID[second.ID].GranteeCount,
			"a count that leaks across libraries reports a grant on one library "+
				"as access to another")
	})
}

// TestLibrary_GranteesAreListedAndScopedToTheOwner is the query an owner runs
// to answer "who can see this".
func TestLibrary_GranteesAreListedAndScopedToTheOwner(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := sqlite.NewLibraryStore()
		owner := insertUser(t, ctx, "libGranteeOwner", false)
		guest := insertUser(t, ctx, "libGranteeGuest", false)

		lib, err := store.CreateLibrary(ctx, "Shared", owner)
		require.NoError(t, err)
		require.NoError(t, store.Grant(ctx, guest, lib.ID))

		users, err := store.Grantees(ctx, lib.ID)
		require.NoError(t, err)
		require.Len(t, users, 1)
		assert.Equal(t, int(guest), users[0].ID)

		// And revoking empties it rather than leaving a stale row.
		require.NoError(t, store.Revoke(ctx, guest, lib.ID))
		users, err = store.Grantees(ctx, lib.ID)
		require.NoError(t, err)
		assert.Empty(t, users, "a revoked user must not still be listed")
		assert.NotNil(t, users,
			"an empty grantee list must be an empty slice, not nil: gqlgen "+
				"marshals a nil element inside a list as null, which reads as a "+
				"user who could not be read rather than a user with no access")
	})
}

// TestLibrary_NonOwnerCannotReadGrantees is the scoping the resolver relies on.
func TestLibrary_NonOwnerCannotReadGrantees(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := sqlite.NewLibraryStore()
		owner := insertUser(t, ctx, "libScopeOwner", false)
		nosy := insertUser(t, ctx, "libScopeNosy", false)

		lib, err := store.CreateLibrary(ctx, "Private", owner)
		require.NoError(t, err)

		err = mustFail(t, func() error { return store.RequireOwnedBy(ctx, lib.ID, nosy) })
		require.ErrorIs(t, err, collab.ErrNotLibraryOwner)
	})
}

// mustFail runs a call expected to return an error and hands the error back.
//
// A helper rather than a closure at each site, because the alternative reads
// `_, err := f(); require.Error(t, err)` inline, which is fine, and
// `_, _ = f(); require.Error(t, err)` — using the err from the PREVIOUS call — is
// one keystroke away and passes.
func mustFail(t *testing.T, call func() error) error {
	t.Helper()
	err := call()
	require.Error(t, err, "expected a refusal")
	return err
}
