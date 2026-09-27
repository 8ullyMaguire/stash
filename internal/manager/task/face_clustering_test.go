package task

import (
	"context"
	"fmt"
	"testing"

	"github.com/stashapp/stash/internal/cluster"
	"github.com/stashapp/stash/pkg/job"
)

// Tests for the face-clustering job.
//
// # What these are for
//
// The job is glue, and glue fails in a specific way: the two halves are wired
// up, each half is tested, and the SEAM is untested. The failure this file
// exists to catch is a job that observes forty thousand faces, reports them
// beautifully, and never calls the clustering stage -- which produces exactly
// the green suite and empty database that step 2.4b's exit condition was
// written to rule out.
//
// So most of these assert on what the job DID, not on what it returned.

// fakeLister returns a fixed target list.
type fakeLister struct {
	targets []cluster.Target
	err     error
	calls   int
}

func (l *fakeLister) TargetList(ctx context.Context) ([]cluster.Target, error) {
	l.calls++
	return l.targets, l.err
}

// fakeObserver returns a fixed observation, and counts calls.
type fakeObserver struct {
	out   cluster.Observed
	err   error
	calls int
	// seen records the targets it was handed, so a test can assert the job
	// passed the lister's list through unchanged.
	seen []cluster.Target
}

func (o *fakeObserver) Run(ctx context.Context, targets []cluster.Target) (cluster.Observed, error) {
	o.calls++
	o.seen = targets
	return o.out, o.err
}

// fakePass records the faces it was handed.
type fakePass struct {
	out   cluster.Result
	err   error
	calls int
	faces []cluster.FaceObservation
}

func (p *fakePass) Run(ctx context.Context, faces []cluster.FaceObservation) (cluster.Result, error) {
	p.calls++
	p.faces = faces
	return p.out, p.err
}

// twoFaces is an observation with something in it.
func twoFaces() cluster.Observed {
	return cluster.Observed{
		Targets:    1,
		FramesRead: 3,
		Detected:   2,
		Faces: []cluster.FaceObservation{
			{Key: "scene:1/0/64x64+10+20", TargetType: "scene", TargetID: 1},
			{Key: "scene:2/0/64x64+10+20", TargetType: "scene", TargetID: 2},
		},
	}
}

// newJob wires three fakes.
func newJob(t *testing.T, l TargetLister, o faceObserver, p facePass) (*FaceClusteringJob, error) {
	t.Helper()
	return NewFaceClusteringJob(l, o, p, 0.5)
}

// --- the wiring, which is the whole point ---

// TestFaceClusteringJobRunsBothStages is the seam test.
//
// A job that observes and does not cluster reports a successful run and leaves
// the table empty, which is indistinguishable from a library with no faces in
// it. That is the exact failure step 2.4b's exit condition forbids, and it is
// invisible to tests of either stage alone.
func TestFaceClusteringJobRunsBothStages(t *testing.T) {
	l := &fakeLister{targets: []cluster.Target{
		{TargetType: "scene", TargetID: 1, Path: "/a.mkv"},
		{TargetType: "scene", TargetID: 2, Path: "/b.mkv"},
	}}
	o := &fakeObserver{out: twoFaces()}
	p := &fakePass{}

	j, err := newJob(t, l, o, p)
	if err != nil {
		t.Fatalf("building the job: %v", err)
	}
	if err := j.Execute(context.Background(), nil); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if o.calls != 1 {
		t.Errorf("the observer ran %d times, want 1", o.calls)
	}
	if p.calls != 1 {
		t.Fatalf("the clustering pass ran %d times, want 1; a job that "+
			"observes without clustering reports success and leaves every "+
			"cluster table empty", p.calls)
	}
	if len(p.faces) != 2 {
		t.Errorf("the pass received %d faces, want 2", len(p.faces))
	}

	// The lister's list must reach the observer unchanged, and in order: the
	// pass is order-dependent, so a reordering here changes which clusters
	// form.
	if len(o.seen) != 2 || o.seen[0].TargetID != 1 || o.seen[1].TargetID != 2 {
		t.Errorf("the observer saw %+v, want the lister's two targets in order", o.seen)
	}
}

// TestFaceClusteringJobClustersAllTargetsTogether is the milestone's exit.
//
// A person who appears in forty films is one person because the pass saw all
// forty. A job that ran the pass once per target would produce forty
// singletons, and the test that would catch it is this one: the pass is called
// ONCE, with every face.
func TestFaceClusteringJobClustersAllTargetsTogether(t *testing.T) {
	many := cluster.Observed{Targets: 5, Detected: 5, Faces: make([]cluster.FaceObservation, 5)}
	l := &fakeLister{targets: make([]cluster.Target, 5)}
	o := &fakeObserver{out: many}
	p := &fakePass{}

	j, err := newJob(t, l, o, p)
	if err != nil {
		t.Fatalf("building the job: %v", err)
	}
	if err := j.Execute(context.Background(), nil); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if p.calls != 1 {
		t.Errorf("the pass ran %d times for 5 targets; it must run once over "+
			"every face, or a person in five films becomes five singletons",
			p.calls)
	}
	if len(p.faces) != 5 {
		t.Errorf("the pass received %d faces, want 5", len(p.faces))
	}
}

// TestFaceClusteringJobDoesNotClusterAnEmptyObservation keeps the job from
// making a pass write empty clusters for a library with no faces.
func TestFaceClusteringJobDoesNotClusterAnEmptyObservation(t *testing.T) {
	l := &fakeLister{targets: []cluster.Target{{TargetType: "scene", TargetID: 1}}}
	o := &fakeObserver{out: cluster.Observed{Targets: 1, FramesRead: 12, Detected: 0}}
	p := &fakePass{}

	j, err := newJob(t, l, o, p)
	if err != nil {
		t.Fatalf("building the job: %v", err)
	}
	if err := j.Execute(context.Background(), nil); err != nil {
		t.Fatalf("a library with no faces is not an error: %v", err)
	}
	if p.calls != 0 {
		t.Errorf("the pass ran on zero faces; there is nothing to cluster and " +
			"a pass over an empty slice is a pass that could create a " +
			"cluster for nothing")
	}
}

// TestFaceClusteringJobDoesNothingWithNoTargets keeps a fresh install from
// reporting a failed run.
func TestFaceClusteringJobDoesNothingWithNoTargets(t *testing.T) {
	l := &fakeLister{targets: nil}
	o := &fakeObserver{out: cluster.Observed{}}
	p := &fakePass{}

	j, err := newJob(t, l, o, p)
	if err != nil {
		t.Fatalf("building the job: %v", err)
	}
	if err := j.Execute(context.Background(), nil); err != nil {
		t.Fatalf("an empty library is not an error: %v", err)
	}
	if o.calls != 0 || p.calls != 0 {
		t.Errorf("stages ran for an empty library: observer %d, pass %d",
			o.calls, p.calls)
	}
}

// --- the failures, and which stage they name ---

// TestFaceClusteringJobNamesTheStageThatFailed is the reporting rule.
//
// "The clustering pass failed" is true of a missing ONNX runtime and of a
// corrupt video file. The two need opposite responses, so the error says which
// stage produced it.
func TestFaceClusteringJobNamesTheStageThatFailed(t *testing.T) {
	cases := []struct {
		name     string
		obsErr   error
		passErr  error
		wantWord string
	}{
		{"observe", fmt.Errorf("no ONNX runtime"), nil, "observing"},
		{"cluster", nil, fmt.Errorf("disk full"), "clustering"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := &fakeLister{targets: []cluster.Target{{TargetType: "scene", TargetID: 1}}}
			o := &fakeObserver{out: twoFaces(), err: tc.obsErr}
			p := &fakePass{err: tc.passErr}

			j, err := newJob(t, l, o, p)
			if err != nil {
				t.Fatalf("building the job: %v", err)
			}
			err = j.Execute(context.Background(), nil)
			if err == nil {
				t.Fatalf("the %s stage failed and the job reported success", tc.name)
			}
			if !contains(err.Error(), tc.wantWord) {
				t.Errorf("error %q does not name the stage (%q); an operator "+
					"reading it cannot tell a missing model from a full disk",
					err, tc.wantWord)
			}
		})
	}
}

// TestFaceClusteringJobPropagatesAListerFailure keeps a database error from
// looking like an empty library.
func TestFaceClusteringJobPropagatesAListerFailure(t *testing.T) {
	l := &fakeLister{err: fmt.Errorf("database is locked")}
	o := &fakeObserver{}
	p := &fakePass{}

	j, err := newJob(t, l, o, p)
	if err != nil {
		t.Fatalf("building the job: %v", err)
	}
	if err := j.Execute(context.Background(), nil); err == nil {
		t.Fatal("a lister failure was swallowed; the job reported an empty " +
			"library, which is indistinguishable from a library with no faces")
	}
	if o.calls != 0 {
		t.Errorf("the observer ran %d times after the lister failed", o.calls)
	}
}

// TestFaceClusteringJobTreatsCancellationAsNotAnError keeps a cancelled job
// from showing the user a failure they caused.
func TestFaceClusteringJobTreatsCancellationAsNotAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	l := &fakeLister{targets: []cluster.Target{{TargetType: "scene", TargetID: 1}}}
	o := &fakeObserver{out: twoFaces(), err: context.Canceled}
	p := &fakePass{}

	j, err := newJob(t, l, o, p)
	if err != nil {
		t.Fatalf("building the job: %v", err)
	}
	if err := j.Execute(ctx, nil); err != nil {
		t.Errorf("a cancelled job returned %v; the user pressed stop, and a "+
			"job that reports its own cancellation as a failure trains them "+
			"to ignore the job's status", err)
	}
}

// TestFaceClusteringJobRefusesIncompleteParts is the nil check, and the typed
// nil with it.
//
// A typed nil pointer inside an interface is not `== nil`, so without the
// reflection the job is built, queued, shown as running, and panics on the
// first target. A panic in a job is a crash the user sees as a hung spinner.
func TestFaceClusteringJobRefusesIncompleteParts(t *testing.T) {
	ok := &fakeObserver{}
	pk := &fakePass{}
	l := &fakeLister{}

	if _, err := NewFaceClusteringJob(nil, ok, pk, 0.5); err == nil {
		t.Error("NewFaceClusteringJob accepted a nil lister")
	}
	if _, err := NewFaceClusteringJob(l, nil, pk, 0.5); err == nil {
		t.Error("NewFaceClusteringJob accepted a nil observer")
	}
	if _, err := NewFaceClusteringJob(l, ok, nil, 0.5); err == nil {
		t.Error("NewFaceClusteringJob accepted a nil pass")
	}

	// The typed nil, which a plain == nil misses.
	var typedNil *fakePass
	if _, err := NewFaceClusteringJob(l, ok, typedNil, 0.5); err == nil {
		t.Error("NewFaceClusteringJob accepted a TYPED nil pass; `p == nil` " +
			"is false for a typed nil in an interface, so the job would be " +
			"built and then panic inside a running job")
	}
}

// TestFaceClusteringJobSatisfiesJobExec is the compile-time proof it is a job.
//
// A method with the right name and the wrong signature is not a job, and the
// failure appears at the call site that tries to queue it -- possibly in a
// resolver, possibly in production.
func TestFaceClusteringJobSatisfiesJobExec(t *testing.T) {
	var _ job.JobExec = (*FaceClusteringJob)(nil)
}

// contains is a substring check, local so the test file does not drag in
// strings for two calls.
func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}
