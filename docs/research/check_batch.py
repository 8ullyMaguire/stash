#!/usr/bin/env python3
"""Acceptance check for a remap batch.

Written 2026-09-27 after the first remap attempt produced structurally valid,
semantically worthless output. This script exists because the checks that would
have caught it were mechanical, and I had left them to a subagent's judgement.

Usage:
    python3 docs/research/check_batch.py docs/research/batches/b01.json \
                                           docs/research/answer/b01.json

Exit 0 = accept, 1 = reject. Every failure prints what is wrong.
"""
import json
import re
import sys
from collections import Counter

TAXONOMY = "docs/research/taxonomy-reference.md"


def load_codes():
    with open(TAXONOMY, encoding="utf-8") as fh:
        return set(re.findall(r"^([CX]\d\d) ", fh.read(), re.M))


def main():
    if len(sys.argv) != 3:
        print(__doc__)
        return 1
    batch_path, answer_path = sys.argv[1], sys.argv[2]

    with open(batch_path, encoding="utf-8") as fh:
        batch = json.load(fh)
    with open(answer_path, encoding="utf-8") as fh:
        answer = json.load(fh)

    known = load_codes()
    wanted = {i["key"] for i in batch}
    got = set(answer)
    failures = []

    # 1. Coverage. Every issue exactly once.
    missing, extra = wanted - got, got - wanted
    if missing:
        failures.append(f"{len(missing)} issues missing: {sorted(missing)[:8]}")
    if extra:
        failures.append(f"{len(extra)} answers for issues not in batch: {sorted(extra)[:8]}")

    # 2. Codes are real.
    unknown = sorted({v["cap"] for v in answer.values() if v["cap"] not in known})
    if unknown:
        failures.append(f"capability codes not in taxonomy: {unknown}")

    # 3. THE CHECK THAT MATTERS: the quoted evidence must be falsifiable.
    #    An earlier attempt passed every count and code check while 224 of 283
    #    "why" fields were the issue title pasted back. A quote that is not a
    #    substring of the issue text is a fabrication, and a quote that IS the
    #    title verbatim is the same failure in a different shape.
    by_key = {i["key"]: i for i in batch}
    fabricated, echoed, thin = [], [], []
    for key, val in answer.items():
        issue = by_key.get(key)
        if issue is None:
            continue
        quote = (val.get("quote") or "").strip()
        reason = (val.get("why") or "").strip()
        haystack = f"{issue['title']} {issue['excerpt']}".lower()
        norm = re.sub(r"\s+", " ", haystack)

        if not quote:
            failures.append(f"{key}: empty quote")
            continue
        if re.sub(r"\s+", " ", quote.lower()) not in norm:
            fabricated.append(key)
        # The title pasted back as its own justification.
        if re.sub(r"\s+", " ", quote.lower()) == re.sub(r"\s+", " ", issue["title"].lower()):
            echoed.append(key)
        # "why" must add something the quote does not already say: a reason, not
        # a restatement. Too short to carry a clause is the tell.
        if len(reason.split()) < 4:
            thin.append(key)
    if fabricated:
        failures.append(
            f"{len(fabricated)} quotes are not found in the issue text "
            f"(fabricated evidence): {sorted(fabricated)[:8]}"
        )
    if echoed:
        failures.append(
            f"{len(echoed)} quotes are the issue title verbatim, which is the "
            f"thing being classified and cannot justify it: {sorted(echoed)[:8]}"
        )
    if thin:
        failures.append(
            f"{len(thin)} reasons are under 4 words, too short to state a "
            f"reason: {sorted(thin)[:8]}"
        )

    # 4. Known-answer probes. These have a defensible right answer, agreed
    #    before the batch ran, and a scorer cannot get them right by accident.
    probes = [(k, v) for k, v in ((i["key"], i["probe"]) for i in batch) if v]
    probe_fail = [(k, answer[k]["cap"], p) for k, p in probes if k in answer and answer[k]["cap"] != p]
    if probe_fail:
        detail = ", ".join(f"{k}: said {s}, expected {p}" for k, s, p in probe_fail)
        failures.append(f"{len(probe_fail)}/{len(probes)} known-answer probes missed: {detail}")

    # 5. Degenerate distribution. A single capability swallowing a large share
    #    is the signature of a keyword scorer, not of judgement. Reported as a
    #    warning with the real numbers rather than a hard failure, because a
    #    genuinely large capability can legitimately dominate.
    counts = Counter(v["cap"] for v in answer.values())
    top_cap, top_n = counts.most_common(1)[0]
    spread = len(counts)
    banner = (
        f"distribution: {spread} distinct capabilities; largest is {top_cap} "
        f"at {top_n}/{len(answer)} ({100 * top_n // max(len(answer), 1)}%)"
    )
    degenerate = top_n > 0.25 * len(answer)

    if failures:
        print(f"REJECT {batch_path}")
        for f in failures:
            print(f"  - {f}")
        print(f"  {banner}")
        return 1

    print(f"ACCEPT {batch_path}")
    print(f"  {len(answer)} answers, {spread} distinct capabilities, "
          f"{len(probes)} probes all correct")
    print(f"  {banner}")
    if degenerate:
        print("  NOTE: one capability holds >25% of this batch -- confirm by "
              "reading it, do not accept on the strength of this script")
    return 0


if __name__ == "__main__":
    sys.exit(main())
