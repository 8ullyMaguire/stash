-- Migration 117: access policy and content consent
--
-- M7 step 7.2 (R025–R028, R062, R066). Spec §6a.10–§6a.12.
--
-- WHY THIS TABLE EXISTS WHEN internal/collab/access_level.go ALREADY DOES
--
-- The domain model is complete and its 9 tests pass; what it had was no
-- reachability. No table, no store, no caller outside its own package, so a
-- correct model sat at `specified` for a full milestone while looking finished
-- in every test. This migration is the missing substrate, not new thinking.
--
-- WHY TWO TABLES AND NOT ONE
--
-- `DecideAccess` takes three inputs and two of them belong to DIFFERENT PARTIES:
-- the ceiling is the operator's, the consent is the user's. One table with
-- (content_ceiling, granted) columns would let a single UPDATE express "the
-- operator raised the ceiling and the user consented" as one atomic act, and
-- nothing in the schema would record that only one of the two happened. The two
-- facts change for different reasons, are revocable by different parties, and
-- have to be auditable separately — so they are two tables.
--
-- WHY THE CEILING DEFAULTS TO 0 (LevelPublic)
--
-- `content_ceiling` is the operator's threshold, and §6a.12 says a ceiling is a
-- ceiling: it never grants the operator anything the threshold excludes. The
-- safe reading of "an operator has not configured this" is therefore "this
-- instance does not serve content", not "serve it to everyone".
--
-- A default of 5 would make an instance that never touched a setting a content-
-- serving instance, which is a decision nobody made. Migration 116 set the
-- opposite default for the opposite reason and both are load-bearing: curation
-- defaults to 'propose' because a safe feature must be reachable without
-- configuration, while a consent-gated capability defaults to OFF because the
-- whole two-number design exists so that reaching a level never enables viewing
-- by itself. The difference is whether the default grants someone something.
--
-- WHY content_ceiling IS SPECIFICALLY ABOUT CONTENT
--
-- It is not the instance's general policy level. §6a.12's rule only has teeth
-- where there is a threshold to exclude, and the capability being withheld here
-- is specifically viewing content — metadata, collages and the directory are
-- LevelPublic and stay available at any ceiling. Widening this later to cap
-- everything would turn "this instance serves no video" into "this instance
-- serves nothing", which is a different policy and needs its own column.
--
-- WHY granted DEFAULTS TO 0 AND THAT IS THE LOAD-BEARING PART
--
-- §6a.11: consent is a separate switch and is NEVER automatic on reaching a
-- level. `granted = 1` by default would make every user who earned Archivist a
-- content viewer at the moment of earning, which is precisely the conflation the
-- two-number design (earned vs offered vs consented) exists to prevent. The
-- zero value of the Go bool and the default of the column are the same answer,
-- so a read that forgets to check the row still gets the safe one.
CREATE TABLE `access_policy` (
  -- PRIMARY KEY and not an autoincrement id, mirroring migration 102's "the row
  -- IS the instance" reasoning: exactly one ceiling per instance, and a second
  -- row is not a state any reader should have to interpret.
  `instance_id` integer NOT NULL PRIMARY KEY CHECK (`instance_id` > 0),

  -- 0..5, and 5 is the ceiling there is (no level 6 -- see collab.MaxAccessLevel).
  -- The domain's Clamp() folds an out-of-range operator input, but a value that
  -- can be STORED would be a second source of truth, so the CHECK is what makes
  -- the column and the type agree.
  `content_ceiling` integer NOT NULL DEFAULT 0
      CHECK (`content_ceiling` >= 0 AND `content_ceiling` <= 5),

  -- No `set_by`: the operator IS this instance, so a column naming who set it
  -- would be a second identity for something the schema already knows.
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Seeded, not created empty, so the default above is a value this file states
-- rather than something a reader infers from an absent row. An absent row and a
-- row saying 0 mean the same thing here, and stating it means a caller that
-- forgets to check for absence still reads the safe value.
INSERT INTO `access_policy` (`instance_id`, `content_ceiling`) VALUES (1, 0);

-- WHY CONSENT IS A SEPARATE TABLE FROM consent_preferences
--
-- Migration 102's consent_preferences answers "may this user's METADATA be
-- published". This table answers "may this user VIEW CONTENT". They are
-- different questions with different defaults, different revocation semantics
-- and different failure consequences, and §6a.11's whole point is that the
-- second is a separate switch. Reusing the first would make one row answer both,
-- and an operator who disabled metadata publication would silently also disable
-- content consent (or worse, the reverse).
--
-- WHY PRIMARY KEY (user_id) ALONE
--
-- Same reasoning as consent_preferences: the row IS the user's answer, so
-- exactly one answer per user is representable and "opted in or out" cannot have
-- two answers. `instance_id` is carried rather than left implicit because this
-- table is per-instance by construction (§6a.11 says consent is revocable PER
-- INSTANCE) and an unnamed instance column would invite a future multi-instance
-- read that silently answers about the wrong one.
--
-- WHY REVOCATION IS RECORDED, NOT DELETED
--
-- §6a.11 requires consent to be revocable, and a hard DELETE satisfies that
-- requirement while destroying the question a governance review actually asks:
-- "when did this user withdraw?". So `revoked_at` is set and the row stays. The
-- CHECK keeps the two columns honest -- a row may not claim consent and
-- revocation simultaneously, which is otherwise a state every caller would have
-- to resolve for itself.
CREATE TABLE `content_consent` (
  `user_id` integer NOT NULL PRIMARY KEY CHECK (`user_id` > 0),

  `instance_id` integer NOT NULL CHECK (`instance_id` > 0),

  -- 0 by default: see the load-bearing note above. An absent row and a row
  -- saying 0 mean the same thing, and both mean OFF.
  `granted` integer NOT NULL DEFAULT 0 CHECK (`granted` IN (0, 1)),

  -- Set when the answer is recorded, and NOT NULL for the same reason
  -- `granted` is never NULL: an answer with no timestamp cannot be shown to the
  -- user as "you consented on ...", which is what makes consent meaningful rather
  -- than merely recorded.
  `granted_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,

  -- NULL while consent stands. Set on revocation, and the row is kept -- see
  -- above. A second grant clears it, so the column records the CURRENT state and
  -- not a history; the audit log (migration 108) is where history belongs.
  `revoked_at` timestamp,

  -- A row that says granted=1 AND revoked_at IS NOT NULL is not a state any
  -- caller should have to interpret. Placed AFTER the last column deliberately:
  -- a table-level CHECK interleaved with column definitions parses the NEXT
  -- column name and reports the error against the wrong thing. Measured, not
  -- folklore.
  CHECK ((`granted` = 1 AND `revoked_at` IS NULL) OR (`granted` = 0))
);

-- One consent row per user, so this is an index on the FK-free lookup the
-- resolver makes on every content request rather than a constraint.
CREATE INDEX `content_consent_instance_idx` ON `content_consent` (`instance_id`);
