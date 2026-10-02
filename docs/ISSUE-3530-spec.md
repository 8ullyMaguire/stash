# stash#3530 — multiple scenes in a single file

**Spec.** Written 2026-10-02, before any code, per the standing workflow.

The data-model decision this issue was blocked on is made here, in §3, on measurements
rather than preference.

## 1. What the row claimed, and what is there

The row now reads:

> the feature is PRESENT but came from UPSTREAM: `pkg/ffmpeg/stream_segmented.go`, 915 lines
> with `makeStreamArgs`, `checkSegments` and `HLSGetCodec`, added by upstream `05669f550`
> "Overhaul HLS streaming (#3274)". **The row previously said `build: segments,
> segment-aware duration and filters`, which attributes upstream work to this fork and names
> no commit.** Segmentation of a single file into multiple scenes — which is what the issue
> title actually asks for — is a different thing from segmenting the HLS VIDEO STREAM.

**That correction stands.** `stream_segmented.go` segments an HLS stream for transcoding. It
takes no scene boundaries and creates no scene rows. Upstream's commit title says what it is.

## 2. The model, measured 2026-10-02

The join table is `scenes_files` (`pkg/sqlite/migrations/32_files.up.sql:103`):

```sql
CREATE TABLE `scenes_files` (
    `scene_id` integer NOT NULL,
    `file_id` integer NOT NULL,
    `primary` boolean NOT NULL,
    foreign key(`scene_id`) references `scenes`(`id`) on delete CASCADE,
    foreign key(`file_id`) references `files`(`id`) on delete CASCADE,
    PRIMARY KEY(`scene_id`, `file_id`)
);
CREATE INDEX `index_scenes_files_file_id` ON `scenes_files` (`file_id`);
CREATE UNIQUE INDEX `unique_index_scenes_files_on_primary` on `scenes_files` (`scene_id`) WHERE `primary` = 1;
```

Three facts follow, and each one decides part of the design:

**F1. The schema ALREADY permits many scenes per file.** The primary key is the *pair*
`(scene_id, file_id)`, and the only uniqueness on `file_id` is a plain non-unique index.
Nothing stops two scene rows referencing one file row today. **The referential half of this
feature is not a schema change.**

**F2. Nothing anywhere carries a time range.** No `start_time`/`end_time` on `scenes_files`,
none on `files`, none on `video_files` (which has `duration` — the WHOLE file's duration).
`grep -n 'start\|end\|offset' 32_files.up.sql` returns one comment line, nothing else.
**The semantic half — *which part of the file* — does not exist and must be added.** This is
the whole of the work.

**F3. `primary` means "this scene's main file", not "this scene's own copy".** It is the
`WHERE primary = 1` partial index that `pkg/sqlite/scene.go:268` filters on to compute
`PrimaryFileID`, `PrimaryFileBasename`, `PrimaryFileOshash`. It answers "which of this
scene's files is the one to play", which is orthogonal to segmentation.

### 2.1 Why `stream_segmented.go` cannot be reused for this

`internal/api/urlbuilders/scene.go:26` builds the play URL as:

```go
fmt.Sprintf("%s/scene/%s/stream", b.BaseURL, b.SceneID)
```

**The URL carries a SCENE id, not a file id and not a byte range.** And `ScenePlayer.tsx`
derives every control from the media element's own duration:

```ts
const duration = player.duration();
```

So the player has no notion of "this scene is 30s of a 45-minute file". Making it work is
not a matter of reusing upstream's segmentation — it is a change to the play URL's meaning,
the player's duration source, and the scene's duration everywhere it is computed. That is
why this is a spec and not a patch.

## 3. THE DATA-MODEL DECISION

Two candidate shapes. Both are implemented in real media managers, and they are not variants
of one another — they answer different questions about what a "scene" *is*.

### Option A — time ranges on the link

Add `start_time` / `end_time` (float seconds, nullable) to `scenes_files`.

- A scene is a **window onto a file**. `duration` becomes `end - start`.
- The file stays one file. Segments are not extracted; nothing is transcoded on disk.
- The player must seek to `start_time` on load and stop at `end_time`.

### Option B — real segment entities

A new `scene_segments` table with its own rows, container/path, and a
`files`-style fingerprint so each segment is scannable, groupable and deletable
independently.

- A scene is **a file of its own**.
- Segments exist on disk and are first-class library objects.

### DECISION: **Option A**, and here is the argument rather than the preference

**1. Option B is not an incremental feature; it is a second library model.** Under B, a
segment is a `files` row, so it must be fingerprinted (`files_fingerprints`), live under a
folder (`parent_folder_id NOT NULL`), participate in the scanner, appear in
duplicate-detection, be deleted by folder cleanup, and appear in `galleries_files` /
`scenes_files` like any other file. The scanner would have to decide whether to re-create a
user's segments on every scan or adopt existing ones — and a wrong answer either destroys
user data or duplicates it. That is a large, irreversible change to the core library model
for a feature the issue asks for as a convenience.

**2. Option B's write path is the part nobody can undo.** Producing segments means
**transcoding or copying media on disk**. If the transcode is interrupted, the library is
left holding segments that are shorter than their recorded durations. If a user deletes the
parent file, `on delete CASCADE` removes the scene rows and the segments become orphans on
disk. Every one of those is silent data loss or silent disk growth. Option A writes **no
media at all**.

**3. Option A is additive and reversible.** Adding two nullable columns touches no existing
row. `NULL` means "the whole file", which is what every existing scene means — so the
migration is a no-op for every current user, and dropping the columns restores the previous
schema. **A feature that can be rolled back by dropping two columns is a different kind of
commit from one that rewrites a library's media.**

**4. The player change is the same size either way.** Both options need the player to seek
to a start and stop at an end. Option B does not save that work; it adds the scan, the
transcode, and the reconciliation on top.

**The cost of A, stated plainly, and it is WORSE than a fingerprint approximation.**

`FindDuplicates` (`pkg/sqlite/scene.go:1502`) joins `files_fingerprints` on
`scenes_files.file_id ... AND type = 'phash'` — the phash belongs to the **file**, so two
segments of one file share it *identically*, not approximately. The grouping is:

```sql
GROUP BY phash
HAVING COUNT(phash) > 1
    AND COUNT(DISTINCT scene_id) > 1
```

`COUNT(DISTINCT scene_id) > 1` is **already true** for two segments of one file. So Option A
does not merely make duplicate-detection imprecise — **it makes the feature actively harmful:
splitting one file into scenes would make every scene in it show up as a duplicate of every
other, and `FindDuplicates` is what the duplicate-checker UI and the cleanup tooling call.**

Note the last clause is also the exact place to fix it: a segment pair is not a duplicate
pair, and the query already has both `scene_id` and `scenes_files.file_id` in scope from the
joins above. The fix is a `COUNT(DISTINCT file_id) > 1` alongside the existing
`COUNT(DISTINCT scene_id) > 1` — two segments of one file then have one distinct file and
are excluded, while two genuinely different files with the same phash still match. **R5 is
therefore a one-clause change to an existing query, and its absence is a bug this feature
would introduce, so it is in scope rather than deferred.**

## 4. Requirements

**R1. A scene may name a time range within its file.** `NULL` start means "from the
beginning"; `NULL` end means "to the end". Both `NULL` must behave exactly as today, for
every existing row.

**R2. Duration is derived, never stored.** A scene's duration is `end - start`, and for an
existing scene it must be **numerically identical** to today's value. A migration that
changes anyone's scene duration is a bug, not a rounding.

**R3. The player honours the range.** Seek to `start` on load; do not play past `end`.
Playback ending at `end` must fire the scene's normal completion path, not the file's.

**R4. The play URL can express a range.** R3 needs the browser to be told; the current URL
(`/scene/:id/stream`) carries no file id and no range.

**R5. Duplicate detection must not report two segments of one file as duplicates.** The
consequence of §3, recorded as a requirement rather than discovered as a bug.

**R6. Detection.** How does a scan discover that a file holds several scenes? A scene
bounding box, a companion metadata file, or a manual user action. **This is the second open
decision and it is deliberately not made here** — it needs an upstream discussion, not a
guess. Step 1 below implements the *model* (R1, R2, R5), which is what R6 would populate.

## 5. Definition of done for step 1 (the model only)

- Migration adds `start_time`/`end_time` to `scenes_files`, both nullable, both `CHECK >= 0`,
  and `CHECK (end_time IS NULL OR start_time IS NULL OR end_time > start_time)`.
- Every existing scene's duration is **bit-identical** before and after, asserted by a test
  that writes a scene, migrates, and compares — not by asserting the migration ran.
- A scene with a range reports `end - start`.
- Two scenes sharing a `file_id` are **not** reported as duplicates, and two DIFFERENT files
  with the same phash still are — asserted both ways, because a fix that excluded all
  same-file pairs by disabling the phash join would pass the first assertion and silently
  break duplicate detection for every user.
- The bad rows are **written** to prove the CHECKs fire (a `pg_constraint`-style "it exists"
  proof does not prove it fires — the rule from #1790).

## 6. Not in scope, and why

- **Option B.** §3. If the user wants real segment files, that is a separate project with its
  own spec, and it should be chosen knowingly rather than by default.
- **Automatic detection (R6).** Needs an upstream conversation. Until then the feature is
  manual: a user marks the range.
- **The UI for marking a range.** A scrubber over the player, which is a real component
  (`abLoop` already exists in `ScenePlayer/`) but is its own step.
- **Transcoding segments on disk.** See §3's reason 2.
- **Browser verification.** No configured stash instance on this host. Every claim below is
  about the schema and the query layer under the shipped libraries.

## 7. Implementation notes

**CORRECTION (2026-10-02, after implementing it).** An earlier draft of this spec claimed
`ALTER TABLE ... ADD CONSTRAINT` is not valid SQLite and that a table rebuild would be
required — and then a second draft claimed the opposite, that no rebuild is needed, because
the `sqlite3` CLI accepted the statement. **Both were wrong for the same reason: the CLI is a
newer SQLite than the app runs.** stash pins `go-sqlite3 v1.14.22` (bundled SQLite 3.45.1),
where the statement fails with `near "CONSTRAINT": syntax error`, while the CLI and v1.14.52
both accept it. `ALTER TABLE ... ADD COLUMN … CHECK` *is* accepted on the pinned version.

**A rebuild is therefore required, and its real cost is the indexes:** `DROP TABLE` takes
`index_scenes_files_file_id` and `unique_index_scenes_files_on_primary` with it, and the
latter is what guarantees a scene has exactly one primary file. Migration 122 recreates both
by hand and tests them. Recorded here because the wrong claim was written with the same
confidence as the right ones, and the `sqlite3` CLI accepting a statement is not evidence.

Measured while writing this, and the reason the plan is not a patch:

- **The join table is `scenes_files`, not `files_scenes`** — grepping for the wrong name
  returns nothing at all, which reads as "there is no join table" rather than "wrong name".
- **`primary` is NOT the thing to change.** It means "this scene's main file"
  (`pkg/sqlite/scene.go:268`, the `WHERE primary = 1` partial index behind `PrimaryFileID`).
  Misreading it as "this scene's own segment" is the obvious first mistake here.
- **The play URL carries a scene id only** (`internal/api/urlbuilders/scene.go:26`), so R4 is
  not optional and cannot be skipped as "the player already knows".

## 8. The duration sites — RECOUNTED, because there are five and the important one is not a scene query

§8 originally said "three sites", listing `scene.go:1135`, `1136` and `951`. **That was
measured from the wrong question** — "which scene-query column carries a duration?" — and it
missed the site users actually look at.

**A scene has NO duration field of its own.** Measured:

    type Scene @publicRead {     graphql/schema/types/scene.graphql:44
      ...
      play_duration: Float       <- seconds actually WATCHED, not length
      files: [VideoFile!]!       <- the length lives on the FILE
    }

`duration: Float` in that file is at line 4, on `type SceneFileType` — **per file**, not per
scene. And the UI reads it straight off the file, bypassing any scene query at all:

    SceneListTable.tsx:86   const file = scene.files.length > 0 ? scene.files[0] : undefined;
    SceneListTable.tsx:88   return file?.duration && TextUtils.secondsToTimestamp(file.duration);
    SceneCard.tsx:518       duration={file?.duration ?? undefined}

So a scene's displayed length is `VideoFile.Duration` — `pkg/models/model_file.go:284` —
and a **file is shared between scenes**, which is the whole point of #3530. A range cannot
live on the file, because the same file means different lengths in different scenes.

### The five sites, and what each one is for

| # | site | what it feeds | needs the range? |
|---|---|---|---|
| 1 | `pkg/models/model_file.go:284` `VideoFile.Duration` | **what the user sees** — list, card, detail | **YES**, per scene |
| 2 | `pkg/sqlite/scene.go:1135` `COALESCE(video_files.duration, 0) as duration` | `FindScenes.duration` — the **AGGREGATE** total, not per-scene | **NO** (see below) |
| 3 | `pkg/sqlite/scene.go:951` `Duration()` — `SUM(video_files.duration)` over the library | library total | **YES** |
| 4 | `pkg/sqlite/scene_filter.go:141` filter on `video_files.duration` | `duration` filter criterion | **NO** — see the trap |
| 5 | `pkg/sqlite/file.go:962` the same expression for FILE queries | `FindFiles.duration` | **NO** — a file has no range |

**Site 2 is the aggregate, and this is the finding that changes the design.** Look at
`internal/api/resolver_query_find_scene.go:112`: `result.TotalDuration += f.Duration` — and
the GraphQL field it fills is `FindScenesResult.duration`, documented as *"Total duration in
seconds"* (`scene.graphql:232`). **There is no per-scene duration in `FindScenes` at all.**
So `scene.go:1135` is a sum input, not a per-row value, and the spec's claim that "line 1135
is the one that decides whether the feature works" was wrong.

**Site 4 is a trap worth naming.** `floatIntCriterionHandler(sceneFilter.Duration,
"video_files.duration", …)` filters on the FILE's length. A user filtering "under 5 minutes"
after splitting a file into four 5-minute scenes gets the 45-minute file and sees nothing.
**Filtering by the file's length is defensible — that is what the user is asking about when
they sort by size** — but it must be a DELIBERATE choice recorded here, not an oversight. It
is left unfiltered in this step and flagged in §6 as a known inconsistency, because changing
filter semantics for every existing user is not a decision this issue gets to make silently.

### The expression, and the rule it encodes

    // sceneRangeDuration reduces NULL/NULL to exactly video_files.duration, which is what
    // makes migration 122 a no-op for every pre-existing scene.
    CASE WHEN scenes_files.start_time IS NULL AND scenes_files.end_time IS NULL
         THEN video_files.duration
         ELSE COALESCE(scenes_files.end_time, video_files.duration)
            - COALESCE(scenes_files.start_time, 0)
    END

**Site 1 cannot use this expression**, because `VideoFile` is built from the FILE's tables
and has no `scenes_files` row in scope — a file shared by two scenes is one row. So site 1
needs the range applied where the scene→files association exists, and that query is the work
of this step rather than a string substitution. **A file-level duration column is the wrong
place for a scene-level answer, and that is precisely the trap this issue walks into.**

### The cost of A, revisited now that this is measurable

Under A, a range is not a file, so:

- the scene's **screenshot** is whatever the file's thumbnail generator picked — usually
  frame 0, which for a segment starting at 40s is the wrong image;
- **duplicate detection** groups on the file's phash (spec §3's reason 4) — fixed by one
  clause, still required;
- **`video_files.duration` stays the file's**, so anything reading the file directly (site 5,
  `FindFiles`, the scene detail page's file list) is correct and must NOT be changed. A file
  genuinely does last as long as it lasts.

**That last point is the strongest argument for A over B and it is only visible now:** with
time ranges, `video_files.duration` remains TRUE — it is the file's duration — and only the
scene-level views are adjusted. With segment files, every one of those reads would have to be
reconciled with the fact that a "file" is now a slice of one.
## 8a. What shipped for the derived duration, and what deliberately did not

`SceneStore.GetFiles` now applies the range. That function is the chokepoint because the UI
reads a scene's length straight off `scene.files[0].duration`, bypassing every scene query.

Ten tests, and a mutation sweep of 8/8 killed — including the mutants that a comment in the
code claims are impossible ("subtract backwards", "look the range up under the wrong key",
"drop either clamp", "treat a NULL end as 0", "drop the unknown-duration guard"). A guard
that cannot be individually killed is not protection.

**Three things the tests found that the reasoning did not:**

1. **The window may run off the end of the file, and the schema cannot refuse it.** The
   CHECKs are `start >= 0`, `end >= 0`, `end > start` — and a CHECK may not reference another
   table, so none of them can see `video_files.duration`. `start 300, end NULL` on a
   2700-second file is a legal row, and the naive reduction is `-100`. Clamped.

2. **A file whose length ffprobe could not determine would have had every range clamped to
   nothing.** `video_files.duration` is 0 for files still being written, partial downloads and
   unrecognised containers. `min(end, 0)` kills the feature for a whole class of files. Guarded
   by `fileDur > 0`, with a two-sided test so the guard cannot widen into "never clamp".

3. **Two of my own guards masked each other, so neither was testable.** `min(end, file)` then
   `min(start, end)` then `max(end-start, 0)` — the sweep killed 5/7 and the two survivors
   were exactly the redundant arms. Collapsed to ONE mechanism (`start = min(start, end)`,
   then plain subtraction), because a guard that cannot be individually justified is a comment
   that looks like one.

**One test expectation was wrong and the code was right:** a `2600..2900` window on a 2700s
file clamps to 100, not 0. Asserting 300 would have been "fix the test to match the code";
asserting 0 was the same mistake one level up. It is now written as `fileDur - start` so the
arithmetic is visible.

### Still not done, so §8 is not complete

- `scene_filter.go:141` filters on the file's length (see the trap above).
- `file.go:962` (`FindFiles.duration`) is correct and must stay so: a file has no range.

## 8b. The aggregates — DONE, and the sweep that found four of five sites unwired

Five sites summed `video_files.duration`, so a split file was counted once **per scene** —
three scenes of one 45-minute file contributing 135 minutes. All five now use
`sqlite.SceneRangeDurationSQL`, a single SQL constant (`scene_range_sql.go`):

    scene.go:Duration()            the library total
    scene.go:FindScenes `as duration`   summed into FindScenes.duration
    performer.go selectPerformerScenesDurationSQL
    studio.go    sortByScenesDuration
    tag.go       sortByScenesDuration

**Why one SQL constant rather than a Go helper:** four of the five are hand-written
`ORDER BY` subqueries built with `fmt.Sprintf`, so a Go helper cannot reach them without
first rewriting each into goqu. One exported string makes a fix one edit and makes the five
*visibly* identical rather than accidentally so.

**The Go reduction and the SQL constant are the same rule written twice, so
`TestTheSQLFragmentAgreesWithTheGoImplementationOnEveryCase` drives 13 cases through both.**
They are used for different things and a disagreement is silent in both directions: Go decides
what a card reads, SQL decides what the library total says.

### The SQL constant was WRONG on the first run, and the case table caught it

My first draft clamped the computed *duration* against a sentinel instead of clamping the *end*
against the file's length — so nothing was clamped: `2600..2900` on a 2700s file reported 300,
and an open tail `2400..NULL` reported 2700 instead of 300. **4 of 11 cases failed.** The
comment above the old expression described an intent it did not implement, which is the same
failure as §7's SQLite/CLI divergence: a claim about an expression is not a measurement of it.

### Two dead guards, found because a mutant SURVIVED rather than because a test failed

1. `MIN(x, 999999999)` as a stand-in for `+Inf` was in the wrong arm, and `NULLIF(duration, 0)`
   appeared twice — once in step 1, where `COALESCE(NULLIF(d,0),0)` and `COALESCE(d,0)` are
   the **same value for every d**, so no test could ever distinguish them. Removed rather than
   tested: a test that must contrive a case to kill a no-op is a test of the contrive.
2. `MAX(0, …)` was also unreachable once `start = min(start, end)` is in place, since the
   subtraction is then non-negative by construction. Collapsed to one mechanism.

### The finding that mattered: FOUR OF FIVE CALL SITES WERE UNTESTED

The constant's own sweep was 5/5 killed while two call sites could be reverted with no test
noticing. **Sweeping the constant proves the rule; only calling it proves the wiring.**

And the per-site sweep that should have caught it was itself broken — twice:

- `replace("SceneRangeDurationSQL", "video_files.duration", 1)` hit the **comment** three lines
  above the `Sprintf` argument. The code was never mutated, the mutant built cleanly, and the
  test correctly passed — reported as "NOT DETECTED". A wide anchor's first occurrence being a
  comment is the common case, because the comment is written first.
- The sort assertions were **positional**, and every one of those three lists carries a
  mandatory trailing `COALESCE(sort_name, name, id) ASC` tiebreak (measured: tag.go:894). With
  both sides tied on the buggy 1800 the tiebreak happened to agree with the expected order, so
  "the 1200s entity sorts first" proved nothing.

Fixed by asserting **differentially** — sort twice, windows swapped, same entities, same
library, and require the order to CHANGE, since a tie cannot reverse its own order — and by
naming the entities so the tiebreak **contradicts** the expected order. Each case also needed
its own transaction: one shared txn left earlier subtests' entities in the library while later
ones sorted, landing in the same index band, so the test passed once and failed the next run.

Also: `scene_filter.go:141` still filters on the file's length, and `models.Scene` still has no
`Duration` field — so the per-row `as duration` column exists **only** to be summed, and
`FindScenesResult.duration` is its sole observable. The compiler refusing `s.Duration` is the
same fact §8's recount established by reading the schema.
