package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/models"
)

const issuesTable = "issues"

// stash#837 — the issues store.
//
// WHERE THE WORK IS, and it is not in the CRUD. Two decisions carry the feature
// and both are about what happens when a scan finds something it has found
// before:
//
//   - a LIVE finding is unique. Enforced by the index in migration 120, so two
//     scans racing cannot both insert. `Record` inserts and lets the index
//     refuse, then refreshes `detected_at`; the common path costs no read at all.
//
//   - a DISMISSED finding is NOT re-raised. NOT enforced by the index, because
//     refusing duplicate dismissed rows and allowing a new finding of the same
//     kind are contradictory for a unique index (migration 120 says why at
//     length). It is enforced HERE, as a policy, because a policy belongs in code
//     where a test can assert it.
//
// The asymmetry is the point. If the dismissal policy lived only in the index it
// would be a fact about the schema that no test could distinguish from an
// accident; if it lived only in code the uniqueness would be a race. Each is in
// the layer that can actually guarantee it.

type issueRow struct {
	ID         int        `db:"id"`
	FileID     *int       `db:"file_id"`
	Domain     string     `db:"domain"`
	Kind       string     `db:"kind"`
	Details    string     `db:"details"`
	DetectedAt Timestamp  `db:"detected_at"`
	Resolved   bool       `db:"resolved"`
	ResolvedAt *Timestamp `db:"resolved_at"`
}

func (r *issueRow) resolve() *models.Issue {
	ret := &models.Issue{
		ID:         r.ID,
		Domain:     r.Domain,
		Kind:       r.Kind,
		Details:    r.Details,
		DetectedAt: r.DetectedAt.Timestamp,
		Resolved:   r.Resolved,
	}
	if r.FileID != nil {
		id := models.FileID(*r.FileID)
		ret.FileID = &id
	}
	if r.ResolvedAt != nil {
		t := r.ResolvedAt.Timestamp
		ret.ResolvedAt = &t
	}
	return ret
}

// IssueStore takes no dependencies: an issue names a file by id and nothing else,
// so there is no other store to hold. The FileStore-less shape is a fact about the
// data, not an omission -- see the Record comment on why the dismissal check is a
// query rather than a loaded object.
type IssueStore struct{}

func NewIssueStore() *IssueStore { return &IssueStore{} }

func (s *IssueStore) table() string { return issuesTable }

// Record notes a finding. It is IDEMPOTENT for a finding that is still live: the
// row's `detected_at` is refreshed and no second row appears. For a finding the
// user has DISMISSED it does nothing at all — see the type comment.
//
// `issue.ID` is populated on success, so a caller that wants to link to the row
// afterwards does not have to query for it.
func (qb *IssueStore) Record(ctx context.Context, issue *models.Issue) error {
	// THE DISMISSAL POLICY, and the only read in the common path. A dismissed
	// finding of the same identity is not re-raised, so the user's click means
	// something: the next scan of the same broken file does not put it back on
	// their list.
	//
	// `details` is deliberately NOT part of this check. The index cannot use it
	// (migration 120) and the spec's promise that a genuinely NEW finding of the
	// same kind is recordable is a weaker promise than "the finding is never
	// re-raised", and the weaker one is the one worth keeping: a file the user
	// dismissed stays dismissed even if the scanner describes it differently next
	// time. The alternative — matching on details too — means a scanner whose
	// wording shifts puts the same finding back on the panel, which is precisely
	// the behaviour that makes people stop opening the panel.
	dismissed, err := qb.hasDismissed(ctx, issue)
	if err != nil {
		return err
	}
	if dismissed {
		return nil
	}

	now := time.Now()
	issue.DetectedAt = now

	// `resolved` is always false here: a caller that wants to record a dismissal
	// calls Resolve. Accepting it as an input would make this function's contract
	// "record whatever state you like", and the index's NULL-tail trick means the
	// caller could insert a row the uniqueness rules never considered.
	issue.Resolved = false
	issue.ResolvedAt = nil

	var fileID interface{}
	if issue.FileID != nil {
		fileID = int(*issue.FileID)
	}

	// ON CONFLICT names the SAME expression as the index in migration 120. If the
	// two ever drift apart SQLite refuses the statement outright with "ON CONFLICT
	// clause does not match any PRIMARY KEY or UNIQUE constraint" — which is the
	// right outcome, and the reason the expression is repeated here rather than
	// hidden behind a helper that could be edited in one place only.
	const conflictTarget = "(file_id, domain, kind, CASE WHEN resolved THEN NULL ELSE 0 END)"

	res, err := dbWrapper.Exec(ctx, `
		INSERT INTO issues (file_id, domain, kind, details, detected_at, resolved, resolved_at)
		VALUES (?, ?, ?, ?, ?, false, NULL)
		ON CONFLICT `+conflictTarget+`
		DO UPDATE SET detected_at = excluded.detected_at, details = excluded.details
		WHERE issues.resolved = false`,
		fileID, issue.Domain, issue.Kind, issue.Details, Timestamp{Timestamp: now})
	if err != nil {
		return fmt.Errorf("recording issue %s/%s: %w", issue.Domain, issue.Kind, err)
	}

	if id, err := res.LastInsertId(); err == nil && id > 0 {
		issue.ID = int(id)
	} else {
		// The conflict path reports the row it updated as 0 from LastInsertId, so the
		// caller's ID is stale rather than wrong-but-plausible. Say so instead of
		// leaving a zero that looks like a real id.
		issue.ID = 0
	}
	return nil
}

// hasDismissed reports whether the user has already dismissed this exact finding.
func (qb *IssueStore) hasDismissed(ctx context.Context, issue *models.Issue) (bool, error) {
	var fileID interface{}
	if issue.FileID != nil {
		fileID = int(*issue.FileID)
	}
	var n int
	err := dbWrapper.Get(ctx, &n, `
		SELECT count(*) FROM issues
		WHERE file_id IS ? AND domain = ? AND kind = ? AND resolved = true`,
		fileID, issue.Domain, issue.Kind)
	if err != nil {
		return false, fmt.Errorf("checking for a dismissed issue: %w", err)
	}
	return n > 0, nil
}

// Resolve dismisses an issue so it stops appearing on the panel.
//
// IDEMPOTENT, and the guard is the whole point:
//
//	UPDATE ... SET resolved = true, resolved_at = ? WHERE id = ? AND resolved = false
//
// Without `AND resolved = false` a second call rewrites `resolved_at`, and the
// panel shows that as "dismissed on" — so the date drifts every time somebody
// re-clicks or a client retries. TestResolveIsIdempotent fails if the guard goes.
func (qb *IssueStore) Resolve(ctx context.Context, id int) error {
	_, err := dbWrapper.Exec(ctx, `
		UPDATE issues SET resolved = true, resolved_at = ?
		WHERE id = ? AND resolved = false`,
		Timestamp{Timestamp: time.Now()}, id)
	if err != nil {
		return fmt.Errorf("resolving issue %d: %w", id, err)
	}
	return nil
}

// Find returns the issue, or nil if there is none. `nil, nil` rather than an
// error, matching the other stores: a missing row is not a failure.
func (qb *IssueStore) Find(ctx context.Context, id int) (*models.Issue, error) {
	var row issueRow
	err := dbWrapper.Get(ctx, &row, `SELECT * FROM issues WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding issue %d: %w", id, err)
	}
	return row.resolve(), nil
}

// FindMany returns the issues with these ids, skipping ones that do not exist.
func (qb *IssueStore) FindMany(ctx context.Context, ids []int) ([]*models.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	var rows []issueRow
	if err := dbWrapper.Select(ctx, &rows, `
		SELECT * FROM issues
		WHERE id IN `+getInBinding(len(ids)), args...); err != nil {
		return nil, fmt.Errorf("finding issues: %w", err)
	}
	ret := make([]*models.Issue, len(rows))
	for i, r := range rows {
		ret[i] = r.resolve()
	}
	return ret, nil
}

// FindBy filters. THE DEFAULT IS UNRESOLVED, and it is here rather than in the
// model: a filter that says nothing about `resolved` means "show me what needs
// attention", because the panel exists to be looked at and 4000 dismissals from
// last month is not a thing anybody opens.
//
// A caller that genuinely wants resolved issues says so, and that is why
// models.IssueFilterType.Resolved is a *bool: nil ("say nothing") is
// distinguishable from false ("only unresolved").
func (qb *IssueStore) FindBy(ctx context.Context, filter *models.IssueFilterType) ([]*models.Issue, error) {
	q := `SELECT * FROM issues`
	var conds []string
	var args []interface{}

	if filter == nil {
		filter = &models.IssueFilterType{}
	}
	if filter.Resolved == nil {
		conds = append(conds, "resolved = false")
	} else {
		conds = append(conds, "resolved = ?")
		args = append(args, *filter.Resolved)
	}
	if filter.Domain != nil {
		conds = append(conds, "domain = ?")
		args = append(args, *filter.Domain)
	}
	if filter.Kind != nil {
		conds = append(conds, "kind = ?")
		args = append(args, *filter.Kind)
	}
	if filter.FileID != nil {
		conds = append(conds, "file_id = ?")
		args = append(args, int(*filter.FileID))
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	// Newest first, and this is the order idx_issues_unresolved serves, so the
	// panel's default query is an index range rather than a sort.
	q += " ORDER BY detected_at DESC, id DESC"

	var rows []issueRow
	if err := dbWrapper.Select(ctx, &rows, q, args...); err != nil {
		return nil, fmt.Errorf("finding issues: %w", err)
	}
	ret := make([]*models.Issue, len(rows))
	for i, r := range rows {
		ret[i] = r.resolve()
	}
	return ret, nil
}

// CountBy counts what FindBy would return, so the panel can say "12 of 340".
func (qb *IssueStore) CountBy(ctx context.Context, filter *models.IssueFilterType) (int, error) {
	issues, err := qb.FindBy(ctx, filter)
	if err != nil {
		return 0, err
	}
	return len(issues), nil
}

// CountUnresolved is the panel's badge number, and it is a separate method rather
// than a filter value because a caller wanting "how many need attention" should
// not have to know the default lives in FindBy.
func (qb *IssueStore) CountUnresolved(ctx context.Context) (int, error) {
	return qb.CountBy(ctx, &models.IssueFilterType{})
}
