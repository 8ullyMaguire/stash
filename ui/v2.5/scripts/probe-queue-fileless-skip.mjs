// stash#6814: the automatic playlist gets stuck on a scene with no file.
//
// WHY A .mjs SCRIPT: the UI has no test runner (no `test` script, no jest/vitest).
// Same precedent as the other harnesses in scripts/.
//
// THE BUG. `queueNext(autoPlay)` advanced to `currentQueueIndex + 1` and loaded
// it. A scene with no file has no streams, so ScenePlayer mounts, the video never
// plays, `onComplete` never fires, and the queue never advances. The reporter's
// description matches exactly: "the next scene's info but with the previous
// scene's video in a stopped state ... no seek bar or any other control", and
// seeking to the end with the keyboard unblocked it, because that is the only way
// to make the unplayable scene "finish".
//
// THE FIX (upstream PR #7227, still open) makes `queueNext` take a
// `skipUnplayable` flag, set only from `onComplete` -- i.e. when advancing without
// the user choosing the destination. A manually-advanced queue still stops on the
// fileless scene and shows the "This scene has no file." placeholder instead,
// because a user pressing "next" asked for that scene specifically.
//
// WHAT IS WORTH TESTING, and what is not.
//
// The index arithmetic is the whole fix, and it is pure, so it is extracted and
// driven directly rather than through a rendered React tree. What is NOT worth
// testing is the placeholder's styling, or that the string is in the locale file;
// those are asserted by reading the source, since a rendered assertion would need
// a DOM this tree does not have.
//
// The interesting cases are the boundary ones, because the original code's guard
// was `currentQueueIndex === queueScenes.length - 1` -- which is exactly the guard
// that stops working once a skip can advance past the end of the loaded page.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

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
// The real predicate, extracted from the shipped source.
// ---------------------------------------------------------------------------
const queueSource = readFileSync(join(SRC, "models", "sceneQueue.ts"), "utf8");

function loadIsPlayable() {
  const start = queueSource.indexOf("export function isPlayable");
  if (start < 0) {
    throw new Error("isPlayable not found in src/models/sceneQueue.ts");
  }
  const end = queueSource.indexOf("}", start) + 1;
  const body = queueSource
    .slice(start, end)
    .replace("export function isPlayable(s: IObjectWithFiles)", "function isPlayable(s)");
  // biome-ignore lint/security/noGlobalEval: evaluating the shipped source is the point
  return new Function(`${body}; return isPlayable;`)();
}

const isPlayable = loadIsPlayable();

// The index walk, mirroring queueNext's first half. Returns the index the queue
// should load, or -1 meaning "we ran off the end of the loaded page".
function nextIndexFor(currentQueueIndex, queueScenes, skipUnplayable) {
  let nextIndex = currentQueueIndex + 1;
  while (
    skipUnplayable &&
    nextIndex < queueScenes.length &&
    !isPlayable(queueScenes[nextIndex])
  ) {
    nextIndex++;
  }
  return nextIndex < queueScenes.length ? nextIndex : -1;
}

const playable = (id) => ({ id, files: [{ id: "f", path: "a.mp4" }] });
const fileless = (id) => ({ id, files: [] });

// ---------------------------------------------------------------------------
console.log("premise: isPlayable matches what the query can return");
{
  check("a scene with a file is playable", isPlayable(playable("a")) === true);
  check("a scene with no files is not playable", isPlayable(fileless("b")) === false);
  // `files` is non-nullable in FindScenesQuery, but the type is
  // IObjectWithFiles where it is optional, and a defensive `?.` that vanished in a
  // refactor would turn a missing field into a crash mid-queue.
  check(
    "a scene with files undefined is not playable rather than throwing",
    isPlayable({ id: "c" }) === false
  );
  check(
    "a scene with files null is not playable rather than throwing",
    isPlayable({ id: "d", files: null }) === false
  );
}

// ---------------------------------------------------------------------------
console.log("\nthe reported case: a fileless scene mid-queue does not stall it");
{
  const queue = [playable("a"), fileless("b"), playable("c")];

  // What the OLD code did: advance to index 1, the fileless scene, and stop
  // forever because its video never plays. (Scoped to the block -- the first
  // version referenced currentQueueIndex outside it and threw.)
  const currentQueueIndex = 0;
  const old = currentQueueIndex + 1;
  check(
    "the old behaviour loaded the fileless scene and stopped there",
    old === 1 && !isPlayable(queue[old]),
    `old landed on index ${old}`
  );

  const next = nextIndexFor(0, queue, true);
  check(
    "the fix steps over it and lands on the next playable scene",
    next === 2,
    `landed on index ${next}`
  );
}

// ---------------------------------------------------------------------------
console.log("\nskipping is opt-in, so a manual 'next' still shows the scene");
{
  const queue = [playable("a"), fileless("b"), playable("c")];
  const next = nextIndexFor(0, queue, false);
  check(
    "without skipUnplayable the fileless scene is still loaded",
    next === 1,
    `landed on index ${next}`
  );
  check(
    "so the placeholder is what the user sees, not a silent skip",
    !isPlayable(queue[next])
  );
}

// ---------------------------------------------------------------------------
console.log("\nconsecutive fileless scenes are all skipped");
{
  const queue = [
    playable("a"),
    fileless("b"),
    fileless("c"),
    fileless("d"),
    playable("e"),
  ];
  check(
    "a run of three fileless scenes is stepped over",
    nextIndexFor(0, queue, true) === 4,
    `landed on ${nextIndexFor(0, queue, true)}`
  );
}

// ---------------------------------------------------------------------------
console.log("\nBOUNDARY: the skip runs off the end of the loaded page");
// This is where the original guard broke. The old code required
// `currentQueueIndex === queueScenes.length - 1` before it would ask for more
// scenes, so a skip that advanced PAST the last index fell into the else-branch
// and did nothing -- the queue sat still even though more scenes existed.
{
  // The skip has to be able to run PAST the last loaded index, which needs an
  // unplayable scene there and a playable one before it.
  const queue = [playable("a"), fileless("b"), fileless("c")];
  const currentQueueIndex = 1;

  const next = nextIndexFor(currentQueueIndex, queue, true);
  check(
    "running off the end returns -1 so the caller loads more",
    next === -1,
    `got ${next}, which would load queue[-1] or silently do nothing`
  );

  // And the old guard, for the record. The queue above is length 3 and the
  // current index is 1, so `currentQueueIndex === queueScenes.length - 1` is
  // FALSE -- the old code would have fallen into its else-branch and done
  // nothing at all rather than paging. My first version of this case used a
  // length-3 queue at index 2, where the guard DOES hold, so the assertion was
  // wrong rather than the code -- worth fixing, since an assertion that inverts
  // reads as a code defect.
  const oldGuardHeld = currentQueueIndex === queue.length - 1;
  check(
    "the old `currentQueueIndex === length - 1` guard does NOT hold here",
    oldGuardHeld === false,
    "if this held, the skip could never have advanced past the end"
  );
  check(
    "so the old code would have silently done nothing, not paged",
    oldGuardHeld === false && next === -1,
    "the scenario that motivated removing the guard"
  );
}

// ---------------------------------------------------------------------------
console.log("\nthe whole page being fileless also asks for more");
{
  const queue = [fileless("a"), fileless("b")];
  check(
    "an all-fileless page returns -1 rather than looping on itself",
    nextIndexFor(0, queue, true) === -1
  );
}

// ---------------------------------------------------------------------------
console.log("\nthe last scene being playable is still advanced onto");
{
  const queue = [fileless("a"), playable("b")];
  check("the walk stops at a playable scene", nextIndexFor(0, queue, true) === 1);
}

// ---------------------------------------------------------------------------
console.log("\nthe shipped code actually contains the fix");
// A harness that passes against reverted source proves nothing.
{
  const scene = readFileSync(
    join(SRC, "components", "Scenes", "SceneDetails", "Scene.tsx"),
    "utf8"
  );

  check(
    "queueNext takes a skipUnplayable flag",
    /async function queueNext\(autoPlay: boolean, skipUnplayable = false\)/.test(scene)
  );
  check(
    "onComplete -- the automatic advance -- passes it as true",
    /function onComplete\(\)[\s\S]{0,200}queueNext\(true, true\)/.test(scene)
  );
  check(
    "the manual onNext does NOT skip, so a chosen scene is still shown",
    /onNext=\{\(\) => queueNext\(true\)\}/.test(scene)
  );
  check(
    "the render shows a placeholder instead of a dead player",
    /isPlayable\(scene\) \? \(/.test(scene) && scene.includes("scene_has_no_file")
  );
  check(
    "the old length-1 guard is gone",
    !scene.includes("currentQueueIndex === queueScenes.length - 1 && queueHasMoreScenes"),
    "the guard that silently did nothing after a skip is still present"
  );

  // The player no longer holds a stale seek callback across a scene change.
  const player = readFileSync(
    join(SRC, "components", "ScenePlayer", "ScenePlayer.tsx"),
    "utf8"
  );
  check(
    "the player deregisters its seek callback on unmount",
    /return \(\) => sendSetTimestamp\(\(\) => \{\}\)/.test(player),
    "a stale callback can seek a disposed player"
  );
}

// ---------------------------------------------------------------------------
console.log("\nthe queue query actually selects `files`, or isPlayable sees nothing");
{
  const gql = readFileSync(join(SRC, "core", "generated-graphql.ts"), "utf8");
  const i = gql.indexOf("FindScenesQuery = ");
  check("FindScenesQuery exists in the generated client", i > 0);
  const frag = gql.slice(i, i + 2600);
  check(
    "it selects `files`",
    /files: Array</.test(frag),
    "isPlayable would always be false without it, and every queue would be skipped"
  );
  check(
    "`files` is non-nullable there, so an empty array is the only no-file case",
    /files: Array<\{ __typename\?/.test(frag)
  );
}

console.log(
  failures === 0
    ? "\nOK: an automatic advance steps over fileless scenes, a manual one does not"
    : `\nFAIL: ${failures} check(s)`
);
process.exit(failures === 0 ? 0 : 1);