package handoff

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stashapp/stash-plugin-p2pdownloader/internal/library"
)

// fakeTransfer is a Transfer with a scripted answer.
type fakeTransfer struct {
	files []string
	err   error
	calls int
}

// Calls reports how many times the transfer was asked. Exists so a mutation can
// make the hand-off skip its work on a REPEAT, which is a different bug from
// skipping it on the first run and needs a different probe to reach.
func (f *fakeTransfer) Calls() int { return f.calls }

func (f *fakeTransfer) Files() ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.files, nil
}

// countingHost wraps a library.Host and records the paths it was asked about, so
// a test can assert what the hand-off sent rather than only what came back.
type countingHost struct {
	inner library.Host

	mu     sync.Mutex
	scans  [][]string
	jobs   int
	scenes int
}

func (c *countingHost) Scan(ctx context.Context, paths []string) (string, error) {
	c.mu.Lock()
	c.scans = append(c.scans, paths)
	c.mu.Unlock()
	return "1", nil
}

func (c *countingHost) SceneForPath(ctx context.Context, path string) (*library.Scene, error) {
	c.mu.Lock()
	c.scenes++
	c.mu.Unlock()
	return &library.Scene{ID: "s-" + path, Path: path}, nil
}

func (c *countingHost) JobStatus(ctx context.Context, jobID string) (library.Job, error) {
	return library.Job{ID: jobID, Status: library.JobFinished}, nil
}

// fast returns an Integrator that does not spend real seconds.
func fast(h library.Host) *library.Integrator {
	i := library.NewIntegrator(h)
	i.PollInterval = time.Millisecond
	i.Timeout = 500 * time.Millisecond
	return i
}

// TestACompletedTransferIsScannedAndLinked is the positive path end to end
// through the hand-off.
func TestACompletedTransferIsScannedAndLinked(t *testing.T) {
	host := &countingHost{}
	h := &Handoff{Library: fast(host)}
	tr := &fakeTransfer{files: []string{"/library/Scene (2019).mp4"}}

	res, err := h.Run(context.Background(), tr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Linked()) != 1 {
		t.Fatalf("linked %v, expected one path. The host is asked to scan "+
			"and the host's matcher links; this is the whole of step 5.5",
			res.Linked())
	}
	if res.Linked()[0] != "/library/Scene (2019).mp4" {
		t.Errorf("linked %q, expected the completed file", res.Linked()[0])
	}
}

// TestEveryFileIsHandedOffAndOneBadOneDoesNotDiscardTheRest is the
// do-not-stop-at-the-first-error property.
//
// A release directory holds a video, three subtitles and a cover. The host links
// the video and correctly declines the rest — and a hand-off that returned on
// the first error would report the whole release as failed and make the
// operator re-run the job until it got past whichever file the host disliked.
func TestEveryFileIsHandedOffAndOneBadOneDoesNotDiscardTheRest(t *testing.T) {
	// A host that links the video and produces no scene for the rest, because
	// it is scanning a directory that contains both.
	host := &mixedHost{linked: map[string]bool{"/library/Scene (2019).mp4": true}}
	h := &Handoff{Library: fast(host)}
	tr := &fakeTransfer{files: []string{
		"/library/Scene (2019).mp4",
		"/library/Scene (2019).srt",
		"/library/Scene (2019).nfo",
		"/library/cover.jpg",
	}}

	res, err := h.Run(context.Background(), tr)
	if err != nil {
		t.Fatalf("Run returned the error %v. One unlinked file is a "+
			"correct outcome, not a failure of the hand-off", err)
	}
	if len(res.Outcomes) != 4 {
		t.Fatalf("got %d outcome(s) for 4 files. Every file gets an outcome; "+
			"collapsing them loses either the link or the refusals",
			len(res.Outcomes))
	}
	if len(res.Linked()) != 1 {
		t.Errorf("linked %v, expected only the video. The host declines a "+
			"subtitle and that is the host being right", res.Linked())
	}
	for _, o := range res.Outcomes {
		if o.State == library.StateScannedNoScene && o.Path == "/library/Scene (2019).mp4" {
			t.Error("the video was reported as scanned-with-no-scene, which " +
				"means the hand-off lost the one link that existed")
		}
	}
}

// mixedHost links a fixed set of paths and produces no scene for the rest.
type mixedHost struct {
	linked map[string]bool
	scans  []string
}

func (m *mixedHost) Scan(ctx context.Context, paths []string) (string, error) {
	m.scans = append(m.scans, paths...)
	return "1", nil
}

func (m *mixedHost) SceneForPath(ctx context.Context, path string) (*library.Scene, error) {
	if m.linked[path] {
		return &library.Scene{ID: "s1", Path: path}, nil
	}
	return nil, nil
}

func (m *mixedHost) JobStatus(ctx context.Context, jobID string) (library.Job, error) {
	return library.Job{ID: jobID, Status: library.JobFinished}, nil
}

// TestAFailureIsReportedButThePathsAreStillReturned, because a caller that
// loses the paths cannot tell the operator where the download went.
func TestAFailureIsReportedButThePathsAreStillReturned(t *testing.T) {
	host := &mixedHost{linked: nil} // nothing links
	h := &Handoff{Library: fast(host)}
	// A relative path is refused by library.CheckPath, so the outcome is a
	// failure — and the file is still on disk.
	tr := &fakeTransfer{files: []string{"relative/Scene.mp4"}}

	res, err := h.Run(context.Background(), tr)
	if err == nil {
		t.Fatal("Run returned no error for a file the library refused. The " +
			"operator's next question is why, and a silent result has no answer")
	}
	if len(res.Files) != 1 {
		t.Errorf("the result carries %d path(s), expected 1. A caller that "+
			"loses the paths cannot tell the operator where the download went",
			len(res.Files))
	}
}

// TestATransferWithNoFilesIsAnErrorRatherThanAnEmptySuccess is the contradiction
// case.
//
// "Finished and produced nothing" means the transfer reported completion without
// writing anything. Returning an empty result would report a download that did
// nothing as one that worked, which is the most expensive kind of quiet.
func TestATransferWithNoFilesIsAnErrorRatherThanAnEmptySuccess(t *testing.T) {
	h := &Handoff{Library: fast(&countingHost{})}
	tr := &fakeTransfer{files: nil}

	res, err := h.Run(context.Background(), tr)
	if err == nil {
		t.Fatalf("Run returned no error and %d outcome(s). A completed "+
			"transfer that wrote nothing is a contradiction, and reporting it "+
			"as success reports a download that did nothing as one that worked",
			len(res.Outcomes))
	}
	if len(res.Outcomes) != 0 {
		t.Errorf("got %d outcome(s) from a transfer with no files", len(res.Outcomes))
	}
}

// TestATransferThatCannotReportItsFilesIsNotScanned, so a transfer that failed
// to enumerate does not turn into a scan of nothing.
func TestATransferThatCannotReportItsFilesIsNotScanned(t *testing.T) {
	host := &countingHost{}
	h := &Handoff{Library: fast(host)}
	tr := &fakeTransfer{err: errors.New("the piece hash did not verify")}

	if _, err := h.Run(context.Background(), tr); err == nil {
		t.Fatal("Run returned no error for a transfer that could not report " +
			"its files")
	}
	if len(host.scans) != 0 {
		t.Errorf("the host was asked to scan %d time(s) after the transfer "+
			"failed to report its files. There is nothing known to scan",
			len(host.scans))
	}
}

// TestAHandOffWithNothingToHandOffIsAnError, so a nil transfer is not a
// successful no-op.
func TestAHandOffWithNothingToHandOffIsAnError(t *testing.T) {
	h := &Handoff{Library: fast(&countingHost{})}
	res, err := h.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("Run(nil) returned no error")
	}
	if len(res.Outcomes) != 0 {
		t.Errorf("got %d outcome(s) from a nil transfer", len(res.Outcomes))
	}
}

// TestTheHandoffSendsAbsolutePathsAndNeverAnEmptyList ties the two packages'
// central properties together.
//
// This is the seam between `internal/torrent`'s contract (absolute paths) and
// `internal/library`'s requirement (a scan with no paths is a full-library
// scan). A hand-off that dropped a path on the floor, or sent a slice that
// arrived empty, would trigger a full library reindex from a background
// download task.
func TestTheHandoffSendsAbsolutePathsAndNeverAnEmptyList(t *testing.T) {
	host := &countingHost{}
	h := &Handoff{Library: fast(host)}
	tr := &fakeTransfer{files: []string{"/library/a.mp4", "/library/b.mp4"}}

	if _, err := h.Run(context.Background(), tr); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(host.scans) != 2 {
		t.Fatalf("the host was asked to scan %d time(s), expected 2 — one per "+
			"file", len(host.scans))
	}
	for i, paths := range host.scans {
		if len(paths) != 1 {
			t.Errorf("scan %d carried %d path(s), expected 1", i, len(paths))
		}
		for _, p := range paths {
			if p == "" {
				t.Error("a scan was sent with an empty path. An empty path list " +
					"means \"scan every configured library\"")
			}
			if !strings.HasPrefix(p, "/") {
				t.Errorf("the scan was sent the relative path %q. A relative "+
					"path is resolved against the host's working directory", p)
			}
		}
	}
}

// TestAZeroHandoffBehavesLikeAConstructedOne is the nil-Library property.
//
// A zero `Handoff` is a reasonable thing to write in a test, and it must not
// panic. It cannot succeed — there is no host — but it must fail through the
// normal path rather than by dereferencing nil.
func TestAZeroHandoffBehavesLikeAConstructedOne(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a zero Handoff panicked: %v. A value used directly in a "+
				"test should be in the state production puts it in", r)
		}
	}()
	h := &Handoff{}
	tr := &fakeTransfer{files: []string{"/library/a.mp4"}}
	_, err := h.Run(context.Background(), tr)
	if err == nil {
		t.Error("a zero Handoff reported success. It has no host, so it cannot " +
			"scan anything")
	}
}

// TestTheFirstErrorIsReturnedButNotTheOnlyOne is the multi-failure property.
//
// The function returns the FIRST error so a caller that only checks `err` still
// learns something went wrong, and carries ALL the outcomes so a caller that
// shows the operator every message has them. Returning only the first would
// make the other nine invisible; returning a joined error would make the common
// case, one failure, read as a wall of text.
func TestTheFirstErrorIsReturnedButNotTheOnlyOne(t *testing.T) {
	// Every file is refused, so every outcome is a failure.
	h := &Handoff{Library: fast(&mixedHost{})}
	tr := &fakeTransfer{files: []string{
		"rel1.mp4", "rel2.mp4", "rel3.mp4",
	}}

	res, err := h.Run(context.Background(), tr)
	if err == nil {
		t.Fatal("Run returned no error with three failures")
	}
	if len(res.Outcomes) != 3 {
		t.Errorf("got %d outcome(s), expected 3. The full list is what a "+
			"caller shows the operator", len(res.Outcomes))
	}
	failures := 0
	for _, o := range res.Outcomes {
		if o.State == library.StateFailed {
			failures++
		}
	}
	if failures != 3 {
		t.Errorf("%d of 3 outcomes are failures, expected 3", failures)
	}
}

// TestAZeroIntegratorIsNotSilentlyASuccess is the same property one layer down,
// and it is here because a nil Host is the way the previous test gets its
// failure.
func TestAZeroIntegratorIsNotSilentlyASuccess(t *testing.T) {
	i := library.NewIntegrator(nil)
	i.PollInterval = time.Millisecond
	i.Timeout = 50 * time.Millisecond

	out := i.Call(context.Background(), "/library/a.mp4")
	if out.State != library.StateFailed {
		t.Errorf("a nil host reported %q, expected %q. An integrator with no "+
			"host must fail, not report a scan that did not happen",
			out.State, library.StateFailed)
	}
	if out.Err == nil {
		t.Error("a nil host produced no error")
	}
}

// TestTheHandOffNeverReportsALinkItDidNotGetFromTheHost is the anti-invention
// property, and the reason this package exists rather than living in the
// transfer.
//
// Every outcome's scene id has to come from the host. A hand-off that filled one
// in, or reported `linked` on a path the host declined, would satisfy "scanned
// and linked" on paper while the operator's library has no such scene.
func TestTheHandOffNeverReportsALinkItDidNotGetFromTheHost(t *testing.T) {
	// The host links exactly one of the two.
	host := &mixedHost{linked: map[string]bool{"/library/good.mp4": true}}
	h := &Handoff{Library: fast(host)}
	tr := &fakeTransfer{files: []string{"/library/good.mp4", "/library/bad.mp4"}}

	res, err := h.Run(context.Background(), tr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, o := range res.Outcomes {
		if o.State == library.StateLinked && o.Path != "/library/good.mp4" {
			t.Errorf("%s reported as linked with scene %q, but the host only "+
				"ever confirmed one path. A link the host did not give is "+
				"invented", o.Path, o.SceneID)
		}
		if o.State != library.StateLinked && o.SceneID != "" {
			t.Errorf("%s is %q and carries the scene id %q. A scene id on a "+
				"non-linked outcome is a link reported that does not exist",
				o.Path, o.State, o.SceneID)
		}
	}
}

// TestTheHandoffIsIdempotentInTheSenseThatMatters is about REPEATS, not
// idempotence.
//
// A job that is re-run — the operator pressed the button again, or the host
// retried a failed task — must not report a failure the first run caused. The
// file is already on disk and already scanned, so the second scan is a no-op
// that still returns a job id, and the plugin must report what the host said
// rather than what it expected to hear.
func TestTheHandoffIsIdempotentInTheSenseThatMatters(t *testing.T) {
	host := &mixedHost{linked: map[string]bool{"/library/a.mp4": true}}
	h := &Handoff{Library: fast(host)}
	tr := &fakeTransfer{files: []string{"/library/a.mp4"}}

	first, err := h.Run(context.Background(), tr)
	if err != nil {
		t.Fatalf("the first run: %v", err)
	}
	second, err := h.Run(context.Background(), tr)
	if err != nil {
		t.Fatalf("the second run: %v", err)
	}
	if len(first.Linked()) != len(second.Linked()) {
		t.Errorf("the first run linked %d file(s) and the second %d. The host "+
			"is asked the same question both times and answers the same way; a "+
			"difference means the hand-off is caching something it should not",
			len(first.Linked()), len(second.Linked()))
	}
	if len(host.scans) != 2 {
		t.Errorf("the host was asked to scan %d time(s), expected 2. Each run "+
			"asks; a second run that skipped the scan would leave a file that "+
			"the first run failed to link unlinked forever", len(host.scans))
	}
}

// TestAContextStopEndsTheHandOffPromptly, because the host calls Stop and does
// not wait.
func TestAContextStopEndsTheHandOffPromptly(t *testing.T) {
	// A host that never answers, so only the context can end the wait.
	host := &silentHost{}
	i := library.NewIntegrator(host)
	i.PollInterval = 10 * time.Millisecond
	i.Timeout = 30 * time.Second
	h := &Handoff{Library: i}
	tr := &fakeTransfer{files: []string{"/library/a.mp4", "/library/b.mp4"}}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := h.Run(ctx, tr)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("Run returned no error after a Stop")
	}
	if elapsed > 5*time.Second {
		t.Errorf("the hand-off took %s after a Stop. The host calls Stop and "+
			"does not wait for it", elapsed)
	}
}

// silentHost never produces a scene and never finishes its job.
type silentHost struct{}

func (silentHost) Scan(ctx context.Context, paths []string) (string, error) {
	return "1", nil
}

func (silentHost) SceneForPath(ctx context.Context, path string) (*library.Scene, error) {
	return nil, nil
}

func (silentHost) JobStatus(ctx context.Context, jobID string) (library.Job, error) {
	return library.Job{ID: jobID, Status: "RUNNING"}, nil
}

// TestTheHandoffNamesTheTransferItCouldNotRead, so a transfer failure is
// diagnosable from the error alone.
func TestTheHandoffNamesTheTransferItCouldNotRead(t *testing.T) {
	h := &Handoff{Library: fast(&countingHost{})}
	tr := &fakeTransfer{err: fmt.Errorf("piece 3 hash did not verify")}

	_, err := h.Run(context.Background(), tr)
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "piece 3") {
		t.Errorf("the error %q does not carry the transfer's own reason. The "+
			"operator's question is which piece failed, and the hand-off is "+
			"the only place that answer is available", err)
	}
}
