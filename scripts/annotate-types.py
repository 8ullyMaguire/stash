#!/usr/bin/env python3
"""Annotate schema TYPES with @requiresRole or @publicRead.

Run from the repo root:

    python3 scripts/annotate-types.py
    go generate ./cmd/stash

## Why types rather than fields

The read side is 2,356 fields across 285 types. Annotating every field is 2,356
decisions, and 2,356 decisions nobody reviews is the same as no decisions at all --
it produces a diff so large that the annotations stop being readable, which defeats
the purpose. A type-level directive is 285, and the only case worth a field-level
override is a field stricter than its type, which is a small and genuinely
interesting set.

So the review unit is a type, and a field-level annotation is the exception.

## Placement -- and the first version of this script was wrong in a way that
## made the schema unparseable

A type-level directive goes AFTER the name and BEFORE the brace:

    type Tag @publicRead {
      id: ID!
    }

It cannot go inside the body, and the error is misleading enough to be worth
recording. Measured against gqlparser v2.5.27, the version this repo pins:

    type User {          type Tag {
      @requiresRole(x)     @publicRead
      id: ID!             id: ID!
    }                    }

    -> probe:7:3: Expected Name, found @        (BOTH of these)

The error points AT the directive and names a Name, which reads like a
misspelled directive rather than misplaced placement. The first version of this
script wrote the inside-body form, annotated all 285 types, and then gqlgen
refused to load the schema at all -- so the annotations were not merely ignored,
they took the build down with them.

The directive definitions also need `| OBJECT` (and `| INTERFACE`) for the
type-level form to be *legal*, which the first version of access.graphql did
not have:

    directive @publicRead on FIELD_DEFINITION
    type Tag @publicRead { id: ID! }
      -> Directive publicRead is not applicable on OBJECT.

## Scope is read from gqlgen.yml, not restated

The schema is more than one file: `gqlgen.yml` loads both
`graphql/schema/types/*.graphql` and `graphql/schema/*.graphql`, and three of
the type files carry `extend type Query` blocks whose types must be annotated
like any other. This script walks `graphql/schema/` recursively for that reason.

## Idempotent

A type already carrying a directive is left alone.
"""
import pathlib
import re
import sys

# scripts/annotate-types.py -> parents[0]=scripts, parents[1]=repo root.
REPO = pathlib.Path(__file__).resolve().parents[1]
SCHEMA_DIR = REPO / "graphql/schema"

# Roles, from the spec's five-role ladder (Commons §8.3).
#
# The classification is by NAME PREFIX, because stash's schema is generated from
# Go structs and its type names are systematic: `*FilterType` is always a filter,
# `Config*Result` is always instance configuration, and so on. A prefix table is
# 285 types expressed in about thirty rules, which is reviewable; a per-type list
# is not, and would be wrong within a month as types are added.
#
# Anything unclassified defaults to `subscriber` -- any logged-in account -- and is
# reported. That default is deliberately the permissive-but-not-public one: a new
# type shows to logged-in users rather than to the internet, and the report means
# nobody has to notice the difference.
PUBLIC_TYPES = {
    # Public site content. A public instance needs a title, a performer name and a
    # scene's runtime to be readable without a login -- that is the whole of what
    # "public site" means.
    "Scene", "Performer", "Studio", "Tag", "Group", "Gallery", "Image", "Movie",
    "File", "SceneFile", "PerformerAppearance",
    "Identification", "StudioAlias", "PerformerAlias", "TagAlias", "GroupTag",
}
PUBLIC_PREFIXES = (
    "Scraped",        # scraper results: fetched from a remote the operator chose
    "BulkAdd", "BulkUpdate", "BulkRemove",
)
SUBSCRIBER_PREFIXES = (
    "FilterType", "SortDirectionEnum", "CriterionOption", "CriterionInput",
    "FindFilterType", "CountFilterType",
    "PerformerAppearanceFilterType",
    "Bookmark", "SavedFilter",
    "Generated", "StashBox", "JobSpec",
)
SUBSCRIBER_TYPES = {
    "User", "AuthPayload", "ConfigGeneralResult", "ConfigInterfaceResult",
    "Version", "LatestVersion", "SystemStatus", "SystemInitStatus",
    "Stats", "LibraryPathsResult", "ServerCacheResult", "DLNAServerStatus",
    "Job", "LogEntry", "Plugin", "PluginOption", "Package", "PackageStatus",
    "Migration", "Cluster", "SceneSimilarity",
}
ADMIN_PREFIXES = (
    "Config",          # instance settings, both directions
    "Metadata",        # bulk metadata operations
)
ADMIN_TYPES = {
    "APIKey", "ApiKey", "TempDLNAIP", "RemoteSite", "DLNAserver",
}

# The root types that are never annotated at the type level. ONLY Mutation:
# every field on it is a write carrying its own @requiresWriteRole, and
# `@requiresRole` there would be a claim about reading a write. Query and
# Subscription are NOT here — both are read surfaces with a genuinely shared
# floor, and 87 + 3 fields annotated individually would be the same as none.
ROOT_TYPES = {"Mutation"}


def role_for(type_name: str) -> str:
    if type_name in PUBLIC_TYPES:
        return "public"
    if type_name in ADMIN_TYPES:
        return "admin"
    if type_name in SUBSCRIBER_TYPES:
        return "subscriber"
    for pfx in ADMIN_PREFIXES:
        if type_name.startswith(pfx):
            return "admin"
    for pfx in SUBSCRIBER_PREFIXES:
        if type_name.startswith(pfx):
            return "subscriber"
    for pfx in PUBLIC_PREFIXES:
        if type_name.startswith(pfx):
            return "public"
    return "subscriber"  # the default; reported by the caller


def main() -> int:
    unclassified = []
    counts = {"public": 0, "subscriber": 0, "contributor": 0, "steward": 0, "admin": 0}
    annotated = 0
    already = 0

    for path in sorted(SCHEMA_DIR.rglob("*.graphql")):
        if path.name == "access.graphql":
            # Holds the directive DEFINITIONS, not types to annotate.
            continue
        src = path.read_text()
        lines = src.splitlines(keepends=True)
        out = []
        i = 0
        changed = False

        while i < len(lines):
            line = lines[i]
            # `type Foo ... {`, `interface Foo {`, or `extend type Foo {` at
            # column 0. The optional `extend` is here because three type files
            # extend Query, and gqlgen merges those fields into the one root.
            m = re.match(r"^(extend\s+)?(type|interface)\s+([A-Za-z_]\w*)", line)
            if not m or "{" not in line:
                out.append(line)
                i += 1
                continue

            name = m.group(3)

            # The root types are decided per FIELD, never per type, and annotating
            # them is not merely redundant -- it is misleading. `@requiresRole` is
            # the READ floor, and a Mutation root has no reads: every field on it is
            # a write carrying its own @requiresWriteRole. So the first run of this
            # script emitted
            #
            #     type Mutation @requiresRole(role: "subscriber") {
            #
            # which reads as a claim about who may read a write, and contradicts the
            # per-field directives two lines below it. Query and Subscription ARE
            # annotated, because their fields genuinely have a shared read floor and
            # 300-odd individual annotations would be the same as none.
            if name in ROOT_TYPES:
                out.append(line)
                i += 1
                continue

            # Collect the whole type body so an existing directive is visible.
            body = [line]
            j = i
            while "}" not in body[-1] and j + 1 < len(lines):
                j += 1
                body.append(lines[j])

            existing = "".join(body)
            if "@requiresRole" in existing or "@publicRead" in existing:
                out.extend(body)
                already += 1
                i = j + 1
                continue

            role = role_for(name)
            if role == "subscriber" and name not in SUBSCRIBER_TYPES \
                    and not any(name.startswith(p) for p in SUBSCRIBER_PREFIXES):
                unclassified.append(name)

            directive = ('@publicRead' if role == "public"
                         else f'@requiresRole(role: "{role}")')

            # AFTER the name and BEFORE the brace -- the only placement gqlparser
            # accepts for a type-level directive. See the placement section.
            first = body[0]
            brace = first.index("{")
            newline = "\n" if first.endswith("\n") else ""
            body[0] = f"{first[:brace].rstrip()} {directive} {first[brace:]}{newline}"

            out.extend(body)
            counts[role] += 1
            annotated += 1
            changed = True
            i = j + 1

        if changed:
            path.write_text("".join(out))

    print(f"annotated {annotated} type(s); {already} already carried a directive")
    for role in ("public", "subscriber", "admin"):
        print(f"  {role:12s} {counts[role]}")
    if unclassified:
        print(f"DEFAULTED to subscriber ({len(unclassified)}) -- review these:",
              file=sys.stderr)
        for n in sorted(unclassified):
            print(f"  {n}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())