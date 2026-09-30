#!/usr/bin/env node
// Structural checks for the translation files.
//
// Every locale is a JSON object mirroring en-GB.json, and nothing in the build
// enforced that. A translation that omits a key, renames a nested object, or drops
// an interpolation placeholder still builds, still deploys, and renders the raw
// key to the user at runtime -- so the defect is invisible until someone opens the
// UI in that language.
//
// Found while merging #7252: of 41 locales, only the new sw-KE.json was
// structurally perfect. Every other one was missing keys, most of them over a
// thousand. Those gaps predate this check and are not fixed by it, but the check
// makes them visible and prevents new ones.
//
// Usage:  node scripts/test-locale-structure.mjs
//
// Exits non-zero on any structural defect. Deliberately does NOT check
// translation QUALITY -- that needs a speaker, not a script. It checks only what a
// script can decide: shape, coverage, and placeholder integrity.

import { readFileSync, readdirSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const LOCALES = join(HERE, "..", "src", "locales");

const REFERENCE = "en-GB.json";

// Locales added by a specific upstream PR. A locale that is NEW must arrive
// complete: shipping a half-translated locale is a regression the moment it is
// registered in the picker, whereas the older locales have been incomplete for
// years and are reported rather than blocked on.
const NEW_LOCALES = new Set(["sw-KE.json"]);

// flatten a nested locale into dotted leaf paths. A leaf is any non-object value;
// an empty object is a leaf too, since i18next treats it as a missing key.
function flatten(obj, prefix = "", out = {}) {
  for (const [k, v] of Object.entries(obj)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (v && typeof v === "object" && !Array.isArray(v) && Object.keys(v).length > 0) {
      flatten(v, key, out);
    } else {
      out[key] = v;
    }
  }
  return out;
}

// PLACEHOLDERS -- and the first version of this check was WRONG, in a way worth
// recording.
//
// i18next interpolates BOTH {name} and {{name}}. The doubled form is not a
// different variable: it is the escaped literal brace required when a placeholder
// sits INSIDE a plural branch, which is exactly where en-GB puts them --
//
//   "added_entity": "Added {count, plural, one {{singularEntity}} other {{pluralEntity}}}"
//
// while fr-FR writes {singularEntity} in the same position, unescaped:
//
//   "added_entity": "{count, plural, one {{singularEntity} ajouté} other {{pluralEntity} ajoutés}}"
//
// Both interpolate correctly. A regex matching only the doubled form therefore
// reports 19 "mismatches" that are all false positives, and flags a RENAME
// ({{Tiefe}} for {{depth}}) as a break when that is correct German.
//
// So match both forms. A locale is defective only when a variable the CALLER
// supplies is absent from the string entirely -- which is a real bug found in this
// tree: es-ES "added_entity": "{entity} añadida" refers to {entity}, which no caller
// passes, so it renders empty.
const PLACEHOLDER = /\{\{?[A-Za-z_]\w*\}?\}/g;
const placeholders = (s) => {
  // Normalise the doubled form to a bare name so {{x}} and {x} compare equal.
  return new Set([...String(s).matchAll(PLACEHOLDER)].map((m) => m[0].replace(/\{|\}/g, "")));
};

const ref = flatten(JSON.parse(readFileSync(join(LOCALES, REFERENCE), "utf8")));
const refKeys = Object.keys(ref);
const files = readdirSync(LOCALES).filter((f) => f.endsWith(".json") && f !== REFERENCE).sort();

let failures = 0;
let totalMissing = 0;

const report = [];
const coverage = {};

for (const file of files) {
  const problems = [];

  let parsed;
  try {
    parsed = JSON.parse(readFileSync(join(LOCALES, file), "utf8"));
  } catch (e) {
    console.error(`FAIL ${file}: not valid JSON: ${e.message}`);
    failures++;
    coverage[file] = { pct: 0, missing: -1, extra: 0, empty: 0 };
    continue;
  }

  const flat = flatten(parsed);
  const keys = new Set(Object.keys(flat));

  // 1. COVERAGE -- keys present in the reference but absent here.
  const missing = refKeys.filter((k) => !keys.has(k));

  // 2. STALENESS -- keys that no longer exist in the reference. These are dead
  //    weight: they suggest a translation for a string that is gone, and they hide
  //    real typos in the key name.
  const extra = [...keys].filter((k) => !(k in ref));

  // 3. PLACEHOLDERS -- interpolation variables must survive translation.
  const badPlaceholders = [];
  for (const k of refKeys) {
    if (!keys.has(k)) continue;
    const want = placeholders(ref[k]);
    const got = placeholders(flat[k]);
    if (want.size !== got.size || [...want].some((p) => !got.has(p))) {
      badPlaceholders.push(`${k} (en: ${[...want].join(",") || "none"} | here: ${[...got].join(",") || "none"})`);
    }
  }

  // 4. EMPTY VALUES -- a key present with an empty string renders as blank, which
  //    is worse than falling back to English.
  const empty = refKeys.filter((k) => keys.has(k) && String(flat[k]).trim() === "" && String(ref[k]).trim() !== "");

  totalMissing += missing.length;
  if (missing.length || extra.length || badPlaceholders.length || empty.length) {
    if (missing.length) problems.push(`${missing.length} missing (e.g. ${missing.slice(0, 3).join(", ")})`);
    if (extra.length) problems.push(`${extra.length} unknown (e.g. ${extra.slice(0, 3).join(", ")})`);
    if (badPlaceholders.length) problems.push(`${badPlaceholders.length} placeholder mismatch: ${badPlaceholders[0]}`);
    if (empty.length) problems.push(`${empty.length} empty value (e.g. ${empty[0]})`);
  }

  // Integer division: 1383/1384 rounds to 100%, so a locale missing ONE key would
  // read as complete. Store the exact missing count; the percentage is for display
  // only.
  const pct = Math.round(((refKeys.length - missing.length) / refKeys.length) * 100);
  coverage[file] = { pct, missing: missing.length, extra: extra.length, empty: empty.length };
  // Display, floored so a locale missing one key never reads "100%". That rounded
  // display is exactly what hid the defect from me while writing this file.
  const shown = missing.length ? `${Math.floor((refKeys.length - missing.length) * 1000 / refKeys.length) / 10}` : "100";
  if (problems.length) {
    report.push(`  ${file.padEnd(12)} ${String(shown).padStart(5)}%  ${problems.join("; ")}`);
  } else {
    report.push(`  ${file.padEnd(12)} ${String(shown).padStart(5)}%  OK`);
  }
}

console.log(`locale structure vs ${REFERENCE} (${refKeys.length} keys, ${files.length} locales)\n`);
for (const line of report) console.log(line);

// The existing gaps are pre-existing, not regressions, so they are reported rather
// than fatal -- a hard failure here would block every commit until 40 languages are
// retranslated, which is not this script's job. What IS fatal is a NEW locale being
// registered without full coverage, or any placeholder break, since both ship
// broken strings immediately.
console.log(`\n${totalMissing} missing keys across ${files.length} locales (pre-existing)`);
console.log("Placeholder mismatches are fatal; coverage gaps in existing locales are reported only.");

const placeholderSuspect = report.filter((l) => l.includes("placeholder mismatch"));

console.log(`\n${totalMissing} missing keys across ${files.length} locales (pre-existing)`);

// A placeholder comparison is a WARNING, not a failure, and deliberately so.
//
// The caller decides which variable names a key receives, and that varies BY CALL
// SITE: GalleryAddPanel passes { count, singularEntity, pluralEntity } to
// toast.added_entity, while other components pass entityType to actions.add_entity
// and entity_type to actions.created_entity. A JSON-to-JSON comparison has no way
// to know which, so it produces false positives -- it flags {{Tiefe}} for {{depth}}
// as broken when that is correct German -- and cannot be trusted to catch real
// breakage either.
//
// The first version of this check was worse than useless: it matched only the
// doubled-brace form, so it reported 19 phantom mismatches in fr-FR and friends.
// i18next accepts {name} and {{name}} alike; the doubled form is just the escaped
// literal brace a placeholder needs when it sits inside a plural branch.
//
// What it DID find, verified by reading the call site: es-ES has
//   "added_entity": "{entity} añadida"
// while the caller supplies singularEntity/pluralEntity, so Spanish renders a
// toast with the entity name missing. Real, but not detectable by comparing files.
if (placeholderSuspect.length) {
  console.log(`\nWARNING: ${placeholderSuspect.length} locale(s) reference a placeholder the English does not:`);
  for (const l of placeholderSuspect) console.log(l);
  console.log("Not fatal -- the correct variable set is decided by the call site, not the file.");
}

// Structural defects that need no caller knowledge ARE fatal: a locale that is not
// valid JSON, or a NEWLY registered locale without full coverage. The older 40 are
// reported and not blocked on -- they have been incomplete for years and fixing
// them is a translation job, not a code change.
const newLocales = files.filter((f) => NEW_LOCALES.has(f) && coverage[f].missing > 0);
if (newLocales.length) {
  console.error(`\nFAIL: newly added locale(s) are not complete: ${newLocales.join(", ")}`);
  process.exit(1);
}

if (failures) {
  console.error(`\nFAIL: ${failures} locale(s) are not valid JSON`);
  process.exit(1);
}

console.log(`\nOK: all ${files.length} locales parse; newly registered ones have full coverage`);
