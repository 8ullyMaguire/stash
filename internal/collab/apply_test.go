package collab_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
)

// fakeTargets is an in-memory TargetStore that records what happened, including
// how many audit rows were written.
type fakeTargets struct {
	mu sync.Mutex

	rows    map[string]*string // "type|id|field"
	present map[string]bool

	rejected map[int]string
	audit    []collab.AuditEntry

	// Injection points, nil in the happy path.
	readErr       error
	writeErr      error
	auditErr      error
	markErr       error
	writeCount    int
	auditCount    int
	lastDeciderID int
	markRejects   int
}

func newFakeTargets() *fakeTargets {
	return &fakeTargets{
		rows:     map[string]*string{},
		present:  map[string]bool{},
		rejected: map[int]string{},
	}
}

func fk(t string, id int, f string) string {
	return t + "|" + strconvItoa(id) + "|" + f
}

func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func (f *fakeTargets) add(t string, id int, field string, v *string) {
	f.rows[fk(t, id, field)] = v
	f.present[fk(t, id, field)] = true
}

func (f *fakeTargets) ReadField(_ context.Context, t string, id int, field string) (*string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, false, f.readErr
	}
	if !f.present[fk(t, id, field)] {
		return nil, false, nil
	}
	v := f.rows[fk(t, id, field)]
	if v == nil {
		return nil, true, nil
	}
	cp := *v
	return &cp, true, nil
}

// WriteFieldIfChanged is the compare-and-set. The WHOLE read-compare-write is
// under one mutex, which is what a real implementation gets from a single
// UPDATE ... WHERE col IS ? -- and what the earlier two-call version did not
// have, which is why eight workers produced two writes.
func (f *fakeTargets) WriteFieldIfChanged(_ context.Context, t string, id int, field string, expected, value *string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return false, f.writeErr
	}

	if !f.present[fk(t, id, field)] {
		return false, nil
	}
	if !equalPtr(f.rows[fk(t, id, field)], expected) {
		return false, nil
	}
	if equalPtr(f.rows[fk(t, id, field)], value) {
		// Already correct: report no write rather than counting a pointless one.
		return false, nil
	}

	f.writeCount++
	if value == nil {
		f.rows[fk(t, id, field)] = nil
	} else {
		cp := *value
		f.rows[fk(t, id, field)] = &cp
	}
	return true, nil
}

// equalPtr keeps NULL and "" apart, exactly as the vocabulary requires.
func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// deciderID is recorded so a test can assert WHO rejected, not just that a
// rejection happened. It was added to the interface because
// edit_proposals.decided_by is a foreign key, so a rejection without an actor
// fails at the SQL layer — and the fake drifted from the interface until this
// signature changed, which is how the unit suite stopped compiling.
func (f *fakeTargets) MarkRejected(_ context.Context, id int, deciderID int, reason string) error {
	f.lastDeciderID = deciderID
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markErr != nil {
		return f.markErr
	}
	f.markRejects++
	f.rejected[id] = reason
	return nil
}

func (f *fakeTargets) AppendAudit(_ context.Context, e collab.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auditErr != nil {
		return f.auditErr
	}
	f.auditCount++
	f.audit = append(f.audit, e)
	return nil
}

// ---------------------------------------------------------------------------

// A second apply of the same proposal must change nothing and write no audit
// row. This matters because M3's reconciliation re-applies everything on a
// schedule, and a re-apply that appended a row would make the audit trail a
// transcript of a cron job.
func TestApply_IsIdempotent(t *testing.T) {
	targets := newFakeTargets()
	targets.add("scene", 1, "title", ptr("Old Title"))
	a := collab.NewApplier(targets)

	p := collab.Proposal{
		ID: 10, TargetType: "scene", TargetID: 1, Field: "title",
		OldValue: ptr("Old Title"), NewValue: ptr("New Title"), AuthorID: 7,
	}

	out, err := a.Apply(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, collab.ApplyWrote, out)
	assert.Equal(t, 1, targets.writeCount)
	assert.Equal(t, 1, targets.auditCount)
	assert.Equal(t, "New Title", *targets.rows[fk("scene", 1, "title")])

	// Re-apply. Everything about it must be free.
	out2, err := a.Apply(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, collab.ApplyAlreadyCorrect, out2)
	assert.Equal(t, 1, targets.writeCount, "a re-apply must not write again")
	assert.Equal(t, 1, targets.auditCount, "a re-apply must not append an audit row; "+
		"M3's reconciler re-applies everything and the trail must not become a transcript of it")
	assert.Equal(t, "New Title", *targets.rows[fk("scene", 1, "title")])

	// And a third, for good measure.
	_, err = a.Apply(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, 1, targets.writeCount)
	assert.Equal(t, 1, targets.auditCount)
}

// The distinction that makes the idempotence check correct: a target that
// already has the value, and a target whose field is UNSET. A proposal to clear
// a field is a real edit, and it must still be applied the first time.
func TestApply_AlreadyCorrectDistinguishesUnsetFromEmpty(t *testing.T) {
	targets := newFakeTargets()
	// The field exists and holds the empty string.
	targets.add("scene", 2, "details", ptr(""))
	a := collab.NewApplier(targets)

	// A proposal to set it to "x" is a real change even though "" == "x" is
	// false but "unset" would have been wrongly treated as equal.
	out, err := a.Apply(context.Background(), collab.Proposal{
		ID: 1, TargetType: "scene", TargetID: 2, Field: "details",
		NewValue: ptr("x"), AuthorID: 7,
	})
	require.NoError(t, err)
	assert.Equal(t, collab.ApplyWrote, out, "an empty string is NOT the same as unset, so this is a real write")

	// Now clear it. nil vs "" is the comparison that matters.
	out, err = a.Apply(context.Background(), collab.Proposal{
		ID: 2, TargetType: "scene", TargetID: 2, Field: "details",
		OldValue: ptr("x"), NewValue: nil, AuthorID: 7,
	})
	require.NoError(t, err)
	assert.Equal(t, collab.ApplyWrote, out, "clearing a field is a real edit")
	assert.Nil(t, targets.rows[fk("scene", 2, "details")])

	// Re-applying the clear must be a no-op, and NOT be confused with the
	// already-empty case.
	out, err = a.Apply(context.Background(), collab.Proposal{
		ID: 2, TargetType: "scene", TargetID: 2, Field: "details",
		NewValue: nil, AuthorID: 7,
	})
	require.NoError(t, err)
	assert.Equal(t, collab.ApplyAlreadyCorrect, out,
		"an already-unset field IS already correct for a proposal that clears it")
	assert.Equal(t, 2, targets.writeCount, "exactly two writes: set to x, then cleared")
}

// A value that was valid when proposed but is not now must REJECT the proposal,
// not write it.
func TestApply_RejectsValueThatBecameInvalid(t *testing.T) {
	targets := newFakeTargets()
	targets.add("image", 3, "rating", ptr("3"))
	a := collab.NewApplier(targets)

	// rating is bounded 1-5. A 9 could not have been proposed, but a policy
	// change or a corrupted row could put one here, and the apply path is the
	// last gate before the column.
	p := collab.Proposal{
		ID: 20, TargetType: "image", TargetID: 3, Field: "rating",
		NewValue: ptr("9"), AuthorID: 7,
	}

	outcome, err := a.Apply(context.Background(), p)
	assert.ErrorIs(t, err, collab.ErrValueBecameInvalid)
	assert.Equal(t, collab.ApplyRejected, outcome)
	assert.Zero(t, targets.writeCount, "an invalid value must not reach the column")
	assert.Equal(t, "3", *targets.rows[fk("image", 3, "rating")], "the target is untouched")
	assert.Equal(t, 1, targets.markRejects, "the proposal's own status must record the rejection")
	assert.Contains(t, targets.rejected[20], "no longer valid")
	assert.Equal(t, 1, targets.auditCount, "a rejection is a decision and is audited like one")
	assert.Equal(t, collab.ActionProposalRejected, targets.audit[0].Action)
}

// A deleted target is a different failure from an invalid value, and must not
// be recorded as "your value was bad" -- the author did nothing wrong.
func TestApply_MissingTargetIsNotAnInvalidValue(t *testing.T) {
	targets := newFakeTargets()
	a := collab.NewApplier(targets)

	out, err := a.Apply(context.Background(), collab.Proposal{
		ID: 30, TargetType: "scene", TargetID: 999, Field: "title",
		NewValue: ptr("gone"), AuthorID: 7,
	})
	require.NoError(t, err, "a missing target is a normal outcome, not an error")
	assert.Equal(t, collab.ApplyTargetMissing, out)
	assert.Zero(t, targets.writeCount)
	assert.Equal(t, 1, targets.markRejects)
	assert.Contains(t, targets.rejected[30], "no longer exists",
		"the reason must distinguish a deleted target from a rejected value")
	assert.Equal(t, collab.ActionProposalRejected, targets.audit[0].Action)
}

// Exactly one audit row per apply. Not zero (no accountability), not two (a
// reconciler that ran twice would double every number a moderator ever reads).
func TestApply_WritesExactlyOneAuditRow(t *testing.T) {
	targets := newFakeTargets()
	targets.add("scene", 4, "title", ptr("a"))
	a := collab.NewApplier(targets)

	_, err := a.Apply(context.Background(), collab.Proposal{
		ID: 40, TargetType: "scene", TargetID: 4, Field: "title",
		OldValue: ptr("a"), NewValue: ptr("b"), AuthorID: 7,
	})
	require.NoError(t, err)

	require.Len(t, targets.audit, 1)
	row := targets.audit[0]
	assert.Equal(t, collab.ActionProposalApplied, row.Action)
	assert.Equal(t, "scene", row.TargetType)
	assert.Equal(t, "title", row.Field)
	require.NotNil(t, row.ActorID)
	assert.Equal(t, 7, *row.ActorID, "the audit attributes the change to the AUTHOR, not to "+
		"the worker that happened to run the apply -- otherwise every automated apply "+
		"looks like it came from the system account")
	assert.Equal(t, 40, row.Detail["proposal_id"])
	assert.Equal(t, "a", row.Detail["old_value"])
	assert.Equal(t, "b", row.Detail["new_value"])
}

// The audit detail must distinguish a cleared field from an empty one, or the
// record misleads whoever reads it years later.
func TestApply_AuditDetailDistinguishesClearedFromEmpty(t *testing.T) {
	targets := newFakeTargets()
	targets.add("scene", 5, "details", ptr("text"))
	a := collab.NewApplier(targets)

	_, err := a.Apply(context.Background(), collab.Proposal{
		ID: 50, TargetType: "scene", TargetID: 5, Field: "details",
		OldValue: ptr("text"), NewValue: nil, AuthorID: 7,
	})
	require.NoError(t, err)

	require.Len(t, targets.audit, 1)
	v, present := targets.audit[0].Detail["new_value"]
	assert.True(t, present, "the key must be PRESENT even when the value is nil, so a reader "+
		"can tell 'cleared the field' from 'the field was never mentioned'")
	assert.Nil(t, v, "a cleared field must serialise as JSON null, not as the string \"nil\" or \"\"")
}

// A write that succeeds but whose audit row cannot be appended must NOT report
// success. The caller's transaction rolls the write back with it; reporting
// success would leave a change nobody can account for.
func TestApply_AuditFailureRollsBackRatherThanReportingSuccess(t *testing.T) {
	targets := newFakeTargets()
	targets.add("scene", 6, "title", ptr("a"))
	targets.auditErr = assert.AnError
	a := collab.NewApplier(targets)

	// The write DID happen -- the fake has no transaction to roll it back, which
	// is precisely why the applier has to say so rather than returning success.
	_, err := a.Apply(context.Background(), collab.Proposal{
		ID: 60, TargetType: "scene", TargetID: 6, Field: "title",
		NewValue: ptr("b"), AuthorID: 7,
	})
	require.Error(t, err)
	assert.Equal(t, 1, targets.writeCount,
		"the write really did land; in production the caller's transaction rolls it back, "+
			"but the applier must not pretend the apply succeeded")
	assert.Contains(t, err.Error(), "audit row could not be appended",
		"the error must say the write HAPPENED and the audit did not, because that is the "+
			"state the caller has to roll back")
	assert.Zero(t, targets.auditCount)
}

// Two workers applying the same accepted proposal must not double-mutate or
// write two audit rows. In production this is guaranteed by the transaction the
// caller holds, so the test drives the same interleaving the transaction
// prevents and asserts the applier's own compare step is what makes it free.
func TestApply_ConcurrentApplyWritesOnce(t *testing.T) {
	targets := newFakeTargets()
	targets.add("scene", 7, "title", ptr("a"))
	a := collab.NewApplier(targets)

	p := collab.Proposal{
		ID: 70, TargetType: "scene", TargetID: 7, Field: "title",
		OldValue: ptr("a"), NewValue: ptr("b"), AuthorID: 7,
	}

	const workers = 8
	var wg sync.WaitGroup
	outs := make([]collab.ApplyOutcome, workers)
	errs := make([]error, workers)

	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			outs[i], errs[i] = a.Apply(context.Background(), p)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "worker %d", i)
	}

	wrote := 0
	for _, o := range outs {
		if o == collab.ApplyWrote {
			wrote++
		}
		assert.True(t, o.Applied(), "every worker ends with the value applied")
	}

	// The fake store serialises on its own mutex, so the first writer wins and
	// the rest see the new value. That is exactly the ordering the real
	// transaction enforces, and it is why the compare step exists.
	assert.Equal(t, 1, wrote, "exactly ONE worker may report having written it")
	assert.Equal(t, 1, targets.writeCount, "one write, not eight")
	assert.Equal(t, 1, targets.auditCount, "one audit row, not eight -- a doubled audit trail "+
		"makes every number a moderator reads wrong")
	assert.Equal(t, "b", *targets.rows[fk("scene", 7, "title")])
}

// A re-applied proposal whose value is no longer valid is STILL correct: the
// field already says what the voters agreed. Reporting that as a rejection would
// record a decision nobody made and could put a live, correct value at risk.
func TestApply_AlreadyCorrectWinsOverRevalidation(t *testing.T) {
	targets := newFakeTargets()
	// The target already holds the value, and that value would not pass today's
	// validation -- as if the rating scale had been tightened after acceptance.
	targets.add("image", 8, "rating", ptr("9"))
	a := collab.NewApplier(targets)

	out, err := a.Apply(context.Background(), collab.Proposal{
		ID: 80, TargetType: "image", TargetID: 8, Field: "rating",
		NewValue: ptr("9"), AuthorID: 7,
	})
	require.NoError(t, err)
	assert.Equal(t, collab.ApplyAlreadyCorrect, out,
		"a value the target already holds is correct regardless of what today's rules say")
	assert.Zero(t, targets.writeCount)
	assert.Zero(t, targets.markRejects, "nobody's proposal should be rejected for being right")
	assert.Zero(t, targets.auditCount)
}

// FormatValueForDB maps a validated value into the column's storage form. The
// rating case is the one that matters: a rating that validates as the string
// "4" must be written as the INTEGER 4, or a numeric column stores text and
// every later comparison sorts wrongly.
func TestFormatValueForDB_MapsTypesToStorage(t *testing.T) {
	v, err := collab.FormatValueForDB("image", "rating", ptr("4"))
	require.NoError(t, err)
	assert.Equal(t, 4, v, "a rating must be written as an integer, not as the string \"4\"")

	v, err = collab.FormatValueForDB("scene", "title", ptr("Some Title"))
	require.NoError(t, err)
	assert.Equal(t, "Some Title", v)

	v, err = collab.FormatValueForDB("scene", "title", nil)
	require.NoError(t, err)
	assert.Nil(t, v, "clearing a field writes SQL NULL, not the empty string")

	_, err = collab.FormatValueForDB("scene", "studio_id", ptr("not-a-number"))
	assert.ErrorIs(t, err, collab.ErrValueInvalid)

	_, err = collab.FormatValueForDB("scene", "nope", ptr("x"))
	assert.ErrorIs(t, err, collab.ErrFieldNotProposable)
}

func TestApplyOutcome_Applied(t *testing.T) {
	assert.True(t, collab.ApplyWrote.Applied())
	assert.True(t, collab.ApplyAlreadyCorrect.Applied())
	assert.False(t, collab.ApplyRejected.Applied())
	assert.False(t, collab.ApplyTargetMissing.Applied())

	assert.Equal(t, "wrote", collab.ApplyWrote.String())
	assert.Equal(t, "already-correct", collab.ApplyAlreadyCorrect.String())
}
