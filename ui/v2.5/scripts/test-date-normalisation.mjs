// Test for `normalizeDateString` / `validateDateString` in src/utils/yup.ts.
//
// WHY THIS IS A .mjs SCRIPT AND NOT A TEST FILE: the UI has no test runner.
// package.json has no `test` script and no jest/vitest dependency, so the
// precedent set by check-country-names.mjs and test-file-input-nesting.mjs is a
// plain node script that exits non-zero on failure. It is the only regression
// guard the frontend has, which is why the ones that exist test their own
// checker as well (see the file-input script's `scripts/test-*.mjs`).
//
// WHAT IS WORTH TESTING HERE, and it is not the regexes. Every branch of
// normalizeDateString is a small regexp whose behaviour is readable. The risk is
// the property the function exists to provide: the output is a string the BACKEND
// also accepts, and the two live in different languages with independently
// written two-digit-year pivots. A front end that normalises to a string the API
// then rejects has moved the error, not fixed it -- and the user sees a
// validation error on a value they were told was fine.
//
// So the cases below are pairs: an input, and the exact string the backend must
// accept. See scripts/test-date-normalisation.mjs for the Go side, which asserts
// the same table from the other direction.

// WHY THE FUNCTION IS RE-PARSED INSTEAD OF IMPORTED.
//
// The obvious move is `import { normalizeDateString } from "../src/utils/yup.ts"`
// and it does not work: yup.ts's first line is
//
//   import { FormikErrors, yupToFormErrors } from "formik";
//
// and `FormikErrors` is a TYPE that formik exports only from its .d.ts. Node's
// ESM resolver looks for a named export in the runtime module, does not find
// one, and fails the whole import. `tsc --noEmit` is clean and `vite build` is
// clean, so this is NOT a defect in yup.ts -- it is node being stricter about a
// CJS/type-only import than the project's own toolchain is. Verified: tsc exits 0
// on this tree with the PR applied.
//
// So the function under test is lifted out of the source text below, rather than
// importing a module whose other 200 lines need a bundler to load. That is a
// real cost -- the copy can drift from the original -- and it is paid for
// deliberately: the drift is DETECTED, because `assertSourceUnchanged` fails the
// run if the lifted text no longer appears verbatim in yup.ts. A test that
// silently tests a stale copy is worse than no test, so the copy is policed.

import { readFileSync } from "node:fs";

const SOURCE = readFileSync(
  new URL("../src/utils/yup.ts", import.meta.url),
  "utf8",
);

/**
 * Lift `name`'s WHOLE declaration out of yup.ts, signature included.
 *
 * Two mistakes here, both of which produce a confusing error rather than an
 * obvious one:
 *
 * 1. Anchoring on `export function` misses `isLeapYear`, a module-private
 *    helper. Reported "isLeapYear not found" for a function three lines below
 *    the call site.
 * 2. Slicing from the opening BRACE returns the body only, so
 *    `isLeapYear(year: number)` becomes an arrow of nothing and the body
 *    references a `year` that was never declared -- `ReferenceError: year is not
 *    defined`, pointing into a function that plainly has a parameter. Lift from
 *    the `function` keyword to the closing brace, annotations and all.
 */
function lift(name) {
  const start = SOURCE.search(
    new RegExp(`(^|\\n)\\s*(export\\s+)?function\\s+${name}\\s*\\(`),
  );
  if (start < 0) throw new Error(`${name} not found in yup.ts`);
  const at = SOURCE.indexOf("function", start);
  const open = SOURCE.indexOf("{", at);
  let depth = 0;
  for (let i = open; i < SOURCE.length; i++) {
    if (SOURCE[i] === "{") depth++;
    else if (SOURCE[i] === "}" && --depth === 0) {
      return SOURCE.slice(at, i + 1);
    }
  }
  throw new Error(`unbalanced braces lifting ${name}`);
}

/**
 * Strip TypeScript-only syntax from a lifted body.
 *
 * Node's `--experimental-strip-types` rewrites a .ts MODULE, but `new Function`
 * gets a raw string with the annotations still in it, so `let year: number;`
 * is a SyntaxError. The annotations that can appear in these three functions
 * are narrow and enumerated below; anything else raises rather than being
 * silently mangled, because a stripper that quietly corrupts a function is the
 * same failure mode as a test asserting a hand-written copy of its subject.
 *
 * The alternative -- a real bundler, or a vitest dependency -- is out of scope
 * for a repo whose frontend deliberately has no test runner.
 */
function stripTypes(src, fnName) {
  let out = src;
  // `let|const|var|param) name: Type` -> drop the annotation. The lookahead keeps
  // a ternary's `?:` and an object literal's `key:` intact by requiring the
  // annotation to run to a comma, paren, brace, semicolon or line end.
  out = out.replace(
    /([\w$)\]])\s*:\s*[A-Za-z_$][\w$.<>[\]|&, ]*?(?=\s*[,)=;{])/g,
    "$1",
  );
  // A bare `name: Type` at the start of a line (a struct-ish field in an object
  // literal would match the rule above, so this is the declared-variable case).
  out = out.replace(/^\s*(let|const|var)\s+(\w+)\s*:\s*[^=;]+=/gm, "$1 $2 =");
  // An optional marker on a PARAMETER: `value?: string`. The lookahead above
  // deliberately stops at `?` so a ternary's `?:` survives, which means an
  // optional parameter's annotation is not reached by it -- `value?:` needs its
  // own rule, and the error when it is missing is
  // `SyntaxError: Unexpected token '?'` pointing at a `?` that is plainly a
  // parameter marker. Only `?:`, never a bare `?`, so ternaries stay whole.
  out = out.replace(/\?\s*:\s*[A-Za-z_$][\w$.<>[\]|&, ]*?(?=\s*[,)=;{])/g, "");
  // An `as Type` assertion. `as` is a real word here: the source has locals
  // named `year` and `daysInMonth` and an expression containing `as` between
  // them, so a naive `\s+as\s+\w+` silently deletes code. The type must look
  // like a type -- a capitalised or underscore identifier, optionally
  // dotted/generic/array -- which is what separates `as Type` from the
  // mid-expression `as`.
  out = out.replace(
    /(\S)\s+as\s+(?=[A-Z_$][\w$]*(?:\s*[<.\[,]|\s*$|\s+[^a-z]))([A-Z_$][\w$.<>[\]|&]*(?:\s*<[^>]*>)?(?:\s*\[\])?)/gm,
    "$1",
  );
  // A final PARSE, not a pattern-match. The previous check scanned for `<`, `>`
  // and `?` in "non-operator position", which is not a thing that can be decided
  // per character: it flagged five lines of `normalizeDateString` that are
  // entirely valid JavaScript -- `year < 1`, `shortYear <= 68 ? a : b`,
  // `isLeapYear(year) ? 29 : 28`. A checker that rejects correct code is worse
  // than no checker, because the obvious response is to delete the check.
  //
  // The real question is one boolean: is this now parseable as plain JS? Build
  // the function and let the engine answer.
  try {
    new Function(`return function _syntaxProbe(){\n${out}\n};`);
  } catch (e) {
    const bad = out
      .split("\n")
      .map((l, i) => [i + 1, l])
      .filter(([, l]) => /\?[.\s)\]},;]/.test(l.replace(/`[^`]*`/g, "``")))
      .map(([n, l]) => `${n}: ${l.trim()}`);
    throw new Error(
      `stripTypes left unparseable JS in ${fnName}: ${e.message}` +
        (bad.length ? `\n        suspect line(s): ${bad.join(" | ")}` : ""),
    );
  }
  return out;
}

const isLeapYearSrc = stripTypes(lift("isLeapYear"), "isLeapYear");
const validateSrc = stripTypes(lift("validateDateString"), "validateDateString");
const normalizeSrc = stripTypes(lift("normalizeDateString"), "normalizeDateString");

// The drift check must compare against the RAW lifted text, since stripTypes
// has by definition changed it.
const RAW = {
  normalizeDateString: lift("normalizeDateString"),
  validateDateString: lift("validateDateString"),
  isLeapYear: lift("isLeapYear"),
};

/**
 * Build a callable from a lifted declaration.
 *
 * `new Function("normalizeDateString", src)` does NOT do what it looks like: the
 * first argument is the name of a PARAMETER, so inside `src` the identifier
 * `normalizeDateString` is bound to the value passed at the call site -- here,
 * the function itself. It happened to work, and the wiring error was invisible
 * until the tests failed: `validateDateString` returned `undefined` for every
 * input, because it was calling a boolean.
 *
 * `deps` maps the identifier the SOURCE calls to the function to supply, so the
 * parameter names always match what the source says. Passing `$dep` instead --
 * a name that cannot collide -- looks safer and is strictly worse: the source
 * calls `isLeapYear`, the parameter is `$dep1`, and the function throws
 * `isLeapYear is not defined` only when that branch is reached.
 *
 * A name that cannot occur in the source is the right idea, applied to the
 * CHECK rather than the wiring: `assertDepsResolve` below proves every free
 * identifier the body calls is actually bound.
 */
function build(src, deps = {}) {
  const depNames = Object.keys(deps);
  const factory = new Function(
    ...depNames,
    `${src}\nreturn ${src.match(/function\s+(\w+)/)[1]};`,
  );
  return factory(...depNames.map((n) => deps[n]));
}

const isLeapYear = build(isLeapYearSrc);
const normalizeDateString = build(normalizeSrc, { isLeapYear });
const validateDateString = build(validateSrc, { normalizeDateString });

/**
 * Prove the wiring, rather than trusting it.
 *
 * Every one of these three mistakes produces a function that RUNS and returns a
 * plausible wrong answer, which is the worst kind of test-harness bug:
 *
 * 1. A dependency passed under a name the source does not call (`$dep1` for
 *    `isLeapYear`) -- only throws on the branch that reaches it.
 * 2. A dependency passed as the function that needs it (the original wiring:
 *    validateDateString was handed normalizeDateString, then normalizeDateString
 *    was handed validateDateString, so each called the other and both returned
 *    undefined for every input).
 * 3. A dependency omitted entirely.
 *
 * So: call each one and require the answer to be a value of the right TYPE.
 * `undefined` is the tell for all three, because a wrong wiring returns
 * undefined rather than throwing.
 */
for (const [name, fn, want] of [
  ["isLeapYear", isLeapYear, "boolean"],
  ["normalizeDateString", normalizeDateString, "string|undefined"],
  ["validateDateString", validateDateString, "boolean"],
]) {
  const probe = fn === isLeapYear ? fn(2000) : fn("2014-01-02");
  if (probe === undefined) {
    console.error(
      `FAIL  ${name} returned undefined for a valid input, which is the symptom ` +
        `of a mis-wired dependency rather than of ${name} itself.`,
    );
    process.exit(1);
  }
  if (want === "boolean" && typeof probe !== "boolean") {
    console.error(`FAIL  ${name} returned ${typeof probe}, want boolean`);
    process.exit(1);
  }
}

/**
 * The copy is policed: every lifted body must still appear verbatim in the
 * source. If someone edits yup.ts and forgets this script, the run fails
 * loudly instead of quietly testing yesterday's function.
 */
function assertSourceUnchanged() {
  for (const [name, body] of Object.entries(RAW)) {
    if (!SOURCE.includes(body)) {
      console.error(
        `FAIL  ${name} was lifted but no longer appears verbatim in yup.ts.\n` +
          `        This script tests a COPY. Update it, or the run is testing a\n` +
          `        function that no longer exists in the source.`,
      );
      process.exit(1);
    }
  }
}
assertSourceUnchanged();

let failures = 0;
let checks = 0;

function eq(label, got, want) {
  checks += 1;
  if (got !== want) {
    failures += 1;
    console.error(`FAIL  ${label}\n        got  ${JSON.stringify(got)}\n        want ${JSON.stringify(want)}`);
  }
}

// [input, expected output or undefined]
const CASES = [
  // Already canonical -- normalising must be a no-op, and idempotent.
  ["2014-01-02", "2014-01-02"],
  ["2014-01", "2014-01"],
  ["2014", "2014"],

  // The new formats.
  ["2014-1-2", "2014-01-02"],
  ["2014-01-2", "2014-01-02"],
  ["2014-1-02", "2014-01-02"],
  ["20140102", "2014-01-02"],
  ["2014.01.02", "2014-01-02"],
  ["2014.1.2", "2014-01-02"],
  ["2014.8", "2014-08"],

  // Two-digit years, and the pivot. The cutoff is the interesting part: 68 is
  // 2068 and 69 is 1969. This is the same pivot Go's "06-01-02" layout uses,
  // which the backend harness asserts independently.
  ["14-01-02", "2014-01-02"],
  ["68-01-02", "2068-01-02"],
  ["69-01-02", "1969-01-02"],
  ["99-01-02", "1999-01-02"],
  ["00-01-02", "2000-01-02"],
  ["14.01.02", "2014-01-02"],

  // Leap years, including the century rules a flat %4 test gets wrong.
  ["2000-02-29", "2000-02-29"],
  ["2024-02-29", "2024-02-29"],
  ["1900-02-29", undefined], // 1900 is divisible by 4 and NOT by 400
  ["2100-02-29", undefined],
  ["2023-02-29", undefined], // not a leap year at all

  // Month lengths. April/June/September/November have 30; the old code only
  // checked <= 31, so 2023-04-31 was accepted and the backend then rejected it.
  ["2023-04-31", undefined],
  ["2023-06-31", undefined],
  ["2023-09-31", undefined],
  ["2023-11-31", undefined],
  ["2023-04-30", "2023-04-30"],

  // Out of range.
  ["0000-01-01", undefined],
  ["2023-00-01", undefined],
  ["2023-13-01", undefined],
  ["2023-01-00", undefined],
  ["2023-01-32", undefined],

  // Not a date at all.
  ["not-a-date", undefined],
  ["", undefined],
  ["2014-01-02T15:04:05Z", undefined], // RFC3339 is a backend format but not a
  // form this field accepts; the backend still parses it, the UI does not offer it
];

console.log(`normalizeDateString: ${CASES.length} cases`);
for (const [input, want] of CASES) {
  eq(`normalizeDateString(${JSON.stringify(input)})`, normalizeDateString(input), want);
}

// IDEMPOTENCE. The DateInput calls this on every blur, so normalising an
// already-normalised value must not change it -- otherwise a second blur
// produces a third value, and a third blur a fourth.
console.log("idempotence");
for (const [input] of CASES) {
  const once = normalizeDateString(input);
  if (once === undefined) continue;
  const twice = normalizeDateString(once);
  eq(`normalizeDateString(normalizeDateString(${JSON.stringify(input)}))`, twice, once);
}

// validateDateString is the yup validator and must agree with normalize, and
// must still accept the empty value (an optional field).
console.log("validateDateString");
eq("validateDateString(undefined)", validateDateString(undefined), true);
// "" is a DIFFERENT case from undefined, and getting it wrong here would have
// been a false alarm: the source is `if (!value) return true;` and "" is
// falsy, so the function returns true -- correctly. The table below computed
// `want !== undefined`, and the table's entry for "" is `undefined`, so the
// test asserted false and the FUNCTION was right.
eq('validateDateString("")', validateDateString(""), true);
for (const [input, want] of CASES) {
  if (input === "") continue; // covered above, and for the opposite reason
  eq(`validateDateString(${JSON.stringify(input)})`, validateDateString(input), want !== undefined);
}

// THE POINT OF THE WHOLE FILE: everything this accepts must be a string the
// BACKEND parses to the same year/month/day. If the two implementations disagree
// about the two-digit pivot or about a month's length, this is where it shows.
console.log("backend agreement (see scripts/date-parse-probe.go for the Go side)");
const { execFileSync } = await import("node:child_process");
for (const [input, want] of CASES) {
  const normalised = normalizeDateString(input);
  if (normalised === undefined) continue;
  const out = execFileSync(
    "go",
    ["run", "./scripts/date-parse-probe.go", normalised],
    { cwd: new URL("..", import.meta.url).pathname, encoding: "utf8" },
  ).trim();
  // The probe prints "ok <date> <precision>", so compare the PREFIX. Asserting
  // `out === "ok"` reported a failure for every single case -- the first
  // version of this check was simply wrong about what the probe prints, and it
  // would have looked like a total backend/frontend disagreement.
  eq(
    `backend accepts ${JSON.stringify(normalised)} (from ${JSON.stringify(input)})`,
    out.startsWith("ok") ? "ok" : out,
    "ok",
  );
}

console.log(failures === 0 ? `\nOK: ${checks} checks` : `\n${failures}/${checks} FAILED`);
process.exit(failures === 0 ? 0 : 1);
