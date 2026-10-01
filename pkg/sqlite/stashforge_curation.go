package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/stashapp/stash/internal/autoproposal"
)

// R086: automatic curation's storage -- how a machine's match reaches shared content.
//
// THE SHAPE IS R083's AutoAcquireStore, and deliberately so. Both are one-row
// instance_settings reads, both are consulted at the point a permission is used rather
// than by filtering a query, and both have a default chosen for what a MISSING row
// means rather than for what a new install should get.
//
// WHY IT IS A SEPARATE STORE FROM AutoAcquireStore: by permission, not by table -- the
// same rule that split mode from auto_acquire. Curation answers "may a machine write
// this"; auto_acquire answers "may this instance take content in". They are read in
// different code paths, they have different consequences when wrong (one bypasses
// governance, the other loses preservation), and a caller wanting both would hold one
// of each rather than one merged handle whose read quietly acquired both answers.
type CurationStore struct {
	repository
}

var _ = (*CurationStore)(nil)

// NewCurationStore builds the store, and it is the same construction as R083's
// AutoAcquireStore -- the same embedded repository pointed at instance_settings, for
// the reason that file gives: a store is built once and asked a question, rather than
// constructed per call, so a read that fails is a decision the CALLER has to make
// rather than a nil store discovered halfway through.
func NewCurationStore() *CurationStore {
	return &CurationStore{repository{tableName: instanceSettingsTable, idColumn: "id"}}
}

// CurationMode returns the instance's curation mode.
//
// A MISSING ROW IS `propose`, and that is a different argument from the migration's
// DEFAULT even though both land on the same value. The migration's default is a product
// decision about a new install; a missing row is not that situation at all -- the
// instance is in a state its schema does not describe. So the question is not "what
// would a new install do" but "what is safe when the instance is broken", and filing
// proposals answers that. The alternative, refusing so autotag does not run, turns a
// database anomaly into a feature that has silently stopped -- and a silently stopped
// scan is indistinguishable from a scan that found nothing.
//
// IT IS NOT `off`, and the difference is the whole reason. off means an operator
// DECIDED, and a decided thing must never be inferred from a missing row.
func (s *CurationStore) CurationMode(ctx context.Context) (autoproposal.CurationMode, error) {
	var raw string
	err := dbWrapper.Get(ctx, &raw,
		"SELECT curation FROM instance_settings WHERE id = 1")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return autoproposal.CurationPropose, nil
		}
		return "", fmt.Errorf("reading the curation mode: %w", err)
	}

	mode, err := autoproposal.ParseCurationMode(raw)
	if err != nil {
		// REFUSED, and returned rather than defaulted. The CHECK constraint should make
		// this unreachable, and a test asserts the constraint agrees with the Go guard
		// so neither can drift -- but if a row does hold a value this build cannot
		// read, the operator's intent is UNKNOWN, and answering `propose` would be
		// reporting a decision nobody made.
		return "", fmt.Errorf("the stored curation mode is unreadable: %w", err)
	}
	return mode, nil
}

// SetCurationMode writes the instance's curation mode.
//
// VALIDATED IN GO BEFORE THE WRITE, and the schema's CHECK is the second line rather
// than the only one: a Go guard is unit-testable exhaustively, and a CHECK is what a
// writer bypassing this method still meets. A test asserts the two agree, because a
// guard that only one layer has stops guarding when that layer is bypassed -- which is
// the same trap migration 115's own note records.
func (s *CurationStore) SetCurationMode(ctx context.Context, mode autoproposal.CurationMode) error {
	if !mode.Valid() {
		return fmt.Errorf("%w: refusing to store %q", autoproposal.ErrCurationModeInvalid, mode)
	}

	// The zero value is NOT written as "". It means propose when READ, so storing ""
	// would be correct by accident and would persist something the operator never chose.
	stored := mode
	if stored == "" {
		stored = autoproposal.CurationPropose
	}

	res, err := dbWrapper.Exec(ctx,
		"UPDATE instance_settings SET curation = ? WHERE id = 1", string(stored))
	if err != nil {
		return fmt.Errorf("writing the curation mode: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("cannot determine whether the curation mode was written: %w", err)
	}
	if n == 0 {
		// NO ROW TO WRITE. A silent success here leaves the instance reading its
		// default forever while the operator believes they set something, and the
		// migration seeds exactly one row -- so this is an anomaly worth reporting
		// rather than swallowing.
		return fmt.Errorf("no instance_settings row to write the curation mode to; " +
			"the instance would keep reading its default while the operator believes " +
			"they changed it")
	}
	return nil
}
