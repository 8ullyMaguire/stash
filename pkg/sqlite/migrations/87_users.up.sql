-- Users. StashForge M1.
--
-- Upstream has NO user model: auth is a single shared username/password in
-- config (internal/manager/config/config.go:45-46). This table is the
-- foundation for per-user sessions, invite-key registration and the
-- governance model in the spec.
--
-- `is_owner` is a partial-unique-indexed flag rather than a role table: there
-- is exactly one owner, and an owner has NO privilege over shared content
-- (spec §5.1) -- only user management, library paths, instance settings and
-- moderator appointment.
CREATE TABLE `users` (
  `id`            integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  -- NOCASE so "Alice" and "alice" cannot both register; SQLite's default
  -- binary collation would allow it and the login lookup would be ambiguous.
  `username`      varchar(255) NOT NULL UNIQUE COLLATE NOCASE,
  -- argon2id PHC string, prefixed "$argon2id$" so verification is unambiguous
  -- while legacy config.hashPassword values still exist in the wild.
  `password_hash` blob NOT NULL,
  `email`         varchar(255) DEFAULT NULL,
  `created_at`    datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  -- NULL = active. Disabled users keep their rows so their authored content
  -- and audit trail stay intact.
  `disabled_at`   datetime DEFAULT NULL,
  `is_owner`      integer NOT NULL DEFAULT 0 CHECK (`is_owner` IN (0, 1)),
  `reputation`    integer NOT NULL DEFAULT 0
);

-- At most one owner, enforced by the database rather than by application
-- convention. A governance system whose most important invariant is "there is
-- exactly one of these" should not rely on a code path never running twice.
CREATE UNIQUE INDEX `idx_users_single_owner` ON `users` (`is_owner`) WHERE `is_owner` = 1;

-- Case-insensitive login lookup.
CREATE INDEX `idx_users_username_nocase` ON `users` (`username` COLLATE NOCASE);
