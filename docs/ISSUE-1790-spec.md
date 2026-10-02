# stash#1790 — Generalized support for external IDs

**Spec.** Written 2026-10-02, before any code, per the standing workflow.

## 1. What the issue asks

Upstream #1790 asks for generalized external IDs: the ability to associate an entity with
identifiers from sources other than StashDB. Today a scene carries `StashID{StashID,
Endpoint, UpdatedAt}` — a type named for one provider.

## 2. What actually exists — measured, not assumed

The pre-existing ledger row claimed `absent — StashIDs is a stash-specific type`. That
implies the mechanism does not exist. **It does.** Measured 2026-10-02:

| Thing | State |
|---|---|
| Tables | 4: `scene_stash_ids`, `performer_stash_ids`, `studio_stash_ids`, `tag_stash_ids` |
| Columns | `<entity>_id integer`, `endpoint varchar(255)`, `stash_id varchar(36)`, `updated_at datetime` |
| FK | `on delete CASCADE` to the parent, all four |
| Read path | **already generic** — one `stashIDRepository` parameterised by `tableName` + `idColumn` (repository.go:408-434) |
| Write path | 4 duplicated `stashIDTable` managers in `tables.go`, one per entity |
| `ExternalID` / `external_id` | **absent everywhere** — `grep -rn "ExternalID\|external_id" --include=*.go pkg/ internal/` returns nothing |

So the honest statement is: **the read side is already generic; the type name, the source
registry and the fifth entity are missing.** A row that says "absent" would have sent the
next person to build something that already exists.

## 3. Requirements

**R1. A source registry.** Every external ID names a source, and the set of sources is
known. Today `endpoint` is a bare `varchar(255)` that nothing validates.

**R2. A generic table shape.** One table, not one per entity per provider.

**R3. `StashIDs` keeps working.** The JSON schema, GraphQL and four stores all read it.

**R4. A fifth entity can adopt external IDs without a migration per provider.**

## 4. The design

### 4.1 Why a registry, and why it must be a table

The problem with a bare `endpoint` string is not tidiness. It is that **a typo becomes a
permanent orphan row**: `endpoint = 'https://stashd.org'` with a stray character still
inserts, still round-trips, and is then unreachable by every join that filters on the
correct endpoint. Nothing reports it. The user has an external ID they cannot use and no way
to learn why.

A registry turns that into a foreign key. `external_sources(id, name, url, ...)`; an
`external_ids` row references it. A typo now fails at insert, loudly, and the fix is one
character.

**Cost, stated:** it is one more table and one more join on the read path. Accepted because
the orphan failure is silent and permanent, and silent-and-permanent is what a data
integrity feature exists to prevent.

### 4.2 Why ONE `external_ids` table and not four more per entity

Alternative: keep the per-entity pattern and add `<entity>_external_ids` for each. Five
entities × N providers stays N tables per entity.

Chosen: a single `external_ids(entity_type, entity_id, source_id, external_id, updated_at)`
with a **polymorphic entity reference**.

**This is the most dangerous decision in the spec and it is made against my instinct,
because a polymorphic reference cannot be a foreign key.**

| | per-entity table | polymorphic `external_ids` |
|---|---|---|
| FK to parent | **yes**, CASCADE deletes children | **NO** — cannot be, the parent varies by row |
| Adding an entity | new migration + table | none |
| Orphan risk | impossible | possible; must be cleaned explicitly |

Trading a real FK for generality is a bad trade *in general*. It is defensible here for one
specific reason: **SQLite foreign keys cannot reference a column that is not unique**, so a
single table with four possible parents has no target to reference, and a fake
`entity_id` column pointing at whichever of the four happens to match is strictly worse —
it would enforce nothing while appearing to.

**The consequence must be paid for explicitly, or this design is worse than the four
tables:** deleting an entity must delete its external IDs, and nothing enforces it. A test
must prove the delete path for **each** entity type, and a startup sweep must remove
orphans left by a partial failure. If that pair of guarantees is not implemented, this
spec is not done — it is a leak with a nicer schema.

### 4.3 What is NOT being done, and why

**The four existing `*_stash_ids` tables are NOT migrated.** Rationale, stated rather than
assumed:

- They hold every provider ID this library has ever seen for existing users. A migration
  rewriting them is large, irreversible, and its failure mode is silent data loss.
- Nothing yet needs the generality. The five-entity case is not hypothetical — it is
  planned — but it can be served by the new table alone.
- **The honest test of whether the new path is generic is a fifth entity that uses it.**
  Refactoring four existing call sites would prove only that the old path still works,
  which is a weaker claim wearing the same evidence.

Cost of this decision, stated plainly: the duplication is **reduced, not removed**. Four
legacy tables remain. A later migration can move them once the new path has production
exercise behind it.

## 5. Test requirements

Every one of these is a test, not a claim:

1. **T1 (negative)** — an unknown source is refused at insert. The whole point of R1.
2. **T2** — the same external ID from two sources on one entity is two rows, not a conflict.
   Identity is `(entity_type, entity_id, source_id, external_id)`, and forgetting
   `source_id` in that tuple makes two providers' ids collide.
3. **T3** — re-recording an existing external ID updates `updated_at` rather than inserting
   a second row.
4. **T4 (negative)** — the entity-delete path removes external IDs, **for every entity
   type**. This is the guarantee §4.2 says must be paid for explicitly.
5. **T5** — an orphan sweep removes rows whose parent is gone, and leaves real rows alone.
6. **T6** — `StashIDs` still round-trips through JSON and GraphQL unchanged. R3 is a
   regression guard, not a new feature.

## 6. Mutation testing requirement

Each of T1–T6 must be shown to fail under a mutation. A test that cannot be made to fail
is decoration.

## 7. Definition of done

- Registry table + generic `external_ids` table, in one migration, with `appSchemaVersion`
  bumped in the same commit.
- `models.ExternalID`, `models.ExternalSource`, the store, and the contract interface.
- A fifth entity wired to the generic path.
- T1–T6 green, each shown to kill a mutation.
- `docs/ISSUES.md` row → `done` with commit evidence; `UPSTREAM-ISSUES.md` → `closed`;
  `closed-issues.md` row; both header counts; `check-issue-ledgers.py` OK; `goal-check.py`
  C8 green.