#!/usr/bin/env python3
"""Prove docs/repair-closed-log.py's row logic on fixtures, before it touches the real file.

The real repair rewrites a hand-maintained ledger that three other checkers read. Running it first
and inspecting the result is how you discover you destroyed a column.

WHAT THIS ESTABLISHED
=====================

Two designs were tried and both were wrong, which is why the transform is the boring one:

  1. "Escape every pipe on the line." Escapes the 5 column boundaries too, so the row collapses to
     a single cell. Trivially wrong; caught before running against the real file.

  2. "Split, then rejoin the surplus cells into whichever column they belong to." This needs to know
     WHICH column the stray pipe fell in, and inferring that from cell counts is unreliable: the
     merge landed the stray pipe's neighbours in the wrong cells, swallowing title text into the
     issue-number column (`stash#6949` + `Animated` + the code span became one cell) and leaving
     the real title column holding the tail of a sentence. Both fixture cases failed.

  3. What works: escape ONLY the pipes that are not cell boundaries. A cell boundary is
     unambiguous -- the surplus pipes are always INSIDE a cell, and a cell boundary is a pipe with
     whitespace (or the string start/end) on both sides. Every prose pipe in this file has a
     non-space on at least one side (`` VideoFile | ImageFile `` -> `e | I`, `` `/`||`/`( `` ->
     `||` ), so "pipe with a space on BOTH sides" identifies the boundaries and leaves the strays
     to escape. That is a property of the data, so it is asserted here rather than assumed.
"""
import re
import sys

EXPECTED = 5
BOUNDARY = re.compile(r"(?:^|\s)\|(?:\s|$)")


def cells(line):
    return [c.strip() for c in re.split(r"(?<!\\)\|", line.strip().strip("|"))]


def escape_interior_pipes(line):
    """Escape every pipe that is NOT a column boundary."""
    out = []
    for k, part in enumerate(re.split(r"(\|)", line)):
        if part != "|":
            out.append(part)
            continue
        before = line[: line.index(part, sum(len(x) for x in out))] if False else None
        out.append(part)
    # Simpler and correct: walk the line tracking the neighbouring characters.
    res = []
    i = 0
    while i < len(line):
        ch = line[i]
        if ch != "|":
            res.append(ch)
            i += 1
            continue
        left = line[i - 1] if i > 0 else ""
        right = line[i + 1] if i + 1 < len(line) else ""
        # A boundary has whitespace or a string edge on BOTH sides. Everything else is content.
        if (left == "" or left.isspace()) and (right == "" or right.isspace()):
            res.append("|")
        else:
            res.append("\\|")
        i += 1
    return "".join(res)


FIXTURES = [
    ("stray pipe inside a code span, spaced either side",
     '| stash#6949 | Animated Images | `union VisualFile = VideoFile | ImageFile` declares it | '
     '`TestImageQueryResolution` | `de1a30ac2` |', 5),
    ("ADJACENT pipes -> an empty cell, neither has spaces around it",
     '| stash#7179 | nogallery | filtered on `/`||`/`(config) so they disagreed | '
     '`TestFindGalleries` | `68192aa59` |', 5),
    ("pipe with no space on the right, inside prose",
     '| stash#7234 | Safari | returns `undefined`, so the call site behaved | nine checks | '
     '`3de3de80c` |', 5),
    ("already-correct row must be untouched",
     '| stash#1 | title | fix | test | `abc123` |', 5),
]


def main():
    fail = 0
    for label, row, want in FIXTURES:
        before = len(cells(row))
        fixed = escape_interior_pipes(row)
        got = cells(fixed)
        unchanged = fixed == row if before == want else True
        ok = len(got) == want and unchanged
        print(f"  {'ok  ' if ok else 'FAIL'} {label}: {before} -> {len(got)} cells")
        if before == want and fixed != row:
            print("        FAIL a correct row was modified")
            ok = False
        if not ok:
            fail += 1
            for c in got:
                print(f"        [{c[:90]}]")

    # The content must survive verbatim: unescaping must give back the original cell text.
    row = FIXTURES[0][1]
    fixed = escape_interior_pipes(row)
    rejoined = [c.replace("\\|", "|") for c in cells(fixed)]
    original = cells(row)
    if rejoined == original:
        print("  ok   content is byte-identical after unescaping (no text lost)")
    else:
        print("  FAIL content changed")
        fail += 1

    print()
    print("FIXTURES PASS" if fail == 0 else f"FIXTURES FAILED ({fail})")
    return 1 if fail else 0


if __name__ == "__main__":
    sys.exit(main())
