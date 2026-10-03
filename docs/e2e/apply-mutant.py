#!/usr/bin/env python3
"""Apply the e2e mutants.

Kept out of mutation-check.sh because a python heredoc nested inside a bash heredoc needs a
delimiter that cannot appear in either, and getting that wrong corrupts the shell script silently --
which is how `export default function` (an anchor that does not exist in Stats.tsx) shipped in the
first version and was reported as a surviving mutant.

Every mutation REFUSES to run if its anchor is missing. A no-op mutant reported as "the suite does
not catch this" is the most expensive kind of test bug: it looks exactly like a hole in the suite,
and it sends you to strengthen the suite when the real fix is here.
"""
import pathlib
import shutil
import sys
import tempfile

REPO = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else ".").resolve()
UI = REPO / "ui" / "v2.5" / "src"


def fail(msg):
    print(f"ANCHOR MISSING: {msg}")
    sys.exit(1)


def edit(path, old, new, *, count=1):
    p = pathlib.Path(path)
    if not p.exists():
        fail(f"{path} does not exist")
    s = p.read_text()
    if s.count(old) < 1:
        fail(f"{p.name} no longer contains the anchor: {old[:70]!r}")
    p.write_text(s.replace(old, new, count))
    print(f"  mutated {p.relative_to(REPO)}")


def backup(path):
    return Path(tempfile.mkstemp(suffix=".mutbak")[1])


# ---------------------------------------------------------------- M1
def m1_resolver_panic():
    """sceneResolver.getPrimaryFile panics.

    A Go resolver panic must surface as a GraphQL error the browser logs, even on an EMPTY library
    -- but getPrimaryFile only runs for a scene row that exists, so with no data it is never
    reached. The first version of the suite therefore SURVIVED this mutant, which is the whole
    reason test [6] exists.
    """
    f = REPO / "internal" / "api" / "resolver_model_scene.go"
    old = "func (r *sceneResolver) getPrimaryFile(ctx context.Context, obj *models.Scene) (*models.VideoFile, error) {"
    edit(f, old, old + '\n\tpanic("MUTANT M1: deliberate resolver panic")')


# ---------------------------------------------------------------- M2
def m2_stats_throws():
    """Stats.tsx throws during render.

    Status stays 200, the nav still renders, the page still has text. Only `pageerror` sees it.
    """
    f = UI / "components" / "Stats.tsx"
    if not f.exists():
        fail(f"{f} does not exist")
    s = f.read_text()
    # Real form: `export const Stats: React.FC = () => {`
    i = s.find("export const Stats")
    if i < 0:
        fail("Stats.tsx has no `export const Stats` -- it may be a default export now")
    j = s.find("{", i)
    if j < 0:
        fail("Stats.tsx has no function body")
    f.write_text(s[: j + 1] + '\n  throw new Error("MUTANT M2: deliberate render throw");\n' + s[j + 1 :])
    print("  mutated ui/v2.5/src/components/Stats.tsx")


# ---------------------------------------------------------------- M3
def m3_routing_dead():
    """Every route renders the landing page.

    The exact failure the first version of the e2e suite had: HTTP 200, the navbar present, body
    text present, eight of nine routes indistinguishable. Any assertion made against `body` text
    passes.
    """
    f = UI / "App.tsx"
    if not f.exists():
        fail(f"{f} does not exist")
    s = f.read_text()

    # Make every path render the front page, by giving EVERY route the index path. React Router
    # matches the first route whose path matches, and all of these now match everything, so the
    # first one -- the front page -- wins for every URL.
    #
    # Rewriting each `path` rather than deleting routes, because deleting them makes the build fail
    # on unused imports, and a mutant killed by a compile error tests nothing about the suite.
    import re
    routes = re.findall(r'<Route\s[^>]*path="([^"]*)"[^>]*/>', s)
    if len(routes) < 3:
        fail(f"expected several self-closing <Route path=.../> entries, found {len(routes)}")
    for r in set(routes) - {"/"}:
        s = s.replace(f'path="{r}"', 'path="/"', 1)
    f.write_text(s)
    print(f"  mutated ui/v2.5/src/App.tsx ({len(routes)} routes collapsed onto the index)")


MUTANTS = {
    "m1": m1_resolver_panic,
    "m2": m2_stats_throws,
    "m3": m3_routing_dead,
}

if __name__ == "__main__":
    which = sys.argv[2] if len(sys.argv) > 2 else ""
    fn = MUTANTS.get(which)
    if not fn:
        print("usage: apply-mutant.py <repo-root> <" + "|".join(MUTANTS) + ">")
        sys.exit(2)
    fn()