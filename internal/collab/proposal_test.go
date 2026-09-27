package collab_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
)

// fakeProposalStore is an in-memory ProposalStore.
//
// It exists so the proposer's rules can be tested without a database, for the
// same reason governance.go has no I/O: these rules are the security boundary,
// and a test that needs a 200MB fixture to check "you cannot supersede your way
// around a rejection" is a test that will not be written.
type fakeProposalStore struct {
	props   map[string]*collab.Proposal // key: target|field
	nextID  int
	reject  map[collab.StickyRejectionKey]bool
	supers  int // how many times Supersede actually did something
	supFail error
}

func newFake() *fakeProposalStore {
	return &fakeProposalStore{
		props:  map[string]*collab.Proposal{},
		reject: map[collab.StickyRejectionKey]bool{},
	}
}

func k(t string, id int, f string) string {
	return t + "|" + string(rune('0'+id)) + "|" + f
}

func (f *fakeProposalStore) Create(_ context.Context, p *collab.Proposal) (*collab.Proposal, error) {
	f.nextID++
	cp := *p
	cp.ID = f.nextID
	f.props[k(p.TargetType, p.TargetID, p.Field)] = &cp
	return &cp, nil
}

func (f *fakeProposalStore) FindOpen(_ context.Context, t string, id int, field string) (*collab.Proposal, bool, error) {
	p, ok := f.props[k(t, id, field)]
	return p, ok, nil
}

func (f *fakeProposalStore) RejectionsFor(_ context.Context, _ int) (map[collab.StickyRejectionKey]bool, error) {
	return f.reject, nil
}

func (f *fakeProposalStore) Supersede(_ context.Context, t string, id int, field string) (int, bool, error) {
	if f.supFail != nil {
		return 0, false, f.supFail
	}
	key := k(t, id, field)
	if _, ok := f.props[key]; !ok {
		return 0, false, nil
	}
	delete(f.props, key)
	f.supers++
	return 0, true, nil
}

// A field outside the vocabulary never reaches the store. The assertion is on
// the store being untouched, not just on the error: a validation that rejects
// after having already written something is not a validation.
func TestProposal_RejectsFieldOutsideVocabulary(t *testing.T) {
	store := newFake()
	p := collab.NewProposer(store)

	_, err := p.Create(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 1, Field: "nope",
		NewValue: ptr("x"), AuthorID: 7,
	})
	assert.ErrorIs(t, err, collab.ErrFieldNotProposable)
	assert.Empty(t, store.props, "a rejected proposal must leave no trace in the store")
	assert.Zero(t, store.nextID, "not even an id may be allocated for a refused proposal")
}

func TestProposal_RejectsUnparseableValueAtProposalTime(t *testing.T) {
	store := newFake()
	p := collab.NewProposer(store)

	_, err := p.Create(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 1, Field: "studio_id",
		NewValue: ptr("not-a-number"), AuthorID: 7,
	})
	assert.ErrorIs(t, err, collab.ErrValueInvalid)
	assert.Empty(t, store.props, "an unparseable value must not reach the vote queue")
}

func TestProposal_StickyRejectionBlocksRetry(t *testing.T) {
	store := newFake()
	// The author already lost a proposal on this exact field.
	store.reject[collab.NewStickyRejectionKey("scene", 1, "title", 7)] = true
	p := collab.NewProposer(store)

	_, err := p.Create(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 1, Field: "title",
		NewValue: ptr("try again"), AuthorID: 7,
	})
	assert.ErrorIs(t, err, collab.ErrStickyRejected,
		"a rejected (target, field, author) must not get an infinite retry queue")

	// A DIFFERENT author is unaffected: one author losing a vote says nothing
	// about anyone else's proposal on the same field.
	_, err = p.Create(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 1, Field: "title",
		NewValue: ptr("mine is better"), AuthorID: 8,
	})
	assert.NoError(t, err, "a rejection binds the author who lost it, not the field")
}

// The one that matters most here: supersession must NOT be a way around a
// rejection. If it were, the sticky rule would be theatre -- you would simply
// propose again harder.
func TestProposal_StickyRejectionCannotBeSupersededAround(t *testing.T) {
	store := newFake()
	store.reject[collab.NewStickyRejectionKey("scene", 1, "title", 7)] = true
	p := collab.NewProposer(store)

	_, err := p.Supersede(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 1, Field: "title",
		NewValue: ptr("replacement"), AuthorID: 7,
	})
	assert.ErrorIs(t, err, collab.ErrStickyRejected,
		"superseding is not a way around a refusal; the replacement is validated exactly as a fresh Create")
	assert.Zero(t, store.supers, "the existing proposal must not even be touched")
}

func TestProposal_SupersededByNewerOnSameField(t *testing.T) {
	store := newFake()
	p := collab.NewProposer(store)

	first, err := p.Create(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 2, Field: "details",
		NewValue: ptr("first wording"), AuthorID: 7,
	})
	require.NoError(t, err)
	require.Zero(t, store.supers, "a plain Create supersedes nothing")

	second, err := p.Supersede(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 2, Field: "details",
		NewValue: ptr("better wording"), AuthorID: 7,
	})
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, second.ID, "the replacement is a NEW proposal, not a version bump")
	assert.Equal(t, 1, store.supers)
	assert.Equal(t, "better wording", *store.props[k("scene", 2, "details")].NewValue)
}

func TestProposal_SupersedeWithNothingOpenIsRefused(t *testing.T) {
	store := newFake()
	p := collab.NewProposer(store)

	_, err := p.Supersede(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 3, Field: "title",
		NewValue: ptr("x"), AuthorID: 7,
	})
	assert.ErrorIs(t, err, collab.ErrOpenProposalExists,
		"silently creating a replacement for a proposal that does not exist would "+
			"make the caller believe a decision was made about a request that was never on record")
	assert.Empty(t, store.props)
}

func TestProposal_CreateWithOneAlreadyOpenIsRefused(t *testing.T) {
	store := newFake()
	p := collab.NewProposer(store)

	_, err := p.Create(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 4, Field: "title",
		NewValue: ptr("mine"), AuthorID: 7,
	})
	require.NoError(t, err)

	// A second user proposing the same field. The database's partial unique
	// index also refuses this; the service refuses it first so the author gets a
	// sentence rather than a constraint error.
	_, err = p.Create(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 4, Field: "title",
		NewValue: ptr("theirs"), AuthorID: 8,
	})
	assert.ErrorIs(t, err, collab.ErrOpenProposalExists)
	assert.Equal(t, "mine", *store.props[k("scene", 4, "title")].NewValue,
		"the first proposal must be untouched")
}

// A store failure must surface as an error, not as a silently-created proposal.
func TestProposal_StoreFailureIsNotSwallowed(t *testing.T) {
	store := newFake()
	store.supFail = errors.New("database is locked")
	p := collab.NewProposer(store)

	_, err := p.Create(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 5, Field: "title",
		NewValue: ptr("x"), AuthorID: 7,
	})
	require.NoError(t, err)

	_, err = p.Supersede(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 5, Field: "title",
		NewValue: ptr("y"), AuthorID: 7,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database is locked",
		"the underlying cause must survive; a wrapped-and-bare error would send the author to the wrong problem")
}

// Validation must run BEFORE the sticky check. A field the author could never
// have proposed on must not report "you were rejected" -- that would confirm the
// existence of a rejection they have no business knowing about.
func TestProposal_VocabularyIsCheckedBeforeStickyRejection(t *testing.T) {
	store := newFake()
	store.reject[collab.NewStickyRejectionKey("scene", 6, "nope", 7)] = true
	p := collab.NewProposer(store)

	_, err := p.Create(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 6, Field: "nope",
		NewValue: ptr("x"), AuthorID: 7,
	})
	assert.ErrorIs(t, err, collab.ErrFieldNotProposable,
		"a non-proposable field must not leak the fact that it is also sticky-rejected")
	assert.NotErrorIs(t, err, collab.ErrStickyRejected)
}

// The replacement is validated exactly as a fresh Create is, and the value check
// is the part most likely to be skipped in a "supersede" code path because it
// looks like it was already validated for the proposal being replaced. It was
// not: that was a different value.
func TestProposal_SupersedeValidatesTheReplacementValue(t *testing.T) {
	store := newFake()
	p := collab.NewProposer(store)

	_, err := p.Create(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 9, Field: "studio_id",
		NewValue: ptr("1"), AuthorID: 7,
	})
	require.NoError(t, err)

	// The open proposal's value was fine; the REPLACEMENT's is not.
	_, err = p.Supersede(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 9, Field: "studio_id",
		NewValue: ptr("not-a-number"), AuthorID: 7,
	})
	assert.ErrorIs(t, err, collab.ErrValueInvalid,
		"a replacement is a new proposal and must pass the same validation")
	assert.Zero(t, store.supers,
		"the open proposal must survive a replacement that was never valid -- "+
			"superseding before validating would destroy a good proposal to make room for a bad one")
	assert.Equal(t, "1", *store.props[k("scene", 9, "studio_id")].NewValue,
		"the original proposal is still the one on record")
}
