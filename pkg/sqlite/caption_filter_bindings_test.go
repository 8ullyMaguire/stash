package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// stash#6459 -- the caption filter's SQL BINDINGS, as produced by the handler.
//
// ## WHY THIS FILE EXISTS WHEN THERE IS ALREADY A SQL-LEVEL TEST
//
// caption_language_filter_test.go executes the predicate and is the stronger test of behaviour: it proves
// the query returns the right rows. But it supplies its own bindings, so it cannot see whether the handler
// passes the right ones.
//
// That distinction is not academic. The mutation "bind the escaped value to the equality and length()/substr()
// checks instead of the raw value" survived every SQL-level test in that file, because the test's fixture and
// assertions are about ordinary language codes, where the escaped and raw forms are the same string. The
// difference appears only for a value containing '_' or '%'.
//
// So this file drives captionCriterionHandler -- the function the scene filter actually calls -- and
// inspects what it bound. A test that cannot see the production binding is not a test of the production
// binding.

// whereArgs returns the arguments of every where clause the builder collected.
func whereArgs(f *filterBuilder) [][]interface{} {
	var out [][]interface{}
	for _, c := range f.whereClauses {
		out = append(out, c.args)
	}
	return out
}

func TestCaptionCriterionHandlerBindsRawValuesToTheBoundaryChecks(t *testing.T) {
	qb := &sceneFilterHandler{}

	tests := []struct {
		name  string
		value string
		// want is the exact binding list the handler must produce.
		want []interface{}
	}{
		{
			name:  "an ordinary value binds identically escaped and raw",
			value: "en",
			// No LIKE metacharacters, so the pattern is just "en%" and the raw value is "en".
			want: []interface{}{"en%", "en", "en", "en"},
		},
		{
			name:  "the underscore form binds the RAW value to the boundary checks",
			value: "en_GB",
			// THE case. The pattern is the escaped value plus '%' ("en\_GB%"); the other three bindings must
			// be the raw "en_GB", or the equality test compares against a six-character string and the
			// boundary offsets are computed from the wrong length.
			want: []interface{}{`en\_GB%`, "en_GB", "en_GB", "en_GB"},
		},
		{
			name:  "a percent sign is escaped in the pattern only",
			value: "%",
			want:  []interface{}{`\%%`, "%", "%", "%"},
		},
		{
			name:  "a regional value",
			value: "pt-BR",
			want:  []interface{}{"pt-BR%", "pt-BR", "pt-BR", "pt-BR"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &filterBuilder{}

			mod := models.CriterionModifierIncludes
			qb.captionCriterionHandler(&models.StringCriterionInput{
				Value:    tt.value,
				Modifier: mod,
			})(context.Background(), f)

			args := whereArgs(f)
			require.Len(t, args, 1, "expected exactly one where clause, got %d", len(args))
			assert.Equal(t, tt.want, args[0])
		})
	}
}

// TestCaptionCriterionHandlerExcludesBindsTheSameWay pins that the Excludes branch, which uses a NOT IN
// subquery rather than a join, binds identically. It is a separate clause in the code and a separate place
// for the raw/escaped mix-up to reappear.
func TestCaptionCriterionHandlerExcludesBindsTheSameWay(t *testing.T) {
	qb := &sceneFilterHandler{}
	f := &filterBuilder{}

	qb.captionCriterionHandler(&models.StringCriterionInput{
		Value:    "en_GB",
		Modifier: models.CriterionModifierExcludes,
	})(context.Background(), f)

	args := whereArgs(f)
	require.Len(t, args, 1, "expected exactly one where clause, got %d", len(args))
	assert.Equal(t, []any{`en\_GB%`, "en_GB", "en_GB", "en_GB"}, args[0])
}

func TestCaptionCriterionHandlerIgnoresANilCriterion(t *testing.T) {
	// The filter is optional, so a nil criterion must add nothing rather than panicking on Value.
	qb := &sceneFilterHandler{}
	f := &filterBuilder{}

	qb.captionCriterionHandler(nil)(context.Background(), f)

	assert.Empty(t, f.whereClauses, "a nil criterion must not add a where clause")
}
