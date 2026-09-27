//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// tableColumns returns the real column names of a table, read off the migrated
// test database with PRAGMA rather than off the Go row structs.
//
// The Go structs were close enough to be tempting and would have hidden the bug
// this exists to catch: they carry a subset of columns in some cases and a
// `db` tag per field in others, and neither is a substitute for the schema.
func tableColumns(ctx context.Context, t *testing.T, table string) []string {
	t.Helper()

	out, err := db.SchemaColumns(ctx, table)
	require.NoError(t, err, "cannot read the columns of %s", table)
	return out
}
