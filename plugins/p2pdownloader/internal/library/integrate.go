package library

import (
	"context"
	"fmt"
	"time"
)

// Outcome is what became of a completed download.
//
// A type rather than an error-or-nil, because there are three genuinely
// different endings and a caller has to be able to tell them apart in a task
// list:
//
//	Linked     the file is in the library and a scene points at it
//	ScannedNoScene  the host scanned it and decided it is not a scene
//	Failed     something did not work, and the reason is in Err
//
// The middle one is the one a boolean would lose. A download of a subtitle, a
// cover image or a `.nfo` completes, is scanned, and correctly produces no
// scene -- and reporting that as a failure tells the operator something is
// wrong when the plugin did exactly the right thing. Reporting it as success
// tells them a scene exists when none does.
type Outcome struct {
	// State is one of the three constants below.
	State string

	// Path is the file as the plugin wrote it.
	Path string

	// SceneID is the linked scene, set only when State is StateLinked. Empty
	// otherwise, rather than a zero id that a caller might use.
	SceneID string

	// JobID is the host's scan job, so an operator can find it in the host's
	// own job list. Carried on every outcome that reached the host, so a
	// "nothing happened" case is still traceable to a job.
	JobID string

	// Err is set only when State is StateFailed.
	Err error
}

// The three states, as constants.
//
// A closed set rather than free strings, because the caller switches on them and
// a typo in a comparison against a literal is a comparison that is always false
// -- so an unrecognised state would take whichever branch the zero value
// happened to reach.
const (
	StateLinked         = "linked"
	StateScannedNoScene = "scanned_no_scene"
	StateFailed         = "failed"
)

// DefaultPollInterval and DefaultTimeout are the wait's shape.
//
// A scan of one video file is a few seconds -- it walks a directory, reads
// video metadata and computes phashes. The interval is a tenth of a second
// because the poll is one GraphQL query and the operator is watching a task
// list, and the timeout is generous because the alternative to waiting is
// reporting "not linked" for a file that is about to be linked.
//
// Both are fields rather than constants so a test can drive the wait without
// spending real seconds, which is the only way to test the timeout branch at
// all: a test that waits 90 real seconds to check a deadline is a test nobody
// runs.
const (
	DefaultPollInterval = 100 * time.Millisecond
	DefaultTimeout      = 90 * time.Second
)

// Integrator runs the host hand-off for a completed download.
//
// The method is on a struct rather than a bare function so the interval and
// timeout can be fields, and so the host dependency is one field that a test
// substitutes. Everything else in this file is orchestration, and orchestration
// is where the interesting mistakes are: a scan that is fired and not waited
// for, a result reported for a path nobody scanned, a timeout reported as a
// failure when the file may still be scanned a second later.
type Integrator struct {
	host Host

	// PollInterval and Timeout may be zero, in which case the defaults apply.
	// Zero-able rather than defaulted in a constructor so a zero Integrator
	// behaves the same as a constructed one -- the same reasoning as
	// `rpc.NewRunner`, and for the same reason: a value used directly in a test
	// should be in the state production puts it in.
	PollInterval time.Duration
	Timeout      time.Duration
}

// NewIntegrator returns an Integrator over the given host.
func NewIntegrator(host Host) *Integrator {
	return &Integrator{host: host}
}

func (i *Integrator) interval() time.Duration {
	if i.PollInterval > 0 {
		return i.PollInterval
	}
	return DefaultPollInterval
}

func (i *Integrator) timeout() time.Duration {
	if i.Timeout > 0 {
		return i.Timeout
	}
	return DefaultTimeout
}

// Call runs the host hand-off for one completed download, and reports what
// actually happened.
//
// The sequence, and each step's reason for being where it is:
//
//  1. Check the path locally. A path that cannot be checked never reaches the
//     host -- see CheckPath.
//  2. Ask the host to scan it. `metadataScan` returns immediately with a job
//     id; the scan itself is queued.
//  3. Wait for a scene to appear, by asking the host about the PATH rather than
//     about the job.
//
// # WHY STEP 3 POLLS THE PATH AND NOT THE JOB
//
// The job can finish having decided the file is not a video, and a finished job
// with no scene is the answer "not a scene". Polling the job and then checking
// once would race: the job's completion and the scene's creation are not ordered
// the way that assumes, and a single check after completion reports "not linked"
// for a file the host has in fact linked a moment later.
//
// So the question asked is the one the operator is asking -- "is my file in the
// library yet" -- and the job id is carried for traceability rather than used as
// the synchronisation point.
//
// # WHAT A TIMEOUT MEANS, AND WHY IT IS NOT A FAILURE
//
// A timeout means the file is written and the scan was asked for, and the
// answer had not arrived within the window. That is neither a link nor a
// rejection, and the operator's next question is "did it work", which this
// cannot say. So the timeout is reported as a failure WITH the job id, and the
// message says what is known: the file is on disk, the host was asked, and the
// scene had not appeared. The alternative -- treating a timeout as "not linked" --
// reports a link that may exist a second later as one that does not.
//
// # WHY A SCAN FAILURE IS NOT A LINK FAILURE
//
// `ErrNotLinked` says the host answered and said no. A transport error says the
// host did not answer. Both are `StateFailed` with different errors, because the
// caller's next action differs: a `ErrNotLinked` is done, and a transport error
// is worth a retry. Collapsing them produces either a retry loop against a host
// that has already answered, or an operator told to re-download a file that is
// fine.
func (i *Integrator) Call(ctx context.Context, path string) Outcome {
	out := Outcome{State: StateFailed, Path: path}

	// The nil-host check, and it is here rather than in NewIntegrator because a
	// constructor that panicked would make the zero value unreachable to even
	// construct -- and a nil Host is reachable in two ordinary ways: a zero
	// `Handoff` in internal/handoff, and a caller that wired the dependency
	// later and forgot.
	//
	// A nil interface method call is a SEGFAULT, not an error, so without this
	// the first test to build a zero Integrator took the process down with it.
	if i.host == nil {
		out.Err = fmt.Errorf("%w: there is no host to ask, so there is no "+
			"scan and no link. A zero Integrator is a reasonable thing to "+
			"construct, and it must fail rather than crash", ErrUnreachable)
		return out
	}

	if err := CheckPath(path); err != nil {
		out.Err = err
		return out
	}

	jobID, err := i.host.Scan(ctx, []string{path})
	if err != nil {
		out.Err = err
		return out
	}
	// Set BEFORE the wait, so a timeout still carries a handle to the host's
	// job. The operator's next step for "did it work" is to look at that job,
	// and an outcome with no job id cannot be followed up.
	out.JobID = jobID

	scene, err := i.waitForScene(ctx, path, jobID)
	switch {
	case err != nil:
		out.Err = err
		return out
	case scene != nil:
		out.State = StateLinked
		out.SceneID = scene.ID
		return out
	}

	// The job finished and no scene appeared. Deciding WHICH of the two reasons
	// this is takes one more question to the host, and the question is the job
	// itself -- see the JobStatus doc comment for why "no scene" is ambiguous
	// on its own.
	job, err := i.host.JobStatus(ctx, jobID)
	if err != nil {
		// Not fatal. The scan ran and produced no scene, which is a real answer
		// about the file; the extra question was only for the finer distinction.
		// Reporting a failure here would turn "scanned, not a scene" into an
		// error because of a second, optional lookup.
		out.State = StateScannedNoScene
		return out
	}
	switch job.Status {
	case JobFailed, JobCancelled:
		out.Err = &ScanFailedError{Path: path, JobID: jobID,
			Status: job.Status, HostError: job.Error}
		out.State = StateFailed
		return out
	case JobFinished:
		out.State = StateScannedNoScene
		return out
	default:
		// READY or RUNNING: the job says it is still going, so the scan has not
		// finished and "no scene" is not yet an answer. Treated as a timeout
		// rather than a verdict, because a verdict is a claim about a scan that
		// has not happened yet.
		out.Err = &TimeoutError{Path: path, JobID: jobID, Within: i.timeout()}
		out.State = StateFailed
		return out
	}
}

// waitForScene polls until the host reports a scene, or the job finishes
// without one.
//
// Returning `(nil, nil)` for "the job is DONE and there is no scene" is what
// makes the caller's follow-up question necessary, and it is the honest shape:
// the wait itself cannot distinguish a finished scan from an exhausted deadline,
// because only the host knows which happened. So it returns as soon as EITHER a
// scene appears OR the job reaches a terminal state, and the caller decides what
// the absence means.
//
// That is also what stops a subtitle from costing the full timeout. The first
// version polled until the deadline regardless, so every download that correctly
// produced no scene -- a caption, a cover, a .nfo -- burned 90 seconds before
// reporting a perfectly good outcome.
//
// The job id is a PARAMETER rather than something looked up, because the whole
// point is that it has to travel with the timeout: the operator's next step for
// "did it work" is to open that job, and a timeout that arrives without it is
// the one failure nobody can follow up. The first version built the TimeoutError
// here with a literal "unknown", which is exactly the failure it was supposed to
// prevent.
//
// `(nil, nil)` means "the host has no scene and is not expected to produce one
// yet" -- the caller turns that into a link, a non-scene, or a timeout depending
// on how the wait ended, and this function does not know which yet. An error
// means the host could not be asked, which is terminal immediately: a poll loop
// that keeps going through a transport error is a download that hangs until the
// deadline for a host that has already gone away.
func (i *Integrator) waitForScene(ctx context.Context, path, jobID string) (*Scene, error) {
	deadline := time.Now().Add(i.timeout())
	ticker := time.NewTicker(i.interval())
	defer ticker.Stop()

	for {
		scene, err := i.host.SceneForPath(ctx, path)
		if err != nil {
			// TERMINAL, not retried.
			//
			// A transport error means the host did not answer, which is a
			// different thing from the host answering "no scene". Retrying it
			// until the deadline is 90 seconds of a task that was never going
			// to succeed, against a host that has already gone away.
			//
			// And it must be a `return` and never a `continue`: `continue` skips
			// the select at the bottom of the loop, so the loop never yields and
			// spins at full CPU for the whole deadline. That is a real hang, and
			// it is the single worst failure mode this function could have.
			if ctx.Err() != nil {
				return nil, fmt.Errorf("the wait was stopped: %w", ctx.Err())
			}
			return nil, err
		}
		if scene != nil {
			return scene, nil
		}

		// No scene YET. Whether that is an answer depends on whether the scan
		// has finished, and only the host knows -- so the job is consulted
		// before the deadline is, because a finished job is information and an
		// expired deadline is only the absence of it.
		job, err := i.host.JobStatus(ctx, jobID)
		if err == nil && isTerminal(job.Status) {
			// Terminal without a scene. The caller asks the job once more to
			// tell a failed scan from a clean one that found no video.
			return nil, nil
		}

		if time.Now().After(deadline) {
			return nil, &TimeoutError{
				Path:   path,
				JobID:  jobID,
				Within: i.timeout(),
			}
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("the wait was stopped: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// isTerminal reports whether a job status means the job has stopped changing.
//
// NOT `status == FINISHED`: FAILED and CANCELLED are equally terminal, and a
// scan that FAILED is exactly the case where waiting for the deadline would be
// worst — the outcome was decided seconds ago.
func isTerminal(status string) bool {
	switch status {
	case JobFinished, JobFailed, JobCancelled:
		return true
	}
	return false
}

// ScanFailedError is what a host-reported scan failure returns.
//
// A type and not `ErrNotLinked`, and the difference is the whole point: the host
// DECIDED the scan failed and said why, which is terminal and diagnosable, while
// "no scene" is an answer about the file's content.
type ScanFailedError struct {
	Path      string
	JobID     string
	Status    string
	HostError string
}

func (e *ScanFailedError) Error() string {
	if e.HostError != "" {
		return fmt.Sprintf("the host's scan of %s finished as %s: %s. The "+
			"file is on disk; nothing was linked", e.Path, e.Status, e.HostError)
	}
	return fmt.Sprintf("the host's scan of %s finished as %s. The file is on "+
		"disk; nothing was linked", e.Path, e.Status)
}

// Is returns false for every target, so a failed scan matches neither
// ErrNotLinked nor ErrUnreachable.
//
// A scan the host reported as FAILED is neither "the host said no" nor "the host
// did not answer" — it is a third thing, and a caller switching on those two
// would handle it as one of them.
func (e *ScanFailedError) Is(target error) bool { return false }

// TimeoutError is what a wait that ran out returns.
//
// A type rather than a sentinel so the job id and the path travel with it: the
// operator's next action is "look at that job", and a sentinel error carries
// neither. `ErrNotLinked` is deliberately NOT used here, and
// `TestATimeoutIsNotARejection` is what holds that line -- a timeout says the
// host has not answered, which is the opposite of a host that answered no.
type TimeoutError struct {
	Path   string
	JobID  string
	Within time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("the file is written and the host was asked to scan it, "+
		"but no scene appeared for it within %s. The file is on disk at %s and "+
		"the scan may still complete, so this is not a rejection",
		e.Within, e.Path)
}

// Is lets errors.Is(err, ErrNotLinked) stay false for a timeout.
//
// A one-line method that is the entire difference between "the host said no"
// and "we did not hear yet", and it is worth a method because a caller that
// distinguishes the two with `errors.Is(err, ErrNotLinked)` is the reason the
// distinction has to hold at the type level.
func (e *TimeoutError) Is(target error) bool { return false }
