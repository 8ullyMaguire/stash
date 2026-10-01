package autotag

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

const sceneExt = "mp4"

var testSeparators = []string{
	".",
	"-",
	"_",
	" ",
}

var testEndSeparators = []string{
	"{",
	"}",
	"(",
	")",
	",",
}

// asserts that got == expected
// ignores expected.UpdatedAt, but ensures that got.UpdatedAt is set and not null
func scenePartialsEqual(got, expected models.ScenePartial) bool {
	// updated at should be set and not null
	if !got.UpdatedAt.Set || got.UpdatedAt.Null {
		return false
	}
	// else ignore the exact value
	got.UpdatedAt = models.OptionalTime{}

	return assert.ObjectsAreEqual(got, expected)
}

func generateNamePatterns(name, separator, ext string) []string {
	var ret []string
	ret = append(ret, fmt.Sprintf("%s%saaa.%s", name, separator, ext))
	ret = append(ret, fmt.Sprintf("aaa%s%s.%s", separator, name, ext))
	ret = append(ret, fmt.Sprintf("aaa%s%s%sbbb.%s", separator, name, separator, ext))
	ret = append(ret, filepath.Join("dir", fmt.Sprintf("%s%saaa.%s", name, separator, ext)))
	ret = append(ret, filepath.Join(fmt.Sprintf("dir%sdir", separator), fmt.Sprintf("%s%saaa.%s", name, separator, ext)))
	ret = append(ret, filepath.Join(fmt.Sprintf("%s%saaa", name, separator), "dir", fmt.Sprintf("bbb.%s", ext)))
	ret = append(ret, filepath.Join("dir", fmt.Sprintf("%s%s", name, separator), fmt.Sprintf("aaa.%s", ext)))

	return ret
}

func generateSplitNamePatterns(name, separator, ext string) []string {
	var ret []string
	splitted := strings.Split(name, " ")
	// only do this for names that are split into two
	if len(splitted) == 2 {
		ret = append(ret, fmt.Sprintf("%s%s%s.%s", splitted[0], separator, splitted[1], ext))
	}

	return ret
}

func generateFalseNamePatterns(name string, separator, ext string) []string {
	splitted := strings.Split(name, " ")

	var ret []string
	// only do this for names that are split into two
	if len(splitted) == 2 {
		ret = append(ret, fmt.Sprintf("%s%saaa%s%s.%s", splitted[0], separator, separator, splitted[1], ext))
	}

	return ret
}

func generateTestPaths(testName, ext string) (scenePatterns []string, falseScenePatterns []string) {
	separators := testSeparators
	separators = append(separators, testEndSeparators...)

	for _, separator := range separators {
		scenePatterns = append(scenePatterns, generateNamePatterns(testName, separator, ext)...)
		scenePatterns = append(scenePatterns, generateNamePatterns(strings.ToLower(testName), separator, ext)...)
		scenePatterns = append(scenePatterns, generateNamePatterns(strings.ReplaceAll(testName, " ", ""), separator, ext)...)
		falseScenePatterns = append(falseScenePatterns, generateFalseNamePatterns(testName, separator, ext)...)
	}

	// add test cases for intra-name separators
	for _, separator := range testSeparators {
		if separator != " " {
			scenePatterns = append(scenePatterns, generateNamePatterns(strings.ReplaceAll(testName, " ", separator), separator, ext)...)
		}
	}

	// add basic false scenarios
	falseScenePatterns = append(falseScenePatterns, fmt.Sprintf("aaa%s.%s", testName, ext))
	falseScenePatterns = append(falseScenePatterns, fmt.Sprintf("%saaa.%s", testName, ext))

	// add path separator false scenarios
	falseScenePatterns = append(falseScenePatterns, generateFalseNamePatterns(testName, string(filepath.Separator), ext)...)

	// split patterns only valid for ._- and whitespace
	for _, separator := range testSeparators {
		scenePatterns = append(scenePatterns, generateSplitNamePatterns(testName, separator, ext)...)
	}

	// false patterns for other separators
	for _, separator := range testEndSeparators {
		falseScenePatterns = append(falseScenePatterns, generateSplitNamePatterns(testName, separator, ext)...)
	}

	return
}

type pathTestTable struct {
	Path    string
	Matches bool
}

func generateTestTable(testName, ext string) []pathTestTable {
	var ret []pathTestTable

	var scenePatterns []string
	var falseScenePatterns []string

	separators := testSeparators
	separators = append(separators, testEndSeparators...)

	for _, separator := range separators {
		scenePatterns = append(scenePatterns, generateNamePatterns(testName, separator, ext)...)
		scenePatterns = append(scenePatterns, generateNamePatterns(strings.ToLower(testName), separator, ext)...)
		falseScenePatterns = append(falseScenePatterns, generateFalseNamePatterns(testName, separator, ext)...)
	}

	for _, p := range scenePatterns {
		t := pathTestTable{
			Path:    p,
			Matches: true,
		}

		ret = append(ret, t)
	}

	for _, p := range falseScenePatterns {
		t := pathTestTable{
			Path:    p,
			Matches: false,
		}

		ret = append(ret, t)
	}

	return ret
}

func TestScenePerformers(t *testing.T) {
	t.Parallel()

	const sceneID = 1
	const performerName = "performer name"
	const performerID = 2
	performer := models.Performer{
		ID:      performerID,
		Name:    performerName,
		Aliases: models.NewRelatedStrings([]string{}),
	}

	const reversedPerformerName = "name performer"
	const reversedPerformerID = 3
	reversedPerformer := models.Performer{
		ID:      reversedPerformerID,
		Name:    reversedPerformerName,
		Aliases: models.NewRelatedStrings([]string{}),
	}

	testTables := generateTestTable(performerName, sceneExt)

	assert := assert.New(t)

	for _, test := range testTables {
		db := mocks.NewDatabase()

		db.Performer.On("Query", testCtx, mock.Anything, mock.Anything).Return(nil, 0, nil)
		db.Performer.On("QueryForAutoTag", testCtx, mock.Anything).Return([]*models.Performer{&performer, &reversedPerformer}, nil).Once()

		scene := models.Scene{
			ID:           sceneID,
			Path:         test.Path,
			PerformerIDs: models.NewRelatedIDs([]int{}),
		}

		// THE CLAIM IS ASSERTED, not on the mock. `db.Scene.On("UpdatePartial", ...)`
		// proved a column write was attempted; a recording sink proves the claim reached
		// the right TARGET with the right ENTITY, which is the pair the write depends on
		// and which a matcher on a partial cannot see.
		sink := testSink()

		err := ScenePerformers(testCtx, &scene, db.Scene, db.Performer, nil, sink)

		assert.Nil(err)
		// A CLAIM IFF THE PATH MATCHED. Asserting that a claim arrived is only half
		// the test; the other half is that NO claim arrives for a path that did not match.
		//
		// That half is what the mock expectation could not see. A mock fails on a MISSING
		// call and is silent on an EXTRA one, so a tagger whose matcher was too loose
		// passed the old suite -- and a claim filed for the wrong path is a real person
		// attributed to a file they are not in.
		if test.Matches {
			assert.Len(sink.recorded(), 1, "a matching path must produce exactly one claim")
			if len(sink.recorded()) == 1 {
				assert.Equal(performerID, sink.recorded()[0].EntityID,
					"the claim must name the entity the path matched")
				assert.Equal(sceneID, sink.recorded()[0].TargetID,
					"and the target that matched -- the right entity on the wrong target "+
						"is a real corruption, and the pair is what the write needs")
			}
		} else {
			assert.Empty(sink.recorded(),
				"a NON-matching path must produce no claim")
		}

		db.AssertExpectations(t)
	}
}

func TestSceneStudios(t *testing.T) {
	t.Parallel()

	var (
		sceneID    = 1
		studioName = "studio name"
		studioID   = 2
	)
	studio := models.Studio{
		ID:   studioID,
		Name: studioName,
	}

	const reversedStudioName = "name studio"
	const reversedStudioID = 3
	reversedStudio := models.Studio{
		ID:   reversedStudioID,
		Name: reversedStudioName,
	}

	testTables := generateTestTable(studioName, sceneExt)

	assert := assert.New(t)

	doTest := func(db *mocks.Database, test pathTestTable) {
		scene := models.Scene{
			ID:   sceneID,
			Path: test.Path,
		}
		// THE CLAIM IS ASSERTED, not on the mock. `db.Scene.On("UpdatePartial", ...)`
		// proved a column write was attempted; a recording sink proves the claim reached
		// the right TARGET with the right ENTITY, which is the pair the write depends on
		// and which a matcher on a partial cannot see.
		sink := testSink()

		err := SceneStudios(testCtx, &scene, db.Scene, db.Studio, nil, sink)

		assert.Nil(err)
		// A CLAIM IFF THE PATH MATCHED. Asserting that a claim arrived is only half
		// the test; the other half is that NO claim arrives for a path that did not match.
		//
		// That half is what the mock expectation could not see. A mock fails on a MISSING
		// call and is silent on an EXTRA one, so a tagger whose matcher was too loose
		// passed the old suite -- and a claim filed for the wrong path is a real person
		// attributed to a file they are not in.
		if test.Matches {
			assert.Len(sink.recordedStudios(), 1, "a matching path must produce exactly one claim")
			if len(sink.recordedStudios()) == 1 {
				assert.Equal(studioID, sink.recordedStudios()[0].StudioID,
					"the claim must name the entity the path matched")
				assert.Equal(sceneID, sink.recordedStudios()[0].TargetID,
					"and the target that matched -- the right entity on the wrong target "+
						"is a real corruption, and the pair is what the write needs")
			}
		} else {
			assert.Empty(sink.recordedStudios(),
				"a NON-matching path must produce no claim")
		}

		db.AssertExpectations(t)
	}

	for _, test := range testTables {
		db := mocks.NewDatabase()

		db.Studio.On("Query", testCtx, mock.Anything, mock.Anything).Return(nil, 0, nil)
		db.Studio.On("QueryForAutoTag", testCtx, mock.Anything).Return([]*models.Studio{&studio, &reversedStudio}, nil).Once()
		db.Studio.On("GetAliases", testCtx, mock.Anything).Return([]string{}, nil).Maybe()

		doTest(db, test)
	}

	const unmatchedName = "unmatched"
	studio.Name = unmatchedName

	// test against aliases
	for _, test := range testTables {
		db := mocks.NewDatabase()

		db.Studio.On("Query", testCtx, mock.Anything, mock.Anything).Return(nil, 0, nil)
		db.Studio.On("QueryForAutoTag", testCtx, mock.Anything).Return([]*models.Studio{&studio, &reversedStudio}, nil).Once()
		db.Studio.On("GetAliases", testCtx, studioID).Return([]string{
			studioName,
		}, nil).Once()
		db.Studio.On("GetAliases", testCtx, reversedStudioID).Return([]string{}, nil).Once()

		doTest(db, test)
	}
}

func TestSceneTags(t *testing.T) {
	t.Parallel()

	const sceneID = 1
	const tagName = "tag name"
	const tagID = 2
	tag := models.Tag{
		ID:   tagID,
		Name: tagName,
	}

	const reversedTagName = "name tag"
	const reversedTagID = 3
	reversedTag := models.Tag{
		ID:   reversedTagID,
		Name: reversedTagName,
	}

	testTables := generateTestTable(tagName, sceneExt)

	assert := assert.New(t)

	doTest := func(db *mocks.Database, test pathTestTable) {
		scene := models.Scene{
			ID:     sceneID,
			Path:   test.Path,
			TagIDs: models.NewRelatedIDs([]int{}),
		}
		// THE CLAIM IS ASSERTED, not on the mock. `db.Scene.On("UpdatePartial", ...)`
		// proved a column write was attempted; a recording sink proves the claim reached
		// the right TARGET with the right ENTITY, which is the pair the write depends on
		// and which a matcher on a partial cannot see.
		sink := testSink()

		err := SceneTags(testCtx, &scene, db.Scene, db.Tag, nil, sink)

		assert.Nil(err)
		// A CLAIM IFF THE PATH MATCHED. Asserting that a claim arrived is only half
		// the test; the other half is that NO claim arrives for a path that did not match.
		//
		// That half is what the mock expectation could not see. A mock fails on a MISSING
		// call and is silent on an EXTRA one, so a tagger whose matcher was too loose
		// passed the old suite -- and a claim filed for the wrong path is a real person
		// attributed to a file they are not in.
		if test.Matches {
			assert.Len(sink.recorded(), 1, "a matching path must produce exactly one claim")
			if len(sink.recorded()) == 1 {
				assert.Equal(tagID, sink.recorded()[0].EntityID,
					"the claim must name the entity the path matched")
				assert.Equal(sceneID, sink.recorded()[0].TargetID,
					"and the target that matched -- the right entity on the wrong target "+
						"is a real corruption, and the pair is what the write needs")
			}
		} else {
			assert.Empty(sink.recorded(),
				"a NON-matching path must produce no claim")
		}

		db.AssertExpectations(t)
	}

	for _, test := range testTables {
		db := mocks.NewDatabase()

		db.Tag.On("Query", testCtx, mock.Anything, mock.Anything).Return(nil, 0, nil)
		db.Tag.On("QueryForAutoTag", testCtx, mock.Anything).Return([]*models.Tag{&tag, &reversedTag}, nil).Once()
		db.Tag.On("GetAliases", testCtx, mock.Anything).Return([]string{}, nil).Maybe()

		doTest(db, test)
	}

	const unmatchedName = "unmatched"
	tag.Name = unmatchedName

	// test against aliases
	for _, test := range testTables {
		db := mocks.NewDatabase()

		db.Tag.On("Query", testCtx, mock.Anything, mock.Anything).Return(nil, 0, nil)
		db.Tag.On("QueryForAutoTag", testCtx, mock.Anything).Return([]*models.Tag{&tag, &reversedTag}, nil).Once()
		db.Tag.On("GetAliases", testCtx, tagID).Return([]string{
			tagName,
		}, nil).Once()
		db.Tag.On("GetAliases", testCtx, reversedTagID).Return([]string{}, nil).Once()

		doTest(db, test)
	}
}
