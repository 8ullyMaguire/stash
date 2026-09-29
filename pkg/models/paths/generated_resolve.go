package paths

import (
	"os"
	"path/filepath"
)

// ResolveGeneratedFile returns the path of a generated file that actually
// exists: the sharded location first, then the pre-sharding flat location.
//
// This exists so the legacy fallback cannot be forgotten at a call site. Every
// reader and deleter of a generated scene file goes through it, and the failure
// of not calling it is silent -- a preview 404s, a scene re-transcodes, a
// deleted scene leaves files behind -- so the correctness of the migration
// depends on the number of call sites, which is exactly the kind of thing that
// rots.
//
// Returns "" when neither exists.
func ResolveGeneratedFile(sharded, legacy string) string {
	if sharded == "" {
		return legacyOrEmpty(legacy)
	}
	if fileExists(sharded) {
		return sharded
	}
	return legacyOrEmpty(legacy)
}

// ResolveGeneratedFileForRead is ResolveGeneratedFile for a caller that is about
// to open the file. A missing file is reported as a missing file by the caller's
// own existence check, so this does not need to distinguish them.
//
// Prefer ResolveGeneratedFile unless the difference matters; the two exist so
// the intent at the call site is readable.
func ResolveGeneratedFileForRead(sharded, legacy string) string {
	return ResolveGeneratedFile(sharded, legacy)
}

func legacyOrEmpty(legacy string) string {
	if legacy == "" {
		return ""
	}
	if fileExists(legacy) {
		return legacy
	}
	return ""
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// GeneratedFileToRemove pairs a sharded path with its legacy twin for deletion,
// so a file is removed wherever it happens to live.
type GeneratedFileToRemove struct {
	Sharded string
	Legacy  string
}

// ResolveForDelete returns every existing path for a generated file: the
// sharded one, the legacy one, or both if a previous version wrote the file
// twice.
//
// Deletion has to consider both. Returning only the sharded path would leave the
// flat copy behind for every scene deleted between the release that sharded
// the paths and the one that regenerated the file -- which for a large library
// is most of them, and the leftover files are what keep the directory large.
func ResolveForDelete(f GeneratedFileToRemove) []string {
	var out []string
	if f.Sharded != "" && fileExists(f.Sharded) {
		out = append(out, f.Sharded)
	}
	if f.Legacy != "" && f.Legacy != f.Sharded && fileExists(f.Legacy) {
		out = append(out, f.Legacy)
	}
	return out
}

// SceneGeneratedFiles enumerates every generated file belonging to a scene, with
// both the sharded and legacy locations, so a deleter can remove all of them
// without knowing the layout.
//
// A single entry point rather than six call sites each assembling their own
// list: a file type added to this list and forgotten in pkg/scene/delete.go is
// silently never deleted, and nothing reports it.
func SceneGeneratedFiles(p Paths, checksum string) []GeneratedFileToRemove {
	sp := p.Scene
	return []GeneratedFileToRemove{
		{Sharded: sp.GetVideoPreviewPath(checksum), Legacy: sp.GetLegacyVideoPreviewPath(checksum)},
		{Sharded: sp.GetWebpPreviewPath(checksum), Legacy: sp.GetLegacyWebpPreviewPath(checksum)},
		{Sharded: sp.GetTranscodePath(checksum), Legacy: sp.GetLegacyTranscodePath(checksum)},
		{Sharded: sp.GetSpriteImageFilePath(checksum), Legacy: sp.GetLegacySpriteImageFilePath(checksum)},
		{Sharded: sp.GetSpriteVttFilePath(checksum), Legacy: sp.GetLegacySpriteVttFilePath(checksum)},
		{Sharded: sp.GetInteractiveHeatmapPath(checksum), Legacy: sp.GetLegacyInteractiveHeatmapPath(checksum)},
	}
}

// PruneEmptyShardDirs removes the a/b directory pair a file used to live in, once
// it is empty.
//
// Without this, a scene deleted from a small library leaves an empty a/b behind.
// Over time that recreates the many-small-directories problem the sharding was
// meant to solve, just one level down -- 256 directories per generated tree
// instead of one, which is harmless, but the directories accumulate per checksum
// prefix and are never reclaimed.
//
// Only removes a directory that is genuinely empty, and never the generated root
// itself, so it cannot delete anything a caller did not put there.
func PruneEmptyShardDirs(generatedRoot string, checksum string) {
	intra := shardDirFor(generatedRoot, checksum)
	if intra == "" {
		return
	}
	// Deepest first: the leaf, then its parent. Both must be empty.
	for dir := intra; dir != generatedRoot && len(dir) > len(generatedRoot); {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// shardDirFor returns the a/b directory a checksum's files live in, or "" when
// the checksum is too short to shard.
func shardDirFor(generatedRoot, checksum string) string {
	return shardedJoin(generatedRoot, checksum, "")
}
