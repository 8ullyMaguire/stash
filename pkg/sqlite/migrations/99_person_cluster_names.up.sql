-- Cluster naming. StashForge M2c, step 2.4b.7.
--
-- Two things, and the second is the reason this is a migration rather than a
-- field on the existing table.
--
-- 1. `person_clusters.name`. NULL, and NULL is the NORMAL case. Migrations 96
--    through 98 created a cluster that could not be named at all, which is a
--    table that can express every state except the one the feature exists for.
--
-- 2. `person_cluster_names`, an append-only audit of every name ever attached.
--
-- WHY THE NAME IS NOT JUST A COLUMN
--
-- A name on a cluster is a claim about a person's identity that the system
-- inferred and a human accepted. Six months later, when it turns out to have
-- been two people, the only question that matters is who decided and when. A
-- name written in place -- overwritten by the next rename -- answers nothing.
--
-- So `person_clusters.name` is a CACHE of the latest name, and this table is
-- the record. Every write to the cache goes through an insert here, in the same
-- transaction, and a rename is a new row rather than an update. "Was this always
-- this person?" stays answerable.
--
-- The alternative, a single mutable name column, is not less work: it is the
-- same work done once and then thrown away, with the record of it discarded.
ALTER TABLE `person_clusters` ADD COLUMN `name` text DEFAULT NULL;

-- The audit log. Append-only by construction -- this file is the only writer,
-- it only ever INSERTs, and there is deliberately no UPDATE or DELETE path in
-- the store.
CREATE TABLE `person_cluster_names` (
  `id`          integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `cluster_id`  integer NOT NULL REFERENCES `person_clusters` (`id`) ON DELETE CASCADE,

  -- NOT NULL and not DEFAULT ''. An empty name is not an unnamed cluster, it is
  -- a name that was set to nothing, and the store refuses to write one. A
  -- nullable name column with a CHECK against '' would allow the row; a NOT
  -- NULL column with no default refuses the write at the schema, which is one
  -- layer earlier and one layer harder to work around.
  `name`        text NOT NULL CHECK (length(trim(`name`)) > 0),

  -- Who decided. NOT NULL because an attribution that might be missing is not
  -- an attribution. 'system' is a legal value for an automated naming and is
  -- passed EXPLICITLY -- the store does not default it, because a row that says
  -- 'system' for a name a human typed is worse than no row at all: it looks
  -- like an attribution and is not one.
  `actor`       text NOT NULL CHECK (length(trim(`actor`)) > 0),

  `created_at`  datetime NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- "What has this cluster been called, and by whom?" -- read in order for the
-- rename history of one cluster. (cluster_id, created_at) rather than
-- (cluster_id) alone because the read is always ordered and usually small, but
-- a user with a long rename history on one cluster is exactly the case this
-- table exists for.
CREATE INDEX `idx_cluster_names_cluster` ON `person_cluster_names` (`cluster_id`, `created_at`);

-- "Who is still unnamed?" -- the review queue, and the single most common query
-- this feature makes. Partial on name IS NULL, because named clusters are the
-- minority in a corpus with no scraper and scanning them to find the ones
-- needing attention is scanning everything.
--
-- The index is on created_at, not on the primary key, because the review order
-- is oldest-first: the cluster that has been unnamed longest is the one a user
-- is most likely to have an opinion about.
CREATE INDEX `idx_clusters_unnamed` ON `person_clusters` (`created_at`, `id`)
  WHERE `name` IS NULL;
