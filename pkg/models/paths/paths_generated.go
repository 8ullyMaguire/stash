package paths

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
)

const thumbDirDepth int = 2
const thumbDirLength int = 2 // thumbDirDepth * thumbDirLength must be smaller than the length of checksum

type generatedPaths struct {
	Screenshots        string
	Thumbnails         string
	Vtt                string
	Markers            string
	Transcodes         string
	Downloads          string
	Tmp                string
	InteractiveHeatmap string
}

func newGeneratedPaths(path string) *generatedPaths {
	gp := generatedPaths{}
	gp.Screenshots = filepath.Join(path, "screenshots")
	gp.Thumbnails = filepath.Join(path, "thumbnails")
	gp.Vtt = filepath.Join(path, "vtt")
	gp.Markers = filepath.Join(path, "markers")
	gp.Transcodes = filepath.Join(path, "transcodes")
	gp.Downloads = filepath.Join(path, "download_stage")
	gp.Tmp = filepath.Join(path, "tmp")
	gp.InteractiveHeatmap = filepath.Join(path, "interactive_heatmaps")
	return &gp
}

func (gp *generatedPaths) GetTmpPath(fileName string) string {
	return filepath.Join(gp.Tmp, fileName)
}

// TempFile creates a temporary file using os.CreateTemp.
// It is the equivalent of calling os.CreateTemp using Tmp and pattern.
func (gp *generatedPaths) TempFile(pattern string) (*os.File, error) {
	if err := gp.EnsureTmpDir(); err != nil {
		logger.Warnf("Could not ensure existence of a temporary directory: %v", err)
	}
	return os.CreateTemp(gp.Tmp, pattern)
}

func (gp *generatedPaths) EnsureTmpDir() error {
	return fsutil.EnsureDir(gp.Tmp)
}

func (gp *generatedPaths) EmptyTmpDir() error {
	return fsutil.EmptyDir(gp.Tmp)
}

func (gp *generatedPaths) RemoveTmpDir() error {
	return fsutil.RemoveDir(gp.Tmp)
}

func (gp *generatedPaths) TempDir(pattern string) (string, error) {
	if err := gp.EnsureTmpDir(); err != nil {
		logger.Warnf("Could not ensure existence of a temporary directory: %v", err)
	}
	ret, err := os.MkdirTemp(gp.Tmp, pattern)
	if err != nil {
		return "", err
	}

	if err = fsutil.EmptyDir(ret); err != nil {
		logger.Warnf("could not recursively empty dir: %v", err)
	}

	return ret, nil
}

// thumbnailExt is the extension, and therefore the Content-Type, of a generated
// image thumbnail.
//
// It is .webp because WebP carries an alpha channel and JPEG does not, so a JPEG
// thumbnail of line art with transparency has an opaque background and reads as
// a black box (stash#5850). The extension is not cosmetic: http.ServeFile
// derives the response Content-Type from it, so a WebP payload written under a
// .jpg name is still served as image/jpeg and the browser will not composite it
// over the page background.
//
// The on-disk change orphans every previously generated .jpg thumbnail, which is
// the intended trade: they are regenerated on demand (the route falls back to
// encoding on the fly when the file is absent) and they are deleted with the
// image. GetLegacyThumbnailPath exists so the old ones can still be found and
// cleaned up rather than living forever.
const thumbnailExt = ".webp"

func (gp *generatedPaths) GetThumbnailPath(checksum string, width int) string {
	fname := fmt.Sprintf("%s_%d%s", checksum, width, thumbnailExt)
	return filepath.Join(gp.Thumbnails, fsutil.GetIntraDir(checksum, thumbDirDepth, thumbDirLength), fname)
}

// GetLegacyThumbnailPath is where a thumbnail generated before stash#5850 lives.
// It is not written to any more; it exists so a delete or a re-generate can
// clear the old file instead of leaving it orphaned in the thumbnails tree.
func (gp *generatedPaths) GetLegacyThumbnailPath(checksum string, width int) string {
	fname := fmt.Sprintf("%s_%d.jpg", checksum, width)
	return filepath.Join(gp.Thumbnails, fsutil.GetIntraDir(checksum, thumbDirDepth, thumbDirLength), fname)
}

// RemoveLegacyThumbnail deletes a pre-#5850 JPEG thumbnail if one is still there.
// A no-op when there is none, which is the normal case on a fresh install, so
// callers do not need to check first.
//
// Returns whether a file was actually removed, and swallows a removal failure
// deliberately: the new thumbnail is already written, so failing here would turn
// a successful regeneration into a reported error over a stale file. The stale
// file is a waste of disk, not a correctness problem.
func (gp *generatedPaths) RemoveLegacyThumbnail(checksum string, width int) bool {
	legacy := gp.GetLegacyThumbnailPath(checksum, width)
	if legacy == gp.GetThumbnailPath(checksum, width) {
		// Defensive: if the extensions ever converge, this must not delete the
		// thumbnail that was just generated.
		return false
	}
	if err := os.Remove(legacy); err != nil {
		return false
	}
	return true
}

func (gp *generatedPaths) GetClipPreviewPath(checksum string, width int) string {
	fname := fmt.Sprintf("%s_%d.webm", checksum, width)
	return filepath.Join(gp.Thumbnails, fsutil.GetIntraDir(checksum, thumbDirDepth, thumbDirLength), fname)
}
