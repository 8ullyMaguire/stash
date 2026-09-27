-- Edit proposals. StashForge M2.
--
-- The ONLY write path to shared content. Non-negotiable #5: a user may not edit
-- a shared field directly; they propose an edit, and the edit lands only when
-- governance accepts it. This table is where a proposal lives before that
-- happens.
--
-- `target_type` and `target_id` are deliberately denormalised rather than
-- foreign keys to seven different tables. A real FK would need seven nullable
-- columns or a polymorphic join, and neither buys anything here: the target is
-- resolved at apply time by the vocabulary in internal/collab, which is the
-- component that already has to know which (type, field) pairs are legal.
CREATE TABLE `edit_proposals` (
  `id`          integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  -- scene | performer | studio | tag | gallery | image | group. Constrained by
  -- internal/collab's vocabulary map, not by a CHECK here: the list is code, so
  -- adding a target type does not need a migration, and a CHECK that disagrees
  -- with the map is a second source of truth that will eventually be wrong.
  `target_type` varchar(64) NOT NULL,
  `target_id`   integer NOT NULL,
  -- title | details | director | studio_id | ... from the same vocabulary.
  `field`       varchar(64) NOT NULL,

  -- NULL means "was unset", which is DISTINCT from the empty string. Collapsing
  -- them would make it impossible to propose "clear this field" separately from
  -- "set this field to nothing", and the former is a real edit users ask for.
  `old_value`   text DEFAULT NULL,
  `new_value`   text DEFAULT NULL,

  -- Why the author wants this. Optional in the schema and required in practice
  -- by the UI: a change with no stated reason is much harder for a moderator to
  -- rule on, and a much easier way to launder a vandal's edit.
  `rationale`   text DEFAULT NULL,

  `author_id`   integer NOT NULL REFERENCES `users` (`id`),
  `created_at`  datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,

  -- Terminal states are open|accepted|rejected|superseded|withdrawn. The CHECK
  -- is here because this list is closed and small, and a status outside it is
  -- a bug that must not become a row.
  --
  -- `superseded` and `withdrawn` are separate from `rejected` on purpose:
  -- supersession means a newer proposal replaced this one, and withdrawal means
  -- the author took it back. Neither is a judgement on the author, and neither
  -- may trigger the sticky-rejection rule (spec §5).
  `status`      varchar(16) NOT NULL DEFAULT 'open'
    CHECK (`status` IN ('open','accepted','rejected','superseded','withdrawn')),

  -- A vote tally is NOT stored here. Scores are always computed from
  -- proposal_votes. Non-negotiable #4: a stored counter is a cache that can
  -- disagree with its source, and the disagreement is invisible until it
  -- decides an election. This is the bug stash-box has open as #743/#9.
  --
  -- `decided_at` and `decided_by` are recorded because "when and on whose
  -- authority" is the first question anyone asks after a disputed outcome.
  `decided_at`  datetime DEFAULT NULL,
  `decided_by`  integer DEFAULT NULL REFERENCES `users` (`id`),

  -- A proposal that has already been decided must not be re-decided silently.
  -- Partial unique index: at most ONE open proposal per (target, field), so two
  -- users cannot open competing proposals on the same field and split the votes
  -- between them. Superseding is the sanctioned way to replace one.
  CHECK (`status` <> 'accepted' OR `decided_at` IS NOT NULL)
);

-- "What is open on this target?" — the single most common query, and the one the
-- apply path and the UI both run per target.
CREATE INDEX `idx_edit_proposals_target` ON `edit_proposals` (`target_type`, `target_id`, `status`);

-- "What has this author proposed?" — moderation, rate limiting, and the
-- NewAccountProposalHold count all need it, and created_at DESC because the
-- question is always about the most recent ones.
CREATE INDEX `idx_edit_proposals_author` ON `edit_proposals` (`author_id`, `created_at` DESC);

-- At most one OPEN proposal per (target_type, target_id, field). Partial, so a
-- decided proposal never blocks the next one.
CREATE UNIQUE INDEX `idx_edit_proposals_one_open_per_field`
  ON `edit_proposals` (`target_type`, `target_id`, `field`)
  WHERE `status` = 'open';
