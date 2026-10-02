# stash#3530 — play URL honours the scene's window

## The problem, stated from the code

A ranged scene's data is correct: `GetFiles` returns the window's duration, aggregates use it,
duplicate detection respects it. But the PLAYER is not:

| endpoint | range-aware? | measured |
|---|---|---|
| `GET /scene/{id}/stream` | **NO** | `running_streams.go:65` is `http.ServeFile(w, r, fp)` — the whole file, no seek |
| `GET /scene/{id}/stream.mp4\|webm\|mkv` | start only | `StartTime` exists; **no end** |
| `GET /scene/{id}/stream.m3u8\|mpd` | **NO** | `streamManifest` never reads `start` |

So a scene that is 60s of a 2-hour file plays 2 hours. This is the last piece that makes the
feature usable rather than merely correct in the database.

## What already exists (do not rebuild it)

- `ffmpeg.Args.Seek(seconds)` → `-ss`, and `Args.Duration(seconds)` → `-t`. `Duration` is used by
  `codec_hardware.go` (0.1s and 1s probe timeouts) but **never for a scene window**, so the
  plumbing shape is proven and the window is genuinely new.
- `TranscodeOptions.StartTime float64` and `streamTranscode` already reads `r.Form.Get("start")`.
- `Args.NoAccurateSeek()` exists for the copy-codec case (#7103).

## Design

### 1. There is NO per-pair model type — measured, and it changes the design

My first plan was "add `StartTime`/`EndTime` to a `SceneFile` type, because the window belongs to
the `(scene_id, file_id)` pair, not to the file." **That type does not exist in the runtime model.**
Measured:

    Scene.Files  is  RelatedVideoFiles   ([]*VideoFile)      model_scene.go:26
    VideoFile    is  the FILE: Format/Width/Height/Duration  model_file.go:279
    SceneFile    exists ONLY in pkg/models/jsonschema — the JSON EXPORT shape

So the window currently has no carrier between `pkg/sqlite` (which reads the columns) and the
handlers (which need them). Adding the fields to `models.VideoFile` would be **wrong in principle
and impossible in practice**: the same file backs several scenes with different windows, so one
value per file cannot express it, and `GetFiles` already returns a per-scene copy.

`RelatedVideoFiles` is built in `LoadFiles` from `GetFiles`, and `GetFiles` is where the columns
are read. **So the window must be carried on the per-scene copy of the file**, and the type that
models "a file as this scene uses it" has to exist. Adding `StartTime`/`EndTime` to
`models.VideoFile` is acceptable *only because GetFiles returns a fresh copy per call* — which is
precisely the invariant already relied on for the derived duration. It is noted here because that
is a subtle load-bearing coupling, not an obvious one.

### 2. `streamTranscode` — add `end`, and make the scene's window the DEFAULT

The client may still pass `start`/`end` explicitly (that is how seeking inside a scene works
today), so the query params win. When a param is ABSENT, fall back to the scene's window:

```go
startTime := sceneWindowStart(scene)   // 0 when unranged
if v := r.Form.Get("start"); v != "" {
    if ss, err := strconv.ParseFloat(v, 64); err == nil {
        startTime = ss
    }
}
```

**Why fall back rather than always override:** the frontend's scrubber appends `?start=` as the
user seeks *within* the scene. If the backend ignored query params and always used the stored
window, seeking would break for every ranged scene — a worse bug than the one being fixed.

`end` is new. Same precedence, and `end <= start` is clamped to "no end" rather than rejected,
because a client that races a seek can legitimately send one and an error would be worse than
ignoring it.

### 3. `TranscodeOptions` gains `EndTime`, and the args gain `-t`

```go
type TranscodeOptions struct {
	StreamType StreamFormat
	VideoFile  *models.VideoFile
	Resolution string
	StartTime  float64
	EndTime    float64   // 0 = to the end of the file
}
```

```go
if o.StartTime != 0 {
	if codec == VideoCodecCopy {
		args = args.NoAccurateSeek()
	}
	args = args.Seek(o.StartTime)
}
if o.EndTime != 0 {
	args = args.Duration(o.EndTime - o.StartTime)
}
```

**`-t` is a DURATION, not an end point.** Passing the end as `-t` would seek past the file. The
subtraction is the whole reason this is not a one-line change, and it is why the test asserts on
the produced arg list.

### 4. `streamManifest` — HLS and DASH get the window too

Same parse, and `ServeManifest` gains the two offsets. Without this the player, which prefers
HLS/DASH, would still play the whole file while `/stream.mp4` was correct — a fix that is right
in the URL nobody uses.

### 5. `/stream` (direct) — 409, not a silent whole file

`http.ServeFile` cannot seek by TIME; MP4 byte-range seeks land mid-GOP and produce a video that
starts at the wrong frame. So for a ranged scene the direct endpoint **must not** pretend:

- If transcoding is enabled → `307` to the equivalent `/stream.mp4`, which honours the window.
- If transcoding is disabled → `409 Conflict` with a plain-text body naming the scene and its
  window. NOT a silent 200 with the whole file, which is the bug being fixed.

This is the one place I am deliberately returning an error where something used to "work".

## Verification, per step

| step | command | expected |
|---|---|---|
| model + sqlite populate | `go test -tags integration ./pkg/sqlite/ -run TestARangedSceneReportsItsRange -v` | PASS |
| args builder | `go test ./pkg/ffmpeg/ -run TestTranscodeArgsCarryTheWindow -v` | PASS; asserts `-ss` present AND `-t <end-start>` |
| `-t` is a duration | same | fails if `-t` receives the END (e.g. `-t 300` for 60..300) |
| precedence | `go test ./pkg/api/ -run TestWindowIsTheDefaultButAQueryParamWins -v` | PASS both ways |
| manifest | `go test ./pkg/ffmpeg/ -run TestManifestCarriesTheWindow -v` | PASS |
| mutation: drop `-t` | delete `args.Duration(...)` | KILLED |
| mutation: `-t` = end not duration | `o.EndTime - o.StartTime` → `o.EndTime` | KILLED |
| mutation: always override | drop the `if v := r.Form.Get` branch | KILLED |
| mutation: 409 → silent 200 | remove the ranged-scene branch | KILLED |
| full | `go build ./... && go test -tags integration ./pkg/sqlite/ -count=1` | ok |

**Each mutant's test must be made to fail for the right reason.** A mutant that fails to BUILD is
not a survivor, it is an invalid experiment — that happened twice this session and I assert
`builds: True` alongside every kill now.

## Out of scope

- Screenshots/previews/sprite/VTT: all generated from the file at t=0 and would need their own
  range-aware ffmpeg calls. Not doing them here.
- Detection and the range-editing UI remain open either way.
## IMPLEMENTATION RECORD — 4 commits (the 4th in ISSUE-3530-hls-spec.md)

    e1e0bb65a  (1) ffmpeg:  TranscodeOptions.EndTime, -ss + -t <end-start>   sweep 4/4
    5ea9bf3ad  (2) handler: window reaches the play URL, param precedence       sweep 8/8
    eab18956f  (3) /stream: refuses a window rather than serving the whole file sweep 3/3

Three findings the spec did not predict:

1. **`?start=0` was being ignored**, found by mutation and not by reading. The scrubber sends 0
   when the playhead is dragged home, so a ranged scene could not be rewound to its beginning.
   `ParseFloat`'s `err == nil` is the only thing separating "explicit 0" from "no param", which
   is exactly the branch a tidy-up edit deletes.

2. **`RelatedVideoFiles.Primary()` panics by contract** when the relationship is unloaded. Every
   route gets that for free via `SceneCtx`, but a helper called with a hand-built scene took the
   process down — and a scene whose file is missing is #3526's normal 404 path, so it is a real
   state. The guard lives in the helper, not the caller.

3. **The scene-file window has no runtime model type.** `SceneFile` exists only in
   pkg/models/jsonschema (the JSON export shape); `Scene.Files` is `[]*VideoFile`. So the window
   rides on `models.VideoFile`, sound only because `GetFiles` returns a fresh copy per call — the
   same invariant the derived duration relies on.

### DONE, LATER: HLS and DASH manifests (ecd659eb3, docs/ISSUE-3530-hls-spec.md)

The gap described below was closed in a fourth commit. `lastSegment` turned out to be ALREADY
correct for the wrong reason — it reads `vf.Duration`, which the derived-duration change made mean
the WINDOW's length. The real work was the seek base, `-t`, the manifests' declared duration, and
the segment cache key (a genuine bug: the key derives from the FILE, so two scenes of one file
shared a cache directory).

### (historical) NOT DONE at the time of the third commit: HLS and DASH manifests

Measured, not assumed:

    serveHLSManifest   ffprobes the whole file, so the manifest declares the FILE's duration
    streamSegment      caches segments on the scene hash ALONE

Both need the window: the manifest must declare the window's length, and the segment cache key
must include it or two scenes of one file share segments. A ranged scene therefore still plays
whole over `.m3u8` and `.mpd` — which the player prefers, so this is the remaining gap in "the
play URL honours the window".

A second-order consequence worth writing down: the scene hash is derived from the file, so
scenes sharing a file share a cache key. That was harmless when a scene WAS a file; #3530 makes it
wrong. Fixing it is a cache-invalidation change, not a playback change, and is the reason this
was left rather than squeezed in.
