package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/stashapp/stash/internal/collab"
)

// The instance-mode store. M4 step 4.1.
//
// The posture is read on every request and written rarely, so the read path is
// the one that matters. It reads the single row and returns collab.Mode, and
// three things about it are deliberate:
//
//   - A MISSING ROW IS `private`, not an error. The migration seeds the row, so a
//     miss means someone deleted it or is running against a database the
//     migration did not reach. Both resolve to the most restrictive mode, because
//     an absent posture must fail closed.
//   - An UNRECOGNISED MODE IS AN ERROR, not a default. The column is CHECKed, so
//     this means the CHECK was bypassed -- and defaulting here would let a
//     corrupted row choose a posture.
//   - The wizard flag travels with the mode, because "public because somebody
//     chose it" and "public because a row says so" must not look alike.

var _ collab.ModeStore = (*InstanceModeStore)(nil)

type InstanceModeStore struct {
	repository
}

func NewInstanceModeStore() *InstanceModeStore {
	return &InstanceModeStore{repository{tableName: instanceSettingsTable, idColumn: "id"}}
}

const instanceSettingsTable = "instance_settings"

// Mode returns the instance's posture.
func (s *InstanceModeStore) Mode(ctx context.Context) (collab.Mode, error) {
	var mode string
	err := dbWrapper.Get(ctx, &mode, "SELECT mode FROM instance_settings WHERE id = 1")
	if err != nil {
		// errors.Is, NOT ==. dbWrapper wraps with %w, so a direct comparison
		// never matches. This path is the fail-closed one -- it is what stops a
		// missing row from reading as anything but private -- so a comparison
		// that cannot match means a missing row surfaces as an internal error
		// and the instance will not start.
		if errors.Is(err, sql.ErrNoRows) {
			// The migration seeds this row, so a miss is an anomaly rather than
			// a normal state. Private is the answer that cannot cause harm.
			return collab.ModePrivate, nil
		}
		return "", fmt.Errorf("reading instance mode: %w", err)
	}

	m := collab.Mode(mode)
	if !m.Valid() {
		// CHECKed in the schema, so this is a bypassed constraint or a corrupted
		// row. Erroring is right: silently choosing a posture here would be a
		// security decision made by a bug.
		return "", fmt.Errorf("instance_settings.mode is %q, which is not a valid mode", mode)
	}
	return m, nil
}

// WizardCompleted reports whether the blocking first-run wizard has been done.
func (s *InstanceModeStore) WizardCompleted(ctx context.Context) (bool, error) {
	var done bool
	err := dbWrapper.Get(ctx, &done, "SELECT wizard_completed FROM instance_settings WHERE id = 1")
	if err != nil {
		// errors.Is for the same reason as above: == cannot match a wrapped
		// driver error, and "wizard has not run" must not read as an internal
		// error.
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("reading wizard_completed: %w", err)
	}
	return done, nil
}

// SetMode records a posture change and who made it.
//
// The actor is a parameter and may be nil, and nil means "nobody is
// authenticated" -- which is a real state for a first-run wizard, not a default
// to paper over. Recording a system change as if a user made it is worse than
// recording it as unattributed.
//
// decidedBy is an int64 rather than the models.User because the mode is read at
// startup, before any user session exists, and the first mode this instance ever
// has is set by the wizard with no session behind it.
func (s *InstanceModeStore) SetMode(ctx context.Context, mode collab.Mode, decidedBy *int64) error {
	if !mode.Valid() {
		return fmt.Errorf("%w: %q", collab.ErrModeInvalid, mode)
	}
	_, err := dbWrapper.Exec(ctx,
		"UPDATE instance_settings SET mode = ?, mode_changed_at = CURRENT_TIMESTAMP, mode_changed_by = ? WHERE id = 1",
		string(mode), int64PtrOrNil(decidedBy))
	return err
}

// CompleteWizard marks the first-run wizard done, in the same statement as the
// mode it was choosing.
//
// Same statement on purpose: if these were two writes, a crash between them
// leaves either a completed wizard with no chosen mode or a chosen mode with an
// unfinished wizard, and the first is a silent posture and the second is a
// wizard that reappears having already been answered.
func (s *InstanceModeStore) CompleteWizard(ctx context.Context, mode collab.Mode, decidedBy *int64) error {
	if !mode.Valid() {
		return fmt.Errorf("%w: %q", collab.ErrModeInvalid, mode)
	}
	_, err := dbWrapper.Exec(ctx,
		`UPDATE instance_settings
		    SET mode = ?, wizard_completed = 1, mode_changed_at = CURRENT_TIMESTAMP, mode_changed_by = ?
		  WHERE id = 1`,
		string(mode), int64PtrOrNil(decidedBy))
	return err
}

func int64PtrOrNil(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// CheckStartup is the step 4.1 rule applied to a real database: a public mode
// over plain HTTP refuses to start.
//
// It is a function here rather than only in collab so that the check reads the
// mode AND the wizard flag together, which is the pair that decides whether a
// posture was chosen.
func (s *InstanceModeStore) CheckStartup(ctx context.Context, scheme string) error {
	mode, err := s.Mode(ctx)
	if err != nil {
		return err
	}
	wizardDone, err := s.WizardCompleted(ctx)
	if err != nil {
		return err
	}
	if err := collab.CheckStartup(mode, scheme, wizardDone); err != nil {
		return err
	}
	return nil
}
