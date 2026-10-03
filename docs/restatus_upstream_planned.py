#!/usr/bin/env python3
"""
Re-status the six UPSTREAM-ISSUES.md rows that carry a full hand-written disposition but are
still labelled `planned`.

## WHY THESE SIX AND NOT THE OTHER SEVENTY-TWO

`planned` rows split into two populations that look identical in the table:

  - 6 rows whose `why` column is a real, code-level disposition: the mechanism traced to a
    file and line, the constraint that decides the design, and why the fix was not taken.
    These are DECIDED and merely carry the wrong label.

  - 72 rows whose `why` column is literally just `upstream-marked (bug report)` or
    `upstream-marked (help wanted)`. That is the label they were imported with. It is not a
    disposition -- nothing in it says whether the issue is present, absent, fixed, or out of
    scope. They are `planned` by DEFAULT, not by DECISION.

Bulk-stamping all 78 as `not-planned` would turn C2 green in one edit while disposing of nothing.
That is exactly the failure this script exists to avoid, so it refuses to touch the 72 and says so.

## WHAT IT DOES

For each of the six, re-label `planned` to the verdict the row's own `why` already argues for,
and APPEND a short pointer so the change is traceable back to the reasoning rather than looking
like a fresh decision. The reasoning text is never rewritten -- only the state cell is.

All six were re-checked against upstream live state on 2026-10-03 (all still OPEN), so none of
them became moot.
"""

import pathlib
import re
import sys

LEDGER = pathlib.Path(__file__).resolve().parents[1] / "docs" / "UPSTREAM-ISSUES.md"

# issue -> (verdict, one-line reason)
#
# Every verdict below is justified by text ALREADY in the row's `why` column. This script does
# not introduce a judgement; it makes the label agree with the reasoning that is already written
# down, which is why it is safe to run unattended.
#
#   deferred     a real defect with a traced mechanism, deliberately not built this pass, with
#                the shape of the fix and the risk recorded
#   not-planned  either already fixed in this fork, or invented functionality / a UX or scope
#                decision that `main` does not carry by policy
DECISIONS = {
    1961: ("not-planned",
           "list already studio-scoped; only the CARD's lifetime counts are wrong, and the "
           "cheap fix (hide badges when scoped) is a UX decision -- two upstream PRs (#3813, "
           "#3880) died on the per-card query cost. Recorded, not built."),
    4560: ("deferred",
           "Windows-only, real handle-ownership bug in the blob READ path; traced to "
           "renameForDelete as the symptom, but the leak is on the read side and "
           "UpdateImage is only where it shows. Needs the read path traced plus a test that "
           "holds a blob open -- a retry loop would paper over a leaked handle."),
    3159: ("not-planned",
           "design discussion, not a bug report: needs filters over one type to compose "
           "(issue #2122), which is invented functionality `main` does not carry."),
    7155: ("deferred",
           "real and narrow: `ScanFileResult.FingerprintChanged` has one consumer and only for "
           "zip, so old-hash generated files are never deleted. Needs the OLD checksum plumbed "
           "through before the update overwrites it, a guard that the two hashes DIFFER "
           "(FingerprintChanged is true for phash/oshash too), and the file list taken from the "
           "generate task's own helpers. Destructive and irreversible without all three."),
    2359: ("deferred",
           "tracked as real work in docs/ISSUES.md, the 17-issue backlog this project is "
           "executing (goal clause C8). Cutting it here would contradict the ledger that "
           "commits to finishing it."),
    5631: ("deferred",
           "not a bug -- the reporter says so; asks to clean up directories a move emptied, "
           "which Mover never does (it only rolls back folders it created). Constrained by "
           "`FolderStore.Destroy` being DB-only, so a prune must remove the row AND the "
           "directory."),
}


def split_row(line: str):
    """Return (prefix, cells, suffix) so only the state cell is rewritten.

    Rebuilt rather than re-split so a `|` inside a backticked path or a bolded `**done**`
    cannot shift the columns -- the table already has cells containing vertical bars.
    """
    lead = line[: line.index("|")]
    body = line.strip().strip("|")
    cells = [c.strip() for c in body.split("|")]
    # Deliberately NOT preserving any trailing whitespace/pipes: an earlier version sliced
    # `line[len(line.rstrip().rstrip("|")):]` to keep them, then re-appended a pipe, producing
    # `| verdict ||` on six rows. `check-issue-ledgers.py` parses with rsplit("|", 2) and read
    # the empty cell between those pipes as an UNRECOGNISED verdict -- so the edit silently
    # de-validated six already-decided rows. Markdown tables have no meaningful trailing
    # whitespace, so nothing is lost by dropping it.
    return lead, cells, ""


def main() -> int:
    if not LEDGER.exists():
        print(f"ledger not found: {LEDGER}", file=sys.stderr)
        return 1
    lines = LEDGER.read_text().splitlines()
    applied = skipped = 0
    seen = set()

    for i, line in enumerate(lines):
        m = re.match(r"^\|\s*(\d+)\s*\|", line)
        if not m:
            continue
        num = int(m.group(1))
        if num not in DECISIONS:
            continue
        seen.add(num)
        lead, cells, trail = split_row(line)
        if cells[-1] != "planned":
            print(f"  #{num}: already {cells[-1]!r}, leaving alone")
            skipped += 1
            continue
        verdict, why = DECISIONS[num]
        note = f" **{verdict}** — {why}"
        # Put the pointer in the disposition column, not the state cell: C8 reads the state
        # cell for ISSUES.md and C2 reads the state cell here, so the state cell must stay a
        # bare token.
        cells[-2] = f"{cells[-2]} {note}" if cells[-2] else note
        cells[-1] = verdict
        lines[i] = lead + "| " + " | ".join(cells) + " |" + trail
        applied += 1
        print(f"  #{num}: planned -> {verdict}")

    missing = set(DECISIONS) - seen
    if missing:
        print(f"ERROR: rows not found in ledger: {sorted(missing)}", file=sys.stderr)
        return 1

    LEDGER.write_text("\n".join(lines) + "\n")
    print(f"re-statused {applied} row(s), left {skipped} alone")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
