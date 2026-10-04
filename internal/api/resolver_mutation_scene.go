package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/pkg/file"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/plugin"
	"github.com/stashapp/stash/pkg/plugin/hook"
	"github.com/stashapp/stash/pkg/scene"
	"github.com/stashapp/stash/pkg/sliceutil"
	"github.com/stashapp/stash/pkg/sliceutil/stringslice"
	"github.com/stashapp/stash/pkg/utils"
)

// used to refetch scene after hooks run
func (r *mutationResolver) getScene(ctx context.Context, id int) (ret *models.Scene, err error) {
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.Scene.Find(ctx, id)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *mutationResolver) SceneCreate(ctx context.Context, input models.SceneCreateInput) (ret *models.Scene, err error) {
	translator := changesetTranslator{
		inputMap: getUpdateInputMap(ctx),
	}

	fileIDs, err := translator.fileIDSliceFromStringSlice(input.FileIds)
	if err != nil {
		return nil, fmt.Errorf("converting file ids: %w", err)
	}

	// Populate a new scene from the input
	newScene := models.NewScene()

	newScene.Title = translator.string(input.Title)
	newScene.Code = translator.string(input.Code)
	newScene.Details = translator.string(input.Details)
	newScene.Director = translator.string(input.Director)
	// stash#2359 (#3051). The structured form, beside the packed string rather than replacing it.
	// UniqueFOLD because a director is a NAME, and two directors differing only in case are one
	// person -- which also means "Ana Lapez" and "Ana Lopez" collide, and that is the intended
	// behaviour: they are the same director with a typo in one of them.
	newScene.Directors = models.NewRelatedStrings(stringslice.UniqueFold(stringslice.TrimSpace(input.Directors)))
	newScene.Rating = input.Rating100
	newScene.Organized = translator.bool(input.Organized)
	newScene.StashIDs = models.NewRelatedStashIDs(models.StashIDInputs(input.StashIds).ToStashIDs())

	newScene.Date, err = translator.datePtr(input.Date)
	if err != nil {
		return nil, fmt.Errorf("converting date: %w", err)
	}
	newScene.ProductionDate, err = translator.datePtr(input.ProductionDate)
	if err != nil {
		return nil, fmt.Errorf("converting production date: %w", err)
	}
	newScene.StudioID, err = translator.intPtrFromString(input.StudioID)
	if err != nil {
		return nil, fmt.Errorf("converting studio id: %w", err)
	}

	if input.Urls != nil {
		newScene.URLs = models.NewRelatedStrings(stringslice.TrimSpace(input.Urls))
	} else if input.URL != nil {
		newScene.URLs = models.NewRelatedStrings([]string{strings.TrimSpace(*input.URL)})
	}

	newScene.PerformerIDs, err = translator.relatedIds(input.PerformerIds)
	if err != nil {
		return nil, fmt.Errorf("converting performer ids: %w", err)
	}
	newScene.TagIDs, err = translator.relatedIds(input.TagIds)
	if err != nil {
		return nil, fmt.Errorf("converting tag ids: %w", err)
	}
	newScene.GalleryIDs, err = translator.relatedIds(input.GalleryIds)
	if err != nil {
		return nil, fmt.Errorf("converting gallery ids: %w", err)
	}

	// prefer groups over movies
	if len(input.Groups) > 0 {
		newScene.Groups, err = translator.relatedGroups(input.Groups)
		if err != nil {
			return nil, fmt.Errorf("converting groups: %w", err)
		}
	} else if len(input.Movies) > 0 {
		newScene.Groups, err = translator.relatedGroupsFromMovies(input.Movies)
		if err != nil {
			return nil, fmt.Errorf("converting movies: %w", err)
		}
	}

	var coverImageData []byte
	if input.CoverImage != nil {
		var err error
		coverImageData, err = utils.ProcessImageInput(ctx, *input.CoverImage, r.localImageResolverFor(ctx))
		if err != nil {
			return nil, fmt.Errorf("processing cover image: %w", err)
		}
	}

	customFields := convertMapJSONNumbers(input.CustomFields)

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		ret, err = r.Resolver.sceneService.Create(ctx, models.CreateSceneInput{
			Scene:        &newScene,
			FileIDs:      fileIDs,
			CoverImage:   coverImageData,
			CustomFields: customFields,
		})
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *mutationResolver) SceneUpdate(ctx context.Context, input models.SceneUpdateInput) (ret *models.Scene, err error) {
	translator := changesetTranslator{
		inputMap: getUpdateInputMap(ctx),
	}

	// Start the transaction and save the scene
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		ret, err = r.sceneUpdate(ctx, input, translator)
		return err
	}); err != nil {
		return nil, err
	}

	r.hookExecutor.ExecutePostHooks(ctx, ret.ID, hook.SceneUpdatePost, input, translator.getFields())
	return r.getScene(ctx, ret.ID)
}

func (r *mutationResolver) ScenesUpdate(ctx context.Context, input []*models.SceneUpdateInput) (ret []*models.Scene, err error) {
	inputMaps := getUpdateInputMaps(ctx)

	// Start the transaction and save the scenes
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		for i, scene := range input {
			translator := changesetTranslator{
				inputMap: inputMaps[i],
			}

			thisScene, err := r.sceneUpdate(ctx, *scene, translator)
			if err != nil {
				return err
			}

			ret = append(ret, thisScene)
		}

		return nil
	}); err != nil {
		return nil, err
	}

	// execute post hooks outside of txn
	var newRet []*models.Scene
	for i, scene := range ret {
		translator := changesetTranslator{
			inputMap: inputMaps[i],
		}

		r.hookExecutor.ExecutePostHooks(ctx, scene.ID, hook.SceneUpdatePost, input, translator.getFields())

		scene, err = r.getScene(ctx, scene.ID)
		if err != nil {
			return nil, err
		}

		newRet = append(newRet, scene)
	}

	return newRet, nil
}

func scenePartialFromInput(input models.SceneUpdateInput, translator changesetTranslator) (*models.ScenePartial, error) {
	updatedScene := models.NewScenePartial()

	updatedScene.Title = translator.optionalString(input.Title, "title")
	updatedScene.Code = translator.optionalString(input.Code, "code")
	updatedScene.Details = translator.optionalString(input.Details, "details")
	updatedScene.Director = translator.optionalString(input.Director, "director")
	// stash#2359 (#3051). updateStrings returns nil when `directors` was ABSENT and a non-nil
	// UpdateStrings when it was sent empty, so an update that does not mention directors cannot
	// clear them. Without this line the field is accepted by the schema and silently discarded,
	// which is exactly what the e2e seed's round-trip assertion caught.
	updatedScene.Directors = translator.updateStrings(input.Directors, "directors")
	updatedScene.Rating = translator.optionalInt(input.Rating100, "rating100")

	if input.OCounter != nil {
		logger.Warnf("o_counter is deprecated and no longer supported, use sceneIncrementO/sceneDecrementO instead")
	}

	if input.PlayCount != nil {
		logger.Warnf("play_count is deprecated and no longer supported, use sceneIncrementPlayCount/sceneDecrementPlayCount instead")
	}

	updatedScene.PlayDuration = translator.optionalFloat64(input.PlayDuration, "play_duration")
	updatedScene.Organized = translator.optionalBool(input.Organized, "organized")
	updatedScene.StashIDs = translator.updateStashIDs(input.StashIds, "stash_ids")

	var err error

	updatedScene.Date, err = translator.optionalDate(input.Date, "date")
	if err != nil {
		return nil, fmt.Errorf("converting date: %w", err)
	}
	updatedScene.ProductionDate, err = translator.optionalDate(input.ProductionDate, "production_date")
	if err != nil {
		return nil, fmt.Errorf("converting production date: %w", err)
	}
	updatedScene.StudioID, err = translator.optionalIntFromString(input.StudioID, "studio_id")
	if err != nil {
		return nil, fmt.Errorf("converting studio id: %w", err)
	}

	updatedScene.URLs = translator.optionalURLs(input.Urls, input.URL)

	updatedScene.PrimaryFileID, err = translator.fileIDPtrFromString(input.PrimaryFileID)
	if err != nil {
		return nil, fmt.Errorf("converting primary file id: %w", err)
	}

	updatedScene.PerformerIDs, err = translator.updateIds(input.PerformerIds, "performer_ids")
	if err != nil {
		return nil, fmt.Errorf("converting performer ids: %w", err)
	}
	updatedScene.TagIDs, err = translator.updateIds(input.TagIds, "tag_ids")
	if err != nil {
		return nil, fmt.Errorf("converting tag ids: %w", err)
	}
	updatedScene.GalleryIDs, err = translator.updateIds(input.GalleryIds, "gallery_ids")
	if err != nil {
		return nil, fmt.Errorf("converting gallery ids: %w", err)
	}

	if translator.hasField("groups") {
		updatedScene.GroupIDs, err = translator.updateGroupIDs(input.Groups, "groups")
		if err != nil {
			return nil, fmt.Errorf("converting groups: %w", err)
		}
	} else if translator.hasField("movies") {
		updatedScene.GroupIDs, err = translator.updateGroupIDsFromMovies(input.Movies, "movies")
		if err != nil {
			return nil, fmt.Errorf("converting movies: %w", err)
		}
	}

	return &updatedScene, nil
}

// validateSceneWindow checks a window before it is written.
//
// # WHY THIS IS NOT THE DATABASE'S JOB ALONE
//
// The CHECKs on scenes_files are start >= 0, end >= 0, end > start. They are the last line and
// they are correct, but a violation surfaces as the string "CHECK constraint failed:
// scenes_files", which names no field and no value -- useless to the person who typed the
// number. This mirrors them with messages that name the offending field and value, and adds
// one case the CHECKs cannot express:
//
//   - a CHECK may not reference another table, so `end <= the file's duration` is NOT
//     enforceable in the schema. SceneStore.GetFiles clamps an overrunning window on READ
//     (sceneFileRanges), so accepting one would save a number the reader then silently
//     changes -- the form would show 9999, the scene would report the real end. Refusing is
//     strictly better than lying.
//
// The duplication is deliberate and is the same shape as the sprite plan: the database owns
// correctness, this owns the message.
//
// # THE AMBIGUITY REFUSAL
//
// The window lives on a (scene, file) pair, so a window update has to name a file. When
// `primary_file_id` is absent the scene's CURRENT primary is used, which is unambiguous for a
// one-file scene and WRONG for a multi-file one -- there is no way to know which file was meant,
// and guessing is precisely how two scenes of one file end up sharing a range. So it refuses.
func (r *mutationResolver) validateSceneWindow(ctx context.Context, sceneID int, input models.SceneUpdateInput) error {
	if input.StartTime == nil && input.EndTime == nil {
		// Not part of this update. Distinct from "both null", which CLEARS the window --
		// gqlgen gives an unsent field and an explicit null the same nil pointer, so the
		// clearing case is handled by the caller that knows the field was sent.
		return nil
	}

	var start, end *float64
	if input.StartTime != nil {
		v := *input.StartTime
		start = &v
	}
	if input.EndTime != nil {
		v := *input.EndTime
		end = &v
	}

	if start != nil && *start < 0 {
		return fmt.Errorf("start_time must not be negative, got %v", *start)
	}
	if end != nil && *end < 0 {
		return fmt.Errorf("end_time must not be negative, got %v", *end)
	}
	// `end == start` is a zero-length window: the CHECK's `end > start` refuses it, and so
	// does this, because a zero-length window has no frames and every derived artefact
	// (sprite, preview) would be an empty grid.
	if start != nil && end != nil && *end <= *start {
		return fmt.Errorf("end_time (%v) must be greater than start_time (%v)", *end, *start)
	}

	// Resolve the file the window belongs to.
	var fileID models.FileID
	if input.PrimaryFileID != nil {
		parsed, err := strconv.Atoi(*input.PrimaryFileID)
		if err != nil {
			return fmt.Errorf("converting primary_file_id: %w", err)
		}
		fileID = models.FileID(parsed)
	} else {
		scene, err := r.repository.Scene.Find(ctx, sceneID)
		if err != nil {
			return err
		}
		if scene == nil {
			return fmt.Errorf("scene with id %d not found", sceneID)
		}
		if err := scene.LoadFiles(ctx, r.repository.Scene); err != nil {
			return err
		}
		files := scene.Files.List()
		if len(files) == 0 {
			return errors.New("cannot set a range on a scene with no files")
		}
		if len(files) > 1 {
			return fmt.Errorf(
				"cannot set a range without primary_file_id: scene %d has %d files and the range belongs to one of them",
				sceneID, len(files))
		}
		fileID = files[0].ID
	}

	files, err := r.repository.File.Find(ctx, fileID)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("file %d not found", fileID)
	}
	vf, ok := files[0].(*models.VideoFile)
	if !ok {
		return fmt.Errorf("file %d is not a video file, so it has no range", fileID)
	}

	// The check the schema cannot express.
	if end != nil && *end > vf.Duration {
		return fmt.Errorf("end_time (%v) is past the end of the file (%v seconds)", *end, vf.Duration)
	}
	if start != nil && *start > vf.Duration {
		return fmt.Errorf("start_time (%v) is past the end of the file (%v seconds)", *start, vf.Duration)
	}

	return nil
}

func (r *mutationResolver) sceneUpdate(ctx context.Context, input models.SceneUpdateInput, translator changesetTranslator) (*models.Scene, error) {
	sceneID, err := strconv.Atoi(input.ID)
	if err != nil {
		return nil, fmt.Errorf("converting id: %w", err)
	}

	qb := r.repository.Scene

	originalScene, err := qb.Find(ctx, sceneID)
	if err != nil {
		return nil, err
	}

	if originalScene == nil {
		return nil, fmt.Errorf("scene with id %d not found", sceneID)
	}

	// Populate scene from the input
	updatedScene, err := scenePartialFromInput(input, translator)
	if err != nil {
		return nil, err
	}

	// ensure that title is set where scene has no file
	if updatedScene.Title.Set && updatedScene.Title.Value == "" {
		if err := originalScene.LoadFiles(ctx, r.repository.Scene); err != nil {
			return nil, err
		}

		if len(originalScene.Files.List()) == 0 {
			return nil, errors.New("title must be set if scene has no files")
		}
	}

	if updatedScene.PrimaryFileID != nil {
		newPrimaryFileID := *updatedScene.PrimaryFileID

		// if file hash has changed, we should migrate generated files
		// after commit
		if err := originalScene.LoadFiles(ctx, r.repository.Scene); err != nil {
			return nil, err
		}

		// ensure that new primary file is associated with scene
		var f *models.VideoFile
		for _, ff := range originalScene.Files.List() {
			if ff.ID == newPrimaryFileID {
				f = ff
			}
		}

		if f == nil {
			return nil, fmt.Errorf("file with id %d not associated with scene", newPrimaryFileID)
		}
	}

	var coverImageData []byte
	coverImageIncluded := translator.hasField("cover_image")
	if input.CoverImage != nil {
		var err error
		coverImageData, err = utils.ProcessImageInput(ctx, *input.CoverImage, r.localImageResolverFor(ctx))
		if err != nil {
			return nil, fmt.Errorf("processing cover image: %w", err)
		}
	}

	// #3530 -- validate the window BEFORE anything is written, and against the file the
	// window will actually live on. See validateSceneWindow for why this is not the
	// database's job alone.
	windowSent := translator.hasField("start_time") || translator.hasField("end_time")
	if windowSent {
		if err := r.validateSceneWindow(ctx, sceneID, input); err != nil {
			return nil, err
		}
	}

	var customFields *models.CustomFieldsInput
	if input.CustomFields != nil {
		cfCopy := *input.CustomFields
		customFields = &cfCopy
		// convert json.Numbers to int/float
		customFields.Full = convertMapJSONNumbers(customFields.Full)
		customFields.Partial = convertMapJSONNumbers(customFields.Partial)
	}

	scene, err := qb.UpdatePartial(ctx, sceneID, *updatedScene)
	if err != nil {
		return nil, err
	}

	if coverImageIncluded {
		if err := r.sceneUpdateCoverImage(ctx, scene, coverImageData); err != nil {
			return nil, err
		}
	}

	if customFields != nil {
		if err := qb.SetCustomFields(ctx, scene.ID, *customFields); err != nil {
			return nil, err
		}
	}

	// #3530 -- write the window LAST, after UpdatePartial.
	//
	// Order matters and it is not cosmetic. UpdatePartial rewrites the scene's file rows when
	// the input carries a file list (SceneStore.Update's Files.Loaded() branch), which is
	// exactly the path that used to ERASE a window -- fixed in b604926c0, but writing the
	// window first would still race that rewrite for the reader. Writing last means the value
	// that survives is the one just validated.
	//
	// The file is re-resolved rather than reused from validation because UpdatePartial may have
	// changed which file is primary when the input set primary_file_id.
	if windowSent {
		if err := r.setSceneWindow(ctx, scene, input); err != nil {
			return nil, err
		}
	}

	return scene, nil
}

// setSceneWindow writes the validated window to the row it belongs to.
//
// `windowSent` is what makes "clear the window" expressible: gqlgen gives an ABSENT field and an
// explicit `null` the same nil pointer, so `input.StartTime == nil` cannot tell "leave it alone"
// from "remove it". The changeset translator can, and the caller has already used it to decide
// that a window was part of this update at all.
//
// The file is resolved HERE rather than reusing validation's answer, because UpdatePartial may
// have changed which file is primary when the input set primary_file_id -- and reading the
// post-update scene is what makes the write land on the row the caller will then read back.
func (r *mutationResolver) setSceneWindow(ctx context.Context, scene *models.Scene, input models.SceneUpdateInput) error {
	fileID := scene.PrimaryFileID
	if input.PrimaryFileID != nil {
		parsed, err := strconv.Atoi(*input.PrimaryFileID)
		if err != nil {
			return fmt.Errorf("converting primary_file_id: %w", err)
		}
		id := models.FileID(parsed)
		fileID = &id
	}

	// A nil PrimaryFileID means the scene has no primary file, which validateSceneWindow has
	// already refused for a scene with no files -- but a scene whose primary was just CLEARED by
	// this same input reaches here, so the check cannot be skipped.
	if fileID == nil || *fileID == 0 {
		return errors.New("cannot set a range: the scene has no primary file")
	}

	// Pass the pointers straight through. A nil here means NULL in the column, which is how a
	// window is cleared and how an open-ended window is expressed -- the store does not clamp and
	// does not second-guess, because validateSceneWindow has already refused everything the
	// database would have rejected.
	if err := r.repository.Scene.SetSceneRange(ctx, scene.ID, *fileID, input.StartTime, input.EndTime); err != nil {
		return err
	}
	return nil
}

func (r *mutationResolver) sceneUpdateCoverImage(ctx context.Context, s *models.Scene, coverImageData []byte) error {
	qb := r.repository.Scene

	// update cover table - empty data will clear the cover
	if err := qb.UpdateCover(ctx, s.ID, coverImageData); err != nil {
		return err
	}

	return nil
}

func (r *mutationResolver) BulkSceneUpdate(ctx context.Context, input BulkSceneUpdateInput) ([]*models.Scene, error) {
	sceneIDs, err := stringslice.StringSliceToIntSlice(input.Ids)
	if err != nil {
		return nil, fmt.Errorf("converting ids: %w", err)
	}

	translator := changesetTranslator{
		inputMap: getUpdateInputMap(ctx),
	}

	// Populate scene from the input
	updatedScene := models.NewScenePartial()

	updatedScene.Title = translator.optionalString(input.Title, "title")
	updatedScene.Code = translator.optionalString(input.Code, "code")
	updatedScene.Details = translator.optionalString(input.Details, "details")
	updatedScene.Director = translator.optionalString(input.Director, "director")
	// stash#2359 (#3051). updateStrings returns nil when `directors` was ABSENT and a non-nil
	// UpdateStrings when it was sent empty, so an update that does not mention directors cannot
	// clear them. Without this line the field is accepted by the schema and silently discarded,
	// which is exactly what the e2e seed's round-trip assertion caught.
	updatedScene.Directors = translator.updateStringsBulk(input.Directors, "directors")
	updatedScene.Rating = translator.optionalInt(input.Rating100, "rating100")
	updatedScene.Organized = translator.optionalBool(input.Organized, "organized")

	updatedScene.Date, err = translator.optionalDate(input.Date, "date")
	if err != nil {
		return nil, fmt.Errorf("converting date: %w", err)
	}
	updatedScene.ProductionDate, err = translator.optionalDate(input.ProductionDate, "production_date")
	if err != nil {
		return nil, fmt.Errorf("converting production date: %w", err)
	}
	updatedScene.StudioID, err = translator.optionalIntFromString(input.StudioID, "studio_id")
	if err != nil {
		return nil, fmt.Errorf("converting studio id: %w", err)
	}

	updatedScene.URLs = translator.optionalURLsBulk(input.Urls, input.URL)

	updatedScene.PerformerIDs, err = translator.updateIdsBulk(input.PerformerIds, "performer_ids")
	if err != nil {
		return nil, fmt.Errorf("converting performer ids: %w", err)
	}
	updatedScene.TagIDs, err = translator.updateIdsBulk(input.TagIds, "tag_ids")
	if err != nil {
		return nil, fmt.Errorf("converting tag ids: %w", err)
	}
	updatedScene.GalleryIDs, err = translator.updateIdsBulk(input.GalleryIds, "gallery_ids")
	if err != nil {
		return nil, fmt.Errorf("converting gallery ids: %w", err)
	}

	if translator.hasField("group_ids") {
		updatedScene.GroupIDs, err = translator.updateGroupIDsBulk(input.GroupIds, "group_ids")
		if err != nil {
			return nil, fmt.Errorf("converting group ids: %w", err)
		}
	} else if translator.hasField("movie_ids") {
		updatedScene.GroupIDs, err = translator.updateGroupIDsBulk(input.MovieIds, "movie_ids")
		if err != nil {
			return nil, fmt.Errorf("converting movie ids: %w", err)
		}
	}

	var customFields *models.CustomFieldsInput
	if input.CustomFields != nil {
		cf := handleUpdateCustomFields(*input.CustomFields)
		customFields = &cf
	}

	ret := []*models.Scene{}

	// Start the transaction and save the scenes
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.Scene

		for _, sceneID := range sceneIDs {
			scene, err := qb.UpdatePartial(ctx, sceneID, updatedScene)
			if err != nil {
				return err
			}

			if customFields != nil {
				if err := qb.SetCustomFields(ctx, scene.ID, *customFields); err != nil {
					return err
				}
			}

			ret = append(ret, scene)
		}

		return nil
	}); err != nil {
		return nil, err
	}

	// execute post hooks outside of txn
	var newRet []*models.Scene
	for _, scene := range ret {
		r.hookExecutor.ExecutePostHooks(ctx, scene.ID, hook.SceneUpdatePost, input, translator.getFields())

		scene, err = r.getScene(ctx, scene.ID)
		if err != nil {
			return nil, err
		}

		newRet = append(newRet, scene)
	}

	return newRet, nil
}

func (r *mutationResolver) SceneDestroy(ctx context.Context, input models.SceneDestroyInput) (bool, error) {
	sceneID, err := strconv.Atoi(input.ID)
	if err != nil {
		return false, fmt.Errorf("converting id: %w", err)
	}

	fileNamingAlgo := manager.GetInstance().Config.GetVideoFileNamingAlgorithm()
	trashPath := manager.GetInstance().Config.GetDeleteTrashPath()

	var s *models.Scene
	fileDeleter := &scene.FileDeleter{
		Deleter:        file.NewDeleterWithTrash(trashPath),
		FileNamingAlgo: fileNamingAlgo,
		Paths:          manager.GetInstance().Paths,
	}

	deleteGenerated := utils.IsTrue(input.DeleteGenerated)
	deleteFile := utils.IsTrue(input.DeleteFile)
	destroyFileEntry := utils.IsTrue(input.DestroyFileEntry)

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.Scene
		var err error
		s, err = qb.Find(ctx, sceneID)
		if err != nil {
			return err
		}

		if s == nil {
			return fmt.Errorf("scene with id %d not found", sceneID)
		}

		// kill any running encoders
		manager.KillRunningStreams(s, fileNamingAlgo)

		return r.sceneService.Destroy(ctx, s, fileDeleter, deleteGenerated, deleteFile, destroyFileEntry)
	}); err != nil {
		fileDeleter.Rollback()
		return false, err
	}

	// perform the post-commit actions
	fileDeleter.Commit()

	// call post hook after performing the other actions
	r.hookExecutor.ExecutePostHooks(ctx, s.ID, hook.SceneDestroyPost, plugin.SceneDestroyInput{
		SceneDestroyInput: input,
		Checksum:          s.Checksum,
		OSHash:            s.OSHash,
		Path:              s.Path,
	}, nil)

	return true, nil
}

func (r *mutationResolver) ScenesDestroy(ctx context.Context, input models.ScenesDestroyInput) (bool, error) {
	sceneIDs, err := stringslice.StringSliceToIntSlice(input.Ids)
	if err != nil {
		return false, fmt.Errorf("converting ids: %w", err)
	}

	var scenes []*models.Scene
	fileNamingAlgo := manager.GetInstance().Config.GetVideoFileNamingAlgorithm()
	trashPath := manager.GetInstance().Config.GetDeleteTrashPath()

	fileDeleter := &scene.FileDeleter{
		Deleter:        file.NewDeleterWithTrash(trashPath),
		FileNamingAlgo: fileNamingAlgo,
		Paths:          manager.GetInstance().Paths,
	}

	deleteGenerated := utils.IsTrue(input.DeleteGenerated)
	deleteFile := utils.IsTrue(input.DeleteFile)
	destroyFileEntry := utils.IsTrue(input.DestroyFileEntry)

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.Scene

		for _, id := range sceneIDs {
			scene, err := qb.Find(ctx, id)
			if err != nil {
				return err
			}
			if scene == nil {
				return fmt.Errorf("scene with id %d not found", id)
			}

			scenes = append(scenes, scene)

			// kill any running encoders
			manager.KillRunningStreams(scene, fileNamingAlgo)

			if err := r.sceneService.Destroy(ctx, scene, fileDeleter, deleteGenerated, deleteFile, destroyFileEntry); err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		fileDeleter.Rollback()
		return false, err
	}

	// perform the post-commit actions
	fileDeleter.Commit()

	for _, scene := range scenes {
		// call post hook after performing the other actions
		r.hookExecutor.ExecutePostHooks(ctx, scene.ID, hook.SceneDestroyPost, plugin.ScenesDestroyInput{
			ScenesDestroyInput: input,
			Checksum:           scene.Checksum,
			OSHash:             scene.OSHash,
			Path:               scene.Path,
		}, nil)
	}

	return true, nil
}

func (r *mutationResolver) SceneAssignFile(ctx context.Context, input AssignSceneFileInput) (bool, error) {
	sceneID, err := strconv.Atoi(input.SceneID)
	if err != nil {
		return false, fmt.Errorf("converting scene id: %w", err)
	}

	fileID, err := strconv.Atoi(input.FileID)
	if err != nil {
		return false, fmt.Errorf("converting file id: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		return r.Resolver.sceneService.AssignFile(ctx, sceneID, models.FileID(fileID))
	}); err != nil {
		return false, fmt.Errorf("assigning file to scene: %w", err)
	}

	return true, nil
}

func (r *mutationResolver) SceneMerge(ctx context.Context, input SceneMergeInput) (*models.Scene, error) {
	srcIDs, err := stringslice.StringSliceToIntSlice(input.Source)
	if err != nil {
		return nil, fmt.Errorf("converting source ids: %w", err)
	}

	destID, err := strconv.Atoi(input.Destination)
	if err != nil {
		return nil, fmt.Errorf("converting destination id: %w", err)
	}

	var values *models.ScenePartial
	var coverImageData []byte
	var customFields *models.CustomFieldsInput

	if input.Values != nil {
		translator := changesetTranslator{
			inputMap: getNamedUpdateInputMap(ctx, "input.values"),
		}

		values, err = scenePartialFromInput(*input.Values, translator)
		if err != nil {
			return nil, err
		}

		if input.Values.CoverImage != nil {
			var err error
			coverImageData, err = utils.ProcessImageInput(ctx, *input.Values.CoverImage, r.localImageResolverFor(ctx))
			if err != nil {
				return nil, fmt.Errorf("processing cover image: %w", err)
			}
		}

		if input.Values.CustomFields != nil {
			cf := handleUpdateCustomFields(*input.Values.CustomFields)
			customFields = &cf
		}
	} else {
		v := models.NewScenePartial()
		values = &v
	}

	mgr := manager.GetInstance()
	trashPath := mgr.Config.GetDeleteTrashPath()
	fileDeleter := &scene.FileDeleter{
		Deleter:        file.NewDeleterWithTrash(trashPath),
		FileNamingAlgo: mgr.Config.GetVideoFileNamingAlgorithm(),
		Paths:          mgr.Paths,
	}

	var ret *models.Scene
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		if err := r.Resolver.sceneService.Merge(ctx, srcIDs, destID, fileDeleter, scene.MergeOptions{
			ScenePartial:       *values,
			IncludePlayHistory: utils.IsTrue(input.PlayHistory),
			IncludeOHistory:    utils.IsTrue(input.OHistory),
		}); err != nil {
			return err
		}

		ret, err = r.Resolver.repository.Scene.Find(ctx, destID)
		if err != nil {
			return err
		}
		if ret == nil {
			return fmt.Errorf("scene with id %d not found", destID)
		}

		// only update cover image if one was provided
		if len(coverImageData) > 0 {
			if err := r.sceneUpdateCoverImage(ctx, ret, coverImageData); err != nil {
				return err
			}
		}

		if customFields != nil {
			if err := r.Resolver.repository.Scene.SetCustomFields(ctx, ret.ID, *customFields); err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *mutationResolver) getSceneMarker(ctx context.Context, id int) (ret *models.SceneMarker, err error) {
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.SceneMarker.Find(ctx, id)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *mutationResolver) SceneMarkerCreate(ctx context.Context, input SceneMarkerCreateInput) (*models.SceneMarker, error) {
	sceneID, err := strconv.Atoi(input.SceneID)
	if err != nil {
		return nil, fmt.Errorf("converting scene id: %w", err)
	}

	primaryTagID, err := strconv.Atoi(input.PrimaryTagID)
	if err != nil {
		return nil, fmt.Errorf("converting primary tag id: %w", err)
	}

	// Populate a new scene marker from the input
	newMarker := models.NewSceneMarker()

	newMarker.Title = strings.TrimSpace(input.Title)
	newMarker.Seconds = input.Seconds
	newMarker.PrimaryTagID = primaryTagID
	newMarker.SceneID = sceneID

	if input.EndSeconds != nil {
		if err := validateSceneMarkerEndSeconds(newMarker.Seconds, *input.EndSeconds); err != nil {
			return nil, err
		}
		newMarker.EndSeconds = input.EndSeconds
	}

	tagIDs, err := stringslice.StringSliceToIntSlice(input.TagIds)
	if err != nil {
		return nil, fmt.Errorf("converting tag ids: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.SceneMarker

		err := qb.Create(ctx, &newMarker)
		if err != nil {
			return err
		}

		// Save the marker tags
		// If this tag is the primary tag, then let's not add it.
		tagIDs = sliceutil.Exclude(tagIDs, []int{newMarker.PrimaryTagID})
		return qb.UpdateTags(ctx, newMarker.ID, tagIDs)
	}); err != nil {
		return nil, err
	}

	r.hookExecutor.ExecutePostHooks(ctx, newMarker.ID, hook.SceneMarkerCreatePost, input, nil)
	return r.getSceneMarker(ctx, newMarker.ID)
}

func validateSceneMarkerEndSeconds(seconds, endSeconds float64) error {
	if endSeconds < seconds {
		return fmt.Errorf("end_seconds (%f) must be greater than or equal to seconds (%f)", endSeconds, seconds)
	}
	return nil
}

func float64OrZero(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func (r *mutationResolver) SceneMarkerUpdate(ctx context.Context, input SceneMarkerUpdateInput) (*models.SceneMarker, error) {
	markerID, err := strconv.Atoi(input.ID)
	if err != nil {
		return nil, fmt.Errorf("converting id: %w", err)
	}

	translator := changesetTranslator{
		inputMap: getUpdateInputMap(ctx),
	}

	// Populate scene marker from the input
	updatedMarker := models.NewSceneMarkerPartial()

	updatedMarker.Title = translator.optionalString(input.Title, "title")
	updatedMarker.Seconds = translator.optionalFloat64(input.Seconds, "seconds")
	updatedMarker.EndSeconds = translator.optionalFloat64(input.EndSeconds, "end_seconds")
	updatedMarker.SceneID, err = translator.optionalIntFromString(input.SceneID, "scene_id")
	if err != nil {
		return nil, fmt.Errorf("converting scene id: %w", err)
	}
	updatedMarker.PrimaryTagID, err = translator.optionalIntFromString(input.PrimaryTagID, "primary_tag_id")
	if err != nil {
		return nil, fmt.Errorf("converting primary tag id: %w", err)
	}

	var tagIDs []int
	tagIdsIncluded := translator.hasField("tag_ids")
	if input.TagIds != nil {
		tagIDs, err = stringslice.StringSliceToIntSlice(input.TagIds)
		if err != nil {
			return nil, fmt.Errorf("converting tag ids: %w", err)
		}
	}

	mgr := manager.GetInstance()
	trashPath := mgr.Config.GetDeleteTrashPath()

	fileDeleter := &scene.FileDeleter{
		Deleter:        file.NewDeleterWithTrash(trashPath),
		FileNamingAlgo: mgr.Config.GetVideoFileNamingAlgorithm(),
		Paths:          mgr.Paths,
	}

	// Start the transaction and save the scene marker
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.SceneMarker
		sqb := r.repository.Scene

		// check to see if timestamp was changed
		existingMarker, err := qb.Find(ctx, markerID)
		if err != nil {
			return err
		}
		if existingMarker == nil {
			return fmt.Errorf("scene marker with id %d not found", markerID)
		}

		// Validate end_seconds
		shouldValidateEndSeconds := (updatedMarker.Seconds.Set || updatedMarker.EndSeconds.Set) && !updatedMarker.EndSeconds.Null
		if shouldValidateEndSeconds {
			seconds := existingMarker.Seconds
			if updatedMarker.Seconds.Set {
				seconds = updatedMarker.Seconds.Value
			}

			endSeconds := existingMarker.EndSeconds
			if updatedMarker.EndSeconds.Set {
				endSeconds = &updatedMarker.EndSeconds.Value
			}

			if endSeconds != nil {
				if err := validateSceneMarkerEndSeconds(seconds, *endSeconds); err != nil {
					return err
				}
			}
		}

		newMarker, err := qb.UpdatePartial(ctx, markerID, updatedMarker)
		if err != nil {
			return err
		}

		existingScene, err := sqb.Find(ctx, existingMarker.SceneID)
		if err != nil {
			return err
		}
		if existingScene == nil {
			return fmt.Errorf("scene with id %d not found", existingMarker.SceneID)
		}

		// remove the marker preview if the scene changed or if the timestamp was changed
		if existingMarker.SceneID != newMarker.SceneID || existingMarker.Seconds != newMarker.Seconds || float64OrZero(existingMarker.EndSeconds) != float64OrZero(newMarker.EndSeconds) {
			seconds := int(existingMarker.Seconds)
			if err := fileDeleter.MarkMarkerFiles(existingScene, seconds); err != nil {
				return err
			}
		}

		if tagIdsIncluded {
			// Save the marker tags
			// If this tag is the primary tag, then let's not add it.
			tagIDs = sliceutil.Exclude(tagIDs, []int{newMarker.PrimaryTagID})
			if err := qb.UpdateTags(ctx, markerID, tagIDs); err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		fileDeleter.Rollback()
		return nil, err
	}

	// perform the post-commit actions
	fileDeleter.Commit()

	r.hookExecutor.ExecutePostHooks(ctx, markerID, hook.SceneMarkerUpdatePost, input, translator.getFields())
	return r.getSceneMarker(ctx, markerID)
}

func (r *mutationResolver) BulkSceneMarkerUpdate(ctx context.Context, input BulkSceneMarkerUpdateInput) ([]*models.SceneMarker, error) {
	ids, err := stringslice.StringSliceToIntSlice(input.Ids)
	if err != nil {
		return nil, fmt.Errorf("converting ids: %w", err)
	}

	translator := changesetTranslator{
		inputMap: getUpdateInputMap(ctx),
	}

	// Populate performer from the input
	partial := models.NewSceneMarkerPartial()

	partial.Title = translator.optionalString(input.Title, "title")

	partial.PrimaryTagID, err = translator.optionalIntFromString(input.PrimaryTagID, "primary_tag_id")
	if err != nil {
		return nil, fmt.Errorf("converting primary tag id: %w", err)
	}

	partial.TagIDs, err = translator.updateIdsBulk(input.TagIds, "tag_ids")
	if err != nil {
		return nil, fmt.Errorf("converting tag ids: %w", err)
	}

	ret := []*models.SceneMarker{}

	// Start the transaction and save the performers
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.SceneMarker

		for _, id := range ids {
			l := partial

			if err := adjustMarkerPartialForTagExclusion(ctx, r.repository.SceneMarker, id, &l); err != nil {
				return err
			}

			updated, err := qb.UpdatePartial(ctx, id, l)
			if err != nil {
				return err
			}

			ret = append(ret, updated)
		}

		return nil
	}); err != nil {
		return nil, err
	}

	// execute post hooks outside of txn
	var newRet []*models.SceneMarker
	for _, m := range ret {
		r.hookExecutor.ExecutePostHooks(ctx, m.ID, hook.SceneMarkerUpdatePost, input, translator.getFields())

		m, err = r.getSceneMarker(ctx, m.ID)
		if err != nil {
			return nil, err
		}

		newRet = append(newRet, m)
	}

	return newRet, nil
}

// adjustMarkerPartialForTagExclusion adjusts the SceneMarkerPartial to exclude the primary tag from tag updates.
func adjustMarkerPartialForTagExclusion(ctx context.Context, r models.SceneMarkerReader, id int, partial *models.SceneMarkerPartial) error {
	if partial.TagIDs == nil && !partial.PrimaryTagID.Set {
		return nil
	}

	// exclude primary tag from tag updates
	var primaryTagID int
	if partial.PrimaryTagID.Set {
		primaryTagID = partial.PrimaryTagID.Value
	} else {
		existing, err := r.Find(ctx, id)
		if err != nil {
			return fmt.Errorf("finding existing primary tag id: %w", err)
		}

		primaryTagID = existing.PrimaryTagID
	}

	existingTagIDs, err := r.GetTagIDs(ctx, id)
	if err != nil {
		return fmt.Errorf("getting existing tag ids: %w", err)
	}

	tagIDAttr := partial.TagIDs

	if tagIDAttr == nil {
		tagIDAttr = &models.UpdateIDs{
			IDs:  existingTagIDs,
			Mode: models.RelationshipUpdateModeSet,
		}
	}

	newTagIDs := tagIDAttr.Apply(existingTagIDs)
	// Remove primary tag from newTagIDs if present
	newTagIDs = sliceutil.Exclude(newTagIDs, []int{primaryTagID})

	if len(existingTagIDs) != len(newTagIDs) {
		partial.TagIDs = &models.UpdateIDs{
			IDs:  newTagIDs,
			Mode: models.RelationshipUpdateModeSet,
		}
	} else {
		// no change to tags required
		partial.TagIDs = nil
	}

	return nil
}

func (r *mutationResolver) SceneMarkerDestroy(ctx context.Context, id string) (bool, error) {
	return r.SceneMarkersDestroy(ctx, []string{id})
}

func (r *mutationResolver) SceneMarkersDestroy(ctx context.Context, markerIDs []string) (bool, error) {
	ids, err := stringslice.StringSliceToIntSlice(markerIDs)
	if err != nil {
		return false, fmt.Errorf("converting ids: %w", err)
	}

	var markers []*models.SceneMarker
	fileNamingAlgo := manager.GetInstance().Config.GetVideoFileNamingAlgorithm()
	trashPath := manager.GetInstance().Config.GetDeleteTrashPath()

	fileDeleter := &scene.FileDeleter{
		Deleter:        file.NewDeleterWithTrash(trashPath),
		FileNamingAlgo: fileNamingAlgo,
		Paths:          manager.GetInstance().Paths,
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.SceneMarker
		sqb := r.repository.Scene

		for _, markerID := range ids {
			marker, err := qb.Find(ctx, markerID)

			if err != nil {
				return err
			}

			if marker == nil {
				return fmt.Errorf("scene marker with id %d not found", markerID)
			}

			s, err := sqb.Find(ctx, marker.SceneID)

			if err != nil {
				return err
			}

			if s == nil {
				return fmt.Errorf("scene with id %d not found", marker.SceneID)
			}

			markers = append(markers, marker)

			if err := scene.DestroyMarker(ctx, s, marker, qb, fileDeleter); err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		fileDeleter.Rollback()
		return false, err
	}

	fileDeleter.Commit()

	for _, marker := range markers {
		r.hookExecutor.ExecutePostHooks(ctx, marker.ID, hook.SceneMarkerDestroyPost, markerIDs, nil)
	}

	return true, nil
}

func (r *mutationResolver) SceneSaveActivity(ctx context.Context, id string, resumeTime *float64, playDuration *float64) (ret bool, err error) {
	sceneID, err := strconv.Atoi(id)
	if err != nil {
		return false, fmt.Errorf("converting id: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.Scene

		ret, err = qb.SaveActivity(ctx, sceneID, resumeTime, playDuration)
		return err
	}); err != nil {
		return false, err
	}

	return ret, nil
}

func (r *mutationResolver) SceneResetActivity(ctx context.Context, id string, resetResume *bool, resetDuration *bool) (ret bool, err error) {
	sceneID, err := strconv.Atoi(id)
	if err != nil {
		return false, fmt.Errorf("converting id: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.Scene

		ret, err = qb.ResetActivity(ctx, sceneID, utils.IsTrue(resetResume), utils.IsTrue(resetDuration))
		return err
	}); err != nil {
		return false, err
	}

	return ret, nil
}

// deprecated
func (r *mutationResolver) SceneIncrementPlayCount(ctx context.Context, id string) (ret int, err error) {
	sceneID, err := strconv.Atoi(id)
	if err != nil {
		return 0, fmt.Errorf("converting id: %w", err)
	}

	var updatedTimes []time.Time

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.Scene

		updatedTimes, err = qb.AddViews(ctx, sceneID, nil)
		return err
	}); err != nil {
		return 0, err
	}

	return len(updatedTimes), nil
}

func (r *mutationResolver) SceneAddPlay(ctx context.Context, id string, t []*time.Time) (*HistoryMutationResult, error) {
	sceneID, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("converting id: %w", err)
	}

	var times []time.Time

	// convert time to local time, so that sorting is consistent
	for _, tt := range t {
		times = append(times, tt.Local())
	}

	var updatedTimes []time.Time

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.Scene

		updatedTimes, err = qb.AddViews(ctx, sceneID, times)
		return err
	}); err != nil {
		return nil, err
	}

	return &HistoryMutationResult{
		Count:   len(updatedTimes),
		History: sliceutil.ValuesToPtrs(updatedTimes),
	}, nil
}

func (r *mutationResolver) SceneDeletePlay(ctx context.Context, id string, t []*time.Time) (*HistoryMutationResult, error) {
	sceneID, err := strconv.Atoi(id)
	if err != nil {
		return nil, err
	}

	var times []time.Time

	for _, tt := range t {
		times = append(times, *tt)
	}

	var updatedTimes []time.Time

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.Scene

		updatedTimes, err = qb.DeleteViews(ctx, sceneID, times)
		return err
	}); err != nil {
		return nil, err
	}

	return &HistoryMutationResult{
		Count:   len(updatedTimes),
		History: sliceutil.ValuesToPtrs(updatedTimes),
	}, nil
}

func (r *mutationResolver) SceneResetPlayCount(ctx context.Context, id string) (ret int, err error) {
	sceneID, err := strconv.Atoi(id)
	if err != nil {
		return 0, err
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.Scene

		ret, err = qb.DeleteAllViews(ctx, sceneID)
		return err
	}); err != nil {
		return 0, err
	}

	return ret, nil
}

// deprecated
func (r *mutationResolver) SceneIncrementO(ctx context.Context, id string) (ret int, err error) {
	sceneID, err := strconv.Atoi(id)
	if err != nil {
		return 0, fmt.Errorf("converting id: %w", err)
	}

	var updatedTimes []time.Time

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.Scene

		updatedTimes, err = qb.AddO(ctx, sceneID, nil)
		return err
	}); err != nil {
		return 0, err
	}

	return len(updatedTimes), nil
}

// deprecated
func (r *mutationResolver) SceneDecrementO(ctx context.Context, id string) (ret int, err error) {
	sceneID, err := strconv.Atoi(id)
	if err != nil {
		return 0, fmt.Errorf("converting id: %w", err)
	}

	var updatedTimes []time.Time

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.Scene

		updatedTimes, err = qb.DeleteO(ctx, sceneID, nil)
		return err
	}); err != nil {
		return 0, err
	}

	return len(updatedTimes), nil
}

func (r *mutationResolver) SceneResetO(ctx context.Context, id string) (ret int, err error) {
	sceneID, err := strconv.Atoi(id)
	if err != nil {
		return 0, fmt.Errorf("converting id: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.Scene

		ret, err = qb.ResetO(ctx, sceneID)
		return err
	}); err != nil {
		return 0, err
	}

	return ret, nil
}

func (r *mutationResolver) SceneAddO(ctx context.Context, id string, t []*time.Time) (*HistoryMutationResult, error) {
	sceneID, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("converting id: %w", err)
	}

	var times []time.Time

	// convert time to local time, so that sorting is consistent
	for _, tt := range t {
		times = append(times, tt.Local())
	}

	var updatedTimes []time.Time

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.Scene

		updatedTimes, err = qb.AddO(ctx, sceneID, times)
		return err
	}); err != nil {
		return nil, err
	}

	return &HistoryMutationResult{
		Count:   len(updatedTimes),
		History: sliceutil.ValuesToPtrs(updatedTimes),
	}, nil
}

func (r *mutationResolver) SceneDeleteO(ctx context.Context, id string, t []*time.Time) (*HistoryMutationResult, error) {
	sceneID, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("converting id: %w", err)
	}

	var times []time.Time

	for _, tt := range t {
		times = append(times, *tt)
	}

	var updatedTimes []time.Time

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.Scene

		updatedTimes, err = qb.DeleteO(ctx, sceneID, times)
		return err
	}); err != nil {
		return nil, err
	}

	return &HistoryMutationResult{
		Count:   len(updatedTimes),
		History: sliceutil.ValuesToPtrs(updatedTimes),
	}, nil
}

func (r *mutationResolver) SceneGenerateScreenshot(ctx context.Context, id string, at *float64) (string, error) {
	var jobID int
	if at != nil {
		jobID = manager.GetInstance().GenerateScreenshot(ctx, id, *at)
	} else {
		jobID = manager.GetInstance().GenerateDefaultScreenshot(ctx, id)
	}

	return strconv.Itoa(jobID), nil
}
