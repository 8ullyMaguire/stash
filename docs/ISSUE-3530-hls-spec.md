# stash#3530 — HLS and DASH manifests honour the scene's window

Predecessor: `docs/ISSUE-3530-playurl-spec.md`, commits `e1e0bb65a` / `5ea9bf3ad` / `eab18956f`.

The play URL is fixed for `/stream.mp4|webm|mkv` and `/stream`. **HLS and DASH still play the whole
file, and they are the paths the player prefers.** This is the remaining gap.

## Measured, before designing anything

| site | what it does today | line |
|---|---|---|
| `serveHLSManifest` | `leftover := probeResult.FileDuration` — the FILE's length | 458 |
| `serveDASHManifest` | `mediaDuration := ... probeResult.FileDuration` | 575 |
| `transcodeProcess.makeArgs` | `args.Seek(segment * segmentLength)` — no base offset | 344 |
| `lastSegment` | `math.Ceil(vf.Duration/segmentLength) - 1` | 406 |
| `StreamType.FileDir` | cache dir = `hash_streamType[_size]` — no window | 303 |

Four sites, not one. Two read the *probed* duration (the file's) and two read `vf.Duration`.

## The finding that makes this tractable

`lastSegment` already uses `vf.Duration`, and `GetFiles` returns a **per-scene copy whose Duration is
the WINDOW's length** (that is the derived-duration change from the data-model commit).

**So the segment COUNT is already correct.** A 240s window gives `ceil(240/10)-1 = 23` segments,
not `ceil(7200/10)-1 = 719`. The code was already half-right for the wrong reason — it reads a
field that now means something new.

That leaves three real changes, and one of them is a correctness fix rather than a feature.

## Design

### 1. Segment bounds and the seek base — `makeArgs`

The seek is relative to the file, so a windowed scene needs the window's start ADDED:

```go
// a ranged scene's segments are numbered from the WINDOW's start
base := 0.0
if s.vf.StartTime != nil {
    base = *s.vf.StartTime
}
if segment > 0 || base != 0 {
    args = args.Seek(base + float64(segment*segmentLength))
}
args = args.Duration(<window length>)   // so ffmpeg stops at the window's end
```

**`args.Duration` is essential, not optional.** Without it ffmpeg runs to the end of the file and
happens to write the right segments into the right filenames — which looks correct in the
playlist and then plays on into footage the scene does not contain. The manifest says
`#EXT-X-ENDLIST` after segment 23, so a player stops; a player that fetches segment 24 anyway gets
real video instead of a 404. `-t` closes that.

### 2. The manifest's declared duration — both handlers

`probeResult.FileDuration` is the FILE's length and is what the playlist's `leftover` loop and the
DASH `mediaDuration` are built from. Both must use the WINDOW's length.

**`vf.Duration` is already that value** (the derived duration), so this is a one-token change per
site — but it is load-bearing and needs its own tests, because `probeResult.FileDuration` and
`vf.Duration` being the same number for an unranged scene is exactly why a regression here is
invisible on ordinary content.

### 3. The cache key — `FileDir`, and this is a BUG, not a feature

`FileDir(hash, size)` keys on the scene hash, which `scene.GetHash()` derives **from the file**. So
two scenes sharing one file share a cache directory, and their segments collide.

That was harmless when a scene *was* a file. #3530 makes it wrong: scene A (0–240) and scene B
(240–480) of one file share `dir`, and whichever is transcoded first populates it for both.

Fix: include the window in the directory name.

```go
func (t StreamType) FileDir(hash string, maxTranscodeSize int, window windowKey) string
```

with an empty `windowKey` for an unranged scene, so **every existing cache directory keeps its
current name** and no cache is invalidated by this change. That is the whole reason the parameter is
a struct with an `IsZero()` rather than two floats: `0-0` is a legal window (the head of the file)
and must not collide with "no window".

### 4. The manifest must forward the window to the segment URLs

The playlist URLs are built from `urlQuery`. A ranged scene's segments must carry the window or the
segment requests resolve against a different cache dir than the manifest advertised, and every
segment 404s. `copyAuthParams` already forwards auth; the window goes in the same place.

## Verification

| step | command | expected |
|---|---|---|
| segment count is the WINDOW's | `go test ./pkg/ffmpeg/ -run TestTheWindowDecidesTheSegmentCount -v` | PASS |
| seek includes the base | `go test ./pkg/ffmpeg/ -run TestASeekedSegmentStartsAtTheWindow -v` | PASS; `-ss` = `start + n*10` |
| `-t` bounds the process | same | PASS; `-t` present when ranged |
| HLS playlist length | `go test ./pkg/ffmpeg/ -run TestTheHlsPlaylistIsAsLongAsTheWindow -v` | 24 segments, not 720 |
| DASH mediaDuration | `go test ./pkg/ffmpeg/ -run TestTheDashManifestDeclaresTheWindow -v` | window length |
| cache key separates windows | `go test ./pkg/ffmpeg/ -run TestTheCacheKeyIncludesTheWindow -v` | differs; and unranged keeps its OLD name |
| mutation: drop the seek base | `base + segment*len` → `segment*len` | KILLED |
| mutation: drop `-t` | delete the Duration call | KILLED |
| mutation: manifest uses FileDuration | revert either handler | KILLED |
| mutation: cache key ignores the window | `FileDir(hash, size)` | KILLED |
| mutation: `0-0` == no window | drop `IsZero()` | KILLED |
| full | `go build ./... && go test ./pkg/ffmpeg/ -count=1` | ok |

Every mutant asserts `builds: True` — three times this session a mutant that only failed to
compile was misreported as a survivor.

## Out of scope, deliberately

- **`segmentLength` (10s) vs a window shorter than it.** A 4s window yields
  `ceil(4/10)-1 = 0`, i.e. one segment of 4s. That works, and is tested.
- **A window that starts mid-segment.** Segment boundaries stay on absolute 10s multiples of the
  FILE. Real HLS players tolerate a first segment that starts at an arbitrary offset; making the
  grid relative to the window would change every segment index and is a much larger change.
- **Screenshot / preview / sprite / VTT** — still generated from the file at t=0.