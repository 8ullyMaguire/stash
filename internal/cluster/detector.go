package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// Face detection and model trust. StashForge M2c, step 2.4b.0.
//
// # The property this package exists to protect
//
// A detector that cannot run must not report that it found nothing. The empty
// result is not neutral: it becomes a permanent-looking fact about the user's
// library. A missing model file is a one-line fix; the index it corrupted looks
// like a finding about their content, and nothing later distinguishes "this
// library has no faces" from "we never looked".
//
// So every unavailability is a typed error carrying a reason, and there is no
// code path that returns an empty face list for any reason other than "the
// engine ran and found no faces".

// UnavailableReason says why a detector could not run.
//
// A string type rather than a bare error so callers can branch — the UI needs
// to distinguish "install the model" from "the pin is wrong" from "rebuild with
// ONNX support" — and a bare error message would make that string matching.
type UnavailableReason string

const (
	// ReasonModelMissing means the model file is not there.
	ReasonModelMissing UnavailableReason = "model-missing"

	// ReasonDigestMalformed means the PIN is unusable: empty, truncated, not
	// hex, or otherwise not 32 bytes.
	//
	// Deliberately distinct from ReasonDigestMismatch. A truncated digest can
	// never equal any SHA-256, so a check that merely *compares* refuses the
	// model too — but by accident. The difference appears the moment the pin is
	// wrong for any other reason: a placeholder never replaced, or a character
	// dropped in transcription. Under a comparing implementation those are
	// indistinguishable from a corrupt model, so the operator is told to
	// distrust a file that is fine.
	ReasonDigestMalformed UnavailableReason = "digest-malformed"

	// ReasonDigestMismatch means the pin was well-formed and the model does not
	// match it. That is a fact about the model.
	ReasonDigestMismatch UnavailableReason = "digest-mismatch"

	// ReasonRuntimeAbsent means no engine is available to execute a verified
	// model — a build without ONNX support, say.
	ReasonRuntimeAbsent UnavailableReason = "runtime-absent"
)

// UnavailableError is returned whenever a detector cannot run.
//
// The type is exported and the Reason is a field rather than baked into the
// message so a caller can switch on it; Error() is built for a human and must
// stay changeable without breaking that.
type UnavailableError struct {
	Reason UnavailableReason

	// Detail is the actionable half: the path, the digests, the remedy. A
	// reason alone ("model-missing") leaves an operator hunting for which file,
	// on which volume, under which user.
	Detail string
}

func (e *UnavailableError) Error() string {
	if e.Detail == "" {
		return string(e.Reason)
	}
	return string(e.Reason) + ": " + e.Detail
}

// Is lets errors.Is match on the reason without a sentinel per case, so a
// caller can write errors.Is(err, &UnavailableError{Reason: ReasonDigestMismatch})
// if it ever needs to.
func (e *UnavailableError) Is(target error) bool {
	t, ok := target.(*UnavailableError)
	return ok && t.Reason == e.Reason
}

// Face is one detected face in a frame, in pixel coordinates.
type Face struct {
	Left, Top, Width, Height int

	// Score is the detector's confidence, 0..1. Carried through so the
	// clustering stage can threshold on it; a face below the threshold is not
	// the same claim as one above it.
	Score float64
}

// Frame is one sampled keyframe handed to the detector.
type Frame struct {
	// Index is the sample ordinal, not a timestamp. Ordering and coverage are
	// what the sample budget controls, and an index makes the last-sample
	// assertion (step 2.4b.1) a direct comparison.
	Index int

	// Bytes is the encoded image. Opaque here — the engine's business.
	Bytes []byte
}

// Engine executes a verified model over a frame.
//
// An interface so the availability rules are testable without ONNX. The
// alternative is a build tag, which means the guard that matters most — "no
// runtime must fail rather than return empty" — is only exercised in builds
// nobody runs.
type Engine interface {
	DetectFaces(ctx context.Context, image []byte) ([]Face, error)
}

// Detector runs a verified face model over frames.
type Detector struct {
	modelPath string
	engine    Engine
}

// NewDetector verifies the model and returns a runnable detector, or an
// *UnavailableError saying exactly why it could not.
//
// The order is load-bearing and is the security property, so it is stated once:
//
//	1. the pin is well-formed
//	2. the model file exists
//	3. the digest matches
//	4. a runtime is available
//
// (1) before (2) because a malformed pin is a fault in the configuration and
// says nothing about the file; reporting "model missing" for a caller that
// passed a bad digest sends them to fix the wrong thing.
//
// (3) before (4) because a digest failure is a security problem and a missing
// runtime is an availability one. Checking the other way round reports "install
// ONNX" while the model on disk is not the model that was pinned, and the
// operator fixes the runtime and re-runs having already left unverified bytes
// in place.
//
// (3) before any parsing, per the spec: verifying first means the parser —
// the largest, most complex, most bug-prone code in the path — never sees
// untrusted input.
func NewDetector(modelPath, expectedDigest string, engine Engine) (*Detector, error) {
	// (1) the pin, before touching the filesystem at all.
	if err := checkDigestWellFormed(expectedDigest); err != nil {
		return nil, err
	}

	// (2) existence, so a missing file is reported as a missing file rather
	// than as a mismatch against the pin.
	data, err := os.ReadFile(modelPath)
	if err != nil {
		return nil, &UnavailableError{
			Reason: ReasonModelMissing,
			Detail: fmt.Sprintf("the face model could not be read from %q: %v. "+
				"Fetch it and set the path in the configuration; do not treat "+
				"this library as face-free", modelPath, err),
		}
	}

	// (3) identity, before parsing or executing a single byte.
	if err := VerifyModelDigest(data, expectedDigest); err != nil {
		return nil, err
	}

	// (4) only now, having established what the bytes are.
	if engine == nil {
		return nil, &UnavailableError{
			Reason: ReasonRuntimeAbsent,
			Detail: "the model is present and verified, but no ONNX runtime is " +
				"available to execute it. Rebuild with ONNX support, or install " +
				"the runtime; clustering is disabled rather than reporting no " +
				"faces",
		}
	}

	return &Detector{modelPath: modelPath, engine: engine}, nil
}

// Detect runs the model over one frame.
//
// Returns an error for every failure, including an engine that failed
// mid-frame, and returns no partial results alongside one. A caller that gets
// `(faces, err)` with both populated has no way to know whether the faces it
// holds are the whole set.
func (d *Detector) Detect(ctx context.Context, frame Frame) ([]Face, error) {
	if d == nil || d.engine == nil {
		// A zero Detector is reachable if someone declares one instead of
		// calling NewDetector, and it is the exact shape this package exists
		// to prevent: a detector that reports nothing without saying why.
		return nil, &UnavailableError{
			Reason: ReasonRuntimeAbsent,
			Detail: "Detect was called on a detector that was never constructed " +
				"by NewDetector; there is no verified model behind it",
		}
	}

	faces, err := d.engine.DetectFaces(ctx, frame.Bytes)
	if err != nil {
		// Wrapped so the engine's own error survives for the caller, and
		// reported as a failure rather than as an empty result.
		return nil, fmt.Errorf("detecting faces in frame %d: %w", frame.Index, err)
	}
	if faces == nil {
		// An engine that returns a nil slice is indistinguishable from one that
		// found nothing; normalise so the caller's len() is meaningful.
		return []Face{}, nil
	}
	return faces, nil
}

// ModelPath is where the verified model lives, for diagnostics.
func (d *Detector) ModelPath() string {
	if d == nil {
		return ""
	}
	return d.modelPath
}

// VerifyModelDigest checks a model's bytes against a pinned SHA-256.
//
// Exported because a caller may want to verify a model before staging it, and
// because a security property that is only reachable through one constructor is
// one path away from being skipped.
func VerifyModelDigest(data []byte, expectedDigest string) error {
	// The pin is checked first, and separately from the comparison. A pin that
	// cannot be trusted must not be compared against anything: a truncated one
	// refuses by accident rather than by decision.
	if err := checkDigestWellFormed(expectedDigest); err != nil {
		return err
	}

	want, err := hex.DecodeString(expectedDigest)
	if err != nil {
		// Unreachable given the well-formedness check, but the decode is what
		// makes the comparison below a comparison of bytes rather than of
		// strings, and a future edit to the check must not turn it into a string
		// compare without this being noticed.
		return &UnavailableError{
			Reason: ReasonDigestMalformed,
			Detail: fmt.Sprintf("the pinned digest %q is not valid hex: %v", expectedDigest, err),
		}
	}

	got := sha256.Sum256(data)

	// Constant-time. The value compared is a digest of a file the attacker may
	// control, so a timing-variable comparison leaks information about how much
	// of a guess was right. It is a small thing to get right by default.
	if subtle.ConstantTimeCompare(want, got[:]) != 1 {
		return &UnavailableError{
			Reason: ReasonDigestMismatch,
			Detail: fmt.Sprintf("the model does not match its pinned digest "+
				"(pinned %s, actual %s). Refusing to load it: a face model is "+
				"executed, so an unverified one is arbitrary code",
				expectedDigest, hex.EncodeToString(got[:])),
		}
	}
	return nil
}

// checkDigestWellFormed reports whether a pin is a usable SHA-256.
//
// Separate from VerifyModelDigest so the ordering requirement is expressible: a
// caller that has not read the file yet can still reject a bad pin, and
// NewDetector does exactly that before touching the filesystem.
func checkDigestWellFormed(expectedDigest string) error {
	malformed := func(detail string) error {
		return &UnavailableError{
			Reason: ReasonDigestMalformed,
			Detail: detail + ". A malformed pin cannot be compared against " +
				"anything, so the model is refused rather than trusted or " +
				"rejected; copy the digest again from wherever it is pinned",
		}
	}

	// Whitespace is trimmed rather than refused, because a pin transcribed out
	// of a YAML file or a shell variable very often arrives padded, and that is
	// a formatting accident rather than an attack. Everything else is refused.
	trimmed := strings.TrimSpace(expectedDigest)
	if trimmed == "" {
		return malformed("the pinned digest is empty")
	}

	// A common convention, accepted rather than refused, for the same reason as
	// the trimming: it is a naming convention, not a different algorithm.
	trimmed = strings.TrimPrefix(trimmed, "sha256:")

	if len(trimmed) != sha256.Size*2 {
		return malformed(fmt.Sprintf(
			"the pinned digest is %d characters, want %d (SHA-256 is %d bytes, "+
				"hex-encoded as %d characters)", len(trimmed), sha256.Size*2,
			sha256.Size, sha256.Size*2))
	}

	// hex.DecodeString is the test, and it is strict: it rejects non-hex
	// characters rather than skipping them, which is what makes "64 z's" a
	// malformed pin rather than a well-formed one that happens to be wrong.
	if _, err := hex.DecodeString(trimmed); err != nil {
		return malformed(fmt.Sprintf("the pinned digest %q is not hex", expectedDigest))
	}
	return nil
}
