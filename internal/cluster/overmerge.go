package cluster

import (
	"errors"
	"fmt"
	"sort"
)

// Step 2.4b.2: the over-merge guard. Commons §7.1 step 4.
//
// # The problem, in one paragraph
//
// Pairwise similarity is not transitive. A is within T of B and B is within T
// of C tells you nothing about A and C. Greedy clustering that puts each face
// in its nearest neighbour's cluster therefore merges A and C on B's
// authority, and the resulting cluster is one hop further from every pair
// inside it than any single decision justified. It gets worse in a specific
// way: the first merge is the most confident one, so the wrong anchor is
// exactly the one a casual human review would have approved.
//
// # The guard
//
// A face is measured against the cluster's CENTROID, not against any member.
// And a member is never added that would push the centroid far enough to
// strand a face that was previously inside the threshold -- because that face
// was accepted under the old centroid and would silently become an outlier,
// which is how a cluster erodes from the inside.
//
// Splitting is deliberately NOT gated by the same threshold. A guard that can
// only refuse to grow is a one-way ratchet: correcting a bad merge would be
// harder than making one, and people merge first and split later.

// ErrOverMerge is returned when a candidate is refused because joining it would
// exceed the cluster's diameter, not merely because it is far from the centre.
//
// The two failures are distinguished because they mean opposite things and the
// remedy is opposite. Too far from the centre is an ordinary miss: the face
// belongs to another person. Beyond the diameter is evidence the CLUSTER is
// already two people, and the remedy is a split, not a retry.
var ErrOverMerge = errors.New("join would exceed the cluster's diameter")

// ErrTooFarFromCentre is an ordinary assignment miss.
var ErrTooFarFromCentre = errors.New("candidate is outside the threshold of the cluster's centroid")

// errFaceAlreadyClaimed means this exact face is already a member of another
// cluster -- a corrupt or duplicated index, not a distance problem.
//
// It is a distinct sentinel rather than a formatted error because the remedy is
// the opposite. ErrTooFarFromCentre means the face belongs to someone else and
// should be looked for elsewhere. This means two clusters are claiming one
// face, and the fix is to repair the index. Conflating them means a user
// correcting one gets told to search harder, which is exactly wrong.
var errFaceAlreadyClaimed = errors.New("face is already a member of another cluster")

// testFace is the fixture. A 1-D position is enough to exercise the guard and
// keeps the arithmetic checkable by hand, which matters because the interesting
// cases are the ones where a human can see the two rules disagree.
type testFace struct {
	pos float64
	// key is the membership identity: target + frame + crop. Two faces with
	// the same key are the same appearance seen twice.
	key string
}

// distanceTo is |a - b| on the line. A real embedding distance is a cosine
// distance; the guard only ever asks "is this larger than T", so a monotone
// stand-in is sufficient and the tests stay arithmetic.
func (f *testFace) distanceTo(other *testFace) float64 {
	d := f.pos - other.pos
	if d < 0 {
		return -d
	}
	return d
}

// distanceToCentroid is the face-side spelling, because that is how a caller
// reads it: "how far is this face from the cluster's centre".
func (f *testFace) distanceToCentroid(c centroid) float64 {
	return c.distanceTo(f)
}

// centroid is the mean position, which for a 1-D fixture is the same arithmetic
// as a mean embedding.
type centroid float64

func (c centroid) distanceTo(f *testFace) float64 {
	d := f.pos - float64(c)
	if d < 0 {
		return -d
	}
	return d
}

// guard holds the clustering state under test.
type guard struct {
	threshold float64
	// clusters maps a cluster id to its members. A map keyed by id is not
	// needed for correctness but keeps the split test readable.
	clusters map[int][]*testFace
	// keys maps a membership key to the cluster that already contains it, so a
	// rescan is a no-op rather than a duplicate.
	keys map[string]int
	next int
}

func newGuard(threshold float64) *guard {
	return &guard{
		threshold: threshold,
		clusters:  map[int][]*testFace{},
		keys:      map[string]int{},
	}
}

func (g *guard) newCluster() int {
	g.next++
	g.clusters[g.next] = nil
	return g.next
}

func (g *guard) size(id int) int { return len(g.clusters[id]) }

// forceCluster plants a cluster without running the guard.
//
// This exists because the guard makes its own precondition untestable through
// the guarded path: a cluster of two halves cannot be built by adding members,
// since the second half is refused. So "what happens if a cluster IS two
// people" can only be asked of state that arrived some other way -- imported
// from an older corpus, or produced by a threshold change after the fact.
//
// Naming that explicitly is better than a back door. An unexported helper that
// skips the guard is easy to misuse; one whose comment says "this is how you
// test the guard's own precondition" is at least honest about what it is.
func (g *guard) forceCluster(members []*testFace) int {
	g.next++
	g.clusters[g.next] = members
	for _, m := range members {
		if m.key != "" {
			g.keys[m.key] = g.next
		}
	}
	return g.next
}

func (g *guard) centroidOf(id int) centroid {
	members := g.clusters[id]
	if len(members) == 0 {
		return 0
	}
	var sum float64
	for _, m := range members {
		sum += m.pos
	}
	return centroid(sum / float64(len(members)))
}

// addMember admits a face, or explains why not.
//
// Two checks, in order. The first is the one the spec names: distance to the
// centroid against the threshold. The second is the one that makes the first
// hold over time -- adding a far face drags the centroid toward itself, which
// can strand a member that was inside the threshold a moment ago.
func (g *guard) addMember(id int, f *testFace) error {
	// A rescan of an existing member. The store's UNIQUE key turns this into an
	// UPDATE, and the guard must agree: counting it twice would compute the
	// centroid over a multiset and make every rescan loosen the clustering.
	if owner, ok := g.keys[f.key]; ok && f.key != "" {
		if owner == id {
			return nil
		}
		return fmt.Errorf("%w: face %q is in cluster %d", errFaceAlreadyClaimed, f.key, owner)
	}

	members := g.clusters[id]

	// An empty cluster accepts anything: the first face has no one to be
	// confused with, and requiring a second would mean no cluster ever forms.
	if len(members) == 0 {
		g.clusters[id] = append(members, f)
		if f.key != "" {
			g.keys[f.key] = id
		}
		return nil
	}

	old := g.centroidOf(id)
	if d := old.distanceTo(f); d > g.threshold {
		return fmt.Errorf("%w: %.3f from centroid, threshold %.2f",
			ErrTooFarFromCentre, d, g.threshold)
	}

	// Would adding this face strand an existing member?
	//
	// The new centroid is the mean including the candidate. Every member must
	// still be within the threshold of it. This is the check that makes the
	// guard a property of the CLUSTER rather than of the candidate: without
	// it, a cluster can pass every individual admission and still end up with
	// a face 3T from the middle, accepted at a time when it was 0.1T.
	combined := append(append([]*testFace{}, members...), f)
	newC := meanOf(combined)

	var stranded *testFace
	var worst float64
	for _, m := range combined {
		if d := newC.distanceTo(m); d > g.threshold {
			if d > worst {
				worst, stranded = d, m
			}
		}
	}
	if stranded != nil {
		return fmt.Errorf("%w: adding %+v would leave member %+v %.3f from the "+
			"new centroid, threshold %.2f; the cluster is already two people "+
			"and needs a split rather than a bigger cluster",
			ErrOverMerge, f, stranded, worst, g.threshold)
	}

	g.clusters[id] = combined
	if f.key != "" {
		g.keys[f.key] = id
	}
	return nil
}

func meanOf(faces []*testFace) centroid {
	if len(faces) == 0 {
		return 0
	}
	var sum float64
	for _, f := range faces {
		sum += f.pos
	}
	return centroid(sum / float64(len(faces)))
}

// split separates the faces at the given key from their cluster and returns the
// resulting clusters.
//
// Intentionally not threshold-gated. The whole point of the guard is that it
// refuses a bad join; if the matching refusal also blocked the correction, a
// user who agrees a cluster is wrong would have to delete every face in it.
//
// The cut is at the widest gap rather than at a fixed point, because the widest
// gap is where "these are two groups" is most likely to be true.
func (g *guard) split(id int, f *testFace) []int {
	members := g.clusters[id]

	var keep, moved []*testFace
	for _, m := range members {
		if m.key == f.key {
			moved = append(moved, m)
		} else {
			keep = append(keep, m)
		}
	}
	if len(moved) == 0 {
		return []int{id}
	}

	// If this removal is itself a bisection -- the moved face is far from the
	// rest -- it leaves one cluster, not two. Splitting must report the actual
	// result rather than the number of arguments that looked like a split.
	if len(keep) == 0 {
		g.clusters[id] = moved
		return []int{id}
	}

	// A single face pulled out of a cluster is a split only when it is not
	// adjacent to the remainder: pulling out a face that sits in the middle
	// leaves a hole, not two people.
	//
	// Note the comparison, and that the first version compared against
	// g.threshold. It must be 0. A face at distance zero from the remainder IS
	// the same face -- there is no judgement to make -- but any positive
	// distance is a correction the user asked for, and gating it on the merge
	// threshold is the ratchet this whole function is written to avoid. The
	// threshold bounds what the ENGINE may conclude; the user asking to peel a
	// face off is not the engine concluding anything.
	if len(moved) == 1 {
		c := meanOf(keep)
		if c.distanceTo(moved[0]) == 0 {
			return []int{id}
		}
	}

	// Cut at the widest gap in the sorted order, which is the most likely
	// boundary between two groups. Deterministic: ties break by position, and
	// the sort is stable on a unique key.
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].pos < keep[j].pos })
	//
	// bestGap starts at 0, NOT at a negative sentinel. The first version used
	// -1 to mean "no gap found yet", and then tested `bestGap <= 0` -- which
	// cannot tell that sentinel apart from a real gap of exactly 0. Three
	// members all at 1.0 have gaps of 0 between them, so peeling a face off the
	// far end read as "no split" and was refused. The two states are different
	// and need different values: the loop below only runs when there is
	// something to compare, and a cut at index 0 of a group of coincident
	// faces is meaningless rather than a split.
	best, bestGap, haveGap := 0, 0.0, false
	for i := 0; i < len(keep)-1; i++ {
		if gap := keep[i+1].pos - keep[i].pos; gap > bestGap || !haveGap {
			best, bestGap, haveGap = i, gap, true
		}
	}

	// The 1-vs-N case needs no bisection: the moved face or faces ARE the second
	// group, and there is nothing to cut. The first version fell through to the
	// gap loop here, which does not run when len(keep) == 1, leaving bestGap at
	// its -1 sentinel and silently reporting "no split" for the most obvious
	// split there is -- peeling one face off a pair.
	if len(keep) == 1 {
		newID := g.newCluster()
		g.clusters[id] = keep
		g.clusters[newID] = moved
		for _, m := range moved {
			if m.key != "" {
				g.keys[m.key] = newID
			}
		}
		return []int{id, newID}
	}

	// A split that does not separate anything is not a split: if the widest
	// gap is zero the faces are identical and cutting is meaningless.
	//
	// This check belongs AFTER the 1-vs-N case below, not before it. With one
	// face kept the gap loop never executes, bestGap keeps its -1 sentinel, and
	// a check placed here reads that sentinel as "no split" -- so peeling one
	// face off a pair, the most ordinary correction there is, silently reported
	// as a no-op. Ordering this before the case it does not apply to is how the
	// first version lost the most common split in the feature.
	//
	// Note what this does NOT compare against, and the first version of this
	// function did compare against g.threshold -- which is the bug
	// TestOverMergeGuard_SplitIsAlwaysPossible caught. Gating the correction on
	// the same number that refused the join makes the guard a one-way ratchet:
	// a user who agrees a cluster is two people cannot separate them unless the
	// gap happens to exceed the merge threshold, which is a property of the
	// ENGINE's confidence rather than of the user's judgement.
	//
	// The threshold's job is to keep the ENGINE from guessing. It has no
	// business overruling a correction a human asked for.
	if !haveGap {
		return []int{id}
	}

	lo, hi := keep[:best+1], keep[best+1:]
	newID := g.newCluster()
	g.clusters[id] = lo
	g.clusters[newID] = append(hi, moved...)
	for _, m := range hi {
		if m.key != "" {
			g.keys[m.key] = newID
		}
	}
	for _, m := range moved {
		if m.key != "" {
			g.keys[m.key] = newID
		}
	}
	return []int{id, newID}
}
