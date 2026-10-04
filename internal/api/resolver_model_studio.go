package api

import (
	"context"
	"fmt"

	"github.com/stashapp/stash/internal/api/loaders"
	"github.com/stashapp/stash/internal/api/urlbuilders"
	"github.com/stashapp/stash/pkg/models"
)

func (r *studioResolver) ImagePath(ctx context.Context, obj *models.Studio) (*string, error) {
	var hasImage bool
	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		var err error
		hasImage, err = r.repository.Studio.HasImage(ctx, obj.ID)
		return err
	}); err != nil {
		return nil, err
	}

	baseURL, _ := ctx.Value(BaseURLCtxKey).(string)
	imagePath := urlbuilders.NewStudioURLBuilder(baseURL, obj).GetStudioImageURL(hasImage)
	return &imagePath, nil
}

func (r *studioResolver) Aliases(ctx context.Context, obj *models.Studio) ([]string, error) {
	if !obj.Aliases.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadAliases(ctx, r.repository.Studio)
		}); err != nil {
			return nil, err
		}
	}

	return obj.Aliases.List(), nil
}

func (r *studioResolver) URL(ctx context.Context, obj *models.Studio) (*string, error) {
	if !obj.URLs.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadURLs(ctx, r.repository.Studio)
		}); err != nil {
			return nil, err
		}
	}

	urls := obj.URLs.List()
	if len(urls) == 0 {
		return nil, nil
	}

	return &urls[0], nil
}

func (r *studioResolver) Urls(ctx context.Context, obj *models.Studio) ([]string, error) {
	if !obj.URLs.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadURLs(ctx, r.repository.Studio)
		}); err != nil {
			return nil, err
		}
	}

	return obj.URLs.List(), nil
}

func (r *studioResolver) Tags(ctx context.Context, obj *models.Studio) (ret []*models.Tag, err error) {
	if !obj.TagIDs.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadTagIDs(ctx, r.repository.Studio)
		}); err != nil {
			return nil, err
		}
	}

	var errs []error
	ret, errs = loaders.From(ctx).TagByID.LoadAll(obj.TagIDs.List())
	return ret, firstError(errs)
}

// studioCount answers one studio count field for one studio.
//
// It goes through the per-request batcher when one is attached, which is the difference between one
// query per page and one query per row (docs/qcount.sh measured 219 statements for a 12-studio page
// before this). The fallback is deliberate rather than an error: a single-object query or a resolver
// reached without a page still has to work, and going through the same batched SQL with a one-element
// slice keeps exactly one implementation of each count.
//
// depth is passed through untouched. It is part of the batcher's cache key because the studios page
// asks for the same field at two depths in one request.
func (r *studioResolver) studioCount(ctx context.Context, kind studioCountKind, studioID int, depth *int) (int, error) {
	if b, ok := studioCountBatcherFrom(ctx); ok {
		return b.count(ctx, kind, studioID, depth)
	}

	var counts []int
	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		var err error
		counts, err = r.studioCounts(ctx, kind, []int{studioID}, depth)
		return err
	}); err != nil {
		return 0, err
	}
	if len(counts) == 0 {
		return 0, nil
	}
	return counts[0], nil
}

// studioCounts dispatches to the batched repository method for one count kind over a slice of studio
// ids. Every count in this file goes through here, so there is exactly one call site per SQL shape
// and the batched and single-studio paths cannot drift apart.
func (r *studioResolver) studioCounts(ctx context.Context, kind studioCountKind, ids []int, depth *int) ([]int, error) {
	switch kind {
	case studioSceneCountKind:
		return r.repository.Studio.GetManySceneCount(ctx, ids, depth)
	case studioImageCountKind:
		return r.repository.Studio.GetManyImageCount(ctx, ids, depth)
	case studioGalleryCountKind:
		return r.repository.Studio.GetManyGalleryCount(ctx, ids, depth)
	case studioGroupCountKind:
		return r.repository.Studio.GetManyGroupCount(ctx, ids, depth)
	case studioPerformerCountKind:
		return r.repository.Studio.GetManyPerformerCount(ctx, ids, depth)
	case studioSceneMarkerCountKind:
		return r.repository.Studio.GetManySceneMarkerCount(ctx, ids, depth)
	default:
		return nil, fmt.Errorf("unknown studio count kind %d", kind)
	}
}

func (r *studioResolver) SceneCount(ctx context.Context, obj *models.Studio, depth *int) (int, error) {
	return r.studioCount(ctx, studioSceneCountKind, obj.ID, depth)
}

func (r *studioResolver) ImageCount(ctx context.Context, obj *models.Studio, depth *int) (int, error) {
	return r.studioCount(ctx, studioImageCountKind, obj.ID, depth)
}

func (r *studioResolver) GalleryCount(ctx context.Context, obj *models.Studio, depth *int) (int, error) {
	return r.studioCount(ctx, studioGalleryCountKind, obj.ID, depth)
}

func (r *studioResolver) PerformerCount(ctx context.Context, obj *models.Studio, depth *int) (int, error) {
	return r.studioCount(ctx, studioPerformerCountKind, obj.ID, depth)
}

func (r *studioResolver) GroupCount(ctx context.Context, obj *models.Studio, depth *int) (int, error) {
	return r.studioCount(ctx, studioGroupCountKind, obj.ID, depth)
}

func (r *studioResolver) SceneMarkerCount(ctx context.Context, obj *models.Studio, depth *int) (int, error) {
	return r.studioCount(ctx, studioSceneMarkerCountKind, obj.ID, depth)
}

// deprecated
func (r *studioResolver) MovieCount(ctx context.Context, obj *models.Studio, depth *int) (ret int, err error) {
	return r.GroupCount(ctx, obj, depth)
}

func (r *studioResolver) OCounter(ctx context.Context, obj *models.Studio, depth *int) (ret int, err error) {
	var res_scene int
	var res_image int
	depthVal := 0
	if depth != nil {
		depthVal = *depth
	}
	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		res_scene, err = r.repository.Scene.OCountByStudioID(ctx, obj.ID, depthVal)
		if err != nil {
			return err
		}
		res_image, err = r.repository.Image.OCountByStudioID(ctx, obj.ID, depthVal)
		return err
	}); err != nil {
		return 0, err
	}
	return res_scene + res_image, nil
}

func (r *studioResolver) ParentStudio(ctx context.Context, obj *models.Studio) (ret *models.Studio, err error) {
	if obj.ParentID == nil {
		return nil, nil
	}

	return loaders.From(ctx).StudioByID.Load(*obj.ParentID)
}

func (r *studioResolver) ChildStudios(ctx context.Context, obj *models.Studio) (ret []*models.Studio, err error) {
	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.Studio.FindChildren(ctx, obj.ID)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *studioResolver) StashIds(ctx context.Context, obj *models.Studio) ([]*models.StashID, error) {
	if !obj.StashIDs.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadStashIDs(ctx, r.repository.Studio)
		}); err != nil {
			return nil, err
		}
	}

	return stashIDsSliceToPtrSlice(obj.StashIDs.List()), nil
}

func (r *studioResolver) Rating100(ctx context.Context, obj *models.Studio) (*int, error) {
	return obj.Rating, nil
}

func (r *studioResolver) Groups(ctx context.Context, obj *models.Studio) (ret []*models.Group, err error) {
	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.Group.FindByStudioID(ctx, obj.ID)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *studioResolver) CustomFields(ctx context.Context, obj *models.Studio) (map[string]interface{}, error) {
	m, err := loaders.From(ctx).StudioCustomFields.Load(obj.ID)
	if err != nil {
		return nil, err
	}

	if m == nil {
		return make(map[string]interface{}), nil
	}

	return m, nil
}

// deprecated
func (r *studioResolver) Movies(ctx context.Context, obj *models.Studio) (ret []*models.Group, err error) {
	return r.Groups(ctx, obj)
}
