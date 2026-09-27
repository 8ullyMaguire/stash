-- Fix an over-broad UNIQUE that made `ambiguous` unrepresentable. StashForge M2c,
-- step 2.4b.7.
--
-- THE BUG
--
-- Migration 97 ended person_cluster_members with:
--
--   UNIQUE (target_type, target_id, frame_index, face_left, face_top)
--
-- The stated intent was right -- "a face appears in a given target at a given
-- frame once, so re-detecting the same file must UPDATE this row rather than
-- add a second" -- and the constraint does prevent that. But it prevents
-- something else as well, and that something else is a state the spec requires.
--
-- §7.1 defines `ambiguous` as "conflicting evidence -- one embedding matched two
-- distinct candidates". Two DISTINCT candidates means two cluster_ids for one
-- appearance. The UNIQUE above does not include cluster_id, so it forbids
-- exactly that: a face can be claimed by ONE cluster and no more.
--
-- So the first-class ambiguous state, the state the whole "the system knowing
-- it does not know is a state the UI has to render" argument rests on, was
-- unrepresentable in the schema. The store could not record a conflict, could
-- not show the user both candidates, and ClustersForTarget could never return
-- more than one row -- which is the query whose multi-row result IS the
-- ambiguity signal. A test caught it when it tried to record one.
--
-- This is the class of bug the migration-comment discipline exists to prevent:
-- the comment above the constraint states an intent, the constraint implements a
-- broader rule, and nothing checks that the implementation matches the intent.
--
-- THE FIX
--
-- Scope the uniqueness to WITHIN a cluster, which is what the intent actually
-- was. Re-detecting the same crop into the same cluster still cannot add a
-- second row, so the rescan-inflation problem is solved. Two clusters claiming
-- one appearance is now permitted, which is what makes `ambiguous` recordable.
--
-- A UNIQUE CONSTRAINT cannot be dropped by ALTER TABLE in SQLite -- there is no
-- DROP CONSTRAINT -- so the table is rebuilt. That is the standard procedure and
-- it is why the CREATE below is a verbatim copy: the new table must differ from
-- the old one in exactly one respect, or this migration becomes an opportunity
-- to silently change something nobody reviewed.
--
-- The foreign key to person_clusters is recreated, and PRAGMA foreign_keys is
-- toggled around the rebuild because a foreign_keys=ON connection cannot drop a
-- table another table references. person_cluster_members is referenced by
-- nothing, but the toggle is unconditional rather than conditional on that
-- being true today: a later migration adding a reference to this table must not
-- fail at COPY time with a confusing error.

PRAGMA foreign_keys = OFF;

CREATE TABLE `person_cluster_members_new` (
  `id`           integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `cluster_id`   integer NOT NULL REFERENCES `person_clusters` (`id`) ON DELETE CASCADE,
  `target_type`  varchar(64) NOT NULL,
  `target_id`    integer NOT NULL,
  `frame_index`  integer NOT NULL DEFAULT 0,
  `face_left`    integer NOT NULL DEFAULT 0,
  `face_top`     integer NOT NULL DEFAULT 0,
  `face_width`   integer NOT NULL DEFAULT 0,
  `face_height`  integer NOT NULL DEFAULT 0,
  `detector_score` real NOT NULL DEFAULT 0.0
    CHECK (`detector_score` >= 0.0 AND `detector_score` <= 1.0),
  `distance`     real NOT NULL DEFAULT 0.0 CHECK (`distance` >= 0.0),
  `embedding`    blob NOT NULL,

  -- Scoped to the cluster, not global. A face appears once IN A GIVEN CLUSTER at
  -- a given position, so re-detecting the same file updates this row rather than
  -- adding a second and inflating the cluster size. Two clusters claiming one
  -- appearance is permitted, and is the `ambiguous` state rather than a
  -- duplicate.
  UNIQUE (`cluster_id`, `target_type`, `target_id`, `frame_index`, `face_left`, `face_top`)
);

INSERT INTO `person_cluster_members_new`
  (`id`, `cluster_id`, `target_type`, `target_id`, `frame_index`,
   `face_left`, `face_top`, `face_width`, `face_height`,
   `detector_score`, `distance`, `embedding`)
SELECT
  `id`, `cluster_id`, `target_type`, `target_id`, `frame_index`,
  `face_left`, `face_top`, `face_width`, `face_height`,
  `detector_score`, `distance`, `embedding`
FROM `person_cluster_members`;

DROP TABLE `person_cluster_members`;
ALTER TABLE `person_cluster_members_new` RENAME TO `person_cluster_members`;

PRAGMA foreign_keys = ON;

-- The two indexes from migration 97 are dropped with the table and must be
-- recreated, or every cluster-member read becomes a table scan. This is the
-- cost of a table rebuild in SQLite and the reason to batch schema changes
-- rather than doing one per week.

-- "Who is in this cluster?" -- the one query that dominates, and the one the
-- over-merge guard runs per member. Covering cluster_id is what lets the guard
-- load a whole cluster's distances without a table scan per face.
CREATE INDEX `idx_cluster_members_cluster` ON `person_cluster_members` (`cluster_id`, `distance`);

-- "Which clusters claim this face?" -- the assign step, and the query that
-- FINDS the conflict the ambiguous state exists to record. Without it, a face
-- already in a cluster is invisible to the engine and gets assigned again.
--
-- This index is why the rebuild was worth doing carefully: `ClustersForTarget`
-- returning more than one row IS the ambiguity signal, and it reads this index
-- to do it.
CREATE INDEX `idx_cluster_members_target` ON `person_cluster_members` (`target_type`, `target_id`);
