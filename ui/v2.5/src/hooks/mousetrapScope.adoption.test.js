// #2833 -- the scoping layer must stay WIRED.
//
// ## WHY THIS IS A SEPARATE TEST FROM THE BEHAVIOUR ONES
//
// `mousetrapScope.fix.test.js` proves the layer WORKS. It does not prove anything CALLS
// it, and a complete, correct, fully-passing shortcut-scoping layer that no component
// imports is a feature that does not exist.
//
// This project has hit that shape five times and named it: "a store nothing can reach is
// a feature that does not exist", "the switch exists with nothing behind it". A coverage
// number cannot see it -- the layer's own files are fully covered. Only asking "does
// anything import this" can.
//
// So the assertion is about ADOPTION, and it fails the day the last importer goes away.

import test from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const SRC = fileURLToPath(new URL("..", import.meta.url));

// Every `.ts`/`.tsx` under src, so a component moved to a new directory is still found.
// A hand-maintained file list would go stale silently, which is the failure this file
// exists to prevent, applied to itself.
function allSourceFiles(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      out.push(...allSourceFiles(full));
    } else if (/\.tsx?$/.test(entry)) {
      out.push(full);
    }
  }
  return out;
}

const files = allSourceFiles(SRC);

// Anything under hooks/ is the layer's own code, so it does not count as an adopter.
const appFiles = files.filter(
  (f) => !relative(SRC, f).startsWith("hooks" + "/")
);

test("at least one component imports the scoping layer", () => {
  const importers = appFiles.filter((f) =>
    readFileSync(f, "utf8").includes("hooks/mousetrapScope")
  );

  assert.ok(
    importers.length > 0,
    "NOTHING imports src/hooks/mousetrapScope. The layer is complete, tested and " +
      "mutation-gated, and no component uses it -- so #2833 is unfixed while the suite " +
      "stays green. Wire at least one bindScoped call site."
  );
});

// The eight list components were the shared `e` / `d d` pair. Asserting all eight is
// stricter than needed for "the layer is not dead", and deliberately so: they were
// converted as ONE mechanical change from one identical code shape, so a partial
// conversion means the diff was applied unevenly, which is worth knowing immediately.
test("all eight list components bind their edit/delete keys through the layer", () => {
  const expected = [
    "components/Galleries/GalleryList.tsx",
    "components/Groups/GroupList.tsx",
    "components/Images/ImageList.tsx",
    "components/Performers/PerformerList.tsx",
    "components/Scenes/SceneList.tsx",
    "components/Scenes/SceneMarkerList.tsx",
    "components/Studios/StudioList.tsx",
    "components/Tags/TagList.tsx",
  ];

  const notConverted = expected.filter((rel) => {
    const src = readFileSync(join(SRC, rel), "utf8");
    // Must use bindScoped, AND must no longer bind these two keys unscoped. A file that
    // did both would appear converted while the old clobbering binding still ran.
    const scoped = src.includes("bindScoped");
    const stillUnscoped =
      /Mousetrap\.bind\("e"/.test(src) || /Mousetrap\.bind\("d d"/.test(src);
    return !scoped || stillUnscoped;
  });

  assert.deepEqual(
    notConverted,
    [],
    "these list components still bind `e` / `d d` unscoped. They were converted " +
      "together from one identical code shape, so a partial conversion means the " +
      "mechanical edit did not apply uniformly."
  );
});

// And the detail components -- the other half of the collision -- must be on the layer
// too, because a list that scopes `e` while a detail page still clobbers it reintroduces
// exactly the bug. `e` is bound by 8 lists and 8 details.
test("the detail components bind `e` through the layer, not unscoped", () => {
  const details = [
    "components/Tags/TagDetails/Tag.tsx",
    "components/Scenes/SceneDetails/Scene.tsx",
    "components/Performers/PerformerDetails/Performer.tsx",
    "components/Images/ImageDetails/Image.tsx",
    "components/Groups/GroupDetails/Group.tsx",
    "components/Galleries/GalleryDetails/Gallery.tsx",
    "components/Studios/StudioDetails/Studio.tsx",
  ];

  const unscoped = details.filter((rel) => {
    const src = readFileSync(join(SRC, rel), "utf8");
    return /Mousetrap\.bind\("e"/.test(src);
  });

  // This is EXPECTED TO FAIL right now: the detail pages are the second half of the
  // migration and have not been converted yet. It is committed in that state on purpose
  // -- a failing test that names the remaining work is worth more than a passing one that
  // hides it, and the count in `docs/ISSUES.md` is honest about what is done.
  assert.deepEqual(
    unscoped,
    [],
    `${unscoped.length} detail component(s) still bind "e" unscoped: ` +
      unscoped.map((f) => f.split("/").pop()).join(", ") +
      ". The lists are scoped and these are not, so the collision is half-fixed: a " +
      "detail page still clobbers the list's key on mount."
  );
});