# StashForge — Goal Prompt

Paste this into a fresh session to continue the build. It is self-contained: an
agent reading only this file knows what to do next, how to verify it, and what
not to touch.

---

## The goal, in one paragraph

Turn a fork of `stashapp/stash` into **StashForge**: one binary that is a
private library manager and a self-governed public curation site at the same
time. The library's media never leaves the owner's host. The *metadata* is a
shared commons that contributes by default and opts out per library. Who may
change the commons is decided by user quorum or by moderators, never by an
owner-admin override. Plus a P2P downloader plugin (BitTorrent + ed2k over
Kademlia) that fetches into the library and gets files scanned and linked.

## Where everything is

| What | Path |
|---|---|
| Work here | `~/code-local/go/stash` |
| Publish here (per milestone) | `~/code/go/stash` |
| Spec | `~/code-local/go/stash/docs/specs/2026-09-27-stashforge-spec.md` |
| Plan (executable, per-step) | `~/code-local/go/stash/docs/specs/2026-09-27-stashforge-plan.md` |
| Upstream issue research | `~/secondbrain/10-Projects/stashforge/research/` |
| Upstream remote | `https://github.com/stashapp/stash.git` |
| Base commit | `b6b09dd5` (develop) |

**Read the plan before writing any code.** It names every file, every function
signature, and the exact command that proves each step. Do not start a step
whose verification you cannot run.

## Current state

**M0, all of M1, and M2 steps 2.1-2.3 are done**, each committed and tagged.

| | unit (pass/fail) | integration (pass/fail) |
|---|---|---|
| `m0-buildable-fork` baseline | 887 / 0 | 1191 / 1 |
| `m1-user-tables` (migrations 87-90) | 887 / 0 | 1199 / 1 |
| `m1-user-store` (auth + store) | 911 / 0 | 1211 / 1 |
| `m1-session-store` (sessions, invites) | 940 / 0 | 1211 / 1 |
| `m1-auth-wiring` (stores, adapter, factory) | 962 / 0 | 1253 / 1 |
| `m1-graphql-auth` (register/login/logout/me) | 962 / 0 | 1267 / 1 |
| `m2-governance` (rules + migrations 92-94) | 984 / 0 | 1285 / 1 |
| `m2-proposal-path` (vocabulary + proposer) | **1000 / 0** | **1287 / 1** |

`pkg/session.Store` is now an **interface**; the old concrete cookie store was
renamed `CookieStore` and kept for installs with no user database. `Authenticate`
returns a username, not a row id, because the string lands in the same context
slot upstream's signed-URL path uses.

The one integration failure at every point is `TestStudioQueryFast`, the
pre-existing unregistered-`mod` bug. See `docs/BASELINE.md`.

Three M0/M1 fixes are recorded in `docs/BASELINE.md` and were all diagnosed
wrong the first time:

1. `go get github.com/enetx/http@v1.0.29`. Not toolchain skew -- it is
   dependency skew inside the enetx module family, where `http2`'s go1.27 file
   needs a field the pinned `http` fork lacks.
2. `make generate-backend` and `pnpm run gqlgen` are mandatory; the generated
   files are gitignored, so a fresh clone reports a wall of
   `undefined: GalleryResolver`.
3. Migrations must be **contiguous** -- golang-migrate rejects a gap outright.
   StashForge's continue from upstream's 86.

`appSchemaVersion` is bumped in the same commit as any migration (86 -> 90 so
far), and a test asserts the recorded version matches it.

**M1 is complete.** M2 steps 2.1 (migrations 92-94), 2.2 (the pure governance
rules) and 2.3 (the vocabulary and the proposer) are done.

The single-use invite case the plan named as the one that matters is now tested
for real: eight goroutines redeem one `max_uses=1` key simultaneously, each in
its own COMMITTED transaction, exactly one wins. It could not use the package's
`withRollbackTxn`, which serialises the writers -- so a green test would have
proved nothing.

M2 so far is pure logic with no HTTP: `internal/collab` decides whether a change
may be proposed (closed field vocabulary, value validation, sticky rejection,
supersession) and whether it is accepted (`Evaluate(Policy, VoteCount)`). The
decision is a function of two values with no I/O, so it is tested in every
combination rather than a few hand-picked ones.

**Next up is M2 step 2.4 — applying an accepted proposal.** This is the step the
plan flags as the one where the design can go wrong: apply must be idempotent
and atomic, two workers applying the same proposal must not double-mutate or
write two audit rows, and a value that became invalid between proposal and apply
must reject the proposal rather than corrupt the target. Then 2.5 (GraphQL +
UI).

### M5 is a plugin, and that is a testable claim

The owner requires the P2P downloader to be an **easy-to-install plugin, not
core code**. The docs previously said "a plugin" while describing it as
`pkg/p2pdownloader/` in this repo, which is core code wearing a plugin's name.
The requirement is now non-negotiable #11 and is enforced by a named test rather
than by wording: the downloader is its own Go module, the core never imports it,
and `go build ./...` in the core must pass with the plugin directory deleted. If
it cannot be removed by deleting a directory, M5 is not done.

### Mutation checking is not optional here

Every security guard in `pkg/auth` is mutation-checked, because two of them were
decorative on first write and the tests did not notice. Current state: **12/12
killed**. When adding a guard, add the mutation trial too -- the procedure is in
`docs/GOAL.md`'s companion, and the two bugs it caught were:

- a lockout that recorded failures and never consulted the counter, so the
  right password still worked after five wrong ones;
- an invite use consumed by a registration that was then rejected for a taken
  username, contradicting its own doc comment.

A test that only counts "something was audited" is vacuous -- one of the spray
tests passed with the entire control deleted. Assert on the *distinction*.

## The seven milestones

| # | Delivers |
|---|---|
| M0 | Buildable fork, baseline recorded |
| M1 | Users, sessions, invite keys, rate limiting, auth migration |
| M2 | Edit proposals, quorum, moderation, audit |
| M3 | Consent, exporter (dry-run first), commons endpoint, federation |
| M4 | Mode enforcement, TLS requirement, 2FA, access grants, wizard |
| M5 | P2P downloader **as an installable plugin** (BitTorrent + ed2k + Kademlia) |
| M6 | The 850 upstream issues, capability by capability |

## Non-negotiables

1. **Upstream tests stay green, unchanged.** The pass count may only go up. A
   fork that breaks upstream has silently become a different product.
2. **Migrations are additive and CONTIGUOUS, numbered from 87.** Never edit an
   applied migration, never renumber, never reuse a number. An earlier draft of
   this file said "numbered from 1100" to leave headroom; that is wrong and
   breaks the build. golang-migrate orders by the numeric prefix and rejects a
   gap outright (`invalid migration version 88, expected 1101`). StashForge's
   continue upstream's 86. Bump `appSchemaVersion` in the same commit.
3. **SQLite only.** No Postgres, no sqlc.
4. **No stored counters.** Scores are computed from votes. A counter is the bug
   stash-box has open as #743/#9.
5. **Proposals are the only write path to shared content.** An audit test
   asserts no GraphQL resolver can edit a shared field directly. This is the
   governance invariant.
6. **The owner is not an admin over content.** `is_owner` grants user
   management, paths, settings and moderator appointment — and nothing else.
   There is no UI or resolver for overruling a quorum-accepted edit.
7. **Opt-out is a hard stop in the publish path**, not a filter on a query. A
   refactor that drops the filter must not silently resume publishing.
8. **The first sync after consent is a dry run.** Nothing is sent until the
   user confirms. A silently published private library cannot be recalled.
9. **Path sanitisation before any transfer code** (M5). A peer-supplied
   filename is untrusted input; `SanitizeJoin` and its tests land first, inside
   the plugin module, before a single byte of transfer code.
10. **Mutation-check the security guards.** Delete the quorum threshold, make
    the opt-out a no-op, remove the traversal guard — each must fail a named
    test. A guard that kills no mutant is not a guard.
11. **The P2P downloader is a PLUGIN, not core code.** It must install into a
    stock StashForge the same way any other plugin does — drop a directory
    containing `source.json` into the configured plugins path, or point the UI
    at a release URL — with **no rebuild of the main binary and no import from
    the core tree**. Concretely, which means all of it lives under
    `pkg/p2pdownloader/` in its own Go module (`go.mod` of its own), the core
    tree does not import it, and `go build ./...` in the core must still succeed
    with the plugin directory deleted. If the downloader cannot be removed by
    deleting a directory, it is core code wearing a plugin's name, and M5 is not
    done. See the check in non-negotiable #11's test,
    `TestP2PDownloaderIsNotImportedByCore`.

## Facts that must not be re-derived (already verified against source)

- **Stash has no user model.** No users table in 88 migrations (`ls
  pkg/sqlite/migrations/*.sql | wc -l`, highest numbered 86). Auth is a single
  shared username/password in `internal/manager/config/config.go:45-46`
  through `pkg/session`, plus a signed-URL path for `/scene/` streaming
  devices.
- **Stash-box is the collaborative half** and is 371 stars: `model_user.go`,
  `model_edit.go`, `model_draft.go`, invite keys, mod audit, votes. No media
  library, no scanner, no playback, no plugins.
- Stash already speaks GraphQL to stash-box via `pkg/stashbox`. That client is
  preserved and is the federation integration point.
- **StashForge's own `users` table uses `id`, not `user_id`** (migration
  87_users). Columns: `id`, `username` (COLLATE NOCASE), `password_hash`,
  `email`, `created_at`, `disabled_at`, `is_owner`, `reputation`. A fixture that
  references a column which does not exist is not a test, it is a passing
  no-op — so read the migration, do not recall the shape. (Notes about
  `user_id` / `community_members` / `information_schema` are from a *different*
  repo, Postgres-backed, and do not apply to this SQLite fork.)
- `//go:embed v2.5/build` in `ui/ui.go` means a stale UI build makes every
  route render blank with HTTP 200. Rebuild before browser-verifying anything.
- **stash#2792 is closed** (2026-05-02) and the maintainer's comments argue
  against this direction. The thread's own conclusion — "if you are even
  considering writing your own app, you might as well fork Stash" — is why
  this is a fork. Spec §1.1 has the detail. The ed2k/Kademlia idea in that
  thread is YurikaL's, and it is a P2P proposal, not a downloader.

## The two open decisions that change what gets built

Both are the owner's and both are unblocked by proceeding elsewhere:

1. **Default mode.** `contribute` (metadata shares, media does not) is
   proposed in M4. A one-line config change.
2. **Whether an author may revert their own accepted proposal.** Proposed:
   **no** — reverting is a new proposal, because silent self-revert launders a
   bad edit past the audit trail.

`QuorumThreshold=3` with `MinVoters=3` and `NewAccountProposalHold=5` are
proposed defaults, noted for the record, not blocking.

## How to report

Lead with what changed, what was verified by a real command, and what is left.
Name a milestone incomplete rather than describing it as done. If a test fails
in a way that does not match the code it claims to cover, suspect the harness
before the product: check `git status --short` for a live mutation pass, and
check whether the database is shared with a suite that truncates it.
