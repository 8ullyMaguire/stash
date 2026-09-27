-- Consent. StashForge M3, step 3.1. Spec §4 and §6.1.
--
-- One row per user, holding the answer to "may this user's metadata be
-- published". It defaults to opted-IN (spec §6.1: the owner's requirement that
-- local use contributes by default), which makes this the one table in the
-- project where the ABSENT row is the load-bearing case rather than the row
-- that is written. Get the default wrong and every user who has never been asked
-- is silently published.
--
-- The default leaks, and the spec says so: "This is the one place where the
-- default leaks information the user never actively disclosed, so it is made
-- conspicuous rather than silent." That conspicuousness lives in
-- `disclosure_version` below and in the blocking first-run screen, not here --
-- a schema cannot make a prompt appear, but it can record which prompt the user
-- actually answered.
CREATE TABLE `consent_preferences` (
  -- PRIMARY KEY and not an autoincrement id: the row IS the user, so there is
  -- exactly one per user by construction and no second row is representable.
  -- A surrogate key here would permit two consent rows for one user and make
  -- "opted in or out" a question with two answers.
  `user_id`             integer NOT NULL PRIMARY KEY REFERENCES `users` (`id`) ON DELETE CASCADE,

  -- 'opted-in' | 'opted-out', CHECKed because this string is compared in the
  -- publish path and a typo in a migration would otherwise be a silent
  -- always-share. An unknown value must fail at the write, not at the read.
  `metadata_share`      text NOT NULL DEFAULT 'opted-in'
                           CHECK (`metadata_share` IN ('opted-in', 'opted-out')),

  -- When the user decided. Recorded so "this row was written by the migration's
  -- DEFAULT rather than by a person" is distinguishable from a real answer --
  -- which is why the exporter does not consult decided_at for correctness, but
  -- the disclosure screen does, to decide who still needs asking.
  `decided_at`          datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,

  -- The version of the disclosure the user actually SAW. This is the
  -- re-prompt mechanism and it is the reason the table is not just a boolean.
  --
  -- When the published field set changes -- a new field added to the exporter,
  -- a field removed -- the version is bumped. A user whose `disclosure_version`
  -- is below the current version has not agreed to the *new* list, so they are
  -- re-prompted, and until they answer the current behaviour is whatever
  -- `metadata_share` says. A user who had opted OUT is not re-prompted into
  -- sharing: an absent answer must never be read as a new consent. See
  -- NeedsReprompt for that distinction.
  `disclosure_version`  integer NOT NULL DEFAULT 1
);
