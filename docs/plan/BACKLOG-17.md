# The 17-issue backlog programme

Branch `main` of the stash fork. Upstream `stashapp/stash`, milestone `Backlog`,
17 open issues, fetched 2026-10-01 and cached at
`~/.hermes/profiles/coding-3/cache/scratch/backlog-17.json`.

Every "current state" below was **verified against the code on 2026-10-01**, not
inferred from the issue title. Where the title and the code disagree, the code
wins and the disagreement is written down.

## The gate, every time

```bash
cd ~/code-local/go/stash && export GOFLAGS=-mod=mod
go generate ./cmd/stash      # ~1 min, REQUIRED after any schema or gql change
go build ./...               # must be clean
go vet ./...                 # must be clean
go test ./...                # 38 ok, 0 failures at baseline -- may only go UP
gofmt -l internal/ pkg/      # empty
```

Baseline recorded: 38 ok packages, 0 failures, on commit `a2cb7c0e7`. If a
change makes the ok-count go *down*, that change deleted a package's tests.

## Two rules that outrank convenience

1. **A test that passes before the fix is not evidence.** Break the behaviour on
   purpose, run the test, watch it fail, restore. Do this for every item that
   claims a bug is fixed. For new subsystems there is no "before", so the test
   must instead be shown to *fail to compile or fail assertions* against a
   stubbed-out implementation.
2. **Do not touch `docs/GOAL-SOFT-FORK.md`'s existing clauses.** The spec is
   cumulative. Scope was widened, never reduced.

---

## Phase 0 — audit (done, 2026-10-01)

Findings, in the order the work should go. `?` means "not yet started".

| # | title | verified state | plan |
|---|---|---|---|
| 571 | multiple performer images | partial | [A](#a-571) |
| 837 | log file issues, show in UI | absent | [B](#b-837) |
| 1253 | tags on higher-level objects | partial | [C](#c-1253) |
| 1580 | DLNA folders | partial | [D](#d-1580) |
| 1790 | generalized external IDs | absent | [E](#e-1790) |
| 2149 | phash validation | **skip** (bounty) | recorded below |
| 2293 | non-ASCII performers fail Auto Tag | **fixed** (`e8ae81674`) | [F](#f-2293) |
| 2337 | multiple users, configurable permissions | absent, large | [G](#g-2337) |
| 2359 | Stash-Box parity | satisfied by pointing at the owner's Stash-Box | recorded below |
| 2507 | performer alias in Auto Tag | absent | [H](#h-2507) |
| 2747 | external remote player | absent | [I](#i-2747) |
| 2833 | `e` shortcut collides with subpages | bug present | [J](#j-2833) |
| 3001 | `File.Destroy.Post` hook | absent | [K](#k-3001) |
| 3122 | Create All/New/Missing on tagger page | absent | [L](#l-3122) |
| 3450 | relative date filters | absent | [M](#m-3450) |
| 3530 | multiple scenes in one file | absent | [N](#n-3530) |
| 4326 | related content during playback | absent | [O](#o-4326) |

### Not built, and why

**#2149 (phash validation).** Has a `bounty` label; someone is paid for it. The
mechanism was still found and is worth keeping: a broken or unparsable file
yields a *common* phash, which StashDB then matches to whatever scene shares it.
Upstream's own comment gives the concrete bad values, `f8fcfcfcf8e00100
f8fcfcfcfc700000`. The place to fix it is `pkg/file/scan.go:869`, which already
special-cases `models.FingerprintTypePhash` and drops the fingerprint — it
removes the *type* without checking the *value*. A future contributor should add
a value check against a deny-list there.

**#2359 (Stash-Box parity).** Asks for studio codes, performer scene alias,
director, multiple URLs, split fields. The owner runs a Stash-Box instance
already. Configuring this app to use it as the StashDB endpoint delivers the
feature; duplicating the fields locally would maintain a second implementation
of a codebase that already exists and is already the owner's.

---

## A. #571 — multiple performer images

**Verified state.** `pkg/models/image.go:88` has `PerformerIds []string`, so
images already reference performers as a list. What does **not** exist is a
per-performer image list on the performer itself, nor a UI to manage one. The
image is the join; nothing on the performer side enumerates them.

**Work.** Add `Images []models.Image` to `models.Performer` in
`pkg/models/model_performer.go`, populated by the existing performer loader
alongside `LoadAliases`. Persist nothing new — the join already exists. Then the
UI: in `ui/v2.5/src/components/Performers/Performers.tsx` add a strip of image
thumbnails to the performer detail pane, each clickable to open
`ui/v2.5/src/components/Images/ImageGrid.tsx`.

**Proves it.**

```bash
go test ./pkg/models/ -run TestPerformerImages -v   # multiple images, both directions
cd ui/v2.5 && npx jest src/components/Performers     # strip renders, dedupes by id
```

---

## B. #837 — log potential file issues, surface them in the UI

**Verified state.** Nothing. `ls pkg/models | grep -i issue` is empty; there is
no issues table, no model, no resolver.

**Work.** New migration in `pkg/sqlite/migrations/` — name it after the highest
existing number. Table `file_issues (id, file_id, kind, detail, created_at,
resolved_at)`. Model `pkg/models/file_issue.go`. Detection belongs in
`pkg/file/scan.go` next to the existing phash special-case: unparsable
`ffprobe` output, a file that exists but cannot be opened, a fingerprint that
fails validation. GraphQL: `internal/api/resolver_query_file_issue.go` and a
mutation to dismiss one. UI panel listing open issues with a "dismiss" action.

**Proves it.**

```bash
go test ./pkg/sqlite/ -run TestFileIssue -v   # create, list, dismiss
go test ./pkg/file/ -run TestScanDetectsIssues -v
```

---

## C. #1253 — tags for higher-level objects

**Verified state.** Partial. `pkg/models/model_scene.go:152` has
`LoadTagIDs`, and 9 call sites exist across models, and
`pkg/models/model_gallery.go` has it too. `model_movie.go` and
`model_marker.go` do **not**. So galleries and scenes have tags; movies and
markers do not.

**Work.** Extend to `model_movie.go` and `model_marker.go`, mirroring
`model_gallery.go` exactly — that is the reference implementation. Then the
`tags` table needs a `movie_id`/`marker_id` column, or a generic owner column;
prefer whatever the gallery precedent did so the query layer stays uniform.

**Proves it.**

```bash
go test ./pkg/models/ -run TestTagLoad -v     # scene, gallery, movie, marker
```

---

## D. #1580 — DLNA "recently added / viewed / unplayed" folders

**Verified state.** DLNA exists: `internal/api/resolver_mutation_dlna.go` and
`internal/api/resolver_query_dlna.go`. The *folders* the issue asks for do not.

**Work.** The DLNA server lives in its own package. Add three virtual
containers to whatever enumerates DLNA directories, each backed by a query:
recently added = scenes ordered by created date; recently viewed = needs a view
timestamp (check whether `scenes` already has `last_played`; if not, add it and
write it from the stream handler); unplayed = same minus viewed. Keep it read-only.

**Proves it.**

```bash
go test ./internal/manager/ -run TestDLNAFolders -v
```

---

## E. #1790 — generalized support for external IDs

**Verified state.** Absent. `ls pkg/models | grep -i external` is empty. Every
performer currently stores `StashIDs` and `RelatedStashIDs` as a stash-specific
type — `pkg/models/model_performer.go:40,96,126`. That is exactly the
one-provider shape this issue asks to generalise.

**Work.** New `pkg/models/external_id.go`:

```go
type ExternalID struct {
    ID         string       `json:"id"`
    Source     string       `json:"source"`
    SourceName string       `json:"source_name"`
    URL        *string      `json:"url,omitempty"`
}

type ExternalIDs []ExternalID

func (e ExternalIDs) Get(source string) *ExternalID
func (e ExternalIDs) Set(source, id string, url *string)
```

Store as a JSON column. Migrate `StashIDs` on the way so existing data is not
lost. Sources are registry-driven, not hard-coded: the first source is
`stashdb`, then whatever metadata providers get added. Every provider that
currently writes `StashID` must move to the new API, or the two will drift.

**Proves it.**

```bash
go test ./pkg/models/ -run TestExternalID -v
go test ./pkg/sqlite/ -run TestExternalIDMigration -v   # existing StashIDs survive
```

---

## F. #2293 — non-ASCII performers fail Auto Tag  ← the confirmed bug

**Status: DONE — commit `e8ae81674`, 2026-10-01.**

**Verified state, and this one is proven rather than believed.** The issue
reports that Japanese/Chinese performers "appear invalid to the auto taggers".
A comment on the issue names the cause: *"names with single unicode letters will
not match due to a performance optimisation."*

The optimisation is in `pkg/match/path.go`, `getPathWords`:

```go
if utf8.RuneCountInString(w) > 1 {
    ret = sliceutil.AppendUnique(ret, string([]rune(w)[0:2]))
}
```

Two defects, both real:

1. A word of exactly **one** rune is **dropped**. `桜` never enters the word
   list, so the SQL query for autotag candidates never sees it and the tagger
   ignores the path.
2. A word of **three or more** runes is **truncated to two**, so the query key is
   only a prefix.

`pkg/match/pathwords_test.go` already has 8 tests around this function,
including `TestSingleRuneWordsAreStillDropped` — which pins the current
behaviour. **That test encodes the bug.** It must be rewritten, not deleted
silently, and its rename and new expectation called out in the commit.

**A hypothesis I tested and rejected:** I first assumed `COLLATE NOCASE` was at
fault, since it is ASCII-only. Running it against the real driver
(`github.com/mattn/go-sqlite3`, per `go.mod:43`) says otherwise:

```
NOCASE exact 'björk'     -> [1]
NOCASE upper  'BJÖRK'    -> []      <-- folds ASCII only
NOCASE upper  'ÉMILIE'   -> [2]     <-- but this one folds
NOCASE mixed  'bJöRk'    -> [1]
```

`NOCASE` is genuinely ASCII-only, but it is **not** the reported bug, because
autotag matching does not go through a case-insensitive SQL `IN` on the final
name — it goes through `QueryForAutoTag` on *word prefixes*. Do not "fix" the
collation. `NATURAL_CI` does not exist in this driver; registering it would be
a much larger change than the bug needs.

**Work — as built.** The filter is asymmetric rather than removed, because the
two halves of the old condition protect different things:

```go
r := []rune(w)
if len(r) == 0 {
    continue
}
if len(r) == 1 && len(w) == 1 {
    continue          // lone ASCII letter: noise, keep filtering it
}
frag := string(r[0:2])
if len(r) == 1 {
    frag = w          // lone non-ASCII rune: a whole NAME, keep it intact
}
ret = sliceutil.AppendUnique(ret, frag)
```

`len(r) == 1 && len(w) == 1` is the ASCII test without a second helper: a
one-rune string whose byte length is also 1 is ASCII by definition.

The `frag` line is load-bearing and was found by the test, not by reading. Taking
`r[0:2]` of a one-element rune slice yields that element plus a NUL byte past the
end, so a one-rune name was emitted as `"\u05e7\x00"` and matched nothing. The
first version of this fix passed the guard and then failed on exactly that, which
is the third instance in this file of the same NUL trap the existing test comments
warn about. Keep whole short words; never slice a one-element slice.

**Proves it — done.** `go test ./pkg/match/` passes all 8 pathword tests, and the
two new ones were run against a restored pre-fix filter and observed to fail:

```
--- FAIL: TestTheRuneThresholdIsCountedInRunesNotBytes  does not contain "\u4f0f"
--- FAIL: TestASingleRuneWordIsDroppedOnlyWhenItIsASCII  does not contain "\u4f0f" / "\u0401" / "\u3042"
```

`TestSingleRuneWordsAreStillDropped` still passes unchanged, which is the point:
ASCII short words are still noise and are still filtered.

## G. #2337 — multiple users with configurable permissions  ← the large one

**Verified state.** Absent, and this is the biggest item by an order of
magnitude. `grep 'CREATE TABLE users' pkg/sqlite/migrations/` returns **nothing**
— there is no user table in this codebase. `internal/api/authentication.go`
exists but is single-user session handling for one operator.

This is the issue that justified widening the fork's scope. Build it in layers,
each independently shippable and independently tested. **Do not attempt all of it
in one sitting** — a half-built identity layer is worse than none.

### G1 — users table and sessions

Migration: `users (id, username UNIQUE, password_hash, created_at, disabled_at)`,
`user_sessions (id, user_id, token, created_at, expires_at, last_seen_at)`. Hash
with bcrypt at cost 12, never store a plaintext or a fast hash. `pkg/models/user.go`.
Config gets a `bootstrap` admin so a fresh install is not bricked. Migrate the
existing single-operator config into that admin row rather than maintaining two
auth paths — one path, or the permission checks will be wrong in one of them.

```bash
go test ./pkg/sqlite/ -run TestUser -v
go test ./internal/auth/ -run TestSession -v    # expiry, revocation, timing
```

### G2 — permission model

A permission is `(resource, action)`. Roles are named bundles. `pkg/permissions`
evaluates them; a check is a single call on the request context. Default role for
a new user is the least-privileged one that can still do something, never admin.
Every existing `internal/api` resolver that assumed one operator must be audited
— a resolver that reads without a permission check is a leak, and this is the
step where that gets found.

```bash
go test ./pkg/permissions/ -v
```

### G3 — per-user views

Where the UI is global, make it per-user. Preferences table keyed by user id.
Owned data (custom scenes, organized flags) is *not* per-user by default —
decide that explicitly and write it in the spec, because the other choice is a
rewrite later.

```bash
cd ui/v2.5 && npx jest src/components/Settings
```

**Proves the whole issue.** A non-admin user cannot reach a mutation a
non-permission grants, and cannot see another user's session. One integration
test that logs in as two different users and asserts both directions.

---

## H. #2507 — performer alias(es) in the Auto Tag task

**Verified state.** The model supports it — `pkg/models/model_performer.go:37`
has `Aliases RelatedStrings`. But `pkg/match/path.go` has alias matching
**explicitly commented out** inside `PathToPerformers`:

```go
// TODO - disabled alias matching until we can get finer
// control over the matching
```

Note the asymmetry: `PathToStudio` and `PathToTags` both match aliases. Only
performers has it switched off. So this is enabling existing code, not writing
new logic.

**Work.** Re-enable, with the "finer control" the TODO wants: add a config flag
`autotag.performers.use_aliases`, default **on**, and log when a match came from
an alias so a wrong match is diagnosable.

**Proves it.**

```bash
go test ./pkg/match/ -run TestPerformerAlias -v
```

Test both that an alias matches and that the flag-off path does not.

---

## I. #2747 — Jellyfin-like external remote player

**Verified state.** Absent. The earlier grep hit on `internal/manager/scene.go`
was a false positive — no player, VLC, or mpv reference exists there.

**Work.** Config block with a command template. Stream handler spawns it detached
with the file path and the configured range, passing through neither credentials
nor cookies. Surface the PID so it can be killed. Never auto-download an
external binary; make the path configurable and document it. A player launched
by the server is a server-side process — it inherits the server's privileges, so
give it a scrubbed environment and an absolute path.

```bash
go test ./internal/manager/ -run TestExternalPlayer -v
```

---

## J. #2833 — `e` keyboard shortcut collides with subpages

**Verified state.** Bug present upstream, in the UI. The shortcut table is
global; subpages that use `e` for something else get shadowed.

**Work.** Shortcut resolution becomes scoped: a page declares its own handlers,
which win over global ones, and unbound keys fall through. Find the shortcut
table under `ui/v2.5/src/components/List/` and the key handler that registers
it.

**Proves it.** UI test that dispatches `e` on a subpage and asserts the subpage
handler ran, not the global one.

```bash
cd ui/v2.5 && npx jest src/components/List -t "shortcut"
```

---

## K. #3001 — `File.Destroy.Post` hook

**Verified state.** Absent. No hook system for file events.

**Work.** A minimal hook registry, not a plugin framework: a `Register` function
and a `Dispatch` call at the file-destroy point, invoked **after** the row is
gone so hooks see the final state. Errors from a hook are logged, never fatal —
one bad subscriber must not block a delete. This is deliberately the smallest
useful shape, so #3001's follow-ups have somewhere to grow.

**Proves it.**

```bash
go test ./pkg/file/ -run TestDestroyHook -v   # fires, fires after, errors non-fatal
```

---

## L. #3122 — Create All/New/Missing on the Scene Tagger page

**Verified state.** Absent in the UI.

**Work.** The Tagger page already lists scenes for a path. Add three bulk
actions, each a batched mutation: **All** = every candidate, **New** = candidates
with no organized flag, **Missing** = candidates that exist but have no metadata
filled. Batch in chunks, and report a count per action — the spec's own rule is
no count without names.

**Proves it.**

```bash
cd ui/v2.5 && npx jest src/components/Tagger
```

---

## M. #3450 — relative dates in date filters

**Verified state.** Absent. The date criterion takes a fixed value.

**Work.** A `RelativeDateCriterion` alongside the existing one: parse
`today`, `yesterday`, `-7d`, `this week`, `last month` into an absolute
`DateTime` range **at query time**, in the user's timezone — not in UTC, or
"today" is wrong for most of the world. Keep the existing absolute criterion
working; this is additive, and the spec forbids removing existing behaviour.

**Proves it.**

```bash
go test ./pkg/sqlite/ -run TestRelativeDate -v   # incl. a DST boundary
```

---

## N. #3530 — multiple scenes in one file

**Verified state.** Absent. The data model is one scene per file row.

**Work.** This is a deep change and the riskiest item. A file that contains two
concatenated scenes is currently one scene whose duration is wrong. Add a
`scene_count` or a boundary list, make the player treat a file as a set of
segments, and make every duration/path filter aware of segments. Decide early
whether a file's scenes are one organized entity or several — the answer changes
the UI, and picking late means a migration that duplicates data.

**Proves it.**

```bash
go test ./pkg/sqlite/ -run TestMultiSceneFile -v
go test ./pkg/file/ -run TestSegmentDuration -v
```

---

## O. #4326 — related content during playback

**Verified state.** Absent.

**Work.** While a scene plays, offer related content without leaving the
player: a side panel listing scenes sharing performers, a studio, or a tag,
sourced from the existing query layer. Overlay, not a route change, so playback
is not interrupted. Respect the existing autoplay/loop state.

**Proves it.**

```bash
cd ui/v2.5 && npx jest src/components/Player -t "related"
```

---

## Order of work

Phases, in dependency order. Each is independently shippable.

1. **F (#2293)** — a confirmed bug with a confirmed root cause, small, and it
   establishes the break-the-fix discipline the rest of the programme follows.
2. **H (#2507)** — re-enabling commented-out code, so it should be quick.
3. **C (#1253)** — extend an existing pattern, gallery is the template.
4. **M (#3450)** — additive query feature, timezone is the whole difficulty.
5. **K (#3001)** — small, and it gives later work an extension point.
6. **B (#837)** — new table plus new model plus new UI.
7. **A (#571)** — the join exists, so this is model + UI.
8. **J (#2833)** — UI only.
9. **D (#1580)** — read-only virtual folders.
10. **L (#3122)** — UI bulk actions.
11. **E (#1790)** — the one that touches every provider; do it when the others
    are green so a regression has one suspect.
12. **I (#2747)** — process spawning, so it needs its own review.
13. **O (#4326)** — UI overlay.
14. **N (#3530)** — the risky one, on a clean tree.
15. **G1→G3 (#2337)** — the identity layer, last, because it is the one that
    should be built once the rest of the app is stable enough to have users.

**#2149 and #2359 are not built.** Both are recorded above with the reason, so
the finding is not lost and the omission is deliberate rather than an oversight.
