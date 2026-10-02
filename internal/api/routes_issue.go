package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/txn"
)

// stash#837 — the issues panel's HTTP surface.
//
//	GET  /issues                      the panel's list, unresolved by default
//	GET  /issues/count                the badge number
//	POST /issues/{issueId}/resolve    dismiss a finding
//	POST /issues/{issueId}/restore    undo a dismissal
//
// WHY HAND-WRITTEN REST AND NOT GRAPHQL YET. internal/api/generated_models.go is
// GENERATED (gqlgen), and hand-editing generated code produces a diff that the next `go
// generate` silently reverts -- so the panel would break for whoever ran it next. These
// routes survive regeneration. The GraphQL fields are plan step 7 and are meant to go in
// through the generator.
//
// THREE ROUTES THAT ARE NOT OBVIOUSLY NEEDED, BOTH HERE FOR THE REASON STATED:
//
//	restore -- a dismissal is a decision, and decisions are wrong. Without it, "I
//	          dismissed that by accident" is answered by editing the database, and the
//	          panel teaches people that dismissals are permanent.
//	count   -- the badge and the list are the same query rendered twice. Rendering
//	          them separately is how they come to disagree, so the badge comes from the
//	          STORE's own count rather than from len() of a paginated page.
type issueRoutes struct {
	routes

	issueReaderWriter models.IssueReaderWriter
}

func (s *Server) getIssueRoutes() chi.Router {
	repo := s.manager.Repository
	return issueRoutes{
		routes:            routes{txnManager: repo.TxnManager},
		issueReaderWriter: repo.Issue,
	}.Routes()
}

func (rs issueRoutes) Routes() chi.Router {
	r := chi.NewRouter()

	r.Get("/", rs.index)
	r.Get("/count", rs.count)

	r.Route("/{issueId}", func(r chi.Router) {
		r.Use(rs.IssueCtx)
		r.Post("/resolve", rs.resolve)
		r.Post("/restore", rs.restore)
	})

	return r
}

// writeIssueJSON is a LOCAL json writer, not a shared helper.
//
// Deliberately scoped to this file. The package's other handlers each call
// json.NewEncoder directly (see bool_map.go, stashforge_wizard.go) rather than sharing one
// helper, and introducing a package-wide writeJSON would be a change to every caller's
// conventions to serve one new file -- a wider edit than the feature needs, and one whose
// failure mode is silent: a shared helper that swallows errors would turn every route's
// encoding failure into a 200 with a truncated body.
//
// THE ERROR IS LOGGED, NOT SWALLOWED AND NOT REFLECTED. Once the header is written the
// status is fixed, so there is nothing to tell the client; a log line is the only place
// the information can still go.
func writeIssueJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Errorf("stash#837: encoding issue response failed: %v", err)
	}
}

// indexParams is the filter the panel's controls bind to.
//
// EVERY FIELD IS A POINTER, so "absent" is distinguishable from "zero". `resolved=false`
// is a request for live findings; an absent `resolved` is a request for the default.
// Collapsing them into a plain bool would mean the panel cannot ask for resolved issues
// without also being handed the unresolved ones -- and the default (unresolved) is the
// store's decision, not this layer's, precisely so it can be a tested one.
type indexParams struct {
	Domain   *string `json:"domain"`
	Kind     *string `json:"kind"`
	FileID   *int    `json:"file_id"`
	Resolved *bool   `json:"resolved"`
	Search   *string `json:"q"`
}

func (rs issueRoutes) index(w http.ResponseWriter, r *http.Request) {
	var p indexParams
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil && !errors.Is(err, io.EOF) {
			// 400, not 500: the caller sent something unparseable and no amount of
			// retrying will fix it. An empty body is not an error -- a GET with no body
			// means "no filter".
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
	}

	var result []*models.Issue
	readTxnErr := rs.withReadTxn(r, func(ctx context.Context) error {
		filter := &models.IssueFilterType{
			Domain:   p.Domain,
			Kind:     p.Kind,
			Resolved: p.Resolved,
		}
		if p.FileID != nil {
			id := models.FileID(*p.FileID)
			filter.FileID = &id
		}

		var err error
		result, err = rs.issueReaderWriter.FindBy(ctx, filter)
		return err
	})
	if readTxnErr != nil && !errors.Is(readTxnErr, context.Canceled) {
		// A cancelled read is the user navigating away, not a failure to report.
		logger.Warnf("read transaction error on fetch issues: %v", readTxnErr)
	}

	writeIssueJSON(w, applySearch(result, p.Search))
}

// applySearch filters in memory.
//
// The search box is a substring filter over a set bounded by the number of files WITH
// PROBLEMS, not by the library. Pushing it into SQL would mean a LIKE across every issue
// on every keystroke for a list nobody scrolls past a few hundred rows -- and the store's
// contract is filter-then-find with no text term, so this would be the one caller not
// using the store's own query, which is the shape that later gets copied by mistake.
//
// Case-INSENSITIVE via strings.ToLower on both sides, because the panel's users type
// "zero" and expect to find `zero_duration`. The allocation is irrelevant at this size and
// a hand-rolled case-fold is a second thing to get wrong.
func applySearch(issues []*models.Issue, term *string) []*models.Issue {
	if term == nil || *term == "" {
		return issues
	}
	t := strings.ToLower(*term)

	out := make([]*models.Issue, 0, len(issues))
	for _, i := range issues {
		if i == nil {
			continue
		}
		if strings.Contains(strings.ToLower(i.Details), t) ||
			strings.Contains(strings.ToLower(i.Kind), t) ||
			strings.Contains(strings.ToLower(i.Domain), t) {
			out = append(out, i)
		}
	}
	// An EMPTY SLICE, NEVER NIL, so the JSON is `[]` rather than `null`. A panel that
	// renders `(issues ?? []).length` is fine either way, but one that renders
	// `issues.length` throws on null, and the fix belongs here rather than in every
	// caller that happens to remember.
	return out
}

func (rs issueRoutes) count(w http.ResponseWriter, r *http.Request) {
	var n int
	readTxnErr := rs.withReadTxn(r, func(ctx context.Context) error {
		var err error
		n, err = rs.issueReaderWriter.CountUnresolved(ctx)
		return err
	})
	if readTxnErr != nil && !errors.Is(readTxnErr, context.Canceled) {
		logger.Warnf("read transaction error on count issues: %v", readTxnErr)
	}

	// ALWAYS AN OBJECT, NEVER A BARE NUMBER. A bare integer has nowhere to grow: the next
	// field this endpoint needs breaks every client that read the body as a number.
	writeIssueJSON(w, map[string]int{"count": n})
}

func (rs issueRoutes) resolve(w http.ResponseWriter, r *http.Request) {
	issue := r.Context().Value(issueKey).(*models.Issue)
	rs.mutate(w, r, func(ctx context.Context) error {
		return rs.issueReaderWriter.Resolve(ctx, issue.ID)
	})
}

func (rs issueRoutes) restore(w http.ResponseWriter, r *http.Request) {
	issue := r.Context().Value(issueKey).(*models.Issue)
	rs.mutate(w, r, func(ctx context.Context) error {
		return rs.issueReaderWriter.Restore(ctx, issue.ID)
	})
}

// mutate is the write path shared by resolve and restore.
//
// A WRITE transaction, unlike the reads. A dismissal that is not committed is a dismissal
// the user believes they made and that reappears on the next scan -- which is how a panel
// teaches people not to trust it.
func (rs issueRoutes) mutate(w http.ResponseWriter, r *http.Request, fn txn.TxnFunc) {
	if err := txn.WithTxn(r.Context(), rs.txnManager, fn); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeIssueJSON(w, nil)
}

// IssueCtx loads the issue named in the URL, or refuses the request.
//
// 404 FOR A MISSING ISSUE, NOT AN EMPTY 200. A resolve that silently succeeds on an
// unknown id makes the panel look like it worked while nothing changed, which is the
// failure a user cannot diagnose.
//
// The store returns (nil, nil) for a missing row -- a missing row is not a store failure
// -- so the nil check HERE is what turns that into a 404. Reading it as an error instead
// would make every store change to that contract a behaviour change in the API.
func (rs issueRoutes) IssueCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issueID, err := strconv.Atoi(chi.URLParam(r, "issueId"))
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}

		var issue *models.Issue
		readTxnErr := rs.withReadTxn(r, func(ctx context.Context) error {
			var err error
			issue, err = rs.issueReaderWriter.Find(ctx, issueID)
			return err
		})
		if readTxnErr != nil {
			if errors.Is(readTxnErr, context.Canceled) {
				return
			}
			http.Error(w, readTxnErr.Error(), http.StatusInternalServerError)
			return
		}
		if issue == nil {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}

		ctx := context.WithValue(r.Context(), issueKey, issue)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
