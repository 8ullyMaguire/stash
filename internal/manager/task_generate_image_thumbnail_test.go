package manager

// stash#5850, the CALLER side.
//
// A mutation harness deleted the legacy-thumbnail cleanup from
// GenerateImageThumbnailTask.Start and killed nothing, because
// pkg/image/thumbnail_orphan_test.go drives the delete path and
// pkg/models/paths drives the helper -- and neither of them runs this task. A
// test of a helper is not a test of its caller.
//
// This drives the real Start(). The only thing stubbed is the encoder:
// NewThumbnailEncoder is called inside the task and cannot be injected, so
// instead the manager singleton is pointed at a real vips binary with a real
// source PNG. That is the seam that matters: the branch under test is
// production code in Start, and the assertion is an on-disk effect.
//
// The previous version of this file could not compile (undefined: fsutil), and
// carried 130 lines of commentary recording that it could not inject a fake
// encoder -- while the answer, withEncoder, was already used by the sibling
// stash#2149 test in this same package.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/image"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/paths"
	"github.com/stretchr/testify/require"
)

// transparentPNG is a 2x2 PNG whose top-left pixel is fully transparent and
// whose bottom-right pixel is opaque red, so the encoder has real alpha to
// carry and a real colour to preserve. Hardcoded rather than encoded at
// runtime: the point is that the bytes are known-good input, not that a
// particular encoder produced them.
var transparentPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x02,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0xC8, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9C, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
	0x00, 0x03, 0x01, 0x00, 0x18, 0xDD, 0x8D, 0xB0,
	0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44,
	0xAE, 0x42, 0x60, 0x82,
}

const testImageChecksum = "deadbeefdeadbeefdeadbeefdeadbeef"

// requireVips skips rather than silently passing. A skipped caller test is
// honest about not having run; a caller test that passes vacuously is the
// failure mode this file exists to prevent.
func requireVips(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("vips"); err != nil {
		t.Skip("vips not on PATH: the task's encoder cannot run, so the caller wiring is untested")
	}
	if image.GetVipsPath() == "" {
		t.Skip("image.GetVipsPath is empty: NewThumbnailEncoder would not install the vips encoder")
	}
}

// withThumbnailManager points the manager singleton at temp paths and a real
// vips, restoring whatever was there before. Same shape as the sibling
// stash#2149 helper, and for the same reason: GetInstance() panics when the
// singleton is nil, so the task cannot be driven without one.
func withThumbnailManager(t *testing.T) *paths.Paths {
	t.Helper()
	requireVips(t)

	generated := t.TempDir()
	p := paths.NewPaths(generated, generated)

	prev := instance
	// Config is not optional: Start reads mgr.Config.GetTranscodeInputArgs()
	// before it touches the encoder, so a Manager with a nil Config is a nil
	// dereference in production code, not a test artefact. InitializeEmpty()
	// gives the defaults with no config file, which is what a fresh install has.
	instance = &Manager{Paths: &p, Config: config.InitializeEmpty()}
	t.Cleanup(func() { instance = prev })
	return &p
}

// writeSource writes the PNG to a real file and returns its path, plus an
// Image whose primary file is that path at a size above the thumbnail width so
// required() is satisfied.
func writeSource(t *testing.T, sum string) (string, *models.Image) {
	t.Helper()

	dir := t.TempDir()
	src := filepath.Join(dir, "artwork.png")
	require.NoError(t, os.WriteFile(src, transparentPNG, 0o644))

	f := &models.ImageFile{
		BaseFile: &models.BaseFile{ID: 1, Path: src},
		Width:    2000,
		Height:   2000,
	}
	return src, &models.Image{
		ID:       1,
		Files:    models.NewRelatedFiles([]models.File{f}),
		Checksum: sum,
	}
}

// writeLegacy puts a pre-#5850 JPEG thumbnail in place, creating the shard.
func writeLegacy(t *testing.T, p *paths.Paths, sum string) string {
	t.Helper()
	legacy := p.Generated.GetLegacyThumbnailPath(sum, models.DefaultGthumbWidth)
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o755))
	require.NoError(t, os.WriteFile(legacy, []byte("legacy jpeg"), 0o644))
	return legacy
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// The core assertion: regenerating a thumbnail must clear the pre-upgrade JPEG.
// Deleting the RemoveLegacyThumbnail call from Start fails this.
func TestRegeneratingAThumbnailRemovesTheLegacyJpeg(t *testing.T) {
	p := withThumbnailManager(t)
	_, img := writeSource(t, testImageChecksum)
	legacy := writeLegacy(t, p, testImageChecksum)

	task := &GenerateImageThumbnailTask{Image: *img, Overwrite: true}
	task.Start(context.Background())

	require.NoFileExists(t, legacy,
		"the pre-#5850 JPEG survived a regeneration; it would sit in the same shard forever, "+
			"because the task's existence check only looks for the new name")
}

// The control: the task must actually produce the new thumbnail, or the test
// above passes merely because nothing ever ran. Without this, a task that
// returns early on every input satisfies the legacy assertion for free.
func TestRegeneratingAThumbnailWritesTheWebp(t *testing.T) {
	p := withThumbnailManager(t)
	_, img := writeSource(t, testImageChecksum)
	writeLegacy(t, p, testImageChecksum)

	task := &GenerateImageThumbnailTask{Image: *img, Overwrite: true}
	task.Start(context.Background())

	current := p.Generated.GetThumbnailPath(testImageChecksum, models.DefaultGthumbWidth)
	require.FileExists(t, current, "no thumbnail was written; the legacy test above is vacuous")
	assertWebpPayload(t, current)
}

// The payload must be a WebP and not a JPEG wearing a .webp name. The Content-
// Type is derived from the extension, so a correct extension over a wrong
// payload is the same defect one layer down.
func TestTheGeneratedThumbnailIsAWebpAndNotAJpeg(t *testing.T) {
	p := withThumbnailManager(t)
	_, img := writeSource(t, testImageChecksum)

	task := &GenerateImageThumbnailTask{Image: *img, Overwrite: true}
	task.Start(context.Background())

	current := p.Generated.GetThumbnailPath(testImageChecksum, models.DefaultGthumbWidth)
	require.FileExists(t, current)
	assertWebpPayload(t, current)
}

func assertWebpPayload(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(data), 12, "thumbnail is too short to carry any format header")

	// RIFF....WEBP is the WebP container magic.
	require.Equal(t, "RIFF", string(data[0:4]), "thumbnail is not a RIFF container")
	require.Equal(t, "WEBP", string(data[8:12]), "thumbnail is RIFF but not WebP")
}

// A regeneration that fails must NOT clear the legacy file. Removing it
// unconditionally -- on the error path as well as the success path -- would
// delete a thumbnail whose replacement was never written, losing both copies.
func TestAFailedRegenerationLeavesTheLegacyJpegInPlace(t *testing.T) {
	p := withThumbnailManager(t)
	_, img := writeSource(t, testImageChecksum)
	legacy := writeLegacy(t, p, testImageChecksum)

	// Point the source at a file that is not decodable, so GetThumbnail errors
	// and Start returns before writing anything.
	require.NoError(t, os.WriteFile(img.Files.Primary().Base().Path, []byte("not an image"), 0o644))

	task := &GenerateImageThumbnailTask{Image: *img, Overwrite: true}
	task.Start(context.Background())

	require.FileExists(t, legacy,
		"a failed regeneration removed the only thumbnail on disk; the legacy cleanup "+
			"must run after the new file is written, never before")
	require.False(t, fileExists(t, p.Generated.GetThumbnailPath(testImageChecksum, models.DefaultGthumbWidth)),
		"the task reported a thumbnail it did not write")
}

// When there is nothing to clean up, the task must still succeed. A cleanup
// that treated a missing legacy file as an error would make every regeneration
// on a fresh install log a failure.
func TestRegenerationSucceedsWithNoLegacyFilePresent(t *testing.T) {
	p := withThumbnailManager(t)
	_, img := writeSource(t, testImageChecksum)

	task := &GenerateImageThumbnailTask{Image: *img, Overwrite: true}
	task.Start(context.Background())

	require.FileExists(t, p.Generated.GetThumbnailPath(testImageChecksum, models.DefaultGthumbWidth))
}
