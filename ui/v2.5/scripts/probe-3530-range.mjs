// #3530 -- exercises SceneRangeForm's `validate` against the real compiled component.
//
// WHY A PROBE SCRIPT AND NOT A TEST FILE
//
// ui/v2.5 has no JS test runner at all: no vitest, no jest, no `test` script, and no existing
// *.test.ts anywhere under src/. Adding a runner to ship three validation rules would be a far
// larger change than the feature, and would put a new toolchain requirement on every future
// contributor. The repo's own convention for this is scripts/*.mjs -- a node script that
// asserts and exits non-zero (see probe-organized-contrast.mjs, test-date-normalisation.mjs).
//
// WHY THIS IS NOT A PROXY FOR THE COMPONENT
//
// `validate` is exported from SceneRangeForm.tsx precisely so it can be reached this way. This
// loads the actual TypeScript source, strips the types and the JSX-free imports, and evaluates
// the real function -- so a change to the rules that breaks the API's rules breaks this probe.
// The rules are duplicated in mutationResolver.validateSceneWindow on purpose (each layer has
// messages the other cannot produce), and this probe pins the FRONT-END copy.
//
// Run:  node scripts/probe-3530-range.mjs
// Exits 1 on any failure.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const src = readFileSync(
  join(here, "../src/components/Scenes/SceneDetails/SceneRangeForm.tsx"),
  "utf8"
);

// Pull `validate` out of the component module without a TS/JSX toolchain: take the function
// source, strip its parameter type annotations, and hand it to the Function constructor.
const start = src.indexOf("export function validate(");
if (start === -1) {
  console.error("FAIL: validate() not found in SceneRangeForm.tsx -- renamed or moved?");
  process.exit(2);
}
// The function runs to the end of the file and the file has NO trailing newline, so the
// terminator is `\n}` with nothing after it. Matching `\n}\n` silently yields an empty slice
// and "validate is not a function", which looks like a broken probe rather than a bad
// terminator -- hence the guard on the extracted text below.
const tail = src.slice(start);
const end =
  start +
  (/\n\}\s*$/.test(tail) ? tail.lastIndexOf("\n}") : src.indexOf("\n}\n", start)) +
  2;
let body = src.slice(start, end);

if (!body.includes("function validate(") || body.trim().length < 200) {
  console.error("FAIL: validate() could not be extracted from SceneRangeForm.tsx.");
  console.error(
    "It is either gone, renamed, no longer last in the file, or grew past the terminator."
  );
  console.error(`extracted ${body.length} bytes`);
  process.exit(2);
}

// Strip the parameter list and the return type, keeping the body's opening brace.
//
// `paramsEnd` points at the newline that ENDS the last parameter, so the literal
// `): string | undefined` starts one character later. Getting that off-by-one lands the slice
// inside the word `undefined` and produces `function validate(...)d {` -- which reads as a
// mystery syntax error rather than an arithmetic slip, so the two offsets are named here.
//
// Slicing from after the return type leaves ` {`, the brace that opens the body. Only the
// space is stripped, never the brace: dropping the brace leaves a bare `if` statement where a
// function body should be.
//
// Replacing the whole span, rather than matching each type separately, also sidesteps the intl
// type's own braces and the trailing return type, which a brace-matching strip half-consumes.
const returnType = "): string | undefined";
const paramsEnd = body.indexOf(`\n${returnType}`);
if (paramsEnd === -1) {
  console.error(
    `FAIL: validate()'s signature no longer matches \`${returnType}\`.`
  );
  console.error("Its return type changed, or it was reformatted. Update this probe.");
  process.exit(2);
}
body =
  "function validate(start, end, fileDuration, intl)" +
  body
    .slice(paramsEnd + 1 + returnType.length)
    .replace(/^ +/, "");

if (/: number|: string|\| null|\| undefined/.test(body)) {
  console.error("FAIL: a type annotation survived the strip; the extraction is stale.");
  console.error(body.slice(0, 400));
  process.exit(2);
}

// Two stubs, because validate() reaches for both.
//
// intl: the probe cares WHICH rule fires, not what it says. Returning the id keeps a failure
// readable -- "expected no error, got validation.range_end_before_start" beats "expected no
// error, got <message>".
//
// TextUtils: only used to render a duration into the past-the-file message, so the probe
// supplies the one function it needs. Supplying it also means a future edit that leans on
// another TextUtils helper fails here loudly rather than silently passing on an undefined
// that happens not to be reached by these cases.
const intl = { formatMessage: (m) => m.id };
const TextUtils = { secondsToTimestamp: (s) => `${s}s` };

// Evaluate the extracted function with both stubs in scope.
// biome-ignore lint/security/noGlobalEval: evaluating the extracted source is the point
const validate = new Function(
  "intl",
  "TextUtils",
  `${body}\nreturn validate;`
)(intl, { secondsToTimestamp: (s) => `${s}s` });

let failed = 0;
function check(label, start, end, dur, expectError) {
  const got = validate(start, end, dur, intl);
  const ok = expectError === null ? got === undefined : got === expectError;
  if (!ok) {
    failed++;
    console.error(`  FAIL ${label}`);
    console.error(`    expected ${expectError ?? "(no error)"}`);
    console.error(`    got      ${got ?? "(no error)"}`);
  } else {
    console.log(`  ok   ${label}${got ? `  [${got}]` : ""}`);
  }
}

console.log("=== accepted ===");
// No window at all is the state every pre-existing scene is in, so it must not be refused.
check("both null", null, null, 1800, null);
// `0` is a VALID start, not "unset" -- the reason the API uses pointers rather than float64.
check("start 0, open-ended", 0, null, 1800, null);
check("bounded, sane", 120, 480, 1800, null);
check("end exactly at duration", 120, 1800, 1800, null);
// A zero-LENGTH file cannot have a window at all, and the database agrees: migration 122's
// CHECK is `end_time > start_time`, STRICT, so even 0..0 is refused. My first draft asserted
// this was accepted -- the assertion was wrong, not the rule, and the DB is the authority.
check("zero-length file, 0..0", 0, 0, 0, "validation.range_end_before_start");
// ...while a window on a zero-length file that is still open-ended is fine: there is no end to
// compare against, so the file's length is 0 and nothing overruns it.
check("zero-length file, open-ended", 0, null, 0, null);

console.log("\n=== refused ===");
check("end before start", 480, 120, 1800, "validation.range_end_before_start");
// end == start is a window with no frames. Easy to fold into the case above by accident, and
// every derived artefact (sprite grid, preview) would render empty.
check("end == start (zero-length window)", 120, 120, 1800, "validation.range_end_before_start");
check("start negative", -1, 120, 1800, "validation.range_start_negative");
check("end negative", 120, -5, 1800, "validation.range_end_negative");
check(
  "end past the file",
  120,
  1801,
  1800,
  "validation.range_end_past_file"
);

console.log("\n=== the boundary that matters ===");
// 1800.5 is past a 1800s file by half a second. Floating point makes this the case a naive
// `> fileDuration` on a rounded display value gets wrong.
check("end past by 0.5s", 120, 1800.5, 1800, "validation.range_end_past_file");
// ...and the same value must be ACCEPTED once the file is genuinely longer.
check("same value, longer file", 120, 1800.5, 3600, null);

console.log(failed === 0 ? "\nPASS" : `\nFAIL (${failed})`);
process.exit(failed === 0 ? 0 : 1);