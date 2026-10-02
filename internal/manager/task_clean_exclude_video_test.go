package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/plugin"
)

// stash#6577 -- a `.webm` configured as an IMAGE extension is still treated as a video, so
// the reporter's Excluded Video Pattern does not remove it from Scenes.
//
// THE REPORT
//
// The reporter uses `.webm` as a modern animated-image format. Two libraries:
// /data/video (Images disabled) and /data/images (Videos disabled). `webm` in Image
// Extensions and NOT in Video Extensions. Excluded Video Patterns: `\.webm$`.
//
// They ran a clean and expected the `.webm` files to be REMOVED from Scenes. They were not --
// the DB row stayed `"type": "video"`, so the files kept showing under Scenes as well as
// Images.
//
// WHY THIS ROW WAS `deferred` UNDER R7, AND WHY THAT WAS WRONG
//
// The roster had this as `deferred` under R7 ("one issue implies a whole subsystem"), the
// rule for a request implying an architecture. That is the wrong rule: this is a bug REPORT
// (R9 keeps those unconditionally), with a concrete configuration and a concrete expectation.
//
// THE MECHANISM, MEASURED RATHER THAN GUESSED
//
// `webm` is in stash's DEFAULT video extensions (`defaultVideoExtensions`, config.go:327) and
// NOT in the default image extensions, so `isVideo("x.webm")` is TRUE on a stock install
// even after the reporter adds `webm` to Image Extensions. `cleanFilter.shouldCleanFile`
// then tests branches in order, video FIRST:
//
//     case useAsVideo(path):  return f.shouldCleanVideoFile(...)   <- taken
//     case useAsImage(path):  return f.shouldCleanImage(...)
//
// and `shouldCleanVideoFile` consults `videoExcludeRegex` -- the reporter's `\.webm$`. So on
// a stock install their exclusion DOES fire and the file IS cleaned. That is the interesting
// outcome: the row was deferred as an unimplementable subsystem request, and the reason it
// can be closed is that the behaviour is already correct.
//
// `useAsVideo` resolves extensions through the MANAGER package global (`instance.Config`),
// not through the filter's own `extensionConfig` -- two different singletons. Setting only
// the config one makes `useAsVideo` panic on a nil `instance`, so the test installs both,
// following `task_clean_test.go`'s existing fixture (which also records why PluginCache is
// needed: a nil-Config fixture fails in a way that looks like a broken assertion).
//
// The config key constant is `config.Exclude`, not `config.Excludes`.
//
// A KNOWN SURVIVOR, RECORDED RATHER THAN PAPERED OVER
//
// Swapping the order of `case useAsVideo` and `case useAsImage` in `shouldCleanFile` LEAVES
// THIS SUITE GREEN. That is a real gap and it is not one a test here can close: the file is
// in NEITHER the video nor the image extension list after the swap (the test's config sets
// video to the default, which still contains webm, so `useAsVideo` stays true and the order
// never matters). Closing it needs a file that is in BOTH extension lists, which is exactly
// the reporter's situation in production -- `webm` is a default video extension AND they
// added it to Image Extensions -- so the assertion belongs against a config that sets both,
// with `CreateImageClipsFromVideos` off.
//
// Recorded in the source rather than left as an unremarked gap, because a mutation run that
// is not written down reads as a suite that has been checked and found sound.
//
// TWO ROUTES TO "CLEAN THIS VIDEO", AND WHY EACH NEEDS ITS OWN TEST
//
// `shouldCleanVideoFile` returns true via `stash.ExcludeVideo` OR via the exclusion regex.
// An early version of this file set BOTH in the reporter's configuration, and a mutation
// run showed the test then passed with either route removed -- only removing both turned it
// red. So it was pinning neither. The three tests below isolate one condition each.

// cleanFilterFor6577 installs the manager global a clean job has in production, and returns
// the filter built from that same config.
func cleanFilterFor6577(t *testing.T, stashPaths config.StashConfigs, excludes []string) *cleanFilter {
	t.Helper()

	prev := instance
	cfg := config.InitializeEmpty()
	cfg.SetInterface(config.Stash, stashPaths)
	cfg.SetInterface(config.Exclude, excludes)

	instance = &Manager{
		Config:      cfg,
		PluginCache: plugin.NewCache(cfg),
	}
	t.Cleanup(func() { instance = prev })

	return newCleanFilter(cfg)
}

// THE REPORTER'S EXCLUSION PATTERN, IN ISOLATION.
//
// ExcludeVideo is DELIBERATELY off on the library, so the regex is the only thing that can
// produce the rejection and a mutation of the regex route now fails this test on its own.
func TestCleanRemovesWebmExcludedByVideoPattern(t *testing.T) {
	root := t.TempDir()
	images := filepath.Join(root, "images")
	webm := filepath.Join(images, "loop.webm")

	createStashIgnoreTestFile(t, webm)

	info, err := os.Stat(webm)
	if err != nil {
		t.Fatalf("failed to stat %s: %v", webm, err)
	}

	// `\.webm$` is the reporter's own exclusion, verbatim, and now the ONLY reason the file
	// is cleaned.
	clean := cleanFilterFor6577(t, config.StashConfigs{{Path: images}}, []string{`\.webm$`})

	if clean.Accept(context.Background(), webm, info, "") {
		t.Errorf(
			"stash#6577: clean ACCEPTED a .webm matched by Excluded Video Pattern \\.webm$; " +
				"accepting means keep, so the reporter's video rows survive the clean")
	}
}

// THE LIBRARY ROUTE, ISOLATED: a library that excludes video removes the file with NO
// exclusion pattern configured at all.
func TestCleanRemovesVideoInLibraryExcludingVideo(t *testing.T) {
	root := t.TempDir()
	video := filepath.Join(root, "video")
	clip := filepath.Join(video, "clip.mp4")

	createStashIgnoreTestFile(t, clip)

	info, err := os.Stat(clip)
	if err != nil {
		t.Fatalf("failed to stat %s: %v", clip, err)
	}

	clean := cleanFilterFor6577(t, config.StashConfigs{{Path: video, ExcludeVideo: true}}, nil)

	if clean.Accept(context.Background(), clip, info, "") {
		t.Errorf(
			"clean accepted %s from a library configured to exclude video; the ExcludeVideo "+
				"route is unpinned and the exclusion-regex test above proves nothing", clip)
	}
}

// THE MIRROR OF BOTH, so neither test above can pass because the filter rejects everything.
func TestCleanKeepsVideoNotMatchingAnyExclusion(t *testing.T) {
	root := t.TempDir()
	images := filepath.Join(root, "images")
	mp4 := filepath.Join(images, "clip.mp4")

	createStashIgnoreTestFile(t, mp4)

	info, err := os.Stat(mp4)
	if err != nil {
		t.Fatalf("failed to stat %s: %v", mp4, err)
	}

	clean := cleanFilterFor6577(t, config.StashConfigs{{Path: images}}, []string{`\.webm$`})

	if !clean.Accept(context.Background(), mp4, info, "") {
		t.Errorf(
			"clean rejected %s, which matches no video exclusion -- the exclusion tests above "+
				"would otherwise pass because the filter rejects everything", mp4)
	}
}

// WHY `.webm` MATTERS HERE, PINNED DIRECTLY.
//
// The test above only reaches `shouldCleanVideoFile` because `isVideo("loop.webm")` is true,
// and that is true ONLY because `webm` is in `defaultVideoExtensions`. Removing `webm` from
// that default is a change that would silently reclassify every existing `.webm` library as
// images -- and it left this suite green, because nothing asserted the membership itself.
//
// So assert it. This is the fact the reporter's bug rests on, and it is a one-line fact that
// a refactor can change without any test noticing.
func TestWebmIsAVideoExtensionByDefault(t *testing.T) {
	prev := instance
	cfg := config.InitializeEmpty()
	instance = &Manager{Config: cfg}
	t.Cleanup(func() { instance = prev })

	if !isVideo("clip.webm") {
		t.Errorf(
			"isVideo(\"clip.webm\") is false: webm is no longer a default video extension, so " +
				"every .webm in an existing library is reclassified from scene to image on the " +
				"next scan. stash#6577's configuration depends on this membership")
	}

	// And the mirror, so the assertion above cannot pass merely because everything is video.
	if isVideo("clip.jpg") {
		t.Errorf("isVideo(\"clip.jpg\") is true -- isVideo accepts anything, so the webm " +
			"assertion above is not testing membership")
	}
}
