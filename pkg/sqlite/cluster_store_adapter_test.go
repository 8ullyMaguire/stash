//go:build integration
// +build integration

// Step 2.4b.9: the seam between the clustering pass and the database.
//
// The pass declares a four-method `cluster.Store` and `ClusterStore` implements
// three of them. That mismatch survived every test in the tree, because no
// file imported both packages -- each half was tested alone, and a test that
// passes alone proves nothing about the join. This file is the join, and the
// compile-time assertion at the bottom of cluster_store_adapter.go is what
// makes the next mismatch a build error rather than a discovery.
//
// What is worth asserting here is the MERGE, because it is the one operation
// that is not a single statement. Moving members, carrying a handle and
// marking the loser are three writes, and a merge that did them in any other
// order, or outside a transaction, could be interrupted between them and leave
// a library where a face is in two clusters and nothing says it was merged --
// a state that looks valid to every later read.

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/cluster"
	"github.com/stashapp/stash/pkg/sqlite"
)

// newAdapter wraps the store the way the manager does.
func newAdapter(t *testing.T) *sqlite.ClusterStoreAdapter {
	t.Helper()
	return sqlite.NewClusterStoreAdapter(sqlite.NewClusterStore(), db)
}

// memberAt builds a member for a cluster.
func memberAt(clusterID, targetID int64, frame int) cluster.StoredMember {
	return cluster.StoredMember{
		ClusterID:   clusterID,
		TargetType:  "scene",
		TargetID:    targetID,
		FrameIndex:  frame,
		FaceLeft:    10,
		FaceTop:     20,
		FaceWidth:   64,
		FaceHeight:  64,
		DetectScore: 0.9,
		Distance:    0.2,
		Embedding:   []byte{1, 2, 3, 4},
	}
}

func TestClusterStoreAdapter_CreatesAClusterThePassCanUse(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		a := newAdapter(t)
		store := sqlite.NewClusterStore()

		// No confidence claim: the pointer nil has to survive to a NULL
		// column, which is the difference between "nobody said" and "0.0".
		id, err := a.CreateCluster(ctx, nil)
		require.NoError(t, err)
		require.NotZero(t, id)

		c, err := store.GetCluster(ctx, id)
		require.NoError(t, err)
		require.Equal(t, sqlite.StateSingleton, c.State)
		require.Nil(t, c.Confidence, "a nil confidence must stay NULL, not "+
			"become 0.0: the column is nullable to say nobody claimed one")
	})
}

func TestClusterStoreAdapter_StoresTheEmbeddingVerbatim(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		a := newAdapter(t)
		store := sqlite.NewClusterStore()
		id, err := a.CreateCluster(ctx, nil)
		require.NoError(t, err)

		// Bytes that are not valid JSON and not valid UTF-8, because that is
		// what a little-endian float32 vector actually is. A store that
		// round-tripped through a string column or a JSON encoder would
		// corrupt it here, and the corruption would only show up as a distance
		// that disagrees with the one the pass computed.
		emb := []byte{0x00, 0x00, 0x80, 0x3f, 0xff, 0xfe, 0x01, 0x02}
		m := memberAt(id, 7, 3)
		m.Embedding = emb

		require.NoError(t, a.AddMember(ctx, m))

		got, err := store.Members(ctx, id)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, emb, got[0].Embedding,
			"the stored embedding is not the bytes the pass handed over; a "+
				"later pass would measure a distance that is not the one "+
				"this pass decided on")
		require.EqualValues(t, 7, got[0].TargetID)
		require.EqualValues(t, 3, got[0].FrameIndex)
	})
}

func TestClusterStoreAdapter_MergeMovesEveryMemberToTheWinner(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		a := newAdapter(t)
		store := sqlite.NewClusterStore()

		winner, err := a.CreateCluster(ctx, nil)
		require.NoError(t, err)
		loser, err := a.CreateCluster(ctx, nil)
		require.NoError(t, err)

		// Two members on the loser, one on the winner, so the test can tell a
		// merge that moved the rows from one that only moved a count.
		require.NoError(t, a.AddMember(ctx, memberAt(winner, 1, 0)))
		require.NoError(t, a.AddMember(ctx, memberAt(loser, 2, 0)))
		require.NoError(t, a.AddMember(ctx, memberAt(loser, 3, 0)))

		survivor, err := a.MergeCluster(ctx, winner, loser)
		require.NoError(t, err)
		require.Equal(t, winner, survivor,
			"the survivor must be the winner; returning the loser would "+
				"delete a cluster that is about to have rows in it")

		// The winner has all three.
		ms, err := store.Members(ctx, winner)
		require.NoError(t, err)
		require.Len(t, ms, 3, "a merge that lost a member deletes a face from "+
			"the library with no record that it was ever there")

		// The loser has none. A face left behind on the loser is the failure
		// this whole transaction exists to prevent: it is in two clusters, and
		// every later pass reads that as two people.
		gone, err := store.Members(ctx, loser)
		require.NoError(t, err)
		require.Empty(t, gone, "a face is still on the merged-away cluster; "+
			"it is now in two clusters and nothing records that")

		// And the loser says it is gone.
		l, err := store.GetCluster(ctx, loser)
		require.NoError(t, err)
		require.Equal(t, sqlite.StateMerged, l.State)
	})
}

func TestClusterStoreAdapter_MergeCarriesTheHandle(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		a := newAdapter(t)
		store := sqlite.NewClusterStore()

		winner, err := a.CreateCluster(ctx, nil)
		require.NoError(t, err)
		loser, err := a.CreateCluster(ctx, nil)
		require.NoError(t, err)

		// A handle is a link the person shared with somebody. It lives on
		// exactly one of the two clusters, and losing it loses the link.
		require.NoError(t, store.SetHandle(ctx, loser, "https://example.invalid/p/1"))

		_, err = a.MergeCluster(ctx, winner, loser)
		require.NoError(t, err)

		w, err := store.GetCluster(ctx, winner)
		require.NoError(t, err)
		require.NotNil(t, w.Handle, "the merged-away cluster had a handle and "+
			"the survivor does not; the link now points at nothing")
		require.Equal(t, "https://example.invalid/p/1", *w.Handle)
	})
}

func TestClusterStoreAdapter_MergeKeepsTheWinnersOwnHandle(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		a := newAdapter(t)
		store := sqlite.NewClusterStore()

		winner, err := a.CreateCluster(ctx, nil)
		require.NoError(t, err)
		loser, err := a.CreateCluster(ctx, nil)
		require.NoError(t, err)

		require.NoError(t, store.SetHandle(ctx, winner, "https://example.invalid/p/w"))
		require.NoError(t, store.SetHandle(ctx, loser, "https://example.invalid/p/l"))

		_, err = a.MergeCluster(ctx, winner, loser)
		require.NoError(t, err)

		w, err := store.GetCluster(ctx, winner)
		require.NoError(t, err)
		require.Equal(t, "https://example.invalid/p/w", *w.Handle,
			"the winner's own handle was overwritten by the loser's; both "+
				"people are now linked as one and the second link is lost")
	})
}

func TestClusterStoreAdapter_RefusesASelfMerge(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		a := newAdapter(t)
		id, err := a.CreateCluster(ctx, nil)
		require.NoError(t, err)

		_, err = a.MergeCluster(ctx, id, id)
		require.Error(t, err, "a cluster merged into itself is a hole in the "+
			"pass's guard, and answering it with a silent success hides that "+
			"instead of reporting it")
	})
}

func TestClusterStoreAdapter_RefusesAMergedCluster(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		a := newAdapter(t)

		winner, err := a.CreateCluster(ctx, nil)
		require.NoError(t, err)
		loser, err := a.CreateCluster(ctx, nil)
		require.NoError(t, err)
		_, err = a.MergeCluster(ctx, winner, loser)
		require.NoError(t, err)

		// `loser` is now merged. Merging it again would move the members it
		// no longer has, or mark a state that is already final.
		other, err := a.CreateCluster(ctx, nil)
		require.NoError(t, err)
		_, err = a.MergeCluster(ctx, other, loser)
		require.Error(t, err, "a cluster that is already merged was merged "+
			"again; the pass is offering a cluster it has already retired")

		// And it cannot win one either.
		_, err = a.MergeCluster(ctx, loser, other)
		require.Error(t, err, "a merged cluster won another merge; it no "+
			"longer has members of its own to be the survivor")
	})
}

// The seam itself: this is the assertion that the pass can be given the
// database, and it is the one thing the two halves cannot check alone.
var _ cluster.Store = (*sqlite.ClusterStoreAdapter)(nil)
