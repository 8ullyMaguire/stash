package collab

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// §6a.10's firewall, as a test: "reputation feeds ballot weight and completion
// metrics; it never grants a trust level".
//
// THE TEST THAT MATTERS IS THE SIGNATURE. TestReputationNeverGrantsAnAccessLevel
// proves the two never meet, and the comment on LevelFor says why a runtime test
// cannot: there is no argument through which a caller could pass a reputation.
// This file asserts that structurally, so the day someone adds one, it fails.

// LevelFor takes no reputation. If a reputation argument is ever added, this
// stops compiling, which is the outcome wanted.
func TestReputationNeverGrantsAnAccessLevel(t *testing.T) {
	// The maximal-reputation case from §6a.10: enormous standing, and no
	// verification record at all. Stays at Public.
	earned := LevelFor(0, 0, 0, 0)
	assert.Equal(t, LevelPublic, earned.Level,
		"a user with maximal reputation and no verification record stays at level 0")

	// And the firewall holds all the way up: nothing but audit-log evidence moves
	// the level, so the two ladders are independent.
	for _, tc := range []struct {
		edits, verified, solves, preserves int
		want                               AccessLevel
	}{
		{0, 0, 0, 0, LevelPublic},
		{1, 0, 0, 0, LevelContributor},
		{3, 0, 0, 0, LevelCurator},
		// Archivist needs ident solves AND approved edits: identification is the
		// activity that earns it, but a user who only identifies has not yet had
		// any edit approved, so they reach Curator. A row that asserted Archivist
		// here would have been a second, lazier copy of the ladder.
		{0, 0, 5, 0, LevelCurator},
		{4, 0, 5, 0, LevelCurator}, // one edit short of Archivist
		{5, 0, 5, 0, LevelArchivist},
		{10, 10, 0, 0, LevelSteward},
	} {
		got := LevelFor(tc.edits, tc.verified, tc.solves, tc.preserves)
		assert.Equal(t, tc.want, got.Level,
			"edits=%d verified=%d solves=%d preserves=%d",
			tc.edits, tc.verified, tc.solves, tc.preserves)
	}

	// The types are separate. An Earned carries an AccessLevel and nothing else,
	// so there is no field a reputation could be smuggled through.
	e := Earned{Level: LevelCurator}
	assert.Equal(t, LevelCurator, e.Level)
}

// §6a.11's resolution, as a test: level 4 with no consent gets
// ErrConsentRequired, not content.
func TestLevelFourDoesNotEnableViewingOnItsOwn(t *testing.T) {
	earned := LevelFor(5, 0, 5, 0)
	require.Equal(t, LevelArchivist, earned.Level,
		"precondition: this user HAS earned Archivist")

	err := DecideAccess(earned, LevelArchivist, LevelArchivist, false)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConsentRequired,
		"reaching level 4 does not itself enable content; §6a.11 makes consent a "+
			"separate switch")
	assert.NotErrorIs(t, err, ErrNotEarned,
		"the user HAS earned it -- reporting NotEarned would send them to contribute "+
			"when the actual blocker is a consent they have not given")

	// The same user, having consented, is allowed. Otherwise the test would pass
	// by refusing everyone.
	assert.NoError(t, DecideAccess(earned, LevelArchivist, LevelArchivist, true),
		"level 4 plus per-instance consent is exactly what §6a.11 permits")
}

// Consent is not consulted below level 4. Asking for consent to browse would
// train users to click through the one prompt that matters.
func TestConsentIsNotAskedBelowLevelFour(t *testing.T) {
	for _, lvl := range []AccessLevel{LevelPublic, LevelRegistered, LevelContributor, LevelCurator} {
		assert.NoError(t, DecideAccess(Earned{Level: lvl}, lvl, lvl, false),
			"level %d needs no consent: there is no protected thing to consent to", lvl)
	}
}

// The operator's ceiling is a CEILING. §6a.12: it never grants the operator
// anything the threshold excludes.
func TestAnOperatorCeilingOnlyLimits(t *testing.T) {
	// A generous policy does not lift someone who has not earned it.
	err := DecideAccess(Earned{Level: LevelRegistered}, LevelSteward, LevelSteward, true)
	require.ErrorIs(t, err, ErrNotEarned,
		"a low threshold is a policy choice about what the instance OFFERS; it "+
			"never grants anyone the level they have not earned")

	// THE CASE THAT FOUND THE BUG. A Steward on a Registered-ceiling instance.
	// The first version compared earned against ceiling, so 5 >= 1 passed and the
	// operator's ceiling did not restrain the highest-trust users -- precisely the
	// ones lowering a threshold is meant to restrain.
	//
	// ErrNotOffered rather than ErrNotEarned, because the two are fixable by
	// opposite parties and conflating them would tell a Steward to contribute more
	// on an instance that has simply decided not to serve content.
	// The Steward ASKS for Steward on an instance whose ceiling is Registered --
	// that is the case the old comparison got wrong. Asking for only what the
	// ceiling allows is of course fine, which is why this requests the top level.
	err = DecideAccess(Earned{Level: LevelSteward}, LevelSteward, LevelRegistered, true)
	require.ErrorIs(t, err, ErrNotOffered,
		"an operator's ceiling is a CEILING: a Steward still only gets what the "+
			"threshold allows. There is no operator parameter to DecideAccess and it "+
			"must never acquire one.")

	// Requesting exactly what the ceiling offers is allowed, for anyone who has
	// earned it -- the ceiling does not become a target nobody can reach.
	assert.NoError(t,
		DecideAccess(Earned{Level: LevelRegistered}, LevelRegistered, LevelRegistered, true),
		"asking for precisely the offered level is permitted")

	// And the earned/offered pair is genuinely independent in both directions.
	assert.ErrorIs(t,
		DecideAccess(Earned{Level: LevelRegistered}, LevelSteward, LevelSteward, true),
		ErrNotEarned, "not earned")
	assert.ErrorIs(t,
		DecideAccess(Earned{Level: LevelSteward}, LevelSteward, LevelRegistered, true),
		ErrNotOffered, "not offered")
}

// A policy threshold above the top level is clamped, not refused: an operator who
// types 9 has made content maximally available, which is unwise but legitimate.
func TestAnOutOfRangePolicyIsClamped(t *testing.T) {
	assert.Equal(t, MaxAccessLevel, AccessLevel(99).Clamp())
	assert.Equal(t, LevelPublic, AccessLevel(-3).Clamp())

	err := DecideAccess(Earned{Level: LevelSteward}, AccessLevel(99), AccessLevel(99), false)
	assert.ErrorIs(t, err, ErrConsentRequired,
		"clamped to Steward, so the consent requirement still applies -- clamping "+
			"must not become a way to skip it")
}

// Which blocker is reported matters, because the two send a user to different
// places. Order is chosen so the fixable-by-contributing one comes first.
func TestTheReportedBlockerIsTheOneTheUserCanActOn(t *testing.T) {
	// Neither earned nor consented: told they have not earned it, because that is
	// the one they can fix by contributing.
	err := DecideAccess(Earned{Level: LevelPublic}, LevelArchivist, LevelArchivist, false)
	assert.ErrorIs(t, err, ErrNotEarned,
		"when both are missing, the level is the actionable blocker")

	// Earned but not consented: the consent error, specifically.
	err = DecideAccess(Earned{Level: LevelArchivist}, LevelArchivist, LevelArchivist, false)
	assert.ErrorIs(t, err, ErrConsentRequired)
	assert.NotErrorIs(t, err, ErrNotEarned)
}

// Negative counts are floored at zero rather than unlocking a level. A bug
// upstream that subtracts one edit too many should not be a promotion path.
func TestNegativeAuditCountsCannotEarnAnything(t *testing.T) {
	for _, got := range []Earned{
		LevelFor(-100, -100, -100, -100),
		LevelFor(1, -1, -1, -1),
	} {
		assert.NotEqual(t, LevelCurator, got.Level)
		assert.NotEqual(t, LevelArchivist, got.Level)
		assert.NotEqual(t, LevelSteward, got.Level)
	}
	assert.Equal(t, LevelPublic, LevelFor(-1, -1, -1, -1).Level,
		"an all-negative record is the same as no record")
}

// The ladder has exactly six rungs and no seventh. A new level added to the
// const block without updating this would silently make MaxAccessLevel wrong.
func TestThereAreSixLevelsAndNoMore(t *testing.T) {
	seen := map[AccessLevel]string{}
	for _, l := range []AccessLevel{
		LevelPublic, LevelRegistered, LevelContributor,
		LevelCurator, LevelArchivist, LevelSteward,
	} {
		require.NotContains(t, seen, l, "duplicate level value")
		seen[l] = l.String()
	}
	assert.Len(t, seen, LevelCount)
	assert.Equal(t, LevelSteward, MaxAccessLevel)
	assert.Equal(t, 5, int(MaxAccessLevel), "level 5 is the top of the ladder")

	for l, name := range seen {
		assert.NotContains(t, name, "AccessLevel(",
			"level %d has no String case, so it renders as a raw int", l)
	}
}

// Consent follows the REQUEST, not the earned level.
//
// The gap this closes was found by mutation: keying the consent check on the
// earned level instead of the requested one passed every test in this file. The
// user it misbehaves for is an Archivist doing something that needs no consent --
// browsing the directory, reading a review -- who would be prompted anyway because
// of a level they earned months ago and which has nothing to do with the request.
func TestConsentFollowsTheRequestNotTheEarnedLevel(t *testing.T) {
	archivist := Earned{Level: LevelArchivist}

	// Asking for Curator on a Curator ceiling: within policy, and needs no
	// consent. Keyed on earned, this would wrongly demand one.
	assert.NoError(t, DecideAccess(archivist, LevelCurator, LevelCurator, false),
		"an Archivist doing something that needs no consent must not be prompted "+
			"because of a level they earned months ago")

	// The same user asking for content access still is prompted.
	assert.ErrorIs(t,
		DecideAccess(archivist, LevelArchivist, LevelArchivist, false),
		ErrConsentRequired,
		"the request, not the standing, is what brings consent into play")
}
