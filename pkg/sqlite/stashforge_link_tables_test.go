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

// EVERY LINK TABLE AND COLUMN IS REAL, read off a migrated database.
//
// This is the same guard as TestVocabulary_EveryFieldIsARealColumn, pointed at
// internal/collab/link.go instead of vocabulary.go, and it exists for the same reason:
// a name in that map is interpolated into an INSERT, so a name that is not a real
// table or column is a runtime SQL error on the first APPROVED proposal -- discovered
// by a user, in the one code path that is supposed to be safe.
//
// It is here rather than being a comment because I got it wrong immediately. My first
// version of link.go named the tables scene_performers, image_performers, scene_tags
// and image_tags. All four were plausible, all four were wrong: the schema says
// performers_scenes, scenes_tags, performers_images, images_tags. Two are named after
// the entity first and two after the target first, so there is no convention to derive
// them from -- and the "obvious" naming is the wrong one. A guard that reads the real
// database catches that; a comment that says "the join table" does not.
func TestEveryLinkTableAndColumnIsReal(t *testing.T) {
	runWithRollbackTxn(t, "real-links", func(t *testing.T, ctx context.Context) {
		// The target types that can carry links, restated rather than derived so a
		// change to the map's shape is itself visible here.
		targets := []string{"scene", "image"}

		checked := 0
		for _, target := range targets {
			kinds := collab.ProposableLinks(target)
			require.NotEmpty(t, kinds,
				"no links are proposable for %s. If the link map was emptied, this "+
					"test passes while checking nothing", target)

			for _, kind := range kinds {
				shape, err := collab.ValidateLink(target, kind)
				require.NoError(t, err)

				columns := tableColumns(ctx, t, shape.Table)
				require.NotEmpty(t, columns,
					"table %s is named by collab's link map for %s/%s but does not "+
						"exist in the migrated database -- this is the runtime SQL error "+
						"this guard was written to prevent",
					shape.Table, target, kind)
				checked++

				assert.Contains(t, columns, shape.IdColumn,
					"collab's link map says %s/%s targets %s.%s, which is not a column "+
						"of that table",
					target, kind, shape.Table, shape.IdColumn)
				assert.Contains(t, columns, shape.LinkColumn,
					"collab's link map says %s/%s links via %s.%s, which is not a "+
						"column of that table",
					target, kind, shape.Table, shape.LinkColumn)
			}
		}

		// So the loop cannot pass by finding no links at all -- the vacuous pass this
		// repo has hit before, where a scanner reported clean while matching nothing.
		assert.NotZero(t, checked,
			"no link table was checked. A guard that examines nothing and reports "+
				"success is worse than no guard, because it looks like evidence")
	})
}

// AND NO LINK TABLE IS ALSO A VOCABULARY COLUMN, in either direction. The two
// namespaces must stay disjoint: a name that means "UPDATE this column" in one and
// "INSERT into this join table" in the other is a name whose meaning depends on which
// map you consulted, and the apply path is the place that has to choose.
func TestLinkTablesAndVocabularyColumnsDoNotOverlap(t *testing.T) {
	// The vocabulary's fields are COLUMN names on the target's own table, so the
	// comparison that matters is against those column lists, not against the join
	// tables -- a join table having a "scene_id" column is expected, and not a clash.
	// What must not happen is a vocabulary field naming a link kind, because then
	// Proposer.Create would try to UPDATE a column that does not exist.
	linkKinds := map[string]bool{}
	for _, target := range []string{"scene", "image"} {
		for _, k := range collab.ProposableLinks(target) {
			linkKinds[string(k)] = true
		}
	}

	for _, target := range []string{"scene", "image"} {
		for _, f := range collab.ProposableFields(target) {
			if linkKinds[f.Field] {
				t.Errorf("%s.%s is BOTH a vocabulary column and a link kind. A field "+
					"whose meaning depends on which map you consulted is a field the "+
					"apply path can get wrong, and the two maps mean genuinely "+
					"different operations: an UPDATE and an INSERT.", target, f.Field)
			}
		}
	}
}
