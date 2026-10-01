//go:build integration
// +build integration

// Field reputation store tests. StashForge M2b.
//
// The migration tests check the schema enforces its constraints. These check the
// store honours the two rules that only exist in Go: the reputation floor on a
// debit, and the difference between "no row" and "a row of zero".

package sqlite_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/sqlite"
)

func repStore() *sqlite.CollabReputationStore {
	return sqlite.NewCollabReputationStore()
}

func standingFor(uid int, field string) collab.FieldStanding {
	return collab.FieldStanding{
		UserID:     uid,
		TargetType: "performer",
		Field:      field,
	}
}

// TestReputationStore_StandingOnAFreshUserIsErrNoReputation: a user who has
// never been elected on a field has no row, and that must be a distinct answer
// from "a row of zero".
func TestReputationStore_StandingOnAFreshUserIsErrNoReputation(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		uid := insertUser(t, ctx, "repFresh", false)
		s := repStore()

		_, err := s.Standing(ctx, int(uid), "performer", "details")
		require.ErrorIs(t, err, collab.ErrNoReputation,
			"a user with no reputation on a field must report ErrNoReputation, "+
				"which is distinct from a zero row and from a database fault")
	})
}

// TestReputationStore_RecordOutcomeCredits: agreeing with a settled proposal
// raises reputation.
func TestReputationStore_RecordOutcomeCredits(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		uid := insertUser(t, ctx, "repCredit", false)
		s := repStore()
		key := standingFor(int(uid), "details")

		first, err := s.RecordOutcome(ctx, key, true)
		require.NoError(t, err)
		assert.Equal(t, 1, first.Reputation, "the first credit creates the row at 1")
		assert.Equal(t, 0, first.Rejections, "crediting must not touch rejections")

		second, err := s.RecordOutcome(ctx, key, true)
		require.NoError(t, err)
		assert.Equal(t, 2, second.Reputation)

		// And it must be readable back as the same value.
		read, err := s.Standing(ctx, int(uid), "performer", "details")
		require.NoError(t, err)
		assert.Equal(t, 2, read.Reputation)
	})
}

// TestReputationStore_RecordOutcomeDebits: losing raises rejections, and
// reputation never goes negative.
//
// The floor is the point. reputation is CHECK (>= 0), so an unclamped debit
// would end in a constraint violation -- the user would see a server fault
// rather than "you have been overruled here a few times". The floor is also the
// governance position: decay discounts a vote, it does not silence a voter.
func TestReputationStore_RecordOutcomeDebits(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		uid := insertUser(t, ctx, "repDebit", false)
		s := repStore()
		key := standingFor(int(uid), "details")

		// Start with some standing so there is something to debit.
		_, err := s.RecordOutcome(ctx, key, true)
		require.NoError(t, err)

		after, err := s.RecordOutcome(ctx, key, false)
		require.NoError(t, err)
		assert.Equal(t, 1, after.Rejections, "a loss must be counted")
		assert.Equal(t, 0, after.Reputation, "a loss costs one reputation point")

		// Drive it below zero. Must clamp, not fail.
		for i := 0; i < 5; i++ {
			_, err := s.RecordOutcome(ctx, key, false)
			require.NoError(t, err, "a losing streak must clamp at zero, not "+
				"raise a constraint violation")
		}

		floored, err := s.Standing(ctx, int(uid), "performer", "details")
		require.NoError(t, err)
		assert.Equal(t, 0, floored.Reputation,
			"reputation floors at zero; a negative value would be a second, "+
				"stronger silencing mechanism than decay, and an invisible one")
		assert.Equal(t, 6, floored.Rejections,
			"every loss is still counted even after the reputation floor")
	})
}

// TestReputationStore_DebitAgainstNothingIsNoOp: a user with no row who loses
// has nothing to debit, and inventing a row would record a judgement about
// someone who has never been judged.
func TestReputationStore_DebitAgainstNothingIsNoOp(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		uid := insertUser(t, ctx, "repNoRow", false)
		s := repStore()

		_, err := s.RecordOutcome(ctx, standingFor(int(uid), "details"), false)
		require.ErrorIs(t, err, collab.ErrNoReputation)

		assert.Equal(t, int64(0),
			count(t, ctx, "SELECT count(*) FROM field_reputation WHERE user_id = ?", uid),
			"a failed debit must not leave a row behind")
	})
}

// TestReputationStore_StandingsForManyOmitsUsersWithNoRow: the batch read keeps
// "no row" distinguishable from "a row of zero" all the way to the caller.
func TestReputationStore_StandingsForManyOmitsUsersWithNoRow(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		veteran := insertUser(t, ctx, "repVeteran", false)
		newcomer := insertUser(t, ctx, "repNewcomer", false)
		zeroed := insertUser(t, ctx, "repZeroed", false)
		s := repStore()

		key := standingFor(int(veteran), "details")
		_, err := s.RecordOutcome(ctx, key, true)
		require.NoError(t, err)

		// A row that exists and is genuinely zero, reached by winning then
		// losing enough times to floor.
		zeroKey := standingFor(int(zeroed), "details")
		_, err = s.RecordOutcome(ctx, zeroKey, true)
		require.NoError(t, err)
		_, err = s.RecordOutcome(ctx, zeroKey, false)
		require.NoError(t, err)

		out, err := s.StandingsForMany(ctx, []int{int(veteran), int(newcomer), int(zeroed)}, "performer", "details")
		require.NoError(t, err)

		assert.Len(t, out, 2, "the user with no row must be ABSENT, not present with zeros")
		assert.Contains(t, out, int(veteran))
		assert.Contains(t, out, int(zeroed), "a genuine zero row must be present")
		assert.NotContains(t, out, int(newcomer))

		// The two present users must be distinguishable by their values.
		assert.Equal(t, 1, out[int(veteran)].Reputation)
		assert.Equal(t, 0, out[int(zeroed)].Reputation)
	})
}

func TestReputationStore_StandingsForManyEmptyInput(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		out, err := repStore().StandingsForMany(ctx, nil, "performer", "details")
		require.NoError(t, err)
		assert.Empty(t, out, "an empty voter set must not error and must not query")
	})
}

// TestReputationStore_IsPerField: reputation for one field must not leak into
// another. The whole argument for per-field reputation is that being reliable
// about performer metadata says nothing about galleries.
func TestReputationStore_IsPerField(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		uid := insertUser(t, ctx, "repScoped", false)
		s := repStore()

		_, err := s.RecordOutcome(ctx, standingFor(int(uid), "details"), true)
		require.NoError(t, err)

		// A different field on the same target.
		_, err = s.Standing(ctx, int(uid), "performer", "name")
		require.ErrorIs(t, err, collab.ErrNoReputation,
			"reputation on one field must not appear on another; a global "+
				"score would transfer trust between unrelated competences")

		// A different target type on the same field name.
		_, err = s.Standing(ctx, int(uid), "gallery", "details")
		require.ErrorIs(t, err, collab.ErrNoReputation,
			"reputation must be scoped to (target_type, field), not field alone")
	})
}

// TestReputationStore_ErrNoReputationSurvivesWrapping: the adapter wraps errors
// with %w so a caller can still branch on ErrNoReputation. If this ever becomes
// a bare %v, a database fault becomes indistinguishable from "no row", and the
// caller would treat a broken database as a brand-new user.
func TestReputationStore_ErrNoReputationSurvivesWrapping(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		uid := insertUser(t, ctx, "repWrapped", false)

		_, err := repStore().Standing(ctx, int(uid), "performer", "details")
		require.Error(t, err)
		assert.True(t, errors.Is(err, collab.ErrNoReputation),
			"ErrNoReputation must survive wrapping with %%w; got %v", err)
	})
}

// TestReputationStore_WeightBasisIsNotTheStore: the translation from reputation
// to weight is collab's, and the store must not do it.
//
// A store that returned weights would make the sub-linear curve, the floor and
// the cap into persistence-layer policy, and a change to that policy would then
// need a migration to be visible to callers that cache the value.
func TestReputationStore_WeightBasisIsNotTheStore(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		uid := insertUser(t, ctx, "repWeightBasis", false)
		s := repStore()
		key := standingFor(int(uid), "details")
		_, err := s.RecordOutcome(ctx, key, true)
		require.NoError(t, err)

		standing, err := s.Standing(ctx, int(uid), "performer", "details")
		require.NoError(t, err)

		basis := standing.WeightBasisFor(1)
		assert.Equal(t, 1, basis.Reputation, "the standing carries the raw score")
		assert.Equal(t, 0, basis.FieldRejections, "and the raw rejection count")

		// The WEIGHT is collab's arithmetic, applied on demand.
		assert.Equal(t, collab.ReputationFloor, collab.ReputationToBasis(basis.Reputation),
			"a reputation of 1 is still a vote, just the smallest one")
	})
}
