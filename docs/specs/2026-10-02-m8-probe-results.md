# M8 step 0 — probe results

**2026-10-02.** §6b.7 says the implementer is "not to code past" three probes,
because a wrong assumption about each is expensive. Probe 1 is done; probes 2 and 3
are not. Nothing in M8 may be built on a guessed answer to either.

The probe is `plugins/p2pdownloader/cmd/probe_withhold`, runnable with
`cd plugins/p2pdownloader && go run ./cmd/probe_withhold`.

---

## Probe 1 — does the transport withhold the peer address?

**Answer: partly, and less than §6b.4 requires.** The measurement splits into three
findings that must not be collapsed into one word.

### 1a. Discovery is address-independent — MEASURED

With **no address ever supplied**, the client found peers within the probe window:

| run | peers found | sources |
|---|---|---|
| first | 110 | `Hg` (DHT) 110 |
| second | 224 | `Hg` 222, `X` 2 |

`Hg` is the DHT. Nothing was announced to a tracker and no `ip:port` was configured.
So a magnet alone is enough to *find and reach* peers, which is a real and positive
result — and it is a property of the protocol, not of this sandbox.

### 1b. Peer identity carries no address — MEASURED, by type

A peer is `(InfoHash, PeerID)` inside a connection. `PeerID` is a client identifier
with no address in it and nothing derived from one, and `libtorrent.PeerID` is
constructible with no address present at all. The address lives in the socket table,
below the protocol.

### 1c. What is still disclosed — the finding that decides M8's step 1

- **Tracker leg: the address IS disclosed.** A tracker sees the announcing peer's
  IP and port. Unchanged from any BitTorrent client.
- **DHT leg: both participants hold each other's routable address.** Peers were
  reached *without being given* an address; they were not reached *without
  addresses being exchanged*.

So §6b.4's hard requirement — the content plane **MUST NOT** require two instances
to exchange routable addresses — is **NOT met by this transport.** It is met by
neither leg.

**This is the decision M8 step 1 has to make, and probe 1 does not make it.**
Onion routing (Tor/I2P) or a relay-carrying mesh is the only thing that satisfies
the requirement as written. A tracker-based transport is not a candidate, and
calling it one would be the exact failure §6b.7 was written to prevent.

## How the probe got it wrong first

The probe's first draft predicted **0 peers, inconclusive** and printed a branch for
that case. It found 110. Two of its own API guesses did not exist at all in
v1.61.0 — `Client.AddMagnetSpec`, `torrent.PortMapper` — and two more were wrong in
shape (`AddTorrentSpec` returns three values; the peer-count method is `KnownSwarm`,
not `KnownPeers`; `PeerInfo` has no `Port`).

Every symbol was then taken from code in this repository that compiles against the
real library. That is the same failure as the `ToSafeFilePath` case the skill
records, one level up: **a name read off a neighbouring method is a guess with a
doc comment on it.** Four wrong names in one file, none of which existed.

---

## Probe 2 — what is the chunk hash?

**Answer: measured, and the collision worry I expected turned out to be the wrong
worry.** `plugins/p2pdownloader/cmd/probe_chunkhash`, on a sparse 4 GiB fixture.

### Verification cost, per shape

| shape | wire cost | damage recovery | what it commits |
|---|---|---|---|
| whole-file sha256 | 32 B | re-read 4 GiB, re-fetch 4 GiB | nothing much |
| 1 MiB chunk list | 144 KiB = **0.0034 %** | re-fetch 3 MiB | the wire format |

Whole-file verification is exact and costs **one full read**: a 4 GiB replica is
verified by moving 4 GiB. The chunk list costs three ten-thousandths of a percent
of the content and, on a 3-chunk damaged region, saves re-fetching 4 GiB. That is
the whole trade, in two numbers.

### Collision: NOT the deciding factor, and saying otherwise would be the probe talking

Over a corpus of a **billion** 1 MiB chunks (~1 PiB), birthday-bound:

| hash | P(collision) |
|---|---|
| ed2k MD4, 128 bit | 7.35 × 10⁻⁴⁰ |
| sha1, 160 bit | 1.71 × 10⁻⁴⁹ |
| sha256, 256 bit | 2.16 × 10⁻⁷⁸ |

A 128-bit hash is not remotely the problem. My first draft of this section claimed
otherwise by comparing a 160-bit hash against **one file's bit count** and printing
"headroom 0.0x" for both — a file's size says nothing about how many things get
addressed. It also panicked: `humanBytes` ran off the end of a `"KMGT"` unit table
at 1 TiB, which is a formatter that takes the measurement down with it. Both fixed,
and the second version's conclusion is the honest one.

### The finding that does survive

The 20-byte ed2k hash and the 20-byte infohash in this tree were chosen to
**identify a torrent**. A manifest hash answers a different question — it must
detect content *change* — so reusing an infohash would inherit BEP 3's truncation
semantics, a number chosen for a different purpose. The widths are fine; the
*provenance* is what would be wrong.

### Still not decided, and probe 2 does not decide it

A chunk list **commits the wire format**: once two peers exist, changing chunk size
or hash function is a protocol break, not a refactor. So the decision needs two
things a benchmark cannot supply:

1. **Is partial fetch a requirement or a nicety?** If a replica is all-or-nothing
   at the scene level — which is what the scanner and the file model already
   assume — a chunk list buys nothing and costs wire forever.
2. **The ed2k precedent.** ed2k *has* a chunk list, and its library was measured
   and kept **out** of the hash path
   (`docs/specs/2026-09-28-ed2k-wire-plan.md`). A wire format with a tree in it is
   a permanent obligation.

---

## Probe 3 — can a replica be served without becoming an unbounded liability?

**Answer: a policy question, and the probe's job was to make the current posture
measurable rather than to answer it.** `cmd/probe_serving`.

### This instance, as configured: unlimited

`UploadRateLimiter = rate.Inf` (`internal/torrent/downloader.go:392`). That is
deliberate and the reason is recorded beside it — *a rate is not a permission* — and
the library's own default is also unlimited, so `nil` was never available.

That reasoning is sound for **download**: we pay for what we asked for. It inverts
for **serving**: the bytes go to a stranger, the cost is ours, and nothing records
who took them.

### What one fetch costs (4 GiB scene, 100 Mbit/s up)

| concurrent fetchers | each waits | |
|---|---|---|
| 1 | 344 s | 5.7 min |
| 3 | 1 031 s | 17.2 min |
| 5 | 1 718 s | 28.6 min — bad |
| 10 | 3 436 s | 57.3 min — unusable |

One fetch saturates the link for 5.7 minutes. The cap cannot be set from the
sender's side alone, because the sender cannot distinguish a legitimate fetch from
a swarm pulling the same bytes repeatedly.

### A cap is a shared budget, not a percentage

| cap | each peer waits |
|---|---|
| 100 % of link | 5.7 min |
| 25 % | 22.9 min |
| 10 % | 57.3 min |
| 5 % | 114.5 min |

A 10 % cap protects the host and starves every peer at once; a per-peer cap
protects fairness and lets N peers multiply the host's bill by N. Neither is
obviously right.

### The amplification worry is smaller than it looks

A 10 Mbit/s seeder is matched by **2** leechers at 100 Mbit/s down, because each
leecher re-seeds ~10 Mbit/s. Past that the seeder is not the bottleneck, so one
generous host is **not** made to serve a swarm by asking nicely. The swarm's cost is
distributed — which is what makes preservation affordable at all.

The real cost is the node that is neither a pure seeder nor a pure leecher, which is
exactly what a preservation node is: it pays **upstream** to fetch a replica,
**downstream** to re-seed it, and only the disk copy satisfies `ALIGNMENT.md` §2. At
N=3 that is 4× the bytes for one copy on disk — a transcoder's cost profile without
the transcoding.

### Three questions the probe could not settle

1. **Who may fetch.** §6b.5 makes a replica subject to the *receiving* instance's
   consent — "may this instance hold it". It does not answer "who may take it from
   here". A node can be a consented replica host and still be an open distribution
   point.
2. **Whether a fetch is auditable.** R080's allocation log answers *inbound* — what
   did we place and why. Nothing records outbound fetches, so an operator who
   discovers they are serving a swarm cannot find out who did it. Same shape as
   §4.2's audit trail, pointed the other way.
3. **The cap's default.** Unlimited is defensible for download and indefensible for
   serving, so the default has to change with the role. A role the operator did not
   choose — a node that became a seed because the library offered it — is how a
   downloader becomes a liability without anyone deciding it.

---

## Step 0 is complete. All three probes are measured.

Nothing here decided M8 step 1's transport, chunk-hash or serving-policy question.
What it did is make each one a decision with numbers behind it, which is the whole
point of §6b.7's insistence.

### What the three probes cost to get right

Worth recording, because the failure modes are the ones this repo already knows:

- **Probe 1**: four API names that do not exist in the library, plus a prediction of
  "0 peers, inconclusive" when the answer was 110.
- **Probe 2**: claimed 128-bit hashes were too narrow by comparing a hash against a
  *file's size*; the real probability is 7 × 10⁻⁴⁰. Also panicked — a `"KMGT"` unit
  table indexed out of range at 1 TiB, which is a formatter that takes the
  measurement down with it.
- **Probe 3**: a `humanBytes`-style arithmetic slip printing `%!d(float64=1)`, and a
  one-character identifier typo (`uploadMbps` for `uplinkMbps`) that I misdiagnosed
  three times as a Go scoping or toolchain problem before diffing the declared name
  against the used one.

The probe 3 typo is the one worth remembering. It presented as
`undefined: uploadMbps` from a line three lines below a correct `const uplinkMbps`
declaration, in a file `gofmt` parsed without complaint, and a byte-identical copy
in a fresh directory failed identically. **The cheapest possible check — print the
declared name and the used name side by side — is what settled it, and I reached for
the compiler five times first.** A scoping hypothesis that survives a rename of the
*identifier* is not a scoping hypothesis.
