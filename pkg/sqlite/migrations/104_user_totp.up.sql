-- Per-user 2FA secrets. StashForge M4, step 4.2.
--
-- ONE COLUMN, ONE ROW PER USER, AND THE VALUE IS A CIPHERTEXT.
--
-- A TOTP secret is a bearer credential: knowing it lets you mint valid codes for
-- the next 90 seconds, forever, with no further proof. A password hash can be
-- stored in the clear because inverting it is slow; a TOTP secret cannot, because
-- the secret IS the key. So any read of this table -- a backup, a replica, a
-- support dump, an injection on a neighbouring table -- is a permanent 2FA bypass
-- for every account in it, and the value is sealed with AES-256-GCM.
--
-- The `v1.` prefix is the format version, so a future change is DETECTED rather
-- than mis-parsed. collab.DecryptTOTPSecret refuses a version it cannot read
-- instead of treating it as plaintext, so an upgrade cannot silently hand every
-- user the wrong secret.
--
-- There is deliberately no `required` column. "Required" is policy (owners must
-- use 2FA) and lives in code, not data; a boolean here would be a second source
-- of truth that could disagree with the policy, and the disagreement would be an
-- authentication bypass.
CREATE TABLE `user_totp` (
  `user_id`  integer NOT NULL PRIMARY KEY REFERENCES `users` (`id`) ON DELETE CASCADE,

  -- The sealed secret, `v1.<base64(nonce+ciphertext)>`. NULL means the user has
  -- not enrolled -- which is DIFFERENT from an empty string, and the store
  -- relies on the difference: an empty string is a corrupt enrolment, and
  -- treating it as "not enrolled" would let a user whose secret was wiped log in
  -- with one factor.
  `secret`   text CHECK (`secret` IS NULL OR length(`secret`) > 0),

  -- The most recent time steps this user has already spent, so a code accepted
  -- once is refused for the rest of its window. A comma-separated list of
  -- integers, empty when none.
  --
  -- WHY A COLUMN AND NOT A TABLE: the set is at most TOTPReplayWindow (3) entries
  -- and is read and written together with every code verification, so a join
  -- would be a second read on the hottest authentication path. The alternative --
  -- an in-process set -- is wrong, because a restart would reopen the replay
  -- window for every user, and a code observed once would become reusable
  -- again.
  `used_steps` text NOT NULL DEFAULT '',

  -- When enrolment completed, so "has this account ever had 2FA" is answerable
  -- separately from "does it have a secret now" -- a user who removed it is
  -- different from one who never set it up.
  `enrolled_at` datetime,

  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- The login path's query: "does this user have a secret, and which steps are
-- spent". Looked up by user id, which is the primary key, so the index is free.
-- This table is small enough that no other index earns its keep.
