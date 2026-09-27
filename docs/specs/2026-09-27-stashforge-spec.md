# StashForge — Specification

**One instance of Stash that is a private library manager and a self-governed
public curation site at the same time, with metadata sharing on by default and
off by choice.**

Status: spec. No code written yet.
Base: fork of `stashapp/stash` @ `b6b09dd5` (develop).
License: AGPL-3.0, inherited from upstream. This fork stays AGPL-3.0.
Upstream issues addressed: 850 open (`research/matrix.md` is the full map).

---

## 1. Why a fork, and what is actually missing

The request was "merge stash and stash-box into one thing that can be used
interchangeably as both". Verified against both source trees:

| | stashapp/stash | stashapp/stash-box |
|---|---|---|
| Language | Go 56% / TS 40% | TS 64% / Go 33% |
| Stars | 13,026 | 371 |
| Licence | AGPL-3.0 | MIT |
| Store | SQLite via `jmoiron/sqlx` | Postgres via sqlc |
| Users | **none — no users table in 88 migrations** | `model_user.go`, invite keys, mod audit |
| Edits | direct | `model_edit.go`, `model_draft.go`, vote submission |

**Stash has no user model.** `grep -i "CREATE TABLE.*user"` over
`pkg/sqlite/migrations/*.sql` returns nothing. Auth is a single shared
username/password in `internal/manager/config/config.go:45-46`, surfaced
through `pkg/session`, with a signed-URL escape hatch for `/scene/` streaming
devices. There is one implicit user: the instance owner.

**Stash-box is the collaborative half and is 371 stars.** It has users, drafts,
field-level edits, votes, invite keys, moderation audit and notifications. It
has no media library: no scanner, no playback, no ffmpeg pipeline, no plugin
system.

So "merge them" is not symmetric and not a refactor. The two halves are
disjoint in exactly the axis that matters. Forking stash-box would mean
rebuilding scanning, playback, scraping and plugins. Forking stash and porting
stash-box's *model* gives a working instance fastest, which is why that is the
base. §3 says exactly what is ported and what is rewritten.

Stash already speaks GraphQL to stash-box (`pkg/stashbox`). That client is the
integration point and is preserved.

### 1.1 Provenance, and a warning about issue #2792

The request cited `stashapp/stash#2792` ("Serve adult website"). **It is
closed** as of 2026-05-02, and the maintainer's comments in it argue against
this direction specifically. scruffynerf: *"Adding links/video to Stashbox
would be trivial, compared to add the user stuff to Stash. Don't use a hammer
when you want a screwdriver."* Asked to split the request up: *"Please don't
even encourage this."* The thread's own conclusion: *"If you are even
considering writing your own app, you might as well fork Stash."*

That last line is the whole of §2. The 2022 objections — no HTTPS, no 2FA, no
multi-user, no abuse mitigation — are addressed as first-class requirements
here rather than dismissed, because they were right.

The ed2k/BitTorrent idea in that thread is YurikaL's, and it is a P2P proposal
(nodes sharing metadata plus magnet/ed2k links, forwarded to an external
client), not a downloader. Scope for it is §7.

---

## 2. The one-sentence product

A Stash fork where the **library** stays local and private, the **metadata** is
a shared, self-governed commons, and **who may change the commons** is decided
by the users rather than an owner-admin.

Three modes, one binary, one database:

| Mode | Library | Metadata | Who can post |
|---|---|---|---|
| `private` | local, never leaves the host | local only | owner |
| `contribute` | local, never leaves the host | **shared by default**, opt-out per library | owner, publishes |
| `public` | local, never leaves the host | shared | anyone, per §5 governance |

In no mode does media leave the host. "Public" means the *metadata* is public
and the *files* are served only to authenticated users the owner has granted
library access to (§6.4). This is the distinction that makes the instance
serve a community without becoming a public file host, and it is the direct
answer to the security objections in #2792.

---

## 3. What is ported from stash-box, and what is not

**Ported as a model, rewritten in Go, in `internal/collab/`:**

- Field-level edit proposals with per-field values (`model_edit.go`)
- Draft state, accepted/rejected/merged transitions (`model_draft.go`)
- Vote arithmetic recomputed from the accepted-edit set, never a running
  counter — this is the fix for stash-box's own #743 and #9, where
  merged/deleted edits corrupt a maintained score
- Aliases as first-class rows with scoped uniqueness and a selected primary
  name — the fix for #726, #335, #318, #778, stash #5033, #2293
- Invite keys for registration without an admin in the loop
- Mod audit and notifications

**Not ported:** stash-box's TypeScript/React frontend, its Postgres/sqlc
layer, its deployment. Reimplementing its GraphQL schema in stash's
gqlgen pipeline is cheaper than bridging two schema languages, and it keeps one
frontend to maintain.

**Kept from stash:** the scanner, the ffmpeg pipeline, the player, the plugin
system (`pkg/plugin`, Go + jsRPC + hooks + tasks), the scraper framework, and
the `pkg/stashbox` GraphQL client.

---

## 4. Data model

New SQLite migrations, numbered after upstream's 86 (88 files in
`pkg/sqlite/migrations/`, highest numbered 86 — verify the number yourself
before writing, the count is not load-bearing but the collision is), in
`pkg/sqlite/migrations/`:

```sql
-- 1100_users.sql
CREATE TABLE users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT NOT NULL,              -- argon2id, never the legacy hashPassword()
  email         TEXT,
  created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  disabled_at   TIMESTAMP,                  -- NULL = active
  is_owner      INTEGER NOT NULL DEFAULT 0, -- exactly one row; set by migration
  reputation    INTEGER NOT NULL DEFAULT 0
);

-- 1101_invite_keys.sql
CREATE TABLE invite_keys (
  key_hash     TEXT PRIMARY KEY,            -- store the hash, never the key
  created_by   INTEGER NOT NULL REFERENCES users(id),
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at   TIMESTAMP,
  max_uses     INTEGER NOT NULL DEFAULT 1,
  uses         INTEGER NOT NULL DEFAULT 0,
  revoked_at   TIMESTAMP
);

-- 1102_user_sessions.sql
CREATE TABLE user_sessions (
  id         TEXT PRIMARY KEY,              -- random 256-bit, hashed at rest
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at TIMESTAMP NOT NULL,
  ip         TEXT, user_agent TEXT
);
CREATE INDEX idx_user_sessions_user ON user_sessions(user_id);

-- 1103_edit_proposals.sql
CREATE TABLE edit_proposals (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  target_type TEXT NOT NULL,                -- scene|performer|studio|tag|gallery|image|group
  target_id   INTEGER NOT NULL,
  field       TEXT NOT NULL,                -- title|details|director|studio_id|...
  old_value   TEXT,                         -- NULL = was unset; distinct from ''
  new_value   TEXT,
  rationale   TEXT,
  author_id   INTEGER NOT NULL REFERENCES users(id),
  created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  status      TEXT NOT NULL DEFAULT 'open'  -- open|accepted|rejected|superseded|withdrawn
    CHECK (status IN ('open','accepted','rejected','superseded','withdrawn'))
);
CREATE INDEX idx_edit_proposals_target ON edit_proposals(target_type, target_id, status);
CREATE INDEX idx_edit_proposals_author ON edit_proposals(author_id, created_at DESC);

-- 1104_proposal_votes.sql
CREATE TABLE proposal_votes (
  proposal_id INTEGER NOT NULL REFERENCES edit_proposals(id) ON DELETE CASCADE,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  value       INTEGER NOT NULL CHECK (value IN (-1, 1)),
  created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (proposal_id, user_id)        -- one vote per user, enforced
);

-- 1105_user_library_access.sql
CREATE TABLE user_library_access (
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  path_id    INTEGER NOT NULL REFERENCES paths(id) ON DELETE CASCADE,
  can_see    INTEGER NOT NULL DEFAULT 1,
  can_download INTEGER NOT NULL DEFAULT 0,
  granted_by INTEGER REFERENCES users(id),
  granted_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (user_id, path_id)
);

-- 1106_consent.sql
CREATE TABLE consent_preferences (
  user_id       INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  metadata_share TEXT NOT NULL DEFAULT 'opted-in'
    CHECK (metadata_share IN ('opted-in','opted-out')),
  decided_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  -- the version of the disclosure the user actually saw; an upgrade that
  -- changes what is published must bump this and re-prompt
  disclosure_version INTEGER NOT NULL DEFAULT 1
);
```

**Scores are computed, never stored.** `proposal_score(p) = Σ votes` is a view
over `proposal_votes`, and the *field's* effective value is the most recent
accepted proposal. A stored counter is the bug stash-box has open right now
(#743, #9); recomputing makes merged and withdrawn edits self-correcting.

```sql
-- 1107_proposal_score_view.sql
CREATE VIEW proposal_scores AS
SELECT p.id AS proposal_id, p.target_type, p.target_id, p.field,
       COALESCE(SUM(v.value), 0) AS score,
       (SELECT COUNT(*) FROM proposal_votes vv WHERE vv.proposal_id = p.id) AS voters
FROM edit_proposals p LEFT JOIN proposal_votes v ON v.proposal_id = p.id
WHERE p.status = 'open'
GROUP BY p.id;
```

### 4.1 Field vocabulary

Only these fields are proposable, because an open field is a stored-XSS and a
deserialisation surface:

```
scene:     title, details, director, studio_id, date
performer: name, disambiguation, details, gender, birthdate, country
studio:    name, details, parent_id
tag:       name, description
gallery:   title, details
image:     title, rating
group:     name, description, date, studio_id, rating
```

**Corrected 2026-09-27, during M2 step 2.4.** The original list was written from
assumption rather than from the schema, and four of its entries name columns that
do not exist:

| listed | reality |
|---|---|
| `scene.url` | URLs live in the `scene_urls` **join table** (scene.go:34), multi-valued and ordered. A single-valued `url` proposal would have to invent a column or silently drop every URL but one. Out of scope until the proposal model carries list semantics. |
| `studio.url` | studios have no `url` column. |
| `group.title`, `group.details` | a group has `name` and `description`; title/details are the **gallery's** fields, so this was a copy-paste. |
| `gallery.details` etc. | fine. |

Each of these would have been a runtime SQL error on the first proposal touching
it — a 500, discovered by a user, not a 422. `TestVocabulary_EveryFieldIsARealColumn`
in `pkg/sqlite` now reads the real columns with PRAGMA and asserts the two agree,
and it is mutation-checked by re-adding `scene.url` and confirming the test
fails. **A vocabulary derived from assumption is a stored-XSS surface with better
manners; read the schema.**

`new_value` is validated against the same validator the mutation path uses
(`validate_*` in `internal/api`); an unparseable value is a 422 at proposal
time, not a 500 at apply time.

---

## 5. Governance: quorum or moderator

> **Superseded in part, 2026-09-27 by owner decision.** The *acceptance
> arithmetic* below is replaced by Commons §8.1–8.5 (reproduced in §5.3): typed
> per-field proposals, reputation-weighted ballots, weight decay, and Sybil
> damping. Flat net-vote counting does not survive contact with a public
> instance — `MinVoters=3` is a speed bump, not a defence, because three fresh
> accounts cross it. Everything else in this section stands: the tables, the
> audit, §5.1, §5.2, and the sticky-rejection rule, which Commons did not
> specify and which is carried into Commons §8.1.1. The code in
> `internal/collab/governance.go` is not deleted — flat counting remains the
> moderator-only and migration path — but it is no longer the primary rule.

The rule as originally specified: **a proposal is accepted when
`Σ(+1) − Σ(−1) ≥ threshold`, or when a moderator approves it. Both paths
always exist; which is reachable is configuration.**

```go
// internal/collab/governance.go
type Policy struct {
    // QuorumThreshold is the net-vote count that auto-accepts a proposal.
    // Zero disables the quorum path entirely, leaving moderator-only.
    QuorumThreshold int
    // MinVoters rejects a quorum decision reached by too few distinct users,
    // so one account with three votes cannot pass a threshold of two.
    // Ignored when QuorumThreshold == 0.
    MinVoters int
    // AllowSelfAccept lets a proposal's author accept it. Off by default:
    // an author-vote is not a second opinion.
    AllowSelfAccept bool
}
```

Decision, in order, in `Evaluate(policy, votes, moderatorIDs, isAuthor)`:

1. `Withdrawn`/`Superseded` → rejected, terminal.
2. Moderator approval present → accepted.
3. `QuorumThreshold > 0` **and** `distinctVoters ≥ MinVoters` **and**
   `net ≥ QuorumThreshold` → accepted.
4. Otherwise open.

Rules that are not optional, because each was a real bug in a comparable system:

- **`MinVoters` defaults to 3.** Without it, quorum is not consensus; it is a
  threshold one account can cross by voting repeatedly from fresh accounts.
- **The author cannot accept their own proposal** unless `AllowSelfAccept`.
- **Rejection is sticky per (target, field, author).** An author whose proposal
  on a field was rejected does not get an infinite retry queue against the same
  field. Superseded-by-newer is a distinct terminal state from rejected.
- **Every state change appends to `collab_audit`** (§4.2) and notifies.

### 5.1 The owner is not an admin over content

The owner holds `is_owner`, which grants: user management, invite keys, library
paths, instance settings, and **moderator appointment**. It does **not** grant
the ability to overrule a quorum-accepted edit, and there is no UI for it. This
is the "admin having to get involved much" constraint, enforced in the resolver
layer rather than by convention: `EditResolver` has no owner override path.

### 5.2 Abuse, since #2792 raised it

- Rate limits on proposal creation and voting, per user and per IP
- A new account's first N proposals are held for moderation regardless of
  quorum (`NewAccountProposalHold`, default 5) — the Sybil-shaped hole in any
  vote-based system
- Per-user edit rate against a *single* target, to stop one user grinding a
  field
- `collab_audit` records every decision, so a bad actor is visible rather than
  inferred
- 2FA (TOTP) is required for `is_owner` and optional for others (§9)

### 4.2 Audit

```sql
-- 1108_collab_audit.sql
CREATE TABLE collab_audit (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  actor_id    INTEGER REFERENCES users(id),
  action      TEXT NOT NULL,   -- propose|vote|accept|reject|withdraw|grant|revoke|login
  target_type TEXT, target_id INTEGER, field TEXT,
  detail      TEXT             -- JSON; never a password, never a session id
);
CREATE INDEX idx_collab_audit_at ON collab_audit(at DESC);
CREATE INDEX idx_collab_audit_actor ON collab_audit(actor_id, at DESC);
```

Append-only. No UPDATE or DELETE path is ever written for it.

### 5.3 The adopted governance model (Commons §8.1–8.5)

Replaced in by owner decision 2026-09-27. Reproduced here so this spec stands
alone, and it is Commons' model rather than a summary of it.

**Typed field voting.** For each `(object, field)` the platform holds a set of
`FieldProposal`s, each with a value, a proposer (a user *or* an automatic
proposer), provenance, and a timestamp. The field's displayed value is the
winner by weighted vote, computed and cached. A field may be `locked`
(stash-box #213), pinning the value and refusing further proposals until a
steward unlocks it. A machine proposal is just another voter.

**Per-field vote scoping.** Agreeing about titles says nothing about tags.
Weight is earned and spent per field, so reputation in one area cannot be
cashed in to overrule another.

**Reputation from agreement, not volume.** Weight is a function of reputation;
reputation is agreement with *settled outcomes* over time. New accounts ramp as
their proposals are confirmed (the newcomer problem, #743 notes the current
method is flawed). Weight decays on repeated rejection. Many accounts voting
the same way is discounted as Sybil, and coordinated patterns are *flagged*,
not silently punished.

**The five-role model**, alongside the consent tier:

| Role | Browses | Votes | Curates | Extra |
|---|---|---|---|---|
| `public` (no login) | yes, within consent tier | no | no | consent-tier-visible items only |
| `subscriber` | yes | yes | no | tier-filtered items |
| `contributor` | yes | yes | yes | full tier filter |
| `steward` | yes | yes | yes | + moderation queue |
| `admin` | yes | yes | yes | + settings, roles, peer config |

A `public` role with no login is explicitly supported: read-only browsing of
consent-tier-visible items, with streaming, download and voting all gated. This
is what actually implements "public site" — §8 of this spec says how to host
one but never said who may read. The tier filter must be enforced **in the
query layer, not the UI**: an anonymous viewer is precisely the case where a
UI-only filter leaks.

**Scores are recomputed from the accepted-edit set, never read from a stored
counter.** Both codebases arrived at this independently; it is stated once here
and once in Commons §8.1.1. A stored tally is a thing to be able to be wrong,
and recomputing fixes stash-box #743/#9.

**Sticky rejection survives** and is carried into Commons §8.1.1 — see the note
at the head of this section.

**What this costs.** M2 is complete and green, and this makes its acceptance
arithmetic the secondary path rather than the primary one. The proposal table,
vote table, audit table, closed field vocabulary, and the apply path with its
compare-and-set are all still load-bearing; what is replaced is the decision
function. Nothing built in M2 is discarded.

---

## 6. Metadata sharing, opt-out

> **Extended 2026-09-27 by owner decision.** Commons §14.1's six-tier consent
> model is adopted, replacing the binary opt-in/opt-out this section starts
> from. A binary flag cannot express "visible but not redistributable", which
> is the state most of an amateur corpus is actually in. **This spec's
> `metadata_share` opt-in *default* is kept** — Commons defaults `unverified`
> items to private, and both are defensible, but an amateur corpus needs
> contribution to happen. §6.1's blocking disclosure is what makes an opt-in
> default honest rather than a leak.

**The adopted tiers** (Commons §14.1), each object carrying tier, attestation,
and audit trail:

| Tier | Meaning |
|---|---|
| `unverified` | Scanned locally; consent not established. Private by default. Not publishable. |
| `self_published` | The uploader asserts they are the creator and the subject consents. |
| `performer_claimed` | A verified performer claim covers it. Strongest tier. |
| `third_party_permitted` | Licensed/permitted by a studio or the subject under a stated basis. |
| `quarantined` | Reported or contested. Hidden everywhere, pending review. |
| `denied` | Takedown accepted. Permanently blocked by hash across all peers. |

Plus `redistribution_permitted`, deliberately independent of the tier: the tier
says *who is asserting*, the flag says *on what terms* (§7.1). A `denied` object
adds a content-hash blocklist entry that propagates to every peer and is checked
on import, scan, and match. Revocation propagates as a tombstone, never as a
vote, and is never outvoted by contribution points.

Enforced in the data layer, not the UI.

### 6.1 The default, and the disclosure

`consent_preferences.metadata_share` defaults to `opted-in`, per the owner's
requirement that local use contributes by default.

This is the one place where the default leaks information the user never
actively disclosed, so it is made conspicuous rather than silent:

- First run after upgrade shows a **blocking** disclosure screen naming exactly
  what is published: field names, values, performer names, tags, dates, and
  fingerprints. Two buttons, equal weight: **Share** and **Don't share**.
- The choice is per-library, and per-user, and is re-prompted if
  `disclosure_version` is bumped by a change to the published set.
- The settings page shows the current state as one sentence, and the "what
  exactly gets published" list is one click away, always.
- `metadata_share = 'opted-out'` **hard-stops the exporter**, server-side, in
  the same place the permission is read — not by omitting the user from a
  query, which a future refactor could undo.

### 6.2 What is published

Published: scene title, details, date, director, studio, tags, performer names
and aliases, galleries, images, custom-field values, and content fingerprints
(phash, oshash, perceptual + cryptographic) so the instance can be
de-duplicated against itself and its peers.

**Never published:** file paths, file names, filesystem sizes, timestamps of
last access, hostnames, IP addresses, internal URLs, the owner's account
details, and any private library's rows. §6.4 is the enforcement point.

### 6.3 The exporter

Runs as a stash **job** (`pkg/job`), so it is visible, cancellable and
rate-limited through machinery that already exists.

```go
// internal/collab/exporter.go
type Payload struct {
    Instance     string            `json:"instance"`
    SubmissionID string            `json:"submission_id"`
    Entries      []ExportEntry     `json:"entries"`
    EmittedAt    time.Time         `json:"emitted_at"`
}

type ExportEntry struct {
    TargetType string            `json:"target_type"`
    TargetID   int64             `json:"target_id"`
    Fields     map[string]string `json:"fields"`
    Fingerprints map[string][]string `json:"fingerprints,omitempty"`
}
```

The instance serves this over an authenticated read endpoint, and also pushes to
configured peer instances (§6.5). **The first sync after consent is a dry run
by default**: the payload is written to the job log and to a local file, and
nothing is sent until the user confirms. A silent first publish of a private
library is the exact failure this design exists to prevent.

### 6.4 Library access is not metadata access

Two separate grants, and this is load-bearing:

- **Metadata** may be published to the commons per §6.2.
- **Media** is served only to users holding a `user_library_access` row, and
  only if the owner is running a mode that serves files at all. Default: the
  instance is `contribute` — metadata flows, media does not.

A user can therefore contribute curation to a public instance while the files
stay on the owner's disk and behind an access grant. An ungranted user receives
404, not 403, for a scene path they can see in metadata — the existence of the
file is itself not disclosed.

### 6.5 Peer federation

Instances may federate over the same GraphQL surface they use to consume
stash-box, so a closed circle of friends' instances is a private commons.
Federation is opt-in per peer, with a per-peer key, and carries a signed
submission id. An instance may consume a peer's commons for *identification*
(the way stash uses stash-box today) without publishing to it.

---

## 7. The P2P downloader plugin

A **Stash plugin**, not core code — the owner's requirement is "easy to
install", and the plugin system is Stash's documented extension point. A
peer-to-peer client does not belong in the security-critical binary that holds
a private library.

**Corrected 2026-09-27, on reading the plugin host rather than assuming it.**
Stash's plugin system is not one thing, it is three transports
(`pkg/plugin/config.go:363`, `getTaskBuilder`):

| `interface` | transport | what runs | long-lived? |
|---|---|---|---|
| `js` | goja VM | a JavaScript plugin | yes |
| `raw` | `Exec`, stdin/stdout, process exits | a binary, one shot | **no** |
| `rpc` | `Exec` once, `net/rpc/jsonrpc` | a binary, persistent | **yes** |

`Config.Exec []string` (line 49) is what launches a program. The original text
said "the plugin is a separate Go module" in `pkg/p2pdownloader/`, which is
**not a Stash plugin at all** — it is a Go package inside the core tree, which
is the thing the owner's requirement rules out. Worse, the milestone's own seam
test (`TestP2PDownloaderIsNotImportedByCore`) would have passed on it, because
a package in the core tree that imports nothing from the core trivially
satisfies a grep. The test would be green and the milestone would describe
something that installs and never runs.

**What "easy to install" has to mean here, concretely:** a user drops the
plugin's directory (containing `source.json` and the built binary) into the
configured plugins path, or pastes a release URL into the UI's plugin
installer, and the downloader appears — on a **stock StashForge build, with no
recompilation and no new host capability**. The downloader is a **separate Go
module built to a static binary** and loaded with `interface: rpc`, so it is
native (the owner's stated preference, and the reason `js` is wrong: goja
cannot host a BitTorrent client), a genuine Stash plugin, and a long-lived
process — which a seeder requires, since DHT participation, inbound peer
connections, and transfers that survive across tasks all need a process that
stays up. `raw` cannot provide that.

The core tree never imports the module, and the milestone is not complete until
a test proves both that the core still builds with the module absent **and that
the core does not bundle or shell out to the downloader as part of itself**. The
second half is the half that matters: a grep for imports alone would pass on a
core that invoked a bundled binary, which is the failure mode actually worth
excluding.

It is also the worked example of "a plugin can do this", which is why the
library-integration half uses only what the plugin API already exposes: the
`tasks` surface runs the download and streams progress, and a finished file
enters the library the same way any other new file does — by being scanned.

Scope is what the owner asked for: everything a P2P downloader normally does.

| Capability | Detail |
|---|---|
| Protocols | BitTorrent (BEP 3/5/9/10/12/19/20/27/41/47), ed2k (eMule/eDonkey2000, eMule protocol, SHA1 hashes) |
| Peer discovery | **Kademlia DHT** (BEP 5), plus Mainline DHT, plus explicit `.torrent`/ed2k link ingest, plus local peer exchange |
| Magnet | Full magnet parsing, metadata fetch over BEP 9, peer discovery fallback |
| Transfers | Multi-connection per torrent, piece hash verification, resume across restart, sparse file support |
| Seeding | Seeding while serving, upload bandwidth cap, ratio target, super-seeding where the client permits |
| Scheduling | Global and per-torrent rate limits, active-torrent cap, sequential/rare-first piece order, queueing |
| Verification | Full and quick verify; a corrupt piece re-downloads rather than being skipped |
| Library integration | Completed files move into a configured stash path, get scanned, and are linked to the scene by fingerprint |
| Safety | Per-magnet allow/deny, path sanitisation on ingest (a crafted torrent name must not escape the target directory), content-type warning surface |

**Implementation stance:** the wire protocols are implemented in Go against the
BEPs, not delegated to an external binary. `anacrolix/torrent` is evaluated
first and preferred if it covers the surface; if it does not, the ed2k protocol
is hand-rolled regardless, since that library has no ed2k. The plugin then only
adds the Stash-specific half — fingerprint linking, path sanitisation,
scheduling integration.

**Path traversal is the first test written**, not the last: a `.torrent` whose
name is `../../etc/cron.d/x` must resolve inside the target root or the item is
rejected. A peer-supplied filename is untrusted input and this is the bug class
that gets a whole box.

### 7.1 The consent gate is core's, and never the plugin's

Adopted from Commons §5.18.1 and §14.1 during the 2026-09-27 reconciliation.
This is the most important thing the other spec contributes to this one, and it
is *more* load-bearing here than there, because the plugin is more powerful.

A tier check inside a third-party component is a tier check that can be buggy,
disabled, or hostile. So the split is strict:

- The plugin **requests**; core **decides**. The plugin calls
  `locator.propose(object, locator)`. Core evaluates the §14.1 tier table and
  either persists the locator or refuses it.
- The plugin has **no write path** to the consent tier, the object, or the
  database. It cannot write a magnet to a `denied` object even if it is
  malicious, and gets the same refusal a well-behaved one gets.
- A **hand-off to a client re-checks the tier at the moment of the action**,
  not at storage time, because consent can be revoked in between.

**`redistribution_permitted` is a separate gate from the tier.** A tier says
*who is asserting*; the flag says *on what terms*. `third_party_permitted`
without the flag is an item the user may watch and keep but not redistribute.
A full-featured downloader is exactly where that distinction gets tested,
because it *can* redistribute — the flag has teeth here that it would not have
in a hand-off plugin, which is the argument for having it.

**Rejected outright, and this is a deliberate asymmetry with Commons:** Commons
gates a P2P locator on `third_party_permitted` **plus** the flag, so an
amateur creator's own upload can never carry a magnet. StashForge's owner
requires a downloader that acquires material, including amateur material, which
is the whole point of a corpus no catalog describes. A gate that makes the
owner's requirement impossible is not a safety property, it is a contradiction.

StashForge's rule, instead: **a locator may be stored at any tier except
`quarantined` and `denied`, and hand-off to a client additionally requires
`redistribution_permitted`.** Storage and redistribution are different acts and
get different gates — storing a magnet in your own library is not
redistributing anything, and refusing it protects no one. The gate that does
load-bearing work is the one on *acting*, not on *recording*, and Commons
conflates them.

The tier model itself (`unverified`, `self_published`, `performer_claimed`,
`third_party_permitted`, `quarantined`, `denied`) and the `denied`-destroys-
locators rule are adopted unchanged, because they are what make a
non-`third_party_permitted` corpus holdable at all.

---

## 8. Hosting a public instance

Directly addresses the security objections in #2792, which were correct.

- **TLS is required to start in a public mode.** Startup refuses `public` over
  plain HTTP, and says why.
- **Rate limiting and lockout** on `/login`, per IP and per account, with
  exponential backoff. Brute-force mitigation is not optional for a public
  login.
- **2FA (TOTP)** required for the owner, optional for users
- **Registration is invite-only by default** in public mode (§4's `invite_keys`),
  so an instance is not open to signup floods
- **Session security**: 256-bit random ids stored hashed, `HttpOnly` + `SameSite=Lax`,
  `Secure` when served over TLS, absolute expiry, and rotation on privilege change
- **A first-run wizard** that makes the mode choice explicit and is the only
  place defaults are set — no "it defaults to public because nobody read the doc"
- **Backup before anything else**: the existing plugin-install backup
  (#6185) is generalised to run before any migration touching `users` or
  `collab_*`

---

## 9. Auth migration

Stash's existing auth is one shared username/password in the config file. The
migration must not break an existing install.

1. On first boot with no `users` row, create one `is_owner=1` user whose
   password is the existing configured password, re-hashed to argon2id.
2. Leave the config credential in place but stop using it for session
   validation; `config.HasCredentials()` continues to gate the signed-URL path
   for streaming devices that cannot send cookies.
3. `pkg/session` keeps its interface. A new `UserSessionStore` satisfies it,
   backed by `user_sessions`, so the ~20 existing call sites are unchanged.
4. `internal/api/authentication.go`'s `allowUnauthenticated` is extended by an
   explicit route table rather than a prefix scan, because a prefix scan is how
   a new public route becomes accidentally public.

### 9.1 Password hashing

`config.hashPassword` is the existing scheme. New passwords use **argon2id**
with parameters from config. `password_hash` records the scheme (`$argon2id$…`)
so verification is unambiguous and migration is possible. Existing hashes keep
verifying and are upgraded to argon2id on next successful login.

---

## 10. GraphQL surface

New, under the existing gqlgen schema (`graphql/schema/`):

```graphql
type User { id: ID!, username: String!, reputation: Int!, isOwner: Boolean!
            createdAt: Time!, disabledAt: Time }
type AuthPayload { sessionId: ID!, user: User!, csrfToken: String! }

type EditProposal { id: ID!, targetType: String!, targetId: ID!, field: String!
                     oldValue: String, newValue: String, rationale: String
                     author: User!, status: String!, score: Int!, voters: Int!
                     createdAt: Time!, myVote: Int }

type Query {
  me: User
  users(limit: Int, offset: Int): [User!]!
  proposals(targetType: String, targetId: ID, status: String): [EditProposal!]!
  communityFeed(sort: String, limit: Int, cursor: String): ProposalConnection!
}
type Mutation {
  register(username: String!, password: String!, inviteKey: String, totp: String): AuthPayload!
  login(username: String!, password: String!, totp: String): AuthPayload!
  logout: Boolean!
  propose(input: EditProposalInput!): EditProposal!
  vote(proposalId: ID!, value: Int!): EditProposal!
  withdraw(proposalId: ID!): EditProposal!
  moderate(proposalId: ID!, approve: Boolean!, reason: String): EditProposal!
  grantLibraryAccess(userId: ID!, pathId: ID!, canSee: Boolean!, canDownload: Boolean!): Boolean!
  setConsent(metadataShare: String!): Boolean!
}
```

`moderate` requires moderator or owner. `grantLibraryAccess` requires owner.
Neither `moderate` nor any resolver can edit a field directly — proposals are
the only write path to shared content, which is the governance invariant.

---

## 11. Testing

Per the standing requirement that a test must be able to fail, and mutation-
checked where it matters.

- **Unit**: quorum arithmetic, `MinVoters`, self-accept rejection, sticky
  rejection, the opt-out hard stop, path sanitisation, TOTP verify/replay.
- **Integration** (real SQLite, per-test file): registration→login→me; invite
  key single-use and expiry; propose→vote→threshold→field applied; moderator
  override; cross-field isolation; audit rows written; a non-granted user gets
  404 on media while seeing metadata; `opted-out` produces zero exported rows.
- **Regression guards**: existing stash tests must stay green unchanged. That is
  the cost of a fork and it is non-negotiable.
- **Mutation checks**: delete the quorum threshold, make the opt-out a no-op,
  remove the traversal guard. Each must fail a named test.
- **The upstream issue matrix** (`research/matrix.md`, 850 rows) is
  machine-checked: a test asserts the count of mapped issues equals the count
  of open issues, so the matrix cannot silently rot.

---

## 12. Milestones

Each is independently shippable and each ends with tests green and a tag.

| # | Milestone | Delivers |
|---|---|---|
| M0 | **Buildable fork** | `go build ./...` green, UI builds, upstream tests green, baseline recorded |
| M1 | **User accounts** | users/sessions/invites, auth migration, login UI, rate limiting |
| M2 | **Proposals & quorum** | edit_proposals, votes, governance, audit, proposal UI |
| M2.5 | **Governance v2** | replace the acceptance arithmetic with Commons §8.1–8.5: typed field proposals, reputation-weighted ballots, decay, Sybil damping, the five-role table, field locking. Sticky rejection carried over. |
| M2.8 | **Identity clustering** | Commons §7.1: `PersonCluster`, ONNX face embed, ANN assign, ambiguous bucket, consolidate, claim. |
| M3 | **Metadata sharing** | consent tiers, exporter, dry-run-first sync, public read endpoint, federation |
| M4 | **Public hosting** | TLS enforcement, 2FA, access grants, first-run wizard |
| M5 | **P2P downloader** | plugin: BitTorrent + ed2k + Kademlia, library integration, as a static binary over `interface: rpc` |
| M6 | **Upstream issues** | the 850-row matrix, worked capability by capability |

M0 is a prerequisite for everything and is where the work starts.

**Reordered 2026-09-27 by the reconciliation.** M2.5 and M2.8 are new and sit
*before* M3, not after it, and the order is not arbitrary. Clustering links a
person across sources, which is a distribution decision, so consent tiers must
exist first — a cluster merge that crosses a consent boundary has to be a
moderation event, and there is nothing to moderate against until §6's tiers
are in. Governance v2 precedes both because reputation-weighted voting is what
makes a public instance survivable, and a public instance is what M3–M4 are
for.

---

## 12.1 The amateur corpus, and the clustering it needs

**Added 2026-09-27 by owner decision**, adopting Commons §7.1, §7.4 and §7.5.
This is the largest gap between the two specs and it is a gap in StashForge, not
a difference of opinion.

The original ask was the amateur corpus: *link scenes with the same person even
when nobody has identified that person as a content creator.* StashForge has no
answer and its plan does not propose one. It inherits stash's scraper-only
identity model — a person is a row that a scraper found a name for — so for a
corpus with no scraper the person is fragmented across every appearance with no
thread, which is exactly the problem.

**The primitive: `PersonCluster`.** A set of face embeddings believed to be one
person, with no name required, no studio, and no credit. Stable id, optional
handle, optional avatar, a confidence. Meaningless to a user until it has three
or more appearances, at which point the UI offers to name it.

**The pipeline** (Commons §7.1): detect faces over generated keyframes → embed
with a local ONNX model → nearest-neighbour assign into the ANN index →
agglomerative consolidate with a guard against transitive over-merge → name, or
deliberately do not.

**The part that matters most is a state, not an algorithm.** The *ambiguous*
bucket is first-class, and an unnamed cluster is the **default**, not a
fallback. A system that requires a name before it will link a person cannot
serve a corpus where the names are the thing that is missing. This is the
single design decision that makes the rest work, and it is why §4.1's field
vocabulary has no way to express "performer" for such items: the item is linked
to a *cluster*, and the cluster is not yet a performer.

Two properties that are easy to get wrong and are specified in Commons:

- **A detector that cannot run is not a detector that found nothing.** A missing
  model, an unverified model, or an absent runtime must fail visibly with a
  reason. An empty result indexes a library as face-free, which is a silent,
  permanent-looking claim about the user's content that is in fact a missing
  file.
- **A model is untrusted input.** A face model is fetched over the network and
  then executed, so its SHA-256 is verified against a pinned digest *before any
  byte of it is parsed*, and a malformed expected digest is refused rather than
  compared.

**Scope adopted:** §7.1 clustering, §7.4 body/appearance similarity, §7.5
self-service performer claim (a performer can claim a cluster without an
account, via a signed request verified against their claim), §7.2 merge/split/
alias/disambiguate, §7.11 automatic career span.

**Consequence for the plan:** a new milestone between M2 and M3, because
consent tiers (§6) and clusters have to exist before material can be linked
across sources. Consent and identity are the same problem here — a cluster
merge that crosses a consent boundary is a moderation event, not a background
job.

---

## 13. Out of scope, and why

- **A Rust port.** A separate agent is doing that; duplicating it wastes both.
- **Replacing the React UI.** The fork keeps it; new pages follow its patterns.
- **The TUI/desktop-app idea from the earlier session.** Out of this spec.
- **Mobile clients.** The GraphQL surface is sufficient; no client is built.
- **Porting stash-box's frontend or Postgres layer** (§3).

---

## 14. Open decisions, flagged rather than guessed

> **Updated 2026-09-27.** Decisions 1–3 below are **closed** by the
> reconciliation (`docs/specs/2026-09-27-reconciliation.md`) and are recorded
> here rather than deleted, because a closed decision that leaves no trace gets
> re-argued. Decision 1: default mode is `contribute` — still open, and it is
> now more constrained, since §6's tier model makes it a per-object question
> rather than an instance-wide one. Decision 2: the flat quorum threshold is
> superseded by M2.5; `MinVoters` survives only as the moderator-only and
> migration path. Decision 3: `NewAccountProposalHold` is retained *and*
> strengthened — reputation ramping (§5.3) is a better answer to the same
> problem, so the hold becomes a floor under it rather than the mechanism.
> Decision 4 stands unchanged and is still the owner's.

**One new open decision, flagged rather than guessed:** whether M2.5's
reputation model needs a *field-type* weight table (titles vote differently
from tags) or a single global weight per user per field. Commons §8.1 scopes
votes per field but does not say whether the *weight function* is shared. The
cheap version is one weight function; the honest version is a type table. This
changes what gets built and is left to the owner at M2.5.

**One new open decision:** whether the M5 downloader plugin's consent gate
(§7.1) requires the plugin to be *signed* to be trusted with locator proposals
at all, or whether any installed plugin may request them and be refused
individually per request. Signing is safer; per-request refusal is what §7.1
currently specifies, and it is weaker.

1. **Default mode.** `contribute` is proposed: metadata shares, media does not.
   It satisfies "self-governed public site" and "my content stays mine", but a
   public instance needs its media served to be useful, and that is the owner's
   call. Default is a one-line config change in M4.
2. **Quorum threshold default.** 3 net votes with `MinVoters=3` is proposed. A
   3-person instance cannot reach it without moderator action, by construction.
3. **`NewAccountProposalHold`** default 5. Zero disables it, which reintroduces
   the Sybil hole.
4. **Whether accepted proposals can be reverted by their author.** Proposed: no.
   Reverting is a new proposal. Silent self-revert is a way to launder a bad
   edit past the audit trail.

Decisions 1 and 4 change what gets built and are the owner's; 2 and 3 have safe
defaults and are noted for the record.
