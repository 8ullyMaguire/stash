package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/stashapp/stash/internal/collab"
)

// The consent store. M3 step 3.1.
//
// Two things about this file are worth stating before the code, because both are
// about what a reviewer should be looking for:
//
// 1. SetConsent is an UPSERT, not an update-then-insert. `consent_preferences`
//    has user_id as its primary key, so "record the answer" is one statement
//    with ON CONFLICT. The alternative -- SELECT, then INSERT or UPDATE -- has a
//    race in it that a concurrent publish can lose, and the loser is a consent
//    write silently dropped.
//
// 2. The write bumps disclosure_version to the current one. A user who has just
//    been shown the current published-field list has seen it, by definition, so
//    recording the old version would re-prompt them for an answer they have
//    already given. See collab.CurrentDisclosureVersion for why the version
//    exists at all.

// Compile-time proof that the adapter satisfies the domain interface. In a
// non-test file, so every build checks it -- a drift between this and collab's
// expectation is a build failure here rather than a runtime failure in the
// publish path, which is the one place a signature mismatch would be
// irreversible.
var _ collab.ConsentStore = (*ConsentStore)(nil)

type ConsentStore struct {
	repository
}

const (
	consentPreferencesTable = "consent_preferences"
)

func NewConsentStore() *ConsentStore {
	return &ConsentStore{repository{tableName: consentPreferencesTable, idColumn: "user_id"}}
}

// QueryContext exposes the read surface collab needs.
//
// A thin pass-through to dbWrapper.QueryxContext, whose *sqlx.Rows satisfies
// collab.Rows structurally -- Next, Scan, Err and Close are all it declares. The
// alternative is a wrapper type restating those four methods, and a wrapper is
// one more place for a Scan destination to drift.
func (s *ConsentStore) QueryContext(ctx context.Context, query string, args ...any) (collab.Rows, error) {
	return dbWrapper.QueryxContext(ctx, query, args...)
}

// SetConsent records the user's decision, replacing any previous answer.
//
// The version written is collab.CurrentDisclosureVersion, not the version the
// row already had: writing the answer and showing the disclosure are the same
// moment, and a row that records an older version would immediately look stale
// and re-prompt.
func (s *ConsentStore) SetConsent(ctx context.Context, userID int64, choice collab.ShareChoice) error {
	if !choice.Valid() {
		// Refused here as well as in collab.SetConsent. The domain check is the
		// one that matters for callers that go through the free function; this
		// one catches a direct store call, where an invalid value would hit the
		// CHECK constraint and come back as a driver error with no indication
		// of which value was wrong.
		return fmt.Errorf("consent choice %q is not a valid share choice", choice)
	}

	query := `INSERT INTO consent_preferences (user_id, metadata_share, disclosure_version)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			metadata_share = excluded.metadata_share,
			decided_at = CURRENT_TIMESTAMP,
			disclosure_version = excluded.disclosure_version`

	_, err := dbWrapper.Exec(ctx, query, userID, string(choice), collab.CurrentDisclosureVersion)
	return err
}

// Decide is the exported convenience the GraphQL resolver calls: validate in
// the domain, then write.
//
// It exists so no call site has to remember the order, because writing first and
// validating second would put an invalid value in the database before the
// validation noticed.
func (s *ConsentStore) Decide(ctx context.Context, userID int64, choice collab.ShareChoice) error {
	return collab.SetConsent(ctx, s, userID, choice)
}

// OptedIn is the one-line read the settings screen uses.
func (s *ConsentStore) OptedIn(ctx context.Context, userID int64) (bool, error) {
	return collab.ShareOptedIn(ctx, s, userID)
}

// NeedsDisclosureReprompt drives the blocking first-run screen.
func (s *ConsentStore) NeedsDisclosureReprompt(ctx context.Context, userID int64) (bool, error) {
	return collab.NeedsReprompt(ctx, s, userID)
}

// ErrConsentRowMissing distinguishes "no row" from "query failed" for a caller
// that must act differently on the two. Exposed because the difference matters
// to the disclosure screen and nothing else in the tree needs it.
var ErrConsentRowMissing = sql.ErrNoRows
