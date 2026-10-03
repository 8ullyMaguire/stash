package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/doug-martin/goqu/v9"
	"github.com/doug-martin/goqu/v9/exp"
	"github.com/jmoiron/sqlx"
	"gopkg.in/guregu/null.v4"
	"gopkg.in/guregu/null.v4/zero"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/sliceutil"
	"github.com/stashapp/stash/pkg/utils"
)

const (
	sceneTable            = "scenes"
	scenesFilesTable      = "scenes_files"
	sceneIDColumn         = "scene_id"
	sceneDateColumn       = "date"
	performersScenesTable = "performers_scenes"
	scenesTagsTable       = "scenes_tags"
	scenesGalleriesTable  = "scenes_galleries"
	groupsScenesTable     = "groups_scenes"
	scenesURLsTable       = "scene_urls"
	sceneURLColumn        = "url"
	scenesViewDatesTable  = "scenes_view_dates"
	sceneViewDateColumn   = "view_date"
	scenesODatesTable     = "scenes_o_dates"
	sceneODateColumn      = "o_date"

	sceneCoverBlobColumn = "cover_blob"
)

// sceneAgeDateExpr is the scene date used when calculating a performer's age
// for a scene. The production date is when the scene was actually shot, so it
// is preferred over the release date where one is set. NULLIF is needed because
// dates are stored as empty strings in some older databases, and an empty
// production date must not mask a valid release date.
const sceneAgeDateExpr = "COALESCE(NULLIF(scenes.production_date, ''), scenes.date)"

type sceneRow struct {
	ID int `db:"id" goqu:"skipinsert"`
	// LibraryID is the sharing scope this row belongs to. Migration 105.
	//
	// Scan-only, and deliberately not carried onto the models type: which
	// library a row is in is a SERVING decision, not metadata about the row,
	// and putting it on models.Scene would make every metadata query carry a
	// column the UI has no use for and a future exporter would publish.
	//
	// It must exist here because the SELECT is table.All() and sqlx fails at
	// RUNTIME -- not compile time -- on a column with no destination. That is
	// the failure this comment is standing next to.
	LibraryID               null.Int    `db:"library_id,omitempty"`
	Title                   zero.String `db:"title"`
	Code                    zero.String `db:"code"`
	Details                 zero.String `db:"details"`
	Director                zero.String `db:"director"`
	Date                    NullDate    `db:"date"`
	DatePrecision           null.Int    `db:"date_precision"`
	ProductionDate          NullDate    `db:"production_date"`
	ProductionDatePrecision null.Int    `db:"production_date_precision"`
	// expressed as 1-100
	Rating       null.Int  `db:"rating"`
	Organized    bool      `db:"organized"`
	StudioID     null.Int  `db:"studio_id,omitempty"`
	CreatedAt    Timestamp `db:"created_at"`
	UpdatedAt    Timestamp `db:"updated_at"`
	ResumeTime   float64   `db:"resume_time"`
	PlayDuration float64   `db:"play_duration"`

	// not used in resolutions or updates
	CoverBlob zero.String `db:"cover_blob"`
}

func (r *sceneRow) fromScene(o models.Scene) {
	r.ID = o.ID
	r.Title = zero.StringFrom(o.Title)
	r.Code = zero.StringFrom(o.Code)
	r.Details = zero.StringFrom(o.Details)
	r.Director = zero.StringFrom(o.Director)
	r.Date = NullDateFromDatePtr(o.Date)
	r.DatePrecision = datePrecisionFromDatePtr(o.Date)
	r.ProductionDate = NullDateFromDatePtr(o.ProductionDate)
	r.ProductionDatePrecision = datePrecisionFromDatePtr(o.ProductionDate)
	r.Rating = intFromPtr(o.Rating)
	r.Organized = o.Organized
	r.StudioID = intFromPtr(o.StudioID)
	r.CreatedAt = Timestamp{Timestamp: o.CreatedAt}
	r.UpdatedAt = Timestamp{Timestamp: o.UpdatedAt}
	r.ResumeTime = o.ResumeTime
	r.PlayDuration = o.PlayDuration
}

type sceneQueryRow struct {
	sceneRow
	PrimaryFileID         null.Int    `db:"primary_file_id"`
	PrimaryFileFolderPath zero.String `db:"primary_file_folder_path"`
	PrimaryFileBasename   zero.String `db:"primary_file_basename"`
	PrimaryFileOshash     zero.String `db:"primary_file_oshash"`
	PrimaryFileChecksum   zero.String `db:"primary_file_checksum"`
}

func (r *sceneQueryRow) resolve() *models.Scene {
	ret := &models.Scene{
		ID:             r.ID,
		Title:          r.Title.String,
		Code:           r.Code.String,
		Details:        r.Details.String,
		Director:       r.Director.String,
		Date:           r.Date.DatePtr(r.DatePrecision),
		ProductionDate: r.ProductionDate.DatePtr(r.ProductionDatePrecision),
		Rating:         nullIntPtr(r.Rating),
		Organized:      r.Organized,
		StudioID:       nullIntPtr(r.StudioID),

		PrimaryFileID: nullIntFileIDPtr(r.PrimaryFileID),
		OSHash:        r.PrimaryFileOshash.String,
		Checksum:      r.PrimaryFileChecksum.String,

		CreatedAt: r.CreatedAt.Timestamp,
		UpdatedAt: r.UpdatedAt.Timestamp,

		ResumeTime:   r.ResumeTime,
		PlayDuration: r.PlayDuration,
	}

	if r.PrimaryFileFolderPath.Valid && r.PrimaryFileBasename.Valid {
		ret.Path = filepath.Join(r.PrimaryFileFolderPath.String, r.PrimaryFileBasename.String)
	}

	return ret
}

type sceneRowRecord struct {
	updateRecord
}

func (r *sceneRowRecord) fromPartial(o models.ScenePartial) {
	r.setNullString("title", o.Title)
	r.setNullString("code", o.Code)
	r.setNullString("details", o.Details)
	r.setNullString("director", o.Director)
	r.setNullDate("date", "date_precision", o.Date)
	r.setNullDate("production_date", "production_date_precision", o.ProductionDate)
	r.setNullInt("rating", o.Rating)
	r.setBool("organized", o.Organized)
	r.setNullInt("studio_id", o.StudioID)
	r.setTimestamp("created_at", o.CreatedAt)
	r.setTimestamp("updated_at", o.UpdatedAt)
	r.setFloat64("resume_time", o.ResumeTime)
	r.setFloat64("play_duration", o.PlayDuration)
}

type sceneRepositoryType struct {
	repository
	galleries  joinRepository
	tags       joinRepository
	performers joinRepository
	groups     repository

	files filesRepository

	stashIDs stashIDRepository
}

var (
	sceneRepository = sceneRepositoryType{
		repository: repository{
			tableName: sceneTable,
			idColumn:  idColumn,
		},
		galleries: joinRepository{
			repository: repository{
				tableName: scenesGalleriesTable,
				idColumn:  sceneIDColumn,
			},
			fkColumn: galleryIDColumn,
		},
		tags: joinRepository{
			repository: repository{
				tableName: scenesTagsTable,
				idColumn:  sceneIDColumn,
			},
			fkColumn:     tagIDColumn,
			foreignTable: tagTable,
			orderBy:      tagTableSortSQL,
		},
		performers: joinRepository{
			repository: repository{
				tableName: performersScenesTable,
				idColumn:  sceneIDColumn,
			},
			fkColumn: performerIDColumn,
		},
		groups: repository{
			tableName: groupsScenesTable,
			idColumn:  sceneIDColumn,
		},
		files: filesRepository{
			repository: repository{
				tableName: scenesFilesTable,
				idColumn:  sceneIDColumn,
			},
		},
		stashIDs: stashIDRepository{
			repository{
				tableName: "scene_stash_ids",
				idColumn:  sceneIDColumn,
			},
		},
	}
)

type SceneStore struct {
	blobJoinQueryBuilder
	customFieldsStore

	tableMgr *table
	oDateManager
	viewDateManager

	repo *storeRepository
}

func NewSceneStore(r *storeRepository, blobStore *BlobStore) *SceneStore {
	return &SceneStore{
		blobJoinQueryBuilder: blobJoinQueryBuilder{
			blobStore: blobStore,
			joinTable: sceneTable,
		},
		customFieldsStore: customFieldsStore{
			table: scenesCustomFieldsTable,
			fk:    scenesCustomFieldsTable.Col(sceneIDColumn),
		},

		tableMgr:        sceneTableMgr,
		viewDateManager: viewDateManager{scenesViewTableMgr},
		oDateManager:    oDateManager{scenesOTableMgr},
		repo:            r,
	}
}

func (qb *SceneStore) table() exp.IdentifierExpression {
	return qb.tableMgr.table
}

func (qb *SceneStore) selectDataset() *goqu.SelectDataset {
	table := qb.table()
	files := fileTableMgr.table
	folders := folderTableMgr.table
	checksum := fingerprintTableMgr.table.As("fingerprint_md5")
	oshash := fingerprintTableMgr.table.As("fingerprint_oshash")

	return dialect.From(table).LeftJoin(
		scenesFilesJoinTable,
		goqu.On(
			scenesFilesJoinTable.Col(sceneIDColumn).Eq(table.Col(idColumn)),
			scenesFilesJoinTable.Col("primary").Eq(1),
		),
	).LeftJoin(
		files,
		goqu.On(files.Col(idColumn).Eq(scenesFilesJoinTable.Col(fileIDColumn))),
	).LeftJoin(
		folders,
		goqu.On(folders.Col(idColumn).Eq(files.Col("parent_folder_id"))),
	).LeftJoin(
		checksum,
		goqu.On(
			checksum.Col(fileIDColumn).Eq(scenesFilesJoinTable.Col(fileIDColumn)),
			checksum.Col("type").Eq(models.FingerprintTypeMD5),
		),
	).LeftJoin(
		oshash,
		goqu.On(
			oshash.Col(fileIDColumn).Eq(scenesFilesJoinTable.Col(fileIDColumn)),
			oshash.Col("type").Eq(models.FingerprintTypeOshash),
		),
	).Select(
		qb.table().All(),
		scenesFilesJoinTable.Col(fileIDColumn).As("primary_file_id"),
		folders.Col("path").As("primary_file_folder_path"),
		files.Col("basename").As("primary_file_basename"),
		checksum.Col("fingerprint").As("primary_file_checksum"),
		oshash.Col("fingerprint").As("primary_file_oshash"),
	)
}

func (qb *SceneStore) Create(ctx context.Context, newObject *models.Scene, fileIDs []models.FileID) error {
	var r sceneRow
	r.fromScene(*newObject)

	id, err := qb.tableMgr.insertID(ctx, r)
	if err != nil {
		return err
	}

	if len(fileIDs) > 0 {
		const firstPrimary = true
		if err := scenesFilesTableMgr.insertJoins(ctx, id, firstPrimary, fileIDs); err != nil {
			return err
		}
	}

	if newObject.URLs.Loaded() {
		const startPos = 0
		if err := scenesURLsTableMgr.insertJoins(ctx, id, startPos, newObject.URLs.List()); err != nil {
			return err
		}
	}

	if newObject.PerformerIDs.Loaded() {
		if err := scenesPerformersTableMgr.insertJoins(ctx, id, newObject.PerformerIDs.List()); err != nil {
			return err
		}
	}
	if newObject.TagIDs.Loaded() {
		if err := scenesTagsTableMgr.insertJoins(ctx, id, newObject.TagIDs.List()); err != nil {
			return err
		}
	}

	if newObject.GalleryIDs.Loaded() {
		if err := scenesGalleriesTableMgr.insertJoins(ctx, id, newObject.GalleryIDs.List()); err != nil {
			return err
		}
	}

	if newObject.StashIDs.Loaded() {
		if err := scenesStashIDsTableMgr.insertJoins(ctx, id, newObject.StashIDs.List()); err != nil {
			return err
		}
	}

	if newObject.Groups.Loaded() {
		if err := scenesGroupsTableMgr.insertJoins(ctx, id, newObject.Groups.List()); err != nil {
			return err
		}
	}

	updated, err := qb.find(ctx, id)
	if err != nil {
		return fmt.Errorf("finding after create: %w", err)
	}

	*newObject = *updated

	return nil
}

func (qb *SceneStore) UpdatePartial(ctx context.Context, id int, partial models.ScenePartial) (*models.Scene, error) {
	r := sceneRowRecord{
		updateRecord{
			Record: make(exp.Record),
		},
	}

	r.fromPartial(partial)

	if len(r.Record) > 0 {
		if err := qb.tableMgr.updateByID(ctx, id, r.Record); err != nil {
			return nil, err
		}
	}

	if partial.URLs != nil {
		if err := scenesURLsTableMgr.modifyJoins(ctx, id, partial.URLs.Values, partial.URLs.Mode); err != nil {
			return nil, err
		}
	}
	if partial.PerformerIDs != nil {
		if err := scenesPerformersTableMgr.modifyJoins(ctx, id, partial.PerformerIDs.IDs, partial.PerformerIDs.Mode); err != nil {
			return nil, err
		}
	}
	if partial.TagIDs != nil {
		if err := scenesTagsTableMgr.modifyJoins(ctx, id, partial.TagIDs.IDs, partial.TagIDs.Mode); err != nil {
			return nil, err
		}
	}
	if partial.GalleryIDs != nil {
		if err := scenesGalleriesTableMgr.modifyJoins(ctx, id, partial.GalleryIDs.IDs, partial.GalleryIDs.Mode); err != nil {
			return nil, err
		}
	}
	if partial.StashIDs != nil {
		if err := scenesStashIDsTableMgr.modifyJoins(ctx, id, partial.StashIDs.StashIDs, partial.StashIDs.Mode); err != nil {
			return nil, err
		}
	}
	if partial.GroupIDs != nil {
		if err := scenesGroupsTableMgr.modifyJoins(ctx, id, partial.GroupIDs.Groups, partial.GroupIDs.Mode); err != nil {
			return nil, err
		}
	}
	if partial.PrimaryFileID != nil {
		if err := scenesFilesTableMgr.setPrimary(ctx, id, *partial.PrimaryFileID); err != nil {
			return nil, err
		}
	}

	return qb.find(ctx, id)
}

func (qb *SceneStore) Update(ctx context.Context, updatedObject *models.Scene) error {
	var r sceneRow
	r.fromScene(*updatedObject)

	if err := qb.tableMgr.updateByID(ctx, updatedObject.ID, r); err != nil {
		return err
	}

	if updatedObject.URLs.Loaded() {
		if err := scenesURLsTableMgr.replaceJoins(ctx, updatedObject.ID, updatedObject.URLs.List()); err != nil {
			return err
		}
	}

	if updatedObject.PerformerIDs.Loaded() {
		if err := scenesPerformersTableMgr.replaceJoins(ctx, updatedObject.ID, updatedObject.PerformerIDs.List()); err != nil {
			return err
		}
	}

	if updatedObject.TagIDs.Loaded() {
		if err := scenesTagsTableMgr.replaceJoins(ctx, updatedObject.ID, updatedObject.TagIDs.List()); err != nil {
			return err
		}
	}

	if updatedObject.GalleryIDs.Loaded() {
		if err := scenesGalleriesTableMgr.replaceJoins(ctx, updatedObject.ID, updatedObject.GalleryIDs.List()); err != nil {
			return err
		}
	}

	if updatedObject.StashIDs.Loaded() {
		if err := scenesStashIDsTableMgr.replaceJoins(ctx, updatedObject.ID, updatedObject.StashIDs.List()); err != nil {
			return err
		}
	}

	if updatedObject.Groups.Loaded() {
		if err := scenesGroupsTableMgr.replaceJoins(ctx, updatedObject.ID, updatedObject.Groups.List()); err != nil {
			return err
		}
	}

	if updatedObject.Files.Loaded() {
		fileIDs := make([]models.FileID, len(updatedObject.Files.List()))
		for i, f := range updatedObject.Files.List() {
			fileIDs[i] = f.ID
		}

		if err := scenesFilesTableMgr.replaceJoins(ctx, updatedObject.ID, fileIDs); err != nil {
			return err
		}
	}

	return nil
}

func (qb *SceneStore) Destroy(ctx context.Context, id int) error {
	// must handle image checksums manually
	if err := qb.destroyCover(ctx, id); err != nil {
		return err
	}

	// scene markers should be handled prior to calling destroy
	// galleries should be handled prior to calling destroy

	return qb.tableMgr.destroyExisting(ctx, []int{id})
}

// returns nil, nil if not found
func (qb *SceneStore) Find(ctx context.Context, id int) (*models.Scene, error) {
	ret, err := qb.find(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return ret, err
}

// FindByIDs finds multiple scenes by their IDs.
// No check is made to see if the scenes exist, and the order of the returned scenes
// is not guaranteed to be the same as the order of the input IDs.
func (qb *SceneStore) FindByIDs(ctx context.Context, ids []int) ([]*models.Scene, error) {
	scenes := make([]*models.Scene, 0, len(ids))

	table := qb.table()
	if err := batchExec(ids, defaultBatchSize, func(batch []int) error {
		q := qb.selectDataset().Prepared(true).Where(table.Col(idColumn).In(batch))
		unsorted, err := qb.getMany(ctx, q)
		if err != nil {
			return err
		}

		scenes = append(scenes, unsorted...)

		return nil
	}); err != nil {
		return nil, err
	}

	return scenes, nil
}

func (qb *SceneStore) FindMany(ctx context.Context, ids []int) ([]*models.Scene, error) {
	scenes := make([]*models.Scene, len(ids))

	unsorted, err := qb.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	for _, s := range unsorted {
		i := slices.Index(ids, s.ID)
		scenes[i] = s
	}

	for i := range scenes {
		if scenes[i] == nil {
			return nil, fmt.Errorf("scene with id %d not found", ids[i])
		}
	}

	return scenes, nil
}

// returns nil, sql.ErrNoRows if not found
func (qb *SceneStore) find(ctx context.Context, id int) (*models.Scene, error) {
	q := qb.selectDataset().Where(qb.tableMgr.byID(id))

	ret, err := qb.get(ctx, q)
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (qb *SceneStore) findBySubquery(ctx context.Context, sq *goqu.SelectDataset) ([]*models.Scene, error) {
	table := qb.table()

	q := qb.selectDataset().Where(
		table.Col(idColumn).Eq(
			sq,
		),
	)

	return qb.getMany(ctx, q)
}

// returns nil, sql.ErrNoRows if not found
func (qb *SceneStore) get(ctx context.Context, q *goqu.SelectDataset) (*models.Scene, error) {
	ret, err := qb.getMany(ctx, q)
	if err != nil {
		return nil, err
	}

	if len(ret) == 0 {
		return nil, sql.ErrNoRows
	}

	return ret[0], nil
}

func (qb *SceneStore) getMany(ctx context.Context, q *goqu.SelectDataset) ([]*models.Scene, error) {
	const single = false
	var ret []*models.Scene
	var lastID int
	if err := queryFunc(ctx, q, single, func(r *sqlx.Rows) error {
		var f sceneQueryRow
		if err := r.StructScan(&f); err != nil {
			return err
		}

		s := f.resolve()
		if s.ID == lastID {
			return fmt.Errorf("internal error: multiple rows returned for single scene id %d", s.ID)
		}
		lastID = s.ID

		ret = append(ret, s)
		return nil
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

// sceneFileRanges returns, per file id, the DURATION a scene should show for that file — the
// file's own duration reduced by the scene's time range, or absent when the scene has no
// range (the whole file).
//
// Returning an ABSENT entry rather than the file duration is deliberate: it lets the caller
// leave a file untouched, which is what keeps `video_files.duration` true everywhere else.
// A map with an entry for every file would be indistinguishable from "every file was
// rewritten", which is the failure this shape is meant to make impossible.
//
// NULL/NULL yields no entry at all, so an untouched scene cannot be affected by arithmetic
// it does not need — the guarantee that no existing scene's duration changes.
// #3530 - now returns the CLAMPED WINDOW itself, not just the length.
//
// It returned only a duration at first, and the play URL then had no way to learn where a scene
// starts: the handler needs both ends to build -ss and -t. Returning the window means the clamp
// below is the single place a window is made legal, so the duration the API reports and the
// offset ffmpeg is given cannot drift apart — which is exactly the drift a second implementation
// of the clamp would cause.
type sceneFileRange struct {
	start float64
	end   float64 // clamped; 0 means "to the end of the file" only when start is also 0
}

func (qb *SceneStore) sceneFileRanges(ctx context.Context, id int) (map[int]sceneFileRange, error) {
	q := dialect.From(scenesFilesJoinTable).
		Select(
			scenesFilesJoinTable.Col(fileIDColumn),
			scenesFilesJoinTable.Col("start_time"),
			scenesFilesJoinTable.Col("end_time"),
		).
		Where(scenesFilesJoinTable.Col(sceneIDColumn).Eq(id)).
		// A row with no range at all is the whole file: leave it alone rather than
		// computing `file - 0`, which is the same number by a different route and would
		// make "was this scene ranged?" unanswerable from the result.
		Where(goqu.Or(
			scenesFilesJoinTable.Col("start_time").IsNotNull(),
			scenesFilesJoinTable.Col("end_time").IsNotNull(),
		))

	var rows []struct {
		FileID    int        `db:"file_id"`
		StartTime null.Float `db:"start_time"`
		EndTime   null.Float `db:"end_time"`
	}

	const single = false
	if err := queryFunc(ctx, q, single, func(r *sqlx.Rows) error {
		var row struct {
			FileID    int        `db:"file_id"`
			StartTime null.Float `db:"start_time"`
			EndTime   null.Float `db:"end_time"`
		}
		if err := r.StructScan(&row); err != nil {
			return err
		}
		rows = append(rows, row)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("getting time ranges for scene %d: %w", id, err)
	}

	if len(rows) == 0 {
		return nil, nil
	}

	// The file's own durations, so a NULL end can mean "to the end of the file".
	fileDurations := make(map[int]float64, len(rows))
	ids := make([]models.FileID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, models.FileID(r.FileID))
	}
	files, err := qb.repo.File.Find(ctx, ids...)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if vf, ok := f.(*models.VideoFile); ok {
			fileDurations[int(vf.ID)] = vf.Duration
		}
	}

	ret := make(map[int]sceneFileRange, len(rows))
	for _, r := range rows {
		fileDur := fileDurations[r.FileID]

		// Same reduction as the SQL CASE in docs/ISSUE-3530-spec.md section 8: a NULL end
		// is the file's length, a NULL start is the beginning.
		//
		// Note that an open-ended range on a file whose length ffprobe could not determine
		// has NO computable duration: the end is unknown, not zero. It reports 0 because
		// `fileDur` is 0 here, and that is the honest answer -- "this scene's length is not
		// known" is what a 0 duration already means everywhere else in the schema.
		// `video_files.duration` is NULL/0 for files still being written, partial downloads
		// and containers ffprobe does not recognise, and a bounded range on such a file
		// still works (the CLAMP note below explains why it must).
		end := fileDur
		if r.EndTime.Valid {
			end = r.EndTime.Float64
		}
		start := 0.0
		if r.StartTime.Valid {
			start = r.StartTime.Float64
		}

		// CLAMP, for the one window shape the schema CANNOT refuse.
		//
		// The three CHECKs are `start >= 0`, `end >= 0` and `end > start`, and a CHECK may
		// not reference another table -- so none of them can compare against
		// video_files.duration. A window that runs OFF THE END of the file therefore
		// satisfies all three and is legal to create through any store or API:
		//
		//     start 300, end NULL, file 2700s   -> a NEGATIVE duration
		//     start 2600, end 2900, file 2700s  -> 300s for a file that stops at 2700
		//
		// Both are wrong, and a negative duration reaches sort order, `play_duration`
		// arithmetic and the timeline the UI draws with nothing downstream checking a sign.
		//
		// ONE mechanism, not two. An earlier draft had `min(end, file)`, then `min(start, end)`,
		// and then `max(end-start, 0)` — and a mutation sweep killed only 5 of 7, with the
		// two survivors being exactly those last two guards. Each was masked by the other:
		// with the start clamp in place `end - start` is never negative, so the `max(...,0)`
		// arm was dead code; and with the `max` arm in place the start clamp was untestable.
		// A guard that cannot be individually justified is not protection, it is a comment
		// that looks like one. So the clamp is the start, and the arithmetic below is plain:
		//
		//     start = min(start, end)   end is already clamped to what the file has
		//     duration = end - start    >= 0 by construction, so no second guard is needed
		//
		// and deleting this clamp is a mutant the tests kill.
		//
		// The `fileDur > 0` guard matters: `video_files.duration` is NULL/0 whenever ffprobe
		// could not determine it, which is common for a partial or still-growing file. Without
		// it, `min(end, 0)` would collapse a perfectly good range to nothing.
		if fileDur > 0 && end > fileDur {
			end = fileDur
		}
		if start > end {
			start = end
		}

		ret[r.FileID] = sceneFileRange{start: start, end: end}
	}

	return ret, nil
}

// GetFiles returns a scene's files, with each file's Duration adjusted to the scene's TIME
// RANGE within it (migration 122, docs/ISSUE-3530-spec.md).
//
// ## WHY THIS IS THE ONE PLACE THAT MATTERS
//
// A scene has no duration field of its own: `type Scene` in
// graphql/schema/types/scene.graphql carries `play_duration` (seconds WATCHED, not length)
// and `files`. The length a user sees comes from the file — `SceneListTable.tsx:88` reads
// `scene.files[0].duration`, and `SceneCard.tsx:518` likewise. So THIS is the chokepoint for
// "how long is this scene", and it is not a scene-query column.
//
// ## WHY THE FILE ITSELF MUST NOT BE TOUCHED
//
// `video_files.duration` is TRUE: it is how long the file lasts. `FindFiles.duration`,
// the scene detail page's file list and the player all read it, and a segment's file is
// still the whole 45 minutes. So the range is applied to the freshly-loaded COPY here and
// never written back — `FileStore.Find` builds a new object per call (`qb.find`, no cache),
// so mutating `Duration` cannot leak into another scene that shares the same file.
//
// ## WHY NULL/NNULL IS EXACTLY RIGHT
//
// A row with no range is the whole file, and `COALESCE(end, file) - COALESCE(start, 0)`
// reduces to `video_files.duration` in that case. Every pre-existing scene is NULL/NULL, so
// no existing scene's duration changes — asserted in TestAScenesDurationIsUnchangedForEvery
// ExistingScene.
func (qb *SceneStore) GetFiles(ctx context.Context, id int) ([]*models.VideoFile, error) {
	fileIDs, err := sceneRepository.files.get(ctx, id)
	if err != nil {
		return nil, err
	}

	// use fileStore to load files
	files, err := qb.repo.File.Find(ctx, fileIDs...)
	if err != nil {
		return nil, err
	}

	ranges, err := qb.sceneFileRanges(ctx, id)
	if err != nil {
		return nil, err
	}

	ret := make([]*models.VideoFile, len(files))
	for i, f := range files {
		var ok bool
		ret[i], ok = f.(*models.VideoFile)
		if !ok {
			return nil, fmt.Errorf("expected file to be *file.VideoFile not %T", f)
		}

		// The window rides along on this per-scene copy of the file, so the handler can
		// build -ss/-t without a second query. See models.VideoFile.StartTime for why
		// putting it on VideoFile is sound (GetFiles returns a fresh copy per call).
		if r, ok := ranges[int(ret[i].ID)]; ok {
			ret[i].Duration = r.end - r.start

			// #3530 - nil means "no window". Copying the values out of the range struct
			// rather than taking its address keeps the map's storage private.
			start, end := r.start, r.end
			ret[i].StartTime = &start
			ret[i].EndTime = &end
		}
	}

	return ret, nil
}

// GetPrimaryFile returns a scene's PRIMARY file, with the scene's time range applied -- the window
// carried on StartTime/EndTime and the derived Duration, exactly as GetFiles does.
//
// # WHY THIS EXISTS, AND WHY IT IS NOT A CHANGE TO LoadPrimaryFile
//
// The window lives on `scenes_files`, which is a SCENE's relationship to a file. FileStore.Find
// looks a file up by id and does not join that table at all, so it CANNOT report a window: there is
// no scene id in scope. Measured, not assumed -- pkg/sqlite/scene_window_loader_test.go drives both
// loaders over one ranged scene and asserts that LoadPrimaryFile reports the file's own 1800s and
// nil StartTime where GetFiles reports 60..300 and 240s.
//
// So LoadPrimaryFile is left alone. It answers "what is this file", and folding a scene's window into
// it would be exactly the mistake the preview key spec refused (#3530-previewkey: GetHash must not
// depend on which window of the file you are looking at). The same separation applies to the loader.
//
// What this adds is the WINDOW-AWARE half, for callers that need the scene's view of its file rather
// than the file's -- the cover and the preview already had one, by calling GetFiles and taking the
// primary from its result, and the sprite generator needs the same thing. primaryFileID is the
// caller's, because the scene carries it (`scene.PrimaryFileID`) and a store cannot see the scene.
//
// A scene with no primary file yields (nil, nil), matching LoadPrimaryFile, so a caller can use
// either without a second nil check.
func (qb *SceneStore) GetPrimaryFile(ctx context.Context, id int, primaryFileID models.FileID) (*models.VideoFile, error) {
	files, err := qb.GetFiles(ctx, id)
	if err != nil {
		return nil, err
	}

	for _, f := range files {
		if f.ID == primaryFileID {
			return f, nil
		}
	}

	return nil, nil
}

func (qb *SceneStore) GetManyFileIDs(ctx context.Context, ids []int) ([][]models.FileID, error) {
	const primaryOnly = false
	return sceneRepository.files.getMany(ctx, ids, primaryOnly)
}

func (qb *SceneStore) FindByFileID(ctx context.Context, fileID models.FileID) ([]*models.Scene, error) {
	sq := dialect.From(scenesFilesJoinTable).Select(scenesFilesJoinTable.Col(sceneIDColumn)).Where(
		scenesFilesJoinTable.Col(fileIDColumn).Eq(fileID),
	)

	ret, err := qb.findBySubquery(ctx, sq)
	if err != nil {
		return nil, fmt.Errorf("getting scenes by file id %d: %w", fileID, err)
	}

	return ret, nil
}

func (qb *SceneStore) GetManyIDsByFileIDs(ctx context.Context, fileIDs []models.FileID) ([][]int, error) {
	sq := dialect.From(scenesFilesJoinTable).Select(scenesFilesJoinTable.Col(sceneIDColumn), scenesFilesJoinTable.Col(fileIDColumn)).Where(
		scenesFilesJoinTable.Col(fileIDColumn).In(fileIDs),
	)

	sql, args, err := sq.ToSQL()
	if err != nil {
		return nil, fmt.Errorf("building query: %w", err)
	}

	var results []struct {
		SceneID int           `db:"scene_id"`
		FileID  models.FileID `db:"file_id"`
	}

	if err := querySelect(ctx, sql, args, &results); err != nil {
		return nil, fmt.Errorf("getting scenes by file ids %v: %w", fileIDs, err)
	}

	retMap := make(map[models.FileID][]int)
	for _, r := range results {
		retMap[r.FileID] = append(retMap[r.FileID], r.SceneID)
	}

	ret := make([][]int, len(fileIDs))
	for i, id := range fileIDs {
		ret[i] = retMap[id]
	}

	return ret, nil
}

func (qb *SceneStore) FindByPrimaryFileID(ctx context.Context, fileID models.FileID) ([]*models.Scene, error) {
	sq := dialect.From(scenesFilesJoinTable).Select(scenesFilesJoinTable.Col(sceneIDColumn)).Where(
		scenesFilesJoinTable.Col(fileIDColumn).Eq(fileID),
		scenesFilesJoinTable.Col("primary").Eq(1),
	)

	ret, err := qb.findBySubquery(ctx, sq)
	if err != nil {
		return nil, fmt.Errorf("getting scenes by primary file id %d: %w", fileID, err)
	}

	return ret, nil
}

func (qb *SceneStore) CountByFileID(ctx context.Context, fileID models.FileID) (int, error) {
	joinTable := scenesFilesJoinTable

	q := dialect.Select(goqu.COUNT("*")).From(joinTable).Where(joinTable.Col(fileIDColumn).Eq(fileID))
	return count(ctx, q)
}

func (qb *SceneStore) FindByFingerprints(ctx context.Context, fp []models.Fingerprint) ([]*models.Scene, error) {
	fingerprintTable := fingerprintTableMgr.table

	var ex []exp.Expression

	for _, v := range fp {
		ex = append(ex, goqu.And(
			fingerprintTable.Col("type").Eq(v.Type),
			fingerprintTable.Col("fingerprint").Eq(v.Fingerprint),
		))
	}

	sq := dialect.From(scenesFilesJoinTable).
		InnerJoin(
			fingerprintTable,
			goqu.On(fingerprintTable.Col(fileIDColumn).Eq(scenesFilesJoinTable.Col(fileIDColumn))),
		).
		Select(scenesFilesJoinTable.Col(sceneIDColumn)).Where(goqu.Or(ex...))

	ret, err := qb.findBySubquery(ctx, sq)
	if err != nil {
		return nil, fmt.Errorf("getting scenes by fingerprints: %w", err)
	}

	return ret, nil
}

func (qb *SceneStore) FindByChecksum(ctx context.Context, checksum string) ([]*models.Scene, error) {
	return qb.FindByFingerprints(ctx, []models.Fingerprint{
		{
			Type:        models.FingerprintTypeMD5,
			Fingerprint: checksum,
		},
	})
}

func (qb *SceneStore) FindByOSHash(ctx context.Context, oshash string) ([]*models.Scene, error) {
	return qb.FindByFingerprints(ctx, []models.Fingerprint{
		{
			Type:        models.FingerprintTypeOshash,
			Fingerprint: oshash,
		},
	})
}

func (qb *SceneStore) FindByPath(ctx context.Context, p string) ([]*models.Scene, error) {
	filesTable := fileTableMgr.table
	foldersTable := folderTableMgr.table
	basename := filepath.Base(p)
	dir := filepath.Dir(p)

	sq := dialect.From(scenesFilesJoinTable).InnerJoin(
		filesTable,
		goqu.On(filesTable.Col(idColumn).Eq(scenesFilesJoinTable.Col(fileIDColumn))),
	).InnerJoin(
		foldersTable,
		goqu.On(foldersTable.Col(idColumn).Eq(filesTable.Col("parent_folder_id"))),
	).Select(scenesFilesJoinTable.Col(sceneIDColumn))

	if pathHasWildcard(basename) || pathHasWildcard(dir) {
		sq = sq.Where(
			pathLike(foldersTable.Col("path"), dir),
			pathLike(filesTable.Col("basename"), basename),
		)
	} else {
		sq = sq.Where(
			pathEqNoCase(foldersTable.Col("path"), dir),
			pathEqNoCase(filesTable.Col("basename"), basename),
		)
	}

	ret, err := qb.findBySubquery(ctx, sq)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("getting scene by path %s: %w", p, err)
	}

	return ret, nil
}

func (qb *SceneStore) FindByPerformerID(ctx context.Context, performerID int) ([]*models.Scene, error) {
	sq := dialect.From(scenesPerformersJoinTable).Select(scenesPerformersJoinTable.Col(sceneIDColumn)).Where(
		scenesPerformersJoinTable.Col(performerIDColumn).Eq(performerID),
	)
	ret, err := qb.findBySubquery(ctx, sq)

	if err != nil {
		return nil, fmt.Errorf("getting scenes for performer %d: %w", performerID, err)
	}

	return ret, nil
}

func (qb *SceneStore) FindByGalleryID(ctx context.Context, galleryID int) ([]*models.Scene, error) {
	sq := dialect.From(galleriesScenesJoinTable).Select(galleriesScenesJoinTable.Col(sceneIDColumn)).Where(
		galleriesScenesJoinTable.Col(galleryIDColumn).Eq(galleryID),
	)
	ret, err := qb.findBySubquery(ctx, sq)

	if err != nil {
		return nil, fmt.Errorf("getting scenes for gallery %d: %w", galleryID, err)
	}

	return ret, nil
}

func (qb *SceneStore) CountByPerformerID(ctx context.Context, performerID int) (int, error) {
	joinTable := scenesPerformersJoinTable

	q := dialect.Select(goqu.COUNT("*")).From(joinTable).Where(joinTable.Col(performerIDColumn).Eq(performerID))
	return count(ctx, q)
}

func (qb *SceneStore) OCountByPerformerID(ctx context.Context, performerID int) (int, error) {
	table := qb.table()
	joinTable := scenesPerformersJoinTable
	oHistoryTable := goqu.T(scenesODatesTable)

	q := dialect.Select(goqu.COUNT("*")).From(table).InnerJoin(
		oHistoryTable,
		goqu.On(table.Col(idColumn).Eq(oHistoryTable.Col(sceneIDColumn))),
	).InnerJoin(
		joinTable,
		goqu.On(
			table.Col(idColumn).Eq(joinTable.Col(sceneIDColumn)),
		),
	).Where(joinTable.Col(performerIDColumn).Eq(performerID))

	var ret int
	if err := querySimple(ctx, q, &ret); err != nil {
		return 0, err
	}

	return ret, nil
}

func (qb *SceneStore) OCountByGroupID(ctx context.Context, groupID int) (int, error) {
	table := qb.table()
	joinTable := scenesGroupsJoinTable
	oHistoryTable := goqu.T(scenesODatesTable)

	q := dialect.Select(goqu.COUNT("*")).From(table).InnerJoin(
		oHistoryTable,
		goqu.On(table.Col(idColumn).Eq(oHistoryTable.Col(sceneIDColumn))),
	).InnerJoin(
		joinTable,
		goqu.On(
			table.Col(idColumn).Eq(joinTable.Col(sceneIDColumn)),
		),
	).Where(joinTable.Col(groupIDColumn).Eq(groupID))

	var ret int
	if err := querySimple(ctx, q, &ret); err != nil {
		return 0, err
	}

	return ret, nil
}

func (qb *SceneStore) OCountByStudioID(ctx context.Context, studioID int, depth int) (int, error) {
	var ret int

	if depth != 0 {
		return qb.oCountByStudioIDRecursive(ctx, studioID, depth)
	}

	table := qb.table()
	oHistoryTable := goqu.T(scenesODatesTable)

	q := dialect.Select(goqu.COUNT("*")).From(table).InnerJoin(
		oHistoryTable,
		goqu.On(table.Col(idColumn).Eq(oHistoryTable.Col(sceneIDColumn))),
	).Where(table.Col(studioIDColumn).Eq(studioID))

	if err := querySimple(ctx, q, &ret); err != nil {
		return 0, err
	}

	return ret, nil
}

func (qb *SceneStore) oCountByStudioIDRecursive(ctx context.Context, studioID int, depth int) (int, error) {
	q := `
	WITH RECURSIVE sub_studios AS (
		SELECT id, 0 AS level FROM studios WHERE id = ?
		UNION ALL
		SELECT s.id, ss.level + 1 FROM studios s
		INNER JOIN sub_studios ss ON s.parent_id = ss.id
		WHERE ss.level < ? OR ? < 0
	)
	SELECT COUNT(*) FROM scenes
	INNER JOIN scenes_o_dates ON scenes.id = scenes_o_dates.scene_id
	WHERE scenes.studio_id IN (SELECT id FROM sub_studios)`

	rows, err := dbWrapper.QueryxContext(ctx, q, studioID, depth, depth)
	if err != nil {
		return 0, fmt.Errorf("querying scene o_count by studio: %w", err)
	}
	defer rows.Close()

	var ret int
	for rows.Next() {
		if err := rows.Scan(&ret); err != nil {
			return 0, fmt.Errorf("scanning scene o_count: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterating scene o_count rows: %w", err)
	}
	return ret, nil
}

func (qb *SceneStore) FindByGroupID(ctx context.Context, groupID int) ([]*models.Scene, error) {
	sq := dialect.From(scenesGroupsJoinTable).Select(scenesGroupsJoinTable.Col(sceneIDColumn)).Where(
		scenesGroupsJoinTable.Col(groupIDColumn).Eq(groupID),
	)
	ret, err := qb.findBySubquery(ctx, sq)

	if err != nil {
		return nil, fmt.Errorf("getting scenes for group %d: %w", groupID, err)
	}

	return ret, nil
}

func (qb *SceneStore) Count(ctx context.Context) (int, error) {
	q := dialect.Select(goqu.COUNT("*")).From(qb.table())
	return count(ctx, q)
}

func (qb *SceneStore) Size(ctx context.Context) (float64, error) {
	table := qb.table()
	fileTable := fileTableMgr.table
	q := dialect.Select(
		goqu.COALESCE(goqu.SUM(fileTableMgr.table.Col("size")), 0),
	).From(table).InnerJoin(
		scenesFilesJoinTable,
		goqu.On(table.Col(idColumn).Eq(scenesFilesJoinTable.Col(sceneIDColumn))),
	).InnerJoin(
		fileTable,
		goqu.On(scenesFilesJoinTable.Col(fileIDColumn).Eq(fileTable.Col(idColumn))),
	)
	var ret float64
	if err := querySimple(ctx, q, &ret); err != nil {
		return 0, err
	}

	return ret, nil
}

func (qb *SceneStore) Duration(ctx context.Context) (float64, error) {
	table := qb.table()
	videoFileTable := videoFileTableMgr.table

	// SceneRangeDurationSQL rather than video_files.duration: summing the FILE's length
	// double-counts a file that has been split into scenes, since each of its scenes
	// contributes the whole file. Proved by
	// TestTheLibraryDurationDoesNotDoubleCountASplitFile.
	//
	// The SUM is raw SQL too, because the fragment is raw: goqu cannot parse a CASE
	// expression out of a string it is handed, and would quote it into something invalid.
	q := dialect.Select(
		goqu.L("COALESCE(SUM("+SceneRangeDurationSQL+"), 0)"),
	).From(table).InnerJoin(
		scenesFilesJoinTable,
		goqu.On(scenesFilesJoinTable.Col("scene_id").Eq(table.Col(idColumn))),
	).InnerJoin(
		videoFileTable,
		goqu.On(videoFileTable.Col("file_id").Eq(scenesFilesJoinTable.Col("file_id"))),
	)

	var ret float64
	if err := querySimple(ctx, q, &ret); err != nil {
		return 0, err
	}

	return ret, nil
}

func (qb *SceneStore) PlayDuration(ctx context.Context) (float64, error) {
	table := qb.table()

	q := dialect.Select(goqu.COALESCE(goqu.SUM("play_duration"), 0)).From(table)

	var ret float64
	if err := querySimple(ctx, q, &ret); err != nil {
		return 0, err
	}

	return ret, nil
}

// TODO - currently only used by unit test
func (qb *SceneStore) CountByStudioID(ctx context.Context, studioID int) (int, error) {
	table := qb.table()

	q := dialect.Select(goqu.COUNT("*")).From(table).Where(table.Col(studioIDColumn).Eq(studioID))
	return count(ctx, q)
}

func (qb *SceneStore) countMissingFingerprints(ctx context.Context, fpType string) (int, error) {
	fpTable := fingerprintTableMgr.table.As("fingerprints_temp")

	q := dialect.From(scenesFilesJoinTable).LeftJoin(
		fpTable,
		goqu.On(
			scenesFilesJoinTable.Col(fileIDColumn).Eq(fpTable.Col(fileIDColumn)),
			fpTable.Col("type").Eq(fpType),
		),
	).Select(goqu.COUNT(goqu.DISTINCT(scenesFilesJoinTable.Col(sceneIDColumn)))).Where(fpTable.Col("fingerprint").IsNull())

	return count(ctx, q)
}

// CountMissingChecksum returns the number of scenes missing a checksum value.
func (qb *SceneStore) CountMissingChecksum(ctx context.Context) (int, error) {
	return qb.countMissingFingerprints(ctx, "md5")
}

// CountMissingOSHash returns the number of scenes missing an oshash value.
func (qb *SceneStore) CountMissingOSHash(ctx context.Context) (int, error) {
	return qb.countMissingFingerprints(ctx, "oshash")
}

func (qb *SceneStore) Wall(ctx context.Context, q *string) ([]*models.Scene, error) {
	s := ""
	if q != nil {
		s = *q
	}

	table := qb.table()
	qq := qb.selectDataset().Prepared(true).Where(table.Col("details").Like("%" + s + "%")).Order(goqu.L("RANDOM()").Asc()).Limit(80)
	return qb.getMany(ctx, qq)
}

func (qb *SceneStore) All(ctx context.Context) ([]*models.Scene, error) {
	table := qb.table()
	fileTable := fileTableMgr.table
	folderTable := folderTableMgr.table

	return qb.getMany(ctx, qb.selectDataset().Order(
		folderTable.Col("path").Asc(),
		fileTable.Col("basename").Asc(),
		table.Col("date").Asc(),
	))
}

func (qb *SceneStore) makeQuery(ctx context.Context, sceneFilter *models.SceneFilterType, findFilter *models.FindFilterType) (*queryBuilder, error) {
	if sceneFilter == nil {
		sceneFilter = &models.SceneFilterType{}
	}
	if findFilter == nil {
		findFilter = &models.FindFilterType{}
	}

	query := sceneRepository.newQuery()
	distinctIDs(&query, sceneTable)

	if q := findFilter.Q; q != nil && *q != "" {
		query.addJoins(
			join{
				table:    scenesFilesTable,
				onClause: "scenes_files.scene_id = scenes.id",
			},
			join{
				table:    fileTable,
				onClause: "scenes_files.file_id = files.id",
			},
			join{
				table:    folderTable,
				onClause: "files.parent_folder_id = folders.id",
			},
			join{
				table:    fingerprintTable,
				onClause: "files_fingerprints.file_id = scenes_files.file_id",
			},
			join{
				table:    sceneMarkerTable,
				onClause: "scene_markers.scene_id = scenes.id",
			},
		)

		filepathColumn := "folders.path || '" + string(filepath.Separator) + "' || files.basename"
		searchColumns := []string{"scenes.title", "scenes.details", filepathColumn, "files_fingerprints.fingerprint", "scene_markers.title"}
		query.parseQueryString(searchColumns, *q)
	}

	filter := filterBuilderFromHandler(ctx, &sceneFilterHandler{
		sceneFilter: sceneFilter,
	})

	if err := query.addFilter(filter); err != nil {
		return nil, err
	}

	if err := qb.setSceneSort(&query, findFilter); err != nil {
		return nil, err
	}
	query.sortAndPagination += getPagination(findFilter)

	return &query, nil
}

func (qb *SceneStore) Query(ctx context.Context, options models.SceneQueryOptions) (*models.SceneQueryResult, error) {
	query, err := qb.makeQuery(ctx, options.SceneFilter, options.FindFilter)
	if err != nil {
		return nil, err
	}

	result, err := qb.queryGroupedFields(ctx, options, *query)
	if err != nil {
		return nil, fmt.Errorf("error querying aggregate fields: %w", err)
	}

	idsResult, err := query.findIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("error finding IDs: %w", err)
	}

	result.IDs = idsResult
	return result, nil
}

func (qb *SceneStore) queryGroupedFields(ctx context.Context, options models.SceneQueryOptions, query queryBuilder) (*models.SceneQueryResult, error) {
	if !options.Count && !options.TotalDuration && !options.TotalSize {
		// nothing to do - return empty result
		return models.NewSceneQueryResult(qb), nil
	}

	aggregateQuery := sceneRepository.newQuery()

	if options.Count {
		aggregateQuery.addColumn("COUNT(DISTINCT temp.id) as total")
	}

	if options.TotalDuration {
		query.addJoins(
			join{
				table:    scenesFilesTable,
				onClause: "scenes_files.scene_id = scenes.id",
			},
			join{
				table:    videoFileTable,
				onClause: "scenes_files.file_id = video_files.file_id",
			},
		)
		// SceneRangeDurationSQL, not video_files.duration: this column is summed into
		// FindScenes.duration, and a file split into scenes would contribute its full
		// length once per scene. The resolver accumulates it per row
		// (`result.TotalDuration += f.Duration`, resolver_query_find_scene.go:112) and
		// reports it as "total duration", so the double count would be visible to users
		// as a library total that exceeds the sum of the cards on screen.
		// Proved by TestTheLibraryDurationDoesNotDoubleCountASplitFile.
		query.addColumn(SceneRangeDurationSQL + " as duration")
		aggregateQuery.addColumn("SUM(temp.duration) as duration")
	}

	if options.TotalSize {
		query.addJoins(
			join{
				table:    scenesFilesTable,
				onClause: "scenes_files.scene_id = scenes.id",
			},
			join{
				table:    fileTable,
				onClause: "scenes_files.file_id = files.id",
			},
		)
		query.addColumn("COALESCE(files.size, 0) as size")
		aggregateQuery.addColumn("SUM(temp.size) as size")
	}

	// #5503 - select the file id so equal-sized/duration files aren't collapsed by DISTINCT
	if options.TotalDuration || options.TotalSize {
		query.addColumn(scenesFilesTable + ".file_id")
	}

	const includeSortPagination = false
	aggregateQuery.from = fmt.Sprintf("(%s) as temp", query.toSQL(includeSortPagination))

	out := struct {
		Total    int
		Duration null.Float
		Size     null.Float
	}{}
	if err := sceneRepository.queryStruct(ctx, aggregateQuery.toSQL(includeSortPagination), query.allArgs(), &out); err != nil {
		return nil, err
	}

	ret := models.NewSceneQueryResult(qb)
	ret.Count = out.Total
	ret.TotalDuration = out.Duration.Float64
	ret.TotalSize = out.Size.Float64
	return ret, nil
}

func (qb *SceneStore) QueryCount(ctx context.Context, sceneFilter *models.SceneFilterType, findFilter *models.FindFilterType) (int, error) {
	query, err := qb.makeQuery(ctx, sceneFilter, findFilter)
	if err != nil {
		return 0, err
	}

	return query.executeCount(ctx)
}

var sceneSortOptions = sortOptions{
	"bitrate",
	"created_at",
	"code",
	"date",
	"production_date",
	"file_count",
	"filesize",
	"duration",
	"file_mod_time",
	"framerate",
	"group_scene_number",
	"id",
	"interactive",
	"interactive_speed",
	"last_o_at",
	"last_played_at",
	"movie_scene_number",
	"o_counter",
	"organized",
	"performer_count",
	"play_count",
	"play_duration",
	"resume_time",
	"path",
	"perceptual_similarity",
	"random",
	"rating",
	"resolution",
	"studio",
	"tag_count",
	"title",
	"updated_at",
	"performer_age",
}

func (qb *SceneStore) setSceneSort(query *queryBuilder, findFilter *models.FindFilterType) error {
	if findFilter == nil || findFilter.Sort == nil || *findFilter.Sort == "" {
		return nil
	}
	sort := findFilter.GetSort("title")

	// CVE-2024-32231 - ensure sort is in the list of allowed sorts
	if err := sceneSortOptions.validateSort(sort); err != nil {
		return err
	}

	addFileTable := func() {
		query.addJoins(
			join{
				sort:     true,
				table:    scenesFilesTable,
				onClause: "scenes_files.scene_id = scenes.id",
			},
			join{
				sort:     true,
				table:    fileTable,
				onClause: "scenes_files.file_id = files.id",
			},
		)
	}

	addVideoFileTable := func() {
		addFileTable()
		query.addJoins(
			join{
				sort:     true,
				table:    videoFileTable,
				onClause: "video_files.file_id = scenes_files.file_id",
			},
		)
	}

	addFolderTable := func() {
		query.addJoins(
			join{
				sort:     true,
				table:    folderTable,
				onClause: "files.parent_folder_id = folders.id",
			},
		)
	}

	direction := findFilter.GetDirection()
	switch sort {
	case "movie_scene_number":
		query.joinSort(groupsScenesTable, "", "scenes.id = groups_scenes.scene_id")
		query.sortAndPagination += getSort("scene_index", direction, groupsScenesTable)
	case "group_scene_number":
		query.joinSort(groupsScenesTable, "scene_group", "scenes.id = scene_group.scene_id")
		query.sortAndPagination += getSort("scene_index", direction, "scene_group")
	case "tag_count":
		query.sortAndPagination += getCountSort(sceneTable, scenesTagsTable, sceneIDColumn, direction)
	case "performer_count":
		query.sortAndPagination += getCountSort(sceneTable, performersScenesTable, sceneIDColumn, direction)
	case "file_count":
		query.sortAndPagination += getCountSort(sceneTable, scenesFilesTable, sceneIDColumn, direction)
	case "path":
		// special handling for path
		addFileTable()
		addFolderTable()
		query.sortAndPagination += fmt.Sprintf(" ORDER BY COALESCE(folders.path, '') || COALESCE(files.basename, '') COLLATE NATURAL_CI %s", direction)
	case "perceptual_similarity":
		// special handling for phash
		addFileTable()
		query.addJoins(
			join{
				sort:     true,
				table:    fingerprintTable,
				as:       "fingerprints_phash",
				onClause: "scenes_files.file_id = fingerprints_phash.file_id AND fingerprints_phash.type = 'phash'",
			},
		)

		query.sortAndPagination += " ORDER BY fingerprints_phash.fingerprint " + direction + ", files.size DESC"
	case "bitrate":
		sort = "bit_rate"
		addVideoFileTable()
		query.sortAndPagination += getSort(sort, direction, videoFileTable)
	case "file_mod_time":
		sort = "mod_time"
		addFileTable()
		query.sortAndPagination += getSort(sort, direction, fileTable)
	case "framerate":
		sort = "frame_rate"
		addVideoFileTable()
		query.sortAndPagination += getSort(sort, direction, videoFileTable)
	case "resolution":
		addVideoFileTable()
		query.sortAndPagination += fmt.Sprintf(" ORDER BY MIN(%s.width, %s.height) %s", videoFileTable, videoFileTable, getSortDirection(direction))
	case "filesize":
		addFileTable()
		query.sortAndPagination += getSort(sort, direction, fileTable)
	case "duration":
		addVideoFileTable()
		query.sortAndPagination += getSort(sort, direction, videoFileTable)
	case "interactive", "interactive_speed":
		addVideoFileTable()
		query.sortAndPagination += getSort(sort, direction, videoFileTable)
	case "title":
		addFileTable()
		addFolderTable()
		query.sortAndPagination += " ORDER BY COALESCE(scenes.title, files.basename) COLLATE NATURAL_CI " + direction + ", folders.path COLLATE NATURAL_CI " + direction
	case "play_count":
		query.sortAndPagination += getCountSort(sceneTable, scenesViewDatesTable, sceneIDColumn, direction)
	case "last_played_at":
		query.sortAndPagination += fmt.Sprintf(" ORDER BY (SELECT MAX(view_date) FROM %s AS sort WHERE sort.%s = %s.id) %s", scenesViewDatesTable, sceneIDColumn, sceneTable, getSortDirection(direction))
	case "last_o_at":
		query.sortAndPagination += fmt.Sprintf(" ORDER BY (SELECT MAX(o_date) FROM %s AS sort WHERE sort.%s = %s.id) %s", scenesODatesTable, sceneIDColumn, sceneTable, getSortDirection(direction))
	case "o_counter":
		query.sortAndPagination += getCountSort(sceneTable, scenesODatesTable, sceneIDColumn, direction)
	case "performer_age":
		// Looking at the youngest performer by default
		aggregation := "MIN"
		if direction == "DESC" {
			// When sorting by performer_'s age DESC, I should consider the oldest performer instead
			aggregation = "MAX"
		}
		fallback := "NULL"
		if direction == "ASC" {
			// When sorting ascending, NULLs are first by default. Coalescing to the MAX int value supported by sqlite
			fallback = "9223372036854775807"
		}
		query.sortAndPagination += fmt.Sprintf(
			" ORDER BY (SELECT COALESCE(%s(JulianDay(%s) - JulianDay(performers.birthdate)), %s) FROM %s as performers INNER JOIN %s AS aggregation WHERE performers.id = aggregation.%s AND aggregation.%s = %s.id) %s",
			aggregation,
			sceneAgeDateExpr,
			fallback,
			performerTable,
			performersScenesTable,
			performerIDColumn,
			sceneIDColumn,
			sceneTable,
			getSortDirection(direction),
		)
	case "studio":
		query.joinSort(studioTable, "", "scenes.studio_id = studios.id")
		query.sortAndPagination += getSort("name", direction, studioTable)
	default:
		query.sortAndPagination += getSort(sort, direction, "scenes")
	}

	// Whatever the sorting, always use title/id as a final sort
	query.sortAndPagination += ", COALESCE(scenes.title, scenes.id) COLLATE NATURAL_CI ASC"

	return nil
}

func (qb *SceneStore) SaveActivity(ctx context.Context, id int, resumeTime *float64, playDuration *float64) (bool, error) {
	if err := qb.tableMgr.checkIDExists(ctx, id); err != nil {
		return false, err
	}

	record := goqu.Record{}

	if resumeTime != nil {
		record["resume_time"] = resumeTime
	}

	if playDuration != nil {
		record["play_duration"] = goqu.L("play_duration + ?", playDuration)
	}

	if len(record) > 0 {
		if err := qb.tableMgr.updateByID(ctx, id, record); err != nil {
			return false, err
		}
	}

	return true, nil
}

func (qb *SceneStore) ResetActivity(ctx context.Context, id int, resetResume bool, resetDuration bool) (bool, error) {
	if err := qb.tableMgr.checkIDExists(ctx, id); err != nil {
		return false, err
	}

	record := goqu.Record{}

	if resetResume {
		record["resume_time"] = 0.0
	}

	if resetDuration {
		record["play_duration"] = 0.0
	}

	if len(record) > 0 {
		if err := qb.tableMgr.updateByID(ctx, id, record); err != nil {
			return false, err
		}
	}

	return true, nil
}

func (qb *SceneStore) GetURLs(ctx context.Context, sceneID int) ([]string, error) {
	return scenesURLsTableMgr.get(ctx, sceneID)
}

func (qb *SceneStore) GetCover(ctx context.Context, sceneID int) ([]byte, error) {
	return qb.GetImage(ctx, sceneID, sceneCoverBlobColumn)
}

func (qb *SceneStore) HasCover(ctx context.Context, sceneID int) (bool, error) {
	return qb.HasImage(ctx, sceneID, sceneCoverBlobColumn)
}

func (qb *SceneStore) UpdateCover(ctx context.Context, sceneID int, image []byte) error {
	return qb.UpdateImage(ctx, sceneID, sceneCoverBlobColumn, image)
}

func (qb *SceneStore) destroyCover(ctx context.Context, sceneID int) error {
	return qb.DestroyImage(ctx, sceneID, sceneCoverBlobColumn)
}

func (qb *SceneStore) AssignFiles(ctx context.Context, sceneID int, fileIDs []models.FileID) error {
	// assuming a file can only be assigned to a single scene
	if err := scenesFilesTableMgr.destroyJoins(ctx, fileIDs); err != nil {
		return err
	}

	// assign primary only if destination has no files
	existingFileIDs, err := sceneRepository.files.get(ctx, sceneID)
	if err != nil {
		return err
	}

	firstPrimary := len(existingFileIDs) == 0
	return scenesFilesTableMgr.insertJoins(ctx, sceneID, firstPrimary, fileIDs)
}

func (qb *SceneStore) GetGroups(ctx context.Context, id int) (ret []models.GroupsScenes, err error) {
	ret = []models.GroupsScenes{}

	if err := sceneRepository.groups.getAll(ctx, id, func(rows *sqlx.Rows) error {
		var ms groupsScenesRow
		if err := rows.StructScan(&ms); err != nil {
			return err
		}

		ret = append(ret, ms.resolve(id))
		return nil
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (qb *SceneStore) AddFileID(ctx context.Context, id int, fileID models.FileID) error {
	const firstPrimary = false
	return scenesFilesTableMgr.insertJoins(ctx, id, firstPrimary, []models.FileID{fileID})
}

func (qb *SceneStore) GetPerformerIDs(ctx context.Context, id int) ([]int, error) {
	return sceneRepository.performers.getIDs(ctx, id)
}

func (qb *SceneStore) GetTagIDs(ctx context.Context, id int) ([]int, error) {
	return sceneRepository.tags.getIDs(ctx, id)
}

func (qb *SceneStore) GetGalleryIDs(ctx context.Context, id int) ([]int, error) {
	return sceneRepository.galleries.getIDs(ctx, id)
}

func (qb *SceneStore) AddGalleryIDs(ctx context.Context, sceneID int, galleryIDs []int) error {
	return scenesGalleriesTableMgr.addJoins(ctx, sceneID, galleryIDs)
}

func (qb *SceneStore) GetStashIDs(ctx context.Context, sceneID int) ([]models.StashID, error) {
	return sceneRepository.stashIDs.get(ctx, sceneID)
}

func (qb *SceneStore) FindDuplicates(ctx context.Context, distance int, durationDiff float64, filter *models.SceneFilterType) ([][]*models.Scene, error) {
	var dupeIds [][]int

	query, err := qb.makeQuery(ctx, filter, nil)
	if err != nil {
		return nil, err
	}

	// Add necessary joins for duplicate checking
	query.addJoins(
		join{
			table:    scenesFilesTable,
			onClause: "scenes.id = scenes_files.scene_id",
		},
		join{
			table:    fileTable,
			onClause: "scenes_files.file_id = files.id",
		},
		join{
			table:    fingerprintTable,
			onClause: "scenes_files.file_id = files_fingerprints.file_id AND files_fingerprints.type = 'phash'",
		},
		join{
			table:    videoFileTable,
			onClause: "files.id = video_files.file_id",
		},
	)

	if distance == 0 {
		query.columns = []string{
			"scenes.id as scene_id",
			"scenes_files.file_id as file_id",
			"video_files.duration as file_duration",
			"files.size as file_size",
			"files_fingerprints.fingerprint as phash",
			"abs(max(video_files.duration) OVER (PARTITION by files_fingerprints.fingerprint) - video_files.duration) as durationDiff",
		}

		sqlStr := query.toSQL(false)

		// The phash belongs to the FILE, so every scene over one file has an IDENTICAL hash
		// and COUNT(DISTINCT scene_id) > 1 is already satisfied by two segments of the same
		// video. Splitting a file into scenes therefore made each scene a duplicate of every
		// other one, and they all appeared in the duplicate checker and its cleanup tooling.
		// Measured before the fix: two scenes over one 1800s file produced the group [33 34].
		//
		// Three fixes were tried, and only the third works. The two that fail are recorded
		// because each fails in a way the obvious test does not catch:
		//
		//   1. HAVING COUNT(DISTINCT file_id) > 1 added to the existing GROUP BY phash.
		//      Insufficient: with three segments of one file plus an untouched COPY of it,
		//      the group is [33 34 35 36] -- two distinct file_ids, so the HAVING passes and
		//      the three same-file segments come along anyway. GROUP_CONCAT concatenates the
		//      rows a group holds and cannot split a group by file.
		//
		//   2. GROUP BY phash, file_id (dropping the HAVING).
		//      Insufficient in the OTHER direction: the UI treats each returned group as an
		//      independent duplicate set (SceneDuplicateChecker), so a copy pair is split into
		//      two singletons and COUNT(phash) > 1 then discards BOTH. Measured: zero groups.
		//
		//   3. This one: GROUP BY phash is kept, and a WHERE clause gates on the phash
		//      occurring under more than one file_id.
		//
		//          a copy pair      one phash, two file_ids  -> kept
		//          a split file     one phash, one file_id   -> dropped
		//
		//      Both requirements hold because they constrain DIFFERENT things: the grouping
		//      keeps a copy pair together, and the gate drops a phash that is unique to one
		//      file. file_id is selected only so the gate can count it.
		//
		// A KNOWN LIMITATION, pinned by TestASplitFileAndACopyOfItInTheSameLibrary: a split
		// file whose phash ALSO occurs on another file is reported as ONE group containing
		// the segments and that other file together. The segments are not duplicates OF EACH
		// OTHER -- they are duplicates OF the other file -- but [][]*Scene cannot express a
		// per-pair relation, and returning nothing would hide a real duplicate. Showing four
		// scenes and asking which to delete is the decision the user actually has to make.
		//
		// Written as WHERE + GROUP BY rather than an INTERSECT of two subqueries: an INTERSECT
		// requires both arms to return the same columns, cannot see scene_id or file_size past
		// the set operation, and must be passed the query arguments TWICE. The measured failure
		// of that version was "no such column: scene_id".
		//
		// NOTE ON STYLE: this SQL is a raw backtick literal, so no backtick may appear inside
		// it -- including in these comments. Backticked identifiers here terminate the string
		// mid-token and Go reports a syntax error far from the cause.
		finalQuery := `
SELECT GROUP_CONCAT(DISTINCT scene_id) as ids
FROM (` + sqlStr + `)
WHERE phash IS NOT NULL
    AND (durationDiff <= ?
    OR ? < 0)  -- Always TRUE if the parameter is negative.
               -- That will disable the durationDiff checking.
    AND phash IN (
		SELECT phash FROM (` + sqlStr + `)
		WHERE phash IS NOT NULL
		GROUP BY phash
		HAVING COUNT(DISTINCT file_id) > 1
	)
GROUP BY phash
HAVING COUNT(phash) > 1
	AND COUNT(DISTINCT scene_id) > 1
ORDER BY SUM(file_size) DESC;
`

		var ids []string
		// `sqlStr` is embedded TWICE (once per INTERSECT arm), so its arguments must be bound
		// TWICE as well, and the two durationDiff placeholders once per arm. Getting this
		// wrong is an "argument count" error at execution time rather than a compile error,
		// because Go's variadic `...` happily accepts any slice.
		args := append(query.allArgs(), durationDiff, durationDiff)
		args = append(args, query.allArgs()...)
		args = append(args, durationDiff, durationDiff)
		if err := dbWrapper.Select(ctx, &ids, finalQuery, args...); err != nil {
			return nil, err
		}

		for _, id := range ids {
			strIds := strings.Split(id, ",")
			var sceneIds []int
			for _, strId := range strIds {
				if intId, err := strconv.Atoi(strId); err == nil {
					sceneIds = sliceutil.AppendUnique(sceneIds, intId)
				}
			}
			// filter out
			if len(sceneIds) > 1 {
				dupeIds = append(dupeIds, sceneIds)
			}
		}
	} else {
		query.columns = []string{
			"scenes.id as id",
			// scenes_files.file_id so utils.FindDuplicates can tell two scenes over ONE
			// file from two scenes over two identical files. Without it every segment of a
			// split file has distance 0 to its siblings and is reported as a duplicate.
			"scenes_files.file_id as file_id",
			"files_fingerprints.fingerprint as phash",
			"video_files.duration as duration",
		}
		query.addWhere("files_fingerprints.fingerprint IS NOT NULL")
		query.sortAndPagination = " ORDER BY files.size DESC"

		sqlStr := query.toSQL(true)

		var hashes []*utils.Phash

		if err := sceneRepository.queryFunc(ctx, sqlStr, query.allArgs(), false, func(rows *sqlx.Rows) error {
			phash := utils.Phash{
				Bucket:   -1,
				Duration: -1,
			}
			if err := rows.StructScan(&phash); err != nil {
				return err
			}

			hashes = append(hashes, &phash)
			return nil
		}); err != nil {
			return nil, err
		}

		dupeIds = utils.FindDuplicates(hashes, distance, durationDiff)
	}

	var duplicates [][]*models.Scene
	for _, sceneIds := range dupeIds {
		if scenes, err := qb.FindMany(ctx, sceneIds); err == nil {
			duplicates = append(duplicates, scenes)
		}
	}

	sortByPath(duplicates)

	return duplicates, nil
}

func sortByPath(scenes [][]*models.Scene) {
	lessFunc := func(i int, j int) bool {
		firstPathI := getFirstPath(scenes[i])
		firstPathJ := getFirstPath(scenes[j])
		return firstPathI < firstPathJ
	}
	sort.SliceStable(scenes, lessFunc)
}

func getFirstPath(scenes []*models.Scene) string {
	var firstPath string
	for i, scene := range scenes {
		if i == 0 || scene.Path < firstPath {
			firstPath = scene.Path
		}
	}
	return firstPath
}
