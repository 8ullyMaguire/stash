#!/usr/bin/env python3
"""Mutation harness for stash#5850 -- thumbnails dropping transparency.

The report is that line art with transparency gets black thumbnails, and the
diagnosis (in a now-deleted maintainer comment) was that thumbnails are encoded
as .jpg, which has no alpha channel. A deleted comment is a hypothesis, so the
first thing this fix did was MEASURE it against the real vips binary:

    current  .jpg  -> 3 bands, transparent pixel reads "56 0 0"     (opaque)
    proposed .webp -> 4 bands, transparent pixel reads "211 20 20 0" (alpha kept)

The defect was in three independent places, and a format change is exactly the
kind of fix where one of them gets missed and the tests still go green:

  pkg/image/vips.go                       the vips output specifier
  pkg/ffmpeg/transcoder/image.go          the ffmpeg ENCODER, set independently
                                          of the output muxer -- webp out, mjpeg
                                          encode, still no alpha
  pkg/models/paths/paths_generated.go     the on-disk extension, which
                                          http.ServeFile turns into Content-Type

And a consequence that is easy to leave behind: the .jpg thumbnails already on
disk become orphans, deletable only if a path still derives the old name.

Run:  python3 pkg/image/mutate_thumbnail_alpha.py
Exit: 0 when every mutation is killed or proven equivalent.
"""
import os
import re
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
PKG = Path(__file__).resolve().parent

VIPS = PKG / "vips.go"
TRANSCODER = REPO / "pkg" / "ffmpeg" / "transcoder" / "image.go"
PATHS = REPO / "pkg" / "models" / "paths" / "paths_generated.go"
DELETE = PKG / "delete.go"
SCAN = REPO / "pkg" / "image" / "scan.go"
TASK = REPO / "internal" / "manager" / "task_generate_image_thumbnail.go"

# Mutations whose guarded branch is UNREACHABLE, with the reason. An exempt row
# is not a kill and not a hole: the mutation survives because no test can drive
# the code, not because the tests are blind. Counting it either way is a lie --
# as a kill it overstates the suite, as a survivor it sends the next reader into
# a branch that cannot be reached.
#
# Shape: (file, name, old, new, package, why-unreachable)
EXEMPT = [
    (
        PATHS,
        "RemoveLegacyThumbnail deletes the CURRENT thumbnail -- the orphan fix becomes data loss",
        "\tif legacy == gp.GetThumbnailPath(checksum, width) {",
        "\tif false {",
        "pkg/models/paths",
        "the branch is unreachable: thumbnailExt is a const (\".webp\") and "
        "GetLegacyThumbnailPath hardcodes \".jpg\", so the two paths can never be "
        "equal. The guard defends against a FUTURE edit that converges them, and "
        "no test can reach it today. Pinned instead by "
        "TestTheTwoPathsDifferOnlyInExtension, which fails if the extensions ever "
        "do converge.",
    ),
]

# (file, name, old, new, test packages to run)
MUTATIONS = [
    (
        VIPS,
        "vips writes JPEG again -- the reported bug, verbatim",
        'const thumbnailOutputSuffix = ".webp[Q=70,strip]"',
        'const thumbnailOutputSuffix = ".jpg[Q=70,strip]"',
        "pkg/image",
    ),
    (
        VIPS,
        "vips writes PNG -- has alpha, so the alpha test passes, but the on-disk"
        " name and the encoder now disagree",
        'const thumbnailOutputSuffix = ".webp[Q=70,strip]"',
        'const thumbnailOutputSuffix = ".png[Q=70,strip]"',
        "pkg/image",
    ),
    (
        TRANSCODER,
        "the ffmpeg encoder goes back to hardcoded mjpeg -- webp container, JPEG"
        " data, alpha dropped, and the muxer still says webp",
        "\t\tVideoCodec(ImageCodecFor(options.OutputFormat))",
        "\t\tVideoCodec(ffmpeg.VideoCodecMJpeg)",
        "pkg/ffmpeg/transcoder",
    ),
    (
        TRANSCODER,
        "ImageCodecFor returns jpeg for everything",
        "\tcase ffmpeg.ImageFormatWebp:\n\t\treturn ffmpeg.VideoCodecLibWebP",
        "\tcase ffmpeg.ImageFormatWebp:\n\t\treturn ffmpeg.VideoCodecMJpeg",
        "pkg/ffmpeg/transcoder",
    ),
    (
        PATHS,
        "the thumbnail is written as .jpg again -- the payload would be correct"
        " and http.ServeFile would still call it image/jpeg",
        'const thumbnailExt = ".webp"',
        'const thumbnailExt = ".jpg"',
        "pkg/models/paths",
    ),
    (
        PATHS,
        "the legacy path is dropped, so a pre-upgrade .jpg is orphaned forever",
        '\tfname := fmt.Sprintf("%s_%d.jpg", checksum, width)\n'
        '\treturn filepath.Join(gp.Thumbnails, fsutil.GetIntraDir(checksum, '
        "thumbDirDepth, thumbDirLength), fname)\n}",
        "\t// no legacy path\n\treturn gp.GetThumbnailPath(checksum, width)\n}",
        "pkg/models/paths",
    ),
    (
        PATHS,
        "RemoveLegacyThumbnail always reports a removal, whether or not it"
        " removed anything",
        "\tif err := os.Remove(legacy); err != nil {\n\t\treturn false\n\t}\n\treturn true",
        "\t_ = os.Remove(legacy)\n\treturn true",
        "pkg/models/paths",
    ),
    (
        DELETE,
        "deleting an image no longer cleans up the pre-upgrade .jpg thumbnail",
        "\tlegacyThumb := d.Paths.Generated.GetLegacyThumbnailPath(image.Checksum, models.DefaultGthumbWidth)\n"
        "\tif legacyThumb != thumbPath {\n\t\texists, _ = fsutil.FileExists(legacyThumb)\n"
        "\t\tif exists {\n\t\t\tfiles = append(files, legacyThumb)\n\t\t}\n\t}",
        "\t// legacy thumbnail left behind",
        "pkg/image",
    ),
    (
        SCAN,
        "a checksum change no longer clears the pre-upgrade .jpg thumbnail",
        "\t_ = os.Remove(h.Paths.Generated.GetLegacyThumbnailPath(oldHash, models.DefaultGthumbWidth))",
        "\t// not removed",
        "pkg/image",
    ),
    (
        TASK,
        "regenerating a thumbnail leaves the pre-upgrade .jpg in place",
        "\tmgr.Paths.Generated.RemoveLegacyThumbnail(t.Image.Checksum, models.DefaultGthumbWidth)",
        "\t// left behind",
        # internal/manager, NOT pkg/image. The first version of this row said
        # "pkg/image", and pkg/image does not import internal/manager
        # (`go list -deps ./pkg/image | grep -c internal/manager` -> 0), so the
        # mutated file was never compiled by the command that judged it. The
        # mutation reported SURVIVED -- which reads as a hole in the tests and
        # sent this at the tests for an hour before the scope was checked.
        # A mutation in a package the run does not build is `unscored`, not
        # `survived`.
        "internal/manager",
    ),
]


def run_tests(pkg: str) -> tuple[int, str]:
    # GOFLAGS=-mod=mod because go.mod/go.sum are mid-edit in this tree and the
    # build would otherwise fail on a stale module graph, which is_compile_error
    # would then score as a KILL of every mutation. A harness that reports 7
    # kills while the build is broken is lying, and it reports them as kills
    # rather than as skips, which is the exact inversion SKIP exists to prevent.
    env = dict(os.environ, GOFLAGS="-mod=mod")
    proc = subprocess.run(
        ["go", "test", "./" + pkg + "/", "-count=1"],
        cwd=REPO,
        capture_output=True,
        text=True,
        timeout=500,
        env=env,
    )
    # go writes test output to stdout and build errors to stderr; reading only
    # one turns a build failure into "no failures found".
    return proc.returncode, proc.stdout + proc.stderr


# A build-error detector that is incomplete reports its own defect as a hole: a
# mutation that does not compile is scored `broken`, which is not a kill, and if
# the shape is missing from this list the harness calls it a kill instead. Every
# form below has actually been seen in this repo.
_BUILD_ERRORS = (
    "build failed",
    "cannot use",
    "undefined:",
    "declared and not used",
    "syntax error",
    "not enough arguments",
    "too many arguments",
    "assignment mismatch",
    "redeclared",
    "no new variables",
    "imported and not used",
    "missing return",
    "all declarations of",
    "shadows declaration",
    "assignment to",
)


def is_compile_error(out: str) -> bool:
    if re.search(r"^\S+\.go:\d+:\d+:", out, re.M):
        return True
    return any(marker in out for marker in _BUILD_ERRORS)


def main() -> int:
    originals = {p: p.read_text() for p in {m[0] for m in MUTATIONS}}
    killed, survived, broken, unscored, exempt = [], [], [], [], []

    def restore() -> None:
        for path, text in originals.items():
            path.write_text(text)

    try:
        # Exempt rows are verified, not assumed: the anchor must be present in
        # the CURRENT source, and the run must be green, or the exemption is
        # recorded as a failure rather than as a pass.
        for path, name, old, new, pkg, why in EXEMPT:
            if old not in path.read_text():
                print(f"  EXEMPT-ANCHOR-MISSING {name} -- the exemption is stale")
                return 2
            rc, _ = run_tests(pkg)
            if rc != 0:
                print(f"  EXEMPT-BASELINE-RED {name} -- fix that first")
                return 2
            exempt.append((name, why))
            print(f"  exempt    {name}")
            print(f"            unreachable, not untested: {why[:90]}...")

        for pkg in sorted({m[4] for m in MUTATIONS}):
            rc, out = run_tests(pkg)
            if rc != 0:
                print(f"baseline RED in {pkg} -- fix that first")
                print(out[:2000])
                return 1
        print("  baseline green (pkg/image, pkg/ffmpeg/transcoder, pkg/models/paths)\n")

        for path, name, old, new, pkg in MUTATIONS:
            # Re-read from disk every probe, and check the anchor against THAT.
            # Three of this harness's rows in an earlier pass scored as "survived"
            # because a probe named a test that had been renamed: the anchor was
            # gone and a missing anchor reads identically to a blind test.
            base = path.read_text()
            if old not in base:
                survived.append((name, "ANCHOR MISSING -- the fix was rewritten"))
                print(f"  NOANCHOR {name}")
                continue

            # SCOPE CHECK. Before scoring anything, confirm the package
            # holding the mutation is one the run will build. This is the
            # "check its scope before trusting its output" rule: a test that
            # fails on everything the first time has a wrong SCOPE, and a
            # mutation in a package the run never compiles is not a survivor.
            pkg_dir = path.parent
            try:
                pkg_dir.relative_to(REPO)
            except ValueError:
                unscored.append((name, "file is outside the repo"))
                print(f"  UNSCORED {name}")
                continue
            if pkg_dir != REPO / pkg:
                unscored.append(
                    (name, f"mutation is in {pkg_dir.relative_to(REPO)} but the run "
                           f"builds {pkg} -- the file is never compiled"))
                print(f"  UNSCORED {name}  (wrong package: {pkg})")
                continue

            path.write_text(base.replace(old, new, 1))
            try:
                rc, out = run_tests(pkg)
            finally:
                # Restore PER PROBE. Restoring only at the end means an
                # interrupted sweep leaves the mutation applied and the next
                # run inherits it as a pre-existing failure.
                path.write_text(base)

            if rc == 0:
                survived.append((name, "tests still pass -- the tests are blind here"))
                print(f"  SURVIVED  {name}")
                continue
            if is_compile_error(out):
                broken.append(name)
                print(f"  COMPILE   {name}  <- fix the mutation, not the test")
                continue

            tests = sorted(set(re.findall(r"--- FAIL: (\w+)", out)))
            killed.append(name)
            print(f"  killed    {name}")
            if tests:
                print(f"            by: {', '.join(tests[:2])}")
    finally:
        restore()
        for path, text in originals.items():
            # Compare against what is on disk now, not against a snapshot taken
            # before the loop: a restore that cannot fail is not a restore.
            if path.read_text() != text:
                raise SystemExit(f"{path} was not restored -- refusing to finish")
            print(f"  restored {path.relative_to(REPO)}")

    print(f"\n  {len(killed)} killed, {len(survived)} survived, "
          f"{len(broken)} build-broken, {len(unscored)} unscored, "
          f"{len(exempt)} exempt")
    for name, why in survived:
        print(f"    SURVIVED  {name}: {why}")
    for name in broken:
        print(f"    BROKEN    {name}: did not compile")
    for name, why in unscored:
        print(f"    UNSCORED  {name}: {why}")
    for name, why in exempt:
        print(f"    EXEMPT    {name}: {why}")

    # Separate exit codes, so a caller can tell "go look at a test" from "go look
    # at this file". A single non-zero conflates a real hole with a harness bug,
    # and the fix is opposite in the two cases.
    if unscored or broken:
        print("\n  A probe was malformed or did not compile. That is a bug in the"
              "\n  harness, not a gap in the tests -- fix the probe.")
        return 2
    if survived:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
