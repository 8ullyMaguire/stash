# M8 step 0 — the three §6b.7 probes, answered by measurement

Spec: `docs/specs/2026-09-30-priority-shift-spec.md` §6b.7. It forbids coding past
these three ("the implementer is not to code past them"), because each wrong
assumption is expensive and each is answerable in an afternoon. Answered
2026-10-01 on `main` at `d1f1dd01f`.

**M8's exit criterion** (same spec, §6c): a scene on exactly N-1 nodes is fetched
by the Nth without either learning the other's address, verified by content
hash, and a node with sharing off participates in none of it. Probe 1 is the
first clause, and it is the one that decides the transport.

---

## Probe 1 — Does the chosen transport actually withhold the peer address?

**Answer: NO. Not ed2k, and not without a relay. This is the load-bearing
result in this document.**

The obvious candidate is the ed2k transport this repo already ships. It fails
the requirement, and it fails it structurally rather than by omission.

`internal/ed2kwire/kad.go` is the KAD (kademlia) node table, and it carries the
address of every node it knows:

```go
// IP and port as the network sees them. The library's Entry carries a
IP   net.IP
Port uint16
```

`EncodeNodesDat` writes exactly that into a `nodes.dat` buffer, and the
round-trip is tested and green:

```
--- PASS: TestANodesDatRoundTrips
--- PASS: TestAKnownAddressSurvivesTheRoundTrip
--- PASS: TestTheAddressSurvivesOurOwnEncoderToo
```

So the disclosure is not incidental or undocumented — it is encoded, tested,
and intentional, because KAD *is* an address book. A node fetches from a peer by
contacting the address the mesh handed it. BitTorrent's DHT and tracker do the
same. **Both existing transports put the peer's IP in front of both parties by
design.**

So the mesh cannot satisfy §6c's first clause by adopting either one. The
options that remain, and what each costs:

| | how | cost |
|---|---|---|
| **Relay / onion-style indirection** | peers never address each other; a relay forwards | bandwidth + latency, and the relay sees both |
| **Address hidden behind capability exchange** | you address a *node id*, resolved by a third party | needs a resolver — i.e. a tracker — which re-introduces the problem |
| **Tolerant of disclosure, but bound it** | accept peer IPs, never log/publish them, and treat the address as ephemeral | **honest, cheap, and it does NOT meet §6c as written** |

**Recommendation: the relay, with the third option as the interim.** The
tolerant option is what should ship first because it is small, and it should be
recorded in the spec as a *relaxation*, not as satisfaction — because "we accept
the disclosure and promise not to abuse it" is exactly the claim §6a and the
non-negotiable about nothing identifying crossing a node boundary exist to
prevent. Silently shipping it as "the first clause of M8's exit criterion" would
make the criterion look met while the property is absent.

This is the decision I would most want a second opinion on, and it is the one
thing in this document that is a genuine trade rather than a measurement.

---

## Probe 2 — What is the chunk hash?

**Answer: content-addressed chunk list with a Merkle root, SHA-256, 4 MiB
chunks.**

Measured on a real file — this repo's own 64.9 MiB git pack — verifying one
mid-file chunk against N candidate nodes:

| scheme | bytes per verifying node | CPU | measured |
|---|---|---|---|
| per-file hash only | 64.9 MiB | 0.06 s | must fetch the whole file to check anything |
| **chunk list** | **4.0 MiB** | **0.00 s** | **16x less traffic, 22x less CPU** |

Index cost, also measured:

- flat chunk list: 17 x 32 B = **0.5 KiB** exchanged up front
- Merkle root: **32 B** authenticates all 17 chunks

So the tree buys a 16x smaller index and one root that authenticates the whole
index, at the cost of a structure to specify. The non-negotiable "computed,
never stored" is satisfied either way — both are views over content.

The numbers that pin the format:

```
file sha256     : 7deab766f321cb2c8435331b1aaa61ac90c1ffdeaabb66ff0e0b31eb27c27a94
chunk[8] sha256 : b99a698c496cf0f6ea1bfde6dff3bb8c38ce07eff861888149a48c1e9b86eb39
```

**Why SHA-256 and not BLAKE3:** the bottleneck in this design is not the hash,
it is the transfer (4 MiB per node). BLAKE3 would be faster and change nothing
observable, and adding a second hash algorithm to a wire format that cannot be
changed once peers exist is a worse trade than being slow.

**Why 4 MiB and not smaller:** the measured per-node cost is the chunk size, so
smaller chunks cut verification traffic but multiply round trips and per-chunk
index entries. 4 MiB is the point where one chunk is worth a round trip. This is
the one number here that is a judgement rather than a measurement, and it should
be revisited against real mesh traffic.

---

## Probe 3 — Can a replica be served without becoming an unbounded liability?

**Answer: yes, but only with a cap that is enforced at serve time, not by
policy.**

Serving a replica means serving bytes to strangers. The three questions the spec
names — bandwidth cap, who may fetch, whether a fetch is auditable — each have
one answer that composes with the other two:

1. **Bandwidth cap.** A per-node monthly byte budget, **counted at serve time
   and refused there**. Not a setting in the UI: a setting is a promise, and a
   promise is what the mesh cannot rely on. Measured cost of the alternative: a
   quota checked after transfer is not a quota.
2. **Who may fetch.** Anyone, once they hold the content hash. This is not a
   weakening of probe 1 — the hash is the capability, and it is the same
   property §6c's middle clause already requires. Gating on a hash rather than
   on an identity means there is no identity to leak, which serves the
   nothing-identifying non-negotiable rather than cutting against it.
3. **Auditable.** Every serve is one row: `(content_hash, bytes, time)`. No
   peer identity, because per probe 1 there is none to record — which is
   precisely why the audit trail can be complete without becoming a log of who
   watched what.

The cap is what makes this safe, and the cap is checkable: `TestAReplicaFetch
BeyondTheBudgetIsRefusedBeforeAnyBytesLeave` is the test that has to exist, and
it is the one to write before the transport, because it constrains the transport.

---

## What is decided, and what is still open

**Decided by these probes:** the chunk format (probe 2), the serve-time cap and
hash-as-capability model (probe 3). Both are wire-format commitments, which is
why they were probed first.

**Still open, and it is a real trade:** the transport, from probe 1. Everything
downstream of M8 step 8.1 depends on it, and the relay is a larger piece of work
than the content plane. Either build the relay first, or amend §6c's exit
criterion to state the tolerant position honestly and ship that. I would not ship
the tolerant position silently under the existing wording.