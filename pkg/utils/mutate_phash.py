#!/usr/bin/env python3
"""Mutation harness for the stash#2149 phash validation.

A green test proves nothing until you watch it go red. Each mutation below
breaks the fix in a way a careless future edit might plausibly break it, and
the harness asserts the tests CATCH it. A surviving mutation means the tests
are blind to that class of defect.

Both halves are covered: the distance function and the known-bad list in
pkg/utils, and the store-or-refuse DECISION in internal/manager. A validator
that is correct but never consulted would pass every test in pkg/utils, which
is why the decision tests exist at all.

Run:  python3 pkg/utils/mutate_phash.py
"""

import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
VALIDATE = ROOT / "pkg/utils/phash_validate.go"
TASK = ROOT / "internal/manager/task_generate_phash.go"

ORIG_VALIDATE = VALIDATE.read_text()
ORIG_TASK = TASK.read_text()

UTIL_RUN = [
    "TestEveryKnownBadPhashIsRejected",
    "TestKnownBadPhashIsRejectedAcrossTheReportedVariation",
    "TestAHashJustOutsideTheToleranceIsAccepted",
    "TestAnUnrelatedHashIsAccepted",
    "TestTheToleranceCannotCollideWithARealisticHash",
    "TestHammingDistanceCountsDifferingBits",
    "TestTheReportedValueRoundTripsThroughTheSameNotation",
    "TestANearMissReportsThePatternNotTheInput",
    "TestTheKnownBadListMatchesTheIssueExactly",
    "TestTheKnownBadListIsWellFormed",
    "TestKnownBadPhashesIsACopy",
    "TestPhashDistanceToAnUnparseableValueDoesNotPanic",
]
TASK_RUN = [
    "TestAKnownBadPhashIsNotStored",
    "TestAGoodPhashIsStored",
    "TestARejectedPhashYieldsNoValueToStore",
    "TestANilPhashIsRefusedRatherThanDereferenced",
    "TestAKnownBadPhashWithinTheReportedVariationIsNotStored",
    "TestAHashJustOutsideTheWindowIsStored",
    # The caller tests. Without these the harness reported survivors for
    # "the validator is never consulted" and "the ok flag is ignored": a
    # helper test cannot see its own call site being deleted.
    "TestTheTaskDoesNotStoreAKnownBadPhash",
    "TestTheTaskStoresAGoodPhash",
    "TestTheTaskCallsTheEncoderAndWritesOnlyOnSuccess",
    "TestReusingAnExistingPhashDoesNotStoreAKnownBadValue",
]


def run(pkg: str, names: list[str]) -> tuple[int, str]:
    proc = subprocess.run(
        ["go", "test", pkg, "-run", "|".join(names), "-count=1"],
        cwd=ROOT,
        capture_output=True,
        text=True,
        timeout=300,
    )
    return proc.returncode, proc.stdout + proc.stderr


def run_all() -> tuple[int, str]:
    c1, o1 = run("./pkg/utils/", UTIL_RUN)
    c2, o2 = run("./internal/manager/", TASK_RUN)
    return max(c1, c2), o1 + o2


# Each mutation: (name, path, old, new, must_kill)
MUTATIONS = [
    (
        "the validator is never consulted -- the decision is skipped",
        TASK,
        "\t\tstored, ok := storablePhash(generated)\n\t\tif !ok {\n\t\t\treturn\n\t\t}\n\t\thash = stored",
        "\t\thash = int64(*generated)",
        "the store-or-refuse tests, which exist only for this",
    ),
    (
        "the ok flag is ignored, so a refused hash is stored as zero",
        TASK,
        "\t\tstored, ok := storablePhash(generated)\n\t\tif !ok {\n\t\t\treturn\n\t\t}\n\t\thash = stored",
        "\t\tstored, _ := storablePhash(generated)\n\t\thash = stored",
        "the refuses-without-writing tests",
    ),
    (
        "the tolerance is widened, admitting hashes 4+ bits away",
        VALIDATE,
        "const badPhashTolerance = 3",
        "const badPhashTolerance = 6",
        "the just-outside-the-tolerance tests",
    ),
    (
        "the tolerance is narrowed, rejecting hashes 2-3 bits away",
        VALIDATE,
        "const badPhashTolerance = 3",
        "const badPhashTolerance = 1",
        "the reported-variation tests",
    ),
    (
        "exact match only, so the 1-3 bit variation is no longer caught",
        VALIDATE,
        "if d := HammingDistance64(phash, bad); d <= badPhashTolerance {",
        "if d := HammingDistance64(phash, bad); d == 0 {",
        "the reported-variation tests",
    ),
    (
        "the hamming distance counts equal bits instead of differing bits",
        VALIDATE,
        "return bits.OnesCount64(a ^ b)",
        "return 64 - bits.OnesCount64(a^b)",
        "the hamming distance tests",
    ),
    (
        "the hamming distance is a constant, so everything matches",
        VALIDATE,
        "return bits.OnesCount64(a ^ b)",
        "return 0",
        "the hamming distance tests",
    ),
    (
        "most of the known-bad list is dropped",
        VALIDATE,
        '\t"8080808080808080",\n',
        "",
        "the every-known-bad-is-rejected test",
    ),
    (
        "a known-bad value is transcribed with a typo",
        VALIDATE,
        '"a000000000800080",',
        '"a000000000800081",',
        "the every-known-bad-is-rejected test",
    ),
    (
        "the reported value is the input rather than the matched pattern",
        VALIDATE,
        "return true, PhashToString(int64(bad))",
        "return true, PhashToString(int64(phash))",
        "the round-trip test",
    ),
    (
        "KnownBadPhashes hands out the live slice",
        VALIDATE,
        "\tout := make([]string, len(knownBadPhashes))\n\tcopy(out, knownBadPhashes)\n\treturn out",
        "\treturn knownBadPhashes",
        "the is-a-copy test",
    ),
    (
        "an empty hex string parses as zero rather than failing",
        VALIDATE,
        '\tif s == "" {\n\t\treturn 0, fmt.Errorf("empty hex value")\n\t}',
        "",
        "the unparseable-value test",
    ),
    (
        "a nil phash is dereferenced instead of refused",
        TASK,
        "\tif generated == nil {\n\t\treturn 0, false\n\t}",
        "",
        "the nil test",
    ),
]


def main() -> int:
    print("stash#2149 -- mutation harness\n")

    code, out = run_all()
    if code != 0:
        print("FATAL: the tests fail on unmutated code.")
        print(out[-4000:])
        return 1
    print("  baseline: all tests pass\n")

    killed = 0
    survivors = []

    for name, path, old, new, why in MUTATIONS:
        original = path.read_text()
        if old not in original:
            print(f"  ERROR  {name}: anchor not found -- update the harness")
            survivors.append(name)
            continue

        path.write_text(original.replace(old, new, 1))
        try:
            code, out = run_all()
        finally:
            path.write_text(original)

        if code != 0:
            killed += 1
            failed = [
                ln.strip() for ln in out.splitlines()
                if ln.strip().startswith("--- FAIL") or "panic:" in ln
            ]
            detail = failed[0].strip()[:82] if failed else "(non-zero exit)"
            print(f"  killed  {name}")
            print(f"          by: {detail}")
            print(f"          covers: {why}")
        else:
            survivors.append(name)
            print(f"  SURVIVED  {name}  <-- the tests are blind to this")
            print(f"          expected it to be caught by: {why}")

    print(f"\n  {killed} killed, {len(survivors)} survived")
    if survivors:
        print("\n  survivors (the tests cannot detect these):")
        for s in survivors:
            print(f"    - {s}")
        return 1

    if VALIDATE.read_text() != ORIG_VALIDATE or TASK.read_text() != ORIG_TASK:
        print("\n  FATAL: a file was not restored")
        return 1
    print("  sources restored, harness clean")
    return 0


if __name__ == "__main__":
    sys.exit(main())
