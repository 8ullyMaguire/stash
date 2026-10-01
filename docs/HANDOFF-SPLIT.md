# HANDOFF — stash / stash-box split across two profiles

**Owner instruction, 2026-09-30:** *"I want this session to work on stash while
the other works on stash-box. if you can coordinate with it fine, otherwise
just keep a handoff file updated for me to share between profiles."*

**This file is the coordination mechanism.** It is one file, written by whichever
profile is working, and it is the only place the two sessions communicate. There
is no cross-session channel, so nothing in it depends on one.

| | This profile (`coding-3`) | The other profile (`coding`) |
|---|---|---|
| Repo | `~/code-local/go/stash`, branch **`stashforge`** | `~/code-local/go/stash-box`, branch **`master`** |
| Worktree | `~/code-local/worktrees/stashforge` | in place |
| Owns | the node: scanning, acquisition, content plane, P2P-in-core | the commons: metadata, trust, discovery, identity |
| **Touches** | **nothing under `stash-box`** | **nothing under `stash`** |

**Rule for both sessions: do not edit the other repo, and do not run its test
suite.** A `go test ./...` in the wrong tree is not harmless — it competes for
the same build cache and the same Postgres/SQLite fixtures, and on this host two
`go test ./...` runs at once have already made results unreadable.

---

## 1. The shared contract, and where it lives

**`~/code-local/worktrees/stashforge/docs/ALIGNMENT.md` is the single source of
truth for anything both sides depend on.** It is written from the stash side and
it is committed there, because that is where the work is, but it governs both.

It is a document, not a plan. Three things in it are the whole design:

1. **The commons owns identity; a node owns bytes.** A scene has one canonical
   id in stash-box; every node references it. A node never invents a second
   canonical id, and the commons never stores a file path.
2. **Three boundaries that keep getting blurred** — trust ≠ access, gravity is
   an operator control not a vote, and *a control keyed on a value the client
   supplies is not a control*. All three have been written wrong once already.
3. **The shared vocabulary** (§2), so one word cannot mean two things across the
   two trees. Three words did, and §2.1 records how each was resolved.

**If the two specs ever disagree, ALIGNMENT.md wins, and changing it is an
amendment to both specs in the same commit.**

---

## 2. What each side owns, and the one rule that keeps them honest

| Capability | Owner | Why there |
|---|---|---|
| Scanning, fingerprints | stash | the files are local |
| Scene/performer/studio **identity** | stash-box | identity is the commons's reason to exist |
| Local tags, favourites, markers | stash | personal state, deliberately **not** governed |
| Edits, ballots, moderation, audit | stash-box | it already has native consensus |
| Private governance of a node's own library | stash | `internal/collab` |
| Content acquisition (P2P) | stash, **core, opt-out** | |
| Content replication (≥N nodes) | stash | it moves bytes; the commons has no storage |
| Metadata replication | **both, always** | commons mirrors; node caches |
| Discovery, Elo, quests, completion, trust | stash-box | ranking is a view over records only the commons holds in full |
| Personal favourites, "would enjoy" | **stash** | a node can see what its own user watches; the commons cannot |

**The rule:** a node's `internal/collab` governs *the node's copy*; the commons
governs *the canonical copy*. When the commons settles a change, the node
applies it as a **proposal**, not a write. That is what stops the two systems
from becoming two sources of truth for one fact.

---

## 3. Status — read this before touching anything

### stash (this profile) — `stashforge` branch

| Item | State |
|---|---|
| `docs/ALIGNMENT.md` | **written, uncommitted** |
| `docs/specs/2026-09-30-priority-shift-spec.md` (§6b) | **written, uncommitted** |
| `docs/GOAL.md` | **amended, uncommitted** — #11 retracted, #11a + #12–#16 added, M8 row |
| `docs/requirements.csv` | **committed** `b710ebcf2` — R074–R086 added (86 rows, 0 pre-existing fields changed) |
| `scripts/add_shift_requirements.py` | **committed** — regenerates the ledger addition, self-validating |
| stash#5850 (thumbnail alpha) | **DONE and committed on `main`**: `6d392659b` (fix) + `02d0d0476` (ledgers). Mutation: 10 killed, 0 survived, 1 exempt. Full suite green at 35 packages, exit 0. |

**stash#5850 is closed, and it was in the goal agent's Phase 2 queue.** It was
marked `planned` in `docs/UPSTREAM-ISSUES.md` on `main` while `coding-3` held
15 uncommitted files for it. It is now `closed` with the test named in
`docs/closed-issues.md`, and `python3 docs/check-issue-ledgers.py` exits 0.
**The goal agent should not redo it** — and should re-derive the roster's state
from the ledgers rather than from any document, including this one.

**Also pushed:** `m6-upstream-issues` is now on `origin`. It was 12 commits from
being unrecoverable (`git rev-list --count m6-upstream-issues --not --remotes`
-> 12; the 3461 total is upstream history the remotes already had, which is why
it looked alarming). Do not push it again.

### stash-box (the other profile) — `master` branch

Last updated by the stash-box profile: **2026-09-30 ~11:50 CEST**.

| Item | State |
|---|---|
| `docs/SPEC.md` §8 (commons half of the alignment) | **not yet written** — acknowledged, queued behind R074 |
| Requirement **R074** (receiving-end path guard) | **accepted as next task.** §4's measurement was **wrong and has been corrected in place** — see below. Scope is now concrete: 7 columns, 3 distinct rules, and one reachable SSRF hole. |
| Peer selection + `Cosine` (SPEC D2 step 2) | **built and committed** — `f5601107`, pushed to origin + forgejo |

#### What the stash-box profile was doing when this was written

Two unrelated threads, both in `~/code-local/go/stash-box` on `master`:

1. **`cmd/sdbimport` — bulk import from stashdb.org into the thinkcentre
   deployment.** A new binary, `internal/sdbimport/`, committed as `45ce957b`
   and pushed to origin + forgejo. ~1.23M records: tags 2,934 and studios 14,662
   are in; performers were at ~106k of 111,704 with 0 failures; scenes
   (1,104,004) run afterwards under a systemd unit `stashbox-import-chain` on
   thinkcentre. **Nothing here touches stash or the node side, and nothing here
   changes any shared contract.** Flagging it because the file is about the
   split and a second long-running job in this profile is worth stating.

2. **SPEC D2, "the identification board federates"** — the last unbuilt row of
   the §7.23 D1–D8 obligation. Spec and plan written
   (`docs/spec/feature-04-identification-federation.md`,
   `docs/plan/feature-04-identification-federation.md`, commit `1cf35daf`),
   steps 1–3 built. **This one DOES touch the shared contract** and is the
   reason §3's status table needs a new row — see §9 below.

#### The one thing the stash side should know about D2

D2's first three pieces are a **peer registry, taste-based peer selection, and a
broadcast that carries questions and candidate evidence only**. Steps 1–4 of 6
are committed (`c4f8fcdc`, `1d64e70a`, `f5601107`, `7704600c`).

**As of `7704600c`, F1 is enforced on both halves — and the second half was the
one that was missing.** The node side should assume the following are true and
should not re-derive them:

- **No content crosses the boundary, structurally.** The broadcast payload's
  field types are checked by reflection against a *closed* set — string, `[]string`,
  `uuid.UUID`, `int`, and one named struct. `[]byte`, `any`, and any array other
  than `uuid.UUID` fail the test by name. Closed rather than a denylist, because
  a denylist has to be updated when someone adds a type and forgetting is the
  failure mode.
- **…and also by value, which the structural half cannot do.** Every field is a
  `string`, and a string holds `/etc/passwd`. So `Question.Validate` rejects
  paths, URLs, UNC paths, traversal, literal internal IPs, and cloud metadata
  hostnames **before the question is sent**. It rejects rather than truncates:
  a silently shortened description returns answers to a different question.
- **A peer's answer is evidence, never a vote, and never a local row.** It lands
  in `identification_foreign_candidates`, not `identification_candidates`, so it
  cannot be voted on locally and cannot become a canonical link. The
  identification service's own rule is "a vote is EVIDENCE, not authority" — a
  remote suggestion is evidence squared and does not get a vote.
- **A peer's answer is scoped to one query and expires.** There is no
  `entity_id` column on the foreign-candidate table, so there is nowhere for a
  peer's opinion to attach to an entity and outlive the question.

**One asymmetry worth stating plainly, because a node will hit it.** The
value-level guard runs on the **sending** side. `Answer` has no equivalent
`Validate`, so a peer that sends back `Name: "/etc/passwd"` produces a *legal*
Answer on our side and the value is stored verbatim in the foreign-candidate
table. It is display-only and cannot reach the vote path (F2), so this is not a
hole — but it means **the guard protects this instance's users, not a peer's.**
Step 5 stores those answers and Step 6 surfaces them, and if either of them
renders a foreign candidate name anywhere it must escape it.

**What this means for the node side: a node that does not implement D2 receives
nothing.** There is no discovery protocol, so an unconfigured peer is simply
never asked. A node that later wants to answer identification queries implements
the same `Question`/`Answer` pair and nothing else.

---

## 4. The one thing stash-box owes stash, and why it is the first thing

`ALIGNMENT.md` §3 says nothing identifying crosses a node boundary. `stash`
enforces the **sending** half today, in the exporter's path guard, with a
positive control.

**The receiving half does not exist.** A one-sided guard is half a guard: the
exporter can be perfect and the commons can still acquire a path, a hostname or
an IP from a peer that does not have the guard.

**Measured on 2026-09-30, so the implementer does not rediscover it.** The
handoff originally recorded this as *"returns exactly one"* — `images.url` in
`04_image_tables.up.sql`. **That measurement is wrong, and it was re-measured
before any code was written.** A word-boundary grep across all 89 migrations for
a text/varchar/inet column named `*path*|*url*|*host*|*dir*|*file*` returns
**six**, not one:

| Migration | Line | Column | Table | Direction |
|---|---|---|---|---|
| `01_initial` | 33 | `url` | `performer_urls` | inbound reference |
| `01_initial` | 89 | `url` | `studio_urls` | inbound reference |
| `01_initial` | 109 | `url` | `scene_urls` | inbound reference |
| `04_image_tables` | 3 | `url` | `images` | **served** (this is the one the original note found) |
| `21_site_urls` | 5 | `url` | `sites` | inbound reference |
| `84_add_webhooks` | 27 | `target_url` | `webhook_endpoints` | **outbound dial** |
| `89_identification_federation` | 28 | `base_url` | `federation_peers` | **outbound dial** |

Two measurement traps in the original count, both worth recording because R074's
own guard will be a source-scanning test and would hit both:

1. **Substring matching is wrong.** The original grep matched `03_misc.up.sql`'s
   `director TEXT` as a path column, because `dir` is a substring of `director`.
   A guard written the same way will demand that the commons stop storing a
   person's director, and someone will "fix" that by weakening the guard.
2. **Quoted-name matching is wrong too.** `images.url` is written *unquoted*
   (`url VARCHAR NOT NULL`) while every other column here is `"url" varchar`. A
   regex that requires the quotes misses the one column the original note found,
   and the first version of the scanner would "pass" while the column it is
   supposed to police goes unwatched. **A guard that scans source needs a
   positive control for the scanner itself** — see §8 rule 4.

**The two `*outbound* columns are the interesting ones and the original note
missed them entirely.** A webhook `target_url` and a federation peer's
`base_url` are not records of where something came from — they are addresses the
box itself **dials**. A path in one of those is an SSRF primitive aimed at the
box's own network, which is a strictly worse outcome than storing a leaked path
in a metadata row.

**And one of the two already has the right guard.** `webhook/service.go`
`ValidateTargetURL` parses the URL, resolves it in DNS, and validates *every*
address it resolves to — which is the correct shape, because it closes the
rebinding hole rather than only checking the literal host.

**`federation_peers.base_url` has no such validation, and this is the concrete
hole.** Verified: `internal/queries/sql/federation.sql` ships
`CreateFederationPeer` and `UpdateFederationPeer`, so the write path is real,
while `internal/service/federation/` contains no `url.Parse`, no `LookupHost`,
no `Resolver` and no call to `ValidateTargetURL`. `peer.go` reads the column and
hands it to the dialer.

This is the **second** half of R074's receiving guard and the more urgent half,
because the sender-side guard stash already has cannot help here: nothing on
this side of the wire stops the box from being pointed at `127.0.0.1`,
`169.254.169.254`, or a `file://` URL by anyone who can write a peer row. The
fix is to reuse the *existing* webhook validator rather than write a second one —
same resolve-then-check-every-address shape, one implementation, two callers.

So the guard is **not** "no column may look like a path" — that fails on the
first run and gets deleted. It is three rules, and they are not the same rule
applied six times:

> 1. A path, a hostname, or an IP address must not be **storable** — not in
>    `*_urls.url`, not in `sites.url`.
> 2. An address the box **dials** (`target_url`, `base_url`) must be validated
>    by resolve-then-check-every-address, at **write time AND at dial time** —
>    a URL that validated on insert can resolve differently later, which is
>    exactly what `84_add_webhooks`' own comment already says.
> 3. A served URL (`images.url`) must not become a proxy for one: `file://`,
>    `\\host\share`, and a bare `/etc/passwd` are all accepted by a string
>    column, and all three are the attack.

**And every version needs a positive control** — a deliberately dirty value the
guard rejects. By the measurement above the existing schema is mostly already
clean, so a test that only scans the schema passes vacuously. That is the exact
trap the `stash` exporter fell into: its guard scanned the marshalled JSON for
values that *began* with a path, and only a deliberately dirty payload found it.
The same applies to a resolver that only sees a *mocked* resolver: rule 2 needs
a control where the hostname resolves to `127.0.0.1` and a second lookup returns
a different address.

---

## 5. Shared vocabulary, the short version

Full table in `ALIGNMENT.md` §2. The three that were ambiguous:

- **trust** — stash: *ballot weighting*. stash-box: *access levels*. They must
  not read each other's code. Reputation weights a ballot; it never unlocks
  anything.
- **replication** — a *content* replica on a node, vs a *metadata* row in the
  commons. Always qualify. The default of three applies to content; metadata is
  replicated unconditionally.
- **preservation** — a mesh property (≥N nodes), not a backup. A node that loses
  its disk has lost nothing if three peers still hold the scene.

---

## 6. Decisions taken 2026-09-30, with reasons

All three were the owner's, or taken by delegation and recorded here so they are
not re-argued.

**D1 — P2P becomes core, with opt-out on sharing.**
Retracts StashForge non-negotiable #11. The reason is not "plugins are awkward":
a capability the product is *about* cannot be something a user has to discover,
install and trust separately. **What survives:** the module boundary (own
`go.mod`, core does not import it, core does not shell out to it), every guard,
and the mutation gate. **Cost, stated plainly:** no longer removable by deleting
a directory, the binary grows, and a downloader defect is now a product defect.
Full reasoning in the spec's §6b.8; rule as `GOAL.md` #11a.

**D2 — content sharing is three states, not a boolean.**
`off` / `fetch_only` / `full`. A boolean cannot express the state a
privacy-conscious user actually wants. `fetch_only` is the default for a **new**
install (it has no publish path, so it leaks nothing) and `off` for an **existing**
one (switching content sharing on is a publish action nobody consented to).

**D3 — metadata replicates unconditionally; content replicates to N.**
"Do the same for the metadata" means the same three properties, not the same
number. Metadata is small and the commons is already the durable copy; content
is large and must be spread. N is configurable, default 3, and is a **view over
verified replica rows** — never a stored counter.

**D4 — R074 is three rules, not one rule applied N times** (stash-box profile,
2026-09-30). The measurement behind it found seven columns in three distinct
classes: *storable references* (`*_urls.url`, `sites.url`), a *served* URL
(`images.url`), and two *outbound dials* (`webhook_endpoints.target_url`,
`federation_peers.base_url`). Treating them as one rule is what produces a guard
that fails on first run and gets deleted. The outbound pair is the sharp end:
those are addresses the box dials, so a path in one is an SSRF primitive aimed
at the box's own network.

**D5 — reuse `webhook.ValidateTargetURL` for `federation_peers.base_url`; do
not write a second resolver validator** (stash-box profile, 2026-09-30). The
webhook service already has the correct shape — parse, resolve, validate *every*
resolved address, which is what closes the rebinding hole. A second
implementation of that rule is a second thing to keep in sync and a second thing
to get subtly wrong. The cost is a package dependency from `federation` to
`webhook`, which is accepted deliberately: it is a shared *validator*, not shared
business logic, and inverting it (a small `internal/netguard` both import) is the
better move if the dependency later reads badly.

**D6 — the `n<=0` guard in `SelectPeers` is pinned by a white-box source test.**
Its behaviour is identical with and without the guard, because the final
truncation produces the same empty result. Two attempts to kill the mutant
through the return value failed. The guard is kept because it makes "n<=0 is a
no-op" explicit rather than incidental, and the test stops a later edit from
deleting it as redundant — which is how it looks from the outside.

---

## 7. What is deliberately NOT happening

- No cross-repo import, no vendored copy, no shared Go module. They are two
  products that federate over a protocol.
- No scanner in the commons, no vote tally on a node, no P2P swarm vocabulary
  as a public protocol.
- No governance over personal tags and favourites — `stash`'s
  `TestNoResolverWritesASharedFieldDirectly` exists because *shared* fields need
  it, and it matches on receiver **type**, not method name.
- No negative reputation, no stored counters, no client-supplied access
  controls (region → the proxy, device class → a soft signal, MFA → the auth
  provider's problem).

---

## 8. Ground rules for whoever picks this up next

1. **Stay in your own repo.** Do not run the other tree's suite.
2. **A spec claim about the other repo is a hypothesis until measured.** Every
   cross-repo claim in these specs was checked with a grep or a run, and two
   that were not became four false rows in a requirements ledger.
3. **A COMMENT in a migration is a claim about the schema, not the schema.**
   Five times in this project, a migration's comment stated a constraint the
   DDL did not implement, and the comment was believed.
4. **A test that matches zero things passes.** Every guard added here needs a
   positive control, and every source-scanning test needs a control for the
   scanner as well as for the rule.
5. **Update this file at the end of every turn.** It is the only channel.
6. **A test derived from the code under test is self-defeating.** Added
   2026-09-30 after `TestUrlSchemesIsReachable` iterated the live list of URL
   schemes: deleting a scheme from the source deleted the case that would have
   noticed, and the suite stayed green. The same shape as rule 4, reached from
   the other direction — a test built out of the thing it is testing shrinks
   when the thing shrinks. **Spell the expected values out in the test.**
7. **A mutation that did not apply is not a surviving mutation.** Two mutations
   in this session reported SURVIVED when the edit had silently not matched the
   gofmt-aligned source, and two apparent survivors turned out to be killed.
   Confirm the file actually changed, or a green result means nothing in either
   direction. One apparent 60s hang was the full suite and the mutation run
   competing for the build cache.
