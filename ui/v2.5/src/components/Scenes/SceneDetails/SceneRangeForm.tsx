import React, { useState } from "react";
import { Button, Form, Modal } from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";
import { DurationInput } from "src/components/Shared/DurationInput";
import * as GQL from "src/core/generated-graphql";
import { useSceneUpdate } from "src/core/StashService";
import { useToast } from "src/hooks/Toast";
import TextUtils from "src/utils/text";

interface IProps {
  sceneID: string;
  /** The scene's PRIMARY file -- the window belongs to a (scene, file) pair. */
  file: GQL.VideoFileDataFragment;
  onClose(): void;
}

/**
 * SceneRangeForm sets the window a scene takes from one of its files.
 *
 * #3530. The API half is in `validateSceneWindow` / `SceneStore.SetSceneRange`; this is the
 * only way a user can reach it, which until now they could not -- the columns were settable
 * only by hand in SQLite.
 *
 * ## Why the form validates locally at all, when the API validates too
 *
 * Because the API's refusals arrive as a 400 with a message, and a message is a bad way to find
 * out you typed 3 digits where 2 go. The two constraints this can check without a round trip are
 * `end > start` and `end <= duration`, and it has both numbers already: the file's duration is in
 * the fragment the panel was handed. So the form refuses before submitting, and the API still
 * validates because a form is not a security boundary.
 *
 * ## Why only the primary file
 *
 * The schema and API allow a window on any file. A scene with two files shows two windows, which
 * is the truth (see the VideoFile schema comment), but the *use* for #3530 is "this file holds
 * several scenes", which is a one-file-per-scene case. Offering it on a non-primary file would
 * invite a range the user cannot tell apart from the primary's in the UI. Deliberate: the API is
 * the more permissive layer, and the panel passes `primary` so this renders for one file only.
 *
 * ## What clearing sends
 *
 * Both fields as `null`. That is NOT the same as omitting them: `changesetTranslator.hasField`
 * is how the resolver tells an absent field from an explicit null, and only the latter clears.
 * `undefined` would silently mean "leave it alone" and the button would appear to work.
 */
export const SceneRangeForm: React.FC<IProps> = ({
  sceneID,
  file,
  onClose,
}) => {
  const intl = useIntl();
  const Toast = useToast();
  const [updateScene] = useSceneUpdate();

  const [start, setStart] = useState<number | null>(file.start_time ?? null);
  const [end, setEnd] = useState<number | null>(file.end_time ?? null);
  const [saving, setSaving] = useState(false);

  // The file's OWN length, needed as the ceiling for `end`.
  //
  // `file.duration` cannot be it: GetFiles REWRITES Duration to `end - start` for a windowed
  // scene, so a scene windowed 0..300 inside a 1800s file reports 300, and using that as the
  // maximum would forbid widening the window to anything longer than it already is.
  //
  // The algebra is exact rather than heuristic. GetFiles sets
  //
  //     Duration = clampedEnd - start
  //
  // and clampedEnd is the file duration for an open-ended window (sceneFileRanges substitutes
  // it), so `Duration + start` is the file's length in every case:
  //
  //   no window        start=0    -> Duration + 0    = the file
  //   open-ended       start=S    -> (file - S) + S  = the file
  //   bounded          start=S    -> (E - S) + S    = E, which is <= the file
  //
  // The bounded case gives the window's end rather than the file's length, which is the right
  // ceiling anyway: an end beyond the current one is beyond this scene's own range, and the
  // server refuses anything past the real file.
  const fileDuration = file.duration + (file.start_time ?? 0);

  const validationError = validate(start, end, fileDuration, intl);

  async function onSave() {
    try {
      setSaving(true);
      await updateScene({
        variables: {
          input: {
            id: sceneID,
            // Explicit null, not undefined -- see the component doc comment.
            start_time: start,
            end_time: end,
            // The window belongs to a (scene, file) pair, so the file is named. Without it
            // the resolver REFUSES a multi-file scene rather than guessing which one.
            primary_file_id: file.id,
          },
        },
      });
      onClose();
    } catch (e) {
      Toast.error(e);
    } finally {
      setSaving(false);
    }
  }

  async function onClear() {
    try {
      setSaving(true);
      await updateScene({
        variables: {
          input: {
            id: sceneID,
            start_time: null,
            end_time: null,
            primary_file_id: file.id,
          },
        },
      });
      onClose();
    } catch (e) {
      Toast.error(e);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal show onClose={onClose}>
      <Modal.Header closeButton>
        <Modal.Title>
          <FormattedMessage id="media_info.range" />
        </Modal.Title>
      </Modal.Header>
      <Modal.Body>
        <Form.Group>
          <Form.Label>
            <FormattedMessage id="media_info.range_start" />
          </Form.Label>
          <DurationInput value={start} setValue={setStart} disabled={saving} />
        </Form.Group>
        <Form.Group>
          <Form.Label>
            <FormattedMessage id="media_info.range_end" />
          </Form.Label>
          <DurationInput
            value={end}
            setValue={setEnd}
            disabled={saving}
            // An empty end is an OPEN-ENDED window running to the end of the file, which is
            // legal and is not the same as "no window". The Clear button is how you get no
            // window; leaving this blank is how you get "to the end of the file".
            placeholder={intl.formatMessage(
              { id: "media_info.range_end_open" },
              { duration: TextUtils.secondsToTimestamp(fileDuration) },
            )}
          />
        </Form.Group>
        {validationError && (
          <div className="text-danger" data-test-id="range-error">
            {validationError}
          </div>
        )}
      </Modal.Body>
      <Modal.Footer>
        <Button
          variant="danger"
          onClick={onClear}
          disabled={saving}
          data-test-id="range-clear"
        >
          <FormattedMessage id="actions.clear_range" />
        </Button>
        <Button variant="secondary" onClick={onClose} disabled={saving}>
          <FormattedMessage id="actions.cancel" />
        </Button>
        <Button
          variant="primary"
          onClick={onSave}
          disabled={saving || validationError !== undefined}
          data-test-id="range-save"
        >
          <FormattedMessage id="actions.save" />
        </Button>
      </Modal.Footer>
    </Modal>
  );
};

/**
 * validate returns an error message, or undefined when the window is acceptable.
 *
 * Exported for tests: the rules are the feature, and a rule reachable only through a modal is a
 * rule nobody exercises. The same two constraints the API enforces, minus the ones only the API
 * can know (whether the file exists, whether the scene is multi-file).
 */
export function validate(
  start: number | null,
  end: number | null,
  fileDuration: number,
  intl: { formatMessage(msg: { id: string }, values?: Record<string, unknown>): string },
): string | undefined {
  if (start !== null && start < 0) {
    return intl.formatMessage({ id: "validation.range_start_negative" });
  }
  if (end !== null && end < 0) {
    return intl.formatMessage({ id: "validation.range_end_negative" });
  }
  // The zero-length case matters and is easy to fold into this one by accident: `end == start`
  // is a window with no frames, which every derived artefact (sprite grid, preview) would
  // render empty. The database CHECK refuses it for the same reason.
  if (start !== null && end !== null && end <= start) {
    return intl.formatMessage({ id: "validation.range_end_before_start" });
  }
  // An overrunning end is refused rather than clamped, matching the API: clamping on write would
  // save a number the user did not type, and the file would then report a different one back.
  if (end !== null && end > fileDuration) {
    return intl.formatMessage(
      { id: "validation.range_end_past_file" },
      { duration: TextUtils.secondsToTimestamp(fileDuration) },
    );
  }
  return undefined;
}