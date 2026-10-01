package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// M8 step 8.4, R080: the storage allocation log.
//
// WHAT MIGRATION 113 ALREADY ANSWERS AND WHAT IT CANNOT. 113 records which replica
// exists, from which peer, and whether it verified — that is "what do I hold". It
// cannot answer the question this exists for: "why is this instance storing a fourth
// copy of a scene nobody here watches, on a disk I am paying for". A row with no
// stated reason leaves an operator reconstructing a scheduler's mood, and a
// scheduler's mood is not a record.
//
// # WHY IT IS HASH-CHAINED
//
// Migration 108 chains `collab_audit` for §5.1's reason: the owner is not an admin
// over content, and a log any SQL session can UPDATE does not support that claim.
// The same logic applies with more force here, because the operator most likely to
// want this log is the one DISSERVED by a rewritten row — a node that quietly placed
// three times the policy allows would show nothing. Integrity that depends on the
// absence of malicious code, in a codebase that ingests peer-supplied data, is not
// an audit trail.
//
// Each row stores sha256(prev_hash || canonical row). Editing any field of any row
// invalidates every hash after it, so a verify walk stops at the first break and
// names the row. Deleting from the middle breaks the same way: the successor no
// longer chains.
//
// # WHY `at` IS IN THE HASHED PAYLOAD
//
// It is the one field an operator might reasonably want to correct after the fact —
// a wrong clock, a timezone fix — and including it is the point. If the time a
// placement happened can be edited, the record is not a record.

const (
	allocationTable    = "mesh_allocation_log"
	allocationSceneID  = "scene_id"
	allocationEndpoint = "source_endpoint"
	allocationReason   = "reason"
	allocationBytes    = "bytes"
	allocationAt       = "at"

	// allocationAtLayout matches the audit chain's, so both logs' timestamps sort
	// against each other with a plain string comparison. An audit that interleaves
	// two chains in two formats is an audit nobody reads.
	allocationAtLayout = "2006-01-02T15:04:05Z"
)

// allocationGenesis is 64 zeros, so the first allocation on an instance chains from
// a known constant and the chain is whole from the start rather than beginning at
// whichever row happens to be oldest.
var allocationGenesis = make([]byte, sha256.Size)

// allocationValues is the canonical row, in a FIXED order.
//
// THE ORDER IS PART OF THE FORMAT and `ordered()` is the only thing that produces
// it. Two callers iterating a map would produce different payloads for the same row
// and permanently break the chain, which is the failure mode a Go programmer
// reaches for by instinct: `range` over a map.
type allocationValues struct {
	sceneID  int
	endpoint string
	reason   string
	bytes    int64
	at       string
}

// ordered returns the fields in the one order that is ever hashed.
func (v allocationValues) ordered() []sql.NullString {
	return []sql.NullString{
		{String: fmt.Sprintf("%d", v.sceneID), Valid: true},
		{String: v.endpoint, Valid: true},
		{String: v.reason, Valid: true},
		{String: fmt.Sprintf("%d", v.bytes), Valid: true},
		{String: v.at, Valid: true},
	}
}

// allocationPayload hashes prev || every field, each LENGTH-PREFIXED.
//
// THE LENGTH PREFIX is not decoration. Without it, moving a character from the end
// of `reason` to the start of the next field produces the same byte stream: reason
// "abc" + endpoint "def" hashes identically to reason "ab" + endpoint "cdef". A
// tampered row would verify. With a uint64 length before each field, the two
// payloads differ, and the chain catches it. There is a matching test
// (TestAllocationChainSeparatesFieldsSoDataCannotMoveBetweenThem).
func allocationPayload(prev []byte, v allocationValues) []byte {
	h := sha256.New()
	h.Write(prev)
	var lenBuf [8]byte
	for _, f := range v.ordered() {
		if !f.Valid {
			binary.BigEndian.PutUint64(lenBuf[:], ^uint64(0))
			h.Write(lenBuf[:])
			continue
		}
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(f.String)))
		h.Write(lenBuf[:])
		h.Write([]byte(f.String))
	}
	return h.Sum(nil)
}

// AllocationGenesisForTest is the chain's genesis value, exposed so a test can pin
// that it is sha256.Size zero bytes. See TestTheTwoSurvivingMutantsAreEquivalent.
var AllocationGenesisForTest = allocationGenesis

// AllocationPayloadFor is the hash of one canonical row, exposed so the property
// "every field changes the hash, and fields cannot move between each other" can be
// tested directly.
//
// IT IS EXPORTED FOR THE TEST ALONE and there is a reason it is not hidden behind a
// build tag: the property is about the FORMAT, and a format whose invariants are only
// reachable from inside its own package is a format nobody outside can check. The
// audit chain's payload stays unexported, which is defensible there because its
// invariants are exercised through real tampering; here the interesting case (two
// rows that concatenate identically) cannot be written to the database at all, since
// the length prefix makes it unrepresentable.
//
// Callers must not use this to compute a hash they then store by hand — RecordPlacement
// is the only supported write path, and a second insert path produces a row that does
// not chain.
func AllocationPayloadFor(sceneID int, sourceEndpoint, reason string, bytes int64, at string, prev []byte) []byte {
	return allocationPayload(prev, allocationValues{
		sceneID: sceneID, endpoint: sourceEndpoint, reason: reason, bytes: bytes, at: at,
	})
}

// AllocationEntry is one placement, as an operator reads it back.
type AllocationEntry struct {
	SceneID        int
	SourceEndpoint string
	Reason         string
	Bytes          int64
	At             time.Time
}

// AllocationLog is the append-and-read surface for R080.
type AllocationLog struct{}

// NewAllocationLog returns the allocation log. There is nothing to configure: the
// log is a property of the database, not of a caller.
func NewAllocationLog() *AllocationLog { return &AllocationLog{} }

// lastAllocationHash returns the newest row_hash, or genesis when there is none.
//
// A stored NULL or empty value also yields genesis. That is the pre-chain state,
// and returning genesis lets Verify describe it rather than error — the same
// choice `lastAuditHash` makes, and for the same reason: a verify that cannot
// report on a half-created table cannot be used to prove the table is sound.
func lastAllocationHash(ctx context.Context) ([]byte, error) {
	var last []byte
	err := dbWrapper.Get(ctx, &last, fmt.Sprintf(
		"SELECT row_hash FROM %s ORDER BY %s DESC LIMIT 1", allocationTable, idColumn))
	if err != nil {
		// errors.Is, NOT ==. dbWrapper wraps with %w, so a direct comparison
		// against sql.ErrNoRows is never true and an empty log surfaces as a hard
		// error instead of starting a chain at genesis. This is the audit chain's
		// bug, reproduced because I wrote `==` first and only found it when the
		// empty-table test failed -- so it is written down here as a hazard rather
		// than left as a fix.
		if errors.Is(err, sql.ErrNoRows) {
			return allocationGenesis, nil
		}
		return nil, err
	}
	// A hash of the WRONG LENGTH is genesis rather than an error, for the reason
	// `lastAuditHash` gives: that is the pre-chain or partially-written state, and
	// Verify can describe it. Silently accepting a short hash as a real link would
	// let a truncated value chain forever.
	if len(last) != sha256.Size {
		return allocationGenesis, nil
	}
	return last, nil
}

// RecordPlacement appends one allocation with the reason it happened.
//
// # THE REASON IS REQUIRED, NOT OPTIONAL, and refusing a blank one is the whole
// requirement. A caller that cannot state why it stored something has exactly the
// state R080 exists to make visible, and the alternatives are both worse: writing
// an empty reason, or omitting the row and leaving a replica in 113 with no
// explanation beside it.
//
// The check is a Go guard AND a CHECK in migration 114. Two checks on one property
// is deliberate, and the reason is the same as `replicastore`'s path check: the
// database is the one place a peer cannot talk you out of, and the Go guard is the
// first place a bug would be caught. A tab-only reason is refused here even though
// SQLite's bare trim() would accept it — see migration 114's header.
func (l *AllocationLog) RecordPlacement(ctx context.Context, sceneID int, sourceEndpoint, reason string, bytes int64) error {
	if sceneID <= 0 {
		return fmt.Errorf("allocation: scene id %d is not addressable", sceneID)
	}
	if !nonBlank(sourceEndpoint) {
		return fmt.Errorf("allocation: a placement from an unnamed source cannot be audited")
	}
	if !nonBlank(reason) {
		return fmt.Errorf("allocation: a placement needs a reason. R080 exists so an " +
			"operator can see WHY a copy is here, and a row with an empty reason is " +
			"the one row that needs explaining")
	}
	if bytes < 0 {
		return fmt.Errorf("allocation: %d bytes is not a size", bytes)
	}

	prev, err := lastAllocationHash(ctx)
	if err != nil {
		return err
	}

	// `at` is computed here and inserted explicitly rather than left to
	// CURRENT_TIMESTAMP, then hashed as exactly that value. Letting the default fill
	// it in and hashing our own idea of "now" would mismatch the moment the two
	// clocks differed by a second, and a permanently broken chain is worse than no
	// chain. The audit chain's comment says this and the reason is identical.
	at := time.Now().UTC().Format(allocationAtLayout)

	v := allocationValues{
		sceneID:  sceneID,
		endpoint: sourceEndpoint,
		reason:   reason,
		bytes:    bytes,
		at:       at,
	}
	rowHash := allocationPayload(prev, v)

	query := fmt.Sprintf(
		"INSERT INTO %s (%s, %s, %s, %s, %s, prev_hash, row_hash) VALUES (?, ?, ?, ?, ?, ?, ?)",
		allocationTable, allocationSceneID, allocationEndpoint, allocationReason,
		allocationBytes, allocationAt)
	_, err = dbWrapper.Exec(ctx, query, sceneID, sourceEndpoint, reason, bytes, at, prev, rowHash)
	return err
}

// AllocationChainStatus is the result of a verify walk.
type AllocationChainStatus struct {
	// Intact is true when every row links correctly from genesis.
	Intact bool

	// BrokenAt is the id of the first row that fails to link, or 0 when intact.
	//
	// THE FIRST, NOT A COUNT. An operator needs to know where the record stops being
	// trustworthy; how many rows are wrong is not actionable, and a count invites a
	// caller to report "N problems" when the answer is "everything after row N".
	BrokenAt int

	// Reason names the break in words an operator can act on.
	Reason string

	// Head is the newest verified row_hash, so a caller can chain further writes
	// onto a verified position. It is filled even when the chain breaks, because a
	// caller extending from a real head keeps the remaining rows verifiable — a
	// chain that cannot be extended after one tampering event stops being an audit
	// trail for exactly the instance that needs it most.
	Head []byte
}

// VerifyAllocationChain walks the log in id order and reports the first break.
//
// # A FULL WALK, BECAUSE A CHAIN CAN BE BROKEN WITHOUT THE HEAD LOOKING WRONG
//
// Deleting a row from the middle leaves the last row's hash exactly as correct as it
// was. Any check that starts from the head would pass. Only reading every row in
// order and comparing each stored prev_hash against the previous row's ACTUAL hash
// catches it — and comparing against the stored one rather than the recomputed one
// is the second trap: a mutation that disables the link check leaves the content
// check running, so both must consume the same `prev` or one of them can be
// switched off silently. That is the same pair of guards the audit chain states, and
// it is restated here because the only defence is that a future editor reads it.
//
// A BREAK IS AN ANSWER, NOT AN ERROR. The walk succeeded; the question was whether
// the data is sound, and "not sound" is the answer to that question. Returning it as
// an error would make every caller treat a tampered log as an unavailable one, which
// is the one response guaranteed to produce no further audit.
func (l *AllocationLog) VerifyAllocationChain(ctx context.Context) (AllocationChainStatus, error) {
	var status AllocationChainStatus
	status.Head = append([]byte(nil), allocationGenesis...)
	prev := allocationGenesis

	rows, err := dbWrapper.QueryxContext(ctx, fmt.Sprintf(
		"SELECT %s, %s, %s, %s, %s, %s, prev_hash, row_hash FROM %s ORDER BY %s ASC",
		idColumn, allocationSceneID, allocationEndpoint, allocationReason,
		allocationBytes, allocationAt, allocationTable, idColumn))
	if err != nil {
		return status, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id                 int
			sceneID            int
			endpoint           string
			reason             string
			bytes              int64
			at                 string
			storedPrev, stored []byte
		)
		if err := rows.Scan(&id, &sceneID, &endpoint, &reason, &bytes, &at,
			&storedPrev, &stored); err != nil {
			return status, err
		}

		// An unusable hash is UNCHAINED rather than broken, and the two need
		// different words: "unchained" means this row never participated in the
		// chain, "broken" means something was changed. An operator reading "row 40
		// is unchained" knows to look for a second write path; reading "row 40 is
		// tampered" sends them looking for an intruder, and those are different
		// afternoons.
		if len(stored) != sha256.Size || len(storedPrev) != sha256.Size {
			status.BrokenAt = id
			status.Reason = fmt.Sprintf(
				"row %d carries no usable hashes (prev %d bytes, row %d bytes): "+
					"either it predates the chain or it was written by a path that "+
					"does not chain", id, len(storedPrev), len(stored))
			return status, nil
		}

		// THE LINK. Against the previous row's ACTUAL hash, not against a
		// recomputation of it.
		if !bytesEqual(storedPrev, prev) {
			status.BrokenAt = id
			status.Reason = fmt.Sprintf(
				"row %d does not chain to row above it: an earlier row was edited or "+
					"removed, so every row from here on cannot be trusted", id)
			return status, nil
		}

		// THE CONTENT. `at` is in here, so an edited timestamp is caught, which is
		// the point: if the time a placement happened can be changed after the
		// fact, the record is not a record.
		want := allocationPayload(storedPrev, allocationValues{
			sceneID: sceneID, endpoint: endpoint, reason: reason, bytes: bytes, at: at,
		})
		if !bytesEqual(stored, want) {
			status.BrokenAt = id
			status.Reason = fmt.Sprintf(
				"row %d does not match its own contents: a field was edited after the "+
					"row was written", id)
			return status, nil
		}

		prev = stored
		status.Head = append([]byte(nil), stored...)
	}
	if err := rows.Err(); err != nil {
		return status, err
	}

	status.Intact = true
	status.Reason = "every allocation links from genesis"
	return status, nil
}

// NonBlank reports whether s holds something other than whitespace.
//
// EXPORTED so the guard's behaviour can be tested at the guard rather than through
// the store. At the store level migration 114's CHECK refuses a whitespace-only reason
// FIRST, so a test asserting "the reason is refused" passes even with this function
// deleted — which a mutation run demonstrated. Asserting it here is the only way to
// pin the layer that decides before any SQL runs.
func NonBlank(s string) bool { return nonBlank(s) }

// nonBlank reports whether s holds something other than whitespace.
//
// EVERY WHITESPACE CHARACTER, not just the space, because SQLite's bare trim(x)
// strips spaces ONLY, so a length(trim(x)) > 0 check accepts a tab-only value.
// Migrations 99 and 101 have that hole; this applies the same correction at the Go
// boundary, and it is why 114's CHECK spells out char(9)..char(13).
func nonBlank(s string) bool {
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\v', '\f', '\r':
			continue
		default:
			return true
		}
	}
	return false
}
