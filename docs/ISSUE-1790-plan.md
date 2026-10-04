> **HISTORICAL — this file records a session or plan as it stood when written.**
> The paths below (`~/code-local/go/...`, `~/code/go/...`) and the host named as `gaming-pc`
> do **not** exist on this machine. The checkout is `~/work/lane-2/stash` on **thinkcentre**;
> a second, older pair sits at `~/work/lane-1/`. For current state read
> `docs/WHATS-LEFT.md` and run `python3 docs/goal-check.py`.
> Nothing here has been rewritten, because a record of what was known then is worth more than
> a tidy path.

# stash#1790 — implementation plan

Companion to `docs/ISSUE-1790-spec.md`. Written before any code, per the standing workflow.

Every step carries the **exact** verification command and its expected output. A step whose
verification is "looks right" is not a step.

**Repo:** `~/code-local/go/stash`, branch `main`. Build/test in place; no second host
(`ssh thinkcentre` does not have this repo — verified).

**Two rules that have cost real time this session and are therefore part of every step:**

- `go build` / `go test -tags integration` are the authorities. The `write_file`/`patch`
  lint runs a bare compiler without build tags and reports bogus errors on `sqlite_test`
  files.
- Before any mutation run: `go clean -testcache`, and anchor the mutation on a string that
  occurs **exactly once**. Count first; an ambiguous anchor mutates a doc comment and
  reports as a survivor.

---

## Step 1 — migration 121: `external_sources` + `external_ids`

**File:** `pkg/sqlite/migrations/121_external_ids.{up,down}.sql`

```sql
CREATE TABLE `external_sources` (
  `id` integer primary key,
  `name` varchar(255) not null unique,
  `url` varchar(255) not null,
  `stash_box` boolean not null default false,
  `created_at` datetime not null default '1970-01-01T00:00:00Z',
  `updated_at` datetime not null default '1970-01-01T00:00:00Z'
);

CREATE TABLE `external_ids` (
  `entity_type` varchar(64) not null,
  `entity_id` integer not null,
  `source_id` integer not null,
  `external_id` varchar(255) not null,
  `updated_at` datetime not null default '1970-01-01T00:00:00Z',
  foreign key(`source_id`) references `external_sources`(`id`) on delete CASCADE,
  unique(`entity_type`, `entity_id`, `source_id`, `external_id`)
);

CREATE INDEX `index_external_ids_on_lookup` ON `external_ids` (`source_id`, `external_id`);
```

**The unique key is four columns, not three.** Identity is
`(entity_type, entity_id, source_id, external_id)`. Dropping `source_id` makes two
providers' ids on one entity collide — this is T2, and it is the single most likely
mistake in the whole change.

**`source_id` cascades, deliberately.** Deleting a source deletes its IDs: an ID from a
source that no longer exists is meaningless, and keeping it means `external_ids` rows that
no join can resolve — the exact orphan the registry exists to prevent.

**Bump `appSchemaVersion` 120 → 121 in the same commit** (`pkg/sqlite/database.go`). It
refuses to open otherwise, and the symptom is "the table is simply absent", which reads as a
migration that did not run.

**Verify**
```
go build ./pkg/sqlite/
grep -n 'appSchemaVersion' pkg/sqlite/database.go | head -2
```
Expect: build clean; version reads 121.

---

## Step 2 — model + store

**Files:** `pkg/models/model_external_id.go`, `pkg/sqlite/external_id.go`

Model, following `pkg/models/model_issue.go`:

```go
type ExternalSource struct {
    ID        int    `db:"id"          json:"id"`
    Name      string `db:"name"        json:"name"`
    URL       string `db:"url"         json:"url"`
    StashBox  bool   `db:"stash_box"   json:"stash_box"`
}

type ExternalID struct {
    ID         int        `db:"id"          json:"id"`
    EntityType string     `db:"entity_type" json:"entity_type"`
    EntityID   int        `db:"entity_id"   json:"entity_id"`
    SourceID   int        `db:"source_id"   json:"source_id"`
    ExternalID string     `db:"external_id" json:"external_id"`
    UpdatedAt  time.Time  `db:"updated_at"  json:"updated_at"`
}
```

Store methods: `CreateSource`, `FindSourceByName`, `AllSources`, `Record` (upsert),
`FindByEntity`, `FindByExternalID`, `DestroyForEntity`, `SweepOrphans`.

`Record` must upsert on the four-column key (`ON CONFLICT ... DO UPDATE SET updated_at`),
which is T3.

`DestroyForEntity` and `SweepOrphans` exist because the polymorphic reference has no
foreign key — see spec §4.2. They are not optional extras; without them the design leaks.

**Add `var _ models.ExternalIDReaderWriter = (*ExternalIDStore)(nil)`** so the contract
cannot silently diverge from the store (the same guard as `issue.go`).

**Verify**
```
go build ./pkg/... && go vet ./pkg/models/ ./pkg/sqlite/
```

---

## Step 3 — tests T1–T5, in `pkg/sqlite/external_id_test.go`

`//go:build integration`, `package sqlite_test`, `runWithRollbackTxn`, helpers from
`issue_detect_test.go`'s pattern.

**T1 (negative).** `Record` with `SourceID` that does not exist must fail. Note: the FK
fires, so this test must use `require.Error` — and must also assert that the failure is the
*foreign key*, not some other error, or it passes for the wrong reason. Read
`PRAGMA foreign_keys` state first; SQLite ignores FKs when the pragma is off, and a test
that "passes" because FKs are unenforced proves nothing.

**T2.** Two sources, same `external_id` string, one entity → two rows.

**T3.** `Record` the same four keys twice → one row, `updated_at` advanced. Second-
precision timestamps mean this test is vacuous without care: backdate the stored value first
(**assert that premise** — backdating moves it *earlier*), then Record again.

**T4 (negative, per entity type).** Create an entity of each supported type, add an
external ID, destroy the entity, assert zero rows. Table-driven over the types. This is the
guarantee spec §4.2 says must be paid for.

**T5.** Insert a row for `entity_id` 999999 (no such parent) → `SweepOrphans` removes it,
and leaves a real row alone.

**Verify**
```
go test -tags integration ./pkg/sqlite/ -count=1 -run 'ExternalID|ExternalSource' -v
```
Expect: all green. **Every one must be shown to fail under mutation — Step 5.**

---

## Step 4 — wire a fifth entity

Pick the entity with no stash_ids today. Confirm before coding:

```
grep -rln 'stashIDs: stashIDRepository{' pkg/sqlite/    # 4 today
```

The new entity gets external IDs through the generic path only — **no new per-entity
table**. That is the proof the path is generic; refactoring the existing four would prove
only that the old path still works.

**Verify**
```
go build ./... && go test -tags integration ./pkg/sqlite/ -count=1
```

---

## Step 5 — mutation sweep

Mutations to prove killed (anchors verified unique first):
1. Drop the `source_id` column from the unique key → must break T2.
2. Make `Record` `INSERT` instead of upsert → must break T3.
3. Remove `DestroyForEntity` from one entity's delete path → must break T4.
4. Make `SweepOrphans` delete everything → must break T5.
5. Make the FK check in `Record` non-fatal → must break T1.

**Verify**
```
# for each mutation: apply, go clean -testcache, run the suite, confirm RED, restore
go test -tags integration ./pkg/sqlite/ -count=1
```
Expect: 5/5 killed, 0 survivors. A survivor is a hole in the tests, not a fact about the
code.

---

## Step 6 — R3 regression guard (T6)

`StashIDs` must still round-trip. Existing coverage exists for the four entities; confirm
rather than assume, and add a test only if a gap shows:

```
go test -tags integration ./pkg/sqlite/ -count=1 -run 'StashID'
```

---

## Step 7 — close the ledgers

1. `docs/ISSUES.md` row 1790 → disposition **leading** with `**done** -- commit \`…\``. C8's
   evidence rule reads the disposition cell (`cells[-3]`) and requires a hex hash; a commit
   in the wrong cell fails it.
2. `docs/UPSTREAM-ISSUES.md` row 1790 → `closed`, and **moved out of the `## Planned` section**
   into `## Resolved` — the checker fails on a closed row sitting under a `Planned` heading.
3. Both header counts updated (planned −1, closed +1) plus the tally line after the rules
   table.
4. `docs/closed-issues.md` row.
5. `python3 docs/goal-check.py` → C8 shows 3 remaining; `python3 docs/check-issue-ledgers.py`
   → OK.

**Verify**
```
python3 docs/check-issue-ledgers.py && python3 docs/goal-check.py 2>&1 | grep -E 'C2|C8'
```

---

## Step 8 — full gate, tag, push

```
gofmt -l pkg/ internal/
go build ./...
go vet ./pkg/... ./internal/api/
go test ./pkg/... -count=1
go test -tags integration ./pkg/sqlite/ -count=1
```

Then commit, tag `stash-1790-done`, push to **both** `origin` and `forgejo`.