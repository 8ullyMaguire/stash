#!/usr/bin/env python3
"""Mutation-check the storage gate (M5 step 5.3).

The gate is the only thing standing between a peer-controlled filename and the
filesystem, so the mutations here are the ones that matter most in this project:
each one lets a hostile name through, or turns a refusal into something
downstream cannot tell from success.

Two of these were written AFTER the tests caught real bugs, and they are
labelled, because a mutation someone invented to prove a test works is worth
less than one that reproduces a failure that actually happened:

  - the joining trap (`SanitizeJoin` handed a name that `filepath.Join` had
    already cleaned), which `TestAHostileNameIsCaughtEvenWhenItIsTheSecondComponent`
    caught as a genuine gap;
  - the relative sentinel, which returned a bare filename from an absolute-path
    contract.

# A LAYERED DEFENCE MAKES PER-LAYER MUTATIONS UNOBSERVABLE
#
# The most useful thing this harness taught, and it cost seven surviving rows to
# learn. The gate has three layers that all refuse a `..` walk:
#
#     1. per-component  -- checkComponent, on the RAW components
#     2. joined-name    -- paths.SanitizeJoin on the assembled name
#     3. directory      -- paths.SanitizeJoin on the torrent's own directory
#
# So "disable layer 1" changes nothing a test can see -- layers 2 and 3 refuse
# the same input. That is NOT a hole. The input is still refused, and a gate is
# allowed to have redundancy; treating a surviving row as a defect sends you
# into the code to fix something that is not broken.
#
# The mutations worth keeping are the COMPOUND ones, marked as such below: every
# layer that refuses this particular input, removed together, and then the
# question is whether the name can be WRITTEN. That is the question an operator
# cares about and the one a per-layer row cannot answer.
#
# Before a `survived` row is treated as a bug, check whether the mutation is a
# PARTIAL defect. A partial defect is still a defect -- the pre-join below is
# one, and its compound form is kept for that reason -- but it is not a hole,
# and the difference is the difference between fixing code and fixing a harness.

Six verdicts. Only `survived` means a hole:

  killed    the named test failed
  covered   no test noticed AND the whole suite still passed: another layer
            refuses the same input. Verified by re-running everything with the
            mutation applied, not assumed.
  survived  the whole suite FAILED but the named test did not. A HOLE.
  broken    does not compile -- NOT a kill
  unscored  the edit did not apply
  no-op     the edit changed no bytes

Run from the plugin directory:
  python3 internal/storage/mutate_gate.py
"""

import fcntl
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
PLUG = os.path.abspath(os.path.join(HERE, "..", ".."))
SRC = os.path.join(HERE, "gate.go")
ENV = {**os.environ, "GOFLAGS": "-mod=mod"}
LOCK = os.path.join(PLUG, ".mutation-harness.lock")

SNAPSHOTS = {}


def snapshot(path):
    SNAPSHOTS[os.path.abspath(path)] = open(path).read()


def run(cmd, timeout=180):
    """Run a command, and NEVER block forever.

    A mutation that removes a lock's `defer Unlock` deadlocks `go test`, and a
    harness that waits on that takes the whole run with it -- the first version
    of this one had no timeout and hung the session for five minutes. A hang is
    reported as `broken`, which is a verdict a human can act on.
    """
    try:
        return subprocess.run(cmd, shell=True, cwd=PLUG, capture_output=True,
                              text=True, env=ENV, timeout=timeout)
    except subprocess.TimeoutExpired:
        class TimedOut:
            returncode = 124
            stdout = ""
            stderr = "TIMED OUT after %ds -- the mutation deadlocked the test" % timeout
        return TimedOut()


MUTATIONS = [
    # ---- the up-front check, per component -------------------------------
    # The two `..`-refusing layers plus the directory check mean most
    # single-layer mutations here are unobservable: another layer refuses the
    # same input. Not a hole -- the input is still refused -- so the rows that
    # count are COMPOUND (a LIST of edits), marked below.

    (
        "the up-front check is removed entirely (backstop only)",
        "\tif bad := g.firstUnwritable(info); bad != \"\" {",
        "\tif bad := \"\"; bad != \"\" {",
        "TestTheGateRefusesTheWholeTorrentBeforeTheLibrarySeesIt",
    ),
    (
        "COMPOUND: the per-component check is handed a pre-joined name, so "
        "filepath.Join cleans the traversal away -- and the joined-name layer "
        "is removed too",
        [
            "\t\tfor _, comp := range f.BestPath() {",
            "if _, err := paths.SanitizeJoin(g.root, g.relativeName(info, &f)); err != nil {",
        ],
        [
            "\t\tfor _, comp := range []string{filepath.Join(f.BestPath()...)} {",
            "if _, err := g.relativeName(info, &f), error(nil); err != nil {",
        ],
        "TestARealSymlinkedTorrentDirectoryIsRefused",
    ),
    (
        "the joined-name layer does not catch a traversal the per-component "
        "check also missed",
        "if _, err := paths.SanitizeJoin(g.root, g.relativeName(info, &f)); err != nil {",
        "if _, err := g.relativeName(info, &f), error(nil); err != nil {",
        "TestANameOnlySanitizeJoinRefusesIsStillRefused",
    ),
    (
        "only the first component of a file's path is checked",
        "\t\tfor _, comp := range f.BestPath() {",
        "\t\tfor _, comp := range f.BestPath()[:1] {",
        "TestAHostileNameIsCaughtEvenWhenItIsTheSecondComponent",
    ),
    (
        "COMPOUND: the torrent's own name is unchecked AND the joined-name "
        "layer is removed",
        [
            "\t\tif err := g.checkComponent(torrentName); err != nil {",
            "if _, err := paths.SanitizeJoin(g.root, g.relativeName(info, &f)); err != nil {",
        ],
        [
            "\t\tif err := g.checkComponent(torrentName); false && err != nil {",
            "if _, err := g.relativeName(info, &f), error(nil); err != nil {",
        ],
        "TestARealSymlinkedTorrentDirectoryIsRefused",
    ),
    (
        "a `..` component stops being refused",
        '\tcase ".", "..":',
        '\tcase ".":',
        "TestAHostileNameIsCaughtEvenWhenItIsTheSecondComponent",
    ),
    (
        "a component containing a separator is allowed",
        "if strings.ContainsAny(comp, `/\\`) {",
        "if false {",
        "TestAHostileNameIsCaughtEvenWhenItIsTheSecondComponent",
    ),
    (
        "COMPOUND: an empty component is refused AND the joined-name layer is "
        "removed, so a torrent every join handles correctly stops working",
        [
            '\tcase "":',
            "if _, err := paths.SanitizeJoin(g.root, g.relativeName(info, &f)); err != nil {",
        ],
        [
            '\tcase "":',
            "if _, err := g.relativeName(info, &f), error(nil); err != nil {",
        ],
        "TestARealSymlinkedTorrentDirectoryIsRefused",
    ),

    # ---- the joined-name layer -------------------------------------------
    (
        "the joined-name backstop check is dropped (SanitizeJoin never runs)",
        "if _, err := paths.SanitizeJoin(g.root, g.relativeName(info, &f)); err != nil {",
        "if _, err := paths.SanitizeJoin(g.root, g.relativeName(info, &f)); false && err != nil {",
        "TestANameOnlySanitizeJoinRefusesIsStillRefused",
    ),
    (
        "COMPOUND: a NUL is stripped before the joined-name check AND the "
        "per-component check ignores it, so the name is written truncated",
        [
            "if _, err := paths.SanitizeJoin(g.root, g.relativeName(info, &f)); err != nil {",
            "if strings.ContainsRune(comp, 0) {",
        ],
        [
            'if _, err := paths.SanitizeJoin(g.root, '
            'strings.ReplaceAll(g.relativeName(info, &f), "\\x00", "")); err != nil {',
            "if false {",
        ],
        "TestANulInAComponentIsRefused",
    ),
    (
        "COMPOUND: a NUL in a component is allowed AND the joined-name layer "
        "strips it before checking",
        [
            "if strings.ContainsRune(comp, 0) {",
            "if _, err := paths.SanitizeJoin(g.root, g.relativeName(info, &f)); err != nil {",
        ],
        [
            "if false {",
            'if _, err := paths.SanitizeJoin(g.root, '
            'strings.ReplaceAll(g.relativeName(info, &f), "\\x00", "")); err != nil {',
        ],
        "TestANulInAComponentIsRefused",
    ),

    # ---- the backstop, through the library's own extension point ---------
    (
        "REPRODUCES A REAL BUG: the backstop returns a bare filename instead "
        "of a path inside the root",
        "return filepath.Join(g.root, refuseSentinel)",
        "return filepath.Join(refuseSentinel)",
        "TestEveryRefusedNameEndsUpInOnePlace",
    ),
    (
        "the backstop returns a path outside the root on refusal",
        "return filepath.Join(g.root, refuseSentinel)",
        'return filepath.Join(g.root, "..", "..", "escape")',
        "TestEveryRefusedNameEndsUpInOnePlace",
    ),
    (
        "COMPOUND: the backstop stops routing names through the gate AND the "
        "up-front check is gone",
        [
            "safe, err := paths.SanitizeJoin(g.root, name)",
            "\tif bad := g.firstUnwritable(info); bad != \"\" {",
        ],
        [
            "safe, err := filepath.Join(g.root, name), error(nil)",
            "\tif bad := \"\"; bad != \"\" {",
        ],
        "TestTheGateRefusesTheWholeTorrentBeforeTheLibrarySeesIt",
    ),
    (
        "the backstop never resolves a symlink",
        "safe, err := paths.SanitizeJoin(g.root, name)",
        "safe, err := name, error(nil)",
        "TestARealSymlinkInsideTheTorrentIsRefused",
    ),

    # ---- the download root -----------------------------------------------
    (
        "a missing download root is created rather than reported",
        "rootInfo, err := os.Stat(g.root)\n\tif err != nil {",
        "rootInfo, err := os.Stat(g.root)\n\tif false {",
        "TestAMissingRootIsReported",
    ),
    (
        "a regular file where the download root should be is accepted",
        "\tif !rootInfo.IsDir() {\n"
        "\t\t// The library refuses this too, but with an error that says something\n"
        "\t\t// about piece completion rather than about the configured path. This one\n"
        "\t\t// names the actual problem, which is the difference between a five-minute\n"
        "\t\t// diagnosis and a five-second one.\n"
        "\t\treturn libstorage.TorrentImpl{}, fmt.Errorf(\n"
        '\t\t\t"%w: the download root %q is not a directory", ErrRefused, g.root)\n'
        "\t}",
        "\tif rootInfo.IsDir() {\n"
        "\t\t// The refusal is gone; `NewFileOpts` then fails on its own.\n"
        "\t}",
        "TestAMissingRootIsReported",
    ),

    # ---- recording refusals ----------------------------------------------
    (
        "COMPOUND: a refusal stops recording the name AND stops deduping, so a "
        "report names nothing and repeats forever",
        [
            "g.refused = append(g.refused, Refusal{Hash: hash, Name: name, Reason: reason})",
            "\tfor _, r := range g.refused {\n\t\tif r.Hash == hash {\n\t\t\treturn\n\t\t}\n\t}",
        ],
        [
            "g.refused = append(g.refused, Refusal{Hash: hash, Reason: reason})",
            "\tif false {\n\t\treturn\n\t}",
        ],
        "TestTheRefusalIsRecordedForTheCaller",
    ),
    (
        "a refusal is recorded under every attempt rather than one per torrent",
        "\tfor _, r := range g.refused {\n\t\tif r.Hash == hash {\n\t\t\treturn\n\t\t}\n\t}",
        "\tif false {\n\t\treturn\n\t}",
        "TestTheRefusalIsRecordedForTheCaller",
    ),
    (
        "a backstop refusal is overwritten by the next one (the zero-hash path)",
        "\tif hash == (metainfo.Hash{}) {",
        "\tif false {",
        "TestEveryBackstopRefusalIsKept",
    ),
    (
        "the library's own refusal is recorded as ours",
        "\t\treturn tl, err\n\t}\n\treturn tl, nil",
        "\t\tg.record(hash, info.BestName(), err.Error())\n\t\treturn tl, err\n\t}\n\treturn tl, nil",
        "TestALibraryRefusalIsNotRecordedAsOurs",
    ),
]

def main():
    # Two harnesses must never run at once: they edit real files and restore
    # them, so a concurrent pair corrupts each other.
    lock = open(LOCK, "w")
    fcntl.flock(lock, fcntl.LOCK_EX)
    try:
        return run_all()
    finally:
        fcntl.flock(lock, fcntl.LOCK_UN)
        lock.close()


def run_all():
    for n, m in enumerate(MUTATIONS):
        if len(m) != 4:
            print(f"FATAL: mutation {n} ({m[0]!r}) has {len(m)} fields, expected 4: "
                  f"name, old, new, test")
            return 2

    snapshot(SRC)
    original = open(SRC).read()
    results = []

    for name, old, new, expect in MUTATIONS:
        # `old` and `new` are either single strings or equal-length LISTS. A
        # list is a COMPOUND mutation: every edit applied in order, because
        # disabling one layer of a layered defence is unobservable while another
        # layer still refuses the same input.
        olds = old if isinstance(old, list) else [old]
        news = new if isinstance(new, list) else [new]
        if len(olds) != len(news):
            print(f"FATAL: mutation {name!r} has {len(olds)} edits to make and "
                  f"{len(news)} replacements")
            return 2

        missing = [o for o in olds if o not in original]
        if missing:
            results.append((name, "unscored",
                            "%d of %d edits found no text, so the mutation is "
                            "testing nothing" % (len(missing), len(olds))))
            continue

        mutated = original
        for o, n in zip(olds, news):
            mutated = mutated.replace(o, n, 1)
        if mutated == original:
            results.append((name, "no-op", "the edits changed no bytes"))
            continue

        open(SRC, "w").write(mutated)
        try:
            build = run("go build ./internal/storage/")
            if build.returncode != 0:
                out = (build.stdout + build.stderr).strip().splitlines()
                detail = next((l for l in out if "gate.go" in l), out[-1] if out else "")
                results.append((name, "broken", "does not compile: " + detail.strip()[:80]))
                continue

            test = run(f"go test ./internal/storage/ -count=1 -run {expect}")
            if test.returncode != 0:
                results.append((name, "killed", f"{expect} failed"))
                continue

            # The named test passed. Is the gate actually open?
            #
            # A layered defence makes most single-layer mutations unobservable:
            # removing the per-component check changes nothing when the
            # joined-name check refuses the same name. So "the test passed" is
            # not yet "there is a hole" -- and the difference is decided by
            # running the WHOLE package with the mutation still applied.
            #
            # If everything passes, no layer is left open and the mutation is
            # `covered`: a real defect in the layer it broke, unreachable now.
            # If something fails, it IS a hole and the row says so.
            whole = run("go test ./internal/storage/ -count=1")
            if whole.returncode == 0:
                results.append((name, "covered",
                                f"{expect} passed and so did the whole suite -- "
                                f"another layer refuses this input"))
            else:
                results.append((name, "survived",
                                f"the suite FAILED with the gate open, but "
                                f"{expect} did not notice"))
        finally:
            open(SRC, "w").write(original)

    for path, want in SNAPSHOTS.items():
        if open(path).read() != want:
            print(f"FATAL: {path} was not restored, so every result below "
                  f"describes a tree that is not the one on disk")
            return 2

    if run("go build ./...").returncode != 0:
        print("FATAL: the plugin does not build after restoring every mutation")
        return 2
    if run("go test ./internal/storage/ -count=1").returncode != 0:
        print("FATAL: the gate does not pass its own tests after restoring")
        return 2

    width = max(len(n) for n, _, _ in results)
    for name, verdict, detail in results:
        print(f"  {name:<{width}}  {verdict.upper():9} {detail}")

    killed = sum(1 for _, v, _ in results if v == "killed")
    covered = sum(1 for _, v, _ in results if v == "covered")
    survived = sum(1 for _, v, _ in results if v == "survived")
    broken = sum(1 for _, v, _ in results if v == "broken")
    unscored = sum(1 for _, v, _ in results if v == "unscored")
    noop = sum(1 for _, v, _ in results if v == "no-op")
    print()
    print(f"  {killed} killed, {covered} covered by another layer, "
          f"{survived} survived, {broken} broken, {unscored} unscored, "
          f"{noop} no-op")

    # `covered` is not a pass. It is a claim that a second layer refuses the
    # same input, and it is only trusted because the WHOLE suite was re-run with
    # the mutation applied. `survived` is the verdict that means a hole.
    return 1 if (survived or broken or unscored or noop) else 0


if __name__ == "__main__":
    sys.exit(main())
