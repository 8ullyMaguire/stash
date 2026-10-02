// #2833 -- the scoping layer's own tests.
//
// ## WHY THESE ARE SEPARATE FROM mousetrapScope.test.js
//
// That file pins the DEFECT: what the raw registry does. This file pins the FIX, and the
// two must not be merged, because they make opposite claims about the same key. If they
// were one file, a change that altered the defect would also alter the fix's test, and
// the pair would stop being able to disagree.
//
// ## THE PROPERTY, stated once
//
// After a child binds and unbinds a contended key, the parent owns it again -- and
// pressing the key reaches the parent. Everything else here is either the mechanism for
// that or a guard against a fix that works by breaking shortcuts.
//
// Dispatch is mousetrap's own `trigger`, and the DOM shim is the same one the defect
// test uses, so both files exercise the same library the app ships.

import test from "node:test";
import assert from "node:assert/strict";
import { freshMousetrap } from "./mousetrapDOMShim.js";

// The shim clears `window.Mousetrap`, which is the same singleton the scope layer
// imports. So the scope module must be imported AFTER the first `freshMousetrap()` --
// hence this dynamic import inside the helper rather than a static one at the top, which
// would capture the pre-shim instance.
//
// Without this, the layer's `Mousetrap.bind` would write to a different registry than the
// one `trigger` dispatches through, and every test would pass vacuously.
let scope = null;
async function scoped() {
  if (!scope) {
    freshMousetrap(); // install the DOM and reset the singleton BEFORE the import
    scope = await import("./mousetrapScope.js");
  }
  return scope;
}

async function press(keys, M) {
  const log = [];
  M.trigger(keys);
  return log;
}

// THE FIX. The parent gets its key back when the child leaves.
test("a parent regains a contended key when a child unmounts", async () => {
  const s = await scoped();
  const M = freshMousetrap();
  s.resetScopedBindings();

  const fired = [];
  const parent = s.bindScoped("e", () => fired.push("parent"));
  const child = s.bindScoped("e", () => fired.push("child"));

  M.trigger("e");
  assert.deepEqual(fired, ["child"], "the most recently bound handler owns the key");

  child();

  M.trigger("e");
  assert.deepEqual(
    fired,
    ["child", "parent"],
    "after the child unmounts the parent owns 'e' again -- this is #2833"
  );

  parent();
});

// And the subpage behaviour the report actually asks for: a subpage CAN override.
test("a subpage's handler wins over the page behind it", async () => {
  const s = await scoped();
  const M = freshMousetrap();
  s.resetScopedBindings();

  const fired = [];
  const list = s.bindScoped("e", () => fired.push("list"));
  const detail = s.bindScoped("e", () => fired.push("detail"));

  M.trigger("e");
  assert.deepEqual(fired, ["detail"], "the subpage wins");

  detail();
  M.trigger("e");
  assert.deepEqual(fired, ["detail", "list"], "and the list gets it back on the way out");

  list();
});

// THREE DEEP, which is the real nesting: list -> route -> detail panel.
test("a key passes down and back up a three-level nest", async () => {
  const s = await scoped();
  const M = freshMousetrap();
  s.resetScopedBindings();

  const fired = [];
  const a = s.bindScoped("e", () => fired.push("a"));
  const b = s.bindScoped("e", () => fired.push("b"));
  const c = s.bindScoped("e", () => fired.push("c"));

  M.trigger("e");
  assert.deepEqual(fired, ["c"]);

  c();
  M.trigger("e");
  assert.deepEqual(fired, ["c", "b"]);

  b();
  M.trigger("e");
  assert.deepEqual(fired, ["c", "b", "a"], "unmounting out of order unwinds correctly");

  a();
});

// A NO-OP FIX MUST FAIL HERE. If the layer never registered anything with mousetrap, or
// registered a dispatcher that always did nothing, the three tests above would still pass
// on the stack bookkeeping alone -- the stack is a plain array, and `trigger` would be
// reaching nothing at all.
//
// So this asserts the registry is actually WIRED: one live mousetrap binding per
// contested key, dispatching to the stack's top.
test("the layer is wired into mousetrap, not just into its own bookkeeping", async () => {
  const s = await scoped();
  const M = freshMousetrap();
  s.resetScopedBindings();

  const fired = [];
  const wired = () => fired.push("wired");
  s.bindScoped("e", wired);
  M.trigger("e");

  assert.deepEqual(
    fired,
    ["wired"],
    "trigger() reached the handler through mousetrap, so the layer registered a real binding"
  );
  // Asserted through `currentOwner`, NOT through `M._callbacks`. The latter is mousetrap
  // private state that `reset()` clears wholesale, so asserting on it made this test
  // depend on whether an earlier test had reset -- an ordering dependency in a suite
  // whose whole point is that ordering must not matter.
  assert.equal(
    s.currentOwner("e"),
    wired,
    "and the layer agrees the handler owns the key"
  );
});

// A FIX THAT WORKS BY BREAKING SHORTCUTS MUST FAIL HERE.
//
// The cheapest way to stop the collision is to stop binding `e` on subpages. That makes
// the defect tests pass and the user loses the shortcut. So: a key that only ONE owner
// ever binds must still work, and must keep working after an unrelated contested key is
// torn down -- which is where a naive `Mousetrap.reset()`-style fix would take it out too.
test("an uncontested key is unaffected by churn on a neighbouring key", async () => {
  const s = await scoped();
  const M = freshMousetrap();
  s.resetScopedBindings();

  const fired = [];
  const parentE = s.bindScoped("e", () => fired.push("parent-e"));
  const soloZ = s.bindScoped("z", () => fired.push("solo-z"));

  const child = s.bindScoped("e", () => fired.push("child-e"));
  child();
  parentE();

  M.trigger("z");
  assert.deepEqual(
    fired,
    ["solo-z"],
    "'z' had one owner and never lost its key to a neighbour's teardown"
  );

  soloZ();
});

// IDEMPOTENCE. The 112 existing sites bind and unbind inside `useEffect`, and several of
// those effects have NO dependency array, so they re-bind on every render. A stack that
// grew per render would push the real handler out of the top position -- the shortcut
// would silently stop working after enough re-renders, which is the same class of bug as
// the one being fixed.
test("re-binding the same owner does not grow the stack", async () => {
  const s = await scoped();
  const M = freshMousetrap();
  s.resetScopedBindings();

  const fired = [];
  const owner = () => fired.push("owner");
  const unbind = s.bindScoped("e", owner);

  // A useEffect with no dep array rebinds every render.
  for (let i = 0; i < 25; i++) {
    s.bindScoped("e", owner, owner);
  }

  M.trigger("e");
  assert.deepEqual(
    fired,
    ["owner"],
    "the handler fires exactly once -- the 25 rebinds replaced in place, not stacked"
  );

  // AND THE STACK DID NOT GROW. This is the assertion the mutant survived, and the
  // reason is worth recording: firing once is NOT sufficient evidence. Under
  // append-always the handler still fired exactly once -- every duplicate dispatches to
  // the SAME callback, so the observable behaviour was identical -- while the stack grew
  // to depth 6. A test that only counted firings would pass against a layer leaking an
  // entry per render, which is precisely the unbounded growth this code exists to avoid.
  assert.equal(
    s.scopedShortcutReport()[0].depth,
    1,
    "25 rebinds from one owner left ONE entry: a duplicate per render would grow the " +
      "stack without bound while still firing once per keypress"
  );

  unbind();
  s.resetScopedBindings();
});

// And unmounting twice must not corrupt the stack, because React runs a cleanup on
// StrictMode's double-invoke and a stale cleanup removing a live handler is the same
// silent breakage.
test("unbinding twice is harmless", async () => {
  const s = await scoped();
  const M = freshMousetrap();
  s.resetScopedBindings();

  const fired = [];
  const parent = s.bindScoped("e", () => fired.push("parent"));
  const child = s.bindScoped("e", () => fired.push("child"));

  child();
  child(); // StrictMode / a stale cleanup

  M.trigger("e");
  assert.deepEqual(
    fired,
    ["parent"],
    "the double-unbind did not take the parent's handler with it"
  );

  parent();
});

// A handler that unbinds ITSELF mid-dispatch. `d d` and `c c` open dialogs whose
// handlers then unbind, and the dispatch loop copies the stack precisely so this is safe;
// without the copy, the splice would skip the next entry and a sibling key would be
// dropped.
test("a handler may unbind itself while dispatching", async () => {
  const s = await scoped();
  const M = freshMousetrap();
  s.resetScopedBindings();

  const fired = [];
  const below = s.bindScoped("e", () => fired.push("below"));
  // The self-unbinding handler is on TOP, which is the case that matters: it is the one
  // the dispatch loop is holding when it splices the stack it is iterating.
  let release = () => {};
  const top = s.bindScoped("e", () => {
    fired.push("top");
    release();
  });

  release = top;
  M.trigger("e");

  assert.deepEqual(
    fired,
    ["top"],
    "the self-unbinding handler ran, and the splice did not skip or double-dispatch"
  );

  // And the key is free again afterwards, rather than dead.
  M.trigger("e");
  assert.deepEqual(
    fired,
    ["top", "below"],
    "after it removed itself the handler below it took the key"
  );

  below();
});
// A KEY THAT GOES COMPLETELY IDLE, AND IS THEN BOUND AGAIN.
//
// This is the mutation gate's second survivor, so the reason it exists is worth stating:
// the gate mutated `wiredKeys(...).delete(keys)` -- the line that lets a re-bound key
// re-register its dispatcher -- and EVERY test still passed. Probing it by hand showed
// the mutation is genuinely observable: under it, re-binding a previously-emptied key
// fires NOTHING (`fired: []`), because `ensureInstalled` believed the dispatcher was
// still registered while the empty-stack teardown had already unbound it.
//
// So the line was real, reachable, and untested. That combination -- live code with no
// coverage -- is the only thing a mutation gate is for, and it is invisible to a coverage
// number.
//
// The scenario is not exotic: it is any page that unmounts its last handler for a key and
// another page later binds the same key. #2833's own navigation does exactly this.
test("a key bound again after going fully idle still works", async () => {
  const s = await scoped();
  const M = freshMousetrap();
  s.resetScopedBindings();

  const fired = [];

  // First owner, then nobody: the empty-stack teardown path.
  const first = s.bindScoped("e", () => fired.push("first"));
  first();
  assert.deepEqual(fired, [], "precondition: nothing was pressed yet");

  // A later page binds the same key.
  const second = s.bindScoped("e", () => fired.push("second"));
  M.trigger("e");

  assert.deepEqual(
    fired,
    ["second"],
    "the key was re-registered rather than assumed still-live -- without the " +
      "delete-wired-on-idle step the dispatcher is gone and the shortcut is dead"
  );

  second();
});
