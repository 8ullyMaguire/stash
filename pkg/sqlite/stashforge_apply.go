package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/models"
)

// CollabTargetStore is the sqlite implementation of collab.TargetStore: the one
// place in StashForge where a decision made by a VOTE becomes a WRITE on shared
// content.
//
// Every statement here is built from a table name and a column name that come
// from collab's vocabulary, never from caller-supplied strings. That is not a
// style preference -- the whole reason the vocabulary is a closed map is that
// this file interpolates its values, so the validation and the interpolation
// have to be the same list or the guarantee is a lie. targetTableFor and
// collabColumnFor return errors rather than defaulting, so a vocabulary entry
// without a mapping fails loudly here instead of writing to the wrong table.
type CollabTargetStore struct {
	proposals *EditProposalStore
	audit     *AuditStore
}

func NewCollabTargetStore() *CollabTargetStore {
	return &CollabTargetStore{
		proposals: NewEditProposalStore(),
		audit:     NewAuditStore(),
	}
}

// targetTables maps a collab target type to its table. Written out here rather
// than derived, so adding a target type to the vocabulary without teaching this
// package where to write is a runtime error, not a silent no-op.
var targetTables = map[string]string{
	"scene":     "scenes",
	"performer": "performers",
	"studio":    "studios",
	"tag":       "tags",
	"gallery":   "galleries",
	"image":     "images",
	"group":     "groups",
}

// collabColumns maps (target, field) to the real column.
//
// This is the second half of the guarantee collab's vocabulary makes, and
// TestVocabulary_EveryFieldIsARealColumn in stashforge_vocabulary_columns_test.go
// reads the schema and asserts every key here is a real column. The lookup is a
// map rather than a passthrough of the field name so that a vocabulary field and
// a column can differ deliberately -- which they already do in one case worth
// naming: none, currently, but the indirection is what makes that possible
// without another migration of this file.
var collabColumns = map[string]map[string]string{
	"scene":     {"title": "title", "details": "details", "director": "director", "studio_id": "studio_id", "date": "date"},
	"performer": {"name": "name", "disambiguation": "disambiguation", "details": "details", "gender": "gender", "birthdate": "birthdate", "country": "country"},
	"studio":    {"name": "name", "details": "details", "parent_id": "parent_id"},
	"tag":       {"name": "name", "description": "description"},
	// studio_id IS MAPPED FOR BOTH, and it was missing for both. That is a real bug
	// this project hit rather than a hypothetical: the two targets DO have the column
	// (pkg/sqlite/migrations/13_images.up.sql declares `studio_id` on both `images`
	// and `galleries`, each with a foreign key to studios), and autotag's studio
	// matcher has always run against all three target types. Before this line, a studio
	// claim on an image or a gallery was REFUSED by collabColumnFor with "not a mapped
	// column", which surfaced as the autotag scan erroring out on every image -- not as
	// a quiet no-op, but not as anything pointing at the column map either.
	//
	// It is found by an END-TO-END test asserting on a real database, not by a unit
	// test: the unit tests for autotag use mocks, a mock does not have a column map,
	// and the guard that reads this map against the real schema (TestVocabulary_...)
	// only covers the VOCABULARY side -- what is proposable -- not the reverse
	// direction, which is "proposable, and writable when approved".
	//
	// Those are different questions and only this one would have caught it. A field can
	// be in the vocabulary and unmapped here, and the consequence is an approval that
	// fails at write time: the moderator voted, the audit row says it applied, and the
	// field is unchanged.
	"gallery": {"title": "title", "details": "details", "studio_id": "studio_id"},
	"image":   {"title": "title", "rating": "rating", "studio_id": "studio_id"},
	"group":   {"name": "name", "description": "description", "date": "date", "studio_id": "studio_id", "rating": "rating"},
}

func targetTableFor(targetType string) (string, error) {
	t, ok := targetTables[targetType]
	if !ok {
		return "", fmt.Errorf("no table mapped for target type %q", targetType)
	}
	return t, nil
}

func collabColumnFor(targetType, field string) (string, error) {
	fields, ok := collabColumns[targetType]
	if !ok {
		return "", fmt.Errorf("no columns mapped for target type %q", targetType)
	}
	col, ok := fields[field]
	if !ok {
		return "", fmt.Errorf("field %q is not a mapped column of %q", field, targetType)
	}
	return col, nil
}

// fieldValueRow is the single-column read target for ReadField.
//
// A NAMED struct, not an anonymous one passed by pointer. The anonymous version
// scanned without error and left Valid=false for every value, which made Apply
// believe every target already held the proposed value and silently no-op --
// with no error anywhere. The raw SELECT one line away returned the right
// value, which is what made it a scan problem rather than a query problem.
type fieldValueRow struct {
	Current sql.NullString `db:"current"`
}

// ReadField returns the target's current value for a field.
//
// found is false only when the ROW does not exist. A row that exists with a
// NULL column returns (nil, true, nil) -- and that distinction is the reason
// this returns a bool rather than a *string: "the field is unset" and "the
// target is gone" lead to different outcomes in Apply.
func (s *CollabTargetStore) ReadField(ctx context.Context, targetType string, targetID int, field string) (*string, bool, error) {
	table, err := targetTableFor(targetType)
	if err != nil {
		return nil, false, err
	}
	col, err := collabColumnFor(targetType, field)
	if err != nil {
		return nil, false, err
	}

	var current fieldValueRow
	err = dbWrapper.Get(ctx, &current, fmt.Sprintf(
		"SELECT %s AS current FROM %s WHERE id = ?", col, table), targetID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}

	if !current.Current.Valid {
		return nil, true, nil
	}
	v := current.Current.String

	// Normalise DATE columns back to YYYY-MM-DD.
	//
	// SQLite stores a text date column as written, but a Go time.Time scanned
	// into it -- which is what the existing content stores write -- comes back
	// as a full ISO timestamp. So a scene dated "2024-02-29" reads back as
	// "2024-02-29T00:00:00Z", which is a DIFFERENT STRING from what the proposal
	// asked for.
	//
	// This is not cosmetic. Apply compares the read value with the proposed
	// value to decide whether the target is already correct. Without the
	// normalisation a re-applied date proposal would compare unequal forever,
	// write on every single pass, and append an audit row each time -- exactly
	// the traffic the idempotency guarantee exists to prevent.
	if len(v) >= 10 && v[4] == '-' && v[7] == '-' && isDateField(targetType, field) {
		return ptr(strings.TrimSuffix(v[:10], "T")), true, nil
	}
	return &v, true, nil
}

// isDateField reports whether the vocabulary types this field as a date, so the
// ReadField normalisation applies to dates and nothing else.
func isDateField(targetType, field string) bool {
	info, ok := collab.LookupField(targetType, field)
	return ok && info.Type == collab.TypeDate
}

func ptr[T any](v T) *T { return &v }

// WriteFieldIfChanged is the compare-and-set, and it is a SINGLE statement on
// purpose.
//
// The naive version -- SELECT the current value, compare in Go, then UPDATE --
// is what the first implementation did and what the concurrency test caught: two
// workers both read the old value, both decide to write, and you get two writes
// and two audit rows for one accepted proposal. Doing the comparison in the
// WHERE clause makes the database the arbiter, so exactly one UPDATE matches and
// every other worker updates zero rows.
//
// `IS ?` rather than `= ?` is required for NULL: in SQL, `NULL = NULL` is NULL,
// not true, so `= ?` would make "clear an already-unset field" match zero rows
// forever and the clear would never be idempotent. `IS` compares NULLs as
// equal, which is exactly the "unset equals unset" rule the vocabulary wants --
// and `IS` also compares non-NULLs correctly, so one operator covers both.
func (s *CollabTargetStore) WriteFieldIfChanged(ctx context.Context, targetType string, targetID int, field string, expected, value *string) (bool, error) {
	table, err := targetTableFor(targetType)
	if err != nil {
		return false, err
	}
	col, err := collabColumnFor(targetType, field)
	if err != nil {
		return false, err
	}

	// Format through the vocabulary, so a rating lands in a numeric column as an
	// integer rather than as the string "4".
	typed, err := collab.FormatValueForDB(targetType, field, value)
	if err != nil {
		return false, err
	}
	typedExpected, err := collab.FormatValueForDB(targetType, field, expected)
	if err != nil {
		return false, err
	}

	// updated_at is bumped alongside, because every one of these tables carries
	// it and a proposal-applied change that leaves it stale would make the
	// library's "recently changed" ordering wrong.
	res, err := dbWrapper.Exec(ctx, fmt.Sprintf(
		"UPDATE %s SET %s = ?, updated_at = ? WHERE id = ? AND %s IS ?",
		table, col, col), typed, time.Now(), targetID, typedExpected)
	if err != nil {
		return false, err
	}

	n, err := res.RowsAffected()
	if err != nil {
		// Cannot tell whether it wrote. Reporting false would claim a no-op that
		// may not have happened, so treat it as an error.
		return false, fmt.Errorf("applying %s %d field %q: cannot determine rows affected: %w",
			targetType, targetID, field, err)
	}
	return n > 0, nil
}

// MarkRejected records the refusal in the proposal's own status. The REASON
// goes to the audit row rather than here, because edit_proposals has no reason
// column and adding one for this would mean a migration for a value that is
// only ever read next to its audit entry.
// AddLink inserts one row into a relationship join table.
//
// EVERY IDENTIFIER COMES FROM collab's link map, never from the caller's strings. The
// table, the target's id column and the entity's id column are all looked up by
// (targetType, kind), so an unknown pair fails BEFORE any SQL is built. That is the same
// discipline the column path takes in collabColumnFor, and for the same reason: this is
// the surface where a vote becomes a write on shared content, and an interpolated
// caller-supplied string here would be SQL injection on an approved change.
func (s *CollabTargetStore) AddLink(ctx context.Context, targetType string, targetID int, kind collab.LinkKind, entityID int) (bool, error) {
	shape, err := collab.ValidateLink(targetType, kind)
	if err != nil {
		return false, err
	}

	// Both ids must be positive for the same reason a vocabulary TypeInt value must
	// be: a 0 or negative id reaching an INSERT is either a constraint violation or,
	// on a table without one, a row that points at nothing.
	if targetID <= 0 {
		return false, fmt.Errorf("adding %s to %s: target id %d is not addressable",
			kind, targetType, targetID)
	}
	if entityID <= 0 {
		return false, fmt.Errorf("adding %s to %s %d: entity id %d is not addressable",
			kind, targetType, targetID, entityID)
	}

	// The target must EXIST, and this is checked rather than inferred from the
	// foreign key. SQLite has foreign keys off by default in many builds, so a missing
	// target would otherwise produce a join row pointing at nothing -- an orphaned link
	// that reads as "this scene has this performer" and resolves to a deleted row. The
	// check is a read the apply path would otherwise not have made.
	exists, err := s.rowExists(ctx, targetType, targetID)
	if err != nil {
		return false, err
	}
	if !exists {
		// The SENTINEL, wrapped, so errors.Is finds it -- and the apply path's
		// "reject with target no longer exists" branch is reachable only through this
		// error and nothing else.
		return false, fmt.Errorf("adding %s to %s %d: %w",
			kind, targetType, targetID, collab.ErrLinkTargetMissing)
	}

	// INSERT OR IGNORE, and the reason is the CONCURRENCY story rather than the
	// duplicate story. Two workers applying the same approved link both reach here; the
	// join table's uniqueness constraint (on the pair) lets exactly one insert and the
	// other's row count is 0. So the loser reports added=false, which the apply path
	// maps to already-correct -- and one approved link produces one row and one audit
	// row, not two of each.
	//
	// OR IGNORE rather than a read-then-write, because a read-then-write here would
	// have the exact race the column path's compare-and-set was written to avoid: both
	// workers read "not linked", both write, and the database is the only thing left
	// to arbitrate. Let it arbitrate, and ask it what happened.
	res, err := dbWrapper.Exec(ctx, fmt.Sprintf(
		"INSERT OR IGNORE INTO %s (%s, %s) VALUES (?, ?)",
		shape.Table, shape.IdColumn, shape.LinkColumn), targetID, entityID)
	if err != nil {
		return false, fmt.Errorf("adding %s to %s %d: %w", kind, targetType, targetID, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		// Cannot tell whether the row landed. Reporting false would claim a no-op that
		// may not have happened, so this is an error -- the same reasoning as
		// WriteFieldIfChanged's.
		return false, fmt.Errorf("adding %s to %s %d: cannot determine rows affected: %w",
			kind, targetType, targetID, err)
	}
	return n > 0, nil
}

// rowExists reports whether the target row is present.
//
// ONE query, and not a SELECT of the field, because a link's target is not a field: a
// scene with every column NULL still exists and can carry a link.
func (s *CollabTargetStore) rowExists(ctx context.Context, targetType string, targetID int) (bool, error) {
	table, err := targetTableFor(targetType)
	if err != nil {
		return false, err
	}
	var one int
	err = dbWrapper.Get(ctx, &one, fmt.Sprintf("SELECT 1 FROM %s WHERE id = ?", table), targetID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("checking whether %s %d exists: %w", targetType, targetID, err)
	}
	return true, nil
}

func (s *CollabTargetStore) MarkRejected(ctx context.Context, proposalID int, deciderID int, reason string) error {
	_ = reason
	return s.proposals.SetStatus(ctx, proposalID, models.ProposalRejected, deciderID)
}

func (s *CollabTargetStore) AppendAudit(ctx context.Context, entry collab.AuditEntry) error {
	return s.audit.Append(ctx, entry.ActorID, entry.Action, entry.TargetType, entry.TargetID, entry.Field, entry.Detail)
}

var _ collab.TargetStore = (*CollabTargetStore)(nil)

// WritableColumnsForTest returns the write side of the governance seam: every
// (target, field) pair the applier can actually write.
//
// IT IS AN EXPORT AND NOT A TEST-ONLY DUPLICATE, and that is the whole point of it. The
// seam-closure test compares this map against collab's vocabulary, and a helper that
// built its answer from a hand-written list would make that comparison circular: both
// sides would be the test author's belief about the schema rather than the schema. So
// this returns the live map, and a change to collabColumns that breaks the seam fails
// the test instead of quietly agreeing with itself.
//
// The name says ForTest because no production caller wants it -- an applier asking
// "what could I write" is asking the wrong question, and would be the first step
// towards letting a caller choose a field by string. The export is for the checker,
// not for callers.
func WritableColumnsForTest() map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(collabColumns))
	for targetType, fields := range collabColumns {
		inner := make(map[string]bool, len(fields))
		for field, column := range fields {
			// The COLUMN name is the value here, not the field name, because the two can
			// differ deliberately -- that indirection is the comment on collabColumns'
			// declaration. A test comparing field names against map keys would be
			// asserting they are identical, which is the assumption the indirection
			// exists to remove.
			inner[field] = column != ""
		}
		out[targetType] = inner
	}
	return out
}
