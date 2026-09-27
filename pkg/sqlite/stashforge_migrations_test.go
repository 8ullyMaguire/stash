//go:build integration
// +build integration

// Migration tests for the StashForge tables.
//
// A migration that does not apply is worse than no migration: the binary
// opens, queries fail at runtime, and the error names a missing column rather
// than the absent migration. So these run against the real migrator via the
// shared integration database (setup_test.go), not a hand-written schema.
//
// Every test runs inside withRollbackTxn, the harness's own idiom, which also
// gives each test private rows: nothing here depends on or disturbs the shared
// fixture data the rest of this package builds.
//
// The constraint assertions are the point. `idx_users_single_owner` and the
// invite `uses <= max_uses` CHECK are guarantees the Go layer does not
// re-implement. If a future migration drops one, the code keeps compiling and
// the governance invariant is quietly gone -- exactly the class of regression
// a green suite should catch.

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sfTxn runs f inside a rolled-back transaction, so each test gets private
// rows and leaves nothing behind.
func sfTxn(t *testing.T, f func(ctx context.Context)) {
	t.Helper()
	require.NoError(t, withRollbackTxn(func(ctx context.Context) error {
		f(ctx)
		return nil
	}))
}

func scalar(t *testing.T, ctx context.Context, query string, args ...interface{}) interface{} {
	t.Helper()
	// QuerySQL/ExecSQL take args as a single []interface{} slice, not variadic --
	// passing the variadic straight through compiles but yields
	// "unsupported type []interface {}, a slice of interface" at run time.
	_, rows, err := db.QuerySQL(ctx, query, args)
	require.NoError(t, err, "query: %s", query)
	require.NotEmpty(t, rows, "query returned no rows: %s", query)
	return rows[0][0]
}

func count(t *testing.T, ctx context.Context, query string, args ...interface{}) int64 {
	t.Helper()
	return scalar(t, ctx, query, args...).(int64)
}

func exec(t *testing.T, ctx context.Context, query string, args ...interface{}) error {
	t.Helper()
	_, _, err := db.ExecSQL(ctx, query, args)
	return err
}

// insertUser creates a user and returns its id.
func insertUser(t *testing.T, ctx context.Context, name string, isOwner bool) int64 {
	t.Helper()
	require.NoError(t, exec(t, ctx,
		"INSERT INTO users (username, password_hash, is_owner) VALUES (?, x'00', ?)",
		name, isOwner))
	return scalar(t, ctx, "SELECT id FROM users WHERE username = ?", name).(int64)
}

func TestStashForge_TablesExist(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		for _, table := range []string{"users", "invite_keys", "user_sessions", "collab_audit"} {
			assert.Equal(t, int64(1),
				count(t, ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table),
				"table %s must exist after migration", table)
		}
	})
}

// The recorded version and appSchemaVersion must agree. database.go refuses to
// open when they differ, so a migration committed without the version bump
// breaks every subsequent start.
func TestStashForge_SchemaVersionMatchesAppSchemaVersion(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		assert.Equal(t, db.AppSchemaVersion(),
			uint(count(t, ctx, "SELECT version FROM schema_migrations")),
			"the recorded schema version and appSchemaVersion must agree; a "+
				"migration committed without the bump reports a mismatch on every open")
	})
}

// The spec's central governance invariant: exactly one owner, enforced by the
// database. An application-layer check is one forgotten code path from two
// owners, and the owner is the one role that must not be ambiguous.
func TestStashForge_OnlyOneOwner(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		insertUser(t, ctx, "sfOwner", true)

		assert.Error(t, exec(t, ctx,
			"INSERT INTO users (username, password_hash, is_owner) VALUES ('sfOwner2', x'00', 1)"),
			"a second is_owner=1 row must be refused by idx_users_single_owner; "+
				"if this passes, the partial unique index has been dropped")

		// ...and non-owners are unaffected, or the index would be useless.
		require.NoError(t, exec(t, ctx,
			"INSERT INTO users (username, password_hash, is_owner) VALUES ('sfPlain', x'00', 0)"))
	})
}

// Usernames are COLLATE NOCASE so "Alice" and "alice" are one account. Without
// it both register, and a case-insensitive login lookup becomes ambiguous.
func TestStashForge_UsernameIsCaseInsensitive(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		insertUser(t, ctx, "sfAlice", false)

		assert.Error(t, exec(t, ctx,
			"INSERT INTO users (username, password_hash) VALUES ('sfalice', x'00')"),
			"a case-variant username must collide")

		assert.Equal(t, int64(1),
			count(t, ctx, "SELECT count(*) FROM users WHERE username = ? COLLATE NOCASE", "SFALICE"),
			"a case-insensitive lookup must find exactly one row")
	})
}

// Over-redemption is the failure that lets one leaked invite create a hundred
// accounts, so the bound is a CHECK as well as a read-path condition.
func TestStashForge_InviteUsesCannotExceedMax(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		ownerID := insertUser(t, ctx, "sfInviter", true)
		require.NoError(t, exec(t, ctx,
			"INSERT INTO invite_keys (key_hash, created_by, max_uses) VALUES (x'01', ?, 1)", ownerID))

		assert.Error(t, exec(t, ctx, "UPDATE invite_keys SET uses = 2 WHERE key_hash = x'01'"),
			"uses must not exceed max_uses; if this passes the CHECK was dropped")

		// uses = max_uses is legal; only exceeding it is not.
		require.NoError(t, exec(t, ctx, "UPDATE invite_keys SET uses = 1 WHERE key_hash = x'01'"))
	})
}

// A session is stored as a hash, never as the token. A database leak must not
// yield a usable session, which is the whole reason for the column shape.
func TestStashForge_SessionStoresHashNotToken(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		uid := insertUser(t, ctx, "sfSession", false)
		require.NoError(t, exec(t, ctx,
			"INSERT INTO user_sessions (id, user_id, expires_at) "+
				"VALUES (x'02', ?, datetime('now', '+1 day'))", uid))

		assert.Equal(t, "blob",
			scalar(t, ctx, "SELECT typeof(id) FROM user_sessions WHERE user_id = ?", uid),
			"the session id column must hold a hash (blob); storing the token "+
				"itself would make a database leak equivalent to a session takeover")

		assert.Equal(t, int64(1),
			count(t, ctx, "SELECT count(*) FROM user_sessions WHERE id = x'02' AND user_id = ?", uid),
			"a stored session must be findable by its hash")
	})
}

// Disabling keeps the row, so authored proposals and the audit trail survive.
// A hard delete would silently rewrite history.
func TestStashForge_DisabledUserKeepsRow(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		uid := insertUser(t, ctx, "sfDisabled", false)
		require.NoError(t, exec(t, ctx,
			"UPDATE users SET disabled_at = CURRENT_TIMESTAMP WHERE id = ?", uid))

		assert.Equal(t, int64(1), count(t, ctx, "SELECT count(*) FROM users WHERE id = ?", uid),
			"a disabled user must keep their row")
	})
}

// The audit table records unauthenticated actions too -- a failed login or a
// rejected invite is exactly the row that shows a brute-force or a signup
// flood, so actor_id must be nullable and the row must persist.
func TestStashForge_AuditAcceptsUnauthenticatedActions(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		require.NoError(t, exec(t, ctx,
			"INSERT INTO collab_audit (action, detail) VALUES ('login_failed', ?)",
			`{"ip":"192.0.2.1"}`))

		assert.Equal(t, int64(1),
			count(t, ctx, "SELECT count(*) FROM collab_audit WHERE action = 'login_failed'"),
			"an unauthenticated action must be recorded with a NULL actor_id")
	})
}
