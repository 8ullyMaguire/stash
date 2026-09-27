-- Per-field reputation. StashForge M2b.
--
-- Reputation is agreement with SETTLED OUTCOMES, not vote volume. This table
-- records, per user and per (target_type, field), how often that user was on the
-- winning side of a proposal that was actually accepted.
--
-- The schema already anticipated this and migration 93 says so: "+1 or -1,
-- nothing else ... a vote table that accepts 0 or 5 invites 'weighted voting',
-- which is how a reputation system becomes a pay-to-win auction." The weight in
-- M2b is derived from THIS table, and it is bounded by a cap in
-- internal/collab, so the vote table is still strictly ±1. What M2b adds is not
-- a second ballot column -- it never does -- it is the reputation that ballots
-- are multiplied by, and that reputation is earned from outcomes rather than
-- bought with votes.
--
-- Why per-field rather than per-user. A user who is reliable about performer
-- metadata has not been vetted about anything else, and a single global score
-- would transfer trust between fields that have nothing to do with each other.
-- The whole point of Commons §8.2 is that reputation is a claim about a
-- specific competence, not a general rank.
CREATE TABLE `field_reputation` (
  `user_id`     integer NOT NULL REFERENCES `users` (`id`) ON DELETE CASCADE,
  `target_type` varchar(64) NOT NULL,
  `field`       varchar(64) NOT NULL,

  -- Weighted agreement count. Positive means the user has been on the winning
  -- side of accepted proposals on this field more often than not.
  --
  -- Signed rather than a separate wins/losses pair because decay (M2b §8.2) is
  -- multiplicative and reads more naturally against a signed value, and because
  -- one column cannot disagree with itself.
  `reputation`  integer NOT NULL DEFAULT 0,

  -- How many times this user was on the losing side of an accepted proposal
  -- here. This is the input to DECAY, and it is stored separately from
  -- `reputation` on purpose: decay must slow down, floor at a fraction of full
  -- weight, and never reach zero -- which is a different function of history than
  -- reputation itself. Decay is a judgement about a named user, so it is stored
  -- as a countable fact rather than derived from a score.
  `rejections`  integer NOT NULL DEFAULT 0,

  `updated_at`  datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,

  -- One row per (user, field). A second row for the same pair would mean two
  -- reputations for one competence, and which one a tally used would depend on
  -- the order rows came back in.
  PRIMARY KEY (`user_id`, `target_type`, `field`),

  -- Reputation is not negative. A user whose proposals lose consistently has
  -- DECAY, which is the mechanism the spec gives for exactly that case, and a
  -- negative reputation would be a second, stronger mechanism that silently
  -- removes a person from governance rather than merely discounting them.
  --
  -- "Not yet trusted" and "silenced" must remain distinguishable: the first is
  -- something a user can see and work towards, the second is not.
  CHECK (`reputation` >= 0)
);

-- "How trusted is this user on performer fields?" -- the read the tally runs for
-- every ballot. Covering the whole cross-product, because a tally is a join
-- across many users and many fields.
CREATE INDEX `idx_field_reputation_field` ON `field_reputation` (`target_type`, `field`);

-- "What is this user's standing on their own fields?" -- the profile view, and
-- the decay audit.
CREATE INDEX `idx_field_reputation_user` ON `field_reputation` (`user_id`, `updated_at` DESC);
