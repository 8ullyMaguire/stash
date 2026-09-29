#!/usr/bin/env python3
"""Mutation harness for the generated-file sharding (stash#2824).

The report is that the generated directories were flat -- ~50k files in
generated/screenshots and generated/vtt -- and that listing such a directory
gets very slow, taking every disk operation on the share with it.

A green test suite proves nothing until you have seen it go red, so this mutates
the fix in twelve ways and checks each is caught. It edits two files:

  pkg/models/paths/paths_scenes.go       the sharded and legacy path functions
  pkg/models/paths/generated_resolve.go  the resolver, the delete set, the prune

THREE MUTATIONS ARE ABOUT THE LEGACY FALLBACK rather than the sharding,
because that fallback is the part whose absence is silent. No sharding at all
means previews 404; no legacy fallback means every already-generated file
becomes invisible and every scene re-transcodes once, with nothing reporting an
error either way.

TWO MUTATIONS WERE FOUND TO BE UNKILLABLE BY THE FIRST VERSION OF THIS SUITE,
and both blind spots are now covered by tests named for them:

  - removing the isValidGeneratedChecksum guard. Every other test passes a valid
    checksum, so the guard was never exercised for rejecting a value that could
    not have produced a generated file. Worse, the first version of the test
    written to cover this passed anyway: it supplied a bad checksum AND a
    non-matching file name, so the SECOND guard rejected the input and the test
    proved nothing about the first. A function with two guards needs tests that
    trip one at a time.
  - dropping the sprite entries from SceneGeneratedFiles. Every other test
    iterates whatever that function returns, so removing entries from it is
    invisible by construction.

ONE MUTATION IS NOT KILLABLE AND IS NOT A GAP: deleting PruneEmptyShardDirs'
emptiness check is an equivalent mutant, proven by probing rmdir(2) directly. It
is listed in EQUIVALENT_MUTATIONS below, and the runner reports it separately
from a real blind spot.

Run:  python3 pkg/models/paths/mutate_generated_sharding.py
Exit: 0 when every mutation is either killed or proven equivalent.
"""
import re
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[3]
PATHS_PKG = Path(__file__).resolve().parent
SCENES = PATHS_PKG / "paths_scenes.go"
RESOLVE = PATHS_PKG / "generated_resolve.go"

# (file, name, old, new)
MUTATIONS = [
    (
        SCENES,
        "no sharding at all -- the pre-#2824 flat layout",
        "\tintra := fsutil.GetIntraDir(checksum, generatedDirDepth, generatedDirLength)\n"
        "\tif intra == \"\" {\n\t\treturn filepath.Join(dir, fileName)\n\t}\n"
        "\treturn filepath.Join(dir, intra, fileName)",
        "\treturn filepath.Join(dir, fileName)",
    ),
    (
        SCENES,
        "sharding uses the LAST characters instead of the first",
        "\tintra := fsutil.GetIntraDir(checksum, generatedDirDepth, generatedDirLength)",
        "\tintra := fsutil.GetIntraDir(checksum[len(checksum)-generatedDirDepth*generatedDirLength:], generatedDirDepth, generatedDirLength)",
    ),
    (
        SCENES,
        "sharding is one level instead of two",
        "const generatedDirDepth int = 2",
        "const generatedDirDepth int = 1",
    ),
    (
        SCENES,
        "the legacy path is returned for any checksum",
        '\tif !isValidGeneratedChecksum(checksum) {\n\t\treturn ""\n\t}',
        "",
    ),
    (
        SCENES,
        "a traversing file name is accepted",
        '\tif !isLegacyGeneratedFileName(checksum, fileName) {\n\t\treturn ""\n\t}',
        "",
    ),
    (
        SCENES,
        "the legacy file name is matched on suffix only, not on the checksum",
        "\t\tif fileName == checksum+suffix {",
        "\t\tif len(fileName) > len(suffix) && fileName[len(fileName)-len(suffix):] == suffix {",
    ),
    (
        RESOLVE,
        "the legacy fallback is dropped from ResolveGeneratedFile",
        "\tif fileExists(sharded) {\n\t\treturn sharded\n\t}\n\treturn legacyOrEmpty(legacy)",
        "\treturn legacyOrEmpty(legacy)",
    ),
    (
        RESOLVE,
        "the legacy path wins over the sharded one",
        "\tif fileExists(sharded) {\n\t\treturn sharded\n\t}",
        "",
    ),
    (
        RESOLVE,
        "ResolveForDelete ignores the legacy path",
        '\tif f.Legacy != "" && f.Legacy != f.Sharded && fileExists(f.Legacy) {\n\t\tout = append(out, f.Legacy)\n\t}',
        "",
    ),
    (
        RESOLVE,
        "SceneGeneratedFiles omits the sprite files",
        "\t\t{Sharded: sp.GetSpriteImageFilePath(checksum), Legacy: sp.GetLegacySpriteImageFilePath(checksum)},\n"
        "\t\t{Sharded: sp.GetSpriteVttFilePath(checksum), Legacy: sp.GetLegacySpriteVttFilePath(checksum)},",
        "",
    ),
    (
        RESOLVE,
        "SceneGeneratedFiles omits the transcode",
        "\t\t{Sharded: sp.GetTranscodePath(checksum), Legacy: sp.GetLegacyTranscodePath(checksum)},",
        "",
    ),
    (
        RESOLVE,
        "PruneEmptyShardDirs removes a directory that still has files in it "
        "[KNOWN-EQUIVALENT: unkillable, see EQUIVALENT_MUTATIONS]",
        # `entries` has to stay used or the program will not build, and a
        # mutation that does not compile is a defect in the harness, not a kill.
        "\t\tentries, err := os.ReadDir(dir)\n\t\tif err != nil || len(entries) > 0 {\n\t\t\treturn\n\t\t}",
        "\t\tentries, err := os.ReadDir(dir)\n\t\t_ = entries\n\t\tif err != nil {\n\t\t\treturn\n\t\t}",
    ),
    (
        RESOLVE,
        "PruneEmptyShardDirs can walk out past the generated root",
        "\tfor dir := intra; dir != generatedRoot && len(dir) > len(generatedRoot); {",
        "\tfor dir := intra; dir != \"/\"; {",
    ),
]


# Mutations that are provably EQUIVALENT: deleting them changes no observable
# behaviour, so no test can kill them and their surviving is not a test gap.
#
# Proven, not assumed. `PruneEmptyShardDirs`'s emptiness check is belt-and-braces
# over what the kernel already guarantees: Go's os.Remove on a directory is
# rmdir(2), and Linux returns ENOTEMPTY for a directory that still holds an
# entry. Verified directly:
#
#   os.Remove(non-empty dir) -> err = directory not empty
#   file SURVIVED
#   dir SURVIVED
#
# So with the guard deleted the loop still stops on the same directory and still
# leaves the same file in place -- the only difference is the error string. A
# test asserting on that string would be testing the OS, not this code.
#
# The guard is KEPT anyway: it makes the intent explicit at the call site and it
# avoids a pointless syscall on a directory that is obviously not empty. But it is
# not a correctness boundary, and this file says so rather than quietly counting
# it as a gap.
EQUIVALENT_MUTATIONS = {
    "PruneEmptyShardDirs removes a directory that still has files in it",
}


def run_tests() -> tuple[int, str]:
    proc = subprocess.run(
        ["go", "test", "./pkg/models/paths/", "-count=1"],
        cwd=REPO,
        capture_output=True,
        text=True,
        timeout=400,
    )
    # Go writes test output to stdout; build errors go to stderr. Reading only one
    # of them turns a test failure into "no failures found".
    return proc.returncode, proc.stdout + proc.stderr


def is_compile_error(output: str) -> bool:
    return bool(re.search(r"^pkg/.*\.go:\d+:\d+:", output, re.M) or "build failed" in output)


def main() -> int:
    originals = {p: p.read_text() for p in (SCENES, RESOLVE)}
    killed, survived, equivalent = [], [], []

    def restore() -> None:
        for path, text in originals.items():
            path.write_text(text)

    try:
        rc, out = run_tests()
        if rc != 0:
            print("baseline is RED -- fix that before reading this harness")
            print(out[:2000])
            return 1
        print("  baseline green (pkg/models/paths)\n")

        for path, name, old, new in MUTATIONS:
            base = originals[path]
            # The EQUIVALENT_MUTATIONS keys carry a "[KNOWN-EQUIVALENT: ...]"
            # suffix so the report lines up with this dict; strip it to compare.
            key = name.split(" [KNOWN-EQUIVALENT")[0]

            if old not in base:
                survived.append((name, "ANCHOR MISSING -- the fix was rewritten"))
                print(f"  NOANCHOR {name}")
                continue

            path.write_text(base.replace(old, new, 1))
            rc, out = run_tests()
            restore()

            if rc == 0:
                if key in EQUIVALENT_MUTATIONS:
                    equivalent.append(name)
                    print(f"  equivalent {name}")
                    print("             (no test can kill this; see EQUIVALENT_MUTATIONS)")
                else:
                    survived.append((name, "tests still pass -- the tests are blind here"))
                    print(f"  SURVIVED  {name}")
                continue

            if is_compile_error(out):
                survived.append((name, "compile error -- fix the mutation, not the test"))
                print(f"  COMPILE   {name}")
                continue

            tests = sorted(set(re.findall(r"--- FAIL: (\w+)", out)))
            killed.append(name)
            print(f"  killed    {name}")
            if tests:
                print(f"            by: {', '.join(tests[:2])}")
    finally:
        restore()
        for path, text in originals.items():
            if path.read_text() != text:
                raise SystemExit(f"{path} was not restored -- refusing to finish")

    print(
        f"\n  {len(killed)} killed, {len(survived)} survived, "
        f"{len(equivalent)} equivalent (provably unkillable)"
    )
    for name, why in survived:
        print(f"    - {name}: {why}")

    if any("compile error" in w for _, w in survived):
        print("\n  A mutation did not compile. That is a bug in the harness.")
        return 1
    return 1 if survived else 0


if __name__ == "__main__":
    sys.exit(main())