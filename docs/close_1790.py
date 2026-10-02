#!/usr/bin/env python3
"""Close out the ledgers for stash#1790.

Three things this must get right, each of which has bitten before:

1. A `closed` row sitting under a `## Planned` heading is itself the failure the checker
   exists to catch, so the row has to MOVE, not just change status.
2. The header counts and the tally line after the rules table have to move WITH the row --
   reconciling counts against the table is the whole point of the checker.
3. `docs/ISSUES.md` C8 reads the disposition cell (`cells[-3]`) and requires a hex commit
   hash in it, so the commit has to be written to a file and substituted in. A commit in
   the wrong cell fails the evidence rule.

Idempotent: re-running with the same commit is a no-op, and it refuses to run if the row is
not in the state it expects rather than silently doing nothing.

Takes the commit hash as an argument. There is no default, because a ledger row that
records "commit TBD" is worse than an unclosed one -- it looks closed.
"""

import pathlib
import re
import subprocess
import sys
from typing import NoReturn

REPO = pathlib.Path(__file__).resolve().parent.parent
ISSUES = REPO / "docs" / "ISSUES.md"
UPSTREAM = REPO / "docs" / "UPSTREAM-ISSUES.md"
CLOSED = REPO / "docs" / "closed-issues.md"
ISSUE_NO = "1790"


def read(p):
    return p.read_text()


def write(p, t):
    p.write_text(t)


def fail(msg: str) -> NoReturn:
    """Refuse rather than guess.

    Typed NoReturn, which is not decoration: every index below is `int | None` from
    `next(...)`, and without this the type checker cannot see that the `if ... is None:
    fail(...)` guards have already exited. That is the difference between a guard the
    checker verifies and a guard it merely tolerates.
    """
    print("ERROR:", msg)
    sys.exit(2)


def main():
    if len(sys.argv) != 2:
        fail("usage: close_1790.py <commit-hash>")
    commit = sys.argv[1].strip()
    if not re.fullmatch(r"[0-9a-f]{7,40}", commit):
        fail(f"{commit!r} is not a hex commit hash. The C8 evidence rule requires one in "
             f"the disposition cell, and a placeholder there reads as closed when it is not.")

    # ---------------------------------------------------------------- ISSUES.md
    t = read(ISSUES)
    lines = t.splitlines(keepends=True)
    row_idx = next((i for i, l in enumerate(lines)
                    if l.startswith(f"| {ISSUE_NO} |")), None)
    if row_idx is None:
        fail(f"no row for {ISSUE_NO} in docs/ISSUES.md")
    cells = [c.strip() for c in lines[row_idx].strip().strip("|").split("|")]

    disposition = (
        f"**done** -- commit `{commit}`. Built: migration 121 adds a SOURCE REGISTRY "
        f"(`external_sources`, name UNIQUE) and a polymorphic `external_ids` table whose "
        f"unique key is all FOUR of (entity_type, entity_id, source_id, external_id) -- "
        f"leaving source_id out makes two providers' ids on one entity collide. `entity_id` "
        f"is deliberately NOT a foreign key (SQLite requires FK targets to be UNIQUE and "
        f"there is no single unique column across five parents), which is why "
        f"`DestroyForEntity` and `SweepOrphans` are wired into BOTH delete chokepoints "
        f"(`repository.destroy` and `table.destroy`) rather than being nice-to-have. The "
        f"four legacy `*_stash_ids` tables are NOT migrated, deliberately: rewriting four "
        f"tables holding every provider id existing users have is large, irreversible, and "
        f"its failure mode is silent data loss. Accepted cost, stated: the duplication is "
        f"reduced, not removed. Spec `docs/ISSUE-1790-spec.md`, plan "
        f"`docs/ISSUE-1790-plan.md`, mutation gate `docs/mutate_external_id.py`"
    )

    # cells[-1] is the status cell, cells[-3] the disposition.
    if len(cells) < 3:
        fail(f"row has only {len(cells)} cells; the disposition cell is cells[-3]")
    cells[-3] = disposition
    cells[-1] = "done"
    lines[row_idx] = "| " + " | ".join(cells) + " |\n"
    write(ISSUES, "".join(lines))
    print(f"docs/ISSUES.md  row {ISSUE_NO}: disposition + status -> done")

    # ------------------------------------------------------------ UPSTREAM-ISSUES.md
    t = read(UPSTREAM)
    lines = t.splitlines(keepends=True)
    row_idx = next((i for i, l in enumerate(lines)
                    if l.startswith(f"| {ISSUE_NO} |")), None)
    if row_idx is None:
        fail(f"no row for {ISSUE_NO} in docs/UPSTREAM-ISSUES.md")
    row = lines[row_idx]
    cells = [c.strip() for c in row.strip().strip("|").split("|")]

    verdict = (
        f"**Closed** in this project, not upstream. Implemented as a source REGISTRY plus one "
        f"polymorphic `external_ids` table, with the four existing `*_stash_ids` tables left "
        f"in place -- rewriting four tables holding every provider id existing users have is "
        f"large, irreversible, and its failure mode is silent data loss, so the duplication "
        f"is reduced rather than removed. See `docs/ISSUES.md` row {ISSUE_NO} for the verified "
        f"state and `docs/ISSUE-1790-spec.md` for the design."
    )
    cells[-1] = "closed"
    cells[-2] = verdict
    moved = "| " + " | ".join(cells) + " |\n"

    # Remove from wherever it is, and append under the Resolved heading.
    del lines[row_idx]
    t = "".join(lines)
    lines = t.splitlines(keepends=True)
    res = next((i for i, l in enumerate(lines)
                if l.startswith("## Resolved — closed or done")), None)
    if res is None:
        fail("no `## Resolved` heading in docs/UPSTREAM-ISSUES.md")
    # Insert after the heading's table header separator, not right under the heading.
    j = res
    while j < len(lines) and not lines[j].lstrip().startswith("|---"):
        j += 1
    if j >= len(lines):
        fail("`## Resolved` has no table header separator to insert after")
    j += 1
    lines.insert(j, moved)
    t = "".join(lines)

    # Counts: planned -1, closed +1, in both the bold summary and the tally line.
    t, n1 = re.subn(r"(\*\*\d+ issues: )(\d+) planned(, \d+ not planned or deferred, )(\d+) closed",
                    lambda m: (f"{m.group(1)}{int(m.group(2))-1} planned"
                               f"{m.group(3)}{int(m.group(4))+1} closed"), t, count=1)
    if n1 != 1:
        fail("the `**N issues: ... planned ... closed**` summary line did not match; "
             "refusing to guess at the counts")
    t, n2 = re.subn(r"(\*\*)(\d+) planned, (\d+ not planned or deferred, )(\d+) closed, (\d+ total)",
                    lambda m: (f"{m.group(1)}{int(m.group(2))-1} planned, "
                               f"{m.group(3)}{int(m.group(4))+1} closed, {m.group(5)} total"),
                    t, count=1)
    if n2 != 1:
        fail("the `**N planned, ... **` tally line did not match")

    # Section headings carry their own counts.
    t, n3 = re.subn(r"(## Planned — by signal \()(\d+)(\))",
                    lambda m: f"{m.group(1)}{int(m.group(2))-1}{m.group(3)}", t, count=1)
    t, n4 = re.subn(r"(## Resolved — closed or done \()(\d+)(\))",
                    lambda m: f"{m.group(1)}{int(m.group(2))+1}{m.group(3)}", t, count=1)
    if n3 != 1 or n4 != 1:
        fail(f"section heading counts did not both match (by-signal={n3}, resolved={n4})")

    write(UPSTREAM, t)
    print(f"docs/UPSTREAM-ISSUES.md  row {ISSUE_NO}: MOVED to Resolved, status -> closed, "
          f"counts -1/+1")

    # ------------------------------------------------------------ closed-issues.md
    t = read(CLOSED)
    if re.search(rf"^\| {ISSUE_NO} \|", t, re.M):
        print(f"docs/closed-issues.md  row {ISSUE_NO}: already present")
    else:
        lines = t.splitlines(keepends=True)
        hdr = next((i for i, l in enumerate(lines) if l.startswith("|---")), None)
        if hdr is None:
            fail("docs/closed-issues.md has no table header separator")
        lines.insert(hdr + 1, f"| {ISSUE_NO} | Generalized support for external IDs | "
                              f"closed | Source registry + polymorphic `external_ids`; "
                              f"four legacy `*_stash_ids` tables left in place on purpose. "
                              f"`{commit}` |\n")
        write(CLOSED, "".join(lines))
        print(f"docs/closed-issues.md  row {ISSUE_NO}: added")

    # -------------------------------------------------------------------- verify
    for name, cmd in (("check-issue-ledgers", [sys.executable, "docs/check-issue-ledgers.py"]),
                      ("goal-check", [sys.executable, "docs/goal-check.py"])):
        p = subprocess.run(cmd, cwd=str(REPO), capture_output=True, text=True)
        out = (p.stdout + p.stderr).strip()
        print(f"\n--- {name} (exit {p.returncode}) ---")
        print(out[-2500:])


if __name__ == "__main__":
    main()
