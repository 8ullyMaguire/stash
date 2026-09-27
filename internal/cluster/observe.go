package cluster

import (
	"context"
	"fmt"
	"reflect"
)

// The detection and embedding half of a pass.
//
// # Why this is separate from Pass
//
// `Pass` takes faces that have already been detected and embedded. That split
// is not for test convenience -- it is because the two halves fail in opposite
// ways and an operator needs to be told which one happened:
//
//   - DETECTION needs a decoder, a video file and a model. It fails for reasons
//     that are about the library ("this file is corrupt", "no runtime
//     installed").
//   - CLUSTERING needs neither. It is pure arithmetic over vectors, and it fails
//     for reasons that are about the vectors.
//
// A pass that did both would report one error for two unrelated causes, and
// "the clustering pass failed" would be true of a missing ONNX runtime. Keeping
// them apart means the job can name the stage that failed, and a library that
// cannot be decoded produces a DETECTION error while every face it did manage
// to detect is still clusterable.

// Target is one video to look at.
type Target struct {
	// TargetType and TargetID locate it. The clustering does not interpret
	// them; they are carried through to the member row so a reviewer can find
	// the scene the face came from.
	TargetType string
	TargetID   int64

	// Path is the file frames are read from.
	Path string

	// DurationSeconds drives the sample budget. A frame the budget never
	// reaches is a frame nobody ever looked at, and a person who only appears
	// in the last act of a long video is a person this pass will not find.
	DurationSeconds int
}

// FrameSource yields the encoded frames for a target.
//
// An interface so the job is testable without ffmpeg and without a video file,
// which is the same reason the detector takes an Engine rather than importing
// ONNX. A build tag would mean the rule that matters most -- "no decoder must
// fail rather than return empty" -- is only exercised in builds nobody runs.
type FrameSource interface {
	// Frame returns the encoded image at the given sample ordinal, which is an
	// index into the target's own sample plan and not a timestamp. Ordering and
	// coverage are what the budget controls, and an index makes "did the last
	// sample get read" a direct comparison.
	Frame(ctx context.Context, t Target, sampleIndex int) ([]byte, error)
}

// FaceEmbedder turns a detected face's crop into a vector.
//
// Named for what it does rather than `Embedder`, which the concrete
// `*Embedder` in embedding.go already is. *Embedder satisfies this interface,
// and so does a one-line fake, which is the point: the observe stage must be
// testable without a model file.
//
// The crop is passed as encoded bytes, which is what the model wants and what
// the decoder produces; nothing here decodes an image, so nothing here has an
// opinion about image formats.
type FaceEmbedder interface {
	// Embed returns a vector, or an error saying why it could not.
	//
	// An error is expected and normal: a blurry face, a face at the edge of the
	// frame, a crop the model was not trained on. It is not a failure of the
	// pass and is counted, not returned.
	Embed(crop []byte) ([]float32, error)
}

// Cropper turns a frame and a detected face into the bytes the embedder wants.
//
// A separate step because the crop is where a detector's output meets a model's
// input, and that boundary is where the padding convention, the colour space
// and the resize policy live. A caller that supplied its own Cropper is
// asserting a convention, and the job takes the assertion rather than
// overriding it.
type Cropper interface {
	Crop(frame []byte, face Face) ([]byte, error)
}

// ObserveOptions configures the detection and embedding stage.
type ObserveOptions struct {
	// SampleBudget is how many frames to look at per target. Zero means the
	// stage picks from the duration using PlanSamples' own default, which is
	// the right answer for a caller that does not know.
	SampleBudget int

	// MinDetectScore drops a detection the model is unsure about. It duplicates
	// the pass's own MinDetectScore on purpose: this is the EARLY filter, before
	// a crop is encoded, and Pass applies its own again over what it is given.
	// Two filters would be redundant and one would be too late.
	MinDetectScore float64
}

// Observed is what one pass of the observe stage produced.
type Observed struct {
	// Faces are the observations, in target order then sample order.
	Faces []FaceObservation

	// Detected is how many faces the model found, before filtering.
	Detected int

	// BelowScore is how many were dropped for low confidence.
	BelowScore int

	// EmbedFailed is how many could not be embedded. NOT an error: a crop the
	// model rejects is a face this pass did not look at, and reporting it as
	// anything else makes the skip invisible.
	EmbedFailed int

	// FramesRead and FramesFailed count the decode step separately, because a
	// file that will not decode is a library problem and a model that will not
	// embed is a configuration one. They need different responses and lumping
	// them together sends the operator to the wrong place.
	FramesRead   int
	FramesFailed int

	// TargetErrors maps a target to why it could not be read. Per target rather
	// than a single error, because one unreadable file must not stop the other
	// 39,999 -- and because "the library has a problem" is useless when the
	// library has four hundred files and one of them is bad.
	TargetErrors map[string]error

	// Targets is how many targets were observed, successful or not.
	//
	// Counted rather than derived from len(TargetErrors), which holds only the
	// FAILURES. A Summary built from it would report "1 target(s)" for a
	// library of forty files where one was unreadable -- a number that is
	// correct and completely misleading.
	Targets int
}

// Summary is one line for a log.
func (o Observed) Summary() string {
	return fmt.Sprintf("%d target(s), %d unreadable: %d frame(s) read, "+
		"%d unreadable; %d face(s) detected, %d below score, "+
		"%d unembeddable; %d ready to cluster",
		o.Targets, len(o.TargetErrors), o.FramesRead, o.FramesFailed,
		o.Detected, o.BelowScore, o.EmbedFailed, len(o.Faces))
}

// Observer runs detection and embedding over a set of targets.
type Observer struct {
	detector FaceDetector
	src      FrameSource
	cropper  Cropper
	embedder FaceEmbedder
	opts     ObserveOptions
}

// FaceDetector finds faces in a frame.
//
// A one-method interface rather than the concrete *Detector, for the same
// reason: *Detector satisfies it, and so does a three-line fake, and the
// observe stage stays testable without a model file and a digest pin.
type FaceDetector interface {
	Detect(ctx context.Context, frame Frame) ([]Face, error)
}

// NewObserver builds the detection and embedding stage.
func NewObserver(det FaceDetector, src FrameSource, cropper Cropper, emb FaceEmbedder, opts ObserveOptions) (*Observer, error) {
	if isNil(det) || isNil(src) || isNil(cropper) || isNil(emb) {
		return nil, fmt.Errorf("the observe stage needs a detector, a frame " +
			"source, a cropper and an embedder; a nil one is a nil " +
			"dereference waiting for the first frame")
	}
	if opts.SampleBudget < 0 {
		return nil, fmt.Errorf("sample budget %d is negative", opts.SampleBudget)
	}
	return &Observer{detector: det, src: src, cropper: cropper, embedder: emb, opts: opts}, nil
}

// Run observes every target and returns the faces worth clustering.
//
// A target that cannot be read is recorded and skipped. The alternative --
// failing the whole pass -- turns one corrupt file into a library that cannot
// be clustered at all, and the user has no way to get their clusters back
// without finding and fixing that one file first.
func (o *Observer) Run(ctx context.Context, targets []Target) (Observed, error) {
	var out Observed
	out.TargetErrors = map[string]error{}

	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		out.Targets++
		if t.TargetType == "" {
			out.TargetErrors[t.Path] = fmt.Errorf("target type is required")
			continue
		}
		if t.Path == "" {
			out.TargetErrors[t.Path] = fmt.Errorf("target %d has no path", t.TargetID)
			continue
		}

		samples := o.samplesFor(t)
		for _, s := range samples {
			if err := ctx.Err(); err != nil {
				return out, err
			}

			frame, err := o.src.Frame(ctx, t, s)
			if err != nil {
				// A bad SAMPLE is skipped, not fatal to the target. The first
				// version broke out of the loop here, so one undecodable frame
				// abandoned the rest of the file -- and a face in the closing
				// credits, which is exactly what the sample budget exists to
				// reach, was never looked at. TestObserverKeepsGoingAfterOneBadSample
				// is the test that caught it.
				out.FramesFailed++
				out.TargetErrors[t.Path] = fmt.Errorf("reading frame %d: %w", s, err)
				continue
			}
			out.FramesRead++

			faces, err := o.detector.Detect(ctx, Frame{Index: s, Bytes: frame})
			if err != nil {
				// A detection failure is per-frame, not per-target: one
				// unreadable frame does not make the rest of the file
				// unclusterable, and a face in the next sample is a face.
				out.FramesFailed++
				out.TargetErrors[t.Path] = fmt.Errorf("detecting in frame %d: %w", s, err)
				continue
			}

			for _, f := range faces {
				out.Detected++
				if f.Score < o.opts.MinDetectScore {
					out.BelowScore++
					continue
				}

				crop, err := o.cropper.Crop(frame, f)
				if err != nil {
					out.EmbedFailed++
					continue
				}
				vec, err := o.embedder.Embed(crop)
				if err != nil {
					out.EmbedFailed++
					continue
				}

				out.Faces = append(out.Faces, FaceObservation{
					// The key is the membership identity, so it names the box as
					// well as the location: two models in one frame are two faces,
					// and a key of "scene:1/0" would be the same face twice.
					Key: fmt.Sprintf("%s:%d/%d/%dx%d+%d+%d",
						t.TargetType, t.TargetID, s, f.Width, f.Height, f.Left, f.Top),
					TargetType: t.TargetType,
					TargetID:   t.TargetID,
					FrameIndex: s,
					Box:        f,
					Vector:     vec,
				})
			}
		}
	}

	return out, nil
}

// samplesFor is the sample plan for one target.
//
// PlanSamples already encodes the rules -- the first sample always, the last
// always, the budget spread across the middle -- and its tests are the ones
// that prove the last frame of a long video is looked at. Re-deriving a plan
// here would be a second implementation of those rules, and the second one
// would not be tested against the property that matters.
func (o *Observer) samplesFor(t Target) []int {
	if o.opts.SampleBudget > 0 {
		return PlanSamples(t.DurationSeconds, o.opts.SampleBudget)
	}
	return PlanSamples(t.DurationSeconds, DefaultSampleBudget)
}

// DefaultSampleBudget is how many frames a target is sampled at when the caller
// does not say.
//
// Twelve, and the number is a judgement rather than a measurement: enough that a
// person who appears in a handful of shots in a two-hour film is seen at least
// once, few enough that a library of ten thousand videos does not spend a day
// decoding. The plan always includes the first and last sample whatever the
// budget, so a person who appears only in the opening titles or only in the end
// credits is still found -- which is the property the budget exists to protect,
// and the one PlanSamples' tests pin.
const DefaultSampleBudget = 12

// isNil reports whether an interface holds nothing usable.
//
// A plain `x == nil` is wrong here and wrong in a way that matters: a typed nil
// pointer stored in an interface is NOT equal to nil, so the check passes, the
// stage is built, and the first call dereferences nil and panics a job halfway
// through a library. The reflection is the price of accepting interfaces as
// constructor arguments, and paying it at construction is much cheaper than
// paying it inside a running job.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Func,
		reflect.Interface, reflect.Chan:
		return rv.IsNil()
	}
	return false
}
