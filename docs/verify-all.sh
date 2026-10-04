#!/usr/bin/env bash
# Full-site verification for the stash fork, from a clean state.
#
# Order matters: cheap gates first so a formatting slip fails in seconds rather than after a
# 5-minute test run. TMPDIR lives outside the repo for a reason explained below; HOME is NOT
# overridden -- an earlier version set HOME=$RUN, which sent the module cache into a temp dir the
# trap then deleted.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
export GOFLAGS=-mod=mod
rc=0
say() { printf '\n=== %s ===\n' "$1"; }
check() { if [ "$1" -ne 0 ]; then printf '  !! FAILED (rc=%s): %s\n' "$1" "$2"; rc=1; fi; }

say "gofmt"
out=$(gofmt -l internal/ pkg/ cmd/ 2>&1)
[ -z "$out" ] || { echo "$out"; rc=1; }
echo "  ${out:-clean}"

say "go vet"
go vet ./... 2>&1 | tail -5
check "${PIPESTATUS[0]}" "go vet"

say "go build"
go build ./... 2>&1 | tail -5
check "${PIPESTATUS[0]}" "go build"

# TMPDIR must live OUTSIDE the repo. internal/mesh/claim_guard_test.go walks the whole tree with
# filepath.WalkDir and its skipDir list covers .git, node_modules, vendor, ui, docs and testdata --
# but NOT a scratch dir. A repo-relative TMPDIR therefore puts Go's build cache inside the walk, and
# the test reads files the compiler is concurrently deleting:
#     open .tmpbuild/go-build.../b733: no such file or directory
# That is a harness artefact, not a product defect. Put the scratch dir on external storage instead;
# /tmp is a small tmpfs on this host, hence the explicit /home/hermes path.
export TMPDIR="${VERIFY_TMPDIR:-/home/hermes/work/.verify-tmp}"
mkdir -p "$TMPDIR"

say "unit suite"
# Same fix as the integration suite below: extract failures FIRST so a chatty package cannot push
# the diagnosis out of a tail window. PIPESTATUS[0] is `go test`, which is what the verdict needs.
go test ./... -count=1 2>&1 | tee /tmp/stash-unit.log \
  | grep -E '^(--- FAIL|    --- FAIL|FAIL|panic:)' | head -20
check "${PIPESTATUS[0]}" "unit"

say "integration suite"
# `tail -15` HID A REAL FAILURE. The suite logs one INFO line per wizard test and several of them
# say `error="database is locked"` -- which is the fake store's DELIBERATE error string from
# stashforge_wizard_test.go, not a real lock. Fifteen lines of that buried the actual failing
# assertion, so the gate reported `FAIL` with no diagnosis and the next step looked like debugging
# a database problem. Failures are now extracted FIRST and the log tail is only context.
go test -tags integration ./... -count=1 2>&1 | tee /tmp/stash-integration.log \
  | grep -E '^(--- FAIL|    --- FAIL|FAIL|panic:)' | head -20
check "${PIPESTATUS[0]}" "integration"

say "boot check"
bash docs/boot-check.sh 2>&1 | tail -8
check "${PIPESTATUS[0]}" "boot-check"

say "alias studio-association mutation gate"
python3 docs/mutate-alias-studio-association.py . 2>&1 | tail -8
check "${PIPESTATUS[0]}" "mutate-alias-studio"

say "ledger structure"
python3 docs/ledger-check.py
check "${PIPESTATUS[0]}" "ledger-check"

# The cross-file check, which ledger-check.py does NOT do: it validates each ROW's shape and stops
# there, so two ledgers holding the same fact can disagree -- a row `closed` in the roster with no
# row in the log, or a stale count in the roster's own summary -- while ledger-check reports
# "all well formed".
#
# That is not hypothetical. On 2026-10-04 the goal read ALL CLAUSES PASS and verify-all.sh printed
# "674 issue rows, all well formed" while docs/check-issue-ledgers.py failed with 14 problems: six
# `closed` rows stranded in the `Planned` work queue, the same six absent from closed-issues.md, and
# a summary claiming 56 planned / 52 closed against a table holding 0 / 58. The checker that saw it
# was simply not in the gate.
say "ledger cross-check"
python3 docs/check-issue-ledgers.py 2>&1 | tail -6
check "${PIPESTATUS[0]}" "check-issue-ledgers"

# The SHAPE of the closed-issue log, which check-issue-ledgers.py does not check: it extracts issue
# NUMBERS with a regex and never looks at a row's columns, so a truncated row, one split by a stray
# pipe, or one never closed still contributes its number correctly and the cross-check passes.
# docs/ledger-check.py checks the ROSTER's shape, not the log's.
#
# Ten such rows accumulated by 2026-10-04, in a file whose header insists every closure names the
# test that proves it. Repaired by docs/repair-closed-log.py; this gate keeps them repaired.
say "closed-log shape"
python3 docs/closed-log-check.py
check "${PIPESTATUS[0]}" "closed-log-check"

say "cited paths"
python3 docs/check_cited_paths.py 2>&1 | tail -3
check "${PIPESTATUS[0]}" "check_cited_paths"

# docs/WHATS-LEFT.md is a prose SUMMARY of the ledgers, and it sat wrong for two days (C2 FAIL / C8
# FAIL, both resolved on 2026-10-03) while every gate stayed green -- because goal-check.py computes
# its clauses from the ledgers and nothing asserted that the summary agreed with them. A document
# that restates a conclusion is a second copy of a fact with no gate on it.
#
# It shells out to goal-check.py rather than re-implementing the clauses: a second implementation of
# the same rules is a second thing to keep honest, which is how the cross-ledger drift started.
# Costs ~40s because goal-check runs the suite. Verified to FAIL with 4 signals when the original
# drift is re-injected.
say "whats-left summary"
python3 docs/check-whats-left.py 2>&1 | tail -8
check "${PIPESTATUS[0]}" "check-whats-left"

say "RESULT"
[ "$rc" -eq 0 ] && echo "GO/BOOT VERIFICATION PASSED" || echo "GO/BOOT VERIFICATION FAILED"
# Do NOT delete $TMPDIR on the way out. It is a shared, caller-overridable path
# (VERIFY_TMPDIR), and other tooling on this host points TMPDIR at it too -- docs/e2e/mutation-check.sh
# inherits it and then fails at `go: creating work dir: stat /home/hermes/work/.verify-tmp: no such
# file or directory`. Removing a directory another process is about to use is a worse failure than
# leaving a few hundred MB behind, so the scratch dir is left in place and cleaned by whoever owns
# it.
exit "$rc"