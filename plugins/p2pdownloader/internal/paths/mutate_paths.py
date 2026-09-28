#!/usr/bin/env python3
"""Mutation-check the path sanitisation gate (M5 step 5.2).

A peer-supplied filename is untrusted input and a name that escapes the
download directory is the bug class that owns a box. Every mutation here is a
gate that still refuses SOMETHING while letting one specific escape through —
which is why "the tests pass" is not the question and "which test failed" is.

Five verdicts, and anything but `killed` is a hole:

  killed    the named test failed
  survived  the tests passed -- a HOLE
  broken    does not compile -- NOT a kill
  unscored  the edit did not apply
  no-op     the edit changed no bytes

Run from the plugin directory:  python3 internal/paths/mutate_paths.py
"""

import fcntl
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
PLUG = os.path.abspath(os.path.join(HERE, "..", ".."))
SRC = os.path.join(HERE, "sanitize.go")
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
        "the `..` component check is removed",
        '\tfor _, comp := range strings.FieldsFunc(name, func(r rune) bool {\n'
        '\t\treturn r == \'/\' || r == \'\\\\\'\n'
        '\t}) {\n'
        '\t\tif comp == ".." {\n',
        '\tfor _, comp := range strings.FieldsFunc(name, func(r rune) bool {\n'
        '\t\treturn r == \'/\' || r == \'\\\\\'\n'
        '\t}) {\n'
        '\t\tif false && comp == ".." {\n',
        "TestHostileNamesAreRefused",
    ),
    (
        "the absolute-path check is removed",
        "\tif filepath.IsAbs(name) {",
        "\tif false {",
        "TestHostileNamesAreRefused",
    ),
    (
        "the NUL check is removed",
        "\tif strings.ContainsRune(name, 0) {",
        "\tif false {",
        "TestHostileNamesAreRefused",
    ),
    (
        "the trailing dot/space check is removed",
        "\t\tif strings.HasSuffix(comp, \".\") || strings.HasSuffix(comp, \" \") {",
        "\t\tif false {",
        "TestHostileNamesAreRefused",
    ),
    (
        "the Windows reserved-name check is removed",
        "\tif reservedWindows[strings.ToLower(strings.TrimSpace(stem))] {",
        "\tif false && reservedWindows[strings.ToLower(strings.TrimSpace(stem))] {",
        "TestHostileNamesAreRefused",
    ),
    (
        # The whole filesystem check, not one line of it. An earlier version
        # removed only `resolveExistingPrefix`, and the mutation SURVIVED --
        # because `filepath.EvalSymlinks(root)` on the other side still
        # resolved the symlink and `containedIn` caught the escape on the
        # resolved root. Two lines, either of which alone is not the bug.
        "the symlink check is removed entirely -- THE headline escape",
        "\tresolved, err := resolveExistingPrefix(joined)\n\tif err != nil {\n\t\treturn \"\", err\n\t}\n\n"
        "\trootResolved, err := filepath.EvalSymlinks(root)\n\tif err != nil {\n"
        "\t\treturn \"\", fmt.Errorf(\"resolving the download root %q: %w\", root, err)\n\t}\n",
        "\tvar err error\n\tresolved := joined\n\t_ = err\n\n\trootResolved := root\n",
        "TestASymlinkInTheNameIsRefused",
    ),
    (
        "a symlink in a SUBDIRECTORY is missed (only the first component is resolved)",
        "\t\tresolved, err := filepath.EvalSymlinks(current)",
        "\t\tresolved, err := current, error(nil)",
        "TestASymlinkInASubdirectoryIsRefused",
    ),
    (
        "containment becomes a string prefix -- the sibling-directory escape",
        '\tif rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {',
        "\tif false {",
        "TestASiblingDirectoryWithASharedPrefixIsNotInside",
    ),
    (
        "a component beginning with dots is treated as a traversal",
        '\tif rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {',
        '\tif rel == ".." || strings.HasPrefix(rel, "..") {',
        "TestANameBeginningWithDotsIsInsideTheRoot",
    ),
    (
        "an escape is CLAMPED into the root instead of refused",
        '\t\t\treturn "", fmt.Errorf("%w: %q contains a %q component. A torrent "+\n'
        '\t\t\t\t"names files, and a file that names its own path upward is not "+\n'
        '\t\t\t\t"a file", ErrEscapes, name, "..")',
        "\t\t\tcontinue",
        "TestHostileNamesAreRefused",
    ),
    (
        # MkdirAll on a path occupied by a file returns an error, so replacing
        # the IsDir check with MkdirAll is a NO-OP in practice -- the first
        # version of this mutation survived for exactly that reason, and looked
        # like a gap in EnsureRoot rather than in the mutation. The real clobber
        # is a REMOVE, which silently succeeds and destroys the file.
        "EnsureRoot removes whatever is at the path, clobbering a file",
        "\t\tif !info.IsDir() {\n\t\t\treturn fmt.Errorf(\"%w: the download root %q exists but is not a \"+\n"
        "\t\t\t\t\"directory\", ErrEscapes, root)\n\t\t}\n\t\treturn nil",
        "\t\t_ = info\n\t\tif !info.IsDir() {\n\t\t\tif err := os.RemoveAll(root); err != nil {\n"
        "\t\t\t\treturn err\n\t\t\t}\n\t\t}\n\t\treturn nil",
        "TestEnsureRootNeverDestroysWhatIsAlreadyThere",
    ),
    (
        "EnsureRoot is not idempotent (an existing directory is an error)",
        "\t\tif !info.IsDir() {\n\t\t\treturn fmt.Errorf(\"%w: the download root %q exists but is not a \"+\n"
        "\t\t\t\t\"directory\", ErrEscapes, root)\n\t\t}\n\t\treturn nil",
        "\t\tif !info.IsDir() || info.IsDir() {\n\t\t\treturn fmt.Errorf(\"%w: the download root %q already exists\", ErrEscapes, root)\n\t\t}\n\t\treturn nil",
        "TestEnsureRootCreatesItOnceAndRefusesAFile",
    ),
]


def main():
    # Two harnesses must never run at once: they edit real files and restore
    # them, so a concurrent pair corrupts each other and produces a red in
    # unrelated code.
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
            build = run("go build ./internal/paths/")
            if build.returncode != 0:
                out = (build.stdout + build.stderr).strip().splitlines()
                results.append((name, "broken",
                                "does not compile: " + (out[-1] if out else "")[:80]))
                continue

            test = run(f"go test ./internal/paths/ -count=1 -run {expect}")
            if test.returncode != 0:
                results.append((name, "killed", f"{expect} failed"))
            else:
                results.append((name, "survived",
                                f"{expect} passed with the escape open"))
        finally:
            open(SRC, "w").write(original)

    # Prove the restore, do not assume it. `git checkout` cannot restore an
    # untracked file and this file is work in progress.
    for path, want in SNAPSHOTS.items():
        if open(path).read() != want:
            print(f"FATAL: {path} was not restored, so every result below "
                  f"describes a tree that is not the one on disk")
            return 2

    if run("go build ./...").returncode != 0:
        print("FATAL: the plugin does not build after restoring every mutation")
        return 2
    if run("go test ./internal/paths/ -count=1").returncode != 0:
        print("FATAL: the paths package does not pass its own tests after restoring")
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
