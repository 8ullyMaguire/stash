# stash#3530 — the cover and previews come from inside the scene's window

Predecessors: `docs/ISSUE-3530-playurl-spec.md`, `docs/ISSUE-3530-hls-spec.md`.

## The defect, measured

`task_generate_screenshot.go:48`:

    at = float64(videoFile.Duration) * 0.2

`videoFile.Duration` is now **the window's length** (the derived-duration change). `at` is then
passed to `ScreenshotTime(input, at, …)`, which does `args.Seek(t)` — an **absolute offset into the
file**.

So for a scene that is 300s..600s of a 7200s file:

    Duration (window)      300
    at = 300 * 0.2         = 60
    the scene actually starts at 300

**The cover is grabbed 240 seconds before the scene begins.** For a scene at 3600s..3900s of a
2-hour file the cover comes from 60s — the first minute of the file.

This is the same class of bug as `lastSegment` reading `vf.Duration`, but the opposite: there the
derived duration was accidentally *correct*, here it is actively *wrong*, because the value is used
as a position rather than a length.

Three more places carry the same assumption. All measured:

| site | expression | with a 300..600 window |
|---|---|---|
| `task_generate_screenshot.go:48` | `Duration * 0.2` | cover at 60s — **240s early** |
| `screenshot.go:30` | `screenshotDurationProportion * videoDuration` | same, via `Generator.Screenshot` |
| `preview.go:179` (`previewVideoSingle`) | `StartTime: 0, Duration: videoDuration` | preview of the file's **first 300s** |
| `preview.go:109` (`getStepSizeAndOffset`) | step/offset from `videoDuration` | tile grid sized for the window, sampled from the file's head |

## Design

### 1. One place decides where "inside the scene" means

A window has a start; every timestamp the generator picks must be **start + fraction × length**.
Four call sites each doing that arithmetic is four places to get it wrong, and the screenshot one
is already wrong.

`pkg/scene/generate` gets:

```go
// SceneWindow is the part of a file a scene plays. A nil Start or End means the scene runs
// to that end of the file.
type SceneWindow struct {
	Start float64
	End   float64 // 0 = to the end of the file
	Set   bool
}

// At returns the absolute file offset for a point INSIDE the window, given as a
// proportion of the window's length.
//
// This exists because a proportion of a length is not a position. For a scene at 300..600
// of a 7200s file, 20% in is 360 -- not 60.
func (w SceneWindow) At(fraction float64, length float64) float64 {
	if !w.Set {
		return fraction * length
	}
	return w.Start + fraction*length
}
```

The unranged case reduces to the old expression exactly, so **every pre-existing scene's cover is
byte-identical** — which is the same "no existing row changes" guarantee migration 122 made.

### 2. The screenshot uses it

```go
at := window.At(screenshotDurationProportion, videoFile.Duration)
```

and `Generator.Screenshot` gains the window. `options.At` (an explicit user-chosen timestamp, from
`sceneGenerateScreenshot(at:)`) **still wins** — a user asking for a frame at 400s means 400s, and
silently rebasing that into the window would be surprising. What changes is only the *default*.

### 3. The preview seeks into the window

`previewVideoSingle` currently sets `StartTime: 0`. For a ranged scene it becomes the window's
start, with `Duration` the window's length — which it already is. So the single-segment path
(`videoDuration < SegmentDuration × Segments`) becomes correct with one field changed.

The multi-segment path (`getStepSizeAndOffset`) computes a step and an offset across
`videoDuration`; with the window's length that grid is the right *shape* but it must be **shifted**
to start at the window's start rather than at 0. `excludeStart` is already a proportion of the
duration, so it becomes `window.At(excludeStart, length)`.

### 4. The cached-artifact problem — and why the cover is a DIFFERENT shape of work

The cover is stored as a **blob on the scene row** (`UpdateCover`), so it is already per-scene and
needs no cache key. Good.

The **preview and webp are files keyed by `scene.GetHash()`** — and the hash derives from the FILE.
So two scenes sharing one file share `generated/screens/<hash>.mp4`, and after this change they
would need *different* previews (different windows) from one cache slot.

That is the same defect already fixed for HLS segments (`stash-3530-hls`), and the same fix: put
the window in the key. **But** this one is not free:

- the preview path is a *file on disk* consumed by the frontend at a URL, so the key changes the
  filename and any bookmarked/external preview URL stops resolving;
- `migrate_screenshots.go` and the legacy-path fallback (`ResolveGeneratedFile(preview, legacy)`)
  both exist to paper over old filenames.

So this is **two separable pieces** and they are worth separating deliberately:

- **(A) do now:** the cover and the preview *content* come from inside the window. Correct for every
  scene, immediately.
- **(B) separate commit:** the preview/webp cache key includes the window, with the legacy fallback
  extended rather than removed.

(B) touches generated-file naming and therefore external URLs; doing it inside the same commit as a
behaviour change would make a revert lose both.

## Verification

| step | command | expected |
|---|---|---|
| cover timestamp | `go test ./pkg/scene/generate/ -run TestTheCoverComesFromInsideTheWindow -v` | PASS; window 300..600 → at 360 |
| unranged unchanged | same | PASS; 7200 → 1440, byte-identical to before |
| explicit `at` wins | same | PASS; `at=400` stays 400 |
| preview seeks | `go test ./pkg/scene/generate/ -run TestThePreviewSeeksIntoTheWindow -v` | PASS; StartTime 300 |
| segment grid shifted | `go test ./pkg/scene/generate/ -run TestTheSegmentGridIsShiftedToTheWindow -v` | PASS |
| mutation: drop the base | `w.At` → `fraction*length` | KILLED |
| mutation: unranged regresses | remove the `!w.Set` branch | KILLED |
| mutation: explicit At ignored | always use the window | KILLED |
| mutation: preview StartTime 0 | revert | KILLED |
| full | `go build ./... && go test ./pkg/scene/... -count=1` | ok |

## Out of scope

- **Sprite / VTT thumbs.** Same window-awareness applies (they are tiled across the duration), but
  they are generated by a different task with its own cache and are a third piece, not a fourth site
  in this one. Recorded in WHATS-LEFT.
- **The preview/webp cache key** — piece (B) above.