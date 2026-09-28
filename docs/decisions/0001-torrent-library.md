# 0001 — The BitTorrent library: `anacrolix/torrent`

**Date:** 2026-09-27
**Status:** accepted
**Milestone:** M5, step 5.1

## Context

M5 needs a BitTorrent client: metainfo parsing, magnet links, Kademlia DHT,
the peer wire protocol, piece verification, and resume. Step 5.1 exists to
answer one question before any of that is written — *does an existing library
already cover the surface?* — and the plan is explicit that adopting a library
for a surface you then spend the milestone reimplementing is a failure.

The plan named five capabilities to check: **Kademlia DHT, BEP 47 v2,
super-seeding, sparse, rate limiting.**

Evaluated: `github.com/anacrolix/torrent@v1.61.0` (latest as of this date),
probed with `go doc` against a real module, and confirmed against the library's
source rather than its documentation comments.

## Decision

**Adopt `anacrolix/torrent` v1.61.0 for the whole BitTorrent surface, and
hand-roll ed2k (step 5.4) because no Go library provides it.**

Capability by capability, against the real API:

| Capability | Verdict | Evidence |
|---|---|---|
| Kademlia DHT | **covered** | `ClientConfig.ClientDhtConfig`; `Client.AddDhtNodes`, `AddDhtServer`, `NewAnacrolixDhtServer` |
| BEP 47 v2 / extension protocol | **covered** | `metainfo/bep47.go`, `peer_protocol/metadata.go`; BEP 9 magnet metadata via `Torrent.GotInfo()` / `Info` |
| Rate limiting | **covered** | `ClientConfig.UploadRateLimiter` and `DownloadRateLimiter`, both `*golang.org/x/time/rate.Limiter` |
| Super-seeding | **not covered** | no `SuperSeed` option anywhere in the module; `ClientConfig.Seed` is ordinary seeding |
| Sparse | **not covered** | no sparse option; the `storage` backends are file, mmap, and resource-backed, all of which materialise the full file |

Two gaps, both in features the plan listed but neither of which the downloader
needs to function, and — more importantly — the second finding below means the
library's own path handling cannot be the one we rely on.

## The finding that shaped step 5.2

The library exports `storage.ToSafeFilePath(fileInfoComponents ...string)
(string, error)`, documented as:

> Combines file info path components, ensuring the result won't escape into
> parent directories.

That reads like exactly the guarantee step 5.2 asks us to build. It is not
that guarantee. The whole implementation is 29 lines
(`storage/safe-path.go`), and it is:

```go
safeComps := make([]string, 0, len(fileInfoComponents))
for _, comp := range fileInfoComponents {
    safeComps = append(safeComps, filepath.Clean(comp))
}
safeFilePath := filepath.Join(safeComps...)
switch firstComponent(safeFilePath) {   // <-- the FIRST component only
case "..":
    return "", errors.New("escapes root dir")
default:
    return safeFilePath, nil
}
```

It checks whether the **first component** of the joined path is `..`. That
catches `../../etc/passwd` and `a/../../b`, and nothing else. Measured against
it, with `go run` on v1.61.0:

| Input | Result | Verdict |
|---|---|---|
| `["..", "..", "etc", "passwd"]` | error `escapes root dir` | refused |
| `["a", "..", "..", "b"]` | error `escapes root dir` | refused |
| `["/etc/passwd"]` | `"/etc/passwd"`, **nil error** | **absolute path passed through** |
| `["..\\..\\windows"]` | `"..\\..\\windows"`, **nil error** | **Windows traversal passed through** |
| `["con"]` | `"con"`, nil error | **reserved name passed through** |
| `["trailing."]` | `"trailing."`, nil error | **trailing dot passed through** |
| `["", "x"]` | `"x"`, nil error | empty component silently dropped |

Plus the thing it cannot do at all: **it never touches the filesystem**, so it
cannot see a symlink. A torrent whose first path component is a symlink
pointing outside the root resolves to an in-root path string and lands outside
anyway. The plan's own requirement — *"never via a symlink"* — is unreachable
for any pure-string function.

**So step 5.2's `SanitizeJoin` is not a parallel implementation of something the
library provides. It is the only path-safety this downloader has.** The
library's function may be used as a first cheap filter, and must not be the
last one; the plugin's own `SanitizeJoin` does the rejection, and the final
path is checked with `filepath.EvalSymlinks` against the root after resolution.

This is worth stating plainly because it is the fifth time in this project a
documented behaviour did not match the implementation, and the pattern is
consistent: **a comment describing a guarantee is a claim, not a guarantee.**
Three of the five were in this repo's own migrations; this one was in a
third-party library, which is strictly harder to notice because the comment is
typed as documentation rather than as a comment.

## Consequences

- `anacrolix/torrent` is a **dependency of the plugin module only**
  (`plugins/p2pdownloader/go.mod`). It must not appear in the core's `go.mod`,
  and `TestP2PDownloaderIsNotImportedByCore` is what holds that line.
- The dependency pulls in cgo sqlite (`modernc.org/sqlite`,
  `zombiezen.com/go/sqlite`, `go-llsqlite/crawshaw`) via the storage backends.
  That is a real cost for a downloader and it is why the plugin ships as its
  own static binary rather than being linked into the core. The build emits
  cgo warnings from vendored sqlite; they are upstream and were left alone.
- **Super-seeding and sparse are not available.** Neither is needed for the
  corpus StashForge is acquiring — untracked, self-published material is the
  point, and nobody is seeding a partial file. If either becomes a requirement
  it is a new decision, not a bug in this one.
- `ClientConfig.NoUpload` and `Torrent.AllowDataUpload` /
  `DisallowDataUpload` exist and are relevant to spec §7.1's redistribution
  distinction. They are noted here, not used yet: the consent gate decides
  whether a locator may be acted on at all, and whether a given torrent is
  allowed to seed is a second question that step 5.3 must answer explicitly
  rather than by leaving the library's default.
- ed2k is hand-rolled in step 5.4. Nothing here changes that.
