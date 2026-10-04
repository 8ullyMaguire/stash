// stash#2359 — Stash-Box parity: model types.
//
// Seven capabilities from #2359's umbrella list, and each one reuses an EXISTING idiom in this
// package rather than introducing a new one. The reuse is the point: a list of strings on an
// entity is `RelatedStrings` here (studios already carry `Aliases` and `URLs` as `RelatedStrings`),
// and a list of related entities is `RelatedIDs`. Inventing a parallel type for parity features
// would mean two implementations of the same read-modify-write semantics, one of which would be
// exercised only by the newer code.
//
// What these types are NOT: #2359 also lists scene and performer multiple URLs, tag
// descriptions, tag categories and tag stash_ids. Those were built upstream years earlier --
// migrations 47, 62, 36, 26 and 74 -- and need nothing here. See
// docs/plan/ISSUE-2359-PARITY.md for the evidence, because "the column I expected is absent" and
// "the feature is absent" are different claims and the second one cost a migration to discover.

package models

// StudioCode is one Stash-Box identifier for a studio. A studio has SEVERAL (a network code plus
// per-site codes), which is why this is a related list and not a `Studios.StudioCode string`
// column -- a column holds one, and the plurality is the feature.
type StudioCode struct {
	ID       int    `db:"id"        json:"id"`
	StudioID int    `db:"studio_id" json:"studio_id"`
	Code     string `db:"code"      json:"code"`
}

// BodyMark is a tattoo or a piercing. One type with a Kind discriminator rather than two types,
// because the two have identical shape (a location, a description, an optional image) and the
// only difference is a closed two-value literal. Two types would mean two stores, two destroy
// paths and two GraphQL types for no representational gain.
type BodyMark struct {
	ID          int `json:"id"`
	PerformerID int `db:"performer_id" json:"performer_id"`
	// Kind is "tattoo" or "piercing". Constrained by a CHECK in migration 125, so a caller cannot
	// construct an unreadable row; these constants are the Go-side spelling of that constraint.
	Kind        string  `db:"kind"        json:"kind"`
	Location    string  `db:"location"    json:"location"`
	Description *string `db:"description" json:"description"`
	ImagePath   *string `db:"image_path"  json:"image_path"`
}

const (
	BodyMarkKindTattoo   = "tattoo"
	BodyMarkKindPiercing = "piercing"
)

// Nationality is an entry in the controlled list behind #1922. A reference table rather than an
// enum because SQLite cannot ALTER a CHECK constraint, so an enum would freeze the list into
// every existing database with no way to extend it.
type Nationality struct {
	ID   int    `db:"id"   json:"id"`
	Name string `db:"name" json:"name"`
	// ISO 3166-1 alpha-2 where one applies. Nullable because a nationality is not always a country
	// ("Basque", "Kurdish" have no code in common use) and a non-null code would force a fake one.
	// NOT NULL here would make those two unselectable rather than error, so the test asserts the
	// nullability instead of trusting it.
	//
	// Codes are deliberately NOT unique: GB is shared by British, English, Scottish and Welsh, and
	// PH/IL/KR by language-vs-demonym pairs. One country, one code, several names.
	Code *string `db:"code" json:"code"`
}

// PerformerAliasOwnership records which performer an alias string is attributed to (#422, #2341).
//
// WHY THIS IS A SEPARATE TYPE AND NOT A FIELD ON Performer. `performer_aliases` has PRIMARY KEY
// (performer_id, alias), so the owner is ALREADY PART OF THAT KEY and the same string cannot
// belong to two performers. That constraint is precisely the defect #422/#2341 describe. Stash-Box
// stores an alias as an (alias, owner_performer) pair so one performer's "JD" can be recorded
// independently of another's.
//
// A nullable owner is what makes this additive: an alias with no attribution is still a plain
// alias, so every row that existed before migration 125 remains valid and no migration has to
// invent an owner.
type PerformerAliasOwnership struct {
	ID          int    `db:"id"           json:"id"`
	PerformerID int    `db:"performer_id" json:"performer_id"`
	Alias       string `db:"alias"        json:"alias"`

	// OwnerPerformerID is which PERFORMER an ambiguous alias string is attributed to (#2341): "JD"
	// can belong to two different people, and this says which row a match is evidence for.
	OwnerPerformerID *int `db:"owner_performer_id" json:"owner_performer_id"`

	// StudioID is which STUDIO an alias is associated with (#422): upstream's
	// `"aliases": {"Jane": "Brazzers"}`, where "" means no association. Orthogonal to the owner --
	// the same alias at a different studio is a different match, and the same studio can carry
	// different aliases of one performer. NULL is upstream's empty string, the common case, and
	// means "no studio association recorded" rather than "no owner".
	StudioID *int `db:"studio_id" json:"studio_id"`
}

// ScenePerformerAlias is a per-SCENE alias for a performer -- "Jane Doe as Jane" (#3825).
//
// Keyed on (scene_id, performer_id) rather than performer alone because a scene may credit one
// performer TWICE under two names (two segments, or a cameo alongside different billing) and both
// rows have to exist. That is the one thing which distinguishes this from Performer.Aliases, and
// it is why it lives on the scene.
type ScenePerformerAlias struct {
	ID          int    `db:"id"          json:"id"`
	SceneID     int    `db:"scene_id"    json:"scene_id"`
	PerformerID int    `db:"performer_id" json:"performer_id"`
	Alias       string `db:"alias"       json:"alias"`
}

// MergePerformersInput is the request for #1351.
//
// Merge is a MOVE, not a delete. The survivor keeps its identity and every reference to the source
// is repointed at it; the source row survives as a tombstone with MergedIntoID set. Deleting the
// source instead would destroy its scene credit and its images, and keeping two live rows would
// leave the duplicate this feature exists to remove.
type MergePerformersInput struct {
	// Source is the duplicate being merged away. It is retired, not deleted.
	Source int `json:"source"`
	// Destination is the surviving performer that absorbs the source's references.
	Destination int `json:"destination"`
}
