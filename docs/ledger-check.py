#!/usr/bin/env python3
"""Structural check on docs/UPSTREAM-ISSUES.md.

The ledger is a markdown table that other tooling reads by cell index. It has been corrupted that way
before: #2773 carried a reason containing Go code (`updated := ...`), and an unescaped '|' inside a table
cell split the row into 8 cells. Cell [4] was empty and the detached tail sat in cell [5], so anything
indexing the status column read that tail as the status. This is invisible when reading by eye and
silent in every other test, because no test parsed the file.

So: every issue row must have exactly 6 cells (empty, id, title, reason, status, empty), and the status
must be one of the known values. Exits non-zero on any violation.

Run from the repo root:  python3 docs/ledger-check.py
"""
import pathlib
import sys

# The verdict values actually in use in the table. Derived from the data rather than invented: an
# earlier version of this script listed "wontfix"/"duplicate"/"invalid", which appear nowhere, and
# omitted "done", which 12 rows use. A checker that fails on correct input is worse than no checker.
#
#   not-planned  upstream will not take it, or it is not worth taking
#   deferred     a named blocker; the reason cell must say which
#   closed       resolved without a change here, or fixed upstream
#   built        implemented in this fork
#   done         deliberately declined, with the reasoning recorded in the reason cell
VALID_STATUSES = {
    "not-planned",
    "deferred",
    "closed",
    "built",
    "done",
}


def main() -> int:
    path = pathlib.Path("docs/UPSTREAM-ISSUES.md")
    if not path.exists():
        print(f"FAIL: {path} not found")
        return 1

    rows = 0
    problems = []

    for n, line in enumerate(path.read_text().splitlines(), 1):
        cells = line.split("|")
        # A row is an issue row only if cell [1] is a bare issue number.
        if len(cells) < 6 or not cells[1].strip().isdigit():
            continue

        rows += 1
        issue = cells[1].strip()

        if len(cells) != 6:
            # The classic cause is an unescaped '|' inside the reason text.
            problems.append(
                f"line {n}: issue #{issue} has {len(cells)} cells, expected 6 "
                f"(an unescaped '|' in the reason column splits the row)"
            )
            continue

        status = cells[4].strip()
        if status not in VALID_STATUSES:
            problems.append(
                f"line {n}: issue #{issue} has status {status!r}, "
                f"expected one of {sorted(VALID_STATUSES)}"
            )

    if problems:
        print(f"FAIL: {len(problems)} problem(s) in {path}")
        for p in problems:
            print(f"  {p}")
        return 1

    print(f"PASS: {rows} issue rows, all well formed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
