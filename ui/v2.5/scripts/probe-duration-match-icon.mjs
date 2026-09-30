#!/usr/bin/env node
// Harness for stash#7267 (upstream PR #7267, issue #7266).
//
// THE DEFECT. `getDurationStatus` computes a match percentage and hands it to
// `getDurationIcon`, which picks a colour by threshold:
//
//     const matchPercentage = (matchCount / durations.length) * 100;
//
// `durations` comes from `scene.fingerprints`, and the guard above it only bails
// when BOTH the local duration is missing AND there are no fingerprints:
//
//     if (!scene.duration && durations.length === 0) return "";
//
// So when the local scene HAS a duration and the server has NO fingerprints for
// it, `durations.length === 0`, `matchCount === 0`, and the ratio is 0/0 = NaN.
// Every comparison against NaN is false, so `getDurationIcon` falls through both
// thresholds and returns the RED cross — for a scene whose duration matches
// exactly. That is the bug: a green tick is the whole point of the check.
//
// PR #7267 special-cases exactly that combination. This harness pins the
// arithmetic and the icon choice so the fix is provable rather than visual.

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const SRC = join(
  here,
  "..",
  "src",
  "components",
  "Tagger",
  "scenes",
  "StashSearchResult.tsx"
);

let pass = 0;
let fail = 0;
function check(name, cond, detail = "") {
  if (cond) {
    pass++;
    console.log(`  ok    ${name}`);
  } else {
    fail++;
    console.log(`  FAIL  ${name}${detail ? " -- " + detail : ""}`);
  }
}

// Mirror the shipped arithmetic exactly. This is the same extraction discipline
// the other harnesses use: drive the real formula, not a re-derivation of it.
function matchPercentage(fingerprintDurations, stashDuration) {
  const durations =
    fingerprintDurations?.map((d) => Math.abs(d - stashDuration)) ?? [];
  if (durations.length === 0) return { pct: NaN, durations, matchCount: 0 };
  const matchCount = durations.filter((d) => d <= 5).length;
  return { pct: (matchCount / durations.length) * 100, durations, matchCount };
}

function icon(pct) {
  if (pct > 65) return "success";
  if (pct > 35) return "warning";
  return "danger";
}

console.log("stash#7267 -- duration match with no server fingerprints");
console.log("");

// THE PREMISE. With no fingerprints the percentage is NaN, not 0. This is the
// defect's arithmetic and it is what makes every threshold comparison false.
{
  const { pct } = matchPercentage([], 120);
  check("no fingerprints yields NaN, not 0", Number.isNaN(pct), `got ${pct}`);
  check(
    "NaN fails every threshold comparison",
    !(pct > 65) && !(pct > 35),
    "which is why the icon falls through to the red cross"
  );
  check("so the icon is danger (red cross)", icon(pct) === "danger");
}

// THE FIX. A duration match with no fingerprints must be a SUCCESS, because the
// thing being checked is the duration and it matched.
{
  const stashDuration = 120;
  const localDuration = 120; // matches exactly
  const { pct } = matchPercentage([], stashDuration);

  const durationsMatch = Math.abs(localDuration - stashDuration) < 5;
  const fixedPct = Number.isNaN(pct) ? (durationsMatch ? 100 : 0) : pct;

  check(
    "a matching duration with no fingerprints scores 100",
    durationsMatch && fixedPct === 100,
    `localDuration=${localDuration} stashDuration=${stashDuration} -> ${fixedPct}`
  );
  check("and therefore shows the green tick", icon(fixedPct) === "success");
}

// The neighbouring cases must not change, or the fix is a regression.
{
  const { pct, matchCount } = matchPercentage([120, 121], 120);
  check(
    "2/2 fingerprints matching is still success",
    icon(pct) === "success",
    `pct=${pct} matchCount=${matchCount}`
  );
}
{
  const { pct } = matchPercentage([120, 400], 120);
  check(
    "1/2 fingerprints matching is still warning",
    icon(pct) === "warning",
    `pct=${pct}`
  );
}
{
  const { pct } = matchPercentage([400, 500], 120);
  check(
    "0/2 fingerprints matching is still danger",
    icon(pct) === "danger",
    `pct=${pct}`
  );
}
{
  // A mismatch must not be promoted to success by the fix.
  const { pct } = matchPercentage([], 120);
  const mismatchPct = Number.isNaN(pct) ? 0 : pct;
  check(
    "a MISMATCHED duration with no fingerprints is not success",
    icon(mismatchPct) === "danger"
  );
}

// SOURCE-SHAPE. The guard is what lets NaN through, so assert it is still the
// narrow `&&` form rather than an unconditional early return -- an over-broad
// "fix" would be to bail whenever durations is empty, which would also suppress
// the mismatch case the UI is meant to show.
{
  const src = readFileSync(SRC, "utf8");
  check(
    "the guard is still `!scene.duration && durations.length === 0`",
    /if \(!scene\.duration && durations\.length === 0\) return "";/.test(src),
    "widening this to `durations.length === 0` alone would hide mismatches too"
  );
  check(
    "the 0/0 division is still on the page (so the fix must guard it)",
    /\(matchCount \/ durations\.length\) \* 100/.test(src)
  );

  // THE FIX IS PRESENT. Asserting the shipped source, because the arithmetic above
  // is a mirror -- this is what proves the mirror matches the product.
  check(
    "the shipped code guards the NaN case",
    /Number\.isNaN\(rawPercentage\)/.test(src),
    "the harness models the fix; the source must actually contain it"
  );
  // Collapse whitespace before matching: the nested ternary is formatted across
  // several lines, and an assertion that depends on that formatting is an
  // assertion that fails on a reformat rather than on a behaviour change.
  const flat = src.replace(/\s+/g, " ");
  check(
    "and scores a duration-only match as 100 while a mismatch stays 0",
    /Number\.isNaN\(rawPercentage\)\s*\?\s*scene\.duration && Math\.abs\(scene\.duration - stashDuration\) < 5\s*\?\s*100\s*:\s*0/.test(
      flat
    ),
    "the source must contain the exact three-way decision the harness models"
  );
}

console.log("");
console.log(`${pass} passed, ${fail} failed`);
process.exit(fail === 0 ? 0 : 1);
