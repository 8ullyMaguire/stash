"""Append the 2026-09-30 priority-shift requirements to docs/requirements.csv.

Written as a script rather than a hand edit because the failure mode this
replaces is a real one that has already happened in this file: an unquoted comma
in a notes field shifts every later cell, and `cut -d,` cannot see it. The
2026-09-28 pass found exactly that and fixed it by parsing with csv.

So: the column count is asserted against the REAL header, not a literal, and
the result is re-read and validated after writing. Both checks are here because
both have caught a real defect in this repository.
"""

import csv
import sys

PATH = "docs/requirements.csv"

# thread, id, title, source, priority, status, spec_section, plan_step,
# depends_on, notes -- in the file's own column order, which is read from the
# header rather than assumed.
NEW = [
    # Keys are the file's own column names, so a wrong ORDER is not
    # expressible. The tuple form of this list led with `thread` while the
    # header leads with `id`, and the column-count assertion passed anyway --
    # 10 values against 10 columns -- while every cell in 13 rows was shifted
    # left by one. A count check cannot see a permutation.
    dict(id="R074", thread="acquisition",
         title="Automatic scan and curation profile per library",
         source="owner directive 2026-09-30", priority="high", status="specified",
         spec_section="6b.2", plan_step="M8 step 8.0", depends_on="R003",
         notes="Scanning is upstream and works. The missing part is the policy: schedule and consent, not machinery. Curation writes PROPOSALS so an automatic action lands in the same audit trail as a human's (#5)"),

    dict(id="R075", thread="acquisition",
         title="Acquire content the user would enjoy automatically with opt-out",
         source="owner directive 2026-09-30", priority="high", status="specified",
         spec_section="6b.3", plan_step="M8 step 8.2", depends_on="R010 R011 R009",
         notes="Three states not a boolean: off / fetch_only / full. fetch_only is the default for a NEW install and leaks nothing outward; off is the default for an EXISTING one because switching content sharing on is a publish action nobody consented to"),

    dict(id="R076", thread="acquisition",
         title="Taste-ranked acquisition queue reusing the mesh recommender",
         source="owner directive 2026-09-30", priority="high", status="specified",
         spec_section="6b.3", plan_step="M8 step 8.2", depends_on="R010 R011",
         notes="No new ranker. 6a.5's ranking function pointed at a download queue. What the mesh holds minus what this instance has, by taste similarity to what the user watches"),

    dict(id="R077", thread="preservation",
         title="Content plane that does not require peers to exchange routable addresses",
         source="owner directive 2026-09-30", priority="high", status="specified",
         spec_section="6b.4", plan_step="M8 step 8.1", depends_on="R018",
         notes="Stated as a PROPERTY not a technology, because naming a transport here makes it a dependency. Probe step 0 answers it by running the candidate, not by reading its docs"),

    dict(id="R078", thread="preservation",
         title="Manifest plus content hash and a replica counts only when verified",
         source="owner directive 2026-09-30", priority="high", status="specified",
         spec_section="6b.5", plan_step="M8 step 8.3", depends_on="R077 R019",
         notes="Non-negotiable #14. A peer saying it stored the bytes is a claim, the same posture the downloader's storage gate takes toward a peer-supplied filename. Unverified replicas are pending and do NOT count toward N"),

    dict(id="R079", thread="preservation",
         title="Preservation alerts when a scene falls below N healthy replicas",
         source="stash-box SPEC 7.18.2 cross-ref", priority="high", status="specified",
         spec_section="6b.4", plan_step="M8 step 8.3", depends_on="R078 R019",
         notes="Complements rather than duplicates the commons side: the commons knows the count is low, the node is the one that can fix it. Urgency weights rarity and demand, never mesh-wide traffic"),

    dict(id="R080", thread="preservation",
         title="Storage allocation log so an operator can audit what was placed and why",
         source="stash-box SPEC 7.18.3 cross-ref", priority="medium", status="specified",
         spec_section="6b.4", plan_step="M8 step 8.4", depends_on="R078",
         notes="The audit trail for a subsystem that moves bytes onto other people's disks. Without it an operator cannot answer why this instance is full"),

    dict(id="R081", thread="privacy",
         title="No path filename directory structure hostname IP username or token crosses a node boundary",
         source="owner directive 2026-09-30", priority="high", status="specified",
         spec_section="6b.4 6b.9", plan_step="M8 step 8.1", depends_on="R005",
         notes="Non-negotiable #13. Node side is the exporter path guard WITH its positive control; commons side is stash-box R074 and does not exist yet. A one-sided guard is half a guard"),

    dict(id="R082", thread="privacy",
         title="Denied and consented-out objects are never replication subjects",
         source="owner directive 2026-09-30", priority="high", status="specified",
         spec_section="6b.4", plan_step="M8 step 8.3", depends_on="R081 R005",
         notes="Extends non-negotiable #7 from one path to three: exporter, acquisition queue, replication scheduler. A preservation bounty never overrides a denial"),

    dict(id="R083", thread="privacy", title="Three-state sharing switch off / fetch_only / full",
         source="owner directive 2026-09-30", priority="high", status="specified",
         spec_section="6b.3 6b.6", plan_step="M8 step 8.2", depends_on="R075",
         notes="A boolean cannot express the state a privacy-conscious user actually wants. Enforced at the permission read, never by omitting rows from a query"),

    dict(id="R084", thread="foundation",
         title="P2P downloader ships as part of core with the module boundary and guards retained",
         source="owner directive 2026-09-30", priority="high", status="specified",
         spec_section="6b.8", plan_step="M8 step 8.5", depends_on="R009",
         notes="RETRACTS non-negotiable #11. Module boundary kept: own go.mod, core does not import it, core does not shell out to it. Cost stated in 6b.8: not removable by deleting a directory, the binary grows, and a downloader defect is now a product defect"),

    dict(id="R085", thread="foundation", title="Cross-repo alignment contract between stash and stash-box",
         source="owner directive 2026-09-30", priority="high", status="specified",
         spec_section="ALIGNMENT.md 1-9", plan_step="M8 step 8.0", depends_on="R081",
         notes="docs/ALIGNMENT.md is the tie-break when the two specs disagree, and changing it amends both specs in the same commit. It owns the shared vocabulary so one word cannot mean two things"),

    dict(id="R086", thread="acquisition",
         title="Automatic metadata acquisition filtered by instance taste",
         source="owner directive 2026-09-30", priority="medium", status="specified",
         spec_section="6b.1 6b.2", plan_step="M8 step 8.0", depends_on="R006 R011",
         notes="The do-the-same-for-metadata half. Mostly built (6.5 federation plus pkg/stashbox); what is missing is the taste filter, not a foundation. Metadata replicates UNCONDITIONALLY with no N, because the commons is already the durable copy"),
]


def main():
    with open(PATH, newline="") as f:
        reader = csv.DictReader(f)
        header = reader.fieldnames
        rows = list(reader)

    # Key set, not column count: a dict row carrying the wrong KEYS is caught
    # here, and a count check could not catch it.
    for r in NEW:
        assert set(r) == set(header), \
            "%s keys %s != header %s" % (r.get("id"), sorted(set(r) ^ set(header)), header)

    before = {r["id"]: dict(r) for r in rows}
    rows.extend({k: r[k] for k in header} for r in NEW)

    with open(PATH, "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=header, quoting=csv.QUOTE_MINIMAL)
        w.writeheader()
        w.writerows(rows)

    # ---- validate by re-reading. A test matching zero things passes. ----
    with open(PATH, newline="") as f:
        back = list(csv.DictReader(f))

    ids = [r["id"] for r in back]
    assert len(ids) == len(set(ids)), "duplicate ids: %s" % [i for i in ids if ids.count(i) > 1]
    assert all(i[:1] == "R" and i[1:].isdigit() for i in ids), \
        "an id cell does not look like an id: %s" % [i for i in ids if not (i[:1] == "R" and i[1:].isdigit())]

    # No pre-existing row may change. A rewrite that quietly rewrites 74 rows is
    # a review tax, and the re-quoting this file got is invisible in a diff
    # review -- so it is asserted rather than eyeballed.
    for rid, old in before.items():
        now = next(r for r in back if r["id"] == rid)
        for field, value in old.items():
            assert now[field] == value, \
                "row %s field %s changed: %r -> %r" % (rid, field, value, now[field])

    for r in back:
        assert r["thread"] and r["title"] and r["status"], "empty required cell in %s" % r["id"]
        for dep in r["depends_on"].split():
            assert dep in ids, "%s depends on unknown id %r" % (r["id"], dep)

    from collections import Counter
    print("OK: %d rows (%d pre-existing, %d added), %d columns, 0 field changes"
          % (len(back), len(before), len(NEW), len(header)))
    print("threads:", dict(Counter(r["thread"] for r in back)))


if __name__ == "__main__":
    main()
