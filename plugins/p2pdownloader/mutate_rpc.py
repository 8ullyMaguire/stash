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

import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
SRC = os.path.join(HERE, "internal/rpc/rpc.go")
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


def run(cmd):
    return subprocess.run(cmd, shell=True, cwd=HERE, capture_output=True,
                          text=True, env=ENV)


def main():
    original = open(SRC).read()
    results = []

    for name, old, new, expect in MUTATIONS:
        if old not in original:
            results.append((name, "unscored", "the text to mutate is gone, so the "
                                               "mutation is testing nothing"))
            continue

        open(SRC, "w").write(original.replace(old, new, 1))
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
                results.append((name, "survived",
                                f"{expect} passed with the defect in place"))
        finally:
            open(SRC, "w").write(original)

    # The tree must be exactly as we found it, and green.
    if run("go build ./...").returncode != 0:
        print("FATAL: the plugin does not build after restoring every mutation")
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
    print()
    print(f"  {killed} killed, {survived} survived, {broken} broken, {unscored} unscored")
    return 1 if (survived or broken or unscored) else 0


if __name__ == "__main__":
    sys.exit(main())
