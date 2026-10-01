package manager

import (
	"context"
	"errors"
	"testing"

	"github.com/stashapp/stash/internal/manager/task"
	"github.com/stashapp/stash/pkg/models"
)

// Tests for the scene target lister.
//
// # What these are for
//
// The lister is the one place that decides WHICH videos a pass examines, and
// every way it can be wrong is a pass that either misses a person or
// clusters a preview. Both look identical from the outside -- a library with
// clusters and a library without -- so the decisions are asserted here rather
// than left to a full pass over a real library.

// stubSceneReader is a SceneReader that answers All and GetFiles.
type stubSceneReader struct {
	scenes []*models.Scene
	files  map[int][]*models.VideoFile

	allErr  error
	fileErr map[int]error

	allCalls int
	fileIDs  []int
}

func (r *stubSceneReader) All(ctx context.Context) ([]*models.Scene, error) {
	r.allCalls++
	return r.scenes, r.allErr
}

func (r *stubSceneReader) GetFiles(ctx context.Context, id int) ([]*models.VideoFile, error) {
	r.fileIDs = append(r.fileIDs, id)
	if err, ok := r.fileErr[id]; ok {
		return nil, err
	}
	return r.files[id], nil
}

// vid builds a video file with the fields the lister reads.
func vid(path string, size int64, duration float64) *models.VideoFile {
	return &models.VideoFile{
		BaseFile: &models.BaseFile{Path: path, Size: size},
		Duration: duration,
	}
}

// --- the one decision: which file ---

// TestTargetFromFilesPicksTheLargest is the preview problem.
//
// A scene with a 4K remux and a 240p preview has both attached. The preview is
// a different picture of the same footage, at a scale the detector was not
// trained for, so building clusters from it means building them from the worst
// available image of each person -- and the clusters would be confidently
// wrong rather than absent.
func TestTargetFromFilesPicksTheLargest(t *testing.T) {
	s := &models.Scene{ID: 1}
	files := []*models.VideoFile{
		vid("/preview.mp4", 2_000_000, 600),
		vid("/remux.mkv", 8_000_000_000, 601),
	}

	got, ok := targetFromFiles(s, files)
	if !ok {
		t.Fatal("a scene with two files produced no target")
	}
	if got.Path != "/remux.mkv" {
		t.Errorf("picked %q, want the 8GB remux: the preview is a "+
			"lower-resolution picture of the same footage and the detector "+
			"was not trained for it", got.Path)
	}
	if got.DurationSeconds != 601 {
		t.Errorf("duration %d, want 601 -- and note it is the remux's, not "+
			"the preview's, which is the other half of picking the right file",
			got.DurationSeconds)
	}
}

// TestTargetFromFilesSkipsUnusableFiles covers the three ways a file can be
// unusable, all of which produce a nil dereference or an empty path rather
// than an error.
func TestTargetFromFilesSkipsUnusableFiles(t *testing.T) {
	s := &models.Scene{ID: 1}

	cases := []struct {
		name  string
		files []*models.VideoFile
	}{
		{"no files", nil},
		{"a nil entry", []*models.VideoFile{nil}},
		{"a nil BaseFile", []*models.VideoFile{{}}},
		{"an empty path", []*models.VideoFile{vid("", 100, 10)}},
		{"all unusable", []*models.VideoFile{nil, {}, vid("", 1, 1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := targetFromFiles(s, tc.files); ok {
				t.Error("a target was built from a file with no path; the pass " +
					"would hand it to ffmpeg as an empty filename")
			}
		})
	}
}

// TestTargetFromFilesFallsBackToAUsableFile checks the skip is a skip and not
// an abort: a good file after three bad ones must still be found.
func TestTargetFromFilesFallsBackToAUsableFile(t *testing.T) {
	got, ok := targetFromFiles(&models.Scene{ID: 1}, []*models.VideoFile{
		nil, {}, vid("", 0, 0), vid("/good.mkv", 10, 30),
	})
	if !ok {
		t.Fatal("a usable file after three unusable ones was not found")
	}
	if got.Path != "/good.mkv" {
		t.Errorf("picked %q, want /good.mkv", got.Path)
	}
}

// --- the listing ---

// TestSceneTargetListerMakesOneTargetPerScene is the shape of a normal run.
func TestSceneTargetListerMakesOneTargetPerScene(t *testing.T) {
	repo := &stubSceneReader{
		scenes: []*models.Scene{{ID: 1}, {ID: 2}},
		files: map[int][]*models.VideoFile{
			1: {vid("/a.mkv", 100, 120)},
			2: {vid("/b.mkv", 200, 240)},
		},
	}

	got, err := sceneTargetLister{repo: repo}.TargetList(context.Background())
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("%d targets, want 2", len(got))
	}

	if got[0].TargetType != "scene" {
		t.Errorf("target type %q, want \"scene\": the type is part of the "+
			"membership key, so a wrong one files a face under something "+
			"that does not exist", got[0].TargetType)
	}
	if got[0].TargetID != 1 || got[1].TargetID != 2 {
		t.Errorf("targets %d and %d, want 1 and 2 in order", got[0].TargetID, got[1].TargetID)
	}
}

// TestSceneTargetListerSkipsScenesWithNoFile is the empty-stash-entry case.
//
// A scene with no video file is not broken, and reporting it as an error would
// make a pass over a large library fail on entries the user did not know they
// had.
func TestSceneTargetListerSkipsScenesWithNoFile(t *testing.T) {
	repo := &stubSceneReader{
		scenes: []*models.Scene{{ID: 1}, {ID: 2}, {ID: 3}},
		files: map[int][]*models.VideoFile{
			1: {vid("/a.mkv", 100, 120)},
			3: {vid("/c.mkv", 100, 120)},
			// 2 has no entry: a scene with nothing behind it.
		},
	}

	got, err := sceneTargetLister{repo: repo}.TargetList(context.Background())
	if err != nil {
		t.Fatalf("a scene with no file failed the whole listing: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("%d targets, want 2: the empty scene should be skipped, "+
			"not turned into a target and not treated as a failure", len(got))
	}
	for _, tg := range got {
		if tg.TargetID == 2 {
			t.Error("a target was built for a scene with no file")
		}
	}
}

// TestSceneTargetListerKeepsGoingWhenOneSceneFails is the availability rule at
// the listing level.
//
// The scenes are already enumerated by the time any file lookup happens, so
// there is nothing to roll back and nothing inconsistent. Losing one video's
// faces is much better than losing the pass.
func TestSceneTargetListerKeepsGoingWhenOneSceneFails(t *testing.T) {
	repo := &stubSceneReader{
		scenes: []*models.Scene{{ID: 1}, {ID: 2}, {ID: 3}},
		files: map[int][]*models.VideoFile{
			1: {vid("/a.mkv", 100, 120)},
			3: {vid("/c.mkv", 100, 120)},
		},
		fileErr: map[int]error{2: errors.New("row not found")},
	}

	got, err := sceneTargetLister{repo: repo}.TargetList(context.Background())
	if err != nil {
		t.Fatalf("one scene's file lookup failed the whole listing: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("%d targets, want 2: one unreadable scene must not stop "+
			"the pass over every other scene in the library", len(got))
	}
}

// TestSceneTargetListerPropagatesTheListingFailure is the other direction: a
// failure to enumerate the scenes IS fatal, because there is nothing to work
// with and a pass over no targets reports an empty library.
func TestSceneTargetListerPropagatesTheListingFailure(t *testing.T) {
	repo := &stubSceneReader{allErr: errors.New("database is locked")}

	got, err := sceneTargetLister{repo: repo}.TargetList(context.Background())
	if err == nil {
		t.Fatal("a listing failure was swallowed; the job would report an " +
			"empty library, which is indistinguishable from a library with " +
			"no faces in it")
	}
	if got != nil {
		t.Errorf("%d targets returned alongside the error; a caller that "+
			"ignores the error would pass a partial library to the pass", len(got))
	}
}

// TestSceneTargetListerSkipsNilScenes is defensive but cheap: a nil in the
// slice would panic the listing, and a pass that panics twenty minutes in
// leaves no clusters at all.
func TestSceneTargetListerSkipsNilScenes(t *testing.T) {
	repo := &stubSceneReader{
		scenes: []*models.Scene{nil, {ID: 2}},
		files:  map[int][]*models.VideoFile{2: {vid("/b.mkv", 10, 20)}},
	}

	got, err := sceneTargetLister{repo: repo}.TargetList(context.Background())
	if err != nil {
		t.Fatalf("a nil scene in the slice failed the listing: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("%d targets, want 1", len(got))
	}
}

// The lister is a task.TargetLister, and the job cannot be built without one.
// This is the assertion that the two agree, at compile time, and it is the seam
// the job's own tests stub out -- so a change to either name without the other
// is a build error rather than a job that lists nothing.
var _ task.TargetLister = sceneTargetLister{}
