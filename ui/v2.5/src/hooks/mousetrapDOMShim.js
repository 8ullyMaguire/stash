// Minimal DOM shim so `mousetrap` can be exercised under plain `node --test`.
//
// WHY THIS EXISTS. #2833's mechanism is a property of the mousetrap REGISTRY, not of
// React: whether a parent keeps a key after a child mounts and unmounts. Proving it
// therefore needs the real library -- a hand-written model of `bind`/`unbind` would only
// prove the model agrees with itself, which is the mistake this project has made twice
// (probe_relay reporting R077 failing on correct input; the mirror-byte-order codec
// agreeing with itself and both being wrong).
//
// So: the real library, plus a shim just large enough for its constructor and its key
// handlers. Dispatch goes through mousetrap's OWN `trigger`, not a synthesised
// KeyboardEvent, so assertions exercise its real `_callbacks` bookkeeping.
//
// THIS IS ESM. `ui/v2.5/package.json` sets `"type": "module"`, so a CommonJS
// `module.exports` here yields an EMPTY namespace under both `import` and `require` --
// which is exactly what the first version of this file hit. Hence `export function`,
// and hence the createRequire below rather than a bare `import Mousetrap from
// "mousetrap"`: a static import is hoisted and would evaluate the singleton's
// constructor before `installDOM()` had run.
//
// WHAT THE SHIM COVERS, and why each part is needed:
//   document.addEventListener  _addEvent falls back to `attachEvent` when
//                               addEventListener is missing, and calls it unguarded.
//   document.createElement     handleKey builds `document.createElement("div")` for the
//                               modifier-key lookup.
//   element.style, setAttribute  _getKeyInfo reads both.
//   document.documentElement   stopCallback reads `.tagName`.
import { createRequire } from "node:module";

const el = () => ({
  style: {},
  setAttribute() {},
  addEventListener() {},
  attachEvent() {},
  detachEvent() {},
  removeAttribute() {},
  tagName: "DIV",
});

let installedDoc = null;

export function installDOM() {
  if (installedDoc) {
    // Reuse the SAME document object. The mousetrap IIFE captured `document` at
    // evaluation; handing it a different one later would make every keydown land
    // somewhere nothing is listening.
    globalThis.document = installedDoc;
    globalThis.window.document = installedDoc;
    return installedDoc;
  }

  const doc = {
    documentElement: el(),
    createElement: () => el(),
    addEventListener() {},
    attachEvent() {},
    detachEvent() {},
    removeEventListener() {},
  };
  globalThis.document = doc;
  globalThis.window = {
    document: doc,
    addEventListener() {},
    attachEvent() {},
    detachEvent() {},
    removeEventListener() {},
  };
  // `navigator` is a READ-ONLY getter on Node 26, so assignment throws. `defineProperty`
  // is the only way, and it has to be conditional: on a real browser there is no such
  // getter and this is a plain set.
  Object.defineProperty(globalThis, "navigator", {
    value: { userAgent: "node" },
    configurable: true,
    writable: true,
  });

  installedDoc = doc;
  return doc;
}
// A registry with no bindings, for a test to build up from a known state.
//
// ## WHY THE RETURNED OBJECT IS THE FACADE, AND WHY THAT IS THE POINT
//
// The module's IIFE ends with `Mousetrap.init()`, which REPLACES every public method on
// the exported object with a closure over an instance it builds itself:
//
//     Mousetrap.init = function() {
//         var documentMousetrap = Mousetrap(document);
//         for (var method in documentMousetrap) {
//             if (method.charAt(0) !== '_') {
//                 Mousetrap[method] = function() {
//                     return documentMousetrap[method].apply(documentMousetrap, arguments);
//                 };
//             }
//         }
//     };
//
// Two facts, both MEASURED rather than taken from the docs, because the first version of
// this function got both wrong and both failures were silent:
//
//  1. `_callbacks` starts with `_`, so `init` SKIPS it. It is never copied onto the
//     export, so `Mousetrap._callbacks` there is `undefined` -- and assigning `{}` to it
//     makes a READ succeed while `bind` writes to the internal object. That is a green
//     assertion over a registry nothing dispatches through.
//  2. THE CONSTRUCTOR DOES NOT CACHE. `Mousetrap(t)` does `new Mousetrap(t)` whenever it
//     is called without `new`, so a second call returns a DIFFERENT instance
//     (`a === b` is `false`). The instance `init` closed over is therefore reachable only
//     through the facade's own closures.
//
// The facade IS that instance's public face, it is what the application itself uses, and
// `bind`/`unbind`/`trigger` through it all reach one internal registry. So this returns
// the facade with `_callbacks` MATERIALISED -- which is the only way to make the slot
// inspectable, and `mousetrapScope.test.js` asserts on it directly.
//
// Because the registry is a singleton, tests in this directory SHARE it. Independence
// comes from `reset()` here plus `resetScopedBindings()` in the scope layer, which is why
// both are called at the top of every test.
export function freshMousetrap() {
  installDOM();

  const facade = createRequire(import.meta.url)("mousetrap");

  // `reset()` is mousetrap's own and the only supported way to clear. It assigns
  // `self._callbacks = {}` on the INTERNAL instance, which is why the field is absent on
  // the facade afterwards.
  facade.reset();

  if (facade._callbacks === undefined) {
    facade._callbacks = {};
    facade._directMap = {};
  }

  return facade;
}
