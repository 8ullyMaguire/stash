package models

import "context"

// stash#1790 — the external-ID repository contract.
//
// Same reason as IssueReaderWriter: the API layer is written against this, not against the
// SQLite store, and the store proves it satisfies the interface with a compile-time
// assertion.
//
// DestroyForEntity and SweepOrphans ARE PART OF THE CONTRACT, NOT EXTRAS, and that is the
// only unusual thing about it. `external_ids.entity_id` has no foreign key — SQLite
// requires FK targets to be UNIQUE and there is no single unique column across five
// possible parents — so these two are the only things preventing the table from silently
// accumulating orphans. An interface that let a caller Record without knowing about them
// would be an interface that hands out the ability to leak.
type ExternalSourceReader interface {
	// FindSourceByName returns nil, nil for a missing source: a missing row is not a
	// failure, and the API layer turns nil into a 404.
	FindSourceByName(ctx context.Context, name string) (*ExternalSource, error)
	AllSources(ctx context.Context) ([]*ExternalSource, error)
}

type ExternalSourceWriter interface {
	// CreateSource registers a source, or returns the existing one with the same name.
	// Idempotent on name: a caller registering the same source on every scrape must not get
	// a UNIQUE violation, and must not get a second row either.
	CreateSource(ctx context.Context, input ExternalSourceInput) (*ExternalSource, error)
}

type ExternalIDReader interface {
	FindByID(ctx context.Context, id int) (*ExternalID, error)
	// FindByEntity is the panel/detail view: everything known about one entity.
	FindByEntity(ctx context.Context, entityType string, entityID int) ([]*ExternalID, error)
	// FindByExternalID is the scrape direction: which local entity carries this id.
	FindByExternalID(ctx context.Context, sourceID int, externalID string) ([]*ExternalID, error)
}

type ExternalIDWriter interface {
	// Record upserts on the full four-column identity
	// (entity_type, entity_id, source_id, external_id).
	Record(ctx context.Context, input ExternalIDInput) (*ExternalID, error)

	// DestroyForEntity must be called from EVERY entity destroy path. See the type comment.
	DestroyForEntity(ctx context.Context, entityType string, entityID int) error

	// SweepOrphans removes ids whose parent is gone. Returns how many it removed.
	SweepOrphans(ctx context.Context) (int, error)
}

// ExternalIDReaderWriter provides all external-id methods.
type ExternalIDReaderWriter interface {
	ExternalIDReader
	ExternalIDWriter
	ExternalSourceReader
	ExternalSourceWriter
}
