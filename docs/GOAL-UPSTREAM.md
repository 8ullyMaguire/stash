# GOAL — continue solving open issues from the stash repo

**This file is the prompt for `/goal`.** Paste it whole, or run:

```
/goal continue solving open issues from stash repo
```

Everything an agent needs is in this file and in `docs/UPSTREAM-ISSUES.md`.
Nothing here assumes you were in this conversation.

---

## The goal, in one sentence

Work through the issues marked `planned` in `docs/UPSTREAM-ISSUES.md`, one at
a time, until they are all done or the queue is empty. As of 2026-10-02 there are
**84**; the header counts in both ledgers are reconciled against their tables by
`docs/check-issue-ledgers.py`, so quote that number rather than this one.

---

## Where you are

| | |
|---|---|
| Repo | `~/code-local/go/stash` |
| **Branch** | **`main`** — the soft fork. All issue work lands here. |
| Base | `b6b09dd5f` = upstream `stashapp/stash` `develop` |
| Not this branch | `stashforge` — the StashForge product work (governance, clustering, P2P plugin). Do not touch it. |
| The roster | `docs/UPSTREAM-ISSUES.md` — 675 issues, 84 planned, 553 not planned, 38 closed |
| The log | `docs/closed-issues.md` — one row per issue closed, with the test that proves it |

**`main` is a soft fork.** The point is to be worth more than upstream: real
fixes, tests, and commits a maintainer could read. If a change would make the
diff harder to review than the issue was worth, it is the wrong change.

---

## Before you touch anything: the two build facts

**1. You must generate before you build or test.**

```bash
cd ~/code-local/go/stash
git checkout main
go generate ./cmd/stash     # REQUIRED — takes ~2 min
```

Upstream gitignores `internal/api/generated_*.go` and its CI runs
`make generate` before the test job, so a fresh clone **does not compile**
until you generate. If you see `undefined: models.User` or
`undefined: BulkUpdateIds`, that is a stale or missing generated file, not a
real error. Regenerate.

**2. Do not "fix" the `enetx/http2` error by changing code.**

```
s.DisableClientPriority undefined (type *http.Server has no field...)
```

`go.mod` pins `enetx/http v1.0.29` to work around this on Go 1.27. It is
already correct. If you see it, you are on a branch without the pin, or you
regenerated `go.mod` from upstream. Restore the pin.

---

## The loop, exactly

For each issue, in the order the roster lists them:

### 1. Read the issue properly

```bash
gh issue view <N> --repo stashapp/stash
```

Read the body and the comments. The comment thread is where the actual
requirement usually is; the title frequently is not. Issues have sat open for
years and the discussion is often better than the report.

If the issue is not what its title says, **record that** in
`docs/closed-issues.md` and pick the next one. Do not build the wrong thing.

### 2. Find the code before you plan the fix

```bash
# where is it? -- grep for the nouns in the issue, in the code, not the docs
grep -rn "<the thing>" --include=*.go internal/ pkg/ | head -20
grep -rn "<the thing>" ui/v2.5/src/ | head -20
```

Then read the call site. M6's own log records the trap: four of the sites
doing zip extraction looked identical and two of them were already safe,
because they resolved paths differently. Reading the call site is not
optional.

### 3. Write the failing test first

**A test that passes on unfixed code is not evidence.** The check that it
fails first is not optional either — run it, see it fail, then fix.

```bash
go test ./the/package/ -run TestTheThing -v   # MUST FAIL
# ... now write the fix ...
go test ./the/package/ -run TestTheThing -v   # MUST PASS
```

Name the test after the property, not the function.
`TestAHostileNameIsRefusedWithoutAskingTheSource`, not `TestSanitize`.

### 4. Prove the test can fail

This is the step that is skipped and it is the one that matters. Break the
fix on purpose, one mutation at a time, and confirm the test notices:

```bash
# invert a condition, change a constant, drop a guard -- then re-run
go test ./the/package/ -run TestTheThing -v   # MUST FAIL
# restore
```

If the test still passes with the fix broken, **the test is wrong, not the
fix**. Rebuild the test. Two real examples from this repo: a guard that no
test covered, and a test that passed straight through a change because it
asserted too loosely.

### 5. The gate — all of it, every time

```bash
go generate ./cmd/stash
go build ./...            # clean
go vet ./...              # clean
go test ./...             # the pass count may only go UP, never down
gofmt -l internal/ pkg/   # empty
```

`go test ./...` on a clean `main` is **34 packages ok, 0 failures, after
generate**. If you see fewer packages ok, you are on the wrong branch. If the
count is lower than it was before your change, something you did removed
coverage.

### 6. Commit, one issue per commit

```
fix(<area>): stash#<N> -- <what was wrong, in one clause>

<the bug, in two or three sentences: what the code did, what it should
have done, and the test that proves the difference>

closes #<N>   (upstream issue number, not this repo's)
```

Reference the **upstream** number (`stash#7240`), so the history is readable
against the issue tracker. The stash#7240 commit is the shape to copy.

### 7. Record it

One row in `docs/closed-issues.md`:

| issue | title | the fix | the test that proves it | commit |

**If one issue closes several**, name every number. And the honest summary of
this work is "N issues closed, each with a test that fails without the fix" —
never "N issues fixed" with no test named.

---

## When to stop and ask

Stop, record it, move on. Do not half-build any of these:

- **The issue needs a new first-class object** (a new entity type, with
  schema, GraphQL type, scan rule, detail page and filters). That is a
  project. The roster already deferred 5 of these; if a *planned* one turns
  out to need it, that is a finding — record it as deferred with the reason.
- **The issue needs a subsystem** (a new storage format, a new pipeline, a
  new auth model). Same.
- **The issue is a product decision** rather than a defect ("build stash for
  a particular kind of thing", "make this configurable for everyone").
- **The fix and the test both want to be in the same file** and you cannot
  find the seam. Record what you found.

Recording "this one is a project, and here is why" is a real result. Half of
one is not.

---

## What not to do

- **Do not re-derive the roster.** It is measured, and the rules and counts
  are in the file. If an issue is missing or wrong, fix the roster and say so
  in the commit; do not quietly skip it.
- **Do not work on `stashforge`.** That branch holds the StashForge product
  work — governance, clustering, the P2P plugin, and a large spec. Issue work
  goes on `main`. Mixing them makes both diffs unreviewable.
- **Do not reformat, rename or tidy code you are not fixing.** In a soft fork
  an unrelated diff is a review tax on someone who is trying to read a bugfix.
- **Do not add a dependency** without recording why in the commit. The
  dependency set is upstream's and it is load-bearing.
- **Do not disable or relax a test to make the suite green.** If an upstream
  test fails because your fix changed behaviour that test asserted, the test
  was asserting a bug — say so explicitly in the commit and in
  `docs/closed-issues.md`.
- **Do not claim a count you cannot name.** "12 issues fixed" is meaningless
  without the twelve numbers. The pass count going up is the real progress
  signal; the issue count is the bookkeeping.

---

## The existing state, so you do not redo it

Two issues are already fixed on `main` and are in the log:

| issue | what |
|---|---|
| `stash#7240` | zip-slip arbitrary file write on import and on package install. `pkg/fsutil/safepath.go` is the shared gate. |
| `stash#7152` | nil deref aborting a whole stash-box batch job. |

Both came with tests, and both fix commits record an audit of the other call
sites that looked similar and turned out to be safe. Read
`docs/ZIP-EXTRACTION-AUDIT.md` before touching archive extraction — it is the
reason the second half of #7240 is a two-line change and not a rewrite.

---

## Where the numbers are

- 675 open issues at `stashapp/stash`, fetched 2026-09-29.
- **84 planned**, 553 not planned or deferred, every one with a reason in the roster.
- 34 packages, all green, as the starting point.

Re-fetch and the count will have moved; that is normal. The roster is a
snapshot with a date on it. If the live count differs materially from 675,
say so before continuing — it may mean issues were closed upstream, which is
worth knowing.
