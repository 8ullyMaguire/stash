-- stash#2359 follow-up: the STUDIO half of #422.
--
-- WHAT #422 ACTUALLY ASKS FOR, IN ITS OWN WORDS
-- =============================================
--
-- The issue proposes:
--
--   { "name": "Jane Doe",
--     "aliases": { "Jane": "Brazzers", "Jane Doe": "Naughty America",
--                  "Jayne": "21 Naturals", "Janey": "" } }
--
-- i.e. an alias maps to a STUDIO, and "" means "not associated with any studio or website". The
-- suggested table in the thread is `performers_aliases(alias_name, performer_id, studio_id)`.
--
-- Migration 125 built `performer_alias_owners(performer_id, alias, owner_performer_id)`. That column
-- answers #2341 -- which performer an alias string is ATTRIBUTED to, so two performers called "JD"
-- can each own one -- and NOT #422, which asks which STUDIO an alias belongs to. Those are different
-- questions and the distinction is the whole content of this migration: the same alias is common
-- across studios, and "JD" at Brazzers need not be "JD" at DDF Network.
--
-- WHY A COLUMN AND NOT A SECOND TABLE
-- ===================================
--
-- An alias row is identified by (performer, alias). Adding studio_id to THAT row keeps one row per
-- alias, so the alias list stays a list of strings -- which is what `Performer.Aliases`
-- (`RelatedStrings`) already is, and what every existing reader and writer assumes. A parallel
-- `performer_alias_studios` table would make "this performer's aliases" a join whose result changes
-- shape, and would give no way to say "this alias has NO studio", which upstream's "" requires.
--
-- NULLABLE, AND SET NULL
-- =====================
--
-- NULL is upstream's empty string: an alias with no studio association is the COMMON case, and it
-- must round-trip as NULL rather than as a sentinel. ON DELETE SET NULL matches
-- 48_cleanup.up.sql and 59_movie_urls.up.sql, and it is the correct behaviour for a studio being
-- removed: the alias is still a true alias of that performer, it has simply lost the studio it was
-- associated with. Cascading would delete a real alias because an unrelated studio row went away.

ALTER TABLE `performer_alias_owners` ADD COLUMN `studio_id` integer REFERENCES `studios`(`id`) ON DELETE SET NULL;

CREATE INDEX `index_performer_alias_owners_on_studio`
  ON `performer_alias_owners` (`studio_id`);

-- The EXISTING unique index is (performer_id, alias). It stays, and that is a decision.
-- ------------------------------------------------------------------------------
--
-- Two rows differing only in studio are now a question the issue actually raises:
--
--   "One performer can have two (or more) aliases for the same studio"  -- fine, different aliases.
--   "Two (or more) performers can have the same alias with a given studio" -- fine, different
--     performers, and #2341's whole purpose is to let that be recorded independently.
--
-- Neither requires the SAME (performer, alias) to appear twice with different studios. The issue's
-- own author says "including duplicate aliases per studio" is a data-entry reality people will
-- create, and the honest handling of a duplicate is to reject it rather than silently keep two
-- identical (performer, alias) rows whose studio is then ambiguous -- "who owns this alias" would
-- return a list, which is the defect #2341 was filed about.
--
-- So the unique index is LEFT UNCHANGED and studio_id is not part of it. Adding studio_id would
-- permit the ambiguous duplicate and trade a clear rejection for a silent one; and it would not
-- change any existing row, since every current row has studio_id NULL.
--
-- SQLite cannot ALTER a UNIQUE INDEX, and this does not need to: the constraint that matters --
-- one row per (performer, alias) -- is already enforced.

-- The precise auto-tagging lookup #422 describes: "matches that contain both the alias and studio in
-- the filename". Without a studio on the row this is unindexable, because every query would have to
-- join studios on name. With it, the join is on an integer id.
--
-- Composite rather than single-column: a lookup always filters BOTH, and a composite index serves
-- that and the reverse (all aliases at a studio) from one structure.
CREATE INDEX `index_performer_alias_owners_on_studio_alias`
  ON `performer_alias_owners` (`studio_id`, `alias`);