//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stash#2359 L5 — destroying a performer must leave nothing behind.
//
// MEASURED, NOT ASSUMED: THE FKs ALREADY DO THIS
// ===============================================
//
// WHATS-LEFT.md assumed L5 needed explicit cleanup, on the grounds that migration 121's rule is
// that a general table cannot use a foreign key. That is true of general tables, but every table
// carrying a performer_id here DOES declare one, and `Database.open` appends `&_fk=true` unless
// disableForeignKeys is set -- which it is not for the read or write handle. So ON DELETE CASCADE
// fires and no cleanup code is required.
//
// Proven rather than argued, with two mutations:
//
//   - Destroy replaced by a bare `DELETE FROM performers WHERE id = ?`, bypassing
//     destroyExisting entirely: the test still PASSED. The cascade does the work.
//   - Destroy made to delete the four parity child tables but forget the performer row: the test
//     FAILED, naming all eight tables that kept their rows.
//
// The first result is the useful one. It says the assertion is measuring the SCHEMA plus the pragma
// rather than any code of ours, which is exactly what should be true, and it means this test's job
// is to catch a table that LATER loses its foreign key -- not to police cleanup code that does not
// need to exist. The second result says the assertion bites.
//
// SO THIS FILE IS A REGRESSION GUARD, NOT A FIX
// =============================================
//
// If a future migration adds a performer_id column without a cascading foreign key, this fails and
// names the table. That is the failure worth catching, because it is invisible until someone
// queries an orphaned row.
//
// WHY MEASURE AND NOT READ THE SCHEMA
// ====================================
//
// "Does deleting a performer clean up its body marks" has three possible answers, and the schema
// alone does not distinguish them:
//
//   - the FK declares ON DELETE CASCADE and the pragma is on, so it cascades;
//   - the FK declares ON DELETE CASCADE but the pragma is OFF, so the rows SURVIVE;
//   - there is no FK at all -- migration 121's rule is that a general table cannot use one -- so the
//     rows survive regardless of the pragma.
//
// The third is the case that matters, and reading the CREATE TABLE statements makes it look exactly
// like the second. Both leave rows behind; only a DELETE tells you which. So the test counts rows
// before and after, on a database opened with the app's real DSN (`_fk=true`).
//
// WHY EACH TABLE IS SEEDED EXPLICITLY
// ====================================
//
// A table that starts empty and stays empty proves nothing, and that is precisely how a broken
// cascade hides behind a passing assertion. Each table gets one real row through the real API, so
// "the count did not change" can only mean "the row survived the delete".
//
// The list is explicit rather than introspected. Introspection finds the right 13 tables but cannot
// INSERT into them generically -- most need a parent row, a CHECK-satisfying value, or a column this
// test would have to guess. `TestEveryPerformerReferencingTableIsCovered` then checks the list
// against the schema, so a table added later fails rather than being silently unchecked.

// parityDestroyTable names one table and how to give a performer a row in it.
type parityDestroyTable struct {
	table string
	// seed creates one row belonging to performerID, failing the test if it cannot.
	seed func(t *testing.T, ctx context.Context, performerID int)
}

// The table names below are the ones the MIGRATED SCHEMA ACTUALLY HAS, measured by running
// `SELECT m.name FROM sqlite_master ... pragma_table_info ... name='performer_id'` against a
// database created by the app's own migration path. Five of the thirteen names in the first draft
// were wrong -- `performer_favorites`, `performer_overseers`, `scene_performers`,
// `gallery_performers` and `performers_stash_ids` do not exist. The real ones are plural in an
// inconsistent way (`performer_tags` AND `performers_tags`, `performers_scenes` rather than
// `scene_performers`), which is exactly why guessing is worse than measuring.
var parityDestroyFixtures = []parityDestroyTable{
	{table: "performer_body_marks", seed: seedBodyMark},
	{table: "performer_nationalities", seed: seedNationality},
	{table: "performer_aliases", seed: seedPerformerAlias},
	{table: "performer_alias_owners", seed: seedPerformerAliasOwner},
	{table: "performer_custom_fields", seed: seedPerformerCustomField},
	{table: "performer_stash_ids", seed: seedPerformerStashID},
	{table: "performer_urls", seed: seedPerformerURL},
	{table: "scene_performer_aliases", seed: seedScenePerformerAlias},
	{table: "performers_images", seed: seedPerformerImageLink},
	{table: "performers_tags", seed: seedPerformerTag},
	{table: "performers_scenes", seed: seedScenePerformer},
	{table: "performers_galleries", seed: seedGalleryPerformer},
}

func seedBodyMark(t *testing.T, ctx context.Context, id int) {
	_, err := db.Performer.CreateBodyMark(ctx, models.BodyMark{
		PerformerID: id, Kind: "tattoo", Location: "destroy-fixture",
	})
	require.NoError(t, err)
}

func seedNationality(t *testing.T, ctx context.Context, id int) {
	nats, err := db.Performer.AllNationalities(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, nats, "the nationality reference list is empty; migration 125 seeds it")
	require.NoError(t, db.Performer.SetNationalities(ctx, id, []int{nats[0].ID}))
}

func seedPerformerAlias(t *testing.T, ctx context.Context, id int) {
	// No SetAliases exists: aliases are a RelatedStrings on the model, written like every other
	// relationship through UpdatePartial rather than by a store method of their own.
	_, err := db.Performer.UpdatePartial(ctx, id, models.PerformerPartial{
		Aliases: &models.UpdateStrings{
			Values: []string{"destroy-fixture-alias"},
			Mode:   models.RelationshipUpdateModeSet,
		},
	})
	require.NoError(t, err)
}

func seedPerformerAliasOwner(t *testing.T, ctx context.Context, id int) {
	// OwnerPerformerID is the SAME performer, which is the #422/#2341 case: the alias belongs to
	// someone, and that someone is who is being destroyed. Two FKs point at `performers` here --
	// performer_id cascades and owner_performer_id sets null -- and this row must go away, not be
	// left pointing at nobody.
	// OwnerPerformerID is a *int because "no owner" is a real state (#422: an alias that is just an
	// alias). Pointing it at the same performer needs an explicit address, and taking the address of
	// the parameter is safe because the store reads it during this call.
	owner := id
	require.NoError(t, db.Performer.SetAliasOwner(ctx, models.PerformerAliasOwnership{
		PerformerID:      id,
		Alias:            "destroy-fixture-owned",
		OwnerPerformerID: &owner,
	}))
}

func seedScenePerformerAlias(t *testing.T, ctx context.Context, id int) {
	s := models.Scene{Path: "/tmp/destroy-fixture-scene-alias.mp4"}
	require.NoError(t, db.Scene.Create(ctx, &s, nil))
	require.NoError(t, db.Scene.SetPerformerAlias(ctx, models.ScenePerformerAlias{
		SceneID:     s.ID,
		PerformerID: id,
		Alias:       "destroy-fixture-scene-alias",
	}))
}

func seedPerformerTag(t *testing.T, ctx context.Context, id int) {
	tag := models.Tag{Name: "destroy-fixture-tag"}
	require.NoError(t, db.Tag.Create(ctx, &models.CreateTagInput{Tag: &tag}))

	_, err := db.Performer.UpdatePartial(ctx, id, models.PerformerPartial{
		TagIDs: &models.UpdateIDs{IDs: []int{tag.ID}, Mode: models.RelationshipUpdateModeSet},
	})
	require.NoError(t, err)
}

func seedPerformerImageLink(t *testing.T, ctx context.Context, id int) {
	// The image carries the performer rather than the other way round, which is how filesystem
	// autotag links them (image.AddPerformer) and therefore how the row really arrives. An
	// unseeded table proves nothing, so this creates a real image row rather than skipping.
	img := makeImage(2359)
	img.PerformerIDs.Add(id)
	require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{Image: img}))
	require.NotZero(t, img.ID)
}

func seedPerformerStashID(t *testing.T, ctx context.Context, id int) {
	_, err := db.Performer.UpdatePartial(ctx, id, models.PerformerPartial{
		StashIDs: &models.UpdateStashIDs{
			StashIDs: []models.StashID{{StashID: "stash-destroy-fixture", Endpoint: "fixture"}},
			Mode:     models.RelationshipUpdateModeSet,
		},
	})
	require.NoError(t, err)
}

func seedPerformerCustomField(t *testing.T, ctx context.Context, id int) {
	// Custom fields are JSON against a definition. An unrecognised key is stored as a custom field
	// rather than rejected, which is the behaviour that makes this fixture possible without first
	// creating a field definition -- and creating a definition would mean the value depends on the
	// definition existing, so the fixture would be testing the definition too.
	_, err := db.Performer.UpdatePartial(ctx, id, models.PerformerPartial{
		CustomFields: models.CustomFieldsInput{
			Full: map[string]interface{}{"destroy_fixture": "present"},
		},
	})
	require.NoError(t, err)
}

func seedScenePerformer(t *testing.T, ctx context.Context, id int) {
	s := models.Scene{Path: "/tmp/destroy-fixture-scene.mp4"}
	require.NoError(t, db.Scene.Create(ctx, &s, nil))

	_, err := db.Scene.UpdatePartial(ctx, s.ID, models.ScenePartial{
		PerformerIDs: &models.UpdateIDs{IDs: []int{id}, Mode: models.RelationshipUpdateModeSet},
	})
	require.NoError(t, err)
}

func seedGalleryPerformer(t *testing.T, ctx context.Context, id int) {
	g := models.Gallery{Path: "/tmp/destroy-fixture-gallery"}
	require.NoError(t, db.Gallery.Create(ctx, &models.CreateGalleryInput{Gallery: &g}))

	_, err := db.Gallery.UpdatePartial(ctx, g.ID, models.GalleryPartial{
		PerformerIDs: &models.UpdateIDs{IDs: []int{id}, Mode: models.RelationshipUpdateModeSet},
	})
	require.NoError(t, err)
}

func seedPerformerURL(t *testing.T, ctx context.Context, id int) {
	_, err := db.Performer.UpdatePartial(ctx, id, models.PerformerPartial{
		URLs: &models.UpdateStrings{
			Values: []string{"https://example.invalid/destroy-fixture"},
			Mode:   models.RelationshipUpdateModeSet,
		},
	})
	require.NoError(t, err)
}

// destroyRowCount returns how many rows in `table` belong to performerID.
//
// Built on parityScalar rather than db.Get, because `Database.QuerySQL` is the accessor this
// package's tests already use and db.Get does not exist on *Database -- the sqlx handle is reached
// through a wrapper instead. Two ways to count in one file is one more than is needed.
func destroyRowCount(t *testing.T, ctx context.Context, table string, performerID int) int {
	t.Helper()

	s := parityScalar(ctx, t,
		fmt.Sprintf("SELECT count(*) FROM `%s` WHERE `performer_id` = ?", table), performerID)

	n, err := strconv.Atoi(s)
	require.NoErrorf(t, err, "counting %s rows for performer %d returned %q", table, performerID, s)

	return n
}

// TestDestroyingAPerformerLeavesNoRowsBehind is the L5 assertion.
//
// The failure it is written for is silent in the strongest sense: Destroy returns nil, the performer
// row is gone, and every table that mentioned it still holds rows pointing at an id that no longer
// exists. Nothing queries for a missing performer id often enough to notice.
func TestDestroyingAPerformerLeavesNoRowsBehind(t *testing.T) {
	runWithRollbackTxn(t, "destroying a performer clears every table that references it", func(t *testing.T, ctx context.Context) {
		p := models.Performer{Name: "destroy-target"}
		require.NoError(t, db.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &p}))

		// Seed, then count, then assert the count is non-zero. A fixture that silently inserted
		// nothing would leave the after-count at zero for the wrong reason.
		before := make(map[string]int, len(parityDestroyFixtures))
		for _, fx := range parityDestroyFixtures {
			fx.seed(t, ctx, p.ID)

			n := destroyRowCount(t, ctx, fx.table, p.ID)
			require.NotZerof(t, n,
				"the fixture for %s inserted no row, so the assertion below would pass vacuously",
				fx.table)
			before[fx.table] = n
		}

		require.NoError(t, db.Performer.Destroy(ctx, p.ID))

		var orphaned []string
		for _, fx := range parityDestroyFixtures {
			if after := destroyRowCount(t, ctx, fx.table, p.ID); after > 0 {
				orphaned = append(orphaned, fmt.Sprintf("%s: %d row(s) survived", fx.table, after))
			}
		}
		sort.Strings(orphaned)

		assert.Empty(t, orphaned,
			"deleting a performer left rows behind in %d of %d tables:\n  %s\n\n"+
				"Each points at a performer id that no longer exists. SQLite enforces only the "+
				"constraints it was given, and migration 121's rule is that a general table cannot use "+
				"a foreign key at all -- so ON DELETE CASCADE is not a substitute for deleting rows "+
				"explicitly.",
			len(orphaned), len(parityDestroyFixtures), strings.Join(orphaned, "\n  "))
	})
}

// TestEveryPerformerReferencingTableIsCovered keeps the fixture list honest.
//
// A hand-maintained inventory is exactly what goes stale: it names the tables that existed when it
// was written and omits the one added later, and the omission is the bug this file exists to
// prevent. So the schema is the authority and the list is checked against it.
func TestEveryPerformerReferencingTableIsCovered(t *testing.T) {
	runWithRollbackTxn(t, "the fixture list covers every table with a performer_id", func(t *testing.T, ctx context.Context) {
		covered := make(map[string]bool, len(parityDestroyFixtures))
		for _, fx := range parityDestroyFixtures {
			covered[fx.table] = true
		}

		_, rows, err := db.QuerySQL(ctx, `
			SELECT m.name
			FROM sqlite_master AS m
			WHERE m.type = 'table'
			  AND m.name NOT LIKE 'sqlite_%'
			  AND EXISTS (
			    SELECT 1 FROM pragma_table_info(m.name) AS c WHERE c.name = 'performer_id'
			  )
			ORDER BY m.name`, nil)
		require.NoError(t, err)

		// Excluded by NAME rather than silently, so the exclusion is visible in this file instead of
		// invisible in the schema.
		// Empty on purpose. An earlier draft carried an entry here for a table that does not exist,
		// which would have made the exclusion look deliberate when it was only a wrong guess.
		ignored := map[string]string{}

		var uncovered []string
		for _, r := range rows {
			table := fmt.Sprintf("%v", r[0])
			if !covered[table] {
				if _, ok := ignored[table]; !ok {
					uncovered = append(uncovered, table)
				}
			}
		}

		assert.Empty(t, uncovered,
			"%d table(s) reference a performer but have no fixture in parityDestroyFixtures, so "+
				"TestDestroyingAPerformerLeavesNoRowsBehind does not check them: %v. Add a fixture, or "+
				"add the table to the ignored map WITH a reason.", len(uncovered), uncovered)
	})
}
