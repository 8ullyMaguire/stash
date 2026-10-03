package api

// stash#2747 -- launch an external player for a scene, the way Jellyfin's "play on" does.
//
// ## WHY THIS IS A POST, NOT A LINK
//
// A link cannot launch a local program. The browser can open a URL, and a URL cannot start
// `mpv` on the machine that runs Stash. So the request has to reach the SERVER, and the server
// is the only party that can spawn a process. Hence POST + a JSON body carrying the scene id,
// rather than a GET whose URL a browser could be induced to hit.
//
// That also matters for the next point.
//
// ## WHY AUTH IS REQUIRED, AND IT IS NOT OPTIONAL
//
// Whoever can reach this endpoint can start programs on the Stash host, with a file path and a
// window of their choosing. That is a remote-code-execution primitive wearing a feature's
// clothes, so the endpoint inherits `SceneCtx` like every other scene route -- it is inside the
// `/{sceneId}` block and NOT one of the two hash-keyed routes outside it.
//
// The file path is NOT caller-supplied. It comes from the scene the caller named, resolved
// through the configured libraries. If the caller could pass a path, this would be arbitrary
// local file disclosure to any media player on the LAN, which is a strictly worse bug than the
// one #2747 asked to fix.
//
// ## WHY THE WINDOW IS SENT
//
// Because of #3530. A scene may be a WINDOW of its file, and a player handed the whole file
// shows the wrong thing: the scene appears to be 45 minutes when it is 20 seconds of a
// two-hour file. `Ranged` is reported separately from the numbers because "start 0, end 1800 on
// a 1800s file" is the whole file and must not be logged as a seek.
//
// ## WHY NO CREDENTIALS GO TO THE PLAYER
//
// The player gets a URL and nothing else. If the instance requires auth, the operator puts a
// token in the URL template -- in the config file, visible to anyone who can read it. Handing
// this process a session cookie or API key would be far worse. See the config file's comment.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
)

// externalPlayerProcess is a launched player, as far as Stash is concerned.
type externalPlayerProcess struct {
	// PID is exposed so an operator can see the process exists and can find it. Stash does not
	// maintain a long-lived handle: the player outlives the request that launched it, which is
	// the entire point -- it is playing on a TV across the room, not streaming to this browser.
	PID int

	// Binary and Args are what was run, recorded rather than logged only, so a failed launch can
	// be explained without asking the operator to go dig in the log.
	Binary string
	Args   []string

	// StartedAt is when the process was spawned.
	StartedAt time.Time

	// Ranged, Start and End describe the window handed to the player. Reported even when the
	// template contains no {start}/{end}, so the operator can see what Stash THINKS it asked
	// for and compare it against what the player is showing.
	Ranged bool
	Start  float64
	End    float64
}

// playerLaunchResult is what the API returns.
//
// Deliberately does NOT include a success/failure field inferred from the player's exit: the
// request returns as soon as the process is spawned, and a player that fails to open a file exits
// a moment later with nothing here to say so. Reporting `pid: 1234` means "the process started",
// and claiming more than that would be a lie an operator would eventually catch.
type playerLaunchResult struct {
	PID       int       `json:"pid"`
	Binary    string    `json:"binary"`
	Ranged    bool      `json:"ranged"`
	Start     float64   `json:"start"`
	End       float64   `json:"end"`
	StartedAt time.Time `json:"startedAt"`
}

// launchSync guards the package-level registry below.
//
// A mutex rather than a plain map because two simultaneous launch requests are ordinary (a user
// double-clicks) and a map written from two handler goroutines is a data race that the race
// detector will eventually, and unpleasantly, report.
var launchSync struct {
	sync.Mutex
	byPID map[int]externalPlayerProcess
}

// recordLaunch notes a launched player.
//
// The registry is bounded by nothing and pruned to the most recent entries, deliberately: it
// exists to answer "did it start, and what did we run", not to be a process supervisor. A player
// Stash forgets about is a player the OS reaps; a player Stash remembers forever is a leak.
const launchRegistryMax = 64

func recordLaunch(p externalPlayerProcess) {
	launchSync.Lock()
	defer launchSync.Unlock()
	if launchSync.byPID == nil {
		launchSync.byPID = make(map[int]externalPlayerProcess)
	}
	launchSync.byPID[p.PID] = p
	if len(launchSync.byPID) <= launchRegistryMax {
		return
	}
	// Prune the oldest entries. Map iteration order is random, so this is not strictly
	// oldest-first; it does not need to be, because the registry is a diagnostic and not an
	// index anything is looked up in.
	for pid := range launchSync.byPID {
		if len(launchSync.byPID) <= launchRegistryMax {
			return
		}
		delete(launchSync.byPID, pid)
	}
}

// LastLaunch returns the most recent player Stash started, for tests and diagnostics.
func LastLaunch() (externalPlayerProcess, bool) {
	launchSync.Lock()
	defer launchSync.Unlock()
	var newest externalPlayerProcess
	found := false
	for _, p := range launchSync.byPID {
		if !found || p.StartedAt.After(newest.StartedAt) {
			newest, found = p, true
		}
	}
	return newest, found
}

// playerCommand is a seam so tests can observe the argv without spawning a real player.
var playerCommand = exec.Command

// launchExternalPlayer starts the configured player for a file and window.
//
// `environ` is passed in rather than read from os.Environ inside so the scrubbing is testable
// without mutating process state.
func launchExternalPlayer(ctx context.Context, environ []string, filePath, title string, start, end float64, ranged bool) (externalPlayerProcess, error) {
	var p externalPlayerProcess

	cfg, ok, reason := config.GetInstance().GetExternalPlayerConfig(environ, filePath, title, start, end, ranged)
	if !ok {
		return p, fmt.Errorf("%s", reason)
	}

	// #2747 - the environment is scrubbed to a fixed allowlist. See the config comment for why
	// this is an allowlist and not "the server's minus the secrets": the server's environment
	// holds the API key and the database credentials, and this process outlives the request.
	cmd := playerCommand(cfg.BinaryPath, cfg.Argv[1:]...)
	cmd.Env = config.PlayerEnv(environ)

	// Detach from the request context. WITHOUT this, the moment the HTTP response is written Go
	// kills the process, and the operator gets a PID for something that is already gone -- the
	// single most confusing possible failure for this feature.
	cmd.Cancel = nil

	if err := cmd.Start(); err != nil {
		return p, fmt.Errorf("starting %s: %w", cfg.BinaryPath, err)
	}

	p = externalPlayerProcess{
		PID:       cmd.Process.Pid,
		Binary:    cfg.BinaryPath,
		Args:      cfg.Argv[1:],
		StartedAt: time.Now(),
		Ranged:    ranged,
		Start:     start,
		End:       end,
	}
	recordLaunch(p)

	if ranged {
		logger.Infof("#2747: launched %s for %s at %.1fs-%.1fs (pid %d)",
			cfg.BinaryPath, filePath, start, end, p.PID)
	} else {
		logger.Infof("#2747: launched %s for %s (whole file, pid %d)",
			cfg.BinaryPath, filePath, p.PID)
	}

	// Reap the zombie without blocking. The player is expected to outlive us, so Wait is
	// necessary -- Stash must not accumulate unreaped children -- but it is in a goroutine
	// because Wait blocks until the player exits, which for a feature TV is minutes or hours.
	go func() {
		if err := cmd.Wait(); err != nil {
			logger.Debugf("#2747: player pid %d exited: %v", p.PID, err)
		}
	}()

	return p, nil
}

// playerFilePath resolves the absolute path of the scene's primary file.
//
// Absolutised here because the player is spawned by the SERVER, whose working directory is not
// necessarily the library root -- a relative path handed to mpv would resolve against the
// server's cwd and silently open the wrong file, or nothing.
//
// The path comes from the scene record, never from the request body. See the file comment.
func playerFilePath(scene *models.Scene) (string, error) {
	pf := scenePrimaryFile(scene)
	if pf == nil {
		return "", fmt.Errorf("scene has no primary file to play")
	}
	if pf.Path == "" {
		return "", fmt.Errorf("scene's primary file has no path on this host")
	}
	return pf.Path, nil
}

// sceneWindowForPlayer resolves the window a player should be handed.
//
// Mirrors the read-side rule in resolveSceneWindow: an explicit query param wins, because that
// is how a user asks for a sub-range, and the stored window is the fallback. `ranged` is
// computed the same way so an unranged scene is never reported as a window.
func sceneWindowForPlayer(scene *models.Scene, r *http.Request) (start, end float64, ranged bool) {
	pf := scenePrimaryFile(scene)
	if pf == nil {
		return 0, 0, false
	}

	// No window at all is the common case -- every scene that predates #3530, and every scene
	// whose range was cleared -- and it is emphatically NOT a window. Without this check the
	// arithmetic below falls through: start=0 and end=0 satisfy `end < duration`, so the scene
	// would be handed `--start=0 --end=0`, which is a zero-length clip rather than the whole file.
	//
	// Caught by TestExternalPlayerLaunchReturnsJSON, which asserted the response's `ranged` flag
	// against a scene carrying no window. The nil check has to come BEFORE the arithmetic for
	// that reason: "no window" and "a window from 0 to 0" are the same numbers and only the
	// pointers distinguish them.
	if pf.StartTime == nil && pf.EndTime == nil {
		return 0, 0, false
	}

	if pf.StartTime != nil {
		start = *pf.StartTime
	}
	if pf.EndTime != nil {
		end = *pf.EndTime
	}

	// A window that covers the whole file is not a window. Treating it as one hands the player a
	// seek to 0 and a stop at the end, which some players implement as a full remount.
	//
	// Two sub-cases, and both have to be here:
	//
	//   - start at the head (0) with no end: the window runs to the end of the file by
	//     definition, so it IS the whole file.
	//   - start at the head with an end at or past the duration: also the whole file. `>=` not
	//     `>`, because a window ending exactly at the duration covers all of it.
	//
	// Note what is deliberately NOT special-cased: an open-ended window partway in (say from
	// 600s) IS a real window, and `end` is 0 for it because there is no end. A test table I wrote
	// caught that a `end == 0` guard I'd added first was reporting those as NOT ranged, i.e.
	// handing the player the whole file for a scene that is the second half of one. The
	// distinction that matters is whether START is at the head, not whether end is zero.
	if start == 0 && (pf.EndTime == nil || end >= pf.Duration) {
		return 0, 0, false
	}

	return start, end, true
}

// ExternalPlayer launches the configured external player for a scene.
//
// POST /scene/{id}/external_player
//
// The body is empty. Everything the launch needs -- which file, which window -- is derived from
// the scene, so there is nothing for a caller to supply and therefore nothing for a caller to get
// wrong. That is the same reasoning as not accepting a file path, and it is why there is no
// request struct here.
func (rs sceneRoutes) ExternalPlayer(w http.ResponseWriter, r *http.Request) {
	scene := r.Context().Value(sceneKey).(*models.Scene)

	if !config.GetInstance().GetExternalPlayerEnabled() {
		http.Error(w, "external player is not enabled", http.StatusNotImplemented)
		return
	}

	filePath, err := playerFilePath(scene)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	start, end, ranged := sceneWindowForPlayer(scene, r)

	p, err := launchExternalPlayer(r.Context(), os.Environ(), filePath, scene.Title, start, end, ranged)
	if err != nil {
		// 400 not 500: a misconfigured command template is the operator's input, not a server
		// fault, and a 500 would send them looking at server logs for a config problem.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := json.NewEncoder(w).Encode(playerLaunchResult{
		PID:       p.PID,
		Binary:    p.Binary,
		Ranged:    p.Ranged,
		Start:     p.Start,
		End:       p.End,
		StartedAt: p.StartedAt,
	}); err != nil {
		logger.Errorf("#2747: writing the launch response failed: %v", err)
	}
}
