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

// stash#3849: "Wrong order of images in galleries on identical files."
//
//	Different image galleries may have the exact same image ... When this
//	happens (that is: the hash is exactly the same) when viewing a gallery the
//	images will appear out of order, so i.e. image 034 will appear before 001.
//
// THE BUG, and it is FIXED. `images_files` is a many-to-many join: one row per
// (image, file). An image present in two galleries has TWO file rows sharing one
// image_id -- `003.jpg` inside gallery_99.zip and `006.jpg` inside gallery_01.zip.
//
// The gallery FILTER restricted membership via `galleries_images`. The ORDER BY
// joined `images_files` + `files` with no such restriction (image.go:1057-1070
// as it was), so the sort KEY was read off an ARBITRARY row of that join rather
// than the one the filter matched:
//
//	q.addJoins(join{images_files, on: "images_files.image_id = images.id"})
//	q.addJoins(join{files,         on: "images_files.file_id  = files.id"})
//	ORDER BY COALESCE(folders.path,'') || COALESCE(files.basename,'')
//
// Opening gallery 99 therefore ordered by `006.jpg` and rendered 006, 001, 002 --
// precisely the report. Same shape as stash#571 (closed earlier): a many-to-many
// whose far side is unconstrained.
//
// NOT RANDOM. SQLite's plan is stable, so which row wins is reproducible. The
// title says "intermittently" because it depends on insertion order, not on
// chance -- which is what made it testable.
//
// THE FIX. When a gallery filter is active, the sort's `images_files` join is
// ALIASED and correlated to that gallery's files (image.go, setImageSortAndPagination):
//
//	LEFT JOIN images_files AS gallery_files ON gallery_files.image_id = images.id
//	  AND ( <the row is one of the gallery's own files>
//	        OR <its zip_file_id is one of the gallery's files>
//	        OR <it is the image's primary file> )
//	LEFT JOIN files ON files.id = gallery_files.file_id
//
// so `files` -- and so the sort key -- comes from a row the gallery actually
// contains. The primary-file arm is what makes it safe to apply unconditionally;
// see the comment in image.go for why leaving it out broke folder galleries.
//
// WHAT CHANGED IN THESE TESTS, and it is the only reason they are worth reading:
// they began as CHARACTERISATION tests pinning the broken order, so that a green
// run meant "the defect still reproduces". `TestGalleryOrderIsBrokenWhenAnImageIsSharedAcrossGalleries`
// asserted `[shared first second]` and PASSED against the bug. With the fix it
// asserts `[first second shared]` and fails against the old code -- which is the
// point: the assertion is now the specification, not a snapshot.
//
// WHY THE FIXTURE USES REAL FILE ROWS. The first version created images with
// `models.Image{Path: ...}` and no file rows. Both defect tests then PASSED -- for
// the wrong reason: the path a query returns comes from the joined `files` row,
// so every path came back empty, `filepath.Base("")` returned ".", and
// `NotContains(out, "006.jpg")` was trivially true. The POSITIVE CONTROL failed
// and is what exposed it. The helper now rejects a "." basename outright, which
// turns that whole class of false pass into a loud failure.

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

		assert.Equal(t, []int{first, second, shared}, out,
			"stash#3849, FIXED: the shared image must sort where 003.jpg does "+
				"(/library/99/...), the file this gallery actually contains, not where "+
				"006.jpg does (/library/01/..., which sorts first). Before the fix this "+
				"assertion was [shared first second] and passed against the defect; it "+
				"is the specification now, and it goes red if the correlation is removed.")
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

// A MALFORMED gallery id must not change the sort silently. The correlation binds its ids as
// SQL arguments, and a non-numeric one cannot be bound as the integer the schema declares -- so
// the code falls back to the plain, unaliased join and lets the CRITERION handler refuse the
// filter. That fallback was untested, so a mutation replacing it with "substitute 0 and carry on"
// survived: a filter that names no gallery at all would then be correlated to gallery 0 and
// quietly return the wrong rows rather than an error.
func TestAMalformedGalleryIDDoesNotSilentlyChangeTheSort(t *testing.T) {
	sortPath, dirAsc := "path", models.SortDirectionEnumAsc
	pageOne, perPageHundred := 1, 100

	runWithRollbackTxn(t, "a malformed gallery id does not silently change the sort", func(t *testing.T, ctx context.Context) {
		folderID := mkStash3849Folder(t, ctx, "/library/plain2")
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

		// A gallery id that is not an integer. The criterion handler must refuse it -- the point
		// is that the SORT does not paper over it by quietly correlating to something else.
		res, err := db.Image.Query(ctx, models.ImageQueryOptions{
			QueryOptions: models.QueryOptions{
				FindFilter: &models.FindFilterType{
					Sort: &sortPath, Direction: &dirAsc,
					Page: &pageOne, PerPage: &perPageHundred,
				},
			},
			ImageFilter: &models.ImageFilterType{
				Galleries: &models.MultiCriterionInput{
					Value:    []string{"not-a-number"},
					Modifier: models.CriterionModifierIncludes,
				},
			},
		})
		if err == nil {
			// MEASURED, and worth recording because I expected an error here and got none: the
			// criterion handler matches gallery ids as STRINGS, so "not-a-number" is a
			// well-formed criterion that matches no gallery. So the correct assertion is not
			// "it errors" -- it is "it does not return the SORTED GALLERY'S ROWS anyway", which
			// is what a correlation to gallery 0 or to a defaulted id would do.
			//
			// The first version of this test asserted an error and FAILED against correct code.
			t.Logf("the filter accepted %q as a string criterion and returned %v", "not-a-number", res.IDs)
			assert.Empty(t, res.IDs,
				"a malformed gallery id matches no gallery, so it must return NO rows; anything "+
					"else means the sort correlated to some gallery the caller did not name, which "+
					"is a cross-gallery read dressed up as an empty result")
			// And the gallery's real rows are still reachable -- the fallback did not corrupt
			// anything for a well-formed query.
			ok, err := db.Image.Query(ctx, models.ImageQueryOptions{
				QueryOptions: models.QueryOptions{
					FindFilter: &models.FindFilterType{
						Sort: &sortPath, Direction: &dirAsc,
						Page: &pageOne, PerPage: &perPageHundred,
					},
				},
				ImageFilter: &models.ImageFilterType{
					Galleries: &models.MultiCriterionInput{
						Value:    []string{strconv.Itoa(int(g.ID))},
						Modifier: models.CriterionModifierIncludes,
					},
				},
			})
			require.NoError(t, err, "a well-formed gallery id must still work")
			assert.Equal(t, want, ok.IDs,
				"and the well-formed query must return the gallery's images in path order")
			return
		}
		t.Logf("the filter refused the malformed id outright, which is also fine: %v", err)
		assert.Contains(t, err.Error(), "not-a-number",
			"the error must say WHICH value was bad, or a caller cannot fix the request")
	})
}

// THE FIXTURE CANNOT DISTINGUISH THE ARMS, and this says so where a reader will see it.
//
// `requireStash3849SharedRowsStraddleTheGallery` proves the shared image has two file rows in
// two archives. It does NOT prove which of them is the PRIMARY one -- and the primary-file arm of
// the correlation is a fallback that, on this fixture, happens to agree with the arm that carries
// the fix. So this asserts the fact that makes the fixture's limits explicit: whichever row is
// primary, the sorted order is the one the gallery's own file set implies.
func TestTheFixtureSaysWhichSharedRowIsPrimary(t *testing.T) {
	runWithRollbackTxn(t, "the fixture says which shared row is primary", func(t *testing.T, ctx context.Context) {
		stash3849Fixture(t, ctx)
		requireStash3849SharedRowsStraddleTheGallery(t, ctx)

		_, rows, err := db.QuerySQL(ctx, `
			SELECT files.basename, images_files."primary"
			FROM images
			JOIN images_files ON images_files.image_id = images.id
			JOIN files ON files.id = images_files.file_id
			WHERE images.id = ?`, []interface{}{stash3849SharedID})
		require.NoError(t, err)
		require.Len(t, rows, 2)

		var primary string
		for _, r := range rows {
			if r[1] == int64(1) || r[1] == true {
				primary, _ = r[0].(string)
			}
		}
		t.Logf("the shared image's PRIMARY file row is %q", primary)

		// RECORDED, not asserted as a requirement: the fixture currently makes the primary row
		// the one inside the gallery being viewed, which is why dropping the archive arm does not
		// turn these tests red. A fixture that made the OTHER row primary would test the
		// correlation properly, and this line is where that change would go.
		require.NotEmpty(t, primary, "an image must have exactly one primary file")
		require.Len(t, rows, 2, "and the shared image must still have two rows to choose between")
	})
}

// THE FIXTURE THAT ACTUALLY DISTINGUISHES THE ARMS, and the reason the two above are not
// sufficient on their own.
//
// MEASURED, not assumed: `TestTheFixtureSaysWhichSharedRowIsPrimary` reports that the original
// fixture's shared image has `003.jpg` -- the row inside gallery 99 -- as its PRIMARY. So the
// primary-file fallback and the gallery's-own-file set AGREE on this data, and dropping either
// one leaves the suite green. A test that cannot tell two arms apart cannot tell whether either
// is present, so the original fixture is a weaker check than it looks.
//
// The reporter's library is the case where they DISAGREE: the shared image's primary row is the
// one in the OTHER gallery. Then the primary fallback alone sorts the image as if it belonged to
// the other gallery, and only the gallery's-own-file arm gets it right. That is this fixture.
//
// The setup is the original one with the insertion order of the two shared file rows REVERSED, so
// `004.jpg` (gallery 99) is created before `003.jpg` and becomes primary. `primaryFirst = false`
// on the shared image's creation says the same thing explicitly rather than relying on insertion
// order, because relying on it is what made the first fixture's tests vacuous twice already.
func TestTheSharedImageWhosePrimaryRowIsInTheOtherGallery(t *testing.T) {
	runWithRollbackTxn(t, "the shared image whose primary row is in the other gallery", func(t *testing.T, ctx context.Context) {
		folder99 := mkStash3849Folder(t, ctx, "/library/99b")
		folder01 := mkStash3849Folder(t, ctx, "/library/01b")

		mkZip := func(name string, folderID models.FolderID) models.FileID {
			f := &models.BaseFile{Basename: name, ParentFolderID: folderID}
			require.NoError(t, db.File.Create(ctx, f))
			return f.ID
		}
		zip99 := mkZip("gallery_99b.zip", folder99)
		zip01 := mkZip("gallery_01b.zip", folder01)

		mkMember := func(zipID models.FileID, folderID models.FolderID, name string) models.FileID {
			f := &models.BaseFile{Basename: name, ParentFolderID: folderID}
			f.ZipFileID = &zipID
			require.NoError(t, db.File.Create(ctx, f))
			return f.ID
		}

		f001 := mkMember(zip99, folder99, "001.jpg")
		f002 := mkMember(zip99, folder99, "002.jpg")
		// THE REVERSAL: the OTHER gallery's row is created first, so it becomes primary.
		f003 := mkMember(zip01, folder01, "003.jpg")
		f004 := mkMember(zip99, folder99, "004.jpg")

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
		// The shared image owns BOTH 003.jpg (gallery_01b) and 004.jpg (gallery_99b).
		i003 := mkImage(f003, f004)

		mkGallery := func() int {
			g := &models.Gallery{}
			require.NoError(t, db.Gallery.Create(ctx, &models.CreateGalleryInput{Gallery: g}))
			return int(g.ID)
		}
		g99 := mkGallery()
		require.NoError(t, db.Gallery.UpdateImages(ctx, g99, []int{
			int(i001.ID), int(i002.ID), int(i003.ID),
		}))
		// THE GALLERY'S OWN FILE. Without this the gallery has NO galleries_files row, arm 1 of
		// the correlation resolves nothing, and the primary-file floor decides alone -- which is
		// exactly what happened to every earlier version of these fixtures, including the
		// original. That is why the original suite could not tell arm 1 from the floor.
		// NOT `FileIDs`. GalleryStore.Update reads `updatedObject.Files` and guards the whole
		// branch on `Files.Loaded()`, so passing FileIDs was a silent NO-OP -- the gallery kept
		// zero galleries_files rows and arm 1 resolved nothing. The failing assertion was the
		// only thing that noticed.
		zipFiles, err := db.File.FindByZipFileID(ctx, zip99)
		require.NoError(t, err)
		require.NoError(t, db.Gallery.Update(ctx, &models.UpdateGalleryInput{
			Gallery: &models.Gallery{
				ID:    g99,
				Files: models.NewRelatedFiles(zipFiles),
			},
		}))

		// THE PRECONDITION, asserted rather than assumed: the primary row must be the one in the
		// OTHER gallery, or this test proves nothing -- which is exactly the mistake the original
		// fixture made twice.
		_, rows, err := db.QuerySQL(ctx, `
			SELECT files.basename FROM images
			JOIN images_files ON images_files.image_id = images.id
			JOIN files ON files.id = images_files.file_id
			WHERE images.id = ? AND images_files."primary" = 1`,
			[]interface{}{int(i003.ID)})
		require.NoError(t, err)
		require.Len(t, rows, 1, "an image has exactly one primary file")
		primaryName, ok := rows[0][0].(string)
		require.True(t, ok)
		require.Equal(t, "003.jpg", primaryName,
			"this fixture is only meaningful when the PRIMARY row is the one in the OTHER "+
				"gallery; if it is 004.jpg the two arms agree again and the test is vacuous")

		first, second, shared := int(i001.ID), int(i002.ID), int(i003.ID)
		out := idsInGalleryOrdered(t, ctx, g99, "path")
		t.Logf("gallery_99b path order: %v (001=%d 002=%d shared=%d)", out, first, second, shared)

		assert.Equal(t, []int{first, second, shared}, out,
			"the shared image must sort by 004.jpg -- the file THIS gallery contains -- even "+
				"though its primary row is 003.jpg in the other gallery. Sorting by the primary "+
				"row here would place the shared image as 003.jpg, whose key begins /library/01b "+
				"and therefore sorts FIRST: the reporter's symptom, reproduced with the primary "+
				"row deliberately pointing the wrong way")
	})
}

// THE FIXTURE THAT PINS ARM 1's ARCHIVE PATH, and the reason it exists.
//
// MEASURED: the fixture above sets the gallery's files with `FindByZipFileID`, which returns the
// archive AND its members, so `galleries_files` came out as [001.jpg, 002.jpg, 004.jpg] and arm 1's
// DIRECT test (`gff.file_id IN (galleries_files)`) already matched 004.jpg. Arm 1's ARCHIVE path
// and the whole of arm 2 were therefore never needed to pass, and mutations that disabled them
// survived.
//
// A real scanner writes ONE row per gallery: the ARCHIVE. So this fixture writes exactly that --
// `galleries_files` = [gallery_99c.zip] only -- which is the shape the production query sees and
// the only one where the archive indirection is load-bearing.
//
// With arm 1's archive path and arm 2 removed, this test goes red; with the direct path removed it
// stays green. That is the asymmetry worth pinning: a folder gallery needs the direct path and a
// zip gallery needs the archive path, and neither fixture can substitute for the other.
func TestAZipGalleryWhoseGalleryFileIsTheArchiveItself(t *testing.T) {
	runWithRollbackTxn(t, "a zip gallery whose gallery file is the archive itself", func(t *testing.T, ctx context.Context) {
		folder99 := mkStash3849Folder(t, ctx, "/library/99c")
		folder01 := mkStash3849Folder(t, ctx, "/library/01c")

		mkZip := func(name string, folderID models.FolderID) models.FileID {
			f := &models.BaseFile{Basename: name, ParentFolderID: folderID}
			require.NoError(t, db.File.Create(ctx, f))
			return f.ID
		}
		zip99 := mkZip("gallery_99c.zip", folder99)
		zip01 := mkZip("gallery_01c.zip", folder01)
		mkMember := func(zipID models.FileID, folderID models.FolderID, name string) models.FileID {
			f := &models.BaseFile{Basename: name, ParentFolderID: folderID}
			f.ZipFileID = &zipID
			require.NoError(t, db.File.Create(ctx, f))
			return f.ID
		}
		f001 := mkMember(zip99, folder99, "001.jpg")
		f002 := mkMember(zip99, folder99, "002.jpg")
		// The OTHER gallery's row first, so it is primary -- the reporter's shape.
		f003 := mkMember(zip01, folder01, "003.jpg")
		f004 := mkMember(zip99, folder99, "004.jpg")

		mkImage := func(fileIDs ...models.FileID) *models.Image {
			img := &models.Image{}
			require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{Image: img, FileIDs: fileIDs}))
			return img
		}
		i001, i002, i003 := mkImage(f001), mkImage(f002), mkImage(f003, f004)

		g := &models.Gallery{}
		require.NoError(t, db.Gallery.Create(ctx, &models.CreateGalleryInput{Gallery: g}))
		g99 := int(g.ID)
		require.NoError(t, db.Gallery.UpdateImages(ctx, g99, []int{
			int(i001.ID), int(i002.ID), int(i003.ID),
		}))
		// THE PRODUCTION SHAPE: the gallery's file is the ARCHIVE, and nothing else.
		zipFiles, err := db.File.Find(ctx, zip99)
		require.NoError(t, err)
		require.Len(t, zipFiles, 1, "finding a file by its own id returns just that file")
		require.NoError(t, db.Gallery.Update(ctx, &models.UpdateGalleryInput{
			Gallery: &models.Gallery{ID: g99, Files: models.NewRelatedFiles(zipFiles)},
		}))

		// THE PRECONDITION. Without it a mutation that breaks the archive path could still pass,
		// because arm 1's direct test would quietly match a member row.
		_, gf, err := db.QuerySQL(ctx,
			`SELECT file_id FROM galleries_files WHERE gallery_id = ?`, []interface{}{g99})
		require.NoError(t, err)
		require.Len(t, gf, 1, "a zip gallery has exactly one file: the archive")
		require.Equal(t, int64(zip99), gf[0][0],
			"and it must be the ARCHIVE, or the archive indirection is untested")

		first, second, shared := int(i001.ID), int(i002.ID), int(i003.ID)
		out := idsInGalleryOrdered(t, ctx, g99, "path")
		t.Logf("gallery_99c path order: %v", out)
		assert.Equal(t, []int{first, second, shared}, out,
			"the shared image must sort by 004.jpg, which is only reachable from the gallery "+
				"through its zip_file_id -> the archive. Arm 1's direct path cannot get here: "+
				"004.jpg is not in galleries_files")
	})
}

// THE OTHER HALF, and the reason arm 1 is an OR rather than just the archive path.
//
// A FOLDER gallery has no archive: the images ARE the gallery's files, so the only way to reach
// the right row is the direct test `gff.file_id IN (galleries_files)`. Remove it and a folder
// gallery's shared image falls through to the primary-file floor, which is the other gallery's row
// -- the reporter's symptom again, by a different route.
//
// The pair of tests (this one and `TestAZipGalleryWhoseGalleryFileIsTheArchiveItself`) is what
// makes both arms of arm 1 load-bearing. Neither alone can: the zip fixture's archive path
// satisfies the query without the direct path, and the folder fixture's direct path satisfies it
// without the archive path.
func TestAFolderGalleryWhereTheImagesAreTheGalleryFiles(t *testing.T) {
	runWithRollbackTxn(t, "a folder gallery where the images are the gallery files", func(t *testing.T, ctx context.Context) {
		folderA := mkStash3849Folder(t, ctx, "/library/99f")
		folderB := mkStash3849Folder(t, ctx, "/library/01f")

		mkFile := func(folderID models.FolderID, name string) models.FileID {
			f := &models.BaseFile{Basename: name, ParentFolderID: folderID}
			require.NoError(t, db.File.Create(ctx, f))
			return f.ID
		}
		// NO ZipFileID anywhere: this is a folder gallery.
		f001 := mkFile(folderA, "001.jpg")
		f002 := mkFile(folderA, "002.jpg")
		// The OTHER gallery's copy first, so it is PRIMARY. Its key begins /library/01f and so
		// sorts BEFORE both images in this gallery.
		f003 := mkFile(folderB, "003.jpg")
		// ...and it is named 004.jpg here, so the two candidate keys are
		//     /library/01f/003.jpg   (wrong gallery -- sorts FIRST)   and
		//     /library/99f/004.jpg   (this gallery  -- sorts LAST)
		// which is the reported symptom exactly, and -- unlike the earlier fixtures -- makes the
		// final ORDER depend on which row the sort reads. The first version of this test used
		// 004.jpg in a gallery whose other rows were 001/002, so the correct answer and the
		// buggy one both put the shared image LAST, and every mutation of the correlation
		// survived. The order alone is not the assertion; this naming is what makes it one.
		f004 := mkFile(folderA, "004.jpg")

		mkImage := func(fileIDs ...models.FileID) *models.Image {
			img := &models.Image{}
			require.NoError(t, db.Image.Create(ctx, &models.CreateImageInput{Image: img, FileIDs: fileIDs}))
			return img
		}
		i001, i002, i003 := mkImage(f001), mkImage(f002), mkImage(f003, f004)

		g := &models.Gallery{}
		require.NoError(t, db.Gallery.Create(ctx, &models.CreateGalleryInput{Gallery: g}))
		gA := int(g.ID)
		require.NoError(t, db.Gallery.UpdateImages(ctx, gA, []int{
			int(i001.ID), int(i002.ID), int(i003.ID),
		}))
		// The gallery's files are the IMAGES THEMSELVES -- no archive, no members.
		galleryFiles := findFiles(t, ctx, f001, f002, f004)
		require.NoError(t, db.Gallery.Update(ctx, &models.UpdateGalleryInput{
			Gallery: &models.Gallery{ID: gA, Files: models.NewRelatedFiles(galleryFiles)},
		}))

		// THE PRECONDITION: the shared image's primary row is the one in the OTHER folder, and
		// 004.jpg is in this gallery's file set while 003.jpg is not.
		_, rows, err := db.QuerySQL(ctx, `
			SELECT files.basename FROM images
			JOIN images_files ON images_files.image_id = images.id
			JOIN files ON files.id = images_files.file_id
			WHERE images.id = ? AND images_files."primary" = 1`, []interface{}{int(i003.ID)})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, "003.jpg", rows[0][0], "the primary row must be the OTHER gallery's")

		first, second, shared := int(i001.ID), int(i002.ID), int(i003.ID)
		out := idsInGalleryOrdered(t, ctx, gA, "path")
		t.Logf("folder gallery path order: %v", out)
		assert.Equal(t, []int{first, second, shared}, out,
			"the shared image must sort by 004.jpg, which for a folder gallery is reachable ONLY "+
				"through the direct test against galleries_files. Drop that test and the "+
				"primary-file floor returns 003.jpg from the other folder instead")
	})
}

// A small helper, added because the three gallery fixtures all need the same thing and my first
// attempt inlined a triple-nested closure to produce a []models.File.
func findFiles(t *testing.T, ctx context.Context, ids ...models.FileID) []models.File {
	t.Helper()
	var out []models.File
	for _, id := range ids {
		fs, err := db.File.Find(ctx, id)
		require.NoError(t, err)
		require.Len(t, fs, 1, "looking a file up by its own id returns exactly that file")
		out = append(out, fs...)
	}
	return out
}
