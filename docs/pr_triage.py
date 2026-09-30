#!/usr/bin/env python3
"""Fetch the open upstream PR queue and classify it for triage.

Phase 1 of GOAL-stashforge.md needs a DECISION recorded for every open PR, and
70 PRs read one at a time is a day of context. This builds the working set: one
row per PR with the facts a decision needs, so the reading is targeted rather
than exhaustive.

What it deliberately does NOT do is decide. Classification is by mechanical,
checkable signals (draft state, age, mergeability, CI, whether it touches
something a non-negotiable protects) and every one of them is recorded as a
signal with its source. A classifier that emitted a verdict would be a verdict
nobody checked, which is the failure this project keeps paying for.

Run:
    python3 docs/pr_triage.py fetch     # write docs/pr-queue.json
    python3 docs/pr_triage.py report    # print the triage table

Re-run `fetch` before `report` when the queue may have moved: this is a
snapshot, and a snapshot with no date on it is a stale document.
"""

import json
import subprocess
import sys
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path

REPO = "stashapp/stash"
OUT = Path(__file__).resolve().parent / "pr-queue.json"

# Deliberately WITHOUT `body`. The body is ~4KB per PR and 70 of them is the
# difference between one query and a 502; and the body is only needed for the
# PRs a human actually reads. `body <N>` fetches it for one PR on demand.
FIELDS = ",".join([
    "number", "title", "author", "isDraft", "createdAt", "updatedAt",
    "mergeable", "mergeStateStatus", "additions", "deletions", "changedFiles",
    "baseRefName", "headRefName", "url", "labels", "reviewDecision",
    "statusCheckRollup", "maintainerCanModify",
])


def gh(*args, timeout=300, retries=4):
    """Run gh and return stdout, retrying a TRANSIENT failure.

    Measured: the full-field query returned `HTTP 502 Bad Gateway` from
    api.github.com/graphql on the first attempt and succeeded on every one after,
    with a 3s gap. A single-shot call would have reported "the queue is
    unavailable" for what is a momentary upstream blip, and a triage that gives
    up is a triage nobody trusts the second time.

    Only 5xx and transport errors are retried. A 403 or a 422 is an answer, and
    retrying it just delays the message that says what is actually wrong.
    """
    last = ""
    for attempt in range(retries):
        p = subprocess.run(
            ["gh", *args], capture_output=True, text=True, timeout=timeout,
        )
        if p.returncode == 0:
            return p.stdout
        last = p.stderr
        transient = ("502" in last or "503" in last or "504" in last
                     or "Bad Gateway" in last or "timeout" in last.lower()
                     or "connection" in last.lower())
        if not transient:
            break
        if attempt < retries - 1:
            time.sleep(3 * (attempt + 1))
    sys.exit(f"gh {' '.join(args)} failed after {retries} attempt(s):\n{last[:2000]}")


PAGE = 25


def fetch():
    """Build the snapshot, one PR at a time by NUMBER.

    Three paging strategies were measured before this one, and the reasons they
    failed are the reason this works:

    1. One call at --limit 200/100/70 -> HTTP 502 from api.github.com/graphql.
       Every field works alone; the full set works at limit 30; and the SAME
       query at limit 70 passed once and failed the next time. So it is a
       payload threshold with the timeout beside it, not a bad field.

    2. `gh pr list --page N` -> "unknown flag: --page". The flag does not exist.

    3. Paging with `--search created:>=<date> sort:created-asc` advanced the
       cursor by exactly ONE new PR per call, because the first 25 rows of any
       such window are all older than the advancing boundary. Instrumented, it
       walked 2023-08 -> 2024-09 gaining 1 row per iteration, which is a loop
       that never ends on a queue of 70.

    So: ask for one PR per call, by number, over the range that exists. 70
    calls is a minute and it cannot 502, because the payload is one PR. The
    numbers come from the cheap unfiltered listing, so nothing is invented here
    and the expensive part is only ever asked about a PR that exists.
    """
    # The cheap call -- no statusCheckRollup, so it does not hit the threshold.
    numbers = json.loads(gh("pr", "list", "--repo", REPO, "--state", "open",
                            "--limit", "200", "--json", "number"))
    todo = [r["number"] for r in numbers]
    print(f"fetching {len(todo)} PRs, one call each")

    prs = []
    for i, n in enumerate(todo, 1):
        raw = gh("pr", "view", str(n), "--repo", REPO, "--json", FIELDS)
        # `gh pr view N` returns ONE object; `gh pr list` returns an array.
        # Mixing the two shapes is a TypeError three lines later that names
        # neither the cause nor the PR, so the shape is asserted here.
        obj = json.loads(raw)
        assert isinstance(obj, dict) and "number" in obj, f"pr view {n} returned {type(obj)}"
        prs.append(obj)
        if i % 10 == 0:
            print(f"  {i}/{len(todo)}")

    now = datetime.now(timezone.utc)
    rows = []
    for p in prs:
        checks = p.get("statusCheckRollup") or []
        # gh returns entries with `conclusion` (already run) or `state` (pending).
        conclusions = [c.get("conclusion") for c in checks if c.get("conclusion")]
        pending = [c for c in checks if not c.get("conclusion") and not c.get("state")]

        created = p.get("createdAt") or ""
        try:
            age_days = (now - datetime.fromisoformat(created.replace("Z", "+00:00"))).days
        except Exception:
            age_days = None

        rows.append({
            "number": p["number"],
            "title": p["title"],
            "author": (p.get("author") or {}).get("login"),
            "isDraft": p.get("isDraft", False),
            "createdAt": created,
            "ageDays": age_days,
            "updatedAt": p.get("updatedAt"),
            "mergeable": p.get("mergeable"),
            "mergeStateStatus": p.get("mergeStateStatus"),
            "reviewDecision": p.get("reviewDecision"),
            "additions": p.get("additions"),
            "deletions": p.get("deletions"),
            "changedFiles": p.get("changedFiles"),
            "base": p.get("baseRefName"),
            "head": p.get("headRefName"),
            "url": p.get("url"),
            "labels": [l["name"] for l in (p.get("labels") or [])],
            "ciTotal": len(checks),
            "ciFailed": [c for c in conclusions if c not in ("SUCCESS", "NEUTRAL", "SKIPPED")],
            "ciPending": len(pending),
            # No body here. Fetch it per-PR with `body <N>` when reading one.
        })

    rows.sort(key=lambda r: r["number"])
    snapshot = {
        "repo": REPO,
        "fetchedAt": now.isoformat(),
        "count": len(rows),
        "prs": rows,
    }
    OUT.write_text(json.dumps(snapshot, indent=1))
    print(f"wrote {OUT} -- {len(rows)} open PRs, fetched {snapshot['fetchedAt']}")


def classify(r):
    """Mechanical signals only. Returns (bucket, why) and NEVER a verdict.

    The buckets are reading order, not judgement:
      stale-draft    -- a draft nobody has touched in 180+ days
      stale          -- ready, untouched 180+ days
      conflicted     -- git says the merge is not clean
      ci-red         -- a check that ran and failed
      ci-unknown     -- a check still running
      blocked-review  -- changes requested, or a maintainer objected
      fresh          -- everything else
    """
    if r["mergeable"] == "CONFLICTING":
        return "conflicted", "git reports the merge is not clean"
    if r["reviewDecision"] == "CHANGES_REQUESTED":
        return "blocked-review", "a reviewer requested changes"
    if r["ciFailed"]:
        return "ci-red", f"{len(r['ciFailed'])} check(s) failed"
    if r["ciPending"]:
        return "ci-unknown", f"{r['ciPending']} check(s) still running"
    age = r["ageDays"]
    if age is not None and age >= 180:
        return ("stale-draft" if r["isDraft"] else "stale"), f"untouched {age}d"
    return "fresh", ""


def report():
    if not OUT.exists():
        sys.exit("no snapshot -- run `python3 docs/pr_triage.py fetch` first")
    snap = json.loads(OUT.read_text())
    print(f"# open PRs on {snap['repo']} — {snap['count']} — snapshot {snap['fetchedAt']}\n")

    buckets = {}
    for r in snap["prs"]:
        b, why = classify(r)
        buckets.setdefault(b, []).append((r, why))

    order = ["conflicted", "ci-red", "ci-unknown", "blocked-review",
             "stale-draft", "stale", "fresh"]
    for b in order:
        if b not in buckets:
            continue
        rows = sorted(buckets[b], key=lambda x: x[0]["number"])
        print(f"## {b} ({len(rows)})")
        for r, why in rows:
            flags = []
            if r["isDraft"]:
                flags.append("draft")
            if r["changedFiles"] and r["changedFiles"] > 40:
                flags.append(f"{r['changedFiles']}files")
            if r["ageDays"] is not None and r["ageDays"] >= 365:
                flags.append(f"{r['ageDays']}d")
            tail = ("  [" + ", ".join(flags) + "]") if flags else ""
            print(f"  #{r['number']:<5} {r['title'][:66]:<66} {r['author'][:16]:<16}{tail}")
        print()

    print("Counts:", {k: len(v) for k, v in sorted(buckets.items())})
    print(f"\nTotal {sum(len(v) for v in buckets.values())} of {snap['count']}.")


def body(number):
    """The PR body on demand. The title frequently is not the requirement --
    the same trap the issue roster warns about -- so this exists rather than
    triaging from titles alone."""
    raw = gh("pr", "view", str(number), "--repo", REPO, "--json",
             "number,title,body,files,commits")
    d = json.loads(raw)
    print(f"#{d['number']} {d['title']}\n")
    print(d.get("body") or "(no body)")
    print("\n--- files ---")
    for f in d.get("files") or []:
        print(f"  {f.get('additions', 0):>5}+ {f.get('deletions', 0):<5} {f['path']}")
    print(f"\n--- {len(d.get('commits') or [])} commit(s) ---")
    for c in d.get("commits") or []:
        print(f"  {c['oid'][:9]} {c['messageHeadlineHTML']}")


def main():
    if len(sys.argv) < 2 or sys.argv[1] not in ("fetch", "report", "body"):
        sys.exit(__doc__)
    if sys.argv[1] == "body":
        if len(sys.argv) < 3:
            sys.exit("usage: pr_triage.py body <PR-number>")
        return body(sys.argv[2])
    {"fetch": fetch, "report": report}[sys.argv[1]]()


if __name__ == "__main__":
    main()
