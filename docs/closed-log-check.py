#!/usr/bin/env python3
"""Fail if any row of docs/closed-issues.md is malformed.

WHY THIS EXISTS
===============

`docs/check-issue-ledgers.py` reads closed-issues.md to find out WHICH issues are closed, and it
extracts that with a regex (`^\\| stash#(\\d+) \\|`). It never looks at the rows' shape -- so a row
that is truncated, split by an unescaped pipe, or never closed still contributes its issue number
correctly and the cross-check passes.

`docs/ledger-check.py` does check shape, but only for `docs/UPSTREAM-ISSUES.md`, the roster.

So the log itself accumulated ten malformed rows, in a file whose own header says:

    **The honest summary of this work is "N issues closed, each with a test that fails without
    the fix."** Never "N issues fixed" with no test named -- a fix with no test is a claim.

This is the same defect class as the row-2747 truncation (found 2026-10-04, and the checker that
missed it had `len(cells) < 6` in its row-identification gate), one file over. That fix taught the
lesson this file encodes: a checker must identify a row by something that CANNOT be destroyed by the
defect it is looking for.

THE ROW-IDENTIFICATION TRAP, AGAIN
==================================

The first version of this checker identified a row by counting cells -- which is exactly the mistake
`ledger-check.py` made. Under that rule a row truncated into fewer cells "is not a row", so the
checker skips precisely the rows it exists to catch, and reports a clean count.

So: a line is a log row iff it starts with `| stash#`. Nothing else. Then its shape is judged.

THE FOUR FAILURE MODES CHECKED
==============================

  1. wrong column count -- a truncated row, a row split by a stray pipe, or one never closed
  2. an unescaped `|` inside a cell that still happens to parse (caught by requiring the LAST cell
     to be a plausible commit: a hash, a dash, or a short phrase)
  3. an empty commit cell -- every row's whole purpose is to name the fix's proof
  4. a row id that appears twice -- two rows for one issue means one of them is wrong

Exit 0 = every row is well formed. Non-zero = the problems are listed.
"""
import re
import sys
from collections import Counter
from pathlib import Path

LOG = Path(__file__).resolve().parent / "closed-issues.md"
EXPECTED_COLUMNS = 5

# A commit cell: a backticked 7-40 char hex hash, possibly several; or an explicit dash/phrase for
# "no commit" (upstream fixed it, or nothing changed). Anything else in that column means a row was
# split and a piece of prose landed there.
#
# Built from parts and joined, because the one-line form had an unbalanced group: the trailing
# alternative `|`?[^|\n]{0,60}`?)$` closed a group that was never opened, and the checker died with
# a regex error on BOTH the good and the broken file -- which reads as "the checker fails" when it
# means "the checker does not run". A gate that cannot start is not a gate.
COMMIT_OK = re.compile(
    r"^(?:"
    r"`[0-9a-f]{7,40}`(?:\s*`[0-9a-f]{7,40}`)*"   # one or more backticked hashes
    r"|\u2014[^\n]*"                              # an em-dash explanation ("— (no code change)")
    r"|pending commit"
    r"|see commits? above"
    r"|[^|\n]{0,60}"                              # a short phrase, e.g. "upstream `7018`"
    r")$"
)


def cells(line):
    """Split a table row, honouring backslash-escaped pipes."""
    return [c.strip() for c in re.split(r"(?<!\\)\|", line.strip().strip("|"))]


def main() -> int:
    if not LOG.exists():
        print(f"FAIL: {LOG} not found")
        return 1

    lines = LOG.read_text().splitlines()

    # A row is a line starting with the issue-id prefix. No cell-count precondition -- see the
    # module docstring; requiring a minimum cell count hides exactly the rows worth reporting.
    rows = []
    for n, line in enumerate(lines, 1):
        if not line.startswith("| stash#"):
            continue
        rows.append((n, line))

    if not rows:
        print(f"FAIL: {LOG} holds no `| stash#N |` rows -- is the table gone?")
        return 1

    problems = []
    seen = Counter()

    for n, line in rows:
        c = cells(line)
        m = re.match(r"^\|\s*(stash#\d+)\s*\|", line)
        if not m:
            problems.append(f"line {n}: starts with '| stash#' but has no parseable issue id")
            continue
        issue = m.group(1)
        seen[issue] += 1

        if len(c) != EXPECTED_COLUMNS:
            # The classic causes, in the order they actually happen here:
            #   - a literal `|` in prose or in a code span (markdown does NOT protect those)
            #   - a row truncated mid-cell, its remainder loose in the file body
            #   - a row never closed with the trailing `| |`
            problems.append(
                f"line {n}: {issue} has {len(c)} columns, expected {EXPECTED_COLUMNS} "
                f"(a literal '|' in a cell splits the row; a truncated row loses columns; "
                f"an unclosed row does too)"
            )
            continue

        if not c[0].startswith("stash#"):
            problems.append(f"line {n}: first column is {c[0]!r}, expected a stash#N id")

        commit = c[-1]
        if not commit:
            problems.append(f"line {n}: {issue} has an EMPTY commit column -- the row's whole "
                            "purpose is to name what proves the fix")

    # Duplicate ids: two rows for one issue means at least one is wrong, and a reader cannot tell
    # which. Reported separately because it is a different kind of defect from a malformed row.
    for issue, count in sorted(seen.items()):
        if count > 1:
            problems.append(f"{issue} appears in {count} rows; one issue gets one row")

    if problems:
        print(f"FAIL: {len(problems)} problem(s) in {LOG.name}")
        for p in problems:
            print(f"  - {p}")
        return 1

    print(f"PASS: {len(rows)} closed-issue rows, all {EXPECTED_COLUMNS} columns, "
          f"{len(seen)} distinct issues")
    return 0


if __name__ == "__main__":
    sys.exit(main())
