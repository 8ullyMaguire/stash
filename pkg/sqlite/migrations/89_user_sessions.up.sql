-- Per-user sessions.
--
-- Stash's existing auth (pkg/session) is a single shared credential. This
-- table backs a SessionStore that satisfies the same interface, so the ~20
-- existing call sites are unchanged and a legacy install keeps working.
--
-- The session id is 256 bits of crypto/rand, returned to the client in a
-- cookie and stored here only as a SHA-256 hash: a database leak must not
-- yield usable sessions.
CREATE TABLE `user_sessions` (
  `id`         blob NOT NULL PRIMARY KEY,  -- SHA-256 of the session id
  `user_id`    integer NOT NULL REFERENCES `users` (`id`) ON DELETE CASCADE,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `expires_at` datetime NOT NULL,
  `ip`         varchar(45) DEFAULT NULL,   -- 45 = IPv6 max, with room for a port
  `user_agent` text DEFAULT NULL
);

CREATE INDEX `idx_user_sessions_user` ON `user_sessions` (`user_id`);
-- The purge query is "delete everything already expired", so index expiry
-- rather than user: a per-user index does not help a time-range scan.
CREATE INDEX `idx_user_sessions_expires` ON `user_sessions` (`expires_at`);
