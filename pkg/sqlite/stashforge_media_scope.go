package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/stashapp/stash/internal/collab"
)

// The media-scope store. M4 step 4.3: the adapter behind
// collab.ResolveScope.
//
// WHY THE INTERFACE ASSERTION IS IN A NON-TEST FILE
//
// This is step 2.4b.9's lesson arriving a third time, and the same one that cost
// M2 and M2c a milestone each: a pure module with fake-backed tests proves its
// own logic and NOTHING about whether the real store fits it. In M2c the cluster
// store implemented three of the four methods the pass's interface needed -- the
// fourth, the only one that was not a single statement, did not exist -- and
// every test in the tree passed, because no file imported both packages and the
// compiler never had to compare them.
//
// So the assertion is here, in production code, where every build checks the
// seam. The first thing to write for a new module is a construction from OUTSIDE
// against the real implementation of whatever it will be given.
//
// # WHY THE TABLE NAME IS NOT INTERPOLATED
//
// LibraryOfTarget takes a target type that ultimately came from a route or a
// call site, and the obvious implementation is
// fmt.Sprintf("SELECT library_id FROM %s WHERE id = ?", targetType). That is a
// SQL injection with a short fuse: the value is a Go string that a future caller
// can fill from anywhere.
//
// So the type is matched against a CLOSED MAP first, and the query text is
// chosen by the program rather than assembled from the input. An unknown type
// is a refusal before any SQL is built, which is the same rule collab applies --
// and it is applied HERE as well as there because two layers that agree is one
// layer too many places to keep in step, and the one that can be forgotten is
// the one that gets forgotten.

// scopeTables maps a target type to the table its library_id lives in.
//
// Every entry is a compile-time constant. Adding a target type means adding a
// row here, and TestMediaScopeStore_EveryScopedTargetTypeHasATable walks the
// two lists against each other so a type that is valid to the domain but has no
// query here fails a test rather than 404ing in production.
var scopeTables = map[string]string{
	collab.TargetScene:     "scenes",
	collab.TargetImage:     "images",
	collab.TargetGallery:   "galleries",
	collab.TargetPerformer: "performers",
	collab.TargetTag:       "tags",
	collab.TargetStudio:    "studios",
	collab.TargetGroup:     "groups",
}

var _ collab.ScopeStore = (*MediaScopeStore)(nil)

// MediaScopeStore resolves "which library is this row in, and may this caller
// have it".
type MediaScopeStore struct {
	repository
}

func NewMediaScopeStore() *MediaScopeStore {
	return &MediaScopeStore{repository{tableName: "libraries"}}
}

// LibraryOfTarget reports the library a target row belongs to, or 0 when the row
// is in no library.
//
// 0 rather than an error for "no library" is deliberate and load-bearing: a row
// in no library is the SCANNER'S NORMAL OUTPUT, not a broken reference, and
// making it an error would put a refusal in the error channel where a caller
// would report it as a 500. collab.ResolveScope maps 0 to the default library.
func (s *MediaScopeStore) LibraryOfTarget(ctx context.Context, targetType string, targetID int64) (int64, error) {
	table, known := scopeTables[targetType]
	if !known {
		// Not an error: an unknown target type is a refusal, and the domain
		// has already refused it. Reaching here means a caller skipped that
		// check, and returning an error names the bug instead of silently
		// answering 0 (which would resolve to the default library and ALLOW).
		return 0, fmt.Errorf("%w: %q is not a scopable target type",
			collab.ErrNoLibraryAccess, targetType)
	}

	var libraryID sql.NullInt64
	err := dbWrapper.Get(ctx, &libraryID,
		"SELECT library_id FROM "+table+" WHERE id = ?", targetID)
	if err != nil {
		// A row that does not exist is "in no library" for this purpose, and
		// the caller is going to refuse either way. errors.Is, not ==:
		// dbWrapper wraps the driver error with %w, so == never matches and
		// a missing row becomes a 500 -- which tells a prober the row exists.
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading the library of %s %d: %w", targetType, targetID, err)
	}

	if !libraryID.Valid {
		return 0, nil
	}
	return libraryID.Int64, nil
}

// LibraryOwner reports who owns a library.
func (s *MediaScopeStore) LibraryOwner(ctx context.Context, libraryID int64) (int64, error) {
	var userID int64
	err := dbWrapper.Get(ctx, &userID,
		"SELECT user_id FROM libraries WHERE id = ?", libraryID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// A library that does not exist owns nothing, and reporting 0
			// means "owned by nobody" -- so only a real user can match it.
			// The caller treats 0 as "nobody is the owner" and falls through
			// to the grant check, which is the refusal.
			return 0, nil
		}
		return 0, fmt.Errorf("reading the owner of library %d: %w", libraryID, err)
	}
	return userID, nil
}

// DefaultLibraryID reports the instance's default library, or 0 when there is
// none.
//
// The partial unique index from migration 105 means there is at most one, so
// this needs no ORDER BY and no LIMIT: a second row is a database error, not a
// choice to make between two answers.
func (s *MediaScopeStore) DefaultLibraryID(ctx context.Context) (int64, error) {
	var id int64
	err := dbWrapper.Get(ctx, &id,
		"SELECT id FROM libraries WHERE is_default = 1")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No default library. An instance with no users has no owner to
			// own one, and the caller's answer is the refusal.
			return 0, nil
		}
		return 0, fmt.Errorf("reading the default library: %w", err)
	}
	return id, nil
}

// HasAccess reports the grant. The same question LibraryAccessStore asks, and
// the same single query -- duplicated rather than composed on purpose, because
// MediaScopeStore embeds `repository` for its own table and delegating to
// LibraryAccessStore would mean reaching through the Manager for a field that is
// three lines of SQL.
func (s *MediaScopeStore) HasAccess(ctx context.Context, userID, libraryID int64) (bool, error) {
	var one int
	err := dbWrapper.Get(ctx, &one,
		"SELECT 1 FROM user_library_access WHERE user_id = ? AND library_id = ?",
		userID, libraryID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("checking library %d access for user %d: %w", libraryID, userID, err)
	}
	return true, nil
}

// Mode is the instance posture, so a caller does not have to hold two stores.
func (s *MediaScopeStore) Mode(ctx context.Context) (collab.Mode, error) {
	return NewInstanceModeStore().Mode(ctx)
}
