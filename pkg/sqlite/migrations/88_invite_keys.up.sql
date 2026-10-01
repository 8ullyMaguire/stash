-- Invite keys, so a public instance is not open to signup floods.
--
-- This is the direct answer to the abuse objection raised on the upstream
-- issue this project came from: registration is invite-only by default, and
-- the owner is not required to approve each one individually.
--
-- Only the HASH of a key is stored. A leaked database must not yield usable
-- invites, for the same reason user_sessions stores session hashes rather than
-- the token itself.
CREATE TABLE `invite_keys` (
  -- SHA-256 of the key. The plaintext exists once, at creation, and is shown
  -- to the creator exactly once.
  `key_hash`    blob NOT NULL PRIMARY KEY,
  `created_by`  integer NOT NULL REFERENCES `users` (`id`),
  `created_at`  datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `expires_at`  datetime DEFAULT NULL,
  -- Bounded by construction: max_uses defaults to 1, so the common case is a
  -- single-use invite and a leaked-and-shared key cannot be reused.
  `max_uses`    integer NOT NULL DEFAULT 1 CHECK (`max_uses` > 0),
  `uses`        integer NOT NULL DEFAULT 0 CHECK (`uses` >= 0),
  `revoked_at`  datetime DEFAULT NULL,
  -- uses can never exceed max_uses. Enforced here so a bug in the redemption
  -- path cannot over-redeem; the read path still checks it, because a CHECK
  -- rejects the write rather than the read.
  CHECK (`uses` <= `max_uses`)
);

CREATE INDEX `idx_invite_keys_created_by` ON `invite_keys` (`created_by`);
CREATE INDEX `idx_invite_keys_active` ON `invite_keys` (`expires_at`, `revoked_at`);
