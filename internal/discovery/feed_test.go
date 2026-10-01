package discovery

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// boardType and fieldNames let the no-stored-rank test assert the SHAPE of Board
// rather than its behaviour. A behavioural assertion cannot see a field that is
// never read -- which is precisely the field that must not exist.
func boardType() reflect.Type { return reflect.TypeOf(Board{}) }
func fieldNames(t reflect.Type) []string {
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, t.Field(i).Name)
	}
	return out
}

func TestTheFeedComposesExistingSurfaces(t *testing.T) {
	items := []FeedItem{
		{ID: "r1", Surface: SurfaceRecommendations, Rank: 0},
		{ID: "q1", Surface: SurfaceQuests, Rank: 0},
		{ID: "t1", Surface: SurfaceMeshTrending, Rank: 0},
		{ID: "i1", Surface: SurfaceIdentHighlights, Rank: 0},
		{ID: "b1", Surface: SurfaceBoards, Rank: 0},
		{ID: "a1", Surface: SurfaceAwards, Rank: 0},
		{ID: "s1", Surface: SurfaceInstanceSpotlight, Rank: 0},
	}

	feed, err := Compose(items)
	require.NoError(t, err)

	// EVERY ITEM NAMES ITS SOURCE. This is the plan's assertion: a new feed source
	// cannot appear unlabelled.
	got := feed.Items()
	require.Len(t, got, len(items), "every item appears exactly once")
	for _, it := range got {
		assert.NotEmpty(t, it.Surface,
			"item %s has no source. §6a.7: the feed is a composition of the existing "+
				"surfaces, so each item's source stays legible to the reader.", it.ID)
		assert.Contains(t, KnownSurfaces(), it.Surface,
			"item %s names a surface that is not one of the known ones", it.ID)
	}

	// And each section groups its own items rather than interleaving them.
	bySurface := map[string]int{}
	for _, it := range got {
		bySurface[string(it.Surface)]++
	}
	assert.Equal(t, map[string]int{
		"recommendations": 1, "quests": 1, "mesh_trending": 1,
		"ident_highlights": 1, "boards": 1, "awards": 1, "instance_spotlight": 1,
	}, bySurface)

	// Sections come out in SurfaceOrder, and the top one is the operator's
	// spotlight -- a product decision stated in one place so it can be argued about.
	require.NotEmpty(t, feed.Sections)
	assert.Equal(t, SurfaceInstanceSpotlight, feed.Sections[0].Surface)
	var order []Surface
	for _, s := range feed.Sections {
		order = append(order, s.Surface)
	}
	assert.Equal(t, []Surface{
		SurfaceInstanceSpotlight, SurfaceIdentHighlights, SurfaceQuests,
		SurfaceRecommendations, SurfaceMeshTrending, SurfaceAwards, SurfaceBoards,
	}, order, "sections are ordered by SurfaceOrder, not by any score")
}

// The failure mode the plan names: a new feed source appearing unlabelled.
func TestAnUnknownSurfaceCannotAppearInTheFeed(t *testing.T) {
	feed, err := Compose([]FeedItem{
		{ID: "good", Surface: SurfaceQuests, Rank: 0},
		// Someone adds a surface and forgets to register it.
		{ID: "rogue", Surface: Surface("algorithmic-boost"), Rank: 0},
		{ID: "empty-surf", Surface: Surface(""), Rank: 0},
		{ID: "", Surface: SurfaceQuests, Rank: 0}, // no id at all
	})
	require.NoError(t, err)

	for _, it := range feed.Items() {
		assert.Contains(t, KnownSurfaces(), it.Surface,
			"item %s reached the feed with an unregistered surface", it.ID)
	}
	for _, it := range feed.Items() {
		assert.NotEqual(t, "rogue", it.ID, "an unregistered surface is dropped, not rendered")
		assert.NotEqual(t, "", it.ID, "an item with no id is dropped")
	}
	assert.Len(t, feed.Items(), 1, "only the one well-formed item survives")
}

// §6a.7's design: an ordering is meaningful only WITHIN a surface, so the feed
// groups rather than interleaves. Interleaving would need a cross-surface score,
// which is the stored-counter shape ground rule 4 forbids.
func TestTheFeedDoesNotInterleaveSurfaces(t *testing.T) {
	// Ranks chosen so that a naive flat sort would reorder them badly: the quest's
	// rank 99 is "worse" than the spotlight's rank 0, but they are not comparable.
	//
	// The two quest IDs sort OPPOSITE to their ranks ("a-worst" before "z-best"),
	// so the within-section ordering cannot be produced by the ID tie-break alone.
	// My first fixture used q-low/q-high, which sort the same way as 99/1 -- so
	// deleting the rank comparison entirely changed nothing and the test passed
	// against the mutation. A tie-break that agrees with the primary key is not a
	// check on the primary key.
	feed, err := Compose([]FeedItem{
		{ID: "a-worst", Surface: SurfaceQuests, Rank: 99},
		{ID: "s-top", Surface: SurfaceInstanceSpotlight, Rank: 0},
		{ID: "z-best", Surface: SurfaceQuests, Rank: 1},
	})
	require.NoError(t, err)

	require.Len(t, feed.Sections, 2)
	assert.Equal(t, SurfaceInstanceSpotlight, feed.Sections[0].Surface)
	assert.Equal(t, SurfaceQuests, feed.Sections[1].Surface)

	// Within the quest section the source's own rank order is preserved, which is
	// the opposite of the ID order.
	q := feed.Sections[1].Items
	require.Len(t, q, 2)
	assert.Equal(t, "z-best", q[0].ID,
		"rank 1 before rank 99 -- despite 'z' sorting last, because the RANK decides")
	assert.Equal(t, "a-worst", q[1].ID,
		"and the ID tie-break only applies when ranks are equal")

	// The flat list is grouped, NOT globally sorted, and no cross-surface
	// comparison happened: the quest at rank 99 is not demoted below the trending
	// surface.
	assert.Equal(t, []string{"s-top", "z-best", "a-worst"},
		[]string{feed.Items()[0].ID, feed.Items()[1].ID, feed.Items()[2].ID},
		"a globally sorted feed would need a cross-surface score, which does not exist")

	// A tie on rank falls back to the ID, so a section is reproducible even when
	// the source gave no distinct ordering.
	tied, err := Compose([]FeedItem{
		{ID: "b", Surface: SurfaceQuests, Rank: 5},
		{ID: "a", Surface: SurfaceQuests, Rank: 5},
	})
	require.NoError(t, err)
	require.Len(t, tied.Sections, 1)
	assert.Equal(t, "a", tied.Sections[0].Items[0].ID,
		"equal ranks break on ID ascending, so the same source gives the same feed")
}

// An empty surface is not a section. A blank heading is worse than nothing.
func TestAnEmptySurfaceIsNotASection(t *testing.T) {
	feed, err := Compose([]FeedItem{
		{ID: "a", Surface: SurfaceQuests, Rank: 0},
	})
	require.NoError(t, err)
	require.Len(t, feed.Sections, 1)
	assert.Equal(t, SurfaceQuests, feed.Sections[0].Surface)

	empty, err := Compose(nil)
	require.NoError(t, err)
	assert.Empty(t, empty.Sections, "no items means no sections, not blank headings")
}

// A board is an ORDERING, not a score. Ground rule 4 and the plan's warning: a
// board with an editable `rank` column has become a stored counter.
func TestABoardIsAnOrderingWithNoStoredRank(t *testing.T) {
	b := Board{
		ID:          "b1",
		Title:       "Late night, long shots",
		AuthorID:    7,
		EntityIDs:   []string{"c", "a", "b"},
		Description: "For the hours when nothing else will do.",
	}

	items := ResolveBoard(b)
	require.Len(t, items, 3)

	// The order IS the board -- "c, a, b" is what the author chose, and nothing
	// sorted it into that sequence.
	assert.Equal(t, []string{"c", "a", "b"},
		[]string{items[0].EntityID, items[1].EntityID, items[2].EntityID},
		"a board's order is the author's, preserved exactly")
	for i, it := range items {
		assert.Equal(t, i, it.Position, "positions are derived from slice order")
	}

	// THE STRUCTURAL HALF: Board has no Rank, no Score, and no Weight field.
	// Position exists only on the DERIVED item, never on the stored board.
	typ := boardType()
	assert.NotContains(t, fieldNames(typ), "Rank",
		"Board must not carry a Rank: an author-editable rank is a stored counter "+
			"and leaves the model")
	assert.NotContains(t, fieldNames(typ), "Score")
	assert.NotContains(t, fieldNames(typ), "Weight")

	// And the resolution consults no score at all. A version that sorted by
	// recommendation score would still return three items -- only in a different
	// order -- which is why the assertion above is on ORDER, not on length.
	assert.True(t, BoardIsCuration(b))
}

// Every registered surface must be ordered, or it can never appear in a feed.
//
// This is the invariant the unlabelled-item guarantee actually rests on: Compose
// emits sections by iterating SurfaceOrder, so a surface missing from it is
// dropped there. Without this assertion a new surface could be added to
// KnownSurfaces, render correctly in isolation, and never appear in any feed.
func TestEveryKnownSurfaceIsOrdered(t *testing.T) {
	ordered := map[Surface]bool{}
	for _, s := range SurfaceOrder {
		assert.False(t, ordered[s], "surface %q appears twice in SurfaceOrder", s)
		ordered[s] = true
	}

	for _, s := range KnownSurfaces() {
		assert.True(t, ordered[s],
			"surface %q is registered but never ordered, so it can never appear in "+
				"a feed -- an unlabelled surface by another route", s)
	}

	assert.Len(t, SurfaceOrder, len(KnownSurfaces()),
		"and there are no extra entries in SurfaceOrder that KnownSurfaces does not "+
			"list, which would be a section nothing can ever fill")
}

// Compose's filter is deliberately redundant with the section loop, so the
// unlabelled-item guarantee is asserted at the OUTPUT rather than at either
// mechanism: whatever the internals do, nothing unlabelled escapes.
func TestNoUnlabelledItemEscapesCompose(t *testing.T) {
	// Everything a contributor might plausibly get wrong at once.
	feed, err := Compose([]FeedItem{
		{ID: "ok-1", Surface: SurfaceQuests, Rank: 0},
		{ID: "rogue-1", Surface: Surface("recommendations_v2")},
		{ID: "rogue-2", Surface: Surface("ADMIN_PUSH")},
		{ID: "rogue-3", Surface: Surface(" ")},
		{ID: "", Surface: SurfaceAwards, Rank: 0},
		{ID: "no-source", Surface: Surface(""), Rank: 0},
	})
	require.NoError(t, err)

	got := feed.Items()
	for _, it := range got {
		assert.Contains(t, KnownSurfaces(), it.Surface,
			"item %q escaped with surface %q", it.ID, it.Surface)
		assert.NotEmpty(t, strings.TrimSpace(string(it.Surface)))
		assert.NotEmpty(t, it.ID)
	}

	// And the count is exact, so a rogue item cannot hide by being merged into a
	// section that also holds a legitimate one.
	var ids []string
	for _, it := range got {
		ids = append(ids, it.ID)
	}
	assert.Equal(t, []string{"ok-1"}, ids,
		"exactly one well-formed item survives five malformed ones")
}
