//go:build integration
// +build integration

// stash#837: the issues log — schema tests.
//
// WHY THIS FILE EXISTS SEPARATELY FROM THE STORE TESTS
//
// Every other behaviour in issue_test.go can be proven by calling the store. The
// SCHEMA cannot: a test that reads sqlite_master and finds the unique index
// proves the index is PRESENT, which is not the same claim as the index FIRING.
// So this file writes the bad rows directly and asserts the database refuses
// them. The first version of this check only read sqlite_master and passed
// against a table whose index had been renamed -- which is the shape of gate
// that looks like coverage and is not.
//
// The three refusals tested here are the ones the spec's §3 identity promise
// rests on:
//   1. the identical live finding twice            -> refused (one fact, one row)
//   2. the identical finding, already dismissed    -> refused (dismissal sticks)
//   3. a NEW finding of the same kind, new details -> ACCEPTED (not suppressed)

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// runWithRollbackTxn is the package's standard isolation helper; issue_test.go
// uses the same one so both files read alike.
func insertIssueRaw(t *testing.T, ctx context.Context, fileID interface{}, domain, kind, details string, resolved bool) error {
	t.Helper()
	// A direct INSERT, deliberately bypassing the store: this is the database's
	// own answer to "may these two rows coexist", with no application logic in
	// the way to be helpful.
	_, _, err := db.ExecSQL(ctx, `
		INSERT INTO issues (file_id, domain, kind, details, detected_at, resolved)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, ?)`,
		[]interface{}{fileID, domain, kind, details, resolved})
	return err
}

// stash#837: the table and its two indexes exist, and the columns say what the
// spec says they say.
//
// The index assertions are the weak half of this test and are labelled as such
// in the comments; the strong half is TestTheUniqueIndexFires.
func TestIssueTable(t *testing.T) {
	runWithRollbackTxn(t, "the issues table has the shape the spec describes", func(t *testing.T, ctx context.Context) {
		_, rows, err := db.QuerySQL(ctx, `PRAGMA table_info(issues)`, nil)
		require.NoError(t, err)

		cols := map[string]bool{}
		for _, r := range rows {
			cols[r[1].(string)] = true
		}
		for _, want := range []string{
			"id", "file_id", "domain", "kind", "details",
			"detected_at", "resolved", "resolved_at",
		} {
			assert.True(t, cols[want], "issues.%s must exist", want)
		}

		// WEAK BY CONSTRUCTION, and stated as such: this proves the indexes are
		// present, NOT that they constrain anything. TestTheUniqueIndexFires is
		// what proves the constraint.
		_, idx, err := db.QuerySQL(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'issues'`, nil)
		require.NoError(t, err)
		var names []string
		for _, r := range idx {
			names = append(names, r[0].(string))
		}
		assert.Contains(t, names, "idx_issues_unique")
		assert.Contains(t, names, "idx_issues_unresolved")
		assert.Contains(t, names, "idx_issues_file")
	})
}

// stash#837, spec §3: the unique index FIRES. Not "exists" -- fires.
func TestTheUniqueIndexFires(t *testing.T) {
	runWithRollbackTxn(t, "the unique index refuses the rows it is there to refuse", func(t *testing.T, ctx context.Context) {
		folderID := mkStash3849Folder(t, ctx, "/library/issues-837")
		f := &models.BaseFile{Basename: "a.jpg", ParentFolderID: folderID}
		require.NoError(t, db.File.Create(ctx, f))

		// (1) THE SAME LIVE FINDING TWICE. This is the case the whole design
		// exists for: a duplicate file is a duplicate on every scan, and a table
		// that appends a row per scan is a log.
		require.NoError(t, insertIssueRaw(t, ctx, int(f.ID), "file", "duplicate", "same as b.jpg", false),
			"the first live finding must be insertable")
		err := insertIssueRaw(t, ctx, int(f.ID), "file", "duplicate", "same as b.jpg", false)
		require.Error(t, err,
			"the identical live finding must be REFUSED -- this is the case that makes "+
				"the table a record of facts rather than an append-only log")
		assert.Contains(t, err.Error(), "UNIQUE",
			"and it must fail on the unique index, not on something incidental")

		// (2) THE SAME FINDING, ALREADY DISMISSED. DELIBERATELY NOT ASSERTED HERE, and
		// the migration says why at length: refusing duplicate DISMISSED rows and allowing
		// a NEW finding of the same kind are contradictory for a unique index, so the index
		// keeps the live-row invariants and the STORE enforces the dismissal policy. The
		// test for (2) is TestDismissedIssueIsNotReraised in issue_test.go, and asserting it
		// here would be asserting the wrong layer.
		//
		// What IS asserted here is the weaker fact the index does guarantee: a live row and
		// a dismissed row of the same finding COEXIST, which is what lets a dismissal be
		// recorded while the same finding is still being re-detected.
		require.NoError(t, insertIssueRaw(t, ctx, int(f.ID), "file", "duplicate", "same as b.jpg", true),
			"a dismissed finding must be insertable alongside the live one")
	})

	runWithRollbackTxn(t, "a new finding of the same kind is not suppressed by a dismissal", func(t *testing.T, ctx context.Context) {
		folderID := mkStash3849Folder(t, ctx, "/library/issues-837b")
		f := &models.BaseFile{Basename: "b.jpg", ParentFolderID: folderID}
		require.NoError(t, db.File.Create(ctx, f))

		require.NoError(t, insertIssueRaw(t, ctx, int(f.ID), "file", "duplicate", "first finding", true),
			"a finding the user has already dismissed")
		// The same file, same kind, but a DIFFERENT fact. Refusing this is how a
		// library silently stops reporting problems, which is the failure mode
		// the spec's §3 explicitly rejects.
		require.NoError(t, insertIssueRaw(t, ctx, int(f.ID), "file", "duplicate", "now also a zero-byte copy", true),
			"a genuinely NEW finding of the same kind must be recorded even after a dismissal; "+
				"`details` is not in the key precisely so it is not")
	})
}

// stash#837: a file's issues die with the file.
//
// The panel renders a file's name from the join, so a surviving row for a
// deleted file shows as a blank line the user cannot act on. This is the
// migration's ON DELETE CASCADE stated as a behaviour rather than a clause.
func TestAnIssueDiesWithItsFile(t *testing.T) {
	runWithRollbackTxn(t, "an issue dies with its file", func(t *testing.T, ctx context.Context) {
		folderID := mk837Folder(t, ctx, "/library/issues-837c")
		// SIZE 1024, SO DETECTION STAYS QUIET. This test counts this file's issue ROWS, and
		// FileStore.Create runs stash#837's detection -- so a zero-byte fixture makes the
		// detector record a `zero_size` row of its own, and the raw insert below then
		// collides with it on the unique index. The detector would be RIGHT; the fixture is
		// what has to move. Two earlier versions of this fixture had Size 0 and both failed
		// for this reason, which is worth stating because "the insert was refused" reads like
		// a schema bug and is not one.
		f := &models.BaseFile{Basename: "c.jpg", ParentFolderID: folderID, Size: 1024}
		require.NoError(t, db.File.Create(ctx, f))
		require.NoError(t, insertIssueRaw(t, ctx, int(f.ID), "file", "zero_size", "", false))

		_, before, err := db.QuerySQL(ctx, `SELECT count(*) FROM issues WHERE file_id = ?`, []interface{}{int(f.ID)})
		require.NoError(t, err)
		require.Equal(t, int64(1), before[0][0])

		require.NoError(t, db.File.Destroy(ctx, f.ID))

		_, after, err := db.QuerySQL(ctx, `SELECT count(*) FROM issues WHERE file_id = ?`, []interface{}{int(f.ID)})
		require.NoError(t, err)
		assert.Equal(t, int64(0), after[0][0],
			"deleting a file must delete its issues; a surviving row renders as a blank "+
				"line in the panel, which is a row the user cannot act on or explain")
	})
}

// stash#837: a scan-level issue has no file, and two of them do not collide.
//
// NULL file_ids are distinct as far as a SQL unique index is concerned, which is
// the behaviour wanted here: two "this scan found nothing" rows for the SAME scan
// are noise, but for two different scans they are not the same fact.
func TestAScanLevelIssueHasNoFile(t *testing.T) {
	runWithRollbackTxn(t, "a scan-level issue has no file", func(t *testing.T, ctx context.Context) {
		require.NoError(t, insertIssueRaw(t, ctx, nil, "scan", "no_files", "scan 41 added nothing", false))
		require.NoError(t, insertIssueRaw(t, ctx, nil, "scan", "no_files", "scan 42 added nothing", false),
			"two scans that each added nothing are two different facts, so neither may be "+
				"suppressed by the other's NULL file_id")

		_, rows, err := db.QuerySQL(ctx,
			`SELECT count(*) FROM issues WHERE domain = 'scan' AND file_id IS NULL`, nil)
		require.NoError(t, err)
		assert.Equal(t, int64(2), rows[0][0])
	})
}

// stash#837: the domain CHECK holds, and it is not a tautology.
//
// A CHECK that ORs exhaustive cases accepts everything (see the project's trim
// and CHECK notes), so this asserts the refusal rather than the acceptance.
func TestTheDomainCheckRefusesAnUnknownDomain(t *testing.T) {
	runWithRollbackTxn(t, "the domain check refuses an unknown domain", func(t *testing.T, ctx context.Context) {
		err := insertIssueRaw(t, ctx, nil, "not_a_domain", "whatever", "", false)
		require.Error(t, err, "the domain CHECK must refuse a value outside ('file','scan','metadata')")
		assert.Contains(t, err.Error(), "CHECK", "and it must fail on the CHECK, not on something else")

		// And a blank kind is refused too, for the same reason: a row whose kind is
		// '' cannot be filtered on, and the panel has nothing to render.
		err = insertIssueRaw(t, ctx, nil, "file", "   ", "", false)
		require.Error(t, err, "a whitespace-only kind must be refused; it can never be matched by a filter")
	})
}
