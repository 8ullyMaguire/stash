package cluster

import (
	"errors"
	"strings"
	"testing"
)

// Step 2.4b.2: the over-merge guard (Commons §7.1 step 4).
//
// # The failure this exists to prevent
//
// Pairwise similarity is not transitive. If A is within T of B and B is within
// T of C, that says nothing about A and C. Greedy clustering that merges each
// face into its nearest neighbour therefore merges A and C on B's authority —
// and the resulting cluster is one hop further from every pair inside it than
// any single decision justified.
//
// This is not a rare edge case; it is what happens at scale. A fan of similar
// faces (the same performer, or genuinely similar people) produces a chain
// where every link is defensible and the endpoints are not the same person. The
// cluster grows by absorbing the *most confident* link first, which makes it
// worse in a specific way: the first merge is the one a casual review would
// have approved.
//
// The guard: a face may only join a cluster if it is within T of the cluster's
// CENTROID, not of any individual member. And a merge that would make the
// centroid move so far that a member now sits outside T of it is REFUSED
// entirely, not applied and re-checked later.
//
// Written before the implementation, as with 2.4b.0.

func TestOverMergeGuard_RejectsABodyThatMergesViaAChain(t *testing.T) {
	// A and C are far apart. B is near both. A->B and B->C each look fine.
	// A->C must be refused, because nothing measured A against C.
	g := newGuard(0.5)
	cl := g.newCluster()

	// On a line: a at 0.0, b at 0.4, c at 0.8. a-c is 0.8, which is over the
	// threshold. a-b and b-c are both 0.4, both comfortably under it.
	a := &testFace{pos: 0.0, key: "a"}
	b := &testFace{pos: 0.4, key: "b"}
	c := &testFace{pos: 0.8, key: "c"}

	// a seeds the cluster, b joins it legitimately: 0.4 <= 0.5.
	if err := g.addMember(cl, a); err != nil {
		t.Fatalf("seeding a cluster must always be allowed: %v", err)
	}
	if err := g.addMember(cl, b); err != nil {
		t.Fatalf("b is %.2f from a, under the threshold %.2f, so it must be "+
			"admitted: %v", b.distanceTo(a), 0.5, err)
	}

	// Now c. It is 0.4 from b, so a nearest-MEMBER rule would admit it on b's
	// authority. The centroid of {a, b} is 0.2, and c is 0.6 from there --
	// over the threshold. The guard is the difference between those two
	// measurements, and it must refuse.
	centroid := g.centroidOf(cl)
	if d := c.distanceToCentroid(centroid); d > 0.5 {
		if err := g.addMember(cl, c); err == nil {
			t.Fatalf("c was admitted at distance %.2f from a centroid at %.2f "+
				"with threshold %.2f, even though it is 0.8 from a and 0.4 from "+
				"b; nothing ever measured a against c, and a chain of "+
				"individually-defensible links is how two people become one",
				d, centroid, 0.5)
		}
	} else {
		t.Fatalf("test setup is wrong: c is %.2f from the centroid, which is "+
			"within the threshold, so the guard is never exercised", d)
	}
}

func TestOverMergeGuard_MeasuresAgainstTheCentroidNotTheNearestMember(t *testing.T) {
	// The distinction that the first test is about, isolated: a face CLOSER to
	// a member than to the centroid must be measured against the centroid.
	//
	// Take three members at 0.0, 0.0 and 1.0. Centroid is 0.333. A new face at
	// 0.30 is 0.0 from one member and 0.033 from the centroid — admitted either
	// way, so the test must find a case where the two rules DISAGREE.
	//
	// Members at 0.0, 0.0, 1.0, 1.0: centroid 0.5. A face at 0.45 is 0.45 from
	// the near pair and 0.05 from the centroid. Here the centroid rule is the
	// PERMISSIVE one, which is the opposite of the intended direction.
	//
	// The setup plants the two halves directly rather than through addMember,
	// and that is the point rather than a convenience: the guard is exactly
	// what makes a two-half cluster impossible to build. Seeding 0.9 against a
	// centroid of 0.0 is refused (0.9 > 0.2), so "what happens if a cluster IS
	// two people" is a question about state the guarded path cannot reach --
	// imported from an older corpus, or produced by a threshold change. Planting
	// it is the only honest way to test the behaviour.
	//
	// The first version of this test seeded through addMember and failed on the
	// third member, which is the guard working correctly.
	//
	// The direction that matters: the centroid rule must be STRICTER when a
	// cluster has an outlier. Members 0.0, 0.0, 0.0, 0.0, 0.9. Centroid 0.18. A
	// face at 0.5 is 0.5 from the bulk and 0.32 from the centroid. A
	// nearest-member rule refuses it; a centroid rule admits it. That is wrong
	// in the safe direction for THIS face, but the test is not about this face.
	//
	// The case the guard exists for: members 0.0, 0.0, 0.9, 0.9 -- two tight
	// pairs far apart, which is what a bad merge produces. Centroid 0.45. A
	// face at 0.1 is 0.1 from a member and 0.35 from the centroid, and a
	// threshold of 0.2 admits it under the member rule while refusing it under
	// the centroid rule. A cluster that is really two people must not grow
	// further along one of its halves.
	g := newGuard(0.2)
	cl := g.forceCluster([]*testFace{
		{pos: 0.0, key: "a1"}, {pos: 0.0, key: "a2"},
		{pos: 0.9, key: "b1"}, {pos: 0.9, key: "b2"},
	})

	cand := &testFace{pos: 0.1}
	centroidDist := cand.distanceToCentroid(g.centroidOf(cl))
	memberDist := cand.distanceTo(&testFace{pos: 0.0})

	if !(memberDist <= 0.2 && centroidDist > 0.2) {
		t.Fatalf("test setup is wrong: it must produce a case where the two "+
			"rules disagree. member distance %.3f, centroid distance %.3f, "+
			"threshold 0.2", memberDist, centroidDist)
	}

	if err := g.addMember(cl, cand); err == nil {
		t.Fatalf("a face %.3f from a member but %.3f from the centroid was "+
			"admitted at threshold 0.2; a cluster that is really two people "+
			"must not grow along one of its halves",
			memberDist, centroidDist)
	}
}

func TestOverMergeGuard_ARescanDoesNotGrowACluster(t *testing.T) {
	// Idempotence. A rescan of a file the cluster already contains must not
	// change the cluster, or every rescan makes the clustering looser and a
	// corpus degrades with each pass.
	g := newGuard(0.5)
	cl := g.newCluster()

	f := &testFace{pos: 0.1, key: "scene:1:frame:10"}
	if err := g.addMember(cl, f); err != nil {
		t.Fatalf("first add: %v", err)
	}
	before := g.size(cl)

	// Same face, same frame. The membership UNIQUE key makes this an UPDATE at
	// the store level; the guard must treat it as a no-op rather than a second
	// member, or the centroid is computed over a multiset of duplicates.
	if err := g.addMember(cl, &testFace{pos: 0.1, key: "scene:1:frame:10"}); err != nil {
		t.Fatalf("a rescan of an existing member must be accepted as a no-op, "+
			"not an error: %v", err)
	}
	if got := g.size(cl); got != before {
		t.Errorf("a rescan changed the cluster size from %d to %d; duplicates "+
			"inflate the centroid's support and every rescan makes the "+
			"clustering looser", before, got)
	}
}

// TestOverMergeGuard_SplitIsAlwaysPossible is the half that makes the guard
// safe to apply aggressively.
//
// A guard that can only refuse to grow is a one-way ratchet: a user who agrees
// a cluster is wrong cannot undo it, and the only way forward is to delete
// every face in it. Splitting must not be gated by the same threshold, or
// "reject a bad merge" and "correct a bad merge" have different costs and
// people will merge first and split later.
func TestOverMergeGuard_SplitIsAlwaysPossible(t *testing.T) {
	g := newGuard(0.01) // absurdly tight: nothing would ever be admitted
	cl := g.forceCluster([]*testFace{
		{pos: 0.0, key: "a"}, {pos: 0.5, key: "b"},
	})

	// At threshold 0.01 a face 0.5 away is not admissible by the merge rule.
	// Splitting it out must still work.
	out := g.split(cl, &testFace{pos: 0.5, key: "b"})
	if len(out) != 2 {
		t.Errorf("split produced %d clusters, want 2; a user correcting a bad "+
			"merge must not be blocked by the same threshold that refused it",
			len(out))
	}
}

// TestOverMergeGuard_JoiningDoesNotStrandAnExistingMember is the check that
// makes the guard a property of the CLUSTER rather than of the candidate.
//
// Removing the strand check -- `if false && stranded != nil` -- leaves the
// whole file GREEN. That mutation survived the first pass, which is the same
// gap the zero-Detector guard had in step 2.4b.0: a rule that can only ever
// make the code more cautious is invisible to a suite, because a suite with no
// test for the cautious case cannot tell cautious from absent.
//
// The scenario: three members at 1.0 and one at 0.0, so the centroid is 0.75.
// A new face at 0.2 is 0.55 from that centroid -- exactly the threshold, so it
// is ADMITTED. But adding it drags the centroid to 0.64, and the member at 0.0
// is now 0.64 away: outside. Every individual decision was defensible and the
// cluster has quietly become a line from 0.0 to 1.0.
//
// The geometry matters and the first version of this test got it backwards.
// The stranded member has to be on the OPPOSITE side of the centroid from the
// incoming face, because the centroid moves TOWARD the incoming face. Three
// members at 0.0 plus an outlier at 0.5, with a candidate at 0.9, does not
// work: the candidate is 0.775 from the centroid and the plain centroid check
// refuses it first, so the strand rule is never reached. The test asserted its
// own precondition and failed -- which is the assertion earning its keep.
func TestOverMergeGuard_JoiningDoesNotStrandAnExistingMember(t *testing.T) {
	g := newGuard(0.55)
	cl := g.forceCluster([]*testFace{
		{pos: 1.0, key: "a1"},
		{pos: 1.0, key: "a2"},
		{pos: 1.0, key: "a3"},
		{pos: 0.0, key: "far"},
	})

	cand := &testFace{pos: 0.2, key: "new"}

	// The precondition, asserted rather than assumed: WITHOUT the strand rule
	// this candidate is admissible, because it is within the threshold of the
	// current centroid (0.125). So this test is not vacuous -- it exercises a
	// join that a naive implementation really would make.
	centroidNow := g.centroidOf(cl)
	if d := cand.distanceToCentroid(centroidNow); d > g.threshold {
		t.Fatalf("test setup is wrong: the candidate is %.3f from the current "+
			"centroid, already over the threshold %.2f, so the strand rule is "+
			"never reached", d, g.threshold)
	}

	err := g.addMember(cl, cand)
	if err == nil {
		t.Fatalf("a join that would leave the face at 0.0 outside the " +
			"threshold of the new centroid was accepted; the member at 0.0 was " +
			"well inside the threshold before the join and the cluster is a " +
			"line from 0.0 to 1.0 after it")
	}
	if !errors.Is(err, ErrOverMerge) {
		t.Errorf("expected ErrOverMerge, got %v; a strand is the cluster "+
			"already being two people, which is a split rather than a retry, "+
			"and the two must not be conflated", err)
	}

	// And the cluster is unchanged: a refused join leaves no trace.
	if got := g.size(cl); got != 4 {
		t.Errorf("a refused join changed the cluster size to %d; a partial "+
			"admission is the worst outcome, because the centroid moved and the "+
			"member is in the cluster but was never accounted for", got)
	}
}

// ---------------------------------------------------------------------------
// The three mutations that survived the first pass.
//
// Each is a rule that can only make the code more permissive, so a suite with
// no test for the strict case cannot tell "careful" from "absent". That is now
// three for three in this milestone -- the zero-Detector guard, the strand
// check, and these -- and it is the shape of gap worth naming: a missing
// refusal is invisible, a missing permission is loud.
// ---------------------------------------------------------------------------

// TestOverMergeGuard_AFaceBelongsToOneCluster: a face in two clusters is
// counted twice, and both clusters then grow a centroid supported by a member
// the other also claims.
//
// The consequence is not cosmetic. The over-merge guard measures distance to a
// centroid, so a face inflating two centroids drags both toward itself and
// makes two clusters converge -- the over-merge happening through the
// bookkeeping rather than through a bad judgement call.
func TestOverMergeGuard_AFaceBelongsToOneCluster(t *testing.T) {
	g := newGuard(0.5)
	first := g.forceCluster([]*testFace{{pos: 0.0, key: "shared"}})
	second := g.forceCluster([]*testFace{{pos: 0.9, key: "other"}})

	err := g.addMember(first, &testFace{pos: 0.1, key: "new"})
	if err != nil {
		t.Fatalf("a face joining the cluster that already holds its neighbours "+
			"must be allowed: %v", err)
	}

	// Same key, different cluster. The face is 0.8 from second's centroid and
	// 0.1 from its own neighbour, so the DISTANCE check would not catch it --
	// only the identity check can.
	owner, held := g.keys["new"]
	if !held {
		t.Fatal("the face was not recorded as a member of any cluster")
	}

	err = g.addMember(second, &testFace{pos: 0.1, key: "new"})
	// The error IDENTITY is the assertion, not merely that there was one.
	//
	// The first version of this test only checked `err == nil`, and the
	// identity-guard mutation SURVIVED it: with the guard removed the candidate
	// still failed, but on the DISTANCE check (it is 0.8 from second's lone
	// member at 0.9), so a green test was agreeing with a broken implementation
	// for the wrong reason. ErrTooFarFromCentre means "this face belongs to
	// someone else"; the identity guard means "this face is already spoken
	// for". Only the second is the defect, and they are opposite remedies --
	// a different face somewhere, versus a corrupt index.
	if !errors.Is(err, errFaceAlreadyClaimed) {
		t.Errorf("a face already in cluster %d was admitted to cluster %d too "+
			"(error: %v); a face in two clusters counts twice toward two "+
			"centroids and makes them converge. Wanted %v specifically -- a "+
			"distance failure is a different defect with a different remedy",
			owner, second, err, errFaceAlreadyClaimed)
	}
}

// TestOverMergeGuard_AFirstFaceIsAlwaysAdmitted: the singleton free-pass.
//
// Removing it leaves the suite green. A new cluster has a centroid of 0, and a
// face anywhere near 0 is inside any sane threshold, so the free-pass looks
// like a no-op. It is not: with a threshold below the face's distance from zero
// the first face is refused and NO CLUSTER EVER FORMS. The library silently
// contains no clusters, which is the same failure shape as the missing model
// file in step 2.4b.0 -- an empty result that reads as a finding.
func TestOverMergeGuard_AFirstFaceIsAlwaysAdmitted(t *testing.T) {
	g := newGuard(0.01)
	cl := g.newCluster()

	// 0.5 from the empty cluster's centroid of 0 -- far outside a 0.01
	// threshold. Without the free-pass this is refused and no cluster is born.
	if err := g.addMember(cl, &testFace{pos: 0.5, key: "first"}); err != nil {
		t.Fatalf("the first face of a cluster was refused (%v); requiring a "+
			"cluster to already contain a face means no cluster is ever created, "+
			"and an empty cluster table reads as 'no faces found'", err)
	}
	if g.size(cl) != 1 {
		t.Errorf("cluster has %d members after its first face, want 1", g.size(cl))
	}
}

// TestOverMergeGuard_ASplitAlwaysSeparatesTwoDistinctFaces: the
// adjacent-face check exists so pulling a face out of the MIDDLE of a cluster
// does not leave a hole, but it must not be so wide that a genuine correction
// is refused.
//
// The mutation gated it on g.threshold, which makes the guard a one-way
// ratchet: the engine's confidence decides whether a user is allowed to
// correct a mistake it made.
func TestOverMergeGuard_ASplitAlwaysSeparatesTwoDistinctFaces(t *testing.T) {
	g := newGuard(0.55)
	cl := g.forceCluster([]*testFace{
		{pos: 1.0, key: "a1"},
		{pos: 1.0, key: "a2"},
		{pos: 1.0, key: "a3"},
		{pos: 0.0, key: "b"},
	})

	out := g.split(cl, &testFace{pos: 0.0, key: "b"})
	if len(out) != 2 {
		t.Errorf("split produced %d clusters, want 2; face b is 1.0 from the "+
			"other three, and a threshold of 0.55 gating the correction would "+
			"mean the engine's confidence decides whether a user may undo its "+
			"own mistake", len(out))
	}
}

// TestOverMergeGuard_ASplitCutsAtTheWidestGapNotTheMiddle: where the cut
// lands decides which faces end up together, and the widest gap is the only
// place the boundary is likely to be.
//
// Three clusters of members at 0.0, 0.1 and 1.0. The midpoint split would cut
// between 0.1 and 1.0 -- the same place, in this arrangement. To separate the
// two rules the near pair must straddle the midpoint: 0.45 and 0.55, with a
// lone face at 0.0. The midpoint cut lands between 0.0 and 0.45 and leaves
// 0.45 with 0.55, merging the isolated face with half the cluster.
func TestOverMergeGuard_ASplitCutsAtTheWidestGapNotTheMiddle(t *testing.T) {
	// The gap rule, exercised directly rather than through the 1-vs-N path.
	//
	// The first version of this test moved the lone face out and then asserted
	// on the remainder, which took the len(keep)==1 shortcut and never
	// consulted the gap at all -- so replacing the widest-gap cut with a
	// midpoint cut SURVIVED. The arrangement below is chosen so the two rules
	// pick different cut points and the assertion can tell.
	//
	// keep = 0.00, 0.45, 0.55
	//   widest gap is 0.45, between 0.00 and 0.45 -> cut index 0
	//   midpoint (len/2) is 1, between 0.45 and 0.55 -> cut index 1
	//
	// So the widest-gap rule isolates 0.00 and the midpoint rule strands it
	// with 0.45. A midpoint cut is the one that would put the odd face in the
	// wrong group, and that is exactly what a fixed split does to a corpus
	// whose members are not evenly distributed along the line.
	g := newGuard(0.6)
	cl := g.forceCluster([]*testFace{
		{pos: 0.0, key: "odd"},
		{pos: 0.45, key: "p1"},
		{pos: 0.55, key: "p2"},
	})

	// Move p1 out. keep = {odd, p2}; moved = {p1}. p1 is 0.05 from p2 and 0.45
	// from odd, so it is NOT adjacent-to-nothing and the split proceeds to the
	// gap cut.
	out := g.split(cl, &testFace{pos: 0.45, key: "p1"})
	if len(out) != 2 {
		t.Fatalf("split produced %d clusters, want 2", len(out))
	}

	// Identify the group each key landed in by cluster id, since the keys are
	// what the test cares about and the ids are an implementation detail.
	byID := map[int][]string{}
	for _, id := range out {
		for _, m := range g.clusters[id] {
			byID[id] = append(byID[id], m.key)
		}
	}

	oddGroup, p2Group := "", ""
	for _, members := range byID {
		for _, m := range members {
			if m == "odd" {
				oddGroup = strings.Join(members, ",")
			}
			if m == "p2" {
				p2Group = strings.Join(members, ",")
			}
		}
	}

	if oddGroup == p2Group {
		t.Errorf("odd (0.00) and p2 (0.55) ended up together in %q; the widest "+
			"gap is 0.45, between odd and p1, so a midpoint cut would have "+
			"grouped the odd face with half a cluster", oddGroup)
	}
	if oddGroup != "odd" {
		t.Errorf("the widest gap is 0.45, so the odd face at 0.00 should be "+
			"isolated, but it landed in %q", oddGroup)
	}
	if p2Group != "p1,p2" && p2Group != "p2,p1" {
		t.Errorf("p2 should be grouped with the moved face p1, got %q", p2Group)
	}
}
