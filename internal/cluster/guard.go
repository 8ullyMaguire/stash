package cluster

import "fmt"

// ErrUnusableFace is a face whose embedding cannot be measured against
// anything: a NaN, a zero vector, or the wrong width.
//
// A distinct sentinel from ErrTooFarFromCentre for the same reason
// errFaceAlreadyClaimed is: the remedies are opposite. Too-far means look
// elsewhere for this face; unusable means the INDEX is corrupt and the face
// needs re-embedding. A user correcting one with the other's advice is told to
// search harder about a problem that is not about distance at all.
var ErrUnusableFace = fmt.Errorf("face embedding is unusable")

// Step 2.4b.9: the guard, ported onto Geometry.
//
// # What changed and what did not
//
// The guard's LOGIC is unchanged and its nine tests are unchanged and still pass.
// What changed is where the arithmetic comes from: `guard` now holds a
// `Geometry` and asks it for distances, instead of reading `pos` off a
// `testFace` and subtracting.
//
// That is the whole refactor, and it is worth being precise about why it is
// small. The guard makes two measurements -- "is the candidate near this
// cluster's centre" and "would adding it strand an existing member" -- and
// neither of them ever mentioned cosine. They asked `|a - b|`. On a line that
// is the distance; in 512 dimensions it is cosine distance, and the two
// measurements are the same measurement with a different function underneath.
//
// # Why the scalar tests still work
//
// `testFace` stays, and `ScalarGeometry` is wired in by default, so
// `newGuard(0.5)` behaves exactly as before and the nine existing tests assert
// what they asserted. A real-vector twin then runs the SAME guard with
// `CosineGeometry` -- not a reimplementation of the rules, the same rules with a
// different arithmetic underneath. That is the property worth having: a twin
// that reimplemented the guard would test the twin.

// Membership decides ADMISSION: may this face join this cluster?
//
// It is a separate type from the scalar guard in overmerge.go rather than a
// replacement for it, because the two answer different questions. Membership
// asks whether a join is safe. The scalar guard also owns SPLIT, which is
// inherently one-dimensional -- "cut at the widest gap in the sorted order"
// has no meaning in 512 dimensions -- and whose edge cases took four rounds of
// debugging to get right. Porting it would have meant rewriting it.
type Membership struct {
	threshold float64
	// geom supplies the arithmetic. Never nil: newGuard defaults it, and a
	// zero guard is a panic rather than a silent zero distance, which is the
	// right trade -- a guard that measured everything as 0.0 would admit
	// everything.
	geom Geometry

	clusters map[int][]Point
	// keys maps a membership key to the cluster that already contains it, so a
	// rescan is a no-op rather than a duplicate.
	keys map[string]int
	next int
}

func newMembership(threshold float64) *Membership {
	return newMembershipWith(threshold, ScalarGeometry{})
}

// newMembershipWith is the real constructor. The scalar default above is not a
// convenience for the tests -- it is what makes them property tests rather than
// tests of the cosine implementation, which is the property the whole seam
// exists to preserve.
func newMembershipWith(threshold float64, geom Geometry) *Membership {
	if geom == nil {
		geom = ScalarGeometry{}
	}
	return &Membership{
		threshold: threshold,
		geom:      geom,
		clusters:  map[int][]Point{},
		keys:      map[string]int{},
	}
}

func (g *Membership) newCluster() int {
	g.next++
	g.clusters[g.next] = nil
	return g.next
}

func (g *Membership) size(id int) int { return len(g.clusters[id]) }

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
func (g *Membership) forceCluster(members []Point) int {
	g.next++
	g.clusters[g.next] = members
	for _, m := range members {
		if m.Key != "" {
			g.keys[m.Key] = g.next
		}
	}
	return g.next
}

func (g *Membership) centroidOf(id int) Point {
	c, err := g.geom.Centroid(g.clusters[id])
	if err != nil {
		// Unreachable for any cluster the guard admitted: the centroid of a
		// non-empty set of validated vectors exists, and a zero-centroid was
		// rejected at insert time. It IS reachable for a cluster planted by
		// forceCluster with a pair of cancelling vectors, which is why this
		// returns the geometry's answer rather than panicking -- and why
		// addMember re-checks and refuses.
		return Point{}
	}
	return c
}

// diameterOf is the cluster's width, or 0 with a flag when the width is
// unknown.
//
// Kept separate from centroidOf because the two answer different questions and
// conflating them is the over-merge. See geometry.go.
func (g *Membership) diameterOf(id int) (float64, bool) {
	d, err := g.geom.Diameter(g.clusters[id])
	if err != nil {
		return 0, false
	}
	return d, true
}

// addMember admits a face to a cluster or refuses it, with the reason.
//
// The three refusals, in order:
//
//   - the face is already a member of ANOTHER cluster (a corrupt index, not a
//     distance problem -- the remedy is the opposite)
//   - it is too far from the centre (it belongs to someone else)
//   - adding it would strand an existing member (this cluster is already two
//     people and needs a split)
func (g *Membership) addMember(id int, f Point) error {
	// A rescan of an existing member. The store's UNIQUE key turns this into an
	// UPDATE, and the guard must agree: counting it twice would compute the
	// centroid over a multiset and make every rescan loosen the clustering.
	if owner, ok := g.keys[f.Key]; ok && f.Key != "" {
		if owner == id {
			return nil
		}
		return errFaceAlreadyClaimedFor(f, owner)
	}

	members := g.clusters[id]

	// An empty cluster accepts anything: the first face has no one to be
	// confused with, and requiring a second would mean no cluster ever forms.
	//
	// The face's own vector is still validated, because a first face with a NaN
	// embedding becomes a centroid that every later face is measured against,
	// and every such measurement is NaN -- which compares false against the
	// threshold and passes. A guard that admitted it here would admit
	// everything after it.
	if len(members) == 0 {
		if err := g.validate(f); err != nil {
			return err
		}
		g.clusters[id] = append(members, f)
		if f.Key != "" {
			g.keys[f.Key] = id
		}
		return nil
	}

	// Measurement 1: the candidate against the cluster's centre.
	old := g.centroidOf(id)
	d, err := g.geom.Distance(f, old)
	if err != nil {
		return err
	}
	if d > g.threshold {
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
	combined := append(append([]Point{}, members...), f)
	newC, err := g.geom.Centroid(combined)
	if err != nil {
		return err
	}

	var stranded *Point
	var worst float64
	for i := range combined {
		dd, err := g.geom.Distance(combined[i], newC)
		if err != nil {
			return err
		}
		if dd > g.threshold {
			if dd > worst {
				worst, stranded = dd, &combined[i]
			}
		}
	}
	if stranded != nil {
		return fmt.Errorf("%w: adding %q would leave member %q %.3f from the "+
			"new centroid, threshold %.2f; the cluster is already two people "+
			"and needs a split rather than a bigger cluster",
			ErrOverMerge, f.Key, stranded.Key, worst, g.threshold)
	}

	// Measurement 3, and the one the scalar version did not have: the cluster's
	// WIDTH must not exceed twice the threshold.
	//
	// Measurements 1 and 2 are both about distance to the CENTRE. A bimodal
	// cluster has a mean position in the middle of the gap, so both pass while
	// the cluster is twice as wide as its threshold -- and a cluster twice as
	// wide admits twice the faces, so the error compounds. On the 1-D fixture
	// the two measurements coincide, which is exactly why it went unnoticed:
	// a span of 2T is also 2T from the centre of a symmetric pair, so the
	// existing nine tests all pass with this line deleted.
	//
	// 2T rather than T: a cluster of two faces T apart is legitimate, and the
	// guard admits each when it arrives. The width bound is a statement about
	// spread, not about the admission threshold.
	// The current width is not compared against anything: what matters is the
	// width the cluster would HAVE. This call exists only to fail early and
	// loudly when an existing member is already unmeasurable -- otherwise the
	// new width below would be the first thing to notice, and its error would
	// name the candidate rather than the corrupt member that caused it.
	if _, ok := g.diameterOf(id); !ok {
		return fmt.Errorf("%w: the cluster's width is unknown because a "+
			"member's embedding is corrupt; refusing rather than guessing, "+
			"because a split is destructive", ErrOverMerge)
	}
	if newWidth, err := g.geom.Diameter(combined); err == nil && newWidth > 2*g.threshold {
		return fmt.Errorf("%w: adding %q would make the cluster %.3f wide, "+
			"more than twice the threshold %.2f; the members are two groups "+
			"rather than one",
			ErrOverMerge, f.Key, newWidth, g.threshold)
	}

	g.clusters[id] = combined
	if f.Key != "" {
		g.keys[f.Key] = id
	}
	return nil
}

// validate refuses a face whose vector cannot be measured against anything.
//
// Called on the first face of a cluster, where there is no centroid to compare
// against and so no other measurement would ever catch it.
func (g *Membership) validate(f Point) error {
	// A face with ITSELF, and the self-comparison is not a formality -- it is
	// the only measurement available for a first face, and it is where the NaN
	// trap lives.
	//
	// It also catches a wrong-width embedding, which is subtler than it sounds.
	// CosineDistance checks that its two arguments are the same width, so a
	// 2-wide vector compared against another 2-wide vector sails through -- the
	// self-comparison of a malformed vector is perfectly well-formed. Without
	// an explicit check against the expected width, a short embedding enters
	// as the cluster's first face and is only discovered when the SECOND face
	// arrives, by which point the bad centroid is already stored.
	//
	// So: the self-distance for the NaN and zero-vector traps, and the width
	// for the one it structurally cannot see.
	if len(f.Vector) == 0 {
		return fmt.Errorf("%w: face %q has no embedding", ErrUnusableFace, f.Key)
	}
	// The self-comparison, which is where the NaN and zero-vector traps live.
	//
	// The WIDTH check is deliberately not here. It belongs to the geometry --
	// CosineGeometry knows it expects 512 components and ScalarGeometry has no
	// notion of a width at all. Putting it in the guard meant the guard could
	// not be used with any geometry but the production one, which is precisely
	// what the scalar property tests are for. CosineGeometry.Distance performs
	// the check instead, so a Geometry added later gets the right behaviour for
	// free rather than silently admitting short vectors.
	if _, err := g.geom.Distance(f, f); err != nil {
		return fmt.Errorf("%w: face %q has an unusable embedding: %v",
			ErrUnusableFace, f.Key, err)
	}
	return nil
}

func errFaceAlreadyClaimedFor(f Point, owner int) error {
	return fmt.Errorf("%w: face %q is in cluster %d", errFaceAlreadyClaimed, f.Key, owner)
}
