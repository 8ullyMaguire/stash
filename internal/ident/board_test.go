package ident

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The step's reason to exist, and the constraint §6a.13 calls "the most important
// constraint in this section": the difference between the board working and the
// board bypassing governance.
//
// The test asserts THREE things and the third is the one that matters: the audit
// log gained a proposal row AND the target field is unchanged until the ordinary
// governance path accepts it. A test that only checked the proposal exists would
// pass if the board wrote the field AND filed a proposal.
func TestASolvedIdentificationWritesAProposalNotAField(t *testing.T) {
	store := newFakeStore()
	board := NewBoard(collab.NewProposer(store))
	ctx := context.Background()

	// The target's field before anything happens.
	before := store.targetValue("performer", 42, "name")
	require.Equal(t, "", before, "precondition: the field starts empty")

	val := "Jane Doe"
	q := Query{
		ID:         "q1",
		TargetType: "performer",
		TargetID:   42,
		Field:      "name",
		Value:      &val,
		Evidence:   []Evidence{{Kind: "snapshot", Ref: "collage-1", Note: "face match"}},
	}

	prop, err := board.Solve(ctx, Solve{Query: q, ResolverID: 7})
	require.NoError(t, err)
	require.NotNil(t, prop)

	// 1. A proposal row exists, typed and attributed.
	assert.Equal(t, "name", prop.Field,
		"the proposal carries the vocabulary's field name, not a board-invented one")
	assert.Equal(t, 42, prop.TargetID)
	assert.Equal(t, 7, prop.AuthorID, "the resolver authors it, not the query's opener")
	require.Len(t, store.proposals, 1)
	assert.Equal(t, 1, len(store.audit), "the audit log gained exactly one row")
	assert.Contains(t, store.audit[0].Detail, "evidence",
		"evidence is attached so a reviewer can see why the proposal was filed")

	// 2. THE THIRD THING, AND THE POINT. The field is untouched.
	after := store.targetValue("performer", 42, "name")
	assert.Equal(t, before, after,
		"§6a.13: a solved identification becomes a PROPOSAL, not a write. The field "+
			"changes only when the ordinary governance path accepts it.")

	// 3. And it changes THEN, through the ordinary path — otherwise "unchanged"
	// would be satisfied by a board that simply never writes anything.
	require.NoError(t, store.applyProposal(prop.ID, 99), "the governance path accepts it")
	assert.Equal(t, val, store.targetValue("performer", 42, "name"),
		"once governance accepts, the ordinary path writes it")
	assert.Len(t, store.audit, 2, "and the application is audited too")
}

// A solve with no author cannot be reviewed, so it is refused. A reviewer who
// cannot see who filed a proposal is reviewing nothing.
func TestASolveWithoutAResolverIsRefused(t *testing.T) {
	store := newFakeStore()
	board := NewBoard(collab.NewProposer(store))
	val := "Jane Doe"

	_, err := board.Solve(context.Background(), Solve{
		Query: Query{ID: "q1", TargetType: "performer", TargetID: 1, Field: "name", Value: &val},
		// ResolverID deliberately zero.
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoResolver)

	assert.Empty(t, store.proposals, "no proposal is filed for an unattributable solve")
	assert.Empty(t, store.audit)
}

// A proposal asserting nothing is noise in the governance queue, and a queue full
// of noise is a queue nobody reads.
func TestASolveAssertingNothingIsRefused(t *testing.T) {
	store := newFakeStore()
	board := NewBoard(collab.NewProposer(store))

	_, err := board.Solve(context.Background(), Solve{
		Query:      Query{ID: "q1", TargetType: "performer", TargetID: 1, Field: "name"},
		ResolverID: 7,
		// No Value and no Evidence.
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoValue)
	assert.Empty(t, store.proposals)
}

// Collab's vocabulary check is the LAST word, not a duplicated rule here. A
// non-proposable field is refused by the governance path itself.
func TestANonProposableFieldIsRefusedByTheGovernancePath(t *testing.T) {
	store := newFakeStore()
	board := NewBoard(collab.NewProposer(store))
	val := "anything"

	_, err := board.Solve(context.Background(), Solve{
		Query:      Query{ID: "q1", TargetType: "performer", TargetID: 1, Field: "not_a_real_field", Value: &val},
		ResolverID: 7,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotProposable)
	assert.Empty(t, store.proposals, "nothing is filed for a field that cannot be proposed")
}

// Governance errors are passed through, not re-wrapped, so a caller can
// errors.Is against the precise reason. A board that string-matched would force
// every caller to do the same.
func TestGovernanceErrorsKeepTheirIdentity(t *testing.T) {
	store := newFakeStore()
	board := NewBoard(collab.NewProposer(store))
	val := "Jane Doe"

	q := Query{ID: "q1", TargetType: "performer", TargetID: 1, Field: "name", Value: &val}
	prop, err := board.Solve(context.Background(), Solve{Query: q, ResolverID: 7})
	require.NoError(t, err)
	require.NoError(t, store.rejectProposal(prop.ID, 99))

	// Retrying immediately: the sticky rejection must surface with its OWN
	// sentinel, reaching the board caller unchanged rather than flattened into a
	// generic ident error.
	//
	// I had an unblockAuthor(7) call here, which is what made this test pass
	// vacuously for a long time -- it removed the very state under test before the
	// retry. The block is lifted LATER, in its own test below.
	_, err = board.Solve(context.Background(), Solve{Query: q, ResolverID: 7})
	require.Error(t, err)
	assert.ErrorIs(t, err, collab.ErrStickyRejected,
		"the sticky rejection reaches the board caller unchanged")

	// Lifting the block makes the same retry succeed, which proves the refusal was
	// the sticky rule and not a permanent bar on the author.
	store.unblockAuthor(7)
	_, err = board.Solve(context.Background(), Solve{Query: q, ResolverID: 7})
	assert.NoError(t, err,
		"§5.3: superseding is not a way around a refusal, but an UNBLOCK is -- and "+
			"the same author can file again once it is lifted")
}

// Evidence is carried as RATIONALE — the thing a reviewer actually reads. A
// side table the review screen does not read is how evidence stops being
// available at the moment it is needed.
func TestEvidenceTravelsAsRationale(t *testing.T) {
	store := newFakeStore()
	board := NewBoard(collab.NewProposer(store))
	val := "Jane Doe"

	prop, err := board.Solve(context.Background(), Solve{
		Query: Query{
			ID: "q1", TargetType: "performer", TargetID: 1, Field: "name", Value: &val,
			Evidence: []Evidence{
				{Kind: "snapshot", Ref: "collage-1", Note: "face match"},
				{Kind: "description", Ref: "desc-9"},
			},
		},
		ResolverID: 7,
	})
	require.NoError(t, err)

	r := prop.Rationale
	assert.Contains(t, r, "collage-1")
	assert.Contains(t, r, "face match")
	assert.Contains(t, r, "desc-9")
	assert.Contains(t, r, "identified via board",
		"and the origin is visible, so a reviewer knows this came from the board "+
			"rather than a user's own edit")
}

// A cleared field is a legitimate proposal, distinct from asserting "". If the
// board could not express that, "clear this field" would be unrepresentable.
func TestAClearedFieldIsAProposalNotAnEmptyString(t *testing.T) {
	val2 := "Someone New"
	store := newFakeStore()
	board := NewBoard(collab.NewProposer(store))
	require.NoError(t, store.setTargetValue("performer", 42, "name", "Someone Else"))

	// Value == nil means "clear it".
	prop, err := board.Solve(context.Background(), Solve{
		Query: Query{
			ID: "q1", TargetType: "performer", TargetID: 42, Field: "name",
			Value:        nil, // a clear, not an empty string
			CurrentValue: strPtr("Someone Else"),
			Evidence:     []Evidence{{Kind: "context", Ref: "the board is wrong"}},
		},
		ResolverID: 7,
	})
	require.NoError(t, err)
	require.Nil(t, prop.NewValue, "a clear is nil, not a pointer to an empty string")

	// The old value is what the reviewer compares against, so it must be read.
	require.NotNil(t, prop.OldValue,
		"§5.3: the proposal carries old and new, and a reviewer needs the old")
	assert.Equal(t, "Someone Else", *prop.OldValue)

	// A board that filed old=nil always would make every proposal look like "set
	// from nothing", hiding the actual disagreement -- the case the reviewer is
	// there to judge.
	unknown, err := board.Solve(context.Background(), Solve{
		Query: Query{
			ID: "q2", TargetType: "performer", TargetID: 43, Field: "name",
			Value:    &val2,
			Evidence: []Evidence{{Kind: "context", Ref: "no idea what is there"}},
		},
		ResolverID: 7,
	})
	require.NoError(t, err)
	assert.Nil(t, unknown.OldValue,
		"when the board does not know the current value it says so rather than "+
			"guessing an empty string -- a guessed diff is one nobody can trust")

	// And still nothing is written until governance accepts.
	assert.Equal(t, "Someone Else", store.targetValue("performer", 42, "name"),
		"filing a clear proposal does not clear the field")
}

// The structural half of §6a.13, asserted rather than promised: this package has
// no way to write a field. Board holds exactly one dependency, and it is the
// proposer.
//
// A future step wanting to "just apply it directly" would have to change
// NewBoard's signature, and that is a reviewable diff rather than a one-line
// addition inside this package.
func TestTheBoardHasNoFieldWriter(t *testing.T) {
	b := NewBoard(collab.NewProposer(newFakeStore()))

	// The only exported method is Solve.
	typ := reflect.TypeOf(b)
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		assert.True(t, name == "Solve",
			"Board exposes %q; anything that can write a field is a bypass of "+
				"§6a.13 and belongs in the governance path, not here", name)
	}
}

// fakeStore is a ProposalStore plus a target table, so the test can observe both
// halves: the proposal that was filed AND the field that was not written.
//
// It deliberately implements collab.ProposalStore only. It cannot apply a
// proposal to a real target except through applyProposal, which stands in for the
// ordinary governance path — so there is no way for the board to reach a target
// except through a proposal.
type fakeStore struct {
	proposals []*decidedProposal
	audit     []collab.AuditEntry
	values    map[string]string
	blocked   map[int]bool
	nextID    int
}

// rejected marks a decided proposal, so FindOpen ignores it -- which is what the
// real store does, and what makes ErrStickyRejected reachable at all.
type decidedProposal struct {
	collab.Proposal
	rejected bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{values: map[string]string{}, blocked: map[int]bool{}, nextID: 1}
}

func key(targetType string, targetID int, field string) string {
	return fmt.Sprintf("%s/%d/%s", targetType, targetID, field)
}

func (f *fakeStore) targetValue(targetType string, targetID int, field string) string {
	return f.values[key(targetType, targetID, field)]
}

func (f *fakeStore) setTargetValue(targetType string, targetID int, field, v string) error {
	f.values[key(targetType, targetID, field)] = v
	return nil
}

func (f *fakeStore) Create(_ context.Context, p *collab.Proposal) (*collab.Proposal, error) {
	if f.blocked[p.AuthorID] {
		return nil, collab.ErrStickyRejected
	}
	p.ID = f.nextID
	f.nextID++
	dp := &decidedProposal{Proposal: *p}
	f.proposals = append(f.proposals, dp)
	f.audit = append(f.audit, collab.AuditEntry{
		ActorID:    &p.AuthorID,
		Action:     "proposal_created",
		TargetType: p.TargetType,
		Field:      p.Field,
		Detail:     map[string]interface{}{"evidence": p.Rationale, "proposal_id": p.ID},
	})
	return &dp.Proposal, nil
}

func (f *fakeStore) FindOpen(_ context.Context, targetType string, targetID int, field string) (*collab.Proposal, bool, error) {
	for i, p := range f.proposals {
		if f.proposals[i].rejected {
			continue
		}
		if p.TargetType == targetType && p.TargetID == targetID && p.Field == field {
			return &p.Proposal, true, nil
		}
	}
	return nil, false, nil
}

// RejectionsFor returns the REAL sticky-rejection state for this author.
//
// The first version returned an empty map from a `blocked` map I had invented,
// which meant the sticky-rejection rule was never exercised at all and the retry
// simply succeeded. The rejection state lives in exactly one place -- this
// method's result -- so a fake that does not model it is not modelling anything.
func (f *fakeStore) RejectionsFor(_ context.Context, authorID int) (map[collab.StickyRejectionKey]bool, error) {
	out := map[collab.StickyRejectionKey]bool{}
	if !f.blocked[authorID] {
		return out, nil
	}
	for _, p := range f.proposals {
		if p.rejected && p.AuthorID == authorID {
			// The CONSTRUCTOR, not a hand-built literal. My first version spelled
			// the struct out by hand and IsBlocked did not match it -- which means
			// the key has fields I did not know about, and guessing at them is how
			// a fake ends up modelling a rule it has not implemented.
			out[collab.NewStickyRejectionKey(p.TargetType, p.TargetID, p.Field, p.AuthorID)] = true
		}
	}
	return out, nil
}

func (f *fakeStore) Supersede(_ context.Context, _ string, _ int, _ string) (int, bool, error) {
	return 0, false, nil
}

// applyProposal stands in for the ordinary governance path accepting a proposal.
// Only this writes a target value.
func (f *fakeStore) applyProposal(id, decider int) error {
	for _, p := range f.proposals {
		if p.ID != id {
			continue
		}
		if p.NewValue == nil {
			delete(f.values, key(p.TargetType, p.TargetID, p.Field))
		} else {
			f.values[key(p.TargetType, p.TargetID, p.Field)] = *p.NewValue
		}
		f.audit = append(f.audit, collab.AuditEntry{
			ActorID: &decider,
			Action:  collab.ActionProposalApplied,
			Detail:  map[string]interface{}{"proposal_id": id},
		})
		return nil
	}
	return errors.New("no such proposal")
}

// rejectProposal marks the proposal rejected AND supersedes it.
//
// Superseding is not optional bookkeeping: collab.Proposer checks for an already
// open proposal on the (target, field) before it reaches the sticky-rejection
// check, so a rejected-but-still-open proposal yields ErrOpenProposalExists
// instead. My first version omitted it and the test failed on the wrong sentinel
// -- which is the real governance ordering being stricter than my fake assumed.
func (f *fakeStore) rejectProposal(id, decider int) error {
	for _, p := range f.proposals {
		if p.ID == id {
			p.rejected = true
		}
	}
	f.blocked[7] = true
	f.audit = append(f.audit, collab.AuditEntry{
		ActorID: &decider,
		Action:  collab.ActionProposalRejected,
	})
	return nil
}

func (f *fakeStore) unblockAuthor(id int) { delete(f.blocked, id) }

func strPtr(s string) *string { return &s }
