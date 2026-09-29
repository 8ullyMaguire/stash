package manager

import (
	"context"
	"fmt"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/paths"
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
	if !t.required() {
		return
	}

	ffprobe := instance.FFProbe
	videoFile, err := ffprobe.NewVideoFile(t.Scene.Path)
	if err != nil {
		logger.Errorf("error reading video file: %s", err.Error())
		return
	}

	sceneHash := t.Scene.GetHash(t.fileNamingAlgorithm)
	imagePath := instance.Paths.Scene.GetSpriteImageFilePath(sceneHash)
	vttPath := instance.Paths.Scene.GetSpriteVttFilePath(sceneHash)

	cfg := DefaultSpriteGeneratorConfig
	cfg.SpriteSize = instance.Config.GetSpriteScreenshotSize()

	if instance.Config.GetUseCustomSpriteInterval() {
		cfg.MinimumSprites = instance.Config.GetMinimumSprites()
		cfg.MaximumSprites = instance.Config.GetMaximumSprites()
		cfg.SpriteInterval = instance.Config.GetSpriteInterval()
	}

	generator, err := NewSpriteGenerator(*videoFile, sceneHash, imagePath, vttPath, cfg)

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

// required returns true if the sprite needs to be generated
func (t GenerateSpriteTask) required() bool {
	if t.Scene.Path == "" {
		return false
	}

	if t.Overwrite {
		return true
	}

	sceneHash := t.Scene.GetHash(t.fileNamingAlgorithm)
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
