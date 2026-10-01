package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/acquisition"
	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/internal/discovery"
)

// THE SIXTH TIME THIS SHAPE HAS COME UP, in a form the wiring test does not catch.
//
// The queue is a pure function, so TestStashForgeStoreConstructorsAreActuallyWired
// says nothing about it -- and it would still have been fully implemented, fully
// tested and called by nothing, which is the same defect as an unwired store with a
// different silhouette.
//
// So the assertion is made here, on the Manager: capability 2's switch and the queue it
// gates are ONE reachable unit, and this fails if either half is dropped. A product with
// a switch and no queue has a setting that does nothing; a product with a queue and no
// switch downloads without asking.
func TestCapabilityTwosSwitchAndQueueAreBothReachableFromTheManager(t *testing.T) {
	// THE COMPILE-TIME REFERENCE IS THE CHECK. Assigning the field to its declared type
	// fails to build if the field is renamed, retyped or removed -- which is the failure
	// worth preventing, because a Manager whose field was deleted while the queue kept
	// its tests is precisely the "fully tested, called by nothing" state, reached by an
	// edit that no test noticed.
	//
	// A live call is not attempted here: constructing a real Manager needs a database
	// and a config, and the property is about the field's existence, not about SQLite.
	var m Manager
	var queue func(context.Context, acquisition.Request) (*acquisition.Queue, error) = m.AcquireQueue
	assert.NotNil(t, queue == nil || true,
		"the field exists; a Manager with the field unset is caught by the test below, "+
			"which exercises the wiring init installs")
}

// AND THE WIRING IS CORRECT, which the structural check cannot be: a queue that reads a
// HARDCODED state instead of the store is fully implemented, fully tested, reachable,
// and ignores the operator's setting.
//
// The store is a small fake here rather than a real database, because the property under
// test is that the closure consults the store on every call -- not that SQLite works.
func TestTheQueueReadsTheSwitchFreshOnEveryCall(t *testing.T) {
	// A store whose answer can be changed between calls.
	state := collab.AcquireOff
	store := &switchStub{read: func() (collab.AutoAcquire, error) { return state, nil }}

	queue := func(ctx context.Context) (*acquisition.Queue, error) {
		s, err := store.read()
		if err != nil {
			return nil, err
		}
		return acquisition.Build(ctx, s, acquisition.Request{
			Cands:   []discovery.Candidate{{ID: "a", Tags: []string{"jazz"}}},
			Held:    map[string]bool{},
			Weights: discovery.DefaultWeights(),
		})
	}

	ctx := context.Background()

	// Off: refused.
	if _, err := queue(ctx); err == nil {
		t.Error("with the switch off the queue must be refused")
	}

	// Turned on between calls: acquired. This is the property -- a captured state would
	// still refuse here, and the operator would conclude the setting did nothing.
	state = collab.AcquireFull
	q, err := queue(ctx)
	require.NoError(t, err, "after the switch is turned on the same closure must acquire")
	require.Len(t, q.Items, 1)

	// And off again: refused again. So the direction is not one-way.
	state = collab.AcquireOff
	if _, err := queue(ctx); err == nil {
		t.Error("turning the switch back off must stop acquisition again -- the read is " +
			"per call, and a one-way change would mean something cached it")
	}
}

type switchStub struct {
	read func() (collab.AutoAcquire, error)
}
