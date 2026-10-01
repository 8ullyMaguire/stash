-- Migration 113: mesh_replica
--
-- M7 step 7.1. A replica of a scene held on THIS instance. Migration 110 records
-- the bytes this instance has served to others; this records the bytes it has
-- taken in.
--
-- health IS A CHECK, NOT A COUNTER. There is no `verification_score` and no
-- `failure_count`: a replica is either verified against its manifest or it is
-- not, and a count would be a stored tally (non-negotiable #4) that drifts from
-- the thing it is supposed to summarise. `verified_at` is the evidence for the
-- current `health`, and it is NULL whenever health is anything but 'verified',
-- so the two cannot disagree.
--
-- §6a.6 NAMESPACING, and the load-bearing decision here. A peer's `scene 412`
-- is not this instance's `scene 412`. `source_endpoint` is part of the UNIQUE key
-- for that reason: without it, two peers serving the same scene_id collide into
-- one row, and the mesh then believes it holds a replica it does not have -- a
-- missing copy that health checks pass, which is the worst possible failure for
-- the mechanism whose entire purpose is knowing what you still hold.
--
-- The consequence, and it is why scene_id is a plain INTEGER with a foreign key
-- rather than a composite type: a replica is keyed by LOCAL scene_id. A peer that
-- offers an id we do not have needs an identification solve (step 7.5) to become
-- a local id first. Deciding that here instead would mean inventing a
-- cross-instance id space in a schema, before step 7.1's wire format says how ids
-- are exchanged.
--
-- replica_path is UNDER THE STORAGE ROOT and sanitised. It is a relative path,
-- never an absolute one and never one containing a peer-supplied component: a
-- path from a stranger is the one input here that can name a file outside the
-- library, and non-negotiable #13 (no path or filename crosses a node boundary)
-- is enforced by the exporter guard and needs its mirror here.

CREATE TABLE mesh_replica (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,

    -- LOCAL scene id. See the header: a remote id is resolved by an
    -- identification solve before it can appear here.
    scene_id     INTEGER NOT NULL REFERENCES scenes(id) ON DELETE CASCADE,

    -- Who supplied it, and part of the identity of the row per §6a.6.
    source_endpoint TEXT NOT NULL,

    -- Relative to the storage root, sanitised. CHECKed below for the shapes that
    -- would escape it.
    replica_path TEXT NOT NULL,

    -- What we must still verify: the hash of the manifest describing this copy.
    -- BLOB because it is a digest and not a number or a name. NOT NULL because a
    -- replica with nothing to verify against cannot be verified later.
    manifest_hash BLOB NOT NULL,

    health       TEXT NOT NULL DEFAULT 'pending'
        CHECK (health IN ('pending','verified','corrupt','missing')),

    -- The evidence for 'verified', and NULL otherwise. See the header.
    verified_at  TIMESTAMP,

    -- EVERY TABLE CHECK IS BELOW THE LAST COLUMN. SQLite only accepts table
    -- constraints after the column definitions; one interleaved with the columns
    -- is a syntax error reported at the NEXT column name, which points nowhere
    -- near the real cause. See migration 112 for the measured case.

    -- §6a.6: the namespace is part of the key.
    UNIQUE (scene_id, source_endpoint),

    -- A segment with no video stream can still be written and still be
    -- non-empty, so the path check below is the only thing standing between a
    -- stray filename and the filesystem.
    CHECK (length(trim(replica_path, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) > 0),
    CHECK (length(trim(source_endpoint, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) > 0),

    -- Path safety, as close to the database as it can be enforced. An absolute
    -- path or one that walks up out of the storage root is refused by the
    -- DATABASE rather than by whichever caller remembered to sanitise.
    CHECK (replica_path NOT LIKE '/%'),
    CHECK (replica_path NOT LIKE '%..%'),

    -- 'verified' is a claim, and a timestamp is its evidence. Either alone is
    -- half a statement, so the database refuses to hold one without the other.
    CHECK (
        (health = 'verified' AND verified_at IS NOT NULL)
        OR (health <> 'verified' AND verified_at IS NULL)
    )
);

-- Health checks sweep by health, and a missing replica is what they look for
-- (§6a.9: "a peer going offline is detected as a missing replica"), so this is
-- indexed on the column that is actually queried. Finding every replica of one
-- scene is served by the UNIQUE index's leading column and needs no index of
-- its own.