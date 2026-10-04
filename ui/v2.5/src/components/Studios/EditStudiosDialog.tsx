import React, { useEffect, useMemo, useState } from "react";
import { Form } from "react-bootstrap";
import { useIntl } from "react-intl";
import { useBulkStudioUpdate } from "src/core/StashService";
import * as GQL from "src/core/generated-graphql";
import { ModalComponent } from "../Shared/Modal";
import { useToast } from "src/hooks/Toast";
import { MultiSet } from "../Shared/MultiSet";
import { RatingSystem } from "../Shared/Rating/RatingSystem";
import {
  getAggregateInputValue,
  getAggregateState,
  getAggregateStateObject,
} from "src/utils/bulkUpdate";
import { IndeterminateCheckbox } from "../Shared/IndeterminateCheckbox";
import { BulkUpdateFormGroup, BulkUpdateTextInput } from "../Shared/BulkUpdate";
import { StringListInput } from "../Shared/StringListInput";
import { faPencilAlt } from "@fortawesome/free-solid-svg-icons";
import { StudioSelect } from "../Shared/Select";

interface IListOperationProps {
  selected: GQL.SlimStudioDataFragment[];
  onClose: (applied: boolean) => void;
}

const studioFields = [
  "favorite",
  "rating100",
  "details",
  "ignore_auto_tag",
  "organized",
  // stash#2359 (#2607, #3051). Codes join the bulk-updatable fields so a multi-select can set them
  // the same way it sets aliases; a bulk edit that could not touch codes would leave codes as the
  // one studio field only reachable one studio at a time.
  "codes",
];

export const EditStudiosDialog: React.FC<IListOperationProps> = (
  props: IListOperationProps
) => {
  const intl = useIntl();
  const Toast = useToast();

  const [updateInput, setUpdateInput] = useState<GQL.BulkStudioUpdateInput>({
    ids: props.selected.map((studio) => {
      return studio.id;
    }),
  });

  const [tagIds, setTagIds] = useState<GQL.BulkUpdateIds>({
    mode: GQL.BulkUpdateIdMode.Add,
  });

  // stash#2359. Codes are a BulkUpdateStrings rather than a plain string[] so that NOT touching
  // them is distinguishable from clearing them -- the same tri-state the backend depends on. A
  // plain string[] would make an unrelated bulk edit wipe every studio's codes.
  // `values`, not `ids`: BulkUpdateStrings is the STRING list variant. `values` stays undefined
  // until the user types something, which is what keeps "did not touch" distinct from "cleared".
  const [codes, setCodes] = useState<GQL.BulkUpdateStrings>({
    mode: GQL.BulkUpdateIdMode.Set,
  });

  const unsetDisabled = props.selected.length < 2;

  const [updateStudios] = useBulkStudioUpdate();

  // Network state
  const [isUpdating, setIsUpdating] = useState(false);

  const aggregateState = useMemo(() => {
    const updateState: Partial<GQL.BulkStudioUpdateInput> = {};
    const state = props.selected;
    let updateTagIds: string[] = [];
    let first = true;

    state.forEach((studio: GQL.SlimStudioDataFragment) => {
      getAggregateStateObject(updateState, studio, studioFields, first);

      // studio data fragment doesn't have parent_id, so handle separately
      updateState.parent_id = getAggregateState(
        updateState.parent_id,
        studio.parent_studio?.id,
        first
      );

      const studioTagIDs = (studio.tags ?? []).map((p) => p.id).sort();

      updateTagIds = getAggregateState(updateTagIds, studioTagIDs, first) ?? [];

      first = false;
    });

    return { state: updateState, tagIds: updateTagIds };
  }, [props.selected]);

  // update initial state from aggregate
  useEffect(() => {
    setUpdateInput((current) => ({ ...current, ...aggregateState.state }));
  }, [aggregateState]);

  function setUpdateField(input: Partial<GQL.BulkStudioUpdateInput>) {
    setUpdateInput((current) => ({ ...current, ...input }));
  }

  function getStudioInput(): GQL.BulkStudioUpdateInput {
    const studioInput: GQL.BulkStudioUpdateInput = {
      ...updateInput,
      tag_ids: tagIds,
      // stash#2359. Only sent when the user actually typed something. A BulkUpdateStrings with an
      // empty `ids` and mode SET means "clear every code on every selected studio", which is a
      // legitimate action but must not be the default for opening a dialog and pressing save.
      // `values` is `Maybe<Array<String>>`, so null and undefined are both possible and both mean
      // "not touched". A length check alone does not narrow it, hence the explicit null guard.
      ...(codes.values != null && codes.values.length > 0
        ? { codes: { values: codes.values, mode: GQL.BulkUpdateIdMode.Set } }
        : {}),
    };

    // we don't have unset functionality for the rating star control
    // so need to determine if we are setting a rating or not
    studioInput.rating100 = getAggregateInputValue(
      updateInput.rating100,
      aggregateState.state.rating100
    );

    return studioInput;
  }

  async function onSave() {
    setIsUpdating(true);
    try {
      await updateStudios({
        variables: {
          input: getStudioInput(),
        },
      });
      Toast.success(
        intl.formatMessage(
          { id: "toast.updated_entity" },
          {
            entity: intl.formatMessage({ id: "studios" }).toLocaleLowerCase(),
          }
        )
      );
      props.onClose(true);
    } catch (e) {
      Toast.error(e);
    }
    setIsUpdating(false);
  }

  function render() {
    return (
      <ModalComponent
        dialogClassName="edit-studios-dialog"
        show
        icon={faPencilAlt}
        header={intl.formatMessage(
          { id: "dialogs.edit_entity_count_title" },
          {
            count: props?.selected?.length ?? 1,
            singularEntity: intl.formatMessage({ id: "studio" }),
            pluralEntity: intl.formatMessage({ id: "studios" }),
          }
        )}
        accept={{
          onClick: onSave,
          text: intl.formatMessage({ id: "actions.apply" }),
        }}
        cancel={{
          onClick: () => props.onClose(false),
          text: intl.formatMessage({ id: "actions.cancel" }),
          variant: "secondary",
        }}
        isRunning={isUpdating}
      >
        <Form>
          <BulkUpdateFormGroup name="parent-studio" messageId="parent_studio">
            <StudioSelect
              onSelect={(items) =>
                setUpdateField({
                  parent_id: items.length > 0 ? items[0]?.id : undefined,
                })
              }
              ids={updateInput.parent_id ? [updateInput.parent_id] : []}
              isDisabled={isUpdating}
              menuPortalTarget={document.body}
            />
          </BulkUpdateFormGroup>
          <BulkUpdateFormGroup name="rating">
            <RatingSystem
              value={updateInput.rating100}
              onSetRating={(value) =>
                setUpdateField({ rating100: value ?? undefined })
              }
              disabled={isUpdating}
            />
          </BulkUpdateFormGroup>

          <Form.Group controlId="favorite">
            <IndeterminateCheckbox
              setChecked={(checked) => setUpdateField({ favorite: checked })}
              checked={updateInput.favorite ?? undefined}
              label={intl.formatMessage({ id: "favourite" })}
            />
          </Form.Group>

          <BulkUpdateFormGroup name="tags" inline={false}>
            <MultiSet
              type={"tags"}
              disabled={isUpdating}
              onUpdate={(itemIDs) => {
                setTagIds((c) => ({ ...c, ids: itemIDs }));
              }}
              onSetMode={(newMode) => {
                setTagIds((c) => ({ ...c, mode: newMode }));
              }}
              ids={tagIds.ids ?? []}
              existingIds={aggregateState.tagIds}
              mode={tagIds.mode}
              menuPortalTarget={document.body}
            />
          </BulkUpdateFormGroup>

          <BulkUpdateFormGroup name="details" inline={false}>
            <BulkUpdateTextInput
              value={updateInput.details}
              valueChanged={(newValue) => setUpdateField({ details: newValue })}
              unsetDisabled={unsetDisabled}
              as="textarea"
            />
          </BulkUpdateFormGroup>

          {/* stash#2359 (#2607, #3051) -- Stash-Box codes: IMDB, TMDB, R18 and friends. One
              StringListInput rather than a MultiSet because a code is an opaque identifier with no
              local entity behind it, so there is nothing to select FROM. */}
          <BulkUpdateFormGroup name="codes" inline={false}>
            <StringListInput
              value={codes.values ?? []}
              setValue={(values) =>
                setCodes((c) => ({
                  ...c,
                  values,
                  mode: GQL.BulkUpdateIdMode.Set,
                }))
              }
              readOnly={isUpdating}
            />
          </BulkUpdateFormGroup>

          <Form.Group controlId="ignore-auto-tags">
            <IndeterminateCheckbox
              label={intl.formatMessage({ id: "ignore_auto_tag" })}
              setChecked={(checked) =>
                setUpdateField({ ignore_auto_tag: checked })
              }
              checked={updateInput.ignore_auto_tag ?? undefined}
            />
          </Form.Group>

          <Form.Group controlId="organized">
            <IndeterminateCheckbox
              label={intl.formatMessage({ id: "organized" })}
              setChecked={(checked) => setUpdateField({ organized: checked })}
              checked={updateInput.organized ?? undefined}
            />
          </Form.Group>
        </Form>
      </ModalComponent>
    );
  }

  return render();
};
