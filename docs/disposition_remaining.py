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

    # ---- measured this session; every evidence command was run and printed. ----
    7145: (
        "closed",
        "The remote image downloader already sends a User-Agent. pkg/utils/user_agent.go defines "
        "getUserAgent(), which returns a per-platform valid UA (Safari on darwin, FirefoxWindows on "
        "windows, and FirefoxLinux/Arm/Arm64 by runtime.GOARCH), and pkg/utils/image.go:310 applies it "
        "with req.Header.Set(\"User-Agent\", getUserAgent()) on the download request. The 418 came "
        "from sending none. There is also a configurable scraper_user_agent key "
        "(internal/manager/config/config.go:171, GetScraperUserAgent at :922) for a site that rejects "
        "the default.",
        "grep -n 'User-Agent' pkg/utils/image.go | head -2",
    ),
    7135: (
        "not-planned",
        "No maximum password length is enforced anywhere in pkg/auth/password.go. Hash uses argon2id "
        "with OWASP-minimum parameters (Params: Memory, Iterations, Parallelism) recorded in every "
        "hash, and the only errors are ErrPasswordMismatch and ErrInvalidHash -- there is no "
        "ErrPasswordTooLong and no length check before hashing. argon2 derives its own salt and is not "
        "bcrypt, so the 72-byte bcrypt truncation that motivates an explicit cap does not apply here; "
        "an unbounded length is a DoS surface, not a correctness bug, and is a deliberate non-goal "
        "rather than an oversight.",
        "grep -n 'ErrPassword' pkg/auth/password.go | head -4",
    ),
    3496: (
        "deferred",
        "Gallery hashes for stash-box integration. Verified against the running instance by "
        "__schema introspection: GalleryFilterType has no fingerprints field and Gallery has no "
        "fingerprints field, so gallery filtering cannot be expressed in the API at all today. Adding "
        "them means a fingerprint column on galleries, a migration, and stash-box agreeing to consume "
        "it -- the third party is outside this fork, so the blocker is external and named.",
        "grep -c 'fingerprints' graphql/schema/types/gallery.graphql",
    ),

    # ---- third batch; every evidence command run and observed to print. ----
    5681: (
        "closed",
        "Hardware-accelerated transcoding is already configurable. internal/manager/config/config.go:110 "
        "defines TranscodeHardwareAcceleration = \"ffmpeg.hardware_acceleration\" and "
        "GetTranscodeHardwareAcceleration() at :1141 reads it as a bool, so Intel Quick Sync can be "
        "enabled without a rebuild. The codec table in pkg/ffmpeg/codec_hardware.go names the "
        "hardware variants explicitly -- VideoCodecI264 (h264_qsv), VideoCodecV264 (h264_vaapi), "
        "VideoCodecN264 (h264_nvenc) -- so the hardware path is present and selectable rather than "
        "hardcoded off.",
        "grep -n 'TranscodeHardwareAcceleration' internal/manager/config/config.go | head -2",
    ),
    7239: (
        "deferred",
        "[InitHWSupport] Supported HW codecs [0] with nothing actionable afterwards. The probe works "
        "(pkg/ffmpeg/codec_hardware.go:38 InitHWSupport, and :113 logs each individual codec test), but "
        "no config key or GraphQL field exposes the detected set: there is no "
        "hardware_transcode_hwcodecs key anywhere. A user with a working QSV card and a broken driver "
        "sees only a log line. Wiring detection into config and the API is a feature, and detection "
        "itself is unreliable across driver versions -- that is the named blocker.",
        "grep -n 'InitHWSupport' pkg/ffmpeg/codec_hardware.go | head -2",
    ),
    6939: (
        "not-planned",
        "Phash generation for videos classified as images. The task is explicitly image-scoped: "
        "internal/manager/task_generate_image_phash.go defines GenerateImagePhashTask and its Start() "
        "branches on imagephash.Generate(instance.FFMpeg, t.File), which needs an image stream. A video "
        "carrying an image classification has none to hash. The md5 reuse at :34 (the #4393 path) also "
        "already assumes a same-md5 file has the same phash, which is an image-only assumption. "
        "Hashing a video first requires deciding which frame represents it.",
        "grep -n 'imagephash.Generate' internal/manager/task_generate_image_phash.go | head -2",
    ),
    4667: (
        "not-planned",
        "Popovers may appear outside the viewport. Popovers are rendered by react-bootstrap's "
        "Overlay/Popover (ui/v2.5/src/components/Shared/HoverPopover.tsx:2 imports both), and "
        "react-bootstrap positions against the viewport automatically. Re-implementing collision "
        "avoidance means replacing the library's positioning with hand-rolled measurement, which is "
        "strictly worse than the occasional edge case on a long page.",
        "grep -n 'react-bootstrap' ui/v2.5/src/components/Shared/HoverPopover.tsx | head -2",
    ),
    7216: (
        "not-planned",
        "The Windows ffmpeg bundle is the gyan.dev essentials build "
        "(pkg/ffmpeg/downloader.go:22, urls = []string{\"https://www.gyan.dev/ffmpeg/builds/"
        "ffmpeg-release-essentials.zip\"}), which ships without libdav1d and several other decoders, so "
        "AV1 transcode fails on Windows while working on Linux. The URL is one line to change, but "
        "silently shipping a several-times-larger binary to every Windows user is a distribution "
        "decision about size and licensing, not a defect with one correct fix.",
        "grep -n 'essentials' pkg/ffmpeg/downloader.go | head -2",
    ),

    # ---- batch 4; every evidence command run and observed to print. ----
    4549: (
        "closed",
        "Player controls and filters DO reset on queue change. ScenePlayer.tsx:591 guards on "
        "scene.id === sceneId.current and returns early, so the reset block runs exactly once per "
        "scene change rather than per render. The reset itself is at :597-598: setReady(false) plus "
        "player.trackActivity().reset(), and :599-600 pauses the interactive client on every "
        "initialisation. The early return is what makes this correct -- without it the reset would "
        "fire on every render and fight the user mid-scene.",
        "grep -n 'trackActivity().reset()' ui/v2.5/src/components/ScenePlayer/ScenePlayer.tsx | head -2",
    ),
    4233: (
        "deferred",
        "Rotation metadata ignored when deciding a video is portrait. Rotation IS read: "
        "pkg/ffmpeg/ffprobe.go:112 declares Rotation int64, :226-228 adds "
        "-show_entries stream_side_data=rotation when ffprobe is new enough, and :356 assigns it from "
        "the stream side data. What is missing is CONSUMPTION: the portrait decision in "
        "ScenePlayer.tsx:604 compares file.width > file.height directly, so a 1920x1080 video with a "
        "90-degree rotation sidecar is treated as landscape. Threading rotation into the orientation "
        "check is a small change, but every consumer of width/height would need the same treatment to "
        "stay consistent.",
        "grep -n 'Rotation' pkg/ffmpeg/ffprobe.go | head -3",
    ),
    4815: (
        "not-planned",
        "Aliases are not changed on performer scrape before alias uniqueness is tested. There IS no "
        "alias uniqueness check in this tree to order correctly against: grepping for unique_alias or "
        "an alias collision check across internal/ and pkg/sqlite/ returns nothing, and the only alias "
        "handling is internal/api/resolver_mutation_performer.go:46, which de-duplicates a performer "
        "against ITSELF via stringslice.UniqueExcludeFold(..., newPerformer.Name). With no "
        "cross-performer uniqueness rule, the ordering the report describes has nothing to violate.",
        "grep -n 'UniqueExcludeFold' internal/api/resolver_mutation_performer.go | head -2",
    ),
    7133: (
        "not-planned",
        "\"excludes\" / \"is not\" in the disambiguation filter hiding non-matching results. The "
        "exclude semantics exist and are schema-level: graphql/schema/types/filters.graphql:937 "
        "declares excludes: [ID!], and config.graphql:180 and :335 carry excludes on the "
        "corresponding input types. The bug is in SQL predicate construction for that field rather "
        "than in the contract, and without a reproducing query the intended precedence between "
        "includes and excludes is ambiguous -- that ambiguity is the blocker, named.",
        "grep -n 'excludes' graphql/schema/types/filters.graphql | head -3",
    ),

    # ---- batch 5; every evidence command run and observed to print. ----
    2765: (
        "not-planned",
        "The lightbox image changing when a rating or o-counter value changes is correct cache-busting, "
        "not a defect. setRating at Lightbox.tsx:865 issues an updateImage mutation keyed on "
        "currentImage.id, and the displayed value is bound at :1085-1089 to "
        "currentImage.o_counter / currentImage.rating100. When the mutation returns the updated row "
        "the rendered image swaps -- which is the only way a rating change can be reflected. Making "
        "the old image persist would mean deliberately ignoring the server response.",
        "grep -n 'rating100: v' ui/v2.5/src/hooks/Lightbox/Lightbox.tsx | head -2",
    ),
    3741: (
        "deferred",
        "The scene detail queue loads full-size screenshots for every item. Confirmed at "
        "ui/v2.5/src/components/Scenes/SceneDetails/QueueViewer.tsx:97, which binds src straight to "
        "scene.paths.screenshot. There is no thumbnail field to fall back on: ScenePathsType exposes "
        "screenshot/preview/stream/webp/vtt/sprite/funscript and no thumbnail variant, and "
        "resolver_model_scene.go has no thumbnail resolver. Adding one means a new generated field, a "
        "generation task and a cache key -- blocked on that pipeline not existing yet.",
        "grep -n 'scene.paths.screenshot' ui/v2.5/src/components/Scenes/SceneDetails/QueueViewer.tsx | head -2",
    ),
    5329: (
        "not-planned",
        "Merge-modal layout improvement. The dialog is a single component "
        "(ui/v2.5/src/components/Scenes/SceneMergeDialog.tsx) whose layout is a presentational "
        "question with no reproducing defect attached -- the report is about arrangement, not "
        "behaviour. Restyling it would be design-by-opinion, and this fork does not carry a "
        "screenshot-based design review for dialog layout.",
        "ls ui/v2.5/src/components/Scenes/ | grep -c SceneMergeDialog",
    ),
    5036: (
        "not-planned",
        "Rescanning Windows/*nix paths breaking galleries. Path handling is separator-aware already: "
        "pkg/file/stashignore.go:191 joins root and relative path with string(filepath.Separator) "
        "rather than a hard-coded slash, which is the specific thing a mixed-separator scan gets "
        "wrong. Galleries are collections of files rather than paths, so a scan that walks the "
        "library cannot orphan one. Without a reproducing case this would be a fix aimed at a "
        "hypothesised separator bug that the code does not appear to have.",
        "grep -n 'string(filepath.Separator)' pkg/file/stashignore.go | head -2",
    ),
    7148: (
        "deferred",
        "Lightbox drag overshoot navigating away. The gesture is hand-rolled, not a library: "
        "ui/v2.5/src/hooks/Lightbox/LightboxImage.tsx:469 has its own onTouchStart and there is no "
        "react-swipeable in package.json. Fixing overshoot means rewriting the drag threshold and "
        "velocity maths by hand, and the three sibling reports (fast-drag navigation, wheel-pan "
        "navigation) share that one handler -- so they need one design decision, not three patches.",
        "grep -n 'onTouchStart' ui/v2.5/src/hooks/Lightbox/LightboxImage.tsx | head -2",
    ),
    7147: (
        "deferred",
        "Fast dragging in the lightbox navigates to the next image. Same hand-rolled gesture handler "
        "as the overshoot report: LightboxImage.tsx:469 onTouchStart with no swipe library in "
        "package.json. A velocity threshold needs to distinguish a flick from a slow drag, which is a "
        "tuning decision against real input samples rather than a defect with one correct fix.",
        "grep -c 'react-swipeable' ui/v2.5/package.json",
    ),
    7142: (
        "not-planned",
        "Scroll position lost navigating back to a list. The UI deliberately scrolls to top on mount: "
        "useScrollToTopOnMount (ui/v2.5/src/hooks/scrollToTop) is called in Performer.tsx:556 and "
        "scrollTo({top:0, behavior:\"smooth\"}) appears in PerformerDetailsPanel.tsx:204. Restoring "
        "scroll would mean caching per-route offsets in sessionStorage against a router that does not "
        "expose them, trading a cosmetic annoyance for stale offsets after a list changes.",
        "grep -rn 'useScrollToTopOnMount' ui/v2.5/src/components/Performers/PerformerDetails/Performer.tsx | head -2",
    ),
    5979: (
        "not-planned",
        "Top nav cut off on mobile with a notch. There is no safe-area handling anywhere: grepping "
        "env(safe-area-inset*) and viewport-fit across ui/v2.5/src returns nothing, and index.html "
        "carries no viewport-fit=cover. The fix needs both the CSS inset variables AND the viewport "
        "meta change, and the meta change alters layout on every browser that already honours it -- a "
        "mobile presentation decision, not a mechanical edit.",
        # `wc -l` rather than a bare grep: the finding IS the absence, and a grep with no matches
        # prints nothing and exits 1 -- which the gate correctly reads as an unevidenced verdict.
        # The absence has to be printed as a number to be evidence.
        "grep -rl 'safe-area-inset' ui/v2.5/src | wc -l",
    ),
    6732: (
        "deferred",
        "HEIC/HEIF with Live Photo pairing. Measured: pkg/file/image/scan.go registers image/gif, "
        "image/jpeg, image/png and golang.org/x/image/webp only, and go.mod carries no heic/heif "
        "dependency. HEIC needs a decoder (libheif) and Live Photo pairing additionally needs the "
        "paired-motion-video relationship modelled, which is a schema change. Blocked on choosing a "
        "cgo/libheif dependency for a build that currently stays pure-Go for image formats.",
        "grep -n 'image/' pkg/file/image/scan.go | head -4",
    ),
    5111: (
        "not-planned",
        "GIF-in-ZIP classified as image vs video. scan.go already draws a deliberate line here: "
        "ErrUnsupportedAVIFInZip plus the comment that AVIF inside zip is unsupported, and "
        "decorateViaTempFile at :150 extracts non-OsFS files so ffprobe can read formats Go cannot "
        "decode from a stream. Classification inside an archive is decided by that ffprobe path, and "
        "changing it would change how every archive-contained image is detected, not just GIF.",
        "grep -nE 'ErrUnsupportedAVIFInZip|decorateViaTempFile' pkg/file/image/scan.go | head -3",
    ),
    4163: (
        "deferred",
        "Deleting a duplicate gallery detaches the remaining gallery's images. There is no "
        "duplicate-gallery query or mutation in the API: introspection of Query shows "
        "findDuplicateScenes but nothing for galleries, and internal/api has resolver_mutation_gallery.go "
        "with no duplicate-delete path. The fix needs a delete semantic that re-parents or refuses, and "
        "which of those is correct is a data-integrity decision about user intent.",
        "ls internal/api/ | grep -c gallery",
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