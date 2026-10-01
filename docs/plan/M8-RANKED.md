# M8 — the ranked build list

**Written:** 2026-10-01, after the single-branch consolidation.
**Branch:** `main`. **Base:** `9e7d1be31`.

Twenty-six candidate ideas, ranked by impact ÷ effort, highest first. This file
records the verification done before planning, because **four of the stated
premises did not survive contact with the code.** Ranking an unverified idea is
how a plan ends up scoping work that already exists.

## What the spec already has, verified 2026-10-01

| # | premise | verdict |
|---|---|---|
| 1 | `collab_audit` is append-only "by convention" | **TRUE.** The table exists with no hash column. |
| 4 | `invite_keys` already exists | **TRUE.** `88_invite_keys.up.sql`, 22 Go files. |
| 6 | `pkg/stashbox` client is preserved | **TRUE.** `client.go`, `graphql/`, and 5 type files. |
| 10 | plugin signing is an open decision | **TRUE.** Zero `ed25519`/`Sign(`/`Verify(` in `plugins/`, `pkg/plugin/`. |
| 16 | stash already has phash and a duplicate checker | **TRUE, and it matters.** `internal/identify/` is 9 files; migrations `20_phash`, `87_phash_short_videos`; 43 Go files reference phash. §6a.14's "a new mechanism" is mostly a reuse. |
| 22 | ed2k is the largest effort sink in M5 | **TRUE, by a wide margin.** ed2k 2,484 + ed2ktransfer 1,104 + **ed2kwire 10,033** = **13,621 lines**, against torrent's 3,203. |
| 3 | no schema-walking test exists | **TRUE.** No test loads the GraphQL AST. 121 Go files in `internal/api`. |

**Two of my own greps were wrong first**, and both had to be redone:
`grep -rl 'quest'` matched "**request**" in 246 files and `directory` matched
ordinary path handling in 101 — all false. Shell quoting inside the pattern also
broke two probes and returned empty, which read as "absent" rather than as an
error. A broad word match on a large codebase proves nothing; a missing package
needs its directory listed.

## Spec risks — one is a non-issue

**Migration numbering (RISK 1: 1100+ vs "after 86").** Resolved by the code:
migrations run **1..106 contiguous**, `appSchemaVersion = 106`, nothing ≥ 1100.
There is no second scheme to reconcile. New migrations continue at 107.

**"Trust" vocabulary collision (RISK 2).** Real but small. Only one column
matches today: `weight`. Nothing named `trust`, `ballot`, or `access_level`
exists yet, so the collision is still preventable. Adopt before the first mesh
migration: `access_level` for §6a.11 permissions, `ballot_weight` for §5.3
governance. Two words, never one.

**Plugin tier path (RISK 3).** Unresolved and real. §7.1 says the plugin has no
path to write the tier *and* relies on core refusing individual requests. Those
are compatible only if the refusal has a home, which is what item 10 provides.

## The build order

Ranked by the supplied I÷E, ties broken so that each item unblocks the next.
E is the estimate of **remaining** work after verification, which is lower than
the estimate for several items because the groundwork already exists.

### Wave 1 — cheap, high impact (I÷E ≥ 4)

**1. Hash-chain `collab_audit`** (4/1). One column: `prev_hash`, `row_hash`
where `row_hash = sha256(prev_hash ‖ canonical row)`. A verify function walks
the chain and stops at the first break. This is the only item that makes an
existing spec *claim* mechanically true: §5.1 says the owner is not an admin
over content, and an audit log that can be silently rewritten does not support
that. Backfill the existing rows in the migration so the chain is whole from
row 1, or the verify function must tolerate a null head.

**2. Shadow-mode governance v2** (5/2). Run the reputation-weighted decision
beside flat quorum, log every disagreement, apply nothing. Defuses the open
weight-table question (§14) with data instead of a guess, and switching over
later is a flag rather than a rewrite. Test: a proposal where the two functions
disagree produces a shadow row and the applied result is still flat quorum.

**3. GraphQL visibility coverage test** (5/2). Walk the schema; every field must
declare a visibility tier or carry an explicit opt-out marker. Fails CI
otherwise. §5.3 puts the filter in the query layer, and 121 resolver files is
exactly the scale at which that quietly stops being true — the anonymous `public`
role is where UI-only filtering leaks. Test: a deliberately unannotated field
fails the walk.

**4. Invite lineage** (4/2). `invite_keys` exists; add `invited_by_user_id`,
render the tree for stewards, revoke a subtree, and damp sibling accounts'
ballot weight. Sybil resistance that does not depend on behavioural detection.

**5. Permanent publication preview** (4/2). Promote the first-sync dry run to a
standing "what would be published" view, diffable per entry, with per-entry
exclude. Makes §6.1's opt-out disclosure tangible rather than a promise.

**10. Capability tokens for plugins** (3/1). Scoped revocable tokens
(`locator.propose`), core still decides. Resolves RISK 3 and the open signing
decision without a PKI. Per-request refusal stays as the second layer.

**22. Scope ed2k out of the critical path** (3/1, savings). Not a feature — a
recording. 13,621 lines of hand-rolled protocol against a mostly-dead network,
with BitTorrent already working. Move it behind a build tag, do not delete it.

### Wave 2 — the mesh's foundations (I÷E 3–4)

**6. Consume-only federation** (4/2). Peer identification first — read a peer's
commons using the preserved `pkg/stashbox` client — before publishing or
replicating. Delivers "a private commons among friends" and defers consent and
replication, which are the genuinely hard parts.

**8. Completion score and quests as SQL views** (4/2). Early, per the brief, and
it cannot violate non-negotiable #4 because it is derived state by
construction. Use `access_level`/`ballot_weight` naming now.

**9. Tombstone feed** (4/2). Standalone subscribable list. Small, useful alone,
and the one propagation that must never be outvoted — so it needs no governance.

**11. Sybil simulator in CI** (5/3). Synthetic accounts attempt to cross
thresholds; assert the weighting resists. Without it "damping" is a claim. It
depends on 2 and 4 existing, which is why it is not wave 1.

**16. Reuse phash for dedupe** (3/2). Smaller than §6a.14 implies — phash and
`internal/identify` already exist. Image-frame phash only for images.

### Wave 3 — adoption and the rest

**7. Stash-box-compatible endpoint** (5/3), **12. Preservation dashboard**
(3/2), **13. RFC 9421 signatures** (4/3), **14. Seed from public stash-box**
(4/3, licence-check first), **15. Bradley–Terry Elo** (3/2), **17. Passkeys**
(3/3), **18. "Name this person?"** (3/2), **19. Leave-with-your-data** (3/2),
**20. Proposal webhooks** (2/1), **21. Public read-only role** (4/3), **23.
Gravity config-only** (2/1).

### Not planned

**24. Merkle transparency log** (3/4) and **25. Cross-instance attested trust**
(2/4) need peers and key distribution first. **26. Awards, guilds, mentorship,
streaks** (2/4) last, and only if the spec wants it: the risk named in the brief
is real — retention mechanics drift toward "reputation buys access", which §6a.10
forbids.

## Two rules for this wave

1. **A test that passes on unfixed code is not evidence.** Wave 1 is small enough
   to hold to the full standard: break the behaviour, watch the test fail,
   restore — in one process, so a silent restore cannot fake a pass.
2. **Verification precedes scope.** Each item below re-checks its own premise
   before the plan step is written, because six did not survive the first pass
   and one premise (ed2k's size) turned out to be understated by 4×.
