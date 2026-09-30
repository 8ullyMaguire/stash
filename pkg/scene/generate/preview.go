package generate

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/ffmpeg/transcoder"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
)

const (
	scenePreviewWidth        = 640
	scenePreviewAudioBitrate = "128k"

	scenePreviewImageFPS = 12

	minSegmentDuration = 0.75
)

type PreviewOptions struct {
	Segments        int
	SegmentDuration float64
	ExcludeStart    string
	ExcludeEnd      string

	Preset string

	Audio bool
}

func getExcludeValue(videoDuration float64, v string) float64 {
	if strings.HasSuffix(v, "%") && len(v) > 1 {
		// proportion of video duration
		v = v[0 : len(v)-1]
		prop, _ := strconv.ParseFloat(v, 64)
		return prop / 100.0 * videoDuration
	}

	prop, _ := strconv.ParseFloat(v, 64)
	return prop
}

// getStepSizeAndOffset calculates the step size for preview generation and
// the starting offset.
//
// Step size is calculated based on the duration of the video file, minus the
// excluded duration. The offset is based on the ExcludeStart. If the total
// excluded duration exceeds the duration of the video, then offset is 0, and
// the video duration is used to calculate the step size.
func (g PreviewOptions) getStepSizeAndOffset(videoDuration float64) (stepSize float64, offset float64) {
	excludeStart := getExcludeValue(videoDuration, g.ExcludeStart)
	excludeEnd := getExcludeValue(videoDuration, g.ExcludeEnd)

	duration := videoDuration
	if videoDuration > excludeStart+excludeEnd {
		duration = duration - excludeStart - excludeEnd
		offset = excludeStart
	}

	stepSize = duration / float64(g.Segments)
	return
}

func (g Generator) PreviewVideo(ctx context.Context, input string, videoDuration float64, hash string, options PreviewOptions, fallback bool, useVsync2 bool) error {
	lockCtx := g.LockManager.ReadLock(ctx, input)
	defer lockCtx.Cancel()

	output := g.ScenePaths.GetVideoPreviewPath(hash)
	if !g.Overwrite {
		if exists, _ := fsutil.FileExists(output); exists {
			return nil
		}
	}

	logger.Infof("[generator] generating video preview for %s", input)

	if err := g.generateFile(lockCtx, g.ScenePaths, mp4Pattern, output, g.previewVideo(input, videoDuration, options, fallback, useVsync2)); err != nil {
		return err
	}

	logger.Debug("created video preview: ", output)

	return nil
}

func (g *Generator) previewVideo(input string, videoDuration float64, options PreviewOptions, fallback bool, useVsync2 bool) generateFn {
	// #2496 - generate a single preview video for videos shorter than segments * segment duration
	if videoDuration < options.SegmentDuration*float64(options.Segments) {
		return g.previewVideoSingle(input, videoDuration, options, fallback, useVsync2)
	}

	return func(lockCtx *fsutil.LockContext, tmpFn string) error {
		// a list of tmp files used during the preview generation
		var tmpFiles []string

		// segments that are actually usable; tmpFiles becomes this (stash#7229)
		var usableFiles []string

		// remove tmpFiles when done
		defer func() { removeFiles(tmpFiles) }()

		stepSize, offset := options.getStepSizeAndOffset(videoDuration)

		segmentDuration := options.SegmentDuration
		// TODO - move this out into calling function
		// a very short duration can create files without a video stream
		if segmentDuration < minSegmentDuration {
			segmentDuration = minSegmentDuration
			logger.Warnf("[generator] Segment duration (%f) too short. Using %f instead.", options.SegmentDuration, minSegmentDuration)
		}

		for i := 0; i < options.Segments; i++ {
			chunkFile, err := g.tempFile(g.ScenePaths, mp4Pattern)
			if err != nil {
				return fmt.Errorf("generating video preview chunk file: %w", err)
			}

			tmpFiles = append(tmpFiles, chunkFile.Name())

			time := offset + (float64(i) * stepSize)

			chunkOptions := previewChunkOptions{
				StartTime:  time,
				Duration:   segmentDuration,
				OutputPath: chunkFile.Name(),
				Audio:      options.Audio,
				Preset:     options.Preset,
			}

			if err := g.previewVideoChunk(lockCtx, input, chunkOptions, fallback, useVsync2); err != nil {
				return err
			}

			// stash#7229. ffmpeg exits 0 for a range that contains no video frames
			// and still leaves a file behind, so a chunk that cannot go into the
			// concat is dropped here rather than being allowed to poison it.
			// Dropping it from BOTH lists matters: the concat list must not name a
			// file the reader will not find, and tmpFiles is what gets cleaned up.
			if g.segmentIsUsable(chunkFile.Name()) {
				usableFiles = append(usableFiles, chunkFile.Name())
			} else {
				logger.Warnf("[generator] Preview segment %d (%s) has no video stream, skipping it.", i, filepath.Base(chunkFile.Name()))
			}
		}
		tmpFiles = usableFiles

		// A file whose video stream starts after the container claims can have EVERY
		// segment unusable -- there are no frames at all in the requested range.
		// Producing an empty preview would look like success, so this is an error.
		if len(usableFiles) == 0 {
			return fmt.Errorf("no preview segment contained a video stream; the video stream may start after the container start time")
		}

		// generate concat file based on generated video chunks
		concatFilePath, err := g.generateConcatFile(tmpFiles)
		if concatFilePath != "" {
			tmpFiles = append(tmpFiles, concatFilePath)
		}

		if err != nil {
			return err
		}

		return g.previewVideoChunkCombine(lockCtx, concatFilePath, tmpFn)
	}
}

func (g *Generator) previewVideoSingle(input string, videoDuration float64, options PreviewOptions, fallback bool, useVsync2 bool) generateFn {
	return func(lockCtx *fsutil.LockContext, tmpFn string) error {
		chunkOptions := previewChunkOptions{
			StartTime:  0,
			Duration:   videoDuration,
			OutputPath: tmpFn,
			Audio:      options.Audio,
			Preset:     options.Preset,
		}

		return g.previewVideoChunk(lockCtx, input, chunkOptions, fallback, useVsync2)
	}
}

type previewChunkOptions struct {
	StartTime  float64
	Duration   float64
	OutputPath string
	Audio      bool
	Preset     string
}

func (g Generator) previewVideoChunk(lockCtx *fsutil.LockContext, fn string, options previewChunkOptions, fallback bool, useVsync2 bool) error {
	var videoFilter ffmpeg.VideoFilter
	videoFilter = videoFilter.ScaleWidth(scenePreviewWidth)

	var videoArgs ffmpeg.Args
	videoArgs = videoArgs.VideoFilter(videoFilter)

	videoArgs = append(videoArgs,
		"-pix_fmt", "yuv420p",
		"-profile:v", "high",
		"-level", "4.2",
		"-preset", options.Preset,
		"-crf", "21",
		"-threads", "4",
		"-strict", "-2",
	)

	if useVsync2 {
		videoArgs = append(videoArgs, "-vsync", "2")
	}

	trimOptions := transcoder.TranscodeOptions{
		OutputPath: options.OutputPath,
		StartTime:  options.StartTime,
		Duration:   options.Duration,

		XError:   !fallback,
		SlowSeek: fallback,

		VideoCodec: ffmpeg.VideoCodecLibX264,
		VideoArgs:  videoArgs,

		ExtraInputArgs:  g.FFMpegConfig.GetTranscodeInputArgs(),
		ExtraOutputArgs: g.FFMpegConfig.GetTranscodeOutputArgs(),
	}

	if options.Audio {
		var audioArgs ffmpeg.Args
		audioArgs = audioArgs.AudioBitrate(scenePreviewAudioBitrate)

		trimOptions.AudioCodec = ffmpeg.AudioCodecAAC
		trimOptions.AudioArgs = audioArgs
	}

	args := transcoder.Transcode(fn, trimOptions)

	return g.generate(lockCtx, args)
}

// segmentIsUsable reports whether a generated preview chunk can go into the concat
// list.
//
// stash#7229. A file whose container reports start_time 0 while its video stream
// starts later -- HandBrake preserving an offset from the original recording, where
// audio began first -- has no frames before the offset. Segment positions come from
// the CONTAINER duration, so the first segment is cut at t=0 and the last overruns
// the real end of the video.
//
// ffmpeg treats seeking into a frameless range as a clean no-op: it exits 0 and
// writes a small file. Measured against ffmpeg 9.0.1, the segment at t=0 came out
// as 261 bytes containing NO streams -- and it did so identically with -xerror, so
// the XError option cannot catch it, because there is no warning to escalate. That
// is also why the fallback path failed the same way: fallback only changes SlowSeek
// and drops XError.
//
// The file then poisons the concat. The demuxer takes the stream layout from the
// FIRST entry of the list, so one stream-less segment aborts the whole preview with
// "Output file does not contain any stream" even though every other segment encoded
// correctly.
//
// A size check is not sufficient -- 261 bytes is not 0 -- so the file is probed and
// required to have a video stream.
func (g Generator) segmentIsUsable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		return false
	}

	vf, err := g.FFProbe.NewVideoFile(path)
	if err != nil {
		logger.Warnf("[generator] Probing preview segment %q failed, skipping it: %v", filepath.Base(path), err)
		return false
	}

	return vf.VideoCodec != ""
}

func (g Generator) generateConcatFile(chunkFiles []string) (fn string, err error) {
	concatFile, err := g.ScenePaths.TempFile(txtPattern)
	if err != nil {
		return "", fmt.Errorf("creating concat file: %w", err)
	}
	defer concatFile.Close()

	w := bufio.NewWriter(concatFile)
	for _, f := range chunkFiles {
		// files in concat file should be relative to concat
		relFile := filepath.Base(f)
		if _, err := fmt.Fprintf(w, "file '%s'\n", relFile); err != nil {
			return concatFile.Name(), fmt.Errorf("writing concat file: %w", err)
		}
	}
	return concatFile.Name(), w.Flush()
}

func (g Generator) previewVideoChunkCombine(lockCtx *fsutil.LockContext, concatFilePath string, outputPath string) error {
	spliceOptions := transcoder.SpliceOptions{
		OutputPath: outputPath,
	}

	args := transcoder.Splice(concatFilePath, spliceOptions)

	return g.generate(lockCtx, args)
}

func removeFiles(list []string) {
	for _, f := range list {
		if err := os.Remove(f); err != nil {
			logger.Warnf("[generator] Delete error: %s", err)
		}
	}
}

// PreviewWebp generates a webp file based on the preview video input.
// TODO - this should really generate a new webp using chunks.
func (g Generator) PreviewWebp(ctx context.Context, input string, hash string) error {
	lockCtx := g.LockManager.ReadLock(ctx, input)
	defer lockCtx.Cancel()

	output := g.ScenePaths.GetWebpPreviewPath(hash)
	if !g.Overwrite {
		if exists, _ := fsutil.FileExists(output); exists {
			return nil
		}
	}

	logger.Infof("[generator] generating webp preview for %s", input)

	src := g.ScenePaths.GetVideoPreviewPath(hash)

	if err := g.generateFile(lockCtx, g.ScenePaths, webpPattern, output, g.previewVideoToImage(src)); err != nil {
		return err
	}

	logger.Debug("created video preview: ", output)

	return nil
}

func (g Generator) previewVideoToImage(input string) generateFn {
	return func(lockCtx *fsutil.LockContext, tmpFn string) error {
		var videoFilter ffmpeg.VideoFilter
		videoFilter = videoFilter.ScaleWidth(scenePreviewWidth)
		videoFilter = videoFilter.Fps(scenePreviewImageFPS)

		var videoArgs ffmpeg.Args
		videoArgs = videoArgs.VideoFilter(videoFilter)

		videoArgs = append(videoArgs,
			"-lossless", "1",
			"-q:v", "70",
			"-compression_level", "6",
			"-preset", "default",
			"-loop", "0",
			"-threads", "4",
		)

		encodeOptions := transcoder.TranscodeOptions{
			OutputPath: tmpFn,

			VideoCodec: ffmpeg.VideoCodecLibWebP,
			VideoArgs:  videoArgs,

			ExtraInputArgs:  g.FFMpegConfig.GetTranscodeInputArgs(),
			ExtraOutputArgs: g.FFMpegConfig.GetTranscodeOutputArgs(),
		}

		args := transcoder.Transcode(input, encodeOptions)

		return g.generate(lockCtx, args)
	}
}
