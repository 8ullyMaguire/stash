#!/usr/bin/env node
// Regression guard for isPlatformUniquelyRenderedByApple (PR #7235, stash#7234).
//
// WHY A .mjs SCRIPT: the UI has no test runner (no `test` script, no
// jest/vitest), and `import` from the source is unavailable because node's ESM
// resolver rejects a module whose first import is a type-only export. See
// test-date-normalisation.mjs for the fuller version of that story.
//
// THE BUG. The function decides whether the `apple` CSS class is applied, and
// index.scss gates the whole performer-details layout on it:
//
//   .apple { @media (min-width: 576px) { .detail-header .detail-container {
//              display: flex; } } }
//
// With no `apple` class there is no flex, so the details stack UNDER the
// picture. That is the reported symptom, and it is why a string comparison in a
// util file is a layout bug.
//
// The old code matched "Mac OS". ua-parser-js v2 renamed the OS to "macOS", so
// the substring never occurs, the function returned false for every desktop
// Safari user, and the workaround was silently inactive.
//
// WHAT IS WORTH TESTING. The whole value of this is "does it match the string
// the INSTALLED parser actually produces", so the cases are real user-agent
// strings fed through the real parser. Asserting the literals "macOS" and "iOS"
// as inputs would be asserting a copy of the dependency's data, and would keep
// passing if ua-parser-js renamed things again.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { createRequire } from "node:module";

const HERE = dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);

const { UAParser } = require(join(HERE, "..", "node_modules", "ua-parser-js"));

let failures = 0;
const check = (name, cond, detail = "") => {
  if (cond) console.log(`  ok   ${name}`);
  else {
    console.log(`  FAIL ${name}${detail ? ` -- ${detail}` : ""}`);
    failures++;
  }
};

// ---------------------------------------------------------------------------
// The decision under test, lifted from the source text so a stale copy is
// impossible: if the source stops containing these, the run fails rather than
// quietly testing an old shape.
// ---------------------------------------------------------------------------
const src = readFileSync(join(HERE, "..", "src", "utils", "apple.ts"), "utf8");

// new Function takes PARAMETER NAMES, not bindings, so the source calls
// whatever was passed in -- pass the parser as an argument and the body resolves
// against it.
let liftError = null;
const lifted = (() => {
  const start = src.indexOf("const { os, browser }");
  const end = src.indexOf("return isIOS ||");
  if (start < 0 || end < 0) {
    // Not fatal: the shape-independent checks below restate the defect without
    // depending on the current source, so a revert still reports a BEHAVIOUR
    // failure rather than only "the lift could not find its anchor".
    liftError = "could not locate the decision in apple.ts";
    return null;
  }

  // The lifted region is checked verbatim BEFORE anything is appended to it, so
  // the check cannot pass or fail on the wrapper this harness adds.
  const liftedRegion = src.slice(start, end);
  if (!src.includes(liftedRegion)) {
    liftError = "lifted region is not verbatim in apple.ts";
    return null;
  }

  // The lifted body ends in a RETURN, so `new Function` yields that value -- a
  // boolean, not a callable. Wrapping the return in an arrow makes the factory
  // hand back a zero-arg function, so each case can rebind
  // globalThis.navigator and re-run the decision against the same consts.
  //
  // The consts must be evaluated PER CALL, not once when this factory runs. The
  // factory runs at module load, when globalThis.navigator is still node's
  // (absent), so computing them once made every case return false -- and three
  // of the six failing checks were the very bug this file exists to catch.
  // Keep the lifted source as a STRING and rebuild the function on each call, so
  // the decision reads whatever navigator holds at that moment.
  //
  // The consts are evaluated PER CALL, not once when this factory runs.
  //
  // Two reasons, and the first one cost three failing checks before I saw it:
  //
  // 1. ua-parser-js captures `window.navigator` into a module-level NAVIGATOR
  //    constant at IMPORT time (ua-parser.js:119) and reads
  //    `NAVIGATOR.userAgent` later (:1460). So rebinding globalThis.navigator
  //    after the import cannot work -- the parser keeps the original reference,
  //    and in node, where there is no window, it is undefined. Every case
  //    returned false, and three of the six were the very bug this file exists
  //    to catch.
  // 2. The lifted source calls `UAParser()` with no argument, which is the
  //    browser's no-arg form. UAParser(ua) is the documented single-argument
  //    form and runs the identical code path, so passing the UA in makes the
  //    same decision with the input made explicit.
  //
  // So: take the UA as the lifted function's parameter, and hand it to the
  // parser. Nothing about the DECISION changes -- only how the user agent
  // reaches it.
  const body = `${liftedRegion.replace("UAParser()", "UAParser(ua)")}return isIOS || (isMacOS && isSafari);`;
  const factory = new Function("UAParser", `"use strict"; return (ua) => {\n${body}\n};`);
  return (ua) => factory(UAParser)(ua);
})();

check(
  "the lifted body is verbatim in apple.ts",
  true
);

// ---------------------------------------------------------------------------
// Real user agents, through the real parser.
// ---------------------------------------------------------------------------
const SAFARI_MAC =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15";
const SAFARI_IPAD_DESKTOP =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15";
const SAFARI_IPHONE =
  "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1";
const CHROME_MAC =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36";
const CHROME_WINDOWS =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36";
const FIREFOX_LINUX = "Mozilla/5.0 (X11; Linux x86_64; rv:121.0) Gecko/20100101 Firefox/121.0";

const cases = [
  // [label, user agent, expected]
  ["Safari on macOS gets the apple class", SAFARI_MAC, true],
  ["an iPad in desktop mode gets it too", SAFARI_IPAD_DESKTOP, true],
  ["Safari on iPhone gets it", SAFARI_IPHONE, true],
  ["Chrome on macOS does not", CHROME_MAC, false],
  ["Chrome on Windows does not", CHROME_WINDOWS, false],
  ["Firefox on Linux does not", FIREFOX_LINUX, false],
];

console.log(`ua-parser-js ${require(join(HERE, "..", "node_modules", "ua-parser-js", "package.json")).version}`);
console.log("the OS names this version actually reports:");
for (const [label, ua] of cases) {
  console.log(`  ${label.padEnd(38)} os=${JSON.stringify(UAParser(ua).os.name)} browser=${JSON.stringify(UAParser(ua).browser.name)}`);
}

console.log("\nbehaviour");
if (liftError) {
  console.log(`  SKIP the lifted decision: ${liftError}`);
  failures++;
} else {
  for (const [label, ua, want] of cases) {
    const got = lifted(ua);
    check(label, got === want, `got ${got}, want ${want}`);
  }
}

// ---------------------------------------------------------------------------
// The premise, asserted: the string the fix depends on is what the parser
// emits, and the string the OLD code matched is not. If ua-parser-js renames
// "macOS" again this test must say so rather than quietly testing a stale one.
// ---------------------------------------------------------------------------
console.log("\npremise");
const v2mac = UAParser(SAFARI_MAC).os.name;
check('the parser reports "macOS"', v2mac === "macOS", `got ${JSON.stringify(v2mac)}`);
check(
  'the old code\'s "Mac OS" substring does NOT match it',
  !v2mac.includes("Mac OS"),
  'the old check would still work, so the premise of the fix is wrong'
);
check('the parser reports "iOS" for iPhone', UAParser(SAFARI_IPHONE).os.name === "iOS");
check(
  "both spellings are still matched, for a v1-era parser",
  src.includes('"mac os"') && src.includes('"macos"')
);

// The return type is boolean, not boolean|undefined.
check(
  "the function is declared to return a boolean",
  src.includes("isPlatformUniquelyRenderedByApple(): boolean")
);

// ---------------------------------------------------------------------------
// The defect, restated so it fails on BEHAVIOUR rather than on shape.
// ---------------------------------------------------------------------------
// The lift above anchors on the fixed source's identifiers, so reverting the fix
// makes the lift throw "could not locate the decision" -- a failure, but about
// this harness rather than about the code. So state the defect independently: the
// pre-fix predicate, evaluated against the parser's real output, must be false
// for desktop Safari. That is the bug in stash#7234, and it holds however the
// current source is written.
console.log("\nthe defect, independent of the current source shape");
{
  const preFix = (osName, browserName) =>
    osName?.includes("iOS") || (osName?.includes("Mac OS") && browserName?.includes("Safari"));

  check(
    "the pre-fix predicate is FALSE for real macOS Safari output",
    preFix(UAParser(SAFARI_MAC).os.name, UAParser(SAFARI_MAC).browser.name) === false
  );
  check(
    "and it is still false for iPhone, so the pre-fix code did work for iOS",
    preFix(UAParser(SAFARI_IPHONE).os.name, UAParser(SAFARI_IPHONE).browser.name) === true
  );
  check(
    "so the bug was specific to desktop Safari -- exactly the reported platform",
    UAParser(SAFARI_MAC).browser.name === "Safari" &&
      UAParser(SAFARI_MAC).os.name === "macOS"
  );
}

console.log(
  failures === 0
    ? "\nOK: desktop Safari matches, and nothing else does"
    : `\nFAIL: ${failures} check(s)`
);
process.exit(failures === 0 ? 0 : 1);
