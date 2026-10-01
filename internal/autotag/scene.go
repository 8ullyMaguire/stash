package autotag

import (
	"context"
	"slices"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/match"
	"github.com/stashapp/stash/pkg/models"
)

type SceneFinderUpdater interface {
	models.SceneQueryer
	models.SceneUpdater
}

type ScenePerformerUpdater interface {
	models.PerformerIDLoader
	models.SceneUpdater
}

type SceneTagUpdater interface {
	models.TagIDLoader
	models.SceneUpdater
}

func getSceneFileTagger(s *models.Scene, cache *match.Cache) tagger {
	return tagger{
		ID:    s.ID,
		Type:  "scene",
		Name:  s.DisplayName(),
		Path:  s.Path,
		cache: cache,
	}
}

// ScenePerformers tags the provided scene with performers whose name matches the scene's path.
//
// ROUTED THROUGH A SINK since 2026-10-03, and the shape is unchanged: the same
// LoadPerformerIDs, the same slices.Contains check, the same log line. What changed is
// the last step -- scene.AddPerformer is now sink.AddMatch, and a sink backed by
// autoproposal.Curator files the match as a proposal instead of writing it. §6b.2: an
// automatic direct write is a machine laundering a claim past governance.
//
// THE ALREADY-CHECK STAYS ABOVE THE SINK, and that is deliberate. It is a READ of
// local state, not a write, so it is not what governance governs -- and keeping it
// here means a filed proposal is not filed for a link the scene already has, which is
// what stops a re-run of autotag from filling the proposal table with no-op claims.
func ScenePerformers(ctx context.Context, s *models.Scene, rw ScenePerformerUpdater, performerReader models.PerformerAutoTagQueryer, cache *match.Cache, sink Sink) error {
	t := getSceneFileTagger(s, cache)

	return t.tagPerformers(ctx, performerReader, func(subjectID, otherID int) (bool, error) {
		if err := s.LoadPerformerIDs(ctx, rw); err != nil {
			return false, err
		}
		existing := s.PerformerIDs.List()

		if slices.Contains(existing, otherID) {
			return false, nil
		}

		return sink.AddMatch(ctx, "scene", t.ID, collab.LinkScenePerformer, otherID, "")
	})
}

// SceneStudios tags the provided scene with the first studio whose name matches the scene's path.
//
// Scenes will not be tagged if studio is already set.
func SceneStudios(ctx context.Context, s *models.Scene, rw SceneFinderUpdater, studioReader models.StudioAutoTagQueryer, cache *match.Cache, sink Sink) error {
	if s.StudioID != nil {
		// don't modify
		return nil
	}

	t := getSceneFileTagger(s, cache)

	return t.tagStudios(ctx, studioReader, func(subjectID, otherID int) (bool, error) {
		return sink.SetStudio(ctx, "scene", t.ID, otherID)
	})
}

// SceneTags tags the provided scene with tags whose name matches the scene's path.
func SceneTags(ctx context.Context, s *models.Scene, rw SceneTagUpdater, tagReader models.TagAutoTagQueryer, cache *match.Cache, sink Sink) error {
	t := getSceneFileTagger(s, cache)

	return t.tagTags(ctx, tagReader, func(subjectID, otherID int) (bool, error) {
		if err := s.LoadTagIDs(ctx, rw); err != nil {
			return false, err
		}
		existing := s.TagIDs.List()

		if slices.Contains(existing, otherID) {
			return false, nil
		}

		return sink.AddMatch(ctx, "scene", t.ID, collab.LinkSceneTag, otherID, "")
	})
}
