#!/usr/bin/env python3
"""Mutation gate for discovery: migration 119, the store, and the call site.

WHY A COMMITTED SCRIPT
======================

Inherited, with all three reasons measured on this tree:

  1. A loop that reports a survival it never MEASURED is the danger. Every mutant
     is verified to have landed by re-reading the file, so a survivor is always a
     real survivor.
  2. The list of mutants that SHOULD exist is recorded, so one dropped shows up as
     a count change rather than a quiet weakening.
  3. Files are restored WITH VERIFICATION.

WHAT THIS GATE IS FOR
====================

Migration 119's central claim is an ABSENCE -- a board has no rank column -- and
Compose's central claim is a REFUSAL -- the feed groups by surface and never
interleaves. Both are the kind of thing a test suite passes through without ever
distinguishing, because in both cases the correct behaviour and a plausible wrong
one agree on every input a test happens to use.

So five of the six mutants are inversions of the design:

  - add the rank column the schema refuses to have
  - read the ordering DESC
  - drop UNIQUE (board_id, entity_id), so a list can show a studio twice
  - make Compose interleave, which is the one thing it must never do
  - make a rejected... (n/a here) -- see the list

RUN
===

    python3 pkg/sqlite/mutate_discovery.py
"""

from __future__ import annotations

import pathlib
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parents[2]

MIGRATION = REPO / "pkg/sqlite/migrations/119_discovery.up.sql"
STORE = REPO / "pkg/sqlite/stashforge_discovery.go"
FEED = REPO / "internal/discovery/feed.go"

MUTANTS: list[tuple[str, pathlib.Path, str, str, str]] = [
    (
        # THE ABSENCE, attacked by adding the thing that must not be there.
        "rank-column-added-to-boards",
        MIGRATION,
        "  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,",
        "  `rank` integer,\n  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,",
        "TestTheBoardsTableHasNoRankColumn -- ground rule 4 forbids a stored "
        "counter, and the failure is silent: adding one keeps every test green "
        "while the board has become a ranking",
    ),
    (
        # THE ORDER, inverted.
        "ordering-read-backwards",
        STORE,
        '" WHERE "+itemsBoardCol+" = ? ORDER BY "+itemsPosCol+" ASC"',
        '" WHERE "+itemsBoardCol+" = ? ORDER BY "+itemsPosCol+" DESC"',
        "TestABoardPersistsInAuthorOrder -- an ordering that reads back reversed is "
        "a board whose stored order is not what it shows",
    ),
    (
        # THE DUPLICATE, removed.
        "same-entity-twice-allowed",
        MIGRATION,
        "  PRIMARY KEY (`board_id`, `position`),\n  UNIQUE (`board_id`, `entity_id`),",
        "  PRIMARY KEY (`board_id`, `position`),",
        "TestDuplicatePositionsAndRepeatedEntitiesAreRefused -- a list showing the "
        "same studio at positions 0 and 2 is a bug a reader reports",
    ),
    (
        # THE FEED'S WHOLE DESIGN, and the one most worth a mutant.
        # AIMED AT internal/discovery.Compose, NOT AT THE STORE.
        #
        # The first version of this mutant renamed a rank in ComposeFeed, which only
        # ever builds ONE surface -- so "interleaving across surfaces" was not merely
        # untested there, it was UNOBSERVABLE. A gate that points a mutant at a place
        # the failure cannot occur reports a survivor and means nothing by it.
        #
        # Compose is where several surfaces actually meet, and
        # TestTheFeedDoesNotInterleaveSurfaces is the test that can see it.
        "feed-interleaves-surfaces",
        FEED,
        "		f.Sections = append(f.Sections, FeedSection{Surface: s, Items: got})",
        """		for _, it := range got {
			f.Sections = append(f.Sections, FeedSection{Surface: s, Items: []FeedItem{it}})
		}""",
        "TestTheFeedDoesNotInterleaveSurfaces (internal/discovery) -- an ordering "
        "is only meaningful within the surface that produced it, so ranking a quest "
        "against a recommendation compares two things with no common scale",
    ),
    (
        # THE MISSING-BOARD ANSWER, made to look like an empty one.
        "absent-board-reads-as-empty",
        STORE,
        '	return discovery.StoredBoard{}, fmt.Errorf("%w: %s", discovery.ErrNoBoard, boardID)',
        '		return discovery.StoredBoard{}, nil',
        "TestBoardForIsAbsentRatherThanEmpty -- 'no such board' is a 404 and 'the "
        "board is empty' is a page, and conflating them renders one as the other",
    ),
    (
        # THE DENSE POSITIONS, relaxed to gaps.
        "negative-positions-allowed",
        MIGRATION,
        "  `position` integer NOT NULL CHECK (`position` >= 0),",
        "  `position` integer NOT NULL,",
        "TestABoardPersistsInAuthorOrder -- positions are 0-BASED, and a negative "
        "one is an ordering that starts before it begins",
    ),
]

# TWO PACKAGES, because the mutants do not all live in one. Compose is in
# internal/discovery and its catcher is a test there, so a runner scoped to
# ./pkg/sqlite/ could never apply that mutant -- which is exactly what happened on
# the first run, and it reported a survivor.
TEST_RUNNER = ["go", "test", "-tags", "integration", "./pkg/sqlite/", "./internal/discovery/",
               "-count=1", "-run"]

# ONE PATTERN, USED THREE TIMES (baseline and both mutant runs). Written out once
# here and asserted by TestTheDiscoveryGateNamesEveryDiscoveryTest, which EXTRACTS
# it from this file rather than comparing a second copy.
SUITE_PATTERN = (
    "TestTheBoardsTableHasNoRankColumn|TestABoardPersistsInAuthorOrder|"
    "TestAnEditReplacesRatherThanMerges|"
    "TestDuplicatePositionsAndRepeatedEntitiesAreRefused|"
    "TestBoardForIsAbsentRatherThanEmpty|TestBoardsByAuthorAndContaining|"
    "TestThePositionsAndEntityConstraintsHoldAgainstRawSQL|"
    "TestTheFeedComposesFromStoredBoards|"
    "TestEveryFeedSymbolIsReferencedOutsideItsPackage|"
    "TestTheDiscoveryGateNamesEveryDiscoveryTest|TestTheFeedDoesNotInterleaveSurfaces"
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
    print("discovery mutation gate: %d mutants\n" % len(MUTANTS))

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

        if new not in path.read_text():
            print("  %-32s ERROR -- applied but not found afterwards." % name)
            survivors.append((name, "SUBSTITUTION DID NOT LAND"))
            path.write_text(original)
            continue

        killed = not run_tests(SUITE_PATTERN)
        path.write_text(original)

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
