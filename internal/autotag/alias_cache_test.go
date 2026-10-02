package autotag

import (
	"testing"

	"github.com/stashapp/stash/pkg/match"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stretchr/testify/assert"
)

// #2507, found while enabling performer aliases: `getStudioTaggers` passed `cache` to the
// canonical-name tagger and OMITTED it from every alias tagger.
//
//	ret = append(ret, tagger{ID: p.ID, Type: "studio", Name: a})     // no cache
//
// The effect is a performance bug, not a crash -- `Tagger.Cache` is non-nil in production,
// and a tagger with a nil cache re-parses the paths itself -- so nothing noticed. It is
// also invisible to any assertion on names or IDs, which is why `getTagTaggers`, checked at
// the same time, is the control: it already had `cache: cache` on both taggers.
//
// The asymmetry is the tell. Three sibling builders, two identical, one not.

// EVERY STUDIO TAGGER MUST CARRY THE CACHE, canonical name and aliases alike.
func TestStudioAliasTaggersCarryTheCache(t *testing.T) {
	t.Parallel()

	sentinel := &match.Cache{}
	studio := &models.Studio{ID: 3, Name: "Studio A"}

	got := getStudioTagger(studio, []string{"Alias One", "Alias Two"}, sentinel)

	assert.Len(t, got, 3, "expected the canonical name plus two aliases")
	for _, tt := range got {
		assert.Same(t, sentinel, tt.cache,
			"studio tagger %q has a nil or different cache; aliases must share the "+
				"parent's so path parsing is not repeated", tt.Name)
	}
}

// AND TAGS, WHICH ALREADY DID THIS, ARE PINNED TOO.
//
// A regression test only for the code that was broken would leave the correct sibling
// unpinned, so the next omission would land there. This is the control case.
func TestTagAliasTaggersCarryTheCache(t *testing.T) {
	t.Parallel()

	sentinel := &match.Cache{}
	tag := &models.Tag{ID: 4, Name: "Tag A"}

	got := getTagTaggers(tag, []string{"Alias One"}, sentinel)

	assert.Len(t, got, 2)
	for _, tt := range got {
		assert.Same(t, sentinel, tt.cache, "tag tagger %q must carry the cache", tt.Name)
	}
}

// AND THE THREE SIBLINGS MUST AGREE ON THE WHOLE SHAPE, not just the cache.
//
// Asserting each function separately pins three facts and misses the class of defect, which
// is a builder that differs from its siblings. So this compares them against each other:
// same ID, same Type-as-parent, same cache, one tagger per name.
func TestAllThreeAliasBuildersAgreeOnShape(t *testing.T) {
	t.Parallel()

	cache := &match.Cache{}
	aliases := []string{"Alias One", "Alias Two"}

	performer := getPerformerTaggers(
		&models.Performer{ID: 1, Name: "P", Aliases: models.NewRelatedStrings(aliases)}, cache)
	studio := getStudioTagger(&models.Studio{ID: 2, Name: "S"}, aliases, cache)
	tag := getTagTaggers(&models.Tag{ID: 3, Name: "T"}, aliases, cache)

	builders := map[string][]tagger{"performer": performer, "studio": studio, "tag": tag}

	for name, taggers := range builders {
		t.Run(name, func(t *testing.T) {
			assert.Len(t, taggers, 3, "canonical name plus %d aliases", len(aliases))
			assert.Equal(t, name, taggers[0].Type, "the first tagger must be the canonical one")
			for _, tt := range taggers {
				assert.Same(t, cache, tt.cache, "tagger %q lost the cache", tt.Name)
				assert.NotZero(t, tt.ID, "tagger %q has a zero id", tt.Name)
			}
		})
	}

	// The canonical name must be first in ALL of them, so the ordering contract holds
	// uniformly rather than per-builder by accident.
	assert.Equal(t, "P", performer[0].Name)
	assert.Equal(t, "S", studio[0].Name)
	assert.Equal(t, "T", tag[0].Name)
}
