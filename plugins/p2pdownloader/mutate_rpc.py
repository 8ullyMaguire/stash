#!/usr/bin/env python3
"""Mutation-check the P2P downloader plugin's RPC layer (M5 step 5.0).

Every mutation is a defect that would make the plugin start cleanly and then do
nothing, or — worse — keep running after an operator asked it to stop. Both
failure modes are silent: the host records a finished task and exits.

Four outcomes, and anything but `killed` is a hole:

  killed    the named test failed
  survived  the tests passed -- a HOLE
  broken    the mutation does not compile -- NOT a kill
  unscored  the edit did not apply -- not a result

Run from the plugin directory:  python3 mutate_rpc.py
"""

import fcntl
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
SRC = os.path.join(HERE, "internal/rpc/rpc.go")
CONSENT = os.path.join(HERE, "internal/rpc/consent.go")
ENV = {**os.environ, "GOFLAGS": "-mod=mod"}

MUTATIONS = [
    (
        "Run does not block (the host closes the client immediately)",
        "\tdefer cancel()\n",
        "\tdefer cancel()\n\tif true {\n\t\toutput.Output = nil\n\t\treturn nil\n\t}\n",
        "TestRunBlocksUntilTheTransferFinishes",
    ),
    (
        "Stop does not unblock Run (the transfer survives Stop)",
        "\t}\n\tcancel()\n\treturn nil\n}\n",
        "\t}\n\treturn nil\n}\n",
        "TestStopUnblocksRun",
    ),
    (
        "Stop declares *interface{} as its reply arg",
        "func (r *Runner) Stop(_ interface{}, _ *StopReply) error {",
        "func (r *Runner) Stop(_ interface{}, _ *interface{}) error {",
        "TestStopUnblocksRun",
    ),
    (
        "the service registers under the type name, not RPCRunner",
        'const serviceName = "RPCRunner"',
        'const serviceName = "Runner"',
        "TestTheServiceNameIsTheOneTheHostDials",
    ),
    (
        "LocatorFrom falls through to any argument",
        '\tfor _, key := range []string{"url", "locator", "magnet", "torrent"} {\n'
        '\t\tif v, ok := args[key]; ok {\n'
        '\t\t\tif s, ok := v.(string); ok && s != "" {\n'
        '\t\t\t\treturn s, nil\n'
        '\t\t\t}\n'
        '\t\t}\n'
        '\t}\n',
        '\tfor k, v := range args {\n'
        '\t\tif s, ok := v.(string); ok && s != "" {\n'
        '\t\t\t_ = k\n\t\t\treturn s, nil\n'
        '\t\t}\n'
        '\t}\n',
        "TestLocatorFromRefusesRatherThanGuessing",
    ),
    (
        "an empty locator is accepted",
        'if s, ok := v.(string); ok && s != "" {',
        'if s, ok := v.(string); ok {',
        "TestLocatorFromRefusesRatherThanGuessing",
    ),
    (
        "a refusal is returned as an RPC error instead of in Output",
        "\toutput.SetError(cause)\n\t\treturn nil\n\t}\n\toutput.SetError(err)\n\treturn nil",
        "\t\treturn cause\n\t}\n\treturn err",
        "TestARefusedLocatorIsReportedInOutputNotAsAnRPCError",
    ),
    (
        "the wire types lose the server connection",
        '\tServerConnection StashServerConnection `json:"server_connection"`',
        '\tServerConnection StashServerConnection `json:"-"`',
        "TestTheWireTypesRoundTrip",
    ),
]

# # THE CONSENT GATE (step 5.0a)
#
# The most important mutations in this file, because a plugin that fetches after
# core refuses is the failure 7.1 exists to prevent -- and it is invisible from
# core's side, where every test passes while the download happens.
#
# Each carries a "consent" tag naming the file, because these live in
# consent.go. A harness that mutates one file cannot find text in another, and
# reporting that as a result rather than as a harness bug is how a mutation
# suite ends up looking thorough while measuring nothing.
MUTATIONS += [
    (
        "a refused proposal is ignored and the transfer proceeds",
        "\tif !refusal.Allowed {",
        "\tif false {",
        "TestTheGateRefusesBeforeAnyTransfer",
        "consent",
    ),
    (
        "an unreachable core is treated as permission",
        '\tif err != nil {\n\t\t// Core did not answer. Not a permission.\n\t\treturn fmt.Errorf("%w: core did not answer, so the gate has not "+\n\t\t\t"permitted anything: %v", ErrRefused, err)\n\t}\n',
        "\tif err != nil {\n\t\t// MUTATED: an unreachable core is treated as permission.\n\t\treturn nil\n\t}\n",
        "TestAnUnreachableCoreMeansNoTransfer",
        "consent",
    ),
    (
        "a nil answer is read as consent",
        "\tif refusal == nil {\n",
        "\tif refusal == nil {\n\t\trefusal = &Refusal{Allowed: true}\n\t}\n\tif false {\n",
        "TestANilAnswerIsNotConsent",
        "consent",
    ),
    (
        "a build with no gate proceeds anyway",
        "\tif p == nil {",
        "\tif p == nil && p != nil {",
        "TestABuildWithNoGateRefusesEverything",
        "consent",
    ),
    (
        "the refusal reason from core is dropped",
        '\t\treturn fmt.Errorf("%w: %s", ErrRefused, reason)',
        '\t\treturn fmt.Errorf("%w: refused", ErrRefused)',
        "TestTheGateRefusesBeforeAnyTransfer",
        "consent",
    ),
    (
        "a file:// locator reaches the protocol handler",
        "\tif _, err := LocatorSchemeOf(locator); err != nil {",
        "\tif _, err := LocatorSchemeOf(locator); err != nil && false {",
        "TestAMalformedLocatorNeverReachesTheGate",
        "consent",
    ),
    (
        "a locator with no object is proposed anyway",
        "\tobjectID, err := ObjectIDFrom(input.Args)",
        "\tobjectID, err := int64(1), error(nil)\n\t_ = ObjectIDFrom",
        "TestNoObjectMeansNoProposal",
        "rpc",
    ),
    (
        "the gate is asked about a different locator than the one fetched",
        "\trefusal, err := p.Propose(ctx, Proposal{ObjectID: objectID, Locator: locator})",
        '\trefusal, err := p.Propose(ctx, Proposal{ObjectID: objectID, Locator: "magnet:?xt=urn:btih:other"})',
        "TestTheGateIsAskedWithTheObjectAndLocatorThatWillBeFetched",
        "consent",
    ),
    (
        "a non-200 from core is read as an answer",
        "\tif resp.StatusCode != http.StatusOK {",
        "\tif resp.StatusCode != http.StatusOK && false {",
        "TestANonOKStatusIsNotAPermission",
        "consent",
    ),
    (
        "a GraphQL error is read as an empty decision",
        "\tif len(envelope.Errors) > 0 {",
        "\tif len(envelope.Errors) > 0 && false {",
        "TestGraphQLErrorsAreSurfaced",
        "consent",
    ),
    (
        "the plugin advertises file://, which core does not accept",
        "var knownSchemes = []LocatorScheme{\n\tSchemeMagnet, SchemeHTTP, SchemeHTTPS, SchemeED2K,\n}",
        'var knownSchemes = []LocatorScheme{\n\tSchemeMagnet, SchemeHTTP, SchemeHTTPS, SchemeED2K, "file",\n}',
        "TestThePluginAndCoreAgreeOnLocatorSchemes",
        "consent",
    ),
    (
        "the plugin advertises torrent:, which its own parser refuses",
        "var knownSchemes = []LocatorScheme{\n\tSchemeMagnet, SchemeHTTP, SchemeHTTPS, SchemeED2K,\n}",
        'var knownSchemes = []LocatorScheme{\n\tSchemeMagnet, SchemeHTTP, SchemeHTTPS, SchemeED2K, "torrent",\n}',
        "TestThePluginAndCoreAgreeOnLocatorSchemes",
        "consent",
    ),
    (
        # TWO edits applied together, because one is not enough and that is the
        # whole finding.
        #
        # Removing `file:` from the named refusals alone changes nothing: the
        # value falls through to url.Parse and is refused as an unknown scheme,
        # with a different message. Accepting it in the URL switch alone changes
        # nothing either: the named-refusal prefix check fires first.
        #
        # So this harness has a category the other two do not -- a mutation that
        # needs SEVERAL edits to be a defect at all. Reported as two independent
        # mutations, both SURVIVE and the suite reports 2 holes that are not
        # holes. Reported as one mutation carrying both edits, it is killed.
        #
        # A survivor here would not have been a gap in the code, and reading it
        # as one would send someone to "fix" a refusal that is working.
        "file:// is ACCEPTED, so a downloader can read a local file",
        [
            ('\t\t{"file:", "a locator with this scheme makes a downloader read a local " +\n\t\t\t"file, which turns fetching material into reading anything the user " +\n\t\t\t"can read"},\n', ""),
            ('\tswitch strings.ToLower(parsed.Scheme) {\n\tcase "http":\n\t\treturn SchemeHTTP, nil', '\tswitch strings.ToLower(parsed.Scheme) {\n\tcase "file":\n\t\treturn SchemeHTTP, nil\n\tcase "http":'),
        ],
        "TestTheDangerousDirectionIsRefusedEvenIfCoreWouldAcceptIt",
        "consent",
    ),
    (
        "a schemeless locator is assumed to be https",
        '\tif parsed.Scheme == "" {\n'
        '\t\treturn "", fmt.Errorf("the locator %q has no scheme. A bare host or path "+\n'
        '\t\t\t"is refused rather than assumed to be https, because guessing the "+\n'
        '\t\t\t"transport is how content gets fetched over the wrong one. Write the "+\n'
        '\t\t\t"scheme explicitly: https://, magnet:, or ed2k://", value)\n'
        '\t}',
        '\tif parsed.Scheme == "" {\n'
        '\t\treturn SchemeHTTPS, nil\n'
        '\t}',
        "TestASchemelessLocatorIsRefusedRatherThanGuessed",
        "consent",
    ),
    (
        "the session cookie is not sent, so every proposal is unauthenticated",
        "\t\treq.AddCookie(h.conn.SessionCookie)",
        "\t\t_ = h.conn.SessionCookie",
        "TestTheSessionCookieIsSent",
        "consent",
    ),
]


def snapshot(path):
    """Record a file's contents so the restore can be PROVEN, not assumed."""
    SNAPSHOTS[os.path.abspath(path)] = open(path).read()


SNAPSHOTS = {}


def run(cmd):
    return subprocess.run(cmd, shell=True, cwd=HERE, capture_output=True,
                          text=True, env=ENV)


def main():
    # # LOCK THE REPO BEFORE TOUCHING IT
    #
    # These harnesses edit real files and restore them, so two of them running
    # at once corrupt each other: mutate_seam.py rewrote the plugin's exec entry
    # to /usr/bin/nothing while mutate_rpc.py was reading the same directory, and
    # the result was a test failure that looked like a code bug and a manifest
    # that stayed broken.
    #
    # The symptom is the dangerous kind -- a red in an unrelated test, with the
    # cause in a different harness's finally-block. An exclusive lock on one
    # shared file makes the second harness wait rather than interleave.
    # HERE, not ROOT: this harness lives in the plugin module, and the lock has
    # to be reachable from all three. The plugin directory is inside the repo, so
    # it is the same lock file by a different route.
    lock_path = os.path.join(HERE, ".mutation-harness.lock")
    lock = open(lock_path, "w")
    fcntl.flock(lock, fcntl.LOCK_EX)
    try:
        return run_all()
    finally:
        fcntl.flock(lock, fcntl.LOCK_UN)
        lock.close()


def run_all():
    # Snapshot every file this harness writes to, BEFORE any mutation, and
    # compare byte-for-byte at the end. mutate_seam.py learned this the hard
    # way: it restored with `git checkout`, which cannot restore an UNTRACKED
    # file, and left `interface: raw` in a manifest while reporting a clean
    # tree. Every file a mutation harness touches is work in progress, so every
    # one of them is untracked.
    for path in (SRC, CONSENT):
        snapshot(path)

    original = open(SRC).read()
    results = []

    consent_original = open(CONSENT).read()

    for entry in MUTATIONS:
        # # UNPACKED BY SHAPE, NOT BY POSITION
        #
        # A variable-width unpack (`name, old, new, expect = entry[:4]`) shifts
        # every field left by one for the multi-edit form, which has no `new`
        # string. That put the file tag where `expect` belongs and sent every
        # multi-edit mutation at rpc.go -- reported as `unscored`, with a message
        # naming missing source text, when the real fault was here. A harness
        # whose diagnostic points at the code instead of at itself costs an hour
        # of looking in exactly the wrong place.
        #
        # So: a list in the second slot means the multi-edit form, and the
        # remaining fields are read from the end, where the file tag lives.
        name = entry[0]
        if isinstance(entry[1], list):
            old, expect, tag = entry[1], entry[2], entry[3]
            new = None
        else:
            old, new, expect = entry[1], entry[2], entry[3]
            tag = entry[4] if len(entry) > 4 else None

        target = CONSENT if tag == "consent" else SRC
        original = consent_original if target is CONSENT else open(SRC).read()

        if isinstance(old, list):
            # Every edit must apply, or the mutation is not the defect it claims.
            # A partial application is the worst outcome here: it looks like a
            # real mutation and measures a different one.
            missing = [pair_old for pair_old, _ in old if pair_old not in original]
            if missing:
                results.append((name, "unscored",
                                f"{len(missing)} of {len(old)} edits did not apply; "
                                f"the first starts {missing[0][:60]!r} and "
                                f"{target} is {len(original)} bytes"))
                continue
        elif old not in original:
            results.append((name, "unscored", "the text to mutate is gone, so the "
                                               "mutation is testing nothing"))
            continue

        # A mutation may be ONE edit or a LIST of them, applied in order. The
        # list form exists for defects that need several edits to be real -- see
        # the file:// mutation below, where each half alone is a no-op.
        if isinstance(old, list):
            mutated = original
            for pair_old, pair_new in old:
                mutated = mutated.replace(pair_old, pair_new, 1)
        else:
            mutated = original.replace(old, new, 1)

        # A mutation that changes NOTHING is a harness bug, and reporting it as
        # a survivor is the worst possible outcome: a red that means nothing,
        # in a suite whose entire value is that its reds mean something.
        #
        # Four of these arrived as no-ops and were each "surviving" for a
        # different, entirely mechanical reason:
        #
        #   - adding a constant to the const block instead of to the slice the
        #     code actually reads;
        #   - renaming a map key so a refusal is never matched, but leaving the
        #     fallthrough to refuse everything anyway, so behaviour is identical;
        #   - `if x == ""` -> `if false`, which skips a branch whose fallthrough
        #     already returns the same error;
        #   - a tautology like `x == nil || x != nil`, which is always true.
        #
        # All four compile, all four leave the tests passing, and all four are
        # indistinguishable from a real hole unless the harness says so.
        if mutated == original:
            results.append((name, "no-op", "the edit changed no bytes, so this " +
                                           "mutation measures nothing"))
            continue

        open(target, "w").write(mutated)
        try:
            build = run("go build ./...")
            if build.returncode != 0:
                results.append((name, "broken",
                                "does not compile, so it kills no test: "
                                + (build.stderr.strip().splitlines() or [""])[-1][:90]))
                continue
            test = run(f"go test ./internal/rpc/ -count=1 -run {expect}")
            if test.returncode != 0:
                results.append((name, "killed", f"{expect} failed"))
            else:
                # A mutation that is a TAUTOLOGY is a harness bug, not a
                # result. `if x == nil || x != nil` is always true, so the
                # mutation leaves behaviour unchanged and the test passes for
                # the right reason -- which is a green that says nothing. Two of
                # the consent mutations were exactly this, and they were
                # reported as survivors until they were rewritten to actually
                # change what the code does.
                results.append((name, "survived",
                                f"{expect} passed with the defect in place"))
        finally:
            open(target, "w").write(original)

    # The tree must be exactly as we found it, and green.
    for path, want in SNAPSHOTS.items():
        if open(path).read() != want:
            print(f"FATAL: {path} was not restored. Every result below describes "
                  f"a tree that is not the one on disk.")
            return 2

    if run("go build ./...").returncode != 0:
        print("FATAL: the plugin does not build after restoring every mutation")
        return 2
    if run("go test ./...").returncode != 0:
        print("FATAL: the plugin does not pass its own tests after restoring")
        return 2
    if run("go test ./...").returncode != 0:
        print("FATAL: the plugin does not pass its own tests after restoring")
        return 2

    width = max(len(n) for n, _, _ in results)
    for name, verdict, detail in results:
        print(f"  {name:<{width}}  {verdict.upper():9} {detail}")

    killed = sum(1 for _, v, _ in results if v == "killed")
    survived = sum(1 for _, v, _ in results if v == "survived")
    broken = sum(1 for _, v, _ in results if v == "broken")
    unscored = sum(1 for _, v, _ in results if v == "unscored")
    noop = sum(1 for _, v, _ in results if v == "no-op")
    print()
    print(f"  {killed} killed, {survived} survived, {broken} broken, "
          f"{unscored} unscored, {noop} no-op")
    return 1 if (survived or broken or unscored or noop) else 0


if __name__ == "__main__":
    sys.exit(main())
