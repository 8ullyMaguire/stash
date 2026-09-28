# 0002 — A downloader that uploads is redistributing, and that is a decision

**Status**: accepted
**Date**: 2026-09-27
**Milestone**: M5, step 5.3
**Spec**: §7.1 (consent tiers), §7 (transfer surfaces)

## Context

spec §7.1 draws a line between *storing* a locator and *acting* on it, and
`internal/collab`'s consent gate implements it. That gate answers one question:
**may this locator be acted on at all?** It is asked once, at the moment a
transfer starts.

It does not answer a second question, which is: **may the bytes leave this box
afterwards?**

A BitTorrent client that uploads is not reading. It is writing to a library of
peers the operator never sees and cannot consent to, and the bytes are gone the
moment they are sent — no later decision retracts them. Fetching a torrent is
the thing the operator asked for; seeding it is a second thing that nobody
asked for and that the original request does not imply.

`anacrolix/torrent`'s default is to upload opportunistically, and its own
comment says so: *"Upload even after there's nothing in it for us. By default
uploading is not altruistic, we'll only upload to encourage the peer to
reciprocate."* That is a reasonable default for a client whose operator asked
for a BitTorrent client.

It is the wrong default here, and the reason is not politeness. It is that a
permissive default in a client pointed at a corpus of untracked, self-published
material means the downloader publishes strangers' work without anyone having
decided that it should.

## Decision

Seeding is **derived from the object's consent tier**, per torrent, and the
derivation is a table in `plugins/p2pdownloader/internal/policy`:

| tier | upload | why |
|---|---|---|
| `self_published` | allowed | the creator permits redistribution |
| `performer_claimed` | allowed | a claim by a party with standing; §7.1 treats it as covering redistribution |
| `third_party_permitted` | allowed | the locator was already gated on this at hand-off |
| `unverified` | **forbidden** | nobody has asserted anything; absence of a refusal is not permission to publish |
| `quarantined` | forbidden | the consent gate refuses it; a bypass must not also become a publish |
| `denied` | forbidden | likewise |
| *anything else* | **forbidden** | an unrecognised value is not an assertion by anyone |

`unverified` is the **common** case — it is where every object starts — which is
what makes the default the interesting one. The unrecognised-value row is the
other: a tier from a newer core, a hand-edited row, or a truncated database
falls to the restrictive branch deliberately, because every permissive tier is
a claim by an *identified* party and a value nobody can be identified for is not
one of them.

## Why this is not a setting

An operator who does not want their box seeding anything can turn it off
(`OperatorAllowedSeed: false`) without a code change. That switch is an **outer
bound**: it can make a permissive decision more restrictive and nothing else.
An operator who wants to seed something the tier does not permit cannot say so
at all, and `TestTheOperatorCannotWidenThePolicy` is the test that says so — it
is the direction a settings screen invites, which is exactly why it is pinned.

## Why this is not part of the consent gate

Because it is a different question at a different time, and conflating them
fails invisibly. The gate is asked once before a transfer starts. This is asked
continuously, for as long as the client runs. Both derive from the same tier, so
they cannot disagree, and both live on the **core** side of the seam: the plugin
has no tier of its own to read, and asking the thing that wants to upload to
decide whether it should would be asking the wrong party.

## Consequences

- `internal/policy` has no dependency on the library. It is a pure function from
  a tier string to a decision, which is what makes the mutation harness
  meaningful and the library's defaults irrelevant to it.
- The tier strings are **duplicated** across the module boundary, because the
  plugin cannot import `internal/collab`. That duplication is checked by reading
  both files' source and comparing the sets — and **both sides are read from
  source**, not one of them written out by hand, because the first version of
  the test did exactly that and two mutations survived it.
- A stale duplicate fails SAFE and silent: every decision falls to the
  restrictive branch, the downloader stops seeding everywhere, and nothing
  errors. That is why the drift test exists and why it reads the source rather
  than trusting a hand-written list.
- `TestThePolicyTableHasNoGaps` catches the other direction: a real tier in core
  that has no case in the policy table. It refuses too, for the same reason.
