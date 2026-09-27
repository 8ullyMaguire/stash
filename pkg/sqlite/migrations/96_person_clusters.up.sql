-- Person clusters. StashForge M2c.
--
-- The primitive that lets the amateur corpus work: a set of face embeddings
-- believed to be one person, with NO NAME REQUIRED, no studio, no credit.
--
-- The design decision this table exists to record, and the reason the plan
-- gives for inserting M2c before M3: an unnamed cluster is the DEFAULT, not a
-- fallback. A system that requires a name before it will link a person cannot
-- serve a corpus where the names are the thing that is missing.
--
-- The `ambiguous` state is a first-class row value rather than a soft flag
-- somewhere else, because the system knowing it does not know is a state the UI
-- has to render (stash-box #299, #846). Silently guessing is the failure mode
-- that makes people distrust the links.
CREATE TABLE `person_clusters` (
  `id`          integer NOT NULL PRIMARY KEY AUTOINCREMENT,

  -- NULL, and that is the normal case. An unnamed cluster is a valid, browsable
  -- state -- it is the DEFAULT -- so this column is not NOT NULL and nothing
  -- downstream may treat NULL as "incomplete".
  --
  -- The handle is a user-supplied label, deliberately not a performer foreign
  -- key: this item is linked to a CLUSTER, and the cluster is not yet a
  -- performer. Making it an FK would mean a cluster cannot exist without
  -- pointing at a performer row, which is the requirement this whole milestone
  -- exists to lift.
  `handle`      text DEFAULT NULL,

  -- avatar is likewise optional and likewise not a requirement. A cluster with
  -- no name and no picture is still a cluster the user can browse and act on.
  `avatar_path` text DEFAULT NULL,

  -- `state` is where the ambiguity lives. The values:
  --
  --   singleton     one appearance so far. Not a special case: it is the state
  --                 every new face starts in, and it is what makes the "three or
  --                 more appearances before it is meaningful to a user" rule
  --                 computable.
  --   settled       more than one appearance, mutually consistent, no
  --                 outstanding conflict.
  --   ambiguous     conflicting evidence -- one embedding matched two distinct
  --                 candidates, or a merge is pending disambiguation. The UI
  --                 shows BOTH candidates and a "this is two people" action.
  --                 Never auto-resolved.
  --   merged        superseded by another cluster. Kept as a row rather than
  --                 deleted so a merge is auditable and reversible; see
  --                 person_cluster_members.moved_to.
  --
  -- A CHECK because the list is closed and small, and a state outside it is a
  -- bug that must not become a row.
  `state`       varchar(16) NOT NULL DEFAULT 'singleton'
    CHECK (`state` IN ('singleton','settled','ambiguous','merged')),

  -- `confidence` is the engine's belief that this is one person, 0..1. It is a
  -- claim about the ENGINE, not a guarantee, and the UI must be able to show it
  -- so a user can interrogate a link (Commons §7.4: a link the user cannot
  -- interrogate is a link they will not trust).
  --
  -- Nullable on purpose. A singleton has no merge decision behind it, so a
  -- confidence for it would be an invented number. NULL means "no confidence
  -- claim has been made", which is a different statement from 0.0.
  `confidence`  real DEFAULT NULL CHECK (`confidence` IS NULL OR (`confidence` >= 0.0 AND `confidence` <= 1.0)),

  -- Which identity engine produced this. 'face' today; 'composite' once
  -- Commons §7.4's body/appearance embedding lands. Stored rather than inferred
  -- so a score from one engine is never read as a score from another -- the
  -- thresholds differ, and mixing them silently is how a corpus ends up with
  -- links nobody can reproduce.
  `engine`      varchar(16) NOT NULL DEFAULT 'face'
    CHECK (`engine` IN ('face','composite')),

  `created_at`  datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`  datetime NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- "What is still unresolved?" -- the ambiguity review queue. Partial on
-- `state`, because the overwhelmingly common row is a settled cluster and a scan
-- of those to find the handful needing attention is a scan of everything.
CREATE INDEX `idx_person_clusters_state` ON `person_clusters` (`state`)
  WHERE `state` = 'ambiguous';

-- "Whose clusters are these?" -- the per-user view, and the one a claim (Commons
-- §7.5) is looked up through.
CREATE INDEX `idx_person_clusters_created` ON `person_clusters` (`created_at` DESC);
