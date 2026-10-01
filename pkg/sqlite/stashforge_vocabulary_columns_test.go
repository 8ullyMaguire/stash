//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
)

// The vocabulary in internal/collab lists the fields a proposal may change. Each
// one is a COLUMN NAME that the apply path will interpolate into an UPDATE, so
// a field that is not a real column is not a validation gap -- it is a runtime
// SQL error on the first proposal that touches it, discovered by a user.
//
// That is not hypothetical. Spec §4.1 as written listed `studio.url`, and
// `group.title` / `group.details`, none of which exist; groups have `name` and
// `description`. The list had been written from assumption rather than from the
// schema, and nothing in the build could catch it.
//
// So this test reads the real columns off a migrated database and asserts the
// two agree, in both directions: a vocabulary entry with no column fails, and a
// mismatch in the count is reported so a field cannot be quietly added to one
// side only.

// targetTables maps a collab target type to the table the apply path writes to.
//
// Written out here rather than derived from the vocabulary, so that a change to
// the vocabulary's type names is itself a test failure rather than a silent
// rename.
var targetTables = map[string]string{
	"scene":     "scenes",
	"performer": "performers",
	"studio":    "studios",
	"tag":       "tags",
	"gallery":   "galleries",
	"image":     "images",
	"group":     "groups",
}

func TestVocabulary_EveryFieldIsARealColumn(t *testing.T) {
	runWithRollbackTxn(t, "real-columns", func(t *testing.T, ctx context.Context) {
		for targetType, fields := range collab.VocabularyForTest() {
			table, ok := targetTables[targetType]
			require.True(t, ok, "collab has a target type %q with no table mapping; "+
				"the apply path would not know where to write it", targetType)

			columns := tableColumns(ctx, t, table)
			require.NotEmpty(t, columns, "table %s was not found in the migrated database", table)

			for field := range fields {
				assert.Contains(t, columns, field,
					"collab proposes %s.%s, which is not a column of %s -- "+
						"this would be a runtime SQL error on the first proposal, not a validation gap",
					targetType, field, table)
			}
		}
	})
}

// The other direction. A column the apply path could plausibly want, and that
// the vocabulary omits, is a gap in the other direction -- but this cannot be
// asserted as "every column must be proposable", because most columns are
// deliberately not (primary_file_id, created_at, o_counter, and so on).
//
// What this DOES assert is that the count of vocabulary fields per type matches
// what the tests in the pure package expect, so adding a field to the vocabulary
// requires updating the expectations rather than slipping through.
func TestVocabulary_FieldCountsArePinned(t *testing.T) {
	want := map[string]int{
		"scene":     5, // spec §4.1 said 6; scene.url is a join table, not a column
		"performer": 6,
		"studio":    3, // was 4 in spec §4.1; studios have no url column
		"tag":       2,
		// WAS 2. studio_id was added for both on 2026-10-03: the column exists with a
		// foreign key on each, autotag matches studios against both target types, and
		// without it a governed autotag could not file a studio claim for an image or a
		// gallery at all. Found by internal/autotag's end-to-end tests -- the write-side
		// map was missing it too, and neither this guard nor the reverse one can see a
		// column that is real, writable and absent from both.
		"gallery": 3, // title, details, studio_id
		"image":   3, // title, rating, studio_id
		"group":   5, // name, description, date, studio_id, rating
	}

	got := collab.VocabularyForTest()
	for targetType, n := range want {
		assert.Len(t, got[targetType], n,
			"the %s vocabulary changed size; update this test and the pure package's "+
				"TestVocabulary_ProposableFieldsIsScopedAndSorted, which asserts the same list", targetType)
	}
}
