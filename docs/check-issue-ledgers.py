#!/usr/bin/env python3
"""Fail if docs/UPSTREAM-ISSUES.md and docs/closed-issues.md disagree.

Two files hold the same fact -- which issues are closed -- and while they
were maintained by hand they silently drifted. #7240 and #7152 were fixed and
written into the log while the roster still listed them as `planned`, so the
queue claimed work that was already done and the log claimed work the queue
did not know about. Nothing caught it because both files were internally
plausible.

This is the check that would have caught it. It exits non-zero on any
disagreement, so it can gate a commit.

Run:  python3 docs/check-issue-ledgers.py
"""

import re
import sys
from collections import Counter
from pathlib import Path

DOCS = Path(__file__).resolve().parent
ROSTER = DOCS / "UPSTREAM-ISSUES.md"
LOG = DOCS / "closed-issues.md"

# The header states these numbers in TWO places -- the top-of-file summary and
# the tally after the rules table -- and they drifted apart once already, so
# every occurrence is checked, not just the first. "not planned" in the prose
# covers BOTH `not-planned` and `deferred` rows, which is the pre-existing
# convention and not a discrepancy -- so the check sums them rather than
# expecting the header to name a fourth category.
# The prose says "N not planned or deferred" -- the `or deferred` is deliberate (see the note
# above) and the previous regex demanded a bare "not planned", so it matched nothing and this
# check reported "the roster has no line to check against the table" against a roster that has
# two perfectly good ones. The optional suffix is what the comment already described.
HEAD = re.compile(r"(\d+) planned, (\d+) not planned(?: or deferred)?, (\d+) closed")
HEAD_TOTAL = re.compile(
    r"(\d+) planned, (\d+) not planned(?: or deferred)?, (\d+) closed, (\d+) total")

problems = []

# A row whose verdict is `closed`/`done` must not sit inside a section headed `Planned`. It was
# the cause of the header drift above: 34 such rows were left in the `Planned` sections while
# the summary counted them as closed, and nothing reconciled the two. Section membership is
# part of the verdict's meaning here -- the section is what says "this is the work queue".
_section = None
for _ln in roster_text_pre.splitlines() if (roster_text_pre := ROSTER.read_text()) else []:
    if _ln.startswith("## "):
        _section = _ln
    elif re.match(r"^\| \d+ \|", _ln) and _ln.rsplit("|", 2)[1].strip() in ("closed", "done"):
        if _section and _section.startswith("## Planned"):
            problems.append(
                f"stash#{_ln.split('|')[1].strip()} is "
                f"{_ln.rsplit('|', 2)[1].strip()} but sits in the "
                f"{_section.splitlines()[0]!r} section; move it to the Resolved section")


roster_text = ROSTER.read_text()
log_text = LOG.read_text()

rows = [ln for ln in roster_text.splitlines() if re.match(r"^\| \d+ \|", ln)]
if not rows:
    print("FAIL: the roster has no issue rows -- is it truncated?", file=sys.stderr)
    sys.exit(1)

verdicts = Counter(ln.rsplit("|", 2)[1].strip() for ln in rows)
roster_closed = {ln.split("|")[1].strip() for ln in rows
                 if ln.rsplit("|", 2)[1].strip() == "closed"}
log_closed = set(re.findall(r"^\| stash#(\d+) \|", log_text, re.M))

actual_not = verdicts["not-planned"] + verdicts["deferred"]
# `done` is a second spelling of "no longer open" that six rows use, so the header's
# "N closed" has to count it. Excluding it is what let the summary sit at 30/37 while the
# table held 31/32 and nothing failed.
actual_closed = verdicts["closed"] + verdicts["done"]

# --- the header must match the table ------------------------------------
heads = list(HEAD.finditer(roster_text))
if not heads:
    problems.append("the roster has no 'N planned, N not planned, N closed' "
                    "line to check against the table")
else:
    said_planned, said_not, said_closed = (int(g) for g in heads[0].groups())

    if verdicts["planned"] != said_planned:
        problems.append(f"the summary says {said_planned} planned, the table "
                        f"has {verdicts['planned']}")
    if actual_not != said_not:
        problems.append(f"the summary says {said_not} not planned, the table "
                        f"has {actual_not} (not-planned {verdicts['not-planned']}"
                        f" + deferred {verdicts['deferred']})")
    if actual_closed != said_closed:
        problems.append(f"the summary says {said_closed} closed, the table "
                        f"has {actual_closed}")

# Every tally that also states a total must agree with the table AND sum
# right. These are separate lines from the summary and have drifted
# independently before.
for mt in HEAD_TOTAL.finditer(roster_text):
    p, n, cl, tot = (int(g) for g in mt.groups())
    where = "the tally after the rules table"
    if (p, n, cl) != (verdicts["planned"], actual_not, actual_closed):
        problems.append(f"{where} says {p}/{n}/{cl}, the table has "
                        f"{verdicts['planned']}/{actual_not}/{actual_closed}")
    if p + n + cl != tot:
        problems.append(f"{where} says {tot} total but its parts sum to "
                        f"{p + n + cl}")
    if tot != sum(verdicts.values()):
        problems.append(f"{where} says {tot} total, the table has "
                        f"{sum(verdicts.values())}")

total = sum(verdicts.values())
if total != 675:
    problems.append(f"the roster holds {total} issues, expected 675")

# --- the two files must agree on which issues are closed ----------------
only_roster = roster_closed - log_closed
only_log = log_closed - roster_closed
for n in sorted(only_roster):
    problems.append(f"stash#{n} is `closed` in the roster but has no row in "
                    f"closed-issues.md")
for n in sorted(only_log):
    problems.append(f"stash#{n} is in closed-issues.md but the roster does not "
                    f"mark it closed")

# --- report -------------------------------------------------------------
print("issue ledgers")
print(f"  roster: {len(rows)} issues")
for k in sorted(verdicts):
    print(f"    {k:<12} {verdicts[k]}")
print(f"  closed-issues.md: {len(log_closed)} rows")
print(f"  closed set: {', '.join('stash#' + n for n in sorted(roster_closed))}")

if problems:
    print("\nFAIL:")
    for p in problems:
        print(f"  - {p}")
    sys.exit(1)

print("\nOK: header, table and log agree")
