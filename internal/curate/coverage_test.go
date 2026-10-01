package curate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The plan's test for step 7.6c: both inputs are COUNTS in the view.
//
// The way that is proved is structural rather than behavioural. A test asserting
// "the count equals 3" cannot tell a COUNT from a stored column — both answer 3.
// What distinguishes them is that the function TAKES THE ROWS, so the assertion
// here is about the signature's effect: delete a row and the count falls.
func TestCompletionInputsAreCountsInTheView(t *testing.T) {
	snapshots := []Snapshot{
		{SceneID: "s1", Frames: 12},
		{SceneID: "s1", Frames: 14},
		{SceneID: "s1", Frames: 16},
	}
	links := []Link{
		{SceneID: "s1", PerformerID: 1, SourceID: 10},
		{SceneID: "s1", PerformerID: 2, Confirmed: true},
	}

	count, frames, err := SnapshotCoverage("s1", snapshots)
	require.NoError(t, err)
	assert.Equal(t, 3, count, "a COUNT over the rows")
	assert.Equal(t, 42, frames)

	lc, err := LinksFor("s1", links)
	require.NoError(t, err)
	assert.Equal(t, 2, lc.Performers)
	assert.Equal(t, 1, lc.Confirmed)
	assert.Equal(t, 1, lc.FromSources)

	// THE PROPERTY. Both functions take ROWS, so deleting a row changes the count
	// and nothing has to be told to change. A stored column cannot do this without a
	// trigger, and there is no reliable trigger here: a row can be removed by an
	// import, a federated sync, or a retention sweep, none of which this code owns.
	afterSnapshots, afterFrames, err := SnapshotCoverage("s1", snapshots[:2])
	require.NoError(t, err)
	assert.Equal(t, 2, afterSnapshots, "delete a snapshot and the count falls")
	assert.Equal(t, 26, afterFrames)

	afterLinks, err := LinksFor("s1", links[:1])
	require.NoError(t, err)
	assert.Equal(t, 1, afterLinks.Performers)
	assert.Equal(t, 0, afterLinks.Confirmed)

	// And to zero. Not to a stale value — to zero.
	empty, _, err := SnapshotCoverage("s1", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, empty, "no snapshots is zero, not a remembered number")

	emptyLinks, err := LinksFor("s1", nil)
	require.NoError(t, err)
	assert.Equal(t, LinkCounts{}, emptyLinks)
}

// THE SCHEMA GUARD, because the plan's fear is a COLUMN and no behaviour test can
// see one. Both column names are the obvious ones a person would reach for.
func TestCompletionInputsAreNotColumns(t *testing.T) {
	assertNoCoverageColumn(t)

	// And the struct cannot be the row either: InputCompleteness has no ID, so it
	// cannot be a table row that accumulates state per entity.
	// (Checked by field names in the test helper below.)
}

// A snapshot with no performer resolved is not a link. Counting it would report a
// scene as linked when it has nobody attached — precisely the gap a curator is
// trying to close, and the reason R070 calls unlinked scenes the canonical gap.
func TestASourceWithNoResolvedPerformerIsNotALink(t *testing.T) {
	lc, err := LinksFor("s1", []Link{
		// A scraped source that never resolved to a performer.
		{SceneID: "s1", SourceID: 10},
		{SceneID: "s1", SourceID: 11},
		{SceneID: "s1", SourceID: 12},
		{SceneID: "s1", PerformerID: 5, SourceID: 10},
	})
	require.NoError(t, err)

	assert.Equal(t, 1, lc.Performers,
		"only the resolved association counts. Three unresolved scrapes and one real "+
			"link is ONE performer, and reporting 4 would close the gap a curator is "+
			"looking at.")
	assert.False(t, LinkComplete(lc), "and the gap is still open")
}

// COMPLETION NEEDS A CONFIRMED LINK. A scraped, unconfirmed association is a claim
// from a source, and §6a.15 wants metadata "present and verified".
func TestAnUnconfirmedScrapedLinkIsPresentButNotComplete(t *testing.T) {
	unconfirmed, err := LinksFor("s1", []Link{
		{SceneID: "s1", PerformerID: 1, SourceID: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, unconfirmed.Performers, "present")
	assert.Equal(t, 0, unconfirmed.Confirmed, "not verified")
	assert.False(t, LinkComplete(unconfirmed),
		"counting an unconfirmed link as complete lets an importer fill every scene's "+
			"completion bar without a human ever looking, which turns a progress bar "+
			"into a measure of importer coverage")

	confirmed, err := LinksFor("s1", []Link{
		{SceneID: "s1", PerformerID: 1, Confirmed: true},
	})
	require.NoError(t, err)
	assert.True(t, LinkComplete(confirmed))
}

// One is enough, for both inputs, and the reasoning is recorded because the
// alternative is defensible and wrong.
func TestOneSnapshotAndOneConfirmedLinkIsComplete(t *testing.T) {
	assert.True(t, SnapshotComplete(1),
		"§6a.15 wants 12-24 evenly spaced keyframes per collage; a second collage of "+
			"the same scene is not more identified, and a target of 3 would leave "+
			"every scene in a curated instance permanently at 2/3 for no gain")
	assert.False(t, SnapshotComplete(0))

	assert.True(t, LinkComplete(LinkCounts{Confirmed: 1}))
	assert.False(t, LinkComplete(LinkCounts{Performers: 5}),
		"five unconfirmed performers is still no confirmed performer. And requiring "+
			"three would make completion a measure of a scene's popularity rather than "+
			"of whether anybody knows who is in it -- a scene genuinely featuring one "+
			"performer would sit at 33%, which tells a curator nothing actionable.")

	// A scene with one performer and three from a scraper: the confirmed one counts.
	assert.True(t, LinkComplete(LinkCounts{Performers: 4, Confirmed: 1}))
}

// Combine evaluates both inputs for one entity, taking RECORDS for both.
func TestCombineTakesRecordsForBothInputs(t *testing.T) {
	full, err := Combine("s1",
		[]Snapshot{{SceneID: "s1", Frames: 12}},
		[]Link{{SceneID: "s1", PerformerID: 1, Confirmed: true}})
	require.NoError(t, err)
	assert.Equal(t, 2, full.Of)
	assert.Equal(t, 2, full.Done)
	assert.Equal(t, 1.0, full.Fraction())

	// Snapshots only.
	partial, err := Combine("s1",
		[]Snapshot{{SceneID: "s1", Frames: 12}},
		nil)
	require.NoError(t, err)
	assert.Equal(t, 1, partial.Done)
	assert.Equal(t, 0.5, partial.Fraction())

	// Nothing at all: the canonical unlinked scene.
	none, err := Combine("s1", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, none.Done)
	assert.Equal(t, 0.0, none.Fraction(),
		"an unlinked, uncovered scene is 0% -- the gap a curator is looking for")
}

// An entity with no inputs required is complete, not 0%. A 0% bar for something
// nothing is expected of looks like a bug to every curator who sees it.
func TestNoRequiredInputsIsComplete(t *testing.T) {
	i := InputCompleteness{Of: 0}
	assert.Equal(t, 1.0, i.Fraction())
}

// A negative is an error, not a clamp. A COUNT cannot be negative, so a negative
// means the rows were walked by code with a bug — and clamping would render a
// plausible-looking progress bar over a broken query.
func TestNegativeRowsAreAnError(t *testing.T) {
	_, _, err := SnapshotCoverage("s1", []Snapshot{{SceneID: "s1", Frames: -1}})
	assert.ErrorIs(t, err, ErrNegativeCount)

	_, err = LinksFor("s1", []Link{{SceneID: "s1", PerformerID: -3}})
	assert.ErrorIs(t, err, ErrNegativeCount)

	_, err = LinksFor("s1", []Link{{SceneID: "s1", PerformerID: 1, SourceID: -9}})
	assert.ErrorIs(t, err, ErrNegativeCount)

	// And it propagates out of Combine rather than being swallowed.
	_, err = Combine("s1", []Snapshot{{SceneID: "s1", Frames: -1}}, nil)
	assert.ErrorIs(t, err, ErrNegativeCount)
}

// Derived inputs must not leak between scenes — and the fixture must therefore
// hand over a MIXED list.
//
// My first version called LinksFor separately for each scene, passing one scene's
// rows each. That let a mutation which ignored SceneID entirely survive: every
// fixture was already filtered, so the check was untestable. A caller whose query
// returned rows for many scenes would have got one tally attributed to whichever
// scene it was asking about, and the progress bar would be confidently wrong.
//
// So both counters now take a scene and filter by it themselves, and this test
// passes one list containing three scenes' rows.
func TestCountsArePerSceneFromAMixedList(t *testing.T) {
	// A single list, three scenes, returned by one query.
	links := []Link{
		{SceneID: "a", PerformerID: 1, Confirmed: true},
		{SceneID: "a", PerformerID: 2, Confirmed: true},
		{SceneID: "a", PerformerID: 3},
		{SceneID: "b", PerformerID: 9, Confirmed: true},
		{SceneID: "c", PerformerID: 7},
		{SceneID: "c", PerformerID: 8, Confirmed: true},
		{SceneID: "c", PerformerID: 9, Confirmed: true},
	}

	a, err := LinksFor("a", links)
	require.NoError(t, err)
	assert.Equal(t, 3, a.Performers, "scene a's three links, not all seven")
	assert.Equal(t, 2, a.Confirmed)

	b, err := LinksFor("b", links)
	require.NoError(t, err)
	assert.Equal(t, 1, b.Performers, "scene b's count is its own, not a remainder")
	assert.Equal(t, 1, b.Confirmed)

	c, err := LinksFor("c", links)
	require.NoError(t, err)
	assert.Equal(t, 3, c.Performers)
	assert.Equal(t, 2, c.Confirmed)

	// And a scene with no rows at all is genuinely zero rather than somebody
	// else's tally — this is the canonical unlinked scene.
	none, err := LinksFor("d", links)
	require.NoError(t, err)
	assert.Equal(t, LinkCounts{}, none,
		"a scene absent from the list counts as zero, not as the 3 links scene c "+
			"happened to contribute")

	// Same for snapshots: mixed list, one scene counted.
	snaps := []Snapshot{
		{SceneID: "a", Frames: 12},
		{SceneID: "b", Frames: 20},
		{SceneID: "b", Frames: 8},
		{SceneID: "c", Frames: 12},
	}
	ca, fa, err := SnapshotCoverage("a", snaps)
	require.NoError(t, err)
	assert.Equal(t, 1, ca, "scene a has one snapshot")
	assert.Equal(t, 12, fa)

	cb, fb, err := SnapshotCoverage("b", snaps)
	require.NoError(t, err)
	assert.Equal(t, 2, cb, "scene b has two")
	assert.Equal(t, 28, fb, "and 28 frames, not 40 from the whole list")

	cd, fd, err := SnapshotCoverage("d", snaps)
	require.NoError(t, err)
	assert.Equal(t, 0, cd)
	assert.Equal(t, 0, fd)
}

// Combine's sceneID is load-bearing, not decorative: it names the entity whose
// completion is being computed.
func TestCombineNamesTheEntityItComputes(t *testing.T) {
	links := []Link{
		{SceneID: "a", PerformerID: 1, Confirmed: true},
		{SceneID: "b", PerformerID: 2},
	}
	snaps := []Snapshot{
		{SceneID: "a", Frames: 12},
		{SceneID: "b", Frames: 12},
	}

	// Scene b has a snapshot but only an UNCONFIRMED link.
	b, err := Combine("b", snaps, links)
	require.NoError(t, err)
	assert.Equal(t, 1, b.Done, "snapshot done, link not confirmed")
	assert.Equal(t, 0.5, b.Fraction())

	// Scene a is complete, and the fact that b's rows were in the same list does
	// not leak into it.
	a, err := Combine("a", snaps, links)
	require.NoError(t, err)
	assert.Equal(t, 2, a.Done, "scene a has a confirmed link of its own")
	assert.Equal(t, 1.0, a.Fraction())
}

// The structural half: the derived type cannot be a row, because it carries no
// identity. A row-shaped struct is how "derived" quietly becomes "stored".
func TestTheDerivedTypeIsNotARow(t *testing.T) {
	fields := fieldNamesOf(InputCompleteness{})
	for _, banned := range []string{"ID", "SceneID", "EntityID", "UpdatedAt", "CreatedAt"} {
		assert.NotContains(t, fields, banned,
			"InputCompleteness carries %s, so it could be persisted per entity and "+
				"become the stored counter this package exists to avoid", banned)
	}

	// LinkCounts likewise: it is a tally of rows, not a row.
	lc := fieldNamesOf(LinkCounts{})
	for _, banned := range []string{"SceneID", "ID", "UpdatedAt"} {
		assert.NotContains(t, lc, banned)
	}
}
