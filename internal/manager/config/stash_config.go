package config

import (
	"path/filepath"

	"github.com/stashapp/stash/pkg/fsutil"
)

// Stash configuration details
type StashConfigInput struct {
	Path         string `json:"path"`
	ExcludeVideo bool   `json:"excludeVideo"`
	ExcludeImage bool   `json:"excludeImage"`

	// SubtitleFolders are directory names, relative to this stash's Path, in which to look for
	// subtitle files for videos stored elsewhere. See StashConfig.SubtitleFolders.
	SubtitleFolders []string `json:"subtitleFolders"`
}

type StashConfig struct {
	Path         string `json:"path"`
	ExcludeVideo bool   `json:"excludeVideo"`
	ExcludeImage bool   `json:"excludeImage"`

	// SubtitleFolders lists directories to search for subtitle files, in addition to the video's own
	// directory. Entries are absolute paths, or paths relative to Path; the most recently written value
	// wins.
	//
	// stash#6744. Caption matching was previously directory-blind: video.MatchesCaption compared only
	// basename prefixes, so `/library/vids/scene.mp4` and `/library/subs/scene.en.srt` never matched and
	// the subtitle was silently discarded. That is fine for the sidecar layout the scanner assumed, and
	// wrong for the two layouts people actually use: subtitles on a separate share (a SMB mount of a
	// subtitle directory, which is how large subtitle collections are usually distributed), and a
	// library where each show has a `subs/` folder next to the video.
	//
	// Kept per-stash rather than global because the folder only means something relative to a particular
	// library root -- an absolute path in a global setting would be wrong as soon as a second library is
	// configured, and a relative one has no anchor.
	SubtitleFolders []string `json:"subtitleFolders"`
}

type StashConfigs []*StashConfig

// GetStashFromPath returns the most specific stash configuration containing path.
func (s StashConfigs) GetStashFromPath(path string) *StashConfig {
	return s.GetStashFromDirPath(filepath.Dir(path))
}

// GetStashFromDirPath returns the most specific stash configuration containing dirPath.
func (s StashConfigs) GetStashFromDirPath(dirPath string) *StashConfig {
	var ret *StashConfig
	longestPath := -1

	for _, f := range s {
		if f == nil {
			continue
		}

		path := filepath.Clean(f.Path)
		if fsutil.IsPathInDir(path, dirPath) && len(path) > longestPath {
			ret = f
			longestPath = len(path)
		}
	}

	return ret
}

// GetStashRootFromDirPath returns the topmost configured stash path containing dirPath.
func (s StashConfigs) GetStashRootFromDirPath(dirPath string) string {
	var ret string
	shortestPath := -1

	for _, f := range s {
		if f == nil {
			continue
		}

		path := filepath.Clean(f.Path)
		if fsutil.IsPathInDir(path, dirPath) && (shortestPath == -1 || len(path) < shortestPath) {
			ret = path
			shortestPath = len(path)
		}
	}

	return ret
}

func (s StashConfigs) Paths() []string {
	paths := make([]string, len(s))
	for i, c := range s {
		// #6618 - clean the path to ensure comparison works correctly
		paths[i] = filepath.Clean(c.Path)
	}
	return paths
}
