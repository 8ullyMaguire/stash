import React, { useMemo, useState } from "react";
import { Button, Modal } from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";

import { OperationButton } from "src/components/Shared/OperationButton";
import { chunk, selectForAction, ISceneMetadata } from "./bulkCreate";

// #3122 -- Create All / New / Missing on the Scene Tagger page.
//
// ## WHAT THE THREE ACTIONS DO
//
// Per `docs/plan/BACKLOG-17.md` section L they are three batched mutations over the SCENES on
// the page: All = every candidate, New = candidates with no organized flag, Missing =
// candidates that exist but have no metadata filled. So each is a bulk request that applies to
// a SET of scenes -- not a per-entity create from a list of typed names, which is what
// `PerformerTagger`'s "Batch Add Performers" already does and which is a different feature.
//
// The selection itself is in ./bulkCreate.ts as pure functions with their own tests, because
// it is the part that can be subtly wrong and it is otherwise reachable only by clicking
// buttons in a browser.
//
// ## WHY THE CONFIRMATION LISTS THE SCENES
//
// The plan's rule, and its stated reason (line 1054 of the plan): a count is satisfied by ANY
// row, so an absence can be masked by an unrelated one appearing. A button that says "7 scenes"
// and shows no list is exactly that failure -- the user cannot tell WHICH seven. So the count
// is a `.length` of a list the dialog then renders.

export type BulkAction = "all" | "new" | "missing";

interface ISceneLike extends ISceneMetadata {
  id: string;
}

interface IProps {
  scenes: ReadonlyArray<ISceneLike>;
  /** Performs the batched mutation. The page owns it; the selection lives here. */
  onCreate: (action: BulkAction, sceneIds: string[]) => Promise<void>;
  disabled?: boolean;
}

/** How many scenes per request. The plan says "batch in chunks"; this is the chunk. */
const CHUNK_SIZE = 50;

export const BulkCreate: React.FC<IProps> = ({
  scenes,
  onCreate,
  disabled,
}) => {
  const intl = useIntl();
  const [confirming, setConfirming] = useState<BulkAction | null>(null);

  // Derived from the same function the dialog renders, so a button's count and the dialog's
  // list cannot disagree.
  const selected = useMemo(
    () => (action: BulkAction) => selectForAction(scenes, action),
    [scenes]
  );

  const counts = {
    all: selected("all").length,
    new: selected("new").length,
    missing: selected("missing").length,
  };

  const namesForConfirm = confirming ? selected(confirming) : [];

  function button(action: BulkAction, messageId: string, count: number) {
    return (
      // A plain Button, not OperationButton: clicking only opens a confirmation and does no
      // work, so a spinner here would report progress that has not happened.
      <Button
        className="ml-1"
        variant="secondary"
        size="sm"
        disabled={disabled || count === 0}
        onClick={() => setConfirming(action)}
        title={intl.formatMessage({ id: messageId })}
      >
        <FormattedMessage id={messageId} values={{ count }} />
      </Button>
    );
  }

  async function run() {
    if (!confirming) return;
    const ids = selected(confirming);
    setConfirming(null);
    // Chunked, so a page with 5000 scenes does not become one enormous request. Sequential
    // rather than parallel: these are writes to the same objects, and firing them concurrently
    // would race the very state the user is looking at.
    for (const batch of chunk(ids, CHUNK_SIZE)) {
      await onCreate(confirming, batch);
    }
  }

  return (
    <>
      <div className="d-flex align-items-center" role="group">
        {button("all", "scene_tagger.bulk_create.all", counts.all)}
        {button("new", "scene_tagger.bulk_create.new", counts.new)}
        {button("missing", "scene_tagger.bulk_create.missing", counts.missing)}
      </div>

      <Modal show={confirming !== null} onHide={() => setConfirming(null)}>
        <Modal.Header closeButton>
          <Modal.Title>
            <FormattedMessage
              id="scene_tagger.bulk_create.confirm_title"
              values={{ action: confirming ?? "" }}
            />
          </Modal.Title>
        </Modal.Header>
        <Modal.Body>
          <p>
            <FormattedMessage
              id="scene_tagger.bulk_create.confirm_count"
              values={{ count: namesForConfirm.length }}
            />
          </p>
          {/* The scenes, not just the number. See the file comment. */}
          <ul className="bulk-create-scenes">
            {namesForConfirm.map((id) => (
              <li key={id}>{id}</li>
            ))}
          </ul>
        </Modal.Body>
        <Modal.Footer>
          <Button variant="danger" onClick={() => setConfirming(null)}>
            <FormattedMessage id="actions.cancel" />
          </Button>
          <OperationButton variant="primary" operation={run}>
            <FormattedMessage id="scene_tagger.bulk_create.confirm" />
          </OperationButton>
        </Modal.Footer>
      </Modal>
    </>
  );
};
