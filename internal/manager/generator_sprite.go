package manager

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"

	"github.com/disintegration/imaging"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/scene/generate"
)

type SpriteGenerator struct {
	Info *generatorInfo

	VideoChecksum   string
	ImageOutputPath string
	VTTOutputPath   string
	Config          SpriteGeneratorConfig
	// Window is the scene's window (#3530). The zero value reproduces pre-#3530 behaviour
	// exactly, so every unranged scene's sprite is unchanged.
	Window generate.SceneWindow
	// Plan is the whole grid decision: which time each tile is sampled at, which frame it
	// corresponds to under frame seeking, and how the VTT cues are spaced. Both loops read
	// positions from it rather than computing any, because the two loops and the VTT writer must
	// agree and agreement between three call sites is not something arithmetic guarantees.
	Plan *generate.SpritePlan

	SlowSeek bool // use alternate seek function, very slow!

	Overwrite bool

	g *generate.Generator
}

// SpriteGeneratorConfig holds configuration for the SpriteGenerator
type SpriteGeneratorConfig struct {
	// MinimumSprites is the minimum number of sprites to generate, even if the video duration is short
	// SpriteInterval will be adjusted accordingly to ensure at least this many sprites are generated.
	// A value of 0 means no minimum, and the generator will use the provided SpriteInterval or
	// calculate it based on the video duration and MaximumSprites
	MinimumSprites int

	// MaximumSprites is the maximum number of sprites to generate, even if the video duration is long
	// SpriteInterval will be adjusted accordingly to ensure no more than this many sprites are generated
	// A value of 0 means no maximum, and the generator will use the provided SpriteInterval or
	// calculate it based on the video duration and MinimumSprites
	MaximumSprites int

	// SpriteInterval is the default interval in seconds between each sprite.
	// If MinimumSprites or MaximumSprites are set, this value will be adjusted accordingly
	// to ensure the desired number of sprites are generated
	// A value of 0 means the generator will calculate the interval based on the video duration and
	// the provided MinimumSprites and MaximumSprites
	SpriteInterval float64

	// SpriteSize is the size in pixels of the longest dimension of each sprite image.
	// The other dimension will be automatically calculated to maintain the aspect ratio of the video
	SpriteSize int
}

const (
	// DefaultSpriteAmount is the default number of sprites to generate if no configuration is provided
	// This corresponds to the legacy behavior of the generator, which generates 81 sprites at equal
	// intervals across the video duration
	DefaultSpriteAmount = 81

	// DefaultSpriteSize is the default size in pixels of the longest dimension of each sprite image
	// if no configuration is provided. This corresponds to the legacy behavior of the generator.
	DefaultSpriteSize = 160
)

var DefaultSpriteGeneratorConfig = SpriteGeneratorConfig{
	MinimumSprites: DefaultSpriteAmount,
	MaximumSprites: DefaultSpriteAmount,
	SpriteInterval: 0,
	SpriteSize:     DefaultSpriteSize,
}

// NewSpriteGenerator creates a new SpriteGenerator for the given video file and configuration
// It calculates the appropriate sprite interval and count based on the video duration and the provided configuration
//
// #3530 - window is the SCENE's window, not a property of the file: it decides how long the grid is,
// where it starts, and whether tiles are sought by frame at all. The zero SceneWindow reduces every
// quantity below to the pre-#3530 expression, so every unranged scene's sprite is unchanged.
func NewSpriteGenerator(videoFile ffmpeg.VideoFile, videoChecksum string, imageOutputPath string, vttOutputPath string, config SpriteGeneratorConfig, window generate.SceneWindow) (*SpriteGenerator, error) {
	exists, err := fsutil.FileExists(videoFile.Path)
	if !exists {
		return nil, err
	}

	// #3530 - the length the grid spans is the WINDOW's, not the file's. A 300s window of a 7200s
	// file is tiled with 81 tiles 3.7s apart, not 81 tiles 89s apart across two hours.
	//
	// The validity check stays on the FILE's duration: it asks "can ffmpeg read this stream at
	// all", and a window cannot change the answer. A degenerate WINDOW (start past the end) is a
	// data error, and it is caught below by the plan's chunk-count clamp rather than by refusing
	// to generate -- a scene with a bad range should still get whatever sprite it can.
	spriteDuration := window.Length(videoFile.VideoStreamDuration)

	if videoFile.VideoStreamDuration <= 0 {
		s := fmt.Sprintf("video %s: duration(%.3f)/frame count(%d) invalid, skipping sprite creation", videoFile.Path, videoFile.VideoStreamDuration, videoFile.FrameCount)
		return nil, errors.New(s)
	}

	config.SpriteInterval = calculateSpriteInterval(spriteDuration, config)
	chunkCount := int(math.Ceil(spriteDuration / config.SpriteInterval))

	// adjust the chunk count to the next highest perfect square, to ensure the sprite image
	// is completely filled (no empty space in the grid) and the grid is as square as possible (minimizing the number of rows/columns)
	gridSize := generate.GetSpriteGridSize(chunkCount)
	newChunkCount := gridSize * gridSize

	if newChunkCount != chunkCount {
		logger.Debugf("[generator] adjusting chunk count from %d to %d to fit a %dx%d grid", chunkCount, newChunkCount, gridSize, gridSize)
		chunkCount = newChunkCount
	}

	if config.SpriteSize <= 0 {
		config.SpriteSize = DefaultSpriteSize
	}

	// generator.configure() runs FIRST, before the slow-seek decision.
	//
	// #3530 - the order was the reverse until a wiring test caught the consequence: the decision
	// was made from videoFile.FrameRate (the PROBE's figure, 0 when the probe could not determine
	// it) while the plan was built from generator.FrameRate (calculateFrameRate's RESOLVED figure).
	// Two rates for one decision, and the windowed arm reads the rate directly -- so a windowed
	// short file whose rate the probe could not read was judged to have no frames and wrongly
	// refused frame seeking.
	//
	// Moving configure() up is safe, and the reason is measurable: calculateFrameRate reads
	// videoStream.NbFrames and VideoStreamDuration, never videoFile.FrameCount. The frame RECOUNT
	// below changes FrameCount and nothing configure() computes, so its position relative to
	// configure() does not matter -- what matters is that it happens BEFORE the plan is built,
	// because FileFrames comes from it.
	generator, err := newGeneratorInfo(videoFile)
	if err != nil {
		return nil, err
	}
	generator.ChunkCount = chunkCount
	if err := generator.configure(); err != nil {
		return nil, err
	}

	// For files with small duration / low frame count  try to seek using frame number intead of seconds
	//
	// #3530 - a WINDOWED scene decides this from the window's length and frame count, not the
	// file's, and the file-level condition must NOT run for it. That condition is wrong for a
	// window in both directions: a 2-hour file passes "not too short" however short its window is,
	// and a 90-frame file passes `FrameCount <= chunkCount` even when the window holds 40 frames
	// and the grid wants 81 -- which produces a grid of duplicate frames, because
	// GetSpriteGridSize rounds the chunk count up past the window's frame count.
	//
	// So the two branches are exclusive, and both are given generator.FrameRate -- the same figure
	// the plan below uses, which is the point.
	planOptions := generate.SpritePlanOptions{
		Window:       window,
		FileDuration: videoFile.VideoStreamDuration,
		FrameRate:    generator.FrameRate,
		FileFrames:   videoFile.FrameCount,
		NthFrame:     generator.NthFrame,
		ChunkCount:   chunkCount,
	}

	slowSeek := false
	if window.Set {
		slowSeek = generate.SpriteNeedsFrameSeek(planOptions)
	} else if videoFile.VideoStreamDuration < 5 || (0 < videoFile.FrameCount && videoFile.FrameCount <= int64(chunkCount)) { // some files can have FrameCount == 0, only use SlowSeek  if duration < 5
		if videoFile.VideoStreamDuration <= 0 {
			s := fmt.Sprintf("video %s: duration(%.3f)/frame count(%d) invalid, skipping sprite creation", videoFile.Path, videoFile.VideoStreamDuration, videoFile.FrameCount)
			return nil, errors.New(s)
		}
		logger.Warnf("[generator] video %s too short (%.3fs, %d frames), using frame seeking", videoFile.Path, videoFile.VideoStreamDuration, videoFile.FrameCount)
		slowSeek = true
		// do an actual frame count of the file ( number of frames = read frames)
		ffprobe := GetInstance().FFProbe
		fc, err := ffprobe.GetReadFrameCount(videoFile.Path)
		if err == nil {
			if fc != videoFile.FrameCount {
				logger.Warnf("[generator] updating framecount (%d) for %s with read frames count (%d)", videoFile.FrameCount, videoFile.Path, fc)
				videoFile.FrameCount = fc
			}
		}
	}

	// #3530 - the plan is built LAST, after the recount above, because FileFrames comes from the
	// (possibly updated) frame count and NthFrame from configure(). Built before either, the
	// unranged cue spacing reads 0 and every cue is written at 0.000.
	planOptions.FileFrames = videoFile.FrameCount
	plan := generate.NewSpritePlan(planOptions)

	return &SpriteGenerator{
		Info:            generator,
		VideoChecksum:   videoChecksum,
		ImageOutputPath: imageOutputPath,
		VTTOutputPath:   vttOutputPath,
		Config:          config,
		Window:          window,
		Plan:            &plan,
		SlowSeek:        slowSeek,
		g: &generate.Generator{
			Encoder:      instance.FFMpeg,
			FFProbe:      instance.FFProbe,
			FFMpegConfig: instance.Config,
			LockManager:  instance.ReadLockManager,
			ScenePaths:   instance.Paths.Scene,
		},
	}, nil
}

// calculateSpriteInterval picks the seconds between tiles, given the LENGTH being tiled.
//
// #3530 - the argument is a duration, named for what it now is. It used to take the whole
// ffmpeg.VideoFile only to read VideoStreamDuration, and a windowed caller passing a file would
// have tiled two hours silently: the type made the mistake invisible, which is the same reason
// the plan is a value rather than a pile of arguments.
func calculateSpriteInterval(videoDuration float64, config SpriteGeneratorConfig) float64 {
	// If a custom sprite interval is provided, start with that
	spriteInterval := config.SpriteInterval

	// If no custom interval is provided, calculate the interval based on the
	// video duration and minimum sprite count
	if spriteInterval <= 0 {
		minSprites := config.MinimumSprites
		if minSprites <= 0 {
			panic("invalid configuration: MinimumSprites must be greater than 0 if SpriteInterval is not set")
		}

		logger.Debugf("[generator] calculating sprite interval for video duration %.3fs with minimum sprites %d", videoDuration, minSprites)
		return videoDuration / float64(minSprites)
	}

	// Calculate the number of sprites that would be generated with the provided interval
	spriteCount := int(math.Ceil(videoDuration / spriteInterval))

	// If the calculated sprite count is greater than the maximum, adjust the interval to meet the maximum
	if config.MaximumSprites > 0 && spriteCount > int(config.MaximumSprites) {
		spriteInterval = videoDuration / float64(config.MaximumSprites)
		logger.Debugf("[generator] provided sprite interval %.1fs results in %d sprites, which exceeds the maximum of %d, adjusting interval to %.1fs", config.SpriteInterval, spriteCount, config.MaximumSprites, spriteInterval)
	}

	// If the calculated sprite count is less than the minimum, adjust the interval to meet the minimum
	if config.MinimumSprites > 0 && spriteCount < int(config.MinimumSprites) {
		spriteInterval = videoDuration / float64(config.MinimumSprites)
		logger.Debugf("[generator] provided sprite interval %.1fs results in %d sprites, which is less than the minimum of %d, adjusting interval to %.1fs", config.SpriteInterval, spriteCount, config.MinimumSprites, spriteInterval)
	}

	return spriteInterval
}

func (g *SpriteGenerator) Generate() error {
	if err := g.generateSpriteImage(); err != nil {
		return err
	}
	if err := g.generateSpriteVTT(); err != nil {
		return err
	}
	return nil
}

func (g *SpriteGenerator) generateSpriteImage() error {
	if !g.Overwrite && g.imageExists() {
		return nil
	}

	var images []image.Image

	isPortrait := g.Info.VideoFile.Height > g.Info.VideoFile.Width

	if !g.SlowSeek {
		logger.Infof("[generator] generating sprite image for %s", g.Info.VideoFile.Path)
		// generate `ChunkCount` thumbnails
		//
		// #3530 - the time comes from the PLAN, which anchors it on the window's start and spaces
		// it by the window's length. For an unranged scene the plan reduces to
		// i*VideoStreamDuration/ChunkCount, which is the expression this loop used inline.
		for i := 0; i < g.Info.ChunkCount; i++ {
			time := g.Plan.Time(i)
			img, err := g.g.SpriteScreenshot(context.TODO(), g.Info.VideoFile.Path, time, g.Config.SpriteSize, isPortrait)
			if err != nil {
				return err
			}
			images = append(images, img)
		}
	} else {
		logger.Infof("[generator] generating sprite image for %s (%d frames)", g.Info.VideoFile.Path, g.Info.VideoFile.FrameCount)

		for i := 0; i < g.Info.ChunkCount; i++ {
			// #3530 - the frame comes from the PLAN. A frame index is ABSOLUTE, so a window has to
			// be converted through the frame rate; the plan clamps it to the window's last frame,
			// which an inline `i*stepFrame` could not do and which a window narrower than the grid
			// makes reachable.
			frame := g.Plan.Frame(i)
			if frame >= math.MaxInt || frame <= math.MinInt {
				return errors.New("invalid frame number conversion")
			}

			img, err := g.g.SpriteScreenshotSlow(context.TODO(), g.Info.VideoFile.Path, int(frame), g.Config.SpriteSize)
			if err != nil {
				return err
			}
			images = append(images, img)
		}

	}

	if len(images) == 0 {
		return fmt.Errorf("images slice is empty, failed to generate sprite images for %s", g.Info.VideoFile.Path)
	}

	return imaging.Save(g.g.CombineSpriteImages(images), g.ImageOutputPath)
}

func (g *SpriteGenerator) generateSpriteVTT() error {
	if !g.Overwrite && g.vttExists() {
		return nil
	}
	logger.Infof("[generator] generating sprite vtt for %s", g.Info.VideoFile.Path)

	// #3530 - the cue spacing comes from the PLAN. Both unranged branches are upstream's own,
	// moved verbatim: NthFrame/FrameRate on the fast path, and (FrameCount-1)/ChunkCount/FrameRate
	// on the slow one (which exists because a file with fewer frames than tiles has NthFrame == 0
	// and would otherwise write every cue at 0.000).
	//
	// A windowed scene's spacing is the WINDOW's step, not the file's. The cues are a CONTRACT with
	// the player -- vtt-thumbnails.ts matches a cue against percent*player.duration(), so they are
	// relative to the media element's own timeline -- which is also what makes them agree with the
	// grid: tile i is at start+i*step and its cue covers [i*step, (i+1)*step) of the windowed stream.
	stepSize := g.Plan.VttStep()

	return g.g.SpriteVTT(context.TODO(), g.VTTOutputPath, g.ImageOutputPath, stepSize, g.Info.ChunkCount)
}

func (g *SpriteGenerator) imageExists() bool {
	exists, _ := fsutil.FileExists(g.ImageOutputPath)
	return exists
}

func (g *SpriteGenerator) vttExists() bool {
	exists, _ := fsutil.FileExists(g.VTTOutputPath)
	return exists
}
