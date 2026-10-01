package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"gopkg.in/guregu/null.v4"
)

// Hash-chaining for collab_audit.
//
// The AuditStore exposes no update or delete, and its own comment says that
// absence is the feature. It is not sufficient: anybody holding the sqlite file
// and a query runner has UPDATE, and the schema has no reason to stop them.
// §5.1 claims the owner is not an admin over content, and that claim needs the
// audit log to be tamper-evident rather than merely append-only by convention.
//
// Each row stores sha256(prev_hash || canonical row). Changing any field of any
// row changes that row's hash, which invalidates every hash after it, so a
// verify walk stops at the first break and names the row. Deleting a row from
// the middle breaks the same way: its successor no longer chains.

// auditGenesis is the prev_hash of the very first audit row: 32 zero bytes.
// A chain that starts from a known constant can be verified from row 1, which
// is what distinguishes "nothing has been removed" from "everything before
// this point was replaced wholesale".
var auditGenesis = make([]byte, sha256.Size)

// auditAtLayout is how the mattn/go-sqlite3 driver stores a `datetime` column,
// and is therefore part of the hashed bytes.
//
// NOT the same as the layout I write. The driver rewrites "2006-01-02
// 15:04:05" into RFC3339 with a Z -- measured, not assumed: inserting the
// layout below stores "2026-10-01T09:30:00Z", 20 bytes. Hashing the string I
// passed in rather than the string that lands in the row made every row fail
// its own verify on the very first write.
//
// The consequence for a fixed-width field: "2026-10-01T09:30:00Z" is always 20
// bytes for a UTC timestamp, which is the only kind written here, so the
// length-prefix in auditChainPayload stays unambiguous.
const auditAtLayout = "2006-01-02T15:04:05Z"

// auditChainValues is one row as it participates in the hash. The order is the
// hash's order: it is fixed here rather than derived from a map iteration or a
// SELECT *, because reordering it without a migration would invalidate every
// stored hash.
type auditChainValues struct {
	actorID   sql.NullInt64
	action    sql.NullString
	targetTyp sql.NullString
	targetID  sql.NullInt64
	field     sql.NullString
	detail    sql.NullString
	at        sql.NullString
}

func (v auditChainValues) ordered() []sql.NullString {
	return []sql.NullString{
		nullStr(v.actorID), v.action, v.targetTyp, nullStr(v.targetID),
		v.field, v.detail, v.at,
	}
}

// nullStr renders a nullable integer as a nullable string for hashing, so the
// whole payload can be one loop. 7 and "7" hash the same, which is fine: they
// are the same value and the column is typed INTEGER anyway.
func nullStr(n sql.NullInt64) sql.NullString {
	if !n.Valid {
		return sql.NullString{}
	}
	return sql.NullString{String: fmt.Sprintf("%d", n.Int64), Valid: true}
}

func auditInt(n *int) sql.NullInt64 {
	if n == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*n), Valid: true}
}

// auditStr converts the guregu null.String the rest of this file uses into the
// database/sql one. guregu's zero String is Invalid, so "" and NULL are the same
// value here -- which matches how Append has always written these columns.
func auditStr(s null.String) sql.NullString {
	if !s.Valid {
		return sql.NullString{}
	}
	return sql.NullString{String: s.String, Valid: true}
}

// auditChainPayload builds the canonical bytes that get hashed: prev_hash,
// then each field as an 8-byte big-endian length followed by its bytes.
//
// Length-prefixed rather than separator-joined. A separator is ambiguous: with
// "|", the row (action="a|b", field="c") and the row (action="a", field="b|c")
// produce identical bytes, so data could move between columns without changing
// the hash. NULL is encoded as a length with the high bit set, so it can never
// collide with a real length and is distinguishable from the empty string.
func auditChainPayload(prev []byte, v auditChainValues) []byte {
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

// lastAuditHash returns the row_hash of the newest audit row, or auditGenesis
// when there are none. A stored NULL or empty value also yields genesis: that is
// the pre-chain database, and treating it as genesis keeps VerifyChain able to
// describe the state instead of erroring on it.
func lastAuditHash(ctx context.Context) ([]byte, error) {
	var last []byte
	err := dbWrapper.Get(ctx, &last, fmt.Sprintf(
		"SELECT row_hash FROM %s ORDER BY %s DESC LIMIT 1", collabAuditTable, idColumn))
	if err != nil {
		// errors.Is, not ==. dbWrapper wraps with %w, so a direct comparison
		// against sql.ErrNoRows is never true and an empty table surfaces as a
		// hard error instead of starting a chain at genesis.
		if errors.Is(err, sql.ErrNoRows) {
			return auditGenesis, nil
		}
		return nil, err
	}
	if len(last) != sha256.Size {
		return auditGenesis, nil
	}
	return last, nil
}

// appendAuditChained is the single write path for collab_audit. Append and
// RecordLoginFailure both route through it, and that is the point: a second
// insert path produces a row that does not chain, and one unchained row breaks
// verification for every row after it.
//
// Reading the head and writing the successor happen in the caller's transaction,
// so two concurrent appends serialise on SQLite's write lock instead of both
// reading the same head and forking the chain.
func appendAuditChained(ctx context.Context, actorID *int, action, targetType string, targetID *int, field string, detail string) error {
	prev, err := lastAuditHash(ctx)
	if err != nil {
		return err
	}

	// `at` defaults to CURRENT_TIMESTAMP, so the timestamp is the database's to
	// choose. Compute it here, insert it explicitly, and hash exactly that: let
	// the default fill it in and hashing our own idea of "now" would mismatch the
	// moment the two clocks disagreed by a second -- and a permanently broken
	// chain is worse than no chain.
	at := time.Now().UTC().Format(auditAtLayout)

	v := auditChainValues{
		actorID:   auditInt(actorID),
		action:    sql.NullString{String: action, Valid: true},
		targetTyp: auditStr(null.StringFrom(targetType)),
		targetID:  auditInt(targetID),
		field:     auditStr(null.StringFrom(field)),
		detail:    sql.NullString{String: detail, Valid: true},
		at:        sql.NullString{String: at, Valid: true},
	}
	hash := auditChainPayload(prev, v)

	query := fmt.Sprintf(
		"INSERT INTO %s (%s, %s, %s, %s, %s, %s, at, prev_hash, row_hash) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		collabAuditTable, collabAuditActorCol, collabAuditActionCol,
		collabAuditTargetTypeCol, collabAuditTargetIDCol, collabAuditFieldCol,
		collabAuditDetailCol)

	_, err = dbWrapper.Exec(ctx, query,
		intOrNull(actorID), action, null.StringFrom(targetType),
		intOrNull(targetID), null.StringFrom(field), detail,
		at, prev, hash)
	return err
}

// ChainStatus is the result of a verify walk.
type ChainStatus struct {
	// Rows is the id of the last row walked, so an empty table reports 0.
	Rows int
	// Head is the final row_hash: what the next append chains from, and what a
	// peer compares against.
	Head []byte
	// BrokenAt is the id of the first row that does not verify, or 0 when the
	// chain is intact. Nothing from here on can be trusted.
	BrokenAt int
	// Reason explains the break. Empty when BrokenAt is 0.
	Reason string
}

// Intact reports whether the walk found no break.
func (c ChainStatus) Intact() bool { return c.BrokenAt == 0 }

// VerifyChain recomputes every hash in collab_audit, in id order, and stops at
// the first mismatch.
//
// It stops rather than continuing because each hash depends on its predecessor:
// once one row is wrong, every later mismatch is the same single cause reported
// repeatedly, and a list of forty "failures" is less useful than one row id.
//
// Deliberately a full scan. A verify that checks only the last N rows cannot
// detect a deletion near the head, which is the deletion an attacker would
// actually make.
func (s *AuditStore) VerifyChain(ctx context.Context) (ChainStatus, error) {
	var status ChainStatus
	prev := auditGenesis

	rows, err := dbWrapper.QueryxContext(ctx, fmt.Sprintf(
		"SELECT %s, %s, %s, %s, %s, %s, %s, at, prev_hash, row_hash FROM %s ORDER BY %s ASC",
		idColumn, collabAuditActorCol, collabAuditActionCol, collabAuditTargetTypeCol,
		collabAuditTargetIDCol, collabAuditFieldCol, collabAuditDetailCol,
		collabAuditTable, idColumn))
	if err != nil {
		return status, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id         int
			actorID    sql.NullInt64
			action     sql.NullString
			targetTyp  sql.NullString
			targetID   sql.NullInt64
			field      sql.NullString
			detail     sql.NullString
			at         sql.NullString
			storedPrev []byte
			stored     []byte
		)
		if err := rows.Scan(&id, &actorID, &action, &targetTyp, &targetID,
			&field, &detail, &at, &storedPrev, &stored); err != nil {
			return status, err
		}

		if len(stored) != sha256.Size {
			status.BrokenAt = id
			status.Reason = fmt.Sprintf(
				"row %d carries no usable row_hash (%d bytes): either it predates the "+
					"chain or it was written by a path that does not chain", id, len(stored))
			return status, nil
		}

		// The stored prev_hash is compared against the previous row's ACTUAL
		// hash, not merely recomputed alongside it.
		//
		// Both guards below consume `prev`, which is the point of this one: a
		// mutation that disables the link check leaves the content check running,
		// and because that check also hashes `prev` into the expected value, it
		// still catches a removed row. Measured, not assumed -- I tried disabling
		// this line and the deletion test still passed. So the link check is not
		// load-bearing for deletion, and the honest statement is that the two are
		// redundant for that case and complementary for the others: the link
		// check names the failure precisely ("row N chains from X, predecessor is
		// Y"), which is the difference between a diagnosis and a mismatch.
		if !bytesEqual(storedPrev, prev) {
			status.BrokenAt = id
			status.Reason = fmt.Sprintf(
				"row %d chains from %x but the preceding row's hash is %x: a row was "+
					"removed, reordered, or a link was rewritten", id, storedPrev, prev)
			return status, nil
		}

		v := auditChainValues{actorID: actorID, action: action, targetTyp: targetTyp,
			targetID: targetID, field: field, detail: detail, at: at}
		if want := auditChainPayload(prev, v); !bytesEqual(stored, want) {
			status.BrokenAt = id
			status.Reason = fmt.Sprintf(
				"row %d content does not match its hash: stored %x, recomputed %x",
				id, stored, want)
			return status, nil
		}

		prev = stored
		status.Rows = id
	}
	if err := rows.Err(); err != nil {
		return status, err
	}

	status.Head = prev
	return status, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
