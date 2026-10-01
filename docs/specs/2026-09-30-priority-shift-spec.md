---

## 6b. Priority shift, 2026-09-30 — the node half of the alignment

**Owner directive, verbatim:** *"stash helps me scan and curate all content
automatically and get content I would enjoy automatically, opt-out
(configurable), helping preserve content between all nodes without leaking ip
or any personal data, opt-out (configurable), for example ensuring quality
content is preserved in at least N nodes. then stash would do something similar
for the metadata."* Plus: *"since we made p2p a priority, now the stash plugin
becomes part of core with (opt-out on sharing)"* and *"shift each project
priorities, make them complement each other, they should retain most
functionality, only change anything if it would be an improvement."*

**Companion documents, which are part of this spec and are cited rather than
restated:** `docs/ALIGNMENT.md` (the cross-repo contract, §1–§9) and
`~/code-local/go/stash-box/docs/SPEC.md` §8 (the commons half).

**This section amends §6, §7 and non-negotiable #11. It replaces nothing else,
and it retracts one written rule outright — §7.24, with the reason and the cost
stated, because a retraction nobody can find gets re-litigated.**

### 6b.1 What the directive means as a priority order

The four capabilities, in the order the owner named them, and what each one
actually requires:

| # | Capability | Already exists? | The work that is missing |
|---|---|---|---|
| 1 | **scan and curate automatically** | yes — scanner, fingerprints, autotag, `pkg/stashbox` | the *automatic* part: a schedule, a policy, and a resume story |
| 2 | **get content I would enjoy, automatically, opt-out** | the downloader ships; the *taste* that chooses what is fetched does not | taste profile → acquisition queue, with a switch |
| 3 | **preserve across nodes without leaking IP or personal data** | metadata federation exists; content replication does not | the content plane, manifest verification, and the transport that does not leak an address |
| 4 | **do the same for metadata** | largely built (§6.5, `pkg/stashbox`) | the taste-*filtered* version, and the explicit asymmetry with content |

**The honest reading: this is not four new subsystems, it is two.** Content
acquisition (2) and content preservation (3) are one subsystem with two entry
points — the same transport, the same verification, the same consent gate — and
splitting them would mean inventing a protocol twice. Metadata (1, 4) is
mostly built and needs a filter, not a foundation.

**What is therefore reordered.** M7's plan (§ the ledger) put the *federation
protocol* first and content last, with content preservation deferred. That
ordering is right for *metadata* federation, which is what M7 step 7.1 builds,
and wrong for content, which is the directive's third item. The plan is amended:
M8 (below) takes content acquisition and preservation, and M7's content-shaped
steps (7.4 preservation, 7.5 duplicate-driven fetches) move into it.

### 6b.2 Capability 1 — automatic scan and curate

**Keep the scanner; add the policy.** Scanning is upstream's and it works. What
is missing is that "automatically" is a scheduling and consent question, not a
scanning one:

- A **default scan profile** per library: which paths, which file types, whether
  new files are scanned on the filesystem-watcher event or on a job. All
  existing config, surfaced with a sensible default; **no new scanning
  machinery.**
- **Curation is proposals, not writes** (#5). An automatic tag, performer, or
  studio suggestion is a `collab.Proposer` proposal, so an automatic action
  lands in the same audit trail a human's does. **This is the reason automatic
  curation is safe to enable by default and the reason it must not be a direct
  write** — an automatic direct write is a machine laundering a claim past
  governance, and it is the single most important constraint in this subsection.
- The stashed `pkg/stashbox` client already pushes proposals upstream; automatic
  curation makes that a policy rather than a user action.

### 6b.3 Capability 2 — content you would enjoy, automatically, opt-out

**What "I would enjoy" is.** The taste vector §6a.3 defines, computed from the
user's own votes, tags and curation — **a view, never a counter** (#4). The
acquisition queue is then: *what the mesh holds, minus what this instance already
has, ranked by taste similarity to what this user already watches.* The ranking
function is §6a.5's, unmodified. Nothing new is invented here; the mesh's
recommender is pointed at a download queue.

**The opt-out, and it is a hard stop.** A per-instance switch,
`auto_acquire = off` by default for a **new** install, and the default for an
existing install is **off** as well — see §6b.6. Enforcement is at the point the
permission is read, in the same place the existing tier policy is read, and
**not** by omitting rows from a query (non-negotiable #7, and #7's own note
that a refactor dropping the filter must not silently resume publishing).

**Three states, not two**, because a boolean cannot express the middle one:

| State | Fetches | Seeds | Uploads data |
|---|---|---|---|
| `off` | no | no | no |
| `fetch_only` | yes | no | no |
| `full` | yes | yes | per the existing upload control |

`fetch_only` is the state a privacy-conscious user actually wants and a boolean
forces them to choose wrongly in one direction or the other. The default for a
new install is **`fetch_only`**, not `off`: an instance that fetches but never
seeds leaks no data outward and cannot be made into a source for someone else,
so opting users in by default to the *harmless* half of the capability is not a
leak. Non-negotiable #7 is about the publish path, and `fetch_only` has no
publish path.

**Downloads still land in the library and are scanned**, exactly as §7
specifies — nothing about the acquisition path changes the ingest path.

### 6b.4 Capability 3 — preservation without leaking an IP or personal data

This is the capability with the most new design in it, and the constraint is
hard: **two instances must exchange bytes without learning each other's address,
and neither may learn anything about the other's user.**

**The transport.** A peer-to-peer transport that does not expose the peer
address: onion routing (Tor/I2P) or a mesh that carries a relay. This is the
mechanism §7.19 of the commons spec rejected as a *public swarm protocol* and
re-routed to an instance-to-instance protocol — and this is that interface's
first real user. Reusing it is the test §7.19 named: a new transport plugs in
behind the content plane and adds no spec change.

**Stated as a requirement rather than a technology, because a technology named
here becomes a dependency:** the content plane MUST NOT require two instances to
exchange routable addresses, and MUST NOT require either to learn the other's
users. What satisfies that is a decision for the implementation step, probed
and measured (§6b.7), not an assumption in this spec.

**What crosses the wire, and the four things that must never:**

| Crosses | Never crosses |
|---|---|
| content bytes, chunk-addressed by a content hash | the peer's address, unless the transport reveals it (it must not) |
| a manifest: content hash, size, chunk list | file paths, file names, or directory structure |
| the scene's canonical id from the commons | any user, account, username, or session token |
| capability *claims*, labelled as claims | anything the peer's consent forbids, in either direction |

**Metadata replicates unconditionally; content replicates to N, N configurable
and defaulting to 3.** Metadata is small, the commons is already the durable
copy, and a rule that puts metadata at risk of loss is a rule that loses it.
Content is large and must be spread, and N is a policy because a node's storage
budget is finite. **This asymmetry is the answer to "do the same for the
metadata", and stating it is the point: the same three properties, not the same
number.**

**Preservation is not a backup** (§2 of `ALIGNMENT.md`). A node that loses its
disk has lost nothing if three peers hold the scene; that is the property being
built, and it is why a replica is verified (§6b.5) rather than merely
transferred.

**A denied object is denied everywhere, permanently.** §6's `denied` tier and
its content-hash blocklist already propagate by hash to every peer and are
checked on import, scan, and match. **Replication is a publish path**, so a
`denied` object is never a replication subject and a preservation bounty never
overrides it. Non-negotiable #7 makes this a hard stop; this section names
replication as one of the paths it applies to.

**Preservation urgency, from the commons, not from popularity.** A scene is a
replication candidate when the commons reports it at fewer than N healthy
replicas, weighted by rarity and by whether anyone is asking for it — never by
how much mesh-wide traffic it has drawn, which would concentrate scarce storage
on content that is already plentiful.

### 6b.5 Verification is what makes a replica count

**A replica counts as healthy only after a manifest verification against a
content hash, never after a peer says it accepted the bytes** (§6a.2's posture,
applied). A peer that reports "replicated" is a claim; the same posture the
downloader's storage gate takes toward a peer-supplied filename applies here,
and the two share the reasoning.

Concretely: a replica is `verified` only when the reassembled content hashes to
the manifest's content hash. A replica that has not been verified is
`pending` and **does not count toward N**. This makes the ≥N promise true by
construction rather than by a peer's honesty, and it is why a failed
verification triggers re-fetch from a healthy peer rather than an alert.

**A replica is not a copy the receiving instance may forget.** Where a replica
is observable by a user of the receiving instance it is subject to *that*
instance's own consent state; an instance that must not hold a replica refuses
it, and refusing is not a partial success.

### 6b.6 Migration and defaults, stated as a table

The directive is opt-out, and the defaults below are the decision. They are
listed together because the interesting cases are the mixed ones, and a
per-feature table that is read one row at a time hides exactly those.

| Feature | Existing install | New install | Enforced at |
|---|---|---|---|
| auto scan | **on** (what it does today) | on | the scanner's existing config |
| auto curate as proposals | **on** | on | `collab.Proposer` — the only write path (#5) |
| content auto-acquire | **off** | `fetch_only` | the acquisition queue, at the permission read |
| content seeding | **off** | **off** | the existing upload control |
| content replication out | **off** | **off** | the replication scheduler, at the permission read |
| content replication in | **off** | **off** | the replication receiver, at the permission read |
| metadata share | unchanged (§6.1's existing default) | unchanged | the exporter, already a hard stop |
| peer federation | **off** per peer, already opt-in | off | already per-peer opt-in |

**Why content is `off` for an existing install and `fetch_only` for a new one.**
A node that begins replicating content onto peers it has never spoken to is a
publish action nobody consented to, and non-negotiable #7 is exactly the rule
that forbids it. A new install has no content yet, so `fetch_only` can change
nothing that exists; it is a starting posture, not a grant. **Every one of
these is reversible per instance, per library, and — for content — per
object**, and the settings page says which in one sentence with the exact
published set one click away, as §6.1 already requires for metadata.

**Turning replication on for an existing install is a one-line config change
and requires no migration**, because the policy is read at the permission and
not stored on the content.

### 6b.7 What has to be probed before implementation, not assumed

Three questions this spec deliberately does not answer, each because a wrong
assumption is expensive and each measurable in an afternoon. They are listed as
**probes with a named output**, in the plan's step 0, and the implementer is not
to code past them:

1. **Does the chosen transport actually withhold the peer address?** A
   capability checklist answers "is it there"; only running it answers "does it
   do what the doc says". This repo has already been bitten by exactly that
   shape, by a library whose documented safety property was implemented by
   checking the first component of an already-joined path.
2. **What is the chunk hash?** A content hash implies a Merkle tree, and the
   tree's shape is a wire-format decision that cannot be changed once peers
   exist. A plain per-file hash is simpler and wastes bandwidth on partial
   fetches; a content-addressed chunk list is better and commits the format.
3. **Can a replica be served without becoming an unbounded liability?** An
   instance that hosts replicas is serving bytes to strangers, which is a policy
   question (bandwidth cap, who may fetch, whether a fetch is auditable) and
   cannot be answered by the transport.

### 6b.8 P2P becomes core — the retraction of non-negotiable #11

**Non-negotiable #11 required** that the P2P downloader be an installable
plugin, in its own Go module, not imported by the core, removable by deleting a
directory, with `TestP2PDownloaderIsNotImportedByCore` as the enforcement. **The
owner's directive retracts it:** the downloader becomes part of core, with
opt-out on sharing.

**The retraction is deliberate and the reason is not "plugins are awkward."** A
capability the owner's product is *about* — acquiring content automatically, at
the mesh's recommendation — cannot be a thing a user has to discover, install
and trust separately, and cannot be the one feature whose absence makes the
product look broken. It also cannot keep the weakest property of the plugin
model: a plugin that is merely *configured off* and a feature that is *not
there* are different products.

**What survives the retraction, and must:**

1. **The module boundary.** `plugins/p2pdownloader/` keeps its own `go.mod`.
   "Part of core" means *shipped in the binary and configured in-repo* — not
   merged into the core's import graph. The seam test keeps its second half,
   which is the one that matters: the core must not bundle or shell out to the
   downloader as a separate process. The first half (no imports) survives as a
   weaker, still-real property.
2. **Every guard, unchanged and now more load-bearing.** The consent gate
   (§7.1, core's, never the plugin's), the storage gate, the path sanitiser,
   the upload control, and the distinction between `ErrRefusedUpFront` and
   `ErrRefused` — a refusal before the client ever held the torrent is a
   different event with different consequences from a refusal during cleanup,
   and a sentinel is the only way to say so. **Core code cannot be removed by
   deleting a directory, so a guard in core is permanent, and a permanent guard
   is one that has to be right.**
3. **The mutation gate.** Non-negotiable #10 applies to every guard in this
   path, and the seam's own history is the argument: a deleted call site that
   killed no mutant is how a milestone stayed green while doing nothing.

**What it costs, plainly:** a user can no longer remove the downloader by
deleting a directory; the core binary grows by its size; and a defect in the
downloader is now a defect in the product rather than in an extension of it.
That is the trade the owner chose, and the opt-out is the mitigation, not a
reversal of the decision.

### 6b.9 Non-negotiables: one amended, none removed

| # | Status | Change |
|---|---|---|
| **#11** | **RETRACTED** | §6b.8. Replaced by "shipped in core, module boundary kept, guards kept" |
| **#7** | **EXTENDED** | Opt-out is a hard stop in **three** paths, not one: the metadata exporter, the acquisition queue, and the replication scheduler (out and in) |
| **#4** | **EXTENDED** | "No stored counters" now covers the **replication count** explicitly: it is a view over verified replica rows, which is what makes ≥N true by construction (§6b.5) |
| **#5** | **EXTENDED** | Automatic curation writes **proposals**, so an automatic action is in the same audit trail as a human's (§6b.2) |
| **#3** | unchanged | SQLite only. A mesh does not get its own database |
| #1, #2, #6, #8, #9 | unchanged | — |

**The additions, as new non-negotiables:**

13. **Nothing crosses a node boundary that identifies a person or a host.** No
    path, filename, directory structure, hostname, IP address, username, or
    session token, in either direction, ever. Enforced on the node by the
    exporter's path guard *with its positive control*, and on the commons by the
    receiving-end guard that does not exist yet
    (`stash-box` R074, `ALIGNMENT.md` §9). **A one-sided guard is half a
    guard.**
14. **A replica counts only when its content hash verifies.** Not when a peer
    says it stored the bytes (§6b.5).
15. **The content plane never requires two instances to exchange routable
    addresses.** Stated as a property, tested by probe, not satisfied by naming
    a technology in this spec (§6b.7).

---

## 6c. M8 — the node's content plane, and what it displaces

**The plan amendment.** `docs/specs/2026-09-27-stashforge-plan.md` gains an M8
whose step 0 is §6b.7's three probes, because the wire format cannot be chosen
before they are answered and cannot be changed after. Nothing in M7 is
cancelled; the content-shaped steps move:

| Was | Now | Why |
|---|---|---|
| M7 step 7.4 (preservation, ≥3) | **M8 step 8.3** | it is content replication, and it needs a transport that does not exist until M8 step 8.1 |
| M7 step 7.1 (federation wire format) | M7 step 7.1, **metadata plane only** | it is built and it is the commons integration point; two planes, two formats |
| M7 step 7.3 (recommendations as a view) | stays in M7 | ranking is needed by *both* the discovery surface and the acquisition queue |
| — | **M8 step 8.2** | the acquisition queue: §6a.5's ranker, pointed at a downloader, with the three-state switch |

**M8's exit criterion is not "the code builds".** It is: a scene present on
exactly N-1 nodes is fetched by the Nth without either node learning the other's
address, verified by content hash, and a node with sharing switched off
participates in none of it. Each clause has a test; the middle one has a probe.
