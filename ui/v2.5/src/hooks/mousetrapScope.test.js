// #2833 -- `e` collides with subpages, and the losing page's key is lost, not restored.
//
// ## THE DEFECT, as measured
//
// `e` is bound by 16 components into ONE global mousetrap registry: 8 list components
// (`SceneList`, `TagList`, ...) and 8 detail components (`Scene`, `Tag`, `Performer`,
// `Image`, `Group`, `Gallery`, `Studio`, `SceneMarkerList`). `d d` is bound 16 times,
// `p r` nine.
//
// From mousetrap 1.6.5's own source, not from its docs:
//
//   unbind = function(keys, action) {
//       return self.bind.call(self, keys, function() {}, action);   // a NOOP
//   };
//
// and `_bindSingle` first calls `_getMatches(...)` to REMOVE an existing match, then
// PUSHES the new callback. So for a plain key the array holds exactly one entry:
//
//   parent binds 'e'   -> [parent]
//   child  binds 'e'   -> [child]        (parent's entry was removed)
//   child  unbinds 'e' -> [noop]         (child's entry removed, noop pushed)
//
// The noop does not restore the previous owner, and nothing re-registers it. The parent
// is now holding a key that does nothing -- which is precisely the reported "works every
// other time", and precisely why navigating away and back fixes it (the remount re-runs
// the parent's `bind`).
//
// THE POINT OF THIS FILE: the mechanism above is a property of the REGISTRY, so it is
// tested against the real library under `node --test`, with a DOM shim, and dispatch
// through mousetrap's own `trigger`. A hand-written model of bind/unbind would only
// prove the model agrees with itself.
//
// It does NOT test the fix. React is not loaded here on purpose: what must not regress
// is the registry-level property below, and a test that mounted 16 components would
// assert the fix's IMPLEMENTATION rather than the property that made the bug a bug.
// The fix's own test lives with the fix.

import test from "node:test";
import assert from "node:assert/strict";
import { freshMousetrap } from "./mousetrapDOMShim.js";

// The defect itself, pinned. If mousetrap ever changes unbind to genuinely remove the
// entry, this fails -- and that failure would be GOOD NEWS: the underlying library
// defect is gone and the binding layer's own bookkeeping can be simplified.
//
// ## WHY THIS ASSERTS ON BEHAVIOUR AND NOT ON `_callbacks`
//
// The obvious thing to assert is "the slot holds a noop". That assertion is WRONG here,
// and the first version of this file made it, which cost a run.
//
// `Mousetrap.init()` replaces every public method on the export with a closure over an
// instance it builds itself, and it SKIPS any method whose name starts with `_`. So
// `Mousetrap._callbacks` on the export is a DIFFERENT object from the one `bind` writes
// to. Measured: after `M.bind("e", ...)` the facade's `_callbacks` is still `{}`, while
// `M.trigger("e")` demonstrably fires. Reading that field tells you nothing about the
// registry, and a test asserting on it passes or fails for reasons unrelated to the bug.
//
// The observable that IS honest: after a child takes `e` and gives it back, the parent's
// handler is gone and pressing the key does nothing. That is the whole defect, and it is
// stated in terms a user would recognise.
test("mousetrap.unbind poisons the slot rather than restoring the previous owner", () => {
  const M = freshMousetrap();
  const log = [];

  M.bind("e", () => log.push("parent"));
  M.bind("e", () => log.push("child")); // a subpage mounts
  M.unbind("e"); // ... and unmounts

  M.trigger("e");
  assert.deepEqual(
    log,
    [],
    "the parent's callback must not fire after a child took the key and gave it back: " +
      "unbind is bind(noop), so the slot holds a no-op instead of the previous owner"
  );
});

// The consequence a user actually reports. Written as its own test because the symptom
// ("works every other time") is the thing that was filed, and it is the symptom -- not
// the slot contents -- that has to stay fixed.
test("a parent loses its key when a child mounts and unmounts, and recovers only on remount", () => {
  const M = freshMousetrap();
  const log = [];

  M.bind("e", () => log.push("parent"));
  M.bind("e", () => log.push("child"));
  M.unbind("e");

  M.trigger("e");
  assert.deepEqual(log, [], "broken while the parent is still mounted");

  // Re-entering the page remounts the parent, whose effect re-binds. This is the
  // reporter's own workaround, and it is why the bug read as intermittent.
  M.bind("e", () => log.push("parent"));
  M.trigger("e");
  assert.deepEqual(
    log,
    ["parent"],
    "re-binding restores it, which is why 'go back and re-enter' fixes it"
  );
});

// A DIFFERENT KEY IS UNAFFECTED, so the scope of the defect is pinned.
//
// Without this, a fix that simply stopped binding globally -- removing `e` from every
// list component -- would pass the tests above, and the fix must not be allowed to
// work by breaking shortcuts outright.
test("only the shared key is affected; an unshared key survives a neighbour's churn", () => {
  const M = freshMousetrap();
  const log = [];

  M.bind("e", () => log.push("parent-e"));
  M.bind("z", () => log.push("parent-z"));

  M.bind("e", () => log.push("child-e"));
  M.unbind("e");

  M.trigger("z");
  assert.deepEqual(log, ["parent-z"], "'z' was never contended, so it still works");
});