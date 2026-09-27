-- Libraries. StashForge M3, step 3.1.
--
-- The spec's §6.2 and §6.4 both lean on a library boundary that no migration
-- ever created: "never published ... any private library's rows", "served only
-- to users holding a `user_library_access` row". Until this file, neither
-- `libraries` nor `user_library_access` existed, so "a private library" was a
-- word in a comment and nothing enforced it. M3's exit criterion is that an
-- opted-out library publishes nothing, and that criterion cannot be true without
-- something for a library to be.
--
-- WHY A LIBRARY AND NOT A BOOLEAN ON EACH ROW
--
-- A `is_private` column on every target type is the obvious cheaper design and
-- it is wrong here. It spreads one policy decision across seven tables, so
-- "which rows may leave this instance" becomes seven separate queries that must
-- agree, and the day one of them is forgotten the leak is silent. A library is
-- the unit of sharing: the exporter asks which libraries are in scope once, and
-- every target row hangs off a library. One boundary, checked in one place.
--
-- So this is a *thin* table on purpose. It does not describe a collection of
-- files -- stash already has paths for that, and duplicating a file tree here
-- would be a second source of truth about storage. A library here is a sharing
-- scope: a set of target rows that share one consent decision.

CREATE TABLE `libraries` (
  `id`          integer NOT NULL PRIMARY KEY AUTOINCREMENT,

  -- The owner. NOT NULL and a real user: every library belongs to someone, and
  -- a library with no owner is a library whose consent can never be read.
  `user_id`     integer NOT NULL REFERENCES `users` (`id`) ON DELETE CASCADE,

  -- UNIQUE per (owner, name) and not globally. Two users may each own a
  -- library called "Main" without colliding, and a name is a label rather than
  -- an identifier, so uniqueness is scoped to where the label is shown.
  `name`        text NOT NULL CHECK (length(trim(`name`)) > 0),

  -- Whether this library's metadata may be published. This is the column the
  -- exporter reads, and it is NOT the same thing as consent: consent is a
  -- *user's* decision about sharing in general, this is a per-library default
  -- for a user who has not expressed one. NULL means "defer to the owner's
  -- consent row" -- a third state, deliberately, because a new library must be
  -- excluded from nothing silently: it inherits the owner's decision rather
  -- than asserting its own. See ShareOptedIn for the resolution order.
  `is_private`  integer CHECK (`is_private` IN (0, 1)) DEFAULT NULL,

  `created_at`  datetime NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- "Every library belonging to this user", which is what the consent screen and
-- the exporter's scope resolution both ask. The owner is the leading column
-- because it is the only equality in the query.
CREATE INDEX `idx_libraries_user` ON `libraries` (`user_id`, `id`);

-- Every library that is explicitly private, for the review queue and for the
-- "is anything actually private here" diagnostic. Partial because a library
-- that is private is the minority, and NULL (defer) is the common case for a
-- library nobody has decided about -- which is precisely the set that must not
-- be silently treated as public.
CREATE INDEX `idx_libraries_private` ON `libraries` (`created_at`, `id`)
  WHERE `is_private` = 1;

-- Grant table for §6.4's two-separate-grants rule: metadata may be published to
-- the commons, media is served only to a user holding a row here.
--
-- The absence of a row is the security-relevant case, so it is the one the
-- design has to get right: an ungranted user gets 404, not 403, because the
-- existence of the file is itself not disclosed (§6.4). There is deliberately
-- no "default allow" and no wildcard row -- a grant names a specific library and
-- a specific user, so adding access is a deliberate, auditable insert.
--
-- Read-side note: the common question is "may THIS user reach THIS library",
-- which is why (user_id, library_id) is the primary key order and not
-- (library_id, user_id) -- the serving path has a user in hand first.
CREATE TABLE `user_library_access` (
  `user_id`     integer NOT NULL REFERENCES `users` (`id`) ON DELETE CASCADE,
  `library_id`  integer NOT NULL REFERENCES `libraries` (`id`) ON DELETE CASCADE,

  -- When the grant was made, so "who could see this, and since when" is
  -- answerable after the fact. Not nullable and not defaulted by the database:
  -- the store writes it explicitly, because a grant whose date is an artefact
  -- of when the migration ran is not a record of anything.
  `granted_at`  datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,

  -- One grant per (user, library): re-granting is not a new row, it is the same
  -- grant. An upsert rather than an error, so a repeated grant is idempotent
  -- and a second row cannot make "is this user granted?" ambiguous.
  PRIMARY KEY (`user_id`, `library_id`)
);

-- "Everyone with access to this library" -- the revocation path, and the
-- question the 404-not-403 rule asks. A grant for a user who no longer exists
-- is removed by the CASCADE above, so this index never returns a dangling id.
CREATE INDEX `idx_library_access_library` ON `user_library_access` (`library_id`, `user_id`);
