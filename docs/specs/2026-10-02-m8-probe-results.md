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

## What is still unmeasured

- **Probe 2 — the chunk hash.** `metainfo.Hash` is 20 bytes and is the *infohash*;
  a content hash for chunks is a separate decision (Merkle tree vs per-file vs
  content-addressed chunk list) and it **commits the wire format**, which §6b.7
  says cannot be changed once peers exist. Not started.
- **Probe 3 — can a replica be served without becoming an unbounded liability?**
  Bandwidth cap, who may fetch, whether a fetch is auditable. A policy question the
  transport cannot answer. Not started.
