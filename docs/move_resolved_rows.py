#!/usr/bin/env python3
"""Move dispositioned rows into the roster section their verdict belongs to.

`check-issue-ledgers.py` fails on a resolved row that still sits under a heading reading
`Planned`, and that is the correct rule: a section headed Planned holding a closed row is how this
roster's header drifted once already (the summary said 90 planned while the table said 84, and the
checker did not catch it -- see the Resolved section's own note).

So a verdict is not finished when the row is re-labelled; it is finished when the row is in the
section that agrees with its verdict. This moves rows and refuses to do it silently.

    python3 docs/move_resolved_rows.py --check
    python3 docs/move_resolved_rows.py --apply
"""

import argparse
import pathlib
import re
import sys

REPO = pathlib.Path(__file__).resolve().parents[1]
ROSTER = REPO / "docs" / "UPSTREAM-ISSUES.md"

RESOLVED = {"closed", "done"}
SECTION_RESOLVED = "## Resolved — closed or done"


def sections(lines):
    """Map line index -> the heading above it."""
    current = None
    out = {}
    for i, l in enumerate(lines):
        if l.startswith("## "):
            current = l.strip()
        out[i] = current
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true")
    ap.add_argument("--apply", action="store_true")
    args = ap.parse_args()

    lines = ROSTER.read_text().split("\n")
    heads = sections(lines)

    # Find the resolved section's TABLE (after its header separator), so rows are inserted into the
    # table rather than appended to prose.
    resolved_idx = next(i for i, l in enumerate(lines) if l.startswith(SECTION_RESOLVED))
    insert_at = None
    for i in range(resolved_idx, len(lines)):
        if lines[i].startswith("|---"):
            insert_at = i + 1
            break
    if insert_at is None:
        print("!! could not find the Resolved section's table")
        return 2

    # Rows that are resolved but sit under a Planned heading.
    moving = []
    for i, l in enumerate(lines):
        m = re.match(r"^\|\s*(\d+)\s*\|", l)
        if not m:
            continue
        cells = [c.strip() for c in l.strip().strip("|").split("|")]
        if len(cells) < 4:
            continue
        verdict = cells[-1]
        head = heads[i] or ""
        if verdict in RESOLVED and head.startswith("## Planned"):
            moving.append((i, m.group(1), verdict, head))

    if not moving:
        print("nothing to move: every resolved row is already in the Resolved section")
        return 0

    for i, num, verdict, head in moving:
        print(f"  #{num} ({verdict}) out of {head}")

    body = [lines[i] for i, _, _, _ in moving]
    keep = [l for i, l in enumerate(lines) if i not in {m[0] for m in moving}]

    # Re-find the insertion point in the filtered list.
    ridx = next(i for i, l in enumerate(keep) if l.startswith(SECTION_RESOLVED))
    ins = None
    for i in range(ridx, len(keep)):
        if keep[i].startswith("|---"):
            ins = i + 1
            break
    if ins is None:
        print("!! could not find the table after filtering")
        return 2

    new = keep[:ins] + body + keep[ins:]

    # Count the section headings so the "(43)" in the title can be corrected rather than left to
    # drift. A stale count in a heading is the exact defect this roster has suffered from.
    total = sum(1 for l in new if re.match(r"^\|\s*\d+\s*\|", l)
                and (lambda c: len(c) >= 4 and c[-1] in RESOLVED)([x.strip() for x in l.strip().strip("|").split("|")]))
    for i, l in enumerate(new):
        if l.startswith(SECTION_RESOLVED):
            new[i] = f"{SECTION_RESOLVED} ({total})"
            print(f"  heading count -> ({total})")

    if args.apply:
        ROSTER.write_text("\n".join(new))
        print(f"moved {len(moving)} row(s) into {SECTION_RESOLVED}")
    else:
        print(f"--check: {len(moving)} row(s) would move; nothing written")
    return 0


if __name__ == "__main__":
    sys.exit(main())
