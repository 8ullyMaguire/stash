//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/image"
	"github.com/stashapp/stash/pkg/models"
)

// stash#571 is "Support for multiple performer images" (bounty). The issue's
// premise is that the data model can only hold ONE image per performer. Measured
// against a real database, that is not the blocker: `performers_images` is a
// many-to-many join table (migration 13) and a performer can already have any
// number of rows in it.
//
// The real defect is narrower and worse than a missing feature. A performer
// carries an image through TWO unrelated systems:
//
//	1. a single-blob COLUMN on `performers`, written by
//	   `blobJoinQueryBuilder.UpdateImage` and read by
//	   `performerResolver.ImagePath` via `HasImage`;
//	2. the `images` TABLE plus the `performers_images` join, read by
//	   `performerResolver.ImageCount` via `image.CountByPerformerID`.
//
// The GraphQL mutation writes (1) and never touches (2). The counter reads (2).
// So `image_count` reports 0 for a performer that demonstrably has an image --
// the exact field a client would use to decide whether to fetch one.
//
// This file proves that against the real schema, because reading the resolvers
// only shows the two code paths exist; it does not show what each returns after
// the mutation the API actually performs.

// withTxn COMMITS, and the suite's own TestPerformerCount and TestPerformerAll
// assert against a fixed global fixture count (totalPerformers). A performer left
// behind here makes those fail -- it did, with 28 against 25 -- so every test
// below ends by destroying what it created, INSIDE the transaction.
//
// Two mistakes in a row, both worth recording:
//
//   - cleanup in a `defer` fired AFTER withTxn returned, and wrote `_ =` on the
//     Destroy error, so a cleanup that silently did nothing looked identical to
//     one that worked. A discarded error is exactly the verification a cleanup
//     exists to provide.
//   - the images my tests create also need destroying, since
//     performers_images is ON DELETE CASCADE from both sides and a stale join row
//     outlives the performer it pointed at.
func withStash571Performer(ctx context.Context, name string) (*models.Performer, error) {
	gender := models.GenderEnumFemale
	p := &models.Performer{
		Name:   name,
		Gender: &gender,
	}
	if err := db.Performer.Create(ctx, &models.CreatePerformerInput{Performer: p}); err != nil {
		return nil, err
	}
	return p, nil
}

func TestPerformerImageCountIgnoresTheAPIPathImage(t *testing.T) {
	withTxn(func(ctx context.Context) error {
		// Create sets the ID on the performer it is given and returns only an
		// error, so the id is read back off the struct.
		p, err := withStash571Performer(ctx, "stash#571 api path")
		require.NoError(t, err)
		require.NotZero(t, p.ID)

		// This is EXACTLY what PerformerCreate does: ProcessImageInput into bytes,
		// then qb.UpdateImage(ctx, id, imageData).
		require.NoError(t, db.Performer.UpdateImage(ctx, p.ID, []byte("fake-png-bytes")))

		hasImage, err := db.Performer.HasImage(ctx, p.ID)
		require.NoError(t, err)
		require.True(t, hasImage, "the API path stored the image -- otherwise this test proves nothing")

		count, err := image.CountByPerformerID(ctx, db.Image, p.ID)
		require.NoError(t, err)

		// THE DEFECT. The performer has an image by every measure the API exposes,
		// and image_count says it has none.
		assert.Equal(t, 0, count,
			"image_count counts performers_images rows, but PerformerCreate writes a blob "+
				"column and never inserts one -- so the field is 0 for any performer created "+
				"through the API with an image")

		return db.Performer.Destroy(ctx, p.ID)
	})
}

// The control that keeps the test above honest, and it is the positive control
// this project keeps needing: if `CountByPerformerID` returned 0 for EVERY
// performer, the defect test would pass for the wrong reason. Joining a real
// image row -- which is what filesystem autotag does, via
// `image.AddPerformer` -- must make the count 1.
func TestPerformerImageCountSeesAnAutotaggedImage(t *testing.T) {
	withTxn(func(ctx context.Context) error {
		p, err := withStash571Performer(ctx, "stash#571 autotag path")
		require.NoError(t, err)

		img := makeImage(571)
		img.PerformerIDs.Add(p.ID)
		require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{Image: img}))
		require.NotZero(t, img.ID)

		count, err := image.CountByPerformerID(ctx, db.Image, p.ID)
		require.NoError(t, err)

		assert.Equal(t, 1, count,
			"POSITIVE CONTROL: a performer with a real images row counts as 1, so the 0 in the "+
				"test above is the missing join row and not a broken counter")

		return db.Performer.Destroy(ctx, p.ID)
	})
}

// And the third state, which is the one that makes the two systems concrete: a
// performer with BOTH. The blob is what image_path serves; the join row is what
// image_count counts. They are independent, and nothing in the API reconciles
// them.
func TestPerformerBlobAndJoinImageAreIndependent(t *testing.T) {
	withTxn(func(ctx context.Context) error {
		p, err := withStash571Performer(ctx, "stash#571 both")
		require.NoError(t, err)

		require.NoError(t, db.Performer.UpdateImage(ctx, p.ID, []byte("blob-image")))

		hasBlob, err := db.Performer.HasImage(ctx, p.ID)
		require.NoError(t, err)
		countBefore, err := image.CountByPerformerID(ctx, db.Image, p.ID)
		require.NoError(t, err)

		assert.True(t, hasBlob, "the blob is set -- this is what image_path reports")
		assert.Equal(t, 0, countBefore, "and image_count still cannot see it")

		img := makeImage(5711)
		img.PerformerIDs.Add(p.ID)
		require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{Image: img}))

		countAfter, err := image.CountByPerformerID(ctx, db.Image, p.ID)
		require.NoError(t, err)
		hasBlobAfter, err := db.Performer.HasImage(ctx, p.ID)
		require.NoError(t, err)

		assert.Equal(t, 1, countAfter, "adding the join row moves image_count")
		assert.True(t, hasBlobAfter, "and leaves the blob alone -- the two systems do not touch each other")

		return db.Performer.Destroy(ctx, p.ID)
	})
}
