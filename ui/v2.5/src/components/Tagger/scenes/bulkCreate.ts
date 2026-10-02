// #3122 -- Create All / New / Missing on the Scene Tagger page.
//
// ## THE THREE ACTIONS, AS THE PLAN DEFINES THEM
//
// `docs/plan/BACKLOG-17.md` section L:
//
//   All     = every candidate
//   New     = candidates with no organized flag
//   Missing = candidates that exist but have no metadata filled
//
// so these are selections over SCENES, which is why they are about `organized` and
// metadata rather than about whether a name is in the library. My first pass read the three
// words as "all names / only new names / only names missing from the scene", which is a
// different feature and not the one that was asked for. Corrected to the plan's definitions.
//
// ## WHY THE SELECTION IS PURE AND SEPARATE
//
// The interesting cases -- a scene with no studio, a scene whose only performer has no image,
// a scene that is organized but has no date -- are exactly the ones a human will not click
// through on every run, and inline in a component none of them is reachable except by
// clicking buttons in a browser. So the selection is a pure function and is tested as one.
//
// ## NO COUNT WITHOUT NAMES
//
// The plan's rule, and the spec's own reason for it (line 1054 of the plan): a count is
// satisfied by ANY row, so an absence can be masked by an unrelated one appearing. So every
// count here is derived from a LIST, and the UI shows that list rather than only its length
// -- a bulk action that reports "7 names will be created" and does not show them is exactly
// the failure the rule exists to prevent.

/** What the plan means by "no metadata filled". */
export interface ISceneMetadata {
  organized: boolean;
  studio?: string | null;
  date?: string | null;
  /** Performer/studio/tag names attached. An empty list means no metadata. */
  performerNames?: string[];
  tagNames?: string[];
}

/** True when a scene has no filled metadata at all. */
export function isMissingMetadata(s: ISceneMetadata): boolean {
  if (s.organized) {
    return false;
  }
  if (s.studio) return false;
  if (s.date) return false;
  if (s.performerNames?.length) return false;
  if (s.tagNames?.length) return false;
  return true;
}

/** True when a scene is not organized -- the plan's "New". */
export function isUnorganized(s: ISceneMetadata): boolean {
  return !s.organized;
}

/**
 * The scenes each action applies to.
 *
 * Returned as ID lists, not counts, for the reason above: the caller shows and confirms the
 * names, so the count is always derived from something inspectable.
 *
 * The three are NOT disjoint -- an unorganized scene with no metadata qualifies for both "New"
 * and "Missing" -- and that is the plan's own definitions overlapping, not a bug. Asserted in
 * the tests so a future "make them exclusive" edit has to notice.
 */
export function selectForAction(
  scenes: ReadonlyArray<{ id: string } & ISceneMetadata>,
  action: "all" | "new" | "missing"
): string[] {
  return scenes
    .filter((s) => {
      switch (action) {
        case "all":
          return true;
        case "new":
          return isUnorganized(s);
        case "missing":
          return isMissingMetadata(s);
      }
      // Unreachable for a well-typed caller -- `Action` is the union that makes it so, and a
      // mutation replacing this with `return false` is NOT killed by the suite, because no
      // test can reach an action outside the union. Recorded rather than papered over.
      //
      // It still belongs here. biome's `useIterableCallbackReturn` is right that falling off
      // the end makes `filter` treat the callback as returning undefined, which DROPS the
      // scene; and `false` (this branch's original value in a draft) is the worse of the two
      // wrong answers, because a bulk action that silently selects NOTHING looks identical
      // to one that legitimately had no candidates. `true` fails toward doing the work.
      return true;
    })
    .map((s) => s.id);
}

/**
 * Split the work into chunks, because the plan says "batch in chunks".
 *
 * The chunk size is a parameter rather than a constant so the tests can prove the boundary
 * behaviour without generating thousands of rows -- and so a caller can trade request count
 * against progress reporting.
 */
export function chunk<T>(items: ReadonlyArray<T>, size: number): T[][] {
  // `size = 0` in the loop below would never terminate, so this is a real guard rather than
  // defensive padding.
  if (size < 1) {
    throw new RangeError(`chunk size must be >= 1, got ${size}`);
  }

  // NOTE: there is deliberately no `if (items.length === 0) return []` here. An earlier
  // version had one, and a mutation run showed it was DEAD -- with an empty input the loop
  // body never runs, so the function already returns no batches. The test that appeared to
  // require it could not tell the guard from its absence, which is exactly the trap: a test
  // passing for the wrong reason is worse than no test, because it licenses the branch to
  // stay. `chunk([], 10)` returning `[]` is a property of the loop, and is asserted as such.
  const out: T[][] = [];
  for (let i = 0; i < items.length; i += size) {
    out.push(items.slice(i, i + size));
  }
  return out;
}
