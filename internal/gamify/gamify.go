// Package gamify derives XP, levels, badges and streaks from the audit log.
//
// M7 step 7.6a (R023, R033, R050–R053), spec §4.2 and §6a.10.
//
// NOTHING IS COUNTERED. Every number here is a function of audit rows. A stored
// XP column is the counter rule with a holiday hat on it: it needs a trigger on
// every path that should increment it, and there is no reliable trigger, because
// a contribution can arrive by an import, a federated sync, or somebody else's
// edit. Streaks are the worst case — a streak IS a count, so "a stored streak is
// the counter rule with a holiday hat on it" is literal, not a figure of speech.
//
// THE ONE DATE OPERATION, and it is the whole of §6a.10's implementation here:
// a streak is a run of distinct date(decided_at) values. The audit table already
// indexes (actor_id, at DESC) and (at DESC), which is exactly the access pattern,
// so this reads what migration 90 built rather than inventing storage.
//
// AND THE FIREWALL, which is this step's second named test: no badge grants an
// access level. See NoBadgeGrantsAnAccessLevel's comment for why the type
// structure rather than a runtime check.
package gamify

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// Event is one audit row, as this package needs it.
//
// A STRUCT RATHER THAN collab.AuditEntry, because that type carries no timestamp
// and a streak is a function of time. Reading the time from a separate call would
// mean two queries that can disagree — the streak computed from one snapshot of
// `at` and the XP from another — and gamification numbers that disagree with
// themselves are exactly the numbers nobody trusts.
type Event struct {
	ActorID int
	Action  string
	At      time.Time
	Target  string
}

// Audit actions that earn XP. Spelled out so a typo is a compile error rather
// than a row that silently earns nothing.
//
// WHAT IS NOT HERE, and the omission is the design: no action that REPRESENTS a
// level, and no action whose count could stand in for one. R023, R033 and the
// whole of R050-R053 are reached from the audit log's existing vocabulary, so
// adding a new earning action is a decision about governance, not a schema
// change.
const (
	ActionEditApproved  = "proposal_applied"  // a field this user proposed was accepted
	ActionEditRejected  = "proposal_rejected" // NO xp — see XPFor
	ActionIdentSolve    = "ident_solved"
	ActionVerification  = "verified"
	ActionMergeApproved = "duplicate_merged"
	ActionQuestComplete = "quest_completed"
)

// XPFor is what one event is worth.
//
// A REJECTED PROPOSAL EARNS NOTHING, and a rejection is a decision AGAINST a
// contribution. The tempting version awards a smaller amount for the effort, on
// the reasoning that showing up is itself worth something; what that actually
// builds is an incentive to submit cheap proposals and hope one lands, which
// makes the queue worse for the reviewers who have to reject them.
func XPFor(e Event) int {
	switch e.Action {
	case ActionEditApproved:
		return 10
	case ActionIdentSolve:
		return 25
	case ActionVerification:
		return 15
	case ActionMergeApproved:
		return 20
	case ActionQuestComplete:
		return 30
	case ActionEditRejected:
		return 0
	}
	// An unknown action earns nothing rather than a default. A default of 10 would
	// mean a typo in a new action's name pays out silently, and gamification that
	// pays for typos is gamification nobody can reason about.
	return 0
}

// ErrNegativeXP is returned when a total would go below zero.
var ErrNegativeXP = errors.New("gamify: XP total cannot be negative")

// TotalXP is the XP for an actor, derived from the log.
//
// NOT ACCUMULATED. Recomputed from the whole event set every time, which is the
// point: delete an event and the total drops. A counter cannot do that, and the
// reconciliation job that makes a counter correct is itself a counter.
func TotalXP(events []Event, actorID int) (int, error) {
	total := 0
	for _, e := range events {
		if e.ActorID != actorID {
			continue
		}
		total += XPFor(e)
	}
	if total < 0 {
		// Unreachable with the current table, and that is the point: every XPFor
		// returns a non-negative constant. A future action with a penalty needs
		// this, and finding out by a negative level is not how to find out.
		return 0, fmt.Errorf("%w: %d for actor %d", ErrNegativeXP, total, actorID)
	}
	return total, nil
}

// Level is an XP band. Exported as data because the UI needs the boundaries to
// render a progress bar, and a view that cannot say where the next level starts
// makes the user guess.
const LevelBase = 100

// Level is the level for an XP total: 1 at zero, 2 at 100, 3 at 300.
//
// The bands widen (100, 200, 300, …) rather than being uniform, so later levels
// stay worth reaching. A uniform +100 makes level 20 take twenty times as long as
// level 2, and every user hits that wall and leaves.
func Level(xp int) int {
	if xp < 0 {
		xp = 0
	}
	level, remaining := 1, xp
	for {
		need := LevelBase * level
		if remaining < need {
			return level
		}
		remaining -= need
		level++
	}
}

// LevelProgress is how far into the current level an actor is, for a progress
// bar.
type LevelProgress struct {
	Level      int
	IntoLevel  int
	LevelWidth int
	// Fraction is 0..1 and is 0 for a fresh level rather than 1, so a bar at the
	// start of a level reads as empty instead of full.
	Fraction float64
}

func Progress(xp int) LevelProgress {
	lvl := Level(xp)
	// Walk back down to find the level's floor.
	into := xp
	for l := lvl - 1; l >= 1; l-- {
		into -= LevelBase * l
	}
	width := LevelBase * lvl
	if width <= 0 || into < 0 {
		return LevelProgress{Level: lvl, LevelWidth: width}
	}
	return LevelProgress{
		Level:      lvl,
		IntoLevel:  into,
		LevelWidth: width,
		Fraction:   float64(into) / float64(width),
	}
}

// DAY is the streak's unit, and it is a fixed 24 hours in UTC rather than a
// calendar day in the user's local zone.
//
// A local-zone calendar day means "yesterday" depends on where the server is,
// which makes a streak a function of deployment geography — move the instance and
// everybody's streak breaks. §6a.10 has no timezone requirement, so the simplest
// deterministic unit wins and the ambiguity is documented rather than guessed at.
const DAY = 24 * time.Hour

// Streak is a run of consecutive days with at least one earning event.
type Streak struct {
	// Current is the run ending on or before Today.
	Current int
	// Longest is the longest run ever seen in this event set. It is derived like
	// everything else here, so it means "longest in the log we were given", not
	// "longest ever" — a caller with a truncated log gets a truncated answer and
	// must say so in its UI rather than claim a record.
	Longest int
	// Days is the distinct earning dates, ascending. Exported because a caller
	// showing a heatmap needs them and recomputing would invite a second
	// implementation of the same date() rule.
	Days []time.Time
}

// StreakFor computes the streaks for one actor from the audit log.
//
// §6a.10 AND THE PLAN'S NAMED TEST, TestAStreakIsRecomputedFromTheAuditLog: this
// is a VIEW whose first operation is date(at). There is no streak column and no
// increment path, so:
//
//   - delete an event and the streak shrinks;
//   - an event with a different action does not count at all;
//   - the same events always give the same streak.
//
// The one subtlety worth naming: events on the SAME day collapse to one day, so a
// user who does five things in an afternoon has a one-day streak, not a five-day
// one. That is what a streak means, and a version that counted events would be a
// counter wearing a streak's name.
func StreakFor(events []Event, actorID int, today time.Time) Streak {
	// The clock is a PARAMETER, never time.Now(). A function that reads the wall
	// clock cannot be tested for a streak that ended three days ago, and every
	// streak test would have to be written on the day it runs.
	day := func(t time.Time) time.Time {
		u := t.UTC()
		return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	}

	seen := map[time.Time]bool{}
	for _, e := range events {
		if e.ActorID != actorID {
			continue
		}
		if XPFor(e) == 0 {
			continue
		}
		seen[day(e.At)] = true
	}

	days := make([]time.Time, 0, len(seen))
	for d := range seen {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })

	st := Streak{Days: days}
	if len(days) == 0 {
		return st
	}

	// Longest run over the whole set.
	run := 1
	for i := 1; i < len(days); i++ {
		if days[i].Sub(days[i-1]) == DAY {
			run++
		} else {
			if run > st.Longest {
				st.Longest = run
			}
			run = 1
		}
	}
	if run > st.Longest {
		st.Longest = run
	}

	// Current run: walk BACKWARDS from the most recent day, and only count it if
	// it is today or yesterday.
	//
	// Yesterday counts, and that is the whole reason for the check. A user who
	// did nothing today has not lost their streak — the day is not over. A version
	// that required the last day to be exactly today zeroes every streak in the
	// morning, which means a badge anyone can earn disappears once a day.
	last := days[len(days)-1]
	gap := day(today).Sub(last)
	if gap != 0 && gap != DAY {
		// Older than yesterday: the run is over, and only Longest survives.
		return st
	}

	cur := 1
	for i := len(days) - 1; i > 0; i-- {
		if days[i].Sub(days[i-1]) != DAY {
			break
		}
		cur++
	}
	st.Current = cur

	return st
}

// Badge is a named achievement.
//
// THE CRITICAL PROPERTY: Badge carries no access level, no role, and no
// permission. It has a name and a threshold. That is not politeness about §6a.10,
// it is the only way that rule can be enforced structurally — there is no field
// for a badge to grant with, so "a badge grants an access level" is not a bug
// someone can introduce later, it is a field someone would have to add.
type Badge struct {
	// ID is stable and machine-readable.
	ID string
	// Label is what a user sees.
	Label string
	// Threshold is the earning condition, as a function of derived state rather
	// than of XP — because §6a.10 also holds that reward does not grant access, and
	// an XP threshold would make the badge a repackaged counter.
	Threshold func(derived Derived) bool
}

// Derived is everything gamification knows about one actor, all of it computed.
type Derived struct {
	XP                int
	Level             int
	Streak            Streak
	ProposalsApproved int
	IdentSolves       int
	Verifications     int
}

// Derive computes every gamification number for an actor from the audit log.
func Derive(events []Event, actorID int, today time.Time) (Derived, error) {
	xp, err := TotalXP(events, actorID)
	if err != nil {
		return Derived{}, err
	}

	d := Derived{
		XP:     xp,
		Level:  Level(xp),
		Streak: StreakFor(events, actorID, today),
	}
	for _, e := range events {
		if e.ActorID != actorID {
			continue
		}
		switch e.Action {
		case ActionEditApproved:
			d.ProposalsApproved++
		case ActionIdentSolve:
			d.IdentSolves++
		case ActionVerification:
			d.Verifications++
		}
	}
	return d, nil
}

// Earned returns the badge IDs an actor has earned.
func Earned(d Derived, badges []Badge) []string {
	out := []string{}
	for _, b := range badges {
		if b.Threshold != nil && b.Threshold(d) {
			out = append(out, b.ID)
		}
	}
	sort.Strings(out)
	return out
}
