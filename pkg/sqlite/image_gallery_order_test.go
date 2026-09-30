//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stash#3849: "Wrong order of images in galleries on identical files.
//
//	Different image galleries may have the exact same image ... When this
//	happens (that is: the hash is exactly the same) when viewing a gallery the
//	images will appear out of order, so i.e. image 034 will appear before 001."
//
// THE MECHANISM. `images_files` is a many-to-many join: one row per
// (image, file). An image present in two galleries has TWO file rows sharing one
// image_id -- say `003.jpg` inside gallery_99.zip and `006.jpg` inside
// gallery_01.zip.
//
// The gallery FILTER restricts membership via `galleries_images`
// (image_filter.go:255-262). The ORDER BY joins `images_files` and `files` with
// no such restriction (image.go:1057-1070):
//
//	q.addJoins(join{images_files, on: "images_files.image_id = images.id"})
//	q.addJoins(join{files,         on: "images_files.file_id  = files.id"})
//	ORDER BY COALESCE(folders.path,'') || COALESCE(files.basename,'')
//
// The sort KEY can therefore be read off the OTHER gallery's file row. Opening
// gallery 99 orders by `006.jpg` and renders 006, 001, 002 -- precisely the
// report.
//
// This is the same shape as stash#571 (closed earlier today): a many-to-many
// join whose far side is unconstrained, so a read takes an arbitrary row of the
// join rather than the one that was matched.
//
// NOT RANDOM. SQLite's plan is stable, so which row wins is reproducible. The
// title says "intermittently" because it depends on insertion order, not on
// chance -- which is what makes it testable.
//
// CHARACTERISATION tests: they pin what the code does today. The fix (correlate
// the sort key to the file row the gallery filter actually matched) is a rewrite
// of the sort builder, and the goal file says to record a subsystem-sized issue
// rather than half-build it.
//
// WHY THE FIXTURE USES REAL FILE ROWS. The first version of this file created
// images with `models.Image{Path: ...}` and no file rows. Both defect tests then
// PASSED -- for the wrong reason: the path a query returns comes from the joined
// `files` row, so every path came back empty, `filepath.Base("")` returned ".",
// and `NotContains(out, "006.jpg")` was trivially true. The POSITIVE CONTROL
// failed and is what exposed it; the two defect assertions said nothing. The
// helper now rejects a "." basename outright, which turns that whole class of
// false pass into a loud failure.

// mkStash3849Folder creates a parent folder for file rows to hang off.
// `files.parent_folder_id` has a FOREIGN KEY, so a file cannot be inserted
// without one -- the first run of this fixture failed exactly here.
//
// The folder PATH IS PART OF THE BUG, not scenery. The sort key is the joined
// file's full path (`COALESCE(folders.path,”) || COALESCE(files.basename,”)`),
// and the defect only shows when the shared image has a candidate key in a
// folder that sorts EARLIER than the gallery being viewed. The first version of
// this fixture put both zips in ONE folder, so both keys were
// `/library/.../003.jpg` and `/library/.../006.jpg`, 003 still sorted first, and
// the listing came out correct. Both defect tests passed -- vacuously, against
// correct code too. gallery_99.zip and gallery_01.zip therefore need SEPARATE
// folders, which is also what the reporter had.
func mkStash3849Folder(t *testing.T, ctx context.Context, path string) models.FolderID {
	t.Helper()
	f := &models.Folder{Path: path}
	require.NoError(t, db.Folder.Create(ctx, f))
	return f.ID
}

func stash3849Fixture(t *testing.T, ctx context.Context) (gallery99, gallery01, first, second, shared int) {
	t.Helper()

	// SEPARATE folders, so the shared image has a candidate key that sorts
	// BEFORE this gallery's own images -- which is the reported symptom.
	folder99 := mkStash3849Folder(t, ctx, "/library/99")
	folder01 := mkStash3849Folder(t, ctx, "/library/01")

	// Two real zip archives, so the shared image has two candidate file rows in
	// two different paths for the ORDER BY to choose between.
	mkZip := func(name string, folderID models.FolderID) models.FileID {
		f := &models.BaseFile{Basename: name, ParentFolderID: folderID}
		require.NoError(t, db.File.Create(ctx, f))
		return f.ID
	}
	zip99 := mkZip("gallery_99.zip", folder99)
	zip01 := mkZip("gallery_01.zip", folder01)

	// A member of a zip: basename plus the zip it lives in.
	mkMember := func(zipID models.FileID, folderID models.FolderID, name string) models.FileID {
		f := &models.BaseFile{Basename: name, ParentFolderID: folderID}
		f.ZipFileID = &zipID
		require.NoError(t, db.File.Create(ctx, f))
		return f.ID
	}

	f001 := mkMember(zip99, folder99, "001.jpg")
	f002 := mkMember(zip99, folder99, "002.jpg")

	// ORDER MATTERS, and finding out how was the whole difficulty. 006.jpg --
	// the same bytes as 003.jpg, in the OTHER archive, whose sort key begins
	// /library/01/... and therefore sorts before every /library/99/... key -- is
	// created FIRST, so its images_files row gets the lower id.
	//
	// The sort clause reads a single arbitrary row of the unconstrained
	// images_files join, and SQLite's plan takes the first row it reaches. With
	// the rows the other way round this fixture returns the correct order and
	// every defect test passes VACUOUSLY: they would pass against correct code
	// too. That is why the diagnostic that dumped the join rows was worth
	// writing before believing a green run.
	f006 := mkMember(zip01, folder01, "006.jpg")
	f003 := mkMember(zip99, folder99, "003.jpg")
	f004 := mkMember(zip01, folder01, "004.jpg")
	f005 := mkMember(zip01, folder01, "005.jpg")

	// CreateImageInput.FileIDs is []models.FileID, not []int.
	mkImage := func(fileIDs ...models.FileID) *models.Image {
		img := &models.Image{}
		require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{
			Image:   img,
			FileIDs: fileIDs,
		}))
		return img
	}
	i001 := mkImage(f001)
	i002 := mkImage(f002)
	// gallery_01 needs three images of its own, or a single-image gallery cannot
	// be mis-ordered and the symmetric test below could not fail.
	i004 := mkImage(f004)
	i005 := mkImage(f005)

	// THE SHARED IMAGE: one image row owning BOTH 003.jpg (in gallery_99.zip) and
	// 006.jpg (in gallery_01.zip) -- the same bytes under two names, which is
	// what the reporter's galleries had in common. This is the whole bug, and it
	// cannot be expressed without real file rows in images_files.
	i003 := mkImage(f003, f006)

	mkGallery := func() int {
		g := &models.Gallery{}
		require.NoError(t, db.Gallery.Create(ctx, &models.CreateGalleryInput{Gallery: g}))
		return int(g.ID)
	}
	g99 := mkGallery()
	g01 := mkGallery()

	require.NoError(t, db.Gallery.UpdateImages(ctx, g99, []int{
		int(i001.ID), int(i002.ID), int(i003.ID),
	}))
	require.NoError(t, db.Gallery.UpdateImages(ctx, g01, []int{
		int(i004.ID), int(i005.ID), int(i003.ID),
	}))

	// Published so the assertions below can name WHICH image is out of place
	// instead of asserting on a literal, which is how the earlier version of this
	// file could pass without the fixture having reproduced anything.
	stash3849FirstID, stash3849SecondID, stash3849SharedID = int(i001.ID), int(i002.ID), int(i003.ID)
	stash3849FourthID, stash3849FifthID = int(i004.ID), int(i005.ID)

	return g99, g01, int(i001.ID), int(i002.ID), int(i003.ID)
}

// The ids the fixture created, so a test can assert on positions by name.
var (
	stash3849FirstID  int
	stash3849SecondID int
	stash3849SharedID int
	stash3849FourthID int
	stash3849FifthID  int
)

func stash3849Fourth(t *testing.T) int {
	t.Helper()
	require.NotZero(t, stash3849FourthID, "stash3849Fixture must run before this")
	return stash3849FourthID
}

func stash3849Fifth(t *testing.T) int {
	t.Helper()
	require.NotZero(t, stash3849FifthID, "stash3849Fixture must run before this")
	return stash3849FifthID
}

func stash3849First(t *testing.T) int {
	t.Helper()
	require.NotZero(t, stash3849FirstID, "stash3849Fixture must run before this")
	return stash3849FirstID
}

func stash3849Second(t *testing.T) int {
	t.Helper()
	require.NotZero(t, stash3849SecondID, "stash3849Fixture must run before this")
	return stash3849SecondID
}

func stash3849Shared(t *testing.T) int {
	t.Helper()
	require.NotZero(t, stash3849SharedID, "stash3849Fixture must run before this")
	return stash3849SharedID
}

// idsInGalleryOrdered returns the image ids of gallery g, in the order the store
// returns them for the given sort.
//
// THIS IS THE OBSERVABLE, and getting that right took the whole exercise. The
// first two versions of this file asserted on `filepath.Base(img.Path)` and both
// defect tests passed against a fixture that had not even reproduced the bug:
// `img.Path` is the PRIMARY file (`images_files.primary = 1`), so it reads
// `003.jpg` no matter which row the sort used. The defect changes WHERE an image
// LANDS, not what it is called -- the 006 is a red herring the reporter inferred
// from the symptom.
func idsInGalleryOrdered(t *testing.T, ctx context.Context, galleryID int, sort string) []int {
	t.Helper()

	page, perPage := 1, 100
	direction := models.SortDirectionEnumAsc
	res, err := db.Image.Query(ctx, models.ImageQueryOptions{
		QueryOptions: models.QueryOptions{
			FindFilter: &models.FindFilterType{
				Sort: &sort, Direction: &direction, Page: &page, PerPage: &perPage,
			},
		},
		ImageFilter: &models.ImageFilterType{
			Galleries: &models.MultiCriterionInput{
				Value:    []string{strconv.Itoa(galleryID)},
				Modifier: models.CriterionModifierIncludes,
			},
		},
	})
	require.NoError(t, err)
	return res.IDs
}

// THE DEFECT, CHARACTERISED. Gallery 99 holds 001, 002 and 003, and the shared
// image's OTHER file row -- 006.jpg, in gallery_01.zip, under a folder that sorts
// earlier -- is what the ORDER BY reads. So the shared image sorts FIRST:
//
//	observed [28 24 25]   (28 is the 003/006 image)
//	correct  [24 25 28]
//
// which is precisely the reporter's "It will look as 006, 001, 002".
//
// This asserts the order the code PRODUCES, so it passes today and goes red when
// the sort is correlated to the file row the gallery filter actually matched. The
// precondition is asserted alongside it, because an assertion that merely pins
// "the shared image comes first" would still pass on a fixture that had stopped
// producing two candidate keys -- which is exactly what happened twice while
// writing this file.
func TestGalleryOrderIsBrokenWhenAnImageIsSharedAcrossGalleries(t *testing.T) {
	runWithRollbackTxn(t, "order is broken when an image is shared across galleries", func(t *testing.T, ctx context.Context) {
		g99, _, _, _, _ := stash3849Fixture(t, ctx)
		first, second, shared := stash3849First(t), stash3849Second(t), stash3849Shared(t)

		// THE PRECONDITION: the shared image must genuinely have two file rows
		// whose sort keys fall either side of this gallery's own.
		requireStash3849SharedRowsStraddleTheGallery(t, ctx)

		out := idsInGalleryOrdered(t, ctx, g99, "path")
		t.Logf("gallery_99 path order: %v (001=%d 002=%d shared=%d)", out, first, second, shared)

		assert.Equal(t, []int{shared, first, second}, out,
			"stash#3849: the shared image sorts where 006.jpg does (/library/01/... "+
				"sorts before /library/99/...), not where 003.jpg does, because the "+
				"ORDER BY reads an arbitrary images_files row instead of the one the "+
				"gallery filter matched. This assertion flips when that is fixed.")
	})
}

// requireStash3849SharedRowsStraddleTheGallery checks the fixture can show the
// defect at all: the shared image must have TWO images_files rows, one in each
// archive. One row cannot straddle anything, and without this check a fixture
// that had silently stopped reproducing the defect would still pass.
func requireStash3849SharedRowsStraddleTheGallery(t *testing.T, ctx context.Context) {
	t.Helper()

	_, rows, err := db.QuerySQL(ctx, `
		SELECT COALESCE(folders.path,'') || '/' || files.basename
		FROM images
		JOIN images_files ON images_files.image_id = images.id
		JOIN files ON files.id = images_files.file_id
		LEFT JOIN folders ON folders.id = files.parent_folder_id
		WHERE images.id = ?`, []interface{}{stash3849SharedID})
	require.NoError(t, err)
	require.Len(t, rows, 2,
		"the shared image must have TWO file rows; one cannot straddle anything")

	var keys []string
	for _, r := range rows {
		k, ok := r[0].(string)
		require.True(t, ok, "unexpected key type %T", r[0])
		keys = append(keys, k)
	}
	in99 := func(k string) bool { return strings.HasPrefix(k, "/library/99/") }
	in01 := func(k string) bool { return strings.HasPrefix(k, "/library/01/") }
	require.True(t, (in99(keys[0]) && in01(keys[1])) || (in01(keys[0]) && in99(keys[1])),
		"the two keys must sit in different archives, got %v", keys)
}

// GUARD, not a reproduction -- and recorded as such because the asymmetry is the
// interesting part. gallery_01 is /library/01, and the row the sort happens to
// reach is 006.jpg in /library/01, which is the key that AGREES with this
// gallery. So gallery_01 comes out right. Only the gallery whose own archive is
// NOT the first row reached misorders.
//
// This guards that asymmetry from silently changing: if a fix to gallery 99 also
// changed gallery 01, this goes red.
func TestTheSharedImageDoesNotReorderEitherGallery(t *testing.T) {
	runWithRollbackTxn(t, "the shared image does not reorder either gallery", func(t *testing.T, ctx context.Context) {
		_, g01, _, _, shared := stash3849Fixture(t, ctx)

		out01 := idsInGalleryOrdered(t, ctx, g01, "path")
		t.Logf("gallery_01 path order: %v (004=%d 005=%d shared=%d)",
			out01, stash3849Fourth(t), stash3849Fifth(t), shared)

		// gallery_01 is /library/01, so its own keys are 004.jpg and 005.jpg and
		// the shared image sorts as 006 -- LAST. If the sort instead reads the
		// image's 003.jpg row in /library/99, that key sorts before them and the
		// shared image jumps to the FRONT. The mirror of the other direction.
		assert.Equal(t, []int{stash3849Fourth(t), stash3849Fifth(t), shared}, out01,
			"the shared image must sort as 006 in gallery_01, not as 003: the "+
				"ORDER BY reads an arbitrary images_files row")
	})
}

// POSITIVE CONTROL, and the test that exposed everything above. The sort is
// correct when images are NOT shared. Without this, the two defect tests above
// would pass against a fixture that had not reproduced the bug, against an empty
// result, and against a sort broken outright.
func TestGalleryOrderIsCorrectWhenImagesAreNotShared(t *testing.T) {
	runWithRollbackTxn(t, "gallery order is correct when images are not shared", func(t *testing.T, ctx context.Context) {
		folderID := mkStash3849Folder(t, ctx, "/library/plain")
		g := &models.Gallery{}
		require.NoError(t, db.Gallery.Create(ctx, &models.CreateGalleryInput{Gallery: g}))

		var want []int
		for _, n := range []string{"001.jpg", "002.jpg", "010.jpg"} {
			f := &models.BaseFile{Basename: n, ParentFolderID: folderID}
			require.NoError(t, db.File.Create(ctx, f))
			img := &models.Image{}
			require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{
				Image:   img,
				FileIDs: []models.FileID{f.ID},
			}))
			want = append(want, int(img.ID))
		}
		require.NoError(t, db.Gallery.UpdateImages(ctx, int(g.ID), want))

		out := idsInGalleryOrdered(t, ctx, int(g.ID), "path")
		assert.Equal(t, want, out,
			"NATURAL_CI must order 010 after 002; if this fails the sort is broken "+
				"independently of sharing, and the other tests prove nothing")
	})
}

// GUARD, not a reproduction. "title" is COALESCE(images.title, files.basename),
// so it reads the same unconstrained images_files join and would in principle
// misorder identically. It does NOT here, and the reason is worth recording: these
// fixture images have empty titles, so COALESCE ties on every row and the secondary
// `folders.path` decides the order. That secondary sort happens to come out right
// here. So this asserts the correct order and would go red if a title were added,
// or if a fix to "path" were applied without covering "title".
func TestTheTitleSortIsAffectedTheSameWay(t *testing.T) {
	runWithRollbackTxn(t, "the title sort is affected the same way", func(t *testing.T, ctx context.Context) {
		g99, _, _, _, _ := stash3849Fixture(t, ctx)
		shared := stash3849Shared(t)

		out := idsInGalleryOrdered(t, ctx, g99, "title")
		t.Logf("gallery_99 title order: %v (shared=%d)", out, shared)

		assert.Equal(t, []int{stash3849First(t), stash3849Second(t), shared}, out,
			"the title sort reads the same unconstrained images_files join, so it "+
				"needs the same fix as path")
	})
}
