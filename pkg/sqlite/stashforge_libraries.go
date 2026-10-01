package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/models"
)

// The OWNER-FACING library surface: create, delete, list, and "who can see my
// library". The grant table itself lives in stashforge_library_access.go; this
// file is the `libraries` table and the ownership checks around it.
//
// # WHY THESE ARE NOT METHODS ON LibraryAccessStore
//
// Because the two have different failure modes and different callers.
// LibraryAccessStore is on the AUTHENTICATION path — the media gate calls it on
// every request — and every method there is a single indexed read. This file is
// on the SETUP path, and every method here is a scan of one user's libraries or a
// check against `users`.
//
// Keeping them apart means the media gate's dependency is still
// `var _ collab.LibraryStore = (*LibraryAccessStore)(nil)` — a compile error if it
// needs a setup method — rather than one struct that grew a setup method and
// quietly became the thing every caller depends on.

// LibraryStore is the per-user library surface: a user's own sharing scopes.
//
// A library is PER-USER (migration 101: `libraries.user_id` NOT NULL REFERENCES
// users(id), UNIQUE (user_id, name)), so every method is scoped to one owner and
// every ownership check is "is this row's user_id the caller".
type LibraryStore struct {
	repository
}

func NewLibraryStore() *LibraryStore {
	return &LibraryStore{repository{tableName: "libraries"}}
}

// libraryColumns is the shared projection, so ListLibraries and GetLibrary
// cannot disagree about which columns a LibraryRow has.
//
// Written out rather than "SELECT *" on purpose: sqlx errors on a column with no
// destination field, which is a useful alarm, but a silent extra column in a
// hand-written struct is the same bug in a form that does not alarm.
const libraryColumns = `id, user_id, name, is_private,
	COALESCE(is_default, 0) AS is_default, created_at`

// LibraryRow is one library, before it becomes a collab.Library.
//
// The db tags are not optional: sqlx maps columns to fields by tag, and a column
// with no matching destination is an error ("missing destination name
// created_at") rather than a skipped field. So `created_at` is HERE and
// `libraryColumns` selects it, even though nothing reads it: the projection and
// the struct are a pair, and the first version of this had a projection with
// created_at and a struct without it, which failed at runtime with a message
// about a column rather than about the mismatch.
type LibraryRow struct {
	ID        int64     `db:"id"`
	UserID    int64     `db:"user_id"`
	Name      string    `db:"name"`
	IsPrivate *bool     `db:"is_private"`
	IsDefault bool      `db:"is_default"`
	CreatedAt Timestamp `db:"created_at"`
}

// toLibrary converts a row to the domain object.
//
// One function rather than inline at each call site, because the is_private
// pointer is the subtle field: a conversion written twice is a conversion
// written with a nil-deref or a `!= nil &&` typo in one of the two, and neither
// shows up in a happy-path test.
func (r LibraryRow) toLibrary() *collab.Library {
	return &collab.Library{
		ID:        r.ID,
		Name:      r.Name,
		IsPrivate: r.IsPrivate,
		IsDefault: r.IsDefault,
		OwnerID:   r.UserID,
	}
}

// ListLibraries returns every library belonging to one user, ordered by id.
//
// Ordered, and the order is a property rather than a cosmetic choice: this list
// renders as rows, and a list that reorders between two reads makes an
// "unchanged" diff report a change that did not happen.
func (s *LibraryStore) ListLibraries(ctx context.Context, userID int64) (collab.LibraryList, error) {
	var rows []LibraryRow
	err := dbWrapper.Select(ctx, &rows,
		"SELECT "+libraryColumns+" FROM libraries WHERE user_id = ? ORDER BY id", userID)
	if err != nil {
		return nil, fmt.Errorf("listing libraries for user %d: %w", userID, err)
	}

	out := make(collab.LibraryList, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toLibrary())
	}

	// The grantee counts are one query for the whole list, not one per library.
	// Doing it per library would be N+1 on the screen an owner opens first after
	// granting somebody access — and the counts are the numbers they are
	// checking.
	counts, err := s.granteeCounts(ctx, out)
	if err != nil {
		return nil, err
	}
	for _, lib := range out {
		lib.GranteeCount = counts[lib.ID]
	}

	return out, nil
}

// granteeCounts returns COUNT(*) per library id for a set of libraries.
//
// One query with a GROUP BY, mapped back by id. The map is nil for a library with
// no grants rather than an error, because "nobody has access" is an answer and
// the zero value of int says exactly that.
func (s *LibraryStore) granteeCounts(ctx context.Context, libs collab.LibraryList) (map[int64]int, error) {
	if len(libs) == 0 {
		return map[int64]int{}, nil
	}

	// Placeholders built from the ids. Every value is an int64 from a row, not
	// caller input, so this cannot inject -- and it is still built as
	// placeholders rather than a literal list, because an id of 0 or a negative
	// would be a real id and a "definitely not an id" check is the kind of
	// validation that gets removed as noise.
	ids := make([]any, 0, len(libs))
	marks := make([]string, 0, len(libs))
	for _, lib := range libs {
		ids = append(ids, lib.ID)
		marks = append(marks, "?")
	}

	type row struct {
		LibraryID int64 `db:"library_id"`
		Count     int   `db:"n"`
	}
	var rows []row
	query := "SELECT library_id, COUNT(*) AS n FROM user_library_access " +
		"WHERE library_id IN (" + strings.Join(marks, ",") + ") GROUP BY library_id"
	if err := dbWrapper.Select(ctx, &rows, query, ids...); err != nil {
		return nil, fmt.Errorf("counting grantees: %w", err)
	}

	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		out[r.LibraryID] = r.Count
	}
	return out, nil
}

// GetLibrary returns one library by id, with NO ownership check.
//
// Deliberately unchecked, and named for what it is: it is the primitive the
// checked reads are built from. A caller wanting "library 7, if it is mine" uses
// OwnedLibrary; a caller reaching for GetLibrary directly is the case the
// ownership tests are written to catch.
func (s *LibraryStore) GetLibrary(ctx context.Context, libraryID int64) (*collab.Library, error) {
	var row LibraryRow
	err := dbWrapper.Get(ctx, &row,
		"SELECT "+libraryColumns+" FROM libraries WHERE id = ?", libraryID)
	if err != nil {
		// sql.ErrNoRows, NOT models.ErrNotFound: dbWrapper.Get is a raw sqlx
		// call, so the DRIVER error arrives wrapped in %w. models.ErrNotFound
		// is the goqu-layer convention, used by UserStore, and it never
		// appears here. (errors.Is rather than ==, because dbWrapper adds
		// context with %w -- a direct comparison cannot match.)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no library with id %d: %w", libraryID, models.ErrNotFound)
		}
		return nil, fmt.Errorf("reading library %d: %w", libraryID, err)
	}
	return row.toLibrary(), nil
}

// OwnedLibrary returns a library only if the named user owns it.
//
// The single checked read, and every path from a caller-supplied libraryId to a
// library's contents comes through here. One function rather than a check
// repeated at each call site, because a check that is repeated is a check that
// is eventually omitted.
//
// # THE TWO CASES RETURN THE SAME ERROR, AND THAT IS THE POINT
//
// "This library is not yours" and "no library has this id" are both
// ErrNotLibraryOwner. Distinguishing them turns the second into an oracle: a
// caller probing ids learns exactly which ones are real, which is an existence
// map of every library on the instance.
//
// The first version returned models.ErrNotFound for the missing case, wrapped
// from sql.ErrNoRows, and the difference was a real disclosure — found by a test
// that asserts the two errors are indistinguishable, which is a test worth having
// precisely because the property is invisible from the code.
//
// So this is a NARROWER rule than the media gate's, and the difference is worth
// being precise about. The gate must not confirm a FILE exists to a user with no
// grant. Here the caller already holds the id — they were given it, or they
// guessed it — and the disclosure risk is not "they know an id" but "they can
// tell real ids from fake ones". Collapsing the two errors removes exactly that,
// and still lets a legitimate owner learn that the id they were given is not one
// of theirs.
func (s *LibraryStore) OwnedLibrary(ctx context.Context, libraryID, userID int64) (*collab.Library, error) {
	lib, err := s.GetLibrary(ctx, libraryID)
	if err != nil {
		// A missing library reports the OWNERSHIP refusal, not a not-found.
		// That is the whole property: see the file comment.
		return nil, fmt.Errorf("%w (library %d)", collab.ErrNotLibraryOwner, libraryID)
	}
	if !lib.OwnedBy(userID) {
		return nil, fmt.Errorf("%w (library %d)", collab.ErrNotLibraryOwner, libraryID)
	}
	return lib, nil
}

// RequireOwnedBy is the ownership assertion, for callers holding an id and
// needing no library back.
func (s *LibraryStore) RequireOwnedBy(ctx context.Context, libraryID, userID int64) error {
	_, err := s.OwnedLibrary(ctx, libraryID, userID)
	return err
}

// CreateLibrary makes a library for a user.
//
// A user's FIRST library becomes the default, and that is a rule with a
// consequence rather than a convenience: the default is what every row with no
// library_id resolves to (see media_scope.go), so a user with exactly one library
// has an unambiguous answer for an unscanned row, while a user with no library at
// all has no owner for one — and the gate refuses those. So creating a library is
// what makes the NULL substitution sound for a new user.
//
// A second and later library is NOT the default, and the partial unique index
// makes that a database fact rather than a convention: two defaults would make
// "which library is this file in" a question with two answers.
func (s *LibraryStore) CreateLibrary(ctx context.Context, name string, userID int64) (*collab.Library, error) {
	if err := collab.ValidateLibraryName(name); err != nil {
		return nil, err
	}

	// is_private is left NULL on purpose. NULL is the third state, "defer to
	// the owner's consent row": a new library asserts nothing about publication
	// and inherits the owner's decision. Writing false here would publish a
	// library nobody chose to publish, and §6.1's default is the publishing one.
	var defaultVal int

	var existing int
	if err := dbWrapper.Get(ctx, &existing,
		"SELECT COUNT(*) FROM libraries WHERE user_id = ?", userID); err != nil {
		return nil, fmt.Errorf("counting libraries for user %d: %w", userID, err)
	}
	if existing == 0 {
		defaultVal = 1
	}

	res, err := dbWrapper.Exec(ctx,
		"INSERT INTO libraries (user_id, name, is_private, is_default) VALUES (?, ?, NULL, ?)",
		userID, name, defaultVal)
	if err != nil {
		// UNIQUE (user_id, name) is the common failure here and it deserves its
		// own message: "UNIQUE constraint failed: libraries.user_id,
		// libraries.name" tells an operator neither which library nor what to
		// change. The detection is on the SQLite message rather than on a typed
		// error because the driver does not export one for this constraint.
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, fmt.Errorf("you already have a library named %q", name)
		}
		return nil, fmt.Errorf("creating library %q: %w", name, err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("reading the new library id: %w", err)
	}

	return &collab.Library{
		ID:        id,
		Name:      name,
		IsPrivate: nil, // defer, as inserted
		IsDefault: existing == 0,
		OwnerID:   userID,
	}, nil
}

// DeleteLibrary removes a library the named user owns.
//
// The default is refused, and the refusal is the interesting part. Everything in
// a deleted library's rows does not become public: those rows fall out of any
// library, which the gate resolves to the owner's default… which still exists,
// because the default cannot be deleted. So they become owner-visible, which is
// the same visibility they had before the library existed.
//
// The refusal is still necessary, because "still exists" is a fact about the
// OTHER library. A user who has one library and it is the default cannot delete
// it, and without the refusal the gate would resolve their every row to "no
// library, no owner" and refuse it — taking their own files offline on an
// instance they own.
func (s *LibraryStore) DeleteLibrary(ctx context.Context, libraryID, userID int64) (bool, error) {
	lib, err := s.OwnedLibrary(ctx, libraryID, userID)
	if err != nil {
		return false, err
	}
	if lib.IsDefault {
		return false, fmt.Errorf("%w (%s)", collab.ErrDefaultLibraryUndeletable,
			collab.DescribeLibrary(lib))
	}

	if _, err := dbWrapper.Exec(ctx, "DELETE FROM libraries WHERE id = ?", libraryID); err != nil {
		return false, fmt.Errorf("deleting library %d: %w", libraryID, err)
	}
	return true, nil
}

// ResolveLibraryID turns an optional libraryId into a concrete library id.
//
// A null libraryId means the user's DEFAULT library, which is what an operator
// means by "the library". It is not an error to omit it, and it is deliberately
// not "all libraries": "all" would make a revoke a wildcard, and the schema has
// no wildcard row on purpose — a grant names a specific library and a specific
// user, so adding access is a deliberate, auditable insert.
func (s *LibraryStore) ResolveLibraryID(ctx context.Context, libraryID *string, userID int64) (int64, error) {
	if libraryID != nil && *libraryID != "" {
		id, err := strconv.ParseInt(strings.TrimSpace(*libraryID), 10, 64)
		if err != nil || id <= 0 {
			// The field is named because `strconv.ParseInt: parsing "abc":
			// invalid syntax` tells an operator nothing about which of the two
			// ids in one request was malformed. And id <= 0 is refused rather
			// than passed on: a library id of 0 would be a real row in every
			// table the gate reads, owning nothing and matching nothing.
			return 0, fmt.Errorf("libraryId is not a valid id: %q", *libraryID)
		}
		return id, nil
	}

	var id int64
	err := dbWrapper.Get(ctx, &id,
		"SELECT id FROM libraries WHERE user_id = ? AND is_default = 1", userID)
	if err != nil {
		// sql.ErrNoRows, NOT models.ErrNotFound. dbWrapper.Get is a raw sqlx
		// call, so the driver error is what arrives wrapped in %w -- and
		// models.ErrNotFound is the goqu-layer convention used by UserStore.
		// Checking the wrong one means the refusal never fires and the caller
		// gets "error executing SELECT id FROM libraries..." naming a column
		// instead of "create a library first". The first version of this
		// checked models.ErrNotFound and its own test caught it.
		if errors.Is(err, sql.ErrNoRows) {
			// A user with no default library has no library at all, because the
			// default is set on first creation. The error says so, because the
			// fix is "create a library" and a bare not-found sends the caller
			// looking for a missing row instead.
			return 0, collab.ErrNoDefaultLibrary
		}
		return 0, fmt.Errorf("resolving the default library for user %d: %w", userID, err)
	}
	return id, nil
}

// findUsersByIDs loads user rows for a set of ids, in the given order.
//
// It exists because the alternative is a Find call per id inside a loop, and the
// two call sites that want it (the grantee list, the "who can see this" view) are
// both on screens that render N rows at once.
//
// The ORDER is preserved on purpose, and this was the second attempt: the first
// wrote raw SQL against models.User, which has no `db` tags at all, so sqlx
// failed with "missing destination name created_at in *[]*models.User". The
// package's own userRow is the struct with the tags, so the projection has to
// match THAT -- which means going through UserStore rather than around it.
//
// The order matters because the grant query already sorted, and a grantee list
// that comes back in a different order on two reads is useless as an audit view.
func findUsersByIDs(ctx context.Context, ids []int64) ([]*models.User, error) {
	if len(ids) == 0 {
		return []*models.User{}, nil
	}

	// One query for the whole set. A Find per id would be N round trips on the
	// screen an owner opens right after granting somebody access.
	rows, err := NewUserStore().FindAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}

	// Index by id, then emit in the REQUESTED order so the caller's sort
	// survives. A missing id is an ERROR rather than a shorter list: the grant
	// table cascades on user delete, so a grant with no user is a bug in the
	// cascade -- and a list that silently dropped it would read to the owner as
	// "that is everyone with access".
	byID := make(map[int64]*models.User, len(rows))
	for _, u := range rows {
		byID[int64(u.ID)] = u
	}

	out := make([]*models.User, 0, len(ids))
	for _, id := range ids {
		u, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("user %d has a grant but no row: this is a bug in "+
				"the user_library_access cascade, not a state to render", id)
		}
		out = append(out, u)
	}
	return out, nil
}

// Grant gives a user access to a library, and Revoke takes it away.
//
// They delegate to LibraryAccessStore rather than duplicating its SQL. The
// ownership question is answered HERE (the caller must own the library) and the
// grant question is answered THERE (does this row exist), and the two are
// different checks on different tables -- a grant against somebody else's library
// is refused here before the row is ever written.
//
// The ownership check is done by the caller (GrantLibraryAccess) rather than
// re-derived here, because a grant to a library you do not own is exactly the
// case a second, silent check inside a method named "Grant" would hide.
func (s *LibraryStore) grants() *LibraryAccessStore { return NewLibraryAccessStore() }

// Grant gives a user access to a library. Idempotent.
func (s *LibraryStore) Grant(ctx context.Context, userID, libraryID int64) error {
	return s.grants().Grant(ctx, userID, libraryID)
}

// Revoke removes a grant. Idempotent, and not an error when absent: the desired
// end state is "this user has no access", and reporting failure for a revoke of
// something already absent turns a well-behaved retry into a retry loop.
func (s *LibraryStore) Revoke(ctx context.Context, userID, libraryID int64) error {
	return s.grants().Revoke(ctx, userID, libraryID)
}

// Grantees returns the users holding a grant on a library, as user rows.
//
// The ids are read sorted (so the answer is stable for an audit log) and the ROWS
// are what the GraphQL layer needs, so the second read happens here rather than
// in the resolver. A user id with no row cannot happen: the grant table cascades
// on user delete, so a dangling id would be a bug in the cascade rather than a
// state worth handling here.
func (s *LibraryStore) Grantees(ctx context.Context, libraryID int64) ([]*models.User, error) {
	var ids []int64
	if err := dbWrapper.Select(ctx, &ids,
		"SELECT user_id FROM user_library_access WHERE library_id = ? ORDER BY user_id",
		libraryID); err != nil {
		return nil, fmt.Errorf("listing grantees for library %d: %w", libraryID, err)
	}

	// An empty slice, never nil: gqlgen marshals a nil slice as [] anyway, but a
	// nil *models.User inside a slice marshals as null, and the distinction that
	// matters here is "nobody has a grant" versus "a grantee we could not read".
	// The latter is impossible — the ids came from a successful read — so the
	// empty slice states "nobody" honestly.
	if len(ids) == 0 {
		return []*models.User{}, nil
	}

	users, err := findUsersByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	return users, nil
}
