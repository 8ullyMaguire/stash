# stash#4326 — implementation plan

Implements `docs/ISSUE-4326-spec.md`. Six steps, each with the exact verification and its
expected output. Nothing here is written before its predecessor is green.

**Build command for every step** (hoisted linker — `pnpm build` does not work):

```bash
cd /home/alvaro/code-local/go/stash/ui/v2.5 && node node_modules/vite/bin/vite.js build
```

**Baseline, MEASURED 2026-10-02** — `pnpm run validate` is RED and pre-existing:

```
Found 10 errors.
Found 20 warnings.
Found 1 info.
Checked 632 files in 782ms.
```

(The vault's earlier figure of "7 errors / 6 warnings" is stale — it is a different
`validate` version. Measure it, do not carry it.)

**ONLY THE COUNTS ARE COMPARABLE, and that is a measured constraint rather than a
choice.** Biome reports `The number of diagnostics exceeds the limit allowed. Use
--max-diagnostics to increase it. Diagnostics not shown: 11.` — so the error *messages*
are truncated and a `diff` of them compares an arbitrary subset. The gate is therefore
the two counts:

```bash
cd /home/alvaro/code-local/go/stash/ui/v2.5
pnpm run validate 2>&1 | grep -E '^Found [0-9]+ (errors|warnings|info)' > /tmp/validate-base.txt
cat /tmp/validate-base.txt     # expect: 10 errors, 20 warnings, 1 info
```

**11 of the diagnostics are already hidden by the limit**, so a new error in one of my
files could be silently dropped from the output while the count rises — which the count
does catch. But a new error that replaces a hidden one would not. So step 3 additionally
runs `npx tsc --noEmit` on the specific files, where nothing is truncated.

**THE REGRESSION GUARD THIS STEP MUST NOT BREAK.** The vault records that 16 components
bind `e`, 16 bind `d d`, 9 bind `p r`, and that #2833's fix is `mousetrapScope.js`.
`mousetrapScope.test.js` is the pinned reproduction and runs under `node --test` against the
real library. Step 2 adds a new owner to that registry, so step 2's verification runs it
**and its mutation harness**, not just `tsc`.

---

## Step 1 — the panel component, with no wiring at all

**Deliberately built and wired in separate steps.** A component that is correct but
unreachable is the "fully implemented, fully tested, read by nothing" shape this project
has hit six times (`ProposalStore`, `ReputationStore`, the media gate, `ConsentStore`,
`AccessPolicyStore`, the acquisition queue). So step 1's test drives the component
directly, and step 2 is the step that could regress into invisibility.

Create `ui/v2.5/src/components/ScenePlayer/RelatedContentPanel.tsx`:

```tsx
import React, { useCallback, useMemo, useRef, useState } from "react";
import { FormattedMessage } from "react-intl";
import { Button, ListGroup } from "react-bootstrap";
import { useScopedKeybinds } from "src/hooks/mousetrapScope";
import { objectTitle } from "src/utils/objectTitle";
```

Props — and the shape is the spec's §3.1 two-navigation-point decision made into a type:

```tsx
export interface IRelatedScene {
  id: string;
  title?: string | null;
  paths: { screenshot?: string | null };
  studio?: { name?: string | null } | null;
}

interface IRelatedContentPanelProps {
  scenes: IRelatedScene[];
  currentSceneId: string;
  /** Called ONLY on an explicit choice. Never on open, browse, or arrow key. */
  onSceneChosen: (sceneID: string) => void;
  onClose: () => void;
}
```

Behaviour, with the two properties that are the feature:

- **Local index state** drives a highlight. `ArrowUp`/`ArrowDown`/`j`/`k` move the
  highlight **only** — they never call `onSceneChosen`. This is R3 as code.
- `Enter` on the highlighted row, or a click on a row, calls `onSceneChosen(row.id)`.
- `Escape` calls `onClose`.

Keys, via the existing facade — **not** `Mousetrap` directly (spec §3.3's caveat):

```tsx
useScopedKeybinds(
  useMemo(
    () => ({
      down: () => move(1),
      up: () => move(-1),
      enter: () => chooseHighlighted(),
      escape: () => onClose(),
    }),
    [move, chooseHighlighted, onClose]
  )
);
```

`move`, `chooseHighlighted` are `useCallback`s. Note the **dependency on the key SET**
rather than a bare `[]` — #2833's fix derives its deps from the key set for exactly this
reason, and a stale closure here means the highlight does not move.

**Do not add a test in this step.** Step 1's verification is the build. The tests come in
step 3 with the wiring they are supposed to observe.

**Verify**

```bash
cd /home/alvaro/code-local/go/stash/ui/v2.5 && node node_modules/vite/bin/vite.js build 2>&1 | tail -5
```
Expect: a build. No test claims are made yet, and the commit message must not claim any.

---

## Step 2 — mount it in the player container

In `ui/v2.5/src/components/Scenes/SceneDetails/Scene.tsx`, inside the
`scene-player-container` div (line ~1087), render the panel **as a sibling of the player**,
not inside the player and not as a route:

```tsx
const [relatedOpen, setRelatedOpen] = useState(false);
```

```tsx
<div className={`scene-player-container ${collapsed ? "expanded" : ""}`}>
  {relatedOpen && (
    <RelatedContentPanel
      scenes={queueScenes as IRelatedScene[]}
      currentSceneId={scene.id}
      onSceneChosen={(id) => {
        setRelatedOpen(false);
        onQueueSceneClicked(id);
      }}
      onClose={() => setRelatedOpen(false)}
    />
  )}
  {isPlayable(scene) ? ( /* existing ScenePlayer unchanged */ ) : ( ... )}
</div>
```

Two decisions in that snippet, both load-bearing:

1. **`onSceneChosen` closes the panel and then delegates to the EXISTING
   `onQueueSceneClicked`.** That is the spec's single navigation point, and it is what keeps
   `autoPlay`/`continue`/`newPage` working — `onQueueSceneClicked` reaches `loadScene`,
   which reaches `history.replace`. One navigation, the existing mechanism, no new history
   behaviour.
2. **The panel renders BEFORE the player and outside it**, so opening it does not remount
   the player. The player's `key="ScenePlayer"` is constant, so a change of `children`
   position would be the only thing that could interrupt playback — R2 depends on this.

Add the open trigger as a hotkey in the existing `useScopedKeybinds` call at line 277, so
it participates in the same stack as the page's other keys:

```
// r
"r": (() => setRelatedOpen((v) => !v)),
```

**`r` is chosen because it is unbound in this file** — measured, and the check is in step
3's test. Do not assume that; `grep -n '"r":' ui/v2.5/src/components/Scenes/SceneDetails/Scene.tsx`
must return nothing before using it.

**Verify**

```bash
cd /home/alvaro/code-local/go/stash/ui/v2.5
node node_modules/vite/bin/vite.js build 2>&1 | tail -5
node --test src/hooks/mousetrapScope.test.js 2>&1 | tail -8
```
Expect: build succeeds; `mousetrapScope.test.js` still passes (it is the #2833 pinned
reproduction and a new key owner must not disturb it).

---

## Step 3 — tests, in the form this repo actually has

**MEASURED, and this step was wrong before it was written.** There are **no `.test.tsx`
files anywhere** in `ui/v2.5/src` — `find ui/v2.5/src -name '*.test.tsx'` returns nothing,
and `package.json` has no `test` script. The convention is **pure-JS `node --test` files**:
`mousetrapScope.test.js`, `mousetrapScope.fix.test.js`, `mousetrapScope.adoption.test.js`.

And `mousetrapScope.test.js` says why, in its own header, and it is the reason that governs
this step:

> It does NOT test the fix. React is not loaded here on purpose: what must not regress is
> the registry-level property below, and a test that mounted 16 components would assert the
> fix's *implementation* rather than the property that made the bug a bug.

So the panel's testable content is **split by whether React is needed**, and the split is
the design:

**3a — `RelatedContentPanel.logic.js` — pure functions, no React, `node --test`.**

Extract the panel's decision logic into plain functions the component calls. This is the
shape that makes T1–T3 testable at all without a rendering harness:

```js
// highlight.js
export const clampIndex = (index, length) =>
  length === 0 ? 0 : Math.min(Math.max(index, 0), length - 1);

export const moveHighlight = (current, delta, length) =>
  clampIndex(current + delta, length);
```

`clampIndex` **clamps**, it does not wrap — and that is the decision T3 pins. Put the
rationale in the file, not only in the spec: a wrapped highlight is not obviously wrong to
a user and is indistinguishable from a bounded one.

```js
// navigation.js
// The ONLY place onSceneChosen may be called. One function, so "browsing never navigates"
// is a property of the module rather than a discipline the component must maintain.
export const isNavigationEvent = (key) => key === "enter";
```

Tests, all pure:

| # | test | asserts |
|---|---|---|
| T1 | `moveHighlight` never navigates | `moveHighlight` has no access to a callback — asserted on the **module's exports**, that `moveHighlight` takes exactly `(current, delta, length)` and no callback parameter. A test that reads its own return value cannot see a wiring; asserting the *shape* can. |
| T2 | `chosenIndex` follows the highlight | `chosenIndex(highlight, length) === highlight` — the positive half of R3, and it is what catches an off-by-one |
| T3 | the highlight clamps, not wraps | `moveHighlight(0, -4, 3) === 0`, `moveHighlight(0, 4, 3) === 2`, `moveHighlight(1, 1, 0) === 0` |
| T4 | an empty queue cannot produce an index | `chosenIndex(0, 0)` is `null`, so `onSceneChosen` is never called with a scene that is not there |

**3b — the registry property, against the real library.**

T5 (`r` collides with nothing) and T6 (keys are released) are **registry** properties, so
they belong in a `node --test` file using the existing `freshMousetrap` shim — the same
technique `mousetrapScope.test.js` uses, and deliberately not a model of it:

```js
import test from "node:test";
import assert from "node:assert/strict";
// RELATIVE, not "src/hooks/...": `src` is a VITE alias, and `node --test` does not know
// it. The existing test uses "./mousetrapDOMShim.js" from the same directory, and
// `node --test src/hooks/mousetrapScope.test.js` passes 3/3 -- measured, not assumed.
import { freshMousetrap } from "../../hooks/mousetrapDOMShim.js";
```

- **T5** — bind `r` through the facade, then dispatch through mousetrap's own `trigger`,
  and assert the handler ran. Then assert `r` appears in **no** other binding site, read by
  grepping the source rather than from a hand-written list. A literal list of keys drifts
  the moment a key is added, and the test would then pass while the collision exists.
- **T6** — bind through the facade, unbind, and assert the key resolves to **nothing**
  rather than to a noop. This is #2833's defect verbatim, and a rendered-snapshot assertion
  would pass with the noop in place because the noop leaves the shape unchanged.

**Verify**

```bash
cd /home/alvaro/code-local/go/stash/ui/v2.5
node --test src/components/ScenePlayer/highlight.test.js 2>&1 | tail -8
node --test src/hooks/mousetrapScope.test.js 2>&1 | tail -6      # must still pass
npx tsc --noEmit 2>&1 | grep -E 'RelatedContentPanel|highlight|navigation' ; echo "tsc on new files: clean if nothing above"
pnpm run validate 2>&1 | grep -E '^Found [0-9]+ (errors|warnings|info)' > /tmp/validate-after.txt
diff /tmp/validate-base.txt /tmp/validate-after.txt && echo "no new lint errors"
```
Expect: the new file passes; `mousetrapScope.test.js` **still** passes (step 2 added a key
owner to that registry); `diff` empty and `no new lint errors`. **Any** rise in the error
count fails the step — the baseline is red, so only the counts can be compared.

## Step 4 — the mutation gate, and this step is not optional

**There is a direct precedent, and it should be copied rather than reinvented:**
`ui/v2.5/src/hooks/mousetrapScope.mutate.py` is a Python harness that mutates **JavaScript**,
runs `node --test`, and applies each mutant by **exact-string substitution** so a file edit
that moves the code fails the substitution loudly instead of silently mutating nothing. Put
`docs/mutate_4326.py` in the same shape:

```python
HERE = pathlib.Path(__file__).resolve().parent
UI = HERE.parent / "ui" / "v2.5"
TESTS = [UI / "src/components/ScenePlayer/highlight.test.js"]
# run:  subprocess.run(["node", "--test", *[str(t) for t in TESTS]], cwd=UI, ...)
```

Verdicts `KILLED`/`SURVIVED`/`SKIP`/`COVERED`, exit 1 on survivors — the shape from
`docs/mutate_external_id.py`. **A gate that can be made to pass without being open is
decoration.**

Mutants, one per requirement:

| # | mutation | must kill |
|---|---|---|
| M1 | `down`/`up` call `chooseHighlighted()` as well as `move` | T1 |
| M2 | `enter` chooses `scenes[0]` instead of the highlighted index | T2 |
| M3 | the highlight wraps (`(i + 1) % len` where clamping was intended) | T3 |
| M4 | the panel does not release its keys on unmount | T4 |
| M5 | `onSceneChosen` does not close the panel | T7 — **added in this step**, see below |
| M6 | `r` is bound as a global rather than through the facade | T5 |

**M5 forces a seventh test, and that is the gate earning its keep.** A mutation whose
witness does not exist is scored `SKIP` — which is not a kill, and a harness that counts
those as kills reports 6/6 while testing 5. So step 4 adds **T7**: `chooseScene` in the
pure `navigation.js` module takes `(highlight, length, onSceneChosen, onClose)` and calls
**both** — chosen then closed. T7 asserts both callbacks fired, once each, with the
highlighted id. Putting that logic in the pure module is what makes it testable at all,
and it is why M5 is killable without a rendering harness.

**M6 is the one this repo has been bitten by.** `mousetrapScope.js`'s own first test failed
with every shortcut dead while every stack-only assertion passed, because the registry was
captured at import time. A panel reaching for `Mousetrap` directly reproduces that exact
bug in a new place.

**And the harness audits its own `-run` filter**, as `mutate_external_id.py` now does. The
filter silently excluded two tests there, so a mutant survived that the suite would have
killed. A gate that quietly under-measures looks exactly like a gate that found real gaps.

**Verify**

```bash
cd /home/alvaro/code-local/go/stash && python3 docs/mutate_4326.py; echo "exit=$?"
```
Expect: **6/6 killed, exit 0**. A survivor is a hole in the tests *unless* the code is
wrong — read the diff before touching the source: if disabling the check makes the suite
pass, the check was wrong rather than the test blind.

---

## Step 5 — the two honest caveats, recorded where they are read

1. **Not browser-verified.** No configured stash instance on this host. Append to the spec
   §5 and to the commit message: *nobody has opened this panel in a browser*.
2. **`useScopedKeybinds` deps.** Step 1's note about deriving deps from the key set is the
   #2833 lesson; if the highlight fails to move in review, that is the first thing to check.

Do not skip this step because it "is only documentation". The #2833 commit that omitted it
is the one a later session had to re-derive the mechanism from.

---

## Step 6 — the ledger, then the gate

`docs/ISSUE-4326-row.py`, modelled on `docs/close_1790.py`, and **borrowing
`goal-check.py`'s `split_cells`** rather than reimplementing the index arithmetic:

- state `open` → `done`, with the commit hash in the **disposition** cell (`cells[-3]`).
- `docs/UPSTREAM-ISSUES.md`: move the row **out of** `## Planned — by signal` into
  `## Resolved`, update both header counts and the tally after the rules table.
- `docs/closed-issues.md`: a row with the **`stash#` prefix** and **5 columns**, matched
  against that file's own `^\| stash#(\d+) \|` regex. The first attempt at this wrote a
  bare `| 4326 |` in 4 columns and the checker reported a *missing row* rather than a
  wrong-format one.

```bash
cd /home/alvaro/code-local/go/stash
python3 docs/check-issue-ledgers.py     # expect: OK: header, table and log agree
python3 docs/goal-check.py 2>&1 | grep -E '^C[0-9]'
```

**Verify the whole thing**

```bash
cd /home/alvaro/code-local/go/stash
gofmt -l pkg/ internal/ ; go build ./... && echo "go ok"
go test ./pkg/... -count=1 2>&1 | grep -v '^ok' | head -5
cd ui/v2.5 && node node_modules/vite/bin/vite.js build 2>&1 | tail -3
```

Then commit, tag `stash-4326-done`, push to **both** `origin` and `forgejo`.
