//go:build integration
// +build integration

// Migration tests for the M2c cluster tables.
//
// The question is not "does the version number match" — the version test
// already covers that, and a version can match perfectly while a migration
// silently does nothing. It is "does each table exist, and does it enforce the
// constraints the design depends on".

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/sqlite"
)

func TestPersonClusters_TablesExist(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		for _, table := range []string{"person_clusters", "person_cluster_members", "person_cluster_merges"} {
			assert.Equal(t, int64(1),
				count(t, ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table),
				"%s must exist; if this fails, its migration did not apply even "+
					"though the schema version agrees", table)
		}
	})
}

// TestPersonCluster_UnnamedIsTheDefaultAndNotAnError is the constraint the whole
// milestone rests on.
//
// The design decision: an unnamed cluster is the DEFAULT, not a fallback. A
// system that requires a name before it will link a person cannot serve a
// corpus where the names are the thing that is missing.
//
// So a cluster with NULL handle and NULL avatar must insert without complaint.
// If this ever starts failing, something has reintroduced the requirement, and
// the failure would be invisible everywhere else — every other test would still
// pass, with clusters it had bothered to name.
func TestPersonCluster_UnnamedIsTheDefaultAndNotAnError(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		require.NoError(t, exec(t, ctx,
			"INSERT INTO person_clusters (handle) VALUES (NULL)"),
			"an unnamed cluster is the DEFAULT state, not a failure; the "+
				"absence of a name is the normal case for an amateur corpus")

		// And it must be readable as a normal row with a NULL handle, rather
		// than the insert having quietly written an empty string.
		assert.Equal(t, nil, scalar(t, ctx, "SELECT handle FROM person_clusters LIMIT 1"),
			"handle must be NULL, not an empty string; ''unset'' and ''set to "+
				"nothing'' are different statements and conflating them is how a "+
				"cluster looks named when it is not")

		row := scalar(t, ctx, "SELECT state FROM person_clusters LIMIT 1")
		assert.Equal(t, "singleton", row,
			"a new cluster starts as a singleton; every new face begins here, "+
				"and it is what makes the three-appearances rule computable")

		assert.Equal(t, int64(1), count(t, ctx, "SELECT count(*) FROM person_clusters"),
			"exactly one row: the unnamed cluster is the subject")
	})
}

// TestPersonCluster_ConfidenceIsNullNotZero: a singleton has no merge decision
// behind it, so a confidence for it would be an invented number.
func TestPersonCluster_ConfidenceIsNullNotZero(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		require.NoError(t, exec(t, ctx,
			"INSERT INTO person_clusters (handle, confidence) VALUES ('withConfidence', 0.87)"))
		require.NoError(t, exec(t, ctx,
			"INSERT INTO person_clusters (handle) VALUES ('noConfidence')"))

		// A cluster with a confidence claims the ENGINE believes something about
		// identity. A cluster without one must be NULL, which means "no claim
		// has been made" -- a different statement from 0.0, which would read as
		// "the engine is certain these are different people".
		assert.Equal(t, nil, scalar(t, ctx,
			"SELECT confidence FROM person_clusters WHERE handle = 'noConfidence'"),
			"confidence must default to NULL; 0.0 is a claim of certainty in "+
				"the negative")
		assert.Equal(t, 0.87, scalar(t, ctx,
			"SELECT confidence FROM person_clusters WHERE handle = 'withConfidence'"))
	})
}

func TestPersonCluster_ConfidenceIsBounded(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		for _, bad := range []float64{-0.01, 1.01, 2.0, -1.0} {
			assert.Error(t, exec(t, ctx,
				"INSERT INTO person_clusters (handle, confidence) VALUES ('bad', ?)", bad),
				"confidence %v is outside 0..1 and must be refused", bad)
		}
		// The boundaries themselves are valid.
		for _, ok := range []float64{0.0, 1.0} {
			require.NoError(t, exec(t, ctx,
				"INSERT INTO person_clusters (handle, confidence) VALUES ('edge', ?)", ok),
				"confidence %v is on the boundary and must be accepted", ok)
		}
	})
}

// TestPersonCluster_AmbiguousIsAReachableState is the stash-box #299 / #846
// requirement: the system knowing it does not know is a state the UI renders.
//
// It is a CHECK value rather than a soft flag, so a test has to prove the state
// is actually WRITABLE. A state that appears in a comment and not in a
// constraint is documentation.
func TestPersonCluster_AmbiguousIsAReachableState(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		for _, state := range []string{"singleton", "settled", "ambiguous", "merged"} {
			require.NoError(t, exec(t, ctx,
				"INSERT INTO person_clusters (handle, state) VALUES (?, ?)", "s-"+state, state),
				"state %q must be writable; the ambiguous state is first-class "+
					"because silently guessing is what makes people distrust the "+
					"links", state)
		}
		assert.Error(t, exec(t, ctx,
			"INSERT INTO person_clusters (handle, state) VALUES ('bad', 'unsure')"),
			"a state outside the closed list must be refused; it is a bug that "+
				"must not become a row")
	})
}

// TestPersonCluster_AmbiguousIsIndexedForTheReviewQueue: a scan of every
// cluster to find the handful needing attention is a scan of everything.
func TestPersonCluster_AmbiguousIsIndexedForTheReviewQueue(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		assert.Equal(t, int64(1),
			count(t, ctx, "SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_person_clusters_state'"),
			"the ambiguity review queue needs its index; a partial index on "+
				"state='ambiguous' because the common row is a settled cluster")
	})
}

// TestPersonClusterMember_EmbeddingIsRequired: an appearance with no embedding
// cannot be compared to anything, so it is a member that does not participate
// in the identity decision.
func TestPersonClusterMember_EmbeddingIsRequired(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		clusterID := insertCluster(t, ctx, "embedRequired")

		assert.Error(t, exec(t, ctx,
			"INSERT INTO person_cluster_members (cluster_id, target_type, target_id, embedding) "+
				"VALUES (?, 'scene', 1, NULL)", clusterID),
			"an embedding is NOT NULL: a member with no vector is a member that "+
				"cannot be compared to anything")
	})
}

// TestPersonClusterMember_DetectorScoreIsDistinctFromIdentityConfidence:
// conflating them is how a low-quality detection gets presented as a doubtful
// identity match.
func TestPersonClusterMember_DetectorScoreIsDistinctFromIdentityConfidence(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		clusterID := insertCluster(t, ctx, "scoreDistinct")

		// A terrible detection that is a perfect identity match, and vice
		// versa. Both must be storable, because both are real.
		require.NoError(t, exec(t, ctx,
			"INSERT INTO person_cluster_members (cluster_id, target_type, target_id, detector_score, distance, embedding) "+
				"VALUES (?, 'scene', 1, 0.05, 0.02, x'00')", clusterID),
			"a low detector score with a low distance is a real state: a faint "+
				"frame that matched well")
		require.NoError(t, exec(t, ctx,
			"INSERT INTO person_cluster_members (cluster_id, target_type, target_id, detector_score, distance, embedding) "+
				"VALUES (?, 'scene', 2, 0.99, 0.91, x'00')", clusterID),
			"a high detector score with a high distance is equally real")

		for _, bad := range []float64{-0.1, 1.1} {
			assert.Error(t, exec(t, ctx,
				"INSERT INTO person_cluster_members (cluster_id, target_type, target_id, detector_score, distance, embedding) "+
					"VALUES (?, 'scene', 3, ?, 0.5, x'00')", clusterID, bad),
				"detector_score %v is outside 0..1", bad)
		}
	})
}

// TestPersonClusterMember_AFaceIsIndexedOnce is the rescan property.
//
// Re-detecting the same file must UPDATE rather than insert. A rescan that
// inserts duplicates inflates the cluster size, and the "three or more
// appearances before it is meaningful" threshold gets crossed by one face seen
// three times.
func TestPersonClusterMember_AFaceIsIndexedOnce(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		clusterID := insertCluster(t, ctx, "rescan")

		const ins = "INSERT INTO person_cluster_members " +
			"(cluster_id, target_type, target_id, frame_index, face_left, face_top, embedding) " +
			"VALUES (?, 'scene', 42, 100, 10, 20, x'00')"

		require.NoError(t, exec(t, ctx, ins, clusterID))
		assert.Error(t, exec(t, ctx, ins, clusterID),
			"the same face at the same frame must not be indexed twice")

		// A different frame in the same scene IS a different appearance: the
		// same person at second 12 and at second 9000 is two appearances, and
		// collapsing them would make the cluster size a lie.
		require.NoError(t, exec(t, ctx,
			"INSERT INTO person_cluster_members (cluster_id, target_type, target_id, frame_index, face_left, face_top, embedding) "+
				"VALUES (?, 'scene', 42, 9000, 10, 20, x'00')", clusterID))
		assert.Equal(t, int64(2), count(t, ctx,
			"SELECT count(*) FROM person_cluster_members WHERE cluster_id = ?", clusterID))
	})
}

// TestPersonClusterMember_CascadeOnClusterDelete: members of a deleted cluster
// are orphans nothing can reach, and they hold embeddings.
func TestPersonClusterMember_CascadeOnClusterDelete(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		clusterID := insertCluster(t, ctx, "cascade")

		require.NoError(t, exec(t, ctx,
			"INSERT INTO person_cluster_members (cluster_id, target_type, target_id, embedding) "+
				"VALUES (?, 'gallery', 7, x'00')", clusterID))

		require.NoError(t, exec(t, ctx, "DELETE FROM person_clusters WHERE id = ?", clusterID))
		assert.Equal(t, int64(0), count(t, ctx,
			"SELECT count(*) FROM person_cluster_members WHERE cluster_id = ?", clusterID),
			"deleting a cluster must remove its members; they hold embeddings "+
				"and are unreachable without it")
	})
}

// ---------------------------------------------------------------------------
// Merges
// ---------------------------------------------------------------------------

// TestClusterMerge_CannotMergeAClusterIntoItself: a retried job produces this.
//
// The second attempt sees one cluster and "merges" it with itself, creating a
// self-referential record that makes the member count read as doubled. The
// CHECK is the only thing standing between a retried consolidate pass and a
// doubled cluster.
func TestClusterMerge_CannotMergeAClusterIntoItself(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		a := insertCluster(t, ctx, "selfMergeA")
		b := insertCluster(t, ctx, "selfMergeB")

		require.NoError(t, exec(t, ctx,
			"INSERT INTO person_cluster_merges (winner_id, loser_id, kind, reason) "+
				"VALUES (?, ?, 'merge', 'distinct clusters')", a, b))

		assert.Error(t, exec(t, ctx,
			"INSERT INTO person_cluster_merges (winner_id, loser_id, kind, reason) "+
				"VALUES (?, ?, 'merge', 'a retry')", a, a),
			"a self-merge must be refused; a retried pass that produced one would "+
				"double the cluster's member count in any review UI")
	})
}

// TestClusterMerge_ReasonIsRequired: a merge with no stated reason is a
// moderation queue item nobody can rule on.
func TestClusterMerge_ReasonIsRequired(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		a := insertCluster(t, ctx, "reasonA")
		b := insertCluster(t, ctx, "reasonB")

		assert.Error(t, exec(t, ctx,
			"INSERT INTO person_cluster_merges (winner_id, loser_id, kind, reason) "+
				"VALUES (?, ?, 'merge', NULL)", a, b),
			"a reason is NOT NULL: without it the merge cannot be explained or "+
				"reversed by a moderator")
	})
}

// TestClusterMerge_DecidedByIsNullable: NULL is a genuine case, not a forgotten
// value. The consolidate stage merges clusters without a user, and those merges
// are the ones most in need of being visible to one.
func TestClusterMerge_DecidedByIsNullable(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		a := insertCluster(t, ctx, "nullActorA")
		b := insertCluster(t, ctx, "nullActorB")

		require.NoError(t, exec(t, ctx,
			"INSERT INTO person_cluster_merges (winner_id, loser_id, kind, reason) "+
				"VALUES (?, ?, 'merge', 'consolidate pass, no user')", a, b),
			"an automated merge has no actor and must be storable")

		// A DIFFERENT pair, because the first version of this row reused (a, b)
		// and was refused by idx_cluster_merges_one_per_pair -- correctly. A
		// second merge of the same pair is a duplicate and the index exists to
		// refuse it; the test was asking the store to record something it had
		// already been told is not new information.
		c := insertCluster(t, ctx, "nullActorC")
		uid := insertUser(t, ctx, "mergeActor", false)
		require.NoError(t, exec(t, ctx,
			"INSERT INTO person_cluster_merges (winner_id, loser_id, kind, reason, decided_by) "+
				"VALUES (?, ?, 'merge', 'user action', ?)", a, c, uid),
			"a user-initiated merge records who did it")
	})
}

// TestClusterMerge_OneRecordPerPair: a duplicated record double-counts the merge
// in any review UI, and the duplicate is not information — the second attempt
// learned nothing new.
func TestClusterMerge_OneRecordPerPair(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		a := insertCluster(t, ctx, "dedupA")
		b := insertCluster(t, ctx, "dedupB")

		const ins = "INSERT INTO person_cluster_merges (winner_id, loser_id, kind, reason) " +
			"VALUES (?, ?, 'merge', 'first')"
		require.NoError(t, exec(t, ctx, ins, a, b))
		assert.Error(t, exec(t, ctx, ins, a, b),
			"a duplicate merge record must be refused")

		// The reverse direction is a DIFFERENT merge and must be allowed: a->b
		// then b->a is a split having happened in between, which is real.
		require.NoError(t, exec(t, ctx,
			"INSERT INTO person_cluster_merges (winner_id, loser_id, kind, reason) "+
				"VALUES (?, ?, 'merge', 'after a split')", b, a))
	})
}

// TestClusterMerge_SplitsAreNotDeduplicated: splitting the same face out of the
// same cluster twice is two real events — the second time it was a different
// face. This is why the unique index is PARTIAL on kind='merge'.
func TestClusterMerge_SplitsAreNotDeduplicated(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		a := insertCluster(t, ctx, "splitSrc")
		b := insertCluster(t, ctx, "splitDst1")
		c := insertCluster(t, ctx, "splitDst2")

		require.NoError(t, exec(t, ctx,
			"INSERT INTO person_cluster_merges (winner_id, loser_id, kind, reason) "+
				"VALUES (?, ?, 'split', 'face one was a different person')", a, b))
		require.NoError(t, exec(t, ctx,
			"INSERT INTO person_cluster_merges (winner_id, loser_id, kind, reason) "+
				"VALUES (?, ?, 'split', 'face two was a different person')", a, c),
			"two splits from the same source are two real events")

		assert.Equal(t, int64(2), count(t, ctx,
			"SELECT count(*) FROM person_cluster_merges WHERE kind = 'split' AND winner_id = ?", a))
	})
}

// TestClusterMerge_LoserSurvivesTheRecord: the evidence of what was merged into
// what must not vanish when the loser row goes.
func TestClusterMerge_LoserSurvivesTheRecord(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		a := insertCluster(t, ctx, "loserKeepsA")
		b := insertCluster(t, ctx, "loserKeepsB")

		require.NoError(t, exec(t, ctx,
			"INSERT INTO person_cluster_merges (winner_id, loser_id, kind, reason) "+
				"VALUES (?, ?, 'merge', 'same person')", a, b))

		// The absorbed cluster cannot be hard-deleted while a merge record
		// names it. That is not an inconvenience to work around -- it IS the
		// invariant. A cluster is not deleted, it is marked state='merged' and
		// kept, so the DELETE that would orphan the evidence is refused at the
		// database rather than depending on every caller remembering.
		assert.Error(t, exec(t, ctx, "DELETE FROM person_clusters WHERE id = ?", b),
			"deleting a cluster named by a merge record must be refused; the "+
				"first version of this migration cascaded the delete and lost the "+
				"evidence the row exists to preserve")

		// The supported way to retire it, and what the evidence looks like
		// after. The record is still there, naming a cluster that is now
		// explicitly superseded.
		require.NoError(t, exec(t, ctx,
			"UPDATE person_clusters SET state = 'merged' WHERE id = ?", b))

		assert.Equal(t, int64(1), count(t, ctx,
			"SELECT count(*) FROM person_cluster_merges WHERE loser_id = ?", b),
			"the record of what was merged into what survives the loser being "+
				"retired")
		assert.Equal(t, "merged", scalar(t, ctx,
			"SELECT state FROM person_clusters WHERE id = ?", b),
			"an absorbed cluster is marked merged, not deleted")
	})
}

// insertCluster creates a cluster and returns its id.
func insertCluster(t *testing.T, ctx context.Context, handle string) int64 {
	t.Helper()
	require.NoError(t, exec(t, ctx,
		"INSERT INTO person_clusters (handle) VALUES (?)", handle))
	return scalar(t, ctx, "SELECT id FROM person_clusters WHERE handle = ?", handle).(int64)
}


// TestClusterMember_UniquenessIsScopedToTheCluster is the test for migration
// 100, and it exists because a schema VERSION does not check anything.
//
// Migration 97 ended person_cluster_members with a UNIQUE over
// (target_type, target_id, frame_index, face_left, face_top) -- no cluster_id.
// The comment above it stated the intent ("a face appears in a given target at a
// given frame once, so re-detecting must UPDATE rather than add a second") and
// the constraint implemented a BROADER rule: one face, one cluster, ever.
//
// That makes the `ambiguous` state unrepresentable, and §7.1 defines ambiguous
// as one embedding matching two distinct candidates -- two cluster_ids for one
// appearance. The store could not record a conflict, the UI could not be shown
// both candidates, and ClustersForTarget could never return more than one row,
// which is the query whose multi-row result IS the signal.
//
// A version test would have watched 98 become 99 become 100 and reported green
// throughout, because the version matched and the migration applied. What
// actually broke was a CONSTRAINT SEMANTICS question, and that is what this
// asserts.
func TestClusterMember_UniquenessIsScopedToTheCluster(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		// Two clusters, so the "two distinct candidates" case is expressible.
		store := sqlite.NewClusterStore()

		a, err := store.CreateCluster(ctx, nil)
		assert.NoError(t, err)
		b, err := store.CreateCluster(ctx, nil)
		assert.NoError(t, err)

		// The SAME crop into the same cluster twice is still a duplicate, and
		// refusing it is the property migration 97 was actually for.
		err = store.AddMember(ctx, sqlite.Member{
			ClusterID: a, TargetType: "scene_uniq_a", TargetID: 1, FrameIndex: 0,
			Distance: 0.1, Embedding: []byte{1},
		})
		assert.NoError(t, err)

		err = store.AddMember(ctx, sqlite.Member{
			ClusterID: a, TargetType: "scene_uniq_a", TargetID: 1, FrameIndex: 0,
			Distance: 0.1, Embedding: []byte{1},
		})
		assert.Error(t, err,
			"re-detecting the same crop into the same cluster added a second "+
				"row; a rescan inflates the cluster size and crosses the "+
				"'three or more appearances' threshold with one face seen thrice")

		// The SAME crop into a DIFFERENT cluster is the ambiguous state, and it
		// must be recordable.
		err = store.AddMember(ctx, sqlite.Member{
			ClusterID: b, TargetType: "scene_uniq_a", TargetID: 1, FrameIndex: 0,
			Distance: 0.2, Embedding: []byte{2},
		})
		assert.NoError(t, err,
			"one face cannot be claimed by two clusters; §7.1's ambiguous "+
				"state is exactly that, and it is unrepresentable")

		claimants, err := store.ClustersForTarget(ctx, "scene_uniq_a", 1, 0)
		assert.NoError(t, err)
		assert.Len(t, claimants, 2,
			"ClustersForTarget cannot report a conflict, so the UI is never "+
				"shown both candidates and has nothing to render")

		// And the per-cluster member lists are each still one row.
		membersA, err := store.Members(ctx, a)
		assert.NoError(t, err)
		assert.Len(t, membersA, 1)

		membersB, err := store.Members(ctx, b)
		assert.NoError(t, err)
		assert.Len(t, membersB, 1)
	})
}

// TestPersonClusterNames_ExistsAndRefusesAnEmptyName pins migration 99.
//
// The name column is the whole point of the milestone and the audit table is
// what makes a rename a record rather than an overwrite, so their absence is
// not something a schema-version test would notice.
func TestPersonClusterNames_ExistsAndRefusesAnEmptyName(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		assert.Equal(t, int64(1),
			count(t, ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?",
				"person_cluster_names"),
			"the name audit table must exist; a name with no record of who set "+
				"it is the state the whole design exists to prevent")

		// The CHECK holds for a writer that is not the store -- which is the
		// point of having it in the schema rather than only in Go.
		store := sqlite.NewClusterStore()
		id, err := store.CreateCluster(ctx, nil)
		assert.NoError(t, err)

		assert.Error(t, exec(t, ctx,
			`INSERT INTO person_cluster_names (cluster_id, name, actor) VALUES (?, ?, ?)`,
			id, "", "alice"),
			"an empty name was accepted; an empty string is not an unnamed "+
				"cluster, it is a name that was set to nothing")

		assert.Error(t, exec(t, ctx,
			`INSERT INTO person_cluster_names (cluster_id, name, actor) VALUES (?, ?, ?)`,
			id, "Alice", ""),
			"an empty actor was accepted; an attribution that might be missing "+
				"is not an attribution")

		// A real one works, so the two failures above are the CHECK and not a
		// broken table.
		assert.NoError(t, exec(t, ctx,
			`INSERT INTO person_cluster_names (cluster_id, name, actor) VALUES (?, ?, ?)`,
			id, "Alice", "alice"))
	})
}
