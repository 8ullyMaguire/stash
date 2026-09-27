package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Step 2.4b.5: the embedding.
//
// # Why this needs its own tests at all
//
// Every distance in this milestone -- the over-merge guard, the assign margin,
// the consolidate threshold -- is a function of an embedding. An embedding bug
// therefore does not fail here; it fails there, as clusters that merge people
// who do not look alike, with a green suite and a plausible-looking distance
// in the log.
//
// The specific hazard is DIMENSION. An embedding is a []float32 whose length is
// decided by the model, not by this code. Every distance function in the
// milestone compares vectors, and a comparison across two different lengths
// either panics, truncates silently, or -- worst -- compares only a prefix and
// produces a number that looks like a real distance.
//
// So this file is mostly about refusing bad vectors rather than computing good
// ones.

func TestEmbedding_DimensionIsKnownAndChecked(t *testing.T) {
	// A vector of the wrong length must be refused, not padded, not truncated.
	if got := DimensionOf(make([]float32, 3)); got != 3 {
		t.Errorf("DimensionOf returned %d for a 3-element vector", got)
	}
	// The zero vector is not a valid embedding and is not a distance-zero
	// candidate: it means the model produced nothing.
	if err := ValidateEmbedding(make([]float32, 4)); err == nil {
		t.Error("a zero embedding was accepted; a model that produced nothing " +
			"must be a fault, not a point at the origin that matches everything")
	}
	// NaN and Inf poison every arithmetic operation downstream and must be
	// refused at the boundary rather than propagating into a distance that
	// compares as neither greater nor less than anything.
	for _, bad := range []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
		v := make([]float32, 4)
		v[0] = 0.5
		v[1] = bad
		if err := ValidateEmbedding(v); err == nil {
			t.Errorf("an embedding containing %v was accepted", bad)
		}
	}
	// A zero-LENGTH embedding is the degenerate case: it has no dimension to
	// disagree about, and must still be refused.
	if err := ValidateEmbedding(nil); err == nil {
		t.Error("a nil embedding was accepted")
	}
	if err := ValidateEmbedding(make([]float32, 0)); err == nil {
		t.Error("an empty embedding was accepted")
	}
}

// TestEmbedding_DistanceRefusesMismatchedDimensions is the property that keeps a
// model swap from silently corrupting every cluster.
//
// The failure this prevents is specific: comparing a prefix of two vectors
// gives a number, the number is plausible, and every threshold in the milestone
// was calibrated against the OTHER dimension. So the result is not obviously
// wrong -- it is wrong at a scale nobody can see.
func TestEmbedding_DistanceRefusesMismatchedDimensions(t *testing.T) {
	a := make([]float32, 4)
	b := make([]float32, 8)
	for i := range a {
		a[i] = 1
	}
	for i := range b {
		b[i] = 1
	}

	if _, err := CosineDistance(a, b); err == nil {
		t.Error("a 4-element and an 8-element vector were compared; the " +
			"thresholds in this milestone are calibrated against one " +
			"dimension and a prefix comparison returns a plausible wrong number")
	}
	// Identical dimensions are fine -- and the vectors are real, because two
	// all-zero vectors are correctly refused as unusable. The first version of
	// this row used zeros and failed, which was the validation working rather
	// than the dimension check misfiring.
	cc := []float32{1, 0, 0, 0}
	dd := []float32{0, 1, 0, 0}
	if _, err := CosineDistance(cc, dd); err != nil {
		t.Errorf("two 4-element vectors were refused: %v", err)
	}
}

// TestEmbedding_CosineDistanceIsBoundedByOne: a cosine distance outside 0..1 is
// a bug in the arithmetic, and a threshold of 0.5 applied to a distance of 1.3
// means something entirely different from one applied to 0.3.
func TestEmbedding_CosineDistanceIsBoundedByOne(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", unit(1, 0, 0), unit(1, 0, 0), 0},
		{"orthogonal", unit(1, 0, 0), unit(0, 1, 0), 1},
		{"opposite", unit(1, 0, 0), unit(-1, 0, 0), 2},
		// Magnitude must not matter: that is what normalisation means, and
		// getting it wrong is how a threshold calibrated on one scale meets
		// embeddings on another.
		{"scaled", unit(1, 0, 0), unit(1000, 0, 0), 0},
		{"scaled orthogonal", unit(3, 0, 0), unit(0, 0.5, 0), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CosineDistance(tc.a, tc.b)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if math.Abs(got-tc.want) > 1e-6 {
				t.Errorf("CosineDistance = %v, want %v", got, tc.want)
			}
		})
	}
}

func unit(parts ...float32) []float32 { return parts }

// TestEmbedding_NormalizeIsIdempotentAndSafe is where the zero-vector hazard
// lands.
//
// Normalising a zero vector divides by zero. The result is NaN, which then
// compares as false against every threshold -- so the face matches nothing and
// nobody is told why. That is the same failure shape as the zero-Detector guard
// in step 2.4b.0, one layer down.
func TestEmbedding_NormalizeIsIdempotentAndSafe(t *testing.T) {
	v := []float32{3, 4}
	n, err := Normalize(v)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if math.Abs(float64(n[0])-0.6) > 1e-6 || math.Abs(float64(n[1])-0.8) > 1e-6 {
		t.Errorf("Normalize([3 4]) = %v, want [0.6 0.8]", n)
	}
	// Length 1.
	if l := math.Sqrt(float64(n[0]*n[0] + n[1]*n[1])); math.Abs(l-1) > 1e-6 {
		t.Errorf("normalized vector has length %v, want 1", l)
	}
	// Normalising again must not change it -- a caller that normalises twice
	// gets a different answer, and "idempotent" is the only property that makes
	// it safe for a caller not to know.
	n2, err := Normalize(n)
	if err != nil {
		t.Fatalf("second Normalize: %v", err)
	}
	if math.Abs(float64(n2[0]-n[0])) > 1e-6 || math.Abs(float64(n2[1]-n[1])) > 1e-6 {
		t.Errorf("Normalize is not idempotent: %v then %v", n, n2)
	}

	// The zero vector.
	if _, err := Normalize(make([]float32, 3)); err == nil {
		t.Error("normalising a zero vector was allowed; the result is NaN, " +
			"which compares as false against every threshold, so the face " +
			"matches nothing and nobody is told why")
	}
}

// TestEmbedding_EncodeDoesNotRunAnUnverifiedModel is the 2.4b.0 property carried
// down one layer.
//
// The detector verified a digest before parsing. The embedder has the same
// exposure -- it is handed a model file -- and the temptation is to check the
// digest in the detector and trust whoever calls the embedder. A caller that
// forgot is an unverified model, executed.
func TestEmbedding_EncodeDoesNotRunAnUnverifiedModel(t *testing.T) {
	r := &recordingRuntime{}

	// No pin at all.
	if _, err := NewEmbedder(r, ""); err == nil {
		t.Error("an embedder with no expected digest was constructed")
	}
	if r.calls != 0 {
		t.Errorf("the runtime was invoked %d times during construction; a model "+
			"must not be executed before its digest is verified", r.calls)
	}

	// A malformed pin.
	for _, bad := range []string{"not-a-digest", "abcd", "sha256:", "sha256:zz" + string(make([]byte, 62))} {
		if _, err := NewEmbedder(r, bad); err == nil {
			t.Errorf("an embedder was constructed with the malformed pin %q", bad)
		}
		if r.calls != 0 {
			t.Errorf("the runtime was invoked for the malformed pin %q", bad)
		}
	}
}

// TestEmbedding_AWellFormedPinStillRefusesAMismatchedModel: the pin is not
// decoration.
//
// A test that only checks malformed pins proves the parser works. The property
// that matters is that a well-formed pin and a wrong model are refused, because
// that is the case an operator actually meets after a model is replaced.
func TestEmbedding_AWellFormedPinStillRefusesAMismatchedModel(t *testing.T) {
	dir := t.TempDir()

	// The file the pin was taken from.
	rightPath := filepath.Join(dir, "right.onnx")
	require.NoError(t, os.WriteFile(rightPath, []byte("the expected model"), 0o600))
	rightPin := testDigestOf("the expected model")

	// A DIFFERENT file, well-formed pin, wrong bytes. This is the case an
	// operator meets after replacing a model, and the first version of this
	// test could not express it at all: Attach took the caller's word for the
	// file's hash, so passing the expected digest for the wrong file produced a
	// "verified" embedder over arbitrary bytes.
	wrongPath := filepath.Join(dir, "wrong.onnx")
	require.NoError(t, os.WriteFile(wrongPath, []byte("a different model"), 0o600))

	t.Run("matching pin attaches", func(t *testing.T) {
		r := &recordingRuntime{dimension: 4}
		e, err := NewEmbedder(r, rightPin)
		require.NoError(t, err)
		require.NoError(t, e.Attach(rightPath))
		if r.calls != 1 {
			t.Errorf("the runtime was called %d times, want 1 (the load)", r.calls)
		}
		if e.Dimension() != 4 {
			t.Errorf("Dimension = %d, want 4", e.Dimension())
		}
	})

	t.Run("mismatched file is refused", func(t *testing.T) {
		r := &recordingRuntime{dimension: 4}
		e, err := NewEmbedder(r, rightPin)
		require.NoError(t, err)

		err = e.Attach(wrongPath)
		if err == nil {
			t.Fatal("a model whose bytes do not match the pin was accepted")
		}
		var unavailable *UnavailableError
		require.True(t, errors.As(err, &unavailable), "got %T, want *UnavailableError", err)
		if unavailable.Reason != ReasonDigestMismatch {
			t.Errorf("reason is %q, want %q", unavailable.Reason, ReasonDigestMismatch)
		}
		if r.calls != 0 {
			t.Errorf("the runtime was called %d times despite a mismatch; "+
				"verification must precede any parsing", r.calls)
		}
		if e.Dimension() != 0 {
			t.Errorf("a refused embedder reports dimension %d; it must report "+
				"none, or a caller will treat an unusable model as ready", e.Dimension())
		}
	})

	t.Run("an unreadable file is its own reason", func(t *testing.T) {
		r := &recordingRuntime{dimension: 4}
		e, err := NewEmbedder(r, rightPin)
		require.NoError(t, err)

		err = e.Attach(filepath.Join(dir, "does-not-exist.onnx"))
		require.Error(t, err)
		var unavailable *UnavailableError
		require.True(t, errors.As(err, &unavailable), "got %T, want *UnavailableError", err)
		if unavailable.Reason != ReasonDigestUnreadable {
			t.Errorf("reason is %q, want %q; a missing file and a wrong file "+
				"are different operator actions -- get the file versus fix the pin",
				unavailable.Reason, ReasonDigestUnreadable)
		}
		if r.calls != 0 {
			t.Errorf("the runtime was called %d times for a missing file", r.calls)
		}
	})

	t.Run("embed before attach is a fault, not an empty result", func(t *testing.T) {
		r := &recordingRuntime{dimension: 4}
		e, err := NewEmbedder(r, rightPin)
		require.NoError(t, err)

		v, err := e.Embed([]byte("a crop"))
		if err == nil {
			t.Fatalf("embedding with no model returned %v and no error; the "+
				"whole of step 2.4b.0 applies here -- a component that cannot "+
				"run must not look like a face it did not find", v)
		}
	})
}

// recordingRuntime counts the calls a verification is supposed to precede.
//
// The count is the only reason this fixture exists. "The digest is checked
// before the model is loaded" is otherwise an assertion about ordering that no
// test can see -- a mock that records nothing proves nothing about WHEN it was
// consulted.
type recordingRuntime struct {
	calls     int
	dimension int
	loaded    bool
	loadErr   error
	embedOut  []float32
	embedErr  error
}

func (r *recordingRuntime) LoadModel(path string) error {
	r.calls++
	r.loaded = true
	return r.loadErr
}

func (r *recordingRuntime) Embed(crop []byte) ([]float32, error) {
	r.calls++
	return r.embedOut, r.embedErr
}

func (r *recordingRuntime) Dimension() int {
	if !r.loaded {
		return 0
	}
	return r.dimension
}

// testDigestOf is the hex SHA-256 of a string.
//
// It lives in the test file rather than beside the code, which is a small thing
// worth being deliberate about: a helper only tests use, in production, is a
// helper the production build drags in. The first version of embedding.go had
// it, and the file would not compile without crypto/sha256 and encoding/hex for
// one function no caller outside a test can reach.
func testDigestOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// TestEmbedding_AnUnverifiedModelIsNeverParsed is the property AE and AF both
// hid behind.
//
// AE loads the runtime before checking the digest; AF leaves modelReady true on
// a refused embedder. Both left the suite green because the existing tests
// checked the OUTCOME (a mismatched model is refused, an unattached embedder
// embeds nothing) and never the ORDERING or the resulting state. An outcome
// test cannot see a reordered implementation, and a state left dirty by an
// error path is invisible until the next call reuses it.
//
// So this test asserts both directly, on the runtime fixture that counts calls
// and the embedder's own readiness.
func TestEmbedding_AnUnverifiedModelIsNeverParsed(t *testing.T) {
	dir := t.TempDir()
	wrongPath := filepath.Join(dir, "wrong.onnx")
	require.NoError(t, os.WriteFile(wrongPath, []byte("a different model"), 0o600))
	pin := testDigestOf("the expected model")

	t.Run("the runtime is not touched when the digest does not match", func(t *testing.T) {
		r := &recordingRuntime{dimension: 4}
		e, err := NewEmbedder(r, pin)
		require.NoError(t, err)
		require.Error(t, e.Attach(wrongPath))

		// Not "no error" -- NO CALLS. A load that happened and then reported a
		// mismatch has already parsed unverified bytes, which is the entire
		// hazard, and an outcome-only test cannot tell it from one that did not.
		if r.calls != 0 {
			t.Errorf("the runtime was called %d times; LoadModel on unverified "+
				"bytes is the hazard, and a load that reports a mismatch "+
				"afterwards has already paid it", r.calls)
		}
		if r.loaded {
			t.Error("the runtime reports a loaded model after a refused attach")
		}
	})

	t.Run("a refused embedder is not ready", func(t *testing.T) {
		r := &recordingRuntime{dimension: 4}
		e, err := NewEmbedder(r, pin)
		require.NoError(t, err)
		require.Error(t, e.Attach(wrongPath))

		if e.modelReady {
			t.Error("modelReady is true after a refused attach; a later caller " +
				"that checks the flag rather than the error will embed")
		}
		if e.Dimension() != 0 {
			t.Errorf("Dimension() is %d after a refused attach, want 0", e.Dimension())
		}
	})
}

// TestEmbedding_AModelThatChangesItsOutputWidthIsRefused covers AG.
//
// A model that returns a different width than it did on the first call is not
// a model this code can use: every distance it feeds is calibrated against the
// width it had before, and a silent width change is a corpus of clusters built
// on two incompatible scales.
//
// The runtime fixture is given a first width and a second one, and the two Embed
// calls differ. That is the only way to reach the check -- a model whose width
// is constant trips nothing.
func TestEmbedding_AModelThatChangesItsOutputWidthIsRefused(t *testing.T) {
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "model.onnx")
	require.NoError(t, os.WriteFile(modelPath, []byte("the model"), 0o600))

	r := &recordingRuntime{dimension: 4}
	e, err := NewEmbedder(r, testDigestOf("the model"))
	require.NoError(t, err)
	require.NoError(t, e.Attach(modelPath))

	// First call: the declared width.
	r.embedOut = []float32{1, 0, 0, 0}
	v, err := e.Embed([]byte("crop"))
	require.NoError(t, err)
	require.Len(t, v, 4)

	// Second call: a different width from the same model.
	r.embedOut = []float32{1, 0, 0, 0, 0, 0, 0, 0}
	v, err = e.Embed([]byte("crop"))
	if err == nil {
		t.Fatalf("a model that changed its output width from 4 to %d returned "+
			"%v and no error; every threshold in this milestone is calibrated "+
			"against the width the model had before", len(v), v)
	}
	if !errors.Is(err, ErrDimensionMismatch) {
		t.Errorf("got %v, want ErrDimensionMismatch", err)
	}
}
