#!/usr/bin/env python3
"""Mutation harness for the stash#5538 local-image fix.

A green test proves nothing until you watch it go red. Each mutation below
breaks the fix in a way a careless future edit might plausibly break it, and
the harness asserts the tests CATCH it. A surviving mutation means the tests
are blind to that class of defect.

The matcher is a trust boundary -- a path that matches when it should not reads
a database record as though the user had named a valid id -- so the "must NOT
match" mutations are weighted as heavily as the "must match" ones.

Run:  python3 pkg/utils/mutate_local_image.py
"""

import subprocess
import sys
from pathlib import Path

PKG = Path(__file__).resolve().parent
TARGET = PKG / "image.go"
TEST = PKG / "image_local_test.go"

ORIGINAL = TARGET.read_text()
ORIGINAL_TEST = TEST.read_text()

RUN = [
    "TestStashImagePathsAreRecognised",
    "TestNonStashPathsAreNotResolvedLocally",
    "TestTheHostIsIgnored",
    "TestANonLocalURLIsNotHandledLocally",
    "TestALocalURLIsServedByTheResolverWithoutHTTP",
    "TestAResolverErrorPropagatesAndStaysLocal",
    "TestProcessImageInputServesAStashURLWithoutARequest",
    "TestProcessImageInputStillFetchesARemoteURL",
    "TestProcessImageInputWithANilResolver",
    "TestLocallyServedHTMLIsRejected",
    "TestABase64DataURIIsNotTreatedAsALocalPath",
]


def run_tests() -> tuple[int, str]:
    proc = subprocess.run(
        ["go", "test", "./", "-run", "|".join(RUN), "-count=1"],
        cwd=PKG,
        capture_output=True,
        text=True,
        timeout=300,
    )
    return proc.returncode, proc.stdout + proc.stderr


# Each mutation: (name, old, new, must_kill)
MUTATIONS = [
    (
        "the feature removed: never treat a URL as local",
        "\tif localResolver != nil {\n\t\tif d, local, err := ReadLocalImage(imageInput, localResolver); local {",
        "\tif false {\n\t\tif d, local, err := ReadLocalImage(imageInput, localResolver); local {",
        "the no-HTTP-request assertion",
    ),
    (
        "the guard against panicking on a suffix-only path removed",
        "\t\tif len(rest) <= len(matched)+len(suffix) {\n\t\t\treturn \"\", false\n\t\t}",
        "",
        "the /performer/image case (this one panicked)",
    ),
    (
        "a non-numeric id accepted",
        "\t\tfor _, c := range id {\n\t\t\tif c < '0' || c > '9' {\n\t\t\t\treturn \"\", false\n\t\t\t}\n\t\t}",
        "\t\t_ = id",
        "the traversal and non-numeric id cases",
    ),
    (
        "a leading zero id accepted",
        "\t\tif len(id) > 1 && id[0] == '0' {\n\t\t\treturn \"\", false\n\t\t}",
        "",
        "the leading-zero case",
    ),
    (
        "any scheme accepted, including file: and javascript:",
        "\tswitch u.Scheme {\n\tcase \"\", \"http\", \"https\":\n\tdefault:\n\t\treturn \"\", false\n\t}",
        "\t_ = u.Scheme",
        "the file: and javascript: cases",
    ),
    (
        "a query string accepted, so ?default=true resolves to a placeholder",
        "\tif u.RawQuery != \"\" || u.Fragment != \"\" {\n\t\treturn \"\", false\n\t}",
        "",
        "the query and fragment cases",
    ),
    (
        "the first route wins instead of the last",
        "\t\tif len(candidate) > len(rest) {",
        "\t\tif len(candidate) < len(rest) {",
        "the path-containing-two-routes case",
    ),
    (
        "the subpath prefix no longer stripped, breaking reverse proxies",
        "\t\ti := strings.LastIndex(path, prefix)\n\t\tif i < 0 {\n\t\t\tcontinue\n\t\t}",
        "\t\ti := strings.Index(path, prefix)\n\t\tif i != 0 {\n\t\t\tcontinue\n\t\t}",
        "the reverse-proxy subpath cases",
    ),
    (
        "a resolver error falls through to HTTP instead of propagating",
        "\tdata, err := resolve(path)\n\tif err != nil {\n\t\treturn nil, true, err\n\t}\n\treturn data, true, nil",
        "\tdata, err := resolve(path)\n\tif err != nil {\n\t\treturn nil, false, nil\n\t}\n\treturn data, true, nil",
        "the resolver-error assertion",
    ),
    (
        "a nil resolver panics instead of falling back to HTTP",
        "\tif localResolver != nil {",
        "\tlocalResolver(path)",
        "the nil-resolver fallback test",
    ),
    (
        "locally-served HTML accepted, reintroducing a stored non-image",
        "\t\t\tif err := validateImageData(d); err != nil {\n\t\t\t\treturn nil, err\n\t\t\t}\n\t\t\treturn d, nil",
        "\t\t\treturn d, nil",
        "the local-HTML rejection test",
    ),
    (
        "the base64 branch moved after the local branch",
        "\tif base64Regex.MatchString(imageInput) {\n\t\td, err := ProcessBase64Image(imageInput)\n\t\treturn d, err\n\t}\n",
        "",
        "the data-URI test",
    ),
]


def main() -> int:
    print("stash#5538 -- mutation harness\n")

    code, out = run_tests()
    if code != 0:
        print("FATAL: the tests fail on unmutated code.")
        print(out[-4000:])
        return 1
    print("  baseline: all tests pass\n")

    killed = 0
    survivors = []

    for name, old, new, why in MUTATIONS:
        if old not in ORIGINAL:
            print(f"  ERROR  {name}: anchor not found -- update the harness")
            survivors.append(name)
            continue

        TARGET.write_text(ORIGINAL.replace(old, new, 1))
        try:
            code, out = run_tests()
        finally:
            TARGET.write_text(ORIGINAL)

        if code != 0:
            killed += 1
            failed = [
                ln.strip() for ln in out.splitlines()
                if ln.strip().startswith("--- FAIL")
                or "panic:" in ln
            ]
            detail = failed[0].strip()[:80] if failed else "(non-zero exit)"
            print(f"  killed  {name}")
            print(f"          by: {detail}")
            print(f"          covers: {why}")
        else:
            survivors.append(name)
            print(f"  SURVIVED  {name}  <-- the tests are blind to this")
            print(f"          expected it to be caught by: {why}")

    print(f"\n  {killed} killed, {len(survivors)} survived")
    if survivors:
        print("\n  survivors (the tests cannot detect these):")
        for s in survivors:
            print(f"    - {s}")
        return 1

    if TARGET.read_text() != ORIGINAL or TEST.read_text() != ORIGINAL_TEST:
        print("\n  FATAL: a file was not restored")
        return 1
    print("  sources restored, harness clean")
    return 0


if __name__ == "__main__":
    sys.exit(main())
