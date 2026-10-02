#!/usr/bin/env python3
"""Mutation gate for the directory: migration 118 and its store.

WHY A COMMITTED SCRIPT
======================

Inherited from the access-policy gate, where all three reasons were measured:

  1. A loop that reports a survival it never MEASURED is the danger. A
     substitution that silently fails to apply reads as SURVIVED, which looks
     like a finding and is not one. Every mutant is verified to have landed by
     re-reading the file, so a survivor is always a real survivor.
  2. The list of mutants that SHOULD exist is recorded, so one dropped from the
     list shows up as a count change rather than a quiet weakening.
  3. Files are restored WITH VERIFICATION, so a failed run cannot leave the tree
     mutated.

WHAT THIS GATE IS FOR
====================

Migration 118's load-bearing content is mostly NEGATIVE -- there is no grant
column, no float column, no free-text state -- so three of these six mutants
attack an ABSENCE. That is the hard case for a mutation gate: a gate proves a
test kills a mutant, and "the mutant adds a column nobody should add" is a
mutation of the schema's shape rather than of its rules.

The three absences are attacked by ADDING the thing that must not be there, so
each is the inverse of the ordinary direction and each is the mutant a reviewer
should ask about first.

RUN
===

    python3 pkg/sqlite/mutate_directory.py

Exit 0 means every mutant was killed. Exit 1 means at least one survived, and
the survivor is named.
"""

from __future__ import annotations

import pathlib
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parents[2]

MIGRATION = REPO / "pkg/sqlite/migrations/118_directory.up.sql"
STORE = REPO / "pkg/sqlite/stashforge_directory.go"

# (name, file, old, new, test that must catch it)
MUTANTS: list[tuple[str, pathlib.Path, str, str, str]] = [
    (
        # THE THREE ABSENCES, each attacked by adding the forbidden thing.
        "grant-path-column-added",
        MIGRATION,
        "  `rejected_at` timestamp,",
        "  `rejected_at` timestamp,\n  `granted_by` integer DEFAULT NULL,",
        "TestTheSchemaHasNoGrantPath -- R048's guarantee is the ABSENCE of a "
        "grant path, and a schema column reintroduces it where nobody looks",
    ),
    (
        "price-becomes-a-float",
        MIGRATION,
        "`amount_minor` integer NOT NULL CHECK (`amount_minor` >= 0)",
        "`amount_minor` real NOT NULL CHECK (`amount_minor` >= 0)",
        "TestPricesAreStoredInMinorUnitsAndNegativeIsRefused -- 4500 minor "
        "units is EUR 45.00 and no float is involved anywhere in the path",
    ),
    (
        # THE FOUR STATES ARE ENUMERATED ONCE, in the branches, BY SIDE EFFECT: each
        # branch names its own state, so the set is closed without a separate
        # IN (...) list. A fifth spelling therefore has to be added AS A BRANCH.
        #
        # NOTE THE HISTORY, because it is the useful part of this file. This mutant
        # originally widened a SECOND, column-level CHECK, and it SURVIVED --
        # correctly, as it turned out. Migration 118 stated the four states in TWO
        # places, so widening one left the other refusing the row: correct and
        # mutated code agreed for every input, and the mutant was unobservable
        # rather than survived. Verified in `sqlite3 :memory:` rather than inferred.
        #
        # The schema now states them once, which is what makes this mutation
        # expressible AND killable. A redundant constraint is not merely untidy: it
        # is a second place for the next change to go, and this gate is the
        # instrument that reports one of your two statements of a rule as dead.
        "fifth-state-as-a-branch",
        MIGRATION,
        "    OR (`state` = 'pending_confirmation' AND `confirmed_by` IS NULL)",
        "    OR (`state` = 'pending_confirmation' AND `confirmed_by` IS NULL)\n"
        "    OR (`state` = 'confirmed')",
        "TestTheSchemaEnumeratesExactlyFourStates -- 'confirmed' is not a state. "
        "Free text would let it coexist with 'verified' as two spellings of one idea",
    ),
    (
        # The other direction of the same enumeration: a branch that stops requiring
        # what its state PROMISES lets a `verified` badge exist with nobody behind it,
        # which is exactly the badge §6a.4 exists to keep earned.
        "verified-without-a-confirmer",
        MIGRATION,
        "    (`state` = 'verified' AND `confirmed_by` IS NOT NULL AND `decided_at` IS NOT NULL\n"
        "     AND `rejected_at` IS NULL)",
        "    (`state` = 'verified' AND `rejected_at` IS NULL)",
        "TestTheSchemaEnumeratesExactlyFourStates -- a VERIFIED claim has a "
        "confirmer and a decision time; without both it is a badge nobody earned",
    ),
    (
        # THE `rejected_at IS NULL` THAT THE FIRST TEST RUN CAUGHT.
        #
        # This is the clause that was MISSING from the verified branch, and without
        # it the one constraint meant to make a rejection terminal permitted
        # rejected -> verified by a bare UPDATE: a resurrected row has a confirmer and
        # a decision time, so the branch was satisfied anyway. Kept as a mutant
        # because a fix nobody can re-break is a comment.
        "verified-ignores-rejection-history",
        MIGRATION,
        "    (`state` = 'verified' AND `confirmed_by` IS NOT NULL AND `decided_at` IS NOT NULL\n"
        "     AND `rejected_at` IS NULL)",
        "    (`state` = 'verified' AND `confirmed_by` IS NOT NULL AND `decided_at` IS NOT NULL)",
        "TestRejectedIsTerminalAndCannotBeResurrected -- history that can be "
        "relabelled is not history, and a terminal state must name what it is "
        "terminal with respect to",
    ),
    (
        # §6a.4's whole mechanism, in the SCHEMA rather than only in Go.
        "claimer-neq-confirmer-dropped",
        MIGRATION,
        "CHECK (`confirmed_by` IS NULL OR `confirmed_by` <> `claimed_by`)",
        "CHECK (`confirmed_by` IS NULL OR `confirmed_by` IS NOT NULL)",
        "TestConfirmCannotBeTheClaimer -- this is the layer a direct SQL session "
        "cannot get past, and §6a.4's content is that it cannot",
    ),
    (
        # The STORE's pending guard. Its absence was found by THIS gate reporting a
        # survivor: the tests asserted directory.ErrNotPending only against the
        # DOMAIN, which refuses to BUILD a decision for a decided claim, so removing
        # the store's own `AND state = pending_confirmation` made a verified claim
        # re-confirmable and no test noticed. A domain check that can only be
        # bypassed by not calling the domain is a documentation, not a guard.
        "confirm-overwrites-a-decided-claim",
        STORE,
        "\" WHERE \"+claimsEntityTypeCol+\" = ? AND \"+claimsEntityIDCol+\" = ?\"+\n"
        "\t\t\" AND \"+claimsStateCol+\" = ?\",\n"
        "\t\tstring(state), confirmer, at, c.EntityType, c.EntityID, string(directory.BadgePending))",
        "\" WHERE \"+claimsEntityTypeCol+\" = ? AND \"+claimsEntityIDCol+\" = ?\",\n"
        "\t\tstring(state), confirmer, at, c.EntityType, c.EntityID)",
        "TestADecidedClaimCannotBeDecidedAgain -- the store's own guard. Without it "
        "a verified claim is re-confirmable by anyone with a user id",
    ),
]

TEST_RUNNER = ["go", "test", "-tags", "integration", "./pkg/sqlite/", "-count=1", "-run"]

# ONE PATTERN, USED THREE TIMES (baseline and both mutant runs).
#
# The access-policy gate learned this the hard way: the pattern was written out in
# each place and the copies drifted, so two mutants were reported SURVIVED while
# the suite they ran could not have contained the test that kills them. A gate that
# cannot name the tests it runs has uninterpretable survivors -- "survived" and "was
# never run" are indistinguishable from the output.
#
# EVERY test in this step's file is listed here, and
# TestTheDirectoryGateNamesEveryDirectoryTest asserts that by EXTRACTING the names from the
# test file rather than comparing against a second copy of them. A literal in two
# files is the same drift in a different hat; extracting it is the fix.
SUITE_PATTERN = (
    "TestTheSchemaHasNoGrantPath|TestConfirmCannotBeTheClaimer|"
    "TestRejectedIsTerminalAndCannotBeResurrected|"
    "TestTheSchemaEnumeratesExactlyFourStates|"
    "TestAClaimIsAttributedAndBornPending|"
    "TestPricesAreStoredInMinorUnitsAndNegativeIsRefused|"
    "TestTheNetworkProfileAssemblesAndSortsDeterministically|"
    "TestPendingClaimsIsTheReviewQueue|TestDirectoryIsImportedByNonTestCode|"
    "TestADecidedClaimCannotBeDecidedAgain|TestTheDirectoryGateNamesEveryDirectoryTest"
)

# The test file whose function names must all appear in SUITE_PATTERN.
TEST_FILE = REPO / "pkg/sqlite/stashforge_directory_test.go"

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
    print("directory mutation gate: %d mutants\n" % len(MUTANTS))

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
            print("  %-34s SKIPPED -- the pattern is not in the file." % name)
            print("     A mutant that cannot be APPLIED is not a test of anything;")
            print("     reporting it as survived would be the failure mode above.")
            survivors.append((name, "PATTERN NOT FOUND"))
            path.write_text(original)
            continue

        path.write_text(original.replace(old, new, 1))

        # Verify the substitution actually landed.
        if new not in path.read_text():
            print("  %-34s ERROR -- applied but not found afterwards." % name)
            survivors.append((name, "SUBSTITUTION DID NOT LAND"))
            path.write_text(original)
            continue

        killed = not run_tests(SUITE_PATTERN)
        path.write_text(original)

        # RESTORE VERIFICATION, not just restoration.
        if path.read_text() != original:
            print("  %-34s ERROR -- restore failed, tree left mutated!" % name)
            survivors.append((name, "RESTORE FAILED"))
            continue

        if killed:
            print("  %-34s killed  <- %s" % (name, catcher.split(" -- ")[0]))
        else:
            print("  %-34s SURVIVED" % name)
            print("     expected catcher: %s" % catcher)
            survivors.append((name, catcher))

    print()
    if survivors:
        print("SURVIVORS: %d of %d" % (len(survivors), len(MUTANTS)))
        for name, why in survivors:
            print("  %-34s %s" % (name, why))
        return 1

    print("all %d mutants killed." % len(MUTANTS))
    return 0


if __name__ == "__main__":
    sys.exit(main())
