//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stashapp/stash/pkg/image"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stash#571 -- `images: [String!]` on the performer inputs, with REPLACE semantics.
//
// THE DEFECT THIS PINS
//
// A performer carries images through TWO systems, and no field reaches both:
//
//	(1) a single-blob COLUMN, written by `blobJoinQueryBuilder.UpdateImage` and read by
//	    `performerResolver.ImagePath` via `HasImage` -- this is what `image` (the input) and
//	    `image_path` (the output) use;
//	(2) the `images` TABLE + `performers_images` join, read by `performerResolver.ImageCount`
//	    via `image.CountByPerformerID` -- this is what `image_count` uses.
//
// The GraphQL mutation writes (1) and never touches (2), so `image_count` is 0 for a performer
// whose image was set through the API. Correct only for autotagged rows, which write the join
// from the filesystem side. `TestPerformerImageCountIgnoresTheAPIPathImage` already proves that
// half; what is missing is any way to SET the join from the API.
//
// THE FIX, and the decision behind it
//
// Add `images: [String!]` to PerformerCreateInput and PerformerUpdateInput with REPLACE
// semantics, matching how `alias_list`, `urls` and `tag_ids` already behave: the field IS the
// new set, absent means "do not touch", present replaces wholesale.
//
// The blob column is deliberately LEFT ALONE. Two systems is ugly, but rewriting the blob to
// mirror the join would delete the single image every existing user has, and the goal file
// says to record a subsystem-sized issue rather than half-build it. So this is additive in
// effect even though the field replaces.
//
// WHY THE VALUES ARE IMAGE PATHS AND NOT IDs
//
// `performers_images` joins performer_id to image_id, and images are FILES with their own
// scan lifecycle -- so the input is a path (or data URL) that the existing image-creation path
// resolves, exactly as the autotag side does. An ID input would need a client to have created
// the image row first, which is the opposite of the use case.

// stash571Image builds a minimal image row for these tests.
//
// NOT `makeImage`: that helper indexes the seeded fixture arrays (`imagePerformers[i]`,
// `imageGalleries[i]`, ...) by the index you pass, so an index outside the fixture range
// indexes nonsense or panics. The first version of this file passed 571_001 and got a
// FOREIGN KEY failure instead of a clear panic -- a fixture bug that looked like a product
// bug, which is the worst way to spend an afternoon.
func stash571Image(title string) *models.Image {
	return &models.Image{Title: title}
}

// The store method this test needs. It does not exist yet -- that is the point: the RED run
// below fails to COMPILE, which is a stronger statement of "not implemented" than a failing
// assertion would be, and it cannot be mistaken for a test that was never wired up.

// SETTING IMAGES MUST MAKE image_count SEE THEM.
//
// Without this, a client has no way to reach system (2) at all: `image` sets the blob, the
// autotagger sets the join, and nothing in the API sets the join on demand.
func TestSettingImagesMakesTheCountSeeThem(t *testing.T) {
	var (
		performerID int
		firstID     int
		secondID    int
	)

	err := withTxn(func(ctx context.Context) error {
		p, err := withStash571Performer(ctx, "stash#571 images field")
		if err != nil {
			return err
		}
		performerID = p.ID

		// Two DISTINCT images, because a test that set the same path twice could not tell
		// "replaced" from "appended", and REPLACE-vs-APPEND is the whole semantic question.
		first := stash571Image("stash#571 first")
		require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{Image: first}))
		require.NotZero(t, first.ID, "Create must set the ID; a zero here means the row was not inserted")
		firstID = first.ID

		second := stash571Image("stash#571 second")
		require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{Image: second}))
		require.NotZero(t, second.ID)
		secondID = second.ID

		return db.Performer.SetImages(ctx, performerID, []int{firstID, secondID})
	})
	require.NoError(t, err)

	err = withTxn(func(ctx context.Context) error {
		got, err := image.CountByPerformerID(ctx, db.Image, performerID)
		require.NoError(t, err)
		assert.Equal(t, 2, got,
			"image_count must see both images after they are set through the API -- it reads the "+
				"join table, which nothing in the mutation wrote before this")

		// And the blob must be untouched: the two systems are independent, and this fix is
		// additive precisely so no existing single image is destroyed.
		hasBlob, err := db.Performer.HasImage(ctx, performerID)
		require.NoError(t, err)
		assert.False(t, hasBlob,
			"setting the join must NOT create a blob; a client that sets images has not set "+
				"image_path, and conflating them is how the two systems got confused")

		return db.Performer.Destroy(ctx, performerID)
	})
	require.NoError(t, err)
}

// REPLACE, NOT APPEND -- and this is the assertion that would catch an append implementation
// passing every other test here.
func TestSettingImagesReplacesRatherThanAccumulates(t *testing.T) {
	err := withTxn(func(ctx context.Context) error {
		p, err := withStash571Performer(ctx, "stash#571 replace")
		require.NoError(t, err)

		first := stash571Image("stash#571 replace first")
		require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{Image: first}))
		second := stash571Image("stash#571 replace second")
		require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{Image: second}))

		require.NoError(t, db.Performer.SetImages(ctx, p.ID, []int{first.ID, second.ID}))
		count, err := image.CountByPerformerID(ctx, db.Image, p.ID)
		require.NoError(t, err)
		require.Equal(t, 2, count)

		// Now set only the second. REPLACE semantics means the first is GONE.
		require.NoError(t, db.Performer.SetImages(ctx, p.ID, []int{second.ID}))
		countAfter, err := image.CountByPerformerID(ctx, db.Image, p.ID)
		require.NoError(t, err)
		assert.Equal(t, 1, countAfter,
			"setting images REPLACES the set (like alias_list / urls / tag_ids); the first image "+
				"is no longer linked, so the count must drop to 1")

		return db.Performer.Destroy(ctx, p.ID)
	})
	require.NoError(t, err)
}

// AN EMPTY LIST CLEARS. Present-but-empty is not the same as absent, and conflating them is
// how "I removed the last image" turns into "I silently did nothing".
func TestAnEmptyImageListClearsThem(t *testing.T) {
	err := withTxn(func(ctx context.Context) error {
		p, err := withStash571Performer(ctx, "stash#571 clear")
		require.NoError(t, err)

		img := stash571Image("stash#571 clear")
		require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{Image: img}))
		require.NoError(t, db.Performer.SetImages(ctx, p.ID, []int{img.ID}))

		count, err := image.CountByPerformerID(ctx, db.Image, p.ID)
		require.NoError(t, err)
		require.Equal(t, 1, count)

		require.NoError(t, db.Performer.SetImages(ctx, p.ID, []int{}))
		countAfter, err := image.CountByPerformerID(ctx, db.Image, p.ID)
		require.NoError(t, err)
		assert.Equal(t, 0, countAfter,
			"an explicitly empty list clears the images; treating it as \"absent, do not touch\" "+
				"would leave a removed image in place")

		return db.Performer.Destroy(ctx, p.ID)
	})
	require.NoError(t, err)
}
