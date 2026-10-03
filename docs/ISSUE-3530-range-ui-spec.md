# ISSUE-3530-PART2 — UI to set a scene's range

Spec, 2026-10-03. Sibling to `docs/ISSUE-3530-sprite-spec.md`.
Closes the second half of what `docs/WHATS-LEFT.md` records as open on #3530:

> **any UI to set a range** — the columns are SQL/API-settable only today.

## The problem, stated precisely

`scenes_files.start_time` / `end_time` exist, are validated by CHECKs, and are honoured by the
player, the previews, the HLS/DASH manifests and the sprite (`af5ea1a83`). **No user can create
one.** The only way to set a window is to open SQLite by hand and `UPDATE scenes_files`. So the
feature is complete for every consumer and unreachable for every user, which is the worst shape a
feature can be in: fully built, entirely unusable, and no error anywhere to say so.

The absence is not an oversight in the UI layer, it is a **missing surface**: `graphql/schema/
types/file.graphql`'s `VideoFile` type does not carry `start_time`/`end_time` at all, and
`SceneUpdateInput` has no field for them. There is nothing for a component to read or write.

## Why the window is not a Scene field (and must not become one)

The window lives on `models.VideoFile.StartTime/EndTime`, not on `Scene`. That looks like a
modelling mistake and is not: there is no per-scene-file type in the runtime model
(`Scene.Files` is `[]VideoFile`; the only `SceneFile` is the JSON *export* shape in
`pkg/models/jsonschema`). It is sound only because `pkg/sqlite`'s `GetFiles` returns a **fresh
copy per call** — one file can back several scenes with different windows, so the fields describe
*this scene's use of* the file and must never be cached or shared.

**Consequence for this spec, and it is the whole design:** a mutation cannot say "set scene X's
window" without saying *which file*, because the answer differs per file and a scene may have
several. Two rejected alternatives, recorded so they are not re-proposed:

| Option | Why rejected |
|---|---|
| Add `start_time`/`end_time` to `SceneUpdateInput` | Ambiguous the moment a scene has >1 file, and it would have to silently pick the primary. Two scenes of one file can hold different windows, so "the scene's window" is not a fact about the scene. |
| A new `SceneSetRange` mutation taking `scene_id` + `file_id` | Sound, but a *third* mutation shape for one field pair, and the UI would need two calls (set, then invalidate). Prefer the existing input. |

**Chosen:** extend the existing file-update path rather than adding a mutation, and expose both
ends on the `VideoFile` GraphQL type.

## Design

### 1. Schema — `graphql/schema/types/file.graphql`

On `type VideoFile`, alongside `duration`:

```graphql
  "The time this scene starts within this file, in seconds. null = the scene uses the whole file."
  start_time: Float
  "The time this scene ends within this file, in seconds. null or 0 = to the end of the file."
  end_time: Float
```

Exposed on the FILE type, not the scene type, because that is where they live. A client reading
`scene.files[0].start_time` gets the window that applies to *that* file's use by that scene,
which is the truth, and a scene with two files shows two windows rather than a fictional one.

### 2. Mutation input

On `input SceneUpdateInput`, beside `primary_file_id`:

```graphql
  """
  Set the window this scene takes from `primary_file_id`. Both null clears the window and
  restores the whole file. Send `end_time: 0` for an open-ended window.
  """
  start_time: Float
  end_time: Float
```

`SceneUpdateInput` already has `primary_file_id`, so the file is named by an existing field
rather than a new one. The window is written against the file the scene is being updated with, and
the resolver **refuses** to write a window when `primary_file_id` is absent and the scene has
more than one file — it cannot know which one you meant, and guessing is how two scenes of one
file end up sharing a window.

### 3. Validation, at the store and mirrored in the API

The database already CHECKs `start >= 0`, `end >= 0`, `end > start`. **The API must reject the
same cases with a message naming the field**, because a CHECK violation surfaces as a SQLite
error string, and a UI form that says `CHECK constraint failed: scenes_files` tells a user
nothing. The mirror is deliberate duplication, not an oversight: the CHECK is the last line and
the API check is the one a person reads.

Rejected cases, all of which must produce a 400 with the offending values in the message:
- `start_time < 0` or `end_time < 0`
- `end_time <= start_time` when both are set
- `end_time > the file's duration` — a window cannot run past the end of the file it is a window
  *of*. `GetFiles` already clamps this on read (`sceneFileRanges`), so an unclamped write would
  be a value the reader silently changes: a form that saves 9999 and then displays the real end
  is worse than one that refuses.
- both null → **valid**, and means "whole file" (clears the window)
- `start_time: 0` with no `end_time` → **valid**, an open-ended window from the head

### 4. What must be invalidated when a window changes

This is the part that decides whether the feature *works* rather than merely saves. Every
window-derived artefact is keyed by `GeneratedChecksum`, which now includes the window, so a
changed window names a **different** key — the old files are not overwritten, and the new ones
are generated on next request. Nothing is served stale. That is the payoff of the preview-key
work (`992e8da69`) and the sprite key work (`af5ea1a83`), and it is why this spec needs no
deletion step: the old key's files become unreferenced rather than wrong.

Consequence to state plainly: **changing a window does not regenerate anything eagerly.** The
sprite, previews and thumbs appear on next access, and the old ones stay on disk until the
existing cleanup runs. Accepted: an eager regenerate is a second code path through the generator
queue, and a stale-but-unreferenced file costs disk, not correctness.

### 5. UI — `SceneFileInfoPanel.tsx`

A row in the existing file-info `<dl>`, shown only for the **primary** file (a window on a
non-primary file is legal in the schema but is not what this feature is for, and the panel
already distinguishes primary from the rest):

- label: `media_info.range`
- value: the window rendered as `start – end` against the file's duration, or a localized
  "whole file" when neither end is set.
- an **Edit** button opening a small form with two duration inputs and a Clear button.

The form's floor and ceiling are the file's own `duration`, which the panel already has — so the
user cannot type a window the API will refuse, which is the cheapest possible validation.

`TextField` is not reusable for an editable pair; the form is a local component in the same file
with its own `useState`, submitted through the existing `mutateSceneUpdate`. No new dialog
framework, no new modal component.

### 6. i18n

Three new message ids in `en.json` (`media_info.range`, `actions.edit_range`,
`media_info.range_whole_file`) plus the existing `validation.*` namespace for the API's messages.
The repo's convention is that a component must not ship an untranslated string, so all three are
added rather than inlined.

## Verification

```bash
# the store writes what it is told and reads back the same numbers
go test ./pkg/sqlite/ -run TestSceneWindow -v

# the API refuses each case the CHECK would refuse, and accepts the two open forms
go test ./internal/api/ -run TestSceneWindowInput -v

# the resolver refuses an ambiguous window on a multi-file scene rather than guessing
go test ./internal/api/ -run TestSceneWindowAmbiguous -v

# the schema actually carries both fields, and the UI can query them
cd ui/v2.5 && npx tsc --noEmit && npx jest src/components/Scenes -t "range"
```

Plus the repo gates: `go build ./...`, `go vet ./...`, `go test ./...`,
`python3 docs/check-issue-ledgers.py`, `python3 docs/goal-check.py`.

## Mutations to prove the tests bite

A test that cannot fail is a comment. These are the mutations that must each turn the suite red;
they are the reason the API tests exist separately from the store tests, since the store's CHECKs
would mask a missing API check entirely.

| # | Mutation | Must be caught by |
|---|---|---|
| W1 | drop the `end > start` API check | the API test, not the store test |
| W2 | drop the `end <= duration` API check | the API test asserting 400 on an overrunning window |
| W3 | write the window even when the file is ambiguous | the multi-file resolver test |
| W4 | treat `both null` as a refusal instead of a clear | the clear-the-window test |
| W5 | treat `end_time: 0` as invalid instead of open-ended | the open-ended test |
| W6 | expose `start_time` on `Scene` instead of `VideoFile` | the schema-shape test |

## Out of scope, deliberately

- **Detection** — splitting a multi-scene file automatically. Needs an upstream discussion; a
  guess here writes wrong boundaries into a user's library, which is worse than no feature.
- **Multi-file windows in the UI.** The schema and API allow a window on any file; the UI only
  offers it on the primary. The API is the more permissive layer on purpose.
- **Eager regeneration** on window change. See §4.
- **Export keying.** Export keeps the plain hash, and `GeneratedChecksum`'s doc comment now says so
  explicitly as the one remaining exception.