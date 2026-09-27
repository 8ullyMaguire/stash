package manager

import (
	"context"
	"errors"

	"github.com/stashapp/stash/internal/cluster"
	"github.com/stashapp/stash/internal/manager/task"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/sqlite"
)

// The face-clustering job's entry point.
//
// # Why it is a job and not inline work
//
// A pass over a library of ten thousand videos decodes frames and runs two
// models on each. That is hours of CPU, so it has to be cancellable, visible
// in the job list, and reportable while it runs -- which is what the job
// framework is for. Doing it inline in a resolver would hold a GraphQL request
// open for the duration and produce a proxy timeout instead of a progress bar.
//
// # What the returned ID means
//
// The ID is the job, not the result. A caller wanting to know how many clusters
// were made has to wait for the job and read the log. The schema says so in its
// description, because a mutation that returns an ID is routinely mistaken for
// one that has already happened.
//
// # Where it stands
//
// The clustering half is finished and tested. The observation half -- the
// decoder, the detector, the embedder, and the lister that says which videos to
// examine -- is not, and this file says so rather than starting a pass that
// would find nothing. It is the one place in the tree allowed to import both
// `internal/cluster` and `pkg/sqlite`, which is the price of that boundary being
// enforced anywhere at all.

// ErrObserverNotWired is returned until the observation stage has a model.
//
// Exported so the GraphQL layer can match on it and say what is missing, rather
// than surfacing a generic failure to a user who pressed a button that looks
// like it should work. A standing refusal, not a bug: the clustering half is
// done, and the model half needs files that are not in this tree.
var ErrObserverNotWired = errors.New("face clustering needs a decoder, a " +
	"detector and an embedder, and none is configured; the clustering stage " +
	"they feed is implemented and tested, and this is the half that needs the " +
	"model files")

// NewFaceClusteringPass builds the pass the job will run.
//
// It is separate from FaceClustering so the wiring can be built and tested
// without a job, and so the pieces that DO exist can be exercised: the
// adapter, the geometry, and the defaults. A construction failure here is a
// configuration error, and it happens before any video is decoded -- which is
// the difference between a message and twenty minutes of decoding.
func (s *Manager) NewFaceClusteringPass() (*cluster.Pass, error) {
	// The adapter is the seam. Until it existed the pass had no real caller
	// anywhere in the tree: `NewPass` was tested against a fake store, the
	// sqlite store was tested against its own methods, and neither fact said
	// the two fitted together. They did not -- the store implemented three of
	// the four methods the interface needs, which no build reported, because
	// no file imported both packages. The compile-time assertion at the bottom
	// of cluster_store_adapter.go is what keeps that from recurring.
	store := sqlite.NewClusterStoreAdapter(sqlite.NewClusterStore(), s.Database)

	// DefaultConfig comes from the domain rather than from here. The
	// thresholds decide who a person is, so the values the pass is tested
	// with have to be the values it ships with; a manager that supplied its
	// own would leave every test asserting numbers production never uses.
	//
	// CosineGeometry{} rather than a scalar stand-in: this is the real runtime
	// path. The scalar geometry exists for tests that need a number they can
	// write down.
	return cluster.NewPass(cluster.DefaultConfig(), store, cluster.CosineGeometry{})
}

// newFaceObserver builds the detection and embedding stage.
//
// It is a method on Manager rather than a package function because the stage
// needs the manager's own config and store. The parts are resolved HERE and
// nowhere else, so every failure a user can plausibly hit -- no model file, a
// bad digest, a missing decoder -- surfaces as an error from this call before
// any work starts. Discovering it twenty minutes into a pass over a large
// library is the difference between a message and an afternoon.
func (s *Manager) newFaceObserver() (*cluster.Observer, error) {
	return nil, ErrObserverNotWired
}

// FaceClustering queues a face-clustering pass and returns the job ID.
//
// It refuses, for now, because the observation stage has no model. The refusal
// is explicit and comes from this function rather than from a job that fails
// twenty minutes in: a caller gets the error, the user gets the message, and
// neither has to watch a job appear in the list and then die.
func (s *Manager) FaceClustering(ctx context.Context) (int, error) {
	// The observer is resolved first, and it is what is missing. Built before
	// anything else so the error a caller gets is the real one, rather than a
	// later failure that hides it.
	obs, err := s.newFaceObserver()
	if err != nil {
		return 0, err
	}

	// The rest is written and compiles, rather than being deferred to the day
	// the model lands. A TODO comment is not a plan; this is the plan, in
	// code, checked by the compiler and by the tests on the job.
	pass, err := s.NewFaceClusteringPass()
	if err != nil {
		return 0, err
	}

	ex, err := task.NewFaceClusteringJob(sceneTargetLister{repo: s.Repository.Scene}, obs, pass,
		cluster.DefaultConfig().MinDetectScore)
	if err != nil {
		return 0, err
	}

	return s.JobManager.Add(ctx, "Clustering faces...", job.MakeJobExec(ex.Execute)), nil
}
