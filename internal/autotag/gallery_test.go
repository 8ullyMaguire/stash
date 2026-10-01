package autotag

import (
	"context"
	"testing"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

const galleryExt = "zip"

var testCtx = context.Background()

// returns got == expected
// ignores expected.UpdatedAt, but ensures that got.UpdatedAt is set and not null
func galleryPartialsEqual(got, expected models.GalleryPartial) bool {
	// updated at should be set and not null
	if !got.UpdatedAt.Set || got.UpdatedAt.Null {
		return false
	}
	// else ignore the exact value
	got.UpdatedAt = models.OptionalTime{}

	return assert.ObjectsAreEqual(got, expected)
}

func TestGalleryPerformers(t *testing.T) {
	t.Parallel()

	const galleryID = 1
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

	testTables := generateTestTable(performerName, galleryExt)

	assert := assert.New(t)

	for _, test := range testTables {
		db := mocks.NewDatabase()

		db.Performer.On("Query", testCtx, mock.Anything, mock.Anything).Return(nil, 0, nil)
		db.Performer.On("QueryForAutoTag", testCtx, mock.Anything).Return([]*models.Performer{&performer, &reversedPerformer}, nil).Once()

		gallery := models.Gallery{
			ID:           galleryID,
			Path:         test.Path,
			PerformerIDs: models.NewRelatedIDs([]int{}),
		}
		// THE CLAIM IS ASSERTED, not on the mock. `db.Gallery.On("UpdatePartial", ...)`
		// proved a column write was attempted; a recording sink proves the claim reached
		// the right TARGET with the right ENTITY, which is the pair the write depends on
		// and which a matcher on a partial cannot see.
		sink := testSink()

		err := GalleryPerformers(testCtx, &gallery, db.Gallery, db.Performer, nil, sink)

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
				assert.Equal(galleryID, sink.recorded()[0].TargetID,
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

func TestGalleryStudios(t *testing.T) {
	t.Parallel()

	const galleryID = 1
	const studioName = "studio name"
	var studioID = 2
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

	testTables := generateTestTable(studioName, galleryExt)

	assert := assert.New(t)

	doTest := func(db *mocks.Database, test pathTestTable) {
		gallery := models.Gallery{
			ID:   galleryID,
			Path: test.Path,
		}
		// THE CLAIM IS ASSERTED, not on the mock. `db.Gallery.On("UpdatePartial", ...)`
		// proved a column write was attempted; a recording sink proves the claim reached
		// the right TARGET with the right ENTITY, which is the pair the write depends on
		// and which a matcher on a partial cannot see.
		sink := testSink()

		err := GalleryStudios(testCtx, &gallery, db.Gallery, db.Studio, nil, sink)

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
				assert.Equal(galleryID, sink.recordedStudios()[0].TargetID,
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

	// test against aliases
	const unmatchedName = "unmatched"
	studio.Name = unmatchedName

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

func TestGalleryTags(t *testing.T) {
	t.Parallel()

	const galleryID = 1
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

	testTables := generateTestTable(tagName, galleryExt)

	assert := assert.New(t)

	doTest := func(db *mocks.Database, test pathTestTable) {
		gallery := models.Gallery{
			ID:     galleryID,
			Path:   test.Path,
			TagIDs: models.NewRelatedIDs([]int{}),
		}
		// THE CLAIM IS ASSERTED, not on the mock. `db.Gallery.On("UpdatePartial", ...)`
		// proved a column write was attempted; a recording sink proves the claim reached
		// the right TARGET with the right ENTITY, which is the pair the write depends on
		// and which a matcher on a partial cannot see.
		sink := testSink()

		err := GalleryTags(testCtx, &gallery, db.Gallery, db.Tag, nil, sink)

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
				assert.Equal(galleryID, sink.recorded()[0].TargetID,
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
