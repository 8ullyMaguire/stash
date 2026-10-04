package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gqlHandler "github.com/99designs/gqlgen/graphql/handler"
	gqlExtension "github.com/99designs/gqlgen/graphql/handler/extension"
	gqlLru "github.com/99designs/gqlgen/graphql/handler/lru"
	gqlTransport "github.com/99designs/gqlgen/graphql/handler/transport"
	gqlPlayground "github.com/99designs/gqlgen/graphql/playground"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httplog"
	"github.com/gorilla/websocket"
	"github.com/vearutop/statigz"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/stashapp/stash/internal/api/loaders"
	"github.com/stashapp/stash/internal/build"
	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/plugin"
	"github.com/stashapp/stash/pkg/utils"
	"github.com/stashapp/stash/ui"
)

const (
	loginEndpoint       = "/login"
	loginLocaleEndpoint = loginEndpoint + "/locale"
	logoutEndpoint      = "/logout"
	gqlEndpoint         = "/graphql"
	playgroundEndpoint  = "/playground"
)

type Server struct {
	http.Server
	displayAddress string

	manager *manager.Manager
}

// TODO - os.DirFS doesn't implement ReadDir, so re-implement it here
// This can be removed when we upgrade go
type osFS string

func (dir osFS) ReadDir(name string) ([]os.DirEntry, error) {
	fullname := string(dir) + "/" + name
	entries, err := os.ReadDir(fullname)
	if err != nil {
		var e *os.PathError
		if errors.As(err, &e) {
			// See comment in dirFS.Open.
			e.Path = name
		}
		return nil, err
	}
	return entries, nil
}

func (dir osFS) Open(name string) (fs.File, error) {
	return os.DirFS(string(dir)).Open(name)
}

// Initialize creates a new [Server] instance.
// It assumes that the [manager.Manager] instance has been initialised.
func Initialize() (*Server, error) {
	mgr := manager.GetInstance()
	cfg := mgr.Config

	initCustomPerformerImages(cfg.GetCustomPerformerImageLocation())

	displayHost := cfg.GetHost()
	if displayHost == "0.0.0.0" {
		displayHost = "localhost"
	}
	displayAddress := displayHost + ":" + strconv.Itoa(cfg.GetPort())

	address := cfg.GetHost() + ":" + strconv.Itoa(cfg.GetPort())
	tlsConfig, err := makeTLSConfig(cfg)
	if err != nil {
		// assume we don't want to start with a broken TLS configuration
		return nil, fmt.Errorf("error loading TLS config: %v", err)
	}

	if tlsConfig != nil {
		displayAddress = "https://" + displayAddress + "/"
	} else {
		displayAddress = "http://" + displayAddress + "/"
	}

	r := chi.NewRouter()

	server := &Server{
		Server: http.Server{
			Addr:      address,
			Handler:   r,
			TLSConfig: tlsConfig,
			// disable http/2 support by default
			// when http/2 is enabled, we are unable to hijack and close
			// the connection/request. This is necessary to stop running
			// streams when deleting a scene file.
			TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
		},
		displayAddress: displayAddress,
		manager:        mgr,
	}

	r.Use(middleware.Heartbeat("/healthz"))
	r.Use(cors.AllowAll().Handler)
	r.Use(RequestIPMiddleware)
	r.Use(authenticateHandler())
	// Immediately after authentication, and BEFORE anything that could serve a
	// file: the media gate reads the user id off the request context, so it has
	// to be here or the gate has nothing to ask about. See
	// stashforge_user_context.go for why the id is resolved here at all.
	r.Use(withRequestUserID(mgr.Repository.TxnManager))
	visitedPluginHandler := mgr.SessionStore.VisitedPluginHandler()
	r.Use(visitedPluginHandler)

	r.Use(middleware.Recoverer)

	if cfg.GetLogAccess() {
		httpLogger := httplog.NewLogger("Stash", httplog.Options{
			Concise: true,
		})
		r.Use(httplog.RequestLogger(httpLogger))
	}
	r.Use(SecurityHeadersMiddleware)
	r.Use(middleware.Compress(4))
	r.Use(middleware.StripSlashes)
	r.Use(BaseURLMiddleware)

	recoverFunc := func(ctx context.Context, err interface{}) error {
		logger.Error(err)
		debug.PrintStack()

		message := fmt.Sprintf("Internal system error. Error <%v>", err)
		return errors.New(message)
	}

	repo := mgr.Repository

	dataloaders := loaders.Middleware{
		Repository: repo,
	}

	r.Use(dataloaders.Middleware)

	// The studio count batcher is per-request state, attached here for the same reason the dataloaders
	// are: a field resolver is called once per object and cannot see the whole page, so the page has to
	// be recorded somewhere the resolvers can reach. Attaching it after the dataloaders keeps the two
	// request-scoped mechanisms in one place, and after authentication so the repository is available.
	r.Use(studioCountMiddleware(repo))

	pluginCache := mgr.PluginCache
	sceneService := mgr.SceneService
	imageService := mgr.ImageService
	galleryService := mgr.GalleryService
	groupService := mgr.GroupService
	resolver := &Resolver{
		repository:     repo,
		sceneService:   sceneService,
		imageService:   imageService,
		galleryService: galleryService,
		groupService:   groupService,
		hookExecutor:   pluginCache,
	}

	// Directives MUST be supplied, even though all three are passthroughs. gqlgen's generated
	// executor refuses a query whose directive has no implementation ("directive requiresRole is
	// not implemented"), so omitting this made every @requiresRole-annotated query -- including
	// systemStatus -- return an error. An annotation that is documentation only must not also be an
	// outage. See access_directives.go for why these are passthroughs and not enforcement.
	gqlSrv := gqlHandler.New(NewExecutableSchema(NewSchemaConfig(resolver)))
	gqlSrv.SetRecoverFunc(recoverFunc)
	gqlSrv.AddTransport(gqlTransport.Websocket{
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		KeepAlivePingInterval: 10 * time.Second,
	})
	gqlSrv.AddTransport(gqlTransport.Options{})
	gqlSrv.AddTransport(gqlTransport.GET{})
	gqlSrv.AddTransport(gqlTransport.POST{})
	gqlSrv.AddTransport(gqlTransport.MultipartForm{
		MaxUploadSize: cfg.GetMaxUploadSize(),
	})

	gqlSrv.SetQueryCache(gqlLru.New[*ast.QueryDocument](1000))
	gqlSrv.Use(gqlExtension.Introspection{})

	gqlSrv.SetErrorPresenter(gqlErrorHandler)

	gqlHandlerFunc := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		gqlSrv.ServeHTTP(w, r)
	}

	// register GQL handler with plugin cache
	// chain the visited plugin handler
	// also requires the dataloader middleware
	// responseWriterContextMiddleware puts the ResponseWriter and Request into
	// the context so the login/register resolvers can set the session cookie and
	// read the User-Agent. Scoped to the GraphQL handler only: a ResponseWriter
	// in a context that outlives the handler is how a background job ends up
	// writing to a recycled buffer.
	gqlHandler := visitedPluginHandler(responseWriterContextMiddleware(
		dataloaders.Middleware(http.HandlerFunc(gqlHandlerFunc))))
	pluginCache.RegisterGQLHandler(gqlHandler)

	// The first-run wizard, and the mode read that goes with it. BOTH ARE
	// UNAUTHENTICATED, deliberately, and that is the one genuinely dangerous
	// decision in this file.
	//
	// The GETs are safe: one reports whether the wizard is done, the other
	// reports a policy flag. Neither discloses a secret, and both are needed
	// before a session exists -- the login page must know whether to ask for a
	// second factor, and that is exactly the situation the wizard resolves.
	//
	// The POST is guarded by the instance key, not a session, because the
	// operator has not logged in yet. Without that guard an unauthenticated
	// "make this instance public" would be a serious capability to hand anyone
	// who can reach the port.
	wizard := newWizardHandler(server.manager.InstanceModeStore, func() []byte {
		return server.manager.Config.GetSessionStoreKey()
	})
	r.Get(wizardEndpoint, wizard.State)
	r.Post(wizardEndpoint, wizard.Decide)
	r.Get(modeEndpoint, wizard.Mode)

	r.HandleFunc(gqlEndpoint, gqlHandlerFunc)
	r.HandleFunc(playgroundEndpoint, func(w http.ResponseWriter, r *http.Request) {
		setPageSecurityHeaders(w, r, pluginCache.ListPlugins())
		endpoint := getProxyPrefix(r) + gqlEndpoint
		gqlPlayground.Handler("GraphQL playground", endpoint, gqlPlayground.WithGraphiqlEnablePluginExplorer(true))(w, r)
	})

	r.Mount("/performer", server.getPerformerRoutes())
	r.Mount("/scene", server.getSceneRoutes())
	r.Mount("/gallery", server.getGalleryRoutes())
	r.Mount("/image", server.getImageRoutes())
	r.Mount("/studio", server.getStudioRoutes())
	r.Mount("/group", server.getGroupRoutes())
	r.Mount("/tag", server.getTagRoutes())
	r.Mount("/downloads", server.getDownloadsRoutes())
	r.Mount("/plugin", server.getPluginRoutes())
	// stash#837: the issues panel. Mounted before the static UI catch-all below, because
	// chi matches in registration order and a route registered after a wildcard is
	// unreachable -- a panel that 404s into the SPA looks like a frontend bug.
	r.Mount("/issues", server.getIssueRoutes())
	// stash#2747: the remote player protocol. Same ordering rule as /issues above -- a player
	// that cannot register because the SPA caught its handshake looks like a player bug.
	r.Mount("/external_player", server.getRemotePlayerRoutes())

	r.HandleFunc("/css", cssHandler(cfg))
	r.HandleFunc("/javascript", javascriptHandler(cfg))
	r.HandleFunc("/customlocales", customLocalesHandler(cfg))

	staticLoginUI := statigz.FileServer(ui.LoginUIBox.(fs.ReadDirFS))

	r.Get(loginEndpoint, handleLogin())
	r.Post(loginEndpoint, handleLoginPost())
	r.Get(logoutEndpoint, handleLogout())
	r.Get(loginLocaleEndpoint, handleLoginLocale(cfg))
	r.HandleFunc(loginEndpoint+"/*", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, loginEndpoint)
		w.Header().Set("Cache-Control", "no-cache")
		staticLoginUI.ServeHTTP(w, r)
	})

	// Serve static folders
	customServedFolders := cfg.GetCustomServedFolders()
	if customServedFolders != nil {
		r.Mount("/custom", getCustomRoutes(customServedFolders))
	}

	var uiFS fs.FS
	var staticUI *statigz.Server
	customUILocation := cfg.GetUILocation()
	if customUILocation != "" {
		logger.Debugf("Serving UI from %s", customUILocation)
		uiFS = osFS(customUILocation)
		staticUI = statigz.FileServer(uiFS.(fs.ReadDirFS))
	} else {
		logger.Debug("Serving embedded UI")
		uiFS = ui.UIBox
		staticUI = statigz.FileServer(ui.UIBox.(fs.ReadDirFS))
	}

	// handle favicon override
	r.HandleFunc("/favicon.ico", handleFavicon(staticUI))

	// Serve the web app
	r.HandleFunc("/*", func(w http.ResponseWriter, r *http.Request) {
		ext := path.Ext(r.URL.Path)

		if ext == ".html" || ext == "" {
			w.Header().Set("Content-Type", "text/html")
			setPageSecurityHeaders(w, r, pluginCache.ListPlugins())
		}

		if ext == "" || r.URL.Path == "/" || r.URL.Path == "/index.html" {
			themeColor := cfg.GetThemeColor()
			data, err := fs.ReadFile(uiFS, "index.html")
			if err != nil {
				panic(err)
			}
			indexHtml := string(data)

			prefix := getProxyPrefix(r)
			indexHtml = strings.ReplaceAll(indexHtml, "%COLOR%", themeColor)
			indexHtml = strings.Replace(indexHtml, `<base href="/"`, fmt.Sprintf(`<base href="%s/"`, prefix), 1)

			utils.ServeStaticContent(w, r, []byte(indexHtml))
		} else {
			isStatic, _ := path.Match("/assets/*", r.URL.Path)
			if isStatic {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}

			staticUI.ServeHTTP(w, r)
		}
	})

	logger.Infof("stash version: %s", build.VersionString())
	go printLatestVersion(context.TODO())

	return server, nil
}

func handleFavicon(staticUI *statigz.Server) func(w http.ResponseWriter, r *http.Request) {
	mgr := manager.GetInstance()
	cfg := mgr.Config

	// check if favicon.ico exists in the config directory
	// if so, use that
	// otherwise, use the embedded one
	iconPath := filepath.Join(cfg.GetConfigPath(), "favicon.ico")
	exists, _ := fsutil.FileExists(iconPath)

	if exists {
		logger.Debugf("Using custom favicon at %s", iconPath)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")

		if exists {
			http.ServeFile(w, r, iconPath)
		} else {
			staticUI.ServeHTTP(w, r)
		}
	}
}

// Start starts the server. It listens on the configured address and port.
// It calls ListenAndServeTLS if TLS is configured, otherwise it calls ListenAndServe.
// Calls to Start are blocked until the server is shutdown.
func (s *Server) Start() error {
	// The instance posture decides whether this listener may serve plain HTTP at
	// all, so the check belongs HERE -- at the point of no return. Everything
	// downstream is too late: a request already accepted over HTTP has had its
	// session cookie and its instance key in the clear.
	//
	// collab.RequiresTLS() said a public instance must not be served over plain
	// HTTP, and it shipped in M4 step 4.1 with no production caller at all. A
	// tested rule nobody calls is a comment.
	// WithDatabase, not context.Background(): the posture check reads instance_settings, and every
	// sqlite read resolves its reader from the context. The same idiom as
	// internal/manager/init.go's StashForge auth wiring, and for the same reason -- two separate
	// startup paths, one missing-context bug each.
	ctx, err := s.manager.Database.WithDatabase(context.Background())
	if err != nil {
		return err
	}

	if err := s.checkInstancePosture(ctx, s.scheme()); err != nil {
		return err
	}

	logger.Infof("stash is listening on " + s.Addr)
	logger.Infof("stash is running at " + s.displayAddress)

	if s.TLSConfig != nil {
		return s.ListenAndServeTLS("", "")
	} else {
		return s.ListenAndServe()
	}
}

// scheme reports how requests will actually arrive, which is the only thing the
// posture check may reason about.
//
// It is derived from the TLS config rather than assumed, and extracted as its own
// function only so a test can reach it. The first version had this inline in
// Start(), and the mutation that hardcodes "https" SURVIVED the posture suite,
// because every test passed a scheme in as an argument and none of them could
// see where it came from. Testing a function at the wrong seam tests its inputs
// and leaves its behaviour untested.
func (s *Server) scheme() string {
	if s.TLSConfig != nil {
		return "https"
	}
	return "http"
}

// instancePostureStore is what checkInstancePosture needs from the mode store:
// the two reads, and nothing else. An interface rather than *sqlite.InstanceModeStore
// so the refusals can be tested against a store that fails one read and not the
// other -- which is the case that decides whether this fails open or closed, and
// which a nil-store and a real-store pair cannot express.
type instancePostureStore interface {
	Mode(ctx context.Context) (collab.Mode, error)
	WizardCompleted(ctx context.Context) (bool, error)
}

// checkInstancePosture refuses to start in a posture the instance's own mode
// forbids. It FAILS CLOSED: a store that cannot be read stops the boot rather
// than defaulting to a posture nobody chose, because starting anyway makes a
// transient database error a silent security downgrade that only a restart
// reveals.
func (s *Server) checkInstancePosture(ctx context.Context, scheme string) error {
	// The nil test is HERE, against the concrete pointer, and not on the
	// interface. A nil *sqlite.InstanceModeStore assigned to an interface is a
	// NON-nil interface holding a nil pointer, so `store == nil` in the callee
	// would be false and it would call Mode() on a nil receiver -- a panic at
	// boot, for every deployment that is not a StashForge instance. The
	// order of these two checks is the entire fix for that trap.
	if s.manager == nil || s.manager.InstanceModeStore == nil {
		return nil
	}
	// ctx must carry the database reader. Every sqlite read resolves its reader from the context,
	// so a bare context.Background() here fails with "not in transaction" and -- because this
	// function FAILS CLOSED -- takes the instance down at boot instead of starting it. That is the
	// correct failure direction for an unreadable store and the wrong one for a missing context
	// value, which is why the two must not be confused: a boot log reading
	// "cannot determine the instance mode ... not in transaction" is a bug in the caller, not a
	// posture refusal.
	return checkInstancePosture(ctx, s.manager.InstanceModeStore, scheme)
}

// checkInstancePosture is the testable core: given a store and a scheme, decide
// whether this instance may serve. It takes a non-nil store -- the
// "not a StashForge instance" case is decided by the caller, against the
// concrete pointer, for the reason given above.
func checkInstancePosture(ctx context.Context, store instancePostureStore, scheme string) error {
	mode, err := store.Mode(ctx)
	if err != nil {
		return fmt.Errorf("cannot determine the instance mode, refusing to start rather than guessing a posture: %w", err)
	}

	completed, err := store.WizardCompleted(ctx)
	if err != nil {
		return fmt.Errorf("cannot determine whether the first-run wizard completed, refusing to start: %w", err)
	}

	if err := collab.CheckStartup(mode, scheme, completed); err != nil {
		return fmt.Errorf("refusing to start: %w", err)
	}

	logger.Infof("instance mode is %q, served over %s", mode, scheme)
	return nil
}

// Shutdown gracefully shuts down the server without interrupting any active connections.
func (s *Server) Shutdown() {
	err := s.Server.Shutdown(context.TODO())
	if err != nil {
		logger.Errorf("Error shutting down http server: %v", err)
	}
}

func (s *Server) getPerformerRoutes() chi.Router {
	repo := s.manager.Repository
	return performerRoutes{
		routes:          routes{txnManager: repo.TxnManager},
		performerFinder: repo.Performer,
		sfwConfig:       s.manager.Config,
	}.Routes()
}

func (s *Server) getSceneRoutes() chi.Router {
	repo := s.manager.Repository
	return sceneRoutes{
		routes:            routes{txnManager: repo.TxnManager},
		sceneFinder:       repo.Scene,
		fileGetter:        repo.File,
		captionFinder:     repo.File,
		sceneMarkerFinder: repo.SceneMarker,
		tagFinder:         repo.Tag,
	}.Routes()
}

func (s *Server) getGalleryRoutes() chi.Router {
	repo := s.manager.Repository
	return galleryRoutes{
		routes:        routes{txnManager: repo.TxnManager},
		imageFinder:   repo.Image,
		galleryFinder: repo.Gallery,
		fileGetter:    repo.File,
	}.Routes()
}

func (s *Server) getImageRoutes() chi.Router {
	repo := s.manager.Repository
	return imageRoutes{
		routes:      routes{txnManager: repo.TxnManager},
		imageFinder: repo.Image,
		fileGetter:  repo.File,
	}.Routes()
}

func (s *Server) getStudioRoutes() chi.Router {
	repo := s.manager.Repository
	return studioRoutes{
		routes:       routes{txnManager: repo.TxnManager},
		studioFinder: repo.Studio,
	}.Routes()
}

func (s *Server) getGroupRoutes() chi.Router {
	repo := s.manager.Repository
	return groupRoutes{
		routes:      routes{txnManager: repo.TxnManager},
		groupFinder: repo.Group,
	}.Routes()
}

func (s *Server) getTagRoutes() chi.Router {
	repo := s.manager.Repository
	return tagRoutes{
		routes:    routes{txnManager: repo.TxnManager},
		tagFinder: repo.Tag,
	}.Routes()
}

func (s *Server) getDownloadsRoutes() chi.Router {
	return downloadsRoutes{}.Routes()
}

func (s *Server) getPluginRoutes() chi.Router {
	return pluginRoutes{
		pluginCache: s.manager.PluginCache,
	}.Routes()
}

func copyFile(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(w, f)

	return err
}

func serveFiles(w http.ResponseWriter, r *http.Request, paths []string) {
	buffer := bytes.Buffer{}

	for _, path := range paths {
		err := copyFile(&buffer, path)
		if err != nil {
			logger.Errorf("error serving file %s: %v", path, err)
		}
		buffer.Write([]byte("\n"))
	}

	utils.ServeStaticContent(w, r, buffer.Bytes())
}

func cssHandler(c *config.Config) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var paths []string

		if c.GetCSSEnabled() && !c.GetDisableCustomizations() {
			// search for custom.css in current directory, then $HOME/.stash
			fn := c.GetCSSPath()
			exists, _ := fsutil.FileExists(fn)
			if exists {
				paths = append(paths, fn)
			}
		}

		w.Header().Set("Content-Type", "text/css")
		serveFiles(w, r, paths)
	}
}

func javascriptHandler(c *config.Config) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var paths []string

		if c.GetJavascriptEnabled() && !c.GetDisableCustomizations() {
			// search for custom.js in current directory, then $HOME/.stash
			fn := c.GetJavascriptPath()
			exists, _ := fsutil.FileExists(fn)
			if exists {
				paths = append(paths, fn)
			}
		}

		w.Header().Set("Content-Type", "text/javascript")
		serveFiles(w, r, paths)
	}
}

func customLocalesHandler(c *config.Config) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		buffer := bytes.Buffer{}

		if c.GetCustomLocalesEnabled() && !c.GetDisableCustomizations() {
			// search for custom-locales.json in current directory, then $HOME/.stash
			path := c.GetCustomLocalesPath()
			exists, _ := fsutil.FileExists(path)
			if exists {
				err := copyFile(&buffer, path)
				if err != nil {
					logger.Errorf("error serving file %s: %v", path, err)
				}
			}
		}

		if buffer.Len() == 0 {
			buffer.Write([]byte("{}"))
		}

		w.Header().Set("Content-Type", "application/json")
		utils.ServeStaticContent(w, r, buffer.Bytes())
	}
}

func makeTLSConfig(c *config.Config) (*tls.Config, error) {
	c.InitTLS()
	certFile, keyFile := c.GetTLSFiles()

	if certFile == "" && keyFile == "" {
		// assume http configuration
		return nil, nil
	}

	// ensure both files are present
	if certFile == "" {
		return nil, errors.New("SSL certificate file must be present if key file is present")
	}

	if keyFile == "" {
		return nil, errors.New("SSL key file must be present if certificate file is present")
	}

	cert, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("error reading SSL certificate file %s: %v", certFile, err)
	}

	key, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("error reading SSL key file %s: %v", keyFile, err)
	}

	certs := make([]tls.Certificate, 1)
	certs[0], err = tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, fmt.Errorf("error parsing key pair: %v", err)
	}
	tlsConfig := &tls.Config{
		Certificates: certs,
	}

	return tlsConfig, nil
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

const cspSettingPrefix = "csp_"

// cspConnectSrcFromSettings returns validated http(s) connect-src URLs from
// the plugin's settings, plus the keys that were skipped as invalid.
// Settings keys beginning with "csp_" are treated as connect-src sources.
func cspConnectSrcFromSettings(settings map[string]interface{}) (valid []string, skipped map[string]string) {
	for k, v := range settings {
		// The prefix AND a name after it. A bare "csp_" is a prefix match with
		// nothing behind it, so it silently made a setting named exactly "csp_"
		// a connect-src source -- which a plugin author can do by accident, and
		// which reads as "the opt-in is off" to anyone looking at the key.
		if !strings.HasPrefix(k, cspSettingPrefix) || len(k) == len(cspSettingPrefix) {
			continue
		}
		s, ok := v.(string)
		if !ok || !isValidConnectSrcURL(s) {
			if skipped == nil {
				skipped = make(map[string]string)
			}
			skipped[k] = fmt.Sprintf("%v", v)
			continue
		}
		valid = append(valid, s)
	}

	// settings is a map, so sort to keep the emitted header stable between requests
	sort.Strings(valid)

	return valid, skipped
}

// warnedCSPSettings tracks the invalid csp_ settings already logged, so that a
// misconfigured plugin does not emit a warning on every page request. The value
// is re-logged if the user changes the setting to another invalid value.
var warnedCSPSettings sync.Map

func warnInvalidCSPSettings(pluginID string, skipped map[string]string) {
	for key, value := range skipped {
		k := pluginID + "\x00" + key
		if prev, ok := warnedCSPSettings.Load(k); ok && prev == value {
			continue
		}
		warnedCSPSettings.Store(k, value)
		logger.Warnf("plugin %q: ignoring setting %q: not a valid connect-src URL", pluginID, key)
	}
}

func isValidConnectSrcURL(s string) bool {
	if strings.ContainsAny(s, " ,\t\r\n;\"'") {
		return false
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	// The wildcard check covers the host AND the path. `https://cdn.example.com/*`
	// has no asterisk in u.Host, so the original check passed it -- and a
	// path-wildcard source expression is legal CSP that matches EVERY request
	// under that host, which is exactly the widening the host check exists to
	// prevent. Refuse an asterisk anywhere in the value.
	//
	// Also refuse a host made only of dots. url.Parse accepts "http://." and
	// "https://..", and Hostname() is non-empty, so they passed the host checks.
	// They grant nothing (no one can serve them) but they are not hosts, and a
	// value the validator accepts should at least be a real one. Checked as
	// "every character is a dot" rather than as two literals, because "http://."
	// and "https://.." are the same shape and a third would be missed.
	if strings.Contains(s, "*") {
		return false
	}
	if h := u.Hostname(); h != "" && strings.Trim(h, ".") == "" {
		return false
	}

	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.Hostname() != "" && u.User == nil
}

func setPageSecurityHeaders(w http.ResponseWriter, r *http.Request, plugins []*plugin.Plugin) {
	c := config.GetInstance()

	defaultSrc := "data: 'self' 'unsafe-inline'"
	connectSrcSlice := []string{
		"data:",
		"'self'",
	}
	imageSrc := "data: *"
	scriptSrcSlice := []string{
		"'self'",
		"http://www.gstatic.com",
		"https://www.gstatic.com",
		"'unsafe-inline'",
		"'unsafe-eval'",
	}
	styleSrcSlice := []string{
		"'self'",
		"'unsafe-inline'",
	}
	// mediaSrc is a SLICE like connectSrcSlice/scriptSrcSlice/styleSrcSlice,
	// not a bare string that gets `+=`-ed inside the plugin loop. The string
	// form works -- a stray space is harmless to a browser -- but it differs in
	// shape from the three directives beside it and accumulates an empty
	// segment per plugin that configures none. Joining once, at the end, is the
	// same thing the other three do.
	mediaSrcSlice := []string{
		"blob:",
		"'self'",
	}

	// Workaround Safari bug https://bugs.webkit.org/show_bug.cgi?id=201591
	// Allows websocket requests to any origin
	connectSrcSlice = append(connectSrcSlice, "ws:", "wss:")

	// The graphql playground pulls its frontend from a cdn
	if r.URL.Path == playgroundEndpoint {
		connectSrcSlice = append(connectSrcSlice, "https://cdn.jsdelivr.net")
		scriptSrcSlice = append(scriptSrcSlice, "https://cdn.jsdelivr.net")
		styleSrcSlice = append(styleSrcSlice, "https://cdn.jsdelivr.net")
	}

	if !c.IsNewSystem() && c.GetHandyKey() != "" {
		connectSrcSlice = append(connectSrcSlice, "https://www.handyfeeling.com")
	}

	for _, plugin := range plugins {
		if !plugin.Enabled {
			continue
		}

		ui := plugin.UI

		for _, url := range ui.ExternalScript {
			if isURL(url) {
				scriptSrcSlice = append(scriptSrcSlice, url)
			}
		}

		for _, url := range ui.ExternalCSS {
			if isURL(url) {
				styleSrcSlice = append(styleSrcSlice, url)
			}
		}

		connectSrcSlice = append(connectSrcSlice, ui.CSP.ConnectSrc...)

		// only read plugin settings if the plugin opted in to the csp_ prefix
		if ui.CSPSettings {
			if settings := c.GetPluginConfiguration(plugin.ID); settings != nil {
				valid, skipped := cspConnectSrcFromSettings(settings)
				connectSrcSlice = append(connectSrcSlice, valid...)
				warnInvalidCSPSettings(plugin.ID, skipped)
			}
		}

		scriptSrcSlice = append(scriptSrcSlice, ui.CSP.ScriptSrc...)
		styleSrcSlice = append(styleSrcSlice, ui.CSP.StyleSrc...)
		mediaSrcSlice = append(mediaSrcSlice, ui.CSP.MediaSrc...)
	}

	connectSrc := strings.Join(connectSrcSlice, " ")
	scriptSrc := strings.Join(scriptSrcSlice, " ")
	styleSrc := strings.Join(styleSrcSlice, " ")
	mediaSrc := strings.Join(mediaSrcSlice, " ")

	cspDirectives := fmt.Sprintf("default-src %s; connect-src %s; img-src %s; script-src %s; style-src %s; media-src %s;", defaultSrc, connectSrc, imageSrc, scriptSrc, styleSrc, mediaSrc)
	cspDirectives += " worker-src blob:; child-src 'none'; object-src 'none'; form-action 'self';"

	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", cspDirectives)
}

func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")

		next.ServeHTTP(w, r)
	}
	return http.HandlerFunc(fn)
}

type contextKey struct {
	name string
}

var (
	BaseURLCtxKey = &contextKey{"BaseURL"}
	IPCtxKey      = &contextKey{"requestIP"}
)

func BaseURLMiddleware(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		scheme := "http"
		if strings.Compare("https", r.URL.Scheme) == 0 || r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		prefix := getProxyPrefix(r)

		baseURL := scheme + "://" + r.Host + prefix

		externalHost := config.GetInstance().GetExternalHost()
		if externalHost != "" {
			baseURL = externalHost + prefix
		}

		r = r.WithContext(context.WithValue(ctx, BaseURLCtxKey, baseURL))

		next.ServeHTTP(w, r)
	}
	return http.HandlerFunc(fn)
}

func getProxyPrefix(r *http.Request) string {
	return strings.TrimRight(r.Header.Get("X-Forwarded-Prefix"), "/")
}
