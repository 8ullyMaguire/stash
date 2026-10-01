package curate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCompletionInputsAreNotColumns is the schema half of the plan's warning.
//
// §6a.15 / step 7.6c: "'how many snapshots does this scene have' is a number
// someone will put in a column, and it must be a COUNT in the view."
//
// A behavioural test cannot catch that. A column and a COUNT both answer 3, so
// TestCompletionInputsAreCountsInTheView passes either way — the two are
// indistinguishable from the outside. Only the schema can tell them apart, which is
// why this test exists alongside that one rather than instead of it.
//
// It searches the migrations because that is where a column is born. The test lives
// in internal/curate rather than in a schema package because the invariant belongs
// to the feature, and a guard that lives next to the thing it guards is one somebody
// will actually read.
func assertNoCoverageColumn(t *testing.T) {
	t.Helper()

	root := repoRoot(t)
	dir := filepath.Join(root, "pkg", "sqlite", "migrations")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "migrations dir must exist for this guard to mean anything")

	// Both spellings, plus the two obvious variants of each. A guard that only
	// knows one spelling is one rename away from useless.
	forbidden := []string{
		"snapshot_count",
		"snapshot_coverage",
		"num_snapshots",
		"performer_count",
		"linked_performers",
		"link_count",
		"coverage_score",
	}

	checked := 0
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
				"%s declares %q. §6a.15: completion is a VIEW computed from records. "+
					"A stored count needs a trigger on every insert AND every delete, and "+
					"rows disappear via imports, federated sync and retention sweeps -- "+
					"none of which this code owns. A stale completion bar is worse than "+
					"an expensive one.", e.Name(), col)
		}
	}

	assert.Greater(t, checked, 100,
		"the guard read %d migration files; if that collapsed, it is no longer "+
			"looking at the real schema and would pass vacuously", checked)
}

// repoRoot walks up from the test's directory to the module root, so the guard does
// not depend on the working directory the suite happens to be run from.
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

// fieldNamesOf lists a struct's fields, so the "this is not a row" assertions can be
// made about the SHAPE. Reflection is the point: a test that reads the values of a
// field which never exists cannot see it.
func fieldNamesOf(v any) []string {
	t := reflect.TypeOf(v)
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, t.Field(i).Name)
	}
	return out
}
