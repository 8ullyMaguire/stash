#!/usr/bin/env python3
"""Mutation probe for the stash#7212 fix.

A test that passes is evidence only if it can be shown to fail. Each mutation
below reverts ONE decision the fix makes, and the table records which test
notices. A mutation that no test kills is a hole, not a survivor to be
explained away.

Run from the repo root:  python3 pkg/match/mutate_scraped.py
"""
import os
import re
import shutil
import subprocess
import sys
import tempfile

REPO = "/home/alvaro/code-local/go/stash"
TARGET = "pkg/match/scraped.go"

# (id, the old text to find, the new text to substitute, what the mutation claims)
MUTATIONS = [
    ("revert-to-exactly-one",
     "\tif len(performers) == 0 {",
     "\tif len(performers) != 1 {",
     "the original bug: ambiguity treated as absence"),

    ("drop-the-narrowing",
     "\t\tif p.Disambiguation != nil {",
     "\t\tif false {",
     "the fix never consults the scraper's disambiguation"),

    ("narrow-but-never-assign",
     "\t\t\tif len(narrowed) == 1 {\n\t\t\t\tid := strconv.Itoa(narrowed[0].ID)\n\t\t\t\tp.StoredID = &id\n\t\t\t}",
     "\t\t\t_ = narrowed",
     "the narrowing runs but the match is discarded"),

    ("guess-the-first-candidate",
     "\t\t\tif len(narrowed) == 1 {",
     "\t\t\tif len(narrowed) >= 1 {",
     "an unresolved ambiguity is settled by picking a row"),

    ("treat-empty-as-absent-again",
     "\tif len(performers) == 0 {\n\t\t// genuinely absent -- create it",
     "\tif len(performers) >= 0 {\n\t\t// genuinely absent -- create it",
     "every name is treated as absent, so nothing ever matches"),
]

TESTS = [
    "TestAScraperDisambiguationResolvesAnAmbiguousName",
    "TestAnUnresolvableAmbiguityIsNeverSettledByGuessing",
    "TestSeveralCandidatesSharingTheDisambiguationAreStillAmbiguous",
    "TestASingleNameMatchSetsTheStoredID",
    "TestAnUnknownNameStillLeavesTheStoredIDNil",
    "TestAnAmbiguousNameDoesNotFallThroughToAnAlias",
    "TestARemoteSiteIDOutranksAnAmbiguousName",
]


def run_tests():
    """Run the guard tests. Returns the set of test names that FAILED."""
    p = subprocess.run(
        ["go", "test", "./pkg/match/", "-count=1", "-v"] +
        ["-run", "^(" + "|".join(TESTS) + ")$"],
        cwd=REPO, capture_output=True, text=True, timeout=300)
    failed = set()
    for m in re.finditer(r"^--- FAIL: (\w+)", p.stdout, re.M):
        failed.add(m.group(1))
    return failed, p.stdout + p.stderr


def main():
    src_path = os.path.join(REPO, TARGET)
    original = open(src_path, encoding="utf-8").read()
    baseline_failed, _ = run_tests()
    if baseline_failed:
        print("BASELINE IS RED -- refusing to measure. Failures: %s" % sorted(baseline_failed))
        return 1

    print("baseline: all %d guard tests pass\n" % len(TESTS))

    killed, survived, malformed = 0, [], []
    for mid, old, new, claim in MUTATIONS:
        if old not in original:
            malformed.append((mid, "anchor text not found -- the fix changed shape"))
            print("  MALFORMED  %s" % mid)
            continue
        mutated = original.replace(old, new, 1)
        if mutated == original:
            malformed.append((mid, "substitution was a no-op"))
            print("  MALFORMED  %s" % mid)
            continue
        open(src_path, "w", encoding="utf-8").write(mutated)
        try:
            failed, out = run_tests()
        finally:
            open(src_path, "w", encoding="utf-8").write(original)

        if failed:
            killed += 1
            print("  KILLED    %-30s --- %s" % (mid, ", ".join(sorted(failed))))
            print("            claim: %s" % claim)
        else:
            survived.append(mid)
            print("  SURVIVED  %-30s --- no test noticed" % mid)
            print("            claim: %s" % claim)

    print("\n%d killed, %d survived, %d malformed" % (killed, len(survived), len(malformed)))
    if survived or malformed:
        print("\nA mutation that no test kills is a hole in the test suite.")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
