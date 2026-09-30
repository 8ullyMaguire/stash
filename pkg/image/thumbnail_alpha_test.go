package image

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stash#5850 -- thumbnails were encoded as JPEG, which has no alpha channel, so
// line art with transparency got an opaque background and rendered as a black
// box.
//
// The tests here drive the real encoder rather than a mock, because the defect
// is a property of the FORMAT ("does this container carry an alpha channel") and
// a mock cannot have that property. When vips is missing the format-level tests
// skip; the constant assertions do not, so something always runs.

func mustVipsPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("vips")
	if err != nil {
		t.Skip("vips not installed; skipping the encoder-level assertions")
	}
	return p
}

// --- an 8x8 RGBA PNG: transparent background, opaque red diagonal -----------
//
// The reporter's exact case -- line art with transparency -- at a size where
// "which pixel was transparent" is a fixed, readable fact rather than a
// heuristic. Built here rather than kept as a fixture so the test states the
// property it depends on: (x,y) is transparent UNLESS x==y.

func transparentDiagonalPNG(t *testing.T) []byte {
	t.Helper()

	var pixels bytes.Buffer
	for y := 0; y < 8; y++ {
		pixels.WriteByte(0) // PNG per-scanline filter byte: none
		for x := 0; x < 8; x++ {
			if x == y {
				pixels.Write([]byte{255, 0, 0, 255}) // opaque red line
			} else {
				pixels.Write([]byte{0, 0, 0, 0}) // fully transparent
			}
		}
	}
	return pngFromRGBA(t, 8, 8, pixels.Bytes())
}

func pngFromRGBA(t *testing.T, w, h int, pixels []byte) []byte {
	t.Helper()

	var out bytes.Buffer
	out.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})

	var ihdr bytes.Buffer
	binary.Write(&ihdr, binary.BigEndian, uint32(w))
	binary.Write(&ihdr, binary.BigEndian, uint32(h))
	ihdr.WriteByte(8) // bit depth
	ihdr.WriteByte(6) // colour type: truecolour with alpha
	ihdr.WriteByte(0) // compression: deflate
	ihdr.WriteByte(0) // filter method
	ihdr.WriteByte(0) // interlace: none
	writePNGChunk(t, &out, "IHDR", ihdr.Bytes())
	writePNGChunk(t, &out, "IDAT", zlibCompress(t, pixels))
	writePNGChunk(t, &out, "IEND", nil)
	return out.Bytes()
}

// zlibCompress wraps the raw scanlines in a zlib stream, which is what an IDAT
// chunk is. stdlib, so the test needs no binary fixture and cannot drift from it.
func zlibCompress(t *testing.T, raw []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, err := zw.Write(raw)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func writePNGChunk(t *testing.T, out *bytes.Buffer, kind string, data []byte) {
	t.Helper()
	body := append([]byte(kind), data...)

	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(data)))
	out.Write(l[:])
	out.Write(body)

	var c [4]byte
	binary.BigEndian.PutUint32(c[:], crc32.ChecksumIEEE(body))
	out.Write(c[:])
}

// --- reading an encoded thumbnail back, with vips ---------------------------

func bandsOf(t *testing.T, path string) int {
	t.Helper()

	// vipsheader writes the field list to stdout; the openslide load warning goes
	// to stderr, so reading stdout alone is enough.
	out, err := exec.Command("vipsheader", "-a", path).Output()
	require.NoError(t, err, "vipsheader could not read %s", path)

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "bands:") {
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "bands:")))
			require.NoError(t, err)
			return n
		}
	}
	t.Fatalf("no bands field in vipsheader output for %s:\n%s", path, out)
	return 0
}

func pixelAt(t *testing.T, path, x, y string) []int {
	t.Helper()

	out, err := exec.Command("vips", "getpoint", path, x, y).Output()
	require.NoError(t, err, "vips getpoint failed on %s", path)

	fields := strings.Fields(string(out))
	vals := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		require.NoErrorf(t, err, "unparseable getpoint field %q in %q", f, out)
		vals = append(vals, n)
	}
	return vals
}

func writeTemp(t *testing.T, data []byte, ext string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "thumb"+ext)
	require.NoError(t, os.WriteFile(p, data, 0o644))
	return p
}

// --- the format property, measured -----------------------------------------

// TestAVipsThumbnailKeepsTransparency is the regression test for the report: a
// pixel that was transparent must still be transparent. On the unfixed code the
// encoder wrote .jpg, so this reads 3 bands and fails before it reaches the alpha
// assertion.
func TestAVipsThumbnailKeepsTransparency(t *testing.T) {
	e := vipsEncoder(mustVipsPath(t))

	data, err := e.ImageThumbnail(bytes.NewBuffer(transparentDiagonalPNG(t)), 8)
	require.NoError(t, err, "thumbnail encoding failed")
	require.NotEmpty(t, data)

	p := writeTemp(t, data, ".webp")

	// 4 bands = RGB + alpha. A JPEG is 3 whatever the source had.
	assert.Equal(t, 4, bandsOf(t, p),
		"the thumbnail must carry an alpha channel; 3 bands means the transparency was dropped")

	// (0,1) is off the x==y diagonal, so it was TRANSPARENT in the source.
	px := pixelAt(t, p, "0", "1")
	require.Len(t, px, 4, "expected RGBA, got %v", px)
	assert.Equal(t, 0, px[3],
		"a pixel that was transparent must still have alpha 0, got %v -- this is the black-thumbnail bug", px)
}

// TestTheOpaquePartOfTheArtworkSurvives is the other half. A "fix" that dropped
// the entire alpha channel, or flattened the image, would satisfy the test above
// by making everything opaque. The line must still be there, and still be red.
func TestTheOpaquePartOfTheArtworkSurvives(t *testing.T) {
	e := vipsEncoder(mustVipsPath(t))

	data, err := e.ImageThumbnail(bytes.NewBuffer(transparentDiagonalPNG(t)), 8)
	require.NoError(t, err)

	p := writeTemp(t, data, ".webp")

	// (3,3) IS on the diagonal: opaque red in the source.
	px := pixelAt(t, p, "3", "3")
	require.Len(t, px, 4)
	assert.Equal(t, 255, px[3], "the artwork must stay opaque, got %v", px)
	assert.Greater(t, px[0], px[1], "the red channel must dominate, got %v", px)
}

// TestAnOpaqueSourceIsNotDamaged guards the common case. Almost every thumbnail
// has no transparency at all, and a format switch that broke those would be a far
// worse regression than the bug being fixed.
func TestAnOpaqueSourceIsNotDamaged(t *testing.T) {
	e := vipsEncoder(mustVipsPath(t))

	// Same art on an opaque white background: every alpha byte becomes 255.
	opaque := opaqueBackgroundPNG(t)

	data, err := e.ImageThumbnail(bytes.NewBuffer(opaque), 8)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	p := writeTemp(t, data, ".webp")
	bands := bandsOf(t, p)
	require.Contains(t, []int{3, 4}, bands, "unexpected band count: %d", bands)

	px := pixelAt(t, p, "3", "3")
	require.NotEmpty(t, px)
	assert.Greater(t, px[0], px[1], "the artwork must survive, got %v", px)

	if bands == 4 {
		assert.Equal(t, 255, px[3], "an opaque source must not gain transparency, got %v", px)
	}
}

// opaqueBackgroundPNG is transparentDiagonalPNG with every alpha byte set to 255.
func opaqueBackgroundPNG(t *testing.T) []byte {
	t.Helper()

	var pixels bytes.Buffer
	for y := 0; y < 8; y++ {
		pixels.WriteByte(0)
		for x := 0; x < 8; x++ {
			if x == y {
				pixels.Write([]byte{255, 0, 0, 255})
			} else {
				pixels.Write([]byte{255, 255, 255, 255}) // opaque white
			}
		}
	}
	return pngFromRGBA(t, 8, 8, pixels.Bytes())
}

// --- the requested format, which needs no external binary -------------------

// TestTheEncoderRequestsWebp pins the request itself, so the format-level tests
// are not the only thing standing between this fix and a regression on a host
// where vips is absent.
func TestTheEncoderRequestsWebp(t *testing.T) {
	assert.Equal(t, ".webp[Q=70,strip]", thumbnailOutputSuffix,
		"the vips output specifier must be webp, or the alpha channel is dropped")
}

// TestWebPIsNotJPEG is a regression guard on the REASON the extension matters.
// If these two ever became the same string, the whole fix would be a no-op that
// still looked like a fix.
func TestWebPIsNotJPEG(t *testing.T) {
	ext := strings.SplitN(thumbnailOutputSuffix, "[", 2)[0]
	assert.Equal(t, ".webp", ext)
	assert.NotEqual(t, ".jpg", ext, "a JPEG output cannot carry alpha; that is the #5850 bug in one line")
}

// TestTheQualityIsUnchanged documents the deliberate part of the change: this is
// a format swap, not a quality retune. A reviewer comparing old and new
// thumbnails should not also be comparing compression levels.
func TestTheQualityIsUnchanged(t *testing.T) {
	assert.Contains(t, thumbnailOutputSuffix, "Q=70",
		"Q=70 was the JPEG quality; keep it so the change is format-only")
}
