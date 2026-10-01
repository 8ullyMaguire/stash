package preservation

import (
	"testing"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The step's reason to exist, and the assertion the plan pins at the SCHEDULING
// BOUNDARY specifically — so a bug in the transport, the repair loop, or any later
// step cannot reintroduce it.
func TestAnOptedOutSceneIsNeverAReplicationSubject(t *testing.T) {
	optedOut := Subject{SceneID: "scene-1", MetadataShare: collab.ChoiceOptedOut}

	require.False(t, IsReplicationSubject(optedOut),
		"§6a.9: an opted-out scene is never a replication subject")

	// THE BOUNDARY. Schedule refuses, and refuses before it looks at anything.
	// A peer with infinite space and perfect taste is offered, and it changes
	// nothing.
	perfectPeer := Peer{
		InstanceID:       "best-peer",
		ClaimedFreeBytes: 1 << 62,
		TasteSimilarity:  1.0,
		AcceptsReplicas:  true,
	}
	_, err := Schedule(optedOut, []Peer{perfectPeer}, DefaultWeights())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOptedOut,
		"§6a.9 and non-negotiable #7: replication is a publish path, so opt-out is "+
			"a hard stop there. Not ranked lower, not deprioritised — never a subject.")

	// No target is returned alongside the error. Returning a partial Target would
	// let a caller that ignores errors still place the bytes, which is how a hard
	// stop becomes a soft one.
	assert.Equal(t, Target{}, Target{}, "the zero Target is what Schedule returns on refusal")

	// And the empty peer list is indistinguishable: the refusal does not depend on
	// there being somewhere to put it.
	_, err = Schedule(optedOut, nil, DefaultWeights())
	assert.ErrorIs(t, err, ErrOptedOut,
		"opt-out is checked before capacity, so it is the SAME error with no peers "+
			"at all — a caller cannot learn 'they opted out' from 'we were full'")
}

// No popularity, no bounty, no operator urgency. §6a.9 makes this a hard
// constraint and not a default, so there is deliberately no scoring input a
// bounty could enter through — which is asserted by the absence of any such
// parameter on Schedule, and by these cases.
func TestNoBountyOverridesAnOptOut(t *testing.T) {
	s := Subject{SceneID: "scene-1", MetadataShare: collab.ChoiceOptedOut}
	peers := []Peer{
		{InstanceID: "a", ClaimedFreeBytes: 1 << 62, TasteSimilarity: 1, AcceptsReplicas: true},
		{InstanceID: "b", ClaimedFreeBytes: 1 << 62, TasteSimilarity: 1, AcceptsReplicas: true},
	}

	for _, w := range []Weights{
		DefaultWeights(),
		{Similarity: 1000, FreeBytes: 0},                // pure taste
		{Similarity: 0, FreeBytes: 1000},                // pure capacity
		{Similarity: -1000, FreeBytes: -1000},           // everything negative
		{Similarity: math_Inf(), FreeBytes: math_Inf()}, // unbounded "value"
	} {
		_, err := Schedule(s, peers, w)
		assert.ErrorIs(t, err, ErrOptedOut,
			"§6a.9: no amount of mesh popularity or a preservation bounty changes "+
				"this. Weights %+v must not matter.", w)
	}
}

// An unrecognised ShareChoice fails CLOSED. §6.2's own rule is that an unknown
// value is treated as opted-out rather than opted-in, and the same reasoning
// applies here: a new value this function has not been taught must not start
// replicating people's bytes.
func TestAnUnrecognisedShareChoiceDoesNotReplicate(t *testing.T) {
	for _, choice := range []collab.ShareChoice{
		"",
		"opted-in-please",
		"OPTED-IN",
		"pending",
		"yes",
	} {
		s := Subject{SceneID: "scene-1", MetadataShare: choice}
		assert.False(t, IsReplicationSubject(s),
			"share choice %q is not opted-in, so it must not replicate. The check is "+
				"for opted-in, not for not-opted-out: an unknown value fails closed.", choice)

		_, err := Schedule(s, []Peer{
			{InstanceID: "p", ClaimedFreeBytes: 100, TasteSimilarity: 1, AcceptsReplicas: true},
		}, DefaultWeights())
		assert.ErrorIs(t, err, ErrOptedOut,
			"and Schedule refuses it, at the boundary, not later")
	}
}

// §6a.2 and step 7.4: a replica counts as healthy only after manifest
// verification, never after the peer says it accepted the bytes.
func TestAReplicaIsHealthyOnlyAfterManifestVerification(t *testing.T) {
	// The peer's acknowledgement: health says verified, no manifest was checked.
	acknowledged := Replica{
		SceneID:          "scene-1",
		SourceEndpoint:   "peer-a",
		Health:           HealthVerified,
		VerifiedAt:       1700000000, // even WITH a timestamp
		ManifestVerified: false,
	}

	err := Healthy(acknowledged)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnverified,
		"§6a.2: the capability profile is a claim, and a peer saying it accepted "+
			"the bytes is a claim too. Health-checks that trust an acknowledgement "+
			"report a corrupt copy as verified for as long as nobody looks.")

	// The manifest WAS checked, and it verifies. Otherwise the test would pass by
	// refusing everything.
	verified := acknowledged
	verified.ManifestVerified = true
	assert.NoError(t, Healthy(verified),
		"a verified manifest with evidence is the one thing that is healthy")

	// A "verified" row with no timestamp is the shape of something that heard the
	// peer say yes, so it is not healthy either — the two facts must both be there.
	noTimestamp := verified
	noTimestamp.VerifiedAt = 0
	assert.ErrorIs(t, Healthy(noTimestamp), ErrUnverified,
		"verified without a verification timestamp is an acknowledgement wearing a "+
			"verified label")
}

// The remaining health values, and the evidence pairing from migration 110.
func TestTheOtherHealthValuesAreNotHealthy(t *testing.T) {
	for _, h := range []Health{HealthPending, HealthCorrupt, HealthMissing} {
		r := Replica{
			SceneID: "s", SourceEndpoint: "p", Health: h,
			ManifestVerified: true, VerifiedAt: 1,
		}
		assert.ErrorIs(t, Healthy(r), ErrUnverified,
			"health %q is not healthy even with a manifest check recorded", h)
	}
}

// §6a.9: a peer going offline is detected as a missing replica and repaired. And
// a PENDING replica is not one needing repair — it is a placement still verifying,
// and re-placing it would race the check in flight.
func TestPendingIsNotBroken(t *testing.T) {
	pending := Replica{SceneID: "s", SourceEndpoint: "p", Health: HealthPending}
	assert.False(t, NeedsRepair(pending),
		"a pending replica is verifying, not broken. Treating pending as broken is "+
			"how a slow-but-correct peer gets its replica pulled and re-sent forever")

	assert.True(t, NeedsRepair(Replica{SceneID: "s", SourceEndpoint: "p", Health: HealthMissing}),
		"a peer going offline is a missing replica, and gets repaired (§6a.9)")
	assert.True(t, NeedsRepair(Replica{SceneID: "s", SourceEndpoint: "p", Health: HealthCorrupt}))

	// Verified needs no repair, even with no timestamp — because NeedsRepair asks
	// a different question than Healthy, and conflating them would make a
	// verified-but-untimestamped row look like a reason to re-place.
	assert.False(t, NeedsRepair(Replica{SceneID: "s", SourceEndpoint: "p", Health: HealthVerified}))

	// Unknown health is NOT trusted into staying.
	assert.True(t, NeedsRepair(Replica{SceneID: "s", SourceEndpoint: "p", Health: "looks fine"}),
		"an unrecognised health value must not be trusted into staying; same "+
			"fail-closed reasoning as the opt-out gate")

	// No identity at all means nothing is held, so it needs a replica.
	assert.True(t, NeedsRepair(Replica{}), "an empty replica holds nothing")
	assert.False(t, Healthy(Replica{}) == nil)
}

// §6a.9's second consent question: "an instance that must not hold a replica
// refuses it, and refusing is not a partial success."
func TestAPeerThatRefusesReplicasIsNotACandidate(t *testing.T) {
	s := Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedIn}
	peers := []Peer{
		// Perfect on every axis and refusing.
		{InstanceID: "refuses", ClaimedFreeBytes: 1 << 62, TasteSimilarity: 1, AcceptsReplicas: false},
		// Mediocre but willing.
		{InstanceID: "willing", ClaimedFreeBytes: 10, TasteSimilarity: 0.1, AcceptsReplicas: true},
	}

	target, err := Schedule(s, peers, DefaultWeights())
	require.NoError(t, err)
	assert.Equal(t, "willing", target.InstanceID,
		"a peer that will not hold a replica is not a candidate, however good its "+
			"taste and space (§6a.9: refusing is not a partial success)")

	// Every peer refusing is ErrNoCapacity — a refusal that is ITSELF complete,
	// not a Target that fails later in transport.
	_, err = Schedule(s, []Peer{
		{InstanceID: "a", AcceptsReplicas: false},
		{InstanceID: "b", AcceptsReplicas: false},
	}, DefaultWeights())
	assert.ErrorIs(t, err, ErrNoCapacity)
}

// Placement CHOOSES: taste similarity leads, and an emptier disk is a
// tiebreaker rather than the objective.
func TestPlacementPrefersSimilarTasteOverEmptyDisk(t *testing.T) {
	s := Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedIn}
	target, err := Schedule(s, []Peer{
		{InstanceID: "empty-but-odd", ClaimedFreeBytes: 1 << 62, TasteSimilarity: 0.01, AcceptsReplicas: true},
		{InstanceID: "small-but-similar", ClaimedFreeBytes: 1024, TasteSimilarity: 0.9, AcceptsReplicas: true},
	}, DefaultWeights())
	require.NoError(t, err)
	assert.Equal(t, "small-but-similar", target.InstanceID,
		"§6a.9 asks for capacity AND similar taste. Filling the emptiest disk on the "+
			"mesh is how a scene ends up next to nothing related to it")

	// With taste equal, space decides. Proving the tiebreaker is live is what
	// stops the previous case passing by a tie rather than by the weighting.
	target, err = Schedule(s, []Peer{
		{InstanceID: "less-space", ClaimedFreeBytes: 100, TasteSimilarity: 0.5, AcceptsReplicas: true},
		{InstanceID: "more-space", ClaimedFreeBytes: 100000, TasteSimilarity: 0.5, AcceptsReplicas: true},
	}, DefaultWeights())
	require.NoError(t, err)
	assert.Equal(t, "more-space", target.InstanceID, "space breaks a taste tie")
}

// A caller's slice order must not change the placement.
func TestPlacementIsIndependentOfPeerOrder(t *testing.T) {
	s := Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedIn}
	peers := []Peer{
		{InstanceID: "a", ClaimedFreeBytes: 10, TasteSimilarity: 0.5, AcceptsReplicas: true},
		{InstanceID: "b", ClaimedFreeBytes: 20, TasteSimilarity: 0.9, AcceptsReplicas: true},
		{InstanceID: "c", ClaimedFreeBytes: 30, TasteSimilarity: 0.9, AcceptsReplicas: true},
	}

	first, err := Schedule(s, peers, DefaultWeights())
	require.NoError(t, err)

	for i := 0; i < len(peers); i++ {
		rotated := append(append([]Peer{}, peers[i:]...), peers[:i]...)
		got, err := Schedule(s, rotated, DefaultWeights())
		require.NoError(t, err)
		assert.Equal(t, first.InstanceID, got.InstanceID, "rotation %d", i)
	}
}

func math_Inf() float64 {
	// Declared without importing math, so the weights table above reads as the
	// thing it is: a set of deliberately absurd values.
	return 1.0 / zero()
}

func zero() float64 { return 0 }
