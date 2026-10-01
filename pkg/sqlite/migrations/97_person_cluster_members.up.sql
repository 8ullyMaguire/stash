-- Cluster membership. StashForge M2c.
--
-- One row per (cluster, face appearance). The embedding itself is stored here
-- rather than in a separate table because the ANN index and the membership are
-- the same set of things: an appearance that is not a member is an appearance
-- that was never indexed, and an indexed appearance that is not a member is the
-- bug.
CREATE TABLE `person_cluster_members` (
  `id`           integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `cluster_id`   integer NOT NULL REFERENCES `person_clusters` (`id`) ON DELETE CASCADE,

  -- Where this appearance came from. Deliberately loose: a face can appear in a
  -- scene, a gallery image, a group, or a user-supplied headshot, and this
  -- table must not become the place that decides which of those exist. The
  -- target is resolved at display time, exactly as edit_proposals.target_type
  -- is (migration 92) -- denormalised, not seven nullable foreign keys.
  `target_type`  varchar(64) NOT NULL,
  `target_id`    integer NOT NULL,

  -- Offset within the target, for a video: the same face at second 12 and at
  -- second 9000 are two appearances of one person, and collapsing them would
  -- make the cluster size a lie.
  `frame_index`  integer NOT NULL DEFAULT 0,

  -- The detector's own crop, in pixel coordinates within the sampled frame. Kept
  -- so a user can be shown WHY two appearances were linked, and so a split can
  -- show the offending face without re-running detection.
  `face_left`    integer NOT NULL DEFAULT 0,
  `face_top`     integer NOT NULL DEFAULT 0,
  `face_width`   integer NOT NULL DEFAULT 0,
  `face_height`  integer NOT NULL DEFAULT 0,

  -- detector_score is the DETECTOR's confidence that a face is there at all.
  -- Distinct from the cluster's confidence, which is the engine's belief about
  -- identity. Conflating them is how a low-quality detection ends up presented
  -- as a doubtful identity match.
  `detector_score` real NOT NULL DEFAULT 0.0
    CHECK (`detector_score` >= 0.0 AND `detector_score` <= 1.0),

  -- distance is the embedding distance to the cluster it was ASSIGNED to, not
  -- to its nearest neighbour at the time. Recorded per member because the
  -- cluster centroid moves as members are added, and "this face was 0.31 from
  -- the centroid when it joined" is the fact a user needs when they ask why.
  --
  -- Lower is closer. Stored rather than recomputed because the centroid it was
  -- measured against no longer exists once the cluster grows.
  `distance`     real NOT NULL DEFAULT 0.0 CHECK (`distance` >= 0.0),

  -- The embedding, as a BLOB of little-endian float32. Deliberately NOT NULL
  -- and NOT normalised here: normalisation is a policy decision (§7.4's
  -- composite score compares against a different norm), and storing the raw
  -- vector means a future score can be computed without re-running detection on
  -- the whole library.
  --
  -- The dimension is not a column because the engine column determines it, and
  -- a redundant copy is a second thing to keep in step.
  `embedding`    blob NOT NULL,

  -- A face appears in a given target at a given frame once. Re-detecting the
  -- same file must UPDATE this row rather than add a second, or a rescan
  -- inflates the cluster size and the "three or more appearances" threshold is
  -- crossed by one face seen three times.
  UNIQUE (`target_type`, `target_id`, `frame_index`, `face_left`, `face_top`)
);

-- "Who is in this cluster?" -- the one query that dominates, and the one the
-- over-merge guard runs per member. Covering cluster_id is what lets the guard
-- load a whole cluster's distances without a table scan per face.
CREATE INDEX `idx_cluster_members_cluster` ON `person_cluster_members` (`cluster_id`, `distance`);

-- "Which clusters claim this face?" -- the assign step, and the query that finds
-- the conflict the ambiguous state exists to record. Without it, a face already
-- in a cluster is invisible to the engine and gets assigned again.
CREATE INDEX `idx_cluster_members_target` ON `person_cluster_members` (`target_type`, `target_id`);
