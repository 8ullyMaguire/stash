package models

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"strconv"
	"time"
)

type HashAlgorithm string

const (
	HashAlgorithmMd5 HashAlgorithm = "MD5"
	// oshash
	HashAlgorithmOshash HashAlgorithm = "OSHASH"
)

var AllHashAlgorithm = []HashAlgorithm{
	HashAlgorithmMd5,
	HashAlgorithmOshash,
}

func (e HashAlgorithm) IsValid() bool {
	switch e {
	case HashAlgorithmMd5, HashAlgorithmOshash:
		return true
	}
	return false
}

func (e HashAlgorithm) String() string {
	return string(e)
}

func (e *HashAlgorithm) UnmarshalGQL(v interface{}) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}

	*e = HashAlgorithm(str)
	if !e.IsValid() {
		return fmt.Errorf("%s is not a valid HashAlgorithm", str)
	}
	return nil
}

func (e HashAlgorithm) MarshalGQL(w io.Writer) {
	fmt.Fprint(w, strconv.Quote(e.String()))
}

// ID represents an ID of a file.
type FileID int32

func (i FileID) String() string {
	return strconv.Itoa(int(i))
}

func (i *FileID) UnmarshalGQL(v interface{}) (err error) {
	switch v := v.(type) {
	case string:
		var id int
		id, err = strconv.Atoi(v)
		*i = FileID(id)
		return err
	case int:
		*i = FileID(v)
		return nil
	default:
		return fmt.Errorf("%T is not an int", v)
	}
}

func (i FileID) MarshalGQL(w io.Writer) {
	fmt.Fprint(w, strconv.Quote(i.String()))
}

func FileIDsFromInts(ids []int) []FileID {
	ret := make([]FileID, len(ids))
	for i, id := range ids {
		ret[i] = FileID(id)
	}
	return ret
}

// DirEntry represents a file or directory in the file system.
type DirEntry struct {
	ZipFileID *FileID `json:"zip_file_id"`

	// transient - not persisted
	// only guaranteed to have id, path and basename set
	ZipFile File

	ModTime time.Time `json:"mod_time"`
}

func (e *DirEntry) info(fs FS, path string) (fs.FileInfo, error) {
	if e.ZipFile != nil {
		zipPath := e.ZipFile.Base().Path
		zfs, err := fs.OpenZip(zipPath, e.ZipFile.Base().Size)
		if err != nil {
			return nil, err
		}
		defer zfs.Close()
		fs = zfs
	}
	// else assume os file

	ret, err := fs.Lstat(path)
	return ret, err
}

// File represents a file in the file system.
type File interface {
	Base() *BaseFile
	SetFingerprints(fp Fingerprints)
	Open(fs FS) (io.ReadCloser, error)
	Clone() File
}

// BaseFile represents a file in the file system.
type BaseFile struct {
	ID FileID `json:"id"`

	DirEntry

	// resolved from parent folder and basename only - not stored in DB
	Path string `json:"path"`

	Basename       string   `json:"basename"`
	ParentFolderID FolderID `json:"parent_folder_id"`

	Fingerprints Fingerprints `json:"fingerprints"`

	Size int64 `json:"size"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SetFingerprints sets the fingerprints of the file.
// If a fingerprint of the same type already exists, it is overwritten.
func (f *BaseFile) SetFingerprints(fp Fingerprints) {
	for _, v := range fp {
		f.SetFingerprint(v)
	}
}

// SetFingerprint sets the fingerprint of the file.
// If a fingerprint of the same type already exists, it is overwritten.
func (f *BaseFile) SetFingerprint(fp Fingerprint) {
	for i, existing := range f.Fingerprints {
		if existing.Type == fp.Type {
			f.Fingerprints[i] = fp
			return
		}
	}

	f.Fingerprints = append(f.Fingerprints, fp)
}

// Base is used to fulfil the File interface.
func (f *BaseFile) Base() *BaseFile {
	return f
}

func (f *BaseFile) Open(fs FS) (io.ReadCloser, error) {
	if f.ZipFile != nil {
		zipPath := f.ZipFile.Base().Path
		zfs, err := fs.OpenZip(zipPath, f.ZipFile.Base().Size)
		if err != nil {
			return nil, err
		}

		return zfs.OpenOnly(f.Path)
	}

	return fs.Open(f.Path)
}

func (f *BaseFile) Clone() (ret File) {
	clone := *f
	ret = &clone
	return
}

func (f *BaseFile) Info(fs FS) (fs.FileInfo, error) {
	return f.info(fs, f.Path)
}

func (f *BaseFile) Serve(fs FS, w http.ResponseWriter, r *http.Request) error {
	reader, err := f.Open(fs)
	if err != nil {
		return err
	}

	defer reader.Close()

	return f.serveOpen(reader, w, r)
}

// serveOpen is Serve with the file already open, so the open can be guarded separately from the
// transfer. Split out for stash#7130: a stalled mount parks in Open, not in the copy loop, and the two
// need different treatment (see stall_guard.go -- the open can be abandoned safely, the transfer
// cannot).
func (f *BaseFile) serveOpen(reader io.ReadCloser, w http.ResponseWriter, r *http.Request) error {
	content, ok := reader.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		content = bytes.NewReader(data)
	}

	if r.URL.Query().Has("t") {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}

	// Set filename if not previously set
	if w.Header().Get("Content-Disposition") == "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`filename="%s"`, f.Basename))
	}

	http.ServeContent(w, r, f.Basename, f.ModTime, content)

	return nil
}

// VisualFile is an interface for files that have a width and height.
type VisualFile interface {
	File
	GetWidth() int
	GetHeight() int
	GetFormat() string
}

func GetMinResolution(f VisualFile) int {
	w := f.GetWidth()
	h := f.GetHeight()

	if w < h {
		return w
	}

	return h
}

// ImageFile is an extension of BaseFile to represent image files.
type ImageFile struct {
	*BaseFile
	Format string `json:"format"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func (f ImageFile) GetWidth() int {
	return f.Width
}

func (f ImageFile) GetHeight() int {
	return f.Height
}

func (f ImageFile) Megapixels() float64 {
	return float64(f.Width*f.Height) / 1e6
}

func (f ImageFile) GetFormat() string {
	return f.Format
}

func (f ImageFile) Clone() (ret File) {
	clone := f
	clone.BaseFile = f.BaseFile.Clone().(*BaseFile)
	ret = &clone
	return
}

// VideoFile is an extension of BaseFile to represent video files.
type VideoFile struct {
	*BaseFile
	Format     string  `json:"format"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Duration   float64 `json:"duration"`
	VideoCodec string  `json:"video_codec"`
	AudioCodec string  `json:"audio_codec"`
	FrameRate  float64 `json:"frame_rate"`
	BitRate    int64   `json:"bitrate"`

	// #4233 - the rotation sidecar ffprobe reports, in DEGREES. 0 means no rotation.
	//
	// Stored rather than folded into Width/Height because those are the ENCODED dimensions and are
	// used for more than orientation: the transcode scale filter, sprite sheet geometry and the
	// "does this need a proxy" decision all read them. A 1920x1080 phone video with a 90-degree
	// sidecar DISPLAYS as 1080x1920, and swapping the two on the way in would make all three of
	// those compute against a size the file does not have.
	//
	// Rotation is not normalised to 0..270: ffprobe reports counter-clockwise as negative, and
	// preserving the raw value means a rescan that sees the same sidecar agrees with the stored one.
	// Consumers that need an angle should use VideoFile.DisplayOrientation, which handles the sign.
	Rotation int `json:"rotation"`

	Interactive      bool `json:"interactive"`
	InteractiveSpeed *int `json:"interactive_speed"`

	// #3530 - the window this scene takes from this file, in seconds.
	//
	// These live on VideoFile, not on a scene-file pair type, because no such type exists in
	// the runtime model (Scene.Files is []VideoFile; the only SceneFile is the JSON EXPORT
	// shape in pkg/models/jsonschema). That is sound ONLY because pkg/sqlite's GetFiles
	// returns a FRESH copy per call — the same invariant the derived Duration below relies
	// on. One file can back several scenes with different windows, so these fields describe
	// this SCENE's use of the file and must never be cached or shared between scenes.
	//
	// nil means "no window": the scene uses the whole file.
	StartTime *float64 `json:"start_time,omitempty"`
	EndTime   *float64 `json:"end_time,omitempty"`
}

// DisplayOrientation reports whether the video should be laid out as portrait, taking the rotation
// sidecar into account. stash#4233.
//
// The stored Width/Height are the ENCODED dimensions. ffprobe reports the display dimensions
// separately when it can, and for a 1920x1080 frame carrying a 90-degree rotation the encoded pair is
// the opposite way round from what a viewer sees. Comparing the encoded pair unconditionally -- which
// is what the UI did, at ScenePlayer.tsx:604 and :997 -- therefore classifies a portrait phone video
// as landscape, and the player's own layout then disagrees with the video inside it.
//
// So this is DisplayHeight() > DisplayWidth(), and NOT the encoded Height > Width: for a quarter turn
// those two are the same comparison spelled differently, and only one of them consults the rotation.
//
// Missing or zero dimensions return false. An unknown size is not evidence of portrait, and guessing
// here would flip the layout of every scene whose metadata failed to scan.
func (v *VideoFile) DisplayOrientation() bool {
	if v == nil || v.Width <= 0 || v.Height <= 0 {
		return false
	}
	return v.DisplayHeight() > v.DisplayWidth()
}

// DisplayWidth and DisplayHeight are the dimensions a viewer actually sees, i.e. the encoded pair
// with a quarter-turn rotation applied. stash#4233 -- the pair to use for anything user-facing
// (orientation, aspect ratio, layout); the stored Width/Height stay correct for anything that has to
// describe the file itself (transcode filters, sprite geometry).
//
// Both return 0 when the size is unknown, matching DisplayOrientation rather than transposing a zero
// into the other axis. A partial row (width known, height not) would otherwise report a plausible-
// looking non-zero dimension, and an aspect-ratio calculation would divide by it.
func (v *VideoFile) DisplayWidth() int {
	if v == nil || v.Width <= 0 || v.Height <= 0 {
		return 0
	}
	rotation := ((v.Rotation % 360) + 360) % 360
	if rotation == 90 || rotation == 270 {
		return v.Height
	}
	return v.Width
}

func (v *VideoFile) DisplayHeight() int {
	if v == nil || v.Width <= 0 || v.Height <= 0 {
		return 0
	}
	rotation := ((v.Rotation % 360) + 360) % 360
	if rotation == 90 || rotation == 270 {
		return v.Width
	}
	return v.Height
}

func (f VideoFile) GetWidth() int {
	return f.Width
}

func (f VideoFile) GetHeight() int {
	return f.Height
}

func (f VideoFile) GetFormat() string {
	return f.Format
}

func (f VideoFile) Clone() (ret File) {
	clone := f
	clone.BaseFile = f.BaseFile.Clone().(*BaseFile)
	ret = &clone
	return
}

// #1572 - Inf and NaN values cause the JSON marshaller to fail
// Replace these values with 0 rather than erroring

func (f VideoFile) DurationFinite() float64 {
	ret := f.Duration
	if math.IsInf(ret, 0) || math.IsNaN(ret) {
		return 0
	}
	return ret
}

func (f VideoFile) FrameRateFinite() float64 {
	ret := f.FrameRate
	if math.IsInf(ret, 0) || math.IsNaN(ret) {
		return 0
	}
	return ret
}
