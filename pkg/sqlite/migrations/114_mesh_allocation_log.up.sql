-- Migration 114: mesh_allocation_log
--
-- M8 step 8.4, R080: "Storage allocation log so an operator can audit what was
-- placed and why."
--
-- WHAT "WHY" MEANS HERE IS THE WHOLE REQUIREMENT. Migration 113 records WHICH
-- replica exists, from WHICH peer, and whether it verified. That answers "what do
-- I hold". It cannot answer the question R080 exists for -- "why is this instance
-- storing a fourth copy of a scene nobody here watches, on a disk I am paying
-- for". A row with no stated reason is an answer an operator has to reconstruct
-- from a scheduler's mood, and a scheduler's mood is not a record.
--
-- SO `reason` IS NOT NULL AND NOT AN ENUM. An enum would be a closed list of
-- justifications, and every justification nobody anticipated becomes a
-- NULL -- which is precisely the row that needs explaining. The closed list is
-- the failure mode. What is enforced instead is that a reason EXISTS and is
-- non-blank, so a placement cannot be unexplained, while leaving what counts as
-- an explanation to whoever is explaining it.
--
-- WHY THIS IS HASH-CHAINED, which is the part that looks like overkill until you
-- ask who is being protected from. Migration 108 chains `collab_audit` for the
-- same reason and states it: §5.1 says the owner is not an admin over content, and
-- a log a privileged SQL session can rewrite does not support that. The operator
-- who most needs this log is the one DISSERVED by a rewritten row -- a node that
-- quietly placed three times more than the policy allows would show nothing. A
-- log whose integrity depends on the absence of malicious code, in a codebase
-- that runs peer-supplied data, is not an audit trail.
--
-- Each row stores sha256(prev_hash || canonical row), so changing any field of any
-- row invalidates every hash after it and a verify walk stops at the first break
-- and names the row. Deleting a row from the middle is detected the same way: the
-- successor no longer chains. GENESIS is 64 zeros, so the chain is whole from the
-- first allocation this instance ever made.
--
-- `at` IS PART OF THE HASHED PAYLOAD. It is the one field an operator might
-- reasonably want to rewrite after the fact, and including it is the point: if the
-- time a placement happened can be edited, the record is not a record.
--
-- WHAT THIS DOES NOT LOG, and probe 3's finding stands: this answers INBOUND.
-- Migration 110 records what this instance served and to whom, so the outbound
-- direction exists too, but it is a separate table with a separate chain. Joining
-- them into one log would let a row say "placed" and "served" for bytes that never
-- moved, and the audit question "where did this come from and where did it go"
-- needs both sides to be independently verifiable first.

CREATE TABLE mesh_allocation_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,

    -- LOCAL scene id, same namespace as mesh_replica.scene_id: §6a.6 says a
    -- peer's `scene 412` is not this instance's `scene 412`, and a replica is
    -- keyed by the local id an identification solve already produced. This is the
    -- SAME id, deliberately -- the log is about the row in 113, so a reader must
    -- not have to translate between two id spaces to join them.
    scene_id     INTEGER NOT NULL REFERENCES scenes(id) ON DELETE CASCADE,

    -- Who supplied it. Copied from 113 rather than joined, because a log row that
    -- DEPS ON the row it audits can be made to lie by rewriting that row: delete
    -- mesh_replica and the log's own subject vanishes with it. ON DELETE CASCADE
    -- below would then take the log entry too. The copy is what makes the log
    -- survive the thing it records.
    source_endpoint TEXT NOT NULL,

    -- R080's actual content: WHY this instance placed it. Free text, non-blank.
    -- See the header for why this is not an enum.
    reason       TEXT NOT NULL,

    -- What the placement COST. Recorded because "how much disk did that decision
    -- use" is the question an operator actually asks when auditing, and a log
    -- that answers only "why" leaves them to add it up.
    bytes        INTEGER NOT NULL DEFAULT 0 CHECK (bytes >= 0),

    -- When. Hashed, so it cannot be edited after the fact.
    at           TIMESTAMP NOT NULL,

    -- See the header: sha256(prev_hash || canonical row), genesis 64 zeros.
    prev_hash    BLOB NOT NULL,
    row_hash     BLOB NOT NULL,

    -- EVERY TABLE CHECK IS BELOW THE LAST COLUMN. SQLite only accepts table
    -- constraints after the column definitions; one interleaved with the columns
    -- is a syntax error reported at the NEXT column name, which points nowhere
    -- near the real cause. See migration 112 for the measured case.
    --
    -- trim() INCLUDES EVERY WHITESPACE CHARACTER and not just the space, because
    -- SQLite's trim(X) strips spaces ONLY. A tab-only reason has length 1 after
    -- trim() and would pass a length(trim(x)) > 0 check that looks obviously
    -- right. Migrations 99 and 101 have that hole; it is recorded, not patched,
    -- because an applied migration is immutable.
    CHECK (length(trim(reason, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) > 0),
    CHECK (length(trim(source_endpoint, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) > 0),

    -- The chain needs an answerable question: "every placement for this scene, in
    -- the order it happened". Both columns lead that access path.
    CHECK (length(prev_hash) = 32),
    CHECK (length(row_hash) = 32)
);

-- One placement, one entry. A retry after a crash that re-ran the scheduler would
-- otherwise append a second identical row for the same replica, and an operator
-- counting placements would count the crash.
--
-- The idempotency key is (scene_id, source_endpoint) because that is mesh_replica's
-- own UNIQUE key -- one row per peer per scene. A placement is identified by the
-- replica it placed, so the log is at most one row per replica, which is what
-- makes "how many copies of this scene does this instance hold" a COUNT rather
-- than an interpretation.
CREATE UNIQUE INDEX idx_mesh_allocation_log_replica
    ON mesh_allocation_log (scene_id, source_endpoint);

-- Reading a scene's placements in order is the access path an audit walks, and the
-- unique index above already serves (scene_id, ...) for the lookup -- but not the
-- ordering. Verified after the fact is the order an operator reads, so the ORDER BY
-- has an index behind it.
CREATE INDEX idx_mesh_allocation_log_scene_time
    ON mesh_allocation_log (scene_id, at);

-- The verify walk goes in id order along the chain, matching 108.
CREATE INDEX idx_mesh_allocation_log_chain
    ON mesh_allocation_log (id);
