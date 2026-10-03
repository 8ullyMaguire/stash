package manager

import (
	"context"
	"fmt"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/paths"
	"github.com/stashapp/stash/pkg/scene/generate"
)

type GenerateSpriteTask struct {
	Scene               models.Scene
	Overwrite           bool
	fileNamingAlgorithm models.HashAlgorithm
}

func (t *GenerateSpriteTask) GetDescription() string {
	return fmt.Sprintf("Generating sprites for %s", t.Scene.Path)
}

func (t *GenerateSpriteTask) Start(ctx context.Context) {
	// #3530 - the sprite must be sampled from INSIDE the scene's window, and that means the
	// scene's files have to be loaded: this task used to probe the FILE and never look at the
	// scene, so it had no window to honour. Every derived quantity -- the grid's length, where it
	// starts, whether tiles are sought by frame -- came from the file's own duration.
	//
	// LoadPrimaryFile is read-only here, so it does not need a transaction, and it yields the
	// per-scene copy whose StartTime/EndTime are the window itself. A load failure is not fatal:
	// the probe below still runs and the grid is then the file's, which is the pre-#3530
	// behaviour rather than a wrong-window grid.
	window, sceneHash := t.windowAndKey()

	if !t.spriteRequired(sceneHash) {
		return
	}

	ffprobe := instance.FFProbe
	videoFile, err := ffprobe.NewVideoFile(t.Scene.Path)
	if err != nil {
		logger.Errorf("error reading video file: %s", err.Error())
		return
	}

	imagePath := instance.Paths.Scene.GetSpriteImageFilePath(sceneHash)
	vttPath := instance.Paths.Scene.GetSpriteVttFilePath(sceneHash)

	cfg := DefaultSpriteGeneratorConfig
	cfg.SpriteSize = instance.Config.GetSpriteScreenshotSize()

	if instance.Config.GetUseCustomSpriteInterval() {
		cfg.MinimumSprites = instance.Config.GetMinimumSprites()
		cfg.MaximumSprites = instance.Config.GetMaximumSprites()
		cfg.SpriteInterval = instance.Config.GetSpriteInterval()
	}

	generator, err := NewSpriteGenerator(*videoFile, sceneHash, imagePath, vttPath, cfg, window)

	if err != nil {
		logger.Errorf("error creating sprite generator: %s", err.Error())
		return
	}
	generator.Overwrite = t.Overwrite

	if err := generator.Generate(); err != nil {
		logger.Errorf("error generating sprite: %s", err.Error())
		logErrorOutput(err)
		return
	}
}

// windowAndKey loads the scene's window and computes the cache key it produces.
//
// #3530 - both come from the SAME load, and the key is computed AFTER it, which is the whole
// ordering constraint. Until the scene's files are loaded there is no window to put in the key,
// so a key computed at the top of Start would be the plain hash: the sprite would be WRITTEN to
// the unwindowed path while the ROUTE looks for the windowed one, and two scenes of one file
// would overwrite each other's sprite on every regeneration with no error anywhere.
//
// LoadPrimaryFileWithWindow, NOT LoadPrimaryFile. This is measured, not believed:
// pkg/sqlite/scene_window_loader_test.go drives both over one ranged scene and finds
// LoadPrimaryFile reporting the file's own duration and a nil StartTime. Used here, every arithmetic
// test below stays green and the sprite is of the wrong footage -- which is the defect this whole
// change exists to remove.
//
// A load failure is not fatal: the grid then falls back to the file's own duration, which is the
// pre-#3530 behaviour rather than a grid taken from a window we could not read.
func (t *GenerateSpriteTask) windowAndKey() (generate.SceneWindow, string) {
	window := generate.SceneWindow{}
	if err := t.Scene.LoadPrimaryFileWithWindow(context.TODO(), instance.Repository.Scene); err != nil {
		logger.Debugf("error loading primary file for sprite of scene %d: %v", t.Scene.ID, err)
	} else if vf := t.Scene.Files.Primary(); vf != nil {
		window = generate.WindowOf(vf)
	}

	return window, models.GeneratedChecksum(t.Scene, t.fileNamingAlgorithm)
}

// spriteRequired returns true if the sprite needs to be generated, for the key this scene's window
// produces.
//
// #3530 - the key is a PARAMETER rather than recomputed inside, because Start computes it after
// loading the window and the two must be the same string. A second `t.Scene.GetHash(...)` here
// would be the plain file hash, which for a windowed scene is the *other* scene's key: the check
// would look at the other scene's sprite, find it present, and skip -- so a windowed scene would
// never get a sprite at all.
//
// It is also the form task_generate.go's queue check needs, which runs before Start and so cannot
// rely on anything loaded there.
func (t *GenerateSpriteTask) spriteRequired(sceneHash string) bool {
	if t.Scene.Path == "" {
		return false
	}

	if t.Overwrite {
		return true
	}

	return !t.doesSpriteExist(sceneHash)
}

func (t *GenerateSpriteTask) doesSpriteExist(sceneChecksum string) bool {
	if sceneChecksum == "" {
		return false
	}

	// Both paths count for both files, for the same reason as the transcode
	// check: a false negative here re-generates the sprite for every scene. (stash#2824)
	sp := instance.Paths.Scene
	imageExists := paths.ResolveGeneratedFile(sp.GetSpriteImageFilePath(sceneChecksum), sp.GetLegacySpriteImageFilePath(sceneChecksum)) != ""
	vttExists := paths.ResolveGeneratedFile(sp.GetSpriteVttFilePath(sceneChecksum), sp.GetLegacySpriteVttFilePath(sceneChecksum)) != ""
	return imageExists && vttExists
}
