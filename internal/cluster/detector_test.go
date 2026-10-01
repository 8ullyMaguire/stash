package cluster

// Step 2.4b.0, tests written BEFORE the model they specify.
//
// This file is the specification. The implementation in detector.go and
// model.go exists to satisfy it, and the commit order is deliberate: these two
// behaviours are the ones the spec calls "easy to get wrong", and a test
// written afterwards tends to encode whatever the code happened to do.
//
// The dangerous behaviour, stated once so every test below is recognisable as
// an instance of it:
//
//	A DETECTOR THAT CANNOT RUN MUST NOT RETURN AN EMPTY RESULT.
//
// `faces, err := d.Detect(ctx, frame)` with a broken detector that returns
// `(nil, nil)` reads to the caller as "this frame contains no faces". That
// claim then propagates: the frame is not indexed, the library index records
// zero faces for it, and the user is told — or silently left with the
// impression — that their content is face-free. Nothing errors. The index looks
// complete.
//
// And it is permanent-looking. A missing model file is a one-line fix. The
// index it corrupted looks like a finding about the user's library, and no
// amount of later re-running distinguishes "this library has no faces" from
// "we never looked".

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Known answers, so "it returned an error" is not the only thing asserted
// ---------------------------------------------------------------------------

const knownModel = "not a real model, but its bytes are stable across runs"

// goodDigest is the correct pinned digest for knownModel, computed rather than
// pasted. A hardcoded digest in a test is a digest that can be wrong without
// the test noticing, and a test whose fixture digest is wrong will "prove" the
// comparison rejects everything.
func goodDigest() string {
	sum := sha256.Sum256([]byte(knownModel))
	return hex.EncodeToString(sum[:])
}

func writeModel(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.onnx")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing the model fixture: %v", err)
	}
	return path
}

// stubEngine is a runnable detector engine, so the availability tests exercise
// the guard rather than the absence of ONNX.
type stubEngine struct {
	faces []Face
	err   error
}

func (s stubEngine) DetectFaces(_ context.Context, _ []byte) ([]Face, error) {
	return s.faces, s.err
}

// ---------------------------------------------------------------------------
// 1. A detector that cannot run fails visibly, with a reason
// ---------------------------------------------------------------------------

// TestDetectorNeverReportsNoFacesWhenItCannotRun is the single most important
// test in this milestone.
//
// Every row is a way the detector can be unable to run. For each, the assertion
// is that Detect returns a NON-NIL ERROR — because the failure mode is not a
// wrong answer, it is a plausible one.
func TestDetectorNeverReportsNoFacesWhenItCannotRun(t *testing.T) {
	// A digest that is well-formed and correct, for rows that are not about it.
	ok := goodDigest()
	// A well-formed digest of the wrong bytes.
	wrong := hex.EncodeToString(func() []byte {
		sum := sha256.Sum256([]byte("some other model"))
		return sum[:]
	}())

	tests := []struct {
		name       string
		modelPath  string // "" means the file does not exist
		digest     string
		engine     Engine
		wantReason UnavailableReason
	}{
		{
			name:       "the model file is missing",
			modelPath:  "",
			digest:     ok,
			engine:     stubEngine{},
			wantReason: ReasonModelMissing,
		},
		{
			// Malformed pin AND no file. The PIN wins, and deliberately: a bad
			// pin is a fault in the configuration and says nothing about the
			// filesystem, so reporting "model missing" would send an operator
			// to fix the wrong thing. The first version of this row expected
			// ReasonModelMissing and the test caught the disagreement — the
			// implementation was right and the row was written from the wrong
			// mental order.
			name:       "the model file is absent AND the digest is malformed",
			modelPath:  "",
			digest:     "abc",
			engine:     stubEngine{},
			wantReason: ReasonDigestMalformed,
		},
		{
			name:       "the digest does not match the model",
			modelPath:  writeModel(t, knownModel),
			digest:     wrong,
			engine:     stubEngine{},
			wantReason: ReasonDigestMismatch,
		},
		{
			name:       "the expected digest is empty",
			modelPath:  writeModel(t, knownModel),
			digest:     "",
			engine:     stubEngine{},
			wantReason: ReasonDigestMalformed,
		},
		{
			name:       "the expected digest is truncated",
			modelPath:  writeModel(t, knownModel),
			digest:     goodDigest()[:32],
			engine:     stubEngine{},
			wantReason: ReasonDigestMalformed,
		},
		{
			name:       "the expected digest is not hex",
			modelPath:  writeModel(t, knownModel),
			digest:     strings.Repeat("z", 64),
			engine:     stubEngine{},
			wantReason: ReasonDigestMalformed,
		},
		{
			name:       "the runtime is absent",
			modelPath:  writeModel(t, knownModel),
			digest:     ok,
			engine:     nil,
			wantReason: ReasonRuntimeAbsent,
		},
		{
			// A wrong digest AND no runtime. The digest wins, and
			// TestDetectorReportsTheSecurityProblemFirst explains why.
			name:       "the digest is wrong and the runtime is absent",
			modelPath:  writeModel(t, knownModel),
			digest:     wrong,
			engine:     nil,
			wantReason: ReasonDigestMismatch,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// NewDetector is allowed to fail — a detector that cannot be
			// constructed must not be handed out. The contract is that it either
			// returns a usable detector or an error that says why, and never a
			// usable-looking detector that fails later.
			d, err := NewDetector(tc.modelPath, tc.digest, tc.engine)
			if err == nil {
				_, err = d.Detect(context.Background(), Frame{Index: 0})
			}

			if err == nil {
				t.Fatalf("Detect returned no error for %s; a detector that "+
					"cannot run must fail, because an empty result is "+
					"indistinguishable from a frame with no faces and permanently "+
					"records the user's library as face-free", tc.name)
			}

			// "With a reason" means a typed, specific one. A generic error is
			// what a caller sees when something unforeseen went wrong, and a
			// caller cannot act on that.
			var unavail *UnavailableError
			if !errors.As(err, &unavail) {
				t.Fatalf("error is %T (%v), want *UnavailableError; a caller "+
					"cannot distinguish 'the model is missing' from 'the database "+
					"is down' without the type", err, err)
			}
			if unavail.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", unavail.Reason, tc.wantReason)
			}
			if unavail.Detail == "" {
				t.Error("Detail is empty; the reason alone does not tell an " +
					"operator what to fix")
			}
		})
	}
}

// TestUnavailableErrorNamesTheFile is what makes the failure actionable. A
// reason of "model-missing" without the path leaves an operator hunting.
func TestUnavailableErrorNamesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "face-detector.onnx")

	_, err := NewDetector(path, goodDigest(), stubEngine{})
	if err == nil {
		t.Fatal("expected an error for a missing model")
	}

	if !strings.Contains(err.Error(), "face-detector.onnx") {
		t.Errorf("error %q does not name the missing file; an operator cannot "+
			"act on a reason that does not say which file", err)
	}
}

// TestDetectorReportsTheSecurityProblemFirst pins the precedence order, and the
// order is a decision rather than an accident.
//
// Three things can be wrong: the file is missing, the digest is wrong, the
// runtime is absent. Reporting "runtime absent" while the digest is also wrong
// hides a security failure behind an availability one — the operator fixes the
// runtime, re-runs, and meets the digest problem for the first time, having
// already executed whatever was on disk in the meantime. The digest is checked
// before the runtime for the same reason the spec puts it before parsing.
func TestDetectorReportsTheSecurityProblemFirst(t *testing.T) {
	// Model present, digest wrong, no runtime.
	_, err := NewDetector(writeModel(t, knownModel), hex.EncodeToString(make([]byte, 32)), nil)
	if err == nil {
		t.Fatal("expected an error")
	}

	var unavail *UnavailableError
	if !errors.As(err, &unavail) {
		t.Fatalf("error is %T, want *UnavailableError", err)
	}
	if unavail.Reason != ReasonDigestMismatch {
		t.Errorf("Reason = %q, want %q; a digest failure must not be reported "+
			"as a missing runtime, or the security problem stays hidden until "+
			"after something has already run", unavail.Reason, ReasonDigestMismatch)
	}
}

// TestARunnableDetectorReportsZeroFacesWithoutError is the other direction, and
// it is what makes the first test meaningful.
//
// A detector that works and finds nothing MUST return no error. Without this
// row, the fix for the test above — "always return an error when there are no
// faces" — would satisfy it and break the feature.
func TestARunnableDetectorReportsZeroFacesWithoutError(t *testing.T) {
	d, err := NewDetector(writeModel(t, knownModel), goodDigest(), stubEngine{faces: nil})
	if err != nil {
		t.Fatalf("a runnable detector must construct: %v", err)
	}

	faces, err := d.Detect(context.Background(), Frame{Index: 0})
	if err != nil {
		t.Fatalf("Detect on a runnable detector with no faces returned %v; "+
			"zero faces is a finding, not a fault", err)
	}
	if len(faces) != 0 {
		t.Errorf("got %d faces, want 0", len(faces))
	}
}

// TestARunnableDetectorReturnsWhatTheEngineFound is the positive control: a
// working detector actually surfaces faces, so the zero-face case above is
// distinguishable from a detector that never calls anything.
func TestARunnableDetectorReturnsWhatTheEngineFound(t *testing.T) {
	want := []Face{{Left: 1, Top: 2, Width: 30, Height: 40, Score: 0.91}}
	d, err := NewDetector(writeModel(t, knownModel), goodDigest(), stubEngine{faces: want})
	if err != nil {
		t.Fatalf("constructing: %v", err)
	}

	got, err := d.Detect(context.Background(), Frame{Index: 7})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("Detect returned %+v, want %+v", got, want)
	}
}

// TestAZeroDetectorCannotReportNoFaces closed a mutation-survival gap.
//
// The guard in Detect covers a `Detector` that was never built by NewDetector —
// declared as a zero value, or set to nil. Mutation B removed that guard and
// EVERY test in the file still passed, because nothing constructed one.
//
// That is the spec's most dangerous behaviour with no coverage at all: a
// `var d cluster.Detector` in some future caller would have found no faces,
// indexed the frame as clean, and reported no error. The test above covers the
// cases that go through the constructor; this one covers the one that does not.
func TestAZeroDetectorCannotReportNoFaces(t *testing.T) {
	t.Run("nil detector", func(t *testing.T) {
		var d *Detector
		faces, err := d.Detect(context.Background(), Frame{Index: 0})

		if err == nil {
			t.Fatalf("a nil Detector returned %d faces and no error; it has no "+
				"verified model behind it and cannot be trusted to find nothing",
				len(faces))
		}
		var unavail *UnavailableError
		if !errors.As(err, &unavail) {
			t.Fatalf("error is %T, want *UnavailableError", err)
		}
		if unavail.Reason != ReasonRuntimeAbsent {
			t.Errorf("Reason = %q, want %q", unavail.Reason, ReasonRuntimeAbsent)
		}
	})

	t.Run("zero-value detector", func(t *testing.T) {
		// Not a pointer: the struct is exported and copyable, so `var d
		// Detector` is the more likely mistake and the one a pointer-only
		// check would miss.
		var d Detector
		faces, err := d.Detect(context.Background(), Frame{Index: 0})

		if err == nil {
			t.Fatalf("a zero-value Detector returned %d faces and no error; it "+
				"was never verified against a pinned digest, so it cannot report "+
				"an absence of faces", len(faces))
		}
		var unavail *UnavailableError
		if !errors.As(err, &unavail) {
			t.Fatalf("error is %T, want *UnavailableError", err)
		}
		if unavail.Reason != ReasonRuntimeAbsent {
			t.Errorf("Reason = %q, want %q", unavail.Reason, ReasonRuntimeAbsent)
		}
	})
}

// TestModelPathOnAZeroDetectorDoesNotPanic: the accessor is the thing a
// diagnostics path reaches for, and it must not be the panic that reveals the
// mistake.
func TestModelPathOnAZeroDetectorDoesNotPanic(t *testing.T) {
	defer func() {
		if rec := recover(); rec != nil {
			t.Errorf("ModelPath panicked on a zero detector: %v", rec)
		}
	}()

	var d *Detector
	if got := d.ModelPath(); got != "" {
		t.Errorf("ModelPath = %q on a nil detector, want empty", got)
	}

	var zero Detector
	if got := zero.ModelPath(); got != "" {
		t.Errorf("ModelPath = %q on a zero detector, want empty", got)
	}
}

// TestAnEngineFailurePropagates: a runtime that errors is not a runtime that
// found nothing, and the error must reach the caller rather than becoming an
// empty face list.
func TestAnEngineFailurePropagates(t *testing.T) {
	sentinel := errors.New("onnx session died")
	d, err := NewDetector(writeModel(t, knownModel), goodDigest(), stubEngine{err: sentinel})
	if err != nil {
		t.Fatalf("constructing: %v", err)
	}

	faces, err := d.Detect(context.Background(), Frame{Index: 0})
	if !errors.Is(err, sentinel) {
		t.Errorf("Detect error = %v, want the engine's own error", err)
	}
	if len(faces) != 0 {
		t.Errorf("got %d faces alongside an error; a partial result must not be "+
			"returned as if it were complete", len(faces))
	}
}

// ---------------------------------------------------------------------------
// 2. A malformed expected digest is REFUSED, not compared
// ---------------------------------------------------------------------------

// TestMalformedExpectedDigestIsRefusedNotCompared is the second required test.
//
// The distinction is invisible in the result and enormous in the reasoning. A
// truncated digest can never equal any 32-byte SHA-256, so a check that
// *compares* does refuse the model — by accident. A check that *refuses* the
// digest does so because the pin is malformed and therefore cannot be trusted
// to mean anything.
//
// The difference shows up the moment the pin is wrong for a reason other than
// truncation. A digest copied with a character dropped, or a placeholder that
// was never replaced, refuses identically to a real mismatch under a comparing
// implementation — so the operator is told "the model is corrupt" and goes
// looking for corruption in a file that is fine.
func TestMalformedExpectedDigestIsRefusedNotCompared(t *testing.T) {
	valid := goodDigest()

	tests := []struct {
		name   string
		digest string
	}{
		{"empty", ""},
		{"one character", "a"},
		{"truncated to half", valid[:32]},
		{"one character short", valid[:63]},
		{"one character long", valid + "0"},
		{"not hex", strings.Repeat("g", 64)},
		{"hex with a space", valid[:32] + " " + valid[32:]},
		{"a bare filename", "model.onnx"},
		{"the word sha256", "sha256:" + valid},
		{"whitespace padded", "  " + valid + "  "},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyModelDigest([]byte(knownModel), tc.digest)
			if err == nil {
				t.Fatalf("VerifyModelDigest accepted the malformed digest %q; a "+
					"malformed pin must be refused, because a pin that cannot be "+
					"trusted must not be compared against anything", tc.digest)
			}

			var unavail *UnavailableError
			if !errors.As(err, &unavail) {
				t.Fatalf("error is %T, want *UnavailableError", err)
			}
			// The distinction the whole test exists for: MALFORMED, not
			// MISMATCH. A mismatching digest is a fact about the model; a
			// malformed one is a fact about the pin, and only the second means
			// the check is not doing what it claims.
			if unavail.Reason != ReasonDigestMalformed {
				t.Errorf("Reason = %q, want %q; a malformed pin reported as a "+
					"mismatch tells the operator to distrust a model that may be "+
					"perfectly fine", unavail.Reason, ReasonDigestMalformed)
			}
		})
	}
}

// TestWellFormedDigestsAreAccepted: the refusal above must not be so eager that
// it refuses valid pins. A checker that rejects everything is safe and useless.
func TestWellFormedDigestsAreAccepted(t *testing.T) {
	valid := goodDigest()
	upper := strings.ToUpper(valid)

	tests := []struct {
		name   string
		digest string
	}{
		{"the correct digest", valid},
		// Uppercase is the same 32 bytes written differently, and a pin
		// transcribed from a tool that prints uppercase is a normal thing to
		// encounter. Refusing it would send an operator to look for corruption
		// in a model that is fine.
		{"uppercase", upper},
		{"mixed case", strings.ToUpper(valid[:32]) + valid[32:]},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := VerifyModelDigest([]byte(knownModel), tc.digest); err != nil {
				t.Errorf("VerifyModelDigest rejected the well-formed digest %q: %v", tc.digest, err)
			}
		})
	}
}

// TestDigestMismatchIsDistinctFromMalformed: the two are different diagnoses
// and the test above would pass if both were reported as one.
func TestDigestMismatchIsDistinctFromMalformed(t *testing.T) {
	other := sha256.Sum256([]byte("a different model entirely"))
	err := VerifyModelDigest([]byte(knownModel), hex.EncodeToString(other[:]))
	if err == nil {
		t.Fatal("a wrong digest must be refused")
	}

	var unavail *UnavailableError
	if !errors.As(err, &unavail) {
		t.Fatalf("error is %T, want *UnavailableError", err)
	}
	if unavail.Reason != ReasonDigestMismatch {
		t.Errorf("Reason = %q, want %q", unavail.Reason, ReasonDigestMismatch)
	}
}

// TestTheDigestIsVerifiedBeforeAnyParsing: the spec says "before any byte of it
// is parsed", and that ordering is the whole security property.
//
// A model file is a protobuf-ish ONNX graph. Parsing untrusted bytes before
// checking who sent them means the parser — the largest, most complex,
// most bug-prone code in the path — is the attack surface. Verifying first
// means an attacker who can substitute the file still has to defeat SHA-256.
//
// Observable consequence, and the reason this is testable: given a model whose
// bytes are NOT a valid ONNX graph AND a malformed digest, the reported reason
// must be the digest. A parse failure first would mean the file was opened and
// walked before its provenance was established.
func TestTheDigestIsVerifiedBeforeAnyParsing(t *testing.T) {
	// Bytes that are emphatically not a valid model: a bare protobuf field
	// header claiming a length far beyond the file.
	garbage := []byte{0x08, 0xff, 0xff, 0xff, 0xff, 0x0f, 0x01, 0x02}

	err := VerifyModelDigest(garbage, "not-a-digest")
	if err == nil {
		t.Fatal("expected an error")
	}

	var unavail *UnavailableError
	if !errors.As(err, &unavail) {
		t.Fatalf("error is %T, want *UnavailableError", err)
	}
	if unavail.Reason != ReasonDigestMalformed {
		t.Errorf("Reason = %q, want %q; the pin is checked first, so a "+
			"malformed pin is reported even when the bytes are also unparseable",
			unavail.Reason, ReasonDigestMalformed)
	}
}

// TestVerificationIsOverTheWholeFileNotTheFirstBlock: a digest that covers only
// the header would let an attacker replace the payload.
func TestVerificationIsOverTheWholeFileNotTheFirstBlock(t *testing.T) {
	full := []byte(knownModel)
	err := VerifyModelDigest(full, goodDigest())
	if err != nil {
		t.Fatalf("the whole-file digest must verify: %v", err)
	}

	// Same first bytes, different tail. A header-only check would pass this.
	tampered := append([]byte(nil), full...)
	tampered[len(tampered)-1] ^= 0xff

	sum := sha256.Sum256(tampered)
	if err := VerifyModelDigest(tampered, hex.EncodeToString(sum[:])); err != nil {
		t.Errorf("the digest of the tampered file must be self-consistent: %v", err)
	}
	if err := VerifyModelDigest(tampered, goodDigest()); err == nil {
		t.Error("a one-bit change in the LAST byte passed verification; the " +
			"digest must cover the whole file, not a header")
	}
}
