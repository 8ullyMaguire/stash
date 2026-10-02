-- stash#3530 -- multiple scenes in a single file: TIME RANGE ON THE LINK.
--
-- THE SCHEMA ALREADY ALLOWS MANY SCENES PER FILE. `scenes_files` has PRIMARY KEY
-- (scene_id, file_id) and only a NON-unique index on file_id, so two scene rows may already
-- reference one file row. The referential half of this issue is therefore NOT a schema
-- change; what does not exist anywhere -- no start/end on scenes_files, on files, or on
-- video_files -- is WHICH PART OF THE FILE. That is what these two columns add.
--
-- See docs/ISSUE-3530-spec.md section 3 for why this is a time range and not a real segment
-- entity. The short version: a segment FILE would have to be a `files` row -- fingerprinted,
-- under a folder, scanned, duplicate-checked, cascade-deleted -- and producing one means
-- transcoding media on disk, whose failure mode is silent data loss or silent disk growth.
-- This writes no media at all, and this migration is reversible by dropping two columns.
--
-- BOTH NULLABLE, AND NULL MEANS "THE WHOLE FILE". Every pre-existing row has NULL here, so
-- the migration is a no-op for every current user and the derived duration reduces to
-- video_files.duration EXACTLY -- the same float, no backfill. A backfill writing 0 or NULL
-- into a duration column is how a migration silently turns every scene into a zero-length
-- one, so there is deliberately nothing to backfill.
--
-- WHY THIS IS A TABLE REBUILD, AND WHY THAT WAS NEARLY WRONG. The obvious way to add a CHECK
-- to an existing SQLite table is `ALTER TABLE ... ADD CONSTRAINT`, and it WORKS -- on the
-- `sqlite3` CLI, and on go-sqlite3 v1.14.52. This repo pins go-sqlite3 v1.14.22, whose
-- bundled SQLite is 3.45.1, and that build REJECTS the statement:
--
--     bundled sqlite: 3.45.1
--       ALTER TABLE t ADD COLUMN c float CHECK (c >= 0)   ok
--       ALTER TABLE t ADD CONSTRAINT k CHECK (a > 0)      ERR: near "CONSTRAINT": syntax error
--
-- The CLI being newer than the app's own driver is what made the wrong version of this
-- migration look correct; both were measured before choosing. `ADD COLUMN ... CHECK` IS
-- supported and is used below, so no rebuild is needed for the CHECKs themselves -- the
-- rebuild below exists for the INDEXES, because a new table drops the old ones silently and
-- `unique_index_scenes_files_on_primary` is the guarantee that a scene has exactly one
-- primary file. Losing it is a data-integrity regression no test in the repo would notice.
--
-- `PRAGMA legacy_alter_table` is deliberately NOT used: it would let the RENAME rewrite
-- references to the temporary table. The explicit create/copy/drop/rename sequence is the
-- one already proven in 32_files.up.sql, and following a working in-repo pattern beats
-- relying on a pragma whose behaviour across versions is not something to bet a migration on.
--
-- The appSchemaVersion bump to 122 must land in the SAME commit as this file: the runner
-- refuses to open a database whose schema is ahead of the binary, and the symptom is
-- "the column is simply absent", which reads as a migration that did not run rather than a
-- version that was not bumped.

CREATE TABLE `scenes_files_new` (
  `scene_id` integer NOT NULL,
  `file_id` integer NOT NULL,
  `primary` boolean NOT NULL,
  `start_time` float,
  `end_time` float,
  -- A range is a window onto the file, so both ends are non-negative and an end that is not
  -- after the start describes an empty or inverted window. `NULL` on either side means
  -- unbounded, which is why each arm tests for NULL rather than comparing the whole pair.
  CONSTRAINT `scenes_files_start_time_non_negative`
    CHECK (`start_time` IS NULL OR `start_time` >= 0),
  CONSTRAINT `scenes_files_end_time_non_negative`
    CHECK (`end_time` IS NULL OR `end_time` >= 0),
  CONSTRAINT `scenes_files_end_after_start`
    CHECK (`end_time` IS NULL OR `start_time` IS NULL OR `end_time` > `start_time`),
  foreign key(`scene_id`) references `scenes`(`id`) on delete CASCADE,
  foreign key(`file_id`) references `files`(`id`) on delete CASCADE,
  PRIMARY KEY(`scene_id`, `file_id`)
);

-- Both new columns are explicitly NULL: this is the whole point, and a column added without
-- a default would leave the INSERT below dependent on column order for every existing row.
INSERT INTO `scenes_files_new` (`scene_id`, `file_id`, `primary`, `start_time`, `end_time`)
  SELECT `scene_id`, `file_id`, `primary`, NULL, NULL FROM `scenes_files`;

DROP TABLE `scenes_files`;
ALTER TABLE `scenes_files_new` RENAME TO `scenes_files`;

-- Recreated by hand because the DROP above took them with it. `index_scenes_files_file_id`
-- is what makes "which scenes use this file" cheap; the partial unique index is what
-- guarantees one primary file per scene.
CREATE INDEX `index_scenes_files_file_id` ON `scenes_files` (`file_id`);
CREATE UNIQUE INDEX `unique_index_scenes_files_on_primary` on `scenes_files` (`scene_id`) WHERE `primary` = 1;