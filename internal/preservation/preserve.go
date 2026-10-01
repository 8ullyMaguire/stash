// Package preservation schedules and protects the replicas a scene's bytes live on.
//
// M7 step 7.4 (R018–R024, R072, R063), spec §6a.9 — the brief's central new
// mechanism.
//
// TWO HARD CONSTRAINTS, AND BOTH ARE ENFORCED WHERE A LATER STEP COULD NOT
// REINTRODUCE THEM:
//
//  1. OPT-OUT IS A HARD STOP AT THE SCHEDULING BOUNDARY (non-negotiable #7). A
//     user's opted-out scene is never a replication subject. Not ranked lower, not
//     deprioritised, not subject to a bounty: never a subject. The check is in
//     Schedule, which is the only door into the system — so a bug in the
//     transport, in the repair loop, or in a future step cannot route around it.
//
//  2. A REPLICA IS HEALTHY ONLY AFTER MANIFEST VERIFICATION (§6a.2). Never after
//     the peer says it accepted the bytes. A peer's acknowledgement is a claim
//     from a stranger, in the same posture §7's transfer layer takes toward a
//     peer-supplied name, and health-checks that trust an acknowledgement will
//     happily report a corrupt copy as verified for as long as nobody looks.
package preservation

import (
	"errors"
	"fmt"
	"sort"

	"github.com/stashapp/stash/internal/collab"
)

// Default is the default of three — the spec's headline, and a DEFAULT rather
// than a floor. An operator may lower it; the system will not silently do so.
const Default = 3

// Subject is a candidate for replication.
type Subject struct {
	SceneID string

	// MetadataShare is the owner's §6.2 answer, and it is the field that decides
	// whether this subject may be scheduled at all.
	//
	// It is REQUIRED rather than defaulted. A zero ShareChoice is not a valid
	// answer, and treating "unset" as "opted-in" would make the opt-out gate a
	// gate that opens on a missing value — which is the failure this package
	// exists to prevent, arrived at by a different route.
	MetadataShare collab.ShareChoice
}

// Errors, kept separate because each names a DIFFERENT PARTY's problem and the
// caller acts on them differently.
var (
	// ErrOptedOut means non-negotiable #7. Permanent: no amount of popularity,
	// bounty or operator urgency changes it, and there is deliberately no
	// "force" variant.
	ErrOptedOut = errors.New("preservation: subject is opted out and can never be a replication subject")

	// ErrUnverified means a replica reported healthy before its manifest was
	// verified. Never returned by Schedule — which is correct, since Schedule only
	// creates subjects. It exists for the health path, and having a distinct
	// sentinel is what lets a test prove the two paths are not confused.
	ErrUnverified = errors.New("preservation: replica is not verified")

	// ErrNoCapacity means every candidate peer is full or has no matching taste.
	ErrNoCapacity = errors.New("preservation: no eligible peer")
)

// IsReplicationSubject is the opt-out gate, and it is deliberately a SEPARATE
// exported function rather than a private check inside Schedule.
//
// Two reasons, both about the future:
//
//   - Step 7.4's repair loop and any later step must ask the same question in the
//     same way. A private predicate could be reimplemented with the sign flipped
//     and nothing would catch it.
//   - A caller can assert the INVARIANT without scheduling anything, which is
//     what the test at the scheduling boundary does.
//
// The answer is false for an unrecognised ShareChoice. §6.2's own rule is that an
// unrecognised value is treated as opted-out rather than opted-in — "a schema bug
// stop publishing" is the worse of the two failures — and the same reasoning
// applies here: a new value that has not been taught to this function must not
// start replicating people's bytes.
func IsReplicationSubject(s Subject) bool {
	if s.SceneID == "" {
		return false
	}
	// NOT `== collab.ChoiceOptedIn`. An unrecognised value fails closed.
	return s.MetadataShare == collab.ChoiceOptedIn
}

// Peer is a candidate host.
type Peer struct {
	InstanceID string

	// ClaimedFreeBytes is what the PEER says it has free. §6a.2 makes this a
	// claim, so it must never be sized from — see internal/mesh's claim guard.
	// Schedule treats it as a hint for ordering only, never as an authority.
	ClaimedFreeBytes uint64

	// TasteSimilarity is how close this peer's taste is to the local instance
	// (§6a.2), which is what makes it a sensible host rather than merely the
	// emptiest disk on the mesh.
	TasteSimilarity float64

	// AcceptsReplicas is the RECEIVING instance's own answer to §6a.9's second
	// consent question: "an instance that must not hold a replica refuses it, and
	// refusing is not a partial success."
	AcceptsReplicas bool
}

// Target is a placement decision: this subject, on this peer.
type Target struct {
	SceneID    string
	InstanceID string
	// Score is the ordering key, exposed so a test can assert placement CHOOSES
	// rather than merely accepts.
	Score float64
}

// Schedule places a replica for a subject on a peer with capacity and similar
// taste (§6a.9).
//
// IT REFUSES FIRST. The opt-out gate is the first statement, before capacity is
// examined, before taste is scored, before any peer is consulted. That order is
// the point: a refusal that happened to be computed alongside a capacity check
// could be reordered by a later edit, whereas a check that is the first thing the
// function does cannot be skipped by anything appended below it.
func Schedule(s Subject, peers []Peer, w Weights) (Target, error) {
	if !IsReplicationSubject(s) {
		return Target{}, fmt.Errorf("%w: scene %q", ErrOptedOut, s.SceneID)
	}

	// A peer that will not hold a replica is not a candidate. Filtering first
	// means a peer with the best taste and the most space is simply absent, rather
	// than being chosen and then failing in the transport — where §6a.9 requires
	// the refusal to be a refusal, not a partial success.
	eligible := make([]Peer, 0, len(peers))
	for _, p := range peers {
		if p.InstanceID != "" && p.AcceptsReplicas {
			eligible = append(eligible, p)
		}
	}
	if len(eligible) == 0 {
		return Target{}, fmt.Errorf("%w: every peer refuses replicas or is unnamed", ErrNoCapacity)
	}

	// Deterministic: sort on instance ID before scoring, so a tie in Score cannot
	// depend on the caller's slice order.
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].InstanceID < eligible[j].InstanceID })

	best := eligible[0]
	bestScore := score(best, eligible, w)
	for _, p := range eligible[1:] {
		if sc := score(p, eligible, w); sc > bestScore {
			best, bestScore = p, sc
		}
	}

	return Target{
		SceneID:    s.SceneID,
		InstanceID: best.InstanceID,
		Score:      bestScore,
	}, nil
}

// Weights order candidate hosts. Taste similarity leads: §6a.9 asks for capacity
// AND similar taste, and the taste is what makes the placement defensible to the
// scene's owner. Space is a tiebreaker, not the objective — filling the emptiest
// disk on the mesh is how a scene ends up next to nothing related to it.
type Weights struct {
	Similarity float64
	FreeBytes  float64
}

// DefaultWeights favours taste, as above.
func DefaultWeights() Weights {
	return Weights{Similarity: 1.0, FreeBytes: 0.1}
}

// score weights a peer against the whole candidate set.
//
// FREE BYTES ARE NORMALISED BY THE LARGEST CLAIM IN THE SET, and that is not
// cosmetic. The first version multiplied taste by ~1.0 and bytes by 0.01, which
// sounds like a tiebreaker and is not one: free bytes run to the terabyte, so
// 0.01 * 2^60 dwarfs 1.0 * 0.9 by sixteen orders of magnitude. Measured:
//
//	similar-but-small (0.9 sim, 1 KiB)  -> 11.14
//	empty-but-odd     (0.01 sim, 4 EiB) -> 4.6e16
//
// So "place where the taste matches" was unreachable in practice, and the test
// asserting it failed — which is the whole reason the assertion is there rather
// than a comment. A weight cannot make a term subordinate; only putting the terms
// in the same units can.
//
// Normalising makes free bytes a RATIO in [0,1], so the two weights are actually
// comparable and 0.1 means "a tenth as important as taste", as written.
func score(p Peer, all []Peer, w Weights) float64 {
	space := 0.0
	if max := maxClaimedFree(all); max > 0 {
		space = float64(p.ClaimedFreeBytes) / float64(max)
	}
	return w.Similarity*p.TasteSimilarity + w.FreeBytes*space
}

func maxClaimedFree(peers []Peer) uint64 {
	var max uint64
	for _, p := range peers {
		if p.ClaimedFreeBytes > max {
			max = p.ClaimedFreeBytes
		}
	}
	return max
}

// Health is the four-value domain from migration 110. There is no failure COUNT
// anywhere in this package, and there should never be one: a stored tally drifts
// from the thing it summarises, and it is also the thing an attacker increments
// without the event happening.
type Health string

const (
	HealthPending  Health = "pending"
	HealthVerified Health = "verified"
	HealthCorrupt  Health = "corrupt"
	HealthMissing  Health = "missing"
)

// Replica is a copy this instance holds.
type Replica struct {
	SceneID        string
	SourceEndpoint string
	Health         Health

	// VerifiedAt is the EVIDENCE for HealthVerified. Migration 110 refuses a row
	// claiming verified with no timestamp, or carrying a timestamp alongside
	// corrupt, so the two facts cannot contradict each other in storage.
	VerifiedAt int64

	// ManifestVerified records that the bytes were checked against
	// manifest_hash. Distinct from "the peer said it accepted them", which is a
	// peer's claim and proves nothing (§6a.2).
	ManifestVerified bool
}

// ErrNoReplica is returned when a replica is absent from this instance.
var ErrNoReplica = errors.New("preservation: no replica held")

// Healthy reports whether a replica counts as healthy.
//
// §6a.2 and step 7.4's rule, in one place: healthy means VERIFIED, and verified
// means the manifest was checked. A peer's acknowledgement is not verification, so
// it does not appear in this function's inputs at all — there is no field for it,
// which is the structural half of the rule.
//
// VerifiedAt is required as well as ManifestVerified, because a "verified" health
// with no timestamp is the shape of a row written by something that heard the peer
// say yes.
func Healthy(r Replica) error {
	if r.SceneID == "" || r.SourceEndpoint == "" {
		return fmt.Errorf("%w: replica has no identity", ErrNoReplica)
	}
	if r.Health != HealthVerified {
		return fmt.Errorf("%w: health is %q", ErrUnverified, r.Health)
	}
	if !r.ManifestVerified {
		return fmt.Errorf("%w: health claims verified but the manifest was never checked", ErrUnverified)
	}
	if r.VerifiedAt == 0 {
		return fmt.Errorf("%w: health claims verified with no verification timestamp", ErrUnverified)
	}
	return nil
}

// NeedsRepair reports whether a replica should be replaced, and it is the
// counterpart of Healthy: §6a.9 requires that "a peer going offline is detected as
// a missing replica and repaired".
//
// NOTE WHAT IS NOT HERE. A pending replica is not "needing repair" — it is a
// placement that has not verified yet, and re-placing it would race the
// verification in flight. Treating pending as broken is how a slow-but-correct
// peer gets its replica pulled and re-sent forever.
func NeedsRepair(r Replica) bool {
	if r.SceneID == "" || r.SourceEndpoint == "" {
		return true
	}
	switch r.Health {
	case HealthMissing, HealthCorrupt:
		return true
	case HealthPending:
		// Wait for verification rather than re-placing.
		return false
	case HealthVerified:
		return false
	}
	// An unrecognised health value is treated as needing repair. Same reasoning
	// as the opt-out gate: an unknown state must not be trusted into staying.
	return true
}
