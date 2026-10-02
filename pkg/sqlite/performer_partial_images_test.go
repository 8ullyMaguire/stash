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

// The companion to performer_image_test.go, which proved the DEFECT: a performer
// created through the API has an image by every measure the API exposes, yet
// `image_count` reports 0, because the mutation wrote the blob column and never
// inserted a `performers_images` row.
//
// Those tests describe the hole. This file covers the way through it: the
// `ImageIDs` partial field on PerformerStore.UpdatePartial, which is what the
// GraphQL `images` argument now reaches.
//
// The property under test is REPLACE, and specifically the distinction that makes
// it worth having:
//
//	absent (nil)             -> the join is not touched
//	present, non-empty       -> exactly that set
//	present, EMPTY (non-nil) -> cleared
//
// The empty case is the one a naive implementation gets wrong. An `if len(ids) > 0`
// guard is the obvious way to write this, and it silently converts "the client
// removed the last image" into "the client asked for nothing, do nothing" -- the
// join keeps its old rows and the caller gets no error. That is a delete that
// reports success while deleting nothing, so the empty case is pinned here
// separately rather than folded into the main test.
//
// Cleanup mirrors performer_image_test.go: destroy the performer AND the images
// inside the transaction, because performers_images cascades from both sides and
// the suite's fixed fixture counts (totalPerformers) must survive.

func TestPerformerUpdatePartialImageIDsReplacesTheSet(t *testing.T) {
	withTxn(func(ctx context.Context) error {
		p, err := withStash571Performer(ctx, "stash#571 partial replace")
		require.NoError(t, err)
		require.NotZero(t, p.ID)

		first := stash571Image("partial first")
		require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{Image: first}))
		require.NotZero(t, first.ID)
		second := stash571Image("partial second")
		require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{Image: second}))
		require.NotZero(t, second.ID)

		// Two images, then one: the second call must NOT leave the first behind.
		partial := models.NewPerformerPartial()
		partial.ImageIDs = &models.UpdateIDs{
			IDs:  []int{first.ID, second.ID},
			Mode: models.RelationshipUpdateModeSet,
		}
		_, err = db.Performer.UpdatePartial(ctx, p.ID, partial)
		require.NoError(t, err)

		count, err := image.CountByPerformerID(ctx, db.Image, p.ID)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "both linked images should be counted")

		partial = models.NewPerformerPartial()
		partial.ImageIDs = &models.UpdateIDs{
			IDs:  []int{second.ID},
			Mode: models.RelationshipUpdateModeSet,
		}
		_, err = db.Performer.UpdatePartial(ctx, p.ID, partial)
		require.NoError(t, err)

		count, err = image.CountByPerformerID(ctx, db.Image, p.ID)
		require.NoError(t, err)
		assert.Equal(t, 1, count,
			"REPLACE semantics: sending a shorter set must drop the omitted image, not add to it")

		return stash571Cleanup(ctx, p, first, second)
	})
}

// The empty-list case, deliberately its own test. A `len(ids) > 0` guard would
// make the other three tests pass and this one fail.
func TestPerformerUpdatePartialImageIDsEmptyListClears(t *testing.T) {
	withTxn(func(ctx context.Context) error {
		p, err := withStash571Performer(ctx, "stash#571 partial clear")
		require.NoError(t, err)
		require.NotZero(t, p.ID)

		img := stash571Image("partial to clear")
		require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{Image: img}))
		require.NotZero(t, img.ID)

		partial := models.NewPerformerPartial()
		partial.ImageIDs = &models.UpdateIDs{
			IDs:  []int{img.ID},
			Mode: models.RelationshipUpdateModeSet,
		}
		_, err = db.Performer.UpdatePartial(ctx, p.ID, partial)
		require.NoError(t, err)

		count, err := image.CountByPerformerID(ctx, db.Image, p.ID)
		require.NoError(t, err)
		require.Equal(t, 1, count, "precondition: the image is linked before we clear it")

		partial = models.NewPerformerPartial()
		partial.ImageIDs = &models.UpdateIDs{
			IDs:  []int{}, // present but EMPTY -- must clear, not no-op
			Mode: models.RelationshipUpdateModeSet,
		}
		_, err = db.Performer.UpdatePartial(ctx, p.ID, partial)
		require.NoError(t, err)

		count, err = image.CountByPerformerID(ctx, db.Image, p.ID)
		require.NoError(t, err)
		assert.Equal(t, 0, count,
			"an empty list must CLEAR the set; a len>0 guard would leave the row and "+
				"report success for a delete that deleted nothing")

		return stash571Cleanup(ctx, p, img)
	})
}

// Absent must mean "do not touch". Every OTHER performer partial field is
// optional in exactly this way, and a client updating only `name` must not have
// its images wiped by a field it never sent.
func TestPerformerUpdatePartialAbsentImageIDsLeavesTheSetAlone(t *testing.T) {
	withTxn(func(ctx context.Context) error {
		p, err := withStash571Performer(ctx, "stash#571 partial absent")
		require.NoError(t, err)
		require.NotZero(t, p.ID)

		img := stash571Image("partial untouched")
		require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{Image: img}))
		require.NotZero(t, img.ID)

		partial := models.NewPerformerPartial()
		partial.ImageIDs = &models.UpdateIDs{
			IDs:  []int{img.ID},
			Mode: models.RelationshipUpdateModeSet,
		}
		_, err = db.Performer.UpdatePartial(ctx, p.ID, partial)
		require.NoError(t, err)

		// An unrelated update: name only, ImageIDs left nil.
		partial = models.NewPerformerPartial()
		newName := "stash#571 partial absent renamed"
		partial.Name = models.NewOptionalString(newName)
		_, err = db.Performer.UpdatePartial(ctx, p.ID, partial)
		require.NoError(t, err)

		count, err := image.CountByPerformerID(ctx, db.Image, p.ID)
		require.NoError(t, err)
		assert.Equal(t, 1, count,
			"a partial that omits images must not touch the join; nil and empty mean "+
				"different things and only one of them is 'do not touch'")

		return stash571Cleanup(ctx, p, img)
	})
}

// The blob and the join stay independent. This is the decision that makes the fix
// additive rather than destructive: a performer that sets BOTH `image` (blob,
// serving image_path) and `images` (join, serving image_count) must end up with
// both, and setting one must never delete the other.
func TestPerformerPartialImagesDoNotDisturbTheBlobColumn(t *testing.T) {
	withTxn(func(ctx context.Context) error {
		p, err := withStash571Performer(ctx, "stash#571 independence again")
		require.NoError(t, err)
		require.NotZero(t, p.ID)

		// The blob, written the way PerformerCreate writes it.
		require.NoError(t, db.Performer.UpdateImage(ctx, p.ID, []byte("fake-png-bytes")))

		img := stash571Image("alongside the blob")
		require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{Image: img}))
		require.NotZero(t, img.ID)

		partial := models.NewPerformerPartial()
		partial.ImageIDs = &models.UpdateIDs{
			IDs:  []int{img.ID},
			Mode: models.RelationshipUpdateModeSet,
		}
		_, err = db.Performer.UpdatePartial(ctx, p.ID, partial)
		require.NoError(t, err)

		hasImage, err := db.Performer.HasImage(ctx, p.ID)
		require.NoError(t, err)
		assert.True(t, hasImage,
			"writing the join must not clear the blob column; they are independent by "+
				"design so no existing user's single image is destroyed")

		count, err := image.CountByPerformerID(ctx, db.Image, p.ID)
		require.NoError(t, err)
		assert.Equal(t, 1, count, "and the join is written as asked")

		return stash571Cleanup(ctx, p, img)
	})
}

// A non-Set mode is refused rather than silently treated as Set. A client asking
// to APPEND an image is asking for something this field does not model, and
// quietly replacing the whole set would delete images it never mentioned -- a
// data-loss bug wearing the costume of a working feature.
func TestPerformerPartialImagesRejectsNonSetModes(t *testing.T) {
	withTxn(func(ctx context.Context) error {
		for _, mode := range []models.RelationshipUpdateMode{
			models.RelationshipUpdateModeAdd,
			models.RelationshipUpdateModeRemove,
		} {
			p, err := withStash571Performer(ctx, "stash#571 mode "+string(mode))
			require.NoError(t, err)

			partial := models.NewPerformerPartial()
			partial.ImageIDs = &models.UpdateIDs{IDs: []int{1}, Mode: mode}

			_, err = db.Performer.UpdatePartial(ctx, p.ID, partial)
			require.Error(t, err,
				"mode %v must be refused: silently replacing the set would delete images "+
					"the client never mentioned", mode)
			assert.Contains(t, err.Error(), "images",
				"the error must name the field, so a client can tell which argument was wrong")

			require.NoError(t, db.Performer.Destroy(ctx, p.ID))
		}
		return nil
	})
}

// stash571Cleanup destroys what a test created, INSIDE the transaction, and
// returns the first error rather than discarding it -- see the note in
// performer_image_test.go about a `_ =` on a Destroy error making a broken
// cleanup look like a working one.
func stash571Cleanup(ctx context.Context, p *models.Performer, imgs ...*models.Image) error {
	for _, img := range imgs {
		if err := db.Image.Destroy(ctx, img.ID); err != nil {
			return err
		}
	}
	return db.Performer.Destroy(ctx, p.ID)
}
