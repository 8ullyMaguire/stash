#!/usr/bin/env python3
"""Repair the 10 malformed rows in docs/closed-issues.md, and check the repair afterwards.

Every row here was read in full and its cause identified individually. Two general transforms were
tried first and BOTH were wrong, which is why this is per-row:

  1. "Escape every pipe on the line" -- also escapes the 5 column boundaries, collapsing the row to
     one cell.
  2. "Escape pipes that are not surrounded by whitespace" -- wrong, because the stray pipe in
     `` `union VisualFile = VideoFile | ImageFile` `` IS surrounded by whitespace, and so is the
     stray pipe in 7179's `` `/`||`/` `` is not. Neither predicate separates them.
  3. "Split, then rejoin the surplus into the column it belongs to" -- needs to know which column
     the stray pipe fell in, and inferring that swallowed the title into the issue-number cell.

So: each row gets an explicit, located fix, and the script refuses if a row is not in the state it
expects. A repair that silently does nothing to a row it misidentified is how a ledger rots again.

THE FOUR CAUSES
===============

  A. Stray pipe inside one cell (6949, 684, 7179, 7234) -> escape that one pipe as `\\|`.
     Located by the fact that the row is otherwise well formed, so the surplus cell is the one
     whose text continues mid-sentence.

  B. Row TRUNCATED, remainder loose in the file body (7165, 3849) -> rejoin the loose prose back
     into the row it came from. The prose is not deleted; it is put back where it belongs.

  C. Row never closed (2540, 2824, 7238, 837) -> append the missing columns, each verified
     against the repo rather than invented.

Every commit hash and test name below was checked with `git cat-file` / `grep '^func Test'` BEFORE
being written. The first draft of this script cited `e2b9c5f2a` for #7165 (real: `c71899e7f`) and
tests `TestGalleryImageSort` and `TestFileIssue`, none of which exist -- which in a file whose header
reads "a fix with no test is a claim" is the exact failure the file exists to prevent.
"""
import re
import sys
from pathlib import Path

LOG = Path(__file__).resolve().parent.parent / "docs" / "closed-issues.md"
EXPECTED = 5


def cells(line):
    return [c.strip() for c in re.split(r"(?<!\\)\|", line.strip().strip("|"))]


def fail(msg):
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def find_row(lines, issue):
    for i, l in enumerate(lines):
        if l.startswith(f"| {issue} |"):
            return i
    fail(f"row {issue} not found")


# --- C: rows missing a column. Every value below is verified against the repo. --------------
#
# Two of these were missing the TITLE; one was not, and misreading it is what put a duplicated title
# in the fix column for one commit.
#
# 2540 and 2824: four cells where the title should be. Their cell[1] is 1535 and 1644 characters of
# fix prose starting "Stash sends the image host..." and "Slow scanning and slow disk operations...",
# so the title is genuinely absent and is restored from docs/UPSTREAM-ISSUES.md (the roster is the
# authority for titles).
#
# 7238 is the opposite. Its cell[1] was already a title -- 32 characters, "Signed URL support for
# funscript" -- and what it was missing was the trailing COMMIT cell. Applying the title rule to it
# produced a five-column row whose fix column read "Signed URL support for funscript" and whose real
# fix prose had shifted right, i.e. a row that parses perfectly and is wrong. Shape alone cannot
# catch that; the guard that does is the SHORT-CELL check below, which is why it exists.
#
# (issue -> title, from the roster)
TITLES = {
    "stash#2540": "Image HTTP request referrer behavior",
    "stash#2824": "Slow scanning with huge amounts of videos",
}

# 7238 keeps its own title and gains only the commit.
COMMIT_ONLY = {
    "stash#7238": "`2c705e992`",
}

# 837 and 3849 are different: they DO have their title, and are missing the closing `| |` plus, for
# 3849, the columns after the fix. Verified values only.
APPEND = {
    "stash#837": [
        "`TestIssueIsIdempotent`, `TestDismissedIssueIsNotReraised`, `TestResolveIsIdempotent` "
        "(pkg/sqlite/issue_test.go) plus the detection tests in pkg/sqlite/issue_detect_test.go.",
        "`8b1082e85` `3797a3ded` `76982d8e0` `12e5059ec` `b80f102dc` `2c705e992`",
    ],
    # 3849's row is complete through the fix column; the test and commit cells are missing, and the
    # 40-line essay that follows in the file body is authored prose ABOUT the fix -- left untouched.
    "stash#3849": [
        "`TestGalleryOrderIsBrokenWhenAnImageIsSharedAcrossGalleries`, "
        "`TestTheSharedImageDoesNotReorderEitherGallery` and "
        "`TestGalleryOrderIsCorrectWhenImagesAreNotShared` "
        "(pkg/sqlite/image_gallery_order_test.go). See the note below the table for why the first "
        "two fixtures were not enough.",
        "`de1a30ac2`",
    ],
}

# --- B: rows TRUNCATED mid-cell, with the remainder loose in the file body ----------------
#
# Only 7165 is this. Its row was cut inside a quoted character set (`" ,\t`) and the rest of the
# sentence became a loose prose line, so rejoining is correct there.
#
# 3849 is NOT this case, and treating it as one was the last bug in this script. Its row ends
# cleanly at "Fixed in `de1a30ac2` (...)." and what follows is a 40-line essay about the fix --
# paragraphs, a bulleted list of three traps, and a "the lesson" section. That is authored prose
# ABOUT the row, deliberately placed under it in the file body. Splicing its first line back into
# the table turned the essay's opening sentence into half a cell and left 38 lines of dangling text.
# A repair that cannot tell "orphaned cell text" from "an essay someone wrote under the table" will
# eventually eat the essay. So 3849 is completed by APPEND, and its prose is left exactly where it is.
#
# (issue -> the exact opening words of the loose line that continues it)
CONTINUATIONS = {
    "stash#7165": ';"\'"` anywhere in an accepted value',
}


def main():
    lines = LOG.read_text().split("\n")
    fixed = 0

    # Every issue id present BEFORE the repair. Compared against the set afterwards, because a
    # repair can keep every row parsing to 5 columns while destroying rows as rows -- which is
    # exactly what happened: the first title-restore put the title in the issue column, so three
    # rows stopped matching `^| stash#N |` and every count in the file silently lost them.
    # A repair that can only be checked by column count is a repair that can pass while losing data.
    ids_before = {m.group(1) for m in
                  (re.match(r"\|\s*(stash#\d+)\s*\|", l) for l in lines) if m}

    # --- A: escape one located stray pipe per row ------------------------------
    # Each entry: the substring that CONTAINS the stray pipe, replaced with the escaped form. The
    # anchor is quoted verbatim from the file so a mismatch aborts instead of mangling prose.
    #
    # 684 is NOT here. Its five columns are intact and correct; what follows the closing `| |` is an
    # amendment note ("**Amended by PR #7159** ...") that belongs in the row but sits outside the
    # table's shape. Escaping is the wrong repair there -- see the AMENDMENTS block below.
    ESCAPES = {
        "stash#6949": ("`union VisualFile = VideoFile | ImageFile`",
                       "`union VisualFile = VideoFile \\| ImageFile`"),
        "stash#7179": ("`/`||`/`", "`/` \\| `/`"),
        "stash#7234": ("`boolean | undefined`", "`boolean \\| undefined`"),
    }
    for issue, (before, after) in ESCAPES.items():
        i = find_row(lines, issue)
        if after in lines[i] and before not in lines[i]:
            print(f"  {issue}: stray pipe already escaped; nothing to do")
            continue
        if before not in lines[i]:
            fail(f"{issue}: anchor {before!r} not found in its row, and the escaped form is not "
                 "present either -- the file changed under me, so I will not guess")
        lines[i] = lines[i].replace(before, after, 1)
        fixed += 1
        print(f"  {issue}: escaped the stray pipe in a code span")

    # --- A2: 684's amendment note, which sits AFTER the closing `| |` -----------
    # The row is `| issue | title | fix | test | commit | | **Amended by ...** |`. Five columns
    # plus an unterminated sixth. The amendment is real content that must not be deleted, so it is
    # folded into the commit cell -- which is where an amendment to a commit belongs.
    i = find_row(lines, "stash#684")
    if "| | **Amended by PR #7159**" in lines[i]:
        head, _, amendment = lines[i].partition("| | **Amended by PR #7159**")
        amendment = "**Amended by PR #7159**" + amendment.rstrip()
        if amendment.endswith("|"):
            amendment = amendment[:-1].rstrip()
        lines[i] = head.rstrip() + " | " + amendment.replace("|", "\\|") + " |"
        fixed += 1
        print("  stash#684: folded the trailing amendment note into the commit cell")
    elif "**Amended by PR #7159**" not in lines[i]:
        fail("stash#684: neither the amendment note nor a repaired row is present")

    # --- B: rejoin truncated rows with their loose continuation ----------------
    for issue, marker in CONTINUATIONS.items():
        i = find_row(lines, issue)
        if len(cells(lines[i])) == EXPECTED:
            print(f"  {issue}: row already complete; nothing to rejoin")
            continue
        # Find the loose line: the first non-empty line after the row that does not start with '|'.
        j = None
        for k in range(i + 1, min(i + 6, len(lines))):
            if not lines[k].strip():
                continue
            if lines[k].startswith("|"):
                break
            if marker.split("...")[0][:20] in lines[k]:
                j = k
                break
            break
        if j is None:
            fail(f"{issue}: no loose continuation line found; refusing to guess")
        loose = lines[j].strip()
        # The row ended mid-sentence; join with nothing (the text continues a word) unless the row
        # clearly ends on a word boundary. 7165's row ends `... in `" ,\t` and the loose line starts
        # `;"'`"` -- so the join is DIRECT with no space, because the split was inside a quoted set.
        row = lines[i].rstrip()
        if row.endswith("|") or row.endswith("\\|"):
            fail(f"{issue}: the row looks already-closed; the continuation would duplicate it")
        lines[i] = row + loose
        lines[j] = ""
        fixed += 1
        print(f"  {issue}: rejoined {len(loose)} chars of loose prose back into the row")

    # --- C: restore the missing TITLE column, then append anything still absent ---------
    for issue, title in TITLES.items():
        i = find_row(lines, issue)
        have = len(cells(lines[i]))
        if have == EXPECTED:
            print(f"  {issue}: already has {EXPECTED} columns; leaving it alone")
            continue
        if have != EXPECTED - 1:
            fail(f"{issue}: has {have} columns; expected exactly {EXPECTED - 1} to be missing "
                 "only the title, but the shape is different from what this repair was written "
                 "for -- read the row rather than let the script guess")
        # Insert the title AFTER the issue id, not before it. `partition("|")` on
        # `| stash#2540 | rest` yields head='' and rest=' stash#2540 | rest', so writing
        # f"|{head}| {title} |{rest}" produced `| title | stash#2540 | rest` -- the title landed in
        # the issue column and the row stopped being recognisable as an issue row at all. The three
        # repaired rows vanished from every count in the file while still parsing to 5 columns,
        # which is the same failure mode as the original defect: a row the checkers cannot see.
        #
        # The guard below is what caught it: comparing the issue-id set before and after found three
        # ids gone. So the repair now states the id it expects and verifies it survived.
        m = re.match(r"^\|\s*(stash#\d+)\s*\|(.*)$", lines[i], re.S)
        if not m:
            fail(f"{issue}: row does not start with the issue id; refusing to guess the column order")
        lines[i] = f"| {m.group(1)} | {title} |{m.group(2)}"
        fixed += 1
        print(f"  {issue}: restored the missing title column from the roster")

    # 7238: it already had a title, so it gains only the trailing commit cell.
    for issue, commit in COMMIT_ONLY.items():
        i = find_row(lines, issue)
        have = len(cells(lines[i]))
        if have == EXPECTED:
            print(f"  {issue}: already has {EXPECTED} columns; leaving it alone")
            continue
        if have != EXPECTED - 1:
            fail(f"{issue}: has {have} columns; expected {EXPECTED - 1} (missing only the commit). "
                 "Read the row rather than let the script guess which column is absent")
        # The row's trailing shape varies, and each variant needs a different join. Measured, not
        # assumed -- three attempts got this wrong in three different ways, each caught by the
        # script's own column check rather than by reading the output:
        #
        #   `... | test |`      -> 4 cells, closing pipe present. The commit cell is ABSENT, so a
        #                         separator is needed: " | `hash` |". Appending "`hash` |" with no
        #                         separator welds the hash onto the test text (what 7238 looked like).
        #   `... | test | |`    -> 5 cells with an EMPTY commit. Fill it in place.
        #   `... | test`        -> 4 cells, no closing pipe. Append " | `hash` |".
        #
        # The general rule that covers all three: strip any trailing pipes and whitespace, then join
        # the five cells with " | ". Rebuilding the row from its cells cannot weld or over-count,
        # because the number of cells is known by this point.
        row = lines[i].rstrip()
        existing = cells(row)
        if len(existing) != EXPECTED - 1:
            fail(f"{issue}: internal error -- expected {EXPECTED - 1} cells before the append, "
                 f"found {len(existing)}")
        lines[i] = "| " + " | ".join(existing + [commit]) + " |"
        fixed += 1
        print(f"  {issue}: appended the missing commit column -> {EXPECTED}")

    for issue, extra in APPEND.items():
        i = find_row(lines, issue)
        have = len(cells(lines[i]))
        want = EXPECTED - have
        if want <= 0:
            print(f"  {issue}: already has {have} columns; leaving it alone")
            continue
        if len(extra) != want:
            fail(f"{issue}: has {have} columns, needs {want}, I have {len(extra)} to append")
        # Rebuild from the cells rather than string-appending, for the same reason as COMMIT_ONLY:
        # appending text to a row whose trailing shape is unknown either adds a column or welds two
        # cells together, and both happened here before this was rewritten.
        existing = cells(lines[i])
        lines[i] = "| " + " | ".join(existing + extra) + " |"
        fixed += 1
        print(f"  {issue}: appended {want} column(s) -> {EXPECTED}")

    # --- collapse the blank lines left by B -------------------------------------
    out = "\n".join(lines)
    out = re.sub(r"\n{3,}", "\n\n", out)
    LOG.write_text(out)

    # --- verify -----------------------------------------------------------------
    bad = []
    for l in out.split("\n"):
        if not l.startswith("| stash#"):
            continue
        n = len(cells(l))
        if n != EXPECTED:
            bad.append((cells(l)[0], n))
    if bad:
        fail(f"rows still malformed: {bad}")

    ids_after = {m.group(1) for m in
                 (re.match(r"\|\s*(stash#\d+)\s*\|", l) for l in out.split("\n")) if m}
    lost = sorted(ids_before - ids_after)
    gained = sorted(ids_after - ids_before)
    if lost:
        fail(f"these issue ids no longer appear as rows -- the repair destroyed rows: {lost}")
    if gained:
        fail(f"unexpected new issue ids appeared: {gained}")

    total = len(ids_after)
    print(f"  all {total} log rows parse to {EXPECTED} columns ({fixed} repaired); "
          f"all {len(ids_before)} issue ids still present")
    return 0


if __name__ == "__main__":
    sys.exit(main())
