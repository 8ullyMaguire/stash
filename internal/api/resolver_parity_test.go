package api

import (
	"context"
	"errors"
	"testing"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// stash#2359 — the resolver layer for the six parity features.
//
// WHY THESE TEST THE RESOLVERS AND NOT A QUERY
// =============================================
//
// The property at risk here is that a field resolver STOPS LOADING and starts returning an empty
// list. gqlgen will not catch that: `codes` is backed by `studio_codes`, so the field cannot be
// bound to a struct field and gqlgen generates a resolver; that resolver compiles, the schema is
// correct, the Go types match, and a client selecting `codes` gets `[]`. Every request succeeds.
// Nothing logs. This is the `PersonCluster.Members` failure documented on `Resolver.PersonCluster`,
// and `TestClusterFieldResolversAreActuallyWired` is the test that catches it.
//
// So each test asserts the store was ASKED and its value came back. The mock's expectations are the
// assertion: a resolver that skipped the load would leave `GetCodes` uncalled and
// `mock.AssertExpectations` would fail, which no assertion on the return value alone would catch --
// because the return value would be an empty list, and "no codes" and "never loaded" look identical
// from outside.
//
// There is no live-schema execution harness in this package, so a test that ran a GraphQL query
// would have to invent one. What proves the schema end to end is elsewhere and is run by
// docs/verify-all.sh: docs/boot-check.sh queries the booted server, and docs/e2e drives it.

func TestStudioResolverCodesLoadsAndReturns(t *testing.T) {
	db := mocks.NewDatabase()
	r := newResolver(db)

	studio := &models.Studio{ID: 1}
	want := []string{"imdb-1234", "tmdb-5678"}

	// ONCE, so a resolver that loaded twice would fail on the mock -- an unnoticed double query on a
	// list field is a real performance bug that a value assertion cannot see.
	db.Studio.On("GetCodes", mock.Anything, 1).Return(want, nil).Once()

	got, err := r.Studio().Codes(testCtx, studio)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	// The second call must NOT hit the store again, because the relationship is now loaded.
	got2, err := r.Studio().Codes(testCtx, studio)
	require.NoError(t, err)
	assert.Equal(t, want, got2)

	db.Studio.AssertExpectations(t)
}

func TestSceneResolverDirectorsLoadsAndReturns(t *testing.T) {
	db := mocks.NewDatabase()
	r := newResolver(db)

	scene := &models.Scene{ID: 1}
	want := []string{"Ana Lapez", "Bo Chen"}

	db.Scene.On("GetDirectors", mock.Anything, 1).Return(want, nil).Once()

	got, err := r.Scene().Directors(testCtx, scene)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	db.Scene.AssertExpectations(t)
}

func TestPerformerResolverTattooAndPiercingLocationsShareOneTable(t *testing.T) {
	db := mocks.NewDatabase()
	r := newResolver(db)

	performer := &models.Performer{ID: 1}

	// TWO calls to the same loader, distinguished only by kind -- which is the whole reason
	// TattooLocations and PiercingLocations are separate relationships rather than one
	// `body_marks` list the caller would have to filter.
	db.Performer.On("GetBodyMarks", mock.Anything, 1, "tattoo").
		Return([]*models.BodyMark{{Location: "left arm"}, {Location: "right shoulder"}}, nil).Once()
	db.Performer.On("GetBodyMarks", mock.Anything, 1, "piercing").
		Return([]*models.BodyMark{{Location: "left ear"}}, nil).Once()

	tattoos, err := r.Performer().TattooLocations(testCtx, performer)
	require.NoError(t, err)
	assert.Equal(t, []string{"left arm", "right shoulder"}, tattoos)

	// The piercing call must NOT reload the tattoos: LoadBodyMarks loads both together, so the
	// second resolver reads an already-populated relationship and the tattoo mock's Once is what
	// proves it.
	piercings, err := r.Performer().PiercingLocations(testCtx, performer)
	require.NoError(t, err)
	assert.Equal(t, []string{"left ear"}, piercings)

	db.Performer.AssertExpectations(t)
}

// TestPerformerResolverBodyMarksReturnsBothKinds checks the field that reaches the description.
//
// TattooLocations and PiercingLocations cannot: they carry a location and nothing else. This is the
// only path to `description`, so if it returned one kind or dropped rows the descriptions would be
// unreachable -- and unreachability here is silent.
func TestPerformerResolverBodyMarksReturnsBothKinds(t *testing.T) {
	db := mocks.NewDatabase()
	r := newResolver(db)

	performer := &models.Performer{ID: 1}
	desc := "a raven"

	db.Performer.On("GetBodyMarks", mock.Anything, 1, "tattoo").
		Return([]*models.BodyMark{{Kind: "tattoo", Location: "left arm", Description: &desc}}, nil).Once()
	db.Performer.On("GetBodyMarks", mock.Anything, 1, "piercing").
		Return([]*models.BodyMark{{Kind: "piercing", Location: "left ear"}}, nil).Once()

	got, err := r.Performer().BodyMarks(testCtx, performer)
	require.NoError(t, err)

	require.Len(t, got, 2, "a performer with one tattoo and one piercing must return two marks")
	assert.Equal(t, "left arm", got[0].Location)
	require.NotNil(t, got[0].Description, "the description is the only thing this field carries that "+
		"the location fields do not; losing it makes the field pointless")
	assert.Equal(t, desc, *got[0].Description)
	assert.Equal(t, "left ear", got[1].Location)

	db.Performer.AssertExpectations(t)
}

// TestBodyMarksIsNeverNil pins the non-null promise.
//
// The schema says `[BodyMark!]!`. A nil slice marshals to `null`, and gqlgen reports that as an
// error on a non-null field rather than as an empty list -- so a performer with no marks would fail
// the query instead of returning `[]`.
func TestBodyMarksIsNeverNil(t *testing.T) {
	db := mocks.NewDatabase()
	r := newResolver(db)

	performer := &models.Performer{ID: 1}

	db.Performer.On("GetBodyMarks", mock.Anything, 1, mock.Anything).
		Return([]*models.BodyMark(nil), nil).Twice()

	got, err := r.Performer().BodyMarks(testCtx, performer)
	require.NoError(t, err)
	assert.NotNil(t, got, "[BodyMark!]! must be [] and never null")
	assert.Empty(t, got)

	db.Performer.AssertExpectations(t)
}

func TestPerformerResolverNationalitiesLoadsAndReturns(t *testing.T) {
	db := mocks.NewDatabase()
	r := newResolver(db)

	performer := &models.Performer{ID: 1}
	// TWO, because #1922 is about dual nationality. A single row here would pass a test that only
	// checked "non-empty" and would be the exact defect the issue describes.
	want := []models.Nationality{{ID: 1, Name: "Spanish"}, {ID: 2, Name: "Argentine"}}

	db.Performer.On("GetNationalities", mock.Anything, 1).Return(want, nil).Once()

	got, err := r.Performer().Nationalities(testCtx, performer)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "Spanish", got[0].Name)
	assert.Equal(t, "Argentine", got[1].Name)

	db.Performer.AssertExpectations(t)
}

func TestAllNationalitiesReturnsAnEmptyListNotNil(t *testing.T) {
	db := mocks.NewDatabase()
	r := newResolver(db)

	db.Performer.On("AllNationalities", mock.Anything).
		Return([]*models.Nationality(nil), nil).Once()

	got, err := r.Query().AllNationalities(testCtx)
	require.NoError(t, err)
	assert.NotNil(t, got, "[Nationality!]! must be [] and never null")
	assert.Empty(t, got)

	db.Performer.AssertExpectations(t)
}

// TestBodyMarkCreateRejectsAnUnknownKind pins validation that keeps a row from being written that no
// location field can ever return.
//
// A mark with kind "tattooo" is written to the table, appears in `body_marks`, and is invisible to
// both `tattoo_locations` and `piercing_locations` -- a row that exists and is unreachable. The
// CHECK constraint in migration 125 is the backstop; this asserts the resolver rejects it first, so
// the caller gets an error naming the valid values instead of a row that will confuse them later.
func TestBodyMarkCreateRejectsAnUnknownKind(t *testing.T) {
	for _, kind := range []string{"tattooo", "", "TATTOO", "scar"} {
		db := mocks.NewDatabase()
		r := newResolver(db)

		_, err := r.Mutation().BodyMarkCreate(testCtx, BodyMarkCreateInput{
			PerformerID: "1",
			Kind:        kind,
			Location:    "left arm",
		})

		assert.Error(t, err, "kind %q was accepted; it must be exactly \"tattoo\" or \"piercing\"", kind)

		// Nothing was written.
		db.Performer.AssertNotCalled(t, "CreateBodyMark", mock.Anything, mock.Anything)
	}
}

func TestBodyMarkCreateAcceptsBothKinds(t *testing.T) {
	for _, kind := range []string{"tattoo", "piercing"} {
		db := mocks.NewDatabase()
		r := newResolver(db)

		want := &models.BodyMark{ID: 7, PerformerID: 1, Kind: kind, Location: "left arm"}
		db.Performer.On("CreateBodyMark", mock.Anything, mock.Anything).
			Return(want, nil).Once()

		got, err := r.Mutation().BodyMarkCreate(testCtx, BodyMarkCreateInput{
			PerformerID: "1",
			Kind:        kind,
			Location:    "left arm",
		})
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, kind, got.Kind)
		assert.Equal(t, "left arm", got.Location)

		db.Performer.AssertExpectations(t)
	}
}

func TestBodyMarkCreateRejectsANonNumericPerformerID(t *testing.T) {
	db := mocks.NewDatabase()
	r := newResolver(db)

	_, err := r.Mutation().BodyMarkCreate(testCtx, BodyMarkCreateInput{
		PerformerID: "not-a-number",
		Kind:        "tattoo",
		Location:    "left arm",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-a-number",
		"the error must name the value it could not convert, or the caller cannot tell which field "+
			"was wrong")
}

// TestBodyMarkDestroyReportsANoOpAsFalseRatherThanAnError pins the destroy convention.
//
// Every other destroy in this package returns success for deleting something that is already gone,
// because a destroy is a request to make a thing absent and it already is. Returning ErrNotFound
// would make a retried delete look like a failure, and a client retrying on error would retry
// forever.
func TestBodyMarkDestroyReportsANoOpAsFalseRatherThanAnError(t *testing.T) {
	db := mocks.NewDatabase()
	r := newResolver(db)

	db.Performer.On("DestroyBodyMark", mock.Anything, 999).
		Return(0, nil).Once()

	got, err := r.Mutation().BodyMarkDestroy(testCtx, BodyMarkDestroyInput{ID: "999"})
	require.NoError(t, err, "deleting a row that was not there must not be an error")
	assert.False(t, got, "a delete that removed no rows should report false")

	db.Performer.AssertExpectations(t)
}

func TestBodyMarkDestroyReportsTrueWhenARowWent(t *testing.T) {
	db := mocks.NewDatabase()
	r := newResolver(db)

	db.Performer.On("DestroyBodyMark", mock.Anything, 7).Return(1, nil).Once()

	got, err := r.Mutation().BodyMarkDestroy(testCtx, BodyMarkDestroyInput{ID: "7"})
	require.NoError(t, err)
	assert.True(t, got)

	db.Performer.AssertExpectations(t)
}

func TestBodyMarkDestroyPropagatesAStoreError(t *testing.T) {
	db := mocks.NewDatabase()
	r := newResolver(db)

	boom := errors.New("database is locked")
	db.Performer.On("DestroyBodyMark", mock.Anything, 7).Return(0, boom).Once()

	got, err := r.Mutation().BodyMarkDestroy(testCtx, BodyMarkDestroyInput{ID: "7"})
	require.Error(t, err)
	assert.False(t, got)
	assert.ErrorIs(t, err, boom)
}

// TestResolversPropagateStoreErrors is the negative case for all of the above: a load that FAILS
// must not be reported as an empty list.
//
// This is the mirror image of the failure these tests exist to catch. "Never loaded" and "loaded
// nothing" both look like `[]`, and only one of them is a bug -- but returning `[]` on error makes
// the first indistinguishable from the second, in the one case where the caller needs to know.
func TestResolversPropagateStoreErrors(t *testing.T) {
	t.Run("studio codes", func(t *testing.T) {
		db := mocks.NewDatabase()
		r := newResolver(db)
		boom := errors.New("database is locked")

		db.Studio.On("GetCodes", mock.Anything, 1).Return(nil, boom).Once()

		got, err := r.Studio().Codes(testCtx, &models.Studio{ID: 1})
		require.Error(t, err)
		assert.Nil(t, got, "an error must not be reported as an empty list")
		assert.ErrorIs(t, err, boom)
	})

	t.Run("scene directors", func(t *testing.T) {
		db := mocks.NewDatabase()
		r := newResolver(db)
		boom := errors.New("database is locked")

		db.Scene.On("GetDirectors", mock.Anything, 1).Return(nil, boom).Once()

		got, err := r.Scene().Directors(testCtx, &models.Scene{ID: 1})
		require.Error(t, err)
		assert.Nil(t, got)
	})

	t.Run("performer nationalities", func(t *testing.T) {
		db := mocks.NewDatabase()
		r := newResolver(db)
		boom := errors.New("database is locked")

		db.Performer.On("GetNationalities", mock.Anything, 1).Return(nil, boom).Once()

		got, err := r.Performer().Nationalities(testCtx, &models.Performer{ID: 1})
		require.Error(t, err)
		assert.Nil(t, got)
	})

	t.Run("all nationalities", func(t *testing.T) {
		db := mocks.NewDatabase()
		r := newResolver(db)
		boom := errors.New("database is locked")

		db.Performer.On("AllNationalities", mock.Anything).Return(nil, boom).Once()

		got, err := r.Query().AllNationalities(testCtx)
		require.Error(t, err)
		assert.Nil(t, got)
	})
}

var _ = context.Background
