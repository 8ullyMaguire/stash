//go:build integration

package sqlite

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The length-prefixing in auditChainPayload is what makes the field boundaries
// part of what gets hashed. These test that property directly, at the level of
// the payload function, because a test through the database cannot distinguish
// "the boundary is hashed" from "the values happen to differ".
//
// They are package-internal tests for the obvious reason: auditChainPayload is
// unexported, and going through the database would only observe it indirectly.

// auditChainFullRow returns a row with EVERY field populated, so a test can vary
// one field without the others being NULL.
//
// That matters more than it looks. An earlier version of the boundary test left
// five fields NULL, and because the NULL sentinel is itself length-prefixed, the
// trailing boundary bytes masked the ambiguity completely: a mutation that
// stripped the length prefix from every field SURVIVED, because the bytes the
// test was relying on were the NULL sentinels rather than the field lengths. A
// test that appears to cover a property while resting on a different mechanism
// is worse than no test, because it reports coverage it does not have.
func auditChainFullRow(action, targetTyp string) auditChainValues {
	return auditChainValues{
		actorID:   sql.NullInt64{Int64: 1, Valid: true},
		action:    sql.NullString{String: action, Valid: true},
		targetTyp: sql.NullString{String: targetTyp, Valid: true},
		targetID:  sql.NullInt64{Int64: 2, Valid: true},
		field:     sql.NullString{String: "d", Valid: true},
		detail:    sql.NullString{String: "e", Valid: true},
		at:        sql.NullString{String: "2026-10-01T09:30:00Z", Valid: true},
	}
}

func TestAuditChainPayloadSeparatesFieldsFromTheirBoundaries(t *testing.T) {
	gen := make([]byte, 32)

	// The inputs are chosen so that plain CONCATENATION of the two fields gives
	// identical bytes: "ab"+"c" == "a"+"bc" == "abc".
	//
	// The earlier version of this test used ("a|b", "c") against ("a", "b|c"),
	// which looks equivalent and is not: concatenated they are "a|bc" and
	// "ab|c". So the mutation that strips the lengths SURVIVED, and the test was
	// reporting a boundary guarantee it never actually exercised. Inputs have to
	// be chosen for the collision they are meant to be able to produce.
	splitLeft := auditChainPayload(gen, auditChainFullRow("ab", "c"))
	splitRight := auditChainPayload(gen, auditChainFullRow("a", "bc"))
	assert.NotEqual(t, splitLeft, splitRight,
		"moving a character across a field boundary must change the hash; equal "+
			"hashes mean the payload concatenates without boundaries and a value "+
			"can be shifted between columns undetected")

	// The separator case, which a SEPARATOR-joined encoding would collide on.
	// Both encodings are unsafe in different ways, so both need pinning.
	withPipeInAction := auditChainPayload(gen, auditChainFullRow("a|b", "c"))
	withPipeInTarget := auditChainPayload(gen, auditChainFullRow("a", "b|c"))
	assert.NotEqual(t, withPipeInAction, withPipeInTarget,
		"a value containing the separator must not collide with one split across "+
			"two fields")

}

func TestAuditChainPayloadDistinguishesNullFromEmptyString(t *testing.T) {
	gen := make([]byte, 32)

	asNull := auditChainFullRow("x", "y")
	asNull.detail = sql.NullString{}

	asEmpty := auditChainFullRow("x", "y")
	asEmpty.detail = sql.NullString{String: "", Valid: true}

	assert.NotEqual(t, auditChainPayload(gen, asNull), auditChainPayload(gen, asEmpty),
		"NULL and the empty string must hash differently, or clearing a field and "+
			"setting it to nothing are the same record")
}

func TestAuditChainPayloadDependsOnThePredecessorHash(t *testing.T) {
	// If prev_hash were not in the payload, every row would commit to the same
	// predecessor and the "chain" would be a list of independent hashes: any row
	// could be replaced with a freshly computed one and nothing downstream would
	// notice, which is the entire property this feature exists to provide.
	row := auditChainFullRow("x", "y")

	fromGenesis := auditChainPayload(make([]byte, 32), row)
	other := make([]byte, 32)
	copy(other, "a different predecessor")
	fromOther := auditChainPayload(other, row)
	assert.NotEqual(t, fromGenesis, fromOther,
		"the payload must include prev_hash, or rows do not chain to each other")

	// And the same predecessor with the same row must be stable, or nothing
	// would ever verify.
	assert.Equal(t, fromGenesis, auditChainPayload(make([]byte, 32), row),
		"the payload must be deterministic")
}
