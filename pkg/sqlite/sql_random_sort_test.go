package sqlite

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// getRandomSort had no direct test, which is why it shipped calling mod() --
// a Postgres function SQLite does not have. Every random_* sort failed at
// runtime with "no such function: mod", and the only coverage was
// TestStudioQueryFast, which needs a random sort AND a query that reaches the
// ORDER BY, so nothing else noticed.
//
// These tests check the fragment's SHAPE, which is checkable without a
// database. The companion test in stashforge_random_sort_exec_test.go executes
// it against a real connection, because the whole point of the bug is that the
// fragment parsed fine and only failed at execution time -- a string assertion
// alone would not have caught mod(), which is valid syntax.

// mod() is Postgres. SQLite's modulo is the % OPERATOR, so a fragment
// containing mod() parses and then fails when the query runs.
func TestGetRandomSort_UsesTheModuloOperatorNotMod(t *testing.T) {
	frags := map[string]string{
		"ascending":  getRandomSort("studios", "ASC", 26819649),
		"descending": getRandomSort("studios", "DESC", 42),
		"zero seed":  getRandomSort("studios", "ASC", 0),
	}

	for name, frag := range frags {
		t.Run(name, func(t *testing.T) {
			assert.NotContains(t, frag, "mod(",
				"mod() is Postgres, not SQLite; the % operator is what both have")
			assert.Contains(t, frag, "% 2147483647",
				"the modulo must be present for the sort to be a permutation rather than "+
					"a monotonic function of id")
		})
	}
}

// The modulus is what keeps the ordering value in range: the intermediate
// product overflows int64 for any realistic id, and SQLite silently degrades
// overflowing integers to floats.
func TestGetRandomSort_CarriesTheDocumentedConstants(t *testing.T) {
	frag := getRandomSort("studios", "ASC", 26819649)

	assert.Contains(t, frag, "2147483647",
		"the modulus is what keeps the ordering value inside int32; without it the "+
			"expression overflows and SQLite falls back to float ordering, which is "+
			"slower and imprecise")
	assert.Contains(t, frag, "52959209")
	assert.Contains(t, frag, "1047483763")
	assert.Contains(t, frag, "studios.id",
		"the fragment orders by the row's own id, and qualifies it with the table so a "+
			"joined query cannot sort by the other side of the join")
}

// A seed that does not change the fragment is not a seed. Paging a "random"
// sort that ignores its seed shows the same page forever.
func TestGetRandomSort_SeedChangesTheFragment(t *testing.T) {
	assert.Contains(t, getRandomSort("studios", "ASC", 26819649), "26819649")
	assert.Contains(t, getRandomSort("studios", "ASC", 12345), "12345")
	assert.NotEqual(t,
		getRandomSort("studios", "ASC", 26819649),
		getRandomSort("studios", "ASC", 12345))

	// The direction must reach the fragment too, or ascending and descending
	// random sorts are the same sort.
	assert.NotEqual(t,
		getRandomSort("studios", "ASC", 26819649),
		getRandomSort("studios", "DESC", 26819649))
}

// The seed is used as-is, with no reduction. There used to be a %= 1e8 cap
// whose only purpose was keeping the polynomial inside int64; with the
// reduction moved to the per-id term the cap is not just redundant but wrong,
// because a seed of 1e9 differs from 1e8 by a whole number of periods and so
// yields a near-identical order.
func TestGetRandomSort_SeedIsNotReduced(t *testing.T) {
	assert.Contains(t, getRandomSort("studios", "ASC", 1e9), "1000000000",
		"the seed must be used verbatim; reducing it modulo a cap makes seeds a period "+
			"apart produce the same order")

	// The real guarantee: the per-id reduction is what keeps the arithmetic in
	// range, so the bound appears in the fragment.
	assert.Contains(t, getRandomSort("studios", "ASC", 0), "417314",
		"x must be reduced modulo the polynomial bound before squaring, or the "+
			"product overflows int64 and the sort silently becomes id order")
}
