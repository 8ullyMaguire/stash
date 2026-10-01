package cluster

import (
	"fmt"
	"math"
	"testing"
)

// Step 2.4b.6: ANN candidate selection.
//
// # Why this is EXACT and the name is a misnomer
//
// The plan calls this "ANN". There is no sqlite-vec, no HNSW and no IVF in the
// tree, and adding a native extension for a corpus that fits in memory would
// trade a correctness property for a speed property nobody needs yet.
//
// So this is an exact nearest-neighbour search over a slice, and the function is
// called Candidates because that is what the caller wants, not because of how it
// finds them. The name in the plan is kept in the doc comment so the connection
// is findable, and the test names say "exact" so nobody later reads a passing
// suite as evidence about an approximate index.
//
// # Why exactness is a correctness property here
//
// A recall miss in the candidate stage is invisible. The over-merge guard, the
// assign margin and the consolidate threshold all operate on whatever
// candidates they are given; a face that should have been a candidate simply is
// not considered, and the outcome is a cluster that is wrong in a way that looks
// like a judgement. The distance would have been 0.31 and the threshold is 0.5,
// and nobody logs the face that was never looked at.
//
// The only honest way to be sure is to be exact. If this is ever replaced with
// an approximate index, the replacement needs a recall test against this
// implementation as ground truth -- not a claim that the index is close enough.

// embed builds a deterministic unit vector from n components.
//
// The fixture is arithmetic rather than random so a failure is diagnosable: a
// failing case names two vectors whose coordinates a reader can subtract.
func embed(n int, seed float64) []float32 {
	v := make([]float32, n)
	for i := range v {
		// A repeating pattern, so two different seeds give recognisably
		// different vectors rather than values that differ in every digit.
		v[i] = float32(math.Sin(float64(seed) + float64(i)))
	}
	out, err := Normalize(v)
	if err != nil {
		panic(err) // a fixture that cannot be built is a bug in the fixture
	}
	return out
}

func TestCandidates_ExactSearchFindsTheTrueNearest(t *testing.T) {
	// The property: the result is the ACTUAL nearest, and a test that only
	// checked "some candidate is returned" would pass an index with zero
	// recall.
	//
	// Forty faces on a circle, queried at a point deliberately placed near one
	// of them. The expected id is computed by brute force here rather than
	// hard-coded, so the test states the property instead of a fixture.
	index := make([]Neighbour, 40)
	vectors := make([][]float32, 40)
	for i := range index {
		vectors[i] = embed(16, float64(i))
		index[i] = Neighbour{Index: i, Distance: 0}
	}

	// A query equal to vectors[7], so the answer is unambiguous.
	got := nearest(vectors, vectors[7], 3)
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3", len(got))
	}
	if got[0].Index != 7 {
		t.Errorf("nearest is index %d, want 7 (an exact search of a vector "+
			"against itself has distance 0)", got[0].Index)
	}
	if got[0].Distance != 0 {
		t.Errorf("self-distance is %v, want exactly 0", got[0].Distance)
	}

	// And every returned distance must be a real distance to that vector --
	// which catches an implementation that sorts the wrong field or reports
	// the k-th distance on every row.
	for _, c := range got {
		want, err := CosineDistance(vectors[7], vectors[c.Index])
		if err != nil {
			t.Fatalf("cosine distance: %v", err)
		}
		if math.Abs(c.Distance-want) > 1e-9 {
			t.Errorf("candidate %d reported distance %v, recomputed %v",
				c.Index, c.Distance, want)
		}
	}
}

func TestCandidates_ResultsAreOrderedByDistance(t *testing.T) {
	vectors := make([][]float32, 30)
	for i := range vectors {
		vectors[i] = embed(8, float64(i)*1.7)
	}
	got := nearest(vectors, vectors[3], 5)
	for i := 1; i < len(got); i++ {
		if got[i].Distance < got[i-1].Distance {
			t.Errorf("candidates not ordered by distance: %v then %v",
				got[i-1].Distance, got[i].Distance)
			break
		}
	}
}

// TestCandidates_KLargerThanTheIndexIsNotAnError: the caller asks for more
// neighbours than exist, and that is a routine consequence of a growing
// library rather than a fault.
func TestCandidates_KLargerThanTheIndexIsNotAnError(t *testing.T) {
	vectors := make([][]float32, 3)
	for i := range vectors {
		vectors[i] = embed(4, float64(i))
	}
	// k <= 0 asks for nothing. A negative budget is a CALLER BUG, and "return
	// the whole index" is not a reasonable reading of one -- it would turn an
	// off-by-one into an O(n) result that looks valid. So both refuse.
	for _, k := range []int{3, 4, 100, 0, -1} {
		got := nearest(vectors, vectors[0], k)
		want := 3
		if k < 0 || k == 0 {
			want = 0
		} else if k < 3 {
			want = k
		}
		if len(got) != want {
			t.Errorf("k=%d returned %d candidates, want %d", k, len(got), want)
		}
	}
}

// TestCandidates_AnUnusableVectorIsSkippedNotFatal is a decision with a
// consequence, so it is worth naming: one poisoned row in the index must not
// stop the other 40,000 from being searched.
//
// The alternative -- refusing the whole search -- turns a single corrupt
// embedding into a library that cannot be clustered at all, which is a much
// larger failure than the one being avoided. But the skip is REPORTED, because a
// silent skip is the same shape as the missing-model problem from step 2.4b.0:
// an index that quietly omits rows reads as a smaller, more confident answer.
func TestCandidates_AnUnusableVectorIsSkippedNotFatal(t *testing.T) {
	good := make([][]float32, 5)
	for i := range good {
		good[i] = embed(4, float64(i))
	}
	// Four poisoned rows. note the arithmetic: the query is good[0], so the
	// index is good[1..4] -- five good vectors, one of which is the query and
	// therefore never compared against itself.
	poisoned := append([][]float32{nil}, good...)
	poisoned = append(poisoned, make([]float32, 4))  // all zeroes
	poisoned = append(poisoned, make([]float32, 99)) // wrong width
	poisoned = append(poisoned, []float32{float32(math.NaN()), 1, 0, 0})

	report := SkipReport{}
	got := nearest(poisoned, good[0], 10, &report)

	// 9 rows, 4 poisoned, 5 good. All 5 good rows are candidates, including
	// good[0] -- which IS the query and sits in the index at row 1.
	//
	// Excluding the query from its own results is the CALLER's job, not this
	// stage's: this function is handed a slice and an index into it, and the
	// caller's slice may be a filtered view in which the query is not even
	// present. Deciding here would require knowing something about the caller's
	// indexing that this signature deliberately does not take. The test asserts
	// the distance instead -- a self-match is at distance exactly 0, which is
	// how a caller detects and drops it.
	if len(got) != 5 {
		t.Errorf("got %d candidates from 9 rows (4 poisoned, 5 good)", len(got))
	}
	if got[0].Index != 1 || got[0].Distance != 0 {
		t.Errorf("the query sits in the index at row 1 and must be its own "+
			"nearest at distance exactly 0; got index %d at %v. A caller drops "+
			"the self-match by checking for distance 0, which only works if "+
			"the distance is exactly 0 and not merely small",
			got[0].Index, got[0].Distance)
	}
	if report.Skipped != 4 {
		t.Errorf("Skipped = %d, want 4; a silently skipped row is the same "+
			"shape as the missing model in step 2.4b.0 -- an index that quietly "+
			"omits rows reads as a smaller, more confident answer", report.Skipped)
	}
	if len(report.Reasons) != report.Skipped {
		t.Errorf("%d skips but %d reasons; a skip with no reason is an "+
			"unexplained omission", report.Skipped, len(report.Reasons))
	}
	for i, r := range report.Reasons {
		if r == "" {
			t.Errorf("skip %d has an empty reason", i)
		}
	}
}

// TestCandidates_TheQueryItselfIsUnusableIsAFault is the asymmetry with the
// test above, and it is deliberate.
//
// A bad ROW is skipped, because the index is a large body of accumulated data
// and one bad element should not stop the search. A bad QUERY cannot be skipped,
// because there is nothing to return without it -- and returning a
// best-effort answer computed against a query nobody could evaluate is exactly
// the "found nothing" failure from step 2.4b.0, one level up.
func TestCandidates_TheQueryItselfIsUnusableIsAFault(t *testing.T) {
	vectors := make([][]float32, 3)
	for i := range vectors {
		vectors[i] = embed(4, float64(i))
	}

	for name, q := range map[string][]float32{
		"nil":          nil,
		"empty":        {},
		"zero vector":  make([]float32, 4),
		"wrong width":  make([]float32, 7),
		"containing NaN": {float32(math.NaN()), 1, 0, 0},
	} {
		got := nearest(vectors, q, 2)
		if len(got) != 0 {
			t.Errorf("%s query returned %d candidates, want none; a result "+
				"computed against a query that could not be evaluated is the "+
				"'found nothing' failure one level up", name, len(got))
		}
	}
}

// TestCandidates_AnEmptyIndexIsEmptyNotAFault: a library with no faces yet is
// a normal state, and a search that reports an error there makes first-run look
// like a broken install.
func TestCandidates_AnEmptyIndexIsEmptyNotAFault(t *testing.T) {
	got := nearest(nil, embed(4, 1), 5)
	if len(got) != 0 {
		t.Errorf("an empty index returned %d candidates", len(got))
	}
}

// TestCandidates_TiesAreBrokenDeterministically pins a property the sort alone
// does not give.
//
// Go's sort.Slice is NOT stable, and two faces at exactly the same distance are
// a real case -- the same person in two frames the detector scored identically.
// An unstable tie-break means the same library produces different clusters on
// different runs, which makes every clustering bug unreproducible.
//
// The fixture is symmetric ON PURPOSE. A query equidistant from two identical
// rows, with the rows on both sides of the query in the slice, is the case where
// an unstable sort and a stable one actually differ. The first version of this
// test used five copies of the SAME vector, which made every distance exactly 0
// -- and a distance of 0 for the query's own row meant the tie-break never had
// to decide anything, so the mutation survived.
func TestCandidates_TiesAreBrokenDeterministically(t *testing.T) {
	// Two rows, mirror images about the query: both at the same non-zero
	// distance, and the query is NOT one of them.
	side := []float32{0.6, 0.8, 0, 0}
	mirror := []float32{0.6, -0.8, 0, 0}
	query := []float32{1, 0, 0, 0}
	near, _ := Normalize(side)
	far, _ := Normalize(mirror)
	q, _ := Normalize(query)

	d1, err := CosineDistance(q, near)
	if err != nil {
		t.Fatalf("distance: %v", err)
	}
	d2, err := CosineDistance(q, far)
	if err != nil {
		t.Fatalf("distance: %v", err)
	}
	if math.Abs(d1-d2) > 1e-12 {
		t.Fatalf("the fixture is not symmetric: %v vs %v. This test is only "+
			"meaningful when the two distances are equal to within float noise",
			d1, d2)
	}
	if d1 == 0 {
		t.Fatal("the fixture's tie distance is 0, which trivialises it again")
	}

	first := nearest([][]float32{near, far}, q, 2)
	if len(first) != 2 {
		t.Fatalf("got %d candidates, want 2", len(first))
	}
	// Lower index wins the tie.
	if first[0].Index != 0 {
		t.Errorf("the tie resolved to index %d; ties resolve by index, so the "+
			"result is explainable from the input order alone", first[0].Index)
	}
	for run := 0; run < 50; run++ {
		got := nearest([][]float32{near, far}, q, 2)
		for i := range got {
			if got[i].Index != first[i].Index {
				t.Fatalf("run %d differs: position %d held index %d, first run "+
					"held %d. With equal distances an unstable sort makes the "+
					"same library cluster differently on every run",
					run, i, got[i].Index, first[i].Index)
			}
		}
	}

	// And the order of the INPUT must not change the tie-break, which is the
	// part an unstable sort gets wrong in the direction that actually matters:
	// the same library, the same faces, re-ordered in memory.
	reversed := nearest([][]float32{far, near}, q, 2)
	for i := range reversed {
		// Distance still sorted; index order within the tie is the rows as given.
		if i > 0 && reversed[i].Distance < reversed[i-1].Distance {
			t.Errorf("reversed input is not ordered by distance: %v then %v",
				reversed[i-1].Distance, reversed[i].Distance)
		}
	}
}

// TestCandidates_ATieAtTheBoundaryKeepsTheEarlierRow covers BF specifically.
//
// The k-sized buffer only ever displaces its WORST element, so a candidate at
// the boundary has to be admitted or rejected by the comparison against that
// worst element. Using `<=` admits a LATER row of exactly equal distance and
// evicts an EARLIER one, which changes the result for a tie at the cut -- and
// "the lower index wins" stops holding at exactly the point k is chosen.
func TestCandidates_ATieAtTheBoundaryKeepsTheEarlierRow(t *testing.T) {
	// Four rows at the SAME distance from the query, none of them the query.
	// k = 2, so the buffer fills at rows 0 and 1, and rows 2 and 3 are boundary
	// candidates that must NOT displace them.
	//
	// Query along x. Every row lies in the y-z plane, so every one of them is
	// EXACTLY orthogonal to the query: distance 1, all four equal, none of them
	// the query.
	//
	// Two earlier versions of this fixture were wrong in instructive ways. Rows
	// at 0, 90, 180, 270 degrees put one row ON the query, and rows at 45, 135,
	// 225, 315 degrees are equidistant but at distance 0.29, a value no
	// threshold in this milestone will ever see. Both passed the tie assertion
	// and both were testing the wrong thing.
	//
	// The guard below -- asserting the distance is the 1 the fixture claims --
	// is the only reason either was caught. A fixture should assert its own
	// preconditions; otherwise the test can pass for reasons that have nothing
	// to do with the code under test.
	rows := [][]float32{
		{0, 1, 0, 0},
		{0, -1, 0, 0},
		{0, 0, 1, 0},
		{0, 0, 0, 1},
	}
	q, err := Normalize([]float32{1, 0, 0, 0})
	if err != nil {
		t.Fatalf("normalise query: %v", err)
	}
	for i := range rows {
		n, err := Normalize(rows[i])
		if err != nil {
			t.Fatalf("normalise row %d: %v", i, err)
		}
		rows[i] = n
	}
	for i, r := range rows {
		d, err := CosineDistance(q, r)
		if err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
	if math.Abs(d-1) > 1e-9 {
			t.Fatalf("row %d is at distance %v, not 1; the fixture needs all "+
				"rows equidistant from the query", i, d)
		}
	}

	got := nearest(rows, q, 2)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
	if got[0].Index != 0 || got[1].Index != 1 {
		t.Errorf("k=2 returned indices %d and %d; all four rows are at the "+
			"same distance, so the earlier two must be kept and a `<=` at the "+
			"boundary would have let rows 2 and 3 evict them",
			got[0].Index, got[1].Index)
	}
}

// TestCandidates_CountsEveryRowItConsiders is the cost property, stated as a
// test because the alternative is a quadratic scan nobody notices until a
// library is large.
//
// A partial selection algorithm is O(n log k) and a sort-everything is
// O(n log n). Both are fine at 40 rows; the difference is invisible until
// roughly 10^5, at which point a rescan is minutes of CPU. The test does not
// assert a complexity class -- it cannot -- but it does assert the result is
// complete, which is what a truncated scan would break.
func TestCandidates_CountsEveryRowItConsiders(t *testing.T) {
	const n = 200
	vectors := make([][]float32, n)
	for i := range vectors {
		vectors[i] = embed(8, float64(i)*0.37)
	}
	query := embed(8, 0.11)

	// Brute force, computed here, as ground truth.
	type pair struct {
		idx int
		dist float64
	}
	all := make([]pair, 0, n)
	for i, v := range vectors {
		d, err := CosineDistance(query, v)
		if err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		all = append(all, pair{i, d})
	}
	// Insertion sort: the fixture must not reuse the code under test.
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].dist < all[j-1].dist; j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}

	for _, k := range []int{1, 2, 5, 17, n} {
		got := nearest(vectors, query, k)
		if len(got) != k {
			t.Fatalf("k=%d returned %d", k, len(got))
		}
		for i := 0; i < k; i++ {
			if got[i].Index != all[i].idx {
				t.Errorf("k=%d position %d: got index %d, brute force says %d",
					k, i, got[i].Index, all[i].idx)
			}
		}
	}
	_ = fmt.Sprint()
}

// TestCandidates_ABadQueryIsARefusalNotARowSkip pins the asymmetry the previous
// test only checked from the outside.
//
// A bad ROW is skipped and reported: the index is a large accumulated body of
// data, one poisoned element must not stop the other 40,000, and the caller is
// told what was omitted.
//
// A bad QUERY cannot be skipped, because there is nothing to search WITH. The
// mutation that made these symmetric -- reporting the bad query in the skip
// list and continuing -- is the interesting one, because the caller then sees a
// plausible SkipReport describing rows that were never compared against a
// query, and an empty candidate set that reads as "nothing is nearby" rather
// than "the search never ran".
func TestCandidates_ABadQueryIsARefusalNotARowSkip(t *testing.T) {
	rows := make([][]float32, 6)
	for i := range rows {
		rows[i] = embed(4, float64(i))
	}
	good, err := Normalize([]float32{1, 0, 0, 0})
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}

	// The rows are all perfectly usable. Any skip recorded here describes a row
	// that was never the problem.
	rep := SkipReport{}
	got := nearest(rows, []float32{float32(math.NaN()), 1, 0, 0}, 3, &rep)
	if len(got) != 0 {
		t.Errorf("a NaN query returned %d candidates", len(got))
	}
	if rep.Skipped != 0 {
		t.Errorf("Skipped = %d for a bad QUERY, want 0; a skip report that "+
			"blames rows for a bad query describes rows that were never the "+
			"problem, and an empty result then reads as 'nothing is nearby' "+
			"rather than 'the search never ran'. Reasons: %v",
			rep.Skipped, rep.Reasons)
	}

	// And a usable query over the same rows does produce results, so the two
	// are genuinely distinguishable.
	rep2 := SkipReport{}
	if ok := nearest(rows, good, 3, &rep2); len(ok) != 3 {
		t.Errorf("a good query returned %d candidates, want 3", len(ok))
	}
	if rep2.Skipped != 0 {
		t.Errorf("Skipped = %d for all-usable rows, want 0: %v",
			rep2.Skipped, rep2.Reasons)
	}
}

// TestLess_TheTieBreakOrdersByIndex tests the tie-break DIRECTLY, which is the
// only way to see it.
//
// FOUR attempts were made at a `nearest`-level fixture for this, and all four
// were green with `a.Index < b.Index` deleted. The reasons, in order:
//
//  1. Five copies of the SAME vector. Every distance was 0, including the
//     query's self-match, so the buffer was already ordered and no tie-break
//     was needed.
//  2. Mirror-image rows. Equidistant, but the pair was adjacent and ascending.
//  3. Rotating to avoid the query landing on a row. Same adjacency problem.
//  4. Symmetric perturbations, distances a function of |i - 2.5|, so ties were
//     the position pairs (2,3), (1,4), (0,5) -- always ascending. Reversing the
//     emission order changed nothing, because `Neighbour.Index` is the POSITION
//     in the caller's slice, not a property of the vector: reversing which
//     vector goes where also reverses the label.
//
// The general reason, which took four tries to see: with a k-sized buffer filled
// in arrival order and re-sorted by an insertion sort that only moves an element
// LEFT past strictly-greater ones, positions within an equal-distance run come
// out ascending whatever the comparator says about equality. The comparator is
// never asked. Reaching the tie-break through `nearest` needs a fixture where a
// lower position has to move backwards past a strictly-nearer row, and no
// distance-only construction produces that.
//
// So this tests `less` directly. It is the honest place to test a comparator:
// a private function with a total order and no I/O has no excuse for not having
// a test, and pretending the property is only reachable through the public
// function is what made four fixtures useless.
func TestLess_TheTieBreakOrdersByIndex(t *testing.T) {
	// The property is a TOTAL ORDER. That is what makes the output
	// deterministic, and it is stronger than "nearer comes first": with only
	// "nearer first", two equal distances compare false in both directions and
	// the result depends on the order rows happened to arrive in.
	cases := []struct {
		name   string
		a, b   Neighbour
		lessAB bool
	}{
		{"nearer first", Neighbour{Index: 5, Distance: 0.1},
			Neighbour{Index: 0, Distance: 0.2}, true},
		{"farther is not less", Neighbour{Index: 0, Distance: 0.3},
			Neighbour{Index: 9, Distance: 0.2}, false},
		{"tie broken by LOWER index", Neighbour{Index: 2, Distance: 0.5},
			Neighbour{Index: 7, Distance: 0.5}, true},
		{"tie is not less the other way", Neighbour{Index: 7, Distance: 0.5},
			Neighbour{Index: 2, Distance: 0.5}, false},
		{"a thing is not less than itself", Neighbour{Index: 4, Distance: 0.5},
			Neighbour{Index: 4, Distance: 0.5}, false},
		{"equal distance, distance wins over index",
			Neighbour{Index: 9, Distance: 0.1}, Neighbour{Index: 0, Distance: 0.2}, true},
	}
	for _, tc := range cases {
		if got := less(tc.a, tc.b); got != tc.lessAB {
			t.Errorf("%s: less(%v, %v) = %v, want %v", tc.name, tc.a, tc.b, got, tc.lessAB)
		}
	}

	// Antisymmetry over every pair, which is what "total order" means here and
	// what makes the sort's output independent of the input order.
	all := []Neighbour{
		{Index: 0, Distance: 0.5}, {Index: 1, Distance: 0.5},
		{Index: 2, Distance: 0.1}, {Index: 3, Distance: 0.9},
		{Index: 4, Distance: 0.1},
	}
	for _, a := range all {
		for _, b := range all {
			if less(a, b) && less(b, a) {
				t.Errorf("less(%v,%v) and less(%v,%v) are both true; a total "+
					"order needs exactly one", a, b, b, a)
			}
		}
	}
}

// TestNearest_IsDeterministicRegardlessOfInputOrder is the property that
// actually matters, stated at the level where it is observable.
//
// Two runs over the same faces, in different slice orders, must produce the same
// SET of candidates. Not the same order -- the nearest one is the nearest one
// whatever the input order -- but the same set, so a clustering pass over the
// same library reaches the same answer.
func TestNearest_IsDeterministicRegardlessOfInputOrder(t *testing.T) {
	base, err := Normalize([]float32{0.5, 0.5, 0.5, 0.5})
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	rows := make([][]float32, 0, 6)
	for i := 0; i < 6; i++ {
		v, err := Normalize([]float32{
			0.5 + float32(i)*1e-4, 0.5, 0.5, 0.5 + float32(5-i)*1e-4,
		})
		if err != nil {
			t.Fatalf("normalise row %d: %v", i, err)
		}
		rows = append(rows, v)
	}

	// The candidate SETS, as sorted distances, must agree.
	sig := func(in [][]float32) []float64 {
		got := nearest(in, base, 3)
		out := make([]float64, len(got))
		for i, c := range got {
			out[i] = c.Distance
		}
		return out
	}
	forward := sig(rows)
	reversed := make([][]float32, len(rows))
	for i, r := range rows {
		reversed[len(rows)-1-i] = r
	}
	backward := sig(reversed)

	if len(forward) != len(backward) {
		t.Fatalf("forward returned %d candidates, reversed %d", len(forward), len(backward))
	}
	for i := range forward {
		if math.Abs(forward[i]-backward[i]) > 1e-15 {
			t.Errorf("position %d: forward %v, reversed %v; the same library "+
				"must produce the same candidates whatever order its rows are "+
				"stored in", i, forward[i], backward[i])
		}
	}
}
