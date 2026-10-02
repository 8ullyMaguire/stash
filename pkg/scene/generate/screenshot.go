package generate

import (
	"context"

	"github.com/stashapp/stash/pkg/ffmpeg/transcoder"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
)

const (
	// thumbnailWidth   = 320
	// thumbnailQuality = 5

	screenshotQuality = 2

	screenshotDurationProportion = 0.2
)

type ScreenshotOptions struct {
	// At is an EXPLICIT timestamp in FILE seconds and wins over the window. nil means
	// "pick the default", which is 20% into the scene -- see resolveScreenshotAt.
	At *float64

	// #3530 Window is the scene's window. The zero value (no window) reproduces the pre-#3530
	// behaviour exactly, so every existing scene's cover is unchanged.
	Window SceneWindow
}

func (g Generator) Screenshot(ctx context.Context, input string, videoWidth int, videoDuration float64, options ScreenshotOptions) ([]byte, error) {
	lockCtx := g.LockManager.ReadLock(ctx, input)
	defer lockCtx.Cancel()

	logger.Infof("Creating screenshot for %s", input)

	// #3530 - 20% into the SCENE, not 20% of the window's length measured from the start of the
	// FILE. For a scene at 300..600 of a 7200s file the old expression gave 60, which is 240s
	// before the scene begins.
	at := resolveScreenshotAt(options.At, options.Window, videoDuration)

	ret, err := g.generateBytes(lockCtx, g.ScenePaths, jpgPattern, g.screenshot(input, screenshotOptions{
		Time:    at,
		Quality: screenshotQuality,
		// default Width is video width
	}))
	if err != nil {
		return nil, err
	}

	return ret, nil
}

type screenshotOptions struct {
	Time    float64
	Width   int
	Quality int
}

func (g Generator) screenshot(input string, options screenshotOptions) generateFn {
	return func(lockCtx *fsutil.LockContext, tmpFn string) error {
		ssOptions := transcoder.ScreenshotOptions{
			OutputPath: tmpFn,
			OutputType: transcoder.ScreenshotOutputTypeImage2,
			Quality:    options.Quality,
			Width:      options.Width,
		}

		args := transcoder.ScreenshotTime(input, options.Time, ssOptions)
		if err := g.generate(lockCtx, args); err != nil {
			logger.Warnf("[generator] fast screenshot seek failed for %s at %.3fs, retrying with accurate seek: %v", input, options.Time, err)

			ssOptions.SlowSeek = true
			args = transcoder.ScreenshotTime(input, options.Time, ssOptions)
			return g.generate(lockCtx, args)
		}

		return nil
	}
}
