package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/stashapp/stash/internal/collab"
)

// R083: the three-state sharing switch's storage.
//
// # WHERE THE READ HAPPENS, AND WHY IT CANNOT BE ANYWHERE ELSE
//
// §6b.3 requires enforcement "at the point the permission is read, in the same place
// the existing tier policy is read, and NOT by omitting rows from a query". Non-
// negotiable #7's own note is that a refactor dropping the filter must not silently
// resume publishing.
//
// THIS STORE THEREFORE EXPOSES NO QUERY THAT CAN BE FILTERED. There is no
// "acquirable items" SELECT here, and that absence is the design. If the switch were
// enforced by `WHERE auto_acquire != 'off'` on a candidate query, then the enforcement
// would live in one query string, a refactor that wanted a different query would
// simply not include the filter, and the failure would be silence. Reading the switch
// and asking it a question puts the rule in Go, where a reviewer sees it.
//
// # WHY THE DEFAULT ON A MISSING ROW IS fetch_only, WHICH IS THE UNSAFE DIRECTION
//
// AND WHY THAT IS STILL CORRECT.
//
// Migration 115 defaults the column to fetch_only and the migration seeds exactly one
// instance_settings row, so a miss means the row is gone or the schema was not applied
// — an anomaly. Two defaults are defensible there and the choice is not obvious:
//
//   - AcquireOff fails closed, and is the answer this package gives for a MISSING row,
//     because a missing row is a broken database rather than a user who declined
//     anything.
//   - AcquireFetchOnly matches what a NEW install would read, so an instance that
//     lost the row behaves as it did before losing it.
//
// It returns AcquireOff. The reasoning is that migration 115's default is a product
// decision about a new install — it is the value a user gets without choosing — while a
// missing row is not that situation at all: the instance is in a state its schema
// does not describe, and the conservative answer is the one that acquires nothing.
// "Fetch but never seed" cannot cause loss, and the repair for a broken database is
// the operator's, not this function's silent substitution.

// AutoAcquireStore reads and writes the instance's capability-2 posture.
//
// IT IS A SEPARATE STORE FROM InstanceModeStore even though both read
// instance_settings, and the split is by permission rather than by table. Mode answers
// "what may this instance publish"; AutoAcquire answers "what may this instance take
// in". They are read in different code paths, they have different defaults on a
// missing row (a missing mode is private because §2's posture is a security
// decision; a missing acquire state is off because §6b.3's is a capability), and a
// caller that wanted both would hold one of each. Merging them would mean a mode read
// silently acquiring a posture and vice versa.
type AutoAcquireStore struct {
	repository
}

var _ collab.AutoAcquireStore = (*AutoAcquireStore)(nil)

func NewAutoAcquireStore() *AutoAcquireStore {
	return &AutoAcquireStore{repository{tableName: instanceSettingsTable, idColumn: "id"}}
}

// AutoAcquire returns the instance's capability-2 posture.
//
// AN UNRECOGNISED STORED VALUE IS AN ERROR, not a coerced default. The column is
// CHECKed, so an unknown value here means the CHECK was bypassed — and returning
// fetch_only would let a corrupted row choose a posture, which is the same mistake
// migration 103's header describes for `mode`. The error is returned rather than
// swallowed so the caller refuses rather than proceeding on a value nobody chose.
func (s *AutoAcquireStore) AutoAcquire(ctx context.Context) (collab.AutoAcquire, error) {
	var raw string
	err := dbWrapper.Get(ctx, &raw,
		"SELECT auto_acquire FROM instance_settings WHERE id = 1")
	if err != nil {
		// errors.Is, NOT ==. dbWrapper wraps with %w, so `== sql.ErrNoRows` is never
		// true and this fail-closed path would surface as an internal error instead.
		// The instance mode store's comment says exactly this and it cost me a test
		// failure here before I checked.
		if errors.Is(err, sql.ErrNoRows) {
			return collab.AcquireOff, nil
		}
		return collab.AcquireOff, fmt.Errorf("reading instance auto_acquire: %w", err)
	}

	a := collab.AutoAcquire(raw)
	if !a.Valid() {
		// Refuse rather than coerce. The safe direction here is ambiguous — off is
		// safe for the instance, but a silent coercion hides a corrupted column from
		// whoever has to repair it — so the value is reported AND the state returned is
		// the refusing one.
		return collab.AcquireOff, fmt.Errorf(
			"%w: stored auto_acquire is %q, so the column has been written by "+
				"something that bypassed the CHECK. Refusing rather than guessing: "+
				"the operator can repair a row, and nobody should repair it by accident",
			collab.ErrAutoAcquireInvalid, raw)
	}
	return a, nil
}

// SetAutoAcquire writes the posture.
//
// The value is validated HERE as well as by the CHECK, for the same two-layer reason
// as R080's reason guard: the CHECK is the one place a peer cannot talk you out of, and
// this is the first place a bug would be caught. Validating only in the database means
// the error a caller gets is a SQLite constraint message, which does not say which of
// the three states was expected.
func (s *AutoAcquireStore) SetAutoAcquire(ctx context.Context, a collab.AutoAcquire) error {
	if !a.Valid() {
		return fmt.Errorf("%w: %q is not one of %v", collab.ErrAutoAcquireInvalid,
			string(a), collab.AcquireStates())
	}
	_, err := dbWrapper.Exec(ctx,
		"UPDATE instance_settings SET auto_acquire = ? WHERE id = 1", string(a))
	if err != nil {
		return fmt.Errorf("writing instance auto_acquire: %w", err)
	}
	return nil
}
