#!/usr/bin/env python3
"""Find `planned` rows whose issue number is cited in a source comment -- i.e. already fixed here.

## WHY THE NAIVE VERSION OF THIS IS WRONG

The first version of this script grepped the tree for `#<number>` and reported three planned rows as
already-done. All three were FALSE POSITIVES, and each for a different reason:

    #13    "// NON-NEGOTIABLE #13 AND §6b.4"  -- a rule reference, not the issue
    #398   "// #3986 - migrate scene marker files" -- a DIFFERENT issue, four digits
    #7135  a genuine match, found only because the digit boundary is enforced here

So a number appearing in a comment is not evidence that the issue was addressed: the same digits
can be a section reference, a longer issue number, or a substring of unrelated text. What makes a
citation real is that it names THIS issue and carries the vocabulary of a fix -- "stash#NNNN", a
word-boundary match, and a verb describing what changed.

## WHY IT MATTERS ANYWAY

A row marked `planned` whose fix is already in the tree is the most expensive kind of stale row:
it looks like work to do, so it is counted, re-read, and re-debated. Four such rows turned up by
accident this session (#7238's neighbours #7240, #7263, #7256, #7229 -- all already fixed, all
correctly filed in Resolved, none of them planned). So they were not in the planned set at all.

The point of this script is not to close rows. It is to answer one question cheaply and honestly:
how much of the planned set is already done in this tree? The answer decides whether C2 is a
decision-making exercise or a testing exercise, and those want very different amounts of effort.

    python3 docs/find_fixed_planned.py            # report
    python3 docs/find_fixed_planned.py --verbose  # show the citing lines
"""

import argparse
import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parents[1]
ROSTER = REPO / "docs" / "UPSTREAM-ISSUES.md"
SOURCE_DIRS = ["internal/", "pkg/", "ui/v2.5/src/"]

# Verbs that indicate the citing line is describing a CHANGE rather than mentioning the issue.
FIX_VERBS = re.compile(
    r"\b(fix|fixed|fixes|guard|guards|prevent|prevents|refuse|refuses|reject|rejects|"
    r"enforce|enforces|now|added|add|use|uses|instead|rather|no longer|never|"
    r"must|cannot|can not|only)\b",
    re.I,
)


def planned_rows():
    out = []
    for line in ROSTER.read_text().splitlines():
        m = re.match(r"^\|\s*(\d+)\s*\|", line)
        if not m:
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) >= 4 and cells[-1] == "planned":
            out.append((int(m.group(1)), cells[1]))
    return out


def citations(number):
    """(file, line, text) for lines that plausibly cite THIS issue number."""
    # The boundary is the whole point: `#398` must not match `#3986`, and `#13` must not match
    # `#130`.
    #
    # `(?!\d)` would be the natural way to write that and it is WRONG here: this is passed to
    # `grep -E`, which is POSIX ERE and has NO lookahead. It does not error -- it prints
    # "grep: warning: ? at start of expression" to stderr and matches NOTHING, so every row came
    # back as zero citations. A silently-empty result is the worst failure mode for a tool whose
    # whole job is to tell you what exists.
    #
    # `([^0-9]|$)` is the portable equivalent: it requires a non-digit or end-of-line after the
    # number. Checked both directions -- `#72401` does not match `#7240`, and `#7240:` does.
    pattern = re.compile(rf"(?:stash)?#{number}([^0-9]|$)")
    try:
        r = subprocess.run(
            ["grep", "-rnE", "--include=*.go", "--include=*.ts", "--include=*.tsx",
             pattern.pattern] + SOURCE_DIRS,
            capture_output=True, text=True, cwd=REPO, timeout=300,
        )
    except subprocess.TimeoutExpired:
        print(f"  !! grep timed out for #{number}", file=sys.stderr)
        return []

    hits = []
    for raw in r.stdout.splitlines():
        # grep -rn output is `path:line:content`; a path may itself contain a colon only on Windows,
        # which this repo does not target, so split from the right twice.
        parts = raw.split(":", 2)
        if len(parts) < 3:
            continue
        path, lineno, text = parts
        if FIX_VERBS.search(text):
            hits.append((path, lineno, text.strip()))
    return hits


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--verbose", action="store_true", help="print the citing lines")
    args = ap.parse_args()

    planned = planned_rows()
    print(f"planned rows: {len(planned)}")

    found = {}
    for num, title in planned:
        hits = citations(num)
        if hits:
            found[num] = (title, hits)

    print(f"rows whose number is cited in source with fix vocabulary: {len(found)}\n")
    for num, (title, hits) in sorted(found.items()):
        tests = [h for h in hits if "_test.go" in h[0] or ".test." in h[0]]
        print(f"  #{num}  {title[:60]}")
        print(f"      {len(hits)} citing line(s), {len(tests)} in test file(s)")
        if args.verbose:
            for path, lineno, text in hits[:4]:
                print(f"      {path}:{lineno}  {text[:100]}")

    if not found:
        print("  (none -- the planned set is genuinely unfixed in this tree, so C2 is a")
        print("   decision-making exercise rather than a testing one)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
