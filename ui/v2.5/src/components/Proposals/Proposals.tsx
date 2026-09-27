import React, { useCallback } from "react";
import { Button, Card, Form, Modal, Table } from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";
import * as GQL from "src/core/generated-graphql";
import {
  useModerationQueue,
  usePendingMyVote,
  usePropose,
  useProposals,
  useVote,
  useWithdraw,
} from "src/core/StashService";

// The proposal UI.
//
// The design constraint that shapes all of this: a proposal is NOT an edit. The
// value does not change when you submit one, and it does not change when you
// vote for one. It changes when the proposal settles, and that is supposed to be
// visible — so every surface here says what a vote does rather than optimistically
// showing the new value as though it had been applied.
//
// The optimistic-update temptation is worth naming because it is the single
// easiest way to make this lie. A vote returns the proposal, so the new score is
// right there, and rendering `newValue` as the title's current value would be
// smooth and wrong.

interface ProposalRowProps {
  proposal: GQL.EditProposalDataFragment;
  onVote: (id: string, value: number) => void;
  onWithdraw: (id: string) => void;
}

// StatusBadge renders the lifecycle state as a word, not a colour.
//
// Colour alone would make "superseded" and "rejected" indistinguishable to
// anyone who cannot separate the hues, and those two mean opposite things: one
// was replaced by a newer claim, the other was turned down.
const StatusBadge: React.FC<{ status: string }> = ({ status }) => {
  const intl = useIntl();
  const variant =
    status === "accepted"
      ? "success"
      : status === "rejected"
        ? "danger"
        : "secondary";
  return (
    <Card.Text className={`text-${variant}`}>
      <FormattedMessage
        id="proposal.status.label"
        values={{
          status: intl.formatMessage({ id: `proposal.status.${status}` }),
        }}
      />
    </Card.Text>
  );
};

const ProposalRow: React.FC<ProposalRowProps> = ({
  proposal,
  onVote,
  onWithdraw,
}) => {
  const intl = useIntl();

  // blockedReason comes from the server and is null when the caller may act.
  // Driving the disabled state from it rather than re-deriving the rule here
  // means the UI can never disagree with the server about who may vote — and
  // the server is the one that enforces it.
  const blocked = proposal.blockedReason;
  const decidable = proposal.status === "open";

  return (
    <tr>
      <td>
        {proposal.targetType}/{proposal.targetId}
        <div className="text-muted small">
          {intl.formatMessage(
            { id: "proposal.field" },
            { field: proposal.field }
          )}
        </div>
      </td>
      <td>
        {proposal.oldValue ?? (
          <em>{intl.formatMessage({ id: "proposal.unset" })}</em>
        )}
        {" → "}
        {proposal.newValue ?? (
          <em>{intl.formatMessage({ id: "proposal.cleared" })}</em>
        )}
      </td>
      <td>{proposal.author?.username}</td>
      <td>
        {/* The score is what people actually decide on, so it is the loudest
            number here. voters is shown beside it because a score of 3 from 3
            voters and a score of 3 from 30 are different claims. */}
        <strong>{proposal.score}</strong>
        <span className="text-muted small">
          {intl.formatMessage(
            { id: "proposal.voters" },
            { count: proposal.voters }
          )}
        </span>
      </td>
      <td>
        <StatusBadge status={proposal.status} />
      </td>
      <td>
        {decidable && (
          <>
            <Button
              size="sm"
              variant={proposal.myVote === 1 ? "success" : "outline-success"}
              disabled={!!blocked}
              title={blocked ?? undefined}
              onClick={() => onVote(proposal.id, 1)}
            >
              <FormattedMessage id="proposal.vote.for" />
            </Button>{" "}
            <Button
              size="sm"
              variant={proposal.myVote === -1 ? "danger" : "outline-danger"}
              disabled={!!blocked}
              title={blocked ?? undefined}
              onClick={() => onVote(proposal.id, -1)}
            >
              <FormattedMessage id="proposal.vote.against" />
            </Button>{" "}
            <Button
              size="sm"
              variant="outline-secondary"
              disabled={!!blocked}
              title={blocked ?? undefined}
              onClick={() => onWithdraw(proposal.id)}
            >
              <FormattedMessage id="proposal.withdraw" />
            </Button>
            {blocked && <div className="text-muted small">{blocked}</div>}
          </>
        )}
      </td>
    </tr>
  );
};

interface ProposalListProps {
  proposals: GQL.EditProposalDataFragment[];
  onVote: (id: string, value: number) => void;
  onWithdraw: (id: string) => void;
}

const ProposalList: React.FC<ProposalListProps> = ({
  proposals,
  onVote,
  onWithdraw,
}) => {
  if (proposals.length === 0) {
    return (
      <Card.Body className="text-center text-muted">
        <FormattedMessage id="proposal.empty" />
      </Card.Body>
    );
  }

  return (
    <Table hover responsive>
      <thead>
        <tr>
          <th>
            <FormattedMessage id="proposal.target" />
          </th>
          <th>
            <FormattedMessage id="proposal.change" />
          </th>
          <th>
            <FormattedMessage id="proposal.author" />
          </th>
          <th>
            <FormattedMessage id="proposal.score" />
          </th>
          <th>
            <FormattedMessage id="proposal.status.plain" />
          </th>
          <th />
        </tr>
      </thead>
      <tbody>
        {proposals.map((p) => (
          <ProposalRow
            key={p.id}
            proposal={p}
            onVote={onVote}
            onWithdraw={onWithdraw}
          />
        ))}
      </tbody>
    </Table>
  );
};

// ProposeDialog creates a proposal. It is a form, not an editor: the only thing
// it writes is the proposal, and the field it is proposing for is chosen from a
// fixed list rather than typed, because the server validates against a closed
// vocabulary and a free-text field name would just be a way to get a 422.
const ProposeDialog: React.FC<{
  show: boolean;
  onHide: () => void;
  targetType: string;
  targetId: string;
  onCreated?: () => void;
}> = ({ show, onHide, targetType, targetId, onCreated }) => {
  const intl = useIntl();
  const [createProposal, { loading, error }] = usePropose();

  const handleSubmit = useCallback(
    (e: React.FormEvent<HTMLFormElement>) => {
      e.preventDefault();
      const form = new FormData(e.currentTarget);
      const newValue = (form.get("newValue") as string) || null;
      createProposal({
        variables: {
          input: {
            targetType,
            targetId,
            field: (form.get("field") as string) ?? "",
            newValue,
            rationale: (form.get("rationale") as string) || null,
          },
        },
      }).then(() => {
        onCreated?.();
        onHide();
      });
    },
    [createProposal, onCreated, onHide, targetId, targetType]
  );

  return (
    <Modal show={show} onHide={onHide}>
      <Modal.Header closeButton>
        <Modal.Title>
          <FormattedMessage id="proposal.propose" />
        </Modal.Title>
      </Modal.Header>
      <Form onSubmit={handleSubmit}>
        <Modal.Body>
          {error && <div className="text-danger">{error.message}</div>}
          <Form.Group className="mb-3">
            <Form.Label>
              <FormattedMessage id="proposal.field" />
            </Form.Label>
            {/* Free text on purpose-not-free-text: the server rejects anything
                outside the vocabulary, and a dropdown cannot produce a value the
                server will refuse. The list is short and stable. */}
            <Form.Control as="select" name="field" required>
              <option value="title">
                {intl.formatMessage({ id: "title" })}
              </option>
              <option value="details">
                {intl.formatMessage({ id: "details" })}
              </option>
              <option value="date">{intl.formatMessage({ id: "date" })}</option>
            </Form.Control>
          </Form.Group>
          <Form.Group className="mb-3">
            <Form.Label>
              <FormattedMessage id="proposal.newValue" />
            </Form.Label>
            {/* Empty is sent as null, which the server reads as "clear this
                field" — a different edit from setting it to an empty string.
                Leaving this blank is therefore a real action, and the label
                says so. */}
            <Form.Control name="newValue" />
            <Form.Text>
              <FormattedMessage id="proposal.newValue.hint" />
            </Form.Text>
          </Form.Group>
          <Form.Group className="mb-3">
            <Form.Label>
              <FormattedMessage id="proposal.rationale" />
            </Form.Label>
            <Form.Control name="rationale" as="textarea" rows={3} />
          </Form.Group>
        </Modal.Body>
        <Modal.Footer>
          <Button variant="secondary" onClick={onHide}>
            <FormattedMessage id="buttons.cancel" />
          </Button>
          <Button variant="primary" type="submit" disabled={loading}>
            <FormattedMessage id="proposal.propose" />
          </Button>
        </Modal.Footer>
      </Form>
    </Modal>
  );
};

export const Proposals: React.FC = () => {
  const intl = useIntl();
  const [statusFilter, setStatusFilter] = React.useState<string | undefined>(
    undefined
  );
  const [mineOnly, setMineOnly] = React.useState(false);
  const [showPropose, setShowPropose] = React.useState(false);

  // The service hooks take the variables directly and set fetchPolicy
  // themselves — "network-only" because a cached proposal list is a list of
  // claims about content that may have settled since, and a stale score is worse
  // than no score.
  const { data, loading, error, refetch } = useProposals({
    status: statusFilter,
    mine: mineOnly || undefined,
    limit: 100,
  });
  const { data: queueData, error: queueError } = useModerationQueue({
    limit: 50,
  });
  const { data: pendingData } = usePendingMyVote({ limit: 50 });

  const [vote] = useVote();
  const [withdraw] = useWithdraw();

  // Every mutation re-reads rather than patching the local copy. The score is
  // recomputed server-side from the vote rows on every read, and a local
  // increment would have to replicate that rule to stay correct — including the
  // part where voting twice replaces the earlier vote. Getting that subtly wrong
  // is worse than an extra round trip.
  const doVote = useCallback(
    (id: string, value: number) => {
      vote({ variables: { proposalId: id, value } }).then(() => {
        void refetch();
      });
    },
    [refetch, vote]
  );

  const doWithdraw = useCallback(
    (id: string) => {
      withdraw({ variables: { proposalId: id } }).then(() => {
        void refetch();
      });
    },
    [refetch, withdraw]
  );

  if (error) {
    return <div className="text-danger">{error.message}</div>;
  }

  return (
    <div className="ProposalPage">
      <h1>
        <FormattedMessage id="proposal.nav" />
      </h1>

      <div className="mb-3 d-flex gap-2 align-items-center">
        <Form.Control
          as="select"
          value={statusFilter ?? ""}
          onChange={(e: React.ChangeEvent<HTMLSelectElement>) =>
            setStatusFilter(e.target.value || undefined)
          }
          style={{ maxWidth: "16rem" }}
        >
          <option value="">
            {intl.formatMessage({ id: "proposal.filter.all" })}
          </option>
          <option value="open">
            {intl.formatMessage({ id: "proposal.status.open" })}
          </option>
          <option value="accepted">
            {intl.formatMessage({ id: "proposal.status.accepted" })}
          </option>
          <option value="rejected">
            {intl.formatMessage({ id: "proposal.status.rejected" })}
          </option>
        </Form.Control>
        <Form.Check
          type="switch"
          id="mine-only"
          checked={mineOnly}
          onChange={(e) => setMineOnly(e.target.checked)}
          label={intl.formatMessage({ id: "proposal.filter.mine" })}
        />
      </div>

      {loading && (
        <div className="text-muted">
          <FormattedMessage id="loading.generic" />
        </div>
      )}

      <ProposalList
        proposals={data?.proposals ?? []}
        onVote={doVote}
        onWithdraw={doWithdraw}
      />

      {pendingData?.pendingMyVote && pendingData.pendingMyVote.length > 0 && (
        <Card className="mt-4">
          <Card.Header>
            <FormattedMessage id="proposal.pending" />
          </Card.Header>
          <ProposalList
            proposals={pendingData.pendingMyVote}
            onVote={doVote}
            onWithdraw={doWithdraw}
          />
        </Card>
      )}

      {/* The queue is rendered only when the server returned it. A non-moderator
          gets an error rather than an empty list, which is the honest answer:
          "you may not see this" is not "there is nothing here". The client
          deliberately does not hide the section on a client-side role check,
          because the server's answer is the one that counts. */}
      {queueData?.moderationQueue && (
        <Card className="mt-4">
          <Card.Header>
            <FormattedMessage id="proposal.moderationQueue" />
          </Card.Header>
          <ProposalList
            proposals={queueData.moderationQueue}
            onVote={doVote}
            onWithdraw={doWithdraw}
          />
        </Card>
      )}
      {queueError && (
        <Card className="mt-4">
          <Card.Header>
            <FormattedMessage id="proposal.moderationQueue" />
          </Card.Header>
          <Card.Body className="text-muted">{queueError.message}</Card.Body>
        </Card>
      )}

      <ProposeDialog
        show={showPropose}
        onHide={() => setShowPropose(false)}
        targetType="scene"
        targetId="1"
      />
      <Button variant="primary" onClick={() => setShowPropose(true)}>
        <FormattedMessage id="proposal.propose" />
      </Button>
    </div>
  );
};

export default Proposals;
