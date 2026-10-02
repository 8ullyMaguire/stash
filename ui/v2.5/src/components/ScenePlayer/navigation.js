// Navigation for the related-content panel (#4326).
//
// ## WHY THIS MODULE EXISTS AT ALL
//
// The spec's central decision is that browsing must NOT navigate, and choosing MUST.
// Expressing that as "the component calls `onSceneChosen` in one place and nowhere else"
// puts the property in a component's discipline, where a later edit can quietly break it
// and nothing fails. So it lives in a module with ONE export that can navigate, and the
// component's only route to navigation is through it.
//
// `isNavigationEvent` is what makes "browsing never navigates" mechanical rather than
// aspirational: `moveHighlight` moves, and only a key that `isNavigationEvent` accepts can
// reach `chooseScene`. A new key added to the panel without knowing this distinction is
// inert until it is named here, and T1 asserts the predicate's exact membership rather
// than that some keys happen not to navigate.

/**
 * The ONLY keys that may navigate.
 *
 * Membership is exact and asserted (T2): `enter` navigates, `down`/`up`/`j`/`k`/`escape`
 * and every other key do not. A permissive "anything not in the browse set" rule would
 * quietly let a future key navigate without anyone deciding it should.
 */
export const isNavigationEvent = (key) => key === "enter";

/**
 * Choose the highlighted scene: call `onSceneChosen`, then `onClose`.
 *
 * Both callbacks, chosen-then-closed, in that order -- asserted by T7. Closing on choose is
 * not cosmetic: the panel sits over the player it is about to be replaced by, and leaving
 * it up would cover the scene the user just picked.
 *
 * Returns whether anything was chosen, so a caller can avoid its own side effects on an
 * empty list rather than having to re-derive emptiness (T4).
 */
export const chooseScene = (highlight, length, onSceneChosen, onClose) => {
  const index = Number.isFinite(highlight) ? highlight : 0;
  if (!Number.isFinite(length) || length <= 0) {
    // Nothing to choose. Close anyway -- the user asked to leave the panel, and leaving it
    // open with no selection is the surprising outcome.
    if (onClose) onClose();
    return false;
  }
  if (onSceneChosen) onSceneChosen(index);
  if (onClose) onClose();
  return true;
};