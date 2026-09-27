package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

// Step 2.4b.5: the embedding.
//
// # The property that makes this file mostly refusal
//
// Every distance in this milestone -- the over-merge guard, the assign margin,
// the consolidate threshold -- is a function of an embedding. An embedding bug
// does not fail here. It fails THERE, as clusters that merge people who do not
// look alike, with a green suite and a plausible-looking distance in the log.
//
// The specific hazard is DIMENSION. A vector's length is decided by the model,
// not by this code, and every distance function compares two of them. A
// comparison across two lengths either panics, truncates silently, or -- the
// dangerous one -- compares only a prefix and returns a number that looks real
// while every threshold in the milestone was calibrated against the other
// dimension. So the dimension is checked at every boundary, and mismatches are
// refused rather than reconciled.

// DimensionOf returns an embedding's length.
//
// It exists so a caller can CHECK a dimension without reaching into the slice
// header, and so the check has a name that can appear in an error message.
func DimensionOf(v []float32) int { return len(v) }

// ErrDimensionMismatch means two embeddings are not comparable.
//
// A distinct sentinel, not a formatted error, because the two callers that hit
// it want opposite things: a query that finds a wrong-dimension row should skip
// it and continue, while a caller comparing two known-shaped vectors has a bug.
// Neither is served well by a string.
var ErrDimensionMismatch = errors.New("embedding dimensions differ")

// ErrInvalidEmbedding means the vector cannot be used at all: empty, all zeros,
// or containing a non-finite value.
//
// All three fail the same way downstream and are refused for the same reason. A
// NaN compares as false against every threshold, so a face with a NaN in it
// matches nothing and nothing is reported -- the same shape as the zero-face
// Detector in step 2.4b.0, one layer down.
var ErrInvalidEmbedding = errors.New("embedding is not usable")

// ValidateEmbedding refuses a vector that cannot participate in a distance.
func ValidateEmbedding(v []float32) error {
	if len(v) == 0 {
		return fmt.Errorf("%w: empty", ErrInvalidEmbedding)
	}

	var sumSq float64
	for i, f := range v {
		if math.IsNaN(float64(f)) {
			return fmt.Errorf("%w: NaN at index %d", ErrInvalidEmbedding, i)
		}
		if math.IsInf(float64(f), 0) {
			return fmt.Errorf("%w: infinity at index %d", ErrInvalidEmbedding, i)
		}
		sumSq += float64(f) * float64(f)
	}
	if sumSq == 0 {
		// A zero vector normalises to NaN and then matches nothing. The model
		// produced nothing, which is a fault -- not a face at the origin.
		return fmt.Errorf("%w: all zeroes", ErrInvalidEmbedding)
	}
	return nil
}

// CosineDistance returns 1 - cosine similarity, so 0 is identical and 1 is
// orthogonal.
//
// The range is 0..2, not 0..1. Opposite vectors are 2, and a caller that
// assumed 0..1 would misread the far end as "just past orthogonal" rather than
// "as far from this as it is possible to be". The tests pin the endpoints so the
// real range is discoverable from the test names.
func CosineDistance(a, b []float32) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("%w: %d vs %d", ErrDimensionMismatch, len(a), len(b))
	}
	if len(a) == 0 {
		return 0, fmt.Errorf("%w: empty", ErrInvalidEmbedding)
	}

	var dot, normA, normB float64
	for i := range a {
		af, bf := float64(a[i]), float64(b[i])
		dot += af * bf
		normA += af * af
		normB += bf * bf
	}
	if normA == 0 || normB == 0 {
		return 0, fmt.Errorf("%w: zero vector", ErrInvalidEmbedding)
	}
	cos := dot / (math.Sqrt(normA) * math.Sqrt(normB))

	// Clamp. Floating point can produce 1.0000000000000002 for two identical
	// vectors, and a distance above 1 on the identical case is a number every
	// threshold in the milestone will read as "slightly too far".
	if cos > 1 {
		cos = 1
	} else if cos < -1 {
		cos = -1
	}
	return 1 - cos, nil
}

// Normalize returns a unit-length copy. The input is not modified.
//
// Idempotent, which is the property that makes it safe for a caller not to
// know whether a value has been normalised: normalising twice gives the same
// answer, normalising once and then comparing against a raw value does not.
func Normalize(v []float32) ([]float32, error) {
	if err := ValidateEmbedding(v); err != nil {
		return nil, err
	}

	var norm float64
	for _, f := range v {
		norm += float64(f) * float64(f)
	}
	norm = math.Sqrt(norm)

	out := make([]float32, len(v))
	for i, f := range v {
		out[i] = float32(float64(f) / norm)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// The embedder
// ---------------------------------------------------------------------------

// EmbedRuntime is the ONNX runtime, as an interface.
//
// An interface rather than a concrete dependency so this package has no
// import of a native library, and so a test can prove the digest is verified
// before the runtime is touched -- which is only observable if touching it is
// countable.
type EmbedRuntime interface {
	// LoadModel parses and initialises a model file. Calling this on
	// unverified bytes is the whole hazard.
	LoadModel(path string) error

	// Embed runs the model over a preprocessed face crop, returning a vector
	// whose length is the model's output dimension -- decided by the model, not
	// by this code.
	Embed(crop []byte) ([]float32, error)

	// Dimension reports the output dimension, or 0 if no model is loaded.
	Dimension() int
}

// Embedder produces face embeddings from a verified model.
//
// Construction is the only place the model is loaded, and it is the only place
// the digest is checked. That is deliberate: a caller that has an Embedder
// holds a model whose bytes matched a pin, and a caller that does not has
// nothing.
type Embedder struct {
	runtime    EmbedRuntime
	digest     string
	dimension  int
	modelReady bool
}

// NewEmbedder verifies the pin and loads the model.
//
// The check order is the same one the detector uses, for the same reason: a
// malformed pin is a configuration fault and says nothing about the
// filesystem, so reporting "model missing" would send the operator to fix the
// wrong thing. And the digest is verified before ANY parsing, so the largest,
// most bug-prone code in the path never sees untrusted input.
func NewEmbedder(rt EmbedRuntime, expectedDigest string) (*Embedder, error) {
	digest, err := normalizeDigest(expectedDigest)
	if err != nil {
		return nil, err
	}
	if rt == nil {
		return nil, &UnavailableError{
			Reason: ReasonNoRuntime,
			Detail: "no ONNX runtime was provided to the embedder",
		}
	}
	return &Embedder{runtime: rt, digest: digest}, nil
}

// ReasonDigestUnreadable means the model file could not be read.
//
// Its own reason, separate from ReasonDigestMalformed (the PIN is bad) and
// ReasonDigestMismatch (the file is not what the pin says). The three are
// different operator actions: fix the pin, get the right file, fix the
// permissions. Collapsing the first two into "unverified" is what makes an
// operator re-download a model whose PIN is a typo.
const ReasonDigestUnreadable UnavailableReason = "digest-unreadable"

// ReasonNoRuntime is the unavailability reason for a missing runtime.
//
// Part of the Reason set from step 2.4b.0, so an unavailable embedder and an
// unavailable detector are reported through the same vocabulary. A caller
// handling one and not the other will read a face-free library as a finding.
const ReasonNoRuntime = "runtime_unavailable"

// Attach loads a model, verifying the file's bytes against the pin FIRST.
//
// Separate from NewEmbedder so the digest check and the load can be observed
// independently in a test -- and so a caller can construct the embedder before
// it knows where the model lives.
func (e *Embedder) Attach(modelPath string) error {
	if e.runtime == nil {
		return &UnavailableError{
			Reason: ReasonNoRuntime,
			Detail: "no ONNX runtime; the model was never verified because " +
				"there was nothing to verify it with",
		}
	}

	// The digest is COMPUTED HERE, from the bytes, and not accepted from the
	// caller.
	//
	// The first version took an `actualDigest` argument and compared it. That is
	// a caller attesting to the file's hash and this function checking the
	// attestation -- so a caller that passed the expected digest for a model it
	// had not hashed got a "verified" embedder for arbitrary bytes, and the
	// whole check was theatre. A test caught it: a well-formed pin with a
	// different model was accepted.
	//
	// Same property the detector has, and for the same reason. The model is
	// untrusted input; the verification has to be of the bytes that will be
	// parsed, by the code that parses them.
	actual, err := digestFile(modelPath)
	if err != nil {
		return &UnavailableError{
			Reason: ReasonDigestUnreadable,
			Detail: fmt.Sprintf("%s: %v", modelPath, err),
		}
	}

	if actual != e.digest {
		// ReasonDigestMismatch rather than a new sentinel: the vocabulary from
		// step 2.4b.0 belongs to the feature, and a caller that knows how to
		// report an unverified DETECTOR should not have to learn a second
		// dialect for an unverified EMBEDDER.
		return &UnavailableError{
			Reason: ReasonDigestMismatch,
			Detail: fmt.Sprintf("model is %s, expected %s; the model is "+
				"untrusted input and is not parsed until this matches",
				actual, e.digest),
		}
	}

	// Only now is the runtime touched.
	if err := e.runtime.LoadModel(modelPath); err != nil {
		return fmt.Errorf("load model: %w", err)
	}
	e.dimension = e.runtime.Dimension()
	if e.dimension <= 0 {
		return fmt.Errorf("%w: model reports dimension %d", ErrInvalidEmbedding, e.dimension)
	}
	e.modelReady = true
	return nil
}

// Dimension reports the model's output dimension, or 0 if none is attached.
func (e *Embedder) Dimension() int {
	if !e.modelReady {
		return 0
	}
	return e.dimension
}

// Embed returns a validated, normalised embedding for one face crop.
//
// The validation is not redundant with Attach: a model can be verified and
// still return a zero vector for a black crop, and that vector would match
// nothing while looking like a result.
func (e *Embedder) Embed(crop []byte) ([]float32, error) {
	if !e.modelReady {
		// Deliberately not an empty result. The whole of step 2.4b.0 applies
		// here: an embedder that cannot run must not look like a face it did
		// not find.
		return nil, &UnavailableError{
			Reason: ReasonNoRuntime,
			Detail: "no verified model is attached to this embedder",
		}
	}

	raw, err := e.runtime.Embed(crop)
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if err := ValidateEmbedding(raw); err != nil {
		return nil, err
	}
	if len(raw) != e.dimension {
		// A model that changes its output width between calls is not a model
		// this code can use, and every distance it feeds is calibrated against
		// the width it had before.
		return nil, fmt.Errorf("%w: model returned %d, expected %d",
			ErrDimensionMismatch, len(raw), e.dimension)
	}
	return Normalize(raw)
}

// normalizeDigest applies the DETECTOR's pin policy and returns the canonical
// form.
//
// The first version of this file reimplemented the policy -- trim, strip a
// "sha256:" prefix, require 64 hex chars -- a second copy of something
// detector.go already owns. Two copies of "is this a usable pin" drift, and the
// drift is invisible until a model is accepted by one and refused by the other.
// It also emitted ErrDigestMalformed, a sentinel that does not exist, because
// the detector expresses that condition as an UnavailableError REASON. So this
// calls checkDigestWellFormed and speaks the same dialect.
func normalizeDigest(s string) (string, error) {
	if err := checkDigestWellFormed(s); err != nil {
		return "", err
	}
	canonical := strings.ToLower(strings.TrimSpace(s))
	return strings.TrimPrefix(canonical, "sha256:"), nil
}

// digestFile computes the SHA-256 of a file's contents.
//
// Streaming rather than reading it whole: a face model is tens of megabytes,
// and a library holding several of them would otherwise allocate the lot just to
// hash one. io.Copy into the hash is the whole implementation.
func digestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
