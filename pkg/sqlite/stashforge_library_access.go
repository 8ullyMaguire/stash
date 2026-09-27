package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/stashapp/stash/internal/collab"
)

// The library-access store. M4 step 4.3.
//
// The table arrived in migration 101, written for M3's consent work; this is the
// store that makes it usable. Two things about the SQL are deliberate:
//
//   - HasAccess is ONE query, not a "fetch the grants and look". A caller that
//     loaded a list would be one refactor away from checking the wrong slice, and
//     this is the check that decides whether a file is served.
//   - Revoke is idempotent and does not error when the grant is absent. The
//     desired end state is "this user has no access"; reporting failure for
//     revoking something already absent would make a retry loop out of a
//     well-behaved caller.
//
// The DECISION itself is not made here. It is collab.AccessDecision, and it
// takes the grant answer as a parameter, so the mode's rule and the grant's
// answer stay separate inputs. This file answers "does this row exist"; it does
// not decide what that means.

var _ collab.LibraryStore = (*LibraryAccessStore)(nil)

type LibraryAccessStore struct {
	repository
}

func NewLibraryAccessStore() *LibraryAccessStore {
	return &LibraryAccessStore{repository{tableName: "user_library_access"}}
}

// HasAccess reports whether the grant exists.
func (s *LibraryAccessStore) HasAccess(ctx context.Context, userID, libraryID int64) (bool, error) {
	var one int
	err := dbWrapper.Get(ctx, &one,
		"SELECT 1 FROM user_library_access WHERE user_id = ? AND library_id = ?",
		userID, libraryID)
	if err != nil {
		// errors.Is, NOT ==. dbWrapper wraps the driver error with %w and adds
		// context, so a direct comparison never matches and "no grant" surfaces
		// as an internal error -- which is the worst possible outcome here,
		// because it turns every ungranted request into a 500 and tells a prober
		// that a library exists.
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("checking library %d access for user %d: %w", libraryID, userID, err)
	}
	return true, nil
}

// Decide is the whole of §6.4 in one call: read the grant, apply the mode, return
// the single refusal.
//
// It is here as well as in the domain because the serving path should not be
// able to forget the mode. A caller that fetches the grant itself and then calls
// AccessDecision.Decide is correct but verbose, and the verbosity is where a
// missing argument goes.
func (s *LibraryAccessStore) Decide(ctx context.Context, mode collab.Mode, userID, libraryID int64) error {
	has, err := s.HasAccess(ctx, userID, libraryID)
	if err != nil {
		return err
	}
	return collab.AccessDecision{Mode: mode, HasGrant: has}.Decide()
}

// Grant records access, idempotently.
//
// ON CONFLICT DO UPDATE rather than DO NOTHING: the pair is the primary key, so
// re-granting is the same grant -- but it should also refresh granted_at, because
// a re-grant after a revocation is a new decision and "since when" should answer
// with the new date. DO NOTHING would keep the old date and quietly misreport how
// long access has been held.
func (s *LibraryAccessStore) Grant(ctx context.Context, userID, libraryID int64) error {
	_, err := dbWrapper.Exec(ctx,
		`INSERT INTO user_library_access (user_id, library_id, granted_at)
		      VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT (user_id, library_id) DO UPDATE SET granted_at = CURRENT_TIMESTAMP`,
		userID, libraryID)
	return err
}

// Revoke removes a grant, idempotently.
func (s *LibraryAccessStore) Revoke(ctx context.Context, userID, libraryID int64) error {
	_, err := dbWrapper.Exec(ctx,
		"DELETE FROM user_library_access WHERE user_id = ? AND library_id = ?",
		userID, libraryID)
	return err
}

// UsersWithAccess lists the grantees of a library, sorted.
//
// Sorted because the owner's view of their own library is also an audit
// question, and a list that reorders between two reads is useless for that.
func (s *LibraryAccessStore) UsersWithAccess(ctx context.Context, libraryID int64) ([]int64, error) {
	var ids []int64
	if err := dbWrapper.Select(ctx, &ids,
		"SELECT user_id FROM user_library_access WHERE library_id = ? ORDER BY user_id",
		libraryID); err != nil {
		return nil, fmt.Errorf("listing access for library %d: %w", libraryID, err)
	}
	return collab.SortUserIDs(ids), nil
}

// LibrariesForUser lists what a user has been granted, for the "what can I
// actually open" view.
func (s *LibraryAccessStore) LibrariesForUser(ctx context.Context, userID int64) ([]int64, error) {
	var ids []int64
	if err := dbWrapper.Select(ctx, &ids,
		"SELECT library_id FROM user_library_access WHERE user_id = ? ORDER BY library_id",
		userID); err != nil {
		return nil, fmt.Errorf("listing libraries for user %d: %w", userID, err)
	}
	return ids, nil
}
