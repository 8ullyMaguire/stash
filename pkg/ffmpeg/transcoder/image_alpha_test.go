package transcoder

import (
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stash#5850 -- the thumbnail muxer was switched from JPEG to WebP to keep the
// alpha channel, but ImageThumbnail set the output muxer (-f) and the encoder
// (-vcodec) independently, and the ENCODER was hardcoded to mjpeg. Switching
// only the muxer would have written a WebP container holding JPEG data, which has
// no alpha channel -- the same black thumbnails, with the fix in the diff.
//
// These tests assert the two stay in step.

func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// lastArgValue returns the value of the LAST occurrence of a flag.
//
// Necessary because the command legitimately carries two -f flags: ffmpeg takes
// the input muxer (-f image2pipe is not it -- rather, the first -f here is the
// image2 demuxer) and the output muxer. Reading the first -f reads the wrong
// one, which is exactly the mistake this helper exists to prevent.
func lastArgValue(args []string, flag string) (string, bool) {
	var val string
	var found bool
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			val, found = args[i+1], true
		}
	}
	return val, found
}

func TestTheEncoderFollowsTheWebpOutputFormat(t *testing.T) {
	// The regression: webp out, mjpeg encode.
	args := ImageThumbnail("-", ImageThumbnailOptions{
		OutputFormat:  ffmpeg.ImageFormatWebp,
		OutputPath:    "-",
		MaxDimensions: 320,
		Quality:       5,
	}).Args()

	codec, ok := argValue(args, "-c:v")
	require.True(t, ok, "no -c:v in %v", args)
	assert.Equal(t, "libwebp", codec,
		"a webp output must be encoded with libwebp, not %q -- mjpeg drops the alpha channel", codec)

	// The OUTPUT muxer is the last -f, not the first.
	format, ok := lastArgValue(args, "-f")
	require.True(t, ok, "no -f in %v", args)
	assert.Contains(t, format, "webp", "the output muxer must be webp, got %q", format)
}

func TestTheEncoderAndTheMuxerAgree(t *testing.T) {
	// The general property, stated so a future format addition cannot break it
	// by adding a muxer without a matching codec.
	for _, tc := range []struct {
		name   string
		format ffmpeg.ImageFormat
		codec  string
	}{
		{"webp", ffmpeg.ImageFormatWebp, "libwebp"},
		{"jpeg", ffmpeg.ImageFormatJpeg, "mjpeg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := ImageThumbnail("-", ImageThumbnailOptions{
				OutputFormat:  tc.format,
				OutputPath:    "-",
				MaxDimensions: 320,
			})
			codec, ok := argValue(args.Args(), "-c:v")
			require.True(t, ok)
			assert.Equal(t, tc.codec, codec)
		})
	}
}

func TestAnUnknownOutputFormatStillFallsBackToJpeg(t *testing.T) {
	// Not a licence to emit something invalid: the default must be a working
	// format, because this function is called with a caller-supplied value.
	codec, ok := argValue(ImageThumbnail("-", ImageThumbnailOptions{
		OutputFormat:  ffmpeg.ImageFormat("not_a_real_format"),
		OutputPath:    "-",
		MaxDimensions: 320,
	}).Args(), "-c:v")
	require.True(t, ok)
	assert.Equal(t, "mjpeg", codec)
}

func TestTheScaleFilterAndFrameCountSurviveTheFormatChange(t *testing.T) {
	// A regression guard on the parts of the command the fix did not mean to
	// touch: no resize means a full-size image, and no frame cap means a video.
	args := ImageThumbnail("-", ImageThumbnailOptions{
		OutputFormat:  ffmpeg.ImageFormatWebp,
		OutputPath:    "-",
		MaxDimensions: 320,
		Quality:       5,
	}).Args()

	joined := strings.Join(args, " ")
	assert.Contains(t, joined, "scale=320:320:force_original_aspect_ratio=decrease",
		"the thumbnail must still be scaled to maxSize")
	assert.Contains(t, joined, "-frames:v 1", "still exactly one frame")
	assert.Contains(t, joined, "-q:v 5", "quality is still applied")
}
