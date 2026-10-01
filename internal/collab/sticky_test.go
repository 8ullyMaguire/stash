package collab_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
)

func key() collab.StickyRejectionKey {
	return collab.NewStickyRejectionKey("scene", 42, "title", 7)
}

func TestStickyRejection_BlocksTheSameAuthorOnTheSameField(t *testing.T) {
	// The rule: an author whose proposal on a field was rejected does not get an
	// infinite retry queue against that same field. A proposal system that lets
	// an author re-ask until they get their way is a queue, not governance.
	rejected := map[collab.StickyRejectionKey]bool{}
	k := key()

	assert.False(t, collab.IsBlocked(rejected, k), "nothing is rejected yet")

	rejected = collab.RecordRejection(rejected, k, false)
	assert.True(t, collab.IsBlocked(rejected, k),
		"a rejected (target, field, author) must block the next proposal from the same author")
}

func TestStickyRejection_DoesNotBlockADifferentAuthor(t *testing.T) {
	// One author losing a vote says nothing about anyone else's proposal on the
	// same field. Blocking the whole field would let any user grief the field
	// permanently by getting one proposal rejected.
	rejected := collab.RecordRejection(nil, key(), false)

	other := collab.NewStickyRejectionKey("scene", 42, "title", 8)
	assert.False(t, collab.IsBlocked(rejected, other),
		"a rejection must bind the author who lost it, not the field itself")
}

func TestStickyRejection_DoesNotBlockADifferentField(t *testing.T) {
	rejected := collab.RecordRejection(nil, key(), false)

	other := collab.NewStickyRejectionKey("scene", 42, "details", 7)
	assert.False(t, collab.IsBlocked(rejected, other),
		"a rejection on title must not stop the same author proposing a change to details")
}

func TestStickyRejection_DoesNotBlockADifferentTarget(t *testing.T) {
	rejected := collab.RecordRejection(nil, key(), false)

	other := collab.NewStickyRejectionKey("scene", 43, "title", 7)
	assert.False(t, collab.IsBlocked(rejected, other),
		"a rejection on one scene must not stop the same author editing another")
}

// Supersession is a DIFFERENT terminal state from rejection. If superseding
// recorded a sticky rejection, the newer proposal's author would be permanently
// barred from the field — which is how a proposal system quietly stops
// accepting corrections.
func TestStickyRejection_SupersededDoesNotBlock(t *testing.T) {
	rejected := collab.RecordRejection(nil, key(), true)
	assert.False(t, collab.IsBlocked(rejected, key()),
		"a superseded proposal must not bar its author from the field")
}

func TestStickyRejection_RecordRejectionHandlesANilMap(t *testing.T) {
	// The common first-write path. A nil map assignment panics, so this is the
	// case that would take out the first rejection on a fresh instance.
	require.NotPanics(t, func() {
		m := collab.RecordRejection(nil, key(), false)
		assert.True(t, collab.IsBlocked(m, key()))
	})
}

func TestStickyRejection_KeyIsAUsableMapKey(t *testing.T) {
	// The key is a comparable struct rather than a formatted string precisely so
	// it works as a map key and as a column value. A string key built by
	// concatenation would collide on a field or id containing the separator.
	a := collab.NewStickyRejectionKey("scene", 1, "a,b", 2)
	b := collab.NewStickyRejectionKey("scene", 1, "a", 3)

	m := map[collab.StickyRejectionKey]bool{a: true}
	assert.False(t, m[b], "two different keys must not collide in a map")
}
