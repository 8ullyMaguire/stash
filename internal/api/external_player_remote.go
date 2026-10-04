package api

// stash#2747 -- the REMOTE external player: a program on another machine that connects to
// Stash, waits, and is told what to play.
//
// ## WHY THIS EXISTS AT ALL, WHEN A LOCAL LAUNCHER ALSO EXISTS
//
// Three different features have been called #2747 and only one of them is the issue:
//
//   - upstream's `ExternalPlayerButton.tsx` (3d1b949f4, PR #679) is a CLIENT-side url-scheme
//     handoff -- `vlc-x-callback:`, Android `intent:` -- which spawns nothing;
//   - `external_player.go`'s `ExternalPlayer` handler spawns a LOCAL player from a command
//     template, on the Stash host;
//   - THIS: a player on the LAN connects, Stash decides what to send it, and a play command
//     carries metadata and a playlist. Issue #2747's own words: "API that would allow
//     external players connect to Stash and wait for play command."
//
// They are additive, not competing. One answers "start a program here", the other answers
// "there is a TV in the living room and it should play this".
//
// ## WHY A REGISTRY AND NOT A LOOKUP BY TOKEN
//
// The player holds a socket, so the socket IS the player's identity. A registry keyed by an
// opaque server-assigned id is the whole design, and every property below follows from it:
//
//   - a player that dies is removed by its own socket closing, with no polling and no heartbeat
//     protocol to get wrong;
//   - a command cannot be delivered to "whoever has this token" because there is exactly one
//     socket per registration, so two players sharing an operator token are still told apart;
//   - the caller's `playerId` is a LOOKUP KEY and is validated against the registry rather than
//     trusted, so one player's commands cannot land on another's socket.
//
// ## WHY THE TOKEN IS COMPARED IN CONSTANT TIME
//
// `hmac.Equal` rather than `==`. The token is compared on a network-reachable endpoint, and a
// byte-by-byte `==` leaks its prefix through timing -- which is a real technique against an
// endpoint that answers 401 fast and 403 slow, or vice versa. This is the only new secret-
// comparison in the feature, so it is the only place the rule has to be remembered.
//
// ## WHY `enabled` GATES REGISTRATION AND NOT JUST DISPATCH
//
// A switch that stops Stash sending commands to an already-connected player would leave the
// door open: the program on the LAN stays connected and keeps waiting, and `enabled: false`
// reads like "off" while the connection is live. Gating the handshake makes the flag mean what
// an operator expects it to mean.

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/signedurl"
)

// playerCommandType is the `type` field of a dispatched frame.
type playerCommandType string

const (
	playerCmdPlay   playerCommandType = "play"
	playerCmdPause  playerCommandType = "pause"
	playerCmdResume playerCommandType = "resume"
	playerCmdStop   playerCommandType = "stop"
	playerCmdSeek   playerCommandType = "seek"
)

// maxPlaylistScenes bounds a single play command.
//
// REFUSED above this, not truncated. A truncated playlist plays 63 of 200 scenes and reports
// success; a refusal says "that list was too long", which is the truth and is actionable.
const maxPlaylistScenes = 64

// playerDispatchTimeout bounds how long a dispatch may block on a socket.
//
// A player on a sleeping TV holds a TCP connection whose peer has stopped responding, and a
// blocking write on that socket hangs the HTTP request that asked for playback. The dispatch is
// therefore asynchronous and the caller is told "sent", never "playing" -- see the spec's §5.
const playerDispatchTimeout = 5 * time.Second

// remotePlayerItem is one scene in a play command.
type remotePlayerItem struct {
	SceneID  int     `json:"sceneId"`
	Title    string  `json:"title"`
	Details  string  `json:"details"`
	URL      string  `json:"url"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	Duration float64 `json:"duration"`
	Ranged   bool    `json:"ranged"`
}

// remotePlayerCommand is the frame Stash sends down a player's socket.
type remotePlayerCommand struct {
	Type  playerCommandType  `json:"type"`
	Items []remotePlayerItem `json:"items,omitempty"`
	Start float64            `json:"start,omitempty"`
}

// remotePlayer is one registered connection.
type remotePlayer struct {
	ID   string
	Name string

	conn *websocket.Conn

	// writeMu serialises writes. gorilla/websocket permits ONE concurrent writer per
	// connection, and a player sending `play` from two browser tabs at once would otherwise
	// interleave two frames on one socket -- which the player's parser reads as corruption.
	writeMu sync.Mutex

	registeredAt time.Time
}

// playerRegistry holds the connected players.
//
// Bounded, because a program that reconnects in a loop is a program that would otherwise grow
// this map forever. The cap is on REGISTRATIONS, not on bytes, and the oldest is dropped -- a
// player that re-registers more than 64 times without a successful command has something
// wrong with it, and evicting it is better than refusing the 65th real player.
const playerRegistryMax = 64

var playerRegistry = struct {
	sync.RWMutex
	byID map[string]*remotePlayer
	seq  int
}{byID: make(map[string]*remotePlayer)}

// registerRemotePlayer adds a connection and returns its assigned id.
//
// The id is server-assigned and opaque precisely so a caller cannot choose it: a caller-supplied
// name is display data, and letting the caller pick the KEY is how one player's commands land on
// another's socket.
func registerRemotePlayer(p *remotePlayer) string {
	playerRegistry.Lock()
	defer playerRegistry.Unlock()

	playerRegistry.seq++
	p.ID = "player-" + strconv.Itoa(playerRegistry.seq)
	p.registeredAt = time.Now()
	playerRegistry.byID[p.ID] = p

	for len(playerRegistry.byID) > playerRegistryMax {
		var oldestID string
		var oldest time.Time
		for id, q := range playerRegistry.byID {
			if q == p {
				continue
			}
			if oldestID == "" || q.registeredAt.Before(oldest) {
				oldestID, oldest = id, q.registeredAt
			}
		}
		if oldestID == "" {
			break
		}
		logger.Warnf("#2747: evicting player %q, registry is full", oldestID)
		delete(playerRegistry.byID, oldestID)
	}

	return p.ID
}

// unregisterRemotePlayer removes a connection.
//
// Called from the reader goroutine when the socket closes, so a player that is unplugged
// disappears without any part of Stash having to notice.
func unregisterRemotePlayer(id string) {
	playerRegistry.Lock()
	defer playerRegistry.Unlock()
	delete(playerRegistry.byID, id)
}

// playerFor finds a registered player.
func playerFor(id string) (*remotePlayer, bool) {
	playerRegistry.RLock()
	defer playerRegistry.RUnlock()
	p, found := playerRegistry.byID[id]
	return p, found
}

// registeredPlayerIDs lists the connected players, sorted so the log and the API response are
// stable between calls. Map iteration order is random, and a UI listing players that reshuffles
// on every poll reads as a bug in Stash.
func registeredPlayerIDs() []string {
	playerRegistry.RLock()
	defer playerRegistry.RUnlock()
	ids := make([]string, 0, len(playerRegistry.byID))
	for id := range playerRegistry.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// remotePlayerUpgrader upgrades the registration handshake.
//
// Origin is NOT checked, deliberately: the peer is a media player program, not a browser, and it
// sends no Origin at all. A browser-origin policy here would refuse exactly the client this
// feature exists for while blocking nothing that matters -- the handshake is behind a token, and
// the socket carries no ambient authority (no cookie, no session).
var remotePlayerUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// authenticateRemotePlayer compares the presented token against the configured one.
//
// Returns a REASON because the two failures are different problems: "not enabled" is nothing to
// show an operator, while "wrong token" is the one thing a player author needs to see.
//
// hmac.Equal, never `==`: see the file comment.
func authenticateRemotePlayer(r *http.Request) (ok bool, reason string) {
	c := config.GetInstance()
	if !c.GetExternalPlayerEnabled() {
		return false, "external player is not enabled"
	}

	want := c.GetExternalPlayerToken()
	if want == "" {
		return false, "external_player.token is not configured"
	}

	got := r.URL.Query().Get("token")
	if !hmacEqualString(got, want) {
		return false, "invalid external player token"
	}
	return true, ""
}

// remotePlayerWriteJSON writes one ack frame to a freshly-upgraded socket.
//
// `conn.WriteJSON`, not `json.NewEncoder(conn).Encode`: gorilla's Conn has its own WriteJSON and
// does NOT implement io.Writer -- its Write takes a message TYPE as its first argument. Handing it
// to an encoder does not compile, which is the good outcome; the tempting fix is to wrap it in an
// adapter that silently picks the wrong opcode.
func remotePlayerWriteJSON(conn *websocket.Conn, v interface{}) error {
	return conn.WriteJSON(v)
}

// connectedPlayerNames lists the connected players as "id (name)" pairs, for an error body.
//
// NAMES, not ids. The operator's question is "which one is it then?", and they recognise their TV
// as "living-room" and have never seen the id `player-3` -- which is assigned by a server-side
// counter and means nothing outside the log. So the message carries both, and the id is there
// for a caller who wants to script against it.
func connectedPlayerNames() string {
	ids := registeredPlayerIDs()
	if len(ids) == 0 {
		return "none"
	}

	playerRegistry.RLock()
	defer playerRegistry.RUnlock()

	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		if n := playerRegistry.byID[id].Name; n != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", n, id))
		} else {
			parts = append(parts, id)
		}
	}
	return strings.Join(parts, ", ")
}

// RegisterRemotePlayer is the handshake: a player announces itself and waits.
//
//	GET /external_player/register?token=...&name=living-room
//	-> {"id":"player-1","name":"living-room"}
//	and the socket stays open, carrying play commands.
func (rs remotePlayerRoutes) RegisterRemotePlayer(w http.ResponseWriter, r *http.Request) {
	if ok, reason := authenticateRemotePlayer(r); !ok {
		// 401 for a bad token and 403 for "off" would be tidier, but both are "you may not
		// connect" and one code is one thing to get wrong. The reason is in the body, which is
		// where a player author looks.
		http.Error(w, reason, http.StatusUnauthorized)
		return
	}

	conn, err := remotePlayerUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written a response.
		logger.Debugf("#2747: player upgrade failed: %v", err)
		return
	}

	// The name is display data and is bounded: it goes into logs and into the operator's player
	// picker, so an unbounded string from the network is a log-forging and layout hazard.
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if len(name) > 64 {
		name = name[:64]
	}

	p := &remotePlayer{Name: name, conn: conn}
	id := registerRemotePlayer(p)
	logger.Infof("#2747: player %q registered as %s (%d connected)", name, id, len(registeredPlayerIDs()))

	// The ack goes out through sendRaw, which takes the SAME writeMu that dispatch uses, rather than
	// through a bare conn.WriteJSON.
	//
	// This was the data race `go test -race ./internal/api/` reported as
	// TestADispatchedFrameReachesThePlayer, at external_player_remote.go:319 vs :488.
	// registerRemotePlayer hands the player to the registry BEFORE the ack is written, so a concurrent
	// PlayOnRemotePlayer can already be inside p.send -- holding p.writeMu and calling
	// conn.SetWriteDeadline -- while this goroutine calls conn.SetWriteDeadline on the same socket with
	// no lock at all. gorilla's Conn keeps its write deadline in a plain field, and its documentation
	// permits exactly one concurrent writer per connection.
	//
	// It cannot go through send(): send() marshals a remotePlayerCommand, and the ack is a different
	// wire shape entirely -- a flat {"id","name"} object, not {"type":...}. Changing the player's parser
	// to accept an ack masquerading as a command would be a bigger change than the bug deserves.
	//
	// sendRaw also sets and clears the deadline itself, which is why the explicit
	// SetWriteDeadline(time.Time{}) below disappears. A deadline left in place would kill an
	// idle-but-healthy player on the TV after playerDispatchTimeout; the old code had to remember to
	// clear it by hand, on a path that returns early when the ack fails.
	if err := p.sendRaw(map[string]string{"id": id, "name": name}); err != nil {
		logger.Debugf("#2747: player %s ack failed: %v", id, err)
		unregisterRemotePlayer(id)
		_ = conn.Close()
		return
	}

	// Block until the peer goes away, reading so that close frames and pings are handled.
	// This is the liveness mechanism: no heartbeat, no timeout, nothing to get wrong.
	defer func() {
		unregisterRemotePlayer(id)
		_ = conn.Close()
		logger.Infof("#2747: player %q (%s) disconnected", name, id)
	}()

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// PlayOnRemotePlayer dispatches a play command to a registered player.
//
//	POST /external_player/play    {"playerId":"player-1","sceneIds":["11","12"]}
//
// The scene list is a LIST because issue #2747 asks for playlist support by name ("it's also
// impossible to play an entire playlist at once"), and the natural scope is the scenes the
// operator is looking at, in the order they are shown.
func (rs remotePlayerRoutes) PlayOnRemotePlayer(w http.ResponseWriter, r *http.Request) {
	if ok, reason := authenticateRemotePlayer(r); !ok {
		http.Error(w, reason, http.StatusUnauthorized)
		return
	}

	var body struct {
		PlayerID string   `json:"playerId"`
		SceneIDs []string `json:"sceneIds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "body must be JSON: {\"playerId\":...,\"sceneIds\":[...]}", http.StatusBadRequest)
		return
	}

	if body.PlayerID == "" {
		http.Error(w, "playerId is required", http.StatusBadRequest)
		return
	}
	if len(body.SceneIDs) == 0 {
		http.Error(w, "sceneIds is required and must not be empty", http.StatusBadRequest)
		return
	}
	if len(body.SceneIDs) > maxPlaylistScenes {
		// Refused, not truncated: see maxPlaylistScenes.
		http.Error(w, fmt.Sprintf("a play command carries at most %d scenes, got %d",
			maxPlaylistScenes, len(body.SceneIDs)), http.StatusBadRequest)
		return
	}

	p, found := playerFor(body.PlayerID)
	if !found {
		// 409 rather than 404: the player is not a resource with an address, it is a live
		// connection, and "it is not connected right now" is the actionable message.
		http.Error(w, "no player is registered as "+body.PlayerID+
			" (connected: "+connectedPlayerNames()+")", http.StatusConflict)
		return
	}

	items, err := rs.remotePlayerItems(r.Context(), manager.GetInstance().Repository.Scene, body.SceneIDs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cmd := remotePlayerCommand{Type: playerCmdPlay, Items: items}
	if err := p.send(cmd); err != nil {
		// The socket is gone. Report it rather than pretending: the operator clicked play and
		// nothing is playing, and the reason belongs in the response and the log.
		logger.Warnf("#2747: dispatch to player %s failed: %v", p.ID, err)
		http.Error(w, "player "+p.ID+" is not accepting commands: "+err.Error(), http.StatusConflict)
		return
	}

	// "sent", not "playing". A player that dies mid-playback cannot tell Stash, so a stronger
	// claim would be a lie the operator eventually catches.
	writeRemotePlayerJSON(w, http.StatusOK, map[string]interface{}{
		"sent":     true,
		"playerId": p.ID,
		"scenes":   len(items),
	})

	logger.Infof("#2747: dispatched %d scene(s) to player %s (%s)", len(items), p.ID, p.Name)
}

// CommandRemotePlayer sends a transport command (pause/resume/stop/seek) to a player.
//
//	POST /external_player/command   {"playerId":"player-1","command":"pause"}
//
// A protocol that cannot pause is not a media remote, and those five verbs are what a
// jellyfin-mpv-shim client actually implements.
func (rs remotePlayerRoutes) CommandRemotePlayer(w http.ResponseWriter, r *http.Request) {
	if ok, reason := authenticateRemotePlayer(r); !ok {
		http.Error(w, reason, http.StatusUnauthorized)
		return
	}

	var body struct {
		PlayerID string  `json:"playerId"`
		Command  string  `json:"command"`
		Start    float64 `json:"start"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "body must be JSON: {\"playerId\":...,\"command\":\"pause\"}", http.StatusBadRequest)
		return
	}

	if body.PlayerID == "" {
		http.Error(w, "playerId is required", http.StatusBadRequest)
		return
	}

	cmd, err := playerTransportCommand(body.Command, body.Start)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	p, found := playerFor(body.PlayerID)
	if !found {
		http.Error(w, "no player is registered as "+body.PlayerID+
			" (connected: "+connectedPlayerNames()+")", http.StatusConflict)
		return
	}

	if err := p.send(cmd); err != nil {
		logger.Warnf("#2747: %s to player %s failed: %v", body.Command, p.ID, err)
		http.Error(w, "player "+p.ID+" is not accepting commands: "+err.Error(), http.StatusConflict)
		return
	}

	writeRemotePlayerJSON(w, http.StatusOK, map[string]interface{}{"sent": true, "playerId": p.ID, "command": cmd.Type})
}

// playerTransportCommand validates a transport verb.
//
// An unknown verb is an error rather than a passthrough, for the same reason an unknown
// placeholder is: a player that silently ignores a misspelled command leaves the operator
// pressing pause on a player that is not pausing.
func playerTransportCommand(verb string, start float64) (remotePlayerCommand, error) {
	switch playerCommandType(verb) {
	case playerCmdPause:
		return remotePlayerCommand{Type: playerCmdPause}, nil
	case playerCmdResume:
		return remotePlayerCommand{Type: playerCmdResume}, nil
	case playerCmdStop:
		return remotePlayerCommand{Type: playerCmdStop}, nil
	case playerCmdSeek:
		if start < 0 {
			return remotePlayerCommand{}, fmt.Errorf("seek start must not be negative, got %v", start)
		}
		return remotePlayerCommand{Type: playerCmdSeek, Start: start}, nil
	default:
		return remotePlayerCommand{}, fmt.Errorf("unknown command %q (known: pause, resume, stop, seek)", verb)
	}
}

// send writes one frame, serialised, with a deadline.
//
// The deadline is why this is a method with a mutex rather than a bare conn.WriteJSON: a blocking
// write on a half-open socket is the failure this whole design is arranged to survive.
func (p *remotePlayer) send(cmd remotePlayerCommand) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()

	if err := p.conn.SetWriteDeadline(time.Now().Add(playerDispatchTimeout)); err != nil {
		return err
	}
	defer func() { _ = p.conn.SetWriteDeadline(time.Time{}) }()

	return p.conn.WriteJSON(cmd)
}

// sendRaw writes one arbitrary JSON value, serialised against dispatch by the same mutex, with a
// deadline.
//
// It exists for the registration ack, whose wire shape is {"id","name"} rather than a
// remotePlayerCommand. Before this, that ack was written with a bare conn.WriteJSON on a path that had
// already published the player to the registry, which is the data race described at the call site.
//
// Sharing send()'s body rather than duplicating it keeps the deadline discipline in one place: set
// before the write, cleared after, on both the success and failure paths.
func (p *remotePlayer) sendRaw(v interface{}) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()

	if err := p.conn.SetWriteDeadline(time.Now().Add(playerDispatchTimeout)); err != nil {
		return err
	}
	defer func() { _ = p.conn.SetWriteDeadline(time.Time{}) }()

	return remotePlayerWriteJSON(p.conn, v)
}

// remotePlayerSceneFinder is the slice of the scene store the play list needs.
//
// Declared as its own one-method interface rather than reusing `SceneFinder`: that type carries
// nine methods, and a test fake for a one-method dependency should be one method. The production
// value is `repository.Scene`, which satisfies it as-is.
type remotePlayerSceneFinder interface {
	Find(ctx context.Context, id int) (*models.Scene, error)
}

// remotePlayerItems builds the play list for a set of scene ids.
//
// ## WHY THERE IS A FINDER PARAMETER RATHER THAN manager.GetInstance() INSIDE
//
// This is the rule that decides whether the URL is actually correct, and it is the one that R6 and
// R12 in the mutation sweep survived against: the signing prefix and the window query were both
// only ever tested through `mergeSignedParams` and `remotePlayerFormatSeconds` in isolation, so
// removing either from THIS function changed no test result. Sweeping a helper proves the helper;
// only calling it proves the wiring. Same finding as #3530's §8b, where four of five aggregate call
// sites were untested while the constant's own sweep read 5/5.
//
// So the scene lookup is a PARAMETER. The production path passes the repository; the tests pass a
// two-line fake and then assert on the URL this function actually built.
func (rs remotePlayerRoutes) remotePlayerItems(ctx context.Context, finder remotePlayerSceneFinder, sceneIDs []string) ([]remotePlayerItem, error) {
	sc := finder

	base := rs.remotePlayerBaseURL()
	var items []remotePlayerItem

	for _, raw := range sceneIDs {
		id, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("scene id %q is not a number", raw)
		}

		scene, err := sc.Find(ctx, id)
		if err != nil || scene == nil {
			return nil, fmt.Errorf("scene %d not found", id)
		}

		item := remotePlayerItem{SceneID: scene.ID, Title: scene.Title, Details: scene.Details}

		pf := scenePrimaryFile(scene)
		if pf == nil {
			// A scene with no file cannot be played, and silently dropping it would hand the
			// player a playlist shorter than the operator asked for with no error.
			return nil, fmt.Errorf("scene %d has no primary file to play", id)
		}

		start, end, ranged := sceneWindowForPlayer(scene, nil)
		item.Start, item.End, item.Ranged, item.Duration = start, end, ranged, pf.Duration

		// ## WHY /stream AND NOT /stream.mp4
		//
		// Issue #2747's opening complaint is the reason this whole feature exists: "Playing video
		// in a browser is very inconvenient and often requires server-side transcoding, which is
		// extremely inefficient. So native player is preferable for video playback in any case."
		//
		// `/stream.mp4` is the TRANSCODING route (`streamTranscode` -> ffmpeg). Dispatching that to
		// a native player would re-introduce the exact inefficiency the reporter is asking to
		// avoid, on a machine that can play the file natively. `/stream` is
		// `StreamSceneDirect` -- `http.ServeFile`, no ffmpeg at all.
		//
		// The cost, stated rather than hidden: for a WINDOWED scene (/3530) `/stream` cannot serve
		// a window directly, because an HTTP Range addresses BYTES and an MP4's byte offset for
		// time T is not proportional to T. In that case `/stream` 307s to `/stream.mp4`, which
		// transcodes the window -- the one case where the inefficiency is unavoidable, and the
		// case where a local player cannot help either. The item still reports `ranged: true`, so
		// an operator can see which scenes in a playlist will cost a transcode.
		u := fmt.Sprintf("%s/scene/%d/stream", base, scene.ID)
		q := url.Values{}
		if ranged {
			q.Set("start", remotePlayerFormatSeconds(start))
			q.Set("end", remotePlayerFormatSeconds(end))
		}

		// ## WHY THE SIGNING PREFIX IS THE PATH WE DISPATCH
		//
		// `signedurl.DerivePrefix` takes the first three path segments and strips the extension
		// from the third, so a request for `/scene/11/stream` verifies against the prefix
		// `/scene/11/stream`. Signing anything else produces a URL that 401s on the player's
		// machine, and the player's only visible symptom is that it will not play -- with the
		// auth failure happening on a machine the operator is not looking at.
		//
		// Signed only when the instance has credentials -- the same condition
		// resolver_model_scene.go applies, because with no credentials there is nothing to sign
		// against and the URL is already reachable on the LAN.
		if cfg := config.GetInstance(); cfg.HasCredentials() {
			q = mergeSignedParams(q, rs.remotePlayerSigningSecret(cfg), "/scene/"+strconv.Itoa(scene.ID)+"/stream")
		}

		if len(q) > 0 {
			u += "?" + q.Encode()
		}
		item.URL = u

		items = append(items, item)
	}

	return items, nil
}

// remotePlayerSigningSecret is the HMAC key dispatched media URLs are signed with.
//
// `GetJWTSignKey`, because that is the key the STREAMING routes verify against
// (`userSigningKey` -> `GetJWTSignKey`), and a second key would mean a second verification path
// in authenticateSignedRequest.
func (rs remotePlayerRoutes) remotePlayerSigningSecret(cfg *config.Config) []byte {
	return cfg.GetJWTSignKey()
}

// mergeSignedParams adds the signed-URL parameters to a query.
//
// Takes a possibly-NIL `url.Values` and returns a usable one. The nil case is not hypothetical:
// `url.Values` is a map, and `q.Set(...)` on a nil map panics -- so the obvious call
// `mergeSignedParams(nil, ...)` from a caller with no other query parameters to add takes the
// process down. Found by the test that passes nil; the alternative was to make every caller
// remember to allocate, which is one forgotten line away from the same panic.
//
// The cid is overwritten by SignPrefix's own value rather than set first. They are computed from
// the same inputs today, so the two agree -- but SignPrefix is the authority, and having its
// output silently depend on a parameter that happens to be set beforehand is a trap for whoever
// changes the cid computation next.
func mergeSignedParams(q url.Values, secret []byte, prefix string) url.Values {
	if q == nil {
		q = url.Values{}
	}

	username := config.GetInstance().GetUsername()
	signed := signedurl.SignPrefix(prefix, secret,
		signedurl.GenerateCredentialID(secret, username),
		time.Now().Add(config.GetInstance().GetSignedURLExpiry()))
	for k, vs := range signed {
		q.Del(k)
		for _, v := range vs {
			q.Set(k, v)
		}
	}
	return q
}

// remotePlayerBaseURL is the externally reachable root.
//
// Prefers `external_player.base_url`, then upstream's own `external_host`, and only then the
// server's bind address. The fallback warns, because a dispatched URL of
// `http://127.0.0.1:9999/...` handed to a player on the TV resolves on the PLAYER's machine to
// the player's own loopback: a player that fetches nothing, with no error anywhere. That is worth
// one warning line rather than a silent failure.
func (rs remotePlayerRoutes) remotePlayerBaseURL() string {
	if u := config.GetInstance().GetExternalPlayerBaseURL(); u != "" {
		return strings.TrimRight(u, "/")
	}

	cfg := config.GetInstance()
	if u := cfg.GetExternalHost(); u != "" {
		return strings.TrimRight(u, "/")
	}

	host, port := cfg.GetHost(), cfg.GetPort()
	logger.Warnf("#2747: neither external_player.base_url nor external_host is set; dispatching "+
		"http://%s:%d URLs, which a remote player on another machine cannot reach", host, port)
	return fmt.Sprintf("http://%s:%d", host, port)
}

// remotePlayerFormatSeconds renders a window bound as a plain decimal number for a URL query.
//
// strconv 'f' with -1 precision, never %g: %g renders 60 as "6e+01", and a player or an ffmpeg
// argument that parses that as 6 seeks to the wrong place. This is the same rule the command
// template follows (config.formatSeconds); it is duplicated rather than exported because the two
// render into different surfaces -- one into argv, one into a query -- and a shared exported
// helper for two call sites in two packages is not worth an API.
func remotePlayerFormatSeconds(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// hmacEqualString compares two secrets without leaking their common prefix by timing.
func hmacEqualString(got, want string) bool {
	return hmac.Equal([]byte(got), []byte(want))
}
