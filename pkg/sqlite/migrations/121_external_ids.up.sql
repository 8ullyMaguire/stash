-- stash#1790 — generalized external IDs.
--
-- THE REGISTRY (external_sources) EXISTS BECAUSE A BARE `endpoint` STRING HAS A SILENT,
-- PERMANENT FAILURE MODE. `scene_stash_ids.endpoint` is a bare varchar(255) that nothing
-- validates, so a typo inserts cleanly, round-trips, and is then unreachable by every join
-- that filters on the correct endpoint. The user has an external ID they cannot use and no
-- way to learn why. A registry turns that into a foreign key: the typo now fails at insert.
--
-- ONE TABLE FOR ALL ENTITIES, NOT ONE PER ENTITY. The alternative is the existing pattern --
-- a `<entity>_stash_ids` per entity -- which means N tables per provider and a new migration
-- per entity per provider. See docs/ISSUE-1790-spec.md §4.2 for why that trade is
-- defensible here and what it costs.
--
-- WHAT THAT TRADE COSTS, PAID FOR EXPLICITLY: `entity_id` CANNOT BE A FOREIGN KEY. It
-- cannot, because SQLite foreign keys must reference a UNIQUE column and there is no single
-- column here that is unique across four possible parents. A fake column pointing at
-- whichever table happens to match would enforce nothing while appearing to.
--
--   So two guarantees are MANDATORY, and neither is optional:
--     * every entity delete path removes its external IDs  (DestroyForEntity, tested T4)
--     * a sweep removes orphans left by a partial failure  (SweepOrphans, tested T5)
--   Without both, this schema is a leak with a nicer shape. The migration comment is the
--   right place to say so, because the next person to read the DDL will not read the spec.

CREATE TABLE `external_sources` (
  `id` integer primary key,
  `name` varchar(255) not null unique,
  `url` varchar(255) not null,
  -- Marks the source as a StashDB instance. Existing `*_stash_ids` rows all belong to such a
  -- source, so this is what a future migration of those four tables would match on.
  `stash_box` boolean not null default false,
  `created_at` datetime not null default '1970-01-01T00:00:00Z',
  `updated_at` datetime not null default '1970-01-01T00:00:00Z'
);

CREATE TABLE `external_ids` (
  -- NOT PART OF THE IDENTITY. This library's own row id, so a caller can address one
  -- external id directly; the four columns below are what make a row unique. The
  -- distinction matters because Record returns the row and a caller that stored a
  -- plausible-looking zero here would have a reference that points at nothing.
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `entity_type` varchar(64) not null,
  `entity_id` integer not null,
  `source_id` integer not null,
  `external_id` varchar(255) not null,
  `updated_at` datetime not null default '1970-01-01T00:00:00Z',
  -- CASCADE ON SOURCE DELETE, deliberately. An ID from a source that no longer exists is
  -- meaningless, and keeping it produces rows no join can resolve -- the exact orphan the
  -- registry exists to prevent.
  foreign key(`source_id`) references `external_sources`(`id`) on delete CASCADE,
  -- NON-BLANK, BUT NOT AN ENUM. Every lookup filters on entity_type, so a row with an
  -- empty one is unqueryable by definition. But listing the known types in a CHECK would
  -- require a migration to add an entity, which is the generality this feature exists to
  -- provide -- so the column is deliberately free text and only the blank case is refused.
  -- (The schema test inserts a `brand_new_entity_2099` row to prove the free-text half,
  -- and a blank row to prove this half. Both matter; a CHECK listing the five known types
  -- would fail the first.)
  check(length(trim(`entity_type`)) > 0),
  check(length(trim(`external_id`)) > 0),
  -- FOUR COLUMES, NOT THREE. Identity is (entity_type, entity_id, source_id, external_id).
  -- Leaving `source_id` out makes two providers' ids on one entity collide, which is the
  -- single easiest mistake in this change and is what T2 exists to catch.
  unique(`entity_type`, `entity_id`, `source_id`, `external_id`)
);

-- The lookup direction that matters: "which local entity has this external id from this
-- source". That is what a metadata scrape resolves against, and without this index it is a
-- full scan of every external id the library has ever seen.
CREATE INDEX `index_external_ids_on_lookup` ON `external_ids` (`source_id`, `external_id`);

-- The delete direction: "drop everything belonging to this entity". T4 asserts this path
-- exists per entity type, and without the index it is a scan on every entity delete --
-- including every delete during a library clean.
CREATE INDEX `index_external_ids_on_entity` ON `external_ids` (`entity_type`, `entity_id`);