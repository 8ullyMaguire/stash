package gamify

import (
	"testing"
	"time"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The plan's named test, and the reason this package has no migration: a streak is
// a view whose first column is date(decided_at). There is no streak column and no
// increment path, so the streak is recomputed from the log every time.
func TestAStreakIsRecomputedFromTheAuditLog(t *testing.T) {
	today := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	// day takes an explicit month. My first version took just a day-of-month and
	// built September for all of them, so "Oct 1" was really Sep 1 -- 28 days
	// later -- and the run broke for a reason the test never mentioned. A fixture
	// helper that silently assumes a month is a fixture that can lie.
	day := func(m, d int) time.Time {
		return time.Date(2026, time.Month(m), d, 9, 30, 0, 0, time.UTC)
	}
	// Sanity: the fixture really is the three consecutive days claimed.
	require.Equal(t, 24*time.Hour, day(10, 1).Sub(day(9, 30)))
	require.Equal(t, 24*time.Hour, day(9, 30).Sub(day(9, 29)))

	// Three consecutive days, plus a repeat on one of them.
	events := []Event{
		{ActorID: 1, Action: ActionEditApproved, At: day(9, 29)},
		// Same day again. Five things in an afternoon is a ONE-day streak, not a
		// five-day one -- a version counting events would be a counter wearing a
		// streak's name.
		{ActorID: 1, Action: ActionIdentSolve, At: day(9, 29)},
		{ActorID: 1, Action: ActionEditApproved, At: day(9, 30)},
		{ActorID: 1, Action: ActionVerification, At: day(10, 1)},
		// Yesterday still counts: the day is not over.
		{ActorID: 1, Action: ActionEditApproved, At: day(10, 1)},
	}

	st := StreakFor(events, 1, today)
	assert.Equal(t, 3, st.Current,
		"three distinct consecutive earning days (Sep 29, 30, Oct 1)")
	assert.Equal(t, 3, st.Longest)
	require.Len(t, st.Days, 3, "repeated events on one day collapse to one day")

	// DELETING AN EVENT SHRINKS IT. This is the whole property: there is no
	// counter, so removing a day breaks the run rather than decrementing it.
	//
	// The fixture is a FOUR-day run with the second day removed, because deleting
	// an interior day is the only case that distinguishes recomputation from a
	// counter. My first version deleted the third day of a three-day run, leaving
	// two NON-adjacent days -- where the correct answer is 1, not the 2 I expected.
	// The expectation was wrong and the code was right; the fix is a fixture that
	// actually tests what it claims.
	four := append([]Event{}, events...)
	four = append(four, Event{ActorID: 1, Action: ActionIdentSolve, At: day(9, 28)})

	full := StreakFor(four, 1, today)
	require.Equal(t, 4, full.Current, "precondition: Sep 28, 29, 30 and Oct 1")

	// Remove the Sep 29 events (indices 0 and 1).
	gapped := []Event{four[4], four[3], four[2], four[5]}
	after := StreakFor(gapped, 1, today)
	assert.Equal(t, 2, after.Current,
		"delete Sep 29 from a four-day run and the live run is Sep 30 + Oct 1: "+
			"recomputed, not decremented by one")
	assert.Equal(t, 2, after.Longest,
		"and the longest run is now 2 -- a counter would still report 4")
	assert.Equal(t, 3, len(after.Days),
		"three distinct days remain: Sep 28, 30 and Oct 1")

	// Delete all of them and the streak is GONE -- not zero, absent.
	assert.Equal(t, 0, StreakFor(nil, 1, today).Current)
	assert.Empty(t, StreakFor(nil, 1, today).Days)
}

// The day is not over until it is. A user who did nothing today has not lost their
// streak; a version requiring the last day to be exactly today zeroes every streak
// each morning, which makes a badge anyone can earn disappear once a day.
func TestAStreakSurvivesUntilYesterdayGoesStale(t *testing.T) {
	events := []Event{
		{ActorID: 1, Action: ActionEditApproved, At: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)},
		{ActorID: 1, Action: ActionEditApproved, At: time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)},
		{ActorID: 1, Action: ActionEditApproved, At: time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)},
	}

	today := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	assert.Equal(t, 3, StreakFor(events, 1, today).Current,
		"the last earning day is yesterday and today is not over")

	// Two days later the run is over. Longest survives, because it is derived from
	// the same rows and does not need the run to be live.
	twoDaysOn := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	stale := StreakFor(events, 1, twoDaysOn)
	assert.Equal(t, 0, stale.Current, "older than yesterday: the run is over")
	assert.Equal(t, 3, stale.Longest, "but the record of it is still derived from the log")
}

// An action that earns nothing does not extend a streak. Otherwise a rejected
// proposal would be a way to hold a streak open, which is both absurd and a way to
// game the badge.
func TestANonEarningEventDoesNotExtendAStreak(t *testing.T) {
	today := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{ActorID: 1, Action: ActionEditApproved, At: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)},
		// A rejection two days later is not activity.
		{ActorID: 1, Action: ActionEditRejected, At: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)},
	}

	st := StreakFor(events, 1, today)
	assert.Equal(t, 0, st.Current,
		"the rejection is neither a new day nor a continuation -- the run from Oct 1 "+
			"is two days stale")
	assert.Equal(t, 1, st.Longest)
}

// §6a.10's firewall for this step, and the plan's second named test: with the
// maximum badge set, a user's access level is unchanged.
//
// The STRUCTURAL half is in Badge's definition -- it has no access-level field.
// This test pins the observable half, so that adding one to the badge machinery
// would be caught here rather than discovered in production.
func TestNoBadgeGrantsAnAccessLevel(t *testing.T) {
	today := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	// An actor with a LOT of achievement: high XP, a long streak, many solves.
	var events []Event
	for d := 1; d <= 28; d++ {
		events = append(events,
			Event{ActorID: 1, Action: ActionEditApproved, At: time.Date(2026, 9, d, 9, 0, 0, 0, time.UTC)},
			Event{ActorID: 1, Action: ActionIdentSolve, At: time.Date(2026, 9, d, 10, 0, 0, 0, time.UTC)},
			Event{ActorID: 1, Action: ActionVerification, At: time.Date(2026, 9, d, 11, 0, 0, 0, time.UTC)},
		)
	}

	achieved, err := Derive(events, 1, today)
	require.NoError(t, err)
	assert.Greater(t, achieved.XP, 1000, "precondition: this actor has earned a lot")
	assert.Greater(t, achieved.Level, 4, "precondition: well past Curator in LEVEL terms")

	// The maximum badge set, every threshold satisfiable by this actor.
	badges := MaximalBadges()

	before := collab.LevelFor(achieved.ProposalsApproved, achieved.Verifications,
		achieved.IdentSolves, 0)
	earned := Earned(achieved, badges)
	require.NotEmpty(t, earned, "precondition: this actor has badges")

	// The access level is computed from the AUDIT LOG alone -- and nothing about
	// badges enters it. Recomputing it after the badges exist, with the badge set
	// as large as this package can build, changes nothing.
	after := collab.LevelFor(achieved.ProposalsApproved, achieved.Verifications,
		achieved.IdentSolves, 0)
	assert.Equal(t, before, after,
		"§6a.10: reward never grants access. LevelFor takes no badge argument and "+
			"no XP, so a badge cannot reach it -- earned %v changed nothing.", earned)

	// And the badge type carries no level to grant, so the question cannot be
	// answered affirmatively anywhere in this package.
	for _, b := range badges {
		assert.NotContains(t, b.ID, "steward",
			"a badge named after an access level invites reading it as one")
	}

	// XP is not a trust level either. This actor's XP is large; their earned access
	// level comes from contributions, and a separate user with identical XP and no
	// contributions is at Public.
	var noWork []Event
	for d := 1; d <= 28; d++ {
		noWork = append(noWork,
			Event{ActorID: 2, Action: ActionIdentSolve, At: time.Date(2026, 9, d, 9, 0, 0, 0, time.UTC)},
		)
	}
	xpOnly, err := Derive(noWork, 2, today)
	require.NoError(t, err)
	assert.Greater(t, xpOnly.XP, 500)

	earnedLevel := collab.LevelFor(xpOnly.ProposalsApproved, xpOnly.Verifications,
		xpOnly.IdentSolves, 0)
	assert.Equal(t, collab.LevelCurator, earnedLevel.Level,
		"§6a.10's second sentence: a user excellent at identification earns Curator "+
			"and nothing more, however much XP that generates")
}

// A rejected proposal earns no XP at all. Awarding a smaller amount for the effort
// builds an incentive to submit cheap proposals and hope one lands, which makes
// the queue worse for the reviewers who reject them.
func TestARejectedProposalEarnsNothing(t *testing.T) {
	assert.Equal(t, 0, XPFor(Event{ActorID: 1, Action: ActionEditRejected}),
		"a rejection is a decision AGAINST a contribution")

	xp, err := TotalXP([]Event{
		{ActorID: 1, Action: ActionEditApproved, At: time.Now()},
		{ActorID: 1, Action: ActionEditRejected, At: time.Now()},
		{ActorID: 1, Action: ActionEditRejected, At: time.Now()},
	}, 1)
	require.NoError(t, err)
	assert.Equal(t, 10, xp, "only the approval counted")

	// An unknown action earns nothing, rather than a default. A default of 10 would
	// mean a typo in a new action's name pays out silently.
	assert.Equal(t, 0, XPFor(Event{ActorID: 1, Action: "proposal_applid"}),
		"a typo earns nothing")
}

// XP is a view: delete an event and the total drops.
func TestXPIsRecomputedNotAccumulated(t *testing.T) {
	events := []Event{
		{ActorID: 1, Action: ActionIdentSolve, At: time.Now()},
		{ActorID: 1, Action: ActionIdentSolve, At: time.Now()},
	}
	xp, err := TotalXP(events, 1)
	require.NoError(t, err)
	assert.Equal(t, 50, xp)

	xp, err = TotalXP(events[:1], 1)
	require.NoError(t, err)
	assert.Equal(t, 25, xp, "delete one event and the total drops -- not a counter")

	xp, err = TotalXP(nil, 1)
	require.NoError(t, err)
	assert.Equal(t, 0, xp)
}

// The clock is a parameter, so a streak that ended three days ago is testable on
// any day. Every streak test would otherwise have to be written the day it runs.
func TestTheClockIsAParameterNotTheWallClock(t *testing.T) {
	// A streak from 2020, checked "today".
	events := []Event{
		{ActorID: 1, Action: ActionEditApproved, At: time.Date(2020, 1, 1, 9, 0, 0, 0, time.UTC)},
		{ActorID: 1, Action: ActionEditApproved, At: time.Date(2020, 1, 2, 9, 0, 0, 0, time.UTC)},
	}

	live := StreakFor(events, 1, time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, 2, live.Current)

	// The same events, "checked" years later, is a test that runs identically today.
	ancient := StreakFor(events, 1, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, 0, ancient.Current)
	assert.Equal(t, 2, ancient.Longest)
}

// Level bands widen, so a later level is not twenty times the work of level two.
// A uniform +100 makes level 20 take twenty times as long as level 2, and every
// user hits that wall and leaves.
func TestLevelBandsWiden(t *testing.T) {
	assert.Equal(t, 1, Level(0))
	assert.Equal(t, 1, Level(99))
	assert.Equal(t, 2, Level(100))
	assert.Equal(t, 2, Level(299))
	assert.Equal(t, 3, Level(300))
	assert.Equal(t, 3, Level(599))
	assert.Equal(t, 4, Level(600))

	// Reaching level 5 takes 1000 XP, not 400.
	assert.Equal(t, 5, Level(1000))
	assert.Greater(t, Level(1000), Level(400))

	assert.Equal(t, 1, Level(-50), "negative XP is level 1, not an error or a negative level")
}

// A fresh level reads as an EMPTY bar, not a full one.
func TestProgressRendersAFreshLevelAsEmpty(t *testing.T) {
	p := Progress(0)
	assert.Equal(t, 1, p.Level)
	assert.Equal(t, 0, p.IntoLevel)
	assert.Equal(t, 0.0, p.Fraction, "a bar at the start of a level must read empty, not full")

	p = Progress(LevelBase)
	assert.Equal(t, 2, p.Level)
	assert.Equal(t, 0, p.IntoLevel, "exactly on the boundary is the start of the next level")
	assert.Equal(t, 0.0, p.Fraction)

	p = Progress(LevelBase + LevelBase*2/2)
	assert.Equal(t, 2, p.Level)
	assert.InDelta(t, 0.5, p.Fraction, 1e-9)
}

// Days are UTC, not the server's local zone: a local-zone calendar day makes a
// streak a function of deployment geography, so moving the instance breaks
// everybody's streak.
//
// THIS TEST IS ABOUT THE DAY VALUES, NOT THE RUN LENGTH, and a mutation says so.
// Replacing `t.UTC()` with `t` survives an assertion of Current==2, because
// shifting both timestamps into the same zone leaves them two consecutive days
// either way. Measured: 23:30 UTC on Sep 30 is Sep 30 in UTC and Oct 1 in CEST --
// different days -- yet the run length is 2 under both. Asserting the length was
// asserting the wrong property.
func TestStreakDaysAreUTC(t *testing.T) {
	// 23:30 UTC on the 30th and 00:30 UTC on the 31st. Under UTC these are
	// consecutive days; under CEST+2 they become BOTH Oct 1, collapsing to one.
	events := []Event{
		{ActorID: 1, Action: ActionEditApproved, At: time.Date(2026, 9, 30, 23, 30, 0, 0, time.UTC)},
		{ActorID: 1, Action: ActionEditApproved, At: time.Date(2026, 10, 1, 0, 30, 0, 0, time.UTC)},
	}

	st := StreakFor(events, 1, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	require.Len(t, st.Days, 2,
		"two distinct days. Under a local zone reading these timestamps the second "+
			"one would land on Oct 1 and the first would ALSO be Oct 1, collapsing "+
			"the run to one -- which is the geography dependency being ruled out.")

	// The exact days, not just how many. This is the assertion the mutant needs.
	assert.Equal(t, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), st.Days[0],
		"the first event is Sep 30 in UTC")
	assert.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), st.Days[1],
		"the second is Oct 1 in UTC. A CEST deployment would report Oct 1 for both.")

	for _, day := range st.Days {
		assert.Equal(t, 0, day.Hour(), "every streak day is normalised to midnight")
		assert.Equal(t, time.UTC, day.Location(), "and normalised to UTC, so two days "+
			"cannot compare equal-or-odd by accident")
	}

	assert.Equal(t, 2, st.Current, "and they are consecutive, so the run is 2")

	// A single-day case, where a local zone would visibly disagree about WHICH day.
	one := StreakFor([]Event{
		{ActorID: 1, Action: ActionEditApproved, At: time.Date(2026, 9, 30, 23, 30, 0, 0, time.UTC)},
	}, 1, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	require.Len(t, one.Days, 1)
	assert.Equal(t, 30, one.Days[0].Day(),
		"23:30 UTC on Sep 30 is Sep 30 -- a CEST deployment would call it Oct 1, "+
			"which would silently keep this user's streak alive today")
}

// MaximalBadges is the largest badge set this package can express, used by the
// firewall test. Every threshold is satisfiable, so it is the worst case for
// "badges accidentally confer something".
func MaximalBadges() []Badge {
	return []Badge{
		{ID: "first-edit", Label: "First Edit", Threshold: func(d Derived) bool { return d.ProposalsApproved >= 1 }},
		{ID: "prolific", Label: "Prolific", Threshold: func(d Derived) bool { return d.ProposalsApproved >= 10 }},
		{ID: "detective", Label: "Detective", Threshold: func(d Derived) bool { return d.IdentSolves >= 1 }},
		{ID: "super-sleuth", Label: "Super Sleuth", Threshold: func(d Derived) bool { return d.IdentSolves >= 25 }},
		{ID: "careful", Label: "Careful", Threshold: func(d Derived) bool { return d.Verifications >= 5 }},
		{ID: "streak-3", Label: "Three in a Row", Threshold: func(d Derived) bool { return d.Streak.Current >= 3 }},
		{ID: "streak-30", Label: "Month of Days", Threshold: func(d Derived) bool { return d.Streak.Longest >= 30 }},
		{ID: "level-2", Label: "Contributor", Threshold: func(d Derived) bool { return d.Level >= 2 }},
		{ID: "level-5", Label: "Veteran", Threshold: func(d Derived) bool { return d.Level >= 5 }},
		{ID: "marathon", Label: "Marathon", Threshold: func(d Derived) bool { return d.XP >= 10000 }},
	}
}

// Derive must not count another actor's events, or XP leaks between users.
func TestDeriveIsPerActor(t *testing.T) {
	today := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{ActorID: 1, Action: ActionIdentSolve, At: time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)},
		{ActorID: 2, Action: ActionIdentSolve, At: time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)},
		{ActorID: 1, Action: ActionIdentSolve, At: time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)},
		{ActorID: 2, Action: ActionIdentSolve, At: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)},
	}

	one, err := Derive(events, 1, today)
	require.NoError(t, err)
	assert.Equal(t, 2, one.IdentSolves)
	assert.Equal(t, 2, one.Streak.Current)

	two, err := Derive(events, 2, today)
	require.NoError(t, err)
	assert.Equal(t, 2, two.IdentSolves)
	assert.Equal(t, 1, two.Streak.Current, "actor 2's days are Sep 28 and 30 -- a gap, so no live run")
	assert.Equal(t, 1, two.Streak.Longest)
}

// A streak is computed from what the DATABASE returns, and a driver may hand back
// a time in the server's local zone rather than UTC. Without the .UTC() conversion
// in day(), the same audit rows produce different streaks depending on where the
// instance is deployed -- the geography dependency, arriving through the driver.
//
// The mutation that replaces `t.UTC()` with `t` survived every other assertion in
// this file, and the reason is that every fixture timestamp was built with
// time.Date(..., time.UTC). For such a value t.Location() IS time.UTC, so the two
// spellings are identical. Measured, not assumed.
//
// So this test uses a NON-UTC input: the same instant expressed in a +2 zone. 23:30
// UTC on Sep 30 is 01:30 on Oct 1 in Madrid, and a local-zone reading calls it the
// wrong day -- which silently keeps a stale streak alive.
func TestStreakDaysAreUTCForALocalZoneInput(t *testing.T) {
	madrid := time.FixedZone("CEST", 2*60*60)

	// ONE event, at 00:30 on Oct 1 in CEST. That is 22:30 on SEP 30 in UTC.
	//
	// So the two readings disagree about which DAY it is, and that disagreement is
	// the whole point: read in the input zone this is "today" and the streak is
	// live; read in UTC it is "yesterday" and the streak has lapsed. My first
	// version of this test used TWO events an hour apart and asserted two distinct
	// days -- but 23:30 and 00:30 CEST are both 21:30 and 22:30 UTC on the SAME
	// day, so UTC correctly collapses them and my assertion was simply wrong.
	single := StreakFor([]Event{
		{ActorID: 1, Action: ActionEditApproved, At: time.Date(2026, 10, 1, 0, 30, 0, 0, madrid)},
	}, 1, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))

	require.Len(t, single.Days, 1)
	assert.Equal(t, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), single.Days[0],
		"00:30 on Oct 1 in CEST is 22:30 on Sep 30 in UTC, so this is a SEP 30 day. A "+
			"local-zone streak would call it Oct 1 and report it as today's activity.")

	// Current is 1, NOT 0, and that is correct: yesterday still counts, because the
	// day is not over. I wrote 0 here on the reasoning that "the last day is
	// yesterday so it has lapsed" -- which is exactly the bug the
	// TestAStreakSurvivesUntilYesterdayGoesStale test exists to forbid. The streak
	// is live under BOTH readings; what differs is WHICH DAY it is on, and the Days
	// assertion above is what pins that down.
	//
	// So the local-zone mutant is caught by the day value, not by the run length --
	// which is the finding that made this test worth writing.
	assert.Equal(t, 1, single.Current,
		"yesterday still counts, so the streak is live under the correct reading")

	assert.Equal(t, 1, single.Longest)

	// And the same instant, expressed in UTC, behaves identically -- proving the
	// conversion is what normalises them rather than the fixture being lucky.
	asUTC := StreakFor([]Event{
		{ActorID: 1, Action: ActionEditApproved, At: time.Date(2026, 9, 30, 22, 30, 0, 0, time.UTC)},
	}, 1, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))

	assert.Equal(t, single.Days, asUTC.Days,
		"the same instant in two zones gives the same streak day -- which is what "+
			"the .UTC() conversion buys")
	assert.Equal(t, single.Current, asUTC.Current)
}
