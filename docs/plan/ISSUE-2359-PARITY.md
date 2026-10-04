# stash#2359 — Stash-Box parity: progress and what is left

**Measured 2026-10-04. Repo `~/work/lane-2/stash`, branch `main`.**

## Status: schema, store and GraphQL done and proven. UI, destroy paths and ledgers remain

Last commit `dd391abc3`. Nine of #2359's seventeen were already built upstream; six are now built
here (#1351 excluded, see below).

#2359 is an **umbrella issue** — `[Meta] Update Stash to be inline with Stash-Box`, 2022-03-03 —
listing 17 capabilities, each numbered by a sub-issue, plus one with no number (tattoo & piercing
structure). It was declined twice as "satisfied by pointing at the owner's Stash-Box instance;
duplicating the fields locally would maintain a second implementation of a codebase that already
exists". That reason described a **StashDB endpoint**, which delivers a document shape over the
wire but nothing to the rest of the app. `GOAL-SOFT-FORK` widened this branch on 2026-10-01 to
welcome new functionality, which retires the objection. The owner directed building all
buildable sub-features, including re-opening the R9/R10 cuts.

## ⚠️ The inventory kept shrinking — read this before estimating

**Nine of the seventeen were already built**, mostly upstream years before the issue was filed.
My first two inventories were both wrong, in *opposite* directions, which is the reason this
warning is at the top:

| Item | Status | Evidence |
|---|---|---|
| #1444 tag description | **DONE** | migration `36_tags_description`; `tags.description`, already in GraphQL |
| #1253 tag categories | **DONE** | migration `26_tag_hierarchy` → `tags_relations(parent_id, child_id)` |
| #2643 tag stash_ids | **DONE** | migration `74_tag_stash_ids` |
| #1868 scene multiple URLs | **DONE** | migration `47_scene_urls` → dropped `scenes.url` |
| #703 performer multiple URLs | **DONE** | migration `62_performer_urls` → dropped `url`/`twitter`/`instagram` |
| #638 disambiguation | **DONE** | migration `42_performer_disambig_aliases` |
| #2507 performer aliases | **DONE** | `performer_aliases`, migration 42 |
| #571 multiple performer images | **DONE** | this fork, `8e3c87778` |
| **#1351 performer merging** | **DONE** | `PerformerStore.Merge`, upstream `65e82a0cf` (#5910), in `PerformerReader`, wired to the `performerMerge` mutation, covered by `TestPerformerMerge` |

**#1351 was found AFTER I had already built it.** Migration 125 originally added
`performers.merged_into_id` plus a trigger, on sound reasoning ("merge is a move, not a delete,
keep a tombstone"). The existing `Merge` already repoints every referencing row with
`UPDATE OR IGNORE`, deletes what would duplicate, then destroys the source — so it satisfies the
requirement. **The column was removed.** A second merge mechanism with different semantics,
reachable through a different column that nothing reads, is exactly the ambiguity #2359 was filed
to remove. `docs/migration-124-125-check.sh` now asserts `merged_into_id` is **absent**, so the
duplication cannot come back unnoticed.

The first error was grepping Go models for `urls`/`URLs []string` and concluding the multi-URL
features were absent — backwards, because they are join tables plus GraphQL resolvers.
**"The column I expected is absent" and "the feature is absent" are different claims**, and the
second one costs a migration to discover.

## Built and verified

| # | Item | Sub-issues | Migration | Store |
|---|---|---|---|---|
| S1 | studio codes (several per studio) | #2607, #3051 | 124 | ✅ |
| S2 | scene director, **structured** | #3051 | 124 | ✅ |
| S3 | performer scene alias ("Jane Doe as Jane") | #3825 | 124 | ✅ |
| S5 | performer **split** aliases (alias owner) | #422, #2341 | 125 | ✅ |
| S6 | defined nationality | #1922 | 125 | ✅ |
| S10 | tattoo & piercing structure | *(none)* | 125 | ✅ |

**Verified:** `docs/migration-124-125-check.sh` builds, boots on a fresh DB so all 125
migrations run, asserts `systemStatus` reports `databaseSchema:125 appSchema:125`, then asserts
each new table, each added column, that `merged_into_id` is absent, and the back-fill. Back-fill
correctness asserted by direct query, not inferred from exit 0: a performer seeded with
`tattoos='left arm, right shoulder, ankle'` yields **3** rows.
`go build ./pkg/...` clean.

Store layer committed in `dd391abc3`: **21 subtests pass**, full
`go test -tags integration ./pkg/sqlite/` green, `go build ./...` clean, 61 unit packages green.
Mutation-checked — `SetCodes` replaceJoins→insertJoins is KILLED, and the draft-2 back-fill bug is
measured at **0 rows** against the shipped version's **3**.

### Three real bugs the tests found (all fixed)

1. **The nationality list was empty.** Migration 125 created `nationalities` and nothing ever
   inserted a row — as unusable as the free-text column it replaces, and it fails *silently*:
   every read returns empty, nothing errors, the only symptom is an empty dropdown. Now seeded
   with 107 entries by the migration itself, two with a NULL `code` (Basque, Kurdish).
2. **`SetNationalities` rejected a duplicate in the input**, returning `UNIQUE constraint failed`.
   A bad contract for a setter: a UI multi-select can hand back a repeated value. Now de-duplicated.
3. **`SELECT *` on a join failed at runtime, not compile time** — the extra `performer_id` column
   had no field to receive it and StructScan rejects unknown columns. Now an explicit column list.

Plus two the harness forced: parity models needed `db:"snake_case"` tags (no global `NameMapper`
exists), and the count helper must accept `int64` as well as `string`/`[]byte` — `QuerySQL` is built
on `rows.SliceScan()` and this driver is not consistent, so a single-form assertion fails on *some*
queries, which reads as a product bug.

## Four decisions forced by something already in the tree

**S2 — a packed column cannot answer `director = ?`.** `scenes.director` already exists
(migration 47) as `text` holding a comma-separated list, and is **kept**. "Ana L.opez" and
"Ana López" differ only across a comma that is not part of either name, so accent-folded
comparison of a substring fails; filtering scenes by director — the actual use — is a `LIKE`
against a packed column, which cannot use an index and cannot be exact. Hence one row per
director, composite primary key.

**S5 — the owner cannot be a column on `performer_aliases`.** That table's primary key is
`(performer_id, alias)`, so **the owner is already part of the key** and one alias string cannot
belong to two performers. That constraint *is* the #422/#2341 defect. A column cannot express
"this string belongs to A and the same string to B"; widening the key can, but then alias is no
longer unique per performer and every existing read must tolerate duplicates. Hence a side table
with a **nullable** owner, so an unowned alias stays valid and no migration invents an owner.

**S10 — one table for tattoos and piercings, `kind` discriminator.** `performers.tattoos` and
`.piercings` already exist as `varchar(255)`, are on the GraphQL surface, are rendered by the UI
and written by CSV import. **Kept**, structure added beside them, back-filled. Two tables would
mean two stores, two destroy paths and two GraphQL types for identical shape.

**Back-fill correctness.** The first version kept only the *first* comma-separated value. The
second carried `performer_id` through the recursion but joined back on `rest`, which the
recursion rewrites — so it matched only the first iteration, back-filled exactly one mark per
performer, **and exited 0**. A migration that silently drops data is indistinguishable from one
that worked. Shipped version carries the id through the recursion.

Separator is comma-**and-space**, because that is what this app's UI and importer write; a bare
`,` would split `"Los Angeles, CA"` — one location — into two.

## What is left

| # | Item | Sub-issues | Migration | Store | GraphQL | UI | Test |
|---|---|---|---|---|---|---|---|
| S1 | studio codes | #2607, #3051 | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| S2 | scene directors | #3051 | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| S3 | performer scene alias | #3825 | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| S5 | alias ownership | #422, #2341 | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| S6 | nationality | #1922 | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| S10 | body marks | *(none)* | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| ~~L1~~ | ~~**store tests**~~ — **DONE** `dd391abc3`: 21 subtests, mutation-checked | | | | | ✅ |
| ~~L2~~ | ~~**GraphQL**~~ — **DONE** `91dcfd73b`. 6 output fields, 6 input fields, `allNationalities`, `bodyMarkCreate`/`Destroy`, 17 resolver tests | | | | | ✅ |
| L3 | **UI** — studio codes field, director field, per-scene alias, body-mark editor, nationality selector | | | | | | ⬜ |
| ~~L4~~ | ~~**model fields**~~ — **DONE** `092a279c8`: `RelatedStrings` on Studio.Codes / Scene.Directors / Tattoo+PiercingLocations, plus `RelatedNationalities` | | | | | ✅ |
| L5 | **destroy paths** — deleting a performer must remove body marks and alias ownership. 13 tables reference `performers`; migration 121's rule is that a general table cannot use a foreign key, so cleanup must be explicit and **tested on row counts** | | | | | | ⬜ |
| L6 | **ledgers** — own roster rows in `docs/UPSTREAM-ISSUES.md` with verdict + proving test; `docs/ISSUES.md` #2359 `skipped`→`done`; one `docs/closed-issues.md` row per sub-feature | | | | | | ⬜ |
| L7 | **full re-verification** — `verify-all.sh` six gates, Playwright 71/0, mutation 3/3, fresh clone | | | | | | ⬜ |

### Order of work

L4 (model fields) → L2 (GraphQL) → **L1 (tests)** → L3 (UI) → L5 (destroy paths) → L6 →
L7. Tests land with or immediately after each feature rather than batched at the end, because
the #3849 lesson in `WHATS-LEFT.md` is that a test written after the fact is a description of
what the code does, not evidence that it does the right thing.

### Two things needing a decision

- **#1253 tag categories** exist as a general parent/child DAG rather than Stash-Box's
  single-parent shape. Left as-is: a DAG subsumes a tree, so changing it is churn with no
  capability gained. Reversible if exact shape parity is wanted.
- **`entity_urls` was dropped.** The first draft of migration 124 added one general URL table for
  scenes and performers. It was **removed** when migrations 47/62 turned out to already do
  exactly that with `scene_urls`/`performer_urls` (including `position`, which preserves order and
  which the general table had dropped). Re-adding it would have been a second, worse
  implementation of a shipped feature.

## The rule this issue keeps teaching

**Inventory by reading the migrations, not by grepping models.** Six of nine discoveries in this
programme were sitting in files whose *names* say what they do
(`36_tags_description`, `47_scene_urls`, `74_tag_stash_ids`), and none of them would match a
grep for the feature name in the current language's idiom. And when a design decision rests on
"this does not exist", the check that settles it is `git log -S`, not a search.

## L2 lessons: the four failures that no unit test could see

The GraphQL layer exposed a failure class worth carrying forward. **All four bugs returned HTTP 200,
wrote nothing, and logged nothing.**

1. **`directors: [String!]!` on a non-bulk input.** Seeding failed with HTTP 422 "must be defined".
   An accidentally-required input field is valid Go and valid schema; it only breaks clients that
   *omit* the field, which is every existing client.
2. **Codes written only if aliases were also sent** — `StudioStore.UpdatePartial` nested the codes
   block inside `if input.Aliases != nil`.
3. **Tattoos written only if `alias_list` was also sent** — the *same mistake* in
   `PerformerUpdate`, inside `if translator.hasField("alias_list")`. Two instances of one error is
   why the third fix was a systematic sweep of every parity assignment against its nearest enclosing
   guard, not a third spot fix.
4. **Scene directors and the performer fields were never wired at all** — in the schema, the model,
   the partial and the store, and the mutation never assigned them. Accepted and discarded.

**What caught them:** the e2e seed's round-trip assertion, and only that. Every other suite exercises
one field at a time — unit tests call the resolver, integration tests call the store, resolver tests
assert the resolver reached the mock. None sends a mutation with ONE field and reads back what the
DATABASE holds, which is the only path where a field gated behind another field is visible.

Lesson to keep: **a field that is wired to a resolver but never assigned in its mutation passes every
test in this repo.** Round-trip through the database, not through the same layer that wrote.

Two smaller ones: `getUpdateInputMap` panics (nil deref) outside a live GraphQL operation, so
`BodyMarkCreate` must not build a `changesetTranslator` — it has no optional fields and does not need
one; and `performer_body_marks` PK (performer_id, kind, location) makes `bodyMarkCreate`
non-idempotent, so the seed tolerates exactly that one UNIQUE error and still aborts on everything
else.
