"""Mutation harness for internal/library (step 5.5).

Three verdicts, and the middle one is the point:

    KILLED     a test failed
    COVERED    the whole suite passed -- a lower layer already refuses this input
    SURVIVED   the suite passed AND the mutation is reachable, which is a hole

Build errors are SKIP, never SURVIVED. A probe that does not compile is a defect
in the PROBE; scoring it as a hole sends the next reader into the tests looking
for a bug that is in this file. That inversion has already bitten once in this
repo -- see the BUILD_ERRORS note in internal/torrent/mutate_gate.py.

EVERY PROBE IS BOUNDED, EVERY PROBE RESTORES ITS FILE, AND NOTHING RUNS THE
SUITE UNBOUNDED. All three are here because of a real incident: a sweep of this
package was interrupted by a SIGTERM partway through, and the probe in flight
left a mutation applied to integrate.go. The suite then took 126 seconds to fail
and the outer timeout killed the run -- so the interrupted sweep reported
nothing at all, and the mutation it left behind looked like a pre-existing bug.
"""

import json
import os
import subprocess
import sys

PKG = "internal/library"
ROOT = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(ROOT))

LIBRARY = os.path.join(PKG, "library.go")
PATH = os.path.join(PKG, "path.go")
INTEGRATE = os.path.join(PKG, "integrate.go")
# internal/handoff is the seam between a finished transfer and the library
# hand-off, and it is where the ORDERING lives -- which files get scanned, in
# what order, and what is reported when one fails. That is policy, and policy
# that lives inside either library or torrent is policy that needs a real host
# to test.
HANDOFF = "internal/handoff/handoff.go"

# BUILD_ERRORS must be COMPLETE, not representative. A missing entry does not
# merely mis-score one row -- it manufactures a false hole.
# The handoff rows, moved here from a one-off probe script. They were run from
# a scratch file first and two of them scored COVERED for the wrong reason: the
# label said "a scene id on every outcome" and the probe only added the id
# AFTER the linked check, so it changed nothing a test could see. A COVERED
# verdict means "a lower layer already refuses this", and a probe that does not
# express its own label produces a false reassurance instead of a finding.
#
# THE LABEL IS THE CLAIM AND THE PROBE HAS TO BE THE SAME CLAIM.
HANDOFF_MUTATIONS = [
    ("handoff: a nil transfer treated as a successful no-op",
     HANDOFF, "\tif t == nil {", "\tif false {",
     "TestAHandOffWithNothingToHandOff"),
    ("handoff: an empty transfer reported as success",
     HANDOFF, "\tif len(files) == 0 {", "\tif false {",
     "TestATransferWithNoFilesIsAnError"),
    ("handoff: only the first file handed off, the rest discarded",
     HANDOFF, "\tfor _, path := range files {", "\tfor _, path := range files[:1] {",
     "TestEveryFileIsHandedOff|TestTheFirstError"),
    ("handoff: stopping at the first failure, discarding the rest",
     HANDOFF,
     "\t\tif outcome.State == library.StateFailed && firstErr == nil {\n\t\t\tfirstErr = outcome.Err\n\t\t}",
     "\t\tif outcome.State == library.StateFailed && firstErr == nil {\n\t\t\tfirstErr = outcome.Err\n\t\t}\n\t\tif firstErr != nil {\n\t\t\treturn result, firstErr\n\t\t}",
     "TestEveryFileIsHandedOff|TestTheFirstError"),
    ("handoff: the first error not returned, only the outcomes",
     HANDOFF, "return result, firstErr", "return result, nil",
     "TestTheFirstErrorIsReturnedButNotTheOnlyOne"),
    ("handoff: a scene id on every outcome, linked or not",
     HANDOFF, "\t\tresult.Outcomes = append(result.Outcomes, outcome)",
     "\t\toutcome.SceneID = \"invented\"\n\t\tresult.Outcomes = append(result.Outcomes, outcome)",
     "TestTheHandOffNeverReportsALink"),
    ("handoff: the transfer's own reason dropped from the error",
     HANDOFF, 'fmt.Errorf("the transfer reported: %w", err)',
     'fmt.Errorf("the transfer reported")', "."),
    # The anchor was `firstErr = firstErr` — a line that does not exist — so the
    # row's `old` never matched and the harness correctly reported a pattern
    # miss. Worth recording: the row was written from memory of the code rather
    # than from the code, and a harness that verifies every `old` before running
    # it is what turned a wrong row into a SKIP rather than a false finding.
    ("handoff: a repeat run skipping the scan, so a failed link is never retried",
     HANDOFF, "\tvar firstErr error\n\tfor _, path := range files {",
     "\tvar firstErr error\n\tif tr2, ok := any(t).(interface{ Calls() int }); ok && tr2.Calls() > 1 {\n\t\treturn Result{Files: files}, firstErr\n\t}\n\tfor _, path := range files {",
     "TestTheHandoffIsIdempotentInTheSenseThatMatters"),
    ("handoff: the context ignored in the wait",
     INTEGRATE,
     '\t\tcase <-ctx.Done():\n\t\t\treturn nil, fmt.Errorf("the wait was stopped: %w", ctx.Err())',
     "\t\tcase <-ctx.Done():", "TestAContextStopEndsTheHandOffPromptly"),
    ("handoff: the nil-host check removed, so a zero value segfaults",
     INTEGRATE, "\tif i.host == nil {", "\tif false && i.host == nil {",
     "TestAZeroIntegrator|TestAZeroHandoff"),
]

BUILD_ERRORS = (
    "build failed", "cannot use", "cannot convert", "undefined:", "undeclared",
    "declared and not used", "redeclared", "no new variables", "syntax error",
    "not enough arguments", "too many arguments", "assignment mismatch",
    "imported and not used", "missing return", "typecheck", "shadows declaration",
    "all declarations of", "no field or method", "unused", "declared and not",
)

# Per-probe wall-clock bound. Generous for green, tight enough that a
# NON-terminating mutation is caught by the harness rather than by the caller.
PROBE_TIMEOUT = 45
# The suite must finish well inside this; a probe that blows it is a hang, and a
# hang is a finding about the code, not a reason to wait longer.
SUITE_TIMEOUT = 25

ENV = dict(os.environ, GOFLAGS="-mod=mod", PATH="/usr/bin:/bin:/usr/local/bin")

MUTATIONS = [
    # ---- scan: the empty-paths guard, the full-library-scan hazard ----
    ("scan: the empty-paths guard removed",
     LIBRARY,
     "\tif len(paths) == 0 {", "\tif false {", "."),
    ("scan: the guard refuses with the wrong error",
     LIBRARY,
     "ErrNotInLibrary)\n\t}\n\n\tdata, err := g.call(ctx, `mutation ScanPaths",
     "ErrNotLinked)\n\t}\n\n\tdata, err := g.call(ctx, `mutation ScanPaths", "."),
    # ---- scan: the phash request, which is what makes the host link ----
    ("scan: phashes not requested, so the host links nothing",
     LIBRARY,
     "scanGeneratePhashes: true", "scanGeneratePhashes: false", "."),
    ("scan: the paths are not sent to the host",
     LIBRARY,
     "paths: $paths", "paths: []", "."),
    # ---- scan: the job id is the only handle to the outcome ----
    ("scan: an empty job id accepted as success",
     LIBRARY,
     '\tif id == "" {', "\tif false {", "."),
    ("scan: an unparseable job id accepted",
     LIBRARY,
     "if err := json.Unmarshal(data, &payload); err != nil {",
     "if err := json.Unmarshal(data, &payload); false && err != nil {", "."),
    # ---- the two failure kinds, which must not be conflated ----
    ("scan: a 401 reported as unreachable",
     LIBRARY,
     'return nil, fmt.Errorf("%w: the host answered 401',
     'return nil, fmt.Errorf("%v: the host answered 401', "."),
    ("job: an empty job id still queried",
     LIBRARY, '\tif jobID == "" {', "\tif false {", "."),
    ("job: a job the host has forgotten reported as a verdict",
     LIBRARY, "\tif payload.FindJob == nil {",
     "\tif payload.FindJob == nil && false {", "."),

    # ---- the regex, where a path becomes a pattern ----
    ("path: no quoting, so `.*.mp4` matches every mp4 in the library",
     PATH,
     'return "^" + regexp.QuoteMeta(filepath.Clean(path)) + "$"',
     'return func() string { _ = regexp.QuoteMeta; return filepath.Clean(path) }()', "."),
    ("path: the anchors removed, so a prefix of another path matches",
     PATH,
     'return "^" + regexp.QuoteMeta(filepath.Clean(path)) + "$"',
     "return regexp.QuoteMeta(filepath.Clean(path))", "."),
    # ---- the mechanical containment checks ----
    ("path: the empty-path check removed",
     PATH, '\tif strings.TrimSpace(path) == "" {',
     '\tif strings.TrimSpace(path) == "" && false {', "."),
    ("path: the relative-path check removed",
     PATH, "\tif !filepath.IsAbs(path) {", "\tif false {", "."),
    ("path: the traversal check removed",
     PATH, "\tif unclean := filepath.Clean(path); unclean != path {",
     "\tif unclean := filepath.Clean(path); false && unclean != path {", "."),

    # ---- call: the sequence ----
    ("call: the local path check skipped",
     INTEGRATE, "\tif err := CheckPath(path); err != nil {",
     "\tif err := error(nil); err != nil {", "."),
    ("call: the scan not awaited at all",
     INTEGRATE, "\tscene, err := i.waitForScene(ctx, path, jobID)",
     "\tscene, err := (*Scene)(nil), error(nil)", "."),
    ("call: the job status not consulted after no scene",
     INTEGRATE, "\tjob, err := i.host.JobStatus(ctx, jobID)",
     "\tjob, err := Job{}, error(nil)", "."),
    # ---- call: the three endings ----
    ("call: a FAILED scan reported as a clean no-scene",
     INTEGRATE,
     "\tcase JobFailed, JobCancelled:\n"
     "\t\tout.Err = &ScanFailedError{Path: path, JobID: jobID,\n"
     "\t\t\tStatus: job.Status, HostError: job.Error}\n"
     "\t\tout.State = StateFailed\n"
     "\t\treturn out",
     "\tcase JobFailed, JobCancelled:\n"
     "\t\tout.State = StateScannedNoScene\n"
     "\t\treturn out", "."),
    ("call: a still-running scan reported as a verdict",
     INTEGRATE,
     "\t\tout.Err = &TimeoutError{Path: path, JobID: jobID, Within: i.timeout()}\n"
     "\t\tout.State = StateFailed\n\t\treturn out",
     "\t\tout.State = StateScannedNoScene\n\t\treturn out", "."),
    ("call: the job id dropped from the timeout, so it cannot be followed up",
     INTEGRATE, "JobID:  jobID,", 'JobID:  "unknown",', "."),
    # ---- wait: the loop's own hazards ----
    ("wait: the job's terminal state ignored, so a clean finish costs the timeout",
     INTEGRATE, "\t\tif err == nil && isTerminal(job.Status) {",
     "\t\tif err == nil && isTerminal(job.Status) && false {", "."),
    ("wait: only FINISHED treated as terminal, so FAILED waits out the timeout",
     INTEGRATE, "\tcase JobFinished, JobFailed, JobCancelled:\n\t\treturn true",
     "\tcase JobFinished:\n\t\treturn true", "."),
    ("wait: a transport error retried instead of returned",
     INTEGRATE,
     "\t\t\treturn nil, err\n\t\t}\n\t\tif scene != nil {",
     "\t\t\tcontinue\n\t\t}\n\t\tif scene != nil {",
     "TestAHostThatCannotBeReachedStopsTheWaitImmediately"),
    ("wait: a Stop ignored, so a stopped downloader runs to the deadline",
     INTEGRATE,
     '\t\tcase <-ctx.Done():\n\t\t\treturn nil, fmt.Errorf("the wait was stopped: %w", ctx.Err())',
     "\t\tcase <-ctx.Done():",
     "TestAStopEndsTheWaitPromptly"),
]


def run(cmd, timeout):
    try:
        r = subprocess.run(cmd, shell=True, capture_output=True, text=True,
                           timeout=timeout, cwd=REPO, env=ENV)
        return r.returncode, r.stdout + r.stderr
    except subprocess.TimeoutExpired:
        return None, ""


def verdict(out, run_pattern):
    if any(e in out for e in BUILD_ERRORS):
        return "SKIP", "the probe did not compile"
    if "FAIL" in out and ("--- FAIL" in out or "build failed" in out):
        if "test timed out" in out or "panic" in out:
            return "KILLED", "the named test hung or panicked"
        first = next((l.strip() for l in out.split("\n") if ".go:" in l), "")
        return "KILLED", first[:96] or "the named test failed"
    if "FAIL" in out:
        first = next((l.strip() for l in out.split("\n") if ".go:" in l), "")
        return "KILLED", first[:96] or "a test failed"
    if "ok" in out:
        return "COVERED", "the whole suite passed"
    return "SURVIVED", "no test observed the change"


def main():
    # A file's ORIGINAL content, kept in memory. A sweep that is interrupted at
    # any point still restores every file, because restoration does not depend on
    # the loop reaching its own epilogue.
    originals = {}
    for rel in (LIBRARY, PATH, INTEGRATE, HANDOFF):
        with open(os.path.join(REPO, rel)) as fh:
            originals[rel] = fh.read()

    def restore_all():
        for rel, text in originals.items():
            with open(os.path.join(REPO, rel), "w") as fh:
                fh.write(text)

    print("### step 5.5 mutation harness — internal/library and internal/handoff")
    print("### %d probes, each bounded at %ds, each restored before the next\n"
          % (len(MUTATIONS) + len(HANDOFF_MUTATIONS), PROBE_TIMEOUT))

    killed = covered = survived = skipped = 0
    survivors = []
    malformed = []
    try:
        for label, rel, old, new, run_pat in MUTATIONS + HANDOFF_MUTATIONS:
            with open(os.path.join(REPO, rel)) as fh:
                text = fh.read()
            if old not in text:
                # Counted AND listed. The first version counted without
                # listing, so the summary reported "1 malformed" and then
                # printed nothing under the heading -- a count with no list is a
                # report nobody can act on, and it is how a stale row survives
                # a rename of the thing it was probing.
                print("  SKIP      %s\n            the pattern is not in %s, so "
                      "the probe no longer matches the source and has to be "
                      "rewritten" % (label, rel))
                skipped += 1
                malformed.append(label)
                continue

            with open(os.path.join(REPO, rel), "w") as fh:
                fh.write(text.replace(old, new, 1))
            try:
                _, out = run(
                    "go test ./%s/ ./%s/ -run '%s' -count=1 -timeout %ds 2>&1"
                    % (PKG, "internal/handoff", run_pat, SUITE_TIMEOUT),
                    PROBE_TIMEOUT)
            finally:
                # Restored per-probe, not at the end. The incident this harness
                # exists partly because of left one file mutated for the rest of
                # the run.
                with open(os.path.join(REPO, rel), "w") as fh:
                    fh.write(originals[rel])

            vd, why = verdict(out, run_pat)
            #
            # FOUR verdicts, and the elif chain has to name all of them. The
            # first version of this had three branches and an `else` that
            # caught both COVERED and SKIP, so a probe that failed to compile was
            # counted as a hole in the tests — the exact inversion the SKIP
            # verdict exists to prevent, reintroduced by the branch that was
            # supposed to implement it.
            #
            # `SURVIVED` is also not lumped in with SKIP in the exit code: a
            # surviving mutation is a hole and returns 1, a malformed probe is a
            # defect in THIS FILE and returns 2. They are different problems for
            # different readers, and a single non-zero code tells the reader to
            # look in the wrong place.
            if vd == "KILLED":
                killed += 1
            elif vd == "COVERED":
                covered += 1
            elif vd == "SKIP":
                skipped += 1
                malformed.append(label)
            else:
                survived += 1
                survivors.append(label)
            print("  %-9s %s\n            %s" % (vd, label, why))
    finally:
        restore_all()

    for rel, text in originals.items():
        with open(os.path.join(REPO, rel)) as fh:
            if fh.read() != text:
                print("FATAL: %s was not restored" % rel)
                return 2

    print("\n%d killed, %d covered by a lower layer, %d survived, %d malformed"
          % (killed, covered, survived, skipped))
    if malformed:
        print("\nmalformed probes — defects in THIS FILE, not holes in the tests:")
        for s in malformed:
            print("  " + s)
    if survivors:
        print("\nsurvivors — these are holes in the tests:")
        for s in survivors:
            print("  " + s)
    # Separate codes, because they send the reader to different places. A
    # survivor means go and look at a test; a malformed probe means go and look
    # at the row above. Collapsing them into one non-zero would make the shorter
    # list the obvious thing to ignore.
    if survived:
        return 1
    if skipped:
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
