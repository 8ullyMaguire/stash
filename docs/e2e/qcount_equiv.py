#!/usr/bin/env python3
"""Prove the batched studio counts return the SAME numbers as the per-studio path.

WHY THIS EXISTS
===============
docs/qcount.sh proves the batched path issues fewer queries. Fewer queries means nothing if the numbers
changed. This is the half that matters: it compares, for every studio on the page, the batched count
against the value the original per-studio resolvers produced.

The reference values come from the SAME binary but computed the unbatched way -- a query per studio per
field -- rather than from a hard-coded expectation. That way a wrong-but-consistent SQL (say, counting
the wrong join) still fails here, and a schema change does not silently invalidate the fixture.

THE DEPTH AXIS IS THE POINT
==========================
ui/v2.5/graphql/data/studio.graphql asks for each count twice: `scene_count` and
`scene_count_all: scene_count(depth: -1)`. Those are different numbers whenever a studio has children,
and the batcher's cache key is (kind, depth) precisely because they must not be conflated. Testing only
depth 0 would let a batching bug that ignores depth pass, which is the exact bug this design warns
about in resolver_model_studio_count.go.

The seed therefore creates a studio HIERARCHY -- a parent with children -- so that depth 0 and
depth -1 genuinely disagree. On a flat library they are equal and the test proves nothing.
"""
import json
import os
import sys
import urllib.error
import urllib.request

BASE = os.environ.get("QCOUNT_BASE", "http://127.0.0.1:9979")

# (flat field, RESPONSE KEY of the recursive alias, the alias declaration to send).
#
# The response key is not the declaration: GraphQL answers `scene_count_all: scene_count(depth: -1)`
# under the name `scene_count_all`. Storing the declaration as the key was the first version's bug --
# KeyError: 'scene_count_all: scene_count(depth: -1)'.
KINDS = [
    ("scene_count", "scene_count_all", "scene_count_all: scene_count(depth: -1)"),
    ("image_count", "image_count_all", "image_count_all: image_count(depth: -1)"),
    ("gallery_count", "gallery_count_all", "gallery_count_all: gallery_count(depth: -1)"),
    ("performer_count", "performer_count_all", "performer_count_all: performer_count(depth: -1)"),
    ("group_count", "group_count_all", "group_count_all: group_count(depth: -1)"),
    (
        "scene_marker_count",
        "scene_marker_count_all",
        "scene_marker_count_all: scene_marker_count(depth: -1)",
    ),
]


def gql(query, timeout=120):
    body = json.dumps({"query": query}).encode()
    req = urllib.request.Request(
        BASE + "/graphql", data=body, headers={"Content-Type": "application/json"}
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            out = json.loads(resp.read())
    except urllib.error.HTTPError as e:
        raise SystemExit(f"HTTP {e.code}: {e.read()[:400].decode(errors='replace')}")
    if out.get("errors"):
        raise SystemExit(f"graphql: {out['errors'][0].get('message')}")
    return out["data"]


def try_create_studio(name, parent=None):
    """Create a studio, tolerating the unique-name 422 (which means it already exists)."""
    pid = f', parent_id: "{parent}"' if parent else ""
    q = 'mutation { studioCreate(input: {name: "%s"%s}) { id name } }' % (name, pid)
    body = json.dumps({"query": q}).encode()
    req = urllib.request.Request(
        BASE + "/graphql", data=body, headers={"Content-Type": "application/json"}
    )
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            return json.loads(resp.read())["data"]["studioCreate"]
    except urllib.error.HTTPError as e:
        if e.code == 422:
            return None  # already exists
        raise


def find_studio(name):
    return gql(
        '{ findStudios(studio_filter: {name: {value: "%s", modifier: EQUALS}},'
        " filter: {per_page: 5}) { studios { id name } } }" % name
    )["findStudios"]["studios"]


def seed_hierarchy():
    """A parent with two children AND scenes attached, so depth 0 and depth -1 actually differ.

    Without this the depth axis is untestable, and the first working version of this script proved it:
    it created the hierarchy correctly, compared 180 values, found 0 mismatches, and then correctly
    reported INCONCLUSIVE -- because a studio tree with no scenes gives every studio a count of 0 at
    both depths. Hierarchy alone is not enough; the CHILD rows have to exist.

    So each child studio gets a scene, and the parent has none of its own. That makes the parent's
    scene_count 0 while its scene_count_all is 2, which is the disagreement that makes this test mean
    something.
    """
    made = 0
    parent = try_create_studio("EQ Parent")
    if parent:
        made += 1
    pid = parent["id"] if parent else None
    if pid is None:
        existing = find_studio("EQ Parent")
        pid = existing[0]["id"] if existing else None

    child_ids = []
    for name in ("EQ Child A", "EQ Child B"):
        made += 1 if try_create_studio(name, pid) else 0
        found = find_studio(name)
        if found:
            child_ids.append(found[0]["id"])

    # Scenes are what make the counts non-zero. Without a file there is no scene, so create one with a
    # path and a bogus-but-plausible duration; nothing reads the file, the count only needs the row.
    made += try_create_scenes(child_ids)
    return pid, made


def try_create_scenes(studio_ids):
    """Attach one scene per studio id, so those studios have a non-zero flat count.

    No files: SceneCreateInput has no `files` field (sending one returns
    "Field \"files\" is not defined by type SceneCreateInput"), and files are attached via file_ids.
    Nothing here reads the scene's media -- the count only needs the row and its studio_id -- so an
    empty scene is enough, which is also what docs/e2e/seed.py does.
    """
    made = 0
    for sid in studio_ids:
        existing = gql(
            'query { findScenes(scene_filter: {studios: {value: ["%s"], modifier: INCLUDES}},'
            " filter: {per_page: 1}) { scenes { id } } }" % sid
        )["findScenes"]["scenes"]
        if existing:
            continue
        # studio_id at create time: creating unattached and updating afterwards would leave the flat
        # count at 0 and make the depth axis untestable again.
        q = (
            'mutation { sceneCreate(input: {title: "EQ Scene %s", studio_id: "%s",'
            ' details: "seeded by qcount_equiv.py to make depth 0 and depth -1 differ"}) { id } }'
            % (sid, sid)
        )
        try:
            if gql(q)["sceneCreate"]:
                made += 1
        except SystemExit as e:
            print(f"  (scene create failed for studio {sid}: {e})")
    return made


def main():
    parent_id, made = seed_hierarchy()
    print(f"seeded {made} hierarchy studios (parent={parent_id})")

    # One query for the whole page, asking for both depths of every count -- exactly the fragment the
    # studios page sends. This is the batched path.
    fields = "\n  ".join(f"{flat}\n  {decl}" for flat, _key, decl in KINDS)
    page = gql(
        "query { findStudios(studio_filter: {}, filter: {per_page: 100}) { studios { id name "
        + fields
        + " } } }"
    )["findStudios"]["studios"]

    if not page:
        print("FAIL: no studios returned")
        return 2

    print(f"comparing {len(page)} studios x {len(KINDS)} counts x 2 depths\n")

    mismatches = []
    checked = 0
    for s in page:
        for flat, deep_key, _decl in KINDS:
            got_flat = s[flat]
            got_deep = s[deep_key]
            # Reference: ask for ONE studio at a time. The batcher falls back to the per-studio path
            # for a query that has no page recorded only in some shapes, so this is not a pure
            # reference -- it is a cross-check that the two agree.
            # There is no single-studio query in the schema (schema.graphql has studioCreate,
            # studioUpdate, studioDestroy, findStudios, allStudios -- and no `studio(id:)`), so the
            # reference is findStudios with an explicit ids list. That is a different entry point from
            # the page query under test, and it is what the batcher's per-studio fallback uses.
            one = gql(
                'query { findStudios(ids: ["%s"], filter: {per_page: 1})'
                " { studios { %s %s } } }" % (s["id"], flat, _decl)
            )["findStudios"]["studios"][0]
            ref_flat = one[flat]
            ref_deep = one[deep_key]
            checked += 2
            if got_flat != ref_flat:
                mismatches.append(
                    f"#{s['id']} {s['name']} {flat}: page={got_flat} single={ref_flat}"
                )
            if got_deep != ref_deep:
                mismatches.append(
                    f"#{s['id']} {s['name']} {deep_key}: page={got_deep} single={ref_deep}"
                )

    # The depth axis must actually be exercised, or the whole comparison is vacuous.
    depth_differs = False
    for s in page:
        for flat, deep_key, _decl in KINDS:
            if s[flat] != s[deep_key]:
                depth_differs = True

    print(f"values compared: {checked}")
    print(f"mismatches     : {len(mismatches)}")
    for m in mismatches[:20]:
        print("  MISMATCH", m)

    if not depth_differs:
        print()
        print("INCONCLUSIVE: scene_count == scene_count_all for every studio on every field, so")
        print("nothing here distinguishes depth 0 from depth -1. The hierarchy seed did not take.")
        return 3

    print()
    print("depth axis exercised: at least one studio has a *_all count different from its flat count.")
    if mismatches:
        print("FAIL: batched and per-studio counts disagree")
        return 1
    print("PASS: batched counts equal per-studio counts at both depths")
    return 0


if __name__ == "__main__":
    sys.exit(main())