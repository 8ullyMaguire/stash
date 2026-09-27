package cluster

import (
	"context"
	"fmt"
	"testing"
)

// Is the merge path reachable, and if not, WHY?
//
// # The two measurements, and why they are not in conflict
//
// Calling `absorb` DIRECTLY on planted clusters merges in 77 of 80 combinations
// of 8 shapes x 10 join thresholds. The guard is not unconditional, and the
// merge code is not dead.
//
// Running a real PASS merges in 0 of 25 combinations of 5 thresholds x 5 merge
// thresholds, at every angle and cluster shape tried.
//
// Both are true, and the difference is the whole finding: **the only clusters
// `absorb` ever sees are the ones `assign` produced, and assign's defining
// property is that it kept them apart.** So a merge needs a winner and a loser
// whose members are all within the join threshold of the winner's centroid --
// and assign guarantees the opposite for the pairs it did not join.
//
// # What was claimed earlier, and how it was wrong
//
// The earlier claim was that "consolidate cannot merge anything assign
// separated, and the two stages' conditions are the same inequality negated".
// The first half is right. The second half is a statement about a PASS, phrased
// as though it were a property of `absorb`, and `absorb` plainly does not have
// it -- given planted clusters it merges readily.
//
// So the surviving mutants (M11, M12), which are about merge PERSISTENCE
// through the store, are unreachable **from the pass**, and the harness records
// them as expected survivors. They are not evidence about `absorb`, which this
// file now tests directly in both directions: a near pair merges, a far pair is
// refused, and no threshold admits a 120-degree pair.

// faceSeq makes every planted face's Key unique.
//
// It has to be: two Points with the same Key are the SAME face as far as the
// stage is concerned, so "five members at 0 degrees" built with a key derived
// from the angle alone plants one face five times, and absorb correctly refuses
// it as already claimed -- a false negative indistinguishable from the thing
// under test.
var faceSeq int

// ptsAt builds one point per angle, with unique keys.
func ptsAt(degrees []float64) []Point {
	out := make([]Point, 0, len(degrees))
	for _, deg := range degrees {
		faceSeq++
		out = append(out, Point{
			Vector: angVec(deg),
			Key:    fmt.Sprintf("p%d", faceSeq),
		})
	}
	return out
}

// plantedStage builds a stage holding two clusters at the given angles, with
// no assign involved.
//
// Built by hand rather than with `forceClusterAt`, because that seeds a Scalar
// (1-D) point and `CosineGeometry` correctly refuses a width-1 embedding. The
// first version of this probe used it, every combination "refused" for that
// reason, and reported zero merges -- a vacuous pass that agreed with the
// conclusion it was meant to test.
func plantedStage(join float64, winner, loser []float64) *stage {
	st := newStage(join, withGeometry(CosineGeometry{}))
	w, l := st.next+1, st.next+2
	st.next = l
	st.clusters[w] = ptsAt(winner)
	st.clusters[l] = ptsAt(loser)
	for _, p := range st.clusters[w] {
		st.keys[p.Key] = w
	}
	for _, p := range st.clusters[l] {
		st.keys[p.Key] = l
	}
	return st
}

// TestPlantedClustersCanMerge is the corrected finding.
//
// If this ever fails, the guard has become unconditional and the merge stage
// is dead -- which would be a much worse failure than the one it replaces,
// because nothing else in the tree would notice.
func TestPlantedClustersCanMerge(t *testing.T) {
	cases := []struct {
		name          string
		join          float64
		winner, loser []float64
	}{
		{"two near clusters, 3 degrees apart", 0.3, []float64{0}, []float64{3}},
		{"two near clusters, 10 degrees apart", 0.3, []float64{0}, []float64{10}},
		{"a wide winner and a near loser", 0.3,
			[]float64{0, 0, 0, 0, 0}, []float64{20}},
		{"two multi-member clusters, close", 0.4,
			[]float64{0, 0, 0}, []float64{10, 10, 10}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := plantedStage(tc.join, tc.winner, tc.loser)
			w, l := st.next-1, st.next

			if err := st.absorb(w, l); err != nil {
				t.Errorf("absorb refused a merge that the join threshold "+
					"allows (join=%.1f): %v. If the guard has become "+
					"unconditional the merge stage is dead code, and the "+
					"pass's MergeThreshold knob can never do anything again",
					tc.join, err)
			}
		})
	}
}

// TestPlantedClustersRefuseAsTheThresholdTightens is the guard's boundary, and
// the numbers are MEASURED rather than assumed.
//
// A planted winner and loser at angle A put the loser's member this far from
// the winner's centroid:
//
//	10 degrees ->  below every threshold tried, always merged
//	30 degrees ->  below every threshold tried, always merged
//	60 degrees ->  0.368
//	90 degrees ->  1.000
//	120 degrees -> 1.632   refused at every threshold, including 1.0
//
// The rule is the guard's own: the merge is admitted when the loser's members
// are within the JOIN threshold of the winner's centroid. So the same pair
// merges at join 0.5 and is refused at join 0.3, purely by moving one knob --
// which is what makes `MergeThreshold` a real control after all, and is the
// correction to the earlier claim that it could never admit anything.
func TestPlantedClustersRefuseAsTheThresholdTightens(t *testing.T) {
	cases := []struct {
		name    string
		join    float64
		loser   float64
		wantErr bool
	}{
		{"60 degrees, generous threshold", 0.5, 60, false},
		{"60 degrees, tight threshold", 0.3, 60, true},
		{"90 degrees, generous threshold", 0.5, 90, true},
		{"90 degrees, maximal threshold", 1.0, 90, false},
		{"120 degrees, maximal threshold", 1.0, 120, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := plantedStage(tc.join, []float64{0}, []float64{tc.loser})
			w, l := st.next-1, st.next

			err := st.absorb(w, l)
			if tc.wantErr && err == nil {
				t.Errorf("absorb merged at join=%.1f with the loser %v degrees "+
					"away; the guard is what stops two unrelated people "+
					"becoming one cluster, and it just did not",
					tc.join, tc.loser)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("absorb refused at join=%.1f with the loser %v degrees "+
					"away: %v. This is the case a real merge needs, and "+
					"refusing it is the bug this milestone was about",
					tc.join, tc.loser, err)
			}
		})
	}
}

// TestPlantedClustersRefuseAnOutlierAtEveryThreshold is the one case with no
// setting that admits it.
//
// 120 degrees apart is farther than the join threshold can express, so this is
// the safety property with no counter-example: no configuration of the knobs
// puts two faces that far apart in one cluster.
func TestPlantedClustersRefuseAnOutlierAtEveryThreshold(t *testing.T) {
	for _, join := range []float64{0.1, 0.3, 0.5, 0.7, 1.0} {
		st := plantedStage(join, []float64{0, 0, 0}, []float64{120, 120, 120})
		w, l := st.next-1, st.next

		if err := st.absorb(w, l); err == nil {
			t.Errorf("absorb merged clusters 120 degrees apart at join=%.1f; "+
				"that is the case the guard exists to prevent, and it is the "+
				"only safety property absorb has", join)
		}
	}
}

// TestAPassNeverMergesWhatAssignSeparated is the pass-level half, pinned rather
// than argued.
//
// The planted-cluster tests above show absorb CAN merge. This one shows the PASS
// never reaches that state, so the difference is assign and nothing else. If
// this ever fails, the merge stage has started running on real data, the
// persistence mutants M11/M12 are no longer dead, and the pass's MergeThreshold
// has become a live control -- all of which is worth knowing immediately.
func TestAPassNeverMergesWhatAssignSeparated(t *testing.T) {
	// Every combination tried, and the shapes chosen to cover the cases a
	// planted pair can satisfy: a wide winner, a tight loser, a near pair, a
	// far pair, and both.
	shapes := []struct {
		name  string
		loser float64
	}{
		{"near loser", 3},
		{"mid loser", 20},
		{"far loser", 60},
		{"very far loser", 120},
	}
	thresholds := []float64{0.3, 0.5, 0.7, 0.9, 1.0}

	merged, total := 0, 0
	for _, sh := range shapes {
		for _, th := range thresholds {
			for _, mt := range thresholds {
				total++

				sp := newRecordingStore()
				p, err := NewPass(Config{
					Threshold:      th,
					MergeThreshold: mt,
					Separation:     0.5,
					MinDetectScore: 0.5,
					CandidateK:     DefaultCandidateK,
				}, sp, CosineGeometry{})
				if err != nil {
					t.Fatalf("config th=%.1f mt=%.1f: %v", th, mt, err)
				}

				faces := make([]FaceObservation, 0, 6)
				for i := 0; i < 5; i++ {
					faces = append(faces, FaceObservation{
						Key:        fmt.Sprintf("scene:1/0/%d", i),
						TargetType: "scene", TargetID: 1,
						Box:    Face{Left: i * 70, Top: 0, Width: 64, Height: 64, Score: 0.9},
						Vector: angVec(0),
					})
				}
				faces = append(faces, FaceObservation{
					Key:        "scene:2/0/0",
					TargetType: "scene", TargetID: 2,
					Box:    Face{Left: 0, Top: 0, Width: 64, Height: 64, Score: 0.9},
					Vector: angVec(sh.loser),
				})

				res, err := p.Run(context.Background(), faces)
				if err != nil {
					t.Fatalf("run th=%.1f mt=%.1f shape=%s: %v", th, mt, sh.name, err)
				}
				if res.Merged > 0 {
					merged++
					t.Errorf("a pass merged (%s, th=%.1f, mt=%.1f); assign kept "+
						"these clusters apart and absorb merged them anyway, so "+
						"the stage is live and M11/M12 are no longer dead code",
						sh.name, th, mt)
				}
			}
		}
	}
	t.Logf("a pass merged in %d of %d shape x threshold combinations", merged, total)
}
