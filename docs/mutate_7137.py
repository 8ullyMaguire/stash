#!/usr/bin/env python3
"""Mutation harness for the stash#7136 CDP JSON-scraper fix.

A test that passes proves nothing unless it CAN fail. Each mutation below
re-introduces one specific pre-fix behaviour and asserts that a named test
notices.

Three rules, each learned from a harness that got one wrong:

1. **Mutation and restore happen in ONE process, with restore in a `finally`
   AND a fresh read from the snapshot EVERY probe.** A restore in a separate
   step is orphaned whenever the caller dies between the two, and a sweep
   interrupted mid-probe leaves the mutation applied -- which still compiles and
   still looks fine.

2. **A mutation that stops the package building is NOT a kill.** It is the
   guard never running. `did-not-run` is a separate verdict with its own exit
   code, because a harness that counts build failures as kills reports 4/4
   while testing one thing.

3. **A surviving mutation is a RESULT, not a failure to route around.** Read the
   diff before touching the source: the guarded line is either DEAD or
   REDUNDANT, or the CODE is wrong and removing the check made it correct.

Usage:  python3 docs/mutate_7137.py
Exit:   0 all killed · 1 a survivor (go look at a test) · 2 a probe did not
        run or the restore is broken (go look at THIS FILE)
"""

import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
TARGET = REPO / "pkg/scraper/url.go"

# (name, test-to-run, extra go test flags, old, new)
# Every `old` is post-fix source, so a MISSING anchor means the code moved and
# the row is a SKIP -- never reported as a survivor.
MUTATIONS = [
    (
        "a JSON mime type is not recognised (the pre-fix behaviour)",
        "TestIsJSONMimeType",
        [],
        '\treturn mimeType == "application/json" ||\n\t\tstrings.HasSuffix(mimeType, "+json") ||\n\t\tmimeType == "text/json"',
        '\t// mutation: never recognise JSON\n\treturn false && strings.HasSuffix(mimeType, "+json")',
    ),
    (
        "the mime type is compared without normalising case or parameters",
        "TestIsJSONMimeType",
        [],
        '\tmimeType, _, _ = strings.Cut(mimeType, ";")\n\tmimeType = strings.TrimSpace(strings.ToLower(mimeType))',
        '\t// mutation: no Cut, no case fold',
    ),
    (
        "a later Document response overrides the main one (the iframe hijack)",
        "TestJSONDocumentTrackerOnlyRecordsFirstDocument",
        [],
        "\tif t.recorded {\n\t\treturn\n\t}\n\tt.recorded = true",
        "\t// mutation: every document overwrites the last",
    ),
    (
        # The anchor moved when the decision was EXTRACTED into
        # readMainDocument. While it was inline in the ActionFunc this row
        # reported SKIP, and the SKIP was a finding: the branch was reachable
        # only by running a browser, so nothing could guard it. Extraction is
        # the seam and its existence is the test's precondition.
        "a JSON document's body is not read; OuterHTML is used for everything",
        "TestReadMainDocumentUsesTheRawBodyForAJSONDocument",
        [],
        "\tif !isJSON {\n\t\treturn outerHTML(ctx)\n\t}\n\n\tbody, err := getBody(ctx, requestID)",
        "\t// mutation: always take the OuterHTML path, as before\n\tif true || !isJSON {\n\t\treturn outerHTML(ctx)\n\t}\n\n\tbody, err := getBody(ctx, requestID)",
    ),
    (
        "a body that cannot be read returns empty content instead of falling back",
        "TestReadMainDocumentFallsBackToHTMLWhenTheBodyIsUnavailable",
        [],
        "\t\tlogger.Warnf(\"[scraper] could not get raw response body for JSON document, falling back to OuterHTML (the scrape will likely still fail as a result): %v\", err)\n\t\treturn outerHTML(ctx)",
        "\t\t// mutation: swallow the error and return nothing\n\t\treturn \"\", nil",
    ),
    (
        "an OuterHTML failure is swallowed rather than propagated",
        "TestReadMainDocumentPropagatesAnOuterHTMLFailure",
        [],
        "\tif !isJSON {\n\t\treturn outerHTML(ctx)\n\t}",
        "\tif !isJSON {\n\t\t// mutation: swallow the accessor's error\n\t\thtml, _ := outerHTML(ctx)\n\t\treturn html, nil\n\t}",
    ),
    (
        "the tracker reads the JSON flag without the lock (the data race)",
        "TestJSONDocumentTrackerConcurrentAccess",
        ["-race"],
        "func (t *jsonDocumentTracker) mainDocument() (network.RequestID, bool) {\n\tt.mutex.Lock()\n\tdefer t.mutex.Unlock()\n",
        "func (t *jsonDocumentTracker) mainDocument() (network.RequestID, bool) {\n",
    ),
]

BUILD_ERRORS = (
    "build failed", "cannot use", "undefined:", "declared and not used",
    "syntax error", "imported and not used", "missing return",
    "all declarations of", "assignment mismatch", "no new variables",
    "redeclared", "too many arguments", "not enough arguments",
    "declared and not used", "unused variable", "constant 0 overflows",
)


def run_test(pattern, flags):
    """Run one test. Returns (verdict, output).

    verdict is "passed", "failed", or "did-not-run". The third is the important
    one: a mutation that breaks the build produces `[build failed]` and exit 1,
    which a naive harness scores as a kill.
    """
    try:
        r = subprocess.run(
            ["go", "test", *flags, "./pkg/scraper/", "-count=1", "-v", "-run", pattern],
            cwd=REPO, capture_output=True, text=True, timeout=900,
        )
    except subprocess.TimeoutExpired:
        return "did-not-run", "(harness: the test run TIMED OUT -- a probe that " \
                               "hangs takes the session with it)"
    out = r.stdout + r.stderr

    for shape in BUILD_ERRORS:
        if shape in out:
            return "did-not-run", out

    if "--- FAIL" in out or "--- PASS" in out:
        # Confirm the named test actually executed rather than being filtered
        # out by -run. A pattern that matches nothing exits 0 and looks like a
        # pass, which is the other way this harness can lie.
        if f"=== RUN   {pattern}" not in out:
            return "did-not-run", out + f"\n(harness: the pattern {pattern!r} " \
                                         "matched no test, so this proves nothing)"
        if "WARNING: DATA RACE" in out:
            # A race is a FAIL for this harness even when the test says PASS:
            # the race detector reports it and the test still asserts only the
            # end state, so a torn read can pass.
            return "failed", out
        return ("failed" if "--- FAIL" in out else "passed"), out
    return "did-not-run", out


def main():
    # A snapshot in a temp dir, NOT in the repo: a backup file left in the work
    # tree is a backup somebody will eventually commit.
    backup = Path(tempfile.mkdtemp()) / "url.go.orig"
    shutil.copy2(TARGET, backup)

    killed, survived, not_run, skipped = 0, [], [], []
    restore_broken = False
    try:
        for name, test, flags, old, new in MUTATIONS:
            # Read from the pristine snapshot EVERY time, not from the previous
            # iteration's restored file, so a restore that silently failed
            # cannot compound across probes.
            src = backup.read_text()
            if old not in src:
                print(f"SKIP    {name}\n"
                      f"        anchor not found -- the code moved. A mutation whose "
                      f"anchor is missing tests nothing, and reporting it as a "
                      f"survivor would be wrong.")
                skipped.append(name)
                continue

            TARGET.write_text(src.replace(old, new, 1))
            try:
                verdict, out = run_test(test, flags)
            finally:
                # Restore INSIDE this iteration. If the test command raises, the
                # tree is still put back.
                shutil.copy2(backup, TARGET)

            if verdict == "did-not-run":
                not_run.append(name)
                detail = next((l for l in out.splitlines()
                               if "harness:" in l or "build failed" in l), "")
                print(f"NOT-RUN {name}\n"
                      f"        {test} never executed -- {detail.strip()[:70]}\n"
                      f"        This is NOT a kill. The mutation must keep the "
                      f"package compiling.")
            elif verdict == "passed":
                survived.append(name)
                print(f"SURVIVED {name}\n"
                      f"        {test} still passed -- this test does not guard "
                      f"this line.")
            else:
                killed += 1
                first = next((l for l in out.splitlines()
                              if "--- FAIL" in l or "DATA RACE" in l), "")
                print(f"KILLED   {name}\n"
                      f"        by {test}  [{first.strip()[:66]}]")
    finally:
        # Restoration must not depend on the loop reaching its own epilogue.
        shutil.copy2(backup, TARGET)
        # Compare byte-for-byte: a restore that cannot fail is not a restore.
        restore_broken = TARGET.read_bytes() != backup.read_bytes()
        if not restore_broken:
            print("restored url.go from snapshot (byte-for-byte verified)")

    # Outside the finally on purpose: a `return` inside a `finally` discards an
    # exception that is still propagating, which turns a crash into a quiet
    # verdict.
    if restore_broken:
        print("\nFATAL: url.go does NOT match the snapshot after the sweep. "
              "The tree is dirty and the restore is broken.")
        return 2

    total = len(MUTATIONS)
    print(f"\n{killed}/{total} killed, {len(survived)} survived, "
          f"{len(not_run)} did not run, {len(skipped)} skipped")
    if not_run:
        print("\nA 'did not run' is a harness bug, not a result. The probe broke "
              "the build, so the guard never ran.")
        return 2
    if survived:
        print("\nSurvivors are findings, not noise:")
        for name in survived:
            print(f"  - {name}")
        print("\nA surviving mutation means the guarded line is DEAD or REDUNDANT "
              "(the next layer already does it), or that the CODE was wrong and "
              "removing the check made it correct. Read the diff before deciding.")
    return 1 if survived else 0


if __name__ == "__main__":
    sys.exit(main())
