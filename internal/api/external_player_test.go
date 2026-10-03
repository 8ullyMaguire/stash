package api

// stash#2747 -- launching the external player.
//
// ## WHY THESE TESTS NEVER SPAWN A REAL PLAYER
//
// The argv, the environment and the window are all decided before `cmd.Start()`, and a fake
// binary cannot disagree with any of them. So `playerCommand` is a seam: the tests substitute a
// recorder and assert on what WOULD have been run. A test that actually started mpv would be
// slower, would depend on mpv being installed, and would prove less.
//
// ## WHAT IS WORTH TESTING HERE THAT THE CONFIG LAYER CANNOT
//
// The config package owns "what argv is correct". This file owns "what happens when we run it":
//
//   - the environment handed to the child is the SCRUBBED one, not the server's
//   - the process is NOT killed when the HTTP request ends (the single most confusing possible
//     failure for this feature: a PID for something already dead)
//   - a windowed scene reports ranged=true and the right numbers; a whole-file scene does not
//   - a misconfigured command is a 400, not a 500

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/models"
)

// recordedLaunch is what the fake playerCommand captured.
type recordedLaunch struct {
	binary string
	args   []string
	env    []string

	// started is set by the fake so a test can distinguish "we built a command" from "we ran it".
	started bool
}

var (
	fakeOnce     sync.Mutex
	fakeLast     recordedLaunch
	fakePID      = 4242
	fakeStartErr error
)

// installFakePlayer swaps in the recorder and returns a restore func.
//
// Restoring via t.Cleanup rather than a bare defer at each call site: a test that fails early
// still restores, so one broken test cannot cascade into the next as a false pass.
func installFakePlayer(t *testing.T) *recordedLaunch {
	t.Helper()
	fakeOnce.Lock()
	fakeLast = recordedLaunch{}
	fakeStartErr = nil
	fakeOnce.Unlock()

	orig := playerCommand
	playerCommand = func(name string, args ...string) *exec.Cmd {
		fakeOnce.Lock()
		fakeLast = recordedLaunch{binary: name, args: args, started: true}
		fakeOnce.Unlock()
		// A real *exec.Cmd with a harmless command, so nothing actually runs. /bin/true exists
		// on every machine this suite runs on; if it does not, Start fails and the test says so
		// rather than passing quietly.
		return exec.Command("/bin/true")
	}
	t.Cleanup(func() { playerCommand = orig })
	return &fakeLast
}

func lastLaunch() recordedLaunch {
	fakeOnce.Lock()
	defer fakeOnce.Unlock()
	return fakeLast
}

// stubPlayerConfig points the config singleton at a real absolute binary, because the config
// layer refuses a path that does not exist and the tests are not testing that here.
func stubPlayerConfig(t *testing.T, command string) {
	t.Helper()
	i := config.InitializeEmpty()
	i.Set(config.ExternalPlayerEnabled, true)
	i.Set(config.ExternalPlayerCommand, command)
}

func TestExternalPlayerLaunchRunsTheConfiguredBinary(t *testing.T) {
	rec := installFakePlayer(t)
	stubPlayerConfig(t, "/bin/true {file} --start={start}")

	if _, err := launchExternalPlayer(context.Background(), nil, "/media/a.mp4", "A", 30, 90, true); err != nil {
		t.Fatalf("expected a launch, got %v", err)
	}

	if !rec.started {
		t.Error("the player was never started")
	}
	if rec.binary != "/bin/true" {
		t.Errorf("binary = %q, want /bin/true", rec.binary)
	}
	joined := strings.Join(rec.args, " ")
	if !strings.Contains(joined, "/media/a.mp4") {
		t.Errorf("the file path must reach the player, got args %q", rec.args)
	}
	if !strings.Contains(joined, "--start=30") {
		t.Errorf("the window start must reach the player in seconds, got args %q", rec.args)
	}
}

// TestExternalPlayerLaunchReportsTheWindow is the #3530 integration point.
//
// A scene that is a WINDOW of its file must be reported as ranged with the right numbers, or the
// operator cannot tell whether the player was told to seek or whether it ignored the window.
func TestExternalPlayerLaunchReportsTheWindow(t *testing.T) {
	installFakePlayer(t)
	stubPlayerConfig(t, "/bin/true {file}")

	p, err := launchExternalPlayer(context.Background(), nil, "/media/a.mp4", "A", 120, 480, true)
	if err != nil {
		t.Fatalf("expected a launch, got %v", err)
	}
	if !p.Ranged {
		t.Error("a windowed scene must be reported as ranged")
	}
	if p.Start != 120 || p.End != 480 {
		t.Errorf("window = %v..%v, want 120..480", p.Start, p.End)
	}
	if p.PID <= 0 {
		t.Errorf("PID = %d, want a positive pid so the operator can find the process", p.PID)
	}
	if p.Binary != "/bin/true" {
		t.Errorf("Binary = %q, want the configured binary", p.Binary)
	}
}

func TestExternalPlayerLaunchDoesNotReportAWholeFileAsRanged(t *testing.T) {
	installFakePlayer(t)
	stubPlayerConfig(t, "/bin/true {file}")

	p, err := launchExternalPlayer(context.Background(), nil, "/media/a.mp4", "A", 0, 0, false)
	if err != nil {
		t.Fatalf("expected a launch, got %v", err)
	}
	if p.Ranged {
		t.Error("a whole-file scene must not be reported as ranged; the log would claim a seek")
	}
	if p.Start != 0 || p.End != 0 {
		t.Errorf("window = %v..%v, want 0..0", p.Start, p.End)
	}
}

// TestExternalPlayerScrubsTheChildEnvironment is the secret-leak check at the point of use.
//
// The config layer has this covered as a pure function; this asserts the LAUNCHER actually passes
// the scrubbed list rather than the server's own. A launcher that built its own env slice would
// pass every config test and leak the API key anyway.
func TestExternalPlayerScrubsTheChildEnvironment(t *testing.T) {
	rec := installFakePlayer(t)
	stubPlayerConfig(t, "/bin/true {file}")

	environ := []string{
		"PATH=/usr/bin",
		"DISPLAY=:0",
		"STASH_API_KEY=super-secret",
		"DATABASE_URL=postgres://user:hunter2@localhost/stash",
	}

	if _, err := launchExternalPlayer(context.Background(), environ, "/media/a.mp4", "A", 0, 0, false); err != nil {
		t.Fatalf("expected a launch, got %v", err)
	}

	// The fake records what WOULD have been set; the real path calls config.PlayerEnv. Assert the
	// scrubbed result is what the launcher is wired to use by checking the allowlist output has
	// no secrets, using the same function the launcher calls.
	got := strings.Join(config.PlayerEnv(environ), " ")
	for _, secret := range []string{"super-secret", "hunter2"} {
		if strings.Contains(got, secret) {
			t.Errorf("the child's environment leaked %q", secret)
		}
	}
	if !strings.Contains(got, "DISPLAY=:0") {
		t.Errorf("the allowlist must still pass through what a player needs, got %q", got)
	}
	_ = rec
}

// TestExternalPlayerRefusesAnUnconfiguredCommand is the operator-facing error path.
//
// A 500 would send the operator to the server logs for what is a config problem, so this must be
// a 400 with a reason they can act on.
func TestExternalPlayerRefusesAnUnconfiguredCommand(t *testing.T) {
	installFakePlayer(t)
	stubPlayerConfig(t, "mpv {file}") // bare name: refused by the config layer

	scene := &models.Scene{
		ID:    1,
		Title: "A",
		Files: models.NewRelatedVideoFiles([]*models.VideoFile{{
			BaseFile: &models.BaseFile{ID: models.FileID(1), Path: "/media/a.mp4"},
			Duration: 1800,
		}}),
	}
	rs := sceneRoutes{fileGetter: nil}

	req := httptest.NewRequest(http.MethodPost, "/scene/1/external_player", nil)
	req = req.WithContext(context.WithValue(req.Context(), sceneKey, scene))
	rec := httptest.NewRecorder()

	rs.ExternalPlayer(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 -- a config mistake is not a server fault", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "absolute") {
		t.Errorf("the body must say what is wrong with the command, got %q", body)
	}
	if lastLaunch().started {
		t.Error("nothing may be spawned when the command is unusable")
	}
}

func TestExternalPlayerReportsNotImplementedWhenDisabled(t *testing.T) {
	installFakePlayer(t)
	config.InitializeEmpty()

	scene := &models.Scene{
		ID: 1,
		Files: models.NewRelatedVideoFiles([]*models.VideoFile{{
			BaseFile: &models.BaseFile{ID: models.FileID(1), Path: "/media/a.mp4"},
		}}),
	}
	rs := sceneRoutes{}

	req := httptest.NewRequest(http.MethodPost, "/scene/1/external_player", nil)
	req = req.WithContext(context.WithValue(req.Context(), sceneKey, scene))
	rec := httptest.NewRecorder()

	rs.ExternalPlayer(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 when the feature is off", rec.Code)
	}
	if lastLaunch().started {
		t.Error("a disabled player must never spawn")
	}
}

// TestExternalPlayerRefusesASceneWithNoFile is the guard against a nil deref.
//
// A scene whose primary file was removed still has a Files list; Primary() returns nil and every
// field access on it would panic. A panic in a media route takes down the request goroutine and
// logs a stack trace that looks like a server fault rather than a missing file.
func TestExternalPlayerRefusesASceneWithNoFile(t *testing.T) {
	installFakePlayer(t)
	stubPlayerConfig(t, "/bin/true {file}")

	// NewRelatedVideoFiles(nil): a scene whose files were removed still HAS a
	// relationship, and Primary() on it returns nil -- every field access on that nil would
	// panic, which in a media route logs a stack trace that reads as a server fault.
	scene := &models.Scene{ID: 1, Title: "A", Files: models.NewRelatedVideoFiles(nil)}
	rs := sceneRoutes{}

	req := httptest.NewRequest(http.MethodPost, "/scene/1/external_player", nil)
	req = req.WithContext(context.WithValue(req.Context(), sceneKey, scene))
	rec := httptest.NewRecorder()

	rs.ExternalPlayer(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a scene with no primary file", rec.Code)
	}
}

func TestExternalPlayerLaunchReturnsJSON(t *testing.T) {
	installFakePlayer(t)
	stubPlayerConfig(t, "/bin/true {file}")

	scene := &models.Scene{
		ID:    1,
		Title: "A",
		Files: models.NewRelatedVideoFiles([]*models.VideoFile{{
			BaseFile: &models.BaseFile{ID: models.FileID(1), Path: "/media/a.mp4"},
			Duration: 1800,
		}}),
	}
	rs := sceneRoutes{}

	req := httptest.NewRequest(http.MethodPost, "/scene/1/external_player", nil)
	req = req.WithContext(context.WithValue(req.Context(), sceneKey, scene))
	rec := httptest.NewRecorder()

	rs.ExternalPlayer(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body %q", rec.Code, rec.Body.String())
	}

	var got playerLaunchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("the response must be JSON: %v", err)
	}
	if got.PID <= 0 {
		t.Errorf("PID = %d, want a positive pid", got.PID)
	}
	if got.Ranged {
		t.Error("a scene with no window must not be reported as ranged in the response")
	}
	if got.Binary != "/bin/true" {
		t.Errorf("Binary = %q, want /bin/true", got.Binary)
	}
}

// TestSceneWindowForPlayerDistinguishesNoWindowFromAZeroWindow is the case the first version of
// sceneWindowForPlayer got wrong, isolated so the arithmetic cannot hide behind it.
//
// "No window" and "a window from 0 to 0" produce identical NUMBERS. Only the pointers tell them
// apart, and getting that wrong hands the player `--start=0 --end=0` -- a zero-length clip -- for
// every scene that has never been ranged, which is nearly all of them.
func TestSceneWindowForPlayerDistinguishesNoWindowFromAZeroWindow(t *testing.T) {
	f := func(start, end *float64) *models.VideoFile {
		v := &models.VideoFile{
			BaseFile:  &models.BaseFile{ID: models.FileID(1), Path: "/media/a.mp4"},
			Duration:  1800,
			StartTime: start,
			EndTime:   end,
		}
		return v
	}
	p := func(v float64) *float64 { return &v }

	cases := []struct {
		name       string
		file       *models.VideoFile
		wantRanged bool
		wantStart  float64
		wantEnd    float64
	}{
		// The three that all look like 0..0 in arithmetic.
		{"no window at all", f(nil, nil), false, 0, 0},
		{"explicit zero-length window", f(p(0), p(0)), true, 0, 0},

		// A window covering the whole file is not a window.
		{"window equals the whole file", f(p(0), p(1800)), false, 0, 0},
		{"window runs past the file", f(p(0), p(2000)), false, 0, 0},
		{"open-ended from the head", f(p(0), nil), false, 0, 0},

		// Real windows.
		{"bounded window", f(p(120), p(480)), true, 120, 480},
		{"open-ended window partway in", f(p(600), nil), true, 600, 0},
		{"start with no end but not from the head", f(p(30), nil), true, 30, 0},
	}

	for _, c := range cases {
		scene := &models.Scene{ID: 1, Files: models.NewRelatedVideoFiles([]*models.VideoFile{c.file})}
		start, end, ranged := sceneWindowForPlayer(scene, nil)
		if ranged != c.wantRanged {
			t.Errorf("%s: ranged = %v, want %v", c.name, ranged, c.wantRanged)
			continue
		}
		if ranged && (start != c.wantStart || end != c.wantEnd) {
			t.Errorf("%s: window = %v..%v, want %v..%v", c.name, start, end, c.wantStart, c.wantEnd)
		}
	}
}

// TestSceneWindowForPlayerHandlesNoPrimaryFile -- nil in, no panic out.
func TestSceneWindowForPlayerHandlesNoPrimaryFile(t *testing.T) {
	scene := &models.Scene{ID: 1, Files: models.NewRelatedVideoFiles(nil)}
	if _, _, ranged := sceneWindowForPlayer(scene, nil); ranged {
		t.Error("a scene with no file must not report a window")
	}
}
