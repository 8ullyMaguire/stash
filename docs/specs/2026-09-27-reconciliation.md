# Spec Reconciliation — StashForge ↔ Commons

Date: 2026-09-27
Author: Hermes, for the owner
Supersedes: the four contested sections of each spec, as cited

This document records the reconciliation between the two specs. It is not a
merge: the two remain separate codebases with separate plans. What it does is
make each one say the same thing about the four points where they contradicted
each other, so that a capability built in one is a capability the other can
accept rather than re-derive.

**Owner decisions, binding:**

| # | Decision | Owner |
|---|---|---|
| 1 | **AGPL-3.0 for both.** Keep the AGPL grant. | owner |
| 2 | **Full P2P downloader plugin.** Real BitTorrent + ed2k, not metadata hand-off. | owner |
| 3 | **Commons governance supersedes.** §8 wholesale. | owner |

Everything else was mine to decide; each is marked below with its reason.

---

## 0. The one correction that decides #2

The analysis that brought these two specs together asserted that
StashForge §7 "builds a full downloader **plugin**". Reading the code, that is
not quite right, and the difference decides how the downloader ships.

**Stash's plugin host is `goja` — JavaScript.** `pkg/plugin/js.go` runs a
`jsTaskBuilder` over a goja VM. The three transports stash actually offers are:

| `interface` | transport | what it runs |
|---|---|---|
| `js` | goja VM | a JavaScript plugin |
| `raw` | `exec` per task, stdin/stdout, process exits | a binary, one shot |
| `rpc` | `exec` once, long-lived, `net/rpc/jsonrpc` | a binary, persistent |

`Config.Exec []string` (`pkg/plugin/config.go:49`) is the field that launches a
program; `Config.Interface` picks the transport.

So **"a separate Go module in `pkg/p2pdownloader/`" is not a Stash plugin at
all.** It would be a Go package in the core tree, which is exactly what the
owner's requirement rules out, and the plan's own seam test would pass on
something that cannot load:

> `TestP2PDownloaderIsNotImportedByCore` asserts the grep is empty.

It would be empty. The package is *in* the core tree, importing nothing from it,
and the test would be green while the milestone described something that
installs and never runs.

**This is not an argument against the owner's decision — it is the shape the
decision takes.** A native Go P2P client is wanted; the transport that can
carry one is `rpc`.

**Decision 4 (mine): the downloader ships as a separate Go module, built to a
static binary, loaded by a stock StashForge as an `interface: rpc` plugin.**

- It is **native** — the owner's stated preference, and the reason the owner
  preferred native over the goja route. No JavaScript runtime is involved.
- It is **a real Stash plugin** — installed by dropping a directory with
  `source.json` into the configured plugins path, on a stock build, with no
  recompilation. That is the owner's "easy to install" requirement, met
  literally rather than by assertion.
- It is **genuinely outside the core** — a different `go.mod`, which the
  existing plan already identified as the real enforcement mechanism.
- It gets a **long-lived process**, which `raw` cannot provide and which a
  seeder needs: DHT participation, inbound peer connections, and a transfer
  that survives across tasks all require a process that stays up.

The seam test changes shape accordingly, and this matters: the test must assert
the core neither imports the module **nor loads the binary as part of itself**.
A test that only greps for an import would pass on a core that shells out to a
bundled downloader, which is the failure mode that actually matters.

---

## 1. License (decision 1 — owner: keep AGPL-3.0)

The finding was that Commons §3.2 justifies the Rust rewrite on licensing
grounds while the repository is AGPL-3.0, so the stated reason for the rewrite
was false.

`Cargo.toml:28` is `license = "AGPL-3.0-only"`, and `LICENSE` is the AGPL text.
The comment above the line is already correct and already says the thing worth
saying: a public index must offer its source to the people it serves, and §13 of
the AGPL requires exactly that.

**What changes:** §3.2 loses the licensing bullet and keeps the ecosystem one.
**What does not:** the license, the §16 borrowing terms, or the `LICENSE` file.

The bullet was wrong in a way worth recording rather than silently deleting.
It claimed a from-scratch implementation "keeps the platform permissively
licensed". Writing the code yourself changes the copyright; it does not change
the license you chose to apply, and the reasoning "reusing stash's code obliges
any networked deployment to publish modifications" applied exactly as well to
code written from scratch. A rationale that survives its own refutation is
usually a rationale for something else, and the ecosystem reasons — ONNX
Runtime, usearch, ffmpeg-as-sidecar, Tauri — stand on their own and are kept
verbatim.

**Applied to StashForge:** no change. It was already AGPL-3.0, correctly and
without a false rationale.

---

## 2. P2P (decision 2 — owner: full downloader plugin)

This is the genuine contradiction, and it is worth being precise about where it
actually lives, because the two sections agree more than the framing suggests.

| | StashForge §7 | Commons §2, §5.18, §5.18.1 |
|---|---|---|
| Plugin or core | plugin | **already a plugin** (§5.18.1) |
| Locator metadata | — | stores infohash/ed2k/magnet |
| Speaks the wire protocols | yes, in Go | **no, never** |
| Network reach | full egress | **loopback only** |
| Scope | full client | hand-off to a client you run |

**The contradiction is not "plugin vs core".** Commons already made it a plugin
in §5.18.1, with a WASM sandbox and declared capabilities. The real difference
is scope and reach: Commons' plugin computes hashes and hands a URI to a local
client over loopback; StashForge's plugin *is* the client.

The owner's decision resolves it in StashForge's favour, and it can be adopted
honestly rather than by weakening the other side's claim, because Commons'
strongest argument was mechanical and is preserved:

> "no swarms, no peer list, no DHT, no incoming connections" stops being a
> commitment in a design document and becomes a property the host can prove.

**What is preserved from Commons, because it was about the host and not the
downloader:**

1. **The consent gate stays in core, never in the plugin.** A tier check inside
   a third-party component is a check that can be buggy, disabled, or hostile.
   The plugin *requests*; the host *decides*. A hand-edited or malicious
   downloader plugin asking to redistribute a `denied` object gets the same
   refusal a well-behaved one gets. This is the single most important thing
   Commons §5.18.1 contributes to the full-downloader design, and it is
   orthogonal to how much the downloader can do.
2. **Locators are excluded from fingerprint publication and research exports**
   by default. A magnet plus a size plus a name is a strong join key.
3. **`redistribution_permitted` is a separate gate from the consent tier.** A
   `third_party_permitted` item without the flag is watchable and not
   redistributable, and a full-featured downloader is exactly where that
   distinction gets tested — because it *can* redistribute, and now the flag
   has teeth it would not otherwise have.

**What Commons gives up, and it should say so plainly:** the structural
"no-downloader" property. A plugin that can run a DHT and hold inbound
connections cannot be given `loopback_http` only. The sandbox's value here drops
from "impossible" to "declared and disclosed", which is a real weakening and
belongs in the document as such rather than being argued away.

**StashForge's §7 additions to Commons, all preserved:** protocol coverage
(BEP 3/5/9/10/12/19/20/27/41/47), Kademlia + Mainline DHT, magnet metadata
fetch, multi-connection transfers, piece verification, resume, sparse files,
seeding, super-seeding, rate limits, piece ordering, and library integration by
fingerprint.

**Path traversal stays the first test written, not the last.** A `.torrent`
whose name is `../../etc/cron.d/x` must resolve inside the target root or be
rejected. With a real client this is the bug class that gets a whole box, and
it is worse than in Commons' loopback-only plugin because a full client accepts
far more attacker-controlled path material.

---

## 3. Governance (decision 3 — owner: Commons supersedes)

Commons §8.1–8.5 is adopted wholesale. It is a superset, not a different
answer, and two pieces close gaps StashForge has no answer for at all:

- **Reputation-weighted ballots** mean quorum is no longer one account from
  Sybil. StashForge's flat `Σ(+1) − Σ(−1) ≥ threshold` is sound and already
  built and tested, but `MinVoters=3` is a speed bump, not a defence: three
  fresh accounts cross it.
- **The five-role table with an explicit no-login `public` role** is what
  actually implements "public site". StashForge §8 is about hosting but says
  nothing about who may read.

**One StashForge rule is carried into Commons, because Commons does not state
it:** *sticky rejection per (target, field, author)* — an author whose proposal
on a field was rejected does not get an infinite retry queue against that
field, and superseded-by-newer is a distinct terminal state from rejected. With
reputation and Sybil damping in play, the reverse is worse, not better: a
low-reputation account can cheaply generate near-misses.

**Both arrived at the same rule independently**, and it should be stated once:

> Scores are recomputed from the accepted-edit set, never read from a stored
> counter.

That fixes stash-box #743/#9 in both. Duplicated correctly; keep one copy and
cross-reference.

**StashForge M2's existing work is not discarded.** It is the substrate: the
proposal table, the vote table, the audit table, the closed field vocabulary,
and the apply path with its compare-and-set are all still needed. What changes
is the decision function — `Evaluate(Policy, VoteCount)` becomes a
weight-aware, reputation-sourced, per-field evaluation — and the field
vocabulary gains the new content types. StashForge M2 is not redone; its
acceptance rule is replaced.

**Cost this decision does not hide:** StashForge M2 is complete and green, and
this makes part of it wrong. `internal/collab/governance.go`'s flat net-vote
arithmetic and `MinVoters` become the fallback path (they are still needed for
the moderator-only configuration) rather than the primary one.

---

## 4. Amateur content (my decision: Commons wins outright)

Not contested, so stated briefly: Commons §7.1 `PersonCluster` is the answer to
the original ask and StashForge has no equivalent. The plan does not propose
one — StashForge inherits stash's scraper-only identity model, which is
precisely the gap that leaves amateur material unlinked.

**The part that matters most is a detail, not the architecture:** the ambiguous
bucket is a first-class state and an unnamed cluster is the *default*. A system
that requires a name before it will link a person cannot serve a corpus where
nobody knows the names. Every other design detail in §7.1 follows from that.

Also carried: §7.4 body/appearance similarity, §14.1's six consent tiers with
`redistribution_permitted` independent of tier, and the hash blocklist that
propagates to peers. StashForge's binary `metadata_share` opt-in/opt-out is
replaced by the tier model — a binary flag cannot express "visible but not
redistributable", which is the state most of an amateur corpus is actually in.

**Consent defaults differ and that is a decision, not an oversight.**
StashForge §6.1 defaults `metadata_share` to `opted-in` because the owner asked
for local use to contribute by default. Commons defaults `unverified` items to
private. Both are defensible; they cannot both be the rule. **I keep
StashForge's opt-in default**, with Commons' disclosure discipline: a blocking
first-run screen naming exactly what is published, re-prompted when the
published set changes. An amateur corpus needs contribution to happen, and the
disclosure is what makes an opt-in default honest rather than a leak.

---

## 5. What this does not reconcile

Stated so nobody assumes it did.

- **Language, runtime, and content model.** Still a Go fork and a Rust
  rewrite. Still different object models (§5 adds audio, comics, funscript,
  text, interviews to StashForge's inherited set). These are not contradictions,
  they are the two projects, and reconciling them would mean picking one.
- **"Drop-in place capable" — read this narrowly.** It means *convergent specs*:
  each codebase can accept a capability designed in the other, and neither will
  reject it as already-solved differently. It does **not** mean one binary can
  load the other's plugins, share a schema, or interoperate. They cannot, and
  no amount of spec editing changes that.
- **The M5-vs-Phase-10 plugin transports differ.** StashForge's is a Go binary
  over `net/rpc`; Commons' is WASM in a capability sandbox. Both are plugins;
  the mechanisms are not interchangeable, and Commons' sandbox still has the
  stronger property for its own scope.
