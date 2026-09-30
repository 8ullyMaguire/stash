#!/usr/bin/env python3
"""Mutation harness for the stash#7135 credential fix.

A test that passes proves nothing unless it CAN fail. Each mutation below
re-introduces one specific pre-fix behaviour and asserts that a named test
notices. A mutation that survives means the corresponding test is not actually
guarding the thing it claims to guard.

Two rules this harness follows because getting them wrong produces a confident
wrong answer:

1. **Mutation and restore happen in ONE process, with restore in a `finally`.**
   A restore in a separate step is orphaned whenever the caller dies between
   the two -- and an orphaned mutation leaves the tree in a state that still
   builds and still looks fine.

2. **A surviving mutation is a RESULT, not a failure to route around.** It means
   the guarded line is DEAD or REDUNDANT: the next layer already does the job,
   so the check should be deleted rather than defended. A harness that reports
   survivors as "inconclusive" has quietly become a harness that never fails.

Usage:  python3 docs/mutate_7135.py
"""

import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent

CONFIG = REPO / "internal/manager/config/config.go"

# (name, test to run, the exact text to replace, what replaces it)
MUTATIONS = [
    (
        "hashPassword swallows bcrypt's error again",
        "TestAPasswordOverBcryptsLimitIsRefusedRatherThanStoredEmpty",
        # NOTE the return signature is UNCHANGED. An earlier draft dropped the
        # error from the signature too, which made SetPassword's two-value call
        # fail to compile -- and a mutation that stops the package building gets
        # counted as a kill while the test never runs. The signature stays; only
        # the BODY is mutated.
        """	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		return "", err
	}

	return string(hash), nil""",
        """	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	_ = err

	return string(hash), nil""",
    ),
    (
        "ValidateCredentials fails OPEN again (the original hole)",
        "TestAnAlreadyCorruptCredentialDoesNotAuthenticateAnyone",
        # Flip the restricted branch's RETURN VALUE rather than deleting the
        # branch. Deleting it left `logger` with no remaining use in the
        # package, so the import went unused and the build failed -- and a
        # build failure is NOT a kill, it is the guard never running. This
        # form compiles, and `return true` here is precisely the fail-open
        # behaviour the pre-fix code had.
        """			logger.Error("a username is configured but the stored password hash is " +
				"missing or unusable; refusing all authentication. Reset the " +
				"password to restore access")
			return false""",
        """			logger.Error("a username is configured but the stored password hash is " +
				"missing or unusable; refusing all authentication. Reset the " +
				"password to restore access")
			return true""",
    ),
    (
        "SetPassword stores the hash even when hashing failed",
        # The guard was MIS-POINTED on the first run, and this is the fix.
        # It named TestAPasswordOverBcryptsLimitIsRefusedRatherThanStoredEmpty,
        # which calls hashPassword DIRECTLY -- so it is blind to what SetPassword
        # does with the result, and the mutation survived it. A different test
        # in the same file, one that goes through SetPassword, does catch it.
        # Verified: the mutation applied, that test FAILed, the tree was
        # restored, and the row then scored KILLED.
        "TestSetPasswordRefusesAnOverlongPasswordWithoutDestroyingTheStoredCredential",
        """	hash, err := hashPassword(value)
	if err != nil {
		return err
	}

	i.SetString(Password, hash)
	return nil""",
        # Two-value call kept, so this still compiles; only the refusal is gone.
        """	hash, err := hashPassword(value)
	_ = err

	i.SetString(Password, hash)
	return nil""",
    ),
]


def run_test(pattern):
    """Run one test. Returns (verdict, output).

    verdict is "passed", "failed", or "did-not-run". The third is the important
    one: a mutation that breaks the build produces `[build failed]` and exit 1,
    which a naive harness scores as a kill. It is not a kill -- the test never
    executed, so it guarded nothing. An earlier draft of this harness did exactly
    that and reported 2/2 killed while both mutations had only stopped the
    package compiling.
    """
    r = subprocess.run(
        ["go", "test", "./internal/manager/config/", "-count=1",
         "-v", "-run", pattern],
        cwd=REPO, capture_output=True, text=True, timeout=900,
    )
    out = r.stdout + r.stderr

    if "build failed" in out or "cannot use" in out or "undefined:" in out:
        return "did-not-run", out
    if "--- FAIL" in out or "--- PASS" in out:
        # Confirm the named test actually executed rather than being filtered
        # out by -run. A pattern that matches nothing exits 0 and looks like a
        # pass, which is the other way this harness can lie.
        if f"=== RUN   {pattern}" not in out:
            return "did-not-run", out + f"\n(harness: the pattern {pattern!r} "\
                                      "matched no test, so this proves nothing)"
        return ("passed" if "--- FAIL" not in out else "failed"), out
    return "did-not-run", out


def main():
    # A snapshot in a temp dir, NOT in the repo: a backup file left in the work
    # tree is a backup somebody will eventually commit.
    backup = Path(tempfile.mkdtemp()) / "config.go.orig"
    shutil.copy2(CONFIG, backup)

    killed, survived, not_run = 0, [], []
    try:
        for name, test, old, new in MUTATIONS:
            src = backup.read_text()
            if old not in src:
                print(f"SKIP  {name}\n"
                      f"      anchor not found -- the code moved. A mutation whose "
                      f"anchor is missing tests nothing, and reporting it as a "
                      f"survivor would be wrong.")
                continue

            CONFIG.write_text(src.replace(old, new, 1))
            try:
                verdict, out = run_test(test)
            finally:
                # Restore INSIDE this iteration. If the test command raises, the
                # tree is still put back.
                shutil.copy2(backup, CONFIG)

            if verdict == "did-not-run":
                not_run.append((name, test))
                detail = next((l for l in out.splitlines()
                               if "build failed" in l or "cannot use" in l
                               or "undefined:" in l or "harness:" in l), "")
                print(f"NOT-RUN   {name}\n"
                      f"          {test} never executed -- {detail.strip()[:70]}\n"
                      f"          This is NOT a kill. The mutation must keep the "
                      f"package compiling.")
            elif verdict == "passed":
                survived.append((name, test))
                print(f"SURVIVED  {name}\n"
                      f"          {test} still passed -- this test does not guard "
                      f"this line.")
            else:
                killed += 1
                first = next((l for l in out.splitlines() if "--- FAIL" in l), "")
                print(f"KILLED    {name}\n"
                      f"          by {test}  [{first.strip()[:70]}]")
    finally:
        shutil.copy2(backup, CONFIG)
        print("\nrestored config.go from snapshot")

    total = len(MUTATIONS)
    print(f"\n{killed}/{total} killed, {len(survived)} survived, "
          f"{len(not_run)} did not run")
    if not_run:
        print("\nA 'did not run' is a harness bug, not a result. The mutation "
              "broke the build, so the guard never ran.")
        return 2
    if survived:
        print("\nSurvivors are findings, not noise:")
        for name, test in survived:
            print(f"  - {name}\n    guard: {test}")
        print("\nA surviving mutation means the guarded line is DEAD or REDUNDANT "
              "(the next layer already does it). Delete the check; do not add a test.")
    return 1 if survived else 0


if __name__ == "__main__":
    sys.exit(main())
