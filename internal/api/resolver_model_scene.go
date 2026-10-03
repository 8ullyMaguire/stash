package api

import (
	"context"
	"fmt"
	"time"

	"github.com/stashapp/stash/internal/api/loaders"
	"github.com/stashapp/stash/internal/api/urlbuilders"
	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/session"
	"github.com/stashapp/stash/pkg/signedurl"
)

func convertVideoFile(f models.File) (*models.VideoFile, error) {
	vf, ok := f.(*models.VideoFile)
	if !ok {
		return nil, fmt.Errorf("file %T is not a video file", f)
	}
	return vf, nil
}

func (r *sceneResolver) getPrimaryFile(ctx context.Context, obj *models.Scene) (*models.VideoFile, error) {
	if obj.PrimaryFileID != nil {
		f, err := loaders.From(ctx).FileByID.Load(*obj.PrimaryFileID)
		if err != nil {
			return nil, err
		}

		ret, err := convertVideoFile(f)
		if err != nil {
			return nil, err
		}

		obj.Files.SetPrimary(ret)

		return ret, nil
	} else {
		_ = obj.LoadPrimaryFile(ctx, r.repository.File)
	}

	return nil, nil
}

func (r *sceneResolver) getFiles(ctx context.Context, obj *models.Scene) ([]*models.VideoFile, error) {
	fileIDs, err := loaders.From(ctx).SceneFiles.Load(obj.ID)
	if err != nil {
		return nil, err
	}

	files, errs := loaders.From(ctx).FileByID.LoadAll(fileIDs)
	err = firstError(errs)
	if err != nil {
		return nil, err
	}

	ret := make([]*models.VideoFile, len(files))
	for i, f := range files {
		ret[i], err = convertVideoFile(f)
		if err != nil {
			return nil, err
		}
	}

	obj.Files.Set(ret)

	return ret, nil
}

func (r *sceneResolver) Date(ctx context.Context, obj *models.Scene) (*string, error) {
	if obj.Date != nil {
		result := obj.Date.String()
		return &result, nil
	}
	return nil, nil
}

func (r *sceneResolver) ProductionDate(ctx context.Context, obj *models.Scene) (*string, error) {
	if obj.ProductionDate != nil {
		result := obj.ProductionDate.String()
		return &result, nil
	}
	return nil, nil
}

func (r *sceneResolver) Files(ctx context.Context, obj *models.Scene) ([]*VideoFile, error) {
	files, err := r.getFiles(ctx, obj)
	if err != nil {
		return nil, err
	}

	ret := make([]*VideoFile, len(files))

	for i, f := range files {
		ret[i] = &VideoFile{
			VideoFile: f,
		}
	}

	return ret, nil
}

func (r *sceneResolver) Rating(ctx context.Context, obj *models.Scene) (*int, error) {
	if obj.Rating != nil {
		rating := models.Rating100To5(*obj.Rating)
		return &rating, nil
	}
	return nil, nil
}

func (r *sceneResolver) Rating100(ctx context.Context, obj *models.Scene) (*int, error) {
	return obj.Rating, nil
}

func (r *sceneResolver) Paths(ctx context.Context, obj *models.Scene) (*ScenePathsType, error) {
	baseURL, _ := ctx.Value(BaseURLCtxKey).(string)
	config := manager.GetInstance().Config

	paths, err := scenePaths(ctx, config, urlbuilders.NewSceneURLBuilder(baseURL, obj), obj)
	if err != nil {
		return nil, err
	}

	// Web-only formats: use unsigned URLs (rely on cookie authentication)
	screenshotPath := paths.builder.GetScreenshotURL()
	previewPath := paths.builder.GetStreamPreviewURL()
	webpPath := paths.builder.GetStreamPreviewImageURL()
	objHash := obj.GetHash(config.GetVideoFileNamingAlgorithm())
	vttPath := paths.builder.GetSpriteVTTURL(objHash)
	spritePath := paths.builder.GetSpriteURL(objHash)
	interactiveHeatmap := paths.builder.GetInteractiveHeatmapURL()

	return &ScenePathsType{
		Screenshot:         &screenshotPath,
		Preview:            &previewPath,
		Stream:             &paths.stream,
		Webp:               &webpPath,
		Vtt:                &vttPath,
		Sprite:             &spritePath,
		Funscript:          &paths.funscript,
		InteractiveHeatmap: &interactiveHeatmap,
		Caption:            &paths.caption,
	}, nil
}

// signedScenePaths is the set of scene URLs that carry a credential.
//
// ## WHY THIS IS A SEPARATE FUNCTION
//
// #7238 fixed the funscript URL leaking `?apikey=` while the stream and caption URLs beside it were
// signed. The fix was one line -- but the line lives in a resolver method that reads its config
// through `manager.GetInstance()`, and that singleton PANICS when uninitialised with no test-only
// setter (`internal/manager/manager.go:288`: `var instance *Manager`, assigned only in init.go).
//
// So the whole function was untestable from `internal/api`: a test could not reach the decision it
// needed to check, and "the fix is obviously right" would have been the only available evidence.
// Extracting the credential-carrying URLs into this pure function makes the #7238 decision
// reachable by an ordinary test, and keeps the resolver a thin assembly of them.
//
// The alternative -- adding an exported `manager.SetInstance` for tests -- was rejected: it widens
// the production API so a test can reach one branch, and a singleton setter is exactly the seam
// through which a later test starts initialising global state for the whole package.
type signedScenePaths struct {
	stream    string
	caption   string
	funscript string

	// builder is carried so the caller can finish the web-only paths without rebuilding it.
	builder urlbuilders.SceneURLBuilder
}

// The config parameter is named `cfg`, not `config`: `config` is also the imported PACKAGE name
// in this file, so a parameter of that name shadows it and every use of the package inside the
// function body stops resolving.
func scenePaths(ctx context.Context, cfg *config.Config, builder urlbuilders.SceneURLBuilder, obj *models.Scene) (signedScenePaths, error) {
	var out signedScenePaths
	out.builder = builder

	if cfg.HasCredentials() {
		userID := session.GetCurrentUserID(ctx)
		if userID == nil {
			return out, fmt.Errorf("user ID not found")
		}

		// Sign the stream prefix
		streamURL := builder.GetStreamURL("")
		streamURL.RawQuery = signedParams(cfg, *userID, signedurl.DerivePrefix(streamURL.Path)).Encode()
		out.stream = streamURL.String()

		// Sign the caption prefix
		captionBase := builder.GetCaptionURL()
		out.caption = captionBase + "?" + signedParams(cfg, *userID, builder.GetCaptionPath()).Encode()

		// #7238 - sign the funscript prefix too.
		//
		// This was the ONE scene path that handed out `?apikey=` while the two beside it signed,
		// and the apikey can rewrite the entire database. The reason it is a real bug rather than a
		// style difference: the funscript URL is the one a CLIENT fetches -- the interactive-script
		// and TheHandy integrations load it directly -- so it is precisely the URL most likely to
		// end up in a third party's logs, a browser history, or a proxy's access log. Stream and
		// caption were already signed for exactly that reason; funscript was simply missed.
		//
		// Signed, not dropped: the path is inside `/scene/{id}/`, so `authenticateSignedRequest`
		// already accepts a signature here, and `DerivePrefix` reduces `/scene/1/funscript` to
		// itself (three segments, no extension). So this needs no new auth path -- only the call.
		funscriptURL := builder.GetFunscriptURL("")
		funscriptURL.RawQuery = signedParams(cfg, *userID,
			signedurl.DerivePrefix(funscriptURL.Path)).Encode()
		out.funscript = funscriptURL.String()
		return out, nil
	}

	// No credentials configured: there is nothing to sign against, and the instance is already
	// reachable without auth on the LAN. This is the same fallback the stream path has always had.
	apiKey := cfg.GetAPIKey()
	out.stream = builder.GetStreamURL(apiKey).String()
	out.caption = builder.GetCaptionURL()
	out.funscript = builder.GetFunscriptURL(apiKey).String()
	return out, nil
}

func (r *sceneResolver) SceneMarkers(ctx context.Context, obj *models.Scene) (ret []*models.SceneMarker, err error) {
	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.SceneMarker.FindBySceneID(ctx, obj.ID)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *sceneResolver) Captions(ctx context.Context, obj *models.Scene) (ret []*models.VideoCaption, err error) {
	primaryFile, err := r.getPrimaryFile(ctx, obj)
	if err != nil {
		return nil, err
	}
	if primaryFile == nil {
		return nil, nil
	}

	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.File.GetCaptions(ctx, primaryFile.Base().ID)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, err
}

func (r *sceneResolver) Galleries(ctx context.Context, obj *models.Scene) (ret []*models.Gallery, err error) {
	if !obj.GalleryIDs.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadGalleryIDs(ctx, r.repository.Scene)
		}); err != nil {
			return nil, err
		}
	}

	var errs []error
	ret, errs = loaders.From(ctx).GalleryByID.LoadAll(obj.GalleryIDs.List())
	return ret, firstError(errs)
}

func (r *sceneResolver) Studio(ctx context.Context, obj *models.Scene) (ret *models.Studio, err error) {
	if obj.StudioID == nil {
		return nil, nil
	}

	return loaders.From(ctx).StudioByID.Load(*obj.StudioID)
}

func (r *sceneResolver) Movies(ctx context.Context, obj *models.Scene) (ret []*SceneMovie, err error) {
	if !obj.Groups.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			qb := r.repository.Scene

			return obj.LoadGroups(ctx, qb)
		}); err != nil {
			return nil, err
		}
	}

	loader := loaders.From(ctx).GroupByID

	for _, sm := range obj.Groups.List() {
		movie, err := loader.Load(sm.GroupID)
		if err != nil {
			return nil, err
		}

		sceneIdx := sm.SceneIndex
		sceneMovie := &SceneMovie{
			Movie:      movie,
			SceneIndex: sceneIdx,
		}

		ret = append(ret, sceneMovie)
	}

	return ret, nil
}

func (r *sceneResolver) Groups(ctx context.Context, obj *models.Scene) (ret []*SceneGroup, err error) {
	if !obj.Groups.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			qb := r.repository.Scene

			return obj.LoadGroups(ctx, qb)
		}); err != nil {
			return nil, err
		}
	}

	loader := loaders.From(ctx).GroupByID

	for _, sm := range obj.Groups.List() {
		group, err := loader.Load(sm.GroupID)
		if err != nil {
			return nil, err
		}

		sceneIdx := sm.SceneIndex
		sceneGroup := &SceneGroup{
			Group:      group,
			SceneIndex: sceneIdx,
		}

		ret = append(ret, sceneGroup)
	}

	return ret, nil
}

func (r *sceneResolver) Tags(ctx context.Context, obj *models.Scene) (ret []*models.Tag, err error) {
	if !obj.TagIDs.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadTagIDs(ctx, r.repository.Scene)
		}); err != nil {
			return nil, err
		}
	}

	var errs []error
	ret, errs = loaders.From(ctx).TagByID.LoadAll(obj.TagIDs.List())
	return ret, firstError(errs)
}

func (r *sceneResolver) Performers(ctx context.Context, obj *models.Scene) (ret []*models.Performer, err error) {
	if !obj.PerformerIDs.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadPerformerIDs(ctx, r.repository.Scene)
		}); err != nil {
			return nil, err
		}
	}

	var errs []error
	ret, errs = loaders.From(ctx).PerformerByID.LoadAll(obj.PerformerIDs.List())
	return ret, firstError(errs)
}

func (r *sceneResolver) StashIds(ctx context.Context, obj *models.Scene) (ret []*models.StashID, err error) {
	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		return obj.LoadStashIDs(ctx, r.repository.Scene)
	}); err != nil {
		return nil, err
	}

	return stashIDsSliceToPtrSlice(obj.StashIDs.List()), nil
}

func (r *sceneResolver) SceneStreams(ctx context.Context, obj *models.Scene) ([]*manager.SceneStreamEndpoint, error) {
	// load the primary file into the scene
	_, err := r.getPrimaryFile(ctx, obj)
	if err != nil {
		return nil, err
	}

	config := manager.GetInstance().Config

	baseURL, _ := ctx.Value(BaseURLCtxKey).(string)
	builder := urlbuilders.NewSceneURLBuilder(baseURL, obj)

	// Build the base stream URL with signing params or apikey
	streamURL := builder.GetStreamURL("")
	if config.HasCredentials() {
		userID := session.GetCurrentUserID(ctx)
		if userID == nil {
			return nil, fmt.Errorf("user ID not found")
		}
		streamURL.RawQuery = signedParams(config, *userID, signedurl.DerivePrefix(streamURL.Path)).Encode()
	} else {
		apiKey := config.GetAPIKey()
		if apiKey != "" {
			v := streamURL.Query()
			v.Set("apikey", apiKey)
			streamURL.RawQuery = v.Encode()
		}
	}

	return manager.GetSceneStreamPaths(obj, streamURL, config.GetMaxStreamingTranscodeSize())
}

func (r *sceneResolver) Interactive(ctx context.Context, obj *models.Scene) (bool, error) {
	primaryFile, err := r.getPrimaryFile(ctx, obj)
	if err != nil {
		return false, err
	}
	if primaryFile == nil {
		return false, nil
	}

	return primaryFile.Interactive, nil
}

func (r *sceneResolver) InteractiveSpeed(ctx context.Context, obj *models.Scene) (*int, error) {
	primaryFile, err := r.getPrimaryFile(ctx, obj)
	if err != nil {
		return nil, err
	}
	if primaryFile == nil {
		return nil, nil
	}

	return primaryFile.InteractiveSpeed, nil
}

func (r *sceneResolver) URL(ctx context.Context, obj *models.Scene) (*string, error) {
	if !obj.URLs.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadURLs(ctx, r.repository.Scene)
		}); err != nil {
			return nil, err
		}
	}

	urls := obj.URLs.List()
	if len(urls) == 0 {
		return nil, nil
	}

	return &urls[0], nil
}

func (r *sceneResolver) Urls(ctx context.Context, obj *models.Scene) ([]string, error) {
	if !obj.URLs.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadURLs(ctx, r.repository.Scene)
		}); err != nil {
			return nil, err
		}
	}

	return obj.URLs.List(), nil
}

func (r *sceneResolver) OCounter(ctx context.Context, obj *models.Scene) (*int, error) {
	ret, err := loaders.From(ctx).SceneOCount.Load(obj.ID)
	if err != nil {
		return nil, err
	}

	return &ret, nil
}

func (r *sceneResolver) LastPlayedAt(ctx context.Context, obj *models.Scene) (*time.Time, error) {
	ret, err := loaders.From(ctx).SceneLastPlayed.Load(obj.ID)
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *sceneResolver) PlayCount(ctx context.Context, obj *models.Scene) (*int, error) {
	ret, err := loaders.From(ctx).ScenePlayCount.Load(obj.ID)
	if err != nil {
		return nil, err
	}

	return &ret, nil
}

func (r *sceneResolver) PlayHistory(ctx context.Context, obj *models.Scene) ([]*time.Time, error) {
	ret, err := loaders.From(ctx).ScenePlayHistory.Load(obj.ID)
	if err != nil {
		return nil, err
	}

	// convert to pointer slice
	ptrRet := make([]*time.Time, len(ret))
	for i, t := range ret {
		tt := t
		ptrRet[i] = &tt
	}

	return ptrRet, nil
}

func (r *sceneResolver) OHistory(ctx context.Context, obj *models.Scene) ([]*time.Time, error) {
	ret, err := loaders.From(ctx).SceneOHistory.Load(obj.ID)
	if err != nil {
		return nil, err
	}

	// convert to pointer slice
	ptrRet := make([]*time.Time, len(ret))
	for i, t := range ret {
		tt := t
		ptrRet[i] = &tt
	}

	return ptrRet, nil
}

func (r *sceneResolver) CustomFields(ctx context.Context, obj *models.Scene) (map[string]interface{}, error) {
	m, err := loaders.From(ctx).SceneCustomFields.Load(obj.ID)
	if err != nil {
		return nil, err
	}

	if m == nil {
		return make(map[string]interface{}), nil
	}

	return m, nil
}
