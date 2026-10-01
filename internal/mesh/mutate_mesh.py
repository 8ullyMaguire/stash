#!/usr/bin/env python3
"""Mutation harness for the M7 mesh-and-governance packages.

The M7 gate (plan step 7.8) requires `python3 internal/mesh/mutate_mesh.py` to
report **0 survived** and exit 0. This script is that check, and it exists as a
committed artefact rather than a shell loop because a loop cannot be trusted:

  - the mutants I ran by hand reported three FALSE survivals from shell quoting,
    where the substitution silently did not apply and the harness reported
    SURVIVED for a mutation that never happened;
  - nothing recorded which mutations were supposed to exist, so the next run
    could quietly cover less ground and still print "0 survived".

So: every mutant is declared here with an anchor and a reason, the script asserts
the anchor is present before substituting (loud, not silent), restores the file
afterwards, and exits non-zero if any mutant survives or any fails to apply.

Run:  python3 internal/mesh/mutate_mesh.py [-v]
"""

from __future__ import annotations

import argparse
import os
import pathlib
import subprocess
import sys
from dataclasses import dataclass

REPO = pathlib.Path(__file__).resolve().parents[2]
GO = ["go", "test"]


@dataclass(frozen=True)
class Mutant:
    """One mutation: the FILE it lands in, an anchor, its replacement, and why.

    The file is per-mutant rather than per-package because these packages are not
    one file each: internal/discovery puts Score in rank.go and the feed ordering in
    feed.go. Assuming a single source file per package made two discovery anchors
    unfindable, and the symptom was a silent ANCHOR NOT FOUND rather than an error.
    """

    pkg: str
    anchor: str
    replacement: str
    why: str
    # file is the file WITHIN the package's directory. Defaults to the group's
    # TARGET_FILE entry.
    file: str | None = None

    @property
    def test_target(self) -> str:
        """The ./path form go test wants.

        `self.pkg` is the IMPORT path (internal/rank), while `go test` is given a
        filesystem-relative path (./internal/rank). Getting this wrong produced
        "FAIL ./rank [setup failed]" ten times, which reads like a broken build
        rather than a broken argument.
        """
        return f"./{self.pkg}"


# --- internal/mesh: the claim guard ------------------------------------------

MESH_MUTANTS = [
    # internal/mesh has NO behavioural mutants, deliberately. The package's rule is a
    # NAMING rule -- accessors like ClaimStoreBytesOrZero must not be named like an
    # authority -- and a naming rule cannot be mutated: an earlier entry here
    # rewrote the accessor body to `return 0` and SURVIVED, because no test
    # distinguishes a body that returns the field from one that returns 0, and
    # because the constraint is on the NAME, which mutation does not touch.
    #
    # The naming rule is enforced instead by internal/mesh/claim_guard_test.go,
    # which parses the package's own source. A mutant that cannot be killed by any
    # behavioural test should not be listed here pretending otherwise.
]

# --- internal/collab: the §6a.10 reputation/access firewall -------------------

COLLAB_MUTANTS = [
    Mutant(
        pkg="internal/collab",
        anchor="\tif ceiling < requested {",
        replacement="\tif false {",
        why="§6a.12: an operator's threshold is a CEILING. Without this a Steward "
            "passes on a Registered-ceiling instance (5 >= 1) and the ceiling does "
            "not restrain the users it exists to restrain",
    ),
    Mutant(
        pkg="internal/collab",
        anchor="\tif requested >= LevelArchivist && !consented {",
        replacement="\tif e.Level >= LevelArchivist && !consented {",
        why="consent must follow the REQUEST. Keyed on the earned level, an "
            "Archivist browsing the directory is prompted about an unrelated "
            "consent",
    ),
    Mutant(
        pkg="internal/collab",
        anchor="\tif identSolves >= 5 && approvedEdits >= 5 {",
        replacement="\tif identSolves >= 5 {",
        why="§6a.10: a user excellent at identification must not thereby reach "
            "Archivist. Identification is not curation",
    ),
    Mutant(
        pkg="internal/collab",
        anchor="\tif requested >= LevelArchivist && !consented {",
        replacement="\tif false {",
        why="the consent gate removed entirely -- level 4 would enable content "
            "viewing on its own",
    ),
    Mutant(
        pkg="internal/collab",
        anchor="\treturn Earned{Level: LevelPublic}\n}",
        replacement="\treturn Earned{Level: LevelRegistered}\n}",
        why="the no-record floor. Registered would give every new account vote "
            "and edit rights",
    ),
]

# --- internal/preservation: opt-out and manifest verification ----------------

PRESERVATION_MUTANTS = [
    Mutant(
        pkg="internal/preservation",
        anchor="\tif !IsReplicationSubject(s) {",
        replacement="\tif false && !IsReplicationSubject(s) {",
        why="non-negotiable #7: opt-out is a HARD STOP in the publish path, and "
            "replication is a publish path",
    ),
    Mutant(
        pkg="internal/preservation",
        anchor="\tif !r.ManifestVerified {",
        replacement="\tif false {",
        why="§6a.2: a replica is healthy only after MANIFEST verification, never "
            "after the peer says it accepted the bytes",
    ),
    Mutant(
        pkg="internal/preservation",
        anchor="\tif r.VerifiedAt == 0 {",
        replacement="\tif false {",
        why="a 'verified' row with no timestamp is the shape of something that "
            "heard the peer say yes",
    ),
    Mutant(
        pkg="internal/preservation",
        anchor="\t\t// Wait for verification rather than re-placing.\n\t\treturn false",
        replacement="\t\treturn true",
        why="pending is verifying, not broken. Treating it as broken re-places a "
            "correct peer's replica forever",
    ),
    Mutant(
        pkg="internal/preservation",
        anchor="if p.InstanceID != \"\" && p.AcceptsReplicas {",
        replacement="if p.InstanceID != \"\" {",
        why="§6a.9: an instance that must not hold a replica refuses it, and "
            "refusing is not a partial success",
    ),
    Mutant(
        pkg="internal/preservation",
        anchor="space = float64(p.ClaimedFreeBytes) / float64(max)",
        replacement="space = float64(p.ClaimedFreeBytes) / 1024",
        why="free bytes must be a RATIO so taste is not swamped by a "
            "multi-terabyte claim -- the bug that inverted placement",
    ),
]

# --- internal/rank: two pools, and the normalised vote scale ------------------

RANK_MUTANTS = [
    Mutant(
        pkg="internal/rank",
        anchor="\treturn float64(c.Present+c.Cleared) / float64(c.Expected), nil",
        replacement="\treturn float64(c.Present) / float64(c.Expected), nil",
        why="§6a.15: absent is distinct from empty, so a CLEARED field is "
            "complete. Counting only Present leaves curated entities unfinishable",
    ),
    Mutant(
        pkg="internal/rank",
        anchor="\t\treturn 1.0, nil",
        replacement="\t\treturn 0.0, nil",
        why="an entity with nothing expected is COMPLETE. 0% for a fully-curated "
            "entity looks like a bug to every user who sees it",
    ),
    Mutant(
        pkg="internal/rank",
        anchor="\treturn float64(score) / 100.0",
        replacement="\treturn float64(score)",
        why="the 0..100 UI scale must be mapped onto Elo's 0..1, or one vote "
            "swings a rating by 3200 and ordering depends on who voted first",
    ),
    Mutant(
        pkg="internal/rank",
        anchor="rated[v.Target] = target + K*(actual-Expected(target, rater))",
        replacement="rated[v.Target] = target + K*(actual-Expected(rater, target))",
        why="THE TWO-POOL BUG. Each side must use its OWN expectation. Sharing one "
            "conflates 'how well a voter predicts' with 'how well an entity is "
            "received', and the ranking comes out INVERTED",
    ),
    Mutant(
        pkg="internal/rank",
        anchor="\t\trated[v.Target] = DefaultRating",
        replacement="\t\trated[v.Target] = DefaultRating\n\t\trated[v.VoterID] = DefaultRating",
        why="only RECEIVED entities belong in the rated pool. A voter-only name in "
            "there is an entity that received nothing, so a voter who never has "
            "anything said about them is ranked as though everybody did",
    ),
]

# --- internal/discovery: the view, the local/peer firewall, the feed ----------

DISCOVERY_MUTANTS = [
    Mutant(
        pkg="internal/discovery",
        anchor="\treturn w.Taste*taste + w.Gravity*gravity + w.Peer*peerTerm",
        replacement="\treturn w.Taste*taste + w.Peer*peerTerm",
        why="§6a.8 gravity is a term; without it the slider does nothing",
    ),
    Mutant(
        pkg="internal/discovery",
        file="rank.go",
        anchor="\tif c.Origin != \"\" {\n\t\tpeerTerm = peer[c.Origin]\n\t}",
        replacement="\tpeerTerm = peer[c.Origin]",
        why="§6a.6: a LOCAL record must not pick up a peer term, or a remote "
            "input steers the ranking of local records",
    ),
    Mutant(
        pkg="internal/discovery",
        anchor="\tfor _, c := range candidates {\n\t\tif c.ID == wantID {",
        replacement="\tfor _, c := range candidates {\n\t\tif true || c.ID == wantID {",
        why="§6a.5 / #6: an explicit request returns that entity. Encoding it as a "
            "score term makes it beatable",
    ),
    Mutant(
        pkg="internal/discovery",
        anchor="\t\t\t// Weight becomes presence. See the doc comment.\n\t\t\tout[k] = 1",
        replacement="\t\t\tout[k] = m[k]",
        why="§6a.3: a peer fingerprint's MAGNITUDES are dropped, not clamped -- a "
            "clamp is a number the peer chose",
    ),
    Mutant(
        pkg="internal/discovery",
        file="feed.go",
        anchor="\t\t\tif got[i].Rank != got[j].Rank {",
        replacement="\t\t\tif false {",
        why="the feed preserves each surface's own order within its section",
    ),
]

# --- internal/ident: the board cannot bypass governance ----------------------

IDENT_MUTANTS = [
    Mutant(
        pkg="internal/ident",
        anchor="\tif s.ResolverID == 0 {",
        replacement="\tif false {",
        why="a solve with no identifiable author cannot be reviewed",
    ),
    Mutant(
        pkg="internal/ident",
        anchor="\tif s.Query.Value == nil && len(s.Query.Evidence) == 0 {",
        replacement="\tif false {",
        why="a proposal asserting nothing is noise in the governance queue",
    ),
    Mutant(
        pkg="internal/ident",
        anchor="Rationale: renderRationale(s.Query),",
        replacement='Rationale: "identified via board",',
        why="evidence must reach the reviewer as rationale, or it is unavailable "
            "at the moment it is needed",
    ),
    Mutant(
        pkg="internal/ident",
        anchor="OldValue: s.Query.CurrentValue,",
        replacement="OldValue: nil,",
        why="§5.3: a proposal carries old and new so a reviewer sees the diff",
    ),
]

# --- internal/gamify: the streak view and the badge firewall -----------------

GAMIFY_MUTANTS = [
    Mutant(
        pkg="internal/gamify",
        anchor="\tcase ActionEditRejected:\n\t\treturn 0",
        replacement="\tcase ActionEditRejected:\n\t\treturn 2",
        why="a rejection is a decision AGAINST a contribution; paying for it "
            "builds an incentive to spam proposals",
    ),
    Mutant(
        pkg="internal/gamify",
        anchor="\tif gap != 0 && gap != DAY {",
        replacement="\tif gap != 0 {",
        why="yesterday still counts -- the day is not over. Otherwise every "
            "streak zeroes each morning",
    ),
    Mutant(
        pkg="internal/gamify",
        anchor="\t\tu := t.UTC()\n\t\treturn time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)",
        replacement="\t\treturn time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())",
        why="a streak day must be UTC, or a streak follows the SERVER's timezone "
            "and every user gains or loses a day when the instance moves",
    ),
    Mutant(
        pkg="internal/gamify",
        anchor="\t\tneed := LevelBase * level",
        replacement="\t\tneed := LevelBase",
        why="level bands widen, so a later level is not twenty times the work of "
            "level two",
    ),
]

# --- internal/curate: counts in the view, and the scene filter ---------------

CURATE_MUTANTS = [
    Mutant(
        pkg="internal/curate",
        anchor="\t\tif l.PerformerID == 0 {\n\t\t\tcontinue\n\t\t}",
        replacement="\t\tif false {\n\t\t\tcontinue\n\t\t}",
        why="a source that never resolved to a performer is not a link",
    ),
    Mutant(
        pkg="internal/curate",
        anchor="return c.Confirmed >= PerformerTarget",
        replacement="return c.Performers >= PerformerTarget",
        why="§6a.15 wants metadata present AND verified; counting an "
            "unconfirmed scrape lets an importer fill a curator's progress bar",
    ),
    Mutant(
        pkg="internal/curate",
        anchor="\t\tif l.SceneID != sceneID {\n\t\t\tcontinue\n\t\t}\n\t\tif l.PerformerID < 0 {",
        replacement="\t\tif false {\n\t\t\tcontinue\n\t\t}\n\t\tif l.PerformerID < 0 {",
        why="the scene filter must live in the function, or a caller passing a "
            "mixed list gets one confident tally for the wrong scene",
    ),
    Mutant(
        pkg="internal/curate",
        anchor="\t\tif s.SceneID != sceneID {\n\t\t\tcontinue\n\t\t}",
        replacement="\t\tif false {\n\t\t\tcontinue\n\t\t}",
        why="same, for snapshots",
    ),
    Mutant(
        pkg="internal/curate",
        anchor="const SnapshotTarget = 1",
        replacement="const SnapshotTarget = 3",
        why="one sufficiently detailed collage serves identification; a target of "
            "3 leaves every scene permanently at 2/3",
    ),
]

# --- internal/ecosystem: the write-through and the consent gate --------------

ECOSYSTEM_MUTANTS = [
    Mutant(
        pkg="internal/ecosystem",
        anchor="\tif !Publishable(share) {",
        replacement="\tif false {",
        why="§6a.21: an opted-out entity is neither indexed nor reachable",
    ),
    Mutant(
        pkg="internal/ecosystem",
        anchor="\tif !published {",
        replacement="\tif false {",
        why="§6a.21: a page for an entity the owner has not published does not "
            "get created",
    ),
    Mutant(
        pkg="internal/ecosystem",
        anchor="return share == collab.ChoiceOptedIn",
        replacement="return share != collab.ChoiceOptedOut",
        why="publishing must fail CLOSED: an unrecognised share choice must not "
            "default to sharing somebody's library",
    ),
    Mutant(
        pkg="internal/ecosystem",
        anchor="\tif q.Limit > MaxPublicLimit {\n\t\tq.Limit = MaxPublicLimit\n\t}",
        replacement="",
        why="a public read must be bounded here rather than in each transport",
    ),
]

# --- internal/directory: a badge is confirmed, never granted -----------------

DIRECTORY_MUTANTS = [
    Mutant(
        pkg="internal/directory",
        anchor="\tif confirmer == c.ClaimedBy {",
        replacement="\tif false {",
        why="§6a.4: no operator grants a badge. ConfirmedBy cannot equal "
            "ClaimedBy, and that inequality IS the mechanism",
    ),
    Mutant(
        pkg="internal/directory",
        anchor="func ConveysTrust(s BadgeState) bool { return s == BadgeVerified }",
        replacement="func ConveysTrust(s BadgeState) bool {\n\treturn s == BadgeVerified || s == BadgePending\n}",
        why="claimed is NOT verified; collapsing them is how an unverified claim "
            "becomes a trust signal",
    ),
    Mutant(
        pkg="internal/directory",
        anchor="\tif !trusted {\n\t\t// Not an error: a non-trusted user is simply not the person whose\n\t\t// confirmation counts. Erroring would make a UI offer them an action that\n\t\t// always fails, which teaches users that the button is broken.\n\t\treturn c, nil\n\t}",
        replacement="\tif false {\n\t\treturn c, nil\n\t}",
        why="an untrusted user cannot advance a claim",
    ),
    Mutant(
        pkg="internal/directory",
        anchor="\tif r.Rating < 0 || r.Rating > 100 {",
        replacement="\tif false {",
        why="a review rating feeds the Elo pool, where 900 is an unbeatable "
            "player rather than a failed save",
    ),
]

ALL_GROUPS = {
    "mesh": (REPO / "internal/mesh", MESH_MUTANTS),
    "collab": (REPO / "internal/collab", COLLAB_MUTANTS),
    "preservation": (REPO / "internal/preservation", PRESERVATION_MUTANTS),
    "rank": (REPO / "internal/rank", RANK_MUTANTS),
    "discovery": (REPO / "internal/discovery", DISCOVERY_MUTANTS),
    "ident": (REPO / "internal/ident", IDENT_MUTANTS),
    "gamify": (REPO / "internal/gamify", GAMIFY_MUTANTS),
    "curate": (REPO / "internal/curate", CURATE_MUTANTS),
    "ecosystem": (REPO / "internal/ecosystem", ECOSYSTEM_MUTANTS),
    "directory": (REPO / "internal/directory", DIRECTORY_MUTANTS),
}


# The source file each group's mutants land in. Named explicitly because these file
# names do NOT follow the package name (internal/rank/rank.go yes,
# internal/collab/access_level.go no), and deriving it produced ten "no source file"
# failures on the first run.
TARGET_FILE = {
    "mesh": "protocol.go",
    "collab": "access_level.go",
    "preservation": "preserve.go",
    "rank": "rank.go",
    "discovery": "rank.go",
    "ident": "board.go",
    "gamify": "gamify.go",
    "curate": "coverage.go",
    "ecosystem": "ecosystem.go",
    "directory": "directory.go",
}


def target_file(pkg_dir: pathlib.Path, group: str) -> pathlib.Path:
    """The non-test source file for a group -- mutants land there, not in tests."""
    return pkg_dir / TARGET_FILE[group]


def run_tests(pkg: str) -> bool:
    """Run a package's tests. Returns True when green.

    The environment is INHERITED and only GOFLAGS is added. My first version built a
    fresh env from scratch, which dropped GOCACHE -- so every baseline came out RED
    for want of a build cache, and the harness would have reported 'baseline is
    RED, results are meaningless' for all ten packages instead of running anything.
    """
    env = dict(os.environ)
    env["GOFLAGS"] = "-mod=mod"
    env["PATH"] = _go_bin()
    proc = subprocess.run(
        GO + [pkg],
        cwd=REPO,
        capture_output=True,
        text=True,
        env=env,
    )
    return proc.returncode == 0


def _go_bin() -> str:
    """Prepend GOROOT/bin to PATH.

    thinkcentre's system gofmt is 1.22 while the toolchain is 1.25, and `go test`
    shells out to gofmt for some vet checks -- so the wrapper matters there. The
    value is split on os.pathsep and each entry stripped: a newline left inside one
    PATH entry makes every subprocess invocation fail, which is what produced ten
    "baseline is RED" results that had nothing to do with the code.
    """
    proc = subprocess.run(
        ["go", "env", "GOROOT"], capture_output=True, text=True, cwd=REPO
    )
    goroot = proc.stdout.strip()
    entries = [e for e in os.environ.get("PATH", "").split(os.pathsep) if e]
    if goroot:
        entries.insert(0, os.path.join(goroot, "bin"))
    return os.pathsep.join(entries)


def _diagnose(pkg: str) -> str:
    """Run a package's tests and return its combined output, for error reporting.

    A harness that says "baseline is RED" without saying WHY is the same defect as
    a shell loop reporting a survival it never measured: the next reader cannot
    tell a real red from a broken harness.
    """
    env = dict(os.environ)
    env["GOFLAGS"] = "-mod=mod"
    env["PATH"] = _go_bin()
    proc = subprocess.run(GO + [pkg], cwd=REPO, capture_output=True, text=True, env=env)
    return (proc.stdout + proc.stderr).strip()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("-v", "--verbose", action="store_true")
    args = ap.parse_args()

    survived: list[str] = []
    did_not_apply: list[str] = []
    total = 0

    for group, (pkg_dir, mutants) in ALL_GROUPS.items():
        # Baseline: the package must be green before any mutation, or "0 survived"
        # means nothing.
        if not run_tests(f"./internal/{group}"):
            out = _diagnose(f"./internal/{group}")
            first = out.splitlines()[0] if out else "(no output)"
            did_not_apply.append(f"{group}: baseline is RED -- {first}")
            continue

        # Group by target file, so a package with mutants in two files restores both.
        by_file: dict[pathlib.Path, list[tuple[int, Mutant]]] = {}
        for i, m in enumerate(mutants):
            src = pkg_dir / (m.file or TARGET_FILE[group])
            if not src.exists():
                did_not_apply.append(f"{group}/{i}: no file at {src}")
                continue
            by_file.setdefault(src, []).append((i, m))

        for src, entries in by_file.items():
            original = src.read_text()

            for i, m in entries:
                total += 1
                label = f"{group}/{i} [{src.name}]: {m.why.splitlines()[0][:64]}"

                if m.anchor not in original:
                    # LOUD. A substitution that silently does not apply is how the
                    # hand-written shell loop reported three false survivals.
                    did_not_apply.append(f"{label} -- ANCHOR NOT FOUND")
                    continue

                src.write_text(original.replace(m.anchor, m.replacement, 1))
                try:
                    green = run_tests(m.test_target)
                finally:
                    src.write_text(original)

                if green:
                    survived.append(label)
                    print(f"  SURVIVED  {label}")
                elif args.verbose:
                    print(f"  killed    {label}")

            # Confirm the restore, byte for byte. A harness that leaves a file
            # mutated is worse than no harness.
            if src.read_text() != original:
                did_not_apply.append(
                    f"{group}: {src.name} was not restored byte-identically"
                )
                src.write_text(original)

    print()
    print(f"mutants run: {total}")
    print(f"survived:    {len(survived)}")
    if did_not_apply:
        print(f"DID NOT APPLY: {len(did_not_apply)}")
        for d in did_not_apply:
            print(f"  ! {d}")

    if survived or did_not_apply:
        print("\nFAIL")
        return 1
    print("OK: 0 survived")
    return 0


if __name__ == "__main__":
    sys.exit(main())