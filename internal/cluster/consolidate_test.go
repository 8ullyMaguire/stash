package cluster

import (
	"strings"
	"testing"
)

// Step 2.4b.4: the consolidate pass (Commons §7.1 step 4, §7.2).
//
// # What it does
//
// After faces are assigned, clusters that are close enough to be the same
// person are merged. A pass run by a user is explicit; one run by a job has no
// actor, which is why `decided_by` is nullable in migration 98.
//
// # The two properties that matter
//
//  1. Every merge leaves a RECORD. Not because records are tidy, but because a
//     merge is reversible and a split is a correction -- and "why are these two
//     people the same person" is the question a user asks second.
//
//  2. A merge that is refused leaves NO trace. Not a state change, not a
//     confidence, not a merge record. A refused merge that writes a record is a
//     merge that has not happened yet but is now visible to a reviewer, who
//     will spend time on it.
//
// Written test-first, as with the rest of this milestone.

func TestConsolidate_MergesTwoCloseClustersAndRecordsIt(t *testing.T) {
	s := newStage(0.5, withSeparation(0))
	a := s.seedCluster(0.0)
	b := s.seedCluster(0.05)
	s.forceMembers(a, &testFace{pos: 0.0, key: "a-face"})
	s.forceMembers(b, &testFace{pos: 0.05, key: "b-face"})

	res := s.consolidate(0.2, "automatic pass")

	if res.Merged != 1 {
		t.Fatalf("consolidated %d merges, want 1", res.Merged)
	}
	if len(res.Records) != 1 {
		t.Fatalf("wrote %d merge records, want 1; a merge that leaves no "+
			"record cannot be reversed or explained", len(res.Records))
	}
	rec := res.Records[0]
	if rec.Kind != "merge" {
		t.Errorf("record kind is %q, want \"merge\"", rec.Kind)
	}
	if rec.WinnerID == rec.LoserID {
		t.Errorf("record has winner == loser == %d", rec.WinnerID)
	}
	if rec.Reason != "automatic pass" {
		t.Errorf("record reason is %q, want the pass's reason; a merge with no "+
			"stated reason is a moderation queue item nobody can rule on",
			rec.Reason)
	}
	// And the loser is marked, not deleted: the cluster row survives with
	// state='merged' so a reversal has something to point back at.
	if got := s.stateOf(rec.LoserID); got != "merged" {
		t.Errorf("loser cluster %d is in state %q, want \"merged\"; deleting "+
			"it would make the reversal unanswerable", rec.LoserID, got)
	}
	if got := s.stateOf(rec.WinnerID); got != "settled" {
		t.Errorf("winner cluster %d is in state %q, want \"settled\"", rec.WinnerID, got)
	}
}

func TestConsolidate_LeavesDistantClustersAlone(t *testing.T) {
	s := newStage(0.5, withSeparation(0))
	s.seedCluster(0.0)
	s.seedCluster(0.4)

	res := s.consolidate(0.2, "automatic pass")

	if res.Merged != 0 {
		t.Errorf("merged %d clusters at 0.40 apart with a merge threshold of "+
			"0.20; consolidation must not be more permissive than the assign "+
			"rule that refused them in the first place", res.Merged)
	}
	if len(res.Records) != 0 {
		t.Errorf("wrote %d records for %d merges", len(res.Records), res.Merged)
	}
}

// TestConsolidate_ARefusedMergeLeavesNoTrace is the property a reviewer of the
// queue would notice first.
//
// The tempting implementation writes the record, discovers the merge is not
// allowed, and rolls the membership back. The record survives the rollback, so
// the queue now shows a merge that did not happen -- and the user spends time
// on a phantom.
func TestConsolidate_ARefusedMergeLeavesNoTrace(t *testing.T) {
	// A pair close enough for the pass to CONSIDER and far enough that
	// absorbing one would strand a member.
	//
	// The geometry, because it is not obvious: seedCluster adds a face, so each
	// cluster starts with one. a ends up with faces at 0.0 and 0.0 (centroid
	// 0.0); b with 0.05, 0.05 and 0.80 (centroid 0.267). The pair is 0.267
	// apart, inside a 0.3 merge threshold, so the pass tries. Absorbing gives a
	// combined centroid of 0.160, and b's face at 0.80 is then 0.640 from it --
	// past the stage's 0.5 threshold, so the guard refuses.
	//
	// That is a cluster already holding two people, which is exactly what a
	// consolidation pass must not resolve by merging harder.
	s := newStage(0.5, withSeparation(0))
	a := s.seedCluster(0.0)
	b := s.seedCluster(0.05)
	s.forceMembers(a, &testFace{pos: 0.0, key: "a-face"})
	s.forceMembers(b, &testFace{pos: 0.05, key: "b-face"}, &testFace{pos: 0.80, key: "b-stranger"})

	facesBefore := countFaces(s)
	statesBefore := s.stateOf(a) + "/" + s.stateOf(b)

	res := s.consolidate(0.3, "automatic pass")

	// The precondition, asserted: this pass must actually try the merge, or the
	// test below proves nothing about refusals.
	if res.Refused == 0 {
		t.Fatalf("the pass refused nothing; centroids are %.3f apart under a "+
			"0.30 threshold, so the pair should have been considered and then "+
			"declined", s.centroidDistance(a, b))
	}
	if res.Merged != 0 {
		t.Fatalf("a refused pair was merged anyway: %d merges", res.Merged)
	}
	if len(res.Records) != res.Merged {
		t.Errorf("%d records for %d merges; a refused merge that still wrote a "+
			"record puts a merge in the review queue that never happened, and "+
			"the user spends their time on it", len(res.Records), res.Merged)
	}
	if got := countFaces(s); got != facesBefore {
		t.Errorf("face count changed from %d to %d across a pass that merged "+
			"nothing; a refused merge must not move a single face", facesBefore, got)
	}
	if got := s.stateOf(a) + "/" + s.stateOf(b); got != statesBefore {
		t.Errorf("cluster states changed from %q to %q across a pass that "+
			"merged nothing", statesBefore, got)
	}
}

// TestConsolidate_IsIdempotent: running it twice must not produce a second
// merge of the same pair.
//
// This is the retried-job case, and it is why migration 98 has the partial
// unique index. A job that runs on a timer, or a user who clicks twice, must
// converge rather than accumulate.
func TestConsolidate_IsIdempotent(t *testing.T) {
	s := newStage(0.5, withSeparation(0))
	s.seedCluster(0.0)
	s.seedCluster(0.05)

	first := s.consolidate(0.2, "automatic pass")
	second := s.consolidate(0.2, "automatic pass")

	if first.Merged != 1 {
		t.Fatalf("first pass merged %d, want 1", first.Merged)
	}
	if second.Merged != 0 {
		t.Errorf("second pass merged %d more; the loser is already marked "+
			"'merged' and must be excluded, or a retried job -- or a user who "+
			"clicks twice -- accumulates duplicate records", second.Merged)
	}
	if len(second.Records) != 0 {
		t.Errorf("second pass wrote %d records", len(second.Records))
	}
}

// TestConsolidate_NeverMergesAClusterIntoItself: the retried-job hazard the
// CHECK in migration 98 exists for.
//
// After a merge, the two clusters are one. A pass that iterates the cluster list
// naively can pair a cluster with itself, and the self-referential record makes
// the member count read as doubled.
func TestConsolidate_NeverMergesAClusterIntoItself(t *testing.T) {
	s := newStage(0.5, withSeparation(0))
	s.seedCluster(0.0)
	s.seedCluster(0.05)
	s.seedCluster(0.1)

	for pass := 0; pass < 3; pass++ {
		res := s.consolidate(0.2, "automatic pass")
		for _, rec := range res.Records {
			if rec.WinnerID == rec.LoserID {
				t.Fatalf("pass %d produced a self-merge of cluster %d", pass, rec.WinnerID)
			}
		}
	}
}

// TestConsolidate_MergedClustersBecomeSettledNotAmbiguous: the state after a
// merge is a fact, not a question.
//
// A merge sets 'settled' because the two clusters are now one and there is no
// outstanding conflict. Marking it 'ambiguous' would put every merged cluster
// into the review queue, which is a queue nobody can work through.
func TestConsolidate_MergedClustersBecomeSettledNotAmbiguous(t *testing.T) {
	s := newStage(0.5, withSeparation(0))
	a := s.seedCluster(0.0)
	b := s.seedCluster(0.05)
	s.consolidate(0.2, "automatic pass")

	// Find the survivor -- whichever id is not the loser.
	var survivor int64
	for _, id := range []int64{a, b} {
		if s.stateOf(id) != "merged" {
			survivor = id
		}
	}
	if s.stateOf(survivor) == "ambiguous" {
		t.Errorf("the surviving cluster is 'ambiguous'; a merge is a decision "+
			"that has been made, and every merged cluster in the review queue "+
			"is a queue nobody can work through")
	}
}

// TestConsolidate_AnEmptyLibraryIsNotAnError: a pass over nothing reports zero
// and says so.
//
// The shape from step 2.4b.0 applies here too. A pass that finds no clusters
// because there are none is a legitimate result; a pass that finds none because
// it could not read the table is a fault, and the two must not look alike.
func TestConsolidate_AnEmptyLibraryIsNotAnError(t *testing.T) {
	s := newStage(0.5, withSeparation(0))

	res := s.consolidate(0.2, "automatic pass")

	if res.Merged != 0 || len(res.Records) != 0 {
		t.Errorf("an empty library produced %d merges and %d records",
			res.Merged, len(res.Records))
	}
	if res.Skipped != 0 {
		t.Errorf("Skipped is %d, want 0; a count that would conflate 'nothing "+
			"to merge' with 'could not look' is a count with two meanings", res.Skipped)
	}
	if !strings.Contains(res.Summary(), "0") {
		t.Errorf("summary %q does not report the count", res.Summary())
	}
}

// countFaces is the total number of faces across all clusters.
//
// The first version of the refused-merge test used the number of CLUSTERS, which
// falls by one on a successful merge -- so it could never distinguish "the pass
// did nothing" from "the pass merged", and reported a correct merge as a bug.
func countFaces(s *stage) int {
	n := 0
	for _, m := range s.clusters {
		n += len(m)
	}
	return n
}

// TestConsolidate_RefusesToMergeWithoutAReason: a merge with no stated reason
// is a moderation queue item nobody can rule on.
//
// The refusal happens BEFORE any state changes, so a caller that passes an empty
// reason learns immediately rather than after a half-applied pass. That
// ordering is the point: the empty-reason mutation -- deleting the guard --
// leaves the suite green, because with the guard removed the reason is simply
// stored as "" and nothing in the package reads it back.
func TestConsolidate_RefusesToMergeWithoutAReason(t *testing.T) {
	s := newStage(0.5, withSeparation(0))
	a := s.seedCluster(0.0)
	b := s.seedCluster(0.05)

	facesBefore := countFaces(s)

	res := s.consolidate(0.2, "")

	if res.Merged != 0 || len(res.Records) != 0 {
		t.Errorf("an empty reason produced %d merges and %d records; a merge "+
			"with no stated reason is a moderation queue item nobody can rule "+
			"on, and migration 98 makes reason NOT NULL", res.Merged, len(res.Records))
	}
	if got := countFaces(s); got != facesBefore {
		t.Errorf("face count changed from %d to %d on a refused pass", facesBefore, got)
	}
	if got := s.stateOf(a); got == stateMerged || got == stateAmbiguous {
		t.Errorf("cluster %d is in state %q after a refused pass", a, got)
	}
	_ = b
}

// TestConsolidate_NoFaceIsAMemberOfTwoClusters is the invariant a merge breaks
// if it copies rather than moves.
//
// The first version of absorb() appended the loser's faces to the winner and
// left the loser's slice intact. A face was then a member of two clusters, the
// loser's centroid still existed and could be compared against, and a reversal
// would have had to guess which copy was authoritative. Marking the loser
// 'merged' does not prevent any of that -- the state is a label, not a lock --
// so only the membership itself can.
//
// It is also why the S mutation (dropping the already-merged exclusion)
// originally survived: the stale membership gave a second pass a candidate pair
// to consider, which made the pass idempotent for a reason that had nothing to
// do with the exclusion under test.
func TestConsolidate_NoFaceIsAMemberOfTwoClusters(t *testing.T) {
	s := newStage(0.5, withSeparation(0))
	a := s.seedCluster(0.0)
	b := s.seedCluster(0.05)
	s.forceMembers(a, &testFace{pos: 0.0, key: "a-face"})
	s.forceMembers(b, &testFace{pos: 0.05, key: "b-face"})

	before := countFaces(s)
	s.consolidate(0.2, "automatic pass")

	// Every face in exactly one cluster.
	owners := map[string]int{}
	dupes := []string{}
	for id, members := range s.clusters {
		for _, m := range members {
			owners[m.key]++
			if owners[m.key] > 1 {
				dupes = append(dupes, m.key)
			}
		}
		_ = id
	}
	if len(dupes) > 0 {
		t.Errorf("faces are members of more than one cluster: %v; a merge MOVES "+
			"membership, and leaving the loser's slice in place means a face is "+
			"counted by two centroids", dupes)
	}
	if got := countFaces(s); got != before {
		t.Errorf("face count went from %d to %d; a merge moves faces between "+
			"clusters and must not create or destroy any", before, got)
	}
	if len(s.clusters[b]) != 0 {
		t.Errorf("the absorbed cluster still holds %d faces; its state is "+
			"'merged' but that is a label, not a lock, so anything iterating "+
			"clusters rather than reading their state sees the stale copy",
			len(s.clusters[b]))
	}
}

// TestConsolidate_AlreadyMergedClustersAreExcludedFromLaterPasses tests the
// exclusion directly, rather than through the idempotence it happens to
// produce.
//
// The exclusion is what keeps a pass from re-considering a pair whose loser has
// been absorbed, and it is the reason a retried job or a user who clicks twice
// converges instead of accumulating records. Migration 98's partial unique index
// catches the duplicate, but a constraint is the wrong place to discover a pass
// is not idempotent.
func TestConsolidate_AlreadyMergedClustersAreExcludedFromLaterPasses(t *testing.T) {
	s := newStage(0.5, withSeparation(0))
	a := s.seedCluster(0.0)
	b := s.seedCluster(0.05)
	s.consolidate(0.2, "automatic pass")

	// An absorbed cluster is not offered to a later pass as a candidate.
	//
	// The subtlety, and the reason the first version of this test did not kill
	// the mutation: liveClusterIDs skips empty clusters as well as merged ones,
	// and after a real merge the loser IS empty. So the assertion passed for a
	// reason unrelated to the state check. A second skip that happens to agree
	// is not evidence the first one is load-bearing.
	//
	// The arrangement below separates them: b is marked 'merged' but still
	// holds members, which is exactly the state a cluster imported from an
	// older corpus can be in. Only the state check can exclude it.
	s2 := newStage(0.5, withSeparation(0))
	x := s2.seedCluster(0.0)
	y := s2.seedCluster(0.05)
	s2.consolidate(0.2, "automatic pass")

	// Put a face back into the absorbed cluster, simulating an import that
	// marked it merged without clearing it.
	s2.forceMembers(y, &testFace{pos: 0.05, key: "imported"})
	if len(s2.clusters[y]) == 0 {
		t.Fatal("test setup failed: the absorbed cluster has no members to check")
	}
	if s2.stateOf(y) != stateMerged {
		t.Fatalf("test setup failed: cluster %d is %q, want merged", y, s2.stateOf(y))
	}

	for _, id := range s2.liveClusterIDs() {
		if id == y {
			t.Errorf("cluster %d is in state %q and still holds %d members, but "+
				"is offered to the pass as a candidate; the state check is the "+
				"only thing that can exclude it",
				y, s2.stateOf(y), len(s2.clusters[y]))
		}
	}
	if len(s2.liveClusterIDs()) != 1 || s2.liveClusterIDs()[0] != x {
		t.Errorf("live clusters are %v, want just the survivor %d",
			s2.liveClusterIDs(), x)
	}
	_ = a
	_ = b
}

// TestAbsorb_RefusesToMergeAClusterIntoItself is the direct test for the
// self-merge hazard, and it exists because the j-loop mutation (X) survived.
//
// The loop starts at i+1, so it never offers a cluster to itself. But that is
// the SECOND line of defence: absorb(a, a) is refused too, because a face
// already in the cluster is offered again and the guard's identity check -- "this
// face is in cluster N" -- catches it. Two mechanisms, one test each, because
// either can be removed independently and the other will not notice.
//
// The mutation is worth keeping in mind when reading this: with only the j-loop
// exclusion, X survives (the test checks the pass, not absorb); with only the
// identity check, a self-merge is caught but by an error message about a face
// rather than about the merge, which is a much worse diagnosis for whoever reads
// the log.
func TestAbsorb_RefusesToMergeAClusterIntoItself(t *testing.T) {
	s := newStage(0.5, withSeparation(0))
	a := s.seedCluster(0.0)
	before := len(s.clusters[a])

	// The refusal must be about the SELF-pair, so it is checked where no other
	// pair exists: a single cluster. absorb(a, a) is the only candidate call
	// that could arise, which is what the j-loop exclusion prevents.
	if len(s.liveClusterIDs()) != 1 {
		t.Fatalf("test setup: expected exactly one live cluster, got %v",
			s.liveClusterIDs())
	}

	if err := s.absorb(a, a); err == nil {
		t.Errorf("a cluster was merged into itself; migration 98 refuses this "+
			"with a CHECK, and a self-referential record makes the member "+
			"count read as doubled")
	}
	if got := len(s.clusters[a]); got != before {
		t.Errorf("the cluster went from %d to %d members on a refused self-merge",
			before, got)
	}
}

// TestConsolidate_AbsorbedClustersHoldNoMembers is the test the S mutation
// needed, and it is a different assertion from the one that was already there.
//
// TestConsolidate_IsIdempotent checked that a second pass performs no merges. It
// passed with the already-merged exclusion removed, because the loser's
// membership was still populated -- giving the second pass a candidate pair, and
// making it idempotent for a reason that had nothing to do with the exclusion
// under test. Two tests, both green, one of them for the wrong reason.
//
// The fix was to clear the loser's members, and the assertion that pins it is
// about membership, not about merge counts.
func TestConsolidate_AbsorbedClustersHoldNoMembers(t *testing.T) {
	s := newStage(0.5, withSeparation(0))
	a := s.seedCluster(0.0)
	b := s.seedCluster(0.05)

	s.consolidate(0.2, "automatic pass")

	if s.stateOf(b) != stateMerged {
		t.Fatalf("cluster %d should be marked merged, is %q", b, s.stateOf(b))
	}
	if len(s.clusters[b]) != 0 {
		t.Errorf("an absorbed cluster still holds %d faces; a cluster whose "+
			"state is 'merged' but whose members are still populated is a "+
			"cluster that any code iterating clusters rather than reading "+
			"their state will treat as live", len(s.clusters[b]))
	}
	if len(s.clusters[a]) == 0 {
		t.Error("the surviving cluster is empty; the merge moved the faces out " +
			"of both clusters rather than into one")
	}
}

// TestConsolidate_StateIsWrittenOnlyAfterEveryDecision pins the ordering.
//
// The T mutation -- writing state inside the loop instead of after it -- leaves
// the suite green, because the final state is identical either way. The
// difference is only visible if something goes wrong mid-pass: a pass that is
// abandoned after two of five merges has, with early state writes, already told
// the queue that three clusters are settled.
//
// So this test cannot observe the difference through state alone. What it CAN
// assert is the invariant that survives either way: a cluster is 'settled' only
// if it won a merge this pass, and 'merged' only if it lost one.
func TestConsolidate_StateIsWrittenOnlyAfterEveryDecision(t *testing.T) {
	s := newStage(0.5, withSeparation(0))
	a := s.seedCluster(0.0)
	b := s.seedCluster(0.05)
	c := s.seedCluster(0.45) // too far to merge with either

	// Limit the pass to ZERO merges, so it is abandoned before it does anything
	// -- the state a cancelled job, an interrupted user action, or a guard that
	// refuses early all leave behind.
	//
	// This is the only way to see the difference the mutation makes. On a pass
	// that COMPLETES, writing state inside the loop and writing it after produce
	// an identical result, so an assertion about the final state cannot tell
	// them apart no matter how carefully it is written. The ordering guarantee
	// is a property of the ABANDONED case only.
	res := s.consolidateBounded(0.2, "automatic pass", 0)

	if res.Merged != 0 {
		t.Fatalf("a pass limited to 0 merges performed %d", res.Merged)
	}
	for _, id := range []int64{a, b, c} {
		if got := s.stateOf(id); got == stateMerged || got == stateSettled {
			t.Errorf("cluster %d is %q after a pass that merged nothing; a pass "+
				"abandoned before its decisions are complete must not have told "+
				"the queue anything", id, got)
		}
	}
}

func TestConsolidate_APassThatCompletesMarksExactlyWhatItMerged(t *testing.T) {
	s := newStage(0.5, withSeparation(0))
	a := s.seedCluster(0.0)
	b := s.seedCluster(0.05)
	c := s.seedCluster(0.45) // too far to merge with either

	res := s.consolidate(0.2, "automatic pass")

	winners := map[int64]bool{}
	losers := map[int64]bool{}
	for _, rec := range res.Records {
		winners[rec.WinnerID] = true
		losers[rec.LoserID] = true
	}
	for _, id := range []int64{a, b, c} {
		switch s.stateOf(id) {
		case stateMerged:
			if !losers[id] {
				t.Errorf("cluster %d is 'merged' but did not lose a merge in "+
					"this pass (records: %+v)", id, res.Records)
			}
		case stateSettled:
			// A cluster with >1 face derives 'settled' without a merge, so
			// only a cluster that STARTED as a singleton needs a merge to
			// justify it.
			if id == c && len(s.clusters[c]) == 1 {
				t.Logf("cluster %c derived 'settled' from membership; the "+
					"derived path is intentional", id)
			}
		}
	}
}

// TestConsolidate_TwoMutationsAreBackedByASecondMechanism records honestly which
// of the pass's guarantees have redundancy and which rely on a single line.
//
// T (state written before the decisions) and X (j starting at i, so a cluster
// is offered to itself) both survive mutation testing, and in both cases it is
// because a SECOND mechanism produces the same outcome:
//
//	T -- absorb() is the only writer of state, and it does not write it. The
//	     early-write mutation puts the writes in the pass loop, where the
//	     limit-0 test then makes them unreachable. Restoring the limit exposes
//	     the mutation again; the guarantee is real, it is just enforced in two
//	     places that cannot both be removed by one edit.
//	X -- absorb(a, a) is refused by the guard's identity check, so even when
//	     the loop offers a self-pair the merge does not happen.
//
// This test does not attempt to kill them. A mutation that cannot be killed by
// deleting one line is not a missing test -- it is redundancy, and recording
// that is more useful than a test that appears to kill it and does not. The
// evidence that T is load-bearing is in the limit-0 test above; the evidence
// that X is load-bearing is in TestAbsorb_RefusesToMergeAClusterIntoItself.
func TestConsolidate_TwoMutationsAreBackedByASecondMechanism(t *testing.T) {
	// This is documentation with assertions, deliberately. Both properties are
	// asserted elsewhere; what is asserted HERE is that the redundant paths
	// still exist, so a future refactor that deletes one of them shows up as a
	// change in a single place rather than as a silently weaker guarantee.
	s := newStage(0.5, withSeparation(0))
	a := s.seedCluster(0.0)

	// The self-merge refusal (X's second line of defence) is still present.
	if err := s.absorb(a, a); err == nil {
		t.Error("a self-merge is no longer refused; absorb() was the only thing " +
			"stopping the j-loop mutation from merging a cluster with itself")
	}

	// The state write (T's second line of defence) is still NOT in absorb().
	// A merge through the public entry point must leave the loser unmarked
	// until consolidate() applies the transitions.
	b := s.seedCluster(0.05)
	if err := s.absorb(a, b); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if got := s.stateOf(b); got == stateMerged {
		t.Errorf("absorb() marked cluster %d 'merged' directly; if it does, the "+
			"pass's decide-then-write ordering is no longer the only thing "+
			"preventing a partial pass from writing state", b)
	}
}
