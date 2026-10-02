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

## 8. The three duration sites, measured — because "duration is derived" is three edits

R2 looks like one change and is three, and missing any of them produces a scene whose list
row disagrees with its own detail page:

| site | expression | what it must become |
|---|---|---|
| `pkg/sqlite/scene.go:1135` | `COALESCE(video_files.duration, 0) as duration` | the range, when one is set |
| `pkg/sqlite/scene.go:1136` | `SUM(temp.duration) as duration` (aggregate/total view) | sums the range, not the file |
| `pkg/sqlite/scene.go:951` | `COALESCE(SUM(video_files.duration), 0)` (`Duration()`, library total) | unchanged in aggregate terms: a segment's length is what counts |

**Line 1135 is the one that decides whether the feature works at all**, because it is the
`duration` every scene row carries into the UI. With a range set and this untouched, the
scene shows the WHOLE FILE's length — a 4-minute "scene" inside a 45-minute file — while the
player, honouring R3, stops after the segment. The list and the player then disagree, which
is the shape of bug users report as "the duration is wrong" and maintainers cannot reproduce.

**The expression to use, and why it is written defensively:**

```sql
CASE WHEN scenes_files.start_time IS NULL AND scenes_files.end_time IS NULL
     THEN video_files.duration
     ELSE COALESCE(scenes_files.end_time, video_files.duration)
        - COALESCE(scenes_files.start_time, 0)
END as duration
```

`NULL` on BOTH ends must reduce to exactly `video_files.duration` — the identical float, so
R2's "numerically identical" is satisfied by construction for every pre-existing scene and
needs no backfill. **A backfill writing 0 or NULL into a duration column is how a migration
silently turns every scene into a zero-length one.**