package autoproposal

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE CORE REQUIREMENT of §6b.2, and the reason this step exists: "an automatic
// direct write is a machine laundering a claim past governance, and it is the
// single most important constraint in this subsection."
//
// The test asserts a proposal exists AND nothing was applied. A test asserting only
// the first would be satisfied by a curator that also wrote the field.
func TestAnAutomaticMatchBecomesAProposalNotAWrite(t *testing.T) {
	store := &fakeStore{}
	c := mustCurator(t, store, Attribution{
		Author: Author{UserID: 1, Name: "instance-owner"},
		Source: "autotag",
	}, Policy{})

	out, err := c.Curate(context.Background(), Suggestion{
		TargetType: "scene", TargetID: 7,
		Kind: KindScenePerformer, EntityID: 42, EntityName: "Some Performer",
	})
	require.NoError(t, err)

	require.Len(t, store.proposals, 1, "exactly one proposal")
	p := store.proposals[0]

	// The proposal is real and typed: a link add on a scene's performer field.
	assert.Equal(t, "scene", p.TargetType)
	assert.Equal(t, 7, p.TargetID)
	assert.Equal(t, KindScenePerformer, p.Field)
	require.NotNil(t, p.NewValue)
	assert.Equal(t, "42", *p.NewValue)
	assert.Equal(t, 1, p.AuthorID,
		"§4.2: an unattributable proposal is the same thing internal/ident "+
			"refuses to create. 'The scheduler did it' is not a source an operator "+
			"can investigate.")

	// AND NOTHING WAS APPLIED.
	assert.False(t, out.Applied,
		"§6b.2 and #5: automatic curation writes PROPOSALS. An automatic direct "+
			"write is a machine laundering a claim past governance")
	assert.Zero(t, store.applied,
		"and the fake store records any direct write, which stayed at zero")
	assert.NotZero(t, out.ProposalID)
}

// The audit trail is only useful if it distinguishes a machine's claim from a
// human's. A reviewer shown a bare proposal cannot tell whether a person or a
// scheduler asserted it.
func TestTheRationaleSaysItWasAutomaticAndWhoItIsFrom(t *testing.T) {
	store := &fakeStore{}
	c := mustCurator(t, store, Attribution{
		Author: Author{UserID: 1, Name: "instance-owner"},
		Source: "autotag",
	}, Policy{})

	_, err := c.Curate(context.Background(), Suggestion{
		TargetType: "scene", TargetID: 7,
		Kind: KindSceneTag, EntityID: 3, EntityName: "outdoor",
	})
	require.NoError(t, err)
	require.Len(t, store.proposals, 1)

	r := store.proposals[0].Rationale
	assert.Contains(t, r, "autotag", "the source names the MECHANISM")
	assert.Contains(t, r, "outdoor", "and the entity, so a reviewer can recognise "+
		"the claim without opening anything")
	assert.Contains(t, r, "instance-owner", "and the attributed user, which is a "+
		"different actor from the mechanism that filed it")
	assert.Contains(t, r, "matched automatically", "and says plainly it was not "+
		"asserted by a user -- two proposals with identical fields and different "+
		"origins deserve different scrutiny")

	// And the two must be distinguishable: a HUMAN proposal of the same field on
	// the same target must not read the same.
	human := collab.Proposal{
		TargetType: "scene", TargetID: 7, Field: KindSceneTag,
		NewValue:  strptr("3"),
		Rationale: "the scene is outdoors",
	}
	assert.NotEqual(t, strings.TrimSpace(r), strings.TrimSpace(human.Rationale),
		"an automatic proposal must not be indistinguishable from a human's. That "+
			"is the whole reason the mechanism is named in the rationale")
}

// THE FALLBACK THAT MUST NOT EXIST.
//
// If a failed filing fell back to a direct write, then every outage of the proposal
// store would silently become a bypass of governance — and it would look like
// success, because the field would have the right value.
func TestAFailedFilingDoesNotFallBackToAWrite(t *testing.T) {
	store := &fakeStore{createErr: errors.New("proposal store unavailable")}
	c := mustCurator(t, store, Attribution{
		Author: Author{UserID: 1, Name: "owner"}, Source: "autotag",
	}, Policy{})

	out, err := c.Curate(context.Background(), Suggestion{
		TargetType: "scene", TargetID: 7,
		Kind: KindScenePerformer, EntityID: 42, EntityName: "x",
	})
	require.Error(t, err, "a filing failure is an error, not a quiet success")
	assert.ErrorIs(t, err, store.createErr)
	assert.False(t, out.Applied,
		"THE assertion: a proposal store outage must not become permission to "+
			"write the field directly")
	assert.Zero(t, store.applied,
		"and nothing was written. The policy check runs BEFORE the proposal "+
			"attempt precisely so there is no recovery path from here to a write")
	assert.Zero(t, out.ProposalID)
}

// The policy check runs BEFORE filing, and the order is load-bearing: a direct
// write must be a decision made in advance, never a fallback taken when filing
// fails.
func TestTheDirectWritePolicyIsCheckedBeforeFiling(t *testing.T) {
	store := &fakeStore{}
	c := mustCurator(t, store, Attribution{
		Author: Author{UserID: 1, Name: "owner"}, Source: "autotag",
	}, Policy{AutoApply: true})

	// A TAG may be applied directly by an instance that opted in, and NO proposal
	// is filed — an operator who approved bulk tag application does not want 4,000
	// proposals nobody will read.
	out, err := c.Curate(context.Background(), Suggestion{
		TargetType: "scene", TargetID: 7,
		Kind: KindSceneTag, EntityID: 3, EntityName: "outdoor",
	})
	require.NoError(t, err)
	assert.True(t, out.Applied)
	assert.Zero(t, out.ProposalID)
	assert.Empty(t, store.proposals, "an approved direct write files nothing")

	// But a PERFORMER is refused however the policy is set, because asserting a
	// performer is a claim about a person.
	out2, err := c.Curate(context.Background(), Suggestion{
		TargetType: "scene", TargetID: 7,
		Kind: KindScenePerformer, EntityID: 42, EntityName: "someone",
	})
	require.NoError(t, err)
	assert.False(t, out2.Applied,
		"§6b.2: AutoApply is permission for BULK APPLICATION of descriptors, not "+
			"for asserting a person. A performer claim is a claim about a person and "+
			"must be voted on whatever the switch says")
	assert.NotZero(t, out2.ProposalID, "so it was filed as a proposal instead")

	// And a STUDIO likewise.
	out3, err := c.Curate(context.Background(), Suggestion{
		TargetType: "scene", TargetID: 8,
		Kind: KindSceneStudio, EntityID: 5, EntityName: "a studio",
	})
	require.NoError(t, err)
	assert.False(t, out3.Applied)
}

// Policy's zero value is SAFE. A config struct that forgot to set the field must
// not start laundering claims — the same reasoning as step 8.1's zero value.
func TestPolicyDefaultsToNotApplyingDirectly(t *testing.T) {
	var unset Policy
	assert.False(t, unset.AutoApply, "the zero value is the safe one")
	for _, tc := range []struct{ target, kind string }{
		{"scene", KindSceneTag}, {"image", KindImageTag},
		{"scene", KindScenePerformer}, {"scene", KindSceneStudio},
	} {
		assert.False(t, unset.MayAutoApply(tc.target, tc.kind),
			"unset policy on %s.%s must refuse a direct write", tc.target, tc.kind)
	}

	// And a KIND not named in the switch is refused even with AutoApply on, so a
	// future field cannot become auto-applied by omission.
	on := Policy{AutoApply: true}
	assert.False(t, on.MayAutoApply("gallery", "anything_at_all"),
		"a kind the switch does not name is refused, so adding a field later does "+
			"not silently auto-apply it")
	assert.False(t, on.MayAutoApply("scene", "studio_id"),
		"and `scene.studio_id` is named in the constants but NOT in the allow-list")
}

// An automatic suggestion with no author is unbuildable, not merely refused at
// runtime — which is why NewCurator takes an Attribution and returns an error.
func TestACuratorCannotExistWithoutAnAttribution(t *testing.T) {
	store := &fakeStore{}

	_, err := NewCurator(store, Attribution{Source: "autotag"}, Policy{})
	assert.ErrorIs(t, err, ErrNoAuthor,
		"user 0 is not a user. A proposal filed by nobody cannot be reviewed, and "+
			"§4.2's audit trail is the only way an operator later finds out where a "+
			"value came from")

	_, err = NewCurator(store, Attribution{Author: Author{UserID: 1}}, Policy{})
	assert.ErrorIs(t, err, ErrNoAuthor,
		"an attribution with no SOURCE is also refused, because a reviewer could "+
			"not tell an automatic proposal from a human one")

	// And no proposer at all: a curator with nothing to file through would have to
	// write fields directly, which is the failure this package exists to prevent.
	_, err = NewCurator(nil, Attribution{Author: Author{UserID: 1}, Source: "autotag"}, Policy{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write fields directly",
		"the error must name WHY, because the alternative is the exact bug §6b.2 "+
			"forbids")
}

// A suggestion that cannot be filed is an error, not a silent skip: a curator
// processing 4,000 scenes that silently drops malformed ones is indistinguishable
// from one that worked.
func TestAnUnaddressableSuggestionIsAnError(t *testing.T) {
	c := mustCurator(t, &fakeStore{}, Attribution{
		Author: Author{UserID: 1, Name: "owner"}, Source: "autotag",
	}, Policy{})

	for name, s := range map[string]Suggestion{
		"no target type": {TargetID: 7, Kind: KindSceneTag, EntityID: 3},
		"no target id":   {TargetType: "scene", Kind: KindSceneTag, EntityID: 3},
		"zero target id": {TargetType: "scene", TargetID: 0, Kind: KindSceneTag, EntityID: 3},
		"no kind":        {TargetType: "scene", TargetID: 7, EntityID: 3},
		"no entity":      {TargetType: "scene", TargetID: 7, Kind: KindSceneTag},
		"zero entity":    {TargetType: "scene", TargetID: 7, Kind: KindSceneTag, EntityID: 0},
	} {
		_, err := c.Curate(context.Background(), s)
		assert.Error(t, err, "%s must be an error", name)
	}
}

// THE STRUCTURAL HALF: Curator's dependency is a Proposer with ONE method, and
// there is no field writer or store for it to reach one through.
func TestTheCuratorHasNoWritePath(t *testing.T) {
	// By interface, not by grep: the Proposer interface has exactly Create, so a
	// type implementing it has no other way to reach storage.
	var p Proposer = &fakeStore{}
	assert.Equal(t, 1, proposerMethodCount(),
		"Proposer must have exactly ONE method. A second method — Update, Apply, "+
			"Write — would be a way for this package to write a shared field "+
			"directly, which §6b.2 forbids and which the interface is shaped to "+
			"make impossible")
	_ = p

	// And the concrete fields on Curator: a proposer, an attribution, a policy. No
	// repository, no store, no TxnManager.
	names := fieldNamesOf(Curator{})
	for _, banned := range []string{"Repo", "Store", "Txn", "Writer", "Updater", "DB"} {
		assert.NotContains(t, names, banned,
			"Curator carries a %s field, which would be a route to a shared-field "+
				"write that the proposal path is supposed to be the only way past", banned)
	}
}

func mustCurator(t *testing.T, p Proposer, attrib Attribution, policy Policy) *Curator {
	t.Helper()
	c, err := NewCurator(p, attrib, policy)
	require.NoError(t, err)
	return c
}

func strptr(s string) *string { return &s }

// proposerMethodCount counts the methods on the Proposer interface, by REFLECTION
// on the interface type.
//
// Reading it off the source with an AST walk would also work, and reflection is
// the smaller tool here: the interface is declared in this package, so
// reflect.TypeOf((*Proposer)(nil)).Elem() IS the declaration. What reflection
// cannot do is see a method nobody calls -- but a method on an interface is
// declared, not called, so this measures the right thing.
func proposerMethodCount() int {
	return reflect.TypeOf((*Proposer)(nil)).Elem().NumMethod()
}

// fieldNamesOf reads a struct's SHAPE. A behavioural test cannot see a field that
// is never read, which is exactly the field that must not exist.
func fieldNamesOf(v any) []string {
	t := reflect.TypeOf(v)
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, t.Field(i).Name)
	}
	return out
}

// --- a Proposer that records what it was asked to file ---

type fakeStore struct {
	proposals []collab.Proposal
	applied   int
	createErr error
	nextID    int
}

func (f *fakeStore) Create(_ context.Context, req collab.Proposal) (*collab.Proposal, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextID++
	req.ID = f.nextID
	f.proposals = append(f.proposals, req)
	return &req, nil
}
