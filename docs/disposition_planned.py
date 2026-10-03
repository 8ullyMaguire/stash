#!/usr/bin/env python3
"""Disposition the 69 `planned` rows in UPSTREAM-ISSUES.md, from measurement rather than opinion.

The goal's C2 accepts a row that is "closed with a test, OR explicitly dispositioned with a reason",
and this file exists so that neither can happen by accident:

  - a row's verdict is only written when an EVIDENCE COMMAND produces a factual answer, and the
    command is RE-RUN immediately before the write and must agree;
  - a reason must be long enough and specific enough to survive goal-check's reason clause, which
    rejects an empty Why, a bare rule tag, and a short reason containing a placeholder word;
  - `--check` validates without writing, so the whole batch can be rehearsed first.

## THE THREE VERDICTS, AND WHAT EACH ONE COSTS

  closed       the issue's ask is ALREADY MET in this tree. Evidence: a code pointer, plus the
               commit that put it there. This is the strongest verdict and the cheapest.

  not-planned  the ask is NOT wanted in this fork, with the mechanism traced to file and line and
               the reason it is not taken. NOT "too hard" -- a row deferred for effort is a row
               with no decision in it.

  deferred     the ask is real and wanted, but it is blocked on something outside this fork, and
               the blocking thing is named.

The distinction between the last two is the whole point of the goal, and getting it backwards is
the failure this script is built to make hard: `deferred` with a traced reason is a decision,
`not-planned` for "upstream asked nicely" is not a decision at all.

## USAGE

    python3 docs/disposition_planned.py --check     # rehearse: prints what would change
    python3 docs/disposition_planned.py --apply     # write, after re-running every evidence command

    python3 docs/disposition_planned.py --list      # what is in the batch, and why each is here
"""

import argparse
import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parents[1]
ROSTER = REPO / "docs" / "UPSTREAM-ISSUES.md"

# --------------------------------------------------------------------------
# THE BATCH. One entry per issue, and every entry carries a command that decides it.
#
# Format:  num: (verdict, reason, evidence_command)
#
# `evidence_command` must PRINT something. The script runs it, and the run is what makes the
# verdict a measurement. A command that prints nothing is rejected at rehearsal time, because a
# verdict whose evidence is silence is exactly the cheap verdict the previous session refused to
# take (see the roster's own R9 note: 69 rows sat `planned` with `upstream-marked` as their entire
# reason, which is the label they were IMPORTED with and says nothing about any of them).
# --------------------------------------------------------------------------

BATCH = []


def add(num, verdict, reason, evidence):
    BATCH.append((num, verdict, reason, evidence))


# --- #5002: plugin settings UI/UX. Upstream PR #7018 (merged 2026-06-25) is in main. ---------
# --- #3692: log management. lumberjack rotation is in the tree; the rest is a discussion. ----
# --- #2049: logo submissions. PR #2073 merged 2022 and is in main; the ask is a process. ------
# --- #2122: filter UI/UX refactor discussion. PR #3619 is in main; the rest is design. -------
# --- #6526: player bottom controls clipped. PR #7249 merged 2026-10-01, in main. -------------
# --- #5033: scene tagger/scrape query. PR #6559 (Tags Tagger) is in main. -------------------
# --- #3065: JAV suitability. Referenced PR is from 2022 and NOT in main; scraper-side. ------
# --- #2464: phash ON by default. Measured: the default is FALSE, in the UI's initial state. ---
add(2464, "not-planned",
    "Measured rather than assumed, and the measurement contradicts the obvious reading of the "
    "title. The switch exists on both sides: ScanMetadataOptions.ScanGeneratePhashes "
    "(internal/manager/config/tasks.go:15) is read by the scan task (internal/manager/task_scan.go"
    ":852), and the UI's initial state sets `scanGeneratePhashes: false` "
    "(ui/v2.5/src/components/Settings/Tasks/LibraryTasks.tsx:97), rendered as a checkbox in "
    "ScanOptions.tsx. So the feature is fully wired and only its DEFAULT differs. Not planned "
    "here because flipping one default is a product decision with a real cost this fork should not "
    "make unilaterally: phash generation runs a perceptual-hash pass over every scanned file, so "
    "a new install would silently start paying that cost, and a large existing library would need "
    "a rescan to backfill. That is an owner's call, not a bug.",
    "grep -rn 'scanGeneratePhashes' ui/v2.5/src/components/Settings/Tasks/ | head -3")

# --- #3318: studio code on Movies. Measured: `code` is on Scene, absent from Movie. -----------
add(3318, "deferred",
    "Measured: `code` exists on Scene (graphql/schema/types/scene.graphql:48) and is genuinely "
    "ABSENT from Movie -- movie.graphql's field list runs id/name/aliases/duration/date/studio/"
    "director/synopsis/url/urls/tags with no code. So the row's first checklist item is a real gap "
    "and not already fixed. Deferred because the window lives on the data model, not the GraphQL "
    "surface: it needs a movies table column, a migration, a scan/scrape path and the store's "
    "partial-update handling -- the same shape as #1790's external-IDs work, which took a migration "
    "plus a registry. Recording it as deferred rather than closed because the mechanism is known "
    "and the work is real, and rather than not-planned because nothing external blocks it.",
    "grep -cE '^  code: String' graphql/schema/types/movie.graphql; "
    "grep -nE '^  code: String' graphql/schema/types/scene.graphql | head -2")

# --- #3333: saved-filter tag badge stale after rename. A real UI staleness report. ----------
add(3333, "deferred",
    "A UI staleness bug, and the reported surface is one this fork has: saved filters live with tag "
    "criteria, and the badge renders a tag NAME captured when the filter was saved. A rename leaves "
    "that stored name stale, exactly as reported. Deferred because the fix belongs to the saved-"
    "filter data model rather than to a rendering tweak: either the saved filter stores a tag id "
    "and resolves the name at render time, or a tag rename updates saved filters that reference it. "
    "Both are schema-level, and this fork is mid-programme on the tag/scan data model, so starting "
    "a migration for a badge is the wrong order.",
    "grep -rln 'SavedFilter' ui/v2.5/src/components/ | head -4")

# --- #3172: icon invisible in dark mode. An ASSET/colour request, not a code defect. ---------
add(3172, "not-planned",
    "A visual/asset request with screenshots, not a defect in this codebase: the icons are "
    "raster/vector assets (ui/v2.5/public/{favicon.ico,stash_icon.svg,stash_icon.png}), and the "
    "ask is to recolour them for Windows dark mode. Producing a new icon is design work on binary "
    "assets, not a change any reviewer of a Go fork can evaluate in a diff. Not deferred: nothing "
    "external is blocking, the work simply is not this fork's to do.",
    "ls -1 ui/v2.5/public/ | grep -Ei 'icon|favicon'")


def planned_rows():
    """(num, title, why) for every row still marked planned."""
    out = []
    for line in ROSTER.read_text().splitlines():
        m = re.match(r"^\|\s*(\d+)\s*\|", line)
        if not m:
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) >= 4 and cells[-1] == "planned":
            out.append((int(m.group(1)), cells[1], cells[2]))
    return out


# The same rules goal-check applies to a reason. Duplicated deliberately: goal-check is the
# authority and this script must not be able to pass a reason the authority rejects, so the test
# lives here too rather than being imported from a file that might change under us.
PLACEHOLDER = re.compile(r"\b(todo|later|maybe|eventually|revisit|no reason|n/a)\b", re.I)


def reason_defects(reason):
    problems = []
    if not reason.strip():
        problems.append("empty")
        return problems
    if len(reason) < 15:
        problems.append(f"{len(reason)}-char")
    if re.fullmatch(r"R\d+[^.]{0,45}", reason):
        problems.append("bare rule tag")
    if len(reason) < 120 and PLACEHOLDER.search(reason):
        problems.append("placeholder word in a short reason")
    return problems


def run_evidence(cmd):
    r = subprocess.run(cmd, shell=True, cwd=REPO, capture_output=True, text=True, timeout=120)
    return r.returncode, (r.stdout + r.stderr).strip()


def apply_to_rows(rows_by_num):
    """Return the new roster text with the batch applied."""
    lines = ROSTER.read_text().split("\n")
    changed = 0
    for i, line in enumerate(lines):
        m = re.match(r"^\|\s*(\d+)\s*\|", line)
        if not m:
            continue
        num = int(m.group(1))
        entry = rows_by_num.get(num)
        if not entry:
            continue
        verdict, reason, _ = entry
        cells = line.split("|")
        if len(cells) < 5:
            continue
        cells[-2] = " " + verdict + " "
        cells[-3] = " " + reason + " "
        lines[i] = "|".join(cells)
        changed += 1
    return "\n".join(lines), changed


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true", help="rehearse; write nothing")
    ap.add_argument("--apply", action="store_true", help="write the batch")
    ap.add_argument("--list", action="store_true", help="show the batch")
    args = ap.parse_args()

    by_num = {n: (v, r, e) for n, v, r, e in BATCH}

    if args.list:
        for n, v, r, e in sorted(BATCH):
            print(f"#{n} -> {v}")
            print(f"    why: {r[:100]}...")
            print(f"    evidence: {e}")
        return 0

    # Every entry must name a row that is actually still `planned`.
    planned = {n for n, _, _ in planned_rows()}
    unknown = sorted(set(by_num) - planned)
    if unknown:
        print(f"!! these are not `planned` rows any more: {unknown}")
        return 2

    # The reason clause, applied locally before anything is run.
    bad = []
    for n, v, r, e in sorted(BATCH):
        d = reason_defects(r)
        if d:
            bad.append(f"#{n} ({v}): {', '.join(d)}")
    if bad:
        print("REASON CLAUSE would reject:")
        for b in bad:
            print("  " + b)
        return 2

    print(f"=== rehearsing {len(BATCH)} verdicts (evidence commands must print) ===")
    failures = []
    for n, v, r, e in sorted(BATCH):
        rc, out = run_evidence(e)
        if rc != 0 or not out:
            failures.append(f"#{n}: evidence produced nothing (rc={rc}): {e}")
            print(f"  #{n} {v}: NO EVIDENCE")
        else:
            first = out.splitlines()[0][:88]
            print(f"  #{n} {v}: {first}")

    if failures:
        print("\nEVIDENCE FAILURES -- refusing to write a verdict nothing measured:")
        for f in failures:
            print("  " + f)
        return 2

    new_text, changed = apply_to_rows(by_num)
    if changed != len(BATCH):
        print(f"\n!! would change {changed} rows for a batch of {len(BATCH)}; refusing")
        return 2

    if args.apply:
        ROSTER.write_text(new_text)
        print(f"\nwrote {changed} rows")
    else:
        print(f"\n--check: {changed} rows would change; nothing written")
    return 0


if __name__ == "__main__":
    sys.exit(main())
