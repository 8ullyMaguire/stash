-- Instance mode. StashForge M4, step 4.1. Spec §2.
--
-- The instance's posture, and the one setting in this project that changes what
-- the server will REFUSE TO DO rather than what it will do.
--
-- WHY A TABLE AND NOT A CONFIG FILE
--
-- The mode has to be readable by the same code that enforces it, transactionally,
-- alongside the grants it governs. A config file read at startup is a different
-- source of truth from the database the consent and grant rows live in, and the
-- day they disagree -- a mode changed in the file, a grant revoked in the
-- database -- the instance behaves according to whichever one the caller happened
-- to read. The two failure modes that matter here are "a public mode nobody
-- chose" and "media served to someone whose grant was revoked", and both are
-- easier to rule out with one store.
--
-- WHY THE DEFAULT IS private
--
-- The default is the most restrictive mode, not the most permissive. A mode
-- defaulting to public because nobody read a doc is the exact failure step 4.4
-- exists to prevent, and a schema default is read by nobody at all. An
-- UNSET row is therefore not "public" or "contribute" -- it is an unconfigured
-- instance, which reads as private and is treated as private by
-- collab.ModeFrom.
CREATE TABLE `instance_settings` (
  `id`          integer NOT NULL PRIMARY KEY CHECK (`id` = 1),

  -- CHECKed, and the CHECK lists all three modes. An unrecognised mode must
  -- fail at the WRITE: a mode string that reaches the read path unvalidated
  -- would be a posture nobody chose, decided by a typo.
  `mode`        text NOT NULL DEFAULT 'private'
                  CHECK (`mode` IN ('private', 'contribute', 'public')),

  -- When the mode was last changed, and by whom. A posture change is a security
  -- decision, and "when did this instance become public, and who made it public"
  -- has to be answerable afterwards -- a mode column with no history answers
  -- neither.
  `mode_changed_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `mode_changed_by` integer REFERENCES `users` (`id`) ON DELETE SET NULL,

  -- Whether the blocking first-run wizard has been completed (step 4.4).
  -- Separate from the mode because "the mode is private" and "somebody chose
  -- private on purpose" are different states, and only the second one means the
  -- wizard ran. Until this is set, a public mode is refused even if the row says
  -- public -- a mode set by a migration or a hand-edited row is not a choice.
  `wizard_completed` integer NOT NULL DEFAULT 0 CHECK (`wizard_completed` IN (0, 1))
);

-- Exactly one row. CHECK (id = 1) makes a second row unrepresentable rather
-- than merely discouraged, so "what is the mode" can never have two answers.
--
-- Seeded here rather than by the store on first read, because a lazy "INSERT IF
-- NOT EXISTS" is a write on a read path, and a read path that writes is a read
-- path that can fail in a transaction that expected to do nothing.
INSERT INTO `instance_settings` (`id`, `mode`, `wizard_completed`) VALUES (1, 'private', 0);
