#!/usr/bin/env python3
"""Refuse to commit while a mutation is still in the tree.

mutate_consent.py restores its mutants on the way out, but SIGKILL defeats that,
and a session that dies mid-run leaves the working tree holding a mutant that
every later test run will pass. The interrupted M4 run did exactly that.

This costs nothing and needs no test run, so it is worth having as its own gate
rather than as a step in a script someone has to remember to finish.

    python3 internal/collab/verify_tree.py    # exit 2 if anything is mutated
"""

import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent

# The files mutate_consent.py rewrites. Anything else it does not own.
GUARDED = [
    "internal/collab/consent.go",
    "internal/collab/exporter.go",
    "internal/collab/federation.go",
    "internal/collab/mode.go",
]


def main():
    # A diff against HEAD is the authority: these files are committed, so any
    # unstaged change to one of them is a suspect. (The M4 files are not yet
    # committed, so their first commit legitimately shows as added -- hence the
    # --ignored-untracked default and the explicit path list.)
    proc = subprocess.run(
        ["git", "diff", "--stat", "HEAD", "--"] + GUARDED,
        cwd=ROOT, capture_output=True, text=True,
    )
    if proc.returncode != 0:
        print(proc.stderr, file=sys.stderr)
        return 2

    changed = [ln.split("|")[0].strip() for ln in proc.stdout.splitlines() if "|" in ln]
    if not changed:
        return 0

    # A guard file may be dirty legitimately: this project has edited them
    # (mode.go is new work). What must never happen is a MUTANT -- a single
    # changed line. So report the diff and let a human or the agent judge, but
    # fail, because the common case by far is an interrupted harness.
    print(
        "REFUSING: files owned by the mutation harness are modified.\n"
        "If a mutation run was interrupted, restore first:\n"
        "    git checkout -- " + " ".join(GUARDED) + "\n"
        "If you are legitimately editing them, stage the change and commit it.\n"
        + proc.stdout,
        file=sys.stderr,
    )
    return 2


if __name__ == "__main__":
    sys.exit(main())
