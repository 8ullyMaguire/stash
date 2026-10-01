-- Hash-chain the audit log, so "append-only" is a property of the data rather
-- than a promise about the code.
--
-- §5.1 says the owner is not an admin over content. An audit log that a
-- privileged SQL session can rewrite does not support that claim, and the
-- AuditStore's own comment -- "no code path can rewrite it" -- is only as true
-- as the absence of that code path. Anyone with a sqlite file and a query
-- runner has UPDATE.
--
-- Each row stores sha256(prev_hash || canonical row). Changing any field of any
-- row changes that row's hash, which invalidates every hash after it, so a
-- verify walk stops at the first break and names the row. Deleting a row from
-- the middle is detected the same way: the successor no longer chains.
--
-- GENESIS is 64 zeros, so row 1 chains from a known constant and the chain is
-- whole from the first audit row ever written. Existing rows are backfilled in
-- id order, because a chain that starts partway through is a chain with a hole
-- in it that nobody can distinguish from tampering.
--
-- `at` is part of the hashed payload. It is the one field a caller might
-- reasonably want to rewrite (a wrong clock, a timezone fix), and including it
-- is the point: if the timestamp of a moderation action can be edited after
-- the fact, the record is not a record.

ALTER TABLE `collab_audit` ADD COLUMN `prev_hash` BLOB DEFAULT NULL;
ALTER TABLE `collab_audit` ADD COLUMN `row_hash`  BLOB DEFAULT NULL;

-- Verify walks the chain in id order, so this is the access path that matters.
CREATE INDEX `idx_collab_audit_hash` ON `collab_audit` (`id`);
