package models

import "context"

// StudioGetter provides methods to get studios by ID.
type StudioGetter interface {
	// TODO - rename this to Find and remove existing method
	FindMany(ctx context.Context, ids []int) ([]*Studio, error)
	Find(ctx context.Context, id int) (*Studio, error)
}

// StudioFinder provides methods to find studios.
type StudioFinder interface {
	StudioGetter
	FindChildren(ctx context.Context, id int) ([]*Studio, error)
	FindBySceneID(ctx context.Context, sceneID int) (*Studio, error)
	FindByStashID(ctx context.Context, stashID StashID) ([]*Studio, error)
	FindByStashIDStatus(ctx context.Context, hasStashID bool, stashboxEndpoint string) ([]*Studio, error)
	FindByName(ctx context.Context, name string, nocase bool) (*Studio, error)
}

// StudioQueryer provides methods to query studios.
type StudioQueryer interface {
	Query(ctx context.Context, studioFilter *StudioFilterType, findFilter *FindFilterType) ([]*Studio, int, error)
	QueryCount(ctx context.Context, studioFilter *StudioFilterType, findFilter *FindFilterType) (int, error)
}

type StudioAutoTagQueryer interface {
	StudioQueryer
	AliasLoader

	// TODO - this interface is temporary until the filter schema can fully
	// support the query needed
	QueryForAutoTag(ctx context.Context, words []string) ([]*Studio, error)
}

// StudioCounter provides methods to count studios.
type StudioCounter interface {
	Count(ctx context.Context) (int, error)
	CountByTagID(ctx context.Context, tagID int) (int, error)
}

// StudioChildCounter provides BATCHED counts of a studio's children.
//
// These exist because the per-studio CountBy* helpers are called once per object by gqlgen, which
// makes a list query issue one query per field per row. docs/qcount.sh measured that at 219 SQL
// statements for a 12-studio page (18.2 per studio).
//
// depth is part of every signature and is NOT optional: the studios page asks for each count twice,
// once plain and once as *_all with depth: -1 (ui/v2.5/graphql/data/studio.graphql), so one request
// legitimately carries two depths for the same field. Batching across depths would return the wrong
// number for one of them.
//
// Each returns a slice indexed by input position, with 0 for a studio whose child set is empty.
type StudioChildCounter interface {
	GetManySceneCount(ctx context.Context, ids []int, depth *int) ([]int, error)
	GetManyImageCount(ctx context.Context, ids []int, depth *int) ([]int, error)
	GetManyGalleryCount(ctx context.Context, ids []int, depth *int) ([]int, error)
	GetManyGroupCount(ctx context.Context, ids []int, depth *int) ([]int, error)
	GetManyPerformerCount(ctx context.Context, ids []int, depth *int) ([]int, error)
	GetManySceneMarkerCount(ctx context.Context, ids []int, depth *int) ([]int, error)
}

// StudioCreator provides methods to create studios.
type StudioCreator interface {
	Create(ctx context.Context, newStudio *CreateStudioInput) error
}

// StudioUpdater provides methods to update studios.
type StudioUpdater interface {
	Update(ctx context.Context, updatedStudio *UpdateStudioInput) error
	UpdatePartial(ctx context.Context, updatedStudio StudioPartial) (*Studio, error)
	UpdateImage(ctx context.Context, studioID int, image []byte) error
}

// StudioDestroyer provides methods to destroy studios.
type StudioDestroyer interface {
	Destroy(ctx context.Context, id int) error
}

type StudioFinderCreator interface {
	StudioFinder
	StudioCreator
}

type StudioCreatorUpdater interface {
	StudioCreator
	StudioUpdater
}

// StudioReader provides all methods to read studios.
type StudioReader interface {
	StudioFinder
	StudioQueryer
	StudioAutoTagQueryer
	StudioCounter
	StudioChildCounter

	AliasLoader
	StashIDLoader
	TagIDLoader
	URLLoader
	CodeLoader

	CustomFieldsReader

	All(ctx context.Context) ([]*Studio, error)
	GetImage(ctx context.Context, studioID int) ([]byte, error)
	HasImage(ctx context.Context, studioID int) (bool, error)
}

// StudioWriter provides all methods to modify studios.
type StudioWriter interface {
	StudioCreator
	StudioUpdater
	StudioDestroyer
}

// StudioReaderWriter provides all studio methods.
type StudioReaderWriter interface {
	StudioReader
	StudioWriter
}
