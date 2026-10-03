#!/usr/bin/env python3
"""Mutation sweep for the stash#3530 window-PRESERVATION fix.

Two mutants, and the reason both are here rather than only in a commit message: the first
version of this work had NO witness for scene-scoped deletion, and I reported it as a
survivor before discovering I had been reading a stale build. A sweep that cannot be
trusted to say "survived" is worth less than no sweep, so this one is explicit about what
each mutant must kill.

  P1  revert to the generic relatedFilesTable.replaceJoins (destroy-then-insert)
      -> kills TestTheWindowSurvivesAnExplicitFileListUpdate
      This is the ORIGINAL defect: a surviving row comes back with start/end NULL.

  P2  drop the scene_id predicate from the scene-scoped delete
      -> kills TestDetachingAFileKeepsTheOtherScenesWindow
      Added after P2 survived on the first attempt. It survived because no earlier test
      ever DETACHES a file: rename keeps the file, explicit-list keeps the file, and the
      two-scenes test edits one scene without changing its list -- so `drop` was empty in
      all of them and the delete statement never executed. A whole branch of the fix was
      untested.

Exits 1 on any survivor, any skip, or a red baseline.

    python3 docs/mutate_3530_preserve.py
"""

import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
PKG = "./pkg/sqlite/"

# The tests each mutant must kill, by name. Matched against `--- FAIL:` lines.
MUTATIONS = [
    (
        "P1: revert to the generic replaceJoins (destroy-then-insert)",
        "pkg/sqlite/scene.go",
        "if err := qb.replaceSceneFiles(ctx, updatedObject.ID, updatedObject.Files.List()); err != nil {",
        "if err := scenesFilesTableMgr.replaceJoins(ctx, updatedObject.ID, func() []models.FileID {\n"
        "\t\t\tids := make([]models.FileID, 0, len(updatedObject.Files.List()))\n"
        "\t\t\tfor _, f := range updatedObject.Files.List() {\n"
        "\t\t\t\tids = append(ids, f.ID)\n"
        "\t\t\t}\n"
        "\t\t\treturn ids\n"
        "\t\t}()); err != nil {",
        "TestTheWindowSurvivesAnExplicitFileListUpdate",
    ),
    (
        "P2: delete by file_id alone (no scene scoping)",
        "pkg/sqlite/table.go",
        "\tq := dialect.Delete(t.table.table).Where(\n"
        "\t\tt.idColumn.Eq(sceneID),\n"
        "\t\tt.table.table.Col(\"file_id\").In(fileIDs),\n"
        "\t)",
        "\tq := dialect.Delete(t.table.table).Where(\n"
        "\t\tt.table.table.Col(\"file_id\").In(fileIDs),\n"
        "\t)\n"
        "\t_ = sceneID",
        "TestDetachingAFileKeepsTheOtherScenesWindow",
    ),
]


def run(extra=None):
    cmd = ["go", "test", "-tags", "integration", "-count=1", PKG, "-run", "Window|Range"]
    if extra:
        cmd = ["go", "test", "-tags", "integration", "-count=1", PKG]
    r = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True, timeout=900)
    return r.returncode, r.stdout + r.stderr


def audit_test_filter():
    """Every preservation test must be reachable by the sweep's `-run` filter.

    Added because P2 reported SURVIVED while its witness test had never been executed: the test
    was named TestDetachingOneSceneOfAFileLeavesTheOthersAttached, which matches neither
    `Window` nor `Range`, so `-run` skipped it and the suite passed having run nothing relevant.
    A sweep's filter is part of what it proves, and a filter that silently excludes the witness
    turns the sweep into a machine that reports SURVIVED on faith.
    """
    f = REPO / "pkg/sqlite/scene_window_preserved_test.go"
    names = re.findall(r"^func (Test\w+)\(", f.read_text(), re.M)
    missing = [n for n in names if not re.search(r"Window|Range", n)]
    if missing:
        print("  !! the sweep's -run filter cannot reach: " + ", ".join(missing))
        print("     rename the test so the filter matches, or the sweep reports SURVIVED on faith")
        sys.exit(2)
    print(f"  filter reaches all {len(names)} preservation tests")


def main():
    originals = {}
    for label, path, *_ in MUTATIONS:
        if path not in originals:
            originals[path] = (REPO / path).read_text()

    audit_test_filter()

    print("=== baseline ===")
    rc, out = run()
    if rc != 0:
        print(out[-3000:])
        print("HARNESS MALFORMED: baseline is red")
        sys.exit(2)
    print("  green")

    results = []
    for label, path, anchor, repl, expect in MUTATIONS:
        original = originals[path]
        n = original.count(anchor)
        if n != 1:
            print(f"\nSKIP {label}\n  anchor occurs {n} times in {path}")
            results.append((label, "SKIP"))
            continue

        print(f"\n--- {label}")
        try:
            (REPO / path).write_text(original.replace(anchor, repl, 1))
            rc, out = run()
            if "[build failed]" in out or "declared and not used" in out:
                verdict = "SKIP"
                detail = "does not compile -- invalid mutant"
            elif rc == 0:
                verdict = "SURVIVED"
                detail = "the suite passed with the mutation applied"
            elif f"--- FAIL: {expect}" in out:
                verdict = "KILLED"
                detail = f"{expect} went red"
            else:
                verdict = "COVERED"
                detail = "the suite failed, but not by the named test"
        finally:
            (REPO / path).write_text(original)
            assert (REPO / path).read_text() == original, f"restore failed for {path}"

        print(f"  {verdict}: {detail}")
        results.append((label, verdict))

    print("\n=== summary ===")
    for label, verdict in results:
        print(f"  {verdict:<9} {label}")
    bad = [r for r in results if r[1] != "KILLED"]
    print(f"\nkilled {len(results) - len(bad)}/{len(results)}")
    if bad:
        print("\nA non-KILLED mutant is either a hole in the tests or a wrong anchor. "
              "Read the diff before touching the source.")
        sys.exit(1)
    sys.exit(0)


if __name__ == "__main__":
    main()