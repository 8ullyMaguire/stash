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

**M0, all of M1, and M2 steps 2.1-2.4 are done**, each committed and tagged.
The suite is now fully green: no failing test at any milestone.

| | unit (pass/fail) | integration (pass/fail) |
|---|---|---|
| `m0-buildable-fork` baseline | 887 / 0 | 1191 / 1 |
| `m1-user-tables` (migrations 87-90) | 887 / 0 | 1199 / 1 |
| `m1-user-store` (auth + store) | 911 / 0 | 1211 / 1 |
| `m1-session-store` (sessions, invites) | 940 / 0 | 1211 / 1 |
| `m1-auth-wiring` (stores, adapter, factory) | 962 / 0 | 1253 / 1 |
| `m1-graphql-auth` (register/login/logout/me) | 962 / 0 | 1267 / 1 |
| `m2-governance` (rules + migrations 92-94) | 984 / 0 | 1285 / 1 |
| `m2-proposal-path` (vocabulary + proposer) | 1000 / 0 | 1287 / 1 |
| `m2-apply-path` (applier + sqlite targets) | 969 / 0 | **2287 / 0** |

The `1` failure in every row above is the same test, and it is now fixed. The
counts jump sharply in the last row because that commit also fixed the
long-standing `TestStudioQueryFast` failure -- see the note below.

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

**M2 step 2.4 (the apply path) is done.** Apply is idempotent and atomic: the
write is a single `UPDATE ... WHERE col IS ?`, so of N concurrent workers
exactly one matches and the losers report already-correct having done nothing.
One accepted proposal produces exactly one audit row, however many workers run.
A value that became invalid between proposal and apply is rejected without
touching the target.

Then **M2 step 2.5 — GraphQL + UI**, which is the last step in M2. **Done**
(`4e43ea0f`, `836cbb08`).

**M2b — governance v2 (plan step 2.4a) is done** (`b74e6c15`, `44867817`,
`b278253a`, `505a5780`). The flat acceptance arithmetic is no longer the
primary path: `Evaluate` was *extended* rather than replaced, with
`Policy.Weighted` zero-valued by `DefaultPolicy`, so M2's behaviour is
unchanged by M2b's arrival and every mode remains reachable.

What landed: the five-role table (`roles.go`), reputation-weighted ballots with
decay and Sybil damping (`weighting.go`), the weighted decision function
(`evaluate_weighted.go`), per-field reputation persistence (migration 95 plus
the store), and the role wiring in the resolvers.

Four bugs the tests caught, all silent — the kind that leave a green suite and
a governance system that does nothing:

1. The basis-point scale was off by 4x, so every voter looked like a newcomer
   and the weighting did nothing at all. Now pinned by an explicit property.
2. Sybil damping compared against the wrong neighbour: `i-1%len(basis)` parses
   as `i-(1%len)`, not `(i-1)%len`, so the "is this ballot the same as its
   group" test was meaningless.
3. Excluding the author's ballot also decremented the distinct-voter count, so
   `MinVoters` became a tool for excluding authors from their own proposals'
   quorum. Same shape as #2's neighbour: a filter applied to the wrong
   collection, where the intent was to move a sum and not a count.
4. A test asserted the wrong sign, so it asserted nothing about weighting.

The recurring lesson is #2 and #3 as one idea: in governance code, *sum* and
*count* are different quantities, and a change to one that was meant to affect
the other is invisible because the arithmetic still produces a plausible
number.

Two permission checks were also mutation-tested, because a permission test that
has never been observed to fail is not evidence the permission is unchanged:

* `TestRoleOfAgreesWithM2ModeratorPredicate` fails, naming the affected user,
  if the role table stops matching M2's `isModerator` for any user the database
  can represent.
* The reputation floor and the migration's CHECK and per-field primary key all
  fail loudly when removed.

Next is whatever the plan lists after step 2.4a.

### What step 2.5 found: the governance layer was unreachable

`collab.ProposalStore` had **no implementation anywhere in the tree**. The
`Proposer`, the sticky-rejection rule, the vocabulary, the acceptance
arithmetic — all of it was callable only by its own unit tests, running against
fakes. M2 was green throughout: 962 unit and 2287 integration tests, and nothing
in the application could reach the governance logic.

**A pure module with fake-backed tests verifies its logic and nothing about
whether anything can call it.** The tests were not wrong; they answered a
different question. Step 2.5 was the first step that had to reach the code from
outside the package, and that is what exposed it.

Two bugs the write-path test found in itself, worth keeping as a rule:

- The first version matched writer calls **by method name**, which flagged
  `SceneMarkerStore.UpdateTags` — a user tagging their own bookmark, which is
  personal state, not a claim about shared content. A test that fails on
  legitimate code gets deleted, taking the real check with it. Now matched by
  **receiver type**, with `sharedFieldStores` an explicit list so "is this
  shared?" is a stated judgement rather than an accident of naming.
- The scanner then passed while matching **nothing**: `stripImports` used an
  anchored `(?s)` pattern that ate most of any file without an import block, and
  the declaration regex consumed the trailing `Store` while the comparison
  looked for `SceneStore`. `TestWritePathDetectorCatchesAViolation` is the
  meta-test that caught both, asserting known-answer snippets in both
  directions. A detector never observed to fail is a detector whose failure mode
  is silence.

Also fixed: `internal/collab/apply_test.go`'s fake still lacked `DeciderID`, so
**`go test ./...` did not compile at HEAD**. Committed in `7a34d330`, never
rebuilt since the decider fix.

### Four bugs the apply path exposed, and one it did not

The apply step is where a design meets a database, and four defects surfaced
that no amount of pure-logic testing would have found:

1. **The sqlite proposal store validated nothing.** `ValidateValue` lived only
   in the collab service, so any caller reaching the store directly wrote rows
   no proposer is allowed to write. A rule enforced one layer up is one call
   path from being skipped.
2. **`ReadField` scanned into a pointer to an anonymous struct** and silently
   returned NULL for every value, with no error. Apply then believed every
   target already held the proposed value and no-opped on everything. This is
   why the tests read the value back through the applier rather than trusting
   the returned outcome.
3. **`MarkRejected` hardcoded `decided_by = 0`**, which is a foreign key, so
   every rejection lost both its status change and its audit row.
4. **Rejecting an *accepted* proposal whose target had been deleted overwrote
   its status to `rejected`.** The status column says what people decided; three
   people approving an edit is still an approval after the row is gone.
   `ErrAlreadyDecided` is now tolerated and the audit row records the failure to
   apply.

The fifth, found while fixing the suite's last red test, is worse and older.

### The random sort was not random, and never had been

`getRandomSort` shipped calling `mod()`, a Postgres function SQLite does not
have, so every `random_*` sort failed at runtime. The comment directly above it
had always specified the `%` operator, so the SQL had drifted from its own
documentation. `TestStudioQueryFast` was the suite's one permanently red test.

Fixing that exposed the real defect. The polynomial `x*x*P1 + x*P2` evaluates
to about 3.8e22 against an int64 ceiling of 9.2e18, so the arithmetic
overflowed for **every** id; SQLite degraded it to float64, and at that
magnitude the trailing `% 2147483647` became a no-op because the float spacing
exceeds the modulus. Every row's sort key came back as the constant `1`.
A "random" sort that silently returned id order, for every seed, on every
table — with valid SQL and no error.

The fix reduces `x` modulo 417314, the largest value for which the product
still fits int64, before squaring. The old `% = 1e8` seed cap existed only to
keep the polynomial in range; with the reduction moved to the per-id term it
became harmful (seeds a period apart would produce near-identical orders) and
was removed.

It survived because `getRandomSort` had no test of its own. The new tests
execute the fragment against a real connection and assert `typeof(key)` is
`integer` — that single assertion is what turns a silent overflow into a
failure.

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

### Step 2.4b.9: the pass, and the seam that was never closed

`internal/cluster` now has a production entry point. `cluster.Pass` drives
filter → candidates → assign → guard → consolidate → persist over real
512-wide embeddings (`pass.go`), through a narrow `Store` interface declared in
the package rather than importing `pkg/sqlite` — layering runs database-below-
domain, so a pass that reached for the store would be the one place in the tree
where the arrows point the wrong way. `observe.go` is the half that talks to the
outside world: decode a frame, detect, crop, embed, skip what is unreadable and
say which file it was. `task.FaceClusteringJob` is the `job.JobExec` wrapper, and
`faceClustering: ID!` is the mutation.

**The store implemented three of the four methods the pass's interface needs,
and no build reported it.** `AddMember` took sqlite's own `Member` rather than
`cluster.StoredMember`, and `MergeCluster` — the only method that is not a single
statement — did not exist at all. Every test in the tree passed: the pass was
tested against a fake store, the store against its own methods, and *each half
testing alone is not a claim that the halves fit together*. Nothing imported both
packages, so the compiler never had to compare them. The fix is
`ClusterStoreAdapter` with a `var _ cluster.Store = (*ClusterStoreAdapter)(nil)`
assertion in a **non-test** file, so every build checks the seam from now on.

This is step 2.5's lesson, arriving a second time and in the same shape: a pure
module with fake-backed tests verifies its logic and nothing about whether
anything can call it. Rule worth keeping — **the first thing to write for a new
module is a test that constructs it from outside**, against the real
implementation of whatever it will be given. It takes ten minutes and it is the
only thing that would have caught this.

Two more, both silent:

1. **`MergeLimit: 0` documents "no cap"; `consolidateBounded` read it as a hard
   limit of zero.** The loop never ran, the pass reported "merged 0, refused 0,
   skipped 0", and nothing errored. Found because mutation M12 *survived* — a
   surviving mutant means a region has no test, and the region it pointed at
   turned out to be dead code. Still the right way to find these.
2. **One bad frame abandoned the rest of the file.** The sample loop `break`ed
   on a frame error, so a single undecodable frame meant the closing credits
   were never looked at — which is precisely what the sample budget exists to
   reach. A per-sample failure is a skip; only an unreadable *target* stops a
   target.

**Consolidate cannot merge anything assign separated, and that is structural.**
The two stages' conditions are the same inequality, negated. Assign keeps
clusters apart when the loser's nearest member is farther than the join
threshold from the winner's centroid; `absorb` merges only when every loser
member is *closer* than that same threshold from that same centroid. A pair that
survived assign is exactly a pair absorb refuses. Verified by sweep — angles
65–100° against join thresholds 0.3–0.6 and merge thresholds to 1.5 produced no
merge at any setting, and the one shape that could satisfy both (an outlier
dragging the centroid across the gap) still fails because absorb measures the
*member*, not the centroid. Pre-existing, not introduced here, and pinned by
`TestPassConsolidateCannotMergeWhatAssignSeparated` so a future change to absorb
fails loudly. The pass's `MergeThreshold` knob is therefore a control that can
never admit a merge, which is a real problem and a **design decision for the
owner**: fixing it means relaxing the guard's re-check for merges specifically,
trading a safety property for a working merge path.

**The test fixture was lying about the geometry.** `angVec` set two components
and left 510 at zero — a 512-wide vector that was 2-dimensional. In a 2-plane,
renormalising a near-zero mean gave a face-to-centroid distance of 1.02, larger
than the 2.0 maximum for a true cosine. Every probe disagreed with the last for
that reason. Worth stating as a rule: **a fixture that spans fewer dimensions
than the thing it stands for will produce numbers that are arithmetically
impossible and still compare as plausible.**

Also made: `*job.Progress` is now nil-safe, which is what makes every job in the
tree testable (`updater` is unexported, so a test in any other package cannot
build a real `Progress`). `ExecuteTask` on a nil progress still **runs the work** —
an early return there is a job that reports success having done nothing, which is
the worst failure a job can have.

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
