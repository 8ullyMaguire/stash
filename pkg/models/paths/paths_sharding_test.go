package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Sharding of the per-scene generated files. (stash#2824)
//
// The report measured ~50k files in generated/screenshots and generated/vtt in a
// single flat directory, where listing gets slow and every disk operation on the
// share does too. These tests pin two things that must both hold:
//
//   1. NEW files are sharded, so no directory grows without bound.
//   2. EXISTING files at the old flat paths are still found, or every preview
//      404s and every scene re-transcodes after the upgrade.
//
// (2) is the one that matters most and is easiest to get wrong, because a
// missing fallback is silent: nothing errors, the file is just not there.

const (
	// A 32-char md5 and a 16-char oshash -- the two Stash actually uses.
	md5Hash   = "0123456789abcdef0123456789abcdef"
	oshashStr = "0123456789abcdef"
)

func newTestPaths(t *testing.T) *Paths {
	t.Helper()
	dir := t.TempDir()
	p := NewPaths(filepath.Join(dir, "generated"), filepath.Join(dir, "blobs"))
	return &p
}

func TestScenePaths_AreSharded(t *testing.T) {
	p := newTestPaths(t)
	sp := p.Scene

	cases := []struct {
		name string
		got  string
		// want shard components, relative to the generated root's subdir
		wantDir string
	}{
		{"video preview", sp.GetVideoPreviewPath(md5Hash), filepath.Join("screenshots", "01", "23")},
		{"webp preview", sp.GetWebpPreviewPath(md5Hash), filepath.Join("screenshots", "01", "23")},
		{"transcode", sp.GetTranscodePath(md5Hash), filepath.Join("transcodes", "01", "23")},
		{"sprite image", sp.GetSpriteImageFilePath(md5Hash), filepath.Join("vtt", "01", "23")},
		{"sprite vtt", sp.GetSpriteVttFilePath(md5Hash), filepath.Join("vtt", "01", "23")},
		{"heatmap", sp.GetInteractiveHeatmapPath(md5Hash), filepath.Join("interactive_heatmaps", "01", "23")},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Contains(t, c.got, c.wantDir,
				"expected %s to be sharded into %s", c.got, c.wantDir)
		})
	}
}

func TestScenePaths_OshashIsAlsoSharded(t *testing.T) {
	// oshash is 16 characters, still longer than depth*length = 4.
	p := newTestPaths(t)
	got := p.Scene.GetTranscodePath(oshashStr)
	assert.Contains(t, got, filepath.Join("transcodes", "01", "23"))
}

func TestScenePaths_ShardingIsDeterministic(t *testing.T) {
	// The whole migration depends on the sharded path being computable without
	// touching the filesystem: a reader and a writer must agree.
	p := newTestPaths(t)
	for i := 0; i < 5; i++ {
		assert.Equal(t, p.Scene.GetTranscodePath(md5Hash), p.Scene.GetTranscodePath(md5Hash))
	}
}

func TestScenePaths_DifferentScenesLandInDifferentDirs(t *testing.T) {
	// 256 * 256 shards. If the shard were derived from something that collapses
	// -- say the file extension, or the last characters -- every file would
	// still share one directory and nothing would have been fixed.
	p := newTestPaths(t)
	seen := map[string]int{}
	for i := 0; i < 200; i++ {
		h := hashForIndex(i)
		dir := filepath.Dir(p.Scene.GetTranscodePath(h))
		seen[dir]++
	}
	assert.Greater(t, len(seen), 100, "200 scenes should spread over many directories, got %d", len(seen))
}

// hashForIndex makes a distinct 32-char hex string per index, with the LEADING
// characters varying. Sharding uses the first four, so a generator that only
// varied the tail would put every scene in one directory -- which is the bug
// this test exists to catch, so the generator has to be honest about it.
func hashForIndex(i int) string {
	const hex = "0123456789abcdef"
	b := make([]byte, 32)
	// Vary the first four characters with the index. 16^4 is far more than the
	// few hundred scenes any test needs.
	for j := 0; j < 4; j++ {
		b[j] = hex[(i>>(12-4*j))&0xf]
	}
	// Fill the rest so the strings are unique even for indices sharing a prefix.
	for j := 4; j < 32; j++ {
		b[j] = hex[(i*31+j*17)%16]
	}
	return string(b)
}

func TestScenePaths_ShortChecksumIsFlat(t *testing.T) {
	// A checksum shorter than depth*length cannot be sharded. It must land flat
	// rather than in a partial or escaping directory -- a flat path is always
	// correct, just slower.
	p := newTestPaths(t)
	short := "ab"
	got := p.Scene.GetTranscodePath(short)
	assert.Equal(t, filepath.Join(p.Scene.Transcodes, "ab.mp4"), got)
	assert.NotContains(t, got, "..")
}

// --- the legacy fallback, which is the part that must not be forgotten ---

func TestResolveGeneratedFile_PrefersSharded(t *testing.T) {
	dir := t.TempDir()
	sharded := filepath.Join(dir, "01", "23", "x.mp4")
	legacy := filepath.Join(dir, "x.mp4")

	require.NoError(t, os.MkdirAll(filepath.Dir(sharded), 0o755))
	require.NoError(t, os.WriteFile(sharded, []byte("new"), 0o644))
	require.NoError(t, os.WriteFile(legacy, []byte("old"), 0o644))

	// Both exist. The sharded one wins, because that is where new writes go and
	// it is the one that will be kept.
	assert.Equal(t, sharded, ResolveGeneratedFile(sharded, legacy))
}

func TestResolveGeneratedFile_FallsBackToLegacy(t *testing.T) {
	// The upgrade case: the file is only at the old flat path. Without this the
	// preview 404s and the sprite regenerates.
	dir := t.TempDir()
	sharded := filepath.Join(dir, "01", "23", "x.mp4")
	legacy := filepath.Join(dir, "x.mp4")
	require.NoError(t, os.WriteFile(legacy, []byte("old"), 0o644))

	assert.Equal(t, legacy, ResolveGeneratedFile(sharded, legacy))
}

func TestResolveGeneratedFile_ReturnsEmptyWhenNeitherExists(t *testing.T) {
	dir := t.TempDir()
	got := ResolveGeneratedFile(filepath.Join(dir, "01", "x"), filepath.Join(dir, "x"))
	assert.Equal(t, "", got, "a missing file must report as missing, not as a path")
}

func TestResolveGeneratedFile_EmptyLegacyDoesNotResolve(t *testing.T) {
	// A "" legacy path must not be stat'd and must not be returned.
	assert.Equal(t, "", ResolveGeneratedFile("", ""))
}

func TestResolveGeneratedFile_ShardedOnlyWhenLegacyIsEmpty(t *testing.T) {
	dir := t.TempDir()
	sharded := filepath.Join(dir, "01", "x")
	require.NoError(t, os.MkdirAll(filepath.Dir(sharded), 0o755))
	require.NoError(t, os.WriteFile(sharded, []byte("new"), 0o644))
	assert.Equal(t, sharded, ResolveGeneratedFile(sharded, ""))
}

func TestResolveForDelete_ReturnsBothWhenBothExist(t *testing.T) {
	// Deleting a scene must remove the flat copy too, or the leftover files are
	// exactly what keeps the directory large.
	dir := t.TempDir()
	sharded := filepath.Join(dir, "01", "x.mp4")
	legacy := filepath.Join(dir, "x.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(sharded), 0o755))
	require.NoError(t, os.WriteFile(sharded, []byte("n"), 0o644))
	require.NoError(t, os.WriteFile(legacy, []byte("o"), 0o644))

	got := ResolveForDelete(GeneratedFileToRemove{Sharded: sharded, Legacy: legacy})
	assert.ElementsMatch(t, []string{sharded, legacy}, got)
}

func TestResolveForDelete_LegacyOnly(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "x.mp4")
	require.NoError(t, os.WriteFile(legacy, []byte("o"), 0o644))

	got := ResolveForDelete(GeneratedFileToRemove{
		Sharded: filepath.Join(dir, "01", "x.mp4"),
		Legacy:  legacy,
	})
	assert.Equal(t, []string{legacy}, got)
}

func TestResolveForDelete_NeitherExists(t *testing.T) {
	dir := t.TempDir()
	got := ResolveForDelete(GeneratedFileToRemove{
		Sharded: filepath.Join(dir, "01", "x"),
		Legacy:  filepath.Join(dir, "x"),
	})
	assert.Empty(t, got)
}

func TestResolveForDelete_DoesNotDuplicate(t *testing.T) {
	// If a caller ever passes the same path twice, deleting it twice is at best
	// an error and at worst a crash in a loop.
	dir := t.TempDir()
	p := filepath.Join(dir, "x")
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	assert.Len(t, ResolveForDelete(GeneratedFileToRemove{Sharded: p, Legacy: p}), 1)
}

func TestSceneGeneratedFiles_CoversEveryFileType(t *testing.T) {
	// The point of the single entry point: a file type added to the scene
	// generated set cannot be forgotten here. Every one must have a legacy twin
	// too, or deleting a scene leaks the old flat file.
	p := newTestPaths(t)
	files := SceneGeneratedFiles(*p, md5Hash)

	require.NotEmpty(t, files)
	for _, f := range files {
		assert.NotEmpty(t, f.Sharded, "every entry needs a sharded path")
		assert.NotEmpty(t, f.Legacy, "every entry needs a legacy path, or deletes leak files")
		assert.NotEqual(t, f.Sharded, f.Legacy, "the two must differ, or the sharding did nothing")
	}
}

func TestSceneGeneratedFiles_LegacyPathsAreFlat(t *testing.T) {
	// The legacy paths must be the OLD layout exactly -- the file directly under
	// its directory root. A "legacy" path that is itself sharded would mean the
	// existing flat files are never found, which is the silent failure this
	// whole fallback exists to prevent.
	p := newTestPaths(t)

	for _, f := range SceneGeneratedFiles(*p, md5Hash) {
		legacyDir := filepath.Dir(f.Legacy)
		shardedDir := filepath.Dir(f.Sharded)
		assert.NotEqual(t, legacyDir, shardedDir,
			"the legacy path must differ from the sharded one: %s", f.Sharded)

		// The property that matters: the sharded directory is the legacy
		// directory plus exactly two shard components. So the legacy path's
		// directory is a strict prefix of the sharded one, and the file names
		// match -- same file, two locations.
		rel, err := filepath.Rel(legacyDir, shardedDir)
		require.NoError(t, err)
		assert.Equal(t, 2, len(strings.Split(rel, string(filepath.Separator))),
			"sharded dir %s should be legacy dir %s plus two components", shardedDir, legacyDir)
		assert.Equal(t, filepath.Base(f.Sharded), filepath.Base(f.Legacy),
			"the two paths name the same file")
		assert.False(t, strings.HasPrefix(rel, ".."),
			"the sharded dir must be under the legacy dir, not beside it")
	}

	// And concretely, for the transcode.
	assert.Equal(t,
		filepath.Join(p.Scene.Transcodes, md5Hash+".mp4"),
		p.Scene.GetLegacyTranscodePath(md5Hash))
	assert.Equal(t,
		filepath.Join(p.Scene.Screenshots, md5Hash+".mp4"),
		p.Scene.GetLegacyVideoPreviewPath(md5Hash))
	assert.Equal(t,
		filepath.Join(p.Scene.Vtt, md5Hash+"_thumbs.vtt"),
		p.Scene.GetLegacySpriteVttFilePath(md5Hash))
}

func TestLegacyGeneratedFilePath_RejectsBadChecksum(t *testing.T) {
	// A checksum that could never have been generated must not yield a legacy
	// path. This is also the guard that stops a caller-supplied string from
	// escaping the directory.
	dir := t.TempDir()
	for _, bad := range []string{
		"",
		"../../etc/passwd",
		"..",
		strings12(),
		"ZZZZ456789abcdef0123456789abcdef", // right length, not hex
		"short",
	} {
		assert.Equal(t, "", LegacyGeneratedFilePath(dir, bad, "x.mp4"),
			"checksum %q must not produce a legacy path", bad)
	}
}

func strings12() string { return "0123456789ab" } // 12 chars, too short to shard

func TestLegacyGeneratedFilePath_RejectsUnknownSuffix(t *testing.T) {
	dir := t.TempDir()
	assert.Equal(t, "", LegacyGeneratedFilePath(dir, md5Hash, "x.txt"))
	assert.Equal(t, "", LegacyGeneratedFilePath(dir, md5Hash, "noextension"))
}

func TestLegacyGeneratedFilePath_RejectsTraversalInFileName(t *testing.T) {
	dir := t.TempDir()
	// A name that ends in a known suffix but tries to climb out.
	got := LegacyGeneratedFilePath(dir, md5Hash, "../../../etc/passwd.mp4")
	assert.Equal(t, "", got, "a traversing name must be rejected even with a valid suffix")
}

func TestLegacyGeneratedFilePath_AcceptsRealNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		md5Hash + ".mp4",
		md5Hash + ".webp",
		md5Hash + "_sprite.jpg",
		md5Hash + "_thumbs.vtt",
		md5Hash + ".png",
		md5Hash + ".jpg",
	} {
		got := LegacyGeneratedFilePath(dir, md5Hash, name)
		assert.Equal(t, filepath.Join(dir, name), got, "name %q should be accepted", name)
	}
}

func TestIsValidGeneratedChecksum(t *testing.T) {
	assert.True(t, isValidGeneratedChecksum(md5Hash))
	assert.True(t, isValidGeneratedChecksum(oshashStr))
	assert.True(t, isValidGeneratedChecksum("0123456789ABCDEF0123456789abcdef"), "upper case hex is valid")
	assert.False(t, isValidGeneratedChecksum("0123456789abcde"))   // 15
	assert.False(t, isValidGeneratedChecksum("0123456789abcdef0")) // 17
	assert.False(t, isValidGeneratedChecksum("0123456789abcdeg0123456789abcdef"))
}

// --- pruning the shard directories ---

func TestPruneEmptyShardDirs_RemovesBothLevels(t *testing.T) {
	root := t.TempDir()
	sharded := shardedJoin(root, md5Hash, md5Hash+".mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(sharded), 0o755))
	require.NoError(t, os.WriteFile(sharded, []byte("x"), 0o644))
	require.NoError(t, os.Remove(sharded))

	PruneEmptyShardDirs(root, md5Hash)
	assert.NoDirExists(t, filepath.Join(root, "01"), "empty shard dirs should be reclaimed")
	assert.NoDirExists(t, filepath.Join(root, "01", "23"))
	assert.DirExists(t, root, "the generated root itself must never be removed")
}

func TestPruneEmptyShardDirs_KeepsNonEmpty(t *testing.T) {
	root := t.TempDir()
	// Two scenes sharing the "01" top-level shard, in DIFFERENT leaf shards, so
	// the leaf for `a` is genuinely empty and the parent is genuinely not.
	// This is the case that matters: a prune that ignored the emptiness check
	// would remove 01/23 here and take b with it.
	a := shardedJoin(root, md5Hash, md5Hash+".mp4")
	b := shardedJoin(root, "01ffffffffffffffffffffffffffffff", "b.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(a), 0o755))
	require.NoError(t, os.WriteFile(a, []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Dir(b), 0o755))
	require.NoError(t, os.WriteFile(b, []byte("x"), 0o644))
	// Delete only a's file; the directories are now non-empty at the leaf for b.
	require.NoError(t, os.Remove(a))

	PruneEmptyShardDirs(root, md5Hash)
	assert.DirExists(t, filepath.Join(root, "01"), "a non-empty parent must be kept")
	assert.FileExists(t, b, "the other scene's file must survive")
}

func TestPruneEmptyShardDirs_NeverRemovesANonEmptyLeaf(t *testing.T) {
	// The emptiness check itself, isolated. A prune that skipped it would delete
	// a directory that still holds a file, and the file with it -- so this writes
	// a file into the leaf and prunes without deleting that file first, which is
	// the only way the leaf is non-empty when PruneEmptyShardDirs runs.
	root := t.TempDir()
	keep := shardedJoin(root, md5Hash, md5Hash+".mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(keep), 0o755))
	require.NoError(t, os.WriteFile(keep, []byte("x"), 0o644))

	// Prune for a DIFFERENT checksum that happens to share the same leaf would be
	// a different test; here the point is that pruning the very shard holding the
	// file leaves both the leaf and the file in place.
	PruneEmptyShardDirs(root, md5Hash)

	assert.DirExists(t, filepath.Dir(keep), "a non-empty leaf must survive")
	assert.FileExists(t, keep, "a file in a non-empty leaf must survive")
}

func TestPruneEmptyShardDirs_ShortChecksumIsNoOp(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(root, 0o755))
	PruneEmptyShardDirs(root, "ab") // cannot shard
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Len(t, entries, 0, "nothing should be created or removed")
}

// --- the end-to-end shape, on a real filesystem ---

func TestGeneratedDirs_StaySmallAsScenesAccumulate(t *testing.T) {
	// The bug in one assertion: with 500 scenes in a flat directory, the
	// directory has 500 entries. Sharded, no directory has more than a handful.
	p := newTestPaths(t)
	const scenes = 500

	for i := 0; i < scenes; i++ {
		h := hashForIndex(i)
		path := p.Scene.GetTranscodePath(h)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	}

	// Every top-level entry under transcodes is a shard directory.
	entries, err := os.ReadDir(p.Scene.Transcodes)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(entries), 256,
		"a sharded directory can have at most 256 top-level entries")

	// And no single shard holds more than a small number of these scenes.
	// 500 scenes over 256*256 shards means most shards are empty or tiny.
	worst := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub, err := os.ReadDir(filepath.Join(p.Scene.Transcodes, e.Name()))
		require.NoError(t, err)
		for _, s := range sub {
			inner, err := os.ReadDir(filepath.Join(p.Scene.Transcodes, e.Name(), s.Name()))
			require.NoError(t, err)
			if len(inner) > worst {
				worst = len(inner)
			}
		}
	}
	assert.Less(t, worst, 20, "no single leaf directory should hold a meaningful share of the library")
}

func TestLegacyFileIsFoundAfterUpgrade(t *testing.T) {
	// The scenario end to end: an existing library with flat files only.
	p := newTestPaths(t)
	sp := p.Scene

	// What the pre-sharding code wrote.
	for _, d := range []string{sp.Transcodes, sp.Screenshots, sp.Vtt, sp.InteractiveHeatmap} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	require.NoError(t, os.WriteFile(sp.GetLegacyTranscodePath(md5Hash), []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(sp.GetLegacyVideoPreviewPath(md5Hash), []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(sp.GetLegacySpriteImageFilePath(md5Hash), []byte("old"), 0o644))

	// Everything resolves to the existing file.
	assert.Equal(t, sp.GetLegacyTranscodePath(md5Hash),
		ResolveGeneratedFile(sp.GetTranscodePath(md5Hash), sp.GetLegacyTranscodePath(md5Hash)))
	assert.Equal(t, sp.GetLegacyVideoPreviewPath(md5Hash),
		ResolveGeneratedFile(sp.GetVideoPreviewPath(md5Hash), sp.GetLegacyVideoPreviewPath(md5Hash)))
	assert.Equal(t, sp.GetLegacySpriteImageFilePath(md5Hash),
		ResolveGeneratedFile(sp.GetSpriteImageFilePath(md5Hash), sp.GetLegacySpriteImageFilePath(md5Hash)))

	// And deleting the scene removes them, so the flat directory drains.
	for _, f := range SceneGeneratedFiles(*p, md5Hash) {
		for _, existing := range ResolveForDelete(f) {
			require.NoError(t, os.Remove(existing))
		}
	}
	entries, err := os.ReadDir(sp.Transcodes)
	require.NoError(t, err)
	assert.Empty(t, entries, "the flat directory should be empty after the delete")
}

// --- tests added because the mutation harness found the suite blind ---
//
// Both of these were blind spots the harness exposed, and neither is visible by
// reading the other tests: they assert the file LIST and the file SET, and every
// test that existed checked the path shape of entries that were present.

func TestLegacyGeneratedFilePath_ValidChecksumIsRequired(t *testing.T) {
	// The harness removed the isValidGeneratedChecksum guard and this test still
	// passed, because every case below ALSO has a file name that is not
	// "<checksum><suffix>", so the second guard rejected it and the test proved
	// nothing about the first. Two guards, one assertion.
	//
	// The fix: make the name well-formed for every case, so the checksum guard
	// is the only thing that can reject. If both guards are removed, this fails.
	dir := t.TempDir()
	for _, bad := range []string{
		"not-a-checksum",
		"../../etc",
		strings.Repeat("a", 31), // one short of md5
		strings.Repeat("a", 33),
		strings.Repeat("z", 32), // right length, not hex
		"",
	} {
		// The name matches the checksum, so isLegacyGeneratedFileName accepts it.
		// Only the checksum validity check can turn this into "".
		got := LegacyGeneratedFilePath(dir, bad, bad+".mp4")
		assert.Equal(t, "", got,
			"checksum %q is not a real scene hash and must not yield a path", bad)
	}
}

func TestLegacyGeneratedFilePath_EachGuardIsLoadBearing(t *testing.T) {
	// The two guards catch different things, and neither subsumes the other:
	//
	//   checksum guard  -- a value that could never be a scene hash
	//   name guard      -- a name that is not <checksum><suffix>, which is also
	//                     what stops "../../../etc/passwd.mp4"
	//
	// A test that only ever supplies a bad checksum AND a bad name is satisfied
	// by either one alone, which is exactly how the checksum guard survived a
	// mutation.
	dir := t.TempDir()

	// Bad checksum, good name: only the checksum guard can catch this.
	badSum := strings.Repeat("a", 31)
	assert.Equal(t, "", LegacyGeneratedFilePath(dir, badSum, badSum+".mp4"),
		"the checksum guard must reject a non-hash")

	// Good checksum, bad name: only the name guard can catch this.
	assert.Equal(t, "", LegacyGeneratedFilePath(dir, md5Hash, "../../../etc/passwd.mp4"),
		"the name guard must reject a traversal")
	assert.Equal(t, "", LegacyGeneratedFilePath(dir, md5Hash, "unrelated.txt"),
		"the name guard must reject an unknown suffix")

	// Both good: the path is produced.
	assert.NotEqual(t, "", LegacyGeneratedFilePath(dir, md5Hash, md5Hash+".mp4"),
		"a real hash and a real name must produce a path")
}

func TestSceneGeneratedFiles_ContainsEveryExpectedFile(t *testing.T) {
	// The harness removed the sprite entries and nothing failed: the other
	// tests iterate whatever SceneGeneratedFiles returns, so dropping entries
	// from it is invisible to them by construction. This test names the set.
	p := newTestPaths(t)
	files := SceneGeneratedFiles(*p, md5Hash)

	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, filepath.Base(f.Sharded))
	}

	want := []string{
		md5Hash + ".mp4", // video preview
		md5Hash + ".webp",
		md5Hash + ".mp4", // transcode -- same name, different root
		md5Hash + "_sprite.jpg",
		md5Hash + "_thumbs.vtt",
		md5Hash + ".png",
	}

	// The two .mp4 entries are the video preview and the transcode, in the
	// order SceneGeneratedFiles lists them, so compare as a multiset of
	// root+name rather than a set of names.
	keys := make([]string, 0, len(files))
	for _, f := range files {
		keys = append(keys, filepath.Dir(f.Sharded)+"|"+filepath.Base(f.Sharded))
	}
	for _, w := range want {
		found := false
		for _, k := range keys {
			if strings.HasSuffix(k, "|"+w) {
				found = true
				break
			}
		}
		assert.True(t, found, "SceneGeneratedFiles is missing %s; got %v", w, names)
	}
	assert.Len(t, files, len(want), "the generated file set changed: %v", names)
}

func TestSceneGeneratedFiles_SpritesAreActuallyRemoved(t *testing.T) {
	// The concrete consequence of the sprite entries going missing: a deleted
	// scene leaves its sprite behind forever, and the vtt directory never
	// shrinks -- which is the original complaint.
	p := newTestPaths(t)
	for _, d := range []string{p.Scene.Vtt, p.Scene.Screenshots, p.Scene.Transcodes, p.Scene.InteractiveHeatmap} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}

	// Write the LEGACY sprite files, as an existing installation would have.
	sprite := p.Scene.GetLegacySpriteImageFilePath(md5Hash)
	vtt := p.Scene.GetLegacySpriteVttFilePath(md5Hash)
	require.NoError(t, os.WriteFile(sprite, []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(vtt, []byte("x"), 0o644))

	for _, f := range SceneGeneratedFiles(*p, md5Hash) {
		for _, existing := range ResolveForDelete(f) {
			require.NoError(t, os.Remove(existing))
		}
	}

	assert.NoFileExists(t, sprite, "the sprite must be deleted with the scene")
	assert.NoFileExists(t, vtt, "the vtt must be deleted with the scene")
	entries, err := os.ReadDir(p.Scene.Vtt)
	require.NoError(t, err)
	assert.Empty(t, entries, "the flat vtt directory must drain")
}
