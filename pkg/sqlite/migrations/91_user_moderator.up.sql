-- Moderator flag. StashForge M2, ahead of the governance tables because the
-- User type exposes `isModerator` from the start.
--
-- Additive migration rather than an edit to 87_users: migration 87 has been
-- applied to real databases, and golang-migrate will not re-run an edited file.
--
-- A moderator is appointed BY THE OWNER and is deliberately a separate flag
-- rather than a row in a role table. Two reasons:
--
--  1. There are exactly two privileged roles, and both are constrained. A
--     general role table invites a third role added later with no thought about
--     what it can do, which is the failure mode that turns a governance system
--     into an admin backdoor.
--  2. The owner is NOT a moderator by virtue of being the owner. Non-negotiable
--     #6: the owner manages users and appoints moderators, and has no power to
--     overrule a quorum-accepted edit. Collapsing the two flags would make that
--     invariant impossible to express.
--
-- Checked to 0/1 like is_owner, and deliberately NOT unique: a site may have any
-- number of moderators.
ALTER TABLE `users` ADD COLUMN `is_moderator` integer NOT NULL DEFAULT 0 CHECK (`is_moderator` IN (0, 1));

-- Every moderator lookup is "is this user a moderator?", and moderation views
-- list moderators. A partial index on the flag itself is tiny and serves both.
CREATE INDEX `idx_users_moderators` ON `users` (`is_moderator`) WHERE `is_moderator` = 1;
