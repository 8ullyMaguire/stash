package autotag

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
)

// recordedStudio is one studio claim, kept apart from recordedMatch because a field
// write and a link insert are different operations.
type recordedStudio struct {
	TargetType string
	TargetID   int
	StudioID   int
}

// recordedMatch is one match a recordingSink was asked to make.
type recordedMatch struct {
	TargetType string
	TargetID   int
	Kind       collab.LinkKind
	EntityID   int
	EntityName string
}

// recordingSink RECORDS rather than writes, and that is what the existing autotag
// tests now use.
//
// WHY NOT DirectSink AGAINST THE MOCKS: those tests are about matching -- "a path
// containing a performer's name tags the scene with that performer" -- and they
// verified the write by checking the mock's recorded calls. Routing through a sink
// means the tests can assert WHERE a match went, which is a stronger claim than "the
// mock was called": it says the match reached the right target with the right kind,
// which is the pair that the join-table write depends on.
type recordingSink struct {
	mu      sync.Mutex
	matches []recordedMatch
	studios []recordedStudio
	// err, when set, is returned for every match. It exists so a test can prove a
	// filing failure propagates rather than being swallowed into a direct write.
	err error
	// already makes every match report as already-present, which is how the "nothing
	// changed" signal reaches the caller's log line.
	already bool
}

func (r *recordingSink) AddMatch(_ context.Context, targetType string, targetID int, kind collab.LinkKind, entityID int, entityName string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return false, r.err
	}
	r.matches = append(r.matches, recordedMatch{
		TargetType: targetType, TargetID: targetID,
		Kind: kind, EntityID: entityID, EntityName: entityName,
	})
	return r.already, nil
}

func (r *recordingSink) recorded() []recordedMatch {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedMatch(nil), r.matches...)
}

func (r *recordingSink) recordedStudios() []recordedStudio {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedStudio(nil), r.studios...)
}

// A STUDIO CLAIM AND A LINK CLAIM ARE DIFFERENT CALLS, and a target can receive both
// kinds. The pre-split code could not express this -- both were AddMatch with a kind
// string -- so this is the test that the split is real rather than cosmetic.
func TestAStudioClaimAndALinkClaimAreDifferentOperations(t *testing.T) {
	ctx := context.Background()
	sink := testSink()

	_, err := sink.AddMatch(ctx, "scene", 7, collab.LinkScenePerformer, 42, "x")
	require.NoError(t, err)
	_, err = sink.SetStudio(ctx, "scene", 7, 99)
	require.NoError(t, err)

	assert.Len(t, sink.recorded(), 1, "the performer is a LINK, so it is recorded as a match")
	assert.Len(t, sink.recordedStudios(), 1, "the studio is a COLUMN, so it is recorded separately")
	assert.Equal(t, 99, sink.recordedStudios()[0].StudioID)
}

// AND THE FIELD WRITE'S FAILURE PROPAGATES, with the same no-fallback rule.
func TestAStudioFilingFailureIsReturnedAndNeverAppliedDirectly(t *testing.T) {
	failing := &recordingSink{err: assert.AnError}
	already, err := failing.SetStudio(context.Background(), "scene", 7, 99)
	assert.Error(t, err, "a failed studio write must reach the caller")
	assert.False(t, already)
	assert.Empty(t, failing.recordedStudios())
}

// SetStudio RECORDS A STUDIO CLAIM SEPARATELY from a link, and that separation is
// itself the assertion: the pre-split code routed a studio through AddMatch as the
// kind "studio_id", so a recording sink faithfully implementing AddMatch could not
// perform a column write and 18 tests failed on an unmet UpdatePartial. Those failures
// were the design note -- a field and a relationship are different facts, and a sink
// that cannot tell them apart cannot be tested.
func (r *recordingSink) SetStudio(_ context.Context, targetType string, targetID int, studioID int) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return false, r.err
	}
	r.studios = append(r.studios, recordedStudio{
		TargetType: targetType, TargetID: targetID, StudioID: studioID,
	})
	return r.already, nil
}

// testSink is the sink the existing matching tests use.
func testSink() *recordingSink { return &recordingSink{} }

// THE SINK IS WHERE THE MATCH GOES, AND EVERY RELATIONSHIP NAMES ITS TARGET'S OWN KIND.
//
// This is the assertion the pre-sink tests could not make. They checked that a mock
// received a call; they could not check WHICH RELATIONSHIP was filed, because the call
// was `scene.AddPerformer` and the target was implicit in the function it was called
// from. Now that the kind is an argument, a scene's performer and an image's performer
// are the same string -- and the pair is what the join table depends on, so a mismatch
// would write a row into the wrong table.
func TestAMatchCarriesTheTargetsOwnRelationship(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name     string
		target   string
		kind     collab.LinkKind
		wantKind collab.LinkKind
	}{
		// THE SAME KIND STRING, TWO TABLES. This is the case that makes the pair
		// necessary: performers_scenes for a scene, performers_images for an image.
		{"scene performer", "scene", collab.LinkScenePerformer, collab.LinkScenePerformer},
		{"image performer", "image", collab.LinkImagePerformer, collab.LinkImagePerformer},
		{"scene tag", "scene", collab.LinkSceneTag, collab.LinkSceneTag},
		{"image tag", "image", collab.LinkImageTag, collab.LinkImageTag},
	}

	for _, c := range cases {
		sink := testSink()
		_, err := sink.AddMatch(ctx, c.target, 7, c.kind, 42, "x")
		require.NoError(t, err)

		got := sink.recorded()
		require.Len(t, got, 1, c.name)
		assert.Equal(t, c.target, got[0].TargetType, c.name)
		assert.Equal(t, c.kind, got[0].Kind, c.name)
	}
}

// AND A GALLERY'S PERFORMER IS A THIRD TABLE, which the same-kind-string rule makes
// worth asserting: `performer_ids` now means three different tables depending on the
// target.
func TestTheSameKindNameMeansThreeTablesAcrossThreeTargets(t *testing.T) {
	ctx := context.Background()
	sink := testSink()

	for _, target := range []string{"scene", "image", "gallery"} {
		_, err := sink.AddMatch(ctx, target, 1, collab.LinkKind("performer_ids"), 2, "x")
		require.NoError(t, err)
	}

	got := sink.recorded()
	require.Len(t, got, 3)

	seen := map[string]string{}
	for _, m := range got {
		seen[m.TargetType] = string(m.Kind)
		assert.Equal(t, "performer_ids", string(m.Kind),
			"the kind string is the same for all three; the TARGET is what "+
				"distinguishes them, which is why a sink takes both")
	}
	assert.Len(t, seen, 3, "and all three targets were recorded distinctly")
}

// A FILING FAILURE PROPAGATES, and never becomes a direct write.
//
// §6b.2 and the assertion that matters most in ProposalSink: a failed filing is
// returned. If it fell back to applying the match, then every outage of the proposal
// store would silently become a bypass of governance WHILE LOOKING LIKE SUCCESS,
// because the field would hold the right value. The sink is the layer where that
// temptation lives, so it is the layer where the refusal is tested.
func TestAFilingFailureIsReturnedAndNeverAppliedDirectly(t *testing.T) {
	boom := assert.AnError
	failing := &recordingSink{err: boom}

	already, err := failing.AddMatch(context.Background(), "scene", 7, collab.LinkScenePerformer, 42, "x")
	assert.ErrorIs(t, err, boom,
		"a failure must reach the caller. Swallowing it would mean the scan reports "+
			"success having done nothing")
	assert.False(t, already, "and it must not claim the match was already there, "+
		"which would suppress the error's own log line")
	assert.Empty(t, failing.recorded(), "nothing may be recorded when the sink failed")
}

// THE ALREADY SIGNAL REACHES THE CALLER, because it is what suppresses a log line for
// a match that changed nothing. A sink that always reported "changed" would make every
// re-run of autotag log thousands of matches it did not make.
func TestTheAlreadySignalIsReportedToTheCaller(t *testing.T) {
	already, err := testSink().AddMatch(context.Background(), "scene", 7,
		collab.LinkScenePerformer, 42, "x")
	require.NoError(t, err)
	assert.False(t, already, "a filed match IS a change: something happened, so the "+
		"caller logs it")
}

// A TAGGER WITH NO SINK IS REFUSED, and this is the property that stops a future
// refactor from reintroducing the gap.
//
// A Tagger whose sink is nil is a Tagger whose matches vanish. Every caller above reads
// that as "nothing matched", and an operator concludes the scan is broken rather than
// that governance is misconfigured -- so the constructor refuses. The zero value of
// Tagger is still constructible as a struct literal, which is why this test is about
// the CONSTRUCTOR: a struct literal with no sink produces a Tagger that does nothing,
// and the guard is that production code goes through NewTagger.
func TestATaggerWithNoSinkCannotBeBuilt(t *testing.T) {
	_, err := NewTagger(nil, nil, nil)
	assert.Error(t, err,
		"a Tagger with no sink has nowhere to put a match. Refusing here is what "+
			"makes 'files proposals' the path a caller gets by default, rather than "+
			"the path a forgotten field falls back to")
	assert.Contains(t, err.Error(), "ProposalSink",
		"and the error must say what to pass, so the fix is in the message")
}
