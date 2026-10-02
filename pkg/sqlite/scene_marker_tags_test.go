//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #1253 -- a marker's tags, through a REAL database.
//
// The model-level tests in `pkg/models/scene_marker_tags_test.go` prove the field and loader
// exist and delegate correctly. They cannot see the join, which is the part that can be
// wrong. So this drives the store.
//
// ## THE PRIMARY TAG IS NOT IN THE SET, AND THAT IS CORRECT
//
// I first wrote this file asserting that a marker's `primary_tag_id` must be among its
// `TagIDs`, reasoning that the two are "the same relationship stored twice". That is false,
// and the tree says so in three places:
//
//   - `pkg/sqlite/scene_marker.go:27` -- the tag count query deliberately ORs them:
//     `WHERE tags_join.tag_id = ? OR scene_markers.primary_tag_id = ?`
//   - `pkg/sqlite/scene_marker_test.go:160` carries a `HACK - if modifier isn't null/not
//     null, then add the primary tag id`, i.e. the existing test already compensates
//   - `pkg/scene/export.go:166` does `AppendUnique(ret, smm.PrimaryTagID)` when exporting
//
// So `primary_tag_id` and the join are TWO SEPARATE STORED FACTS that happen to describe
// overlapping sets, and the codebase reconciles them in each reader. `GetTagIDs` returns the
// join table, full stop. Asserting otherwise would have been me inventing a requirement and
// writing a test that "proved" the invention.
//
// What is worth pinning is the behaviour that actually exists, because it is the thing a
// future edit to `UpdateTags` could plausibly break.

// THE LOADER MUST RETURN THE JOIN TABLE, AND NOTHING ELSE.
func TestAMarkersTagIDsAreExactlyItsTagJoins(t *testing.T) {
	withTxn(func(ctx context.Context) error {
		tagA := createTagByName(ctx, t, "Invariant Alpha")
		tagB := createTagByName(ctx, t, "Invariant Beta")

		marker := &models.SceneMarker{Title: "Opening", Seconds: 1.5, PrimaryTagID: tagA, SceneID: sceneIDs[sceneIdxWithMarkers]}
		require.NoError(t, db.SceneMarker.Create(ctx, marker))

		// Create then UpdateTags: the production pattern, because `Create` does not write
		// the joins itself. Confirmed against every caller -- resolver_mutation_scene.go
		// twice, marker_import.go, and the test-db generator.
		require.NoError(t, db.SceneMarker.UpdateTags(ctx, marker.ID, []int{tagA, tagB}))

		got, err := db.SceneMarker.GetTagIDs(ctx, marker.ID)
		require.NoError(t, err)

		assert.ElementsMatch(t, []int{tagA, tagB}, got,
			"the loader returns the marker's tag joins")

		return nil
	})
}

// THE SAME MUST HOLD THROUGH THE MODEL LOADER ADDED BY #1253.
//
// Asserted separately because a `LoadTagIDs` wired to the wrong repository would satisfy a
// store-only test completely. `tagC` is created and NOT attached, so a loader that is not
// scoped to this marker's joins is caught rather than merely under-specified.
func TestTheModelLoaderReturnsExactlyTheJoinsAndNothingElse(t *testing.T) {
	withTxn(func(ctx context.Context) error {
		tagA := createTagByName(ctx, t, "Invariant Gamma")
		tagC := createTagByName(ctx, t, "Invariant Delta")
		tagUnrelated := createTagByName(ctx, t, "Invariant Epsilon")

		marker := &models.SceneMarker{Title: "Middle", Seconds: 10, PrimaryTagID: tagC, SceneID: sceneIDs[sceneIdxWithMarkers]}
		require.NoError(t, db.SceneMarker.Create(ctx, marker))
		require.NoError(t, db.SceneMarker.UpdateTags(ctx, marker.ID, []int{tagA, tagC}))

		require.NoError(t, marker.LoadTagIDs(ctx, db.SceneMarker))

		got := marker.TagIDs.List()
		assert.ElementsMatch(t, []int{tagA, tagC}, got)
		assert.NotContains(t, got, tagUnrelated,
			"an unrelated tag leaked in; the loader is not scoped to this marker's joins")

		return nil
	})
}

// AND THE PRIMARY TAG MUST NOT BE SYNTHESISED INTO THE SET.
//
// This is the tempting shortcut, and it is exactly what a future "helpfulness" edit would
// add: fall back to `[]int{PrimaryTagID}` when the join is empty. That would make the set
// disagree with the join table, which is the same class of bug as the zero-Date binding in
// #3450 -- a plausible value substituted for an absent one, invented rather than read.
func TestThePrimaryTagIsNotSynthesisedIntoTheSet(t *testing.T) {
	withTxn(func(ctx context.Context) error {
		tagA := createTagByName(ctx, t, "Invariant Zeta")

		marker := &models.SceneMarker{Title: "Ending", Seconds: 99, PrimaryTagID: tagA, SceneID: sceneIDs[sceneIdxWithMarkers]}
		require.NoError(t, db.SceneMarker.Create(ctx, marker))
		// Deliberately no UpdateTags: the join is empty while the primary tag is set.

		require.NoError(t, marker.LoadTagIDs(ctx, db.SceneMarker))

		assert.Empty(t, marker.TagIDs.List(),
			"with no tag joins the set must be empty; synthesising the primary tag would "+
				"invent a relationship the user never created")
		assert.True(t, marker.TagIDs.Loaded(),
			"an empty result must still be marked loaded, so a caller can tell "+
				"\"no tags\" from \"not loaded\"")

		return nil
	})
}

// REPLACING THE JOINS MUST ACTUALLY REPLACE THEM.
//
// `UpdateTags` deletes then inserts, so a second call with a different set must not leave
// the old ids behind. Worth its own assertion because `replace` and `modifyJoins` are
// different functions with different semantics, and #1253 made the replace path reachable
// through a new loader.
func TestUpdatingTagJoinsReplacesRatherThanAccumulates(t *testing.T) {
	withTxn(func(ctx context.Context) error {
		tagA := createTagByName(ctx, t, "Invariant Eta")
		tagB := createTagByName(ctx, t, "Invariant Theta")
		tagC := createTagByName(ctx, t, "Invariant Iota")

		marker := &models.SceneMarker{Title: "Replaced", Seconds: 20, PrimaryTagID: tagA, SceneID: sceneIDs[sceneIdxWithMarkers]}
		require.NoError(t, db.SceneMarker.Create(ctx, marker))
		require.NoError(t, db.SceneMarker.UpdateTags(ctx, marker.ID, []int{tagA, tagB}))
		require.NoError(t, db.SceneMarker.UpdateTags(ctx, marker.ID, []int{tagC}))

		require.NoError(t, marker.LoadTagIDs(ctx, db.SceneMarker))

		got := marker.TagIDs.List()
		assert.Equal(t, []int{tagC}, got,
			"a second UpdateTags must replace the set, not accumulate onto it")
		assert.NotContains(t, got, tagA)
		assert.NotContains(t, got, tagB)

		return nil
	})
}

// A marker belongs to a scene, and that is a REAL FOREIGN KEY -- not a convention.
//
// Found by hitting it: creating a marker without `SceneID` fails with `FOREIGN KEY
// constraint failed` on the `scene_markers` insert, with the bound arguments visible as
// `primary_tag_id = 0, scene_id = 0`. The existing `scene_marker_test.go` never creates a
// marker from scratch -- it reads the fixture at `sceneIDs[sceneIdxWithMarkers]` -- so
// this constraint is not documented anywhere except the schema.
//
// `primary_tag_id` is ALSO a real foreign key, and `0` is not a valid tag, so a marker
// cannot be created without one either. That is a second undocumented constraint found the
// same way: the insert is rejected with `primary_tag_id = 0` in the bound arguments.
//
// Note the asymmetry this creates, and it is the same one the rest of this file is about:
// the marker REQUIRES a primary tag to exist, yet that tag is deliberately NOT in the set
// this change makes reachable. Creating a marker and reading its tags therefore involve two
// different tag relationships, and a caller wanting "the tags of this marker" must decide
// deliberately whether the primary tag counts.
//
// `created_at`/`updated_at` come out as the zero time when a marker is built by hand rather
// than through `NewSceneMarker()`. Nothing under test reads the timestamps, so these tests
// build markers literally and accept that.
//
// createTagByName creates one tag and returns its id.
//
// The suite's existing `createTags` makes N tags with generated names and no handle on the
// ids, which is fine for its callers. These tests need to ATTACH a known tag and then assert
// an unrelated one is absent, so the id has to come back.
func createTagByName(ctx context.Context, t *testing.T, name string) int {
	t.Helper()

	tag := models.CreateTagInput{
		Tag: &models.Tag{
			Name:        name,
			Description: "created by TestSceneMarkerTagIDs",
		},
	}
	require.NoError(t, db.Tag.Create(ctx, &tag), "creating tag %q", name)

	found, err := db.Tag.FindByName(ctx, name, false)
	require.NoError(t, err, "finding tag %q just created", name)
	require.NotNil(t, found, "tag %q not found after creation", name)

	return found.ID
}
