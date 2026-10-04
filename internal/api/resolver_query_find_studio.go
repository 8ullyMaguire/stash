package api

import (
	"context"
	"strconv"

	"github.com/stashapp/stash/pkg/models"
)

func (r *queryResolver) FindStudio(ctx context.Context, id string) (ret *models.Studio, err error) {
	idInt, err := strconv.Atoi(id)
	if err != nil {
		return nil, err
	}

	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		var err error
		ret, err = r.repository.Studio.Find(ctx, idInt)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

// FindStudios returns a page of studios.
//
// This is the only place that knows the whole page, so it is where the per-studio count batcher is
// seeded (see resolver_model_studio_count.go). gqlgen calls each count field resolver once per studio
// and cannot tell the last call from the others, so without the page the resolvers have nothing to
// batch against and each one would issue its own query -- the N+1 docs/qcount.sh measured at 219
// statements for 12 studios.
//
// The batcher is attached to the returned context, not to r, because it must not outlive the request.
func (r *queryResolver) FindStudios(ctx context.Context, studioFilter *models.StudioFilterType, filter *models.FindFilterType, ids []string) (ret *FindStudiosResultType, err error) {
	idInts, err := handleIDList(ids, "ids")
	if err != nil {
		return nil, err
	}

	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		var studios []*models.Studio
		var err error
		var total int

		if len(idInts) > 0 {
			studios, err = r.repository.Studio.FindMany(ctx, idInts)
			total = len(studios)
		} else {
			studios, total, err = r.repository.Studio.Query(ctx, studioFilter, filter)
		}
		if err != nil {
			return err
		}

		ret = &FindStudiosResultType{
			Count:   total,
			Studios: studios,
		}

		return nil
	}); err != nil {
		return nil, err
	}

	// Seed the batcher with the page, in page order, so a count resolver's result can be indexed back
	// to the studio that asked for it.
	if b, ok := studioCountBatcherFrom(ctx); ok && ret != nil {
		pageIDs := make([]int, 0, len(ret.Studios))
		for _, s := range ret.Studios {
			if s != nil {
				pageIDs = append(pageIDs, s.ID)
			}
		}
		b.setPage(pageIDs)
	}

	return ret, nil
}

// AllStudios also returns a list, so it seeds the same batcher. It is used by tag and performer detail
// pages that ask for studio counts.
func (r *queryResolver) AllStudios(ctx context.Context) (ret []*models.Studio, err error) {
	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.Studio.All(ctx)
		return err
	}); err != nil {
		return nil, err
	}

	if b, ok := studioCountBatcherFrom(ctx); ok {
		allIDs := make([]int, 0, len(ret))
		for _, s := range ret {
			if s != nil {
				allIDs = append(allIDs, s.ID)
			}
		}
		b.setPage(allIDs)
	}

	return ret, nil
}
