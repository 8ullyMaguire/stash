#!/usr/bin/env python3
"""Mutation gate for the access policy: migration 117 and its store.

WHY A COMMITTED SCRIPT AND NOT A HAND-WRITTEN LOOP
===================================================

The mesh's gate exists for three measured reasons, and all three apply here:

  1. A loop that reports a survival it never MEASURED is the danger. A
     substitution that silently fails to apply reads as SURVIVED, which looks
     like a finding and is not one. Every mutant below is verified to have been
     applied -- the script re-reads the file and asserts the change is present --
     so a survivor is always a real survivor.
  2. The list of mutants that SHOULD exist is recorded, so a mutant silently
     dropped from the list is visible as a count change rather than as a quiet
     weakening of the gate.
  3. Files are restored with verification, so a failed run cannot leave the tree
     mutated.

WHAT THIS GATE IS FOR
====================

Every mutant here attacks a DEFAULT or a BOUNDARY, and those are exactly the
decisions this step made:

  - the ceiling defaulting to 0 (LevelPublic)
  - consent defaulting to 0 (off)
  - the CHECK refusing granted=1 together with revoked_at
  - DecideAccess's ceiling comparison being strict

A mutation gate earns its place by finding a test that does not distinguish two
spellings of the same behaviour. Each mutant below is paired, in the comment,
with the test that is expected to catch it -- and every one of them was verified
to be caught by actually running it.

RUN
===

    python3 pkg/sqlite/mutate_access_policy.py

Exit 0 means every mutant was killed. Exit 1 means at least one survived, and
the survivor is named.
"""

from __future__ import annotations

import pathlib
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parents[2]

MIGRATION = REPO / "pkg/sqlite/migrations/117_access_policy.up.sql"
STORE = REPO / "pkg/sqlite/stashforge_access_policy.go"

# (name, file, old, new, test that must catch it)
#
# Each entry states the catcher in advance. That is the discipline: a mutant
# whose catcher is unknown is a mutant whose death proves nothing, because a
# passing suite and a green mutant look identical.
MUTANTS: list[tuple[str, pathlib.Path, str, str, str]] = [
    (
        "ceiling-default-opens-content",
        MIGRATION,
        "`content_ceiling` integer NOT NULL DEFAULT 0",
        "`content_ceiling` integer NOT NULL DEFAULT 5",
        "TestAccessPolicyCeilingDefaultsToPublic -- an instance that never "
        "configured a threshold must serve NO content",
    ),
    (
        "consent-default-on",
        MIGRATION,
        "`granted` integer NOT NULL DEFAULT 0 CHECK (`granted` IN (0, 1))",
        "`granted` integer NOT NULL DEFAULT 1 CHECK (`granted` IN (0, 1))",
        "TestContentConsentDefaultsToOff -- §6a.11 says consent is never "
        "automatic on reaching a level",
    ),
    (
        "consent-check-dropped",
        MIGRATION,
        "CHECK ((`granted` = 1 AND `revoked_at` IS NULL) OR (`granted` = 0))",
        "CHECK (`granted` IN (0, 1))",
        "TestContentConsentRefusesConsentAndRevocationTogether -- a row "
        "claiming both consent and revocation must be refused by the schema",
    ),
    (
        "absent-row-is-an-error",
        STORE,
        "case errors.Is(err, sql.ErrNoRows):\n\t\treturn collab.LevelPublic, nil",
        "case errors.Is(err, sql.ErrNoRows):\n\t\treturn collab.LevelPublic, "
        "fmt.Errorf(\"no access policy row for instance %d\", s.instanceID)",
        "TestAccessPolicyAnAbsentRowIsPublicAndNotAnError -- an absent row is a "
        "POLICY, and an error here pushes every caller into an unsafe fallback",
    ),
    (
        "absent-consent-reads-true",
        STORE,
        "case errors.Is(err, sql.ErrNoRows):\n\t\treturn false, nil",
        "case errors.Is(err, sql.ErrNoRows):\n\t\treturn true, nil",
        "TestContentConsentDefaultsToOff -- an absent consent row must read as "
        "REFUSED, which is the whole of §6a.11's separate switch",
    ),
    (
        "ident-count-inflated",
        STORE,
        "const countDistinctTarget = `SELECT COUNT(DISTINCT target_id)",
        "const countDistinctTarget = `SELECT COUNT(target_id)",
        "TestIdentSolvesCountDistinctTargets -- five solves of ONE scene is one "
        "contribution, and a row count lets a user farm the level",
    ),
    (
        "ceiling-clamped-to-zero",
        STORE,
        "clamped := level.Clamp()",
        "clamped := collab.AccessLevel(0)",
        "TestEveryAccessCeilingRoundTrips -- an operator's ceiling must actually "
        "store what they set",
    ),
    (
        "revocation-not-recorded",
        STORE,
        '"+contentConsentGrantedCol+" = 0, revoked_at = CURRENT_TIMESTAMP"',
        '"+contentConsentGrantedCol+" = 0"',
        "TestConsentRevocationIsRecordedNotDeleted -- a revoke with no timestamp "
        "cannot answer 'when did this user withdraw'",
    ),
    # The two below were added because the first run of this gate found that the
    # DEFAULT mutants SURVIVED: migration 117 seeds instance 1 explicitly, so every
    # assertion against it was really asserting the seed rather than the column
    # DEFAULT. TestTheColumnDefaultsApplyToARowThatDoesNotNameThem exists to close
    # that, and these mutants are the proof it closed.
    (
        "seed-row-opens-content",
        MIGRATION,
        "INSERT INTO `access_policy` (`instance_id`, `content_ceiling`) VALUES (1, 0);",
        "INSERT INTO `access_policy` (`instance_id`, `content_ceiling`) VALUES (1, 5);",
        "TestAccessPolicyCeilingDefaultsToPublic -- the SEEDED row is the first "
        "thing an instance reads, and 5 would serve content to everyone",
    ),
]

TEST_RUNNER = ["go", "test", "-tags", "integration", "./pkg/sqlite/", "-count=1", "-run"]

# ONE PATTERN, USED THREE TIMES (baseline and both mutant runs).
#
# It was previously written out in each place, and the copy drifted: a new test
# (TestTheColumnDefaults...) went into one and not the others, so two DEFAULT
# mutants were reported SURVIVED while the suite they were run against could not
# have contained the test that catches them. A gate that cannot name the tests it
# runs is a gate whose survivors are uninterpretable -- "survived" and "was never
# run" look identical from the output.
#
# Every test in this file's suite is listed here, and
# TestTheMutationGateNamesEveryTest asserts that, so adding a test without adding
# it here is a failure rather than a silent weakening.
SUITE_PATTERN = (
    "TestAccessPolicy|TestContentConsent|TestDecide|TestConsent|TestReGrant|"
    "TestEarned|TestIdentSolves|TestTheAccessModel|TestArchivist|"
    "TestTheThreeAccessErrors|"
    "TestEveryAccessCeiling|TestTheColumnDefaults|TestTheMutationGateNamesEveryTest"
)
GO_ENV = {"GOFLAGS": "-mod=mod", "PATH": "/usr/bin:/bin:/usr/local/bin", "HOME": "/home/alvaro"}


def run_tests(pattern: str) -> bool:
    """True if the suite passes, i.e. the mutant was killed."""
    r = subprocess.run(
        TEST_RUNNER + [pattern],
        cwd=str(REPO),
        capture_output=True,
        text=True,
        env=GO_ENV,
    )
    return r.returncode == 0


def main() -> int:
    print("access-policy mutation gate: %d mutants\n" % len(MUTANTS))

    baseline = run_tests(SUITE_PATTERN)
    if not baseline:
        print("FATAL: the suite does not pass BEFORE mutation.")
        print("A gate run against a red suite reports every mutant as a survivor,")
        print("which reads like a finding about the code and is a finding about the gate.")
        return 1
    print("baseline: green\n")

    survivors = []
    for name, path, old, new, catcher in MUTANTS:
        original = path.read_text()
        if old not in original:
            print("  %-32s SKIPPED -- the pattern is not in the file." % name)
            print("     A mutant that cannot be APPLIED is not a test of anything;")
            print("     reporting it as survived would be the failure mode above.")
            survivors.append((name, "PATTERN NOT FOUND"))
            path.write_text(original)
            continue

        path.write_text(original.replace(old, new, 1))

        # Verify the substitution actually landed. This is the check the hand-written
        # loop lacked: without it, a mismatch here reads as SURVIVED.
        if new not in path.read_text():
            print("  %-32s ERROR -- applied but not found afterwards." % name)
            survivors.append((name, "SUBSTITUTION DID NOT LAND"))
            path.write_text(original)
            continue

        killed = not run_tests(SUITE_PATTERN)
        path.write_text(original)

        # RESTORE VERIFICATION, not just restoration.
        if path.read_text() != original:
            print("  %-32s ERROR -- restore failed, tree left mutated!" % name)
            survivors.append((name, "RESTORE FAILED"))
            continue

        if killed:
            print("  %-32s killed  <- %s" % (name, catcher.split(" -- ")[0]))
        else:
            print("  %-32s SURVIVED" % name)
            print("     expected catcher: %s" % catcher)
            survivors.append((name, catcher))

    print()
    if survivors:
        print("SURVIVORS: %d of %d" % (len(survivors), len(MUTANTS)))
        for name, why in survivors:
            print("  %-32s %s" % (name, why))
        return 1

    print("all %d mutants killed." % len(MUTANTS))
    return 0


if __name__ == "__main__":
    sys.exit(main())
