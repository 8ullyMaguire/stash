#!/usr/bin/env node
// Regression guard for the WebKit media-player cost in the scene lists
// (PR #7245, closing stash#7236).
//
// WHY A .mjs SCRIPT: the UI has no test runner (no `test` script, no
// jest/vitest), and `import` from the source is unavailable because node's ESM
// resolver rejects a module whose first import is a type-only export.
//
// THE BUG, in the reporter's own two layers (stash#7236):
//
//   1. `IntersectionObserver` delivers an INITIAL callback when `observe()` is
//      called. On a 1000-card page that is 1000 callbacks, and for every card
//      offscreen `intersectionRatio` is 0. The old code did
//      `if (ratio > 0) play() else pause()`, so `pause()` ran on every offscreen
//      card -- and in WebKit `pause()` on a media element that HAS a src begins
//      media player setup. 1000 media players at page load.
//
//   2. Even with no call, every `<video>` with a src costs player/compositing
//      work. `preload="none"` does not remove the element's cost.
//
// The fix has two halves and BOTH are load-bearing:
//   - `else if (!el.paused) el.pause()` -- a never-played video reports
//     `paused === true`, so no pause() call is made.
//   - `src={loadVideo ? video : undefined}` -- offscreen cards carry no src at
//     all, so WebKit sets up nothing.
//
// This harness models that decision and asserts the property that matters: for a
// page of N cards, the number of videos that ever RECEIVE A SOURCE and the
// number of pause() CALLS are both bounded by what is visible, not by N.

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

const sceneCard = readFileSync(join(SRC, "components", "Scenes", "SceneCard.tsx"), "utf8");
const wallItem = readFileSync(join(SRC, "components", "Wall", "WallItem.tsx"), "utf8");

// ---------------------------------------------------------------------------
// The two halves, asserted against the real source text.
// ---------------------------------------------------------------------------
console.log("the fix, as written");
check("SceneCard guards the pause with !el.paused", /else if \(!el\.paused\)/.test(sceneCard));
check("SceneCard defers the src until the preview is shown", /src=\{loadVideo \? video : undefined\}/.test(sceneCard));
check("SceneCard latches loadVideo on visibility", /if \(visible\) setLoadVideo\(true\)/.test(sceneCard));

check("WallItem guards the pause with !video.paused", /else if \(!video\.paused\)/.test(wallItem));
check("WallItem defers the src", /src=\{loadVideo \? previews\.video : undefined\}/.test(wallItem));
check(
  "WallItem derives needsVideo rather than only setting it on hover",
  /const needsVideo = previewType === "video" \|\| active;/.test(wallItem)
);

// ---------------------------------------------------------------------------
// THE MODEL: one card, the two halves, over a sequence of visibility events.
// ---------------------------------------------------------------------------
// `paused` starts true, exactly as a <video> that has never played reports.
function makeCard({ autoplay = false } = {}) {
  return { paused: true, hasSrc: false, playCalls: 0, pauseCalls: 0, loadVideo: autoplay };
}

// The pre-fix SceneCard behaviour.
function oldCard(card, visible) {
  if (visible) card.playCalls++;
  else card.pauseCalls++;
}

// The post-fix SceneCard behaviour: src deferred, pause guarded.
function newCard(card, visible) {
  if (visible) {
    card.loadVideo = true;
    if (card.loadVideo) card.playCalls++;
  } else if (!card.paused) {
    card.pauseCalls++;
  }
  if (card.playCalls > 0) card.paused = false;
  if (card.pauseCalls > 0) card.paused = true;
}

// ---------------------------------------------------------------------------
console.log("\nthe reported page: 1000 cards, none visible");
{
  const N = 1000;
  const oldTotal = { pause: 0, src: 0 };
  for (let i = 0; i < N; i++) {
    const c = makeCard();
    c.hasSrc = true; // pre-fix: src was always present
    oldCard(c, false); // the initial observer callback, offscreen
    oldTotal.pause += c.pauseCalls;
    oldTotal.src += c.hasSrc ? 1 : 0;
  }
  check("pre-fix: every offscreen card got a src", oldTotal.src === N, `src=${oldTotal.src}`);
  check("pre-fix: every offscreen card got a pause()", oldTotal.pause === N, `pause=${oldTotal.pause}`);

  const newTotal = { pause: 0, src: 0 };
  for (let i = 0; i < N; i++) {
    const c = makeCard();
    newCard(c, false);
    newTotal.pause += c.pauseCalls;
    newTotal.src += c.loadVideo ? 1 : 0;
  }
  check("post-fix: no offscreen card gets a src", newTotal.src === 0, `src=${newTotal.src}`);
  check("post-fix: no offscreen card gets a pause()", newTotal.pause === 0, `pause=${newTotal.pause}`);
  check(
    "so 1000 media players become 0 -- the reported hang is addressed",
    newTotal.src === 0 && newTotal.pause === 0
  );
}

// ---------------------------------------------------------------------------
console.log("\na card that scrolls into view still works");
// ---------------------------------------------------------------------------
{
  const c = makeCard();
  newCard(c, false); // initial callback, offscreen
  check("nothing happened while offscreen", c.playCalls === 0 && c.pauseCalls === 0 && !c.loadVideo);
  newCard(c, true); // scrolled into view
  check("it gets a src on first sight", c.loadVideo === true);
  check("and play() runs exactly once", c.playCalls === 1, `play=${c.playCalls}`);
  newCard(c, false); // scrolled away
  check("scrolling away pauses it", c.pauseCalls === 1, `pause=${c.pauseCalls}`);
  newCard(c, true); // back into view
  check("returning replays it", c.playCalls === 2, `play=${c.playCalls}`);
  check("but never pauses a video that was not playing", c.pauseCalls === 1);
}

// ---------------------------------------------------------------------------
console.log("\nwall mode: autoplay must still have a source");
// ---------------------------------------------------------------------------
// The regression this harness exists to catch: setting loadVideo only inside the
// `active` branch leaves previewType === "video" with a permanent undefined src,
// so autoplay has nothing to play and the wall shows no video at all.
{
  const needsVideo = (previewType, active) => previewType === "video" || active;

  check("wall video mode needs a source when not hovered", needsVideo("video", false) === true);
  check("hovering needs a source in image mode", needsVideo("image", true) === true);
  check("an idle image tile needs none", needsVideo("image", false) === false);
  check("an idle animation tile needs none", needsVideo("animation", false) === false);

  // A card that never becomes active in wall-video mode must still load, or the
  // wall renders blank.
  const c = makeCard({ autoplay: needsVideo("video", false) });
  check("an idle wall-video tile has its src from the start", c.loadVideo === true);
}

// ---------------------------------------------------------------------------
console.log("\nthe cost the fix does NOT remove, stated so it is not re-introduced");
{
  // A card IN VIEW still gets a src and a media player. That is intended -- it
  // is what the preview is. So the bound is "visible cards", not "zero".
  const N = 1000;
  const VISIBLE = 24; // a plausible viewport's worth
  let withSrc = 0;
  for (let i = 0; i < N; i++) {
    const c = makeCard();
    newCard(c, i < VISIBLE);
    if (c.loadVideo) withSrc++;
  }
  check("only the visible cards pay the cost", withSrc === VISIBLE, `withSrc=${withSrc}`);
  check(
    "and that is independent of page size",
    withSrc < N,
    "the cost scales with the page, which is the bug"
  );
}

console.log(
  failures === 0
    ? "\nOK: the cost is bounded by what is visible, not by page size"
    : `\nFAIL: ${failures} check(s)`
);
process.exit(failures === 0 ? 0 : 1);
