package cluster

import (
	"fmt"
	"sort"
)

// Step 2.4b.6: candidate selection. The plan calls this "ANN"; see the note
// below on why it is exact.
//
// # Why exact, and why the name is a misnomer
//
// There is no sqlite-vec, no HNSW and no IVF in the tree, and adding a native
// extension for a corpus that fits in memory would trade a correctness property
// for a speed property nobody needs yet.
//
// Exactness is a CORRECTNESS property here, not a performance choice. A recall
// miss in the candidate stage is invisible: the over-merge guard, the assign
// margin and the consolidate threshold all operate on whatever candidates they
// are handed, so a face that should have been a candidate is simply not
// considered. The outcome is a cluster that is wrong in a way that looks like a
// judgement -- the distance would have been 0.31, the threshold is 0.5, and
// nobody logs the face that was never looked at.
//
// If this is ever replaced with an approximate index, the replacement needs a
// recall test against this implementation as ground truth. A claim that the
// index is "close enough" is not evidence.

// Neighbour is one candidate: a row index into the caller's slice, and the
// distance to the query.
//
// An INDEX rather than a cluster id, because this stage runs before any cluster
// is known. The candidate set is a set of faces; what they belong to is decided
// later, by the assign stage, which is the thing that has the cluster map.
type Neighbour struct {
	Index    int
	Distance float64
}

// SkipReport records rows this stage declined to use.
//
// It exists because the alternative is a silent skip, and a silent skip is the
// same failure shape as the missing model in step 2.4b.0: an index that quietly
// omits rows reads as a smaller, more confident answer. A caller that surfaces
// this can tell a user "12 of 40,000 faces could not be compared", which is
// actionable; nothing else can.
type SkipReport struct {
	// Skipped is how many rows were not considered.
	Skipped int

	// Reasons is one non-empty string per skip, in row order. A skip with no
	// reason is an unexplained omission, so the two counts are required to
	// agree -- and a test holds them to it.
	Reasons []string
}

func (r *SkipReport) skip(reason string) {
	r.Skipped++
	r.Reasons = append(r.Reasons, reason)
}

// nearest returns the k closest vectors to query, nearest first.
//
// The signature is deliberately split in two:
//
//	nearest(index, query, k)              -- no report, skips silently
//	nearest(index, query, k, report)      -- reports what it skipped
//
// A caller that does not care about skips should not have to construct a report
// to get one, and a caller that DOES care must be able to say so at the call
// site rather than by remembering. The two-argument form is not "the unchecked
// one"; it is the one for callers that have already validated the index.
func nearest(index [][]float32, query []float32, k int, report ...*SkipReport) []Neighbour {
	var rep *SkipReport
	if len(report) > 0 {
		rep = report[0]
	}

	// A bad QUERY is a fault, not a skip. See the asymmetry note: a bad row can
	// be skipped because the index is a large accumulated body of data and one
	// bad element should not stop the search, but there is nothing to return
	// without a query that could be evaluated. Returning a best-effort answer
	// computed against an unusable query is the "found nothing" failure from
	// step 2.4b.0, one level up.
	if err := ValidateEmbedding(query); err != nil {
		// DELIBERATELY not recorded in the report.
		//
		// SkipReport counts rows that were not COMPARED. A bad query means no row
		// was compared, and recording it as a skip would describe a row as
		// unusable when every row in the index is fine -- so an operator reading
		// "3 of 40000 faces could not be compared" would go looking for three
		// corrupt embeddings that do not exist, while the real fault, the query,
		// is in a field named "row".
		//
		// The empty return is the signal, and the caller distinguishes it from a
		// genuine "nothing is nearby" by asking whether it had a usable query --
		// which it knows, because it built it.
		return nil
	}

	// An empty index is a normal state -- a library with no faces yet -- and must
	// not read as a broken install.
	if len(index) == 0 {
		return nil
	}
	// k <= 0 asks for nothing. Not an error: a caller with a budget of zero has
	// said what it means.
	if k <= 0 {
		return nil
	}

	// Partial selection by a bounded insertion into a k-sized buffer, rather
	// than sorting all n.
	//
	// O(n*k) is worse than O(n log n) for large k, and the crossover matters
	// only past roughly n = k*ln(k) -- at which point the caller is holding
	// hundreds of thousands of vectors and should be reaching for an index
	// rather than tuning this. For the k this milestone uses (a handful of
	// candidate clusters) the buffer wins by a wide margin, and it is a single
	// pass, so a poisoned row cannot abort a long sort half-way.
	//
	// The test asserts the result against a brute-force fixture rather than
	// asserting a complexity class, because a complexity class is not something
	// a test can observe. What it CAN observe is completeness, which is exactly
	// what a truncated scan would break.
	out := make([]Neighbour, 0, k)
	for i, v := range index {
		d, err := CosineDistance(query, v)
		if err != nil {
			// Skip and record. See the SkipReport doc for why a single bad row
			// must not stop the other 40,000.
			if rep != nil {
				rep.skip(fmt.Sprintf("row %d: %v", i, err))
			}
			continue
		}

		if len(out) < k {
			out = append(out, Neighbour{Index: i, Distance: d})
			out = insertionSort(out)
			continue
		}
		// Beyond k, only a strictly-nearer row displaces the current worst. Using
		// strict '<' and never '<' on the worst means a tie at the boundary is
		// resolved by the earlier row, which is what makes the whole thing
		// deterministic.
		if d < out[len(out)-1].Distance {
			out[len(out)-1] = Neighbour{Index: i, Distance: d}
			out = insertionSort(out)
		}
	}

	return out
}

// insertionSort keeps the k-sized buffer ordered by distance, ties by index.
//
// A hand-rolled insertion sort rather than sort.Slice for two reasons. The
// buffer is k elements, where the sort's setup cost dominates, and -- more
// importantly -- sort.Slice is NOT STABLE, so it cannot be used to make a tie
// deterministic without a second pass. Ties are a real case: the same person in
// two frames the detector scored identically, or a duplicated import. An
// unstable tie-break means the same library produces different clusters on
// different runs, which makes every clustering bug unreproducible.
func insertionSort(s []Neighbour) []Neighbour {
	for i := 1; i < len(s); i++ {
		cur := s[i]
		j := i - 1
		for j >= 0 && less(cur, s[j]) {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = cur
	}
	return s
}

// less is the total order: nearer first, then lower index.
//
// A total order, not a partial one, is what makes the output deterministic. With
// only "nearer first", two equal distances compare false in both directions and
// the result depends on the order rows happened to arrive in.
func less(a, b Neighbour) bool {
	if a.Distance != b.Distance {
		return a.Distance < b.Distance
	}
	return a.Index < b.Index
}

// SortNeighbours orders a slice of candidates in place. Exported because the
// assign stage receives candidates from several sources -- this function, and a
// future approximate index -- and they must be ordered the same way.
func SortNeighbours(n []Neighbour) {
	sort.Slice(n, func(i, j int) bool { return less(n[i], n[j]) })
}
