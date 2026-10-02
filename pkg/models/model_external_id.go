package models

import "time"

// stash#1790 — generalized external IDs.
//
// WHY THIS EXISTS NEXT TO StashID, RATHER THAN REPLACING IT.
//
// `StashID` is not a generic external identifier. It is named for one provider, its column
// is named `stash_id`, and the four tables that hold it (`scene_stash_ids`,
// `performer_stash_ids`, `studio_stash_ids`, `tag_stash_ids`) are byte-identical apart
// from the FK column. So "support external IDs from other sources" is not a new feature so
// much as the existing feature with the provider name removed.
//
// THE FOUR LEGACY TABLES ARE NOT MIGRATED, deliberately. They hold every provider id
// existing users have, rewriting them is large and irreversible, and its failure mode is
// silent data loss. Nothing yet needs the generality. The cost is stated plainly: the
// duplication is REDUCED, not removed. See docs/ISSUE-1790-spec.md §4.3.

// ExternalSource is a registry of places external ids can come from.
//
// THE REGISTRY IS THE POINT, not a lookup table. The legacy `endpoint` column is a bare
// varchar(255) that nothing validates, so a typo inserts cleanly, round-trips, and is then
// unreachable by every join that filters on the correct endpoint: a silent, permanent
// orphan. Here the typo fails at insert.
//
// `StashBox` marks a source as a StashDB instance. Every existing `*_stash_ids` row
// belongs to one, so this is what a future migration of those tables would match on.
type ExternalSource struct {
	ID        int       `db:"id"        json:"id"`
	Name      string    `db:"name"      json:"name"`
	URL       string    `db:"url"       json:"url"`
	StashBox  bool      `db:"stash_box" json:"stash_box"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// ExternalSourceInput creates a source.
type ExternalSourceInput struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Entity types that can carry external ids.
//
// A CONSTANT rather than a validation list, and that is a decision: `entity_type` in the
// database is deliberately unconstrained (see the schema test that inserts a
// `brand_new_entity_2099` row), because requiring a migration to add an entity is the
// generality this feature exists to provide. These are the types the CODE knows how to
// delete external ids for — see ExternalIDReader.DestroyForEntity.
const (
	ExternalIDEntityScene     = "scene"
	ExternalIDEntityPerformer = "performer"
	ExternalIDEntityStudio    = "studio"
	ExternalIDEntityTag       = "tag"
	ExternalIDEntityGallery   = "gallery"
)

// ExternalID is one identifier for one entity, from one source.
//
// IDENTITY IS (EntityType, EntityID, SourceID, ExternalID) — all four. `ExternalID` alone is
// obviously not unique; dropping `SourceID` from that tuple is the easiest mistake in this
// feature and makes two providers' ids on one entity collide. `ID` is this library's own
// row id and is not part of the external identity at all.
type ExternalID struct {
	ID         int       `db:"id"          json:"id"`
	EntityType string    `db:"entity_type" json:"entity_type"`
	EntityID   int       `db:"entity_id"   json:"entity_id"`
	SourceID   int       `db:"source_id"   json:"source_id"`
	ExternalID string    `db:"external_id" json:"external_id"`
	UpdatedAt  time.Time `db:"updated_at"  json:"updated_at"`
}

// ExternalIDInput records an external id. Used instead of the full struct because the
// library assigns SourceID (the caller may pass 0 meaning "resolve by source name") and
// UpdatedAt.
type ExternalIDInput struct {
	EntityType string     `json:"entity_type"`
	EntityID   int        `json:"entity_id"`
	SourceID   int        `json:"source_id"`
	ExternalID string     `json:"external_id"`
	Source     *string    `json:"source,omitempty"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}
