package manager

import (
	"context"
	"testing"

	"github.com/stashapp/stash/pkg/models"
)

// stash#7152: "Studio Tagger -> Batch Update Studios can panic with a nil
// pointer dereference when updating an existing local studio from a Stash-Box.
// The failure occurs when the Stash-Box returns a studio, but the scraped
// studio's StoredID is still nil when processMatchedStudio() is called. The
// code dereferences s.StoredID unconditionally, which aborts the entire batch
// job."
//
// The helper is the fix; this pins the three input shapes that used to panic.
func TestScrapedStoredIDRejectsUnusableValues(t *testing.T) {
	empty := ""
	nonNumeric := "not-a-number"
	zero := "0"
	good := "42"

	cases := []struct {
		name  string
		in    *string
		want  int
		wantK bool
	}{
		{"nil pointer -- the reported panic", nil, 0, false},
		{"empty string", &empty, 0, false},
		{"non-numeric", &nonNumeric, 0, false},
		// "0" parses. A stash-box id of 0 is not a real entity, and letting it
		// through would make GetStashIDs(0) a query for a row that cannot
		// exist, so it is treated as unusable rather than as a valid id.
		{"zero is not a real stored id", &zero, 0, false},
		{"valid id", &good, 42, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := scrapedStoredID(tc.in)
			if ok != tc.wantK {
				t.Fatalf("scrapedStoredID(%v) ok = %v, want %v", tc.in, ok, tc.wantK)
			}
			if got != tc.want {
				t.Errorf("scrapedStoredID(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// The panic was a dereference, so the property that matters is that the helper
// never dereferences a nil pointer on ANY input. A table test of expected
// values does not prove that; this does, because it is the one call shape that
// used to crash.
func TestScrapedStoredIDNeverPanicsOnNil(t *testing.T) {
	// No recover(): a nil-pointer dereference in Go aborts the process before
	// a deferred recover can be installed here, and the test binary's
	// non-zero exit is the signal. This simply has to not crash.
	var nilPtr *string
	if _, ok := scrapedStoredID(nilPtr); ok {
		t.Error("nil stored id was accepted")
	}
}

// The helper's own tests prove the helper is correct and prove nothing about
// whether processMatchedStudio calls it. A test that only exercises the helper
// stays green when someone removes the call-site guard -- which is exactly
// what happened when this test was first written, and the mutation check is
// what caught it.
//
// This calls the real function. The guard is the first statement, before any
// repository access, so no database is needed: a task with a nil box and a
// nil-StoreID scraped studio either returns at the guard or panics.
func TestProcessMatchedStudioSkipsNilStoredID(t *testing.T) {
	empty := ""
	zero := "0"
	name := "Example Studio"

	cases := []struct {
		name     string
		storedID *string
	}{
		{"nil stored id -- the reported panic", nil},
		{"empty stored id", &empty},
		{"zero stored id", &zero},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := &stashBoxBatchStudioTagTask{
				studio: &models.Studio{Name: name},
				// box is nil: any code path that got past the guard and
				// reached t.box.Endpoint would panic with a nil dereference
				// of its own, which would make this test fail loudly rather
				// than pass for the wrong reason.
			}
			scraped := &models.ScrapedStudio{Name: name, StoredID: tc.storedID}

			// Must return, not panic. Before the fix this is a
			// nil pointer dereference and the test binary dies.
			task.processMatchedStudio(context.Background(), scraped, map[string]bool{})
		})
	}
}

// The same shape for the performer path, which had the identical unguarded
// dereference.
func TestProcessMatchedPerformerSkipsNilStoredID(t *testing.T) {
	name := "Example Performer"
	task := &stashBoxBatchPerformerTagTask{performer: &models.Performer{Name: name}}
	scraped := &models.ScrapedPerformer{Name: &name, StoredID: nil}

	task.processMatchedPerformer(context.Background(), scraped, map[string]bool{}, map[string]bool{})
}
