package sqlite

import (
	"context"
	"fmt"

	"github.com/stashapp/stash/internal/cluster"
)

// Adapting the pass's Store to the database.
//
// # Why this file exists
//
// The pass declares a narrow `cluster.Store` with four methods, and
// `ClusterStore` implements three of them. That is not a near miss: it is the
// difference between a pass that persists clusters and a pass that computes
// them, and nothing in the build says so, because no file in the tree imports
// both packages. The two halves were written, each tested alone, and the seam
// between them was never closed -- so `NewPass` had no real caller and the
// whole of step 2.4b looked finished.
//
// This file closes it, and its build tag is nil: the assertion below does not
// compile unless every method matches exactly, so a divergence between the
// interface and the store is now a build error rather than a discovery.

// ClusterStoreAdapter presents the database as the pass's Store.
//
// An adapter rather than a re-implementation. Everything the four methods do --
// the SQL, the state validation, the confidence nullability, the transactional
// merge -- is already written and already tested in this package, and a second
// implementation would be a second set of bugs. The only thing done here is
// translation between two struct types that carry the same eleven fields and
// differ by their db tags.
type ClusterStoreAdapter struct {
	store *ClusterStore
	db    *Database
}

// NewClusterStoreAdapter wraps a store for use by the pass.
//
// The database handle is needed separately because `ClusterStore` is built by
// `NewClusterStore()` with nothing, and a merge is the one operation that has
// to be a transaction. Everything else here is a single statement and needs no
// handle.
func NewClusterStoreAdapter(s *ClusterStore, db *Database) *ClusterStoreAdapter {
	return &ClusterStoreAdapter{store: s, db: db}
}

// CreateCluster makes a new cluster.
func (a *ClusterStoreAdapter) CreateCluster(ctx context.Context, confidence *float64) (int64, error) {
	return a.store.CreateCluster(ctx, confidence)
}

// AddMember attaches a face to a cluster, carrying the embedding.
func (a *ClusterStoreAdapter) AddMember(ctx context.Context, m cluster.StoredMember) error {
	return a.store.AddMember(ctx, Member{
		ClusterID:   m.ClusterID,
		TargetType:  m.TargetType,
		TargetID:    m.TargetID,
		FrameIndex:  m.FrameIndex,
		FaceLeft:    m.FaceLeft,
		FaceTop:     m.FaceTop,
		FaceWidth:   m.FaceWidth,
		FaceHeight:  m.FaceHeight,
		DetectScore: m.DetectScore,
		Distance:    m.Distance,
		Embedding:   m.Embedding,
	})
}

// SetState writes one of the four lifecycle values.
func (a *ClusterStoreAdapter) SetState(ctx context.Context, clusterID int64, state string) error {
	return a.store.SetState(ctx, clusterID, state)
}

// MergeCluster folds loser into winner and returns the survivor.
//
// This is the method the store never had, and writing it is the real work of
// this file. It is one operation rather than four on purpose: a merge that
// moved the members, rewrote the handles and marked the loser as four separate
// statements could be interrupted between any two of them, leaving a library
// where a face is in two clusters and no cluster is marked merged. That is
// worse than a merge that did not happen, because the state looks valid and
// every later pass reads it as though it were.
//
// So it is a transaction, and the survivor is returned only after it commits.
func (a *ClusterStoreAdapter) MergeCluster(ctx context.Context, winner, loser int64) (int64, error) {
	if winner == loser {
		// A self-merge is a bug in the caller, and answering it with a
		// successful no-op would hide that. The pass is supposed to have
		// already dropped self-pairs; if one reaches here, the guard has a
		// hole and the only useful thing to do is say so.
		return 0, fmt.Errorf("merging cluster %d into itself; the pass should "+
			"never offer a cluster as its own loser", loser)
	}

	// The winner has to exist and the loser has to exist, and both states have
	// to be ones that can still merge. Checked up front so the whole thing
	// fails before any write rather than half way through.
	w, err := a.store.GetCluster(ctx, winner)
	if err != nil {
		return 0, fmt.Errorf("reading the winning cluster %d: %w", winner, err)
	}
	l, err := a.store.GetCluster(ctx, loser)
	if err != nil {
		return 0, fmt.Errorf("reading the losing cluster %d: %w", loser, err)
	}
	if w.State == StateMerged {
		return 0, fmt.Errorf("cluster %d has already been merged into "+
			"something; it cannot win another merge", winner)
	}
	if l.State == StateMerged {
		return 0, fmt.Errorf("cluster %d has already been merged into "+
			"something; it cannot lose another merge", loser)
	}

	// One transaction for all three writes, and only if there is not already
	// one.
	//
	// The join case is not a convenience. `Begin` refuses when a transaction is
	// already on the context, and every test in this package runs inside one --
	// so a merge that always opened its own would fail every test while
	// working in production, and a test-only branch for it would be a branch
	// nothing exercises. Joining is also the correct behaviour and not just the
	// testable one: a merge that committed on its own while its caller was mid
	// transaction would be durable before the work around it, which is the
	// opposite of what a caller wrapping several operations in one transaction
	// asked for.
	tx := ctx
	ownTransaction := false
	// getTx reports (nil, error) when there is NO transaction and (tx, nil) when
	// there is one -- the opposite of the usual shape, which is how this read
	// backwards the first time and every test failed with "already in
	// transaction" while production would have worked. Checking the error
	// rather than the tx is the only correct reading of it.
	if _, err := getTx(ctx); err != nil {
		var err error
		tx, err = a.db.Begin(ctx, true)
		if err != nil {
			return 0, fmt.Errorf("beginning the merge of %d into %d: %w",
				loser, winner, err)
		}
		ownTransaction = true
	}
	// Rollback on any error, and on a panic. Deferred rather than written on
	// each branch: a merge that wrote the members, then panicked before
	// marking the loser, would leave a library where a face is in two clusters
	// and no cluster says it was merged -- worse than a merge that never
	// happened, because every later pass reads that state as valid.
	//
	// Only when this call opened the transaction. Rolling back a transaction the
	// caller owns would discard the caller's earlier work on the way past.
	if ownTransaction {
		defer func() {
			_ = a.db.Rollback(tx)
		}()
	}

	// 1. Move the members. The member rows are re-pointed at the winner, so
	//    nothing that referenced a face through the loser has to be rewritten
	//    and no face ends up in two clusters.
	if _, err := dbWrapper.Exec(tx,
		`UPDATE person_cluster_members SET cluster_id = ? WHERE cluster_id = ?`,
		winner, loser); err != nil {
		return 0, fmt.Errorf("moving members from %d to %d: %w", loser, winner, err)
	}

	// 2. Carry the loser's handle onto the winner, when it has one. A handle is
	//    a link a person shared with somebody; dropping it loses the link, and
	//    keeping it on the loser points at a cluster that no longer exists.
	if l.Handle != nil && *l.Handle != "" && (w.Handle == nil || *w.Handle == "") {
		if err := a.store.SetHandle(tx, winner, *l.Handle); err != nil {
			return 0, fmt.Errorf("carrying the handle from %d to %d: %w",
				loser, winner, err)
		}
	}

	// 3. Mark the loser merged. Last, and inside the same transaction, so the
	//    state that says "this is gone" can only be committed once the members
	//    are already safely in the winner.
	if err := a.store.SetState(tx, loser, StateMerged); err != nil {
		return 0, fmt.Errorf("marking cluster %d merged: %w", loser, err)
	}

	// Commit only what this call opened. The caller's transaction is the
	// caller's to commit, and committing it here would end a transaction the
	// caller still had work to do.
	if ownTransaction {
		if err := a.db.Commit(tx); err != nil {
			return 0, fmt.Errorf("committing the merge of %d into %d: %w",
				loser, winner, err)
		}
	}
	return winner, nil
}

// The compile-time proof that the adapter really is the pass's Store.
//
// Without this line the adapter would be a struct with some methods, and the
// next person to change an interface would find out from a runtime failure in
// a job twenty minutes into a library. This makes it a build error instead.
var _ cluster.Store = (*ClusterStoreAdapter)(nil)
