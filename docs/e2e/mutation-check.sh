#!/usr/bin/env bash
# Prove the e2e suite CATCHES regressions, rather than merely passing.
#
# A green browser suite is the weakest evidence in software. A suite that asserts against `body` text
# passes on a totally broken app, because the navbar renders on every route -- which is exactly
# what the first version of this suite did, reporting all nine routes OK while eight of them were
# indistinguishable. This injects real defects into a COPY of the tree and asserts the suite reports
# each one.
#
# Two halves, and the first is the one people skip:
#
#   0. the BASELINE. The unmutated copy must PASS. Otherwise "the mutant was killed" only means the
#      tree was already red, and every result below is meaningless.
#   1. the mutants. Each must produce a FAIL.
#
# The mutations live in apply-mutant.py, not in heredocs here: a python heredoc nested inside a
# bash heredoc needs a delimiter absent from both, and getting that wrong corrupts this file
# silently. It already happened once -- an anchor that did not exist produced a no-op mutant that
# was then reported as "the suite does not catch this", pointing at the suite instead of the harness.
set -uo pipefail

REPO="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
cd "$REPO"

# The scratch tree lives OUTSIDE the repo by default.
#
# It used to be $REPO/.e2e-mut, and that broke an unrelated test:
#
#   --- FAIL: TestAProfileClaimIsLabelledAClaim
#       a peer's claim must never be reachable from a sizing path:
#       [.e2e-mut/app/internal/mesh/protocol.go references ClaimStoreBytes ...]
#
# internal/mesh/claim_guard_test.go walks the whole repo tree looking for a forbidden field name in
# any .go file. A staged COPY of the repo inside the repo therefore doubles every source file, and the
# guard sees its own duplicate. A test that scans the tree is sensitive to where you put your
# scratch -- and the honest reading of that failure was not "my mutation leaked" but "my harness put
# a second copy of the tree where a scanner could find it".
BUILD="${E2E_BUILDDIR:-${TMPDIR:-/tmp}/stash-e2e-mut}"
MUT="$BUILD/app"
PORT="${E2E_PORT:-9982}"
SUITE_DIR="$REPO/docs/e2e"
APPLY="$SUITE_DIR/apply-mutant.py"
export E2E_BASE="http://127.0.0.1:$PORT"

killed=0; survived=0; broken=0

start_app() {
  mkdir -p "$BUILD/data" "$BUILD/generated"
  cat > "$BUILD/c.yml" <<EOF
database: $BUILD/data/m.sqlite
generated: $BUILD/generated
host: 127.0.0.1
port: $PORT
ui_location: $MUT/ui/v2.5/build
nobrowser: true
EOF

  # REFUSE TO START IF THE PORT IS ALREADY HELD. This is not defensive tidiness -- it is the fix for
  # a false "SURVIVED" on all three mutants, observed 2026-10-04.
  #
  # What happened: an earlier run left a server on $PORT. Each mutant then built a fresh binary,
  # launched it, and that binary logged `bind: 127.0.0.1:$PORT: address already in use` and EXITED.
  # The readiness loop above greps for 'is listening on' in a log file the new process truncates on
  # open -- but the STALE process had already written that line in a previous run and `pkill` between
  # mutants only killed processes matching $BUILD/stash, which the other run's binary also matched,
  # so the port was never actually released before the next launch. The suite then ran against the
  # OLD, UNMUTATED binary and reported all three mutants SURVIVED -- a 0/3 kill rate that looked
  # exactly like three holes in the suite and was in fact one wrong path in the harness.
  #
  # So: check the port is free BEFORE launching, and confirm the log has no bind error AFTER. A
  # readiness check that can be satisfied by another process's log line is not a readiness check.
  if command -v ss >/dev/null 2>&1 && ss -ltn "sport = :$PORT" 2>/dev/null | grep -q ":$PORT"; then
    echo "    HARNESS BUG -- port $PORT is already in use; a stale server would make every mutant"
    echo "    run against the unmutated binary. Free it first (pkill -f 'stash -c')."
    return 1
  fi

  : > "$BUILD/log"
  # setsid, because this script's EXIT trap would otherwise be inherited by the server and run when
  # the server exits -- deleting the build directory out from under the suite.
  ( setsid "$BUILD/stash" -c "$BUILD/c.yml" > "$BUILD/log" 2>&1 < /dev/null & )
  local i
  for i in $(seq 1 60); do
    if grep -q 'address already in use' "$BUILD/log" 2>/dev/null; then
      echo "    HARNESS BUG -- the new server could not bind $PORT:"
      grep 'address already in use' "$BUILD/log" | head -2 | sed 's/^/      /'
      return 1
    fi
    grep -q 'is listening on' "$BUILD/log" 2>/dev/null && return 0
    sleep 1
  done
  echo "    (server did not start; see $BUILD/log)"
  return 1
}

run_suite() {
  ( cd "$SUITE_DIR" && timeout 400 node playwright-e2e.js 2>&1 )
}

build_ui() {
  # Forced for UI mutants (their edit invalidates the bundle); skipped otherwise to save a minute.
  if [ ! -f "$MUT/ui/v2.5/build/index.html" ] || [ "${E2E_FORCE_UI:-0}" = 1 ]; then
    echo "  building the UI..."
    # cd into ui/v2.5, NOT $MUT: pnpm resolves the manifest from the working directory and $MUT is
    # the app ROOT, so building there fails with ERR_PNPM_NO_IMPORTER_MANIFEST_FOUND -- an error
    # that claims there is no package.json while a good one sits one level down.
    ( cd "$MUT/ui/v2.5" && pnpm run build > "$BUILD/uibuild.log" 2>&1 ) || {
      echo "  UI BUILD FAILED -- see $BUILD/uibuild.log"; return 1; }
  fi
  return 0
}

report() {
  if echo "$1" | grep -q 'E2E PASSED'; then
    survived=$((survived+1))
    echo "  SURVIVED  <-- the suite does not catch this"
    return 1
  fi
  killed=$((killed+1))
  echo "$1" | grep -E '^  FAIL' | head -4 | sed 's/^/      /'
  echo "  KILLED"
  return 0
}

# stage_copy -- a full copy of the tree, because the mutants edit files and editing the real
# checkout to test the test is how a checkout gets left broken.
stage_copy() {
  if [ -d "$MUT" ]; then return 0; fi
  echo "  (staging a copy of the tree)"
  rm -rf "$BUILD/.staged"; mkdir -p "$BUILD/.staged"
  # Staged into a sibling and renamed afterwards. Piping tar into $REPO/.e2e-mut/app made tar read
  # a tree it was concurrently writing into, and the copy silently lost ui/ -- surfacing as "no
  # package.json" with nothing pointing at the copy.
  ( cd "$REPO" && tar -cf - --exclude=./node_modules --exclude=./.git \
      --exclude=./.boot-check --exclude=./.e2e --exclude=./.sd --exclude=./.sd2 . ) \
    | tar -C "$BUILD/.staged" -xf -
  [ -f "$BUILD/.staged/ui/v2.5/package.json" ] || {
    echo "  COPY INCOMPLETE -- ui/v2.5/package.json missing from the staged tree"; exit 2; }
  mv "$BUILD/.staged" "$MUT"
  # node_modules is excluded (huge, and not source), so link the real one back in. Without this the
  # UI build fails on missing dependencies -- indistinguishable from an incomplete copy, which is
  # the ambiguity the check above exists to remove.
  ln -sfn "$REPO/ui/v2.5/node_modules" "$MUT/ui/v2.5/node_modules"
}

# run_mutant <id> <label> <needs-ui:yes|no> <files...>
run_mutant() {
  local id="$1" label="$2" needsui="$3"; shift 3
  echo
  echo "=== mutant $id: $label ==="

  if [ "$needsui" = yes ]; then export E2E_FORCE_UI=1; else unset E2E_FORCE_UI; fi

  # Back up every file the mutation touches.
  local baks=() baksrc=()
  for f in "$@"; do
    local src="$f"
    case "$id" in
      m2|m3) src="$MUT/$f" ;;   # UI files: the copy is what gets built
    esac
    cp "$src" "/tmp/e2e-mut-bak.$(echo "$src" | tr / _)" 2>/dev/null
    baks+=("$src"); baksrc+=("$f")
  done
  restore() {
    local f
    for f in "${baks[@]}"; do
      cp "/tmp/e2e-mut-bak.$(echo "$f" | tr / _)" "$f"
    done
  }

  # Mutate the COPY, not $REPO. The app under test is served from $MUT (ui_location points into the
  # copy), so mutating $REPO changed files nothing was running -- and every mutant then reported
  # SURVIVED, including the routing mutant that this suite exists specifically to catch. The result
  # looked like three holes in the suite and was actually one wrong path in the harness.
  #
  # The Go binary is the exception: it is built from $REPO, so the Go mutant (m1) must be applied to
  # $REPO. That asymmetry is why the root is a parameter of run_mutant rather than a constant.
  local target_root="$MUT"
  case "$id" in
    m1) target_root="$REPO" ;;   # compiled into the binary from $REPO
  esac

  if ! python3 "$APPLY" "$target_root" "$id"; then
    # A mutation that was never applied proves NOTHING. Counting it as a survivor would report a
    # suite gap that does not exist and send someone to fix the wrong thing.
    echo "  HARNESS BUG -- the mutation was never applied; this is not a suite result"
    broken=$((broken+1))
    restore
    return 2
  fi

  if ! ( cd "$REPO" && go build -o "$BUILD/stash" ./cmd/stash ) 2>"$BUILD/goerr"; then
    echo "  KILLED BY COMPILE -- the mutant does not build, so it tests nothing about the suite"
    broken=$((broken+1))
    restore
    return 2
  fi

  if ! build_ui; then
    echo "  HARNESS BUG -- the mutated UI does not build"
    broken=$((broken+1))
    restore; E2E_FORCE_UI=1 build_ui || true; unset E2E_FORCE_UI
    return 2
  fi

  rm -rf "$BUILD/data"; mkdir -p "$BUILD/data"
  if ! start_app >/dev/null 2>&1; then
    echo "  KILLED -- the instance does not start"
    killed=$((killed+1))
  else
    # Seed EVERY mutant. Without this, mutant m1 (a panic in sceneResolver.getPrimaryFile) SURVIVED:
    # that resolver only runs for a scene that exists, and with an empty library the panicking line
    # was never executed. An empty fixture hides the code under test -- so each mutant gets real
    # data, which is also what makes the route assertions meaningful.
    if ! python3 "$SUITE_DIR/seed.py" "$E2E_BASE" > "$BUILD/seed.log" 2>&1; then
      echo "  SEED FAILED -- the mutant is untested against an empty instance:"
      sed 's/^/      /' "$BUILD/seed.log" | head -6
      broken=$((broken+1))
      pkill -f "$BUILD/stash" 2>/dev/null; sleep 2
      restore; return 2
    fi
    report "$(run_suite)"
  fi

  pkill -f "$BUILD/stash" 2>/dev/null; sleep 2
  restore
  # Rebuild the clean UI so the next mutant starts from a known-good bundle.
  E2E_FORCE_UI=1 build_ui > /dev/null 2>&1 || true
  unset E2E_FORCE_UI
}

# ------------------------------------------------------------------ baseline
echo "=== 0. baseline: the unmutated tree must PASS ==="
mkdir -p "$BUILD"
stage_copy
build_ui || exit 2
( cd "$REPO" && go build -o "$BUILD/stash" ./cmd/stash ) || exit 2

rm -rf "$BUILD/data"; mkdir -p "$BUILD/data"
start_app || exit 2
if ! python3 "$SUITE_DIR/seed.py" "$E2E_BASE" > "$BUILD/seed.log" 2>&1; then
  echo "SEED FAILED before the baseline -- see $BUILD/seed.log"
  sed 's/^/  /' "$BUILD/seed.log" | head -8
  exit 2
fi
base_out="$(run_suite)"
echo "$base_out" | tail -2
if echo "$base_out" | grep -q 'E2E PASSED'; then
  echo "BASELINE PASS  (good)"
else
  echo "BASELINE FAIL -- fix first; the mutants below prove nothing against a red tree"
  echo "$base_out" | grep -E '^  FAIL' | head -10 | sed 's/^/    /'
  exit 2
fi
pkill -f "$BUILD/stash" 2>/dev/null; sleep 2

# ------------------------------------------------------------------- mutants
run_mutant m1 "a GraphQL resolver panics" no \
  internal/api/resolver_model_scene.go

run_mutant m2 "the /stats component throws during render" yes \
  ui/v2.5/src/components/Stats.tsx

run_mutant m3 "routing dies -- every URL renders the landing page" yes \
  ui/v2.5/src/App.tsx

# ------------------------------------------------------------------- verdict
rm -f "$BUILD/stash"
echo
echo "============================================================"
echo "mutants killed: $killed, survived: $survived, harness errors: $broken"
if [ "$broken" -gt 0 ]; then
  echo "E2E MUTATION CHECK INCONCLUSIVE -- $broken mutant(s) were never actually tested"
  exit 2
fi
if [ "$survived" -eq 0 ]; then
  echo "E2E MUTATION CHECK PASSED"
  exit 0
fi
echo "E2E MUTATION CHECK FAILED"
exit 1