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

> **Corrected 2026-09-28.** The first version of this section claimed that
> `/etc/passwd`, `..\..\windows` and friends were reachable escapes. They are
> not: `filepath.Join(location, safeName)` cleans the *concatenation*, so a name
> beginning with a separator is re-anchored under `location` and lands inside
> anyway. The claim was about the function's contract, not about a path on disk,
> and I did not check the join before asserting it. The finding below is
> narrower and stronger: **the escape is the symlink, and the library's only
> containment check cannot see one.**

The library exports `storage.ToSafeFilePath(fileInfoComponents ...string)
(string, error)`, documented as:

> Combines file info path components, ensuring the result won't escape into
> parent directories.

The whole implementation is 29 lines (`storage/safe-path.go`) and it checks
whether the **first component** of the joined path is `..`:

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

### What that does and does not stop

Measured, on v1.61.0, both the function and the join the library then performs
(`location = /data/downloads`):

| Name | `ToSafeFilePath` | Lands at | Inside? |
|---|---|---|---|
| `["..", "..", "etc", "passwd"]` | refused `escapes root dir` | — | safe |
| `["a", "..", "..", "b"]` | refused `escapes root dir` | — | safe |
| `["sub", "..", "..", "x"]` | refused `escapes root dir` | — | safe |
| `["/etc/passwd"]` | **accepted** | `/data/downloads/etc/passwd` | safe — the `Join` re-anchors it |
| `["..\..\windows"]` | **accepted** | `/data/downloads/..\..\windows` | safe, one odd filename |
| `["with\x00nul"]` | **accepted** | `/data/downloads/with\x00nul` | safe (the syscall truncates) |
| `["con"]`, `["trailing."]` | **accepted** | as given | safe on Linux; wrong on Windows |

So the string attacks are handled — not by `ToSafeFilePath`'s *logic* being
right, but by `filepath.Join` cleaning the concatenation, which is a property
of the caller rather than of the function. A caller that concatenated with `/`
instead of joining would get the escapes the first version of this ADR claimed.

**The real escape is a symlink, and it is reachable through the classic
storage's own containment check.** `file-client.go:92` looks like a defence:

```go
filePath := filepath.Join(dir, fs.opts.FilePathMaker(...))
if !isSubFilepath(dir, filePath) {
    err = fmt.Errorf("file %v: path %q is not sub path of %q", ...)
}
```

and `isSubFilepath` (`storage/file-paths.go:32`) is a **string** check:

```go
func isSubFilepath(base, sub string) bool {
	rel, err := filepath.Rel(base, sub)
	if err != nil { return false }
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
```

`filepath.Rel` is pure string arithmetic. It cannot resolve a symlink, so a
torrent whose directory name is a symlink pointing outside the download root
passes this check. Executed, with the library's own functions:

```
download root : .../downloads
file path     : .../downloads/innocent/passwd      (innocent is a symlink)
isSubFilepath -> true
would create  : .../downloads/innocent
really is     : .../outside          (EvalSymlinks)
relative      : "../outside" -> *** OUTSIDE ***

wrote         : ".../downloads/innocent/passwd"
the decoy OUTSIDE the download root now reads: "PEERS CONTROLLED BYTES"
```

The library's own containment check said `true` and the write landed outside
the download directory anyway.

**The mmap storage is worse and says so.** `storage/mmap.go` calls
`grep -c isSubFilepath` → **0**, and carries its own TODO: *"Support all the
same native filepath configuration that NewFileOpts provides."* It also
prepends `md.BestName()` unconditionally, where the classic path skips it when
the torrent is nameless (BEP 52 v2) — so in the mmap path the first component
that `ToSafeFilePath` inspects is the file's own first component.

**So step 5.2's `SanitizeJoin` is not a parallel implementation of something the
library provides. It is the only path-safety this downloader has**, and it has
to be enforced at OUR storage layer, because the library's is a string check
that a symlink defeats. `internal/paths` is written to run
`filepath.EvalSymlinks` on both sides and compare with `filepath.Rel`, which is
the filesystem-resolving version of the check the library intended to write.

This is the sixth time in this project a documented behaviour did not match the
implementation, and the pattern is consistent: **a comment describing a
guarantee is a claim, not a guarantee.** Five were in this repo's own
migrations; this one is in a third-party library, which is strictly harder to
notice because the claim is a doc comment on a function whose name says exactly
what it does.

And the second half of the lesson, which cost more than the first: **measure the
caller, not just the callee.** `ToSafeFilePath` accepts `/etc/passwd`, which
looks damning. It is not, because the caller's `filepath.Join` neutralises it. I
wrote "the absolute path is passed through" and moved on, when the question was
"where does the file end up". The first version of this ADR would have had a
reader go looking for a hole that does not exist, and — worse — would have made
the real one, the symlink, look like one item in a list of string bugs.

## What was built

`plugins/p2pdownloader/internal/storage/gate.go`. A `storage.ClientImpl` that
wraps the classic backend, validates every file in a torrent before handing it
to the library, and routes the library's own path makers through
`internal/paths` as a backstop.

**The classic backend is the only usable one** and the measurement above is why:
`NewMMap` has no containment check at all, and the classic one has a check that
is a string comparison. The gate supplies the filesystem-resolving version.

**The library's extension points cannot report an error.** `FilePathMaker` and
`TorrentDirFilePathMaker` both return a bare `string`, and the library calls them
before `OpenTorrent` gets a chance to return anything. So a per-file refusal has
nowhere to go: it becomes a sentinel filename and the transfer completes with the
wrong file in it. The gate therefore refuses the **whole torrent** in
`OpenTorrent`, which is the only place a real error can be returned, and uses
the makers purely as a backstop.

**The joining trap.** The obvious up-front check validates a file's name as one
string, and it does not work:

```go
name := filepath.Join(append([]string{info.BestName()}, file.BestPath()...)...)
paths.SanitizeJoin(root, name)   // <-- the traversal is already gone
```

`filepath.Join` cleans its result, so `["sub", "..", "..", "escape"]` becomes
the string `"escape"` before the gate sees it. `SanitizeJoin` is not wrong — it
is handed a name that no longer contains the attack. So the gate checks each
component **as the torrent supplied it** and never pre-joins; the joined name is
only ever an output, for the error message.

That is also why `ToSafeFilePath` looks adequate and is not: it joins first and
checks the first component of the **result**, which is a different question from
"does any component of the input walk out".

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
