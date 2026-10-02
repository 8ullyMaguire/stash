-- Migration 120: the issues log — stash#837
--
-- "Log potential issues with files, show in a dedicated UI."
--
-- WHY THE ISSUE IS ABOUT A TABLE AND NOT A LOG
--
-- The obvious implementation is a log line: the scanner finds something wrong
-- and appends to the existing logger. That is a log, and a log cannot be a UI.
-- The user asked for a panel they can open and look at, and a panel over a log
-- is a log viewer -- the thing they are complaining about. So the findings are
-- rows, and the row below is the product.
--
-- WHY THIS MIGRATION IS MOSTLY A CONSTRAINT
--
-- The columns are unremarkable. The one line that carries the design is the
-- unique index, and the reasoning is the whole of the feature:
--
--   An issue is a FACT ABOUT THE LIBRARY, not an event that happened.
--
-- A duplicate file is a duplicate on every scan. Without a uniqueness rule the
-- same finding appends a row each pass, the table becomes an append-only log
-- with a timestamp column, and after a week of scanning the panel is 4000
-- identical rows the user scrolls past once and never opens again. So:
--
--   UNIQUE (file_id, domain, kind, resolved)
--
-- `resolved` is in the key DELIBERATELY, which is the part that is easy to get
-- wrong in the intuitive direction. A naive UNIQUE (file_id, domain, kind) says
-- a dismissed finding can never be recorded again -- so a file the user dismissed
-- last month, deleted, and re-imported broken a *different* way, can never be
-- flagged at all. Putting `resolved` in the key means:
--
--   - the identical finding, still live       -> conflicts, updates in place
--   - the identical finding, already dismissed -> conflicts, STAYS dismissed
--   - a genuinely new finding of the same kind -> a new row, because `details`
--                                               differs and is not in the key
--
-- The third case is the one the spec (§3) promises and the one a reviewer should
-- check first, because it is the case that makes "dismissed" mean "dismissed"
-- rather than "ignored forever".
--
-- file_id IS NULLABLE because an issue can be about the library rather than a
-- file -- a scan that added nothing, for instance (spec §4, domain `scan`). SQL
-- unique indexes treat NULLs as distinct, so NULL file_ids do not collide; that
-- is the behaviour we want, since two "this scan found nothing" rows for the
-- same scan would be noise, but two for DIFFERENT scans are not.
--
-- `resolved_at` is NULL until dismissal, and is NOT updated on a second
-- dismissal: the panel shows it as "dismissed on", and rewriting it every time
-- somebody re-clicks makes that date drift. The store's UPDATE is guarded with
-- `AND resolved = false` to make that hold, and TestResolveIsIdempotent is the
-- test that fails if the guard is dropped.

CREATE TABLE `issues` (
  `id` integer PRIMARY KEY AUTOINCREMENT,

  -- NULLABLE, and deliberately so: `domain = 'scan'` findings are about the scan,
  -- not a file. ON DELETE CASCADE because an issue about a file that no longer
  -- exists is not an issue any more, and a dangling row renders as an empty
  -- filename in the panel -- a row the user cannot act on and cannot explain.
  `file_id` integer REFERENCES `files` (`id`) ON DELETE CASCADE,

  -- Which subsystem raised it, and what specifically it found. `details` is
  -- human-facing prose that NOTHING parses, so the two columns above are the
  -- whole of the machine-readable contract; anything a caller needs to filter on
  -- has to be a column, or it is a string match waiting to rot.
  `domain` text NOT NULL CHECK (`domain` IN ('file', 'scan', 'metadata')),
  `kind` text NOT NULL CHECK (length(trim(`kind`)) > 0),

  `details` text NOT NULL DEFAULT '',

  `detected_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,

  `resolved` boolean NOT NULL DEFAULT false,
  `resolved_at` timestamp
);

-- THE INDEX THAT IS THE DESIGN, and it is an EXPRESSION index rather than a plain
-- one because a plain `UNIQUE (file_id, domain, kind, resolved)` cannot express what
-- the spec promises.
--
-- What it has to do, all three at once:
--   (1) the identical LIVE finding twice            -> refuse (one fact, one row)
--   (2) the identical finding, already DISMISSED    -> refuse (a dismissal sticks)
--   (3) a NEW finding of the same kind, new details -> ALLOW (dismissal is not silence)
--
-- A plain unique on (file_id, domain, kind, resolved) does (1) and (2) and BREAKS (3):
-- two dismissed rows of the same kind collide, because `details` is not in the key. So
-- the index is over a computed identity instead:
--
--     (file_id, domain, kind, CASE WHEN resolved THEN NULL ELSE 0 END)
--
-- A live row's key is (file, domain, kind, 0) -- so a second live row of the same
-- finding collides, and a dismissed row CANNOT collide with it.
-- A dismissed row's key ends in NULL, and SQLite treats NULLs in a unique index as
-- DISTINCT, so any number of dismissals coexist. That is what makes (2) and (3)
-- hold at once: the dismissal is remembered per finding, and a different finding of
-- the same kind is still recordable.
--
-- WHAT THIS INDEX DOES NOT DO, and why, because the first attempt tried to make it do
-- three things at once and could not.
--
--   (1) the identical LIVE finding twice            -> REFUSED, here
--   (2) the identical finding, already DISMISSED    -> refused...
--   (3) a NEW finding of the same kind, new details -> ALLOWED, here
--
-- (2) and (3) are contradictory for a unique index. Refusing duplicate DISMISSED rows
-- means any two dismissed rows of the same kind collide; allowing a new finding of the
-- same kind means any two dismissed rows of that kind must not. There is no column
-- list that separates them, because the only thing distinguishing a repeat from a new
-- finding is `details` -- and putting `details` in the key makes a live duplicate
-- differing only in prose insertable, which breaks (1).
--
-- So the index keeps the two INVARIANTS, which are the ones a race can violate, and
-- the store keeps the POLICY:
--
--   index  -> (1) and (3). A live finding is unique, so two scans racing cannot both
--             insert, and a new finding of a dismissed kind is still recordable.
--   store  -> (2). Before inserting, Record looks for an existing DISMISSED row with the
--             same (file_id, domain, kind) and skips. This is a policy -- "do not
--             re-raise what the user dismissed" -- rather than an invariant, and
--             policies belong in code where a test can assert them and a change can be
--             argued about. TestDismissedIssueIsNotReraised is that test.
--
-- The consequence, stated plainly: two scans racing CAN both insert a live duplicate
-- of the same finding, because the index only refuses a THIRD row. That is acceptable
-- because a scan is single-writer per file and the panel is a place a human resolves
-- duplicates, not a ledger an accountant reconciles. It is noted here because the
-- alternative -- a read-then-write check in Record -- reintroduces the race the index
-- was added to remove.
--
-- `Record` inserts and lets the index refuse, then updates `detected_at` on conflict,
-- so the common path needs no read at all.
CREATE UNIQUE INDEX `idx_issues_unique`
  ON `issues` (`file_id`, `domain`, `kind`,
               CASE WHEN `resolved` THEN NULL ELSE 0 END);

-- The panel's default query: unresolved, newest first. An index on `resolved`
-- alone cannot also order by `detected_at`, and the composite one serves the
-- read that actually happens on every page load.
CREATE INDEX `idx_issues_unresolved`
  ON `issues` (`resolved`, `detected_at` DESC);

-- Finding "what is wrong with THIS file" is the other direction, and neither
-- index above can serve it: both lead with `resolved`, so a lookup keyed on
-- file_id is a scan.
CREATE INDEX `idx_issues_file`
  ON `issues` (`file_id`);
