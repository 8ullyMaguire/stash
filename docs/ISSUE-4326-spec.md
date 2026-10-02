# stash#4326 — browse related content during video playback

**Spec.** Written 2026-10-02, before any code, per the standing workflow.

Upstream #4326 asks for the ability to browse related content **while a video is playing**
without leaving the player.

## 1. What actually exists — measured 2026-10-02, not assumed

The ledger row for this issue previously claimed `build: overlay panel, no route change,
playback uninterrupted`. **All three halves of that are false, and one is false in the
direction that changes the design.** Measured:

| thing | state |
|---|---|
| `ui/v2.5/src/components/ScenePlayer/ScenePlayer.tsx` | 28.7 kB, video.js player, mature |
| video.js plugins in that package | `skipButtons`, `abLoop`, scrubber, VTT thumbnails, VR mode, live mode |
| queue navigation | **exists** — `queueNext`/`queuePrevious`/`queueRandom`, hotkeys `p n`/`p p`/`p r` |
| `QueueViewer` | a panel listing queue scenes, each a `<Link>` to `/scenes/:id` |
| a related-content overlay | **absent everywhere.** `git log --all --diff-filter=A --name-only` finds only `RelatedGroupTable.tsx` and `RelatedGroupPopover.tsx`, both upstream `bcf0fda7a` (#5105), neither about playback |
| "no route change" | **FALSE, and this is the load-bearing measurement** |

### 1.1 The load-bearing measurement

`SceneDetails/Scene.tsx:942`:

```ts
function loadScene(sceneID: string, autoPlay?: boolean, newPage?: number) {
  const sceneLink = sceneQueue.makeLink(sceneID, { newPage, autoPlay, continue: continuePlaylist });
  history.replace(sceneLink);
}
```

**Every in-app scene change goes through `history.replace`.** Three consequences, and the
design has to answer all three:

1. **It IS a route change.** The old disposition said "no route change"; the code says
   otherwise. Accepting that claim would have produced a component designed against a
   property the app does not have.
2. **`replace`, not `push`.** History is *destroyed*, not extended. So there is no
   back-button path through a browsing session — which is the right behaviour for a queue
   and wrong for exploratory browsing, where "back to where I was" is the point.
3. **It carries state in the query string** — `newPage`, `autoPlay`, `continue`. So a
   scene link is not a bare id.

`QueueViewer` looks like an in-place mechanism — it calls `event.preventDefault()` and then
a callback — but the callback's implementation is `loadScene`, so it reaches
`history.replace` too. **A `preventDefault` on a `<Link>` is not in-place navigation; it is
navigation that also suppressed the browser default before doing the same thing.** That is
the trap here, and it is why the disposition was written confidently and wrongly.

### 1.2 What "playback uninterrupted" can and cannot mean

The player is rendered as `<ScenePlayer key="ScenePlayer" ...>` — a **constant** key, with
`scene` as a prop. So when the scene id changes, React re-renders the same component
instance rather than remounting it, and `ScenePlayer` is already written to load a new
source into an existing video.js instance (that is what `source-selector.ts` is for).

So playback of the *current* scene is not interrupted by browsing — but playback of the
*next* scene obviously begins when it is selected. The honest reading of "uninterrupted":
**the current video keeps playing while the panel is open and while the user reads it.** A
panel that paused playback on open would defeat the feature, since the reason to browse
during playback is to decide what to watch next.

## 2. Requirements

**R1. Browse without leaving the player.** Opening, browsing and closing related content
must not navigate away from the playing scene.

**R2. Current playback continues.** Opening the panel does not pause the current scene, and
closing it does not resume or restart it.

**R3. Choosing a destination is an explicit act.** Browsing must not itself change what is
playing. A user reading a list of candidates is not a user who has chosen.

**R4. A keyboard route, because the player owns the keyboard.** video.js captures keys on
the player element. A panel that cannot be operated without a mouse is unusable here, and
this repo already hit that lesson (#2833, `useScopedKeybinds`, 16 components binding `e`).

**R5. Keys scoped and released.** Whatever the panel binds must be released on close, and
must not collide with the player's own bindings.

**R6. No history damage.** Browsing must not consume the user's back-button history.

## 3. The design

### 3.1 A panel, not a route — and the *one* navigation point

The panel is a component in the `ScenePlayer` package (it owns the player and its styles)
rendered as a sibling of the player inside `scene-player-container`, not as a route.

**R6 is why this is not negotiable, and it is the interesting constraint.** `loadScene`
uses `history.replace`, so the panel must NOT go through it for *browsing* — only for the
final choice. Two navigation points is the honest shape:

- **browsing** — entirely local state. No history, no navigation, nothing to undo.
- **choosing** — one call to the existing `onSceneClicked`-equivalent, which reaches
  `loadScene`. One navigation, using the existing mechanism, so `autoPlay`/`continue`/
  `newPage` keep working and the queue keeps its semantics.

A panel that navigated per row-hover or per arrow-key would fill the history with browsing
noise; `replace` hides that by destroying history, which is worse (R6 by accident).

### 3.2 What counts as "related content"

**The queue, not a recommendation engine.** The queue already exists, already has the
scenes in order, and already defines what "next" means for this user in this session. A
new recommender would be a second, disagreeing answer to a question the app already answers.

The panel shows the **current queue** with the current scene marked, plus a filter over
that queue. Optionally: scenes sharing the current scene's performers or studio, which is a
GraphQL query that already exists in the filter system — **recorded as a named extension,
not built in step 1**, because it introduces a second data source and a second notion of
"next" at the same time as the first.

### 3.3 Keys: scoped, not global

Reuse the repo's existing mechanism rather than a new one. **`ui/v2.5/src/hooks/mousetrapScope.js`**
is the fix for #2833, and it is a **per-key STACK** — top of the stack owns the key, and a
handler that unmounts POPS so the key is re-registered to the handler below. That is
precisely R5, verified by reading it rather than assumed: `useScopedKeybinds` is exported
from that file and is already called twice in `Scene.tsx` (lines 277, 870).

It is reusable here because the failure mode it was written for is the same one a
player-plus-panel pair hits: without a stack, closing the panel would install a **noop**
in place of its keys, and the scene page behind it would be left holding dead keys — the
defect #2833's report described as "works every other time".

**No new keybinding helper.** A second mechanism beside a working one is how the #2833 bug
comes back in a place nobody looks. `mousetrapScope.js` already ships
`mousetrapScope.mutate.py`, so the mechanism this panel relies on has its own mutation
gate; a new helper would arrive ungated.

**One caveat, measured and not assumed:** that file's registry is resolved at CALL TIME,
never captured at import time, because mousetrap is a singleton and an instance captured at
import can stop being the one anything dispatches through. So the panel must call
`useScopedKeybinds` (the facade) and must not reach for `Mousetrap` directly — that is the
exact failure its first test caught, with every shortcut dead and every stack-only
assertion still passing.

### 3.4 R2 is a property of video.js state, so it is tested there

"Playback continues" is not a React-state fact; it is that the underlying media element is
still playing. Asserted against the player's own `paused()`, not against a prop — a test
that reads React state cannot see whether the video stopped.

## 4. Definition of done

- Panel opens and closes without a route change (asserted: the router's location is
  unchanged across open→browse→close).
- The current scene keeps playing while the panel is open (asserted against `paused()`).
- Browsing does not change the playing scene; choosing does (two separate assertions — the
  second one is the one that would catch an accidental navigation in the browse path).
- The panel is fully operable by keyboard, and every key it binds is released on close
  (asserted on the key registry, not on a rendered snapshot).
- No new route, no new history entry.

## 5. Not in scope, and why

- **A recommender.** §3.2. The queue is the answer that already exists.
- **Changing `loadScene` from `replace` to `push`.** It would be a one-word change that
  alters back-button behaviour for every existing scene transition, including the queue's,
  to serve a panel. Not this issue's business.
- **Touch/swipe gestures.** video.js owns the touch surface here and getting them wrong
  breaks seeking. Recorded, not attempted.
- **Browser verification.** This host has no configured stash instance (no `~/.stash`,
  nothing listening), so provisioning one is out of scope — the same limit recorded for
  #2833. Every claim here is about the code and the registry under the shipped libraries.
  **Nobody has opened this panel in a browser, and the commit says so.**
