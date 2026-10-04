-- stash#2359 — Stash-Box parity, part 2: what is actually missing, performers and merging.
--
-- Part 1 (124_stashbox_parity_scenes) covers studio codes, structured scene directors and
-- per-scene performer aliases. This covers split aliases, nationality, tattoo & piercing
-- structure, and performer merging.
--
-- For what was ALREADY here before this programme, read the header of 124. Six of #2359's
-- seventeen items predate the issue and need nothing: tag description (migration 36), tag
-- categories (26), tag stash_ids (74), scene URLs (47), performer URLs (62), and the packed
-- `scenes.director` column (47).

-- stash#422 + #2341 — performer split aliases.
--
-- WHAT "SPLIT ALIASES" MEANS, since the upstream titles are cryptic and the ledger gets this
-- wrong: Stash-Box stores an alias as an (alias, owner_performer) pair rather than a bare string,
-- so one performer's "JD" can be recorded and disambiguated from another's. This fork already has
-- `performer_aliases (performer_id, alias, PRIMARY KEY(performer_id, alias))` from migration 42,
-- which stores the string only -- and note that the existing PRIMARY KEY makes the OWNER THE KEY,
-- so the same alias string cannot be attached to two different performers at all. That is the
-- defect #422/#2341 describe, and it is a constraint, not a missing column.
--
-- The owner therefore goes in a SIDE table rather than as a new column on performer_aliases, and
-- this is forced by the primary key: an owner column on that table cannot express "this alias
-- belongs to performer A while the same string belongs to performer B", because (performer_id,
-- alias) is already unique. Widening the key to (performer_id, alias, owner_performer_id) would
-- express it, but then `alias` is no longer unique per performer, and every existing read of
-- "this performer's alias list" has to tolerate duplicates.
--
-- NULLABLE owner, so an ownerless alias stays a plain alias and remains valid. That is what makes
-- this additive rather than a data migration that has to invent an owner for every existing row.
CREATE TABLE `performer_alias_owners` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `performer_id` integer not null,
  `alias` varchar(255) not null,
  -- The performer this alias string is attributed to. NULL means "no attribution recorded",
  -- which is every row that existed before this migration and every alias added since.
  `owner_performer_id` integer REFERENCES `performers`(`id`) ON DELETE SET NULL,
  foreign key(`performer_id`) references `performers`(`id`) on delete CASCADE,
  check(length(trim(`alias`)) > 0)
);
CREATE INDEX `index_performer_alias_owners_on_alias` ON `performer_alias_owners` (`alias`);
CREATE INDEX `index_performer_alias_owners_on_owner`
  ON `performer_alias_owners` (`owner_performer_id`);
-- A given alias belongs to a given performer at most once. Without this the table accepts two
-- rows differing only in owner, and "who owns this alias" returns a list.
CREATE UNIQUE INDEX `index_performer_alias_owners_uniq`
  ON `performer_alias_owners` (`performer_id`, `alias`);

-- Back-fill: every existing alias becomes an unowned row, so the new store is authoritative from
-- the first read and no query has to union two tables and risk disagreeing about what exists.
INSERT INTO `performer_alias_owners` (`performer_id`, `alias`)
  SELECT `performer_id`, `alias` FROM `performer_aliases`;

-- stash#1922 — defined nationality.
--
-- A controlled list, not free text, and `performers.country varchar(255)` is already there. The
-- reason a second representation is still needed: `country` is a free-text string, so "American",
-- "USA" and "United States" are three different values, which is precisely the defect #1922 exists
-- to fix. Stash-Box presents nationality as a dropdown, and the point of a dropdown is that two
-- performers who chose the same entry compare equal.
--
-- A REFERENCE TABLE rather than an enum CHECK, because SQLite cannot ALTER a CHECK constraint: an
-- enum would freeze the list into the schema of every existing database with no way to extend it.
CREATE TABLE `nationalities` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `name` varchar(255) not null unique,
  -- ISO 3166-1 alpha-2 where one applies. NULLABLE on purpose: Stash-Box carries nationalities
  -- that are not countries ("Kurdish", "Basque"), and NOT NULL here would force a fake code.
  `code` varchar(8)
);

-- SEEDED, BECAUSE AN EMPTY LIST IS A FEATURE THAT DOES NOT WORK.
--
-- #1922 exists because `performers.country` is free text, so "American", "USA" and "United
-- States" are three different values for one nationality. The fix is a controlled list -- but a
-- table with no rows is exactly as unusable as the free-text column it replaced, and it fails
-- SILENTLY: every read returns empty, nothing errors, and the only symptom is a dropdown with
-- nothing in it. The store test asserts `require.NotEmpty` on this list for exactly that reason.
--
-- Seeded here rather than by the application at boot, because a migration that creates the
-- structure and a startup path that fills it are two things that can disagree, and this one
-- already did: the first draft of this migration created the table and nothing else, and the
-- integration test caught it immediately.
--
-- The list is deliberately NOT exhaustive -- it covers the countries that appear in performer
-- metadata, and `code` is nullable for entries that are nationalities rather than countries. New
-- entries are an INSERT, which is why this is a reference table and not a CHECK constraint:
-- SQLite cannot ALTER a CHECK, so an enum here would freeze this list into every existing
-- database with no way to extend it.
INSERT INTO `nationalities` (`name`, `code`) VALUES
  ('Afghan', 'AF'), ('Albanian', 'AL'), ('Algerian', 'DZ'), ('American', 'US'),
  ('Argentine', 'AR'), ('Australian', 'AU'), ('Austrian', 'AT'), ('Azerbaijani', 'AZ'),
  ('Bangladeshi', 'BD'), ('Basque', NULL), ('Belarusian', 'BY'), ('Belgian', 'BE'),
  ('Bolivian', 'BO'), ('Bosnian', 'BA'), ('Brazilian', 'BR'), ('British', 'GB'),
  ('Bulgarian', 'BG'), ('Cambodian', 'KH'), ('Cameroonian', 'CM'), ('Canadian', 'CA'),
  ('Chilean', 'CL'), ('Chinese', 'CN'), ('Colombian', 'CO'), ('Costa Rican', 'CR'),
  ('Croatian', 'HR'), ('Cuban', 'CU'), ('Cypriot', 'CY'), ('Czech', 'CZ'),
  ('Danish', 'DK'), ('Dominican', 'DO'), ('Dutch', 'NL'), ('Ecuadorian', 'EC'),
  ('Egyptian', 'EG'), ('English', 'GB'), ('Estonian', 'EE'), ('Ethiopian', 'ET'),
  ('Filipino', 'PH'), ('Finnish', 'FI'), ('French', 'FR'), ('Georgian', 'GE'),
  ('German', 'DE'), ('Greek', 'GR'), ('Guatemalan', 'GT'), ('Hebrew', 'IL'),
  ('Honduran', 'HN'), ('Hong Kong', 'HK'), ('Hungarian', 'HU'), ('Icelandic', 'IS'),
  ('Indian', 'IN'), ('Indonesian', 'ID'), ('Iranian', 'IR'), ('Iraqi', 'IQ'),
  ('Irish', 'IE'), ('Israeli', 'IL'), ('Italian', 'IT'), ('Jamaican', 'JM'),
  ('Japanese', 'JP'), ('Jordanian', 'JO'), ('Kazakh', 'KZ'), ('Kenyan', 'KE'),
  ('Korean', 'KR'), ('Kurdish', NULL), ('Latvian', 'LV'), ('Lebanese', 'LB'),
  ('Lithuanian', 'LT'), ('Luxembourgish', 'LU'), ('Macedonian', 'MK'), ('Malaysian', 'MY'),
  ('Mexican', 'MX'), ('Moldovan', 'MD'), ('Moroccan', 'MA'), ('Nepalese', 'NP'),
  ('New Zealand', 'NZ'), ('Nicaraguan', 'NI'), ('Nigerian', 'NG'), ('Norwegian', 'NO'),
  ('Pakistani', 'PK'), ('Palestinian', 'PS'), ('Panamanian', 'PA'), ('Paraguayan', 'PY'),
  ('Peruvian', 'PE'), ('Philippine', 'PH'), ('Polish', 'PL'), ('Portuguese', 'PT'),
  ('Puerto Rican', 'PR'), ('Romanian', 'RO'), ('Russian', 'RU'), ('Saudi', 'SA'),
  ('Scottish', 'GB'), ('Serbian', 'RS'), ('Singaporean', 'SG'), ('Slovak', 'SK'),
  ('Slovenian', 'SI'), ('South African', 'ZA'), ('South Korean', 'KR'), ('Spanish', 'ES'),
  ('Swedish', 'SE'), ('Swiss', 'CH'), ('Taiwanese', 'TW'), ('Thai', 'TH'),
  ('Tunisian', 'TN'), ('Turkish', 'TR'), ('Ukrainian', 'UA'), ('Uruguayan', 'UY'),
  ('Venezuelan', 'VE'), ('Vietnamese', 'VN'), ('Welsh', 'GB');

CREATE TABLE `performer_nationalities` (
  -- (performer_id, nationality_id) as the key: a performer may hold SEVERAL nationalities (#1922
  -- is explicitly about dual nationality) and may not hold the same one twice.
  `performer_id` integer not null,
  `nationality_id` integer not null,
  foreign key(`performer_id`) references `performers`(`id`) on delete CASCADE,
  foreign key(`nationality_id`) references `nationalities`(`id`) on delete CASCADE,
  primary key(`performer_id`, `nationality_id`)
);
-- The reverse direction -- "which performers hold this nationality" -- which the primary key's
-- leading column cannot serve.
CREATE INDEX `index_performer_nationalities_on_nationality`
  ON `performer_nationalities` (`nationality_id`);

-- Tattoo & piercing structure. The one #2359 entry with no upstream issue number.
--
-- `performers.tattoos` and `performers.piercings` ALREADY EXIST as varchar(255) columns (see
-- migration 42's table rebuild) holding a comma-separated list. They are KEPT: they are on the
-- GraphQL surface, the UI renders them and CSV import writes them. Dropping them would break
-- every existing query and every user's data for no gain, so this adds structure beside them and
-- back-fills.
--
-- ONE TABLE FOR BOTH KINDS, WITH A `kind` DISCRIMINATOR, rather than `tattoos` and `piercings`
-- tables. They have identical shape -- location, description, optional image -- and each is read
-- by the same query with a different literal. Two tables would mean two stores, two destroy paths
-- and two GraphQL types for no representational gain.
--
-- `kind` is part of the uniqueness key so a tattoo and a piercing described identically are two
-- rows, and so the same described mark cannot be entered twice.
CREATE TABLE `performer_body_marks` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `performer_id` integer not null,
  `kind` varchar(32) not null,
  `location` varchar(255) not null,
  `description` varchar(255),
  `image_path` varchar(510),
  foreign key(`performer_id`) references `performers`(`id`) on delete CASCADE,
  -- A CHECK, not free text: unlike the nationality list this set really is closed -- there is no
  -- third kind of mark Stash-Box models -- and a typo would otherwise create a row no reader
  -- selects, which is the unqueryable-row failure mode migration 121 was built to remove.
  check(`kind` in ('tattoo', 'piercing')),
  check(length(trim(`location`)) > 0),
  unique(`performer_id`, `kind`, `location`, `description`)
);
CREATE INDEX `index_performer_body_marks_on_performer`
  ON `performer_body_marks` (`performer_id`, `kind`);

-- Back-fill. One row PER COMMA-SEPARATED ENTRY, not one row per performer: a single INSERT that
-- keeps only the first entry is the worst version of this migration, because it looks like a
-- successful back-fill and quietly drops every value after the first.
--
-- The performer_id is carried THROUGH the recursion rather than joined back afterwards. Joining
-- on the accumulated `rest` matches only the first iteration for each performer -- `rest` is
-- rewritten every step, so by the second entry it no longer equals the original string -- which
-- silently back-fills exactly one mark per performer and looks like it worked.
--
-- Splitting uses a recursive CTE because SQLite has no SPLIT_STRING. The separator is ', ' --
-- comma AND space -- because that is what this app's UI and CSV importer write, so splitting on it
-- reads back exactly what was stored. A bare ',' would also split "Los Angeles, CA", which is one
-- location.
WITH RECURSIVE marks(performer_id, kind, rest, piece) AS (
  SELECT `id`, 'tattoo', trim(`tattoos`) || ', ', NULL
  FROM `performers`
  WHERE `tattoos` IS NOT NULL AND trim(`tattoos`) <> ''
  UNION ALL
  SELECT performer_id, kind,
         substr(rest, instr(rest, ', ') + 2),
         trim(substr(rest, 1, instr(rest, ', ') - 1))
  FROM marks
  WHERE instr(rest, ', ') > 0
)
INSERT OR IGNORE INTO `performer_body_marks` (`performer_id`, `kind`, `location`, `description`)
  SELECT performer_id, kind, piece, NULL FROM marks WHERE piece IS NOT NULL AND piece <> '';

WITH RECURSIVE marks(performer_id, kind, rest, piece) AS (
  SELECT `id`, 'piercing', trim(`piercings`) || ', ', NULL
  FROM `performers`
  WHERE `piercings` IS NOT NULL AND trim(`piercings`) <> ''
  UNION ALL
  SELECT performer_id, kind,
         substr(rest, instr(rest, ', ') + 2),
         trim(substr(rest, 1, instr(rest, ', ') - 1))
  FROM marks
  WHERE instr(rest, ', ') > 0
)
INSERT OR IGNORE INTO `performer_body_marks` (`performer_id`, `kind`, `location`, `description`)
  SELECT performer_id, kind, piece, NULL FROM marks WHERE piece IS NOT NULL AND piece <> '';

-- stash#1351 — performer merging. ALREADY DONE, AND NOT REBUILT HERE.
--
-- This migration originally added `performers.merged_into_id` plus a no-self-merge trigger, on the
-- reasoning that "merge is a move, not a delete" and that a tombstone preserves the duplicate's
-- identity. That reasoning was sound and the feature turned out to EXIST ALREADY:
-- `PerformerStore.Merge(ctx, source, destination)` has been in pkg/sqlite/performer.go since
-- upstream commit 65e82a0cf ("Performer merge", #5910), it is in the PerformerReader interface, it
-- is wired to the `performerMerge` GraphQL mutation (internal/api/resolver_mutation_performer.go
-- :724) and it is covered by TestPerformerMerge.
--
-- It also does what this comment proposed: repoint every referencing row at the destination with
-- UPDATE OR IGNORE, DELETE the rows that would have become duplicates, then destroy the source. So
-- the existing implementation already satisfies the requirement, and what it does NOT do is keep
-- a tombstone -- which is a design difference, not a missing feature, and #2359 does not ask for
-- one.
--
-- ADDING merged_into_id ANYWAY WOULD BE ACTIVELY HARMFUL: a second merge mechanism with different
-- semantics, reachable through a different column, that nothing reads. Two ways to express "these
-- two performers are the same person" is exactly the ambiguity #2359 was filed to remove. The
-- column and its trigger are therefore deliberately NOT here. This paragraph is the record of
-- that decision, because a later reader comparing #2359 against the schema will notice the absence
-- and should find the reason rather than re-add it.
--
-- Recorded as: satisfied by 65e82a0cf, no work required.