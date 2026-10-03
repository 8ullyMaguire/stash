#!/usr/bin/env python3
"""Verify every file path cited in a dispositioned roster row actually exists.

This exists because the evidence gate checks that each row's COMMAND prints, and a command can print
successfully while the path in the REASON is wrong. Three verdicts in the first batch were caught
exactly that way -- their greps named `ui_location`, `ui/v2.5/src/core/*.ts` and a `lightbox`
component, none of which exist, and the gate rejected them only because the grep printed nothing.

That protection is incidental. A command like `ls ui/v2.5/src/components/Tags/ | head -5` prints
happily whatever directory it is pointed at, so a reason can cite a plausible-looking path that was
never checked. This closes that gap by extracting every path-shaped token out of every dispositioned
row's reason and stat-ing it.

Run after every --apply:

    python3 docs/check_cited_paths.py

Exits non-zero if any cited path is missing, so it can gate a commit the same way goal-check gates
C2.
"""
import pathlib
import re
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
ROSTER = REPO / "docs" / "UPSTREAM-ISSUES.md"
CLOSED = REPO / "docs" / "closed-issues.md"

# Only paths with a source extension. Bare directories are excluded deliberately: the reasons cite
# files and line numbers, and a directory token is usually prose ("the lightbox is
# src/hooks/Lightbox/") rather than a citable artefact.
PATH_RE = re.compile(
    r"\b((?:ui/v2\.5|internal|pkg|graphql|cmd|docs|scripts)"
    r"/[A-Za-z0-9_./-]+\.(?:tsx|ts|go|graphql|scss|py|js|sh))\b"
)
VERDICTS = ("deferred", "not-planned", "closed")


def cited_paths(text):
    out = {}
    for line in text.splitlines():
        if not line.startswith("|"):
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) < 4:
            continue
        num, verdict = cells[0], cells[3].lower()
        if verdict not in VERDICTS:
            continue
        for m in PATH_RE.finditer(cells[2]):
            out.setdefault(m.group(1), set()).add(num)
    return out


def main():
    found = cited_paths(ROSTER.read_text())
    if CLOSED.exists():
        for path, nums in cited_paths(CLOSED.read_text()).items():
            found.setdefault(path, set()).update(nums)

    if not found:
        print("no cited paths found -- refusing to call that PASS")
        return 2

    missing = []
    for path, nums in sorted(found.items()):
        if not (REPO / path).exists():
            missing.append((path, sorted(nums, key=lambda n: int(n))))

    print(f"{len(found)} distinct cited paths across dispositioned rows; {len(missing)} missing")
    for path, nums in missing:
        print(f"  MISSING {path}   cited by row(s) {', '.join('#' + n for n in nums)}")

    if missing:
        print("\nA disposition citing a file that does not exist is worse than `planned`:")
        print("`planned` admits nobody looked, while this claims someone did and was wrong.")
        return 1
    print("PATHS OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())