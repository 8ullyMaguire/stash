package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/remeh/sizedwaitgroup"
	"github.com/stashapp/stash/internal/desktop"
	"github.com/stashapp/stash/internal/dlna"
	"github.com/stashapp/stash/internal/log"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/auth"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/gallery"
	"github.com/stashapp/stash/pkg/group"
	"github.com/stashapp/stash/pkg/image"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models/paths"
	"github.com/stashapp/stash/pkg/plugin"
	"github.com/stashapp/stash/pkg/scene"
	"github.com/stashapp/stash/pkg/scraper"
	"github.com/stashapp/stash/pkg/session"
	"github.com/stashapp/stash/pkg/sqlite"
	"github.com/stashapp/stash/pkg/utils"
	"github.com/stashapp/stash/ui"
)

// Called at startup
func Initialize(cfg *config.Config, l *log.Logger) (*Manager, error) {
	ctx := context.TODO()

	db := sqlite.NewDatabase()
	repo := db.Repository()

	// start with empty paths
	mgrPaths := &paths.Paths{}

	scraperRepository := scraper.NewRepository(repo)
	scraperCache := scraper.NewCache(cfg, scraperRepository)

	pluginCache := plugin.NewCache(cfg)

	sceneService := &scene.Service{
		File:             db.File,
		Repository:       db.Scene,
		MarkerRepository: db.SceneMarker,
		PluginCache:      pluginCache,
		Paths:            mgrPaths,
		Config:           cfg,
	}

	imageService := &image.Service{
		File:       db.File,
		Repository: db.Image,
	}

	galleryService := &gallery.Service{
		Repository:   db.Gallery,
		ImageFinder:  db.Image,
		ImageService: imageService,
		File:         db.File,
		Folder:       db.Folder,
	}

	groupService := &group.Service{
		Repository: db.Group,
	}

	sceneServer := &SceneServer{
		TxnManager:       repo.TxnManager,
		SceneCoverGetter: repo.Scene,
	}

	dlnaRepository := dlna.NewRepository(repo)
	dlnaService := dlna.NewService(dlnaRepository, cfg, sceneServer, repo.Scene, cfg.GetMinimumPlayPercent())

	mgr := &Manager{
		Config: cfg,
		Logger: l,

		Paths: mgrPaths,

		ImageThumbnailGenerateWaitGroup: sizedwaitgroup.New(1),

		JobManager:      initJobManager(cfg),
		ReadLockManager: fsutil.NewReadLockManager(),

		DownloadStore: NewDownloadStore(),

		PluginCache:  pluginCache,
		ScraperCache: scraperCache,

		DLNAService: dlnaService,

		Database:   db,
		Repository: repo,

		SceneService:   sceneService,
		ImageService:   imageService,
		GalleryService: galleryService,
		GroupService:   groupService,

		scanSubs: &subscriptionManager{},
	}

	if !cfg.IsNewSystem() {
		logger.Infof("using config file: %s", cfg.GetConfigFile())

		err := cfg.Validate()
		if err != nil {
			return nil, fmt.Errorf("invalid configuration: %w", err)
		}

		if err := mgr.postInit(ctx); err != nil {
			return nil, err
		}
	} else {
		cfgFile := cfg.GetConfigFile()
		if cfgFile != "" {
			cfgFile += " "
		}

		// create temporary session store - this will be re-initialised
		// after config is complete
		mgr.SessionStore = session.NewCookieStore(cfg)

		logger.Warnf("config file %snot found. Assuming new system...", cfgFile)
	}

	instance = mgr
	return mgr, nil
}

func formatDuration(t time.Duration) string {
	switch {
	case t >= time.Minute: // 1m23s or 2h45m12s
		t = t.Round(time.Second)
	case t >= time.Second: // 45.36s
		t = t.Round(10 * time.Millisecond)
	default: // 51ms
		t = t.Round(time.Millisecond)
	}

	return t.String()
}

func initJobManager(cfg *config.Config) *job.Manager {
	ret := job.NewManager()

	// desktop notifications
	ctx := context.Background()
	c := ret.Subscribe(context.Background())
	go func() {
		for {
			select {
			case j := <-c.RemovedJob:
				if cfg.GetNotificationsEnabled() {
					cleanDesc := strings.TrimRight(j.Description, ".")

					if j.StartTime == nil {
						// Task was never started
						return
					}

					timeElapsed := j.EndTime.Sub(*j.StartTime)
					msg := fmt.Sprintf("Task \"%s\" finished in %s.", cleanDesc, formatDuration(timeElapsed))
					desktop.SendNotification("Task Finished", msg)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return ret
}

// postInit initialises the paths, caches and database after the initial
// configuration has been set. Should only be called if the configuration
// is valid.
func (s *Manager) postInit(ctx context.Context) error {
	s.RefreshConfig()

	s.SessionStore = session.NewCookieStore(s.Config)
	s.PluginCache.RegisterSessionStore(s.SessionStore)

	s.RefreshPluginCache()
	s.RefreshPluginSourceManager()

	s.RefreshScraperCache()
	s.RefreshScraperSourceManager()

	s.RefreshDLNA()

	s.SetBlobStoreOptions()

	s.writeStashIcon()

	// clear the downloads and tmp directories
	// #1021 - only clear these directories if the generated folder is non-empty
	if s.Config.GetGeneratedPath() != "" {
		const deleteTimeout = 1 * time.Second

		utils.Timeout(func() {
			if err := fsutil.EmptyDir(s.Paths.Generated.Downloads); err != nil {
				logger.Warnf("could not empty downloads directory: %v", err)
			}
			if err := fsutil.EnsureDir(s.Paths.Generated.Tmp); err != nil {
				logger.Warnf("could not create temporary directory: %v", err)
			} else {
				if err := fsutil.EmptyDir(s.Paths.Generated.Tmp); err != nil {
					logger.Warnf("could not empty temporary directory: %v", err)
				}
			}
		}, deleteTimeout, func(done chan struct{}) {
			logger.Info("Please wait. Deleting temporary files...") // print
			<-done                                                  // and wait for deletion
			logger.Info("Temporary files deleted.")
		})
	}

	if err := s.Database.Open(s.Config.GetDatabasePath()); err != nil {
		var migrationNeededErr *sqlite.MigrationNeededError
		if errors.As(err, &migrationNeededErr) {
			logger.Warn(err)
		} else {
			return err
		}
	}

	// Set the proxy if defined in config
	if s.Config.GetProxy() != "" {
		os.Setenv("HTTP_PROXY", s.Config.GetProxy())
		os.Setenv("HTTPS_PROXY", s.Config.GetProxy())
		os.Setenv("NO_PROXY", s.Config.GetNoProxy())
		logger.Info("Using HTTP proxy")
	}

	s.RefreshFFMpeg(ctx)
	s.RefreshStreamManager()

	// StashForge multi-user wiring. MUST come after Database.Open: the user
	// store reads the users table, and the factory decides between the cookie
	// store and the database store by counting the rows in it. Any earlier and
	// the count runs against an unopened database, which fails open as
	// single-user and silently never offers accounts.
	if err := s.initStashForgeAuth(); err != nil {
		return err
	}

	// Same ordering constraint: the collab stores read the edit_proposals table,
	// which does not exist until the schema is at version 94.
	s.initStashForgeCollab()

	return nil
}

// initStashForgeCollab constructs the collaboration stores.
//
// No error return, and that is deliberate. None of these constructors touch the
// database -- they build table handles -- so there is no failure mode to report,
// and inventing one would mean a caller had to handle an error that can never
// occur. The first real database access is in the resolver, where a genuine
// error has a real message.
func (s *Manager) initStashForgeCollab() {
	s.CollabProposals = sqlite.NewEditProposalStore()
	s.CollabVotes = sqlite.NewProposalVoteStore()
	s.CollabTargets = sqlite.NewCollabTargetStore()

	// The adapter is what makes collab.Proposer usable at all: collab speaks its
	// own Proposal type and its own four methods, and no row store implements
	// that interface. Without this line the governance logic is unreachable from
	// the application and only its unit tests ever run.
	s.CollabStore = sqlite.NewCollabProposalStore()
}

// initStashForgeAuth builds the StashForge auth layer and decides which session
// store this instance uses.
//
// A failure here is fatal rather than degraded. The alternative -- log a warning
// and leave the cookie store in place -- would run an instance that has accounts
// in its database while authenticating everyone as the single config user,
// which is the exact lockout this milestone was built to avoid.
func (s *Manager) initStashForgeAuth() error {
	users := sqlite.NewUserStore()
	s.UserStore = users

	factory := &auth.Factory{
		Users:    users,
		Sessions: sqlite.NewUserSessionStore(),
		Invites:  sqlite.NewInviteStore(),
		Audit:    sqlite.NewAuditStore(),
		Config:   s.Config,
	}

	store, mode, err := factory.Build(session.NewCookieStore(s.Config))
	if err != nil {
		return fmt.Errorf("StashForge auth: %w", err)
	}

	s.SessionStore = store
	s.AuthMode = mode

	// Keep the concrete multi-user store for the GraphQL resolvers. The
	// resolvers need Register/Login, which the session.Store interface does not
	// expose, and re-deriving it by type assertion at every call site is how two
	// of them end up disagreeing about whether the instance is multi-user.
	if mode == auth.ModeMultiUser {
		adapter, ok := store.(*session.HTTPAdapter)
		if !ok {
			// The factory said multi-user and handed back something that is not
			// the adapter. Refusing here beats setting s.Auth to nil and
			// discovering it as a confusing "accounts disabled" error on the
			// first registration attempt.
			return errors.New("StashForge auth: factory returned multi-user mode without the database session store")
		}

		resolver, ok := adapter.Resolver().(*auth.SessionStore)
		if !ok {
			return errors.New("StashForge auth: session adapter is not backed by the database store")
		}
		s.Auth = resolver
	}

	// The plugin cache holds the session store too, so it must be re-registered
	// after the swap. Without this a plugin's MakePluginCookie keeps writing a
	// cookie for the store that is no longer in use.
	s.PluginCache.RegisterSessionStore(store)

	return nil
}

func (s *Manager) writeStashIcon() {
	iconPath := filepath.Join(s.Config.GetConfigPath(), "icon.png")
	err := os.WriteFile(iconPath, ui.FaviconProvider.GetFaviconPng(), 0644)
	if err != nil {
		logger.Errorf("Couldn't write icon file: %v", err)
	}
}

func (s *Manager) RefreshFFMpeg(ctx context.Context) {
	// use same directory as config path
	// executing binaries requires directory to be included
	// https://pkg.go.dev/os/exec#hdr-Executables_in_the_current_directory
	configDirectory := s.Config.GetConfigPathAbs()
	stashHomeDir := paths.GetStashHomeDirectory()

	// prefer the configured paths
	ffmpegPath := s.Config.GetFFMpegPath()
	ffprobePath := s.Config.GetFFProbePath()

	// ensure the paths are valid
	if ffmpegPath != "" {
		// path was set explicitly
		if err := ffmpeg.ValidateFFMpeg(ffmpegPath); err != nil {
			logger.Errorf("invalid ffmpeg path: %v", err)
			return
		}

		if err := ffmpeg.ValidateFFMpegCodecSupport(ffmpegPath); err != nil {
			logger.Warn(err)
		}
	} else {
		ffmpegPath = ffmpeg.ResolveFFMpeg(configDirectory, stashHomeDir)
	}

	if ffprobePath != "" {
		if err := ffmpeg.ValidateFFProbe(ffmpegPath); err != nil {
			logger.Errorf("invalid ffprobe path: %v", err)
			return
		}
	} else {
		ffprobePath = ffmpeg.ResolveFFProbe(configDirectory, stashHomeDir)
	}

	if ffmpegPath == "" {
		logger.Warn("Couldn't find FFmpeg")
	}
	if ffprobePath == "" {
		logger.Warn("Couldn't find FFProbe")
	}

	if ffmpegPath != "" && ffprobePath != "" {
		logger.Debugf("using ffmpeg: %s", ffmpegPath)
		logger.Debugf("using ffprobe: %s", ffprobePath)

		s.FFMpeg = ffmpeg.NewEncoder(ffmpegPath)
		s.FFProbe = ffmpeg.NewFFProbe(ffprobePath)

		// initialise hardware support with background context
		s.FFMpeg.InitHWSupport(context.Background())
	}
}
