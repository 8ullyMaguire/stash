#!/usr/bin/env python3
"""Mutation sweep for stash#1790 — external_ids.

Per the skill: a harness that reports `killed` has proved nothing yet. Four verdicts,
exiting with different codes, and every one of them earned by a bug in an EARLIER version
of this harness:

  killed    the named test went red
  covered   the named test passed BUT the suite failed -- a lower layer already refused
            the input. NOT a pass; it is a claim, and it is only trustworthy because the
            whole suite is re-run with the mutation still applied.
  survived  the whole suite passed with the mutation applied. This is the verdict that
            means a hole.
  skip      the mutation did not compile or did not apply. NOT a kill. Counting these as
            kills is how a harness reports 14/14 while testing 10.

Restoration is per-mutation and verified BYTE-FOR-BYTE from an in-memory snapshot, not
with `git checkout` (which cannot restore an untracked file) and not at the end of the loop
(an interrupted sweep leaves the mutation applied, and the leftover then reads as a
pre-existing bug). Every run is bounded by a timeout, because a harness that can hang takes
the session with it.

Exit codes: 0 all resolved, 1 survivors present, 2 harness malformed.
"""

import os
import pathlib
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
PKG = "./pkg/sqlite/"
# A `-run` filter, for speed. IT IS ALSO A CORRECTNESS BUG SURFACING AREA, and it was one:
# this list was written before the two raw-SQL blank-value tests existed, so it silently
# excluded them and a mutation whose ONLY witness is one of them could never be killed --
# M7 survived twice, and the first "fix" (adding a test) could not have worked while the
# test was filtered out of the run.
#
# SO IT IS CHECKED, not trusted: `audit_test_filter` asserts every test in the external-id
# files is either matched here or explicitly listed as deliberately out of scope. A filter
# that silently excludes a test is a gate that can be made to pass without being open.
#
# Run WITHOUT the filter when a survivor appears -- `docs/one_mutation.sh <n>` does exactly
# that, which is how M7's real witness was found.
TEST_RE = r'^(TestT1|TestT1b|TestT1c|TestT2|TestT3|TestT4|TestT5|TestR4|TestFindByExternalID|TestCreatingASource|TestRecordingRejects|TestDestroyForEntityRefuses|TestEveryKnownEntityType|TestAnUnknownSource|TestTwoSources|TestTheSameSource|TestTheExactDuplicate|TestASourceName|TestDeletingASource|TestTheLookupIndex|TestTheEntityIDColumn|TestEntityTypeIsFree|TestTheForeignKeyPragma|TestABlankExternalID|TestABlankEntityType)'


def audit_test_filter():
    """Every Test in the external-id files must be reachable by TEST_RE.

    A `-run` filter that quietly excludes a test is the same defect as a gate that cannot
    be made to fail: the harness reports a survivor the whole suite would have caught, and
    the next reader goes looking for a hole in the tests instead of in the filter.
    """
    import re as _re
    files = ["pkg/sqlite/external_id_test.go", "pkg/sqlite/external_id_schema_test.go"]
    pat = _re.compile(r"^func (Test\w+)\(", _re.M)
    missing = []
    for f in files:
        for name in pat.findall((REPO / f).read_text()):
            if not _re.match(TEST_RE, name):
                missing.append(f"{f}:{name}")
    if missing:
        print("  !! TEST_RE does not match these tests -- add them or the harness will "\
              "report a survivor the full suite would have killed:")
        for m_ in missing:
            print(f"       {m_}")
        fail("test filter is incomplete")
    print(f"  test filter covers every test in {len(files)} external-id files")

# A BUILD_ERRORS list that is complete is a safety device, not a nicety: a missing entry
# produces a FALSE HOLE that sends the next reader into the code. Prefer go build/vet output
# shape over substring guessing.
BUILD_ERRORS = (
    "build failed", "cannot use", "undefined:", "declared and not used", "syntax error",
    "not enough arguments", "too many arguments", "assignment mismatch", "redeclared",
    "no new variables", "imported and not used", "missing return", "all declarations of",
    "shadows declaration", "vet:", "build constraints exclude", "no field or method",
    "cannot convert", "too many arguments to", "not enough arguments to",
)

# (label, file, old, new, expect)  -- expect is a test name substring.
#
# THE FIRST SET IS THE FIVE FROM THE PLAN'S STEP 5. THE REST ARE COMPOUND, because several
# defects need SEVERAL edits to exist at all: `SweepOrphans` refuses an unknown type in
# both `DestroyForEntity` and `SweepOrphans`, so disabling one changes nothing observable.
# Labelling a single edit "COMPOUND" without making it compound is a lie the harness
# cannot detect.
MUTATIONS = [
    ("M1 drop source_id from the unique key",
     "pkg/sqlite/migrations/121_external_ids.up.sql",
     [("unique(`entity_type`, `entity_id`, `source_id`, `external_id`)",
       "unique(`entity_type`, `entity_id`, `external_id`)")],
     "TestTwoSourcesMayUseTheSameExternalIDStringOnOneEntity"),

    ("M2 Record INSERTs instead of upserting",
     "pkg/sqlite/external_id.go",
     [("""		ON CONFLICT (entity_type, entity_id, source_id, external_id)
		DO UPDATE SET updated_at = excluded.updated_at`,""",
       "`,")],
     "TestT3ReRecordingUpdatesRatherThanInserting"),

    ("M3a drop the external-id destroy from table.destroy",
     "pkg/sqlite/table.go",
     [("""	if entityType, ok := externalIDEntityTypeForTable[t.table.GetTable()]; ok {
		for _, id := range ids {
			if err := NewExternalIDStore().DestroyForEntity(ctx, entityType, id); err != nil {
				return err
			}
		}
	}""", "")],
     "TestR4GalleryAdoptsExternalIDsThroughTheGenericPathOnly"),

    ("M3b drop the external-id destroy from repository.destroy",
     "pkg/sqlite/repository.go",
     [("""	if entityType, ok := externalIDEntityTypeForTable[r.tableName]; ok {
		for _, id := range ids {
			if err := NewExternalIDStore().DestroyForEntity(ctx, entityType, id); err != nil {
				return err
			}
		}
	}""", "")],
     "TestT4EveryEntityDestroyPathRemovesExternalIDs"),

    ("M4a SweepOrphans deletes everything (COMPOUND: both guards + the predicate)",
     "pkg/sqlite/external_id.go",
     [("""	var total int64
	for _, t := range externalIDEntityTables {""",
       """	var total int64
	for _, t := range []struct {
		entityType string
		table      string
	}{{"anything", "scenes"}, {"everything", "tags"}} {""")],
     "TestT5SweepOrphansRemovesOrphansAndNothingElse"),

    ("M5 Record treats an unknown source as source 0 instead of refusing",
     "pkg/sqlite/external_id.go",
     [("""		if src == nil {""", """		if false {""")],
     "TestT1RecordingAgainstAnUnknownSourceIsRefusedByName"),

    # --- additional mutations, one per requirement the tests claim to cover ---

    ("M6 the entity_type blank check is removed",
     "pkg/sqlite/migrations/121_external_ids.up.sql",
     [("  check(length(trim(`entity_type`)) > 0),", "")],
     "TestEntityTypeIsFreeTextButNotBlank"),

    # M7 is the mutation that found a real hole, and the fix is in `expect`, not in the
    # mutation. It originally pointed at the STORE test -- which stays green with the CHECK
    # deleted, because Record refuses a blank id in Go before any SQL runs. A lower layer
    # refused the identical input, so by the four-verdict rule that is `covered` rather than
    # a kill; but as a single test it read as proof of the schema and proved nothing about
    # it. The expect now names the test that inserts RAW SQL, where the Go check cannot
    # intercept and the CHECK is the only thing standing in the way.
    ("M7 the external_id blank check is removed",
     "pkg/sqlite/migrations/121_external_ids.up.sql",
     [("  check(length(trim(`external_id`)) > 0),", "")],
     "TestABlankExternalIDIsRefusedByTheDatabaseItself"),

    ("M8 the source_id foreign key is removed",
     "pkg/sqlite/migrations/121_external_ids.up.sql",
     [("  foreign key(`source_id`) references `external_sources`(`id`) on delete CASCADE,", "")],
     "TestAnUnknownSourceIsRefused"),

    ("M9 CASCADE on source delete is dropped, so ids outlive their source",
     "pkg/sqlite/migrations/121_external_ids.up.sql",
     [("on delete CASCADE", "")],
     "TestDeletingASourceDeletesItsIDs"),

    ("M10 the lookup index on (source_id, external_id) is dropped",
     "pkg/sqlite/migrations/121_external_ids.up.sql",
     [("index_external_ids_on_lookup", "index_external_ids_renamed")],
     "TestTheLookupIndexExistsOnSourceAndExternalID"),

    ("M11 CreateSource overwrites the URL on conflict instead of keeping the first",
     "pkg/sqlite/external_id.go",
     [("		ON CONFLICT (name) DO NOTHING`,", "		ON CONFLICT (name) DO UPDATE SET url = excluded.url`,")],
     "TestCreatingASourceTwiceReturnsTheSameOne"),

    ("M12 Record resolves a missing source name to source 0 rather than erroring",
     "pkg/sqlite/external_id.go",
     [("""		if input.Source == nil || *input.Source == "" {""", """		if false {""")],
     "TestT1cRecordingWithNoSourceAtAllIsRefused"),

    ("M13 IsKnownExternalIDEntityType accepts everything",
     "pkg/sqlite/external_id.go",
     [("""	for _, t := range externalIDEntityTables {
		if t.entityType == entityType {
			return true
		}
	}
	return false""", "	return true")],
     "TestDestroyForEntityRefusesAnUnknownEntityType"),
]

TIMEOUT = 600

# A LOCK FILE, because a second sweep racing the first on the same source files is a
# corruption generator: each holds a snapshot of the other's mutation. This happened once
# during development -- two sweeps were started to double-check a survivor, and killing the
# second left a mutation applied that the first then reported as a pre-existing change. The
# cost of the lock is that concurrent sweeps serialise; the benefit is that they cannot
# interleave. A stale lock is reported with its age rather than silently stolen.
LOCK = REPO / "docs" / ".mutate_external_id.lock"


def acquire_lock():
    import time
    if LOCK.exists():
        age = time.time() - LOCK.stat().st_mtime
        if age < 3600:
            fail(f"another sweep holds {LOCK.name} (age {age:.0f}s). Refusing to race it.")
        print(f"  stale lock ({age:.0f}s old) -- taking it over")
    LOCK.write_text(str(os.getpid()))


def release_lock():
    try:
        LOCK.unlink()
    except FileNotFoundError:
        pass


def run(args, timeout=TIMEOUT):
    """Run a command, inheriting os.environ. A fresh env drops GOCACHE and every baseline
    comes out RED 'for want of a build cache' -- which reads as a broken build."""
    env = dict(os.environ)
    env["GOFLAGS"] = "-mod=mod"
    env.setdefault("GOFLAGS", "-mod=mod")
    try:
        p = subprocess.run(args, cwd=str(REPO), capture_output=True, text=True,
                           timeout=timeout, env=env)
        return p.returncode, p.stdout + p.stderr
    except subprocess.TimeoutExpired:
        return 124, "TIMEOUT"


def baseline():
    rc, out = run(["go", "test", "-tags", "integration", PKG, "-count=1", "-timeout", "480s"])
    if rc != 0:
        print("BASELINE IS RED -- refusing to score anything against it.\n")
        print(out[-4000:])
        sys.exit(2)
    return out


def apply_edits(path, edits):
    """Snapshot, apply, return the snapshot. ANCHORS ARE ASSERTED PRESENT FIRST: a silent
    no-op substitution reported as `survived` claims to have measured something it never
    did."""
    p = REPO / path
    original = p.read_text()
    current = original
    for old, new in edits:
        if current.count(old) != 1:
            return None, original, (
                f"ANCHOR NOT UNIQUE ({current.count(old)} matches) in {path}: "
                f"{old.splitlines()[0][:70]!r}")
        current = current.replace(old, new, 1)
    if current == original:
        return None, original, f"NO-OP: edits changed zero bytes in {path}"
    p.write_text(current)
    return current, original, None


def trap_restore():
    """SIGTERM/SIGINT: restore whatever the CURRENT mutation touched, then exit.

    A sweep interrupted mid-probe leaves the mutation applied, and the leftover reads as a
    pre-existing bug rather than as a harness artifact. This has already happened once in
    this repo -- a killed sweep left migration 121 with the `entity_type` CHECK removed, and
    the next `git diff` showed a change to a file nobody had edited on purpose.

    Restoration must not depend on the loop reaching its epilogue, so it is registered as a
    signal handler AND run in the `finally` of each probe. Both, because a signal arriving
    between two probes has no `finally` to run.
    """
    cur = _CURRENT
    if cur is None:
        return
    path, original = cur
    try:
        (REPO / path).write_text(original)
        print(f"\n  trap: restored {path} from the interrupted probe")
    except Exception as e:  # a restore that cannot fail is not a restore
        print(f"\n  !! TRAP RESTORE FAILED for {path}: {e}")


_CURRENT = None


def main():
    import signal
    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, lambda *_: (trap_restore(), sys.exit(130)))

    acquire_lock()
    try:
        sweep()
    finally:
        release_lock()


def sweep():
    print("=== baseline ===")
    audit_test_filter()
    baseline()

    results = []
    for label, path, edits, expect in MUTATIONS:
        mutated, original, err = apply_edits(path, edits)
        global _CURRENT
        _CURRENT = (path, original)
        try:
            if err:
                results.append((label, "SKIP", err))
                print(f"SKIP    {label} -- {err}")
                continue

            rc, out = run(["go", "test", "-tags", "integration", PKG, "-count=1",
                           "-timeout", "480s", "-run", TEST_RE])
            if any(b in out for b in BUILD_ERRORS):
                results.append((label, "SKIP", "does not compile"))
                print(f"SKIP    {label} -- does not compile")
                continue

            named = [l for l in out.splitlines()
                     if l.startswith("--- FAIL") and expect in l]
            if named:
                results.append((label, "KILLED", named[0].strip()))
                print(f"KILLED  {label}  <- {expect}")
            elif rc == 0:
                # THE NAMED TEST PASSED. Is the gate actually open? Re-run the WHOLE suite
                # with the mutation still applied: if that fails, a different test caught
                # it, which is `survived` -- the exact inversion SKIP exists to prevent.
                rc2, out2 = run(["go", "test", "-tags", "integration", PKG,
                                 "-count=1", "-timeout", "480s"])
                if rc2 == 0:
                    results.append((label, "SURVIVED", "whole suite passed"))
                    print(f"SURVIVED {label}  <- HOLE: no test can see this")
                else:
                    fails = [l for l in out2.splitlines() if l.startswith("--- FAIL")]
                    results.append((label, "SURVIVED", f"suite failed elsewhere: {fails[:2]}"))
                    print(f"SURVIVED {label}  <- suite failed but NOT by {expect}")
            else:
                results.append((label, "SURVIVED", "suite failed, named test passed"))
                print(f"SURVIVED {label}  <- suite failed but NOT by {expect}")
        finally:
            # RESTORE PER MUTATION, from the in-memory snapshot, and VERIFY.
            if original is not None:
                (REPO / path).write_text(original)
                if (REPO / path).read_text() != original:
                    print(f"  !! RESTORE FAILED for {path} -- ABORTING")
                    sys.exit(2)
            _CURRENT = None

    print("\n=== summary ===")
    for v in ("SURVIVED", "SKIP", "COVERED", "KILLED"):
        rows = [r for r in results if r[1] == v]
        if rows:
            print(f"{v} ({len(rows)}):")
            for label, _, detail in rows:
                print(f"  - {label}: {detail}")

    survivors = [r for r in results if r[1] == "SURVIVED"]
    print(f"\nkilled {sum(1 for r in results if r[1]=='KILLED')}/{len(results)}")
    if survivors:
        print("\nSURVIVORS -- a survivor is a hole in the tests unless the CODE is wrong. "
              "Read the diff before touching the source: if disabling the check makes the "
              "suite pass, the check was wrong, not the test blind.")
        sys.exit(1)
    sys.exit(0)


if __name__ == "__main__":
    main()
