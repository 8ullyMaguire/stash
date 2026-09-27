//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/sqlite"
)

// The random sort shipped calling mod(), a Postgres function SQLite does not
// have, so every random_* sort failed at runtime. A string assertion would not
// have caught it -- mod() is valid syntax -- so this runs the fragment against
// a real connection. That is the only kind of test that catches this class of
// bug, and its absence is why it survived in a suite of 2273.
func TestGetRandomSort_ExecutesOnARealDatabase(t *testing.T) {
	runWithRollbackTxn(t, "random-sort", func(t *testing.T, ctx context.Context) {
		_, _, err := db.QuerySQL(ctx,
			fmt.Sprintf("SELECT id FROM studios %s LIMIT 3",
				sqlite.GetRandomSortForTest("studios", "ASC", 26819649)), []interface{}{})
		require.NoError(t, err,
			"the random sort must execute against SQLite, not merely parse: mod() parses "+
				"and then fails, which is exactly how this shipped")

		_, _, err = db.QuerySQL(ctx,
			fmt.Sprintf("SELECT id FROM studios %s LIMIT 3",
				sqlite.GetRandomSortForTest("studios", "DESC", 42)), []interface{}{})
		require.NoError(t, err)
	})
}

// A seeded random sort that is not deterministic makes pagination impossible:
// page 2 comes back overlapping page 1 arbitrarily. Same seed, same order.
func TestGetRandomSort_SameSeedGivesSameOrder(t *testing.T) {
	runWithRollbackTxn(t, "random-sort-determinism", func(t *testing.T, ctx context.Context) {
		// BOTH queries run inside the transaction. A closure returned from the
		// helper would run after the transaction closed, and dbWrapper follows
		// the active transaction -- so it fails with "not in transaction".
		q := func(seed uint64) [][]interface{} {
			_, rows, err := db.QuerySQL(ctx, fmt.Sprintf("SELECT id FROM studios %s LIMIT 5",
				sqlite.GetRandomSortForTest("studios", "ASC", seed)), []interface{}{})
			require.NoError(t, err)
			return rows
		}

		first := q(26819649)
		second := q(26819649)
		assert.Equal(t, first, second,
			"a seeded sort that is not deterministic makes paging a random order a lottery")
	})
}

func TestGetRandomSort_ActuallyPermutes(t *testing.T) {
	runWithRollbackTxn(t, "random-sort-permutes", func(t *testing.T, ctx context.Context) {
		_, rows, err := db.QuerySQL(ctx,
			"SELECT id FROM studios ORDER BY id LIMIT 40", []interface{}{})
		require.NoError(t, err)
		if len(rows) < 12 {
			t.Skipf("only %d studios in the fixture database; too few to tell a "+
				"permutation from id order", len(rows))
		}

		_, randomised, err := db.QuerySQL(ctx, fmt.Sprintf("SELECT id FROM studios %s LIMIT 40",
			sqlite.GetRandomSortForTest("studios", "ASC", 26819649)), []interface{}{})
		require.NoError(t, err)
		require.Len(t, randomised, len(rows))

		// At least one adjacent pair must be inverted relative to id order.
		//
		// Not "the order must differ": a hash over a small set can coincide with
		// id order, and a test demanding it differ would be flaky rather than
		// strict. One inversion out of dozens of pairs is what distinguishes a
		// real permutation from a no-op, and it cannot happen by chance often
		// enough to be flaky.
		// The sort key must be an INTEGER and must differ per row.
		//
		// This is the assertion that catches the silent overflow. The broken
		// expression returned typeof() = 'real' and the constant 1 for every row:
		// no error, plausible-looking SQL, and an "ORDER BY" that quietly did
		// nothing. typeof() is what turns that from invisible into a failure.
		keyExpr := sortKeyExpressionForTest("studios", "ASC", 26819649)
		_, krows, err := db.QuerySQL(ctx,
			"SELECT id, "+keyExpr+" AS k, typeof("+keyExpr+") FROM studios ORDER BY k LIMIT 8",
			[]interface{}{})
		require.NoError(t, err)
		require.NotEmpty(t, krows)

		seenKeys := map[interface{}]bool{}
		for _, r := range krows {
			assert.Equal(t, "integer", fmt.Sprintf("%v", r[2]),
				"the sort key for id=%v is typed %v, not integer. A 'real' key means the "+
					"polynomial overflowed int64 and SQLite fell back to float64, which "+
					"makes the trailing modulo a no-op and the sort degenerate to id "+
					"order without raising anything",
				r[0], r[2])
			seenKeys[r[1]] = true
		}
		assert.Greater(t, len(seenKeys), 1,
			"every row got the same sort key, so the order is id order whatever the "+
				"expression looks like")

		inversions := 0
		for i := range rows {
			if randomised[i][0] != rows[i][0] {
				inversions++
			}
		}
		assert.Greater(t, inversions, 0,
			"a random sort that returns id order is not a random sort; this is the "+
				"failure mode that leaves every other test green")

		// And the same set, reordered -- a permutation, not a filter.
		got := map[interface{}]bool{}
		for _, r := range randomised {
			got[r[0]] = true
		}
		assert.Len(t, got, len(rows),
			"the random sort must return the same rows in a different order, not a "+
				"different subset")
	})
}

// sortKeyExpressionForTest turns getRandomSort's " ORDER BY <expr> <dir>" into
// a bare "<expr>" so the test can read the key as a value and ask SQLite for its
// type. The direction is dropped because it is a property of the ORDER BY, not
// of the expression.
func sortKeyExpressionForTest(table, direction string, seed uint64) string {
	frag := strings.TrimSpace(sqlite.GetRandomSortForTest(table, direction, seed))
	frag = strings.TrimPrefix(frag, "ORDER BY")
	frag = strings.TrimSpace(frag)
	frag = strings.TrimSuffix(frag, strings.TrimSpace(direction))
	return strings.TrimSpace(frag)
}
