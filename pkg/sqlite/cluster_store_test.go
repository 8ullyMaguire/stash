//go:build integration
// +build integration

// Step 2.4b.7: the cluster store.
//
// The properties under test are not CRUD. They are the two decisions from §7.1
// that CRUD is in service of:
//
//  1. An unnamed cluster is the DEFAULT, not a fallback. A corpus with no
//     scraper has no names, so "unnamed" is the state every cluster starts in
//     and most clusters stay in.
//  2. Naming a cluster is a RECORDED ACT, not a field assignment. Six months
//     later the question is who decided and when.
//
// Each has a mutation that breaks it invisibly -- an empty string standing in
// for an absent name, an audit row written outside the caller's transaction --
// and each is asserted below at the level where it can actually be seen.

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/sqlite"
)

// Every test runs inside sfTxn: a rolled-back transaction that gives each test
// private rows and leaves nothing behind. The store goes through the
// package-global dbWrapper, which honours that transaction -- which is exactly
// why the store cannot hold its own connection. A store that did would write to
// the real database while the test believed it was writing to a transaction,
// and the suite would pass while destroying the shared fixture data.
func newClusterStore() *sqlite.ClusterStore { return sqlite.NewClusterStore() }

func TestClusterStore_ACreatedClusterHasNoName(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := newClusterStore()

		id, err := store.CreateCluster(ctx, nil)
		require.NoError(t, err)

		c, err := store.GetCluster(ctx, id)
		require.NoError(t, err)

		// nil, not a pointer to "". This is THE assertion of the step: an empty
		// string is a value every consumer downstream will display, filter on,
		// and sort by, and it would render as a blank name in a list of a
		// hundred clusters. A nil is what "this has no name" looks like in Go,
		// and it is what makes `name: String` genuinely nullable in GraphQL.
		require.Nil(t, c.Name,
			"a new cluster reported a name; the default state is UNNAMED and "+
				"an empty string is not the same thing")

		// And the other optionals, for the same reason.
		require.Nil(t, c.Handle)
		require.Nil(t, c.Avatar)
		require.Nil(t, c.Confidence,
			"a new cluster reported a confidence; NULL means no claim has "+
				"been made, which is different from a claim of 0.0")

		// Default state is 'singleton': the state every new face starts in, and
		// what makes the "three or more appearances" threshold computable.
		require.Equal(t, sqlite.StateSingleton, c.State)
		require.Equal(t, "face", c.Engine)
	})
}

func TestClusterStore_NamingIsRecordedWithAnActor(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := newClusterStore()

		id, err := store.CreateCluster(ctx, nil)
		require.NoError(t, err)

		require.NoError(t, store.NameCluster(ctx, id, "Alice Example", "alice"))

		c, err := store.GetCluster(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, c.Name)
		require.Equal(t, "Alice Example", *c.Name)

		hist, err := store.NameHistory(ctx, id)
		require.NoError(t, err)
		require.Len(t, hist, 1)
		require.Equal(t, "Alice Example", hist[0].Name)
		require.Equal(t, "alice", hist[0].Actor)
		require.Equal(t, id, hist[0].ClusterID)
	})
}

func TestClusterStore_ARenameAppendsRatherThanOverwrites(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := newClusterStore()

		id, err := store.CreateCluster(ctx, nil)
		require.NoError(t, err)

		require.NoError(t, store.NameCluster(ctx, id, "Alice", "alice"))
		require.NoError(t, store.NameCluster(ctx, id, "Alice Example", "bob"))

		// The current name is the latest one.
		c, err := store.GetCluster(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "Alice Example", *c.Name)

		// And the previous one is still on file, which is the entire reason the
		// audit table is a separate append-only table rather than a history
		// column. With a single mutable column, "was this always this person?"
		// is unanswerable six months later -- which is the only question that
		// matters when a merge turns out to have been wrong.
		hist, err := store.NameHistory(ctx, id)
		require.NoError(t, err)
		require.Len(t, hist, 2)
		require.Equal(t, "Alice", hist[0].Name, "the FIRST name was lost")
		require.Equal(t, "Alice Example", hist[1].Name)
		require.Equal(t, "alice", hist[0].Actor)
		require.Equal(t, "bob", hist[1].Actor)
	})
}

func TestClusterStore_ClearingANameKeepsTheHistory(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := newClusterStore()

		id, err := store.CreateCluster(ctx, nil)
		require.NoError(t, err)
		require.NoError(t, store.NameCluster(ctx, id, "Alice", "alice"))

		require.NoError(t, store.ClearName(ctx, id))

		c, err := store.GetCluster(ctx, id)
		require.NoError(t, err)
		require.Nil(t, c.Name, "ClearName left a name behind")

		// Unnaming is not un-deciding. The record survives.
		hist, err := store.NameHistory(ctx, id)
		require.NoError(t, err)
		require.Len(t, hist, 1, "ClearName deleted the audit row; the store has "+
			"no DELETE path against person_cluster_names and neither should it")
	})
}

func TestClusterStore_NamingRefusesTheCasesThatWouldCorruptTheRecord(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := newClusterStore()

		// A FRESH cluster per subtest, not one shared.
		//
		// The first version created one cluster and ran four subtests against
		// it. A refused write leaves the cluster unnamed, so the "empty name"
		// subtest could not distinguish "my write was refused" from "a previous
		// subtest's write was refused" -- and "empty actor" failed outright,
		// because the CHECK on the audit table had already rejected a name the
		// store never saw, leaving state the subtest did not create.
		//
		// This is the fixture rule from memory: create a dedicated instance per
		// fixture, never a shared or fixed one. A subtest that shares its
		// subject with a sibling is asserting about the sibling.
		newCluster := func(t *testing.T) int64 {
			t.Helper()
			id, err := store.CreateCluster(ctx, nil)
			require.NoError(t, err)
			return id
		}

		t.Run("an empty name", func(t *testing.T) {
			id := newCluster(t)
			// An empty name is not an unnamed cluster, it is a name that was set
			// to nothing -- and the schema's CHECK refuses it a layer earlier
			// than the store does. Both matter; the store's check produces an
			// error that says which argument was wrong.
			require.Error(t, store.NameCluster(ctx, id, "", "alice"))

			// Nothing was written, not even the cache.
			c, err := store.GetCluster(ctx, id)
			require.NoError(t, err)
			require.Nil(t, c.Name)
			hist, err := store.NameHistory(ctx, id)
			require.NoError(t, err)
			require.Empty(t, hist, "a refused name still wrote an audit row")
		})

		t.Run("a whitespace-only name", func(t *testing.T) {
			id := newCluster(t)
			// Caught by the schema's length(trim(...)) CHECK, not by the store's
			// emptiness check. That is the point of the CHECK: it holds even for
			// a writer that is not this store.
			require.Error(t, store.NameCluster(ctx, id, "   ", "alice"))
		})

		t.Run("an empty actor", func(t *testing.T) {
			id := newCluster(t)
			// Not defaulted to "system". A row that says "system" for a name a
			// human typed is worse than no row: it looks like an attribution and
			// is not one. So the caller must pass the value, even when it is
			// "system".
			require.Error(t, store.NameCluster(ctx, id, "Alice", ""))

			c, err := store.GetCluster(ctx, id)
			require.NoError(t, err)
			require.Nil(t, c.Name)
		})

		t.Run("naming a cluster that does not exist", func(t *testing.T) {
			// A silent success here is the failure the expectOneRow helper
			// exists to prevent: a name written to nothing, with an audit row
			// for a cluster nobody can see.
			require.Error(t, store.NameCluster(ctx, 99999, "Ghost", "alice"))

			hist, err := store.NameHistory(ctx, 99999)
			require.NoError(t, err)
			require.Empty(t, hist, "naming a nonexistent cluster wrote an audit row")
		})
	})
}

func TestClusterStore_ConfidenceNullIsNotZero(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := newClusterStore()

		// NULL: no merge decision has been made, so there is no claim.
		id, err := store.CreateCluster(ctx, nil)
		require.NoError(t, err)
		c, err := store.GetCluster(ctx, id)
		require.NoError(t, err)
		require.Nil(t, c.Confidence)

		// 0.0: a claim, and the claim is that there is none. A different
		// statement, and the UI has to be able to tell them apart -- one means
		// "nothing to show", the other means "shown and it is zero".
		zero := 0.0
		id2, err := store.CreateCluster(ctx, &zero)
		require.NoError(t, err)
		c2, err := store.GetCluster(ctx, id2)
		require.NoError(t, err)
		require.NotNil(t, c2.Confidence)
		require.Equal(t, 0.0, *c2.Confidence)

		// Out of range is refused by the store with a message naming the
		// value, where the CHECK says only "CHECK constraint failed".
		tooHigh := 1.5
		_, err = store.CreateCluster(ctx, &tooHigh)
		require.Error(t, err)
		require.Contains(t, err.Error(), "1.5")
	})
}

func TestClusterStore_MembersRoundTripAndRequireAnEmbedding(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := newClusterStore()

		id, err := store.CreateCluster(ctx, nil)
		require.NoError(t, err)

		// Two members at different distances. The embedding is required, not
		// optional: a member whose embedding was not stored cannot be compared,
		// and the over-merge guard would have to re-run detection on the whole
		// library to check a link the user is being asked to trust.
		require.NoError(t, store.AddMember(ctx, sqlite.Member{
			ClusterID: id, TargetType: "scene", TargetID: 7, FrameIndex: 0,
			Distance: 0.31, DetectScore: 0.92, Embedding: []byte{1, 2, 3, 4},
		}))
		require.NoError(t, store.AddMember(ctx, sqlite.Member{
			ClusterID: id, TargetType: "gallery", TargetID: 9, FrameIndex: 120,
			Distance: 0.12, DetectScore: 0.88, Embedding: []byte{5, 6, 7, 8},
		}))

		// Nearest first, because this is the list a reviewer reads when asking
		// why two appearances are linked, and the nearest face is the strongest
		// evidence.
		members, err := store.Members(ctx, id)
		require.NoError(t, err)
		require.Len(t, members, 2)
		require.Equal(t, "gallery", members[0].TargetType)
		require.InDelta(t, 0.12, members[0].Distance, 1e-9)
		require.Equal(t, []byte{5, 6, 7, 8}, members[0].Embedding,
			"the embedding did not round-trip")
		require.Equal(t, "scene", members[1].TargetType)

		n, err := store.MemberCount(ctx, id)
		require.NoError(t, err)
		require.Equal(t, 2, n)

		t.Run("a member with no target is refused", func(t *testing.T) {
			// A face belongs to a target; a member with no target cannot be
			// shown to anyone, so it is a member row that exists and does
			// nothing.
			require.Error(t, store.AddMember(ctx, sqlite.Member{
				ClusterID: id, TargetType: "", TargetID: 7,
				Distance: 0.1, Embedding: []byte{1},
			}))
		})

		t.Run("a member with no embedding is refused", func(t *testing.T) {
			require.Error(t, store.AddMember(ctx, sqlite.Member{
				ClusterID: id, TargetType: "scene", TargetID: 8,
				Distance: 0.1, Embedding: nil,
			}))
		})

		t.Run("removing a member that is not there is an error", func(t *testing.T) {
			// A silent no-op means a user clicks "remove" on a face already
			// gone and is told nothing, which reads as a broken interface.
			require.Error(t, store.RemoveMember(ctx, id, "scene", 999, 0))
		})
	})
}

func TestClusterStore_AnAppearanceClaimedTwiceIsReportedNotResolved(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := newClusterStore()

		a, err := store.CreateCluster(ctx, nil)
		require.NoError(t, err)
		b, err := store.CreateCluster(ctx, nil)
		require.NoError(t, err)

		// The same face assigned to two clusters: one embedding matched two
		// distinct candidates. §7.1 calls this the `ambiguous` state, and the
		// point is that it is REPORTED rather than silently resolved -- a link
		// the user cannot interrogate is a link they will not trust (§7.4).
		require.NoError(t, store.AddMember(ctx, sqlite.Member{
			ClusterID: a, TargetType: "scene", TargetID: 42, FrameIndex: 3,
			Distance: 0.2, Embedding: []byte{1, 2},
		}))
		require.NoError(t, store.AddMember(ctx, sqlite.Member{
			ClusterID: b, TargetType: "scene", TargetID: 42, FrameIndex: 3,
			Distance: 0.25, Embedding: []byte{3, 4},
		}))

		claimants, err := store.ClustersForTarget(ctx, "scene", 42, 3)
		require.NoError(t, err)
		require.Len(t, claimants, 2, "the conflict is invisible; the assign step "+
			"will assign this face a third time")
		require.Equal(t, []int64{a, b}, claimants)

		// A face claimed once is unambiguous, and one claimed zero times is
		// unassigned -- three distinct states that a boolean would flatten into
		// two.
		//
		// The single-claim face is at a frame NOBODY else has used. The
		// fixed `frame 99` the first version used was already claimed by a row
		// the previous run's rolled-back transaction had inserted: the rollback
		// undoes the row, but the shared fixture's AUTOINCREMENT sequence keeps
		// moving and an earlier test in THIS run had claimed scene 42 at frame 3
		// with two rows. A hardcoded identity in a store test is a collision
		// waiting for a reordering, and this one fired on the first run.
		require.NoError(t, store.AddMember(ctx, sqlite.Member{
			ClusterID: a, TargetType: "scene", TargetID: 42, FrameIndex: 7,
			Distance: 0.3, Embedding: []byte{9, 9},
		}))

		only, err := store.ClustersForTarget(ctx, "scene", 42, 7)
		require.NoError(t, err)
		require.Len(t, only, 1)
		require.Equal(t, a, only[0])

		// Unclaimed, on an identity this test has not touched at all.
		none, err := store.ClustersForTarget(ctx, "video_unclaimed_xyzzy", 1, 0)
		require.NoError(t, err)
		require.Empty(t, none)
	})
}

func TestClusterStore_ListUnnamedIsTheReviewQueue(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := newClusterStore()

		baseline, err := store.UnnamedCount(ctx)
		require.NoError(t, err)

		var ids []int64
		for i := 0; i < 5; i++ {
			id, err := store.CreateCluster(ctx, nil)
			require.NoError(t, err)
			ids = append(ids, id)
		}
		// Name the middle one. The other four are the queue.
		require.NoError(t, store.NameCluster(ctx, ids[2], "Alice", "alice"))

		// The counts are DELTAS, not absolutes.
		//
		// sfTxn rolls back, so this test's own rows leave nothing behind -- but
		// the shared integration fixture is not empty and the rest of the
		// package builds rows in it, so an absolute "4" measures the fixture as
		// much as the code. The first version of this test asserted 4 and
		// failed with 7. A value another test can set is not a test; measuring
		// the baseline and asserting the change is what makes the assertion
		// about the store.
		clusters, reviewable, err := store.ListUnnamed(ctx, 100)
		require.NoError(t, err)
		require.Equal(t, baseline+4, reviewable,
			"reviewable is how many clusters could be reviewed; a UI needs it "+
				"to say 'showing N of M' without a second query")
		require.Equal(t, baseline+4, int64(len(clusters)),
			"the page and the count disagree")

		// All four of THIS test's clusters are in the page, and the named one is
		// not. Asserting by ID rather than by count, so the check is about which
		// rows came back and not about how many.
		seen := map[int64]bool{}
		for _, c := range clusters {
			require.Nil(t, c.Name)
			seen[c.ID] = true
		}
		for _, id := range ids {
			if id == ids[2] {
				require.False(t, seen[id], "a named cluster appeared in the "+
					"unnamed queue")
				continue
			}
			require.True(t, seen[id], "cluster %d is missing from the unnamed "+
				"queue", id)
		}

		// Oldest first: the cluster that has been unnamed longest is the one a
		// user is most likely to have an opinion about. Newest-first would put
		// the clusters the pipeline just made at the top, which are the ones
		// nobody has had time to look at.
		require.Equal(t, ids[0], clusters[0].ID)

		// The cap is real, and the count is the uncapped one -- that is the
		// whole point of returning both.
		limited, stillReviewable, err := store.ListUnnamed(ctx, 2)
		require.NoError(t, err)
		require.Len(t, limited, 2, "the limit was not applied")
		require.Equal(t, baseline+4, stillReviewable,
			"the count is UNCAPPED even when the page is; that is the whole "+
				"point of returning both, so a UI can say 'showing 2 of M'")

		// An unlimited read has to be asked for out loud.
		_, _, err = store.ListUnnamed(ctx, 0)
		require.Error(t, err)
	})
}

func TestClusterStore_ListClustersRequiresALimit(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := newClusterStore()

		// "Give me everything" has to be said out loud and paid for: a library
		// with no scraper can hold hundreds of thousands of unnamed clusters.
		_, err := store.ListClusters(ctx, 0, 0)
		require.Error(t, err)

		before, err := store.ListClusters(ctx, 1000, 0)
		require.NoError(t, err)

		newID, err := store.CreateCluster(ctx, nil)
		require.NoError(t, err)

		page, err := store.ListClusters(ctx, 1000, 0)
		require.NoError(t, err)
		require.Len(t, page, len(before)+1,
			"the new cluster is not visible in the listing")

		// Present, and identifiable -- not merely counted. A count is satisfied
		// by any row, so the new cluster's absence could be masked by an
		// unrelated one appearing.
		var found bool
		for _, c := range page {
			if c.ID == newID {
				found = true
			}
		}
		require.True(t, found, "the new cluster %d is not in the listing", newID)

		// Paging past the end is empty, not an error.
		page, err = store.ListClusters(ctx, 10, 100)
		require.NoError(t, err)
		require.Empty(t, page)
	})
}

func TestClusterStore_SetStateRefusesAnUnknownState(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := newClusterStore()

		id, err := store.CreateCluster(ctx, nil)
		require.NoError(t, err)

		// A state outside the closed set is a bug that must not become a row,
		// so both the store and the CHECK refuse it. The store's message names
		// the value, which the constraint's cannot.
		err = store.SetState(ctx, id, "confused")
		require.Error(t, err)
		require.Contains(t, err.Error(), "confused")

		c, err := store.GetCluster(ctx, id)
		require.NoError(t, err)
		require.Equal(t, sqlite.StateSingleton, c.State,
			"a refused transition changed the state")

		require.NoError(t, store.SetState(ctx, id, sqlite.StateAmbiguous))
		c, err = store.GetCluster(ctx, id)
		require.NoError(t, err)
		require.Equal(t, sqlite.StateAmbiguous, c.State)

		// The CHECK holds even for a writer that is not this store.
		require.Error(t, exec(t, ctx,
			`UPDATE person_clusters SET state = 'confused' WHERE id = ?`, id))
	})
}

func TestClusterStore_HandleIsOptionalAndValidated(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		store := newClusterStore()

		id, err := store.CreateCluster(ctx, nil)
		require.NoError(t, err)

		c, err := store.GetCluster(ctx, id)
		require.NoError(t, err)
		require.Nil(t, c.Handle, "a handle is not a requirement; the handle is "+
			"a user-supplied label and is deliberately NOT a performer "+
			"foreign key")

		require.NoError(t, store.SetHandle(ctx, id, "alice-2019"))
		c, err = store.GetCluster(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, c.Handle)
		require.Equal(t, "alice-2019", *c.Handle)

		// An empty handle is a removal, not a set -- the two acts are named
		// separately so a caller cannot blank a handle by accident.
		require.Error(t, store.SetHandle(ctx, id, ""))

		// And a handle on a cluster that does not exist is an error rather than
		// a silent success.
		require.Error(t, store.SetHandle(ctx, 99999, "ghost"))
	})
}
