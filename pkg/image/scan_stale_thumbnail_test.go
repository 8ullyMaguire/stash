package image

// stash#5850, the SCAN side.
//
// A mutation that removed the legacy-thumbnail cleanup from this file SURVIVED
// the whole package: nothing constructed a ScanHandler cheaply enough to drive
// Handle, so the line was untestable where it lived. The fix is the seam, not
// another mock -- removeStaleThumbnails takes the two hashes and nothing else,
// so the decision can be exercised directly.
//
// A test of a helper is not a test of its caller, so TestTheCleanupIsWiredInto
// the scan path below pins the call site separately, by reading the source.
// That is the weakest of the three kinds of check and it is here because the
// alternative is a collaborator set too heavy to fake, which is the very thing
// that hid this line.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	staleHash5850 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	freshHash5850 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	otherHash5850 = "cccccccccccccccccccccccccccccccc"
)

func newScanHandlerPaths(t *testing.T) (*ScanHandler, *paths.Paths) {
	t.Helper()
	p := paths.NewPaths(t.TempDir(), t.TempDir())
	return &ScanHandler{Paths: &p}, &p
}

// writeThumbFile creates a thumbnail at path, shard directory included.
func writeThumbFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("thumb"), 0o644))
}

// The upgrade case: a file whose content changed, thumbnailed before #5850, so
// only the .jpg exists under the old hash. This is the exact orphan the
// mutation removed and no test caught.
func TestAChangedFileClearsItsPreUpgradeJpegThumbnail(t *testing.T) {
	h, p := newScanHandlerPaths(t)

	legacy := p.Generated.GetLegacyThumbnailPath(staleHash5850, models.DefaultGthumbWidth)
	writeThumbFile(t, legacy)

	h.removeStaleThumbnails(staleHash5850, freshHash5850)

	assert.NoFileExists(t, legacy,
		"the pre-#5850 JPEG for the superseded checksum survived a content change; "+
			"GetThumbnailPath no longer resolves it, so nothing would ever clean it up")
}

// The current format too, not only the legacy one. Cleaning up only the legacy
// path would leave the .webp of the same orphan.
func TestAChangedFileClearsItsCurrentThumbnail(t *testing.T) {
	h, p := newScanHandlerPaths(t)

	current := p.Generated.GetThumbnailPath(staleHash5850, models.DefaultGthumbWidth)
	writeThumbFile(t, current)

	h.removeStaleThumbnails(staleHash5850, freshHash5850)

	assert.NoFileExists(t, current)
}

// When both exist, both go. One rename ago this was the same bug reported
// twice, so the two-call shape is deliberate and this pins it.
func TestAChangedFileClearsBothFormatsWhenBothExist(t *testing.T) {
	h, p := newScanHandlerPaths(t)

	current := p.Generated.GetThumbnailPath(staleHash5850, models.DefaultGthumbWidth)
	legacy := p.Generated.GetLegacyThumbnailPath(staleHash5850, models.DefaultGthumbWidth)
	writeThumbFile(t, current)
	writeThumbFile(t, legacy)

	h.removeStaleThumbnails(staleHash5850, freshHash5850)

	assert.NoFileExists(t, current)
	assert.NoFileExists(t, legacy)
}

// The live thumbnail must survive. This is the data-loss direction, and it is
// the reason the method is keyed on the OLD hash: a cleanup that resolved to
// the current path would delete the thumbnail the regeneration is about to
// write.
func TestAContentChangeLeavesTheNewThumbnailAlone(t *testing.T) {
	h, p := newScanHandlerPaths(t)

	fresh := p.Generated.GetThumbnailPath(freshHash5850, models.DefaultGthumbWidth)
	writeThumbFile(t, fresh)

	h.removeStaleThumbnails(staleHash5850, freshHash5850)

	assert.FileExists(t, fresh, "a content change deleted the NEW checksum's thumbnail")
}

// An unchanged file must not be cleaned up at all. Deleting on an unchanged
// checksum would make every rescan of a stable library delete and regenerate
// every thumbnail.
func TestAnUnchangedChecksumIsNotAContentChange(t *testing.T) {
	h, p := newScanHandlerPaths(t)

	current := p.Generated.GetThumbnailPath(freshHash5850, models.DefaultGthumbWidth)
	writeThumbFile(t, current)

	h.removeStaleThumbnails(freshHash5850, freshHash5850)

	assert.FileExists(t, current, "an unchanged file had its thumbnail deleted")
}

// Two empty hashes are not evidence of a change. This is the case that a
// naive `oldHash != newHash` gets wrong, and it deletes a live thumbnail on any
// file whose MD5 merely failed to compute.
func TestTwoMissingHashesAreNotAContentChange(t *testing.T) {
	h, p := newScanHandlerPaths(t)

	// Under a hash the caller never supplied -- so nothing should be touched.
	orphan := p.Generated.GetThumbnailPath(otherHash5850, models.DefaultGthumbWidth)
	writeThumbFile(t, orphan)

	h.removeStaleThumbnails("", "")

	assert.FileExists(t, orphan, "two empty hashes were treated as a change")
}

// One empty hash is equally undecidable: the new file's MD5 may simply not
// have been computed yet.
func TestAMissingNewHashIsNotAContentChange(t *testing.T) {
	h, p := newScanHandlerPaths(t)

	live := p.Generated.GetThumbnailPath(staleHash5850, models.DefaultGthumbWidth)
	writeThumbFile(t, live)

	h.removeStaleThumbnails(staleHash5850, "")

	assert.FileExists(t, live, "a missing new hash was treated as a change")
}

// A missing file is the normal case and must not panic or error: a thumbnail
// that was never generated leaves nothing to remove.
func TestCleaningUpAThumbnailThatWasNeverGeneratedIsQuiet(t *testing.T) {
	h, _ := newScanHandlerPaths(t)

	assert.NotPanics(t, func() {
		h.removeStaleThumbnails(staleHash5850, freshHash5850)
	})
}

// TestTheCleanupIsWiredIntoTheScanPath pins the CALL SITE, which the tests
// above deliberately do not reach.
//
// This is a source-reading test and it is the weakest kind: it proves a name
// appears in a function body, not that the function is called. It is here
// because the alternative is constructing a ScanHandler with CreatorUpdater,
// ScanGenerator, GalleryFinder, a plugin cache and a live transaction, and the
// cost of that is precisely what let the line go untested in the first place.
//
// It is paired with a control for the SCANNER, because a source-scanning test
// that matches nothing passes: TestTheCleanupScanIsNotVacuous fails if the
// pattern this file searches for stops matching the source at all.
func TestTheCleanupIsWiredIntoTheScanPath(t *testing.T) {
	src, err := os.ReadFile("scan.go")
	require.NoError(t, err)

	assert.Contains(t, string(src), "h.removeStaleThumbnails(",
		"removeStaleThumbnails is defined and tested but no longer called from "+
			"the scan path; the cleanup this file exists to protect is now dead code")
}

// The control for the control. If the needle in the test above ever stops
// matching -- renamed, reindented onto a different receiver -- that test would
// pass for the wrong reason, or fail for a reason that has nothing to do with
// the wiring. This asserts the scanner still finds what it is looking for.
func TestTheCleanupScanIsNotVacuous(t *testing.T) {
	src, err := os.ReadFile("scan.go")
	require.NoError(t, err)

	text := string(src)
	assert.Contains(t, text, "func (h *ScanHandler) removeStaleThumbnails(",
		"the method under test is gone or renamed; every test in this file is now "+
			"asserting against a name that no longer exists")
	assert.Contains(t, text, "GetLegacyThumbnailPath",
		"the legacy cleanup this milestone is about has been removed from scan.go entirely")
}
