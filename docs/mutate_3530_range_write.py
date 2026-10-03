#!/usr/bin/env python3
"""Mutation sweep for stash#3530 SetSceneRange -- the window WRITE path.

Three mutants, each aimed at one of the ways a write can be wrong while returning success.
That combination is the whole hazard: SQLite reports an UPDATE that matched no row as success,
and an implementation that quietly omits a column behaves exactly like one that wrote it.

  W1  drop the RowsAffected check
      -> kills TestARangeOnAnUnattachedFileIsRefused
      The caller would believe it set a window on a scene that has no such file.

  W2  omit nil values from the SET clause instead of writing NULL
      -> kills TestBothNilClearsTheWindow
      The most dangerous shape: "clear the window" returns success and leaves the OLD window
      in place. An implementation that wrote 0 instead would fail LOUDLY (the CHECK refuses a
      zero-length window), so only the omit-it version is silent -- which is why this test
      reads the duration back rather than trusting the nil error.

  W3  clamp on write (clamp the end to the file's duration, as the READ path does)
      -> kills TestTheStoreDoesNotClampAWindowItIsGiven
      The deliberate division of labour: the reader clamps so hand-written SQL cannot break it,
      the writer does not, because clamping would save a number the caller never sent. If this
      dies, the two layers have swapped jobs and a read-back disagrees with the request.

Exits 1 on any survivor, any skip, or a red baseline.

    python3 docs/mutate_3530_range_write.py
"""

import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
PKG = "./pkg/sqlite/"
TARGET = "pkg/sqlite/scene.go"

MUTATIONS = [
    (
        "W1: drop the RowsAffected check",
        TARGET,
        '\tif n, err := res.RowsAffected(); err == nil && n == 0 {\n'
        '\t\treturn fmt.Errorf("file %d is not attached to scene %d, so its range cannot be set", fileID, sceneID)\n'
        '\t}',
        "\t_ = res",
        "TestARangeOnAnUnattachedFileIsRefused",
    ),
    (
        "W2: omit nil values instead of writing NULL",
        TARGET,
        'q := dialect.Update(scenesFilesJoinTable).\n'
        '\t\tSet(goqu.Record{\n'
        '\t\t\t"start_time": start,\n'
        '\t\t\t"end_time":   end,\n'
        '\t\t}).',
        "rec := goqu.Record{}\n"
        "\tif start != nil {\n"
        '\t\trec["start_time"] = start\n'
        "\t}\n"
        "\tif end != nil {\n"
        '\t\trec["end_time"] = end\n'
        "\t}\n"
        "q := dialect.Update(scenesFilesJoinTable).\n"
        "\t\tSet(rec).",
        "TestBothNilClearsTheWindow",
    ),
    (
        "W3: clamp on write",
        TARGET,
        "q := dialect.Update(scenesFilesJoinTable).\n"
        "\t\tSet(goqu.Record{\n"
        '\t\t\t"start_time": start,\n'
        '\t\t\t"end_time":   end,\n'
        "\t\t}).",
        "// MUTANT: clamp the end to a fixed ceiling, as the READ path does.\n"
        "\t//\n"
        "\t// The ceiling is 1000, NOT the file's real duration. The fixture file is 1800s and\n"
        "\t// the test writes 9999, so a ceiling chosen to match the written value clamps\n"
        "\t// nothing and the mutant survives for the WRONG reason. My first version clamped at\n"
        "\t// 9999 and did exactly that -- a mutant that tests nothing is worse than no mutant,\n"
        "\t// because it reads as a kill-shaped thing that survived.\n"
        "\tclamped := end\n"
        "\tif clamped != nil {\n"
        "\t\tv := *clamped\n"
        "\t\tif v > 1000.0 {\n"
        "\t\t\tv = 1000.0\n"
        "\t\t}\n"
        "\t\tclamped = &v\n"
        "\t}\n"
        "q := dialect.Update(scenesFilesJoinTable).\n"
        "\t\tSet(goqu.Record{\n"
        '\t\t\t"start_time": start,\n'
        '\t\t\t"end_time":   clamped,\n'
        "\t\t}).",
        "TestTheStoreDoesNotClampAWindowItIsGiven",
    ),
]


def audit_test_filter():
    """Every write-path test must be reachable by `-run "Window|Range"`."""
    f = REPO / "pkg/sqlite/scene_range_write_test.go"
    names = re.findall(r"^func (Test\w+)\(", f.read_text(), re.M)
    missing = [n for n in names if not re.search(r"Window|Range", n)]
    if missing:
        print("  !! the sweep's -run filter cannot reach: " + ", ".join(missing))
        sys.exit(2)
    print(f"  filter reaches all {len(names)} write-path tests")


def run():
    r = subprocess.run(
        ["go", "test", "-tags", "integration", "-count=1", PKG, "-run", "Window|Range"],
        cwd=REPO, capture_output=True, text=True, timeout=900,
    )
    return r.returncode, r.stdout + r.stderr


def main():
    audit_test_filter()

    original = (REPO / TARGET).read_text()

    print("=== baseline ===")
    rc, out = run()
    if rc != 0:
        print(out[-3000:])
        print("HARNESS MALFORMED: baseline is red")
        sys.exit(2)
    print("  green")

    results = []
    for label, path, anchor, repl, expect in MUTATIONS:
        print(f"\n--- {label}")
        try:
            if original.count(anchor) != 1:
                print(f"  SKIP: anchor occurs {original.count(anchor)} times")
                results.append((label, "SKIP"))
                continue
            (REPO / path).write_text(original.replace(anchor, repl, 1))
            rc, out = run()
            if "[build failed]" in out or "declared and not used" in out:
                verdict, detail = "SKIP", "does not compile -- invalid mutant"
            elif rc == 0:
                verdict, detail = "SURVIVED", "the suite passed with the mutation applied"
            elif f"--- FAIL: {expect}" in out:
                verdict, detail = "KILLED", f"{expect} went red"
            else:
                verdict, detail = "COVERED", "the suite failed, but not by the named test"
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
        sys.exit(1)
    sys.exit(0)


if __name__ == "__main__":
    main()