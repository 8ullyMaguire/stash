package cluster

// Step 2.4b.1: sample planning.
//
// # Why this is a function and not an inline loop
//
// The defect it exists to prevent is invisible in every other output. A plan
// that takes the first N frames, or the first N frames of a coarse stride, has
// exactly the right sample count, every index is a real frame, and nothing
// errors. The library is indexed as though it had been watched end to end.
//
// So the plan is a pure function with no I/O, which is what makes it testable
// at all, and its contract is stated in terms of where the LAST sample lands
// rather than how many there are.

// PlanSamples spreads a sample budget evenly across a file.
//
// duration is the file's frame count; budget is the ceiling on samples. The
// samples are i * duration / budget for i in [0, budget), clamped to the file's
// length and de-duplicated.
//
// Even spread rather than the first N, and rather than a coarse stride over the
// same interval: both of those concentrate the budget at the start, so a face in
// the last act of a long video is never looked at.
//
// # The spread INCLUDES the final frame, and that is the decision
//
// The obvious formulation -- i * duration / budget for i in [0, budget) -- puts
// the last sample at (budget-1)/budget of the file. With 5 samples over 1000
// frames that is frame 800, leaving the final fifth of the file unwatched while
// the plan looks textbook-correct: even gaps, the right count, no error.
//
// That formulation samples the INTERVALS [0, 1000/budget), and the tail -- the
// last partial interval -- falls outside all of them. So the samples are spread
// over [0, duration-1] instead, which is the spec's phrase read literally:
// evenly from the first frame to the last.
//
// The cost is real and worth stating rather than hiding: including the endpoint
// makes the stride (duration-1)/(budget-1) rather than duration/budget, so
// coverage is a hair less even in the interior -- 0, 249, 499, 749, 999 rather
// than 0, 250, 500, 750, 1000. Buying the last frame with one frame of interior
// unevenness is the right trade for a budget whose entire purpose is not
// missing the end of a file, and the gaps still differ by at most one, which is
// what TestGapsAreEven asserts.
//
// The one-sample case is the exception and is handled separately: a single
// sample goes at frame 0, the opening frame, which is the one frame guaranteed
// to be worth looking at (a face, or a post-credit title card). Spreading one
// sample across a range would mean picking a midpoint and gambling on the film
// having a face in its middle.
//
// When budget >= duration every frame is taken exactly once. Repeats would
// spend the budget on frames already looked at, which is the only thing a
// budget exists to avoid.
func PlanSamples(duration, budget int) []int {
	// Degenerate inputs return nothing rather than guessing. A negative
	// duration is a caller bug, and a plan built from it would be arithmetic on
	// nonsense that happens not to panic.
	if duration <= 0 || budget <= 0 {
		return nil
	}

	// budget == 1 is a special case, not the general formula applied: see the
	// doc comment. Handled before the endpoint spread, which would otherwise
	// put the single sample on the final frame.
	if budget == 1 {
		return []int{0}
	}

	if budget >= duration {
		out := make([]int, duration)
		for i := range out {
			out[i] = i
		}
		return out
	}

	// Spans [0, duration-1] inclusive, so the last sample is the last frame.
	last := duration - 1
	span := budget - 1

	out := make([]int, 0, budget)
	for i := 0; i < budget; i++ {
		// Integer division, deliberately. float64 is exact for these values
		// only up to 2^53, and a frame count large enough to matter is not
		// beyond that -- but the integer form also makes the evenness property
		// obvious, where float rounding would leave gaps that differ by one in
		// the last place and a test asserting exact equality would flake.
		frame := i * last / span

		// Clamp rather than skip: on the last iteration frame can reach
		// duration-1 at most, but a clamp is what guarantees no index is ever
		// past the end if the arithmetic above ever changes. An out-of-range
		// index makes the caller seek past the end and report a frame nobody
		// sampled.
		if frame >= duration {
			frame = duration - 1
		}
		if frame < 0 {
			frame = 0
		}

		// De-duplicate. Integer division can map two iterations to the same
		// frame when budget is close to duration, and a repeat spends the
		// budget twice on one frame.
		if len(out) > 0 && out[len(out)-1] == frame {
			continue
		}
		out = append(out, frame)
	}
	return out
}
