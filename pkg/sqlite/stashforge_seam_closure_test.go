//go:build integration
// +build integration

package sqlite_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/sqlite"
)

// EVERY PROPOSABLE FIELD MUST BE WRITABLE, AND EVERY WRITABLE FIELD MUST BE PROPOSABLE.
//
// This is the test that should have caught studio_id on image and gallery, and the two
// gallery link kinds, before internal/autotag's end-to-end tests did. It exists because
// the project had two guards and both looked at ONE DIRECTION of the same seam:
//
//   - TestVocabulary_EveryFieldIsARealColumn reads the propose side (collab's
//     vocabulary) and checks each entry names a real column of the real table. It
//     cannot notice a column that is real, writable, and ABSENT from the vocabulary --
//     which is not a broken field, just an ungoverned one.
//   - nothing checked the reverse, so an approved proposal could fail at the UPDATE
//     with "field is not a mapped column", after a moderator had voted and an audit
//     row had been written.
//
// The consequence of that gap is worth stating plainly, because it is worse than a
// crash. A vote is recorded as applied. The field is unchanged. The audit trail says
// the change happened. Nothing in the system ever reports a discrepancy, so the
// failure is discovered by a person noticing a missing studio months later, and the
// audit row actively misleads the investigation.
//
// SEAM CLOSURE IS THE ASSERTION, IN BOTH DIRECTIONS, AND THAT IS WHY IT IS ONE TEST
// RATHER THAN TWO. A test asserting only "everything proposable is writable" passes
// today and misses a mapping added tomorrow; a test asserting only the reverse passes
// today and misses a vocabulary entry added tomorrow. The two halves fail in opposite
// directions, and the bug class is "one of the two maps grew without the other", so the
// check has to be the equality.
//
// WHY AN INTEGRATION TEST. Both halves need the real schema: collabColumns is
// unexported, and the column side must be read off a migrated database rather than a
// hand-written list -- which is how the gallery link table names were nearly wrong
// again (performers_galleries is entity-first, galleries_tags is target-first, one
// CREATE apart, with no convention to derive either from).
func TestTheProposeSideAndTheWriteSideAreTheSameSurface(t *testing.T) {
	// BOTH SIDES AS THE SYSTEM SEES THEM.
	proposable := collab.VocabularyForTest()
	links := collab.LinksForTest()
	writable := sqlite.WritableColumnsForTest()

	require.NotEmpty(t, proposable, "the vocabulary must be non-empty or this test "+
		"passes vacuously, which is how a write-path guard once reported clean while "+
		"matching nothing")

	// DIRECTION ONE: EVERYTHING PROPOSABLE IS WRITABLE.
	//
	// A field a moderator can vote on that the applier cannot write is a vote that lies.
	for targetType, fields := range proposable {
		for field := range fields {
			tgt, fld := targetType, field
			assert.NotEmpty(t, writable[tgt][fld],
				"%s.%s is PROPOSABLE but has no writable column, so an approved "+
					"proposal for it fails at the UPDATE -- after the vote, the audit "+
					"row and the moderator's decision all say it was applied",
				tgt, fld)
		}
	}

	// AND EVERY LINK IS WRITABLE, which is the same argument for the other namespace.
	// A link is additive rather than an UPDATE, so the check is that the join table
	// shape resolves -- the operation existing but the table missing is the identical
	// failure one layer down.
	for targetType, kinds := range links {
		for _, kind := range kinds {
			tgt, k := targetType, kind
			shape, ok := collab.LookupLink(tgt, k)
			assert.True(t, ok,
				"%s/%s is proposable but has no link shape, so the applier cannot "+
					"resolve a join table for it", tgt, k)
			assert.NotEmpty(t, shape.Table, "%s/%s", tgt, k)
		}
	}

	// DIRECTION TWO: EVERY WRITABLE COLUMN IS PROPOSABLE.
	//
	// This is the half that would have caught the omission, and it is a REAL
	// CONSTRAINT rather than a wish. A column that can be written but cannot be
	// proposed is a field the governance model cannot reach: the direct autotag path
	// can change it, and no vote ever can. That is a machine able to edit content no
	// participant can propose an edit to, which is the asymmetry §6b.2 exists to
	// remove.
	//
	// It is a constraint on the DESIGN, and if a future field is deliberately
	// closed -- a system-managed column, say -- then this assertion is where that
	// decision gets written down, next to the field name, rather than discovered later
	// by someone wondering why autotag can set it but nobody can vote on it.
	for targetType, fields := range writable {
		for field := range fields {
			tgt, fld := targetType, field
			_, isProposable := proposable[tgt][fld]
			assert.True(t, isProposable,
				"%s.%s is WRITABLE but not proposable. Autotag's direct path can "+
					"change it and no vote ever can, so the field is editable by a "+
					"machine and unreachable by a participant. Either add it to the "+
					"vocabulary, or record here why this column is deliberately "+
					"outside the governance model",
				tgt, fld)
		}
	}
}

// AND THE LINK NAMESPACE IS CLOSED IN THE SAME WAY: a target that carries links must
// have every kind it can produce, and ValidateLink must refuse a pair it does not.
//
// The second half is what keeps the map honest. Without it, a new target added with one
// of its two kinds would let a machine file the missing one as... nothing: there is no
// code path that reaches a join table by name, so the omission surfaces as a refusal
// rather than a wrong write. That is the right direction to fail, and this test says so
// rather than leaving it to chance.
func TestALinkKindWithNoShapeIsRefusedRatherThanGuessed(t *testing.T) {
	// A kind that exists as a constant but has no entry for this target.
	_, ok := collab.LookupLink("performer", collab.LinkScenePerformer)
	assert.False(t, ok,
		"a performer is the ENTITY on the far end of a join table, not a target, so "+
			"it carries no links of its own and must be refused rather than mapped to "+
			"some plausible table")

	_, ok = collab.LookupLink("scene", collab.LinkKind("nonsense"))
	assert.False(t, ok, "an unknown kind is refused, not mapped to a plausible table")

	// And the real ones still resolve, so the refusal above is a specific answer and
	// not a map that fails everything.
	for _, targetType := range []string{"scene", "image", "gallery"} {
		kinds := collab.ProposableLinks(targetType)
		require.NotEmpty(t, kinds, "%s must carry links", targetType)
		for _, k := range kinds {
			shape, ok := collab.LookupLink(targetType, k)
			require.True(t, ok, "%s/%s must resolve to a real join table", targetType, k)
			assert.NotEmpty(t, shape.Table)
			assert.NotEmpty(t, shape.IdColumn)
			assert.NotEmpty(t, shape.LinkColumn,
				"a shape with no link column would INSERT NULL")
		}
	}
}
