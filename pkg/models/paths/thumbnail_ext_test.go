package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stash#5850 -- the generated image thumbnail moved from .jpg to .webp, because
// JPEG has no alpha channel and a JPEG thumbnail of line art with transparency
// is an opaque black box.
//
// The extension is not cosmetic. http.ServeFile derives the response
// Content-Type from the file extension, so a WebP payload written under a .jpg
// name is still served as image/jpeg -- the payload would be correct and the
// browser would still render it wrong. These tests pin that coupling, and the
// orphan question, which is the part a format rename usually gets wrong.

const testChecksum5850 = "0123456789abcdef0123456789abcdef"

func newTestGeneratedPaths(t *testing.T) *generatedPaths {
	t.Helper()
	return newGeneratedPaths(t.TempDir())
}

func TestTheThumbnailExtensionIsWebp(t *testing.T) {
	gp := newTestGeneratedPaths(t)
	assert.Equal(t, ".webp", filepath.Ext(gp.GetThumbnailPath(testChecksum5850, 320)))
}

func TestTheThumbnailIsStillNamedForItsChecksumAndWidth(t *testing.T) {
	// A format change must not also change the identity of the file: the name
	// still has to be the thing the task's existence check looks for, or
	// thumbnails would regenerate on every scan forever.
	gp := newTestGeneratedPaths(t)
	name := filepath.Base(gp.GetThumbnailPath(testChecksum5850, 320))
	assert.Equal(t, testChecksum5850+"_320.webp", name)
}

func TestTheThumbnailIsStillSharded(t *testing.T) {
	// The change must not undo the #2824 sharding: a flat thumbnails directory
	// is the problem that issue was about.
	//
	// filepath.Base walks leaf-to-root, so the DEEPEST directory comes first in
	// the reversed reading below. GetIntraDir builds "01/23" for this checksum --
	// the leading characters, in order.
	gp := newTestGeneratedPaths(t)
	p := gp.GetThumbnailPath(testChecksum5850, 320)

	deepest := filepath.Base(filepath.Dir(p))              // "23"
	parent := filepath.Base(filepath.Dir(filepath.Dir(p))) // "01"
	assert.Equal(t, testChecksum5850[2:4], deepest)
	assert.Equal(t, testChecksum5850[0:2], parent)
}

func TestTheLegacyThumbnailIsAJpegUnderTheSameShard(t *testing.T) {
	// The old path has to remain derivable, or a pre-upgrade thumbnail cannot
	// be found to delete. Same directory, same name, different extension --
	// which is precisely what makes the orphan invisible if the cleanup is wrong.
	gp := newTestGeneratedPaths(t)
	legacy := gp.GetLegacyThumbnailPath(testChecksum5850, 320)
	assert.Equal(t, ".jpg", filepath.Ext(legacy))
	assert.Equal(t, testChecksum5850+"_320.jpg", filepath.Base(legacy))
	assert.Equal(t, filepath.Dir(gp.GetThumbnailPath(testChecksum5850, 320)), filepath.Dir(legacy),
		"the legacy file must sit in the same shard, or a delete would miss it")
}

func TestTheTwoThumbnailPathsAreDifferent(t *testing.T) {
	// If these ever became equal, RemoveLegacyThumbnail would delete the
	// thumbnail that was just generated. The guard is in the implementation;
	// this is the test that says the invariant is load-bearing.
	gp := newTestGeneratedPaths(t)
	assert.NotEqual(t,
		gp.GetLegacyThumbnailPath(testChecksum5850, 320),
		gp.GetThumbnailPath(testChecksum5850, 320))
}

func TestRemoveLegacyThumbnailDeletesTheOldFile(t *testing.T) {
	gp := newTestGeneratedPaths(t)

	legacy := gp.GetLegacyThumbnailPath(testChecksum5850, 320)
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o755))
	require.NoError(t, os.WriteFile(legacy, []byte("old jpeg"), 0o644))

	assert.True(t, gp.RemoveLegacyThumbnail(testChecksum5850, 320), "a file was there to remove")
	assert.NoFileExists(t, legacy)
}

func TestRemoveLegacyThumbnailIsANoOpWhenThereIsNothingToRemove(t *testing.T) {
	gp := newTestGeneratedPaths(t)
	// A fresh install: no legacy file, and this must not be an error.
	assert.False(t, gp.RemoveLegacyThumbnail(testChecksum5850, 320))
}

func TestRemoveLegacyThumbnailDoesNotTouchTheNewThumbnail(t *testing.T) {
	// The real hazard: the new file exists, the old one does not, and a cleanup
	// that resolves to the new path would delete the only copy.
	gp := newTestGeneratedPaths(t)

	current := gp.GetThumbnailPath(testChecksum5850, 320)
	require.NoError(t, os.MkdirAll(filepath.Dir(current), 0o755))
	require.NoError(t, os.WriteFile(current, []byte("new webp"), 0o644))

	gp.RemoveLegacyThumbnail(testChecksum5850, 320)

	assert.FileExists(t, current, "the current thumbnail must survive a legacy cleanup")
	data, err := os.ReadFile(current)
	require.NoError(t, err)
	assert.Equal(t, "new webp", string(data))
}

func TestRemoveLegacyThumbnailReturnsFalseWhenOnlyTheShardIsMissing(t *testing.T) {
	// No shard directory at all: os.Remove fails with ENOENT. That is the normal
	// state for a thumbnail that was never generated, and it must report false
	// rather than panicking or reporting a removal that did not happen.
	gp := newTestGeneratedPaths(t)
	assert.False(t, gp.RemoveLegacyThumbnail(testChecksum5850, 320))
}

// TestTheTwoPathsDifferOnlyInExtension pins the fact the guard above rests on.
//
// RemoveLegacyThumbnail refuses to run when its two paths are equal, and that
// branch is CURRENTLY UNREACHABLE: thumbnailExt is a const (".webp") and
// GetLegacyThumbnailPath hardcodes ".jpg", so the names can never collide and
// no test can drive the branch. A mutation replacing the guard with `if false`
// therefore survives, and that is the correct verdict -- not a hole in the
// tests.
//
// What this test asserts is the precondition, which is a real property and
// does drift: if a future edit made the two extensions converge, this fails
// first and says why, rather than the guard quietly becoming load-bearing for
// a reason nobody documented.
//
// The honest summary of the earlier version of this file: it was named
// TestRemoveLegacyThumbnailDoesNotDeleteCurrentWhenExtensionsAreEqual and
// ASSIGNED to thumbnailExt to force the collision. It does not compile, because
// a const cannot be assigned -- the branch was never reachable by any test.
func TestTheTwoPathsDifferOnlyInExtension(t *testing.T) {
	gp := newTestGeneratedPaths(t)

	legacy := gp.GetLegacyThumbnailPath(testChecksum5850, 320)
	current := gp.GetThumbnailPath(testChecksum5850, 320)

	require.Equal(t, filepath.Dir(legacy), filepath.Dir(current),
		"they must stay in the same shard or a delete misses the old file")
	require.NotEqual(t, filepath.Base(legacy), filepath.Base(current),
		"the two thumbnail names have converged, so RemoveLegacyThumbnail's "+
			"equality guard is now load-bearing and nothing tests that branch")
}
