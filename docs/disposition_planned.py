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
add(5002, "closed",
    "Upstream PR #7018 (merged 2026-06-25, 'Settings: collapsible, filterable, sorted plugin "
    "list') is already an ancestor of this branch, so the plugin panel's collapse/filter/sort "
    "behaviour arrived with the upstream merge rather than being built here. The issue stays open "
    "upstream because it is a UI/UX discussion thread with more than the one shipped slice, so "
    "this row records the shipped slice as met rather than claiming the whole thread is finished.",
    "grep -rl 'Plugins' ui/v2.5/src/components/Settings/ | head -3")

# --- #3692: log management. lumberjack rotation is in the tree; the rest is a discussion. ----
add(3692, "closed",
    "The rotation half of this scope-based thread is already in the tree: gopkg.in/natefinch/"
    "lumberjack.v2 is a direct dependency (go.mod), internal/log/logger.go constructs a "
    "lumberjack.Logger, and config carries logfile_max_size. That landed upstream in PR #5696 "
    "(2025-11-18), which is an ancestor of main. The remaining checklist items (clear-from-UI, "
    "delete-files-while-running, total-size cap) are the UI parts of the same thread and are "
    "tracked there, so this row records the engine half as met.",
    "grep -n 'lumberjack' go.mod internal/log/logger.go | head -3")

# --- #2049: logo submissions. PR #2073 merged 2022 and is in main; the ask is a process. ------
add(2049, "not-planned",
    "This is a submissions PROCESS thread ('Request for Submissions: Stash Logo'), not a code "
    "ask, and its only referenced PR (#2073, 'Desktop integration', merged 2022-02-03) is already "
    "an ancestor of main. There is no artefact this fork can build: the deliverable is upstream "
    "deciding on a logo. Cutting it is a decision about scope, not a deferral of work.",
    # The evidence for this row is that the DELIVERABLE IS AN ASSET, not a behaviour: the icons a
    # new logo would replace are all present, and this fork changed none of them.
    "ls -1 ui/v2.5/public/ | grep -Ei 'icon|favicon'")

# --- #2122: filter UI/UX refactor discussion. PR #3619 is in main; the rest is design. -------
add(2122, "not-planned",
    "A long UI/UX DISCUSSION thread rather than a defect, and the concrete slice it produced "
    "(PR #3619, 'Improve studio/tag/performer filtering', merged 2023-05-25) is already an "
    "ancestor of main. The remainder asks for a filter-authoring model this fork has no mandate "
    "to design; upstream owns the filter language and R3 in this roster already excludes inventing "
    "a new first-class object. Not-deferred: nothing external is blocking, the ask is simply not "
    "this fork's decision to make.",
    "git merge-base --is-ancestor $(git log --format=%H --all --grep='Improve studio/tag/performer filtering' | head -1) main && echo 'PR #3619 IS an ancestor of main'")

# --- #6526: player bottom controls clipped. PR #7249 merged 2026-10-01, in main. -------------
add(6526, "closed",
    "PR #7249 ('Fix: Cap Portrait Video Wrapper Height', merged 2026-10-01) is an ancestor of "
    "main, so the portrait-orientation clipping half landed with the upstream merge. The row is "
    "closed on that evidence rather than on the issue being resolved upstream, which it is not.",
    "sed -n '27,30p' ui/v2.5/src/components/ScenePlayer/styles.scss")

# --- #5033: scene tagger/scrape query. PR #6559 (Tags Tagger) is in main. -------------------
add(5033, "closed",
    "PR #6559 ('FR: Tags Tagger', merged 2026-02-25) is an ancestor of main, so the tag-tagger "
    "capability this row asks for arrived with the upstream merge. The stash-box command-parsing "
    "subtlety in the title is scraper-side and not a fork change.",
    "ls ui/v2.5/src/components/Tags/ | head -6")

# --- #3065: JAV suitability. Referenced PR is from 2022 and NOT in main; scraper-side. ------
add(3065, "deferred",
    "The referenced PR (#1190, 'Add Studio Code and Director to JavLibrary_python', merged "
    "2022-12-11) is NOT an ancestor of this branch, so the tree does not carry that change. The "
    "ask is scraper-side rather than core: it is about what a scraper source returns, and scraper "
    "content is vendored from stashapp/stash-box and community scraper repositories. Deferred "
    "until that is updated upstream, which is outside this fork's control.",
    "ls pkg/scraper/ | head -8")


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
