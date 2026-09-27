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

**M0 and M1 steps 1.1-1.3 are done**, each committed and tagged.

| | unit (pass/fail) | integration (pass/fail) |
|---|---|---|
| `m0-buildable-fork` baseline | 887 / 0 | 1191 / 1 |
| `m1-user-tables` (migrations 87-90) | 887 / 0 | 1199 / 1 |
| `m1-user-store` (auth + store) | **911 / 0** | **1211 / 1** |

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

Next up is **M1 step 1.4**: the session layer -- a `SessionStore` implementing
Stash's existing `pkg/session` surface so a legacy single-password install keeps
working, with invite create/redeem and login lockout. Then M2, the
edit/vote/draft model.

## The seven milestones

| # | Delivers |
|---|---|
| M0 | Buildable fork, baseline recorded |
| M1 | Users, sessions, invite keys, rate limiting, auth migration |
| M2 | Edit proposals, quorum, moderation, audit |
| M3 | Consent, exporter (dry-run first), commons endpoint, federation |
| M4 | Mode enforcement, TLS requirement, 2FA, access grants, wizard |
| M5 | P2P downloader plugin (BitTorrent + ed2k + Kademlia) |
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
   filename is untrusted input; `SanitizeJoin` and its tests land first.
10. **Mutation-check the security guards.** Delete the quorum threshold, make
    the opt-out a no-op, remove the traversal guard — each must fail a named
    test. A guard that kills no mutant is not a guard.

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
