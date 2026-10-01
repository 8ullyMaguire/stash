# FEDERATION.md — the mesh wire format

**Status:** step 7.1, version 1. Spec §6a.18, §6a.2, §6a.6, §6a.9, §6a.16.
Requirement R059. Package `internal/mesh`.

This document specifies the peering handshake, the taste-profile exchange,
capability advertisement and replication coordination. It is the format every
later mesh step depends on, which is why it lands with the schema and before any
algorithm that uses it: building the scheduler first means inventing a private
protocol, and then inventing it twice more for discovery and for querying.

**Scope, deliberately narrow.** This is a *format*. It decides what goes on the
wire and in what order. It decides nothing about storage policy, replica
placement, ranking or access. Those are steps 7.2 through 7.7, and putting one of
them here is how a wire format stops being reusable — a scheduler inside the
protocol package can read a peer's claims (see [Claims](#3-claims)), and then the
format has an opinion about how much storage a stranger said it has.

---

## 1. Identity and namespacing

Every entity that crosses a node boundary is namespaced by its origin
(§6a.6). A peer's `scene 412` is **not** this instance's `scene 412`.

This is not a naming convention. `mesh_replica` carries `source_endpoint` as part
of its primary key precisely so that two peers offering the same local id cannot
collapse into one row — because a collapsed row means the mesh believes it holds
a replica it does not have, and a missing copy that health checks pass is the
worst failure available to the mechanism whose entire purpose is knowing what it
still holds.

Two rules follow, and they are different rules:

- **Across the wire**, ids are `(instance_id, local_id)` pairs. Neither half is
  meaningful alone.
- **In storage here**, a replica is keyed by a **local** `scene_id`. A remote id
  becomes a local one only through an identification solve (step 7.5).

The second is a deliberate asymmetry. Storing a composite remote id here would
mean inventing a cross-instance id space in the schema, before this document says
how ids are exchanged — and inventing it in the schema is how it becomes
permanent and load-bearing before anyone has agreed what it means.

---

## 2. The handshake

First message of a peering exchange. Version first, then identity, then claims.

```json
{
  "version": 1,
  "instance_id": "inst-7f3a…",
  "display_name": "A Peer",
  "public_key": "<base64, ed25519>",
  "claims": {
    "claim_store_bytes": 1099511627776,
    "claim_bandwidth_bps": 10737418240
  }
}
```

**Version is checked first and is fatal.** A peer speaking a version this build
does not implement is refused, not best-effort parsed. The reason is specific: a
partially understood replication manifest is how a peer convinces you to store a
file you cannot describe, and nothing about that failure is loud.

**`public_key` is required.** A peer with no key cannot be verified and must not
be stored as though it had been — `ErrUnsigned` is a distinct error from
`ErrBadSignature`, because "we were not asked to check" and "this peer proved
something false about itself" are different operator problems and only one of
them is an accusation.

**Signature verification is a separate step.** `Handshake.Validate()` in
`internal/mesh` checks well-formedness and version. It cannot verify a signature,
because the key arrives inside the message it is supposed to have signed. A single
`Validate` doing both would let a caller pass by checking the cheap half and
believe it checked everything — the "handle that is right about one property and
useless about another" failure the spec names for federated ids.

**Order is load-bearing.** Read the claims only after the signature verifies. A
verifier that decides anything from the claims first has used unauthenticated
input, and any decision it caches from them outlives the handshake.

---

## 3. Claims

> **A claim is what a peer says about itself. Nothing may size from one.**

`claim_store_bytes` and `claim_bandwidth_bps` are the peer's own statements,
received from a stranger. They are unverified by construction: we did not measure
them, we were told them.

**Why the rule is stated this hard.** The failure is silent. A node that sizes a
receive buffer from a stranger's claim works perfectly for every cooperative
peer, and does not notice for a week — because nothing raises an error, the
numbers are simply wrong. A claim three orders of magnitude off is
indistinguishable from the allocator being generous.

**How the rule is kept.** Two mechanisms, and they are deliberately redundant:

1. **Naming.** The Go fields are `ClaimStoreBytes`, not `StoreBytes`, so a call
   site has to write `p.ClaimStoreBytes` to read one. The accessor is
   `ClaimStoreBytesOrZero()`, never `HasCapacity()` or `CanStore()` — those names
   invite the use this exists to prevent.
2. **A source-scan guard.** `TestAProfileClaimIsLabelledAClaim` in
   `internal/mesh` walks the repository and fails if those fields appear in any
   non-exempt `.go` file. It is a grep-level test because there is nothing to
   assert at runtime: a function that sizes from a claim produces a perfectly
   reasonable result for any honest peer.

The guard exempts `internal/mesh` itself, because that package has to name the
fields to decode them — a scan that included it would match its own struct tags
and pass forever while the invariant rotted. The exemption is pinned by
`TestTheExemptionIsOnlyThisPackage`, so it cannot be widened silently.

**What belongs in a sizing path instead:** a *measured* capacity. Ask, verify,
and record it separately from the claim. A measured value and a claimed value are
different facts and belong in different columns — `claimed_store_bytes` and
`measured_store_bytes`, never one wearing the other's name. `TestHealthIsOneOfFour
ValuesAndNotACounter` asserts no such "measured" column has appeared next to a
claim.

**A peer's claim about trust is a claim about a claim.** `trust_profile` is what
the peer asserts about its own standing. §6a.10 holds that reward never grants
access, so a peer cannot grant standing either — not by claiming, and not later
by being agreed with.

---

## 4. Taste-profile exchange

The instance publishes `mesh_instance_profile.taste_profile`: a BLOB holding the
last **derivation** of local records, rebuilt from them by step 7.4.

**It is a published view, not a score.** It is not incremented, and no ranking
reads it as an authority about what the instance likes (non-negotiable #4 — the
ranking that consumes it is computed from records). The column is named
`taste_profile` rather than `taste_score` for this reason, and the same is true of
`gravity`, which §6a.8 makes operator-set and explicitly *not* an override of any
individual entity's rank.

Peers read `updated_at` to decide whether to re-fetch. The index exists for that
one query.

---

## 5. Replication coordination

Replicas live in `mesh_replica`; bytes served from here are recorded by
migration 110's `mesh_replication_serve_log`.

**Verification is a fact about a digest.** `manifest_hash` is what must still be
verified and is required. `health` is a four-value domain
(`pending`/`verified`/`corrupt`/`missing`), and `verified_at` is the evidence for
it: the database refuses a row that claims `verified` with no timestamp, or that
carries a timestamp alongside `corrupt`. Two facts that contradict each other
cannot both be stored, which is what stops a health check believing a copy is
verified when it was not.

There is no verification *score* and no failure count. A count would be a stored
tally that drifts from the thing it summarises — and the tally is also the thing
an attacker increments without the event happening.

**`replica_path` is relative to the storage root and checked by the database.**
A path from a stranger is the one input that can name a file outside the library,
so the schema refuses absolute paths and any path containing `..`. This mirrors
non-negotiable #13 on the exporter side: no path, filename, directory structure,
hostname, IP, username or token crosses a node boundary.

**Serving is budgeted where the bytes leave** (§6a.9's storage and bandwidth
budgets). A node with no budget row serves nothing, and the budget check and the
log write are one transaction — a cap checked after a transfer is not a cap. See
`docs/M8-PROBES.md` probe 3.

---

## 6. What this document does not decide

Stated so a later reader does not look for it here:

- **Trust levels and access** (§6a.10, §6a.11) — step 7.2. Access is *earned from
  the audit log* and consented to per instance; neither is derivable from a
  handshake.
- **Ranking and recommendations** (§6a.5, §6a.16) — steps 7.3, 7.6. Computed from
  local records, never from a peer's claim.
- **Replica placement** (§6a.9) — step 7.4. A replica target is chosen by taste
  similarity; choosing where to put bytes before there is a taste to match is how
  you end up storing everything on whoever answers first.
- **Cross-instance query execution** (§6a.6) — step 7.6c. The GraphQL surface of
  §6.5 is reused, extended with a ranking argument; this format is not a query
  language.

---

## Related

- `docs/specs/2026-09-27-stashforge-spec.md` — §6a.2, §6a.6, §6a.9, §6a.16, §6a.18
- `docs/specs/2026-09-27-stashforge-plan.md` — step 7.1, and step 7.8's gate
- `docs/M8-PROBES.md` — probe 3, the serve budget
- `internal/mesh/protocol.go`, `internal/mesh/claim_guard_test.go`
- `pkg/sqlite/migrations/111_mesh_instance_profile.up.sql`, `112_mesh_peer.up.sql`,
  `113_mesh_replica.up.sql`