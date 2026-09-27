# StashForge — Implementation Plan

**Spec:** `docs/specs/2026-09-27-stashforge-spec.md`
**Base:** fork of `stashapp/stash` @ `b6b09dd5`, AGPL-3.0
**Tree:** `~/code-local/go/stash` (work here) → `~/code/go/stash` (publish on each milestone)

This document is executable by an LLM with no further context. Every step names
its files, the change, and the command that proves it. **Do not start a step
whose verification you cannot run.** A milestone is done when its verification
passes and a tag exists — not when the code looks finished.

---

## Ground rules

1. **Upstream tests must stay green, unchanged.** A fork that breaks upstream
   has silently become a different product. `go test ./...` on an untouched
   tree is the M0 baseline; every later milestone re-runs it and the count may
   only go up.
2. **Migrations are additive and numbered contiguously from 87.** Never edit an applied
   migration. Never renumber.
3. **SQLite is the only store.** No Postgres, no sqlc. The `users` table and its
   peers go in `pkg/sqlite/migrations/` like every other migration.
4. **Computed, never stored.** No score, vote total, or counter column. A
   counter is the bug stash-box has open as #743/#9.
5. **Read the handler before writing the resolver.** Two claims in the research
   spec were wrong when checked against source (see §11); a third is likely
   lurking.
6. **Commit per step**, message naming the change and its verification.
   `git push` to the mirror at each milestone, never mid-milestone.

### The M0 blocker, already diagnosed and fixed

`go build ./...` failed on a clean checkout with:

```
$ go/pkg/mod/github.com/enetx/http2@v1.0.26/client_priority_go127.go:12:11:
    s.DisableClientPriority undefined (type *http.Server has no field or method DisableClientPriority)
```

**This is transitive dependency skew, not toolchain skew** — the diagnosis in the
first draft of this plan was wrong, and the distinction changes the fix. The
failing file imports `github.com/enetx/http`, a vendored fork of the stdlib
`net/http` reached via `enetx/surf`, and the pinned `enetx/http v1.0.28` has no
`DisableClientPriority` field. `enetx/http2` ships a `go1.27`-tagged file
requiring it, so the build breaks only on go1.27+ and is fine on go1.26.

**Fixed in M0 by upgrading the dependency, not the toolchain:**

```
go get github.com/enetx/http@v1.0.29
```

Pinning the `go` directive downward would also have worked, but it leaves the
module family internally inconsistent for the next person to upgrade. Do not
edit anything under `$GOMODCACHE` — that is a local cache, and the change would
not survive `go clean -modcache` or appear in review.

The second M0 requirement is codegen: `internal/api/generated_*.go` is
gitignored (`.gitignore:22`) and the frontend's `src/core/generated-graphql.ts`
is generated too. `make generate-backend` and `pnpm run gqlgen` must both run
before the tree builds. See `docs/BASELINE.md`.

---

## M0 — Buildable fork

**Exit:** `go build ./...` green, `pnpm run build` green, upstream tests green,
baseline counts recorded in `docs/BASELINE.md`, tag `m0-buildable-fork`.

### Step 0.1 — Record what upstream actually does, before changing it

```bash
cd ~/code-local/go/stash
go build ./... 2>&1 | head -5        # expect the enetx error
cd ui/v2.5 && pnpm install --frozen-lockfile && pnpm run gqlgen && pnpm run build
cd ~/code-local/go/stash && go build ./... 2>&1 | head -5   # same error
```

`gqlgen` is required: `src/App.tsx` imports `./core/generated-graphql`, which
is **not committed** and is generated from `graphql/schema/**/*.graphql` plus
`graphql/client-schema.graphql`. Without it the frontend build fails with
`Could not resolve "./core/generated-graphql"`.

Write `docs/BASELINE.md` with the real numbers:

```bash
go test ./... 2>&1 | grep -cE '^(ok|---)' > /tmp/upstream_ok
go test ./... 2>&1 | grep -E '^FAIL' | head
ls pkg/sqlite/migrations/*.sql | wc -l        # expect 88 (highest numbered: 86)
git rev-parse HEAD
```

**Verify:** `docs/BASELINE.md` exists, contains the HEAD sha, the migration
count, and the upstream pass/fail counts. Commit: `docs: record the upstream
baseline before any change`.

### Step 0.2 — Make the toolchain skew go away

Check what the failing module actually needs, then pin to a toolchain that
satisfies it:

```bash
grep -n "^go " go.mod
go list -m github.com/enetx/http2
go env GOVERSION
```

Two acceptable fixes, in preference order:

- **A.** Pin the `go` directive in `go.mod` to the newest version that builds
  (likely `go 1.25.x`, matching upstream's `go 1.25.0` directive and below the
  go1.27 build-tag skew), and record the reason in a comment in
  `docs/BASELINE.md`.
- **B.** If A is impossible, add an explicit `replace` for
  `github.com/enetx/http2` at a version whose go1.27 file compiles, and say so.

Do **not** edit files under `$GOMODCACHE`. That is a local cache; the change
would not survive `go clean -modcache` and would be invisible in review.

```bash
go build ./... && echo "GO BUILD GREEN"
```

**Verify:** exit 0. Commit: `build: pin the go directive for enetx/http2 go1.27 skew`.

### Step 0.3 — Prove the tree is healthy end to end

```bash
cd ~/code-local/go/stash
go vet ./internal/... ./pkg/sqlite/... 2>&1 | head
cd ui/v2.5 && pnpm run check && pnpm run lint && pnpm run build
cd ~/code-local/go/stash && go test ./... 2>&1 | grep -E '^(FAIL|---.*FAIL)' | head
```

**Verify:** no `FAIL` lines, or every one of them is listed in
`docs/BASELINE.md` as a pre-existing upstream failure. Any *new* failure is
M0's job to fix or to record explicitly. Tag `m0-buildable-fork`, push the
mirror.

---

## M1 — User accounts

**Exit:** a second user can register with an invite key, log in, and be
distinguished from the owner; legacy installs keep working. Tag `m1-users`.

### Step 1.1 — Migrations

Create, in `pkg/sqlite/migrations/`, exactly as specified in spec §4:

| File | Contents |
|---|---|
| `1100_users.sql` | `users` |
| `1101_invite_keys.sql` | `invite_keys` |
| `1102_user_sessions.sql` | `user_sessions` + index |
| `90_collab_audit.sql` | `collab_audit` (it is needed to record account actions, so it ships in M1) |

Field types and constraints are verbatim from spec §4. `users.is_owner` gets
exactly one row; enforce with a partial unique index:

```sql
CREATE UNIQUE INDEX idx_users_single_owner ON users(is_owner) WHERE is_owner = 1;
```

```bash
sqlite3 /tmp/t.db < pkg/sqlite/migrations/1100_users.sql   # each file standalone
for f in pkg/sqlite/migrations/110*.sql; do sqlite3 /tmp/fresh.db < "$f" || echo "FAIL $f"; done
```

**Verify:** every file applies to an empty database with no error, and twice in
a row is a no-op (migrations are idempotent-checked by the runner, not by
`IF NOT EXISTS` — check the runner's expectations first with
`grep -n "migrations" pkg/sqlite/database.go | head`).

### Step 1.2 — Password hashing

New file `internal/auth/password.go`:

```go
package auth

// Hash returns an argon2id hash in PHC string format, prefixed "$argon2id$".
// The prefix is what makes verification unambiguous once the legacy
// scheme's hashes are still in the table.
func Hash(plain string) (string, error)

// Verify reports whether plain matches encoded. It accepts both the argon2id
// PHC format and the legacy config.hashPassword format, and returns an
// UpgradeNeeded bool when the match succeeded against the legacy scheme, so
// the caller can transparently re-hash.
func Verify(encoded, plain string) (ok bool, upgradeNeeded bool, err error)
```

Use `golang.org/x/crypto/argon2`. Parameters from config, defaults
`time=1, memory=64MB, threads=4` — record them in the encoded string so a
future parameter change does not invalidate existing hashes.

```bash
go test ./internal/auth/ -run 'TestHash|TestVerify' -v
```

**Verify:** `TestVerify_RejectsWrongPassword`, `TestVerify_UpgradesLegacyHash`,
`TestHash_EmbedsItsParameters`. All pass. Then mutation-check: make
`Verify` return `true` on any input and confirm a named test fails.

### Step 1.3 — Session store behind the existing interface

`pkg/session` already has the interface. Implement it over `user_sessions`
rather than changing its shape, so the ~20 existing call sites are untouched.

New `internal/auth/session_store.go`:

```go
// SessionStore issues and validates opaque session ids. Ids are 256 bits of
// crypto/rand, returned to the client and stored only as a SHA-256 hash, so a
// database leak does not yield usable sessions.
type SessionStore struct { db *sqlx.DB; ttl time.Duration }

func (s *SessionStore) Create(userID int64, ip, ua string) (plaintextID string, err error)
func (s *SessionStore) Validate(plaintextID string) (userID int64, err error)  // ErrInvalidSession
func (s *SessionStore) Destroy(plaintextID string) error
func (s *SessionStore) PurgeExpired(ctx context.Context) (int64, error)
```

Wiring: `internal/api/authentication.go` gains a `userResolver` that consults
the store first, then falls back to the existing config credential. Keep
`config.HasCredentials()` gating the signed-URL path unchanged — that is for
AirPlay/Chromecast devices that cannot send cookies.

**Verify:**

```bash
go test ./internal/auth/ ./internal/api/ -run 'TestSession|TestAuth' -v
go test ./... 2>&1 | grep -E '^FAIL' | head    # no NEW failures vs M0
```

Tests must cover: a valid id resolves to its user; a tampered id does not; an
expired id does not; the plaintext id is not present in the table.

### Step 1.4 — Login rate limiting

`internal/auth/ratelimit.go`. Per-IP and per-account, exponential backoff,
persisted in a table so a restart does not reset an attack in progress.

```go
// Limiter counts failed attempts in a window and reports whether the next
// attempt should be refused, plus how long to wait.
type Limiter struct{ db *sqlx.DB }
func (l *Limiter) Allow(ctx context.Context, account, ip string) (ok bool, retryAfter time.Duration)
func (l *Limiter) Record(ctx context.Context, account, ip string) error   // call on FAILURE only
func (l *Limiter) Reset(ctx context.Context, account string) error       // call on success
```

The success path must call `Reset`, or a user who mistypes five times then
types correctly is locked out of their own account.

**Verify:** `TestLimiter_LocksOutAfterN`, `TestLimiter_ResetsOnSuccess`,
`TestLimiter_SurvivesRestart` (write attempts, drop the in-memory state,
refuse again). This is a security control, so it gets the mutation check:
delete the `Record` call and confirm a test fails.

### Step 1.5 — GraphQL: register/login/logout/me

Extend `graphql/schema/` and regenerate with `go run ./cmd/gqlgen` (check the
Makefile for the exact target — `make generate` is likely). Schema per spec §10,
auth subset.

`cmd/stash/graphql` resolvers in `internal/api/`:

- `register` — invite key required unless the instance has no users yet (first
  run); verify the key's hash, its `uses < max_uses`, not expired, not revoked
- `login` — verify, then `Reset` the limiter, then `Create` a session
- `me` — the authenticated user
- `logout` — `Destroy`

**Verify:**

```bash
make generate && go build ./... && go test ./internal/api/ -run 'TestRegister|TestLogin|TestLogout|TestMe' -v
```

Required cases: an invite key is single-use (second use fails even on a
concurrent second registration — that is the race that matters); an expired key
fails; a revoked key fails; a first run with no users succeeds with no key; a
second registration with no key fails.

---

## M2 — Proposals and quorum

**Exit:** a user proposes a field edit, users vote, quorum or a moderator
resolves it, and the field is actually applied. Tag `m2-quorum`.

### Step 2.1 — Migrations

| File | Contents |
|---|---|
| `92_edit_proposals.sql` | `edit_proposals` + both indexes |
| `93_proposal_votes.sql` | `proposal_votes` with the composite PK |
| `94_proposal_score_view.sql` | the `proposal_scores` view from spec §4 |

Note the renumbering: M1 landed as migrations 87-90 (`users`, `invite_keys`,
`user_sessions`, `collab_audit`), and 91 is the moderator flag added for
the GraphQL `User` type, so proposals start at 92. golang-migrate
rejects a gap outright -- the original plan said 1100 and had to be renumbered
mid-implementation.
Keep one number per file, never reuse.

**Verify:** apply 92…94 in order to an empty database; the view exists and
returns a row for a seeded proposal.

### Step 2.2 — Governance, as a pure module

`internal/collab/governance.go`. **No database, no HTTP** — this is the part
most worth testing exhaustively, and it must be testable without either.

```go
type Decision int
const (
    DecisionPending Decision = iota
    DecisionAccepted
    DecisionRejected
)

type Policy struct {
    QuorumThreshold int
    MinVoters       int
    AllowSelfAccept bool
}

type VoteCount struct {
    Net      int
    Voters   int      // DISTINCT users
    SelfVote bool
    ModeratorApproved bool
}

func Evaluate(p Policy, v VoteCount) Decision
```

The `Policy` defaults live in one place, `DefaultPolicy()`, and are read from
config. Spec §5 fixes the semantics; implement exactly that order.

```bash
go test ./internal/collab/ -v
```

**Required test cases, each named for the rule it protects:**

- `TestQuorum_AcceptsAtThreshold` — net 3, threshold 3 → accepted
- `TestQuorum_RejectsOneBelowThreshold` — net 2, threshold 3 → pending
- `TestQuorum_MinVotersBlocksOneAccountWithManyVotes` — net 4 from 1 voter,
  `MinVoters=3` → **pending**. This is the Sybil case; it is the single most
  important test in the milestone.
- `TestQuorum_ZeroThresholdLeavesModeratorOnly` — every vote, still pending
- `TestModerator_OverridesPendingQuorum` → accepted
- `TestSelfAccept_RejectedByDefault` — author votes for their own proposal,
  net at threshold, still pending
- `TestSelfAccept_AllowedWhenConfigured` → accepted

Mutation-check all seven: comment out the `MinVoters` comparison and confirm
exactly `TestQuorum_MinVotersBlocksOneAccountWithManyVotes` fails. A guard that
kills no mutant is not a guard.

### Step 2.3 — The proposal write path

`internal/collab/proposal.go`, `internal/collab/store.go`. Targets and fields
are restricted to the vocabulary in spec §4.1 — a fixed map, not a parameter
interpolated into SQL, and a `field` outside the map is a 422, never a 500.

`ValidateValue(targetType, field, value) error` reuses the same validators the
mutation path uses, so a proposal cannot carry a value that would fail at apply
time.

Sticky rejection lives here: a rejected `(target, field, author)` is checked
before insert.

**Verify:**

```bash
go test ./internal/collab/ -v
```

`TestProposal_RejectsFieldOutsideVocabulary`,
`TestProposal_RejectsUnparseableValueAtProposalTime`,
`TestProposal_StickyRejectionBlocksRetry`,
`TestProposal_SupersededByNewerOnSameField`.

### Step 2.4 — Applying an accepted proposal

This is the step where the whole design can go wrong: applying must be
**idempotent and atomic**, and a value that became invalid between proposal and
apply must not corrupt the target.

```go
// Apply writes an accepted proposal's value onto its target inside tx. It is
// called from the job queue, not inline in the vote handler, so a slow apply
// does not hold a request open.
func Apply(ctx context.Context, tx *sqlx.Tx, p Proposal) error
```

- Re-read the target inside the transaction
- If the field's current value already equals `new_value`, no-op and report
  `applied=false` — re-apply is then free, which matters because M3's periodic
  reconciliation re-applies everything
- If the value is no longer valid, mark the proposal `rejected` with a reason
  rather than writing it
- Write one `collab_audit` row

**Verify:** `TestApply_IsIdempotent`, `TestApply_RejectsValueThatBecameInvalid`,
`TestApply_WritesExactlyOneAuditRow`, `TestApply_ConcurrentApplyWritesOnce`.

The last one matters: two workers applying the same accepted proposal must not
produce two audit rows or a double-mutate.

### Step 2.5 — GraphQL proposals + UI

Schema per spec §10. `moderate` requires moderator or owner. **No resolver edits
a shared field directly** — an audit test asserts there is no mutation path
from the GraphQL schema to a shared field's write function except through
`Apply`. That is the governance invariant, and it is worth a test that reads
the resolver registry.

UI: proposal list, proposal detail with votes, a vote button, a "propose edit"
dialog on scene/performer/studio/tag pages, and a moderation queue. Follow the
existing page patterns in `ui/v2.5/src`; do not introduce a new state library.

```bash
cd ui/v2.5 && pnpm run gqlgen && pnpm run check && pnpm run build
```

---

## M3 — Metadata sharing

**Exit:** an opted-in library exports; an opted-out one exports nothing, and
that is enforced where the permission is read. Tag `m3-metadata-sharing`.

### Step 3.1 — Consent

| File | Contents |
|---|---|
| `1108_consent.sql` | `consent_preferences` from spec §4 |

`internal/collab/consent.go`:

```go
// ShareOptedIn reports whether the user has consented. Absence of a row means
// opted-in (spec §6.1) — the default is deliberate, so the ABSENT case is the
// one that must be right.
func ShareOptedIn(ctx context.Context, q Queryer, userID int64) (bool, error)
```

The disclosure is blocking at first run and re-prompted when
`disclosure_version` increases. Ship the **exact published field list** in the
prompt, generated from the same constant the exporter uses — a disclosure that
is written by hand drifts from what is published, and then it is a lie.

**Verify:** `TestConsent_DefaultsToOptedIn`, `TestConsent_OptOutIsSticky`,
`TestConsent_DisclosureVersionChangeForcesReprompt`,
`TestDisclosureMatchesExporterFields` — the last one compares the prompt's list
against the exporter's field map, so they cannot diverge.

### Step 3.2 — The exporter

`internal/collab/exporter.go`, per spec §6.3. Runs as a stash job.

```go
// BuildPayload produces the export for one library. It is a pure read: it
// MUST NOT filter by consent, because the caller decides which libraries are
// in scope. Consent is enforced in the publish path so that the stop is in one
// place.
func BuildPayload(ctx context.Context, q Queryer, instance string, lib LibraryRef) (Payload, error)
```

Enforcement point, in `Publish`:

```go
if optedIn, err := ShareOptedIn(ctx, tx, userID); err != nil {
    return err
} else if !optedIn {
    // Hard stop, in the same place the permission is read. Not "omit the user
    // from the query" -- a refactor could drop that filter and silently resume
    // publishing. See spec §6.1.
    audit(ctx, tx, userID, "publish_refused_optout", ...)
    return ErrConsentOptedOut
}
```

**Verify:**

- `TestPublish_RefusedWhenOptedOut` — and assert the audit row says
  `publish_refused_optout`, so a refusal is observable, not silent
- `TestExport_ExcludesFilenamesAndPaths` — walk the marshalled payload and
  assert no key or value matches a path, a filename, a hostname or an IP. This
  is a string assertion over the actual JSON, because a struct-level assertion
  cannot see a field added later.
- `TestExport_DryRunIsTheDefault` — the first sync after consent writes to disk
  and sends nothing

**This is the milestone where a mistake is irreversible** — a published private
library cannot be unpublished from users' copies. Mutation-check the opt-out:
make `ShareOptedIn` always return true and confirm `TestPublish_RefusedWhenOptedOut`
fails.

### Step 3.3 — Serving the commons

An authenticated read endpoint over `Payload`, plus push to configured peers.
Federation is opt-in per peer with a per-peer key and a signed submission id.

**Verify:** `TestFederation_OptOutPeerReceivesNothing`,
`TestFederation_SignedIdRejectsTampering`, `TestCommons_UnauthenticatedReadIs404`.

---

## M4 — Public hosting

**Exit:** a public instance that is safe to expose. Tag `m4-public-hosting`.

### Step 4.1 — Mode enforcement

```go
// Mode is the instance's posture. See spec §2.
type Mode string
const (
    ModePrivate     Mode = "private"     // nothing leaves the host
    ModeContribute  Mode = "contribute"  // metadata shared, media not served
    ModePublic      Mode = "public"      // metadata shared, media per access grants
)
```

In `internal/api/authentication.go`, a public mode **refuses to start** over
plain HTTP, and says which config to change.

### Step 4.2 — 2FA (TOTP)

`internal/auth/totp.go`, `github.com/pquerna/otp`. Secret stored encrypted at
rest; required for `is_owner`, optional otherwise. A TOTP code is
**single-use within its time step** — replay of the same code inside the window
must fail, and that is the case worth a test.

**Verify:** `TestTOTP_RejectsReplayWithinTimeStep`, `TestTOTP_OwnerRequiredAtSetup`,
`TestTOTP_SecretNotReturnedAfterSetup`.

### Step 4.3 — Library access grants

Migration `1109_user_library_access.sql`, resolvers `grantLibraryAccess` /
owner-only. An ungranted user gets **404, not 403**, for a scene path they can
see in metadata — the file's existence is itself not disclosed (spec §6.4).

**Verify:** `TestAccess_UngrantedUserGets404Not403`,
`TestAccess_MetadataVisibleWhileMediaIsNot`.

### Step 4.4 — First-run wizard

A blocking screen that makes the mode choice explicit. The only place defaults
are set. A mode defaulting to public because nobody read a doc is the failure
this prevents.

**Verify:** a browser test that the wizard cannot be skipped, and that mode
`public` without TLS refuses to start.

---

## M5 — P2P downloader, as an installable plugin

**Exit:** a plugin that installs into a **stock, unmodified StashForge** the way
any other plugin does — a directory with `source.json` in the configured plugins
path, or a release URL the UI fetches — and can fetch a file over BitTorrent or
ed2k into a stash path and get it scanned and linked. No rebuild of the main
binary, no import from the core tree. Tag `m5-p2p-downloader`.

### Step 5.0 — Prove the seam BEFORE writing any transfer code

The whole point of "plugin" is that it can be removed by deleting a directory.
A downloader that only *claims* to be a plugin — because the types live in
`pkg/` and the core imports them — is core code, and the milestone is not done.
So the isolation is established and tested first, while there is nothing to
untangle:

```bash
cd pkg/p2pdownloader && go mod init github.com/stashapp/stash-plugin-p2pdownloader
```

Its own `go.mod` is what enforces it: a package in the core tree cannot import a
different module without the core's own `go.mod` gaining a `require` and a
`replace`, and both are visible in review.

**Verify, before implementing transfers:**

```bash
# the core must not know the plugin exists
go build ./... && go vet ./...        # passes with pkg/p2pdownloader absent
grep -rn 'p2pdownloader' --include='*.go' . | grep -v '^./pkg/p2pdownloader/'
# must print nothing
```

Named test `TestP2PDownloaderIsNotImportedByCore` asserts the grep above is
empty, and `TestP2PDownloaderHasItsOwnModule` asserts the directory has a
`go.mod` whose module path is not under `github.com/stashapp/stash/`. If either
fails, the downloader is core code and M5 is incomplete no matter how well the
transfer protocols work.

The plugin's manifest, per `pkg/plugin/plugins.go`'s `Plugin` struct:

```json
{
  "id": "p2p-downloader",
  "name": "P2P Downloader",
  "description": "Fetches files over BitTorrent and ed2k into your library",
  "url": "https://github.com/stashapp/stash-plugin-p2pdownloader",
  "version": "1.0.0",
  "tasks": [{"name": "download", "description": "Fetch a magnet, torrent or ed2k link"}],
  "settings": []
}
```

`tasks` is the integration surface: Stash's plugin task API already runs a
plugin operation in-process via jsRPC and streams progress, which is exactly
what a download needs, so the plugin needs no new host capability at all.

### Step 5.1 — Evaluate `anacrolix/torrent` first

Before hand-rolling anything:

```bash
go get github.com/anacrolix/torrent@latest
# Does it cover: Kademlia DHT, BEP 47 v2, super-seeding, sparse, rate limiting?
```

If it covers the BitTorrent surface, use it and hand-roll only ed2k, which no
Go library provides. Write down the decision in
`docs/decisions/0001-torrent-library.md` with the specific gap, if any. Do not
adopt a library for a surface you then spend the milestone reimplementing.

### Step 5.2 — Path sanitisation FIRST, before any transfer code

This is the test that is written before the feature, because a peer-supplied
filename is untrusted input and this is the bug class that owns a box.

```go
// SanitizeJoin resolves name under root and guarantees the result is inside
// root. A torrent file named "../../etc/cron.d/x" must resolve under root or
// be rejected -- never outside, and never via a symlink.
func SanitizeJoin(root, name string) (string, error)
```

Rules: reject absolute paths, reject any `..` component, reject NUL, reject
Windows reserved names and trailing dots/spaces, and verify the final path with
`filepath.EvalSymlinks` is still under the root.

**Verify, before implementing transfers:**

```bash
cd pkg/p2pdownloader && go test ./... -run TestSanitizeJoin -v
```

Cases: `../../etc/passwd`, `/etc/passwd`, `a/../../b`, a name with a NUL, a
symlink pointing outside the root, a name with a trailing dot, and a
legitimate nested path that must succeed. **No transfer code lands until these
pass.**

### Step 5.3 — BitTorrent

Per spec §7: metainfo parsing, magnet + BEP 9 metadata fetch, Kademlia DHT
(BEP 5) discovery, peer wire protocol, multi-connection, piece verification,
resume, rate limits.

### Step 5.4 — ed2k

eMule protocol: eDonkey2000 server + Kademlia (Kad) node list, eHash (MD4)
chunk hashes, the eMule extended handshake. No Go library provides this, so it
is written against the protocol description.

### Step 5.5 — Library integration, through the plugin API only

This is where the plugin boundary gets tested for real. A plugin cannot call
Stash's internals — it has jsRPC, the `tasks` surface, and whatever the host
deliberately exposes. So the integration is:

- On completion, move the file into a **configured stash path** the plugin was
  given as a setting. The plugin writes the file; it does not move it into place
  on the host's behalf, and it does not need to.
- Then **let the normal scanner find it.** Do not hand-insert scan rows, do not
  call the scanner's Go API, do not write to `files` — a finished download
  enters the library exactly the way a file copied in by hand does.
- Link to the scene by fingerprint (phasher/osher) the way stash already links,
  using the plugin API's query surface if the host exposes it, or by letting the
  scanner's own fingerprint match do it. Either is acceptable; reaching into
  `pkg/sqlite` is not.

The test that proves the boundary held is
`TestP2PDownloaderLibraryIntegrationUsesNoCoreImports`, which asserts the
plugin module's import graph contains no `github.com/stashapp/stash/...` path
outside the documented plugin interface. If a future change needs a new
capability from the host, that is a real finding about the host — it means M5
needs a host change, and the goal prompt's "the plugin needs no new host
capability" is what is under test.

**Verify:** `TestLibrary_CompletedFileIsScannedAndLinked`,
`TestLibrary_UnmatchedFileDoesNotAttachToNearestScene`,
`TestResume_SurvivesProcessRestart`.

---

## M6 — Upstream issue matrix

**Exit:** the 850-row matrix worked capability by capability, with each closed
issue traceable to the change that closed it. Tag `m6-upstream-issues`.

`research/matrix.md` maps every open issue in both repos to a capability id
(`research/taxonomy.py`, 91 capabilities + 6 documented non-goals).

**Step 6.0 — Make the matrix self-checking.** The mapping is a document, and a
document rots. Before working any capability, a test asserts the mapping is
complete against the live issue list:

```go
// The matrix must cover every open issue in both repos. A capability that
// closes issues nobody mapped is still worth doing; an issue nobody mapped is
// silently dropped, which is the failure this catches.
func TestIssueMatrixCoversEveryOpenIssue(t *testing.T) { ... }
```

It fetches both issue lists, and fails naming any unmapped `repo#number`. The
six documented non-goals (`X01`…`X06`) are allow-listed with their reasons.

**Then, per capability:** read the issues, implement, and record in
`docs/closed-issues.md` which change closed which numbers. A capability that
closes five issues says so once, in one row — the matrix's A.1 section already
identifies the multi-issue capabilities (scanner throughput alone is five).

Work M6 by dependency, not by issue count: the scanner and job-queue
capabilities (C15, C17, C20) unblock the most downstream fixes.

---

## Verification, per milestone

Every milestone ends with all four green:

```bash
cd ~/code-local/go/stash
go build ./...                                   # no output
go test ./... 2>&1 | grep -E '^FAIL' | head      # none
cd ui/v2.5 && pnpm run check && pnpm run lint && pnpm run build
cd ~/code-local/go/stash && git status --short    # clean
```

Plus the milestone's own test target, plus a mutation check on the security-relevant
guards added in that milestone.

Then:

```bash
git tag -a m<N>-<name> -m "<what it delivers, and how it was verified>"
rsync -a --delete --exclude '.git' --exclude 'ui/v2.5/node_modules' \
      --exclude 'ui/v2.5/build' ~/code-local/go/stash/ ~/code/go/stash/
cd ~/code/go/stash && go build ./... && go test ./... 2>&1 | grep -cE '^ok'
```

The mirror is verified by building and testing it, not by assuming the copy
worked.

---

## What is deliberately not in this plan

- The Rust port. A separate agent owns it; two ports of the same idea is
  duplicated work, not a safety net.
- Porting stash-box's TypeScript frontend or its Postgres layer. Spec §3.
- Any change to upstream's scanner, ffmpeg pipeline, or player beyond what a
  milestone explicitly names. Those are the parts that work; M6 touches them
  only where a mapped issue requires it.
- Mobile clients. The GraphQL surface is enough.
