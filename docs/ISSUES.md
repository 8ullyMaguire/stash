# Backlog issue ledger — 17 issues, upstream milestone `Backlog`

One row per issue. Generated from `gh issue list --repo stashapp/stash
--milestone Backlog --state open` on 2026-10-01; full issue bodies cached at
`~/.hermes/profiles/coding-3/cache/scratch/backlog-17.json`. The plan that
describes the work per issue is `docs/plan/BACKLOG-17.md`.

`State` values: `open` (not started), `in progress`, `done` (built and proven),
`skipped` (deliberately not built — reason given, never silently).

| # | title | labels | verified state | disposition | state |
|---|---|---|---|---|---|
| 571 | Support for multiple performer images | feature request | partial — `PerformerIds []string` exists on `models.Image`; no per-performer list or UI | build: model + UI over the existing join | open |
| 837 | Log potential issues with files, show in a dedicated UI | feature request | absent — no issues table, model, or resolver | build: new table + model + detection + UI panel | open |
| 1253 | Separating tags for higher-level objects | feature request | partial — scene and gallery have `LoadTagIDs`; movie and marker do not | build: extend the gallery pattern to movie and marker | open |
| 1580 | DLNA folders: recently added, viewed, unplayed | feature request | partial — DLNA resolvers exist; the folders do not | build: three read-only virtual containers | open |
| 1790 | Generalized support for external IDs | feature request, bounty | absent — `StashIDs` is a stash-specific type | build: `ExternalID` with a source registry, migrating `StashIDs` | open |
| 2149 | Phash validation | bug report, bounty | bug present — `pkg/file/scan.go:869` drops the fingerprint *type* without checking the *value* | **skipped** — bounty-labelled, someone is paid for it. Root cause recorded in `BACKLOG-17.md` | skipped |
| 2293 | Non-ASCII performers fail to be tagged with Auto Tag | bug report | **bug confirmed and fixed** — `pkg/match/path.go` `getPathWords` dropped 1-rune words. Not the collation: verified `NOCASE` against the real driver | **done** — commit `e8ae81674`, tests fail on the pre-fix filter | done |
| 2337 | Support multiple users with configurable permissions | feature request | **built** — verified 2026-10-01, and the recorded state was WRONG. It said "absent — no `users` table exists"; migration `87_users.up.sql` has existed since M1. G1 users+sessions (argon2id, hashed session tokens, invite keys, rate limiting); G2 permissions (`is_owner` partial-unique-indexed, `is_moderator` migration 91, `collab.ResolveRole` deriving the five-role ladder); G3 per-user scoping via `LibraryAccessStore.Decide/Grant/Revoke` | **done** — verified by commit `f40acdd53`: 48 auth/session/access tests pass under the integration tag | **done** |
integration tag). All three sub-features shipped. G1 users+sessions (argon2id, hashed session tokens, invite keys, rate limiting). G2 permissions: `is_owner` partial-unique-indexed, `is_moderator` (migration 91), and `collab.ResolveRole` deriving the five-role ladder rather than storing it. G3 per-user views: `LibraryAccessStore.Decide/Grant/Revoke` scopes media per user. 48 auth/access tests green | done |
| 2359 | [Meta] Update Stash to be in line with Stash-Box | feature request | satisfied by configuration — owner runs a Stash-Box instance | **skipped** — point the app at that StashDB; a local reimplementation duplicates a codebase that already exists | skipped |
| 2507 | Support performer alias(es) in Auto Tag task | feature request | **fixed 2026-10-02** — the recorded state was PARTLY WRONG in a way that mattered. `getPerformerTaggers` ignores aliases (upstream disabled it with "TODO - disabled until we can have finer control over alias matching"), AND `task_autotag.go` loaded aliases in the single-id branch only — so auto-tagging ONE performer by id had its aliases available while auto-tagging ALL (`"*"`, what the UI uses) did not. The alias load now sits in the shared per-performer loop, exactly where `autoTagStudios` has always had it; studios were never affected, which is the evidence it was a bug and not a design choice | **fixed** — alias load moved to the shared per-item loop. `getPerformerTaggers` still ignores aliases: re-enabling wholesale is a separate decision about matching precision, and is now ONE EDIT away because the aliases are loaded on every path | **partial** |
| 2747 | Jellyfin-like external remote player support | feature request | absent — an earlier grep hit was a false positive | build: config-driven command template, detached, scrubbed env | open |
| 2833 | `e` keyboard shortcut collides with subpages | bug report | bug present — shortcuts are global, subpages cannot override | build: scoped shortcut resolution, page handlers win | open |
| 3001 | Adding `File.Destroy.Post` hook | feature request | absent — no file event hook system | build: minimal register/dispatch, non-fatal errors | open |
| 3122 | Create All/New/Missing on Scene Tagger page | feature request | absent in the UI | build: three batched bulk actions with per-action counts | open |
| 3450 | Ability to use relative dates for date-specific filters | feature request | absent — the date criterion takes a fixed value | build: additive `RelativeDateCriterion`, resolved in user timezone | open |
| 3530 | Support multiple scenes in a single file | feature request | absent — the model is one scene per file row | build: segments, segment-aware duration and filters | open |
| 4326 | Ability to browse related content during video playback | feature request | absent | build: overlay panel, no route change, playback uninterrupted | open |

**Totals: 17 — 1 done, 14 to build, 2 deliberately skipped.** No row claims
`done` until a test exists that fails without the change.
