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
scene:     title, details, director, studio_id, date, url
performer: name, disambiguation, details, gender, birthdate, country
studio:    name, details, url, parent_id
tag:       name, description
gallery:   title, details
image:     title, rating
group:     title, details
```

`new_value` is validated against the same validator the mutation path uses
(`validate_*` in `internal/api`); an unparseable value is a 422 at proposal
time, not a 500 at apply time.

---

## 5. Governance: quorum or moderator

The rule the owner chose: **a proposal is accepted when
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

---

## 6. Metadata sharing, opt-out

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

**What "easy to install" has to mean here, concretely:** a user drops the
plugin's directory (containing `source.json`) into the configured plugins path,
or pastes a release URL into the UI's plugin installer, and the downloader
appears — on a **stock StashForge build, with no recompilation and no new host
capability**. The plugin is a separate Go module, the core tree never imports
it, and the milestone is not complete until a test proves the core still builds
and still contains no reference to it. Shipping it as `pkg/p2pdownloader/`
*inside the core module* would satisfy the wording "a plugin" while being core
code with an extra directory; the separate module is what makes the claim true.

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
| M3 | **Metadata sharing** | consent, exporter, dry-run-first sync, public read endpoint, federation |
| M4 | **Public hosting** | TLS enforcement, 2FA, access grants, first-run wizard |
| M5 | **P2P downloader** | plugin: BitTorrent + ed2k + Kademlia, library integration |
| M6 | **Upstream issues** | the 850-row matrix, worked capability by capability |

M0 is a prerequisite for everything and is where the work starts.

---

## 13. Out of scope, and why

- **A Rust port.** A separate agent is doing that; duplicating it wastes both.
- **Replacing the React UI.** The fork keeps it; new pages follow its patterns.
- **The TUI/desktop-app idea from the earlier session.** Out of this spec.
- **Mobile clients.** The GraphQL surface is sufficient; no client is built.
- **Porting stash-box's frontend or Postgres layer** (§3).

---

## 14. Open decisions, flagged rather than guessed

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
