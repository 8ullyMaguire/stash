#!/usr/bin/env python3
"""Mutation check for the consent guard (M3 step 3.1).

The plan's warning is the reason this file exists:

    This is the milestone where a mistake is irreversible -- a published private
    library cannot be unpublished from users' copies. Mutation-check the opt-out:
    make ShareOptedIn always return true and confirm
    TestPublish_RefusedWhenOptedOut fails.

So the mutation that matters most is the one that silently turns every user into
a consenting one. If that mutation SURVIVES, this harness is telling you
nothing about the guard, and the number below is decoration.

Every mutation here must be KILLED. There is no EXEMPT list, and that is
deliberate: a survivor in this file is either a missing test or a bug, and both
are worth stopping for.
"""

import pathlib
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
CONSENT = ROOT / "internal/collab/consent.go"
EXPORTER = ROOT / "internal/collab/exporter.go"

# Which file each mutation applies to, and the test selection that must kill it.
TEST_RE = "Consent|Disclosure|PublishedField|SetConsent|Export|Publish|Payload|SubmissionID|AssertNo"

# (file, label, old, new) -- each removes or inverts one safety property.
CONSENT_MUTATIONS = [
    (
        "absent row treated as opted OUT (the default inverted)",
        "		return true, nil\n	}\n	return choice == ChoiceOptedIn, nil",
        "		return false, nil\n	}\n	return choice == ChoiceOptedIn, nil",
    ),
    (
        "opt-out user reported as opted in (the reverse)",
        "	return choice == ChoiceOptedIn, nil",
        "	_ = choice\n	return true, nil",
    ),
    (
        "a failed consent query is swallowed and reads as opted in",
        "	if err != nil {\n		return \"\", false, fmt.Errorf(\"querying consent for user %d: %w\", userID, err)\n	}",
        "	if err != nil {\n		return ChoiceOptedIn, true, nil\n	}",
    ),
    (
        "a failed consent query is swallowed and reads as opted OUT",
        "	if err != nil {\n		return \"\", false, fmt.Errorf(\"querying consent for user %d: %w\", userID, err)\n	}",
        "	if err != nil {\n		return ChoiceOptedOut, true, nil\n	}",
    ),
    (
        "corrupt metadata_share defaults to opted in instead of erroring",
        "	if !parsed.Valid() {\n		return \"\", false, fmt.Errorf(\"consent for user %d has unknown metadata_share %q\", userID, choice)\n	}",
        "	if !parsed.Valid() {\n		return ChoiceOptedIn, true, nil\n	}",
    ),
    (
        "opted-out user is re-prompted (a prompt is a Share button)",
        "	if choice == ChoiceOptedOut {\n		// Declined. Stays declined, and stays un-prompted, until a human says\n		// otherwise. See the comment above.\n		return false, nil\n	}",
        "	if choice == ChoiceOptedOut {\n		return true, nil\n	}",
    ),
    (
        "disclosure staleness inverted, so a stale answer looks current",
        "	return version < current, nil",
        "	return version >= current, nil",
    ),
    (
        "an invalid choice is written instead of refused",
        "	if !choice.Valid() {\n		return fmt.Errorf(\"consent choice %q is not one of %q or %q\", choice, ChoiceOptedIn, ChoiceOptedOut)\n	}",
        "",
    ),
    (
        "never-asked user is not prompted",
        "	if !found {\n		return true, nil\n	}\n	if choice == ChoiceOptedOut {",
        "	if !found {\n		return false, nil\n	}\n	if choice == ChoiceOptedOut {",
    ),
]


def run(cmd, cwd):
    return subprocess.run(
        cmd, cwd=cwd, capture_output=True, text=True, shell=isinstance(cmd, str)
    )


EXPORTER_MUTATIONS = [
    (
        "exporter: an undisclosed field is passed through",
        "		if allowed[k] {\n			out[k] = v\n		}",
        "		_ = allowed\n		out[k] = v",
    ),
    (
        "exporter: the dry-run default is flipped to a real publish",
        "		// The first export after consent writes to disk and sends nothing\n		// (spec §6.3). The caller may clear this once the user has seen the\n		// disclosure and asked for a real publish.\n		DryRun: true,",
        "		DryRun: false,",
    ),
    (
        "exporter: a path-shaped value is no longer refused",
        "	if i := strings.Index(v, \"/\"); i >= 0 && i < len(v)-1 {",
        "	if i := -1; i >= 0 && i < len(v)-1 {",
    ),
    (
        "exporter: a non-content fingerprint type is published",
        "		if !allowedFingerprintTypes[k] {\n			continue\n		}",
        "",
    ),
    (
        "exporter: the submission id ignores the entries (content-blind)",
        "		fmt.Fprintf(h, \"entry=%s/%d\\n\", e.TargetType, e.TargetID)",
        "		_ = e",
    ),
]


def main():
    work = [(CONSENT, m) for m in CONSENT_MUTATIONS] + [(EXPORTER, m) for m in EXPORTER_MUTATIONS]
    originals = {p: p.read_text() for p in (CONSENT, EXPORTER)}
    killed, survived, broken = [], [], []

    try:
        for path, (label, old, new) in work:
            original = originals[path]
            if old not in original:
                # A replacement that does not land reports as a survivor that
                # means nothing. Distinguish it, or the number lies.
                broken.append(f"{label} -- ANCHOR NOT FOUND (mutation never applied)")
                print(f"BROKEN   {label} (anchor not found)")
                continue

            mutated = original.replace(old, new, 1)
            if mutated == original:
                broken.append(f"{label} -- replacement was a no-op")
                print(f"BROKEN   {label} (no-op replacement)")
                continue

            path.write_text(mutated)
            proc = run(
                ["go", "test", "./internal/collab/", "-run", TEST_RE, "-count=1"],
                ROOT,
            )
            out = proc.stdout + proc.stderr

            # A mutation that does not compile kills no test, and a guard that
            # only looks for a test failure will score it SURVIVED -- a lie
            # about the suite. Detect the build error explicitly.
            if re.search(r"^(\[|.*\] )?# ", out, re.M) or "cannot use" in out or "undefined:" in out:
                print(f"BROKEN   {label} (does not compile -- scores nothing)")
                broken.append(f"{label} -- does not compile")
            elif proc.returncode != 0:
                killed.append(label)
                print(f"KILLED   {label}")
            else:
                print(f"SURVIVED {label}  <-- investigate")
                survived.append(label)

    finally:
        for path, text in originals.items():
            path.write_text(text)

    # Confirm the restore actually took, so a killed run cannot leave the tree
    # mutated for the next commit.
    for path, text in originals.items():
        if path.read_text() != text:
            print(f"FATAL: {path.name} was not restored", file=sys.stderr)
            return 2

    total = len(work)
    print(f"\napplied {total} / killed {len(killed)} / survived {len(survived)} / broken {len(broken)}")

    if broken:
        print("\nBROKEN means the harness itself failed, not that the code is weak:", file=sys.stderr)
        for b in broken:
            print(f"  {b}", file=sys.stderr)
        return 2
    if survived:
        print(
            "\nA survivor in this file is either a missing test or a bug in the guard.",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
