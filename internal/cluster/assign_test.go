package cluster

import "testing"

// Step 2.4b.3: the assign decision.
//
// # What this decides
//
// Given a face and the clusters that could contain it, one of three things
// happens, and the choice between them is the whole feature:
//
//	new cluster    nothing was close. The normal outcome for a first face.
//	join           exactly one cluster was clearly closer than the rest.
//	ambiguous      two clusters were close ENOUGH TO EACH OTHER that joining
//	               either would be a guess. Both candidates are recorded and
//	               the state goes to 'ambiguous' for a human to resolve.
//
// # Why 'ambiguous' is the hard case
//
// The tempting shortcut is to pick the nearer cluster and move on. It is
// wrong in a specific way: the distance that decides "nearer" is the same
// distance that says nothing about whether the two clusters are the SAME
// person. Two clusters at 0.10 and 0.14 from the candidate, with the
// candidate's threshold at 0.15, means both are in range and the ranking
// between them is inside the noise. Choosing 0.10 is a 0.04-margin decision
// dressed up as a finding.
//
// The user is better at that call than the engine is, and the cost of being
// wrong is asymmetric: an ambiguous state is a few seconds of review, a wrong
// join silently merges two people and the error compounds with every
// subsequent face assigned to the merged cluster.
//
// Written test-first, as with 2.4b.0 and 2.4b.2.

func TestAssign_AFaceWithNoCloseClusterStartsANewOne(t *testing.T) {
	s := newStage(0.5)

	got := s.assign(Scalar(0.0, "new"))

	if got.Kind != AssignNew {
		t.Errorf("a face with no cluster in range created %v, want AssignNew; a "+
			"first appearance is the NORMAL outcome, not a fallback", got.Kind)
	}
	if got.ClusterID == 0 {
		t.Error("AssignNew must return the id of the cluster it created; " +
			"returning zero hands the caller a row that does not exist")
	}
}

// TestAssign_JoinsWhenExactlyOneClusterIsClearlyNearest is the common case, and
// it has a boundary worth pinning: "clearly" means the margin, not the
// distance.
//
// Two clusters at 0.10 and 0.40 from the candidate, threshold 0.5. Both are in
// range. The margin is 0.30, which is a real margin, so this joins -- and the
// test exists to prove the join is not accidentally the ambiguous branch.
func TestAssign_JoinsWhenExactlyOneClusterIsClearlyNearest(t *testing.T) {
	s := newStage(0.5)
	near := s.seedCluster(0.10)
	far := s.seedCluster(0.40)

	got := s.assign(Scalar(0.0, "f"))

	if got.Kind != AssignJoin {
		t.Fatalf("a face 0.10 from one cluster and 0.40 from another, margin "+
			"0.30 at threshold 0.5, produced %v; that is a clear winner", got.Kind)
	}
	if got.ClusterID != near {
		t.Errorf("joined cluster %d, want %d (the nearer one); cluster %d was "+
			"0.40 away", got.ClusterID, near, far)
	}
	if len(got.Candidates) != 2 {
		t.Errorf("recorded %d candidates, want 2; the decision must record what "+
			"it rejected, or a user cannot tell what it was weighed against",
			len(got.Candidates))
	}
}

// TestAssign_TwoCloseClustersAreAmbiguousNotAGuess is the property the feature
// turns on.
//
// Both clusters within the threshold, and within the separation margin of each
// other. The nearer one is not meaningfully nearer: the ranking is inside the
// noise, so joining it is a decision with no evidence behind it.
func TestAssign_TwoCloseClustersAreAmbiguousNotAGuess(t *testing.T) {
	s := newStage(0.15)
	a := s.seedCluster(0.10)
	b := s.seedCluster(0.14)

	// The margin is 0.04 against a required 0.15 * 0.5 = 0.075, so the
	// ranking is inside the noise. Asserting the arithmetic here rather than
	// restating the numbers means a change to DefaultSeparation fails the test
	// with a diagnosis instead of silently flipping the expected outcome.
	const (
		gap       = 0.14 - 0.10
		requiredM = 0.15 * DefaultSeparation
	)
	if gap >= requiredM {
		t.Fatalf("test setup is wrong: a gap of %.3f already clears the "+
			"required margin of %.3f, so the ambiguous branch is unreachable",
			gap, requiredM)
	}

	got := s.assign(Scalar(0.0, "f"))

	if got.Kind != AssignAmbiguous {
		t.Fatalf("a face %.2f from one cluster and %.2f from another (gap %.2f, "+
			"required margin %.3f) produced %v; the ranking is inside the noise, "+
			"so joining either is a guess presented as a finding",
			0.10, 0.14, gap, requiredM, got.Kind)
	}
	if len(got.Candidates) != 2 {
		t.Errorf("an ambiguous decision recorded %d candidates, want 2; the "+
			"whole point is that the UI shows BOTH and a 'this is two people' "+
			"action", len(got.Candidates))
	}
	// Both must be named, and they must be the two real ones.
	seen := map[int64]bool{}
	for _, c := range got.Candidates {
		seen[c.ClusterID] = true
	}
	if !seen[a] || !seen[b] {
		t.Errorf("candidates were %v, want clusters %d and %d; a candidate list "+
			"missing one of them leaves the user unable to choose", got.Candidates, a, b)
	}
	// And the engine must not have picked a winner.
	if got.ClusterID != 0 {
		t.Errorf("an ambiguous decision named cluster %d as the destination; "+
			"naming one is how a guess gets committed downstream", got.ClusterID)
	}
}

// TestAssign_AmbiguousIsNotResolvedByConfidence: the temptation is to break the
// tie with the cluster's own confidence or size, and both are wrong.
//
// Size is the classic mistake: the bigger cluster is the bigger cluster
// because it was assigned more faces, including faces assigned to it wrongly.
// Letting size break the tie makes the first wrong merge the most attractive
// place to put the next wrong merge, and the corpus converges on a small number
// of giant wrong clusters.
func TestAssign_AmbiguousIsNotResolvedByClusterSize(t *testing.T) {
	s := newStage(0.15)
	small := s.seedCluster(0.14)
	big := s.seedCluster(0.10)
	// Make `big` genuinely bigger.
	for i := 0; i < 5; i++ {
		s.forceMembers(big, Scalar(0.10, "big"+string(rune('A'+i))))
	}

	got := s.assign(Scalar(0.0, "f"))

	if got.Kind != AssignAmbiguous {
		t.Errorf("a 6-face cluster 0.10 away beat a 1-face cluster 0.14 away "+
			"and the face was joined to %v; size is not evidence, and it is "+
			"evidence that compounds -- a wrong merge makes the wrong cluster "+
			"more attractive for the next wrong merge", got.ClusterID)
	}
	if got.ClusterID == big || got.ClusterID == small {
		t.Errorf("an ambiguous outcome named cluster %d", got.ClusterID)
	}
}

// TestAssign_AnAmbiguousOutcomeLeavesTheClustersAlone: the engine's job is to
// record the conflict, not to act on it.
//
// A stage that moved the face into the nearer cluster AND marked it ambiguous
// has already made the decision it says it cannot make, and the state on the
// cluster is a lie about what happened.
func TestAssign_AnAmbiguousOutcomeLeavesTheClustersAlone(t *testing.T) {
	s := newStage(0.15)
	a := s.seedCluster(0.10)
	b := s.seedCluster(0.14)

	before := s.size(a) + s.size(b)
	s.assign(Scalar(0.0, "f"))

	if after := s.size(a) + s.size(b); after != before {
		t.Errorf("cluster membership changed from %d to %d during an AMBIGUOUS "+
			"assign; the face must be recorded as a candidate for review, not "+
			"added to a cluster the engine said it could not choose", before, after)
	}
}

// TestAssign_TheSameFaceIsNotAssignedTwice: a rescan must not produce a second
// decision, or the ambiguity queue fills with duplicates of one conflict and the
// real signal is buried.
func TestAssign_TheSameFaceIsNotAssignedTwice(t *testing.T) {
	s := newStage(0.5)
	s.seedCluster(0.10)

	first := s.assign(Scalar(0.0, "same"))
	second := s.assign(Scalar(0.0, "same"))

	if second.Kind != AssignAlreadySeen {
		t.Errorf("a rescan of a face already assigned produced %v, want "+
			"AssignAlreadySeen; a second decision duplicates the record and "+
			"buries the real conflicts in the review queue", second.Kind)
	}
	if second.ClusterID != first.ClusterID {
		t.Errorf("the rescan reported cluster %d, the original reported %d",
			second.ClusterID, first.ClusterID)
	}
}

// TestAssign_CandidatesAreReportedInDistanceOrder: the UI lists both, and a
// reviewer's eye goes to the first one. An unsorted list makes the engine's
// silent preference the default answer even when the state says ambiguous.
func TestAssign_CandidatesAreReportedInDistanceOrder(t *testing.T) {
	// Separation 0: every candidate ties on margin, so the order must come
	// from the distances rather than from a tie-break.
	s := newStage(0.5, withSeparation(0))
	s.seedCluster(0.40)
	s.seedCluster(0.10)
	s.seedCluster(0.25)

	got := s.assign(Scalar(0.0, "f"))
	if len(got.Candidates) != 3 {
		t.Fatalf("got %d candidates, want 3", len(got.Candidates))
	}
	for i := 1; i < len(got.Candidates); i++ {
		if got.Candidates[i].Distance < got.Candidates[i-1].Distance {
			t.Errorf("candidates are not in distance order: %v",
				got.Candidates)
			break
		}
	}
}

// TestAssign_TheMarginIsMeasuredAgainstTheThresholdNotTheRunnerUp is the
// inversion the implementation comment warns about, and it survived the first
// mutation pass because every earlier test had a runner-up close to the
// threshold -- where the two formulas happen to agree.
//
// The bug it produces is a margin that SHRINKS as the runner-up gets farther.
// Two clusters at 0.40 and 0.41, threshold 0.5: the absolute gap is 0.01, which
// as a fraction of the runner-up is 0.024 and as a fraction of the threshold is
// 0.02. Those are close, so a threshold-relative test with a small gap does not
// separate them.
//
// Separate them with a runner-up far from the threshold and a gap in the
// middle: clusters at 0.20 and 0.30, threshold 0.5.
//
//	threshold-relative: gap 0.10, required 0.5*0.5 = 0.25 -> NOT enough -> ambiguous
//	runner-up-relative: required 0.30*0.5 = 0.15 -> 0.10 < 0.15 -> also ambiguous
//
// Still agreeing. The formulas cross where second == threshold. Push the
// runner-up above the threshold's useful range -- but then it would not be a
// candidate at all, since candidates must be within the threshold.
//
// So with any two in-range candidates, second <= threshold, and the two
// formulas CAN differ: runner-up-relative requires gap >= second*sep, which is
// SMALLER than threshold*sep whenever second < threshold. So the runner-up
// formula is always the more permissive of the two inside the candidate set,
// and the two agree only when second == threshold.
//
// Take second just under the threshold: clusters at 0.02 and 0.49, threshold
// 0.5.
//
//	threshold-relative: gap 0.47, required 0.25 -> enough -> JOIN
//	runner-up-relative: gap 0.47, required 0.49*0.5 = 0.245 -> enough -> JOIN
//
// The crossing point is second = threshold, and below that the runner-up
// formula demands LESS. So the permissive direction is: a runner-up far below
// the threshold gets a smaller required margin under the buggy formula, letting
// the engine choose between two clusters it can barely distinguish from the
// face. Take second = 0.10, best = 0.08, threshold 0.5:
//
//	threshold-relative: gap 0.02, required 0.25 -> AMBIGUOUS
//	runner-up-relative: gap 0.02, required 0.05 -> 0.02 < 0.05 -> AMBIGUOUS
//
// Both ambiguous again, because the gap is small either way. The real
// discriminator needs gap BETWEEN the two required margins: 0.25*second < gap
// < 0.25*threshold. With threshold 0.5 that is 0.25*second < gap < 0.25. For
// gap = 0.10, need 0.25*second < 0.10, so second < 0.40.
//
// Clusters at 0.10 and 0.20, threshold 0.5, gap 0.10:
//
//	threshold-relative: required 0.25, gap 0.10 -> AMBIGUOUS
//	runner-up-relative: required 0.20*0.5 = 0.10, gap 0.10 -> 0.10 >= 0.10 -> JOIN
//
// That is the pair. Identical inputs, opposite outcomes, decided entirely by
// which number the margin is a fraction of.
func TestAssign_TheMarginIsMeasuredAgainstTheThresholdNotTheRunnerUp(t *testing.T) {
	const (
		threshold = 0.5
		best      = 0.10
		second    = 0.20
		gap       = second - best
	)

	// The two formulas, spelled out. If the implementation changes which one
	// it uses, exactly one of these assertions fails and names the change.
	thresholdRelative := threshold * DefaultSeparation
	runnerUpRelative := second * DefaultSeparation

	if gap >= thresholdRelative {
		t.Fatalf("test setup is wrong: a gap of %.3f already clears the "+
			"threshold-relative margin of %.3f", gap, thresholdRelative)
	}
	if gap < runnerUpRelative {
		t.Fatalf("test setup is wrong: the runner-up-relative margin is "+
			"%.3f and the gap %.3f already clears it, so the two formulas "+
			"agree and the test cannot tell them apart", runnerUpRelative, gap)
	}

	// Seeds at exactly best and second, so the distances to the face at 0.0
	// are best and second.
	s := newStage(threshold)
	near := s.seedCluster(best)
	far := s.seedCluster(second)

	got := s.assign(Scalar(0.0, "f"))

	if got.Kind != AssignAmbiguous {
		t.Errorf("clusters %.2f and %.2f from the face, threshold %.2f, gap "+
			"%.2f: got %v into cluster %d (near=%d, far=%d). The required "+
			"margin is %.3f as a fraction of the THRESHOLD, and the gap is "+
			"%.3f. A margin taken from the runner-up would be %.3f, which the "+
			"gap clears -- and that formula shrinks the required margin as the "+
			"runner-up gets farther, so the engine ends up choosing between "+
			"clusters it cannot distinguish from the face.",
			best, second, threshold, gap, got.Kind, got.ClusterID, near, far,
			thresholdRelative, gap, runnerUpRelative)
	}
}
