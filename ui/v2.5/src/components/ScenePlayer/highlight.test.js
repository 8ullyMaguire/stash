import test from "node:test";
import assert from "node:assert/strict";
import * as highlight from "./highlight.js";
import * as navigation from "./navigation.js";
import { clampIndex, moveHighlight, chosenIndex } from "./highlight.js";
import { isNavigationEvent, chooseScene } from "./navigation.js";

// Pure arithmetic, no DOM and no React -- `mousetrapScope.test.js` explains why this repo
// tests properties rather than mounted implementations. The mutants in
// `docs/mutate_4326.py` target exactly the lines below; each test names the mutant it
// kills, so a survivor can be read as "this test stopped covering that mutation".
//
// `navigation.js` is imported as a NAMESPACE as well as by name: T1 has to assert on the
// module's export list, and that is only possible with a namespace object.

test("T1 moveHighlight takes no callback, so browsing cannot navigate", () => {
  // The ARITY is the assertion. A test that only checked return values would pass even if
  // `moveHighlight` had grown a fourth `onSceneChosen` parameter and called it -- the
  // returned number would be unchanged. Asserting the signature makes "arrow keys do not
  // navigate" structural instead of a promise.
  assert.equal(moveHighlight.length, 3, "moveHighlight takes (current, delta, length) only");
  assert.equal(clampIndex.length, 2);
  assert.equal(chosenIndex.length, 2);

  // And no key predicate in this module can name navigation: there is no such export.
  const names = Object.keys(highlight);
  assert.ok(
    !names.some((n) => /nav|choose|onScene/i.test(n)),
    `highlight.js must contain no navigation export, found ${JSON.stringify(names)}`
  );

  // navigation.js holds the ONLY way out, and exactly one export can call back.
  const navNames = Object.keys(navigation);
  assert.deepEqual(
    navNames.sort(),
    ["chooseScene", "isNavigationEvent"],
    "navigation.js exports exactly these; a third export could navigate from anywhere"
  );
});

test("T2 the navigation predicate admits exactly enter", () => {
  assert.equal(isNavigationEvent("enter"), true);
  // Exact membership, not "not in the browse set": a permissive rule would let any future
  // key navigate without anyone deciding it should.
  for (const key of ["down", "up", "j", "k", "escape", "e", "r", "shift", "tab", ""]) {
    assert.equal(isNavigationEvent(key), false, `${key} must not navigate`);
  }
});

test("T3 the highlight clamps at both ends and never wraps", () => {
  // Clamping is a decision, not an implementation detail. Wrapping is invisible to a user
  // (it looks like the key did nothing) and makes choosing feel broken.
  assert.equal(moveHighlight(0, -4, 3), 0, "up at the top holds, does not wrap to the end");
  assert.equal(moveHighlight(2, 4, 3), 2, "down at the bottom holds");
  assert.equal(moveHighlight(1, 1, 3), 2);
  assert.equal(moveHighlight(1, -1, 3), 0);

  // The mutation that replaces clamping with `(i + d) % length` MUST die here:
  //   moveHighlight(0, -4, 3) -> ((-4 % 3) + 3) % 3 == 2
  assert.notEqual(moveHighlight(0, -4, 3), 2);

  // Degenerate lists hold still rather than producing NaN.
  assert.equal(moveHighlight(0, 1, 0), 0, "an empty list has no index to move");
  assert.equal(moveHighlight(0, 1, -1), 0, "a negative length is empty, not an error");
  assert.equal(moveHighlight(NaN, 1, 3), 1, "a NaN highlight starts at the top");
});

test("T4 an empty list yields no choice rather than index 0", () => {
  assert.equal(chosenIndex(0, 0), null, "empty list must choose nothing");
  assert.equal(chosenIndex(3, 0), null);
  assert.equal(chosenIndex(0, -1), null);

  assert.equal(chosenIndex(0, 3), 0);
  assert.equal(chosenIndex(2, 3), 2, "the highlight is the choice");
  assert.equal(chosenIndex(9, 3), 2, "an out-of-range highlight is clamped, never thrown");
});

test("T7 choosing calls onSceneChosen THEN onClose, once each", () => {
  const calls = [];
  const chosen = chooseScene(1, 3, (i) => calls.push(["chosen", i]), () => calls.push(["closed"]));

  assert.equal(chosen, true, "a selection was made");
  assert.deepEqual(calls, [["chosen", 1], ["closed"]], "chosen first, then closed, exactly once each");

  // An empty list still closes -- the user asked to leave -- but chooses nothing.
  const empty = [];
  const ok = chooseScene(0, 0, (i) => empty.push(["chosen", i]), () => empty.push(["closed"]));
  assert.equal(ok, false, "nothing was chosen");
  assert.deepEqual(empty, [["closed"]], "no scene that is not there is ever passed on");
});