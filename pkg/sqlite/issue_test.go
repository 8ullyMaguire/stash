//go:build integration
// +build integration

// stash#837 — the issues store.
//
// The tests here cover the two behaviours the SCHEMA deliberately does not, and
// the split is the design (see migration 120 and the IssueStore doc comment):
//
//   the INDEX  guarantees  a live finding is unique, and two racing scans
//                    cannot both insert one.
//   the STORE  guarantees  a finding the user DISMISSED is not raised again, and
//                    that resolving twice does not move the date.
//
// A test that asserted either of those at the schema layer would be asserting the
// wrong layer, which is a mistake this file has already made once: the first
// version of the schema test asserted the dismissal rule against the unique index
// and passed only after the index was changed to a shape that cannot hold it.

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// mk837Folder makes a folder for a file to live in. A LOCAL helper rather than a
// shared one on purpose: the identity of an issue is (file_id, domain, kind), so
// its tests need a file with a real parent folder, and borrowing another issue's
// fixture helper would tie this file's setup to a bug fix that has nothing to do
// with it. When that helper changed, these tests would move for no reason a reader
// could see.
func mk837Folder(t *testing.T, ctx context.Context, path string) models.FolderID {
	t.Helper()
	f := &models.Folder{Path: path}
	require.NoError(t, db.Folder.Create(ctx, f))
	return f.ID
}

// mkIssueFile makes a real file row, because an issue's whole identity is
// (file_id, domain, kind) and a fabricated id would not exercise the uniqueness.
func mkIssueFile(t *testing.T, ctx context.Context, folder string, name string) *models.BaseFile {
	t.Helper()
	f := &models.BaseFile{Basename: name, ParentFolderID: mk837Folder(t, ctx, folder)}
	require.NoError(t, db.File.Create(ctx, f))
	return f
}

func countIssues(t *testing.T, ctx context.Context, where string, args ...interface{}) int {
	t.Helper()
	q := `SELECT count(*) FROM issues`
	if where != "" {
		q += " WHERE " + where
	}
	_, rows, err := db.QuerySQL(ctx, q, args)
	require.NoError(t, err)
	n, ok := rows[0][0].(int64)
	require.True(t, ok, "count(*) came back as %T", rows[0][0])
	return int(n)
}

// stash#837, spec §3: the same live finding twice is ONE row, with detected_at moved on.
func TestIssueIsIdempotent(t *testing.T) {
	runWithRollbackTxn(t, "the same live finding is recorded once", func(t *testing.T, ctx context.Context) {
		f := mkIssueFile(t, ctx, "/library/837-idem", "a.jpg")
		id := models.FileID(f.ID)

		first := &models.Issue{FileID: &id, Domain: models.IssueDomainFile, Kind: models.IssueKindDuplicate, Details: "same as b.jpg"}
		require.NoError(t, db.Issue.Record(ctx, first))
		firstSeen := first.DetectedAt
		require.NotZero(t, firstSeen, "Record must stamp detected_at; an issue with no time cannot be ordered")

		second := &models.Issue{FileID: &id, Domain: models.IssueDomainFile, Kind: models.IssueKindDuplicate, Details: "same as b.jpg"}
		require.NoError(t, db.Issue.Record(ctx, second))

		assert.Equal(t, 1, countIssues(t, ctx, "file_id = ? AND resolved = false", int(f.ID)),
			"the identical finding must stay ONE row. A table that appends a row per scan is a "+
				"log, and a panel over a log is the thing the issue complains about")

		// THE CONFLICT PATH MUST ACTUALLY UPDATE, and the observable is `details`,
		// NOT detected_at.
		//
		// The first version of this assertion checked that detected_at had moved, and it
		// FAILED against correct code. sqlite.Timestamp stores RFC3339 -- SECOND
		// precision -- so two Records inside the same second are the same instant once
		// round-tripped, and no assertion on the timestamp can distinguish them. Trying
		// to (a string timestamp that did not move) was a test bug, not a store defect.
		//
		// `details` is written by the SAME `DO UPDATE SET` clause, so it is the
		// observable that survives the precision limit: if the conflict path silently
		// did nothing, the new wording would not land.
		third := &models.Issue{FileID: &id, Domain: models.IssueDomainFile, Kind: models.IssueKindDuplicate, Details: "re-scan: still matches b.jpg"}
		require.NoError(t, db.Issue.Record(ctx, third))

		all, err := db.Issue.FindBy(ctx, &models.IssueFilterType{FileID: &id})
		require.NoError(t, err)
		require.Len(t, all, 1, "still one row after three recordings of the same finding")
		assert.Equal(t, "re-scan: still matches b.jpg", all[0].Details,
			"re-recording a live finding must REFRESH the row, not silently no-op: the "+
				"conflict path's DO UPDATE is what proves the index was hit rather than "+
				"the insert being skipped")
		assert.False(t, all[0].DetectedAt.Before(firstSeen.Truncate(time.Second)),
			"and detected_at must never go BACKWARDS when a finding is re-recorded")
	})
}

// stash#837: a DISMISSED finding is not raised again. The index cannot do this
// (migration 120 explains why), so this is the STORE's policy and this is its test.
func TestDismissedIssueIsNotReraised(t *testing.T) {
	runWithRollbackTxn(t, "a dismissed finding is not raised again", func(t *testing.T, ctx context.Context) {
		f := mkIssueFile(t, ctx, "/library/837-dismiss", "b.jpg")
		id := models.FileID(f.ID)

		live := &models.Issue{FileID: &id, Domain: models.IssueDomainFile, Kind: models.IssueKindZeroSize}
		require.NoError(t, db.Issue.Record(ctx, live))
		issueID := live.ID
		require.NotZero(t, issueID, "Record must populate ID so a caller can resolve the row it just made")

		require.NoError(t, db.Issue.Resolve(ctx, issueID))

		// The next scan finds the same problem. Nothing should happen.
		again := &models.Issue{FileID: &id, Domain: models.IssueDomainFile, Kind: models.IssueKindZeroSize}
		require.NoError(t, db.Issue.Record(ctx, again))

		assert.Equal(t, 0, countIssues(t, ctx, "file_id = ? AND resolved = false", int(f.ID)),
			"a dismissed finding must NOT come back as a new live row -- the user's click has to "+
				"mean something, or they click it again every scan and stop trusting the panel")
		assert.Equal(t, 1, countIssues(t, ctx, "file_id = ? AND resolved = true", int(f.ID)),
			"and the dismissed row must still be there, exactly once")
	})
}

// stash#837: the dismissal is by IDENTITY, not by wording.
//
// `details` is deliberately excluded from the dismissal check, so a scanner whose
// phrasing changes does not resurrect a finding the user has already dealt with.
// This test pins that decision, because the intuitive version (match on details
// too) is one line away and would put the same row back on the panel forever.
func TestADismissalSurvivesTheScannerRephrasingItself(t *testing.T) {
	runWithRollbackTxn(t, "a dismissal survives the scanner rephrasing the finding", func(t *testing.T, ctx context.Context) {
		f := mkIssueFile(t, ctx, "/library/837-rephrase", "c.jpg")
		id := models.FileID(f.ID)

		first := &models.Issue{FileID: &id, Domain: models.IssueDomainFile, Kind: models.IssueKindDuplicate, Details: "matches 003.jpg"}
		require.NoError(t, db.Issue.Record(ctx, first))
		require.NoError(t, db.Issue.Resolve(ctx, first.ID))

		// Same fact, different words -- e.g. a path that changed, or a version bump.
		second := &models.Issue{FileID: &id, Domain: models.IssueDomainFile, Kind: models.IssueKindDuplicate, Details: "is a byte-for-byte copy of 003.jpg"}
		require.NoError(t, db.Issue.Record(ctx, second))

		assert.Equal(t, 0, countIssues(t, ctx, "file_id = ? AND resolved = false", int(f.ID)),
			"a reworded finding is still the finding the user dismissed; matching on details "+
				"would put it back on the panel every time the scanner's wording shifted")
	})
}

// stash#837: a GENUINELY DIFFERENT kind on a dismissed file IS recorded.
//
// The counterpart to the test above, and the one that keeps "a dismissal is not
// silence". Same file, same dismissal, different KIND: the user said "this
// duplicate is fine", not "this file is fine".
func TestADifferentKindIsRecordedAfterADismissal(t *testing.T) {
	runWithRollbackTxn(t, "a different kind is recorded after a dismissal", func(t *testing.T, ctx context.Context) {
		f := mkIssueFile(t, ctx, "/library/837-otherkind", "d.jpg")
		id := models.FileID(f.ID)

		dup := &models.Issue{FileID: &id, Domain: models.IssueDomainFile, Kind: models.IssueKindDuplicate, Details: "matches e.jpg"}
		require.NoError(t, db.Issue.Record(ctx, dup))
		require.NoError(t, db.Issue.Resolve(ctx, dup.ID))

		// A DIFFERENT problem with the same file, found later.
		zero := &models.Issue{FileID: &id, Domain: models.IssueDomainFile, Kind: models.IssueKindZeroDuration, Details: "container reports 0s"}
		require.NoError(t, db.Issue.Record(ctx, zero))

		assert.Equal(t, 1, countIssues(t, ctx, "file_id = ? AND resolved = false", int(f.ID)),
			"dismissing a duplicate must not silence a zero-duration finding on the same file: "+
				"the user dismissed a fact, not the file")
		// BOTH rows exist; the live one is unfiltered, the dismissed one is behind
		// `resolved = true`. Asking only for resolved and expecting two rows was a bug
		// in this test, not a store defect -- caught here rather than in review.
		live, err := db.Issue.FindBy(ctx, &models.IssueFilterType{FileID: &id, Resolved: boolPtr(false)})
		require.NoError(t, err)
		require.Len(t, live, 1)
		assert.Equal(t, models.IssueKindZeroDuration, live[0].Kind)
		resolved, err := db.Issue.FindBy(ctx, &models.IssueFilterType{FileID: &id, Resolved: boolPtr(true)})
		require.NoError(t, err)
		require.Len(t, resolved, 1)
		assert.Equal(t, models.IssueKindDuplicate, resolved[0].Kind)
	})
}

// stash#837: Resolve is IDEMPOTENT, and the guard is what makes it so.
func TestResolveIsIdempotent(t *testing.T) {
	runWithRollbackTxn(t, "resolving twice leaves the dismissal date alone", func(t *testing.T, ctx context.Context) {
		f := mkIssueFile(t, ctx, "/library/837-resolve", "e.jpg")
		id := models.FileID(f.ID)
		issue := &models.Issue{FileID: &id, Domain: models.IssueDomainFile, Kind: models.IssueKindZeroSize}
		require.NoError(t, db.Issue.Record(ctx, issue))

		require.NoError(t, db.Issue.Resolve(ctx, issue.ID))
		first, err := db.Issue.Find(ctx, issue.ID)
		require.NoError(t, err)
		require.NotNil(t, first)
		require.NotNil(t, first.ResolvedAt,
			"a resolved issue must record WHEN it was dismissed. The panel shows this as "+
				"'dismissed on', and a dismissal with no date is a dismissal the user cannot "+
				"reason about later -- 'I dealt with that' means nothing without a when")
		assert.False(t, first.ResolvedAt.IsZero(),
			"and the recorded instant must be a real time, not the zero value")

		// READ THE RAW COLUMN, before anything in this test writes to it. A mutation that
		// dropped `resolved_at` from Resolve's UPDATE survived the version of this test
		// that only compared two Find() results, because the backdating below sets the
		// column itself -- so the value was present no matter what Resolve did. Asserting
		// on the column immediately after the first Resolve is the only place where
		// Resolve's own write is the sole explanation for what is there.
		_, raw, rawErr := db.QuerySQL(ctx,
			`SELECT resolved_at IS NOT NULL FROM issues WHERE id = ?`, []interface{}{issue.ID})
		require.NoError(t, rawErr)
		require.Equal(t, int64(1), raw[0][0],
			"Resolve itself must write resolved_at: a dismissal with no date is one the user "+
				"cannot reason about later")

		// AND THE DATE MUST BE THE MOMENT OF THE DISMISSAL, not merely a non-null
		// instant. A mutation replacing the timestamp with a constant
		// ('2000-01-01T00:00:00Z') SURVIVED the version of this test that only checked
		// non-nil -- a value that is present, plausible, and completely wrong, which is
		// worse than one that is absent. "Dismissed on" has to mean when it actually
		// happened.
		//
		// The window is deliberately generous. RFC3339 stores whole seconds and this
		// assertion must not be a race against the clock; a minute either way is far
		// tighter than any constant substitution and still catches one.
		assert.WithinDuration(t, time.Now(), *first.ResolvedAt, time.Minute,
			"resolved_at must be the instant of the dismissal, not some other non-null time")

		// A second click, or a client retry.
		//
		// THE TIMESTAMP IS BACKDATED FIRST, and that is the whole point of this test.
		// Without it the assertion below is VACUOUS: sqlite.Timestamp stores RFC3339,
		// which is second-precision, so two Resolve calls in the same test are the same
		// instant and the assertion passes whether or not the `AND resolved = false`
		// guard exists. A mutation that deleted the guard SURVIVED the first version of
		// this test for exactly that reason.
		//
		// Moving resolved_at back by an hour first makes the guard's effect observable:
		// with the guard, a second Resolve leaves the backdated value alone; without it,
		// the second Resolve overwrites it with now. Writing the backdated value through
		// SQL rather than through Resolve is deliberate -- Resolve is the thing under
		// test.
		_, _, err = db.ExecSQL(ctx, `
			UPDATE issues SET resolved_at = ? WHERE id = ?`,
			[]interface{}{first.ResolvedAt.Add(-time.Hour), issue.ID})
		require.NoError(t, err)
		backdated, err := db.Issue.Find(ctx, issue.ID)
		require.NoError(t, err)
		require.NotNil(t, backdated)

		require.NoError(t, db.Issue.Resolve(ctx, issue.ID))
		second, err := db.Issue.Find(ctx, issue.ID)
		require.NoError(t, err)
		require.NotNil(t, second)

		assert.Equal(t, backdated.ResolvedAt, second.ResolvedAt,
			"a second resolve must not move resolved_at: the panel shows it as 'dismissed on', "+
				"and a date that drifts on every re-click is a date nobody believes")
		// The self-check, and it is a real assertion rather than a formality: it FAILED
		// on the first run with the comparison the wrong way round. The backdating moves
		// the value EARLIER, so the test that verifies the backdating landed has to
		// assert backdated < first, not first < backdated. A guard test that does not
		// check its own premise is how a vacuous one survives.
		assert.True(t, backdated.ResolvedAt.Before(*first.ResolvedAt),
			"the backdating must have actually moved the stored value EARLIER, or this "+
				"test is asserting nothing -- a guard test that cannot fail is decoration")
	})
}

// stash#837: the panel's DEFAULT is unresolved, and that default lives in the store.
//
// This is the test for the decision recorded in FindBy's doc comment. A filter
// that says nothing about `resolved` means "show me what needs attention"; a
// caller that wants the other list has to ask for it.
func TestTheDefaultQueryIsUnresolved(t *testing.T) {
	runWithRollbackTxn(t, "the default query returns unresolved issues only", func(t *testing.T, ctx context.Context) {
		f := mkIssueFile(t, ctx, "/library/837-default", "f.jpg")
		id := models.FileID(f.ID)

		live := &models.Issue{FileID: &id, Domain: models.IssueDomainFile, Kind: models.IssueKindZeroSize}
		require.NoError(t, db.Issue.Record(ctx, live))

		dismissed := &models.Issue{FileID: &id, Domain: models.IssueDomainFile, Kind: models.IssueKindDuplicate}
		require.NoError(t, db.Issue.Record(ctx, dismissed))
		require.NoError(t, db.Issue.Resolve(ctx, dismissed.ID))

		// No filter at all.
		out, err := db.Issue.FindBy(ctx, nil)
		require.NoError(t, err)
		require.Len(t, out, 1, "a nil filter means 'unresolved', not 'everything'")
		assert.Equal(t, models.IssueKindZeroSize, out[0].Kind)

		// An EMPTY filter means the same thing -- this is the case a caller hits by
		// passing &IssueFilterType{} from a form with nothing set, and it is the one
		// that would otherwise quietly show six months of dismissals.
		out, err = db.Issue.FindBy(ctx, &models.IssueFilterType{})
		require.NoError(t, err)
		require.Len(t, out, 1, "an EMPTY filter must also mean unresolved")

		// And asking for resolved explicitly works.
		out, err = db.Issue.FindBy(ctx, &models.IssueFilterType{Resolved: boolPtr(true)})
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, models.IssueKindDuplicate, out[0].Kind)

		n, err := db.Issue.CountUnresolved(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, n, "the panel's badge counts what needs attention, not the archive")
	})
}

// stash#837: the filters the panel actually offers, each of which must narrow.
func TestIssueFiltersNarrow(t *testing.T) {
	runWithRollbackTxn(t, "the panel's filters narrow", func(t *testing.T, ctx context.Context) {
		f := mkIssueFile(t, ctx, "/library/837-filters", "g.jpg")
		id := models.FileID(f.ID)
		for _, k := range []string{models.IssueKindDuplicate, models.IssueKindZeroSize, models.IssueKindZeroDuration} {
			require.NoError(t, db.Issue.Record(ctx, &models.Issue{
				FileID: &id, Domain: models.IssueDomainFile, Kind: k,
			}))
		}
		// A scan-level issue, to prove the domain filter separates them.
		require.NoError(t, db.Issue.Record(ctx, &models.Issue{
			Domain: models.IssueDomainScan, Kind: models.IssueKindNoFiles, Details: "scan 7 added nothing",
		}))

		all, err := db.Issue.FindBy(ctx, nil)
		require.NoError(t, err)
		assert.Len(t, all, 4)

		byDomain, err := db.Issue.FindBy(ctx, &models.IssueFilterType{Domain: strToPtr(models.IssueDomainScan)})
		require.NoError(t, err)
		require.Len(t, byDomain, 1, "the domain filter must narrow, not pass everything through")
		assert.Equal(t, models.IssueKindNoFiles, byDomain[0].Kind)

		byKind, err := db.Issue.FindBy(ctx, &models.IssueFilterType{Kind: strToPtr(models.IssueKindDuplicate)})
		require.NoError(t, err)
		require.Len(t, byKind, 1)
		assert.Equal(t, models.IssueDomainFile, byKind[0].Domain)

		// A filter that matches nothing returns nothing, not everything.
		none, err := db.Issue.FindBy(ctx, &models.IssueFilterType{Kind: strToPtr("no_such_kind")})
		require.NoError(t, err)
		assert.Empty(t, none, "a filter matching nothing must return nothing; a WHERE clause that "+
			"is dropped when it has no values is how a panel shows the whole library by accident")
	})
}

// stash#837: newest first, and the order is total (no ties left to chance).
func TestIssuesAreOrderedNewestFirst(t *testing.T) {
	runWithRollbackTxn(t, "issues come back newest first with a total order", func(t *testing.T, ctx context.Context) {
		f := mkIssueFile(t, ctx, "/library/837-order", "h.jpg")
		id := models.FileID(f.ID)
		var kinds []string
		for _, k := range []string{"k1", "k2", "k3", "k4"} {
			require.NoError(t, db.Issue.Record(ctx, &models.Issue{
				FileID: &id, Domain: models.IssueDomainFile, Kind: k,
			}))
			kinds = append(kinds, k)
		}
		out, err := db.Issue.FindBy(ctx, &models.IssueFilterType{FileID: &id})
		require.NoError(t, err)
		require.Len(t, out, 4)

		for i := 1; i < len(out); i++ {
			assert.False(t, out[i].DetectedAt.After(out[i-1].DetectedAt),
				"issues must come back newest first, but %v is newer than the row before it", out[i].Kind)
		}
		// `id DESC` as the tiebreak: these rows are recorded within the same clock
		// tick, so without it the order of a same-second batch is whatever SQLite
		// feels like -- which is exactly the "intermittently" shape of bug the
		// gallery-order fix ran into.
		assert.Equal(t, kinds[3], out[0].Kind, "the newest recorded finding must come first")
		assert.Equal(t, kinds[0], out[3].Kind, "and the oldest last")
	})
}

// A missing row is nil, nil -- not an error. Every other store in this package
// behaves that way and a caller that has to special-case issues would be odd.
func TestFindingAMissingIssueIsNotAnError(t *testing.T) {
	runWithRollbackTxn(t, "finding a missing issue returns nil, nil", func(t *testing.T, ctx context.Context) {
		got, err := db.Issue.Find(ctx, 999999)
		require.NoError(t, err, "a missing row is not a failure")
		assert.Nil(t, got)
	})
}

func boolPtr(b bool) *bool      { return &b }
func strToPtr(s string) *string { return &s }

// stash#837: Record IGNORES the caller's `resolved`, and the mutation that deleted
// `issue.Resolved = false` survived until this test existed.
//
// The reset looks redundant because the INSERT statement hardcodes `false` in SQL.
// It is not redundant: it is what the CALLER'S struct is left holding afterwards.
// A caller that reuses a struct it just filled in — the ordinary shape when a
// detector runs in a loop over one `issue` value — would otherwise carry
// `Resolved: true` from a previous file, and the struct is what a later
// `db.Issue.Record` or a log line or an API response reads.
func TestRecordIgnoresTheCallersResolvedFlag(t *testing.T) {
	runWithRollbackTxn(t, "Record ignores the caller's resolved flag", func(t *testing.T, ctx context.Context) {
		f := mkIssueFile(t, ctx, "/library/837-flag", "i.jpg")
		id := models.FileID(f.ID)

		// A caller that hands in a struct claiming to be already resolved -- e.g. one
		// built by copying a row it just read back.
		stale := time.Now().Add(-24 * time.Hour)
		resolvedAt := stale
		issue := &models.Issue{
			FileID:     &id,
			Domain:     models.IssueDomainFile,
			Kind:       models.IssueKindZeroSize,
			Resolved:   true,
			ResolvedAt: &resolvedAt,
		}
		require.NoError(t, db.Issue.Record(ctx, issue))

		assert.False(t, issue.Resolved,
			"Record must leave the caller's struct saying the finding is NOT resolved; a "+
				"caller reusing one struct across files would otherwise carry a stale "+
				"Resolved=true into the next Record and into anything that logs it")
		assert.Nil(t, issue.ResolvedAt,
			"and the stale ResolvedAt must be cleared with it, or the struct claims a "+
				"dismissal date for a finding that was never dismissed")

		// And the ROW agrees: one live issue, no dismissal recorded.
		assert.Equal(t, 1, countIssues(t, ctx, "file_id = ? AND resolved = false", int(f.ID)),
			"a fresh finding must be live in the database too")
		assert.Equal(t, 0, countIssues(t, ctx, "file_id = ? AND resolved = true", int(f.ID)),
			"and must not have been born dismissed: a caller-supplied flag must not be able "+
				"to hide a finding from the panel")
	})
}
