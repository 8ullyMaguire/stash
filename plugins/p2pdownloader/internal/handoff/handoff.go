// Package handoff joins a finished download to the library.
//
// # WHAT THIS IS, AND WHY IT IS A SEPARATE PACKAGE
//
// `internal/library` knows how to get a file into the library and
// `internal/torrent` knows how to fetch one. Neither knows the other exists,
// and that is deliberate: the integration is the part with policy in it — what
// gets scanned, in what order, what happens when a file is refused — and policy
// that lives inside either of the two is policy that cannot be tested without a
// BitTorrent client or an HTTP server.
//
// So the join is its own package with no protocol in it, which is what makes it
// the cheapest thing here to test and the easiest thing to get wrong.
//
// # THE ORDER IS THE DESIGN, AND IT IS NOT NEGOTIABLE
//
//  1. the locator is validated          (internal/rpc)
//  2. core's consent gate is asked      (internal/rpc)
//  3. the transfer runs                 (internal/torrent)
//  4. the file is scanned and linked    (internal/library)
//
// Steps 2 and 4 are both the host, and the reason they are not interchangeable
// is the failure the whole plugin is designed around: a gate that says no must
// end the run, and a gate that is consulted AFTER the bytes are on disk has not
// gated anything. So a refused proposal never reaches step 3, and a completed
// transfer never happens for a locator core declined.
//
// # WHY A SCAN FAILURE DOES NOT DELETE THE FILE
//
// The operator spent bandwidth and wall-clock time on that download. The
// library refusing to link it is a statement about the FILE — a subtitle, a
// cover, a codec the host does not read — and not about the download being
// wrong. So the outcome is reported, the file stays, and the reason travels with
// it. A hand-off that cleaned up on failure would throw away a correct download
// because of a scanner's opinion, and re-running the job would fetch it again.
package handoff

import (
	"context"
	"fmt"

	"github.com/stashapp/stash-plugin-p2pdownloader/internal/library"
)

// Transfer is what a finished download looks like from here.
//
// An interface, and the reason is the same one that produced
// `rpc.Proposer` and `library.Host`: the orchestration is where the mistakes are,
// and a mistake in the orchestration should be testable without a DHT, a peer
// wire protocol and a disk.
//
// The contract is one sentence, and it is the whole contract:
//
//	Files returns ABSOLUTE paths of completed files, or an error.
//
// Absolute, and that is load-bearing. `library.CheckPath` refuses a relative
// path, and the error it returns names the host's working directory — a caller
// that hands one over gets a scan of somewhere it did not intend, which is a
// scan of the host's own files.
type Transfer interface {
	// Files returns the paths the transfer completed.
	Files() ([]string, error)
}

// Result is what a whole download reports: the files, and what became of them.
type Result struct {
	// Files is what completed, in the order the transfer reported it. Populated
	// even when the hand-off failed, because a caller that has lost the paths
	// cannot tell the operator where the download went.
	Files []string

	// Outcomes is one entry per file, in the same order as Files.
	//
	// A LIST rather than a single outcome because a multi-file torrent produces
	// several, and they do not agree: a release directory holds a video, three
	// subtitles and a cover, and the host links the video and correctly declines
	// the rest. Collapsing that to one status loses either the link or the
	// refusals.
	Outcomes []library.Outcome
}

// Linked returns the paths that entered the library.
//
// A convenience over the outcomes, and the shape a caller usually wants: "which
// of these did the operator get". The scan outcomes are on the other field for
// the operator who wants to know why something did not link.
func (r Result) Linked() []string {
	var out []string
	for _, o := range r.Outcomes {
		if o.State == library.StateLinked {
			out = append(out, o.Path)
		}
	}
	return out
}

// Handoff runs the library hand-off for a completed transfer.
//
// A struct with one field rather than a function, so a test can substitute a
// host and so the zero value behaves the same as a constructed one — the
// reasoning `rpc.NewRunner` documents, applied again.
type Handoff struct {
	// Library is the hand-off. Nil in a zero Handoff, and nil means "use the
	// default integrator", which is what makes the zero value usable.
	Library *library.Integrator
}

// Run scans and links everything the transfer completed.
//
// # WHY IT DOES NOT STOP AT THE FIRST FAILURE
//
// A release with one unreadable file and nine good ones should link nine files
// and report the tenth. Stopping at the first error would make a single
// permission problem on a .nfo discard the video next to it, and the operator
// would have to run the job four more times to get through the directory.
//
// So every file is attempted and every outcome is reported. The error the
// function returns is the FIRST one, not the only one, and the full list is on
// the result — a caller that wants to show all ten messages has them.
//
// # WHY A COMPLETED TRANSFER WITH NO FILES IS AN ERROR
//
// "The transfer finished and produced nothing" is not a success and not a
// failure of the hand-off; it is a contradiction, and it means the transfer
// reported completion without having written anything. Silently returning an
// empty result would report a download that did nothing as one that worked.
func (h *Handoff) Run(ctx context.Context, t Transfer) (Result, error) {
	if t == nil {
		return Result{}, fmt.Errorf("there is no transfer to hand off. A " +
			"hand-off with nothing to hand off reports success for a download " +
			"that never happened")
	}

	files, err := t.Files()
	if err != nil {
		return Result{}, fmt.Errorf("the transfer reported: %w", err)
	}
	if len(files) == 0 {
		return Result{}, fmt.Errorf("the transfer finished without producing a " +
			"file, so there is nothing to scan. A completed transfer that " +
			"wrote nothing is a contradiction, not an empty success")
	}

	integrator := h.Library
	if integrator == nil {
		// Unreachable through Handoff's own constructor in production, and
		// tolerated because a zero Handoff is a reasonable thing to write in a
		// test. It fails on the first call rather than at construction, which is
		// the same choice `rpc.NewRunner` makes for the same reason.
		integrator = library.NewIntegrator(nil)
	}

	result := Result{Files: files}
	var firstErr error
	for _, path := range files {
		outcome := integrator.Call(ctx, path)
		result.Outcomes = append(result.Outcomes, outcome)
		if outcome.State == library.StateFailed && firstErr == nil {
			firstErr = outcome.Err
		}
	}
	return result, firstErr
}
