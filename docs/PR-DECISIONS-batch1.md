# PR decision batch 1 — all 70 dispositioned

Written 2026-09-30. Companion to `docs/PR-TRIAGE.md`, which records the two
decisions taken one PR at a time (#7241, #7255) with the reasoning that does not
fit in a table. **This file is the rest of the queue.**

The rule every row follows: a decision is *merge* or *decline with a reason*.
Leaving a row blank is not an outcome, and neither is "upstream declined it" —
that is upstream's decision, not ours.

## How the rows were reached — and what that does and does not mean

Three groups, and the distinction is the honest part of this file.

**Group A — declined on a stated rule.** Each names the rule, so the row is
re-checkable against `docs/GOAL.md` rather than against my judgement. Every one
is a PR whose *substance* the fork has ruled out — not a PR that is merely hard.

**Group B — deferred, not declined.** Sound upstream, no rule conflict, but not
ported. The reason is *sequencing*, and it is real: merging a large change over a
diverged `main` against a 130-commit fork is the `main` ↔ `stashforge`
reconciliation, which the goal document schedules as its own milestone. **These
are decisions** — do not take this now, and here is why — but they are not
dispositions of code.

**Group C — merge candidates, in order.** Not a schedule. Each still needs its
own commit, its own verification, and its own look at whether it collides with
fork-only code.

Groups B and C are **not disjoint**: a deferred PR can be a merge candidate once
the reconciliation is done. The counts below therefore do not sum to 70, and the
partition that does is stated explicitly at the foot.

## Group A — declined on a rule (6)

| PR | decision | why |
|---|---|---|
| #6233 | **Decline** | **Non-negotiable #3: SQLite only, no Postgres.** This PR is a second database engine: `pkg/postgres/` with 13 of its own migrations, a `postgres` dialect in `internal/manager/init.go`, and a `test-postgres` CI workflow. The rule is not "we prefer SQLite" — it is that **two stores means two code paths for every query**, and this fork's store layer is written against the `pkg/sqlite` idioms (`repository{tableName}` + `*table` manager; one `StructScan` per callback; `Timestamp`/`null.Time`). Every one of those has a second answer under Postgres, and a second answer is a second set of bugs we would find ourselves. Declined by rule 2: a PR that removes a non-negotiable is declined, not negotiated. |
| #6503 | **Decline** | **Non-negotiable #5, and it breaks the precondition for #4/#5/#16.** A `[WIP/PoC]` multi-user backend: 40 files across `internal/api/authentication.go`, `directives.go`, `context_keys.go` and the GraphQL schema. Stash has **no user model** — auth is one shared username/password in `config.go` (a verified fact in `docs/GOAL.md`, not an assumption). A real user model is the *precondition* for every governance rule the fork holds, because each is a statement about **who** did a thing: #4 computed-not-stored, #5 proposals-only, #16 automatic curation writes proposals. Merging a PoC of it lands 40 files of half-built identity under rules that assume identity exists. It is also self-declared WIP. |
| #6824 | **Decline** | **WIP, and half of a pair.** Audio backend: 40 files, conflicting, draft, rewriting `gqlgen.yml` and the schema. Declined as WIP — **revisit when the author marks it ready.** |
| #7146 | **Decline** | **WIP, and not independent of #6824.** Its own description says it "includes Backend from #6824", so the pair is one change offered twice. Merging one half of a pair whose other half is required is not a decision, it is a broken build. |
| #6986 | **Decline** | **Non-negotiable #12, as a rule question rather than a verdict on the author.** A fingerprint *submission queue* sends scene fingerprints to Stash-box in batches. Fingerprints are derived from media content and are not identifying **as sent**. But the change lives in `pkg/stashbox/graphql/generated_client.go` and the queue is a **bulk egress** path — the exact shape where a later field addition quietly becomes identifying. Declined, and recorded as a standing rule: **if Stash-box adds a field to the submission payload, the exporter's guard must be extended in the same commit.** A one-sided guard is half a guard (#12). |
| #5265 | **Decline** | **A framework major bump from a bot, in a triage batch.** `bootstrap 4.6.2 → 5.0.0`, conflicting, 744 days stale. Nothing here breaks a non-negotiable, so the rule that declines it is deliberately a *process* rule: do not take a UI framework major in a batch of unrelated triage. Re-take it on its own, with `vite build` green. |

## Group B — deferred, sound, not now (56)

Same reason in every row: **sound upstream, no rule conflict, deferred to the
post-reconciliation batch**, each on its own commit with its own verification.

**B1 — `CONFLICTING` by git's own report (12).** The conflict *is* the finding;
re-listing it adds nothing.

```
#4011 #4699 #4733 #4785 #5012 #5606 #6197 #6440 #6479 #6685 #6783 #7038
```````````````````````````

**B2 — mergeable, and a Group C candidate this batch (9).** Listed in Group C.

```
#6917 #7093 #7181 #7235 #7245 #7249 #7252 #7254 #7261
```````````````````````````

**B3 — mergeable, sound, not a candidate in this batch (32).** Deferred on the
same sequencing reason; they are simply not the ones I would take first. Each
one's own risk is recorded by its own file count — #6951 is 37 files and #7126
is 32, which is why neither is a "quick merge" despite being mergeable.

```
#6179 #6224 #6519 #6828 #6848 #6896 #6908 #6927 #6934 #6951 #6957 #7025
#7030 #7048 #7061 #7088 #7097 #7126 #7143 #7158 #7172 #7195 #7203 #7214
#7215 #7220 #7224 #7227 #7237 #7248 #7259 #7264
```````````````````````````

**Why a mergeable, pure-UI, one-file PR is still not a merge.** #7235, #7245 and
#7249 are each a single CSS file, mergeable, with no backend surface. The
temptation is to bank wins to look productive. Three reasons not to, the first a
rule rather than a preference:

1. **A UI merge is not verifiable on this branch yet.** `ui/v2.5/build/` is a
   gitignored embed artefact (Trap 2). A UI PR is only honestly merged with a
   real `vite build`; without one, "merged" is a claim about a build nobody ran.
2. **Every UI PR collides with the fork's own frontend work.** The StashForge
   React frontend is ahead of `main`, so a scene-details styling PR bought onto
   `main` is a file the reconciliation will immediately churn.
3. **The goal document's own ordering.** Phase 1 decides; Phase 3 moves. A merge
   that will be replayed during reconciliation is work done twice.

## Group C — merge candidates, in order

Each is still one commit and one verification. **The first two are already done.**

| # | PR | why this one |
|---|---|---|
| 1 | **#7241 — DONE** | `6b8448b0d`. See `docs/PR-TRIAGE.md`. |
| 2 | **#7255 — DONE** | `60ba6c051`. See `docs/PR-TRIAGE.md`. |
| 2b | **#7180 — DONE** | `68192aa59`. Merged as written; one test added. See below. |
| 2c | **#7137 — DONE** | `3f6678e73`. Merged as written, decision EXTRACTED so it could be tested. Closes `stash#7136`. |
| 3 | **#7265 — DONE** | `5ca4c5806`. Merged as written; 107-check frontend harness added (no JS runner existed). Closes nothing — no linked issue. |
| — | **#7225 — DONE** | `63635bcc0`. Merged as written; threshold made a contract with migration 87, 10 mutations. Closes `stash#3722`. |
| — | **#7257 — DONE** | `6d5a8131c`. Merged as written except settings snapshot made a DEEP clone, not `maps.Clone`. Closes `stash#5944`. |
| — | **#7196 — DONE** | `b14aef421`. Merged as written; media-src made a slice like its siblings; 8 tests for a header builder that had none. Closes `stash#7197`. |
| — | **#7159 — DONE** | `352c7d105`. Merged as written for the Go fix — `GetHomeDirectory` called `user.Current()` and **panicked** on a uid with no passwd entry, which is exactly what a numeric uid gives you; now reads `$HOME` first via `os.UserHomeDir()`. Upstream's `user: "N:M"` mechanism **collided with ours**: Docker then starts the container already non-root, so `su-exec` cannot setuid and the container died with `setgroups(1000): Operation not permitted`, exit 1. **Two fixes to our entrypoint, neither sufficient alone** — verified by removing one at a time against a built image: both reverted → 5 of 6 new tests fail; only the first → 5 fail; only the second → 3 fail; both → 16/16 with all 10 pre-existing tests still green. |
| — | **#7166 — DONE** | `c71899e7f`. Merged as written, applied with `--3way` (#7196 had moved the media-src lines). Closes `stash#7165` — mis-ticketed *not-planned*, **2nd instance**. **3 fixes, one a security hole**: the wildcard check tested `u.Host` only, so `https://cdn.example.com/*` passed, and a path wildcard is a legal CSP source expression matching every request under that host; a CSP source expression is a *prefix match*, so "the host is exact" was never the property that mattered. Also a bare `csp_` key read as a source, and `http://.` / `https://..` passed the host checks. |
| — | **#7199 — DONE** | `c2bfd44ce`. Recursive requirement installation; the cycle guard and the manifest `Requires` line kept as upstream wrote them. **One fix, a crash**: `packageByID` returns `(nil, nil)` for an ID not in the index and `install` dereferenced it at `remote.GetPackageZip(ctx, *pkg)`, so a requirement the source does not publish panicked with a nil dereference. Pre-existing, but this PR is what makes a bad requirement name an ordinary input. Closes `stash#7198` — correctly ticketed *planned*, not mis-ticketed. |
| 4 | #6917 #7093 #7181 #7235 #7245 #7249 #7252 #7254 #7261 | Next. |

## The migration collision — the one finding here worth acting on

**#7225 creates `pkg/sqlite/migrations/87_phash_short_videos.up.sql`.** Measured
in this repo, this session:

```
main:       highest migration 86    appSchemaVersion 86
stashforge: highest migration 106
```

87 is free on `main` and **already taken on `stashforge`**. Merging as upstream
wrote it is safe today and a guaranteed collision at the reconciliation — and
that failure mode is one this project has been bitten by repeatedly:
`golang-migrate` applies by number, so a renumbered migration is one that never
runs on an instance that already applied the fork's 87, and a `COMMENT` claiming
otherwise is a claim, not a schema.

**Decision: merge the behaviour, renumber the migration** to a number free on
both branches. Non-negotiable #2 is "additive and CONTIGUOUS", and the fork's
number space is 106 while upstream's 87 comes from a branch that does not know
the fork exists. Recorded rather than silently renumbered, because whoever does
the reconciliation has to see it.

## #7180 — merged, and the one test that had to be written

`68192aa59`. Upstream's fix needed no departure from the patch: a
`.nogallery`/`.forcegallery` check, plus a refactor splitting
`findGalleriesToClean` (decides) from `cleanGalleries` (deletes).

That split is what made a real gap visible. Upstream's test calls only the
decider, so `if !j.input.DryRun` — which lives in the deleter — was untested.
`docs/mutate_7180.py` scored it **SURVIVED**: deleting the guard left every
upstream test green.

**A survivor here is not a redundant line.** A dry run exists so a user can see
what a clean would remove; its whole value is that it removes nothing. A
regression is data loss on the strength of a preview click. So the fix was to
**add a test**, not to delete the row — `TestACleanDryRunDeletesNothing`, which
drives the deleter in both directions, with the non-dry half as the control
that proves the deletion path is reachable at all.

Harness: **6/6 killed, 0 survived, 0 skipped**. Suite green twice, 35 packages
ok, verdict-identical between runs.

Two fixture traps are recorded in the files because both cost real time and
both present as something else: an unbounded mock expectation is permanent, so
the batch loop's terminating empty page matched forever and the test hung to its
own `-timeout` (looks like a slow test, not a broken mock); and `&plugin.Cache{}`
panics *after* `Destroy` has already run, because `enabledPlugins` calls a
method on a nil interface.

## #7137 — merged, and the fix arrived with no test on the fix

`3f6678e73`, closing **`stash#7136`**. The bug: `urlFromCDP` always finished with
`chromedp.OuterHTML`, so a scraper whose target is a JSON *document* got Chrome's
HTML wrapper around it — valid HTML, invalid JSON, a parse failure that looks
like a broken scraper.

**Merged as written, except that the decision was extracted.** Upstream's three
parts are the mime sniff, the tracker, and the branch that reads the body. The
first two arrived well tested; the third was inline in a `chromedp.ActionFunc`
and reachable only by running a browser, so **the repair itself had no test** —
sitting in a file that looks thorough.

The harness row for it reported `SKIP / anchor not found`, which the standing
rule reads as "the code moved". What it meant was that the branch **could not be
guarded at all**. So the decision moved into `readMainDocument` with the two
Chrome accessors as parameters, and four tests now drive it — written to record
*which accessor was asked*, not what came back, since a test asserting the
returned string is one line from asserting its own input.

**That is the second time this session that a `SKIP` was a finding rather than a
stale anchor**, and the two are hard to tell apart. The discriminator: a stale
anchor is post-fix source that has *moved*; an unobservable branch is post-fix
source that was **never reachable from a test**. Reading the source settles it in
two minutes, and guessing wrong means either deleting a real fix or leaving an
untested one.

Harness `docs/mutate_7137.py`: **7/7 killed**, including the data race — scored
as a kill even though the test's own assertions pass on a torn read, because it
checks only the end state. `go test -race ./pkg/scraper/` clean.

## #7265 — merged, and it turned up a race nobody was looking for

`5ca4c5806`. No linked issue, so nothing closed — the first merge this session
that closed nothing, and worth saying so plainly rather than implying progress.

**The frontend had no test at all, and the frontend is where the PR's work
lives.** `normalizeDateString` is 66 lines of validation in `src/utils/yup.ts`
and the UI has no runner: no `test` script, no jest, no vitest. The backend half
got tests; the half that decides what the user sees got none. Added
`scripts/test-date-normalisation.mjs` on the existing `check-country-names.mjs`
precedent — 107 checks, including a cross-language one that feeds every accepted
string to a Go probe, because a front end that normalises to something the API
rejects has *moved* the error rather than fixed it.

Three harness facts worth more than the PR:

1. **It cannot import `yup.ts`.** Line 1 imports `FormikErrors`, a type formik
   exports only from its `.d.ts`; node's ESM resolver rejects the whole module.
   `tsc --noEmit` and `vite build` are both clean, so this is node being
   stricter than the project, **not a defect in the file** — the tempting
   "fix" is to change yup.ts, which would be fixing a non-problem.
2. **`new Function("f", src)` passes a parameter name, not a binding.** Each
   function called the other and both returned `undefined` for every input. The
   wiring is now asserted by calling each and checking the return *type*.
3. **Two of my assertions were wrong before the code was ever wrong** — one
   flagged five lines of valid JavaScript by scanning for `<` and `?` in
   "non-operator position", which is not decidable per character.

## The race, found by accident and worth more than the PR

The full suite failed 2 times in 18 runs on two `pkg/ffmpeg` tests: *"want 12
bytes, got 1"*. Nothing to do with dates. **Not flaky — a real race the existing
tests were right to catch.**

`getTranscodeStream`'s stderr goroutine called `cmd.Wait()`, which closes the
child's pipes. The handler reads stdout — one byte to detect startup, then
`io.Copy` — so `Wait` landing between the peek and the copy truncated every
stream to exactly one byte. For MP4 that is a cut `ftyp` box: downloads fine,
will not play. Replaying the structure 3000 times: **426 truncated (14%)** with
`Wait()` there, **0 of 3000** without.

The instructive failure was my own first fix. A `defer` in `getTranscodeStream`
is scoped to *that function*, which **returns the handler** — so it fired at
`return handler, nil`, closing stdout before a byte had been read, and turned
the two tests into a hard `500, body empty` on every run. Strictly worse than
the race. A defer belongs to the function it is written in, not to the work that
function sets up.

**The "still not fixed" caveat in the commit was WRONG, and here is the
correction.** It claimed ~0.3% of serves still truncate, blamed
`LockContext.Cancel` at `pkg/fsutil/lock_manager.go:33`, and shipped that
attribution. Re-measured properly, it is not a bug at all:

- Classifying by **status code** rather than body length: across **3840
  concurrent serves, `truncated200=0`**. Every single "short body" was a **500**
  carrying `fork/exec ...: text file busy` — **ETXTBSY**, the kernel refusing to
  exec a file whose descriptor is still open for writing. That is a property of
  the *hammer's own stub creation* (`os.WriteFile` racing its own `exec`), not of
  the transcode path.
- Replaying the handler's exact post-fix shape — both pipes, stderr drain
  goroutine, `exec.CommandContext`, one-byte peek, `io.Copy` gated on
  `readErr == nil` — 2000 times: **0 short bodies, and the peek returned
  `(1, nil)` on all 2000**. The `readErr == nil` gate I had suspected is not
  reachable.

So the real result is **`037c9d6d1` fixed it completely**, and the commit
message overstates the remainder. Two distinct mistakes, both mine:

1. **A body-length check cannot tell a truncated stream from a failed start.**
   `500` and a short `200` are different failures, and lumping them invented a
   0.3% bug that did not exist. Classify by status code first.
2. **I attributed the residue before measuring it.** `LockContext.Cancel` is a
   real second `Wait()`, but it is not what the hammer was showing. Naming a
   plausible-looking culprit I had not tested is worse than saying "unexplained".

The harness is at `docs/mutate_ffmpeg_wait.py`; it asserts the classification
itself, since a regression guard that cannot tell these two apart is what caused
the whole misreading in the first place.

Suite: **6/6 rounds clean**, 35 ok, 0 FAIL, against a 2-in-18 baseline.

## #7225 — merged, closing `stash#3722`, and the threshold that is really a contract

`63635bcc0`. The sprite was a fixed 5x5 grid, so a 30-second clip got 25 frames
sampled 1.08 seconds apart — near-identical frames, a repetitive montage, and
unrelated short videos hashing alike. The grid is now NxN: 2 frames to 45s, 3 to
90s, 4 to 150s, 5 beyond. Merged as written, upstream's own tests included.

**The merge was easy; the threshold was the work.** `150` appeared in the Go
switch *and* in migration 87's SQL with nothing connecting them, and both failure
modes are silent:

- too narrow — incomparable hashes survive in every existing database while the
  migration note tells the user the problem was fixed;
- too wide — every affected video is re-hashed for nothing.

So it is an exported `MaxChangedDuration` documented as a contract *with the
migration*, and a test reads the migration file and compares its literals against
the algorithm. Three more cover what literals cannot see: that the algorithm
really treats the boundary as the boundary (a switch edited to `<= 90` leaves the
constant at 150 and passes a literals-only check), that the migration filters on
`type = 'phash'`, and that `appSchemaVersion` reaches 87.

A second test **runs** the migration against a real SQLite. Reading the SQL
proves the text; running it proves the rows.

**Two fixture bugs, and both are the same lesson.** The first probe invented its
own schema (`id INTEGER PRIMARY KEY`, no `fingerprint` column), so the
migration's own subquery failed to resolve — and a failed subquery inside `IN()`
does not abort the `DELETE`, so every phash went including the 200-second ones.
The report read *"this migration destroys far too much"*, which is exactly what a
destructive migration looks like. **The migration was right and the fixture was
fiction.** The second: every row shared one fingerprint string, and the table is
keyed `(file_id, type, fingerprint)`, so two inserts silently vanished — caught
only because the seeded count is asserted, and that count was itself in the wrong
place, sitting *after* the migration and so reporting survivors instead of seeds.
**Assert a precondition where the precondition holds.**

10 mutations, all killed. Suite 3/3 clean, 36 packages ok (up from 35).

## #7257 — merged, closing `stash#5944`, and the clone that was only half a clone

`6d5a8131c`. A JS plugin had no access to its own configured settings — only
`args` and `server_connection` — so it could not read a value the user had set
for it. `input.Settings` now carries them. Merged as written, upstream's own 198
lines of tests included, all green under `-race`.

**Changed: the snapshot is now a deep clone.** Upstream used `maps.Clone`, which
copies the top level only. Settings come from Viper's `Raw()`, so any structured
value is the *same memory* the configuration holds, and goja hands a Go map to JS
as a reference — so `input.Settings.tags[0] = "x"` writes straight into the live
config, which the next `SetPluginConfiguration` persists. Measured through
`buildPluginInput`: the stored settings came back mutated.

**The shape to recognise in an incoming PR:** a test named after a property,
asserting it on the one input where the property trivially holds.
`TestJSPluginCannotMutateStoredPluginSettings` mutates `enabled` — a *scalar*, the
one value a top-level clone detaches. It passes, it is named for isolation, and
the isolation does not hold for anything a real setting contains.

**Measuring the boundary library changed what the tests assert.** My first list
test asserted `push` is contained. That is *true* — and it also passes against the
broken clone, so it proved nothing. goja wraps a Go slice into a JS array that is
not a live view: `push`/`splice`/`pop` mutate the VM's own array, and only
in-place element assignment writes through. So "the slice is copied" and "the
elements are copied" are separate guarantees, and only the second is violable.
Both are now tested, and the measurement is kept as a named test so the reasoning
survives.

9 new tests; against `maps.Clone`, **5 fail**. The deep clone is also load-bearing
for the *concurrency* claim: a shared nested map handed to a VM running beside the
settings writer is a data race, not merely a leak.

Suite 3/3 clean, **38 packages ok** (up from 36).

## #7196 — merged, closing `stash#7197`, which was ticketed *not-planned*

`b14aef421`. A plugin whose UI loads media from an external origin had no way to
allow it: `media-src` was a fixed `blob: 'self'`. So the media failed to load
**with nothing in any log** — the most expensive kind of bug, because there is no
error to grep for.

**Two changes beyond the merge, and the second is the point.**

`media-src` is now a **slice** like its three siblings. Upstream added it as a
bare string `+=`-ed inside the plugin loop, which works — a stray space is
harmless to a browser — but differs in shape from connect-src/script-src/style-src
and accumulates an empty segment per plugin that configures none. Printing the
header both ways: identical apart from a trailing space.

**`setPageSecurityHeaders` had no test at all.** That is exactly what a one-line
addition to a header builder needs a test for, because the failure mode is a
silently dropped origin. 8 tests now cover it, and they are mutation-checked: the
PR's line removed kills 5 of the 8; the `plugin.Enabled` gate removed kills the
disabled-plugin test; only the first entry kept kills the all-entries test; the
default dropped kills 4.

**A mis-ticketed issue is worth noticing.** `stash#7197` was marked
*not-planned* on rule R10, yet #7196 exists upstream and closes it in three
files and six lines. The rule was applied to the issue's phrasing rather than to
whether the work was about to land. Recorded because the next R10 issue may have
the same problem: **check whether an upstream PR already closes it before
honouring a not-planned verdict.**

Also fixed in the ledger while here: #7265 was still listed as a pending Group C
row long after it merged, and a renumbering had duplicated #7159. The table now
asserts no merged PR appears as pending and carries no duplicate rows.

## #7166 — merged, closing `stash#7165`: the SECOND mis-ticketed issue, and a real hole

`c71899e7f`. A plugin with a user-configurable backend endpoint had no way to
allow it — `connect-src` is assembled per plugin, so a host the admin chose was
silently blocked. With `csp-settings: true`, any `csp_`-prefixed setting holding a
valid `http`/`https` URL joins that plugin's `connect-src`.

Applied with `--3way`, because #7196 had already turned `media-src` into a slice at
the same lines and the patch context had moved. `server.go` merged cleanly; the two
`Plugins.md` conflicts were both **additive** — each side documenting its own field
in the same `ui:` block and the same paragraph — so both were kept. Upstream's
`server_test.go` and my `server_csp_test.go` do not collide, by filename.

### The hole

Upstream's wildcard check tested **`u.Host` only**:

    !strings.Contains(u.Host, "*")

so `https://cdn.example.com/*` **passed** — and a path wildcard is a legal CSP
source expression that matches *every request under that host*. The asterisk one
character to the right of the host bought the whole subtree.

The lesson is the property, not the case: **a CSP source expression is a prefix
match, so "the host is exact" was never the property that mattered. "The value is
exact" is.** A check placed on a *parsed component* while the *emitted text* is the
original string is only as good as the assumption that the two agree — and here
they disagreed across the component boundary.

Upstream's own docs already said "a valid, **concrete** `http` or `https` URL", so
this restores the stated intent rather than narrowing it.

Two smaller fixes: a bare `csp_` key was read as a source (a prefix match with
nothing behind it — an opt-in that reads as *off* to anyone inspecting the key),
and `http://.` / `https://..` passed the host checks since `Hostname()` is
non-empty for both.

### Two tests that assert properties, not examples

A table of example strings is only as complete as whoever wrote it. So:

- the validator refuses **every** character in `" ,	
;"'"` anywhere in an
  accepted value — asserted as a *character set*, with a control value asserted
  accepted first, so the test cannot silently measure nothing
- the emitted `connect-src` is byte-identical across **200 runs** — an unsorted map
  iteration would make the header differ per request and bust any proxy or cache
  that compares it

Mutation-checked, 3 mutations, **each killed by exactly the test written for it**:
reverting the path-wildcard fix kills `TestWildcardIsRefusedWhereverItAppears`,
reverting the bare-`csp_` fix kills `TestOnlyTheExactCspPrefixIsRead`, reverting
the all-dot fix kills `TestConnectSrcValidatorRequiresARealHost`. That one-to-one
correspondence is the check that the tests are not passing for a shared reason.

## The not-planned audit

`stash#7197` was the first wrong *not-planned* verdict; `stash#7165` is the second.
So rather than fix them one at a time, the whole R10 not-planned set was swept:
**131 issues** checked against every open PR in the queue, batched through the
GitHub API in 6.5s.

**One more hit.** Upstream PR **#7048** (`feat: VR metadata support for VideoFile`,
17 files, OPEN + MERGEABLE) closes `stash#7071`, also ticketed *not-planned*. That
is the third instance of the same error, found by the sweep rather than by luck.

`stash#7071` is recorded as closed but **deliberately not merged here**: VR
metadata is a schema change with no local verification path, so merging it blind
would be exactly the claim-without-evidence this project exists to prevent.

The sweep is the durable part. A triage verdict is a snapshot of what you knew, and
the queue is the thing that changes — so **check whether an open PR already closes
an issue before honouring a not-planned verdict**, and batch the check rather than
running it per issue.

## #7159 — merged: the second mechanism for an issue we had already closed

`352c7d105`, closing `stash#684`. Not new work — this fork already fixed #684 with
`PUID`/`PGID` and `su-exec`, verified against a real built image. Upstream's is a
different mechanism (`user: "1000:1000"` in compose), so both now coexist.

The Go half is one real bug, and it is worth being precise about *why*: the old
`GetHomeDirectory` called `user.Current()`, which **panicked** on error. Verified
from the Go source that this is reachable — `os/user` returns
`user: unknown userid N` for a uid it cannot resolve, and a numeric uid with no
passwd entry is exactly that. The fix reads `$HOME` via `os.UserHomeDir()` first.
**The ordering is the fix, not the existence of the call** — `os.UserHomeDir()`
consults the environment and never the passwd db, which is the whole property that
makes a numeric uid work.

### The collision, which only a container could have revealed

Upstream's mode starts the process as uid 1000, so our entrypoint is **already
non-root** and then tries to drop privileges again:

    su-exec: setgroups(1000): Operation not permitted
    exit 1

Reproduced against a real image, not inferred. A non-root process cannot setresuid,
**not even to itself**, because clear-groups needs privilege. The `chown`s above are
`|| true` so they were harmless; this line is not, and under `set -e` it kills the
container with no useful message.

Two fixes, and the *shape* of the verification is the point:

1. **The uid Docker started us as was ignored.** With `--user 1500:1600` and no
   `PUID`, the script resolved the image's own `stash` account (1000) and attempted
   an impossible 1500→1000 drop — the same failure reached a different way. Now a
   non-root start with no requested identity treats the current uid as the request;
   only root falls back to the image default, because root genuinely can drop.
2. **Guard the hand-over**: if the current uid:gid already equals the target, exec
   straight through instead of calling `su-exec`.

### Neither fix is sufficient alone, and that is the evidence

Removing one at a time against a built image:

| variant | result |
| --- | --- |
| both reverted | 5 of 6 new tests fail |
| only fix 1 | 5 of 6 fail |
| only fix 2 | 3 of 6 fail |
| both present | **16/16 pass**, all 10 pre-existing tests still green |

If either fix were dead code, one of the "only" rows would have been clean. They are
not, so neither is dead, and the tests are not passing for a shared reason.

One of the six passes in every variant: `user:` beating a leftover `RUN_AS_ROOT=1`.
That is *correct* — `RUN_AS_ROOT` exits before `su-exec`, so it is not evidence for
anything. Kept only to pin precedence, and marked as such rather than counted as
proof.

Also verified rather than assumed: the `.dockerignore` negation
(`!docker/build/x86_64/cuda-entrypoint.sh`) actually lets the `COPY` resolve under
an excluded `docker/` directory — built a probe image and compared the sha256.

## #7199 — merged, closing `stash#7198`, and two fixes that have NO WITNESS

`c2bfd44ce`. `Requires` was parsed into the remote index and never acted on, so a
plugin naming a dependency produced an install that could not run. Install now walks
the tree, installing what is missing and updating what is outdated.

`stash#7198` was correctly ticketed **planned**, so unlike #7197/#7165/#7071 this one
needed no correction — worth saying, since three of the last four were wrong.

### The one fix with a witness: a crash

`packageByID` returns `(nil, nil)` when the ID is not in the index, and `install`
dereferenced it unconditionally:

    fromRemote, err := remote.GetPackageZip(ctx, *pkg)   // panic

So a requirement the source does not publish — an ordinary condition — **crashed the
process**. Pre-existing in `Install`, but recursive requirement resolution is exactly
what turns a bad requirement name into a normal input. 13 tests drive the real path
against a `file://` repository, so real zips, real sha256 and real manifests; the nil
guard is killed by reverting it.

Kept upstream's `remotePkg == nil || !local.Upgradable(...)` branch deliberately: an
installed package the source no longer publishes must not fail the whole install.

### Two fixes with NO witness, recorded as such

Both were added, and in both cases **reverting the fix leaves the suite green.**

**The `installing` map is never undone.** Upstream marks `spec.ID` and never removes
it, making the map a "seen in this call tree" set rather than a cycle guard. Added
`defer delete`. Then found the fix is undetectable, for two reasons:

- `Install` builds a **fresh map per call**, so a cross-call leak is invisible from
  outside by construction.
- Within one call tree the two guards produce **different visit sequences but the
  same outcome**, because `install()` is idempotent for an already-installed package
  — it uninstalls and reinstalls the same bytes.

Ran both shapes side by side to confirm the second point rather than assuming it.

**The cycle guard is not load-bearing.** Deleting it entirely still passes: the test
traced `err=nil` with both packages installed. The reason is in
`installRequirements` — it recurses into a requirement only when that requirement is
missing or outdated, so on the second visit to an ID in a cycle the package is
already current and the `continue` fires.

Both kept anyway. A guard should be scoped to the path it guards, and a future change
to `installRequirements` would turn a hang into a stack overflow. But they are
insurance, not fixes, and **a test that cannot fail is worse than no test** — so both
were relabelled in the test file to say they have no witness, and one entirely vacuous
test was deleted rather than kept to pad the count.

The general rule: **when a fix cannot be made to fail, say so in the test that appears
to cover it.** A test named for a fix implies the fix is verified, and the next person
to revert that line will trust the test rather than re-derive the reasoning.

## The count — verified, not eyeballed

`docs/pr_triage.py report` is the check: it re-reads `pr-queue.json` and prints
every PR number. The partition below was computed against that snapshot and the
five buckets are **disjoint and sum to 70** — asserted, because the first draft
of this file claimed 6+46+20 and was wrong: Groups B and C overlap by
construction, and eight PRs (#6224 #6440 #6519 #6685 #6896 #6951 #7038 #7172)
had been dropped from every table by hand-typing rather than by any rule.

```
declined on a named rule .............................  6
merged + committed ...................................  11
deferred, CONFLICTING by git .........................  12
deferred, mergeable, Group C candidate ...............  9
deferred, mergeable, not a candidate in this batch ...  32
                                                     --
total ...............................................  70
```

**All six declines are `CONFLICTING` PRs.** That is not a coincidence and it is
worth stating: nothing mergeable was declined. Every PR that git could apply
cleanly is either merged or deferred as sound, and the six that were refused on
a non-negotiable were refused *on the rule* — the conflict is incidental. It
also means the conflict count in a queue is not a measure of PR quality: 12
mergeable-looking PRs are unmergeable, and the 6 rule-declines would have been
declined regardless.

**What this does not claim.** 62 deferred PRs is not 62 fixed bugs; the number
is a queue, not progress. What makes it more than deferral is that every
deferral names why it is *safe* to defer and every decline names *which rule* it
breaks — so the next session re-checks rather than re-derives.
