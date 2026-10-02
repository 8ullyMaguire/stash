// Types for `navigation.js` -- the ONLY route by which the panel can navigate (#4326).

/**
 * The only keys that may navigate. Exact membership, asserted in `highlight.test.js` (T2).
 */
export function isNavigationEvent(key: string): boolean;

/**
 * Call `onSceneChosen(highlight)` then `onClose()`, and report whether anything was chosen.
 *
 * Both callbacks are optional in the type because an empty list calls only `onClose`, and
 * because the component guards a missing scene itself. Returns `false` for an empty list so
 * a caller can skip its own side effects rather than re-derive emptiness.
 */
export function chooseScene(
  highlight: number,
  length: number,
  onSceneChosen?: (index: number) => void,
  onClose?: () => void
): boolean;