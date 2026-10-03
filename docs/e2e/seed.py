#!/usr/bin/env python3
"""Seed the e2e instance with one studio, performer and scene.

WHY THIS EXISTS
===============

Mutant M1 -- a panic injected into sceneResolver.getPrimaryFile -- SURVIVED the first working
version of the e2e suite. The cause was not a harness bug: getPrimaryFile only executes for a scene
row that exists, and the test instance had an empty library, so the panicking line was never reached.

That is the empty-fixture blind spot, and it is the reason this file exists. Every test page here
reads from the API, so with zero rows a large part of the backend is simply not exercised -- and a
suite cannot be said to cover a path it never walks.

Seeding also makes the assertions stronger rather than merely more numerous: with a scene present,
`/scenes` must render a row, and "the list is empty" stops being an acceptable outcome.

Verifies the seed worked before returning. A seeder that fails silently is worse than none: the suite
then passes against an empty instance and every result is quietly meaningless.
"""
import json
import pathlib
import sys
import urllib.error
import urllib.request

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:9982"


def gql(query, variables=None, timeout=30):
    body = json.dumps({"query": query, "variables": variables or {}}).encode()
    req = urllib.request.Request(
        BASE + "/graphql", data=body, headers={"Content-Type": "application/json"}
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return json.loads(r.read())
    except urllib.error.HTTPError as e:
        # Return the body instead of raising. urllib raises HTTPError for 4xx, so a GraphQL
        # validation error -- which arrives as HTTP 422 here -- became an opaque
        # "cannot reach <url>: HTTP Error 422", naming neither the query nor the offending field.
        # Every one of the schema mistakes made while writing this file was invisible for exactly
        # that reason, and each was a one-line fix once the body was visible.
        body = e.read().decode()[:400]
        return {"errors": [{"message": f"HTTP {e.code}: {body}"}]}


def must(result, what):
    """GraphQL returns HTTP 200 even for errors, so the status proves nothing."""
    if isinstance(result, dict) and result.get("errors"):
        print(f"  SEED FAILED at {what}: {json.dumps(result['errors'])[:300]}")
        sys.exit(1)
    return result["data"]


def ensure_studio():
    """Create the studio, or reuse it if it is already there.

    Idempotent on purpose. The mutation-check re-seeds before every mutant, and re-running the
    seeder against an instance that already holds the row fails with
    "studio with name 'E2E Studio' already exists" -- which aborts the seed and leaves the next
    mutant facing an instance with a studio and no scene. A fixture that only works once is a
    fixture that silently stops working.

    Looked up by name rather than remembered, because a fresh sqlite database per mutant means the
    id is different every time.
    """
    # Three shapes wrong in three different ways before this worked, each of them an HTTP 422:
    #   allStudios            does not exist; the query is findStudios
    #   name: "E2E Studio"    a filter field is a StringCriterionInput, not a bare string
    #   name: {equals: "X"}   the criterion needs {value, modifier}
    # and findStudios returns a count wrapper, so the rows live under `.studios`.
    # The schema was read from the running instance via __schema introspection rather than from the
    # .graphql files, which is the reliable source: those files were not where I assumed the Query
    # block lived.
    existing = gql(
        """query($f: StudioFilterType) {
             findStudios(studio_filter: $f) { count studios { id name } }
           }""",
        {"f": {"name": {"value": "E2E Studio", "modifier": "EQUALS"}}},
    )
    if "errors" not in existing:
        for st in existing["data"]["findStudios"]["studios"]:
            if st.get("name") == "E2E Studio":
                print(f"  studio already present: {st['id']}")
                return st["id"]

    data = must(
        gql(
            "mutation($s: StudioCreateInput!) { studioCreate(input: $s) { id name } }",
            {"s": {"name": "E2E Studio"}},
        ),
        "studioCreate",
    )
    return data["studioCreate"]["id"]


def ensure_performer():
    existing = gql(
        """query($f: PerformerFilterType) {
             findPerformers(performer_filter: $f) { count performers { id name } }
           }""",
        {"f": {"name": {"value": "E2E Performer", "modifier": "EQUALS"}}},
    )
    if "errors" not in existing:
        for p in existing["data"]["findPerformers"]["performers"]:
            if p.get("name") == "E2E Performer":
                print(f"  performer already present: {p['id']}")
                return p["id"]
    data = must(
        gql(
            "mutation($p: PerformerCreateInput!) { performerCreate(input: $p) { id name } }",
            {"p": {"name": "E2E Performer"}},
        ),
        "performerCreate",
    )
    return data["performerCreate"]["id"]


def create_scene(sid, pid):
    """Create the seeded scene and return its id."""
    data = must(
        gql(
            """mutation($s: SceneCreateInput!) {
                 sceneCreate(input: $s) { id title }
               }""",
            {
                "s": {
                    "title": "E2E Scene",
                    "details": "seeded by docs/e2e/seed.py so resolvers are actually exercised",
                    "studio_id": sid,
                    "performer_ids": [pid],
                    # `urls`, NOT `paths`. SceneCreateInput has no `paths` field -- a scene's files
                    # are attached via file_ids/stash_ids -- and `url` is deprecated in favour of
                    # `urls`. Sending `paths` returns
                    #   {"message":"unknown field","path":["variable","s","paths"],
                    #    "code":"GRAPHQL_VALIDATION_FAILED"}
                    # as HTTP 422. Which is worth recording: GraphQL VALIDATION errors arrive as
                    # 422 here, so a status-code check alone reads a malformed fixture as "the
                    # server is down" -- and the response body is the only thing that says which.
                    "urls": ["http://127.0.0.1:1/e2e-seeded-scene.mp4"],
                }
            },
        ),
        "sceneCreate",
    )
    return data["sceneCreate"]["id"]


def main():
    print(f"seeding {BASE}")

    # An empty library is legitimate, so the same query shape must work twice without erroring.
    # `findScenes` with no filter is how the suite reads the list back.
    must(gql("{ findScenes { count } }"), "pre-seed findScenes")

    sid = ensure_studio()

    pid = ensure_performer()

    existing = gql(
        """query($f: SceneFilterType) {
             findScenes(scene_filter: $f) { count scenes { id title } }
           }""",
        {"f": {"title": {"value": "E2E Scene", "modifier": "EQUALS"}}},
    )
    if "errors" not in existing:
        for sc in existing["data"]["findScenes"]["scenes"]:
            if sc.get("title") == "E2E Scene":
                # Assign, do not `return`. Returning from main() here skips the read-back
                # verification below entirely, so a re-run against an already-seeded instance
                # printed a success line having checked nothing. A fixture that only verifies on
                # its first run is a fixture that stops verifying exactly when it is re-run.
                print(f"  scene already present: {sc['id']}")
                scene_id = sc["id"]
                break
        else:
            scene_id = create_scene(sid, pid)
    else:
        scene_id = create_scene(sid, pid)

    # Read it back through the resolver that M1 panics in. If this does not resolve, the suite is
    # again testing an instance where that code never runs.
    data = must(
        gql(
            """query($id: ID!) {
                 findScene(id: $id) {
                   id title
                   # `paths` is an OBJECT type (ScenePathsType), not a list of strings, so it needs
                   # a selection set. Querying it bare is a validation error, not an empty result.
                   # ScenePathsType fields are the per-scene MEDIA URLS (screenshot, preview,
                   # stream, webp, vtt, sprite, funscript, ...), not filesystem paths. Reading
                   # `paths { path basename }` is a validation error:
                   #   Cannot query field "path" on type "ScenePathsType".
                   # Discovered by introspecting the type rather than by reading the schema file --
                   # the obvious name (`path`) is not a field, and guessing it cost a 422.
                   paths { screenshot stream }
                   studio { name }
                   performers { name }
                 }
               }""",
            {"id": scene_id},
        ),
        "findScene read-back",
    )
    scene = data["findScene"]
    if not scene or not scene.get("id"):
        print(f"  SEED FAILED: the scene does not resolve: {json.dumps(scene)[:200]}")
        sys.exit(1)

    # `{ findScenes { count } scenes { id } }` was MY typo -- the brace closed findScenes after
    # `count`, leaving `scenes` as a top-level field that does not exist on Query. gqlgen reported
    # it as "Cannot query field \"scenes\" on type \"Query\"", which reads like a schema problem
    # and sent me off introspecting the schema instead of reading the query I had just written.
    # FindScenesResultType has exactly: count, duration, filesize, scenes.
    counts = must(gql("{ findScenes { count scenes { id title } } }"), "post-seed findScenes")
    print(
        f"  seeded scene={scene['id']} title={scene['title']!r} "
        f"studio={scene.get('studio', {}).get('name')!r} "
        f"performers={[p['name'] for p in scene.get('performers') or []]} "
        f"count={counts['findScenes']['count']}"
    )
    if counts["findScenes"]["count"] < 1:
        print("  SEED FAILED: the scene list is still empty")
        sys.exit(1)
    print("  seed verified")


if __name__ == "__main__":
    try:
        main()
    except urllib.error.URLError as e:
        print(f"  SEED FAILED: cannot reach {BASE}: {e}")
        sys.exit(1)