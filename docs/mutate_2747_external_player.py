#!/usr/bin/env python3
"""Mutation sweep for stash#2747 -- the external player.

Every mutant here disables a SECURITY or CORRECTNESS rule that produces NO visible failure when
broken. That is the whole selection criterion: a mutation that makes the code return an error is
uninteresting, because the tests would notice. These are the ones where the code carries on
perfectly happily while doing something wrong.

  E1  drop the absolute-path requirement for the binary
      -> kills TestExternalPlayerCommandMustBeAnAbsolutePath
      A bare `mpv` then resolves through the SERVER's PATH, so which player runs depends on how
      Stash was started rather than on the operator's config. Still works, wrong binary.

  E2  drop the exists-and-is-a-regular-file check
      -> kills TestExternalPlayerCommandMustExist
      A typo in the path becomes an opaque exec failure at launch instead of a message naming
      the file that is not there.

  E3  substitute into the joined string, then strings.Fields it
      -> kills TestExternalPlayerSplitsArgumentsWithoutAShell
      THE ONE THAT MATTERS MOST. A scene titled "A; rm -rf /" becomes five argv entries. No shell
      is involved so nothing executes, but the player is handed five files where one was meant.

  E4  split FIRST, substitute per argument (i.e. split the substituted text -- same as E3 but
      the other way round), dropping any argument that becomes empty
      -> kills TestExternalPlayerUsesAnAbsoluteBinaryPath indirectly and the whole-file case
      A `--start={start}` flag vanishes entirely for a whole-file scene, so the player silently
      does something other than what the config says.

  E5  pass through an unknown placeholder instead of erroring
      -> kills TestExternalPlayerRejectsAnUnknownPlaceholder
      `{startt}` reaches the player literally. The player then fails to open the file, and the
      operator concludes the MEDIA is broken -- the wrong place to look, from a wrong symptom.

  E6  scrub the environment by DENYLIST (drop only the known secret names)
      -> kills TestExternalPlayerScrubsTheEnvironment
      Every secret the server does not yet know about reaches a long-lived child process.

  E7  truncate the fraction off a window bound
      -> kills TestExternalPlayerFormatSecondsKeepsAFraction
      A window ending at 1799.999s becomes 1800. The clip cuts early by up to half a second, which
      a user notices and nobody can explain.

      This REPLACED a mutant I first wrote that formatted with %g on the belief it rendered 60 as
      "6e+01". It does not -- %g only exponent-ifies at extreme magnitudes, and 1e21 seconds is
      not a video duration. The mutant SURVIVED, and the honest conclusion was that there was
      never a bug: my test had asserted "no e or E anywhere", which would have FAILED on the
      legitimate 1e21 case. Recorded because "the mutation survived" and "my test was wrong"
      look identical from the outside and only one of them is a hole.

  E8  substitute 0 for an absent window bound
      -> kills TestExternalPlayerLeavesTheWindowEmptyForAWholeFileScene
      The whole-file case sends `--start=0`, which several players treat as an explicit seek to
      zero -- a full remount in some of them -- rather than as "no seek". The file is the same
      length either way, so this is invisible until a player does something surprising.

  E4  DROPPED, because it was aimed at code that was not load-bearing.

  E4 removed the `if/else` that set an emptied argument to "" before a filter dropped empty
  strings anyway. Nothing died -- not because the tests were weak, but because the branch decided
  nothing. That is a different failure from a weak test, and the sweep is what told them apart:
  a weak test hides a real bug, whereas a survivor here meant the code was already simpler than
  it looked. So the branch was deleted rather than defended, and E4 with it.

  E8 was E4's duplicate on the first run and pointed at the same dead branch, so it was rewritten
  to substitute `0` for an absent window bound -- a genuine bug. It exposed a real weakness in the
  meantime: the whole-file test asserted only that `--start=0` was ABSENT from a JOINED argv
  string, which is true whether the flag is dropped, kept empty, or replaced by zero. Asserting
  the argv (counting occurrences, checking exact length) is what separates those three.

Exits 1 on any survivor, any skip, or a red baseline.

    python3 docs/mutate_2747_external_player.py
"""

import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
TARGET = "internal/manager/config/external_player.go"
PKG = "./internal/manager/config/"
RUN = "TestExternalPlayer"

MUTATIONS = [
    (
        "E1: drop the absolute-path requirement",
        '\tif !filepath.IsAbs(bin) {\n'
        '\t\treturn cfg, false, "external_player.command must begin with an absolute path to the " +\n'
        '\t\t\t"player, not " + bin + " (a bare name resolves through the server\'s PATH)"\n'
        "\t}",
        "\t_ = filepath.IsAbs",
        "TestExternalPlayerCommandMustBeAnAbsolutePath",
    ),
    (
        "E2: drop the exists-and-is-regular-file check",
        '\tif fi, statErr := os.Stat(bin); statErr != nil || !fi.Mode().IsRegular() {\n'
        '\t\treturn cfg, false, "external_player.command: " + bin + " is not an existing file"\n'
        "\t}",
        "\t_, _ = os.Stat, bin",
        "TestExternalPlayerCommandMustExist",
    ),
    (
        "E3: substitute then strings.Fields the joined text",
        "\targv := strings.Fields(tmpl)\n"
        "\tfor i, arg := range argv {\n"
        '\t\tif !strings.Contains(arg, "{") {\n'
        "\t\t\tcontinue\n"
        "\t\t}",
        '\tvar joined strings.Builder\n'
        "\tjoined.WriteString(tmpl)\n"
        "\twhole, _ := substituteInArgument(joined.String(), filePath, title, start, end, ranged)\n"
        "\treturn strings.Fields(whole), nil\n"
        "\targv := strings.Fields(tmpl)\n"
        "\tfor i, arg := range argv {\n"
        '\t\tif !strings.Contains(arg, "{") {\n'
        "\t\t\tcontinue\n"
        "\t\t}",
        "TestExternalPlayerSplitsArgumentsWithoutAShell",
    ),
    (
        "E5: pass an unknown placeholder through",
        "\t\tif !isKnownPlayerPlaceholder(token) {\n"
        '\t\t\treturn "", &playerPlaceholderError{token: token}\n'
        "\t\t}",
        "\t\tif !isKnownPlayerPlaceholder(token) {\n"
        "\t\t\tout.WriteString(token)\n"
        "\t\t\ti += closing + 1\n"
        "\t\t\tcontinue\n"
        "\t\t}",
        "TestExternalPlayerRejectsAnUnknownPlaceholder",
    ),
    (
        "E6: scrub by denylist instead of allowlist",
        "\tkeep := make(map[string]bool, len(externalPlayerEnvAllowlist))\n"
        "\tfor _, k := range externalPlayerEnvAllowlist {\n"
        "\t\tkeep[k] = true\n"
        "\t}",
        "\tkeep := map[string]bool{\n"
        '\t\t"STASH_API_KEY": true, "DATABASE_URL": true,\n'
        "\t\t\"PATH\": true, \"HOME\": true, \"DISPLAY\": true,\n"
        "\t}",
        "TestExternalPlayerScrubsTheEnvironment",
    ),
    (
        "E7: truncate the fraction off a window bound",
        '\treturn strconv.FormatFloat(v, \'f\', -1, 64)',
        '\treturn strconv.FormatFloat(v, \'f\', 0, 64)',
        "TestExternalPlayerFormatSecondsKeepsAFraction",
    ),
    (
        "E8: substitute 0 for an absent window bound",
        "\t\tcase \"{start}\":\n"
        "\t\t\t// Empty when not ranged: see the file comment. An explicit 0 is a different signal\n"
        "\t\t\t// to a player than \"no seek\".\n"
        "\t\t\tif ranged {\n"
        "\t\t\t\tout.WriteString(formatSeconds(start))\n"
        "\t\t\t}",
        '\t\tcase "{start}":\n'
        "\t\t\tout.WriteString(formatSeconds(start))",
        "TestExternalPlayerLeavesTheWindowEmptyForAWholeFileScene",
    ),
]


def audit_filter():
    """Every witness must be reachable by the sweep's own -run filter.

    A filter that silently excludes a test makes the suite pass having tested nothing, and the
    harness then reports SURVIVED on faith. This has already bitten this repo twice.
    """
    f = REPO / "internal/manager/config/external_player_test.go"
    names = re.findall(r"^func (Test\w+)\(", f.read_text(), re.M)
    missing = [n for n in names if RUN not in n]
    if missing:
        print("  !! the -run filter cannot reach: " + ", ".join(missing))
        sys.exit(2)
    print(f"  filter reaches all {len(names)} external-player tests")


def run():
    r = subprocess.run(
        ["go", "test", "-count=1", PKG, "-run", RUN],
        cwd=REPO, capture_output=True, text=True, timeout=600,
        env={**__import__("os").environ,
             "GOFLAGS": "-mod=mod",
             "GOMODCACHE": "/home/hermes/go/pkg/mod",
             "GOCACHE": "/home/hermes/.cache/go-build"},
    )
    return r.returncode, r.stdout + r.stderr


def main():
    audit_filter()
    original = (REPO / TARGET).read_text()

    print("=== baseline ===")
    rc, out = run()
    if rc != 0:
        print(out[-2500:])
        print("HARNESS MALFORMED: baseline is red")
        sys.exit(2)
    print("  green")

    results = []
    for label, anchor, repl, expect in MUTATIONS:
        print(f"\n--- {label}")
        try:
            n = original.count(anchor)
            if n != 1:
                print(f"  SKIP: anchor occurs {n} times")
                results.append((label, "SKIP"))
                continue
            (REPO / TARGET).write_text(original.replace(anchor, repl, 1))
            rc, out = run()
            if "build failed" in out or "declared and not used" in out:
                verdict, detail = "SKIP", "does not compile -- invalid mutant"
            elif rc == 0:
                verdict, detail = "SURVIVED", "the suite passed with the rule disabled"
            elif f"--- FAIL: {expect}" in out:
                verdict, detail = "KILLED", f"{expect} went red"
            else:
                verdict, detail = "COVERED", "the suite failed, but not by the named test"
        finally:
            (REPO / TARGET).write_text(original)
            assert (REPO / TARGET).read_text() == original, f"restore failed for {TARGET}"
        print(f"  {verdict}: {detail}")
        results.append((label, verdict))

    print("\n=== summary ===")
    for label, verdict in results:
        print(f"  {verdict:<9} {label}")
    bad = [r for r in results if r[1] != "KILLED"]
    print(f"\nkilled {len(results) - len(bad)}/{len(results)}")
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()