> **HISTORICAL — this file records a session or plan as it stood when written.**
> The paths below (`~/code-local/go/...`, `~/code/go/...`) and the host named as `gaming-pc`
> do **not** exist on this machine. The checkout is `~/work/lane-2/stash` on **thinkcentre**;
> a second, older pair sits at `~/work/lane-1/`. For current state read
> `docs/WHATS-LEFT.md` and run `python3 docs/goal-check.py`.
> Nothing here has been rewritten, because a record of what was known then is worth more than
> a tidy path.

# Baseline — the fork before any StashForge change

Recorded at M0 so that every later milestone can prove it did not break
upstream. **The rule for every milestone: this pass count may only go up.**

## Environment

| | |
|---|---|
| Go | `go1.27.1-X:nodwarf5` |
| Node | `v26.7.0` |
| pnpm | `12.4.1` |
| Upstream commit | `c205ac8d` (fork of `stashapp/stash` @ `b6b09dd5`, develop) |
| Migrations | 88 files in `pkg/sqlite/migrations/`, highest numbered 86 |

## Test baseline

```
go test -count=1 -json ./... 2>/dev/null | <count pass/fail/skip>
```

| | |
|---|---|
| Packages `ok` | 34 |
| **Tests passing** | **887** |
| **Tests failing** | **0** |
| Tests skipped | 0 |

This is the floor, not the target. StashForge adds tests, so the count should
only ever go up. Where it stands at each tag:

| tag | unit pass/fail | integration pass/fail |
|---|---|---|
| `m0-buildable-fork` | 887 / 0 | 1191 / 1 |
| `m1-user-tables` | 887 / 0 | 1199 / 1 |
| `m1-user-store` | 911 / 0 | 1211 / 1 |

The integration failure at every row is the same pre-existing one, described
below.

`go test` prints `test result:` lines only in verbose mode. To count non-verbose,
use `-json` and tally the `Action` field — a `grep "test result"` returns zero
and looks like "no tests exist" rather than "the flag was wrong".

## Two things a clean checkout does not have

Both are gitignored, and both are required before `go build ./...` can succeed.
A fresh clone that reports a wall of `undefined: GalleryResolver` is missing
codegen, not broken source.

```bash
make generate-backend    # -> internal/api/generated_exec.go, generated_models.go
cd ui/v2.5 && pnpm run gqlgen && pnpm run build   # -> src/core/generated-graphql.ts
```

`.gitignore:22` is `internal/api/generated_*.go`. The frontend's
`src/core/generated-graphql.ts` is likewise generated from
`graphql/schema/**/*.graphql`.

`ui/ui.go` has `//go:embed v2.5/build`, so **the Go build hard-requires the
built frontend**. A stale `ui/v2.5/build` produces a binary that serves blank
pages with HTTP 200. Rebuild it before browser-verifying anything.

## The dependency fix this baseline required

`go build ./...` failed on a clean checkout:

```
$ go/pkg/mod/github.com/enetx/http2@v1.0.26/client_priority_go127.go:12:11:
    s.DisableClientPriority undefined (type *http.Server has no field or method DisableClientPriority)
```

**Root cause, corrected.** This is *not* toolchain skew against the standard
library, which is what the plan assumed before the module was read. The file
imports `github.com/enetx/http` — a vendored fork of the stdlib `net/http`,
pulled in transitively via `enetx/surf` — and the pinned `enetx/http v1.0.28`
has no `DisableClientPriority` field. `enetx/http2` ships a `go1.27`-tagged
file that requires it, so the build breaks only on a go1.27+ toolchain and
works on go1.26. It is a **transitive dependency skew**: `http2` is ahead of
the `http` version in the same module family.

The fix is the one-line upgrade in this commit:

```
github.com/enetx/http v1.0.28 => v1.0.29
```

Nothing under `$GOMODCACHE` is edited — that is a local cache, and the change
would not survive `go clean -modcache` or appear in review.

## A pre-existing integration failure, so nobody chases it

`TestStudioQueryFast` fails in `pkg/sqlite` and has nothing to do with
StashForge. Verified by running the suite at the `m0-buildable-fork` tag, before
any of these migrations existed:

| | at `m0-buildable-fork` | with M1 migrations |
|---|---|---|
| integration tests passing | 1191 | 1199 (+8 new) |
| failing | 1 | 1 (the same one) |
| skipped | 0 | 0 |

The cause is `no such function: mod`. Stash's generated ORDER BY clauses use a
deterministic scatter sort:

```sql
ORDER BY mod((studios.id + 26819649) * (studios.id + 26819649) * 52959209
             + (studios.id + 26819649) * 1047483763, 2147483647) ASC
```

but `mod` is never registered in `pkg/sqlite/driver.go` — the `ConnectHook`
registers `regexp`, `durationToTinyInt`, `basename` and `phash_distance`, and
that is all. So any `Find` query with a non-trivial filter errors at run time.
Upstream presumably runs on a build where something else supplies it, or the
test is simply not run in CI.

This is a real bug in the fork and it is recorded rather than fixed, because
fixing it is a separate change from adding user accounts and mixing them would
make this commit's diff unreviewable. The fix is one line in the `ConnectHook`:
register a deterministic integer `mod`.

## Reading order for anyone verifying this

```bash
cd ~/code-local/go/stash
make generate-backend
(cd ui/v2.5 && pnpm install --frozen-lockfile && pnpm run gqlgen && pnpm run build)
go build ./...                                   # expect: no output
go test -count=1 -json ./... | <tally>           # expect: 887 pass, 0 fail
```
