#!/usr/bin/env python3
"""Annotate every Mutation root field with @requiresWriteRole.

Run from the repo root:

    python3 scripts/annotate-mutations.py
    go generate ./cmd/stash

## Why this script exists

The point is not to guess well -- it is to make every guess REVIEWABLE. A field
annotated with the wrong role is visible in a diff; a field annotated with nothing
is invisible until it leaks, which is the failure this whole scheme exists to
catch. Hand-writing 137 directives invites not writing them.

## Placement, and why the obvious thing does not work

GraphQL SDL requires a directive to sit INSIDE the field definition, after the
return type:

    register(input: RegisterInput!): AuthPayload! @requiresWriteRole(role: "public")

It cannot go on its own line before the field. Verified against gqlparser
v2.5.27, which rejects all three of these with "Expected Name, found @":

    type Mutation {             type Mutation {             type Mutation {
      @foo(role: "x")             "desc"                       @foo(role: "x")
      register(x: Int): Int         @foo(role: "x")             "desc"
                                  register(x: Int): Int         register(x: Int): Int

The first version of this script emitted the second form and gqlgen refused to
load the schema. The error points AT the directive, which reads like a malformed
directive rather than misplaced placement -- so placement is documented here
rather than left to be rediscovered.

## Idempotent

A field already carrying the directive is left alone; verified by running twice
and comparing counts. The first version checked for the directive on the field's
own line while writing it to the previous line, so every run annotated every field
twice.
"""
import glob
import pathlib
import re
import sys

# scripts/annotate-mutations.py -> parents[0]=scripts, parents[1]=repo root.
REPO = pathlib.Path(__file__).resolve().parents[1]
GQLGEN = REPO / "gqlgen.yml"
DIRECTIVE = "@requiresWriteRole"


def schema_files():
    """Every .graphql file codegen actually loads, read from gqlgen.yml.

    The globs are PARSED rather than restated, because a restated copy is a
    second thing to keep in step. The first version of this script hardcoded
    `graphql/schema/schema.graphql` and that file is only ONE of the schema's
    sources: `gqlgen.yml` loads `graphql/schema/types/*.graphql` as well, and
    three of those files carry `extend type Mutation` blocks.

    Those 14 fields were therefore never annotated, and nothing said so. The
    coverage test caught them -- `Mutation.propose`, `Mutation.vote`,
    `Mutation.moderate`, the three 2FA mutations, the three cluster mutations,
    and the four library/hosting mutations -- because it walks the MERGED
    schema, which is exactly the thing a single-file script does not see.
    """
    globs = []
    in_schema = False
    for line in GQLGEN.read_text().splitlines():
        if re.match(r"^schema:", line):
            in_schema = True
            continue
        if in_schema:
            # The schema block ends at the first non-indented, non-blank line.
            if line.strip() and not line.startswith((" ", "\t", "-")):
                break
            m = re.match(r'^\s*-\s*"?([^"\s]+)"?\s*$', line)
            if m:
                globs.append(m.group(1))

    if not globs:
        # Loud, not silent: an empty scope would annotate nothing and report
        # success, which is the shape of bug this script just had.
        raise SystemExit(
            f"no schema globs found in {GQLGEN.name}; refusing to annotate "
            "nothing. Check the 'schema:' block."
        )

    out = []
    for pattern in globs:
        out.extend(pathlib.Path(p) for p in glob.glob(str(REPO / pattern)))
    return sorted(set(out))

# The spec's five-role ladder (Commons §8.3). Roles are ordered, so an annotation
# of `steward` is satisfied by steward and by admin.
#
# public      -- the no-login role. Auth entry points must be reachable by someone
#                who is not logged in, or nobody can ever log in.
# contributor -- curation: creating and editing shared metadata.
# steward     -- moderation, and anything that moves or destroys shared content.
# admin       -- settings, roles, plugins, raw SQL, backup and restore.
PUBLIC = {
    "register", "login", "logout",
}
CONTRIBUTOR = {
    "sceneCreate", "sceneUpdate", "sceneMerge", "bulkSceneUpdate",
    "imageUpdate", "bulkImageUpdate",
    "galleryCreate", "galleryUpdate", "bulkGalleryUpdate",
    "addGalleryImages", "removeGalleryImages", "setGalleryCover", "resetGalleryCover",
    "galleryChapterCreate", "galleryChapterUpdate",
    "performerCreate", "performerUpdate", "bulkPerformerUpdate", "performerMerge",
    "studioCreate", "studioUpdate", "bulkStudioUpdate",
    "movieCreate", "movieUpdate", "bulkMovieUpdate",
    "groupCreate", "groupUpdate", "bulkGroupUpdate",
    "addGroupSubGroups", "removeGroupSubGroups", "reorderSubGroups",
    "tagCreate", "tagUpdate", "bulkTagUpdate",
    # Governance itself: any logged-in account may propose and vote. `moderate` is
    # steward because it is the moderation queue.
    "propose", "vote", "withdraw",
    # Activity and organisation are personal, not shared metadata.
    "sceneSaveActivity", "sceneResetActivity", "sceneIncrementPlayCount",
    "sceneAddPlay", "sceneDeletePlay", "sceneResetPlayCount",
    "sceneAddO", "sceneDeleteO", "sceneIncrementO", "sceneDecrementO", "sceneResetO",
    "imageIncrementO", "imageDecrementO", "imageResetO",
    "saveFilter", "destroySavedFilter", "setDefaultFilter",
    "namePersonCluster", "clearPersonClusterName", "setPersonClusterHandle",
    "beginTOTPEnrollment", "confirmTOTPEnrollment", "disableTOTP",
}
STEWARD = {
    "sceneDestroy", "scenesDestroy", "scenesUpdate",
    "imageDestroy", "imagesDestroy", "imagesUpdate",
    "galleryDestroy", "galleriesDestroy", "galleriesUpdate",
    "galleryChapterDestroy",
    "performerDestroy", "performersDestroy",
    "studioDestroy", "studiosDestroy",
    "movieDestroy", "moviesDestroy", "bulkMovieUpdate",
    "groupDestroy", "groupsDestroy", "bulkGroupUpdate",
    "tagDestroy", "tagsDestroy", "tagsMerge", "bulkTagUpdate",
    "sceneMarkerCreate", "sceneMarkerUpdate", "bulkSceneMarkerUpdate",
    "sceneMarkerDestroy", "sceneMarkersDestroy", "sceneAssignFile",
    "metadataImport", "metadataExport", "metadataGenerate", "metadataAutoTag",
    "metadataClean", "metadataCleanGenerated", "metadataIdentify",
    "moveFiles", "deleteFiles", "destroyFiles", "fileSetFingerprints",
    "faceClustering", "submitStashBoxFingerprints",
    "submitStashBoxSceneDraft", "submitStashBoxPerformerDraft",
    "stashBoxBatchPerformerTag", "stashBoxBatchStudioTag", "stashBoxBatchTagTag",
    "moderate", "grantLibraryAccess", "setConsent", "createLibrary", "deleteLibrary",
}
ADMIN = {
    "setup", "migrate", "downloadFFMpeg",
    "configureGeneral", "configureInterface", "configureDLNA", "configureScraping",
    "configureDefaults", "configurePlugin", "configureUI", "configureUISetting",
    "generateAPIKey",
    # Raw SQL and whole-database operations: the most dangerous things here, and
    # the reason they are listed rather than left to a default.
    "querySQL", "execSQL",
    "exportObjects", "importObjects",
    "anonymiseDatabase", "optimiseDatabase", "backupDatabase",
    "metadataScan", "migrateHashNaming", "migrateSceneScreenshots", "migrateBlobs",
    "reloadScrapers",
    # Plugins and packages execute third-party code.
    "setPluginsEnabled", "runPluginTask", "runPluginOperation", "reloadPlugins",
    "installPackages", "updatePackages", "uninstallPackages",
    "stopJob", "stopAllJobs", "enableDLNA", "disableDLNA",
    "addTempDLNAIP", "removeTempDLNAIP",
    "sceneGenerateScreenshot",
    "revealFileInFileManager", "revealFolderInFileManager",
}

ROLES = (("public", PUBLIC), ("subscriber", set()),
         ("contributor", CONTRIBUTOR), ("steward", STEWARD), ("admin", ADMIN))


def role_for(name):
    """Returns (role, was_guessed). Guessed fields default to admin."""
    for role, names in ROLES:
        if name in names:
            return role, False
    return "admin", True


def annotate_block(lines, unclassified):
    """Annotate every field in one Mutation block, returning new lines and count.

    Handles both `type Mutation {` and `extend type Mutation {`, because gqlgen
    merges both into the one root and the coverage test walks the merged result.
    """
    out = []
    i = 0
    annotated = 0

    while i < len(lines):
        line = lines[i]

        # A field definition starts at two-space indentation with a name. Anything
        # else -- a description, a blank line, a closing brace -- passes through.
        m = re.match(r"^  ([A-Za-z_]\w*)\s*[(:]", line)
        if not m:
            out.append(line)
            i += 1
            continue

        name = m.group(1)

        # A field definition ends when its parentheses balance, which is what
        # handles an argument list spread over several lines.
        depth = line.count("(") - line.count(")")
        field_lines = [line]
        while depth > 0 and i + 1 < len(lines):
            i += 1
            field_lines.append(lines[i])
            depth += lines[i].count("(") - lines[i].count(")")

        definition = "".join(field_lines)
        if DIRECTIVE in definition:
            out.extend(field_lines)
            i += 1
            continue

        role, guessed = role_for(name)
        if guessed:
            unclassified.append(name)

        last = field_lines[-1]
        newline = "\n" if last.endswith("\n") else ""
        body = last[: len(last) - len(newline)]
        field_lines[-1] = f'{body} {DIRECTIVE}(role: "{role}"){newline}'

        out.extend(field_lines)
        annotated += 1
        i += 1

    return out, annotated


def main() -> int:
    unclassified = []
    total = 0
    touched = 0

    for path in schema_files():
        src = path.read_text()
        lines = src.splitlines(keepends=True)

        out = []
        i = 0
        file_annotated = 0

        while i < len(lines):
            line = lines[i]
            # `type Mutation {` or `extend type Mutation {`, both at column 0.
            if re.match(r"^(extend\s+)?type\s+Mutation\s*\{", line):
                # Consume to the block's closing brace.
                j = i
                block = [line]
                while "}" not in block[-1] and j + 1 < len(lines):
                    j += 1
                    block.append(lines[j])
                new_block, n = annotate_block(block[1:], unclassified)
                out.append(block[0])
                out.extend(new_block)
                file_annotated += n
                i = j + 1
                continue

            out.append(line)
            i += 1

        if file_annotated:
            path.write_text("".join(out))
            touched += 1
            total += file_annotated

    print(f"annotated {total} mutation root field(s) across {touched} file(s)")
    if unclassified:
        print(f"UNCLASSIFIED ({len(unclassified)}) -- defaulted to admin:",
              file=sys.stderr)
        for n in sorted(unclassified):
            print(f"  {n}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())