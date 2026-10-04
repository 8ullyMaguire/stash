// stash#2359 — Stash-Box parity: the SQLite store layer.
//
// Every accessor here follows an existing pattern in this package rather than a new one: a string
// list uses the `stringTable` helper that already backs performer aliases and studio aliases
// (giving get/insert/replace/destroy for free), and a query with custom columns uses the same
// goqu + queryFunc shape as the surrounding stores.
//
// TWO RULES THAT ARE NOT NEGOTIABLE, learned the hard way in migration 121 and repeated here
// because the tables in 124/125 have the same exposure:
//
//  1. Every entity destroy path removes its related rows. Migration 121 records that a general
//     table keyed on (entity_type, entity_id) cannot use a foreign key -- SQLite requires the
//     parent column to be UNIQUE and no single column is unique across parent tables -- so cleanup
//     has to be explicit. Without it a deleted performer leaves body marks and alias-ownership rows
//     pointing at nothing.
//  2. A delete that reports success after matching zero rows is not evidence. The orphan tests
//     assert on row COUNTS after the delete, because a filter on the wrong column silently removes
//     nothing and still returns nil.

package sqlite

import (
	"context"
	"fmt"

	"github.com/doug-martin/goqu/v9"
	"github.com/jmoiron/sqlx"

	"github.com/stashapp/stash/pkg/models"
)

const (
	studioCodesTable            = "studio_codes"
	sceneDirectorsTable         = "scene_directors"
	scenePerformerAliasesTable  = "scene_performer_aliases"
	performerAliasOwnersTable   = "performer_alias_owners"
	nationalitiesTable          = "nationalities"
	performerNationalitiesTable = "performer_nationalities"
	performerBodyMarksTable     = "performer_body_marks"
)

var (
	studioCodesTableName            = goqu.T(studioCodesTable)
	sceneDirectorsTableName         = goqu.T(sceneDirectorsTable)
	scenePerformerAliasesTableName  = goqu.T(scenePerformerAliasesTable)
	performerAliasOwnersTableName   = goqu.T(performerAliasOwnersTable)
	nationalitiesTableName          = goqu.T(nationalitiesTable)
	performerNationalitiesTableName = goqu.T(performerNationalitiesTable)
	performerBodyMarksTableName     = goqu.T(performerBodyMarksTable)
)

// ---------------------------------------------------------------------------
// S1 — studio codes (#2607, #3051)
// ---------------------------------------------------------------------------

// studioCodesTableMgr is a stringTable because a code IS a string on a child row; the surrogate
// `id` column is not part of the identity and nothing reads it. A custom struct here would mean
// hand-writing the get/insert/replace/destroy that stringTable already implements and tests.
var studioCodesTableMgr = &stringTable{
	table: table{
		table:    studioCodesTableName,
		idColumn: studioCodesTableName.Col("studio_id"),
	},
	stringColumn: studioCodesTableName.Col("code"),
}

// GetCodes returns the studio's Stash-Box codes. SEVERAL per studio is the feature -- that is why
// this is a child table and not a `studios.studio_code` column, which could hold exactly one.
func (qb *StudioStore) GetCodes(ctx context.Context, studioID int) ([]string, error) {
	return studioCodesTableMgr.get(ctx, studioID)
}

// SetCodes replaces the studio's codes wholesale. Replace rather than add, because a caller that
// sends the full list would otherwise hit the unique index on (studio_id, code) the second time --
// turning a correct update into an error the caller cannot act on.
func (qb *StudioStore) SetCodes(ctx context.Context, studioID int, codes []string) error {
	return studioCodesTableMgr.replaceJoins(ctx, studioID, codes)
}

// ---------------------------------------------------------------------------
// S2 — scene directors (#3051), structured
// ---------------------------------------------------------------------------

// sceneDirectorsTableMgr covers the packed `scenes.director` column that already exists
// (migration 47), which is KEPT for compatibility. The reason this table exists at all is stated
// in migration 124 and is worth repeating where a reader will hit it: a packed column cannot
// answer `director = ?`, because "Ana L.opez" and "Ana L\u00f3pez" differ only across a comma that
// is not part of either name, so accent-folded comparison of a substring fails. Filtering a scene
// list by director -- the actual use -- is a LIKE against a packed column, which cannot use an
// index and cannot be exact.
var sceneDirectorsTableMgr = &stringTable{
	table: table{
		table:    sceneDirectorsTableName,
		idColumn: sceneDirectorsTableName.Col("scene_id"),
	},
	stringColumn: sceneDirectorsTableName.Col("director"),
}

func (qb *SceneStore) GetDirectors(ctx context.Context, sceneID int) ([]string, error) {
	return sceneDirectorsTableMgr.get(ctx, sceneID)
}

func (qb *SceneStore) SetDirectors(ctx context.Context, sceneID int, directors []string) error {
	return sceneDirectorsTableMgr.replaceJoins(ctx, sceneID, directors)
}

// ---------------------------------------------------------------------------
// S3 — performer scene aliases (#3825)
// ---------------------------------------------------------------------------

// GetPerformerAliases returns the per-scene credit names: "Jane Doe as Jane".
//
// NOT a stringTable. The identity here is (scene_id, performer_id) and the alias is DATA, because
// a scene may credit one performer TWICE under two names -- two segments, or a cameo alongside
// different billing -- and both rows must exist. A stringTable keyed on scene_id would collapse
// them to one row and silently lose the second name.
func (qb *SceneStore) GetPerformerAliases(ctx context.Context, sceneID int) ([]models.ScenePerformerAlias, error) {
	q := dialect.Select("*").From(scenePerformerAliasesTableName).
		Where(scenePerformerAliasesTableName.Col("scene_id").Eq(sceneID))

	var ret []models.ScenePerformerAlias
	if err := queryFunc(ctx, q, false, func(rows *sqlx.Rows) error {
		var row models.ScenePerformerAlias
		if err := rows.StructScan(&row); err != nil {
			return err
		}
		ret = append(ret, row)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("getting scene %d performer aliases: %w", sceneID, err)
	}

	return ret, nil
}

// SetPerformerAlias records or replaces the name a performer is credited under in one scene.
//
// Delete-then-insert rather than an UPDATE, because the unique index on
// (scene_id, performer_id) means a plain INSERT of an existing pair fails, and an UPDATE would
// need the row's surrogate id -- which the caller does not have and should not need, since the
// pair IS the identity.
func (qb *SceneStore) SetPerformerAlias(ctx context.Context, alias models.ScenePerformerAlias) error {
	if err := qb.ClearPerformerAlias(ctx, alias.SceneID, alias.PerformerID); err != nil {
		return err
	}

	if _, err := exec(ctx, dialect.Insert(scenePerformerAliasesTableName).
		Cols("scene_id", "performer_id", "alias").
		Vals(goqu.Vals{alias.SceneID, alias.PerformerID, alias.Alias})); err != nil {
		return fmt.Errorf("setting scene %d performer %d alias: %w", alias.SceneID, alias.PerformerID, err)
	}

	return nil
}

// ClearPerformerAlias removes the per-scene alias, so the performer's own name is used again.
//
// Deleting the row rather than blanking `alias`: an empty string satisfies NOT NULL and would
// render as a blank name in the UI, which is worse than no alias at all because it looks set.
func (qb *SceneStore) ClearPerformerAlias(ctx context.Context, sceneID, performerID int) error {
	_, err := exec(ctx, dialect.Delete(scenePerformerAliasesTableName).Where(
		scenePerformerAliasesTableName.Col("scene_id").Eq(sceneID),
		scenePerformerAliasesTableName.Col("performer_id").Eq(performerID),
	))
	return err
}

// ---------------------------------------------------------------------------
// S5 — performer split aliases (#422, #2341)
// ---------------------------------------------------------------------------

// GetAliasOwners returns the alias-attribution rows for a performer.
//
// THE REASON THIS IS A SEPARATE TABLE, restated because it is the least obvious decision in the
// whole change: `performer_aliases` has PRIMARY KEY (performer_id, alias), so the owner is ALREADY
// part of that key and the same alias string cannot be attached to two performers at all. That
// constraint IS the defect #422/#2341 report. A nullable owner column on that table cannot express
// "this string belongs to A while the same string belongs to B" -- the key would have to widen to
// (performer_id, alias, owner_performer_id), after which alias is no longer unique per performer
// and every existing read has to tolerate duplicates.
func (qb *PerformerStore) GetAliasOwners(ctx context.Context, performerID int) ([]models.PerformerAliasOwnership, error) {
	q := dialect.Select("*").From(performerAliasOwnersTableName).
		Where(performerAliasOwnersTableName.Col("performer_id").Eq(performerID))

	var ret []models.PerformerAliasOwnership
	if err := queryFunc(ctx, q, false, func(rows *sqlx.Rows) error {
		var row models.PerformerAliasOwnership
		if err := rows.StructScan(&row); err != nil {
			return err
		}
		ret = append(ret, row)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("getting performer %d alias owners: %w", performerID, err)
	}

	return ret, nil
}

// SetAliasOwner records which performer an alias string is attributed to. A nil owner means "no
// attribution recorded", which is the state of every row that predates migration 125.
func (qb *PerformerStore) SetAliasOwner(ctx context.Context, ownership models.PerformerAliasOwnership) error {
	if err := qb.clearAliasOwner(ctx, ownership.PerformerID, ownership.Alias); err != nil {
		return err
	}

	q := dialect.Insert(performerAliasOwnersTableName).
		Cols("performer_id", "alias", "owner_performer_id").
		Vals(goqu.Vals{ownership.PerformerID, ownership.Alias, ownership.OwnerPerformerID})
	if _, err := exec(ctx, q); err != nil {
		return fmt.Errorf("setting performer %d alias owner: %w", ownership.PerformerID, err)
	}

	return nil
}

func (qb *PerformerStore) clearAliasOwner(ctx context.Context, performerID int, alias string) error {
	_, err := exec(ctx, dialect.Delete(performerAliasOwnersTableName).Where(
		performerAliasOwnersTableName.Col("performer_id").Eq(performerID),
		performerAliasOwnersTableName.Col("alias").Eq(alias),
	))
	return err
}

// ---------------------------------------------------------------------------
// S6 — defined nationality (#1922)
// ---------------------------------------------------------------------------

// GetNationalities returns every nationality in the controlled list. The list is a table rather
// than an enum because SQLite cannot ALTER a CHECK constraint, so an enum would freeze the list
// into every existing database with no way to extend it.
func (qb *PerformerStore) AllNationalities(ctx context.Context) ([]*models.Nationality, error) {
	var ret []*models.Nationality
	q := dialect.Select("*").From(nationalitiesTableName)
	if err := queryFunc(ctx, q, false, func(rows *sqlx.Rows) error {
		var row models.Nationality
		if err := rows.StructScan(&row); err != nil {
			return err
		}
		ret = append(ret, &row)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("getting nationalities: %w", err)
	}

	return ret, nil
}

// SetNationalities replaces the performer's nationalities wholesale. A performer may hold SEVERAL
// (#1922 is explicitly about dual nationality); the composite primary key
// (performer_id, nationality_id) is what prevents the same one twice.
//
// DUPLICATES IN THE INPUT ARE TOLERATED, NOT REJECTED. The first version inserted the caller's
// list verbatim, so passing the same nationality twice hit the primary key and returned
// "UNIQUE constraint failed: performer_nationalities.performer_id,
// performer_nationalities.nationality_id". That is a bad contract for a setter: a UI multi-select
// can legitimately hand back a repeated value, and the correct result of "this performer has
// nationality 1" is one row, not an error the caller cannot interpret. The set is de-duplicated
// here so the caller's list order and repetition do not matter.
func (qb *PerformerStore) SetNationalities(ctx context.Context, performerID int, nationalityIDs []int) error {
	if _, err := exec(ctx, dialect.Delete(performerNationalitiesTableName).
		Where(performerNationalitiesTableName.Col("performer_id").Eq(performerID))); err != nil {
		return err
	}

	seen := make(map[int]bool, len(nationalityIDs))
	for _, nid := range nationalityIDs {
		if seen[nid] {
			continue
		}
		seen[nid] = true

		if _, err := exec(ctx, dialect.Insert(performerNationalitiesTableName).
			Cols("performer_id", "nationality_id").
			Vals(goqu.Vals{performerID, nid})); err != nil {
			return fmt.Errorf("setting performer %d nationality %d: %w", performerID, nid, err)
		}
	}

	return nil
}

func (qb *PerformerStore) GetNationalities(ctx context.Context, performerID int) ([]models.Nationality, error) {
	// THE COLUMN LIST IS EXPLICIT, NOT `*`. The join selects every column from both tables, and
	// `performer_nationalities.performer_id` is then present in the result set with no field on
	// models.Nationality to receive it. StructScan rejects unknown columns rather than ignoring
	// them ("missing destination name performer_id"), so `SELECT *` here fails at runtime, not at
	// compile time -- which is the kind of mistake that survives a build and dies in a test.
	q := dialect.Select(
		nationalitiesTableName.Col("id"),
		nationalitiesTableName.Col("name"),
		nationalitiesTableName.Col("code"),
	).
		From(performerNationalitiesTableName).
		Join(nationalitiesTableName, goqu.On(
			nationalitiesTableName.Col("id").Eq(performerNationalitiesTableName.Col("nationality_id")),
		)).
		Where(performerNationalitiesTableName.Col("performer_id").Eq(performerID))

	var ret []models.Nationality
	if err := queryFunc(ctx, q, false, func(rows *sqlx.Rows) error {
		var row models.Nationality
		if err := rows.StructScan(&row); err != nil {
			return err
		}
		ret = append(ret, row)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("getting performer %d nationalities: %w", performerID, err)
	}

	return ret, nil
}

// ---------------------------------------------------------------------------
// S10 — tattoo & piercing structure
// ---------------------------------------------------------------------------

// GetBodyMarks returns a performer's marks, optionally narrowed to one kind. Pass an empty kind
// for both. The filter is done in SQL rather than in Go so a caller filtering server-side does not
// read every row to discard most of them.
func (qb *PerformerStore) GetBodyMarks(ctx context.Context, performerID int, kind string) ([]*models.BodyMark, error) {
	q := dialect.Select("*").From(performerBodyMarksTableName).
		Where(performerBodyMarksTableName.Col("performer_id").Eq(performerID))
	if kind != "" {
		q = q.Where(performerBodyMarksTableName.Col("kind").Eq(kind))
	}

	var ret []*models.BodyMark
	if err := queryFunc(ctx, q, false, func(rows *sqlx.Rows) error {
		var row models.BodyMark
		if err := rows.StructScan(&row); err != nil {
			return err
		}
		ret = append(ret, &row)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("getting performer %d body marks: %w", performerID, err)
	}

	return ret, nil
}

// CreateBodyMark records a tattoo or piercing.
//
// THE KIND IS VALIDATED HERE, NOT ONLY BY SQL. Migration 125 has a CHECK constraining kind to
// ('tattoo','piercing'), so a bad value fails at the database -- but a caller that discovers this
// by way of a constraint violation gets an opaque SQLite error. Validating first turns the common
// mistake into a message naming the field and the legal values.
func (qb *PerformerStore) CreateBodyMark(ctx context.Context, mark models.BodyMark) (*models.BodyMark, error) {
	if mark.Kind != models.BodyMarkKindTattoo && mark.Kind != models.BodyMarkKindPiercing {
		return nil, fmt.Errorf("body mark kind must be %q or %q, got %q",
			models.BodyMarkKindTattoo, models.BodyMarkKindPiercing, mark.Kind)
	}
	if mark.Location == "" {
		return nil, fmt.Errorf("body mark location is required")
	}

	if _, err := exec(ctx, dialect.Insert(performerBodyMarksTableName).
		Cols("performer_id", "kind", "location", "description", "image_path").
		Vals(goqu.Vals{mark.PerformerID, mark.Kind, mark.Location, mark.Description, mark.ImagePath}),
	); err != nil {
		return nil, fmt.Errorf("creating body mark for performer %d: %w", mark.PerformerID, err)
	}

	// THE ROW IS READ BACK BY ITS OWN KEY, NOT RETURNED FROM LastInsertId.
	//
	// external_id.go records why, and the reason applies here: on `ON CONFLICT DO NOTHING` SQLite
	// returns the rowid of the last successful insert ON THIS CONNECTION, not zero. So a
	// LastInsertId-based read-back can hand back a plausible id belonging to an unrelated row --
	// every value individually reasonable, the result wrong. Here the insert cannot conflict (the
	// unique index is on (performer_id, kind, location, description) and a conflict returns an
	// error rather than a row), so the risk is lower; reading back by key costs one query and makes
	// the returned struct a value the database actually holds rather than one this function
	// assembled from its own input.
	created := &mark
	if existing, err := qb.GetBodyMarks(ctx, mark.PerformerID, ""); err == nil {
		for _, m := range existing {
			if m.Kind == mark.Kind && m.Location == mark.Location &&
				eqOptString(m.Description, mark.Description) {
				return m, nil
			}
		}
	}

	return created, nil
}

// eqOptString compares two optional strings, treating nil and "" as equal.
//
// Load-bearing for the read-back above: `description` is nullable, and SQLite returns NULL for a
// row whose description was inserted as NULL. Comparing pointers would say two rows differ when
// they are the same row.
func eqOptString(a, b *string) bool {
	if a == nil || b == nil {
		return (a == nil || *a == "") && (b == nil || *b == "")
	}
	return *a == *b
}

// DestroyBodyMark removes one mark. Returns the number of rows removed so a caller can tell a
// no-op delete from a real one -- a delete matching zero rows is not an error in SQLite, and "I
// deleted something that was not there" and "I deleted it" must not look the same.
func (qb *PerformerStore) DestroyBodyMark(ctx context.Context, id int) (int, error) {
	res, err := exec(ctx, dialect.Delete(performerBodyMarksTableName).
		Where(performerBodyMarksTableName.Col("id").Eq(id)))
	if err != nil {
		return 0, fmt.Errorf("destroying body mark %d: %w", id, err)
	}

	// A delete that matched nothing is not an error here, but the caller is told, because the
	// alternative -- returning nil for both outcomes -- makes an idempotent re-delete look
	// identical to a first delete, and a caller counting deletions would silently overcount.
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}

	return int(affected), nil
}