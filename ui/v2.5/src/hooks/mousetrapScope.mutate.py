#!/usr/bin/env python3
"""Mutation gate for the #2833 shortcut-scoping layer.

A test that cannot fail is worse than no test, because it reads as coverage. This gate
exists so `mousetrapScope.fix.test.js` is known to be load-bearing: each mutant below is
a REAL way the fix could be wrong, and every one of them must be KILLED.

Reading the survivors is the point, not just the count:

  - A mutant that SURVIVES on every test is a line no test reaches -- dead or redundant
    code. Delete it or cover it.
  - A mutant that survives because a guard is DEFENSIBLE (the schema already refuses
    what the Go guard refuses) is a different finding: the guard is unreachable, and the
    test passed for a reason other than the one it names.
  - A mutant that survives because two lines are REDUNDANT -- each covering for the other
    -- means neither is independently verified. Add an input where they differ rather
    than assuming one is unreachable.

Each mutant below is applied by an exact-string substitution, so a file edit that moves
the code fails the substitution loudly instead of silently mutating nothing.

Usage:  python3 mousetrapScope.mutate.py        # exits non-zero if any mutant survives
"""

import pathlib
import subprocess
import sys
import tempfile

HERE = pathlib.Path(__file__).resolve().parent
TARGET = HERE / "mousetrapScope.js"
TESTS = [
    HERE / "mousetrapScope.fix.test.js",
    HERE / "mousetrapScope.test.js",
]

ORIGINAL = TARGET.read_text()

# (label, old, new, why this is a plausible mistake)
MUTANTS = [
    (
        "stack-popped-nothing",
        "    current.splice(at, 1);",
        "    // current.splice(at, 1);",
        "the unbind forgets to pop, so a departed handler keeps owning the key",
    ),
    (
        "pop-removes-the-wrong-handler",
        "    const at = current.findIndex((entry) => entry.owner === token);",
        "    const at = 0;",
        "the unbind pops the bottom of the stack instead of its own entry -- which looks "
        "correct while two components share a key and is wrong for every nest deeper "
        "than one",
    ),
    (
        "top-owner-ignored",
        "    const handler = stack[stack.length - 1];",
        "    const handler = stack[0];",
        "dispatch runs the OLDEST owner instead of the most recent, so a subpage never "
        "overrides the page behind it -- the regression #2833 asks for",
    ),
    (
        "dispatch-copies-a-stale-stack",
        "    const handler = stack[stack.length - 1];",
        "    const handler = stack[stack.length - 2] ?? stack[stack.length - 1];",
        "dispatch reads one level too shallow, which happens to work for a two-deep nest "
        "and breaks every deeper one",
    ),
    (
        "rebind-always-appends",
        "  if (existing >= 0) {\n    stack[existing] = { owner: token, callback };\n  } else {\n    stack.push({ owner: token, callback });\n  }",
        "  stack.push({ owner: token, callback });",
        "a re-render pushes a duplicate instead of replacing in place, so the handler "
        "fires once per render and grows without bound -- the same silent breakage as "
        "the original defect",
    ),
    (
        "idle-key-keeps-a-dispatcher",
        "      wiredKeys(registry()).delete(keys);",
        "      // wiredKeys(registry()).delete(keys);",
        "a key that went fully idle and is bound again gets no dispatcher, so it is dead "
        "forever -- a fix that trades one dead key for another",
    ),
    (
        "never-registers-the-dispatcher",
        "    M.bind(keys, wrapped(keys));",
        "    void keys;",
        "the layer maintains its stack but never talks to mousetrap, so every shortcut "
        "is dead while all the bookkeeping still looks right",
    ),
    (
        "unbind-is-not-idempotent",
        "    if (at < 0) return; // already popped -- unmount must be idempotent",
        "    if (at < 0) at = current.length - 1;",
        "a double unbind pops the NEXT owner's handler, which is what React StrictMode's "
        "double-invoke does in development",
    ),
]


def run_tests() -> tuple[bool, str]:
    proc = subprocess.run(
        ["node", "--test", *[str(t) for t in TESTS]],
        cwd=str(HERE),
        capture_output=True,
        text=True,
    )
    return proc.returncode == 0, proc.stdout + proc.stderr


def main() -> int:
    # A gate that cannot fail is the failure mode this file exists to prevent, so the
    # clean baseline is established FIRST and a dirty baseline aborts the run.
    ok, out = run_tests()
    if not ok:
        print("BASELINE IS NOT GREEN -- refusing to mutate. Fix the suite first.\n")
        print("\n".join(l for l in out.splitlines() if l.startswith(("✖", "not ok")))[:2000])
        return 2
    print("baseline green\n")

    survivors = []
    for label, old, new, why in MUTANTS:
        if old not in ORIGINAL:
            print(f"  SKIP  {label}: anchor not found -- the code moved, update this gate")
            survivors.append((label, "ANCHOR MISSING"))
            continue

        TARGET.write_text(ORIGINAL.replace(old, new, 1))
        try:
            passed, mout = run_tests()
        finally:
            TARGET.write_text(ORIGINAL)

        if passed:
            print(f"  SURVIVED  {label}: {why}")
            survivors.append((label, why))
        else:
            print(f"  killed    {label}")

    print()
    print(f"{len(MUTANTS) - len(survivors)}/{len(MUTANTS)} mutants killed")
    if survivors:
        print("\nSURVIVORS -- each is a real finding, not noise:")
        for label, why in survivors:
            print(f"  {label}: {why}")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())