import React, { useState } from "react";
import {
  Button,
  Col,
  Form,
  OverlayTrigger,
  Popover,
  Row,
} from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";
import { IconDefinition } from "@fortawesome/fontawesome-svg-core";
import { ModalComponent } from "./Modal";
import { Icon } from "./Icon";
import {
  faClipboard,
  faFile,
  faLink,
  faTrashAlt,
} from "@fortawesome/free-solid-svg-icons";
import { PatchComponent } from "src/patch";
import ImageUtils from "src/utils/image";
import { useToast } from "src/hooks/Toast";

export interface IImageInputExtraAction {
  icon: IconDefinition;
  labelId: string;
  onClick: () => void | Promise<void>;
}

interface IImageInput {
  isEditing: boolean;
  text?: string;
  onImageChange: (event: React.ChangeEvent<HTMLInputElement>) => void;
  onImageURL?: (url: string) => void;
  onReset?: () => void;
  acceptSVG?: boolean;
  extraActions?: IImageInputExtraAction[];
}

function acceptExtensions(acceptSVG: boolean = false) {
  return `.jpg,.jpeg,.png,.webp,.gif${acceptSVG ? ",.svg" : ""}`;
}

// #7256. The file input needs a stable id so a <label htmlFor> can point at
// it. React 17 has no useId, so this counter does the job: it is assigned once
// per mounted ImageInput and never reused, which is all htmlFor requires. It
// does not need to be unique across page loads or across sessions, only among
// the inputs on the current page -- two ImageInputs open at once must not share
// an id, or the second label would activate the first input.
let fileInputSeq = 0;
function nextFileInputId() {
  fileInputSeq += 1;
  return `image-file-input-${fileInputSeq}`;
}

export const ImageInput: React.FC<IImageInput> = PatchComponent(
  "ImageInput",
  ({
    isEditing,
    text,
    onImageChange,
    onImageURL,
    onReset,
    acceptSVG = false,
    extraActions,
  }) => {
    const [isShowDialog, setIsShowDialog] = useState(false);
    const [url, setURL] = useState("");
    // #7256 - the id for the file input this component owns. See
    // nextFileInputId for why it is a counter and not useId.
    const [fileInputId] = useState(nextFileInputId);
    const intl = useIntl();
    const Toast = useToast();
    if (!isEditing) return <div />;

    if (!onImageURL) {
      // just return the file input
      //
      // The <label> is the button, not a <button> wrapping the input. An
      // interactive element inside a <button> is invalid HTML, and Firefox
      // refuses to activate the nested file input -- which is #7256. A label
      // with htmlFor is the standard, valid way to make a styled control open a
      // file picker.
      return (
        <Form.Label className="image-input">
          <label className="btn btn-secondary" htmlFor={fileInputId}>
            {text ?? <FormattedMessage id="actions.browse_for_image" />}
          </label>
          <Form.Control
            id={fileInputId}
            type="file"
            onChange={onImageChange}
            accept={acceptExtensions(acceptSVG)}
          />
        </Form.Label>
      );
    }

    async function onPasteClipboard() {
      try {
        const data = await ImageUtils.readClipboardImage();
        if (data && onImageURL) {
          onImageURL(data);
          Toast.success(
            intl.formatMessage({ id: "toast.clipboard_image_pasted" })
          );
        } else {
          Toast.error(intl.formatMessage({ id: "toast.clipboard_no_image" }));
        }
      } catch (e) {
        if (e instanceof DOMException && e.name === "NotAllowedError") {
          Toast.error(
            intl.formatMessage({ id: "toast.clipboard_access_denied" })
          );
        } else {
          Toast.error(e);
        }
      }
    }

    function showDialog() {
      setURL("");
      setIsShowDialog(true);
    }

    function onConfirmURL() {
      if (!onImageURL) {
        return;
      }

      setIsShowDialog(false);
      onImageURL(url);
    }

    function renderDialog() {
      return (
        <ModalComponent
          show={!!isShowDialog}
          onHide={() => setIsShowDialog(false)}
          header={intl.formatMessage({ id: "dialogs.set_image_url_title" })}
          accept={{
            onClick: onConfirmURL,
            text: intl.formatMessage({ id: "actions.confirm" }),
          }}
        >
          <div className="dialog-content">
            <Form.Group controlId="url" as={Row}>
              <Form.Label column xs={3}>
                <FormattedMessage id="url" />
              </Form.Label>
              <Col xs={9}>
                <Form.Control
                  className="text-input"
                  onChange={(event: React.ChangeEvent<HTMLInputElement>) =>
                    setURL(event.currentTarget.value)
                  }
                  value={url}
                  placeholder={intl.formatMessage({ id: "url" })}
                />
              </Col>
            </Form.Group>
          </div>
        </ModalComponent>
      );
    }

    const popover = (
      <Popover id="set-image-popover">
        <Popover.Content>
          <div>
            <span className="image-input">
              {/* #7256. This was a <Button> with the file input nested inside
                  it, which is invalid HTML -- an interactive element inside a
                  button -- and Firefox never opened the picker. "From URL" kept
                  working because that button has an onClick and no nested
                  input, which is exactly the contrast in the bug report. A
                  label with htmlFor is the valid, browser-independent
                  equivalent and renders identically because the .btn.minimal
                  class is applied here too. */}
              <label className="btn minimal" htmlFor={fileInputId}>
                <Icon icon={faFile} className="fa-fw" />
                <span>
                  <FormattedMessage id="actions.from_file" />
                </span>
              </label>
              <Form.Control
                id={fileInputId}
                type="file"
                onChange={onImageChange}
                accept={acceptExtensions(acceptSVG)}
              />
            </span>
          </div>
          <div>
            <Button className="minimal" onClick={showDialog}>
              <Icon icon={faLink} className="fa-fw" />
              <span>
                <FormattedMessage id="actions.from_url" />
              </span>
            </Button>
          </div>
          {window.isSecureContext && (
            <div>
              <Button className="minimal" onClick={onPasteClipboard}>
                <Icon icon={faClipboard} className="fa-fw" />
                <span>
                  <FormattedMessage id="actions.from_clipboard" />
                </span>
              </Button>
            </div>
          )}
          {extraActions && extraActions.length > 0 && (
            <div className="set-image-menu-divider" />
          )}
          {extraActions?.map((action) => (
            <div key={action.labelId}>
              <Button className="minimal" onClick={action.onClick}>
                <Icon icon={action.icon} className="fa-fw" />
                <span>
                  <FormattedMessage id={action.labelId} />
                </span>
              </Button>
            </div>
          ))}
          {onReset && (
            <>
              <div className="set-image-menu-divider" />
              <div>
                <Button className="minimal" onClick={onReset}>
                  <Icon icon={faTrashAlt} className="fa-fw" />
                  <span>
                    <FormattedMessage id="actions.clear_image" />
                  </span>
                </Button>
              </div>
            </>
          )}
        </Popover.Content>
      </Popover>
    );

    return (
      <>
        {renderDialog()}
        <OverlayTrigger
          trigger="click"
          placement="top"
          overlay={popover}
          rootClose
        >
          <Button variant="secondary" className="mr-2">
            {text ?? <FormattedMessage id="actions.set_image" />}
          </Button>
        </OverlayTrigger>
      </>
    );
  }
);
