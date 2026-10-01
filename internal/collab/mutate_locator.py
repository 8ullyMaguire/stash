#!/usr/bin/env python3
"""Mutation-check the locator consent gate (M5 step 5.0a).

A gate that cannot refuse is not a gate, and the mutations here are all
"refusal quietly became permission". They are the exact shape §7.1 exists to
prevent: a plugin that requests, core that decides, and a core that decides
"yes" because one line of a switch was deleted.

  killed    the named test failed
  survived  the tests passed -- a HOLE
  broken    does not compile -- NOT a kill
  unscored  the edit did not apply -- not a result

Run from the repo root:  python3 internal/collab/mutate_locator.py
"""

import fcntl
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, "..", ".."))
ENV = {**os.environ, "GOFLAGS": "-mod=mod"}

GATE = os.path.join(HERE, "locator.go")

MUTATIONS = [
    (
        "quarantined stops refusing storage",
        "\tTierQuarantined: \"a quarantined object holds material but does not act on it, \" +\n"
        "\t\t\"so a locator stored against one is a locator waiting for the quarantine \" +\n"
        "\t\t\"to be lifted without anything deciding to lift it\",\n",
        "\t// TierQuarantined removed\n",
        "TestStorageIsRefusedAtQuarantinedAndDenied",
    ),
    (
        "denied stops refusing storage",
        '\tTierDenied: "denial is terminal, so storing a locator against it would leave a " +\n'
        '\t\t"pointer to material that must not exist",\n',
        "\t// TierDenied removed\n",
        "TestStorageIsRefusedAtQuarantinedAndDenied",
    ),
    (
        "the acting gate drops its tier requirement and permits everything",
        "\tif tier != TierThirdPartyPermitted {",
        "\tif false {",
        "TestActingNeedsThirdPartyPermitted",
    ),
    (
        "the acting gate trusts the denormalised tier instead of re-reading",
        "\ttier, found, err := s.CurrentTier(ctx, objectID)",
        "\ttier, found, err := TierThirdPartyPermitted, true, error(nil)",
        "TestTheActGateRereadsTheTierRatherThanTrustingTheLocator",
    ),
    (
        "an unreadable tier fails OPEN",
        "\t\treturn ActDecision{}, fmt.Errorf(\"re-reading the object's tier at the \"+\n"
        "\t\t\t\"moment of the act: %w\", err)",
        "\t\treturn ActDecision{Permitted: true}, nil",
        "TestAnUnreadableTierIsAnErrorRatherThanAGrant",
    ),
    (
        "a missing object is treated as permitted",
        "\tif !found {\n",
        "\tif !found && found {\n",
        "TestActingOnAnObjectThatNoLongerExists",
    ),
    (
        "the gate stores the locator even after refusing it",
        "\tif refused, reason := l.Tier.IsStorageRefused(); refused {\n"
        "\t\treturn refuse(l.Tier, reason), nil\n\t}",
        "\tif refused, reason := l.Tier.IsStorageRefused(); refused {\n"
        "\t\t_ = reason\n\t}",
        "TestStorageIsRefusedAtQuarantinedAndDenied",
    ),
    (
        "the gate accepts a locator naming no object",
        "\tif objectID == 0 {",
        "\tif false {",
        "TestAStoredLocatorNeedsAnObject",
    ),
    (
        "the gate lets the plugin choose the object",
        "\tl.ObjectID = objectID",
        "\tif l.ObjectID == 0 {\n\t\tl.ObjectID = objectID\n\t}",
        "TestTheObjectIDIsCoreSNot",
    ),
    (
        "an empty locator is stored",
        '\tif strings.TrimSpace(l.Value) == "" {',
        '\tif false {',
        "TestAnEmptyLocatorIsRefusedRatherThanStored",
    ),
    (
        "a store failure becomes a refusal instead of an error",
        '\t\treturn LocatorDecision{}, fmt.Errorf("storing the locator: %w", err)',
        "\t\treturn refuse(l.Tier, err.Error()), nil",
        "TestAStoreFailureIsAnErrorNotARefusal",
    ),
    (
        "a grant carries a reason",
        "\treturn LocatorDecision{Allowed: true, Checked: checked}",
        '\treturn LocatorDecision{Allowed: true, Reason: "ok", Checked: checked}',
        "TestStorageIsPermittedAtTheOtherFourTiers",
    ),
    (
        "denial invalidates instead of destroying",
        "\tif refused, reason := newTier.IsStorageRefused(); refused {\n",
        "\tif refused, reason := newTier.IsStorageRefused(); refused && reason == \"x\" {\n\t\t_ = reason\n",
        "TestDenialDestroysAndOtherChangesInvalidate",
    ),
    (
        "an unknown tier is defaulted to the permissive one",
        "\treturn \"\", fmt.Errorf(\"%q is not a locator tier. The known tiers are %s\",\n"
        "\t\ts, strings.Join(LocatorTierNames(), \", \"))",
        "\treturn TierUnverified, nil",
        "TestAnUnknownTierIsRefusedRatherThanDefaulted",
    ),
]


def snapshot(path):
    """Record a file's contents so the restore can be PROVEN, not assumed."""
    SNAPSHOTS[os.path.abspath(path)] = open(path).read()


SNAPSHOTS = {}


def run(cmd):
    return subprocess.run(cmd, shell=True, cwd=ROOT, capture_output=True,
                          text=True, env=ENV)


def main():
    # # LOCK THE REPO BEFORE TOUCHING IT
    #
    # These harnesses edit real files and restore them, so two of them running
    # at once corrupt each other: mutate_seam.py rewrote the plugin's exec entry
    # to /usr/bin/nothing while mutate_rpc.py was reading the same directory, and
    # the result was a test failure that looked like a code bug and a manifest
    # that stayed broken.
    #
    # The symptom is the dangerous kind -- a red in an unrelated test, with the
    # cause in a different harness's finally-block. An exclusive lock on one
    # shared file makes the second harness wait rather than interleave.
    lock_path = os.path.join(ROOT, "plugins", "p2pdownloader", ".mutation-harness.lock")
    lock = open(lock_path, "w")
    fcntl.flock(lock, fcntl.LOCK_EX)
    try:
        return run_all()
    finally:
        fcntl.flock(lock, fcntl.LOCK_UN)
        lock.close()


def run_all():
    original = open(GATE).read()
    results = []

    for name, old, new, expect in MUTATIONS:
        if old not in original:
            results.append((name, "unscored", "the text to mutate is gone, so the "
                                               "mutation is testing nothing"))
            continue

        open(GATE, "w").write(original.replace(old, new, 1))
        try:
            build = run("go build ./internal/collab/")
            if build.returncode != 0:
                out = (build.stdout + build.stderr)
                # An unused import after a deletion is a compile error that says
                # nothing about the gate, so it is scored as broken.
                results.append((name, "broken",
                                "does not compile: " + out.strip().splitlines()[-1][:80]))
                continue

            test = run(f"go test ./internal/collab/ -count=1 -run {expect}")
            if test.returncode != 0:
                results.append((name, "killed", f"{expect} failed"))
            else:
                results.append((name, "survived",
                                f"{expect} passed with the defect in place"))
        finally:
            open(GATE, "w").write(original)

    for path, want in SNAPSHOTS.items():
        if open(path).read() != want:
            print(f"FATAL: {path} was not restored, so every result below "
                  f"describes a tree that is not the one on disk")
            return 2

    if run("go build ./...").returncode != 0:
        print("FATAL: the tree does not build after restoring every mutation")
        return 2
    if run("go test ./internal/collab/ -count=1").returncode != 0:
        print("FATAL: the package does not pass its own tests after restoring")
        return 2

    width = max(len(n) for n, _, _ in results)
    for name, verdict, detail in results:
        print(f"  {name:<{width}}  {verdict.upper():9} {detail}")

    killed = sum(1 for _, v, _ in results if v == "killed")
    survived = sum(1 for _, v, _ in results if v == "survived")
    broken = sum(1 for _, v, _ in results if v == "broken")
    unscored = sum(1 for _, v, _ in results if v == "unscored")
    print()
    print(f"  {killed} killed, {survived} survived, {broken} broken, {unscored} unscored")
    return 1 if (survived or broken or unscored) else 0


if __name__ == "__main__":
    sys.exit(main())
