"""Static pre-flight for the internal/ed2k mutation harness.

Refuses to run the harness if any probe's 'old' pattern is absent from the
file it names. A stale pattern is a silent no-op: the probe "runs", the suite
passes, and the verdict reads SURVIVED -- which sends the next reader to
look for a hole in a test when the defect is in the probe.

This is the same class of failure as the KeyError that truncated verify.go:
something about the probe list was out of sync with the code it points at.
"""
import os
import runpy
import sys
from collections import Counter

HARNESS = os.path.join(
    "/home/alvaro/code-local/go/stash/plugins/p2pdownloader",
    "internal/ed2k/mutate_ed2k.py")
REPO = "/home/alvaro/code-local/go/stash/plugins/p2pdownloader"

# Load the harness's own constants without running main().
ns = runpy.run_path(HARNESS, run_name="_preflight")
consts = {k: ns[k] for k in ("ED2K", "EHASH", "PARSE", "VERIFY")}
muts = ns["MUTATIONS"]

print(f"  {len(muts)} probes")
for f, n in sorted(Counter(m[1] for m in muts).items()):
    print(f"    {f:32} {n} probes")

stale = []
for probe in muts:
    label, rel = probe[0], probe[1]
    with open(os.path.join(REPO, rel)) as fh:
        text = fh.read()
    # A probe's old/new may be a string OR a list of strings (several
    # replacements applied as one mutation). Normalise to lists.
    old = probe[2] if isinstance(probe[2], list) else [probe[2]]
    new = probe[3] if isinstance(probe[3], list) else [probe[3]]
    if len(old) != len(new):
        stale.append((rel, label + "  [old/new count differs]"))
        continue
    for o, n in zip(old, new):
        if o not in text:
            stale.append((rel, label + f"  [pattern absent: {o[:40]!r}]"))
    # A probe MAY carry an identical pair alongside a real one: an unchanged
    # pattern used as a position anchor, so a second replacement lands
    # somewhere specific. What must not happen is ALL pairs being identical.
    if all(o == n for o, n in zip(old, new)):
        stale.append((rel, label + "  [every old == new: not a mutation]"))

if stale:
    print(f"\n  {len(stale)} MALFORMED probe(s) -- DO NOT RUN THE HARNESS:")
    for rel, label in stale:
        print(f"    {rel}: {label[:70]}")
    sys.exit(2)

print("\n  every probe's pattern is present and every mutation differs")
print(f"  originals the harness will hold: {sorted({m[1] for m in muts})}")
