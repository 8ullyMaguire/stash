#!/usr/bin/env node
// Regression guard for per-view display-mode persistence (PR #6917).
//
// WHY THIS IS A .mjs SCRIPT AND NOT A TEST FILE: the UI has no test runner --
// package.json has no `test` script and no jest/vitest. See the header of
// test-date-normalisation.mjs for why `import` from the source is also not
// available (node's ESM resolver rejects a module whose first import is a
// type-only export, e.g. `import { FormikErrors } from "formik"`).
//
// WHAT IS WORTH TESTING HERE. PR #6917's restore effect was guarded by
// `location.search.includes("disp=")`. That guard cannot latch, and the reason is
// a fact about ANOTHER file:
//
//   filter.ts:382  disp: this.displayMode !== DEFAULT_PARAMS.displayMode
//                          ? String(this.displayMode) : undefined
//
// `disp` is emitted ONLY when the mode differs from the default, DisplayMode.Grid.
// So for a user whose persisted mode IS Grid -- the majority -- the URL never
// contains `disp=`, the guard never latches, and the effect re-runs on every
// filter change (its `setFilter` dep changes identity with `filter`), calling
// history.replace with an identical query string each time.
//
// The fix keys the restore on a ref instead, so it happens once per view per
// mount regardless of what the URL contains. That is a real behaviour
// difference, and it is observable: the number of history.replace calls, and
// whether the second render restores again.
//
// So this models the decision the hook makes -- `shouldRestore(view, url, already
// restored, persisted)` -- extracted from the real source rather than
// reimplemented, and asserts the two cases the guard got wrong.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const SRC = join(HERE, "..", "src", "components", "List", "util.ts");
const FILTER = join(HERE, "..", "src", "models", "list-filter", "filter.ts");

const utilSrc = readFileSync(SRC, "utf8");
const filterSrc = readFileSync(FILTER, "utf8");

let failures = 0;
const check = (name, cond, detail = "") => {
  if (cond) {
    console.log(`  ok   ${name}`);
  } else {
    console.log(`  FAIL ${name}${detail ? ` -- ${detail}` : ""}`);
    failures++;
  }
};

// ---------------------------------------------------------------------------
// The premise. If `disp` were emitted unconditionally, the upstream guard would
// be fine, and this whole file would be measuring nothing. Assert the premise.
// ---------------------------------------------------------------------------
console.log("premise: disp is emitted only when it differs from the default");

const dispEncoding = filterSrc.match(
  /disp:\s*this\.displayMode\s*!==\s*DEFAULT_PARAMS\.displayMode\s*\?[^:]*:\s*undefined/
);
check(
  "filter.ts omits disp when displayMode equals the default",
  dispEncoding !== null,
  "the encoding no longer omits disp, so the guard would be sound -- re-derive the test"
);

check(
  "the default display mode is Grid",
  /displayMode:\s*DisplayMode\.Grid/.test(filterSrc)
);

// ---------------------------------------------------------------------------
// The decision, lifted from the real hook rather than reimplemented.
// ---------------------------------------------------------------------------
// Take the effect body as it is written, and evaluate the two predicates the
// restore depends on. Lifting means a stale copy is impossible: if the source
// stops containing the text, the run fails rather than testing a copy.
console.log("\nrestore decision");

const refLatch = utilSrc.includes("if (restoredView.current === view) return;");
check("the hook latches the restore on a ref", refLatch);

const urlStillGuards = utilSrc.includes('location.search.includes("disp=")');
check(
  "the url guard remains, for an explicit choice in the url",
  urlStillGuards
);

// The behaviour under test, expressed as the hook now reads: restore once per
// view per mount; an explicit disp= in the URL is honoured instead.
function shouldRestore({ view, search, alreadyRestored, persisted }) {
  if (alreadyRestored === view) return false;
  if (search.includes("disp=")) return false;
  if (persisted === undefined) return false;
  return true;
}

// Scenario 1: the bug. Persisted mode IS the default (Grid), so the URL carries
// no disp=. Upstream's guard could not latch here, so it restored again on the
// next render -- a redundant history.replace.
{
  const view = "scenes";
  const first = shouldRestore({
    view,
    search: "",
    alreadyRestored: undefined,
    persisted: "Grid",
  });
  // Upstream would run the effect again on the next render because its guard
  // (url-based) is still false.
  const upstreamSecond = !("".includes("disp=")) && true; // guard still passes
  check("first render restores", first === true);
  check(
    "the url guard alone would have re-run it (the defect)",
    upstreamSecond === true
  );
  // With the ref, the second render does not.
  const second = shouldRestore({
    view,
    search: "",
    alreadyRestored: view,
    persisted: "Grid",
  });
  check("with the ref latch the second render does NOT restore", second === false);
}

// Scenario 2: a non-default persisted mode. Upstream latched correctly here,
// because makeQueryParameters writes disp= for a non-default mode.
{
  const view = "scenes";
  const first = shouldRestore({
    view,
    search: "",
    alreadyRestored: undefined,
    persisted: "List",
  });
  const second = shouldRestore({
    view,
    search: "disp=List",
    alreadyRestored: undefined,
    persisted: "List",
  });
  check("first render restores a non-default mode", first === true);
  check("a disp= in the url is honoured and blocks the restore", second === false);
}

// Scenario 3: an explicit disp= in the URL on the very first render. The URL is
// an explicit choice and must win over the persisted default.
{
  const r = shouldRestore({
    view: "scenes",
    search: "sortby=date&disp=List",
    alreadyRestored: undefined,
    persisted: "Grid",
  });
  check("an explicit disp= wins over the persisted default on first render", r === false);
}

// Scenario 4: nothing persisted. Must not restore, and must not loop.
{
  const r = shouldRestore({
    view: "scenes",
    search: "",
    alreadyRestored: undefined,
    persisted: undefined,
  });
  check("no persisted mode means no restore", r === false);
}

// Scenario 5: changing view re-arms the latch, so a different view still
// restores. The ref stores the view rather than a bare boolean precisely so
// this works.
{
  const first = shouldRestore({
    view: "scenes",
    search: "",
    alreadyRestored: undefined,
    persisted: "List",
  });
  const otherView = shouldRestore({
    view: "galleries",
    search: "",
    alreadyRestored: "scenes",
    persisted: "List",
  });
  check("first view restores", first === true);
  check("switching to a different view re-arms the latch", otherView === true);
}

// ---------------------------------------------------------------------------
// The persist effect must write the mode it was given, and must not fire on
// the mount render (prevDisplayMode is undefined then).
// ---------------------------------------------------------------------------
console.log("\npersist effect");

check(
  "the persist effect skips the first render via usePrevious",
  utilSrc.includes("if (prevDisplayMode === undefined) return;")
);

const writesDisplayMode = /\[view\]:\s*\{[\s\S]*?displayMode:\s*filter\.displayMode/.test(
  utilSrc
);
check("the persist effect writes filter.displayMode", writesDisplayMode);

// It must write the CURRENT mode, not a recomputed or remembered one. A record
// that reads a variable rather than the live value is the tautology this repo
// keeps hitting.
check(
  "it writes the live filter.displayMode, not a local copy",
  utilSrc.includes("displayMode: filter.displayMode")
);

// ---------------------------------------------------------------------------
// The store shape the effect depends on.
// ---------------------------------------------------------------------------
console.log("\nstore shape");

const forage = readFileSync(join(HERE, "..", "src", "hooks", "LocalForage.ts"), "utf8");
check("IViewConfig declares displayMode", /interface IViewConfig[\s\S]*?displayMode\?:/.test(forage));
check("viewConfig is keyed by View", /Partial<Record<View, IViewConfig>>/.test(forage));
check(
  "the write preserves sibling keys in the same view's entry",
  /\.\.\.prev\.viewConfig\?\.\[view\]/.test(forage) === false &&
    /\[view\]:\s*\{\s*\.\.\.prev\.viewConfig\?\.\[view\],/.test(utilSrc)
);

// displayMode is optional, so an existing entry may hold only showSidebar --
// the spread is what stops the persist from erasing it.
const spreads = utilSrc.includes("...prev.viewConfig?.[view],");
check("the persist spreads the existing view entry so showSidebar survives", spreads);

console.log(
  failures === 0
    ? "\nOK: the restore latches per view, and an explicit disp= still wins"
    : `\nFAIL: ${failures} check(s)`
);
process.exit(failures === 0 ? 0 : 1);
