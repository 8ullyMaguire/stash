#!/usr/bin/env python3
# MUTATION GATE -- stash#2359 follow-up, the #422 studio association
# =============================================================================
# Six mutants. Each injects a plausible regression into the real tree, runs the real integration
# tests, and requires a FAIL. A survivor is a hole in the tests, not a pass.
#
# WHY THE SUITE NEEDS ITS OWN GATE
# ================================
# The existing `docs/e2e/mutation-check.sh` covers the BROWSER suite and cannot run Go integration
# tests. These are SQLite-level behaviours -- a SET NULL cascade, a preserved column across a
# clear-then-insert -- and nothing in the e2e suite can observe them. So the gate lives here.
#
# EVERY MUTANT REFUSES TO RUN IF ITS ANCHOR IS MISSING
# =====================================================
# A no-op mutant reported as "the tests do not catch this" is the most expensive kind of test bug:
# it looks exactly like a hole in the suite, and it sends you to strengthen the tests when the real
# fix is here. (That happened on this repo already: an anchor that did not exist produced a no-op
# mutant that was then reported as a survivor.)
import pathlib
import shutil
import subprocess
import sys
import tempfile

REPO = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else ".").resolve()

GO_ENV = {
    "GOFLAGS": "-mod=mod",
    "GOTMPDIR": "/home/hermes/work/.gotmp",
    "TMPDIR": "/home/hermes/work/.verify-tmp",
    "PATH": "/home/alvaro/.config/node_modules/bin:" + __import__("os").environ["PATH"],
}
# A LIST, not a string to be .split(). The first version was one string containing shell quotes, and
# `.split()` broke the quoted -run pattern into separate argv entries -- so `go test` ran with NO -run
# filter at all... which sounds harmless and was not: it meant every package ran, and a mutant could
# be reported SURVIVED on the strength of an unrelated package's result. Three of six mutants were
# reported survivors for exactly this reason, and each one was killed the moment the argv was fixed.
#
# Kept as a real argv list so there is nothing left to mis-tokenise.
TEST_ARGS = [
    "-tags", "integration",
    "./pkg/sqlite/",
    "-run", "TestPerformerAliasStudioAssociation|TestPerformerAliasOwnership|"
            "TestPerformerNationalities|"
            "TestDestroyingAPerformerLeavesNoRowsBehind|TestEveryPerformerReferencingTableIsCovered",
    "-count=1",
]

MUTANTS = {}


def mutant(name):
    def deco(fn):
        MUTANTS[name] = fn
        return fn
    return deco


def edit(path, old, new):
    p = REPO / path
    if not p.exists():
        sys.exit(f"ANCHOR MISSING: {path} does not exist")
    s = p.read_text()
    if s.count(old) < 1:
        sys.exit(f"ANCHOR MISSING: {path} no longer contains: {old[:80]!r}")
    p.write_text(s.replace(old, new, 1))
    print(f"  mutated {path}")


# ---------------------------------------------------------------------------- M1: the data-loss mutant
@mutant("m1-writer-drops-studio-id")
def m1():
    """SetAliasOwner omits studio_id from the INSERT.

    THE ONE THAT MATTERS MOST. SetAliasOwner is clear-then-insert, so a writer that forgets the
    column leaves the alias present and the association GONE -- silently, on any re-save of that
    alias for an unrelated reason. Nothing else in the suite would notice: the alias still reads
    back, the performer still has the alias, and the loss is only visible to someone who knew the
    studio was there.
    """
    edit("pkg/sqlite/stashbox_parity.go",
         'Cols("performer_id", "alias", "owner_performer_id", "studio_id").\n'
         '\t\tVals(goqu.Vals{ownership.PerformerID, ownership.Alias, ownership.OwnerPerformerID, ownership.StudioID})',
         'Cols("performer_id", "alias", "owner_performer_id").\n'
         '\t\tVals(goqu.Vals{ownership.PerformerID, ownership.Alias, ownership.OwnerPerformerID})')


# ---------------------------------------------------------------------------- M2: SET NULL -> CASCADE
@mutant("m2-set-null-becomes-cascade")
def m2():
    """ON DELETE SET NULL becomes ON DELETE CASCADE in the migration.

    The subtle one. CASCADE DELETES THE ALIAS ROW when the studio goes, which is a real loss: the
    alias is still a true alias of that performer, it has merely lost the studio it was associated
    with. A test that only checked "the studio_id is gone afterwards" would pass under both; only
    asserting the ALIAS SURVIVES distinguishes them.
    """
    edit("pkg/sqlite/migrations/126_alias_studio_association.up.sql",
         "ADD COLUMN `studio_id` integer REFERENCES `studios`(`id`) ON DELETE SET NULL;",
         "ADD COLUMN `studio_id` integer REFERENCES `studios`(`id`) ON DELETE CASCADE;")


# ---------------------------------------------------------------------------- M3: NOT NULL
@mutant("m3-studio-id-not-null")
def m3():
    """studio_id becomes NOT NULL, so an unassociated alias cannot exist.

    Upstream's example contains `"Janey": ""` -- "not associated with any studio or website" -- and
    that is the COMMON case. NOT NULL turns the feature into one that only works for aliases which
    happen to have a studio, which is the minority of them.
    """
    edit("pkg/sqlite/migrations/126_alias_studio_association.up.sql",
         "ADD COLUMN `studio_id` integer REFERENCES `studios`(`id`) ON DELETE SET NULL;",
         "ADD COLUMN `studio_id` integer not null default 0 REFERENCES `studios`(`id`) ON DELETE SET NULL;")


# ---------------------------------------------------------------------------- M4: no FK at all
@mutant("m4-studio-id-has-no-foreign-key")
def m4():
    """The REFERENCES clause is dropped, so a deleted studio leaves a DANGLING id.

    The worst of the four referential cases and the easiest to ship by accident, because nothing
    errors at write time: studio_id is a plain integer and the value is accepted. The damage appears
    later, when a reader follows the id to a studio that is gone.
    """
    edit("pkg/sqlite/migrations/126_alias_studio_association.up.sql",
         "ADD COLUMN `studio_id` integer REFERENCES `studios`(`id`) ON DELETE SET NULL;",
         "ADD COLUMN `studio_id` integer;")


# ---------------------------------------------------------------------------- M5: the unique index widened
@mutant("m5-unique-index-widened-to-include-studio")
def m5():
    """The unique index on (performer_id, alias) is replaced by one including studio_id.

    This is the tempting "fix" for upstream's remark about "duplicate aliases per studio": permit the
    duplicate so users can type what they like. It trades a clear rejection for a silent ambiguity --
    two rows for one alias whose studio is then indeterminate, which is the defect #2341 was filed
    about. The two-performers-one-studio case still passes under this mutant, which is exactly why
    the rejection needs its own assertion.
    """
    edit("pkg/sqlite/migrations/125_stashbox_parity_performers_tags.up.sql",
         "  ON `performer_alias_owners` (`performer_id`, `alias`);",
         "  ON `performer_alias_owners` (`performer_id`, `alias`, `studio_id`);")


# ---------------------------------------------------------------------------- M6: the model field dropped
@mutant("m6-model-drops-studio-field")
def m6():
    """StudioID is removed from the model struct.

    The layer-boundary mutant: the column exists, the writer persists it, the database is correct,
    and every READER silently loses it because the struct has nowhere to put it. The round-trip test
    is what catches this -- a column-count assertion would not.
    """
    edit("pkg/models/stashbox_parity.go",
         "\tStudioID *int `db:\"studio_id\" json:\"studio_id\"`",
         "\t// MUTANT: studio_id removed from the model")


# ---------------------------------------------------------------------------- M7: migration 127 seeds nothing
@mutant("m7-completion-migration-is-empty")
def m7():
    """Migration 127's INSERT is removed, leaving the reference list at its original 107 rows.

    Nothing else notices. The table is not empty, `allNationalities` returns a healthy list, and the
    UI renders a populated dropdown -- it is just missing eight countries, and the only symptom is a
    performer whose nationality cannot be recorded. `require.NotEmpty` on the list passes; only
    asserting the eight names catches it.
    """
    f = REPO / "pkg/sqlite/migrations/127_nationality_reference_completion.up.sql"
    if not f.exists():
        sys.exit("ANCHOR MISSING: 127_nationality_reference_completion.up.sql does not exist")
    s = f.read_text()
    if "Armenian" not in s:
        sys.exit("ANCHOR MISSING: 127 no longer seeds 'Armenian'")
    f.write_text(s.replace("INSERT INTO `nationalities`", "-- MUTANT: the completion INSERT is removed\n-- INSERT INTO `nationalities`", 1))


# ---------------------------------------------------------------------------- M8: the duplicate name
@mutant("m8-croat-is-seeded-too")
def m8():
    """'Croat' is added alongside 'Croatian', giving HR two names.

    The temptation migration 127's own comment argues against: someone probing the list for gaps
    finds HR has no 'Croat' and adds it. Nothing errors -- SQLite has no uniqueness on `code` and
    would not on `name` either without a further index -- so the list grows a second name for one
    country and the exact ambiguity #1922 exists to remove comes back.
    """
    f = REPO / "pkg/sqlite/migrations/127_nationality_reference_completion.up.sql"
    s = f.read_text()
    if "Montenegrin" not in s:
        sys.exit("ANCHOR MISSING: 127 no longer seeds 'Montenegrin'")
    f.write_text(s.replace(
        "UNION ALL SELECT 'Uzbek',",
        "UNION ALL SELECT 'Croat', 'HR'  WHERE NOT EXISTS (SELECT 1 FROM `nationalities` WHERE `name` = 'Croat')\n"
        "UNION ALL SELECT 'Uzbek',", 1))


def run_tests(verbose=False):
    env = {**__import__("os").environ, **GO_ENV}
    args = ["go", "test"] + TEST_ARGS + (["-v"] if verbose else [])
    r = subprocess.run(
        args,
        cwd=REPO, capture_output=True, text=True, env=env, timeout=1800,
    )
    return r.returncode, r.stdout + r.stderr


if __name__ == "__main__":
    which = sys.argv[2] if len(sys.argv) > 2 else ""
    if which:
        MUTANTS[which]()
        sys.exit(0)

    # --- 0. BASELINE. The unmutated tree must PASS, or every result below is meaningless: "the
    # mutant was killed" would only mean the tree was already red.
    print("=== baseline: the unmutated tree must PASS ===")
    rc, out = run_tests()
    if rc != 0:
        print("BASELINE FAILED -- results below would be meaningless:\n" + out[-2500:])
        sys.exit(2)
    print("BASELINE PASS  (good)\n")

    # --- 0b. THE BASELINE MUST HAVE ACTUALLY RUN THE NAMED TESTS.
    #
    # A green baseline is not sufficient evidence that the harness works: the first version of this
    # gate had a mis-tokenised `-run` and therefore ran EVERY package, which is also green. Three
    # mutants were then reported SURVIVED on the strength of an unrelated package's result -- a false
    # hole in the suite that pointed at the tests instead of the harness.
    #
    # So the baseline is only accepted if it actually executed the tests this gate is about. `-v`
    # makes the run names appear; their absence is a harness bug, not a pass.
    rc, out = run_tests(verbose=True)
    ran = [n for n in ("TestPerformerAliasStudioAssociation", "TestPerformerAliasOwnership",
                       "TestPerformerNationalities",
                       "TestDestroyingAPerformerLeavesNoRowsBehind",
                       "TestEveryPerformerReferencingTableIsCovered") if ("=== RUN   " + n) in out]
    if len(ran) != 5:
        print("HARNESS BUG -- the baseline did not run the tests this gate is about.")
        print(f"  expected 4 named tests, saw {len(ran)}: {ran}")
        print("  (a mis-tokenised -run filter looks exactly like a pass)")
        sys.exit(2)
    print(f"HARNESS SELF-CHECK PASS -- all {len(ran)} named tests executed\n")

    killed, survived, broken = 0, 0, 0
    for name, fn in MUTANTS.items():
        print(f"=== mutant {name} ===")
        with tempfile.TemporaryDirectory() as td:
            baks = {}
            for f in ("pkg/sqlite/stashbox_parity.go",
                      "pkg/sqlite/migrations/127_nationality_reference_completion.up.sql",
                      "pkg/models/stashbox_parity.go",
                      "pkg/sqlite/migrations/126_alias_studio_association.up.sql",
                      "pkg/sqlite/migrations/125_stashbox_parity_performers_tags.up.sql"):
                src = REPO / f
                if src.exists():
                    dst = pathlib.Path(td) / f.replace("/", "_")
                    shutil.copy2(src, dst)
                    baks[f] = dst
            try:
                fn()
            except SystemExit as e:
                print(f"  HARNESS BUG -- {e}")
                broken += 1
                continue
            rc, out = run_tests()
            if rc != 0:
                print("  KILLED")
                killed += 1
            else:
                print("  SURVIVED -- the suite does not catch this")
                survived += 1
            for f, dst in baks.items():
                shutil.copy2(dst, REPO / f)

    print()
    print("=" * 60)
    print(f"mutants killed: {killed}, survived: {survived}, harness errors: {broken}")
    if broken:
        print("INCONCLUSIVE -- a mutant was never actually tested")
        sys.exit(2)
    if survived:
        print("MUTATION GATE FAILED")
        sys.exit(1)
    print("MUTATION GATE PASSED")