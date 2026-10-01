//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/sqlite"
)

// The audit chain exists because §5.1 claims the owner is not an admin over
// content, and an append-only log that any SQL session can UPDATE does not
// support that claim. These tests therefore do the one thing the AuditStore's
// own API forbids: they tamper, through raw SQL, and assert the walk notices.
//
// Every test here has a matching "clean" case. A test that only ever asserts
// failure would pass against a verify function that always reports a break.

func auditChainHead(t *testing.T, ctx context.Context) []byte {
	t.Helper()
	s := sqlite.NewAuditStore()
	st, err := s.VerifyChain(ctx)
	require.NoError(t, err)
	return st.Head
}
func auditRowCount(t *testing.T, ctx context.Context) int {
	t.Helper()
	var n int
	require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &n,
		"SELECT COUNT(*) FROM collab_audit"))
	return n
}

// firstAuditID returns the lowest id currently in the table. Tests must compare
// against this rather than assuming ids start at 1: withRollbackTxn rolls back
// each test's writes, but SQLite's AUTOINCREMENT counter does NOT roll back, so
// ids keep climbing across tests in a shared database. Asserting `BrokenAt == 1`
// therefore passes alone and fails in a full-suite run -- which is exactly the
// order-dependent bug the package's own comments warn about elsewhere.
func firstAuditID(t *testing.T, ctx context.Context) int {
	t.Helper()
	var id int
	require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &id,
		"SELECT COALESCE(MIN(id), 0) FROM collab_audit"))
	return id
}

func TestAuditChainAFreshTableVerifies(t *testing.T) {
	runWithRollbackTxn(t, "chain-empty", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAuditStore()
		st, err := s.VerifyChain(ctx)
		require.NoError(t, err)
		assert.True(t, st.Intact(),
			"an empty or unchained table must still report intact, not error: %s", st.Reason)
	})
}

func TestAuditChainRowsLinkToEachOther(t *testing.T) {
	runWithRollbackTxn(t, "chain-link", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAuditStore()
		userID := mustCreateUser(ctx, t, "chainlink")

		for i := 0; i < 4; i++ {
			require.NoError(t, s.Append(ctx, &userID, "proposal_created",
				"proposal", nil, "title", map[string]interface{}{"n": i}))
		}

		st, err := s.VerifyChain(ctx)
		require.NoError(t, err)
		assert.True(t, st.Intact(), "four chained rows must verify: %s", st.Reason)
		// The walk must have reached the newest row, whose id is the highest --
		// not a fixed number, because AUTOINCREMENT does not roll back with the
		// test transaction and ids keep climbing across a shared database.
		var maxID int
		require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &maxID,
			"SELECT COALESCE(MAX(id), 0) FROM collab_audit"))
		assert.Equal(t, maxID, st.Rows, "the walk must reach the newest row")
		assert.Len(t, st.Head, 32, "the head is a sha256, so 32 bytes")
	})
}

func TestAuditChainLoginFailuresChainToo(t *testing.T) {
	// The row an attacker most wants gone is the one recording the flood, so it
	// must be inside the chain. It is written by a different method on purpose:
	// two write paths is exactly the shape that produces an unchained row.
	runWithRollbackTxn(t, "chain-loginfail", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAuditStore()
		require.NoError(t, s.RecordLoginFailure(ctx, "victim", "10.0.0.1", "bad password"))
		require.NoError(t, s.RecordLoginFailure(ctx, "victim", "10.0.0.2", "bad password"))
		require.NoError(t, s.RecordLoginFailure(ctx, "victim", "10.0.0.3", "bad password"))

		st, err := s.VerifyChain(ctx)
		require.NoError(t, err)
		assert.True(t, st.Intact(),
			"login failures go through their own method and must still chain: %s", st.Reason)
		var maxID int
		require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &maxID,
			"SELECT COALESCE(MAX(id), 0) FROM collab_audit"))
		assert.Equal(t, maxID, st.Rows)
	})
}

// --- the tampering cases -----------------------------------------------------
//
// Each of these needs raw SQL, because the point is to do what the Go API
// deliberately does not expose.

func rawExec(t *testing.T, ctx context.Context, query string, args ...interface{}) {
	t.Helper()
	_, err := sqlite.DbgAuditRawExec(ctx, query, args...)
	require.NoError(t, err, "the tampering statement itself must succeed -- if it "+
		"fails, the test proves nothing about detection")
}

func TestAuditChainDetectsAnEditedRow(t *testing.T) {
	runWithRollbackTxn(t, "chain-edit", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAuditStore()
		userID := mustCreateUser(ctx, t, "chainedit")
		for i := 0; i < 3; i++ {
			require.NoError(t, s.Append(ctx, &userID, "proposal_created", "proposal", nil, "title", nil))
		}

		// Sanity: intact before tampering. Without this, a walk that always
		// reports a break would satisfy the assertion below.
		st, err := s.VerifyChain(ctx)
		require.NoError(t, err)
		require.True(t, st.Intact(), "precondition: chain is intact before the edit")

		// Rewrite history: make the first action something it never was.
		first := firstAuditID(t, ctx)
		rawExec(t, ctx, "UPDATE collab_audit SET action = 'innocent_action' WHERE id = ?", first)

		st, err = s.VerifyChain(ctx)
		require.NoError(t, err)
		assert.False(t, st.Intact(), "an edited row must break the chain")
		assert.Equal(t, first, st.BrokenAt, "the break must name the first bad row, not a later one")
		assert.Contains(t, st.Reason, fmt.Sprintf("row %d", first),
			"the reason must identify the row: %s", st.Reason)
	})
}

func TestAuditChainDetectsADeletedRow(t *testing.T) {
	// Recomputing each row's hash alone would NOT catch this: every remaining
	// row still hashes correctly. Only comparing the stored prev_hash against
	// the previous row's actual hash notices that a link no longer points at
	// its predecessor.
	runWithRollbackTxn(t, "chain-delete", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAuditStore()
		userID := mustCreateUser(ctx, t, "chaindelete")
		for i := 0; i < 3; i++ {
			require.NoError(t, s.Append(ctx, &userID, "proposal_created", "proposal", nil, "title", nil))
		}
		st, err := s.VerifyChain(ctx)
		require.NoError(t, err)
		require.True(t, st.Intact())

		first := firstAuditID(t, ctx)
		rawExec(t, ctx, "DELETE FROM collab_audit WHERE id = ?", first+1)

		st, err = s.VerifyChain(ctx)
		require.NoError(t, err)
		assert.False(t, st.Intact(), "a deleted row must break the chain -- its successor "+
			"chains from a hash that is no longer there")
		assert.Equal(t, first+2, st.BrokenAt,
			"the break surfaces at the row AFTER the gap, since that is the row "+
				"whose link no longer resolves")
	})
}

func TestAuditChainDetectsARewiredLink(t *testing.T) {
	// The sophisticated version of deletion: remove the row and re-point the
	// successor's prev_hash at its new predecessor, so the sequence of hashes
	// looks continuous. The successor's own content hash no longer matches,
	// because its hash was computed over the old prev_hash.
	runWithRollbackTxn(t, "chain-rewire", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAuditStore()
		userID := mustCreateUser(ctx, t, "chainrewire")
		for i := 0; i < 3; i++ {
			require.NoError(t, s.Append(ctx, &userID, "proposal_created", "proposal", nil, "title", nil))
		}
		st, err := s.VerifyChain(ctx)
		require.NoError(t, err)
		require.True(t, st.Intact())

		// Swap rows 2 and 3 by rewriting row 3's link to row 1's hash.
		first := firstAuditID(t, ctx)
		rawExec(t, ctx, `UPDATE collab_audit
			SET prev_hash = (SELECT row_hash FROM collab_audit WHERE id = ?)
			WHERE id = ?`, first, first+2)
		rawExec(t, ctx, "DELETE FROM collab_audit WHERE id = ?", first+1)

		st, err = s.VerifyChain(ctx)
		require.NoError(t, err)
		assert.False(t, st.Intact(),
			"rewiring prev_hash to hide a deletion must still break the chain: "+
				"the row's own content hash was computed over the old predecessor")
	})
}

func TestAuditChainDetectsAnAppendedBackdatedRow(t *testing.T) {
	runWithRollbackTxn(t, "chain-forged", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAuditStore()
		userID := mustCreateUser(ctx, t, "chainforged")
		require.NoError(t, s.Append(ctx, &userID, "proposal_created", "proposal", nil, "title", nil))

		// A forged row inserted straight into the table: plausible content, and
		// a prev_hash copied from the real head so the sequence looks continuous.
		rawExec(t, ctx, `INSERT INTO collab_audit
			(actor_id, action, target_type, target_id, field, detail, at, prev_hash, row_hash)
			SELECT actor_id, 'never_happened', 'proposal', NULL, 'title', NULL, at,
			       row_hash, row_hash
			FROM collab_audit WHERE id = ?`, firstAuditID(t, ctx))

		st, err := s.VerifyChain(ctx)
		require.NoError(t, err)
		assert.False(t, st.Intact(),
			"a row inserted outside the write path carries a row_hash that does not "+
				"match its content, even when its prev_hash is copied correctly")
	})
}

func TestAuditChainDetectsARewrittenTimestamp(t *testing.T) {
	// `at` is inside the hashed payload on purpose. A moderation action whose
	// timestamp can be edited after the fact is not a record of when it happened.
	runWithRollbackTxn(t, "chain-timestamp", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAuditStore()
		userID := mustCreateUser(ctx, t, "chaintime")
		require.NoError(t, s.Append(ctx, &userID, "proposal_created", "proposal", nil, "title", nil))
		st, err := s.VerifyChain(ctx)
		require.NoError(t, err)
		require.True(t, st.Intact())

		first := firstAuditID(t, ctx)
		rawExec(t, ctx, "UPDATE collab_audit SET at = '2001-01-01T00:00:00Z' WHERE id = ?", first)

		st, err = s.VerifyChain(ctx)
		require.NoError(t, err)
		assert.False(t, st.Intact(), "backdating a row must break the chain")
		assert.Equal(t, first, st.BrokenAt)
	})
}

func TestAuditChainVerifiesFromGenesisNotFromTheFirstRowItSees(t *testing.T) {
	// The first row's prev_hash must be 32 zero bytes. If verification simply
	// adopted whatever the first row claimed, an attacker could rewrite the
	// entire table and verification would agree with it.
	runWithRollbackTxn(t, "chain-genesis", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAuditStore()
		require.NoError(t, s.Append(ctx, nil, "login_failed", "", nil, "",
			map[string]interface{}{"ip": "1.2.3.4"}))

		var prev []byte
		require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &prev,
			"SELECT prev_hash FROM collab_audit WHERE id = ?", firstAuditID(t, ctx)))
		assert.Len(t, prev, 32, "genesis is a sha256 of nothing: 32 bytes")
		assert.Equal(t, make([]byte, 32), []byte(prev),
			"row 1 must chain from all zeroes, not from an arbitrary seed")
	})
}

// --- the payload encoding ---------------------------------------------------

func TestAuditChainSeparatesFieldsSoDataCannotMoveBetweenThem(t *testing.T) {
	// With a separator-joined payload, ("a|b", "c") and ("a", "b|c") hash the
	// same, so an attacker could shift a value from one column to another
	// without invalidating anything. Length-prefixing is what prevents it, and
	// this is the test that would fail if someone "simplified" it back.
	runWithRollbackTxn(t, "chain-ambiguity", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAuditStore()
		require.NoError(t, s.Append(ctx, nil, "a", "b|c", nil, "d", nil))

		first := firstAuditID(t, ctx)
		var action, targetType sql.NullString
		require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &action,
			"SELECT action FROM collab_audit WHERE id = ?", first))
		require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &targetType,
			"SELECT target_type FROM collab_audit WHERE id = ?", first))
		require.Equal(t, "a", action.String)
		require.Equal(t, "b|c", targetType.String)

		st, err := s.VerifyChain(ctx)
		require.NoError(t, err)
		assert.True(t, st.Intact(), "a value containing the separator must round-trip "+
			"and still verify: %s", st.Reason)

		// Move the value across the boundary, exactly as a separator-joined hash
		// would be unable to notice.
		rawExec(t, ctx, "UPDATE collab_audit SET action = 'a|b', target_type = 'c' WHERE id = ?", first)
		st, err = s.VerifyChain(ctx)
		require.NoError(t, err)
		assert.False(t, st.Intact(),
			"moving a value between two columns must change the hash -- the payload "+
				"is length-prefixed, so the field boundary is part of what is hashed")
	})
}

// The deletion test above is caught by whichever check runs first, and both
// consume `prev`, so disabling either one alone leaves the other to catch it.
// This test pins the link check's OWN value, which is that it distinguishes
// "a row was removed" from "a row's content was altered" -- two different
// incidents that must not produce the same diagnosis.
//
// It is here because a mutation that disables the link check SURVIVED the suite
// as originally written. That is the definition of a redundant-looking guard,
// and the fix is a test that fails when it is removed, not a comment claiming it
// matters.
func TestAuditChainNamesARemovedRowDistinctlyFromAnAlteredOne(t *testing.T) {
	runWithRollbackTxn(t, "chain-diagnosis", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAuditStore()
		userID := mustCreateUser(ctx, t, "chaindiag")
		for i := 0; i < 3; i++ {
			require.NoError(t, s.Append(ctx, &userID, "proposal_created", "proposal", nil, "title", nil))
		}
		first := firstAuditID(t, ctx)

		// (a) remove a row
		rawExec(t, ctx, "DELETE FROM collab_audit WHERE id = ?", first+1)
		st, err := s.VerifyChain(ctx)
		require.NoError(t, err)
		require.False(t, st.Intact())
		removed := st.Reason
		assert.Contains(t, removed, "removed",
			"a removed row must be reported as a removal, not as a content mismatch: %s", removed)
	})

	runWithRollbackTxn(t, "chain-diagnosis-altered", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAuditStore()
		userID := mustCreateUser(ctx, t, "chaindiag2")
		for i := 0; i < 3; i++ {
			require.NoError(t, s.Append(ctx, &userID, "proposal_created", "proposal", nil, "title", nil))
		}
		first := firstAuditID(t, ctx)

		// (b) alter a row's content, leaving every link in place
		rawExec(t, ctx, "UPDATE collab_audit SET action = 'different' WHERE id = ?", first)
		st, err := s.VerifyChain(ctx)
		require.NoError(t, err)
		require.False(t, st.Intact())
		assert.Contains(t, st.Reason, "content does not match its hash",
			"an altered row must be reported as a content mismatch: %s", st.Reason)
	})
}

// A row written before the chain existed, or by a path that does not chain, has
// no usable row_hash. The walk must say so rather than treating the empty value
// as if it verified -- and it must be the reason it gives, because "this row is
// not in the chain" and "this row was altered" call for different responses.
func TestAuditChainReportsARowWithNoHashAsUnchained(t *testing.T) {
	runWithRollbackTxn(t, "chain-nohash", func(t *testing.T, ctx context.Context) {
		s := sqlite.NewAuditStore()
		userID := mustCreateUser(ctx, t, "chainnohash")
		require.NoError(t, s.Append(ctx, &userID, "proposal_created", "proposal", nil, "title", nil))
		st, err := s.VerifyChain(ctx)
		require.NoError(t, err)
		require.True(t, st.Intact())

		first := firstAuditID(t, ctx)
		// Simulate a pre-chain database: the row exists with its content, but no
		// hashes were ever computed for it.
		rawExec(t, ctx, "UPDATE collab_audit SET row_hash = NULL, prev_hash = NULL WHERE id = ?", first)

		st, err = s.VerifyChain(ctx)
		require.NoError(t, err)
		assert.False(t, st.Intact(),
			"a row with no row_hash cannot be part of a verified chain and must not "+
				"read as intact -- an empty hash compared equal to nothing would pass")
		assert.Equal(t, first, st.BrokenAt)
		assert.Contains(t, st.Reason, "predates the chain",
			"the reason must distinguish 'never chained' from 'altered': %s", st.Reason)
	})
}
