package video

import (
	"context"
	"testing"

	"github.com/stashapp/stash/pkg/models"
)

// stash#6744 -- findCaptionTargets: which videos a caption may belong to.
//
// This is the half of the feature that the scanner reaches FIRST. When the walk encounters a caption file
// it calls AssociateCaptions immediately, and only falls back to holding the path in unmatchedCaptionFiles
// if that returns false. So if this lookup cannot see a video in another directory, the caption is dropped
// before the folder-aware matching in the scan loop ever runs.
//
// It needs the two-lookup structure it has: FindAllByPath filters on the DIRECTORY as well as the
// basename (pkg/sqlite/file.go:686-690), so a single lookup cannot both respect the video's own directory
// and reach outside it.

type fakeFinder struct {
	// results maps a search pattern to the files it should return.
	results map[string][]models.File

	// searches records every pattern queried, so a test can assert what was NOT searched.
	searches []string
}

func (f *fakeFinder) FindAllByPath(_ context.Context, p string, _ bool) ([]models.File, error) {
	f.searches = append(f.searches, p)
	return f.results[p], nil
}

func vf(id int, path string) models.File {
	return &models.VideoFile{BaseFile: &models.BaseFile{ID: models.FileID(id), Path: path}}
}

func TestFindCaptionTargets(t *testing.T) {
	const (
		captionPath   = "/library/subs/scene.en.srt"
		captionPrefix = "/library/subs/scene."
	)

	// Vars, not consts: these are interface values built by a function call, which Go does not allow in a
	// const block.
	sidecarVideo := vf(1, "/library/vids/scene.mp4")
	farawayVid := vf(2, "/elsewhere/vids/scene.mp4")
	otherVid := vf(3, "/elsewhere/vids/other.mp4")

	t.Run("no folders configured does only the ordinary lookup", func(t *testing.T) {
		f := &fakeFinder{results: map[string][]models.File{
			captionPrefix + "*": {sidecarVideo},
		}}

		got, err := findCaptionTargets(context.Background(), f, captionPath, captionPrefix, nil)
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 1 || got[0].Base().ID != 1 {
			t.Errorf("got %d targets, want just the same-directory video", len(got))
		}
		if len(f.searches) != 1 {
			t.Errorf("searched %v, want only the prefix lookup when nothing is configured", f.searches)
		}
	})

	t.Run("a configured folder adds a basename-only search", func(t *testing.T) {
		f := &fakeFinder{results: map[string][]models.File{
			captionPrefix + "*": {sidecarVideo},
			"%/scene.*":         {sidecarVideo, farawayVid, otherVid},
		}}

		got, err := findCaptionTargets(context.Background(), f, captionPath, captionPrefix, []string{"/library/subs"})
		if err != nil {
			t.Fatal(err)
		}

		// `other.mp4` has a different basename and must be excluded by the caller's own matching; what is
		// pinned here is that the wide search HAPPENS and the same-directory hit is not duplicated.
		if len(got) != 2 {
			t.Fatalf("got %d targets (%v), want 2 after de-duplication", len(got), got)
		}
		if got[0].Base().ID != 1 || got[1].Base().ID != 2 {
			t.Errorf("got ids %d,%d; want 1,2", got[0].Base().ID, got[1].Base().ID)
		}
	})

	t.Run("a caption outside every configured folder is not matched across directories", func(t *testing.T) {
		// The important guard. The user configured /library/subs, and this caption is in /library/other.
		// Widening the search for it would attach subtitles found anywhere in the library, which is not
		// what configuring a folder asks for.
		//
		// The caption path here is deliberately NOT the shared captionPath, which lives in the configured
		// folder. An earlier version of this test reused it and so asserted the guard against a caption the
		// guard does not apply to -- it passed for the wrong reason, or rather it failed and I had to work
		// out why the code was right and the test was wrong.
		const (
			elsewhereCap = "/library/other/scene.en.srt"
			elsewherePfx = "/library/other/scene."
		)

		f := &fakeFinder{results: map[string][]models.File{
			elsewherePfx + "*": {},
			"%/scene.*":        {farawayVid},
		}}

		got, err := findCaptionTargets(context.Background(), f, elsewhereCap, elsewherePfx, []string{"/library/subs"})
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 0 {
			t.Errorf("got %d targets, want none", len(got))
		}
		if len(f.searches) != 1 {
			t.Errorf("searched %v, want only the prefix lookup", f.searches)
		}
	})

	t.Run("an empty folder entry does not trigger the wide search", func(t *testing.T) {
		f := &fakeFinder{results: map[string][]models.File{captionPrefix + "*": {sidecarVideo}}}

		got, err := findCaptionTargets(context.Background(), f, captionPath, captionPrefix, []string{""})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Errorf("got %d targets, want 1", len(got))
		}
		if len(f.searches) != 1 {
			t.Errorf("searched %v, want no wide search for an empty folder entry", f.searches)
		}
	})

	t.Run("a trailing slash on the configured folder still triggers the wide search", func(t *testing.T) {
		f := &fakeFinder{results: map[string][]models.File{
			captionPrefix + "*": {},
			"%/scene.*":         {farawayVid},
		}}

		got, _ := findCaptionTargets(context.Background(), f, captionPath, captionPrefix, []string{"/library/subs/"})
		if len(got) != 1 {
			t.Errorf("got %d targets, want 1", len(got))
		}
	})
}
