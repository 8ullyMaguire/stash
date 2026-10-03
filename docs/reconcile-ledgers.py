#!/usr/bin/env python3
"""Reconcile docs/UPSTREAM-ISSUES.md against docs/ISSUES.md for rows BUILT here.

#3530 exposed the class of bug this script closes. I marked the issue done in
docs/ISSUES.md and the C2 clause kept counting it as undispositioned work,
because C2 reads docs/UPSTREAM-ISSUES.md and that row still said `planned`.
Three ledgers, and the one the gate reads was the one I had not touched.

Then `docs/check-issue-ledgers.py` rejected the corrected row for a SECOND
reason: a resolved row sitting inside a `## Planned` section is itself a
failure. So a fix has to move the row to the Resolved section AND reconcile the
header counts, which are in four places (two section headings, two prose
tallies). Missing any one leaves the checker red.

So this script does the whole reconciliation for a list of issue numbers,
because doing it by hand is exactly how two of the four places get missed:

  1. verify each issue really is built, before touching a ledger -- a commit that
     is an ancestor of `main`, named by the ISSUES.md row. This script EDITS
     LEDGERS, so it must not be the thing that decides an issue is finished.
  2. rewrite the Why column with the commit and what it built;
  3. set the verdict to `done`;
  4. MOVE the row into `## Resolved -- closed or done`, in table order;
  5. recount every verdict and rewrite the section headings and both prose
     tallies from the table, so the header cannot drift from it again.

Deliberately NOT touching docs/closed-issues.md: that log is for issues closed
UPSTREAM via PR. An issue still open upstream as a bounty and closed only in
this fork does not belong there -- the #3450/#3001 pattern.

Idempotent: re-running with the same list is a no-op, and re-running after the
rows are already in Resolved reports that rather than duplicating them.

    python3 docs/reconcile-ledgers.py 571 1580 2337 2833
    python3 docs/reconcile-ledgers.py --check      # report only, edit nothing
"""

import pathlib
import re
import subprocess
import sys

DOCS = pathlib.Path(__file__).resolve().parent
UPSTREAM = DOCS / "UPSTREAM-ISSUES.md"
FORK = DOCS / "ISSUES.md"


def split_cells(line):
    return [c.strip() for c in line.strip().strip("|").split("|")]


def table_rows(text):
    """Yield (issue_number, raw_line, section) for every roster row."""
    section = ""
    for line in text.splitlines():
        if line.startswith("## "):
            section = line
            continue
        m = re.match(r"^\|\s*(\d+)\s*\|", line)
        if m and not re.match(r"^\|\s*-{2,}", line):
            yield int(m.group(1)), line, section


def is_resolved(section):
    return "Resolved" in section


def in_main(sha):
    r = subprocess.run(
        ["git", "merge-base", "--is-ancestor", sha, "main"],
        capture_output=True,
    )
    return r.returncode == 0


def commit_for(number):
    """The commit the fork ledger names for this issue, verified reachable from main."""
    for num, line, _ in table_rows(FORK.read_text()):
        if num != number:
            continue
        m = re.search(r"`([0-9a-f]{7,40})`", line)
        if not m:
            return None, "ISSUES.md row names no commit"
        sha = m.group(1)
        if not in_main(sha):
            return None, f"{sha} is not an ancestor of main -- refusing to mark it done"
        return sha, None
    return None, "no row in docs/ISSUES.md"


def recount(text):
    counts = {}
    for _, _, _ in table_rows(text):
        pass
    for number, line, _ in table_rows(text):
        cells = split_cells(line)
        v = cells[-1].strip("* ") if cells else "?"
        counts[v] = counts.get(v, 0) + 1
    return counts


def section_counts(text):
    """Row count per `##` section, from the table.

    The section headings carry their own counts and they drift independently of
    the prose tallies: after moving four rows, `## Planned -- upstream-marked`
    said 78 while holding 76, and `## Planned -- by signal` still said 3 while
    holding 2. Both are the same bug the file's own checker exists to catch, one
    level down. So the headings are recounted from the table here rather than
    derived from the verdict totals, which cannot see which SECTION a row is in.
    """
    counts = {}
    section = None
    for line in text.splitlines():
        if line.startswith("## "):
            section = line
            counts.setdefault(section, 0)
            continue
        if section and re.match(r"^\|\s*\d+\s*\|", line):
            counts[section] += 1
    return counts


def rewrite_header(text, counts):
    """Rewrite every place a tally lives, from the table.

    `closed` in the header is the RESOLVED total -- `closed` PLUS `done` rows --
    not the closed rows alone. I first wrote it as closed-only and the checker
    reported "the summary says 35 closed, the table has 46", which is the
    difference the section heading `## Resolved -- closed or done (N)` already
    implies. The section heading and the prose tally are the same number wearing
    different clothes, and they have to agree.

    The per-section headings are recounted separately (see `section_counts`),
    because no verdict total can tell them apart.
    """
    planned = counts.get("planned", 0)
    notplanned = counts.get("not-planned", 0) + counts.get("deferred", 0)
    done = counts.get("done", 0)
    closed_only = counts.get("closed", 0)
    closed = closed_only + done
    total = sum(counts.values())

    # Section headings that carry a count in their title, recounted in place.
    for section, n in section_counts(text).items():
        if not re.search(r"\(\d+\)\s*$", section):
            continue
        stem = re.sub(r"\s*\(\d+\)\s*$", "", section)
        text = re.sub(
            "^" + re.escape(section) + "$",
            f"{stem} ({n})",
            text,
            flags=re.M,
        )

    # The `by signal` share of the work queue, read from the section itself
    # rather than assumed -- the original prose said "79 upstream-marked plus 3",
    # and hardcoding the second number wrote "plus 0" while 2 rows sat there.
    by_signal = sum(
        n for section, n in section_counts(text).items()
        if section.startswith("## Planned — by signal")
    )
    upstream_marked = planned - by_signal

    # The two prose tallies: `N planned, M not planned or deferred, K closed`.
    text = re.sub(
        r"\d+ planned, \d+ not planned(?: or deferred)?, \d+ closed",
        f"{planned} planned, {notplanned} not planned or deferred, {closed} closed",
        text,
    )
    # The work-queue sentence that names the section sizes.
    text = re.sub(
        r"The \d+ are the work queue: \d+ upstream-marked plus \d+",
        f"The {planned} are the work queue: {upstream_marked} upstream-marked plus {by_signal}",
        text,
    )
    return text, (planned, notplanned, closed, total)


def reconcile(numbers, check_only):
    text = UPSTREAM.read_text()

    # ---- verify first, edit nothing until every issue passes ----
    plan = []
    for n in numbers:
        sha, err = commit_for(n)
        if err:
            print(f"  #{n}: REFUSED -- {err}")
            return 1
        row = [l for num, l, sec in table_rows(text) if num == n]
        assert len(row) == 1, f"#{n}: {len(row)} rows in the roster, expected 1"
        already = is_resolved([s for num, _, s in table_rows(text) if num == n][0])
        print(f"  #{n}: {sha} in main; currently in "
              f"{'Resolved' if already else 'a Planned section'}")
        plan.append((n, sha, row[0], already))

    if check_only:
        print("\n--check: nothing written")
        return 0

    for n, sha, row, already in plan:
        cells = split_cells(row)
        title, verdict = cells[1], cells[-1].strip("* ")
        if verdict == "done" and already:
            print(f"  #{n}: already done and already in Resolved -- left alone")
            continue
        why = (
            f"Now DONE — built and verified in `{sha}`, in main. The fork ledger's "
            f"row is `docs/ISSUES.md`, which records what was built and the tests that "
            f"prove it. This row previously read `{verdict}` with a reason describing "
            f"the work as not built; that was the FORK ledger being right and this one "
            f"being stale, which is the direction `docs/check-issue-ledgers.py` does not "
            f"check — it reconciles the header against the table, not one roster against "
            f"the other."
        )
        new = f"| {n} | {title} | {why} | done |"
        text = text.replace(row + "\n", new + "\n", 1)
        if row + "\n" not in text:
            text = text.replace(row, new, 1)

    # ---- move every resolved row into the Resolved section, in table order ----
    lines = text.splitlines(True)
    res_start = next(i for i, l in enumerate(lines) if l.startswith("## Resolved — closed or done"))
    targets = {n for n, _, _, already in plan if not already}
    moving = []
    keep = []
    for i, l in enumerate(lines):
        m = re.match(r"^\|\s*(\d+)\s*\|", l)
        if m and int(m.group(1)) in targets and i < res_start:
            moving.append(l)
        else:
            keep.append(l)
    if moving:
        # Insert at the end of the Resolved table (last consecutive `| n |` row after res_start).
        last = max(
            i for i, l in enumerate(keep)
            if i > next(j for j, x in enumerate(keep) if x.startswith("## Resolved — closed or done"))
            and re.match(r"^\|\s*\d+\s*\|", l)
        )
        keep[last + 1 : last + 1] = moving
        lines = keep
    text = "".join(lines)

    counts = recount(text)
    text, (planned, notplanned, closed, total) = rewrite_header(text, counts)

    UPSTREAM.write_text(text)
    print(f"\nrewrote {UPSTREAM.name}: planned={planned} not-planned/deferred={notplanned} "
          f"closed={closed} done={counts.get('done', 0)} total={total}")
    return 0


def main():
    args = [a for a in sys.argv[1:] if a != "--check"]
    check_only = "--check" in sys.argv[1:]
    if not args:
        print(__doc__)
        return 2
    numbers = []
    for a in args:
        if not a.isdigit():
            print(f"not an issue number: {a}")
            return 2
        numbers.append(int(a))
    return reconcile(numbers, check_only)


if __name__ == "__main__":
    sys.exit(main())