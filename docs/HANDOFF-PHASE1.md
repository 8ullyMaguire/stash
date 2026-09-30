# HANDOFF — stashforge `/goal`, session of 2026-09-30 (phase 1)

**Written at the limit, so this is a handoff and not a summary.** The next
session's first action is in "The first action" below and it is not a reading
task.

## Environment

```
host        cachyos-B450, Linux 7.2.6-1-cachyos, 30 GB RAM, load ~3 at session start
repo        ~/code-local/go/stash            (primary, on `main`)
my worktree ~/code-local/worktrees/upstream (branch `goal/upstream`)
upstream    https://github.com/stashapp/stash.git
origin      https://github.com/8ullyMaguire/stash.git
gh          authenticated as 8ullyMaguire (repo, workflow, write:discussion)
Go          1.x, GOFLAGS=-mod=mod exported in every shell that builds
```

**`goal/upstream` and `main` are the same commit.** I committed to
`goal/upstream` and fast-forwarded `main` from the primary checkout with
`git merge --ff-only goal/upstream` — this is Option B from the goal document,
chosen because the ledger tooling lives on `main` and Phase 1 merges into it.
**Nothing is unpushed on `goal/upstream` relative to `main`**; the branch is the
mechanism, not a queue of unmerged work.

## The first action

```bash
cd ~/code-local/go/stash
export GOFLAGS=-mod=mod
python3 docs/pr_triage.py report        # the 70-PR queue, bucketed
```

Then pick the next PR off **Group C** in `docs/PR-DECISIONS-batch1.md` and merge
it: **#7137** is next (2 files, `pkg/scraper/url.go` + a test). One commit, one
verification, then `python3 docs/check-issue-ledgers.py`.

Do **not** start Phase 2 until the Group C merges are done or consciously
deferred — the goal document's ordering is deliberate, and Phase 2 is 435
issues, which is weeks.

## Session 3 — #7180 merged, and the one issue it closed

Worked the next merge candidate. `68192aa59`, closing **`stash#7179`**.

The bug: a clean removed an *empty* gallery but never one whose folder carries
`.nogallery`, because the filter was `ImageCount = 0` and nothing else. Scan has
always honoured the marker (`pkg/image/scan.go:415`), so scan and clean
disagreed about the same directory.

**Merged as written** — no departure from upstream's patch needed. Its refactor
splits the decider (`findGalleriesToClean`) from the deleter (`cleanGalleries`),
and that split is what exposed the real finding.

**The finding: upstream's test never calls the deleter.** So `if !j.input.DryRun`
was untested, and `docs/mutate_7180.py` scored it **SURVIVED** — deleting the
guard left every upstream test green.

A survivor normally means the line is dead or redundant and the check should go.
**Not here.** A dry run exists so a user can see what a clean would remove, and
its entire value is that it removes nothing; a regression is data loss on the
strength of a preview click. So the outcome was to **add a test and keep the
row**: `TestACleanDryRunDeletesNothing`, driving the deleter in both directions,
with the non-dry half as the control that proves the path is reachable at all.

**Generalisable, and a sibling of the "wrong layer" rule: a test that exercises
only the decider cannot see a guard in the deleter.** Two functions split for
testability — and the split is also a seam the test must cross deliberately.

Two fixture traps, both recorded in the files, both presenting as something else:

- **A mock expectation with no count constraint is permanent.** The batch loop's
  terminating empty page matched the populated one forever; the test hung to its
  own `-timeout` with the stack in `mock.MethodCalled`. Reads as a slow test.
- **`&plugin.Cache{}` panics *after* `Destroy` has already run** —
  `enabledPlugins` calls a method on a nil interface.

### Gates at `c54dd1926`

```bash
$ go test ./... -count=1   # twice
exit1=0 FAIL1=0 ok1=35
exit2=0 FAIL2=0 ok2=35
$ python3 docs/mutate_7180.py     # 6/6 killed, 0 survived, 0 skipped
$ python3 docs/mutate_7135.py     # 3/3 killed, 0 survived
$ python3 docs/check-issue-ledgers.py
  closed 15 · deferred 93 · not-planned 132 · planned 435   (675 total)
OK: header, table and log agree
```

## What this session actually did

**Phase 1 is complete against its exit condition: every open upstream PR has a
recorded decision.**

| | |
|---|---|
| Open PRs, all dispositioned | **70 / 70** |
| Merged and committed | **3** (`#7241`, `#7255`, `#7180` → `stash#7179`) |
| Declined on a named non-negotiable | **6** |
| Deferred with the reason recorded | **62** |
| Commits on `main` | 7, from `02d0d0476` to `c54dd1926` |

The two merges are not "applied upstream's patch". Each is **the idea, not the
patch**, and both are recorded in `docs/PR-TRIAGE.md` with the measurement that
justified departing from upstream:

- **`6b8448b0d` — `#7241`, plugin asset path containment.** Upstream fixed a
  zip-traversal class with a trailing-separator `HasPrefix`. We had already
  fixed two of its three call sites properly with `fsutil.SafeJoin`
  (`filepath.Rel` containment), so merging it would have **regressed** our two
  correct gates and fixed only the third. Took the third site, used the gate we
  already had. Measured: `../myplugin-evil` against `<plugins>/myplugin` — old
  guard `true`, actually inside `false`.
- **`60ba6c051` — `#7255` / `stash#7135`, password hashing.** Upstream's report
  frames it as an availability bug ("no user will show"). It is a **security**
  bug: `hash, _ :=` discarded bcrypt's error, a >72-byte password produced an
  empty hash, `HasCredentials()` went false, and `ValidateCredentials()` then
  took its "nothing to authenticate" branch and returned **true for any input**.
  Verified live on the tree before the fix:
  `ValidateCredentials("attacker", "totally wrong") == true`.

## Gates run at session 2, with verbatim output (superseded by the session-3 block above)

```bash
$ go build ./internal/... ./pkg/...                      # exit 0
$ go test ./... -count=1   # run 1
FAIL1=0 ok1=35
$ go test ./... -count=1   # run 2
FAIL2=0 ok2=0 → exit2=0, FAIL2=0 ok2=35
$ python3 docs/check-issue-ledgers.py
  roster: 675 issues
    closed       14
    deferred     93
    not-planned  132
    planned      436
  closed-issues.md: 14 rows
OK: header, table and log agree          (exit 0)
$ python3 docs/mutate_7135.py
KILLED  hashPassword swallows bcrypt's error again
KILLED  ValidateCredentials fails OPEN again (the original hole)
KILLED  SetPassword stores the hash even when hashing failed
3/3 killed, 0 survived, 0 did not run      (exit 0)
```

Run twice because these tests share a database and the goal document requires
it. Both runs identical: **35 packages ok, 0 FAIL.** Baseline was also 35 ok /
0 FAIL, so **the count did not go down** (non-negotiable #1).

## What I deliberately did NOT do, and why

- **Did not rebase or merge `stashforge`.** The convention is *inverted*: at
  session start `main` was 29 ahead and 130 behind, and **neither branch is an
  ancestor of the other** — `git merge-base --is-ancestor` returns false in both
  directions. The goal document says this reconciliation is its own milestone
  with its own tag, and that rebasing `stashforge` is forbidden. Untouched.
- **Did not push anything.** No milestone completed, and the goal document says
  push at milestones, never mid-milestone. `main` is 4 commits ahead of
  `origin/main` (which was already 2 ahead before this session).
- **Did not merge 62 deferrals "to look productive."** A mergeable one-file CSS
  PR (`#7235`, `#7245`, `#7249`) is still not a merge: `ui/v2.5/build/` is a
  gitignored embed artefact, so without a real `vite build` "merged" would be a
  claim about a build nobody ran, and every UI file is about to be churned by
  the reconciliation anyway.
- **Did not touch `stash-box`, `~/code-local/worktrees/stashforge`, or
  `~/code/go/stash`.** Owned by other profiles / a stale pre-tag copy.
- **Did not start Phase 2.** 435 planned issues, one commit and one named test
  each. Starting it without finishing Phase 1 would produce a half-run.

## The one finding that is not bookkeeping

**`#7225` adds `pkg/sqlite/migrations/87_phash_short_videos.up.sql`, and 87 is
already taken on `stashforge`.**

```
main:       highest migration 86    appSchemaVersion 86
stashforge: highest migration 106
```

Safe on `main` today; a guaranteed collision at the reconciliation. And the
failure mode is the one this project has been bitten by repeatedly:
`golang-migrate` applies **by number**, so a renumbered migration is one that
never runs on an instance that already applied the fork's 87 — silently, with
no error. **Decision recorded: merge the behaviour, renumber the migration.** It
is written down in `docs/PR-DECISIONS-batch1.md` rather than done silently,
because whoever does the reconciliation must see it.

**This is a standing hazard, not a one-off.** Any upstream PR adding a migration
below 106 collides. The check to run before merging one:
`ls ~/code-local/worktrees/stashforge/pkg/sqlite/migrations/ | tail -1`.

## Traps paid for this session, as rules

1. **A decision recorded in a table is not a decision implemented in code.** The
   worktree I inherited had `#7255` written up in full — the comment, the
   signature, the reasoning — and `SetPassword` still had the OLD body
   (`hash, _ :=`) behind a new `error` return. The mutation harness reported it
   as `SKIP / anchor not found`, which reads like a harness artefact and was
   actually the single most important finding of the session. **A `SKIP` whose
   anchor is "the fixed code" is a report that the fix was never written.**
2. **A named guard is a claim, and the mis-pointed one is invisible.** The
   harness's third row survived, guarded by
   `TestAPasswordOverBcryptsLimitIsRefusedRatherThanStoredEmpty` — a test that
   calls `hashPassword` **directly** and therefore cannot see what `SetPassword`
   does with the result. My *own* new propagation line had zero coverage and the
   suite was green. The diagnostic is the same two minutes every time: apply the
   mutation, run `-v`, read which test FAILed, and point the row at **that** one.
3. **A mutation that breaks the build is not a kill.** The row deleting the
   fail-open branch left `logger` unused, the package stopped compiling, and a
   naive harness scores that green. Fixed by flipping the branch's **return
   value** instead of deleting the branch — compiles, and `return true` is
   exactly the pre-fix behaviour.
4. **Never run the full suite concurrently with a mutation harness.** I did,
   and got a `FAIL` from `internal/manager/config` that was entirely
   self-inflicted — the harness was editing `config.go` underneath the run. A
   red suite with a harness in flight is not a finding. Re-ran clean: 35/0.
5. **Hand-typed tables lose rows.** The first draft of the decision file claimed
   6+46+20 and had **eight PRs missing from every table** (#6224 #6440 #6519
   #6685 #6896 #6951 #7038 #7172), because the counts were typed rather than
   derived. It also double-counted, because Groups B and C overlap by
   construction. The buckets are now computed from `docs/pr-queue.json` and
   **asserted disjoint, summing to 70** — the assertion is in the file's own
   commit message so the next person knows it was checked.
6. **All six declines are `CONFLICTING` PRs.** Nothing mergeable was declined —
   every PR git could apply cleanly is merged or deferred as sound. So a
   conflict count in a queue is **not** a measure of PR quality, and the 6
   rule-declines would have been declined regardless of the conflict.

## State of the other branches (re-derived, not trusted from the goal doc)

```
main            ed14c3de2  [origin/main: ahead 4]     clean
goal/upstream   ed14c3de2  == main                     clean
stashforge      68154f24b  [origin/stashforge: ahead 2]  owned by coding-3
m6-upstream-issues a5a86a2c9  pushed, do not re-push
```

The goal document's warning is confirmed: **the two lineages have swapped
roles.** Neither is an ancestor of the other. `docs/UPSTREAM-ISSUES.md` and
`docs/closed-issues.md` exist on `main` **only**; `docs/requirements.csv`,
`docs/GOAL.md` and `docs/ALIGNMENT.md` on `stashforge` **only**.

## Phase 2, ready to start

435 planned issues, none started. The ledger is in agreement (15 closed, 93
deferred, 132 not-planned, 435 planned, 675 total). Work them in the
dependency order `docs/UPSTREAM-ISSUES.md` states — **the scanner and job-queue
capabilities unblock the most downstream fixes** — not by issue number.

**`stash#7179` and `stash#5850` are now closed** (it is in `closed-issues.md` with named
tests, commit `6d392659b`), so the collision the goal document warned about is
resolved. Check `docs/closed-issues.md` before picking anything up: the roster
is the filter, the closed log is the truth.
