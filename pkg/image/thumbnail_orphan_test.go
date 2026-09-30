package image

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/pkg/file"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stash#5850 -- the thumbnail format change orphans every .jpg thumbnail already
// on disk, and the helper that cleans them up does nothing if its CALLERS are
// not wired to it.
//
// A mutation harness deleted the legacy cleanup from all three call sites and
// killed nothing, because the suite tested RemoveLegacyThumbnail directly and
// never observed it being called. A test of a helper is not a test of its
// caller; these go through the callers, which is the only place the wiring is
// visible.
//
// The assertion is the on-disk effect rather than a returned list. FilesWithoutTrash
// appends to an unexported field, so there is nothing to inspect without reaching
// into package internals -- but it renames each marked file to `<path>.delete`,
// which is public, observable, and stronger: it proves the file was really
// handled, not merely listed.

// writeThumb creates a thumbnail file at path, creating its shard directory.
func writeThumb(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func newTestFileDeleter(t *testing.T) (*FileDeleter, *paths.Paths) {
	t.Helper()
	p := paths.NewPaths(t.TempDir(), t.TempDir())
	// file.NewDeleter rather than a struct literal: the RenamerRemover
	// implementation is unexported, and hand-rolling the struct would leave it
	// nil and panic in Stat rather than failing the assertion.
	return &FileDeleter{
		Deleter: file.NewDeleter(),
		Paths:   &p,
	}, &p
}

// markedForDelete reports whether a file was handed to the Deleter, by looking
// for the .delete suffix that FilesWithoutTrash adds.
func markedForDelete(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path + ".delete")
	return err == nil
}

func testImage(sum string) *models.Image {
	return &models.Image{Checksum: sum}
}

// TestDeletingAnImageRemovesItsLegacyJpegThumbnail is the upgrade case: the
// image was thumbnailed before #5850, so only the .jpg exists. A cleanup that
// checked only the current path would orphan it forever.
func TestDeletingAnImageRemovesItsLegacyJpegThumbnail(t *testing.T) {
	d, p := newTestFileDeleter(t)
	sum := "aaaaaaaabbbbbbbbccccccccdddddddd"

	legacy := p.Generated.GetLegacyThumbnailPath(sum, models.DefaultGthumbWidth)
	writeThumb(t, legacy, "old jpeg")

	require.NoError(t, d.MarkGeneratedFiles(testImage(sum)))

	assert.True(t, markedForDelete(t, legacy),
		"the pre-upgrade .jpg thumbnail was not marked for deletion, so it is orphaned on disk")
}

func TestDeletingAnImageAlsoRemovesItsCurrentThumbnail(t *testing.T) {
	d, p := newTestFileDeleter(t)
	sum := "11111111222222223333333344444444"

	current := p.Generated.GetThumbnailPath(sum, models.DefaultGthumbWidth)
	writeThumb(t, current, "new webp")

	require.NoError(t, d.MarkGeneratedFiles(testImage(sum)))
	assert.True(t, markedForDelete(t, current), "the current .webp thumbnail was not marked")
}

func TestDeletingAnImageMarksBothFormatsWhenBothExist(t *testing.T) {
	d, p := newTestFileDeleter(t)
	sum := "99999999000000001111111122222222"

	legacy := p.Generated.GetLegacyThumbnailPath(sum, models.DefaultGthumbWidth)
	current := p.Generated.GetThumbnailPath(sum, models.DefaultGthumbWidth)
	writeThumb(t, legacy, "old jpeg")
	writeThumb(t, current, "new webp")

	require.NoError(t, d.MarkGeneratedFiles(testImage(sum)))

	assert.True(t, markedForDelete(t, legacy), "the .jpg was not marked")
	assert.True(t, markedForDelete(t, current), "the .webp was not marked")
}

func TestDeletingAnImageWithNoThumbnailsMarksNothing(t *testing.T) {
	d, p := newTestFileDeleter(t)
	sum := "ffffffff00000000eeeeeeeeffffffff"

	// No files written at all: the shard does not exist.
	require.NoError(t, d.MarkGeneratedFiles(testImage(sum)))

	assert.False(t, markedForDelete(t, p.Generated.GetThumbnailPath(sum, models.DefaultGthumbWidth)))
	assert.False(t, markedForDelete(t, p.Generated.GetLegacyThumbnailPath(sum, models.DefaultGthumbWidth)))
}

func TestDeletingAnImageLeavesAnUnrelatedImagesThumbnailAlone(t *testing.T) {
	// The negative control: the deleter must key on the image's OWN checksum.
	// Without this, a cleanup that scanned the whole shard directory would pass
	// every other test here.
	d, p := newTestFileDeleter(t)
	doomed := "12345678123456781234567812345678"
	bystander := "87654321876543218765432187654321"

	bystanderThumb := p.Generated.GetLegacyThumbnailPath(bystander, models.DefaultGthumbWidth)
	writeThumb(t, bystanderThumb, "someone else's jpeg")

	require.NoError(t, d.MarkGeneratedFiles(testImage(doomed)))

	assert.False(t, markedForDelete(t, bystanderThumb),
		"another image's thumbnail must not be touched")
	assert.FileExists(t, bystanderThumb)
}
