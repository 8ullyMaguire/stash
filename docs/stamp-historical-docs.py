#!/usr/bin/env python3
"""Add a HISTORICAL banner to the docs whose paths and numbers were true when written.

WHY THIS EXISTS
===============

Nine docs reference `~/code-local/go/stash` or `~/code/go/stash` or the host `gaming-pc`.
None of those paths exist on this host; the checkout is `~/work/lane-2/stash` on thinkcentre.

There are two kinds of stale, and they need opposite treatment:

  - a doc that STATES THE PRESENT (GOAL.md, WHATS-LEFT.md, ALIGNMENT.md, GOAL-UPSTREAM.md)
    must be corrected, because a reader following it lands somewhere that does not exist.
    Those were edited by hand.

  - a doc that RECORDS A SESSION or a PLAN AS IT STOOD (the HANDOFF set, the M6 set,
    WORKTREE-SETUP, ZIP-EXTRACTION-AUDIT, BASELINE, ISSUE-1790-plan) must NOT be rewritten.
    A handoff from 2026-09-30 that no longer names 2026-09-30's paths is a lie about the past,
    and editing it destroys the record of what was known then.

So this only adds a banner to the second kind, pointing at the current path. It is idempotent
and it refuses to touch a file that already has one.

Run:  python3 docs/stamp-historical-docs.py
"""
import pathlib
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
DOCS = REPO / "docs"

BANNER = """> **HISTORICAL — this file records a session or plan as it stood when written.**
> The paths below (`~/code-local/go/...`, `~/code/go/...`) and the host named as `gaming-pc`
> do **not** exist on this machine. The checkout is `~/work/lane-2/stash` on **thinkcentre**;
> a second, older pair sits at `~/work/lane-1/`. For current state read
> `docs/WHATS-LEFT.md` and run `python3 docs/goal-check.py`.
> Nothing here has been rewritten, because a record of what was known then is worth more than
> a tidy path.

"""

# doc name -> one line saying what kind of record it is
FILES = {
    "HANDOFF.md": "the cold-start summary for the session that ended 2026-10-03",
    "HANDOFF-PHASE1.md": "a phase-1 handoff from 2026-09-30",
    "HANDOFF-SPLIT.md": "the two-profile stash / stash-box split, as it stood when written",
    "M6-DECISIONS.md": "the M6 upstream-issues decision log",
    "M6-MAPPING-QUALITY.md": "the M6 issue-matrix finding",
    "WORKTREE-SETUP.md": "the worktree layout used by the M6 pass",
    "ZIP-EXTRACTION-AUDIT.md": "the four-site zip-extraction audit",
    "BASELINE.md": "the fork's baseline before any StashForge change",
    "ISSUE-1790-plan.md": "the #1790 implementation plan as written",
}


def main() -> int:
    changed, skipped, missing = [], [], []
    for name, what in sorted(FILES.items()):
        p = DOCS / name
        if not p.exists():
            missing.append(name)
            continue
        text = p.read_text()
        if text.lstrip().startswith("> **HISTORICAL"):
            skipped.append(name)
            continue
        if not text.startswith("# "):
            missing.append(f"{name} (no leading '# ' heading)")
            continue
        banner = BANNER.replace("the session that ended", what).replace(
            "the cold-start summary for the session that ended 2026-10-03", what)
        p.write_text(banner + text)
        changed.append(name)

    for n in changed:
        print(f"  bannered {n}")
    for n in skipped:
        print(f"  already bannered {n}")
    if missing:
        for n in missing:
            print(f"  SKIPPED {n}")
        return 1
    print(f"  {len(changed)} bannered, {len(skipped)} already done")
    return 0


if __name__ == "__main__":
    sys.exit(main())
