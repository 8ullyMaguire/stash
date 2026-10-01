//go:build integration

package sqlite

import (
	"context"
	"database/sql"
)

// Debug helpers for the audit-chain tests.
//
// The AuditStore deliberately exposes no update and no delete, so proving the
// chain detects tampering requires going around it. These live in package
// sqlite (not sqlite_test) because dbWrapper is unexported, and they use
// dbWrapper rather than a raw *sql.DB so they run inside the caller's
// transaction -- the same reason zz_rawvote_test.go does it this way. A helper
// that opened its own connection would write outside the rollback transaction
// and leak state into every later test.

// DbgAuditRawExec runs arbitrary SQL against the audit table inside the current
// transaction. It exists so a test can do precisely what the Go API forbids.
func DbgAuditRawExec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return dbWrapper.Exec(ctx, query, args...)
}

// DbgAuditSelectOne reads a single value out of the audit table. Returns
// sql.ErrNoRows when there is none, which callers must handle rather than
// treat as success.
func DbgAuditSelectOne(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return dbWrapper.Get(ctx, dest, query, args...)
}
