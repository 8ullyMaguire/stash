package models

import "context"

// stash#837 — the issues repository contract.
//
// The interface exists so the API layer can be written against a contract rather than
// against the SQLite store, which is what makes the route handlers testable without a
// database and is the same reason every other store in this package has one
// (see repository_scene.go).
//
// DELIBERATELY SMALL. Every method here is something the panel actually calls. A method
// added "in case" is a method the store has to keep working for a caller that does not
// exist, and the dismissal policy in particular is easy to get subtly wrong: an
// interface that exposes Record without exposing that Record refuses dismissed rows would
// let a future caller bypass the rule this feature is built on.
type IssueReader interface {
	// Find returns the issue, or nil if there is none. A missing row is not an error --
	// the API turns nil into a 404, so the distinction is preserved without this
	// signature having to carry it.
	Find(ctx context.Context, id int) (*Issue, error)
	FindMany(ctx context.Context, ids []int) ([]*Issue, error)

	// FindBy takes a nil filter as "unresolved", not "everything". The default lives in
	// the store rather than here precisely so that it is a tested decision rather than a
	// nil check every caller has to remember.
	FindBy(ctx context.Context, filter *IssueFilterType) ([]*Issue, error)

	CountBy(ctx context.Context, filter *IssueFilterType) (int, error)
	CountUnresolved(ctx context.Context) (int, error)
}

type IssueWriter interface {
	// Record writes a finding. It is idempotent for a live finding and REFUSES to
	// re-raise one the user has already dismissed.
	Record(ctx context.Context, issue *Issue) error

	// Resolve dismisses a finding. Restore undoes it, and only Restore: a re-record is
	// deliberately not a way back, because "the scanner said so again" is not the user
	// changing their mind.
	Resolve(ctx context.Context, id int) error
	Restore(ctx context.Context, id int) error
}

// IssueReaderWriter provides all issue methods.
type IssueReaderWriter interface {
	IssueReader
	IssueWriter
}
