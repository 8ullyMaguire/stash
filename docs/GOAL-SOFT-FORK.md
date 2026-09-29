# main — the soft fork

**This branch is upstream `stashapp/stash` plus fixes for upstream issues, and
nothing else.** Base: `b6b09dd5f` (upstream `develop`).

The StashForge product work — governance, face clustering, the P2P downloader
plugin, and its spec — is on the `stashforge` branch, with its own
`docs/GOAL.md`. Do not mix the two: a soft fork whose diff contains a
governance kernel is a soft fork nobody can review.

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
go test ./...             # 34 packages ok, 0 failures -- may only go UP
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
