#!/usr/bin/env python3
"""Probe whether stash#2293 is actually fixed, or the tests merely agree.

The issue was filed in 2019 and the code carries a `#2293` comment, so the
first question is not "is it fixed" but "would we know if it stopped being
fixed". The rune-based truncation is the fix. Each mutation below restores a
byte-based version of it -- the code as it was before the fix -- and the
table records whether any test notices.

Run from the repo root:  python3 pkg/match/mutate_pathwords.py
"""
import os
import re
import subprocess
import sys

REPO = "/home/alvaro/code-local/go/stash"
TARGET = "pkg/match/path.go"

RUNE_LINE = "\t\tret = sliceutil.AppendUnique(ret, string([]rune(w)[0:2]))"
BYTE_LINE = "\t\tret = sliceutil.AppendUnique(ret, w[0:2])"

# (id, old, new, claim)
MUTATIONS = [
    # The pre-#2293 bug, verbatim: a byte slice of a multi-byte rune.
    ("byte-slice-instead-of-rune",
     RUNE_LINE, BYTE_LINE,
     "the 2019 defect: the first two BYTES of a CJK word, which is not a name"),

    # Half-fixed: rune-aware but skips non-ASCII entirely, which is a
    # different way of making these performers invisible.
    ("skip-non-ascii-words",
     "if utf8.RuneCountInString(w) > 1 {",
     "if utf8.RuneCountInString(w) > 1 && allASCII(w) {",
     "non-ASCII words are treated as separators, so the performer is never a candidate"),

    # The single-rune filter that keeps the query tractable, removed.
    ("keep-single-rune-words",
     "if utf8.RuneCountInString(w) > 1 {",
     "if utf8.RuneCountInString(w) > 0 {",
     "single-rune words are kept, so unrelated performers become candidates"),
]

TESTS = [
    "TestANonASCIIPathFragmentSurvivesWordExtraction",
    "TestANonASCIIFragmentIsNotCorruptedByByteSlicing",
    "TestAMultiRuneNameKeepsItsLeadingRunesIntact",
    "TestNonASCIINamesBeyondCJKAlsoSurvive",
    "TestAnASCIIPrefixOfANonASCIINameStillExtracts",
    "TestSingleRuneWordsAreStillDropped",
    "TestTheRuneThresholdIsCountedInRunesNotBytes",
]


def run_tests():
    p = subprocess.run(
        ["go", "test", "./pkg/match/", "-count=1", "-v",
         "-run", "^(" + "|".join(TESTS) + ")$"],
        cwd=REPO, capture_output=True, text=True, timeout=300)
    return {m.group(1) for m in re.finditer(r"^--- FAIL: (\w+)", p.stdout, re.M)}, p.stdout


def main():
    path = os.path.join(REPO, TARGET)
    original = open(path, encoding="utf-8").read()

    baseline, _ = run_tests()
    if baseline:
        print("BASELINE IS RED: %s" % sorted(baseline))
        return 1
    print("baseline: all %d tests pass on current code" % len(TESTS))
    print("reading: stash#2293 is reported fixed. These tests say so; now do")
    print("they know it?\n")

    killed, survived, malformed = 0, [], []
    for mid, old, new, claim in MUTATIONS:
        if old not in original:
            malformed.append(mid)
            print("  MALFORMED  %-28s anchor not found" % mid)
            continue
        open(path, "w", encoding="utf-8").write(original.replace(old, new, 1))
        try:
            failed, _ = run_tests()
        finally:
            open(path, "w", encoding="utf-8").write(original)

        if failed:
            killed += 1
            print("  KILLED    %-28s --- %s" % (mid, ", ".join(sorted(failed))))
        else:
            survived.append(mid)
            print("  SURVIVED  %-28s --- nothing noticed" % mid)
        print("            %s" % claim)

    print("\n%d killed, %d survived, %d malformed" % (killed, len(survived), len(malformed)))
    return 1 if (survived or malformed) else 0


if __name__ == "__main__":
    sys.exit(main())
