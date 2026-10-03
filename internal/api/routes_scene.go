package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/internal/static"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/file/video"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/paths"
	"github.com/stashapp/stash/pkg/utils"
)

type SceneFinder interface {
	models.SceneGetter

	FindByChecksum(ctx context.Context, checksum string) ([]*models.Scene, error)
	FindByOSHash(ctx context.Context, oshash string) ([]*models.Scene, error)
	GetCover(ctx context.Context, sceneID int) ([]byte, error)
}

type SceneMarkerFinder interface {
	models.SceneMarkerGetter
	FindBySceneID(ctx context.Context, sceneID int) ([]*models.SceneMarker, error)
}

type SceneMarkerTagFinder interface {
	models.TagGetter
	FindBySceneMarkerID(ctx context.Context, sceneMarkerID int) ([]*models.Tag, error)
}

type CaptionFinder interface {
	GetCaptions(ctx context.Context, fileID models.FileID) ([]*models.VideoCaption, error)
}

type sceneRoutes struct {
	routes
	sceneFinder       SceneFinder
	fileGetter        models.FileGetter
	captionFinder     CaptionFinder
	sceneMarkerFinder SceneMarkerFinder
	tagFinder         SceneMarkerTagFinder
}

func (rs sceneRoutes) Routes() chi.Router {
	r := chi.NewRouter()

	r.Route("/{sceneId}", func(r chi.Router) {
		r.Use(rs.SceneCtx)

		// streaming endpoints
		r.Get("/stream", rs.StreamDirect)
		r.Get("/stream.mp4", rs.StreamMp4)
		r.Get("/stream.webm", rs.StreamWebM)
		r.Get("/stream.mkv", rs.StreamMKV)
		r.Get("/stream.m3u8", rs.StreamHLS)
		r.Get("/stream.m3u8/{segment}.ts", rs.StreamHLSSegment)
		r.Get("/stream.mpd", rs.StreamDASH)
		r.Get("/stream.mpd/{segment}_v.webm", rs.StreamDASHVideoSegment)
		r.Get("/stream.mpd/{segment}_a.webm", rs.StreamDASHAudioSegment)

		r.Get("/screenshot", rs.Screenshot)
		r.Get("/preview", rs.Preview)
		r.Get("/webp", rs.Webp)
		r.Get("/vtt/chapter", rs.VttChapter)
		r.Get("/vtt/thumbs", rs.VttThumbs)
		r.Get("/vtt/sprite", rs.VttSprite)
		r.Get("/funscript", rs.Funscript)
		r.Get("/interactive_csv", rs.InteractiveCSV)
		r.Get("/interactive_heatmap", rs.InteractiveHeatmap)
		r.Get("/caption", rs.CaptionLang)

		r.Get("/scene_marker/{sceneMarkerId}/stream", rs.SceneMarkerStream)
		r.Get("/scene_marker/{sceneMarkerId}/preview", rs.SceneMarkerPreview)
		r.Get("/scene_marker/{sceneMarkerId}/screenshot", rs.SceneMarkerScreenshot)
	})
	// These two are OUTSIDE the /{sceneId} block, so they never see SceneCtx
	// and never saw its gate -- a hole, found by listing every media route
	// and recording which middleware each one passes rather than by reading
	// the handlers (all of which looked correct). They serve a generated
	// sprite and thumbnail strip keyed only by a hash, so the hash is
	// resolved to a scene and the scene is checked.
	// See stashforge_scene_hash_routes.go.
	r.Route("/{sceneHash}*", func(r chi.Router) {
		r.Use(sceneHashCtx)
		r.Get("_thumbs.vtt", rs.VttThumbs)
		r.Get("_sprite.jpg", rs.VttSprite)
	})

	return r
}

func (rs sceneRoutes) StreamDirect(w http.ResponseWriter, r *http.Request) {
	scene := r.Context().Value(sceneKey).(*models.Scene)

	// #3530 - a ranged scene CANNOT be served from the file directly.
	//
	// StreamSceneDirect ends in `http.ServeFile(w, r, fp)`, which serves the WHOLE file. It has
	// no notion of a time window, and the obvious workaround does not work either: an HTTP
	// Range header addresses BYTES, and an MP4's byte offset for time T is not (T / duration)
	// -- it depends on the container's variable-bitrate layout, so a time-to-byte guess lands
	// mid-GOP and the player starts at the wrong frame with the wrong audio.
	//
	// So the two honest options are to transcode the window, or to say we cannot. Silently
	// serving the whole file is the bug this fixes: a 60s scene of a 2-hour file plays 2 hours
	// and nothing about the response indicates a problem.
	//
	//	transcoding available -> 307 to the same scene's /stream.mp4, which honours the window
	//	transcoding disabled     -> 409, with the window in the body
	//
	// An unranged scene is unaffected and still serves directly -- that is the common case and
	// it must keep working, including with transcoding off.
	if isRangedScene(scene) {
		if manager.GetInstance().StreamManager == nil {
			http.Error(w,
				fmt.Sprintf("scene %d is a window (%s) of its file, and serving a window directly "+
					"is not possible: it would play the whole file. Enable transcoding to play it.",
					scene.ID, describeWindow(scene)),
				http.StatusConflict)
			return
		}

		// 307, not 302: the method and body must be preserved, and a range is a property of
		// the resource, not a temporary detour. Signed-URL credentials are carried across in
		// the query, so the redirect target stays authorised.
		target := *r.URL
		target.Path += ".mp4"
		http.Redirect(w, r, target.String(), http.StatusTemporaryRedirect)
		return
	}

	ss := manager.SceneServer{
		TxnManager:       rs.txnManager,
		SceneCoverGetter: rs.sceneFinder,
	}
	ss.StreamSceneDirect(scene, w, r)
}

// #3530 - helpers for the direct-stream decision above.

// isRangedScene reports whether the scene is a WINDOW of its file rather than the whole thing.
// Either end being set makes it a window: a scene with only an end is the head of the file, which
// is still not the whole file.
//
// nil means "no window", which is why this cannot be `StartTime != nil && *StartTime > 0` -- a
// window starting at 0 is still a window.
func isRangedScene(scene *models.Scene) bool {
	pf := scenePrimaryFile(scene)
	if pf == nil {
		return false
	}
	return pf.StartTime != nil || pf.EndTime != nil
}

// scenePrimaryFile is Primary() WITHOUT the panic.
//
// RelatedVideoFiles.Primary() panics by contract when the relationship has not been loaded
// ("relationship has not been loaded"), and a handler reached before the file is loaded would
// take the process down rather than return a 500. Every other route gets this for free because
// SceneCtx loads the files first, but these helpers are also called from tests with a hand-built
// scene, so the check belongs HERE rather than being assumed by a caller.
//
// Measured: `TestASceneWithNoFileIsNotWindowed` panicked on `&models.Scene{ID: 7}` before this
// existed. A missing file is #3526's normal 404 case, so "no file" is a state this code must
// survive.
func scenePrimaryFile(scene *models.Scene) *models.VideoFile {
	if scene == nil || !scene.Files.PrimaryLoaded() {
		return nil
	}
	return scene.Files.Primary()
}

// describeWindow renders a scene's window for an error body, so a user hitting the 409 can see
// which window was refused rather than just "not possible".
func describeWindow(scene *models.Scene) string {
	pf := scenePrimaryFile(scene)
	if pf == nil {
		return "unknown"
	}
	switch {
	case pf.StartTime != nil && pf.EndTime != nil:
		return fmt.Sprintf("%.1fs-%.1fs", *pf.StartTime, *pf.EndTime)
	case pf.StartTime != nil:
		return fmt.Sprintf("from %.1fs", *pf.StartTime)
	default:
		return fmt.Sprintf("until %.1fs", *pf.EndTime)
	}
}

func (rs sceneRoutes) StreamMp4(w http.ResponseWriter, r *http.Request) {
	rs.streamTranscode(w, r, ffmpeg.StreamTypeMP4)
}

func (rs sceneRoutes) StreamWebM(w http.ResponseWriter, r *http.Request) {
	rs.streamTranscode(w, r, ffmpeg.StreamTypeWEBM)
}

func (rs sceneRoutes) StreamMKV(w http.ResponseWriter, r *http.Request) {
	// only allow mkv streaming if the scene container is an mkv already
	scene := r.Context().Value(sceneKey).(*models.Scene)

	pf := scene.Files.Primary()
	if pf == nil {
		return
	}

	container, err := manager.GetVideoFileContainer(pf)
	if err != nil {
		logger.Errorf("[transcode] error getting container: %v", err)
	}

	if container != ffmpeg.Matroska {
		w.WriteHeader(http.StatusBadRequest)
		if _, err := w.Write([]byte("not an mkv file")); err != nil {
			logger.Warnf("[stream] error writing to stream: %v", err)
		}
		return
	}

	rs.streamTranscode(w, r, ffmpeg.StreamTypeMKV)
}

// #3530 - resolve the window a scene should play, letting an explicit query param win over the
// stored one.
//
// Returned as (start, end) in SECONDS FROM THE START OF THE FILE, where end == 0 means "to the end
// of the file" and is encoded downstream as the ABSENCE of -t.
//
// ## Why the param overrides the stored window
//
// The player's scrubber appends ?start= as the user seeks WITHIN a scene, so a backend that always
// used the stored window would break seeking for every ranged scene. That is a worse bug than the
// one being fixed -- the stored window is a default for the INITIAL request, not a lock on every
// request. Only an ABSENT param falls back.
//
// ## Why nil is not collapsed to 0 at this boundary
//
// The stored window is nil when the scene was never ranged, whereas 0 is a meaningful offset (the
// head of the file). They mean different things, and only the latter should produce -ss at all --
// so the distinction is kept until makeStreamArgs, which decides.
//
// ## Why a malformed param falls back rather than zeroing
//
// The code this replaced was `ss, _ := strconv.ParseFloat(r.Form.Get("start"), 64)`, which turns
// "abc" into 0 and seeks to the beginning of the file -- a silent wrong answer. Falling back to
// the stored window is closer to what the client meant, and if there is no stored window the scene
// plays whole rather than jumping to the head.
//
// One function rather than two so start and end cannot be resolved by different rules: a caller
// that forgot one of a pair is a bug this shape makes impossible.
func resolveSceneWindow(r *http.Request, f *models.VideoFile) (start, end float64) {
	var qs url.Values
	if r != nil {
		// ParseForm has already been called by the caller; a second call is a no-op and
		// returns the same values, so reading r.Form here cannot fail differently.
		qs = r.URL.Query()
	}

	if f != nil && f.StartTime != nil {
		start = *f.StartTime
	}
	if f != nil && f.EndTime != nil {
		end = *f.EndTime
	}

	if v, err := strconv.ParseFloat(qs.Get("start"), 64); err == nil {
		start = v
	}
	if v, err := strconv.ParseFloat(qs.Get("end"), 64); err == nil {
		end = v
	}

	// An inverted window from the client is left for makeStreamArgs to drop. It is NOT
	// normalised here, because doing so in two places is how the two drift apart -- the
	// same reason sceneFileRanges is the only place a stored window is clamped.
	return start, end
}

func (rs sceneRoutes) streamTranscode(w http.ResponseWriter, r *http.Request, streamType ffmpeg.StreamFormat) {
	scene := r.Context().Value(sceneKey).(*models.Scene)

	streamManager := manager.GetInstance().StreamManager
	if streamManager == nil {
		http.Error(w, "Live transcoding disabled", http.StatusServiceUnavailable)
		return
	}

	f := scene.Files.Primary()
	if f == nil {
		return
	}

	if err := r.ParseForm(); err != nil {
		logger.Warnf("[transcode] error parsing query form: %v", err)
	}

	resolution := r.Form.Get("resolution")

	// #3530 - a ranged scene plays its WINDOW, not the whole file.
	//
	// The scene's stored window is the DEFAULT and a query param OVERRIDES it. That direction
	// matters and is not arbitrary: the player's scrubber appends ?start= as the user seeks
	// WITHIN a scene, so always ignoring query params would break seeking for every ranged
	// scene -- a worse bug than the one being fixed. Only an ABSENT param falls back.
	ss, se := resolveSceneWindow(r, f)

	options := ffmpeg.TranscodeOptions{
		StreamType: streamType,
		VideoFile:  f,
		Resolution: resolution,
		StartTime:  ss,
		EndTime:    se,
	}

	logger.Debugf("[transcode] streaming scene %d as %s", scene.ID, streamType.MimeType)
	streamManager.ServeTranscode(w, r, options)
}

func (rs sceneRoutes) StreamHLS(w http.ResponseWriter, r *http.Request) {
	rs.streamManifest(w, r, ffmpeg.StreamTypeHLS, "HLS")
}

func (rs sceneRoutes) StreamDASH(w http.ResponseWriter, r *http.Request) {
	rs.streamManifest(w, r, ffmpeg.StreamTypeDASHVideo, "DASH")
}

func (rs sceneRoutes) streamManifest(w http.ResponseWriter, r *http.Request, streamType *ffmpeg.StreamType, logName string) {
	scene := r.Context().Value(sceneKey).(*models.Scene)

	streamManager := manager.GetInstance().StreamManager
	if streamManager == nil {
		http.Error(w, "Live transcoding disabled", http.StatusServiceUnavailable)
		return
	}

	f := scene.Files.Primary()
	if f == nil {
		return
	}

	if err := r.ParseForm(); err != nil {
		logger.Warnf("[transcode] error parsing query form: %v", err)
	}

	resolution := r.Form.Get("resolution")

	logger.Debugf("[transcode] returning %s manifest for scene %d", logName, scene.ID)
	streamManager.ServeManifest(w, r, streamType, f, resolution)
}

func (rs sceneRoutes) StreamHLSSegment(w http.ResponseWriter, r *http.Request) {
	rs.streamSegment(w, r, ffmpeg.StreamTypeHLS)
}

func (rs sceneRoutes) StreamDASHVideoSegment(w http.ResponseWriter, r *http.Request) {
	rs.streamSegment(w, r, ffmpeg.StreamTypeDASHVideo)
}

func (rs sceneRoutes) StreamDASHAudioSegment(w http.ResponseWriter, r *http.Request) {
	rs.streamSegment(w, r, ffmpeg.StreamTypeDASHAudio)
}

func (rs sceneRoutes) streamSegment(w http.ResponseWriter, r *http.Request, streamType *ffmpeg.StreamType) {
	scene := r.Context().Value(sceneKey).(*models.Scene)

	streamManager := manager.GetInstance().StreamManager
	if streamManager == nil {
		http.Error(w, "Live transcoding disabled", http.StatusServiceUnavailable)
		return
	}

	f := scene.Files.Primary()
	if f == nil {
		return
	}

	if err := r.ParseForm(); err != nil {
		logger.Warnf("[transcode] error parsing query form: %v", err)
	}

	sceneHash := scene.GetHash(config.GetInstance().GetVideoFileNamingAlgorithm())

	segment := chi.URLParam(r, "segment")
	resolution := r.Form.Get("resolution")

	options := ffmpeg.StreamOptions{
		StreamType: streamType,
		VideoFile:  f,
		Resolution: resolution,
		Hash:       sceneHash,
		Segment:    segment,
	}

	streamManager.ServeSegment(w, r, options)
}

func (rs sceneRoutes) Screenshot(w http.ResponseWriter, r *http.Request) {
	// if default flag is set, return the default image
	if r.URL.Query().Get("default") == "true" {
		utils.ServeImage(w, r, static.ReadAll(static.DefaultSceneImage))
		return
	}

	scene := r.Context().Value(sceneKey).(*models.Scene)

	ss := manager.SceneServer{
		TxnManager:       rs.txnManager,
		SceneCoverGetter: rs.sceneFinder,
	}
	ss.ServeScreenshot(scene, w, r)
}

func (rs sceneRoutes) Preview(w http.ResponseWriter, r *http.Request) {
	scene := r.Context().Value(sceneKey).(*models.Scene)
	// #3530 - GeneratedChecksum, not GetHash: the preview is generated from inside the scene's
	// window, so two scenes of ONE file must not share a cache entry. An unranged scene's key is
	// byte-identical to its hash, so nothing existing moves.
	//
	// The LEGACY half uses the same key deliberately. A suffixed checksum fails
	// isValidGeneratedChecksum, so a windowed scene has no legacy path at all -- which is the
	// right answer (there is no pre-#3530 preview of a window) and is what stops a windowed scene
	// falling back onto another scene's file.
	sceneHash := models.GeneratedChecksum(*scene, config.GetInstance().GetVideoFileNamingAlgorithm())
	sp := manager.GetInstance().Paths.Scene
	filepath := paths.ResolveGeneratedFile(sp.GetVideoPreviewPath(sceneHash), sp.GetLegacyVideoPreviewPath(sceneHash))

	utils.ServeStaticFile(w, r, filepath)
}

func (rs sceneRoutes) Webp(w http.ResponseWriter, r *http.Request) {
	scene := r.Context().Value(sceneKey).(*models.Scene)
	// #3530 - GeneratedChecksum, as in Preview. See there for why the legacy half shares the key.
	sceneHash := models.GeneratedChecksum(*scene, config.GetInstance().GetVideoFileNamingAlgorithm())
	sp := manager.GetInstance().Paths.Scene
	filepath := paths.ResolveGeneratedFile(sp.GetWebpPreviewPath(sceneHash), sp.GetLegacyWebpPreviewPath(sceneHash))

	utils.ServeStaticFile(w, r, filepath)
}

func (rs sceneRoutes) getChapterVttTitle(r *http.Request, marker *models.SceneMarker) (*string, error) {
	if marker.Title != "" {
		return &marker.Title, nil
	}

	var title string
	if err := rs.withReadTxn(r, func(ctx context.Context) error {
		qb := rs.tagFinder
		primaryTag, err := qb.Find(ctx, marker.PrimaryTagID)
		if err != nil {
			return err
		}

		title = primaryTag.Name

		tags, err := qb.FindBySceneMarkerID(ctx, marker.ID)
		if err != nil {
			return err
		}

		for _, t := range tags {
			title += ", " + t.Name
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return &title, nil
}

func (rs sceneRoutes) VttChapter(w http.ResponseWriter, r *http.Request) {
	scene := r.Context().Value(sceneKey).(*models.Scene)
	var sceneMarkers []*models.SceneMarker
	readTxnErr := rs.withReadTxn(r, func(ctx context.Context) error {
		var err error
		sceneMarkers, err = rs.sceneMarkerFinder.FindBySceneID(ctx, scene.ID)
		return err
	})
	if errors.Is(readTxnErr, context.Canceled) {
		return
	}
	if readTxnErr != nil {
		logger.Warnf("read transaction error on fetch scene markers: %v", readTxnErr)
		http.Error(w, readTxnErr.Error(), http.StatusInternalServerError)
		return
	}

	vttLines := []string{"WEBVTT", ""}
	for i, marker := range sceneMarkers {
		vttLines = append(vttLines, strconv.Itoa(i+1))
		time := utils.GetVTTTime(marker.Seconds)
		vttLines = append(vttLines, time+" --> "+time)

		vttTitle, err := rs.getChapterVttTitle(r, marker)
		if errors.Is(err, context.Canceled) {
			return
		}
		if err != nil {
			logger.Warnf("read transaction error on fetch scene marker title: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		vttLines = append(vttLines, *vttTitle)
		vttLines = append(vttLines, "")
	}
	vtt := strings.Join(vttLines, "\n")

	w.Header().Set("Content-Type", "text/vtt")
	utils.ServeStaticContent(w, r, []byte(vtt))
}

// #3530 - the sprite and its VTT are generated from INSIDE the scene's window, so the key is
// GeneratedChecksum, exactly as in Preview. The scene on the context has its files loaded by
// SceneCtx (and by sceneHashCtx's resolver path), so the window is available here.
//
// The two halves share the key deliberately: a suffixed checksum fails isValidGeneratedChecksum and
// therefore has no legacy path at all, which is the right answer -- there is no pre-#3530 sprite of
// a window -- and is what stops a windowed scene falling back onto the UNWINDOWED sprite, which is
// a sprite of the wrong footage and would look entirely correct.
func spriteSceneHash(r *http.Request) string {
	scene, ok := r.Context().Value(sceneKey).(*models.Scene)
	if ok && scene != nil {
		return models.GeneratedChecksum(*scene, config.GetInstance().GetVideoFileNamingAlgorithm())
	}
	// The /{sceneHash}* form. A suffixed checksum carries '_' and '.', both legal in a chi URL
	// segment, so a windowed sprite is reachable by its own URL too.
	return chi.URLParam(r, "sceneHash")
}

func (rs sceneRoutes) VttThumbs(w http.ResponseWriter, r *http.Request) {
	sceneHash := spriteSceneHash(r)
	sp := manager.GetInstance().Paths.Scene
	filepath := paths.ResolveGeneratedFile(sp.GetSpriteVttFilePath(sceneHash), sp.GetLegacySpriteVttFilePath(sceneHash))

	w.Header().Set("Content-Type", "text/vtt")
	utils.ServeStaticFile(w, r, filepath)
}

func (rs sceneRoutes) VttSprite(w http.ResponseWriter, r *http.Request) {
	sceneHash := spriteSceneHash(r)
	sp := manager.GetInstance().Paths.Scene
	filepath := paths.ResolveGeneratedFile(sp.GetSpriteImageFilePath(sceneHash), sp.GetLegacySpriteImageFilePath(sceneHash))

	utils.ServeStaticFile(w, r, filepath)
}

func (rs sceneRoutes) Funscript(w http.ResponseWriter, r *http.Request) {
	s := r.Context().Value(sceneKey).(*models.Scene)
	filepath := video.GetFunscriptPath(s.Path)

	utils.ServeStaticFile(w, r, filepath)
}

func (rs sceneRoutes) InteractiveCSV(w http.ResponseWriter, r *http.Request) {
	s := r.Context().Value(sceneKey).(*models.Scene)
	filepath := video.GetFunscriptPath(s.Path)

	// TheHandy directly only accepts interactive CSVs
	csvBytes, err := manager.ConvertFunscriptToCSV(filepath)

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	utils.ServeStaticContent(w, r, csvBytes)
}

func (rs sceneRoutes) InteractiveHeatmap(w http.ResponseWriter, r *http.Request) {
	scene := r.Context().Value(sceneKey).(*models.Scene)
	sceneHash := scene.GetHash(config.GetInstance().GetVideoFileNamingAlgorithm())
	sp := manager.GetInstance().Paths.Scene
	filepath := paths.ResolveGeneratedFile(sp.GetInteractiveHeatmapPath(sceneHash), sp.GetLegacyInteractiveHeatmapPath(sceneHash))

	utils.ServeStaticFile(w, r, filepath)
}

func (rs sceneRoutes) Caption(w http.ResponseWriter, r *http.Request, lang string, ext string) {
	s := r.Context().Value(sceneKey).(*models.Scene)

	var captions []*models.VideoCaption
	readTxnErr := rs.withReadTxn(r, func(ctx context.Context) error {
		var err error
		primaryFile := s.Files.Primary()
		if primaryFile == nil {
			return nil
		}

		captions, err = rs.captionFinder.GetCaptions(ctx, primaryFile.Base().ID)

		return err
	})
	if errors.Is(readTxnErr, context.Canceled) {
		return
	}
	if readTxnErr != nil {
		logger.Warnf("read transaction error on fetch scene captions: %v", readTxnErr)
		http.Error(w, readTxnErr.Error(), http.StatusInternalServerError)
		return
	}

	for _, caption := range captions {
		if lang != caption.LanguageCode || ext != caption.CaptionType {
			continue
		}

		sub, err := video.ReadSubs(caption.Path(s.Path))
		if err != nil {
			logger.Warnf("error while reading subs: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var buf bytes.Buffer

		err = sub.WriteToWebVTT(&buf)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/vtt")
		utils.ServeStaticContent(w, r, buf.Bytes())
		return
	}
}

func (rs sceneRoutes) CaptionLang(w http.ResponseWriter, r *http.Request) {
	// serve caption based on lang query param, if provided
	if err := r.ParseForm(); err != nil {
		logger.Warnf("[caption] error parsing query form: %v", err)
	}

	l := r.Form.Get("lang")
	ext := r.Form.Get("type")
	rs.Caption(w, r, l, ext)
}

func (rs sceneRoutes) SceneMarkerStream(w http.ResponseWriter, r *http.Request) {
	scene := r.Context().Value(sceneKey).(*models.Scene)
	sceneHash := scene.GetHash(config.GetInstance().GetVideoFileNamingAlgorithm())
	sceneMarkerID, _ := strconv.Atoi(chi.URLParam(r, "sceneMarkerId"))
	var sceneMarker *models.SceneMarker
	readTxnErr := rs.withReadTxn(r, func(ctx context.Context) error {
		var err error
		sceneMarker, err = rs.sceneMarkerFinder.Find(ctx, sceneMarkerID)
		return err
	})
	if errors.Is(readTxnErr, context.Canceled) {
		return
	}
	if readTxnErr != nil {
		logger.Warnf("read transaction error on fetch scene marker: %v", readTxnErr)
		http.Error(w, readTxnErr.Error(), http.StatusInternalServerError)
		return
	}

	if sceneMarker == nil {
		http.Error(w, http.StatusText(404), 404)
		return
	}

	filepath := manager.GetInstance().Paths.SceneMarkers.GetVideoPreviewPath(sceneHash, int(sceneMarker.Seconds))
	utils.ServeStaticFile(w, r, filepath)
}

func (rs sceneRoutes) SceneMarkerPreview(w http.ResponseWriter, r *http.Request) {
	scene := r.Context().Value(sceneKey).(*models.Scene)
	sceneHash := scene.GetHash(config.GetInstance().GetVideoFileNamingAlgorithm())
	sceneMarkerID, _ := strconv.Atoi(chi.URLParam(r, "sceneMarkerId"))
	var sceneMarker *models.SceneMarker
	readTxnErr := rs.withReadTxn(r, func(ctx context.Context) error {
		var err error
		sceneMarker, err = rs.sceneMarkerFinder.Find(ctx, sceneMarkerID)
		return err
	})
	if errors.Is(readTxnErr, context.Canceled) {
		return
	}
	if readTxnErr != nil {
		logger.Warnf("read transaction error on fetch scene marker preview: %v", readTxnErr)
		http.Error(w, readTxnErr.Error(), http.StatusInternalServerError)
		return
	}

	if sceneMarker == nil {
		http.Error(w, http.StatusText(404), 404)
		return
	}

	filepath := manager.GetInstance().Paths.SceneMarkers.GetWebpPreviewPath(sceneHash, int(sceneMarker.Seconds))

	// If the image doesn't exist, send the placeholder
	exists, _ := fsutil.FileExists(filepath)
	if !exists {
		w.Header().Set("Content-Type", "image/png")
		utils.ServeStaticContent(w, r, utils.PendingGenerateResource)
	} else {
		utils.ServeStaticFile(w, r, filepath)
	}
}

func (rs sceneRoutes) SceneMarkerScreenshot(w http.ResponseWriter, r *http.Request) {
	scene := r.Context().Value(sceneKey).(*models.Scene)
	sceneHash := scene.GetHash(config.GetInstance().GetVideoFileNamingAlgorithm())
	sceneMarkerID, _ := strconv.Atoi(chi.URLParam(r, "sceneMarkerId"))
	var sceneMarker *models.SceneMarker
	readTxnErr := rs.withReadTxn(r, func(ctx context.Context) error {
		var err error
		sceneMarker, err = rs.sceneMarkerFinder.Find(ctx, sceneMarkerID)
		return err
	})
	if errors.Is(readTxnErr, context.Canceled) {
		return
	}
	if readTxnErr != nil {
		logger.Warnf("read transaction error on fetch scene marker screenshot: %v", readTxnErr)
		http.Error(w, readTxnErr.Error(), http.StatusInternalServerError)
		return
	}

	if sceneMarker == nil {
		http.Error(w, http.StatusText(404), 404)
		return
	}

	filepath := manager.GetInstance().Paths.SceneMarkers.GetScreenshotPath(sceneHash, int(sceneMarker.Seconds))

	// If the image doesn't exist, send the placeholder
	exists, _ := fsutil.FileExists(filepath)
	if !exists {
		w.Header().Set("Content-Type", "image/png")
		utils.ServeStaticContent(w, r, utils.PendingGenerateResource)
	} else {
		utils.ServeStaticFile(w, r, filepath)
	}
}

func (rs sceneRoutes) SceneCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sceneID, err := strconv.Atoi(chi.URLParam(r, "sceneId"))
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}

		var scene *models.Scene
		_ = rs.withReadTxn(r, func(ctx context.Context) error {
			qb := rs.sceneFinder
			scene, _ = qb.Find(ctx, sceneID)

			if scene != nil {
				if err := scene.LoadPrimaryFile(ctx, rs.fileGetter); err != nil {
					if !errors.Is(err, context.Canceled) {
						logger.Errorf("error loading primary file for scene %d: %v", sceneID, err)
					}
					// set scene to nil so that it doesn't try to use the primary file
					scene = nil
				}
			}

			return nil
		})
		if scene == nil {
			http.Error(w, http.StatusText(404), 404)
			return
		}

		// §6.4's gate, BEFORE the handler opens the file. One call here
		// covers every media route under this middleware, so a route added
		// later cannot forget it. See stashforge_media_gate.go.
		if !allowMedia(w, r, collab.TargetScene, int64(scene.ID)) {
			return
		}

		ctx := context.WithValue(r.Context(), sceneKey, scene)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
