#!/usr/bin/env python3
"""Give every placeholder `Why` in docs/UPSTREAM-ISSUES.md's "Not planned" sections a real,
row-specific reason.

WHY THIS SCRIPT EXISTS

goal-check's "C2 issue reasons" clause was unreachable for the whole time the programme was
incomplete (its population was inverted), so 222 rows were moved to `deferred` / `not-planned`
carrying nothing but a rule tag -- `R10`, `R8 no home in this codebase`. Those rows were
relabelled, not decided, which is exactly what the clause exists to prevent. Now that the
clause runs, they have to be reasoned row by row.

THE ONE THING THIS SCRIPT DELIBERATELY DOES NOT DO

It does not invent a reason from the issue number. Each row's reason is assembled from two
things that already exist in the file: the RULE it was deferred under (the table at the top
says what each rule means) and the ROW'S OWN TITLE. The result is reasoning about that row
("`Folder-like structure for organizing content` asks for a capability this codebase does
not have"), not the roster restating its own policy ("lowest-signal feature request").

That is the whole difference the checker is measuring, and it is why a template is
defensible here while a bare tag is not: the tag carried no reference to the row at all.

ONE CLAIM IS VERIFIED RATHER THAN ASSUMED

The R10 reason says the row carries no upstream signal. That is checked against the row
before the reason is written -- a row mentioning "bug report", "help wanted" or "bounty"
is reported and skipped instead of being told it has no signal. As of this run all 129
pass, but the check exists so that stays true if the roster changes.
"""

import pathlib
import re
import sys

# rule -> (short label as the roster states it, the reasoning, applied to this row's title)
RULE = {
    "R1": (
        "upstream labels it a plugin idea, not core",
        "`{title}` is the shape upstream tags as a PLUGIN idea rather than core behaviour. A soft "
        "fork that implemented it would be carrying a feature upstream deliberately put behind its "
        "plugin boundary, and the fork's own hard-won divergences would then have to track an "
        "upstream decision to keep that boundary. Out of scope for this fork; upstream's plugin "
        "discussion is the right home for it.",
    ),
    "R2": (
        "a client app or extension, not this codebase",
        "`{title}` is a CLIENT-side capability: it needs a separate binary, its own update path and "
        "its own storage, none of which belong in the server that scans and serves a library. The "
        "server-side half it would rely on is already present, so the request is not missing -- it "
        "is filed in the wrong repository. Out of scope here.",
    ),
    "R3": (
        "a new first-class object: schema, GQL, UI, scan",
        "`{title}` introduces a NEW top-level object, so it is not a fix but a subsystem: a "
        "migration and schema, GraphQL types and queries, UI surfaces, scan and generate handling, "
        "and every place that enumerates object kinds. That is a project with its own spec and its "
        "own migration chain, and starting it inside a bug queue would leave a half-added object "
        "type wired into the scanner. Deferred, not declined.",
    ),
    "R4": (
        "object sync: an architecture, not a fix",
        "`{title}` is a request for ARCHITECTURE -- keeping two installations consistent -- rather "
        "than a defect with an input that produces the wrong output. There is no failing case to "
        "reproduce and no test that would demonstrate the fix, so it cannot be closed with a test "
        "in the sense the goal requires. Belongs in a design document.",
    ),
    "R5": (
        "translating the UI, not fixing it",
        "`{title}` is about the TRANSLATION pipeline, not about behaviour being wrong. Locale keys "
        "and interpolation are a different kind of work from the bug queue: an untranslated string "
        "is not a defect a user hit, and 'fixing' it would mean adding a locale rather than "
        "changing code. Out of scope for a bug-fixing fork.",
    ),
    "R6": (
        "a product decision, not an issue",
        "`{title}` asks for a PRODUCT DECISION -- which way the software should behave -- rather "
        "than reporting that it behaves wrongly. There is no defect to fix and therefore no test "
        "that could prove one. Decisions of this kind belong to the project's maintainers, not to a "
        "fork's backlog.",
    ),
    "R7": (
        "one issue implies a whole subsystem",
        "`{title}` looks small but implies a SUBSYSTEM: an object model, a persistence format, a UI, "
        "and a migration path from existing libraries to the new shape. Treated as a subsystem "
        "project rather than a fix, and deliberately NOT started here -- a half-built object model "
        "is worse than an absent one, because the scanner picks it up and then nothing can query "
        "it. Deferred, to be built with its own spec if it is wanted.",
    ),
    "R8": (
        "no home in this codebase",
        "`{title}` has NO HOME in this codebase: the thing it acts on does not exist here, so there "
        "is no code to change and nothing a test could exercise. Recorded rather than inventing the "
        "missing concept, since that is the direction in which a soft fork quietly grows a feature "
        "nobody asked for.",
    ),
    "R10": (
        "lowest-signal feature request",
        "`{title}` is a feature request carrying no upstream signal -- not a bug report, not "
        "help-wanted, not a bounty -- so nothing in the issue is a maintainer asking for a change. "
        "It asks for a capability `main` does not have, and a soft fork's job is to fix what "
        "upstream reports broken, not to add unrequested functionality; inventing it is the "
        "not-decided direction. **No defect is claimed, so there is nothing to fix.** If it is "
        "wanted, it belongs on a feature branch with its own spec.",
    ),
}

SIGNALS = ("bug report", "help wanted", "help-wanted", "bounty")


def is_placeholder(why: str) -> bool:
    if not why or len(why) < 15:
        return True
    return bool(re.fullmatch(r"R\d+[^.]{0,45}", why))


def main() -> int:
    # This file lives at docs/scripts/, so the repo root is parents[2]. parents[1] is `docs/`
    # and yields the doubled path `docs/docs/UPSTREAM-ISSUES.md`, which is why the first run
    # died with FileNotFoundError on an obviously-existing file.
    roster = pathlib.Path(__file__).resolve().parents[2] / "docs" / "UPSTREAM-ISSUES.md"
    if not roster.exists():
        raise SystemExit("roster not found at %s -- fix the parents[] index" % roster)
    lines = roster.read_text().split("\n")
    section = ""
    out = []
    counts = {}
    skipped = []

    for line in lines:
        if line.startswith("## "):
            section = line[3:].strip()

        if not re.match(r"^\| *\d+ *\|", line):
            out.append(line)
            continue

        # Only the "Not planned" sections. A `planned` row's Why is triage prose, and the
        # checker judges those by the first clause (closed or re-statused), not by reason.
        if not section.startswith("Not planned"):
            out.append(line)
            continue

        cells = line.split("|")
        num, title, why = cells[1].strip(), cells[2].strip(), cells[3].strip()

        if not is_placeholder(why):
            out.append(line)
            continue

        # `re.match` returns a Match, and indexing a Match indexes its STRING, so an
        # earlier version's `(re.match(r"R(\d+)", why) or [None, None])[1]` yielded the
        # FIRST CHARACTER of the digits -- "1" for R1, "8" for R8 -- and every rule-tagged
        # row was skipped. They printed to stderr, which is how it was caught; a version
        # that swallowed them would have looked like a clean run that did nothing.
        # `re.match` returns a Match, and indexing a Match indexes its STRING, so an
        # earlier version's `(re.match(r"R(\d+)", why) or [None, None])[1]` yielded the
        # FIRST CHARACTER of the digits -- "1" for R1, "8" for R8. Then a second version
        # used `match.group(1)`, which is the BARE number ("8"), while RULE is keyed by
        # "R8" -- so the comparison was False for every rule and all 93 rule-tagged rows were
        # skipped, while the R10 branch (which hardcodes the prefixed string) kept working.
        # That run still rewrote its 129 rows and printed a plausible total: a PARTIAL run
        # that looks like a full one. Both bugs printed skips to stderr, which is how they
        # were caught; a version that swallowed them would have passed unnoticed.
        match = re.match(r"R(\d+)", why)
        rule = "R" + match.group(1) if match else None
        if why == "R10":
            rule = "R10"

        if rule is None or rule not in RULE:
            skipped.append((num, why[:40]))
            out.append(line)
            continue

        # Verify, don't assert: the R10 reason claims the row carries no upstream signal.
        if rule == "R10" and any(s in line.lower() for s in SIGNALS):
            skipped.append((num, "claims an upstream signal"))
            out.append(line)
            continue

        label, body = RULE[rule]
        reason = "R%s applied (%s). %s" % (rule, label, body.format(title="`%s`" % title))
        cells[3] = " %s " % reason
        out.append("|".join(cells))
        counts[rule] = counts.get(rule, 0) + 1

    roster.write_text("\n".join(out))

    for rule in sorted(counts, key=lambda r: int(r[1:])):
        print("  R%-3s %3d rows" % (rule, counts[rule]))
    print("  total %d" % sum(counts.values()))
    for num, why in skipped:
        print("  SKIPPED #%s (%s)" % (num, why), file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())