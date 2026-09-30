package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
	"github.com/stashapp/stash/pkg/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestFindGalleriesToCleanIncludesNoGalleryFolders(t *testing.T) {
	cleanPath := t.TempDir()
	outsideCleanPath := t.TempDir()

	createGalleryFolder := func(root, name string, markers ...string) string {
		t.Helper()

		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(path, 0755))
		for _, marker := range markers {
			require.NoError(t, os.WriteFile(filepath.Join(path, marker), nil, 0644))
		}

		return path
	}

	// Empty path-backed galleries retain the existing clean behaviour.
	emptyGallery := &models.Gallery{ID: 1, Path: createGalleryFolder(cleanPath, "empty")}

	// A gallery matching both clean rules must only be returned once.
	emptyNoGalleryFolderID := models.FolderID(2)
	emptyNoGallery := &models.Gallery{
		ID:       2,
		Path:     createGalleryFolder(cleanPath, "empty-nogallery", ".nogallery"),
		FolderID: &emptyNoGalleryFolderID,
	}

	// A populated folder gallery is cleaned when its folder opts out.
	noGalleryFolderID := models.FolderID(3)
	noGallery := &models.Gallery{
		ID:       3,
		Path:     createGalleryFolder(cleanPath, "nogallery", ".nogallery"),
		FolderID: &noGalleryFolderID,
	}

	// An ordinary folder gallery has no reason to be cleaned.
	regularFolderID := models.FolderID(4)
	regular := &models.Gallery{
		ID:       4,
		Path:     createGalleryFolder(cleanPath, "regular"),
		FolderID: &regularFolderID,
	}

	// Selective clean must not touch galleries outside the requested paths.
	outsideFolderID := models.FolderID(5)
	outside := &models.Gallery{
		ID:       5,
		Path:     createGalleryFolder(outsideCleanPath, "nogallery", ".nogallery"),
		FolderID: &outsideFolderID,
	}

	// Match Scan semantics: .forcegallery wins when both markers exist.
	forcedFolderID := models.FolderID(7)
	forced := &models.Gallery{
		ID:       7,
		Path:     createGalleryFolder(cleanPath, "forced", ".nogallery", ".forcegallery"),
		FolderID: &forcedFolderID,
	}

	db := mocks.NewDatabase()
	emptyGalleryFilter := mock.MatchedBy(func(filter *models.GalleryFilterType) bool {
		return filter != nil && filter.ImageCount != nil &&
			filter.ImageCount.Value == 0 &&
			filter.ImageCount.Modifier == models.CriterionModifierEquals
	})
	// Only folder galleries need marker checks; filter out user and file galleries in SQL.
	folderGalleryFilter := mock.MatchedBy(func(filter *models.GalleryFilterType) bool {
		return filter != nil && filter.FoldersFilter != nil &&
			filter.FoldersFilter.Path != nil &&
			filter.FoldersFilter.Path.Modifier == models.CriterionModifierNotNull
	})

	db.Gallery.On("Query", mock.Anything, emptyGalleryFilter, mock.Anything).
		Return([]*models.Gallery{emptyGallery, emptyNoGallery}, 2, nil).Once()
	db.Gallery.On("Query", mock.Anything, emptyGalleryFilter, mock.Anything).
		Return([]*models.Gallery{}, 0, nil).Once()
	db.Gallery.On("Query", mock.Anything, folderGalleryFilter, mock.Anything).
		Return([]*models.Gallery{emptyNoGallery, noGallery, regular, outside, forced}, 5, nil).Once()
	db.Gallery.On("Query", mock.Anything, folderGalleryFilter, mock.Anything).
		Return([]*models.Gallery{}, 0, nil).Once()

	j := cleanJob{
		repository: db.Repository(),
		input: CleanMetadataInput{
			Paths: []string{cleanPath},
		},
	}

	got, err := j.findGalleriesToClean(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []int{emptyGallery.ID, emptyNoGallery.ID, noGallery.ID}, got)
	db.Gallery.AssertExpectations(t)
}

// A DRY RUN THAT DELETES.
//
// The test above exercises `findGalleriesToClean`, which only ever DECIDES.
// The deletion is a separate function, `cleanGalleries`, and it is the one that
// destroys rows. Nothing upstream tested it, and the mutation harness recorded
// the consequence as a survivor: deleting the `if !j.input.DryRun` guard left
// every other test green.
//
// That is the most expensive kind of untested line in this codebase. A dry run
// exists so a user can see what a clean WOULD remove before it removes it, and
// the whole feature's value is that it removes nothing. A regression here is
// not a wrong answer, it is data loss performed on the strength of a
// "preview" click.
//
// The control for the whole file: the SAME set-up with DryRun false MUST reach
// Destroy. Without that, this test would also pass against a `cleanGalleries`
// that never deletes anything at all -- and a test that cannot distinguish
// "refuses to delete" from "cannot delete" is a comment.
func TestACleanDryRunDeletesNothing(t *testing.T) {
	setUp := func(t *testing.T, dryRun bool) *mocks.Database {
		t.Helper()

		cleanPath := t.TempDir()
		marker := filepath.Join(cleanPath, "nogallery")
		require.NoError(t, os.MkdirAll(marker, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(marker, ".nogallery"), nil, 0644))

		folderID := models.FolderID(9)
		g := &models.Gallery{ID: 9, Path: marker, FolderID: &folderID}

		emptyGalleryFilter := mock.MatchedBy(func(filter *models.GalleryFilterType) bool {
			return filter != nil && filter.ImageCount != nil &&
				filter.ImageCount.Value == 0 &&
				filter.ImageCount.Modifier == models.CriterionModifierEquals
		})
		folderGalleryFilter := mock.MatchedBy(func(filter *models.GalleryFilterType) bool {
			return filter != nil && filter.FoldersFilter != nil &&
				filter.FoldersFilter.Path != nil &&
				filter.FoldersFilter.Path.Modifier == models.CriterionModifierNotNull
		})

		db := mocks.NewDatabase()
		// `.Twice()` per filter, NOT unbounded. An expectation with no count
		// constraint is a permanent one, so the SECOND batch query -- the
		// paginating loop's terminating call -- matches it again and
		// `queryInBatches` never sees an empty page. The loop spins forever,
		// and the test dies on its own -timeout with a stack pointing at
		// mock.MethodCalled. The upstream test declared these `.Once()` for the
		// same reason; without the terminator the hang IS the bug, and it
		// looks like a slow test rather than a broken mock.
		//
		// One populated page then one empty page is what ends the batch loop,
		// and both filters need the pair.
		db.Gallery.On("Query", mock.Anything, emptyGalleryFilter, mock.Anything).
			Return([]*models.Gallery{}, 0, nil).Twice()
		db.Gallery.On("Query", mock.Anything, folderGalleryFilter, mock.Anything).
			Return([]*models.Gallery{g}, 1, nil).Once()
		db.Gallery.On("Query", mock.Anything, folderGalleryFilter, mock.Anything).
			Return([]*models.Gallery{}, 0, nil).Once()
		// deleteGallery's real sequence: Find, LoadPrimaryFile, Destroy, then a
		// plugin hook. LoadPrimaryFile needs the file query to answer, and the
		// hook needs a PluginCache, hence the instance below.
		db.Gallery.On("Find", mock.Anything, g.ID).Return(g, nil)
		db.Gallery.On("Destroy", mock.Anything, g.ID).Return(nil)
		db.Gallery.On("GetManyFileIDs", mock.Anything, []int{g.ID}).
			Return([][]models.FileID{{}}, nil)

		// deleteGallery reads GetInstance().PluginCache and then registers a
		// post-destroy hook, so a live Manager with a REAL cache is required
		// for the DELETING case to run at all -- same save/restore the
		// thumbnail and phash tests use.
		//
		// `&plugin.Cache{}` is NOT enough and panics: `enabledPlugins` calls
		// `c.config.GetDisabledPlugins()` on a nil interface, so the hook
		// registration segfaults AFTER Destroy has already been called. Use the
		// production constructor with a real config, which is what init.go
		// does -- the nil-Config version of this test failed in a way that
		// looked like the assertion was wrong rather than the fixture.
		prev := instance
		cfg := config.InitializeEmpty()
		instance = &Manager{
			Config:      cfg,
			PluginCache: plugin.NewCache(cfg),
		}
		t.Cleanup(func() { instance = prev })

		j := cleanJob{
			repository: db.Repository(),
			input: CleanMetadataInput{
				Paths:  []string{cleanPath},
				DryRun: dryRun,
			},
		}
		j.cleanGalleries(context.Background())
		return db
	}

	t.Run("dry run destroys nothing", func(t *testing.T) {
		db := setUp(t, true)
		db.Gallery.AssertNotCalled(t, "Destroy", mock.Anything, mock.Anything)
	})

	t.Run("a real run does destroy -- the control", func(t *testing.T) {
		db := setUp(t, false)
		// If this fails, the test above proves nothing: it would pass against a
		// cleanGalleries that never deletes anything.
		db.Gallery.AssertCalled(t, "Destroy", mock.Anything, 9)
	})
}
