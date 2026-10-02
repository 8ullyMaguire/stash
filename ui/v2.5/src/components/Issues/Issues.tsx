import React, { useCallback, useMemo, useState } from "react";
import { Button, Card, Form, Table } from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";

import * as GQL from "src/core/generated-graphql";
import {
  useFindIssues,
  useIssueCount,
  useIssueResolve,
  useIssueRestore,
} from "src/core/StashService";

// stash#837 — the issues panel.
//
// WHAT THIS PANEL IS FOR, AND THE CONSTRAINT THAT SHAPES ALL OF IT: a panel nobody opens
// is the outcome the issue complains about. So the bias throughout is AGAINST volume --
// nothing here auto-refreshes on a timer, nothing fires a notification, and the count in
// the nav is a number rather than a colour. A user who is told about forty things at once
// learns to ignore the badge, and then the one thing that mattered goes unread too.
//
// The single most load-bearing decision is that `details` is shown VERBATIM and never
// summarised. The store writes prose precisely so a human can read it ("byte-for-byte copy
// of scene_0001_Path"), and a panel that reformats it into "1 duplicate" has thrown away
// the only part that was specific to this library.

// KIND LABEL, NOT A COLOUR.
//
// The proposals panel in this codebase already made this call and the reason applies
// exactly: colour alone makes "duplicate" and "zero_duration" indistinguishable to anyone
// who cannot separate the hues, and those mean different things -- one is a redundancy, the
// other is a file that will not play. The kind is rendered as a word.
const kindLabelId = (kind: string) => `issue.kind.${kind}`;

// The row's type is the GENERATED FRAGMENT, not a hand-written shape.
//
// This is not tidiness: a hand-declared prop type is a second description of the Issue type
// that nothing keeps in step with the schema. The first version of this file declared
// `file_id: string | null` where codegen produces `file_id?: string | null`, and tsc
// rejected the assignment -- the compiler caught the drift. Copying the convention from
// Proposals.tsx means the only place Issue is described is generated-graphql.ts.
const IssueRow: React.FC<{
  issue: GQL.IssueDataFragment;
  onResolve: (id: string) => void;
  onRestore: (id: string) => void;
  busy: boolean;
}> = ({ issue, onResolve, onRestore, busy }) => {
  const intl = useIntl();

  return (
    <tr>
      <td>
        {/* The word, not a badge colour. A colour is not information for a user who
            cannot see it, and "duplicate" versus "zero_duration" are different problems
            needing different actions. */}
        <span className="text-muted small">
          {intl.formatMessage({ id: kindLabelId(issue.kind) })}
        </span>
      </td>
      <td>
        {/* VERBATIM. This is the whole value of the row: the store's prose names the
            actual file that is duplicated, and a summarised "1 duplicate" does not. */}
        {issue.details}
        {issue.file_id && (
          <div className="text-muted small">
            <FormattedMessage id="issue.file" values={{ id: issue.file_id }} />
          </div>
        )}
      </td>
      <td>
        <span className="text-muted small">{issue.detected_at}</span>
      </td>
      <td>
        {issue.resolved ? (
          // A dismissed finding keeps its restore button, and this is the point of having
          // it: a dismissal is a decision and decisions are wrong. Without this, "I
          // dismissed that by accident" is answered by editing the database, and the panel
          // teaches people that dismissals are permanent.
          <Button
            size="sm"
            variant="outline-secondary"
            disabled={busy}
            onClick={() => onRestore(issue.id)}
          >
            <FormattedMessage id="issue.restore" />
          </Button>
        ) : (
          <Button
            size="sm"
            variant="outline-secondary"
            disabled={busy}
            onClick={() => onResolve(issue.id)}
          >
            <FormattedMessage id="issue.resolve" />
          </Button>
        )}
      </td>
    </tr>
  );
};

export const Issues: React.FC = () => {
  const intl = useIntl();

  // The toggle is tri-state on purpose: undefined means "the server's default", which is
  // unresolved. Sending an explicit false would be the same request while making this
  // file the place that decides what the default IS -- and the default is a tested store
  // decision, not something a checkbox should restate.
  const [showResolved, setShowResolved] = useState<boolean | undefined>(
    undefined
  );
  const [kind, setKind] = useState("");
  const [busy, setBusy] = useState(false);

  const filter = useMemo(
    () => ({
      // Left undefined unless the user touched the toggle -- see above.
      ...(showResolved === undefined ? {} : { resolved: showResolved }),
      ...(kind ? { kind } : {}),
    }),
    [showResolved, kind]
  );

  const { data, loading, error, refetch } = useFindIssues({
    issue_filter: filter,
  });
  const { data: countData } = useIssueCount();
  const [resolve] = useIssueResolve();
  const [restore] = useIssueRestore();

  // REFETCH AFTER A MUTATION, NOT A LOCAL UPDATE.
  //
  // Resolving a finding changes what the DEFAULT list contains, so the row is gone from
  // this query's result. Splicing it out of local state would leave the list and the
  // server disagreeing, and the next query to run would put it straight back -- which the
  // user sees as the button not working.
  const act = useCallback(
    async (
      id: string,
      fn: (opts: { variables: { id: string } }) => Promise<unknown>
    ) => {
      setBusy(true);
      try {
        await fn({ variables: { id } });
        await refetch();
      } finally {
        setBusy(false);
      }
    },
    [refetch]
  );

  const issues = data?.findIssues.issues ?? [];
  const unresolved = countData?.issueCount.count ?? 0;

  if (error) {
    return (
      <Card>
        <Card.Text className="text-danger">
          <FormattedMessage id="issue.load_error" />
        </Card.Text>
      </Card>
    );
  }

  return (
    <div className="Issues">
      <div className="d-flex justify-content-between align-items-center mb-3">
        <h2>
          <FormattedMessage id="issues.title" />
          {unresolved > 0 && (
            // The badge is a plain number next to the heading, and it comes from
            // issueCount rather than from data.findIssues.count -- the latter is the count
            // of what the current filter matched, so it would shrink as the user filters
            // and the badge would start meaning something else.
            <span className="badge badge-secondary ml-2">{unresolved}</span>
          )}
        </h2>
        <Button
          size="sm"
          variant="outline-secondary"
          onClick={() => refetch()}
          disabled={loading || busy}
        >
          <FormattedMessage id="issues.refresh" />
        </Button>
      </div>

      <Form inline className="mb-3">
        <Form.Group className="mr-3">
          <Form.Label className="mr-2">
            <FormattedMessage id="issue.filter.kind" />
          </Form.Label>
          <Form.Control
            as="select"
            value={kind}
            onChange={(e: React.ChangeEvent<HTMLSelectElement>) =>
              setKind(e.target.value)
            }
          >
            <option value="">
              <FormattedMessage id="issue.filter.any_kind" />
            </option>
            <option value="duplicate">
              <FormattedMessage id="issue.kind.duplicate" />
            </option>
            <option value="zero_size">
              <FormattedMessage id="issue.kind.zero_size" />
            </option>
            <option value="zero_duration">
              <FormattedMessage id="issue.kind.zero_duration" />
            </option>
            <option value="no_files">
              <FormattedMessage id="issue.kind.no_files" />
            </option>
          </Form.Control>
        </Form.Group>

        <Form.Check
          type="switch"
          id="show-resolved"
          checked={showResolved === true}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
            setShowResolved(e.target.checked ? true : undefined)
          }
          label={intl.formatMessage({ id: "issue.filter.show_resolved" })}
        />
      </Form>

      {issues.length === 0 && !loading && (
        <Card>
          <Card.Text className="text-muted">
            {/* THE EMPTY STATE IS THE COMMON ONE and deserves a real message: a panel that
                says nothing when it is fine is indistinguishable from a panel that
                failed to load. */}
            <FormattedMessage id="issues.empty" />
          </Card.Text>
        </Card>
      )}

      {issues.length > 0 && (
        <Table hover responsive>
          <thead>
            <tr>
              <th>
                <FormattedMessage id="issue.column.kind" />
              </th>
              <th>
                <FormattedMessage id="issue.column.details" />
              </th>
              <th>
                <FormattedMessage id="issue.column.detected" />
              </th>
              <th />
            </tr>
          </thead>
          <tbody>
            {issues.map((issue) => (
              <IssueRow
                key={issue.id}
                issue={issue}
                busy={busy}
                onResolve={(id) => act(id, resolve)}
                onRestore={(id) => act(id, restore)}
              />
            ))}
          </tbody>
        </Table>
      )}
    </div>
  );
};

// Both a named and a default export, matching Proposals.tsx: the named one for any direct
// import, the default because App.tsx reaches this through lazyComponent(), which requires
// a default. Omitting it compiles as a module and then fails at runtime with "Property
// 'default' is missing" -- and tsc here only caught it because the lazyComponent type
// demands the shape.
export default Issues;
