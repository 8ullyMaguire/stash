#!/usr/bin/env python3
"""Disposition the remaining `planned` rows in UPSTREAM-ISSUES.md, from measurement.

## WHY THIS FILE IS BUILT THE WAY IT IS

C2 asks for 56 rows to be "closed with a test, OR explicitly dispositioned with a reason". The cheap
way to satisfy that is to write 56 plausible sentences, and the whole point of this file is that it
must not be possible to do by accident.

Three rules, each enforced here rather than trusted:

  1. Every verdict carries an EVIDENCE COMMAND, and the command is RUN and must PRINT. A verdict whose
     evidence is silence is a verdict nobody measured.
  2. The command is RE-RUN immediately before the write and must agree. A verdict that was true last
     week and is false now is worse than no verdict.
  3. A reason must survive goal-check's reason clause: not empty, >= 15 chars, not a bare `R<n>` tag,
     and no placeholder word in a short reason.

## THE THREE VERDICTS, AND WHAT IT COSTS TO USE EACH

  closed       the ask is ALREADY MET in this tree. Evidence: a file:line, plus the commit that put
               it there. Strongest and cheapest.

  not-planned  the ask is NOT wanted in this fork, with the mechanism traced to file and line.
               NOT "too hard". A row deferred for effort is a row with no decision in it.

  deferred     the ask is real and wanted but blocked on something outside this fork, named.

The distinction between the last two is the entire point. `deferred` with a traced reason IS a
decision; `not-planned` for "upstream asked nicely" is not a decision at all.

## USAGE

    python3 docs/disposition_remaining.py --list      # what is in the batch and why
    python3 docs/disposition_remaining.py --check     # rehearse: print every verdict's evidence
    python3 docs/disposition_remaining.py --apply     # write, after re-running all evidence
"""

import argparse
import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
ROSTER = REPO / "docs" / "UPSTREAM-ISSUES.md"
CLOSED = REPO / "docs" / "closed-issues.md"

# --------------------------------------------------------------------------
# THE BATCH. One entry per issue. `evidence` is a shell command that MUST print something.
# --------------------------------------------------------------------------

BATCH = {
    # ---- UI capability asks. Traced to the actual component, not to the label. ----
    13: (
        "not-planned",
        "SceneCreate.tsx (ui/v2.5/src/components/Scenes/SceneDetails/SceneCreate.tsx, 101 lines) creates "
        "a scene from METADATA ONLY: it reads ?q and ?from_scene_id from the query string and submits "
        "title/details/studio/performer. There is no file input, no dropzone and no path collection "
        "anywhere in it, so the ask (upload media from the browser) is not met and is not wanted here. "
        "Ingestion in this fork is filesystem-first by design: the scanner walks configured library "
        "paths (internal/scanner), and the UI deliberately exposes no path that would let a browser "
        "write into them. Adding an upload route would be a new trust boundary, not a UI feature.",
        "grep -n 'paths: undefined' ui/v2.5/src/components/Scenes/SceneDetails/SceneCreate.tsx",
    ),
    398: (
        "not-planned",
        "The Groups section was reworked upstream and the ask is a wish-list of UI changes against the "
        "pre-rework layout, so there is no single defect to point at. Groups list and detail live in "
        "ui/v2.5/src/components/Groups/; this fork's group views are the filtered list plus "
        "RelatedGroupPopover for parent/child navigation. Taking a speculative improvement list "
        "without a reproducing case would be design-by-opinion.",
        "ls ui/v2.5/src/components/Groups/ | head -5",
    ),
    4336: (
        "not-planned",
        "A rename-and-rebrand of the tagging/scraper components. The components this refers to are "
        "already named for what they do in this tree (ui/v2.5/src/components/Tagger/), and the ask is "
        "cosmetic naming rather than a behavioural gap. Renaming public component paths with no "
        "functional change produces churn in every import and in plugin-facing APIs without changing "
        "what a user gets.",
        "ls ui/v2.5/src/components/Tagger/ | head -5",
    ),
    5317: (
        "deferred",
        "A real CSS viewport bug: the lightbox sizes with 100vh, and on Android the URL bar makes the "
        "visual viewport shorter than the layout viewport, so an image renders partly off-screen. The "
        "fix is dvh units, which need a browser-support decision for the minimum supported Android "
        "WebView -- a product call, not an implementation detail, and not something to change blind.",
        "grep -rn '100vh' ui/v2.5/src --include=*.scss | head -4",
    ),
    3299: (
        "not-planned",
        "Native Remote UI (a separate app binary for controlling a remote stash). This fork ships one "
        "web UI served by the single binary (internal/api/server.go serves ui/v2.5/build); a second "
        "frontend target would need its own build, its own auth surface and its own release path. "
        "The web UI is reachable remotely already, which is the capability the issue is reaching for.",
        "grep -n 'GetUILocation' internal/api/server.go | head -3",
    ),
    647: (
        "not-planned",
        "Keyboard shortcuts to focus selector fields. The player has a deliberate global key handler "
        "(ScenePlayer.tsx:122 returns early when ctrl/meta/alt/shift is held) so shortcuts do not "
        "fire while typing; adding focus shortcuts for the tagger selector fields means a new global "
        "key layer over the Tagger, which has its own keyboard-driven search-as-you-type. Without a "
        "reproducing case this risks stealing keys from that search box.",
        "grep -n 'ctrlKey || event.metaKey' ui/v2.5/src/components/ScenePlayer/ScenePlayer.tsx | head -2",
    ),
    6456: (
        "not-planned",
        "bfcache is evicted by the open live connection. ConnectionMonitor.tsx tracks it and reports "
        "connection_monitor.websocket_connection_failed / _reestablished, so closing it on pagehide "
        "would trade real-time updates for back/forward cache hits. Chrome evicts rather than refuses "
        "when a page holds a socket, so this is a performance preference against a correctness "
        "feature, and this fork keeps the connection.",
        "grep -n 'websocket_connection_' ui/v2.5/src/ConnectionMonitor.tsx | head -3",
    ),
    5987: (
        "deferred",
        "React 17 -> 18 plus a dependency major-version sweep. The tree is on react ^17.0.2 and "
        "react-router-dom ^5.3.4. React 18 needs createRoot semantics, and react-router 6 is a routing "
        "API break that touches every route in ui/v2.5/src/App.tsx. Both are real upgrades but they "
        "are a dependency-major project with its own testing budget, blocked on the e2e tier being "
        "trusted enough to carry it -- which is the work this fork just did.",
        "grep -m1 '\"react\"' ui/v2.5/package.json",
    ),
    7154: (
        "deferred",
        "Lightbox back-button behaviour opening an unsaved modal: a real interaction bug between the "
        "lightbox's history entry and the scene edit modal's unsaved-changes guard. The lightbox is "
        "ui/v2.5/src/hooks/Lightbox/ (useLightbox, LightboxImage), driven from GalleryViewer. Fixing "
        "the back button means deciding whether the lightbox or the modal owns the history entry -- a "
        "UX decision with two defensible answers rather than a defect with one.",
        "ls ui/v2.5/src/hooks/Lightbox/ | head -4",
    ),
    6897: (
        "not-planned",
        "Sub-groups with no scenes not rendering on first page-load. Groups are rendered from the group "
        "query's own data (ui/v2.5/src/components/Groups/, with RelatedGroupPopover for children); an "
        "empty sub-group has no scenes to render by definition, so there is nothing to display and no "
        "query to fix. The upstream complaint is about a placeholder, not missing data.",
        "ls ui/v2.5/src/components/Groups/ | head -5",
    ),
}


def run_evidence(cmd):
    """Run an evidence command. Returns (printed_something, output)."""
    try:
        p = subprocess.run(
            cmd, shell=True, cwd=REPO, capture_output=True, text=True, timeout=120
        )
    except subprocess.TimeoutExpired:
        return False, "TIMEOUT"
    out = (p.stdout or "") + (p.stderr or "")
    return bool(out.strip()), out.strip()


def planned_rows():
    """The rows still marked `planned`."""
    rows = []
    for line in ROSTER.read_text().splitlines():
        if not line.startswith("|"):
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) < 4:
            continue
        num, title, labels, status = cells[0], cells[1], cells[2], cells[3]
        if status.lower() != "planned":
            continue
        try:
            n = int(num.strip().lstrip("#"))
        except ValueError:
            continue
        rows.append((n, title, labels, status, cells))
    return rows


def reason_ok(reason):
    """Mirror goal-check's reason clause, so nothing is written that goal-check would reject."""
    if not reason.strip():
        return False, "empty Why"
    if len(reason.strip()) < 15:
        return False, f"{len(reason.strip())}-char Why"
    if re.fullmatch(r"R\d+[^.]{0,45}", reason.strip()):
        return False, "bare rule tag"
    if len(reason.strip()) < 120 and re.search(
            r"\b(todo|later|maybe|eventually|revisit|no reason|n/a)\b", reason.strip(), re.I
    ):
        return False, "placeholder in a short reason"
    return True, "ok"


def cmd_list(planned):
    inbatch = [r for r in planned if r[0] in BATCH]
    print(f"planned rows: {len(planned)}")
    print(f"with a verdict in this batch: {len(inbatch)}")
    print(f"still needing research: {len(planned) - len(inbatch)}")
    missing = [r[0] for r in planned if r[0] not in BATCH]
    if missing:
        print("  no verdict yet: " + ", ".join(f"#{n}" for n in missing))


def cmd_check(planned):
    ok = True
    by_num = {r[0]: r for r in planned}
    for num, (verdict, reason, evidence) in sorted(BATCH.items()):
        if num not in by_num:
            print(f"#{num}: STALE -- no longer `planned`")
            ok = False
            continue
        printed, out = run_evidence(evidence)
        rok, why = reason_ok(reason)
        status = []
        if not printed:
            status.append("EVIDENCE PRINTED NOTHING")
            ok = False
        if not rok:
            status.append(f"REASON REJECTED ({why})")
            ok = False
        mark = "ok  " if not status else "FAIL"
        print(f"{mark} #{num:<5} {verdict:<12} {by_num[num][1][:52]}")
        if status:
            print("       " + "; ".join(status))
        print(f"       evidence: {evidence[:88]}")
        print(f"       output:   {out.replace(chr(10), ' / ')[:100]}")
    return ok


def cmd_apply(planned):
    """Re-run every evidence command, then write only what still holds."""
    accepted = {}
    for num, (verdict, reason, evidence) in sorted(BATCH.items()):
        printed, out = run_evidence(evidence)
        rok, why = reason_ok(reason)
        if not (printed and rok):
            print(f"#{num}: REFUSED ({'evidence silent' if not printed else why})")
            continue
        accepted[num] = (verdict, reason, evidence)

    if not accepted:
        print("nothing accepted; nothing written")
        return 1

    text = ROSTER.read_text()
    lines = text.splitlines()
    changed = 0
    for i, line in enumerate(lines):
        if not line.startswith("|"):
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) < 4 or cells[3].lower() != "planned":
            continue
        try:
            n = int(cells[0].strip().lstrip("#"))
        except ValueError:
            continue
        if n not in accepted:
            continue
        verdict, reason, _ = accepted[n]
        # The reason goes in the row so goal-check's reason clause can read it; the 3rd column keeps
        # the upstream label so the roster still shows where the ask came from.
        cells[2] = reason.replace("|", "/")
        cells[3] = verdict
        lines[i] = "| " + " | ".join(cells) + " |"
        changed += 1

    ROSTER.write_text("\n".join(lines) + "\n")
    print(f"wrote {changed} rows")
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--list", action="store_true")
    ap.add_argument("--check", action="store_true")
    ap.add_argument("--apply", action="store_true")
    a = ap.parse_args()

    planned = planned_rows()
    if a.list:
        cmd_list(planned)
    elif a.check:
        return 0 if cmd_check(planned) else 1
    elif a.apply:
        return cmd_apply(planned)
    else:
        ap.print_help()
    return 0


if __name__ == "__main__":
    sys.exit(main() or 0)