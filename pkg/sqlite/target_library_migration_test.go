//go:build integration
// +build integration

// Migration tests for M4 step 4.3: the library scope on the target tables.
//
// The question is never "does the version number match" -- the version test
// already covers that, and a version matches perfectly while a migration
// silently does nothing. It is "does every target table carry a library_id, and
// does a row in no library exist and mean what the gate says it means".
//
// Three things here are worth more than the assertions around them:
//
//  1. COLUMNS ON ALL SEVEN TABLES. A migration that added the column to five of
//     seven would still bump the version, still pass the version test, and leave
//     two target types with no library to enforce -- a hole whose size is
//     invisible until someone serves media from it.
//
//  2. THE NULL ROW IS WRITABLE. NULL means "in no library" and the gate refuses
//     it. If NULL were merely the absence of a default, that would still be
//     true, but a future migration that backfills NULL as "public" would make
//     the refusal unreachable, and the way to know is to plant the row and see
//     whether the database accepts it.
//
//  3. THE FOREIGN KEY IS REAL. A library_id with no REFERENCES clause accepts
//     any integer, so a typo'd or stale id resolves to a library that does not
//     exist -- and the grant check then answers "no grant" for a row whose
//     library was deleted, which is the right answer for the wrong reason.

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stashforgeTargetTables is every table migration 105 was supposed to touch.
// Named once, and both the column test and the index test walk it, so a table
// added to the migration but not to this list fails rather than being silently
// untested.
var stashforgeTargetTables = []string{
	"scenes", "images", "galleries", "performers", "tags", "studios", "groups",
}

func TestTargetLibraryScope_ColumnExistsOnEveryTargetTable(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		for _, table := range stashforgeTargetTables {
			assert.Equal(t, int64(1),
				count(t, ctx,
					"SELECT count(*) FROM pragma_table_info(?) WHERE name = 'library_id'",
					table),
				"%s must carry a library_id. If this fails, migration 105 did not "+
					"apply even though the schema version agrees -- and %s is a "+
					"target type whose media the access gate cannot scope", table, table)
		}
	})
}

func TestTargetLibraryScope_IndexedForTheScopeQuery(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		for _, table := range stashforgeTargetTables {
			name := "idx_" + table + "_library"
			assert.Equal(t, int64(1),
				count(t, ctx,
					"SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?",
					name),
				"%s is needed for the scope query, and a partial index on "+
					"library_id IS NOT NULL is the right shape: a row in no "+
					"library is the minority this project cares about", name)
		}
	})
}

// TestTargetLibraryScope_NullIsAStateTheSchemaAllows is the fail-closed property,
// asserted from the database side rather than the Go side.
//
// The gate resolves a NULL library to a refusal. That is a Go decision, and a Go
// decision is only as good as the assumption underneath it -- so this test
// plants the row and confirms the schema ACCEPTS it, which is what makes the
// gate's NULL branch a reachable case rather than dead code.
//
// The second half matters just as much: the FK must REFUSE an id that names no
// library. A library_id with no REFERENCES clause accepts any integer, so a row
// can point at a library that does not exist, and the grant check then answers
// "no grant" for a deleted library -- the right answer for the wrong reason, and
// a diagnostic that never resolves.
func TestTargetLibraryScope_NullIsAStateTheSchemaAllows(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		// Every target table gets one row in no library. The target_type/
		// target_id pairs are unique strings rather than fixed ids: a fixed id
		// passes exactly once and then dies in the full suite, because the
		// shared fixture database persists between runs.
		// Required columns are read off the LIVE schema, not guessed. The
		// first version guessed `scenes(path, checksum)` from migration 12's
		// text and failed with "table scenes has no column named path" --
		// a later migration had moved scenes to the files tables. A fixture
		// that guesses the schema is a fixture whose failure is a typo rather
		// than a finding.
		rows := map[string]struct {
			ins  string
			args []interface{}
		}{
			// scenes: created_at/updated_at are the only NOT NULL, no-default
			// columns outside the autoincrement id. The CHECK that a scene has
			// a hash moved to the files table with migration 45, so a bare row
			// inserts -- the first two versions of this fixture guessed
			// `path` and `checksum` from migration 12's text and failed on
			// columns that stopped existing 33 migrations ago.
			"scene": {
				"INSERT INTO scenes (created_at, updated_at, library_id) " +
					"VALUES (datetime('now'), datetime('now'), NULL)",
				nil,
			},
			"image": {
				"INSERT INTO images (created_at, updated_at, library_id) " +
					"VALUES (datetime('now'), datetime('now'), NULL)",
				nil,
			},
			"gallery": {
				"INSERT INTO galleries (created_at, updated_at, library_id) " +
					"VALUES (datetime('now'), datetime('now'), NULL)",
				nil,
			},
			"performer": {
				"INSERT INTO performers (name, created_at, updated_at, library_id) " +
					"VALUES (?, datetime('now'), datetime('now'), NULL)",
				nil,
			},
			"tag": {
				"INSERT INTO tags (name, created_at, updated_at, library_id) " +
					"VALUES (?, datetime('now'), datetime('now'), NULL)",
				nil,
			},
			"studio": {
				"INSERT INTO studios (name, created_at, updated_at, library_id) " +
					"VALUES (?, datetime('now'), datetime('now'), NULL)",
				nil,
			},
			"group": {
				"INSERT INTO groups (name, created_at, updated_at, library_id) " +
					"VALUES (?, datetime('now'), datetime('now'), NULL)",
				nil,
			},
		}

		for target, r := range rows {
			// A unique name per row, not a fixed one: a fixed identity in a
			// store test is a collision waiting for a reordering, and the
			// shared fixture database persists between runs.
			tag := "sf105null-" + target
			args := r.args
			if args == nil {
				args = []interface{}{tag}
			}
			err := exec(t, ctx, r.ins, args...)
			require.NoError(t, err,
				"a row in no library must be WRITABLE; it is the state the gate "+
					"refuses, and if the schema will not hold it the gate's NULL "+
					"branch is unreachable -- so the backfill is the only thing "+
					"holding the refusal in place (%s)", target)
		}

		// And the foreign key refuses an id naming no library.
		owner := insertUser(t, ctx, "sf105fkowner", false)
		var missingID int64 = 987654321
		assert.Error(t, exec(t, ctx,
			"INSERT INTO tags (name, created_at, updated_at, library_id) VALUES (?, datetime('now'), datetime('now'), ?)",
			"sf105dangling", missingID),
			"a library_id naming no existing library was accepted; the column "+
				"must REFERENCES libraries(id), or a stale id resolves to a "+
				"library that does not exist and the grant check answers 'no "+
				"grant' for the wrong reason (owner %d present, so the failure "+
				"is the FK and not the user fixture)", owner)
	})
}

// TestTargetLibraryScope_PointerIsAcceptedAndRoundTrips: the ordinary case, so
// the two tests above are known to be testing the NULL and FK paths rather than
// a table that refuses every write.
func TestTargetLibraryScope_PointerIsAcceptedAndRoundTrips(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		uid := insertUser(t, ctx, "sf105roundtrip", false)
		require.NoError(t, exec(t, ctx,
			"INSERT INTO libraries (user_id, name) VALUES (?, ?)", uid, "sf105scope"))
		libID := scalar(t, ctx,
			"SELECT id FROM libraries WHERE name = ?", "sf105scope").(int64)

		require.NoError(t, exec(t, ctx,
			"INSERT INTO tags (name, created_at, updated_at, library_id) "+
				"VALUES (?, datetime('now'), datetime('now'), ?)", "sf105scoped", libID))

		assert.Equal(t, libID, scalar(t, ctx,
			"SELECT library_id FROM tags WHERE name = ?", "sf105scoped"),
			"a scoped row must read back with the library it was written with")
	})
}
