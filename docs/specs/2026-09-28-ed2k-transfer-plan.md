# Step 5.5 — the ed2k transfer: spec and plan

Written 2026-09-28, immediately after step 5.4 closed with every gate green
(`fe9dc1a21`). This is the step that turns an ed2k link into bytes on disk, and
it is the last thing between the plugin and a working download.

## 0. Where this sits, and what already exists

The plugin's ed2k path is built in three layers, and this step adds the third:

| Layer | Package | State |
|---|---|---|
| locators (parse `ed2k://…`, validate the name) | `internal/ed2k` | done |
| the wire (login, ext hello, search, Kad) | `internal/ed2kwire` | done, live-proven |
| **the transfer (request a file, read parts, write it)** | **`internal/ed2kwire`** | **this step** |

`internal/rpc/rpc.go:383` ends every granted ed2k link in
`ErrTransferNotImplemented`. That is the last honest refusal, and this step
replaces it.

**What is already proven and is NOT re-derived here:** framing (the size field
counts the opcode), the tag codec in both directions, obfuscation of the first
packet only, the real deadlines, and a live search returning 299 decodable
results from `85.17.116.222:6082`. The transfer reuses all of it.

**The symbols this plan builds on, all verified to exist:**

- `ed2kwire.Server` — one connected server; `conn net.Conn`, `sentFirst bool`
- `ed2kwire.Dial(ctx, addr) (*Server, error)`
- `ed2kwire.SearchRequest{Keyword string; MaxResults int}`,
  `(SearchRequest).Build() ([]byte, error)`, `(s *Server).Search(req) error`
- `ed2kwire.SearchResult{Name string; Size int64; Type uint32; Sources uint32;
  SourcesComplete uint32; Hash [16]byte; UserID uint32; Port uint16}` — the
  field names are verbatim and verified; `UserID`/`Port` are the **only**
  handle to a source a server gives us, and the plan's whole order follows
  from that.
- `ed2kwire.DecodeSearchResult(plain []byte) ([]SearchResult, error)`
- `ed2kwire.TagList` / `Tag` / `encodeTagList` / `parseTagListAt` (unexported)
- `ed2k.ParseHash(s string) (Hash, error)`, `ed2k.Hash`, `ed2k.HashLength`
- `rpc.ErrTransferNotImplemented` — the sentinel this step retires

**No capture is needed for this step.** The earlier milestones were blocked on
observing real bytes; the request framing is already proven by the login, the
extended hello and the search. What is new is the part that only a real source
can confirm, and that is called out as a live test rather than assumed.

## 1. What the protocol actually requires, and the order

eDonkey2000's source protocol is a **request/response conversation per part**,
not a stream. The order is forced, and the reason is not obvious:

1. **OP_EDONKEYPROT / server handshake** — already done, reused. *Before
   anything*, because every source is reached the same way.
2. **OP_REQUESGPART (0xD4 → no: the REQUEST is 0xD4 only for results)** —
   the client asks a source for one part. **A part is a 9,500-byte PARTSIZE
   window**, and the request names the file hash plus a part number. This is
   why the plan is ordered part-by-part: the first part is what proves the
   conversation, and a stream-oriented design cannot be tested at all before
   one part round-trips.
3. **OP_PARTPACKET (0xD5-ish)** — the source answers with up to
   `PARTSIZE` bytes plus a checksum and the file's size.
4. **Repeat for every part**, then verify the whole file against its ed2k hash.

**Why part-by-part and not a stream:** the source is a stranger. A single
unbounded read gives a stranger an unbounded write. Each part is bounded,
verifiable, and independently attributable in a log, which is what makes a
failed download resumable rather than a mystery.

**The ordering constraint that is not obvious:** a source connection is
established *per download*, not per file, and a source that has the file but
is firewalled cannot be reached at all — the search result's `UserID`/`Port`
is the only handle we get. So the transfer's first milestone is not "download
a file" but "resolve a `SearchResult` into a reachable source", because that
is where this can fail and nothing downstream exists yet.

## 2. Scope — what ships in this step, and what does not

**Ships:**

- a source connection handshake (reuse the server-side framing)
- `OP_REQUESGPART` for one part, and its answer decoded
- the ed2k hash of a completed file, verified against the link's hash
- `internal/rpc` no longer returning `ErrTransferNotImplemented` for ed2k
- the transfer's errors, named specifically enough to act on

**Does NOT ship, and why:**

- **A Kad file-source lookup.** Kad source finding is a second protocol (KAD2
  `OP_KAD2_SEARCH_SOURCE_REQ`, `OP_START_TRANSFER`) with its own framing. It
  is real work and is a separate step; pretending the source list is "the
  search results" would be wrong and would hide a whole protocol behind a
  claim.
- **Partial/resume across restarts.** Each part is verified, so the data
  supports resume, but no resume index is persisted in this step.
- **Multiple sources per file.** Pick the first that answers; the design leaves
  room for more and does not pretend to have tried them.
- **Chunked parallel download.** One part at a time, deliberately: PARTSIZE
  windows make this a poor parallel story, and parallelism here would be
  untestable against real sources.

**The honest gap this leaves:** a file with *no* server-side source (a Kad-only
file) still cannot be downloaded, and the error says so by name. That is a
better state than today's blanket `ErrTransferNotImplemented`, because it names
which half is missing.

## 3. The failure modes this is built to survive

Each of these is a real behaviour of the network, not a hypothetical:

- **The source never answers.** A timeout with the address in the message.
  A caller cannot distinguish "firewalled" from "gone" without trying, and a
  generic `i/o timeout` names neither.
- **The source sends something we do not model.** Its banner, or a Kad packet
  on a source port. This happened during the Kad work: 7 of 40 contacts
  answered with `unsupported kad protocol header`, and the message said which.
- **The part arrives short.** Fewer than PARTSIZE bytes on the last part is
  normal; fewer on any other part is a truncated transfer and is an error,
  because silently accepting it writes a corrupt file.
- **The hash does not match.** The file is *not* written. This is the single
  most important line in the step: a wrong file on disk is worse than no file,
  because the user cannot tell.
- **The file is larger than the link's size.** A stranger's size claim; the
  parts decide, not the claim.

## 4. Trade-offs taken, stated so they can be argued with

- **A `[16]byte` hash at the wire layer, not `ed2k.Hash`.** `ed2kwire` sits
  below `ed2k`; importing up inverts the dependency. Convert at the boundary.
- **Parts, not a stream.** Slower, and it is the reason a bad source cannot
  do unbounded damage.
- **The first part is the milestone.** One part proves the conversation; a
  whole file proves nothing extra about the protocol and hides a bug in the
  part loop behind a long test.
- **The hash check is the gate, not the size.** Size is a claim; the hash is
  the identity.

---

# PLAN

Each step ends in a command and the output it must print. **If the output
differs, stop.**

## Step 1 — the source handshake, reusing the server framing

**New file:** `internal/ed2kwire/source.go`

The framing is the server's, and the reason it can be reused is that
`Server` already models exactly one connection with `sentFirst` for the
first-packet obfuscation rule. A source is the same conversation with a
different role, so the plan introduces `Source` as a distinct type over the
same primitives — **not** a second `Server`, because a source has no
`0x40` server-info packet and forcing one to parse would be a lie.

```go
// source.go — new. A source is a peer holding a file, not a server.
//
// NOT A Server. A source does not send OP_SERVERINFO and has no users or
// files; giving it those fields would invite a caller to read a count that
// is always zero, and a zero that reads as a fact is worse than a
// missing field.
type Source struct {
	addr    string
	conn    net.Conn
	guid    [16]byte
	sentFirst bool
}

// DialSource connects to a source and completes the obfuscated handshake.
//
// The handshake is the same as Dial's, and is NOT yet factored out of it:
// doing that in the same commit as a new feature means the diff contains
// both a refactor and a feature and neither can be judged. Step 6 does the
// refactor, with the transfer as the second caller to prove it.
func DialSource(ctx context.Context, addr string) (*Source, error)
```

`DialSource` returns `ErrRefused` if the peer sends nothing before the
deadline — **an existing sentinel in `server.go`, verified present**, so a
caller handles "this peer is silent" with one case instead of two.

> `ErrRefused` is one of four real sentinels in this package: `ErrNotAServer`,
> `ErrTruncatedPacket` and `ErrRefused` in `server.go`, and `ErrNotZlib` in
> `exthello.go`. An earlier draft of this plan invented a fifth,
> `ErrNoAnswer`, and was wrong — a plan that names a sentinel which does not
> exist teaches the implementer to trust the plan instead of the tree. That
> is the failure this document is supposed to prevent, so it is recorded
> here rather than quietly fixed.

**Verify:**

```bash
cd ~/code-local/go/stash/plugins/p2pdownloader
GOFLAGS=-mod=mod go build ./... && echo CLEAN
GOFLAGS=-mod=mod go test ./internal/ed2kwire/ -run 'TestASourceThatNeverSpeaksIsRefused' -v
```

Expected: `CLEAN`, and one `--- PASS`. The test dials a listener that accepts
and never writes, and asserts the error names the address.

## Step 2 — one part, requested and decoded

**New file:** `internal/ed2kwire/part.go`

```go
// The wire constants. PARTSIZE is 9500 and is not negotiable: it is what
// both ends use to size the window, and a source that answers with a
// different amount is not an error -- the last part is shorter by nature.
const (
	opRequestPart  byte = 0xD4  // TO BE VERIFIED against a live source; see below
	opPartPacket   byte = 0xD5  // likewise
	PartSize            = 9500
)

// PartRequest asks a source for one part of a file.
type PartRequest struct {
	Hash [16]byte
	Part uint32
}

// Build returns the part request's PAYLOAD, not a framed packet, for the
// same reason SearchRequest.Build does: framing is the connection's job.
func (r PartRequest) Build() ([]byte, error)

// PartAnswer is a source's reply to a part request.
type PartAnswer struct {
	Data      []byte
	FileSize  uint32
	HasChecksum bool
}

func (s *Source) RequestPart(ctx context.Context, r PartRequest) (*PartAnswer, error)
```

> ### THE OPCODES IN THIS STEP ARE UNVERIFIED, AND THE PLAN SAYS SO
>
> This package has **never observed a source-side packet**. Every opcode above
> `0x33` in this document is the published eDonkey2000 value taken on trust,
> and the two constants in the snippet are written as placeholders precisely
> so an implementer cannot copy them past a check.
>
> **The check is step 5, and a failure there is a successful plan** — a live
> source that does not answer `0xD4` has told us the constant is wrong, which
> no amount of reading would have. The values are then corrected in this file
> in the same commit as the code, per the rule that the plan is a living
> contract.
>
> This is the opposite situation from step 5.4, where every constant was
> confirmed against a real server before it was written down. There is no
> such confirmation available here yet, and pretending otherwise is how a
> plausible-but-wrong opcode becomes a permanent assumption.

**The payload is a 4-byte little-endian part number followed by the 16-byte
hash** — a tag-free binary body, unlike every other packet in this package,
and that asymmetry is worth its own comment in the code.

**Verify:**

```bash
GOFLAGS=-mod=mod go test ./internal/ed2kwire/ -run 'TestThePartRequestIsAHashAndAnIndex' -v
```

Expected: `--- PASS`, and the test asserts the golden bytes by hand, not a
round trip — a part request is 20 bytes with no tag list, and a round trip
through our own writer would prove nothing.

## Step 3 — the hash gate, and nothing is written on a mismatch

**New file:** `internal/ed2k/verify.go` (this layer, not `ed2kwire`)

The ed2k hash is an MD5 over a specific layout, and it belongs beside the
existing `ed2k.Hash` type.

```go
// VerifyPart checks one part's MD5 against the expected value.
//
// ed2k hashes each part SEPARATELY, not the whole file: the part's bytes,
// prefixed with the part number as 4 little-endian bytes. That is why a
// single bad part is attributable and why the whole-file hash is not simply
// MD5(contents).
func VerifyPart(part uint32, data []byte) [16]byte
```

**Verify:**

```bash
GOFLAGS=-mod=mod go test ./internal/ed2k/ -run 'TestAPartHashIsItsBytesAndItsIndex' -v
```

Expected: `--- PASS`, against a **golden hex value computed by an independent
tool** (e.g. `python3 -c "import hashlib;print(hashlib.md5(b'\x01\x00\x00\x00'+b'payload').hexdigest())"`),
not against our own output. This is the rule the whole package works by: a
round trip proves two halves agree, never that either is right.

## Step 4 — `internal/rpc` stops refusing, and says what it cannot do

**Modify:** `internal/rpc/rpc.go` (the `ErrTransferNotImplemented` return at
line 383)

Two new sentinels, so the caller learns *which* half is missing:

```go
// ErrNoSource means no reachable source holds this file. A Kad-only file
// reaches here: the search found nothing, and finding sources over Kad is
// not implemented. It is named apart from a transfer failure because the
// remedy is different -- a different file, or the Kad work.
var ErrNoSource = errors.New("no reachable ed2k source holds this file")

// ErrTransferFailed means a source was reached and the transfer did not
// complete: a hash mismatch, a short part, or a timeout mid-transfer.
var ErrTransferFailed = errors.New("the ed2k transfer failed")
```

`TestTheDownloadStubStillReportsTheTransferIsUnimplemented` — the test the
spec's §5 requires — **changes name and meaning** to
`TestAnEd2kLinkWithNoSourceSaysSoByName`, and asserts the specific sentinel.
A test that keeps the old name while asserting the new behaviour is a lie
about what is being tested.

**Verify:**

```bash
GOFLAGS=-mod=mod go test ./internal/rpc/ -run 'TestAnEd2kLinkWithNoSourceSaysSoByName' -v
```

Expected: `--- PASS`, and the old test name must no longer exist:

```bash
GOFLAGS=-mod=mod go test ./internal/rpc/ -run 'TestTheDownloadStubStillReportsTheTransferIsUnimplemented' -v
# test.*: warning: no tests to run   <- EXPECTED, and that is the point
```

## Step 5 — the live test, and it is the only real proof

A source is a stranger, and everything above is a claim about what it will do.
**This is the step that can genuinely fail**, and it is why the earlier
milestones needed captures.

Behind the existing `ed2klive` build tag, in `internal/ed2kwire/live_test.go`:

- take a `SearchResult` from a live search against `85.17.116.222:6082`
- dial its `UserID`/`Port` as a source
- request **one** part, and assert the answer's length is `PartSize` or less
- assert the part's MD5 matches `VerifyPart`'s expectation

**It refuses to skip.** The Kad live tests already established the pattern and
the reason: a live test that skips reports `ok` having proved nothing. Without
`ED2K_LIVE_SERVERS` this test FAILS with the instruction to set it.

**Verify:**

```bash
GOFLAGS=-mod=mod ED2K_LIVE_SERVERS=85.17.116.222:6082 \
  go test -tags ed2klive ./internal/ed2kwire/ -run 'TestLiveOnePartComesBackFromARealSource' -v
```

Expected: `--- PASS`, or a **named** failure (`no source answered` /
`the answer is not a part packet`). Either is progress; a hang is not, and the
existing deadline tests are the model for that.

## Step 6 — factor the handshake out, with two callers

Only now, with the transfer working: extract the shared handshake from `Dial`
and `DialSource` into one unexported function, and make both call it.

**This is ordered last on purpose.** Refactoring `Dial` and adding a feature in
one commit produces a diff where neither can be reviewed, and the refactor's
test coverage is the feature's coverage. The transfer is the second caller
that makes the extraction honest — and if the two handshakes turn out to
differ, that is worth learning *after* both work, not before.

**Verify:** the full hermetic suite, unchanged in outcome:

```bash
GOFLAGS=-mod=mod go test ./... -count=1
```

Expected: 9 packages `ok`, and **the ed2kwire test count must not decrease**.

## Step 7 — the gate, verbatim from step 5.4's §5

```
go build ./...                                  clean
go vet ./...                                    clean
gofmt -l internal/ed2kwire/                     clean
go test ./... -count=1                          green, no network
python3 internal/ed2kwire/mutate_ed2kwire.py    0 survived, PYEXIT=0
go test -tags ed2klive ./internal/ed2kwire/     reported honestly, including failure
```

**And the honest advertisement stays honest.** The plugin advertises what it
can do; a file with no reachable source returns `ErrNoSource` **by name**, not
a generic failure and not a silent success. The spec's existing rule — that a
granted link must not silently do nothing — is unchanged in force, and this
step narrows the blanket refusal to two specific, actionable errors.

## 8. What this plan deliberately does not settle

Per the rule that a detailed plan for an unstarted milestone is written from
imagination, the following are named but **not** specified here, and each gets
its own spec when it starts:

- Kad source lookup (`OP_KAD2_SEARCH_SOURCE_REQ`, `OP_START_TRANSFER`)
- persisting a resume index
- multi-source and parallel transfer
- the plug-in UI's progress reporting, which is the host's concern

**Provenance.** Third-party protocol behaviour is verified by the live tests
in step 5, not by this document. This plan cites opcode values and payload
layouts it has not itself observed, and marks them TO BE VERIFIED; a capture
in `internal/ed2kwire/testdata/` is the durable form of that evidence, and the
search milestone's capture is the precedent.
