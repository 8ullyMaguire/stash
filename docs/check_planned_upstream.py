#!/usr/bin/env python3
"""Query upstream state for every `planned` row in UPSTREAM-ISSUES.md, in one GraphQL call.

The 72 `planned` rows carry no disposition -- just the label they were imported with. Before
triage-by-hand, the cheap question is whether upstream has already resolved them: an issue closed
as `completed` upstream, or a fix that landed, is a factual answer that needs no opinion from me.

One GraphQL query rather than 72 `gh issue view` calls, because the latter is ~40s and rate-limits.

Writes nothing. Prints a table for the caller to decide from.
"""
import json
import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parents[1]
LEDGER = REPO / "docs" / "UPSTREAM-ISSUES.md"


def planned_ids():
    out = []
    for line in LEDGER.read_text().splitlines():
        m = re.match(r"^\|\s*(\d+)\s*\|", line)
        if not m:
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) >= 4 and cells[-1] == "planned":
            out.append((int(m.group(1)), cells[1]))
    return out


def fetch(ids):
    """Return {number: {state, title, labels, closedByPRs}} via GraphQL, chunked."""
    result = {}
    owner, name = "stashapp", "stash"
    for i in range(0, len(ids), 25):
        chunk = ids[i : i + 25]
        parts = []
        for j, num in enumerate(chunk):
            parts.append(
                f'p{j}: repository(owner:"{owner}", name:"{name}") {{'
                f'  issue(number:{num}) {{'
                f'    number title state'
                f'    labels(first:12) {{ nodes {{ name }} }}'
                f'    timelineItems(first:50, itemTypes:[CROSS_REFERENCED_EVENT]) {{'
                f'      nodes {{ ... on CrossReferencedEvent {{'
                f'        source {{ ... on PullRequest {{ state merged mergedAt title }} }}'
                f'      }} }}'
                f'    }}'
                f'  }}'
                f'}}'
            )
        query = "{" + " ".join(parts) + "}"
        r = subprocess.run(
            ["gh", "api", "graphql", "-f", f"query={query}"],
            capture_output=True, text=True, timeout=300,
        )
        if r.returncode != 0:
            print(f"graphql failed: {r.stderr[:300]}", file=sys.stderr)
            continue
        data = json.loads(r.stdout).get("data") or {}
        for j, num in enumerate(chunk):
            node = (data.get(f"p{j}") or {}).get("issue")
            if not node:
                continue
            tl = node.get("timelineItems") or {}
            refs = []
            for n in (tl.get("nodes") or []):
                src = n.get("source")
                if src and src.get("merged"):
                    refs.append(src)
            result[num] = {
                "state": node.get("state"),
                "title": node.get("title"),
                "labels": [x["name"] for x in ((node.get("labels") or {}).get("nodes") or [])],
                "mergedPRs": refs,
            }
    return result


def main():
    rows = planned_ids()
    data = fetch([n for n, _ in rows])
    titles = dict(rows)
    tally = {}
    for num in sorted(data):
        d = data[num]
        merged = d["mergedPRs"]
        if d["state"] == "OPEN":
            verdict = f"open, {len(merged)} merged ref-PR(s)" if merged else "open, no merged ref-PR"
        else:
            verdict = "CLOSED upstream"
            if merged:
                verdict += f" ({len(merged)} merged PR)"
        tally[verdict] = tally.get(verdict, 0) + 1
        print(f"#{num:<6} {verdict:<28} {titles[num][:56]}")
        for pr in merged[:3]:
            print(f"          └ PR #{pr.get('number','?')} {str(pr.get('title',''))[:60]}")
    print("\nTALLY:")
    for k, v in sorted(tally.items(), key=lambda kv: -kv[1]):
        print(f"  {v:3d}  {k}")
    print(f"\n{len(data)}/{len(rows)} resolved from the API")


if __name__ == "__main__":
    raise SystemExit(main())
