-- Migration 118: directory claims and network pricing
--
-- M7 step 7.7a (R045–R048). Spec §6a.4, §6a.15.
--
-- WHY internal/directory WAS 319 LINES AND UNREACHABLE
--
-- The package decides what a PROFILE of an entity may claim and — the load-bearing
-- part of §6a.4 — who may say a claim is true. Its four badge states, its
-- ConfirmedBy-can-never-equal-ClaimedBy rule and its minor-unit prices are all
-- correct, and all thirteen of its exported symbols were imported by nothing.
--
-- So this migration is the missing substrate. The design is not repeated here; it is
-- in internal/directory, and it is worth noticing that its best idea is an ABSENCE.
--
-- WHY claimed_by <> confirmed_by IS A CHECK AND NOT ONLY A GO COMPARISON
--
-- §6a.4 says the claim-and-confirm flow exists because an OPERATOR-GRANTED BADGE IS
-- THE OWNER BEING AN ADMIN OVER CONTENT. That is the whole reason the flow exists,
-- and the reason it belongs in the schema: a Go comparison holds only while every
-- writer goes through the Go code, and the owner of a self-hosted instance has a
-- sqlite file and a query runner. A CHECK holds against that too.
--
-- THE OR (`OR confirmed_by IS NULL`) IS NOT WEAKENING -- it is what makes the rule
-- true. A pending claim has no confirmer, so `confirmed_by` is NULL, and a strict
-- inequality against NULL is NULL, which a CHECK accepts -- meaning the strict form
-- would reject every claim nobody has looked at yet. The rule being enforced is
-- "a claim that HAS a confirmer was not confirmed by its claimer", which is exactly
-- what this says.
CREATE TABLE `directory_claims` (
  -- The entity being claimed, and the pair is the identity: one studio, one claim.
  -- A single `entity_id` column would be meaningless across three entity types, and
  -- a surrogate key here would permit two users to claim the same studio, which
  -- makes "who claimed this" a question with two answers.
  `entity_type` varchar(32) NOT NULL CHECK (`entity_type` IN ('studio', 'performer', 'site')),
  `entity_id`   integer NOT NULL CHECK (`entity_id` > 0),

  -- WHO ASSERTED IT. Not nullable and not defaulted: a claim with no author is not a
  -- claim, and §6a.4's entire concern is that somebody is standing behind it.
  `claimed_by` integer NOT NULL CHECK (`claimed_by` > 0),

  -- FOUR STATES, NOT THREE. `claimed` is not `verified`, and collapsing them is how
  -- an unverified claim becomes a trust signal.
  --
  -- AND THE FOUR ARE ENUMERATED EXACTLY ONCE -- in the four-branch CHECK at the
  -- bottom of this table, not here as well.
  --
  -- This column CHECK WAS THERE FIRST, and the mutation gate proved it REDUNDANT.
  -- Two mutants -- `fifth-state-allowed` and `state-enumerated-twice` -- both
  -- SURVIVED, which took some checking to believe, because the test that inserts
  -- 'confirmed' and requires it to fail was green throughout.
  --
  -- The reason, verified in `sqlite3 :memory:` rather than inferred: the four-branch
  -- CHECK enumerates the same four states as a SIDE EFFECT of tying each one to its
  -- confirmer and decision time, so it refuses 'confirmed' on its own. Widening or
  -- removing this column's CHECK changed nothing observable -- the insert failed
  -- either way, with the error naming the OTHER check.
  --
  -- One rule stated twice is a rule a future change must update twice, and the way
  -- that goes wrong is exactly what this gate exists to catch. So it is stated once,
  -- here, where the comment can explain why there are four of them.
  --
  -- A case variant ('VERIFIED') is refused too, which is worth stating because
  -- SQLite's string CHECKs are case-SENSITIVE -- verified, not assumed. That is the
  -- one place where stating the rule twice bought something, and it is not worth the
  -- cost of a rule that can be updated in one place and not the other.
  `state` varchar(32) NOT NULL DEFAULT 'pending_confirmation'
      CHECK (length(`state`) > 0),

  -- The trusted user who confirmed or rejected it, 0/NULL while undecided.
  `confirmed_by` integer DEFAULT NULL CHECK (`confirmed_by` IS NULL OR `confirmed_by` > 0),

  `decided_at` timestamp,

  -- A rejection is TERMINAL and RECORDED. internal/directory calls a rejected claim
  -- terminal because a rejected claim that could be re-filed on demand is a claim
  -- anybody can spam, so `rejected_at` is set and the row stays rather than being
  -- deleted -- a governance review asks WHO declined, and a hard DELETE answers that
  -- question by destroying it.
  `rejected_at` timestamp,

  PRIMARY KEY (`entity_type`, `entity_id`),

  -- EVERY TABLE-LEVEL CHECK IS HERE, TOGETHER, AFTER EVERY COLUMN. That is a
  -- SQLite grammar requirement and it cost two debugging rounds to find, so it is
  -- recorded in the file rather than left as folklore:
  --
  -- After a table constraint, SQLite's parser expects a COMMA and then a COLUMN
  -- definition. A COMMENT in between is not skipped there -- the parser resumes on
  -- the next non-space token, which is the following column name, and reports
  -- `near "<column>": syntax error` against a column that is entirely valid. The
  -- first version of this migration put this CHECK above `rejected_at` and failed
  -- with `near "rejected_at": syntax error`, having done nothing wrong to the
  -- column. Reproduced directly with `sqlite3 :memory:` to be sure it was the SQL
  -- and not the Go runner.
  --
  -- The same trap applies to a CHECK placed before a column it references, for the
  -- same reason. So: all columns, then all table constraints, then PRIMARY KEY.

  -- §6a.4's whole mechanism, and the reason it belongs in the schema rather than
  -- only in Go: the claimer cannot be the confirmer.
  --
  -- The `OR confirmed_by IS NULL` is NOT WEAKENING -- it is what makes the rule
  -- true. A pending claim has no confirmer, so `confirmed_by` is NULL, and a strict
  -- inequality against NULL evaluates to NULL, which a CHECK accepts; the strict
  -- form would therefore reject every claim nobody has looked at yet. The rule being
  -- enforced is "a claim that HAS a confirmer was not confirmed by its claimer".
  CHECK (`confirmed_by` IS NULL OR `confirmed_by` <> `claimed_by`),

  -- THE FOUR STATES ARE ENUMERATED HERE AND NOWHERE ELSE: `verified`, `rejected`,
  -- `pending_confirmation`, `unclaimed`. Four, not three, because `claimed` is not
  -- `verified` and collapsing them is how an unverified claim becomes a trust
  -- signal; and enumerated rather than free text, because free text would let
  -- `confirmed` and `verified` coexist as two spellings of one idea and leave a
  -- reader guessing which means what.
  --
  -- This is an ENUMERATION by side effect: each branch names its own state, so the
  -- set is closed without a separate IN (...) list. The `state` column's own CHECK
  -- used to enumerate the same four, and the mutation gate showed the duplication
  -- was not free -- see that column's comment.

  -- A VERIFIED claim has a confirmer and a decision time; a REJECTED one has a
  -- confirmer and a rejection time; a PENDING one has neither. Asserted here rather
  -- than trusted to every writer, and this is what makes a rejection TERMINAL in the
  -- data: flipping `state` back to `verified` by hand leaves `rejected_at` set and
  -- `decided_at` set, which this rejects as not a valid `verified` row.
  --
  -- NO granted_by, NO verified_by, NO force COLUMN, AND THAT IS THE POINT.
  -- R048's guarantee is the ABSENCE OF A GRANT PATH. A schema carrying a grant
  -- column reintroduces the path in the one place a future contributor would not
  -- think to look, and "nobody has written the code that uses it" is not a guarantee
  -- a user can check. TestTheSchemaHasNoGrantPath reads PRAGMA table_info and fails
  -- on a column matching that list, because an absence is not something a
  -- behavioural test can check.
  CHECK (
    (`state` = 'verified' AND `confirmed_by` IS NOT NULL AND `decided_at` IS NOT NULL
     AND `rejected_at` IS NULL)
    OR (`state` = 'rejected' AND `confirmed_by` IS NOT NULL AND `rejected_at` IS NOT NULL)
    OR (`state` = 'pending_confirmation' AND `confirmed_by` IS NULL)
    OR (`state` = 'unclaimed')
  )
);

CREATE INDEX `idx_directory_claims_state` ON `directory_claims` (`state`);

-- Network pricing. R045.
--
-- MINOR UNITS, AND THAT IS THE WHOLE DESIGN. internal/directory stores
-- `Amount int` with the comment "4500 is €45.00 and no float is involved". Storing
-- anything else reintroduces the float the domain removed, so there is no REAL
-- column here and the mutation gate's third mutant checks exactly that.
--
-- The key is (entity, unit, currency) because a network may quote several units in
-- several currencies and each is a separate price. `unit` and `currency` are the
-- natural key rather than a surrogate id, so a duplicate quote is not representable
-- -- an UPDATE to the amount is then the only way to change a price, which is what
-- makes a price change an event rather than an append.
CREATE TABLE `directory_pricing` (
  `entity_type` varchar(32) NOT NULL CHECK (`entity_type` IN ('studio', 'performer', 'site')),
  `entity_id`   integer NOT NULL CHECK (`entity_id` > 0),

  -- The unit a price is per, from internal/directory's own four: per_scene,
  -- per_minute, per_day, subscription. A CHECK rather than free text so a price
  -- cannot be quoted "per fortnight" with no way to interpret it.
  `unit` varchar(32) NOT NULL CHECK (`unit` IN ('per_scene', 'per_minute', 'per_day', 'subscription')),

  -- ISO 4217, upper case. No CHECK on the length: the domain owns currency
  -- validation and a CHECK here that disagrees with it is a second rule.
  `currency` varchar(8) NOT NULL,

  -- MINOR UNITS. Integer, NOT NULL, and refused when negative: a negative price is
  -- not a discount, it is a store that will hand out money.
  `amount_minor` integer NOT NULL CHECK (`amount_minor` >= 0),

  -- A price quoted in a currency the site does not accept is a listing the site
  -- does not honour. internal/directory's AcceptedCurrencies says so in a comment;
  -- here it is a boolean the store refuses to write a true price against, because
  -- the alternative is a price a shopper can read and cannot pay.
  `accepted` integer NOT NULL DEFAULT 1 CHECK (`accepted` IN (0, 1)),

  PRIMARY KEY (`entity_type`, `entity_id`, `unit`, `currency`)
);
