#!/usr/bin/env python3
"""Count the SQL queries one studio-list GraphQL query actually issues.

WHY THIS EXISTS
===============
Issue #7222 reports the studios page as slow. The hypothesis is a dataloader N+1: gqlgen invokes a
field resolver once per object in a list, and internal/api/resolver_model_studio.go:86-124 has six
count resolvers (scene/image/gallery/performer/group/scene_marker) that EACH open their own read
transaction via withReadTxn and issue their own query. A page of N studios therefore costs 6N count
queries plus N transactions, on top of the single page query.

That is a claim about query COUNT, so it has to be measured rather than argued. Run this before and
after the loader change. If the fix does not reduce the count, the fix is wrong -- and a timing
measurement would not catch that, because one batched query and one fast query per row can look
identical on a small library.

WHERE THE COUNT COMES FROM
==========================
pkg/sqlite/tx.go:29 logSQL logs EVERY statement the wrapper executes, at Trace level:

    func logSQL(start time.Time, query string, args ...interface{}) {
        if since >= slowLogTime { logger.Debugf("SLOW SQL [%v]: %s, args: %v", ...) }
        else { logger.Tracef("SQL [%v]: %s, args: %v", ...) }
    }

so with `loglevel: Trace` in the config the count is simply the number of SQL lines the server
emitted while serving one request. That is a count of statements the process actually ran, which is
the thing the N+1 claim is about.

The request is timed with a marker query before and after, so background chatter (the scan loop,
schema checks) is excluded rather than guessed at.
"""
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("QCOUNT_BASE", "http://127.0.0.1:9979")
LOG = os.environ.get("QCOUNT_LOG", "")

# The exact fragment the studios page uses: ui/v2.5/graphql/data/studio.graphql requests six counts
# plus a `_all` alias for each, i.e. scene_count(depth: -1) alongside scene_count. That alias pair is
# the reason `depth` must be part of a loader's batching key rather than an afterthought.
COUNT_FIELDS = """
  scene_count
  scene_count_all: scene_count(depth: -1)
  image_count
  image_count_all: image_count(depth: -1)
  gallery_count
  gallery_count_all: gallery_count(depth: -1)
  performer_count
  performer_count_all: performer_count(depth: -1)
  group_count
  group_count_all: group_count(depth: -1)
  scene_marker_count
  scene_marker_count_all: scene_marker_count(depth: -1)
"""

QUERY = """
query CountStudios {
  findStudios(studio_filter: {}, filter: {per_page: %d}) {
    count
    studios { id name %s }
  }
}
"""


def gql(query, timeout=120):
    body = json.dumps({"query": query}).encode()
    req = urllib.request.Request(
        BASE + "/graphql", data=body, headers={"Content-Type": "application/json"}
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read())


def read_log():
    if not LOG or not os.path.exists(LOG):
        return []
    with open(LOG, errors="replace") as fh:
        return fh.read().splitlines()


def main():
    per_page = int(sys.argv[1]) if len(sys.argv) > 1 else 5

    # Marker query so only statements between the markers are counted.
    try:
        gql("{ systemStatus { status } }", timeout=30)
    except (urllib.error.URLError, OSError) as exc:
        print(f"FAIL: instance not reachable at {BASE}: {exc}")
        return 2
    time.sleep(1.0)
    before = len(read_log())

    q = QUERY % (per_page, COUNT_FIELDS)
    t0 = time.time()
    try:
        res = gql(q)
    except Exception as exc:  # noqa: BLE001 - report whatever the server said
        print(f"FAIL: query error: {exc}")
        return 2
    elapsed = time.time() - t0
    time.sleep(1.0)
    after = len(read_log())

    if res.get("errors"):
        print("FAIL: graphql errors:")
        for e in res["errors"][:3]:
            print("   ", e.get("message"))
        return 2

    studios = res["data"]["findStudios"]["studios"]
    total = res["data"]["findStudios"]["count"]
    lines = read_log()
    issued = lines[before:after]
    sql = [ln for ln in issued if re.search(r"\bSQL \[", ln) or "SLOW SQL" in ln]

    print(f"studios returned : {len(studios)} (library has {total})")
    print(f"SQL statements   : {len(sql)}")
    print(f"wall clock       : {elapsed:.2f}s")

    if not sql:
        print()
        print("NOTE: no SQL lines captured. The count needs `loglevel: Trace` in the instance config,")
        print("      because logSQL() emits at Trace for fast queries and Debug only for slow ones.")
        return 3

    # Group by the shape of the statement so the per-row repetition is visible rather than asserted.
    shapes = {}
    for ln in sql:
        m = re.search(r"(?:SLOW )?SQL \[[^\]]*\]:\s*(.*)", ln)
        if not m:
            continue
        shape = re.sub(r"\s+", " ", m.group(1))[:90]
        shapes[shape] = shapes.get(shape, 0) + 1

    print()
    print("top statement shapes (count x shape):")
    for shape, n in sorted(shapes.items(), key=lambda kv: -kv[1])[:8]:
        print(f"  {n:>4}x {shape}")

    per_studio = len(sql) / max(len(studios), 1)
    print()
    print(f"per studio returned: {per_studio:.1f} SQL statements")
    if per_studio > 12:
        print("VERDICT: consistent with the N+1 — counts scale with the number of studios returned.")
    else:
        print("VERDICT: counts do NOT scale with studios — no N+1 for this fragment.")
    return 0


if __name__ == "__main__":
    sys.exit(main())