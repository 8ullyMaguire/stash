-- Library scope on every target row. StashForge M4, step 4.3. Spec §6.4.
--
-- This migration is the one docs/HANDOFF.md named as the reason
-- `LibraryAccessStore.Decide` is unreachable. It was: the gate existed, the
-- store existed, the refusal had a test, and `library_id` appeared in exactly
-- one migration -- 101, the table that DEFINES the grant. No scene, image,
-- gallery, performer, tag, studio or movie carried a library, and no request
-- context carried a user id, so there was no "a private library" for the gate
-- to refuse. "A private library" was a phrase in a comment.
--
-- 101's own rationale says "every target row hangs off a library". That sentence
-- was aspirational and written before the columns existed; it is false of the
-- schema it heads, and it could not be corrected in place because editing an
-- applied migration breaks its checksum. The correction is here, and the
-- sentence is left standing as the thing this file makes true.
--
-- GROUPS, NOT MOVIES. The seventh target table is `groups`, and the name in
-- every document that names the list -- the plan's step 4.3, GOAL.md's
-- non-negotiable, migration 101's comment -- is `movies`. Migration
-- 65_movie_group_rename renamed it. So the list had to be read off the schema
-- rather than off the docs, and the first attempt at this migration named a
-- table that had not existed for 39 migrations: it failed to apply with
-- "no such table: movies", which is the error golang-migrate reports for the
-- whole initial-schema run.
--
-- Worth recording as a rule rather than a fix. A table list written in prose
-- rots silently -- the sentence stays true-looking forever, and only a
-- migration that names the table reports it. The integration test walks the
-- list, so the next rename fails there.
--
-- WHY ADDITIVE COLUMNS RATHER THAN A JOIN TABLE
--
-- A `target_library(target_type, target_id, library_id)` table would leave the
-- seven target tables untouched, and it is the design that re-introduces the
-- problem 101 was written to avoid. A row's library is a property OF THE ROW:
-- it is read on every media request, and a separate table means a row can
-- belong to zero libraries (nobody assigned it), one library, or -- because
-- nothing in the schema prevents it -- two, and "which library is this file in"
-- becomes a query that can return more than one answer. A column makes the
-- zero case explicit, which is the case that matters.
--
-- WHY NULL IS A REFUSAL AND NOT A DEFAULT
--
-- NULL means "this row is in no library". It does NOT mean "public", and that
-- distinction is the whole point. The tempting reading of a missing scope is
-- "unrestricted, so allow it" -- and that is a fail-open: a row the migration
-- failed to backfill, a row inserted by a write path that forgot the column, or
-- a row belonging to a library that was deleted all read as unrestricted, and
-- the file is served to a user with no grant.
--
-- So `internal/collab.MediaScope` resolves NULL to the refusal, and the
-- serving path calls that. Fail-closed is not defensiveness for its own sake
-- here: the alternative fails in exactly the case where the operator has the
-- least reason to be looking.
--
-- WHY THE BACKFILL IS NOT OPTIONAL
--
-- Every existing install has rows. Left NULL, all of them become unservable
-- media -- a silent outage on upgrade, and the kind that reads as "the fork
-- broke stash". So the backfill runs, and it puts everything the migration can
-- reach into one library owned by the instance owner.
--
-- ONE library rather than one per user, deliberately. Pre-existing rows have no
-- recorded author: stash had no user model until migration 87, so there is
-- nothing to attribute them to, and inventing an attribution would be a lie in
-- a column that a security decision reads. One library, owned by the owner, is
-- the honest answer to "whose library is a row that predates users".
--
-- The owner's access to their own library does not come from this backfill. It
-- comes from `libraries.user_id` being the owner, which the gate honours as a
-- rule of its own -- so an owner upgrading a large instance does not come back
-- to a public instance where their own media 404s until they grant themselves
-- access to it.

-- WHY `is_default` IS AN ALTER ON `libraries` AND NOT PART OF 101
--
-- Migration 101 shipped and was applied -- tag `m3-metadata-sharing` contains
-- it -- and non-negotiable #2 forbids editing an applied migration, because
-- golang-migrate records a checksum. So the column the default-library rule
-- needs is added here, additively, which is the same rule the target columns
-- follow for the same reason.
--
-- At most ONE default library, enforced by a partial unique index rather than
-- by Go. Two defaults would make "which library does an unscoped row belong to"
-- answerable two ways, and the serving path would then pick one arbitrarily --
-- which is how a row in a private library ends up checked against a public one.
ALTER TABLE `libraries` ADD COLUMN `is_default` integer NOT NULL DEFAULT 0
  CHECK (`is_default` IN (0, 1));

ALTER TABLE `scenes`     ADD COLUMN `library_id` integer REFERENCES `libraries` (`id`) ON DELETE SET NULL;
ALTER TABLE `images`     ADD COLUMN `library_id` integer REFERENCES `libraries` (`id`) ON DELETE SET NULL;
ALTER TABLE `galleries`  ADD COLUMN `library_id` integer REFERENCES `libraries` (`id`) ON DELETE SET NULL;
ALTER TABLE `performers` ADD COLUMN `library_id` integer REFERENCES `libraries` (`id`) ON DELETE SET NULL;
ALTER TABLE `tags`       ADD COLUMN `library_id` integer REFERENCES `libraries` (`id`) ON DELETE SET NULL;
ALTER TABLE `studios`    ADD COLUMN `library_id` integer REFERENCES `libraries` (`id`) ON DELETE SET NULL;
ALTER TABLE `groups`     ADD COLUMN `library_id` integer REFERENCES `libraries` (`id`) ON DELETE SET NULL;

-- The backfill's target. One row, owned by the instance owner, and NOT private
-- (`is_private` NULL) so the backfilled library inherits the owner's consent
-- decision rather than asserting a per-library one nobody made.
--
-- Guarded on `is_owner = 1` because the partial unique index on users already
-- guarantees at most one such row; a subquery that returned two ids would make
-- the INSERT a silent multi-row insert. `LIMIT 1` is belt and braces, and the
-- COALESCE fallback is the case worth being explicit about: an instance whose
-- users table is empty has no owner to own anything, so there is no library to
-- create and the backfill below is a no-op. That is correct -- with no users
-- there is no media request to refuse.
--
-- `is_default` is 1, and that flag is what keeps the owner able to watch their
-- own instance after this migration. The scanner writes rows through a code
-- path that knows nothing about libraries, so every newly-scanned row lands with
-- `library_id` NULL -- and NULL is a refusal (see the column note above), so a
-- public instance would 404 its owner's own media until they hand-assigned a
-- library to every file they have ever watched. That is a silent breakage of
-- working software caused by a security column, and it is the kind of thing
-- that gets "fixed" by making NULL mean allow.
--
-- So NULL resolves to the DEFAULT library rather than to "unrestricted". The
-- distinction matters and is the whole design: a row in no library is not
-- public, it is in the default library, and the default library is owned by the
-- owner, who bypasses the grant check by ownership. Every other user still needs
-- an explicit `user_library_access` row. Fail-closed where it counts, and an
-- owner whose own instance keeps working.
INSERT INTO `libraries` (`user_id`, `name`, `is_private`, `is_default`)
SELECT `id`, 'Imported', NULL, 1
  FROM `users`
 WHERE `is_owner` = 1
 LIMIT 1;

-- The backfill itself. One statement per table, each writing the single
-- library the INSERT above created, and each guarded by "that library exists"
-- so an empty users table leaves the tables untouched rather than failing.
--
-- Written as three separate statements rather than a loop, because a migration
-- is SQL and cannot loop; the repetition is the price, and it is why the
-- integration test asserts the COUNT of NULL rows afterwards rather than
-- trusting seven copies of the same expression to agree.
UPDATE `scenes`     SET `library_id` = (SELECT `id` FROM `libraries` WHERE `name` = 'Imported' LIMIT 1)
 WHERE `library_id` IS NULL AND EXISTS (SELECT 1 FROM `libraries` WHERE `name` = 'Imported');
UPDATE `images`     SET `library_id` = (SELECT `id` FROM `libraries` WHERE `name` = 'Imported' LIMIT 1)
 WHERE `library_id` IS NULL AND EXISTS (SELECT 1 FROM `libraries` WHERE `name` = 'Imported');
UPDATE `galleries`  SET `library_id` = (SELECT `id` FROM `libraries` WHERE `name` = 'Imported' LIMIT 1)
 WHERE `library_id` IS NULL AND EXISTS (SELECT 1 FROM `libraries` WHERE `name` = 'Imported');
UPDATE `performers` SET `library_id` = (SELECT `id` FROM `libraries` WHERE `name` = 'Imported' LIMIT 1)
 WHERE `library_id` IS NULL AND EXISTS (SELECT 1 FROM `libraries` WHERE `name` = 'Imported');
UPDATE `tags`       SET `library_id` = (SELECT `id` FROM `libraries` WHERE `name` = 'Imported' LIMIT 1)
 WHERE `library_id` IS NULL AND EXISTS (SELECT 1 FROM `libraries` WHERE `name` = 'Imported');
UPDATE `studios`    SET `library_id` = (SELECT `id` FROM `libraries` WHERE `name` = 'Imported' LIMIT 1)
 WHERE `library_id` IS NULL AND EXISTS (SELECT 1 FROM `libraries` WHERE `name` = 'Imported');
UPDATE `groups`      SET `library_id` = (SELECT `id` FROM `libraries` WHERE `name` = 'Imported' LIMIT 1)
 WHERE `library_id` IS NULL AND EXISTS (SELECT 1 FROM `libraries` WHERE `name` = 'Imported');

-- "Which rows in this library?", which is what the exporter's scope resolution
-- and the owner's view of their own sharing both ask. library_id is the leading
-- column because it is the only equality in the query, and WITHOUT is the
-- partial form: a row in no library is precisely the set an operator needs to
-- see, because it is the set the gate refuses.
CREATE INDEX `idx_scenes_library`     ON `scenes`     (`library_id`, `id`) WHERE `library_id` IS NOT NULL;
CREATE INDEX `idx_images_library`     ON `images`     (`library_id`, `id`) WHERE `library_id` IS NOT NULL;
CREATE INDEX `idx_galleries_library`  ON `galleries`  (`library_id`, `id`) WHERE `library_id` IS NOT NULL;
CREATE INDEX `idx_performers_library` ON `performers` (`library_id`, `id`) WHERE `library_id` IS NOT NULL;
CREATE INDEX `idx_tags_library`       ON `tags`       (`library_id`, `id`) WHERE `library_id` IS NOT NULL;
CREATE INDEX `idx_studios_library`    ON `studios`    (`library_id`, `id`) WHERE `library_id` IS NOT NULL;
CREATE INDEX `idx_groups_library`     ON `groups`     (`library_id`, `id`) WHERE `library_id` IS NOT NULL;

-- At most one default library. A partial unique index rather than a Go
-- invariant, so a second default is refused by the database even if some future
-- write path adds one.
CREATE UNIQUE INDEX `idx_libraries_single_default` ON `libraries` (`is_default`)
  WHERE `is_default` = 1;
