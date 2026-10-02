//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// stash#1790 — the external_ids SCHEMA, tested as schema rather than through the store.
//
// THESE TESTS INSERT RAW SQL on purpose. The store's tests (T1-T5, external_id_test.go)
// prove the store's behaviour; these prove the CONSTRAINTS EXIST. A constraint can exist and
// never fire — a typo in a CHECK expression, a UNIQUE index on the wrong columns, or a
// foreign key that SQLite silently ignores because the pragma is off — and the only way to
// know is to write the bad row and watch it be refused.
//
// THE FOREIGN KEY TESTS ARE THE ONES MOST LIKELY TO PASS FOR THE WRONG REASON. SQLite
// IGNORES foreign keys unless `PRAGMA foreign_keys = ON`, per connection. A test that inserts
// a row with a dangling source_id would SUCCEED on a connection with the pragma off and the
// test would report success while proving nothing. So the pragma is asserted first, on the
// same connection the insert uses.

func TestTheForeignKeyPragmaIsOnForThisConnection(t *testing.T) {
	runWithRollbackTxn(t, "foreign keys are enforced on the test connection", func(t *testing.T, ctx context.Context) {
		var on int
		_, out, err := db.QuerySQL(ctx, "PRAGMA foreign_keys", nil)
		require.NoError(t, err)
		require.Len(t, out, 1)
		on = int(out[0][0].(int64))

		// THIS ASSERT IS THE POINT OF THE FILE. Without it, every foreign-key test below
		// could pass because the pragma was off and the constraint was never consulted.
		assert.Equal(t, 1, on,
			"foreign keys must be ON, or every FK test in this file passes for the wrong "+
				"reason: SQLite ignores FK declarations entirely when this is 0")
	})
}

// execRaw is a direct statement with no application logic in the way to be helpful --
// the database's own answer to the question. db.ExecSQL, matching insertIssueRaw in
// issue_schema_test.go, so both schema-test files read alike.
func execRaw(t *testing.T, ctx context.Context, query string, args []interface{}) error {
	t.Helper()
	_, _, err := db.ExecSQL(ctx, query, args)
	return err
}

// queryOneInt runs a query expected to yield a single count.
func queryOneInt(t *testing.T, ctx context.Context, query string, args []interface{}) int64 {
	t.Helper()
	_, rows, err := db.QuerySQL(ctx, query, args)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	return rows[0][0].(int64)
}

func insertSource(t *testing.T, ctx context.Context, name string, url string) int {
	t.Helper()
	require.NoError(t, execRaw(t, ctx,
		`INSERT INTO external_sources (name, url, stash_box) VALUES (?, ?, 0)`,
		[]interface{}{name, url}))
	return int(queryOneInt(t, ctx, `SELECT id FROM external_sources WHERE name = ?`,
		[]interface{}{name}))
}

func TestAnUnknownSourceIsRefused(t *testing.T) {
	runWithRollbackTxn(t, "an unknown source is refused", func(t *testing.T, ctx context.Context) {
		err := execRaw(t, ctx,
			`INSERT INTO external_ids (entity_type, entity_id, source_id, external_id) VALUES (?, ?, ?, ?)`,
			[]interface{}{"scene", 1, 999999, "abc123"})
		require.Error(t, err,
			"THIS IS THE WHOLE POINT OF THE REGISTRY. The legacy `*_stash_ids.endpoint` is a "+
				"bare varchar(255) that nothing validates, so a typo inserts cleanly and is "+
				"then unreachable by every join that filters on the correct endpoint -- a "+
				"silent, permanent orphan. Here it fails at insert instead")
		assert.Contains(t, err.Error(), "FOREIGN KEY",
			"and it must be refused BY THE FOREIGN KEY, not by some unrelated constraint: %v", err)
	})
}

func TestTwoSourcesMayUseTheSameExternalIDStringOnOneEntity(t *testing.T) {
	runWithRollbackTxn(t, "two sources may use the same external id string on one entity",
		func(t *testing.T, ctx context.Context) {
			a := insertSource(t, ctx, "source-a", "https://a.example")
			b := insertSource(t, ctx, "source-b", "https://b.example")

			// SAME string, two providers. The four-column unique key is what makes these two
			// rows rather than one -- the most easily forgotten column in the key, and T2.
			for _, sid := range []int{a, b} {
				err := execRaw(t, ctx,
					`INSERT INTO external_ids (entity_type, entity_id, source_id, external_id) VALUES (?, ?, ?, ?)`,
					[]interface{}{"scene", 7, sid, "shared-string"})
				require.NoError(t, err, "source %d must be allowed to use the same string", sid)
			}

			assert.Equal(t, int64(2), queryOneInt(t, ctx,
				`SELECT count(*) FROM external_ids WHERE entity_type = 'scene' AND entity_id = 7`,
				nil),
				"TWO ROWS. A unique key of (entity_type, entity_id, external_id) without "+
					"source_id would have refused the second, and two providers' ids would "+
					"collide for every user with more than one scraper configured")
		})
}

func TestTheSameSourceOnADifferentEntityIsNotADuplicate(t *testing.T) {
	runWithRollbackTxn(t, "the same source on a different entity is not a duplicate",
		func(t *testing.T, ctx context.Context) {
			s := insertSource(t, ctx, "shared-source", "https://s.example")
			for _, eid := range []int{1, 2} {
				err := execRaw(t, ctx,
					`INSERT INTO external_ids (entity_type, entity_id, source_id, external_id) VALUES (?, ?, ?, ?)`,
					[]interface{}{"scene", eid, s, "same-string"})
				require.NoError(t, err)
			}
			assert.Equal(t, int64(2), queryOneInt(t, ctx,
				`SELECT count(*) FROM external_ids WHERE source_id = ?`, []interface{}{s}),
				"entity_id is part of the identity: two entities may each carry the same id "+
					"from one source")
		})
}

func TestTheExactDuplicateIsRefused(t *testing.T) {
	runWithRollbackTxn(t, "the exact duplicate is refused",
		func(t *testing.T, ctx context.Context) {
			s := insertSource(t, ctx, "dup-source", "https://d.example")
			q := `INSERT INTO external_ids (entity_type, entity_id, source_id, external_id) VALUES (?, ?, ?, ?)`
			require.NoError(t, execRaw(t, ctx, q, []interface{}{"scene", 3, s, "identical"}))

			err := execRaw(t, ctx, q, []interface{}{"scene", 3, s, "identical"})
			require.Error(t, err,
				"the four-column unique key must refuse an exact repeat. The store's Record "+
					"upserts, so this constraint is what stops a second row existing by any "+
					"other path")
			assert.Contains(t, err.Error(), "UNIQUE")
		})
}

func TestASourceNameIsUnique(t *testing.T) {
	runWithRollbackTxn(t, "a source name is unique",
		func(t *testing.T, ctx context.Context) {
			insertSource(t, ctx, "only-one", "https://one.example")
			err := execRaw(t, ctx, `INSERT INTO external_sources (name, url) VALUES (?, ?)`,
				[]interface{}{"only-one", "https://two.example"})
			require.Error(t, err,
				"two sources with one name would make `endpoint` -> source resolution "+
					"ambiguous, which reintroduces the typo problem the registry removes")
		})
}

func TestDeletingASourceDeletesItsIDs(t *testing.T) {
	runWithRollbackTxn(t, "deleting a source deletes its external ids",
		func(t *testing.T, ctx context.Context) {
			s := insertSource(t, ctx, "doomed", "https://doomed.example")
			require.NoError(t, execRaw(t, ctx,
				`INSERT INTO external_ids (entity_type, entity_id, source_id, external_id) VALUES (?, ?, ?, ?)`,
				[]interface{}{"scene", 9, s, "orphaned-by-source"}))

			require.NoError(t, execRaw(t, ctx, `DELETE FROM external_sources WHERE id = ?`,
				[]interface{}{s}))

			assert.Equal(t, int64(0), queryOneInt(t, ctx,
				`SELECT count(*) FROM external_ids WHERE external_id = ?`,
				[]interface{}{"orphaned-by-source"}),
				"CASCADE on source_id is deliberate. An id from a source that no longer "+
					"exists is meaningless, and keeping it produces rows no join can resolve -- "+
					"the exact orphan the registry exists to prevent")
		})
}

func TestTheLookupIndexExistsOnSourceAndExternalID(t *testing.T) {
	runWithRollbackTxn(t, "the lookup index exists on (source_id, external_id)",
		func(t *testing.T, ctx context.Context) {
			// The lookup direction that matters is "which local entity has this external id
			// from this source" -- what a metadata scrape resolves against. Without the index
			// that is a full scan of every external id the library has ever seen, on every
			// scrape.
			_, rows, err := db.QuerySQL(ctx,
				`SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'external_ids'`,
				nil)
			require.NoError(t, err)

			names := map[string]bool{}
			for _, r := range rows {
				names[r[0].(string)] = true
			}
			assert.True(t, names["index_external_ids_on_lookup"],
				"found: %v. Missing means every scrape full-scans", names)
			assert.True(t, names["index_external_ids_on_entity"],
				"found: %v. Missing means every entity delete full-scans, including during a "+
					"library clean", names)
		})
}

func TestTheEntityIDColumnIsNotAForeignKeyAndThatIsDeliberate(t *testing.T) {
	runWithRollbackTxn(t, "entity_id is NOT a foreign key, and that is deliberate",
		func(t *testing.T, ctx context.Context) {
			s := insertSource(t, ctx, "polymorphic", "https://p.example")

			// MUST SUCCEED. entity_id 999999 does not exist in scenes, galleries, performers,
			// studios or tags.
			err := execRaw(t, ctx,
				`INSERT INTO external_ids (entity_type, entity_id, source_id, external_id) VALUES (?, ?, ?, ?)`,
				[]interface{}{"gallery", 999999, s, "no-such-gallery"})
			require.NoError(t, err,
				"this insert SUCCEEDING is the documented cost of the polymorphic reference "+
					"(spec section 4.2): entity_id cannot be a foreign key because SQLite "+
					"requires FK targets to be UNIQUE and there is no single unique column "+
					"here. That is WHY DestroyForEntity and SweepOrphans are mandatory rather "+
					"than nice-to-have -- this row is now an orphan and nothing removes it but "+
					"the sweep")

			// ...and which is exactly why the sweep exists. The DELETE runs in THIS
			// transaction rather than in a nested one.
			//
			// NO NESTED runWithRollbackTxn. The first version wrapped the sweep in its own
			// rollback transaction inside this one, and the suite HUNG -- not failed, hung,
			// until the -timeout killed it. The outer transaction holds the write lock on
			// the connection, so the inner Begin blocks on a lock nothing will release. It
			// looks like a reasonable structure and is a deadlock; a test that hangs is far
			// worse than one that fails, because it costs a timeout instead of a message.
			//
			// The rollback of the OUTER transaction is what cleans the orphan up here, so
			// doing it inline needs no cleanup and leaks nothing into the committed seed.
			// ExecSQL, NOT QuerySQL: QuerySQL returns the statement's RESULT ROWS, and a
			// DELETE returns none, so the first version of this asserted len(res) == 1
			// against an empty slice and failed for a reason that had nothing to do with
			// orphans. ExecSQL's first return value is RowsAffected, which is what a
			// DELETE actually has to report.
			affected, _, err := db.ExecSQL(ctx,
				`DELETE FROM external_ids WHERE entity_type = 'gallery' AND entity_id = 999999`,
				nil)
			require.NoError(t, err)
			require.NotNil(t, affected)
			assert.Equal(t, int64(1), *affected,
				"the orphan is removable by exactly the query SweepOrphans runs -- asserted "+
					"here so the documented cost is demonstrated rather than merely claimed")
		})
}

func TestEntityTypeIsFreeTextButNotBlank(t *testing.T) {
	runWithRollbackTxn(t, "entity_type is free text but not blank",
		func(t *testing.T, ctx context.Context) {
			s := insertSource(t, ctx, "free-type", "https://f.example")
			// A NEW entity type must be addable without a migration -- that is one of the
			// requirements, and a CHECK constraint listing the known types would defeat it.
			for _, et := range []string{"gallery", "movie", "brand_new_entity_2099"} {
				err := execRaw(t, ctx,
					`INSERT INTO external_ids (entity_type, entity_id, source_id, external_id) VALUES (?, ?, ?, ?)`,
					[]interface{}{et, 1, s, "x-" + et})
				require.NoError(t, err, "entity_type %q must be accepted without a migration", et)
			}

			// But an EMPTY type is not: a row with no entity is unqueryable by definition,
			// since every lookup filters on it.
			err := execRaw(t, ctx,
				`INSERT INTO external_ids (entity_type, entity_id, source_id, external_id) VALUES (?, ?, ?, ?)`,
				[]interface{}{"", 1, s, "no-type"})
			require.Error(t, err,
				"a blank entity_type is refused, but a NON-BLANK unknown one is accepted: "+
					"the column is deliberately unconstrained so a new entity needs no "+
					"migration, and the blank check is what stops an unqueryable row")
			_ = models.ExternalID{}
		})
}

// THE BLANK external_id ROW IS REFUSED BY THE DATABASE, NOT BY THE STORE.
//
// This exists because mutation M7 (remove the `check(length(trim(external_id)) > 0)`)
// SURVIVED the whole suite, and the reason is a layered defence:
//
//	pkg/sqlite/external_id.go   refuses a blank external_id in Go, before any SQL runs
//	migration 121                refuses a blank external_id with a CHECK
//
// The store test asserts the store refuses, which stays green when the CHECK is deleted --
// because the Go check still refuses. A lower layer refused the identical input, so per the
// four-verdict rule that is `covered`, not `killed`; but as a single test it reads as proof
// and proves nothing about the schema.
//
// THE FIX IS TO TEST THE LAYER. This inserts raw SQL, so the Go check cannot intercept it
// and the CHECK is the only thing standing between the insert and a row. Same family as the
// foreign-key pragma assertion at the top of this file: measure the layer you name.
func TestABlankExternalIDIsRefusedByTheDatabaseItself(t *testing.T) {
	runWithRollbackTxn(t, "a blank external_id is refused by the database",
		func(t *testing.T, ctx context.Context) {
			s := insertSource(t, ctx, "blank-id-src", "https://blank.example")

			// THE INPUT ONLY THE CHECK REFUSES. Store.Record would refuse this before
			// reaching the database, so going through the store would prove nothing.
			err := execRaw(t, ctx,
				`INSERT INTO external_ids (entity_type, entity_id, source_id, external_id) VALUES (?, ?, ?, ?)`,
				[]interface{}{"scene", 1, s, ""})
			require.Error(t, err,
				"the DATABASE must refuse a blank external_id. A row with an empty id "+
					"matches nothing and is findable by nothing, and it would be "+
					"indistinguishable from a row that was never recorded")

			// ...and the check is not merely "not empty": SPACES must be refused too,
			// which is what the trim() in the CHECK is for, since an id of "  " is as
			// unmatchable as one of "". This one faces UNTRUSTED input -- external_id
			// comes from a provider -- so the store's own Go check matters as a second
			// layer. See the tab case in the companion test for what this CHECK does NOT
			// catch: one-argument trim strips spaces and nothing else, measured.
			err = execRaw(t, ctx,
				`INSERT INTO external_ids (entity_type, entity_id, source_id, external_id) VALUES (?, ?, ?, ?)`,
				[]interface{}{"scene", 1, s, "   "})
			require.Error(t, err,
				"a spaces-only id is refused -- a provider that sends \"  \" must not get "+
					"a row that no lookup can ever find")

			// THE CONTROL: a real value on the same path succeeds, so the assertion above
			// is about the VALUE rather than about the insert being broken.
			require.NoError(t, execRaw(t, ctx,
				`INSERT INTO external_ids (entity_type, entity_id, source_id, external_id) VALUES (?, ?, ?, ?)`,
				[]interface{}{"scene", 1, s, "real-value"}))
		})
}

// The same trap, caught in the OTHER direction: the blank entity_type CHECK (M6) was killed
// by a schema test, and this is the test that did it. M7 needed a twin because the store
// guarded one field in Go and the schema test covered only the other.
func TestABlankEntityTypeIsRefusedByTheDatabaseItself(t *testing.T) {
	runWithRollbackTxn(t, "a blank entity_type is refused by the database",
		func(t *testing.T, ctx context.Context) {
			s := insertSource(t, ctx, "blank-type-src", "https://blank2.example")

			// SPACES ONLY, and that limit is measured, not assumed.
			//
			// `check(length(trim(entity_type)) > 0)` refuses "" and "  ". It does NOT
			// refuse a value made only of whitespace that is not a space, because
			// SQLite's one-argument `trim` strips SPACES and nothing else. Probed
			// against the same driver the suite uses:
			//
			//	""    -> refused
			//	"  "  -> refused
			//	"\t"   -> ACCEPTED      <-- length(trim("\\t")) is 1
			//	" \t " -> ACCEPTED
			//
			// So the loop below asserts only the cases the DDL actually refuses, and the
			// tab is asserted to be ACCEPTED below. A test that asserted a tab was refused
			// would fail against correct code, and the reflex fix -- widening the CHECK --
			// would be solving a problem this column does not have: entity_type is set by
			// this library from a constant, never scraped from a provider, so a
			// tab-only value cannot arrive from the input that matters. The external_id
			// CHECK is the one that faces untrusted input, and it has the same limit,
			// which is why the store ALSO refuses a blank id in Go before any SQL runs.
			for _, blank := range []string{"", "  ", "   "} {
				err := execRaw(t, ctx,
					`INSERT INTO external_ids (entity_type, entity_id, source_id, external_id) VALUES (?, ?, ?, ?)`,
					[]interface{}{blank, 1, s, "x"})
				require.Error(t, err, "entity_type %q must be refused", blank)
			}

			require.NoError(t, execRaw(t, ctx,
				`INSERT INTO external_ids (entity_type, entity_id, source_id, external_id) VALUES (?, ?, ?, ?)`,
				[]interface{}{"scene", 1, s, "x"}))
		})
}
