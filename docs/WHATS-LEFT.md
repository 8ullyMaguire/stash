# stash — what is left (measured 2026-10-02, `docs/goal-check.py`)

**The predicate is the authority.** `python3 docs/goal-check.py` is the repo's own
completion test; this file is a reading of it, not a substitute. Re-run it before
believing anything here — the numbers move, and a number copied into a summary is
not evidence.

Repo `~/code-local/go/stash`, branch `main` (the soft fork of `8ullyMaguire/stash`).
Two remotes, both pushed: `origin` (GitHub), `forgejo`.

**Host note:** this repo lives on **gaming-pc, which is this machine**
(`cachyos-B450`). `ssh gaming-pc` returns to the same box, so there is no
second-host gate for stash. `ssh thinkcentre` does **not** have this repo
(`~/code-local/go/stash` does not exist there) — verified, not assumed. Do not
spend a build lane on it.

## Status: 2 clauses outstanding

Measured by `python3 docs/goal-check.py` and `python3 docs/check-issue-ledgers.py` after
commit `65422cea9`. Re-run both before believing this table.

| Clause | Verdict | What it says |
|---|---|---|
| C1 PRs decided | PASS | all 64 open PRs have a recorded decision |
| **C2 issues dispositioned** | **FAIL** | **80 `planned` rows in `docs/UPSTREAM-ISSUES.md` are neither closed nor re-statused** (was 82; #3530 moved to Resolved in `af5ea1a83`) |
| C3 M5 tagged | PASS | `m5-p2p-downloader`, reachable from main |
| C4 M7/M8 done | PASS | `m7-mesh`, `m8-relay-mesh` |
| C5 requirements.csv | PASS | 90 rows: 80 `tested`, 6 `shipped`, 4 `deferred` |
| C6 branch convention | PASS | single-branch layout, nothing stranded |
| C7 suites | PASS | 60 packages unit, 1 integration |
| **C8 backlog-17 ledger** | **FAIL** | **1 of 17 rows remains: #2747** — #3530 went `done` in `af5ea1a83` (its `done` row carries the commit as evidence, which is what the clause requires). #4326 and #1790 blank-state but closed; verified by parsing the `state` column |

`check-issue-ledgers.py`: **OK** — header, roster table and closed log agree (675 issues,
34 closed, 34 log rows).

## What moved since the last reading of this file

**#1790 is DONE** (commits `62313ad60`, `1501fe43b`, `2974ed15a`; tag `stash-1790-done`).
Generic external IDs: a source registry plus one polymorphic `external_ids` table. Proven
generic by gallery — the one entity with no legacy `stash_ids` table — adopting external
ids through the same code path with no migration. 26 tests and a 14/14 mutation gate
(`docs/mutate_external_id.py`, exit 0).

**#837 is DONE** and its API layer is verified, not assumed: 13 passing tests over
`internal/api/routes_issue.go` + `resolver_issue.go` covering list, badge count,
resolve, restore, 404 and 400.

**Three `build:` dispositions were FALSE and are corrected** (commit `65422cea9`). Rows
2747, 3530 and 4326 each claimed a build while their state said `open`. Measured with
`git log --diff-filter=A`: 2747's `ExternalPlayerButton` came from upstream `3d1b949f4`
(#679) and 3530's `stream_segmented.go` from upstream `05669f550` (#3274) — the features
are present, but this fork did not build them, and no fork commit carries those numbers.
**4326 is not built anywhere**: the only `Related*` components are upstream group/sub-group
relations (#5105), unrelated to browsing content during playback.

## The three that remain, and what each actually needs

**#4326 — browse related content during video playback.** The one genuine implementation
task of the three. Needs a panel that overlays the player and navigates without a route
change, so playback is uninterrupted. No such component exists; nothing to correct.

**#3530 — multiple scenes in a single file.** Note the trap: `pkg/ffmpeg/stream_segmented.go`
looks like this feature and is not. It segments the HLS **video stream**; the issue asks for
one file holding several **scenes**. Spec + plan written (`docs/ISSUE-3530-{spec,plan}.md`),
decision made and **partly built**:

    DONE  data model      migration 122, start_time/end_time on scenes_files (9871247b5)
    DONE  derived length  GetFiles applies the window; 10 tests, mutation 8/8 (726ef5bf4)
    DONE  aggregates      5 sites use one SQL constant; no double-count; 4/4 sites proven
                          by mutation (2be0d52e3, tag stash-3530-duration)
    DONE  dup detection   split files no longer self-duplicate; both branches; 6/6 killed
                          (a444dc6c8, tag stash-3530-dupes)

**The measurement that chose the model:** `scenes_files` has `PRIMARY KEY (scene_id, file_id)`
and only a NON-unique index on `file_id`, so the schema **already** permits many scenes per
file. The referential half was never missing — only *which part of the file* was, so the
change is two nullable floats rather than segment entities. Under segment-as-file, every
`video_files.duration` read would need reconciling with "a file is now a slice of one"; under
time ranges that column stays **true** and only scene-level views change.

**The measurement that corrected the spec:** a scene has NO duration field of its own. §8
called `scene.go:1135` the site that decides the feature; it actually feeds
`FindScenes.duration`, an aggregate. The chokepoint is `SceneStore.GetFiles`, because the UI
reads `scene.files[0].duration` directly (`SceneListTable.tsx:88`).

**#3530 remains, in this order:**
1. **the player + play URL** — DONE, all of it: `/stream.mp4|webm|mkv` (`-ss`/`-t`),
   `/stream` (307/409), and HLS + DASH manifests (ecd659eb3, tag stash-3530-hls). The segment
   cache key now includes the window, which was a real bug: two scenes of one file shared a
   cache directory and served each other's segments.
2. **the preview/webp cache key** — DONE. `992e8da69`, tag `stash-3530-previewkey`. `GeneratedChecksum`
   (an opt-in wrapper around `GetHash`, NOT a change to `GetHash`) appends `_w<start>-<end>` to the
   checksum, so two scenes of one file get different previews. Unranged scenes keep their exact
   filename. Sweep 6/6.
3. **sprite/VTT thumbs** — DONE. Tag `stash-3530-sprite`. Spec `docs/ISSUE-3530-sprite-spec.md`.
   Deliberately not a copy of the preview fix, for three reasons recorded in the spec:
   - the VTT cues are a **contract with the player**, not a lookup key;
   - `SlowSeek` works in FRAMES, so the window must be converted via `FrameRate`;
   - `chunkCount` is snapped to a perfect square, so a window inside a long file gets a grid sized
     for the file.

   The spec's one open question — absolute vs window-relative cues — is **resolved by reading the
   player** (`vtt-thumbnails.ts`: `time = percent * player.duration()`), so cues are relative to the
   media element's timeline and window-relative is forced, not chosen.

   What the implementation turned out to need, beyond the spec's four mutations:
   - **`SpritePlan`** (`pkg/scene/generate/sprite_window.go`) so the tile loop, the frame loop and
     the VTT writer all read one decision. The spec's own note — "reverting the CALL in
     `previewVideo` changed no result" — is why the plan is a value the loops destructure rather
     than helpers they call.
   - **`SpriteNeedsFrameSeek` as a free function.** The caller must decide *before* the plan exists,
     because choosing frame seeking triggers a frame RECOUNT and the plan carries the recounted
     count. My first version made it a method, the caller built a throwaway plan to ask it, and that
     plan was built from `videoFile.FrameRate` (the probe's figure, **0 when ffprobe cannot read it**)
     while the real plan used `generator.FrameRate` (resolved). Two rates for one decision; the
     windowed arm reads the rate directly, so a windowed short file with an unreadable rate was
     judged frameless and wrongly refused frame seeking. Fixed by making it free, calling it once,
     and moving `configure()` above the decision — safe because `calculateFrameRate` reads
     `NbFrames`/`VideoStreamDuration` and never `videoFile.FrameCount`.
   - **`LoadPrimaryFileWithWindow` on the sprite task**, because `LoadPrimaryFile` goes through
     `FileStore.Find`, which does not select `start_time`/`end_time` **at all** — MEASURED in
     `pkg/sqlite/scene_window_loader_test.go`, not assumed. It reports no window whatever the row
     says, so the grid comes out tiled across the whole file with every arithmetic test green.
   - **the sprite routes key on `GeneratedChecksum`.** Serving by the plain hash serves the
     *unwindowed* sprite of another scene, at a URL that looks entirely correct.
   - **MUTATION SWEEP 12/12 killed** (`docs/mutate_3530_sprite.py`, exit 0). Four from the spec, and
     eight found while writing it. Two harness defects were fixed rather than accepted: a mutant
     that fails to COMPILE was being counted as a cover (M3's first form left `firstFrame`
     declared and not used), and an interrupted sweep left a mutation on disk, which the next run's
     baseline reported as a source regression. The harness now reports `SKIP` for a non-compiling
     mutant, exits non-zero on any survivor **or any skip**, and restores from an in-memory
     snapshot on interrupt. Proven to report a survivor and exit 1 by adding a probe mutant the
     suite genuinely does not catch.
4. **detection** — needs an upstream discussion, not a guess.
5. **any UI to set a range** — API DONE, FORM NOT YET BUILT. `0b48672f5` (tag `stash-3530-range-api`): `VideoFile` carries `start_time`/`end_time`, `SceneUpdateInput` takes both ends, `SceneStore.SetSceneRange` writes them, and `validateSceneWindow` mirrors the CHECKs with field-naming messages plus the one a CHECK cannot express (`end <= the file's duration`, because a CHECK may not reference another table and `GetFiles` clamps on read). It REFUSES an ambiguous window on a multi-file scene rather than guessing. The store deliberately does not clamp — write verbatim, refuse at the API, so a read-back never disagrees with the request. Sweep 3/3 (`docs/mutate_3530_range_write.py`). **Remaining: the React form only.** Spec `docs/ISSUE-3530-range-ui-spec.md`. The GraphQL `VideoFile` type does not carry `start_time`/`end_time` at all and `SceneUpdateInput` has no field for them, so there is nothing for a component to read or write. The spec designs the schema, the mutation input, the API validation that mirrors the CHECKs, and the form. Its §3 records the constraint that shapes the design: the window lives on `VideoFile`, not `Scene`, so a mutation must name the file — "the scene's window" is not a fact about a scene that may have several. **A PRECONDITION was found and fixed first** (`b604926c0`): a scene rename silently erased the window, because `relatedFilesTable.replaceJoins` is destroy-then-insert and `scenes_files` now carries the window columns. Setting a range through the UI would have been undone by the next title edit. Sweep 2/2 (`docs/mutate_3530_preserve.py`).

   Segment boundaries deliberately stay on absolute multiples of the FILE's 2s grid, so a window
   starting mid-segment has a short first segment. Real players tolerate it, and making the grid
   relative to the window would renumber every segment — out of scope, recorded so it reads as a
   decision rather than an oversight.
6. `scene_filter.go:141` still filters on the file's length. Defensible, now recorded as a
   deliberate choice rather than an oversight.

**#3530's window-erasure finding worth carrying (`b604926c0`):** a range column added to a JOIN table is erased by every `replaceJoins` on that table, silently, because the destroy half loses the columns the insert half does not carry. It is silent because NULL is a legal value ("no window") and the CHECKs still pass — so the failure mode is a scene quietly reverting to the whole file with no error anywhere. Three transferable points: (a) adding data columns to a join table means auditing every `replaceJoins` caller; (b) the existing range tests could not see it because they set the window *after* creating the scene and never updated it again — the window was only ever set, never edited-around; (c) `relatedFilesTable.destroyJoins` filters on `file_id` ALONE, which is right for its callers and wrong for a per-scene delete, because two scenes of one file is the whole point of #3530.

**#3530's duplicate-detection finding worth carrying:** three fixes were needed and the two that
failed did so invisibly. `HAVING COUNT(DISTINCT file_id) > 1` is insufficient (`GROUP_CONCAT`
cannot split a group by file: three segments plus a copy came back `[33 34 35 36]`), and
`GROUP BY phash, file_id` is insufficient the other way (the UI treats each group as an
independent set, so a copy pair becomes two singletons and `COUNT(phash) > 1` drops both).
What works is `GROUP BY phash` plus a WHERE gate on the phash occurring under >1 file_id.

**A known limitation, stated not hidden:** a split file whose phash ALSO occurs on another file
is reported as ONE group with that file. The segments are not duplicates *of each other* but of
the other file, and `[][]*Scene` cannot express a per-pair relation — returning nothing would hide
a real duplicate.

**#3530's own §8b finding worth carrying:** four of the five aggregate call sites were
UNTESTED while the constant's own mutation sweep read 5/5 killed. **Sweeping the constant
proves the rule; only calling it proves the wiring.** The per-site sweep that should have
caught it was itself broken — `replace("SceneRangeDurationSQL", ..., 1)` hit the COMMENT above
the `Sprintf` argument, and the sort assertions were POSITIONAL against lists that carry a
mandatory `COALESCE(sort_name, name, id)` tiebreak, so a tie satisfied the assertion. Now
differential (sort twice, windows swapped, order must change) with the entity names chosen to
contradict the tiebreak. In the `test-driven-development` skill.

**#3530's own spec §7 was wrong twice** (`ALTER TABLE ADD CONSTRAINT` — "invalid", then
"verified and works"), both times because the `sqlite3` CLI is a NEWER SQLite than the pinned
`go-sqlite3 v1.14.22`. The rebuild is required for the INDEXES, not the CHECKs, and the whole
episode is in `db-migration-integrity` now.

**#2747 — Jellyfin-like external remote player.** The upstream button opens a scene in a
local external player. The issue asks for a **remote** player, which the row's old
"config-driven command template" note gestured at and which does not exist. Smaller than
3530; needs a spec too.

**C2 is a separate goal item** from the C8 programme: it concerns the 82 upstream `planned`
rows in the roster, not the 17-row backlog. Dispositioning 82 rows is a documentation
pass with a checker to satisfy, not code.

### "Merge all PRs" — already nothing to merge

Verified live this session, not read from a note:

- our fork `8ullyMaguire/stash`: **0 open PRs**, 0 open issues.
- `gh pr list` returns nothing. The 7268-series PRs are **upstream** `stashapp/stash`,
  not ours.
- forgejo is reachable; `fj pr ls` is not a valid subcommand (`fj pr list` is) — a
  stale habit from an earlier session, corrected.

The real queue is C2's 84 rows plus C8's 5, not a merge backlog.

## C8 — the five remaining rows, in build order

Ordered by dependency, not by issue number. Each is a real feature request; none
is a bug with a one-line fix.

| # | Issue | Build | Done when |
|---|---|---|---|
| **837** | Log potential issues with files, show in a dedicated UI | new table + model + detection + panel | **partially built — see below** |
| **1790** | Generalized support for external IDs | `ExternalID` with a source registry, migrating `StashIDs` | table + registry + migration, `StashIDs` reads through it |
| **3530** | Support multiple scenes in a single file | segments, segment-aware duration and filters | segments table; duration sums the segments |
| **2747** | Jellyfin-like external remote player | config-driven command template, detached, scrubbed env | a configured command runs detached and serves playback |
| **4326** | Browse related content during video playback | overlay panel, no route change, playback uninterrupted | panel opens over playback; `<video>` is not reloaded |

### #837 is halfway — the state to build from

Landed and pushed:

- `docs/ISSUE-837-spec.md`, `docs/ISSUE-837-plan.md` (spec + plan **before** code,
  per the standing workflow).
- `8b1082e85` — migration `120_issues`, `appSchemaVersion` 119→120, schema tests.
- `3797a3ded` — `pkg/models/model_issue.go`, `pkg/sqlite/issue.go`, store tests.
  **13/13 mutations killed.**

**The design finding, so it is not re-derived:** refusing duplicate *dismissed*
rows and allowing a *new* finding of the same kind are contradictory for a unique
index. So the **index** keeps the live-row invariants (a live finding is unique, so
two racing scans cannot both insert) and the **store** keeps the dismissal policy
(`Record` skips a finding with a dismissed row of the same identity). Cost, stated
in the migration rather than hidden: two racing scans *can* both insert a live
duplicate, because the index only refuses a third row.

**What remains for #837** (steps 4–8 of the plan):

1. detection in the scan — four kinds (`duplicate`, `zero_duration`, `zero_size`,
   `no_files`), each with a **negative** test, because a detector that fires on
   everything produces a table nobody reads.
2. `GET /issues` + `POST /issues/{id}/resolve`, and GraphQL for the panel.
3. the `/issues` panel.
4. close it: `docs/ISSUES.md` → done, `docs/closed-issues.md` row, roster row →
   `closed`, then `check-issue-ledgers.py` → OK and `goal-check.py` → C8 green.

## C2 — the 84 planned rows

`docs/UPSTREAM-ISSUES.md` holds 675 rows: **84 planned, 553 not-planned or
deferred, 38 closed**. Every row needs to move out of `planned` with a *reason*,
which means closing it with a test or deferring it under a cited rule. C2 accepts
"closed with a test, OR deferred with a reason" — so triage is a legitimate
outcome for rows that genuinely do not belong in this fork.

**This is the bulk of the remaining work and it is bookkeeping-shaped, not
code-shaped.** The efficient order:

1. Work the 5 C8 rows (real features, above).
2. Sweep the remaining `planned` rows in batches: measure each, then either close
   with a test or defer with a rule citation — one verdict per row, both files
   updated together so they cannot disagree.
3. Run `python3 docs/check-issue-ledgers.py` after every batch. It fails if the
   roster says `closed` without a `closed-issues.md` row, which is the check that
   catches a half-finished close.

**The trap, measured twice in this project:** a row moved from `planned` to
`done` with the commit in the wrong CSV cell is rejected by C8's *positional*
evidence rule. Verified-column rows must **lead** with `**done** — commit ...`.
Also: 34 rows were found sitting inside sections headed `Planned` while marked
closed — the checker now fails on that, so the sections cannot lie again.

## Ledger integrity, already repaired (2026-10-02)

`04fa7dff6`. `docs/UPSTREAM-ISSUES.md` header said 90/554/30 while its own table
held 84/553/32, and `check-issue-ledgers.py` said **OK** — for three separate
reasons, all now fixed and all verified by mutation:

- the header regex demanded a bare "not planned" while the prose deliberately says
  "not planned or deferred";
- it counted `closed` but never `done` (six rows use it);
- nothing checked section membership.

Both header counts and both section counts now reconcile to the table.

## Commits this session

| Hash | What |
|---|---|
| `de1a30ac2` | **#3849 fixed** — gallery sort correlated to the gallery being viewed |
| `e47c5c5eb` | `closed-issues.md` row for #3849 |
| `04fa7dff6` | ledger drift + the checker that missed it |
| `8b1082e85` | #837 migration 120 + schema tests |
| `3797a3ded` | #837 model + store (13/13 mutations killed) |

## Reusable lessons from #3849, which shaped everything after

1. **"Intermittently" usually means insertion order, not chance.** SQLite's plan
   is stable, so a many-to-many whose far side is unconstrained picks the same
   row every time — which is why it was reproducible.
2. **The gallery's file set must be reached through `files.zip_file_id`.**
   `galleries_files` records a gallery's *archive*, so correlating against it
   directly matches nothing, NULLs every key and **reverses** the order — worse
   than the bug.
3. **A flat `A OR B OR C` is wrong where the alternatives are ranked.** The `OR`
   made the join non-unique and the symptom returned verbatim; the failure was a
   non-unique join, not a missing condition.
4. **A characterisation test goes red when the bug is fixed** — so it is half a
   test. Invert the assertion; then it is the specification.
5. **A positive control that fails is the signal.** Two fixtures passed for the
   wrong reason (no `files` rows, so `NotContains` was trivially true) and only the
   control exposed it.
6. **Assert the value the defect corrupts**, not an aggregate that happens to
   agree — one fixture's expected order was the same whether the bug was present
   or fixed, so every mutation survived.
7. **A mutation that "survives" may never have been applied.** An anchor string
   that also occurs in a doc comment means `replace(..., 1)` mutates the comment;
   `-count=1` bypasses the result cache but not reliably the build cache. Both are
   now in the `test-driven-development` skill. An ambiguous anchor is a harness
   defect, not a finding.
8. **`sqlite.Timestamp` is RFC3339 — second precision.** Every idempotence test
   written against a timestamp is vacuous until the stored value is moved out of
   the way first, and the test asserting that move must check its own premise.