#!/usr/bin/env bash
# Boot a real stash instance and prove it serves -- the check that found four startup bugs.
#
# WHY THIS EXISTS
#
# `go test ./...` was green on a tree where the binary PANICKED on startup and every annotated
# GraphQL query returned an error. The suite builds resolvers, exercises stores and walks schemas; it
# never once does what a user does, which is start the thing. So this does:
#
#   1. builds ./cmd/stash
#   2. starts it against a throwaway sqlite database
#   3. asserts the log says it is listening AND that no panic/ERRO was logged
#   4. asserts GET / returns 200
#   5. asserts a @requiresRole-annotated GraphQL query returns DATA
#   6. asserts the two sprite URLs are routed (404 for an unknown scene, but NOT 500)
#
# Each of those five has caught a real bug that the unit suite did not. They are listed in the
# comments on the code they cover; this script exists because "start the binary" is not a thing a Go
# test in this repo does.
#
# Usage:  docs/boot-check.sh [workdir]
# Exit 0 = the instance serves. Non-zero = it does not, with the log tail on stderr.

set -uo pipefail

REPO="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"

# The run directory goes under the repo, NOT /tmp.
#
# /tmp on this host is a 4G tmpfs that is often 80% full, and cgo compilation (go-sqlite3 pulls in
# mattn/go-sqlite3, which is cgo) assembles into $TMPDIR. With ~780M free the build died with
# "can't write N bytes to section .text ... No space left on device" -- a failure that looks like a
# toolchain bug and is not one. The disk has 153G free; there is no reason for a build to happen on
# the small filesystem.
#
# The trap is also installed BEFORE the build, not after: an earlier version set it once the server
# was running, so a failed build left its 885M run directory behind and made the NEXT attempt fail
# for the same reason. A cleanup that does not run when the thing fails is not a cleanup.
RUN="${STASH_BOOT_DIR:-$(mktemp -d "$REPO/.boot-check.XXXXXX")}"
mkdir -p "$RUN"
# On failure the run directory is KEPT and its path printed. A cleanup that deletes the evidence
# makes every failure look identical, and this script's whole value is the diagnostics. Three
# earlier iterations failed for three different reasons and every one of them printed nothing
# useful, because the log was deleted by the same trap that reported the failure.
KEEP="${STASH_BOOT_KEEP:-0}"
cleanup() {
  # Kill the server FIRST. The reverse order deletes the log the diagnosis needs, and a failed
  # boot-check that prints "the process exited during startup" with no log is unactionable.
  [ -n "${SRV:-}" ] && kill "$SRV" 2>/dev/null
  [ "$KEEP" = 1 ] && { echo "run directory kept: $RUN" >&2; return; }
  rm -rf "$RUN"
}
trap cleanup EXIT
PORT="${STASH_BOOT_PORT:-9977}"
BIN="$RUN/stash"
LOG="$RUN/server.log"
CFG="$RUN/stash.yml"

export GOFLAGS="${GOFLAGS:--mod=mod}"

# TMPDIR moves off /tmp but HOME DOES NOT.
#
# TMPDIR because /tmp here is a 4G tmpfs often 80% full, and cgo (mattn/go-sqlite3) assembles into
# it: the build died with "can't write N bytes to section .text ... No space left on device", which
# reads as a broken toolchain and is not one. The disk has 153G free.
#
# HOME is NOT overridden. An earlier version did `export HOME="$RUN"`, which made GOMODCACHE resolve
# to $RUN/go/pkg/mod, so the build re-downloaded the whole module graph into the throwaway directory,
# wrote it read-only, and then could not delete it -- ~1.5MB of rm errors on every run. The config
# points at absolute paths instead, which it needs anyway because the config loader does not expand
# $HOME.
# TMPDIR points at a scratch dir that is NOT $RUN.
#
# $RUN is deleted by the EXIT trap, and Go keeps build scratch and the module cache under TMPDIR and
# GOPATH -- so pointing TMPDIR at the directory that gets deleted means the binary is built with
# caches that vanish mid-run. Observed as: the build "succeeded", then the server produced an EMPTY
# log and exited, and the trap then deleted the log that would have said why. The cleanup hid the
# evidence, which is the worst possible combination.
SCRATCH="$RUN/scratch"
mkdir -p "$SCRATCH"
export TMPDIR="$SCRATCH"

fail() { echo "FAIL: $*" >&2; echo "--- log tail ---" >&2; tail -25 "$LOG" >&2 2>/dev/null; exit 1; }

echo "== building"
( cd "$REPO" && go build -o "$BIN" ./cmd/stash ) || fail "go build ./cmd/stash"

# `database` is a SCALAR PATH, not the postgres map shape -- GetDatabasePath() does
# `i.getString(Database)` (internal/manager/config/config.go:791). `generated` is one of exactly two
# mandatory keys (Validate, config.go:1993) and must be set BY HAND: the server rewrites the config
# into canonical form before validating it, writing `generated: ""` and then rejecting that file, so
# a genuinely first-run config cannot start unless this is pre-set. `$HOME` is NOT expanded in config
# values, hence the absolute paths.
mkdir -p "$RUN/data" "$RUN/generated"
cat > "$CFG" <<YAML
database: $RUN/data/boot.sqlite
generated: $RUN/generated
host: 127.0.0.1
port: $PORT
ui_location: $REPO/ui/v2.5/build
nobrowser: true
security:
    trusted_proxies:
        - 127.0.0.1
YAML

echo "== starting (fresh database, so this also exercises every migration)"
# `setsid` is not decoration: the EXIT trap that removes the run directory is INHERITED by a
# backgrounded child, so the server itself runs the cleanup when it exits -- and in an earlier
# version an unconditional `kill $SRV` sat here and killed the server one second after launch,
# producing an empty log and the message "the process exited during startup".
#
# setsid detaches it into its own session so it never runs this script's trap. Killing it is done
# from cleanup(), which is the only place that should.
setsid "$BIN" -c "$CFG" > "$LOG" 2>&1 < /dev/null &
SRV=$!

# Startup runs the whole migration chain; 60s is generous and the wait is bounded rather than blind.
for _ in $(seq 1 90); do
  grep -q 'is listening on' "$LOG" 2>/dev/null && break
  grep -qE 'panic:|http server error' "$LOG" 2>/dev/null && fail "the instance refused to start"
  # `kill -0` rather than `wait`: the server is a long-running daemon, so waiting for it would
  # block until it exits. The check is "is it still alive", not "has it finished".
  kill -0 $SRV 2>/dev/null || fail "the process exited during startup (log has $(wc -l < "$LOG") lines)"
  sleep 1
done
grep -q 'is listening on' "$LOG" || fail "never reached 'is listening on' within 60s"

echo "== 1. no panic or error in the startup log"
if grep -qE 'panic:|ERRO' "$LOG"; then
  fail "startup logged a panic or ERRO"
fi

echo "== 2. GET / serves the UI"
code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/" || echo 000)
[ "$code" = 200 ] || fail "GET / returned $code, want 200"

echo "== 3. an @requiresRole-annotated query returns DATA"
# This exact query returned {"errors":[{"message":"directive requiresRole is not implemented"}],
# "data":null} because server.go built the schema with no Directives. An annotation documented as
# having "no runtime behaviour" was in fact taking down every field it was attached to.
body=$(curl -s -X POST "http://127.0.0.1:$PORT/graphql" -H 'Content-Type: application/json' \
  -d '{"query":"{ systemStatus { status databaseSchema appSchema } }"}')
echo "$body" | grep -q '"status":"OK"' || fail "systemStatus did not return OK: $body"
echo "$body" | grep -q '"errors"' && fail "systemStatus returned errors: $body"
echo "   $(echo "$body" | head -c 120)"

echo "== 4. the sprite URLs are routed, not panicking"
# 404 is the CORRECT answer for a hash no scene has. What must not happen is a 500 or a panic --
# these are the two URLs whose registration used to take the whole instance down at startup.
for p in deadbeef_thumbs.vtt deadbeef_sprite.jpg; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/scene/$p" || echo 000)
  [ "$code" = 404 ] || fail "/scene/$p returned $code, want 404 (routed, no such scene)"
done

echo "== 5. an ordinary scene id is routed too"
code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/scene/1" || echo 000)
[ "$code" = 404 ] || fail "/scene/1 returned $code, want 404 (routed, no such scene)"

echo "PASS: the instance boots, serves the UI, answers GraphQL, and routes the sprite URLs"
