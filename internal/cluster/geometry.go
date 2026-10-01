package cluster

import (
	"errors"
	"fmt"
	"math"
)

// sqrt is aliased so the normalisation above reads as arithmetic rather than
// as a package qualifier in the middle of a loop.
var sqrt = math.Sqrt

// Step 2.4b.9: the geometry seam.
//
// # Why this file exists
//
// The five stages of a pass -- candidates, assign, over-merge, consolidate --
// all ask the same two questions about a pair of faces:
//
//	how far apart are these?
//	where is the middle of this set?
//
// Until now those two questions were answered by a 1-D stand-in: `testPoint` with
// a `pos float64` and a distance of `|a - b|`. That is a genuinely good
// property-test fixture -- the interesting cases are the ones where a human can
// see the two rules disagree, and they are checkable by hand on a line. The
// problem is not the stand-in. The problem is that it was the ONLY
// implementation, and `internal/cluster` was imported by nothing, so the
// properties proven by steps 2.4b.2 through 2.4b.6 held for arithmetic on a
// line and for no reachable production path.
//
// So the arithmetic stays and becomes a named implementation rather than a
// private accident, and a real one joins it.
//
// # The interface
//
// Two methods, deliberately. The temptation is to make this a general
// "similarity" abstraction with weighting, vectors, and a strategy pattern. Two
// methods is what the stages actually call, and every method that is never
// called is a method whose correctness nobody has tested.
//
// The type parameter is the embedding. `any` would be honest at the interface
// and useless at every call site, because every caller immediately has to assert
// the thing back. The stages index an embedding as a []float32 by index, and
// that is a fact about the pipeline, not a guess.

// Geometry answers the two questions a clustering stage asks about faces.
//
// Implementations must be MONOTONE with respect to similarity: two
// implementations that disagree about ordering will produce different clusters
// from the same input, and the disagreement will look like a threshold problem
// rather than a geometry problem.
type Geometry interface {
	// Distance reports how far apart two faces are. Smaller is closer.
	//
	// Returns an error because one implementation can fail: a NaN component
	// makes a cosine distance undefined, and undefined compares false against
	// every threshold in this package. ScalarGeometry cannot fail and returns a
	// nil error -- a documented guarantee rather than a differently-shaped
	// method, because an interface whose implementations disagree on arity is
	// not an interface.
	Distance(a, b Point) (float64, error)

	// Centroid reports the middle of a set of faces, as a point that Distance
	// can consume. The result is NORMALISED, so a tight cluster's centroid is
	// no further from its members than a loose cluster's is.
	Centroid(faces []Point) (Point, error)

	// Diameter reports the largest distance between any two faces in a set.
	//
	// Separate from Centroid because a cluster's mean position and its width are
	// different questions, and an implementation that conflates them produces a
	// guard that accepts a bimodal cluster because its average is in the middle
	// of the gap. That is the over-merge this whole milestone exists to prevent.
	//
	// An error means the width is UNKNOWN, not large. A caller that receives a
	// large diameter would split a cluster for a reason that is really a
	// corrupt embedding, and a split is destructive.
	Diameter(faces []Point) (float64, error)
}

// Point is one appearance's embedding plus the key that identifies which
// appearance it is.
//
// The key is target + frame + crop: two faces with the same key are the same
// appearance seen twice, which is a duplicate rather than a coincidence. It
// travels with the vector because the membership check and the distance
// calculation are two answers about the same object, and passing them separately
// is how a caller ends up comparing face A's vector against face B's key.
type Point struct {
	// Vector is the embedding. COPIED on the way into a stage, because a caller
	// reusing its decode buffer across faces is a bug that produces a cluster of
	// identical vectors -- every face equidistant from every other, so every
	// face ambiguous, so a review queue of everything.
	Vector []float32
	// Key is the membership identity: target + frame + crop.
	Key string
}

// ScalarGeometry is the 1-D line the fixture tests use.
//
// It stays, and it stays exported, because the scalar tests are the property
// tests: they can enumerate every case where the separation margin and the
// over-merge diameter disagree, which is the actual content of those steps. A
// real-vector test that has to hand-compute a cosine is a test nobody writes.
//
// The mapping is exact and the tests are hand-checkable: a scalar face is
// `Vector: []float32{pos}`, so the real pipeline reading these tests is
// "one-dimensional embeddings", and every number in assign_test.go is a
// coordinate on a line rather than a distance in 512 dimensions.
type ScalarGeometry struct{}

// Scalar builds a Point on the line at position pos.
func Scalar(pos float64, key string) Point {
	return Point{Vector: []float32{float32(pos)}, Key: key}
}

// ScalarPos reads the coordinate back out. Exists so a test can assert that a
// centroid landed where the arithmetic says it should, rather than asserting
// against a literal that would have to be kept in step by hand.
func ScalarPos(f Point) float64 {
	if len(f.Vector) == 0 {
		return 0
	}
	return float64(f.Vector[0])
}

func (ScalarGeometry) Distance(a, b Point) (float64, error) {
	d := ScalarPos(a) - ScalarPos(b)
	if d < 0 {
		return -d, nil
	}
	return d, nil
}

// Centroid of nothing is the origin, and never an error. An empty cluster has
// no centre, but the stages only ever ask for the centre of a cluster that has
// at least one member, and inventing an error here would mean the scalar tests
// had to construct a singleton before asking anything -- which is the kind of
// ceremony that makes a fixture read as noise.
func (ScalarGeometry) Centroid(faces []Point) (Point, error) {
	if len(faces) == 0 {
		return Point{Vector: []float32{0}}, nil
	}
	var sum float64
	for _, f := range faces {
		sum += ScalarPos(f)
	}
	return Point{Vector: []float32{float32(sum / float64(len(faces)))}}, nil
}

// Diameter is the span: max - min. Which is |a - b| for a set of two, and the
// widest gap for a set of more.
func (ScalarGeometry) Diameter(faces []Point) (float64, error) {
	if len(faces) < 2 {
		return 0, nil
	}
	lo, hi := ScalarPos(faces[0]), ScalarPos(faces[0])
	for _, f := range faces[1:] {
		p := ScalarPos(f)
		if p < lo {
			lo = p
		}
		if p > hi {
			hi = p
		}
	}
	return hi - lo, nil
}

// CosineGeometry is the production implementation.
//
// It delegates to the same CosineDistance the candidate stage uses, rather than
// having its own copy. Two implementations of cosine distance in one package is
// one too many: they would drift, and the drift would show up as a cluster
// whose stored distances do not match the ones its own members were assigned
// on -- a data problem that looks like a threshold problem.
type CosineGeometry struct{}

// EmbeddingDim is the width every embedding must have.
//
// A constant rather than a per-model field because it is checked, not carried:
// an embedding of the wrong width is a decode error, and a decode error is
// caught where the decode happens. Carrying it would mean every Geometry had to
// agree with the store about the number, and two places that have to agree is
// one more than is needed.
const EmbeddingDim = 512

// Distance checks the width against EmbeddingDim, not merely against each
// other.
//
// CosineDistance compares its two arguments' widths to EACH OTHER, so a 2-wide
// vector measured against another 2-wide vector passes -- which is why the
// first version of the guard admitted a short embedding as a cluster's first
// face and only discovered it when the second face arrived. The self-comparison
// that catches NaN cannot catch this one, because the self-comparison of a
// malformed vector is perfectly well-formed.
func (CosineGeometry) Distance(a, b Point) (float64, error) {
	if n := len(a.Vector); n != EmbeddingDim {
		return 0, fmt.Errorf("%w: width %d, want %d", ErrWrongWidth, n, EmbeddingDim)
	}
	if n := len(b.Vector); n != EmbeddingDim {
		return 0, fmt.Errorf("%w: width %d, want %d", ErrWrongWidth, n, EmbeddingDim)
	}
	return CosineDistance(a.Vector, b.Vector)
}

// ErrWrongWidth is an embedding that is not the width the engine produces.
//
// Distinct from ErrInvalidEmbedding (which is about VALUES -- NaN, infinity,
// all zeroes) because the two have different origins: a wrong width is a
// decode or version fault, a bad value is a corrupt row. Both are unmeasurable,
// and both are refused, but an operator reading the error needs to know which
// to go and look for.
var ErrWrongWidth = errors.New("embedding has the wrong width")

func (CosineGeometry) Centroid(faces []Point) (Point, error) {
	if len(faces) == 0 {
		return Point{}, errors.New("cannot take the centroid of no faces")
	}
	mean, err := meanEmbedding(points2vectors(faces))
	if err != nil {
		return Point{}, err
	}
	return Point{Vector: mean}, nil
}

// meanEmbedding is the componentwise mean, then re-normalised.
//
// The re-normalisation is not optional. A mean of unit vectors is shorter than
// 1, and its length depends on how spread out the members are -- so an
// un-normalised mean makes a tight cluster's centroid look FARTHER from a face
// than a loose cluster's does, which inverts the assign threshold exactly where
// it matters: a cluster that has quietly spread out is the one that must stop
// attracting new faces.
func meanEmbedding(vectors [][]float32) ([]float32, error) {
	if len(vectors) == 0 {
		return nil, errors.New("cannot average no embeddings")
	}
	dim := len(vectors[0])
	if dim == 0 {
		return nil, errors.New("cannot average a zero-width embedding")
	}
	sum := make([]float32, dim)
	for _, v := range vectors {
		if len(v) != dim {
			return nil, fmt.Errorf("embedding width mismatch: want %d, got %d", dim, len(v))
		}
		for j, x := range v {
			sum[j] += x
		}
	}
	n := float32(len(vectors))
	mean := make([]float32, dim)
	for j := range sum {
		mean[j] = sum[j] / n
	}

	var ss float64
	for _, x := range mean {
		ss += float64(x) * float64(x)
	}
	if ss == 0 {
		// The members cancelled, or every one of them was the zero vector. A
		// zero centroid has no direction, so every distance to it is undefined
		// and CosineDistance would hand back NaN -- which compares false
		// against every threshold and passes straight through. This is the same
		// trap as the one found in CosineDistance itself, reached from the other
		// end, and it is why this is an error rather than a zero vector.
		return nil, errors.New("embeddings averaged to the zero vector")
	}
	inv := float32(1 / math.Sqrt(ss))
	for j := range mean {
		mean[j] *= inv
	}
	return mean, nil
}

// Diameter is max pairwise distance, which for cosine is the widest angle in
// the set. Not the distance to the centroid: a cluster of two faces 0.6 apart
// has a centroid each is 0.3 from, and a guard keyed on 0.3 would accept a
// cluster twice as wide as its threshold.
func (CosineGeometry) Diameter(faces []Point) (float64, error) {
	if len(faces) < 2 {
		return 0, nil
	}
	worst := 0.0
	for i := 0; i < len(faces); i++ {
		for j := i + 1; j < len(faces); j++ {
			d, err := CosineDistance(faces[i].Vector, faces[j].Vector)
			if err != nil {
				// A bad pair makes the diameter UNKNOWN, not large. Returning a
				// large diameter would let the guard split a cluster for a
				// reason that is really a corrupt embedding, and the split is
				// destructive.
				return 0, err
			}
			if d > worst {
				worst = d
			}
		}
	}
	return worst, nil
}

func points2vectors(faces []Point) [][]float32 {
	out := make([][]float32, 0, len(faces))
	for _, f := range faces {
		out = append(out, f.Vector)
	}
	return out
}
