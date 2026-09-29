package match

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// A fake performer finder that returns a caller-controlled set of performers
// for a name lookup. It exists so the ambiguity case can be tested without a
// database: the defect being guarded here is decided entirely by the LENGTH
// of the result set, so the length is the only thing the test needs to set.
type fakePerformerFinder struct {
	byName    map[string][]*models.Performer
	byStashID map[string][]*models.Performer
	aliasHit  map[string]*models.Performer
}

func (f *fakePerformerFinder) Query(_ context.Context, _ *models.PerformerFilterType, _ *models.FindFilterType) ([]*models.Performer, int, error) {
	// performer.ByAlias goes through Query, so the fake must answer it
	// rather than panic. Returning the alias hit (possibly nil) keeps the
	// test honest about which path the code took: an aliasHit of nil and an
	// empty answer are the same thing to ScrapedPerformer, and a populated
	// one is what proves the alias fallback was NOT used.
	var out []*models.Performer
	for _, p := range f.aliasHit {
		out = append(out, p)
	}
	return out, len(out), nil
}

func (f *fakePerformerFinder) QueryCount(_ context.Context, _ *models.PerformerFilterType, _ *models.FindFilterType) (int, error) {
	panic("QueryCount is not used by ScrapedPerformer")
}

func (f *fakePerformerFinder) FindByNames(_ context.Context, names []string, _ bool) ([]*models.Performer, error) {
	var out []*models.Performer
	for _, n := range names {
		out = append(out, f.byName[n]...)
	}
	return out, nil
}

func (f *fakePerformerFinder) FindByStashID(_ context.Context, stashID models.StashID) ([]*models.Performer, error) {
	return f.byStashID[stashID.StashID], nil
}

func named(n int, name string) *models.Performer {
	return &models.Performer{ID: n, Name: name}
}

func strptr(s string) *string { return &s }

// THE BUG. A library can legitimately hold two performers with the same name
// and different disambiguations -- that is what disambiguation is FOR, and
// the uniqueness constraint is on (name, disambiguation), not on name alone.
//
// ScrapedPerformer used to require `len(performers) == 1` to treat a name as
// matched. With two same-named performers the name lookup returns two rows,
// the count is not 1, so the match was DISCARDED, StoredID stayed nil, and
// the caller went on to create a duplicate. Creation then failed on the
// database's own uniqueness constraint, surfacing as
//
//	error creating performer: ... UNIQUE constraint failed: performers.name
//
// and, through the UI's create-then-save path, as
//
//	performer with name 'X' already exists
//
// which is a validation error about a row the user never asked to create.
// The reporter read that as "the existence check failed"; the existence check
// succeeded perfectly and the AMBIGUITY case discarded its result.
//
// The fix resolves the ambiguity with the scraper's OWN disambiguation field,
// which the name lookup never looked at. Note what the fix must NOT do: bind
// the scene to whichever of two same-named performers came back first. These
// two tests hold both halves of that apart, and the second is the one that
// would catch a regression into guessing.

func TestAScraperDisambiguationResolvesAnAmbiguousName(t *testing.T) {
	qb := &fakePerformerFinder{
		byName: map[string][]*models.Performer{
			"Mia Ipanema": {
				{ID: 1, Name: "Mia Ipanema", Disambiguation: "I"},
				{ID: 2, Name: "Mia Ipanema", Disambiguation: "II"},
			},
		},
	}

	// The scraper said which one. The lookup ignored that field entirely.
	p := &models.ScrapedPerformer{
		Name:           strptr("Mia Ipanema"),
		Disambiguation: strptr("II"),
	}
	require.NoError(t, ScrapedPerformer(context.Background(), qb, p, ""))

	require.NotNil(t, p.StoredID,
		"the scraper's disambiguation was supplied and matched exactly one "+
			"candidate, so the name is NOT ambiguous and must resolve")
	assert.Equal(t, "2", *p.StoredID)
}

func TestAnUnresolvableAmbiguityIsNeverSettledByGuessing(t *testing.T) {
	qb := &fakePerformerFinder{
		byName: map[string][]*models.Performer{
			"Mia Ipanema": {
				{ID: 1, Name: "Mia Ipanema", Disambiguation: "I"},
				{ID: 2, Name: "Mia Ipanema", Disambiguation: "II"},
			},
		},
	}

	// No disambiguation from the scraper: nothing to resolve against.
	p := &models.ScrapedPerformer{Name: strptr("Mia Ipanema")}
	require.NoError(t, ScrapedPerformer(context.Background(), qb, p, ""))

	assert.Nil(t, p.StoredID,
		"two real performers share this name and nothing distinguishes them; "+
			"binding the scene to the first one is a guess about a person's "+
			"identity and must not happen silently")
}

// Several candidates can share the SAME disambiguation too -- the constraint
// is a partial unique index, so this is representable, and narrowing must not
// pick among them either.
func TestSeveralCandidatesSharingTheDisambiguationAreStillAmbiguous(t *testing.T) {
	qb := &fakePerformerFinder{
		byName: map[string][]*models.Performer{
			"Twin": {
				{ID: 1, Name: "Twin", Disambiguation: "A"},
				{ID: 2, Name: "Twin", Disambiguation: "A"},
			},
		},
	}

	p := &models.ScrapedPerformer{Name: strptr("Twin"), Disambiguation: strptr("A")}
	require.NoError(t, ScrapedPerformer(context.Background(), qb, p, ""))

	assert.Nil(t, p.StoredID,
		"the disambiguation narrowed 2 candidates to 2, which is no narrowing "+
			"at all; the answer is still ambiguous")
}

func TestASingleNameMatchSetsTheStoredID(t *testing.T) {
	qb := &fakePerformerFinder{
		byName: map[string][]*models.Performer{
			"Solo Performer": {named(7, "Solo Performer")},
		},
	}

	p := &models.ScrapedPerformer{Name: strptr("Solo Performer")}
	require.NoError(t, ScrapedPerformer(context.Background(), qb, p, ""))

	require.NotNil(t, p.StoredID, "a unique name match must set StoredID")
	assert.Equal(t, "7", *p.StoredID)
}

func TestAnUnknownNameStillLeavesTheStoredIDNil(t *testing.T) {
	// The fix must not break the case the existing behaviour was written for:
	// a name that genuinely matches nothing should still be created.
	qb := &fakePerformerFinder{byName: map[string][]*models.Performer{}}

	p := &models.ScrapedPerformer{Name: strptr("Nobody At All")}
	require.NoError(t, ScrapedPerformer(context.Background(), qb, p, ""))

	assert.Nil(t, p.StoredID, "an unmatched name must remain unmatched so the "+
		"caller creates it -- this is the only case where StoredID stays nil")
}

// The alias fallback runs only when the name lookup found NOTHING. With the
// ambiguity fix, an ambiguous name short-circuits before the alias lookup, so
// an alias cannot be allowed to override a real same-name performer.

func TestAnAmbiguousNameDoesNotFallThroughToAnAlias(t *testing.T) {
	qb := &fakePerformerFinder{
		byName: map[string][]*models.Performer{
			"Dual Match": {named(1, "Dual Match"), named(2, "Dual Match")},
		},
		aliasHit: map[string]*models.Performer{
			"Dual Match": named(99, "Some Other Person"),
		},
	}

	p := &models.ScrapedPerformer{Name: strptr("Dual Match")}
	require.NoError(t, ScrapedPerformer(context.Background(), qb, p, ""))

	if p.StoredID != nil {
		assert.NotEqual(t, "99", *p.StoredID,
			"an ambiguous name must not be resolved by the alias fallback; "+
				"the alias belongs to a different performer")
	}
}

// A stash-box ID is an identity, not a name. When the remote site supplies one
// it must win outright, even if a local performer shares the name.

func TestARemoteSiteIDOutranksAnAmbiguousName(t *testing.T) {
	qb := &fakePerformerFinder{
		byName: map[string][]*models.Performer{
			"Shared Name": {named(1, "Shared Name"), named(2, "Shared Name")},
		},
		byStashID: map[string][]*models.Performer{
			"remote-42": {named(42, "Shared Name")},
		},
	}

	p := &models.ScrapedPerformer{Name: strptr("Shared Name"), RemoteSiteID: strptr("remote-42")}
	require.NoError(t, ScrapedPerformer(context.Background(), qb, p, "stashbox.example"))

	require.NotNil(t, p.StoredID)
	assert.Equal(t, "42", *p.StoredID,
		"an explicit remote identity is unambiguous and must be preferred")
}
