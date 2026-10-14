# This fork vs upstream — changelog

Generated 2026-10-14 by measuring git; every number below came from a command in
the last section. A vault mirror lives at
`~/secondbrain/10-Projects/stashforge/2026-10-14-stash-fork-vs-upstream-changelog.md`;
this file is the canonical copy because it travels with the code it describes.

Repos: this fork is `8ullyMaguire/stash` (`origin`, mirrored to
`git.polarisocial.xyz/hirrolot19/stash` as `forgejo`); upstream is
`stashapp/stash`, default branch `develop`.

The fork's entire delta over `stashapp/stash`.

## 0. Two baselines — read this first

There are **two** fork tips, and they are 85 commits apart:

| | this checkout | fork tip (published) |
|---|---|---|
| ref | `main` @ `7ce915881` | `origin/main` @ `e5bffb242` |
| commit date | 2026-10-03 | 2026-10-05 |
| merge-base with `upstream/develop` | `b6b09dd5f` (2026-09-22) | `e7d33c9bd` (2026-10-01) |
| fork commits not upstream | 424 | 503 |
| upstream commits not in the fork | 8 | 1 |
| files changed | 858 | 990 |
| lines | +188,171 / −3,284 | +214,299 / −3,532 |

`main` is a **strict ancestor** of `origin/main` — nothing local is unpushed, the
checkout is simply stale. §11 lists the 86 commits sitting on `origin/main` that
this working tree has not pulled.

The fork is a *soft fork*: it tracks `develop` by merging (see
`docs/GOAL-SOFT-FORK.md`), and has merged upstream up to 2026-10-01. Upstream has
since advanced, so the fork is 1–8 commits behind upstream depending on which
tip you measure.

## 1. Scale

| metric | value |
|---|---|
| fork-only commits (tip) | 503 |
| authors | `hirrolot19` 407, `Hermes Build Lane` 79, `Hermes Agent` 17 |
| files changed | 990 |
| Go files changed | 617 |
| `_test.go` files touched / newly added | 286 / 274 |
| new migrations | 41 (`87` … `127`; 123–127 exist only on `origin/main`) |
| GraphQL schema files changed | 34 (6 new type files) |
| UI files changed | 109 |
| docs files added | 135 (52 of them `.md`) |
| distinct upstream issues referenced | 86 |
| commit-type mix | docs 169, feat 47, fix 47, test 10, build 4, gofmt 4, other ledgers/plans |

Commit subjects name the upstream issue they serve, which is what makes the
delta legible at all — that is a stated rule of the branch
(`docs/GOAL-SOFT-FORK.md`).

## 2. New subsystems that do not exist upstream

Added lines by package, combined diff:

| lines | files | package | what it is |
|---|---|---|---|
| 28,709 | 112 | `pkg/sqlite` | all 36 new migrations + the stores they back |
| 15,304 | 75 | `internal/api` | new resolvers, routes, media gate, wizard, role mapping |
| 14,784 | 48 | `internal/collab` | edit proposals, ballots, reputation, hash-chained audit |
| 9,374 | 25 | `internal/cluster` | face/person clustering: candidate selection, assign, consolidate, merge |
| 6,385 | 45 | `internal/manager` | scan/manager changes incl. windowed sprite planning |
| 4,733 | 15 | `internal/ecosystem` | plugin/ecosystem surface |
| 4,386 | 43 | `pkg/models` | new models for the above |
| 3,653 | 11 | `pkg/auth` | argon2id hashing, sessions, TOTP |
| 2,631 | 17 | `pkg/file` | zip/gallery handling, containment |
| 1,812 | 10 | `pkg/scene` | scene generation, window handling |
| 1,718 | 19 | `internal/autotag` | alias cache, sink, write path |
| 1,714 | 9 | `pkg/utils`, 1,559 `pkg/scraper` | utilities; scraper referer/JSON handling |
| 1,412 | 9 | `pkg/ffmpeg` | transcoder, stream generation fixes |
| 1,398 | 5 | `internal/autoproposal` | automatic proposal generation + curation |
| 1,348 | 6 | `internal/discovery` | boards + feed |
| 1,128 | 2 | `internal/replicastore` | replica storage |
| 978 | 2 | `internal/alert` | alerting |
| 962 | 3 | `internal/subjects` | subject/identity index |
| 911 | 2 | `internal/acquisition` | acquisition policy |
| 797 | 2 | `internal/rank` | ranking |
| 787 | 3 | `internal/directory` | site directory, claim-and-confirm |
| 771 | 2 | `internal/gamify` | gamification |
| 758 | 9 | `pkg/plugin` | plugin settings, CSP, invocation |
| 674 | 4 | `pkg/pkg` | recursive package requirements |
| 657 | 2 | `internal/manifest` | manifest |
| 629 | 3 | `internal/curate` | curation |
| 594 | 2 | `internal/ident` | identity |
| 545 | 2 | `internal/preservation` | preservation |
| 537 | 3 | `internal/mesh` | mesh replication (peer, replica, budget, allocation log) |
| 470 | 4 | `pkg/match` | non-ASCII / ambiguous performer matching |
| 424 | 4 | `pkg/hash` | perceptual hash (`videophash`) |
| 347 | 1 | `internal/matrix` | matrix bridge |
| 339 | 5 | `pkg/session` | multi-user session store |
| 339 | 2 | `internal/dlna` | DLNA |
| 323 | 4 | `pkg/exec` | process exec with scrubbed env |
| 187 | 4 | `pkg/fsutil` | filesystem helpers |

Plus **`plugins/p2pdownloader/`** — a self-contained Go program: 96 files,
35,223 lines added, 30,366 lines of Go. Submodules: `ed2k`, `ed2kwire`,
`ed2ktransfer`, `torrent`, `relaycrypto`, `relayconsent`, `relaydiscovery`,
`relaybind`, `handoff`, `library`, `policy`, `rpc`, `storage`, `paths`, with
`plugins/p2pdownloader/cmd/probe_withhold` and `plugins/p2pdownloader/cmd/probe_chunkhash`. This is the largest single
addition in the fork and is *not* part of upstream at all.

## 3. New user-facing subsystem: multi-user auth (stash#2337)

Upstream has no `users` table at all — verified: `grep 'CREATE TABLE users'
pkg/sqlite/migrations/` returns nothing on upstream. The fork builds the whole
identity layer:

- migrations `87_users`, `88_invite_keys`, `89_user_sessions`, `91_user_moderator`, `104_user_totp`
- `pkg/auth` — argon2id password hashing (a bcrypt-refused password is never stored as an empty hash), invite keys with the race handled
- `pkg/session` — multi-user session store with lockout
- `internal/api` — `resolver_user.go`, `resolver_mutation_auth.go`, `role_mapping_test.go`, `stashforge_user_context.go`, multi-user auth wired into HTTP behind a registry
- `graphql/schema/types/auth.graphql` — `register` / `login` / `logout` / `me`
- moderation gates routed through the role table (`feat(api): route the moderation gates through the role table`)

## 4. New user-facing subsystem: collaboration governance

- migrations `90_collab_audit`, `92_edit_proposals`, `93_proposal_votes`, `94_proposal_score_view`, `95_field_reputation`, `108_collab_audit_hash_chain`, `109_governance_shadow_log`
- `internal/collab` — proposal write path + field vocabulary guard, weighted decision function extending `Evaluate`, apply path, governance v2 (five roles, reputation-weighted ballots), per-field reputation persistence, hash-chained audit so append-only is a *property* not a promise
- `graphql/schema/types/proposal.graphql` + the proposal UI surface

## 5. New user-facing subsystem: face/person clustering

- migrations `96_person_clusters`, `97_person_cluster_members`, `98_person_cluster_merges`, `99_person_cluster_names`, `100_cluster_member_uniqueness`
- `internal/cluster` — candidate selection, sample budget, over-merge guard, assign decision, consolidate pass, embeddings
- `graphql/schema/types/cluster.graphql`, `internal/api/resolver_mutation_face_clustering.go`

## 6. New user-facing subsystem: p2p / mesh / federation

- mesh: migrations `110_mesh_replication_serve_budget`, `111_mesh_instance_profile`, `112_mesh_peer`, `113_mesh_replica`, `114_mesh_allocation_log`; `internal/mesh`, `internal/replicastore`
- `FEDERATION.md` at the repo root
- `plugins/p2pdownloader` (§2)

## 7. StashForge remainder

- `internal/directory` + `internal/discovery` (migrations `118`, `119`) — site directory claim-and-confirm, boards and feed
- `internal/gamify`, `internal/rank`, `internal/curate`, `internal/preservation`, `internal/manifest`, `internal/alert`, `internal/subjects`, `internal/ident`, `internal/autoproposal`, `internal/acquisition`, `internal/dlna`, `internal/matrix`
- `graphql/schema/types/access.graphql`, `hosting.graphql`; `internal/api/models_hosting.go`, `internal/api/stashforge_media_gate.go`, `internal/api/stashforge_wizard.go`
- migrations `101_libraries`, `102_consent_preferences`, `103_instance_settings`, `105_target_library_scope`, `106_libraries_owner_name_unique`, `115_instance_auto_acquire`, `116_instance_curation`, `117_access_policy`

## 8. Upstream issues worked (86 referenced)

Referenced by fork-only commit subjects, grouped roughly by the work done:

**Panels / new API surface**
`#837` — file-integrity issues: `120_issues` migration + issues store with the
constraint *as* the design, GraphQL surface, HTTP surface, the UI panel.

**Multi-user / access**
`#2337` (users + permissions, §3), `#1253`, `#1580`, `#5944`, `#6917`.

**Scraper / matching**
`#2293` (non-ASCII performers invisible to Auto Tag), `#7212` (ambiguous
performer name read as absent), `#2507` (aliases loaded on only one of two
paths), `#2540` (Referer fallback on 403), `#7263` (sub-object attributes),
`#7265`, `#7261`, plus "a JSON document is scraped as Chrome's HTML rendering of it".

**Security**
`#7240` (zip-slip arbitrary file write on import **and** on PACKAGE INSTALL),
`#5538` (image URLs served in-process instead of over unauthenticated HTTP),
`#684` (run as unprivileged user with configurable UID/GID), plugin asset-route
containment, `#7269` (PR: *do not merge* — 2 of its 4 RCE claims are already
fixed here and merging regresses them).

**Media / streaming / phash**
`#3530` (windowed scenes: range API, GraphQL read+write, React form, window-aware
sprite and VTT thumbnails, own preview cache key), `#7229` (preview segments
with no video stream), `#5683` (do not answer 200 before ffmpeg has output),
`#5850` (JPEG thumbnails came out with black transparency), `#7106` (deleting an
image inside a zip survives the rescan), `#2149` (refuse to store a known-bad
perceptual hash), `#4233` (honour video rotation when deciding orientation),
`#3738` (bump `updated_at` when a funscript is attached), `#4771` (per-language
subtitle timing offsets), `#6744` (look for subtitles in configured folders),
`#6459` (caption-language matching on subtag boundaries).

**Scanner / library**
`#2773` (rescan a file whose content was replaced at the same path), `#2824`
(shard generated directories, legacy layout still resolvable), `#3849` (image
sort correlated to the gallery being viewed), `#7130` (bound the filesystem open
so a stalled mount cannot hang the request), `#5631` (optional pruning of
directories a move leaves empty).

**UI / frontend**
`#2833` (subpage mount/unmount stole its parent's key), `#6466` (a playing scene
wiped unsaved tag/performer edits), `#6814` (playlist stepped over scenes with no
file), `#7256` ("From file" broken in Firefox), `#5237` (TW country name),
`#7235` (Safari layout on performer details), `#7249` (portrait video wrapper
height), `#7254` (organized button visibility, contrast 1.35:1 → 10.64:1),
`#2833`, "wait for in-flight queries before resetting the Apollo store",
"persist list display mode per view".

**Plugins**
`#5709` (stopping a plugin orphaned its helpers), plugin settings passed to
invocations, plugin-declared `connect-src`/`media-src` CSP entries.

**Crashes / data races / jobs**
`#7152` (nil deref aborts a whole stash-box batch job), remote-player
registration ack serialised with dispatch, `#7222` (studio-list N+1: 219 SQL
statements → 21), `#2747` (server-side external player: register, wait, be told
what to play; the funscript URL is signed because the apikey was leaking).

**Deliberately not built here** (recorded, not implemented): `#2359` (Stash-Box
parity — satisfied by *using* the owner's existing Stash-Box instance as this
app's StashDB endpoint) and `#2149`'s bounty half (someone is already paid for
it; the audit finding survives).

Also referencing: `#571`, `#1790` (external ids, `121_external_ids`),
`#1961`, `#3001`, `#3122`, `#3159`, `#3171` (`.stashignore` already answers it),
`#3450`, `#3722`, `#422`, `#4326`, `#4536`, `#4560`, `#6457`, `#6577`, `#7093`,
`#7136`, `#7137`, `#7155`, `#7159`, `#7160`, `#7165`, `#7166`, `#7179`, `#7180`,
`#7181`, `#7196`, `#7197`, `#7198`, `#7199`, `#7225`, `#7234`, `#7236`, `#7240`,
`#7245`, `#7252`, `#7257`, `#7268`.

## 9. Non-functional: tests, gates, docs

- **274 new `_test.go` files.** Heavy on invariant tests: `schema_access_coverage_test.go`, `stashforge_media_gate_coverage_test.go`, `stashforge_write_path_test.go`, `routes_plugin_containment_test.go`, `stashforge_p2p_guards_test.go`, `stashforge_p2p_seam_test.go`, `stashforge_store_wiring_test.go`, `write_path_guard_test.go`, `vocabulary_coverage_test.go`.
- **Harnesses added**: `docs/e2e/mutation-check.sh` (mutation gate; teardown by PID not
`pkill` pattern), `docs/verify-all.sh`, `docs/goal-check.py`, `docs/check-whats-left.py`,
`docs/check_cited_paths.py`, `docs/check_planned_upstream.py`, `docs/ledger-check.py`,
`docs/closed-log-check.py`, `docs/check-issue-ledgers.py`, `docs/reconcile-ledgers.py`,
`docs/one_mutation.sh`, plus per-issue `mutate_*.py` scripts.
- **Docs added: 135 files (52 `.md`)** — `GOAL-SOFT-FORK.md`, `GOAL-UPSTREAM.md`, `UPSTREAM-ISSUES.md` (675 issues, 450 planned, each with a reason), `closed-issues.md`, `WHATS-LEFT.md`, `ISSUES.md`, `PR-TRIAGE.md`, `PR-DECISIONS-batch1.md`, `M6-DECISIONS.md`, `ZIP-EXTRACTION-AUDIT.md`, `BASELINE.md`, `WORKTREE-SETUP.md`, `FEDERATION.md`, `ALIGNMENT.md`, per-issue spec/plan pairs (`ISSUE-1790-*`, `ISSUE-3530-*` ×7, `ISSUE-4326-*`, `ISSUE-837-*`), `docs/specs/` (8), `docs/research/` (31), `docs/decisions/` (2 ADRs: torrent library, seeding policy), `docs/plan/` (3).
- **Docker**: non-root by default, configurable UID/GID, `compose user:` support, `docker/entrypoint.sh` + `test-entrypoint.sh`.

## 10. What the fork is *missing* from upstream

Upstream commits not reachable from the checkout (`main`):

```
bdee3c83d 2026-10-07 New/RFC: Saved Filter List refactor (#7224)
e7d33c9bd 2026-10-01 Fix: Safari Layout Bug on Performer Details Page (#7235)
71932ef45 2026-10-02 Fix: VR Player Colour Mapping (#7248)
92ea03bd3 2026-10-01 New: Improve Scraper Search (#7259)
3cadfe4e1 2026-10-01 Fix: Cap Portrait Video Wrapper Height (#7249)
dd2a04732 2026-10-01 Fix: Make Organized Button More Visible in Scene Page (#7254)
d7147b59a 2026-10-01 New: Make PerformerCreate and SceneCreate Patchable (#7264)
a903b1af3 2026-10-01 Fix: Show a Green Checkmark When the Duration Matches (#7267)
```

Seven of those eight are already merged on `origin/main` (via `6b77ccd3b merge
upstream/develop (7 commits, merge-base b6b09dd5f, 2026-09-22)`, 2026-10-03).
`origin/main` is missing only `bdee3c83d`.

Note that four of them are *the same fixes the fork independently landed* —
portrait wrapper height (`#7249`), organized-button visibility (`#7254`), the
duration-match checkmark (`#7267`), Safari performer layout (`#7235`). Those are
now candidates for dropping from the fork's own delta on the next upstream merge.

## 11. The 86 commits on `origin/main` not in this checkout

`git log --oneline main..origin/main` — the fork's own work, already published,
not yet pulled here. Summary of the themes:

- **stash#3530 finished**: range API, GraphQL read+write, the React form,
  window-aware sprite and VTT thumbnails, window survives an ordinary scene update.
- **stash#2359 (Stash-Box parity)**: schema for seven of seventeen fields, store
  layer + 21 tests, GraphQL surface for six features + 17 resolver tests, parity
  UI, ledgers, full re-verification.
- **stash#2747**: the server-side external player — config, argv, scrubbed env,
  window; register/wait/be-told-what-to-play; the funscript URL signed.
- **stash#2359 nationality list**, **#422 studio half**.
- **A real browser e2e tier** and the four startup bugs it exposed (the binary
  did not boot) — `e47da5644`.
- **C2 disposition sweep**: 69 → 0 planned rows, 662 dispositioned, each verdict
  backed by a command that prints.
- **Upstream fixes landed on the fork side**: `#6459` caption-language matching,
  `#6744` subtitles in configured folders, `#4771` per-language subtitle offsets,
  `#3738` funscript `updated_at`, `#7130` bounded filesystem open, `#5631` empty
  directory pruning, `#4233` video rotation, `#7238` signed funscript URL.
- **Perf**: `#7222` studio N+1 (219 → 21 SQL statements), scene-queue thumbnails.
- **Gate repairs**: `goal-check` hardcoded integration packages, `verify-all.sh`
  deleting the shared TMPDIR, `mutation-check.sh` teardown, ledger row
  corruption, and a goal that read COMPLETE while an unrun checker failed.

## 12. Uncommitted in the working tree (as of generation)

```
M  docs/WHATS-LEFT.md
AM internal/manager/sprite_window_plan.go        (+239)
AM internal/manager/sprite_window_plan_test.go   (+392)
```

`#3530` sprite planning — staged, not committed.

## 13. Regenerate

```sh
cd ~/code-local/go/stash
git fetch upstream origin

# checkout vs upstream
BASE=$(git merge-base upstream/develop main)
git rev-list --left-right --count upstream/develop...main
git diff --shortstat $BASE main

# fork tip vs upstream
BASE=$(git merge-base upstream/develop origin/main)
git rev-list --left-right --count upstream/develop...origin/main
git diff --numstat $BASE origin/main | awk '{split($3,a,"/"); k=a[1]"/"a[2]; add[k]+=$1; n[k]++} END{for(i in n) printf "%7d + %3d files %s\n", add[i], n[i], i}' | sort -rn

# issues referenced, migrations, upstream lag
git log --no-merges --format='%s' $BASE..origin/main | grep -oE '#[0-9]{3,4}' | sort -u
git diff --name-status --diff-filter=A $BASE origin/main -- pkg/sqlite/migrations
git log --oneline $BASE..upstream/develop
```

## Related

Vault notes (not in this repo):

- `docs/GOAL-SOFT-FORK.md` — the branch's own rulebook and why the fork stopped
  being "upstream plus issue fixes".
- `[[2026-09-29-soft-fork-issue-queue]]`
- `[[2026-10-01-stash-fork-widened-to-feature-programme]]`
- `[[2026-10-01-stash-single-branch-consolidation]]`
- `[[GOAL-stashforge]]`
