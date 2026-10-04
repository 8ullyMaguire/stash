package sqlite

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Registers the "sqlite3" driver used below.
	_ "github.com/mattn/go-sqlite3"
)

// stash#6459 -- the scene captions filter must match on language-code BOUNDARIES, not substrings.
//
// ## THE BUG
//
// The filter went through the shared stringListCriterionHandlerBuilder, which matches with
// LIKE '%value%'. That is right for a studio NAME, where a substring is a sensible query, and wrong for a
// language code, because a language code is a delimited token. Verified against SQLite directly:
//
//	'en'    LIKE '%en%'  -> true    correct
//	'en-US' LIKE '%en%'  -> true    correct, and why the substring match was there: a base language must
//	                               select its regional variants
//	'sen'   LIKE '%en%'  -> true    WRONG - Senegal, not English
//	'men'   LIKE '%en%'  -> true    WRONG - Montenegrin
//	'eng'   LIKE '%en%'  -> true    WRONG
//
// So asking for scenes with English subtitles returned scenes whose only subtitles are Senegalese or
// Montenegrin. That is a correctness bug in a filter people rely on to find subtitled scenes.
//
// ## WHY THE TEST RUNS SQL RATHER THAN ASSERTING ON A STRING
//
// The predicate IS the behaviour. Checking that the generated clause contains "substr" would pass for a
// predicate that matched nothing at all. So this executes the real predicate against a real in-memory
// SQLite with real rows, which is the same engine the application uses. The DB dependency is already
// present -- pkg/sqlite imports this driver.
//
// ## THE SUBTLETY, RECORDED BECAUSE I GOT IT WRONG TWICE
//
// Matching en -> {en, en-US, en-GB} needs BOTH boundaries explicit:
//   - a trailing substring match ('%en') drops en-US, which is the case the original substring match
//     existed to serve;
//   - a leading substring match ('%en%') is the original bug, admitting sen and men.
// Only a prefix match plus an explicit subtag-separator check satisfies both.

func newCaptionLangDB(t *testing.T, codes ...string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec("CREATE TABLE video_captions(language_code TEXT)")
	require.NoError(t, err)

	for _, c := range codes {
		_, err = db.Exec("INSERT INTO video_captions VALUES (?)", c)
		require.NoError(t, err)
	}

	return db
}

// matchCaptionsWithBindings is matchCaptions with the four bindings supplied explicitly, so a test can
// substitute a wrong binding and observe the difference.
//
// This exists because one mutation SURVIVED the first version of this file: binding the escaped value to
// the equality and length()/substr() checks, instead of the raw value, changes nothing for any value
// without a LIKE metacharacter -- the escaped and raw forms are the identical string -- so a table of
// ordinary language codes passed either way. It differs only for a value containing '_' or '%'. Binding
// them explicitly makes that difference visible instead of theoretical.
func matchCaptionsWithBindings(t *testing.T, db *sql.DB, like, a, b, c string) []string {
	t.Helper()

	rows, err := db.Query(
		"SELECT language_code FROM video_captions WHERE "+captionLanguageWhere, like, a, b, c)
	require.NoError(t, err)
	defer rows.Close()

	var got []string
	for rows.Next() {
		var code string
		require.NoError(t, rows.Scan(&code))
		got = append(got, code)
	}
	require.NoError(t, rows.Err())
	return got
}

func matchCaptions(t *testing.T, db *sql.DB, value string) []string {
	t.Helper()

	// Four placeholders, and they are deliberately NOT all the same value: the LIKE gets the escaped value
	// suffixed with '%', and the equality/length/substr checks get the RAW value, because those do
	// arithmetic on the real code. Feeding the escaped form to length() is a real bug -- see
	// captionLanguageWhere -- and TestCaptionLanguageBindsRawValuesToTheBoundaryChecks pins it.
	raw := value
	return matchCaptionsWithBindings(t, db, captionLanguageMatch(raw), raw, raw, raw)
}

func TestCaptionLanguageFilterMatchesSubtagBoundaries(t *testing.T) {
	db := newCaptionLangDB(t,
		"en", "en-US", "en-GB", "en_US",
		"fr", "fr-CA",
		"pt", "pt-BR",
		"zh-Hans",
		"de",
		// The three that the old LIKE '%value%' wrongly admitted for "en", plus a short code and a
		// longer one containing it, for the single-character boundary case:
		"sen", "men", "eng",
		"s", "sene",
	)

	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{
			name:  "a base language selects its regional variants",
			value: "en",
			want:  []string{"en", "en-US", "en-GB", "en_US"},
			// THE core case, and the one a trailing-anchor fix would break.
		},
		{
			name:  "french selects fr and fr-CA but not english",
			value: "fr",
			want:  []string{"fr", "fr-CA"},
		},
		{
			name:  "portuguese selects pt and pt-BR",
			value: "pt",
			want:  []string{"pt", "pt-BR"},
		},
		{
			name:  "a script subtag is a boundary too",
			value: "zh",
			want:  []string{"zh-Hans"},
		},
		{
			name:  "a specific regional code selects only itself",
			value: "en-US",
			want:  []string{"en-US"},
			// A user typing en-US wants American English, not every English track.
		},
		{
			name:  "a specific regional code does not select its base language",
			value: "pt-BR",
			want:  []string{"pt-BR"},
		},
		{
			name:  "a code that is a substring of another is not matched by the shorter value",
			value: "sen",
			want:  []string{"sen"},
		},
		{
			name:  "a single-character value still respects boundaries",
			value: "s",
			want:  []string{"s"},
			// Would be ['s','sen'] under a substring match.
		},
		{
			name:  "an unknown language matches nothing",
			value: "xx",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ElementsMatch(t, tt.want, matchCaptions(t, db, tt.value))
		})
	}
}

// TestCaptionLanguageFilterDoesNotMatchUnrelatedCodes is the bug pinned on its own, so a regression is
// unambiguous: nothing here is about which variants SHOULD match.
func TestCaptionLanguageFilterDoesNotMatchUnrelatedCodes(t *testing.T) {
	db := newCaptionLangDB(t, "sen", "men", "eng", "sene", "mengl", "england")

	assert.Empty(t, matchCaptions(t, db, "en"),
		"filtering by 'en' must not return Senegalese, Montenegrin or malformed codes")
	assert.Empty(t, matchCaptions(t, db, "me"))
	assert.Empty(t, matchCaptions(t, db, "ng"))
}

// TestCaptionLanguageFilterEscapesWildcards pins that a user's value cannot become a wildcard. Without
// the escaping, a filter value of "%" matches every caption in the library, and "en_GB" -- an underscore
// form that real tools emit -- matches "enXGB" too.
func TestCaptionLanguageFilterEscapesWildcards(t *testing.T) {
	// en_GB is the underscore form real tools emit; enXGB is what an UNESCAPED underscore would also match.
	db := newCaptionLangDB(t, "en", "en-GB", "en_GB", "enXGB", "%", "anything")

	// The escaped value matches ONLY the code that is literally a percent sign, not every caption. Without
	// the escaping this returned the entire table.
	assert.Equal(t, []string{"%"}, matchCaptions(t, db, "%"),
		"a literal %% in the filter value must match only that code, not every caption")
	assert.ElementsMatch(t, []string{"en_GB"}, matchCaptions(t, db, "en_GB"),
		"an escaped underscore must match the underscore form only, not any single character")
}

// TestCaptionLanguageMatchIsIdempotentOnEscaping guards the escaper itself, which is otherwise only
// exercised indirectly.
func TestCaptionLanguageMatchBuildsTheLikePattern(t *testing.T) {
	// The trailing % is what makes a base language select its regional variants; the escaping is what stops
	// a user's value acting as a wildcard.
	assert.Equal(t, "en%", captionLanguageMatch("en"))
	assert.Equal(t, `en-US%`, captionLanguageMatch("en-US"))
	assert.Equal(t, `\%%`, captionLanguageMatch("%"))
	assert.Equal(t, `\_%`, captionLanguageMatch("_"))
	assert.Equal(t, `en\_GB%`, captionLanguageMatch("en_GB"))
}

// TestCaptionLanguageBindsRawValuesToTheBoundaryChecks pins WHICH binding goes where.
//
// Swapping the raw value for the escaped one in the equality and length()/substr() checks is invisible for
// any value without a LIKE metacharacter, which is why it survived the first version of this file. For a
// value containing '_' the escaped form is two characters longer, so `language_code = 'en\\_GB'` is false
// for the code `en_GB`, and the boundary offsets are computed from the wrong length.
//
// The correct bindings must find the underscore form; the wrong ones must not. If the second assertion ever
// starts failing, the production bindings have been changed to match it.
func TestCaptionLanguageBindsRawValuesToTheBoundaryChecks(t *testing.T) {
	db := newCaptionLangDB(t, "en_GB", "en-GB", "en")

	const value = "en_GB"
	raw := value
	escaped := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(value)

	assert.Equal(t, []string{"en_GB"},
		matchCaptionsWithBindings(t, db, captionLanguageMatch(raw), raw, raw, raw),
		"binding the raw value to the equality and boundary checks must find the underscore form")

	assert.Empty(t,
		matchCaptionsWithBindings(t, db, captionLanguageMatch(raw), escaped, escaped, escaped),
		"binding the ESCAPED value to the boundary checks must NOT match -- that is the bug this pins")
}
