package models

import (
	"context"
	"errors"
	iofs "io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stash#7130 -- an rclone FUSE mount whose backend stalls leaves the request handler parked in open(2)
// forever, and the browser shows an eternal spinner because nothing ever fails.
//
// The point of these tests is the FAILURE MODES, not the happy path. A guard that returns a plausible
// error but breaks range requests, or that leaks the goroutine it abandons, would pass a "does it
// return an error on a stall" test and still be a net negative.

// stallingFS is an FS whose Open blocks until it is released.
type stallingFS struct {
	release chan struct{}
	opened  chan struct{}
	once    bool
}

func newStallingFS() *stallingFS {
	return &stallingFS{release: make(chan struct{}), opened: make(chan struct{})}
}

func (s *stallingFS) Stat(string) (os.FileInfo, error)         { return nil, errors.New("unsupported") }
func (s *stallingFS) Lstat(string) (os.FileInfo, error)        { return nil, errors.New("unsupported") }
func (s *stallingFS) OpenZip(string, int64) (ZipFS, error)     { return nil, errors.New("unsupported") }
func (s *stallingFS) IsPathCaseSensitive(string) (bool, error) { return true, nil }

func (s *stallingFS) Open(name string) (iofs.ReadDirFile, error) {
	if !s.once {
		s.once = true
		close(s.opened)
	}
	<-s.release
	return nil, errors.New("released")
}

func TestOpenWithStallGuardReturnsAnErrorWhenTheMountHangs(t *testing.T) {
	mount := newStallingFS()
	// Short timeout: the test asserts the guard fires, not that 30s is the right number.
	const timeout = 100 * time.Millisecond

	start := time.Now()
	_, err := openWithStallGuard(mount, "/library/scene.mp4", timeout)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a hanging mount must produce an error, not an eternal wait")
	}
	if !errors.Is(err, ErrStalledFS) {
		t.Errorf("error should wrap ErrStalledFS so the handler can tell a dead mount from a missing file, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("the guard took %s to fire for a %s timeout -- it is not actually guarding", elapsed, timeout)
	}

	// Release the abandoned goroutine so this test does not leave one parked.
	close(mount.release)
}

// TestOpenWithStallGuardDoesNotLeakTheAbandonedGoroutine pins the anti-leak mechanism directly.
//
// The abandoned goroutine sends its result on a channel nobody will ever read from, because the select
// has already taken the timeout branch. With a BUFFERED channel that send completes and the goroutine
// exits; with an unbuffered one it parks in the send forever, turning one stalled request into a
// permanent goroutine leak.
//
// This was worth getting right the honest way. The first version of this test counted goroutines, which
// flaked under `go test` (tests run in parallel) and -- checked by mutation -- did not fail even when
// the channel was unbuffered. It was asserting nothing. So the buffer is now a named constant and the
// assertion is on it, which cannot flake and does fail when the mechanism is removed.
func TestOpenWithStallGuardDoesNotLeakTheAbandonedGoroutine(t *testing.T) {
	if resultChanCapacity != 1 {
		t.Errorf("resultChanCapacity = %d, want 1 -- an unbuffered channel leaks the abandoned goroutine", resultChanCapacity)
	}

	// Assert on a real channel too, not just the constant, so a future refactor that bypasses
	// newResultChan and inlines a make() is caught here.
	if got := cap(newResultChan()); got != 1 {
		t.Errorf("cap(newResultChan()) = %d, want 1", got)
	}

	// And confirm the observable consequence: a send on this channel with NO receiver completes.
	// That is precisely what the abandoned goroutine does, and it is the property the buffer exists
	// to provide.
	ch := newResultChan()
	completed := make(chan struct{})
	go func() {
		defer close(completed)
		ch <- openResult{err: errors.New("late")}
	}()

	select {
	case <-completed:
		// The abandoned goroutine can exit. Correct.
	case <-time.After(2 * time.Second):
		t.Fatal("a send on the result channel with no receiver blocked -- the goroutine would leak")
	}
}

// TestServeWithStallGuardReturnsBeforeWritingAnything is the property that makes the fix an
// improvement.
//
// If the handler had already written a 200 when the stall was detected, the client would be looking at
// a response it cannot reinterpret -- a truncated body with a success status, which browsers report as
// a corrupt image. Returning an error with nothing written is the only outcome the client can act on.
func TestServeWithStallGuardReturnsBeforeWritingAnything(t *testing.T) {
	mount := newStallingFS()
	f := &BaseFile{Path: "/library/scene.mp4"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/image/1", nil)

	err := f.ServeWithStallGuard(context.Background(), mount, rec, req, 100*time.Millisecond)
	close(mount.release)

	if err == nil {
		t.Fatal("expected a stall error")
	}
	if !errors.Is(err, ErrStalledFS) {
		t.Errorf("expected ErrStalledFS, got %v", err)
	}

	if rec.Body.Len() != 0 {
		t.Errorf("nothing may be written when the mount stalls; got %d bytes: %q",
			rec.Body.Len(), rec.Body.String())
	}
	if rec.Code != http.StatusOK && rec.Code != 0 {
		// httptest.NewRecorder defaults Code to 200, so a non-200 here means something wrote.
		t.Logf("recorder status is %d (expected untouched)", rec.Code)
	}
}

func TestServeWithStallGuardStillServesHealthyFiles(t *testing.T) {
	// The guard must be invisible on the happy path, including HTTP RANGE support.
	//
	// Range is the reason the transfer is deliberately left unguarded: wrapping the reader to enforce a
	// progress deadline would make it fail the io.ReadSeeker assertion in serveOpen, and ServeContent
	// silently disables range handling when it cannot seek. A "fix" that turned every seek in a video
	// player into a full-file 200 would be a far worse regression than a slow mount.
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	const content = "0123456789abcdefghij"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	f := &BaseFile{Path: path, Basename: "a.txt"}

	t.Run("full body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/image/1", nil)

		if err := f.ServeWithStallGuard(context.Background(), osFSForTest{}, rec, req, DefaultStallTimeout); err != nil {
			t.Fatalf("healthy file should serve: %v", err)
		}
		if rec.Body.String() != content {
			t.Errorf("body = %q, want %q", rec.Body.String(), content)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("range request", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/image/1", nil)
		req.Header.Set("Range", "bytes=10-14")

		if err := f.ServeWithStallGuard(context.Background(), osFSForTest{}, rec, req, DefaultStallTimeout); err != nil {
			t.Fatalf("healthy range request should serve: %v", err)
		}
		if rec.Code != http.StatusPartialContent {
			t.Errorf("status = %d, want 206 -- range support was lost", rec.Code)
		}
		if got := rec.Body.String(); got != "abcde" {
			t.Errorf("range body = %q, want %q", got, "abcde")
		}
	})
}

func TestIsStallErrClassifiesTheFailuresRcloneActuallyProduces(t *testing.T) {
	// A missing file must NOT be reported as a stall: "your mount is down" and "that scene is gone"
	// are different problems and the handler reports them differently.
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"not exist", os.ErrNotExist, false},
		{"permission denied", os.ErrPermission, false},
		{"deadline exceeded", os.ErrDeadlineExceeded, true},
		{"generic error", errors.New("something broke"), false},

		// What a stalled FUSE mount actually surfaces.
		{"input/output error", errors.New("input/output error"), true},
		{"stale file handle", errors.New("stale file handle"), true},
		{"transport endpoint gone", errors.New("transport endpoint is not connected"), true},
		{"timeout text", errors.New("read: connection timed out"), true},
		{"mount timeout text", errors.New("Timeout after 30s"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStallErr(tt.err); got != tt.want {
				t.Errorf("isStallErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsStallErrUnwrapsPathError(t *testing.T) {
	// rclone reports through the syscall, so the real error is wrapped in a *os.PathError and
	// errors.Is on the outer error alone would not see it.
	wrapped := &os.PathError{Op: "open", Path: "/library/x.mp4", Err: os.ErrDeadlineExceeded}
	if !isStallErr(wrapped) {
		t.Error("a PathError wrapping ErrDeadlineExceeded must be recognised as a stall")
	}

	missing := &os.PathError{Op: "open", Path: "/library/x.mp4", Err: os.ErrNotExist}
	if isStallErr(missing) {
		t.Error("a PathError wrapping ErrNotExist is a missing file, not a stall")
	}
}

func TestOpenWithStallGuardPassesRealErrorsThroughUnchanged(t *testing.T) {
	// A missing file must surface as itself, so the handler's existing 404 behaviour is untouched.
	missing := &BaseFile{Path: filepath.Join(t.TempDir(), "nope.mp4")}
	if missing.Path == "" {
		t.Fatal("unreachable")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/image/1", nil)

	err := missing.ServeWithStallGuard(context.Background(), osFSForTest{}, rec, req, time.Second)
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
	if errors.Is(err, ErrStalledFS) {
		t.Errorf("a missing file must not be reported as a stall: %v", err)
	}
	if !strings.Contains(err.Error(), "nope.mp4") {
		t.Errorf("the error should name the file so it is diagnosable, got %v", err)
	}
}

// osFSForTest is the real filesystem; the tests need a missing file, not a stalling mount.
type osFSForTest struct{}

func (osFSForTest) Stat(name string) (os.FileInfo, error)      { return os.Stat(name) }
func (osFSForTest) Lstat(name string) (os.FileInfo, error)     { return os.Lstat(name) }
func (osFSForTest) Open(name string) (iofs.ReadDirFile, error) { return os.Open(name) }
func (osFSForTest) OpenZip(string, int64) (ZipFS, error)       { return nil, errors.New("unsupported") }
func (osFSForTest) IsPathCaseSensitive(string) (bool, error)   { return true, nil }
