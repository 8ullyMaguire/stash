#!/usr/bin/env python3
"""Mutation-check the P2P downloader seam (M5 step 5.0).

Each mutation is a defect that turns the downloader from a PLUGIN into core
code. Both of them are silent: the core builds, the tests pass, and the thing
being tested -- "removable by deleting a directory" -- is no longer true.

Four outcomes, and anything but `killed` is a hole:

  killed    the named test failed
  survived  the tests passed -- a HOLE
  broken    the mutation does not compile -- NOT a kill
  unscored  the edit did not apply -- not a result

Run from the repo root:  python3 mutate_seam.py
"""

import fcntl
import os
import shutil
import subprocess
import sys

# The REPO ROOT, not this file's directory.
#
# # WHY THIS NEEDED A CORRECTION
#
# The first version used `os.path.dirname(__file__)`, which put ROOT at
# internal/api when the harness lived there. Everything then went to the wrong
# place: the mutations appended a `require` to internal/api/go.mod -- CREATING a
# nested module -- and built a copy of the plugin at internal/api/plugins/. The
# harness's own "the core still builds" check caught it and printed FATAL, which
# is the check doing its job, but a harness that damages the tree before
# noticing is a harness that needs the path pinned.
#
# A harness that writes into the repo must know exactly where the repo is, or
# its own bugs become unrecoverable ones.
ROOT = os.path.dirname(os.path.abspath(__file__))
PLUG = os.path.join(ROOT, "plugins", "p2pdownloader")
ENV = {**os.environ, "GOFLAGS": "-mod=mod"}

GOMOD = os.path.join(ROOT, "go.mod")
MAIN = os.path.join(ROOT, "cmd", "stash", "main.go")
PLUGGOMOD = os.path.join(PLUG, "go.mod")
MANIFEST = os.path.join(PLUG, "p2p-downloader.yml")

# What a bundled downloader actually looks like, since the obvious mutation does
# not compile. Go's internal/ rule means the core cannot import the plugin's
# internal packages AT ALL, so a bundled-downloader mutation needs a public
# package in the plugin module plus a `replace` in the core's go.mod.
ANCHOR_DIR = os.path.join(PLUG, "anchor")
ANCHOR = os.path.join(ANCHOR_DIR, "anchor.go")
ANCHOR_SRC = '''package anchor

import "github.com/stashapp/stash-plugin-p2pdownloader/internal/rpc"

func Marker() string { return rpc.ServiceNameForTest() }
'''

# SNAPSHOTS maps a mutated path to its contents from before any mutation ran.
SNAPSHOTS = {}


def snapshot(path):
    SNAPSHOTS[os.path.abspath(path)] = open(path).read()


def run(cmd, cwd=ROOT):
    return subprocess.run(cmd, shell=True, cwd=cwd, capture_output=True,
                          text=True, env=ENV)


def append_to(path, text):
    open(path, "a").write("\n" + text)


def replace_in(path, old, new):
    body = open(path).read()
    open(path, "w").write(body.replace(old, new, 1))


def restore(path):
    """Put a file back, whether or not git knows about it.

    `git checkout` only restores TRACKED files. The plugin's own go.mod and its
    manifest are untracked -- they are part of the work in progress, not part of
    any commit yet -- so `git checkout` on them is a silent no-op that returns
    success, and the mutation survives the harness.

    That happened twice, and both times the harness reported a clean tree while
    leaving a defect behind: `interface: raw` in the manifest, and the core's
    module path in the plugin's go.mod (which then made the plugin unbuildable
    and looked like a plugin bug for the next twenty minutes).

    A restore that cannot fail is not a restore. This one copies from an
    in-memory snapshot taken before ANY mutation ran, so it does not care about
    git at all.
    """
    snapshot = SNAPSHOTS.get(os.path.abspath(path))
    if snapshot is None:
        raise SystemExit(f"no snapshot for {path}: the harness would leave a "
                         f"mutation behind and report success")
    open(path, "w").write(snapshot)


def replace_value_line(path, old, new):
    """Replace the VALUE on the matching line, not the first mention of it.

    The manifest's comment block mentions `interface: rpc` in prose, so a plain
    str.replace hits the comment and the mutation measures nothing. This version
    only edits a line that is exactly the key.
    """
    lines = open(path).read().split("\n")
    for i, line in enumerate(lines):
        if line.strip() == old:
            lines[i] = line.replace("rpc", "raw")
            break
    open(path, "w").write("\n".join(lines))


def bundle_the_downloader():
    """Make the core legally import the downloader, so it links in.

    Three edits, and all three are needed: a public package inside the plugin
    module (internal/ is unimportable from outside), a require plus a replace in
    the core's go.mod, and a real use in main.go (a blank import alone does not
    survive the linker's reachability analysis, which is how the first attempt
    at this mutation was defeated).
    """
    os.makedirs(ANCHOR_DIR, exist_ok=True)
    open(ANCHOR, "w").write(ANCHOR_SRC)

    append_to(GOMOD, "require github.com/stashapp/stash-plugin-p2pdownloader v0.0.0\n")
    append_to(GOMOD, "replace github.com/stashapp/stash-plugin-p2pdownloader => ./plugins/p2pdownloader\n")

    body = open(MAIN).read()
    body = body.replace("func main() {", "func main() {\n\t_ = anchor.Marker()", 1)
    body = body.replace("import (", 'import (\n\t"github.com/stashapp/stash-plugin-p2pdownloader/anchor"', 1)
    open(MAIN, "w").write(body)


def unbundle_the_downloader():
    restore(GOMOD)
    restore(MAIN)
    shutil.rmtree(ANCHOR_DIR, ignore_errors=True)
    # go.sum is only touched by the bundled mutation, and only through
    # `go mod tidy` -- which this harness deliberately does not run, so the
    # checksum file is restored rather than regenerated. Regenerating it would
    # silently rewrite a file the mutation did not edit.
    subprocess.run("git checkout go.sum", shell=True, cwd=ROOT, capture_output=True)
    # Not `git status --short` as the check: every mutated file is untracked, so
    # the working tree looks clean whether or not the restore worked. The
    # snapshot comparison above is the check.


MUTATIONS = [
    (
        "core requires the downloader (the import seam breaks)",
        "TestP2PDownloaderIsNotImportedByCore",
        lambda: append_to(GOMOD, "require github.com/stashapp/stash-plugin-p2pdownloader v0.0.0\n"),
        lambda: restore(GOMOD),
    ),
    (
        "the downloader's module path sits under the core's",
        "TestP2PDownloaderHasItsOwnModule",
        lambda: replace_in(PLUGGOMOD, "module github.com/stashapp/stash-plugin-p2pdownloader",
                           "module github.com/stashapp/stash/plugins/p2pdownloader"),
        lambda: restore(PLUGGOMOD),
    ),
    (
        "the core binary links the downloader in (bundled)",
        "TestP2PDownloaderIsNotBundledByCore",
        bundle_the_downloader,
        unbundle_the_downloader,
    ),
    (
        "the manifest sets interface: raw",
        "TestTheDownloaderShipsAManifestTheHostCanRead",
        lambda: replace_value_line(MANIFEST, "interface: rpc", "interface: raw"),
        lambda: restore(MANIFEST),
    ),
    (
        "the manifest names no binary, so the host starts nothing",
        "TestTheDownloaderShipsAManifestTheHostCanRead",
        lambda: replace_in(MANIFEST, "  - stash-plugin-p2pdownloader", "  - /usr/bin/nothing"),
        lambda: restore(MANIFEST),
    ),
    (
        "the manifest is named wrongly, so the plugin's id is wrong",
        "TestTheDownloaderShipsAManifestTheHostCanRead",
        lambda: os.rename(MANIFEST, os.path.join(PLUG, "downloader.yml")),
        lambda: os.rename(os.path.join(PLUG, "downloader.yml"), MANIFEST),
    ),
]


def classify(test_out):
    """'broken' is not a kill: an uncompilable mutation proves nothing."""
    if any(s in test_out for s in (
        "no required module", "cannot find", "does not build",
        "no Go files", "undefined:", "imported and not used",
    )):
        return "broken", "the mutation does not compile, so it kills no test"
    return None, None


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
    # Snapshot every file this harness writes to, BEFORE any mutation. The
    # first version restored with `git checkout`, which cannot restore an
    # untracked file -- and every file the seam mutations touch is untracked,
    # because the plugin is the work in progress.
    for path in (GOMOD, MAIN, PLUGGOMOD, MANIFEST):
        snapshot(path)

    results = []

    for name, expect, apply_mutation, undo in MUTATIONS:
        try:
            apply_mutation()
        except Exception as exc:  # a mutation that cannot be applied is not a result
            results.append((name, "unscored", f"applying it failed: {exc}"))
            continue

        try:
            test = run(f"go test ./internal/api/ -count=1 -run {expect}")
            out = test.stdout + test.stderr

            verdict, detail = classify(out)
            if verdict is None:
                if test.returncode != 0:
                    verdict, detail = "killed", f"{expect} failed"
                else:
                    verdict, detail = "survived", f"{expect} passed with the defect in place"

            # Report WHICH assertion caught it, so a kill by the build check is
            # visible as distinct from a kill by the seam assertion itself.
            if verdict == "killed":
                for line in out.splitlines():
                    if "seam_test.go:" in line and ("contains" in line or "does not build" in line):
                        detail = "caught by: " + line.strip()[:110]
                        break
            results.append((name, verdict, detail))
        finally:
            undo()

    # # VERIFY THE RESTORE, DO NOT ASSUME IT
    #
    # The first version of this harness restored with `git checkout` and
    # reported success. Every file the seam mutations touch is UNTRACKED -- the
    # plugin is work in progress, not yet in any commit -- and `git checkout` on
    # an untracked path is a silent no-op. So the harness left `interface: raw`
    # in the manifest and the core's module path in the plugin's go.mod, and
    # reported a clean tree while doing it. The go.mod one made the plugin
    # unbuildable and read as a plugin bug.
    #
    # So the tree is compared against the snapshots, byte for byte, and a
    # difference is a FATAL rather than a footnote. A harness that writes into
    # the repo and cannot prove it put everything back is not finished.
    for path, want in SNAPSHOTS.items():
        got = open(path).read()
        if got != want:
            print(f"FATAL: {os.path.relpath(path, ROOT)} was not restored.")
            print("  The harness mutated it and did not put it back, so every")
            print("  result below describes a tree that is not the one you have.")
            return 2

    # BOTH modules, and the plugin's is the one that matters here: the seam
    # mutations are about the plugin, and a core-only build check would have
    # passed over a plugin whose go.mod had been left rewritten.
    if run("go build ./...").returncode != 0:
        print("FATAL: the core does not build after restoring every mutation")
        return 2
    if run("go build ./...", cwd=PLUG).returncode != 0:
        print("FATAL: the plugin does not build after restoring every mutation")
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
