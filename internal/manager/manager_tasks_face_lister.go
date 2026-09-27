package manager

import (
	"context"

	"github.com/stashapp/stash/internal/cluster"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
)

// sceneTargetLister turns the scene table into the targets a clustering pass
// examines.
//
// # Why this is a type rather than a function in the job file
//
// The job takes a `TargetLister` interface so it can be tested with three
// targets and no database, and so the answer to "which videos does a pass
// examine" lives in one named place rather than in a switch inside a job. This
// is that place: the whole library's scenes, each with the video file a face can
// actually be found in.
//
// # The one decision this makes
//
// Every scene, not just the ones a user asked about. A person who appears in
// forty films is one person because the pass saw all forty, so a pass that
// looked only at the current scene would produce a library of singletons and
// report no faces found. The cost is a pass over the whole library, which is
// why it is a job the user starts rather than something that happens on scan.
//
// Scenes with no video file are skipped rather than reported as failures. They
// are not broken; they are a stash entry with nothing behind it, and a pass
// that tried to decode a file it does not have would report an error for every
// one of them.

// sceneReader is the two methods the lister needs from the scene store.
//
// Declared here rather than taking `models.SceneReader`, which is twenty-odd
// methods spanning every mutating operation on a scene. A pass that renames or
// deletes a scene has no business reading the library, and the narrow interface
// is the only thing stopping it -- a parameter typed as the full interface
// hands every one of those methods over and relies on the caller not calling
// them.
//
// It is also why the lister is testable at all: `models.SceneReader` would need
// twenty methods faked, and a fake that implements twenty methods to check two
// of them is a fake whose other eighteen are wrong.
type sceneReader interface {
	All(ctx context.Context) ([]*models.Scene, error)
	GetFiles(ctx context.Context, id int) ([]*models.VideoFile, error)
}

// sceneTargetLister lists every scene's primary video file.
type sceneTargetLister struct {
	repo sceneReader
}

// TargetList returns one target per scene that has a video file.
//
// Order is scene ID ascending, which is stable across runs. The pass is
// order-dependent -- a face can only join a cluster formed from faces found
// earlier -- so a pass that enumerated in a random order would produce
// different clusters on the same library, and a user re-running it to fix one
// bad cluster would get a different answer for every other cluster too.
func (l sceneTargetLister) TargetList(ctx context.Context) ([]cluster.Target, error) {
	scenes, err := l.repo.All(ctx)
	if err != nil {
		return nil, err
	}

	targets := make([]cluster.Target, 0, len(scenes))
	noFile := 0

	for _, s := range scenes {
		if s == nil {
			continue
		}
		files, err := l.repo.GetFiles(ctx, s.ID)
		if err != nil {
			// One scene failing to list its files must not stop the pass. The
			// scenes are already enumerated, so there is nothing to roll back;
			// losing one video's faces is much better than losing the pass.
			logger.Warnf("Face clustering: listing files for scene %d: %v", s.ID, err)
			continue
		}

		t, ok := targetFromFiles(s, files)
		if !ok {
			noFile++
			continue
		}
		targets = append(targets, t)
	}

	if noFile > 0 {
		logger.Infof("Face clustering: %d scene(s) have no video file to "+
			"examine", noFile)
	}
	return targets, nil
}

// targetFromFiles picks the video a pass will decode from a scene's files.
//
// The LARGEST file, and the reason matters: a scene with a 4K remux and a
// 240p preview has both attached, and the preview is a different picture of the
// same footage. Decoding the low-resolution one and finding faces in it is
// finding faces the user did not upload, at a scale the detector was not
// trained for -- so the clusters built from it would be built from the worst
// available image of each person.
func targetFromFiles(s *models.Scene, files []*models.VideoFile) (cluster.Target, bool) {
	var best *models.VideoFile
	for _, f := range files {
		if f == nil || f.BaseFile == nil || f.Path == "" {
			continue
		}
		if best == nil || f.Size > best.Size {
			best = f
		}
	}
	if best == nil {
		return cluster.Target{}, false
	}

	return cluster.Target{
		TargetType:      "scene",
		TargetID:        int64(s.ID),
		Path:            best.Path,
		DurationSeconds: int(best.Duration),
	}, true
}
