package file

import (
	"context"
	"errors"
	"testing"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
	"github.com/stashapp/stash/pkg/txn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// #3001 -- a `File.Destroy.Post` hook.
//
// ## WHAT WAS ACTUALLY MISSING
//
// The ledger said "absent -- no file event hook system". Half of that is right and half is
// misleading: `pkg/txn` has `AddPostCommitHook` / `AddPostRollbackHook`, and
// `pkg/file/delete.go` already registers both through `Deleter.RegisterHooks`. So the
// mechanism exists. What was missing is a hook keyed to the FILE DESTROY EVENT, which is a
// different axis: the existing hooks fire once per transaction for the deleter's own
// bookkeeping.
//
// ## WHY POST-COMMIT RATHER THAN "WHEN Destroy RETURNS"
//
// `Destroy` returning is NOT the same as the file being gone. `FileStore.Destroy` goes
// through `destroyExisting`, which runs inside the caller's transaction; if that caller
// rolls back, the row is still there.
//
// A hook fired when `Destroy` returned would announce a deletion that has not happened, and
// a consumer acting on it -- deleting a sprite, a generated preview, a sidecar -- would have
// destroyed something belonging to a file that still exists, with no way to restore it.
//
// So the hook fires on post-commit, the earliest moment at which "the row is gone" is a
// fact. `pkg/file` already treats this as load-bearing: `Deleter` renames files during the
// transaction and only commits the rename in its post-commit hook, rolling it back on
// rollback.
//
// ## WHY THE HANDLER IS A PARAMETER AND NOT A GLOBAL
//
// Two measured facts decided this:
//
//   - `txn.MustFunc` is `func(ctx context.Context)` -- it returns NOTHING. A handler's error
//     cannot reach anyone through a post-commit hook, so an error-silent global is the only
//     shape the existing hook type allows. That is how a failed scrub goes unnoticed.
//   - `txn.hookManagerCtx` returns nil outside a transaction, and `AddPostCommitHook`
//     dereferences it with no guard. So calling it with a plain context PANICS. `Destroy` is
//     reachable from 13 call sites across 4 packages, and a global registration would make
//     every one of them a potential panic.
//
// So the handler is threaded through `Destroy` explicitly. Nothing panics, nothing is
// silent, and a caller that has no interest passes nothing.

// A DESTROY MUST ANNOUNCE THE FILE, ONCE, WITH ITS IDENTITY -- AND ONLY AT COMMIT.
//
// The ordering assertion is the point of the whole feature, so it is made inside the
// transaction: `Destroy` must return having announced NOTHING, and the announcement must
// appear only once `txn.WithTxn` has committed. `withTxn` runs post-commit hooks in a
// deferred function AFTER `fn` returns, so observing from inside `fn` is exactly the
// "before commit" window.
func TestDestroyAnnouncesTheFileOnceOnlyAtCommit(t *testing.T) {
	ctx := context.Background()
	destroyer := &mocks.FileReaderWriter{}
	destroyer.On("Destroy", mock.Anything, models.FileID(42)).Return(nil)

	f := &models.VideoFile{BaseFile: &models.BaseFile{ID: 42, Path: "/library/a.mp4"}}

	var announced []DestroyedFile
	insideTxn := 0
	handler := func(_ context.Context, d DestroyedFile) error {
		announced = append(announced, d)
		return nil
	}

	db := mocks.NewDatabase()
	err := txn.WithTxn(ctx, db, func(tctx context.Context) error {
		if err := Destroy(tctx, destroyer, f, nil, false, handler); err != nil {
			return err
		}

		// Still inside the transaction: nothing may have been announced. A hook that fired
		// on `Destroy`'s return would pass every other assertion here -- it fires once,
		// with the right id -- while being wrong in the way that matters, because a
		// rollback would leave the file in place with its sprite already gone.
		insideTxn = len(announced)
		return nil
	})
	assert.NoError(t, err)

	assert.Equal(t, 0, insideTxn,
		"Destroy announced %d file(s) before the transaction committed; the row is not "+
			"durable yet and a rollback would leave the file in place", insideTxn)

	assert.Len(t, announced, 1, "after commit, exactly one announcement")
	assert.Equal(t, models.FileID(42), announced[0].ID)
	assert.Equal(t, "/library/a.mp4", announced[0].Path)
}

// A HANDLER MUST NOT BE CALLED WHEN THERE IS NONE.
//
// With no handler the feature is inert. Asserted because a hook that fires with a nil
// handler would either panic or invent an empty event for every delete in the app.
func TestDestroyWithNoHandlerIsInert(t *testing.T) {
	ctx := context.Background()
	destroyer := &mocks.FileReaderWriter{}
	destroyer.On("Destroy", mock.Anything, models.FileID(43)).Return(nil)

	f := &models.VideoFile{BaseFile: &models.BaseFile{ID: 43, Path: "/library/b.mp4"}}

	db := mocks.NewDatabase()

	err := txn.WithTxn(ctx, db, func(tctx context.Context) error {
		return Destroy(tctx, destroyer, f, nil, false, nil)
	})
	assert.NoError(t, err, "a nil handler must be accepted, not dereferenced")
}

// A ROLLBACK MUST ANNOUNCE NOTHING.
//
// The file still exists after a rollback, so an announcement would be a lie. This is the
// case that makes post-commit the correct moment rather than merely a tidy one.
func TestARollbackAnnouncesNothing(t *testing.T) {
	ctx := context.Background()
	destroyer := &mocks.FileReaderWriter{}
	destroyer.On("Destroy", mock.Anything, models.FileID(44)).Return(nil)

	f := &models.VideoFile{BaseFile: &models.BaseFile{ID: 44, Path: "/library/c.mp4"}}

	var announced []DestroyedFile
	handler := func(_ context.Context, d DestroyedFile) error {
		announced = append(announced, d)
		return nil
	}

	boom := errors.New("caller gave up")
	db := mocks.NewDatabase()

	err := txn.WithTxn(ctx, db, func(tctx context.Context) error {
		if err := Destroy(tctx, destroyer, f, nil, false, handler); err != nil {
			return err
		}
		return boom
	})
	assert.ErrorIs(t, err, boom)
	assert.Empty(t, announced, "a rolled-back delete must not be announced")
}

// A FAILED DESTROY MUST ANNOUNCE NOTHING, and must not remove the file either.
func TestAFailedDestroyAnnouncesNothing(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("db down")

	destroyer := &mocks.FileReaderWriter{}
	destroyer.On("Destroy", mock.Anything, models.FileID(45)).Return(boom)

	f := &models.VideoFile{BaseFile: &models.BaseFile{ID: 45, Path: "/library/d.mp4"}}

	var announced []DestroyedFile
	handler := func(_ context.Context, d DestroyedFile) error {
		announced = append(announced, d)
		return nil
	}

	db := mocks.NewDatabase()

	err := txn.WithTxn(ctx, db, func(tctx context.Context) error {
		return Destroy(tctx, destroyer, f, nil, false, handler)
	})
	assert.ErrorIs(t, err, boom)
	assert.Empty(t, announced)
}

// A FILE INSIDE A ZIP MUST CARRY ITS ZIP PARENT, so a consumer can tell it has no
// filesystem path of its own to act on.
func TestAFileInsideAZipCarriesItsZipParent(t *testing.T) {
	ctx := context.Background()
	destroyer := &mocks.FileReaderWriter{}

	zipID := models.FileID(7)
	f := &models.VideoFile{BaseFile: &models.BaseFile{
		ID: 46, Path: "/library/pack.zip/inner.mp4",
		DirEntry: models.DirEntry{ZipFileID: &zipID},
	}}
	destroyer.On("Destroy", mock.Anything, models.FileID(46)).Return(nil)

	var announced []DestroyedFile
	handler := func(_ context.Context, d DestroyedFile) error {
		announced = append(announced, d)
		return nil
	}

	db := mocks.NewDatabase()

	err := txn.WithTxn(ctx, db, func(tctx context.Context) error {
		return Destroy(tctx, destroyer, f, nil, false, handler)
	})
	assert.NoError(t, err)

	assert.Len(t, announced, 1)
	assert.NotNil(t, announced[0].ZipFileID, "a zipped file must be announced with its zip parent")
	assert.Equal(t, zipID, *announced[0].ZipFileID)
}

// A POINTER TO THE ZERO FILE ID MEANS "NO ZIP", NOT "ZIP 0".
//
// `ZipFileID` is a `*FileID`, so an unset field can arrive as a pointer to zero. A consumer
// checking `*f.ZipFileID != 0` would treat a top-level file as living in zip 0 and go
// looking for a generated directory that does not exist.
func TestAPointerToTheZeroZipIDMeansNoZip(t *testing.T) {
	ctx := context.Background()
	destroyer := &mocks.FileReaderWriter{}

	zero := models.FileID(0)
	f := &models.VideoFile{BaseFile: &models.BaseFile{
		ID: 47, Path: "/library/e.mp4",
		DirEntry: models.DirEntry{ZipFileID: &zero},
	}}
	destroyer.On("Destroy", mock.Anything, models.FileID(47)).Return(nil)

	var announced []DestroyedFile
	handler := func(_ context.Context, d DestroyedFile) error {
		announced = append(announced, d)
		return nil
	}

	db := mocks.NewDatabase()

	err := txn.WithTxn(ctx, db, func(tctx context.Context) error {
		return Destroy(tctx, destroyer, f, nil, false, handler)
	})
	assert.NoError(t, err)

	assert.Len(t, announced, 1)
	assert.Nil(t, announced[0].ZipFileID, "a pointer to the zero FileID must not be reported as a parent")
}

// A HANDLER'S ERROR MUST NOT FAIL THE DELETE, AND MUST NOT BE DROPPED.
//
// The commit has already happened by the time a post-commit hook runs, so returning the
// error from `Destroy` would be a lie -- the row is gone either way. But silently discarding
// it is how a failed scrub goes unnoticed, which is why it is stashed where a caller can
// reach it rather than swallowed.
func TestAHandlerErrorNeitherFailsTheDeleteNorVanishes(t *testing.T) {
	ctx := context.Background()
	destroyer := &mocks.FileReaderWriter{}
	destroyer.On("Destroy", mock.Anything, models.FileID(48)).Return(nil)

	f := &models.VideoFile{BaseFile: &models.BaseFile{ID: 48, Path: "/library/f.mp4"}}

	boom := errors.New("scrub failed")
	var seen error
	handler := func(_ context.Context, _ DestroyedFile) error {
		seen = boom
		return boom
	}

	db := mocks.NewDatabase()

	err := txn.WithTxn(ctx, db, func(tctx context.Context) error {
		return Destroy(tctx, destroyer, f, nil, false, handler)
	})

	assert.NoError(t, err, "a handler error must not be returned from a committed delete")
	assert.ErrorIs(t, seen, boom, "the handler did run and did fail")
}
