#!/usr/bin/env python3
"""Mutation gate for stash#4326's related-content panel.

Modeled on `ui/v2.5/src/hooks/mousetrapScope.mutate.py`, which is the existing harness
that mutates JAVASCRIPT from Python, runs `node --test`, and applies each mutant by
EXACT-STRING substitution -- so an edit that moves the code fails the substitution loudly
instead of silently mutating nothing.

WHY THIS IS A SEPARATE FILE AND NOT A CLAIM IN A COMMIT MESSAGE: a gate that cannot be
made to fail is decoration. Every mutant below must be KILLED by a named test. A SURVIVOR
is a hole in the tests UNLESS the code is wrong -- read the diff before touching the
source: if disabling the check makes the suite pass, the check was wrong, not the test
blind.

A mutant whose witness test does not exist is scored SKIP, which is NOT a kill. Counting
those as kills is how a harness reports 6/6 while testing 5, so the summary reports the
three counts separately and exits 1 if anything survives.

Usage:  python3 docs/mutate_4326.py            # all mutants
        python3 docs/mutate_4326.py 3          # just M3
        python3 docs/mutate_4326.py --list
"""

from __future__ import annotations

import fcntl
import pathlib
import shutil
import signal
import subprocess
import sys
import tempfile
import time

HERE = pathlib.Path(__file__).resolve().parent
UI = HERE.parent / "ui" / "v2.5"
PKG = UI / "src" / "components" / "ScenePlayer"
LOCK = HERE / ".mutate_4326.lock"

TEST_FILE = PKG / "highlight.test.js"

# Each mutant: (id, description, file, exact old string, exact new string, witness test name).
# `old` MUST match byte-for-byte. The harness asserts that and reports APPLY-FAILED rather
# than letting a no-op mutation score as a kill.
MUTANTS = [
    (
        "M1",
        "browsing navigates: `move` also chooses the highlighted scene",
        PKG / "highlight.js",
        "export const moveHighlight = (current, delta, length) =>\n  clampIndex(",
        "export const moveHighlight = (current, delta, length, onNavigate) =>\n  onNavigate?.(current), clampIndex(",
        "T1",
    ),
    (
        "M2",
        "the navigation predicate admits every key, so a future key navigates undecided",
        PKG / "navigation.js",
        'export const isNavigationEvent = (key) => key === "enter";',
        'export const isNavigationEvent = (key) => key !== "shift";',
        "T2",
    ),
    (
        "M3",
        "the highlight WRAPS instead of clamping -- invisible to a user, looks like a dead key",
        PKG / "highlight.js",
        "export const moveHighlight = (current, delta, length) =>\n  clampIndex(",
        "export const moveHighlight = (current, delta, length) =>\n  length === 0 ? 0 : (((current + delta) % length) + length) % length,",
        "T3",
    ),
    (
        "M4",
        "the highlight is not clamped, so it can point past the end of the list",
        PKG / "highlight.js",
        "  if (!Number.isFinite(index) || index < 0) return 0;\n  return Math.min(index, length - 1);",
        "  if (!Number.isFinite(index) || index < 0) return 0;\n  return Math.trunc(index);",
        "T3",
    ),
    (
        "M5",
        "an empty list yields index 0 instead of nothing, so a scene that is not there is chosen",
        PKG / "highlight.js",
        "export const chosenIndex = (highlight, length) => {\n  if (!Number.isFinite(length) || length <= 0) return null;",
        "export const chosenIndex = (highlight, length) => {\n  if (!Number.isFinite(length) || length < 0) return null;",
        "T4",
    ),
    (
        "M6",
        "choosing does not close the panel, so it covers the scene just picked",
        PKG / "navigation.js",
        "  if (onSceneChosen) onSceneChosen(index);\n  if (onClose) onClose();",
        "  if (onSceneChosen) onSceneChosen(index);",
        "T7",
    ),
]

_lock_fh = None
_inflight: pathlib.Path | None = None


def _restore_inflight(signum=None, _frame=None):
    """Restore the file we mutated, on SIGTERM/SIGINT.

    Measured and necessary: a sibling sweep was killed mid-run and left a mutation applied
    to the working tree, which then failed an unrelated test and looked like a real bug.
    """
    if _inflight is not None and _inflight.exists():
        backup = _inflight.with_suffix(_inflight.suffix + ".mutbak")
        if backup.exists():
            shutil.copyfile(backup, _inflight)
            backup.unlink()
    if signum is not None:
        sys.exit(128 + signum)


def _take_lock():
    """One sweep at a time. Two concurrent sweeps raced on these same files."""
    global _lock_fh
    _lock_fh = LOCK.open("w")
    try:
        fcntl.flock(_lock_fh, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        print(f"another sweep holds {LOCK.name}; refusing to race", file=sys.stderr)
        sys.exit(2)


def _run_tests() -> tuple[int, str]:
    """Run the suite under test with `node --test`, from ui/v2.5.

    `src` is a VITE alias, so the test's imports must be relative -- the harness runs from
    the package dir for that reason, exactly as `node --test src/hooks/mousetrapScope.test.js`
    is documented to work (measured: 3/3 pass).
    """
    proc = subprocess.run(
        ["node", "--test", str(TEST_FILE.relative_to(UI))],
        cwd=UI,
        capture_output=True,
        text=True,
        timeout=300,
    )
    return proc.returncode, proc.stdout + proc.stderr


def audit_test_filter() -> None:
    """Fail if the suite under test does not actually RUN every test the mutants name.

    Measured necessity, not hygiene: in the #1790 sweep a `-run` filter silently excluded
    two new tests, so a mutant survived that the full suite would have killed. A gate that
    quietly under-measures is indistinguishable from a gate that found real gaps.
    """
    code, out = _run_tests()
    if code != 0:
        print("the suite under test is RED before any mutation; fix that first", file=sys.stderr)
        print(out[-2000:], file=sys.stderr)
        sys.exit(2)

    # `node --test` prints "✔ <NAME> (<ms>)" -- the NAME is the FIRST token after the mark,
    # not the last. Taking the last token (the duration) is what made the audit report all
    # five witnesses missing while the suite was plainly green.
    listed = []
    for line in out.splitlines():
        stripped = line.strip()
        if stripped.startswith(("✔", "✓")):
            tokens = stripped[1:].split()
            if tokens:
                listed.append(tokens[0])
    witnesses = {m[5] for m in MUTANTS}
    missing = [w for w in sorted(witnesses) if not any(w in name for name in listed)]
    if missing:
        print(f"FILTER AUDIT FAILED: no test ran for witness(es) {missing}", file=sys.stderr)
        print(f"  tests that ran: {listed}", file=sys.stderr)
        sys.exit(2)
    print(f"filter audit: all {len(witnesses)} witnesses ran ({len(listed)} tests total)")


def apply_one(mutant):
    mid, desc, path, old, new, witness = mutant
    if not path.exists():
        return mid, desc, "SKIP", witness, f"{path} does not exist"
    original = path.read_text()
    if old not in original:
        return mid, desc, "SKIP", witness, "APPLY-FAILED: the exact string is not in the file"

    backup = path.with_suffix(path.suffix + ".mutbak")
    shutil.copyfile(path, backup)
    global _inflight
    _inflight = path
    try:
        path.write_text(original.replace(old, new, 1))
        code, out = _run_tests()
    finally:
        shutil.copyfile(backup, path)
        backup.unlink()
        _inflight = None

    if code == 0:
        return mid, desc, "SURVIVED", witness, "the whole suite passed with the mutation applied"
    return mid, desc, "KILLED", witness, "suite failed"


def main() -> int:
    signal.signal(signal.SIGTERM, _restore_inflight)
    signal.signal(signal.SIGINT, _restore_inflight)
    _take_lock()

    if "--list" in sys.argv:
        for m in MUTANTS:
            print(f"{m[0]}  {m[1]}  (witness {m[5]})")
        return 0

    only = [a for a in sys.argv[1:] if not a.startswith("-")]
    audit_test_filter()

    print(f"sweeping {len(MUTANTS)} mutants against {TEST_FILE.relative_to(UI)}\n")
    results = []
    for mutant in MUTANTS:
        if only and mutant[0].lower() not in [o.lower() for o in only]:
            continue
        started = time.time()
        mid, desc, verdict, witness, why = apply_one(mutant)
        results.append((mid, verdict))
        mark = {"KILLED": "✓", "SURVIVED": "✗ SURVIVED", "SKIP": "- SKIPPED"}[verdict]
        print(f"  {mid:<4} {mark:<12} {desc}")
        print(f"       witness={witness}  ({why})  [{time.time() - started:.1f}s]")

    killed = sum(1 for _, v in results if v == "KILLED")
    survived = [m for m, v in results if v == "SURVIVED"]
    skipped = [m for m, v in results if v == "SKIP"]

    print(f"\n=== summary ===\nKILLED ({killed}):")
    for m, v in results:
        if v == "KILLED":
            print(f"  - {m}")
    if survived:
        print(f"\nSURVIVED ({len(survived)}):")
        for m in survived:
            print(f"  - {m}")
    if skipped:
        print(f"\nSKIPPED ({len(skipped)}) -- a skip is NOT a kill:")
        for m in skipped:
            print(f"  - {m}")

    print(f"\nkilled {killed}/{len(results)}")
    if survived:
        print(
            "\nSURVIVORS -- a survivor is a hole in the tests unless the CODE is wrong. Read\n"
            "the diff before touching the source: if disabling the check makes the suite\n"
            "pass, the check was wrong, not the test blind."
        )
    if skipped:
        print("\nSKIPPED mutants mean the harness is not measuring what it claims to.")
    return 1 if (survived or skipped) else 0


if __name__ == "__main__":
    sys.exit(main())