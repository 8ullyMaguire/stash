-- Cluster merge and split records. StashForge M2c.
--
-- A merge is reversible and a split is a correction, so neither is expressed by
-- mutating the clusters involved. A merge points `loser.state` at 'merged' and
-- records where its members went; a split moves the offending face and records
-- that too. Both leave a row here, because "why are these two people the same
-- person" and "why was this face taken out" are the questions a user asks
-- second, and neither is answerable from the current state alone.
--
-- This is the same reasoning as `collab_audit` in M2 and migration 92's refusal
-- to store a tally: a decision's justification is a record, not a derivation.
CREATE TABLE `person_cluster_merges` (
  `id`          integer NOT NULL PRIMARY KEY AUTOINCREMENT,

  -- The cluster that survived. It is never NULL, and never the loser: a merge
  -- where both sides vanish would leave the members orphaned, and one where the
  -- loser survives would make the record ambiguous about which is which.
  `winner_id`   integer NOT NULL REFERENCES `person_clusters` (`id`),

  -- The cluster that was absorbed.
  --
  -- NO ON DELETE CASCADE, and the first version of this file had one. That was
  -- a real design error, caught by a test asserting the record survives: with a
  -- cascade, deleting the absorbed cluster deletes the record of what was merged
  -- into what, which is precisely the evidence the row exists to preserve.
  --
  -- The consequence is deliberate and worth stating: a cluster that has been
  -- absorbed cannot be hard-deleted while a merge record names it. That is the
  -- correct trade -- a cluster is not deleted, it is marked state='merged' and
  -- kept (see person_clusters), and the referential integrity here is what
  -- stops that invariant being bypassed by a careless DELETE.
  --
  -- The winner has the same constraint. A cluster that has won a merge is the
  -- subject of a decision somebody may want to reverse, and cascading away that
  -- record on deletion would make the reversal unanswerable.
  `loser_id`    integer NOT NULL REFERENCES `person_clusters` (`id`),

  -- 'merge' | 'split'. One table rather than two because they are the same
  -- event shape -- two clusters, one direction, a reason -- and a split is
  -- recorded as a merge from the surviving cluster INTO a new one, which keeps
  -- "what happened to this face" a single query.
  `kind`        varchar(8) NOT NULL CHECK (`kind` IN ('merge','split')),

  -- Why. Not optional in practice: a merge with no stated reason is a
  -- moderation queue item nobody can rule on, exactly as edit_proposals
  -- .rationale (migration 92).
  `reason`      text NOT NULL,

  -- Who did it. NULL is a genuine case and not a forgotten value: the
  -- consolidate stage (Commons §7.1 step 4) merges clusters without a user, and
  -- those merges are the ones most in need of being visible to one.
  `decided_by`  integer DEFAULT NULL REFERENCES `users` (`id`),

  `decided_at`  datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,

  -- A merge may not merge a cluster into itself. Obvious, and exactly the kind
  -- of thing a retried job produces: the second attempt sees one cluster and
  -- "merges" it with itself, creating a self-referential record that makes the
  -- cluster's member count read as doubled.
  CHECK (`winner_id` <> `loser_id`)
);

-- "What has been merged into this cluster?" -- the follow-up question after any
-- merge, and what a reversal needs to walk.
CREATE INDEX `idx_cluster_merges_winner` ON `person_cluster_merges` (`winner_id`, `decided_at` DESC);

-- "What was taken out of this cluster?" -- the same for the other direction.
CREATE INDEX `idx_cluster_merges_loser` ON `person_cluster_merges` (`loser_id`, `decided_at` DESC);

-- One merge per (winner, loser) pair. A retried consolidate pass that produced a
-- duplicate record would double-count the merge in any review UI, and the
-- duplicate is not information: the second attempt learned nothing new.
--
-- Partial on kind='merge' rather than a plain UNIQUE, because a SPLIT is
-- recorded as a merge from the surviving cluster into a new one, and splitting
-- the same face out of the same cluster twice is two real events -- the second
-- time it was a different face.
CREATE UNIQUE INDEX `idx_cluster_merges_one_per_pair`
  ON `person_cluster_merges` (`winner_id`, `loser_id`)
  WHERE `kind` = 'merge';
