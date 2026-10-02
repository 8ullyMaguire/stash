package autotag

import (
	"context"
	"slices"
	"strings"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/match"
	"github.com/stashapp/stash/pkg/models"
)

type SceneQueryPerformerUpdater interface {
	models.SceneQueryer
	models.PerformerIDLoader
	models.SceneUpdater
}

type ImageQueryPerformerUpdater interface {
	models.ImageQueryer
	models.PerformerIDLoader
	models.ImageUpdater
}

type GalleryQueryPerformerUpdater interface {
	models.GalleryQueryer
	models.PerformerIDLoader
	models.GalleryUpdater
}

// getPerformerTaggers returns one tagger per NAME a performer may appear under: the
// canonical name first, then each usable alias.
//
// #2507. The alias loop was present but commented out, with upstream's note "TODO -
// disabled until we can have finer control over alias matching". Meanwhile `task_autotag.go`
// loaded aliases on every path (so the data was available and then discarded), studios and
// tags had always received their aliases explicitly, and `PerformerScenes`'s own doc comment
// already said "Performer aliases must be loaded". So the intent was always here; only the
// loop was missing.
//
// THE PRECISION CONCERN UPSTREAM RAISED, and what answers it. An alias is free text, so it
// can be a variant spelling, a former name, or something that names several people, and
// auto-tag writes silently -- a wrong match is hard to notice and expensive to trace. Three
// rules keep that risk bounded, and each is pinned by a test in `performer_alias_test.go`:
//
//   - The canonical name is ALWAYS the first tagger, so turning aliases off can never lose
//     name-only matching.
//   - A blank or whitespace-only alias is SKIPPED. An empty name becomes a regex that
//     matches every path, so one empty alias would tag the whole library with one
//     performer -- the single most damaging outcome available here.
//   - Aliases are DE-DUPLICATED case-insensitively, so a repeated alias does not re-query
//     the same paths.
//
// Each alias is its own tagger because the name is what becomes the path regex, so a match
// is attributable to the name that produced it rather than being merged into the canonical
// one.
func getPerformerTaggers(p *models.Performer, cache *match.Cache) []tagger {
	ret := []tagger{{
		ID:    p.ID,
		Type:  "performer",
		Name:  p.Name,
		cache: cache,
	}}

	// Case-insensitive because the path regex it feeds is case-insensitive, so "j. doe"
	// and "J. Doe" would match identical paths -- a second query for no new coverage.
	seen := map[string]struct{}{strings.ToLower(p.Name): {}}

	for _, a := range p.Aliases.List() {
		name := strings.TrimSpace(a)
		if name == "" {
			continue
		}

		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		ret = append(ret, tagger{
			ID:    p.ID,
			Type:  "performer",
			Name:  name,
			cache: cache,
		})
	}

	return ret
}

// PerformerScenes searches for scenes whose path matches the provided performer name and tags the scene with the performer.
// Performer aliases must be loaded.
func (tagger *Tagger) PerformerScenes(ctx context.Context, p *models.Performer, paths []string, rw SceneQueryPerformerUpdater) error {
	t := getPerformerTaggers(p, tagger.Cache)

	for _, tt := range t {
		if err := tt.tagScenes(ctx, paths, rw, func(o *models.Scene) (bool, error) {
			if err := o.LoadPerformerIDs(ctx, rw); err != nil {
				return false, err
			}
			existing := o.PerformerIDs.List()

			if slices.Contains(existing, p.ID) {
				return false, nil
			}

			return tagger.Sink.AddMatch(ctx, "scene", o.ID, collab.LinkScenePerformer, p.ID, p.Name)
		}); err != nil {
			return err
		}
	}
	return nil
}

// PerformerImages searches for images whose path matches the provided performer name and tags the image with the performer.
func (tagger *Tagger) PerformerImages(ctx context.Context, p *models.Performer, paths []string, rw ImageQueryPerformerUpdater) error {
	t := getPerformerTaggers(p, tagger.Cache)

	for _, tt := range t {
		if err := tt.tagImages(ctx, paths, rw, func(o *models.Image) (bool, error) {
			if err := o.LoadPerformerIDs(ctx, rw); err != nil {
				return false, err
			}
			existing := o.PerformerIDs.List()

			if slices.Contains(existing, p.ID) {
				return false, nil
			}

			return tagger.Sink.AddMatch(ctx, "image", o.ID, collab.LinkImagePerformer, p.ID, p.Name)
		}); err != nil {
			return err
		}
	}
	return nil
}

// PerformerGalleries searches for galleries whose path matches the provided performer name and tags the gallery with the performer.
func (tagger *Tagger) PerformerGalleries(ctx context.Context, p *models.Performer, paths []string, rw GalleryQueryPerformerUpdater) error {
	t := getPerformerTaggers(p, tagger.Cache)

	for _, tt := range t {
		if err := tt.tagGalleries(ctx, paths, rw, func(o *models.Gallery) (bool, error) {
			if err := o.LoadPerformerIDs(ctx, rw); err != nil {
				return false, err
			}
			existing := o.PerformerIDs.List()

			if slices.Contains(existing, p.ID) {
				return false, nil
			}

			return tagger.Sink.AddMatch(ctx, "gallery", o.ID, collab.LinkKind("performer_ids"), p.ID, p.Name)
		}); err != nil {
			return err
		}
	}
	return nil
}
