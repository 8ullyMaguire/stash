-- Append-only audit of every governance-relevant action.
--
-- Lands in M1 rather than M2 because it must record ACCOUNT actions from the
-- first user: registration, login, failed-login lockouts, invite redemption,
-- admin grants. An audit trail that starts when the feature starts is not an
-- audit trail.
--
-- No UPDATE or DELETE path is ever written for this table. That is the point:
-- a moderator who can edit history is not accountable to anyone.
CREATE TABLE `collab_audit` (
  `id`          integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `at`          datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  -- NULL for actions by an unauthenticated actor (a failed login, or a
  -- registration attempt rejected by a bad invite key). The row still
  -- matters: those are the rows that show a brute-force or a signup flood.
  `actor_id`    integer DEFAULT NULL REFERENCES `users` (`id`),
  `action`      varchar(64) NOT NULL,
  `target_type` varchar(32) DEFAULT NULL,
  `target_id`   integer DEFAULT NULL,
  `field`       varchar(64) DEFAULT NULL,
  -- JSON detail. Never a password, never a session id, never an invite key:
  -- a value written here is readable by any moderator forever.
  `detail`      text DEFAULT NULL
);

CREATE INDEX `idx_collab_audit_at` ON `collab_audit` (`at` DESC);
CREATE INDEX `idx_collab_audit_actor` ON `collab_audit` (`actor_id`, `at` DESC);
CREATE INDEX `idx_collab_audit_target` ON `collab_audit` (`target_type`, `target_id`);
