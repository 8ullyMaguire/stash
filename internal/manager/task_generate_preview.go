package manager

import (
	"context"
	"fmt"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/paths"
	"github.com/stashapp/stash/pkg/scene/generate"
)

type GeneratePreviewTask struct {
	Scene        models.Scene
	ImagePreview bool

	Options generate.PreviewOptions

	Overwrite           bool
	fileNamingAlgorithm models.HashAlgorithm

	generator *generate.Generator

	videoPreviewExists *bool
	imagePreviewExists *bool
}

func (t *GeneratePreviewTask) GetDescription() string {
	return fmt.Sprintf("Generating preview for %s", t.Scene.Path)
}

func (t *GeneratePreviewTask) Start(ctx context.Context) {
	// #3530 - the preview must be sampled from INSIDE the scene's window, and that means the
	// scene's files have to be loaded: the task previously probed the FILE directly and never
	// looked at the scene, so it had no window to honour.
	//
	// LoadPrimaryFile returns the per-scene copy, whose Duration is the WINDOW's length (the
	// derived-duration change) and whose StartTime/EndTime are the window itself. It is
	// read-only here, so it does not need to be in a transaction.
	window := generate.SceneWindow{}
	var sceneDuration float64
	if err := t.Scene.LoadPrimaryFile(context.TODO(), instance.Repository.File); err == nil {
		if vf := t.Scene.Files.Primary(); vf != nil {
			window = generate.WindowOf(vf)
			// Prefer the window's length. Falling back to the probe's own figure covers a
			// scene whose file is missing, and an unranged scene (window unset) where the two
			// are the same number anyway.
			sceneDuration = vf.Duration
		}
	}

	// #3530 - the cache key must be computed HERE, after the scene's files are loaded and not at
	// the top of Start, because until they are loaded there is no window to put in it.
	//
	// That ordering is load-bearing. An earlier version computed videoChecksum first, so with this
	// commit's routes in place the preview would be WRITTEN to the unwindowed path while the ROUTE
	// looks for the windowed one -- and the file would be regenerated on every single request,
	// forever, with no error anywhere.
	//
	// It sits outside both generation blocks because the webp uses it too and may run when the
	// video preview is skipped (already generated).
	videoChecksum := models.GeneratedChecksum(t.Scene, t.fileNamingAlgorithm)

	if t.videoPreviewRequired() {
		ffprobe := instance.FFProbe
		videoFile, err := ffprobe.NewVideoFile(t.Scene.Path)
		if err != nil {
			logger.Errorf("error reading video file: %v", err)
			return
		}

		duration := videoFile.VideoStreamDuration
		if sceneDuration > 0 {
			duration = sceneDuration
		}

		// The window travels with the options, so the generator rebases its tile grid onto it.
		t.Options.Window = window

		if err := t.generateVideo(videoChecksum, duration, videoFile.FrameRate); err != nil {
			logger.Errorf("error generating preview: %v", err)
			logErrorOutput(err)
			return
		}
	}

	if t.imagePreviewRequired() {
		if err := t.generateWebp(videoChecksum); err != nil {
			logger.Errorf("error generating preview webp: %v", err)
			logErrorOutput(err)
		}
	}
}

func (t *GeneratePreviewTask) generateVideo(videoChecksum string, videoDuration float64, videoFrameRate float64) error {
	videoFilename := t.Scene.Path
	useVsync2 := false

	if videoFrameRate <= 0.01 {
		logger.Errorf("[generator] Video framerate very low/high (%f) most likely vfr so using -vsync 2", videoFrameRate)
		useVsync2 = true
	}

	if err := t.generator.PreviewVideo(context.TODO(), videoFilename, videoDuration, videoChecksum, t.Options, false, useVsync2); err != nil {
		logger.Warnf("[generator] failed generating scene preview, trying fallback")
		if err := t.generator.PreviewVideo(context.TODO(), videoFilename, videoDuration, videoChecksum, t.Options, true, useVsync2); err != nil {
			return err
		}
	}

	return nil
}

func (t *GeneratePreviewTask) generateWebp(videoChecksum string) error {
	videoFilename := t.Scene.Path
	return t.generator.PreviewWebp(context.TODO(), videoFilename, videoChecksum)
}

func (t *GeneratePreviewTask) required() bool {
	return t.videoPreviewRequired() || t.imagePreviewRequired()
}

func (t *GeneratePreviewTask) videoPreviewRequired() bool {
	if t.Scene.Path == "" {
		return false
	}

	if t.Overwrite {
		return true
	}

	sceneChecksum := t.Scene.GetHash(t.fileNamingAlgorithm)
	if sceneChecksum == "" {
		return false
	}

	if t.videoPreviewExists == nil {
		sp := instance.Paths.Scene
		videoExists := paths.ResolveGeneratedFile(sp.GetVideoPreviewPath(sceneChecksum), sp.GetLegacyVideoPreviewPath(sceneChecksum)) != ""
		t.videoPreviewExists = &videoExists
	}

	return !*t.videoPreviewExists
}

func (t *GeneratePreviewTask) imagePreviewRequired() bool {
	if !t.ImagePreview {
		return false
	}

	if t.Scene.Path == "" {
		return false
	}

	if t.Overwrite {
		return true
	}

	sceneChecksum := t.Scene.GetHash(t.fileNamingAlgorithm)
	if sceneChecksum == "" {
		return false
	}

	if t.imagePreviewExists == nil {
		sp := instance.Paths.Scene
		imageExists := paths.ResolveGeneratedFile(sp.GetWebpPreviewPath(sceneChecksum), sp.GetLegacyWebpPreviewPath(sceneChecksum)) != ""
		t.imagePreviewExists = &imageExists
	}

	return !*t.imagePreviewExists
}
