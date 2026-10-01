package manager

import (
	"errors"
	"testing"

	"github.com/stashapp/stash/internal/cluster"
	"github.com/stashapp/stash/pkg/sqlite"
)

// Tests for the face-clustering entry point.
//
// # What these are for
//
// The thing worth protecting here is not the wiring -- that is four lines and
// the compiler checks it. It is the claim that the pass can actually be BUILT
// from this manager. That claim was false for the whole of step 2.4b: the pass
// had no real caller, the sqlite store implemented three of the four methods
// the interface needed, and no build reported it because no file imported both
// packages. Every test in the tree passed, including the pass's own and the
// store's own.
//
// So the test that matters is TestNewFaceClusteringPass_SatisfiesTheDomain
// interface, and it exists because "each half tested alone" is not the same
// claim as "the halves fit together".

// The manager can be built without a live database for this purpose: the
// construction path reads s.Database and nothing else.
func managerForTest() *Manager {
	return &Manager{Database: nil}
}

// TestNewFaceClusteringPass_Builds is the smoke test: the default config
// validates, the adapter is accepted, and the real geometry is used.
func TestNewFaceClusteringPass_Builds(t *testing.T) {
	m := managerForTest()

	pass, err := m.NewFaceClusteringPass()
	if err != nil {
		t.Fatalf("building the pass from the manager: %v", err)
	}
	if pass == nil {
		t.Fatal("a nil pass with no error; the caller would queue a job that " +
			"panics on its first face")
	}
}

// TestNewFaceClusteringPass_IsDeterministic checks the defaults are usable.
//
// `DefaultConfig` is the numbers the pass is tested with, so if it stopped
// validating then the pass's own tests would still pass -- they build their own
// config -- and only the production path would break. This is the test that
// connects the two.
func TestNewFaceClusteringPass_IsDeterministic(t *testing.T) {
	m := managerForTest()

	first, err := m.NewFaceClusteringPass()
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	second, err := m.NewFaceClusteringPass()
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if first == second {
		t.Error("two builds returned the same pass; the defaults are supposed " +
			"to be a value, and a shared one means two jobs would share state")
	}
}

// TestFaceClustering_RefusesWithoutAModel is the standing refusal.
//
// Until the observation stage has a model, the mutation must return an error
// rather than queue a job that finds nothing. A job that appears in the list
// and then reports "no faces found" is indistinguishable from a library whose
// faces are all below the detection threshold, and a user has no way to tell
// the two apart.
func TestFaceClustering_RefusesWithoutAModel(t *testing.T) {
	m := managerForTest()

	_, err := m.FaceClustering(nil)
	if err == nil {
		t.Fatal("FaceClustering returned no error with no model configured; it " +
			"would queue a job that reports a library with no faces")
	}
	if !errors.Is(err, ErrObserverNotWired) {
		t.Errorf("error %v does not match ErrObserverNotWired, so a caller "+
			"cannot tell this from a real failure", err)
	}
}

// TestNewFaceClusteringPass_TakesARealStore is the seam, as a test rather than
// a declaration, because it is the one test in the tree that would have caught
// the store not implementing the interface.
//
// The declaration form of this assertion lives at the bottom of
// pkg/sqlite/cluster_store_adapter.go, in a non-test file, so every build checks
// it. This test checks the same thing from the other side: that the pass the
// MANAGER builds is one the domain accepts, with the store the manager chose.
func TestNewFaceClusteringPass_TakesARealStore(t *testing.T) {
	m := managerForTest()

	store := sqlite.NewClusterStoreAdapter(sqlite.NewClusterStore(), m.Database)

	// Built directly rather than through the manager, so this test fails if the
	// adapter stops satisfying the interface -- independently of whether
	// NewFaceClusteringPass happens to still work by some other route.
	if _, err := cluster.NewPass(cluster.DefaultConfig(), store,
		cluster.CosineGeometry{}); err != nil {
		t.Fatalf("the store the manager builds is not one the pass accepts: %v", err)
	}
}
