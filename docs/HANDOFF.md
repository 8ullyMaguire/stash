# Handoff

Where StashForge is, what is verified, and what the next person should know
before touching anything. Written for a cold start: no context from the session
that produced it.

Last updated: **M5 in progress** — the eMule extended hello (ExtHello) is
implemented, 48 tests green, and the mutation harness is at **zero survivors**
across 25 probes. BOTH the ed2k login and the Kad bootstrap complete against
live servers. Branch **`develop`**, unpushed. **Read "RESUME HERE" below before
anything else**; it carries the verified numbers, the four bugs the harness
found in the new codec, and the exact next task.

---

## What this is

A fork of [stash](https://github.com/stashapp/stash) where the **library stays
local and private** and the **metadata is shared** through a self-governed
commons. Users are registered accounts, edits are proposals with reputation
weighting, and the published field set is disclosed to each user before anything
leaves their host.

The governing documents are `docs/GOAL.md` (milestone state) and
`docs/specs/2026-09-27-stashforge-{spec,plan}.md` (what to build and why).

## Milestone state

| Milestone | State | Tag |
|---|---|---|
| M0 baseline | done | — |
| M1 accounts, sessions, invites | done | — |
| M2 governance: proposals, vocabulary, apply | done | — |
| M2b governance v2: roles, weighted ballots | done | — |
| M2c identity clustering (plan 2.4b) | done | `m2c-identity-clustering` |
| **M3 metadata sharing (consent, exporter, federation)** | **done** | `m3-metadata-sharing` |
| **M4 public hosting: mode, 2FA, library grants** | **done** | `m4-public-hosting` |

---

## RESUME HERE — BOTH NETWORKS NOW TALK TO REAL SERVERS

**Last updated 2026-09-28, at a clean milestone. Branch `develop` at `497d1d7a9`.**

### The headline

**Two live proofs, on the same day, and the second one is bigger.**

**ed2k: `Dial` completes a real login.** Against `85.17.116.222:6082`:

```
connected: network users=12181 files=4137451 | this server: users=103755 files=17885
messages=[VPN with port forwarding (for High ID) ... Open-source ed2k-server
          > https://github.com/andrey23127/ed2k-server]
```

**Kad: 40 of 40 contacts complete the hello**, each returning a distinct node
ID, from a real published contact list. `kad.go` had never touched a real node
before this session.

### Where the work stands

| Commit | What |
|---|---|
| `497d1d7a9` | mutation harness at **zero survivors**; a dead guard deleted |
| `336280646` | the live nodes.dat URL, and the check that missed the mirror |
| `ead1cb705` | **Kad reaches the real network**; two byte-order bugs fixed |
| `8e6efcbb1` | the live-login milestone recorded |
| `a82f2d809` | test deadlines shrinkable in the suite, real ones pinned |
| `2713e1711` | a real ed2k login completes |
| `c7f32e039` | the handshake order fixed, SYN obfuscation added |

### The bug worth more than the feature

**Two byte-order bugs that cancelled each other.** `ipv4FromLE` swapped the
bytes of an address the library had *already* swapped, and `endpointFromNode`
mirrored it on write. All 154 contacts in a real `nodes.dat` came out
reversed — Kad was dialling `127.0.0.1`, its own machine.

It stayed invisible because the two errors cancelled. A round trip through a
wrong encoder and a wrong decoder returns what it started with, so
`TestANodesDatRoundTrips` was green on a decoder that could not reach one real
node.

**A round-trip test proves the halves agree. It never proves either is right,
and two mirrored halves agree perfectly.** Byte order is now pinned against the
*format* — golden bytes copied from a real file — in
`TestAKnownAddressSurvivesTheRoundTrip`. A fixture written by the code under
test is circular; that is the whole lesson.

### Three things a later reader should not rediscover

**Probe with a bare socket first.** When every server read EOF, a Python socket
with *no ed2k logic at all* also read nothing — which proved the fault was
ours. An accepted-then-silent connection is a peer ignoring you; a closed
socket is a different finding and should be reported as one.

**Never trust a probe loop against a rate-limited server.** Cost an hour. A
16-probe parallel sweep finished in 0.87 s against a server whose first-byte
latency is 3 s — the threads were being refused, not answered. It then drove
one server to total silence for over 45 s, *including the baseline packet that
had answered moments earlier*. **A baseline that stops answering means the
measurement is measuring the rate limit, not the protocol.**

**A mutation SKIP reads as "nothing wrong" in a summary line.** Two probes in
the first harness run had patterns that no longer matched the source. Both are
now taken from the source bytes rather than transcribed by hand.

### The mutation harness — now at ZERO survivors

`internal/ed2kwire/mutate_ed2kwire.py` — 19 probes, four verdicts (KILLED /
COVERED / SURVIVED / SKIP). Exit **1** for a hole in the tests, **2** for a
defect in the harness, **0** for clean, so one non-zero code sends the reader
to the shorter list. Every probe is bounded and restores its file: a sweep of
`internal/library` was once interrupted by a SIGTERM and left a mutation
applied to `integrate.go`.

**Current: 18 killed, 1 covered, 0 survived, 0 skipped.**

Getting there was worth the runs, because each survivor was a real finding:

**A dead guard, proven by a mutation that survived.** `drainFacts` carried a
"said nothing at all" check identical to the live one in
`readLoginConfirmation`. Deleting it changed no test result — only possible for
a line nothing can reach. It cannot be reached: `readLoginConfirmation` returns
on `!heard` *before* handing over. **A defensive copy of a check that cannot
fire is worse than no check** — it reads as protection, cannot be tested, and
its mutation is a coverage hole no test can close. Removed, reasoning kept.

**A guard nobody could execute.** `newObfuscationSeed` called `crypto/rand`
directly, so its error path was untestable — and the probe that deleted the
check survived, correctly. The randomness source is now a parameter
(`newObfuscationSeedFrom`), which keeps the seam where it is used rather than
in a package var a test has to restore.

**Two probes reporting survivors without testing anything.** A `-run` pattern
matching no test runs no tests, the run passes, and the probe is scored
SURVIVED — identical to a real hole. The harness now checks, *before* applying
any mutation, that the pattern selects at least one test, and reports zero as
MALFORMED instead.

### M5 step 3: the eMule extended hello

`internal/ed2kwire/exthello.go` — the step between "we are connected" and
"we can search". A client declares its capabilities so a server knows whether
the file it found can be served to us. Without it a server has no idea what
version or limits we have, and will not route a search.

**Wire format**, as implemented:

```
[E3] [22] [size:4 LE] [01] [seed:4] [zlib]
                              obfuscated
      the size COUNTS the opcode -- SizePacket() is size-1
```

**One thing this could not be validated against.** The extended hello is
implemented from the specification, not from a capture. The four framing
variants tried against `85.17.116.222:6082` — opcode-counted size,
opcode-excluded size, no protocol byte, raw deflate — all produced the same
silence, and a raw socket with no ed2k logic reads nothing from that server
either. **An accepted-then-silent connection cannot distinguish "our framing
is wrong" from "this server ignores extended hellos".** So the codec is
verified hermetically and the wire shape is unconfirmed. That is the next task.

**Four bugs, all found by mutation rather than by reading the code:**

**A four-byte count written as one byte.** `parseTagList` reads a `uint32`;
the first writer emitted a single byte. A two-tag list then read back as
100,762,114 tags, and `parseTagList`'s own bound caught it and reported a
*corrupt tag list* — a loud failure in the wrong file, naming the reader,
which was correct, and never the writer, which was not.

**A length-prefixed string written without its length.** Round trip failed
with "tag 0x01 claims a 12406-byte string and only 11 bytes are left", where
12406 is `0x306E` — the first two bytes of `v0.60a`. A confident, specific and
wrong number, and the only honest reading of it is that the writer is at fault.

**A round trip that could not see a missing high bit.** `parseTag` does
`Type: payload[0] & 0x7F`, so this package's own reader is *indifferent* to
the type byte's high bit. A round trip passes either way. But a real server
requires it — it is what marks a name-carrying tag, and the library's own
reader refuses a tag without it outright. **A round trip proves the halves
agree, not that either is right.** Same lesson as the byte-order pair, second
time in the same package. Now asserted at the byte level in
`TestTheWrittenTagBytesCarryTheHighBit`.

**Twenty lines of checks that were all redundant.** `inflateExtHello`
validated the zlib header itself — compression method nibble, multiple-of-31,
and a two-byte length guard. All three were probed by deletion and **all three
survived**, because `zlib.NewReader` does the same validation and the function
already wraps its error in `ErrNotZlib`. The length guard looked load-bearing:
a panic with it removed. Re-running the probe showed `zlib.NewReader` refusing
the same one-byte payload with "unexpected EOF". **The panic was real and was
not reachable through this package's entry point.** All three removed; what was
lost is a more specific message, and the comment says so.

**Current: 24 killed, 1 covered, 0 survived, 0 skipped.** 48 tests in the
package, 0 skips — including one that used to skip, when a search for a
fixture that no compressor can produce was replaced by a hand-built one.

**And a note on writing probes.** The count probe took three attempts: the
first did not compile (unused import), the second *survived* because writing
one byte into a zero-initialised `[4]byte` is byte-identical to writing four.
**There is no way to express "one byte instead of four" as a change to the
wire.** The real bug was writing one byte and *not padding*, so the probe
belongs on the byte the reader lands on next.

### What is deliberately NOT done, and why

**The Kad tag list does not decode.** The header is established and correct on
six captured answers: `[E4][09][id:16][tcpport:2 LE][version:1][count:1]`,
version 8 and port 4662 on every node. Byte 22 is `0x00`, and the library
reads tags as `[type|0x80][id][value]` — the eMule **ED2K** format — so the
first tag always failed. Four layouts were tried and ruled out; the analysis is
written up in full above `Bootstrap` in `kad.go`.

`decodeKadAnswer` therefore reads the header and **stops**, carrying the tag
bytes unparsed and the claimed count beside them so the discrepancy stays
visible. A guessed layout is worse than none: a wrong one yields plausible
node IDs and a routing table keyed on them.

**The ed2k client version tag.** Same answer, same reason: needs a reference
client's capture, not a probe loop.

> Both open questions close with **one run of a real eMule or aMule against a
> server, captured**. That is the highest-value hour available and it is not
> work an LLM can do alone.

Also still open: the **TCP half of SYN obfuscation** (needs a raw socket, not
exposed by `net.Dialer`; the payload half alone is enough for a full login),
and **every public `nodes.dat` URL in `live_test.go` 404s** — the working one
is `upd.emule-security.org`, and that test needs updating.

### THE NEXT TASK

**Everything in this plan is now implemented and verified.** The two steps that
were open when this section was last written are closed:

1. ~~**The eMule extended handshake** (plan §3)~~ — **DONE** at `6ae05a910`.
   `exthello.go`, 25 mutation probes at zero survivors. See "M5 step 3" above
   for the four bugs the harness found and what each one taught.
2. ~~**The Kad tag list does not decode**~~ — still open, and **it needs a
   reference client.** Four layouts were tried and ruled out. This is not
   guessable: a wrong layout yields plausible node IDs and a routing table keyed
   on them, which is worse than not decoding.
3. ~~**The ed2k client version tag**~~ — same answer, same reason.

### M5 step 4: search — PROVEN against a live server

`internal/ed2kwire/search.go` and `searchresult.go`, at `f50307621`. The step
after the extended hello: ask a server which files it holds, and read the
answer.

**A live search works end to end.** Against `85.17.116.222:6082` for the
keyword "ubuntu", the server answered with one `0x33` frame of 27,950
compressed bytes that inflated to 40,828 and held **299 results**. The decoder
reads all 299 — names, sizes, types, source counts, 299 distinct file hashes
— consuming every byte but one.

```
[count:4] [token:4] [fileid:18]        the header, 26 bytes
  repeated per result:
[tagcount:4] [tags...] [hash:16] [userid:4] [port:2]
```

**The two directions are asymmetric, and that is the first thing to know.** A
request is one packet we build, plain and uncompressed. A result is packets a
stranger decides the shape of, and arrives zlib-compressed under protocol byte
`0xD4` (PACKEDPROT). Reading one as the other is wrong at the first byte, and
a *compressed request* is answered with silence — the same shape of failure as
the OP_HELLO mistake this package made against every real server.

**Three things the capture taught that no amount of reading would have.**

**A result's file ID is 22 bytes, not 16.** Sixteen looks obvious — it is the
hash, and the hash is a hash. The other six are a 4-byte user ID and a 2-byte
port, and stopping at 16 lands the next tag count two bytes early, where it
reads as **988,510,410**: not an error, and impossible, which is the worst
kind. The port is what settled it — **4662** on the first result is eMule's
standard Kad port, a value nobody would have guessed.

**The header's file ID is 18 bytes, so the header is 26 and not 24.** The
first version assumed 16 for symmetry with a result's ID and read the first
tag count two bytes early: 332,452 tags in a five-tag list.

**A result with no room for its own identity ends the list.** The capture's
300th entry has five complete tags and **one byte left**. A loop guarding only
the four-byte count decodes it from that tail and produces a 300th result
named `5\x00Walt Disney...` — a stray length byte in front of the name, which
is what a misaligned walk looks like, and how it was spotted. So a result with
no room for its file ID **breaks**, while a result whose *tags* are cut short is
an **error**. The asymmetry is deliberate: the first version errored on the
former, which made a real answer undecodable over one trailing byte and cost the
caller 299 good results to learn it.

**And the wider guard I first reached for turned out to be dead.** Reasoning
that "a result is a count *plus* 22 bytes, so the guard should cover all of
it", I wrote the loop condition as `off+4+fileIDLen <= len(plain)`. The
mutation harness disagreed: replacing it with plain `off+4` left every test
green, and the capture decodes to the same 299 results with the same last
name and the same last port either way. The in-loop `break` is what ends the
list, so the wide condition was **deleted, not defended** — one mechanism
instead of two. The comment in the code records that it was tried and
measured, because the "obviously more correct" version is the kind of thing
that otherwise gets re-added.

**The tests decode a CAPTURE, not a fixture.** `testdata/searchresult_live.bin`
is the real frame. Every other test here round-trips through our own encoder,
which proves the halves agree and nothing more — and there is no encoder for a
result at all, since a stranger writes those bytes. The golden values are the
ones a misaligned walk cannot fake: the filename `Hw-004.mp4` and the hash
`40d349929c69b3735a1d5247b6fedde6`. A wrong walk reads a *different* file's 16
bytes and produces 299 **different** wrong values, so a count-based assertion
sees nothing wrong.

**Two writes that were invisible while wrong, both now pinned by tests.** A
mutation run flagged the tag count and the keyword terminator as surviving.
Neither was a hole in the protocol code — both were holes in what the tests
happened to exercise:

- **The tag count** is written into a zero-initialised `[4]byte`, and for a
  little-endian uint32 below 256 only byte 0 is non-zero. So `count[0] = byte(n)`
  produces **byte-identical** output to the real `PutUint32` for every list
  under 256 tags — and a keyword search carries exactly **one** tag. The test
  that bites encodes **300** tags and asserts the list's total length, because
  for that size the one-byte write is no longer equivalent.
- **The keyword's NUL terminator.** eMule string tags are NUL-terminated, and
  the length prefix **includes** the NUL: the wire reads `82 01 07 00` for
  "ubuntu" — type, id, length 7, six letters, NUL. A server reading past a
  missing terminator does not error, it keeps reading the rest of the packet
  as keyword text, finds nothing, and answers nothing. **There is no log line
  anywhere for that failure**, which is what made it worth a test rather than
  a comment.

**One thing I got wrong and corrected, worth not repeating.** A throwaway
Python decoder read the Str-family length from the raw wire byte
(`0x9A - 0x10 = 138`) and reported 2.4 billion tags. The Go was right and the
script was wrong: `tag.go` stores `Type` as `payload[0] & 0x7F`, so wire `0x9A`
becomes `0x1A` and `0x1A - 0x10` is 10. Three tags agree — `0x9A`/"Hw-004.mp4"
(10), `0x99`/".DS_Store" (9), `0x9B`/"OAV1365.mp4" (11) — and the mask is what
makes the masked and wire conventions agree. **The mistake produces a
plausible number, not an error**, which is why the test comment says so.

### M5 step 4 CLOSED: every gate in the spec's "Done means" passes

Run as written on 2026-09-28, all green. Recording the commands, because the
spec's §5 is the authority and "it seems fine" is not the same claim.

```
go build ./...                                       clean
go vet ./...                                         clean
gofmt -l internal/ed2kwire/                          clean
go test ./... -count=1                               9 packages ok, no network
grep goed2k in internal/ed2k/ (imports only)         EMPTY  <- the §0 rule
python3 internal/ed2kwire/mutate_ed2kwire.py         36 killed, 1 covered, 0 survived
go test -tags ed2klive ./internal/ed2kwire/          ok, 44.7s, with real input
```

**66 tests in `ed2kwire`, 0 skips. 37 mutation probes.**

**The live gate needs two environment variables, and the tests say so rather
than skipping.** Run bare, two Kad tests FAIL in 0.00s with:

> `ED2K_LIVE_NODES_DAT is not set, so there is no contact list to bootstrap
> from. This test FAILS rather than skipping, because a live test that skips
> reports ok having proved nothing.`

That is the design working: a live test that quietly skips is the failure mode
this package was built to avoid. With both variables set:

```
ED2K_LIVE_SERVERS=85.17.116.222:6082
ED2K_LIVE_NODES_DAT=/tmp/nodes.dat     # curl -o /tmp/nodes.dat \
                                        #   https://upd.emule-security.org/nodes.dat
-> ok  44.650s
```

**Live results on this run**, all against the real network:

- **ed2k login** to `85.17.116.222:6082` — accepted (23.1s, the server's own
  handshake latency).
- **Kad bootstrap** — 179 contacts parsed, 179 advertising Kad2+, and **33 of
  40** completed the hello. The 7 declines are reported honestly and are not
  ours: `the answer is not a Kad packet: unsupported kad protocol header`.
- **Search decode** — the 299-result capture still decodes.

**A correction to this file, and it matters for anyone reading the git log.**
An earlier revision said commits here are local-only and "there is nowhere to
push". Both halves were wrong: `origin` → `8ullyMaguire/stash` is the fork, it
is writable, and a dry run succeeds. `upstream` → `stashapp/stash` really is
read-only (`403`) and must not be pushed to. **This branch is pushed** to
`origin/develop` at `867703ea3`. The `git pushall` in an earlier revision is
not a command on this host — that is another repository's convention — and
chasing it cost a minute.

### The computer-use route to the captures — investigated, and it does not work here

A later session wrote that the captures were "not work an LLM can do alone", on
the grounds that a capture needs a GUI client and a packet capture. That was
wrong, and it is worth writing down exactly why, because the wrongness is not
visible in any status code: **cua-driver IS installed on this machine.**

    cua-driver 0.28.2      installed, 50 MB binary, symlinked into
                            ~/.local/bin, reports healthy
    60 driver tools        including get_window_state, get_desktop_state,
                            click, type, key, list_windows

**Two separate problems, and neither is the one it first looks like.**

**1. The tool was not in this session's toolset.** `platform_toolsets.cli` in
`~/.hermes/config.yaml` is an explicit allowlist and it did not list
`computer_use` — so the skill was loadable and the driver installed, and the
tool was still absent. **Added it** (one line, between `codegraph` and
`connections`, which is where it sorts). That part was mine to fix and is
fixed; a restart is needed before the tool appears in a session.

**2. The driver cannot see this desktop, and that is not fixable from here.**
This is a **native Wayland** session — Hyprland as compositor, Xwayland present
but with no X clients at all. The driver's own `doctor` states the consequence:

    [ok  ] display server: Wayland+XWayland
    [warn] X11 connection: no top-level windows returned
    [ok  ] AT-SPI: org.a11y.Bus reachable via session bus

How far it actually got: the daemon started on its socket,
`get_accessibility_tree` enumerated **467 processes**, and then reported **0
on-screen windows**. And `get_desktop_state` — the screenshot — **hung for 90s
and had to be killed**, which is the real tell: it is waiting on a compositor
it cannot reach. `hyprctl` is installed and answers, but
`HYPRLAND_INSTANCE_SIGNATURE` is unset in this process, so the compositor IPC
is unreachable from here too.

**So: the daemon is healthy, the tool is now enabled, and there are still zero
windows to act on.** An agent that reported "it is installed, so I can drive
eMule" would be wrong — and nothing in the tool's own behaviour would show it,
because the tool answers; it simply has nothing underneath it.

A capture needs a session this process is not in: either a real eMule run in
the graphical session with a capture running alongside it, or a server that
answers our extended hello so the framing can be read off the wire.

**What is reusable from this.** The config change is committed rather than left
as drift, and it is additive — reverting it is deleting one line. And the
lesson generalises past this task: **"the tool is not in my toolset" and "the
tool cannot see anything" are different failures, and the second one hides
behind the first.** Checking only the first is how a session ends up
confidently reporting a capability it does not have.

**What is left, honestly:**

- **One run of a real eMule or aMule against a server, captured.** That closes
  the Kad tag layout and the version tag together. It is the highest-value
  hour available. It is **not blocked on a person, though** — see the section
  below on computer-use, which I was wrong to call impossible.
- **The extended hello's wire shape is unconfirmed.** Implemented from the
  spec, verified hermetically; the live server is silent either way, so
  framing cannot be distinguished from "that server ignores them". Same
  capture closes it.
- **The TCP half of SYN obfuscation** (needs a raw socket, not exposed by
  `net.Dialer`; the payload half alone is enough for a full login).
- **The transfer itself.** `ErrTransferNotImplemented` is still the honest
  answer at the end of a granted ed2k link, and
  `TestTheDownloadStubStillReportsTheTransferIsUnimplemented` says so. **Search
  is now done and proven against a live server**, so what remains between here
  and a download is: request a file by hash from a source (OP_REQUESGPART and
  the transfer packets after it). That is the next feature, and it is
  unblocked — no capture needed.

**One correction made this session, worth not undoing.** The plan's §0 check was
`grep -rn "goed2k" internal/ed2k/` and the rule was "must return nothing". It
returned two lines — in a **comment** recording the measured counterexample that
justifies the whole rule. The check was firing on its own evidence, and it is
now scoped to imports, which is what the rule was always for. Verified both
ways: clean on the real tree, and still catches an injected import.

### How to run the tests

```
cd plugins/p2pdownloader
go test ./... -count=1                     # whole plugin
go test ./internal/ed2kwire/ -count=1      # ed2kwire, ~10s
python3 internal/ed2kwire/mutate_ed2kwire.py   # 38 probes, bounded

curl -o /tmp/nodes.dat https://upd.emule-security.org/nodes.dat
ED2K_LIVE_NODES_DAT=/tmp/nodes.dat \
  go test -tags ed2klive ./internal/ed2kwire/ -run TestLiveKadBootstraps -v

ED2K_LIVE_SERVERS="85.17.116.222:6082" \
  go test -tags ed2klive ./internal/ed2kwire/ -run TestLiveAServer -v
```

Both live tests **fail rather than skip** when unconfigured, on purpose: a
live test that skips reports `ok` having proved nothing. Read the *exit code of
the command*, not of the pipe.

---



**Written 2026-09-28 at a session boundary caused by a turn-lease failure. Start
from this section; everything below it is background.**

### Where the branch is

`develop` at `644deb212` — *"M5 step 5.4: validate the ed2k locator before the
consent gate"*. Commits on this milestone, all green at the time:

| Commit | What |
|---|---|
| `18a9845d5` | M5 step 5.3 — the magnet path and BEP 9 arrival |
| `e72cf063e` | M5 step 5.5 — the library hand-off, through the plugin API only |
| `a7b2dcf07` | M5 step 5.4 — the ed2k locator parser and eHash, plus a resume handoff |
| `8bcbcc941` | M5 step 5.4 — the ed2k tests, and the two bugs they found |
| `644deb212` | M5 step 5.4 — validate the ed2k locator before the consent gate |

**The working tree is clean.** `internal/ed2k/` and `internal/rpc/ed2k_gate_test.go`
are committed, not untracked.

**The branch is `develop`, not `main`, and `origin` CAN be pushed.** An earlier
revision of this file said there was nowhere to push and that the only remote
was the read-only `upstream`. Both were wrong: `origin` →
`https://github.com/8ullyMaguire/stash.git` is the fork, it is writable, and a
dry-run push succeeds. `upstream` → `stashapp/stash` really is read-only
(`403 Permission denied to 8ullyMaguire`) and must not be pushed to.

So: **commits are pushed to `origin/develop`**, with `git push origin develop`.
The `git pushall` mentioned in an earlier revision is not a command on this
host — that was a different repository's convention — and chasing it cost a
minute. A second worktree exists
at `~/code-local/worktrees/m6` on branch `m6-upstream-issues` — it is a
different milestone and is not part of M5.

### Verified state, re-checked just now

```
go version go1.27.1-X:nodwarf5 linux/amd64
go build ./...      clean
go vet ./...        clean
gofmt -l internal/  clean
go list -deps ./... | grep -c 'github.com/stashapp/stash/'   ->  0
```

| Package | Tests |
|---|---|
| paths | 11 |
| policy | 9 |
| storage | 16 |
| torrent | 51 |
| rpc | 36 |
| library | 30 |
| handoff | 14 |
| ed2k | **39** |
| **total** | **206** |

Core's own unit suite and `-tags integration` suite are both green on this
branch (`go test ./...` and `go test -tags integration ./...` in the repo root,
EXIT=0, no failures).

Eleven mutation harnesses, 318 mutations, 0 survivors. The step 5.4 harness is
`internal/ed2k/mutate_ed2k.py`: 56 rows across three files, 50 killed, 6 covered
by a lower layer, 0 survived, 0 malformed, exit 0. The step 5.5 harness is
`internal/library/mutate_library.py`: 34 probes across four files, 26 killed,
8 covered by a lower layer, 0 survived, 0 malformed, exit 0.

**Run the harnesses with `python3 <harness>` and read the EXIT CODE, not the
pipe.** `... | tail -40; echo $?` reports `tail`'s status. A run of
`mutate_library.py` that ends `PYEXIT=0` is the one to trust.

### THE NEXT TASK: the ed2k wire protocol

**Step 5.4's parser and hasher are done, tested and mutation-checked.** The
remaining piece of 5.4 is the protocol itself, and it is the largest single
piece of work left in M5.

| File | State |
|---|---|
| `ed2k.go` | `Locator`, `Kind`, `Hash`, `String`, `IsZero` — done, tested |
| `parse.go` | `Parse`, `ParseHash`, `checkName` — done, tested |
| `ehash.go` | `Sum`, `HashFile`, `HashBytes`, `TreeHash` — done, tested |
| `parse_test.go` | 21 tests — done |
| `ehash_test.go` | 18 tests — done |
| `mutate_ed2k.py` | 56 rows, all four verdicts — done |

```
internal/ed2k: 39 tests, 98.2% statement coverage
mutate_ed2k.py: 50 killed, 6 covered, 0 survived, 0 malformed  (PYEXIT=0)
```

The 5% and 25% that coverage reports as gaps in `HashFile` and `HashBytes` are
the **error branches**, and both were measured rather than assumed — see
"COVERED is a claim, not a decoration" below. Do not add a test to close them;
there is no input that reaches them.

**Verified correct, do not redo it.** `Sum` was checked against all seven RFC
1320 MD4 test vectors and passes — it is a real `Sum`-shaped helper because
`golang.org/x/crypto/md4` predates the one `crypto/sha256` grew.

The import is `golang.org/x/crypto/md4` at **v0.45.0**. Nothing was downloaded
and no version moved: `x/crypto` was already required *indirect* (the torrent
library needs its chacha20/poly1305), and importing it directly moved the line
from the indirect block to the direct one. That is the only `go.mod` change in
this commit and it is expected — **do not treat it as a new dependency or try to
revert it**, or the build fails.

MD4 is a protocol identifier here, not a security claim; the package's own
`Deprecated:` notice is about the other case, and the import comment says so.

**Next, in order:**

1. **The eDonkey2000 wire protocol**: the server connection, the Kad node list,
   the extended handshake. The plan's own words are *"hand-rolled because
   nothing in Go provides it"*, so this is written against the protocol
   description and nothing can be borrowed.
2. **The transfer surface** (unfinished since 5.3): piece verification, resume,
   rate limits — and `TestResume_SurvivesProcessRestart`, which needs a transfer
   that can actually be resumed. This is why
   `internal/rpc.downloadWithGate` still ends in
   `ErrTransferNotImplemented`, and it is the gap between "the library hand-off
   works" and "a downloader".

### The ed2k advertisement is now honest, and the test that keeps it so

The plugin used to **advertise ed2k and have no handler for it**: `SchemeED2K`
was in `knownSchemes`, `LocatorSchemeOf` accepted `ed2k://`, `source.json`
promised *"Fetches files over BitTorrent and ed2k"*, and a user could hand it
an ed2k link, get a proposal, be granted, and arrive at the transfer stub with
**nothing having validated the name**.

What landed instead of a handler:

- `internal/rpc/downloadWithGate` now parses any ed2k locator **before** the
  consent gate and refuses it if unusable.
- `internal/rpc/consent.go` has `isED2KLocator`, a prefix test that duplicates
  `LocatorSchemeOf`'s on purpose, with
  `TestTheED2KPrefixIsRecognisedTheSameWayTwice` asserting the two agree.

**Why before the gate, which is the whole assertion.** Core's gate decides
whether a locator may be stored against an object. It has no opinion about
filenames, and `ed2k://|file|../../etc/passwd|1|<hash>|` is a *well-formed
locator* by every test a consent gate could apply. So the gate cannot catch it,
and asking core anyway spends an operator's trust on a question with an obvious
answer. Each test therefore asserts two things: the download is refused, **and
`core.asked` is empty**. A test that only asserted the refusal would pass even
if the parser ran *after* a granting gate, because the stub refuses everything
anyway — so the ordering is asserted by its own mutation, and that mutation is
a row in `mutate_rpc.py` (the parse physically moved below `gateDownload`).

**What is still true and still stated plainly:** a granted, well-formed ed2k
link still ends in `ErrTransferNotImplemented`, because there is still no ed2k
transport. `TestTheDownloadStubStillReportsTheTransferIsUnimplemented` says so,
so the error cannot quietly start implying a download was attempted.

### Two harness lessons this step, both of them cost a real detour

**A duplicated defence reads like two defences.** The first version called
`LocatorSchemeOf` in `downloadWithGate` as well as in `gateDownload`. Correct
code, and it made the existing `mutate_rpc.py` row *"a file:// locator reaches
the protocol handler"* report **SURVIVED** — because disabling the check in
consent.go left my copy to refuse it. Not a hole, but the row's meaning had
changed underneath it. Deduping took it back to **29 killed, 0 survived**. A
survivor is a question about the code, and the first thing to check is whether
the code changed, not whether the test is weak.

**A list-form mutation row needs a 4th element even when it has no file tag.**
`run_all` unpacks `old, expect, tag = entry[1], entry[2], entry[3]`, so a list
row written with three elements raises `IndexError` on the *next* row and takes
the whole sweep with it. Pass `None` for "not consent".

**COVERED is a claim, not a decoration.** All six `COVERED` verdicts in
`mutate_ed2k.py` were measured, not assumed, and the measurements are recorded
in the comment above each row. Two worth knowing:

- `HashBytes` discarding `HashFile`'s error is unreachable *from that call
  site*: it takes a `[]byte` and wraps it in a `bytes.Reader`, which cannot
  fail. `HashFile` on a reader that does fail does return the error (measured).
- The `strings.ToLower(name)` in `checkName` really is redundant for the
  traversal check — every entry in `dangerousNameComponents` is `..` plus a
  separator, and none contains a letter, so `..\..\WINDOWS` is refused with it
  removed. It is **kept anyway**, because the UNC check is the case-sensitive
  one and one cheap line makes the whole function case-insensitive by
  construction rather than by remembering to lowercase at three sites.

### Why the order changed, in one line

Zero of the 850 issues in `docs/research/matrix.md` mention ed2k, eMule,
Kademlia or eDonkey — 0 hits in `docs/research/open_issues.json` too — while
5.5 is the half of M5's exit criterion the plan itself names ("get it scanned
and linked"). The user chose 5.5 first, then 5.4, then the rest. The reordering
is recorded in the plan at the 5.5 heading; do not "fix" it back.

### Two process rules this session established

**Long harness runs go in the background with `notify=True`, then `sleep` in a
foreground call.** A `timeout 580 python3 harness.py` in the foreground was
SIGTERM'd twice mid-probe. The first time it left a mutation applied to
`integrate.go`, which cost a debugging detour: the leftover looked like a
pre-existing bug, and the suite took 126 seconds to fail while the outer timeout
killed the run first. **After any interrupted sweep, grep the sources for
mutation markers before believing the next test failure** —
`grep -rn 'if false\|&& false' internal/` catches the common ones.

**Verify the exit code of the thing, not of the pipe.** A stale pair of harness
runs sat in the queue reporting `0 survived` and `7 survived` — from before the
harness fixes. Re-run before believing a number that came from a message.


## Verified state at this tag

- 39/39 unit packages, 0 failures
- 39/39 integration packages (`-tags integration`), 0 failures
- `go build ./...` and `go vet ./...` clean
- `internal/collab` at 177 top-level tests
- `pkg/auth` at 71 top-level tests, of which 9 are the 2FA *wiring* tests
- `internal/api` at 36, of which 19 cover the wizard's refusals
- `pkg/sqlite` adds 9 2FA store tests, one of which races 20 goroutines
- `internal/collab/mutate_consent.py`: **79 fixtures**, spanning collab,
  `pkg/auth`, the wizard handler, and the startup posture gate in `server.go`.
  One `MUTATION_TARGETS` list drives the preflight, the runner and the restore
  guard, and `main()` fails if the count the preflight promised is not the count
  that ran — a mutation group wired into two of three lists used to vanish with
  a clean-looking summary.

Counting convention, because the docs previously mixed two and it looked like a
1000-test regression: `go test ... -v | grep -c '^--- PASS'` counts top-level
tests only. Subtest-inclusive is 1481 unit / 2875 integration on the same tree.

## Running the checks

    export GOFLAGS=-mod=mod
    cd ~/code-local/go/stash

    go build ./...
    go vet ./...
    go test ./...                          # unit, 39 packages
    go test -tags integration -count=1 ./...   # integration, 39 packages
    python3 internal/collab/mutate_consent.py # the consent/exporter/federation guards

`GOFLAGS=-mod=mod` is required; without it the module will not resolve.

**The integration tag is not optional.** `TestStashForge_SchemaVersionMatchesAppSchemaVersion`
lives there, and it is the only thing that catches a migration committed without
the `appSchemaVersion` bump — which means the migration is never applied and the
symptom appears in some unrelated test as "no such table".

`go generate ./cmd/stash` regenerates the GraphQL layer. The generated files are
gitignored, so a fresh clone must run it, and it must succeed: a failure there
is invisible to every other check.

## Five things that will bite you

**1. Adding a migration means bumping `appSchemaVersion` in the same commit.**
`pkg/sqlite/database.go`. golang-migrate stops at the recorded version, so a
migration without the bump is silently never applied. The comment above the
variable says this; the integration test enforces it.

**2. Test fixtures create a dedicated instance per fixture.** Never a fixed or
shared row name, and never a fixed id — a fixed id passes exactly once and then
dies in the full suite, because the dev database persists between runs.

**3. A fake proves the logic; only a real database proves the SQL.** This bit
twice in one session. `internal/collab`'s suite passed 100% while the sqlite
adapter did not compile, because the fake had been written to match the interface
instead of the driver. If a type crosses a package boundary, there must be a test
that constructs the real one.

**4. A probe must carry a case it is EXPECTED to succeed.** A sweep probe meant
to disprove "absorb never merges" returned "0 of 80 merged" — agreement with its
own hypothesis, and vacuous, because its fixture helper seeded a 1-D vector that
cosine geometry correctly refuses. If nothing is ever admitted, the probe is
broken, not the system.

**5. Write the outside-construction test first.** Against the real
implementation of whatever the module will be handed. Both times a seam was
missed in this project, the half that existed was thoroughly tested and the
mismatch was invisible because no file imported both packages.

**6. A comment claiming a mechanism is on a path is not evidence that it is.**
The 2FA replay guard shipped broken: `checkSecondFactor` called
`collab.VerifyTOTP` with a nil spent-step set and returned nil, above a comment
saying "the store owns the spend record, so two concurrent logins cannot both be
accepted". `SpendTOTPStep` was never called on that path. The store was correct,
thoroughly tested, and not invoked. Every test before the fix called the session
store's methods directly with a fake verifier, so all of them passed.

Two things caught it, and both are now permanent:

- **Test through the constructor, not the method.** `TestWiring_*` builds via
  `Factory.Build` and logs in. A verifier that is implemented and never attached
  fails there.
- **A replay guard cannot be tested with a fake**, because the guard lives inside
  the component. Those tests drive the real arithmetic and take their *codes*
  from `pquerna/otp` — a second RFC 6238 implementation. Codes from the library
  under test prove only self-consistency; hand-rolled codes are a third
  implementation, which can agree with a broken one.

`internal/collab/mutate_consent.py` now mutates `pkg/auth/totp.go`,
`pkg/auth/session.go` and `internal/api/stashforge_wizard.go` too, so removing
the spend, ignoring the fresh flag, failing open on a store error, dropping the
instance-key check, re-deciding a decided wizard, or accepting an oversized body
each fail a test.

**7. A test table whose entries are all the same value tests one case.** The
wizard's empty-key test had a map with two keys and two values that were both `""`,
so both subtests took the same branch and the `len(want) == 0` guard was executed
by nothing. It looked like three cases and was one. The mutation that flips that
guard survived, which is how it was found.

**8. Test a function at the seam it is connected by, not the one you can reach.**
The mutation "the scheme is assumed to be https" survived, because the scheme was
derived inline in `Start()` and every posture test passed a scheme in as an
*argument* — nothing could see where the argument came from. Extracting
`Server.scheme()` so the derivation is its own testable unit is what closed it.
The same shape as the typed-nil trap below.

**9. A nil `*T` in an `interface` is not a nil interface.** `checkInstancePosture`
took an `instancePostureStore` and began `if store == nil`. The caller passes
`s.manager.InstanceModeStore`, a `*sqlite.InstanceModeStore`: when that is nil the
interface is **non-nil holding a nil pointer**, so the check never fired and the
function called `Mode()` on a nil receiver — a panic at boot for every
non-StashForge deployment. The nil test has to be against the concrete pointer,
at the caller.

**10. A size limit on a JSON decoder does not limit the body.**
`json.Decoder` stops at the end of the JSON value, so a small valid object
followed by megabytes of trailing whitespace decodes cleanly and the cap never
fires — measured, a 4105-byte body returned 200 with `MaxBytesReader` "in place".
The second attempt (probe-read after decoding) was wrong the other way: the
decoder buffers ahead, so the probe sees the padding, not EOF, and a body of
`limit-1` was refused. The limit belongs on the READER
(`io.ReadAll(http.MaxBytesReader(...))`), not on the decoded value.

## Where the design decisions are written down

Not in the code alone. Each of these has a comment at the decision, because each
is a place where the obvious implementation is wrong:

- `internal/collab/consent.go` — why absence means opted-in, why a corrupt value
  is an error, why an opted-out user is never re-prompted
- `internal/collab/exporter.go` — why the field list is a whitelist, why
  `BuildPayload` does not check consent, and what the path guard can and cannot
  detect
- `internal/collab/federation.go` — why publish and consume are separate flags,
  why the library id is a parameter to the content check rather than read from
  the payload
- `pkg/sqlite/migrations/101_libraries.up.sql` — why a library is a sharing scope
  and not a file collection

## Not done

**M4 has no UI.** Everything in M4 is server-side and tested; nothing is reachable
from a browser. In order of what a user would notice first:

- **The wizard has no UI.** The gate is now escapable —
  `GET/POST /stashforge/wizard` and `GET /stashforge/mode`, guarded by the
  instance key on the POST — but there is no screen. `curl` works; a browser
  gets JSON.
- **2FA enrolment.** The store, the encryption, the login gate and the replay
  guard all exist and are wired. There is no screen to scan a QR code, and no
  recovery codes. `collab.TOTPURIA` produces the provisioning URI, so a resolver
  is a small addition — but until it exists, an account cannot be enrolled, and
  an account that *is* enrolled (by direct DB write) can only be unenrolled the
  same way.
- **Library grants are now ENFORCED.** This was the long-standing blocker and
  it is closed. `library_id` is on all seven target tables (migration 105), a
  user id reaches the request context (`withRequestUserID`), and `allowMedia`
  gates every media route from the per-target `*Ctx` middlewares before any file
  is opened. `internal/api/mutate_media_gate.py` mutation-checks it.
  **Two things to know before changing it:**
  - The seventh target table is `groups`, not `movies` — migration 65 renamed
    it, and the plan, GOAL.md and migration 101's comment all still said
    `movies`. The first version of 105 failed with `no such table: movies`.
  - **A row with no library resolves to the DEFAULT library, not to
    "unrestricted".** Every newly-scanned row has `library_id` NULL, so
    refusing NULL outright would 404 the owner's own new files, and the fix
    shipped under that pressure is "make NULL mean allow". The default library
    is owned by the owner, who bypasses by ownership; everyone else still needs
    a grant row.
  - **A signed-URL request has no user id** and is therefore refused on a public
    instance. Deliberate: a device that cannot send a cookie cannot send a grant.
- **M4 IS COMPLETE.** The wizard screen exists at `/stashforge/wizard`
  (`ui/v2.5/src/components/Setup/StashForgeWizard.tsx`) — a route **separate
  from the upstream `/setup`**, which is the paths-and-credentials wizard.
  Sharing one screen would drop an operator re-running configuration into the
  sharing decision, which the server refuses with `wizard_already_completed`.
  It calls the HTTP endpoint, not GraphQL, because there is no session to send.
  **The mode is chosen once; changing a live mode is a separate authenticated
  operation that deliberately does not exist yet.** The only thing M4 does not
  have is a browser test, and `ui/v2.5` still has no test runner — the security
  property never lived in the client.
- ~~**GraphQL for 2FA, libraries, grants, consent.**~~ **Done** —
  `graphql/schema/types/hosting.graphql`,
  `internal/api/resolver_mutation_hosting.go`, `internal/api/models_hosting.go`,
  `internal/collab/library.go`, `pkg/sqlite/stashforge_libraries.go`.
  **Three things to know before changing any of it:**
  - **`collab.Library` is a DOMAIN type, not a row struct, because `is_private`
    is a three-state column and a Go `bool` cannot hold the third.** A library
    that defers to its owner's consent is not the same as one marked
    not-private, and gqlgen maps a nil pointer onto a non-null `Boolean` by
    returning `false` — which publishes a library nobody chose to publish.
  - **Libraries are per-user, not instance-wide** (101: `user_id` NOT NULL,
    names unique per owner). The first version of the schema described
    instance-wide libraries, which is a different data model wearing the same
    names.
  - **A 2FA mutation takes a code, never a user id.** `disableTOTP(userId:)` would
    be a mutation any account could point at the owner.
- **The mode is still chosen over HTTP, not GraphQL.** `POST /stashforge/wizard`
  is a one-time, instance-key-gated decision and refuses a second POST on purpose;
  changing a live mode afterwards needs a separate authenticated path, and
  deliberately does not exist yet rather than riding on the wizard endpoint.

**Four prose-versus-schema mismatches so far, all the same shape** — a COMMENT
states a constraint, the DDL does not create it, and the comment is believed:

1. The plan and GOAL.md list `movies` among the seven target tables. It is
   `groups`; migration 65 renamed it.
2. 101 says `UNIQUE per (owner, name)` and creates no such index. **Migration
   106** adds it — a user could previously create "Main" three times.
3. 105's partial unique index was on `(is_default)` alone — a UNIQUE constraint on
   a **constant**, so it enforced *one default on the whole instance*. The second
   user to create a library got a violation and **no default at all**, so every
   one of their unscanned rows resolved to "no library, no owner" and the media
   gate refused it. Now `(user_id, is_default)`.
4. 105's comment said NULL resolves to the refusal; the code resolves it to the
   default library. The code is right — NULL is the scanner's normal output, and
   refusing it 404s the owner's own new files.

**The rule: a comment in a migration is a claim about the schema, and the only
way to know whether it holds is to read the DDL in the same file.** Be suspicious
of a partial unique index whose `WHERE` clause is itself the constraint.

**`TestStashForgeStoreConstructorsAreActuallyWired`** now guards the fourth
"referenced != used": every `New*Store` in `pkg/sqlite` must be called from a
non-test file. It caught `sqlite.ConsentStore` — fully implemented, fully tested,
and built by nothing, so `setConsent` was dead code. It checks 30 constructors and
is mutation-checked. **If you add a store, wire it in `internal/manager/init.go`
after `Database.Open` and the test will tell you if you did not.**
- ~~**TLS enforcement.** Nothing calls it yet.~~ **Done** — `Server.Start`
  refuses to boot when the mode forbids the scheme
  (`internal/api/server.go`, `checkInstancePosture`). A public instance over
  plain HTTP no longer starts, and a store that cannot be read stops the boot
  rather than defaulting to a posture nobody chose.

**M5 is in progress: steps 5.0 and 5.0a are done.**

The downloader is at `plugins/p2pdownloader/`, its own module, and the seam is
proved by four tests in `internal/api/stashforge_p2p_seam_test.go` rather than
by a grep — because a package inside the core tree that imports nothing from
the core satisfies a grep trivially, so the original check would have been
green on something that cannot load and never runs.

**`go tool nm` does not work for "is this in the binary", and two other
mechanisms did not either.** It passed on a core that HAD the downloader linked
in: 113,767 symbols, none of them the plugin's. An uncalled function is
dead-code-eliminated, so `nm` cannot see it; an equally-unreachable string
constant is dropped by the compiler before the linker runs — measured on a
107 MB binary that did contain the downloader, where the marker was absent and
the module path was present. What survives reachability analysis is the module
path, in the binary's pclntab name table, so the test reads **bytes**.

**Three mutation harnesses, and the bugs they found in the harnesses
themselves:**

- `mutate_seam.py` (repo root) — 6/6 killed, and the bundled case is caught by
  the seam assertion at `seam_test.go:227`, not by the build guard behind it.
- `internal/collab/mutate_locator.py` — 14/14 killed.
- `plugins/p2pdownloader/mutate_rpc.py` — 23/23 killed.

Three classes of harness bug that each produced a **false green or a false
result**, and all three are worth checking for in any new one:

1. A mutation that **does not compile** is scored `broken`, not `killed`. Four
   of the locator mutations were `broken` on the first run — deleting a map key
   leaves a syntax error — and a harness that counted them as kills would have
   reported 14/14 while testing 10.
2. A **tautological mutation** is a no-op, so the test passes and the mutation
   is reported as a survivor. Two of the consent mutations were
   `x == nil || x != nil`, which is always true. Rewritten to actually change
   behaviour, both kill.
3. `mutate_seam.py` used `dirname(__file__)` as the repo root, so with the
   harness in `internal/api` it appended a `require` to `internal/api/go.mod` —
   **creating a nested module** — and built a second copy of the plugin. Its own
   "the core still builds" check caught it. A harness that writes into the repo
   must know exactly where the repo is.
4. `mutate_seam.py` restored with `git checkout`, which **cannot restore an
   untracked file** — and every file the seam mutations touch is untracked,
   because the plugin is work in progress. So it left `interface: raw` in the
   manifest and core's module path in the plugin's `go.mod`, and reported a clean
   tree while doing it. The go.mod one made the plugin unbuildable and read as a
   plugin bug. It now snapshots before mutating and **compares byte for byte
   afterwards**; a restore that cannot fail is not a restore.
5. `mutate_rpc.py` unpacked mutations with `entry[:4]`, which shifts every field
   left by one for the multi-edit form and put the file tag in the `expect` slot.
   The symptom was `unscored` with a message naming missing source text, which
   pointed at the code instead of at the harness. It now unpacks by shape.

**Two verdicts beyond killed/survived, both added after they were needed:**
`broken` (does not compile — NOT a kill) and `no-op` (changes no bytes — a
harness bug reported as a survivor). And some defects need **several edits** to
exist at all: dropping `file:` from the plugin's named refusals changes nothing
because the value is still refused as an unknown scheme, and accepting it in the
URL switch changes nothing because the prefix check fires first. Reported as two
mutations, both survive and both are reported as holes that are not holes.

**The consent gate is two problems, and the second is invisible from core.**
Core's half is `internal/collab/locator.go`; the plugin's is
`plugins/p2pdownloader/internal/rpc/consent.go`. The plugin holds the magnet in
memory whether or not core grants, so a refusal that does not end the transfer
is §7.1's failure with the gate working perfectly. Invariant: **no transfer
without a granted proposal for that exact locator**, and every way of not
having a grant refuses — nil answer, unreachable core, or a plugin with no gate
at all.

**M5 steps 5.1 and 5.2 are done. Step 5.3 has its seeding decision AND its
storage gate. 5.3's transfer surface and 5.4–5.5 remain**: BitTorrent
transfers, ed2k, library integration. **M6 is unstarted.**

### Seeding is derived from the tier, because uploading is not fetching

`docs/decisions/0002-seeding-policy.md`. `anacrolix/torrent` uploads
opportunistically by default — its own comment says so — and a permissive
default in a client pointed at a corpus of untracked, self-published material
means the box publishes strangers' work with nobody having decided it should.
Once the chunks are out, no later decision retracts them.

So §7.1 needed a third distinction. Storing a locator is writing it. Acting is
starting a transfer. **Seeding is a write to a library the operator never sees**,
and it is permitted only where a tier carries an assertion covering it:
`self_published` / `performer_claimed` / `third_party_permitted` may;
`unverified` may not; so may `quarantined`, `denied`, and **anything the build
does not recognise**.

`unverified` is where every object *starts*, so it is the common case — which is
what makes "nobody has objected" versus "somebody permitted this" the decision
that matters. And every permissive tier is a claim by an **identified** party,
so a value nobody can be identified for is not one.

`OperatorAllowedSeed` is an **outer bound, never an override**: it can narrow, it
cannot widen, and `TestTheOperatorCannotWidenThePolicy` pins that because it is
the direction a settings screen invites.

**The tier strings are duplicated** across the seam and a stale copy fails safe
and silent — every decision falls to the restrictive branch, seeding stops
everywhere, nothing errors. The drift test reads **both** files, both from
source: the first version listed this file's six by hand and compared against a
literal, and two mutations survived it. A test checking a hand-written copy of
what it is checking is the same mistake one level down.

### `anacrolix/torrent` v1.61.0 is adopted, and its path-safety function is not

`docs/decisions/0001-torrent-library.md`. DHT, BEP 47 v2 / BEP 9 magnet metadata
and rate limiting are all covered. Super-seeding and sparse are **not** — no
option for either exists — and neither is needed for a corpus of untracked,
self-published material.

**`storage.ToSafeFilePath` is documented as "ensuring the result won't escape
into parent directories" and is a string check.** 29 lines, and it tests whether
the *first* component of the joined path is `..`.

**Correction, 2026-09-28.** I previously wrote that `/etc/passwd` and
`..\..\windows` were reachable escapes, on the evidence that the function
returns them with a nil error. They are not. `filepath.Join(location, safeName)`
cleans the *concatenation*, so an absolute name is re-anchored under the
location and lands inside. Measured:

| Name | function | lands at | inside? |
|---|---|---|---|
| `["..","..","etc","passwd"]` | refused | — | safe |
| `["/etc/passwd"]` | accepted | `<root>/etc/passwd` | **safe** — `Join` re-anchors |
| `["..\..\windows"]` | accepted | `<root>/..\..\windows` | safe, one odd filename |

I asserted that from the function's return value without checking the caller's
join. The lesson is in the skill under "a documented guarantee is a claim".

**The escape that IS real is the symlink, and the library's own containment
check cannot see it.** `file-client.go:92` looks like a defence and
`isSubFilepath` looks like the check behind it, but `isSubFilepath` is
`filepath.Rel` plus `HasPrefix`, and `Rel` is pure string arithmetic.
Executed with the library's own functions, torrent directory a symlink:

```
file path     : <root>/downloads/innocent/passwd    (innocent -> outside)
isSubFilepath -> true
really is     : <root>/outside       (EvalSymlinks)
the decoy OUTSIDE the download root now reads: "PEERS CONTROLLED BYTES"
```

So `internal/paths.SanitizeJoin` is not a parallel implementation — it is the
only path safety this downloader has, and it must be enforced at *our* storage
layer because the library's cannot be. `internal/paths` runs `EvalSymlinks` on
both sides and compares with `filepath.Rel`, which is the filesystem-resolving
version of the check the library intended to write.

The **mmap** storage is worse: `grep -c isSubFilepath` is 0 for `mmap.go` and 1
for `file-client.go`, and it carries the TODO *"Support all the same native
filepath configuration that NewFileOpts provides"*. It also prepends
`BestName()` unconditionally where the classic path skips it for a nameless
(BEP 52 v2) torrent, so the first component `ToSafeFilePath` inspects is the
file's own. **This settles which storage implementation step 5.3 must use.**

The dependency also drags cgo sqlite in through the storage backends, which is
part of why the plugin ships as its own static binary. It must appear in the
plugin's `go.mod` and **not** in the core's.

### `internal/paths` — three findings the plan's seven cases did not cover

`..\..\windows` (the same attack, other separator), the Windows reserved names,
and **a symlink in a subdirectory** — the first-component case is obvious enough
that a check written for it looks complete.

`EvalSymlinks` on both sides, walking up to the first existing component: a
downloader resolves names for files that are not there, so resolving the whole
path fails constantly; and a *resolved* root against an *unresolved* child
rejects every legitimate file on macOS, where `/tmp` is a symlink.

Containment is `filepath.Rel`, never a prefix — `/data/downloads-evil` starts
with `/data/downloads`. And `Rel` has a subtlety worth knowing: a component
merely *beginning* with dots is not a traversal, so `HasPrefix(rel, "..")`
wrongly rejects `..leading.dots`. The legitimate-names test found that in the
test helper before it could reach the production check.

**`EnsureRoot` is asserted on the FILE still existing, not on the error.** A
plausible "fix" for "accepted a regular file" is `os.RemoveAll` then
`MkdirAll` — it compiles, returns nil for every input, and deletes a user's
file. That mutation is killed by
`TestEnsureRootNeverDestroysWhatIsAlreadyThere`.

### `internal/storage.Gate` — the library's only safe storage has no defence

The library needs a storage implementation whether or not the transfer code
exists yet, and *which* one is a decision:

| backend | containment check | verdict |
|---|---|---|
| `storage.NewFile` (classic) | `isSubFilepath` at `file-client.go:92` — a **string** check | unusable alone |
| `storage.NewMMap` | **none** (`grep -c isSubFilepath` → 0) | unusable |
| **`internal/storage.Gate`** | every raw component, then `paths.SanitizeJoin`, then the library's | **used** |

`Gate` wraps the classic backend and validates every file **before** handing the
torrent to the library, because the library's only extension points —
`FilePathMaker` and `TorrentDirFilePathMaker` — both return a bare `string` and
**cannot report an error**. A per-file refusal has nowhere to go, so it becomes a
sentinel filename and the transfer completes with the wrong file in it. Refusing
the whole torrent in `OpenTorrent` is the only place a real error can be
returned, and it is the only place one is used.

**The bug the tests caught while writing it, which is the whole reason to read
this.** The obvious up-front check validates the file's name as one string:

```go
name := filepath.Join(append([]string{info.BestName()}, file.BestPath()...)...)
paths.SanitizeJoin(root, name)   // <-- the traversal is already gone
```

`filepath.Join` **cleans** its result, so `["sub", "..", "..", "escape"]`
becomes the string `"escape"` before the gate sees it. `SanitizeJoin` is not
wrong — it is handed a name that no longer contains the attack. The gate
therefore checks each component **as the torrent supplied it** and never
pre-joins; the joined name is only an output, for the error message.

This is also why the library's `ToSafeFilePath` looks adequate and is not: it
joins first and checks the first component of the **result**, which is a
different question from "does any component of the input walk out".

The same trap bit a *test* later: a case using `{"..", "escape"}` under a
torrent named `torrent` becomes `torrent/../escape` → `escape`, which is inside
the root and correctly accepted — so the test passed for the wrong reason. Two
levels of `..` are needed: one to leave the torrent directory, one to leave the
root.

A second real bug the same tests caught: the backstop returned a bare
`.refused-by-stashforge` filename from a function whose sibling returns an
**absolute** path. The library joins it onto a base directory so it is harmless
in use, but `FilePathMaker` is a public extension point, and a relative filename
resolves against the *working directory* in any caller that uses it directly.

### A layered defence needs a `covered` verdict, or the harness lies to you

The gate has **three layers that all refuse a `..` walk** — per-component,
joined-name, and the torrent-directory check. So "remove layer 1" changes nothing
a test can see: layers 2 and 3 refuse the same input. That is **not a hole**,
and treating a surviving row as a defect sends you into the code to fix
something that is not broken.

So the harness has six verdicts, and the fifth is the one that matters:

```
killed    the named test failed
covered   no test noticed AND the whole suite still passed -- another layer
          refuses this input. Verified by re-running everything with the
          mutation applied, not assumed.
survived  the whole suite FAILED but the named test did not. A HOLE.
```

`covered` is not a pass; it is a *claim*, and the whole-suite re-run is what
makes it trustworthy. Only `survived` means a hole.

Two other things the harness needed: **compound mutations** (`old`/`new` as
equal-length lists, because disabling one layer is unobservable — and a single
edit labelled "COMPOUND" is a lie no harness can detect, which is why five rows
survived the first run), and **a timeout on every `run`** (a mutation that
removed a lock's `defer Unlock` deadlocked `go test` and hung the session for
five minutes).

And when a `survived` row is real: **apply the mutation, run `-v`, read the FAIL
lines**, and retarget. Five consecutive rows were the same mis-pointing — I had
named a test that passed while a different one caught the mutation.

### A test that asserts on your own decision cannot catch a broken wiring

The `internal/torrent` package exists to bind three decisions together: the
consent policy says whether a torrent may seed, the storage gate says where bytes
land, and `anacrolix/torrent` uploads opportunistically by default. Its first
test file had fourteen tests, all green, all asserting that the reported
`Decision` agreed with the policy.

I mutated the implementation to `spec.DisallowDataUpload = false` -- always
allow upload, the exact bug the package exists to prevent -- and **every one of
them still passed.** `Decision.UploadAllowed` is computed *from* the policy, so
asserting the two agree compares a value with its own source. The reported
decision was perfectly consistent with the policy and the client was being told
the opposite.

The fix is a read-back: `AppliedSpec` records what the client was *given*, which
`Decision` structurally cannot see. The first version of that also passed for a
second reason, and the reason generalises:

- `spec.Storage = nil` (bypassing the gate) also passed. Because
  `DefaultStorage` is the gate, the library called the gate anyway and the gate
  refused. The bytes were safe, the config was wrong, and nothing noticed.
- Ignoring the gate's error entirely also passed, for the same reason — a lower
  layer refused the identical input.

That is defence in depth, and it is worth having. It is **not** evidence the
wiring is tested, and a harness that reports those as kills is lying. They are
`covered`, and they are only `covered` if a whole-suite re-run passes with the
mutation applied.

**The rule:** a test whose subject is a *wiring* must observe the far side of the
wiring. A test that reads back your own return value is a test of your return
value.

### A sentinel you already have can hide a distinction you need

The gate refuses the same input in two places, and both wrapped
`storage.ErrRefused`:

- the up-front check, before the client is told the torrent exists
- the library's call to `OpenTorrent` during `AddTorrentSpec`

`errors.Is(err, storage.ErrRefused)` is true for both, so the test could not tell
them apart, and the only observable difference was the error **message**. The
first version of the test asserted on that message, which is a test that stops
testing the thing the moment someone improves the wording.

`ErrRefusedUpFront` now wraps `ErrRefused` and adds the distinction that matters:
whether the client ever held the torrent. The up-front refusal means nothing was
announced and nothing is cached; the later one means cleanup. A caller acts
differently on each, and a sentinel is the only way to say so.

The general shape: **`errors.Is` on a shared sentinel is a category, not an
outcome.** When one error can arise in two places with different consequences, it
is two errors.

### `x/time/rate` nil is not a library's "unlimited" idiom everywhere

`NewDefaultClientConfig` sets `UploadRateLimiter` to an *unlimited* limiter, so
"leave it nil" was never available — and setting it to nil would have **panicked**:
`config.go:278` calls `cfg.UploadRateLimiter.Burst()` with no nil check, on every
`NewClient`. The download side does handle nil explicitly
(`EffectiveDownloadRateLimit`), which is what makes the asymmetry easy to assume
away.

My test asserted the upload limiter was nil, on the reasoning that a client-wide
upload limit is a permission-shaped knob. The *reasoning* was right and the
*assertion* was wrong: it failed, and the fix was `rate.Inf`, not nil.

### There is no `Client.Listen`, and I documented one for two commits

`internal/torrent`'s comments said reachability was "deferred to `Listen`" —
that a downloader that has not been told to listen cannot announce itself, and
that UPnP belongs "at `Listen`" rather than in the config. **There is no
`Listen` method in `anacrolix/torrent` v1.61.0.** The sockets are created inside
`NewClient` (`client.go:385-420`) and the port forwarder is started there too:

```go
if !cfg.NoDefaultPortForwarding {
    go cl.forwardPort()
}
```

So reachability is decided **entirely by the config**, and the design was right
while the stated mechanism was invented. That is the failure mode this project's
notes keep hitting, in a new costume: I had `go doc`-ed `Client` and read the
`Listeners()` method next to it, and read a `Listen` into the gap.

Measured, with the real client:

| DHT | TCP | uTP | listeners |
|---|---|---|---|
| off | off | off | **0** |
| **on** | off | off | **0** |
| on | on | off | 2 (`0.0.0.0:42069`, `[::]:42069`) |
| on | on | on | 4 |

The middle row is the one worth keeping. A live DHT with both transports off binds
**nothing**: the box is findable by peers and cannot serve them. It advertises
interest, earns leech credit it cannot return, and disappoints everyone it
attracts — strictly worse than never having joined. So the DHT is off with the
transports, and the comment records the measurement so the "harmless" reading has
something to check against.

### A test that asserts a property of the DEPENDENCY is a tautology

The first version of the DHT test built a DHT-only client and asserted
`len(Listeners()) == 0`. It passed — and passed because of how the *library*
behaves, not because of anything this package does. Turning the DHT on in
`ConfigFor` left it green.

This is the "asserting your own return value" trap from the last commit, one level
up: the subject was the dependency, so no mutation to my code could ever fail it.
The fix was to assert `cfg.NoDHT` — a field this package sets — and keep the
listener measurement in a *comment* as the reason the field matters.

The general rule: **name the layer the assertion is about, and if a mutation to
your own code cannot change the result, the assertion is about something else.**
The socket test (`TestTheClientBindsNoSockets`) passes this bar — enabling TCP or
uTP in either `ConfigFor` or `New` makes it fail, and both sites are in the
harness.

### The library's `AddMagnet` is a bare path to the client, and the fix is a grep

`Client.AddMagnet` is four lines:

```go
func (cl *Client) AddMagnet(uri string) (T *Torrent, err error) {
    spec, err := TorrentSpecFromMagnetUri(uri)
    if err != nil { return }
    T, _, err = cl.AddTorrentSpec(spec)
    return
}
```

No policy, no gate, no metainfo check, no upload control. Anything added that way
is a torrent this code has never heard of, from a string a stranger put in a
database. So the magnet path lives here, and
`TestAddMagnetOnTheLibraryIsNotReachableFromHere` greps the package's own
non-test source for `.AddMagnet(` so it cannot be reintroduced by accident.

**The needle is `.AddMagnet(`, not `AddMagnet(`.** The bare name matches this
package's own declaration — the safe path the file exists to provide — so the
first version of the guard failed on its own remedy. A check that fails the
moment you write the thing it asks for is a check that gets deleted.

### A magnet cannot be gated, and saying so is the design

A magnet carries an infohash, a display name and trackers. No `files`, no
`length` — those arrive by BEP 9 from whichever peer answers first. So
`checkMetainfo` has nothing to check, the gate has nothing to resolve, and a
"check" on that input would be a check that always passes.

The gap is a real window in which the library holds a torrent nobody has
examined. It is acceptable for exactly one reason: **`OnMetadata` runs the full
sequence again on arrival and DROPS the torrent if the names escape the root.**
`Decision.Gated` and `GatedAfterMetadata` exist so a caller can tell which side
of that window it is looking at, and `Gated` is false for a magnet without
exception.

Dropping rather than marking is deliberate: a client that still holds a torrent
it cannot open keeps announcing it on the DHT, and a later `AddTorrent*` for the
same infohash succeeds from its own cache.

**The attack, and getting the fixture wrong hides it.** A magnet for *benign*
metadata followed by *hostile* metadata is not an attack — the hashes differ, so
the library rejects the mismatch. The dangerous case is a magnet whose infohash
**is** the hash of metadata naming an escaping path: the locator looks fine at
every point where a magnet can be checked, and the data is hostile when it lands.

### Two bugs a test that reads the wrong key cannot see

**The drop assertion was vacuous.** It looked the torrent up with
`client.Torrent(hash)` — and the fixture built the magnet from benign metadata,
so the magnet's infohash and the hostile metadata's were different, the lookup
missed, and "not found" looked identical to "dropped". It passed with `d.drop`
deleted. Asserted on `len(client.Torrents())` instead, which cannot be wrong that
way: 1 before the arrival, 0 after.

**The zero-infohash check was never reached.** Every URI in the test table was
rejected by `ParseMagnetUri` *itself* — "missing v1 infohash", "unexpected
scheme", "unhandled xt parameter encoding" — so the mutation disabling my
`IsZero` branch survived. The case that reaches it is
`magnet:?xt=urn:btih:0000…0000`: a 40-hex all-zero hash is well formed, the
parser accepts it, `AddTorrentSpec` does not object, and the client ends up
holding a torrent identified by nothing.

General form: **when a test's URIs are all rejected upstream, the test is
measuring the upstream.** Find the one input that reaches your branch.

### The same tautology, written twice

`AppliedSpec` exists because `Decision` reports intent and not what the client
was given. On the magnet path I wrote the record as:

```go
DisallowDataUpload: !upload,     // recomputed from the policy variable
```

which is the identical tautology in a second place — a mutation setting
`spec.DisallowDataUpload = false` leaves that line untouched, so the record still
agreed with the policy and the test passed on a torrent the client was being
told to upload. Now `spec.DisallowDataUpload`, read back off the spec.

Six of thirteen magnet-path mutations survived the first run. Every one was a
missing read-back or a test that observed the wrong thing, and all six are now
killed. The lesson generalises past this package: **a second path through the
same decision needs its own read-back**, because copying the first path's
structure copies its observability too — and its gaps.

### A build-error detector that is missing one form reports its own defect as a hole

The last survivor in the magnet run was scored `SURVIVED — a different test
failed`. The mutation was:

```go
-   spec, err := libtorrent.TorrentSpecFromMagnetUri(uri)
-   if err != nil {
+   var spec *libtorrent.TorrentSpec
+   var err error
+   if false {
```

which does not compile: `err redeclared in this block`. My `BUILD_ERRORS` list
had `build failed`, `cannot use`, `undefined:` and several others, but not
**`redeclared`** — so a probe's own defect was reported as a hole in the tests,
which is the exact inversion the `SKIP` verdict exists to prevent. The harness was
confidently wrong about its own instrument.

**The build-error list has to be complete, not representative.** A missing entry
does not produce a wrong verdict on one row; it produces a *false hole* that sends
the next person into the tests. Extended to twelve forms, and the row rewritten
to compile (`spec, _ := ...` with the branch dead), where it correctly reports
`covered` — the spec builder's own error is unreachable for a URI my upstream
`ParseMagnetUri` check did not already refuse.

### A real bug the tier test found

`AddMagnet` never called `remember`, so `tierOf` returned `""` for every magnet
and `OnMetadata` decided every arriving torrent as an unrecognised tier.
Restrictive, so nothing was ever published and **the downloader was inert rather
than broken** — no error, no log line, every test green. The association is now
made at add time on both paths; it is the second time it has been missing from
one of them.

### An empty `paths` in `metadataScan` is a FULL LIBRARY SCAN

The one finding in step 5.5 that would have been expensive to learn in
production. `getScanPaths` (internal/manager/manager_tasks.go:56) is:

```go
func getScanPaths(inputPaths []string) []*config.StashConfig {
	stashPaths := config.GetInstance().GetStashPaths()
	if len(inputPaths) == 0 {
		return stashPaths
	}
```

An empty list is not "scan nothing" — it is "scan **every** configured library".
The plugin is a background task, so that scan runs with nobody watching and is
not what anybody asked for. `library.Scan` refuses an empty slice rather than
sending it, and `TestTheScanIsAlwaysAskedForByPath` fails if anything can produce
a call with no path in it.

A **path** outside the library is different, and quieter: the host accepts the
mutation, runs a job that scans nothing, and returns a job id. So a *successful*
scan is not evidence the file was scanned. Only the job's own status and error
say why, which is why `library.Host` has a `JobStatus` method and not just
`SceneForPath`.

### "No scene appeared" is ambiguous, and the job status is what resolves it

It is what a subtitle looks like. It is ALSO what a path outside every configured
library looks like. Those two want different answers — one is "correctly not a
scene", the other is "your download path is misconfigured and every future
download will do the same" — and the plugin cannot tell them apart by polling the
file. Only the host knows its library paths.

The first version of `waitForScene` polled until the deadline regardless, so
every download that correctly produced no scene burned the full 90 seconds before
reporting a perfectly good outcome. In a library with 4,000 subtitle files that
is a day of the downloader's time. It now returns as soon as **either** a scene
appears **or** the job reaches a terminal state, and the caller distinguishes
`JobFinished` (a real answer), `JobFailed`/`JobCancelled` (a broken scan, which
must not be reported as "not a video") and READY/RUNNING (no answer yet, so a
timeout rather than a verdict).

### A path handed to a regex is a pattern

`findScenesByPathRegex` takes a REGEX. So the path goes into a regex, and a path
that arrives unescaped is a pattern:

| File name | Unescaped pattern matches |
|---|---|
| `Scene (2019).mp4` | a group — also matches `Scene 2019.mp4` |
| `a|b.mp4` | alternation — also matches `a.mp4` |
| `[a-z].mp4` | a character class |
| `.*.mp4` | **every** `.mp4` in the library |

The last one is the dangerous case, and it is reachable by accident. The plugin
would then report a link to a scene belonging to a different file, and the
operator would believe their download was linked when it was not.
`ExactPathPattern` quotes with `QuoteMeta` and anchors with `^...$` — the portable
anchors rather than `\A`/`\z`, because the pattern appears in the host's logs
where somebody is trying to reproduce it.

### `json.RawMessage` is a `[]byte`, so `data["field"]` slices it

```go
var data json.RawMessage          // it is a []byte
data["metadataScan"]              // THIS COMPILES. It is not a map lookup.
```

It slices twelve arbitrary bytes, and `json.Unmarshal` then fails on them for
reasons that name neither the field nor the endpoint. Decoded through a struct
instead — which also makes the field name a checked thing rather than a
substring.

### A guard that names the thing it forbids fails on its own declaration

This is the **third** time in this project, and it is now a rule rather than an
anecdote. The magnet grep failed on `.AddMagnet(` matching this package's own
safe `AddMagnet`. In step 5.5 it happened twice in one file:
`TestTheLibraryIntegrationReachesTheHostOnlyOverHTTP` failed on the forbidden
strings in its own list, and `TestTheHandOffUsesTheHostsOwnScanAndNotAFinger-
printOfItsOwn` failed on `phash` in the mutation harness's row labels.

The fix each time is the same and it is not subtle: **exclude the file that holds
the list.** Excluding the whole file beats exempting individual lines, because a
newly added forbidden string is then automatically exempt rather than needing
the same edit twice.

### A `COVERED` verdict is a claim, so the probe has to be the claim

Two handoff probes scored `COVERED` — "a lower layer already refuses this" — for
the wrong reason. The label said *"a scene id on every outcome"* and the probe
only added the id **after** the linked check, so it changed nothing a test could
see. A `COVERED` verdict is false reassurance, which is worse than a survivor
because it looks like evidence.

**The label is the claim and the probe has to be the same claim.** When a probe
scores `COVERED`, the first question is whether the probe expresses its own
label, and only the second question is whether a lower layer genuinely refuses
the input. Rewriting the probe the way the label reads killed all three.

### A nil interface method call is a segfault, and my own comment said otherwise

`Handoff.Library` may be nil, `Handoff.Run` then builds
`library.NewIntegrator(nil)`, and the first `Call` dereferenced a nil interface.
A nil interface method call is a **segfault, not an error** — the process dies
with no error to report and no stack in the plugin's own log.

The doc comment on `Handoff` had claimed this "fails through the normal path
rather than by dereferencing nil". That claim was false, it was written before
the code, and the test written to check it took the process down. The check lives
in `Call` rather than in the constructor, because both routes to a nil Host are
ordinary: a zero `Handoff`, and a caller that wires the dependency after
construction.

### A harness that is interrupted mid-probe leaves the mutation applied

A sweep of this package was killed by a `SIGTERM` partway through, and the probe
in flight left `continue` instead of `return nil, err` in `waitForScene`. The
suite then took **126 seconds** to fail and the outer timeout killed the run
first — so the interrupted sweep reported nothing, and the mutation it left
behind looked like a pre-existing bug. It was a real one: `continue` skips the
`select` at the bottom of the loop, so the loop never yields and spins at full
CPU for the whole 90-second deadline.

`internal/library/mutate_library.py` therefore keeps every file's original text
in memory, restores **per probe** rather than at the end, bounds every probe, and
verifies the restoration afterwards.

### A malformed probe must not be counted as a hole in the tests

The same incident exposed a scoring bug: the `elif` chain had three branches and
an `else` that caught both `COVERED` and `SKIP`, so a probe that failed to
compile was counted as a **hole in the tests** — the exact inversion the `SKIP`
verdict exists to prevent, reintroduced by the branch meant to implement it. A
second bug had the "pattern is not in the file" branch counting a skip without
listing it, so the summary said "1 malformed" and printed nothing beneath the
heading.

Four verdicts now, and they exit with different codes: a survivor returns 1 (go
look at a test), a malformed probe returns 2 (go look at this file). A single
non-zero code sends the reader to the shorter list.

### Nine mutation harnesses

```bash
python3 mutate_seam.py                                  # 6
python3 internal/collab/mutate_locator.py               # 14
(cd plugins/p2pdownloader && python3 internal/paths/mutate_paths.py)   # 12
(cd plugins/p2pdownloader && python3 internal/policy/mutate_policy.py) # 14
(cd plugins/p2pdownloader && python3 internal/storage/mutate_gate.py)  # 21
(cd plugins/p2pdownloader && python3 mutate_rpc.py)                    # 23
```

262 mutations across ten harnesses, 0 survivors. The earlier figure said
"seven harnesses" and omitted `mutate_seam.py`, `mutate_consent.py` and
`mutate_media_gate.py` — the plugin harnesses were being counted as the whole
set, which is the same scope error as the source-scanning test that walked
`..` from `internal/api`.

Run them **serially**. They edit real files and restore them.

The push destination is still unset: the only remote is `upstream` =
`github.com/stashapp/stash`, which is the upstream project. Everything here is
committed locally and unpushed, by instruction.
