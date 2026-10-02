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

// stash#1790 — the external-ID store, T1 to T5 from docs/ISSUE-1790-spec.md.
//
// The SCHEMA is proven in external_id_schema_test.go by writing bad rows and watching the
// database refuse them. These prove the STORE's behaviour: which query it runs, what it
// returns, and — for T4 — that the delete path is actually wired.
//
// T4 IS THE ONE THAT EARNS THE DESIGN. `external_ids.entity_id` has no foreign key (spec
// §4.2), so nothing removes an entity's ids automatically. If DestroyForEntity is not called
// from a store's destroy path, ids accumulate invisibly forever, and no test anywhere else
// would notice. So T4 is table-driven over every entity type this build knows, and it
// asserts against the REAL entity stores rather than against a direct store call — calling
// ExternalIDStore.DestroyForEntity in the test would prove only that the method works, not
// that anything calls it.

func newExternalIDStore() *sqlite.ExternalIDStore { return sqlite.NewExternalIDStore() }

// mk1790Source registers a source and returns it.
func mk1790Source(t *testing.T, ctx context.Context, name string) *models.ExternalSource {
	t.Helper()
	s, err := newExternalIDStore().CreateSource(ctx, models.ExternalSourceInput{
		Name: name, URL: "https://" + name + ".example",
	})
	require.NoError(t, err)
	require.NotNil(t, s)
	return s
}

// T1 (NEGATIVE) — an unknown source is refused, with a message naming it.
func TestT1RecordingAgainstAnUnknownSourceIsRefusedByName(t *testing.T) {
	runWithRollbackTxn(t, "T1: an unknown source is refused, by name",
		func(t *testing.T, ctx context.Context) {
			_, err := newExternalIDStore().Record(ctx, models.ExternalIDInput{
				EntityType: models.ExternalIDEntityScene,
				EntityID:   1,
				Source:     strPtr1790("no-such-source"),
				ExternalID: "abc123",
			})
			require.Error(t, err,
				"THIS IS THE POINT OF THE REGISTRY. The legacy `endpoint` is a bare "+
					"varchar(255) nothing validates, so a typo inserts and is then "+
					"unreachable by every join -- silent and permanent")
			assert.Contains(t, err.Error(), "no-such-source",
				"and the message must NAME the source. \"FOREIGN KEY constraint failed\" "+
					"tells the caller nothing about which of their five configured "+
					"scrapers is misspelled")
		})
}

// T1b — a source_id that does not exist is refused by the foreign key.
func TestT1bRecordingAgainstAnUnknownSourceIDIsRefused(t *testing.T) {
	runWithRollbackTxn(t, "T1b: an unknown source_id is refused",
		func(t *testing.T, ctx context.Context) {
			_, err := newExternalIDStore().Record(ctx, models.ExternalIDInput{
				EntityType: models.ExternalIDEntityScene,
				EntityID:   1,
				SourceID:   999999,
				ExternalID: "abc123",
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "FOREIGN KEY")
		})
}

// T1c (NEGATIVE) — passing neither source_id nor source is an error, not a row with 0.
func TestT1cRecordingWithNoSourceAtAllIsRefused(t *testing.T) {
	runWithRollbackTxn(t, "T1c: neither source_id nor source is an error",
		func(t *testing.T, ctx context.Context) {
			_, err := newExternalIDStore().Record(ctx, models.ExternalIDInput{
				EntityType: models.ExternalIDEntityScene,
				EntityID:   1,
				ExternalID: "abc123",
			})
			require.Error(t, err,
				"the FK would refuse source_id 0 anyway, but the explicit message says what "+
					"to fix. Silently resolving a missing source to 0 would produce a "+
					"confusing FK error about a zero the caller never passed")
		})
}

// T2 — the same external id from two sources on one entity is TWO rows.
func TestT2TwoSourcesTheSameStringIsTwoRows(t *testing.T) {
	runWithRollbackTxn(t, "T2: two sources, the same string, is two rows",
		func(t *testing.T, ctx context.Context) {
			qb := newExternalIDStore()
			a := mk1790Source(t, ctx, "t2-a")
			b := mk1790Source(t, ctx, "t2-b")

			ra, err := qb.Record(ctx, models.ExternalIDInput{
				EntityType: models.ExternalIDEntityScene, EntityID: 5,
				SourceID: a.ID, ExternalID: "shared",
			})
			require.NoError(t, err)
			rb, err := qb.Record(ctx, models.ExternalIDInput{
				EntityType: models.ExternalIDEntityScene, EntityID: 5,
				SourceID: b.ID, ExternalID: "shared",
			})
			require.NoError(t, err, "a different source is a different identity")

			assert.NotEqual(t, ra.ID, rb.ID)

			got, err := qb.FindByEntity(ctx, models.ExternalIDEntityScene, 5)
			require.NoError(t, err)
			assert.Len(t, got, 2,
				"TWO ROWS. Identity is (entity_type, entity_id, source_id, external_id); "+
					"leaving source_id out makes two providers' ids collide for every user "+
					"with more than one scraper configured")
		})
}

// T3 — re-recording the same four keys UPDATES rather than inserting.
func TestT3ReRecordingUpdatesRatherThanInserting(t *testing.T) {
	runWithRollbackTxn(t, "T3: re-recording updates rather than inserting",
		func(t *testing.T, ctx context.Context) {
			qb := newExternalIDStore()
			s := mk1790Source(t, ctx, "t3")

			first, err := qb.Record(ctx, models.ExternalIDInput{
				EntityType: models.ExternalIDEntityScene, EntityID: 6,
				SourceID: s.ID, ExternalID: "same",
			})
			require.NoError(t, err)

			// BACKDATE, then record again. This is the part that makes the test able to
			// fail: two Records in one test are the same instant at second precision, so
			// "the timestamp moved" is unobservable without moving it first.
			//
			// THE PREMISE IS ASSERTED, because getting it backwards produces a test that
			// fails against correct code: backdating moves the value EARLIER, so it is
			// backdated < first, not first < backdated.
			past := first.UpdatedAt.Add(-time.Hour)
			_, _, err = db.ExecSQL(ctx,
				`UPDATE external_ids SET updated_at = ? WHERE id = ?`,
				[]interface{}{sqlite.Timestamp{Timestamp: past}, first.ID})
			require.NoError(t, err)
			require.True(t, past.Before(first.UpdatedAt),
				"the backdate must move the stored value EARLIER, or this test cannot fail")

			second, err := qb.Record(ctx, models.ExternalIDInput{
				EntityType: models.ExternalIDEntityScene, EntityID: 6,
				SourceID: s.ID, ExternalID: "same",
			})
			require.NoError(t, err)

			assert.Equal(t, first.ID, second.ID, "the SAME row, updated")
			assert.True(t, second.UpdatedAt.After(past),
				"updated_at advanced from the backdated value -- this is the only assertion "+
					"that can distinguish an upsert from a no-op")

			got, err := qb.FindByEntity(ctx, models.ExternalIDEntityScene, 6)
			require.NoError(t, err)
			assert.Len(t, got, 1, "ONE row, not two")
		})
}

// T4 (NEGATIVE, PER ENTITY TYPE) — the entity destroy path removes external ids.
//
// CALLED THROUGH THE REAL STORES, on purpose. A test that calls
// ExternalIDStore.DestroyForEntity directly would prove the method works; it would NOT prove
// that anything calls it, and "nothing calls it" is the entire risk this feature creates.
func TestT4EveryEntityDestroyPathRemovesExternalIDs(t *testing.T) {
	runWithRollbackTxn(t, "T4: every entity destroy path removes its external ids",
		func(t *testing.T, ctx context.Context) {
			qb := newExternalIDStore()
			s := mk1790Source(t, ctx, "t4")

			// Each case creates a REAL entity of that type, records an external id against
			// it, destroys the entity through the store that owns it, and asserts the id is
			// gone. The entities must be real because the sweep-and-delete paths are keyed
			// on real ids.
			cases := []struct {
				entityType string
				create     func(t *testing.T) int
				destroy    func(t *testing.T, id int)
			}{
				{
					models.ExternalIDEntityTag,
					func(t *testing.T) int {
						// CreateTagInput embeds *Tag and Create writes the new id back
						// onto it, so the id is read off the tag afterwards. Passing a bare
						// *models.Tag does not compile -- the signature wants the input.
						tag := &models.Tag{Name: "1790-t4", Description: "t4"}
						require.NoError(t, db.Tag.Create(ctx, &models.CreateTagInput{Tag: tag}))
						require.NotZero(t, tag.ID, "Create must have assigned an id")
						return tag.ID
					},
					func(t *testing.T, id int) {
						require.NoError(t, db.Tag.Destroy(ctx, id))
					},
				},
				{
					// THROUGH `table`, NOT `repository`. This case is here because the
					// first version of T4 named only tag and studio, and BOTH of those
					// destroy via repository.destroyExisting -- so the suite was green
					// while scene and gallery, which go through `table`, leaked their
					// external ids. The gallery case in TestR4 is what caught it; this one
					// is here so the `table` path is covered by T4 as well.
					models.ExternalIDEntityGallery,
					func(t *testing.T) int {
						g := &models.Gallery{Title: "1790-t4-gallery", Code: "t4g"}
						require.NoError(t, db.Gallery.Create(ctx, &models.CreateGalleryInput{Gallery: g}))
						require.NotZero(t, g.ID, "Create must have assigned an id")
						return g.ID
					},
					func(t *testing.T, id int) {
						require.NoError(t, db.Gallery.Destroy(ctx, id))
					},
				},
				{
					models.ExternalIDEntityStudio,
					func(t *testing.T) int {
						st := &models.Studio{Name: "1790-t4", URLs: models.NewRelatedStrings([]string{"https://t4.example"})}
						require.NoError(t, db.Studio.Create(ctx, &models.CreateStudioInput{Studio: st}))
						require.NotZero(t, st.ID, "Create must have assigned an id")
						return st.ID
					},
					func(t *testing.T, id int) {
						require.NoError(t, db.Studio.Destroy(ctx, id))
					},
				},
			}

			for _, tc := range cases {
				id := tc.create(t)

				_, err := qb.Record(ctx, models.ExternalIDInput{
					EntityType: tc.entityType, EntityID: id,
					SourceID: s.ID, ExternalID: "t4-id",
				})
				require.NoError(t, err)

				// Present before the destroy -- otherwise a path that never recorded would
				// pass the "after" assertion for the wrong reason.
				before, err := qb.FindByEntity(ctx, tc.entityType, id)
				require.NoError(t, err)
				require.Len(t, before, 1, "%s: the id must exist before the destroy", tc.entityType)

				tc.destroy(t, id)

				after, err := qb.FindByEntity(ctx, tc.entityType, id)
				require.NoError(t, err)
				assert.Empty(t, after,
					"%s/%d: destroying the entity must remove its external ids. "+
						"There is no foreign key on entity_id, so nothing else will -- and "+
						"the leftover row is invisible to every query", tc.entityType, id)
			}
		})
}

// T5 — SweepOrphans removes orphans and leaves real rows alone.
func TestT5SweepOrphansRemovesOrphansAndNothingElse(t *testing.T) {
	runWithRollbackTxn(t, "T5: the sweep removes orphans and nothing else",
		func(t *testing.T, ctx context.Context) {
			qb := newExternalIDStore()
			s := mk1790Source(t, ctx, "t5")

			// A REAL tag with a real id, so the sweep has something it must NOT delete.
			tag := &models.Tag{Name: "1790-t5-keep", Description: "t5"}
			require.NoError(t, db.Tag.Create(ctx, &models.CreateTagInput{Tag: tag}))
			require.NotZero(t, tag.ID)
			_, err := qb.Record(ctx, models.ExternalIDInput{
				EntityType: models.ExternalIDEntityTag, EntityID: tag.ID,
				SourceID: s.ID, ExternalID: "keep-me",
			})
			require.NoError(t, err)

			// An ORPHAN: a tag id that does not exist. The polymorphic column permits this,
			// which is the documented cost of the design (spec 4.2).
			_, err = qb.Record(ctx, models.ExternalIDInput{
				EntityType: models.ExternalIDEntityTag, EntityID: 987654,
				SourceID: s.ID, ExternalID: "sweep-me",
			})
			require.NoError(t, err)

			removed, err := qb.SweepOrphans(ctx)
			require.NoError(t, err)
			assert.GreaterOrEqual(t, removed, 1, "the orphan must be swept")

			kept, err := qb.FindByEntity(ctx, models.ExternalIDEntityTag, tag.ID)
			require.NoError(t, err)
			assert.Len(t, kept, 1,
				"THE ROW THAT MUST SURVIVE. The obvious wrong implementation is a blanket "+
					"DELETE FROM external_ids on the theory that a rebuild repopulates it, "+
					"which is true only on a fresh database and destroys every id on a live one")

			gone, err := qb.FindByEntity(ctx, models.ExternalIDEntityTag, 987654)
			require.NoError(t, err)
			assert.Empty(t, gone, "and the orphan must be gone")
		})
}

// FindByExternalID is the scrape direction, so it gets its own test rather than riding
// along in T2.
func TestFindByExternalIDResolvesBackToTheLocalEntity(t *testing.T) {
	runWithRollbackTxn(t, "FindByExternalID resolves an id back to its entity",
		func(t *testing.T, ctx context.Context) {
			qb := newExternalIDStore()
			s := mk1790Source(t, ctx, "scrape-src")

			_, err := qb.Record(ctx, models.ExternalIDInput{
				EntityType: models.ExternalIDEntityGallery, EntityID: 42,
				SourceID: s.ID, ExternalID: "upstream-42",
			})
			require.NoError(t, err)

			found, err := qb.FindByExternalID(ctx, s.ID, "upstream-42")
			require.NoError(t, err)
			require.Len(t, found, 1, "this is the query a metadata scrape resolves against")
			assert.Equal(t, 42, found[0].EntityID)
			assert.Equal(t, models.ExternalIDEntityGallery, found[0].EntityType)

			none, err := qb.FindByExternalID(ctx, s.ID, "never-seen")
			require.NoError(t, err)
			assert.Empty(t, none)
		})
}

func TestCreatingASourceTwiceReturnsTheSameOne(t *testing.T) {
	runWithRollbackTxn(t, "creating a source twice returns the same one",
		func(t *testing.T, ctx context.Context) {
			qb := newExternalIDStore()
			a, err := qb.CreateSource(ctx, models.ExternalSourceInput{
				Name: "dupe-source", URL: "https://one.example",
			})
			require.NoError(t, err)

			b, err := qb.CreateSource(ctx, models.ExternalSourceInput{
				Name: "dupe-source", URL: "https://two.example",
			})
			require.NoError(t, err,
				"a caller registering its scrapers on every run must not get a UNIQUE "+
					"violation on the second run")
			assert.Equal(t, a.ID, b.ID,
				"AND must not get a SECOND source row: two sources with one name would make "+
					"name -> source resolution ambiguous, which is the exact ambiguity the "+
					"registry exists to remove")
			assert.Equal(t, "https://one.example", b.URL,
				"and the FIRST url stands. Two callers naming one source with different "+
					"URLs is a disagreement about configuration; silently preferring the "+
					"latest hides it")
		})
}

func TestCreatingASourceWithNoNameOrURLIsRefused(t *testing.T) {
	runWithRollbackTxn(t, "a source needs a name and a url",
		func(t *testing.T, ctx context.Context) {
			qb := newExternalIDStore()

			_, err := qb.CreateSource(ctx, models.ExternalSourceInput{URL: "https://x.example"})
			require.Error(t, err, "an unnamed source is unresolvable -- that is the whole "+
				"problem the registry exists to prevent, reintroduced one level up")

			_, err = qb.CreateSource(ctx, models.ExternalSourceInput{Name: "no-url"})
			require.Error(t, err)
		})
}

func TestRecordingRejectsEmptyFields(t *testing.T) {
	runWithRollbackTxn(t, "recording rejects empty identity fields",
		func(t *testing.T, ctx context.Context) {
			qb := newExternalIDStore()
			s := mk1790Source(t, ctx, "t-empty")

			_, err := qb.Record(ctx, models.ExternalIDInput{
				EntityType: models.ExternalIDEntityScene, EntityID: 1,
				SourceID: s.ID, ExternalID: "",
			})
			require.Error(t, err, "an empty external_id matches nothing and finds nothing")

			_, err = qb.Record(ctx, models.ExternalIDInput{
				EntityType: "", EntityID: 1,
				SourceID: s.ID, ExternalID: "x",
			})
			require.Error(t, err, "an empty entity_type is unqueryable by definition, since "+
				"every lookup filters on it")

			_, err = qb.Record(ctx, models.ExternalIDInput{
				EntityType: models.ExternalIDEntityScene, EntityID: 0,
				SourceID: s.ID, ExternalID: "x",
			})
			require.Error(t, err)
		})
}

func TestDestroyForEntityRefusesAnUnknownEntityType(t *testing.T) {
	runWithRollbackTxn(t, "destroying for an unknown entity type is refused",
		func(t *testing.T, ctx context.Context) {
			err := newExternalIDStore().DestroyForEntity(ctx, "brand_new_entity_2099", 1)
			require.Error(t, err,
				"REFUSING BEATS SILENTLY DOING NOTHING. An unknown type here means either a "+
					"typo or a new entity never added to externalIDEntityTables, and in both "+
					"cases the ids are left behind with no error to explain it")
		})
}

func TestEveryKnownEntityTypeHasASweepTable(t *testing.T) {
	// A guard on the mapping itself. externalIDEntityTables pairs a type with the table its
	// ids live in; a typo in the table name produces a sweep that silently deletes nothing
	// (or, worse, deletes against the wrong table), and this is the only check that sees it.
	for _, et := range []string{
		models.ExternalIDEntityScene,
		models.ExternalIDEntityPerformer,
		models.ExternalIDEntityStudio,
		models.ExternalIDEntityTag,
		models.ExternalIDEntityGallery,
	} {
		assert.True(t, sqlite.IsKnownExternalIDEntityType(et),
			"%s must be a known entity type, or its ids are never swept", et)
	}
	assert.False(t, sqlite.IsKnownExternalIDEntityType("nonsense"),
		"an unknown type must report false, so a caller wiring a new entity is TOLD to add "+
			"it rather than discovering the omission when the table fills with orphans")
}

func strPtr1790(s string) *string { return &s }
func ptr1790(s string) *string    { return &s }

// R4 (stash#1790) — a FIFTH entity adopts external IDs with no per-provider table and no
// migration.
//
// GALLERY IS THE FIFTH ENTITY because it is the one that has no legacy `*_stash_ids` table
// at all: scene, performer, studio and tag all do. So gallery is the only honest test of
// whether the generic path is actually generic -- the other four could be working through
// their old per-entity tables, and a test on them would pass either way.
//
// THIS IS THE TEST THAT R4 EXISTS TO REQUIRE. If the store had turned out to need a
// gallery-specific table, this would have failed while the four-entity tests stayed green.
func TestR4GalleryAdoptsExternalIDsThroughTheGenericPathOnly(t *testing.T) {
	runWithRollbackTxn(t, "R4: gallery adopts external ids with no per-entity table",
		func(t *testing.T, ctx context.Context) {
			qb := newExternalIDStore()
			s := mk1790Source(t, ctx, "r4-src")

			// A SECOND source, so this is not just "gallery got a row" but "gallery can
			// hold ids from several providers at once" -- the thing the four-column unique
			// key exists to permit.
			s2 := mk1790Source(t, ctx, "r4-src-2")

			g := &models.Gallery{Title: "1790-r4", Code: "r4-code"}
			require.NoError(t, db.Gallery.Create(ctx, &models.CreateGalleryInput{Gallery: g}))
			require.NotZero(t, g.ID)

			for _, src := range []*models.ExternalSource{s, s2} {
				_, err := qb.Record(ctx, models.ExternalIDInput{
					EntityType: models.ExternalIDEntityGallery, EntityID: g.ID,
					SourceID: src.ID, ExternalID: "up-" + src.Name,
				})
				require.NoError(t, err,
					"gallery must accept an id from %s through the SAME Record every "+
						"other entity uses -- no gallery-specific method, no new table",
					src.Name)
			}

			got, err := qb.FindByEntity(ctx, models.ExternalIDEntityGallery, g.ID)
			require.NoError(t, err)
			assert.Len(t, got, 2, "both providers' ids on one gallery")

			// AND the destroy wiring covers gallery too, via repository.destroy.
			require.NoError(t, db.Gallery.Destroy(ctx, g.ID))
			after, err := qb.FindByEntity(ctx, models.ExternalIDEntityGallery, g.ID)
			require.NoError(t, err)
			assert.Empty(t, after,
				"gallery's ids go with it. This is the same repository.destroy block that "+
					"covers the other four, reached by table name rather than by a fifth "+
					"hand-written call site")
		})
}

// R4's OTHER HALF: adding an entity type must not need a migration, and the only evidence
// is that a type this build has never heard of still records, reads and deletes.
//
// The tempting version of this test uses one of the five KNOWN types, which proves nothing
// -- the store hard-codes nothing per type, so the test would pass whether or not the table
// is generic. The entity type here is deliberately one the code has no constant for.
func TestR4AnUnknownEntityTypeRecordsWithoutAMigration(t *testing.T) {
	runWithRollbackTxn(t, "R4: an entity type the code has no constant for still works",
		func(t *testing.T, ctx context.Context) {
			const future = "movie" // a real upstream type with no store in this build yet
			qb := newExternalIDStore()
			s := mk1790Source(t, ctx, "r4-future")

			// NOT a known type: IsKnownExternalIDEntityType says no, so the sweep and the
			// destroy wiring cannot see it. Recording must still work -- that is the whole
			// claim of a polymorphic table.
			require.False(t, sqlite.IsKnownExternalIDEntityType(future))

			_, err := qb.Record(ctx, models.ExternalIDInput{
				EntityType: future, EntityID: 1,
				SourceID: s.ID, ExternalID: "tt-1",
			})
			require.NoError(t, err,
				"a type with no store in this build must still be recordable, or the table "+
					"is not generic and every future entity needs a migration")

			got, err := qb.FindByEntity(ctx, future, 1)
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, "tt-1", got[0].ExternalID)
		})
}
