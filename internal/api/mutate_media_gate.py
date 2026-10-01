#!/usr/bin/env python3
"""Mutation-check the M4 media access gate (step 4.3).

Every mutation below is a REAL defect that would ship media to a user who has no
grant. The harness reports four outcomes and refuses to score anything else:

  killed    a named test failed -- the mutation is caught
  survived  every test passed -- the mutation is a HOLE in the suite
  broken    the mutation does not compile -- NOT a kill. The project's own rule:
            a mutation that does not build kills no test and must never be
            reported as one.
  unscored  the edit did not apply -- not a result

The survivors are the point of the exercise. Each one names a test that was
supposed to catch it, and "the test file changed" is read as suspicious rather
than as a pass.
"""

import os
import re
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.expanduser("~/code-local/go/stash")
API = os.path.join(REPO, "internal/api")

GOFLAGS = "-mod=mod"


def run(cmd, **kw):
    env = dict(os.environ)
    env["GOFLAGS"] = GOFLAGS
    return subprocess.run(cmd, shell=True, cwd=REPO, env=env,
                          capture_output=True, text=True, **kw)


# (name, file, old, new, tests that must fail)
MUTATIONS = [
    # NOTE ON THESE THREE. Deleting the call leaves the collab import unused, so
    # the mutation does not COMPILE -- and a mutation that does not build kills
    # no test. The first run of this harness scored all three as "broken" and
    # correctly refused to count them as kills. So the import is renamed to a
    # blank assignment as well: the mutation is then a real edit to working
    # code, which is the only kind that can tell you anything about the tests.
    # (The `collab.` prefix in each mutation is what makes the import unused, so
    # the replacement text below keeps the identifier used.)
    (
        "gate removed from the image middleware",
        "routes_image.go",
        "\t\tif !allowMedia(w, r, collab.TargetImage, int64(image.ID)) {\n\t\t\treturn\n\t\t}\n",
        "\t\t_ = collab.TargetImage\n",
        "TestEveryMediaRoutePassesThroughAGatedMiddleware",
    ),
    (
        "gate removed from the scene middleware",
        "routes_scene.go",
        "\t\tif !allowMedia(w, r, collab.TargetScene, int64(scene.ID)) {\n\t\t\treturn\n\t\t}\n",
        "\t\t_ = collab.TargetScene\n",
        "TestEveryMediaRoutePassesThroughAGatedMiddleware",
    ),
    (
        "gate removed from the group middleware",
        "routes_group.go",
        "\t\tif !allowMedia(w, r, collab.TargetGroup, int64(group.ID)) {\n\t\t\treturn\n\t\t}\n",
        "\t\t_ = collab.TargetGroup\n",
        "TestEveryMediaRoutePassesThroughAGatedMiddleware",
    ),
    (
        "the two hash-keyed sprite routes ungated",
        "routes_scene.go",
        "\tr.Route(\"/{sceneHash}*\", func(r chi.Router) {\n\t\tr.Use(sceneHashCtx)\n",
        "\tr.Route(\"/{sceneHash}*\", func(r chi.Router) {\n\t\t_ = sceneHashCtx\n",
        "TestTheHashKeyedSpriteRoutesActuallyUseTheMiddleware",
    ),
    (
        "the gate passes through when it has no store (fail-OPEN)",
        "stashforge_media_gate.go",
        "\t\thttp.NotFound(w, r)\n\t\treturn false\n\t}\n\tgate := mediaGate(mgr.MediaScopeStore)",
        "\t\treturn true\n\t}\n\tgate := mediaGate(mgr.MediaScopeStore)",
        "TestAllowMediaRefusesRatherThanPassesThroughWhenUnconfigured",
    ),
    (
        "the refusal answers 403 instead of 404",
        # writeMediaRefusal, not the call site. The first version of this
        # mutation targeted `http.NotFound` inside allowMedia, and after the
        # response was extracted into writeMediaRefusal the text no longer
        # existed -- so the harness reported UNSCORED, correctly: a mutation
        # that does not apply is not a result, and scoring it as a kill would
        # be the project's own forbidden move.
        "stashforge_media_gate.go",
        "\thttp.NotFound(w, r)\n}\n",
        "\thttp.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)\n}\n",
        "TestWriteMediaRefusalIsA404ThatNamesNothing",
    ),
    (
        "the refusal body explains itself",
        "stashforge_media_gate.go",
        "\thttp.NotFound(w, r)\n}\n",
        "\thttp.Error(w, \"no grant for this library\", http.StatusNotFound)\n}\n",
        "TestWriteMediaRefusalIsA404ThatNamesNothing",
    ),
]


def main():
    if not os.path.isdir(API):
        print("not a stash tree:", API)
        return 2

    results = []
    for name, fname, old, new, expect in MUTATIONS:
        path = os.path.join(API, fname)
        original = open(path).read()

        if old not in original:
            results.append((name, "unscored", "the edit did not apply: the "
                                               "text to mutate is gone, so the "
                                               "mutation is not testing anything"))
            continue

        mutated = original.replace(old, new, 1)
        open(path, "w").write(mutated)
        try:
            build = run("go build ./internal/api/")
            if build.returncode != 0:
                results.append((name, "broken",
                                "the mutation does not compile, so it kills no "
                                "test: " + (build.stderr.strip().splitlines() or [""])[-1][:120]))
                continue

            test = run("go test ./internal/api/ -count=1 -run '%s|"
                       "TestAllowMediaIsCalledFromEveryGatedMiddleware'" % expect)
            if test.returncode != 0:
                tail = [l for l in test.stdout.splitlines() if "FAIL" in l or "coverage_test" in l]
                results.append((name, "killed", (tail[0] if tail else "a named test failed")[:120]))
            else:
                results.append((name, "survived",
                                "every test passed with the gate %s. %s was "
                                "supposed to catch this and did not"
                                % ("defeated" if "removed" in name or "ungated" in name
                                   else "made permissive", expect)))
        finally:
            open(path, "w").write(original)

    # The tree must be exactly as we found it.
    if run("go build ./... ").returncode != 0:
        print("FATAL: the tree does not build after restoring every mutation")
        return 2

    print()
    width = max(len(n) for n, _, _ in results)
    killed = survived = broken = unscored = 0
    for name, verdict, detail in results:
        print(f"  {name:<{width}}  {verdict.upper()}")
        print(f"  {'':<{width}}  {detail}")
        if verdict == "killed":
            killed += 1
        elif verdict == "survived":
            survived += 1
        elif verdict == "broken":
            broken += 1
        else:
            unscored += 1
    print()
    print(f"  {killed} killed, {survived} survived, {broken} broken, {unscored} unscored")
    return 1 if (survived or broken or unscored) else 0


if __name__ == "__main__":
    sys.exit(main())
