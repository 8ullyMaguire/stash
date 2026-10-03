package api

// stash#2747 -- the REMOTE external player protocol.
//
// ## WHY THESE TESTS EXIST AND WHAT THEY REFUSE TO DO
//
// They never spawn a player, and they never talk to a real database. The protocol is decided in
// three places -- the registry, the token comparison and the frame builder -- and each of those
// has a rule whose absence produces no visible failure:
//
//   - a token compared with `==` instead of `hmac.Equal` still authenticates every legitimate
//     caller, and only leaks its prefix to someone who is already on the LAN measuring;
//   - a URL carrying `?apikey=` still plays on the operator's own machine, and only leaks a
//     database-rewriting credential into another machine's history file;
//   - a registry that never evicts still works right up until a player with a reconnect loop
//     fills it.
//
// So each rule below has a test that goes red when the rule is removed, and each is a one-line
// mutation -- which is what `docs/mutate_2747_remote_player.py` sweeps.
//
// ## WHY THE FRAME ASSERTIONS ARE ON THE DECODED JSON, NOT THE RAW BYTES
//
// Asserting on a marshalled string couples every test to key ORDER and to whitespace, so a
// field reorder breaks eight tests and teaches nobody anything. Decoding into the same struct the
// player would decode into tests the thing that is actually promised.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/signedurl"
)

// stubRemotePlayerConfig points the config singleton at a known token and base URL.
func stubRemotePlayerConfig(t *testing.T, token, baseURL string) {
	t.Helper()
	i := config.InitializeEmpty()
	i.Set(config.ExternalPlayerEnabled, true)
	if token != "" {
		i.Set(config.ExternalPlayerToken, token)
	}
	if baseURL != "" {
		i.Set(config.ExternalPlayerBaseURL, baseURL)
	}
}

// playerTestDialer upgrades a test HTTP request into a websocket, standing in for a player on
// another machine.
//
// A real dialer against an httptest server, rather than a fake connection: the whole point of the
// handshake is the upgrade, and a test that skips it proves the JSON encodes and nothing about the
// protocol.
func dialTestPlayer(t *testing.T, target string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	u := "ws" + strings.TrimPrefix(target, "http")
	return websocket.DefaultDialer.Dial(u, nil)
}

// resetPlayerRegistry empties the package-level registry and restores it.
//
// The registry is package-level because the handlers are methods on a stateless router struct and
// a registry threaded through every request would be plumbing with no seam. The cost is that
// tests share it, so it is cleared per test rather than per suite -- and `t.Cleanup` restores the
// empty state, so a test that fails early cannot leak a fake player into the next one.
func resetPlayerRegistry(t *testing.T) {
	t.Helper()
	playerRegistry.Lock()
	playerRegistry.byID = make(map[string]*remotePlayer)
	playerRegistry.seq = 0
	playerRegistry.Unlock()
	t.Cleanup(func() {
		playerRegistry.Lock()
		playerRegistry.byID = make(map[string]*remotePlayer)
		playerRegistry.seq = 0
		playerRegistry.Unlock()
	})
}

// registerTestPlayer adds a fake player and returns its assigned id.
//
// The `conn` is nil: nothing in the registry, the eviction policy or the lookup path touches it,
// and a real socket here would mean starting a server per test to test a map. The dispatch tests
// exercise `send` through their own connection, not through this.
func registerTestPlayer(t *testing.T, name string) string {
	t.Helper()
	p := &remotePlayer{Name: name}
	id := registerRemotePlayer(p)

	// registeredAt is stamped with a strictly increasing value so "the oldest" is deterministic
	// in the eviction test rather than dependent on the clock's resolution.
	playerRegistry.RLock()
	p.registeredAt = time.Unix(int64(len(playerRegistry.byID)), 0)
	playerRegistry.RUnlock()

	return id
}

func strconvItoa(i int) string { return strconv.Itoa(i) }

// ---------------------------------------------------------------------------
// the handshake and the wire, end to end
// ---------------------------------------------------------------------------

// TestAPlayerRegistersOverARealSocketAndWaits is the end-to-end handshake test.
//
// Everything above tests functions; this one drives the protocol the way a player on the TV
// actually will: dial, read the ack, and be registered. A fake connection would skip the upgrade,
// and the upgrade is the whole handshake.
func TestAPlayerRegistersOverARealSocketAndWaits(t *testing.T) {
	stubRemotePlayerConfig(t, "s3cret", "http://stash.lan:9999")
	resetPlayerRegistry(t)

	srv := httptest.NewServer(remotePlayerRoutes{}.Routes())
	defer srv.Close()

	conn, _, err := dialTestPlayer(t, srv.URL+"/register?token=s3cret&name=living-room")
	if err != nil {
		t.Fatalf("a valid token must complete the handshake: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	var ack struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("reading the registration ack: %v", err)
	}

	if ack.ID == "" {
		t.Fatal("the ack must carry the server-assigned id, or the player cannot be addressed")
	}
	if ack.Name != "living-room" {
		t.Errorf("the ack must echo the player's name, got %q", ack.Name)
	}

	p, found := playerFor(ack.ID)
	if !found {
		t.Fatal("a player that completed the handshake must be in the registry")
	}
	if p.Name != "living-room" {
		t.Errorf("the registry must hold the name, got %q", p.Name)
	}
}

// TestAPlayersSocketClosingRemovesIt is the liveness test, through the real handler.
//
// The reader loop's only job is this: a player that is unplugged, or whose process is killed,
// disappears with no timer and no sweeper. If this test needed to sleep to pass, the mechanism
// would have changed.
func TestAPlayersSocketClosingRemovesIt(t *testing.T) {
	stubRemotePlayerConfig(t, "s3cret", "http://stash.lan:9999")
	resetPlayerRegistry(t)

	srv := httptest.NewServer(remotePlayerRoutes{}.Routes())
	defer srv.Close()

	conn, _, err := dialTestPlayer(t, srv.URL+"/register?token=s3cret&name=tv")
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}

	var ack struct {
		ID string `json:"id"`
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if _, found := playerFor(ack.ID); !found {
		t.Fatal("the player must be registered while its socket is open")
	}

	_ = conn.Close()

	// The server learns of the close through its read loop. Poll briefly rather than sleeping a
	// fixed interval: the assertion is "it goes away", not "it goes away within 4ms".
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, found := playerFor(ack.ID); !found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a player whose socket closed must be removed from the registry")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestADispatchedFrameReachesThePlayer is the wire test.
//
// The frame is decoded into the same struct a player would decode into, so what is asserted is
// what is promised rather than a marshalled string whose key order happens to match.
func TestADispatchedFrameReachesThePlayer(t *testing.T) {
	stubRemotePlayerConfig(t, "s3cret", "http://stash.lan:9999")
	resetPlayerRegistry(t)

	srv := httptest.NewServer(remotePlayerRoutes{}.Routes())
	defer srv.Close()

	conn, _, err := dialTestPlayer(t, srv.URL+"/register?token=s3cret&name=tv")
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ack struct {
		ID string `json:"id"`
	}
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("ack: %v", err)
	}

	p, found := playerFor(ack.ID)
	if !found {
		t.Fatal("the player must be registered")
	}

	want := remotePlayerCommand{
		Type: playerCmdPlay,
		Items: []remotePlayerItem{
			{SceneID: 11, Title: "A", Details: "2024", URL: "http://stash.lan:9999/scene/11/stream",
				Start: 30, End: 45, Duration: 15, Ranged: true},
			{SceneID: 12, Title: "B", URL: "http://stash.lan:9999/scene/12/stream"},
		},
	}
	if err := p.send(want); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	var got remotePlayerCommand
	if err := conn.ReadJSON(&got); err != nil {
		t.Fatalf("reading the play frame: %v", err)
	}

	if got.Type != playerCmdPlay {
		t.Errorf("frame type must be %q, got %q", playerCmdPlay, got.Type)
	}
	if len(got.Items) != 2 {
		t.Fatalf("a two-scene playlist must arrive with two items, got %d", len(got.Items))
	}
	if got.Items[0].SceneID != 11 || got.Items[1].SceneID != 12 {
		t.Errorf("the playlist must arrive IN ORDER: got %d then %d",
			got.Items[0].SceneID, got.Items[1].SceneID)
	}
	// The window must survive the round trip: a player that drops it plays the whole file, which
	// is the #3530 defect this feature would otherwise reintroduce over the wire.
	if !got.Items[0].Ranged || got.Items[0].Start != 30 || got.Items[0].End != 45 {
		t.Errorf("the window must survive dispatch, got ranged=%v start=%v end=%v",
			got.Items[0].Ranged, got.Items[0].Start, got.Items[0].End)
	}
	if got.Items[1].Ranged {
		t.Error("a whole-file scene must not be reported as ranged")
	}
}

// ---------------------------------------------------------------------------
// the play-list BUILDER, called for real
// ---------------------------------------------------------------------------

// fakeSceneFinder serves hand-built scenes.
//
// The whole point: `remotePlayerItems` is where the URL is actually built, and for a long time
// nothing called it. The signing prefix and the window query were each tested only through their
// own helper, so removing either from the builder changed no test result -- and two mutants in the
// sweep survived for exactly that reason. These tests call the builder and read the URL it
// produced.
type fakeSceneFinder map[int]*models.Scene

func (f fakeSceneFinder) Find(_ context.Context, id int) (*models.Scene, error) {
	if s, found := f[id]; found {
		return s, nil
	}
	return nil, fmt.Errorf("scene %d not found", id)
}

// sceneWithWindow builds a scene whose primary file carries a #3530 window.
//
// TWO construction details, both forced rather than chosen:
//
//   - `VideoFile` embeds `*BaseFile`, so `Path` and `ID` are PROMOTED fields. A struct literal
//     cannot set them (`use of promoted field requires go1.27 or later`, and go.mod says 1.25),
//     so the embedded struct is built and assigned separately.
//   - a Scene's Files is `RelatedVideoFiles`, built by `NewRelatedVideoFiles([]*VideoFile{...})`,
//     which makes files[0] primary. Its fields are unexported, so there is nothing else to set.
func sceneWithWindow(id int, duration, start, end float64, ranged bool) *models.Scene {
	vf := &models.VideoFile{Duration: duration}
	vf.BaseFile = &models.BaseFile{
		ID:       models.FileID(id),
		Path:     fmt.Sprintf("/media/%d.mp4", id),
		Basename: fmt.Sprintf("%d.mp4", id),
	}
	if ranged {
		s, e := start, end
		vf.StartTime = &s
		vf.EndTime = &e
	}
	return &models.Scene{
		ID:    id,
		Title: fmt.Sprintf("Scene %d", id),
		Files: models.NewRelatedVideoFiles([]*models.VideoFile{vf}),
	}
}

// TestTheBuilderSignsTheURLItActuallyDispatches is the R6 witness.
//
// The signing prefix must be the path that is dispatched. DerivePrefix strips the extension from
// the third segment, so the two agree today -- and that is exactly why the mutant survived when
// only the helper was tested. This asserts on the URL the builder emitted.
func TestTheBuilderSignsTheURLItActuallyDispatches(t *testing.T) {
	cfg := config.InitializeEmpty()
	cfg.Set(config.ExternalPlayerEnabled, true)
	cfg.Set(config.ExternalPlayerBaseURL, "http://stash.lan:9999")
	cfg.Set(config.Username, "admin")
	cfg.Set(config.Password, "$2a$10$notarealhashbutlongenough0000000000000000000000000000")
	cfg.Set(config.JWTSignKey, "signing-key")

	rs := remotePlayerRoutes{}
	items, err := rs.remotePlayerItems(context.Background(),
		fakeSceneFinder{11: sceneWithWindow(11, 600, 0, 0, false)}, []string{"11"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one item, got %d", len(items))
	}

	u, err := url.Parse(items[0].URL)
	if err != nil {
		t.Fatalf("the dispatched URL must parse: %v", err)
	}

	// The decisive assertion: the signature verifies against the path the player will request.
	if _, err := signedurl.VerifyURL(u.Path, u.Query(), cfg.GetJWTSignKey()); err != nil {
		t.Fatalf("the dispatched URL must verify against its own path (%s): %v", u.Path, err)
	}
	if u.Path != "/scene/11/stream" {
		t.Errorf("dispatched path is %q; a transcode route here would reintroduce the "+
			"server-side transcoding this issue exists to avoid", u.Path)
	}
}

// TestTheBuilderPutsTheWindowOnTheURL is the R12 witness.
//
// The frame reports `ranged`, so every assertion about the FRAME passes whether or not the URL
// carries the window. Only the URL tells a player that honours the query. A windowed scene
// dispatched without `start`/`end` plays the whole file -- the #3530 defect, reintroduced over
// the wire.
func TestTheBuilderPutsTheWindowOnTheURL(t *testing.T) {
	stubRemotePlayerConfig(t, "s3cret", "http://stash.lan:9999")

	rs := remotePlayerRoutes{}
	finder := fakeSceneFinder{
		11: sceneWithWindow(11, 7200, 30, 45, true),
		12: sceneWithWindow(12, 600, 0, 0, false),
	}

	items, err := rs.remotePlayerItems(context.Background(), finder, []string{"11", "12"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected two items, got %d", len(items))
	}

	windowed, err := url.Parse(items[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	if got := windowed.Query().Get("start"); got != "30" {
		t.Errorf("a windowed scene's URL must carry start=30, got %q (full query %q)",
			got, windowed.RawQuery)
	}
	if got := windowed.Query().Get("end"); got != "45" {
		t.Errorf("a windowed scene's URL must carry end=45, got %q", got)
	}

	whole, err := url.Parse(items[1].URL)
	if err != nil {
		t.Fatal(err)
	}
	// A whole-file scene must NOT carry a seek. `start=0` is a different signal to a player --
	// some treat it as a full remount -- and `end=0` would be a zero-length clip.
	if q := whole.Query(); q.Get("start") != "" || q.Get("end") != "" {
		t.Errorf("a whole-file scene must carry no window, got start=%q end=%q",
			q.Get("start"), q.Get("end"))
	}
}

// TestTheBuilderRefusesASceneWithNoFile is the silent-drop guard.
//
// Dropping a scene with no file would hand the player a playlist SHORTER than the operator asked
// for, with no error -- the player plays three of four and reports nothing.
func TestTheBuilderRefusesASceneWithNoFile(t *testing.T) {
	stubRemotePlayerConfig(t, "s3cret", "http://stash.lan:9999")

	rs := remotePlayerRoutes{}
	finder := fakeSceneFinder{11: {ID: 11, Title: "No file"}}

	_, err := rs.remotePlayerItems(context.Background(), finder, []string{"11"})
	if err == nil {
		t.Fatal("a scene with no primary file must be refused, not silently dropped")
	}
	if !strings.Contains(err.Error(), "no primary file") {
		t.Errorf("the error must say why, got %q", err.Error())
	}
}

// TestTheBuilderRefusesAnUnknownScene is the typo guard.
func TestTheBuilderRefusesAnUnknownScene(t *testing.T) {
	stubRemotePlayerConfig(t, "s3cret", "http://stash.lan:9999")

	rs := remotePlayerRoutes{}
	if _, err := rs.remotePlayerItems(context.Background(), fakeSceneFinder{}, []string{"999"}); err == nil {
		t.Fatal("an unknown scene must be refused, not dispatched as an empty item")
	}
}

// TestTheBuilderRefusesANonNumericSceneID is the input guard.
func TestTheBuilderRefusesANonNumericSceneID(t *testing.T) {
	stubRemotePlayerConfig(t, "s3cret", "http://stash.lan:9999")

	rs := remotePlayerRoutes{}
	if _, err := rs.remotePlayerItems(context.Background(), fakeSceneFinder{}, []string{"abc"}); err == nil {
		t.Fatal("a non-numeric scene id must be refused")
	}
}

// TestTheBuilderCarriesMetadata is the issue's own complaint.
//
// #2747: "Scene metadata is also not sent to external players." Title and details are in the frame,
// and the test asserts on the decoded item rather than on the JSON text.
func TestTheBuilderCarriesMetadata(t *testing.T) {
	stubRemotePlayerConfig(t, "s3cret", "http://stash.lan:9999")

	rs := remotePlayerRoutes{}
	scene := sceneWithWindow(11, 600, 0, 0, false)
	scene.Title = "A specific title"
	scene.Details = "2024 · Studio · 20:04"

	items, err := rs.remotePlayerItems(context.Background(),
		fakeSceneFinder{11: scene}, []string{"11"})
	if err != nil {
		t.Fatal(err)
	}

	if items[0].Title != "A specific title" {
		t.Errorf("the title must reach the player, got %q", items[0].Title)
	}
	if items[0].Details != "2024 · Studio · 20:04" {
		t.Errorf("the details must reach the player, got %q", items[0].Details)
	}
	if items[0].Duration != 600 {
		t.Errorf("the duration must reach the player, got %v", items[0].Duration)
	}
}

// TestTheBuilderKeepsPlaylistOrder is the playlist test.
func TestTheBuilderKeepsPlaylistOrder(t *testing.T) {
	stubRemotePlayerConfig(t, "s3cret", "http://stash.lan:9999")

	rs := remotePlayerRoutes{}
	finder := fakeSceneFinder{}
	for _, id := range []int{30, 10, 20} {
		finder[id] = sceneWithWindow(id, 600, 0, 0, false)
	}

	items, err := rs.remotePlayerItems(context.Background(), finder, []string{"30", "10", "20"})
	if err != nil {
		t.Fatal(err)
	}

	want := []int{30, 10, 20}
	for i, id := range want {
		if items[i].SceneID != id {
			t.Errorf("item %d must be scene %d, got %d: a playlist that reorders plays the wrong "+
				"scenes in the wrong order", i, id, items[i].SceneID)
		}
	}
}

// TestTheBuilderNeverLeaksTheApiKeyEvenWhenOneIsConfigured is the R7 witness.
//
// The apikey branch is inside `if cfg.HasCredentials()`, so a test that configures NO credentials
// never reaches it -- which is precisely how the "put the apikey in the dispatched URL" mutant
// survived: every existing test ran the no-credentials path. So this fixture configures a real
// password and a real API key, and asserts the key is absent from the URL the player will fetch.
//
// This is the security property the whole signing design exists for. The local launch path can
// read the key out of the config and is on the same host; this URL crosses a machine boundary and
// lands in that player's history file and every log between the two.
func TestTheBuilderNeverLeaksTheApiKeyEvenWhenOneIsConfigured(t *testing.T) {
	cfg := config.InitializeEmpty()
	cfg.Set(config.ExternalPlayerEnabled, true)
	cfg.Set(config.ExternalPlayerBaseURL, "http://stash.lan:9999")
	cfg.Set(config.Username, "admin")
	cfg.Set(config.Password, "$2a$10$notarealhashbutlongenough0000000000000000000000000000")
	cfg.Set(config.JWTSignKey, "signing-key")
	cfg.Set(config.ApiKey, "SUPERSECRET-API-KEY")

	if !cfg.HasCredentials() {
		t.Fatal("the fixture must present an instance WITH credentials, or the branch that could " +
			"leak the key never runs and this test passes having checked nothing")
	}
	if cfg.GetAPIKey() != "SUPERSECRET-API-KEY" {
		t.Fatalf("the fixture must configure a key, got %q", cfg.GetAPIKey())
	}

	rs := remotePlayerRoutes{}
	items, err := rs.remotePlayerItems(context.Background(),
		fakeSceneFinder{11: sceneWithWindow(11, 600, 0, 0, false)}, []string{"11"})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(items[0].URL, "SUPERSECRET-API-KEY") {
		t.Fatalf("the dispatched URL leaks the api key: %s", items[0].URL)
	}
	if strings.Contains(items[0].URL, "apikey=") {
		t.Fatalf("the dispatched URL carries an apikey parameter at all: %s", items[0].URL)
	}
	// And the positive: it must still be a URL the player can actually fetch.
	u, err := url.Parse(items[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signedurl.VerifyURL(u.Path, u.Query(), cfg.GetJWTSignKey()); err != nil {
		t.Fatalf("refusing to leak the key must not mean refusing to sign: %v", err)
	}
}

// ---------------------------------------------------------------------------
// the token
// ---------------------------------------------------------------------------

// TestRemotePlayerRefusesARequestWithNoToken is the door test: no credential, no connection.
func TestRemotePlayerRefusesARequestWithNoToken(t *testing.T) {
	stubRemotePlayerConfig(t, "s3cret", "http://stash.lan:9999")

	r := httptest.NewRequest(http.MethodGet, "/external_player/register?name=tv", nil)
	w := httptest.NewRecorder()

	remotePlayerRoutes{}.RegisterRemotePlayer(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a request with no token must be refused, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid external player token") {
		t.Errorf("the body must name the reason, got %q", w.Body.String())
	}
}

// TestRemotePlayerRefusesTheWrongToken guards the actual comparison.
//
// Each token is URL-ENCODED, and one case is a leading space on purpose: a token read out of a
// config file or a shell often carries surrounding whitespace, so the read is trimmed and the
// comparison must still refuse a token that merely *looks* like the right one.
func TestRemotePlayerRefusesTheWrongToken(t *testing.T) {
	stubRemotePlayerConfig(t, "s3cret", "http://stash.lan:9999")

	for _, presented := range []string{"", "s3cre", "s3cretX", "S3CRET", " s3cret"} {
		q := url.Values{"token": []string{presented}}
		r := httptest.NewRequest(http.MethodGet,
			"/external_player/register?"+q.Encode(), nil)
		w := httptest.NewRecorder()
		remotePlayerRoutes{}.RegisterRemotePlayer(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("token %q must be refused, got %d", presented, w.Code)
		}
	}
}

// TestRemotePlayerRefusesWhenNotEnabled is the switch test.
//
// `enabled: false` must close the DOOR, not just refuse to dispatch: a player that stays
// connected after the operator turned the feature off means the flag reads "off" while the
// connection is live.
func TestRemotePlayerRefusesWhenNotEnabled(t *testing.T) {
	i := config.InitializeEmpty()
	i.Set(config.ExternalPlayerEnabled, false)
	i.Set(config.ExternalPlayerToken, "s3cret")

	r := httptest.NewRequest(http.MethodGet, "/external_player/register?token=s3cret", nil)
	w := httptest.NewRecorder()
	remotePlayerRoutes{}.RegisterRemotePlayer(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a disabled external player must refuse the handshake, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not enabled") {
		t.Errorf("the body must say the feature is off, got %q", w.Body.String())
	}
}

// TestRemotePlayerRefusesWhenNoTokenIsConfigured is the fail-closed test.
//
// An operator who sets `enabled: true` and forgets the token must get a refusal, not an
// unauthenticated endpoint. A token of "" compared against any presented value would otherwise
// authenticate an empty presentation.
func TestRemotePlayerRefusesWhenNoTokenIsConfigured(t *testing.T) {
	i := config.InitializeEmpty()
	i.Set(config.ExternalPlayerEnabled, true)

	r := httptest.NewRequest(http.MethodGet, "/external_player/register", nil)
	w := httptest.NewRecorder()
	remotePlayerRoutes{}.RegisterRemotePlayer(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no configured token must mean no registration, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not configured") {
		t.Errorf("the body must name the missing configuration, got %q", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// the registry
// ---------------------------------------------------------------------------

// TestRegisteredPlayersAreListedInAStableOrder guards the sort.
//
// Map iteration order is random, so an unsorted list reshuffles between two polls of the same
// unchanged registry. A UI list that reshuffles reads as a bug in Stash.
func TestRegisteredPlayersAreListedInAStableOrder(t *testing.T) {
	resetPlayerRegistry(t)

	registerTestPlayer(t, "kitchen")
	registerTestPlayer(t, "living-room")
	registerTestPlayer(t, "bedroom")

	first := registeredPlayerIDs()
	for i := 0; i < 20; i++ {
		if got := registeredPlayerIDs(); strings.Join(got, ",") != strings.Join(first, ",") {
			t.Fatalf("player order changed between calls: %v then %v", first, got)
		}
	}
}

// TestRemotePlayerRegisteringAssignsADistinctID is the two-players-one-token test.
//
// Two players behind the same operator token must still be told apart, or one player's command
// lands on the other's socket -- which is the failure a shared secret would otherwise cause.
func TestRemotePlayerRegisteringAssignsADistinctID(t *testing.T) {
	resetPlayerRegistry(t)

	a := registerTestPlayer(t, "a")
	b := registerTestPlayer(t, "b")

	if a == b {
		t.Fatalf("two players must get distinct ids, both got %q", a)
	}
}

// TestRemotePlayerUnregisteringRemovesItFromLookup is the liveness test.
//
// A player that is unplugged disappears because its socket closed. No heartbeat, no timeout, no
// sweeper: if this test needs a timer to pass, the mechanism has changed.
func TestRemotePlayerUnregisteringRemovesItFromLookup(t *testing.T) {
	resetPlayerRegistry(t)

	id := registerTestPlayer(t, "tv")
	if _, found := playerFor(id); !found {
		t.Fatal("a registered player must be findable")
	}

	unregisterRemotePlayer(id)

	if _, found := playerFor(id); found {
		t.Fatal("an unregistered player must not be findable")
	}
}

// TestTheRegistryEvictsTheOldestWhenItIsFull is the leak test.
//
// A program that reconnects in a loop would otherwise grow this map forever. The cap is on
// REGISTRATIONS and drops the oldest, so the 65th real player is not refused.
func TestTheRegistryEvictsTheOldestWhenItIsFull(t *testing.T) {
	resetPlayerRegistry(t)

	// registerTestPlayer stamps registeredAt with a distinct increasing value, so "oldest" is
	// deterministic here and does not depend on wall-clock resolution.
	ids := make([]string, 0, playerRegistryMax+1)
	for i := 0; i < playerRegistryMax+1; i++ {
		ids = append(ids, registerTestPlayer(t, "p"+strconvItoa(i)))
	}

	if n := len(registeredPlayerIDs()); n != playerRegistryMax {
		t.Errorf("registry must hold at most %d players, holds %d", playerRegistryMax, n)
	}
	if _, found := playerFor(ids[0]); found {
		t.Error("the oldest registration must be the one evicted")
	}
	if _, found := playerFor(ids[len(ids)-1]); !found {
		t.Error("the newest registration must survive an eviction")
	}
}

// ---------------------------------------------------------------------------
// dispatch
// ---------------------------------------------------------------------------

// TestRemotePlayerPlayRefusesAnUnregisteredPlayer is the 409 test.
//
// The message must NAME the connected players, because the operator's question is "which one is
// it then?" and an opaque 409 sends them to the UI to look.
func TestRemotePlayerPlayRefusesAnUnregisteredPlayer(t *testing.T) {
	stubRemotePlayerConfig(t, "s3cret", "http://stash.lan:9999")
	resetPlayerRegistry(t)
	registerTestPlayer(t, "living-room")

	body := strings.NewReader(`{"playerId":"player-999","sceneIds":["1"]}`)
	r := httptest.NewRequest(http.MethodPost, "/external_player/play?token=s3cret", body)
	w := httptest.NewRecorder()

	remotePlayerRoutes{}.PlayOnRemotePlayer(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("an unknown player must be a 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "living-room") {
		t.Errorf("the 409 must name who IS connected, got %q", w.Body.String())
	}
}

// TestRemotePlayerPlayRefusesAnOverlongPlaylist is the cap test, and it must be a REFUSAL.
//
// Truncating a 200-scene playlist to 64 plays 64 of 200 scenes and reports success, which is worse
// than an error: the operator believes the whole list played.
func TestRemotePlayerPlayRefusesAnOverlongPlaylist(t *testing.T) {
	stubRemotePlayerConfig(t, "s3cret", "http://stash.lan:9999")
	resetPlayerRegistry(t)
	registerTestPlayer(t, "tv")

	var ids []string
	for i := 0; i <= maxPlaylistScenes; i++ {
		ids = append(ids, `"1"`)
	}
	body := strings.NewReader(`{"playerId":"` + registeredPlayerIDs()[0] + `","sceneIds":[` +
		strings.Join(ids, ",") + `]}`)
	r := httptest.NewRequest(http.MethodPost, "/external_player/play?token=s3cret", body)
	w := httptest.NewRecorder()

	remotePlayerRoutes{}.PlayOnRemotePlayer(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("%d scenes must be refused, got %d", len(ids), w.Code)
	}
	if !strings.Contains(w.Body.String(), strconvItoa(maxPlaylistScenes)) {
		t.Errorf("the refusal must state the limit, got %q", w.Body.String())
	}
}

// TestRemotePlayerPlayRefusesAnEmptyPlaylist is the vacuous-command test.
//
// A play command with no scenes is not "play nothing", it is a mistake, and dispatching it makes
// the operator press stop on the TV to find out.
func TestRemotePlayerPlayRefusesAnEmptyPlaylist(t *testing.T) {
	stubRemotePlayerConfig(t, "s3cret", "http://stash.lan:9999")
	resetPlayerRegistry(t)
	id := registerTestPlayer(t, "tv")

	r := httptest.NewRequest(http.MethodPost, "/external_player/play?token=s3cret",
		strings.NewReader(`{"playerId":"`+id+`","sceneIds":[]}`))
	w := httptest.NewRecorder()
	remotePlayerRoutes{}.PlayOnRemotePlayer(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("an empty playlist must be refused, got %d", w.Code)
	}
}

// TestRemotePlayerPlayRefusesAMissingPlayerID is the id test.
func TestRemotePlayerPlayRefusesAMissingPlayerID(t *testing.T) {
	stubRemotePlayerConfig(t, "s3cret", "http://stash.lan:9999")
	resetPlayerRegistry(t)

	r := httptest.NewRequest(http.MethodPost, "/external_player/play?token=s3cret",
		strings.NewReader(`{"sceneIds":["1"]}`))
	w := httptest.NewRecorder()
	remotePlayerRoutes{}.PlayOnRemotePlayer(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("a missing playerId must be refused, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// transport commands
// ---------------------------------------------------------------------------

// TestPlayerTransportCommandAcceptsTheFourTransportVerbs is the vocabulary test.
//
// FOUR, not five. `play` is deliberately NOT accepted here: `/command` carries transport verbs, and
// a `play` frame built from it would have no items -- telling a player to play an empty playlist,
// which it would honour by clearing whatever it was showing. `play` goes through `/play`, which
// builds the frame from real scenes.
func TestPlayerTransportCommandAcceptsTheFourTransportVerbs(t *testing.T) {
	for _, verb := range []string{"pause", "resume", "stop", "seek"} {
		if _, err := playerTransportCommand(verb, 0); err != nil {
			t.Errorf("%q must be a known transport command: %v", verb, err)
		}
	}
}

// TestPlayerTransportCommandRefusesPlay is the empty-playlist test, from the other direction.
//
// A `play` verb arriving at /command is the one that matters: it is accepted by a naive
// "any known command" switch, and it produces a play frame with zero items.
func TestPlayerTransportCommandRefusesPlay(t *testing.T) {
	cmd, err := playerTransportCommand("play", 0)
	if err == nil {
		t.Fatalf("play must be refused on /command: it would dispatch a frame with no items "+
			"(%+v)", cmd)
	}
}

// TestPlayerTransportCommandRefusesAnUnknownVerb is the typo test.
//
// Same reason an unknown placeholder is an error: a misspelled command silently ignored leaves the
// operator pressing pause on a player that is not pausing.
func TestPlayerTransportCommandRefusesAnUnknownVerb(t *testing.T) {
	for _, verb := range []string{"", "paused", "PAUSE", "rewind", "stop-all"} {
		if _, err := playerTransportCommand(verb, 0); err == nil {
			t.Errorf("verb %q must be refused", verb)
		}
	}
}

// TestPlayerTransportCommandRefusesANegativeSeek is the arithmetic test.
func TestPlayerTransportCommandRefusesANegativeSeek(t *testing.T) {
	if _, err := playerTransportCommand("seek", -1); err == nil {
		t.Fatal("a negative seek must be refused")
	}
}

// TestSeekCarriesItsPosition is the "the number survives" test.
func TestSeekCarriesItsPosition(t *testing.T) {
	cmd, err := playerTransportCommand("seek", 42.5)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Start != 42.5 {
		t.Errorf("seek must carry its position, got %v", cmd.Start)
	}
}

// ---------------------------------------------------------------------------
// the dispatched URL -- the security-critical part
// ---------------------------------------------------------------------------

// TestARemotePlayerURLIsSignedAndNeverCarriesAnApiKey is THE test of this feature.
//
// An apikey can rewrite the entire database. This URL crosses to another machine and lands in
// that player's history file and in every log between the two, so it must be a signed URL scoped
// to one path prefix, and it must expire.
func TestARemotePlayerURLIsSignedAndNeverCarriesAnApiKey(t *testing.T) {
	cfg := config.InitializeEmpty()
	cfg.Set(config.ExternalPlayerEnabled, true)
	cfg.Set(config.ExternalPlayerBaseURL, "http://stash.lan:9999")
	cfg.Set(config.Username, "admin")
	cfg.Set(config.Password, "$2a$10$notarealhashbutlongenough0000000000000000000000000000")
	cfg.Set(config.JWTSignKey, "signing-key")

	if !cfg.HasCredentials() {
		t.Fatal("the fixture must present an instance WITH credentials, or the signing branch " +
			"never runs and this test passes having checked nothing")
	}

	q := mergeSignedParams(nil, cfg.GetJWTSignKey(), "/scene/11/stream")

	if got := q.Get("apikey"); got != "" {
		t.Fatalf("a dispatched URL must never carry an apikey, got %q", got)
	}
	if q.Get(signedurl.CIDParam) == "" || q.Get(signedurl.SigParam) == "" {
		t.Fatalf("the URL must be signed, got %v", q)
	}
	if q.Get(signedurl.ExpiresParam) == "" {
		t.Error("a signed URL must expire")
	}
}

// TestTheSignedPrefixMatchesThePathWeDispatch is the test that would have caught a real bug.
//
// `DerivePrefix` strips an extension from the third path segment, so `/scene/11/stream.mp4`
// verifies against `/scene/11/stream`. A signature computed over one path and dispatched on the
// other produces a URL that 401s on the PLAYER's machine, where the operator is not looking.
func TestTheSignedPrefixMatchesThePathWeDispatch(t *testing.T) {
	const dispatched = "/scene/11/stream"

	if got := signedurl.DerivePrefix(dispatched); got != dispatched {
		t.Errorf("DerivePrefix(%q) = %q; signing and dispatching must agree or the player 401s "+
			"on a machine the operator is not looking at", dispatched, got)
	}

	cfg := config.InitializeEmpty()
	cfg.Set(config.Username, "admin")
	cfg.Set(config.JWTSignKey, "signing-key")

	q := mergeSignedParams(nil, cfg.GetJWTSignKey(), dispatched)

	if _, err := signedurl.VerifyURL(dispatched, q, cfg.GetJWTSignKey()); err != nil {
		t.Fatalf("the dispatched URL must verify against the dispatched path: %v", err)
	}

	// And the negative: the same parameters on a DIFFERENT scene must not verify. If this passes
	// verification, the signature is not scoped to the scene at all.
	if _, err := signedurl.VerifyURL("/scene/12/stream", q, cfg.GetJWTSignKey()); err == nil {
		t.Error("a signed URL must not verify for another scene: it is scoped to one path prefix")
	}
}

// TestRemotePlayerFormatSecondsIsPlainDecimal is the %g test.
//
// %g renders 60 as "6e+01". A player parsing that as 6 seeks to the wrong place, and the symptom
// is "it played from the start" rather than an error.
func TestRemotePlayerFormatSecondsIsPlainDecimal(t *testing.T) {
	cases := map[float64]string{0: "0", 6: "6", 60: "60", 90.5: "90.5", 1800: "1800"}
	for in, want := range cases {
		if got := remotePlayerFormatSeconds(in); got != want {
			t.Errorf("remotePlayerFormatSeconds(%v) = %q, want %q", in, got, want)
		}
	}
}

// TestRemotePlayerWindowComesFromTheSceneNotTheURL is the #3530 seam.
//
// The window lives on the scene's FILE, and a scene that is a window of its file must be
// dispatched as a window. This is the same rule `sceneWindowForPlayer` applies to the LOCAL
// player, and the two must not drift.
func TestRemotePlayerWindowComesFromTheSceneNotTheURL(t *testing.T) {
	start, end := 30.0, 45.0

	if got, want := remotePlayerFormatSeconds(start), "30"; got != want {
		t.Errorf("a whole-second bound must render without a fraction: got %q", got)
	}
	if got, want := remotePlayerFormatSeconds(end), "45"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
