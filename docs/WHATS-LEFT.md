# stash — what is left (measured 2026-10-04)

**The predicate is the authority.** `python3 docs/goal-check.py` is the repo's own
completion test; this file is a reading of it, not a substitute. Re-run it before
believing anything here — the numbers move, and a number copied into a summary is
not evidence.

Repo `~/work/lane-2/stash`, branch `main` (the soft fork of `8ullyMaguire/stash`).
Remote `origin` (GitHub). HEAD `8323b44d4`.

**Host note:** this checkout lives on **thinkcentre** (M720q), at
`~/work/lane-2/stash`. **The previous version of this file said the repo lived on
gaming-pc and that `ssh thinkcentre` did not have it — both were wrong**, and had been
since at least 2026-10-02. A second checkout of the same fork sits at
`~/work/lane-1/stash`; do not confuse the two, and do not build in either.

## Status: nothing outstanding

**Two gate defects were found and fixed while confirming this, and both are the same shape -- a
check that reports a verdict without the evidence needed to act on it.** Recorded here because
"nothing outstanding" is exactly the claim that stops someone looking:

- **C7 ran ONE of THREE integration packages.** `goal-check.py` hardcoded
  `go test -tags integration ./pkg/sqlite/` while `pkg/sqlite`, `internal/autotag` and
  `internal/manager` all carry `//go:build integration` tests. It reported "1 package green" while
  two packages went unexecuted. They were green by hand, but a clause that passes because it did
  not look is not a check. Now discovered by build tag, and an empty discovery result is a FAIL
  rather than a vacuous pass.
- **`verify-all.sh`'s `tail -15` buried the diagnosis.** The integration gate reported
  `FAIL internal/api` naming no failing test, and the fifteen visible lines were wizard spam reading
  `error="database is locked"` -- which is a fake store's deliberate error string
  (`errStoreUnreadable`), not a real lock. Both suites now extract failures FIRST and `tee` the full
  log.
- **This file's own HEAD hash and gate row counts had drifted** (four commits behind; 46 log rows
  against a ledger holding 48) and no gate could see it, because neither is a clause. `check-whats-left.py`
  now checks both, and it fired on its own commit the moment HEAD moved -- which is the point.

**One open observation, deliberately not claimed as fixed:** that `internal/api` failure did not
reproduce in 9 subsequent attempts (3 alone, 5 full-suite, 1 under a concurrent build), no stale
server held any port, and no build failure was logged. Cause unestablished. The harness now shows the
diagnosis if it recurs.


Measured 2026-10-04 at `8323b44d4` by `docs/goal-check.py`,
`docs/check-issue-ledgers.py`, `docs/ledger-check.py`, `docs/closed-log-check.py` and
`docs/verify-all.sh`.

| Clause | Verdict | What it says |
|---|---|---|
| C1 PRs decided | PASS | all 65 open PRs have a recorded decision |
| C2 issues dispositioned | PASS | no rows left `planned` |
| C3 M5 tagged | PASS | `m5-p2p-downloader`, reachable from main |
| C4 M7/M8 done | PASS | `m7-mesh`, `m8-relay-mesh` |
| C5 requirements.csv | PASS | 90 rows: 80 `tested`, 6 `shipped`, 4 `deferred` |
| C6 branch convention | PASS | single-branch layout, nothing stranded |
| C7 suites | PASS | unit 61 packages green; **integration 3 packages green** (./internal/autotag, ./internal/manager, ./pkg/sqlite) — discovered by build tag, not hardcoded |
| C8 backlog-17 ledger | PASS | 17 issues = 16 done, 0 open, 1 skipped (#2149). **#2359 moved from skipped to done** in `8808699bb`: its old "satisfied by configuration" disposition was right about the meta and wrong about the work — configuring a StashDB instance is push from a StashDB this build scrapes, and parity means Stash can *represent* what Stash-Box represents, which configuration cannot do |

Supporting gates, all passing:

| Gate | Result |
|---|---|
| `docs/ledger-check.py` | PASS — 675 roster rows, all well formed |
| `docs/check-issue-ledgers.py` | OK — header, table and log agree |
| `docs/closed-log-check.py` | PASS — 48 log rows, 5 columns, 48 distinct issues (#422 and #2359 added) |
| `docs/check_cited_paths.py` | PATHS OK — 49 distinct cited paths, 0 missing |
| `docs/e2e/playwright-e2e.js` | E2E PASSED — 71 assertions, 0 failed, 0 console/page errors |
| `docs/e2e/mutation-check.sh` | 3 killed, 0 survived, 0 harness errors |

**The previous version of this file reported C2 FAIL (69 planned rows) and C8 FAIL (1 of 17
remaining).** Both were true when measured on 2026-10-02 and both were resolved
afterwards — C2 in `621ceca54`, C8 by the five remaining rows landing. The lesson
below is about why the file went stale without anyone noticing.

## Test counts, measured today

Counted the way `docs/GOAL.md` states: `go test ... -v | grep -c '^--- PASS'`, top-level
only, never mixed with a subtest-inclusive count.

| | unit | integration |
|---|---|---|
| top-level PASS | 1801 | 806 |
| top-level FAIL | 0 | 0 |
| packages green | 61 | 1 |

## Why this file was wrong for two days

Nothing in the gate chain read it. `goal-check.py` computes the clauses from
`docs/ISSUES.md`, `docs/UPSTREAM-ISSUES.md` and `docs/requirements.csv`; `verify-all.sh`
runs the checkers. **No checker asserts that this file agrees with them**, so it drifted
freely while every gate stayed green — which is the same failure mode as the two ledger
defects fixed in `e9929beb7` and `12e4f1db1`, one layer up: a document that states a
conclusion no gate recomputes.

The fix in this pass is to stop this file from *stating* conclusions. It now carries the
measured numbers and the commands that produce them, and says plainly that the predicate
is the authority. It is not a gate and does not pretend to be one.

## Reusable lessons from #3849 and #3530

Kept because they are not specific to a commit. The full accounts are in
`docs/plan/BACKLOG-17.md`, `docs/ISSUE-3530-*.md` and the commit messages.

1. **"Intermittently" usually means insertion order, not chance.** SQLite's plan is
   stable, so a many-to-many whose far side is unconstrained picks the same row every
   time — which is why it was reproducible.
2. **The gallery's file set must be reached through `files.zip_file_id`.**
   `galleries_files` records a gallery's *archive*, so correlating against it directly
   matches nothing, NULLs every key and **reverses** the order — worse than the bug.
3. **A flat `A OR B OR C` is wrong where the alternatives are ranked.** The `OR` made the
   join non-unique and the symptom returned verbatim; the failure was a non-unique join,
   not a missing condition.
4. **A characterisation test goes red when the bug is fixed** — so it is half a test.
   Invert the assertion; then it is the specification.
5. **A positive control that fails is the signal.** Two fixtures passed for the wrong
   reason (no `files` rows, so `NotContains` was trivially true) and only the control
   exposed it.
6. **Assert the value the defect corrupts**, not an aggregate that happens to agree — one
   fixture's expected order was the same whether the bug was present or fixed, so every
   mutation survived.
7. **A mutation that "survives" may never have been applied.** An anchor string that also
   occurs in a doc comment means `replace(..., 1)` mutates the comment. An ambiguous
   anchor is a harness defect, not a finding. In the `test-driven-development` skill.
8. **`sqlite.Timestamp` is RFC3339 — second precision.** Every idempotence test written
   against a timestamp is vacuous until the stored value is moved out of the way first,
   and the test asserting that move must check its own premise.
9. **A range column added to a JOIN table is erased by every `replaceJoins` on that
   table**, silently, because the destroy half loses the columns the insert half does not
   carry. NULL is a legal value ("no window"), so the CHECKs still pass and a scene
   quietly reverts to the whole file with no error anywhere. Adding data columns to a join
   table means auditing every `replaceJoins` caller.
10. **Sweeping a constant proves the rule; only calling it proves the wiring.** Four of
    five aggregate call sites for #3530 were untested while the constant's own sweep read
    5/5 killed.

## Two ledger defects this pass fixed, since they are the reason the numbers above moved

Both were found by checkers that existed but were not wired into any gate.

- `e9929beb7` — `docs/check-issue-ledgers.py` was in no gate at all and failed with 14
  problems: six `closed` rows stranded in `## Planned` sections (4549, 5681, 6949, 7028,
  7145, 7187), the same six absent from `closed-issues.md`, and a summary claiming
  56 planned / 52 closed against a table holding 0 / 58. Additionally `ledger-check.py`
  identified a row with `len(cells) < 6`, so a row **truncated** into fewer cells read as
  "not a row" — row 2747 had been truncated mid-sentence since `a0c41eac9` and was
  invisible. Both checkers are now in `verify-all.sh`.
- `12e4f1db1` — ten malformed rows in `closed-issues.md`, which no checker inspected the
  shape of. Same class of bug one file over, now covered by `docs/closed-log-check.py`.

The general rule, three instances now: **a checker must identify a row by something the
defect cannot destroy.**

## Commits, most recent first

| Hash | What |
|---|---|
| `12e4f1db1` | repair 10 malformed rows in `closed-issues.md`; add `docs/closed-log-check.py` |
| `62b273d41` | e2e `mutation-check.sh`: refuse to start when the test port is already held |
| `e9929beb7` | ledger consistency; wire `check-issue-ledgers.py` + `check_cited_paths.py` into `verify-all.sh` |
| `621ceca54` | **C2 complete** — 0 planned rows, 662 dispositioned |
| `12e4f1db1`..`416895b7a` | the C2 sweep and the upstream-fork work between them |

## Still open, deliberately

- **#3530 detection.** Inferring ranges from repeated encodes is not implemented. This
  needs an upstream discussion rather than a guess. Everything else in #3530 is done,
  including the range UI (`SceneRangeForm.tsx`, `36a2af273`, tag `stash-3530-range-ui`)
  — so a window IS settable from the app, and only the *inference* is missing.
- **#2149 (phash validation)** and **#2359 (Stash-Box parity)** — skipped, with reasons, in
  `docs/plan/BACKLOG-17.md`.
- **`pkg/sqlite/scene_filter.go:142` filters `Duration` on `video_files.duration`**, the
  file's length, not the window. Defensible and recorded as a deliberate choice.
