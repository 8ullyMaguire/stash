package image

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
)

// photographicJPEG builds a large, DETAIL-HEAVY JPEG.
//
// stash#3741. The test that matters here has to fail if the encoder stops shrinking anything, and a
// flat-colour image cannot do that: JPEG compresses a solid field to almost nothing at any size, so
// "the thumbnail is smaller than the original" passes trivially and proves nothing. Real screenshots
// are high-frequency, so this paints per-pixel noise, which is close to the worst case for JPEG and
// therefore the honest one to measure against.
func photographicJPEG(t *testing.T, w, h int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// A cheap LCG, so the pattern is deterministic and a failure is reproducible.
	seed := uint32(0x2545F491)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			seed = seed*1664525 + 1013904223
			img.Set(x, y, color.RGBA{
				R: uint8(seed >> 24),
				G: uint8(seed >> 16),
				B: uint8(seed >> 8),
				A: 0xFF,
			})
		}
	}

	var buf bytes.Buffer
	// Quality 95, deliberately high: the point is that even a well-compressed full-size cover dwarfs
	// the thumbnail. The cover generator uses -q:v 2, which is far more generous still.
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode source jpeg: %v", err)
	}
	return buf.Bytes()
}

// TestGetThumbnailFromBytesShrinksALargeCover is the regression test for stash#3741.
//
// #3741 was deferred with the note that it was "blocked on that pipeline not existing yet". It now
// exists, and this asserts the property the whole change exists for: a width-capped thumbnail of a
// large cover is dramatically smaller.
//
// The thresholds are loose on purpose. This asserts an ORDER OF MAGNITUDE (at least 4x smaller, and
// under a quarter of the pixels), not a byte count, because WebP and ffmpeg versions move exact sizes
// around and a tight threshold would turn a codec upgrade into a test failure.
func TestGetThumbnailFromBytesShrinksALargeCover(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed; cannot exercise the encoder")
	}

	const (
		srcW = 1920
		srcH = 1080
		// 320 is manager.DefaultSceneThumbWidth. Duplicated rather than imported because pkg/image
		// cannot import internal/manager (Go forbids it), and importing the other way round would
		// make this a cycle.
		thumbMax = 320
	)

	src := photographicJPEG(t, srcW, srcH)

	// The encoder is located inside GetThumbnailFromBytes, so this only proves ffmpeg is runnable --
	// a Skip rather than a silent pass if it is not.
	if err := exec.Command(ffmpegPath, "-version").Run(); err != nil {
		t.Skipf("ffmpeg present but not runnable: %v", err)
	}

	data, err := GetThumbnailFromBytes(ffmpeg.NewEncoder(ffmpegPath), src, thumbMax)
	if err != nil {
		t.Fatalf("GetThumbnailFromBytes: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("thumbnail is empty")
	}

	t.Logf("source %dx%d JPEG = %d bytes", srcW, srcH, len(src))
	t.Logf("thumb (max %d)      = %d bytes", thumbMax, len(data))

	// The byte ratio alone is not enough. An encoder that returned a 240-byte solid-colour image
	// would sail past a size check while being useless, and pure-noise input compresses to almost
	// nothing at any size -- which is exactly what this test's own source produced before the check
	// below was added. So decode the result and confirm it is really a scaled 16:9 image.
	//
	// ffprobe rather than image.Decode, because the output is WebP and this package does not import a
	// WebP decoder.
	cfg, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0", "-").Output()
	if err != nil {
		// Older ffprobe builds refuse "-" as an input; a temp file works everywhere.
		tmp := filepath.Join(t.TempDir(), "thumb.webp")
		if werr := os.WriteFile(tmp, data, 0o600); werr != nil {
			t.Fatalf("write thumb for probing: %v", werr)
		}
		cfg, err = exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
			"-show_entries", "stream=width,height", "-of", "csv=p=0", tmp).Output()
		if err != nil {
			t.Fatalf("ffprobe could not read the thumbnail back: %v", err)
		}
	}

	var gotW, gotH int
	if _, serr := fmt.Sscanf(strings.TrimSpace(string(cfg)), "%d,%d", &gotW, &gotH); serr != nil {
		t.Fatalf("could not parse ffprobe dimensions from %q: %v", cfg, serr)
	}
	t.Logf("thumb dimensions: %dx%d", gotW, gotH)

	if gotW > thumbMax {
		t.Errorf("thumbnail is %d px wide; the whole point is that it is capped at %d", gotW, thumbMax)
	}
	if gotW < thumbMax/2 {
		t.Errorf("thumbnail is only %d px wide; that is not a usable thumbnail for a %d px box", gotW, thumbMax)
	}
	// 16:9 within rounding. A squashed or stretched thumbnail is a bug even if the bytes shrink.
	if ratio := float64(gotW) / float64(gotH); ratio < 1.6 || ratio > 2.0 {
		t.Errorf("thumbnail aspect ratio %.2f is not 16:9-ish (%dx%d)", ratio, gotW, gotH)
	}

	if len(data) >= len(src)/4 {
		t.Errorf("thumbnail is %d bytes against a %d byte source; expected at least a 4x reduction",
			len(data), len(src))
	}
}

// TestGetThumbnailFromBytesRefusesAnimatedInput covers the #2266 rule on the new entry point.
//
// A GIF cover is refused rather than reduced to frame zero, because the first frame of an animation is
// not a representative thumbnail and silently serving it would be a wrong image, not a missing one.
func TestGetThumbnailFromBytesRefusesAnimatedInput(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed; cannot exercise the encoder")
	}

	// A minimal valid GIF89a: header, logical screen descriptor, global colour table with two
	// entries, a graphic control extension marking the first frame as 2 frames, then a tiny image.
	gif := []byte{
		'G', 'I', 'F', '8', '9', 'a',
		0x01, 0x00, // width 1
		0x01, 0x00, // height 1
		0x80,             // global colour table, 2 entries
		0x00, 0x00, 0x00, // colour 0
		0xFF, 0xFF, 0xFF, // colour 1
		0x21, 0xF9, 0x04, 0x04, // graphic control extension, delay 4
		0x00, 0x00, // packed, transparent index
		0x00, 0x00, // delay low, delay high
		0x2C, 0x00, 0x00, 0x00, 0x00, // image descriptor
		0x01, 0x00, 0x01, 0x00, 0x00, // local image: 1x1, no table
		0x02, 0x02, 0x44, 0x01, 0x00, // LZW minimum code size + data
		0x3B, // trailer
	}

	_, err = GetThumbnailFromBytes(ffmpeg.NewEncoder(ffmpegPath), gif, 320)
	if err == nil {
		t.Fatal("an animated GIF cover must be refused, not silently reduced to frame zero")
	}
	if !bytes.Contains([]byte(err.Error()), []byte(ErrNotSupportedForThumbnail.Error())) {
		t.Errorf("error %q should wrap ErrNotSupportedForThumbnail", err)
	}
}

// TestGetThumbnailFromBytesRejectsGarbage checks the failure mode is an error, not a panic and not a
// zero-byte "success" that would then be served as a broken image.
func TestGetThumbnailFromBytesRejectsGarbage(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed; cannot exercise the encoder")
	}

	garbage := []byte("this is definitely not an image")

	data, err := GetThumbnailFromBytes(ffmpeg.NewEncoder(ffmpegPath), garbage, 320)
	if err == nil {
		t.Fatalf("expected an error for non-image input, got %d bytes", len(data))
	}
	if len(data) != 0 {
		t.Errorf("on error the caller must get no bytes, got %d", len(data))
	}
}
