# Upstream PR triage — decisions

Phase 1 of `~/secondbrain/10-Projects/stashforge/GOAL-stashforge.md`. **A PR is
not decided until it has a row here.** Leaving a PR undecided is not an outcome.

## The rules this phase runs under

1. **Merge into `main`, never straight onto `stashforge`.** Our fork has
   diverged — `main` is 27 commits ahead and carries real upstream fixes — so a
   PR that is clean against upstream can still break fork-only code.
2. **A PR may not remove a StashForge non-negotiable to be merged.** If one
   would, decline it and name the rule. The rules are in `docs/GOAL.md` on the
   `stashforge` branch, amended 2026-09-30: #11 **retracted** (P2P is now core),
   #3/#4/#5/#7 **extended**, #12–#16 **added**.
3. **Never force-push, never rewrite published history, never `git add -A`.**
4. **Every decision names why**, in one clause. A decision with no reason is not
   recorded, it is just a state change.

## What the snapshot is, and is not

`docs/pr-queue.json` is a derived artefact, gitignored, regenerated with
`python3 docs/pr_triage.py fetch`. It is a point-in-time view of a queue that
moves hourly, so it is deliberately not in the history.

**The classifier sorts; it does not decide.** `pr_triage.py` buckets by
mechanical signals — draft state, age, `mergeable`, CI conclusions,
`reviewDecision` — and every bucket is a *reading order*, not a verdict. A
tool that emitted a verdict would be a verdict nobody checked, which is the
failure mode this project keeps paying for.

## Three things measured while building the tool, because they cost time

**The GitHub GraphQL API 502s on a large `pr list` query, and it is a payload
threshold rather than a bad field.** Measured: every field works alone; the full
field set works at `--limit 30` and fails at 200/100/70; and the *same* query at
limit 70 passed once and failed the next time. A single-shot fetch would report
"the queue is unavailable" for a momentary blip, and a triage that gives up is a
triage nobody trusts the second time.

**`gh pr list` has no `--page` flag** (`unknown flag: --page`), and
`--paginate` does not help because the failure is per-request payload size.

**Paging by `--search created:>=<date> sort:created-asc` is a trap.** It looks
correct and advances the cursor by exactly **one** new PR per call, because the
first 25 rows of any such window are all older than the advancing boundary.
Instrumented, it walked 2023-08 → 2024-09 gaining one row per iteration — a loop
that cannot terminate on a queue of 70. **A paging loop that cannot terminate is
worse than no paging**, because it looks like progress.

So the tool fetches **one PR per call, by number**, over the numbers the cheap
unfiltered listing returns: 70 calls, 67 seconds, and a payload of one PR cannot
hit the threshold. `docs/pr_triage.py body <N>` fetches a PR body on demand —
the body is what decides, and the title frequently is not.

## A decision shape worth noticing

**#7241 is the first PR where "we already fixed this" and "upstream found
something we missed" are both true, and the two halves want opposite
treatment.** The import and package-install fixes are ours and better;
`SafeJoin` is containment where theirs is a string prefix, and on a
`filepath.Join`-ed path the traversal is already gone before the check runs, so
the upstream check is comparing a cleaned string. The plugin-asset fix is
theirs and new — we never had it, and it is a real escape.

Merging the PR wholesale would replace two correct gates with two weaker ones
and gain one. **The right answer is a three-line change, not a merge**, and
recording that is the decision.

## Phase 1 ledger

Appended below as decisions are made. `docs/check-issue-ledgers.py` does **not**
cover this file — it checks the issue roster against the closed-issue log — so
this table is checked by eye and by `pr_triage.py report`, and the count is
stated at the foot.

| PR | decision | why |
|---|---|---|
| #7241 | **Merge the idea, not the patch** — already fixed here, better. Take the third call site (`internal/api/routes_plugin.go:60`), fix it with `fsutil.SafeJoin`. | Upstream fixes zip traversal in import and package install with `strings.HasPrefix(dir, cleanBase)`. **We already fixed both of those** for stash#7240 with `fsutil.SafeJoin` (`pkg/fsutil/safepath.go`), which is containment by `filepath.Rel` and not a string comparison — and a prefix check on a joined path is the exact bug class, since `filepath.Join` has already cleaned the traversal away. What upstream found that we did **not** have is the third site: the plugin asset route, which had the same prefix check and is genuinely broken. Measured on this tree with `pluginDir = <plugins>/myplugin`: `../myplugin-evil` → guard TRUE, inside FALSE. So the PR is worth taking for one line of one file, and worth *not* taking for the other two. Merging it as-is would **regress** our two fixed sites. |
| #7255 | **Merge the idea, not the patch** — take it, and go one step further than upstream did. Verified present in our tree; fixed by returning the error instead of discarding it. | `hashPassword` was `hash, _ := bcrypt.GenerateFromPassword(...)`. Measured by running bcrypt, not reading its docs: **len=72 → 60 bytes, len=73 → 0 bytes + `password length exceeds 72 bytes`**. So a 73-byte password silently produced an empty hash. Upstream's issue (#7135) reports the symptom as *"set a 73-character password and reload, no user will show."* **That symptom understates it.** Because `HasCredentials()` is `username != "" && pwHash != ""` and `ValidateCredentials()` begins `if !HasCredentials() { return true }`, an empty hash does not merely hide the user — it makes the instance **authenticate any username and any password**. That is a security bug, not an availability one, and it is the reason this is worth doing carefully rather than by merging. Upstream's fix (reject >72 bytes up front) is right. **But fixing only that is
not enough, and this is the part worth having found.** Upstream closes the
*cause*; the *effect* — an already-corrupted config on disk — survives it, because
a username with an empty hash is indistinguishable from a never-configured
instance to every function involved. So the fix here is two halves:

1. `hashPassword` returns the error, `SetPassword` propagates it, and the three
   call sites (`config.go`, `manager.go:297`,
   `resolver_mutation_configure.go:356` — the same three upstream had to touch)
   handle it. Empty string still means "clear the credential" and is not an
   error; that distinction is what keeps clearing working.
2. `ValidateCredentials` **fails closed** once a username is configured. "No
   username at all" still returns true, or a fresh install could never be
   claimed; "a username whose hash will not verify" now returns false.

**Verified live, not inferred:** with the pre-fix empty hash stored,
`ValidateCredentials("attacker", "totally wrong")` returned **true** before
half 2. Mutation harness `docs/mutate_7135.py` re-introduces each pre-fix
behaviour and confirms a named test catches it — a passing test that cannot fail
proves nothing. The two controls that would have caught a bad fix: a fresh
instance is still claimable, and clearing a password still works. |
| _(pending)_ | | |

## Verification of the two decisions above — measured, not asserted

The state of this worktree when this session started was **two decisions
recorded and one of them not actually implemented**: `SetPassword` had the new
`error` signature but the *old* body, so it still discarded bcrypt's error and
always returned `nil`. The comment and the signature were committed to the
source and the behaviour was not. That is the exact shape of a decision that
reads as done — which is why this section is measurements.

```bash
export GOFLAGS=-mod=mod
go build ./internal/... ./pkg/...        # exit 0
go test ./internal/manager/config/... -count=1
#   ok  github.com/stashapp/stash/internal/manager/config  7.7s
python3 docs/mutate_7135.py              # 3/3 killed, 0 survived, exit 0
python3 docs/check-issue-ledgers.py      # OK: header, table and log agree
```

### Three defects the harness found, and none of them were in the tests' favour

The harness shipped with two rows that did not do what they claimed, and
re-introducing the real bugs found a third gap in the fix itself.

**1. A mutation that stopped the package building scored as "not run" (exit 2),
and the cause was a guard written on the wrong layer.** The row that
re-introduces the fail-open hole deleted the whole `if` block, which left
`logger` unused in the package and the import dropped — a build failure, which
this harness correctly refuses to call a kill. Fixed by flipping the branch's
**return value** (`return false` → `return true`) rather than deleting the
branch: that compiles, and `return true` is exactly the fail-open behaviour the
pre-fix code had.

**2. A `SKIP / anchor not found` was a real finding, reported as a harness
artefact.** The third row's anchor was the fixed `SetPassword` body, which
`SKIP` reported as "the code moved". It had not moved — it had **never been
written**. That is the defect: `SetPassword` had the new signature and a
comment describing behaviour it did not implement.

**3. A surviving mutation was a genuine hole in the fix, and the harness named a
guard that could not see it.** With the propagation written, the row scored
`SURVIVED` against
`TestAPasswordOverBcryptsLimitIsRefusedRatherThanStoredEmpty` — correct,
because that test calls `hashPassword` **directly** and is therefore blind to
what `SetPassword` does with the result. A new test,
`TestSetPasswordRefusesAnOverlongPasswordWithoutDestroyingTheStoredCredential`,
goes through `SetPassword` and kills it. Verified before repointing the row:
mutation applied → that test `FAIL`ed → tree restored. It also asserts the
*second* property, which is the one that is easy to miss — the pre-fix code
overwrote a **working** stored hash with an empty one, so a user who typed an
overlong password while a valid one was already set lost their login and, with
the fail-closed branch above, was locked out rather than merely
unauthenticated. Asserting only the returned error would miss that entirely.

**The generalisable part, and it is the fourth time in this project:** a named
guard is a claim that a specific test observes a specific line. Five of these
rows were once the *same* mis-pointing — a test that passed while a different
one caught the mutation — and the diagnostic is always the same two minutes:
apply the mutation, run `-v`, read the `FAIL` line, and point the row at *that*
test. A survivor means *look here*, and half the time the answer is the harness
rather than the code.