#!/usr/bin/env python3
"""
Recompute every count in docs/UPSTREAM-ISSUES.md from the table, in place.

## WHY THIS IS A SCRIPT AND NOT A MANUSCRIPT EDITION

The header summary, the tally paragraph and the three section headers each held a count, and all
four drifted at least once during this programme. That is four places to forget. The ledger's whole
purpose is to be checkable, so the counts are the part that must be derived rather than written.

So this edits counts ONLY. It never changes a row's verdict, and it will refuse to run if the
result disagrees with itself.

`check-issue-ledgers.py` then verifies the output. Two checkers, deliberately: this one derives,
that one validates, and neither trusts the other.

## THE TWO DEFINITIONS, AND WHY THEY DIFFER

"closed" in the summary means closed OR done -- six-plus rows use `done` as a second spelling,
and `check-issue-ledgers.py` counts both. An earlier recount here used only `closed` and briefly
made the summary disagree with the checker in the opposite direction, which is the sort of thing
that makes people stop trusting a ledger.

Section headers are counts of rows UNDER that header, which is what makes membership meaningful:
a `done` row sitting under "Planned -- the work queue" contradicts the header.
"""
from __future__ import annotations

import collections
import pathlib
import re
import sys

REPO = pathlib.Path(__file__).resolve().parents[1]
LEDGER = REPO / "docs" / "UPSTREAM-ISSUES.md"

ROW = re.compile(r"^\|\s*\d+\s*\|")


def verdict(line: str) -> str | None:
    """The state cell.

    rsplit from the RIGHT because cells contain `|` -- a Windows path, a backticked expression --
    and a positional split from the left reads the wrong column for those rows. That is not
    hypothetical: it is why `rsplit` is used in check-issue-ledgers.py too.
    """
    if not ROW.match(line):
        return None
    return line.rsplit("|", 2)[1].strip()


def main() -> int:
    lines = LEDGER.read_text().splitlines()

    counts = collections.Counter(v for v in (verdict(l) for l in lines) if v is not None)
    unknown = [v for v in counts if v not in ("planned", "not-planned", "deferred", "closed", "done")]
    if unknown:
        print(f"refusing: unrecognised verdicts {sorted(unknown)} -- fix the rows first",
              file=sys.stderr)
        return 1

    total = sum(counts.values())
    planned = counts["planned"]
    closed = counts["closed"] + counts["done"]
    other = total - planned - closed
    print(f"measured: {total} = {planned} planned, {other} not-planned/deferred, {closed} closed-or-done")

    # Section header counts, recomputed from the rows beneath each.
    sections: list[tuple[int, str]] = []
    for i, l in enumerate(lines):
        if l.startswith("## ") and not l.startswith("### "):
            n, plen = 0, i + 1
            while plen < len(lines) and not lines[plen].startswith("## "):
                if verdict(lines[plen]) is not None:
                    n += 1
                plen += 1
            sections.append((i, n))

    out = list(lines)
    for i, n in sections:
        out[i] = re.sub(r"\(\d+\)", f"({n})", out[i], count=1)

    head = (
        f"**{total} issues: {planned} planned, {other} not planned or deferred, "
        f"{closed} closed or done (see `docs/closed-issues.md`).**"
    )
    out[3] = head

    tally = (
        f"**{planned} planned, {other} not planned or deferred, {closed} closed or done, "
        f"{total} total.**"
    )
    for i, l in enumerate(out):
        if re.match(r"^\*\*\d+ planned,", l):
            out[i] = re.sub(r"^\*\*.*?\*\*", tally, l, count=1)
            break

    LEDGER.write_text("\n".join(out) + "\n")
    for i, n in sections:
        print(f"  {out[i]}")
    print("\ncounts derived; run docs/check-issue-ledgers.py to validate")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
