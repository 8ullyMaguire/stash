-- stash#2359 L5: which tables referencing `performers` actually clean up on delete,
-- and which do not.
--
-- The question is not "does the schema declare ON DELETE CASCADE" but "does the row disappear when
-- a performer is destroyed". Migration 121's rule is that a general table cannot use a foreign key,
-- so several of these are plain `performer_id integer` columns with no constraint at all, and SQLite
-- ignores constraints that are not declared regardless.
--
-- Measured rather than read: a table whose rows survive the delete is a table whose rows outlive the
-- thing they describe.
--
-- Run against a database created by the app's own migration path, with the app's own DSN
-- (`_fk=true`), because both matter. `_fk=false` makes every answer here "yes" and every row here
-- "survives", which is the failure this script exists to distinguish.

PRAGMA foreign_keys;

.headers on
.mode list

SELECT '--- tables with a performer_id column ---';

SELECT name FROM sqlite_master
 WHERE type = 'table'
   AND name NOT LIKE 'sqlite_%'
 ORDER BY name;

SELECT '--- declared FKs pointing at performers(id) ---';

SELECT
  m.name AS child_table,
  m."from" AS child_column,
  m."on_delete" AS on_delete
FROM sqlite_master AS m
 WHERE m.type = 'table'
   AND m.name NOT LIKE 'sqlite_%'
   AND EXISTS (
     SELECT 1 FROM pragma_foreign_key_list(m.name) AS fk
      WHERE fk."table" = 'performers'
   )
 ORDER BY m.name, m."from";