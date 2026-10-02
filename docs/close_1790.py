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
    #
    # THE CHECKER'S OWN PARSER IS IMPORTED, NOT REIMPLEMENTED. The first version of this
    # script indexed `cells[-3]` and `cells[-1]` on a plain `split("|")`, and the row's
    # verified-state cell contains an ESCAPED pipe (it quotes a grep alternation), so the
    # plain split produced six cells for a five-column row and the script wrote the
    # disposition over the VERIFIED STATE -- silently destroying the re-measurement that
    # says why the recorded claim was wrong, while goal-check reported only "no evidence".
    # Two hand-rolled index arithmetic on a table whose cells contain escaped pipes is
    # precisely how that happened; one imported parser cannot drift from the check that
    # judges the result.
    import importlib.util
    import re as _re
    _spec = importlib.util.spec_from_file_location(
        "goalcheck", str(REPO / "docs" / "goal-check.py"))
    _gc = importlib.util.module_from_spec(_spec)
    try:
        _spec.loader.exec_module(_gc)
    except SystemExit:  # goal-check calls sys.exit at import time when run as a module
        pass
    split_cells = _gc.split_cells

    t = read(ISSUES)
    lines = t.splitlines(keepends=True)
    row_idx = next((k for k, l in enumerate(lines)
                    if l.startswith(f"| {ISSUE_NO} |")), None)
    if row_idx is None:
        fail(f"no row for {ISSUE_NO} in docs/ISSUES.md")
    m = _re.match(r"^\|\s*(\d+)\s*\|(.*)$", lines[row_idx].rstrip("\n"))
    if not m:
        fail(f"the {ISSUE_NO} row in docs/ISSUES.md does not parse as a table row")
    cells = split_cells(m.group(2))

    # cells[-2] is the STATE (the row ends in `|`, so cells[-1] is the empty field) and
    # cells[-3] the DISPOSITION. The shape is ASSERTED before writing: "cells[-3] happened
    # to exist" is not a reason to proceed, and the failure mode is destroying the wrong
    # cell with no error at all.
    if len(cells) < 4:
        fail(f"row has only {len(cells)} cells; expected at least "
             f"[title, labels, verified, disposition, state, '']")
    if cells[-2] != "in progress":
        fail(f"the {ISSUE_NO} row's state cell is {cells[-2]!r}, not 'in progress'. "
             f"Refusing to overwrite a row in some other state -- if this issue was "
             f"already dispositioned, that is a fact to preserve, not overwrite.")
    before_verified = cells[-4]

    disposition = (
        f"**done** -- commits `{commit}` and `62313ad60`. Built: migration 121 adds a "
        f"SOURCE REGISTRY (`external_sources`, name UNIQUE) and a polymorphic "
        f"`external_ids` table whose unique key is all FOUR of (entity_type, entity_id, "
        f"source_id, external_id) -- leaving source_id out makes two providers' ids on one "
        f"entity collide. `entity_id` is deliberately NOT a foreign key (SQLite requires FK "
        f"targets to be UNIQUE and there is no single unique column across five parents), "
        f"which is why `DestroyForEntity` and `SweepOrphans` are wired into BOTH delete "
        f"chokepoints (`repository.destroy` AND `table.destroy`) rather than being "
        f"nice-to-have. The four legacy `*_stash_ids` tables are NOT migrated, "
        f"deliberately: rewriting four tables holding every provider id existing users have "
        f"is large, irreversible, and its failure mode is silent data loss. Accepted cost, "
        f"stated: the duplication is reduced, not removed. Proof of generality: GALLERY, "
        f"the one entity with no legacy table, adopts external ids through the same code "
        f"path and no migration. Tests 26, and `docs/mutate_external_id.py` reports 14/14 "
        f"mutations killed -- including M7, which found that the store's own Go check was "
        f"masking the database's CHECK and that the `-run` filter was silently excluding "
        f"the test written to catch it. Spec `docs/ISSUE-1790-spec.md`, plan "
        f"`docs/ISSUE-1790-plan.md`"
    )

    cells[-3] = disposition
    cells[-2] = "done"
    if cells[-4] != before_verified:
        fail("the verified-state cell changed -- refusing to write")

    # REBUILD THE ROW, RE-ESCAPING ANY PIPE INSIDE A CELL. The parser splits on UNESCAPED
    # pipes, so a cell whose content contains a literal `|` came back with that pipe
    # unescaped -- joining the cells with " | " would turn it into a real column separator
    # on the next parse, which is the exact defect being fixed. So every pipe inside a cell
    # is re-escaped on the way out.
    #
    # THE ISSUE NUMBER IS PUT BACK EXPLICITLY. The first version rebuilt the row from
    # `cells` alone and dropped the leading `| N |`, so the row stopped parsing as a table
    # row at all -- and the verification below is what caught it, rather than a later
    # reader noticing a ledger with a silently broken row.
    def esc(c):
        return c.replace("|", r"\|")

    rebuilt = f"| {ISSUE_NO} | " + " | ".join(esc(c) for c in cells) + "\n"
    # ...and prove the round-trip: re-parse the row just written and require the cells back
    # IDENTICAL. A ledger writer that cannot verify its own write is one that eventually
    # corrupts a row silently -- and the first two defects above were both silent.
    rm = _re.match(r"^\|\s*(\d+)\s*\|(.*)$", rebuilt.rstrip("\n"))
    if not rm:
        fail("the rebuilt row does not parse as a table row at all")
    if int(rm.group(1)) != int(ISSUE_NO):
        fail(f"the rebuilt row carries issue {rm.group(1)}, not {ISSUE_NO}")
    check = split_cells(rm.group(2))
    if check != cells:
        fail("the row does not survive a write/read round-trip through the checker "
             f"parser; wrote {len(cells)} cells, read back {len(check)}")
    lines[row_idx] = rebuilt
    write(ISSUES, "".join(lines))
    print(f"docs/ISSUES.md  row {ISSUE_NO}: disposition + state -> done "
          f"(verified-state cell preserved, {len(cells)} cells round-tripped)")

    # ------------------------------------------------------------ UPSTREAM-ISSUES.md
    t = read(UPSTREAM)
    lines = t.splitlines(keepends=True)
    row_idx = next((i for i, l in enumerate(lines)
                    if l.startswith(f"| {ISSUE_NO} |")), None)
    if row_idx is None:
        fail(f"no row for {ISSUE_NO} in docs/UPSTREAM-ISSUES.md")
    # The same imported parser, and the column count is ASSERTED because this table is a
    # different width from the one above (4 columns: # | Title | Why | Verdict, versus 6).
    # Indexing by position with no width check is how the ISSUES.md version came to write
    # over the wrong cell.
    mu = _re.match(r"^\|\s*(\d+)\s*\|(.*)$", lines[row_idx].rstrip("\n"))
    if not mu:
        fail(f"the {ISSUE_NO} row in docs/UPSTREAM-ISSUES.md does not parse as a table row")
    cells = split_cells(mu.group(2))
    # THIS TABLE IS [title, why, verdict, ''] -- FOUR cells after the number is consumed,
    # and 5 in the ISSUES.md table above, so the widths genuinely differ and the assertion
    # is worth having rather than cargo-culted. Measured, not assumed: the first version
    # asserted 5, this is 4, and the assertion fired before any write.
    if len(cells) != 4:
        fail(f"the {ISSUE_NO} row in docs/UPSTREAM-ISSUES.md has {len(cells)} cells; this "
             f"table is [title, why, verdict, ''] so 4 is expected. Refusing to index "
             f"cells[-2] into a row of a different width.")
    if cells[-2] != "planned":
        fail(f"the roster row's verdict cell is {cells[-2]!r}, not 'planned'; refusing to "
             f"overwrite it")

    verdict = (
        f"**Closed** in this project, not upstream. Implemented as a source REGISTRY plus one "
        f"polymorphic `external_ids` table, with the four existing `*_stash_ids` tables left "
        f"in place -- rewriting four tables holding every provider id existing users have is "
        f"large, irreversible, and its failure mode is silent data loss, so the duplication "
        f"is reduced rather than removed. See `docs/ISSUES.md` row {ISSUE_NO} for the verified "
        f"state and `docs/ISSUE-1790-spec.md` for the design."
    )
    # cells[-2] is the VERDICT and cells[-3] the "Why". The first version wrote them the
    # other way round, which put the six-character word `closed` into the Why column and the
    # whole explanation into Verdict -- and goal-check reported it exactly as it read:
    # "1 dispositioned row(s) have no real reason recorded: #1790 (6-char Why)". The row
    # was not corrupt, it was transposed, and the checker's complaint named the real defect
    # without knowing it was a transposition.
    cells[-3] = verdict
    cells[-2] = "closed"
    # Re-escape pipes inside cells on the way out (the parser unescaped them on the way in),
    # and put the issue number back -- see the identical note in the ISSUES.md block above.
    moved = (f"| {ISSUE_NO} | "
             + " | ".join(c.replace("|", r"\|") for c in cells) + "\n")
    mv = _re.match(r"^\|\s*(\d+)\s*\|(.*)$", moved.rstrip("\n"))
    if not mv or int(mv.group(1)) != int(ISSUE_NO):
        fail("the rebuilt roster row does not parse, or lost its issue number")
    if split_cells(mv.group(2)) != cells:
        fail("the roster row does not survive a write/read round-trip")

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
    #
    # THE `stash#` PREFIX IS REQUIRED, AND FIVE COLUMNS NOT FOUR. The file's own regex is
    # `^\| stash#(\d+) \|`, so a bare `| 1790 |` row parses as nothing -- and the checker
    # then reports "stash#1790 is closed in the roster but has no row in closed-issues.md",
    # which reads as a missing row rather than as a row written in the wrong format. The
    # header is `| issue | title | the fix | the test that proves it | commit |`.
    #
    # THE "TEST THAT PROVES IT" CELL IS NOT OPTIONAL PROSE. This file states up front that
    # the honest summary is "N issues closed, each with a test that fails without the fix",
    # so the cell names the mutation gate and the two tests that could not have been found
    # by reading the code.
    t = read(CLOSED)
    if re.search(rf"^\| stash#{ISSUE_NO} \|", t, re.M):
        print(f"docs/closed-issues.md  row stash#{ISSUE_NO}: already present")
    else:
        lines = t.splitlines(keepends=True)
        hdr = next((k for k, l in enumerate(lines) if l.startswith("|---")), None)
        if hdr is None:
            fail("docs/closed-issues.md has no table header separator")
        # The separator's column count IS the table's width, so build the row to match it
        # rather than to a remembered number.
        width = lines[hdr].count("|") - 1
        if width != 5:
            fail(f"closed-issues.md's table has {width} columns, expected 5; refusing to "
                 f"guess the layout and write a row that will not parse")
        row = (f"| stash#{ISSUE_NO} | Generalized support for external IDs | "
               f"Source registry (`external_sources`, name UNIQUE) plus one polymorphic "
               f"`external_ids` table keyed on all four of (entity_type, entity_id, "
               f"source_id, external_id). The four legacy `*_stash_ids` tables are left in "
               f"place ON PURPOSE -- rewriting four tables that hold every provider id "
               f"existing users have is irreversible and its failure mode is silent data "
               f"loss. | "
               f"`docs/mutate_external_id.py` -- 14/14 mutations killed, and it is what "
               f"found the two holes below. `TestT4`/`TestR4` prove the entity destroy "
               f"paths remove external ids (the R4 gallery case found a SECOND delete "
               f"chokepoint, `table.destroy`, that the first version had not wired, while "
               f"T4 was green). `TestABlankExternalIDIsRefusedByTheDatabaseItself` exists "
               f"because M7 showed the store's own Go check was masking the database CHECK. | "
               f"`{commit}`, `62313ad60` |\n")
        # ...and verify the row parses under the file's OWN regex before writing it.
        if not re.match(r"^\| stash#(\d+) \|", row):
            fail("the closed-issues.md row does not match the file's own "
                 "`^\\| stash#(\\d+) \\|` regex")
        lines.insert(hdr + 1, row)
        write(CLOSED, "".join(lines))
        print(f"docs/closed-issues.md  row stash#{ISSUE_NO}: added (5 columns, matches the "
              f"file's own regex)")

    # -------------------------------------------------------------------- verify
    for name, cmd in (("check-issue-ledgers", [sys.executable, "docs/check-issue-ledgers.py"]),
                      ("goal-check", [sys.executable, "docs/goal-check.py"])):
        p = subprocess.run(cmd, cwd=str(REPO), capture_output=True, text=True)
        out = (p.stdout + p.stderr).strip()
        print(f"\n--- {name} (exit {p.returncode}) ---")
        print(out[-2500:])


if __name__ == "__main__":
    main()
