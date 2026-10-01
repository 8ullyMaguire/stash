# main — the feature fork

**This branch is upstream `stashapp/stash` plus a large body of new
functionality.** Base: `b6b09dd5f` (upstream `develop`).

On 2026-10-01 the owner widened this from "upstream plus issue fixes, and
nothing else" to what it is now. The old line was the correct constraint while
the diff was a bugfix queue; it is the wrong one now, because the queue contains
subsystems that do not exist upstream at all. Two of the 17 are the clearest
case: #2337 (multiple users with configurable permissions) requires building an
entire identity layer on a codebase that has **no `users` table** — verified,
`grep 'CREATE TABLE users' pkg/sqlite/migrations/` returns nothing. #2359 is
Stash-Box parity, which is this owner's other project.

The new rule is the same discipline aimed at a bigger target:

- **New functionality is welcome; upstream-mergeable is not the bar.** Commits
  still state which issue they serve, so the diff against upstream stays
  legible.
- **Never remove functionality the spec claims.** The spec is cumulative.
  Widening scope must not cost a single existing clause.
- **A test that passes on unfixed code is not evidence.** Unchanged, and now
  load-bearing for whole new subsystems rather than bugfixes.

## The 17-issue programme

`docs/plan/BACKLOG-17.md` is the plan: one section per issue, each with the
verified current state, the work, and the command that proves it. `docs/ISSUES.md`
is the ledger — one row per issue with its disposition, updated as work lands.
`docs/plan-verify.py` and `docs/goal-check.py` are the predicates.

Two issues are deliberately not built here, and both reasons are recorded:

- **#2359 (Stash-Box parity)** is satisfied by *using* the owner's existing
  Stash-Box instance as this app's StashDB endpoint. Building the parity locally
  would duplicate an entire second codebase.
- **#2149 (phash validation)** has a `bounty` label — someone is already being
  paid for it. The audit found the mechanism, and `BACKLOG-17.md` records it, so
  the finding survives even though the build does not.

The StashForge product work — governance, face clustering, the P2P downloader
plugin, and its spec — is still on the `stashforge` branch, with its own
`docs/GOAL.md`. Keep it there. A branch whose diff mixes a governance kernel with
a feature programme is a branch nobody can review.

## Start here

| If you want to | Read |
|---|---|
| **work on this branch** | `docs/GOAL-UPSTREAM.md` — the `/goal` prompt and the full loop |
| see the queue | `docs/UPSTREAM-ISSUES.md` — 675 issues, 450 planned, every one with a reason |
| see what is done | `docs/closed-issues.md` — one row per issue, with the test that proves it |
| understand a past fix | `docs/M6-DECISIONS.md`, `docs/ZIP-EXTRACTION-AUDIT.md` |

```
/goal continue solving open issues from stash repo
```

## Build: two facts that will waste your time if you do not know them

**1. Generate before building or testing.**

```bash
go generate ./cmd/stash     # ~2 min, REQUIRED
go build ./...
```

Upstream gitignores `internal/api/generated_*.go` and runs `make generate` in
CI before the test job, so **a fresh clone does not compile until you
generate**. `undefined: models.User` means a missing or stale generated file,
not a real error.

**2. The `enetx/http2` error is already fixed. Do not "fix" it again.**

```
s.DisableClientPriority undefined (type *http.Server has no field...)
```

`go.mod` pins `enetx/http v1.0.29` for Go 1.27. Upstream's own `v1.0.28` fails
on this toolchain — verified, not assumed. If you see the error, something
regenerated `go.mod` from upstream.

## The gate, every time

```bash
go generate ./cmd/stash
go build ./...            # clean
go vet ./...              # clean
go test ./...             # 38 packages ok, 0 failures -- may only go UP
gofmt -l internal/ pkg/   # empty
```

## Ground rules

- **One issue per commit**, titled `fix(<area>): stash#<N> -- <what was wrong>`.
- **A test that passes on unfixed code is not evidence.** Break the fix on
  purpose and confirm the test notices.
- **No unrelated diffs.** A reformat in a bugfix commit is a review tax.
- **When an issue turns out to be a project rather than an issue, record it
  and move on.** A half-built subsystem is worth nothing; the finding is worth
  something.
- **No count without names.** "12 issues fixed" means nothing without the
  twelve numbers and their tests.
