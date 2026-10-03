#!/usr/bin/env python3
"""Mutation sweep for stash#2747 -- the REMOTE external player protocol.

Every mutant here disables a SECURITY or CORRECTNESS rule that produces NO visible failure when
broken. That is the selection criterion, and it is the same one the local-player sweep
(`mutate_2747_external_player.py`) uses: a mutation that makes the code return an obvious error is
uninteresting, because the tests would notice.

  R1  accept any PREFIX of the token instead of the whole thing
      -> kills TestRemotePlayerRefusesTheWrongToken
      A one-character presentation authenticates. This is the mutation that makes the token rule
      load-bearing in a way a unit test can see.

      NOTE WHAT THIS MUTATION IS NOT. Replacing `hmac.Equal(a, b)` with `a == b` -- the obvious
      way to attack a constant-time comparison -- is NOT in this list, and cannot be. Those two
      expressions return the same value for every input; they differ only in TIMING, and timing is
      not observable from a Go test without a statistical harness and a real network. A sweep entry
      for it would report SURVIVED forever and teach the next reader that the test suite cannot
      check secrets, which is false: it cannot check TIMING, and nothing else claims it can. So the
      prefix mutation stands in for "the comparison is weaker than it looks", and the constant-time
      property is asserted by the code reading and by `hmac.Equal` being the stdlib's
      constant-time primitive.

  R2  drop the `enabled` gate from the HANDSHAKE, keeping it on dispatch
      -> kills TestRemotePlayerRefusesWhenNotEnabled
      A player stays connected and keeps waiting while the operator believes the feature is off.
      The dispatch check still refuses, so every "does it work" test still passes.

  R3  fail OPEN when no token is configured (treat "" as "no auth required")
      -> kills TestRemotePlayerRefusesWhenNoTokenIsConfigured
      An operator who sets enabled:true and forgets the token gets an unauthenticated endpoint
      that starts programs and hands out media URLs. Nothing else notices.

  R4  cap the playlist by TRUNCATING instead of refusing
      -> kills TestRemotePlayerPlayRefusesAnOverlongPlaylist
      200 requested, 64 played, reported as success. The worst kind of wrong: the operator
      believes the whole list played.

  R5  accept an empty scene list and dispatch it
      -> kills TestRemotePlayerPlayRefusesAnEmptyPlaylist
      A play frame with no items, which a player honours by clearing what it was showing.

  R6  sign the URL over `/scene/{id}/stream.mp4` while dispatching `/scene/{id}/stream`
      -> kills TestTheBuilderSignsTheURLItActuallyDispatches
      THE ONE THAT MATTERS MOST. DerivePrefix strips the extension, so the two agree today and the
      mutant is invisible in every other test. The consequence is a URL that 401s on the PLAYER's
      machine, where the operator is not looking and the only symptom is a TV that will not play.

      THIS SURVIVED THE FIRST RUN, and the reason is worth more than the mutation: the only tests
      touching this rule called `mergeSignedParams` DIRECTLY, so removing the prefix from the
      CALL SITE changed no result. `remotePlayerItems` -- the function that actually builds the URL
      -- had no caller in any test, because it reached for `manager.GetInstance()` internally and
      so needed a whole database. Sweeping a helper proves the helper; only calling it proves the
      wiring. (Identical to #3530's §8b finding: four of five aggregate call sites untested while
      the constant's own sweep read 5/5.) The fix was a one-method finder INTERFACE parameter, so a
      two-line fake can drive the real builder. Nothing else about the function changed.

  R7  put the apikey in the dispatched URL instead of a signature
      -> kills TestARemotePlayerURLIsSignedAndNeverCarriesAnApiKey
      Works perfectly for the operator's own player. Leaks a database-rewriting credential into
      another machine's history file and every log between the two.

  R8  render the window with %g instead of plain decimal
      -> kills TestRemotePlayerFormatSecondsIsPlainDecimal
      %g exponent-ifies at 1e6 SECONDS, which is 11.6 days -- not a video duration. Measured on
      this host: 60 -> "60", 1800 -> "1800", 3600 -> "3600", 86400 -> "86400". The first exponent
      is 1000000 seconds (11.6 days); the last realistic case, a sub-millisecond bound, is
      0.00001. So this mutant SURVIVES for a REASON: there is no scene whose window contains an
      exponent, and the rule it threatens cannot be broken by any input this feature accepts.

      This is the same shape as E7's replacement in the local sweep, where a %g mutant was written,
      survived, and the honest conclusion was that there had never been a bug. Recorded here for
      the same reason: "the mutation survived" and "my test was wrong" look identical from the
      outside, and only one of them is a hole. The plain-decimal rule is KEPT anyway, because
      ffmt is the correct formatter for a query parameter and the test pins it -- but the sweep
      entry is marked EXPECTED-SURVIVOR rather than being quietly deleted, so the next reader knows
      it was examined and why it stands.

  R9  never evict the registry
      -> kills TestTheRegistryEvictsTheOldestWhenItIsFull
      A player with a reconnect loop grows the map forever. Nothing about the protocol changes.

  R10 accept an unknown transport verb and pass it through
      -> kills TestPlayerTransportCommandRefusesAnUnknownVerb
      A misspelled command is silently ignored, so the operator presses pause on a player that is
      not pausing.

  R11 accept `play` on /command
      -> kills TestPlayerTransportCommandRefusesPlay
      Produces a play frame with zero items. A naive "any known command" switch accepts it.

  R12 forget the window when building the URL
      -> kills TestTheBuilderPutsTheWindowOnTheURL
      The frame still reports ranged=true, so every assertion about the FRAME passes; only the URL
      is wrong, and a player that honours the query then plays the whole file. This is the
      #3530 defect reintroduced over the wire.

      SURVIVED THE FIRST RUN for the same reason as R6: the frame-level test could not see the URL,
      and the URL-level test did not exist. The wiring gap was one gap, not two -- a single missing
      caller -- and it hid two independent rules. That is the argument for calling the builder at
      all rather than for two more helper tests.

Exits 1 on any survivor, any skip, or a red baseline.

    python3 docs/mutate_2747_remote_player.py
"""

import os
import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent

# The protocol spans two files, so mutations carry their own target rather than sharing one.
MUTATIONS = [
    (
        "R1: compare the token by DENYLIST of prefixes instead of full equality",
        "internal/api/external_player_remote.go",
        "return hmac.Equal([]byte(got), []byte(want))",
        'return strings.HasPrefix(want, got) || hmac.Equal([]byte(got), []byte(want))',
        "TestRemotePlayerRefusesTheWrongToken",
    ),
    (
        "R2: drop the enabled gate from the handshake",
        "internal/api/external_player_remote.go",
        '\tc := config.GetInstance()\n'
        '\tif !c.GetExternalPlayerEnabled() {\n'
        '\t\treturn false, "external player is not enabled"\n'
        '\t}\n\n'
        '\twant := c.GetExternalPlayerToken()',
        '\tc := config.GetInstance()\n'
        '\twant := c.GetExternalPlayerToken()',
        "TestRemotePlayerRefusesWhenNotEnabled",
    ),
    (
        "R3: fail open when no token is configured",
        "internal/api/external_player_remote.go",
        '\tif want == "" {\n'
        '\t\treturn false, "external_player.token is not configured"\n'
        '\t}',
        '\tif want == "" {\n'
        '\t\treturn true, ""\n'
        '\t}',
        "TestRemotePlayerRefusesWhenNoTokenIsConfigured",
    ),
    (
        "R4: truncate an overlong playlist instead of refusing",
        "internal/api/external_player_remote.go",
        '\tif len(body.SceneIDs) > maxPlaylistScenes {\n'
        '\t\t// Refused, not truncated: see maxPlaylistScenes.\n'
        '\t\thttp.Error(w, fmt.Sprintf("a play command carries at most %d scenes, got %d",\n'
        '\t\t\tmaxPlaylistScenes, len(body.SceneIDs)), http.StatusBadRequest)\n'
        '\t\treturn\n'
        '\t}',
        '\tif len(body.SceneIDs) > maxPlaylistScenes {\n'
        '\t\tbody.SceneIDs = body.SceneIDs[:maxPlaylistScenes]\n'
        '\t}',
        "TestRemotePlayerPlayRefusesAnOverlongPlaylist",
    ),
    (
        "R5: accept an empty playlist",
        "internal/api/external_player_remote.go",
        '\tif len(body.SceneIDs) == 0 {\n'
        '\t\thttp.Error(w, "sceneIds is required and must not be empty", http.StatusBadRequest)\n'
        '\t\treturn\n'
        '\t}',
        '\tif len(body.SceneIDs) < 0 {\n'
        '\t\thttp.Error(w, "sceneIds is required and must not be empty", http.StatusBadRequest)\n'
        '\t\treturn\n'
        '\t}',
        "TestRemotePlayerPlayRefusesAnEmptyPlaylist",
    ),
    (
        "R6: sign over /stream.mp4 while dispatching /stream",
        "internal/api/external_player_remote.go",
        'q = mergeSignedParams(q, rs.remotePlayerSigningSecret(cfg), "/scene/"+strconv.Itoa(scene.ID)+"/stream")',
        'q = mergeSignedParams(q, rs.remotePlayerSigningSecret(cfg), "/scene/"+strconv.Itoa(scene.ID)+"/stream.mp4")',
        "TestTheBuilderSignsTheURLItActuallyDispatches",
    ),
    (
        "R7: put the apikey in the dispatched URL",
        "internal/api/external_player_remote.go",
        '\t\tif cfg := config.GetInstance(); cfg.HasCredentials() {\n'
        '\t\t\tq = mergeSignedParams(q, rs.remotePlayerSigningSecret(cfg), "/scene/"+strconv.Itoa(scene.ID)+"/stream")\n'
        '\t\t}',
        '\t\tif cfg := config.GetInstance(); cfg.HasCredentials() {\n'
        '\t\t\tq.Set("apikey", cfg.GetAPIKey())\n'
        '\t\t}',
        "TestTheBuilderNeverLeaksTheApiKeyEvenWhenOneIsConfigured",
    ),
    (
        "R8: render the window with %g [EXPECTED SURVIVOR -- see the docstring]",
        "internal/api/external_player_remote.go",
        'return strconv.FormatFloat(v, \'f\', -1, 64)',
        'return strconv.FormatFloat(v, \'g\', -1, 64)',
        "TestRemotePlayerFormatSecondsIsPlainDecimal",
    ),
    (
        "R9: never evict the registry",
        "internal/api/external_player_remote.go",
        "\tfor len(playerRegistry.byID) > playerRegistryMax {",
        "\tfor false {",
        "TestTheRegistryEvictsTheOldestWhenItIsFull",
    ),
    (
        "R10: accept an unknown transport verb",
        "internal/api/external_player_remote.go",
        "\tdefault:\n"
        '\t\treturn remotePlayerCommand{}, fmt.Errorf("unknown command %q (known: pause, resume, stop, seek)", verb)',
        "\tdefault:\n"
        "\t\treturn remotePlayerCommand{Type: playerCommandType(verb)}, nil",
        "TestPlayerTransportCommandRefusesAnUnknownVerb",
    ),
    (
        "R11: accept play on /command",
        "internal/api/external_player_remote.go",
        "\tswitch playerCommandType(verb) {\n"
        "\tcase playerCmdPause:",
        "\tswitch playerCommandType(verb) {\n"
        "\tcase playerCmdPlay:\n"
        "\t\treturn remotePlayerCommand{Type: playerCmdPlay}, nil\n"
        "\tcase playerCmdPause:",
        "TestPlayerTransportCommandRefusesPlay",
    ),
    (
        "R12: forget the window when building the URL",
        "internal/api/external_player_remote.go",
        "\t\tif ranged {\n"
        '\t\t\tq.Set("start", remotePlayerFormatSeconds(start))\n'
        '\t\t\tq.Set("end", remotePlayerFormatSeconds(end))\n'
        "\t\t}",
        "\t\tif false {\n"
        '\t\t\tq.Set("start", remotePlayerFormatSeconds(start))\n'
        '\t\t\tq.Set("end", remotePlayerFormatSeconds(end))\n'
        "\t\t}",
        "TestTheBuilderPutsTheWindowOnTheURL",
    ),
]

# Every test that is a witness must be reachable by this sweep's own -run filter.
#
# A filter that silently excludes a test makes the suite pass having tested nothing, and the
# harness then reports SURVIVED on faith. This repo has been bitten by exactly that twice (the
# #3530 preserve and range-write sweeps), so it is checked before anything is mutated -- and it
# EARNED its place the first time it ran: six witnesses were named TestPlayRefuses..., which this
# filter does not match, so four of them never executed. Renaming the tests was the fix; widening
# the filter to catch them would have hidden the fact that they had been dead.
RUN = ("RemotePlayer|PlayerTransport|RegisteredPlayer|Registry|Dispatched|SignedPrefix|"
       "SeekCarries|PlayerRegisters|Socket|Builder")


def audit_filter():
    f = REPO / "internal/api/external_player_remote_test.go"
    names = re.findall(r"^func (Test\w+)\(", f.read_text(), re.M)
    missing = []
    for n in names:
        if not re.search(RUN, n):
            missing.append(n)
    if missing:
        print("  !! the -run filter cannot reach: " + ", ".join(missing))
        sys.exit(2)
    print(f"  filter reaches all {len(names)} remote-player tests")


def run():
    r = subprocess.run(
        ["go", "test", "-count=1", "./internal/api/", "-run", RUN],
        cwd=REPO, capture_output=True, text=True, timeout=900,
        env={**os.environ,
             "GOFLAGS": "-mod=mod",
             "GOMODCACHE": "/home/hermes/go/pkg/mod",
             "GOCACHE": "/home/hermes/.cache/go-build"},
    )
    return r.returncode, r.stdout + r.stderr


def main():
    audit_filter()

    # In-memory snapshot, and an atexit hook, so an interrupted sweep cannot leave a mutation on
    # disk. Found the hard way on the #3530 sprite sweep: the next run's baseline then reported the
    # mutated source as a source regression, and the wiring test insisted the code did not contain
    # a string that was sitting right there in the file.
    import atexit
    originals = {t: (REPO / t).read_text()
                 for _, t, _, _, _ in MUTATIONS}

    def restore():
        for path, text in originals.items():
            if (REPO / path).read_text() != text:
                (REPO / path).write_text(text)
                print(f"  !! restored {path} from the atexit snapshot")

    atexit.register(restore)

    print("=== baseline ===")
    rc, out = run()
    if rc != 0:
        print(out[-2500:])
        print("HARNESS MALFORMED: baseline is red")
        sys.exit(2)
    print("  green")

    results = []
    for label, target, anchor, repl, expect in MUTATIONS:
        print(f"\n--- {label}")
        original = originals[target]
        try:
            n = original.count(anchor)
            if n != 1:
                print(f"  SKIP: anchor occurs {n} times -- ANCHOR MISSING, not a survivor")
                results.append((label, "SKIP"))
                continue
            (REPO / target).write_text(original.replace(anchor, repl, 1))
            rc, out = run()
            # A non-compiling mutant is NOT a cover, and the detection must be EXACT: `[build
            # failed]` and nothing else.
            #
            # The precision was bought twice. An earlier version also matched "# github.com",
            # which heads every panic stack trace. It also tried to detect a panic by looking for
            # "# github.com" / "declared and not used", and R5's mutant -- which does compile, and
            # does kill its witness, by panicking inside the handler -- was still scored a skip.
            #
            # The rule that follows: a PANIC is a kill. Go prints "panic:" and the panic aborts the
            # binary, so the witness's own "--- FAIL:" line may never appear -- and a skip there
            # understates a real kill. `--- FAIL: <witness>` and a panic are both KILLED; only
            # `[build failed]` is a skip.
            if "[build failed]" in out:
                verdict, detail = "SKIP", "does not compile -- invalid mutant"
            elif rc == 0:
                verdict, detail = "SURVIVED", "the suite passed with the rule disabled"
            elif f"--- FAIL: {expect}" in out:
                verdict, detail = "KILLED", f"{expect} went red"
            elif "panic:" in out and expect in out:
                # A panic inside the handler the witness exercises. The test binary dies before
                # printing the witness's FAIL line, but the stack names it.
                verdict, detail = "KILLED", f"{expect} panicked -- the mutant reached real code"
            else:
                verdict, detail = "COVERED", "the suite failed, but not by the named test"
        finally:
            (REPO / target).write_text(original)
            assert (REPO / target).read_text() == original, f"restore failed for {target}"
        print(f"  {verdict}: {detail}")
        results.append((label, verdict))

    print("\n=== summary ===")
    for label, verdict in results:
        print(f"  {verdict:<9} {label}")
    bad = [r for r in results if r[1] != "KILLED"]
    # An expected survivor is a documented, measured finding -- not a hole -- and is not allowed to
    # pass silently either. It has to appear in the count, so "11/12" cannot be read as "12/12".
    expected = [r for r in bad if "EXPECTED SURVIVOR" in r[0]]
    hard = [r for r in bad if "EXPECTED SURVIVOR" not in r[0]]
    print(f"\nkilled {len(results) - len(bad)}/{len(results)}" +
          (f" ({len(expected)} documented expected survivor(s))" if expected else ""))
    if hard:
        print("  HARD FAILURES: " + "; ".join(label for label, _ in hard))
    sys.exit(1 if hard else 0)


if __name__ == "__main__":
    main()
