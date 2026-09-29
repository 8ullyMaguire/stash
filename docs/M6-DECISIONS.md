# M6 decisions log

Every judgement call I made in the M6 pass, with the reason. The owner asked
for best judgement on what was left and for the calls to be written down rather
than buried in commits, so this file is the record. Anything here can be
reversed by editing this file and the code it describes.

Branch: `m6-upstream-issues`. Worktree: `~/code-local/worktrees/m6`.
Started 2026-09-27.

---

## Scope

**Decision: "solve all the issues" means capability-by-capability against the
committed taxonomy, not 850 individual patches.**

The 850 issues are mapped to 70 capability codes (`docs/research/map_final.json`).
The largest, C03 "Image & gallery sets", carries 174 of them; the smallest
carry one. Working issue-by-issue would mean 850 near-duplicate changes and a
diff no maintainer could review.

**Why:** the plan says the same thing — "Work M6 by dependency, not by issue
count" — and the matrix's own A.1 section already identifies the multi-issue
capabilities.

**Consequence for any claim about the count:** a capability that closes five
issues is recorded as one row in `docs/closed-issues.md` naming all five
numbers. The honest summary of M6 is therefore "N of 70 capabilities built",
never "850 issues fixed", and the post must say which.

## The 850 figure

**Decision: treat 850 as a coverage claim, never as a completion claim.**

Verified 2026-09-27 against the committed snapshot: 673 open in
`stashapp/stash`, 177 in `stashapp/stash-box`, 850 mapped, 0 unmapped, 0
phantom entries, 70 distinct capability codes.

Built at the start of this pass: M0–M4 complete, M5 partially (the downloader's
consent gate, path gate, storage gate and seeding policy, with a build prompt
in progress on another worktree). **0 of the 70 M6 capabilities were built.**

**Why this is written down:** the number 850 reads as "issues solved" to almost
any reader, and it is not that. The matrix maps every open issue to a
capability and a spec section; the capabilities are not built. GOAL.md's own
milestone table lists M6 as "The 850 upstream issues, capability by capability"
as a *future* milestone, which is the clearest internal evidence that the
number is scope, not progress.

## What counts as a capability being done

**Decision: a capability is done when its behaviour exists, its test passes,
the upstream suite count has not gone down, and `docs/closed-issues.md` names
the issues it closes.**

Rejected alternatives:

- **A spec section is written** — rejected. §5–§15 of the spec already
  *describe* all 70 capabilities. Writing them is what the matrix measured, and
  treating it as progress is the mistake this log exists to prevent.
- **The test passes** — necessary, not sufficient. A capability can be
  implemented as an empty handler behind a passing test; the mutation check
  (non-negotiable #10) is what distinguishes a guard from a no-op, so every
  capability with a security or governance consequence gets one.
- **A test is written** — rejected as a standalone. Step 6.0's own tests are
  evidence of this: they passed on their first working run and two of the three
  parser bugs were found only by running them, before any mutation check.

## Upstream suite is a floor, measured not transcribed

**Decision: record the real count on this worktree rather than carrying
GOAL.md's table forward.**

GOAL.md's table tops out at `m3-metadata-sharing` (682 unit / 1158
integration). Measured on this worktree at branch `m6-upstream-issues`:
**39 packages ok, 1637 tests passing, 0 failing, 0 skipped.**

**Why:** the table predates the M5 plugin work, so using it as the floor would
make the suite look like it had lost 955 tests. Non-negotiable #1 says the pass
count may only go up; that is only checkable against a real number.

**Caveat, stated because it matters:** 1637 counts this worktree's `./...`,
which includes the plugin module. GOAL.md's rows counted the core tree at
milestone boundaries. The two are not directly comparable, so the floor for
future capabilities is *this* run, and every milestone records its own.

## Worktree isolation

**Decision: all M6 work happens in `~/code-local/worktrees/m6` on branch
`m6-upstream-issues`. The primary checkout is not touched.**

**Why:** another agent is actively building the P2P downloader plugin. At the
start of this pass the primary checkout had five uncommitted files under
`plugins/p2pdownloader/internal/torrent/` plus `docs/HANDOFF.md`. A
docs-only commit there risks a `git add -A` from either side absorbing the
other's work, and the plugin is explicitly excluded from M6 (non-negotiable
#11: the downloader is a plugin, not core code).

**Consequence:** `docs/HANDOFF.md` is *not* updated by this pass. It is shared
with the other agent and is updated when that work lands, not concurrently.

## Ordering

**Decision: work by dependency, starting with the capabilities that unblock the
most downstream work, not the capabilities that close the most issues.**

The plan names C15 (scanner throughput, 15), C17 (background job engine, 14)
and C20 (storage accounting, 12) as the ones that unblock most downstream
fixes, and the counting below shows the same three are the only capabilities
that other capabilities repeatedly depend on.

**Why not the biggest first:** C03 alone is 174 issues and is the largest single
block of work in the milestone. Starting there maximises the chance the pass
ends with one large unfinished thing rather than several finished ones.

## Ordering, in practice: bugs first, mapping second

**Decision: while the mapping is unreliable, work issues whose defect is
self-evident from the issue text alone — crashes, security, data loss — rather
than waiting for a trustworthy capability to work under.**

Three such issues are already fixed in this branch, and all three were invisible
to anyone triaging by capability name:

- `stash#7240`, an arbitrary file write via zip-slip. Filed under C63
  "Confirmation on cancel".
- `stash#7240`'s second half, package install, a separate defect in a separate
  subsystem. I fixed import first and did not notice the package-install path
  until I audited all four zip sites — which is the lesson: fixing the site named
  in the issue is not the same as fixing the issue.
- `stash#7152`, a nil dereference that aborted whole stash-box batch jobs. Filed
  under C36 "Leaderboards & badges".

**Why:** a capability mis-mapping delays work by making the unit of work
wrong. A crash is a crash regardless of which box it sits in, so these can be
done while the mapping is being rebuilt, and they are the highest-severity
issues in the set.

**The trap to name:** the "fix the one site the issue names" instinct is what
left the package-install half unfixed for a commit. Every fix after the first
one got a survey of the whole pattern first.

## Reporting honesty

**Decision: the milestone summary leads with capabilities built, not issues
mapped, and names what is left.**

The prior public progress post said "we solved every issue from both repos".
That is not true of the current state and would not survive a reader opening
issue #12. This log records the decision so the next post cannot drift back
into it: the accurate claim is *every issue is mapped to a capability and a
spec section, and N of those capabilities are built and tested* — which is a
stronger claim precisely because a reader can check the N.

## The "never a fork" line in the progress post

**Decision: out of scope for code, but it is wrong and should not ship.**

The progress post says the P2P downloader is built "as plugins against a stock
upstream core — never as a fork of it". The repository is a fork: its only
remote is `upstream` → `stashapp/stash`, the spec opens "Base: fork of
`stashapp/stash`", and `docs/BASELINE.md` exists to prove each milestone did
not break upstream.

What is true, and is the claim with the byte-level test behind it, is that the
*downloader* is a plugin and the *core* is unmodified. Noted here because the
next thing written about this project should say both halves in the same
sentence.

## Decisions deferred (recorded, not taken)

- **Whether a "closed" upstream issue should be reopened or left alone.** The
  matrix was built from a 2026-09-26 snapshot; some of the 850 may have been
  closed upstream since. Re-proposing work upstream is the owner's call, and
  this pass does not touch either upstream repo.
- **Whether `docs/closed-issues.md` lives in-repo or in the SecondBrain
  project folder.** In-repo for now, next to the matrix it indexes, because the
  two must be diffed together.
