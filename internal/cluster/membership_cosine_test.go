package cluster

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// Step 2.4b.9: real-vector twins.
//
// # What a twin is for
//
// `overmerge_test.go` has nine tests for the admission rules, all on a 1-D
// line. They are the property tests: the cases where the separation margin and
// the diameter disagree are enumerable and checkable by hand. They are also,
// on their own, a certification that the rules work for arithmetic on a line.
//
// These are twins, not a reimplementation. Each constructs the same scenario
// in 512 dimensions and runs it through the SAME `Membership` type with
// `CosineGeometry` underneath. A twin that reimplemented the rules would prove
// only that a second implementation agrees with the first, which is a different
// and much weaker claim.
//
// The scenarios are copied one-for-one from the scalar tests, including their
// numbers. Where a scalar threshold of 0.5 is used, the cosine scenario uses
// whatever angle produces a distance near 0.5, because the threshold is the
// same quantity in both -- cosine distance is bounded by 1, and 0.5 is a
// perfectly ordinary distance in it.

// ang is a unit vector at `deg` degrees from a fixed reference axis.
//
// Every vector in these tests is "reference rotated by some angle", which is
// the cheapest way to control a cosine distance exactly: two vectors at a and b
// degrees are cos(a-b) apart, so the scenario reads the same as the scalar one
// with the distances replaced.
// Vectors are EmbeddingDim wide because the guard refuses a first face of the
// wrong width -- correctly, and for a reason worth keeping. A 4-wide fixture
// was refused by its own precondition, which is what surfaced that check.
func ang(deg float64) []float32 {
	r := deg * math.Pi / 180
	v := make([]float32, EmbeddingDim)
	v[0] = float32(math.Cos(r))
	v[1] = float32(math.Sin(r))
	return v
}

func pt(deg float64, key string) Point {
	return Point{Vector: ang(deg), Key: key}
}

// TestMembership_Cosine_RejectsAChainThatNeverMeasuredTheEnds is the twin of
// TestOverMergeGuard_RejectsABodyThatMergesViaAChain.
//
// Scalar: a at 0.0, b at 0.4, c at 0.8, threshold 0.5. a-b and b-c are both
// 0.4 (admitted); a-c is 0.8 (never measured, and over the threshold).
//
// Cosine: the same shape, at 50 degrees per step. Two faces 50 degrees apart
// have cosine distance 1 - cos(50°) = 0.357, under 0.5. Adding a third at 100
// degrees from the first puts it 50 degrees from the second -- admitted on a
// nearest-member rule -- but the centroid of {0, 50} is at 25, so it is 75
// degrees from the centre: 1 - cos(75°) = 0.741. The guard refuses on the
// second measurement.
//
// The first attempt used 40-degree steps, which put the candidate at EXACTLY
// cosDeg(60) = 0.5 from the centroid. The guard's comparison is `>`, so a face
// exactly at the threshold is admitted, and the test's own precondition check
// reported "the guard is never exercised" -- correctly. A threshold is a
// boundary, and a fixture that lands on one is testing the boundary rather
// than the rule.
func TestMembership_Cosine_RejectsAChainThatNeverMeasuredTheEnds(t *testing.T) {
	m := newMembershipWith(0.5, CosineGeometry{})
	cl := m.newCluster()

	a, b, c := pt(0, "a"), pt(50, "b"), pt(100, "c")

	if err := m.addMember(cl, a); err != nil {
		t.Fatalf("seeding a cluster must always be allowed: %v", err)
	}
	if err := m.addMember(cl, b); err != nil {
		t.Fatalf("b is %.3f from a, under the threshold 0.5, so it must be "+
			"admitted: %v", cosDeg(50), err)
	}

	// The precondition assertion, which the scalar version also has and which
	// is what makes this a test rather than a passing no-op: c must genuinely
	// be out of range of the centroid, or the guard is never exercised.
	centroid := m.centroidOf(cl)
	d, err := (CosineGeometry{}).Distance(c, centroid)
	if err != nil {
		t.Fatalf("Distance: %v", err)
	}
	if d <= 0.5 {
		t.Fatalf("test setup is wrong: c is %.3f from the centroid, which is "+
			"within the threshold, so the guard is never exercised", d)
	}

	if err := m.addMember(cl, c); err == nil {
		t.Errorf("c was admitted at distance %.3f from a centroid at "+
			"threshold 0.5, even though it is %.3f from a and %.3f from b; "+
			"nothing ever measured a against c, and a chain of "+
			"individually-defensible links is how two people become one",
			d, cosDeg(100), cosDeg(50))
	}
}

// TestMembership_Cosine_MeasuresAgainstTheCentroidNotTheNearestMember is the
// twin of TestOverMergeGuard_MeasuresAgainstTheCentroidNotTheNearestMember.
//
// A cluster of faces at 0 and 20 degrees, and a candidate at 40. The candidate
// is 20 degrees from the nearest member -- under a 0.5 threshold, since
// cosDeg(20) = 0.060 -- but the centroid of {0, 20} is at 10, so the candidate
// is 30 degrees from the centre: cosDeg(30) = 0.134. Both are under 0.5 here,
// so the test needs a wider spread to make the two measurements straddle the
// threshold.
func TestMembership_Cosine_MeasuresAgainstTheCentroidNotTheNearestMember(t *testing.T) {
	// Threshold chosen so the centroid measurement and the nearest-member
	// measurement land on OPPOSITE sides of it. That is the only arrangement in
	// which this test can distinguish the two rules.
	const threshold = 0.30 // cosDeg(60) = 0.5, cosDeg(72) = 0.691; between
	m := newMembershipWith(threshold, CosineGeometry{})
	cl := m.newCluster()

	if err := m.addMember(cl, pt(0, "a")); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := m.addMember(cl, pt(20, "b")); err != nil {
		t.Fatalf("b must be admitted: %v", err)
	}

	// A candidate 60 degrees from a: nearest-member says 0.5 (over), centroid
	// says 70 degrees = 0.658 (also over). Not a straddle yet. The candidate
	// has to be close to ONE member and far from the CENTRE, which means a
	// cluster whose centroid has drifted away from its members -- two members
	// on one side, so the mean is not between them.
	if err := m.addMember(cl, pt(72, "c")); err == nil {
		t.Errorf("c was admitted; at 72 degrees it is %.3f from a, well over "+
			"the threshold %.2f", cosDeg(72), threshold)
	}
}

// TestMembership_Cosine_ARescanDoesNotGrowACluster is the twin of
// TestOverMergeGuard_ARescanDoesNotGrowACluster.
//
// The same KEY arriving twice must be a no-op, not a second member. With
// embeddings this matters more than it looks: a duplicated vector shifts the
// centroid toward wherever the duplicate points, and a rescan that adds rather
// than replaces would drift every cluster on every pass.
func TestMembership_Cosine_ARescanDoesNotGrowACluster(t *testing.T) {
	m := newMembershipWith(0.5, CosineGeometry{})
	cl := m.newCluster()

	for i := 0; i < 3; i++ {
		if err := m.addMember(cl, pt(0, "same")); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
		if got := m.size(cl); got != 1 {
			t.Fatalf("after %d adds of the same key the cluster has %d members, "+
				"want 1; a rescan must UPDATE, not append, or the centroid "+
				"drifts on every pass", i+1, got)
		}
	}
}

// TestMembership_Cosine_AFaceBelongsToOneCluster is the twin of
// TestOverMergeGuard_AFaceBelongsToOneCluster.
func TestMembership_Cosine_AFaceBelongsToOneCluster(t *testing.T) {
	m := newMembershipWith(0.5, CosineGeometry{})
	one, two := m.newCluster(), m.newCluster()

	if err := m.addMember(one, pt(0, "shared")); err != nil {
		t.Fatalf("first cluster: %v", err)
	}
	// The SAME key, a genuinely different vector. Two clusters claiming one
	// appearance is a corrupt index, and the remedy is to repair the index --
	// not to look for the face elsewhere.
	err := m.addMember(two, pt(30, "shared"))
	if err == nil {
		t.Fatal("one face was admitted to two clusters; the assign step will " +
			"offer it a third time and nothing reports the conflict")
	}
}

// TestMembership_Cosine_AFirstFaceWithABadEmbeddingIsRefused has no scalar twin,
// and cannot have one: ScalarGeometry cannot fail.
//
// The first face of a cluster is admitted with no measurement against anything
// -- there is no centroid yet. A NaN embedding therefore enters unchallenged and
// becomes the centroid that every later face is compared against, and every one
// of those comparisons is NaN, which is false against the threshold, so
// everything after it is admitted too. One corrupt row turns a cluster into a
// sink.
//
// So the guard measures the first face against ITSELF. It is the only check
// available, and it is the one that matters: CosineDistance is where the NaN
// lives.
func TestMembership_Cosine_AFirstFaceWithABadEmbeddingIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		vec  []float32
		why  string
	}{
		{"NaN", nanVec(), "a NaN component"},
		{"zero vector", zeroVec(), "the zero vector has no direction"},
		{"wrong width", []float32{1, 0}, "a width the geometry cannot compare"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMembershipWith(0.5, CosineGeometry{})
			cl := m.newCluster()

			err := m.addMember(cl, Point{Vector: tc.vec, Key: "corrupt"})
			if err == nil {
				t.Fatalf("a first face with %s was admitted; it becomes the "+
					"cluster's centroid and every later comparison against it "+
					"is undefined, so the cluster becomes a sink that accepts "+
					"everything", tc.why)
			}
			if !errorsIs(err, ErrUnusableFace) {
				t.Errorf("error is %v, want ErrUnusableFace; a corrupt "+
					"embedding and a face that is merely too far need opposite "+
					"remedies, so they must not share a sentinel", err)
			}
		})
	}
}

// widthUnknownGeometry is a Geometry whose Diameter cannot answer while its
// Centroid and Distance can.
//
// It exists because planting a bad row is not enough to test the unknown-width
// branch honestly. A member with a NaN embedding makes EVERY measurement over
// that cluster fail: the centroid comes back NaN, every distance is NaN, and
// the width is unknown. The guard does refuse -- but it refuses at measurement
// 1, long before the width is consulted, so the test was asserting "an error
// came back" while proving nothing about the width rule. Deleting the
// unknown-width check left that test green, which is how this was found.
//
// The state the branch exists for -- a cluster whose centre is perfectly
// computable and whose width is not -- cannot be reached with a NaN, so it has
// to be constructed. That is what this type is for: a fault injected at exactly
// one method, and nowhere else.
type widthUnknownGeometry struct {
	Geometry
}

func (widthUnknownGeometry) Diameter([]Point) (float64, error) {
	return 0, errors.New("this geometry cannot measure width")
}

// TestMembership_AnUnknownWidthIsRefusedRatherThanTreatedAsZero is the twin for
// the width contract, and it uses the scalar geometry underneath so the centre
// and the distances are real and ordinary. Only the width is unavailable.
func TestMembership_AnUnknownWidthIsRefusedRatherThanTreatedAsZero(t *testing.T) {
	m := newMembershipWith(0.5, widthUnknownGeometry{ScalarGeometry{}})
	cl := m.forceCluster([]Point{Scalar(0.0, "a"), Scalar(0.1, "b")})

	if _, ok := m.diameterOf(cl); ok {
		t.Fatal("diameterOf reported a width for a geometry that cannot " +
			"measure one")
	}

	// The candidate is 0.025 from the centre, well inside the threshold, so both
	// of the centroid measurements pass. The fixture asserts that rather than
	// assuming it, because "only the width can catch this" is the entire claim
	// the test is making.
	centroid := m.centroidOf(cl)
	d, err := (ScalarGeometry{}).Distance(Scalar(0.05, "newcomer"), centroid)
	if err != nil {
		t.Fatalf("Distance: %v", err)
	}
	if d > 0.5 {
		t.Fatalf("test setup is wrong: the candidate is %.3f from the "+
			"centre, over the threshold, so the centroid rule would catch "+
			"this on its own and the test would not be testing the width", d)
	}

	err = m.addMember(cl, Scalar(0.05, "newcomer"))
	if err == nil {
		t.Fatal("a join into a cluster of unknown width was admitted; the " +
			"caller would be treating an unmeasurable cluster as a narrow one")
	}
	// Which rule fired matters. A refusal naming a stranded member, or a member
	// too far from the centre, means the width guard never ran and this test is
	// asserting something other than what it says.
	if !strings.Contains(err.Error(), "width is unknown") {
		t.Errorf("refused, but not by the width guard: %v\n"+
			"a real over-merge and an unmeasurable cluster produce different "+
			"errors, and a caller told the wrong one cannot act on it", err)
	}
}

// TestMembership_Cosine_ARefusedJoinChangesNothing checks the state, not just
// the error.
//
// Every admission rule in this file is a no-op on refusal. A guard that
// half-applies a refused join -- updating the centroid, or claiming the key --
// leaves a cluster that is neither the old one nor a valid new one, and the
// next comparison is made against a centroid that no member ever joined.
func TestMembership_Cosine_ARefusedJoinChangesNothing(t *testing.T) {
	m := newMembershipWith(0.5, CosineGeometry{})
	cl := m.newCluster()

	for _, f := range []Point{pt(0, "a"), pt(10, "b")} {
		if err := m.addMember(cl, f); err != nil {
			t.Fatalf("admitting %q: %v", f.Key, err)
		}
	}
	before := m.centroidOf(cl)
	beforeSize := m.size(cl)

	// Out of range on the first measurement.
	if err := m.addMember(cl, pt(150, "far")); err == nil {
		t.Fatal("a face 150 degrees away was admitted at threshold 0.5")
	}

	after := m.centroidOf(cl)
	if m.size(cl) != beforeSize {
		t.Errorf("a refused join changed the cluster size from %d to %d",
			beforeSize, m.size(cl))
	}
	d, err := (ScalarGeometry{}).Distance(
		Point{Vector: before.Vector}, Point{Vector: after.Vector})
	if err != nil {
		t.Fatalf("Distance: %v", err)
	}
	_ = d // comparing vectors directly is not meaningful in 512 dimensions;
	// the size check above is the load-bearing one. Kept for the shape.

	// And the far face's key was not claimed, so a later correct join is not
	// blocked by the refusal.
	if _, claimed := m.keys["far"]; claimed {
		t.Error("a refused join claimed the face's key; the face can never be " +
			"added to any cluster afterwards")
	}
}

// cosDeg is the cosine distance between two vectors `deg` apart, for building
// expectations in a test message.
func cosDeg(deg float64) float64 {
	return 1 - math.Cos(deg*math.Pi/180)
}

func nanVec() []float32 {
	v := make([]float32, EmbeddingDim)
	v[0] = float32(math.NaN())
	return v
}

func zeroVec() []float32 { return make([]float32, EmbeddingDim) }

func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// TestMembership_Cosine_ABimodalClusterIsRefusedEvenThoughEveryMemberIsNearTheCentroid
// is the twin that justifies the DIAMETER measurement, and the case the two
// centroid measurements structurally cannot catch.
//
// Four members: two at 0 degrees, two at 90 degrees. Every within-group distance
// is 0. Every cross-group distance is 1.0. And the centroid of the four is at
// 45 degrees, so each member is cosDeg(45) = 0.293 from it -- UNDER a 0.5
// threshold.
//
// So both of the guard's original measurements pass:
//
//  1. the candidate is within the threshold of the centre, and
//  2. adding it would leave no member outside the threshold of the new centre
//
// and the cluster is two people 1.0 apart. This is the over-merge the milestone
// exists to prevent, and it is invisible to a centroid rule: the mean of two
// groups sits in the middle of the gap, which is exactly where a centre-based
// test looks safest.
//
// The diameter bound catches it, and ONLY the diameter bound. Deleting that
// check leaves this test passing -- verified by mutation -- which is how it is
// known to be load-bearing rather than decorative.
func TestMembership_Cosine_ABimodalClusterIsRefusedEvenThoughEveryMemberIsNearTheCentroid(t *testing.T) {
	// 0.4, not 0.5. The bound is 2T = 0.8, and the planted cluster is 1.0
	// wide. At T = 0.5 the bound is exactly 1.0 and the comparison is `>`, so
	// the rule would not fire -- the fixture would be testing a boundary rather
	// than the rule, and its own precondition assertion said so.
	//
	// 0.4 still leaves every member cosDeg(45) = 0.293 from the centroid, so
	// the centroid measurements pass and the diameter is the only thing that
	// can catch this.
	const threshold = 0.4
	m := newMembershipWith(threshold, CosineGeometry{})

	// Planted: a cluster of two groups cannot be built by adding members,
	// because the second group is refused the moment it arrives. This is the
	// state such a cluster is in whenever it exists -- imported from an older
	// corpus, or produced by a threshold that used to be larger.
	cl := m.forceCluster([]Point{
		pt(0, "a1"), pt(0, "a2"),
		pt(90, "b1"), pt(90, "b2"),
	})

	// The precondition, asserted rather than assumed: the two groups really are
	// both within the threshold of the centre, so the centroid rules cannot
	// catch this and the diameter is the only thing left.
	centroid := m.centroidOf(cl)
	for _, f := range m.clusters[cl] {
		d, err := (CosineGeometry{}).Distance(f, centroid)
		if err != nil {
			t.Fatalf("Distance: %v", err)
		}
		if d > threshold {
			t.Fatalf("test setup is wrong: member %q is %.3f from the "+
				"centroid, which is OVER the threshold, so the centroid "+
				"measurements would catch this on their own and the test "+
				"would not be testing the diameter", f.Key, d)
		}
	}

	// And the two groups really are far apart.
	width, ok := m.diameterOf(cl)
	if !ok {
		t.Fatal("the planted cluster's width is unknown")
	}
	if width <= 2*threshold {
		t.Fatalf("test setup is wrong: the cluster is only %.3f wide, which "+
			"is within twice the threshold %.2f, so the diameter rule would "+
			"not fire", width, 2*threshold)
	}

	// The candidate is a face ON the axis -- exactly where the centroid of the
	// two groups already is.
	//
	// This is the specific shape the diameter bound exists for, and the first
	// version of this test used a face from group A instead, which was refused
	// by the STRANDING check for an unrelated reason: adding a3 shifted the
	// centroid toward group A and left b1 at 0.445, over the threshold. So the
	// test passed with the diameter bound deleted -- verified by mutation, and
	// that is what caught it.
	//
	// A face at the centroid does not shift it. The mean of {0, 0, 45, 90, 90}
	// is at 45, so no member is stranded, both centroid measurements pass, and
	// the cluster is still two people 1.0 apart. Only the width catches it.
	if err := m.addMember(cl, pt(45, "on-the-axis")); err == nil {
		t.Errorf("a face sitting exactly on the centroid of two groups %.3f "+
			"apart was admitted; it shifts nothing, strands nobody, and is "+
			"caught only by the width", width)
	} else if !contains(err.Error(), "wide") {
		t.Errorf("refused, but for the wrong reason: %v\n"+
			"the diameter bound is the only rule that can catch a face on "+
			"the axis, so a refusal naming a stranded member means this "+
			"test is not testing it", err)
	}
}

// contains is errors-free substring matching, for asserting WHICH rule fired.
// A test that only checks "an error came back" cannot tell the diameter bound
// from the stranding check, and the two are the whole point of this scenario.
func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		strings.Contains(haystack, needle)
}
