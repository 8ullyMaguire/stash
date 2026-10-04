#!/usr/bin/env bash
# Prove migrations 124 and 125 apply cleanly to a fresh database, and check the DDL the
# features depend on. Exits non-zero on any failure so it can be a gate.
set -uo pipefail

REPO=/home/hermes/work/lane-2/stash
RUN=/tmp/migtest
PORT=9975
cd "$REPO" || exit 1

rm -rf "$RUN"; mkdir -p "$RUN/data" "$RUN/generated" || exit 1

echo "== building"
go build -o "$RUN/stash" ./cmd/stash || { echo "FAIL: go build"; exit 1; }

cat > "$RUN/c.yml" <<YAML
database: $RUN/data/t.sqlite
generated: $RUN/generated
host: 127.0.0.1
port: $PORT
ui_location: $REPO/ui/v2.5/build
nobrowser: true
YAML

echo "== starting on a fresh database (every migration runs)"
setsid "$RUN/stash" -c "$RUN/c.yml" > "$RUN/log" 2>&1 < /dev/null &
SPID=$!
trap 'kill $SPID 2>/dev/null' EXIT

for i in $(seq 1 90); do
  grep -q "is listening on" "$RUN/log" 2>/dev/null && break
  grep -qi "config initialization error\|panic:" "$RUN/log" 2>/dev/null && break
  sleep 1
done

if grep -qiE "panic:|config initialization error" "$RUN/log"; then
  echo "FAIL: server refused to start"
  grep -iE "panic:|error" "$RUN/log" | head -5
  exit 1
fi
grep -q "is listening on" "$RUN/log" || { echo "FAIL: never listened"; tail -5 "$RUN/log"; exit 1; }
echo "PASS: server started; migrations applied"

# Ask the app what schema version it reached -- same probe boot-check uses.
SCHEMA=$(curl -s -X POST "http://127.0.0.1:$PORT/graphql" \
  -H 'Content-Type: application/json' \
  -d '{"query":"{ systemStatus { status databaseSchema appSchema } }"}')
echo "== systemStatus: $SCHEMA"
case "$SCHEMA" in
  *'"databaseSchema":125'*) echo "PASS: databaseSchema reached 125" ;;
  *) echo "FAIL: expected databaseSchema 125"; exit 1 ;;
esac

echo "== DDL assertions"
DB="$RUN/data/t.sqlite"
for t in studio_codes scene_directors scene_performer_aliases \
         nationalities performer_nationalities performer_body_marks \
         performer_alias_owners; do
  n=$(sqlite3 "$DB" "select count(*) from sqlite_master where type='table' and name='$t';")
  [ "$n" = "1" ] || { echo "FAIL: table $t missing"; exit 1; }
  echo "  table $t present"
done

echo "== column assertions"
chk() { # table column
  n=$(sqlite3 "$DB" "select count(*) from pragma_table_info('$1') where name='$2';")
  [ "$n" = "1" ] || { echo "FAIL: $1.$2 missing"; exit 1; }
  echo "  $1.$2 present"
}
chk performers tattoos
chk performers piercings
chk performers merged_into_id
chk performer_alias_owners performer_id
chk performer_alias_owners alias
chk performer_alias_owners owner_performer_id

echo "== trigger assertions"
for tr in performers_no_self_merge; do
  n=$(sqlite3 "$DB" "select count(*) from sqlite_master where type='trigger' and name='$tr';")
  [ "$n" = "1" ] || { echo "FAIL: trigger $tr missing"; exit 1; }
  echo "  trigger $tr present"
done

echo "ALL MIGRATION CHECKS PASSED"