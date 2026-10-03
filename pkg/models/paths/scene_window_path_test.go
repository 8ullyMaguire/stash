package paths

// stash#3530 — a windowed scene's generated file must live in its OWN shard directory.
//
// GetVideoPreviewPath is shardedJoin(Screenshots, checksum, checksum+".mp4"), and shardedJoin derives
// the shard directory from the checksum. That is the reason the window suffix goes on the CHECKSUM
// rather than the filename: suffixing only the filename would give two scenes of one file different
// names inside ONE shared shard directory.
//
// The shard directory is derived from the first `depth*length` characters, so appending to the
// checksum does NOT change the shard — the prefix is unchanged. That is worth asserting explicitly,
// because it is the non-obvious consequence: the suffix separates the FILES while leaving them in
// the same shard, which is correct (sharding is about directory size, not about scene identity) and
// is why this is not a sharding regression.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A windowed checksum must produce a path that is genuinely different from the plain one, all the
// way down — directory AND filename.
func TestTheShardDirectoryHoldsDistinctFilesForTwoWindows(t *testing.T) {
	p := newTestPaths(t)
	sp := p.Scene

	const checksum = md5Hash
	first := sp.GetVideoPreviewPath(checksum + "_w0.000-240.000")
	second := sp.GetVideoPreviewPath(checksum + "_w240.000-480.000")

	assert.NotEqual(t, first, second,
		"two windows of one file must not resolve to the same preview path")

	// Both must still be SHARDED, at the same shard as the plain hash, because the shard is
	// derived from the checksum PREFIX and the window suffix comes after it.
	shard := filepath.Join("screenshots", "01", "23")
	assert.Contains(t, first, shard,
		"a windowed preview must be sharded like any other; the suffix must not defeat sharding")
	assert.Contains(t, second, shard,
		"and both windows share the shard, because sharding bounds DIRECTORY SIZE and the two "+
			"files are still in the same 50k-file directory the shard exists to break up")

	// The filenames must differ even though the directories do not.
	assert.NotEqual(t, filepath.Base(first), filepath.Base(second))
	assert.Equal(t, filepath.Dir(first), filepath.Dir(second))
}

// An unranged scene's path must be byte-identical to what it has always been.
//
// Every scene in every existing installation is unranged, so if this moves, every generated preview
// in every library stops resolving and is regenerated.
func TestAnUnrangedPreviewPathIsUnchanged(t *testing.T) {
	p := newTestPaths(t)

	assert.Equal(t, p.Scene.GetVideoPreviewPath(md5Hash), p.Scene.GetVideoPreviewPath(md5Hash),
		"the path for a plain checksum must not depend on anything else")
	assert.Contains(t, p.Scene.GetVideoPreviewPath(md5Hash), md5Hash+".mp4")
	assert.Contains(t, p.Scene.GetWebpPreviewPath(md5Hash), md5Hash+".webp")
}

// The suffixed path must be a legal filename. The window suffix contains a '-' and a '.', and a
// name that needs escaping would break on Windows or on a share.
func TestTheWindowSuffixIsALegalFilename(t *testing.T) {
	p := newTestPaths(t)

	got := filepath.Base(p.Scene.GetVideoPreviewPath(md5Hash + "_w60.000-300.000"))
	assert.Equal(t, md5Hash+"_w60.000-300.000.mp4", got)
	assert.NotContains(t, got, string(filepath.Separator),
		"the suffix must not introduce a path separator")
}

// ResolveGeneratedFile must find a windowed preview at its sharded location, and must still find an
// UNRANGED preview at its legacy flat location.
//
// The second half is the one that is easy to break and silent when it does: a missing legacy
// fallback means every already-generated preview 404s after an upgrade, and nothing errors.
func TestResolveFindsAWindowedPreviewAndStillFindsALegacyOne(t *testing.T) {
	p := newTestPaths(t)
	sp := p.Scene

	// A windowed preview, written at the sharded location.
	windowedKey := md5Hash + "_w60.000-300.000"
	windowed := sp.GetVideoPreviewPath(windowedKey)
	require.NoError(t, os.MkdirAll(filepath.Dir(windowed), 0o755))
	require.NoError(t, os.WriteFile(windowed, []byte("windowed preview"), 0o644))

	assert.Equal(t, windowed, ResolveGeneratedFile(windowed, sp.GetLegacyVideoPreviewPath(windowedKey)),
		"a windowed preview must resolve at its sharded location")

	// An unranged preview, left at the PRE-SHARDING flat path, as an existing installation has it.
	legacy := sp.GetLegacyVideoPreviewPath(md5Hash)
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o755))
	require.NoError(t, os.WriteFile(legacy, []byte("old preview"), 0o644))

	assert.Equal(t, legacy, ResolveGeneratedFile(sp.GetVideoPreviewPath(md5Hash), legacy),
		"an unranged preview at the legacy flat path must STILL be found, or every preview in "+
			"every existing installation 404s and regenerates after an upgrade")

	// A windowed key must NOT be able to reach an unwindowed legacy file, and there are TWO
	// separate reasons it cannot, which is why this is asserted both ways:
	//
	//   isValidGeneratedChecksum only accepts 16 or 32 hex characters, so a suffixed checksum
	//   has NO legacy path at all -- LegacyGeneratedFilePath returns "". That is a deliberate
	//   guard from stash#2824 (it stops a caller-supplied string escaping the directory), and
	//   it happens to make the cross-window fallback impossible.
	//
	//   and even if a legacy path were offered, it would have to be for the SAME key.
	t.Run("a windowed key has no legacy path", func(t *testing.T) {
		key := md5Hash + "_w60.000-300.000"
		assert.Equal(t, "", newTestPaths(t).Scene.GetLegacyVideoPreviewPath(key),
			"a suffixed checksum must not produce a legacy path: it is not 16 or 32 hex characters, "+
				"so a windowed key can never fall back to an unwindowed file's location")
	})

	t.Run("a windowed key does not resolve another scene's preview", func(t *testing.T) {
		clean := newTestPaths(t)
		key := md5Hash + "_w60.000-300.000"

		// Scene A's preview, at the plain path.
		plain := clean.Scene.GetLegacyVideoPreviewPath(md5Hash)
		require.NoError(t, os.MkdirAll(filepath.Dir(plain), 0o755))
		require.NoError(t, os.WriteFile(plain, []byte("scene A"), 0o644))

		// Scene B resolves with ITS OWN key for both halves, which is how every call site is
		// wired. It must report missing rather than serve A's file.
		assert.Equal(t, "",
			ResolveGeneratedFile(
				clean.Scene.GetVideoPreviewPath(key),
				clean.Scene.GetLegacyVideoPreviewPath(key)),
			"serving scene A's preview for scene B is the exact bug this key change exists to "+
				"prevent; it must return empty so the caller regenerates")

	})
}

// The deleter must be able to find a windowed file for removal. SceneGeneratedFiles is the single
// entry point for that, and a file it cannot name is a file left on disk forever.
func TestTheDeleterCanNameAWindowedPreview(t *testing.T) {
	p := newTestPaths(t)

	key := md5Hash + "_w60.000-300.000"
	sharded := p.Scene.GetVideoPreviewPath(key)
	require.NoError(t, os.MkdirAll(filepath.Dir(sharded), 0o755))
	require.NoError(t, os.WriteFile(sharded, []byte("windowed preview"), 0o644))

	var found []string
	for _, f := range SceneGeneratedFiles(*p, key) {
		found = append(found, ResolveForDelete(f)...)
	}

	assert.Contains(t, found, sharded,
		"SceneGeneratedFiles(key) must name a windowed preview, or deleting the scene leaves it on "+
			"disk forever -- silently, since nothing reports a leftover generated file")
}
