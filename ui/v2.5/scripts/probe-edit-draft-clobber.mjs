// stash#6466: unsaved entries lost in Scene Edit Tags / Performers.
//
// WHY A .mjs SCRIPT: the UI has no test runner (no `test` script, no jest/vitest).
// Same precedent as probe-apollo-reset-race.mjs.
//
// THE BUG. The scene editor keeps DRAFTS of props that also arrive by background
// refetch:
//
//   SceneEditPanel.tsx
//     useEffect(() => setPerformers(scene.performers ?? []), [scene.performers])
//   hooks/tagsEdit.tsx
//     useEffect(() => setTags(srcTags ?? []), [srcTags])
//
// `scene` is replaced wholesale whenever the query returns, and a PLAYING video
// makes that happen on a timer: track-activity.ts runs a 1s interval and every
// sendInterval = 10 SECONDS calls sendActivity(), which awaits sceneSaveActivity
// and (past minimumPlayPercent) sceneIncrementPlayCount. Apollo normalises those
// mutation results back into the cache, so `data` changes identity, and
// Scene.tsx's
//
//   useLayoutEffect(() => { if (!loading) setScene(data?.findScene) }, [data, loading])
//
// sets a brand-new `scene` object. Every `scene.performers`-shaped value is then a
// fresh array, the effect re-runs, and the unsaved draft is overwritten with the
// saved values. Same for tags -- the reporter lost both boxes.
//
// `player.on('pause', () => this.stop())` is why it "does not reproduce when the
// video is paused": the timer is the trigger, not the select box.
//
// The large maxOptionsShown in the repro is an AMPLIFIER, not the cause: a slower
// select query keeps the input focused with an unsaved entry for longer, widening
// the window in which the next 10s tick lands.
//
// THE FIX under test: `useInitialState` instead of `useState`. It applies an
// incoming value only while the current value is still pristine, so a dirty draft
// survives a refetch. hooks/state.ts already existed and already documented this
// ("only updated if the current state is unchanged from the initial state"); the
// edit panel and the tags hook were simply not using it.
//
// HOW IT IS EXERCISED. The shipped src/hooks/state.ts is compiled with the
// project's own tsc and run against a MINIMAL REACT STUB whose useState is a cell
// and whose useCallback is the identity. That is enough, because the dirty check
// lives entirely in the functional updaters, and a functional updater is a pure
// function of the previous value -- React only supplies "the previous value".
//
// Deliberately not a hand-copied reimplementation: the premise is that the hook
// actually shipped in hooks/state.ts is what protects the drafts, so testing a
// copy would prove nothing. Same reasoning as testing a decider instead of the
// deleter that calls it.
//
// Also not react-dom: this tree has no react-test-renderer, jsdom, linkedom or
// happy-dom, and react-dom/server renders once and discards state, so a hook with
// state cannot be stepped through it. The first version of this probe tried exactly
// that and every behavioural assertion failed with the draft frozen at its initial
// value -- a harness bug that read like a hook bug.

import { readFileSync, mkdtempSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";
import { tmpdir } from "node:os";

const HERE = dirname(fileURLToPath(import.meta.url));
const SRC = join(HERE, "..", "src");

let failures = 0;
const check = (name, cond, detail = "") => {
  if (cond) console.log(`  ok   ${name}`);
  else {
    console.log(`  FAIL ${name}${detail ? ` -- ${detail}` : ""}`);
    failures++;
  }
};

// ---------------------------------------------------------------------------
// Compile the shipped hook, once, and build a React stub around it.
// ---------------------------------------------------------------------------
const COMPILED = (() => {
  const outDir = mkdtempSync(join(tmpdir(), "stash6466-"));
  const res = spawnSync(
    process.execPath,
    [
      join(HERE, "..", "node_modules", "typescript", "bin", "tsc"),
      join(SRC, "hooks", "state.ts"),
      "--outDir", outDir,
      "--module", "esnext",
      "--target", "es2020",
      "--skipLibCheck",
      // src/hooks/state.ts does `import React, { useState } from "react"`, which
      // needs this to typecheck. The project's own tsconfig sets it too.
      "--allowSyntheticDefaultImports",
      "--esModuleInterop",
    ],
    { encoding: "utf8" }
  );
  if (res.status !== 0) {
    console.error(res.stdout || "", res.stderr || "");
    throw new Error("tsc failed to compile src/hooks/state.ts");
  }
  return readFileSync(join(outDir, "state.js"), "utf8")
    .replace(/^import .*$/gm, "")
    .replace(/^export /gm, "");
})();

/** A one-cell store with React's functional-updater semantics. */
function makeCell(initial) {
  let value = initial;
  return {
    get: () => value,
    set: (next) => {
      value = typeof next === "function" ? next(value) : next;
    },
  };
}

// Cells the hook's useState calls write to, in call order.
let cells = [];
let cellIndex = 0;
const ReactStub = {
  useState: (initial) => {
    const cell = makeCell(initial);
    cells[cellIndex++] = cell;
    return [cell.get(), cell.set];
  },
  useCallback: (fn) => fn, // the hook's only callback takes no deps
  useRef: (initial) => ({ current: initial }),
  useEffect: () => {},
  useMemo: (fn) => fn(),
  useContext: () => undefined,
};

// biome-ignore lint/security/noGlobalEval: evaluating the shipped source is the point
const evalHook = new Function(
  "React",
  "useState", "useCallback", "useRef", "useEffect", "useMemo",
  `${COMPILED}\nreturn useInitialState;`
);

/** Instantiate the SHIPPED hook, as a component would on mount. */
function mountHook(initialValue) {
  cells = [];
  cellIndex = 0;
  const hook = evalHook(
    ReactStub,
    ReactStub.useState,
    ReactStub.useCallback,
    ReactStub.useRef,
    ReactStub.useEffect,
    ReactStub.useMemo
  );
  hook(initialValue);
  const valueCell = cells[1]; // cells[0] is setInitialValueInternal
  return {
    setValue: valueCell.set,
    setInitialValue: hook.__setInitialValue,
    value: () => valueCell.get(),
  };
}

// The hook returns the setter created by its own useCallback, so capture it by
// calling the hook and taking the third tuple member.
function mountHookWithSetters(initialValue) {
  cells = [];
  cellIndex = 0;
  const hook = evalHook(
    ReactStub,
    ReactStub.useState,
    ReactStub.useCallback,
    ReactStub.useRef,
    ReactStub.useEffect,
    ReactStub.useMemo
  );
  const [, setValue, setInitialValue] = hook(initialValue);
  const valueCell = cells[1];
  return { setValue, setInitialValue, value: () => valueCell.get() };
}

// ---------------------------------------------------------------------------
{
  cells = [];
  cellIndex = 0;
  evalHook(
    ReactStub,
    ReactStub.useState,
    ReactStub.useCallback,
    ReactStub.useRef,
    ReactStub.useEffect,
    ReactStub.useMemo
  )([]);
  check(
    "the hook holds two useState cells (initial value, then value)",
    cells.length === 2 && cells.every(Boolean),
    `cells=${cells.length}`
  );
}

console.log("premise: a pristine draft takes the incoming value");
{
  const h = mountHookWithSetters(["a"]);
  h.setInitialValue(["b"]);
  check(
    "the incoming value applies while the draft is untouched",
    h.value().join() === "b",
    JSON.stringify(h.value())
  );
}

// ---------------------------------------------------------------------------
console.log("\nTHE DEFECT: a background refetch must not clobber a dirty draft");
{
  const h = mountHookWithSetters(["a"]);

  // The user types an unsaved entry. This is exactly what the reporter lost.
  h.setValue(["a", "UNSAVED-TYPED-ENTRY"]);
  check(
    "the unsaved entry is in the draft",
    h.value().includes("UNSAVED-TYPED-ENTRY"),
    JSON.stringify(h.value())
  );

  // The 10-second playback tick: Apollo writes play_count back into the cache,
  // `data` changes identity, Scene.tsx sets a NEW scene object, and
  // `scene.performers` is a fresh array with identical contents. The syncing
  // effect re-runs and hands the same value back in.
  h.setInitialValue(["a"]);
  check(
    "the refetch did NOT discard the unsaved entry",
    h.value().includes("UNSAVED-TYPED-ENTRY"),
    `draft became ${JSON.stringify(h.value())}`
  );
}

// ---------------------------------------------------------------------------
console.log("\nthe draft stays dirty across repeated ticks");
// A playing video sends one every 10s, so the clobbering would repeat rather than
// happening once.
{
  const h = mountHookWithSetters(["a"]);
  h.setValue(["a", "typed"]);
  for (let i = 0; i < 5; i++) {
    h.setInitialValue(["a"]);
  }
  check(
    "five refetches leave the draft intact",
    h.value().join() === "a,typed",
    JSON.stringify(h.value())
  );
}

// ---------------------------------------------------------------------------
console.log("\na GENUINE server change must still land while the draft is pristine");
// The fix must not become "ignore props forever": if another client changed the
// scene and the user has not touched the field, the new value has to win.
{
  const h = mountHookWithSetters(["a"]);
  h.setInitialValue(["a", "added-elsewhere"]);
  check(
    "a pristine draft accepts a real server-side change",
    h.value().includes("added-elsewhere"),
    JSON.stringify(h.value())
  );
}

// ---------------------------------------------------------------------------
console.log("\nthe draft must track edits made after a sync");
// The failure mode a "never overwrite" fix would introduce is a field frozen
// permanently, because the dirty check can never be cleared.
{
  const h = mountHookWithSetters(["a"]);
  h.setValue(["a", "typed"]);
  h.setInitialValue(["a", "typed", "saved-elsewhere"]);
  h.setValue(["a", "typed", "saved-elsewhere", "edited-again"]);
  check(
    "the draft still accepts edits after a sync",
    h.value().includes("edited-again"),
    JSON.stringify(h.value())
  );
}

// ---------------------------------------------------------------------------
console.log("\nthe shipped components actually use the initial-state setters");
// A hook fix nothing calls is not a fix.
{
  const editPanel = readFileSync(
    join(SRC, "components", "Scenes", "SceneDetails", "SceneEditPanel.tsx"),
    "utf8"
  );
  const tagsEdit = readFileSync(join(SRC, "hooks", "tagsEdit.tsx"), "utf8");

  check(
    "SceneEditPanel declares performers as an initial-state draft",
    /const \[performers, setPerformers, setPerformersInitial\] =\s*\n?\s*useInitialState/.test(
      editPanel
    ),
    "performers is still a plain useState"
  );
  check(
    "SceneEditPanel syncs performers through setPerformersInitial",
    editPanel.includes("setPerformersInitial(scene.performers ?? [])")
  );
  check(
    "SceneEditPanel syncs galleries, groups and studio the same way",
    editPanel.includes("setGalleriesInitial(") &&
      editPanel.includes("setGroupsInitial(") &&
      editPanel.includes("setStudioInitial(")
  );
  check(
    "tagsEdit declares its draft with useInitialState",
    tagsEdit.includes("useInitialState<Tag[]>([])")
  );
  check(
    "tagsEdit syncs srcTags through the initial setter",
    tagsEdit.includes("setTagsInitial(srcTags ?? [])")
  );
  check(
    "no syncing effect writes a draft with the plain setter",
    !/useEffect\(\(\) => \{\s*setPerformers\(/.test(editPanel) &&
      !/useEffect\(\(\) => \{\s*setTags\(/.test(tagsEdit),
    "a bare setState in a syncing effect will clobber the draft again"
  );
  check(
    "the explicit post-save reset path is still reachable",
    editPanel.includes("setPerformers(scene.performers ?? [])") ||
      tagsEdit.includes("resetTagsState"),
    "both reset paths were converted to the dirty-checked setter, which cannot " +
      "overwrite a dirty draft -- the field would never reset after a save"
  );
}

// ---------------------------------------------------------------------------
console.log("\nthe trigger is a 10-second timer that stops on pause");
// If the timer were not gated on playback, "does not reproduce when paused" would
// be unexplained.
{
  const track = readFileSync(
    join(SRC, "components", "ScenePlayer", "track-activity.ts"),
    "utf8"
  );
  const player = readFileSync(
    join(SRC, "components", "ScenePlayer", "ScenePlayer.tsx"),
    "utf8"
  );
  const scene = readFileSync(
    join(SRC, "components", "Scenes", "SceneDetails", "Scene.tsx"),
    "utf8"
  );

  check("activity is sent on a 10-second interval", /sendInterval = 10/.test(track));
  check(
    "the interval is stopped on pause",
    /player\.on\("pause", \(\) => \{\s*this\.stop\(\);/.test(track)
  );
  check("the player sends sceneSaveActivity", /sceneSaveActivity\(/.test(player));
  check(
    "the editor replaces `scene` whenever the query returns",
    /setScene\(data\?\.findScene/.test(scene),
    "if `scene` were stable the drafts would never be re-synced"
  );
}

console.log(
  failures === 0
    ? "\nOK: a dirty draft survives background refetches, and pristine drafts still sync"
    : `\nFAIL: ${failures} check(s)`
);
process.exit(failures === 0 ? 0 : 1);
