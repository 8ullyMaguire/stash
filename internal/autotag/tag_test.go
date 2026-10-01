package autotag

import (
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/pkg/image"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
	"github.com/stashapp/stash/pkg/scene"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type testTagCase struct {
	tagName       string
	expectedRegex string
	aliasName     string
	aliasRegex    string
}

var (
	testTagCases = []testTagCase{
		{
			"tag name",
			`(?i)(?:^|_|[^\p{L}\d])tag[.\-_ ]*name(?:$|_|[^\p{L}\d])`,
			"",
			"",
		},
		{
			"tag + name",
			`(?i)(?:^|_|[^\p{L}\d])tag[.\-_ ]*\+[.\-_ ]*name(?:$|_|[^\p{L}\d])`,
			"",
			"",
		},
		{
			"tag name",
			`(?i)(?:^|_|[^\p{L}\d])tag[.\-_ ]*name(?:$|_|[^\p{L}\d])`,
			"alias name",
			`(?i)(?:^|_|[^\p{L}\d])alias[.\-_ ]*name(?:$|_|[^\p{L}\d])`,
		},
		{
			"tag + name",
			`(?i)(?:^|_|[^\p{L}\d])tag[.\-_ ]*\+[.\-_ ]*name(?:$|_|[^\p{L}\d])`,
			"alias + name",
			`(?i)(?:^|_|[^\p{L}\d])alias[.\-_ ]*\+[.\-_ ]*name(?:$|_|[^\p{L}\d])`,
		},
	}

	trailingBackslashCases = []testTagCase{
		{
			`tag + name\`,
			`(?i)(?:^|_|[^\p{L}\d])tag[.\-_ ]*\+[.\-_ ]*name\\(?:$|_|[^\p{L}\d])`,
			"",
			"",
		},
		{
			`tag + name\`,
			`(?i)(?:^|_|[^\p{L}\d])tag[.\-_ ]*\+[.\-_ ]*name\\(?:$|_|[^\p{L}\d])`,
			`alias + name\`,
			`(?i)(?:^|_|[^\p{L}\d])alias[.\-_ ]*\+[.\-_ ]*name\\(?:$|_|[^\p{L}\d])`,
		},
	}
)

func TestTagScenes(t *testing.T) {
	t.Parallel()

	tc := testTagCases
	// trailing backslash tests only work where filepath separator is not backslash
	if filepath.Separator != '\\' {
		tc = append(tc, trailingBackslashCases...)
	}

	for _, p := range tc {
		testTagScenes(t, p)
	}
}

func testTagScenes(t *testing.T, tc testTagCase) {
	tagName := tc.tagName
	expectedRegex := tc.expectedRegex
	aliasName := tc.aliasName
	aliasRegex := tc.aliasRegex

	db := mocks.NewDatabase()

	const tagID = 2

	var aliases []string

	testPathName := tagName
	if aliasName != "" {
		aliases = []string{aliasName}
		testPathName = aliasName
	}

	matchingPaths, falsePaths := generateTestPaths(testPathName, "mp4")

	var scenes []*models.Scene
	for i, p := range append(matchingPaths, falsePaths...) {
		scenes = append(scenes, &models.Scene{
			ID:     i + 1,
			Path:   p,
			TagIDs: models.NewRelatedIDs([]int{}),
		})
	}

	tag := models.Tag{
		ID:   tagID,
		Name: tagName,
	}

	organized := false
	perPage := 1000
	sort := "id"
	direction := models.SortDirectionEnumAsc

	expectedSceneFilter := &models.SceneFilterType{
		Organized: &organized,
		Path: &models.StringCriterionInput{
			Value:    expectedRegex,
			Modifier: models.CriterionModifierMatchesRegex,
		},
	}

	expectedFindFilter := &models.FindFilterType{
		PerPage:   &perPage,
		Sort:      &sort,
		Direction: &direction,
	}

	// if alias provided, then don't find by name
	onNameQuery := db.Scene.On("Query", testCtx, scene.QueryOptions(expectedSceneFilter, expectedFindFilter, false))
	if aliasName == "" {
		onNameQuery.Return(mocks.SceneQueryResult(scenes, len(scenes)), nil).Once()
	} else {
		onNameQuery.Return(mocks.SceneQueryResult(nil, 0), nil).Once()

		expectedAliasFilter := &models.SceneFilterType{
			Organized: &organized,
			Path: &models.StringCriterionInput{
				Value:    aliasRegex,
				Modifier: models.CriterionModifierMatchesRegex,
			},
		}

		db.Scene.On("Query", mock.Anything, scene.QueryOptions(expectedAliasFilter, expectedFindFilter, false)).
			Return(mocks.SceneQueryResult(scenes, len(scenes)), nil).Once()
	}

	// THE CLAIM IS ASSERTED, not on the mock. `db.X.On("UpdatePartial", ...,
	// matchPartial)` proved a column write was attempted with the right ids; a
	// recording sink proves the claim reached the right TARGET with the right ENTITY,
	// which is the pair the join-table write depends on and which a matcher on a
	// partial cannot distinguish from a write to the wrong row.
	sink := testSink()

	tagger := Tagger{
		TxnManager: db,
		Sink:       sink,
	}

	err := tagger.TagScenes(testCtx, &tag, nil, aliases, db.Scene)

	assert := assert.New(t)

	assert.Nil(err)
	// AND THE CLAIMS NAME THE MATCHING TARGETS AND ONLY THOSE.
	//
	// The half that matters is the "only those". A mock expectation fails on a MISSING
	// call and is silent on an EXTRA one, so the old assertion could not see a claim
	// filed for a path the regex did not match -- the failure mode of a tagger whose
	// filter is too loose, and invisible to any expectation-based test.
	assert.Len(sink.recorded(), len(matchingPaths),
		"exactly the matching targets must produce a claim")
	for i, m := range sink.recorded() {
		// THE TARGET TOO, not only the entity. A claim naming the right tag on the
		// WRONG target puts a real person in a file they are not in, and the old
		// matcher-on-a-partial could not see it: it matched the entity id, while the
		// target id was the mock's own argument rather than the claim's.
		assert.Equal(i+1, m.TargetID, "claim must name the target that matched")
		assert.Equal(tagID, m.EntityID, "claim must name the entity the path matched")
	}
	db.AssertExpectations(t)
}

func TestTagImages(t *testing.T) {
	t.Parallel()

	for _, p := range testTagCases {
		testTagImages(t, p)
	}
}

func testTagImages(t *testing.T, tc testTagCase) {
	tagName := tc.tagName
	expectedRegex := tc.expectedRegex
	aliasName := tc.aliasName
	aliasRegex := tc.aliasRegex

	db := mocks.NewDatabase()

	const tagID = 2

	var aliases []string

	testPathName := tagName
	if aliasName != "" {
		aliases = []string{aliasName}
		testPathName = aliasName
	}

	var images []*models.Image
	matchingPaths, falsePaths := generateTestPaths(testPathName, "mp4")
	for i, p := range append(matchingPaths, falsePaths...) {
		images = append(images, &models.Image{
			ID:     i + 1,
			Path:   p,
			TagIDs: models.NewRelatedIDs([]int{}),
		})
	}

	tag := models.Tag{
		ID:   tagID,
		Name: tagName,
	}

	organized := false
	perPage := 1000
	sort := "id"
	direction := models.SortDirectionEnumAsc

	expectedImageFilter := &models.ImageFilterType{
		Organized: &organized,
		Path: &models.StringCriterionInput{
			Value:    expectedRegex,
			Modifier: models.CriterionModifierMatchesRegex,
		},
	}

	expectedFindFilter := &models.FindFilterType{
		PerPage:   &perPage,
		Sort:      &sort,
		Direction: &direction,
	}

	// if alias provided, then don't find by name
	onNameQuery := db.Image.On("Query", testCtx, image.QueryOptions(expectedImageFilter, expectedFindFilter, false))
	if aliasName == "" {
		onNameQuery.Return(mocks.ImageQueryResult(images, len(images)), nil).Once()
	} else {
		onNameQuery.Return(mocks.ImageQueryResult(nil, 0), nil).Once()

		expectedAliasFilter := &models.ImageFilterType{
			Organized: &organized,
			Path: &models.StringCriterionInput{
				Value:    aliasRegex,
				Modifier: models.CriterionModifierMatchesRegex,
			},
		}

		db.Image.On("Query", mock.Anything, image.QueryOptions(expectedAliasFilter, expectedFindFilter, false)).
			Return(mocks.ImageQueryResult(images, len(images)), nil).Once()
	}

	// THE CLAIM IS ASSERTED, not on the mock. `db.X.On("UpdatePartial", ...,
	// matchPartial)` proved a column write was attempted with the right ids; a
	// recording sink proves the claim reached the right TARGET with the right ENTITY,
	// which is the pair the join-table write depends on and which a matcher on a
	// partial cannot distinguish from a write to the wrong row.
	sink := testSink()

	tagger := Tagger{
		TxnManager: db,
		Sink:       sink,
	}

	err := tagger.TagImages(testCtx, &tag, nil, aliases, db.Image)

	assert := assert.New(t)

	assert.Nil(err)
	// AND THE CLAIMS NAME THE MATCHING TARGETS AND ONLY THOSE.
	//
	// The half that matters is the "only those". A mock expectation fails on a MISSING
	// call and is silent on an EXTRA one, so the old assertion could not see a claim
	// filed for a path the regex did not match -- the failure mode of a tagger whose
	// filter is too loose, and invisible to any expectation-based test.
	assert.Len(sink.recorded(), len(matchingPaths),
		"exactly the matching targets must produce a claim")
	for i, m := range sink.recorded() {
		// THE TARGET TOO, not only the entity. A claim naming the right tag on the
		// WRONG target puts a real person in a file they are not in, and the old
		// matcher-on-a-partial could not see it: it matched the entity id, while the
		// target id was the mock's own argument rather than the claim's.
		assert.Equal(i+1, m.TargetID, "claim must name the target that matched")
		assert.Equal(tagID, m.EntityID, "claim must name the entity the path matched")
	}
	db.AssertExpectations(t)
}

func TestTagGalleries(t *testing.T) {
	t.Parallel()

	for _, p := range testTagCases {
		testTagGalleries(t, p)
	}
}

func testTagGalleries(t *testing.T, tc testTagCase) {
	tagName := tc.tagName
	expectedRegex := tc.expectedRegex
	aliasName := tc.aliasName
	aliasRegex := tc.aliasRegex

	db := mocks.NewDatabase()

	const tagID = 2

	var aliases []string

	testPathName := tagName
	if aliasName != "" {
		aliases = []string{aliasName}
		testPathName = aliasName
	}

	var galleries []*models.Gallery
	matchingPaths, falsePaths := generateTestPaths(testPathName, "mp4")
	for i, p := range append(matchingPaths, falsePaths...) {
		v := p
		galleries = append(galleries, &models.Gallery{
			ID:     i + 1,
			Path:   v,
			TagIDs: models.NewRelatedIDs([]int{}),
		})
	}

	tag := models.Tag{
		ID:   tagID,
		Name: tagName,
	}

	organized := false
	perPage := 1000
	sort := "id"
	direction := models.SortDirectionEnumAsc

	expectedGalleryFilter := &models.GalleryFilterType{
		Organized: &organized,
		Path: &models.StringCriterionInput{
			Value:    expectedRegex,
			Modifier: models.CriterionModifierMatchesRegex,
		},
	}

	expectedFindFilter := &models.FindFilterType{
		PerPage:   &perPage,
		Sort:      &sort,
		Direction: &direction,
	}

	// if alias provided, then don't find by name
	onNameQuery := db.Gallery.On("Query", testCtx, expectedGalleryFilter, expectedFindFilter)
	if aliasName == "" {
		onNameQuery.Return(galleries, len(galleries), nil).Once()
	} else {
		onNameQuery.Return(nil, 0, nil).Once()

		expectedAliasFilter := &models.GalleryFilterType{
			Organized: &organized,
			Path: &models.StringCriterionInput{
				Value:    aliasRegex,
				Modifier: models.CriterionModifierMatchesRegex,
			},
		}

		db.Gallery.On("Query", mock.Anything, expectedAliasFilter, expectedFindFilter).Return(galleries, len(galleries), nil).Once()
	}

	// THE CLAIM IS ASSERTED, not on the mock. `db.X.On("UpdatePartial", ...,
	// matchPartial)` proved a column write was attempted with the right ids; a
	// recording sink proves the claim reached the right TARGET with the right ENTITY,
	// which is the pair the join-table write depends on and which a matcher on a
	// partial cannot distinguish from a write to the wrong row.
	sink := testSink()

	tagger := Tagger{
		TxnManager: db,
		Sink:       sink,
	}

	err := tagger.TagGalleries(testCtx, &tag, nil, aliases, db.Gallery)

	assert := assert.New(t)

	assert.Nil(err)
	// AND THE CLAIMS NAME THE MATCHING TARGETS AND ONLY THOSE.
	//
	// The half that matters is the "only those". A mock expectation fails on a MISSING
	// call and is silent on an EXTRA one, so the old assertion could not see a claim
	// filed for a path the regex did not match -- the failure mode of a tagger whose
	// filter is too loose, and invisible to any expectation-based test.
	assert.Len(sink.recorded(), len(matchingPaths),
		"exactly the matching targets must produce a claim")
	for i, m := range sink.recorded() {
		// THE TARGET TOO, not only the entity. A claim naming the right tag on the
		// WRONG target puts a real person in a file they are not in, and the old
		// matcher-on-a-partial could not see it: it matched the entity id, while the
		// target id was the mock's own argument rather than the claim's.
		assert.Equal(i+1, m.TargetID, "claim must name the target that matched")
		assert.Equal(tagID, m.EntityID, "claim must name the entity the path matched")
	}
	db.AssertExpectations(t)
}
