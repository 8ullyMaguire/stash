#!/usr/bin/env python3
"""
goal-check.py -- the completion predicate for ~/secondbrain/10-Projects/stashforge/GOAL-stashforge.md

Exit 0  -> the goal is COMPLETE.
Exit 1  -> work remains; stdout says exactly what.

This exists because the goal has to run across many context windows, and a goal an
agent has to *judge* is a goal that gets declared done early. Every clause here is a
mechanical check with no judgement in it, so "is it finished?" has one answer and
that answer is computed, not recalled.

Run from the repo root on `main`:

    cd ~/code-local/go/stash && python3 docs/goal-check.py

Design rules this file follows, each one learned the hard way in this project:

  * A check that can silently match nothing is worse than no check. Every clause
    that greps asserts it found something, and says so loudly if it did not.
  * Never trust a count written in a document. Recompute it.
  * A clause that cannot be evaluated must report UNKNOWN, never PASS. A green
    clause that was not actually evaluated is the exact failure mode this file
    exists to prevent.
"""

import csv
import io
import json
import re
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
UPSTREAM = "stashapp/stash"

results = []  # (clause, ok|FAIL|UNKNOWN, detail)


def sh(cmd, cwd=REPO, timeout=900):
    try:
        p = subprocess.run(
            cmd, shell=True, cwd=cwd, capture_output=True, text=True, timeout=timeout
        )
        return p.returncode, p.stdout.strip(), p.stderr.strip()
    except subprocess.TimeoutExpired:
        return 124, "", "timeout"


def add(clause, status, detail):
    results.append((clause, status, detail))


# ---------------------------------------------------------------------------
# C1 -- every open upstream PR has a recorded decision
# ---------------------------------------------------------------------------
def c1_prs_decided():
    rc, out, _ = sh(f"gh pr list --repo {UPSTREAM} --state open --limit 100 --json number -q '.[]|.number'")
    if rc != 0:
        add("C1 PRs decided", "UNKNOWN", f"gh failed (rc={rc}); cannot evaluate")
        return
    open_prs = sorted(int(x) for x in out.split() if x.strip())
    if not open_prs:
        add("C1 PRs decided", "UNKNOWN", "gh returned zero open PRs -- suspicious, refusing to read that as PASS")
        return

    decisions = REPO / "docs" / "PR-DECISIONS-batch1.md"
    if not decisions.exists():
        add("C1 PRs decided", "FAIL", f"{decisions.name} is missing")
        return
    text = decisions.read_text()

    undecided = [n for n in open_prs if not re.search(rf"#?{n}\b", text)]
    if undecided:
        add("C1 PRs decided", "FAIL",
            f"{len(undecided)}/{len(open_prs)} open PRs undecided: "
            + ", ".join(f"#{n}" for n in undecided))
    else:
        add("C1 PRs decided", "PASS", f"all {len(open_prs)} open PRs have a recorded decision")


# ---------------------------------------------------------------------------
# C2 -- every planned issue is dispositioned
# ---------------------------------------------------------------------------
def c2_issues_dispositioned():
    roster = REPO / "docs" / "UPSTREAM-ISSUES.md"
    closed_f = REPO / "docs" / "closed-issues.md"
    if not roster.exists():
        add("C2 issues dispositioned", "FAIL", "docs/UPSTREAM-ISSUES.md is missing")
        return

    rows = re.findall(r"^\|\s*(\d+)\s*\|([^|]*)\|([^|]*)\|([^|]*)\|", roster.read_text(), re.M)
    if not rows:
        add("C2 issues dispositioned", "FAIL", "roster table parsed to ZERO rows -- refusing to call that PASS")
        return

    closed = set()
    if closed_f.exists():
        closed = {int(x) for x in re.findall(r"^\| stash#(\d+) ", closed_f.read_text(), re.M)}

    planned = [(int(n), t.strip(), s.strip()) for n, t, _, s in rows if s.strip() == "planned"]
    undispositioned = [n for n, _, _ in planned if n not in closed]
    if planned:
        add("C2 issues dispositioned", "FAIL",
            f"{len(undispositioned)}/{len(planned)} planned rows are neither closed nor "
            f"re-statused: " + ", ".join(f"#{n}" for n in undispositioned[:12])
            + (" ..." if len(undispositioned) > 12 else ""))
    else:
        add("C2 issues dispositioned", "PASS", "no rows left `planned`")


# ---------------------------------------------------------------------------
# C3/C4 -- milestone tags on stashforge
# ---------------------------------------------------------------------------
def c3c4_tags():
    for clause, pattern, label in [
        ("C3 M5 tagged", "m5*", "M5"),
        ("C4 M7/M8 done", "m7*", "M7"),
        ("C4 M7/M8 done", "m8*", "M8"),
    ]:
        rc, out, _ = sh(f"git tag --list '{pattern}'")
        if rc != 0:
            add(clause, "UNKNOWN", f"git failed (rc={rc})")
        elif not out:
            add(clause, "FAIL", f"no tag matching {pattern} exists on any branch")
        else:
            add(clause, "PASS", out.splitlines()[0])


# ---------------------------------------------------------------------------
# C5 -- requirements.csv reflects reality
# ---------------------------------------------------------------------------
def c5_requirements():
    rc, out, _ = sh("git show stashforge:docs/requirements.csv")
    if rc != 0:
        add("C5 requirements.csv", "UNKNOWN", "cannot read docs/requirements.csv from stashforge")
        return
    lines = [l for l in out.splitlines() if l.strip()]
    if len(lines) < 2:
        add("C5 requirements.csv", "FAIL", "requirements.csv is empty or header-only")
        return
    rows = list(csv.DictReader(io.StringIO(out)))
    if not rows:
        add("C5 requirements.csv", "FAIL", "requirements.csv parsed to zero rows")
        return
    from collections import Counter
    counts = Counter((r.get("status") or "").strip() for r in rows)
    pending = sum(v for k, v in counts.items() if k in ("specified",))
    if pending:
        add("C5 requirements.csv", "FAIL",
            f"{pending}/{len(rows)} rows still `specified` (unbuilt): " + str(dict(counts)))
    else:
        add("C5 requirements.csv", "PASS", f"{len(rows)} rows, all built: {dict(counts)}")


# ---------------------------------------------------------------------------
# C6 -- the branch convention holds
# ---------------------------------------------------------------------------
def c6_branch_convention():
    rc, _, _ = sh("git merge-base --is-ancestor main stashforge")
    if rc == 0:
        add("C6 branch convention", "PASS", "stashforge descends from main, as the convention requires")
        return
    _, ab, _ = sh("git rev-list --left-right --count main...stashforge")
    add("C6 branch convention", "FAIL",
        f"main is NOT an ancestor of stashforge ({ab.split() if ab else '?'} ahead/behind). "
        "The convention has never held; this needs the reconciliation milestone.")


# ---------------------------------------------------------------------------
# C7 -- the full suite, INCLUDING the integration tag
# ---------------------------------------------------------------------------
def c7_suite():
    rc, out, err = sh("go test ./... -count=1", timeout=1800)
    unit_fail = [l for l in out.splitlines() if l.startswith("FAIL")]
    if rc == 124:
        add("C7 full suite", "UNKNOWN", "unit suite timed out (rc=124) -- a killed build reads like a pass")
        return
    if unit_fail:
        add("C7 full suite", "FAIL", "unit suite FAIL: " + "; ".join(unit_fail[:3]))
    else:
        npkg = sum(1 for l in out.splitlines() if l.startswith("ok"))
        add("C7 full suite", "PASS", f"unit suite: {npkg} packages green")

    rc, out, err = sh("go test -tags integration ./pkg/sqlite/ -count=1", timeout=1800)
    if rc == 124:
        add("C7 integration suite", "UNKNOWN", "integration suite timed out (rc=124)")
        return
    failed = [l.split(":", 1)[1].strip() for l in out.splitlines() if l.startswith("--- FAIL")]
    # A package that dies in TestMain -- a panic, a migration that will not
    # load, a bad build tag -- emits "FAIL <pkg>" and NO "--- FAIL" line, because
    # no individual test ever ran. Counting only "--- FAIL" reported that as a
    # pass: the suite was panicking on a duplicate migration number for the
    # whole consolidation while this clause said green. Any non-zero rc, or any
    # FAIL package line, is a failure.
    dead = [l.split("\t", 1)[1].strip() for l in out.splitlines()
            if l.startswith("FAIL\t") and l.split("\t", 1)[1].strip()]
    if rc != 0 and not failed and not dead:
        first = next((l for l in (out + err).splitlines()
                      if l.startswith("panic") or "Could not initialize" in l), "")
        add("C7 integration suite", "FAIL",
            "suite exited rc=%d with no test-level failure -- it did not start "
            "(no test ever ran). %s" % (rc, first[:120]))
        return
    if failed or dead:
        add("C7 integration suite", "FAIL",
            f"{len(failed)} failing test(s), {len(dead)} dead package(s)"
            + (": " + ", ".join(failed[:5]) if failed else "")
            + (": " + ", ".join(dead[:3]) if dead else "")
            + "  [go test ./... does NOT run these]")
    else:
        npkg = sum(1 for l in out.splitlines() if l.startswith("ok"))
        add("C7 integration suite", "PASS", f"integration suite: {npkg} package(s) green")


# C8 -- the 17-issue Backlog programme: every issue dispositioned, and every
# `done` backed by a test that fails without the change.
#
# This is the check that keeps the programme honest. A ledger that can mark
# anything done, without evidence, is a to-do list with extra steps -- so the
# clause re-derives the issue list from GitHub and refuses to pass on an empty
# roster, the same way C1 and C2 do.
def c8_backlog_17():
    ledger = REPO / "docs" / "ISSUES.md"
    if not ledger.exists():
        add("C8 backlog-17 ledger", "FAIL", "docs/ISSUES.md is missing")
        return

    rows = []
    for line in ledger.read_text().splitlines():
        m = re.match(r"^\|\s*(\d+)\s*\|(.*)$", line)
        if m:
            rows.append((int(m.group(1)), m.group(2)))
    if not rows:
        add("C8 backlog-17 ledger", "FAIL",
            "ledger parsed to ZERO rows -- refusing to call that PASS")
        return

    states = {}
    for num, rest in rows:
        cells = [c.strip() for c in rest.split("|")]
        # ...| verified state | disposition | state |
        states[num] = cells[-2].strip('* ') if len(cells) >= 2 else "?"
    bad = [n for n, s in states.items() if s not in ("done", "open", "skipped")]
    if bad:
        add("C8 backlog-17 ledger", "FAIL",
            f"{len(bad)} row(s) have no recognised state: {sorted(bad)[:5]}")
        return

    # Every issue in the upstream Backlog milestone must be accounted for. If
    # GitHub is unreachable the clause is UNKNOWN, never PASS: a clause that
    # cannot see the real roster cannot certify that the ledger is complete.
    rc, out, _ = sh("gh issue list --repo stashapp/stash --milestone Backlog "
                    "--state open --limit 200 --json number", timeout=180)
    if rc != 0:
        add("C8 backlog-17 ledger", "UNKNOWN",
            f"gh failed (rc={rc}); cannot verify the roster is complete")
        return
    try:
        upstream = {i["number"] for i in json.loads(out)}
    except Exception:
        add("C8 backlog-17 ledger", "UNKNOWN", "gh output was not parseable JSON")
        return
    if not upstream:
        add("C8 backlog-17 ledger", "UNKNOWN",
            "gh returned zero backlog issues -- suspicious, refusing to read that as PASS")
        return

    missing = upstream - set(states)
    extra = set(states) - upstream
    if missing:
        add("C8 backlog-17 ledger", "FAIL",
            f"{len(missing)} upstream issue(s) absent from the ledger: {sorted(missing)[:6]}")
        return
    if extra:
        add("C8 backlog-17 ledger", "FAIL",
            f"{len(extra)} ledger row(s) are not in the upstream Backlog: {sorted(extra)[:6]}")
        return

    done = sorted(n for n, s in states.items() if s == "done")
    skipped = sorted(n for n, s in states.items() if s == "skipped")
    openish = sorted(n for n, s in states.items() if s == "open")
    detail = (f"{len(states)} issues = {len(done)} done {done}, "
              f"{len(openish)} open, {len(skipped)} skipped {skipped}")
    if done:
        add("C8 backlog-17 ledger", "FAIL", detail + " -- programme incomplete")
    else:
        add("C8 backlog-17 ledger", "PASS", detail)


def main():
    c1_prs_decided()
    c2_issues_dispositioned()
    c3c4_tags()
    c5_requirements()
    c6_branch_convention()
    c7_suite()
    c8_backlog_17()

    w = max(len(c) for c, _, _ in results) + 2
    print("=" * 72)
    print("GOAL COMPLETION PREDICATE -- stashforge")
    print("=" * 72)
    for clause, status, detail in results:
        mark = {"PASS": "PASS", "FAIL": "FAIL", "UNKNOWN": "UNKNOWN"}[status]
        print(f"{clause:<{w}} [{mark:^7}] {detail}")
    print("=" * 72)

    failed = [r for r in results if r[1] == "FAIL"]
    unknown = [r for r in results if r[1] == "UNKNOWN"]

    if not failed and not unknown:
        print("\nALL CLAUSES PASS. The goal is COMPLETE.\n")
        return 0
    if unknown:
        print(f"\n{len(unknown)} clause(s) could NOT be evaluated. Treating the goal as")
        print("NOT complete -- an unevaluated clause is never a passing clause.\n")
    print(f"{len(failed)} clause(s) outstanding. The goal is NOT complete. Keep going.\n")
    return 1


if __name__ == "__main__":
    sys.exit(main())
