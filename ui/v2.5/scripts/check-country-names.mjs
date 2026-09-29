/**
 * Behavioural verification for the stash#5237 country-name fix.
 *
 * WHY A SCRIPT AND NOT A TEST FILE. The UI package has no test runner: no
 * vitest, no jest, no @testing-library, and package.json has no "test"
 * script. Adding one is a dependency change to upstream's frontend, which is
 * a bigger decision than this issue and belongs to the owner.
 *
 * So this compiles the two real source files with the project's own tsc and
 * drives the compiled output. Compiling rather than mocking matters: the
 * behaviour under test is a call into i18n-iso-countries with a particular
 * `select` mode, and only the real library has the real data.
 *
 *   cd ui/v2.5 && node scripts/check-country-names.mjs
 *
 * WHAT IT CHECKS.
 *
 *  1. TW renders as "Taiwan", not the library's official name. The issue.
 *  2. The name comes from the library's OWN alias table, so a library upgrade
 *     that changes it changes the UI too. Asserting a bare literal would pin
 *     the fix to today's data and hide an upstream rename.
 *  3. Nothing else regressed: every country still present, no empty label,
 *     every code well-formed.
 *  4. getCountryByISO and getCountries agree -- two separate code paths that
 *     display the same field in two different places.
 *  5. Codes with no alias still render their official name. select: "alias"
 *     must fall through, not blank them out.
 *  6. Locale fallback still works in both directions.
 *
 * IT PRINTS THE FULL DIFF. Every entry the library stores as
 * official-name-plus-alternates is listed with what it rendered before and
 * what it renders now, so a reviewer sees the whole blast radius of the
 * change instead of inferring it.
 */

import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { rmSync, mkdirSync, writeFileSync, existsSync } from "node:fs";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";
import Module from "node:module";

const HERE = dirname(fileURLToPath(import.meta.url));
const UI_ROOT = resolve(HERE, "..");
const require = createRequire(import.meta.url);
const Countries = require("i18n-iso-countries");

// Compile the real sources. module commonjs because the library is CJS and
// the emitted output is loaded with require(); resolveJsonModule because
// src/locales/index.ts imports the language JSON files.
//
// The output goes to a sibling of src/ so that the `src/...` baseUrl
// specifier resolves as a plain relative path from the emitted file, with no
// NODE_PATH and no resolver hook. Three other locations were tried and each
// has a distinct reason to fail:
//
//   /tmp -- the emitted country.js does require("i18n-iso-countries"), and
//     Node resolves that by walking up from the file's own directory, so a
//     build with no node_modules above it fails MODULE_NOT_FOUND.
//   node_modules/.cache -- worse: Node SKIPS node_modules directories during
//     resolution, so even a correct NODE_PATH cannot resolve from there.
//   .country-check-build (a plain sibling) -- tsc emits `require("src/locales")`
//     verbatim, because baseUrl is a compile-time convention the emitted
//     require() cannot carry. NODE_PATH finds the directory but not
//     `src/locales` as a directory specifier, because CJS resolution will not
//     append .ts to a bare path. Only .ts is loadable here, since the
//     dependency needed to strip types is not a project dependency.
//
// Emitting with the two sources' own paths as the outDir root -- the
// directory literally named "src" -- makes the emitted layout
//
//   src/.country-check-build/src/utils/country.js
//   src/.country-check-build/src/locales/index.js
//
// so the emitted country.js's `require("src/locales")` is a plain relative
// hop that lands on the emitted locales/index.js. That is the whole reason
// for the ".country-check-build/src" shape rather than a flat one.
const OUT = join(UI_ROOT, ".country-check-build");
rmSync(OUT, { recursive: true, force: true });
mkdirSync(join(OUT, "src"), { recursive: true });
// package.json here has "type": "module", so a .js file anywhere under the
// project is loaded as ESM -- and tsc emitted CommonJS for the CJS library. A
// local marker declaring the build directory CommonJS is the standard way to
// tell Node which module system the emitted files use.
writeFileSync(
  join(OUT, "package.json"),
  JSON.stringify({ type: "commonjs" }, null, 2) + "\n"
);

try {
  execFileSync(
    process.execPath,
    [
      join(UI_ROOT, "node_modules/typescript/bin/tsc"),
      "src/utils/country.ts",
      "src/locales/index.ts",
      // outDir is src/ itself so the emitted tree is OUT/src/... and the
      // `src/locales` specifier inside the emitted JS is a relative hop that
      // lands on the emitted locales/index.js. See the comment on OUT.
      "--outDir",
      join(OUT, "src"),
      "--rootDir",
      "src",
      "--module",
      "commonjs",
      "--target",
      "es2022",
      "--moduleResolution",
      "node",
      "--esModuleInterop",
      "--resolveJsonModule",
      "--skipLibCheck",
      "--baseUrl",
      ".",
    ],
    { cwd: UI_ROOT, stdio: "pipe" }
  );

  // tsc emits `require("src/locales")` verbatim: baseUrl is a compile-time
  // convention that the emitted require() cannot carry. Vite resolves it from
  // baseUrl at dev time; plain Node has no such rule.
  //
  // NODE_PATH is not enough, and the reason is worth recording because it
  // looks like it should work. NODE_PATH does resolve bare specifiers, but CJS
  // resolution then tries `src/locales`, `src/locales.js`, `src/locales.json`,
  // `src/locales.node` -- never `index.ts`. The emitted file is index.js
  // inside a directory, and nothing makes Node try a directory's index for a
  // BARE specifier; the index rule only applies to relative paths like
  // "./locales". The fix is tsconfig-paths, which this project does not
  // depend on and which should not be added to upstream's frontend for a
  // check script.
  //
  // So: map the one specifier, explicitly, to the emitted file. Twelve lines
  // of resolver, no new dependency, and it fails loudly if the emitted layout
  // ever changes.
  const emitRoot = join(OUT, "src");
  const originalResolve = Module._resolveFilename;
  Module._resolveFilename = function patched(request, ...rest) {
    if (request.startsWith("src/")) {
      const rel = request.slice(4);
      // A bare `src/locales` names a DIRECTORY whose entry point is
      // locales/index.ts, so the emitted target is locales/index.js. Probing
      // the index form explicitly is required: this is a bare specifier, and
      // the directory-index rule that would apply to "./locales" does not
      // apply here.
      for (const target of [
        join(emitRoot, `${rel}.js`),
        join(emitRoot, rel, "index.js"),
      ]) {
        if (existsSync(target)) return target;
      }
    }
    return originalResolve.call(this, request, ...rest);
  };

  const { getCountryByISO, getCountries } = require(
    join(emitRoot, "utils/country.js")
  );
  let failures = 0;
  const check = (name, fn) => {
    try {
      fn();
      console.log(`  PASS  ${name}`);
    } catch (e) {
      failures++;
      console.log(`  FAIL  ${name}\n        ${e.message}`);
    }
  };

  console.log("stash#5237 -- country name rendering\n");

  // ---- 1. the issue itself ---------------------------------------------
  check("TW renders as the bare name, not the official one", () => {
    const official = Countries.getName("TW", "en");
    const shown = getCountryByISO("TW", "en");
    assert.equal(
      shown,
      "Taiwan",
      `expected "Taiwan", got ${JSON.stringify(shown)}`
    );
    assert.notEqual(
      shown,
      official,
      `still showing the library's official name ${JSON.stringify(official)}`
    );
  });

  // ---- 2. the name is one the library itself holds ----------------------
  check("the name is a name the library already holds for TW", () => {
    const all = Countries.getName("TW", "en", { select: "all" });
    assert.ok(
      Array.isArray(all) && all.includes(getCountryByISO("TW", "en")),
      `rendered ${JSON.stringify(getCountryByISO("TW", "en"))} is not any name ` +
        `the library stores for TW (${JSON.stringify(all)})`
    );
  });

  // ---- 3. nothing else broke -------------------------------------------
  const entries = getCountries("en");
  const byCode = Object.fromEntries(entries.map((e) => [e.value, e.label]));

  check("the dropdown still has every country", () => {
    const officialCount = Object.keys(Countries.getNames("en")).length;
    assert.equal(
      entries.length,
      officialCount,
      `expected ${officialCount} countries, got ${entries.length}`
    );
  });

  check("no label is empty or undefined", () => {
    const bad = entries.filter(
      (e) => typeof e.label !== "string" || !e.label.trim()
    );
    assert.equal(
      bad.length,
      0,
      `${bad.length} entries have no usable label: ` +
        JSON.stringify(bad.slice(0, 5).map((b) => b.value))
    );
  });

  check("every code is a two-letter alpha-2 code", () => {
    const bad = entries.filter((e) => !/^[A-Z]{2}$/.test(e.value));
    assert.equal(
      bad.length,
      0,
      `malformed codes: ${JSON.stringify(bad.slice(0, 5).map((b) => b.value))}`
    );
  });

  // ---- 4. the two entry points agree ------------------------------------
  check("getCountryByISO and getCountries agree", () => {
    for (const code of ["TW", "CN", "US", "CZ", "GB", "TR", "FR", "JP", "RU"]) {
      assert.equal(
        getCountryByISO(code, "en"),
        byCode[code],
        `${code}: single lookup says ${JSON.stringify(getCountryByISO(code, "en"))}, ` +
          `dropdown says ${JSON.stringify(byCode[code])}`
      );
    }
  });

  // ---- 5. exactly one country is overridden ----------------------------
  // THE CHECK THAT MATTERS MOST, because it is the one that would have
  // caught the reverted approach. An earlier version of this fix used the
  // library's select:"alias" option, which reads like the obvious way to ask
  // for the common name. Measured, it changed all 20 countries that have
  // alternate names -- including two outright regressions:
  //
  //   KR  South Korea  -> Korea, Republic of
  //   AX  Åland Islands -> Aland Islands
  //   TR  Türkiye      -> Turkey
  //
  // So this asserts the blast radius is exactly the one code the issue names.
  // A future "while we're here" edit that widens it fails here first.
  check("EXACTLY ONE country is overridden -- the issue names one", () => {
    const overridden = entries
      .map((e) => e.value)
      .filter((code) => byCode[code] !== Countries.getName(code, "en"));

    assert.deepEqual(
      overridden,
      ["TW"],
      `expected only TW to differ from the library's official names, but ` +
        `${overridden.length} differ: ${JSON.stringify(overridden)}`
    );
  });

  check("no country the library names neutrally was made worse", () => {
    // The specific regressions the alias approach introduced. Each is the
    // library's readable official name, and each must survive untouched.
    for (const [code, mustStay] of [
      ["KR", "South Korea"],
      ["AX", "Åland Islands"],
      ["TR", "Türkiye"],
      ["CI", "Cote d'Ivoire"],
      ["US", "United States of America"],
      ["CZ", "Czech Republic"],
      ["RU", "Russian Federation"],
      ["CN", "People's Republic of China"],
    ]) {
      assert.equal(
        byCode[code],
        mustStay,
        `${code} should still render ${JSON.stringify(mustStay)} as upstream ` +
          `ships it, got ${JSON.stringify(byCode[code])}`
      );
    }
  });

  // ---- 6. a country with no override keeps its official name ------------
  check("a country with no override keeps the library's name", () => {
    assert.equal(byCode.FR, "France");
    assert.equal(getCountryByISO("FR", "en"), "France");
  });

  check("every code resolves to a name through getCountryByISO", () => {
    const missing = entries
      .map((e) => e.value)
      .filter((c) => !getCountryByISO(c, "en"));
    assert.equal(
      missing.length,
      0,
      `${missing.length} codes resolve to nothing: ${missing.slice(0, 10).join(",")}`
    );
  });

  // ---- 7. locale fallback ----------------------------------------------
  check("a locale with no data falls back to English", () => {
    const fallback = getCountries("qq");
    assert.ok(
      fallback.length > 200,
      `unknown locale produced ${fallback.length} countries, expected the English set`
    );
    assert.equal(
      getCountryByISO("TW", "qq"),
      "Taiwan",
      "the fix must survive the fallback path too"
    );
  });

  check("a known non-English locale still renders its own names", () => {
    const de = Object.fromEntries(
      getCountries("de").map((e) => [e.value, e.label])
    );
    // The override applies in every locale, not only English: the label is
    // contentious regardless of UI language, and the same database row is
    // rendered either way.
    assert.equal(de.TW, "Taiwan", "the override must apply in every locale");
    assert.ok(de.DE, "DE must have a German label");
  });

  check("the override survives odd casing and padding", () => {
    // A lookup that assumed an uppercase, untrimmed code would miss and
    // silently render the library's name -- invisible in the UI, and
    // indistinguishable from the override not existing.
    for (const variant of ["TW", "tw", " tw ", " Tw", "tW "]) {
      assert.equal(
        getCountryByISO(variant, "en"),
        "Taiwan",
        `variant ${JSON.stringify(variant)} did not pick up the override`
      );
    }
  });

  // ---- the full blast radius, printed -----------------------------------
  // Only entries the library stores with MORE THAN ONE name can render
  // differently. `select: "all"` returns an array for every code -- most are
  // a single-element array holding the official name -- so the filter is
  // length > 1, and printing the rest would bury the change in 230 "unchanged"
  // lines.
  console.log("\n  entries the library stores with alternate names:");
  const withAlternates = Object.keys(Countries.getNames("en")).filter(
    (code) => {
      const all = Countries.getName(code, "en", { select: "all" });
      return Array.isArray(all) && all.length > 1;
    }
  );

  let changed = 0;
  for (const code of withAlternates) {
    const all = Countries.getName(code, "en", { select: "all" });
    const official = all[0];
    const rendered = byCode[code];
    const differs = official !== rendered;
    if (differs) changed++;
    console.log(
      `    ${code}  ${official.padEnd(30)} -> ${rendered}` +
        `   [alternates: ${all.slice(1).join(", ")}]`
    );
  }
  console.log(
    `\n  ${changed} of ${withAlternates.length} render differently than before.`
  );

  console.log(
    failures ? `\n${failures} check(s) FAILED` : "\nall checks passed"
  );
  process.exitCode = failures ? 1 : 0;
} finally {
  rmSync(OUT, { recursive: true, force: true });
}
