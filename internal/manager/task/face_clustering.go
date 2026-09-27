package task

import (
	"context"
	"fmt"
	"reflect"

	"github.com/stashapp/stash/internal/cluster"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
)

// The face-clustering job.
//
// # This file is deliberately thin
//
// Everything that decides anything lives in `internal/cluster`: the sample
// budget, the over-merge guard, the assign decision, the pass. This file wires
// the two stages together and reports the result. Every rule that could be
// wrong about a person's identity is a rule with its own tests, and none of
// them are here.
//
// It is also the only file allowed to import both `internal/cluster` and a
// store, because the pass declares a narrow `cluster.Store` interface precisely
// so the domain does not have to know about the database. A job that did the
// clustering itself would either invert that layering or duplicate the
// interface here, and both cost more than the indirection saves.
//
// # Two stages, two failure reasons
//
// Detection and embedding need a decoder, a model and a pin; clustering needs
// none of those and is pure arithmetic over vectors. "The clustering pass
// failed" is true of a missing ONNX runtime and of a corrupt video file, and
// the two need opposite responses, so the stage that failed is always named.

// TargetLister supplies the videos to examine.
//
// An interface rather than a scene query, for two reasons. The first is
// testability: the job can be driven with three targets and no database. The
// second is that WHICH videos to examine is a policy question -- the whole
// library, or only what a user asked for -- and a policy hidden inside a task
// file is a policy nobody finds when they want to change it. The caller answers
// it by choosing an implementation.
type TargetLister interface {
	// Targets returns the videos to examine, in the order they should be.
	//
	// Order is load-bearing: the clustering pass builds its index as it runs,
	// so a face can only join a cluster formed from faces examined earlier.
	TargetList(ctx context.Context) ([]cluster.Target, error)
}

// faceObserver is the observe stage as the job needs it.
type faceObserver interface {
	Run(ctx context.Context, targets []cluster.Target) (cluster.Observed, error)
}

// facePass is the clustering stage as the job needs it.
type facePass interface {
	Run(ctx context.Context, faces []cluster.FaceObservation) (cluster.Result, error)
}

// FaceClusteringJob clusters the faces found in a library.
type FaceClusteringJob struct {
	Lister   TargetLister
	Observer faceObserver
	Pass     facePass

	// MinDetectScore is the early filter, applied before a crop is encoded.
	// It is the Observer's option, carried here because a caller configuring a
	// job sets its knobs in one place.
	MinDetectScore float64
}

// NewFaceClusteringJob builds the job from its parts.
//
// The nil checks use reflection because the parameters are interfaces, and a
// typed nil pointer inside an interface is not `== nil`. Without the check the
// job is built, queued, shown to the user as running, and then panics on the
// first target -- and a panic inside a job is a crash the user sees as a hung
// spinner, which is worse than a refusal at construction.
func NewFaceClusteringJob(lister TargetLister, obs faceObserver, p facePass, minDetectScore float64) (*FaceClusteringJob, error) {
	if isNilInterface(lister) {
		return nil, fmt.Errorf("the face clustering job needs a target lister; " +
			"without one it has nothing to examine and reports no faces found")
	}
	if isNilInterface(obs) {
		return nil, fmt.Errorf("the face clustering job needs an observer; " +
			"without one it never looks at a frame")
	}
	if isNilInterface(p) {
		return nil, fmt.Errorf("the face clustering job needs a clustering pass; " +
			"without one it computes clusters and discards them")
	}
	return &FaceClusteringJob{
		Lister:         lister,
		Observer:       obs,
		Pass:           p,
		MinDetectScore: minDetectScore,
	}, nil
}

// Execute runs both stages: observe every target, then cluster the lot.
//
// The pass runs ONCE, over every face, and not once per target. That is the
// point of the milestone: a person who appears in forty films is one person
// because the pass saw all forty, and a per-target pass would produce forty
// singletons and a "no faces found" library.
//
// The stages are sequential, and that is a decision rather than an omission.
// Detection parallelises trivially across targets; clustering does not, because
// the index is built as the pass runs. So the parallelism belongs in the
// Lister and the Observer, which may decode frames in a pool, and the pass
// receives the observations in target order either way.
func (j *FaceClusteringJob) Execute(ctx context.Context, progress *job.Progress) error {
	targets, err := j.Lister.TargetList(ctx)
	if err != nil {
		return fmt.Errorf("listing targets to examine: %w", err)
	}
	if len(targets) == 0 {
		logger.Info("Face clustering: no targets to examine")
		return nil
	}

	// Definite, because the total IS known: it is the number of targets. The
	// observe stage's per-target loop is the natural unit, so the bar advances
	// once per video and a user can see it moving. A bar that sits at 0% for
	// an hour and then jumps to 100% tells them nothing about whether it is
	// working, and an indefinite bar would be honest but useless.
	progress.Definite()
	progress.SetTotal(len(targets))

	observed, err := j.Observer.Run(ctx, targets)
	if err != nil {
		if job.IsCancelled(ctx) {
			logger.Info("Face clustering: cancelled while observing")
			return nil
		}
		return fmt.Errorf("observing faces: %w", err)
	}

	// Every unreadable target is logged by name. "The library has a problem" is
	// useless when the library has four hundred files and one of them is bad,
	// and the operator needs to know which one to go and look at.
	for path, e := range observed.TargetErrors {
		logger.Warnf("Face clustering: %s: %v", path, e)
	}
	logger.Infof("Face clustering: %s", observed.Summary())

	if len(observed.Faces) == 0 {
		// Not an error. A library with no detectable faces is a real and
		// common state -- a shelf of stills, a collection of landscapes -- and
		// reporting it as a failure trains the operator to ignore the job.
		logger.Info("Face clustering: no faces to cluster")
		progress.SetPercent(100)
		return nil
	}

	result, err := j.Pass.Run(ctx, observed.Faces)
	if err != nil {
		if job.IsCancelled(ctx) {
			logger.Info("Face clustering: cancelled while clustering")
			return nil
		}
		return fmt.Errorf("clustering %d face(s): %w", len(observed.Faces), err)
	}

	logger.Infof("Face clustering: %s", result.Summary())
	progress.SetPercent(100)
	return nil
}

// isNilInterface reports whether an interface holds nothing usable.
//
// The same reason as cluster.isNil, written out again rather than imported: the
// domain must not know about jobs, and the job layer must not reach into the
// domain's internals. Four lines duplicated beats an import cycle, and the
// alternative -- trusting callers to pass non-nil interfaces -- is the mistake
// that produces a panic in a running job rather than an error at the call site.
func isNilInterface(v any) bool {
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
