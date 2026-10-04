package sqlite

import (
	"context"
)

// Repository-level entry points for the batched studio counts.
//
// The resolvers in internal/api/resolver_model_studio.go need three things the per-studio helpers do
// not provide: the whole page in one statement, depth as part of the key, and an index that survives
// sparse results. countStudioChildren does the work; these are the named shapes the API layer asks
// for, so a resolver never has to name a SQL fragment.
//
// The depth parameter is NOT optional here. ui/v2.5/graphql/data/studio.graphql requests each count
// twice -- once plain and once as *_all with depth: -1 -- so a request legitimately contains two
// different depths for the same field. Callers must group by depth before calling.

// GetManySceneCount returns the scene count for each studio id, indexed by input position.
func (qb *StudioStore) GetManySceneCount(ctx context.Context, ids []int, depth *int) ([]int, error) {
	return countStudioChildren(ctx, ids, studioSceneCount, depth)
}

// GetManyImageCount returns the image count for each studio id, indexed by input position.
func (qb *StudioStore) GetManyImageCount(ctx context.Context, ids []int, depth *int) ([]int, error) {
	return countStudioChildren(ctx, ids, studioImageCount, depth)
}

// GetManyGalleryCount returns the gallery count for each studio id, indexed by input position.
func (qb *StudioStore) GetManyGalleryCount(ctx context.Context, ids []int, depth *int) ([]int, error) {
	return countStudioChildren(ctx, ids, studioGalleryCount, depth)
}

// GetManyGroupCount returns the group count for each studio id, indexed by input position.
func (qb *StudioStore) GetManyGroupCount(ctx context.Context, ids []int, depth *int) ([]int, error) {
	return countStudioChildren(ctx, ids, studioGroupCount, depth)
}

// GetManyPerformerCount returns the performer count for each studio id, indexed by input position.
func (qb *StudioStore) GetManyPerformerCount(ctx context.Context, ids []int, depth *int) ([]int, error) {
	return countStudioChildren(ctx, ids, studioPerformerCount, depth)
}

// GetManySceneMarkerCount returns the scene marker count for each studio id, indexed by input
// position.
func (qb *StudioStore) GetManySceneMarkerCount(ctx context.Context, ids []int, depth *int) ([]int, error) {
	return countStudioChildren(ctx, ids, studioSceneMarkerCount, depth)
}
