# stash#3530 — implementation plan (step 1: the model)

Implements `docs/ISSUE-3530-spec.md` §5. **This is the model only** — time ranges on
`scenes_files`, derived duration, and the duplicate-detection fix. The player (R3), the play
URL (R4) and detection (R6) are separate steps and deliberately not here; §4's R6 needs an
upstream discussion, not a guess.

**Build/verify commands for every step**

```bash
cd /home/alvaro/code-local/go/stash
export GOFLAGS=-mod=mod
go build ./... && echo "go ok"
go test -tags integration ./pkg/sqlite/ -count=1 -timeout 300s
```

Current `appSchemaVersion` is **121** (`pkg/sqlite/database.go:48`), so this migration is
**122**. **The version bump must be in the SAME commit as the migration file** — the runner
refuses to open a database whose schema is ahead of the binary, and the symptom is "the
column is simply absent", which reads as a migration that did not run rather than a version
that was not bumped. That is the #1790 lesson and it is repeated here deliberately.

---

## Step 1 — migration 122: two nullable columns

Create `pkg/sqlite/migrations/122_scene_time_range.up.sql`:

```sql
-- stash#3530 -- multiple scenes in a single file: TIME RANGE ON THE LINK.
--
-- THE SCHEMA ALREADY ALLOWS MANY SCENES PER FILE. `scenes_files` has PRIMARY KEY
-- (scene_id, file_id) and only a NON-unique index on file_id, so two scene rows may already
-- reference one file row. The referential half of this issue is therefore NOT a schema
-- change; what does not exist anywhere -- no start/end on scenes_files, files or
-- video_files -- is WHICH PART OF THE FILE. That is what these two columns add.
--
-- BOTH NULLABLE, AND NULL MEANS "THE WHOLE FILE". Every pre-existing row has NULL here, so
-- the migration is a no-op for every current user and the derived duration reduces to
-- video_files.duration exactly -- same float, no backfill. A backfill writing 0 or NULL into
-- a duration is how a migration silently turns every scene into a zero-length one.
--
-- ADDITIVE AND REVERSIBLE BY DESIGN: dropping these two columns restores the previous
-- schema. See spec section 3 for why this is time ranges and not real segment files.
ALTER TABLE `scenes_files` ADD COLUMN `start_time` float;
ALTER TABLE `scenes_files` ADD COLUMN `end_time` float;

-- SQLite CHECKs are enforced, and these are the bad rows the store must refuse.
ALTER TABLE `scenes_files` ADD CONSTRAINT `scenes_files_start_time_non_negative`
  CHECK (`start_time` IS NULL OR `start_time` >= 0);
ALTER TABLE `scenes_files` ADD CONSTRAINT `scenes_files_end_time_non_negative`
  CHECK (`end_time` IS NULL OR `end_time` >= 0);
ALTER TABLE `scenes_files` ADD CONSTRAINT `scenes_files_end_after_start`
  CHECK (`end_time` IS NULL OR `start_time` IS NULL OR `end_time` > `start_time`);
```

**NO TABLE REBUILD IS NEEDED — measured, after I got it wrong in this plan first.**

I wrote that `ALTER TABLE ... ADD CONSTRAINT` is not valid SQLite and that a rebuild would be
required. **That is false on this host**, and the reason it looked plausible is that SQLite
*did not error*, which is the more dangerous outcome:

```
$ sqlite3 :memory: "CREATE TABLE t (a int); ALTER TABLE t ADD CONSTRAINT c CHECK (a > 0);
            SELECT sql FROM sqlite_master WHERE name='t';"
CREATE TABLE t (a int, CONSTRAINT c CHECK (a > 0))          <- persisted

$ sqlite3 :memory: "... ; INSERT INTO t VALUES (-5);"
Error: CHECK constraint failed: c                          <- and it FIRES
```

Both halves were checked, because "the statement was accepted" is not "the constraint was
added" — that is the `pg_constraint` mistake from memory, inverted: there, a constraint
existed and did not fire; here, a statement is accepted and does fire. **Neither is provable
without the second measurement.** So the five statements above stand as written, with no
`_new` table, no `PRAGMA foreign_keys` juggling, and none of the index-recreation risk.

**What remains a real trap, and why the gate still exists:**

- **`ALTER TABLE ADD COLUMN` with a CHECK also works** (verified separately, fires with
  `CHECK constraint failed: b>=0`). Both forms are usable; the `ADD CONSTRAINT` form is
  chosen only so each constraint gets a **name**, which is what makes the S1–S3 error
  messages name the constraint instead of dumping raw SQL.
- **A rebuild is still the wrong move here for a second reason** that the first one obscured:
  rebuilding `scenes_files` means recreating `index_scenes_files_file_id` and the partial
  unique `unique_index_scenes_files_on_primary`. A dropped table drops them silently, and
  losing the `primary` guarantee is a data-integrity regression **no test in the repo would
  notice**. Avoiding the rebuild avoids that risk rather than managing it.

**Verify — this is a real gate, run it before writing the Go code**

```bash
cd /home/alvaro/code-local/go/stash
sqlite3 :memory: ".read pkg/sqlite/migrations/122_scene_time_range.up.sql" 2>&1 | head -5
sqlite3 :memory: "SELECT 1;" >/dev/null && echo "sqlite ok"
```
Expect: **no errors.** The file references `scenes_files`, which does not exist in a fresh
in-memory database, so if the runner complains about a missing table that is expected here —
but a *syntax* error is not. Step 2 is the real gate, on a real migrated database.

---

## Step 2 — tests that prove the CONSTRAINTS FIRE

New file `pkg/sqlite/scene_range_test.go`, `//go:build integration`.

**The rule this step exists to obey:** a `CHECK` that *exists* is not a `CHECK` that
*fires*. Writing the bad row is the only proof. The #1790 gate found M7 surviving because a
Go-level check masked the database's own — the same shape of mistake, inverted.

| # | test | writes | asserts |
|---|---|---|---|
| S1 | a negative `start_time` is refused by the DATABASE | raw SQL, `start_time = -1` | error, and the row count is unchanged |
| S2 | `end_time <= start_time` is refused | raw SQL, `start_time=10, end_time=10` | error |
| S3 | `end_time < start_time` is refused | raw SQL, `start_time=30, end_time=10` | error |
| S4 | NULL/NULL is accepted and means the whole file | raw SQL | stored, duration unchanged |
| S5 | one NULL end is accepted (open-ended) | `start_time=10, end_time=NULL` | stored |

**S1–S3 must insert with RAW SQL**, not through the scene store — a store-level guard would
refuse first and the test would prove nothing about the schema. That is the M7 lesson applied
in advance.

**S4 is the one that matters most**, and it is a before/after comparison, not an assertion
about the new code: create a scene, read its duration, migrate, read it again, and assert
**bit equality** on the float. "The migration ran" is not what is being tested; "no user's
scene duration changed" is.

**Verify**

```bash
cd /home/alvaro/code-local/go/stash && export GOFLAGS=-mod=mod
go test -tags integration ./pkg/sqlite/ -count=1 -run 'TestSceneRange' -v 2>&1 | grep -E '^--- |^ok|^FAIL|Error:' | head -14
```
Expect: 5 PASS.

---

## Step 3 — derived duration at all three sites

Per spec §8. The expression, defined ONCE as a helper so three call sites cannot drift:

```go
// sceneRangeDuration is the CASE from spec section 8. NULL/NULL reduces to
// video_files.duration EXACTLY, which is what makes migration 122 a no-op for every
// pre-existing scene. Do NOT inline this three times: three copies of a duration rule is
// three chances for one site to keep reading the whole file's duration.
const sceneRangeDuration = `CASE WHEN scenes_files.start_time IS NULL AND scenes_files.end_time IS NULL
     THEN video_files.duration
     ELSE COALESCE(scenes_files.end_time, video_files.duration) - COALESCE(scenes_files.start_time, 0)
END`
```

Apply at:

- **`pkg/sqlite/scene.go:1135`** — `COALESCE(video_files.duration, 0) as duration` becomes
  `sceneRangeDuration as duration`. **This is the one that decides whether the feature works
  at all**: it is the `duration` every scene row carries into the UI. Untouched, a 4-minute
  scene inside a 45-minute file shows the file's length while the player stops at the
  segment — the list and the player disagree.
- **`pkg/sqlite/scene.go:1136`** — `SUM(temp.duration)` already aggregates whatever the inner
  query produced, so it follows from 1135. **Verify rather than assume**: if it reads
  `video_files.duration` directly it needs the same change.
- **`pkg/sqlite/scene.go:951`** (`Duration()`, library total) — unchanged: summing segments
  should sum their lengths, and the ranges already do.

**Test** — add to `scene_range_test.go`:

| # | test | asserts |
|---|---|---|
| S6 | a scene with `start=10, end=40` reports duration 30 | `30`, not the file's duration |
| S7 | `start=10, end=NULL` reports `file.duration - 10` | open-ended means "to the end" |
| S8 | `start=NULL, end=40` reports 40 | open at the beginning |
| S9 | the library total (`Duration()`) sums segment lengths, not file lengths | two segments of one 100s file total 60, not 200 |

**S9 is the test that catches the aggregate regression**, and it is easy to assume correct:
a scene list showing 30+30 beside a file row of 100 is obviously wrong, but nothing in the
code *complains* about it.

**Verify**

```bash
cd /home/alvaro/code-local/go/stash && export GOFLAGS=-mod=mod
go test -tags integration ./pkg/sqlite/ -count=1 -run 'TestSceneRange' 2>&1 | tail -4
go test -tags integration ./pkg/sqlite/ -count=1 -timeout 300s 2>&1 | tail -3   # no regressions
```
Expect: all pass; full sqlite suite still green.

---

## Step 4 — the duplicate-detection fix, which is a BUG this feature introduces

Per spec §3's reason 4. `FindDuplicates` (`pkg/sqlite/scene.go:1502`) groups by the file's
phash, and today ends with:

```sql
HAVING COUNT(phash) > 1
    AND COUNT(DISTINCT scene_id) > 1
```

`COUNT(DISTINCT scene_id) > 1` is **already true** for two segments of one file — they share
a `file_id`, hence an identical phash. So without this step, **splitting one file into scenes
makes every scene in it appear as a duplicate of every other.** That feeds the
duplicate-checker UI and the cleanup tooling.

The fix is one clause, and both joins are already in scope from the existing query:

```sql
HAVING COUNT(phash) > 1
    AND COUNT(DISTINCT scene_id) > 1
    AND COUNT(DISTINCT file_id) > 1
```

**Two segments of one file have ONE distinct file, so they are excluded. Two genuinely
different files with the same phash still have two, so real duplicates are still found.**

| # | test | asserts |
|---|---|---|
| S10 | two segments of ONE file are not duplicates | no group returned |
| S11 | two DIFFERENT files with the same phash ARE duplicates | one group, both ids — **this is the half that catches an over-broad fix** |
| S12 | two scenes of one file that are genuinely re-encoded copies still match | behaviour for the existing case is unchanged |

**S11 is not optional.** "Exclude same-file pairs" is trivially satisfiable by dropping the
phash join entirely, which would silence duplicate detection for every user while S10 passes
perfectly. **A fix asserted only on the failure it prevents is a fix that can break the thing
it was protecting.**

**Verify**

```bash
cd /home/alvaro/code-local/go/stash && export GOFLAGS=-mod=mod
go test -tags integration ./pkg/sqlite/ -count=1 -run 'TestSceneRangeDuplicate|TestFindDuplicates' 2>&1 | grep -E '^--- |^ok|^FAIL' | head -12
```
Expect: all pass, and the pre-existing `TestFindDuplicates*` tests unchanged.

---

## Step 5 — the mutation gate

`docs/mutate_3530.py`, shaped on `docs/mutate_4326.py` (which is itself shaped on
`ui/v2.5/src/hooks/mousetrapScope.mutate.py`): exact-string substitution, `flock`, SIGTERM
restore, a filter audit that exits 2 if the suite is red or a witness did not run, and
`SKIP` reported separately because **a skip is not a kill**.

| # | mutation | must kill |
|---|---|---|
| M1 | the rebuild drops the `end_after_start` CHECK | S2, S3 |
| M2 | the rebuild drops the `primary` partial unique index | a test asserting two primaries are refused (**add it — it does not exist yet**) |
| M3 | derived duration ignores `start_time` | S6 |
| M4 | derived duration returns `video_files.duration` when only `end_time` is NULL | S7 |
| M5 | the duplicate query loses `COUNT(DISTINCT file_id) > 1` | S10 |
| M6 | the duplicate query drops `COUNT(DISTINCT scene_id) > 1` | S11 |
| M7 | one nullable column is added `NOT NULL DEFAULT 0` | S4 |

**M7 is the interesting one**, because it is the mistake a hurried implementer makes: a
`NOT NULL DEFAULT 0` column makes every existing scene claim it starts at 0 and — combined
with a `NOT NULL` end — would break every existing row. S4 kills it.

**M2's witness does not exist.** A migration that silently drops
`unique_index_scenes_files_on_primary` is a data-integrity regression **no test in the repo
would notice**, so step 5 adds that test rather than letting the harness report 7/7 while
testing 6.

**A table rebuild is the hardest thing to mutate meaningfully**, and that is a real
limitation to record rather than paper over: these mutants are string edits to SQL text, so
they prove the *tests* read the schema, not that SQLite enforces it. The S1–S3 raw-SQL
writes are the real enforcement proof; the gate proves nothing else is compensating.

**Verify**

```bash
cd /home/alvaro/code-local/go/stash && python3 docs/mutate_3530.py; echo "exit=$?"
```
Expect: **7/7 killed, exit 0**.

**Then prove the gate can fail**, as step 5 of the #4326 plan required and its commit did:
flip the verdict condition and confirm it prints `killed 0/N` and exits 1. A gate that cannot
report failure is decoration.

---

## Step 6 — ledgers, spec §6 caveats, commit

- `#3530` stays **`open`**. Steps 1–5 build the *model*; R3 (player), R4 (play URL) and R6
  (detection) are unbuilt, and R6 needs an upstream discussion. **Marking this `done` would
  claim a feature users cannot use** — the exact false claim this project has already had to
  undo twice. Update the disposition to say what exists and name what does not.
- Record in spec §6 that nothing is browser-verified (no configured instance here), and that
  **no UI exists for marking a range** — the columns are settable by API/SQL only, so an
  end user cannot yet create a segment.
- `python3 docs/check-issue-ledgers.py` must stay OK, and `python3 docs/goal-check.py` C8
  must still show #3530 outstanding.

**Verify the whole thing**

```bash
cd /home/alvaro/code-local/go/stash && export GOFLAGS=-mod=mod
gofmt -l pkg/ internal/ | head; go build ./... && echo "go ok"
go test -tags integration ./pkg/sqlite/ -count=1 -timeout 300s 2>&1 | tail -3
python3 docs/mutate_3530.py 2>&1 | tail -3
python3 docs/check-issue-ledgers.py 2>&1 | tail -1
python3 docs/goal-check.py 2>&1 | grep -E '^C8'
```

Then commit, and tag `stash-3530-model` — **not** `stash-3530-done`, because the feature is
not done and a tag that says otherwise is the same false claim in a more durable place.