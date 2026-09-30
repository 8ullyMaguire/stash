#!/usr/bin/env bash
# End-to-end tests for the privilege-dropping entrypoint (stash#684).
#
# These run the REAL image, because the failure modes that matter here are all
# container-level: whether su-exec can set the id, whether /root/.stash is still
# writable, whether set -e turns a lookup into a dead container. None of that is
# visible from reading the script.
#
# The one bug this file already caught: the script looked up the uid with
# `id -u nobody` and then passed the RESULT to `id -g`, which asks for a user
# named "65534" and fails. Under set -e that took the container down, so
# USER=nobody produced no output and exit 1 instead of running as nobody.
#
# Usage:  docker build -t stash684-test <dist ctx> && ./docker/test-entrypoint.sh
set -uo pipefail

IMAGE="${1:-stash684-test}"
PASS=0
FAIL=0

# run <expected-uid> <expected-gid> <description> [docker run args...]
run() {
    local want_uid="$1" want_gid="$2" desc="$3"
    shift 3
    local out
    out="$(docker run --rm "$@" "$IMAGE" sh -c 'echo "U=$(id -u) G=$(id -g)"' 2>/dev/null)"
    local got
    got="$(echo "$out" | grep -o 'U=[0-9]* G=[0-9]*' | head -1)"

    if [ "$got" = "U=$want_uid G=$want_gid" ]; then
        echo "  ok    $desc ($got)"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc: want U=$want_uid G=$want_gid, got '${got:-<no output>}'"
        FAIL=$((FAIL + 1))
    fi
}

# assert_writable <description> [docker run args...]
assert_writable() {
    local desc="$1"
    shift
    local out
    out="$(docker run --rm "$@" "$IMAGE" sh -c \
        'test -w /root/.stash && echo WRITABLE || echo READONLY' 2>/dev/null)"
    if [ "$out" = "WRITABLE" ]; then
        echo "  ok    $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc: /root/.stash is ${out:-<no output>}"
        FAIL=$((FAIL + 1))
    fi
}

# run_as <expected-uid> <expected-gid> <description> -- the `user: "N:M"` path.
#
# Docker starts the container as that uid, so the entrypoint is ALREADY
# non-root and su-exec has nothing to do. Before the fix it tried to setuid
# anyway and died: a non-root process cannot setresuid, not even to itself,
# because clear-groups needs privilege ("setresuid failed: Operation not
# permitted"), and the script runs under `set -e`.
run_as() {
    local want_uid="$1" want_gid="$2" desc="$3"
    shift 3
    local out
    out="$(docker run --rm --user "$want_uid:$want_gid" "$@" "$IMAGE" \
        sh -c 'echo "U=$(id -u) G=$(id -g)"' 2>/dev/null)"
    local got
    got="$(echo "$out" | grep -o 'U=[0-9]* G=[0-9]*' | head -1)"
    if [ "$got" = "U=$want_uid G=$want_gid" ]; then
        echo "  ok    $desc ($got)"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc: want U=$want_uid G=$want_gid, got '${got:-<no output>}'"
        FAIL=$((FAIL + 1))
    fi
}

echo "entrypoint tests against $IMAGE"

# The default. The whole point of the issue.
run 1000 1000 "runs unprivileged by default"
assert_writable "the documented /root/.stash mount stays writable"

# Configurable uid and gid -- the second half of the report.
run 1500 1600 "PUID/PGID select the identity" -e PUID=1500 -e PGID=1600
run 1501 1601 "USER/GID work as aliases" -e USER=1501 -e GID=1601

# A username rather than a number. This is the case that caught the id(1)
# bug: it resolves the name to 65534 and then must NOT pass 65534 back into id.
run 65534 65534 "USER accepts an account name" -e USER=nobody

# An id that has no passwd entry at all. su-exec sets it, id(1) just cannot
# name it, so the process still has to come up.
run 4242 4242 "an id with no passwd entry still runs" -e PUID=4242 -e PGID=4242

# Generated files must carry the requested ownership, which is what makes a
# bind mount out of the container usable from the host.
own="$(docker run --rm -e PUID=1500 -e PGID=1600 "$IMAGE" \
    sh -c 'stat -c "%u:%g" /root/.stash' 2>/dev/null)"
if [ "$own" = "1500:1600" ]; then
    echo "  ok    the data dir is owned by the requested uid/gid ($own)"
    PASS=$((PASS + 1))
else
    echo "  FAIL  the data dir is owned by '${own:-<no output>}', want 1500:1600"
    FAIL=$((FAIL + 1))
fi

# umask is what decides group-writability on a shared mount.
um="$(docker run --rm -e UMASK=077 "$IMAGE" sh -c 'umask' 2>/dev/null)"
if [ "$um" = "0077" ]; then
    echo "  ok    UMASK is honoured ($um)"
    PASS=$((PASS + 1))
else
    echo "  FAIL  UMASK=077 gave '$um'"
    FAIL=$((FAIL + 1))
fi

# The escape hatch has to actually keep the old behaviour, or it is not one.
root="$(docker run --rm -e RUN_AS_ROOT=1 "$IMAGE" sh -c 'echo "U=$(id -u)"' 2>/dev/null | grep -o 'U=[0-9]*')"
if [ "$root" = "U=0" ]; then
    echo "  ok    RUN_AS_ROOT=1 keeps root"
    PASS=$((PASS + 1))
else
    echo "  FAIL  RUN_AS_ROOT=1 gave '${root:-<no output>}', want U=0"
    FAIL=$((FAIL + 1))
fi

# A config file the entrypoint did not create must still be readable, i.e. the
# existing-install case: an old root-owned config.yml mounted in place.
owned="$(docker run --rm -v "$(mktemp -d):/root/.stash" "$IMAGE" \
    sh -c 'test -w /root/.stash && echo WRITABLE || echo READONLY' 2>/dev/null)"
if [ "$owned" = "WRITABLE" ]; then
    echo "  ok    a fresh bind mount at /root/.stash is usable"
    PASS=$((PASS + 1))
else
    echo "  FAIL  a fresh bind mount is ${owned:-<no output>}"
    FAIL=$((FAIL + 1))
fi

# --- the `user:` path (PR #7159) --------------------------------------
# The compose file's alternative to PUID/PGID. Docker picks the identity, so the
# entrypoint must notice it is already there instead of dropping again.

run_as 1000 1000 "user: 1000:1000 comes up already unprivileged"

# The case that motivated the fix: an id the image never had a passwd entry for.
run_as 4242 4242 "user: with an id that has no passwd entry still runs"

# A DIFFERENT id from the image default, so the entrypoint has to resolve rather
# than merely echo back what it was handed.
run_as 1500 1600 "user: 1500:1600 is honoured"

# uids match, gids differ -- a split identity is easy to get wrong in a string
# comparison, which is exactly how the fix compares them.
run_as 1000 1600 "user: a differing gid is honoured"

# The combination that the README documents: `user:` together with HOME pointing
# at the mounted config, which is what GetHomeDirectory now reads.
run_as 1000 1000 "user: plus HOME=/config comes up" -e HOME=/config

# And it must not be broken by RUN_AS_ROOT being set from an old compose file.
run_as 1000 1000 "user: wins over a leftover RUN_AS_ROOT=1" -e RUN_AS_ROOT=1

echo
echo "  $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
