package collab_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
)

func ptr(s string) *string { return &s }

// A field outside the vocabulary is a 422, never a 500. The map is the whole
// enforcement mechanism, so a test that only checked valid fields would leave
// the security boundary unverified.
func TestVocabulary_RejectsFieldOutsideVocabulary(t *testing.T) {
	// Not in the spec's list at all.
	assert.ErrorIs(t, collab.ValidateValue("scene", "rating_hash", ptr("x")),
		collab.ErrFieldNotProposable,
		"a field nobody declared proposable must be refused")

	// A real column that the vocabulary deliberately leaves out. `scenes` has
	// plenty of columns; only these six are proposable, and the refusal is the
	// feature rather than an omission.
	assert.ErrorIs(t, collab.ValidateValue("scene", "studio_id_legacy", ptr("1")),
		collab.ErrFieldNotProposable)

	// rating is a FIELD of image, not of scene. A per-type field is not a global
	// one, and treating the vocabulary as flat is the mistake this catches.
	assert.ErrorIs(t, collab.ValidateValue("scene", "rating", ptr("4")),
		collab.ErrFieldNotProposable)
	assert.NoError(t, collab.ValidateValue("image", "rating", ptr("4")),
		"rating is proposable on image, so the check above is a type-scoping test and not a blanket refusal")
}

// An unparseable value is a 422 at PROPOSAL time. A value that will fail at
// apply time must never reach a vote.
func TestVocabulary_RejectsUnparseableValueAtProposalTime(t *testing.T) {
	// A foreign key that is not a number.
	assert.ErrorIs(t, collab.ValidateValue("scene", "studio_id", ptr("not-a-number")),
		collab.ErrValueInvalid)

	// A negative id is syntactically fine and semantically meaningless, and a
	// -1 reaching the apply path would either no-op or be written as a real row.
	assert.ErrorIs(t, collab.ValidateValue("scene", "studio_id", ptr("-1")),
		collab.ErrValueInvalid)
	assert.ErrorIs(t, collab.ValidateValue("scene", "studio_id", ptr("0")),
		collab.ErrValueInvalid, "0 is not a row id")

	// A date that is the right SHAPE and the wrong date. This is the case the
	// split between proposal-time and apply-time validation exists for: without
	// the leap-year check, 2023-02-29 passes here and fails later.
	assert.ErrorIs(t, collab.ValidateValue("scene", "date", ptr("2023-02-29")),
		collab.ErrValueInvalid, "2023 was not a leap year")
	assert.ErrorIs(t, collab.ValidateValue("scene", "date", ptr("2024-13-01")),
		collab.ErrValueInvalid, "month 13 does not exist")
	assert.ErrorIs(t, collab.ValidateValue("scene", "date", ptr("2024-04-31")),
		collab.ErrValueInvalid, "April has 30 days")
	assert.ErrorIs(t, collab.ValidateValue("scene", "date", ptr("yesterday")),
		collab.ErrValueInvalid)

	// Digits in the right places but the separators are not dashes. Every
	// SUBSTRING here still parses as an integer, so the only thing that can
	// reject it is the shape check -- which is what makes this case distinct
	// from the ones above rather than a sixth way of saying "not a date".
	assert.ErrorIs(t, collab.ValidateValue("scene", "date", ptr("2024/02/29")),
		collab.ErrValueInvalid, "ISO dates use dashes; a slashed date is a different format entirely")
	assert.ErrorIs(t, collab.ValidateValue("scene", "date", ptr("20240229")),
		collab.ErrValueInvalid, "an undashed date would be ambiguous with an integer id")
	assert.ErrorIs(t, collab.ValidateValue("scene", "date", ptr("2024-02-29T00:00:00Z")),
		collab.ErrValueInvalid, "a timestamp is not a calendar date; a time component would "+
			"be applied as a date and lose the time, or fail at apply time")
	assert.NoError(t, collab.ValidateValue("scene", "date", ptr("2024-02-29")),
		"2024 WAS a leap year, so the rule must not be a blanket rejection of Feb 29")

	// A rating outside its bounds, and a half-rating on a five-point scale.
	assert.ErrorIs(t, collab.ValidateValue("image", "rating", ptr("6")), collab.ErrValueInvalid)
	assert.ErrorIs(t, collab.ValidateValue("image", "rating", ptr("0")), collab.ErrValueInvalid)
	assert.ErrorIs(t, collab.ValidateValue("image", "rating", ptr("4.5")), collab.ErrValueInvalid,
		"a five-point scale with halves in it is a different scale; admitting it here would "+
			"mean a rating that proposes fine and applies as a different number than it showed")
	assert.NoError(t, collab.ValidateValue("image", "rating", ptr("4.0")),
		"4.0 is the same number as 4 and must not be refused for its spelling")
}

// Control characters in a string field. These are the one string problem that
// belongs here rather than to the column's length limit, because they are how
// one line of user text forges another in a log, a terminal, or a CSV export.
func TestVocabulary_RejectsControlCharacters(t *testing.T) {
	assert.ErrorIs(t, collab.ValidateValue("scene", "title", ptr("legit\x00title")),
		collab.ErrValueInvalid, "a NUL in a title is never legitimate")
	assert.ErrorIs(t, collab.ValidateValue("scene", "title", ptr("line\x1b[31mred")),
		collab.ErrValueInvalid, "an ANSI escape in a title forges terminal output")
	assert.ErrorIs(t, collab.ValidateValue("scene", "title", ptr("del\x7fchar")),
		collab.ErrValueInvalid)

	// Line breaks are legitimate in a details field, so a blanket ban would be
	// wrong as well as useless.
	assert.NoError(t, collab.ValidateValue("scene", "details", ptr("line one\nline two")),
		"newlines are legitimate prose")
	assert.NoError(t, collab.ValidateValue("scene", "details", ptr("tab\there")),
		"tabs are legitimate prose")
}

// A nil value means "clear this field", which is a real edit and is exactly why
// the schema keeps NULL and "" apart. A validator that rejected it would make
// clearing a field unexpressible.
func TestVocabulary_NilValueIsAValidEdit(t *testing.T) {
	assert.NoError(t, collab.ValidateValue("scene", "title", nil),
		"clearing a title is a legitimate proposal")
	assert.NoError(t, collab.ValidateValue("scene", "date", nil))
	assert.NoError(t, collab.ValidateValue("image", "rating", nil),
		"clearing a rating is legitimate even though ratings are bounded")
}

// proposableFieldView is a test-local pair for reading one field's bounds by name.
type proposableFieldView struct{ Min, Max int }

// ProposableFields is what the UI renders a form from, so it must be stable and
// must not leak fields from other target types.
func TestVocabulary_ProposableFieldsIsScopedAndSorted(t *testing.T) {
	scene := collab.ProposableFields("scene")
	// FIVE, and this is the SECOND time this assertion has fired on this count -- the
	// first was the url removal. It earns its keep: on 2026-10-03 it caught a change
	// I had just made and was about to commit.
	//
	// I added performer_ids and tag_ids to the scene and image vocabularies to close
	// step 8.2's second recorded gap, and this file said "7" while pkg/sqlite's
	// TestVocabulary_EveryFieldIsARealColumn said something better: those are not
	// columns of scenes or images. They are JOIN TABLES, and the apply path
	// interpolates a vocabulary field straight into an UPDATE. So the change would
	// have validated, filed, approved and then failed as a SQL error on the first
	// proposal a user touched -- the exact shape of defect this test was written for
	// when spec §4.1's studio.url was found to be a fiction.
	//
	// The change is reverted and the count is back to five. The gap is REAL and still
	// open, and the honest record of it is the comment in vocabulary.go rather than a
	// field that works in validation and dies in SQL.
	require.Len(t, scene, 5,
		"five proposable scene fields: spec §4.1 listed six, but scene.url is not a "+
			"column -- and performer_ids/tag_ids are join tables, not columns either")

	var names []string
	for _, f := range scene {
		names = append(names, f.Field)
	}
	assert.Equal(t, []string{"date", "details", "director", "studio_id", "title"}, names,
		"the order must be stable; a Go map iteration order would reshuffle the form "+
			"on every render")

	// Image fields sort as rating, title -- so the bounds live on index 0, and
	// finding them by INDEX is itself the bug this version of the test had.
	image := collab.ProposableFields("image")
	require.Len(t, image, 2)
	assert.Equal(t, "rating", image[0].Field, "sorted: rating before title")
	assert.Equal(t, 1, image[0].Min, "the rating bounds are part of the type, so a form can render them")
	assert.Equal(t, 5, image[0].Max)
	assert.Equal(t, "title", image[1].Field)

	assert.Nil(t, collab.ProposableFields("nonsense"),
		"an unknown target type has no fields, not an error and not every field")
}

// The error for an unknown TARGET TYPE must not be distinguishable from the
// error for an unknown field, or the error messages become a schema oracle.
func TestVocabulary_UnknownTargetIsIndistinguishableFromUnknownField(t *testing.T) {
	unknownTarget := collab.ValidateValue("nonsense", "title", ptr("x"))
	unknownField := collab.ValidateValue("scene", "nonsense", ptr("x"))

	assert.Equal(t, unknownTarget, unknownField,
		"distinguishing these two lets a caller enumerate the schema through error messages; "+
			"this is the same mistake as the invite key returning different errors per failure mode")
}
