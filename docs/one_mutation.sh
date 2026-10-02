#!/bin/bash
# Diagnose ONE mutation: apply it, run the suite, restore, report which tests failed.
#
# Separate from mutate_external_id.py so a single probe can be run in the background where
# a 300s tool timeout cannot kill a Python process mid-restore -- which is how a mutation
# got left applied in the tree earlier.
#
# usage: one_mutation.sh <n>
set -u
cd /home/alvaro/code-local/go/stash
export GOFLAGS=-mod=mod
N="$1"

python3 - "$N" <<'PY'
import importlib.util, pathlib, sys, subprocess, os
spec = importlib.util.spec_from_file_location("m", "docs/mutate_external_id.py")
m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)

label, path, edits, expect = m.MUTATIONS[int(sys.argv[1])]
print(f"PROBE {sys.argv[1]}: {label}\n  file={path}\n  expect={expect}", flush=True)

mutated, original, err = m.apply_edits(path, edits)
if err:
    print("ANCHOR PROBLEM:", err); sys.exit(2)
print("  mutation APPLIED", flush=True)
try:
    rc, out = m.run(["go", "test", "-tags", "integration", m.PKG, "-count=1", "-timeout", "480s"])
    fails = [l for l in out.splitlines() if l.startswith("--- FAIL")]
    print(f"\n  exit={rc}  failing tests={len(fails)}", flush=True)
    for f in fails: print("   ", f)
    print(f"\n  named test {expect!r} among them: "
          f"{any(expect in f for f in fails)}", flush=True)
    if rc == 0:
        print("\n  THE MUTATION SURVIVED THE WHOLE SUITE.", flush=True)
    print("\n--- tail ---", flush=True)
    print("\n".join(out.splitlines()[-12:]))
finally:
    (m.REPO / path).write_text(original)
    ok = (m.REPO / path).read_text() == original
    print(f"\nRESTORED {path}: byte-identical={ok}", flush=True)
    sys.exit(0 if ok else 2)
PY
