# #2747 — remote external player: a player that connects and WAITS

Spec written 2026-10-03, against `aafa862a6` (the tip this session started from).
Read with `docs/plan/BACKLOG-17.md` §I, which is where this programme was scoped.

## The finding that shapes the whole spec

**Three different things have been called #2747.** All three are real, and none of them is
what issue #2747 asks for:

| what | who built it | shape |
|---|---|---|
| `ExternalPlayerButton.tsx` | UPSTREAM `3d1b949f4` (PR #679, author InfiniteTF) | CLIENT-side url-scheme handoff (`vlc-x-callback:`, Android `intent:`) — spawns nothing |
| `e881e7edd` (WIP) | this fork, uncommitted-as-done | SERVER-side spawn of a local player from a command template |
| what the ISSUE asks | — | **an API a REMOTE player connects to and waits on, for a play command** — jellyfin-mpv-shim |

Verified against the live issue (`gh issue view 2747`, 2026-10-03, still OPEN, single label
`feature request`): *"API that would allow external players connect to Stash and wait for play
command. See Jellyfin and it's remote client jellyfin-mpv-shim for reference."*

The issue's own *alternatives* section names the two existing things as things the reporter
found insufficient, and states what is actually missing:

- *"It works, but not so convenient! **Scene metadata is also not sent to external players.**"*
- *"**It's also impossible to play an entire playlist at once.**"*

So the deliverable is not "a button". It is: **a player announces itself, Stash decides what to
send it, and a play command carries metadata and a playlist.** Neither existing implementation
does any of the three.

## What the WIP does NOT do, measured

`e881e7edd` is honest code with a real 7/7 mutation gate, and it stays. It is the **local** half:
Stash spawns a player on its own host. The issue wants the **remote** half, which is a different
program with a different security surface:

1. **Nothing waits.** `POST /scene/{id}/external_player` returns as soon as `cmd.Start()`
   succeeds. A remote player has no launch to receive — it is already running, elsewhere, and
   needs to be TOLD what to play.
2. **The launch is one-way.** No registration, no heartbeat, no liveness, no "this player is
   gone". The registry (`launchSync.byPID`) records pids Stash spawned *itself*; a remote
   player's pid means nothing to Stash.
3. **No command path exists at all.** Nothing can tell a registered player to play, to stop, to
   pause, to skip. The API surface is exactly one unidirectional verb.
4. **Metadata is not sent, even to the local player.** `{title}` is the only scene field in the
   template, and nothing else is available to substitute.
5. **No playlist.** The template takes one file.

## Design

### 1. A player registers, and holds a long-lived connection

jellyfin-mpv-shim is a long-lived WebSocket client. That is the right shape here, for a reason
that is not fashion: the alternative — a player polling `GET /pending` every 2s — turns a LAN
of 10 players into 5 requests/second forever, and makes "the user clicked play" take up to 2s
to arrive. Stash already depends on `gorilla/websocket` (`internal/api/server.go:187`) so this
adds no dependency.

    WS  /external_player/register?token=<token>        (player -> Stash)
        -> {"id":"player-3","name":"living-room","capabilities":{...}}
        <- the socket stays open, and carries the play commands

**A token, not a session cookie.** The player is a program on another machine; it has no
browser and no cookie jar. And a session cookie would be the *wrong* credential even if it had
one: this endpoint starts programs on the Stash host, so it must be revocable without
invalidating the operator's login. So a dedicated, config-held token, compared in constant
time.

**Registration is refused unless `external_player.enabled`.** Same flag as the local half, so
there is exactly one switch: if it is off, no program on the LAN can connect and wait.

### 2. A play command carries metadata and a playlist

    POST /scene/{id}/external_player/play   (browser -> Stash, authenticated, SceneCtx)
      body: {"playerId":"player-3"}
      -> dispatches, to that player's socket:

    {"type":"play","items":[
       {"sceneId":11,"title":"A","details":"2024 · Studio · 20:04",
        "url":"http://host:9999/scene/11/stream.mp4?apikey=...",
        "start":30,"end":45,"duration":15}]
     }

**Items, not a single item.** The issue asks for playlist support by name, and the natural
scope is the current *view*: the scenes the user is looking at, in the order they are shown.
`sceneIds []string` in the body, dispatched in order, capped (see §4).

**The URL is signed, not raw.** A remote player cannot pass a cookie, which is exactly the
case `pkg/signedurl` already exists for (its doc comment says so: *"media requests from devices
that cannot pass cookies (AirPlay, Chromecast)"*). So the dispatched URL carries the signed
params for `/scene/{id}/stream` — **not** `?apikey=`, which would put a database-rewriting
credential in a URL that ends up in a player's history file and in any log between the two
machines. This is the single most important security decision in the spec and §5 pins it.

**The window goes with it.** #3530 made a scene able to be a *window* of its file; `start`/`end`
travel with the item, and the URL carries them as `?start=&end=`, which `resolveSceneWindow`
already honours. A player that ignores them plays the whole file and the scene appears to be the
wrong length.

### 3. Commands a player can receive

`play`, `pause`, `resume`, `stop`, `seek`. Five verbs, because those are the five things
jellyfin-mpv-shim's client actually implements and a protocol that cannot pause is not a media
remote. Each is a JSON frame; `stop` is sent on unregister so a player that dies does not leave
its last scene playing forever.

### 4. Bounds, chosen so a bug cannot become an incident

- **one config-held token, and `enabled` gates it.** No per-user tokens, no token issuance
  endpoint: the token is written in the config file by the operator. The threat is a program on
  the LAN, and the mitigation is "the operator decides whether there is a token at all".
- **no more than one pending command per player.** A queue would need a delivery guarantee
  across a socket that may be half-open; one in-flight command, dropped with an error if the
  socket is gone, is honest about what this is.
- **the scene list is capped** (64) and refused above it, not truncated. Truncating a playlist
  silently plays 63 of 200 scenes.
- **every dispatch is logged** with player id, scene ids and the count, so "it did not play on
  the TV" is answerable from the log rather than by guessing.

### 5. What could go wrong, and what each mitigation is

| risk | mitigation | why not the alternative |
|---|---|---|
| anyone on the LAN can start programs | token + `enabled`, constant-time compare | per-user tokens need an issuance endpoint and a revocation list; nothing here needs one |
| credential leaks into a URL | signed URL, never `apikey` | `apikey` is what the *local* player path uses today and it is already the weakest thing in this feature |
| command replay after a reconnect | the socket carries the command; a reconnect carries nothing | a queued command replayed to a player that has since changed rooms is worse than a dropped one |
| stale registration forever | liveness by socket close, plus a bounded registry | polling-based liveness needs the polling we just removed |
| a runaway playlist | cap + refusal | truncation hides the bug |

## NOTIFY — decisions I took, and why

The prompt delegates design judgement, so these are stated rather than asked.

1. **Both halves ship; the local one is not replaced.** `e881e7edd` stays as the *local* launch
   path under its own route, and the remote protocol is additive. They answer different
   questions (a program on this host vs a program on the TV) and one does not subsume the other.
2. **A WebSocket register endpoint, not a REST poll loop.** Stash already vendors
   gorilla/websocket; polling costs latency and constant LAN traffic for a feature whose whole
   point is instant response to "play".
3. **The window travels in the frame, not only in the URL.** A player that honours the URL
   query gets it right with no protocol support; one that ignores the query still has the
   numbers. Belt and braces, and the frame is the contract.
4. **`playerId` is server-assigned and opaque**, never a name the caller supplies as an
   identifier. A display name is data; an id is a lookup key, and letting a caller pick the key
   invites one player's commands landing on another's socket.
5. **The command is not acknowledged by the player's playback state.** Stash reports "sent", not
   "playing". A player that dies mid-playback cannot tell Stash, and inventing a liveness
   protocol for it is a larger design than this issue asks for.
6. **Dispatch is a non-blocking send with a short deadline.** A half-open socket must not hold
   an HTTP request open while a player on a sleeping TV negotiates TCP.

## Verification

    go generate ./cmd/stash                      # REQUIRED (gqlgen; 41s here)
    go build ./...                               # clean
    go vet ./...                                 # clean
    gofmt -l pkg/ internal/                      # empty
    go test ./... -count=1                       # 61 packages green
    go test -tags integration ./... -count=1     # 61 packages green
    python3 docs/mutate_2747_external_player.py   # 7/7 killed, exit 0 (the LOCAL half, untouched)
    python3 docs/mutate_2747_remote_player.py    # 11/12 killed, exit 0 (this work)

Baseline recorded on `aafa862a6`, measured this session, not copied from a note: 61 ok packages
unit, 61 ok packages integration, vet clean, gofmt empty.

## What the mutation sweep actually found, and the two defects behind it

Three survivors in the first run, and **two of them were one real defect**: `remotePlayerItems` --
the function that builds the URL a player fetches -- had no caller in any test, because it reached
for `manager.GetInstance()` internally and so seemed to need a whole database. Every test that
touched the signing or the window therefore tested a HELPER, not the builder.

That is the same shape as #3530's §8b finding, where four of five aggregate call sites were
untested while the constant's own sweep read 5/5: *sweeping the constant proves the rule; only
calling it proves the wiring.* The fix was a one-method finder interface parameter, so a two-line
fake drives the real builder. Two independent rules (R6 signing prefix, R12 window) were hiding in
that single gap.

The third survivor is R8 (`%g` instead of plain decimal), and it is **documented, not fixed**:
measured on this host, `%g` renders 60 as `60`, 1800 as `1800`, 3600 as `3600` and 86400 as
`86400`. The first exponent is 1000000 seconds — 11.6 days. There is no scene whose window contains
one, so no input this feature accepts can break the rule. The plain-decimal formatting is kept and
tested anyway; the sweep entry is marked EXPECTED SURVIVOR rather than deleted, so the next reader
knows it was examined.

**Two harness defects, both fixed rather than accepted:**

1. **A non-compiling mutant was scored as neither cover nor skip-by-panic.** The build-failure
   detector matched `# github.com`, which heads *every* panic stack trace. So R5 — whose mutant
   does compile, and does kill its witness, by panicking inside the handler — was reported as
   "does not compile". Detection is now exactly `[build failed]`, and a panic that names the
   witness counts as a KILL: Go aborts the binary before printing the witness's own `--- FAIL:`
   line, and calling that a skip understates a real kill.
2. **One mutation did not compile and the sweep could not tell why.** R5's replacement was
   `if false {` against a four-line anchor, leaving an unbalanced brace — a broken *mutation*, not
   a broken detector. Anchors are now whole blocks.

**The filter audit earned its place on first run, again.** Six witnesses were named
`TestPlayRefuses...`, which the sweep's `-run` filter does not match, so four of them never
executed and would have been reported as survivors on faith. The tests were renamed rather than the
filter widened — widening it would have hidden the fact that they had been dead.

## One thing a reader may reasonably want that is deliberately absent

**No UI.** The protocol is complete and reachable by hand (`curl`/`websocat`), and the operator
cannot yet click "play on living-room TV". That is a real gap and it is recorded rather than
implied: a UI is a separate piece of work with its own test story, and this repo's ui/v2.5 has no
JS test runner at all (verified while doing #3530's form), so shipping untested React would be
worse than shipping the protocol. The `GET /external_player/players` endpoint exists precisely so a
picker has something to populate.

