package collab

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ApplyOutcome reports what a call to Apply did.
//
// The distinction between Applied and AlreadyApplied is not cosmetic. M3's
// periodic reconciliation re-applies every accepted proposal, so "I wrote it"
// and "it was already there and I correctly did nothing" have to be
// distinguishable, or the reconciler has no way to tell progress from a loop
// that is doing nothing forever.
type ApplyOutcome int

const (
	// ApplyWrote means the value was written to the target.
	ApplyWrote ApplyOutcome = iota

	// ApplyAlreadyCorrect means the target already held this value, so nothing
	// was written and no audit row was appended. This is what makes a re-apply
	// free rather than merely harmless.
	ApplyAlreadyCorrect

	// ApplyRejected means the value was no longer valid at apply time, so the
	// proposal was rejected with a reason instead of being written. The target
	// was not touched.
	ApplyRejected

	// ApplyTargetMissing means the row the proposal pointed at no longer exists.
	ApplyTargetMissing
)

func (o ApplyOutcome) String() string {
	switch o {
	case ApplyWrote:
		return "wrote"
	case ApplyAlreadyCorrect:
		return "already-correct"
	case ApplyRejected:
		return "rejected"
	case ApplyTargetMissing:
		return "target-missing"
	}
	return "unknown"
}

// Applied reports whether the target now holds the proposed value. True for
// ApplyWrote and ApplyAlreadyCorrect, false for the two failure outcomes.
func (o ApplyOutcome) Applied() bool {
	return o == ApplyWrote || o == ApplyAlreadyCorrect
}

// ErrValueBecameInvalid is returned when a value that was valid at proposal
// time is not valid now.
//
// This is a real case, not a theoretical one: between proposing and applying, a
// referenced studio can be deleted, a policy can be tightened, or a rating
// scale can change. The obligation is to REJECT rather than write, because
// writing an invalid value is how a target gets corrupted by a proposal nobody
// could have made today.
var ErrValueBecameInvalid = errors.New("value is no longer valid for this field")

// ErrAlreadyDecided is returned by MarkRejected when the proposal is no longer
// open. It is NOT a failure for the applier: the normal case is an ACCEPTED
// proposal that cannot be applied, and an accepted proposal must stay accepted.
//
// This distinction is the difference between an honest record and a rewritten
// one. The status column says what people decided. If the target was deleted
// after three people approved the edit, the truth is "approved, never
// applicable" -- not "rejected". Writing rejected would tell a future reader
// the community refused something they actually agreed to, and would make the
// proposal count wrong in every summary the M3 dashboard shows.
var ErrAlreadyDecided = errors.New("proposal has already been decided")

// TargetStore is the write surface the apply path needs.
//
// An interface, and a deliberately small one, because this is the one place in
// the system where a decision made by a VOTE turns into a WRITE on shared
// content. Everything above it is logic that can be tested exhaustively; this
// is the boundary, and it is small enough to read in one sitting.
type TargetStore interface {
	// ReadField returns the target's current value for a field. found is false
	// when the row does not exist, which is a different thing from the row
	// existing with a NULL value.
	ReadField(ctx context.Context, targetType string, targetID int, field string) (current *string, found bool, err error)

	// WriteFieldIfChanged sets the target's field to value ONLY IF it currently
	// holds `expected`, and reports whether it wrote.
	//
	// This exists instead of a plain WriteField because a compare-then-write
	// across two statements is not atomic, and the applier is called from a job
	// queue where two workers will pick up the same accepted proposal. With a
	// separate read and write, every worker that read before the first write
	// proceeds: eight workers produce eight writes and eight audit rows, and
	// every number a moderator ever reads is wrong.
	//
	// NULL and "" are kept apart by the implementation, because "clear this
	// field" and "set this field to nothing" are different edits.
	WriteFieldIfChanged(ctx context.Context, targetType string, targetID int, field string, expected, value *string) (wrote bool, err error)

	// MarkRejected records that a proposal will not be applied, with a reason.
	// Separate from the audit row because a proposal's own status is state that
	// the UI reads and someone will eventually ask about.
	//
	// deciderID is REQUIRED, not optional: edit_proposals.decided_by is a
	// foreign key, so recording a rejection without an actor fails at the SQL
	// layer and takes the audit row down with it. Found by
	// TestApply_RealDatabaseRejectsInvalidValueFromApply.
	MarkRejected(ctx context.Context, proposalID int, deciderID int, reason string) error

	// AppendAudit writes one audit row. Exactly one per apply that changes
	// something; a no-op apply writes none, or the audit trail becomes a
	// transcript of a reconciliation loop rather than a record of decisions.
	AppendAudit(ctx context.Context, entry AuditEntry) error
}

// AuditEntry is one row of the audit trail.
type AuditEntry struct {
	ActorID    *int
	Action     string
	TargetType string
	TargetID   *int
	Field      string
	Detail     map[string]interface{}
}

// Audit action names, spelled out so a typo is a compile error rather than a
// row nobody ever queries.
const (
	ActionProposalApplied  = "proposal_applied"
	ActionProposalRejected = "proposal_rejected"
	ActionProposalNoop     = "proposal_already_correct"
)

// Applier writes an accepted proposal's value onto its target.
//
// Every method takes the caller's context, and the caller is expected to be
// inside a transaction: the read, the compare, the write and the audit row must
// all commit or none of them may. Two workers racing the same accepted proposal
// is the case this is built for, and a compare-then-write outside a transaction
// is precisely how both of them win.
type Applier struct {
	targets TargetStore

	// DeciderID is recorded as edit_proposals.decided_by whenever Apply rejects
	// a proposal. Zero means "nobody" and the UPDATE fails on the foreign key,
	// losing the status change and the audit row together -- so callers doing
	// anything but a mechanical re-apply must set it.
	//
	// Zero is the right default for the reconciler in M3, which re-applies
	// already-accepted proposals and does not decide anything.
	DeciderID int
}

func NewApplier(targets TargetStore) *Applier { return &Applier{targets: targets} }

// Apply writes a proposal's value onto its target.
//
// The order is the whole design:
//
//  1. re-read the target INSIDE the caller's transaction
//  2. if it already holds the value -> no-op, no audit row, no error
//  3. re-validate: a value that became invalid is REJECTED, not written
//  4. write
//  5. append exactly one audit row
//
// Step 2 before step 3 is deliberate. A re-applied proposal whose value is no
// longer valid by today's rules is still correct -- the field already says what
// the voters agreed -- so reporting it as a rejection would record a decision
// nobody made and could put a live, correct value at risk of being clawed back.
func (a *Applier) Apply(ctx context.Context, p Proposal) (ApplyOutcome, error) {
	// The read is for REPORTING -- to distinguish a missing target, and to decide
	// whether this is already-correct. It is NOT what makes the write safe: a
	// value can change between this read and the write, and that is precisely the
	// window two workers exploit. The write itself re-checks atomically.
	current, found, err := a.targets.ReadField(ctx, p.TargetType, p.TargetID, p.Field)
	if err != nil {
		return ApplyTargetMissing, fmt.Errorf("reading %s %d field %q: %w", p.TargetType, p.TargetID, p.Field, err)
	}
	if !found {
		if err := a.rejectProposal(ctx, p, "target no longer exists"); err != nil {
			return ApplyTargetMissing, err
		}
		return ApplyTargetMissing, nil
	}

	// Step 2 before step 3, deliberately. A re-applied proposal whose value is
	// no longer valid by today's rules is still correct -- the field already says
	// what the voters agreed -- so reporting it as a rejection would record a
	// decision nobody made and could put a live, correct value at risk of being
	// clawed back.
	if equalFieldValues(current, p.NewValue) {
		return ApplyAlreadyCorrect, nil
	}

	// Step 3: re-validate at apply time. Cheap, and the alternative is writing a
	// value the current rules would refuse to accept.
	if err := ValidateValue(p.TargetType, p.Field, p.NewValue); err != nil {
		if markErr := a.markRejected(ctx, p, "value is no longer valid"); markErr != nil {
			return ApplyRejected, fmt.Errorf("value became invalid (%v) and the refusal could not be recorded: %w", err, markErr)
		}
		if auditErr := a.auditReject(ctx, p, err.Error()); auditErr != nil {
			return ApplyRejected, auditErr
		}
		return ApplyRejected, ErrValueBecameInvalid
	}

	// Step 4: compare-and-set. `expected` is the value we just read, so a worker
	// that read the same value races here and exactly one UPDATE matches. The
	// loser gets wrote=false and reports already-correct, having done nothing --
	// which is why eight concurrent applies produce one write and one audit row.
	wrote, err := a.targets.WriteFieldIfChanged(ctx, p.TargetType, p.TargetID, p.Field, current, p.NewValue)
	if err != nil {
		return ApplyTargetMissing, fmt.Errorf("writing %s %d field %q: %w", p.TargetType, p.TargetID, p.Field, err)
	}
	if !wrote {
		// Another worker applied it first. The value is correct, which is the only
		// thing this function promises; the audit row is theirs to write.
		return ApplyAlreadyCorrect, nil
	}

	// Step 5: exactly one audit row, by the worker whose UPDATE matched.
	if err := a.auditApply(ctx, p); err != nil {
		// The value IS written at this point. Reporting success here would leave
		// a change nobody can account for, so the error propagates and the
		// caller's transaction rolls the write back with it.
		return ApplyWrote, fmt.Errorf("value was written but the audit row could not be appended: %w", err)
	}
	return ApplyWrote, nil
}

// equalFieldValues compares two optional field values with NULL and "" kept
// apart.
func equalFieldValues(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (a *Applier) rejectProposal(ctx context.Context, p Proposal, reason string) error {
	if err := a.markRejected(ctx, p, reason); err != nil {
		return fmt.Errorf("recording refusal of proposal %d: %w", p.ID, err)
	}
	return a.auditReject(ctx, p, reason)
}

// markRejected records a refusal, tolerating a proposal that has already been
// decided.
//
// "Already decided" is swallowed deliberately. Apply runs against ACCEPTED
// proposals, and an accepted proposal whose target has since been deleted
// cannot be made un-accepted. Its status stays accepted -- that is what the
// voters decided -- and the audit row is what records the failure to apply. The
// only real error is one from the database itself.
func (a *Applier) markRejected(ctx context.Context, p Proposal, reason string) error {
	err := a.targets.MarkRejected(ctx, p.ID, a.DeciderID, reason)
	if errors.Is(err, ErrAlreadyDecided) {
		return nil
	}
	return err
}

func (a *Applier) auditApply(ctx context.Context, p Proposal) error {
	targetID := p.TargetID
	authorID := p.AuthorID
	return a.targets.AppendAudit(ctx, AuditEntry{
		// The AUTHOR, not the applier. The audit trail answers "who proposed
		// this and who accepted it", and attributing the write to whichever
		// worker ran the job would make every automated apply look like it came
		// from the system account.
		ActorID:    &authorID,
		Action:     ActionProposalApplied,
		TargetType: p.TargetType,
		TargetID:   &targetID,
		Field:      p.Field,
		Detail: map[string]interface{}{
			"proposal_id": p.ID,
			"old_value":   stringOrNil(p.OldValue),
			"new_value":   stringOrNil(p.NewValue),
		},
	})
}

func (a *Applier) auditReject(ctx context.Context, p Proposal, reason string) error {
	targetID := p.TargetID
	authorID := p.AuthorID
	return a.targets.AppendAudit(ctx, AuditEntry{
		ActorID:    &authorID,
		Action:     ActionProposalRejected,
		TargetType: p.TargetType,
		TargetID:   &targetID,
		Field:      p.Field,
		Detail: map[string]interface{}{
			"proposal_id": p.ID,
			"reason":      reason,
			"new_value":   stringOrNil(p.NewValue),
		},
	})
}

// stringOrNil renders an optional value for a JSON audit detail.
//
// nil becomes an explicit JSON null rather than the string "nil" or the empty
// string, because the difference between "this proposal cleared the field" and
// "this proposal set the field to nothing" is the difference between a record
// that is readable and one that is misleading.
func stringOrNil(v *string) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

// FormatValueForDB renders a validated value into the form the target column
// wants: an integer for the numeric types, otherwise the string itself.
//
// Kept next to the applier rather than in the sqlite layer because the mapping
// from FieldType to storage is part of the vocabulary's contract, and splitting
// it across two packages would let the two halves drift.
//
// BOTH numeric types convert. An earlier version converted only TypeInt, which
// meant a rating -- validated as a number by validateRating -- was written to a
// tinyint column as the string "4". SQLite would accept it, so nothing failed,
// and the damage only showed later as a rating column sorting as text. The test
// that caught it asserts the concrete Go type, not just the rendered value,
// because a string "4" and an int 4 compare equal in several of the ways that
// matter here.
func FormatValueForDB(targetType, field string, value *string) (interface{}, error) {
	info, ok := LookupField(targetType, field)
	if !ok {
		return nil, ErrFieldNotProposable
	}
	if value == nil {
		return nil, nil
	}

	switch info.Type {
	case TypeInt, TypeRating:
		n, err := strconv.Atoi(strings.TrimSpace(*value))
		if err != nil {
			return nil, ErrValueInvalid
		}
		return n, nil
	case TypeDate:
		// Stored as the ISO string, not as a time.Time. The column is a `date`
		// and the ecosystem round-trips it as text; handing SQLite a time.Time
		// would write a different string format than the migration path and
		// every reader of the column.
		return *value, nil
	default:
		return *value, nil
	}
}
