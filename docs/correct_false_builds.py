#!/usr/bin/env python3
"""Correct three ledger rows whose disposition claimed a build that this fork never did.

Measured 2026-10-02, and the correction is the opposite of what the rows said.

THE CLAIM. Rows 2747, 3530 and 4326 in docs/ISSUES.md each carried a disposition cell
beginning `build:` -- "config-driven command template, detached, scrubbed env", "segments,
segment-aware duration and filters", "overlay panel, no route change, playback
uninterrupted" -- while the STATE cell said `open`. A build note in the disposition of an
open row is a contradiction: either the state is wrong or the build note is.

THE MEASUREMENT, and it settles it the other way:

| row | the code exists | added by |
|---|---|---|
| 2747 | `ui/v2.5/.../ExternalPlayerButton.tsx`, wired at `Scene.tsx:738` | upstream `3d1b949f4`, "Add button to scene page to open scene in external player (#679)" |
| 3530 | `pkg/ffmpeg/stream_segmented.go`, 915 lines | upstream `05669f550`, "Overhaul HLS streaming (#3274)" |
| 4326 | no related-content panel anywhere in history; the nearest things are `RelatedGroupPopover` (#5105) and the wall views (#5816) | upstream |

So the features are real and PRESENT, but they arrived by syncing upstream, not by this
fork implementing them. `git log --diff-filter=A` is what distinguishes the two, and the
author field settles it beyond doubt: `3d1b949f4 InfiniteTF infinitekittens@protonmail.com`
is not a commit this project made.

WHY THAT IS WORTH WRITING DOWN rather than quietly marking the rows `done`. A `build:`
note with no commit is exactly the shape C8's evidence rule exists to refuse -- it is a
claim with no commit behind it. The rows were not caught because the STATE cell said
`open`, so C8 never demanded evidence for them. Marking them `done` on the strength of
upstream code would have put a false claim into the ledger with a fabricated commit, and
the whole point of the column is that it is checkable.

So: state stays `open`, and the disposition is replaced with what was actually measured.

Usage: correct_false_builds.py
"""

import importlib.util
import pathlib
import re
import subprocess

REPO = pathlib.Path(__file__).resolve().parent.parent
ISSUES = REPO / "docs" / "ISSUES.md"

def load_split_cells():
    """Borrow the CHECKER'S parser, or refuse to run.

    Typed and guarded rather than assumed, because this file indexes `cells[-3]` and
    `cells[-2]` and a silently-None parser would make every one of those indexes wrong
    while the script reported success. goal-check calls sys.exit at import time when
    treated as a module, hence the guard.
    """
    spec = importlib.util.spec_from_file_location(
        "goalcheck", str(REPO / "docs" / "goal-check.py"))
    if spec is None or spec.loader is None:
        fail("cannot load docs/goal-check.py to borrow its table parser")
    mod = importlib.util.module_from_spec(spec)
    try:
        spec.loader.exec_module(mod)
    except SystemExit:
        pass
    fn = getattr(mod, "split_cells", None)
    if fn is None:
        fail("docs/goal-check.py has no split_cells -- this script's cell indices would "
             "be based on a parser that no longer exists")
    return fn


split_cells = load_split_cells()

def fail(msg):
    print("ERROR:", msg)
    raise SystemExit(2)


# issue -> (measured evidence, what it means)
FINDINGS = {
    "2747": (
        "The feature is PRESENT but came from UPSTREAM, not from this fork: "
        "`ui/v2.5/src/components/Scenes/SceneDetails/ExternalPlayerButton.tsx`, wired at "
        "`Scene.tsx:738`, added by upstream `3d1b949f4` \"Add button to scene page to open "
        "scene in external player (#679)\" (author InfiniteTF). **The row previously said "
        "`build: config-driven command template, detached, scrubbed env`, which is not what "
        "that commit does and is not a commit in this history at all** -- "
        "`git log --diff-filter=A` attributes the file to upstream, and no fork commit "
        "carries the number 2747. Either way this row's own work is NOT done, and the "
        "config-driven-template part of the old note is unevidenced: the component is "
        "lazy-loaded and takes a `scene` prop, with no command template of that shape in "
        "the tree."
    ),
    "3530": (
        "The feature is PRESENT but came from UPSTREAM: `pkg/ffmpeg/stream_segmented.go`, "
        "915 lines with `makeStreamArgs`, `checkSegments` and `HLSGetCodec`, added by "
        "upstream `05669f550` \"Overhaul HLS streaming (#3274)\". **The row previously said "
        "`build: segments, segment-aware duration and filters`, which attributes upstream "
        "work to this fork and names no commit.** Segmentation of a single file into "
        "multiple scenes -- which is what the issue title actually asks for -- is a "
        "different thing from segmenting the HLS VIDEO STREAM, and no fork commit "
        "implements it: the model is still one scene per file row."
    ),
    "4326": (
        "NOT BUILT, in this fork or upstream. There is no related-content panel in any "
        "commit: `git log --all --diff-filter=A --name-only` finds only "
        "`RelatedGroupTable.tsx` and `RelatedGroupPopover.tsx`, both from upstream "
        "`bcf0fda7a` (#5105, group/sub-group relationships) -- neither is about browsing "
        "content during video playback. **The row previously said `build: overlay panel, no "
        "route change, playback uninterrupted`, and no such component exists**; the "
        "`overlay` matches in this tree are list-view and gallery-card overlays, a "
        "different feature entirely. This row is the one real remaining item of the three."
    ),
}


def main():
    t = ISSUES.read_text()
    lines = t.splitlines(keepends=True)
    changed = 0

    for num, disposition in FINDINGS.items():
        idx = next((i for i, l in enumerate(lines) if l.startswith(f"| {num} |")), None)
        if idx is None:
            fail(f"no row for {num} in docs/ISSUES.md")
        m = re.match(r"^\|\s*(\d+)\s*\|(.*)$", lines[idx].rstrip("\n"))
        if m is None:
            fail(f"row {num} does not parse as a table row")
        cells = split_cells(m.group(2))
        if len(cells) < 4:
            fail(f"row {num} has only {len(cells)} cells; refusing to index into it")

        old_disposition, state = cells[-3], cells[-2]
        if not old_disposition.strip().startswith("build:"):
            print(f"  {num}: disposition already corrected -- leaving it alone")
            continue
        if state.strip('* ') != "open":
            fail(f"row {num}'s state is {state!r}, not 'open'; refusing to rewrite a "
                 f"disposition on a row in some other state")

        verified_before = cells[-4]
        cells[-3] = disposition
        if cells[-4] != verified_before:
            fail(f"row {num}: the verified-state cell changed -- refusing to write")

        rebuilt = (f"| {num} | "
                   + " | ".join(c.replace("|", r"\|") for c in cells) + "\n")
        rm = re.match(r"^\|\s*(\d+)\s*\|(.*)$", rebuilt.rstrip("\n"))
        if rm is None or rm.group(1) != num:
            fail(f"row {num} does not survive a rebuild")
        if split_cells(rm.group(2)) != cells:
            fail(f"row {num} does not survive a write/read round-trip")
        lines[idx] = rebuilt
        changed += 1
        print(f"  {num}: 'build: ...' -> measured disposition, state left `open`")

    if changed:
        ISSUES.write_text("".join(lines))
    print(f"\n{changed} row(s) corrected. No state was changed and no commit was invented: "
          f"the features are upstream's, so a `done` here would be a false claim.")

    # Verify with the project's own checkers.
    for name, cmd in (("check-issue-ledgers", ["python3", "docs/check-issue-ledgers.py"]),
                      ("goal-check", ["python3", "docs/goal-check.py"])):
        p = subprocess.run(cmd, cwd=str(REPO), capture_output=True, text=True, timeout=600)
        out = (p.stdout + p.stderr).strip()
        print(f"\n--- {name} (exit {p.returncode}) ---")
        for line in out.splitlines():
            if name == "goal-check" and not re.match(r"^C[0-9]", line):
                continue
            print(line[:240])


if __name__ == "__main__":
    main()
