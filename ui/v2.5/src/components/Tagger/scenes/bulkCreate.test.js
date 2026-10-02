// Tests for #3122's bulk-action scene selection. Run: node --test this file.
//
// Plain .js importing the .ts module -- the same approach as the mousetrapScope tests.
// `node --test` needs no toolchain, so these run anywhere the suite runs.

import assert from "node:assert/strict";
import { test } from "node:test";

import {
  chunk,
  isMissingMetadata,
  isUnorganized,
  selectForAction,
} from "./bulkCreate.ts";

// A SCENE WITH NOTHING FILLED IS BOTH "NEW" AND "MISSING".
//
// The three actions as the plan defines them (BACKLOG-17 section L) are NOT disjoint: "New"
// is `not organized`, "Missing" is `not organized and no metadata`, so an unorganized scene
// with no studio, date, performers or tags qualifies for both. That is the plan's own
// definitions overlapping, and it is worth pinning because "make them exclusive" is the
// obvious future tidy-up and would be a behaviour change disguised as a refactor.
const EMPTY = { organized: false };

test("an unorganized scene with no metadata is both new and missing", () => {
  assert.equal(isUnorganized(EMPTY), true);
  assert.equal(isMissingMetadata(EMPTY), true);

  const scenes = [{ id: "a", ...EMPTY }];
  assert.deepEqual(selectForAction(scenes, "new"), ["a"]);
  assert.deepEqual(selectForAction(scenes, "missing"), ["a"]);
  assert.deepEqual(selectForAction(scenes, "all"), ["a"]);
});

// ANY ONE FILLED FIELD IS ENOUGH TO STOP A SCENE BEING "MISSING".
//
// The failure this prevents: checking only `organized`, which is the obvious one-field
// implementation and which would classify an organized scene with no studio or date as
// complete. Each field is asserted separately, because the bug is per-field, not in the
// aggregate.
test("one filled field is enough to stop a scene being missing", () => {
  const variants = [
    { organized: true },
    { organized: false, studio: "Studio A" },
    { organized: false, date: "2024-01-02" },
    { organized: false, performerNames: ["Someone"] },
    { organized: false, tagNames: ["A Tag"] },
  ];

  for (const v of variants) {
    assert.equal(
      isMissingMetadata(v),
      false,
      `should not be missing: ${JSON.stringify(v)}`
    );
  }
});

// AND AN EMPTY LIST IS NOT A FILLED FIELD.
//
// `performerNames: []` is what a fragment with no performers actually deserialises to, so an
// implementation reading `.length` on undefined needs this to be false rather than a crash.
// It must not be treated as "has metadata".
test("empty lists do not count as filled metadata", () => {
  assert.equal(
    isMissingMetadata({ organized: false, performerNames: [], tagNames: [] }),
    true
  );
});

// "ALL" IS EVERY SCENE, AND "NEW" IS EXACTLY THE UNORGANIZED ONES.
test("all and new select as the plan defines them", () => {
  const scenes = [
    { id: "1", organized: false },
    { id: "2", organized: true },
    { id: "3", organized: false },
  ];

  assert.deepEqual(selectForAction(scenes, "all"), ["1", "2", "3"]);
  assert.deepEqual(selectForAction(scenes, "new"), ["1", "3"]);
});

// AND "MISSING" IS A SUBSET OF "NEW", which is the plan's definitions again.
test("missing is a subset of new", () => {
  const scenes = [
    { id: "1", organized: false }, // both
    { id: "2", organized: false, studio: "Studio A" }, // new only
    { id: "3", organized: true }, // neither
    { id: "4", organized: false, date: "2024-01-02" }, // new only
  ];

  const isNew = selectForAction(scenes, "new");
  const isMissing = selectForAction(scenes, "missing");

  assert.deepEqual(isNew, ["1", "2", "4"]);
  assert.deepEqual(isMissing, ["1"]);

  for (const id of isMissing) {
    assert.ok(
      isNew.includes(id),
      `missing must be a subset of new, but ${id} is not`
    );
  }
});

// IDS, NOT COUNTS.
//
// The plan's rule ("no count without names") exists because a count is satisfied by ANY row,
// so an absence can be masked by an unrelated one appearing. These functions therefore return
// the identifiers themselves, and every count a caller shows is a `.length` of one of these.
test("selection returns scene identifiers so a count can be inspected", () => {
  const scenes = [
    { id: "scene-a", organized: false },
    { id: "scene-b", organized: true },
  ];
  assert.deepEqual(selectForAction(scenes, "all"), ["scene-a", "scene-b"]);
});

// CHUNKING: THE BOUNDARY CASES ARE WHERE A LOOP IS WRONG.
test("chunk splits at the size boundary and keeps every item", () => {
  const items = [1, 2, 3, 4, 5];

  assert.deepEqual(chunk(items, 2), [
    [1, 2],
    [3, 4],
    [5],
  ]);
  assert.deepEqual(chunk(items, 5), [[1, 2, 3, 4, 5]]);
  assert.deepEqual(chunk(items, 1), [[1], [2], [3], [4], [5]]);

  // Exact multiple: no trailing empty batch.
  assert.deepEqual(
    chunk([1, 2, 3, 4], 2),
    [
      [1, 2],
      [3, 4],
    ]
  );

  // Every item appears exactly once, in order, for a range of sizes.
  for (const size of [1, 2, 3, 4, 5, 6, 7]) {
    const flat = chunk(items, size).flat();
    assert.deepEqual(flat, items, `size ${size} lost or reordered items`);
  }
});

test("chunk of nothing is no batches, not one empty batch", () => {
  // An empty batch would be sent as a mutation with nothing to do, which the API rejects --
  // or worse, accepts as a no-op that the caller then reports as work done.
  //
  // The naive `chunk([], n)` happens to return `[]` too, because the loop body never runs --
 // so asserting the value alone cannot tell the guard from its absence. The length IS the
  // point, and a mutation removing the explicit guard survived until this was rewritten that
  // way. Both spellings must agree; that is what is asserted.
  const empty = chunk([], 10);
  assert.deepEqual(empty, []);
  assert.equal(empty.length, 0, "an empty input must produce no batches at all");
});

test("chunk rejects a non-positive size rather than looping forever", () => {
  // `size = 0` in a `for (i += size)` loop never terminates. A RangeError is the whole
  // point: this is a programming error, and it must not hang the app.
  for (const bad of [0, -1]) {
    assert.throws(() => chunk([1, 2, 3], bad), RangeError, `size ${bad}`);
  }
});
