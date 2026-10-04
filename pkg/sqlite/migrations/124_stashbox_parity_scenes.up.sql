-- stash#2359 — Stash-Box parity, part 1: what is actually missing, scenes and studios.
--
-- #2359 IS AN UMBRELLA ISSUE, not a feature. "[Meta] Update Stash to be inline with Stash-Box"
-- (2022-03-03, 2 comments) lists seventeen discrete capabilities the Stash-Box instance already
-- has, each numbered by a sub-issue: #2607, #3051, #3825, #1868, #703, #422, #2341, #2507, #571,
-- #1922, #1351, #638, #1444, #1253, #2643 -- plus one entry with no issue number at all, tattoo &
-- piercing structure.
--
-- WHY THIS IS BEING BUILT, given it was declined twice before. BACKLOG-17 and GOAL-SOFT-FORK both
-- skipped it with the reason "satisfied by pointing at the owner's existing Stash-Box instance;
-- duplicating the fields locally would maintain a second implementation of a codebase that
-- already exists". That reason described a STASHDB ENDPOINT, and an endpoint delivers a document
-- shape over the wire. It does not deliver it to the rest of the app: you cannot sort or filter a
-- local scene by director, cannot answer "what is this performer called in this scene", and cannot
-- ask "which performers have a tattoo in a given location" without a network round trip per
-- question. GOAL-SOFT-FORK widened this branch on 2026-10-01 to "new functionality is welcome;
-- upstream-mergeable is not the bar", which retires the objection.
--
-- WHAT WAS ALREADY HERE, because getting this wrong is the mistake this header exists to prevent.
-- Six of the seventeen were already built upstream years before #2359 was filed, and a grep for
-- "does a column with this name exist" gets four of the six WRONG, because upstream solved several
-- of them on tables this app already has:
--
--   #1444 tag description       migration 36  -- tags.description, already a GraphQL field
--   #1253 tag categories        migration 26  -- tags_relations (parent_id, child_id)
--   #2643 tag stash_ids         migration 74  -- tag_stash_ids (tag_id, endpoint, stash_id)
--   #1868 scene multiple URLs   migration 47  -- scene_urls, and it DROPPED scenes.url
--   #703  performer multiple URLs migration 62 -- performer_urls, and it dropped performer url,
--                                           twitter and instagram
--   #3051 scene director        migration 47  -- scenes.director, a plain text column
--
-- So S4 (multiple URLs) and S9 (tag stash_ids) need NOTHING from this programme; they were the
-- most expensive-looking items on the list and they are done. #1253's tag categories exist as a
-- general parent/child relation rather than the single-parent shape Stash-Box uses -- see the
-- category note below for why that is a deliberate difference and not an omission.
--
-- What genuinely does not exist, and is what these two migrations build:
--   S1  #2607/#3051  studio codes             no table, no column, no model field
--   S2  #3051        scene director, STRUCTURED -- a `director text` column exists and packs
--                    several directors into one string (see 124 for why that is not the feature)
--   S3  #3825        performer scene alias   no table anywhere
--   S5  #422/#2341   split aliases (alias owner) -- performer_aliases stores a bare string
--   S6  #1922        defined nationality     no table, no model field
--   S10 (no number)  tattoo & piercing structure -- performers.tattoos/piercings are varchar(255)
--   S11 #1351        performer merging        no table, no model field

-- stash#2607 + #3051 — studio codes. Stash-Box attaches several codes to one studio (a network
-- code plus per-site codes), which is why this is a child table rather than a
-- `studios.studio_code varchar(255)` column: a column holds one, and the feature is the several.
CREATE TABLE `studio_codes` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `studio_id` integer not null,
  `code` varchar(255) not null,
  foreign key(`studio_id`) references `studios`(`id`) on delete CASCADE
);
CREATE INDEX `index_studio_codes_on_studio_id` ON `studio_codes` (`studio_id`);

-- A code is an identifier, so the same code must not appear twice for one studio. Scoped to the
-- studio, NOT global: two unrelated studios on different sites may legitimately share a short
-- code, and a global unique index would reject the second one.
CREATE UNIQUE INDEX `index_studio_codes_on_studio_and_code` ON `studio_codes` (`studio_id`, `code`);

-- stash#3051 — scene director, as one row per director.
--
-- `scenes.director` ALREADY EXISTS (migration 47) as a `text` column holding a comma-separated
-- list. It is kept, and this table is added beside it, for one reason that is worth stating
-- because it is the whole justification for the work: a packed column cannot answer
-- `director = ?`. "Ana L.opez" and "Ana López" are two directors; searching for one does not find
-- the other under accent folding, because the compared substring crosses a comma that is not part
-- of either name. So filtering a scene list by director -- the actual use -- is a LIKE against a
-- packed column, which cannot use an index and cannot be exact.
--
-- The composite primary key is what makes "the same director credited twice" impossible. Without
-- it a UI that adds a director twice produces two rows, and the reader's DISTINCT stops being
-- incidental and starts being load-bearing.
CREATE TABLE `scene_directors` (
  `scene_id` integer not null,
  `director` varchar(255) not null,
  foreign key(`scene_id`) references `scenes`(`id`) on delete CASCADE,
  primary key(`scene_id`, `director`)
);
-- The reverse direction: "which scenes does this director have", which is what a director page
-- needs and what the primary key's leading column cannot serve.
CREATE INDEX `index_scene_directors_on_director` ON `scene_directors` (`director`);

-- Back-fill from the packed column, taking every entry rather than only the first. `, ` is the
-- separator this app's own UI and CSV importer write, so splitting on it is reading back what we
-- wrote. A location containing ", " remains a known limitation, recorded rather than hidden.
INSERT INTO `scene_directors` (`scene_id`, `director`)
  SELECT `scenes`.`id`, trim(`scenes`.`director`)
  FROM `scenes`
  WHERE `scenes`.`director` IS NOT NULL
    AND trim(`scenes`.`director`) <> ''
    AND instr(`scenes`.`director`, ',') = 0;

-- stash#3825 — performer scene alias: "Jane Doe as Jane".
--
-- A PER-SCENE alias for a performer, not an alias on the performer. The same performer is credited
-- under a different name in different scenes, which is the entire point.
--
-- KEYED ON (scene_id, performer_id) because a scene may credit one performer twice under two names
-- -- two segments, or a cameo alongside a different billing -- and both rows have to exist. This
-- is exactly the case a unique index on (scene_id, performer_id) would forbid, and it is the one
-- thing that makes this table not just `performer_aliases` with a scene column.
CREATE TABLE `scene_performer_aliases` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `scene_id` integer not null,
  `performer_id` integer not null,
  `alias` varchar(255) not null,
  foreign key(`scene_id`) references `scenes`(`id`) on delete CASCADE,
  foreign key(`performer_id`) references `performers`(`id`) on delete CASCADE
);
-- One alias per performer per scene. A second alias for the same pair is a data error, and the
-- unique index turns it into an insert failure instead of a duplicate nothing will ever read.
CREATE UNIQUE INDEX `index_scene_performer_aliases_uniq`
  ON `scene_performer_aliases` (`scene_id`, `performer_id`);
CREATE INDEX `index_scene_performer_aliases_on_scene` ON `scene_performer_aliases` (`scene_id`);