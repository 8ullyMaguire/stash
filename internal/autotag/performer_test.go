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

func TestPerformerScenes(t *testing.T) {
	t.Parallel()

	type test struct {
		performerName string
		expectedRegex string
	}

	performerNames := []test{
		{
			"performer name",
			`(?i)(?:^|_|[^\p{L}\d])performer[.\-_ ]*name(?:$|_|[^\p{L}\d])`,
		},
		{
			"performer + name",
			`(?i)(?:^|_|[^\p{L}\d])performer[.\-_ ]*\+[.\-_ ]*name(?:$|_|[^\p{L}\d])`,
		},
	}

	// trailing backslash tests only work where filepath separator is not backslash
	if filepath.Separator != '\\' {
		performerNames = append(performerNames, test{
			`performer + name\`,
			`(?i)(?:^|_|[^\p{L}\d])performer[.\-_ ]*\+[.\-_ ]*name\\(?:$|_|[^\p{L}\d])`,
		})
	}

	for _, p := range performerNames {
		testPerformerScenes(t, p.performerName, p.expectedRegex)
	}
}

func testPerformerScenes(t *testing.T, performerName, expectedRegex string) {
	db := mocks.NewDatabase()

	const performerID = 2

	var scenes []*models.Scene
	matchingPaths, falsePaths := generateTestPaths(performerName, "mp4")
	for i, p := range append(matchingPaths, falsePaths...) {
		scenes = append(scenes, &models.Scene{
			ID:           i + 1,
			Path:         p,
			PerformerIDs: models.NewRelatedIDs([]int{}),
		})
	}

	performer := models.Performer{
		ID:      performerID,
		Name:    performerName,
		Aliases: models.NewRelatedStrings([]string{}),
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

	db.Scene.On("Query", mock.Anything, scene.QueryOptions(expectedSceneFilter, expectedFindFilter, false)).
		Return(mocks.SceneQueryResult(scenes, len(scenes)), nil).Once()

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

	err := tagger.PerformerScenes(testCtx, &performer, nil, db.Scene)

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
		// THE TARGET TOO, not only the entity. A claim naming the right performer on the
		// WRONG target puts a real person in a file they are not in, and the old
		// matcher-on-a-partial could not see it: it matched the entity id, while the
		// target id was the mock's own argument rather than the claim's.
		assert.Equal(i+1, m.TargetID, "claim must name the target that matched")
		assert.Equal(performerID, m.EntityID, "claim must name the entity the path matched")
	}
	db.AssertExpectations(t)
}

func TestPerformerImages(t *testing.T) {
	t.Parallel()

	type test struct {
		performerName string
		expectedRegex string
	}

	performerNames := []test{
		{
			"performer name",
			`(?i)(?:^|_|[^\p{L}\d])performer[.\-_ ]*name(?:$|_|[^\p{L}\d])`,
		},
		{
			"performer + name",
			`(?i)(?:^|_|[^\p{L}\d])performer[.\-_ ]*\+[.\-_ ]*name(?:$|_|[^\p{L}\d])`,
		},
	}

	for _, p := range performerNames {
		testPerformerImages(t, p.performerName, p.expectedRegex)
	}
}

func testPerformerImages(t *testing.T, performerName, expectedRegex string) {
	db := mocks.NewDatabase()

	const performerID = 2

	var images []*models.Image
	matchingPaths, falsePaths := generateTestPaths(performerName, imageExt)
	for i, p := range append(matchingPaths, falsePaths...) {
		images = append(images, &models.Image{
			ID:           i + 1,
			Path:         p,
			PerformerIDs: models.NewRelatedIDs([]int{}),
		})
	}

	performer := models.Performer{
		ID:      performerID,
		Name:    performerName,
		Aliases: models.NewRelatedStrings([]string{}),
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

	db.Image.On("Query", mock.Anything, image.QueryOptions(expectedImageFilter, expectedFindFilter, false)).
		Return(mocks.ImageQueryResult(images, len(images)), nil).Once()

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

	err := tagger.PerformerImages(testCtx, &performer, nil, db.Image)

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
		// THE TARGET TOO, not only the entity. A claim naming the right performer on the
		// WRONG target puts a real person in a file they are not in, and the old
		// matcher-on-a-partial could not see it: it matched the entity id, while the
		// target id was the mock's own argument rather than the claim's.
		assert.Equal(i+1, m.TargetID, "claim must name the target that matched")
		assert.Equal(performerID, m.EntityID, "claim must name the entity the path matched")
	}
	db.AssertExpectations(t)
}

func TestPerformerGalleries(t *testing.T) {
	t.Parallel()

	type test struct {
		performerName string
		expectedRegex string
	}

	performerNames := []test{
		{
			"performer name",
			`(?i)(?:^|_|[^\p{L}\d])performer[.\-_ ]*name(?:$|_|[^\p{L}\d])`,
		},
		{
			"performer + name",
			`(?i)(?:^|_|[^\p{L}\d])performer[.\-_ ]*\+[.\-_ ]*name(?:$|_|[^\p{L}\d])`,
		},
	}

	for _, p := range performerNames {
		testPerformerGalleries(t, p.performerName, p.expectedRegex)
	}
}

func testPerformerGalleries(t *testing.T, performerName, expectedRegex string) {
	db := mocks.NewDatabase()

	const performerID = 2

	var galleries []*models.Gallery
	matchingPaths, falsePaths := generateTestPaths(performerName, galleryExt)
	for i, p := range append(matchingPaths, falsePaths...) {
		v := p
		galleries = append(galleries, &models.Gallery{
			ID:           i + 1,
			Path:         v,
			PerformerIDs: models.NewRelatedIDs([]int{}),
		})
	}

	performer := models.Performer{
		ID:      performerID,
		Name:    performerName,
		Aliases: models.NewRelatedStrings([]string{}),
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

	db.Gallery.On("Query", mock.Anything, expectedGalleryFilter, expectedFindFilter).Return(galleries, len(galleries), nil).Once()

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

	err := tagger.PerformerGalleries(testCtx, &performer, nil, db.Gallery)

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
		// THE TARGET TOO, not only the entity. A claim naming the right performer on the
		// WRONG target puts a real person in a file they are not in, and the old
		// matcher-on-a-partial could not see it: it matched the entity id, while the
		// target id was the mock's own argument rather than the claim's.
		assert.Equal(i+1, m.TargetID, "claim must name the target that matched")
		assert.Equal(performerID, m.EntityID, "claim must name the entity the path matched")
	}
	db.AssertExpectations(t)
}
