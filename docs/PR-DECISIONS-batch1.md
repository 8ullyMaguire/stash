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

## Group B — deferred, sound, not now (61)

Same reason in every row: **sound upstream, no rule conflict, deferred to the
post-reconciliation batch**, each on its own commit with its own verification.

**B1 — `CONFLICTING` by git's own report (12).** The conflict *is* the finding;
re-listing it adds nothing.

```
#4011 #4699 #4733 #4785 #5012 #5606 #6197 #6440 #6479 #6685 #6783 #7038
```

**B2 — mergeable, and a Group C candidate this batch (17).** Listed in Group C.

```
#6917 #7093 #7137 #7159 #7166 #7181 #7196 #7199 #7225 #7235 #7245 #7249
#7252 #7254 #7257 #7261 #7265
```

**B3 — mergeable, sound, not a candidate in this batch (32).** Deferred on the
same sequencing reason; they are simply not the ones I would take first. Each
one's own risk is recorded by its own file count — #6951 is 37 files and #7126
is 32, which is why neither is a "quick merge" despite being mergeable.

```
#6179 #6224 #6519 #6828 #6848 #6896 #6908 #6927 #6934 #6951 #6957 #7025
#7030 #7048 #7061 #7088 #7097 #7126 #7143 #7158 #7172 #7195 #7203 #7214
#7215 #7220 #7224 #7227 #7237 #7248 #7259 #7264
```

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
| 3 | #7137 | 2 files, and it **ships with** a test (`pkg/scraper/url_test.go`). |
| 4 | #7265 | 8 files, 4 of them tests; date parsing is pure and cheap to verify. |
| 5 | #7225 | **Renumber its migration** — see below. |
| 6 | #7257 | 7 files, 2 test files. |
| 7 | #7196 #7166 | Plugin CSP sources, 3 + 5 files. |
| 8 | #7159 | Docker non-root. **Overlaps our stash#684**, already fixed here: take the idea, diff against ours. |
| 9 | #7199 #7261 #7181 #7252 #6917 #7093 #7235 #7245 #7249 #7254 | Small, 1–4 files each. |

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

## The count — verified, not eyeballed

`docs/pr_triage.py report` is the check: it re-reads `pr-queue.json` and prints
every PR number. The partition below was computed against that snapshot and the
five buckets are **disjoint and sum to 70** — asserted, because the first draft
of this file claimed 6+46+20 and was wrong: Groups B and C overlap by
construction, and eight PRs (#6224 #6440 #6519 #6685 #6896 #6951 #7038 #7172)
had been dropped from every table by hand-typing rather than by any rule.

```
declined on a named rule .............................  6
merged + committed ...................................  3
deferred, CONFLICTING by git .........................  12
deferred, mergeable, Group C candidate ...............  17
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
