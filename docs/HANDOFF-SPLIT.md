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
| `docs/requirements.csv` | **amended, uncommitted** — R074–R086 added (86 rows, 0 pre-existing fields changed) |
| `scripts/add_shift_requirements.py` | new; regenerates the ledger addition, self-validating |
| stash#5850 (thumbnail alpha) | **fixed and green** — 2 of its 4 test files did not compile |

### stash-box (the other profile) — `master` branch

| Item | State |
|---|---|
| `docs/SPEC.md` §8 (commons half of the alignment) | **not yet written** — that profile's job |
| Requirement **R074** (receiving-end path guard) | **not yet written** — see §4 |

---

## 4. The one thing stash-box owes stash, and why it is the first thing

`ALIGNMENT.md` §3 says nothing identifying crosses a node boundary. `stash`
enforces the **sending** half today, in the exporter's path guard, with a
positive control.

**The receiving half does not exist.** A one-sided guard is half a guard: the
exporter can be perfect and the commons can still acquire a path, a hostname or
an IP from a peer that does not have the guard.

**Measured on 2026-09-30, so the implementer does not rediscover it:** grepping
every stash-box migration for a `text`/`varchar`/`inet` column whose name
contains `path|url|host|dir|file` returns exactly **one**:

```
internal/database/migrations/postgres/04_image_tables.up.sql:3:    url VARCHAR NOT NULL
```

`images.url` is a **served URL** on the stash-box host — legitimate, and what
the public API needs. So the guard is **not** "no column may look like a path";
that fails on the first run and gets deleted. It is:

> A path, a hostname, or an IP address must not be **storable**, and a served
> URL must not become a proxy for one — `file://`, `\\host\share`, and a bare
> `/etc/passwd` are all accepted by a string column, and all three are the
> attack.

**And the first version must include a positive control** — a deliberately dirty
value the guard rejects. By the measurement above the existing schema is already
clean, so a test that only scans the schema passes vacuously. That is the exact
trap the `stash` exporter fell into: its guard scanned the marshalled JSON for
values that *began* with a path, and only a deliberately dirty payload found it.

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
