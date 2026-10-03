package api

// stash#2747 -- the remote player's HTTP surface.
//
//	GET  /external_player/register?token=...&name=living-room   the handshake
//	GET  /external_player/players                                who is connected
//	POST /external_player/play      {"playerId":"...","sceneIds":["11","12"]}
//	POST /external_player/command   {"playerId":"...","command":"pause"}
//
// ## WHY A SEPARATE MOUNT AND NOT A SUBDIRECTORY OF /scene
//
// The register endpoint authenticates with a TOKEN, not with a session, and it does not have a
// scene. Putting it under `/scene/{id}/...` would put a scene id in a URL that has no scene and
// inherit `SceneCtx`'s requirement that one exists.
//
// ## WHY `players` IS UNAUTHENTICATED-ISH BUT STILL TOKEN-GATED
//
// It is behind the same token as everything else, deliberately: the list of connected players is
// the set of things an operator can start playback on, which on a shared instance is exactly the
// kind of list that should not be public. It is also the endpoint a UI polls to populate the
// player picker, so it has to be cheap and stable-shaped.
//
// ## WHY THESE MOUNT BEFORE THE STATIC CATCH-ALL
//
// chi matches in registration order. A route registered after `r.HandleFunc("/*", ...)` is
// unreachable, and an unreachable API is indistinguishable from a frontend bug. Same reasoning
// as the #837 issues mount.

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/stashapp/stash/pkg/logger"
)

// writeRemotePlayerJSON is a LOCAL json writer.
//
// Not shared with routes_issue.go's writeIssueJSON: that one logs under its own issue number, and
// a helper that logs "#837" for a #2747 response is worse than a duplicated four lines.
func writeRemotePlayerJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Errorf("stash#2747: encoding player response failed: %v", err)
	}
}

// remotePlayerRoutes serves the remote-player protocol.
type remotePlayerRoutes struct {
	routes
}

func (s *Server) getRemotePlayerRoutes() chi.Router {
	return remotePlayerRoutes{
		routes: routes{txnManager: s.manager.Repository.TxnManager},
	}.Routes()
}

func (rs remotePlayerRoutes) Routes() chi.Router {
	r := chi.NewRouter()

	// The handshake. A GET that upgrades to a WebSocket is still a GET, because the upgrade
	// happens in the handshake and everything after it is frames rather than HTTP.
	r.Get("/register", rs.RegisterRemotePlayer)
	r.Get("/players", rs.Players)

	r.Post("/play", rs.PlayOnRemotePlayer)
	r.Post("/command", rs.CommandRemotePlayer)

	return r
}

// Players lists the connected players.
//
// Returns ids and names and nothing else. The set of connected players is not sensitive on its
// own, but there is no reason to expose a connection's age or socket state to a caller, and every
// field added here is a field a future caller will come to depend on.
func (rs remotePlayerRoutes) Players(w http.ResponseWriter, r *http.Request) {
	if ok, reason := authenticateRemotePlayer(r); !ok {
		http.Error(w, reason, http.StatusUnauthorized)
		return
	}

	ids := registeredPlayerIDs()
	type entry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	out := make([]entry, 0, len(ids))
	playerRegistry.RLock()
	for _, id := range ids {
		out = append(out, entry{ID: id, Name: playerRegistry.byID[id].Name})
	}
	playerRegistry.RUnlock()

	writeRemotePlayerJSON(w, http.StatusOK, map[string]interface{}{"players": out})
}
