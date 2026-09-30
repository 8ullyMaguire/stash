// Verify #7249's CSS fix in a real browser, at real viewport sizes.
//
// WHY A BROWSER: this is a layout bug, and the whole question is which
// declaration WINS the cascade and whether the used height exceeds its
// container. jsdom does not implement the cascade, viewport units, or flex
// overflow, so a jsdom test would prove nothing. (Same lesson as the #7245 DOM
// probe: assert the mechanism in the engine that implements it.)
//
// THE BUG. `.VideoPlayer` is bounded by `max-height: calc(100vh - 4rem)`, but
// its child `.video-wrapper` is sized from viewport WIDTH:
//
//   .VideoPlayer.portrait .video-wrapper { height: 177.78vw; }
//   .video-wrapper                      { height:  56.25vw; overflow: hidden; }
//
// `overflow: hidden` is on the WRAPPER, so it clips its own video -- the
// WRAPPER is not clipped by .VideoPlayer. A flex item taller than its container
// overflows visibly and keeps receiving pointer events, which is the reported
// click-blocking overlay.
//
// TWO THINGS WORTH CHECKING, both of which reading the SCSS does not settle:
//
//   1. SPECIFICITY. `.VideoPlayer.portrait .video-wrapper` is (0,3,0) and
//      `.video-wrapper` inside `@media (min-width: 1200px)` is (0,1,0) --
//      media queries add no specificity. So the portrait rule beats the
//      desktop override, and 177.78vw applies at >=1200px too. That is why the
//      bug shows on a laptop rather than only on a phone.
//   2. Whether LANDSCAPE has the same defect. 56.25vw is a smaller multiplier,
//      but it is the same unbounded-width shape.
//
// This measures used heights at a spread of viewports, with and without the fix.

import { existsSync, readdirSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";

const HERE = dirname(fileURLToPath(import.meta.url));

function findChromium() {
  const cache = join(homedir(), ".cache", "ms-playwright");
  if (existsSync(cache)) {
    const builds = readdirSync(cache)
      .filter((d) => d.startsWith("chromium-"))
      .sort((a, b) => Number(b.split("-")[1]) - Number(a.split("-")[1]));
    for (const b of builds) {
      const c = join(cache, b, "chrome-linux64", "chrome");
      if (existsSync(c)) return c;
    }
  }
  return ["/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome"].find(existsSync);
}

const CHROME = process.env.CHROME_PATH || findChromium();
if (!CHROME) {
  console.log("SKIP: no Chromium; these are cascade and layout facts");
  process.exit(0);
}

// The relevant rules, transcribed. Kept minimal and explicit: the point is to
// measure the cascade, and a real build would need the whole app.
const CSS = `
  :root { --menu: 4rem; }
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { background: #222; }
  .VideoPlayer {
    display: flex;
    flex-direction: column;
    max-height: calc(100vh - var(--menu));
    padding-bottom: 0.25rem;
  }
  @media (min-width: 1200px) {
    .VideoPlayer { height: 100vh; }
  }
  .VideoPlayer.portrait .video-wrapper { height: 177.78vw; }
  .video-wrapper {
    height: 56.25vw;
    overflow: hidden;
    position: relative;
    width: 100%;
  }
  @media (min-width: 1200px) {
    .video-wrapper { height: 100%; }
  }
`;

// The transcription above did NOT reproduce the report: flex-shrink (default 1)
// pulled the wrapper back inside the container, so the cap never binds. The
// report's 3413px on a 1920px screen requires the item NOT to shrink. These
// variants make the shrink behaviour explicit so the difference is measurable.
const NOSHRINK_CSS = CSS.replace(
  ".VideoPlayer.portrait .video-wrapper { height: 177.78vw; }",
  ".VideoPlayer.portrait .video-wrapper { height: 177.78vw; flex-shrink: 0; }"
);

const NOSHRINK_FIXED_CSS = NOSHRINK_CSS.replace(
  ".VideoPlayer.portrait .video-wrapper { height: 177.78vw; flex-shrink: 0; }",
  `.VideoPlayer.portrait .video-wrapper {
    height: 177.78vw;
    flex-shrink: 0;
    max-height: calc(100vh - var(--menu));
  }`
);

const FIXED_CSS = CSS.replace(
  ".VideoPlayer.portrait .video-wrapper { height: 177.78vw; }",
  `.VideoPlayer.portrait .video-wrapper {
    height: 177.78vw;
    max-height: calc(100vh - var(--menu));
  }`
);

function page(css, portrait, landscapeCap) {
  return `<!doctype html><html><head><style>${css}</style></head><body>
<div class="VideoPlayer${portrait ? " portrait" : ""}" id="player">
  <div class="video-wrapper" id="wrap"></div>
  <div style="height:80px;background:#444" id="flowBelow">flow sibling</div>
  <!-- The report names scene tabs / rating buttons: fixed page chrome, not a
       flow sibling. A flow sibling stacks BELOW the wrapper and never overlaps
       it, so measuring one reports overflow=0 no matter how tall the wrapper
       gets -- which is what the first run did, with the wrapper at 3413px. -->
  <div id="below" style="position:fixed;left:20px;bottom:20px;width:200px;
       height:48px;background:#0a0">fixed button</div>
</div>
</body></html>`;
}

let failures = 0;
const check = (name, cond, detail = "") => {
  if (cond) console.log(`  ok   ${name}`);
  else {
    console.log(`  FAIL ${name}${detail ? ` -- ${detail}` : ""}`);
    failures++;
  }
};

const proc = spawn(
  CHROME,
  ["--headless=new", "--disable-gpu", "--no-sandbox", "--remote-debugging-port=0",
   "--user-data-dir=/tmp/portrait-cap-probe", "about:blank"],
  { stdio: ["ignore", "pipe", "pipe"] }
);

const wsUrl = await new Promise((resolve, reject) => {
  let buf = "";
  const t = setTimeout(() => reject(new Error("no debug URL")), 20000);
  proc.stderr.on("data", (d) => {
    buf += d.toString();
    const m = buf.match(/ws:\/\/[^\s]+/);
    if (m) {
      clearTimeout(t);
      resolve(m[0]);
    }
  });
  proc.on("exit", (c) => reject(new Error(`chrome exited ${c}`)));
}).catch((e) => {
  console.log(`SKIP: ${e.message}`);
  process.exit(0);
});

const ws = new WebSocket(wsUrl);
await new Promise((res, rej) => {
  ws.onopen = res;
  ws.onerror = rej;
});
let nextId = 1;
const pending = new Map();
ws.onmessage = (ev) => {
  const m = JSON.parse(ev.data);
  if (m.id && pending.has(m.id)) {
    const { resolve, reject } = pending.get(m.id);
    pending.delete(m.id);
    m.error ? reject(new Error(JSON.stringify(m.error))) : resolve(m.result);
  }
};
const send = (method, params = {}, sessionId) =>
  new Promise((resolve, reject) => {
    const id = nextId++;
    pending.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params, sessionId }));
  });

const { targetId } = await send("Target.createTarget", { url: "about:blank" });
const { sessionId } = await send("Target.attachToTarget", { targetId, flatten: true });
await send("Page.enable", {}, sessionId);
await send("Runtime.enable", {}, sessionId);

const evaluate = async (expression) => {
  const r = await send("Runtime.evaluate", { expression, returnByValue: true }, sessionId);
  if (r.exceptionDetails) throw new Error(r.exceptionDetails.text || "eval failed");
  return r.result.value;
};

async function measure(css, portrait, w, h) {
  await send("Emulation.setDeviceMetricsOverride", { width: w, height: h, deviceScaleFactor: 1, mobile: false }, sessionId);
  const html = page(css, portrait);
  await send("Page.navigate", { url: "data:text/html," + encodeURIComponent(html) }, sessionId);
  await new Promise((r) => setTimeout(r, 250));
  return evaluate(`(() => {
    const p = document.getElementById("player").getBoundingClientRect();
    const vw = document.documentElement.clientWidth;
    const w = document.getElementById("wrap").getBoundingClientRect();
    const below = document.getElementById("below");
    const br = below.getBoundingClientRect();
    // The click blocker: does the wrapper's overflow region COVER fixed chrome?
    // Two separate questions, and conflating them is what hid the bug:
    //   (a) geometric overlap -- wrapper.bottom extends past the button's top
    //   (b) is that region hit-testable -- elementFromPoint at the button
    // Both must hold for a real click to be eaten. A high z-index on the button
    // would beat the wrapper, so the chrome is z-index:auto here, as it is in
    // the app -- the wrapper is later in the DOM and paints over it.
    const overTop = Math.max(0, w.bottom - br.top);
    const overBottom = Math.max(0, br.bottom - w.top);
    const overlap = Math.max(0, Math.min(w.bottom, br.bottom) - Math.max(w.top, br.top));
    const hit = overlap > 0
      ? (() => {
          const el = document.elementFromPoint(br.left + br.width / 2, br.top + 4);
          return el ? el.id || el.className || el.tagName : "none";
        })()
      : "no-overlap";
    return {
      vw, innerWidth: window.innerWidth,
      playerH: p.height, wrapH: w.height, wrapBottom: w.bottom,
      belowTop: br.top, overflowPx: overTop, overlapPx: overlap, hitTarget: hit,
      computed: getComputedStyle(document.getElementById("wrap")).height,
    };
  })()`);
}

console.log(`using ${CHROME}`);
console.log("\nWHETHER the overflow reproduces at all");
{
  // Faithful transcription of the SCSS, at the reporter's own screen size.
  const shrink = await measure(CSS, true, 1920, 1080);
  console.log(`  shrinkable flex item (default): wrapper=${Math.round(shrink.wrapH)} player=${Math.round(shrink.playerH)} overlap=${Math.round(shrink.overlapPx)}px`);
  check(
    "flexbox SHRINKS the wrapper back inside the container, so the report does NOT reproduce here",
    shrink.wrapH <= shrink.playerH,
    `wrapper=${Math.round(shrink.wrapH)} > player=${Math.round(shrink.playerH)}`
  );

  // The report's 3413px = 177.78vw of 1920, i.e. no shrinking at all. So the
  // reported state requires the item not to shrink.
  const noshrink = await measure(NOSHRINK_CSS, true, 1920, 1080);
  console.log(`  flex-shrink: 0:               wrapper=${Math.round(noshrink.wrapH)} overlap=${Math.round(noshrink.overlapPx)}px hit=${noshrink.hitTarget}`);
  check(
    "with flex-shrink: 0 the wrapper DOES reach the reported 3413px",
    Math.abs(noshrink.wrapH - 1.7778 * 1920) < 2,
    `wrapper=${Math.round(noshrink.wrapH)} vs 177.78vw of 1920 = ${Math.round(1.7778 * 1920)}`
  );
  check(
    "and its overflow region COVERS the fixed chrome -- the reported click blocker",
    noshrink.overlapPx > 20,
    `overlap=${Math.round(noshrink.overlapPx)}px`
  );
  // Whether a CLICK is eaten depends on paint order, and this fixture cannot
  // settle it: the button is a later sibling here, so it wins. In the app the
  // player is the LAST child of the row (SceneLoader renders <ScenePage> then
  // the player container), so the wrapper paints over the tabs -- verified by
  // reading Scene.tsx:1045-1079. Stated, not asserted here.

  // And the fix, in exactly that state.
  const fixed = await measure(NOSHRINK_FIXED_CSS, true, 1920, 1080);
  console.log(`  flex-shrink: 0 + max-height:  wrapper=${Math.round(fixed.wrapH)} overlap=${Math.round(fixed.overlapPx)}px hit=${fixed.hitTarget}`);
  check("the cap holds in the no-shrink state", fixed.wrapH <= 1080 - 64 + 1, `wrapper=${Math.round(fixed.wrapH)}`);
  check("and the covered UI is clickable again", fixed.hitTarget === "below", `hit=${fixed.hitTarget}`);
}

console.log("\nAFTER the fix, portrait: the cap holds at every size");
{
  for (const [w, h] of [[1440, 800], [1920, 600], [2560, 400], [1200, 900], [390, 844]]) {
    const r = await measure(FIXED_CSS, true, w, h);
    const cap = h - 64; // 100vh - 4rem
    const ok = r.wrapH <= cap + 1;
    console.log(`  asked ${w}x${h}, vw=${r.vw}  wrapper=${Math.round(r.wrapH)} cap=${cap} overlap=${Math.round(r.overlapPx)}px hit=${r.hitTarget}`);
    check(`  ${w}x${h}: laid out at the requested width`, r.vw === w, `vw=${r.vw}`);
    check(`  ${w}x${h}: wrapper within the cap`, ok, `wrapper=${Math.round(r.wrapH)} > cap=${cap}`);
    check(
    `  ${w}x${h}: fixed chrome not covered by the wrapper`,
    r.overlapPx <= 0 || r.hitTarget === "below",
    `hit=${r.hitTarget} overlap=${Math.round(r.overlapPx)}`
  );
  }
}

console.log("\nlandscape: the cap is INERT, which is why adding it is safe");
{
  const r = await measure(FIXED_CSS, false, 1920, 600);
  console.log(`  1920x600 landscape  wrapper=${Math.round(r.wrapH)} player=${Math.round(r.playerH)} overlap=${Math.round(r.overlapPx)}px`);
  check(
    "the landscape wrapper stays inside its container -- flexbox shrinks it",
    r.wrapH <= r.playerH,
    `wrapper=${Math.round(r.wrapH)} > player=${Math.round(r.playerH)}`
  );
  check("so the base rule needed no cap either; my earlier claim it overflowed was WRONG", r.overlapPx <= 0);

  // The cap is inert wherever flexbox already shrinks, and binding wherever it
  // does not. max-height only ever CLAMPS DOWN, so it cannot grow the box and
  // cannot regress a correctly-sized video.
  const noshrink = await measure(NOSHRINK_CSS, false, 1920, 600);
  const capped = await measure(NOSHRINK_FIXED_CSS, false, 1920, 600);
  console.log(`  landscape, flex-shrink: 0  uncapped=${Math.round(noshrink.wrapH)} capped=${Math.round(capped.wrapH)}`);
  check(
    "even unsuppressed, the landscape box is far shorter than the portrait one",
    noshrink.wrapH < 1080,
    `wrapper=${Math.round(noshrink.wrapH)} vs portrait 3413`
  );
  check(
    "max-height only ever clamps DOWN, never up",
    capped.wrapH <= noshrink.wrapH + 1,
    `capped=${Math.round(capped.wrapH)} > uncapped=${Math.round(noshrink.wrapH)}`
  );
}

console.log("\nCAN THE REPORT BE REPRODUCED FROM THE SCSS AS WRITTEN?");
{
  // Tried and NOT reproduced, in every state the real stylesheet produces:
  //   >=1200px  container height:100vh (definite) -> flex-shrink pulls the item in
  //   <1200px   container height auto, but max-height clamps the container
  //             rather than letting it grow, and overflow:hidden on the wrapper
  //             makes min-height:auto resolve to 0, so the item still shrinks
  // In both, `min-height` -- the automatic minimum size of a flex item in a
  // column container -- is zero BECAUSE the wrapper sets overflow:hidden. That
  // is the property doing the work, and it is easy to miss by reading only the
  // height declarations.
  for (const [w, h] of [[900, 700], [1100, 800], [800, 600], [1440, 800], [1920, 1080]]) {
    const pre = await measure(CSS, true, w, h);
    check(`  ${w}x${h}: no overflow from the stylesheet as written`, pre.wrapH <= pre.playerH + 1, `wrapper=${Math.round(pre.wrapH)} player=${Math.round(pre.playerH)}`);
  }

  // The report's figure is 3413px on a 1920px screen. That is exactly
  // 1.7778 * 1920 -- arithmetic on the declared value, not a rendered
  // measurement. It is reproduced ONLY by suppressing the shrink that the real
  // stylesheet does not suppress:
  const fs0 = await measure(NOSHRINK_CSS, true, 1920, 1080);
  check("the 3413px figure appears only with flex-shrink: 0, which the SCSS does not set", Math.abs(fs0.wrapH - 1.7778 * 1920) < 2, `wrapper=${Math.round(fs0.wrapH)}`);
  console.log("  => the figure is 1.7778 * 1920, reproducible only under an assumption the stylesheet does not make");

  // What IS established, and what the fix therefore rests on:
  //  - max-height only ever CLAMPS DOWN, so it cannot grow the box and cannot
  //    regress a correctly sized video;
  //  - it binds the moment anything prevents the shrink (a taller box, a changed
  //    container, a browser that resolves the minimum size differently), which is
  //    the case in the reporter's environment and the case I cannot reproduce
  //    here.
  for (const [w, h] of [[1920, 600], [2560, 400]]) {
    const capped = await measure(FIXED_CSS, true, w, h);
    check(`  ${w}x${h}: the cap is inert where flexbox already fits the box`, capped.wrapH <= capped.playerH + 1, `wrapper=${Math.round(capped.wrapH)}`);
  }
}

ws.close();
proc.kill();
console.log(
  failures === 0
    ? "\nOK: the cap is safe (clamps down only) and inert where flexbox already fits;\n" +
      "    the reported 3413px overflow could NOT be reproduced from the SCSS as written"
    : `\nFAIL: ${failures} check(s)`
);
process.exit(failures === 0 ? 0 : 1);
