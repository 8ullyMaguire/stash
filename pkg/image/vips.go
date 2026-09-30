// stash#5850 -- thumbnails are encoded as JPEG, which has no alpha channel, so
// line art with transparency gets an opaque background and comes out black.
//
// The bug is in TWO places and they are independent, so fixing one leaves the
// other serving the same defect:
//   1. the vips encoder, which hardcodes ".jpg[Q=70,strip]" in both the stdin
//      and the file-path variants
//   2. the ffmpeg fallback in pkg/image/thumbnail.go, which hardcodes
//      ImageFormatJpeg -- and the transcoder separately hardcodes VideoCodecMJpeg
//
// and in a third place that is a consequence rather than an encode:
//   3. the generated file is named "%s_%d.jpg", so the response Content-Type is
//      derived from the extension by http.ServeFile. A webp payload written
//      under a .jpg name is still served as image/jpeg.
//
// Measured on the reporter's case (8x8 PNG, transparent background, opaque red
// diagonal), via the real vips binary:
//   .jpg  -> 3 bands, transparent pixel reads "56 0 0"     (opaque; the black box)
//   .webp -> 4 bands, transparent pixel reads "211 20 20 0" (alpha preserved)

package image

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/stashapp/stash/pkg/exec"
	"github.com/stashapp/stash/pkg/logger"
)

// thumbnailOutputSuffix is the vips output specifier for thumbnails.
//
// WebP rather than PNG, and the reason is size, not capability: PNG is lossless
// so a photographic source grows, while this is a 320px thumbnail that is
// re-encoded on every change. WebP carries the alpha channel, which is the whole
// point of the fix, and encodes lossy at Q=70 to match what the .jpg path was
// doing.
//
// Q=70 is kept deliberately. It is the quality the JPEG thumbnails were
// generated at, so this is a format change and not a quality change; a reviewer
// comparing before and after should not also be comparing compression levels.
const thumbnailOutputSuffix = ".webp[Q=70,strip]"

type vipsEncoder string

func (e *vipsEncoder) ImageThumbnail(image *bytes.Buffer, maxSize int) ([]byte, error) {
	args := []string{
		"thumbnail_source",
		"[descriptor=0]",
		thumbnailOutputSuffix,
		fmt.Sprint(maxSize),
		"--size", "down",
	}
	data, err := e.run(args, image)

	return []byte(data), err
}

// ImageThumbnailPath generates a thumbnail from a file path instead of stdin.
// This is required for formats like AVIF that need random file access (seeking)
// which stdin cannot provide.
func (e *vipsEncoder) ImageThumbnailPath(path string, maxSize int) ([]byte, error) {
	// vips thumbnail syntax: thumbnail input output width [options]
	// Using an output ending in "]" writes to stdout.
	args := []string{
		"thumbnail",
		path,
		thumbnailOutputSuffix,
		fmt.Sprint(maxSize),
		"--size", "down",
	}

	cmd := exec.Command(string(*e), args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	if err := cmd.Wait(); err != nil {
		logger.Errorf("image encoder error when running command <%s>: %s", strings.Join(cmd.Args, " "), stderr.String())
		return nil, err
	}

	return stdout.Bytes(), nil
}

func (e *vipsEncoder) run(args []string, stdin *bytes.Buffer) (string, error) {
	cmd := exec.Command(string(*e), args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = stdin

	if err := cmd.Start(); err != nil {
		return "", err
	}

	err := cmd.Wait()

	if err != nil {
		// error message should be in the stderr stream
		logger.Errorf("image encoder error when running command <%s>: %s", strings.Join(cmd.Args, " "), stderr.String())
		return stdout.String(), err
	}

	return stdout.String(), nil
}
