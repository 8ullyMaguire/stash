#!/usr/bin/env python3
"""Mutation sweep for stash#3530 -- the window-aware sprite.

Per the skill: a harness that reports `killed` has proved nothing yet. Four verdicts,
exiting with different codes, and every one of them earned by a bug in an EARLIER version
of this harness:

  killed    the named test went red
  covered   the named test passed BUT the suite failed -- a lower layer already refused the
            input. NOT a pass; it is a claim, and it is only trustworthy because the whole
            suite is re-run with the mutation still applied.
  survived  the whole suite passed with the mutation applied. This is the verdict that
            means a hole.
  skip      the mutation did not compile or did not apply. NOT a kill. Counting these as
            kills is how a harness reports 4/4 while testing 2.

Restoration is per-mutation and verified BYTE-FOR-BYTE from an in-memory snapshot, not with
`git checkout` (which cannot restore an untracked file) and not at the end of the loop (an
interrupted sweep leaves the mutation applied, and the leftover then reads as a
pre-existing bug). Every run is bounded by a timeout.

Exit codes: 0 all resolved, 1 survivors present, 2 harness malformed.

The four mutations in docs/ISSUE-3530-sprite-spec.md are M1..M4. M5..M9 are the ones this
implementation actually introduced -- each is a real mistake found while writing it, recorded
here rather than in a comment, because a mutation that only exists in a comment is a mutation
nobody runs.
"""

import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent

# The packages under test. A mutation that a lower package refuses is `covered`, not killed,
# and the distinction only survives because the WHOLE set is run.
PKGS = ["./pkg/scene/generate/", "./internal/manager/"]

# A `-run` filter, for speed -- and CHECKED, because a filter that silently excludes a test is
# a gate that can be made to pass without being open (the #1790 harness's M7 bug).
TEST_RE = r"^(TestTheWindowedGrid|TestEveryTile|TestAnUnrangedSprite|TestAWindowedFrame|TestAWindowNarrower|TestAShortWindow|TestTheUnrangedSlowSeek|TestTheVTTCues|TestTheUnrangedCueSpacing|TestOpenEndedAndZeroStart|TestANonPositiveChunkCount|TestAnUnknownFrameRate|TestAFrameIsClamped|TestTheTileLoop|TestTheFrameLoop|TestTheVTTWriter|TestTheConstructorTiles|TestTheSlowSeekBranches|TestThePlanIsBuilt|TestTheSpriteTask|TestTheSpriteExistence|TestTheSpriteRoutes)"


def audit_test_filter():
    """Every Test in the sprite-window files must be reachable by TEST_RE."""
    files = [
        "pkg/scene/generate/sprite_window_test.go",
        "internal/manager/generator_sprite_window_wiring_test.go",
    ]
    pat = re.compile(r"^func (Test\w+)\(", re.M)
    missing = []
    for f in files:
        for name in pat.findall((REPO / f).read_text()):
            if not re.match(TEST_RE, name):
                missing.append(f"{f}:{name}")
    if missing:
        print("  !! TEST_RE does not match these tests -- add them or the harness will report "
              "a survivor the full suite would have killed:")
        for m in missing:
            print(f"       {m}")
        fail("test filter is incomplete")
    print(f"  test filter covers every test in {len(files)} sprite-window files")


# (label, file, anchor, replacement, expected test)
#
# Anchors are chosen to be UNIQUE in their file. Two of the original four used an anchor that
# also occurs in the comment above the code, so `replace(old, new, 1)` mutated the comment and
# the mutation survived twice while reporting itself as applied. `assert_anchor_unique` below
# is the fix, and it is why this harness has a `count` at all.
MUTATIONS = [
    (
        "M1: stepSize ignores the window start",
        "pkg/scene/generate/sprite_window.go",
        "return p.window.startOf() + float64(i)*p.StepSize()",
        "return float64(i) * p.StepSize()",
        "TestEveryTile|TestTheWindowedGrid",
    ),
    (
        "M2: VTT cues unshifted (spacing taken from the file)",
        "pkg/scene/generate/sprite_window.go",
        "\treturn p.StepSize()\n}",
        "\treturn float64(p.nthFrame) / p.frameRate\n}",
        "TestTheVTTCues",
    ),
    (
        "M3: slow seek unshifted (frames from the file's origin)",
        "pkg/scene/generate/sprite_window.go",
        "\tfirstFrame := int64(math.Round(p.window.startOf() * p.frameRate))\n\tstepFrame := p.windowFrames() / float64(p.chunkCount)\n\tframe := firstFrame + int64(math.Round(float64(i)*stepFrame))",
        "\tfirstFrame := int64(0)\n\tstepFrame := p.windowFrames() / float64(p.chunkCount)\n\tframe := firstFrame + int64(math.Round(float64(i)*stepFrame))",
        "TestAWindowedFrame",
    ),
    (
        "M4: the sprite key goes back to the plain hash",
        "internal/manager/task_generate_sprite.go",
        "return window, models.GeneratedChecksum(t.Scene, t.fileNamingAlgorithm)",
        "return window, t.Scene.GetHash(t.fileNamingAlgorithm)",
        "TestTheSpriteTask",
    ),
    # ---- the ones this implementation introduced, each a real mistake made while writing it ----
    (
        "M5: the grid's length is the FILE's again",
        "internal/manager/generator_sprite.go",
        "spriteDuration := window.Length(videoFile.VideoStreamDuration)",
        "spriteDuration := videoFile.VideoStreamDuration",
        "TestTheConstructorTiles",
    ),
    (
        "M6: the windowed slow-seek arm is dropped (file-level only)",
        "internal/manager/generator_sprite.go",
        "if window.Set {\n\t\tslowSeek = generate.SpriteNeedsFrameSeek(planOptions)",
        "if false {\n\t\tslowSeek = generate.SpriteNeedsFrameSeek(planOptions)",
        "TestTheSlowSeekBranches|TestAWindowNarrower",
    ),
    (
        "M7: the decision and the plan get different frame rates",
        "internal/manager/generator_sprite.go",
        "FrameRate:    generator.FrameRate,",
        "FrameRate:    videoFile.FrameRate,",
        "TestThePlanIsBuilt",
    ),
    (
        "M8: the tile loop stops reading the plan",
        "internal/manager/generator_sprite.go",
        "time := g.Plan.Time(i)",
        "time := float64(i) * (g.Info.VideoFile.VideoStreamDuration / float64(g.Info.ChunkCount))",
        "TestTheTileLoop",
    ),
    (
        "M9: the task loads the primary file through the window-less loader",
        "internal/manager/task_generate_sprite.go",
        "LoadPrimaryFileWithWindow(context.TODO(), instance.Repository.Scene)",
        "LoadPrimaryFile(context.TODO(), instance.Repository.File)",
        "TestTheSpriteTask",
    ),
    (
        "M10: the sprite routes serve the plain hash again",
        "internal/api/routes_scene.go",
        "return models.GeneratedChecksum(*scene, config.GetInstance().GetVideoFileNamingAlgorithm())",
        "return scene.GetHash(config.GetInstance().GetVideoFileNamingAlgorithm())",
        "TestTheSpriteRoutes",
    ),
    (
        "M11: the existence check recomputes the key from the file hash",
        "internal/manager/task_generate_sprite.go",
        "return !t.doesSpriteExist(sceneHash)",
        "return !t.doesSpriteExist(t.Scene.GetHash(t.fileNamingAlgorithm))",
        "TestTheSpriteExistence",
    ),
    (
        "M12: the frame loop stops reading the plan",
        "internal/manager/generator_sprite.go",
        "frame := g.Plan.Frame(i)",
        "frame := int64(math.Round(float64(i) * (float64(g.Info.VideoFile.FrameCount-1) / float64(g.Info.ChunkCount))))",
        "TestTheFrameLoop",
    ),
]


def fail(msg):
    print(f"  HARNESS MALFORMED: {msg}")
    sys.exit(2)


# Restore on the way out, whatever the reason. The per-mutation `finally` already restores after
# each run; this covers the paths it cannot -- SIGINT between the write and the finally, and the
# baseline being red (which exits before any mutation, and so restores nothing).
#
# Added because this harness WAS interrupted once mid-sweep, and the next run's baseline came back
# red because a mutation was still on disk. That failure is expensive and looks nothing like its
# cause: the wiring test reported that the source "does not contain LoadPrimaryFileWithWindow",
# when the source contained exactly that and the sweep had rewritten it under the assertion.
_SNAPSHOT: dict[str, str] = {}


def snapshot(paths):
    for p in paths:
        f = REPO / p
        if f.exists() and p not in _SNAPSHOT:
            _SNAPSHOT[p] = f.read_text()


def restore_all():
    bad = []
    for p, text in _SNAPSHOT.items():
        f = REPO / p
        if f.exists() and f.read_text() != text:
            f.write_text(text)
        if f.exists() and f.read_text() != text:
            bad.append(p)
    if bad:
        print(f"  !! could not restore: {bad}")
    return not bad


def _exit(code):
    if not restore_all():
        code = 2
    sys.exit(code)


def install_exit_hook():
    import atexit

    atexit.register(restore_all)


def assert_anchor_unique(path, anchor, label):
    n = (REPO / path).read_text().count(anchor)
    if n != 1:
        print(f"  !! anchor for {label!r} occurs {n} times in {path} -- the mutation would be "
              "ambiguous (a comment above the code is the usual culprit)")
        fail("anchor is not unique")


def run(pkgs, run_filter):
    cmd = ["go", "test", "-count=1"]
    if run_filter:
        cmd += ["-run", run_filter]
    cmd += pkgs
    p = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True, timeout=900)
    return p.returncode, p.stdout + p.stderr


def main():
    if not all((REPO / m[1]).exists() for m in MUTATIONS):
        fail("a file under mutation does not exist -- the harness names things it invented")

    # Snapshot BEFORE anything is written, and register the restore so an interrupt cannot leave
    # a mutation on disk for the next run to trip over.
    snapshot(sorted({m[1] for m in MUTATIONS}))
    install_exit_hook()

    audit_test_filter()

    print("\n=== baseline (unmutated) ===")
    rc, out = run(PKGS, TEST_RE)
    if rc != 0:
        print(out)
        fail("the baseline is RED -- a sweep on a red tree measures nothing")
    print("  green")

    results = []
    originals = {}
    for label, path, anchor, repl, expect in MUTATIONS:
        originals.setdefault(path, (REPO / path).read_text())
        original = originals[path]

        if anchor not in original:
            print(f"\nSKIP {label}\n  !! anchor not found in {path}")
            results.append((label, "SKIP", "anchor not found"))
            continue
        assert_anchor_unique(path, anchor, label)

        print(f"\n--- {label}")
        try:
            (REPO / path).write_text(original.replace(anchor, repl, 1))

            # A mutation that does not COMPILE is not a cover and not a kill -- it is an
            # invalid mutant, and counting it as either is how a harness reports 12/12 while
            # testing 10. M3 was exactly this: replacing `frame := firstFrame + ...` with
            # `frame := round(...)` left `firstFrame` declared and not used, so the suite
            # failed on a BUILD ERROR and the harness called it COVERED -- "the suite failed,
            # but not by the named test" is exactly what a compile failure looks like.
            #
            # So compile failure is SKIP, reported with the compiler's own words, and the
            # summary counts kills only out of the mutants that built.
            rc, out = run(PKGS, None)
            if "[build failed]" in out or "declared and not used" in out or "build constraints exclude" in out:
                verdict = "SKIP"
                detail = "does not compile -- an invalid mutant, not a kill"
                detail += " | " + "; ".join(
                    l for l in out.splitlines() if ".go:" in l
                )[:300]
            elif rc == 0:
                verdict, detail = "SURVIVED", "the whole suite passed with the mutation applied"
            elif re.search(expect, out) and ("FAIL" in out or "--- FAIL" in out):
                verdict, detail = "KILLED", "the named test went red"
            else:
                verdict, detail = "COVERED", "the suite failed, but not by the named test"
        except subprocess.TimeoutExpired:
            verdict, detail = "SKIP", "timed out"
        finally:
            (REPO / path).write_text(original)
            if (REPO / path).read_text() != original:
                fail(f"restore failed for {path}")
                return

        print(f"  {verdict}: {detail}")
        results.append((label, verdict, detail))

    print("\n=== summary ===")
    for v in ("SURVIVED", "SKIP", "COVERED", "KILLED"):
        rows = [r for r in results if r[1] == v]
        if rows:
            print(f"{v} ({len(rows)}):")
            for label, _, detail in rows:
                print(f"  - {label}: {detail}")

    survivors = [r for r in results if r[1] == "SURVIVED"]
    skips = [r for r in results if r[1] == "SKIP"]
    killed = sum(1 for r in results if r[1] == "KILLED")
    print(f"\nkilled {killed}/{len(results)}")
    if survivors:
        print("\nSURVIVORS -- a survivor is a hole in the tests unless the CODE is wrong. "
              "Read the diff before touching the source: if disabling the check makes the "
              "suite pass, the check was wrong, not the test blind.")
        sys.exit(1)
    if skips:
        # A skipped mutant is an UNTESTED mutant. Exiting 0 with one would print
        # "killed 11/12" and let a reader assume 12 were tried -- which is precisely the
        # number this harness exists to keep honest.
        print("\nSKIPPED MUTANTS -- each one was never actually tested. A mutant that does not "
              "apply, or does not compile, is a claim about the code that nobody checked. "
              "Fix the anchor, or the mutant, until every one is KILLED.")
        sys.exit(1)
    sys.exit(0)


if __name__ == "__main__":
    main()