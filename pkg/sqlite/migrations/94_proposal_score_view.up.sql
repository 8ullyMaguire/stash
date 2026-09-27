-- The proposal score view. StashForge M2.
--
-- This VIEW is what "no stored counters" looks like in practice. The tally is
-- computed from proposal_votes on every read, so it cannot drift from its
-- source: there is nothing to keep in sync and nothing to migrate.
--
-- It is a view rather than a helper function because the tally is needed from
-- two very different places — the governance evaluation in internal/collab
-- (which is pure Go and takes the numbers as arguments) and the SQL that
-- presents a proposal to a human — and both must agree. One definition, two
-- readers.
CREATE VIEW `proposal_scores` AS
SELECT
  `p`.`id`                                        AS `proposal_id`,
  -- Σ(+1) − Σ(−1). The whole point of the view.
  COALESCE(SUM(`v`.`value`), 0)                   AS `net`,
  -- COUNT(DISTINCT user_id), NOT COUNT(*). This is the column that makes
  -- MinVoters enforceable, and it is the one most easily written wrong: a
  -- plain COUNT(*) cannot tell "four people agreed" from "one person voted four
  -- times", so a tally that used it would be a number one account can cross.
  --
  -- It is DISTINCT because the composite primary key already prevents one user
  -- having two rows, so COUNT(*) would usually agree — but "usually" is doing
  -- real work in a security-relevant column, and DISTINCT makes the intent
  -- independent of that constraint holding.
  COUNT(DISTINCT `v`.`user_id`)                   AS `voters`,
  -- True when the author is among the voters. Carried here so Evaluate's
  -- SelfVote input has one source rather than a second query that might
  -- disagree.
  MAX(CASE WHEN `v`.`user_id` = `p`.`author_id` THEN 1 ELSE 0 END) AS `author_voted`
FROM `edit_proposals` `p`
LEFT JOIN `proposal_votes` `v` ON `v`.`proposal_id` = `p`.`id`
GROUP BY `p`.`id`, `p`.`author_id`;
