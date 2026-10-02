// Package manager provides the core manager of the application.
// This consolidates all the services and managers into a single struct.
package manager

import (
	"context"
	"errors"
	"fmt"
	"github.com/stashapp/stash/internal/acquisition"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/remeh/sizedwaitgroup"
	"github.com/stashapp/stash/internal/dlna"
	"github.com/stashapp/stash/internal/log"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/auth"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/paths"
	"github.com/stashapp/stash/pkg/pkg"
	"github.com/stashapp/stash/pkg/plugin"
	"github.com/stashapp/stash/pkg/scraper"
	"github.com/stashapp/stash/pkg/session"
	"github.com/stashapp/stash/pkg/sqlite"

	// register custom migrations
	_ "github.com/stashapp/stash/pkg/sqlite/migrations"
)

type Manager struct {
	Config *config.Config
	Logger *log.Logger

	// ImageThumbnailGenerateWaitGroup is the global wait group image thumbnail generation
	// It uses the parallel tasks setting from the configuration.
	ImageThumbnailGenerateWaitGroup sizedwaitgroup.SizedWaitGroup

	Paths *paths.Paths

	FFMpeg        *ffmpeg.FFMpeg
	FFProbe       *ffmpeg.FFProbe
	StreamManager *ffmpeg.StreamManager

	JobManager      *job.Manager
	ReadLockManager *fsutil.ReadLockManager

	DownloadStore *DownloadStore
	SessionStore  session.Store

	PluginCache  *plugin.Cache
	ScraperCache *scraper.Cache

	PluginPackageManager  *pkg.Manager
	ScraperPackageManager *pkg.Manager

	DLNAService *dlna.Service

	Database   *sqlite.Database
	Repository models.Repository

	// StashForge multi-user surface. UserStore is always present; Auth is
	// non-nil only when the instance is in multi-user mode, which is what the
	// GraphQL resolvers type-assert on rather than asking a config question a
	// second time.
	UserStore models.UserStore

	// TOTPStore holds the 2FA secrets and the durable single-use record of spent
	// steps. It is nil in single-user mode, where there is exactly one account
	// and the config password is the only credential -- so every 2FA call site
	// has to ask whether it is nil rather than assume it.
	TOTPStore *sqlite.TOTPStore

	// LibraryAccessStore decides who may read a library; a library with no grant
	// row is private to its owner.
	//
	// This is the ADMINISTRATION surface -- granting, revoking, listing. The
	// SERVING decision goes through MediaScopeStore below, which additionally
	// resolves which library a row is in. Both are needed and they answer
	// different questions: this one is "does this grant row exist", that one is
	// "may this caller have this file".
	LibraryAccessStore *sqlite.LibraryAccessStore

	// LibraryStore is the `libraries` TABLE: create, delete, list, and the
	// ownership checks around them. Separate from LibraryAccessStore above even
	// though both are about libraries, because one is on the authentication path
	// (a single indexed read per media request) and this one is on the setup
	// path (a scan of one user's libraries). Merging them would make the media
	// gate's dependency a struct that grew setup methods.
	LibraryStore *sqlite.LibraryStore

	// MediaScopeStore is what every media-serving route asks: which library is
	// this row in, who owns it, and may this caller have it (M4 step 4.3,
	// spec §6.4).
	//
	// The nil test for this is against the CONCRETE POINTER at the call site,
	// not against an interface -- a nil *sqlite.MediaScopeStore in an interface
	// is a non-nil interface, and `gate == nil` would be false (HANDOFF.md #9).
	// allowMedia in internal/api/stashforge_media_gate.go does that, and refuses
	// when it is nil rather than passing the request through.
	MediaScopeStore *sqlite.MediaScopeStore

	// MeshServeStore is the mesh's serve-side budget (M8 step 0, probe 3).
	//
	// It is the FIFTH time a store in this package was fully implemented, fully
	// tested, and reachable from nothing — TestStashForgeStoreConstructorsAre
	// ActuallyWired caught NewMeshServeStore with no non-test caller, the same
	// defect as ProposalStore, ReputationStore, the media gate and ConsentStore.
	// So it is built here even though no route consults it yet, because a store
	// nothing can reach is a store whose tests are testing a function no product
	// code calls.
	//
	// Note it takes the *Database, unlike the stores above: its budget check and
	// its log write must be ONE transaction, so it opens its own rather than
	// inheriting whatever the caller happens to be in.
	MeshServeStore *sqlite.MeshServeStore

	// InstanceModeStore holds the instance's private/contribute/public decision
	// and whether the first-run wizard has been completed.
	//
	// Always non-nil after init, unlike TOTPStore: the mode gate applies to EVERY
	// mode, and an instance with no mode store would have no way to record the
	// decision that makes it startable.
	InstanceModeStore *sqlite.InstanceModeStore

	// AutoAcquireStore holds the instance's capability-2 posture — the three-state
	// sharing switch of off / fetch_only / full (§6b.3, R083).
	//
	// Wired here for the reason the ConsentStore comment gives, and this is the FIFTH
	// time: a fully implemented, fully tested store that nothing constructs is a
	// feature that does not exist. The switch is read at the point the permission is
	// read, so it must be reachable from a Manager — a package-level global would be
	// the alternative and it is the shape that makes a store untestable.
	AutoAcquireStore *sqlite.AutoAcquireStore

	// AcquisitionQueue builds capability 2's download queue for the candidates the mesh
	// advertises, gated by AutoAcquireStore's switch.
	//
	// IT IS HERE, AND NOT IN internal/acquisition, BECAUSE THAT IS THE SIXTH TIME THIS
	// SHAPE HAS COME UP. The queue is a pure function with no state, so the wiring test
	// does not catch it -- and it would still have been fully implemented, fully tested
	// and called by nothing, which is the same defect as an unwired store with a
	// different shape. The switch and the queue it gates are one reachable unit, so
	// they live together.
	//
	// The taste inputs are passed in rather than read here: a Fingerprint is derived
	// from a USER's records (§6a.3) and this Manager knows no user, so a caller
	// supplies them. That is a narrower seam than it looks -- §6b.3's queue is
	// per-user taste over a per-instance switch.
	AcquireQueue func(ctx context.Context, req acquisition.Request) (*acquisition.Queue, error)

	// ConsentStore is where a user's metadata-sharing decision lives.
	//
	// It is here because this is the FOURTH time a store in this package was
	// fully implemented, fully tested, and read by nothing: a grep for
	// NewConsentStore found exactly one hit, its own constructor. So the store
	// was not a feature that was switched off -- it was a feature nothing
	// called, which is the same defect as the gate being unreachable and it
	// survives every test in the tree. The rule that catches it is the cheap
	// one: grep the constructor in NON-TEST files and see who builds it.
	//
	// Always non-nil after init, because §6.1's default (an absent row means
	// opted IN) is resolved through this store, so a nil one would leave the
	// publish path unable to answer the question at all.
	ConsentStore *sqlite.ConsentStore

	// AccessPolicyStore answers "may this user do X to content?" and owns the two
	// switches behind it: the operator's ceiling (R062) and each user's revocable
	// content consent (R026, §6a.11).
	//
	// THE SIXTH TIME, and the reason it is worth naming again is that this one is
	// different in kind. The five before it were stores nothing constructed. This
	// one was a store nothing constructed AND a domain model nothing reached:
	// internal/collab/access_level.go was 231 lines with nine passing tests, and
	// R025–R028 sat at `specified` for a whole milestone because DecideAccess took
	// three inputs and two of them had nowhere to live. A complete model with no
	// caller is not half-built work, it is INVISIBLE work -- nothing in a green
	// suite distinguishes "the access model is wrong" from "the access model is
	// unreachable", which is why TestStashForgeStoreConstructorsAreActuallyWired
	// caught the constructor and only reached the model by asking.
	//
	// Always non-nil after init, because a nil one would leave every content path
	// unable to answer the question at all, and the safe answer to "may this user
	// see this" has to come from somewhere.
	AccessPolicyStore *sqlite.AccessPolicyStore

	// DirectoryStore is §6a.4's claim-and-confirm and R045's network pricing: who
	// asserted that a studio is what it says it is, and who independently said so.
	//
	// THE SEVENTH such field, and the SEVENTH time the same question applies -- with
	// one difference worth stating. The previous six were stores nothing
	// CONSTRUCTED, so grepping the constructor found them. This one was worse: it
	// did not exist, and the model it serves -- internal/directory, 319 lines,
	// thirteen exported symbols, every test green -- was imported by NOTHING. So
	// there was no constructor to grep and no store to find.
	//
	// Which is why the guard for this one is a source-tree walk rather than a
	// constructor grep: a guard that searches for one shape of defect is blind to
	// every other shape, and "a domain package nothing reaches" is a different shape
	// from "a store nothing builds". See
	// TestDirectoryIsImportedByNonTestCode.
	DirectoryStore *sqlite.DirectoryStore
	Auth              *auth.SessionStore
	AuthMode          auth.Mode

	// StashForge collaboration surface. The collab stores are present on every
	// instance, including a single-user one: a single-user instance still needs
	// somewhere to put an edit proposal if it is ever read by a remote stash
	// box, and refusing to construct the store means the GraphQL layer has to
	// carry a nil check that exists for no reason. What a single-user instance
	// must NOT get is the ability to self-accept, and that is enforced in
	// collab's policy, not here.
	CollabProposals *sqlite.EditProposalStore
	CollabVotes     *sqlite.ProposalVoteStore
	CollabTargets   *sqlite.CollabTargetStore
	// CollabShadow records what the weighted rule WOULD have decided beside what
	// flat quorum actually decided. Purely observational for now.
	CollabShadow *sqlite.ShadowLogStore
	// CollabStore is the adapter from the row stores to collab.ProposalStore.
	// It is a separate field from CollabProposals because they are different
	// interfaces over the same table, and reaching for the wrong one is a
	// compile error either way.
	CollabStore *sqlite.CollabProposalStore

	// CurationStore reads and writes the instance's automatic-curation mode, which is
	// how a machine's autotag match reaches shared content: filed as a proposal,
	// applied directly, or not run at all.
	//
	// IT IS BUILT IN INIT rather than on demand, because both ways a read can go wrong
	// are bad and only one of them is visible. A read that failed OPEN would be a
	// silent governance bypass; a read that failed CLOSED would be a scan that quietly
	// stopped, which an operator cannot distinguish from a scan that found nothing. A
	// store that exists and is asked a question makes the caller choose which of the
	// two it is.
	CurationStore *sqlite.CurationStore

	// CollabApply is the write surface where an approved proposal becomes a change:
	// the same TargetStore the applier uses, so a direct autotag write and an approved
	// one are the same statement.
	CollabApply *sqlite.CollabTargetStore

	// CollabReputation is the adapter from the row store to
	// collab.ReputationStore, for the same reason CollabStore exists: they are
	// different interfaces over different tables, and reaching for the wrong one
	// should be a compile error.
	CollabReputation *sqlite.CollabReputationStore

	// PersonClusters is the identity-clustering store: clusters, their members,
	// and the append-only record of who named them.
	//
	// On the manager rather than constructed per-resolver for the same reason
	// as the stores above -- the store reaches the database through the package
	// global, so there is no connection to own and nothing to inject. What the
	// manager owns is the one instance, so a resolver cannot end up with two
	// stores that disagree about what exists.
	PersonClusters *sqlite.ClusterStore

	SceneService   SceneService
	ImageService   ImageService
	GalleryService GalleryService
	GroupService   GroupService

	scanSubs *subscriptionManager
}

var instance *Manager

func GetInstance() *Manager {
	if instance == nil {
		panic("manager not initialized")
	}
	return instance
}

// MaybeGetInstance returns the Manager, or nil when the process has none.
//
// # WHY THIS EXISTS
//
// GetInstance PANICS when there is no instance, which is the right behaviour
// for code that cannot work without one -- a handler deep in a request that
// assumes a running server. It is the WRONG behaviour for a security control
// that is supposed to fail closed, because a panic is not a refusal: it is an
// uncontrolled exit, it is recovered by the middleware into a 500, and in a
// goroutine it takes the process down.
//
// M4's media gate hit exactly this. It read
// `if mgr == nil || mgr.MediaScopeStore == nil` and the first clause was dead
// code: GetInstance had already panicked. A guard that cannot be reached looks
// exactly like a guard that works, which is why it needs a test that calls the
// function with no Manager -- and that test is the one that found this.
//
// So the rule is: a refusal path asks MaybeGetInstance. Everything else asks
// GetInstance, because everything else genuinely cannot proceed.
func MaybeGetInstance() *Manager {
	return instance
}

func (s *Manager) SetBlobStoreOptions() {
	storageType := s.Config.GetBlobsStorage()
	blobsPath := s.Config.GetBlobsPath()
	extraBlobsPaths := s.Config.GetExtraBlobsPaths()

	s.Database.SetBlobStoreOptions(sqlite.BlobStoreOptions{
		UseFilesystem:      storageType == config.BlobStorageTypeFilesystem,
		UseDatabase:        storageType == config.BlobStorageTypeDatabase,
		Path:               blobsPath,
		SupplementaryPaths: extraBlobsPaths,
	})
}

func (s *Manager) RefreshConfig() {
	cfg := s.Config
	*s.Paths = paths.NewPaths(cfg.GetGeneratedPath(), cfg.GetBlobsPath())
	if cfg.Validate() == nil {
		if err := fsutil.EnsureDir(s.Paths.Generated.Screenshots); err != nil {
			logger.Warnf("could not create screenshots directory: %v", err)
		}
		if err := fsutil.EnsureDir(s.Paths.Generated.Vtt); err != nil {
			logger.Warnf("could not create VTT directory: %v", err)
		}
		if err := fsutil.EnsureDir(s.Paths.Generated.Markers); err != nil {
			logger.Warnf("could not create markers directory: %v", err)
		}
		if err := fsutil.EnsureDir(s.Paths.Generated.Transcodes); err != nil {
			logger.Warnf("could not create transcodes directory: %v", err)
		}
		if err := fsutil.EnsureDir(s.Paths.Generated.Downloads); err != nil {
			logger.Warnf("could not create downloads directory: %v", err)
		}
		if err := fsutil.EnsureDir(s.Paths.Generated.InteractiveHeatmap); err != nil {
			logger.Warnf("could not create interactive heatmaps directory: %v", err)
		}

		s.ImageThumbnailGenerateWaitGroup.Size = cfg.GetParallelTasksWithAutoDetection()
	}
}

// RefreshPluginCache refreshes the plugin cache.
// Call this when the plugin configuration changes.
func (s *Manager) RefreshPluginCache() {
	s.PluginCache.ReloadPlugins()
}

// RefreshScraperCache refreshes the scraper cache.
// Call this when the scraper configuration changes.
func (s *Manager) RefreshScraperCache() {
	s.ScraperCache.ReloadScrapers()
}

// RefreshStreamManager refreshes the stream manager.
// Call this when the cache directory changes.
func (s *Manager) RefreshStreamManager() {
	// shutdown existing manager if needed
	if s.StreamManager != nil {
		s.StreamManager.Shutdown()
		s.StreamManager = nil
	}

	cfg := s.Config
	cacheDir := cfg.GetCachePath()
	s.StreamManager = ffmpeg.NewStreamManager(cacheDir, s.FFMpeg, s.FFProbe, cfg, s.ReadLockManager)
}

// RefreshDLNA starts/stops the DLNA service as needed.
func (s *Manager) RefreshDLNA() {
	dlnaService := s.DLNAService
	enabled := s.Config.GetDLNADefaultEnabled()
	if !enabled && dlnaService.IsRunning() {
		dlnaService.Stop(nil)
	} else if enabled && !dlnaService.IsRunning() {
		if err := dlnaService.Start(nil); err != nil {
			logger.Warnf("error starting DLNA service: %v", err)
		}
	}
}

func createPackageManager(localPath string, srcPathGetter pkg.SourcePathGetter) *pkg.Manager {
	const timeout = 10 * time.Second
	httpClient := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
		Timeout: timeout,
	}

	return &pkg.Manager{
		Local: &pkg.Store{
			BaseDir:      localPath,
			ManifestFile: pkg.ManifestFile,
		},
		PackagePathGetter: srcPathGetter,
		Client:            httpClient,
	}
}

func (s *Manager) RefreshScraperSourceManager() {
	s.ScraperPackageManager = createPackageManager(s.Config.GetScrapersPath(), s.Config.GetScraperPackagePathGetter())
}

func (s *Manager) RefreshPluginSourceManager() {
	s.PluginPackageManager = createPackageManager(s.Config.GetPluginsPath(), s.Config.GetPluginPackagePathGetter())
}

func setSetupDefaults(input *SetupInput) {
	if input.ConfigLocation == "" {
		input.ConfigLocation = filepath.Join(fsutil.GetHomeDirectory(), ".stash", "config.yml")
	}

	configDir := filepath.Dir(input.ConfigLocation)
	if input.GeneratedLocation == "" {
		input.GeneratedLocation = filepath.Join(configDir, "generated")
	}
	if input.CacheLocation == "" {
		input.CacheLocation = filepath.Join(configDir, "cache")
	}

	if input.DatabaseFile == "" {
		input.DatabaseFile = filepath.Join(configDir, "stash-go.sqlite")
	}

	if input.BlobsLocation == "" {
		input.BlobsLocation = filepath.Join(configDir, "blobs")
	}
}

func (s *Manager) Setup(ctx context.Context, input SetupInput) error {
	setSetupDefaults(&input)
	cfg := s.Config

	// create the config directory if it does not exist
	// don't do anything if config is already set in the environment
	if !config.FileEnvSet() {
		// #3304 - if config path is relative, it breaks the ffmpeg/ffprobe
		// paths since they must not be relative. The config file property is
		// resolved to an absolute path when stash is run normally, so convert
		// relative paths to absolute paths during setup.
		// #6287 - this should no longer be necessary since the ffmpeg code
		// converts to absolute paths. Converting the config location to
		// absolute means that scraper and plugin paths default to absolute
		// which we don't want.
		configFile := input.ConfigLocation
		configDir := filepath.Dir(configFile)

		if exists, _ := fsutil.DirExists(configDir); !exists {
			if err := os.MkdirAll(configDir, 0755); err != nil {
				return fmt.Errorf("error creating config directory: %v", err)
			}
		}

		if err := fsutil.Touch(configFile); err != nil {
			return fmt.Errorf("error creating config file: %v", err)
		}

		s.Config.SetConfigFile(configFile)
	}

	if err := cfg.SetInitialConfig(); err != nil {
		return fmt.Errorf("error setting initial configuration: %v", err)
	}

	// create the generated directory if it does not exist
	if !cfg.HasOverride(config.Generated) {
		if exists, _ := fsutil.DirExists(input.GeneratedLocation); !exists {
			if err := os.MkdirAll(input.GeneratedLocation, 0755); err != nil {
				return fmt.Errorf("error creating generated directory: %v", err)
			}
		}

		s.Config.SetString(config.Generated, input.GeneratedLocation)
	}

	// create the cache directory if it does not exist
	if !cfg.HasOverride(config.Cache) {
		if exists, _ := fsutil.DirExists(input.CacheLocation); !exists {
			if err := os.MkdirAll(input.CacheLocation, 0755); err != nil {
				return fmt.Errorf("error creating cache directory: %v", err)
			}
		}

		cfg.SetString(config.Cache, input.CacheLocation)
	}

	if input.SFWContentMode {
		cfg.SetBool(config.SFWContentMode, true)
	}

	if input.StoreBlobsInDatabase {
		cfg.SetInterface(config.BlobsStorage, config.BlobStorageTypeDatabase)
	} else {
		if !cfg.HasOverride(config.BlobsPath) {
			if exists, _ := fsutil.DirExists(input.BlobsLocation); !exists {
				if err := os.MkdirAll(input.BlobsLocation, 0755); err != nil {
					return fmt.Errorf("error creating blobs directory: %v", err)
				}
			}

			cfg.SetString(config.BlobsPath, input.BlobsLocation)
		}

		cfg.SetInterface(config.BlobsStorage, config.BlobStorageTypeFilesystem)
	}

	// set the configuration
	if !cfg.HasOverride(config.Database) {
		cfg.SetString(config.Database, input.DatabaseFile)
	}

	cfg.SetInterface(config.Stash, input.Stashes)

	if input.InitialUsername != "" && input.InitialPassword != "" {
		cfg.SetString(config.Username, input.InitialUsername)
		// stash#7135: a password bcrypt refuses (over its 72-byte limit) must
		// fail setup, not be written as an empty hash. An empty hash makes
		// HasCredentials() false, so the instance would come up with
		// authentication effectively disabled.
		if err := cfg.SetPassword(input.InitialPassword); err != nil {
			return fmt.Errorf("error setting initial password: %w", err)
		}
	}

	if err := cfg.Write(); err != nil {
		return fmt.Errorf("error writing configuration file: %v", err)
	}

	// finish initialization
	if err := s.postInit(ctx); err != nil {
		return fmt.Errorf("error completing initialization: %v", err)
	}

	cfg.FinalizeSetup()

	return nil
}

func (s *Manager) validateFFmpeg() error {
	if s.FFMpeg == nil || s.FFProbe == nil {
		return errors.New("missing ffmpeg and/or ffprobe")
	}
	return nil
}

func (s *Manager) AnonymiseDatabase(download bool) (string, string, error) {
	var outPath string
	var outName string
	if download {
		outDir := s.Paths.Generated.Downloads
		if err := fsutil.EnsureDir(outDir); err != nil {
			return "", "", fmt.Errorf("could not create output directory %v: %w", outDir, err)
		}
		f, err := os.CreateTemp(outDir, "anonymous*.sqlite")
		if err != nil {
			return "", "", err
		}

		outPath = f.Name()
		outName = s.Database.AnonymousDatabasePath("")
		f.Close()
	} else {
		outDir := s.Config.GetBackupDirectoryPathOrDefault()
		if outDir != "" {
			if err := fsutil.EnsureDir(outDir); err != nil {
				return "", "", fmt.Errorf("could not create output directory %v: %w", outDir, err)
			}
		}
		outPath = s.Database.AnonymousDatabasePath(outDir)
		outName = filepath.Base(outPath)
	}

	err := s.Database.Anonymise(outPath)
	if err != nil {
		return "", "", err
	}

	return outPath, outName, nil
}

func (s *Manager) GetSystemStatus() *SystemStatus {
	workingDir := fsutil.GetWorkingDirectory()
	homeDir := fsutil.GetHomeDirectory()

	database := s.Database
	dbSchema := int(database.Version())
	dbPath := database.DatabasePath()
	appSchema := int(database.AppSchemaVersion())

	status := SystemStatusEnumOk
	if s.Config.IsNewSystem() {
		status = SystemStatusEnumSetup
	} else if dbSchema < appSchema {
		status = SystemStatusEnumNeedsMigration
	}

	configFile := s.Config.GetConfigFile()

	ffmpegPath := ""
	if s.FFMpeg != nil {
		ffmpegPath = s.FFMpeg.Path()
	}

	ffprobePath := ""
	if s.FFProbe != nil {
		ffprobePath = s.FFProbe.Path()
	}

	return &SystemStatus{
		Os:             runtime.GOOS,
		WorkingDir:     workingDir,
		HomeDir:        homeDir,
		DatabaseSchema: &dbSchema,
		DatabasePath:   &dbPath,
		AppSchema:      appSchema,
		Status:         status,
		ConfigPath:     &configFile,
		FfmpegPath:     &ffmpegPath,
		FfprobePath:    &ffprobePath,
	}
}

// Shutdown gracefully stops the manager
func (s *Manager) Shutdown() {
	// TODO: Each part of the manager needs to gracefully stop at some point

	if s.StreamManager != nil {
		s.StreamManager.Shutdown()
		s.StreamManager = nil
	}

	err := s.Database.Close()
	if err != nil {
		logger.Errorf("Error closing database: %s", err)
	}
}
