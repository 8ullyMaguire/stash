#!/usr/bin/env python3
"""Mutation gate for the #3450 relative-date resolver.

WHY A GATE AND NOT JUST TESTS
-----------------------------
The first version of this feature passed its own tests while silently excluding everything
dated today, because `getDateWhereClause` set the upper bound only for the `BETWEEN` path
and a `GreaterThan` filter stayed one-sided. The tests caught it -- but only because one
test happened to assert on the clause SHAPE. A resolver test that only checked
`ResolveRelativeDate("last 30 days")` would have been green throughout.

So this gate mutates the resolver the way a future edit would break it, and requires each
mutant to be killed. The mutants below are the specific wrong answers a plausible edit
produces, not arbitrary damage.

Run: python3 pkg/sqlite/relative_date_mutate.py
"""

import pathlib
import re
import shutil
import subprocess
import sys
import tempfile

REPO = pathlib.Path(__file__).resolve().parents[2]
SRC = REPO / "pkg" / "sqlite" / "relative_date.go"

# (file, name, original, replacement) -- each is a plausible single-line mistake.
# The target is named per mutant because the fix spans TWO files: `relative_date.go` (the
# resolver) and `sql.go` (where a resolved value becomes a WHERE fragment). An earlier
# version of this gate hardcoded the resolver and silently reported "anchor not found" for
# the two sql.go mutants, which reads like a pass and is not one.
MUTANTS = [
    (
        "relative_date.go",
        "off-by-one-day",
        "return startOfDay(now).AddDate(0, 0, -n), true",
        "return startOfDay(now).AddDate(0, 0, -(n - 1)), true",
    ),
    (
        "relative_date.go",
        "accepts-zero-span",
        "if err != nil || n < 1 {",
        "if err != nil || n < 0 {",
    ),
    # #3450 MUTATION FINDING: `accepts-zero-span` above replaced only the FIRST of the TWO
    # identical guards (the bare `last N` path), and SURVIVED -- because the day path's own
    # guard still refused n < 1 and the test that exercises it goes through that one. A
    # gate that reports "killed" while a guard goes unmutated proves nothing about the
    # guard it did not touch, so this second mutant targets the OTHER occurrence. Together
    # they cover both.
    (
        "relative_date.go",
        "accepts-zero-span-day-path",
        "if err != nil || n < 1 {",
        "if err != nil || n < 0 {",
        1,  # occurrence index
    ),
    (
        "relative_date.go",
        "treats-absolute-as-relative",
        "if _, err := models.ParseDate(s); err == nil {\n\t\treturn time.Time{}, false\n\t}",
        "if _, err := models.ParseDate(s); err != nil {\n\t\treturn time.Time{}, false\n\t}",
    ),
    (
        "relative_date.go",
        "guesses-any-phrase",
        'if m := relativeBarePattern.FindStringSubmatch(s); m != nil {',
        'if m := regexp.MustCompile(`(?i)^last\\s+(\\w+)$`).FindStringSubmatch(s); m != nil {\n'
        '\t\treturn startOfDay(now).AddDate(0, 0, -1), true\n\t}\n'
        '\tif m := relativeBarePattern.FindStringSubmatch(s); m != nil {',
    ),
    (
        "relative_date.go",
        "end-excludes-today",
        "e := startOfDay(now).AddDate(0, 0, 1).Add(-time.Second)",
        "e := startOfDay(now)",
    ),
    (
        "sql.go",
        "no-upper-bound-for-relative",
        "\t\tu := end.Format(time.RFC3339)\n\t\tupper = &u",
        "\t\tu := end.Format(time.RFC3339)\n\t\t_ = u",
    ),
    (
        "sql.go",
        "one-sided-range-not-widened",
        "if wasRelative {\n\t\t\treturn fmt.Sprintf(\"%s BETWEEN ? AND ?\", column), betweenArgs\n\t\t}\n",
        "",
        # Occurrence 0 is the GreaterThan arm; 1 is LessThan. Removing only ONE leaves the
        # other arm widening the range, so a `GreaterThan` filter is still exercised and the
        # mutant would survive for the wrong reason. Mutating the GreaterThan arm alone --
        # occurrence 0 -- leaves LessThan widened, which no test covers, so the GreaterThan
        # path is what is genuinely under test.
        0,
    ),
    (
        "relative_date.go",
        "case-sensitive-phrases",
        'case "last week":',
        'case "Last Week":',
    ),
]

# THE -run FILTER IS DERIVED FROM THE TEST FILES, not written out by hand.
#
# #3450 MUTATION FINDING: the hand-written filter omitted four of this feature's eleven
# tests, including `TestZeroAndNegativeDayCountsAreRefused` -- the exact test that pins the
# zero-span guard. Two mutants mutating that guard therefore SURVIVED, and the gate reported
# them as untested when in fact the tests existed and had simply never been run by it.
#
# A hand-maintained list of test names in a gate is a list that rots silently: a new test
# is added, the gate keeps passing, and the coverage the gate claims to be measuring is not
# the coverage that exists. Deriving it means adding a test automatically arms the gate.
TEST_FILES = ["relative_date_test.go", "relative_date_filter_test.go"]


def gate_test_pattern() -> str:
    names = []
    for fname in TEST_FILES:
        path = REPO / "pkg" / "sqlite" / fname
        for m in re.finditer(r"^func (Test\w+)", path.read_text(), re.M):
            names.append(m.group(1))
    if not names:
        raise SystemExit("FATAL: no tests found; the gate would measure nothing")
    # One -run argument, exact names, so a typo cannot silently widen or narrow the set.
    return "^(" + "|".join(sorted(names)) + ")$"


TEST_CMD = ["go", "test", "./pkg/sqlite/", "-count=1", "-run", gate_test_pattern()]

# Cross-check: the filter must actually run every test the files define. Comparing COUNTS is
# what caught the four omissions; comparing names would have been the right instinct and is
# done here so the failure names the test rather than just a number.
EXPECTED_COUNT = sum(
    len(re.findall(r"^func (Test\w+)", (REPO / "pkg" / "sqlite" / f).read_text(), re.M))
    for f in TEST_FILES
)

ENV = {"PATH": "/usr/bin:/bin:/usr/local/bin", "HOME": "/home/alvaro", "GOFLAGS": "-mod=mod",
       "GOCACHE": str(pathlib.Path.home() / ".cache" / "go-build")}


def replace_occurrence(text: str, old: str, new: str, occ: int) -> str:
    """Replace only the `occ`-th occurrence of `old`.

    NOT `str.replace(old, new, occ)`. That argument is a COUNT, not an index, so passing an
    index of 0 replaces NOTHING -- and the gate then ran the unmutated source, saw the tests
    pass, and reported all nine mutants as surviving. A gate that reports 0/9 for the wrong
    reason looks exactly like a gate that has found nine real gaps, which is worse than no
    gate: the next reader trusts the number and goes looking for phantom bugs.
    """
    idx = -1
    for _ in range(occ + 1):
        idx = text.find(old, idx + 1)
        if idx < 0:
            raise ValueError(f"occurrence {occ} of {old!r} does not exist")
    return text[:idx] + new + text[idx + len(old):]


def self_check() -> None:
    """Confirm the derived filter runs every test the files define.

    Without this the gate can silently under-measure, which is how two mutants survived a
    gate that looked like it was working. The count is compared, and the names are listed,
    so a regression here is legible rather than a single number moving.
    """
    proc = subprocess.run(TEST_CMD + ["-v"], cwd=str(REPO), capture_output=True, text=True, env=ENV)
    out = proc.stdout + proc.stderr
    ran = sorted(set(re.findall(r"^=== RUN\s+(Test\w+)", out, re.M)))
    defined = []
    for fname in TEST_FILES:
        defined += re.findall(r"^func (Test\w+)", (REPO / "pkg" / "sqlite" / fname).read_text(), re.M)
    missing = sorted(set(defined) - set(ran))
    if missing:
        print("FATAL: the gate filter omits tests that exist:")
        for m in missing:
            print("   !!", m)
        raise SystemExit(1)
    print(f"  self-check: {len(ran)}/{len(defined)} tests armed ({len(MUTANTS)} mutants)")


def run_tests() -> tuple[bool, str]:
    proc = subprocess.run(TEST_CMD + ["-v"], cwd=str(REPO), capture_output=True, text=True, env=ENV)
    out = proc.stdout + proc.stderr
    ran = len(set(re.findall(r"^=== RUN\s+Test\w+", out, re.M)))
    if ran == 0:
        print("FATAL: the -run filter matched no tests; the gate is measuring nothing.")
        return True, out
    # A `-run` pattern matching no tests exits 0. That is a vacuous pass, and a gate built
    # on a vacuous pass reports every mutant as surviving for a reason that has nothing to
    # do with the code under test.
    if "no tests to run" in out or "[no tests to run]" in out:
        print("FATAL: the -run filter matched no tests; the gate is measuring nothing.")
        return True, out
    return proc.returncode == 0, out


def main() -> int:
    self_check()
    files = {}
    for m in MUTANTS:
        fname = m[0]
        if fname not in files:
            files[fname] = (REPO / "pkg" / "sqlite" / fname).read_text()
    originals = dict(files)
    survived, errored = [], []

    try:
        for spec in MUTANTS:
            fname, name, old, new = spec[:4]
            occ = spec[4] if len(spec) > 4 else 0
            original = originals[fname]
            if old not in original:
                errored.append(f"{name}: ANCHOR NOT FOUND in {fname} -- the gate is stale, not passing")
                print(f"  ?  {name}: anchor not found in {fname} (gate needs updating)")
                continue

            (REPO / "pkg" / "sqlite" / fname).write_text(
                replace_occurrence(original, old, new, occ))
            ok, out = run_tests()
            if ok:
                survived.append(name)
                print(f"  SURVIVED  {name}")
            else:
                first = next((l for l in out.splitlines() if "--- FAIL" in l), "")
                print(f"  killed    {name}  {first.strip()[:60]}")
    finally:
        for fname, text in originals.items():
            (REPO / "pkg" / "sqlite" / fname).write_text(text)

    print()
    print(f"{len(MUTANTS) - len(survived) - len(errored)}/{len(MUTANTS)} killed")
    if survived:
        print("SURVIVING (each is either dead code or untested):")
        for n in survived:
            print(f"  {n}")
    if errored:
        for e in errored:
            print(e)
    return 1 if (survived or errored) else 0


if __name__ == "__main__":
    sys.exit(main())
