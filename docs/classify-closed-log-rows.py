#!/usr/bin/env python3
"""Classify every malformed row in docs/closed-issues.md by its ACTUAL cause.

Written after two wrong general transforms were tried and rejected against fixtures
(docs/prove-repair-closed-log.py records both). The conclusion: this file's damage is not one
bug, it is three, and the repair is per-row and hand-verified rather than clever.

  A. UNESCAPED `|` INSIDE A CELL -- the row is ONE line and has 6+ cells.
     Cause: a literal pipe in prose or in a code span. Markdown does NOT protect pipes inside
     backticks, so `union VisualFile = VideoFile | ImageFile` splits the row.

  B. ROW TRUNCATED AND CONTINUED ONTO FOLLOWING LINES -- the row starts correctly on its own
     line, has too few cells, and the missing text continues on the next line(s) WITHOUT a
     leading pipe. 7165 is this: it was cut mid-sentence inside a quoted character list and the
     remainder became loose prose in the file body.

  C. ROW SIMPLY NEVER CLOSED -- too few cells, no continuation, content otherwise complete.
     837 and 3849 are this: correct content, missing trailing `| |`.

Reports, and does not modify anything.
"""
import re
import sys
from pathlib import Path

LOG = Path("/home/hermes/work/lane-2/stash/docs/closed-issues.md")
EXPECTED = 5


def cells(line):
    return [c.strip() for c in re.split(r"(?<!\\)\|", line.strip().strip("|"))]


def main():
    lines = LOG.read_text().split("\n")
    rows = [(i, l) for i, l in enumerate(lines) if l.startswith("| stash#")]

    a, b, c = [], [], []
    for i, l in rows:
        n = len(cells(l))
        if n == EXPECTED:
            continue
        # Is there a following non-table, non-empty line that looks like continued prose?
        nxt = ""
        for j in range(i + 1, min(i + 4, len(lines))):
            cand = lines[j]
            if not cand.strip():
                continue
            if cand.startswith("|"):
                break
            nxt = cand
            break
        if n > EXPECTED:
            a.append((i + 1, n, l))
        elif nxt and len(nxt) > 20:
            b.append((i + 1, n, l, nxt))
        else:
            c.append((i + 1, n, l))

    print(f"{len(rows)} rows total\n")
    print(f"A. unescaped '|' inside a cell ({len(a)}):")
    for ln, n, l in a:
        print(f"   line {ln}: {n} cells  {cells(l)[0]}")
    print(f"\nB. truncated, continues on the next line ({len(b)}):")
    for ln, n, l, nxt in b:
        print(f"   line {ln}: {n} cells  {cells(l)[0]}")
        print(f"      continues: {nxt[:100]}...")
    print(f"\nC. never closed ({len(c)}):")
    for ln, n, l in c:
        print(f"   line {n if False else ln}: {n} cells  {cells(l)[0]}")

    print(f"\ntotal needing repair: {len(a) + len(b) + len(c)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
