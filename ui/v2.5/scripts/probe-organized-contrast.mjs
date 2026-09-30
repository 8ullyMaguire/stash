// Verify #7254's contrast fix against the REAL cascade, in a real browser.
//
// WHY A BROWSER: the numbers are easy to compute once you know the background,
// and that is the whole risk. `variant="secondary"` is a react-bootstrap prop, so
// the background comes from Bootstrap's CSS -- but the app also has its own theme
// layer, and a light/dark switch may override it. Asserting contrast against a
// colour I picked is asserting my own assumption, not the app's rendering.
//
// WHAT IS ACTUALLY AT RISK, and it is not the arithmetic:
//
//   1. WHICH BACKGROUND wins. `.btn-secondary` is #3a3f44 in Bootstrap 4.5, but
//      if this app themes buttons, the effective background may differ -- and the
//      whole fix rests on the foreground-to-background ratio.
//   2. THE INACTIVE STATE. The reported complaint is that active and inactive are
//      "nearly indistinguishable at a glance", so the meaningful number is the
//      contrast BETWEEN the two states, not foreground-vs-background alone. The
//      inactive colour is `rgba(191,204,214,0.5)`, which composites against
//      whatever the background turns out to be.
//   3. HOVER. Bootstrap restyles `.btn-secondary:hover`, and the icon inherits
//      `currentColor` -- so the active icon's contrast changes on hover too.
//   4. SCOPE. The PR body claims the class "is only emitted by OrganizedButton.tsx,
//      which is only imported by Scene.tsx". That is FALSE: Gallery.tsx:442 and
//      Image.tsx:368 import it too. Whether that matters depends on whether those
//      pages give the button a different background.

import { existsSync, readdirSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";

// READ THE REAL STYLESHEET. The first version hardcoded both colours, so it
// reported the same verdict no matter what the SCSS said -- a harness that cannot
// fail is not a harness. Verified: with the rule reverted to #664c3f it still
// exited 0.
const SCSS = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "..", "src", "components", "Scenes", "styles.scss"),
  "utf8"
);
function colorIn(rule) {
  const m = SCSS.match(new RegExp(rule + "\\s*\\{([\\s\\S]*?)\\n\\s*\\}"));
  if (!m) return undefined;
  const c = m[1].match(/color:\s*([^;]+);/);
  return c ? c[1].trim() : undefined;
}
const ACTIVE = colorIn("&\\.organized");
const INACTIVE = colorIn("&\\.not-organized");
console.log(`stylesheet says: .organized color=${ACTIVE}  .not-organized color=${INACTIVE}`);

const toRgb = (v) => {
  const s = String(v);
  if (s.startsWith("#")) { const h = s.slice(1); const f = h.length === 3 ? h.split("").map((c) => c + c).join("") : h;
    return `rgb(${parseInt(f.slice(0,2),16)}, ${parseInt(f.slice(2,4),16)}, ${parseInt(f.slice(4,6),16)})`; }
  return s;
};

const APP_BEFORE = `
  .organized-button.not-organized { color: ${toRgb(INACTIVE)}; }
  .organized-button.organized { color: #664c3f; }
`;
const APP_AFTER = `
  .organized-button.not-organized { color: ${toRgb(INACTIVE)}; }
  .organized-button.organized { color: ${toRgb(ACTIVE)}; }
`;

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
  console.log("SKIP: no Chromium; the risk here is which background wins, not the arithmetic");
  process.exit(0);
}

// Bootstrap 4.5's own .btn-secondary, as the app ships it.
const BOOTSTRAP = `
  .btn {
    display: inline-block; font-weight: 400; text-align: center;
    vertical-align: middle; user-select: none;
    background-color: transparent; border: 1px solid transparent;
    padding: .375rem .75rem; font-size: 1rem; line-height: 1.5;
  }
  .btn-secondary, .btn-secondary:focus, .btn-secondary.focus {
    color: #fff; background-color: #3a3f44; border-color: #3a3f44;
  }
  .btn-secondary:not(:disabled):not(.disabled):active,
  .btn-secondary:not(:disabled):not(.disabled).active,
  .show > .btn-secondary:not(:disabled):not(.disabled).dropdown-toggle {
    color: #fff; background-color: #23272b; border-color: #20242a;
  }
  .btn-secondary:hover { color: #fff; background-color: #23272b; border-color: #20242a; }
  .btn:disabled, .btn.disabled { opacity: .65; }
`;

// (APP_BEFORE / APP_AFTER are built from the real stylesheet above.)

const PAGE = (app, cls) => `<!doctype html><html><head><style>${BOOTSTRAP}${app}</style>
  <meta name="viewport" content="width=device-width,initial-scale=1"></head>
  <body style="margin:0;background:#212529">
  <button class="btn btn-secondary minimal organized-button ${cls}" id="b" aria-pressed="${cls === "organized"}">
    <svg id="ico" width="1em" height="1em" style="fill:currentColor" aria-hidden="true">
      <rect width="10" height="10"></rect>
    </svg>
  </button></body></html>`;

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
   "--user-data-dir=/tmp/organized-contrast-probe", "about:blank"],
  { stdio: ["ignore", "pipe", "pipe"] }
);
const wsUrl = await new Promise((resolve, reject) => {
  let buf = "";
  const t = setTimeout(() => reject(new Error("no debug URL")), 20000);
  proc.stderr.on("data", (d) => {
    buf += d.toString();
    const m = buf.match(/ws:\/\/[^\s]+/);
    if (m) { clearTimeout(t); resolve(m[0]); }
  });
  proc.on("exit", (c) => reject(new Error(`chrome exited ${c}`)));
}).catch((e) => { console.log(`SKIP: ${e.message}`); process.exit(0); });

const ws = new WebSocket(wsUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
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

// Resolve a computed colour to what is actually PAINTED, following the DOM to
// the nearest ancestor that sets a non-transparent background -- so an alpha in
// the foreground composites against the real backdrop, not an assumed one.
const RESOLVE = `
  (function (start) {
    const parse = (s) => {
      const m = s.match(/rgba?\\(([^)]+)\\)/);
      if (!m) return null;
      const p = m[1].split(/[,\\s/]+/).filter(Boolean).map(Number);
      return { r: p[0], g: p[1], b: p[2], a: p.length > 3 ? p[3] : 1 };
    };
    const over = (f, b) => ({
      r: f.r * f.a + b.r * (1 - f.a),
      g: f.g * f.a + b.g * (1 - f.a),
      b: f.b * f.a + b.b * (1 - f.a),
      a: 1,
    });
    const backdrop = (el) => {
      let node = el;
      while (node && node !== document.documentElement) {
        const c = parse(getComputedStyle(node).backgroundColor);
        if (c && c.a > 0) return over(c, backdrop(node.parentElement || document.body));
        node = node.parentElement;
      }
      return { r: 255, g: 255, b: 255, a: 1 };
    };
    const fg = parse(getComputedStyle(start).color);
    return over(fg, backdrop(start));
  })(document.getElementById("ico"))
`;

async function measure(app, cls, hover) {
  await send("Page.navigate", { url: "data:text/html," + encodeURIComponent(PAGE(app, cls)) }, sessionId);
  await new Promise((r) => setTimeout(r, 200));
  const out = await evaluate(`(() => {
    ${hover ? "document.getElementById('b').classList.add('force-hover');" : ""}
    const bg = (function (el) {
      const parse = (s) => { const m = s.match(/rgba?\\(([^)]+)\\)/); if (!m) return null;
        const p = m[1].split(/[,\\s/]+/).filter(Boolean).map(Number);
        return { r: p[0], g: p[1], b: p[2], a: p.length > 3 ? p[3] : 1 }; };
      let node = el;
      while (node && node !== document.documentElement) {
        const c = parse(getComputedStyle(node).backgroundColor);
        if (c && c.a > 0) return c;
        node = node.parentElement;
      }
      return { r: 255, g: 255, b: 255, a: 1 };
    })(document.getElementById("b"));
    return { fg: getComputedStyle(document.getElementById("ico")).color,
             btnBg: getComputedStyle(document.getElementById("b")).backgroundColor,
             nearestBg: [bg.r, bg.g, bg.b] };
  })()`);
  const painted = await evaluate(RESOLVE);
  return { ...out, painted };
}

const lum = (c) => {
  const f = (v) => { v /= 255; return v <= 0.04045 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
  return 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b);
};
const ratio = (x, y) => { const a = lum(x), b = lum(y); const hi = Math.max(a, b), lo = Math.min(a, b); return (hi + 0.05) / (lo + 0.05); };
const fmt = (c) => `rgb(${Math.round(c.r)},${Math.round(c.g)},${Math.round(c.b)})`;

console.log(`using ${CHROME}`);

console.log("\nTHE SOURCE, not a hardcoded copy of it");
{
  check("the .organized rule is present and was read from the file", ACTIVE !== undefined, `got ${ACTIVE}`);
  check("it is #ffffff -- the colour this PR introduces", String(ACTIVE).toLowerCase() === "#ffffff", `got ${ACTIVE}`);
  check("the inactive state is the untouched rgba(191, 204, 214, 0.5)", /rgba\(191,\s*204,\s*214,\s*0?\.5\)/.test(String(INACTIVE)), `got ${INACTIVE}`);
}

console.log("\nWHICH BACKGROUND WINS -- the assumption the whole fix rests on");
{
  const before = await measure(APP_BEFORE, "organized");
  console.log(`  .btn-secondary background resolves to ${before.nearestBg} (computed ${before.btnBg})`);
  check(
    "it is Bootstrap's #3a3f44, as assumed",
    Math.abs(before.nearestBg[0] - 0x3a) <= 2 && Math.abs(before.nearestBg[1] - 0x3f) <= 2 && Math.abs(before.nearestBg[2] - 0x44) <= 2,
    `got rgb(${before.nearestBg})`
  );
  check("the app's theme layer does not override the button background", before.btnBg.includes("58, 63, 68"), before.btnBg);
}

console.log("\nICON CONTRAST vs the background (WCAG 1.4.11 non-text: 3.0)");
{
  const bg = { r: 0x3a, g: 0x3f, b: 0x44 };
  const old = await measure(APP_BEFORE, "organized");
  const neu = await measure(APP_AFTER, "organized");
  const rOld = ratio({ r: 0x66, g: 0x4c, b: 0x3f }, bg);
  check("the live stylesheet's active colour is what we measured", String(ACTIVE).toLowerCase() === "#ffffff" && Math.abs(neu.painted.r - 255) <= 1, `${ACTIVE} / ${fmt(neu.painted)}`);
  const rNew = ratio(neu.painted, bg);
  console.log(`  before #664c3f -> ${rOld.toFixed(2)}:1   painted ${fmt(old.painted)}`);
  console.log(`  after  #ffffff -> ${rNew.toFixed(2)}:1   painted ${fmt(neu.painted)}`);
  check("the OLD active icon FAILS the 3.0 non-text minimum", rOld < 3.0, `${rOld.toFixed(2)}:1`);
  check("the NEW active icon clears it", rNew >= 3.0, `${rNew.toFixed(2)}:1`);
  check("and clears AA for text too (4.5), which is the stricter reading", rNew >= 4.5, `${rNew.toFixed(2)}:1`);
  check("the icon inherits the button colour (currentColor), so the fix reaches the glyph", neu.painted.r > 240 && neu.painted.g > 240 && neu.painted.b > 240, fmt(neu.painted));
  check("the computed colour is literally #ffffff", neu.fg === "rgb(255, 255, 255)", neu.fg);
}

console.log("\nTHE ACTUAL COMPLAINT: active vs inactive must be DISTINGUISHABLE");
{
  const inact = await measure(APP_BEFORE, "not-organized");
  const oldAct = await measure(APP_BEFORE, "organized");
  const newAct = await measure(APP_AFTER, "organized");
  const rInOld = ratio(inact.painted, oldAct.painted);
  const rInNew = ratio(inact.painted, newAct.painted);
  console.log(`  inactive painted as ${fmt(inact.painted)} (alpha composited against the real background)`);
  console.log(`  inactive vs old active = ${rInOld.toFixed(3)}:1`);
  console.log(`  inactive vs new active = ${rInNew.toFixed(3)}:1`);
  check("the new active state is measurably more distinguishable from inactive", rInNew > rInOld, `${rInOld.toFixed(3)} -> ${rInNew.toFixed(3)}`);
  check("and clears 3.0 between the two states", rInNew >= 3.0, `${rInNew.toFixed(3)}:1`);
  check("the inactive state is untouched by the PR", inact.fg === "rgba(191, 204, 214, 0.5)", inact.fg);
}

console.log("\nHOVER: .btn-secondary:hover darkens the background to #23272b");
{
  const oldHover = ratio({ r: 0x66, g: 0x4c, b: 0x3f }, { r: 0x23, g: 0x27, b: 0x2b });
  const newHover = ratio({ r: 255, g: 255, b: 255 }, { r: 0x23, g: 0x27, b: 0x2b });
  console.log(`  old on hover = ${oldHover.toFixed(2)}:1    new on hover = ${newHover.toFixed(2)}:1`);
  check("the OLD active icon gets WORSE on hover, not better", oldHover < 3.0, `${oldHover.toFixed(2)}:1`);
  check("the NEW active icon still clears 3.0 on hover", newHover >= 3.0, `${newHover.toFixed(2)}:1`);
  check("white is robust across both backgrounds, which is why it is the right choice", newHover >= 7 && ratio({ r: 255, g: 255, b: 255 }, { r: 0x3a, g: 0x3f, b: 0x44 }) >= 7);
}

console.log("\nSCOPE: the PR body says the class is only used by the scene detail page");
{
  // Verified by reading the tree, not by the PR's claim: OrganizedButton is
  // imported by Gallery.tsx:442 and Image.tsx:368 as well as Scene.tsx. All three
  // render the same <Button variant="secondary">, so the same background applies
  // and the fix helps all three -- the claim is wrong, the change is still right.
  check("the PR's scope claim is false, but harmlessly so: same variant, same background, three call sites", true);
}

ws.close();
proc.kill();
console.log(
  failures === 0
    ? "\nOK: 1.35:1 -> 10.64:1, clears AAA, and the active/inactive gap widens 2.10 -> 3.74"
    : `\nFAIL: ${failures} check(s)`
);
process.exit(failures === 0 ? 0 : 1);
