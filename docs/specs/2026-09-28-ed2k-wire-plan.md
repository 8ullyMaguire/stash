# Step 5.4 wire plan — the ed2k server connection and Kad bootstrap

Written 2026-09-28, after the plan's claim that "no Go library provides this"
turned out to be false. The library is `github.com/monkeyWie/goed2k` (MIT);
`docs/specs/2026-09-27-stashforge-plan.md` §Step 5.4 now records the
evaluation table and the measured hashing bug that keeps the library OUT of the
hash path.

**This plan is the contract.** An LLM should be able to implement the whole of
it from this file alone: exact paths, the exact code, and per step the exact
command plus its expected output.

---

## 0. The split, and why it is not negotiable

| Concern | Owner | Never |
|---|---|---|
| The eHash: part boundary, 8-byte truncation, MD4 | **`internal/ed2k` (ours)** | — |
| Locator grammar, traversal refusal, name checks | **`internal/ed2k` (ours)** | — |
| Wire framing, opcodes, packet encode/decode | **`goed2k`** | re-derived by hand |
| The eMule extended handshake | **`goed2k`** | re-derived by hand |
| Kad message set, `nodes.dat` | **`goed2k`** | re-derived by hand |
| Our client: policy, consent, rate limits, the library hand-off | **plugin** | in the library |

`goed2k`'s `HashFromHashSet` is **wrong** — it MD4s full 16-byte part hashes
where the protocol requires the first 8 bytes of each. Measured:

```
goed2k.HashFromHashSet  ->  90955B3AFD7D14B68B672C584F88DD93   (WRONG)
internal/ed2k.HashFile  ->  735E6A43667B72334F8E27F9C46D263B   (correct)
```

**Rule for every reviewer:** no file in `internal/ed2k/` may **import** the
library. The hash is ours. If one ever does, the plugin can hash a file to a
value matching nothing on the network, and no unit test will notice, because
the library's tree hash agrees with itself.

**And the check has to match the rule, which is a correction.** The first
version of this line was `grep -rn "goed2k" internal/ed2k/` and the rule was
"must return nothing". It returned two lines — in a **comment** in `ehash.go`
recording the measured counterexample that justifies the whole rule. So the
check fired on its own evidence, and a reviewer running it got a failure that
meant nothing: the two `//` lines document the rejection, and a third
comment line is where a future reader would look for the reasoning.

Deleting the evidence to satisfy the check would be the wrong trade, so the
check changed to what it was always for — an import is what puts the library
in the build:

```sh
# an IMPORT is the violation; a comment is the documentation of it
grep -rn 'goed2k' --include='*.go' internal/ed2k/ \
  | grep -v '^\s*//' | grep -v ':[0-9]*:\s*//'   # -> empty
```

A blunt grep that is kept green by deleting the reasoning is worse than a
precise one that is red: the first trains a reviewer to ignore it.

---

## 1. Step 5.4a — the server connection

New package: `internal/ed2kwire/`. It is a NEW package, deliberately separate
from `internal/ed2k`, so the rule in §0 is enforced by the import graph rather
than by discipline.

**Files**

| Path | Contents |
|---|---|
| `internal/ed2kwire/server.go` | `Server`, `Dial`, `Hello`, `ServerList` |
| `internal/ed2kwire/server_test.go` | the tests below |
| `internal/ed2kwire/hello.go` | the hello/hello-answer exchange, opcodes |
| `internal/ed2kwire/mutate_ed2kwire.py` | the harness |

**The wire interface, from `goed2k`.** Every packet implements:

```go
type Serializable interface {
	Get(src *bytes.Reader) error
	Put(dst *bytes.Buffer) error
	BytesCount() int
}
```

`PacketHeader` is `{Protocol byte; Size int32; Packet byte}`, little-endian,
`PacketHeaderSize = 6`, and `SizePacket() = Size - 1` (the count EXCLUDES the
opcode byte). `Protocol` is one of `EdonkeyHeader 0xE3`, `EMuleProt 0xC5`,
`KademliaHeader 0xE4`. **The `- 1` is a live hazard**: framing with `Size`
instead of `SizePacket()` desynchronises the stream and every later read fails
with a plausible error, so it is asserted in a test rather than trusted.

**Step 1.1 — add the dependency.**

```bash
cd ~/code-local/go/stash/plugins/p2pdownloader
GOFLAGS=-mod=mod go get github.com/monkeyWie/goed2k@v0.0.0-20260602122456-f2a71d599dee
```

**Verify:** `grep -n goed2k go.mod` shows it as a **direct** require. If it says
`// indirect`, nothing imports it yet — that is the bug to fix, not a reason to
move on.

**Step 1.2 — `server.go`.** The shape:

```go
// Server is one ed2k server we are connected to.
type Server struct {
	addr     string
	conn     net.Conn
	clientID uint32
	port     uint16
	users    int32
	files    int32
}

// Dial connects to an ed2k server, reads its HELLO and sends ours.
// A server speaks first: it sends HELLO, we answer with HELLO. Failing that
// the connection is refused BEFORE any packet is sent, because a peer that
// will not identify itself is not a server.
func Dial(ctx context.Context, addr string) (*Server, error)
```

`Dial` must: (a) honour `ctx` on the TCP connect **and** on the first read;
(b) read a full `PacketHeader` then exactly `Size` payload bytes — a partial
read is a protocol error, not a retry, because the stream is now desynced;
(c) refuse a `Protocol` byte that is not `0xE3`/`0xC5`, naming the byte it got.

**Step 1.3 — the tests.** Each names the behaviour, not the code:

- `TestDialRefusesAConnectionThatIsNotAnED2KServer` — a loopback listener that
  accepts and writes `GET / HTTP/1.1` instead of a HELLO. Must be refused, and
  the error must name the protocol byte it read. (This is the "a plugin that
  starts cleanly and then does nothing" shape.)
- `TestDialRefusesAServerThatHangsBeforeSayingHello` — a listener that accepts
  and never writes. Must fail on the context deadline, **not** hang. Assert the
  elapsed time is bounded.
- `TestAPacketIsReadWholeOrRefused` — a server that writes a header promising
  200 bytes then writes 20 and closes. Must refuse, and must not return a
  `Server` with a 20-byte hello.
- `TestTheHelloWeSendIsTheHelloTheProtocolDescribes` — golden bytes, both
  directions. This is the test that catches a `Size` vs `SizePacket()` bug: the
  recorded bytes are the protocol's, not the encoder's own output.

**Step 1.4 — the harness.** Copy the shape of
`internal/ed2k/mutate_ed2k.py`: four verdicts (`KILLED` / `COVERED` /
`SURVIVED` / `SKIP`), per-probe restore, a **repo lock** (`fcntl.flock` on
`.mutation-harness.lock` — `mutate_rpc.py` and `mutate_seam.py` corrupt each
other without it), and separate exit codes. Rows: the `- 1` in `SizePacket`,
the little-endian read, the partial-read refusal, the context on the read, the
protocol-byte check, and a `file://`-shaped "unknown protocol accepted" row.

**Verify per step:** `python3 internal/ed2kwire/mutate_ed2kwire.py` ends
`PYEXIT=0` with `0 survived`.

---

## 2. Step 5.4b — Kad bootstrap

New file: `internal/ed2kwire/kad.go`.

- `ParseNodesDat([]byte) ([]Node, error)` — the first byte is the node count,
  then 6 + 4 bytes per node (IPv4 + port). A count that overruns the buffer is
  refused with the count it read: an attacker-chosen count is the whole input.
- `KadClient` with `Bootstrap(ctx, node)`, carrying our Kad ID
  (`NewID(protocol.Hash)`), the 0xE4 header, and `Hello`/`HelloResAck`.
- The Kad node ID is the ed2k hash the client would publish, so
  `kad.NewID(ourClientHash)` — **not** a random ID. A wrong ID is discoverable
  only on the live network, which is why the live tests in §4 exist.

**Tests:** a `nodes.dat` whose count field overruns the buffer; a zero count;
a truncated final node; and a round-trip where our encoded `Hello` decodes in
the library's own decoder and the field values survive.

---

## 3. Step 5.4c — the extended handshake  ✅ implemented

New file: `internal/ed2kwire/exthello.go`. The eMule extended hello carries a
**zlib-compressed** `TagList` of the client's capabilities. Two facts to get
right:

- The tag list is compressed, so a decode error is a real error and must not be
  swallowed into "an ordinary hello".
- The compression is zlib, **not** raw deflate — a `zlib.NewReader` that
  silently accepts the wrong stream is the failure mode.

**Tests:** golden compressed bytes; a payload that is not valid zlib refused
by name; a tag list that decompresses but has a truncated final tag refused;
and a round-trip through the library's own `TagList` reader.

### What the implementation found

All four planned tests exist, plus four the plan did not ask for, because each
of the four bugs below passed a test written to the plan.

**The count is a `uint32`, not a byte.** `parseTagList` reads four bytes; the
first writer emitted one. A two-tag list read back as 100,762,114 tags, and
`parseTagList`'s bound reported a *corrupt tag list* — a loud failure that
named the reader, which was right, and never the writer, which was not. A
defensive check downstream turned a writer bug into a reader-shaped error.

**A `tagTypeString` carries its own `uint16` length.** Every other type's
width is implied by the type byte, so writing the value bare is correct for
all of them. This one is the exception, and the symptom was a round trip
failing with "claims a 12406-byte string" — `0x306E`, the first two bytes of
`v0.60a`. A confident, specific, wrong number.

**A round trip cannot see a missing high bit.** `parseTag` does
`Type: payload[0] & 0x7F`, so this package's reader is indifferent to the bit
and a round trip passes either way. A real server is not: the bit marks a
name-carrying tag, and the library's own reader refuses a tag without it.
Asserted at the byte level instead.

**Three of the spec's own precautions turned out to be redundant.** The plan
asked for a zlib-vs-deflate check; `zlib.NewReader` already validates the
header, and the function already wraps its error in `ErrNotZlib`, so explicit
checks for the compression nibble, the multiple-of-31 rule and a two-byte
length guard were all *survived by mutation* and all removed. The length guard
looked load-bearing — a panic with it gone — but re-running the probe showed
the reader refusing the same payload first. **The panic was real and was not
reachable through this package's entry point.**

**Two bounds that are not redundant, and are probed:** the inflated size
(`io.LimitReader` at the limit, plus a post-read check, because a check after
inflating has already allocated the bomb) and the tag count.

**What is still unconfirmed: the wire shape.** The four framing variants
tried against `85.17.116.222:6082` all produced identical silence, and a raw
socket reads nothing from that server either — so an accepted-then-silent
connection cannot distinguish "our framing is wrong" from "this server
ignores extended hellos". The codec is verified hermetically; the framing
needs a reference-client capture or a server that answers.

## 4. Verification — hermetic by default, live by opt-in

**The rule:** `go test ./...` must never touch the network. A suite that
depends on a third party's uptime is not a test, and one that *skips* into a
green run proves nothing — so a live test that skips must **fail**, exactly like
`commons`'s deliberate `PANIC` at `harness/mod.rs:269` and `whitebois`'s silent
skip on an unset `DATABASE_URL` in my memory notes.

Live interop lives in `internal/ed2kwire/live_test.go` behind:

```go
//go:build ed2klive

// Live interop is opt-in: run it with
//   go test -tags ed2klive ./internal/ed2kwire/
// The build tag is the point — a live test must not be reachable by a plain
// `go test ./...`, or the suite's green would depend on someone else's uptime.
```

Live assertions, in order of value: connect to a public server and complete the
hello exchange; bootstrap Kad from a `nodes.dat` we fetched; confirm our hello
is **accepted** rather than ignored. A wrong opcode shows up here as a server
that never answers, which no amount of reading the spec would have told us.

**Verify:** `go test ./... -count=1` (all packages, no network) and
`go test -tags ed2klive ./internal/ed2kwire/ -v` separately, with its own
reported result.

---

## 5. Done means

```
go build ./...                                  clean
go vet ./...                                    clean
gofmt -l internal/ed2kwire/                     clean
go test ./... -count=1                          green, no network
grep -rn goed2k --include='*.go' internal/ed2k/ | grep -v ':[0-9]*:\s*//'
                                                    EMPTY  <- the rule in §0
python3 internal/ed2kwire/mutate_ed2kwire.py    0 survived, PYEXIT=0
go test -tags ed2klive ./internal/ed2kwire/     reported honestly, including failure
```

And the plugin's advertisement stays honest: a granted, well-formed ed2k link
still ends in `ErrTransferNotImplemented` until there is a transfer, and
`TestTheDownloadStubStillReportsTheTransferIsUnimplemented` says so.
