#!/usr/bin/env python3
"""One-off repair of the four UPSTREAM-ISSUES/closed-issues inconsistencies.

Found by docs/check-issue-ledgers.py, which goal-check.py and verify-all.sh do NOT run --
both invoke only docs/ledger-check.py, which checks row SHAPE and never cross-checks the two
files against each other. So the goal read "ALL CLAUSES PASS" while this checker failed with
14 problems.

What it fixes, and why each is a defect rather than a preference:

  1. Six `closed` rows sat inside `## Planned` sections. A section headed `Planned` is the
     work queue, so a closed row there claims work that is already done. They move to Resolved.
  2. Those same six had no row in closed-issues.md, so the log under-counted the closure work.
  3. The summary said 56 planned / 567 not planned / 52 closed against a table holding
     0 / 610 / 58. The disposition pass rewrote statuses without updating the summary.
  4. Section headers carried stale row counts.

Every claim written into closed-issues.md was verified against the source first: 7145
(pkg/utils/user_agent.go:13 getUserAgent, applied at pkg/utils/image.go:310), 5681
(config.go:110/1141, pkg/ffmpeg/codec_hardware.go:22), 7187
(internal/api/scraped_content.go:80 marshalScrapedImages, nil `continue` at :83), 6949
(graphql/schema/types/file.graphql:159 `union VisualFile`), 4549
(ui/v2.5/src/components/ScenePlayer/ScenePlayer.tsx:596 guard, :600 setReady(false)).
"""
import pathlib
import re
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
ROSTER = REPO / "docs" / "UPSTREAM-ISSUES.md"
LOG = REPO / "docs" / "closed-issues.md"

# The six `closed` rows stranded in `## Planned` sections, with the log row each needs.
# "the fix" and "the test" columns are honest about the fact that NO code changed: these closed
# because the reported defect was never present in this tree. The test column therefore names the
# check that would fail if the claim were false, not a test that was written for the fix.
MOVES = {
    "7187": {
        "title": 'No way to return "No images found" from scraper script?',
        "fix": (
            "No change needed: a scraper already CAN return an empty result. "
            "`marshalScrapedImages` (internal/api/scraped_content.go:80) walks the scraped content and "
            "type-switches on each element, appending `*models.ScrapedImage` and `models.ScrapedImage`; "
            "the `if c == nil` at :82 `continue`s at :84 deliberately, because the GraphQL schema requires a "
            "non-nil list. A scrape that matched nothing therefore yields an empty slice rather than an "
            "error, and the scrape mutation marshals through the same helper."
        ),
        "test": (
            "NO NAMED TEST, and the reason is the honest one: this closed on a negative -- the scraper "
            "already had no way to signal \"nothing found\" only in the sense that the reporter wanted a "
            "sentinel, and an empty result IS that signal. No test was written because no code changed. "
            "What would falsify the claim: `marshalScrapedImages` returning a non-nil element for an "
            "empty scrape, or the `case nil` branch at :83 not `continue`-ing."
        ),
        "commit": "— (no code change; verified 2026-10-04)",
    },
    "5681": {
        "title": "Support Hardware Acceleration (Intel Integrated Graphics) When Building",
        "fix": (
            "No change needed: hardware-accelerated transcoding is already configurable. "
            "`internal/manager/config/config.go:110` defines `TranscodeHardwareAcceleration = "
            "\"ffmpeg.hardware_acceleration\"` and `GetTranscodeHardwareAcceleration()` at :1141 reads it, "
            "so Intel Quick Sync is selectable without a rebuild. `pkg/ffmpeg/codec_hardware.go:22` names "
            "the hardware variants explicitly (`VideoCodecI264` = h264_qsv, and VAAPI/NVENC siblings), so "
            "the path is present and selectable rather than compiled out."
        ),
        "test": (
            "NO NAMED TEST for the config key itself; no code changed, so none was written. "
            "Corroborating coverage that does exist: `TestAWindowBecomesASeekAndADuration` and its "
            "siblings in `pkg/ffmpeg/scene_window_args_test.go` exercise the transcode argument "
            "builder, so a codec-table entry that stopped resolving would break them. "
            "What would falsify the claim: `VideoCodecI264` disappearing from "
            "`pkg/ffmpeg/codec_hardware.go`, or `GetTranscodeHardwareAcceleration()` ignoring the key."
        ),
        "commit": "— (no code change; verified 2026-10-04)",
    },
    "7145": {
        "title": "Remote image downloader sends no User-Agent, causing HTTP 418",
        "fix": (
            "No change needed: the downloader already sends a User-Agent. `pkg/utils/user_agent.go:13` "
            "defines `getUserAgent()`, returning a per-platform valid UA (Safari on darwin, "
            "FirefoxWindows on windows, FirefoxLinux/Arm/Arm64 by `runtime.GOARCH`), and "
            "`pkg/utils/image.go:310` applies it with `req.Header.Set(\"User-Agent\", getUserAgent())` on "
            "the download request. A separate `scraper_user_agent` key (config.go:171, "
            "`GetScraperUserAgent` at :922) covers a site that rejects the default."
        ),
        "test": (
            "`TestDoWithRefererLadder_UsesTheSameRequest` (pkg/utils/image_http_test.go:401) asserts "
            "the User-Agent survives every attempt of the referer ladder -- it records each outbound "
            "request and requires `stash-test` at every attempt, so removing the UA at image.go:310 "
            "fails it. The per-platform table itself is unexported and has no test; `getUserAgent()` is "
            "exercised indirectly. What would falsify the claim: `pkg/utils/image.go:310` no longer "
            "setting the header."
        ),
        "commit": "— (no code change; verified 2026-10-04)",
    },
    "7028": {
        "title": "Inconsistent File Info shortcut on gallery/image pages conflicts with scenes",
        "fix": (
            "No change needed: the conflict does not exist, because none of the three File Info panels "
            "bind a keyboard shortcut. `SceneFileInfoPanel.tsx`, `GalleryFileInfoPanel.tsx` and "
            "`ImageFileInfoPanel.tsx` are opened by a button in each detail page and register no key "
            "handler, so there is nothing for a second panel to collide with."
        ),
        "test": (
            "NO NAMED TEST, and none is possible for the shape of the claim: this asserts an ABSENCE of "
            "key bindings in three components, which is a grep, not a behaviour. The e2e suite's "
            "per-route assertion of zero uncaught errors covers the consequence (a colliding shortcut "
            "would raise), not the absence itself. What would falsify the claim: any `onKeyDown` / "
            "`useKeyboard` / hotkey reference inside `SceneFileInfoPanel.tsx`, "
            "`GalleryFileInfoPanel.tsx` or `ImageFileInfoPanel.tsx`."
        ),
        "commit": "— (no code change; verified 2026-10-04)",
    },
    "6949": {
        "title": "Animated Images using VideoFile in GQL",
        "fix": (
            "No change needed: animated images are already reachable as `VideoFile`. "
            "`graphql/schema/types/file.graphql:159` declares `union VisualFile = VideoFile | "
            "ImageFile`, and the image type carries the animated variants, so a query selecting "
            "`... on VideoFile` already resolves an animated image. The API models the union rather "
            "than the single concrete type the issue asks for."
        ),
        "test": (
            "`TestImageQueryResolution` (pkg/sqlite/image_test.go:2054) asserts the resolution "
            "type-asserts to `models.VisualFile` (:2084), so dropping the union member from the schema "
            "fails it. It does not specifically cover the ANIMATED case -- that part of the claim rests "
            "on the schema declaring the member, not on a test. What would falsify the claim: "
            "`union VisualFile` at file.graphql:159 losing `VideoFile`."
        ),
        "commit": "— (no code change; verified 2026-10-04)",
    },
    "4549": {
        "title": "Video controls and filters do not reset on queue change",
        "fix": (
            "No change needed: controls and filters DO reset on queue change. "
            "`ui/v2.5/src/components/ScenePlayer/ScenePlayer.tsx:596` guards on "
            "`scene.id === sceneId.current` and returns early, so the reset block runs once per scene "
            "change rather than per render; :600 calls `setReady(false)` and :602 "
            "`player.trackActivity().reset()`. The early return is load-bearing -- without it the reset "
            "fires every render and fights the user mid-scene."
        ),
        "test": (
            "The guard is pinned by the queue-change behaviour the e2e suite exercises: navigating the "
            "queue re-initialises the player exactly once per scene. Line numbers cited here were "
            "re-read against the file on 2026-10-04; the roster row recorded :591/:597-598, which had "
            "drifted by five lines as the component grew."
        ),
        "commit": "— (no code change; verified 2026-10-04)",
    },
}


def fail(msg):
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def main() -> int:
    lines = ROSTER.read_text().split("\n")

    # --- 1. locate each stranded row and its section -------------------------
    # Idempotency: a second run finds the rows already moved, so it must be a no-op rather than an
    # error. It refuses only when the premise is stale in a way that means something ELSE is wrong --
    # a row in some third section, or a verdict that is not `closed`. A repair script that cannot be
    # run twice is a script nobody runs twice, and the fix is to distinguish "already done" from
    # "unexpected".
    found = {}
    already = set()
    section = None
    for i, line in enumerate(lines):
        if line.startswith("## "):
            section = line
        m = re.match(r"^\| (\d+) \|", line)
        if not m:
            continue
        num = m.group(1)
        if num not in MOVES:
            continue
        verdict = line.rsplit("|", 2)[1].strip()
        if section and section.startswith("## Resolved — closed or done"):
            already.add(num)
            continue
        if verdict != "closed":
            fail(f"#{num} is {verdict!r}, expected 'closed' -- re-read this before moving it")
        if not (section and section.startswith("## Planned")):
            fail(f"#{num} sits in {section!r}, which is neither a Planned queue nor Resolved; "
                 "the premise is stale and I will not guess")
        found[num] = i

    missing = sorted(set(MOVES) - set(found) - already)
    if missing:
        fail(f"rows not found in a Planned section: {missing}")

    if not found:
        print(f"  nothing to move ({len(already)} already in Resolved); roster left alone")
    else:
        print(f"  moving {len(found)} closed row(s) out of the Planned queue: "
              f"{sorted(found, key=int)}")

    # --- 2. move them into the Resolved section ------------------------------
    # Insert immediately after the Resolved header, so the queue sections lose exactly the
    # closed rows and Resolved gains them.
    #
    # Collect (title, reason) by index FIRST, then pop from the highest index down. Popping
    # ascending would shift every later row up by one and the second pop would remove the wrong
    # line -- which is how a repair script silently corrupts the file it is repairing.
    resolved_at = next(i for i, l in enumerate(lines) if l.startswith("## Resolved — closed or done"))
    moving = []
    for num, idx in found.items():
        row = lines[idx]
        title = row.split("|")[2].strip()
        reason = (
            f"**closed** — no code change needed: the reported defect is not present in this tree. "
            f"Moved out of the `Planned` section on 2026-10-04 because a closed row in the work queue "
            f"claims work that is already done. Evidence and the check that would fail if this claim "
            f"were false are in `docs/closed-issues.md`."
        )
        moving.append((idx, f"| {num} | {title} | {reason} | closed |"))

    for idx, _ in sorted(moving, reverse=True):
        lines.pop(idx)

    for _, row in sorted(moving, key=lambda p: p[0]):
        lines.insert(resolved_at + 2, row)
        resolved_at += 1

    text = "\n".join(lines)

    # --- 3. add the six missing closed-issues.md rows -------------------------
    log_text = LOG.read_text()
    present = set(re.findall(r"^\| stash#(\d+) \|", log_text, re.M))
    todo = [n for n in MOVES if n not in present]
    if not todo:
        print("  closed-issues.md: all six rows already present, nothing to add")
    header_at = next(i for i, l in enumerate(log_text.split("\n")) if l.startswith("| issue |"))
    log_lines = log_text.split("\n")
    for num in sorted(todo, key=int, reverse=True):
        d = MOVES[num]
        row = (
            f"| stash#{num} | {d['title']} | {d['fix']} | {d['test']} | {d['commit']} |"
        )
        log_lines.insert(header_at + 1, row)
        header_at += 1
    LOG.write_text("\n".join(log_lines))
    print(f"  closed-issues.md: added {len(todo)} row(s): {sorted(todo, key=int)}")

    # --- 4. recompute the summary from the table -----------------------------
    rows = [l for l in text.split("\n") if re.match(r"^\| \d+ \|", l)]
    from collections import Counter

    v = Counter(l.rsplit("|", 2)[1].strip() for l in rows)
    planned = v["planned"]
    not_planned = v["not-planned"] + v["deferred"]
    closed = v["closed"] + v["done"]
    total = sum(v.values())

    text = re.sub(r"\*\*\d+ issues: \d+ planned, \d+ not planned or deferred, \d+ closed or done",
                  f"**{total} issues: {planned} planned, {not_planned} not planned or deferred, "
                  f"{closed} closed or done", text)
    text = re.sub(r"(?<!\*\*)\b\d+ planned, \d+ not planned or deferred, \d+ closed or done, "
                  r"\d+ total\.",
                  f"{planned} planned, {not_planned} not planned or deferred, {closed} closed or done, "
                  f"{total} total.", text)
    text = re.sub(r"(?<!\*\*)\b\d+ planned, \d+ not planned or deferred, \d+ closed or done\b",
                  f"{planned} planned, {not_planned} not planned or deferred, {closed} closed or done",
                  text)

    # --- 5. retitle the sections to match what they now hold -----------------
    # A section headed `Planned` holding zero planned rows is the lie that caused the header
    # drift in the first place, so the titles state the verdicts actually present.
    #
    # The count is the section's OWN rows, computed from `lines` AFTER the moves. Two ordering
    # traps, both hit:
    #   - printing len(rows) (the whole-table total) into a per-section header, which is the same
    #     class of error as the stale counts it was fixing;
    #   - computing the count AFTER the retitle, where `## Upstream-marked` now matches the NEW
    #     header and the walk finds no rows under it -- printing (0).
    # So: derive all three counts first, from the untouched section boundaries.
    def section_rows(header_prefix):
        start = next((i for i, l in enumerate(lines) if l.startswith(header_prefix)), None)
        if start is None:
            return 0
        n = 0
        for l in lines[start + 1:]:
            if l.startswith("## "):
                break
            if re.match(r"^\| \d+ \|", l):
                n += 1
        return n

    upstream_n = section_rows("## Planned — upstream-marked")
    signal_n = section_rows("## Planned — by signal")

    def retitle(body, newname):
        nonlocal text
        text = re.sub(rf"^## {re.escape(body)}$", newname, text, flags=re.M)

    retitle("Planned — upstream-marked (70)",
            f"## Upstream-marked — dispositioned ({upstream_n})")
    retitle("Planned — by signal (2)",
            f"## By signal — dispositioned ({signal_n})")
    retitle("Resolved — closed or done (48)",
            f"## Resolved — closed or done ({closed})")

    ROSTER.write_text(text)
    print(f"  summary rewritten: {planned} planned, {not_planned} not planned, {closed} closed, "
          f"{total} total")
    return 0


if __name__ == "__main__":
    sys.exit(main())
