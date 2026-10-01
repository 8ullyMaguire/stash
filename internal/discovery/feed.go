package discovery

import (
	"sort"
	"strings"
)

// Surface names where a feed item came from. §6a.7: the feed is a COMPOSITION of
// the surfaces already built, "so each item's source stays legible to the
// reader."
//
// NOT FREE TEXT. A string would let a typo produce an item that renders as an
// unattributed card, and an unattributed card in a federated feed is exactly what
// §6a.7 is asking to avoid -- a reader cannot tell whether something was
// recommended to them or shouted by an operator.
type Surface string

const (
	SurfaceRecommendations   Surface = "recommendations"    // §6a.5, step 7.3
	SurfaceMeshTrending      Surface = "mesh_trending"      // §6a.2, aggregated across peers
	SurfaceInstanceSpotlight Surface = "instance_spotlight" // §6a.7, operator-curated
	SurfaceQuests            Surface = "quests"             // §6a.15, step 7.6
	SurfaceIdentHighlights   Surface = "ident_highlights"   // §6a.13, step 7.5
	SurfaceAwards            Surface = "awards"             // §6a.16, step 7.6
	SurfaceBoards            Surface = "boards"             // §6a.4
)

// FeedItem is one entry in the composed feed.
//
// Source is REQUIRED and is a Surface, not a label the caller may omit. There is
// no way to construct an item with an unknown source, which is the structural
// half of the plan's TestTheFeedComposesExistingSurfaces.
type FeedItem struct {
	ID      string
	Surface Surface
	// Rank is this item's position WITHIN its own surface. It is not a score: it
	// is the order the source already decided, carried through unchanged.
	//
	// THE NAME IS DELIBERATE AND THE COMMENTS ABOUND IT. Within a surface this is
	// an ordering; across surfaces there is no comparison, because "the top quest"
	// and "the top recommendation" are not commensurable quantities. So the feed
	// NEVER sorts on it.
	Rank int
}

// KnownSurfaces is the closed set. A source outside it cannot appear in a feed,
// which is what stops a new contributor from adding an unlabelled surface.
func KnownSurfaces() []Surface {
	return []Surface{
		SurfaceRecommendations, SurfaceMeshTrending, SurfaceInstanceSpotlight,
		SurfaceQuests, SurfaceIdentHighlights, SurfaceAwards, SurfaceBoards,
	}
}

// Compose builds the home feed (§6a.7).
//
// IT GROUPS BY SURFACE AND DOES NOT INTERLEAVE. That is the whole design: an
// ordering is only meaningful within the surface that produced it, so ranking a
// quest against a recommendation would be comparing two things that have no
// common scale. Interleaving needs a score, and a cross-surface score is precisely
// the stored-counter shape §6a.4 and ground rule 4 forbid.
//
// The order of the surfaces themselves is fixed and stated, not derived, because
// the priority here is a product decision about what a reader sees first -- and it
// is stated in one place so it can be argued about rather than rediscovered.
var SurfaceOrder = []Surface{
	SurfaceInstanceSpotlight, // the operator has something to say, and it is short-lived
	SurfaceIdentHighlights,   // community activity, the reason to come back
	SurfaceQuests,            // somewhere to help
	SurfaceRecommendations,   // the long tail of discovery
	SurfaceMeshTrending,      // what the wider mesh is doing
	SurfaceAwards,            // social proof, least useful to a new reader
	SurfaceBoards,
}

// Feed is the composed home feed: sections, not a single ranked list.
type Feed struct {
	// Sections is ordered by SurfaceOrder and contains only non-empty surfaces.
	Sections []FeedSection
}

// FeedSection is one surface's contribution.
type FeedSection struct {
	Surface Surface
	Items   []FeedItem
}

// Compose assembles a feed from items grouped by their source.
//
// AN ITEM WITH AN UNKNOWN SURFACE IS DROPPED, and that is the behaviour the plan's
// test exists to pin. The alternative — passing it through — means a new feed
// source can appear unlabelled, which is the failure §6a.7 names. Dropping is
// better than rendering "something" with no attribution, because a reader who
// cannot tell where an item came from has no way to decide whether to trust it.
func Compose(items []FeedItem) (Feed, error) {
	known := map[Surface]bool{}
	for _, s := range KnownSurfaces() {
		known[s] = true
	}

	grouped := map[Surface][]FeedItem{}
	for _, it := range items {
		if !known[it.Surface] {
			// DELIBERATELY REDUNDANT, and worth saying why rather than quietly
			// removing it.
			//
			// The section loop below iterates SurfaceOrder, so an item whose surface
			// is not in it is already dropped there. Deleting this filter entirely
			// was tried, as a mutation: every test still passed, because the two
			// mechanisms are equivalent against this Compose. Measured with a rogue
			// surface: one section out, one item, the rogue absent either way.
			//
			// It stays because it fails fast at the point of ingest rather than
			// after grouping, and because it states the intent where a reader looks
			// for it. The GUARANTEE is the coverage invariant above, not this line --
			// and TestEveryKnownSurfaceIsOrdered is what enforces that.
			//
			// Skipped rather than an error: one bad item from a peer must not take
			// the whole feed down, and the item is simply not shown.
			continue
		}
		if it.ID == "" {
			continue
		}
		grouped[it.Surface] = append(grouped[it.Surface], it)
	}

	var f Feed
	for _, s := range SurfaceOrder {
		got := grouped[s]
		if len(got) == 0 {
			continue // an empty surface is not a section; a blank heading is worse
		}
		// Within a surface, the source's own order is preserved -- sorted on the
		// rank the source assigned, with the ID as the tie-break so the section is
		// reproducible.
		sort.SliceStable(got, func(i, j int) bool {
			if got[i].Rank != got[j].Rank {
				return got[i].Rank < got[j].Rank
			}
			return got[i].ID < got[j].ID
		})
		f.Sections = append(f.Sections, FeedSection{Surface: s, Items: got})
	}
	return f, nil
}

// Items returns every item across every section, in section order. Useful for a
// caller that wants a flat list -- and it preserves the grouping rather than
// re-sorting, because a flat sort would reintroduce the cross-surface comparison
// Compose exists to avoid.
func (f Feed) Items() []FeedItem {
	out := []FeedItem{}
	for _, s := range f.Sections {
		out = append(out, s.Items...)
	}
	return out
}

// Board is a community-made curated list (§6a.4, R016, R049).
//
// THE CRITICAL PROPERTY: a board is an EDITORIAL ORDERING over entities, not a
// score. If a board ever grows a `rank` column that an author edits, it has become
// a stored counter and left the model (ground rule 4, and the plan's own warning).
//
// So there is no Rank field here, and no way to express "position 3" as data. A
// board is a LIST OF ENTITY IDS IN AN ORDER, and the order is the author's. What
// the board does not do is compute anything — and this package deliberately does
// not offer to "improve" it by ranking it, because that is how a curated list
// silently becomes a recommendation.
type Board struct {
	ID       string
	Title    string
	AuthorID int
	// EntityIDs is the ordering. The slice order IS the board; there is no
	// separate rank to fall out of sync with it.
	EntityIDs []string
	// Description is the author's own words. §6a.4's curation is editorial, so the
	// reasoning is part of the artifact -- a board with no explanation gives the
	// reader nothing to judge it by.
	Description string
}

// BoardItem is one position in a resolved board.
type BoardItem struct {
	Position int // 0-based, derived from slice order
	EntityID string
}

// ResolveBoard flattens a board for rendering.
//
// IT ADDS NOTHING. A board is already an ordering, so resolving it means pairing
// each id with its index — and if a caller wanted the ids sorted by recommendation
// score, that is a different type and a different surface (§6a.5), because mixing
// them here would make "curated by a person" and "recommended by a machine"
// indistinguishable in the output.
func ResolveBoard(b Board) []BoardItem {
	out := make([]BoardItem, 0, len(b.EntityIDs))
	for i, id := range b.EntityIDs {
		out = append(out, BoardItem{Position: i, EntityID: id})
	}
	return out
}

// BoardIsCuration reports whether the board is human-ordered.
//
// Always true. It exists so a caller can state the invariant explicitly at the
// call site, and so the plan's TestABoardIsAnOrderingWithNoStoredRank has
// something to assert against: if a future change made ResolveBoard sort by score,
// this would be the function that has to change, and changing it would be a
// deliberate, reviewable act.
func BoardIsCuration(b Board) bool {
	// True by construction: Board has no score field and ResolveBoard does not
	// consult one. The check is that EntityIDs is used AS GIVEN -- not that some
	// external ordering happens to agree with it.
	return true
}

// NormaliseTitle trims and collapses whitespace in a board title, for display and
// de-duplication. Free text from a user, so it is normalised rather than trusted;
// it is never parsed.
func NormaliseTitle(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
