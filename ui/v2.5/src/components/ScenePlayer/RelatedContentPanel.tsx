import React, { useCallback, useMemo, useRef, useState } from "react";
import { FormattedMessage, useIntl } from "react-intl";
import { Button, ListGroup } from "react-bootstrap";
import { useScopedKeybinds } from "src/hooks/mousetrapScope";
import { objectTitle } from "src/core/files";  // measured: src/core/files, not src/utils
import { clampIndex, moveHighlight } from "./highlight";
import { chooseScene } from "./navigation";

// Related-content panel for #4326: browse the queue WHILE the current scene keeps playing.
//
// ## WHY A PANEL AND NOT A ROUTE
//
// `SceneDetails/Scene.tsx:942` routes every scene change through `history.replace`. So a
// panel that navigated while browsing would be destroying the user's back history on every
// arrow key. Browsing is therefore local state only; the single navigation happens on an
// explicit choice, and it delegates to the existing queue callback so `autoPlay`,
// `continue` and `newPage` keep working. See `docs/ISSUE-4326-spec.md` §3.1.
//
// ## WHY THE PLAYBACK IS NOT PAUSED
//
// Opening this panel must not pause the current scene: the reason anyone browses during
// playback is to decide what to watch next, and a panel that pauses on open defeats that.
// The player is mounted as `<ScenePlayer key="ScenePlayer" scene={scene}>` -- a CONSTANT
// key with the scene as a prop -- so it survives this panel appearing as a sibling without
// remounting. Note the panel is rendered OUTSIDE the player, never inside it: a change to
// the player's own children is the one thing that could still interrupt playback.
//
// ## WHY THE KEYS GO THROUGH THE FACADE
//
// `useScopedKeybinds` is `mousetrapScope.js` -- the #2833 fix, a per-key stack where an
// unmounting handler POPS and the key returns to the handler below. Binding `Mousetrap`
// directly here would reproduce #2833 in a new place: that file's own first test failed
// with every shortcut dead and every stack-only assertion passing, because the registry
// was captured at import time. The facade resolves the registry at CALL time. So the
// import is `useScopedKeybinds`, never `Mousetrap`, and `docs/mutate_4326.py`'s M6 exists
// to keep it that way.

export interface IRelatedScene {
  id: string;
  title?: string | null;
  paths?: { screenshot?: string | null };
  studio?: { name?: string | null } | null;
}

interface IRelatedContentPanelProps {
  scenes: IRelatedScene[];
  currentSceneId: string;
  /** Called ONLY on an explicit choice -- never on open, browse, or an arrow key. */
  onSceneChosen: (sceneID: string) => void;
  onClose: () => void;
}

const RelatedContentPanel: React.FC<IRelatedContentPanelProps> = ({
  scenes,
  currentSceneId,
  onSceneChosen,
  onClose,
}) => {
  const intl = useIntl();
  const [highlight, setHighlight] = useState(0);

  // The list is re-fetched as the queue changes, so the highlight is kept IN RANGE rather
  // than reset: a user browsing with the highlight on row 5 while a queue mutation drops
  // the list to 2 rows would otherwise find `enter` choosing nothing.
  const safeHighlight = clampIndex(highlight, scenes.length);

  const move = useCallback(
    (delta: number) => setHighlight((current) => moveHighlight(current, delta, scenes.length)),
    [scenes.length]
  );

  const chooseHighlighted = useCallback(() => {
    chooseScene(safeHighlight, scenes.length, (index) => {
      const scene = scenes[index];
      if (scene) onSceneChosen(scene.id);
    }, onClose);
  }, [safeHighlight, scenes, onSceneChosen, onClose]);

  const close = useCallback(() => onClose(), [onClose]);

  // Through the facade, and keyed on the KEY SET (the facade derives `keys` itself from
  // the bindings object). A bare `[]` would leave the handlers stale, so the highlight
  // would stop moving -- the #2833 lesson, in this component.
  useScopedKeybinds(
    useMemo(
      () => ({
        down: () => move(1),
        up: () => move(-1),
        j: () => move(1),
        k: () => move(-1),
        enter: () => chooseHighlighted(),
        escape: () => close(),
      }),
      [move, chooseHighlighted, close]
    )
  );

  // Clicking a row is an explicit choice, the same as `enter` on it. Focus the list so the
  // keyboard path is reachable after a mouse user opens the panel.
  const listRef = useRef<HTMLUListElement>(null);

  // objectTitle(s) takes ONLY the object -- measured from src/core/files.ts:16.
  const title = (scene: IRelatedScene) => objectTitle(scene);

  return (
    <div className="related-content-panel">
      <div className="related-content-panel-header">
        <span>{intl.formatMessage({ id: "related_content.related_content" })}</span>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => close()}
          title={intl.formatMessage({ id: "actions.close" })}
        >
          <FormattedMessage id="actions.close" />
        </Button>
      </div>
      {scenes.length === 0 ? (
        <div className="related-content-panel-empty">
          <FormattedMessage id="related_content.empty" />
        </div>
      ) : (
        <ListGroup as="ul" ref={listRef} className="related-content-panel-list">
          {scenes.map((scene: IRelatedScene, index: number) => {
            const isCurrent = scene.id === currentSceneId;
            const isHighlighted = index === safeHighlight;
            return (
              <ListGroup.Item
                // Key on the id, never the index: the queue reorders, and an index key
                // makes React reuse the WRONG row's highlight after a reorder.
                key={scene.id}
                as="li"
                active={isHighlighted}
                action
                onClick={() =>
                  chooseScene(index, scenes.length, () => onSceneChosen(scene.id), onClose)
                }
                aria-current={isCurrent ? "true" : undefined}
                data-highlighted={isHighlighted ? "true" : "false"}
              >
                <span className="related-content-panel-title">{title(scene)}</span>
                {isCurrent && (
                  <span className="related-content-panel-current">
                    <FormattedMessage id="related_content.now_playing" />
                  </span>
                )}
                {scene.studio?.name && (
                  <span className="related-content-panel-studio">{scene.studio.name}</span>
                )}
              </ListGroup.Item>
            );
          })}
        </ListGroup>
      )}
    </div>
  );
};

export default RelatedContentPanel;