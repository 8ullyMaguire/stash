package cluster

import (
	"math"
	"testing"
)

// Step 2.4b.9: the geometry seam.
//
// These are not tests of cosine distance -- `embedding_test.go` already has
// forty-odd of those, including the NaN trap and the self-match-at-1.1e-16 one.
// These test the thing that did not exist before: that there are two
// implementations, that they agree where they are supposed to agree, and that
// the properties the stages depend on hold for BOTH.
//
// The second half is the point. A geometry is only usable by a stage if its
// behaviour is a function of its contract rather than of its implementation.
// A stage that works on the line and misbehaves on cosine is a stage that
// passes every test in this package and over-merges in production.

// unitVec is a unit vector along one axis, the easiest embedding to reason
// about: its cosine distance to another is a function of the angle between the
// axes and nothing else.
// axisAngle is a unit vector at `deg` degrees away from `axis` in the plane
// spanned by `axis` and the next one. The angle is what sets the cosine
// distance: 0 is identical, 90 is orthogonal (distance 1.0), 180 is opposite.
func axisAngle(dim, axis, deg int) []float32 {
	r := float64(deg) * math.Pi / 180
	v := make([]float32, dim)
	v[axis] = float32(math.Cos(r))
	v[(axis+1)%dim] = float32(math.Sin(r))
	return v
}

func unitVec(dim, axis int) []float32 {
	v := make([]float32, dim)
	v[axis] = 1
	return v
}

func TestScalarGeometry_MatchesTheHandArithmetic(t *testing.T) {
	g := ScalarGeometry{}

	d, err := g.Distance(Scalar(0.1, "a"), Scalar(0.4, "b"))
	if err != nil {
		t.Fatalf("Distance: %v", err)
	}
	// 1e-6, not 1e-12: the coordinates round-trip through float32, which carries
	// about seven significant digits, so 0.300000004 is the closest float32 to
	// 0.3 and a tighter tolerance would fail on correct arithmetic.
	if math.Abs(d-0.3) > 1e-6 {
		t.Errorf("Distance(0.1, 0.4) = %v, want 0.3", d)
	}

	// Symmetric. Not a property cosine has for free either, and a stage that
	// calls it from both sides would get two different answers.
	d2, err := g.Distance(Scalar(0.4, "b"), Scalar(0.1, "a"))
	if err != nil {
		t.Fatalf("Distance: %v", err)
	}
	if d2 != d {
		t.Errorf("Distance is not symmetric: %v then %v", d, d2)
	}

	c, err := g.Centroid([]Point{Scalar(0.0, "a"), Scalar(1.0, "b"), Scalar(0.5, "c")})
	if err != nil {
		t.Fatalf("Centroid: %v", err)
	}
	if got := ScalarPos(c); math.Abs(got-0.5) > 1e-6 {
		t.Errorf("Centroid = %v, want 0.5", got)
	}

	// Diameter is the span, which for a set of two is the distance between them
	// and for a set of three is the widest gap. A diameter defined as
	// distance-to-centroid would give 0.5 here, and a guard keyed on 0.5 would
	// accept a cluster twice as wide as its threshold.
	diam, err := g.Diameter([]Point{Scalar(0.0, "a"), Scalar(0.4, "b"), Scalar(0.9, "c")})
	if err != nil {
		t.Fatalf("Diameter: %v", err)
	}
	if math.Abs(diam-0.9) > 1e-6 {
		t.Errorf("Diameter = %v, want 0.9 (the span, not the spread about the centre)", diam)
	}
}

func TestCosineGeometry_DiameterIsNotDistanceToCentroid(t *testing.T) {
	const dim = 8
	g := CosineGeometry{}

	// Two faces 0.6 apart. Their centroid is 0.3 from each, so an
	// implementation that conflated the two questions would report a diameter
	// of 0.3 for a cluster whose members are 0.6 apart.
	a := Point{Vector: unitVec(dim, 0), Key: "a"}
	b := Point{Vector: unitVec(dim, 1), Key: "b"} // orthogonal: distance 1.0
	faces := []Point{a, b}

	pair, err := g.Distance(a, b)
	if err != nil {
		t.Fatalf("Distance: %v", err)
	}
	if math.Abs(pair-1.0) > 1e-6 {
		t.Fatalf("Distance between orthogonal unit vectors = %v, want 1.0", pair)
	}

	diam, err := g.Diameter(faces)
	if err != nil {
		t.Fatalf("Diameter: %v", err)
	}
	if math.Abs(diam-1.0) > 1e-6 {
		t.Errorf("Diameter = %v, want 1.0", diam)
	}

	// The centroid of two orthogonal vectors is 45 degrees from each, so
	// distance-to-centroid is about 0.293. Asserting the two are different is
	// the property; the exact figure is the arithmetic.
	c, err := g.Centroid(faces)
	if err != nil {
		t.Fatalf("Centroid: %v", err)
	}
	toCentre, err := g.Distance(a, c)
	if err != nil {
		t.Fatalf("Distance to centroid: %v", err)
	}
	if math.Abs(toCentre-diam) < 1e-3 {
		t.Errorf("distance-to-centroid (%v) and diameter (%v) are the same, so "+
			"this implementation is keying the over-merge guard on the mean "+
			"position rather than the cluster's width", toCentre, diam)
	}
}

func TestCosineGeometry_CentroidIsNormalised(t *testing.T) {
	const dim = 4
	g := CosineGeometry{}

	// Two nearby unit vectors. Their mean is shorter than 1 -- and the shorter
	// it is, the tighter the cluster. An un-normalised mean would therefore
	// make a TIGHT cluster's centroid look further from an incoming face than a
	// loose cluster's, which is backwards: the tight cluster is the one that
	// should attract faces.
	//
	// This test fails if the normalisation is removed, and the failure it
	// produces is a threshold that loosens as a cluster tightens.
	a := Point{Vector: []float32{1, 0, 0, 0}, Key: "a"}
	b := Point{Vector: []float32{0.99, 0.1, 0, 0}, Key: "b"}
	b.Vector = normalize(b.Vector)

	c, err := g.Centroid([]Point{a, b})
	if err != nil {
		t.Fatalf("Centroid: %v", err)
	}
	var ss float64
	for _, x := range c.Vector {
		ss += float64(x) * float64(x)
	}
	if math.Abs(ss-1.0) > 1e-5 {
		t.Errorf("centroid has length %v, want 1; an un-normalised mean makes "+
			"the assign threshold depend on how spread out a cluster is", math.Sqrt(ss))
	}
}

func normalize(v []float32) []float32 {
	var ss float64
	for _, x := range v {
		ss += float64(x) * float64(x)
	}
	inv := float32(1 / math.Sqrt(ss))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

func TestMeanEmbedding_RefusesTheCasesThatWouldProduceNaN(t *testing.T) {
	t.Run("members that cancel", func(t *testing.T) {
		// v and -v average to the zero vector, which has no direction. Every
		// cosine distance to a zero vector is NaN, and NaN compares false
		// against every threshold -- so it would pass every guard in the
		// package. This is the CosineDistance trap reached from the other end,
		// and it is an error rather than a zero vector for that reason.
		_, err := meanEmbedding([][]float32{
			{1, 0, 0},
			{-1, 0, 0},
		})
		if err == nil {
			t.Fatal("averaging v and -v returned no error; the result is the " +
				"zero vector, whose distances are all NaN")
		}
	})

	t.Run("mismatched widths", func(t *testing.T) {
		// A silent truncation would average the first 3 components and ignore
		// the 4th, producing a plausible-looking centroid for an embedding
		// that was never valid.
		_, err := meanEmbedding([][]float32{{1, 0, 0, 0}, {0, 1, 0}})
		if err == nil {
			t.Fatal("embeddings of different widths were averaged; the " +
				"narrower one is silently truncated")
		}
	})

	t.Run("no embeddings", func(t *testing.T) {
		if _, err := meanEmbedding(nil); err == nil {
			t.Fatal("averaging nothing returned no error")
		}
	})

	t.Run("a zero-width embedding", func(t *testing.T) {
		if _, err := meanEmbedding([][]float32{{}, {}}); err == nil {
			t.Fatal("averaging zero-width embeddings returned no error")
		}
	})
}

func TestGeometry_OverMergePropertiesHoldForBothImplementations(t *testing.T) {
	// The property the whole milestone rests on, checked against both
	// geometries.
	//
	// A stage that behaves differently on the line than on cosine is the failure
	// this file exists to prevent: every existing test in the package uses the
	// scalar geometry, so a divergence here would mean the entire test suite
	// certifies behaviour the production path never takes.
	geoms := []struct {
		name string
		g    Geometry
		// near and far are two clusters a threshold should separate.
		near, far []Point
	}{
		{
			name: "scalar",
			g:    ScalarGeometry{},
			// Both probes are the FIRST member of each cluster. The near one
			// sits at 0.00 and the far one at 0.80, so measuring both against
			// the near centroid (0.025) gives 0.025 and 0.775.
			near: []Point{Scalar(0.00, "a"), Scalar(0.05, "b")},
			far:  []Point{Scalar(0.80, "c"), Scalar(0.90, "d")},
		},
		{
			// Built from ANGLES rather than from axes. The first version used
			// orthogonal unit vectors for both clusters, which are 1.0 apart --
			// the maximum cosine distance -- so "near" and "far" were the same
			// distance and the test asserted a distinction the geometry does not
			// make. 0 and 30 degrees for near, 0 and 170 for far: 0.134 and
			// 1.992 clamped, so unambiguously ordered.
			name: "cosine",
			g:    CosineGeometry{},
			near: []Point{
				{Vector: axisAngle(8, 0, 30), Key: "a"},
				{Vector: unitVec(8, 0), Key: "b"},
			},
			far: []Point{
				{Vector: axisAngle(8, 0, 150), Key: "c"},
				{Vector: unitVec(8, 0), Key: "d"},
			},
		},
	}

	for _, tc := range geoms {
		t.Run(tc.name, func(t *testing.T) {
			nearDiam, err := tc.g.Diameter(tc.near)
			if err != nil {
				t.Fatalf("Diameter(near): %v", err)
			}
			farDiam, err := tc.g.Diameter(tc.far)
			if err != nil {
				t.Fatalf("Diameter(far): %v", err)
			}
			if nearDiam >= farDiam {
				t.Errorf("near cluster diameter %v is not smaller than far %v; "+
					"a threshold between them would be arbitrary", nearDiam, farDiam)
			}

			// Monotonicity: a face nearer the cluster's members must report a
			// smaller distance than a face further away. A stage that assumed
			// this is sound on one geometry and not the other produces clusters
			// that grow in the wrong direction, and the error compounds.
			// Monotonicity is a statement about ONE fixed point: a face near
			// the cluster's centroid must be closer than a face further from it.
			//
			// The first version compared each cluster's own member against the
			// other cluster's member, and the fixture was symmetric enough that
			// both probes landed 0.025 from their respective centroids. The
			// assertion was about the two centroids rather than about the
			// geometry, and it failed for that reason alone.
			c, err := tc.g.Centroid(tc.near)
			if err != nil {
				t.Fatalf("Centroid: %v", err)
			}
			// Both probes are measured against the SAME centroid, and they are
			// members of the near and far clusters respectively.
			inside := tc.near[0]
			outside := tc.far[0]
			dIn, err := tc.g.Distance(inside, c)
			if err != nil {
				t.Fatalf("Distance: %v", err)
			}
			dOut, err := tc.g.Distance(outside, c)
			if err != nil {
				t.Fatalf("Distance: %v", err)
			}
			if dIn >= dOut {
				t.Errorf("a face from the near cluster is %v from the near "+
					"centroid but a face from the far cluster is %v; the "+
					"geometry is not monotone and any threshold on it is "+
					"arbitrary", dIn, dOut)
			}
		})
	}
}

func TestCosineGeometry_DiameterRefusesRatherThanGuessingWhenAPairIsBad(t *testing.T) {
	const dim = 4
	g := CosineGeometry{}

	// One member has a NaN component. The diameter is then UNKNOWN -- not
	// large, not zero. Returning a large diameter would let the guard split a
	// cluster for a reason that is really a corrupt embedding, and splitting is
	// destructive: the faces end up in clusters nobody chose.
	faces := []Point{
		{Vector: unitVec(dim, 0), Key: "a"},
		{Vector: unitVec(dim, 1), Key: "b"},
		{Vector: []float32{float32(math.NaN()), 0, 0, 0}, Key: "corrupt"},
	}
	d, err := g.Diameter(faces)
	if err == nil {
		t.Errorf("Diameter over a NaN embedding = %v with no error; the "+
			"cluster's width is unknown and a guard would be guessing", d)
	}
}
