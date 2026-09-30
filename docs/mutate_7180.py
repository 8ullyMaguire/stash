#!/usr/bin/env python3
"""Mutation harness for the stash#7180 `.nogallery` clean fix.

A test that passes proves nothing unless it CAN fail. Each mutation below
re-introduces one specific pre-fix behaviour and asserts that a named test
notices. A mutation that survives means the corresponding test is not actually
guarding the thing it claims to guard.

Three rules this harness follows, each learned from a harness that got one of
them wrong:

1. **Mutation and restore happen in ONE process, with restore in a `finally`
   AND a snapshot taken per-probe.** A restore in a separate step is orphaned
   whenever the caller dies between the two, and a sweep interrupted mid-probe
   leaves the mutation applied -- which still compiles and still looks fine.

2. **A mutation that stops the package building is NOT a kill.** It is the
   guard never running. `did-not-run` is a separate verdict with its own exit
   code, because a harness that counts build failures as kills reports 4/4
   while testing one thing.

3. **A surviving mutation is a RESULT, not a failure to route around.** It
   means the guarded line is DEAD or REDUNDANT: the next layer already does the
   job, so the check should be deleted rather than defended.

Usage:  python3 docs/mutate_7180.py
Exit:   0 all killed · 1 a survivor (go look at a test) · 2 a probe did not
        run or a probe is malformed (go look at THIS FILE)
"""

import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
TARGET = REPO / "internal/manager/task_clean.go"

# The default guard. Every row may OVERRIDE it with its own, which matters
# because two of the guards here live in different functions: the marker logic
# is in findGalleriesToClean and the DryRun guard is in cleanGalleries. A single
# module-level test name is a claim that one test can see the whole file, and
# for this file it is false.
DEFAULT_TEST = "TestFindGalleriesToCleanIncludesNoGalleryFolders"

# (name, test-to-run, the exact text to replace, what replaces it)
# Every `old` is post-fix source, so a MISSING anchor means the code moved and
# the row is a SKIP -- never reported as a survivor.
MUTATIONS = [
    (
        'the .nogallery marker is not consulted at all (the pre-fix behaviour)',
        'TestFindGalleriesToCleanIncludesNoGalleryFolders',
        '\tif _, err := os.Stat(filepath.Join(path, ".nogallery")); err == nil {\n\t\treturn true, nil\n\t} else if !errors.Is(err, os.ErrNotExist) {\n\t\treturn false, err\n\t}',
        '\t// mutation: never consult .nogallery, as before\n\t_ = errors.Is',
    ),
    (
        # The precedence row is a REORDERING, not a deletion. Deleting the
        # .forcegallery branch would still compile and still leave `errors`
        # used, but it would express "the force marker is gone" rather than
        # "the nogallery marker is consulted first" -- and the second is the
        # bug this row is for. Scan semantics (pkg/image/scan.go:415) are
        # `forceGallery || (config && !exemptGallery)`, so a folder with both
        # markers is a gallery, and that is what the fix must reproduce.
        '.nogallery wins over .forcegallery (inverted precedence)',
        'TestFindGalleriesToCleanIncludesNoGalleryFolders',
        '\tif _, err := os.Stat(filepath.Join(path, ".forcegallery")); err == nil {\n\t\treturn false, nil\n\t} else if !errors.Is(err, os.ErrNotExist) {\n\t\treturn false, err\n\t}',
        '\tif _, err := os.Stat(filepath.Join(path, ".forcegallery")); err == nil {\n\t\t_ = err\n\t}\n\n\tif _, err := os.Stat(filepath.Join(path, ".nogallery")); err == nil {\n\t\treturn false, nil\n\t} else if !errors.Is(err, os.ErrNotExist) {\n\t\treturn false, err\n\t}',
    ),
    (
        # This anchor is a CLOSURE BODY, so it is indented TWO tabs, not one.
        # The first version of this row was indented as a top-level statement
        # and reported `SKIP / anchor not found`, which reads as "the code
        # moved" and was actually a mis-indented string. That is the SKIP
        # verdict's worst failure mode: it is indistinguishable from a stale
        # anchor, so it teaches you to stop looking. SKIP on a row whose anchor
        # is post-fix source is a statement about the HARNESS until proven
        # otherwise.
        'a gallery matching BOTH rules is returned twice (the dedup is gone)',
        'TestFindGalleriesToCleanIncludesNoGalleryFolders',
        '\t\tif _, exists := toCleanSet[id]; exists {\n\t\t\treturn\n\t\t}',
        '\t\t// mutation: no dedup',
    ),
    (
        'selective clean ignores the requested paths',
        'TestFindGalleriesToCleanIncludesNoGalleryFolders',
        '\tinCleanPaths := func(g *models.Gallery) bool {\n\t\treturn g.Path != "" && (len(j.input.Paths) == 0 || fsutil.IsPathInDirs(j.input.Paths, g.Path))\n\t}',
        '\tinCleanPaths := func(g *models.Gallery) bool {\n\t\treturn g.Path != ""\n\t}',
    ),
    (
        'the folder-gallery query is dropped, so no marker is ever checked',
        'TestFindGalleriesToCleanIncludesNoGalleryFolders',
        '\t\treturn queryInBatches(ctx, folderGalleryFilter, func(g *models.Gallery) {\n\t\t\tif inCleanPaths(g) {\n\t\t\t\tfolderGalleries = append(folderGalleries, galleryCleanCandidate{\n\t\t\t\t\tid:   g.ID,\n\t\t\t\t\tpath: g.Path,\n\t\t\t\t})\n\t\t\t}\n\t\t})',
        '\t\t// mutation: never look at folder galleries\n\t\t_ = folderGalleryFilter\n\t\t_ = inCleanPaths\n\t\treturn nil',
    ),
    (
        # MIS-POINTED on the first run, and the fix was to ADD A TEST rather
        # than to delete the row. This row used to name
        # TestFindGalleriesToCleanIncludesNoGalleryFolders, which calls
        # `findGalleriesToClean` -- the function that only DECIDES. The
        # `if !j.input.DryRun` guard lives in `cleanGalleries`, which the
        # upstream test never calls, so deleting the guard was invisible BY
        # CONSTRUCTION and the row scored SURVIVED.
        #
        # A survivor here is not a redundant line. It is a data-destroying line
        # with no coverage: a dry run exists so a user can see what a clean
        # WOULD remove, and its entire value is that it removes nothing.
        #
        # The replacement test drives `cleanGalleries` for real in both
        # directions, and the non-dry half is the control that proves the
        # deletion path is reachable at all. Without that control the dry-run
        # half would also pass against a cleanGalleries that never deletes
        # anything -- which is the read-back-your-own-return-value trap, in a
        # place where the return value is a deleted row.
        'dry run actually deletes (the DryRun guard is gone)',
        'TestACleanDryRunDeletesNothing',
        '\tif !j.input.DryRun {\n\t\tfor _, id := range toClean {\n\t\t\tj.deleteGallery(ctx, id)\n\t\t}\n\t}',
        '\tfor _, id := range toClean {\n\t\tj.deleteGallery(ctx, id)\n\t}',
    ),
]


def run_test(pattern):
    """Run one test. Returns (verdict, output).

    verdict is "passed", "failed", or "did-not-run". The third is the important
    one: a mutation that breaks the build produces `[build failed]` and exit 1,
    which a naive harness scores as a kill. It is not a kill -- the test never
    executed, so it guarded nothing.
    """
    try:
        r = subprocess.run(
            ["go", "test", "./internal/manager/", "-count=1", "-v", "-run", pattern],
            cwd=REPO, capture_output=True, text=True, timeout=900,
        )
    except subprocess.TimeoutExpired:
        return "did-not-run", "(harness: the test run TIMED OUT -- a probe that " \
                               "hangs takes the session with it)"
    out = r.stdout + r.stderr

    for shape in ("build failed", "cannot use", "undefined:", "declared and not used",
                  "syntax error", "imported and not used", "missing return",
                  "all declarations of", "assignment mismatch", "no new variables",
                  "redeclared", "too many arguments", "not enough arguments"):
        if shape in out:
            return "did-not-run", out

    if "--- FAIL" in out or "--- PASS" in out:
        # Confirm the named test actually executed rather than being filtered
        # out by -run. A pattern that matches nothing exits 0 and looks like a
        # pass, which is the other way this harness can lie.
        if f"=== RUN   {pattern}" not in out:
            return "did-not-run", out + f"\n(harness: the pattern {pattern!r} " \
                                         "matched no test, so this proves nothing)"
        return ("failed" if "--- FAIL" in out else "passed"), out
    return "did-not-run", out


def main():
    # A snapshot in a temp dir, NOT in the repo: a backup file left in the work
    # tree is a backup somebody will eventually commit.
    backup = Path(tempfile.mkdtemp()) / "task_clean.go.orig"
    shutil.copy2(TARGET, backup)

    killed, survived, not_run, skipped = 0, [], [], []
    restore_broken = False
    try:
        for name, test, old, new in MUTATIONS:
            # Read from the pristine snapshot EVERY time, not from the previous
            # iteration's restored file. That way a restore that silently failed
            # cannot compound across probes.
            src = backup.read_text()
            if old not in src:
                print(f"SKIP    {name}\n"
                      f"        anchor not found -- the code moved. A mutation whose "
                      f"anchor is missing tests nothing, and reporting it as a "
                      f"survivor would be wrong.")
                skipped.append((name, test))
                continue

            TARGET.write_text(src.replace(old, new, 1))
            try:
                verdict, out = run_test(test)
            finally:
                # Restore INSIDE this iteration. If the test command raises, the
                # tree is still put back.
                shutil.copy2(backup, TARGET)

            if verdict == "did-not-run":
                not_run.append((name, test))
                detail = next((l for l in out.splitlines()
                               if "harness:" in l or "build failed" in l), "")
                print(f"NOT-RUN {name}\n"
                      f"        {test} never executed -- {detail.strip()[:70]}\n"
                      f"        This is NOT a kill. The mutation must keep the "
                      f"package compiling.")
            elif verdict == "passed":
                survived.append((name, test))
                print(f"SURVIVED {name}\n"
                      f"        {test} still passed -- this test does not guard "
                      f"this line.")
            else:
                killed += 1
                first = next((l for l in out.splitlines() if "--- FAIL" in l), "")
                print(f"KILLED   {name}\n"
                      f"        by {test}  [{first.strip()[:70]}]")
    finally:
        # Restoration must not depend on the loop reaching its own epilogue.
        shutil.copy2(backup, TARGET)
        # Compare byte-for-byte: a restore that cannot fail is not a restore.
        restore_broken = TARGET.read_bytes() != backup.read_bytes()
        if not restore_broken:
            print("restored task_clean.go from snapshot (byte-for-byte verified)")

    # Outside the finally on purpose: a `return` inside a `finally` discards an
    # exception that is still propagating, which turns a crash into a quiet
    # verdict. Flag the broken restore and fall through to the normal tally.
    if restore_broken:
        print("\nFATAL: task_clean.go does NOT match the snapshot after the "
              "sweep. The tree is dirty and the restore is broken.")
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
        for name, test in survived:
            print(f"  - {name}\n    guard: {TEST}")
        print("\nA surviving mutation means the guarded line is DEAD or REDUNDANT "
              "(the next layer already does it). Delete the check; do not add a test.")
    return 1 if survived else 0


if __name__ == "__main__":
    sys.exit(main())
