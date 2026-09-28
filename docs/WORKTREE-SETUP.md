# Worktree setup — read this before the first `go build`

Recorded 2026-09-27 by the M6 pass. A fresh worktree of this repo **does not
build** until two gitignored paths are populated. This cost the first twenty
minutes of that pass, so it is written down rather than rediscovered.

## 1. `ui/v2.5/build` — the `//go:embed` trap

`ui/ui.go:10` carries `//go:embed v2.5/build`. The directory is a build
artifact and is gitignored, so a new worktree has no `build/` and the build
fails with:

```text
ui/ui.go:10:12: pattern v2.5/build: no matching files found
```

This is not a stale-UI symptom — GOAL.md's note about a stale build making
routes render blank with HTTP 200 is the *other* half of this trap. Here the
build simply stops.

Fix, from the primary checkout:

```bash
rsync -a --delete ~/code-local/go/stash/ui/v2.5/build/ \
      ~/code-local/worktrees/m6/ui/v2.5/build/
```

Only needed for `go build` / `go test` to compile the embed. A full
`pnpm run build` in the worktree is needed only when UI code changes.

## 2. `internal/api/generated_*.go` — GraphQL codegen

`.gitignore:22` ignores `internal/api/generated_*.go`. Two files are needed:
`generated_exec.go` and `generated_models.go`. Without them the build fails
with a wall of `undefined: BulkUpdateIds`, `undefined: GalleryResolver` and so
on — which reads like broken source but is only missing codegen.

Fix, from the primary checkout (far cheaper than `make generate-backend`,
which also touches the UI):

```bash
rsync -a --include='generated_*.go' --exclude='*' \
      ~/code-local/go/stash/internal/api/ \
      ~/code-local/worktrees/m6/internal/api/
```

Re-run this after any `.graphql` schema change, or the new resolvers are
simply absent and the build fails in a confusing place.

## 3. `ui/v2.5/node_modules`

Absent in the worktree; `node_modules` is gitignored. The `ui/v2.5` project
uses pnpm with a hoisted linker, so from the worktree:

```bash
cd ~/code-local/worktrees/m6/ui/v2.5 && pnpm install
```

The pinned build command is `node node_modules/vite/bin/vite.js build`, not
`pnpm build`.

## Verify all three

```bash
cd ~/code-local/worktrees/m6
go build ./...      # no output
```

That line printing nothing is the whole gate. Any `pattern v2.5/build` or
`undefined: GalleryResolver` output means one of the two rsyncs was skipped.
