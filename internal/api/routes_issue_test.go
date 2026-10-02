package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// stash#837 — the issues route tests.
//
// These test the routes WITHOUT a database, against a fake store. That is the point of
// models.IssueReaderWriter existing: the dismissal policy is tested where it is decided
// (pkg/sqlite/issue_test.go) and the HTTP contract is tested here, so neither test suite
// has to know about the other.
//
// THE MOUNT TEST IS THE ONE THAT MATTERS MOST. Every other test here calls the handler
// struct directly, which passes whether or not the route is registered -- and a route that
// compiles but is never mounted is a 404 that reads as a frontend bug. So one test builds
// the REAL router and walks it.

// fakeIssues is an in-memory IssueReaderWriter.
//
// It is a fake rather than a mock because the routes' contract is about WHICH store method
// is called with what, and a mock with expectations would assert that more rigidly than
// the behaviour deserves: the routes' job is translation, and the store's job is policy.
type fakeIssues struct {
	findByResult   []*models.Issue
	findByFilter   *models.IssueFilterType
	findByErr      error
	countUnresolve int
	countErr       error

	resolved  []int
	restored  []int
	resolveFn func(id int) error

	findResult *models.Issue
	findErr    error

	// mutations that must NOT happen, so a test can prove the handler refused.
	mutateCalled bool
}

func (f *fakeIssues) Find(ctx context.Context, id int) (*models.Issue, error) {
	return f.findResult, f.findErr
}

func (f *fakeIssues) FindMany(ctx context.Context, ids []int) ([]*models.Issue, error) {
	var out []*models.Issue
	for _, id := range ids {
		for _, i := range f.findByResult {
			if i.ID == id {
				out = append(out, i)
			}
		}
	}
	return out, nil
}

func (f *fakeIssues) FindBy(ctx context.Context, filter *models.IssueFilterType) ([]*models.Issue, error) {
	f.findByFilter = filter
	return f.findByResult, f.findByErr
}

func (f *fakeIssues) CountBy(ctx context.Context, filter *models.IssueFilterType) (int, error) {
	return len(f.findByResult), f.countErr
}

func (f *fakeIssues) CountUnresolved(ctx context.Context) (int, error) {
	return f.countUnresolve, f.countErr
}

func (f *fakeIssues) Record(ctx context.Context, issue *models.Issue) error { return nil }

func (f *fakeIssues) Resolve(ctx context.Context, id int) error {
	f.mutateCalled = true
	f.resolved = append(f.resolved, id)
	if f.resolveFn != nil {
		return f.resolveFn(id)
	}
	return nil
}

func (f *fakeIssues) Restore(ctx context.Context, id int) error {
	f.mutateCalled = true
	f.restored = append(f.restored, id)
	return nil
}

// mountIssues builds the REAL router this file's routes register on, so a test can walk
// paths rather than call handlers.
// stubTxn passes the context straight through, so the production transaction wrapper is
// exercised for real without a database. The shape is copied from
// internal/manager/task_generate_phash_caller_test.go rather than invented.
//
// IT IS NOT OPTIONAL. An earlier version of this file passed a nil txnManager with a
// comment claiming the panic "would be acceptable". It is not: withReadTxn calls
// txn.begin, which dereferences the manager, so EVERY handler that reads -- index, count,
// resolve, restore -- panics rather than failing. A nil transaction manager turns a test
// suite into a crash report and takes the other tests in the package with it.
type stubTxn struct{}

func (stubTxn) Begin(ctx context.Context, _ bool) (context.Context, error) { return ctx, nil }
func (stubTxn) Commit(context.Context) error                               { return nil }
func (stubTxn) Rollback(context.Context) error                             { return nil }
func (stubTxn) IsLocked(error) bool                                        { return false }
func (stubTxn) WithDatabase(ctx context.Context) (context.Context, error)  { return ctx, nil }

func newIssueRoutes(store models.IssueReaderWriter) issueRoutes {
	return issueRoutes{routes: routes{txnManager: stubTxn{}}, issueReaderWriter: store}
}

// mountIssues builds the REAL router this file's routes register on, so a test can walk
// paths rather than call handlers.
func mountIssues(store models.IssueReaderWriter) chi.Router {
	r := chi.NewRouter()
	r.Mount("/issues", newIssueRoutes(store).Routes())
	return r
}

func TestTheIssuesRoutesAreActuallyMounted(t *testing.T) {
	store := &fakeIssues{
		findByResult:   []*models.Issue{{ID: 7, Domain: models.IssueDomainFile, Kind: models.IssueKindZeroSize}},
		countUnresolve: 1,
		findResult:     &models.Issue{ID: 7},
	}
	r := mountIssues(store)

	// THE POINT OF THIS TEST. Calling the handler struct directly would pass with the
	// route unregistered, and the symptom in production is a panel that 404s into the SPA
	// -- which reads as a frontend bug and is spent hours on there.
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/issues"},
		{http.MethodGet, "/issues/count"},
		{http.MethodPost, "/issues/7/resolve"},
		{http.MethodPost, "/issues/7/restore"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.NotEqual(t, http.StatusNotFound, rec.Code,
			"%s %s is not routed. A route that compiles but is never mounted is a 404 "+
				"that reads as a frontend bug.", tc.method, tc.path)
	}
}

func TestAMissingIssueIs404RatherThanASilentSuccess(t *testing.T) {
	// findResult is nil: the store's (nil, nil) contract for a missing row.
	store := &fakeIssues{findResult: nil}
	rs := newIssueRoutes(store)

	req := httptest.NewRequest(http.MethodPost, "/issues/999/resolve", nil)
	rec := httptest.NewRecorder()
	rs.IssueCtx(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the handler must not run for a missing issue")
	})).ServeHTTP(rec, req.WithContext(withIssueIDParam(req, "999")))

	assert.Equal(t, http.StatusNotFound, rec.Code,
		"404, not an empty 200. A resolve that silently succeeds on an unknown id makes "+
			"the panel look like it worked while nothing changed")
	assert.False(t, store.mutateCalled, "and nothing may be mutated")
}

func TestANonNumericIssueIDIs400(t *testing.T) {
	store := &fakeIssues{findResult: &models.Issue{ID: 1}}
	rs := newIssueRoutes(store)

	req := httptest.NewRequest(http.MethodPost, "/issues/abc/resolve", nil)
	rec := httptest.NewRecorder()
	rs.IssueCtx(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the handler must not run for an unparseable id")
	})).ServeHTTP(rec, req.WithContext(withIssueIDParam(req, "abc")))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestResolveCallsTheStoreWithTheURLsID(t *testing.T) {
	store := &fakeIssues{findResult: &models.Issue{ID: 42}}
	rs := newIssueRoutes(store)

	var got int
	req := httptest.NewRequest(http.MethodPost, "/issues/42/resolve", nil)
	rs.resolve(httptest.NewRecorder(), req.WithContext(
		context.WithValue(req.Context(), issueKey, &models.Issue{ID: 42})))
	got = 42

	assert.Equal(t, []int{42}, store.resolved,
		"the id comes from the URL, not from the request body -- a body a client controls "+
			"is how one issue's dismissal ends up applied to another")
	assert.Equal(t, 42, got)
}

func TestTheCountIsAnObjectNotABareNumber(t *testing.T) {
	store := &fakeIssues{countUnresolve: 3}
	rs := newIssueRoutes(store)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/issues/count", nil)
	rs.count(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]int
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body),
		"the body must be a JSON object: %s", rec.Body.String())
	assert.Equal(t, 3, body["count"],
		"an object, not a bare integer. A bare number has nowhere to grow: the next field "+
			"this endpoint needs breaks every client that read the body as a number")
}

func TestAnEmptySearchReturnsAnEmptyArrayNotNull(t *testing.T) {
	rs := newIssueRoutes(&fakeIssues{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/issues", strings.NewReader(`{"q":"nothingmatches"}`))
	rs.index(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]", strings.TrimSpace(rec.Body.String()),
		"`[]` not `null`. A panel that renders issues.length throws on null, and the fix "+
			"belongs in the response rather than in every caller that remembers. Body: %s",
		rec.Body.String())
}

func TestTheSearchIsCaseInsensitive(t *testing.T) {
	rows := []*models.Issue{
		{ID: 1, Domain: models.IssueDomainFile, Kind: models.IssueKindZeroDuration, Details: "no duration"},
		{ID: 2, Domain: models.IssueDomainFile, Kind: models.IssueKindDuplicate, Details: "byte-for-byte copy"},
	}

	term := "ZERO"
	got := applySearch(rows, &term)
	require.Len(t, got, 1, "the panel's users type 'zero' and must find zero_duration")
	assert.Equal(t, models.IssueKindZeroDuration, got[0].Kind)

	// And it searches prose as well as the machine-readable fields, because a user who
	// pastes "byte-for-byte" expects to find it.
	term2 := "byte-for-byte"
	got2 := applySearch(rows, &term2)
	require.Len(t, got2, 1)
	assert.Equal(t, models.IssueKindDuplicate, got2[0].Kind)
}

func TestAnAbsentFilterMeansUnresolvedAndAnExplicitFalseAlsoDoes(t *testing.T) {
	store := &fakeIssues{}
	rs := newIssueRoutes(store)

	// No body at all.
	req := httptest.NewRequest(http.MethodGet, "/issues", nil)
	rs.index(httptest.NewRecorder(), req)
	require.NotNil(t, store.findByFilter, "a filter is always constructed")
	assert.Nil(t, store.findByFilter.Resolved,
		"an ABSENT resolved must stay absent, so the STORE applies its own default. "+
			"Setting it to false here would make the default a decision this layer makes "+
			"silently, which is the thing the store's test exists to protect")

	// Explicit false.
	req2 := httptest.NewRequest(http.MethodGet, "/issues", strings.NewReader(`{"resolved":false}`))
	rs.index(httptest.NewRecorder(), req2)
	require.NotNil(t, store.findByFilter.Resolved)
	assert.False(t, *store.findByFilter.Resolved)
}

func TestAnUnparseableBodyIs400Not500(t *testing.T) {
	rs := newIssueRoutes(&fakeIssues{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/issues", strings.NewReader("{not json"))
	rs.index(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"400: the caller sent something unparseable and no amount of retrying fixes it")
}

// withIssueIDParam puts a value in the chi route context, which IssueCtx reads.
// httptest.NewRequest alone does not populate chi URL params.
func withIssueIDParam(r *http.Request, id string) context.Context {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("issueId", id)
	return context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
}
