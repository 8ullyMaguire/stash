-- Migration 119: discovery boards and their ordered items
--
-- M7 step 7.3c (R015, R016, R017, R049, R067, R068). Spec §6a.4, §6a.7.
--
-- WHY THIS MIGRATION IS MOSTLY A REFUSAL
--
-- internal/discovery has 23 exported symbols and 16 had no non-test reference
-- outside the package -- every one of the 225 lines of feed.go. The package was
-- imported, by internal/acquisition, which uses five OTHER symbols from it. So
-- the cheap "is the package imported" audit that found the collab and directory
-- gaps stopped at the package boundary while the feed sat unreferenced inside it.
--
-- The model is right and worth restating because the schema is shaped by it: a
-- board is an EDITORIAL ORDERING, not a score. internal/discovery says so in the
-- type, and its TestABoardIsAnOrderingWithNoStoredRank asserts the absence of a
-- rank field. Ground rule 4 forbids a stored counter here.
--
-- So THE INTERESTING PART OF THIS MIGRATION IS THE `rank` COLUMN IT DOES NOT HAVE,
-- and the guard test that reads PRAGMA table_info to keep it that way. An absence
-- nobody checks is a convention; an absence a test fails on is a rule.

CREATE TABLE `discovery_boards` (
  -- The domain's own string id, not a surrogate. A board's id is part of its
  -- public shape -- it is what a federated peer resolves -- so minting one here
  -- would mean the id the model has and the id the database has are different
  -- facts that have to be kept in step.
  `board_id` varchar(64) NOT NULL,

  `title` varchar(256) NOT NULL CHECK (length(trim(`title`)) > 0),

  -- §6a.4's curation is editorial, so the board HAS an author and the reasoning is
  -- part of the artifact. NOT NULL and not defaulted: a board with no author is
  -- not a community list, it is an operator's list wearing a community feature's
  -- name, and the two are exactly what the ground rules are for telling apart.
  `author_id` integer NOT NULL CHECK (`author_id` > 0),

  -- The author's own words. §6a.4 says a board with no explanation gives the
  -- reader nothing to judge it by, so this is part of the artifact rather than an
  -- optional extra -- but it MAY be empty, because "the reasoning is part of it"
  -- and "the reasoning may be short" are different claims.
  `description` text,

  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,

  -- NO `rank`, NO `score`, NO `weight`, NO `position`. NOT HERE, and not on a child
  -- table either: the positions ARE the child table's primary key.
  --
  -- WHY THAT IS NOT OBVIOUS ENOUGH TO JUST DO. The obvious persistence for "an
  -- ordering of entities" is `entity_ids JSON` on the row, or a `rank INTEGER`
  -- beside each id. The JSON blob is a rank column with worse ergonomics: no
  -- foreign key per position, no way to ask "which boards contain this studio"
  -- without parsing a string, and a reordering that rewrites the whole row so
  -- concurrent edits to two positions collide. A `rank INTEGER` is worse still,
  -- because it is exactly the stored counter ground rule 4 forbids and
  -- TestABoardIsAnOrderingWithNoStoredRank already asserts is absent.
  --
  -- So the ordering is the PRIMARY KEY of a child table: one row per position, so
  -- the order cannot disagree with itself, there is nothing to reconcile, and the
  -- entity at position 3 is a row a query can find.
  PRIMARY KEY (`board_id`)
);

-- The ordering, as a key.
--
-- PRIMARY KEY (board_id, position) makes a DUPLICATE POSITION unrepresentable --
-- two rows claiming position 2 is a board with a hole and a duplicate, which is
-- not a thing a reader can be shown.
--
-- UNIQUE (board_id, entity_id) makes THE SAME ENTITY TWICE IN ONE BOARD
-- unrepresentable. That is a real defect and a common one: a list showing the same
-- studio at positions 2 and 7 is a bug a reader reports and an author cannot see.
-- The alternative -- allowing it and de-duplicating on read -- means the stored
-- order and the shown order disagree, and the shown order is the one that counts.
CREATE TABLE `discovery_board_items` (
  `board_id` varchar(64) NOT NULL,
  -- 0-BASED, because internal/discovery.ResolveBoard numbers positions from 0
  -- (BoardItem.Position is documented as 0-based, derived from slice order) and a
  -- store that shifted the origin would make every comparison against the domain
  -- off by one.
  `position` integer NOT NULL CHECK (`position` >= 0),
  `entity_id` varchar(64) NOT NULL CHECK (length(trim(`entity_id`)) > 0),

  PRIMARY KEY (`board_id`, `position`),
  UNIQUE (`board_id`, `entity_id`),

  -- A position belongs to a board that exists. ON DELETE CASCADE because a board
  -- with orphaned items is not a board with no items -- it is an unreadable row
  -- that BoardFor would have to decide what to do with.
  FOREIGN KEY (`board_id`) REFERENCES `discovery_boards` (`board_id`) ON DELETE CASCADE
);

-- The read that answers "what is on this board, in order": the PK prefix serves it,
-- so no index is needed for a whole board.
--
-- The SECOND index is the other direction -- "which boards contain this studio" --
-- which is how a viewer finds the lists an entity is on, and which the UNIQUE
-- constraint above does not give for free: an index on (board_id, entity_id)
-- cannot answer a lookup keyed on entity_id.
CREATE INDEX `idx_board_items_entity` ON `discovery_board_items` (`entity_id`);

-- "My lists" for R049. Author rather than board_id, because the author's own view
-- is the common one and board_id would be a uniqueness lookup on a column nobody
-- queries by.
CREATE INDEX `idx_boards_author` ON `discovery_boards` (`author_id`);
