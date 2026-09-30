package file

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stash#7106: "Deleting an image in a zip gallery silently fails. The image
// initially disappears, but after a rescan, the image will reappear."
//
// THE BUG, and it was two lines in Destroy:
//
//	destroyer.Destroy(ctx, f.Base().ID)   // the DB row goes, the UI updates
//	if deleteFile && f.Base().ZipFileID == nil { ... }   // skipped for zip members
//	return nil                            // SUCCESS reported
//
// Skipping the filesystem delete is correct in itself -- a zip member's Path is
// synthetic (`/lib/gallery.zip/inner.jpg`) and `os.Rename` on it would fail. But
// it left the ARCHIVE untouched with nothing recording the pending removal, and
// the scanner re-walks a zip whenever it is new, its fingerprint changed, rescan
// is set, or a handler is required (task_scan.go:407). So the member was still
// in the archive, the rescan found it, and the row came back.
//
// Upstream PR #7107 fixes this by rewriting the archive without the member at
// Deleter.Commit. These tests pin the parts that matter to a user: the entry is
// gone for good, the rest of the archive survives, and the rewrite does not
// quietly downgrade the file's permissions.

func writeTestZip(t *testing.T, path string, mode os.FileMode, entries map[string]string, comment string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))

	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, f.Chmod(mode))

	w := zip.NewWriter(f)
	for name, content := range entries {
		ew, err := w.Create(name)
		require.NoError(t, err)
		_, err = ew.Write([]byte(content))
		require.NoError(t, err, "the entry must actually be written; a discarded Create "+
			"writer silently produces an archive of empty files")
	}
	require.NoError(t, w.SetComment(comment))
	require.NoError(t, w.Close())
}

func zipEntryContent(t *testing.T, path, name string) string {
	t.Helper()
	r, err := zip.OpenReader(path)
	require.NoError(t, err)
	defer r.Close()

	for _, f := range r.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		require.NoError(t, err)
		defer rc.Close()
		b, err := io.ReadAll(rc)
		require.NoError(t, err)
		return string(b)
	}
	t.Fatalf("entry %q not found in %s", name, path)
	return ""
}

func zipEntryNames(t *testing.T, path string) []string {
	t.Helper()
	r, err := zip.OpenReader(path)
	require.NoError(t, err)
	defer r.Close()

	var names []string
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	return names
}

// THE FIX. Removing an entry must actually remove it from the archive, which is
// what stops the rescan from resurrecting the row. This is the test the upstream
// PR did not have, and it is the one that fails against the old code.
func TestRemoveEntriesFromZipRemovesTheEntry(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "gallery.zip")
	writeTestZip(t, zipPath, 0o644, map[string]string{
		"keep1.jpg": "a",
		"gone.jpg":  "b",
		"keep2.jpg": "c",
	}, "archive comment")

	tmpPath, err := RemoveEntriesFromZip(zipPath, []string{"gone.jpg"})
	require.NoError(t, err)
	require.FileExists(t, tmpPath, "a temp file is returned for the caller to install")
	t.Cleanup(func() { _ = os.Remove(tmpPath) })

	names := zipEntryNames(t, tmpPath)
	assert.NotContains(t, names, "gone.jpg", "the removed entry must not be in the rewritten archive")
	assert.Contains(t, names, "keep1.jpg", "and the other entries must survive")
	assert.Contains(t, names, "keep2.jpg")

	// CONTENT must survive, not just the filenames. A rewrite that copies names
	// but drops bytes would pass every name-only assertion above and destroy the
	// user's media.
	assert.Equal(t, "a", zipEntryContent(t, tmpPath, "keep1.jpg"))
	assert.Equal(t, "c", zipEntryContent(t, tmpPath, "keep2.jpg"))

	// Install it the way Commit does, then re-read: this is the state a rescan
	// will actually see.
	require.NoError(t, os.Rename(tmpPath, zipPath))
	after := zipEntryNames(t, zipPath)
	assert.NotContains(t, after, "gone.jpg",
		"after the replace, a rescan walking this archive cannot find the deleted member, "+
			"so the row cannot come back -- this is the stash#7106 assertion")
}

// Removing EVERY entry leaves a valid, empty archive rather than a corrupt one.
func TestRemoveEntriesFromZipCanEmptyTheArchive(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "gallery.zip")
	writeTestZip(t, zipPath, 0o644, map[string]string{"a.jpg": "a", "b.jpg": "b"}, "")

	tmpPath, err := RemoveEntriesFromZip(zipPath, []string{"a.jpg", "b.jpg"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(tmpPath) })

	assert.Empty(t, zipEntryNames(t, tmpPath))
	// Still a readable zip: OpenReader would have failed on a truncated file.
	_, err = os.Stat(tmpPath)
	assert.NoError(t, err)
}

// An entry that is not present is not an error -- two images in one archive can
// be deleted in separate requests, and the second rewrite must not fail.
func TestRemoveEntriesFromZipToleratesAnAbsentEntry(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "gallery.zip")
	writeTestZip(t, zipPath, 0o644, map[string]string{"a.jpg": "a", "b.jpg": "b"}, "")

	tmpPath, err := RemoveEntriesFromZip(zipPath, []string{"a.jpg", "never-existed.jpg"})
	require.NoError(t, err, "an absent entry must not fail the rewrite")
	t.Cleanup(func() { _ = os.Remove(tmpPath) })

	assert.Equal(t, []string{"b.jpg"}, zipEntryNames(t, tmpPath))
}

// A missing archive is an error, not a silent success.
func TestRemoveEntriesFromZipErrorsOnAMissingArchive(t *testing.T) {
	_, err := RemoveEntriesFromZip(filepath.Join(t.TempDir(), "nope.zip"), []string{"a.jpg"})
	assert.Error(t, err, "a missing archive must report failure so the caller can log it")
}

// THE DEFECT I FIXED IN THE UPSTREAM PR: os.CreateTemp creates 0600, and the
// caller REPLACES the archive with the temp file, so every rewritten zip
// silently became owner-only. A media library served by another user or process
// loses group/other read after a single image delete.
func TestRemoveEntriesFromZipPreservesTheOriginalMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are not meaningful on windows")
	}
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "gallery.zip")
	// 0640 is the shape that matters: group readable, world NOT readable.
	writeTestZip(t, zipPath, 0o640, map[string]string{"a.jpg": "a", "b.jpg": "b"}, "")

	tmpPath, err := RemoveEntriesFromZip(zipPath, []string{"a.jpg"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(tmpPath) })

	fi, err := os.Stat(tmpPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), fi.Mode().Perm(),
		"the rewritten archive must keep the original's mode; os.CreateTemp's 0600 would "+
			"silently drop group/other read from a user's media library")
}

// The archive comment must survive -- libraries use it for their own metadata,
// and dropping it is a silent regression the upstream PR would have introduced.
func TestRemoveEntriesFromZipPreservesTheArchiveComment(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "gallery.zip")
	writeTestZip(t, zipPath, 0o644, map[string]string{"a.jpg": "a", "b.jpg": "b"}, "my library")

	tmpPath, err := RemoveEntriesFromZip(zipPath, []string{"a.jpg"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(tmpPath) })

	r, err := zip.OpenReader(tmpPath)
	require.NoError(t, err)
	defer r.Close()
	assert.Equal(t, "my library", r.Comment)
}

// A failed rewrite must leave no temp file behind. Disk-full and permission
// errors are the realistic triggers, and a stray *.tmp in the user's library is
// picked up by the scanner.
func TestRemoveEntriesFromZipLeavesNoTempFileOnFailure(t *testing.T) {
	// The library directory holds ONLY the bogus archive, so asserting it is
	// unchanged afterwards is meaningful. Two earlier versions got this wrong:
	// the first created the fixture in the directory it then asserted was empty
	// (impossible), and the second put that directory inside t.TempDir() -- which
	// t.TempDir CREATES, so the parent still listed one entry. Assert on a
	// specific set of names rather than "is empty", and give the fixture a
	// parent of its own.
	libDir := filepath.Join(t.TempDir(), "library")
	require.NoError(t, os.MkdirAll(libDir, 0o755))
	// A directory where the archive should be: OpenReader fails on it.
	zipPath := filepath.Join(libDir, "notazip.zip")
	require.NoError(t, os.MkdirAll(zipPath, 0o755))

	_, err := RemoveEntriesFromZip(zipPath, []string{"a.jpg"})
	require.Error(t, err)

	entries, readErr := os.ReadDir(libDir)
	require.NoError(t, readErr)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Equal(t, []string{"notazip.zip"}, names,
		"a failed rewrite must leave the library exactly as it was -- no *.tmp for the scanner to find")
}
