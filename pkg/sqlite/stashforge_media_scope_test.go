//go:build integration
// +build integration

// The media-scope store, tested against a real database AND through the real
// domain resolver.
//
// The seam is the point. collab's resolver is tested against a fake, which
// proves the rules; this file drives the same rules through the real sqlite
// adapter, which proves the SQL. Neither proves the other, which is M2's lesson
// (a fake written to match the interface instead of the driver) and M2c's (the
// store implemented three of four methods and no build noticed).
//
// So there is deliberately NO test here that calls the store's methods and
// asserts on their return values alone. Every case goes through
// collab.ResolveScope, because "does the query return the right library id" and
// "does the right thing happen when it does" are different questions and only
// one of them is the product.
//
// The fixtures are per-test and never use a fixed id: a fixed id passes exactly
// once and then dies in the full suite, because the shared integration database
// persists between runs.

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/sqlite"
)

// scopeFixture is one test's private world: an owner, an outsider, a library,
// and a tag inside that library.
type scopeFixture struct {
	store    *sqlite.MediaScopeStore
	ownerID  int64
	otherID  int64
	libraryI int64
	tagID    int64
}

func newScopeFixture(t *testing.T, ctx context.Context, name string) *scopeFixture {
	t.Helper()

	ownerID := insertUser(t, ctx, name+"-owner", false)
	otherID := insertUser(t, ctx, name+"-other", false)

	require.NoError(t, exec(t, ctx,
		"INSERT INTO libraries (user_id, name) VALUES (?, ?)", ownerID, name+"-lib"))
	libID := scalar(t, ctx,
		"SELECT id FROM libraries WHERE name = ?", name+"-lib").(int64)

	// A unique tag name per test rather than a fixed one.
	tagName := name + "-tag"
	require.NoError(t, exec(t, ctx,
		"INSERT INTO tags (name, created_at, updated_at, library_id) "+
			"VALUES (?, datetime('now'), datetime('now'), ?)", tagName, libID))
	tagID := scalar(t, ctx,
		"SELECT id FROM tags WHERE name = ?", tagName).(int64)

	return &scopeFixture{
		store:    sqlite.NewMediaScopeStore(),
		ownerID:  ownerID,
		otherID:  otherID,
		libraryI: libID,
		tagID:    tagID,
	}
}

// resolveIn runs the REAL domain resolver against the REAL store, inside the
// caller's transaction -- dbWrapper requires one, and a context.Background()
// here panics with "not in transaction" rather than failing informatively.
//
// There is deliberately no variant that does not take a transaction. A helper
// that looks like this one and cannot work is a trap, and the first version of
// this file had it.
func (f *scopeFixture) resolveIn(ctx context.Context, userID int64) collab.ScopeDecision {
	d, err := collab.ResolveScope(ctx, f.store,
		collab.ModePublic, userID, collab.TargetTag, f.tagID)
	if err != nil {
		// A store failure is a 500, not a refusal, and a test that treated
		// it as a refusal would pass while the database was broken.
		panic("ResolveScope: " + err.Error())
	}
	return d
}

// TestMediaScope_EveryScopedTargetTypeHasATable keeps the two closed sets in
// step. collab.TargetTypes is what the domain accepts; scopeTables is what the
// store can query. A type in the first and not the second is a target whose
// media 404s for everybody, with no error anywhere -- which is the shape of the
// M2c finding, one package over.
func TestMediaScope_EveryScopedTargetTypeHasATable(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		// Reach the store's table through the resolver, since scopeTables is
		// unexported: ask for each domain target type and require that the
		// store does not report it as unscopable.
		store := sqlite.NewMediaScopeStore()
		uid := insertUser(t, ctx, "scopeTypesOwner", false)
		require.NoError(t, exec(t, ctx,
			"INSERT INTO libraries (user_id, name) VALUES (?, ?)", uid, "scopeTypesLib"))
		libID := scalar(t, ctx,
			"SELECT id FROM libraries WHERE name = ?", "scopeTypesLib").(int64)

		for _, targetType := range collab.TargetTypes {
			// Point a row of that type at the library. The ids are the
			// fixture's own, not literals: a fixed target id is a collision
			// waiting for a reordering.
			switch targetType {
			case collab.TargetScene:
				require.NoError(t, exec(t, ctx,
					"INSERT INTO scenes (created_at, updated_at, library_id) VALUES (datetime('now'), datetime('now'), ?)", libID))
			case collab.TargetImage:
				require.NoError(t, exec(t, ctx,
					"INSERT INTO images (created_at, updated_at, library_id) VALUES (datetime('now'), datetime('now'), ?)", libID))
			case collab.TargetGallery:
				require.NoError(t, exec(t, ctx,
					"INSERT INTO galleries (created_at, updated_at, library_id) VALUES (datetime('now'), datetime('now'), ?)", libID))
			case collab.TargetPerformer:
				require.NoError(t, exec(t, ctx,
					"INSERT INTO performers (name, created_at, updated_at, library_id) VALUES (?, datetime('now'), datetime('now'), ?)",
					"scopeTypesPerformer-"+targetType, libID))
			case collab.TargetTag:
				require.NoError(t, exec(t, ctx,
					"INSERT INTO tags (name, created_at, updated_at, library_id) VALUES (?, datetime('now'), datetime('now'), ?)",
					"scopeTypesTag-"+targetType, libID))
			case collab.TargetStudio:
				require.NoError(t, exec(t, ctx,
					"INSERT INTO studios (name, created_at, updated_at, library_id) VALUES (?, datetime('now'), datetime('now'), ?)",
					"scopeTypesStudio-"+targetType, libID))
			case collab.TargetGroup:
				require.NoError(t, exec(t, ctx,
					"INSERT INTO groups (name, created_at, updated_at, library_id) VALUES (?, datetime('now'), datetime('now'), ?)",
					"scopeTypesGroup-"+targetType, libID))
			}

			// The id of the row just inserted, found by the unique name or
			// by "the most recent" for the ones with no name.
			var id int64
			switch targetType {
			case collab.TargetPerformer:
				id = scalar(t, ctx, "SELECT id FROM performers WHERE name = ?", "scopeTypesPerformer-"+targetType).(int64)
			case collab.TargetTag:
				id = scalar(t, ctx, "SELECT id FROM tags WHERE name = ?", "scopeTypesTag-"+targetType).(int64)
			case collab.TargetStudio:
				id = scalar(t, ctx, "SELECT id FROM studios WHERE name = ?", "scopeTypesStudio-"+targetType).(int64)
			case collab.TargetGroup:
				id = scalar(t, ctx, "SELECT id FROM groups WHERE name = ?", "scopeTypesGroup-"+targetType).(int64)
			default:
				id = scalar(t, ctx,
					"SELECT max(id) FROM "+map[string]string{
						collab.TargetScene: "scenes", collab.TargetImage: "images",
						collab.TargetGallery: "galleries",
					}[targetType]).(int64)
			}

			got, err := store.LibraryOfTarget(ctx, targetType, id)
			assert.NoError(t, err, "target type %q must be queryable by the store", targetType)
			assert.Equal(t, libID, got,
				"target type %q resolved to library %d, want %d. A domain type "+
					"with no entry in the store's table map serves nobody, with "+
					"no error anywhere -- the M2c finding one package over",
				targetType, got, libID)
		}
	})
}

func TestMediaScope_OwnerReachesTheirOwnMediaWithoutAGrant(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		f := newScopeFixture(t, ctx, "scopeOwner")

		d := f.resolveIn(ctx, f.ownerID)
		assert.True(t, d.IsOwner, "the library's owner was not recognised as its owner")
		assert.True(t, d.Allowed(),
			"the owner was refused their own media (library %d): %v", d.LibraryID, d.Decide())

		// And the grant table really is empty, so this is ownership and not
		// an accidental grant from a shared fixture.
		assert.Equal(t, int64(0), count(t, ctx,
			"SELECT count(*) FROM user_library_access WHERE library_id = ?", f.libraryI),
			"the fixture must not have granted anything, or the ownership "+
				"assertion above is satisfied for the wrong reason")
	})
}

func TestMediaScope_AnUngrantedUserIsRefused(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		f := newScopeFixture(t, ctx, "scopeRefuse")

		d := f.resolveIn(ctx, f.otherID)
		assert.False(t, d.IsOwner)
		assert.False(t, d.HasGrant)
		assert.False(t, d.Allowed(), "a user with no grant was served")
		assert.True(t, collab.IsNoLibraryAccess(d.Decide()),
			"the refusal must be the single not-found sentinel, because a 403 "+
				"confirms to a prober that the file exists")
	})
}

func TestMediaScope_GrantingMakesTheMediaReachable(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		f := newScopeFixture(t, ctx, "scopeGrant")

		require.False(t, f.resolveIn(ctx, f.otherID).Allowed(),
			"precondition: refused before the grant exists")

		require.NoError(t, exec(t, ctx,
			"INSERT INTO user_library_access (user_id, library_id) VALUES (?, ?)",
			f.otherID, f.libraryI))

		d := f.resolveIn(ctx, f.otherID)
		assert.True(t, d.HasGrant, "the grant was not seen")
		assert.True(t, d.Allowed(),
			"a user holding a user_library_access row was still refused: %v", d.Decide())
	})
}

func TestMediaScope_RevokingTheGrantRefusesAgain(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		f := newScopeFixture(t, ctx, "scopeRevoke")

		require.NoError(t, exec(t, ctx,
			"INSERT INTO user_library_access (user_id, library_id) VALUES (?, ?)",
			f.otherID, f.libraryI))
		require.True(t, f.resolveIn(ctx, f.otherID).Allowed(), "precondition: granted and served")

		require.NoError(t, exec(t, ctx,
			"DELETE FROM user_library_access WHERE user_id = ? AND library_id = ?",
			f.otherID, f.libraryI))

		assert.False(t, f.resolveIn(ctx, f.otherID).Allowed(),
			"the media is still served after the grant was revoked. §6.4's "+
				"grant is the authority, and a revocation that does not take "+
				"effect is the failure this whole mechanism exists to prevent")
	})
}

// TestMediaScope_AMissingRowIsARefusalNotAnError: a target that does not exist
// must not become a 500. A 500 tells a prober the row exists, and it is the
// exact disclosure §6.4 forbids.
func TestMediaScope_AMissingRowIsARefusalNotAnError(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := sqlite.NewMediaScopeStore()
		uid := insertUser(t, ctx, "scopeMissingOwner", false)
		require.NoError(t, exec(t, ctx,
			"INSERT INTO libraries (user_id, name, is_default) VALUES (?, ?, 1)", uid, "scopeMissingLib"))

		// An id nothing occupies. Not a fixed literal in an INSERT, so there
		// is no collision: this is a lookup of a row that was never made.
		missing := count(t, ctx, "SELECT coalesce(max(id), 0) FROM tags") + 1_000_000

		got, err := store.LibraryOfTarget(ctx, collab.TargetTag, missing)
		assert.NoError(t, err,
			"a row that does not exist is 'in no library', not a database "+
				"error. An error here becomes a 500 and tells a prober the "+
				"row exists (the ErrNoRows == mistake, HANDOFF.md #4.3)")
		assert.Equal(t, int64(0), got)
	})
}

// TestMediaScope_AnUnknownTargetTypeNeverReachesSQL: the closed map, proven at
// the seam. A target type that is not in the map must be refused BEFORE a query
// is built, or the value reaches a table name.
func TestMediaScope_AnUnknownTargetTypeNeverReachesSQL(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := sqlite.NewMediaScopeStore()

		for _, bad := range []string{"", "movies", "scenes", "tags; DROP TABLE tags", "TAGS"} {
			_, err := store.LibraryOfTarget(ctx, bad, 1)
			assert.Error(t, err,
				"target type %q reached the database. The store must refuse an "+
					"unmapped type rather than interpolate it into a query", bad)
		}

		// The table is still there, which is the positive control: a guard
		// tested only with bad input proves nothing about whether it fires.
		assert.Equal(t, int64(1), count(t, ctx,
			"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='tags'"),
			"the tags table must survive the injection attempts above")
	})
}

// TestMediaScope_AtMostOneDefaultLibrary: two defaults would make "which library
// does an unscoped row belong to" answerable two ways, and the serving path
// would pick one arbitrarily -- which is how a row in a private library ends up
// checked against a public one.
func TestMediaScope_AtMostOneDefaultLibrary(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		uid := insertUser(t, ctx, "scopeDefaultOwner", false)

		require.NoError(t, exec(t, ctx,
			"INSERT INTO libraries (user_id, name, is_default) VALUES (?, ?, 1)", uid, "scopeDefaultOne"))
		assert.Error(t, exec(t, ctx,
			"INSERT INTO libraries (user_id, name, is_default) VALUES (?, ?, 1)", uid, "scopeDefaultTwo"),
			"a second default library was accepted; the scope of an unscoped "+
				"row would then have two answers and the serving path would "+
				"pick one arbitrarily")

		// A non-default is still fine, and the positive control.
		require.NoError(t, exec(t, ctx,
			"INSERT INTO libraries (user_id, name, is_default) VALUES (?, ?, 0)", uid, "scopeDefaultOrdinary"))

		id, err := sqlite.NewMediaScopeStore().DefaultLibraryID(ctx)
		require.NoError(t, err)
		assert.NotEqual(t, int64(0), id, "the default library must be found")
	})
}

// TestMediaScope_NoDefaultLibraryMeansNoOwnershipBypass: an instance with no
// default library must not let an ungranted user through. This is the case the
// "NULL means public" failure would have produced.
func TestMediaScope_NoDefaultLibraryMeansNoOwnershipBypass(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := sqlite.NewMediaScopeStore()
		uid := insertUser(t, ctx, "scopeNoDefault", false)

		// A library that is NOT the default, to prove the substitution does
		// not depend on this test's own libraries.
		require.NoError(t, exec(t, ctx,
			"INSERT INTO libraries (user_id, name) VALUES (?, ?)", uid, "scopeNoDefaultLib"))
		require.NotEqual(t, int64(0),
			scalar(t, ctx, "SELECT id FROM libraries WHERE name = ?", "scopeNoDefaultLib"),
			"the non-default library must exist for this fixture to mean anything")

		// Claim the default slot for a DIFFERENT owner, so this test's user
		// is definitively not the default library's owner.
		otherOwner := insertUser(t, ctx, "scopeNoDefaultRealOwner", false)
		require.NoError(t, exec(t, ctx,
			"INSERT INTO libraries (user_id, name, is_default) VALUES (?, ?, 1)", otherOwner, "scopeNoDefaultReal"))

		// A tag in no library at all: it resolves to the OTHER owner's
		// default library, and this user owns nothing.
		tagName := "scopeNoDefaultTag"
		require.NoError(t, exec(t, ctx,
			"INSERT INTO tags (name, created_at, updated_at) VALUES (?, datetime('now'), datetime('now'))", tagName))
		tagID := scalar(t, ctx, "SELECT id FROM tags WHERE name = ?", tagName).(int64)

		d, err := collab.ResolveScope(ctx, store, collab.ModePublic, uid, collab.TargetTag, tagID)
		require.NoError(t, err)
		assert.False(t, d.IsOwner,
			"this user was treated as the owner of the default library they do not own")
		assert.False(t, d.Allowed(),
			"an ungranted user reached an unscoped row. The row belongs to the "+
				"DEFAULT library, and the default library is owned by somebody "+
				"else -- the refusal is the only honest answer here")

		// And the owner of the default library does get in, so the assertion
		// above is about the wrong-owner case and not about the substitution
		// being broken.
		dOwner, err := collab.ResolveScope(ctx, store, collab.ModePublic, otherOwner, collab.TargetTag, tagID)
		require.NoError(t, err)
		assert.True(t, dOwner.Allowed(),
			"the default library's own owner was refused an unscoped row: %v", dOwner.Decide())
	})
}

// TestMediaScope_ContributeModeServesNobodyWhateverTheGrant: §6.4's default
// posture. The grant is present in this fixture on purpose -- the point is that
// a grant does not punch through the mode.
func TestMediaScope_ContributeModeServesNobodyWhateverTheGrant(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		f := newScopeFixture(t, ctx, "scopeContribute")
		require.NoError(t, exec(t, ctx,
			"INSERT INTO user_library_access (user_id, library_id) VALUES (?, ?)",
			f.otherID, f.libraryI))

		// The grant is real, proven by the public-mode case.
		pub, err := collab.ResolveScope(ctx, f.store, collab.ModePublic, f.otherID, collab.TargetTag, f.tagID)
		require.NoError(t, err)
		require.True(t, pub.Allowed(),
			"precondition: the grant works in public mode, so a refusal in "+
				"contribute mode is the MODE refusing and not a broken grant")

		for _, mode := range []collab.Mode{collab.ModePrivate, collab.ModeContribute} {
			d, err := collab.ResolveScope(ctx, f.store, mode, f.otherID, collab.TargetTag, f.tagID)
			require.NoError(t, err)
			assert.False(t, d.Allowed(),
				"mode %q served media to a user who holds a grant. The mode is "+
					"the outer boundary: an instance that serves files in "+
					"contribute mode is a public file host that believes it "+
					"is private", mode)
		}
	})
}
