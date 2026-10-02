// Highlight navigation for the related-content panel (#4326).
//
// Pure functions, no React, on purpose. `mousetrapScope.test.js` states the reason this
// repo tests at this level: "what must not regress is the property, and a test that
// mounted components would assert the fix's IMPLEMENTATION rather than the property that
// made the bug a bug." The same applies here: the interesting facts about a highlight are
// arithmetic, and arithmetic is testable without a DOM.
//
// ## WHY CLAMP AND NOT WRAP
//
// `(i + 1) % length` is the reflexive way to write this and it is the wrong choice for a
// list a user is READING. Wrapping is invisible: pressing `up` at the top silently
// teleports to the bottom, which a user cannot distinguish from the key doing nothing at
// all, and which then makes the list feel broken when they choose the wrong row. Clamping
// holds the highlight still at an end, so "nothing happened" is the honest signal that
// there is nowhere further to go. It is asserted in `highlight.test.js` (T3) rather than
// left as an implementation detail.

/**
 * Clamp an index into `[0, length - 1]`.
 *
 * An empty list has no valid index, so it yields 0 -- the only value that keeps a caller
 * from indexing out of bounds. `chosenIndex` is what turns 0-on-empty into "no scene".
 */
export const clampIndex = (index, length) => {
  if (!Number.isFinite(length) || length <= 0) return 0;
  if (!Number.isFinite(index) || index < 0) return 0;
  return Math.min(index, length - 1);
};

/**
 * Move the highlight by `delta`, clamped at both ends.
 *
 * Takes exactly three arguments, and that arity is asserted in T1: no callback can be
 * passed in, so "moving the highlight never navigates" is a property of this module's
 * shape rather than a rule the calling component has to remember.
 */
export const moveHighlight = (current, delta, length) =>
  clampIndex((Number.isFinite(current) ? current : 0) + (Number.isFinite(delta) ? delta : 0), length);

/**
 * The scene index a navigation key should choose: the highlighted one.
 *
 * `null` for an empty list -- the panel must call nothing rather than choose a scene that
 * is not there (T4).
 */
export const chosenIndex = (highlight, length) => {
  if (!Number.isFinite(length) || length <= 0) return null;
  return clampIndex(highlight, length);
};