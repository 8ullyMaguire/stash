#!/usr/bin/env python3
"""Mutation check for the clustering pass.

Every mutation below removes or inverts one rule in internal/cluster/pass.go and
reports whether any test in the package fails. A mutation that SURVIVES is a
rule with no test, which is reported as such rather than quietly ignored -- the
plan's rule is that a guard which kills no mutant is not a guard.

Run from the repository root:  python3 internal/cluster/mutate_pass.py
"""

import os
import re
import shutil
import subprocess
import sys
import tempfile

PASS = "internal/cluster/pass.go"


def replace_once(text, old, new):
    """Replace `old` with `new`, or raise if it is not present exactly once.

    The count matters. A mutation that silently does not apply produces a
    surviving mutant, and a surviving mutant read as "the test is weak" when the
    truth is "the mutation never happened" -- which is the exact mistake the
    plan records about a load-ordering mutation that deleted a comment instead
    of moving a block.
    """
    n = text.count(old)
    if n != 1:
        raise SystemExit(
            "mutation target appears %d times, expected exactly 1:\n%s" % (n, old)
        )
    return text.replace(old, new)


# Each entry: (name, old, new).
MUTATIONS = [
    (
        "M1 filter stops skipping unusable embeddings",
        "\t\tif err := p.measurable(f); err != nil {\n"
        "\t\t\tres.Unembeddable++\n\t\t\tcontinue\n\t\t}",
        "",
    ),
    (
        "M2 ambiguous faces persisted like joins",
        "\t\tif assignment.Kind == AssignAmbiguous {\n\t\t\tcontinue\n\t\t}",
        "",
    ),
    (
        "M3 no state written for a new cluster",
        "\tif err := p.store.SetState(ctx, id, stateSingleton); err != nil {\n"
        '\t\treturn 0, fmt.Errorf("marking cluster %d as a singleton: %w", id, err)\n'
        "\t}",
        "",
    ),
    (
        "M4 per-frame cap keyed on target only",
        'bucket := fmt.Sprintf("%s:%d/%d", f.TargetType, f.TargetID, f.FrameIndex)',
        'bucket := fmt.Sprintf("%s:%d", f.TargetType, f.TargetID)',
    ),
    (
        "M5 nil store accepted",
        '\tif store == nil {\n\t\treturn nil, fmt.Errorf("the clustering pass '
        'needs a store; without one " +\n\t\t\t"it computes clusters and discards '
        'them, which is the shape of a " +\n\t\t\t"subsystem that appears to work")\n\t}',
        "\t_ = store",
    ),
    (
        "M6 geometry defaults to scalar, not cosine",
        "\t\tgeom = CosineGeometry{}",
        "\t\tgeom = ScalarGeometry{}",
    ),
    (
        "M7 candidate-k validation removed",
        "\tif c.CandidateK < 2 {",
        "\tif false {",
    ),
    (
        "M8 separation range check removed",
        "\tif c.Separation < 0 || c.Separation > 1 {",
        "\tif false {",
    ),
    (
        "M9 threshold range check removed",
        "\tif c.Threshold <= 0 || c.Threshold > 1 {",
        "\tif false {",
    ),
    (
        "M10 embedding stored as opaque bytes rather than the vector",
        "\t\tblob, err := MarshalEmbedding(f.Vector)",
        '\t\tblob, err := []byte("x"), error(nil)',
    ),
    (
        "M11 merge failures silently ignored",
        "\t\tsurvivor, err := p.store.MergeCluster(ctx, winner, loser)\n"
        "\t\tif err != nil {\n"
        '\t\t\treturn res, fmt.Errorf("merging cluster %d into %d: %w", loser, winner, err)\n'
        "\t\t}",
        "\t\tsurvivor, _ := p.store.MergeCluster(ctx, winner, loser)",
    ),
    (
        "M12 stored-id map not chained, so a second merge lands on a dead id",
        "\t\tstored[m.WinnerID] = survivor\n\t\tstored[m.LoserID] = survivor",
        "",
    ),
]


def run_tests():
    out = subprocess.run(
        ["go", "test", "./internal/cluster/", "-count=1"],
        capture_output=True,
        text=True,
        env=dict(os.environ, GOFLAGS="-mod=mod"),
    )
    return out.stdout + out.stderr


def failing_tests(output):
    return len(re.findall(r"^(?:--- FAIL|\s+--- FAIL)", output, re.M))


# Mutations that are EXPECTED to survive, with the reason.
#
# These are not untested rules. They sit on the merge-persistence path, and no
# merge can reach it: the assign stage keeps two clusters apart precisely when
# the loser's nearest member is farther than the join threshold from the
# winner's centroid, and absorb merges them only when every loser member is
# CLOSER than that same threshold from that same centroid. The two conditions
# are the same inequality negated, so the path is unreachable by construction.
#
# That is a property of the two stages, not of the pass, and it is pinned by
# TestPassConsolidateCannotMergeWhatAssignSeparated, which asserts the
# constraint rather than working around it. These two survivors become killable
# the moment absorb stops re-checking merges with the join threshold -- which is
# the change a future fix would make, and which that test will then fail on, so
# the signal is not lost.
EXPECTED_SURVIVORS = {
    "M11 merge failures silently ignored":
        "merge PERSISTENCE, not absorb. absorb itself merges readily on planted "
        "clusters (77 of 80, see merge_reachability_test.go) but a real pass "
        "merged in 0 of 100 shape x threshold combinations: assign keeps every "
        "pair apart, and the only clusters absorb sees are the ones assign made. "
        "So this code is unreachable FROM THE PASS. A pass-level test that "
        "merged would make it live, and TestAPassNeverMergesWhatAssignSeparated "
        "is what would say so.",
    "M12 stored-id map not chained, so a second merge lands on a dead id":
        "same reason as M11 -- the chained stored-id map is only read on a merge "
        "that the pass never performs.",
}


def main():
    original = open(PASS).read()
    backup = tempfile.mktemp(suffix=".go")
    shutil.copy(PASS, backup)

    # Sanity: the suite must be green before any mutation, or every later result
    # is meaningless.
    base = run_tests()
    if failing_tests(base):
        print("the suite is already red; refusing to mutate\n" + base)
        return 1

    killed, survived = [], []
    try:
        for name, old, new in MUTATIONS:
            try:
                mutated = replace_once(original, old, new)
            except SystemExit as e:
                print("SKIPPED  %s -- %s" % (name, e))
                survived.append(name + " (target not found)")
                continue

            with open(PASS, "w") as fh:
                fh.write(mutated)

            out = run_tests()
            n = failing_tests(out)
            shutil.copy(backup, PASS)

            if n:
                print("KILLED   %-58s %d failing" % (name, n))
                killed.append(name)
            else:
                print("SURVIVED %-58s <-- no test sees this rule" % name)
                survived.append(name)
    finally:
        shutil.copy(backup, PASS)
        os.unlink(backup)

    print("\n%d killed, %d survived, of %d" % (len(killed), len(survived), len(MUTATIONS)))
    if survived:
        print("survivors:")
        for s in survived:
            reason = EXPECTED_SURVIVORS.get(s)
            if reason:
                print("  - %s\n      expected: %s" % (s, reason))
            else:
                print("  - %s\n      UNEXPECTED: a rule with no test" % s)
    return 0


if __name__ == "__main__":
    sys.exit(main())
