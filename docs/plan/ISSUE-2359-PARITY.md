# stash#2359 — Stash-Box parity: build plan and verified inventory

**Measured 2026-10-04 at `cdfdc1338`, on `~/work/lane-2/stash`.**

## What #2359 actually is

An **umbrella issue**, not a feature. `[Meta] Update Stash to be inline with Stash-Box`
(stashapp/stash#2359, filed 2022-03-03, 2 comments) lists seventeen capabilities that the
Stash-Box instance already has, each numbered by a sub-issue, plus one entry with no issue
number at all. It was declined twice — `BACKLOG-17` and `GOAL-SOFT-FORK` both recorded
"satisfied by pointing at the owner's existing Stash-Box instance; duplicating the fields
locally would maintain a second implementation of a codebase that already exists".

That reason described a **StashDB endpoint**, and an endpoint delivers a document shape over
the wire. It does not deliver it to the rest of the app: you cannot sort or filter a local
scene by director, cannot answer "what is this performer called *in this scene*", and cannot
ask "which performers have a mark in a given location" without a network round trip per
question. `GOAL-SOFT-FORK` widened this branch on 2026-10-01 to "new functionality is
welcome; upstream-mergeable is not the bar", which retires the objection. The owner directed
implementation of all buildable sub-features, including re-opening the R9/R10 cuts.

## The inventory that matters — six of the seventeen were already built

**A grep for "does a column with this name exist" gets four of these six WRONG**, because
upstream solved them on tables this app already has:

| Item | Status | Evidence |
|---|---|---|
| #1444 tag description | **DONE** | migration `36_tags_description`; `tags.description`, already a GraphQL field |
| #1253 tag categories | **DONE** | migration `26_tag_hierarchy` → `tags_relations(parent_id, child_id)` |
| #2643 tag stash_ids | **DONE** | migration `74_tag_stash_ids` → `tag_stash_ids(tag_id, endpoint, stash_id)` |
| #1868 scene multiple URLs | **DONE** | migration `47_scene_urls` → `scene_urls`, which **dropped `scenes.url`** |
| #703 performer multiple URLs | **DONE** | migration `62_performer_urls` → `performer_urls`, which dropped `url`, `twitter`, `instagram` |
| #638 performer disambiguation | **DONE** | migration `42_performer_disambig_aliases` → `performers.disambiguation` |
| #2507 performer aliases (list) | **DONE** | `performer_aliases`, migration 42 |

So the two most expensive-looking items on the list — multi-URL for scenes and performers —
need **nothing**. My first pass at this inventory concluded the opposite, from grepping for
`urls`/`URLs []string` in Go models and finding none, which is exactly backwards: the feature
is implemented as a join table plus a GraphQL resolver, and neither matches those patterns.

**#1253's tag categories exist as a general parent/child relation** (`tags_relations`), not the
single-parent shape Stash-Box uses. Left as-is: a DAG subsumes a tree, and changing it would
be churn with no capability gained.

## What genuinely does not exist, and is being built

| # | Item | Upstream sub-issues | Migration |
|---|---|---|---|
| S1 | studio codes (several per studio) | #2607, #3051 | `124` |
| S2 | scene director, **structured** | #3051 | `124` |
| S3 | performer scene alias ("Jane Doe as Jane") | #3825 | `124` |
| S5 | performer **split** aliases (alias owner) | #422, #2341 | `125` |
| S6 | defined nationality | #1922 | `125` |
| S10 | tattoo & piercing structure | *(none)* | `125` |
| S11 | performer merging | #1351 | `125` |

Also already done and needing nothing: **#571** multiple performer images (`8e3c87778`),
**#1253**'s marker half (`SceneMarker.TagIDs`).

## Four design decisions, each forced by something already in the tree

**S2 — a packed column cannot answer `director = ?`.** `scenes.director` already exists
(migration 47) as `text` holding a comma-separated list, and it is **kept**. "Ana L.opez" and
"Ana López" are two directors; searching for one does not find the other under accent folding,
because the compared substring crosses a comma that is not part of either name. Filtering a
scene list by director — the actual use — is therefore a `LIKE` against a packed column, which
cannot use an index and cannot be exact. Hence `scene_directors(scene_id, director)` with a
composite primary key, which also makes "the same director credited twice" impossible.

**S5 — the owner cannot be a column on `performer_aliases`.** That table's primary key is
`(performer_id, alias)`, so the **owner is already part of the key** and the same alias string
cannot be attached to two performers at all. That constraint *is* the defect #422/#2341
describe. A new column cannot express "this string belongs to A while the same string belongs
to B"; widening the key to `(performer_id, alias, owner_performer_id)` can, but then `alias` is
no longer unique per performer and every existing read has to tolerate duplicates. Hence a side
table `performer_alias_owners`, with a **nullable** owner so an unowned alias stays valid —
which is what makes it additive rather than a migration that invents an owner per existing row.

**S11 — merge is a move, not a delete.** Deleting the duplicate destroys its scene credit and
images; keeping both rows leaves two people in one, which is the duplicate the feature exists
to remove. So `performers.merged_into_id` records the move and the row survives as a tombstone.
`ON DELETE SET NULL` so deleting the *survivor* returns the tombstone to an ordinary performer
rather than leaving a dangling redirect. A `CHECK` cannot be added by `ALTER`, so the
no-self-merge rule is a trigger.

**S10 — one table for tattoos and piercings, with a `kind` discriminator.** `performers.tattoos`
and `performers.piercings` already exist as `varchar(255)` (migration 42's table rebuild), are
on the GraphQL surface, are rendered by the UI and are written by CSV import. They are **kept**
and structure is added beside them. Two tables would mean two stores, two destroy paths and
two GraphQL types for identical shape.

## The back-fill bug worth recording

The first version of the body-marks back-fill produced **one row per performer, keeping only
the first comma-separated entry** — a plausible-looking migration that quietly drops every
value after the first. The second version carried `performer_id` through the recursion but
*joined back* on the accumulated `rest`; since `rest` is rewritten each step, that join matches
only the first iteration, so it back-filled exactly one mark per performer **and still looked
like it worked**.

The shipped version carries the performer id through the recursion, which is the only shape
that cannot lose an entry. Verified directly: a performer seeded with
`tattoos = 'left arm, right shoulder, ankle'` yields **3** rows, and
`piercings = 'earlobe'` yields 1.

## Splitting `, ` versus `,`

The separator is comma-**and-space**, because that is what this app's UI and CSV importer
write, so the back-fill reads back exactly what was stored. A bare `,` would also split
`"Los Angeles, CA"` — one location — into two. A location that itself contains `", "` remains
a known limitation, recorded rather than hidden.

## Verification for the schema layer

- `docs/migration-124-125-check.sh` — builds, boots on a **fresh** database so every migration
  runs, asserts `systemStatus` reports `databaseSchema:125 appSchema:125`, then asserts every
  new table, every added column and the trigger exist.
- The app **refuses to open** a database whose recorded version differs from
  `appSchemaVersion`, and the symptom is "the table is simply absent" — which reads as a
  migration that did not run. `appSchemaVersion` was 123 and had to be bumped to 125 in the
  same change; the first run of the check reported `databaseSchema:123` and no error at all,
  which is what made the cause findable.
- Back-fill correctness asserted directly (above), not inferred from the migration exiting 0.
- `go test ./pkg/sqlite/...` green after the bump.

## Ledger recording

Per the owner's direction, each sub-feature becomes its **own roster row** in
`docs/UPSTREAM-ISSUES.md` with verdict `done` and the proving test, so the two ledgers agree
and `check-issue-ledgers.py` keeps reconciling a closed set. `docs/ISSUES.md`'s #2359 row moves
from `skipped` to `done` with the sub-feature table, and `docs/closed-issues.md` gains one row
per sub-feature — its header claims one row per issue closed, and adding rows for issues that
were not previously in the roster is what keeps that claim true.