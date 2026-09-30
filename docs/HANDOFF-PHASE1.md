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

## Where it stands now

```
main            5d699a4b4  [origin/main: ahead 27]     clean
merged          12 PRs -> 9 issues closed + 1 amended (#7241, #7255, #7180/#7179, #7137/#7136,
                #7265/no issue, #7225/#3722, #7257/#5944, #7196/#7197, #7166/#7165, #7159/#684-amended, #7199/#7198, #7261/no-issue)
issue ledgers   22 closed · 93 deferred · 129 not-planned · 431 planned · 675 total
next PR         #7181
```

## The first action

```bash
cd ~/code-local/go/stash
export GOFLAGS=-mod=mod
python3 docs/pr_triage.py report        # the 70-PR queue, bucketed
```

Then pick the next PR off **Group C** in `docs/PR-DECISIONS-batch1.md` and merge
it: **#7181** is next. One commit, one
verification, then `python3 docs/check-issue-ledgers.py`.

Do **not** start Phase 2 until the Group C merges are done or consciously
deferred — the goal document's ordering is deliberate, and Phase 2 is 432
issues, which is weeks.

## Session 12 — #7261 merged: three Makefile lines, and the check that mattered

`5d699a4b4`, no linked issue. Upstream folds the host os/arch into the build stamp and
splits the env-prefix out of the `$(shell)`. The Windows mechanism — `GOOS=x GOARCH=y
cmd` is a POSIX env prefix that cmd.exe lacks — is **unverifiable on this Linux host**,
so it is merged on the mechanism being coherent. The verifiable part was verified.

**The hazard: upstream assigns `GOOS`/`GOARCH`, the variables the cross-builds use.**
Every `build-cc-*` target exports its pair target-locally and wins, so today it is
harmless — confirmed by running the real shape. But that is luck: a plain global
`GOOS :=` would be silently overwritten by a build stamp and the build would target the
host **while reporting success**, demonstrated. Renamed to `BUILD_HOST_OS`/
`BUILD_HOST_ARCH` so the hazard cannot exist rather than merely not exist today.

**Verified end to end, because nothing in the Go suite would catch an empty stamp:**
`make` yields `BUILD_DATE=[linux amd64 2026-09-30 20:32:26]` against the old form's
`2026-09-30 20:32:26`, and a binary linked **through the real Makefile** carries
`linux amd64 2026-09-30 20:35:52` per `strings`. The multi-word value links fine
because the whole `-X` is single-quoted — both forms checked against the linker.
`VITE_APP_DATE` consumers were read too: both are display strings, neither parses.

**Trap:** `build-info`'s recipe is *empty*, so the evals run only when the target is
invoked. A probe that does not depend on it sees an **empty** `BUILD_DATE` and looks
like the stamp is broken. It is not.

## Session 11 — #7199 merged, `stash#7198` closed, and two fixes with no witness

`c2bfd44ce`. `Requires` was parsed into the index and never acted on, so a plugin
naming a dependency produced an install that could not run. Install now walks the tree,
installing what is missing and updating what is outdated.

`stash#7198` was correctly ticketed **planned** — unlike #7197/#7165/#7071, this one
needed no correction. Worth saying, since three of the last four were wrong.

**One fix with a witness: a crash.** `packageByID` returns `(nil, nil)` for an ID not
in the index and `install` dereferenced it at `remote.GetPackageZip(ctx, *pkg)`, so a
requirement the source does not publish **crashed the process**. Pre-existing, but
recursive resolution is exactly what makes a bad requirement name an ordinary input.

**Two fixes with NO witness — and that is the part worth keeping.**

Upstream never deletes from `installing`, so it is a "seen in this call tree" set, not
a cycle guard. Added `defer delete` — then found **reverting it leaves the suite
green**. Two reasons: `Install` builds a *fresh map per call*, so a cross-call leak is
invisible by construction; and within one call tree the two guards produce different
visit sequences but the **same outcome**, because `install()` is idempotent for an
already-installed package. Ran both shapes side by side rather than assuming.

The cycle guard is likewise not load-bearing: deleting it entirely still passes, since
`installRequirements` recurses only into a requirement that is missing or outdated, so
on the second visit to an ID in a cycle the package is already current and `continue`
fires. Traced it — `err=nil`, both packages installed.

Both kept as insurance, since a future change to `installRequirements` would turn a
hang into a stack overflow. But **a test that cannot fail is worse than no test**, so
both were relabelled in the test file to say they have no witness, and one entirely
vacuous test was deleted rather than kept to pad the count. A test named for a fix
implies the fix is verified, and the next person to revert that line will trust the test
instead of re-deriving the reasoning.

13 tests drive the real install path against a `file://` repository — real zips, real
sha256, real manifests, no mocking. A mock cannot tell you a package was skipped on a
second visit because a map still held it, which is the defect class here. 3 mutations,
all killed: the nil guard removed (panic), the requirement walk removed (9 tests), and
`Requires` dropped from the manifest (1 test).

## Session 10 — #7159 merged: a second mechanism for `stash#684`, and a container that died

`352c7d105`. Not new work — we already fixed #684 with `PUID`/`PGID` + `su-exec`,
verified against a real image. Upstream's mechanism (`user: "N:M"` in compose) now
coexists with ours.

**The Go half is one real bug, and the ordering is the fix.** `GetHomeDirectory`
called `user.Current()`, which **panicked** on error; verified from the Go source
that `os/user` returns `user: unknown userid N` for an unresolvable uid — which a
numeric uid with no passwd entry is. It now reads `$HOME` via `os.UserHomeDir()`
first, and *that* is what matters: `os.UserHomeDir()` consults the environment and
never the passwd db.

**The collision only a container could reveal.** With `user:` Docker starts the
process already non-root, and our entrypoint then tried to drop again:

```
su-exec: setgroups(1000): Operation not permitted     exit 1
```

Reproduced against a real image, not inferred. A non-root process cannot setresuid,
**not even to itself**, because clear-groups needs privilege. The `chown`s are
`|| true` so they were harmless; this line is not, and `set -e` kills the container
with no useful message.

**Two fixes, neither sufficient alone** — and that shape is the evidence:

| variant | result |
| --- | --- |
| both reverted | 5 of 6 new tests fail |
| only fix 1 | 5 of 6 fail |
| only fix 2 | 3 of 6 fail |
| both | **16/16**, all 10 pre-existing still green |

Fix 1: with `--user 1500:1600` the script ignored the uid Docker started it as,
resolved the image's `stash` account (1000), and attempted an impossible 1500→1000
drop. A non-root start with no requested identity now treats the current uid as the
request. Fix 2: exec straight through when the current uid:gid already equals the
target.

One of the six passes in every variant (`user:` vs a leftover `RUN_AS_ROOT=1`) —
correct, since `RUN_AS_ROOT` exits before `su-exec`, so it proves nothing. Kept to
pin precedence and marked as such rather than counted as proof.

The harness gains 6 container tests via a `run_as` helper using `docker run
--user`, including a **split identity** (1000:1600), which is precisely how a string
comparison goes wrong.

Two environment gotchas worth remembering: `COPY` does **not** preserve the
executable bit, so a scratch build context needs `chmod +x` (or `--no-cache`) or the
layer silently reuses a non-executable copy and *every* test fails at 0/16 — which
looks exactly like a code failure. And my stub image needed `CMD ["stash"]`, or
`exec "$@"` runs with no command and prints a `su-exec` usage error.

## Session 9 — #7166 merged, `stash#7165` closed, a CSP hole, and a sweep that found a third

One commit, `c71899e7f`. A plugin with a user-configurable backend endpoint had no
way to allow it — `connect-src` is assembled per plugin, so a host the admin chose
was silently blocked. With `csp-settings: true`, any `csp_`-prefixed setting holding
a valid `http`/`https` URL joins that plugin's `connect-src`.

Applied with `--3way`: #7196 had already turned `media-src` into a slice at the
same lines, so the patch context had moved. `server.go` merged cleanly; both
`Plugins.md` conflicts were **additive** (each side documenting its own field in the
same `ui:` block) and both were kept.

**A real hole.** Upstream's wildcard check tested `u.Host` only, so
`https://cdn.example.com/*` **passed** — and a path wildcard is a legal CSP source
expression matching every request under that host. The asterisk one character right
of the host bought the whole subtree. The lesson is the property: **a CSP source
expression is a prefix match, so "the host is exact" was never the property that
mattered — "the value is exact" is.** Upstream's own docs already said "a valid,
**concrete** URL", so the fix restores intent rather than narrowing it.

Also: a bare `csp_` key was read as a source (an opt-in that reads as *off* to
anyone inspecting the key), and `http://.` / `https://..` passed the host checks.

Two tests assert properties rather than examples — the separator character *set*
(with a control value so the test cannot measure nothing), and byte-identical
output across 200 runs to catch map-iteration order leaking into the header. 3
mutations, **each killed by exactly the test written for it**, which is the check
that they aren't passing for a shared reason.

**The sweep.** `stash#7165` was the second wrong *not-planned* verdict, so instead
of fixing them one at a time I checked all **131** R10 not-planned issues against
every open PR in the queue, batched — 6.5 seconds. **One more hit:** upstream PR
**#7048** closes `stash#7071`, also ticketed *not-planned*. Third instance, found by
the sweep rather than by luck. Recorded as closed but **deliberately not merged** —
VR metadata is a schema change with no local verification path.

**Ledger mechanics learned the hard way:** the header tallies must be derived from
the table with a `Counter`, not by arithmetic — hand-subtracting twice produced
674/675 totals and a 20-vs-21 mismatch that only the checker caught. And the verdict
column must be exactly `closed`; `closed (merged)` is not recognised.

## Session 8 — #7196 merged, `stash#7197` closed (which was ticketed *not-planned*)

One commit, `b14aef421`, six lines upstream across three files. A plugin loading
media from an external origin had no way to allow it — `media-src` was a fixed
`blob: 'self'` — so the media failed **with nothing in any log**. The most
expensive kind of bug: no error to grep for.

**Changed `media-src` to a slice** like its three siblings. Upstream used a bare
string `+=`-ed in the plugin loop; it works (a stray space is harmless to a
browser) but differs in shape and accumulates an empty segment per plugin that
configures none. Printing the header both ways: identical apart from a trailing
space.

**`setPageSecurityHeaders` had zero tests** — exactly what a one-line change to a
header builder needs one for. 8 tests now, mutation-checked: the PR's line removed
kills 5, the `Enabled` gate removed kills the disabled-plugin test, first-entry-only
kills the all-entries test, the default dropped kills 4.

**A mis-ticketed issue worth noticing.** `stash#7197` was marked *not-planned* on
R10 — yet #7196 exists and closes it in six lines. The rule was applied to the
issue's phrasing, not to whether the work was about to land. **Check whether an
upstream PR already closes an issue before honouring a not-planned verdict.**

Ledger hygiene while here: #7265 was still a pending Group C row after merging,
and a renumbering had duplicated #7159. The table now asserts no merged PR appears
as pending, and carries no duplicates.

## Session 7 — #7257 merged, `stash#5944` closed, and a clone that was half a clone

One commit, `6d5a8131c`. A JS plugin could not read its own configured settings —
only `args` and `server_connection` — so `input.Settings` now carries them.
Merged as written, upstream's own tests included.

**Changed: deep clone instead of `maps.Clone`.** `maps.Clone` copies the top level
only, settings come from Viper's `Raw()`, and goja hands a Go map to JS as a
reference — so a plugin writing to `input.Settings.tags[0]` writes into the live
config, which the next `SetPluginConfiguration` persists. Measured: the stored
settings came back mutated.

**The recognisable shape:** a test named after a property, asserting it on the one
input where it trivially holds. Upstream's isolation test mutates `enabled` — a
*scalar*, the one value a shallow clone detaches. Not a bad test; a test that
cannot fail, and its presence is evidence the harder case was never considered.

**Measuring the boundary library changed the assertions.** My first list test
asserted `push` is contained. True — and it passes against the broken clone too.
goja's array wrapper is not a live view: `push`/`splice`/`pop` hit the VM's own
array, only element assignment writes through. "The slice is copied" and "the
elements are copied" are separate guarantees and only the second is violable.

9 new tests, **5 fail against `maps.Clone`**. The clone is also load-bearing for
the concurrency claim — a shared nested map handed to a VM beside the settings
writer is a data race, not a leak.

Gates: suite 3/3 clean, **38 ok** (up from 36), 0 FAIL; `-race` on `pkg/plugin`
×5 clean.

## Session 6 — #7225 merged, `stash#3722` closed, and a correction

Two commits. `f36bce276` is a correction to the previous session's claims;
`63635bcc0` is the merge.

**The "~0.3% still truncates" caveat in `037c9d6d1` was wrong.** Classifying by
**status code** rather than body length: **3840 concurrent serves, zero truncated
200s.** Every "short body" was a 500 carrying `fork/exec ...: text file busy` —
**ETXTBSY**, the kernel refusing to exec a file whose descriptor is still open for
writing, because the hammer wrote stubs into a shared `t.TempDir()` that sibling
goroutines were already exec'ing. My test's artifact, not a bug. The fix is
complete.

Two mistakes, both now structural rather than remembered: a body-length check
cannot tell a truncated stream from a failed start, and **I named
`LockContext.Cancel` as the cause before measuring it**. It does contain a second
`Wait()`, but it was never what the hammer showed. The guard
(`pkg/ffmpeg/stream_transcode_race_test.go`) asserts the classification itself,
since a guard that cannot tell those two apart is what caused the misreading.

**#7225** makes the phash sprite NxN by duration (2 to 45s, 3 to 90s, 4 to 150s,
5 beyond) — a 30-second clip no longer gets 25 frames 1.08s apart, which is why
unrelated short videos hashed alike. Closes `stash#3722`.

The merge was easy; **the threshold was the work**. `150` lived in the Go switch
*and* in migration 87's SQL with nothing connecting them, and both failure modes
are silent: too narrow leaves incomparable hashes everywhere while the note says
it was fixed; too wide re-hashes everything for nothing. Now an exported
`MaxChangedDuration`, with a test that reads the migration file and compares its
literals to the algorithm, plus three for what literals cannot see, plus one that
**runs** the migration against a real SQLite.

Two fixture bugs, one lesson: a probe with an invented schema made the migration
look destructive (a failed subquery inside `IN()` does not abort the `DELETE`), and
a duplicated primary key silently dropped rows — caught only by asserting the
seeded count, which was itself asserted *after* the migration and so counting
survivors. **Assert a precondition where the precondition holds.**

Gates: suite 3/3 clean, **36 ok** (up from 35), 0 FAIL; 10 mutations killed; tsc
clean; 107/107 frontend.

## Session 5 — #7265 merged (no issue), and a race nobody was looking for

Two commits: `037c9d6d1` (the race) and `5ca4c5806` (the PR). The first is the
one that matters.

**#7265 closed no issue** — it has no linked one. The first merge this session
where that is true, so it is said plainly rather than implied as progress.

**The frontend had no test and that is where the work is.** `normalizeDateString`
is 66 lines in `src/utils/yup.ts`; the UI has no runner at all (no `test`
script, no jest, no vitest). The backend half arrived tested, the half that
decides what the user sees arrived untested. Added
`scripts/test-date-normalisation.mjs` on the `check-country-names.mjs` precedent:
**107 checks**, including a cross-language one that feeds every accepted string
to a Go probe — a front end that normalises to something the API rejects has
*moved* the error, not fixed it.

It cannot import `yup.ts` at all: line 1 imports `FormikErrors`, a type formik
exports only from its `.d.ts`. `tsc` and `vite build` are clean, so this is node
being stricter than the project — **not a defect in the file**, and the tempting
"fix" would address a non-problem. The function is lifted from source text and
the copy is checked against the original every run.

**The race.** The full suite failed 2 in 18 runs on two `pkg/ffmpeg` tests —
*"want 12 bytes, got 1"*. Not flaky: `getTranscodeStream`'s stderr goroutine
called `cmd.Wait()`, which closes the child's pipes, and the handler reads
stdout as one peeked byte then `io.Copy`. `Wait` landing between them truncated
every stream to one byte — for MP4, a cut `ftyp` box that downloads fine and will
not play. Replayed 3000 times: **426 truncated (14%)** with `Wait()` there,
**0 of 3000** without.

The instructive failure was my own first fix: a `defer` in `getTranscodeStream`
is scoped to a function that **returns the handler**, so it fired at
`return handler, nil` and closed stdout before a byte was read — a hard
`500, body empty` on every run, strictly worse than the race. A defer belongs to
the function it is written in, not to the work that function sets up.

**Not fully fixed, and the commit says so:** ~0.3% of serves still truncate under
a 12-worker hammer, where the original failed 5/5 runs. The remaining reaper is
`LockContext.Cancel` at `pkg/fsutil/lock_manager.go:33` — a second `Wait()` on
the cancel path, out of scope here. Bisected: 0/1000 at 2 workers, ~0.3% at 12,
0/1000 with per-serve subtest cleanup.

Gates: suite **6/6 clean**, 35 ok, 0 FAIL (baseline 2-in-18); `-race` on
`pkg/ffmpeg` ×20 clean; `tsc` clean; 107/107 frontend.

## Session 4 — #7137 merged, `stash#7136` closed

Second merge this session. `3f6678e73`. `urlFromCDP` always finished with
`chromedp.OuterHTML`, so a scraper targeting a JSON *document* received Chrome's
HTML wrapper around it — valid HTML, invalid JSON, and a parse failure that looks
like a broken scraper rather than a broken extractor.

**Merged as written, except the decision was extracted.** Upstream's three parts
are the mime sniff, the tracker, and the branch that reads the body. The first
two arrived well tested. The third was inline in a `chromedp.ActionFunc` and
reachable only by running a browser — so **the repair had no test at all**, in a
file that looks thorough. The harness row reported `SKIP / anchor not found`,
which the standing rule reads as "the code moved"; what it meant was that the
branch could not be guarded. The decision now lives in `readMainDocument` with
the two Chrome accessors as parameters, and four tests drive it.

**That is the second `SKIP` this session that was a finding rather than a stale
anchor**, and the two are hard to tell apart. The discriminator: a stale anchor
is post-fix source that has *moved*; an unobservable branch is post-fix source
that was **never reachable from a test**. Read the source — two minutes —
because guessing wrong means either deleting a real fix or leaving an untested
one.

Harness: **7/7 killed**, including the data race, scored as a kill even though
the test's own assertions pass on a torn read.

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

### Gates at `c54dd1926` (session 3 — a record, not current state; the figures below are what was true then)

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
| Merged and committed | **12** (`#7241`, `#7255`, `#7180` → `stash#7179`, `#7137` → `stash#7136`, `#7265` → no issue, `#7225` → `stash#3722`, `#7257` → `stash#5944`, `#7196` → `stash#7197`, `#7166` → `stash#7165`, `#7159` → `stash#684` re-fixed, `#7199` → `stash#7198`, `#7261` → no issue) |
| Declined on a named non-negotiable | **6** |
| Deferred with the reason recorded | **62** |
| Commits on `main` | 27, from `02d0d0476` to `5d699a4b4` |

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

## Gates run at session 2, with verbatim output (a record; superseded by the session-3 and session-4 blocks above)

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
- **Did not start Phase 2.** 432 planned issues, one commit and one named test
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

432 planned issues, none started. The ledger is in agreement (17 closed as of
that gate; 18 after session 7, 93
deferred, 129 not-planned, 431 planned, 675 total). Work them in the
dependency order `docs/UPSTREAM-ISSUES.md` states — **the scanner and job-queue
capabilities unblock the most downstream fixes** — not by issue number.

**`stash#3722`, `stash#5944`, `stash#7136`, `stash#7179`, `stash#7197`, `stash#7165`, `stash#7071`, `stash#7198` and `stash#5850` are now closed** (it is in `closed-issues.md` with named
tests, commit `6d392659b`), so the collision the goal document warned about is
resolved. Check `docs/closed-issues.md` before picking anything up: the roster
is the filter, the closed log is the truth.
