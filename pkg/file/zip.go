package file

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/xWTF/chardet"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/transform"
)

var (
	ErrNotReaderAt  = errors.New("invalid reader: does not implement io.ReaderAt")
	errZipFSOpenZip = errors.New("cannot open zip file inside zip file")
)

// ZipFS is a file system backed by a zip file.
type zipFS struct {
	*zip.Reader
	zipFileCloser io.Closer
	zipPath       string
}

func newZipFS(fs models.FS, path string, size int64) (*zipFS, error) {
	reader, err := fs.Open(path)
	if err != nil {
		return nil, err
	}

	asReaderAt, _ := reader.(io.ReaderAt)
	if asReaderAt == nil {
		reader.Close()
		return nil, ErrNotReaderAt
	}

	zipReader, err := zip.NewReader(asReaderAt, size)
	if err != nil {
		reader.Close()
		return nil, err
	}

	// Detect and apply non-UTF-8 encoding for filenames.
	for i, name := range decodeZipEntryNames(zipReader.File, zipReader.Comment) {
		if name != zipReader.File[i].Name {
			logger.Debugf("Decoded non-utf8 zip entry in %s: %q -> %q", path, zipReader.File[i].Name, name)
			zipReader.File[i].Name = name
		}
	}

	return &zipFS{
		Reader:        zipReader,
		zipFileCloser: reader,
		zipPath:       path,
	}, nil
}

func (f *zipFS) rel(name string) (string, error) {
	if f.zipPath == name {
		return ".", nil
	}

	relName, err := filepath.Rel(f.zipPath, name)
	if err != nil {
		// if the path is not relative to the zip path, then it's not found in the zip file,
		// so treat this as a file not found
		return "", fs.ErrNotExist
	}

	// convert relName to use slash, since zip files do so regardless
	// of os
	relName = filepath.ToSlash(relName)

	return relName, nil
}

func (f *zipFS) Stat(name string) (fs.FileInfo, error) {
	reader, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return reader.Stat()
}

func (f *zipFS) Lstat(name string) (fs.FileInfo, error) {
	return f.Stat(name)
}

func (f *zipFS) OpenZip(name string, size int64) (models.ZipFS, error) {
	return nil, errZipFSOpenZip
}

func (f *zipFS) IsPathCaseSensitive(path string) (bool, error) {
	return true, nil
}

type zipReadDirFile struct {
	fs.File
}

func (f *zipReadDirFile) ReadDir(n int) ([]fs.DirEntry, error) {
	asReadDirFile, _ := f.File.(fs.ReadDirFile)
	if asReadDirFile == nil {
		return nil, fmt.Errorf("internal error: not a ReadDirFile")
	}

	return asReadDirFile.ReadDir(n)
}

func (f *zipFS) Open(name string) (fs.ReadDirFile, error) {
	relName, err := f.rel(name)
	if err != nil {
		return nil, err
	}

	r, err := f.Reader.Open(relName)
	if err != nil {
		return nil, err
	}

	return &zipReadDirFile{
		File: r,
	}, nil
}

func (f *zipFS) Close() error {
	return f.zipFileCloser.Close()
}

// openOnly returns a ReadCloser where calling Close will close the zip fs as well.
func (f *zipFS) OpenOnly(name string) (io.ReadCloser, error) {
	r, err := f.Open(name)
	if err != nil {
		return nil, err
	}

	return &wrappedReadCloser{
		ReadCloser: r,
		outer:      f,
	}, nil
}

type wrappedReadCloser struct {
	io.ReadCloser
	outer io.Closer
}

func (f *wrappedReadCloser) Close() error {
	_ = f.ReadCloser.Close()
	return f.outer.Close()
}

// decodeZipEntryNames returns the decoded UTF-8 name for each entry in files.
// If the zip uses a non-UTF-8 encoding, chardet is used to detect and decode.
// Names are returned unchanged when detection fails or the charset is already UTF-8.
func decodeZipEntryNames(files []*zip.File, archiveComment string) []string {
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}

	var buf bytes.Buffer
	for _, f := range files {
		buf.WriteString(f.Name)
		buf.WriteString(f.Comment)
	}
	buf.WriteString(archiveComment)

	d, err := chardet.NewTextDetector().DetectBest(buf.Bytes())
	if err != nil || d == nil || d.Charset == "UTF-8" {
		return names
	}

	e, _ := charset.Lookup(d.Charset)
	if e == nil {
		return names
	}

	decoder := e.NewDecoder()
	for i, f := range files {
		if decoded, _, err := transform.String(decoder, f.Name); err == nil {
			names[i] = decoded
		}
	}
	return names
}

// RemoveEntriesFromZip rewrites the zip at zipPath to a new temporary file in the same
// directory, excluding all entries whose relative paths (using forward slashes) appear in
// entriesToRemove. Returns the path of the temporary file. The caller is responsible for
// atomically replacing the original zip with the temp file, or cleaning it up on rollback.
func RemoveEntriesFromZip(zipPath string, entriesToRemove []string) (string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("opening zip %q: %w", zipPath, err)
	}
	defer r.Close()

	exclude := make(map[string]bool, len(entriesToRemove))
	for _, e := range entriesToRemove {
		exclude[e] = true
	}

	decodedNames := decodeZipEntryNames(r.File, r.Comment)

	// Capture the original's mode BEFORE writing anything. os.CreateTemp creates
	// 0600, and the caller replaces the archive with this file, so without this
	// every rewritten zip silently becomes owner-only -- a media library served
	// by another user or process loses group/other read after one delete. The
	// stat is taken first so a failure leaves no temp file behind.
	origInfo, statErr := os.Stat(zipPath)
	if statErr != nil {
		return "", fmt.Errorf("stat zip %q: %w", zipPath, statErr)
	}

	// The temp file is created in the zip's own directory deliberately: that is
	// what makes the caller's os.Rename an atomic same-filesystem replace. The
	// cost is that a crash between here and the rename leaves a *.tmp inside the
	// user's library, where the scanner will pick it up. The failure paths below
	// clean up; a hard crash does not, and cannot without a startup sweep.
	tmpFile, err := os.CreateTemp(filepath.Dir(zipPath), "*.tmp")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}

	if err := tmpFile.Chmod(origInfo.Mode().Perm()); err != nil {
		cleanup()
		return "", fmt.Errorf("setting mode on temp file: %w", err)
	}

	w := zip.NewWriter(tmpFile)
	for i, f := range r.File {
		if exclude[filepath.ToSlash(decodedNames[i])] {
			continue
		}
		if err := w.Copy(f); err != nil {
			_ = w.Close()
			cleanup()
			return "", fmt.Errorf("copying zip entry %q: %w", f.Name, err)
		}
	}
	if err := w.SetComment(r.Comment); err != nil {
		_ = w.Close()
		cleanup()
		return "", fmt.Errorf("setting zip comment: %w", err)
	}
	if err := w.Close(); err != nil {
		cleanup()
		return "", fmt.Errorf("finalizing zip: %w", err)
	}
	// Flush to disk before the caller replaces the original. Close alone only
	// reaches the page cache, so a crash immediately after the rename can leave
	// a present-but-empty archive where the user's only copy used to be.
	if err := tmpFile.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("syncing temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("closing temp file: %w", err)
	}

	return tmpPath, nil
}
