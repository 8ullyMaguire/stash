package video

import "testing"

// stash#6744 -- option to check a specific folder for subtitles.
//
// THE GAP, MEASURED BEFORE FIXING IT
//
// The reported behaviour is that a subtitle in a dedicated folder is never attached. Probed directly:
//
//	/library/scene.mp4   vs  /library/scene.en.srt         true
//	/library/scene.mp4   vs  /library/subs/scene.en.srt    false
//	/library/vids/scene.mp4 vs /library/subs/scene.en.srt  false
//
// MatchesCaption compared BASENAME prefixes only, never the directory. So the sidecar layout the scanner
// assumed was the only layout that worked, and a subtitle on a separate share -- how large subtitle
// collections are usually distributed -- was silently discarded with no warning anywhere.

func TestMatchesCaptionInFolders(t *testing.T) {
	const (
		videoPath  = "/library/vids/scene.mp4"
		sidecarCap = "/library/vids/scene.en.srt"
		folderCap  = "/library/subs/scene.en.srt"
		otherCap   = "/library/elsewhere/scene.en.srt"
	)

	folders := []string{"/library/subs"}

	tests := []struct {
		name        string
		video       string
		caption     string
		folders     []string
		want        bool
		wantComment string
	}{
		{
			name:    "a sidecar still matches with folders configured",
			video:   videoPath,
			caption: sidecarCap,
			folders: folders,
			want:    true,
			// The regression guard: configuring subtitle folders must not change any existing behaviour.
		},
		{
			name:    "a caption in a configured folder now matches",
			video:   videoPath,
			caption: folderCap,
			folders: folders,
			want:    true,
		},
		{
			name:    "a caption in an UNconfigured folder still does not match",
			video:   videoPath,
			caption: otherCap,
			folders: folders,
			want:    false,
			// Widening must not become "any caption anywhere".
		},
		{
			name:    "no folders configured means the old behaviour exactly",
			video:   videoPath,
			caption: folderCap,
			folders: nil,
			want:    false,
		},
		{
			name:    "an empty folder entry does not match everything",
			video:   videoPath,
			caption: otherCap,
			folders: []string{""},
			want:    false,
			// An empty string in the config must not Clean() down to "." and match the whole tree.
		},
		{
			name:    "a trailing slash on the configured folder still matches",
			video:   videoPath,
			caption: folderCap,
			folders: []string{"/library/subs/"},
			want:    true,
		},
		{
			name:    "a . prefix on the configured folder still matches",
			video:   videoPath,
			caption: folderCap,
			folders: []string{"/library/./subs"},
			want:    true,
		},
		{
			name:    "the caption's own directory being cleaned does not matter",
			video:   videoPath,
			caption: "/library/subs/./scene.en.srt",
			folders: []string{"/library/subs"},
			want:    true,
		},
		{
			name:    "a different basename in a configured folder does not match",
			video:   videoPath,
			caption: "/library/subs/other.en.srt",
			folders: folders,
			want:    false,
			// The folder is a search location, not a licence to attach anything in it.
		},
		{
			name:    "a different extension in a configured folder does not match",
			video:   videoPath,
			caption: "/library/subs/scene.en.mp4",
			folders: folders,
			want:    false,
		},
		{
			name:    "multiple folders, second one matches",
			video:   videoPath,
			caption: "/mnt/subtitles/scene.en.srt",
			folders: []string{"/library/subs", "/mnt/subtitles"},
			want:    true,
			// The realistic case: a library folder AND a mounted share.
		},
		{
			name:    "case differences are not treated as equal on a case-sensitive filesystem",
			video:   videoPath,
			caption: "/library/Subs/scene.en.srt",
			folders: []string{"/library/subs"},
			want:    false,
		},
		{
			name:    "a caption file with no language code matches in a folder",
			video:   videoPath,
			caption: "/library/subs/scene.srt",
			folders: folders,
			want:    true,
		},
		{
			name:    "a nested path under the configured folder does NOT match",
			video:   videoPath,
			caption: "/library/subs/season1/scene.en.srt",
			folders: folders,
			want:    false,
			// Deliberate, and worth stating: the configured folders are exact directories, not roots to
			// be searched recursively. A recursive search would make it impossible to scope a folder to
			// one library, and the scanner's own walk is what decides what gets visited.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesCaptionInFolders(tt.video, tt.caption, tt.folders)
			if got != tt.want {
				t.Errorf("MatchesCaptionInFolders(%q, %q, %v) = %v, want %v",
					tt.video, tt.caption, tt.folders, got, tt.want)
			}
		})
	}
}

// TestMatchesCaptionIsUnchangedByThisFeature pins the pre-existing matcher, because the new function must
// be strictly a widening of it. If someone changes MatchesCaption's semantics, this fails.
func TestMatchesCaptionIsUnchangedByThisFeature(t *testing.T) {
	tests := []struct {
		video, caption string
		want           bool
	}{
		{"/library/scene.mp4", "/library/scene.en.srt", true},
		{"/library/scene.mp4", "/library/scene.srt", true},
		{"/library/scene.mp4", "/library/scene.vtt", true},
		{"/library/scene.mp4", "/library/subs/scene.en.srt", false},
		{"/library/scene.mp4", "/library/other.en.srt", false},
		{"/library/scene.mkv", "/library/scene.en.srt", true},
	}

	for _, tt := range tests {
		if got := MatchesCaption(tt.video, tt.caption); got != tt.want {
			t.Errorf("MatchesCaption(%q, %q) = %v, want %v", tt.video, tt.caption, got, tt.want)
		}
	}
}
