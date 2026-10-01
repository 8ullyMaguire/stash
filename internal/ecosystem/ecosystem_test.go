package ecosystem

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/internal/ident"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The plan's core requirement for 7.7, and §6a.19's: sync pushes edits "through the
// proposal path". The test asserts a proposal exists AND the field is unchanged.
func TestSyncPushesThroughTheProposalPath(t *testing.T) {
	store := newFakeStore()
	syncer := NewSyncer(ident.NewBoard(collab.NewProposer(store)))
	ctx := context.Background()

	before := store.targetValue("scene", 42, "title")
	require.Equal(t, "", before, "precondition: the field starts empty")

	val := "A pushed title"
	prop, err := syncer.Push(ctx, IncomingEdit{
		TargetType:   "scene",
		TargetID:     42,
		Field:        "title",
		Value:        &val,
		CurrentValue: nil,
		Origin:       "stash-app",
		AuthorID:     9,
	})
	require.NoError(t, err)
	require.NotNil(t, prop)

	// A proposal exists, attributed and typed.
	assert.Equal(t, "title", prop.Field)
	assert.Equal(t, 42, prop.TargetID)
	assert.Equal(t, 9, prop.AuthorID)

	// AND THE FIELD IS UNCHANGED. This is the requirement: a pushed edit is filed,
	// not applied. §6a.19 says the rule "applies to sync exactly as it does to the
	// ident board", and this is that assertion.
	assert.Equal(t, "", store.targetValue("scene", 42, "title"),
		"a pushed edit becomes a PROPOSAL. The field changes only when the ordinary "+
			"governance path accepts it.")

	// And it changes THEN, by that path -- otherwise "unchanged" would be satisfied
	// by a syncer that simply never writes anything.
	require.NoError(t, store.applyProposal(prop.ID, 1))
	assert.Equal(t, val, store.targetValue("scene", 42, "title"),
		"once governance accepts, the ordinary path writes it")
}

// §6a.4's vocabulary is the rule about which fields may be pushed at all, so an
// unpushable field is refused by the governance path -- not by a second list here.
func TestAPushOfANonProposableFieldIsRefused(t *testing.T) {
	store := newFakeStore()
	syncer := NewSyncer(ident.NewBoard(collab.NewProposer(store)))
	val := "anything"

	_, err := syncer.Push(context.Background(), IncomingEdit{
		TargetType: "scene", TargetID: 1, Field: "not_a_field",
		Value: &val, Origin: "stash-app", AuthorID: 9,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ident.ErrNotProposable)
	assert.Empty(t, store.proposals, "nothing is filed for a field that cannot be proposed")
}

// Provenance is not optional. An edit whose origin is unknown cannot be reviewed,
// and the audit trail is the entire reason to file a proposal rather than write a
// field.
func TestAnEditWithoutAnOriginIsRefused(t *testing.T) {
	store := newFakeStore()
	syncer := NewSyncer(ident.NewBoard(collab.NewProposer(store)))
	val := "x"

	_, err := syncer.Push(context.Background(), IncomingEdit{
		TargetType: "scene", TargetID: 1, Field: "title",
		Value: &val, AuthorID: 9, // no Origin
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoOrigin)
	assert.Empty(t, store.proposals)

	_, err = syncer.Push(context.Background(), IncomingEdit{
		TargetType: "scene", TargetID: 1, Field: "title",
		Value: &val, Origin: "stash-app", // no AuthorID
	})
	assert.ErrorIs(t, err, ErrNoAuthor)
	assert.Empty(t, store.proposals)
}

// The origin is visible in the proposal, so a reviewer can tell where a field's
// value came from without joining against anything.
func TestTheOriginIsVisibleInTheProposal(t *testing.T) {
	store := newFakeStore()
	syncer := NewSyncer(ident.NewBoard(collab.NewProposer(store)))
	val := "x"

	prop, err := syncer.Push(context.Background(), IncomingEdit{
		TargetType: "scene", TargetID: 1, Field: "title",
		Value: &val, Origin: "stash-forge-peer", AuthorID: 9,
	})
	require.NoError(t, err)

	assert.Contains(t, prop.Rationale, "stash-forge-peer",
		"§4.2: the audit trail is the only way an operator later finds out where a "+
			"field's value came from")
	assert.Contains(t, prop.Rationale, "sync",
		"and it is visibly a sync, distinct from the ident board's own proposals -- "+
			"two write-through paths must not be indistinguishable after the fact")
}

// §6a.21's rule, which §6a.21 goes out of its way to state: an opted-out entity is
// neither indexed nor reachable.
func TestAnOptedOutEntityIsNotIndexed(t *testing.T) {
	optedOut := collab.ChoiceOptedOut

	d := DecideIndexing(optedOut, true)
	assert.False(t, d.Indexable,
		"§6a.21: an opted-out entity is neither indexed nor reachable through a "+
			"public page")
	assert.NotEmpty(t, d.Reason,
		"an operator who cannot see WHY something is not indexed will eventually "+
			"index it by hand")

	// Completeness, popularity and references are all irrelevant: consent is
	// consulted FIRST and the entity's own fields are never examined. §6a.21 names
	// this exact temptation -- "it's metadata, not content" -- as the reasoning
	// non-negotiable #7 exists to stop.
	published := DecideIndexing(collab.ChoiceOptedIn, true)
	require.True(t, published.Indexable, "precondition: a published, opted-in entity IS indexed")

	optedInNotPublished := DecideIndexing(collab.ChoiceOptedIn, false)
	assert.False(t, optedInNotPublished.Indexable,
		"§6a.21: a page for an entity the owner has not published does not get "+
			"created. An entity exists locally from the moment it is imported, long "+
			"before its owner agrees to any of it leaving the instance.")
}

// An unrecognised share choice must not publish somebody's library. Same
// fail-closed rule as preservation and discovery, and the same reason: a new value
// this code has not been taught must not default to "share".
func TestAnUnrecognisedShareChoiceDoesNotPublish(t *testing.T) {
	for _, choice := range []collab.ShareChoice{"", "yes", "OPTED-IN", "public", "opted-out"} {
		if choice == collab.ChoiceOptedOut {
			continue // covered above, but the conclusion is the same
		}
		assert.False(t, Publishable(choice),
			"share choice %q is not opted-in, so it must not publish. The check is for "+
				"opted-in, not for not-opted-out.", choice)
		assert.False(t, DecideIndexing(choice, true).Indexable,
			"and an unpublished entity is never indexed, whatever it is")
	}
}

// Three mechanisms must agree on one consent predicate: replication (7.4), this
// package's indexing (7.7), and sync. Three copies of the rule would drift.
func TestIndexingAgreesWithTheReplicationPredicate(t *testing.T) {
	// preservation.IsReplicationSubject reads the same choice. If the two ever
	// disagree, an entity could be replicated while being unindexable, or the
	// reverse -- and neither is defensible.
	for _, choice := range []collab.ShareChoice{
		collab.ChoiceOptedIn, collab.ChoiceOptedOut, "", "mystery",
	} {
		asReplication := choice == collab.ChoiceOptedIn
		asPublish := Publishable(choice)
		assert.Equal(t, asReplication, asPublish,
			"choice %q: replication and indexing must agree, or a scene can be "+
				"replicated to the mesh while being unindexable", choice)
	}
}

// An unbounded public read on a federated mesh enumerates the whole library, so
// the cap lives in this package rather than at each transport.
func TestAPublicReadIsBounded(t *testing.T) {
	assert.Equal(t, MaxPublicLimit, BoundQuery(PublicQuery{}).Limit,
		"an unset limit is the maximum, not unlimited")
	assert.Equal(t, MaxPublicLimit, BoundQuery(PublicQuery{Limit: 100000}).Limit)
	assert.Equal(t, 10, BoundQuery(PublicQuery{Limit: 10}).Limit)
	assert.Equal(t, 0, BoundQuery(PublicQuery{Offset: -5}).Offset,
		"a negative offset is zero, not an error at the transport")
}

// The public entity shape deliberately omits the consent state. Consent is
// revocable, so a cached copy of it would be a cached permission.
func TestAPublicEntityCarriesNoConsentState(t *testing.T) {
	fields := fieldNamesOf(PublicEntity{})
	for _, banned := range []string{"ShareChoice", "MetadataShare", "Consent", "OptedIn"} {
		assert.NotContains(t, fields, banned,
			"PublicEntity carries %s. §6a.2's consent is per-instance and revocable, "+
				"so shipping it would let a caller cache the permission and use it "+
				"after it was withdrawn.", banned)
	}

	// It does carry Published, because that is a fact about the entity rather than
	// a permission, and re-asking for it on every read would be wasteful.
	assert.Contains(t, fields, "Published")
}

// The structural half of the write-through: Syncer's only dependency is the ident
// board, so there is no field writer for sync to reach.
func TestTheSyncerHasNoFieldWriter(t *testing.T) {
	s := NewSyncer(ident.NewBoard(collab.NewProposer(newFakeStore())))
	methods := methodNamesOf(s)
	for _, m := range methods {
		assert.Equal(t, "Push", m,
			"Syncer exposes %q; anything that writes a field is a bypass of "+
				"§6a.19's write-through rule", m)
	}
}

// --- a minimal ProposalStore, so the syncer can be tested without a database ---

type fakeProposal struct {
	collab.Proposal
	decided bool
}

type fakeStore struct {
	proposals []*fakeProposal
	values    map[string]string
	nextID    int
}

func newFakeStore() *fakeStore {
	return &fakeStore{values: map[string]string{}, nextID: 1}
}

func fieldKey(targetType string, id int, field string) string {
	return fmt.Sprintf("%s/%d/%s", targetType, id, field)
}

func (f *fakeStore) targetValue(t string, id int, field string) string {
	return f.values[fieldKey(t, id, field)]
}

func (f *fakeStore) Create(_ context.Context, p *collab.Proposal) (*collab.Proposal, error) {
	p.ID = f.nextID
	f.nextID++
	fp := &fakeProposal{Proposal: *p}
	f.proposals = append(f.proposals, fp)
	return &fp.Proposal, nil
}

func (f *fakeStore) FindOpen(_ context.Context, t string, id int, field string) (*collab.Proposal, bool, error) {
	for _, p := range f.proposals {
		if !p.decided && p.TargetType == t && p.TargetID == id && p.Field == field {
			return &p.Proposal, true, nil
		}
	}
	return nil, false, nil
}

func (f *fakeStore) RejectionsFor(_ context.Context, _ int) (map[collab.StickyRejectionKey]bool, error) {
	return map[collab.StickyRejectionKey]bool{}, nil
}

func (f *fakeStore) Supersede(_ context.Context, _ string, _ int, _ string) (int, bool, error) {
	return 0, false, nil
}

// applyProposal stands in for the governance path accepting a proposal. ONLY this
// writes a target value, which is what makes "the syncer cannot write" observable.
func (f *fakeStore) applyProposal(id, decider int) error {
	for _, p := range f.proposals {
		if p.ID != id {
			continue
		}
		p.decided = true
		if p.NewValue == nil {
			delete(f.values, fieldKey(p.TargetType, p.TargetID, p.Field))
		} else {
			f.values[fieldKey(p.TargetType, p.TargetID, p.Field)] = *p.NewValue
		}
		_ = decider
		return nil
	}
	return errNoSuchProposal
}

var errNoSuchProposal = errors.New("no such proposal")

// fieldNamesOf and methodNamesOf let the structural assertions read the SHAPE
// rather than behaviour. A behavioural test cannot see a field that is never
// read, which is exactly the field that must not exist.
func fieldNamesOf(v any) []string {
	t := reflect.TypeOf(v)
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, t.Field(i).Name)
	}
	return out
}

func methodNamesOf(v any) []string {
	t := reflect.TypeOf(v)
	out := make([]string, 0, t.NumMethod())
	for i := 0; i < t.NumMethod(); i++ {
		out = append(out, t.Method(i).Name)
	}
	return out
}
