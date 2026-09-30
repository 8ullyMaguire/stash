package file

import (
	"context"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// stash#2773: "Stash is not aware that a file has changed if the file that
// replaced it has the same name."
//
// The reporter's repro, verbatim:
//
//	1. gallery folder `Test` with image00001 and image00002
//	2. run scan
//	3. rename image00001 -> image00003
//	4. rename image00002 -> image00001
//	5. run scan
//	6. "MD5 for file .../image00001.jpg is the same as that of
//	    .../image00002.jpg" -- stash thinks image00001 has not changed
//
// WHY THAT IS STUCK, and it is the mtime, not the name. After step 4 the file at
// Test/image00001.jpg holds DIFFERENT content (image00002's) than it did after
// step 2, but `mv` preserves mtime and both files were written in the same second,
// so for that one path BOTH of the predicates at scan.go:778 are false:
//
//	updated := !fileModTime.Equal(base.ModTime) || base.Basename != f.Basename
//
// Same path means the same basename, and mv preserved the mtime, so the file is
// classified unchanged and keeps the OLD file's fingerprints.
//
// The MD5 collision the reporter sees is a SYMPTOM of that, surfacing later once
// two paths hold the same bytes. It is not the bug.
//
// The fix adds Size to the comparison, which is free because the scanner already
// stores it. The honest boundary, asserted below, is that a same-size swap is NOT
// catchable without hashing the bytes -- so this is not full coverage and the test
// says so.

// buildScanFixture wires a Scanner over the mock repository with an existing file
// and returns the scanner plus the fingerprints the mock calculator will produce.
// Returns a *Scanner: Scanner embeds a sync.Map, so it must not be copied by
// value -- `go vet` catches that and it is a real bug rather than a lint nit.
func buildScanFixture(existingSize, scannedSize int64, modTime time.Time) (*Scanner, *models.VideoFile, *mocks.Database) {
	db := mocks.NewDatabase()

	existing := &models.VideoFile{
		BaseFile: &models.BaseFile{
			ID:             1,
			Path:           `C:\stash\Test\image00001.jpg`,
			Basename:       "image00001.jpg",
			ParentFolderID: 2,
			Size:           existingSize,
			DirEntry:       models.DirEntry{ModTime: modTime},
			Fingerprints: []models.Fingerprint{
				{Type: models.FingerprintTypeOshash, Fingerprint: "old-oshash"},
				{Type: models.FingerprintTypeMD5, Fingerprint: "old-md5"},
			},
		},
		Format:     "jpg",
		VideoCodec: "jpeg",
		AudioCodec: "",
		// A COMPLETE image. Without width/height/format populated,
		// isMissingMetadata fires and onUnchangedFile reports Updated:true for
		// that reason alone -- which made a correct fix look like it over-triggered.
		Width:  1920,
		Height: 1080,
	}

	newFingerprints := []models.Fingerprint{
		{Type: models.FingerprintTypeOshash, Fingerprint: "new-oshash"},
		{Type: models.FingerprintTypeMD5, Fingerprint: "new-md5"},
	}

	// The rescan path writes the file back, so the mock needs Update declared.
	// Without it the call panics -- which is itself a usable signal that the rescan
	// path was reached, but an intentional assertion is clearer.
	db.File.On("Update", mock.Anything, mock.Anything).Return(nil)

	scanner := &Scanner{
		FS: scanTestFS{caseSensitive: true},
		Repository: Repository{
			TxnManager: db,
			File:       db.File,
			Folder:     db.Folder,
		},
		FingerprintCalculator: scanTestFingerprintCalculator{fingerprints: newFingerprints},

		// isHandlerRequired returns TRUE when the filter list is EMPTY
		// (`accept := len(s.HandlerRequiredFilters) == 0`), so an unconfigured
		// scanner sends every unchanged file down the handler path, which sets
		// Updated:true and made a correct fix look like it over-triggered. A filter
		// that rejects everything gives the honest unchanged path.
		HandlerRequiredFilters: []Filter{FilterFunc(func(context.Context, models.File) bool { return false })},
	}

	return scanner, existing, db
}

func scannedFileFor(size int64, modTime time.Time) ScannedFile {
	return ScannedFile{
		BaseFile: &models.BaseFile{
			Path:     `C:\stash\Test\image00001.jpg`,
			Basename: "image00001.jpg",
			Size:     size,
			DirEntry: models.DirEntry{ModTime: modTime},
		},
		FS:   scanTestFS{caseSensitive: true},
		Info: scanTestFileInfo{name: "image00001.jpg", size: size, modTime: modTime},
	}
}

// THE FIX. Same path, same mtime (mv preserved it), DIFFERENT size -- so the
// content really was replaced underneath stash and it must rescan.
func TestScannerRescansWhenContentIsReplacedWithDifferentSizeAtTheSamePath(t *testing.T) {
	modTime := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	scanner, existing, _ := buildScanFixture(100, 250, modTime)

	fresh := scannedFileFor(250, modTime)

	result, err := scanner.onExistingFile(context.Background(), fresh, existing)
	require.NoError(t, err)
	require.NotNil(t, result, "the file must be rescanned, not treated as unchanged")

	// The fingerprints must have been RECALCULATED, not carried over: the mock
	// calculator returns new-md5, so FingerprintChanged proves the code reached it.
	assert.True(t, result.FingerprintChanged,
		"fingerprints must be recalculated, so the stale MD5 cannot survive")
	assert.True(t, result.Updated, "the file must be reported as updated")
	assert.False(t, result.IsUnchanged(), "and not as unchanged")
}

// THE POSITIVE CONTROL. Nothing changed at all -- same path, same size, same
// mtime. This must stay on the fast path, or every scan would rehash the library.
func TestScannerLeavesAFileAloneWhenNothingChanged(t *testing.T) {
	modTime := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	scanner, existing, _ := buildScanFixture(100, 100, modTime)

	fresh := scannedFileFor(100, modTime)

	result, err := scanner.onExistingFile(context.Background(), fresh, existing)
	require.NoError(t, err)

	// onUnchangedFile returns a non-nil result carrying the file, so the check is
	// Updated, NOT a nil result. Asserting nil here is a test bug that reads like
	// an over-broad fix -- which is exactly what I first wrote, and it made a
	// correct fix look wrong.
	require.NotNil(t, result)
	assert.False(t, result.Updated, "an unchanged file must not be rescanned")
	assert.False(t, result.FingerprintChanged, "and must not have its fingerprints recomputed")
	assert.True(t, result.IsUnchanged(), "IsUnchanged() is the intended accessor for this")
}

// The other half of the existing predicate, asserted so adding Size does not
// displace it: a changed mtime alone still triggers a rescan.
func TestScannerRescansWhenModTimeChangedAtTheSameSize(t *testing.T) {
	modTime := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	scanner, existing, _ := buildScanFixture(100, 100, modTime)

	fresh := scannedFileFor(100, modTime.Add(time.Hour))

	result, err := scanner.onExistingFile(context.Background(), fresh, existing)
	require.NoError(t, err)
	assert.NotNil(t, result, "a changed mtime must still trigger a rescan")
}

// And a changed basename at identical size and mtime, which is the #6326 case
// that predates this one.
func TestScannerRescansWhenBasenameChangedAtTheSameSizeAndModTime(t *testing.T) {
	modTime := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	scanner, existing, _ := buildScanFixture(100, 100, modTime)

	fresh := scannedFileFor(100, modTime)
	fresh.Basename = "image00001-RENAMED.jpg"

	result, err := scanner.onExistingFile(context.Background(), fresh, existing)
	require.NoError(t, err)
	assert.NotNil(t, result, "a changed basename must still trigger a rescan (#6326)")
}

// THE LIMITATION, stated as a test so it cannot be forgotten.
//
// The reporter's literal repro swaps two files of the SAME size, so after the fix
// it is still classified unchanged. Catching that requires reading the bytes, which
// would defeat the modtime fast path the scanner is built around.
//
// This test therefore documents the boundary and will FAIL if someone later makes
// same-size detection work -- which is the correct outcome, and the signal to
// update this comment rather than to be surprised.
func TestSameSizeSwapAtIdenticalMTimeIsNotDetectedByDesign(t *testing.T) {
	modTime := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	scanner, existing, _ := buildScanFixture(100, 100, modTime)

	// Same path, same size, same mtime: only the content differs, and nothing
	// cheap distinguishes it.
	fresh := scannedFileFor(100, modTime)

	result, err := scanner.onExistingFile(context.Background(), fresh, existing)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Updated,
		"a same-size same-mtime overwrite is indistinguishable without hashing; "+
			"if this now fails, detection was improved and this comment is stale")
}
