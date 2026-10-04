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
go test ./... -count=1 2>&1 | grep -Ev '^ok|no test files' | tail -15
check "${PIPESTATUS[0]}" "unit"

say "integration suite"
go test -tags integration ./... -count=1 2>&1 | grep -Ev '^ok|no test files' | tail -15
check "${PIPESTATUS[0]}" "integration"

say "boot check"
bash docs/boot-check.sh 2>&1 | tail -8
check "${PIPESTATUS[0]}" "boot-check"

say "RESULT"
[ "$rc" -eq 0 ] && echo "GO/BOOT VERIFICATION PASSED" || echo "GO/BOOT VERIFICATION FAILED"
# Do NOT delete $TMPDIR on the way out. It is a shared, caller-overridable path
# (VERIFY_TMPDIR), and other tooling on this host points TMPDIR at it too -- docs/e2e/mutation-check.sh
# inherits it and then fails at `go: creating work dir: stat /home/hermes/work/.verify-tmp: no such
# file or directory`. Removing a directory another process is about to use is a worse failure than
# leaving a few hundred MB behind, so the scratch dir is left in place and cleaned by whoever owns
# it.
exit "$rc"