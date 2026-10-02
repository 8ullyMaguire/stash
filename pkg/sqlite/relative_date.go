package sqlite

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/models"
)

// Relative dates for date filters -- #3450.
//
// ## WHY THIS IS HERE AND NOT IN THE UI
//
// The filter VALUE is a string that reaches the database layer from the API, so a user
// typing "last 30 days" sends that string to `getDateWhereClause`. Before this file the
// resolution happened nowhere, and the result was measured rather than assumed:
//
//	models.ParseDate("last 30 days")  ->  0001-01-01, err != nil
//	clause = "scenes.date > ?"  arg[0] = Date{0001-01-01}
//
// So the filter built VALID SQL, bound the ZERO DATE, and returned rows that were never
// what the user asked for -- no error, no empty result to notice. That is the failure this
// exists to remove: a filter that silently answers the wrong question is strictly worse
// than one that refuses.
//
// ## THE VOCABULARY, and why it is small
//
// `last N days`, `last N weeks/months/years`, `last week/month/year`, `this
// week/month/year`. Deliberately not more. A phrase this file does not know -- "last
// fortnight", "next 7 days" -- is REFUSED rather than guessed at, because returning a
// plausible date for an unknown phrase gives the user rows with no way to tell the answer
// was invented. Refusal surfaces as a filter error the user can see and correct.
//
// ## WHY WHOLE LOCAL DAYS, AND NOT ROLLING HOURS
//
// ONE RULE FOR EVERY UNIT: a range starts at midnight, N units back, and ends at the end
// of today. So "last 7 days" and "last week" resolve to the SAME date -- which is the point
// of not special-casing either one. "last 1 day" is yesterday through the end of today.
//
// Two deliberate consequences, both chosen so a scene is never silently EXCLUDED from a
// window it plainly belongs to:
//
//   - The range includes today, so `RelativeDateEnd` runs to 23:59:59 rather than to now.
//   - It starts N units back rather than N-1, so "last 1 day" is yesterday rather than
//     today alone. The cost is that "last 7 days" covers eight calendar days; the benefit
//     is that the wording nobody has to learn -- "last N days" -- cannot silently exclude
//     something from N days ago.
//
// `now` is a parameter rather than `time.Now()` so the behaviour is testable; a test
// reading the wall clock is correct on one day and wrong on another.
var relativeDayPattern = regexp.MustCompile(`(?i)^last\s+(\d+)\s*(day|week|month|year)s?$`)

// Unit-less "last 7" means days, because that is what a bare number next to "last"
// means to someone typing it.
var relativeBarePattern = regexp.MustCompile(`(?i)^last\s+(\d+)$`)

var relativeNamed = map[string]func(time.Time) time.Time{
	"week":  func(t time.Time) time.Time { return t.AddDate(0, 0, -7) },
	"month": func(t time.Time) time.Time { return t.AddDate(0, -1, 0) },
	"year":  func(t time.Time) time.Time { return t.AddDate(-1, 0, 0) },
}

// ResolveRelativeDate converts a relative filter value into the ABSOLUTE date its range
// starts at.
//
// The second return value is false when `input` is not a relative phrase this file
// understands -- including when it is an ordinary absolute date, which must pass through
// untouched so that every existing filter value keeps working unchanged.
func ResolveRelativeDate(input string, now time.Time) (time.Time, bool) {
	s := strings.TrimSpace(input)
	if s == "" {
		return time.Time{}, false
	}

	// An absolute date is not ours to reinterpret. Checked first and cheaply, because a
	// resolver that swallows every string would silently rewrite the existing corpus of
	// saved filters.
	if _, err := models.ParseDate(s); err == nil {
		return time.Time{}, false
	}

	if m := relativeBarePattern.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 {
			return time.Time{}, false
		}
		return startOfDay(now).AddDate(0, 0, -n), true
	}

	if m := relativeDayPattern.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 {
			return time.Time{}, false
		}
		unit := strings.ToLower(m[2])
		if unit == "day" {
			return startOfDay(now).AddDate(0, 0, -n), true
		}
		shift, ok := relativeNamed[unit]
		if !ok {
			return time.Time{}, false
		}
		return startOfDay(shift(now)), true
	}

	switch strings.ToLower(s) {
	case "last week":
		return startOfDay(relativeNamed["week"](now)), true
	case "last month":
		return startOfDay(relativeNamed["month"](now)), true
	case "last year":
		return startOfDay(relativeNamed["year"](now)), true
	}

	return time.Time{}, false
}

// RelativeDateEnd is the INCLUSIVE end of a relative range: the end of the current local
// day.
//
// It is a separate function from ResolveRelativeDate because the two answers are not the
// same value, and the existing BETWEEN path would otherwise exclude today. `getDateWhereClause`
// defaults an absent upper bound to `time.Now().AddDate(0, 0, 1)` as an RFC3339
// TIMESTAMP, which is midnight tomorrow -- so a scene from 23:00 today falls OUTSIDE
// "the last 30 days" under that default while a user plainly expects it inside.
func RelativeDateEnd(input string, now time.Time) (time.Time, bool) {
	if _, ok := ResolveRelativeDate(input, now); !ok {
		return time.Time{}, false
	}
	e := startOfDay(now).AddDate(0, 0, 1).Add(-time.Second)
	return e, true
}

// IsRelativeDate reports whether `input` is a phrase this file resolves.
//
// Exposed so the GraphQL layer can REJECT an unresolvable value with an error naming the
// offending text, rather than passing it down to become the zero Date. See the call site
// in `getDateWhereClause`.
func IsRelativeDate(input string) bool {
	_, ok := ResolveRelativeDate(input, time.Now())
	return ok
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// ErrUnresolvableFilterValue is returned when a filter value is neither an absolute date
// this package can parse nor a relative phrase ResolveRelativeDate understands.
//
// The distinction matters. Today the error is DISCARDED (`valueDate, _ := models.ParseDate`)
// and the zero Date is bound, which is the bug. Making it a returned error means a filter
// the user typed badly says so, instead of quietly matching the wrong rows.
var ErrUnresolvableFilterValue = errors.New("filter value is neither a date nor a supported relative phrase")
// validateDateCriterion accepts an absolute date OR a relative phrase this package
// understands, and rejects anything else WITH THE VALUE NAMED.
//
// # WHY THIS EXISTS RATHER THAN A CHECK INSIDE getDateWhereClause
//
// `criterionHandlerFunc.handle` returns nothing, so an error raised while building a WHERE
// fragment has nowhere to go. The seam that already exists for exactly this is each
// handler's `validate() error` -- `performer_filter.go` uses it to reject an unparseable
// `Birthdate.Value` by name. So the check lives there, once, for every date criterion the
// filter surface exposes.
//
// The consequence is the point of #3450: BEFORE this, a value nobody could parse became the
// zero Date and produced valid SQL over the wrong rows. AFTER it, the filter request fails
// with the offending text in the message, which is the difference between a filter that
// answers the wrong question quietly and one that says it cannot answer.
func validateDateCriterion(name string, c *models.DateCriterionInput) error {
	if c == nil || c.Value == "" {
		return nil
	}

	// The NULL modifiers carry no value; requiring one would reject a valid
	// "date is null" filter.
	switch c.Modifier {
	case models.CriterionModifierIsNull, models.CriterionModifierNotNull:
		return nil
	}

	if _, err := models.ParseDate(c.Value); err == nil {
		return nil
	}

	if _, ok := ResolveRelativeDate(c.Value, time.Now()); ok {
		return nil
	}

	return fmt.Errorf("invalid %s value %q: not a date and not a supported relative "+
		"phrase (try \"last 7 days\", \"last month\", \"this year\")", name, c.Value)
}
