# stash#3530 — the preview/webp cache key must distinguish windows

Predecessors: `docs/ISSUE-3530-cover-spec.md` section 4, which deferred this deliberately.

## The defect

The cover/preview *content* now comes from inside the window (`8f84c565f`). The cache key does not:

    GetVideoPreviewPath(checksum)  ->  shardedJoin(Screenshots, checksum, checksum+".mp4")
    GetWebpPreviewPath(checksum)    ->  shardedJoin(Screenshots, checksum, checksum+".webp")

`checksum` is `scene.GetHash()`, and that returns `s.Checksum` (MD5) or `s.OSHash` — **properties of
the FILE**. So two scenes backed by one file resolve to the same preview path, and after the last
commit they need *different* previews. Measured shape:

    scene A  0..240s  of file X   ->  <screens>/ab/abcd...abcd.mp4
    scene B  240..480s of file X  ->  <screens>/ab/abcd...abcd.mp4   <-- SAME

Whichever generated first wins, silently: both filenames and both URLs look correct, and the second
scene is served a preview of the first. This is the same defect already fixed for HLS segments in
`stash-3530-hls`, where the key was `hash_streamType_size`; here it is the hash alone.

## What NOT to do

**Do not change `GetHash`.** It has ~12 callers spanning preview, sprite, VTT thumbs, funscript,
export (`task_export.go:518`) and scene markers (`task_generate_markers.go:103`). Folding a window
into it would rename generated files for export and marker paths, change URLs the frontend and any
external client already have bookmarked, and couple the *file* identity to a *scene* attribute.
`GetHash` answers "what file is this", and that answer should not depend on which window of it you
are looking at.

## Design: a distinct checksum only where a window exists

The filename is `checksum + ".mp4"` and the shard directory is derived from the checksum, so a
suffixed checksum flows through the whole path mechanism untouched — no new path helpers, no change
to `shardedJoin`, no change to sharding depth.

```go
// GeneratedChecksum returns the key a generated artefact for this scene is stored under.
//
// #3530 - it is GetHash() for an unranged scene, and GetHash() plus a window suffix for a
// ranged one, so two scenes of ONE file get different previews.
//
// The suffix is added to the CHECKSUM rather than to the filename so the shard directory
// moves with it: shardedJoin derives the intra-dir from the checksum, so appending to the
// filename alone would leave both scenes sharing one shard directory.
func GeneratedChecksum(scene Scene, hashAlgorithm HashAlgorithm) string
```

### The suffix format

    <checksum>_w<start>-<end>

with the seconds to 3 decimals — `abcd_w60.000-300.000`. Chosen because:

- **it sorts and reads as a boundary**, so a directory listing is diagnosable;
- **3 decimals** because that is `Args.Seek`'s precision elsewhere in this work and a preview
  regenerated at 60.0001 vs 60.0002 must not be a different key;
- **no window at all → no suffix**, so every existing installation's files keep their current names
  and no cache is invalidated. This is the whole reason the suffix is conditional.

### The one thing that makes this safe

`ResolveGeneratedFile` (`generated_resolve.go:93`) tries the sharded path and falls back to the
LEGACY flat path. For a windowed scene the legacy lookup must use the **same suffixed checksum** or
it will find nothing — and worse, for an unranged scene it must NOT be suffixed, or every
pre-existing preview 404s.

So the suffix has to be applied at every call site that resolves a generated file for a scene, not
only at the generator. The call sites are exactly the `sceneHash := scene.GetHash(...)` lines in
`routes_scene.go`, plus `task_export.go` and `task_generate_markers.go` — but **only the preview and
webp ones may be suffixed**; sprite, VTT, funscript and export stay on the plain hash because this
commit does not make them window-aware, and a windowed sprite is a separate piece of work. Changing
them now would rename files for content that has not changed.

## Verification

| step | command | expected |
|---|---|---|
| unranged keeps its name | `go test ./pkg/models/... -run TestAnUnrangedSceneKeepsItsExactChecksum -v` | PASS; byte-identical |
| two windows differ | `go test ./pkg/models/... -run TestTwoScenesOfOneFileGetDifferentChecksums -v` | PASS |
| path moves with the shard | `go test ./pkg/models/paths/... -run TestTheShardDirectoryFollowsTheWindow -v` | PASS |
| resolve finds a windowed file | `go test ./pkg/models/paths/... -run TestResolveFindsAWindowedPreview -v` | PASS |
| unranged still resolves legacy | `go test ./pkg/models/paths/... -run TestResolveStillFindsAnUnrangedLegacyPreview -v` | PASS |
| mutation: always suffix | drop the `!Set` guard | KILLED |
| mutation: suffix the filename only | append after `checksum+".mp4"` instead | KILLED |
| mutation: ignore the end | `w<start>` only | KILLED |
| mutation: apply to sprite too | suffix in the sprite route | KILLED |
| full | `go build ./... && go test ./pkg/models/... ./internal/manager/ -count=1` | ok |

## Out of scope, and why

- **Sprite / VTT thumbs.** Same class of defect, but they are generated by a different task with
  their own cache and are tiled across the duration, so window-awareness there is its own piece. The
  commit message states that they stay on the plain hash *because they are not yet window-aware* —
  so nobody reads this as an oversight.
- **Transcode artefacts** (`GetTranscodePath`) are already window-distinguished through the HLS
  segment key from `stash-3530-hls`; the progressive-transcode cache is not addressed here.
- **Cache migration.** Old previews for scenes that later become ranged are simply not found and are
  regenerated. That is the desired behaviour: the old file is a preview of the wrong thing.