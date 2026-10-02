// Types for `highlight.js`.
//
// This repo puts a `.d.ts` beside each untyped JS module it consumes from TypeScript
// (`videojs-vr.d.ts`, `mousetrap-pause.d.ts`, ...) rather than loosening `strict` or
// sprinkling `any` at the call site. `tsconfig.json` has `allowJs: true` and
// `strict: true`, so without this the callback parameters of an imported JS function
// arrive as implicit `any` -- which `strict` rejects inside a `.tsx`.

export function clampIndex(index: number, length: number): number;

/**
 * Takes exactly three arguments, and that arity is asserted in `highlight.test.js` (T1):
 * a fourth callback parameter would be the shape that lets browsing navigate.
 */
export function moveHighlight(current: number, delta: number, length: number): number;

/** `null` for an empty list -- the panel must choose nothing rather than index 0. */
export function chosenIndex(highlight: number, length: number): number | null;