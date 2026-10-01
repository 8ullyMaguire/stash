-- Shadow governance log: what the weighted rule WOULD have decided, beside what
-- flat quorum actually decided.
--
-- §5.3 leaves the weight table open — per field type or a single weight — and
-- does not answer it. An instance that guesses adopts a governance model nobody
-- voted for. So this table records both decisions while flat quorum stays in
-- force, and switching over later is a config change rather than a rewrite.
--
-- Non-negotiable #4 applies here too, which is the reason there is no
-- `disagreements` counter and no cached rate: every figure an operator reads is
-- computed from these rows by SQL. A counter here would be a second source of
-- truth, and the one number operators would trust to decide a governance
-- switch is exactly the number that must not be able to drift.
--
-- Recorded for EVERY evaluation, not only for disagreements. A log of only the
-- disagreements cannot answer "how often would this have changed things", since
-- that question needs the denominator.

CREATE TABLE `governance_shadow_log` (
  `id`          integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `proposal_id` integer NOT NULL REFERENCES `edit_proposals` (`id`) ON DELETE CASCADE,

  `target_type` varchar(64) NOT NULL,
  `field`       varchar(64) NOT NULL,

  -- The decision that was APPLIED. Not an abstraction of it: this is what took
  -- effect, so a later switch-over can be checked against what really happened.
  `flat_decision`      varchar(16) NOT NULL,
  `weighted_decision`  varchar(16) NOT NULL,

  -- The weighted function's explanation, carried through so an operator reading
  -- the log does not have to re-derive the tally to understand the row.
  `reason` text,

  -- The two tallies. Kept because "they agreed" and "they agreed by luck, with a
  -- weighted total of 2 against a threshold of 30000" are different findings,
  -- and only the numbers tell them apart.
  `weighted_total` integer NOT NULL DEFAULT 0,
  `net`            integer NOT NULL DEFAULT 0,

  -- Distinct voters and ballot rows. These differ when a re-vote updated a row,
  -- and a disagreement caused by a thin ballot is a different finding from one
  -- caused by a broad one.
  `voted`   integer NOT NULL DEFAULT 0,
  `ballots` integer NOT NULL DEFAULT 0,

  -- How many ballots the Sybil detector marked as coordinated. Carried because
  -- "they disagreed AND three ballots looked coordinated" is the most
  -- interesting row in the table, and it is only identifiable if the count is
  -- stored rather than recomputed from ballots nobody can see any more.
  `flagged` integer NOT NULL DEFAULT 0,

  `at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- The moderation view is "disagreements, newest first", and since most
-- evaluations agree, the disagreeing rows are a small fraction of the table.
-- An index on (flat_decision, weighted_decision) cannot express "these differ",
-- so the useful key is the ordered scan the view actually performs.
CREATE INDEX `idx_governance_shadow_proposal` ON `governance_shadow_log` (`proposal_id`);

-- Every record for one proposal, in the order the votes arrived. A steward
-- asking "how did this proposal's tally move?" reads it this way.
CREATE INDEX `idx_governance_shadow_at` ON `governance_shadow_log` (`at` DESC);

-- The summary query groups by the pair to split disagreements by direction. A
-- composite index lets that grouping read an index rather than the table, and
-- the pair is small enough that the index stays compact.
CREATE INDEX `idx_governance_shadow_decisions`
  ON `governance_shadow_log` (`flat_decision`, `weighted_decision`);