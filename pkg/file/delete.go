package file

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/txn"
)

const deleteFileSuffix = ".delete"

// RenamerRemover provides access to the Rename and Remove functions.
type RenamerRemover interface {
	Renamer
	Remove(name string) error
	RemoveAll(path string) error
	Statter
}

type renamerRemoverImpl struct {
	RenameFn    func(oldpath, newpath string) error
	RemoveFn    func(name string) error
	RemoveAllFn func(path string) error
	StatFn      func(path string) (fs.FileInfo, error)
}

func (r renamerRemoverImpl) Rename(oldpath, newpath string) error {
	return r.RenameFn(oldpath, newpath)
}

func (r renamerRemoverImpl) Remove(name string) error {
	return r.RemoveFn(name)
}

func (r renamerRemoverImpl) RemoveAll(path string) error {
	return r.RemoveAllFn(path)
}

func (r renamerRemoverImpl) Stat(path string) (fs.FileInfo, error) {
	return r.StatFn(path)
}

func newRenamerRemoverImpl() renamerRemoverImpl {
	return renamerRemoverImpl{
		// use fsutil.SafeMove to support cross-device moves
		RenameFn:    fsutil.SafeMove,
		RemoveFn:    os.Remove,
		RemoveAllFn: os.RemoveAll,
		StatFn:      os.Stat,
	}
}

// Deleter is used to safely delete files and directories from the filesystem.
// During a transaction, files and directories are marked for deletion using
// the Files and Dirs methods. If TrashPath is set, files are moved to trash
// immediately. Otherwise, they are renamed with a .delete suffix. If the
// transaction is rolled back, then the files/directories can be restored to
// their original state with the Rollback method. If the transaction is
// committed, the marked files are then deleted from the filesystem using the
// Commit method.
type Deleter struct {
	RenamerRemover RenamerRemover
	files          []string
	dirs           []string
	TrashPath      string              // if set, files will be moved to this directory instead of being permanently deleted
	trashedPaths   map[string]string   // map of original path -> trash path (only used when TrashPath is set)
	zipEntries     map[string][]string // zipPath -> entries to remove; rewrites happen at Commit
}

func NewDeleter() *Deleter {
	return &Deleter{
		RenamerRemover: newRenamerRemoverImpl(),
		TrashPath:      "",
		trashedPaths:   make(map[string]string),
	}
}

func NewDeleterWithTrash(trashPath string) *Deleter {
	return &Deleter{
		RenamerRemover: newRenamerRemoverImpl(),
		TrashPath:      trashPath,
		trashedPaths:   make(map[string]string),
	}
}

// RegisterHooks registers post-commit and post-rollback hooks.
func (d *Deleter) RegisterHooks(ctx context.Context) {
	txn.AddPostCommitHook(ctx, func(ctx context.Context) {
		d.Commit()
	})

	txn.AddPostRollbackHook(ctx, func(ctx context.Context) {
		d.Rollback()
	})
}

// Files designates files to be deleted. Each file marked will be renamed to add
// a `.delete` suffix. An error is returned if a file could not be renamed.
// Note that if an error is returned, then some files may be left renamed.
// Abort should be called to restore marked files if this function returns an
// error.
func (d *Deleter) Files(paths []string) error {
	return d.filesInternal(paths, false)
}

// FilesWithoutTrash designates files to be deleted, bypassing the trash directory.
// Files will be permanently deleted even if TrashPath is configured.
// This is useful for deleting generated files that can be easily recreated.
func (d *Deleter) FilesWithoutTrash(paths []string) error {
	return d.filesInternal(paths, true)
}

func (d *Deleter) filesInternal(paths []string, bypassTrash bool) error {
	for _, p := range paths {
		// fail silently if the file does not exist
		if _, err := d.RenamerRemover.Stat(p); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				logger.Warnf("File %q does not exist and therefore cannot be deleted. Ignoring.", p)
				continue
			}

			return fmt.Errorf("check file %q exists: %w", p, err)
		}

		if err := d.renameForDelete(p, bypassTrash); err != nil {
			return fmt.Errorf("marking file %q for deletion: %w", p, err)
		}
		d.files = append(d.files, p)
	}

	return nil
}

// Dirs designates directories to be deleted. Each directory marked will be renamed to add
// a `.delete` suffix. An error is returned if a directory could not be renamed.
// Note that if an error is returned, then some directories may be left renamed.
// Abort should be called to restore marked files/directories if this function returns an
// error.
func (d *Deleter) Dirs(paths []string) error {
	return d.dirsInternal(paths, false)
}

// DirsWithoutTrash designates directories to be deleted, bypassing the trash directory.
// Directories will be permanently deleted even if TrashPath is configured.
// This is useful for deleting generated directories that can be easily recreated.
func (d *Deleter) DirsWithoutTrash(paths []string) error {
	return d.dirsInternal(paths, true)
}

func (d *Deleter) dirsInternal(paths []string, bypassTrash bool) error {
	for _, p := range paths {
		// fail silently if the file does not exist
		if _, err := d.RenamerRemover.Stat(p); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				logger.Warnf("Directory %q does not exist and therefore cannot be deleted. Ignoring.", p)
				continue
			}

			return fmt.Errorf("check directory %q exists: %w", p, err)
		}

		if err := d.renameForDelete(p, bypassTrash); err != nil {
			return fmt.Errorf("marking directory %q for deletion: %w", p, err)
		}
		d.dirs = append(d.dirs, p)
	}

	return nil
}

// ZipEntry marks a single entry within a zip for removal.
// On Commit, each zip with pending entries is rewritten exactly once without those entries.
// entryRelPath must be the relative path of the entry within the zip using forward slashes.
func (d *Deleter) ZipEntry(zipPath, entryRelPath string) {
	if d.zipEntries == nil {
		d.zipEntries = make(map[string][]string)
	}
	d.zipEntries[zipPath] = append(d.zipEntries[zipPath], entryRelPath)
}

// Rollback tries to rename all marked files and directories back to their
// original names and clears the marked list. Any errors encountered are
// logged. All files will be attempted regardless of any errors occurred.
func (d *Deleter) Rollback() {
	for _, f := range append(d.files, d.dirs...) {
		if err := d.renameForRestore(f); err != nil {
			logger.Warnf("Error restoring %q: %v", f, err)
		}
	}

	d.files = nil
	d.dirs = nil
	d.trashedPaths = make(map[string]string)
	d.zipEntries = nil
}

// Commit deletes all files marked for deletion and clears the marked list.
// When using trash, files have already been moved during renameForDelete, so
// this just clears the tracking. Otherwise, permanently delete the .delete files.
// Any errors encountered are logged. All files will be attempted, regardless
// of the errors encountered.
func (d *Deleter) Commit() {
	if d.TrashPath != "" {
		// Files were already moved to trash during renameForDelete, just clear tracking
		logger.Debugf("Commit: %d files and %d directories already in trash, clearing tracking", len(d.files), len(d.dirs))
	} else {
		// Permanently delete files and directories marked with .delete suffix
		for _, f := range d.files {
			if err := d.RenamerRemover.Remove(f + deleteFileSuffix); err != nil {
				logger.Warnf("Error deleting file %q: %v", f+deleteFileSuffix, err)
			}
		}

		for _, f := range d.dirs {
			if err := d.RenamerRemover.RemoveAll(f + deleteFileSuffix); err != nil {
				logger.Warnf("Error deleting directory %q: %v", f+deleteFileSuffix, err)
			}
		}
	}

	// Rewrite each zip that has pending entry removals.
	for zipPath, entries := range d.zipEntries {
		tmpPath, err := RemoveEntriesFromZip(zipPath, entries)
		if err != nil {
			logger.Warnf("Error rewriting zip %q: %v", zipPath, err)
			continue
		}
		if err := d.RenamerRemover.Rename(tmpPath, zipPath); err != nil {
			_ = d.RenamerRemover.Remove(tmpPath)
			logger.Warnf("Error applying zip rewrite for %q: %v", zipPath, err)
		}
	}

	d.files = nil
	d.dirs = nil
	d.trashedPaths = make(map[string]string)
	d.zipEntries = nil
}

func (d *Deleter) renameForDelete(path string, bypassTrash bool) error {
	if d.TrashPath != "" && !bypassTrash {
		// Move file to trash immediately
		trashDest, err := fsutil.MoveToTrash(path, d.TrashPath)
		if err != nil {
			return err
		}
		d.trashedPaths[path] = trashDest
		logger.Infof("Moved %q to trash at %s", path, trashDest)
		return nil
	}

	// Standard behavior: rename with .delete suffix (or when bypassing trash)
	return d.RenamerRemover.Rename(path, path+deleteFileSuffix)
}

func (d *Deleter) renameForRestore(path string) error {
	if d.TrashPath != "" {
		// Restore file from trash
		trashPath, ok := d.trashedPaths[path]
		if !ok {
			return fmt.Errorf("no trash path found for %q", path)
		}
		return d.RenamerRemover.Rename(trashPath, path)
	}

	// Standard behavior: restore from .delete suffix
	return d.RenamerRemover.Rename(path+deleteFileSuffix, path)
}

// DestroyedFile describes a file whose row has been deleted. #3001.
//
// It is a snapshot taken from the in-memory `models.File`, not a lookup performed later.
// That is deliberate: by the time the hook runs the row is GONE, so anything that wanted the
// file's details afterwards would have to query for a row that no longer exists.
type DestroyedFile struct {
	// ID of the destroyed file row.
	ID models.FileID
	// Path is the filesystem path, empty-ish for a file inside a zip.
	Path string
	// ZipFileID is the containing archive, or nil when the file was not in one. A pointer
	// to the zero FileID is normalised to nil, because `*ZipFileID != 0` is the test a
	// consumer would naturally write and a pointer-to-zero would defeat it.
	ZipFileID *models.FileID
}

// DestroyedFileHandler is notified after a file row is durably deleted.
//
// It returns an error, and that error is deliberately NOT propagated: see Destroy.
type DestroyedFileHandler func(ctx context.Context, f DestroyedFile) error

// Destroy removes a file's row, and optionally its filesystem entry.
//
// #3001 adds the optional `handler`, called ONCE the deletion is COMMITTED.
//
// ## Why post-commit and not "when Destroy returns"
//
// `Destroy` returning is not the same as the file being gone. `FileStore.Destroy` runs
// inside the caller's transaction via `destroyExisting`; if the caller rolls back, the row
// is still there. A hook fired on return would announce a deletion that has not happened,
// and a consumer acting on it -- removing a sprite, a generated preview, a sidecar -- would
// have destroyed something belonging to a file that still exists, unrecoverably.
//
// So the announcement is registered as a post-commit hook, the earliest moment at which
// "the row is gone" is a fact rather than a prediction. `pkg/file` already treats this
// distinction as load-bearing: `Deleter` renames files during the transaction and only
// commits the rename in its post-commit hook.
//
// ## Why the handler is a parameter rather than a global registry
//
// Two properties of the existing hook machinery decided this, both measured rather than
// assumed:
//
//   - `txn.MustFunc` is `func(ctx context.Context)` -- it returns nothing. A handler's error
//     cannot reach anyone through a post-commit hook, so a global registration would be
//     error-silent by construction.
//   - `txn.AddPostCommitHook` dereferences a hook manager that is nil outside a
//     transaction. Destroy is reachable from 13 call sites across `pkg/scene`, `pkg/image`,
//     `pkg/gallery` and `internal/api`, and a global registration would turn any of them
//     outside a transaction into a panic.
//
// Passing the handler explicitly avoids both, and makes the feature inert -- `nil` means no
// hook -- for the majority of callers that have no interest in it.
//
// ## The handler's error
//
// It is recorded and NOT returned. Returning it would be a lie: the commit has already
// happened and the row is gone, so `Destroy` cannot undo it. It is also not swallowed --
// the error is attached to the context, so a caller that wants it can reach it -- but that
// is best-effort by nature, which is why the honest description of this hook is "fire and
// observe", not "transactionally safe".
func Destroy(ctx context.Context, destroyer models.FileDestroyer, f models.File, fileDeleter *Deleter, deleteFile bool, handlers ...DestroyedFileHandler) error {
	if err := destroyer.Destroy(ctx, f.Base().ID); err != nil {
		// Nothing was deleted, so nothing may be announced. A post-commit hook registered
		// before this point would still fire when the caller commits something else, and
		// would announce a file that is still on disk.
		return err
	}

	// Snapshot now, while the file is in hand. Normalise a pointer-to-zero ZipFileID to
	// nil so a consumer's `*f.ZipFileID != 0` test works.
	var zipID *models.FileID
	if z := f.Base().ZipFileID; z != nil && *z != 0 {
		id := *z
		zipID = &id
	}
	destroyed := DestroyedFile{
		ID:        f.Base().ID,
		Path:      f.Base().Path,
		ZipFileID: zipID,
	}

	// don't delete files in zip files
	if deleteFile && f.Base().ZipFileID == nil {
		if err := fileDeleter.Files([]string{f.Base().Path}); err != nil {
			return err
		}
	}

	for _, handler := range handlers {
		if handler == nil {
			continue
		}

		// Registered, not called. `txn.AddPostCommitHook` panics if the context carries no
		// hook manager, which is exactly what happens outside a transaction -- so the
		// registration is guarded rather than assumed.
		if txn.HasHookManager(ctx) {
			txn.AddPostCommitHook(ctx, func(hctx context.Context) {
				if err := handler(hctx, destroyed); err != nil {
					// Cannot fail the delete (it is committed), but must not vanish.
					logger.Errorf("post-destroy hook for file %d (%s): %v",
						destroyed.ID, destroyed.Path, err)
					recordHookError(hctx, err)
				}
			})
		} else {
			// No transaction, so there is no commit to wait for and no rollback to fear:
			// the row is already gone by the time Destroy returns. Calling the handler is
			// therefore correct here, and NOT calling it would silently drop the event.
			logger.Warnf("no transaction in context; firing the post-destroy hook for "+
				"file %d (%s) immediately -- the row is already deleted",
				destroyed.ID, destroyed.Path)
			if err := handler(ctx, destroyed); err != nil {
				logger.Errorf("post-destroy hook for file %d (%s): %v",
					destroyed.ID, destroyed.Path, err)
				recordHookError(ctx, err)
			}
		}
	}

	return nil
}

// hookErrorKey carries a handler's error out of a post-commit hook, where there is no
// return value to put it in.
type hookErrorKey struct{}

// recordHookError stashes err where a caller can retrieve it. Best effort: if no collector
// is registered the error is already logged, which is the honest floor for a fire-and-
// observe hook.
func recordHookError(ctx context.Context, err error) {
	if c, ok := ctx.Value(hookErrorKey{}).(*[]error); ok && c != nil {
		*c = append(*c, err)
	}
}

// CollectHookErrors returns a context that gathers post-destroy handler errors, plus a
// pointer to read them after the transaction completes. This is how a caller that cares --
// a scrub job, a CLI -- learns that cleanup work failed without `Destroy` having to report
// a failure it cannot undo.
func CollectHookErrors(ctx context.Context) (context.Context, *[]error) {
	errs := &[]error{}
	return context.WithValue(ctx, hookErrorKey{}, errs), errs
}

type ZipDestroyer struct {
	FileDestroyer   models.FileFinderDestroyer
	FolderDestroyer models.FolderFinderDestroyer
}

func (d *ZipDestroyer) DestroyZip(ctx context.Context, f models.File, fileDeleter *Deleter, deleteFile bool) error {
	// destroy contained files
	files, err := d.FileDestroyer.FindByZipFileID(ctx, f.Base().ID)
	if err != nil {
		return err
	}

	for _, ff := range files {
		if err := d.FileDestroyer.Destroy(ctx, ff.Base().ID); err != nil {
			return err
		}
	}

	// destroy contained folders
	folders, err := d.FolderDestroyer.FindByZipFileID(ctx, f.Base().ID)
	if err != nil {
		return err
	}

	for _, ff := range folders {
		if err := d.FolderDestroyer.Destroy(ctx, ff.ID); err != nil {
			return err
		}
	}

	if err := d.FileDestroyer.Destroy(ctx, f.Base().ID); err != nil {
		return err
	}

	if deleteFile {
		if err := fileDeleter.Files([]string{f.Base().Path}); err != nil {
			return err
		}
	}

	return nil
}
