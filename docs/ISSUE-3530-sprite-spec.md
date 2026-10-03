# stash#3530 — sprite + VTT thumbs need window-awareness, and it is NOT the same fix as the preview

Third piece of the thumbnail work. The preview is done (`stash-3530-cover`, `stash-3530-previewkey`).
This spec records why sprite is a different job, not a copy of that one.

## The defect

`GenerateSpriteTask.Start` (`task_generate_sprite.go:22`) is the exact shape the preview task was in
before `stash-3530-cover`:

    ffprobe.NewVideoFile(t.Scene.Path)        // probes the FILE
    sceneHash := t.Scene.GetHash(...)        // keys on the FILE
    NewSpriteGenerator(*videoFile, ...)       // drives everything off VideoStreamDuration

The scene is never consulted. Every derived quantity — `chunkCount`, `SpriteInterval`, `stepSize`, the
VTT cue spacing, `SlowSeek`'s duration test — comes from `VideoStreamDuration`. So a scene at
300..600s of a 7200s file gets a sprite tiled across the whole two hours, with 30x too many tiles, and
a VTT whose cues point into footage that is not the scene.

## What makes this NOT a copy of the preview fix

Three things, each of which the preview did not have.

**1. The VTT cues are a CONTRACT with the player, not just a lookup key.** `SpriteVTT`
(`pkg/scene/generate/sprite.go:103`) writes cue timestamps, and the frontend seeks by them. A cue
must therefore describe a position **inside the window**, and the sprite grid's step must be the
window's step. Getting this wrong does not produce a wrong thumbnail — it produces a scrub bar that
lands on the wrong second of the wrong footage. There is no cache key that can fix this after the
fact.

**2. `SlowSeek` works in FRAMES, not seconds, and cannot be windowed the same way.**

    stepFrame := float64(FrameCount-1) / float64(ChunkCount)     // generator_sprite.go:222
    frame    := math.Round(float64(i) * stepFrame)               // frame index into the FILE

A frame index is absolute — there is no "frame 300 of the window" without converting through
`FrameRate`. And `SlowSeek` is selected by `VideoStreamDuration < 5 || FrameCount <= chunkCount`,
both of which are file-level. For a window shorter than the file, the correct decision is made from
the WINDOW's duration and frame count, which means computing them, not reading them off the probe.

**3. `chunkCount` is snapped to a perfect square.** `GetSpriteGridSize` rounds up so the grid has no
empty cells. That arithmetic is currently done from the file's duration, so a window of 300s inside a
7200s file would get a grid sized for 2 hours — mostly empty space, and the thumbnails that DO exist
are all wrong.

## Design: carry the window in, exactly as the preview does, but derived

Same shape as `pkg/scene/generate`'s `SceneWindow` (which already exists from `stash-3530-cover`),
extended with what sprite needs that preview did not:

```go
// in pkg/scene/generate/sprite.go
type SpriteOptions struct {
    Window        SceneWindow
    SpriteInterval float64   // already in SpriteGeneratorConfig
    MinimumSprites int
    MaximumSprites int
}
```

`NewSpriteGenerator` gains the window and derives:

| quantity | from the FILE (today) | from the WINDOW (required) |
|---|---|---|
| `duration` | `VideoStreamDuration` | window length, else the file's |
| `chunkCount` | `ceil(duration / interval)` | same, from the window's length |
| `stepSize` | `duration / chunkCount` | window start + `i * stepSize` |
| `SlowSeek` | file duration/frames | window duration/frames, and only when the window can support the grid |
| VTT `stepSize` | `NthFrame / FrameRate` | the window's, so cues land inside it |

**The unranged branch must reduce to the current expression exactly**, as in `stash-3530-cover` — every
existing scene's sprite is byte-identical.

## The slow-seek trap, stated precisely

For `SlowSeek` the window must be converted to frames:

    firstFrame = round(window.Start * FrameRate)
    stepFrame  = windowLengthSeconds * FrameRate / chunkCount
    frame      = firstFrame + round(i * stepFrame)

with `frame` clamped to `< FrameCount` and to the window's last frame. My first pass computed only
`stepFrame`, which produces a grid that is correctly SPACED and starts at frame 0 — i.e. still the
wrong footage, just evenly spaced. That is the kind of half-fix that survives a duration-only test,
because the spacing assertions all pass.

**A window shorter than the grid needs frames must not use `SlowSeek` at all** — `GetSpriteGridSize`
would round `chunkCount` up past the window's frame count and produce duplicate frames. Today's
condition (`FrameCount <= chunkCount`) is file-level and happens to be right only for unranged
scenes.

## Files

    internal/manager/task_generate_sprite.go    load the window, key with GeneratedChecksum
    internal/manager/generator_sprite.go        carry Window through SpriteGenerator
    pkg/scene/generate/sprite.go                SpriteOptions, windowed SpriteVTT + sprite seek

`sceneHash` in this task becomes `models.GeneratedChecksum(*t.Scene, ...)`, for the same reason the
preview's did: two scenes of one file need different sprites. Same suffixed-checksum mechanism, so
`GetSpriteImageFilePath`/`GetSpriteVttFilePath` need no change — and a suffixed checksum has no legacy
path, exactly as for the preview.

## What is deliberately NOT in this change

- **`VttChapter`** is a separate concept (chapter markers, tiled across the file by intent) and is not
  windowed here.
- **The frontend.** RESOLVED by reading the player, and it is the opposite of my first guess.

  `ui/v2.5/src/components/ScenePlayer/vtt-thumbnails.ts` computes, in `updateThumbnailStyle`:

      const duration = this.player.duration();
      const time = percent * duration;
      const currentStyle = this.getStyleForTime(time);

  So a cue is matched against a fraction of whatever the PLAYER is playing — the cues are
  **relative to the media element's own timeline**, not to the file. That makes the choice forced
  rather than a preference:

  - For a windowed scene the player must be playing a windowed stream, or the scrubber is nonsense
    anyway. `StreamDirect` already refuses a ranged scene outright (409 with the window in the body)
    and redirects to the transcoded path when transcoding is on, so there is no reachable state in
    which a ranged scene plays the whole file.
  - Therefore cues stay **relative to the window**, which is also what makes the sprite grid and the
    cues agree: tile `i` is at `window.start + i*step`, and its cue covers `[i*step, (i+1)*step)` of
    the windowed stream.

  This was the spec's one open question and I had written it as "must be resolved against the actual
  player code, not assumed" — which was right, and the answer was not the one I expected: I had been
  weighing absolute cues against relative ones as if both were available.
- **Detection** (choosing a range) — needs upstream discussion.

## Verification

| step | expected |
|---|---|
| unranged sprite unchanged | byte-identical to before; `chunkCount` from the file's duration |
| two scenes of one file differ | distinct `GeneratedChecksum`, distinct sprite paths |
| windowed grid spans the window | `start + i*step` inside `[start, end)` for every `i` |
| short window skips slow seek | no duplicate frames; `chunkCount <= windowFrames` |
| VTT cues inside the window | every cue timestamp within `[start, end]` |
| mutation: stepSize ignores the window start | KILLED |
| mutation: VTT cues unshifted | KILLED |
| mutation: slow seek unshifted | KILLED |
| mutation: sprite key back to GetHash | KILLED |