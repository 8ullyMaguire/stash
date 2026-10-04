package models

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"
)

// Scene stores the metadata for a single video scene.
type Scene struct {
	ID             int    `json:"id"`
	Title          string `json:"title"`
	Code           string `json:"code"`
	Details        string `json:"details"`
	Director       string `json:"director"`
	Date           *Date  `json:"date"`
	ProductionDate *Date  `json:"production_date"`
	// Rating expressed in 1-100 scale
	Rating    *int `json:"rating"`
	Organized bool `json:"organized"`
	StudioID  *int `json:"studio_id"`

	// transient - not persisted
	Files         RelatedVideoFiles
	PrimaryFileID *FileID
	// transient - path of primary file - empty if no files
	Path string
	// transient - oshash of primary file - empty if no files
	OSHash string
	// transient - checksum of primary file - empty if no files
	Checksum string

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	ResumeTime   float64 `json:"resume_time"`
	PlayDuration float64 `json:"play_duration"`

	URLs RelatedStrings `json:"urls"`
	// Directors is the STRUCTURED form of the packed `Director` string above, which stays.
	// stash#2359 (#3051). One director per row rather than a split string, because a scene with two
	// directors cannot be filtered by director without a LIKE on a packed column, and "Ana Lopez"
	// and "Ana López" then differ only across a comma that is part of neither name.
	Directors    RelatedStrings  `json:"directors"`
	GalleryIDs   RelatedIDs      `json:"gallery_ids"`
	TagIDs       RelatedIDs      `json:"tag_ids"`
	PerformerIDs RelatedIDs      `json:"performer_ids"`
	Groups       RelatedGroups   `json:"groups"`
	StashIDs     RelatedStashIDs `json:"stash_ids"`
}

func NewScene() Scene {
	currentTime := time.Now()
	return Scene{
		CreatedAt: currentTime,
		UpdatedAt: currentTime,
	}
}

type CreateSceneInput struct {
	*Scene

	FileIDs      []FileID
	CoverImage   []byte
	CustomFields CustomFieldMap `json:"custom_fields"`
}

type UpdateSceneInput struct {
	*Scene

	CustomFields CustomFieldsInput `json:"custom_fields"`
}

// ScenePartial represents part of a Scene object. It is used to update
// the database entry.
type ScenePartial struct {
	Title    OptionalString
	Code     OptionalString
	Details  OptionalString
	Director OptionalString
	// Directors is *UpdateStrings so absent means "do not touch" and present-but-empty means
	// "clear". stash#2359.
	Directors      *UpdateStrings
	Date           OptionalDate
	ProductionDate OptionalDate
	// Rating expressed in 1-100 scale
	Rating       OptionalInt
	Organized    OptionalBool
	StudioID     OptionalInt
	CreatedAt    OptionalTime
	UpdatedAt    OptionalTime
	ResumeTime   OptionalFloat64
	PlayDuration OptionalFloat64

	URLs          *UpdateStrings
	GalleryIDs    *UpdateIDs
	TagIDs        *UpdateIDs
	PerformerIDs  *UpdateIDs
	GroupIDs      *UpdateGroupIDs
	StashIDs      *UpdateStashIDs
	PrimaryFileID *FileID
}

func NewScenePartial() ScenePartial {
	currentTime := time.Now()
	return ScenePartial{
		UpdatedAt: NewOptionalTime(currentTime),
	}
}

func (s *Scene) LoadURLs(ctx context.Context, l URLLoader) error {
	return s.URLs.load(func() ([]string, error) {
		return l.GetURLs(ctx, s.ID)
	})
}

func (s *Scene) LoadFiles(ctx context.Context, l VideoFileLoader) error {
	return s.Files.load(func() ([]*VideoFile, error) {
		return l.GetFiles(ctx, s.ID)
	})
}

func (s *Scene) LoadPrimaryFile(ctx context.Context, l FileGetter) error {
	return s.Files.loadPrimary(func() (*VideoFile, error) {
		if s.PrimaryFileID == nil {
			return nil, nil
		}

		f, err := l.Find(ctx, *s.PrimaryFileID)
		if err != nil {
			return nil, err
		}

		var vf *VideoFile
		if len(f) > 0 {
			var ok bool
			vf, ok = f[0].(*VideoFile)
			if !ok {
				return nil, errors.New("not a video file")
			}
		}
		return vf, nil
	})
}

// LoadPrimaryFileWithWindow is LoadPrimaryFile for a caller that needs the SCENE's view of its
// primary file rather than the file's.
//
// #3530. LoadPrimaryFile goes through FileStore.Find, which looks the file up by id and does not
// join `scenes_files` -- so it cannot report a window even when the row has one. MEASURED, not
// assumed: pkg/sqlite/scene_window_loader_test.go drives both loaders over one ranged scene and
// finds LoadPrimaryFile reporting the file's own duration and a nil StartTime where GetFiles
// reports the clamped window.
//
// So a task that reads the window MUST NOT use LoadPrimaryFile, and the difference is invisible from
// the model side: both return a *VideoFile with the same Path, and a windowed scene comes back
// looking exactly like an unranged one. That is the shape of the bug #3530's sprite work found --
// every arithmetic test green, and a sprite of the wrong footage.
//
// This loader is therefore separate rather than a flag: LoadPrimaryFile's answer stays "what is this
// file", which is the right answer for a caller that has no scene scope, and this one is "what is
// this scene's primary file", which is the right answer for a generator. It is the same separation
// the preview key draws between GetHash and GeneratedChecksum.
func (s *Scene) LoadPrimaryFileWithWindow(ctx context.Context, l ScenePrimaryFileLoader) error {
	if s.PrimaryFileID == nil {
		// Match LoadPrimaryFile: no primary file loads as "loaded, and there is none". Calling
		// loadPrimary here rather than returning early keeps `primaryLoaded` true, so a later
		// Files.Primary() does not panic on an unloaded relationship.
		return s.Files.loadPrimary(func() (*VideoFile, error) { return nil, nil })
	}

	return s.Files.loadPrimary(func() (*VideoFile, error) {
		return l.GetPrimaryFile(ctx, s.ID, *s.PrimaryFileID)
	})
}

// ScenePrimaryFileLoader is what LoadPrimaryFileWithWindow needs: a store that can apply a scene's
// window to the scene's primary file.
//
// It is part of SceneReader rather than a loose interface argument for two reasons: the store
// satisfying it becomes a compile-time fact rather than a runtime assertion, and adding it breaks
// every mock of SceneReader LOUDLY -- which is what should happen when a reader grows a method,
// rather than a mock quietly satisfying it and a test then asserting against a stub that returns
// nothing.
type ScenePrimaryFileLoader interface {
	GetPrimaryFile(ctx context.Context, sceneID int, primaryFileID FileID) (*VideoFile, error)
}

func (s *Scene) LoadGalleryIDs(ctx context.Context, l GalleryIDLoader) error {
	return s.GalleryIDs.load(func() ([]int, error) {
		return l.GetGalleryIDs(ctx, s.ID)
	})
}

func (s *Scene) LoadPerformerIDs(ctx context.Context, l PerformerIDLoader) error {
	return s.PerformerIDs.load(func() ([]int, error) {
		return l.GetPerformerIDs(ctx, s.ID)
	})
}

func (s *Scene) LoadTagIDs(ctx context.Context, l TagIDLoader) error {
	return s.TagIDs.load(func() ([]int, error) {
		return l.GetTagIDs(ctx, s.ID)
	})
}

func (s *Scene) LoadGroups(ctx context.Context, l SceneGroupLoader) error {
	return s.Groups.load(func() ([]GroupsScenes, error) {
		return l.GetGroups(ctx, s.ID)
	})
}

func (s *Scene) LoadStashIDs(ctx context.Context, l StashIDLoader) error {
	return s.StashIDs.load(func() ([]StashID, error) {
		return l.GetStashIDs(ctx, s.ID)
	})
}

func (s *Scene) LoadRelationships(ctx context.Context, l SceneReader) error {
	if err := s.LoadURLs(ctx, l); err != nil {
		return err
	}

	if err := s.LoadGalleryIDs(ctx, l); err != nil {
		return err
	}

	if err := s.LoadPerformerIDs(ctx, l); err != nil {
		return err
	}

	if err := s.LoadTagIDs(ctx, l); err != nil {
		return err
	}

	if err := s.LoadGroups(ctx, l); err != nil {
		return err
	}

	if err := s.LoadStashIDs(ctx, l); err != nil {
		return err
	}

	if err := s.LoadFiles(ctx, l); err != nil {
		return err
	}

	return nil
}

// UpdateInput constructs a SceneUpdateInput using the populated fields in the ScenePartial object.
func (s ScenePartial) UpdateInput(id int) SceneUpdateInput {
	var dateStr *string
	if s.Date.Set {
		d := s.Date.Value
		v := d.String()
		dateStr = &v
	}

	var productionDateStr *string
	if s.ProductionDate.Set {
		d := s.ProductionDate.Value
		v := d.String()
		productionDateStr = &v
	}

	var stashIDs StashIDs
	if s.StashIDs != nil {
		stashIDs = StashIDs(s.StashIDs.StashIDs)
	}

	ret := SceneUpdateInput{
		ID:             strconv.Itoa(id),
		Title:          s.Title.Ptr(),
		Code:           s.Code.Ptr(),
		Details:        s.Details.Ptr(),
		Director:       s.Director.Ptr(),
		Urls:           s.URLs.Strings(),
		Date:           dateStr,
		ProductionDate: productionDateStr,
		Rating100:      s.Rating.Ptr(),
		Organized:      s.Organized.Ptr(),
		StudioID:       s.StudioID.StringPtr(),
		GalleryIds:     s.GalleryIDs.IDStrings(),
		PerformerIds:   s.PerformerIDs.IDStrings(),
		Movies:         s.GroupIDs.SceneMovieInputs(),
		TagIds:         s.TagIDs.IDStrings(),
		StashIds:       stashIDs.ToStashIDInputs(),
	}

	return ret
}

// GetTitle returns the title of the scene. If the Title field is empty,
// then the base filename is returned.
func (s Scene) GetTitle() string {
	if s.Title != "" {
		return s.Title
	}

	return filepath.Base(s.Path)
}

// DisplayName returns a display name for the scene for logging purposes.
// It returns Path if not empty, otherwise it returns the ID.
func (s Scene) DisplayName() string {
	if s.Path != "" {
		return s.Path
	}

	return strconv.Itoa(s.ID)
}

// #3530 - the cache key for a scene's WINDOW-AWARE generated artefacts.
//
// GetHash answers "what file is this", and its answer must NOT depend on which window of that file
// you are looking at -- so GetHash itself is left alone, deliberately. It has ~12 callers spanning
// preview, sprite, VTT thumbs, funscript, export and scene markers, and folding a window into it
// would rename generated files for export and marker paths and change URLs that are already
// bookmarked.
//
// GeneratedChecksum is the opt-in variant, for the artefacts that ARE window-aware. It returns
// GetHash() unchanged for an unranged scene -- which is the whole reason it is a separate function:
// every scene in every existing installation is unranged, so nothing moves.
//
//	<checksum>_w<start>-<end>        e.g. d3adb33f_w60.000-300.000
//
// ## Why the suffix is on the CHECKSUM and not on the filename
//
// GetVideoPreviewPath is shardedJoin(Screenshots, checksum, checksum+".mp4"), and shardedJoin
// derives the shard directory from the checksum. Appending to the filename would give two scenes of
// one file different names inside ONE shared shard directory -- which fixes the collision but
// leaves both scenes' files interleaved and defeats the sharding that keeps directories small.
//
// ## Why both ends
//
// A key naming only the start would collide for every window beginning at the same offset, which is
// exactly what splitting a file produces. Three decimals because that is the precision used
// elsewhere in #3530's seeking, and a preview regenerated at 60.0001 vs 60.0002 must be the same
// key.
//
// ## An open-ended window
//
// Rendered with only its start. Its missing end is NOT written as 0, because that would collide
// with a degenerate zero-length window -- and, more usefully, because "runs to the end of the file"
// is a different fact from "ends at 0".
//
// ## Which artefacts
//
// Preview, webp AND sprite/VTT thumbs, because those are the ones made window-aware: previews in
// 8f84c565f, sprite + VTT in af5ea1a83. The sprite routes and the sprite task both key on this,
// and they must -- serving a windowed scene's sprite by the plain hash serves the *unwindowed*
// sprite of another scene, at a URL that looks entirely correct.
//
// **Export deliberately keeps the plain hash**, and it is the one remaining exception. It is not
// an oversight and not yet done: export writes a rendered file whose name a user may have recorded
// elsewhere, and suffixing it would rename an artefact whose CONTENT is unchanged for any scene
// whose window covers the whole file. That is a separate decision with its own migration question,
// so it stays out until someone makes it deliberately.
func GeneratedChecksum(s Scene, hashAlgorithm HashAlgorithm) string {
	base := s.GetHash(hashAlgorithm)
	if base == "" {
		return ""
	}

	start, end, ok := s.GeneratedWindow()
	if !ok {
		return base
	}
	if end <= 0 {
		return fmt.Sprintf("%s_w%.3f", base, start)
	}
	return fmt.Sprintf("%s_w%.3f-%.3f", base, start, end)
}

// GeneratedWindow returns the primary file's window, and whether there is one.
//
// The window lives on models.VideoFile as StartTime/EndTime (there is no per-scene-file type in the
// runtime model), and it is present only when GetFiles has run on this scene -- so a caller that has
// not loaded the files gets "no window", which is the safe answer: it yields the plain hash rather
// than a suffix on absent data.
func (s Scene) GeneratedWindow() (start, end float64, ok bool) {
	if !s.Files.PrimaryLoaded() {
		return 0, 0, false
	}
	pf := s.Files.Primary()
	if pf == nil || (pf.StartTime == nil && pf.EndTime == nil) {
		return 0, 0, false
	}
	if pf.StartTime != nil {
		start = *pf.StartTime
	}
	if pf.EndTime != nil {
		end = *pf.EndTime
	}
	return start, end, true
}

// GetHash returns the hash of the scene, based on the hash algorithm provided. If
// hash algorithm is MD5, then Checksum is returned. Otherwise, OSHash is returned.
func (s Scene) GetHash(hashAlgorithm HashAlgorithm) string {
	switch hashAlgorithm {
	case HashAlgorithmMd5:
		return s.Checksum
	case HashAlgorithmOshash:
		return s.OSHash
	}

	return ""
}

// SceneFileType represents the file metadata for a scene.
type SceneFileType struct {
	Size       *string  `graphql:"size" json:"size"`
	Duration   *float64 `graphql:"duration" json:"duration"`
	VideoCodec *string  `graphql:"video_codec" json:"video_codec"`
	AudioCodec *string  `graphql:"audio_codec" json:"audio_codec"`
	Width      *int     `graphql:"width" json:"width"`
	Height     *int     `graphql:"height" json:"height"`
	Framerate  *float64 `graphql:"framerate" json:"framerate"`
	Bitrate    *int     `graphql:"bitrate" json:"bitrate"`
}

type VideoCaption struct {
	LanguageCode string `json:"language_code"`
	Filename     string `json:"filename"`
	CaptionType  string `json:"caption_type"`
}

func (c VideoCaption) Path(filePath string) string {
	return filepath.Join(filepath.Dir(filePath), c.Filename)
}
