-- Migration 112: mesh_peer
--
-- M7 step 7.1. A peered instance: the remote half of the federation protocol.
--
-- THE POINT OF THIS TABLE IS WHAT IT IS NOT ALLOWED TO BE USED FOR.
--
-- Every `claimed_*` and `trust_profile` column below is the PEER'S OWN CLAIM
-- about itself, received over the wire from a stranger. It is unverified by
-- construction: we did not measure it, we were told it. Nothing may size an
-- allocation, decide a replica target, reserve a buffer, or skip a verification
-- from one of these numbers. They exist so the protocol has somewhere to record
-- what a peer said, and so a human can see when a peer's claims drift.
--
-- This is stated at the schema because the failure mode is silent. The first
-- version of a mesh node sizes a buffer from a stranger's `claimed_store_bytes`,
-- it works perfectly for every cooperative peer, and it does not notice for a
-- week -- because nothing raises an error, the numbers are simply wrong. The
-- claim being three orders of magnitude off is indistinguishable from the
-- allocator being generous.
--
-- internal/mesh carries a grep-level guard for exactly this
-- (TestAProfileClaimIsLabelledAClaim): `claimed_store_bytes` and
-- `claimed_bandwidth_bps` must not appear in any sizing path.
--
-- `public_key` is NOT a claim. It is the key the peer signs with, and it is the
-- one field here that is checkable: a handshake is only agreed once a signature
-- under this key verifies. It is NOT NULL because a peer with no key cannot be
-- verified and must not be stored as if it could.

CREATE TABLE mesh_peer (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,

    -- The peer's claimed stable public identity, UNIQUE because two rows for
    -- one peer would let the two disagree about the same instance's claims.
    instance_id    TEXT NOT NULL UNIQUE,

    -- How we reach it. NOT NULL and not defaulted: a peer we cannot address is
    -- not a peer.
    endpoint       TEXT NOT NULL,

    -- NOT NULL: unverified is not the same as unsigned, and a row that could
    -- hold a peer with no key would let a later reader mistake one for the
    -- other.
    public_key     BLOB NOT NULL,

    -- --- claims: the peer's statements about itself. Never sizing inputs. ---

    claimed_store_bytes    INTEGER,
    claimed_bandwidth_bps  INTEGER,

    -- A peer's claim about its OWN trust standing. A claim about a claim, and
    -- doubly so: §6a.10 has reward never granting access, so a peer cannot grant
    -- us standing either.
    trust_profile          BLOB,

    -- Agreed and revoked are both timestamps rather than a boolean `active`,
    -- because "when did this peer stop being trusted" is the question an
    -- incident review actually asks, and a boolean cannot answer it.
    agreed_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    revoked_at     TIMESTAMP,

    -- EVERY TABLE CHECK IS BELOW THE LAST COLUMN, and that is a SQLite rule
    -- rather than a style preference.
    --
    -- A table-level CHECK is a table CONSTRAINT, and a constraint may only appear
    -- where a constraint may appear: after the column definitions. Put one in the
    -- middle of the column list and parsing stops at the next column name --
    -- "near agreed_at: syntax error" -- with no hint that the problem is a CHECK
    -- two lines further up. Measured, so the next reader does not have to be:
    --
    --     a, b, CHECK, CHECK, CHECK   -> OK
    --     a, CHECK, b, CHECK          -> ERROR near "b"
    --
    -- So it is neither "a CHECK cannot precede a column it names" (a forward
    -- reference) nor "one trailing CHECK is fine". It is: no constraint may be
    -- interleaved with columns. All of them go at the end.

    -- Blank is refused, and "blank" means ANY whitespace: SQLite's
    -- single-argument trim() strips SPACES ONLY, so the obvious
    -- length(trim(x)) > 0 accepts a tab-only value. Measured:
    -- length(trim(char(9))) is 1, not 0. Migrations 99 and 101 use the
    -- one-argument form and have that hole; recorded rather than quietly patched,
    -- since editing an applied migration is not allowed.
    CHECK (length(trim(endpoint, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) > 0),
    CHECK (length(trim(instance_id, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) > 0),

    -- Peer claims are non-negative quantities or absent. A negative store size
    -- would make "does this peer have room" true in a way arithmetic does not
    -- expect, and it would be a lie rather than an error. NULL stays allowed: a
    -- peer that has not advertised a figure differs from one advertising zero.
    CHECK (claimed_store_bytes   IS NULL OR claimed_store_bytes   >= 0),
    CHECK (claimed_bandwidth_bps IS NULL OR claimed_bandwidth_bps >= 0),

    -- A peer revoked in the future has not been revoked yet; storing one anyway
    -- would make `revoked_at IS NOT NULL` (the revocation test) disagree with
    -- the time.
    CHECK (revoked_at IS NULL OR revoked_at <= CURRENT_TIMESTAMP)
);

-- The revocation test is `revoked_at IS NULL`, so this index serves it directly.
-- The plan's sketch put `endpoint` on this index instead, for a lookup this
-- schema does not do.
CREATE INDEX idx_mesh_peer_active ON mesh_peer(revoked_at);

-- The handshake looks a peer up by identity before its claims have been
-- verified. UNIQUE already indexes that; named explicitly so the intent
-- survives someone reading the schema and wondering which path it serves.
CREATE INDEX idx_mesh_peer_instance ON mesh_peer(instance_id);