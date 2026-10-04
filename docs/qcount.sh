#!/usr/bin/env bash
# Measure how many SQL queries the studio list query actually issues.
#
# WHY THIS EXISTS
# ===============
# Issue #7222 reports the studios page as slow. The hypothesis is a dataloader N+1: gqlgen calls a
# field resolver once per object, and internal/api/resolver_model_studio.go:86-124 has six count
# resolvers that each open their own read transaction and issue their own query. On a page of N
# studios that is 6N count queries plus N transactions.
#
# That is a claim about query COUNT, so it needs to be measured, not argued. This script boots a real
# instance against a seeded library, runs the exact fragment the studios page uses, and reports the
# number of SELECTs the server issued. Run it before and after the loader change: if the fix does not
# reduce the count, the fix is wrong.
#
# The count comes from the process itself, not from timing. Timing would be noisy and would not
# distinguish "one batched query" from "one query per row that happened to be fast".
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
REPO="$PWD"
BUILD="$REPO/.qcount"
PORT="${QCOUNT_PORT:-9979}"

rm -rf "$BUILD"
mkdir -p "$BUILD/data" "$BUILD/generated"
cat > "$BUILD/c.yml" <<EOF
database: $BUILD/data/m.sqlite
generated: $BUILD/generated
host: 127.0.0.1
port: $PORT
ui_location: $REPO/ui/v2.5/build
nobrowser: true
# Trace is what makes the count possible: pkg/sqlite/tx.go:29 logSQL emits every statement at Trace
# (Debug only for slow ones). Without it there is nothing to count and qcount.py says so.
loglevel: Trace
logfile: $BUILD/log
EOF

( setsid "$REPO/.e2e/stash" -c "$BUILD/c.yml" > "$BUILD/stdout" 2>&1 < /dev/null & )
for i in $(seq 1 90); do
  grep -q 'is listening on' "$BUILD/stdout" 2>/dev/null && break
  sleep 1
done
if ! grep -q 'is listening on' "$BUILD/stdout" 2>/dev/null; then
  echo "server did not start; see $BUILD/stdout"
  exit 1
fi
echo "instance up on 127.0.0.1:$PORT"

export QCOUNT_BASE="http://127.0.0.1:$PORT"

# Seed enough studios that an N+1 is unmistakable: 1 row could plausibly be a single query.
python3 - "$QCOUNT_BASE" <<'SEED'
import json, sys, urllib.error, urllib.request

BASE = sys.argv[1]
SEED_N = 12


def gql(query):
    body = json.dumps({"query": query}).encode()
    req = urllib.request.Request(
        BASE + "/graphql", data=body, headers={"Content-Type": "application/json"}
    )
    with urllib.request.urlopen(req, timeout=60) as resp:
        out = json.loads(resp.read())
    if out.get("errors"):
        raise SystemExit(f"seed failed: {out['errors'][0].get('message')}")
    return out["data"]


# Studio names are UNIQUE, and a duplicate returns a 422 with
#   {"errors":[{"message":"studio with name 'X' already exists"}]}
# rather than a null data with no error. The pre-flight exists-query therefore cannot be trusted to
# prevent every duplicate (an alias can collide without the name query seeing it), so the create is
# treated as advisory: a duplicate is a SUCCESS for our purposes, not a failure.
def try_create(name):
    body = json.dumps(
        {"query": 'mutation { studioCreate(input: {name: "%s"}) { id name } }' % name}
    ).encode()
    req = urllib.request.Request(
        BASE + "/graphql", data=body, headers={"Content-Type": "application/json"}
    )
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            return json.loads(resp.read()).get("data", {}).get("studioCreate") is not None
    except urllib.error.HTTPError as e:
        if e.code == 422:
            return False  # already exists -- fine, we wanted it to exist
        raise


M = "studioCreate(input: {name: \"%s\"}) { id name }"
made = 0
for i in range(SEED_N):
    if try_create(f"QC Studio {i}"):
        made += 1
total_now = gql(
    "{ findStudios(studio_filter: {}, filter: {per_page: 200}) { count studios { name } } }"
)["findStudios"]["count"]
print(f"created {made} new studios; library now has {total_now}")
SEED

export QCOUNT_LOG="$BUILD/log"
python3 docs/e2e/qcount.py "${QCOUNT_PER_PAGE:-12}"
rc=$?

# Fewer queries is only half the claim. This compares every batched count against the single-studio
# path at BOTH depths, on a seeded studio hierarchy so depth 0 and depth -1 genuinely differ.
if [ "${QCOUNT_SKIP_EQUIV:-0}" != 1 ]; then
  echo
  QCOUNT_BASE="$QCOUNT_BASE" QCOUNT_LOG="$BUILD/log" python3 docs/e2e/qcount_equiv.py || rc=1
fi

rc=$?
for pid in $(ps -eo pid,args | grep "[s]tash -c $BUILD/c.yml" | awk '{print $1}'); do
  kill "$pid" 2>/dev/null
done
exit "$rc"