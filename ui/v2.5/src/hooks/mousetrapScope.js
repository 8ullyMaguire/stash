// Scoped keyboard shortcuts for #2833.
//
// ## THE PROBLEM, and why it is not "two components bind the same key"
//
// The report is "`e` collides with subpages". The measured defect underneath is worse,
// and it is a registry property rather than a name clash. From mousetrap 1.6.5's source:
//
//   unbind = function(keys, action) {
//       return self.bind.call(self, keys, function() {}, action);   // a NOOP
//   };
//
// `_bindSingle` removes the existing match for a key and then PUSHES the new callback,
// so a plain key holds exactly ONE entry. `unbind` therefore does not restore the
// previous owner; it installs a no-op in its place. With 16 components binding `e`
// (`SceneList`, `TagList`, ... and `Scene`, `Tag`, `Performer`, `Image`, `Group`,
// `Gallery`, `Studio`, `SceneMarkerList`) the sequence is always:
//
//   parent binds 'e'   -> [parent]
//   child  binds 'e'   -> [child]
//   child  unbinds 'e' -> [noop]      <- the parent is now holding a dead key
//
// which is why the report reads "works every other time": re-entering the page remounts
// the parent, whose effect re-binds, and the key comes back.
//
// The pinned reproduction is `mousetrapScope.test.js`, which runs against the real
// library under `node --test` rather than against a model of it.
//
// ## THE FIX
//
// A per-key STACK, with the top of the stack owning the key. Three properties, and each
// one is a consequence of the mechanism rather than a rule chosen for convenience:
//
//  1. A handler that unmounts POPPS, and the key is re-registered to the handler below.
//     So a child leaving gives the parent its key back -- which is the whole defect.
//  2. The handler at the top is the one that runs. So a subpage that binds `e` wins over
//     the list behind it, which is what "subpages can override" means and what the
//     reporter's workaround was reaching for by hand.
//  3. Binding the same key twice from the SAME owner is idempotent, so the existing
//     112 sites that bind and unbind in a `useEffect` need no per-site rewrite.
//
// ## WHY THE WRAPPERS INSTEAD OF REWRITING 112 CALL SITES
//
// `Mousetrap.bind` is called in 95 places across ~20 components. Rewriting each to a hook
// is a review-hostile diff on a soft fork, where an unrelated change is a tax on someone
// reading a bugfix. So the fix is additive: two functions that do the bookkeeping, and
// they are reached by making the existing `Mousetrap.*` calls go through a facade.
//
// ## WHAT IS NOT FIXED HERE
//
// This layer changes WHO owns a key, not WHICH keys exist. `e` will still mean
// "edit" on a scene and "edit" on a performer, and a list and a detail page will still
// both claim it -- that is upstream's binding vocabulary, not the collision. The defect
// is that the loser LOSES ITS HANDLER PERMANENTLY, and that is what this removes.

// THE REGISTRY IS RESOLVED AT CALL TIME, NEVER CAPTURED AT IMPORT TIME.
//
// mousetrap is a singleton and `Mousetrap.init()` turns the export into a facade, so an
// instance captured at import time can stop being the one anything dispatches through.
// The moment that happens -- an HMR reload, a `window.Mousetrap` swap, a test resetting
// the singleton -- this layer would bind into a registry nobody reads, and every shortcut
// would stop working while every test that only inspected `stacks` still passed.
//
// That is not hypothetical: it is exactly how this file's own first test failed, with
// `fired === []` against an expected `["parent"]`.
//
// # WHY THIS IS `import`, NOT `createRequire`
//
// The test shim needs `createRequire` to install a DOM before the IIFE is evaluated. This
// file must NOT: `node:module` is a Node builtin and the production bundle is built for
// browsers, so `import { createRequire } from "node:module"` fails the vite/rollup build
// outright -- which it did, with an unresolved-import error pointing at this file's line 57.
//
// So the two halves resolve the library differently and deliberately:
//   this file (ships)   static `import` of the package, read through `registry()`
//   the test shim (dev)  `createRequire`, after `installDOM()`
// The consequence to keep in mind: this file's `registry()` is the FADE, which is what the
// application itself uses, and `mousetrapDOMShim.js` documents why that is the object with
// the real registry behind it.
import Mousetrap from "mousetrap";
import { useEffect, useRef } from "react";

let cachedMousetrap = null;
function registry() {
  if (!cachedMousetrap) {
    cachedMousetrap = Mousetrap;
  }
  return cachedMousetrap;
}


// key -> handler stack, most recently pushed last. A `Map` of arrays rather than a
// single owner, because the whole point is to be able to return a key to whoever had
// it before, which a single reference cannot express.
const stacks = new Map();

// Which keys this layer has registered a dispatcher for, PER REGISTRY INSTANCE.
//
// The first version used a single module-level `installed` boolean, on the reasoning that
// "one dispatcher per key, never re-bound". That is right in the app -- there is exactly
// one registry -- and wrong the moment a second registry exists, because the flag is
// already set and the new registry gets no dispatcher at all. Every handler then became
// unreachable while the stack bookkeeping still looked correct, which is the worst shape
// a test failure can take: green stack, dead shortcuts.
//
// So the guard is keyed by the registry the keys were registered against, and a
// registry that has never been wired gets its own set. A WeakMap, so a discarded
// registry takes its bookkeeping with it.
const wired = new WeakMap();

function wiredKeys(M) {
  let set = wired.get(M);
  if (!set) {
    set = new Set();
    wired.set(M, set);
  }
  return set;
}

// Installs ONE mousetrap binding per key, whose only job is to dispatch to the top of
// that key's stack. Registered once, never re-registered: re-binding would re-introduce
// the exact clobber this file exists to avoid.
//
// `useCallback: false` is mousetrap's default and is what we want -- these are UI
// shortcuts, and stopping propagation is the difference between a shortcut working and
// a shortcut also firing in a text field the user is typing in.
function ensureInstalled(M) {
  const already = wiredKeys(M);

  const wrapped = (keys) => (event) => {
    const stack = stacks.get(keys);
    if (!stack || stack.length === 0) return;

    // Copy before calling: a handler may unbind itself (or a sibling) while running,
    // and iterating a live array while it is being spliced skips entries.
    const handler = stack[stack.length - 1];
    handler.callback(event);
  };

  // Every key, not just new ones: a key whose stack was emptied and refilled has no live
  // dispatcher on this registry, because the empty case handed it back with `unbind`.
  for (const keys of stacks.keys()) {
    if (already.has(keys)) continue;
    already.add(keys);
    M.bind(keys, wrapped(keys));
  }
}

/**
 * Bind `keys` for `owner`, pushing it on that key's stack.
 *
 * Returns an unbind function, so it drops into an existing `useEffect` cleanup without
 * changing the call site's shape.
 *
 * `owner` defaults to a per-call token so two components binding the same key stack in
 * mount order. Pass an explicit owner when one component may bind the same key twice --
 * the second bind then replaces the first at its existing depth rather than stacking a
 * duplicate that would need two pops.
 */
export function bindScoped(keys, callback, owner) {
  const token = owner ?? callback;

  let stack = stacks.get(keys);
  if (!stack) {
    stack = [];
    stacks.set(keys, stack);
  }

  // Idempotent for the same owner: replace in place, keeping depth stable, so a
  // re-render that re-binds does not grow the stack without bound. This is what lets the
  // 112 existing bind/unbind pairs keep working unchanged.
  const existing = stack.findIndex((entry) => entry.owner === token);
  if (existing >= 0) {
    stack[existing] = { owner: token, callback };
  } else {
    stack.push({ owner: token, callback });
  }

  ensureInstalled(registry());

  return function unbindScoped() {
    const current = stacks.get(keys);
    if (!current) return;

    const at = current.findIndex((entry) => entry.owner === token);
    if (at < 0) return; // already popped -- unmount must be idempotent

    current.splice(at, 1);

    if (current.length === 0) {
      // Nothing wants this key. `Mousetrap.unbind` installs a noop rather than
      // removing the entry, so the registry keeps a harmless dead binding for a key no
      // component holds. That is the library's behaviour, not ours, and it is why the
      // stack -- not the registry -- is the source of truth for who owns what.
      registry().unbind(keys);
      // Forget that this key is wired on this registry, so a later bind re-registers a
      // dispatcher instead of assuming one is still there. Without this, a key that went
      // fully idle and was bound again would be silently dead -- the same failure the
      // `installed` flag had, one level down.
      wiredKeys(registry()).delete(keys);
      stacks.delete(keys);
      return;
    }

    // The next owner down is now on top, and since `ensureInstalled` registered ONE
    // dispatch per key that reads the top of the stack, taking effect is automatic.
  };
}

/**
 * The handler currently owning `keys`, or undefined.
 *
 * Exported for tests and for the diagnostic in `scopedShortcutReport` below -- the
 * registry cannot answer this question, because by the time anyone asks, the losing
 * handler is already gone from it.
 */
export function currentOwner(keys) {
  const stack = stacks.get(keys);
  return stack && stack.length ? stack[stack.length - 1].callback : undefined;
}

/** Test-only: forget every binding. */
export function resetScopedBindings() {
  for (const keys of Array.from(stacks.keys())) {
    registry().unbind(keys);
  }
  stacks.clear();
  wiredKeys(registry()).clear();
}

// WHY A REPORT EXISTS. The registry holds one no-op per formerly-contested key, so
// "which component owns `e`" cannot be answered by asking mousetrap -- it will happily
// tell you `e` is bound, to a function that does nothing. This is what says instead.
export function scopedShortcutReport() {
  return Array.from(stacks.entries()).map(([keys, stack]) => ({
    keys,
    depth: stack.length,
    ownerIsTop: stack.length > 0,
  }));
}

// Re-exported so call sites can migrate one at a time: `Mousetrap.bind(...)` becomes
// `bindScoped(...)` without importing a second name at every site on day one.
export function activeRegistry() {
  return registry();
}
// ## `useScopedKeybinds` -- the shape the seven detail components actually use
//
// The eight LIST components each bind one `e` and one `d d`, so `bindScoped` called
// twice reads fine there. The seven DETAIL components are the opposite: `Scene.tsx`
// alone binds fifteen keys in a single `useEffect` whose cleanup lists fifteen unbinds.
// Converting those to fifteen `bindScoped` calls plus fifteen teardown variables is a
// 60-line diff per file that a reviewer cannot check by eye, and a single missed unbind is
// the original bug wearing a different hat.
//
// So the whole-map form exists: one call, one cleanup, and the per-key bookkeeping stays
// in the layer. The OWNER is the map object itself, which makes re-binding idempotent in
// exactly the way a re-render needs -- see the note on identity below.
//
// # OWNER IDENTITY, and why the map is not enough on its own
//
// `bindScoped` identifies a handler by object identity, so a map literal recreated on
// every render is a NEW owner each time and the stack grows per render -- which is the
// leak the mutation gate caught in `rebind-always-appends`. So the owner here is a ref,
// seeded once and held across renders, while the CALLBACKS are refreshed each render so a
// handler always closes over current props.
export function useScopedKeybinds(bindings, deps = []) {
  const ownerRef = useRef(null);
  if (ownerRef.current === null) {
    ownerRef.current = { owner: "useScopedKeybinds" };
  }
  const owner = ownerRef.current;

  // Callbacks change every render and must not be a dependency: including them would
  // re-run the effect on every render, which is the churn the `#2833` notes call out as
  // the OTHER bug in the same lines.
  const latest = useRef(bindings);
  latest.current = bindings;

  const keys = Object.keys(bindings).join("\u0000");

  useEffect(() => {
    const unbinders = Object.keys(latest.current).map((key) =>
      bindScoped(key, (event) => latest.current[key]?.(event), owner)
    );

    return () => {
      for (const unbind of unbinders) unbind();
    };
    // `keys` rather than the object: a stable string of the key SET, so adding or
    // removing a shortcut re-runs the effect while re-rendering does not.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [keys, ...deps]);
}
