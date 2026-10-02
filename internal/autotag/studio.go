package autotag

import (
	"context"

	"github.com/stashapp/stash/pkg/match"
	"github.com/stashapp/stash/pkg/models"
)

// the following functions aren't used in Tagger because they assume
// use within a transaction

func getStudioTagger(p *models.Studio, aliases []string, cache *match.Cache) []tagger {
	ret := []tagger{{
		ID:    p.ID,
		Type:  "studio",
		Name:  p.Name,
		cache: cache,
	}}

	// #2507: the alias taggers here were missing `cache`, so a studio's canonical name used
	// the path cache while its ALIASES re-parsed every path on every call. Found while
	// enabling performer aliases -- the same omission in the parallel function, and it had
	// gone unnoticed because a name-only assertion cannot see a nil cache.
	for _, a := range aliases {
		ret = append(ret, tagger{
			ID:    p.ID,
			Type:  "studio",
			Name:  a,
			cache: cache,
		})
	}

	return ret
}

// StudioScenes searches for scenes whose path matches the provided studio name and tags the scene with the studio, if studio is not already set on the scene.
func (tagger *Tagger) StudioScenes(ctx context.Context, p *models.Studio, paths []string, aliases []string, rw SceneFinderUpdater) error {
	t := getStudioTagger(p, aliases, tagger.Cache)

	for _, tt := range t {
		if err := tt.tagScenes(ctx, paths, rw, func(o *models.Scene) (bool, error) {
			// don't set if already set
			if o.StudioID != nil {
				return false, nil
			}

			return tagger.Sink.SetStudio(ctx, "scene", o.ID, p.ID)
		}); err != nil {
			return err
		}
	}

	return nil
}

// StudioImages searches for images whose path matches the provided studio name and tags the image with the studio, if studio is not already set on the image.
func (tagger *Tagger) StudioImages(ctx context.Context, p *models.Studio, paths []string, aliases []string, rw ImageFinderUpdater) error {
	t := getStudioTagger(p, aliases, tagger.Cache)

	for _, tt := range t {
		if err := tt.tagImages(ctx, paths, rw, func(i *models.Image) (bool, error) {
			// don't set if already set
			if i.StudioID != nil {
				return false, nil
			}

			return tagger.Sink.SetStudio(ctx, "image", i.ID, p.ID)
		}); err != nil {
			return err
		}
	}

	return nil
}

// StudioGalleries searches for galleries whose path matches the provided studio name and tags the gallery with the studio, if studio is not already set on the gallery.
func (tagger *Tagger) StudioGalleries(ctx context.Context, p *models.Studio, paths []string, aliases []string, rw GalleryFinderUpdater) error {
	t := getStudioTagger(p, aliases, tagger.Cache)

	for _, tt := range t {
		if err := tt.tagGalleries(ctx, paths, rw, func(o *models.Gallery) (bool, error) {
			// don't set if already set
			if o.StudioID != nil {
				return false, nil
			}

			return tagger.Sink.SetStudio(ctx, "gallery", o.ID, p.ID)
		}); err != nil {
			return err
		}
	}

	return nil
}
