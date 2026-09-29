#!/usr/bin/env python3
"""Mutation harness for the stash#5683 live-transcode fix.

A green test proves nothing until you watch it go red. Each mutation below
breaks the fix in a way a careless future edit might plausibly break it, and
the harness asserts the test suite CATCHES it. A surviving mutation means the
test is blind to that class of defect.

Run:  python3 pkg/ffmpeg/mutate_transcode.py
"""

import subprocess
import sys
from pathlib import Path

PKG = Path(__file__).resolve().parent
TARGET = PKG / "stream_transcode.go"

ORIGINAL = TARGET.read_text()


def run_tests() -> tuple[int, str]:
    proc = subprocess.run(
        ["go", "test", "./", "-run",
         "TestATranscodeThatProducesNothing|TestAFailedTranscodeDoesNot|"
         "TestANonEmptyResponse|TestTheBytePeeked|"
         "TestACancelledTranscode",
         "-count=1"],
        cwd=PKG,
        capture_output=True,
        text=True,
        timeout=300,
    )
    return proc.returncode, proc.stdout + proc.stderr


# Each mutation: (name, old, new, must_kill)
MUTATIONS = [
    (
        "the original defect restored: status written unconditionally",
        "firstByte := make([]byte, 1)\n\t\tn, readErr := stdout.Read(firstByte)\n\t\tif n == 0 && readErr != nil {",
        "firstByte := make([]byte, 1)\n\t\tn, readErr := 0, error(nil)\n\t\t_ = firstByte\n\t\tif false && readErr != nil {",
        "the issue's own assertion",
    ),
    (
        "the peeked byte is swallowed instead of written",
        "if n > 0 {\n\t\t\tif _, err := w.Write(firstByte[:n]); err != nil {",
        "if false {\n\t\t\tif _, err := w.Write(firstByte[:n]); err != nil {",
        "the byte-preservation test",
    ),
    (
        "the failure status downgraded to 200",
        "w.WriteHeader(http.StatusInternalServerError)",
        "w.WriteHeader(http.StatusOK)",
        "the status assertion",
    ),
    (
        "the content type set before the failure is known",
        "\t\tw.Header().Set(\"Cache-Control\", \"no-store\")\n",
        "\t\tw.Header().Set(\"Cache-Control\", \"no-store\")\n"
        "\t\tw.Header().Set(\"Content-Type\", mimeType)\n",
        "the content-type assertion",
    ),
    (
        "cancellation no longer distinguished from a real failure",
        "if sm.context.Err() != nil || r.Context().Err() != nil ||\n\t\t\t\terrors.Is(readErr, context.Canceled) {",
        "if false {",
        "the cancellation test",
    ),
    (
        "only the request context consulted, not the one ffmpeg is parented to",
        "if sm.context.Err() != nil || r.Context().Err() != nil ||\n\t\t\t\terrors.Is(readErr, context.Canceled) {",
        "if r.Context().Err() != nil ||\n\t\t\t\terrors.Is(readErr, context.Canceled) {",
        "the cancellation test",
    ),
    (
        "the remaining stream is never copied after the peek",
        "if readErr == nil {\n\t\t\t_, err = io.Copy(w, stdout)",
        "if false {\n\t\t\t_, err = io.Copy(w, stdout)",
        "the body equality assertion",
    ),
]


def main() -> int:
    print("stash#5683 -- mutation harness\n")

    code, out = run_tests()
    if code != 0:
        print("FATAL: the tests fail on unmutated code.")
        print(out[-3000:])
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
            ]
            detail = failed[0][8:].strip() if failed else "(non-zero exit)"
            print(f"  killed  {name}")
            print(f"          by: {detail}")
            print(f"          covers: {why}")
        else:
            survivors.append(name)
            print(f"  SURVIVED  {name}  <-- the test is blind to this")
            print(f"          expected it to be caught by: {why}")

    print(f"\n  {killed} killed, {len(survivors)} survived")
    if survivors:
        print("\n  survivors (the tests cannot detect these):")
        for s in survivors:
            print(f"    - {s}")
        return 1

    if TARGET.read_text() != ORIGINAL:
        print("\n  FATAL: the file was not restored")
        return 1
    print("  source restored, harness clean")
    return 0


if __name__ == "__main__":
    sys.exit(main())
