-- Migration 110: mesh_replication_serve_budget
--
-- M8 step 0 probe 3, answered: a node can host replicas without becoming an
-- unbounded liability, provided the budget is enforced WHERE THE BYTES LEAVE.
--
-- The design decision this migration encodes, and why it is here rather than in
-- application code:
--
--   A bandwidth cap declared in settings is a promise. The mesh cannot rely on
--   a promise from a node it does not control, and a cap checked AFTER a
--   transfer is not a cap at all -- the bytes are already gone. So the budget
--   is a row, and the refusal happens at serve time by comparing the running
--   total against it. That makes the cap a property of the database, which is
--   the one place a peer cannot talk it out of.
--
-- WHAT IS NOT HERE, deliberately: a counter column. `bytes_served` would be a
-- stored tally, which is non-negotiable #4 ("computed, never stored") twice
-- over -- once for the score and once because a counter that can be incremented
-- without the transfer happening is exactly the laundering this project has
-- refused elsewhere. The total is a SUM over the immutable serve log below, so
-- it cannot drift from reality and cannot be edited.
--
-- The serve log is append-only by construction: no UPDATE or DELETE path is
-- offered for it, and the audit hash chain (migration 108) covers the same
-- property for collab_audit. This table is the mesh's equivalent, and it is
-- deliberately the same shape.

CREATE TABLE mesh_replication_serve_log (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,

    -- The content being served. This IS the capability: probe 1 established that
    -- the transports disclose peer addresses and no design here hides them, so
    -- authorisation is possession of the content hash and there is no identity
    -- to check. Nothing identifying crosses this boundary, in either direction.
    content_hash      TEXT    NOT NULL,

    -- Bytes actually sent. Recorded per serve so the budget is a sum over
    -- history rather than a mutable number.
    bytes             INTEGER NOT NULL CHECK (bytes >= 0),

    served_at         DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- The budget check's own access path: "how much has this node served for this
-- hash, and when". Without this the SUM is a full scan of the log per serve,
-- which is a table scan on the hot path of the thing the cap exists to bound.
CREATE INDEX idx_mesh_replication_serve_log_hash_time
    ON mesh_replication_serve_log (content_hash, served_at);

-- Per-node monthly budgets. One row per (node, period); there is no separate
-- node table because the mesh's node identity is not yet fixed by M8 step 8.1,
-- and inventing one here would commit a schema to a wire format the probes
-- have not settled yet.
--
-- `period` is a YYYY-MM string so the reset is a comparison rather than a
-- scheduled job: a budget for a period nobody is in any more simply stops
-- counting, with no cron and no state to migrate.
CREATE TABLE mesh_replication_budget (
    node_id           TEXT    NOT NULL,
    period            TEXT    NOT NULL,          -- 'YYYY-MM', UTC
    budget_bytes      INTEGER NOT NULL CHECK (budget_bytes >= 0),

    PRIMARY KEY (node_id, period)
);

-- A budget row with no bytes is indistinguishable from no budget, which is the
-- safe default: an unset budget means "serve nothing", not "serve everything".
-- The refusal path depends on this, so it is stated as a constraint rather than
-- left to the default a future reader would assume.
--
-- Note for the implementer of 8.1: this means a node MUST have a budget row
-- before it serves anything. That is intended, and the wiring test is
-- TestAReplicaFetchIsRefusedWithNoBudgetRow -- a node that joins the mesh
-- silent is a node that never becomes an unbounded liability.