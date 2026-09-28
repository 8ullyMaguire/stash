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

**Status: complete.** Done:

- `graphql/schema/types/proposal.graphql` — `EditProposal`, `EditProposalInput`,
  four mutations (`propose`, `vote`, `withdraw`, `moderate`), three queries
  (`proposals`, `moderationQueue`, `pendingMyVote`).
- `ui/v2.5/graphql/proposal.graphql` + `src/components/Proposals/Proposals.tsx`
  + `src/core/StashService.ts` — the UI, on its own top-level route.
- `internal/api/resolver_mutation_proposal.go` — every mutation goes through
  `collab.Proposer` or `collab.Applier`. `moderate` records the decision and
  lets `Apply` do the write, so the moderator path and the quorum path converge
  on one writer and one audit row.
- `pkg/sqlite/stashforge_collab_proposal_store.go` — **the adapter that did not
  exist.** `collab.ProposalStore` had no implementation anywhere in the tree, so
  the whole governance layer was unreachable from the application and only its
  unit tests ever ran. Nothing in M2 caught this because M2's tests exercised
  `collab` against fakes.
- Store methods `FindFiltered`, `FindPendingFor`, `FindRejectedByAuthor`,
  `MyVote`; manager fields for all four collab stores.

Two things the spec did not say and the code forced:

1. **The audit test needed receiver types, not method names.** The first version
   flagged `SceneMarkerStore.UpdateTags` — a user tagging their own bookmark,
   which is personal state, not a claim about shared content. Name-only matching
   conflates the two, and a test that fails on legitimate code gets deleted,
   taking the real check with it. Stores are now matched by receiver type, with
   an explicit `sharedFieldStores` list so "is this shared?" is a stated
   judgement rather than an accident of naming.
2. **`TestWritePathDetectorCatchesAViolation` is a meta-test and it earned its
   place immediately.** The scanner was silently matching nothing: `stripImports`
   used an anchored `(?s)` pattern, so on a file with no `import (` block it ate
   from the start of the file to the last line beginning with `)`. And the
   declaration regex consumed the trailing `Store` while the comparison looked
   for `SceneStore`. The test passed, having verified nothing. The meta-test
   asserts the scanner's behaviour on snippets with known answers **in both
   directions** — bad snippets must be caught, and `SceneMarkerStore.UpdateTags`
   must not be.

3. **The UI is React, not Svelte.** The plan said "follow the existing page
   patterns in `ui/v2.5/src`" and also said Svelte; the tree is React +
   react-bootstrap + Apollo. The pattern reference was right and the framework
   name was wrong.
4. **`en-GB.json` is the base catalogue, not `en-US.json`.** `en-US` is a sparse
   override that falls back to `en-GB`, so a key added only to `en-US` renders
   as the raw id in every locale. `loading` is `loading.generic`; `cancel` did
   not exist and is now `buttons.cancel`.

Verified: `pnpm run validate` clean (biome lint, `tsc --noEmit`, biome
format); `vite build` clean.

```bash
cd ui/v2.5 && pnpm run gqlgen && pnpm run validate
node node_modules/vite/bin/vite.js build
```

UI: proposal list, proposal detail with votes, a vote button, a "propose edit"
dialog on scene/performer/studio/tag pages, and a moderation queue. Follow the
existing page patterns in `ui/v2.5/src`; do not introduce a new state library.

```bash
cd ui/v2.5 && pnpm run gqlgen && pnpm run check && pnpm run build
```

---

### Step 2.4a — Governance v2 (milestone M2b), replacing the acceptance arithmetic

**Spec:** §5.3 (adopted from Commons §8.1–8.5)
**Exit:** a proposal settles by reputation-weighted per-field vote, with decay
and Sybil damping; the five-role table governs what each role may do; flat
net-vote counting survives only as the moderator-only and migration path.

What already exists and is kept: the proposal table, the vote table, the audit
table, the closed field vocabulary, and the apply path with its
compare-and-set. **Nothing built in M2 is discarded.** What is replaced is the
decision function, so this is a smaller step than it looks.

```go
// internal/collab/governance.go -- extended, not replaced
type Policy struct {
    // The M2 fields remain, and remain reachable: QuorumThreshold == 0
    // disables the flat path, leaving moderator-only. That configuration
    // is still a supported mode, so the arithmetic is not deleted.
    QuorumThreshold int
    MinVoters       int
    AllowSelfAccept bool

    // Added in M2b.
    Reputation ReputationSource   // agreement-with-settled-outcomes
    Decay      DecayPolicy        // weight loss on repeated rejection
    Sybil      SybilPolicy        // correlated-vote damping
    Roles      RoleTable          // public/subscriber/contributor/steward/admin
}
```

**Order within the step, and why:** roles before weighting. The five-role table
is what makes a public instance safe, and a public instance is where weighted
voting starts to matter — so the access model is in place before the
economics that depend on it.

**Status: governance core done.** `internal/collab/roles.go` (the five-role
table) and `internal/collab/weighting.go` (reputation, decay, Sybil damping,
tally, correlation flagging), with 151 test cases.

**Persistence done — M2b is complete.** Migration 95 (`field_reputation`),
`collab.ReputationStore`, the SQLite row store and adapter, and the instance
wiring in `manager.go`/`init.go`.

Three decisions in the persistence layer:

*Reputation is per (user, target_type, field),* not per user. A user reliable
about performer metadata has not been vetted about anything else, and a single
global score transfers trust between fields that have nothing to do with each
other. `TestReputationStore_IsPerField` pins it.

*`reputation` is CHECK (>= 0) and the store clamps at zero on a debit.* Two
mechanisms were specified for a user who keeps losing — decay and negative
reputation — and only one is the right one. Decay discounts a vote; a negative
score is a second, stronger mechanism that removes a person from governance
outright, and one the UI cannot render because nothing displays it. "Not yet
trusted" has to stay visibly different from "silenced".

*`rejections` is a separate column from `reputation`,* because they answer
different questions: reputation feeds the weight, rejections feed decay.

The clamp is mutation-tested. Removing `MAX(reputation - 1, 0)` produces a
CHECK constraint failure, which is precisely the user-visible fault the clamp
exists to prevent — a losing streak surfacing as a server error rather than as
"you have been overruled here a few times". The migration's own tests were
mutation-tested the same way: dropping the CHECK and shrinking the primary key
to `user_id` alone both fail loudly.

One coupling nearly introduced. `sqlx.StructScan` needs `db:` tags, and the
first fix put them on `collab.FieldStanding` — which would make the pure
governance package depend on the database's column names. That is a small
coupling until a column is renamed, at which point a schema change stops
compiling in the package that was supposed to know nothing about the database,
and the fix has to be made in the governance layer. `fieldReputationRow` in the
store follows the `proposalRow` pattern instead: the row carries the tags, and
`toStanding()` converts. The row struct has every column the table has, not
just the ones the service wants, because the SELECT is `table.All()` and a
missing destination fails at runtime rather than at compile time.

The index test deliberately does NOT assert a query plan. The first version did
and failed twice over: `EXPLAIN QUERY PLAN`'s first column is `id`, not
`detail`, and more importantly SQLite will correctly prefer a scan on a
three-row table whether or not the index exists. A test that asserts the
planner's choice is a test about data volume, not about the schema — it would
pass with the index dropped. It asserts the index exists, which is the part
that is a property of the migration.

**Role wiring done.** `roleOf` in
`internal/api/resolver_mutation_proposal.go` maps a user row to a role, and
both moderation gates now ask `collab.Can(roleOf(u), collab.CapModerate)`
instead of M2's `isModerator(u)`.

Roles are **derived** from the existing `is_owner`/`is_moderator` columns, not
stored. A stored role would be a second source of truth against two columns
that already exist, and the two would drift on the first promotion — and the
drift would be invisible, because both would still be plausible.

`roleOf` delegates to `collab.ResolveRole` rather than repeating the mapping.
The first version reimplemented all four branches; that is two places to change
one decision, which is how the subscriber/contributor argument ends up
recorded twice and meaning two things.

The nil case is the only thing `roleOf` adds, and it is the case that must not
be folded in: `ResolveRole(false, false)` returns CONTRIBUTOR, so passing
`(false, false)` for an anonymous visitor would let an unauthenticated
request propose edits.

**The equivalence test is the point of this commit.**
`TestRoleOfAgreesWithM2ModeratorPredicate` runs the new check and M2's
predicate side by side over every row the database can represent and fails if
they disagree. Re-expressing a permission check in a new vocabulary is worth
nothing if the people who could moderate before cannot moderate now, and the
failure mode of getting that wrong is a moderator finding out during an
incident.

It was also **mutation-tested**: flipping `RoleSteward.Moderate` to false makes
it fail with

    roleOf(moderator).Can(CapModerate) = false, but M2's isModerator = true;
    the moderation gate changed for this user

naming the affected user rather than just reporting a boolean mismatch. A
permission test that has never been observed to fail is not evidence that the
permission is unchanged — it is evidence that the test agrees with whatever the
code currently does. The file was restored and re-verified clean immediately
after.

The decision function is now done: `internal/collab/evaluate_weighted.go`.
`Evaluate` was EXTENDED rather than replaced, with `Policy.Weighted` at its
zero value by `DefaultPolicy`, so M2's behaviour is unchanged by its arrival.
`Weighted.Threshold == 0` falls through to the flat path, and
`QuorumThreshold == 0` keeps moderator-only. Every combination is a supported
mode, which is what makes the migration possible at all.

A fourth bug the tests caught, and the same shape as the second one: excluding
the author's ballot also decremented `Voters`, because the filter was applied
before tallying. The effect is that `MinVoters` becomes a tool for excluding
authors from their own proposals' quorum — a proposer with a popular correction
would need one more supporter than a proposer nobody agrees with, which is
backwards. The ballot is now excluded from the arithmetic and not from the
headcount. Both this and the `Tally` defect are a filter applied to the wrong
collection, where the intent was to move a sum and not a count.

A fixture worth noting: the first version of `TestEvaluateWeightedThreshold`
wrote a comment working out why its total was 20000, and was wrong about the
damping (the `-1` breaks the run, so both `+1` ballots are undamped and the net
is 10000). A fixture whose arithmetic you have to argue about is one that will
be argued about again, so the test now pins `Tally.Net` directly — otherwise a
change to the damping constants would silently move every threshold in the
table and the assertions would still pass.

**Verify:** `go test ./internal/collab/ -run 'TestWeight|TestDecay|TestSybil|TestRole'`
must be table-driven over the full cross-product, not a handful of cases. The
flat path's existing tests must still pass unchanged — that is the proof it is
a fallback and not dead code.

Three decisions the tests forced, all of which were wrong before they were right:

1. **The basis-point scale was off by 4x.** The first constant made reputation
   100 return the floor (2500) rather than 10000, so *every* voter looked like a
   newcomer and the weighting did nothing. It passed the obvious checks. The
   scale is now fixed by an explicit property — `ReputationToBasis(100) ==
   10000` — so a fresh voter is exactly 1.0 and an M2 quorum threshold carries
   over without rescaling. A magic constant that is nearly right is worse than
   one that is obviously wrong, because the failure is silent.
2. **Damping applied to the wrong neighbour.** The group test read
   `basis[i-1%len(basis)]`, which Go parses as `i-(1%len)` and not `(i-1)%len`,
   so the "is this ballot the same as its group" test was meaningless. The group
   position already encodes sameness, so it now reads `position > 1` directly.
3. **A test asserting the wrong sign.** `TestTallyWeightedNotCounted` asserted
   `Net > 0` for a ballot order where the established user votes *against* two
   newcomers — so it asserted nothing about weighting and failed. It now asserts
   the weighted total against the flat count, which is the property the test
   exists to check.

The minority-dissent case is worth keeping: damping the minority direction would
make a coordinated bloc *cheaper* to execute, the exact inverse of the intent.
`TestSybilDoesNotDampMinorityDissent` pins that.

### Step 2.4b — Identity clustering (milestone M2c)

**Spec:** §12.1 (adopted from Commons §7.1, §7.4, §7.5)
**Exit:** a `PersonCluster` links appearances with no name, no studio, and no
credit, and an unnamed cluster is the default rather than a failure state.

**Step 2.4b.0 — the two tests that must exist before the model does.**

1. **A detector that cannot run fails visibly.** Missing model, unverified
   digest, absent runtime: each fails *with a reason*. An empty result indexes
   a library as face-free, which is a silent, permanent-looking claim about the
   user's content that is in fact a missing file. This is the single most
   dangerous behaviour in the feature and it is a one-line mistake to write.
2. **A malformed expected digest is refused, not compared.** A truncated digest
   can never match, so a check that compares rather than refuses is refusing by
   accident rather than by decision. Test the malformed case explicitly.

**Step 2.4b.1 — the sample budget test. Done** (`internal/cluster/sample_budget.go`).

A per-file sample budget is a ceiling on work, and when a file needs more
samples than the budget allows they are spread evenly from the first frame to
the last — *not* the first N of the requested interval, and not that interval
at a coarser stride. Both of those concentrate the budget at the start, so a
face in the last act of a long video is never looked at. The test asserts where
the **last** sample lands; asserting the sample count cannot distinguish a
correct plan from one that covered the first minute of a three-hour file and
stopped.

**The spread includes the final frame, and the test found that it had to.**
The obvious formulation — `i * duration / budget` for `i` in `[0, budget)` —
samples the *intervals* and leaves the tail outside all of them. With 5 samples
over 1000 frames it puts the last sample at **800**, leaving the final fifth of
the file unwatched while looking textbook-correct: even gaps, the right count,
no error. The implementation now spreads over `[0, duration-1]` inclusive, so
the last sample is always the last frame.

The cost is stated in the code rather than hidden: the stride becomes
`(duration-1)/(budget-1)` instead of `duration/budget`, so coverage is a hair
less even in the interior — `0, 249, 499, 749, 999` rather than
`0, 250, 500, 750, 1000`. Buying the last frame with one frame of interior
unevenness is the right trade for a budget whose entire purpose is not missing
the end of a file.

`budget == 1` is a special case, handled before the endpoint spread: a single
sample goes at frame 0, which is the one frame guaranteed to be worth looking at
(a face, or a post-credit title card). Spreading one sample across a range would
mean picking a midpoint and gambling on the film having a face in its middle.

**Mutation-tested against both defects the plan names.** Taking the first N
frames, and using the `i*duration/budget` even stride, both fail with the
diagnosis in the message:

    last sample is frame 4 of 1000, want 999 (the final frame); the budget must
    reach the END of the file. A plan whose last sample is near the beginning
    never looks at the last act, and reports no error doing it

`TestPlanSamplesIsNotTheFirstNFrames` and
`TestPlanSamplesIsNotAUniformSubsetOfTheInterval` name the two wrong answers
explicitly rather than relying on the end-to-end assertion to catch them.

One thing this cost: the first mutation attempt failed to compile, so it scored
as neither kill nor survivor. A mutation that does not build kills no test and
must not be reported as one. The first `PlanSamples` was restored and the
mutation reapplied cleanly before the result was believed.

**Step 2.4b.2 — the over-merge guard. Done** (`internal/cluster/overmerge.go`).

Commons §7.1 step 4. Pairwise similarity is not transitive, so a face is
measured against the cluster's **centroid**, not against any member. A is within
T of B and B is within T of C says nothing about A and C, and greedy
nearest-neighbour clustering merges A and C on B's authority — worse, the first
merge is the most confident one, so the wrong anchor is the one a casual human
review would have approved.

**Three failure modes, three remedies, three error values.** A distance
failure means the face belongs to someone else and the caller should look
elsewhere; a strand means the *cluster* is already two people and must be
split; a double claim means the index is corrupt. Conflating any two sends the
user the wrong action.

**The split is deliberately not gated by the merge threshold.** This cost three
separate passes and the code says why at each site, because the instinct to
reuse the number is strong and wrong every time: the threshold bounds what the
ENGINE may conclude, and a human asking to peel a face off is not the engine
concluding anything. A guard that can only refuse to grow is a one-way ratchet,
and it makes "reject a bad merge" cheaper than "correct a bad merge", so people
merge first and split later.

**A cluster of two halves cannot be built through the guarded path** — that is
the guard working. `forceCluster` plants one deliberately, so the behaviour can
be tested against state that arrived by import or by a threshold change.

**Seven mutations, five of which survived the first pass** and now all die:

| Mutation | Why it survived |
|---|---|
| strand check removed | a rule that can only make the code *more cautious* is invisible to a suite |
| face joins two clusters | the test asserted `err != nil`, and the candidate then failed on the *distance* check instead — green agreeing with a broken implementation for the wrong reason |
| split adjacency re-gated on T | the ratchet, above |
| midpoint instead of widest gap | the test took the 1-vs-N shortcut and never consulted the gap |
| singleton free-pass removed | looks like a no-op, but then no cluster is ever created, and an empty table reads as "no faces found" — the same shape as a missing model file |

Three for three in this milestone of *a missing refusal is invisible, a missing
permission is loud*, after the zero-`Detector` guard in 2.4b.0. When a mutation
survives the fix is a new test, never a weaker mutation.

**A sentinel conflating "not computed" with "computed as zero"** was the one real
implementation bug, and it is worth naming because the number is innocent:
`bestGap` started at `-1` and a `len(keep) > 1` loop left it untouched, so
`bestGap <= 0` could not tell "no gap found" from a genuine gap of `0.0` — which
is what three members at the same position produce. Now an explicit `haveGap`,
and the check moved after the case the sentinel is unreachable for.

**Step 2.4b.3 — the assign decision. Done** (`internal/cluster/assign.go`).

Four outcomes: new cluster, join one clearly, **ambiguous**, or already seen.

**Ambiguous is the feature, not a corner case.** The distance that decides
"nearer" is the same distance that says nothing about whether two clusters are
the same person. Clusters at 0.10 and 0.14 with a threshold of 0.15 are both in
range, and the 0.04 between them measures nothing. The costs are asymmetric:
an ambiguous state costs a reviewer seconds, a wrong join merges two people and
**compounds** — every later face inherits the error, the centroid moves toward
the wrong answer, and the cluster attracts more wrong faces.

**Neither size nor confidence breaks the tie.** Size is the tempting one and it
is actively harmful: a cluster is bigger because more faces were assigned to it,
including wrongly-assigned ones, so size makes the first wrong merge the most
attractive destination for the next. That is corpus convergence on a few giant
wrong clusters, and the test plants a 6-face cluster against a 1-face one to
require the outcome stay ambiguous.

**An ambiguous outcome modifies no cluster and names no destination.**
`ClusterID` is 0, and a test asserts the membership count is unchanged — a
stage that both joined and flagged has made the decision it says it cannot make.

**The real bug: the margin was a fraction of the wrong number.** It must be a
fraction of the **threshold** (the engine's noise floor), not of the runner-up.
Since any candidate is within the threshold, the runner-up form is always the
*more* permissive one, and its defect is inverted: the required margin shrinks
as the runner-up gets farther — 0.0025 to separate 0.40 from 0.41, but 0.05 to
separate 0.10 from 0.14. The implementation comment warned about exactly this
and the suite could not see it, because every test had a runner-up near the
threshold where both formulas agree.

**Six mutations, five killed first pass.** The sixth is the margin above; the
test that separates the formulas asserts both side by side, so a change to
`DefaultSeparation` fails with a diagnosis instead of silently flipping the
expected outcome. `candidatesFor` sorts with a cluster-id tiebreak because Go
randomises map iteration — unsorted, the winner is chosen by luck and a test
that passes by luck passes by luck forever.

**All twelve mutations across 2.4b.2 and 2.4b.3 now die; none survive, none are
unscored.**

**Step 2.4b.4 — the consolidate pass. Done** (`internal/cluster/consolidate.go`).

Two properties, both **structural rather than disciplinary**:

- **A refused merge leaves nothing.** The tempting implementation writes the
  record, discovers the guard refuses, and rolls the membership back — and the
  record survives the rollback, so the queue shows a merge that never happened.
  `absorb()` consults the guard *before* anything moves, so there is no partial
  state to roll back and nothing that could survive one by accident. That is an
  ordering, not clean-up discipline.
- **A merge moves membership.** The first version appended and left the loser's
  slice populated, so a face was in two clusters, the loser's centroid still
  existed, and a reversal would have to guess which copy was authoritative.
  Marking a loser `merged` does not prevent that — the state is a label, not a
  lock.

**State is written after every decision, not during.** A pass that is abandoned
— cancelled job, interrupted action, an early guard refusal — must not have told
the queue anything. This is only observable when a pass stops early, because a
pass that *completes* produces an identical final state either way, so
`consolidateBounded` exists to make the abandoned case reachable and testable.

**The pass is one sweep, not a fixpoint.** A fixpoint would merge further than
any single pairwise decision justified — the over-merge the guard prevents,
reintroduced one layer up.

**Two mutations survive, and both are redundancy rather than a missing test.**
The `j := i + 1` guard is backed by `absorb` refusing a self-merge; the
state-after-decisions ordering is backed by `absorb` never writing state. Two
places enforce one guarantee and one edit cannot remove both. Neither was
papered over with a test that appears to kill it and does not —
`TestConsolidate_TwoMutationsAreBackedByASecondMechanism` records the redundancy
and asserts the second mechanisms are still present.

**A stale membership was hiding another mutation.** With the loser's members
left in place, the already-merged exclusion was not load-bearing: a second pass
found a candidate pair anyway and was idempotent for an unrelated reason. The
test that pins it now constructs a cluster marked `merged` that *still holds
members* — what an import from an older corpus leaves — because after a real
merge the loser is empty and `liveClusterIDs` skips empty clusters too. A second
skip that happens to agree is not evidence the first one is load-bearing.

**Step 2.4b.5 — the embedding. Done** (`internal/cluster/embedding.go`).

Every distance in this milestone is a function of an embedding, so an embedding
bug does not fail here — it fails *there*, as clusters that merge people who do
not look alike, with a green suite and a plausible-looking distance in the log.
So the file is mostly about **refusing** bad vectors.

**Dimension is the hazard, because the model decides it.** A vector's length is
not this code's decision, and every distance function compares two of them. A
mismatch panics, truncates silently, or — the dangerous one — compares a
**prefix** and returns a number that looks real while every threshold in the
milestone was calibrated against the other dimension. Mismatches are refused,
never reconciled.

Zero vectors, NaN and infinity are refused at the boundary for one reason: they
compare as false against every threshold, so a face carrying a NaN matches
nothing and nothing is reported. The zero-`Detector` shape from 2.4b.0, one
layer down. `CosineDistance` is 0..2 not 0..1, clamped because floating point
yields 1.0000000000000002 for identical vectors, and the tests pin the endpoints
so the real range is discoverable from the test names.

**`Attach` verified the caller's word for the hash.** The first version took an
`actualDigest` argument — a caller attesting to the file's hash while the
function checked the attestation, so passing the expected digest for a model that
was never hashed produced a "verified" embedder over arbitrary bytes. The check
was theatre, and a test caught it. It now computes the digest itself, streaming
through `io.Copy` into the hash because a face model is tens of megabytes.

**The detector's pin policy is not reimplemented.** `normalizeDigest` originally
duplicated `checkDigestWellFormed` and emitted a sentinel that does not exist,
because the detector expresses that condition as an `UnavailableError` *reason*.
Two copies of "is this a usable pin" drift invisibly. One vocabulary for the
whole feature, so a caller that reports an unverified *detector* does not learn
a second dialect for an unverified *embedder*. `ReasonDigestUnreadable` is a
third reason rather than a fold into "unverified": fix the pin, get the right
file, and fix the permissions are different operator actions, and collapsing the
first two re-downloads a model whose pin is a typo.

A model that changes its **output width** between calls is refused: every
distance it feeds is calibrated against the width it had before.

**Four of eight mutations survived the first pass**, all from the same cause —
the tests checked *outcomes*, not *ordering* or *resulting state*:

| Mutation | Why the outcome test could not see it |
|---|---|
| runtime loaded before the digest check | both orders produce the same error |
| `modelReady` left true after a refusal | invisible until a later caller checks the flag, not the error |
| output-width check removed | needs a model whose width *changes*, so no constant-width fixture reaches it |

The fixture now counts runtime calls; the tests assert the count is zero, the
flag is false, and the width is stable.

**A mutation that did not express the defect it names.** The first load-ordering
mutation deleted the comment above the call instead of moving it, producing two
`LoadModel` invocations rather than a reorder. The test passed and I recorded a
false "SURVIVED". Rewritten as a real move of the block, it is killed.

**Step 2.4b.6 — candidate selection. Done** (`internal/cluster/candidates.go`).

The plan calls this "ANN". There is no sqlite-vec, no HNSW and no IVF in the
tree, and adding a native extension for a corpus that fits in memory would trade
a correctness property for a speed property nobody needs yet. So it is an
**exact** search, and the test names say "exact" so nobody later reads a passing
suite as evidence about an approximate index.

**Exactness is a correctness property here, not a performance choice.** A recall
miss in the candidate stage is invisible: the guard, the assign margin and the
consolidate threshold all operate on whatever candidates they are handed, so a
face that should have been a candidate simply is not considered — and the outcome
is a cluster that is wrong in a way that looks like a *judgement*. The distance
would have been 0.31, the threshold is 0.5, and nobody logs the face that was
never looked at. If this is ever replaced, the replacement needs a recall test
against **this** implementation as ground truth.

**A bad row is skipped and reported; a bad query is a refusal.** Asymmetric, and
deliberately. A poisoned row in a 40,000-face index must not stop the other
39,999 — the alternative turns one corrupt embedding into a library that cannot
be clustered at all. But a bad query cannot be skipped, because there is nothing
to search *with*.

A bad query is also **not** recorded in the `SkipReport`, which the first version
got wrong. `SkipReport` counts rows that were not *compared*; recording the query
there describes a row as unusable when every row is fine, so an operator reading
"3 of 40,000 faces could not be compared" hunts for three corrupt embeddings
that do not exist while the real fault sits in a field named `row`.

**The bug this step found: `CosineDistance` returned `(NaN, nil)`.** Validation
checked only the running sum of squares, which catches a zero vector and a NaN in
the run-up to it — but NaN poisons `dot` and `normB`, `cos` comes out NaN, and
**both** clamp comparisons are false against NaN, so it passed straight through.
Found by a candidate fixture that counted three skips where the arithmetic said
four.

That is the worst shape a distance can have: it sorts unpredictably, compares
false against every threshold in the milestone, and reads as a legitimate
candidate — so a face with a NaN embedding was offered to the over-merge guard,
which treated it as infinitely far. **A face with a NaN embedding was
indistinguishable from a face nobody had looked at**, which is the exact failure
step 2.4b.0 was built to prevent, one layer down.

**A second bug, found by a fixture that asserted the wrong thing:** a self-match
is *not* distance exactly 0. Identical vectors land at `cos = 0.9999999999999999`
— a distance of 1.1e-16. A caller dropping the self-match by testing `== 0` would
keep it, so a face would be compared against itself and, being arbitrarily close
to itself, merged into its own cluster.

**The `cos > 1` / `cos < -1` clamps are unreachable defence, documented as such.**
Cauchy-Schwarz guarantees the bound; a search for a reachable pair that exceeds
it (long normalised vectors, near-identical perturbations, up to 65536
components) found none. Two mutations removing the clamps survive — the honest
reading is that they are **not load-bearing**, not that a test is missing. Kept
for un-normalised callers and to survive a change to the normalisation that
reintroduced the drift.

**Four fixtures for the tie-break, all green with it deleted.** Equal distances;
adjacent ascending pairs; rows rotated off the query; and finally symmetric
perturbations where the tie pairs were `(2,3)`, `(1,4)`, `(0,5)` — always
ascending by *position*, and reversing the emission order changed nothing
because `Neighbour.Index` **is the position in the caller's slice**, not a
property of the vector. Reversing which vector goes where also reverses the
label.

The general reason, found on the fourth attempt: **a k-sized buffer filled in
arrival order and re-sorted by an insertion sort that only moves elements LEFT
past strictly-greater ones emits positions within an equal-distance run in
ascending order regardless of what the comparator says about equality. The
comparator is never asked.** So the comparator is tested **directly** — a private
function with a total order and no I/O has no excuse for no test, and the test
asserts antisymmetry over every pair.

**Two fixtures wrong in the same way, and the lesson is the same.** Rows at
0/90/180/270 put one row *on* the query; rows at 45/135/225/315 were equidistant
but at distance 0.29, a value no threshold here will ever see. And a `1e-8`
perturbation of a `0.5` component vanishes entirely in `float32`, so the vectors
came out bit-identical and the exact-equality branch fired instead of the one
under test.

**A fixture must assert its own preconditions.** A test that cannot fail for its
stated reason is worse than no test, because it reports coverage.

**Step 2.4b.7 — the cluster store. Done** (`pkg/sqlite/cluster_store.go`).

Two migrations, because the store needed a column that did not exist and a
constraint that was wrong.

**99 — `person_clusters.name` + `person_cluster_names`.** The name is a
`*string` and stays that way. A corpus with no scraper has no names, so nil is
the state every cluster starts in and most clusters stay in; an empty string
would be a value every downstream consumer displays, filters on and sorts by.
`person_cluster_names` is append-only and there is no DELETE path against it, so
a rename accumulates and `ClearName` leaves the history intact — unnameing is not
un-deciding.

**100 — a schema bug, not a missing feature.** Migration 97 ended
`person_cluster_members` with:

    UNIQUE (target_type, target_id, frame_index, face_left, face_top)

No `cluster_id`. The comment above it stated the intent — *"a face appears in a
given target at a given frame once, so re-detecting must UPDATE rather than add
a second"* — and the constraint implemented a **broader** rule: one face, one
cluster, ever.

That makes `ambiguous` unrepresentable, and §7.1 defines ambiguous as one
embedding matching two distinct candidates — two `cluster_id`s for one
appearance. The store could not record a conflict, the UI could not be shown both
candidates, and `ClustersForTarget` could never return more than one row, which
is the query whose multi-row result **is** the signal. A test caught it by trying
to record one.

Uniqueness is now scoped to the cluster, which is what the intent actually was: a
rescan still cannot inflate a cluster, and a genuine conflict is recordable.
SQLite has no `DROP CONSTRAINT`, so the table is rebuilt.

**A comment stating an intent is not a constraint implementing it.** Nothing
checked the two agreed, and the gap survived three schema versions because every
version test watched the number change rather than the meaning. The replacement
test asserts semantics: same crop twice into one cluster is refused, same crop
into two clusters is allowed *and* visible through `ClustersForTarget`.

**Four fixture defects, one cause each — and three were mine, not the code's.**

1. `no such column: created_at`. The member SELECT asked for a timestamp
   migration 97 never created. The column was only ever *selected*; review order
   is by `distance`, so a `created_at` here would be a second ordering nobody
   reads. Removed rather than added — a column with no reader is not a migration.
2. `UNIQUE constraint failed` on the ambiguous fixture. The schema bug above,
   found by the test that exercised the state the spec requires.
3. **An absolute count.** `ListUnnamed` asserted `4` and got `7`. `sfTxn` rolls
   back, so the test's own rows leave nothing behind, but the shared integration
   fixture is not empty and the rest of the package builds in it. The test was
   measuring the fixture as much as the code. Now: measure the baseline, assert
   the **delta**, and identify rows by ID rather than by count — a count is
   satisfied by any row, so an absence can be masked by an unrelated one
   appearing.
4. **A subtest sharing its subject with its siblings.** Four subtests ran against
   one cluster. A refused write leaves a cluster unnamed, so "empty name" could
   not distinguish *my* refusal from a sibling's, and "empty actor" failed
   outright. Each subtest now creates its own.

The rule, hit from two directions in one step: **a value another test can set is
not a test.** A fixed target id (`scene 42, frame 99`) had already been claimed
by a row a previous run's rolled-back transaction inserted — the rollback undoes
the row but the fixture's AUTOINCREMENT sequence keeps moving. Fixed identities
in a store test are collisions waiting for a reordering, and that one fired on the
first run.

`appSchemaVersion` **100**. Unit 38/38, integration 38/38, store suite green three
runs in a row.

**Exit:** clusters created, browsable, and unnameable without complaint.


**Step 2.4b.8 — the GraphQL surface. Done**
(`graphql/schema/types/cluster.graphql`, `internal/api/resolver_cluster.go`).

**The bug: both field resolvers were generated as dead code.** `personClusterModel`
deliberately leaves `members` nil and `memberCount` zero so that listing twenty
clusters does not read every member of every cluster to render twenty rows. With
no `resolver: true` in `gqlgen.yml`, gqlgen generated

    return obj.Members, nil

which compiles, keeps both fields in the schema, leaves the types right, and
returns an empty list to every client. **No test failed**, because the resolvers
were never called: correct bodies, correct comments, no calls. This is
"referenced != used" in its purest form.

The config key is the **GraphQL** field name. The first attempt wrote
`member_count:`, gqlgen ignored it with no warning, and `members` (already the
right case) wired correctly while `memberCount` did not — the worst shape, since
the half that works is not the half anyone queries first.

Two tests read **generated output** rather than behaviour, because the behaviour
cannot fail: one asserts the executor calls the resolver *and* does not contain
`return obj.Members`; the other is line-based over the YAML, because the question
is about indentation and a parser would answer the more forgiving question. Both
verified load-bearing — reverting the generated call, and flipping
`resolver: true` to false, each kill exactly one.

**The actor comes from the session, not the argument.** A browser mutation that
can pass its own `actor` string is a mutation that can lie about who named a
face, and that string is the only record of who decided two appearances are the
same person. The argument exists for the pipeline's unauthenticated passes.

## Step 2.4b.9 — the pass, and the gap this exit had not closed. Done

(`internal/cluster/pass.go`, `observe.go`, `embedding_store.go`,
`internal/manager/task/face_clustering.go`, `internal/manager/manager_tasks_face.go`,
`pkg/sqlite/cluster_store_adapter.go`.)

The gap this section used to describe is **closed**, and it was closed by
following path 1 above — the plan's own intent, the larger change.

**`internal/cluster` is now imported by seven files across three packages**, and
`cluster.NewPass` has a real production caller. Verified by asking the tree, not
by reading the diff:

    grep -rl 'stashapp/stash/internal/cluster' --include=*.go . | grep -v internal/cluster/

The stages run on real 512-wide `[]float32` embeddings through
candidates → assign → guard → consolidate, writing through `cluster.Store`. The
pass declares that interface itself rather than importing `pkg/sqlite`, because
layering runs database-below-domain and a pass reaching for the store would be
the one place in the tree with the arrows backwards. The scalar `testFace` stays
as the property test it is good for; every stage gained a real-vector twin.

**Two things this step found that no test in the tree had been looking for.**

*The store implemented three of the four methods the pass's interface needed, and
no build reported it.* `AddMember` took sqlite's own `Member` rather than
`cluster.StoredMember`, and `MergeCluster` — the only method that is not a single
statement — did not exist. Both halves had thorough fake-backed tests and both were
green, because **no file imported both packages**, so the compiler never had to
compare them. This is step 2.5's lesson arriving again in the same shape, one
level up. The cure is the same too: `var _ cluster.Store = (*ClusterStoreAdapter)(nil)`
in a **non-test** file, so every build checks the seam. The rule worth keeping is
to write the outside-construction test *first*, against the real implementation of
whatever the module will be given.

*`MergeLimit: 0` documented "no cap" and `consolidateBounded` read it as a hard
limit of zero.* The loop never ran, the pass reported "merged 0, refused 0,
skipped 0", and nothing errored. Found because mutation M12 *survived* — and the
region it pointed at turned out to be dead code. A surviving mutant is a claim
about the harness until proven otherwise, and here it was the only thing looking.

**And one claim this step had to retract.** The first pass reported that
"consolidate cannot merge anything assign separated, and the two stages'
conditions are the same inequality negated". The first half holds. The
explanation did not: it scoped a property of the *pipeline* as a property of
`absorb`, and `absorb` plainly does not have it. Measured both ways and pinned in
`internal/cluster/merge_reachability_test.go`:

* `absorb` on **planted** clusters, assign bypassed — **77 of 80** combinations
  merge (8 shapes x 10 join thresholds).
* a real **pass** — **0 of 100** (4 shapes x 5 join x 5 merge thresholds).

Both true, and the gap between them is the whole finding: the only clusters
`absorb` ever sees are the ones `assign` produced, and assign's defining property
is that it kept them apart. So the merge stage is not dead code — the pass simply
never hands it a mergeable pair, and a change to *assign* that merged more freely
would make consolidate live without touching consolidate. The guard's boundary is
measured, not assumed: a planted loser sits 0.368 from the winner's centroid at 60
degrees, 1.000 at 90, 1.632 at 120, so `MergeThreshold` **is** a live control —
the opposite of what was first reported, and no owner decision is pending on it.

**How the wrong claim survived its own evidence**, and the rule that follows: the
probe that should have disproved it reported "0 of 80 merged", agreeing with the
conclusion it was meant to test, and it was **vacuous** — it planted fixtures
with `forceClusterAt`, which seeds a 1-D `Scalar` point, and `CosineGeometry`
correctly refuses a width-1 embedding, so every case "refused" for a reason
unrelated to the guard. A probe must carry at least one case it is *expected* to
reach, and log it. If nothing is ever admitted, the probe is broken, not the
system.

**Exit status: done.** The store, the surface, the pass, and a production entry
point that runs it. The observation stage that feeds it (decoder, detector,
embedder, and the lister's store) is not wired to a model, so the mutation refuses
explicitly rather than queueing a pass that would find nothing —
`ErrObserverNotWired`, surfaced by the `faceClustering: ID!` mutation.

## M3 — Metadata sharing — DONE. Tag `m3-metadata-sharing`

**Exit:** an opted-in library exports; an opted-out one exports nothing, and
that is enforced where the permission is read. **Met.**

Landed in three steps, plus one the plan did not ask for.

### Step 3.0 — the library boundary the spec assumed existed. Done

(`101_libraries.up.sql`)

`libraries` and `user_library_access` did not exist. §6.2's "never published ...
any private library's rows" and §6.4's "served only to users holding a
`user_library_access` row" were both referring to tables no migration had
created, so M3's exit criterion could not be true as written.

A library here is a **sharing scope**, not a file collection: stash already models
paths, and duplicating a file tree would be a second source of truth about
storage. The alternative — `is_private` on all seven target tables — makes "which
rows may leave" seven queries that must agree, and the one that is forgotten leaks
silently.

### Step 3.1 — Consent. Done

(`102_consent_preferences.up.sql`, `internal/collab/consent.go`,
`pkg/sqlite/stashforge_consent.go`)

Three decisions, all of which are about the default leaking:

* **Absence of a row means opted-in** (§6.1), so the absent case is what the
  tests pin hardest and what a well-meaning "safety" change would break.
* **A corrupt `metadata_share` is an error, not a default.** The column is
  CHECKed, so this can only happen if something bypassed the check — and
  defaulting there lets a bug make a privacy decision.
* **An opted-out user is never re-prompted.** A re-prompt is a dialog with a
  Share button, and any implementation that reads "did not click Share" as
  consent has published a user who declined. This is the subtlest line in the
  file; it has a test and a mutation of its own.

`PublishedFields` is the single source of truth for the disclosure, and the prompt
renders from it — a hand-written disclosure is a lie the first time a field is
added to the export.

### Step 3.2 — The exporter. Done

(`internal/collab/exporter.go`)

The plan's test list is honoured by name, and the third one is the interesting
one. `TestExport_ExcludesFilenamesAndPaths` walks the **marshalled JSON**,
because a struct-level assertion cannot see a field added to the payload later.

**Three defects the tests found, each of which would have shipped:**

1. **The path guard did not work.** It scanned the marshalled blob for values
   that *begin* with a path, so a payload carrying a path inside a longer string
   passed. Only a **positive control** — a deliberately dirty payload the guard
   must reject — caught it. A guard tested only with clean fixtures proves
   nothing about whether the guard fires.
2. Fixing that made the guard **too eager**: it refused a DNS-shaped instance
   name, which is legitimate and operator-chosen. The check is now scoped to
   `entries[].fields` and `fingerprints` — the user's data — with the reasoning
   written at the carve-out, because a carve-out with no stated reason becomes a
   hole.
3. The guard **cannot distinguish a path from a title that is a path**; the bytes
   are identical. That limit is now pinned by a test rather than asserted away:
   the cost is a rare, visible, user-fixable failure; the alternative makes the
   guard useless for the case it exists for.

`BuildPayload` deliberately does **not** check consent. An exporter that silently
returned nothing for an opted-out user would be indistinguishable from one that
returned nothing because the library was empty — and that difference is exactly
what an operator needs. Consent is enforced inside `Publish`, and the refusal is
**audited before it returns**.

### Step 3.3 — Federation. Done

(`internal/collab/federation.go`)

Per-peer opt-in, per-peer key, signed submission id, and an authenticated read.

* **Publishing and consuming are independent flags.** §6.5 says an instance may
  consume a peer's commons "without publishing to it"; a single `Federated bool`
  makes that inexpressible, and the fix afterwards is always to publish to people
  you did not mean to. All four combinations are walked by a test.
* **Two checks, both required, before acceptance.** The signature says who sent
  it; the content-address check says what it contains. Signing the id is only
  safe because the id is derived from the content — so the content is re-hashed
  on receipt, or a peer could sign an id for an empty payload and send a full
  one. The library id is a **parameter** to that check, not read from the
  payload, because the sender must not supply the value the hash binds.
* **404, not 403**, for both "denied" and "does not exist" — they must be
  indistinguishable or the endpoint confirms which libraries exist. An absent
  authorizer is treated as *denied*, never as allowed.
* **Unknown peer ≠ bad signature.** An unconfigured peer is a configuration
  question; conflating it with tampering sends an operator hunting a breach that
  did not happen.

### What M3 found

`appSchemaVersion` was 100 while the new migrations were 101 and 102, so golang-
migrate stopped at the recorded version and simply **never applied them**. The
symptom was six tests failing at once with "no such table: consent_preferences".
`TestStashForge_SchemaVersionMatchesAppSchemaVersion` already guarded exactly
this and passes now — but it only runs under the integration tag, so the
`go test ./...` gate is green with an unapplied migration, which is worth knowing
before the next one.

And the shape repeated from M2c: `internal/collab`'s unit tests passed 100% while
the sqlite adapter did not compile, because `collab.Rows` was missing `Close()`
and the fake had been written to match the interface rather than the driver. A fake
proves the logic; only a real database proves the SQL. Both files now say so.

**Verification:** `internal/collab/mutate_consent.py` — **22 applied, 22 killed,
0 survived, 0 broken**, across all three files. No EXEMPT list, because this is
the milestone where a mistake is irreversible.

**Exit status: done.**

---

## M4 — Public hosting — COMPLETE. Tag: `m4-public-hosting`

**Exit, met:** a public instance that is safe to expose. Steps 4.1, 4.2, 4.3 and
4.4 are all done — server, GraphQL and screen. The two items this section used
to list as outstanding (TOTP at-rest encryption, the wizard UI) are closed; see
step 4.4 and "What is left in M4" for what each turned out to need.

### Step 4.1 — Mode enforcement. Done

(`internal/collab/mode.go`, `103_instance_settings.up.sql`)

Implemented as refusals, not capabilities. Every method answers "may I" or "must
I refuse", because a mode built as a capability list is one where forgetting an
entry silently permits something.

- **A public instance over plain HTTP refuses to start.** The headline. It hands
  session cookies to anyone on the internet, and a public instance is precisely
  the one whose users cannot be told to use a VPN. Contribute and private are
  legitimately served over plain HTTP and are *not* refused — a guard that
  refuses everything is not a guard, and an operator who cannot run a private
  instance on a LAN routes around it. Both directions are tested.
- **Every default fails closed.** A missing settings row reads `private`. An
  invalid mode on a context is ignored, not taken at face value. A mode outside
  the three is an error naming the valid set.
- `instance_settings` is a single row with `CHECK (id = 1)`, so "what is the mode"
  can never have two answers. It is a table and not a config file because
  consent and grant rows live in the database, and two sources of truth for a
  security decision disagree exactly when it matters.

### Step 4.2 — 2FA (TOTP). Done

(`internal/collab/totp.go`, `pkg/auth/totp.go`)

A TOTP code is **single-use within its time step**, which is not what RFC 6238
does: the specification's `Validate` accepts the same code any number of times
inside a ±1 step window. For a login second factor that is a real weakness, so
verification records the step that matched and refuses a step already recorded.

**Two bugs, both found by the test the plan did not ask for.**
`TestTOTP_MatchesTheLibraryImplementation` cross-checks the hand-written
arithmetic against `pquerna/otp`, and without it the replay tests would have been
a guard around a function that rejects every code:

1. **The counter was always zero.** `TOTPStep` is a `time.Duration`, so
   `int64(TOTPStep)` is 30e9 — nanoseconds — and `Unix()/that` is 0 for any date
   this century. 2FA rejected every real code while looking entirely healthy.
2. **Dynamic truncation masked the wrong thing.** RFC 4226 clears the 31st bit
   by masking only the top byte with `0x7f`; masking a 32-bit word with
   `0x7fffffff` also clears three other bytes' high bits, so the two disagree
   most of the time.

**At the login boundary** (`pkg/auth/totp.go`), the rule is: a failed 2FA check
is *the same error value* as a wrong password — not a similar one. A distinct
"invalid code" tells an attacker the password was correct, which stages the
attack. The check sits after the password (so a wrong password is not charged
against the 2FA budget) and before the throttle is cleared (so a valid password
plus a wrong code cannot reset your own lockout). A store that cannot be read
refuses the login; it is never read as "no 2FA configured".

### Step 4.3 — Library access grants. DONE: domain, store, and ENFORCED

(`internal/collab/access.go`, `internal/collab/media_scope.go`,
`pkg/sqlite/stashforge_library_access.go`, `pkg/sqlite/stashforge_media_scope.go`)

§6.4: metadata and media are **two separate grants**, and the refusal is 404, not
403, because the ungranted user can already see the scene in metadata — a 403
confirms a file exists. There is exactly one refusal error in the file and it
reads `not found`; a test asserts all three causes are byte-identical and that
the message names no library, user, grant or mode.

**This step was recorded as "not enforced, and cannot be until migration 105
exists" for a full milestone. It is now enforced.** The blocker was real and is
now closed; the three pieces below are what closed it.

#### Migration 105 — library_id on the target tables

`library_id` is now on all seven target tables, plus `is_default` on
`libraries`, plus a backfill into one owner-owned library so an instance that
upgrades does not come back with its own media 404ing.

**Two things every document in this repo got wrong, both found by running the
migration rather than by reading it:**

1. **The seventh target table is `groups`, not `movies`.** Migration
   `65_movie_group_rename` renamed it. The plan's step 4.3, GOAL.md's
   non-negotiable and migration 101's own comment all still say `movies`, and
   the first version of 105 failed to apply with `no such table: movies`. A
   table list written in prose rots silently; only a migration that names the
   table reports it, so the integration test walks the list.
2. **A row with no library is the SCANNER'S NORMAL OUTPUT, not an anomaly.**
   Every newly-scanned row arrives with `library_id` NULL, because the scanner
   writes through a path that knows nothing about libraries. The plan's framing
   ("NULL is a refusal") would therefore 404 the owner's own newly-scanned
   files, and the fix that gets shipped under that pressure is "make NULL mean
   allow" — which is the fail-open, reached by a different road.

So **NULL resolves to the DEFAULT library**, not to "unrestricted". A row in no
library belongs to a library the owner owns, and the owner bypasses the grant
check by ownership. Every other user still needs an explicit
`user_library_access` row. Fail-closed where it counts, and a working instance
afterwards. `TestMediaScope_NoDefaultLibraryMeansNoOwnershipBypass` is the
positive control that proves the substitution is not a loophole.

#### The user id in the request context

`withRequestUserID` in `internal/api/stashforge_user_context.go`, wired
immediately after `authenticateHandler()`. It resolves the username the
authentication middleware already established into an int64, once per request,
in its own short read transaction.

**A signed-URL request has a username and no id**, and this is recorded rather
than discovered later: a device that cannot present a cookie cannot present a
grant either, so §6.4 applies literally and such a request is refused on a
public instance. The alternative — resolving the signature to an id — is a small
change to the same middleware and is deliberately NOT taken here, because it
would give a grant-bypassing capability to a URL handed to a television.

#### The gate, and where it goes

`allowMedia` in `internal/api/stashforge_media_gate.go`, called from the
per-target `*Ctx` middlewares (scene, image, gallery, performer, studio, tag,
group) — **not** from each serving handler. There are ~20 media routes; a
handler-level gate is 20 edits and the 21st is the one somebody forgets, and a
missing call is a silent leak rather than a crash. With the middleware, a route
added later without a gate is a route with no `*Ctx`, which is visible in review
as a missing line in a route block.

It runs **before any file is opened**, which is a security property rather than
tidiness: an ffmpeg transcode started for a request that is then refused is a
process an ungranted user can start at will, and a 404 does not close a process
that is already running.

**A store failure is a 500, deliberately, and every refusal is a 404.** A
database outage reported as "not found" sends an operator hunting a phantom
attack instead of a broken disk.

#### The two routes the coverage test found

`/{sceneHash}_thumbs.vtt` and `/{sceneHash}_sprite.jpg` are registered OUTSIDE
the `/{sceneId}` block, so they never saw `SceneCtx` and never saw the gate. They
serve a **generated sprite and thumbnail strip** of a video's frames, keyed only
by a hash — so on a public instance a user with no grant could fetch the frames
of any scene whose hash they knew. `sceneHashCtx` resolves the hash to a scene
and checks the scene.

Found by listing every media route and recording which middleware each one
passes, not by reading the handlers, all of which looked correct.

#### What the gate's own tests found

1. **`allowMedia` would have PANICKED, not refused.** It read
   `manager.GetInstance()`, which panics when no instance exists, so the
   `mgr == nil` guard was dead code — reached never, looking exactly like a guard
   that works. Added `manager.MaybeGetInstance()` for refusal paths, with the
   rule written down: **a refusal path asks MaybeGetInstance; everything else
   asks GetInstance, because everything else genuinely cannot proceed.**
2. **The grant-error case of the fail-closed table passed vacuously.** The
   fixture used the owner for all four store-error cases, and ownership
   short-circuits BEFORE the grant is consulted — so the grant read never
   happened and the planted error was never reached. A fixture whose subject
   never visits the code it names is a fixture that reports coverage.
3. **Three "remove the gate" mutations did not COMPILE** (the `collab` import
   became unused) and so killed no test. The harness scored them `broken` and
   refused to count them as kills, which is the project's standing rule. They
   were rewritten to also drop the import, so the mutation is a real edit to
   working code.
4. **"Answer 403 instead of 404" SURVIVED**, because the only test touching that
   line returned before reaching it — with no store configured, `allowMedia`
   refuses at the top, and the 403-vs-404 decision lives at the bottom on the
   path needing a whole application. Fixed by extracting `writeMediaRefusal`, a
   one-caller function that exists to make the property reachable.
5. **"Ungate the two sprite routes" SURVIVED**, because the test checked that
   `sceneHashCtx` *calls* the gate and not that the route table *reaches* it. A
   function nobody calls is not a gate. Both halves are now tested, with the
   route registration itself as the positive control.

Current: `python3 internal/api/mutate_media_gate.py` — **7 applied, 7 killed,
0 survived, 0 broken, 0 unscored**.

The seventh mutation ("the refusal body explains itself" — a 404 whose body
names the library) is the one worth having beyond the obvious 403 case: a
refusal that explains itself is an oracle even at the right status code, and
the test asserts the body names no library, user, grant, mode or owner.

**A real bug this step found, pre-existing from 4.1:** `err == sql.ErrNoRows`
cannot match, because `dbWrapper` wraps the driver error with `%w`. In the access
store that turned every ungranted request into a **500** — and a 500 tells a
prober the library exists, which is the exact disclosure §6.4 forbids. The same
`==` mistake was in both fail-closed paths of the instance-mode store, where it
was harmless in practice and dangerous in principle: the migration seeds the row,
so those branches were dead code that every test reported as covered.

### Step 4.4 — First-run wizard. DONE, server and screen

(`collab.RequireWizard`, `internal/api/stashforge_wizard.go`,
`ui/v2.5/src/components/Setup/StashForgeWizard.tsx`)

The plan asks for "a browser test that the wizard cannot be skipped". A
client-side gate cannot establish that: the client is whatever bytes the caller
sent. The property is enforced in the server, where skipping it requires patching
the binary, and that is where it is tested. The gate refuses *before* reading the
mode — a mode nobody chose must not become an answer to a question that was never
asked.

**The screen exists now**, at `/stashforge/wizard` — a route **separate from the
upstream `/setup`**, which is the paths-and-credentials config wizard. Sharing one
screen would mean an operator who re-runs configuration lands in the sharing
decision, which the server refuses with `wizard_already_completed`.

It calls the HTTP endpoint rather than GraphQL, and that is forced rather than
chosen: there is no session to send. `POST /stashforge/wizard` is the only
unauthenticated POST in the application, authenticated by possession of the
instance key. **The mode is chosen once here**; changing a live instance's mode
is a separate authenticated operation that deliberately does not exist yet,
rather than riding on an endpoint whose whole purpose is to be called once.

The TLS warning uses `window.isSecureContext`, not `location.protocol`, because
the instance may be behind a TLS-terminating proxy where the browser sees
`https` and the Go server saw `http`. It is advisory either way — the server is
the authority, and `collab.ModeErrors` is what actually refuses.

**Still no browser test.** `ui/v2.5` has no test runner, so the test the plan
names would mean introducing a framework. The security property holds without it,
because it was never in the client.

### M4 — COMPLETE. Tag: `m4-public-hosting`

**Nothing is left.** The previous revision of this list had four items; two were
already done and are now recorded as such, and the other two are closed above.

What M4 is, in one paragraph: a private instance is the default and the server
refuses to *start* as public over plain HTTP; a public mode is refused until
somebody has been through the wizard, so "public" always means "chosen"; 2FA is
single-use within its time step, sealed at rest, and has no read path that
returns a secret; and media is served only to a holder of an explicit library
grant, through a gate that answers 404 for every refusal so the answer never
confirms a file exists.

### What is left in M4

**Nothing.** Kept as a section so a reader who has read an older revision of
this document can see what changed and why. The items below are the ones that
were on the previous list and are now closed or corrected.

**DONE since the previous revision of this list:**

- **GraphQL for 2FA, libraries, grants and consent** —
  `graphql/schema/types/hosting.graphql`,
  `internal/api/resolver_mutation_hosting.go`, `internal/api/models_hosting.go`,
  `internal/collab/library.go`, `pkg/sqlite/stashforge_libraries.go`.
- **TOTP at-rest encryption** — the list said "the plan says *encrypted*; it is
  currently stored as base32 in a column, which is a plaintext secret". True
  when written, false now: `internal/collab/totp_secret.go` seals with
  AES-256-GCM under a key derived from the instance key, `pkg/sqlite/stashforge_totp.go`
  calls it on every write, and migration 104 describes the stored form as
  `v1.<base64(nonce+ciphertext)>`.

**Both were verified by `grep` before being believed, and both had to be.** A
"what remains" list is a claim about code and it decays silently — nothing marks
it stale when the work it names arrives. This is the same defect as `movies` vs
`groups`, and it is now the rule: **a comment is a claim about the schema, and
the only way to know whether it holds is to read the DDL in the same file.**

### Migration 106 — and the fourth prose-versus-schema mismatch

Writing the library store turned up three more of the same defect, which makes
four in total. The shape is always identical: a COMMENT states a constraint, the
DDL does not create it, and a reasonable reader — including me, later — believes
the comment.

1. **101 says `UNIQUE per (owner, name)` and creates no such index.** Its only
   indexes are `idx_libraries_user` and `idx_libraries_private`. So a user could
   create "Main" three times, the grant UI would show three libraries with the
   same label, and nothing objected. Migration 106 adds it — per owner, not
   globally, because 101 explicitly says two users may each own a "Main".
2. **105's partial unique index was `UNIQUE (is_default) WHERE is_default = 1`.**
   That is a UNIQUE constraint on a **constant**: every row in the index carries
   the same value, so it permits exactly one such row in the whole table. It was a
   *global* "one default on the instance" rule wearing the comment "at most one
   default library", and it was **enforced** — the second user to create a library
   got a violation and no default at all, so every one of their unscanned rows
   resolved to "no library, no owner" and the gate refused it. Now
   `(user_id, is_default)`.
3. **105's comment said NULL resolves to the refusal; the code resolves it to the
   default library.** The code was right and the comment was wrong, so the comment
   is what changed. NULL is the scanner's normal output, not an anomaly.

The shape to be suspicious of, learned the hard way: **a partial unique index
whose `WHERE` clause is itself the constraint.** It reads like a filter and is a
uniqueness rule, and on a constant column it means something almost nobody
intended.

### `TestStashForgeStoreConstructorsAreActuallyWired`

The fourth "referenced != used": `sqlite.ConsentStore` was fully implemented and
fully tested, and `NewConsentStore` appeared in **exactly one place in the tree —
its own constructor**. Nothing built it, so `setConsent` would have been dead code
written against a store no product path could reach. The whole suite was green
throughout, as it was for `ProposalStore`, `ReputationStore` and the media gate.

The test is one grep: every `New*Store` in `pkg/sqlite` must be called from a
non-test file. It checks **30 constructors, all wired**, and it is
mutation-checked — deleting the `ConsentStore` wiring makes it fail, naming the
store. That is the whole family of bugs, and it costs about a second.

It reported 30 false positives on its first run because it walked `..` from
`internal/api` — which is `internal/`, not the repository — so it never saw
`pkg/sqlite/database.go` where every upstream store is actually built. **A check
that fails on everything the first time is a check whose scope is wrong, not one
that has found thirty bugs.**

### Four defects the new store's own tests found

1. **`OwnedLibrary` returned two distinguishable errors** — `models.ErrNotFound`
   for a library that does not exist, `ErrNotLibraryOwner` for one that is not
   yours. That is an existence oracle: a caller probing ids learns exactly which
   are real. Both are now the ownership refusal. This is *narrower* than the media
   gate's 404 rule and the distinction is deliberate: the gate must not confirm a
   **file** exists, while here the caller already holds the id and the risk is
   only telling real ids from fake ones.
2. **`findUsersByIDs` used raw SQL against `models.User`**, which has no `db` tags
   at all. sqlx failed with `missing destination name created_at`. The tags are
   on the package's own `userRow`, so the query has to go *through* `UserStore`
   rather than around it.
3. **Two refusals checked `models.ErrNotFound` where `dbWrapper.Get` returns
   `sql.ErrNoRows`.** Neither fired, and the caller got `error executing SELECT id
   from libraries...` — naming a column instead of an instruction. The package has
   two not-found conventions and they are not interchangeable.
4. **`UserStore.FindByUsername` returns `(nil, nil)` for a missing name**, not
   `models.ErrNotFound` — the two contracts coexist in one package (`user.go:231`
   swallows `sql.ErrNoRows`, `Find` does not). A caller checking only `err` passed
   a nil `*models.User` through to `u.ID`: a nil dereference on a grant mutation,
   triggered by typing a username that is not there.

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
# OUTSIDE the core module. `plugins/` is not under the core module, so a go.mod
# here is a genuinely separate module rather than a nested one the core tree can
# reach into.
cd plugins && mkdir -p p2pdownloader && cd p2pdownloader
go mod init github.com/stashapp/stash-plugin-p2pdownloader
```

**Not `pkg/p2pdownloader/`.** That path is inside the core module: a `go.mod`
there is a *nested* module, which Go tolerates but which leaves the code in the
core tree, inside `go build ./...`, and greppable as core. The location is the
enforcement, and it has to be outside the module boundary, not merely a
directory with a `go.mod` in it.

Its own `go.mod` is what enforces it: a package in the core tree cannot import a
different module without the core's own `go.mod` gaining a `require` and a
`replace`, and both are visible in review.

**And it must be loadable, which is a separate question from being separate.**
The module is built to a static binary and referenced by a `p2p-downloader.yml`
in the configured plugins path:

```yaml
id: p2p-downloader
name: P2P Downloader
description: Fetches files over BitTorrent and ed2k into your library
version: 1.0.0
interface: rpc
exec:
  - stash-plugin-p2pdownloader
```

**CORRECTED 2026-09-27 — it is a `.yml` file, not a `source.json`.** The plan
above originally specified `source.json` with a JSON manifest, which is the
shape *stash-box* uses for scraper configs and not what the plugin host reads.
`pkg/plugin/plugins.go:141` loads every file with `filepath.Ext(fp) == ".yml"`,
and `pkg/plugin/config.go:19` parses it into `Config` — which is `yaml`-tagged
throughout, and whose `id` is a **lowercase-unexported** field populated from
the FILENAME stem rather than from the document.

That last detail is the one that would have bitten hardest, and it is invisible
from the struct: `Config.id` is unexported, so there is no way to set it from the
document, and a manifest carrying `"id": "p2p-downloader"` is parsed and
**ignored**. The plugin's identity comes entirely from the file being named
`p2p-downloader.yml`. A wrong manifest does not error — it loads, with the
identity the filename gave it and every field the document supplied.

Which is exactly the failure mode this milestone has already hit four times: a
document stating something the code does not do, believed by the person writing
against it. This is the fifth, and it was found by reading the loader.

A module that is separate but never referenced is not a plugin, and that is the
other half of why the original location was wrong: it failed *both* ways —
reachable from the core, and invisible to the plugin host.

**Verify, before implementing transfers:**

```bash
# the core must not know the plugin exists
go build ./... && go vet ./...        # passes with the plugin absent
grep -rn 'p2pdownloader' --include='*.go' . | grep -v '^./plugins/p2pdownloader/'
# must print nothing
```

**DONE 2026-09-27.** The three seam tests are in
`internal/api/stashforge_p2p_seam_test.go`; the plugin is at
`plugins/p2pdownloader/` with its own `go.mod` and `p2p-downloader.yml`. All
three pass. Mutation-checked by `mutate_seam.py` at the repo root.

**One: the location and the transport.** The original text put the module at
`pkg/p2pdownloader/`, which is *inside the core module* and therefore not a
Stash plugin at all. Stash loads plugins three ways
(`pkg/plugin/config.go:363`): `js` (goja — JavaScript, which cannot host a
BitTorrent client), `raw` (a binary that exits when the task ends, so no DHT
and no inbound connections), and `rpc` (a binary, launched once, long-lived,
over `net/rpc/jsonrpc`). The downloader ships as its own module at
`plugins/p2pdownloader/`, built to a static binary, loaded with
`interface: rpc`. It is native, it is a real plugin, and it stays up.

**Two: the test needs a second half.** `TestP2PDownloaderIsNotImportedByCore`
asserts the grep is empty — and a Go package *inside the core tree* that imports
nothing from the core satisfies that grep trivially. The test would have been
green on something that cannot load and never runs. So:

- `TestP2PDownloaderIsNotImportedByCore` — the core's `go.mod` gains no
  `require` or `replace` for the plugin module. Checked on **go.mod**, not on
  `.go` files: an import path in a `require` block is the only way core code can
  reach a separate module, and it is a one-line diff to review. A grep for the
  word "p2pdownloader" across `.go` files would match this very file and say
  nothing.
- `TestP2PDownloaderHasItsOwnModule` — the directory has a `go.mod` whose
  module path is not under `github.com/stashapp/stash/`, **and is not inside
  the core module's directory tree**. The path check is about review, not the
  toolchain: a downloader whose module claims to be core reads as core, and is
  one `replace` away from being imported by it.
- `TestP2PDownloaderIsNotBundledByCore` — **the half that matters.** The core
  binary contains nothing from the downloader's module. A core that shells out
  to a *bundled* downloader passes both checks above and is exactly the failure
  mode worth excluding: the code is outside the import graph but still inside
  the shipped artifact.
- `TestTheDownloaderShipsAManifestTheHostCanRead` — the loadability half.
  Separate is not the same as installable: a module nobody references is a
  directory somebody cloned.

**CORRECTED 2026-09-27 — `go tool nm` does not work for this, and two other
mechanisms did not either.** The original text said to verify with `go tool nm`
over the built binary. That check **passes on a core that has the downloader
linked in**, and the mutation harness proved it: with a `replace` in `go.mod`
and a real import in `cmd/stash/main.go`, the core built successfully and the
test reported 113,767 symbols, not one of them the downloader's.

The cause is reachability analysis, and it took three attempts to find a marker
that survives it:

1. **A symbol** — an uncalled function is dead-code-eliminated, and an
   eliminated symbol is one `nm` cannot report.
2. **A string constant** (`SeamMarker`) — wrong by the same mechanism, but one
   step earlier: an equally-unreachable constant is dropped by the compiler
   before the linker runs. Measured on a 107 MB binary that *did* contain the
   downloader's code: the marker was absent, the module path was present.
3. **The module path** — what actually survives. The Go build system embeds it
   in the binary's own pclntab function-name table for every linked package,
   and that table is not subject to reachability analysis. It is present whether
   or not anything calls the code, which is exactly the property needed: "is it
   in the artifact" must not depend on whether it happens to be reachable.

So the test reads the binary's **bytes** for `stash-plugin-p2pdownloader`, and
`nm` is kept only as a secondary signal. The shipped check caught the bundled
mutation at `seam_test.go:227` — the seam assertion itself, not the build
guard that stands behind it.

**What the harness needed to make the bundled mutation compile**, which is
worth recording because each piece was discovered by the mutation failing:

- The plugin needs a **non-internal** package. Go's `internal/` rule means the
  core cannot import `.../internal/rpc` at all, so the obvious blank import is a
  compile error and the mutation scores `broken`, not `killed`.
- The import must be **used**, not blank. An unused import is eliminated by the
  compiler, and the plugin's code goes with it.
- The manifest's `interface:` check asserts the **value**, not the string's
  presence. `Contains(text, "interface: rpc")` is defeated by the manifest's own
  comment block, which mentions that exact string in prose — the mutation
  replaced the first occurrence, in the comment, and the test passed.

If any of the three fails, the downloader is core code and M5 is incomplete no
matter how well the transfer protocols work.

**Step 5.0a — the consent gate, before any transfer code.** The plugin may
request a locator; core decides (spec §7.1). Ship the gate first, with a test
that a plugin calling `locator.propose` on a `denied` object gets a refusal
and writes nothing. Building the transfer path first would mean retrofitting
the gate onto a component that already has every permission, which is the
failure mode §7.1 exists to prevent.

**DONE 2026-09-27.** The deciding side is `internal/collab/locator.go`; the
obeying side is `plugins/p2pdownloader/internal/rpc/consent.go`. 14/14
mutations killed by `internal/collab/mutate_locator.py`, 23/23 in the plugin's
`mutate_rpc.py`, 6/6 in `mutate_seam.py`.

**The split is two problems, and only one of them is about the database.**
Core deciding not to store a locator is the easy half. The half that is
invisible from core's side is the plugin *obeying*: the plugin holds the magnet
in memory either way, and a refused proposal followed by a transfer is exactly
§7.1's failure with the gate having worked perfectly. So the invariant is
**no transfer starts without a granted proposal for that exact locator**, and
every way of not having a grant is a refusal — including a nil answer, an
unreachable core, and a plugin built with no way to ask.

**Two refusals, not one, and the asymmetry is the owner's requirement.** A
locator may be *stored* at any tier except `quarantined` and `denied`; handing
it to a client additionally requires `third_party_permitted`. Storing a magnet
in your own library redistributes nothing, and a BitTorrent client seeds what it
downloads — so acting is redistribution and storage is not. The acting gate
re-reads the object's tier at the moment of the act, because the denormalised
tier on the locator is the value from when it was written and consent can be
revoked in between.

**The scheme check is duplicated across the module boundary on purpose.** The
plugin cannot import core's `LocatorScheme` — separate module, and importing the
core to share five constants would put a `require` in the core's `go.mod` and
make the downloader core code. So the set is written out again in the plugin,
`file:`/`ftp:`/`gopher:` are refused *by name* rather than merely unrecognised,
and `magnet:`/`ed2k://` are matched by prefix before URL parsing because neither
is a URL and `url.Parse` misreads both.

Duplication nobody can type-check needs a test, so
`TestThePluginAndCoreAgreeOnLocatorSchemes` **reads core's source** rather than
copying its list — a copied list is exactly what goes stale, and it would keep
passing after core changed. Both divergence directions are unsafe but only one
is dangerous: a scheme core accepts and the plugin refuses is merely annoying,
while a scheme the *plugin* accepts and core refuses means the plugin fetches
something the gate would not have permitted.

It immediately found a real divergence: the plugin advertised `torrent` in its
scheme list while its own parser refused it. `torrent:` is not a scheme anything
speaks — a .torrent is fetched over http or https — so it was a lie in the
error message and a trap for the next reader. **The one deliberate exception is
written out in the test rather than filtered quietly**, because a test that hides
its own exceptions is a test whose next failure gets read as a bug in the code.

And the refusals for `file:`/`ftp:`/`gopher:`/`data:`/`javascript:` are asserted
directly, independently of what core's list contains, so that adding `file` to
the plugin "to match a new core behaviour" cannot pass.

**The plugin's manifest, per `pkg/plugin/plugins.go`'s `Plugin` struct:**

```yaml
id: p2p-downloader
name: P2P Downloader
description: Fetches files over BitTorrent and ed2k into your library
version: 1.0.0
interface: rpc
exec:
  - stash-plugin-p2pdownloader
```

`tasks` is the integration surface: Stash's plugin task API already runs a
plugin operation in-process via jsRPC and streams progress, which is exactly
what a download needs, so the plugin needs no new host capability at all.

### Step 5.1 — Evaluate `anacrolix/torrent` first

**DONE 2026-09-27.** Decision recorded in
`docs/decisions/0001-torrent-library.md`: adopt `anacrolix/torrent@v1.61.0` for
the whole BitTorrent surface. Kademlia DHT, BEP 47 v2 / BEP 9 magnet metadata,
and rate limiting are all covered. Super-seeding and sparse are **not** — no
option for either exists in the module — and neither is needed for a corpus of
untracked, self-published material. ed2k is hand-rolled in 5.4 because nothing
in Go provides it.

**The finding that matters: the library's containment check is a string
check, and a symlink defeats it.** `storage.ToSafeFilePath` is documented as
"ensuring the result won't escape into parent directories". Its whole
implementation is 29 lines (`storage/safe-path.go`) and it checks whether the
**first component** of the joined path is `..`.

**Correction (2026-09-28).** An earlier version of this section listed
`/etc/passwd` and `..\..\windows` as reachable escapes, because
`ToSafeFilePath` returns them with a nil error. They are not reachable:
`filepath.Join(location, safeName)` cleans the *concatenation*, so an absolute
name is re-anchored under the location. Measured:

| Name | `ToSafeFilePath` | Lands at | Inside? |
|---|---|---|---|
| `["..", "..", "etc", "passwd"]` | refused | — | safe |
| `["/etc/passwd"]` | **accepted** | `<root>/etc/passwd` | safe — the `Join` re-anchors it |
| `["..\..\windows"]` | **accepted** | `<root>/..\..\windows` | safe, one odd filename |

The string attacks are handled by the *caller's* `Join`, not by the function's
logic. The function is still not path-safety — but the reason is the case
neither of them can address:

`file-client.go:92` looks like a defence, and `isSubFilepath` looks like the
check behind it. `isSubFilepath` is `filepath.Rel` plus a `HasPrefix`, and
`Rel` is pure string arithmetic. Executed against the library's own functions
with a torrent directory that is a symlink:

```
file path     : <root>/downloads/innocent/passwd     (innocent -> outside)
isSubFilepath -> true
really is     : <root>/outside        (EvalSymlinks)
wrote         : "<root>/downloads/innocent/passwd"
the decoy OUTSIDE the download root now reads: "PEERS CONTROLLED BYTES"
```

The library's own check said `true` and the write landed outside the download
directory. The **mmap** storage has no such check at all — `grep -c
isSubFilepath` returns 0 for `mmap.go` against 1 for `file-client.go` — and
carries the TODO *"Support all the same native filepath configuration that
NewFileOpts provides"*.

**So step 5.2 is not a parallel implementation of something the library
provides — it is the only path safety this downloader has.** That is the
sixth prose-versus-implementation mismatch in this project, and the first in a
third-party library, which is harder to notice precisely because the claim is
typed as documentation rather than as a comment.

The dependency also pulls cgo sqlite (`modernc.org/sqlite`,
`zombiezen.com/go/sqlite`, `go-llsqlite/crawshaw`) in through the storage
backends. That cost is real and is part of why the plugin ships as its own
static binary rather than being linked into the core. `anacrolix/torrent` must
appear in the plugin's `go.mod` and **not** in the core's.

### Step 5.2 — Path sanitisation FIRST, before any transfer code

**DONE 2026-09-27.** `plugins/p2pdownloader/internal/paths/sanitize.go`, with
the test written first as the plan requires. 12/12 mutations killed by
`internal/paths/mutate_paths.py`.

The checks run cheap-structural-first and filesystem-last, because a torrent
with ten thousand files in it should not pay ten thousand syscalls to be
rejected on its first component: NUL, absolute, `..` component, Windows
reserved name, trailing dot/space, then the symlink walk.

**Three things the plan's seven cases did not cover, each from a measured
gap in the library's own function:** `..\..\windows` (the same attack with the
other separator), the Windows reserved names, and **a symlink in a
subdirectory** — the first-component case is obvious enough that a check
written for it looks complete, and the attack with more innocent directories in
front of it is the same one.

**`EvalSymlinks` is applied to both sides, and the walk stops at the first
existing component.** A downloader resolves names for files that are not there
— that is the normal case — so `EvalSymlinks` on the whole path fails
constantly, and the answer is to resolve the deepest existing ancestor and
re-append the rest. Comparing a *resolved* root against an *unresolved* child
rejects every legitimate file on macOS, where `/tmp` is a symlink to
`/private/tmp`.

**Containment is `filepath.Rel`, never a string prefix**, and the difference is
a test: `/data/downloads-evil` starts with `/data/downloads` and is a different
directory. The one subtlety `Rel` introduces is that a component merely
*beginning* with dots is not a traversal — `rel` for `/a/b/..leading.dots` is
the string `..leading.dots`, and a naive `HasPrefix(rel, "..")` rejects it. The
legitimate-names test found this in the test helper first, and the production
check was already correct.

**`EnsureRoot` is separate and never destroys anything.** Creating a missing
directory is normal on first run; a root that is a file, or a symlink, or is
not creatable, is not. The property is asserted on the FILE still existing
rather than on the error returned, because a plausible "fix" for the first
version — `os.RemoveAll` then `MkdirAll` — compiles, returns nil for every
input, and deletes a user's file. The mutation is killed by
`TestEnsureRootNeverDestroysWhatIsAlreadyThere`.

**A mutation harness needs a timeout, and a `survived` row needs reading before
the code does.** For the
wiring packages, add two more:

- **A build failure is not a kill.** A mutation that does not compile proves
  nothing, and a harness that counts it as a kill reports confidence it has not
  earned. `internal/torrent`'s harness has a `SKIP` verdict for it, and it caught
  two of my own malformed probes the first time round.
- **A test that reads back your own return value cannot test a wiring.** In
  `internal/torrent` fourteen tests all asserted that the reported `Decision`
  agreed with the policy — and all fourteen passed with the upload control
  removed, because the decision is *computed from* the policy. The fix is a
  read-back of what the far side received, which the decision structurally
  cannot see. A sixth verdict makes the second half mechanical: `covered`
means no test noticed **and the whole suite still passed** with the mutation
applied, which is a verified claim that another layer refuses the same input
rather than my judgement that it does. Only `survived` — the suite failed and
the named test did not notice — means a hole. Three things here that cost more time than the code did:

- A mutation that removed a lock's `defer Unlock` **deadlocked** `go test`, and
  a harness with no timeout took the whole run with it. Every `run` is now
  bounded, and a hang reports `broken` — a verdict a human can act on.
- **Seven mutations survived, and every one was my mistake rather than the
  code's.** I pointed each at a test that happened to pass without checking
  which *layer* of the three-layer gate that test exercises. The up-front check,
  the joined-name backstop and the torrent-directory check all refuse `..`
  walks, so every test using one also passed with the others removed: two layers,
  one test, and the redundancy only apparent. Each test now names the layer it
  measures, and there is a name only that layer refuses (a trailing dot, a
  reserved device) so the layers can be killed independently.
- One mutation was **observably a no-op**: returning a live map instead of a
  copy is invisible through a `[]Refusal`, because the `range` already copied.
  That entry is gone rather than reworked — a mutation that cannot be observed
  is not a mutation, and leaving it would mean a `survived` row that looks like
  a hole and is not. The property is real, and `-race` is what covers it.

One real bug came out of it: backstop refusals are recorded with **no torrent
hash** (a name arriving through the library carries none), so every one of them
landed under the zero hash and overwrote the last. A torrent with two bad names
reported the second only. The refusals are now a slice, deduped on hash for the
up-front path and appended for the backstop path.

### Step 5.3 — BitTorrent

**IN PROGRESS. The seeding decision is DONE** —
`docs/decisions/0002-seeding-policy.md`, `internal/policy/seeding.go`,
14/14 mutations killed. The transfer surface is next.

The decision had to come first, because the library's default is the wrong one
and the default is what a downloader ships with. `ClientConfig.Seed`'s own
comment says uploading is opportunistic by default. A permissive default in a
client pointed at a corpus of untracked, self-published material means the box
publishes strangers' work without anyone having decided that it should — and
once the chunks are out, no later decision retracts them.

So §7.1's line between *storing* a locator and *acting* on it needed a third
distinction. Storing is writing the locator. Acting is starting a transfer.
**Seeding is a write to a library the operator never sees**, and it is
permitted only where a tier carries an assertion covering it:

| tier | upload |
|---|---|
| `self_published`, `performer_claimed`, `third_party_permitted` | allowed |
| `unverified` (the common case — every object starts here) | **forbidden** |
| `quarantined`, `denied` | forbidden |
| anything else | **forbidden** |

`unverified` being the common case is what makes the default the interesting
one: "nobody has objected" is not "somebody permitted this". And an
unrecognised tier value — a newer core, a hand-edited row, a truncated database
— falls to the restrictive branch, because every permissive tier is a claim by
an *identified* party.

The operator's `OperatorAllowedSeed` is an **outer bound, never an override**: it
can narrow, it cannot widen, and
`TestTheOperatorCannotWidenThePolicy` pins that because it is the direction a
settings screen invites.

Two things that are easy to get wrong and are pinned by mutation rather than by
opinion:

- **The tier strings are duplicated** across the module boundary, since the
  plugin cannot import `internal/collab`. A stale copy fails SAFE and SILENT:
  every decision falls to the restrictive branch, the downloader stops seeding
  everywhere, and nothing errors. So `TestTheTierStringsMatchTheCore` reads both
  files. **Both sides from source** — the first version wrote this file's six out
  by hand and compared against a literal count, and two mutations survived it.
  A test that checks a hand-written copy of the thing it is checking is the same
  mistake one level down.
- **`Policy` has no zero value that is safe by design.** `CanUpload()` on a zero
  `Policy` returns false for the right answer by accident, because `""` is not
  `UploadForbidden`. Hence `UploadForbiddenFor`, asserted to return
  `UploadForbidden` specifically rather than merely "cannot upload".

**The storage gate is DONE** — `internal/storage/gate.go`, 20 mutations
killed, 1 covered by another layer (verified by whole-suite re-run), 0
survivors. The library needs a storage implementation whether or not the transfer
code is written yet, and *which* one is a decision rather than a default:

| backend | containment check | verdict |
|---|---|---|
| `storage.NewFile` (classic) | `isSubFilepath` at `file-client.go:92` — a **string** check | unusable alone |
| `storage.NewMMap` | **none** (`grep -c isSubFilepath` → 0) | unusable |
| **`internal/storage.Gate`** | every raw component, then `paths.SanitizeJoin`, then the library's | **used** |

`Gate` wraps the classic backend and validates every file in a torrent **before
handing it to the library**, because the library's only extension points —
`FilePathMaker` and `TorrentDirFilePathMaker` — both return a bare `string` and
**cannot report an error**. A per-file refusal has nowhere to go, so it becomes a
sentinel filename and the transfer completes with the wrong file in it. Refusing
the whole torrent in `OpenTorrent` is the only place a real error can be
returned, and it is the only place one is used.

**The bug the tests caught while writing this one, which is the reason it is
written down.** The obvious up-front check validates the file's name as a single
string:

```go
name := filepath.Join(append([]string{info.BestName()}, file.BestPath()...)...)
paths.SanitizeJoin(root, name)   // <-- the traversal is already gone
```

`filepath.Join` **cleans** its result, so `["sub", "..", "..", "escape"]`
becomes the string `"escape"` before the gate sees it. `SanitizeJoin` is not
wrong — it is being handed a name that no longer contains the attack.
`TestAHostileNameIsCaughtEvenWhenItIsTheSecondComponent` caught this, so the gate
checks each component **as the torrent supplied it** and never pre-joins. The
joined name is only ever an output, for the error message.

That is also why the library's `ToSafeFilePath` looks adequate and is not: it
joins first and checks the first component of the **result**, which is a
different question from "does any component of the input walk out".

The refusal is **recorded, not returned as a path**. A path is indistinguishable
from success to everything downstream, so a task report has to be able to say
*which* file was refused and why — recorded once per torrent, because
`OpenTorrent` is called again on every retry and a report listing one refusal
forty times is a report nobody reads.

**DONE 2026-09-28 (wiring and validation).** `internal/torrent/` — the client
config, the consent/policy binding, the reachability defaults, and
`metainfo_check.go`. 45 mutations across THREE files, 33 killed, 12 covered by a lower
layer, 0 survivors, 0 skipped, by `internal/torrent/mutate_gate.py`.

**Four library facts found by reading the source rather than the docs, each of
which contradicted something I had written:**

1. **There is no `Client.Listen`.** Sockets are bound inside `NewClient`
   (`client.go:385-420`) and `if !cfg.NoDefaultPortForwarding { go cl.forwardPort() }`
   is there too. I had documented reachability as "deferred to `Listen`" for two
   commits. The design was right; the mechanism was invented.
2. **A DHT with both transports off binds nothing** — measured. So it is a box
   peers can find and that cannot serve them, which is worse than never joining.
   The DHT is off with the transports.
3. **`UploadRateLimiter = nil` panics** (`config.go:278` calls `.Burst()`
   unguarded on every `NewClient`), while the download side handles nil
   explicitly. `NewDefaultClientConfig` sets it to an *unlimited* limiter, so
   `rate.Inf` is the value and nil was never available.
4. **`TorrentSpec.InfoBytes` is the INNER info dict**, not a metainfo
   (`spec.go:81`). `metainfo.Load` on it fails with EOF on a good torrent.

**The library validates the same metainfo fields, LATER than the gate.** Zero
piece length, negative piece length and a short piece table all parse cleanly and
are only refused by `AddTorrentSpec`. So a nonsense torrent was being gated —
paths built, nothing reserved, accepted — and then declined by the client, with a
report saying "the library declined the torrent after the storage gate accepted
it". True, and useless: the operator needs to know it is MALFORMED, because
malformed is worth retrying against another source and a path refusal is not.
Hence `ErrMalformed`, distinct from `storage.ErrRefused`, checked before the
gate. The one check the library does not make is piece coverage — a torrent
declaring 1 GiB with one 20-byte hash parses, is added, and then the swarm stalls
with no error anywhere.

**A surviving mutation that meant the CODE was wrong.** `if info.HasV1() &&
info.HasV2() { refuse }` refused every legitimate BEP 52 hybrid, because
`HasV1()` (info.go:212) is true for any torrent with a `length` or a `pieces`
field. The mutation disabling it survived *because removing it made the code
correct*. Replaced with a `switch info.MetaVersion`.

**DONE 2026-09-28 (BEP 9 arrival).** `internal/torrent/magnet.go` — the magnet
path and the arrival path. `Client.AddMagnet` is four lines that bypass this
package entirely, so the path lives here and a grep test keeps it that way.

A magnet carries an infohash, a display name and trackers — no `files`, no
`length`. So it **cannot be gated at add time**, and the code says so rather than
running a check on an empty input. The window is acceptable for one reason:
`OnMetadata` re-runs the full sequence on arrival and **DROPS** the torrent if the
names escape the root. `Decision.Gated` is false for every magnet, and
`GatedAfterMetadata` distinguishes "the gate ran" from "never checked".

**Six of thirteen magnet mutations survived the first run**, and the causes are
the two most reusable findings in this project:

- The `AppliedSpec` record was written `DisallowDataUpload: !upload` —
  *recomputed from the policy variable* rather than read back off the spec. The
  identical tautology `AppliedSpec` was invented to end, written one file over.
- The drop assertion used `client.Torrent(hash)`, and the fixture gave the magnet
  and its metadata different hashes — so the lookup missed and "not found" was
  indistinguishable from "dropped". It passed with `d.drop` deleted. Asserted on
  `len(client.Torrents())` instead: 1 before, 0 after.
- Every URI in the zero-infohash test was rejected by `ParseMagnetUri` ITSELF, so
  the `IsZero` branch was never reached. The case that reaches it is
  `magnet:?xt=urn:btih:0000…0000` — a well-formed all-zero hash, which the parser
  accepts and `AddTorrentSpec` does not object to.

**A real bug the tier test found:** `AddMagnet` never recorded the consent tier,
so `tierOf` returned `""` and the arrival path decided every torrent as an
unrecognised tier. Restrictive — so nothing was ever published, no error was
logged, and **the downloader was inert rather than broken.**

**And a harness bug that reported its own defect as a hole:** a mutation that
produced `err redeclared in this block` was scored `SURVIVED`, because
`redeclared` was missing from `BUILD_ERRORS`. A build-error list has to be
complete; a missing entry produces a false hole that sends the next person into
the tests.

Remaining for this step: the transfer surface itself — metainfo parsing, magnet
+ BEP 9 metadata fetch, Kademlia DHT (BEP 5) discovery, peer wire protocol,
multi-connection, piece verification, resume, rate limits — with
`internal/policy` wired into the client config and the per-torrent
`AllowDataUpload` / `DisallowDataUpload` controls.

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
