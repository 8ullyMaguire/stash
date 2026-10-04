#!/usr/bin/env python3
"""Fail if docs/WHATS-LEFT.md contradicts the ledgers it summarises.

WHY THIS EXISTS
===============

`docs/WHATS-LEFT.md` reported **C2 FAIL (69 planned rows)** and **C8 FAIL (1 of 17
remaining)** on 2026-10-02. Both were true when measured. Both were resolved afterwards —
C2 in `621ceca54`, C8 as the remaining rows landed — and the file was never re-read, so it
sat wrong for two days while every gate stayed green.

Nothing caught it, and the reason is structural: `goal-check.py` computes its clauses from
`docs/ISSUES.md`, `docs/UPSTREAM-ISSUES.md` and `docs/requirements.csv`. `verify-all.sh`
runs the checkers. **No gate asserts that any prose summary agrees with them.** A document
that restates a conclusion is a second copy of a fact with no gate on it, and it will drift
the moment the fact moves.

This closes that for the one file whose whole purpose is to summarise the ledgers. It is
deliberately narrow: it checks the NUMBERS and the VERDICTS, not the prose, because a checker
that tries to judge English fails on rewording and gets switched off.

WHAT IT CHECKS
==============

  1. the clause table's verdicts match what `goal-check.py` computes, per clause
  2. the counts the file states for the ledgers match the ledgers themselves
  3. the C8 done/skipped split matches `docs/ISSUES.md`
  4. the file does not claim a clause is FAIL when the goal is complete
  5. the HEAD commit hash the file states is the repository's actual HEAD
  6. the row counts stated for the supporting gates match those gates' real output

Checks 5 and 6 exist because of the staleness this file was corrected for on 2026-10-04: it
carried `HEAD 12e4f1db1` four commits behind and "46 log rows" when the ledger held 48. Neither
is a clause, so checks 1-4 could not see either -- a document that restates a conclusion is a
second copy of a fact with no gate on it.

Run:  python3 docs/check-whats-left.py     (exit 0 = agrees)
"""
import csv
import re
import subprocess
import sys
from pathlib import Path

DOCS = Path(__file__).resolve().parent
REPO = DOCS.parent
FILE = DOCS / "WHATS-LEFT.md"
ROSTER = DOCS / "UPSTREAM-ISSUES.md"
ISSUES = DOCS / "ISSUES.md"
LEDGER = DOCS / "closed-issues.md"
REQS = DOCS / "requirements.csv"


def verdict_rows(text):
    """The clause table: rows whose first cell is a clause id like `C1 PRs decided`."""
    out = {}
    for line in text.splitlines():
        m = re.match(r"^\|\s*\*{0,2}(C\d)\s+([^|*]+?)\s*\*{0,2}\s*\|\s*(PASS|FAIL|UNKNOWN)\s*\|", line)
        if m:
            out[m.group(1)] = (m.group(2).strip(), m.group(3))
    return out


def main() -> int:
    if not FILE.exists():
        print(f"FAIL: {FILE} not found")
        return 1
    text = FILE.read_text()
    problems = []

    # --- 1 & 4: the clause table against goal-check itself --------------------
    # goal-check.py is the authority. Run it, do not re-implement its clauses here: a second
    # implementation of the same rules is a second thing to keep honest, which is how the
    # cross-ledger drift started.
    try:
        proc = subprocess.run([sys.executable, str(DOCS / "goal-check.py")],
                              capture_output=True, text=True, timeout=1800, cwd=str(REPO))
    except subprocess.TimeoutExpired:
        print("FAIL: goal-check.py timed out -- cannot compare, and a timeout reads like a pass")
        return 1

    authoritative = {}
    for line in proc.stdout.splitlines():
        m = re.match(r"^(C\d)\s+(.+?)\s+\[\s*(PASS|FAIL|UNKNOWN)\s*\]", line)
        if m:
            authoritative.setdefault(m.group(1), m.group(3))

    claimed = verdict_rows(text)
    if not claimed:
        problems.append("no clause table found -- the verdict rows are what this file exists to state")

    for clause, want in sorted(authoritative.items()):
        if clause not in claimed:
            problems.append(f"{clause} is missing from the table (goal-check says {want})")
            continue
        got = claimed[clause][1]
        if got != want:
            problems.append(f"{clause}: this file says {got}, goal-check.py says {want}")

    for clause, (_, got) in sorted(claimed.items()):
        if clause not in authoritative and got == "FAIL":
            problems.append(f"{clause} is reported FAIL here but goal-check.py has no such clause")

    # --- 2: the counts the file states ----------------------------------------
    roster_text = ROSTER.read_text()
    roster_rows = [l for l in roster_text.splitlines() if re.match(r"^\| \d+ \|", l)]
    planned = sum(1 for l in roster_rows if l.rsplit("|", 2)[1].strip() == "planned")

    m = re.search(r"C2 issues dispositioned.*?no rows left `planned`", text)
    if planned == 0 and not m:
        problems.append("the roster has 0 `planned` rows but this file does not say so")

    # C5: requirements.csv counts
    if REQS.exists():
        rows = list(csv.DictReader(REQS.open()))
        counts = {}
        for r in rows:
            counts[r["status"].strip()] = counts.get(r["status"].strip(), 0) + 1
        stated = re.search(r"C5 requirements\.csv.*?(\d+) rows: (.*)", text)
        if stated:
            want_total = int(stated.group(1))
            if want_total != len(rows):
                problems.append(f"C5 says {want_total} rows, requirements.csv has {len(rows)}")
            for part in stated.group(2).split(","):
                mm = re.match(r"\s*(\d+)\s+`?(\w[\w-]*)`?", part)
                if mm:
                    n, status = int(mm.group(1)), mm.group(2)
                    if counts.get(status, 0) != n:
                        problems.append(
                            f"C5 says {n} `{status}`, requirements.csv has {counts.get(status, 0)}")

    # --- 3: the C8 done/skipped split -----------------------------------------
    if ISSUES.exists():
        states = {}
        for line in ISSUES.read_text().splitlines():
            m = re.match(r"^\|\s*(\d+)\s*\|", line)
            if m:
                cells = [c.strip() for c in re.split(r"(?<!\\)\|", line.strip().strip("|"))]
                if len(cells) >= 2:
                    states[m.group(1)] = cells[-1].strip("* ")
        done = [n for n, s in states.items() if s in ("done", "closed")]
        skipped = [n for n, s in states.items() if s == "skipped"]
        c8 = re.search(r"C8 backlog-17 ledger.*?(\d+) issues? = (\d+) done.*?(\d+) open.*?(\d+) skipped", text)
        if c8:
            if int(c8.group(2)) != len(done):
                problems.append(f"C8 says {c8.group(2)} done, docs/ISSUES.md has {len(done)}")
            if int(c8.group(4)) != len(skipped):
                problems.append(f"C8 says {c8.group(4)} skipped, docs/ISSUES.md has {len(skipped)}")
        else:
            problems.append("could not read the C8 done/skipped counts from this file")

    # --- 5. no ledger changed AFTER the measurement this file states ---------
    #
    # The first version of this check compared the stated hash to `git rev-parse HEAD`, and it was
    # WRONG in the worst way for a check: it can never pass. Any commit touching this file --
    # INCLUDING the one that updates the hash -- moves HEAD past it, so it failed on the commit that
    # introduced it and again on the commit that corrected the hash. The only available response was
    # to keep editing a number, which means a gate that is silenced by editing the file it guards.
    #
    # The property that actually matters is not "the hash equals HEAD" but "the measurement is at
    # least as new as every fact this file reports": no ledger file may have changed after the commit
    # this file was last measured against.
    #
    #     changed = git log -1 --format=%H -- docs/closed-issues.md
    #     last    = git log -1 --format=%H -- docs/WHATS-LEFT.md
    #     git merge-base --is-ancestor $changed $last    # non-zero => ledger moved after measuring
    #
    # That is drift, it is fixable by re-measuring, and a commit touching only WHATS-LEFT.md does NOT
    # trip it -- because nothing the file reports has moved.
    head = re.search(r"HEAD\s+`([0-9a-f]{7,40})`", text)
    if not head:
        problems.append("this file states no HEAD commit hash, so nothing dates its own measurement")
    else:
        stated = head.group(1)
        # The commit that last touched this file is what its numbers were measured against.
        last_touch = subprocess.run(
            ["git", "-C", str(REPO), "log", "-1", "--format=%H", "--", str(FILE)],
            capture_output=True, text=True, timeout=60,
        ).stdout.strip()
        if not last_touch:
            problems.append(
                "git cannot report the last commit touching this file, so nothing dates the "
                "measurement (is this a git checkout?)")
        else:
            for label, path in (("docs/ISSUES.md", ISSUES),
                                ("docs/UPSTREAM-ISSUES.md", ROSTER),
                                ("docs/closed-issues.md", LEDGER),
                                ("docs/requirements.csv", REQS)):
                changed = subprocess.run(
                    ["git", "-C", str(REPO), "log", "-1", "--format=%H", "--", str(path)],
                    capture_output=True, text=True, timeout=60,
                ).stdout.strip()
                if not changed:
                    continue
                ancestor = subprocess.run(
                    ["git", "-C", str(REPO), "merge-base", "--is-ancestor", changed, last_touch],
                    capture_output=True, text=True, timeout=60,
                )
                if ancestor.returncode != 0:
                    problems.append(
                        f"{label} changed AFTER this file's stated measurement "
                        f"(HEAD {stated[:9]}); re-measure, or the numbers below predate a "
                        "ledger edit")


    # --- 6. the supporting-gate row counts ------------------------------------
    #
    # The closed-log count drifted by 2 the moment #422 and #2359 were added, and checks 1-4
    # could not see it because it lives in a GATE row rather than a clause row. Reading the
    # number back out of `docs/closed-issues.md` is cheaper than trusting a prose copy.
    rows = re.findall(r"^\| stash#\d+ \|", LEDGER.read_text(), re.M)
    stated_rows = re.search(r"closed-log-check\.py`?\s*\|\s*PASS\s*[^|]*?(\d+)\s+log rows", text)
    if stated_rows:
        if int(stated_rows.group(1)) != len(rows):
            problems.append(
                f"the closed-log gate row says {stated_rows.group(1)} log rows, "
                f"docs/closed-issues.md holds {len(rows)}")
    else:
        problems.append("could not read the closed-log row count from this file")

    # --- report ---------------------------------------------------------------
    if problems:
        print(f"FAIL: {len(problems)} disagreement(s) between WHATS-LEFT.md and the ledgers")
        for p in problems:
            print(f"  - {p}")
        print("\nWHATS-LEFT.md is a summary. The ledgers are the authority: re-measure, then "
              "update the file.")
        return 1

    print(f"PASS: WHATS-LEFT.md agrees with goal-check.py "
          f"({len(authoritative)} clauses), requirements.csv and ISSUES.md")
    return 0


if __name__ == "__main__":
    sys.exit(main())
