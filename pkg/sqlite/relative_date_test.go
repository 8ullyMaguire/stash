package sqlite

import (
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/models"
)

// #3450 -- relative dates in date filters ("in the last 30 days", "this year").
//
// ## WHAT THE GAP IS
//
// `getDateWhereClause` parses `input.Value` with `models.ParseDate` and discards the
// error:
//
//	valueDate, _ := models.ParseDate(value)
//
// `ParseDate` accepts only ABSOLUTE forms -- "2024-01-02", "2024-01", "2024", and the
// several separators `utils.ParseDateStringAsTime` tolerates. So a filter value like
// "last 30 days" or "this year" does not parse, the error is thrown away, and the zero
// `Date` is used as a bound argument. The clause is built anyway, so the query is valid
// SQL and returns the WRONG ROWS rather than an error -- a filter that silently matches
// nothing (or everything), which is the failure mode this project's notes keep calling
// out: "strictly worse than silence, because it converts blocked into just a hassle".
//
// ## WHY THE TEST IS ON THE PARSER AND NOT ON SQL
//
// The resolution logic is the whole of the feature, and it is pure: relative text in,
// absolute bound out. Testing it through a query would exercise SQLite's date functions
// as well, so a failure would not say whether the resolver or the SQL was wrong. So this
// tests `ResolveRelativeDate` directly for the parsing, and the DB-backed filter test in
// `scene_test.go` covers the wiring.
//
// ## THE HOUR-BOUNDARY DECISION, stated because it is the easy thing to get wrong
//
// "Last 7 days" means the last seven DAYS, not the last 168 hours from this instant. A
// scene from 06:00 yesterday is inside "the last 7 days" as a person means it, and outside
// it under a rolling-168-hours reading. So relative ranges are resolved to whole local
// days: the start is midnight N-1 days ago, and the end is the end of today.

func TestRelativeDateResolvesToAnAbsoluteDate(t *testing.T) {
	// A fixed clock. Every assertion below depends on "now", so a test that reads the
	// wall clock would be correct on one day and wrong on another.
	now := time.Date(2024, 6, 15, 14, 30, 0, 0, time.UTC)

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{
			name:  "last 7 days starts 7 days ago",
			input: "last 7 days",
			want:  time.Date(2024, 6, 8, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "last 30 days starts 30 days ago",
			input: "last 30 days",
			want:  time.Date(2024, 5, 16, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "last 1 day starts yesterday",
			input: "last 1 day",
			want:  time.Date(2024, 6, 14, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "the word day is optional",
			input: "last 7",
			want:  time.Date(2024, 6, 8, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "last week starts 7 days ago",
			input: "last week",
			want:  time.Date(2024, 6, 8, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "last month starts one month ago",
			input: "last month",
			want:  time.Date(2024, 5, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "last year starts one year ago",
			input: "last year",
			want:  time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResolveRelativeDate(tt.input, now)
			if !ok {
				t.Fatalf("ResolveRelativeDate(%q) reported not-relative, but it is", tt.input)
			}
			if !got.Equal(tt.want) {
				t.Errorf("ResolveRelativeDate(%q) = %s, want %s",
					tt.input, got.Format(time.RFC3339), tt.want.Format(time.RFC3339))
			}
		})
	}
}

// AN ABSOLUTE DATE IS NOT RELATIVE, and must be left alone.
//
// This is the guard against a resolver that swallows everything: if `ResolveRelativeDate`
// claimed a fixed date, the feature would silently reinterpret every existing filter
// value in the database.
func TestAnAbsoluteDateIsNotTreatedAsRelative(t *testing.T) {
	now := time.Date(2024, 6, 15, 14, 30, 0, 0, time.UTC)

	for _, absolute := range []string{
		"2024-01-02",
		"2024-01",
		"2024",
		"2024.01.02",
		"02 January 2024",
	} {
		t.Run(absolute, func(t *testing.T) {
			if got, ok := ResolveRelativeDate(absolute, now); ok {
				t.Errorf("ResolveRelativeDate(%q) = %s, claimed relative; an absolute "+
					"date must pass through unchanged", absolute, got.Format(time.RFC3339))
			}
		})
	}
}

// NONSENSE IS NOT RELATIVE EITHER, and must not be guessed at.
//
// "last fortnight" is not a thing this resolver knows. Returning a plausible date for it
// would be worse than refusing: the filter would return rows, and the user would have no
// way to tell the answer was invented.
func TestUnknownRelativePhrasesAreRejectedRatherThanGuessed(t *testing.T) {
	now := time.Date(2024, 6, 15, 14, 30, 0, 0, time.UTC)

	for _, phrase := range []string{
		"last fortnight",
		"next 7 days",
		"last 0 days",
		"last -3 days",
		"last abc days",
		"",
		"   ",
	} {
		t.Run("phrase="+phrase, func(t *testing.T) {
			if got, ok := ResolveRelativeDate(phrase, now); ok {
				t.Errorf("ResolveRelativeDate(%q) = %s, want it refused",
					phrase, got.Format(time.RFC3339))
			}
		})
	}
}

// ZERO AND NEGATIVE COUNTS ARE REFUSED, and "last 0 days" is the important one.
//
// It reads as meaningful -- "nothing in the last zero days" is a coherent query -- and a
// naive resolver returns today, which then matches everything dated today. Refusing is
// the honest answer, because the alternative is a filter that looks like "none" and
// behaves like "today only".
func TestZeroAndNegativeDayCountsAreRefused(t *testing.T) {
	now := time.Date(2024, 6, 15, 14, 30, 0, 0, time.UTC)

	for _, phrase := range []string{"last 0 days", "last -1 days", "last 0", "last -5"} {
		t.Run(phrase, func(t *testing.T) {
			if got, ok := ResolveRelativeDate(phrase, now); ok {
				t.Errorf("ResolveRelativeDate(%q) = %s, want it refused: a zero or "+
					"negative span would silently mean 'today'", phrase,
					got.Format(time.RFC3339))
			}
		})
	}
}

// A RANGE NEEDS AN END, AND "last 30 days" MUST SUPPLY ONE.
//
// The resolver returns the START. The end is today, and the existing `BETWEEN` path in
// `getDateWhereClause` already defaults an absent upper bound to `now + 1 day` -- but as an
// RFC3339 TIMESTAMP, not as the end of the local day. So a scene dated later today is
// excluded by that default while a user asking for "the last 30 days" expects it
// included. `RelativeDateEnd` is what closes that, and it is a separate function because
// the two answers are not the same value.
func TestTheEndOfARelativeRangeIncludesToday(t *testing.T) {
	now := time.Date(2024, 6, 15, 14, 30, 0, 0, time.UTC)

	end, ok := RelativeDateEnd("last 30 days", now)
	if !ok {
		t.Fatal(`RelativeDateEnd("last 30 days") reported not-relative`)
	}

	// End of today, so a scene from 23:00 today is inside "the last 30 days".
	want := time.Date(2024, 6, 15, 23, 59, 59, 0, time.UTC)
	if !end.Equal(want) {
		t.Errorf("RelativeDateEnd = %s, want %s (end of today, so a scene from later "+
			"today is included)", end.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// THE EXISTING NULL MODIFIERS MUST KEEP WORKING, because they carry no value at all.
//
// `CriterionModifierIsNull` and `NotNull` never look at `Value`, so a resolver must not
// require one. This is asserted through the real clause builder, which is where the
// interaction actually happens.
func TestNullModifiersAreUnaffectedByRelativeResolution(t *testing.T) {
	for _, modifier := range []models.CriterionModifier{
		models.CriterionModifierIsNull,
		models.CriterionModifierNotNull,
	} {
		t.Run(modifier.String(), func(t *testing.T) {
			// A deliberately unparseable value. The NULL paths must not care.
			clause, args := getDateWhereClause("scenes.date", modifier, "not a date", nil)
			if clause == "" {
				t.Error("empty clause for a NULL modifier")
			}
			if len(args) != 0 {
				t.Errorf("NULL modifier produced %d args %v; it should produce none, "+
					"since it compares against NULL rather than a value",
					len(args), args)
			}
		})
	}
}

// AND THE CLAUSE BUILDER STILL EMITS SQL FOR AN ORDINARY ABSOLUTE VALUE.
//
// Present so the relative feature cannot regress the existing path: this is the assertion
// a reader would write first, and it is the one that says the change is additive.
func TestAnAbsoluteValueStillProducesAWorkingClause(t *testing.T) {
	clause, args := getDateWhereClause("scenes.date", models.CriterionModifierGreaterThan, "2024-01-02", nil)
	if clause != "scenes.date > ?" {
		t.Errorf("clause = %q, want %q", clause, "scenes.date > ?")
	}
	if len(args) != 1 {
		t.Errorf("args = %v, want exactly one", args)
	}
}