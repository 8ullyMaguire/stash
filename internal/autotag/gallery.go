package autotag

import (
	"context"
	"slices"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/match"
	"github.com/stashapp/stash/pkg/models"
)

type GalleryFinderUpdater interface {
	models.GalleryQueryer
	models.GalleryUpdater
}

type GalleryPerformerUpdater interface {
	models.PerformerIDLoader
	models.GalleryUpdater
}

type GalleryTagUpdater interface {
	models.TagIDLoader
	models.GalleryUpdater
}

func getGalleryFileTagger(s *models.Gallery, cache *match.Cache) tagger {
	var path string
	if s.Path != "" {
		path = s.Path
	}

	// only trim the extension if gallery is file-based
	trimExt := s.PrimaryFileID != nil

	return tagger{
		ID:      s.ID,
		Type:    "gallery",
		Name:    s.DisplayName(),
		Path:    path,
		trimExt: trimExt,
		cache:   cache,
	}
}

// GalleryPerformers tags the provided gallery with performers whose name matches the gallery's path.
func GalleryPerformers(ctx context.Context, s *models.Gallery, rw GalleryPerformerUpdater, performerReader models.PerformerAutoTagQueryer, cache *match.Cache, sink Sink) error {
	t := getGalleryFileTagger(s, cache)

	return t.tagPerformers(ctx, performerReader, func(subjectID, otherID int) (bool, error) {
		if err := s.LoadPerformerIDs(ctx, rw); err != nil {
			return false, err
		}
		existing := s.PerformerIDs.List()

		if slices.Contains(existing, otherID) {
			return false, nil
		}

		return sink.AddMatch(ctx, "gallery", t.ID, collab.LinkKind("performer_ids"), otherID, "")
	})
}

// GalleryStudios tags the provided gallery with the first studio whose name matches the gallery's path.
//
// Gallerys will not be tagged if studio is already set.
func GalleryStudios(ctx context.Context, s *models.Gallery, rw GalleryFinderUpdater, studioReader models.StudioAutoTagQueryer, cache *match.Cache, sink Sink) error {
	if s.StudioID != nil {
		// don't modify
		return nil
	}

	t := getGalleryFileTagger(s, cache)

	return t.tagStudios(ctx, studioReader, func(subjectID, otherID int) (bool, error) {
		return sink.SetStudio(ctx, "gallery", t.ID, otherID)
	})
}

// GalleryTags tags the provided gallery with tags whose name matches the gallery's path.
func GalleryTags(ctx context.Context, s *models.Gallery, rw GalleryTagUpdater, tagReader models.TagAutoTagQueryer, cache *match.Cache, sink Sink) error {
	t := getGalleryFileTagger(s, cache)

	return t.tagTags(ctx, tagReader, func(subjectID, otherID int) (bool, error) {
		if err := s.LoadTagIDs(ctx, rw); err != nil {
			return false, err
		}
		existing := s.TagIDs.List()

		if slices.Contains(existing, otherID) {
			return false, nil
		}

		return sink.AddMatch(ctx, "gallery", t.ID, collab.LinkKind("tag_ids"), otherID, "")
	})
}
