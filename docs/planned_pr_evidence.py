#!/usr/bin/env python3
"""Name the merged PRs that reference each `planned` row, with their merge commits.

`check_planned_upstream.py` reports "1 merged ref-PR(s)" without saying WHICH -- and the whole
point of a ref-PR is that it is evidence. A merged PR is a fix that landed upstream, which is a
factual disposition; an unmerged one is a proposal that may since have been closed or rewritten.

Prints, per issue: the PR numbers, their merge state, and -- for merged ones -- whether the commit
is present in THIS history. That last column is the one that decides `done`: a PR merged upstream
after this fork's merge base is not in this tree unless the fork merged it later.

    python3 docs/planned_pr_evidence.py 2049 2122 3065
"""
import json
import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parents[1]


def planned_ids():
    out = []
    for line in (REPO / "docs" / "UPSTREAM-ISSUES.md").read_text().splitlines():
        m = re.match(r"^\|\s*(\d+)\s*\|", line)
        if not m:
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) >= 4 and cells[-1] == "planned":
            out.append(int(m.group(1)))
    return out


def ref_prs(num):
    # Built by concatenation, not an f-string: the query itself contains `{` and `}`, and nesting
    # those inside an f-string produced a query gh accepted and GitHub rejected, which the bare
    # `except` below turned into "query failed" for all 69 rows. A harness that reports the same
    # message for a broken query and for a network error is a harness that hides its own bugs --
    # so the exception is now reported rather than swallowed.
    q = (
        '{repository(owner:"stashapp",name:"stash"){issue(number:' + str(num) + "){title state "
        "timelineItems(first:60,itemTypes:[CROSS_REFERENCED_EVENT]){nodes{"
        "... on CrossReferencedEvent{source{... on PullRequest{number state merged mergedAt "
        "mergeCommit{oid} title}}}}}}}}"
    )
    r = subprocess.run(["gh", "api", "graphql", "-f", "query=" + q],
                       capture_output=True, text=True, timeout=180)
    if r.returncode != 0:
        return None, None, [("gh failed (rc=%d): %s" % (r.returncode, r.stderr[:200]))]
    try:
        d = json.loads(r.stdout)["data"]["repository"]["issue"]
    except Exception as e:
        return None, None, ["unparseable response: %s -- %s" % (e, r.stdout[:200])]
    prs = []
    for n in d["timelineItems"]["nodes"]:
        s = n.get("source") or {}
        if s.get("number"):
            prs.append(s)
    return d["title"], d["state"], prs


def in_history(oid):
    """Is this merge commit part of the tree we would actually ship?

    Decided by ANCESTRY FROM MAIN, not by listing containing branches.

    The first version listed `git branch -r --contains <oid>` and looked for upstream/develop in
    the output. On a clone where upstream had never been fetched, that ref did not exist -- so a
    commit already merged into main was reported "not in this history", for a 2022 PR that has
    been in the tree for years. A helper that answers the wrong question confidently is worse
    than one that refuses, so this asks ancestry directly, which needs no remote refs at all.
    """
    if not oid:
        return "no merge commit recorded"
    r = subprocess.run(["git", "merge-base", "--is-ancestor", oid, "main"],
                       cwd=REPO, capture_output=True, text=True, timeout=60)
    if r.returncode == 0:
        return "IN main"
    return "NOT in main"


def main():
    ids = [int(a) for a in sys.argv[1:]] or planned_ids()
    for num in ids:
        title, state, prs = ref_prs(num)
        if title is None:
            print(f"#{num}: query failed")
            continue
        merged = [p for p in prs if p.get("merged")]
        if not merged:
            continue
        print(f"#{num} [{state}] {title[:66]}")
        for p in merged:
            print(f"    PR #{p['number']} merged {p.get('mergedAt','')[:10]}"
                  f"  {in_history((p.get('mergeCommit') or {}).get('oid'))}")
            print(f"       {p['title'][:78]}")


if __name__ == "__main__":
    main()
