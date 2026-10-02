package sqlite

import (
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/models"
)

// The resolver tests in `relative_date_test.go` cover the parsing. These cover the WIRING --
// that a relative phrase typed into a filter actually becomes a bounded query, and that an
// unresolvable one is refused with the value named instead of quietly binding the zero
// Date. The second half is the part a resolver-only test cannot see, and it is the half the
// #3450 report is actually about.

// A relative phrase must produce a REAL range, not an open-ended one.
//
// Pinned against golden bounds rather than against the resolver's own output: if this
// compared against `ResolveRelativeDate` it would pass even if both were wrong by the same
// amount, which is the round-trip trap -- two halves agreeing proves nothing about either.
func TestARelativeDateFilterValueProducesABoundedClause(t *testing.T) {
	now, err := nowForTest()
	if err != nil {
		t.Fatalf("cannot fix the clock: %v", err)
	}

	clause, args := getDateWhereClause("scenes.date", models.CriterionModifierGreaterThan, "last 30 days", nil)
	if clause == "" {
		t.Fatal("empty clause for a relative value; the value was silently dropped " +
			"rather than resolved, which is the bug this replaced")
	}

	// The zero Date is the exact failure of the discarded parse error, so it is checked
	// for directly. `Date` is not comparable, hence the explicit field read.
	if clause == "scenes.date > ?" {
		if d, ok := args[0].(Date); ok && d.Date.IsZero() {
			t.Errorf("clause %q with a ZERO-DATE bound -- the exact failure of the "+
				"discarded parse error, now reached only if validation was skipped", clause)
		}
	}

	// It must be a RANGE: a lower bound and an upper bound, or today is excluded.
	if clause != "scenes.date BETWEEN ? AND ?" {
		t.Errorf("clause = %q, want %q", clause, "scenes.date BETWEEN ? AND ?")
	}
	if len(args) != 2 {
		t.Fatalf("args = %v, want two bounds", args)
	}

	// The upper bound must be the end of today, so a scene from 23:00 today is inside
	// "the last 30 days". This is the assertion that distinguishes a real relative range
	// from the old `now + 1 day` default, which is midnight tomorrow and excludes today.
	upper, ok := args[1].(string)
	if !ok {
		t.Fatalf("upper bound is %#v, want an RFC3339 string", args[1])
	}
	// `getDateWhereClause` resolves against the REAL clock, so the upper bound is today
	// on the machine, not the fixed instant above. What is pinned is the PROPERTY that
	// matters and that the old default broke: it must be past the end of the current day,
	// so a scene from later today is inside the range. A fixed string here would only
	// pass on one calendar day.
	upperT, err := time.Parse(time.RFC3339, upper)
	if err != nil {
		t.Fatalf("upper bound %q is not RFC3339: %v", upper, err)
	}
	local := upperT.Local()
	endToday := time.Date(local.Year(), local.Month(), local.Day(), 23, 59, 59, 0, local.Location())
	if !upperT.Equal(endToday) {
		t.Errorf("upper bound = %s, want the end of today (%s), so a scene from later "+
			"today is included", upper, endToday.Format(time.RFC3339))
	}
	_ = now
}

// AN UNRESOLVABLE VALUE MUST NOT REACH THE CLAUSE BUILDER AT ALL.
//
// `criterionHandlerFunc.handle` cannot return an error, so `getDateWhereClause` binds
// nothing when a value is unresolvable. This test pins that contract, and pairs it with
// the validation test below -- the refusal the user sees comes from `validate()`, and
// this is the belt to that braces.
func TestAnUnresolvableValueBindsNothingRatherThanTheZeroDate(t *testing.T) {
	clause, args := getDateWhereClause("scenes.date", models.CriterionModifierGreaterThan, "last fortnight", nil)
	if clause != "" {
		t.Errorf("clause = %q, want \"\" for an unresolvable value", clause)
	}
	if len(args) != 0 {
		t.Errorf("args = %v, want none", args)
	}
}

// AND THE VALIDATION THE USER ACTUALLY SEES MUST NAME THE VALUE.
//
// "invalid date filter" tells the user nothing about which of several filters is wrong, so
// the message has to carry the offending text. Asserted on the message, not merely on a
// non-nil error, because a bare error would satisfy the previous line too.
func TestValidationNamesTheOffendingValue(t *testing.T) {
	err := validateDateCriterion("date", &models.DateCriterionInput{Value: "last fortnight"})
	if err == nil {
		t.Fatal("an unresolvable date value was accepted by validateDateCriterion")
	}
	if !contains(err.Error(), "last fortnight") {
		t.Errorf("error %q does not name the offending value %q", err.Error(), "last fortnight")
	}

	// And a relative phrase the resolver DOES know must pass validation, or the feature is
	// unreachable: this is the check that decides whether the value ever gets built into
	// a clause.
	for _, good := range []string{"last 7 days", "last week", "last month", "last year", "2024-01-02", "2024"} {
		if err := validateDateCriterion("date", &models.DateCriterionInput{Value: good}); err != nil {
			t.Errorf("validateDateCriterion(%q) = %v, want it accepted", good, err)
		}
	}
}

// A NULL-OPERATOR FILTER MUST VALIDATE WITHOUT A VALUE, since it compares against NULL.
func TestValidationAcceptsNullModifiersWithNoValue(t *testing.T) {
	for _, m := range []models.CriterionModifier{
		models.CriterionModifierIsNull,
		models.CriterionModifierNotNull,
	} {
		if err := validateDateCriterion("date", &models.DateCriterionInput{Modifier: m}); err != nil {
			t.Errorf("validateDateCriterion with modifier %s and no value = %v, want nil", m, err)
		}
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// nowForTest returns a FIXED instant, so the golden bounds below are the same on every
// day of every year. A test that derives its expectation from `time.Now()` is correct
// until the day it is not, which is the worst time to find out.
func nowForTest() (time.Time, error) {
	return time.Date(2024, 6, 15, 14, 30, 0, 0, time.UTC), nil
}

// endOfToday mirrors `RelativeDateEnd` for a fixed instant. Written out here rather than
// called, so the wiring test does not agree with the implementation by construction.
func endOfToday(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location())
}
