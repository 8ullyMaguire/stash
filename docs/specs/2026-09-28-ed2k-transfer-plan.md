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
// # THE WIRE CONSTANTS, CORRECTED AGAINST eMule's OWN opcodes.h
//
// The first draft of this step had opRequestPart = 0xD4 and opPartPacket =
// 0xD5, both invented. Checking them against eMule's opcodes.h (the upstream
// C++ client) found 0xD4 is OP_PACKEDPROT -- the zlib-compressed PROTOCOL
// BYTE, already used by the search result decoder in this package. Writing
// 0xD4 as a request opcode would have asked a source for something in a
// protocol this client does not speak.
//
// The real source-side pair, verbatim from opcodes.h with its own comments:
//
//	#define OP_SENDINGPART   0x46   // <HASH 16><von 4><bis 4><Daten len:(von-bis)>
//	#define OP_REQUESTPARTS  0x47   // <HASH 16><von[3] 4*3><bis[3] 4*3>
//	#define OP_FILEREQANSNOFIL 0x48  // <HASH 16>
//
// # AND THE PAYLOAD IS OFFSETS, NOT A PART NUMBER
//
// This is the part the draft got most wrong, and it is worth stating
// because "part N" is the natural mental model and it is not the wire.
// A part request carries a THIRD hash (the transfer's identity, distinct
// from the file's), then three start offsets and three end offsets, asking
// for three part-sized blocks in one packet. The answer is OP_SENDINGPART
// with the file hash, a start offset, an end offset, and the bytes between.
//
// The draft's `Part uint32` does not exist on the wire. There is no part
// index in either packet; both speak in byte offsets, and the 9500-byte
// window is a division the two ends apply to those offsets.
const (
	opSendingPart  byte = 0x46
	opRequestParts byte = 0x47
	opFileReqAnsNoFile byte = 0x48

	// PartSize is the 9500-byte window. eMule's own constant, and both
	// ends divide a file by it, so it is not negotiable -- though the
	// LAST window of a file is short by nature and is not an error.
	PartSize = 9500
)

// PartRequest asks a source for one window of a file.
type PartRequest struct {
	// FileHash is the file's 16-byte ed2k hash.
	FileHash [16]byte

	// Start and End are BYTE OFFSETS, half-open: [Start, End). End is
	// therefore Start + the window length, and there is no inclusive
	// bound to get wrong by one.
	Start uint32
	End   uint32
}

// Build returns the part request's PAYLOAD, not a framed packet, for the
// same reason SearchRequest.Build does: framing is the connection's job.
func (r PartRequest) Build() ([]byte, error)

// PartAnswer is a source's reply to a part request.
type PartAnswer struct {
	// FileHash is the file the bytes are for, as the source states it. A
	// source answering about a file we did not ask for is a protocol
	// error and not a confusing name later.
	FileHash [16]byte

	// Start and End are the offsets of the bytes, half-open.
	Start uint32
	End   uint32

	// Data is the window's bytes: len(Data) == End-Start.
	Data []byte
}

func (s *Source) RequestPart(ctx context.Context, r PartRequest) (*PartAnswer, error)
```

> ### THE OPCODES ARE NOW CITED, NOT ASSUMED — AND ONE WAS PLAINLY WRONG
>
> The draft marked these TO BE VERIFIED and said a live failure in step 5
> would be a successful plan. Checking them against eMule's `opcodes.h`
> before writing the code was cheaper than a live test and found a worse
> error than a wrong constant: **`0xD4` is `OP_PACKEDPROT`**, a protocol
> byte this package already reads, not a request opcode at all.
>
> The values now in the plan are quoted from that file, comments included,
> and `docs/PROVENANCE.md` records where they came from so the next person
> can re-verify rather than trust. What remains genuinely unverified is
> whether a real source accepts this client's handshake *and then* answers —
> that is behavioural, not a constant, and only step 5 can settle it.

**The payload is tag-free binary, unlike every other packet in this package**
— no tag count, no tags, just a hash and offsets. That asymmetry is worth
its own comment in the code, because `parseTagList` applied to a part request
would read the hash's first bytes as a tag count.

**Verify:**

```bash
GOFLAGS=-mod=mod go test ./internal/ed2kwire/ -run 'TestThePartRequestIsAHashAndAnIndex' -v
```

Expected: `--- PASS`, and the test asserts the golden bytes by hand, not a
round trip — a part request is 20 bytes with no tag list, and a round trip
through our own writer would prove nothing.

## Step 3 — the hash gate, and nothing is written on a mismatch

**New file:** `internal/ed2k/verify.go` (this layer, not `ed2kwire`)

### THE PLAN HAD BOTH CONSTANTS WRONG, AND THE WIRE PROVES IT

Checked against eMule's own `opcodes.h` before writing code:

```
#define PARTSIZE      9728000ui64        <- not 9500
#define EMBLOCKSIZE   184320             <- the hash unit
```

**9,500 is the obsolete eDonkey2000 value.** eMule replaced it with
9,728,000-byte parts hashed in **184,320-byte blocks** — 9,500 × 195 would be
1,852,500, which is neither, and 9728000 / 184320 is exactly 52.8, so the
block divides the part.

This invalidates `PartSize = 9500` in step 2's code, and the plan is a living
contract: the constant is corrected here, the code change lands with step 3,
and step 2's committed golden bytes are unaffected (they assert the *layout*,
not the size).

### AND THE HASH IS NOT WHAT THE PLAN SAID

The plan says "MD5". eMule hashes in **MD4**, and the file hash is
`MD4(file_size_le32 || filename)` — not `MD5(contents)`, and not MD5 of that
either.

**Measured against the capture, and it does not reproduce.** For the first
result (`Hw-004.mp4`, 505,365,630 bytes, claimed hash `40d349929c69b373
5a1d5247b6fedde6`):

```
MD4(size_le32 || name)     e59bebc5a171ed85a31e1f09d03b746e
MD5(size_le32 || name)     58f3b1e999ac9e1eb46438f2de216766
MD4(name || size_le32)     52408d5b426f2bf0d23c36547c7c605e
MD4(size_be32 || name)     3a0ac53e18e3b0146c4b97e402603d34
                          none of these is the claim
```

Two readings, and the honest one is that **the server's hash is a CLAIM this
client cannot reproduce from the fields it decoded.** Either the name or the
size it hashed is not exactly what came back in the tags, or this server uses
a variant. Either way it settles the design: **the gate must verify against
the hash the LINK carried, never against one recomputed from the name.** A
recomputed hash would reject every file this server offers, and it would be
right to.

So the API takes the expected hash as an argument:

```go
// internal/ed2k/verify.go -- the VERIFICATION, which was the missing half.
// The hash itself is ehash.go's and is NOT reimplemented here.

func VerifyBytes(want Hash, name string, wantSize int64, got []byte) (Verified, error)
func VerifyPart(want Hash, part int, got []byte) error
```

`VerifyBytes` checks the size **before** hashing, so a size mismatch reports
the size rather than a hash mismatch -- otherwise a truncated transfer is
reported as a corrupted file, which names the wrong cause. `VerifyPart` takes
the expected part hash as an argument because a link carries only the file
hash; a caller with no hash set does not call it, rather than inventing a
value.

### WHY THE SIZE GOES IN AS uint64

`PARTSIZE` is 9,728,000 and eMule's max file size is 2^38, which does not fit
in a uint32. A part hash written with a 32-bit size field is correct for
every file under 4 GB and wrong for every file over it — the same silent
wrongness this package has refused three times now.

**Verify:**

```bash
GOFLAGS=-mod=mod go test ./internal/ed2k/ -run 'TestABlockHashIsItsBytesAndItsIndex' -v
```

Expected: `--- PASS`, against a **golden value from an independent tool**:

```bash
printf 'block zero' | openssl dgst -provider legacy -md4 -r
```

`openssl -provider legacy` is required — **OpenSSL 3 dropped MD4 from the
default provider**, so a plain `openssl dgst -md4` fails and
`hashlib.new("md4")` raises. That is worth knowing before a test "proves"
something with a tool that cannot run the algorithm at all.

And the layout assertion is pinned against the algorithm, not against our own
output: RFC 1320's vectors (`MD4("")` = `31d6cfe0d16ae931b73c59d7e0c089c0`,
`MD4("abc")` = `a448017aaf21d8525fc10ae87aa6729d`) must be reproduced by the
block hash, which is the only way to know the digest is MD4 and not merely
self-consistent.

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

> ### RUN 2026-09-28: THIS STEP FAILED, AND WHAT IT FOUND
>
> **The live test was written and run. It does not pass, and the reason is
> recorded here rather than left as a red build.** Measured against
> `85.17.116.222:6082`:
>
> | measurement | result |
> |---|---|
> | server-supplied handles in a live search (`ubuntu`) | 299 |
> | of the first 25, accepting a TCP connection | **3** |
> | of those 3, completing our `DialSource` ed2k handshake | **0** |
> | small files (≤ one part) from 7 keyword searches | 26 |
> | of those, any source answering a part request | **0** |
>
> **The plan's own wording was wrong.** It said "dial its `UserID`/`Port` as a
> source" as if dialling and reaching a source were one step. They are not: a
> handle that accepts a TCP connection is reasonably common, and a handle that
> completes an ed2k source conversation was zero of three. The gap between the
> two is the finding, and no amount of retrying hides it — a peer that ignores
> our handshake will ignore the next one too.
>
> **What this establishes, and it is worth stating plainly:** the source-side
> opcodes in `part.go` remain *correct as cited and unconfirmed in use*. §9's
> caveat holds; this run is what turned it from an assumption into a
> measurement.
>
> **The honest consequence is that Kad source lookup is now blocking, not
> deferred.** A server's `UserID`/`Port` is a *hint* that a source exists
> there, and on today's network that hint is right about TCP reachability and
> wrong about ed2k. Kad (`OP_KAD2_SEARCH_SOURCE_REQ`, `OP_START_TRANSFER`) is
> the protocol that resolves a file hash to peers directly, and it is the only
> remaining path. It was listed in §8 as unsettled; the evidence here promotes
> it to the next milestone.

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

> **And the measured result was neither, on 2026-09-28: a named failure at
> the handshake, not at the part request.** The test's job is to distinguish
> those — `ErrRefused` (the peer never spoke) from an unrecognised opcode (it
> spoke something else) from a short answer (it spoke and lied). It reported
> `ErrRefused` on all three reachable handles, which is the first of the three
> and the one that points at Kad.

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

## 9. PROVENANCE — where every constant in this plan came from

Kept here rather than in a separate file, because a value and its source
belong in the same place and separating them is how a value loses its source.

**Two classes of constant, and the difference is the whole point.**

**Observed here.** Every server-side constant — the login, the extended
hello, the search request and result, the Kad bootstrap — was confirmed
against a real server at `85.17.116.222:6082` before it was written down.
The durable form is `internal/ed2kwire/testdata/searchresult_live.bin`, a
real `OP_SEARCHRESULT` frame, and the tests decode that capture rather than
bytes this package produced. The opcodes in `server.go` carry the captured
byte sequences in their comments for the same reason.

**Cited, not observed.** The source-side opcodes in step 2 are quoted from
eMule's own `opcodes.h` (`irwir/eMule`, the upstream C++ client), with its
layout comments intact:

| Constant | Value | eMule's comment |
|---|---|---|
| `OP_SENDINGPART` | `0x46` | `<HASH 16><von 4><bis 4><Daten len:(von-bis)>` |
| `OP_REQUESTPARTS` | `0x47` | `<HASH 16><von[3] 4*3><bis[3] 4*3>` |
| `OP_FILEREQANSNOFIL` | `0x48` | `<HASH 16>` |
| `OP_COMPRESSEDPART` | `0x40` | `<HASH 16><von 4><size 4><Daten len:size>` |

**How to re-verify:**

```bash
curl -s https://raw.githubusercontent.com/irwir/eMule/master/opcodes.h \
  | grep -E 'OP_(SENDINGPART|REQUESTPARTS|FILEREQANSNOFIL|COMPRESSEDPART) '
```

**Why citing beats inventing, and what it caught.** The first draft of step 2
used `0xD4` and `0xD5`, both plausible and both wrong — `0xD4` is
`OP_PACKEDPROT`, the zlib protocol byte this package already reads in
`DecodeSearchResult`. A value that reads like a real constant is more
dangerous than an obvious placeholder, because nothing about it invites a
second look. That one is the argument for citing.

**What citing does NOT establish.** That a real source will accept this
client's handshake and then answer. Constants are static; behaviour is not,
and a peer may also speak a newer dialect. That is what step 5 is for, and
until it passes, every source-side constant here is *correct as cited* and
*unconfirmed in use*.
