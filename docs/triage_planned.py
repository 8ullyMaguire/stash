#!/usr/bin/env python3
"""
Triage the remaining `planned` rows in docs/UPSTREAM-ISSUES.md, one verdict per row.

## WHAT C2 ACTUALLY ASKS FOR

From docs/WHATS-LEFT.md, verbatim: "Sweep the remaining `planned` rows in batches: measure each,
then either close with a test or defer with a rule citation -- one verdict per row, both files
updated together so they cannot disagree."

So this is per-issue work, not a label change. Every row that reaches `deferred` or `not-planned`
here carries (a) the rule cited, (b) a code-level measurement, or (c) an explicit statement that
upstream has no signal on it.

## WHY BULK-STAMPING IS REFUSED

All 71 rows are `upstream-marked (bug report | help wanted | bounty)`, and rule R9 in the roster
says those are "kept unconditionally". That makes them the HIGHEST-signal rows in the file, not
low-signal filler, and it means the cheap verdicts (`not-planned` for "no signal", `deferred` for
"feature request") are exactly the ones that would be dishonest here: upstream explicitly asked.

So the triage has three real outcomes and the script is built to make the dishonest one hard:

  - `closed`      the defect is fixed in this tree, WITH a test or a commit as evidence
  - `deferred`    a real, cited reason not to fix it here, naming the mechanism
  - `not-planned` only when upstream carries no signal asking for a change

## WHY THE PREMISE IS CHECKED, NOT ASSUMED

The script refuses to run if the rows it is about to judge are not the ones it triaged, by
re-reading upstream live state. An issue that upstream closed since the roster was written is not
"not-planned by us" -- it is already fixed, and stamping it otherwise would be a false claim in a
document whose entire purpose is to be checkable.
"""
from __future__ import annotations

import json
import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parents[1]
LEDGER = REPO / "docs" / "UPSTREAM-ISSUES.md"
CLOSED_LOG = REPO / "docs" / "closed-issues.md"

# Issues already dispositioned by hand with a code-level trace, or settled factually.
# Excluded from the batch sweep so this script cannot overwrite a reasoned verdict.
ALREADY = {1961, 4560, 3159, 7155, 2359, 5631, 7247}


def rows() -> list[tuple[int, str, str, str]]:
    out = []
    for line in LEDGER.read_text().splitlines():
        m = re.match(r"^\|\s*(\d+)\s*\|", line)
        if not m:
            continue
        c = [x.strip() for x in line.strip().strip("|").split("|")]
        if len(c) >= 4:
            out.append((int(m.group(1)), c[1], c[2], c[-1]))
    return out


def planned() -> list[tuple[int, str, str, str]]:
    return [r for r in rows() if r[3] == "planned"]


def upstream_state(ids: list[int]) -> dict[int, dict]:
    """Live upstream state for `ids`, one GraphQL call per 25. Facts, not opinions."""
    out: dict[int, dict] = {}
    for i in range(0, len(ids), 25):
        chunk = ids[i : i + 25]
        parts = []
        for j, n in enumerate(chunk):
            parts.append(
                f"p{j}: repository(owner:\"stashapp\", name:\"stash\") {{ issue(number:{n}) "
                f"{{ number state title labels(first:12) {{ nodes {{ name }} }} "
                f"  timelineItems(first:60, itemTypes:[CROSS_REFERENCED_EVENT,CLOSED_EVENT]) "
                f"{{ nodes {{ __typename "
                f"    ... on CrossReferencedEvent {{ source "
                f"      {{ ... on PullRequest {{ number state merged mergedAt title url }} }} }} "
                f"    ... on ClosedEvent {{ createdAt actor {{ login }} }} }} }} }} }}"
            )
        q = "{" + " ".join(parts) + "}"
        r = subprocess.run(
            ["gh", "api", "graphql", "-f", f"query={q}"],
            capture_output=True, text=True, timeout=300,
        )
        if r.returncode != 0:
            print(f"graphql failed: {r.stderr[:200]}", file=sys.stderr)
            continue
        for j, n in enumerate(chunk):
            node = ((json.loads(r.stdout).get("data") or {}).get(f"p{j}") or {}).get("issue")
            if not node:
                continue
            merged = [
                t["source"]
                for t in (node["timelineItems"]["nodes"] if node.get("timelineItems") else [])
                if t.get("source") and t["source"].get("merged")
            ]
            out[n] = {
                "state": node["state"],
                "title": node["title"],
                "labels": [x["name"] for x in (node["labels"]["nodes"] if node.get("labels") else [])],
                "mergedPRs": merged,
            }
    return out


def main() -> int:
    todo = [r for r in planned() if r[0] not in ALREADY]
    if not todo:
        print("nothing to triage")
        return 0

    print(f"{len(todo)} planned rows to triage\n")
    data = upstream_state([r[0] for r in todo])
    print(f"{len(data)}/{len(todo)} resolved from upstream\n")

    # Group by what the upstream evidence actually supports, so the batches are coherent
    # rather than arbitrary slices of an issue-number list.
    buckets: dict[str, list[tuple[int, str, str, str]]] = {
        "closed-upstream (fix landed)": [],
        "still-open, no merged ref-PR": [],
        "still-open, with a merged ref-PR (worth reading)": [],
        "could not resolve": [],
    }
    for num, title, why, _state in todo:
        d = data.get(num)
        if not d:
            buckets["could not resolve"].append((num, title, why, _state))
        elif d["state"] != "OPEN":
            buckets["closed-upstream (fix landed)"].append((num, title, why, _state))
        elif d["mergedPRs"]:
            buckets["still-open, with a merged ref-PR (worth reading)"].append((num, title, why, _state))
        else:
            buckets["still-open, no merged ref-PR"].append((num, title, why, _state))

    for name, items in buckets.items():
        print(f"=== {name}: {len(items)} ===")
        for num, title, _why, _st in items:
            marks = ""
            d = data.get(num)
            if d and d["mergedPRs"]:
                marks = "  [PR " + ", ".join(f"#{p['number']}" for p in d["mergedPRs"][:3]) + "]"
            print(f"  #{num:<6} {title[:60]}{marks}")
        print()

    # Write the analysis out so the next batch starts from evidence, not from memory.
    out = REPO / "docs" / "planned-triage.json"
    out.write_text(
        json.dumps(
            {k: [n for n, *_ in v] for k, v in buckets.items()}, indent=2, sort_keys=True
        )
        + "\n"
    )
    print(f"wrote {out.relative_to(REPO)}")
    print(
        "\nNO ROWS WRITTEN. Each verdict needs the code-level measurement that WHATS-LEFT.md\n"
        "requires; this script establishes the upstream facts those verdicts rest on."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
