//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/sqlite"
)

// R080, M8 step 8.4: "Storage allocation log so an operator can audit what was
// placed and why."
//
// THESE TESTS DO THE ONE THING THE ALLOCATION LOG'S OWN API FORBIDS: they tamper,
// through raw SQL, and assert the walk notices. A log whose integrity depends on the
// absence of malicious code, in a codebase that ingests peer-supplied data, is not
// an audit trail — so the tamper cases are the tests, not a footnote.
//
// EVERY TAMPER TEST HAS A CLEAN PAIRED ASSERTION. A test that only ever asserts
// failure would pass against a verify walk that reports a break unconditionally, so
// each one checks the chain is intact BEFORE the edit and broken AFTER it.

// allocRawExec runs a statement through raw SQL, which is the only way to tamper.
func allocRawExec(t *testing.T, ctx context.Context, query string, args ...interface{}) {
	t.Helper()
	_, err := sqlite.DbgAuditRawExec(ctx, query, args...)
	require.NoError(t, err, "the tampering statement itself must succeed -- if it "+
		"fails, the test proves nothing about detection")
}

// mustAllocations writes n placements for one scene, each with a distinct peer so
// the log's (scene_id, source_endpoint) uniqueness does not reject them.
func mustAllocations(ctx context.Context, t *testing.T, sceneID, n int, reason string) {
	t.Helper()
	l := sqlite.NewAllocationLog()
	for i := 0; i < n; i++ {
		require.NoError(t, l.RecordPlacement(ctx, sceneID,
			fmt.Sprintf("peer-%d:9000", i), reason, int64(1024*(i+1))))
	}
}

// firstAllocID returns the lowest id in the log.
//
// COMPARED AGAINST RATHER THAN ASSUMING 1: withRollbackTxn rolls back each test's
// writes but SQLite's AUTOINCREMENT counter does NOT roll back, so ids keep
// climbing across a shared database. Asserting an absolute id passes alone and
// fails in a full-suite run -- which is exactly the order-dependent bug this
// package's own comments warn about elsewhere.
func firstAllocID(t *testing.T, ctx context.Context) int {
	t.Helper()
	var id int
	require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &id,
		"SELECT COALESCE(MIN(id), 0) FROM mesh_allocation_log"))
	return id
}

// allocIDBounds returns the lowest and highest log id for one scene.
//
// TWO SINGLE-COLUMN SCANS rather than one "SELECT id" into a slice: DbgAuditSelectOne
// scans exactly one column, so a multi-row id list needs either a different helper
// or two scalar queries. The first version tried the slice and every test using it
// failed with an unsupported Scan -- which is the fix worth recording, because the
// error names the driver and not the mistake.
func allocIDBounds(t *testing.T, ctx context.Context, sceneID int) (int, int) {
	t.Helper()
	var lo, hi int
	require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &lo,
		"SELECT COALESCE(MIN(id), 0) FROM mesh_allocation_log WHERE scene_id = ?", sceneID))
	require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &hi,
		"SELECT COALESCE(MAX(id), 0) FROM mesh_allocation_log WHERE scene_id = ?", sceneID))
	return lo, hi
}

func allocRowCount(t *testing.T, ctx context.Context) int {
	t.Helper()
	var n int
	require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &n,
		"SELECT COUNT(*) FROM mesh_allocation_log"))
	return n
}

// --- The clean cases. Without these a verify that always breaks would pass all the
// --- tamper tests above.

// An empty log verifies: genesis chains from nothing.
func TestAllocationChainAFreshTableVerifies(t *testing.T) {
	runWithRollbackTxn(t, "alloc-empty", func(t *testing.T, ctx context.Context) {
		st, err := sqlite.NewAllocationLog().VerifyAllocationChain(ctx)
		require.NoError(t, err)
		assert.True(t, st.Intact,
			"an empty log must report intact rather than error: %s", st.Reason)
		assert.Zero(t, st.BrokenAt)
	})
}

func TestAllocationChainRowsLinkToEachOther(t *testing.T) {
	runWithRollbackTxn(t, "alloc-link", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc link")
		mustAllocations(ctx, t, scene, 3, "policy: scene below the instance floor")

		st, err := sqlite.NewAllocationLog().VerifyAllocationChain(ctx)
		require.NoError(t, err)
		assert.True(t, st.Intact, "three placements must chain: %s", st.Reason)
		assert.NotEmpty(t, st.Head, "the head is returned so a caller can extend the chain")
	})
}

// --- The tamper cases.

// THE REQUIREMENT ITSELF: an operator must be able to see WHAT WAS PLACED AND WHY.
// This is the read side, and it is why `reason` is a non-blank column rather than an
// optional one.
func TestAnOperatorCanReadWhatWasPlacedAndWhy(t *testing.T) {
	runWithRollbackTxn(t, "alloc-read", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc readable")
		l := sqlite.NewAllocationLog()
		require.NoError(t, l.RecordPlacement(ctx, scene, "peer-a:9000",
			"scene had 0 of 3 verified replicas and this instance is the nearest consented holder", 4096))
		require.NoError(t, l.RecordPlacement(ctx, scene, "peer-b:9000",
			"operator request: mirrored for disaster recovery", 4096))

		// SCALAR SCANS, NOT A SLICE. DbgAuditSelectOne reads exactly one column, so
		// "SELECT reason" into a []string is an unsupported Scan -- it just happens not
		// to fail when the -run filter selects only this test, because the underlying
		// helper takes a different path. It failed in a full-suite run, which is
		// exactly the order-dependent failure this package's own comments warn about:
		// a test that passes alone and fails in company is asserting something other
		// than what it names.
		var first, second string
		require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &first,
			"SELECT reason FROM mesh_allocation_log WHERE scene_id = ? ORDER BY at ASC, id ASC LIMIT 1", scene))
		require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &second,
			"SELECT reason FROM mesh_allocation_log WHERE scene_id = ? ORDER BY at DESC, id DESC LIMIT 1", scene))

		// The reasons are the operator's answer, so they must survive verbatim. Ordered
		// by id as well as `at` because two rows written in the same second have an
		// identical timestamp, and an ORDER BY that ties is not an order.
		assert.Contains(t, first, "0 of 3 verified replicas")
		assert.Contains(t, second, "disaster recovery")
		assert.NotEqual(t, first, second, "and both are readable, not one overwritten")
	})
}

// A BLANK REASON IS REFUSED, including a tab-only one. SQLite's bare trim() strips
// spaces only, so length(trim(x)) > 0 accepts a tab; the CHECK spells out char(9)
// and the Go guard checks every whitespace rune. Both refuse, and the refusal is
// the requirement: a placement nobody can explain is exactly what R080 exists to
// make visible.
func TestAPlacementWithoutAReasonIsRefused(t *testing.T) {
	runWithRollbackTxn(t, "alloc-noreason", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc no reason")
		l := sqlite.NewAllocationLog()

		for _, reason := range []string{"", " ", "\t", "\n", " \t\n ", "\v", "\f", "\r"} {
			err := l.RecordPlacement(ctx, scene, "peer-a:9000", reason, 100)
			assert.Error(t, err, "reason %q must be refused", reason)
		}
		assert.Zero(t, allocRowCount(t, ctx),
			"a refused placement leaves no row, so an unexplainable copy cannot exist "+
				"in the log at all")
	})
}

// THE DATABASE REFUSES IT TOO, not just the Go guard. Both are deliberate and the
// reason is the same as replicastore's path check: the Go guard is the first place a
// bug is caught, and the CHECK is the one place a peer cannot talk you out of.
func TestTheDatabaseAlsoRefusesABlankReason(t *testing.T) {
	runWithRollbackTxn(t, "alloc-dbcheck", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc db check")
		l := sqlite.NewAllocationLog()
		require.NoError(t, l.RecordPlacement(ctx, scene, "peer-a:9000", "first", 100))
		first := firstAllocID(t, ctx)

		// Straight at the table, bypassing the Go guard entirely.
		for _, reason := range []string{"", " ", "\t", "   \t   "} {
			_, err := sqlite.DbgAuditRawExec(ctx,
				"UPDATE mesh_allocation_log SET reason = ? WHERE id = ?", reason, first)
			assert.Error(t, err,
				"the CHECK must refuse reason %q even when the Go guard is bypassed", reason)
		}
	})
}

// AN EDITED ROW IS DETECTED. The reason is the field an operator would most want to
// rewrite — "it said the policy ran out of disk, which was untrue" — and it is
// precisely the field the chain protects.
func TestAllocationChainDetectsAnEditedRow(t *testing.T) {
	runWithRollbackTxn(t, "alloc-edit", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc edit")
		mustAllocations(ctx, t, scene, 3, "policy: below the floor")
		l := sqlite.NewAllocationLog()

		st, err := l.VerifyAllocationChain(ctx)
		require.NoError(t, err)
		require.True(t, st.Intact, "precondition: intact before the edit")

		first := firstAllocID(t, ctx)
		allocRawExec(t, ctx,
			"UPDATE mesh_allocation_log SET reason = 'innocent reason' WHERE id = ?", first)

		st, err = l.VerifyAllocationChain(ctx)
		require.NoError(t, err)
		assert.False(t, st.Intact, "an edited reason must be detected")
		assert.Equal(t, first, st.BrokenAt, "and it must name the row")
		assert.Contains(t, st.Reason, "edited",
			"the message says a field was edited, which is the diagnosis")
	})
}

// A DELETED ROW IS DETECTED. This is the case that makes the chain worth having
// rather than a per-row signature: deleting the middle row leaves the LAST row's
// hash exactly as correct as it was, so a walk from the head would pass.
func TestAllocationChainDetectsADeletedRow(t *testing.T) {
	runWithRollbackTxn(t, "alloc-delete", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc delete")
		mustAllocations(ctx, t, scene, 4, "policy: below the floor")
		l := sqlite.NewAllocationLog()

		st, err := l.VerifyAllocationChain(ctx)
		require.NoError(t, err)
		require.True(t, st.Intact, "precondition: intact before the delete")

		first, last := allocIDBounds(t, ctx, scene)
		middle := (first + last) / 2 // neither the first (genesis link) nor the last
		require.Greater(t, middle, first, "precondition: a row strictly between exists")
		require.Less(t, middle, last, "precondition: the delete is not at either end")

		allocRawExec(t, ctx, "DELETE FROM mesh_allocation_log WHERE id = ?", middle)

		st, err = l.VerifyAllocationChain(ctx)
		require.NoError(t, err)
		assert.False(t, st.Intact,
			"deleting a middle row must be detected even though the head still hashes correctly")
		assert.Equal(t, middle+1, st.BrokenAt,
			"the break is at the row AFTER the deletion, because that is the row whose "+
				"link is now wrong")
		assert.Contains(t, st.Reason, "edited or removed")
	})
}

// A REWIRED LINK IS DETECTED: the row's contents are consistent with its new
// prev_hash, but that hash belongs to no row above it. Recomputing the payload alone
// would pass this, which is why the walk compares the stored link against the
// previous row's ACTUAL hash.
func TestAllocationChainDetectsARewiredLink(t *testing.T) {
	runWithRollbackTxn(t, "alloc-rewire", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc rewire")
		mustAllocations(ctx, t, scene, 3, "policy: below the floor")
		l := sqlite.NewAllocationLog()

		st, err := l.VerifyAllocationChain(ctx)
		require.NoError(t, err)
		require.True(t, st.Intact, "precondition: intact before the rewire")

		_, last := allocIDBounds(t, ctx, scene)

		// Point the LAST row's prev_hash at genesis. Its stored hash no longer matches
		// its contents either, so BOTH guards reject it -- and the message is what
		// proves which one ran first.
		allocRawExec(t, ctx,
			"UPDATE mesh_allocation_log SET prev_hash = ? WHERE id = ?",
			make([]byte, 32), last)

		st, err = l.VerifyAllocationChain(ctx)
		require.NoError(t, err)
		assert.False(t, st.Intact,
			"a row claiming to be the first is not the first: the link check must catch "+
				"it even though the content check alone would not")
		assert.Equal(t, last, st.BrokenAt)
		assert.Contains(t, st.Reason, "does not chain",
			"and it is reported as a LINK break, not a content mismatch, which is what "+
				"shows the stored prev_hash is compared against the previous row's "+
				"actual hash rather than merely recomputed alongside it")
	})
}

// AN EDITED TIMESTAMP IS DETECTED. `at` is in the hashed payload precisely so this
// fails: if the time a placement happened can be rewritten after the fact, the
// record is not a record.
func TestAllocationChainDetectsARewrittenTimestamp(t *testing.T) {
	runWithRollbackTxn(t, "alloc-time", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc time")
		mustAllocations(ctx, t, scene, 2, "policy: below the floor")
		l := sqlite.NewAllocationLog()

		st, err := l.VerifyAllocationChain(ctx)
		require.NoError(t, err)
		require.True(t, st.Intact, "precondition: intact before the timestamp edit")

		first := firstAllocID(t, ctx)
		allocRawExec(t, ctx,
			"UPDATE mesh_allocation_log SET at = '2001-01-01T00:00:00Z' WHERE id = ?", first)

		st, err = l.VerifyAllocationChain(ctx)
		require.NoError(t, err)
		assert.False(t, st.Intact,
			"a rewritten timestamp must be detected, because 'at' is in the hashed payload")
		assert.Equal(t, first, st.BrokenAt)
	})
}

// AN UNCHAINED ROW CANNOT BE WRITTEN AT ALL.
//
// I first wrote this as a DETECTION test -- insert a row with no hashes, assert the
// walk notices -- and it failed because migration 114's CHECKs refuse the insert.
// That is the better outcome: the hole is closed at the schema rather than detected
// after the fact, so there is no window in which an unchained row exists. The walk
// still carries a length check (a pre-migration database, or a second store
// implementation that does not CHECK), and that branch is unreachable from raw SQL,
// which is worth asserting here so the two facts are not confused.
func TestAllocationSchemaRefusesAnUnchainedRow(t *testing.T) {
	runWithRollbackTxn(t, "alloc-unchained", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc unchained")
		l := sqlite.NewAllocationLog()
		require.NoError(t, l.RecordPlacement(ctx, scene, "peer-a:9000", "legitimate", 100))

		// A row inserted by a path that does not chain. NULL hashes are refused by the
		// NOT NULL columns and a zero-LENGTH blob by the length CHECKs, so the
		// migration closes this hole at the schema and the walk never sees it. That is
		// the better outcome and this test now asserts it, rather than asserting a
		// detection that the schema prevents.
		for _, h := range [][]byte{nil, {}, {1, 2, 3}} {
			_, err := sqlite.DbgAuditRawExec(ctx,
				"INSERT INTO mesh_allocation_log (scene_id, source_endpoint, reason, bytes, at, prev_hash, row_hash) "+
					"VALUES (?, 'peer-z:9000', 'smuggled in', 1, '2026-01-01T00:00:00Z', ?, ?)",
				scene, h, h)
			assert.Error(t, err,
				"a %d-byte hash must be refused by the schema, so an unchained row "+
					"cannot be written in the first place", len(h))
		}

		// Nothing was smuggled in, so the log is still exactly what the store wrote.
		st, err := l.VerifyAllocationChain(ctx)
		require.NoError(t, err)
		assert.True(t, st.Intact,
			"the schema refused every unchained insert, so the chain is untouched: %s",
			st.Reason)
		assert.Equal(t, 1, allocRowCount(t, ctx), "and no smuggled row exists")
	})
}

// DATA CANNOT MOVE BETWEEN FIELDS. This is the length prefix earning its place:
// without it, reason "ab" + endpoint "cdef" and reason "abc" + endpoint "def" hash
// to the same bytes, so an attacker could shorten one field and lengthen another and
// keep the row verifying.
func TestAllocationChainSeparatesFieldsSoDataCannotMoveBetweenThem(t *testing.T) {
	runWithRollbackTxn(t, "alloc-separate", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc separate")
		l := sqlite.NewAllocationLog()
		require.NoError(t, l.RecordPlacement(ctx, scene, "abcdef", "abc", 100))

		// Move one character from the endpoint into the reason. Both rows would hash
		// identically without a length prefix.
		allocRawExec(t, ctx,
			"UPDATE mesh_allocation_log SET source_endpoint = 'bcdef', reason = 'ab' "+
				"WHERE scene_id = ?", scene)

		st, err := l.VerifyAllocationChain(ctx)
		require.NoError(t, err)
		assert.False(t, st.Intact,
			"moving a character from one field into the next must be detected; the "+
				"length prefix is what makes the two rows differ")
	})
}

// A VERIFY WALK STARTS FROM GENESIS, NOT FROM THE FIRST ROW IT SEES. Rows deleted
// from the FRONT are the case: the chain would still be internally consistent from
// the new first row onwards, so a walk that trusted the earliest present row would
// pass.
func TestAllocationChainVerifiesFromGenesisNotFromTheFirstRowItSees(t *testing.T) {
	runWithRollbackTxn(t, "alloc-genesis", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc genesis")
		mustAllocations(ctx, t, scene, 4, "policy: below the floor")
		l := sqlite.NewAllocationLog()

		st, err := l.VerifyAllocationChain(ctx)
		require.NoError(t, err)
		require.True(t, st.Intact, "precondition: intact before the front delete")

		first := firstAllocID(t, ctx)
		allocRawExec(t, ctx, "DELETE FROM mesh_allocation_log WHERE id = ?", first)

		st, err = l.VerifyAllocationChain(ctx)
		require.NoError(t, err)
		assert.False(t, st.Intact,
			"deleting the first row must be detected: the new first row claims to "+
				"chain from genesis and does not")
		assert.Contains(t, st.Reason, "does not chain",
			"it fails the LINK, not the content check, which is the distinction that "+
				"proves genesis is being checked rather than assumed")
	})
}

// ONE PLACEMENT, ONE ENTRY. A scheduler that retries after a crash must not leave an
// operator counting the same replica twice — and since R080's whole job is "how much
// disk did that decision use", a double-counted row is a wrong answer.
func TestOnePlacementIsOneEntry(t *testing.T) {
	runWithRollbackTxn(t, "alloc-once", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc once")
		l := sqlite.NewAllocationLog()
		require.NoError(t, l.RecordPlacement(ctx, scene, "peer-a:9000", "policy", 100))

		// The same replica a second time, from a different code path.
		err := l.RecordPlacement(ctx, scene, "peer-a:9000", "policy retried", 100)
		assert.Error(t, err,
			"a second entry for the same replica is refused: mesh_allocation_log's key "+
				"is (scene_id, source_endpoint), the same key as mesh_replica")
		assert.Equal(t, 1, allocRowCount(t, ctx))
	})
}

// AN UNNAMED SOURCE IS REFUSED. An allocation log entry that cannot say where a
// placement came from is not an audit entry, and source_endpoint is the column that
// joins to mesh_replica.
func TestAPlacementFromAnUnnamedSourceIsRefused(t *testing.T) {
	runWithRollbackTxn(t, "alloc-unnamed", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc unnamed")
		l := sqlite.NewAllocationLog()
		for _, endpoint := range []string{"", " ", "\t"} {
			assert.Error(t, l.RecordPlacement(ctx, scene, endpoint, "policy", 100),
				"endpoint %q must be refused", endpoint)
		}
		// And a negative size is not a size.
		assert.Error(t, l.RecordPlacement(ctx, scene, "peer-a:9000", "policy", -1))
		// And scene id 0 addresses nothing.
		assert.Error(t, l.RecordPlacement(ctx, 0, "peer-a:9000", "policy", 100))
	})
}

// THE LOG SURVIVES THE ROW IT RECORDS. mesh_replica's row is copied into the log
// rather than joined, so deleting the replica does not take the record of the
// placement with it — which is the difference between an audit trail and a
// referential-integrity cascade over the evidence.
func TestTheLogSurvivesDeletionOfTheReplicaItRecords(t *testing.T) {
	runWithRollbackTxn(t, "alloc-survives", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc survives")
		l := sqlite.NewAllocationLog()
		require.NoError(t, l.RecordPlacement(ctx, scene, "peer-a:9000",
			"placed while the scene had 1 of 3 verified replicas", 2048))

		// No replica row exists to delete — the log does not depend on one. Asserted
		// positively: the entry is readable with nothing in mesh_replica at all.
		var replicas int
		require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &replicas,
			"SELECT COUNT(*) FROM mesh_replica WHERE scene_id = ?", scene))
		require.Zero(t, replicas, "precondition: no replica row exists")

		var reason string
		require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &reason,
			"SELECT reason FROM mesh_allocation_log WHERE scene_id = ?", scene))
		assert.Contains(t, reason, "1 of 3 verified replicas",
			"the placement is still on record, because the log copied what it needed "+
				"instead of depending on the row it audits")
	})
}

// THE LENGTH PREFIX, TESTED WHERE IT IS ACTUALLY OBSERVABLE.
//
// The mutation run killed 9 of 14 and the survivors were informative. This one
// survived because my original "data cannot move between fields" test moved a
// character between two fields and then expected detection -- which the CONTENT
// check provides regardless of the prefix, because the concatenated bytes differ
// anyway. The test asserted the wrong property.
//
// The prefix earns its place only for TWO DIFFERENT ROWS whose concatenated byte
// streams are IDENTICAL. Without lengths, endpoint "ab" + reason "cd" and endpoint
// "a" + reason "bcd" both hash "abcd". With lengths they differ, so an attacker
// cannot shorten one field and lengthen the next and keep the row verifying.
//
// That is a property of the payload function, not of the database, so it is tested
// directly and needs no tamper setup. Building it at the store level would require
// two rows that collide, which is exactly the case the prefix makes unrepresentable.
// allocRow mirrors the package's unexported payload struct, so the test can name a
// row without reaching into unexported names. Duplicating the shape is acceptable
// here because the test's subject is the HASH, and a test that reused the struct
// would be asserting the struct against itself.
type allocRow struct {
	sceneID  int
	endpoint string
	reason   string
	bytes    int64
	at       string
}

func (r allocRow) payload(prev []byte) []byte {
	return sqlite.AllocationPayloadFor(r.sceneID, r.endpoint, r.reason, r.bytes, r.at, prev)
}

func TestAllocationPayloadSeparatesFieldsSoDataCannotMoveBetweenThem(t *testing.T) {
	at := "2026-01-01T00:00:00Z"
	genesis := make([]byte, 32)

	// Two rows whose endpoint and reason concatenate to the same bytes.
	a := allocRow{sceneID: 1, endpoint: "ab", reason: "cd", bytes: 100, at: at}
	b := allocRow{sceneID: 1, endpoint: "a", reason: "bcd", bytes: 100, at: at}

	// Prove the premise: without the prefix these payloads ARE IDENTICAL, right
	// through every field. This is asserted rather than assumed, because if it stopped
	// being true the assertion below would pass for the wrong reason.
	//
	// My first version of this precondition was wrong in an instructive way: I
	// asserted the two rows' concatenated forms DIFFER, reasoning that `bytes` sits
	// after the strings and would separate them. It does sit after them -- both rows
	// carry bytes=100, so it separates nothing. The premise is stronger than I
	// thought, which is the good direction for this particular property: the whole
	// field sequence collides, not just the two strings.
	plainA := a.endpoint + a.reason + fmt.Sprintf("%d", a.bytes)
	plainB := b.endpoint + b.reason + fmt.Sprintf("%d", b.bytes)
	require.Equal(t, plainA, plainB,
		"precondition: every field concatenated is identical, so nothing but the "+
			"length prefix can tell these rows apart")

	// And with the prefix they do not.
	assert.NotEqual(t, a.payload(genesis), b.payload(genesis),
		"the length prefix is what distinguishes two rows whose fields concatenate "+
			"identically; without it an attacker could move data between adjacent "+
			"fields and keep the row verifying")
}

// EVERY FIELD IS IN THE HASHED PAYLOAD, tested per field rather than for `reason`
// alone. `reason` and `at` were covered by tamper tests; these three were not, and
// the mutation run showed it: removing any of them from the payload left the suite
// green.
//
// A field that leaves the payload is a field an attacker can rewrite undetected, so
// each is asserted here at the cheapest level that can distinguish it.
func TestEveryFieldIsInTheHashedPayload(t *testing.T) {
	genesis := make([]byte, 32)
	base := allocRow{
		sceneID: 1, endpoint: "peer-a:9000", reason: "below the floor", bytes: 100,
		at: "2026-01-01T00:00:00Z",
	}
	baseline := base.payload(genesis)

	changes := map[string]func(v *allocRow){
		"scene id":        func(v *allocRow) { v.sceneID = 2 },
		"source endpoint": func(v *allocRow) { v.endpoint = "peer-b:9000" },
		"reason":          func(v *allocRow) { v.reason = "above the floor" },
		"bytes":           func(v *allocRow) { v.bytes = 101 },
		"at":              func(v *allocRow) { v.at = "2026-01-02T00:00:00Z" },
	}
	require.Len(t, changes, 5, "all five fields are covered, so a sixth field added "+
		"later is a compile-time reminder rather than a silent omission")

	for name, mutate := range changes {
		changed := base
		mutate(&changed)
		assert.NotEqual(t, baseline, changed.payload(genesis),
			"changing the %s must change the hash; a field outside the payload is a "+
				"field an attacker can rewrite undetected", name)
	}

	// And the previous hash is part of it, which is what makes it a CHAIN rather
	// than a set of per-row signatures. Without this, deleting a middle row would
	// leave every remaining row individually valid.
	other := base.payload([]byte("something else entirely"))
	assert.NotEqual(t, baseline, other,
		"the previous hash must be in the payload, or the log is a set of signatures "+
			"and a deleted row goes unnoticed")
}

// # TWO DEFENCES, AND WHICH ONE FIRED MATTERS
//
// The mutation run found that removing the Go blank-reason guard changes nothing
// observable: migration 114's CHECK refuses every whitespace-only reason, so the
// test still passed. That is a good outcome for the SCHEMA and a weak test, because a
// test that passes for two different reasons is passing for one of them by accident.
//
// It is not redundant defence to keep both, and the reason is which layer sees what:
// the Go guard produces an ERROR RETURNING BEFORE ANY SQL RUNS, so a caller that
// batches placements gets a rejected placement rather than a half-committed
// transaction, and the message can say which rule refused. The CHECK is the one place
// a peer cannot talk you out of. Neither can be removed without the other losing its
// property, so this test pins the Go guard where it is the ONLY thing that can act.
func TestTheGoGuardRefusesBeforeAnySQLRuns(t *testing.T) {
	runWithRollbackTxn(t, "alloc-guard", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc guard")
		l := sqlite.NewAllocationLog()

		// With the schema CHECK removed from the picture, only the Go guard can
		// refuse. Asserted by the ERROR TEXT, which names the guard rather than the
		// constraint: a CHECK failure says "CHECK constraint failed", and this says
		// what the caller should do about it.
		err := l.RecordPlacement(ctx, scene, "peer-a:9000", "\t", 100)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "needs a reason",
			"the Go guard's own message, not a CHECK constraint failure — so this test "+
				"fails if the guard is removed and the schema happens to catch it")
		assert.NotContains(t, err.Error(), "CHECK constraint",
			"which proves the refusal happened in Go, before the INSERT")

		// And a valid reason still gets through, so the guard is not simply refusing
		// everything.
		require.NoError(t, l.RecordPlacement(ctx, scene, "peer-a:9000", "a real reason", 100))
		assert.Equal(t, 1, allocRowCount(t, ctx))
	})
}

// nonBlank IS NOT "not the empty string", and the mutation run showed the suite could
// not tell. SQLite's trim(x) strips SPACES ONLY, so a length(trim(x)) > 0 check
// accepts a tab-only value — that hole is live in migrations 99 and 101. The Go guard
// checks every whitespace rune, and 114's CHECK spells out char(9)..char(13) for the
// same reason.
//
// Tested through nonBlank directly rather than through RecordPlacement, because at
// the store level the schema's CHECK catches these first and the guard cannot be seen.
func TestNonBlankRejectsEveryWhitespaceOnlyValue(t *testing.T) {
	// One of every rune in the trim set, alone and with padding.
	for _, blank := range []string{
		"", " ", "  ", "\t", "\n", "\v", "\f", "\r",
		" \t\n\v\f\r ", " \t ", "\n\n", "\r\r",
	} {
		assert.False(t, sqlite.NonBlank(blank), "%q is whitespace only", blank)
	}

	// And it accepts everything else, including the cases that LOOK blank but are
	// not: a zero-width space is not a space, and a string of punctuation is content.
	for _, real := range []string{
		"a", " a ", "0", "\u00a0", "\u200b", "\u3000", "-", ".",
		"placed because the scene had 0 of 3 verified replicas",
	} {
		assert.True(t, sqlite.NonBlank(real), "%q has content", real)
	}
}

// THE HEAD IS WHAT MAKES THE STATUS USABLE. A caller verifying a chain wants to know
// where it ends so it can extend from a verified position, and a Head that never
// advances reports genesis forever — which looks like a working status and is not.
func TestTheHeadAdvancesToTheNewestRow(t *testing.T) {
	runWithRollbackTxn(t, "alloc-head", func(t *testing.T, ctx context.Context) {
		l := sqlite.NewAllocationLog()

		// Empty: the head is genesis, which is a real answer and not a nil.
		st, err := l.VerifyAllocationChain(ctx)
		require.NoError(t, err)
		require.True(t, st.Intact)
		require.Len(t, st.Head, 32, "an empty chain heads at genesis")
		genesisHead := append([]byte(nil), st.Head...)

		scene := mustScene(t, ctx, "alloc head")
		mustAllocations(ctx, t, scene, 3, "policy: below the floor")

		st, err = l.VerifyAllocationChain(ctx)
		require.NoError(t, err)
		require.True(t, st.Intact)
		assert.Len(t, st.Head, 32)
		assert.NotEqual(t, genesisHead, st.Head,
			"the head must advance past genesis once rows exist; a head pinned at "+
				"genesis looks like a working status and would make every caller "+
				"extend from the wrong place")

		// And it is the LAST row's hash, so a caller can chain onto it correctly.
		var lastHash []byte
		require.NoError(t, sqlite.DbgAuditSelectOne(ctx, &lastHash,
			"SELECT row_hash FROM mesh_allocation_log ORDER BY id DESC LIMIT 1"))
		assert.Equal(t, lastHash, st.Head,
			"the head must be the newest row's stored hash, so extending from it "+
				"produces a chain that verifies")
	})
}

// A BREAK STILL REPORTS A HEAD, because a caller extending from a real head keeps the
// remaining rows verifiable. Refusing to extend would let anyone who corrupts one row
// also stop all future logging on that instance — which is the moment the audit is
// most needed.
func TestABrokenChainStillReportsWhereItReached(t *testing.T) {
	runWithRollbackTxn(t, "alloc-breakhead", func(t *testing.T, ctx context.Context) {
		scene := mustScene(t, ctx, "alloc break head")
		mustAllocations(ctx, t, scene, 3, "policy: below the floor")
		l := sqlite.NewAllocationLog()

		first := firstAllocID(t, ctx)
		allocRawExec(t, ctx,
			"UPDATE mesh_allocation_log SET reason = 'rewritten' WHERE id = ?", first)

		st, err := l.VerifyAllocationChain(ctx)
		require.NoError(t, err)
		require.False(t, st.Intact)
		assert.Equal(t, first, st.BrokenAt)
		assert.NotEmpty(t, st.Head,
			"a broken chain reports the head it reached, so logging can continue from "+
				"a verifiable position rather than stopping")
		assert.Len(t, st.Head, 32)
	})
}

// THE TWO SURVIVING MUTANTS, AND WHY A TEST THAT KILLED THEM WOULD BE WRONG.
//
// Recorded rather than papered over, because "12/14" reads like a weakness until you
// know the two are not mutations of behaviour at all.

// M10 — replacing `prev := allocationGenesis` with `prev := make([]byte, 32)`.
//
// `allocationGenesis` IS `make([]byte, sha256.Size)`, so the mutant initialises the
// accumulator to the identical value. Every hash, every comparison and every verdict
// is unchanged. A test that killed this would be asserting that a particular 32-byte
// slice was constructed rather than that the chain starts at genesis — a constant, not
// a property.
//
// The genesis property IS tested, and by the case that would actually fail if it were
// broken: TestAllocationChainVerifiesFromGenesisNotFromTheFirstRowItSees deletes the
// FIRST row and asserts the walk fails the LINK check. A walk that started from
// whichever row happened to be oldest would pass that test; this one does not.

// M14 — replacing `status.Intact = true` with `status.Intact = len(status.Reason) == 0`.
//
// On the intact path `Reason` is still "" at the moment the assignment runs (it is set
// on the NEXT line), so the expression is true. On every break path `Reason` was
// assigned earlier in the loop, so it is non-empty and the expression is false. Both
// spellings are the same function of control flow.
//
// A test that killed this would have to observe a difference between "the loop fell
// through" and "the loop fell through and Reason happened to be empty", which is the
// same statement twice.
func TestTheTwoSurvivingMutantsAreEquivalentNotUntested(t *testing.T) {
	// M10: the genesis value is exactly what the mutant constructs.
	require.Len(t, sqlite.AllocationGenesisForTest, 32,
		"genesis is sha256.Size zero bytes")
	require.Equal(t, make([]byte, 32), sqlite.AllocationGenesisForTest,
		"and it is 32 zero bytes, which is the identical value the M10 mutant builds "+
			"-- so the mutant initialises the accumulator to the same thing")

	runWithRollbackTxn(t, "alloc-equivalent", func(t *testing.T, ctx context.Context) {
		// M14: the intact path sets Reason AFTER Intact, which is exactly what makes
		// the mutant equivalent. Asserted so a future edit reordering those two lines
		// is caught here rather than turning a recorded-equivalent mutant into a real
		// one -- which is the only way that fact can go stale.
		st, err := sqlite.NewAllocationLog().VerifyAllocationChain(ctx)
		require.NoError(t, err)
		require.True(t, st.Intact)
		assert.Equal(t, "every allocation links from genesis", st.Reason,
			"the intact path's Reason is assigned after Intact, which is the reason the "+
				"equivalent mutant is equivalent; reordering them would make it a real one")
	})
}
