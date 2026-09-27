-- Proposal votes. StashForge M2.
--
-- One row per (proposal, user). The tally is always Σ over these rows; nothing
-- caches it. Non-negotiable #4, and the reason this table is the ONLY place a
-- score lives.
CREATE TABLE `proposal_votes` (
  `proposal_id` integer NOT NULL REFERENCES `edit_proposals` (`id`) ON DELETE CASCADE,
  `user_id`     integer NOT NULL REFERENCES `users` (`id`) ON DELETE CASCADE,

  -- +1 or -1, nothing else. A vote is an opinion for or against, and a vote
  -- table that accepts 0 or 5 invites "weighted voting", which is how a
  -- reputation system becomes a pay-to-win auction.
  `value`       integer NOT NULL CHECK (`value` IN (-1, 1)),

  `created_at`  datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,

  -- One vote per user per proposal, enforced by the DATABASE rather than by a
  -- read-then-write in the resolver. A resolver-side check has a window in
  -- which two requests from one user both pass it, and the consequence is
  -- exactly the thing MinVoters exists to prevent: one account manufacturing
  -- a quorum.
  --
  -- Re-voting is therefore expressed as an UPDATE of the existing row, not as
  -- a second row. `created_at` is kept on first insert; the resolver sets it to
  -- the vote's original time so the column means what it says.
  PRIMARY KEY (`proposal_id`, `user_id`)
);

-- "Who has not voted on this yet?" — every vote screen needs it, and without
-- this index it is a scan of every vote ever cast.
CREATE INDEX `idx_proposal_votes_proposal` ON `proposal_votes` (`proposal_id`);

-- "How active is this user?" and the per-user rate limits both read this way.
CREATE INDEX `idx_proposal_votes_user` ON `proposal_votes` (`user_id`, `created_at` DESC);
