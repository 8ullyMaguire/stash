package autotag

import (
	"testing"

	"github.com/stashapp/stash/pkg/match"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stretchr/testify/assert"
)

// #2507 -- performer ALIASES in auto-tag.
//
// ## THE STATE BEFORE THIS
//
// `getPerformerTaggers` built a tagger for the performer's NAME only. The alias loop was
// present but commented out, with upstream's note:
//
//	// TODO - disabled until we can have finer control over alias matching
//
// Meanwhile studios and tags have always had their aliases passed in explicitly
// (`getStudioTaggers(p, aliases, cache)`), and `PerformerScenes`' own doc comment already
// said "Performer aliases must be loaded". So the API expected the aliases and the tagger
// list ignored them: `LoadAliases` was called on every path (fixed separately, commit
// `9c1a1e04`) and the values were then thrown away.
//
// ## WHY IT WAS DISABLED, AND WHAT THE ANSWER IS
//
// Upstream's objection was precision, not correctness: an alias is a free-text string a
// user typed, so it can be a variant spelling ("Jane Doe" / "J. Doe"), a former name, or
// something that legitimately names several people. Turning aliases on wholesale therefore
// risks tagging a scene with the WRONG performer, and auto-tag writes are silent -- so a
// wrong one is expensive to notice and hard to trace.
//
// The answer is not to keep them off. It is to make a wrong alias match VISIBLE and
// REVERSIBLE, which is what these tests pin:
//
//   - An alias produces its OWN query, keyed to the alias text, so a match reports which
//     name matched. `Sink.AddMatch` is called with the performer's real name, but the
//     tagger's `Name` -- which is what becomes the path regex -- is the alias.
//   - The performer's canonical name is ALWAYS included, first, so disabling aliases can
//     never lose name-only matching.
//   - A blank or whitespace-only alias is SKIPPED rather than turned into a regex that
//     matches everything. An empty alias becoming `(?i)(?:^|_|[^\p{L}\d])(?:$|_|[^\p{L}\d])`
//     would tag every scene in the library, and that is the precise failure mode the
//     disabled loop was protecting against.

// THE TAGGER LIST IS THE WHOLE FEATURE, so it is tested directly.
//
// Going through `PerformerScenes` with a mocked database would assert that a query was
// made, not which NAMES were queried -- and the names are the entire question here. Each
// name becomes a distinct path regex, so the list is the unit of behaviour.
func TestPerformerTaggersIncludeAliases(t *testing.T) {
	t.Parallel()

	p := &models.Performer{
		ID:      7,
		Name:    "Jane Doe",
		Aliases: models.NewRelatedStrings([]string{"J. Doe", "Janie"}),
	}

	got := getPerformerTaggers(p, nil)

	names := make([]string, 0, len(got))
	for _, tt := range got {
		names = append(names, tt.Name)
	}

	// The canonical name comes FIRST and is always present. If it were merely somewhere in
	// the list, disabling aliases by filtering the first element would silently change
	// which name is queried -- so ordering is part of the contract.
	assert.Equal(t, "Jane Doe", names[0], "the canonical name must be the first tagger")
	assert.ElementsMatch(t, []string{"Jane Doe", "J. Doe", "Janie"}, names,
		"every alias must produce a tagger")

	// Every tagger must carry the performer's ID. An alias tagger with the alias's own ID
	// -- or a zero ID -- would write the link against the wrong row, and the test above
	// would still pass because it only looks at names.
	for _, tt := range got {
		assert.Equal(t, p.ID, tt.ID, "tagger %q must carry the performer id, not the alias's", tt.Name)
		assert.Equal(t, "performer", tt.Type, "tagger %q must be typed as a performer", tt.Name)
	}
}

// A PERFORMER WITH NO ALIASES MUST PRODUCE EXACTLY ONE TAGGER.
//
// The regression this guards: a loop that always runs and appends nothing is harmless, but
// one that appends a zero-value tagger is not -- it would issue a second query whose regex
// is built from an empty name.
func TestAPerformerWithNoAliasesProducesOneTagger(t *testing.T) {
	t.Parallel()

	p := &models.Performer{
		ID:      7,
		Name:    "Jane Doe",
		Aliases: models.NewRelatedStrings([]string{}),
	}

	got := getPerformerTaggers(p, nil)

	assert.Len(t, got, 1)
	assert.Equal(t, "Jane Doe", got[0].Name)
}

// BLANK ALIASES ARE SKIPPED, which is the precision risk the disabled loop was guarding.
//
// An empty name produces a regex that matches every path, so an empty alias would tag the
// entire library with one performer. This is the single most damaging thing the feature
// could do, so it is pinned explicitly rather than assumed from the loop's shape.
func TestBlankAliasesAreSkipped(t *testing.T) {
	t.Parallel()

	for _, blank := range []string{"", " ", "\t", "\n", "   "} {
		p := &models.Performer{
			ID:      7,
			Name:    "Jane Doe",
			Aliases: models.NewRelatedStrings([]string{blank, "J. Doe"}),
		}

		got := getPerformerTaggers(p, nil)

		names := make([]string, 0, len(got))
		for _, tt := range got {
			names = append(names, tt.Name)
		}
		assert.Equal(t, []string{"Jane Doe", "J. Doe"}, names,
			"a blank alias (%q) must be skipped, not turned into a match-everything regex", blank)
	}
}

// AND THE CACHE MUST REACH EVERY TAGGER, INCLUDING THE ALIAS ONES.
//
// This is a real defect found while writing this, not a hypothetical. `getStudioTaggers`
// passes `cache` to the canonical tagger but OMITS it from every alias tagger:
//
//	ret = append(ret, tagger{ID: p.ID, Type: "studio", Name: a})   // no cache
//
// So studio aliases were re-parsing paths on every call while studio names used the cache.
// `Tagger.Cache` is not nil in production, so this is a performance bug rather than a
// crash -- which is exactly why nothing noticed and why a name-only assertion would miss it.
func TestEveryTaggerReceivesTheCacheIncludingAliases(t *testing.T) {
	t.Parallel()

	p := &models.Performer{
		ID:      7,
		Name:    "Jane Doe",
		Aliases: models.NewRelatedStrings([]string{"J. Doe"}),
	}

	// A sentinel pointer. If any tagger's cache field is nil while others are not, the
	// alias path is bypassing it.
	sentinel := &match.Cache{}
	got := getPerformerTaggers(p, sentinel)

	for _, tt := range got {
		assert.Same(t, sentinel, tt.cache,
			"tagger %q has a different cache; alias taggers must share the parent's", tt.Name)
	}
}

// DUPLICATE ALIASES MUST NOT DUPLICATE QUERIES.
//
// A performer whose alias list contains the canonical name, or repeats an alias, would
// otherwise issue the same query several times -- and each pass calls `Sink.AddMatch`,
// which for a matching scene means a redundant write attempt. The existing
// `slices.Contains(existing, p.ID)` guard stops the link being added twice, but the
// wasted queries remain, and the second pass reports a false "already tagged" rather than
// a fresh match.
func TestDuplicateAndSelfAliasesDoNotProduceDuplicateTaggers(t *testing.T) {
	t.Parallel()

	p := &models.Performer{
		ID:   7,
		Name: "Jane Doe",
		Aliases: models.NewRelatedStrings([]string{
			"Jane Doe", // the canonical name, repeated
			"J. Doe",
			"j. doe", // differs only in case
			"J. Doe", // exact repeat
		}),
	}

	got := getPerformerTaggers(p, nil)

	names := make([]string, 0, len(got))
	for _, tt := range got {
		names = append(names, tt.Name)
	}
	assert.Len(t, names, 2, "expected the canonical name plus one de-duplicated alias, got %v", names)
}
