package api

import (
	"context"
	"fmt"
	"strconv"

	"github.com/stashapp/stash/pkg/models"
)

// stash#837 — the GraphQL surface the panel uses.
//
// THE REST ROUTES EXIST AND THIS IS NOT ONE OF THEM, DELIBERATELY.
//
// internal/api/routes_issue.go is a complete, tested HTTP surface. The panel does not use
// it, because this UI has no REST convention: every list in components/ is an Apollo hook
// over a generated query, and the first fetch() in a component is the kind of thing that
// gets copied by whoever writes the next feature. GraphQL is where this codebase declares
// what a client may ask for, and generated-graphql.ts is produced from the schema, so a
// field here is a typed hook that cannot drift from the server.
//
// So both exist on purpose: REST serves non-GraphQL callers, GraphQL serves the panel.

// FindIssues returns findings for the panel.
//
// A NIL issue_filter MEANS UNRESOLVED, and the store is where that decision is made rather
// than here. It is tempting to resolve nil to "unresolved" at this layer so the schema
// reads as self-contained; doing so would duplicate a tested rule in a second place, and
// the two would drift the first time the default changed.
func (r *queryResolver) FindIssues(ctx context.Context, issueFilter *models.IssueFilterType, ids []string) (ret *FindIssuesResultType, err error) {
	idInts, err := handleIDList(ids, "ids")
	if err != nil {
		return nil, err
	}

	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		var issues []*models.Issue
		var total int
		var err error

		if len(idInts) > 0 {
			issues, err = r.repository.Issue.FindMany(ctx, idInts)
			total = len(issues)
		} else {
			issues, err = r.repository.Issue.FindBy(ctx, issueFilter)
			if err != nil {
				return err
			}
			// Counted BY THE FILTER, NOT len(issues). They are the same number today, and
			// they stop being the same the moment the panel paginates -- at which point a
			// count of len(page) is the page size wearing a total's label, and the panel
			// shows "12" for a library with 400 findings.
			total, err = r.repository.Issue.CountBy(ctx, issueFilter)
		}
		if err != nil {
			return err
		}

		ret = &FindIssuesResultType{Count: total, Issues: issues}
		return nil
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

// IssueCount is the panel's badge.
//
// A SEPARATE QUERY rather than findIssues(...).count, and deliberately UNFILTERED. The
// badge means "how many things want your attention"; reusing a filtered query's count
// makes it mean "how many this filter matched", so applying a filter silently changes what
// the badge claims -- and a badge that changes meaning under a filter is one nobody trusts.
func (r *queryResolver) IssueCount(ctx context.Context) (ret *IssueCountResultType, err error) {
	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		n, err := r.repository.Issue.CountUnresolved(ctx)
		if err != nil {
			return err
		}
		ret = &IssueCountResultType{Count: n}
		return nil
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

// IssueResolve dismisses a finding.
//
// TRUE MEANS "THE DESIRED STATE NOW HOLDS", not "a row changed". Resolve is idempotent --
// `WHERE id = ? AND resolved = false` -- so resolving an already-dismissed finding reports
// success. Returning false there would make a double-click an error, and an error the UI
// must special-case to mean "already done".
//
// A MISSING ID IS AN ERROR, unlike the resolve above: there is no desired state to report
// for a row that does not exist, and a caller that passed a stale id needs to know.
func (r *mutationResolver) IssueResolve(ctx context.Context, id string) (bool, error) {
	// strconv.Atoi, matching every other single-id mutation in this package
	// (resolver_mutation_file.go, resolver_mutation_gallery.go) rather than inventing a
	// helper for one caller.
	issueID, err := strconv.Atoi(id)
	if err != nil {
		return false, fmt.Errorf("converting issue id %q: %w", id, err)
	}

	var ret bool
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		var err error
		ret, err = r.mustGetIssue(ctx, issueID)
		if err != nil {
			return err
		}
		return r.repository.Issue.Resolve(ctx, issueID)
	}); err != nil {
		return false, err
	}

	return ret, nil
}

// IssueRestore undoes a dismissal.
//
// NOT Record. Record refuses to re-raise a dismissed finding -- that refusal is the whole
// point of a dismissal -- so the only way back is an explicit statement from the user.
// The scanner finding the same thing again is not the user changing their mind.
func (r *mutationResolver) IssueRestore(ctx context.Context, id string) (bool, error) {
	// strconv.Atoi, matching every other single-id mutation in this package
	// (resolver_mutation_file.go, resolver_mutation_gallery.go) rather than inventing a
	// helper for one caller.
	issueID, err := strconv.Atoi(id)
	if err != nil {
		return false, fmt.Errorf("converting issue id %q: %w", id, err)
	}

	var ret bool
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		var err error
		ret, err = r.mustGetIssue(ctx, issueID)
		if err != nil {
			return err
		}
		return r.repository.Issue.Restore(ctx, issueID)
	}); err != nil {
		return false, err
	}

	return ret, nil
}

// mustGetIssue loads the issue or fails the mutation.
//
// It exists so BOTH mutations refuse an unknown id identically. Doing the check in each
// resolver is how one of them ends up reporting success on a row that was never there --
// and that resolver is the one nobody tests, because "dismissing something that does not
// exist" looks like a no-op worth having.
func (r *mutationResolver) mustGetIssue(ctx context.Context, id int) (bool, error) {
	issue, err := r.repository.Issue.Find(ctx, id)
	if err != nil {
		return false, err
	}
	// Find returns (nil, nil) for a missing row -- a missing row is not a store failure --
	// so the nil check is what turns that into an error here.
	if issue == nil {
		return false, fmt.Errorf("issue with id %d not found", id)
	}
	return true, nil
}
