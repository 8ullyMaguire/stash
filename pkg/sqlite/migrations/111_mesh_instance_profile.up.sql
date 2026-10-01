-- Migration 111: mesh_instance_profile
--
-- M7 step 7.1. The instance's own identity in the mesh, and the gravity it
-- publishes. This is the local half of the federation protocol; migration 112 is
-- the peers' half, and 113 the replicas.
--
-- NUMBERING. The plan said this phase starts at 107, from a count taken before
-- M8's work. Migrations 107/108/109 went to phash, the collab audit hash chain
-- and the governance shadow log, and 110 is the mesh serve budget, so this
-- starts at 111. Never edit an applied migration.
--
-- SINGLETON BY CHECK, NOT BY CONVENTION. `id = 1` as a column CHECK means the
-- table cannot physically hold a second identity, so there is no code path --
-- no insert, no migration, no bug -- that can produce two. The usual alternative
-- is a UNIQUE column nobody remembers to set, which permits a second row the
-- first time two of them disagree about the default.
--
-- taste_profile IS A PUBLISHED VIEW, NOT A SCORE. It holds the last DERIVATION
-- of local records and is rebuilt from them by step 7.4 (the taste
-- fingerprint). It is NOT a counter that gets incremented, and nothing may read
-- it as an authority about what this instance likes -- per non-negotiable #4 the
-- ranking that consumes it is computed from records. The comment here exists
-- because a BLOB column named `taste_profile` is exactly the shape that grows a
-- writer which updates it in place, and that writer would be the bug.
--
-- gravity is DIFFERENT and is operator-set: §6a.8 makes gravity the theme an
-- instance bends toward, explicitly NOT an override of any individual entity's
-- rank (#6). It is a weight over categories, set deliberately by a human, and
-- is therefore legitimately stored.

CREATE TABLE mesh_instance_profile (
    -- Exactly one row, enforced by the database.
    id              INTEGER PRIMARY KEY CHECK (id = 1),

    -- The stable public identity this instance publishes. UNIQUE because it is
    -- what peers key on: two profiles claiming one instance_id would make every
    -- federated lookup ambiguous.
    instance_id     TEXT NOT NULL UNIQUE,

    display_name    TEXT NOT NULL,

    -- The last published derivation of local records. Rebuilt from them; see the
    -- header. Never incremented, never treated as authoritative.
    taste_profile   BLOB,

    -- Operator-set axes (see §6a.8). Null until an operator sets them: an
    -- instance with no gravity bends toward nothing, which is different from an
    -- instance whose gravity happens to be uniform, so NULL and zero are not
    -- the same and there is no DEFAULT.
    gravity         BLOB,

    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- BLANK IS REFUSED, AND "BLANK" MEANS ANY WHITESPACE.
    --
    -- NOT NULL admits '' and '   ', and a blank public identity would be accepted
    -- silently by every other rule on this table -- it satisfies UNIQUE like any
    -- other value does.
    --
    -- The character list is explicit because SQLite's single-argument trim()
    -- strips SPACES ONLY. Measured: length(trim(char(9))) is 1, not 0, so the
    -- obvious length(trim(x)) > 0 accepts a tab-only value. Migrations 99 and 101
    -- use the one-argument form and have that hole; recorded rather than quietly
    -- patched, since editing an applied migration is not allowed. SQLite has no
    -- regex in the default build, so char() is how to say "any whitespace".
    CHECK (length(trim(instance_id, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) > 0),
    CHECK (length(trim(display_name, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))) > 0)
);

-- The peers read `updated_at` to decide whether to re-fetch a taste profile
-- rather than re-asking for it on every query, so it is read on its own.
CREATE INDEX idx_mesh_instance_profile_updated
    ON mesh_instance_profile(updated_at);