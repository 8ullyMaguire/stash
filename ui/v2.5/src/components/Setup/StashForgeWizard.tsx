import React, { useCallback, useEffect, useState } from "react";
import { FormattedMessage, useIntl } from "react-intl";
import { Alert, Button, Card, Form } from "react-bootstrap";
import { useHistory } from "react-router-dom";

import { LoadingIndicator } from "../Shared/LoadingIndicator";

/**
 * The first-run wizard: the one decision that decides whether this instance is
 * reachable by the internet.
 *
 * # WHY THIS CALLS THE HTTP ENDPOINT AND NOT GraphQL
 *
 * `POST /stashforge/wizard` is the only unauthenticated POST in the
 * application, authenticated by possession of the instance key rather than by a
 * session — because the operator has not logged in yet, which is the situation
 * the wizard exists to resolve. There is no session to send, so there is no
 * GraphQL mutation to call. `GET /stashforge/mode` and `GET /stashforge/wizard`
 * are likewise unauthenticated, and read nothing sensitive.
 *
 * The consequence worth stating: the mode is chosen ONCE here. Changing a live
 * instance's mode afterwards is a separate authenticated operation that
 * deliberately does not exist yet. A wizard that could be re-run would let
 * anyone who reaches the port flip a public instance to private (or worse, the
 * reverse), so the server refuses a second POST with `wizard_already_completed`.
 *
 * # WHY THE MODE IS CHOSEN BY A PERSON, WITH THE CONSEQUENCES SPELLED OUT
 *
 * `collab.CheckStartup` refuses a public mode until the wizard has been
 * completed, even if the settings row already says public. A mode set by a
 * migration, a restored backup or a hand-edit is not a choice, and "somebody
 * chose to be public" is the only thing that should permit it. That is why this
 * screen exists rather than a default: the default is the safe one, and the
 * unsafe one has to be typed on purpose.
 */

type WizardState = {
  completed: boolean;
  mode?: string;
  requiresTLS?: boolean;
};

type WizardRefusal = {
  error: string;
  code: string;
};

const WIZARD_ENDPOINT = "/stashforge/wizard";

/**
 * POST JSON to the wizard endpoint.
 *
 * Written here rather than pulled into src/utils because there is exactly one
 * non-GraphQL endpoint in the whole application, and a shared helper for a single
 * caller is a helper somebody has to keep general. The 4 KiB body matches the
 * server's own `wizardBodyLimit`; sending more is refused there, so there is no
 * reason to construct more here.
 */
async function postWizard(body: {
  mode: string;
  instanceKey: string;
}): Promise<Response> {
  return fetch(WIZARD_ENDPOINT, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

/** The three modes, in the order the server's own message lists them. */
const MODES = [
  {
    value: "private",
    label: "setup.wizard.mode.private",
    blurb: "setup.wizard.mode.private.blurb",
  },
  {
    value: "contribute",
    label: "setup.wizard.mode.contribute",
    blurb: "setup.wizard.mode.contribute.blurb",
  },
  {
    value: "public",
    label: "setup.wizard.mode.public",
    blurb: "setup.wizard.mode.public.blurb",
  },
] as const;

const Wizard: React.FC = () => {
  const intl = useIntl();
  const history = useHistory();

  const [state, setState] = useState<WizardState | null>(null);
  const [mode, setMode] = useState<string>("private");
  const [instanceKey, setInstanceKey] = useState("");
  const [refusal, setRefusal] = useState<WizardRefusal | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);

  const load = useCallback(async () => {
    try {
      const res = await fetch(WIZARD_ENDPOINT, { credentials: "same-origin" });
      if (!res.ok) {
        setLoadFailed(true);
        return;
      }
      const body = (await res.json()) as WizardState;
      setState(body);
      // A decided instance keeps its mode shown, so the operator who lands here
      // later sees what was chosen rather than a blank form.
      if (body.completed && body.mode) {
        setMode(body.mode);
      }
    } catch (_e) {
      // A network failure and a refusal are different problems, and reporting
      // one as the other sends the operator to debug the wrong thing.
      setLoadFailed(true);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function decide() {
    setSubmitting(true);
    setRefusal(null);
    try {
      const res = await postWizard({ mode, instanceKey });
      const body = (await res.json()) as WizardState & WizardRefusal;

      if (!res.ok || body.code) {
        // The server's message is already in operator terms and names the
        // problem ("requests arrive as http"), so it is SHOWN rather than
        // replaced. What is NOT done is switching on an English string: `code`
        // is the contract, and the message is for the human.
        setRefusal({
          error: body.error ?? "unknown_error",
          code: body.code ?? "unknown",
        });
        return;
      }

      // Decided. Straight to the app — there is nothing between here and a
      // usable instance, and a wizard that lingers after success is a screen
      // the operator has to dismiss.
      history.push("/");
    } catch (_e) {
      setRefusal({
        error: "could not reach the server",
        code: "network_error",
      });
    } finally {
      setSubmitting(false);
    }
  }

  if (loadFailed) {
    return (
      <div className="SetupWizard">
        <Alert variant="danger">
          <FormattedMessage id="setup.wizard.load_failed" />
        </Alert>
      </div>
    );
  }

  if (state === null) {
    return <LoadingIndicator />;
  }

  // Already decided. The POST is refused server-side with
  // `wizard_already_completed`, so this screen does not offer a form it knows
  // will fail — it says what was chosen and sends the operator onward.
  if (state.completed) {
    return (
      <div className="SetupWizard">
        <Card>
          <Card.Header>
            <FormattedMessage id="setup.wizard.already_decided" />
          </Card.Header>
          <Card.Body>
            <p>
              <FormattedMessage
                id="setup.wizard.current_mode"
                values={{ mode: state.mode ?? "" }}
              />
            </p>
            <Button variant="primary" onClick={() => history.push("/")}>
              <FormattedMessage id="setup.wizard.continue" />
            </Button>
          </Card.Body>
        </Card>
      </div>
    );
  }

  return (
    <div className="SetupWizard">
      <Card>
        <Card.Header>
          <FormattedMessage id="setup.wizard.title" />
        </Card.Header>
        <Card.Body>
          <p>
            <FormattedMessage id="setup.wizard.intro" />
          </p>

          {refusal && (
            <Alert variant="danger">
              {/* The server's own words. It knows the observed scheme and the
                  valid modes; a client-side message would know neither. */}
              {refusal.error}
            </Alert>
          )}

          <Form.Group as="fieldset">
            <Form.Label as="legend">
              <FormattedMessage id="setup.wizard.mode.label" />
            </Form.Label>
            {MODES.map((m) => (
              <Form.Check
                key={m.value}
                type="radio"
                id={`wizard-mode-${m.value}`}
                name="wizard-mode"
                label={intl.formatMessage({ id: m.label })}
                checked={mode === m.value}
                onChange={() => setMode(m.value)}
              />
            ))}
            <Form.Text className="text-muted">
              {intl.formatMessage({
                id:
                  MODES.find((m) => m.value === mode)?.blurb ?? MODES[0].blurb,
              })}
            </Form.Text>
          </Form.Group>

          {/* The key is asked for here, and nowhere else. It is the same secret
              that encrypts 2FA secrets, so an operator manages exactly one. */}
          <Form.Group>
            <Form.Label>
              <FormattedMessage id="setup.wizard.instance_key" />
            </Form.Label>
            <Form.Control
              type="password"
              value={instanceKey}
              autoComplete="off"
              onChange={(e) => setInstanceKey(e.target.value)}
            />
            <Form.Text className="text-muted">
              <FormattedMessage id="setup.wizard.instance_key.help" />
            </Form.Text>
          </Form.Group>

          {/* A public mode is refused over plain HTTP, and the server would
              refuse it AFTER the form was submitted. Saying so here means the
              operator learns it before committing rather than from a boot
              failure whose only recovery is a hand-edit. */}
          {mode === "public" && !isSecureContext() && (
            <Alert variant="warning">
              <FormattedMessage id="setup.wizard.public_needs_tls" />
            </Alert>
          )}

          <Button
            variant="primary"
            onClick={decide}
            disabled={submitting || instanceKey.length === 0}
          >
            <FormattedMessage id="setup.wizard.decide" />
          </Button>
        </Card.Body>
      </Card>
    </div>
  );
};

/**
 * Whether the page is being served over HTTPS.
 *
 * `window.isSecureContext` rather than `location.protocol === "https:"` because
 * the instance may be behind a TLS-terminating proxy, where the browser sees
 * https even though the Go server saw http and honoured X-Forwarded-Proto. The
 * warning is therefore advisory either way: the SERVER is the authority, and
 * this only avoids showing a warning the server will not agree with.
 */
function isSecureContext(): boolean {
  return typeof window !== "undefined" && window.isSecureContext === true;
}

export default Wizard;
