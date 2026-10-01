package cluster

import "testing"

// Step 2.4b.1: the sample budget.
//
// A per-file sample budget is a ceiling on work, and a file that needs more
// samples than the budget allows must have them spread evenly from the first
// frame to the last.
//
// The two obvious wrong answers both concentrate the budget at the start:
//
//   - the first N frames of the requested interval
//   - the requested interval at a coarser stride
//
// Both sample the opening minutes of a three-hour file and stop, so a face in
// the last act is never looked at. The result is not a detectable error: the
// sample count is exactly right, the frames are all real frames, and the
// detector returned no error. The library is indexed as if it had been watched
// end to end.
//
// That is why almost every test here asserts where the LAST sample lands. An
// assertion about the count is satisfied by a plan that covered the first minute
// and stopped, which is the defect.

// ---------------------------------------------------------------------------
// The property
// ---------------------------------------------------------------------------

// TestSampleBudgetIsSpreadEndToEnd is the core assertion.
//
// A budget of 5 over 1000 frames must place its last sample near frame 1000, not
// near frame 5.
func TestSampleBudgetIsSpreadEndToEnd(t *testing.T) {
	got := PlanSamples(1000, 5)

	if len(got) != 5 {
		t.Fatalf("planned %d samples, want 5", len(got))
	}
	last := got[len(got)-1]
	// Exact, not a loose bound. 999 is the final frame, and any weaker
	// assertion here is satisfied by the i*duration/budget formulation that
	// stops at 800.
	if last != 999 {
		t.Errorf("last sample is frame %d of 1000, want 999 (the final frame); "+
			"the budget must reach the END of the file. A plan whose last "+
			"sample is near the beginning never looks at the last act, and "+
			"reports no error doing it", last)
	}
}

// TestSampleBudgetLastSampleLandsWhereEvenSpreadPutsIt is the precise form, and
// the one the plan asks for.
//
// The spread is over [0, duration-1] INCLUSIVE, so the last sample is always the
// final frame: frame = i * (duration-1) / (budget-1).
//
// The first version of this table expected i * duration / budget -- the obvious
// formulation, sampling the interval [0, 1000/5) -- and the test caught it
// putting the last sample at 800 of 1000. That plan has even gaps, the right
// count, and no error, and it leaves the final fifth of a file unwatched. The
// gap between the two formulations is exactly the tail interval, and a budget
// whose purpose is not missing the end of a file must include the endpoint.
func TestSampleBudgetLastSampleLandsWhereEvenSpreadPutsIt(t *testing.T) {
	tests := []struct {
		name     string
		duration int
		budget   int
		want     []int
	}{
		// Spread over [0, 99] with 5 samples: 0, 24, 49, 74, 99.
		{"exact division", 100, 5, []int{0, 24, 49, 74, 99}},
		// [0, 999] with 4: 0, 333, 666, 999.
		{"even, four samples", 1000, 4, []int{0, 333, 666, 999}},
		// [0, 999] with 3: 0, 499, 999. Integer division must not drift.
		{"uneven, three samples", 1000, 3, []int{0, 499, 999}},
		// Two samples: the ends. The whole point in its smallest form -- one at
		// the opening frame, one at the final frame.
		{"two samples are the ends", 1000, 2, []int{0, 999}},
		// A single sample goes at the start, which is the only defensible
		// choice: it is the frame most likely to contain a face and a
		// post-credit title card.
		{"one sample is the first frame", 1000, 1, []int{0}},
		// Budget exceeds the file: take every frame, no repeats.
		{"budget larger than duration", 3, 10, []int{0, 1, 2}},
		// Budget exactly equals the file: every frame, no repeats.
		{"budget exactly equals duration", 5, 5, []int{0, 1, 2, 3, 4}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanSamples(tc.duration, tc.budget)

			if len(got) != len(tc.want) {
				t.Fatalf("PlanSamples(%d, %d) = %v, want %v", tc.duration, tc.budget, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("sample %d = frame %d, want %d (full plan %v, want %v)",
						i, got[i], tc.want[i], got, tc.want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The two wrong answers, named and excluded
// ---------------------------------------------------------------------------

// TestPlanSamplesIsNotTheFirstNFrames is the "first N of the interval" defect,
// excluded explicitly rather than left to the end-to-end assertion.
func TestPlanSamplesIsNotTheFirstNFrames(t *testing.T) {
	got := PlanSamples(1000, 5)

	// The wrong plan would be {0,1,2,3,4}.
	if got[1] == 1 && got[4] == 4 {
		t.Fatalf("plan %v takes the first %d frames; a face in the last act "+
			"is then never looked at", got, len(got))
	}
	if got[0] != 0 {
		t.Errorf("first sample = %d, want 0; the opening frame is the one "+
			"frame guaranteed to be worth looking at", got[0])
	}
}

// TestPlanSamplesIsNotAUniformSubsetOfTheInterval is the coarser-stride defect.
//
// Take every k-th frame of [0, 1000) until the budget runs out. That is
// {0, k, 2k, 3k, 4k} for a stride that fills the budget -- and it looks
// plausible, is monotonically increasing, and has the right count. It is still
// the first N samples of a stride, so the coverage is 4k of 1000, not 1000.
func TestPlanSamplesIsNotAUniformSubsetOfTheInterval(t *testing.T) {
	const duration, budget = 1000, 5
	got := PlanSamples(duration, budget)

	// Work out the stride the samples imply and check it actually spans the
	// file. A correct plan's last sample is within one interval of the end; a
	// truncated stride's is not.
	last := got[len(got)-1]
	if float64(last) < 0.8*float64(duration) {
		t.Errorf("last sample %d is under 80%% of the way through a %d-frame "+
			"file; that is a truncated stride, not an even spread", last, duration)
	}

	// And the gaps must be even. An even spread has one gap size; a truncated
	// stride has one gap size too, so that alone does not separate them --
	// which is exactly why the end-anchored assertion above exists.
	first := got[0]
	if first != 0 {
		t.Errorf("first sample %d, want 0", first)
	}
	_ = last
}

// TestGapsAreEven is the "spread evenly" half, stated directly.
func TestGapsAreEven(t *testing.T) {
	got := PlanSamples(1000, 7)
	if len(got) != 7 {
		t.Fatalf("got %d samples, want 7", len(got))
	}

	gaps := make([]int, len(got)-1)
	for i := 1; i < len(got); i++ {
		gaps[i-1] = got[i] - got[i-1]
	}
	for i, g := range gaps {
		// Integer division means gaps differ by at most one.
		if g != gaps[0] && g != gaps[0]+1 {
			t.Errorf("gap %d is %d, but gap 0 is %d; an even spread has gaps "+
				"differing by at most one, not by arbitrary amounts (gaps %v)",
				i, g, gaps[0], gaps)
		}
	}
}

// ---------------------------------------------------------------------------
// Degenerate inputs
// ---------------------------------------------------------------------------

// TestPlanSamplesOnDegenerateInput: none of these may panic, and none may
// return a frame index outside the file.
//
// An index past the end is not a no-op: the caller would seek and either fail
// the whole file or, worse, clamp to the last frame and report it as sampled.
func TestPlanSamplesOnDegenerateInput(t *testing.T) {
	tests := []struct {
		name     string
		duration int
		budget   int
		// wantEmpty: a file with no frames has nothing to sample, and returning
		// nothing is the only correct answer. The first version of this table
		// asserted a positive budget always yields samples, which is right for
		// every real file and wrong for a zero-length one -- and it is wrong in
		// the safe direction, so a plan that "succeeded" on a zero-frame file
		// would be inventing a frame index nobody can seek to.
		wantEmpty bool
	}{
		{name: "zero duration, zero budget", duration: 0, budget: 0, wantEmpty: true},
		{name: "zero duration, positive budget", duration: 0, budget: 10, wantEmpty: true},
		{name: "positive duration, zero budget", duration: 100, budget: 0, wantEmpty: true},
		{name: "negative budget", duration: 100, budget: -1, wantEmpty: true},
		{name: "negative duration", duration: -1, budget: 5, wantEmpty: true},
		{name: "both negative", duration: -1, budget: -1, wantEmpty: true},
		{name: "one frame, one sample", duration: 1, budget: 1},
		{name: "one frame, many samples", duration: 1, budget: 100},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanSamples(tc.duration, tc.budget)

			if tc.wantEmpty && len(got) != 0 {
				t.Errorf("planned %v for a file with no frames", got)
			}
			if !tc.wantEmpty && len(got) == 0 {
				t.Errorf("a file with %d frames and a budget of %d planned "+
					"nothing", tc.duration, tc.budget)
			}
			for i, f := range got {
				if f < 0 {
					t.Errorf("sample %d is frame %d, which is negative", i, f)
				}
				if tc.duration > 0 && f >= tc.duration {
					t.Errorf("sample %d is frame %d, past the end of a %d-frame "+
						"file; the caller would seek past the end and report a "+
						"frame nobody sampled", i, f, tc.duration)
				}
			}
			// Strictly increasing, or the budget is being spent twice on one
			// frame.
			for i := 1; i < len(got); i++ {
				if got[i] <= got[i-1] {
					t.Errorf("sample %d (%d) does not advance past sample %d (%d); "+
						"the budget would be spent re-sampling one frame",
						i, got[i], i-1, got[i-1])
				}
			}
		})
	}
}

// TestPlanSamplesNeverRepeatsAFrame: on a file shorter than the budget, every
// frame appears once. Repeats would spend the budget on frames already looked
// at, which is the whole thing the budget exists to avoid.
func TestPlanSamplesNeverRepeatsAFrame(t *testing.T) {
	got := PlanSamples(4, 100)

	if len(got) != 4 {
		t.Fatalf("got %d samples from a 4-frame file, want 4", len(got))
	}
	seen := map[int]bool{}
	for _, f := range got {
		if seen[f] {
			t.Errorf("frame %d planned twice: %v", f, got)
		}
		seen[f] = true
	}
}
