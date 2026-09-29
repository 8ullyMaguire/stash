#!/usr/bin/env python3
"""Mutation harness for the stash#5237 country-name fix.

A green check suite proves nothing until you watch it go red. Each mutation
below breaks the fix in a way a careless future edit might plausibly break it,
and the harness asserts the check script CATCHES it. A mutation that survives
means the check is blind to that class of defect, and the check is worthless.

Run:  python3 mutate_country.py
"""

import re
import shutil
import subprocess
import sys
from pathlib import Path

UI = Path("/home/alvaro/code-local/go/stash/ui/v2.5")
TARGET = UI / "src/utils/country.ts"
SCRIPT = UI / "scripts/check-country-names.mjs"

ORIGINAL = TARGET.read_text()


def run_checks() -> tuple[int, str]:
    proc = subprocess.run(
        ["node", str(SCRIPT)],
        cwd=UI,
        capture_output=True,
        text=True,
        timeout=300,
    )
    return proc.returncode, proc.stdout + proc.stderr


# Each mutation: (name, old, new, must_kill)
MUTATIONS = [
    (
        "override table emptied",
        'TW: "Taiwan",',
        '// TW removed',
        "the issue's own assertion",
    ),
    (
        "override key lowercased",
        "TW: \"Taiwan\",",
        "tw: \"Taiwan\",",
        "the casing check",
    ),
    (
        "trim removed from the lookup",
        "countryNameOverrides[iso.trim().toUpperCase()]",
        "countryNameOverrides[iso]",
        "the padding check",
    ),
    (
        "uppercase removed from the lookup",
        "countryNameOverrides[iso.trim().toUpperCase()]",
        "countryNameOverrides[iso.trim()]",
        "the casing check",
    ),
    (
        "override dropped from the dropdown path",
        "const override = overrideFor(code);\n    return { label: override ?? name, value: code };",
        "return { label: name, value: code };",
        "the single-override blast-radius check",
    ),
    (
        "override dropped from the single-lookup path",
        "const override = iso && overrideFor(iso);\n  if (override) {\n    return override;\n  }",
        "// no override here",
        "the single-lookup assertion",
    ),
    (
        "override applied to CN as well",
        'TW: "Taiwan",',
        'TW: "Taiwan",\n  CN: "China",',
        "the exactly-one-override check",
    ),
    (
        "alias mode used instead of the table",
        "Countries.getName(iso, localeCode)",
        'Countries.getName(iso, localeCode, { select: "alias" })',
        "the regression checks for KR, AX and TR",
    ),
]


def main() -> int:
    print("stash#5237 -- mutation harness\n")

    # Sanity: the unmutated code must pass, or every result below is noise.
    code, out = run_checks()
    if code != 0:
        print("FATAL: the checks fail on unmutated code.")
        print(out)
        return 1
    print("  baseline: all checks pass\n")

    killed = 0
    survivors = []

    for name, old, new, why in MUTATIONS:
        if old not in ORIGINAL:
            print(f"  ERROR  {name}: anchor not found -- update the harness")
            survivors.append(name)
            continue

        TARGET.write_text(ORIGINAL.replace(old, new, 1))
        try:
            code, out = run_checks()
        finally:
            TARGET.write_text(ORIGINAL)

        if code != 0:
            killed += 1
            # Show which check caught it -- the point is to know the failure
            # is attributed, not just that something changed.
            failed = [
                line.strip()
                for line in out.splitlines()
                if line.strip().startswith("FAIL")
            ]
            detail = failed[0][6:].strip() if failed else "(non-zero exit)"
            print(f"  killed  {name}")
            print(f"          by: {detail}")
            print(f"          covers: {why}")
        else:
            survivors.append(name)
            print(f"  SURVIVED  {name}  <-- the check is blind to this")
            print(f"          expected it to be caught by: {why}")

    print(f"\n  {killed} killed, {len(survivors)} survived")
    if survivors:
        print("\n  survivors (the check cannot detect these):")
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
