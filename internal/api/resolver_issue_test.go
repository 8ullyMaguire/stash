package api

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
)

// stash#837 — the GraphQL resolvers the panel actually calls.
//
// THE MOCK IS models/mocks.Database's IssueReaderWriter, NOT a hand-written fake. That is
// deliberate: the resolver tests and the route tests use different seams on purpose, and
// this one exercises the real gqlgen-dispatched path with the package's own mocking
// convention (see resolver_mutation_tag_test.go).
//
// What these tests are FOR, since the store's own tests already cover the policy:
// that the resolvers translate -- filter shape, count semantics, the missing-id rule --
// without re-deciding any of it.

// issueCtx is the same context the other resolver tests use (resolver_mutation_tag_test.go
// declares it). Referenced rather than redeclared so there is one.
var _ = context.Background

func newIssueResolver(db *mocks.Database) *Resolver {
	r := newResolver(db)
	return r
}

func TestFindIssuesReturnsBothRowsAndTheFilteredCount(t *testing.T) {
	db := mocks.NewDatabase()
	r := newIssueResolver(db)

	rows := []*models.Issue{
		{ID: 1, Domain: models.IssueDomainFile, Kind: models.IssueKindZeroSize},
		{ID: 2, Domain: models.IssueDomainFile, Kind: models.IssueKindDuplicate},
	}
	db.Issue.On("FindBy", mock.Anything, mock.Anything).Return(rows, nil)
	db.Issue.On("CountBy", mock.Anything, mock.Anything).Return(2, nil)

	got, err := r.Query().FindIssues(testCtx, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 2, got.Count)
	assert.Len(t, got.Issues, 2)
}

func TestFindIssuesCountsByTheFilterNotByTheRowsReturned(t *testing.T) {
	db := mocks.NewDatabase()
	r := newIssueResolver(db)

	// A page of 1, a total of 340. The count MUST come from CountBy: a resolver that says
	// len(issues) reports the page size wearing a total's label, and the panel then shows
	// "1" for a library with 340 findings -- which is exactly the bug that only appears
	// once pagination exists, so it is asserted now rather than then.
	db.Issue.On("FindBy", mock.Anything, mock.Anything).
		Return([]*models.Issue{{ID: 1}}, nil)
	db.Issue.On("CountBy", mock.Anything, mock.Anything).Return(340, nil)

	got, err := r.Query().FindIssues(testCtx, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 340, got.Count, "the total, not the page size")
	assert.Len(t, got.Issues, 1)
}

func TestFindIssuesPassesANilFilterThroughUntouched(t *testing.T) {
	db := mocks.NewDatabase()
	r := newIssueResolver(db)

	db.Issue.On("FindBy", mock.Anything, mock.MatchedBy(func(f *models.IssueFilterType) bool {
		return f == nil
	})).Return(nil, nil)
	db.Issue.On("CountBy", mock.Anything, mock.Anything).Return(0, nil)

	_, err := r.Query().FindIssues(testCtx, nil, nil)
	require.NoError(t, err)
	// The resolver must NOT resolve nil to &IssueFilterType{Resolved: false}. The default
	// is the store's tested decision; duplicating it here is how the two drift the first
	// time it changes.
	db.Issue.AssertCalled(t, "FindBy", mock.Anything, (*models.IssueFilterType)(nil))
}

func TestFindIssuesByIdsSkipsTheCountQuery(t *testing.T) {
	db := mocks.NewDatabase()
	r := newIssueResolver(db)

	db.Issue.On("FindMany", mock.Anything, []int{5, 6}).
		Return([]*models.Issue{{ID: 5}, {ID: 6}}, nil)

	got, err := r.Query().FindIssues(testCtx, nil, []string{"5", "6"})
	require.NoError(t, err)
	assert.Equal(t, 2, got.Count, "count is the number found when the caller named the ids")
	db.Issue.AssertNotCalled(t, "CountBy", mock.Anything, mock.Anything)
}

func TestFindIssuesRejectsAnUnparseableID(t *testing.T) {
	db := mocks.NewDatabase()
	r := newIssueResolver(db)

	_, err := r.Query().FindIssues(testCtx, nil, []string{"not-a-number"})
	require.Error(t, err, "a bad id in the list is an error, not a silently skipped row")
}

func TestIssueCountIsTheUnresolvedTotalAndIsNeverFiltered(t *testing.T) {
	db := mocks.NewDatabase()
	r := newIssueResolver(db)

	db.Issue.On("CountUnresolved", mock.Anything).Return(12, nil)

	got, err := r.Query().IssueCount(testCtx)
	require.NoError(t, err)
	assert.Equal(t, 12, got.Count)
	// And nothing about a filter reaches it. A badge that changes meaning when the user
	// applies a filter is a badge nobody trusts.
	db.Issue.AssertCalled(t, "CountUnresolved", mock.Anything)
	db.Issue.AssertNotCalled(t, "CountBy", mock.Anything, mock.Anything)
}

func TestIssueResolveCallsTheStoreForAnExistingIssue(t *testing.T) {
	db := mocks.NewDatabase()
	r := newIssueResolver(db)

	db.Issue.On("Find", mock.Anything, 3).Return(&models.Issue{ID: 3}, nil)
	db.Issue.On("Resolve", mock.Anything, 3).Return(nil)

	ok, err := r.Mutation().IssueResolve(testCtx, "3")
	require.NoError(t, err)
	assert.True(t, ok)
	db.Issue.AssertCalled(t, "Resolve", mock.Anything, 3)
}

func TestIssueResolveOnAMissingIssueIsAnErrorNotASilentSuccess(t *testing.T) {
	db := mocks.NewDatabase()
	r := newIssueResolver(db)

	// Find returns (nil, nil) for a missing row -- the store's contract, not a failure.
	db.Issue.On("Find", mock.Anything, 999).Return(nil, nil)

	ok, err := r.Mutation().IssueResolve(testCtx, "999")
	require.Error(t, err,
		"there is no desired state to report for a row that does not exist, and a caller "+
			"that passed a stale id needs to know")
	assert.False(t, ok)
	db.Issue.AssertNotCalled(t, "Resolve", mock.Anything, mock.Anything)
}

func TestIssueRestoreGoesThroughRestoreAndNotRecord(t *testing.T) {
	db := mocks.NewDatabase()
	r := newIssueResolver(db)

	db.Issue.On("Find", mock.Anything, 4).Return(&models.Issue{ID: 4}, nil)
	db.Issue.On("Restore", mock.Anything, 4).Return(nil)

	ok, err := r.Mutation().IssueRestore(testCtx, "4")
	require.NoError(t, err)
	assert.True(t, ok)
	db.Issue.AssertCalled(t, "Restore", mock.Anything, 4)
	// Record REFUSES to re-raise a dismissed finding -- that refusal is what a dismissal
	// is -- so restoring through it would silently do nothing and the panel would appear to
	// work.
	db.Issue.AssertNotCalled(t, "Record", mock.Anything, mock.Anything)
}

func TestIssueRestoreOnAMissingIssueIsAnError(t *testing.T) {
	db := mocks.NewDatabase()
	r := newIssueResolver(db)

	db.Issue.On("Find", mock.Anything, 77).Return(nil, nil)

	ok, err := r.Mutation().IssueRestore(testCtx, "77")
	require.Error(t, err)
	assert.False(t, ok)
	db.Issue.AssertNotCalled(t, "Restore", mock.Anything, mock.Anything)
}

func TestAMutationOnANonNumericIDIsAnErrorBeforeTheStoreIsTouched(t *testing.T) {
	db := mocks.NewDatabase()
	r := newIssueResolver(db)

	_, err := r.Mutation().IssueResolve(testCtx, "abc")
	require.Error(t, err)
	_, err = r.Mutation().IssueRestore(testCtx, "abc")
	require.Error(t, err)

	// Nothing may be called: a resolver that reaches the store before parsing the id can
	// resolve id 0, and 0 is a real row id.
	db.Issue.AssertNotCalled(t, "Resolve", mock.Anything, mock.Anything)
	db.Issue.AssertNotCalled(t, "Restore", mock.Anything, mock.Anything)
	db.Issue.AssertNotCalled(t, "Find", mock.Anything, mock.Anything)
}

func TestAStoreFailureIsPropagatedRatherThanSwallowed(t *testing.T) {
	db := mocks.NewDatabase()
	r := newIssueResolver(db)

	db.Issue.On("Find", mock.Anything, 5).Return(nil, errors.New("database is gone"))

	ok, err := r.Mutation().IssueResolve(testCtx, "5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database is gone")
	assert.False(t, ok)
}
