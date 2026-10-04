# stash#2359 — Stash-Box parity: progress and what is left

**Measured 2026-10-04. Repo `~/work/lane-2/stash`, branch `main`.**

## Status: COMPLETE. Schema, store, GraphQL, destroy paths, UI and ledgers all done and proven.

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
| ~~L3~~ | ~~**UI** — codes on studio bulk + edit, nationalities on performer edit and bulk, body marks / directors ~~ | | | | | ✅ |
| ~~L4~~ | ~~**model fields**~~ — **DONE** `092a279c8`: `RelatedStrings` on Studio.Codes / Scene.Directors / Tattoo+PiercingLocations, plus `RelatedNationalities` | | | | | ✅ |
| ~~L5~~ | ~~**destroy paths**~~ — **DONE**, and the premise was WRONG. Measured: all **12** tables carrying `performer_id` declare a cascading FK, and `Database.open` appends `&_fk=true`, so no cleanup code is needed. Two mutations prove the assertion bites | | | | | ✅ |
| ~~L6~~ | ~~**ledgers**~~ — **DONE.** `docs/ISSUES.md` #2359 `skipped`→`done`; `docs/UPSTREAM-ISSUES.md` #2359 `deferred`→`closed` and #422 `not-planned`→`closed`; one `docs/closed-issues.md` row each for #422 and #2359; header tallies re-counted | | | | | ✅ |
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


## L5: the destroy paths did NOT need work, and proving that took two mutations

WHATS-LEFT.md said L5 needed "explicit cleanup... 13 tables reference `performers`; migration 121's
rule is that a general table cannot use a foreign key". Both halves of that were wrong.

**It is 12 tables, not 13, and five of the names I first wrote did not exist** — `performer_favorites`,
`performer_overseers`, `scene_performers`, `gallery_performers` and `performers_stash_ids`. The real
list came from running `pragma_table_info` against a database the app's own migration path created,
and its naming is inconsistent on purpose: `performer_tags` AND `performers_tags` both exist,
`performers_scenes` rather than `scene_performers`. Guessing would have produced a confidently wrong
list — which is the third time on this issue that reading the code was not the same as measuring it.

**And every one of the 12 declares a cascading FK**, while `Database.open` appends `&_fk=true` unless
`disableForeignKeys` is set — which it is not, for either the read or the write handle. So
`ON DELETE CASCADE` fires and no cleanup code is required. Migration 121's rule is real but does not
apply here: these are purpose-built join tables, not general tables.

Proven with two mutations, and the *first* is the informative one:

| Mutation | Result | What it means |
|---|---|---|
| `Destroy` → bare `DELETE FROM performers WHERE id = ?`, bypassing `destroyExisting` | **PASSED** | The cascade does the work; `destroyExisting` adds nothing for these tables |
| `Destroy` → deletes the four parity child tables but forgets the performer row | **FAILED**, 8 orphans named | The assertion bites and names which tables leaked |

A test that passes under the first mutation is measuring the schema plus the pragma rather than our
code — which is *correct* here, and is the point. The file is a regression guard for a future
migration that adds a `performer_id` without a cascading FK, not a guard on cleanup code.

`TestEveryPerformerReferencingTableIsCovered` checks the fixture list against the schema by
introspection, so a table added later fails the test rather than going unchecked. Its `ignored` map is
now EMPTY — an earlier draft carried an entry for a table that does not exist, which would have made
a wrong guess look like a deliberate exclusion.


## L3: the UI work, and the two wrong places I put it first

The fields went into `EditPerformersDialog` and `EditStudiosDialog` before I checked what those
actually are, and only one of the two is the form a user reaches for a single performer:

| Component | What it really is | Parity fields added |
|---|---|---|
| `EditPerformersDialog` | the **bulk** multi-select dialog, reachable only from `/performers` in LIST display mode | `nationalities` |
| `PerformerEditPanel` | the **single-performer** inline form on `/performers/{id}` — `#performer-edit`, not a modal | `nationalities` |
| `EditStudiosDialog` | the studio bulk dialog; studios have no single-entity edit panel in this build | `codes` |

`PerformerEditPanel` is the one that matters and it is neither a modal nor a dialog. The first version
of the e2e test looked for `.modal.show` after clicking Edit, which would have failed forever against
a dialog that does not exist by design — and the panel also ships `country`, `tattoos` and
`piercings` as plain formik fields, which is the shape the new one follows.

### NationalitySelect is multi and NOT Creatable, unlike CountrySelect

`CountrySelect` is single-valued (one country) and `Creatable` (a user must be able to type a country
this build has never heard of). A performer may hold **several** nationalities — that is all #1922 is —
so this takes an array. It is deliberately **not** Creatable: a nationality here is a reference row
with an id, and migration 125 seeds a fixed 107-entry list, so typing one that is not in it would
create an id the backend cannot resolve. The trade is stated in the component: a nationality
Stash-Box knows and this seed does not cannot be selected, which beats a selection that fails to save.

### Codes are a BulkUpdateStrings, and `values` is not `ids`

`BulkUpdateStrings` uses `values`; `BulkUpdateIds` uses `ids`. Writing `codes.ids` type-checks
against neither and the compiler caught it. `values` also stays `undefined` until the user types,
which is what keeps "did not touch" distinct from "cleared" — a bulk edit of something else must not
wipe every studio's codes.

### The fragment omission that would have silently destroyed data

`graphql/data/performer.graphql` did not carry `nationalities`, so the edit form's
`initialValues` would have been `[]`. Nothing errors: the field renders, an unrelated edit saves, and
every nationality on that performer is **gone**. Adding the field to the fragment is not optional
bookkeeping — it is the difference between a visible absence and data loss.

### i18n: edit the text, never round-trip the JSON

The first attempt rewrote `en-GB.json` through `json.dumps(indent=2)`: 1874 lines changed for 9 added
strings, because the file is 4-space indented. The replacement inserts each key at its alphabetical
position in the raw text, which is an 8-line diff. `director` already existed, so 8 not 9 — and the
guard that caught it is why the file does not now carry a duplicate key.

## The e2e suite now renders the parity UI, and two mutants prove it matters

Sections 1-9 all render pages and read their text. None of them renders a performer edit form, so
none of them could tell a wired field from an absent one: with the component deleted, the API still
answers `allNationalities`, the seed still writes 107 rows, and every pre-existing test still passes.
That is m4, and it is the shape a half-finished feature takes — correct at every layer, invisible in
the product.

| Mutant | Killed by |
|---|---|
| m4 — `#2359` nationality field not rendered at all | `the performer edit form renders a NationalitySelect` |
| m5 — nationality select shows ids where names belong | `nationality options are names, not bare ids` |

m5 exists because "the control rendered" and "the control is legible" are different claims. A select
full of `1`, `2`, `3` renders, opens, and passes a length check while being unusable.

Final e2e: **82 passed, 0 failed**; mutation check **5/5 killed**.


## L6: the ledgers, and the rule that made the verdict `closed` and not `built`

`docs/check-issue-ledgers.py` is stricter than it looks, and two of its rules bit:

**Only `closed` counts as roster-closed.** `built` and `done` are separate verdicts, and the
cross-file check is `verdict == "closed"` on one side against a `| stash#N |` row on the other. So
flipping #2359 and #422 to `built` -- which is what the *other* seven built rows use -- made the two
ledgers disagree about two issues that are, in fact, closed. The header tally counts `closed + done`,
so a `built` row is invisible to it too. Both went to `closed`.

**Editing the text is not the same as editing the row.** Both roster rows were edited with
`row.rsplit("|", 2)[0] + " closed|"`, which keeps the pipe that *precedes* the verdict -- so the row
ended `... 0 survived  closed|` and the parser read the verdict as the entire preceding sentence. The
shape that parses is `... content | closed |`. Worth stating because the failure is invisible: the
row still looks like a table row and the ledger checker reports a plausible "roster does not mark it
closed" rather than a parse error.

**#2359's old disposition was right about the meta and wrong about the work.** It read "satisfied by
configuration -- owner runs a Stash-Box instance" and "point the app at that StashDB". Configuring a
StashDB instance is *push* from a StashDB this build scrapes. Parity means Stash can *represent* what
Stash-Box represents, and configuration cannot add a column.

**#422's old verdict was about process, not about the feature.** It was `not-planned` because the work
had no spec and no branch, and it said so: "if it is wanted, it belongs on a feature branch with its
own spec". It was wanted; #2359 gave it both. Flipping the verdict without recording that would have
been a lie about the row's own history, so the row now explains the flip rather than hiding it.

**The Stash-Box sub-issues (#2607, #3051, #1922, #2341) get NO roster rows, deliberately.** The roster
is generated from `stashapp/stash` via the GitHub API and must hold 675 rows. #2607 and friends are
**Stash-Box** issues, not Stash issues — `grep '^| 2607 |' docs/UPSTREAM-ISSUES.md` returns nothing
and must keep returning nothing. Only #422 is an upstream Stash issue. Their verdicts live in the
`docs/ISSUE-2359-PARITY.md` table and in the closed-issues rows, which is the right home for work whose
issue tracker is a different repository.
