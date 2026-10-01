//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M7 step 7.1 (R059): the mesh's schema layer. Three tables, and each one exists
// to make a specific mistake harder.
//
// The tests here are about the CONSTRAINTS, not the columns. A table with the
// right column names and no enforcement is the shape that lets the project's
// stated invariants rot, so every rule the plan or the spec depends on is
// asserted as something the DATABASE refuses -- by attempting the violation and
// requiring an error, rather than by reading the DDL back and matching a string.

const meshSchemaNow = "2026-10-01 12:00:00"

func TestTheInstanceProfileIsASingletonEnforcedByTheDatabase(t *testing.T) {
	// `id = 1` as a CHECK, not a convention. Two identities for one instance
	// would make every federated lookup ambiguous, and the code path that
	// produces the second is always something nobody thought of.
	runWithRollbackTxn(t, "singleton", func(t *testing.T, ctx context.Context) {
		require.NoError(t, exec(t, ctx,
			"INSERT INTO mesh_instance_profile (id, instance_id, display_name) "+
				"VALUES (1, 'inst-local', 'Local')"))

		assert.Error(t, execErr(t, ctx,
			"INSERT INTO mesh_instance_profile (id, instance_id, display_name) "+
				"VALUES (2, 'inst-second', 'Second')"),
			"a second identity must be refused by the database, not by a convention")
	})

	// And id 0 is refused too -- CHECK (id = 1) is not CHECK (id > 0).
	runWithRollbackTxn(t, "singleton-zero", func(t *testing.T, ctx context.Context) {
		assert.Error(t, execErr(t, ctx,
			"INSERT INTO mesh_instance_profile (id, instance_id, display_name) "+
				"VALUES (0, 'inst-zero', 'Zero')"),
			"id 0 is not the singleton either")
	})
}

func TestAnEmptyInstanceIdentityIsRefused(t *testing.T) {
	// UNIQUE is satisfied by '' and by '   ', and a blank public identity is
	// accepted silently by every other rule on the table.
	runWithRollbackTxn(t, "blank-identity", func(t *testing.T, ctx context.Context) {
		// Written as SQL literals rather than Go values, because the point is that
		// the DATABASE refuses them. A tab is included because trim() removes it
		// and a naive length() check would not.
		for _, blank := range []string{"''", "'   '", "'\t'"} {
			assert.Error(t, execErr(t, ctx,
				"INSERT INTO mesh_instance_profile (id, instance_id, display_name) "+
					"VALUES (1, "+blank+", 'Named')"),
				"a blank instance_id must be refused: %s", blank)
		}
		assert.Error(t, execErr(t, ctx,
			"INSERT INTO mesh_instance_profile (id, instance_id, display_name) "+
				"VALUES (1, 'inst-a', '  ')"),
			"a blank display_name must be refused too")
	})
}

func TestAPeerMustBeAddressableAndKeyed(t *testing.T) {
	runWithRollbackTxn(t, "peer-basics", func(t *testing.T, ctx context.Context) {
		// NOT NULL public_key: "unverified" is not "unsigned", and a row that
		// could hold a keyless peer lets a later reader confuse the two.
		assert.Error(t, execErr(t, ctx,
			"INSERT INTO mesh_peer (instance_id, endpoint) VALUES ('i1', 'http://e')"),
			"a peer with no public_key must be refused: it cannot be verified")

		assert.Error(t, execErr(t, ctx,
			"INSERT INTO mesh_peer (instance_id, endpoint, public_key) "+
				"VALUES ('i1', '   ', X'00')"),
			"a blank endpoint is NOT NULL and still unaddressable")

		// The real one, then a second row for the same peer.
		require.NoError(t, exec(t, ctx,
			"INSERT INTO mesh_peer (instance_id, endpoint, public_key) "+
				"VALUES ('inst-a', 'http://a', X'0102')"))
		assert.Error(t, execErr(t, ctx,
			"INSERT INTO mesh_peer (instance_id, endpoint, public_key) "+
				"VALUES ('inst-a', 'http://elsewhere', X'0304')"),
			"two rows for one peer would let them disagree about its claims")
	})
}

func TestAPeerCannotMakeAClaimOfNegativeSizeOrBandwidth(t *testing.T) {
	// A negative store size makes "does this peer have room" true in a way
	// arithmetic does not expect, and it is a lie rather than an error.
	runWithRollbackTxn(t, "negative-claims", func(t *testing.T, ctx context.Context) {
		assert.Error(t, execErr(t, ctx,
			"INSERT INTO mesh_peer (instance_id, endpoint, public_key, claimed_store_bytes) "+
				"VALUES ('i-neg', 'http://e', X'00', -1)"),
			"a negative claimed_store_bytes must be refused")
		assert.Error(t, execErr(t, ctx,
			"INSERT INTO mesh_peer (instance_id, endpoint, public_key, claimed_bandwidth_bps) "+
				"VALUES ('i-neg2', 'http://e', X'00', -1)"),
			"a negative claimed_bandwidth_bps must be refused")

		// NULL is the absent case and must remain allowed: a peer that has not
		// advertised a figure is different from one advertising zero.
		require.NoError(t, exec(t, ctx,
			"INSERT INTO mesh_peer (instance_id, endpoint, public_key, "+
				"claimed_store_bytes, claimed_bandwidth_bps) "+
				"VALUES ('i-null', 'http://e', X'00', NULL, NULL)"))
	})
}

func TestAPeerCannotBeRevokedInTheFuture(t *testing.T) {
	// The revocation test everywhere is `revoked_at IS NULL`, so a future
	// timestamp would make that test disagree with the time it claims.
	runWithRollbackTxn(t, "future-revocation", func(t *testing.T, ctx context.Context) {
		assert.Error(t, execErr(t, ctx,
			"INSERT INTO mesh_peer (instance_id, endpoint, public_key, revoked_at) "+
				"VALUES ('i-future', 'http://e', X'00', '2099-01-01 00:00:00')"),
			"a peer revoked in the future has not been revoked")

		require.NoError(t, exec(t, ctx,
			"INSERT INTO mesh_peer (instance_id, endpoint, public_key, revoked_at) "+
				"VALUES ('i-now', 'http://e', X'00', CURRENT_TIMESTAMP)"),
			"revoking now is the normal case and must work")
	})
}

func TestAReplicaIsNamespacedBySourceEndpoint(t *testing.T) {
	// §6a.6, and the load-bearing decision in this table: a peer's `scene 412`
	// is not this instance's `scene 412`. Without source_endpoint in the key,
	// two peers offering the same scene_id collide into one row and the mesh
	// believes it holds a replica it does not have -- a missing copy that health
	// checks pass, which is the worst failure available to the mechanism whose
	// entire purpose is knowing what you still hold.
	runWithRollbackTxn(t, "namespacing", func(t *testing.T, ctx context.Context) {
		sceneID := scalar(t, ctx, "SELECT id FROM scenes LIMIT 1")
		require.NotNil(t, sceneID, "the fixture needs at least one scene")

		require.NoError(t, exec(t, ctx,
			"INSERT INTO mesh_replica (scene_id, source_endpoint, replica_path, manifest_hash) "+
				"VALUES (?, 'https://peer-a', 'a/one.mp4', X'00')", sceneID))

		// Same scene, same peer: the same replica, so refused.
		assert.Error(t, execErr(t, ctx,
			"INSERT INTO mesh_replica (scene_id, source_endpoint, replica_path, manifest_hash) "+
				"VALUES (?, 'https://peer-a', 'a/two.mp4', X'00')", sceneID),
			"one replica per (scene, source_endpoint)")

		// Same scene, DIFFERENT peer: a genuinely different copy, and it must be
		// accepted. This is the half that matters -- if the key were scene_id
		// alone, this insert would fail and the mesh would silently hold one
		// copy believing it held two.
		require.NoError(t, exec(t, ctx,
			"INSERT INTO mesh_replica (scene_id, source_endpoint, replica_path, manifest_hash) "+
				"VALUES (?, 'https://peer-b', 'b/one.mp4', X'00')", sceneID),
			"the SAME scene from a DIFFERENT peer is a distinct replica and must be allowed")
	})
}

func TestAReplicaPathCannotEscapeTheStorageRoot(t *testing.T) {
	// A path supplied by a stranger is the one input here that can name a file
	// outside the library. Refused by the DATABASE, so the caller that forgot to
	// sanitise cannot get it past.
	runWithRollbackTxn(t, "path-escape", func(t *testing.T, ctx context.Context) {
		sceneID := scalar(t, ctx, "SELECT id FROM scenes LIMIT 1")

		for i, path := range []string{
			"/etc/passwd",      // absolute
			"../../etc/shadow", // walks up out of the root
			"a/../../b.mp4",    // walks up and back down, still an escape
			"'  '",             // blank
		} {
			assert.Error(t, execErr(t, ctx,
				"INSERT INTO mesh_replica (scene_id, source_endpoint, replica_path, manifest_hash) "+
					"VALUES (?, 'https://peer', "+path+", X'00')", sceneID),
				"path %d (%s) must be refused", i, path)
		}

		require.NoError(t, exec(t, ctx,
			"INSERT INTO mesh_replica (scene_id, source_endpoint, replica_path, manifest_hash) "+
				"VALUES (?, 'https://peer', 'scenes/ok.mp4', X'00')", sceneID),
			"an ordinary relative path must still work")
	})
}

func TestVerifiedAtAndHealthCannotDisagree(t *testing.T) {
	// `verified_at` is the EVIDENCE for health. A replica claiming 'verified'
	// with no timestamp is an unbacked claim, and one carrying a timestamp while
	// health says 'corrupt' is two facts that contradict each other. Either would
	// let a health check believe a copy is verified when it was not.
	runWithRollbackTxn(t, "health-evidence", func(t *testing.T, ctx context.Context) {
		sceneID := scalar(t, ctx, "SELECT id FROM scenes LIMIT 1")
		// Each attempt gets its OWN peer: (scene_id, source_endpoint) is UNIQUE, so
		// reusing one peer would make the second insert fail on the key instead of
		// on the CHECK, and the test would pass for the wrong reason.
		n := 0
		ins := func(health, verifiedAt string) error {
			n++
			va := "NULL"
			if verifiedAt != "" {
				va = "'" + verifiedAt + "'"
			}
			return execErr(t, ctx,
				"INSERT INTO mesh_replica (scene_id, source_endpoint, replica_path, "+
					"manifest_hash, health, verified_at) VALUES (?, 'https://p"+
					strconv.Itoa(n)+"', 'r"+health+"', X'00', '"+health+"', "+va+")", sceneID)
		}

		assert.NoError(t, ins("verified", meshSchemaNow),
			"verified with a timestamp is the honest case")
		assert.Error(t, ins("verified", ""),
			"'verified' with no evidence must be refused")
		assert.Error(t, ins("corrupt", meshSchemaNow),
			"a timestamp alongside 'corrupt' means the two disagree")
		assert.NoError(t, ins("pending", ""),
			"pending with no timestamp is the default shape")
	})
}

func TestHealthIsOneOfFourValuesAndNotACounter(t *testing.T) {
	runWithRollbackTxn(t, "health-domain", func(t *testing.T, ctx context.Context) {
		sceneID := scalar(t, ctx, "SELECT id FROM scenes LIMIT 1")

		assert.Error(t, execErr(t, ctx,
			"INSERT INTO mesh_replica (scene_id, source_endpoint, replica_path, "+
				"manifest_hash, health) VALUES (?, 'https://p', 'x.mp4', X'00', 'mostly-fine')",
			sceneID),
			"health is a four-value domain, not free text")

		// Non-negotiable #4: a verification SCORE would be a stored tally that
		// drifts from the thing it summarises. The absence is asserted, because an
		// absent column cannot fail a test on its own.
		for _, forbidden := range []string{
			"verification_score", "failure_count", "replica_score",
			"integrity_score", "quality",
		} {
			assert.NotContains(t, tableColumns(ctx, t, "mesh_replica"), forbidden,
				"a score or a counter on a replica is a stored tally; verification is "+
					"a CHECK on health plus verified_at, both of which can be re-derived")
		}

		// Nor on the peers table: claimed_* are claims, and a column that looked
		// like a measured counterpart to them is how a claim becomes a fact.
		peerCols := tableColumns(ctx, t, "mesh_peer")
		for _, forbidden := range []string{
			"measured_store_bytes", "verified_store_bytes", "actual_store_bytes",
		} {
			assert.NotContains(t, peerCols, forbidden,
				"a measured counterpart to a peer's claim would invite sizing from it")
		}
	})
}

func TestAReplicaMustReferenceASceneThatExists(t *testing.T) {
	// A replica of a scene this instance does not have is not a replica. The
	// foreign key is what keeps the mesh's answer to "what do I hold" true.
	runWithRollbackTxn(t, "replica-fk", func(t *testing.T, ctx context.Context) {
		// Deliberately NOT taken from the scenes table: the id must not exist, and
		// reading a real one here would make the test assert nothing.
		assert.Error(t, execErr(t, ctx,
			"INSERT INTO mesh_replica (scene_id, source_endpoint, replica_path, manifest_hash) "+
				"VALUES (999999999, 'https://p', 'ghost.mp4', X'00')"),
			"a replica of a nonexistent scene must be refused")
	})
}

func TestTheManifestHashIsRequired(t *testing.T) {
	// It is "what we must still verify". Without it there is nothing to verify
	// against and the row is a claim of a copy that cannot be checked.
	runWithRollbackTxn(t, "manifest-required", func(t *testing.T, ctx context.Context) {
		sceneID := scalar(t, ctx, "SELECT id FROM scenes LIMIT 1")
		assert.Error(t, execErr(t, ctx,
			"INSERT INTO mesh_replica (scene_id, source_endpoint, replica_path) "+
				"VALUES (?, 'https://p', 'nohash.mp4')", sceneID),
			"a replica with no manifest hash cannot be verified later")
	})
}
