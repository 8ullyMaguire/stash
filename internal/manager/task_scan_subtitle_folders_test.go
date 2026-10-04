package manager

import (
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/models"
)

// stash#6744 -- resolving the configured subtitle folders for a video.
//
// The folder list is per-stash, and the useful form of an entry is RELATIVE ("subs"), because the common
// layout is a subtitle folder beside the library content and an absolute path written on one machine
// would be wrong on the next. Absolute entries are passed through untouched, which is what makes a
// separate mount or SMB share work -- the case the issue is actually about.
//
// Relative-to-the-stash also means the resolution has to pick the right stash first: with nested library
// paths configured, a video deep in the tree belongs to the most specific stash, and that stash's root is
// the anchor. GetStashFromDirPath already implements longest-prefix-wins, so this test pins the behaviour
// through that rather than reimplementing it.

func newConfigWithStashes(stashes ...*config.StashConfig) *config.Config {
	cfg := config.InitializeEmpty()
	cfg.SetInterface(config.Stash, stashes)
	cfg.SetInterface(config.Exclude, []string{})
	return cfg
}

func TestSubtitleFoldersFor(t *testing.T) {
	const root = "/library"

	tests := []struct {
		name      string
		stashes   []*config.StashConfig
		videoPath string
		want      []string
	}{
		{
			name:      "no stashes configured",
			stashes:   nil,
			videoPath: root + "/vids/scene.mp4",
			want:      nil,
		},
		{
			name:      "a stash with no subtitle folders",
			stashes:   []*config.StashConfig{{Path: root}},
			videoPath: root + "/vids/scene.mp4",
			want:      nil,
		},
		{
			name:      "a relative folder resolves against the stash root",
			stashes:   []*config.StashConfig{{Path: root, SubtitleFolders: []string{"subs"}}},
			videoPath: root + "/vids/scene.mp4",
			want:      []string{"/library/subs"},
		},
		{
			name:      "several relative folders",
			stashes:   []*config.StashConfig{{Path: root, SubtitleFolders: []string{"subs", "captions/srt"}}},
			videoPath: root + "/vids/scene.mp4",
			want:      []string{"/library/subs", "/library/captions/srt"},
		},
		{
			name:      "an absolute folder is passed through unchanged",
			stashes:   []*config.StashConfig{{Path: root, SubtitleFolders: []string{"/mnt/subtitles"}}},
			videoPath: root + "/vids/scene.mp4",
			// This is the motivating case: a subtitle share mounted elsewhere.
			want: []string{"/mnt/subtitles"},
		},
		{
			name:      "absolute and relative entries mix",
			stashes:   []*config.StashConfig{{Path: root, SubtitleFolders: []string{"subs", "/mnt/subtitles"}}},
			videoPath: root + "/vids/scene.mp4",
			want:      []string{"/library/subs", "/mnt/subtitles"},
		},
		{
			name:      "blank and whitespace-only entries are dropped",
			stashes:   []*config.StashConfig{{Path: root, SubtitleFolders: []string{"", "   ", "subs"}}},
			videoPath: root + "/vids/scene.mp4",
			want:      []string{"/library/subs"},
		},
		{
			name:      "entries are trimmed",
			stashes:   []*config.StashConfig{{Path: root, SubtitleFolders: []string{"  subs  "}}},
			videoPath: root + "/vids/scene.mp4",
			want:      []string{"/library/subs"},
		},
		{
			name:      "a folder list of only blanks resolves to nil",
			stashes:   []*config.StashConfig{{Path: root, SubtitleFolders: []string{"", "  "}}},
			videoPath: root + "/vids/scene.mp4",
			// nil rather than an empty slice, so the caller short-circuits and does no work at all.
			want: nil,
		},
		{
			name: "the most specific stash wins for a nested library",
			stashes: []*config.StashConfig{
				{Path: root, SubtitleFolders: []string{"root-subs"}},
				{Path: root + "/movies", SubtitleFolders: []string{"movie-subs"}},
			},
			videoPath: root + "/movies/vids/scene.mp4",
			// Longest-prefix-wins: the movies stash owns this video, so its folders and its root apply.
			want: []string{"/library/movies/movie-subs"},
		},
		{
			name: "a nested stash with no folders falls back to no folders, not the parent's",
			stashes: []*config.StashConfig{
				{Path: root, SubtitleFolders: []string{"root-subs"}},
				{Path: root + "/movies"},
			},
			videoPath: root + "/movies/vids/scene.mp4",
			// Deliberate: inheriting the parent's folder would resolve against the wrong root and quietly
			// point at a path the user never configured for this library.
			want: nil,
		},
		{
			name:      "a video outside every stash resolves to nil",
			stashes:   []*config.StashConfig{{Path: root, SubtitleFolders: []string{"subs"}}},
			videoPath: "/somewhere/else/scene.mp4",
			want:      nil,
		},
		{
			name:      "a trailing slash on the stash root does not change the resolution",
			stashes:   []*config.StashConfig{{Path: root + "/", SubtitleFolders: []string{"subs"}}},
			videoPath: root + "/vids/scene.mp4",
			want:      []string{"/library/subs"},
		},
		{
			name:      "a relative folder with .. is cleaned",
			stashes:   []*config.StashConfig{{Path: root + "/movies", SubtitleFolders: []string{"../subs"}}},
			videoPath: root + "/movies/vids/scene.mp4",
			want:      []string{"/library/subs"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := &ScanJob{config: newConfigWithStashes(tt.stashes...)}

			got := j.subtitleFoldersFor(tt.videoPath)

			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				// filepath.Clean normalises separators, so compare cleaned forms on both sides.
				if filepath.Clean(got[i]) != filepath.Clean(tt.want[i]) {
					t.Errorf("folder %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSubtitleFoldersForHandlesAMissingConfig(t *testing.T) {
	// The job is constructed in one place and this is read in a scanner goroutine, so a nil config must
	// not panic -- it must simply find no folders.
	var nilConfig *ScanJob
	if got := nilConfig.subtitleFoldersFor("/library/vids/scene.mp4"); got != nil {
		t.Errorf("a nil ScanJob returned %v, want nil", got)
	}

	nilCfgJob := &ScanJob{}
	if got := nilCfgJob.subtitleFoldersFor("/library/vids/scene.mp4"); got != nil {
		t.Errorf("a job with no config returned %v, want nil", got)
	}
}

// TestCaptionMatchesVideoIsTheWiring is the load-bearing test for #6744.
//
// Both mutations survived the first version of this file: reverting the scan loop to the directory-blind
// MatchesCaption, and resolving the folders to nil, both left the suite green. Every test here exercised
// subtitleFoldersFor and the matcher in isolation and nothing exercised the line that joins them.
//
// So this goes through ScanJob.captionMatchesVideo -- the function the scan loop actually calls.
func TestCaptionMatchesVideoIsTheWiring(t *testing.T) {
	j := &ScanJob{config: newConfigWithStashes(&config.StashConfig{
		Path:            "/library",
		SubtitleFolders: []string{"subs"},
	})}

	const (
		videoPath   = "/library/vids/scene.mp4"
		folderCap   = "/library/subs/scene.en.srt"
		sidecarCap  = "/library/vids/scene.en.srt"
		unrelatedCa = "/library/other/scene.en.srt"
	)

	v := &models.VideoFile{BaseFile: &models.BaseFile{Path: videoPath}}

	if !j.captionMatchesVideo(v, folderCap) {
		t.Error("a caption in the configured folder must match once the config is resolved through the job")
	}
	if !j.captionMatchesVideo(v, sidecarCap) {
		t.Error("a sidecar must still match")
	}
	if j.captionMatchesVideo(v, unrelatedCa) {
		t.Error("a caption in an unconfigured folder must not match")
	}
}

// TestCaptionMatchesVideoWithNoConfigIsTheOldBehaviour is the compatibility guard: a job with nothing
// configured must behave exactly as it did before #6744 existed, so no existing library changes meaning.
func TestCaptionMatchesVideoWithNoConfigIsTheOldBehaviour(t *testing.T) {
	j := &ScanJob{config: newConfigWithStashes(&config.StashConfig{Path: "/library"})}

	const (
		videoPath  = "/library/vids/scene.mp4"
		sidecarCap = "/library/vids/scene.en.srt"
		folderCap  = "/library/subs/scene.en.srt"
	)

	v := &models.VideoFile{BaseFile: &models.BaseFile{Path: videoPath}}

	if !j.captionMatchesVideo(v, sidecarCap) {
		t.Error("a sidecar must match with nothing configured")
	}
	if j.captionMatchesVideo(v, folderCap) {
		t.Error("with no subtitle folders configured, a caption elsewhere must NOT match")
	}
}
