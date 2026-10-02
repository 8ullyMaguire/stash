package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/stashapp/stash/pkg/models"
)

// stash#1790 — the external-ID store.
//
// READ THIS BEFORE CHANGING ANYTHING HERE: the polymorphic `entity_id` column has NO foreign
// key, because SQLite requires FK targets to be UNIQUE and there is no single unique column
// across five possible parents (spec §4.2). Two methods below exist purely to pay for that:
//
//	DestroyForEntity  called from every entity delete path, so deleting a scene takes its
//	                  external ids with it
//	SweepOrphans      for rows left behind by a partial failure or an older library
//
// Neither is an optimisation. Without them this table leaks, and it leaks SILENTLY: a
// leftover row is invisible, takes up space, and is reported by no query. If you are adding
// an entity type, you must call DestroyForEntity from its destroy path AND add it to the
// table in destroyExternalIDs.

// Compile-time proof the store satisfies the contract the API layer is written against.
// Without this, a field typed as the interface compiles against any type and the mismatch
// surfaces as a nil-pointer panic at the first request rather than at build time.
var _ models.ExternalIDReaderWriter = (*ExternalIDStore)(nil)

// ExternalIDStore takes no dependencies, exactly like IssueStore: an external id names an
// entity by type and id and nothing else, so there is no other store to hold. Note the
// consequence, which is the whole point -- adding a new entity type needs NO change here.
type ExternalIDStore struct{}

func NewExternalIDStore() *ExternalIDStore {
	return &ExternalIDStore{}
}

// ---------------------------------------------------------------------------
// sources
// ---------------------------------------------------------------------------

// CreateSource registers a source, or returns the existing one with the same name.
//
// IDEMPOTENT ON PURPOSE, and the ON CONFLICT is load-bearing rather than a convenience. A
// caller registering "stashdb" on every scrape must not get a UNIQUE violation on the
// second one — and must not get a SECOND source row either, because `endpoint` -> source
// resolution would then be ambiguous, which is the exact ambiguity the registry exists to
// remove (the schema test refuses a duplicate source name).
//
// THE URL IS NOT OVERWRITTEN on conflict. Two callers naming the same source with different
// URLs is a disagreement about configuration, and silently preferring the first writer hides
// it; the caller that disagrees should see its value not take effect.
func (qb *ExternalIDStore) CreateSource(ctx context.Context, input models.ExternalSourceInput) (*models.ExternalSource, error) {
	if input.Name == "" {
		return nil, fmt.Errorf("creating external source: name must not be empty")
	}
	if input.URL == "" {
		return nil, fmt.Errorf("creating external source %q: url must not be empty", input.Name)
	}

	now := time.Now()
	res, err := dbWrapper.Exec(ctx, `
		INSERT INTO external_sources (name, url, stash_box, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (name) DO NOTHING`,
		input.Name, input.URL, false, Timestamp{Timestamp: now}, Timestamp{Timestamp: now})
	if err != nil {
		return nil, fmt.Errorf("creating external source %q: %w", input.Name, err)
	}

	// THE ROW IS READ BACK BY NAME, ALWAYS -- LastInsertId CANNOT BE TRUSTED TO TELL
	// INSERT FROM CONFLICT.
	//
	// The obvious version checks `LastInsertId() == 0` to detect the conflict path, and
	// that is WRONG: on `ON CONFLICT DO NOTHING` SQLite returns the rowid of the LAST
	// SUCCESSFUL INSERT ON THIS CONNECTION, not zero. So in a live server, where a
	// different insert has just run, a conflicting CreateSource reports a plausible
	// non-zero id belonging to an unrelated row -- and then constructs a source from
	// ITS OWN input, so the caller gets an id that points at something else entirely.
	//
	// That is the worst shape of bug: every value is individually reasonable and the
	// result is wrong. Found by a test asserting the FIRST url survives a second
	// CreateSource with a different one, which failed with the SECOND url -- the tell
	// that the conflict path was taken while the code believed it had inserted.
	existing, err := qb.FindSourceByName(ctx, input.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	// No row by that name afterwards means the insert really did happen and something
	// else removed it; report the insert's own result rather than a fabricated id.
	if id, idErr := res.LastInsertId(); idErr == nil {
		return &models.ExternalSource{
			ID:        int(id),
			Name:      input.Name,
			URL:       input.URL,
			StashBox:  false,
			CreatedAt: now,
			UpdatedAt: now,
		}, nil
	}
	return nil, fmt.Errorf("creating external source %q: inserted but not readable afterwards",
		input.Name)
}

// FindSourceByName returns the source, or nil if there is none.
//
// `nil, nil` for a missing row, matching every other store here: a missing row is not a
// failure, and the API layer turns nil into a 404.
func (qb *ExternalIDStore) FindSourceByName(ctx context.Context, name string) (*models.ExternalSource, error) {
	var row externalSourceRow
	err := dbWrapper.Get(ctx, &row, `
		SELECT id, name, url, stash_box, created_at, updated_at
		FROM external_sources WHERE name = ?`, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding external source %q: %w", name, err)
	}
	return row.resolve(), nil
}

func (qb *ExternalIDStore) AllSources(ctx context.Context) ([]*models.ExternalSource, error) {
	var rows []externalSourceRow
	if err := dbWrapper.Select(ctx, &rows, `
		SELECT id, name, url, stash_box, created_at, updated_at
		FROM external_sources ORDER BY name`); err != nil {
		return nil, fmt.Errorf("listing external sources: %w", err)
	}
	var ret []*models.ExternalSource
	for _, r := range rows {
		ret = append(ret, r.resolve())
	}
	return ret, nil
}

// ---------------------------------------------------------------------------
// ids
// ---------------------------------------------------------------------------

// Record writes an external id, upserting on the full four-column identity.
//
// THE CONFLICT TARGET NAMES THE SAME EXPRESSION AS THE UNIQUE INDEX in migration 121. If the
// two ever drift apart SQLite refuses the statement outright ("ON CONFLICT clause does not
// match any PRIMARY KEY or UNIQUE constraint") rather than quietly inserting duplicates --
// which is the right outcome, and the reason the expression is written out here rather than
// hidden in a helper that could be edited in one place only.
//
// SOURCE_ID 0 MEANS "RESOLVE BY SOURCE NAME", because a caller that knows the source by its
// display name should not have to look up an int first. A caller that passes neither is an
// error rather than a row with source_id 0, which the foreign key would refuse anyway --
// but the explicit message is worth more than the FK's.
func (qb *ExternalIDStore) Record(ctx context.Context, input models.ExternalIDInput) (*models.ExternalID, error) {
	if input.EntityID == 0 {
		return nil, fmt.Errorf("recording external id: entity_id must not be zero")
	}
	if input.ExternalID == "" {
		return nil, fmt.Errorf("recording external id: external_id must not be empty")
	}
	if input.EntityType == "" {
		return nil, fmt.Errorf("recording external id: entity_type must not be empty")
	}

	sourceID := input.SourceID
	if sourceID == 0 {
		if input.Source == nil || *input.Source == "" {
			return nil, fmt.Errorf(
				"recording external id for %s/%d: neither source_id nor source was given",
				input.EntityType, input.EntityID)
		}
		src, err := qb.FindSourceByName(ctx, *input.Source)
		if err != nil {
			return nil, err
		}
		if src == nil {
			// A MESSAGE, not the raw foreign-key error. "no such source: mythicbox" tells
			// the caller what to fix; "FOREIGN KEY constraint failed" does not.
			return nil, fmt.Errorf("recording external id: no such source: %s", *input.Source)
		}
		sourceID = src.ID
	}

	updatedAt := time.Now()
	if input.UpdatedAt != nil {
		updatedAt = *input.UpdatedAt
	}

	// The result is DISCARDED on purpose. LastInsertId cannot distinguish an insert from a
	// conflict on this path, so the row is read back by identity below rather than trusted.
	_, err := dbWrapper.Exec(ctx, `
		INSERT INTO external_ids (entity_type, entity_id, source_id, external_id, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (entity_type, entity_id, source_id, external_id)
		DO UPDATE SET updated_at = excluded.updated_at`,
		input.EntityType, input.EntityID, sourceID, input.ExternalID,
		Timestamp{Timestamp: updatedAt})
	if err != nil {
		return nil, fmt.Errorf("recording external id for %s/%d: %w",
			input.EntityType, input.EntityID, err)
	}

	// READ BACK BY IDENTITY, ALWAYS -- NOT BY LastInsertId.
	//
	// Same SQLite behaviour as CreateSource: on the conflict path LastInsertId returns the
	// rowid of the last successful insert on this connection rather than 0, so trusting it
	// makes a re-record return some OTHER row's id. Reading by the four identity columns
	// costs one query and cannot return a row that is not the one just written.
	var row externalIDRow
	if err := dbWrapper.Get(ctx, &row, `
		SELECT id, entity_type, entity_id, source_id, external_id, updated_at
		FROM external_ids
		WHERE entity_type = ? AND entity_id = ? AND source_id = ? AND external_id = ?`,
		input.EntityType, input.EntityID, sourceID, input.ExternalID); err != nil {
		return nil, fmt.Errorf("reading back external id for %s/%d: %w",
			input.EntityType, input.EntityID, err)
	}
	return row.resolve(), nil
}

func (qb *ExternalIDStore) find(ctx context.Context, id int) (*models.ExternalID, error) {
	var row externalIDRow
	err := dbWrapper.Get(ctx, &row, `
		SELECT id, entity_type, entity_id, source_id, external_id, updated_at
		FROM external_ids WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding external id %d: %w", id, err)
	}
	return row.resolve(), nil
}

func (qb *ExternalIDStore) FindByID(ctx context.Context, id int) (*models.ExternalID, error) {
	return qb.find(ctx, id)
}

func (qb *ExternalIDStore) FindByEntity(ctx context.Context, entityType string, entityID int) ([]*models.ExternalID, error) {
	var rows []externalIDRow
	if err := dbWrapper.Select(ctx, &rows, `
		SELECT id, entity_type, entity_id, source_id, external_id, updated_at
		FROM external_ids
		WHERE entity_type = ? AND entity_id = ?
		ORDER BY source_id, external_id`,
		entityType, entityID); err != nil {
		return nil, fmt.Errorf("finding external ids for %s/%d: %w", entityType, entityID, err)
	}
	return resolveExternalIDs(rows), nil
}

// FindByExternalID is the lookup a metadata scrape resolves against: "which local entity
// carries this id from this source".
func (qb *ExternalIDStore) FindByExternalID(ctx context.Context, sourceID int, externalID string) ([]*models.ExternalID, error) {
	var rows []externalIDRow
	if err := dbWrapper.Select(ctx, &rows, `
		SELECT id, entity_type, entity_id, source_id, external_id, updated_at
		FROM external_ids
		WHERE source_id = ? AND external_id = ?
		ORDER BY entity_type, entity_id`,
		sourceID, externalID); err != nil {
		return nil, fmt.Errorf("finding entities with external id %q from source %d: %w",
			externalID, sourceID, err)
	}
	return resolveExternalIDs(rows), nil
}

func resolveExternalIDs(rows []externalIDRow) []*models.ExternalID {
	out := make([]*models.ExternalID, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.resolve())
	}
	return out
}

// DestroyForEntity removes an entity's external ids.
//
// NOT OPTIONAL. There is no foreign key on entity_id (spec §4.2), so this is the only thing
// that keeps the table from accumulating orphans, and it must be called from every entity
// destroy path. The test for this is T4 and it is table-driven over the entity types
// precisely because the failure mode -- a new entity type wired up without the call -- is
// invisible until the table is full of rows nobody can account for.
func (qb *ExternalIDStore) DestroyForEntity(ctx context.Context, entityType string, entityID int) error {
	if !IsKnownExternalIDEntityType(entityType) {
		return fmt.Errorf("destroying external ids for %s/%d: unknown entity type %q "+
			"(add it to externalIDEntityTables so its ids can be swept)",
			entityType, entityID, entityType)
	}
	if _, err := dbWrapper.Exec(ctx,
		`DELETE FROM external_ids WHERE entity_type = ? AND entity_id = ?`,
		entityType, entityID); err != nil {
		return fmt.Errorf("destroying external ids for %s/%d: %w", entityType, entityID, err)
	}
	return nil
}

// SweepOrphans removes external ids whose parent entity no longer exists.
//
// FOR THE ROWS NO DELETE PATH WILL EVER CATCH: an entity removed by a path that predates
// this feature, a library restored from a partial backup, a crash between two statements.
//
// IT MUST NOT DELETE ANYTHING ELSE. The obvious wrong implementation is a blanket
// `DELETE FROM external_ids` on the theory that a full rebuild repopulates it -- which is
// true only on a fresh database and destroys every id on a live one. So this runs one
// NOT EXISTS per entity type rather than truncating, and T5 asserts both that it removes an
// orphan and that it leaves a real row alone.
//
// THE EMPTY CASE IS AN ERROR, not a no-op. An unknown entity_type means a caller typo'd a
// type name, and silently sweeping nothing hides it; `DestroyForEntity` has the same guard
// for the same reason.
func (qb *ExternalIDStore) SweepOrphans(ctx context.Context) (int, error) {
	var total int64
	for _, t := range externalIDEntityTables {
		res, err := dbWrapper.Exec(ctx, fmt.Sprintf(`
			DELETE FROM external_ids
			WHERE entity_type = ?
			  AND NOT EXISTS (SELECT 1 FROM %s WHERE id = external_ids.entity_id)`,
			t.table), t.entityType)
		if err != nil {
			return int(total), fmt.Errorf("sweeping orphan external ids for %s: %w", t.entityType, err)
		}
		if n, err := res.RowsAffected(); err == nil {
			total += n
		}
	}
	return int(total), nil
}

// externalIDEntityTypes maps an entity type to the table its ids live in.
//
// A MAP RATHER THAN A CONSTANT LIST, because SweepOrphans needs both halves and a list of
// just the type names would mean the mapping from type to table exists in two places.
var externalIDEntityTables = []struct {
	entityType string
	table      string
}{
	{models.ExternalIDEntityScene, "scenes"},
	{models.ExternalIDEntityPerformer, "performers"},
	{models.ExternalIDEntityStudio, "studios"},
	{models.ExternalIDEntityTag, "tags"},
	{models.ExternalIDEntityGallery, "galleries"},
}

// IsKnownExternalIDEntityType reports whether this build knows how to delete an entity's
// external ids. A caller wiring a NEW entity type asks this, and a "no" is the signal to add
// the row above -- which is the answer to "what happens if I add an entity".
func IsKnownExternalIDEntityType(entityType string) bool {
	for _, t := range externalIDEntityTables {
		if t.entityType == entityType {
			return true
		}
	}
	return false
}

// destroyExternalIDs is the helper every entity store's destroy path calls.
//
// A FUNCTION RATHER THAN A CALL SITE PER STORE, because the failure this guards against is
// forgetting: four stores each with a Destroy method is four chances to add an entity and
// not wire this in, and the omission is invisible in review and silent in production. The
// cost of the indirection is that a reader of one store cannot see the call -- so the
// comment here is the only place that says it happens, and that is why it is long.
func (qb *ExternalIDStore) destroyExternalIDs(ctx context.Context, entityType string, entityID int) error {
	if !IsKnownExternalIDEntityType(entityType) {
		// Refusing beats silently doing nothing. An unknown type here means either a typo or
		// a new entity that was never added to externalIDEntityTables, and in both cases
		// the ids would be left behind with no error to explain it.
		return fmt.Errorf("destroying external ids for %s/%d: unknown entity type %q "+
			"(add it to externalIDEntityTables so its ids can be swept)",
			entityType, entityID, entityType)
	}
	return qb.DestroyForEntity(ctx, entityType, entityID)
}

// ---------------------------------------------------------------------------
// rows
// ---------------------------------------------------------------------------

type externalSourceRow struct {
	ID        int       `db:"id"`
	Name      string    `db:"name"`
	URL       string    `db:"url"`
	StashBox  bool      `db:"stash_box"`
	CreatedAt Timestamp `db:"created_at"`
	UpdatedAt Timestamp `db:"updated_at"`
}

func (r *externalSourceRow) resolve() *models.ExternalSource {
	return &models.ExternalSource{
		ID:        r.ID,
		Name:      r.Name,
		URL:       r.URL,
		StashBox:  r.StashBox,
		CreatedAt: r.CreatedAt.Timestamp,
		UpdatedAt: r.UpdatedAt.Timestamp,
	}
}

type externalIDRow struct {
	ID         int       `db:"id"`
	EntityType string    `db:"entity_type"`
	EntityID   int       `db:"entity_id"`
	SourceID   int       `db:"source_id"`
	ExternalID string    `db:"external_id"`
	UpdatedAt  Timestamp `db:"updated_at"`
}

func (r *externalIDRow) resolve() *models.ExternalID {
	return &models.ExternalID{
		ID:         r.ID,
		EntityType: r.EntityType,
		EntityID:   r.EntityID,
		SourceID:   r.SourceID,
		ExternalID: r.ExternalID,
		UpdatedAt:  r.UpdatedAt.Timestamp,
	}
}
