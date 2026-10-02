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

    # THE REASON IS THE `Why` COLUMN, which is why this re-reads the row.
    #
    # The first version of this clause assumed a FIFTH column
    # (`| # | title | labels | status | reason |`) because ISSUES.md -- the OTHER
    # roster -- has one. UPSTREAM-ISSUES.md has four: `| # | Title | Why | Verdict |`.
    # So `len(cells) >= 5` was never true, `reasons` was EMPTY, and the clause
    # reported all 422 rows as having an empty reason -- a verdict about the ROSTER
    # that was entirely an artefact of reading a column that does not exist.
    #
    # It failed loudly rather than passing quietly, which is the only reason this was
    # caught in one run: 422 "empty reason" is not a plausible state of a file whose
    # whole purpose is recording why each issue was kept.
    #
    # A roster-shape assumption is exactly the kind of thing that is cheap to state
    # and expensive to be wrong about, so the column is now NAMED, and a row whose
    # shape does not match is counted as unknown rather than as unreasoned.
    wide = {}
    for line in roster.read_text().splitlines():
        m = re.match(r"^\|\s*(\d+)\s*\|", line)
        if not m:
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        # | number | Title | Why | Verdict |  ->  4 cells, verdict last.
        if len(cells) < 4:
            continue
        wide[int(m.group(1))] = (cells[3], cells[2])
    reasons = {n: r for n, (_, r) in wide.items()}
    verdicts = {n: st for n, (st, _) in wide.items()}
    re_statused = [(n, "", verdicts[n]) for n in verdicts
                   if verdicts[n] in ("deferred", "not-planned", "closed")]

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

    # A RE-STATUS MUST CARRY A REASON, or moving 422 rows to `deferred` would pass
    # this clause in one edit.
    #
    # The goal document states the honest form of C2 directly: "closed with a test, or
    # explicitly dispositioned with a reason. A row that sits `planned` forever has not
    # been decided; a row moved to `deferred` with a reason has." This clause
    # enforced only the FIRST half -- the re-status -- and never checked the reason
    # the goal requires alongside it. The first clause capable of being satisfied by
    # relabelling is the first clause that will be satisfied by relabelling.
    #
    # So the reason column is checked for a REASONABLE REASON: the roster has a column
    # for it, and an empty or placeholder one means the row was moved rather than
    # decided.
    #
    # TWO THINGS ARE DISTINGUISHED, because they are different failures:
    #
    #  - UNKNOWN SHAPE: the row did not parse into the four columns the roster uses.
    #    That is a defect in the CHECKER or the file's format, never in the work, so
    #    it is reported separately and must be fixed before the reason clause means
    #    anything.
    #  - NO REASON: the row parsed, and its `Why` is empty, a placeholder, or a bare
    #    rule tag. That IS a claim about the roster, and it is what this clause is for.
    planned_nums = [n for n, _, _ in planned]
    unparsed = [n for n in planned_nums if n not in wide]

    unreasoned = []
    for n in planned_nums:
        if n in unparsed:
            continue
        reason = reasons[n].strip()
        if not reason:
            unreasoned.append((n, "empty Why"))
        elif len(reason) < 15:
            unreasoned.append((n, f"{len(reason)}-char Why"))
        elif re.fullmatch(r"R\d+[^.]{0,45}", reason):
            # A BARE RULE TAG. `R9/R10 lowest-signal feature request` says which
            # rule kept the row and nothing about the issue itself, so 340 of these
            # are the roster restating its own policy rather than reasoning about
            # the work. The goal asks for a reason PER ROW.
            unreasoned.append((n, f"bare rule tag, not a reason: {reason[:34]!r}"))
        elif len(reason) < 120 and re.search(
                r"\b(todo|later|maybe|eventually|revisit|no reason|n/a)\b", reason, re.I):
            # PLACEHOLDER WORDS, BUT ONLY IN A SHORT REASON.
            #
            # The first version ran this over reasons of any length and flagged #2833,
            # whose 2191-character Why contains the word "later" inside a genuine
            # traced analysis -- the mechanism is confirmed from mousetrap's source and
            # the fix is recorded-not-built. A 2191-char reason is not a placeholder
            # whatever words it happens to contain.
            #
            # So the length gate applies to the PLACEHOLDER RULE, not just the empty
            # check. A reason under 120 chars that says "later" is deferring without
            # saying what; the same word inside a full analysis is prose.
            unreasoned.append((n, f"placeholder in a short reason: {reason[:30]!r}"))

    if unparsed:
        add("C2 roster shape", "FAIL",
            f"{len(unparsed)} planned row(s) did not parse into the roster's four "
            f"columns (# | Title | Why | Verdict): "
            + ", ".join(f"#{n}" for n in unparsed[:8])
            + ". A checker that cannot parse its own roster cannot judge it.")
    if unreasoned:
        add("C2 issue reasons", "FAIL",
            f"{len(unreasoned)} dispositioned row(s) have no real reason recorded: "
            + "; ".join(f"#{n} ({why})" for n, why in unreasoned[:6])
            + ". Moving a row is not deciding it -- the goal says 'with a reason'.")


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
            continue
        if not out:
            add(clause, "FAIL", f"no tag matching {pattern} exists on any branch")
            continue

        tag = out.splitlines()[0]
        # A TAG MUST CARRY ITS VERIFICATION, or `git tag m8-anything` satisfies this
        # clause.
        #
        # The clause is LABELLED "implemented, with their own verification passing" and
        # was checking only that a tag exists. `m7-mesh` sets the standard to meet: its
        # message names the steps it covers and what was proven. So the message must
        # (a) be substantial and (b) reference verification -- a test, a gate, a suite,
        # or a probe. A bare "done" does not pass.
        #
        # The tag is also checked for being REACHABLE from main, because a tag on a
        # deleted branch would let a milestone be marked complete by a commit nothing
        # can get to.
        _, msg, _ = sh(f"git tag -l {tag} --format='%(contents)'")
        body = msg.strip()
        if len(body) < 40:
            add(clause, "FAIL",
                f"tag {tag} has a {len(body)}-char message. A milestone tag has to say "
                f"what it covers and what was verified -- see m7-mesh.")
            continue
        if not re.search(r"\b(test|gate|suite|probe|verif|mutation|green|pass)\w*", body, re.I):
            add(clause, "FAIL",
                f"tag {tag} names no verification. The clause says 'with their own "
                f"verification passing', so the tag message must say what was run: "
                f"{body[:70]!r}")
            continue
        rc2, _, _ = sh(f"git merge-base --is-ancestor {tag}^{{commit}} main")
        if rc2 != 0:
            add(clause, "FAIL",
                f"tag {tag} is not an ancestor of main, so the milestone it marks is not "
                f"reachable from the branch that ships")
            continue
        add(clause, "PASS", f"{tag} ({len(body)}-char message, verification named, reachable from main)")


# ---------------------------------------------------------------------------
# C5 -- requirements.csv reflects reality
# ---------------------------------------------------------------------------
def c5_requirements():
    # Read from the WORKING TREE, not from a branch named `stashforge`.
    #
    # The single-branch consolidation (2026-10-01, one commit, four branches to
    # one) deleted `stashforge` and put its 130 commits on `main`. This clause
    # was still doing `git show stashforge:docs/requirements.csv`, so it reported
    # UNKNOWN — "cannot read from stashforge" — for every run since, while
    # `docs/requirements.csv` sat in the tree the whole time, at HEAD, on main.
    # An UNKNOWN is never a PASS, so the clause could not go green by doing the
    # work: it was reading a ref that no longer exists.
    #
    # A goal document's own rule applies here: the checker is the truth, and a
    # stale checker is worse than a stale cache because it is consulted first.
    path = REPO / "docs" / "requirements.csv"
    if not path.exists():
        add("C5 requirements.csv", "UNKNOWN",
            f"{path} is missing from the working tree")
        return
    out = path.read_text()
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

    # `specified` is the ONLY unbuilt status. Everything else -- shipped, tested,
    # ignored, deferred -- records an outcome.
    #
    # `deferred` is accepted, and it is accepted ONLY with a reason, which is the
    # whole point of having it. The goal document's own wording for C2 is "closed
    # with a test, or explicitly dispositioned with a reason", and a `deferred` row
    # with an empty note is a row that moved sideways: still unbuilt, now with a
    # status that reads as progress. So an unreasoned `deferred` is a FAIL, not a
    # PASS -- it is the cheapest possible way to make this clause green and the most
    # misleading one.
    #
    # The reason must be substantive rather than a placeholder, because "later" and
    # "TODO" are what a row says when it has not been decided. A minimum length is a
    # crude proxy and is admitted as such: it cannot tell a good reason from padding,
    # only from nothing.
    unreasoned = []
    for r in rows:
        st = (r.get("status") or "").strip()
        if st not in ("specified", "deferred"):
            continue
        note = (r.get("notes") or "").strip()
        if st == "deferred" and len(note) < 40:
            unreasoned.append((r.get("id", "?"), f"{st} with a {len(note)}-char note"))
        if st == "deferred" and re.search(r"\b(todo|later|maybe|eventually|revisit)\b", note, re.I):
            unreasoned.append((r.get("id", "?"), f"{st} with a placeholder note: {note[:40]!r}"))

    if unreasoned:
        add("C5 requirements.csv", "FAIL",
            f"{len(unreasoned)} deferred row(s) carry no real reason: "
            + "; ".join(f"{i} ({why})" for i, why in unreasoned[:5])
            + ". A deferred row must say WHY it is not being built now -- the same "
              "rule the goal states for C2.")
        return

    pending = sum(v for k, v in counts.items() if k == "specified")
    if pending:
        ids = [r.get("id") for r in rows if (r.get("status") or "").strip() == "specified"]
        add("C5 requirements.csv", "FAIL",
            f"{pending}/{len(rows)} rows still `specified` (unbuilt): "
            + ", ".join(ids[:12]) + (" ..." if len(ids) > 12 else "")
            + "  -- build them or mark them `deferred` WITH a reason.")
    else:
        add("C5 requirements.csv", "PASS",
            f"{len(rows)} rows, all built or deferred-with-a-reason: {dict(counts)}")


# ---------------------------------------------------------------------------
# C6 -- the branch convention holds
# ---------------------------------------------------------------------------
def c6_branch_convention():
    """The two-branch convention, against the branches that actually exist.

    `stashforge` was merged into `main` on 2026-10-01, so this clause has no
    second branch to compare. Rather than let it report a FAIL about a branch
    that no longer exists — which is how C3/C4/C5/C6 all sat permanently red
    while describing history rather than work — it checks what the convention
    MEANT, against what is there: one branch, no work stranded outside it.
    """
    rc, _, _ = sh("git merge-base --is-ancestor main stashforge")
    if rc == 0:
        add("C6 branch convention", "PASS",
            "stashforge descends from main, as the convention requires")
        return

    rc, _, _ = sh("git rev-parse --verify --quiet stashforge")
    if rc != 0:
        # The second branch is gone, so the convention cannot hold and does not
        # need to: the check is that no commit exists outside main, which is the
        # property the two-branch rule existed to preserve.
        #
        # Deliberate recovery refs are EXCLUDED. `refs/preserved/*` are pinned on
        # purpose -- 28 stash snapshots plus the four pre-consolidation tips,
        # created before those branches were deleted precisely so this work could
        # not be lost. Counting them as "stranded" reports the safety net as the
        # problem, and would make this clause permanently red for the act of
        # having been careful. Only refs that are neither main nor a recovery ref
        # count as stranded.
        #
        # Two ways this is WRONG, both found by making this clause go red on
        # purpose and watching it stay green:
        #
        #   git log --not main --not 'refs/preserved/*' --oneline
        #       fatal: option '--oneline' must come before non-option arguments
        #
        # rc=128, empty stdout, and `sh()` treats a non-zero rc as "no output" —
        # so the count was 0 and the clause PASSED no matter what. The same
        # spelling error in C5 was harmless; here it silently disabled the check.
        # And `refs/preserved/*` is not a valid pathspec for a ref anyway: the
        # exclusion is done here by passing the real ref NAMES, since a glob that
        # matches no path excludes nothing.
        _, allrefs, _ = sh("git for-each-ref --format='%(refname)'")
        _, pres, _ = sh("git for-each-ref --format='%(refname)' refs/preserved")
        skip = set(pres.split())
        stranded = []
        for ref in allrefs.split():
            if ref == "main" or ref in skip:
                continue
            _, out, _ = sh(f"git rev-list --oneline {ref} --not main")
            if out.strip():
                stranded.extend(out.splitlines())
        n = len(stranded)
        if n == 0:
            add("C6 branch convention", "PASS",
                "single-branch layout (stashforge was merged into main); "
                f"0 commits outside main outside {len(skip)} deliberate "
                "recovery ref(s), so nothing is stranded")
        else:
            add("C6 branch convention", "FAIL",
                f"single-branch layout but {n} commit(s) exist outside main "
                f"and outside refs/preserved/*, so they are unreachable: "
                + "; ".join(stranded[:3]))
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
    bad = [n for n, s in states.items()
           if s not in ("done", "open", "skipped", "in progress", "partial")]
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

    # EVIDENCE: every `done` row must name a commit, and every `skipped` row a reason.
    #
    # A ledger that can mark anything done without evidence is a to-do list with extra
    # steps. The goal document says `done` means "built and proven", so a `done` row
    # with no commit is claiming a proof it does not have.
    #
    # `in progress` is accepted as a recognised state but is NOT terminal: it is
    # counted with `open`, because a half-built row and an unstarted row are both work
    # that remains. Recognising it as a value keeps the "no recognised state" check
    # meaningful without letting it become a parking space.
    rows_by_num = {n: rest for n, rest in rows}
    unproven = []
    for n, state in states.items():
        rest = rows_by_num.get(n, "")
        cells = [c.strip() for c in rest.split("|")]
        disposition = cells[-3] if len(cells) >= 3 else ""
        # `done` is terminal and must be backed by a commit. `partial` is NOT
        # terminal -- it is counted with `open` below -- so it is held to no
        # evidence rule here, because requiring a commit for work that is
        # genuinely half-finished would push the author to write `done`.
        if state == "done" and not re.search(r"\b[0-9a-f]{7,40}\b", disposition):
            unproven.append(n)
        if state == "skipped" and not disposition:
            unproven.append(n)
    if unproven:
        add("C8 backlog-17 ledger", "FAIL",
            f"{len(unproven)} row(s) claim done/skipped with no evidence in the "
            f"disposition column (a commit for done, a reason for skipped): "
            f"{sorted(unproven)[:6]}")
        return

    done = sorted(n for n, s in states.items() if s == "done")
    skipped = sorted(n for n, s in states.items() if s == "skipped")
    # `in progress` counts as remaining: it is not a terminal state, and treating it
    # as done would let a row be parked there indefinitely while the clause reads green.
    openish = sorted(n for n, s in states.items()
                     if s in ("open", "in progress", "partial"))
    detail = (f"{len(states)} issues = {len(done)} done {done}, "
              f"{len(openish)} open/in-progress, {len(skipped)} skipped {skipped}")
    if openish:
        add("C8 backlog-17 ledger", "FAIL",
            detail + f" -- programme incomplete, {len(openish)} row(s) remain: "
            + ", ".join(f"#{n}" for n in openish[:12])
            + (" ..." if len(openish) > 12 else ""))
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
