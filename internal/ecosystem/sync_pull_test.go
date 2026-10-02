package ecosystem

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R057 — "Stash app two-way sync" — and §6a.19, "Stash app integration".
//
// The WRITE direction is already built and tested (Syncer.Push files a proposal and does not
// write the field). This covers the READ direction: pulling metadata FROM a peer, which is the
// half a Stash app client actually needs to populate a library before it can push anything back.
//
// WHY A TRANSPORT INTERFACE RATHER THAN AN HTTP CALL
//
// `pkg/stashbox.Client` is bound to stash-box's generated GraphQL types, so reusing it here
// would couple the peer protocol to a schema this fork does not control — and R057's whole point
// is that a StashForge instance is a DIFFERENT peer from a stash-box instance, speaking the
// same client surface. A narrow interface makes the seam explicit and lets a pull be tested
// without a network, which is what makes the interesting behaviour (consent, bounds, identity)
// testable at all.
//
// THE SHAPE OF THE REQUIREMENT, restated so the tests can check it:
//
//   - a pull is CONSENT-BOUNDED: a peer must not be able to make this instance reveal an
//     entity whose owner opted out. Direction matters here and is easy to invert: a peer's
//     answer is INPUT, never authority to publish.
//   - a pull is BOUNDED: one page, capped, and a limit the caller asked for beyond the cap is
//     clamped rather than honoured.
//   - a pull is IDENTITY-PRESERVING: §6a.6's (instance, local) pair. A pulled entity that
//     arrives with only a local id is indistinguishable from a local entity, and two peers both
//     using id 42 would collide.

// fakePeer is a scripted peer. Every field is a recording, so a test can assert not only what
// came back but what was asked for -- which is where a consent or bounds bug hides.
type fakePeer struct {
	// entities is what the peer returns, per query.
	got []PublicEntity
	// err is returned if set, before anything else.
	err error

	// recorded
	called   int
	lastQ    PublicQuery
	lastFrom string
}

func (f *fakePeer) Pull(ctx context.Context, from string, q PublicQuery) ([]PublicEntity, error) {
	f.called++
	f.lastQ = q
	f.lastFrom = from
	if f.err != nil {
		return nil, f.err
	}
	// A real peer would apply the bound itself; this one returns whatever it was scripted to,
	// so a test that asks for more than the cap proves the CALLER clamps rather than trusting
	// the peer.
	return f.got, nil
}

// THE POSITIVE CONTROL: without this, a suite in which every pull is refused would pass every
// refusal test while the feature did nothing.
func TestAPullReturnsWhatThePeerSent(t *testing.T) {
	peer := &fakePeer{got: []PublicEntity{
		{PublicID: "42", Title: "A Scene", Published: true},
		{PublicID: "43", Title: "Another", Published: true},
	}}
	syncer := NewSyncer(nil).SetPeer(peer)

	got, err := syncer.Pull(context.Background(), "peer-a", PublicQuery{Limit: 10},
		PullOptions{Share: "opted-in"})
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Equal(t, "A Scene", got[0].Title)
	assert.Equal(t, 1, peer.called)
}

// §6a.6: across a node boundary an id is an (instance, local) PAIR. A pulled entity that keeps
// only its local id is indistinguishable from a local entity, and two peers both using 42 would
// collide -- so the origin has to travel with it.
func TestAPulledEntityCarriesItsOriginInstance(t *testing.T) {
	peer := &fakePeer{got: []PublicEntity{{PublicID: "42", Title: "A Scene", Published: true}}}
	syncer := NewSyncer(nil).SetPeer(peer)

	got, err := syncer.Pull(context.Background(), "peer-a", PublicQuery{Limit: 10},
		PullOptions{Share: "opted-in"})
	require.NoError(t, err)
	require.Len(t, got, 1)

	assert.Equal(t, "peer-a", got[0].Origin,
		"a pulled entity must say which instance it came from; two peers using id 42 must not "+
			"be the same entity, and a bare local id cannot express that")
	assert.Equal(t, "peer-a:42", got[0].QualifiedID(),
		"the qualified id is what a caller stores, and it must be stable and unambiguous")
}

// THE DIRECTION OF CONSENT, which is the trap. A peer's answer is INPUT.
//
// A peer saying "entity 42 is published" must not make this instance publish it. The peer is a
// different instance with a different owner and a different consent decision; §6a.21's rule is
// per-instance, and treating a peer's claim as authority would let any peer publish this
// instance's library by answering a query.
func TestAPeersPublicationClaimIsNotAuthorityOverThisInstance(t *testing.T) {
	// The peer claims an entity is published, and the puller applies the LOCAL consent state --
	// which here is opted out.
	peer := &fakePeer{got: []PublicEntity{{PublicID: "42", Title: "Secret", Published: true}}}
	syncer := NewSyncer(nil).SetPeer(peer)

	got, err := syncer.Pull(context.Background(), "peer-a", PublicQuery{Limit: 10},
		PullOptions{Share: "opted-out"})
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.False(t, got[0].Published,
		"a peer's Published flag must not survive the local consent check; consent is "+
			"per-instance and a peer cannot publish this one")
	// The metadata itself is still useful -- it is the PUBLICATION that is refused, not the
	// read. Dropping the row entirely would make a peer unusable for discovery.
	assert.Equal(t, "Secret", got[0].Title,
		"the entity's metadata may still be read; only its published state is local")
}

// A pull is bounded. A library with 100k entities must not be enumerable in one request, and
// the bound must be applied by the CALLER -- a peer that ignores the cap must not be able to
// widen it.
func TestAPullIsBoundedAndThePeerCannotWidenTheBound(t *testing.T) {
	// The peer is scripted to return more than the cap allows, ignoring what it was asked.
	tooMany := make([]PublicEntity, MaxPublicLimit+50)
	for i := range tooMany {
		tooMany[i] = PublicEntity{PublicID: string(rune('a' + i%26)), Title: "x", Published: true}
	}
	peer := &fakePeer{got: tooMany}
	syncer := NewSyncer(nil).SetPeer(peer)

	got, err := syncer.Pull(context.Background(), "peer-a", PublicQuery{Limit: 10_000},
		PullOptions{Share: "opted-in"})
	require.NoError(t, err)

	assert.LessOrEqual(t, len(got), MaxPublicLimit,
		"a caller asking for 10000 must be clamped to the cap, and a peer returning more must "+
			"not be able to push past it")
	assert.Equal(t, MaxPublicLimit, peer.lastQ.Limit,
		"the bound is sent to the peer too, so a well-behaved peer never pages more than the cap")
}

// An offset beyond the cap, or negative, is a paging bug rather than a query; the bound
// normalises both so a caller cannot produce an unbounded scan by paging.
func TestAPullNormalisesOffsetAndLimit(t *testing.T) {
	peer := &fakePeer{got: []PublicEntity{}}
	syncer := NewSyncer(nil).SetPeer(peer)

	_, err := syncer.Pull(context.Background(), "peer-a",
		PublicQuery{Limit: -5, Offset: -10}, PullOptions{Share: "opted-in"})
	require.NoError(t, err)

	assert.Equal(t, MaxPublicLimit, peer.lastQ.Limit, "a non-positive limit becomes the cap")
	assert.Equal(t, 0, peer.lastQ.Offset, "a negative offset becomes zero, not a scan backwards")
}

// An empty peer name is refused. An unqualified pull would produce entities whose origin is
// blank, which is the collision case above with the origin made invisible.
func TestAPullWithoutAPeerNameIsRefused(t *testing.T) {
	peer := &fakePeer{}
	syncer := NewSyncer(nil).SetPeer(peer)

	_, err := syncer.Pull(context.Background(), "", PublicQuery{}, PullOptions{Share: "opted-in"})
	require.Error(t, err)
	assert.Zero(t, peer.called, "the peer must not be contacted at all when the pull is invalid")
	assert.Contains(t, err.Error(), "peer",
		"the error must name what is missing, or an operator cannot tell which field to set")
}

// A peer failure is returned as-is rather than swallowed. A sync that reports success having
// pulled nothing is worse than one that fails, because it looks like there was nothing to pull.
func TestAPeerErrorIsSurfacedNotSwallowed(t *testing.T) {
	sentinel := errors.New("peer unreachable")
	peer := &fakePeer{err: sentinel}
	syncer := NewSyncer(nil).SetPeer(peer)

	_, err := syncer.Pull(context.Background(), "peer-a", PublicQuery{}, PullOptions{Share: "opted-in"})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel,
		"the peer error must survive so a caller can distinguish 'nothing there' from 'could not ask'")
}

// Pushing still requires a board. A pull needs no board -- it writes nothing -- so a pull must
// work on a syncer with no board wired. This is the control that keeps Pull from accidentally
// acquiring a dependency on the write path.
func TestAPullDoesNotRequireTheBoard(t *testing.T) {
	peer := &fakePeer{got: []PublicEntity{{PublicID: "42", Title: "A Scene", Published: true}}}
	syncer := NewSyncer(nil).SetPeer(peer) // deliberately no board

	got, err := syncer.Pull(context.Background(), "peer-a", PublicQuery{}, PullOptions{Share: "opted-in"})
	require.NoError(t, err, "a pull writes nothing, so it must not need the proposal path")
	assert.Len(t, got, 1)

	// And the write direction still refuses without a board, so the two are not conflated.
	_, err = syncer.Push(context.Background(), IncomingEdit{
		TargetType: "scene", TargetID: 1, Field: "title", Origin: "stash-app", AuthorID: 1,
	})
	assert.Error(t, err, "Push must still refuse with no board; a shared dependency would be a bug")
}

// An UNRECOGNISED consent value must be treated as opted OUT.
//
// This is the same rule Publishable already follows for the replication predicate: only an
// EXPLICIT opt-in publishes, so an unrecognised string cannot widen the surface. Getting it
// backwards -- "anything that is not opted-out publishes" -- means a typo, a truncated value
// off the wire, or a future rename silently starts publishing this instance's library. A
// mutation inverting the comparison survived because every test passed a value this build
// recognises.
func TestAnUnrecognisedConsentValueDoesNotPublish(t *testing.T) {
	for _, share := range []string{
		"",          // the zero value, which is what a caller omitting the field sends
		"opted-in ", // a trailing space from a query string
		"OPTED-IN",  // a case difference
		"opted in",  // the human-readable spelling
		"yes",       // another way of saying it
		"opted-out", // the real refusal, for completeness
		"public",    // a word from a different vocabulary
	} {
		t.Run("share="+share, func(t *testing.T) {
			peer := &fakePeer{got: []PublicEntity{{PublicID: "42", Title: "T", Published: true}}}
			syncer := NewSyncer(nil).SetPeer(peer)

			got, err := syncer.Pull(context.Background(), "peer-a", PublicQuery{},
				PullOptions{Share: share})
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.False(t, got[0].Published,
				"only the exact string %q publishes; %q must not", "opted-in", share)
		})
	}
}

// A missing transport must be an ERROR, not an empty result.
//
// The distinction is the whole reason the test exists: an empty result is indistinguishable from
// "this peer has nothing", and a caller that treats it as success will mark this peer as synced
// and never ask again. SetPeer deliberately refuses to store nil for the same reason -- a syncer
// that looks wired but is not is worse than one that visibly has no transport.
func TestAMissingTransportIsAnErrorNotAnEmptyResult(t *testing.T) {
	syncer := NewSyncer(nil) // SetPeer never called

	_, err := syncer.Pull(context.Background(), "peer-a", PublicQuery{},
		PullOptions{Share: "opted-in"})
	require.Error(t, err,
		"a syncer with no transport must report that, not return an empty slice which reads as "+
			"'the peer had nothing' and gets cached as a successful sync")
	assert.Contains(t, err.Error(), "peer-a", "the error must name the peer, or a multi-peer "+
		"deployment cannot tell which transport is missing")
}

// SetPeer(nil) must not clear a working transport, so a caller that passes a nil peer by
// mistake cannot silently disable the read direction.
func TestSetPeerIgnoresNil(t *testing.T) {
	peer := &fakePeer{got: []PublicEntity{{PublicID: "42", Title: "T", Published: true}}}
	syncer := NewSyncer(nil).SetPeer(peer).SetPeer(nil)

	got, err := syncer.Pull(context.Background(), "peer-a", PublicQuery{},
		PullOptions{Share: "opted-in"})
	require.NoError(t, err, "SetPeer(nil) must not unwire a working transport")
	assert.Len(t, got, 1)
}

// QualifiedID must be unambiguous for the ambiguous case: two peers using the same local id.
func TestTwoPeersUsingTheSameLocalIDStayDistinct(t *testing.T) {
	a := &fakePeer{got: []PublicEntity{{PublicID: "42", Title: "From A", Published: true}}}
	sa := NewSyncer(nil).SetPeer(a)
	b := &fakePeer{got: []PublicEntity{{PublicID: "42", Title: "From B", Published: true}}}
	sb := NewSyncer(nil).SetPeer(b)

	fromA, err := sa.Pull(context.Background(), "peer-a", PublicQuery{}, PullOptions{Share: "opted-in"})
	require.NoError(t, err)
	fromB, err := sb.Pull(context.Background(), "peer-b", PublicQuery{}, PullOptions{Share: "opted-in"})
	require.NoError(t, err)

	require.Len(t, fromA, 1)
	require.Len(t, fromB, 1)
	assert.NotEqual(t, fromA[0].QualifiedID(), fromB[0].QualifiedID(),
		"two peers both using local id 42 must not collapse to one identifier, or one peer's "+
			"entity overwrites the other's on the way in")
	assert.Equal(t, "From A", fromA[0].Title)
	assert.Equal(t, "From B", fromB[0].Title)
}

// PullFrom with an explicit nil peer must error rather than return an empty result.
//
// The nil check inside `pull` is unreachable through Syncer.Pull, which checks its own field
// first -- so a mutation removing the inner guard survived the whole suite. This exercises the
// standalone entry point, where it is the only thing standing between a nil transport and an
// empty result that reads as "the peer had nothing".
func TestPullFromWithANilPeerIsAnError(t *testing.T) {
	_, err := PullFrom(context.Background(), nil, "peer-a", PublicQuery{},
		PullOptions{Share: "opted-in"})
	require.Error(t, err,
		"PullFrom is reachable with a caller-supplied transport, so its own nil check has to "+
			"hold; without it a nil peer becomes an empty slice and a cached 'nothing to sync'")
	assert.Contains(t, err.Error(), "peer-a", "the error must name the peer that has no transport")
}

// The positive control for the same entry point, so the nil test above cannot pass because
// PullFrom errors for every input.
func TestPullFromReturnsResultsWithAWorkingPeer(t *testing.T) {
	peer := &fakePeer{got: []PublicEntity{{PublicID: "42", Title: "T", Published: true}}}

	got, err := PullFrom(context.Background(), peer, "peer-a", PublicQuery{},
		PullOptions{Share: "opted-in"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "peer-a:42", got[0].QualifiedID())
}
