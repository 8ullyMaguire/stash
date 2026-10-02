#!/usr/bin/env python3
"""Mutation gate for relayconsent: R089, somebody pays for every relayed byte.

WHY A GATE FOR A FILE WITH NO CALLER
====================================

Because R089's claim is about ORDER and about a check that happens BEFORE anything
moves. Both are invisible to a passing test suite in exactly the way that matters: a
cap checked after the transfer still "passes" every functional test, and a check
performed in the wrong order still refuses the right things in the right tests. So
the gate's job is to move the ordering and move the check, and confirm something
notices.

EVERY MUTANT HERE IS AN INVERSION OF A STATED DECISION. None of them is a typo.

RUN
===

    cd plugins/p2pdownloader && python3 internal/relayconsent/mutate_relayconsent.py
"""

from __future__ import annotations

import pathlib
import subprocess
import sys

HERE = pathlib.Path(__file__).resolve().parent
PLUGIN = HERE.parents[1]
SRC = HERE / "relayconsent.go"

# (name, pattern to find, replacement, which test is meant to catch it)
MUTANTS: list[tuple[str, str, str, str]] = [
    (
        # THE ORDER, DONE AS A SWAP. Kill is moved BELOW consent, so a node with no
        # consent AND a thrown switch reports ErrNotConsented -- and the operator is
        # told to grant consent, which is advice that does nothing while the switch is
        # thrown.
        #
        # IT HAS TO BE A SWAP, and the first version got this wrong in an instructive
        # way: it appended a second `if c.killed` below the consent check, leaving the
        # original one in place above. That mutant is a NO-OP -- the original check
        # still fires first, the appended one is unreachable, and the suite is
        # correctly green. A mutant that cannot change behaviour cannot be killed by
        # any test, so "survived" here would have been a statement about the gate
        # rather than about the code.
        "kill-checked-after-consent",
        "	// KILL FIRST. If the operator is stopping the node, the fact that consent was\n"
        "	// never granted is a detail they do not need in the way out.\n"
        "	if c.killed {\n		return refuse(ErrKilled)\n	}\n"
        "	if !c.granted {\n		return refuse(ErrNotConsented)\n	}",
        "	// MUTANT: consent first, kill second -- the order is what is being changed.\n"
        "	if !c.granted {\n		return refuse(ErrNotConsented)\n	}\n"
        "	if c.killed {\n		return refuse(ErrKilled)\n	}",
        "TestTheCheckOrderIsObservable -- the operator's most urgent intent must win, "
        "or they are told to grant consent while the node is switched off",
    ),
    (
        # The budget check swapped to `used + bytes > limit` WITHOUT the zero-means-
        # uncapped special case. This is the single most likely way to break this
        # file, because the guard looks redundant when the limit is normally nonzero.
        "budget-zero-means-zero",
        "	if c.budgetBytes != 0 && c.used+bytes > c.budgetBytes {",
        "	// MUTANT: no uncapped case, so a budget of 0 refuses everything.\n"
        "	if c.used+bytes > c.budgetBytes {",
        "TestAZeroBudgetMeansNoCap -- the most common real setting is 'I consent but "
        "do not let me spend unnoticed', which is exactly a budget of zero. A "
        "zero-means-zero rule forbids the setting entirely",
    ),
    (
        # The budget becomes a post-hoc REPORT rather than a check: charge it, then
        # refuse. The total is then already spent when the operator is told to stop,
        # which is the failure R089 names directly -- "a cap checked after the "
        #transfer is not a cap".
        "budget-checked-after-charging",
        "	if c.budgetBytes != 0 && c.used+bytes > c.budgetBytes {\n		return refuse(ErrBudget)\n	}\n\n	c.used += bytes",
        "	// MUTANT: charge first, check after.\n	c.used += bytes\n"
        "	if c.budgetBytes != 0 && c.used > c.budgetBytes {\n		return refuse(ErrBudget)\n	}",
        "TestTheBudgetStopsTransferAndNamesItself -- a cap checked after the bytes "
        "are spent is a report, not a cap; the operator is told to stop after their "
        "bandwidth is already gone",
    ),
    (
        # Revocation clears the counter. Then an operator who changes their mind
        # twice gets a fresh budget, for free, and R089's cap is not a cap.
        "revocation-resets-the-counter",
        "	c.granted = granted",
        "	c.granted = granted\n	// MUTANT: revoking forgives the bytes already carried.\n	if !granted {\n		c.used = 0\n		c.attribution = make(map[string]uint64)\n	}",
        "TestKillIsASwitchAndRevocationIsADecision -- bytes already carried were "
        "carried; a counter that forgets them on revocation lets an operator reset "
        "the budget by changing their mind twice",
    ),
    (
        # A refused frame is charged. Now a peer looping against an exhausted budget
        # inflates the operator's used-bytes without moving any payload -- the log
        # becomes a worse lie than the one R089 was filed against.
        "refusal-charged-to-the-counter",
        "	refuse := func(reason error) error {\n		c.refusals[reasonKey(reason)]++",
        "	refuse := func(reason error) error {\n		// MUTANT: charge the refusal.\n		c.used += Frame\n		c.refusals[reasonKey(reason)]++",
        "TestTheBudgetStopsTransferAndNamesItself -- a refused frame must not be "
        "charged, or a peer looping on the refusal inflates the operator's accounting",
    ),
    (
        # The frame ceiling is clamped instead of refused. The caller then believes it
        # sent what it asked to send, and the admission decision was about a different
        # quantity than the bytes that actually move.
        "oversized-frame-clamped",
        "	if bytes > Frame {\n		return refuse(ErrBudget)\n	}",
        "	// MUTANT: clamp instead of refusing.\n	if bytes > Frame {\n		bytes = Frame\n	}",
        "TestAFrameLargerThanAFrameIsRefusedNotClamped -- a clamp admits a decision "
        "about a different quantity than the bytes that move, and the caller is told "
        "nothing",
    ),
    (
        # Lowering the cap below usage is honoured by clamping the counter down, so
        # the node is instantly back under its cap and keeps serving.
        "lowering-cap-resets-to-fit",
        "	c.budgetBytes = bytes",
        "	c.budgetBytes = bytes\n	// MUTANT: shrink usage to fit the new cap.\n	if c.budgetBytes != 0 && c.used > c.budgetBytes {\n		c.used = c.budgetBytes\n	}",
        "TestLoweringTheCapBelowUsageRefusesRatherThanClamping -- a cap that can be "
        "lowered below usage and then serves anyway is not a cap",
    ),
    (
        # Attribution returns the live map. A status endpoint can then rewrite the
        # operator's accounting, which is a way to raise your own budget without the
        # operator's involvement.
        "attribution-returns-the-live-map",
        "	out := make(map[string]uint64, len(c.attribution))\n	for k, v := range c.attribution {\n		out[k] = v\n	}\n	return out",
        "	// MUTANT: hand back the live map.\n	return c.attribution",
        "TestForwardedBytesAreAttributedToTheirOrigin -- the operator's number is "
        "nobody else's to move, and a status endpoint is not the operator",
    ),
    (
        # The admission lock is dropped, so concurrent connections each see the same
        # `used` and all are admitted. Overspend is silent and grows with connection
        # count -- the worst shape for a bug whose whole point is that the cap holds.
        "admission-lock-removed",
        "	c.mu.Lock()\n	defer c.mu.Unlock()\n\n	refuse := func(reason error) error {",
        "	// MUTANT: no lock, so check-then-charge races.\n\n	refuse := func(reason error) error {",
        "TestConcurrentAdmissionDoesNotOverspend -- a relay copies on many "
        "connections at once, and every one of them reading the same counter is how a "
        "cap silently stops holding",
    ),
    (
        # A zero-byte frame is admitted for free. Not a crash, and not an overspend --
        # a caller can "transfer" without transferring and the bookkeeping looks fine.
        "zero-byte-frame-admitted",
        "	if bytes == 0 {\n		return refuse(ErrBudget)\n	}",
        "	// MUTANT: admit it.\n	if false && bytes == 0 {\n		return refuse(ErrBudget)\n	}",
        "TestAFrameLargerThanAFrameIsRefusedNotClamped -- a zero-length grant looks "
        "like a successful transfer and moves nothing",
    ),
]

SUITE_PATTERN = (
    "TestAZeroValueRelayRefuses|"
    "TestConsentAloneIsNotEnoughAndACapAloneIsNotEnough|"
    "TestTheBudgetStopsTransferAndNamesItself|"
    "TestTheKillSwitchStopsTheTransferItIsThrownDuring|"
    "TestKillIsASwitchAndRevocationIsADecision|"
    "TestTheCheckOrderIsObservable|"
    "TestAZeroBudgetMeansNoCap|"
    "TestLoweringTheCapBelowUsageRefusesRatherThanClamping|"
    "TestForwardedBytesAreAttributedToTheirOrigin|"
    "TestAFrameLargerThanAFrameIsRefusedNotClamped|"
    "TestRefusalsAreCountedByReason|"
    "TestConcurrentAdmissionDoesNotOverspend|"
    "TestARefusalNamesItsOriginOrSaysItDoesNotKnow|"
    "TestTheGateNamesEveryRelayConsentTest|"
    "TestEveryRelayConsentCatcherNamesATestThatExists"
)

GO_ENV = {"GOFLAGS": "-mod=mod", "PATH": "/usr/bin:/bin:/usr/local/bin", "HOME": "/home/alvaro"}


def run_tests() -> bool:
    r = subprocess.run(
        ["go", "test", "./internal/relayconsent/", "-count=1", "-run", SUITE_PATTERN],
        cwd=str(PLUGIN),
        capture_output=True,
        text=True,
        env=GO_ENV,
    )
    return r.returncode == 0


def main() -> int:
    print("relayconsent mutation gate: %d mutants\n" % len(MUTANTS))

    if not run_tests():
        print("FATAL: the suite does not pass BEFORE mutation.")
        print("Every mutant would be reported as a survivor, which reads as a finding")
        print("about the code and is a finding about the gate.")
        return 1
    print("baseline: green\n")

    original = SRC.read_text()
    survivors = []

    for name, old, new, catcher in MUTANTS:
        if old not in original:
            print("  %-38s SKIPPED -- pattern not in the file." % name)
            survivors.append((name, "PATTERN NOT FOUND"))
            continue

        mutated = original.replace(old, new, 1)
        SRC.write_text(mutated)

        # The mutant must be IN the file. A substitution that silently failed to land
        # would leave a green suite and report the mutant as survived -- which is the
        # same as reporting a working gate as broken.
        if new not in SRC.read_text():
            print("  %-38s ERROR -- substitution did not land." % name)
            SRC.write_text(original)
            survivors.append((name, "SUBSTITUTION DID NOT LAND"))
            continue

        killed = not run_tests()
        SRC.write_text(original)

        if SRC.read_text() != original:
            print("  %-38s ERROR -- RESTORE FAILED, tree left mutated!" % name)
            survivors.append((name, "RESTORE FAILED"))
            continue

        if killed:
            print("  %-38s killed  <- %s" % (name, catcher.split(" --")[0]))
        else:
            print("  %-38s SURVIVED" % name)
            print("     expected catcher: %s" % catcher)
            survivors.append((name, catcher))

    print()
    if survivors:
        print("SURVIVORS: %d of %d" % (len(survivors), len(MUTANTS)))
        for name, why in survivors:
            print("  %-38s %s" % (name, why))
        return 1

    print("all %d mutants killed." % len(MUTANTS))
    return 0


if __name__ == "__main__":
    sys.exit(main())