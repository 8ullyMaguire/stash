#!/bin/sh
# Drop privileges and exec stash. Solves stash#684.
#
# WHY AN ENTRYPOINT RATHER THAN A "USER" LINE IN THE DOCKERFILE.
#
# A plain `USER stash` would satisfy the letter of the request and break every
# existing install. The documented mount point is /root/.stash (see
# docker/production/docker-compose.yml and both READMEs), and that directory is
# created by the bind mount at container start, not baked into the image. If
# the process ran as a non-root user, it would not be able to write to a
# /root-owned bind mount, and every current user would come back to an empty
# database after upgrading. The image has to keep starting as root, chown what
# it needs, and then hand the process over to an unprivileged uid.
#
# It is worth being explicit that this is a real mitigation and not a complete
# fix: the process is root for the first few milliseconds. Anything that can
# influence this entrypoint before the exec -- a mounted script, a hostile env
# var -- runs as root. Dropping earlier than that requires the volume to be
# chowned by the runtime rather than by us, which is what the PUID/PGID env
# vars below are for: the linuxserver.io convention of setting the uid on the
# mounted volume means the host already arranged the permissions, and in that
# common case the container can be started with a real USER and never needs
# root at all.
#
# WHAT THIS DOES FIX, concretely:
#   - the stash process and everything it spawns (ffmpeg, vips) no longer run
#     as root, so a bug in media parsing is not a container-escape-shaped bug;
#   - generated files carry the requested uid/gid, so a bind mount out of the
#     container is owned by the host user instead of by root on the host --
#     which is the "tighter security" and "control both UID and GID" part of
#     the report;
#   - the umask is configurable, so 0644-by-default is not forced.
#
# ENVIRONMENT:
#   PUID / PGID  numeric uid:gid to run as, and to own the data dirs.
#               Default: 1000:1000. Matches the first non-system user that
#               essentially every base image already has.
#   USER / GID   the linuxserver.io spellings. Accepted as aliases, and taking
#               precedence when they name a user rather than a number, so
#               people who copied that convention keep working.
#   UMASK        default 002. The 022 that most umask defaults produce means
#               "not writable by the group", which breaks a shared bind mount
#               between a container user and a host user in the same group.
#   STASH_USER   the account name to run as when no numeric id is given.
#               Default: stash.
#   RUN_AS_ROOT  set to 1 to skip the privilege drop entirely. Escape hatch for
#               people who bind-mount a root-owned path and are not using PUID
#               at all. Off by default: the request was to stop running as
#               root, so it has to be an explicit choice to keep doing it.
set -e

STASH_USER="${STASH_USER:-stash}"
umask "${UMASK:-002}"

# Resolve the requested identity. Numeric ids win over names, because that is
# what a bind mount from the host is actually going to match.
uid_arg="${PUID:-}"
gid_arg="${PGID:-}"
# USER/GID are the linuxserver.io spellings, accepted as aliases. A literal
# number passed via USER is normal in copied compose files, so it is not
# treated as an account name.
if [ -z "$uid_arg" ] && [ -n "${USER:-}" ]; then
    uid_arg="$USER"
fi
if [ -z "$gid_arg" ] && [ -n "${GID:-}" ]; then
    gid_arg="$GID"
fi

# A USER that is a name rather than a number is looked up in the image.
if [ -n "$uid_arg" ] && [ -z "${PUID:-}" ]; then
    if ! echo "$uid_arg" | grep -qE '^[0-9]+$'; then
        if id "$uid_arg" >/dev/null 2>&1; then
            STASH_USER="$uid_arg"
            # Resolve the name ONCE and read both ids out of that entry.
            # Chaining id(1) calls here looks fine and is not: the second one
            # receives the uid the first one just produced, so it asks for a
            # user literally named "65534" and fails. Under set -e that killed
            # the container, so USER=nobody gave no output and exit 1 instead
            # of running as nobody.
            entry="$(getent passwd "$STASH_USER" 2>/dev/null || true)"
            uid_arg="$(echo "$entry" | cut -d: -f3)"
            gid_arg="${gid_arg:-$(echo "$entry" | cut -d: -f4)}"
        else
            echo "stash: USER=$uid_arg is not a user in this image; using \$PUID/\$PGID or the default" >&2
            uid_arg=""
        fi
    fi
fi

# The default account. Created in the Dockerfile; created again here so the
# script also works in an image that predates it.
if ! getent group stash >/dev/null 2>&1; then
    addgroup -g "${gid_arg:-1000}" -S stash 2>/dev/null || true
fi
if ! getent passwd stash >/dev/null 2>&1; then
    adduser -D -H -u "${uid_arg:-1000}" -G stash stash 2>/dev/null || true
fi

# No explicit request: run as the default account at whatever id it has, which
# is what the Dockerfile or this block gave it. Resolved by name, never by
# feeding a uid back into id(1) -- see the note above.
if [ -z "$uid_arg" ]; then
    if [ -n "$gid_arg" ] && getent passwd "$STASH_USER" >/dev/null 2>&1; then
        # Only a gid was given. Match it, keeping the account name.
        usermod -g "$gid_arg" "$STASH_USER" 2>/dev/null || true
    fi
    entry="$(getent passwd "$STASH_USER" 2>/dev/null || true)"
    if [ -n "$entry" ]; then
        uid_arg="$(echo "$entry" | cut -d: -f3)"
        gid_arg="${gid_arg:-$(echo "$entry" | cut -d: -f4)}"
    else
        uid_arg="${uid_arg:-1000}"
        gid_arg="${gid_arg:-1000}"
    fi
fi

echo "stash: running as uid=$uid_arg gid=$gid_arg (umask $(umask))" >&2

if [ "${RUN_AS_ROOT:-0}" = "1" ]; then
    echo "stash: RUN_AS_ROOT=1, not dropping privileges" >&2
    exec "$@"
fi

# Make the data directories writable by the unprivileged user. These are the
# ones Stash actually writes to; the bind mount over /root/.stash means the
# contents are the host's, but the mount point itself and the parent need to be
# traversable.
for dir in /root /root/.stash; do
    if [ -d "$dir" ]; then
        chown "$uid_arg:$gid_arg" "$dir" 2>/dev/null || true
    else
        mkdir -p "$dir" && chown "$uid_arg:$gid_arg" "$dir" 2>/dev/null || true
    fi
done
chmod u+rwx /root 2>/dev/null || true

# Hand over. su-exec is setuid-root and execs without spawning a shell in
# between, so the process stash sees is the stash binary itself -- no extra
# pid, no shell holding the session, and signals reach it directly.
exec su-exec "$uid_arg:$gid_arg" "$@"
