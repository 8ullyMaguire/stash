package manager

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"path/filepath"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/internal/static"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/image"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/paths"
	"github.com/stashapp/stash/pkg/txn"
	"github.com/stashapp/stash/pkg/utils"
)

func KillRunningStreams(scene *models.Scene, fileNamingAlgo models.HashAlgorithm) {
	instance.ReadLockManager.Cancel(scene.Path)

	sceneHash := scene.GetHash(fileNamingAlgo)

	if sceneHash == "" {
		return
	}

	// Must resolve the same way the streamer resolved it, or the cancel
	// targets a path nothing is reading. (stash#2824)
	sp := GetInstance().Paths.Scene
	transcodePath := paths.ResolveGeneratedFile(sp.GetTranscodePath(sceneHash), sp.GetLegacyTranscodePath(sceneHash))
	instance.ReadLockManager.Cancel(transcodePath)
}

type SceneCoverGetter interface {
	GetCover(ctx context.Context, sceneID int) ([]byte, error)
}

type SceneServer struct {
	TxnManager       txn.Manager
	SceneCoverGetter SceneCoverGetter
}

func (s *SceneServer) StreamSceneDirect(scene *models.Scene, w http.ResponseWriter, r *http.Request) {
	// #3526 - return 404 if the scene does not have any files
	if scene.Path == "" {
		http.Error(w, http.StatusText(404), 404)
		return
	}

	sceneHash := scene.GetHash(config.GetInstance().GetVideoFileNamingAlgorithm())

	fp := GetInstance().Paths.Scene.GetStreamPath(scene.Path, sceneHash)
	streamRequestCtx := ffmpeg.NewStreamRequestContext(w, r)

	// #2579 - hijacking and closing the connection here causes video playback to fail in Safari
	// We trust that the request context will be closed, so we don't need to call Cancel on the
	// returned context here.
	_ = GetInstance().ReadLockManager.ReadLock(streamRequestCtx, fp)
	_, filename := filepath.Split(fp)
	contentDisposition := mime.FormatMediaType("inline", map[string]string{"filename": filename})
	w.Header().Set("Content-Disposition", contentDisposition)
	http.ServeFile(w, r, fp)
}

func (s *SceneServer) ServeScreenshot(scene *models.Scene, w http.ResponseWriter, r *http.Request) {
	var cover []byte
	readTxnErr := txn.WithReadTxn(r.Context(), s.TxnManager, func(ctx context.Context) error {
		var err error
		cover, err = s.SceneCoverGetter.GetCover(ctx, scene.ID)
		return err
	})
	if errors.Is(readTxnErr, context.Canceled) {
		return
	}
	if readTxnErr != nil {
		logger.Warnf("read transaction error on fetch screenshot: %v", readTxnErr)
	}

	if cover == nil {
		// fallback to legacy image if present
		if scene.Path != "" {
			sceneHash := scene.GetHash(config.GetInstance().GetVideoFileNamingAlgorithm())
			filepath := GetInstance().Paths.Scene.GetLegacyScreenshotPath(sceneHash)

			// fall back to the scene image blob if the file isn't present
			screenshotExists, _ := fsutil.FileExists(filepath)
			if screenshotExists {
				if r.URL.Query().Has("t") {
					w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				http.ServeFile(w, r, filepath)
				return
			}
		}

		// fallback to default cover if none found
		cover = static.ReadAll(static.DefaultSceneImage)
	}

	utils.ServeImage(w, r, cover)
}

// DefaultSceneThumbWidth is the width a scene queue thumbnail is generated at.
//
// stash#3741. The queue renders each row at 142x80 CSS px (styles.scss:696); 320px covers that at
// roughly 2x device pixel ratio and keeps the 16:9-ish crop from going soft on a 3x screen. A single
// width, not a query parameter: one cache entry per scene means one file on disk, and letting the
// caller pick sizes would let a single page fill the thumbnail tree with variants.
const DefaultSceneThumbWidth = 320

// ServeThumbnail serves a small, width-capped copy of the scene cover, for list and queue views.
//
// stash#3741. ServeScreenshot returns the cover at its stored resolution, and the cover generator
// passes Width: 0, which pkg/ffmpeg/transcoder/screenshot.go:82 reads as "no scaling applied" -- so a
// 4K scene serves a 3840px JPEG into a 142px box. Measured at -q:v 2 on a photographic 3840x2160
// frame: 890 KiB versus 16 KiB here.
//
// Same three-step shape as the image thumbnail route (routes_image.go:55): serve the cached file if it
// is there, otherwise encode on the fly and, when configured to, write it for next time. Encoding from
// the COVER BYTES rather than re-seeking the video with ffmpeg is deliberate -- it needs no lock on the
// video file, no ffprobe round trip, and cannot be slow because the source is already decoded.
//
// Falls back to ServeScreenshot in every failure path. A queue with slightly-too-large thumbnails is
// fine; a queue with broken images is not.
func (s *SceneServer) ServeThumbnail(scene *models.Scene, w http.ResponseWriter, r *http.Request) {
	mgr := GetInstance()
	sceneHash := models.GeneratedChecksum(*scene, config.GetInstance().GetVideoFileNamingAlgorithm())

	thumbPath := mgr.Paths.Scene.GetThumbnailPath(sceneHash, DefaultSceneThumbWidth)
	if exists, _ := fsutil.FileExists(thumbPath); exists {
		// Same cache-buster convention as Screenshot: ?t=... means "immutable", its absence means
		// revalidate. The thumbnail is derived data and can be regenerated, so it is never immutable
		// on a ?t that the caller controls.
		if r.URL.Query().Has("t") {
			w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		utils.ServeStaticFile(w, r, thumbPath)
		return
	}

	// No cover in the database and no file on disk: ServeScreenshot has the default-image and
	// legacy-file fallbacks, and there is nothing to scale. Hand off rather than duplicating it.
	cover := s.coverBytes(scene, r)
	if cover == nil {
		s.ServeScreenshot(scene, w, r)
		return
	}

	data, err := image.GetThumbnailFromBytes(mgr.FFMpeg, cover, DefaultSceneThumbWidth)
	if err != nil {
		// Not fatal: the unscaled cover is still a correct image.
		logger.Debugf("#3741: scene %s thumbnail encode failed, serving full cover: %v", scene.ID, err)
		s.ServeScreenshot(scene, w, r)
		return
	}

	if mgr.Config.IsWriteImageThumbnails() {
		if err := fsutil.WriteFile(thumbPath, data); err != nil {
			logger.Debugf("#3741: could not cache scene %s thumbnail at %s: %v", scene.ID, thumbPath, err)
		}
	}

	utils.ServeImage(w, r, data)
}

// coverBytes returns the scene's stored cover, or nil if there is none. Read-only: the thumbnail path
// must never create cover rows, or browsing a queue would start writing to the database.
func (s *SceneServer) coverBytes(scene *models.Scene, r *http.Request) []byte {
	var cover []byte
	err := txn.WithReadTxn(r.Context(), s.TxnManager, func(ctx context.Context) error {
		var err error
		cover, err = s.SceneCoverGetter.GetCover(ctx, scene.ID)
		return err
	})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	if err != nil {
		logger.Warnf("read transaction error on fetch scene thumbnail source: %v", err)
	}
	return cover
}
