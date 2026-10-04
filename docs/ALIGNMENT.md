# Two-repo alignment — the contract between `stash` and `stash-box`

**Written:** 2026-09-30 · **Owner directive:** shift each project's priorities so
they complement each other; retain most functionality; change only what is an
improvement.

**Repos, and which is which:**

| Repo | Role | Licence | Database |
|---|---|---|---|
| `~/work/lane-2/stash` (branch `main`) | the **node**: local library, scanning, curation tools, content plane | AGPL-3.0 | SQLite |
| `~/work/lane-2/stash-box` (branch `lane-2/726-phase-F`) | the **commons**: metadata, votes, trust, discovery surfaces | MIT | PostgreSQL |

The `stash` fork's `stashforge` branch was merged into `main` and the two are one history
now, so "branch `stashforge`" is history rather than a place to work. `stash-box` is a
**separate programme** with its own goal-check (`docs/goal-check.py` in that repo) and its
own phase lettering; its phase-F branch is not required to be `master` for anything stated
here, and this file does not track its phase.

Both paths were `~/code-local/go/...` until 2026-10-04 and are now under `~/work/lane-2/`.
A second pair of checkouts exists at `~/work/lane-1/`, several phases behind on
`stash-box`; confirm which lane you are in with `git remote -v` before trusting a path.

This file is the boundary between them. Both specs cite it; neither restates it.
When the two disagree, this file is the tie-break, and changing it is an
amendment to **both** specs in the same commit.

---

## 1. The shape, in one paragraph

`stash` is a **node**: it holds the user's content, scans and curates it, and
fetches what the user would enjoy — opt-out, because fetching on someone's
behalf without asking is the thing that makes a library manager untrustworthy.
It contributes metadata to a commons and helps preserve content across nodes
without leaking an IP address or a name. `stash-box` is that **commons**: the
metadata, the votes, the trust model, the discovery surfaces, and the place
where a scene has one canonical identity shared by every node.

**The same three verbs run on both sides, over different objects.** That is the
whole design, and it is why the two projects are complements rather than
competitors:

| Verb | On a node (`stash`) | On the commons (`stash-box`) |
|---|---|---|
| **scan** | files on disk → scenes, images, fingerprints | submitted metadata → canonical entities, duplicates, merges |
| **curate** | local tags, performers, studios, favourites | edits, ballots, moderation, the audit trail |
| **preserve** | content replicas, verified manifests | metadata replicas, canonical identity, provenance |

Nothing is built twice. `stash-box` does not grow a scanner; `stash` does not
grow a vote tally.

---

## 2. The shared vocabulary

These are the same concepts with the same names on both sides. If a name means
two different things in the two trees, that is a defect in this file, not in
either implementation.

| Term | Meaning | Not to be confused with |
|---|---|---|
| **node** | one StashForge instance, holding content | the binary; one deployment |
| **commons** | the shared metadata surface, i.e. stash-box | the mesh as a whole |
| **content plane** | bytes: replication, storage, transfer | metadata plane |
| **metadata plane** | signed records, attestations, profiles | the content |
| **replica** | a verified copy of content on a *node* | a metadata row |
| **replication count** | how many *nodes* hold a scene, ≥3 by default | a stored counter — it is a **view**, always |
| **preservation** | keeping something on ≥N independent nodes | backing up your own disk |
| **taste** | a derived vector over a user's or instance's activity | a preference setting |
| **gravity** | an operator's axis biases for their instance | a vote (see §4.2) |
| **consent** | a user's explicit, revocable, per-object sharing decision | a role, a trust level |
| **trust** | earned standing, used to *weight* a ballot | access (see §4.1) |
| **access** | whether a user may see or fetch content | trust |
| **proposal** | the only write path to a shared field | a direct edit |

### 2.1 Names that had to be disambiguated, and how

Three words meant two things across the two trees before this file existed.
Recording the resolution so nobody re-litigates it:

- **"trust"** — `stash`'s `internal/collab/weighting.go` means *ballot
  weighting*; `stash-box`'s `internal/service/trust` means *access levels*. The
  rule, stated in both specs, is: **trust feeds weight, never access, and the
  two must not be readable by each other's code.** A column added for one is
  forbidden from being read by the other.
- **"replication"** — a *content* replica on a node, versus a *metadata* row in
  the commons. Always qualify. The default of three applies to content; metadata
  is always replicated.
- **"preservation"** — a mesh property (≥N nodes), not a backup. A node that
  loses its own disk has not lost anything if three peers still hold the scene.

---

## 3. Who owns what, and the one rule that follows from it

**The commons owns identity. A node owns bytes.** A scene has one canonical id
in the commons; every node references it. A node never invents a second
canonical id, and the commons never stores a file path.

The rule that follows, and it is the one to enforce:

> **No path, filename, hostname, IP address, or account detail crosses from a
> node to the commons, in either direction, ever.**

This is `stash` spec §6.2's "never published" list, and it is enforced in the
exporter's path guard — which has a positive control, because a guard tested
only with clean fixtures proves nothing. `stash-box` is the receiving end and
must not acquire a field that could hold one.

---

## 4. The three boundaries that keep getting blurred

Each of these is a real defect that has already been written once, so each
carries the rule and the test that would catch its return.

### 4.1 Trust is not access

`trust` decides how much a **ballot** counts. `access` decides whether a user
may **see or fetch** content. They are separate, and reputation must not bridge
them: a user with a perfect curation record gains weight, never a key to
someone else's library.

- `stash`: `internal/collab/weighting.go` (weights) must not import or read any
  access store.
- `stash-box`: `internal/service/trust` (levels) is not readable by the ballot
  weighting, and the access gate is a **conjunction of conditions**, never a
  disjunction — a disjunction makes the weakest control the effective one.

Test that would catch a regression: the two packages' guards, each named in its
own spec.

### 4.2 Gravity is an operator control, not a vote

Instance taste gravity is set by the operator (or by a delegated high-trust
subset). It is **not** a weighted vote in which a high-trust minority steers
every user's recommendations. Influence is granted as **priority and
nomination**; a vote on the theme is control, and control stays with the
operator.

This was proposed twice by the owner and rejected twice, in both specs, for the
same reason. It is recorded here so the third proposal has a decision to point
at rather than an argument to have.

### 4.3 A control keyed on a value the client supplies is not a control

Region-of-access, device-class, and "verify the client says it is in the
allowed country" are all checks the **client** performs. Applied in the
application they are theatre.

- **Region** → a policy handed to the proxy/CDN in front of the instance, where
  it is enforceable. If there is no proxy, the rule is **reported as
  unenforced**, not silently ignored.
- **Device class** → a soft signal feeding an anomaly score. Never a hard gate.
- **MFA** → a recorded requirement on the instance's auth provider, not a code
  path in a metadata server.

The same rule governs the content plane: a peer-supplied filename, capability
claim, and manifest hash are all **claims**, and the code says so in the column
name where a column exists.

---

## 5. Division of labour, by capability

| Capability | Lives in | Why there, and not in the other |
|---|---|---|
| File scanning, fingerprints | `stash` | the files are local; there is nothing to scan on a metadata server |
| Scene/performer/studio identity | `stash-box` | identity is the commons's entire reason to exist |
| Local favourites, tags, organised markers | `stash` | personal state, deliberately NOT governed (see §6) |
| Edits, ballots, moderation, audit | `stash-box` | it already has native consensus; a second tally would be a second source of truth |
| **Private** governance (a node's own library) | `stash` | `internal/collab`; upstream's rule is proposals-only for *shared* content |
| Content acquisition (P2P) | `stash` — **core, opt-out** | see §7 |
| Content replication (≥N nodes) | `stash` | it moves bytes; the commons has no storage |
| Metadata replication | **both**, always | the commons mirrors it; a node caches it |
| Collages, snapshots, identification | **both, split** | `stash-box` hosts the board and the canonical link; a node renders collages from cached snapshots |
| Discovery, recommendations, Elo | `stash-box` | ranking is a view over records only the commons holds in full |
| Gamification, quests, completion | `stash-box` | computed from the audit log; a node cannot see the audit log |
| Trust levels, access gate | `stash-box` | one authority, so there is one answer |
| Webhooks, API, SDKs | `stash-box` | the commons is the public surface |

**The seam that matters:** a node's `internal/collab` governs the *node's* copy;
the commons governs the *canonical* copy. A node that accepts a ballot about
its own library is a local decision. When the commons settles a change, the
node applies it as a **proposal**, not a write — which is the one rule that
keeps the two systems from being a second source of truth for the same fact.

---

## 6. What deliberately does NOT move

Recorded because each was a plausible move and each is wrong:

1. **The scanner does not move to the commons.** A metadata server has no
   filesystem.
2. **The vote tally does not move to the node.** It would fork the audit trail
   in half, and the half that matters would be the one nobody can see.
3. **Personal tags and favourites are not governed.** `stash`'s
   `TestNoResolverWritesASharedFieldDirectly` exists because *shared* fields
   need governance; a user's own bookmark is not a claim about the world. This
   is why the guard matches on receiver **type**, not method name.
4. **The P2P swarm vocabulary does not become a public protocol.** The mesh's
   content transport is instance-to-instance, spec-agnostic. A public swarm
   protocol plugs in behind that interface and adds no spec change — which is
   the test for whether it was the right shape.
5. **Negative reputation is not implemented.** Decay is the specified mechanism
   and it discounts a vote; a negative score is a second, stronger mechanism
   that no UI here can render.

---

## 7. The P2P decision, and what it replaced

**Owner directive (2026-09-30):** *"since we made p2p a priority, now the stash
plugin becomes part of core with (opt-out on sharing)."*

This **inverts StashForge non-negotiable #11**, which requires the P2P
downloader to be an installable plugin that the core does not import, with the
specific test `TestP2PDownloaderIsNotImportedByCore`. That inversion is
deliberate and the reason is recorded in the stash spec's §7.24, because the
test still exists and now asserts the opposite. A test that asserts the
opposite of a written rule is a **retracted rule**, and the retraction has to be
visible in the same file as the rule.

What is kept, and is the point of the change:

- **The module boundary survives.** `plugins/p2pdownloader/` keeps its own
  `go.mod`. "Part of core" means *shipped in the binary and configured in-repo*,
  not *merged into the core's import graph*. The seam test becomes
  "the core does not import the plugin's packages, and the plugin module still
  builds standalone" — a weaker but still real property, and honest about it.
- **Every guard survives unchanged.** The consent gate, the storage gate, the
  path sanitiser, the upload control, and `ErrRefusedUpFront` vs
  `ErrRefused` are all *more* load-bearing in core than in a plugin, because
  core code cannot be removed by deleting a directory.
- **Opt-out is on sharing, and it is a hard stop.** A node that has opted out
  neither fetches nor seeds. This extends the existing tier policy with a
  per-instance switch, and it is enforced at the same place the permission is
  read — not by omitting rows from a query.

**What the change costs, stated plainly:** a user can no longer remove the
downloader by deleting a directory, and the core binary grows by the
downloader's size. That is the trade the owner chose.

---

## 8. The metadata equivalent — the parallel the owner asked for

> *"then stash would do something similar for the metadata"*

The same three verbs, on the commons:

| | Content | Metadata |
|---|---|---|
| **acquire automatically** | P2P fetch, opt-out, into the library | pull canonical records from peers, opt-out, into the local cache |
| **preserve at ≥N nodes** | content replicas, manifest-verified | metadata is replicated to *every* peer, unconditionally |
| **without leaking identity** | onion-routed content transport | **already built** — `stash` federates over signed submissions with per-peer keys |
| **opt-out** | per instance | per object, and per library — `stash` spec §6 |

**Metadata preservation needs no ≥N rule, and that asymmetry is deliberate.**
Metadata is small, cheap, and the commons is already the durable copy; content
is large and must be spread. So: content gets the configurable N with a default
of 3, metadata gets unconditional replication. Stating this is the point —
"do the same for metadata" means *the same three properties*, not the same
number.

**The metadata auto-acquire path is already built and is the integration
point.** `stash` §6.5 (peer federation) plus `pkg/stashbox` (the client that
consumes stash-box today) is exactly "acquire metadata automatically, signed,
per-peer, revocable". What the mesh adds is the *taste-filtered* version:
acquire what this instance's gravity wants, not everything.

---

## 9. Verification: the cross-repo contract

The alignment is not a document, it is a set of checks. Four of them, and where
each lives:

| # | Property | Test | Repo |
|---|---|---|---|
| 1 | No path/host/IP in the exported payload | positive-control path guard | `stash` |
| 2 | Trust never grants access | the two access/weight guards | both |
| 3 | The plugin's modules are still the plugin's | seam test (retracted form) | `stash` |
| 4 | The commons has no field that could hold a path | a schema grep over `stash-box` migrations | `stash-box` |

**Check 4 was built on the commons side, and this section was STALE until 2026-10-02.**

It used to say check 4 did not exist, with a measurement behind the claim: that the
commons had "exactly one" path-shaped column. Both halves of that were out of date, and
the measurement is the part worth recording because a guard written from it would have
been wrong.

**The measurement was wrong, and re-taking it is what found the real work.** Re-measured
by script over all 94 migrations on 2026-10-02, the commons has **six** address-shaped
columns, not one:

| Column | Migration | What it is |
|---|---|---|
| `performer_urls.url` | `01_initial` | operator-typed inbound reference |
| `studio_urls.url` | `01_initial` | operator-typed inbound reference |
| `scene_urls.url` | `01_initial` | operator-typed inbound reference |
| `images.url` | `04_image_tables` | **served to a client** — the one that matters |
| `sites.url` | `21_site_urls` | operator-typed inbound reference |
| `webhook_endpoints.target_url` | `84_add_webhooks` | the endpoint this instance POSTs to |
| `federation_peers.base_url` | `89_identification_federation` | the peer's own base URL |

The last three did not exist on 2026-09-30. So a guard written from the old
measurement would have blessed one column and refused the other six — including
`images.url`, which is the only one of the seven that is actually rendered to a client,
and so the only one where a `file://` is an active primitive rather than a typo.

**What the check actually is.** The rule is about SHAPE, not reachability, and the
distinction is not a detail. A stored URL in a column a human transcribes is a typo at
worst; a stored URL in a column handed to a client is a weapon aimed at whoever views
it. So the two are governed by different predicates on purpose, and the reason is
recorded in the code rather than only here:

- `federation.ValidateImageURL` — `images.url` and only `images.url`. Scheme must be
  `http` or `https`, and the value must carry no local-file marker. It does **not** do
  DNS resolution, because this box never dials an image URL — a *client* does — so
  refusing a hostname that does not resolve from here would reject legitimate rows, and
  would be the guard being wrong in the direction that looks safe.
- `federation.ValidateBaseURL` — `federation_peers.base_url` only. The opposite: a peer
  is a host this box dials, so it resolves in DNS and refuses loopback, private and
  link-local ranges, which is what stops an operator registering a peer pointing at
  `169.254.169.254`.

Both are wired, not merely defined, and a guard nothing calls is a document — this file
*is* the document, so the wiring is part of the claim rather than an afterthought:

| Guard | Called from | When |
|---|---|---|
| `ValidateImageURL` | `internal/service/image/service.go` | image write |
| `ValidateBaseURL` | `internal/service/federation/service.go` (twice: create and update peer) | peer write |
| `dialGuard` → `ValidateBaseURL` | `internal/service/federation/client.go`, per peer | **every dial** |

The third row is the one that is easy to miss and the one that matters most. The
write-time check cannot cover the rebinding window: a hostname that resolved to a public
address when the peer row was written can resolve to `127.0.0.1` by the time we dial,
because a DNS TTL has nothing to do with when an operator registered the peer. An
attacker with a short TTL walks straight past a guard that only ran at insert. So the
check runs on the way out too, as a per-peer failure rather than a fatal one — refusing
the whole broadcast because one peer is unsafe would let any peer operator deny service
to every other peer.

**The positive control is the load-bearing part, and it is why the scan is not the
test.** A scan of the existing schema passes vacuously — the schema is clean, it was
clean on 2026-09-30, and it is clean now. So each predicate is tested by planting the
attack and asserting the refusal:

- `TestValidateImageURLRefusesLocalFileReferences` — `file:///etc/passwd`,
  `\\host\share`, and a bare `/etc/passwd` with no scheme at all. The third is the one
  a scheme check lets through, and it is why the guard is not a prefix test.
- `TestAdminCannotRegisterAnUnsafePeer` — R074 stated as a user-visible outcome, with
  `http://169.254.169.254/` in the table.

Each is paired with a test that the *ordinary* value is accepted, in the same file,
because a guard that refuses everything passes the attack cases and is deleted on first
real use. That has already happened once here: the first version of `ValidateImageURL`
reused the existing `IsSuspiciousValue` predicate, which is the F1 question-text
predicate and rejects any value containing `http://` — so it refused every legitimate
image URL on the column. The marker list is shared; the predicate is not.

**What this section does not claim.** The six columns above are not all equally
governed, and the table is the honest record of that: three are operator-typed and
*scanned* rather than refused, because refusing them would break real data. If a
hostile write can reach one of those, the current answer is a report, not a rejection,
and closing that gap is open work rather than something this file claims is done.
