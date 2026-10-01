package cluster

import (
	"context"
	"fmt"
	"testing"
)

// Tests for the detection and embedding stage.
//
// # What these are for
//
// The observe stage is the half of a clustering job that talks to the outside
// world: it decodes frames, runs a model, and turns crops into vectors. Every
// way it can go wrong here is a way a user sees "the clustering pass failed"
// with no idea which part failed, so the tests are mostly about WHICH failure
// is reported and WHICH targets are skipped rather than about the arithmetic --
// the arithmetic is the pass's, and it has its own tests.
//
// The fakes stand in for a decoder and two models. That is the same choice the
// detector made with its Engine interface, for the same reason: a build tag
// would mean the rule that matters most is only exercised in builds nobody
// runs.

// fakeFrames is a FrameSource that serves canned bytes, and can be told to
// fail for specific targets or samples.
type fakeFrames struct {
	calls []int // the sample indices requested, in order

	// failPath makes every sample of that path fail.
	failPath string

	// failSample fails one sample of one target, leaving the rest readable.
	failSample int
	failOn     int64
}

func (f *fakeFrames) Frame(ctx context.Context, t Target, sampleIndex int) ([]byte, error) {
	f.calls = append(f.calls, sampleIndex)
	if t.Path == f.failPath {
		return nil, fmt.Errorf("container reports an unreadable header")
	}
	if sampleIndex == f.failSample && t.TargetID == f.failOn {
		return nil, fmt.Errorf("no frame at %d", sampleIndex)
	}
	return []byte("frame:" + t.Path + ":" + fmt.Sprint(sampleIndex)), nil
}

// fakeDetector returns fixed faces, and can be told to fail.
type fakeDetector struct {
	faces []Face
	err   error
	calls int
}

func (d *fakeDetector) Detect(ctx context.Context, f Frame) ([]Face, error) {
	d.calls++
	if d.err != nil {
		return nil, d.err
	}
	return d.faces, nil
}

// fakeCropper passes the frame through, or fails.
type fakeCropper struct {
	err error
}

func (c *fakeCropper) Crop(frame []byte, f Face) ([]byte, error) {
	if c.err != nil {
		return nil, c.err
	}
	return append(frame, []byte(fmt.Sprintf("|crop:%d", f.Left))...), nil
}

// fakeEmbedder returns a real spread vector at a given angle, so the output is
// something the pass would actually accept.
type fakeEmbedder struct {
	angle float64
	err   error
	calls int
}

func (e *fakeEmbedder) Embed(crop []byte) ([]float32, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	return angVec(e.angle), nil
}

// newObserver wires the fakes together.
func newObserver(t *testing.T, det FaceDetector, src FrameSource, crop Cropper, emb FaceEmbedder, opts ObserveOptions) *Observer {
	t.Helper()
	o, err := NewObserver(det, src, crop, emb, opts)
	if err != nil {
		t.Fatalf("building an observer with valid parts: %v", err)
	}
	return o
}

// target is a short video with plenty of samples.
func target(id int64, path string) Target {
	return Target{TargetType: "scene", TargetID: id, Path: path, DurationSeconds: 120}
}

// --- the happy path ---

// TestObserverProducesOneObservationPerFace is the shape of a normal run: every
// detected, confident, embeddable face becomes exactly one observation, located
// precisely enough to be found again.
func TestObserverProducesOneObservationPerFace(t *testing.T) {
	det := &fakeDetector{faces: []Face{
		{Left: 10, Top: 20, Width: 64, Height: 64, Score: 0.9},
		{Left: 200, Top: 30, Width: 48, Height: 48, Score: 0.8},
	}}
	src := &fakeFrames{}
	emb := &fakeEmbedder{angle: 0}

	o := newObserver(t, det, src, &fakeCropper{}, emb, ObserveOptions{
		SampleBudget:   3,
		MinDetectScore: 0.5,
	})

	got, err := o.Run(context.Background(), []Target{target(1, "/a.mkv")})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got.Detected != 2*3 {
		t.Errorf("Detected = %d, want %d (2 faces over 3 samples)", got.Detected, 2*3)
	}
	if len(got.Faces) != 2*3 {
		t.Errorf("%d observations, want %d", len(got.Faces), 2*3)
	}
	if got.FramesRead != 3 {
		t.Errorf("FramesRead = %d, want 3", got.FramesRead)
	}
	if len(got.TargetErrors) != 0 {
		t.Errorf("unexpected target errors on a clean run: %v", got.TargetErrors)
	}

	// Every observation must be locatable and uniquely keyed: two faces in one
	// frame differ only by their box, so a key without the box would make them
	// the same face and the pass would drop one as already-seen.
	seen := map[string]bool{}
	for _, f := range got.Faces {
		if f.Key == "" {
			t.Fatalf("an observation has no key: %+v", f)
		}
		if seen[f.Key] {
			t.Errorf("duplicate key %q; the box is part of the identity and "+
				"without it two faces in one frame are one face", f.Key)
		}
		seen[f.Key] = true
		if f.TargetType != "scene" || f.TargetID != 1 {
			t.Errorf("observation %q is located at %s/%d, want scene/1",
				f.Key, f.TargetType, f.TargetID)
		}
		if len(f.Vector) != EmbeddingDim {
			t.Errorf("observation %q has a %d-wide vector, want %d",
				f.Key, len(f.Vector), EmbeddingDim)
		}
	}
}

// TestObserverSamplesIncludeTheFirstAndLastFrame is the budget's whole purpose,
// asserted through the observe stage rather than only in sample_budget_test.go.
//
// A person who appears only in the closing credits is invisible if the budget
// never samples the end. PlanSamples guarantees the first and last sample; the
// point of this test is that the observe stage actually ASKS for them rather
// than re-deriving a plan of its own.
func TestObserverSamplesIncludeTheFirstAndLastFrame(t *testing.T) {
	src := &fakeFrames{}
	o := newObserver(t, &fakeDetector{}, src, &fakeCropper{}, &fakeEmbedder{},
		ObserveOptions{SampleBudget: 4, MinDetectScore: 0.5})

	tgt := target(1, "/long.mkv")
	tgt.DurationSeconds = 7200 // two hours

	if _, err := o.Run(context.Background(), []Target{tgt}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(src.calls) == 0 {
		t.Fatal("no samples were requested")
	}
	first, last := src.calls[0], src.calls[len(src.calls)-1]
	if first != 0 {
		t.Errorf("first sample is %d, want 0: the opening frame is always "+
			"sampled, and a person who appears only in the titles is otherwise "+
			"never seen", first)
	}
	if last < 7199 {
		t.Errorf("last sample is %d, want at or near 7199 for a 7200-second "+
			"target; the closing credits are as real a place for a face as "+
			"the opening titles", last)
	}
}

// --- the failures, which are the point ---

// TestObserverSkipsAnUnreadableTargetAndKeepsGoing is the availability rule.
//
// One corrupt file in a library of ten thousand must not stop the other 9,999.
// Failing the whole pass turns one bad file into a library that cannot be
// clustered at all, and the user cannot get their clusters back without finding
// and fixing that one file first.
func TestObserverSkipsAnUnreadableTargetAndKeepsGoing(t *testing.T) {
	src := &fakeFrames{failPath: "/broken.mkv"}
	o := newObserver(t, &fakeDetector{faces: []Face{{Score: 0.9}}},
		src, &fakeCropper{}, &fakeEmbedder{angle: 0},
		ObserveOptions{SampleBudget: 2, MinDetectScore: 0.5})

	got, err := o.Run(context.Background(), []Target{
		target(1, "/broken.mkv"), target(2, "/good.mkv"),
	})
	if err != nil {
		t.Fatalf("run returned an error for one unreadable target: %v", err)
	}
	if len(got.TargetErrors) != 1 {
		t.Errorf("TargetErrors has %d entries, want 1: %v", len(got.TargetErrors), got.TargetErrors)
	}
	if _, ok := got.TargetErrors["/broken.mkv"]; !ok {
		t.Errorf("the failure is not attributed to the file that failed: %v", got.TargetErrors)
	}
	if len(got.Faces) == 0 {
		t.Error("no faces from the readable target; one bad file stopped the pass")
	}
	if got.Targets != 2 {
		t.Errorf("Targets = %d, want 2: a target that failed is still a target "+
			"that was attempted, and counting only the successes reports a "+
			"library of one file", got.Targets)
	}
}

// TestObserverKeepsGoingAfterOneBadSample is finer-grained than the target
// case: a single undecodable frame inside an otherwise fine file.
func TestObserverKeepsGoingAfterOneBadSample(t *testing.T) {
	tgt := target(1, "/a.mkv")
	plan := PlanSamples(tgt.DurationSeconds, 3)

	// Fail the MIDDLE sample only, so the first and last still run.
	mid := plan[len(plan)/2]
	src := &fakeFrames{failSample: mid, failOn: 1}
	o := newObserver(t, &fakeDetector{faces: []Face{{Score: 0.9}}},
		src, &fakeCropper{}, &fakeEmbedder{angle: 0},
		ObserveOptions{SampleBudget: 3, MinDetectScore: 0.5})

	got, err := o.Run(context.Background(), []Target{tgt})
	if err != nil {
		t.Fatalf("one bad sample failed the whole target: %v", err)
	}
	if got.FramesFailed != 1 {
		t.Errorf("FramesFailed = %d, want 1", got.FramesFailed)
	}
	if got.FramesRead != 2 {
		t.Errorf("FramesRead = %d, want 2: the other two samples must still be "+
			"read, and a face in the closing credits is still a face", got.FramesRead)
	}
}

// TestObserverCountsEachSkipSeparately is the reporting rule.
//
// A skipped face and a failed frame are different problems in different files
// and need different responses, so lumping them together sends the operator to
// the wrong place. This asserts the distinction survives into the counts.
func TestObserverCountsEachSkipSeparately(t *testing.T) {
	det := &fakeDetector{faces: []Face{
		{Left: 0, Top: 0, Width: 32, Height: 32, Score: 0.9},  // kept
		{Left: 40, Top: 0, Width: 32, Height: 32, Score: 0.1}, // below score
	}}
	cropErr := &fakeCropper{}
	emb := &fakeEmbedder{angle: 0}

	// First run: the cropper fails, so both confident faces are lost to it.
	o := newObserver(t, det, &fakeFrames{}, cropErr, emb,
		ObserveOptions{SampleBudget: 1, MinDetectScore: 0.5})
	got, err := o.Run(context.Background(), []Target{target(1, "/a.mkv")})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got.BelowScore != 1 {
		t.Errorf("BelowScore = %d, want 1: the low-confidence face is dropped "+
			"for its own reason and must not be counted as a crop failure",
			got.BelowScore)
	}

	// Second run: the cropper works and the embedder fails instead, so the two
	// failure modes are separately observable.
	cropErr.err = nil
	emb.err = fmt.Errorf("model rejected the crop")
	o2 := newObserver(t, det, &fakeFrames{}, cropErr, emb,
		ObserveOptions{SampleBudget: 1, MinDetectScore: 0.5})
	got2, err := o2.Run(context.Background(), []Target{target(1, "/a.mkv")})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got2.EmbedFailed != 1 {
		t.Errorf("EmbedFailed = %d, want 1; an unembeddable face is a face "+
			"this pass did not look at, and it is counted as such rather "+
			"than as an error or as a detection", got2.EmbedFailed)
	}
	if len(got2.Faces) != 0 {
		t.Errorf("%d observations survived an embedder that fails every crop",
			len(got2.Faces))
	}
}

// TestObserverRejectsIncompleteParts is the nil-check, and the typed-nil case
// with it.
//
// A plain `x == nil` is wrong for an interface holding a typed nil pointer: the
// check passes, the stage is built, and the first real call panics a job
// halfway through a library. The reflection in isNil exists for that, and this
// test is what would notice its removal.
func TestObserverRejectsIncompleteParts(t *testing.T) {
	ok := []struct {
		name string
		det  FaceDetector
		src  FrameSource
		crp  Cropper
		emb  FaceEmbedder
	}{
		{"no detector", nil, &fakeFrames{}, &fakeCropper{}, &fakeEmbedder{}},
		{"no frame source", &fakeDetector{}, nil, &fakeCropper{}, &fakeEmbedder{}},
		{"no cropper", &fakeDetector{}, &fakeFrames{}, nil, &fakeEmbedder{}},
		{"no embedder", &fakeDetector{}, &fakeFrames{}, &fakeCropper{}, nil},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewObserver(tc.det, tc.src, tc.crp, tc.emb, ObserveOptions{}); err == nil {
				t.Fatal("NewObserver accepted a missing part; the first frame " +
					"would nil-dereference inside a running job")
			}
		})
	}

	// A typed nil pointer, which is the case a plain == nil misses.
	t.Run("typed nil detector", func(t *testing.T) {
		var det *fakeDetector
		if _, err := NewObserver(det, &fakeFrames{}, &fakeCropper{}, &fakeEmbedder{}, ObserveOptions{}); err == nil {
			t.Error("NewObserver accepted a TYPED nil detector; `det == nil` is " +
				"false for a typed nil in an interface, so the check has to use " +
				"reflection or the first Detect call panics")
		}
	})
}

// TestObserverRejectsANegativeBudget is validate, one line of it.
func TestObserverRejectsANegativeBudget(t *testing.T) {
	_, err := NewObserver(&fakeDetector{}, &fakeFrames{}, &fakeCropper{},
		&fakeEmbedder{}, ObserveOptions{SampleBudget: -1})
	if err == nil {
		t.Error("NewObserver accepted a negative sample budget; PlanSamples " +
			"returns nothing for one, so the pass would observe every target " +
			"and find no faces in any of them")
	}
}

// TestObserverIsCancelled checks cancellation, and that the result still
// describes what was done.
func TestObserverIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	o := newObserver(t, &fakeDetector{faces: []Face{{Score: 0.9}}},
		&fakeFrames{}, &fakeCropper{}, &fakeEmbedder{angle: 0},
		ObserveOptions{SampleBudget: 3})

	got, err := o.Run(ctx, []Target{target(1, "/a.mkv")})
	if err == nil {
		t.Fatal("a cancelled observe returned no error; the job would report " +
			"success having looked at nothing")
	}
	if len(got.Faces) != 0 {
		t.Errorf("%d observations were produced after cancellation", len(got.Faces))
	}
}
