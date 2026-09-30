package transcoder

import (
	"errors"

	"github.com/stashapp/stash/pkg/ffmpeg"
)

var ErrUnsupportedFormat = errors.New("unsupported image format")

type ImageThumbnailOptions struct {
	InputFormat   ffmpeg.ImageFormat
	OutputFormat  ffmpeg.ImageFormat
	OutputPath    string
	MaxDimensions int
	Quality       int
}

// ImageCodecFor returns the encoder to use for an output image format.
//
// This exists because the output muxer (-f) and the encoder (-vcodec) are set
// independently in ImageThumbnail, and pointing them at different things is how
// a "webp" output ends up JPEG-encoded under a webp container. Deriving the
// codec from the format keeps the two in step, which is what stash#5850 needed:
// the thumbnail muxer was switched to webp to keep the alpha channel, and the
// hardcoded mjpeg encoder would have quietly thrown the alpha away anyway.
func ImageCodecFor(f ffmpeg.ImageFormat) ffmpeg.VideoCodec {
	switch f {
	case ffmpeg.ImageFormatWebp:
		return ffmpeg.VideoCodecLibWebP
	default:
		return ffmpeg.VideoCodecMJpeg
	}
}

func ImageThumbnail(input string, options ImageThumbnailOptions) ffmpeg.Args {
	var videoFilter ffmpeg.VideoFilter
	videoFilter = videoFilter.ScaleMaxSize(options.MaxDimensions)

	var args ffmpeg.Args
	args = append(args, "-hide_banner")
	args = args.LogLevel(ffmpeg.LogLevelError)

	args = args.Overwrite().
		ImageFormat(options.InputFormat).
		Input(input).
		VideoFilter(videoFilter).
		// Follow the requested OUTPUT format, not a hardcoded default. The
		// input-side muxer is options.InputFormat; the encoder has to match what
		// we are writing.
		VideoCodec(ImageCodecFor(options.OutputFormat))

	args = append(args, "-frames:v", "1")

	if options.Quality > 0 {
		args = args.FixedQualityScaleVideo(options.Quality)
	}

	args = args.ImageFormat(ffmpeg.ImageFormatImage2Pipe).
		Output(options.OutputPath).
		ImageFormat(options.OutputFormat)

	return args
}
