package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNoRecommendationScoreColumnExists asserts an ABSENCE in the schema.
//
// Why a test at all: a missing column cannot fail a test on its own. Non-
// negotiable #4 says there is no recommendation_score column, and the absence is
// the feature — so it needs a guard, or the first person to add a "temporary"
// column has silently converted a view into a cache and §6a.5's gravity slider
// stops taking effect without a migration.
//
// How it searches: the migration files, because that is where a column would be
// born. The test lives in internal/discovery rather than in a schema package
// because the invariant belongs to the ranking, and a guard that lives next to
// the thing it guards is one somebody will read.
func TestNoRecommendationScoreColumnExists(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "pkg", "sqlite", "migrations")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "migrations dir must exist for this guard to mean anything")

	checked := 0
	// Columns that would be a stored score. Deliberately several spellings: the
	// natural name is not the only one a person would reach for under pressure,
	// and a guard that only knows one spelling is one rename away from useless.
	forbidden := []string{
		"recommendation_score",
		"recommendation_rank",
		"discovery_score",
		"taste_score",
		"taste_fingerprint", // the fingerprint is DERIVED (§6a.3), never stored
		"fingerprint_blob",
		"peer_similarity",
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		checked++
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)

		lower := strings.ToLower(string(body))
		for _, col := range forbidden {
			assert.NotContains(t, lower, col,
				"%s declares %q. §6a.5 and non-negotiable #4: the ranking is a VIEW "+
					"computed at query time from records. A stored score means a term "+
					"replaced in the config needs a recompute pass before it takes "+
					"effect, which is the opposite of the gravity slider.",
				e.Name(), col)
		}
	}

	assert.Greater(t, checked, 100,
		"the guard read %d migration files; if that number collapsed, it is no "+
			"longer looking at the real schema and would pass vacuously", checked)
}

// repoRoot walks up from the test's directory to the module root, so the guard
// does not depend on the working directory the suite happens to be run from.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)

	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "walked up to the filesystem root without finding go.mod")
		dir = parent
	}
	t.Fatal("no go.mod found above the test directory")
	return ""
}
