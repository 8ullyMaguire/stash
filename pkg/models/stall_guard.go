package models

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"net/http"
	"os"
	"time"
)

// stash#7130 -- a stalled network filesystem mount must not stall the HTTP handler.
//
// THE REPORT
// ----------
// "The app hanging on RClone drive -- Loading ... forever." Media is served through a raw OS
// filesystem call -- internal/api/routes_image.go:136 calls `Base().Serve(&file.OsFS{}, w, r)`
// directly -- so opening a file inside the mount is a blocking syscall on the request path. An rclone
// FUSE mount whose backend goes away stops answering open(2), and the handler waits forever. The
// browser shows a spinner because from its side nothing failed: no response, no status, no timeout.
//
// ## WHAT THIS DOES AND DOES NOT FIX -- read this before "simplifying" it
//
// A goroutine parked in a syscall cannot be cancelled. context.Context cancels COOPERATION between
// goroutines; it does not interrupt a read(2) the kernel is never going to return from. There is no
// way to make the blocking call itself return.
//
// So the fix is NOT "add a timeout around the read." It is: **detect the stall at the one point where
// abandoning the work is still safe, and refuse to start.** That point is Open() -- before a single
// byte has been written, and before http.ServeContent has committed a status code. A goroutine
// abandoned there holds nothing but a file descriptor, which the kernel reclaims when the process
// exits.
//
// ## WHAT IS DELIBERATELY NOT FIXED
//
// A mount that stalls MID-TRANSFER is not converted into an HTTP error, and cannot be. By then
// ServeContent has written a 200 with a Content-Length, so any "error" this code produced would be a
// 500 appended to a response body the client has already begun parsing -- which browsers report as a
// corrupt image, not as an error. A truncated body, which the client sees as a network failure, is both
// truthful and recoverable; a half-written 500 is neither.
//
// What protects the SERVER from a mid-transfer stall is the connection, not this code: the write
// eventually fails or times out and the handler unwinds. What this code protects is the CLIENT, which
// would otherwise wait forever for a response that was never coming.
//
// That is a smaller claim than "fixes #7130", and it is the true one.

// DefaultStallTimeout is how long a filesystem open may take before the request is treated as stalled.
const DefaultStallTimeout = 30 * time.Second

// ErrStalledFS is returned when the filesystem does not answer within the deadline.
var ErrStalledFS = errors.New("filesystem did not respond")

// openResult is what the open goroutine sends back. Named so newResultChan can be typed, and so the
// two are obviously a pair.
type openResult struct {
	f   iofs.ReadDirFile
	err error
}

// resultChanCapacity is the buffer size of the channel the open goroutine sends on. It MUST be 1.
//
// This is the entire anti-leak mechanism, and it was invisible to the tests when it was an inline
// make(chan result, 1): an unbuffered channel leaks one goroutine per stalled request, forever, and no
// behavioural test could see it. The first attempt at catching it counted goroutines, which flaked
// under `go test` instead of informing. So the number is a named constant and the channel is built
// through one constructor, both of which a test can assert on directly.
//
// Exactly 1: there is one send and at most one receive. With a buffer, the abandoned goroutine always
// completes and exits; without one, it parks in the send because the caller has already taken the
// timeout branch.
const resultChanCapacity = 1

func newResultChan() chan openResult {
	return make(chan openResult, resultChanCapacity)
}

// openWithStallGuard opens name and fails if the filesystem does not answer in time.
//
// The returned reader is NOT wrapped: once open has succeeded the transfer proceeds unguarded, for the
// reason given above. The goroutine that performs the open is abandoned on timeout -- it holds only an
// fd at that point, and it writes to a buffered channel rather than to the ResponseWriter, so it cannot
// corrupt the response if it completes after we have given up on it.
//
// The one-byte buffered channel matters for the same reason. An unbuffered send from a late-returning
// goroutine would leak that goroutine forever, because nobody would ever receive from it.
func openWithStallGuard(filesystem FS, name string, timeout time.Duration) (iofs.ReadDirFile, error) {
	done := newResultChan()

	go func() {
		f, err := filesystem.Open(name)
		done <- openResult{f: f, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case res := <-done:
		if res.err != nil {
			// A real error (missing file, permissions) is reported as-is. Only a STALL becomes
			// ErrStalledFS, so the handler can tell "your library is unreachable" from "that file is
			// not there".
			if isStallErr(res.err) {
				return nil, fmt.Errorf("%w: %s: %v", ErrStalledFS, name, res.err)
			}
			return nil, res.err
		}
		return res.f, nil

	case <-timer.C:
		// The open is still parked in the kernel. We cannot stop it and we do not need to: this
		// goroutine holds no lock the caller needs, and the fd (if one is eventually produced) is
		// reclaimed when the process exits.
		//
		// It is deliberately NOT closed if it arrives late: the reader is only reachable from the
		// abandoned goroutine's own send, which nobody will read.
		return nil, fmt.Errorf("%w: %s did not open within %s", ErrStalledFS, name, timeout)
	}
}

// isStallErr reports whether err indicates a stalled or unreachable filesystem rather than a
// legitimate failure.
//
// rclone's FUSE layer surfaces a stalled backend as a plain error whose only signal is the text -- it
// does not reliably wrap a syscall errno that errors.Is can see. Matching on the text is normally bad
// practice; here it is the difference between reporting "your mount is down" and reporting an
// unexplained internal error, and it can only ever reclassify an error into a better-named member of
// the same class.
//
// EIO and ESTALE are included because a FUSE mount whose backend has gone away reports exactly those,
// and a bare EIO on a media path is almost never a real I/O error -- it is a dead mount.
func isStallErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}

	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		if pathErr.Err != nil && pathErr.Err.Error() == "input/output error" {
			return true
		}
		err = pathErr.Err
	}
	if err == nil {
		return false
	}

	s := err.Error()
	return containsFold(s, "timeout") || containsFold(s, "timed out") ||
		containsFold(s, "stale file handle") || containsFold(s, "transport endpoint is not connected") ||
		containsFold(s, "input/output error")
}

func containsFold(haystack, needle string) bool {
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			c := lowerASCII(haystack[i+j])
			n := lowerASCII(needle[j])
			if c != n {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// ServeWithStallGuard is BaseFile.Serve with a bounded open.
//
// Same signature and same behaviour on the happy path, including HTTP range support: the open is
// guarded, the transfer is not, and the reader is passed through untouched so the io.ReadSeeker
// assertion in Serve still succeeds. Wrapping the reader to enforce a progress deadline would silently
// disable range requests for every media file, which is a far worse regression than a slow transfer.
//
// ctx is honoured only for values already present; the deadline is the explicit timeout, because the
// caller knows how long a library mount is allowed to take and the request context often has none.
func (f *BaseFile) ServeWithStallGuard(ctx context.Context, filesystem FS, w http.ResponseWriter, r *http.Request, timeout time.Duration) error {
	_ = ctx // the deadline is timeout; see the doc comment

	reader, err := openWithStallGuard(filesystem, f.Path, timeout)
	if err != nil {
		return err
	}
	defer reader.Close()

	return f.serveOpen(reader, w, r)
}
