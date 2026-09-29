#!/usr/bin/env python3
"""Mutation harness for the image Referer ladder (stash#2540).

A green test suite proves nothing until you have seen it go red. This mutates
the fix in twelve ways and checks that each one is caught.

It edits TWO files, because the fix spans them:

  pkg/utils/image_http.go        the shared ladder, used by both call sites
  pkg/scraper/image_referer.go   the scraper's use of it

Both packages' tests run for every mutation. The scraper tests exercise the
shared code through getImage and the utils tests through ReadImageFromURL, so a
mutation that only one path notices is still caught -- and a mutation that only
one path notices is exactly the bug a single-path test suite would ship.

Three of the mutations exist because of things the tests were written to pin:

  - the ladder ORDER. Every other mutation is about a strategy being right; a
    reversed order would still have every strategy correct while tripling the
    requests for the common case. Only an assertion on the order catches that.
  - the "none" strategy must DELETE the Referer, not blank it. A blanked header
    is still a header, so attempt two would be a duplicate of attempt one and
    the ladder would never work -- while appearing to.
  - the request is REUSED across attempts. Stash's own scraper authenticates, so
    a ladder that rebuilt the request per attempt would fail on attempts two
    and three for the one scraper guaranteed to be affected.

Run:  python3 pkg/scraper/mutate_image_referer.py
Exit: 0 when every mutation is killed.
"""
import re
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
UTILS_SRC = Path(__file__).resolve().parents[1] / "utils" / "image_http.go"
SCRAPER_SRC = Path(__file__).resolve().parent / "image_referer.go"
TEST_PKGS = ["./pkg/scraper/", "./pkg/utils/"]
TEST_FILTER = "Referer|GetImage|Registrable|ContentType|ImageFromURL|DoWithRefererLadder"

# (which file, name, old, new, extra)
MUTATIONS = [
    (
        UTILS_SRC,
        "the ladder is removed entirely -- the pre-#2540 behaviour",
        "		resp, err := client.Do(req)\n		if err != nil {\n			return nil, nil, err\n		}",
        "		resp, err := client.Do(req)\n		if err != nil {\n			return nil, nil, err\n		}\n		if true {\n			return nil, nil, fmt.Errorf(\"http error 403 (tried referer: host, none, domain)\")\n		}",
    ),
    (
        UTILS_SRC,
        "every status is retried, not just 403",
        "		if resp.StatusCode != http.StatusForbidden {",
        "		if resp.StatusCode < 400 {",
    ),
    (
        UTILS_SRC,
        "a success does not stop the ladder",
        "		if resp.StatusCode < 400 {\n			return resp, body, nil\n		}",
        "		if resp.StatusCode < 400 {\n			_ = body\n			if keepRetrying() {\n				lastErr = fmt.Errorf(\"http error 403\")\n			} else {\n				return resp, body, nil\n			}\n		}",
        "\n\nfunc keepRetrying() bool { return true }\n",
    ),
    (
        UTILS_SRC,
        "the none strategy blanks the Referer instead of deleting it",
        '	req.Header.Del("Referer")\n	if s.Build == nil {\n		return\n	}',
        '	if s.Build == nil {\n		req.Header.Set("Referer", "")\n		return\n	}\n	req.Header.Del("Referer")',
    ),
    (
        UTILS_SRC,
        "registrableDomain ignores two-part public suffixes",
        '	if len(labels) >= 3 && twoPartSuffixes[labels[len(labels)-2]+"."+labels[len(labels)-1]] {\n		return strings.Join(labels[len(labels)-3:], ".")\n	}',
        '	if len(labels) >= 3 && twoPartSuffixes[labels[len(labels)-2]+"."+labels[len(labels)-1]] && len(labels) < 0 {\n		return strings.Join(labels[len(labels)-3:], ".")\n	}',
    ),
    (
        UTILS_SRC,
        "registrableDomain mangles an IP address into a domain",
        '	if ip := net.ParseIP(host); ip != nil {\n		return ""\n	}',
        '	if ip := net.ParseIP(host); ip != nil && false {\n		return ""\n	}',
    ),
    (
        UTILS_SRC,
        "the ladder order is reversed",
        "var RefererAttempts = []RefererStrategy{",
        'var RefererAttempts = []RefererStrategy{\n\t{Name: "none", Build: nil},\n\t{Name: "domain", Build: func(u *url.URL) string { return RegistrableDomain(u.Hostname()) + "/" }},',
    ),
    (
        UTILS_SRC,
        "a dead context is still retried",
        '		if err := ctx.Err(); err != nil {\n			if lastErr != nil {\n				return nil, nil, lastErr\n			}\n			return nil, nil, err\n		}',
        "",
    ),
    (
        UTILS_SRC,
        "a transport error is retried instead of returned",
        "		resp, err := client.Do(req)\n		if err != nil {\n			return nil, nil, err\n		}",
        "		resp, err := client.Do(req)\n		if err != nil {\n			lastErr = err\n			continue\n		}",
    ),
    (
        UTILS_SRC,
        "the error hides which referers were tried",
        '	return nil, nil, fmt.Errorf("%s (tried referer: %s)", lastErr, strings.Join(attempted, ", "))',
        "	return nil, nil, lastErr",
    ),
    (
        UTILS_SRC,
        "the content-type check stops stripping parameters",
        "	if i := strings.IndexByte(contentType, ';'); i >= 0 {\n		contentType = contentType[:i]\n	}\n	contentType = strings.ToLower(strings.TrimSpace(contentType))",
        "	contentType = contentType",
    ),
    (
        SCRAPER_SRC,
        "the scraper's request modifier is dropped",
        "	if i.requestModifier != nil {\n		i.requestModifier(req)\n	}",
        "",
    ),
    (
        SCRAPER_SRC,
        "a non-image 200 is stored as the image anyway",
        "	if !utils.IsImageContentType(contentType) {",
        "	if false {",
    ),
]


def run_tests() -> tuple[int, str]:
    proc = subprocess.run(
        ["go", "test", *TEST_PKGS, "-run", TEST_FILTER, "-count=1"],
        cwd=REPO,
        capture_output=True,
        text=True,
        timeout=560,
    )
    # Go writes test output to stdout; a build failure goes to stderr. Both
    # matter, and conflating them turns a compile error into a false "killed".
    return proc.returncode, proc.stdout + proc.stderr


def is_compile_error(output: str) -> bool:
    return bool(re.search(r"^pkg/.*\.go:\d+:\d+:", output, re.M) or "build failed" in output)


def main() -> int:
    originals = {p: p.read_text() for p in (UTILS_SRC, SCRAPER_SRC)}
    killed, survived = [], []

    def restore() -> None:
        for path, text in originals.items():
            path.write_text(text)

    try:
        # Establish the baseline first. If the suite is red before any mutation,
        # "killed" means nothing.
        rc, out = run_tests()
        if rc != 0:
            print("baseline is RED -- fix that before reading this harness")
            print(out[:2000])
            return 1
        print(f"  baseline green ({' '.join(TEST_PKGS)}, -run {TEST_FILTER})\n")

        for entry in MUTATIONS:
            src, name, old, new = entry[0], entry[1], entry[2], entry[3]
            extra = entry[4] if len(entry) > 4 else ""
            base = originals[src]

            if old not in base:
                survived.append((name, "ANCHOR MISSING -- the fix was rewritten"))
                print(f"  NOANCHOR {name}")
                continue

            src.write_text(base.replace(old, new, 1) + extra)
            rc, out = run_tests()
            restore()

            if rc == 0:
                survived.append((name, "tests still pass -- the tests are blind here"))
                print(f"  SURVIVED {name}")
                continue

            if is_compile_error(out):
                # Not a kill. The mutation did not produce a runnable program.
                survived.append((name, "compile error -- fix the mutation, not the test"))
                print(f"  COMPILE  {name}")
                continue

            tests = sorted(set(re.findall(r"--- FAIL: (\w+)", out)))
            killed.append(name)
            print(f"  killed   {name}")
            if tests:
                print(f"           by: {', '.join(tests[:3])}")
    finally:
        restore()
        for path, text in originals.items():
            if path.read_text() != text:
                raise SystemExit(f"{path} was not restored -- refusing to finish")

    print(f"\n  {len(killed)} killed, {len(survived)} survived")
    for name, why in survived:
        print(f"    - {name}: {why}")

    # A compile-error survivor is a defect in this file, not a finding about the
    # fix, and must not be reported as though it were either.
    if any("compile error" in w for _, w in survived):
        print("\n  A mutation did not compile. That is a bug in the harness.")
        return 1
    return 1 if survived else 0


if __name__ == "__main__":
    sys.exit(main())
