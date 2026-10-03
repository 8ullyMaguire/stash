#!/usr/bin/env bash
# Mutation sweep for the #3530 range form's validation rules.
#
# Each mutant disables ONE rule in SceneRangeForm.tsx's `validate`. All three are killed; the
# point of recording them is that the rules are pinned to specific cases, so a future edit that
# weakens one is caught rather than shipped.
#
#   M1  end <= start  ->  end < start
#       Accepts a ZERO-LENGTH window. Two cases die: `end == start`, and 0..0 on a zero-length
#       file. Both exist because every derived artefact (sprite grid, preview, HLS segments)
#       would render empty, and migration 122's CHECK is `end_time > start_time` -- STRICT.
#       So this is not a style disagreement with the database, it is the database's rule.
#
#   M2  drop `end > fileDuration`
#       The UI then happily offers a range the API will refuse, turning a 400 into a round trip
#       that fails after submission. Two cases die, including the half-second overrun that a
#       rounded display value would otherwise hide.
#
#   M3  drop `start < 0`
#       A negative start has no meaning; the CHECK refuses it.
#
# Exits 1 on any survivor or a red baseline.
#
#   bash ui/v2.5/docs/mutate-3530-range-form.sh

set -uo pipefail

UI="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$UI/src/components/Scenes/SceneDetails/SceneRangeForm.tsx"
PROBE="$UI/scripts/probe-3530-range.mjs"
BAK="$(mktemp)"

cp "$SRC" "$BAK"
trap 'cp "$BAK" "$SRC"; rm -f "$BAK"' EXIT

restore() { cp "$BAK" "$SRC"; }

# $1 label, $2 exact anchor, $3 replacement, $4 expected witness in the probe output
mutate() {
  local label="$1" anchor="$2" repl="$3" witness="$4"
  printf -- '--- %s\n' "$label"

  local n
  n=$(grep -cF "$anchor" "$SRC")
  if [ "$n" != "1" ]; then
    echo "  SKIP: anchor occurs $n times"
    return 2
  fi

  python3 - "$SRC" "$anchor" "$repl" <<'PY'
import pathlib, sys
path, anchor, repl = sys.argv[1], sys.argv[2], sys.argv[3]
p = pathlib.Path(path)
s = p.read_text()
assert s.count(anchor) == 1, f"anchor count {s.count(anchor)}"
p.write_text(s.replace(anchor, repl, 1))
PY

  local out
  out=$(node "$PROBE" 2>&1)
  if [ $? -eq 0 ]; then
    echo "  SURVIVED: the probe passed with the rule disabled"
    restore
    return 1
  fi
  if ! grep -qF "$witness" <<<"$out"; then
    echo "  COVERED: the probe failed, but not by the expected case"
    grep -E "FAIL|expected|got" <<<"$out" | head -6
    restore
    return 1
  fi
  echo "  KILLED: $witness went red"
  restore
  return 0
}

echo "=== baseline ==="
if node "$PROBE" >/dev/null 2>&1; then
  echo "  green"
else
  echo "  HARNESS MALFORMED: baseline is red"
  node "$PROBE" 2>&1 | head -20
  exit 2
fi

fail=0
mutate \
  "M1: end <= start -> end < start (accepts a zero-length window)" \
  'if (start !== null && end !== null && end <= start) {' \
  'if (start !== null && end !== null && end < start) {' \
  'FAIL end == start' || fail=1

mutate \
  "M2: drop the end > fileDuration rule" \
  'if (end !== null && end > fileDuration) {' \
  'if (false && end !== null && end > fileDuration) {' \
  'FAIL end past by 0.5s' || fail=1

mutate \
  "M3: drop the negative-start rule" \
  'if (start !== null && start < 0) {' \
  'if (false && start !== null && start < 0) {' \
  'FAIL start negative' || fail=1

echo
if [ "$fail" != "0" ]; then
  echo "SWEEP FAILED"
  exit 1
fi
echo "killed 3/3"