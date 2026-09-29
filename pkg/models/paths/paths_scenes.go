package paths

import (
	"path/filepath"

	"github.com/stashapp/stash/pkg/fsutil"
)

type scenePaths struct {
	generatedPaths
}

func newScenePaths(p Paths) *scenePaths {
	sp := scenePaths{
		generatedPaths: *p.Generated,
	}
	return &sp
}

// Sharding for the per-scene generated files. (stash#2824)
//
// The generated directories were flat: one file per scene, all in one
// directory, named by checksum. The report measured the cost at ~50k files in
// generated/screenshots and generated/vtt, where "listing such folders for
// whatever reason gets very slow" and every disk operation on the share got
// slow too. Directory entries do not shard; every lookup becomes a scan of a
// huge B-tree, and the cost is paid by every reader, not just the writer.
//
// Thumbnails already solved this -- GetThumbnailPath has sharded on the leading
// characters of the checksum for some time -- and blobs do the same. The scene
// directories were simply never converted, so the fix is to use the mechanism
// that is already there rather than invent one.
//
// Depth 2, length 2 matches the thumbnail and blob layout deliberately, so all
// four generated trees have the same shape. 256 top-level entries and 256 per
// level is enough that no single directory is ever large, and 4 characters of a
// 32-character md5 leaves plenty of room.
//
// Every function here that changes a path also has a Legacy* twin returning the
// old flat path. See LegacyGeneratedFilePath for why both are needed.
const generatedDirDepth int = 2
const generatedDirLength int = 2

// shardedJoin builds a path under a generated directory, sharded by the leading
// characters of checksum.
//
// A checksum shorter than depth*length is NOT sharded and lands flat. That
// matters because scene checksums are either 16 (oshash) or 32 (md5) characters,
// both comfortably longer than 4, but oshash-derived and hand-imported values
// are not guaranteed to be, and a flat path is always correct -- just slower. A
// malformed checksum must not send a file somewhere no reader will look.
func shardedJoin(dir, checksum, fileName string) string {
	intra := fsutil.GetIntraDir(checksum, generatedDirDepth, generatedDirLength)
	if intra == "" {
		return filepath.Join(dir, fileName)
	}
	return filepath.Join(dir, intra, fileName)
}

// LegacyGeneratedFilePath returns the pre-sharding flat path for a generated
// file, or "" when there is none.
//
// Existing installations have tens of thousands of files at the flat paths and
// nothing has moved them. The path functions above are the only thing that
// knows where a generated file lives, and every reader and deleter goes through
// them, so if they return only the sharded path then every already-generated
// file becomes invisible: previews 404, transcodes are not found so scenes
// re-transcode, sprites regenerate, and deleting a scene leaves its generated
// files on disk forever.
//
// Rather than migrate the files -- which for 50k of them over SMB is the exact
// operation the report says is too slow, and which would need to be interruptible
// and resumable to be safe -- each call site checks the sharded path first and
// falls back to the legacy one. The legacy file is then removed the first time it
// is superseded, so the flat directory drains as files are regenerated rather
// than all at once.
//
// A legacy path is only returned for file names Stash itself produced, so a
// caller cannot be tricked into constructing a path outside the directory: the
// name is checked against the suffixes these functions have always used.
func LegacyGeneratedFilePath(dir, checksum, fileName string) string {
	// Checksum first: it is the part that decides whether the name is one Stash
	// could have written, and validating it before using it keeps the two checks
	// independent.
	if !isValidGeneratedChecksum(checksum) {
		return ""
	}
	if !isLegacyGeneratedFileName(checksum, fileName) {
		return ""
	}
	return filepath.Join(dir, fileName)
}

// isLegacyGeneratedFileName reports whether fileName is one of the flat names
// the pre-sharding code created. Anything else returns "" from
// LegacyGeneratedFilePath, so a caller passing user input cannot escape dir.
//
// The name must also be exactly "<checksum><suffix>" with nothing else. Checking
// only the suffix is not enough: "../../../etc/passwd.mp4" ends in ".mp4" and
// would otherwise produce a path outside dir, turning a helper meant to be
// defensive into the traversal. The length check plus the checksum equality
// means a name can be a real generated name or nothing at all.
func isLegacyGeneratedFileName(checksum, fileName string) bool {
	for _, suffix := range legacyGeneratedSuffixes {
		if fileName == checksum+suffix {
			return true
		}
	}
	return false
}

// legacyGeneratedSuffixes are the flat file name suffixes the pre-sharding code
// wrote into the scene generated directories.
var legacyGeneratedSuffixes = []string{
	".jpg",  // GetLegacyScreenshotPath
	".mp4",  // GetVideoPreviewPath, GetTranscodePath
	".webp", // GetWebpPreviewPath
	"_sprite.jpg",
	"_thumbs.vtt",
	".png", // GetInteractiveHeatmapPath
}

// isValidGeneratedChecksum matches the checksums Stash actually uses. Restricting
// the legacy path to these keeps a caller-supplied string from escaping the
// directory, and avoids offering a legacy path for a value that could never have
// been generated in the first place.
func isValidGeneratedChecksum(checksum string) bool {
	if len(checksum) != 16 && len(checksum) != 32 {
		return false
	}
	for i := 0; i < len(checksum); i++ {
		c := checksum[i]
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

func (sp *scenePaths) GetLegacyScreenshotPath(checksum string) string {
	return filepath.Join(sp.Screenshots, checksum+".jpg")
}

func (sp *scenePaths) GetTranscodePath(checksum string) string {
	return shardedJoin(sp.Transcodes, checksum, checksum+".mp4")
}

func (sp *scenePaths) GetStreamPath(scenePath string, checksum string) string {
	transcodePath := sp.GetTranscodePath(checksum)
	transcodeExists, _ := fsutil.FileExists(transcodePath)
	if transcodeExists {
		return transcodePath
	}
	return scenePath
}

func (sp *scenePaths) GetVideoPreviewPath(checksum string) string {
	return shardedJoin(sp.Screenshots, checksum, checksum+".mp4")
}

func (sp *scenePaths) GetWebpPreviewPath(checksum string) string {
	return shardedJoin(sp.Screenshots, checksum, checksum+".webp")
}

func (sp *scenePaths) GetSpriteImageFilePath(checksum string) string {
	return shardedJoin(sp.Vtt, checksum, checksum+"_sprite.jpg")
}

func (sp *scenePaths) GetSpriteVttFilePath(checksum string) string {
	return shardedJoin(sp.Vtt, checksum, checksum+"_thumbs.vtt")
}

func (sp *scenePaths) GetInteractiveHeatmapPath(checksum string) string {
	return shardedJoin(sp.InteractiveHeatmap, checksum, checksum+".png")
}

// Legacy scene generated files, at their pre-sharding flat paths. Empty string
// when the checksum or file name could not have been produced by the old code.
func (sp *scenePaths) GetLegacyVideoPreviewPath(checksum string) string {
	return LegacyGeneratedFilePath(sp.Screenshots, checksum, checksum+".mp4")
}

func (sp *scenePaths) GetLegacyWebpPreviewPath(checksum string) string {
	return LegacyGeneratedFilePath(sp.Screenshots, checksum, checksum+".webp")
}

func (sp *scenePaths) GetLegacySpriteImageFilePath(checksum string) string {
	return LegacyGeneratedFilePath(sp.Vtt, checksum, checksum+"_sprite.jpg")
}

func (sp *scenePaths) GetLegacySpriteVttFilePath(checksum string) string {
	return LegacyGeneratedFilePath(sp.Vtt, checksum, checksum+"_thumbs.vtt")
}

func (sp *scenePaths) GetLegacyTranscodePath(checksum string) string {
	return LegacyGeneratedFilePath(sp.Transcodes, checksum, checksum+".mp4")
}

func (sp *scenePaths) GetLegacyInteractiveHeatmapPath(checksum string) string {
	return LegacyGeneratedFilePath(sp.InteractiveHeatmap, checksum, checksum+".png")
}
