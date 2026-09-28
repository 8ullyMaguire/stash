# The ed2k transfer: one part, one file, written only if proven

**Date:** 2026-09-28 · **Status:** spec written, not started
**Predecessor:** `2026-09-28-ed2k-transfer-plan.md` (steps 1–3 done: wire,
opcodes, hash gate)

## 1. What this milestone is

`internal/ed2kwire` can dial a source, request a window and read the answer.
`internal/ed2k` can prove whether those bytes are the file a link named.
**Nothing connects the two, and nothing writes a file.** `internal/rpc` still
returns `ErrTransferNotImplemented` for every ed2k link.

This milestone adds the piece that decides *where the bytes go*, and the
narrowest useful transfer that exercises it. Deliberately one part, one file:
a resumable multi-source transfer is its own spec with its own evidence
problem (see §8).

## 2. WHY `internal/storage.Gate` CANNOT BE USED, and `paths` CAN

**The Gate is libtorrent's.** Its entry point is

```go
func (g *Gate) OpenTorrent(info *metainfo.Info) (libstorage.FilePathMaker, error)
```

It refuses on a torrent's file list. An ed2k file has no `metainfo.Info` and
no torrent, so there is nothing to hand it — and the temptation is to widen
the Gate to accept a name, which would put an ed2k-shaped hole in a
torrent-shaped defence.

**But the name is peer-supplied here too, and that is the same bug class.**
`ed2k://|file|../../etc/passwd|1|<hash>|` is a well-formed link, and `rpc.go`
already refuses it via `ed2k.Parse` before the proposal. The defence exists;
what is missing is the *second* consumer of that fact.

**So the writer uses `paths.SanitizeJoin(root, name)` directly.** It is the
one function that already resolves a stranger's name under a root, and it is
tested against symlinks, shared prefixes, Windows reserved names and dots.
Reimplementing that for ed2k is the mistake the package doc warns about.

## 3. `internal/ed2ktransfer` — the new package

```go
package ed2ktransfer

// ErrNoSource   no reachable source holds this file
// ErrFailed     a source was reached and the transfer did not complete
// ErrNoSpace    the file does not fit where it is going

type Config struct {
    Root      string        // REQUIRED, must already exist
    DialTimeout time.Duration // zero => 3s
}

type Transfer struct { ... }
func New(cfg Config) (*Transfer, error)
func (t *Transfer) FetchOnePart(ctx context.Context, f ed2k.Locator, src ed2kwire.Source) (Fetched, error)
```

`Fetched` reports what was proven:

```go
type Fetched struct {
    Path     string  // where the bytes are
    Bytes    int64
    Hash     ed2k.Hash
}
```

### 3.1 THE ORDER OF OPERATIONS, WHICH IS THE WHOLE DESIGN

```
1. SanitizeJoin(root, name)            -> refuse an escaping name
2. build the part windows from the size -> refuse a size of 0 or over 2^38
3. request window 0 from the source
4. refuse ErrNoFile by name            -> the source does not hold the file
5. the answer's hash must equal the link's  <- BEFORE any write
6. SanitizeJoin again, for the path    -> the root may have changed under us
7. write to a TEMP file
8. VerifyBytes against the LINK'S hash
9. on success: rename into place
10. on failure: remove the temp file and say so
```

**Step 5 before step 7 is the point of the whole file.** A source that
answers with the wrong file is a stranger being wrong, and the only safe
response is to notice before anything touches the disk.

**Step 8 after step 7, and not before, is also deliberate.** Hashing bytes in
memory would mean keeping a whole part resident to verify it, and a part is
9,728,000 bytes. Writing first and verifying the file is what a real
downloader does, and the temp-file dance is what makes a failed verification
leave no file.

**Step 9 is a rename, not a copy.** A rename within one directory is atomic,
so a reader either sees no file or sees a complete one. A partially written
file with the right name is worse than no file.

## 4. The refusals, by name

Each of these is a different fault with a different remedy, so each is a
distinct sentinel. A single `ErrFailed` would send triage to the wrong place
in every case.

| Refusal | Means | Remedy |
|---|---|---|
| `ErrNoSource` | no source answered, or `ErrNoFile` | a different file, or Kad source lookup |
| `ErrHashMismatch` | bytes are not the file | fetch again; the source is unreliable |
| `ErrNoSpace` | does not fit on the volume | free space, or another root |
| `paths.ErrEscapes` | the name is hostile | never; this is an attack |

`ErrNoSource` and `ErrHashMismatch` both end the transfer, and the difference
is whether the *source* or the *file* is the problem.

## 5. What is deliberately NOT here

- **Multi-part files.** `FetchOnePart` handles a file of one part. A file of
  two parts needs the block-hash tree in `ehash.go`, which hashes
  *part hashes* and so needs every part before the file hash can be checked.
  One part means the file hash IS the part hash, which is the one case where
  the two coincide.
- **Resuming.** No index, no partial file kept after a failure.
- **Multiple sources.** One source, one attempt.
- **Progress reporting.** The host's concern.

## 6. Honest refusals in `internal/rpc`

`ErrTransferNotImplemented` is **replaced**, not wrapped, by:

```go
var ErrNoSource = errors.New("no reachable ed2k source holds this file")
```

`TestTheDownloadStubStillReportsTheTransferIsUnimplemented` changes name and
meaning to `TestAnEd2kLinkWithNoSourceSaysSoByName`. A test that keeps the
old name while asserting new behaviour is a lie about what is tested.

The advertisement must not become quieter than the capability. The plugin
still cannot transfer anything, and it must say so as specifically as it
can: a named `ErrNoSource` is strictly more honest than a blanket
"unimplemented", because it names what is missing.

## 7. Verify

```bash
# hermetic, no network
GOFLAGS=-mod=mod go build ./... && GOFLAGS=-mod=mod go vet ./...
GOFLAGS=-mod=mod go test ./... -count=1

# the refusal matrix
GOFLAGS=-mod=mod go test ./internal/ed2ktransfer/ -v
```

Expected: all packages `ok`; the transfer package's tests cover every row of
the §4 table, and each asserts the SPECIFIC sentinel via `errors.Is`.

## 8. PROVENANCE and what this does not settle

**Constants.** `PartSize` and `BlockSize` come from `ed2kwire`, which cites
eMule's `opcodes.h` (`PARTSIZE 9728000`, `EMBLOCKSIZE 184320`). Nothing new
is defined here — a second definition of either is a second chance to
disagree.

**The 2^38 ceiling** is eMule's stated maximum file size. It is a *limit*,
not a behaviour: a link claiming more is refused as a link that cannot be
real, which is the same class as refusing a name that escapes the root.

**Not settled here, each needing its own spec with its own evidence:**

- multi-part files, and when the block-hash tree becomes necessary
- a resume index, and what a partial file's name should be
- multi-source and parallel transfer, and what "verified" means when two
  sources disagree
- Kad source lookup, which is what would turn `ErrNoSource` into something
  rarer
- the eMule hash-set download that would let `VerifyPart` attribute a failure
  to one window, so a failed download resumes instead of restarting
