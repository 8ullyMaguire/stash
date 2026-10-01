#!/usr/bin/env python3
"""Mutation-check the seeding policy (M5 step 5.3).

Every mutation here turns a refusal into a permission, which is the direction
that matters: an upload that should not happen publishes a stranger's material
to peers the operator never sees, and it is IRREVERSIBLE. No later decision
retracts bytes that have already left.

The library's own default is permissive -- `ClientConfig.Seed`'s comment says
uploading is opportunistic by default -- so "the tests pass" and "the policy is
permissive" are close enough that only a mutation harness tells them apart.

Five verdicts, anything but `killed` is a hole:

  killed    the named test failed
  survived  the tests passed -- a HOLE
  broken    does not compile -- NOT a kill
  unscored  the edit did not apply
  no-op     the edit changed no bytes

Run from the plugin directory:
  python3 internal/policy/mutate_policy.py
"""

import fcntl
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
PLUG = os.path.abspath(os.path.join(HERE, "..", ".."))
SRC = os.path.join(HERE, "seeding.go")
ENV = {**os.environ, "GOFLAGS": "-mod=mod"}
LOCK = os.path.join(PLUG, ".mutation-harness.lock")

SNAPSHOTS = {}


def snapshot(path):
    SNAPSHOTS[os.path.abspath(path)] = open(path).read()


def run(cmd):
    return subprocess.run(cmd, shell=True, cwd=PLUG, capture_output=True,
                          text=True, env=ENV)


MUTATIONS = [
    (
        "unverified stops being restrictive -- the common case, and the dangerous one",
        "\tcase tierUnverified:",
        "\tcase tierUnverified:\n\t\treturn Policy{Upload: UploadAllowed, Tier: in.Tier, Reason: \"ok\"}",
        "TestOnlyAnAssertionPermitsSeeding",
    ),
    (
        "an unrecognised tier becomes permissive",
        "\tdefault:\n\t\t// An unrecognised tier: a value from a newer core, a hand-edited row, a",
        "\tcase \"\", \"future_tier\":\n\t\treturn Policy{Upload: UploadAllowed, Tier: in.Tier, Reason: \"anything goes\"}\n\tdefault:\n\t\t// An unrecognised tier: a value from a newer core, a hand-edited row, a",
        "TestAnUnrecognisedTierIsRestrictive",
    ),
    (
        "the operator's setting stops being an outer bound -- it becomes a force",
        "\tif !in.OperatorAllowedSeed {",
        "\tif false {",
        "TestTheOperatorCannotWidenThePolicy",
    ),
    (
        "the operator's setting is the ONLY thing that decides",
        "\tswitch in.Tier {\n\tcase tierSelfPublished, tierPerformerClaimed, tierThirdPartyPermitted:",
        "\tif in.OperatorAllowedSeed {\n\t\treturn Policy{Upload: UploadAllowed, Tier: in.Tier, Reason: \"enabled\"}\n\t}\n\tswitch in.Tier {\n\tcase tierSelfPublished, tierPerformerClaimed, tierThirdPartyPermitted:",
        "TestTheOperatorCannotWidenThePolicy",
    ),
    (
        "the operator switch stops being the outer bound, so the reason names the tier",
        '\t\t\tReason: "seeding is switched off for this downloader, so no chunk " +',
        '\t\t\tReason: "the object is " + in.Tier + ", so no chunk " +',
        "TestTheOperatorSwitchIsTheOuterBound",
    ),
    # NOT `case tierQuarantined:` -- dropping tierDenied from the case sends it
    # to `default`, which is ALSO restrictive, so the mutation is observably a
    # no-op and the test correctly does not care. The real attack is MOVING it,
    # which is what a future "let people deny a torrent they've already fetched"
    # change would do. Both tiers are moved in a single edit: adding to the
    # permissive case while leaving the restrictive one in place is a duplicate
    # case and does not compile.
    # The `case tierDenied,` form is a compile error, not a mutation.
    #
    # Three earlier attempts: adding the tier to the permissive case's list
    # (leaves it in both cases, Go rejects it), inserting a new case ahead of
    # the restrictive one (same duplicate), and flipping the restricted
    # branch's return value -- which is the one that COMPILES, and is the
    # shape the bug would actually take: someone editing the branch below
    # without noticing it is the restrictive one.
    (
        "the restricted branch starts permitting uploads (denied, quarantined)",
        "\tcase tierQuarantined, tierDenied:\n\t\t// Unreachable through the consent gate, which refuses both before a\n"
        "\t\t// transfer starts. Reachable if something bypasses the gate, and the\n"
        "\t\t// answer here is the restrictive one so a bypass does not also become a\n"
        "\t\t// publish.\n"
        "\t\treturn Policy{\n\t\t\tUpload: UploadForbidden,",
        "\tcase tierQuarantined, tierDenied:\n\t\t// Unreachable through the consent gate, which refuses both before a\n"
        "\t\t// transfer starts. Reachable if something bypasses the gate, and the\n"
        "\t\t// answer here is the restrictive one so a bypass does not also become a\n"
        "\t\t// publish.\n"
        "\t\treturn Policy{\n\t\t\tUpload: UploadAllowed,",
        "TestOnlyAnAssertionPermitsSeeding",
    ),
    (
        "the fail-closed default starts permitting uploads",
        "\t\tUpload: UploadForbidden,\n\t\tTier:   tier,\n\t\tReason: \"no seeding decision was made for this torrent, so no chunk is \" +",
        "\t\tUpload: UploadAllowed,\n\t\tTier:   tier,\n\t\tReason: \"no seeding decision was made for this torrent, so no chunk is \" +",
        "TestNoDecisionMeansNoUpload",
    ),
    (
        "a decision stops recording the tier it was made at",
        "\t\t\tTier:   in.Tier,\n\t\t\tReason: \"the object is unverified, which means nobody has asserted \" +",
        "\t\t\tTier:   \"\",\n\t\t\tReason: \"the object is unverified, which means nobody has asserted \" +",
        "TestEveryDecisionCarriesATier",
    ),
    (
        # The first version of this emptied only the FIRST fragment of the
        # concatenated string, and the test greps for a phrase that lives in a
        # LATER one -- so it passed with the reason mangled. That is a test that
        # checks a substring rather than a sentence, and the mutation is what
        # showed it. Now the whole reason goes.
        "a refusal stops carrying a reason",
        "\t\t\tReason: \"the object's tier \" + tierOrUnknown(in.Tier) + \" is not one \" +",
        "\t\t\tReason: \"\" +",
        "TestEveryRefusalCarriesAReason",
    ),
    (
        "the permissive reason is reduced to a word",
        "\t\t\tReason: \"the object is \" + in.Tier + \", which is an assertion by a \" +\n"
        "\t\t\t\t\"party with standing that redistribution is covered. Fetching is a \" +\n"
        "\t\t\t\t\"read; uploading is a write to a library the operator never sees, \" +\n"
        "\t\t\t\t\"so this needs the assertion rather than merely the absence of a \" +\n"
        "\t\t\t\t\"refusal\",",
        "\t\t\tReason: in.Tier + \" is fine\",",
        "TestEveryRefusalCarriesAReason",
    ),
    (
        "the tier strings drift from core's (renamed in this file only)",
        '\ttierSelfPublished       = "self_published"',
        '\ttierSelfPublished       = "selfpublished"',
        "TestTheTierStringsMatchTheCore",
    ),
    # Deleting the constant does not compile -- every reference becomes
    # undefined -- so it proves nothing. The compiling drift is a tier this file
    # has and core does not, which is the case the LENGTH comparison exists
    # for; a RENAME (the mutation above) is what a rename in core causes.
    (
        "a tier that core does not have is added to the copied list",
        '\ttierPerformerClaimed    = "performer_claimed"',
        '\ttierPerformerClaimed    = "performer_claimed"\n\ttierInventedLater   = "invented_later"',
        "TestTheTierStringsMatchTheCore",
    ),
    (
        "the copied list claims a second tier core has not added yet",
        '\ttierPerformerClaimed    = "performer_claimed"',
        '\ttierPerformerClaimed    = "performer_claimed"\n\ttierSecondClaim    = "second_claim"',
        "TestTheTierStringsMatchTheCore",
    ),
    (
        "a recognised tier falls through to the unrecognised branch",
        "\tcase tierSelfPublished, tierPerformerClaimed, tierThirdPartyPermitted:",
        "\tcase tierThirdPartyPermitted:",
        "TestThePolicyTableHasNoGaps",
    ),
]


def main():
    # Two harnesses must never run at once: they edit real files and restore
    # them, so a concurrent pair corrupts each other.
    lock = open(LOCK, "w")
    fcntl.flock(lock, fcntl.LOCK_EX)
    try:
        return run_all()
    finally:
        fcntl.flock(lock, fcntl.LOCK_UN)
        lock.close()


def run_all():
    snapshot(SRC)
    original = open(SRC).read()
    results = []

    for name, old, new, expect in MUTATIONS:
        if old not in original:
            results.append((name, "unscored", "the text to mutate is gone, so the "
                                               "mutation is testing nothing"))
            continue

        mutated = original.replace(old, new, 1)
        if mutated == original:
            results.append((name, "no-op", "the edit changed no bytes"))
            continue

        open(SRC, "w").write(mutated)
        try:
            build = run("go build ./internal/policy/")
            if build.returncode != 0:
                out = (build.stdout + build.stderr).strip().splitlines()
                results.append((name, "broken",
                                "does not compile: " + (out[-1] if out else "")[:80]))
                continue

            test = run(f"go test ./internal/policy/ -count=1 -run {expect}")
            if test.returncode != 0:
                results.append((name, "killed", f"{expect} failed"))
            else:
                results.append((name, "survived",
                                f"{expect} passed with the policy permissive"))
        finally:
            open(SRC, "w").write(original)

    for path, want in SNAPSHOTS.items():
        if open(path).read() != want:
            print(f"FATAL: {path} was not restored, so every result below "
                  f"describes a tree that is not the one on disk")
            return 2

    if run("go build ./...").returncode != 0:
        print("FATAL: the plugin does not build after restoring every mutation")
        return 2
    if run("go test ./internal/policy/ -count=1").returncode != 0:
        print("FATAL: the policy package does not pass its own tests after restoring")
        return 2

    width = max(len(n) for n, _, _ in results)
    for name, verdict, detail in results:
        print(f"  {name:<{width}}  {verdict.upper():9} {detail}")

    killed = sum(1 for _, v, _ in results if v == "killed")
    survived = sum(1 for _, v, _ in results if v == "survived")
    broken = sum(1 for _, v, _ in results if v == "broken")
    unscored = sum(1 for _, v, _ in results if v == "unscored")
    noop = sum(1 for _, v, _ in results if v == "no-op")
    print()
    print(f"  {killed} killed, {survived} survived, {broken} broken, "
          f"{unscored} unscored, {noop} no-op")
    return 1 if (survived or broken or unscored or noop) else 0


if __name__ == "__main__":
    sys.exit(main())
