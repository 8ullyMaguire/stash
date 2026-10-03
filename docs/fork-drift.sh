#!/usr/bin/env bash
# Report how far the fork has drifted from upstream, without changing anything.
#
# The fork is a soft fork: upstream stash plus issue fixes, on top of which sits a large local
# programme (StashForge governance, the mesh, ed2k transfer). So "behind" and "ahead" are both
# large and both expected, and neither is a health signal on its own. What matters is the
# merge-base age: how long it has been since upstream work could flow in cleanly.
#
# Read-only. Run it before deciding whether to rebase or merge.

set -uo pipefail

REPO="${1:-/home/hermes/work/lane-1/stash}"
cd "$REPO" || exit 1

if ! git remote get-url upstream >/dev/null 2>&1; then
  echo "no 'upstream' remote; add one:"
  echo "  git remote add upstream https://github.com/stashapp/stash.git"
  exit 2
fi

echo "=== fetching (read-only) ==="
git fetch upstream --quiet 2>&1 | tail -2
git fetch origin --quiet 2>&1 | tail -2

UP=upstream/develop
[ -z "$(git rev-parse --verify "$UP" 2>/dev/null)" ] && UP=upstream/main

MB=$(git merge-base HEAD "$UP" 2>/dev/null)
if [ -z "$MB" ]; then
  echo "no merge base with $UP -- unrelated histories"
  exit 2
fi

BEHIND=$(git rev-list --count "$MB".."$UP")
AHEAD=$(git rev-list --count "$MB"..HEAD)
UNPUSHED=$(git rev-list --count "origin/main..HEAD" 2>/dev/null || echo 0)

echo
echo "tracking        $UP"
echo "merge base      $(git log -1 --format='%h %ad %s' --date=short "$MB" | cut -c1-72)"
echo "behind upstream $BEHIND commits"
echo "ahead of base   $AHEAD commits"
echo "unpushed       $UNPUSHED commits on $(git branch --show-current)"
echo
echo "=== upstream work since the merge base (newest 10) ==="
git log --oneline --no-decorate "$MB".."$UP" | head -10 | cat
echo
echo "=== conflicts a merge would hit (files touched both sides) ==="
comm -12 \
  <(git diff --name-only "$MB"..HEAD | sort -u) \
  <(git diff --name-only "$MB".."$UP" | sort -u) \
  | head -40
echo
echo "Merge, do not rebase: this history has local tags the ledger cites by SHA, and"
echo "a rebase would rewrite every one of them."