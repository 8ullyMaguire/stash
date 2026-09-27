package collab

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The library as a DOMAIN object.
//
// It lives here rather than in pkg/sqlite for the same reason MediaScope does:
// the gate, the grant table and the owner-facing list all need to agree on what
// a library IS, and three sqlx row structs is three definitions that drift. A
// library's identity is (owner, name) and its sharing properties are is_private
// and is_default -- and the subtle one, is_private's NULL, has nowhere to live
// in a struct that flattens it to a bool.
//
// # WHY isPrivate IS A *bool AND NOT A bool
//
// Three states, and the third is the one that gets lost:
//
//   - true   explicitly excluded from the commons
//   - false  explicitly included
//   - NULL   defer to the owner's consent row
//
// A bool cannot hold the third, so a bool field forces a choice at the storage
// boundary -- and the choice that gets made is `false`, because zero values are
// what structs are. That silently publishes a library whose owner never decided.
// §6.1's default is the publishing one, so the mistake costs privacy on a
// library nobody opted into, and it is invisible: the column reads NULL and the
// Go field reads false, and only a diff between the two notices.

// Library is one user's sharing scope. Per-user, NOT an instance-wide folder:
// migration 101 makes libraries.user_id NOT NULL REFERENCES users(id) with
// UNIQUE (user_id, name).
type Library struct {
	ID   int64
	Name string

	// IsPrivate nil means "defer to the owner's consent". See the file comment
	// for why the third state has to be representable.
	IsPrivate *bool

	// IsDefault marks the library a row with no library_id resolves to. At most
	// one per user, enforced by a partial unique index, because two defaults
	// make "which library is this file in" a question with two answers.
	IsDefault bool

	// OwnerID is who this library belongs to. Every checked read compares
	// against it, so it is on the domain object rather than only in the store.
	OwnerID int64

	// GranteeCount is how many users hold a grant. Filled by the listing, not
	// by the single read: a count is a question the list asks, not a property
	// the row carries.
	GranteeCount int
}

// IsShared reports whether this library's metadata may be published.
//
// It RESOLVES the null case rather than exposing it, because every caller wants
// the answer and none of them should re-implement the resolution order. The
// resolution: an explicit is_private wins; otherwise the owner's consent row
// decides, and an owner's row that does not exist defaults to opted-IN (§6.1) --
// which is the single most consequential default in the project, and is why it
// lives in one function rather than at each call site.
func (l *Library) IsShared(consentStore ConsentStore, ctx context.Context) (bool, error) {
	if l.IsPrivate != nil {
		return !*l.IsPrivate, nil
	}
	if consentStore == nil {
		// No consent store: nothing to defer TO, so the question cannot be
		// answered. Refuse rather than guess, and notice that this is the
		// opposite of the absent-row default in ShareOptedIn. That is
		// deliberate and is the one asymmetry in the consent rules: an absent
		// ROW means opted in, because §6.1 chose it, while an absent STORE means
		// the instance cannot answer, and guessing there would publish on a
		// build that has no sharing logic at all.
		return false, errors.New("cannot resolve library privacy: this build has no consent store")
	}
	return ShareOptedIn(ctx, consentStore, l.OwnerID)
}

// SetPrivate records an explicit decision, or clears it back to "defer".
//
// Clearing is a first-class operation rather than "set false": there is a real
// state where an owner has overridden the library default and wants the owner's
// own consent row to decide again, and a method that can only set the flag
// cannot express it.
func (l *Library) SetPrivate(private bool) {
	l.IsPrivate = &private
}

// ClearPrivate returns the library to "defer to the owner's consent".
func (l *Library) ClearPrivate() { l.IsPrivate = nil }

// OwnedBy reports whether the named user owns this library.
//
// A method rather than a comparison at the call site, because the call site is
// where it gets forgotten: every path from a caller-supplied libraryId to a
// library's contents passes through this, and a check that is repeated at each
// site is a check that is eventually omitted.
func (l *Library) OwnedBy(userID int64) bool { return l != nil && l.OwnerID == userID }

// LibraryList is an ordered set of libraries.
//
// Sorted at construction so the order is a property of the type rather than of
// whichever query produced it. A list that reorders between two reads makes a
// "nothing changed" diff report a change that did not happen, and this list is
// both rendered as rows and used as an audit view.
type LibraryList []*Library

func (l LibraryList) Len() int           { return len(l) }
func (l LibraryList) Swap(i, j int)      { l[i], l[j] = l[j], l[i] }
func (l LibraryList) Less(i, j int) bool { return l[i].ID < l[j].ID }

// Sort orders the list by id. Used by the store, which gets rows from a query
// that already has an ORDER BY -- and the belt-and-braces is because a query's
// ORDER BY is a promise a later edit can drop, while this is checked by the test
// that asserts the order.
func (l LibraryList) Sort() LibraryList {
	sort.Sort(l)
	return l
}

// FindLibrary returns the library with the given id, or nil.
//
// Nil rather than an error, because "not in this list" is an ordinary answer to
// a search and the caller usually has a second thing to try.
func (l LibraryList) FindLibrary(id int64) *Library {
	for _, lib := range l {
		if lib != nil && lib.ID == id {
			return lib
		}
	}
	return nil
}

// Default returns the user's default library, or nil when they have none.
//
// Nil is meaningful rather than an edge case: a user with no default library has
// no owner for a row with no library_id, and the media gate refuses those. So
// this returning nil is a fact the caller has to handle, not an empty result.
func (l LibraryList) Default() *Library {
	for _, lib := range l {
		if lib != nil && lib.IsDefault {
			return lib
		}
	}
	return nil
}

// ValidateLibraryName checks a name before it reaches the database.
//
// The database has the CHECK too, and this is not redundant with it: the CHECK
// reports "CHECK constraint failed: length(trim(name)) > 0", which tells an
// operator which constraint broke and nothing about what to type.
func ValidateLibraryName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("a library needs a name")
	}
	return nil
}

// ErrNoDefaultLibrary is the refusal when a caller needs a default library and
// has none.
//
// Separate from a generic not-found because the fix is specific: create a
// library. A user has no default library exactly when they have no library at
// all, so the message can say that.
var ErrNoDefaultLibrary = errors.New("no default library: create a library first")

// ErrNotLibraryOwner is the refusal for touching somebody else's library.
var ErrNotLibraryOwner = errors.New("that library belongs to another user")

// ErrDefaultLibraryUndeletable is the refusal for deleting the default library.
//
// A security property rather than a policy choice: the default is what every row
// with no library_id resolves to, so deleting it leaves those rows owned by
// nobody -- which the gate refuses, and the refusal would take the owner's own
// files offline.
var ErrDefaultLibraryUndeletable = errors.New("the default library cannot be deleted")

// DescribeLibrary renders a library for an audit line, naming it by owner AND id.
//
// The id is in the string because a name is not an identifier: uniqueness is
// per owner, so two users may each own a library called "Main", and an audit
// line saying "deleted Main" is ambiguous between them.
func DescribeLibrary(l *Library) string {
	if l == nil {
		return "<no library>"
	}
	return fmt.Sprintf("%q (id %d, owner %d)", l.Name, l.ID, l.OwnerID)
}
