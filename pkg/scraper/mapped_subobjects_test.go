package scraper

// stash#7263: a mapped scraper returns each sub-object attribute as its own
// list of strings, and process joins those lists by index. cleanResults
// deduplicated each attribute's list INDEPENDENTLY, so the two lists collapsed
// differently and index N stopped meaning the same object.
//
// Ten performers with two genders produced ten names and two genders; gender
// index 1 then attached to whichever performer landed at index 1, and everyone
// after the first duplicate got somebody else's attributes. An empty value
// caused the same shift.
//
// These tests drive the real mappedConfig.processSubObjects with a stub query
// that returns per-attribute lists, which is exactly the shape a JSON or XPath
// scraper produces.

import (
	"context"
	"sort"
	"testing"

	"github.com/stashapp/stash/pkg/models"
)

// stubQuery returns a pre-baked list per selector, so a test states the raw
// page contents and then asserts on the assembled objects.
//
// queryType is left at its zero value, which IS the scrape case: scraper.go
// declares only `SearchQuery QueryType = iota + 1`, so anything non-zero is a
// search. A test that wanted the search path would set it to SearchQuery
// explicitly; the bug this file covers only occurs on a URL scrape.
type stubQuery struct {
	results   map[string][]string
	queryType QueryType
}

func (q *stubQuery) runQuery(selector string) ([]string, error) {
	return q.results[selector], nil
}

func (q *stubQuery) getType() QueryType  { return q.queryType }
func (q *stubQuery) setType(t QueryType) { q.queryType = t }
func (q *stubQuery) getURL() string      { return "https://example.com/scene" }
func (q *stubQuery) subScrape(_ context.Context, _ string) mappedQuery {
	return q
}

// The raw page, exactly as the reporter's log shows it: ten names, and gender
// scraped for only two of them.
func reportedPage() map[string][]string {
	return map[string][]string{
		"//div[@class='performer']//span[@class='name']/text()": {
			"Nicole Black", "Lady Gang", "Monika Fox", "Mary Jane",
			"Julia Maze", "Sasha Be Art", "Yves Morgan", "John Price",
			"Dylan Brown", "Larry Steel",
		},
		"//div[@class='performer']//span[@class='gender']/text()": {
			"FEMALE", "MALE",
		},
	}
}

func performerConfig() mappedConfig {
	return mappedConfig{
		"Name":   {Selector: "//div[@class='performer']//span[@class='name']/text()"},
		"Gender": {Selector: "//div[@class='performer']//span[@class='gender']/text()"},
	}
}

// The headline case. Only two performers have a scraped gender, and they are
// the FIRST two. Before the fix, the deduplicated gender list was shorter than
// the name list, so "MALE" attached to the performer at index 1 -- correct by
// luck here -- and the trailing performers inherited nothing, which is right.
// The real corruption shows in the next test.
func TestOnlyThePerformersWithAGenderGetOne(t *testing.T) {
	q := &stubQuery{results: reportedPage()}

	results := performerConfig().processSubObjects(context.Background(), q, nil, nil)
	got := results.scrapedPerformers()

	if len(got) != 10 {
		t.Fatalf("expected 10 performers, got %d", len(got))
	}

	if got[0].Name == nil || *got[0].Name != "Nicole Black" {
		t.Errorf("performer 0 is %v, want Nicole Black", got[0].Name)
	}
	if got[0].Gender == nil || *got[0].Gender != "FEMALE" {
		t.Errorf("performer 0 (%v) has gender %v, want FEMALE", *got[0].Name, got[0].Gender)
	}
	if got[1].Gender == nil || *got[1].Gender != "MALE" {
		t.Errorf("performer 1 (%v) has gender %v, want MALE", *got[1].Name, got[1].Gender)
	}

	// The eight with no gender must have NONE, not a shifted one.
	for i := 2; i < 10; i++ {
		if got[i].Gender != nil {
			t.Errorf("performer %d (%v) was given gender %q, but only the first "+
				"two performers have one on the page; this is the #7263 shift",
				i, derefStr(got[i].Name), *got[i].Gender)
		}
	}
}

// The corrupting case: genders in the middle, so the deduplicated list is
// SHORTER and every later attribute lands on the wrong object.
//
// Page order: Anne(F), Beth(M), Cara(none), Dora(F), Eve(none).
// Before the fix the gender list deduplicated to [F, M] and was written to
// indices 0 and 1, so Dora was never given a gender and Cara was not either --
// but the real damage is that a gender scraped for index 3 lands on index 1.
// Here the misalignment is visible as: the FEMALE that belongs to Dora
// disappearing, and a performer with no gender gaining one.
func TestAGenderInTheMiddleDoesNotShift(t *testing.T) {
	q := &stubQuery{
		results: map[string][]string{
			"//div[@class='performer']//span[@class='name']/text()": {
				"Anne", "Beth", "Cara", "Dora", "Eve",
			},
			// Index 0, 1 and 3 have a gender; 2 and 4 do not. The empty string
			// stands in for a node that matched the selector but had no text,
			// which is how this reaches a scraper in practice.
			"//div[@class='performer']//span[@class='gender']/text()": {
				"FEMALE", "MALE", "", "FEMALE", "",
			},
		},
	}

	got := performerConfig().processSubObjects(context.Background(), q, nil, nil).scrapedPerformers()
	if len(got) != 5 {
		t.Fatalf("expected 5 performers, got %d", len(got))
	}

	// An empty scraped value is NOT the same as a missing one: the selector
	// matched a node that had no text, so Gender is a pointer to "". That is
	// the information the page actually carried, and the whole point of the
	// fix is that index 2 is still Cara. Treating "" as absent is the bug in
	// disguise -- cleanResults deleted these, which is what caused the shift.
	want := []*string{strPtr("FEMALE"), strPtr("MALE"), strPtr(""), strPtr("FEMALE"), strPtr("")}
	for i, w := range want {
		gotv := got[i].Gender
		switch {
		case w == nil && gotv != nil:
			t.Errorf("performer %d (%v) has gender %q, want none", i, derefStr(got[i].Name), *gotv)
		case w != nil && gotv == nil:
			t.Errorf("performer %d (%v) has no gender at all, want %q: the "+
				"attribute was scraped for this performer and must stay "+
				"attached to it", i, derefStr(got[i].Name), *w)
		case w != nil && *gotv != *w:
			t.Errorf("performer %d (%v) has gender %q, want %q; an empty value "+
				"on the page must not shift a later gender onto this performer",
				i, derefStr(got[i].Name), *gotv, *w)
		}
	}
}

// The complementary property, and the one the bug actually broke: a performer
// the page said NOTHING about must have no gender pointer at all, rather than
// inheriting one from a neighbour.
func TestAPerformerWithNoGenderNodeHasNoGenderAtAll(t *testing.T) {
	q := &stubQuery{
		results: map[string][]string{
			"//div[@class='performer']//span[@class='name']/text()": {"Anne", "Beth"},
			// Only the FIRST performer has a gender node; the second has none.
			"//div[@class='performer']//span[@class='gender']/text()": {"FEMALE"},
		},
	}

	got := performerConfig().processSubObjects(context.Background(), q, nil, nil).scrapedPerformers()

	if got[0].Gender == nil || *got[0].Gender != "FEMALE" {
		t.Fatalf("performer 0 has gender %v, want FEMALE", got[0].Gender)
	}
	if got[1].Gender != nil {
		t.Errorf("performer 1 (%v) was given gender %q, but the page has no "+
			"gender node for them; with the list deduplicated to length 1 the "+
			"old code left this performer bare, and any scraper returning two "+
			"identical values would instead have shifted them onto this one",
			derefStr(got[1].Name), *got[1].Gender)
	}
}

// The other trigger: two performers genuinely share a value. Deduplicating
// the gender list removes the second FEMALE, which shifts everything after it.
func TestARepeatedValueDoesNotShiftLaterAttributes(t *testing.T) {
	q := &stubQuery{
		results: map[string][]string{
			"//div[@class='performer']//span[@class='name']/text()": {
				"Anne", "Beth", "Cara", "Dora",
			},
			"//div[@class='performer']//span[@class='gender']/text()": {
				"FEMALE", "MALE", "FEMALE", "FEMALE",
			},
		},
	}

	got := performerConfig().processSubObjects(context.Background(), q, nil, nil).scrapedPerformers()

	want := []string{"FEMALE", "MALE", "FEMALE", "FEMALE"}
	if len(got) != 4 {
		t.Fatalf("expected 4 performers, got %d", len(got))
	}
	for i, w := range want {
		if got[i].Gender == nil {
			t.Errorf("performer %d (%v) has no gender, want %q; the duplicate "+
				"FEMALE was removed from the list and everything after it "+
				"moved up", i, derefStr(got[i].Name), w)
			continue
		}
		if *got[i].Gender != w {
			t.Errorf("performer %d (%v) has gender %q, want %q", i, derefStr(got[i].Name), *got[i].Gender, w)
		}
	}
}

// Deduplication still happens -- just on assembled objects, by name, which is
// what the reporter asked for. Two entries with the same name really are the
// same performer listed twice.
func TestDuplicateNamedSubObjectsAreCollapsedAfterAssembly(t *testing.T) {
	q := &stubQuery{
		results: map[string][]string{
			"//div[@class='performer']//span[@class='name']/text()": {
				"Anne", "Beth", "Anne",
			},
			"//div[@class='performer']//span[@class='gender']/text()": {
				"FEMALE", "MALE", "FEMALE",
			},
		},
	}

	got := performerConfig().processSubObjects(context.Background(), q, nil, nil).
		dedupeByName().scrapedPerformers()

	if len(got) != 2 {
		t.Fatalf("expected 2 performers after deduplication, got %d: %v", len(got), names(got))
	}
	if *got[0].Name != "Anne" || *got[1].Name != "Beth" {
		t.Errorf("got %v, want [Anne Beth]; the FIRST occurrence must win", names(got))
	}
	if *got[0].Gender != "FEMALE" || *got[1].Gender != "MALE" {
		t.Errorf("genders are %q and %q, want FEMALE and MALE; deduplication "+
			"must not re-pair attributes", *got[0].Gender, *got[1].Gender)
	}
}

// A nameless sub-object is DROPPED, not kept. A slot with no name is a page
// artifact -- a container that matched the selector with nothing in it -- and
// scrapedPerformers/scrapedTags would hand the user a blank entry that cannot
// be created, matched or tagged.
//
// This INVERTS what an earlier version of this fix asserted, and the mutation
// harness is why: the tag-route test produced a real tag whose name was the
// empty string. "Keep it, it might have a gender" is true and useless, because
// a performer with no name cannot be matched on anyway -- and the old code's
// version of keeping it was worse than useless, since the empty name was
// deleted from the name list while its gender was retained and handed to a
// different performer. That crossing is the bug.
func TestANamelessSubObjectIsDropped(t *testing.T) {
	q := &stubQuery{
		results: map[string][]string{
			"//div[@class='performer']//span[@class='name']/text()": {
				"Anne", "", "Beth",
			},
			"//div[@class='performer']//span[@class='gender']/text()": {
				"FEMALE", "MALE", "FEMALE",
			},
		},
	}

	got := performerConfig().processSubObjects(context.Background(), q, nil, nil).
		dedupeByName().scrapedPerformers()

	if len(got) != 2 {
		t.Fatalf("expected 2 performers after dropping the nameless slot, got "+
			"%d: %v", len(got), names(got))
	}
	if *got[0].Name != "Anne" || *got[1].Name != "Beth" {
		t.Fatalf("got %v, want [Anne Beth]; the named performers keep their own "+
			"attributes and their order", names(got))
	}
	// The critical half: Beth's gender must be FEMALE -- hers -- and not the
	// MALE that belonged to the dropped slot.
	if got[1].Gender == nil || *got[1].Gender != "FEMALE" {
		t.Errorf("Beth has gender %v, want FEMALE; the dropped slot's MALE "+
			"leaked onto her", got[1].Gender)
	}
}

// Two nameless objects are both dropped, and neither contributes its
// attributes to a neighbour. That neighbour effect is the failure this whole
// fix exists to remove, so it is asserted directly here.
func TestTwoNamelessSubObjectsAreBothDropped(t *testing.T) {
	results := mappedResults{
		{"Name": "", "Gender": "MALE"},
		{"Gender": "FEMALE"},
	}

	if got := results.dedupeByName(); len(got) != 0 {
		t.Errorf("dedupeByName kept %d nameless objects, want 0: there is "+
			"nothing to match on and nothing downstream can use them", len(got))
	}
}

// Tags are single-attribute sub-objects, so the reporter says they are
// unaffected. They now go through the same path, and the invariant is that a
// tag with a repeated name collapses while distinct names survive.
func TestTagsAreDeduplicatedByName(t *testing.T) {
	q := &stubQuery{
		results: map[string][]string{
			"//a[@class='tag']/text()": {"a", "b", "a", "c", "b"},
		},
	}

	cfg := mappedConfig{"Name": {Selector: "//a[@class='tag']/text()"}}
	got := cfg.processSubObjects(context.Background(), q, nil, nil).
		dedupeByName().scrapedTags()

	if len(got) != 3 {
		t.Fatalf("expected 3 distinct tags, got %d", len(got))
	}
	gotNames := []string{got[0].Name, got[1].Name, got[2].Name}
	sort.Strings(gotNames)
	if gotNames[0] != "a" || gotNames[1] != "b" || gotNames[2] != "c" {
		t.Errorf("got %v, want [a b c]", gotNames)
	}
}

// The concat+split branch is a SEPARATE code path in postProcess with its own
// cleaning call, and it is the branch scrapers that scrape a comma-separated
// list out of one node actually use. The mutation harness caught that the
// tests only covered the plain branch, leaving a second copy of the bug
// unobserved.
func TestTheConcatSplitBranchIsAlsoAligned(t *testing.T) {
	q := &stubQuery{
		results: map[string][]string{
			// One node holding a comma-separated list of performers. The
			// empty slot between "Cara" and "Dora" is the shift trigger.
			//
			// No spaces after the commas: splitString does not trim, and a
			// space-padded fixture would fail on whitespace rather than on
			// the alignment this test is about. Scraper authors write
			// `split: ","` against markup where the separator carries no
			// padding, and trimming is a separate concern from indexing.
			"//div[@class='performers']/text()": {
				"Anne,Beth,,Dora,Eve",
			},
		},
	}

	cfg := mappedConfig{
		"Name": {
			Selector: "//div[@class='performers']/text()",
			Concat:   ",",
			Split:    ",",
		},
	}

	got := cfg.processSubObjects(context.Background(), q, nil, nil).scrapedPerformers()

	if len(got) != 5 {
		t.Fatalf("expected 5 performers from the split list, got %d; cleaning "+
			"the split results is what shifts the attributes", len(got))
	}
	want := []string{"Anne", "Beth", "", "Dora", "Eve"}
	for i, w := range want {
		if got[i].Name == nil {
			t.Errorf("performer %d has no name; want %q", i, w)
			continue
		}
		if *got[i].Name != w {
			t.Errorf("performer %d is %q, want %q; the split list was "+
				"deduplicated or trimmed and the indices moved", i, *got[i].Name, w)
		}
	}
}

// The routes in mapped.go are where the two process variants are actually
// chosen, so a test that only calls processSubObjects directly cannot see a
// call site left pointing at process. This goes through the real
// mappedScraper for a scene and asserts the performers came out aligned --
// which is the shape of the reported bug, a scene with several performers.
func TestASceneScrapeKeepsItsPerformersAligned(t *testing.T) {
	q := &stubQuery{
		results: map[string][]string{
			"//span[@class='performer-name']/text()":   {"Anne", "Beth", "Cara", "Dora"},
			"//span[@class='performer-gender']/text()": {"FEMALE", "MALE", "", "FEMALE"},
		},
	}

	scraper := mappedScraper{
		Scene: &mappedSceneScraperConfig{
			mappedConfig: mappedConfig{
				"Title": {Selector: "//h1/text()"},
			},
			Performers: mappedPerformerScraperConfig{
				mappedConfig: mappedConfig{
					"Name":   {Selector: "//span[@class='performer-name']/text()"},
					"Gender": {Selector: "//span[@class='performer-gender']/text()"},
				},
			},
		},
	}

	scene, err := scraper.scrapeScene(context.Background(), q)
	if err != nil {
		t.Fatalf("scrapeScene: %v", err)
	}
	if len(scene.Performers) != 4 {
		t.Fatalf("expected 4 performers, got %d: %+v", len(scene.Performers), scene.Performers)
	}

	wantNames := []string{"Anne", "Beth", "Cara", "Dora"}
	for i, w := range wantNames {
		if scene.Performers[i].Name == nil || *scene.Performers[i].Name != w {
			t.Fatalf("performer %d is %v, want %q", i, scene.Performers[i].Name, w)
		}
	}

	// Cara has an empty gender on the page and must keep exactly that;
	// Dora's FEMALE must not slide onto her.
	if scene.Performers[3].Gender == nil || *scene.Performers[3].Gender != "FEMALE" {
		t.Errorf("performer 3 (%s) has gender %v, want FEMALE; the empty value "+
			"at index 2 shifted the list", wantNames[3], scene.Performers[3].Gender)
	}
	if scene.Performers[2].Gender == nil || *scene.Performers[2].Gender != "" {
		t.Errorf("performer 2 (%s) has gender %v, want the empty value the "+
			"page gave her", wantNames[2], scene.Performers[2].Gender)
	}
}

// The image and gallery performer routes are separate call sites in mapped.go
// with the same multi-attribute alignment requirement. They are tested through
// the real scrapers because the harness cannot otherwise see a call site
// reverted, and a test of processSubObjects alone does not reach them.
func TestAnImageScrapeKeepsItsPerformersAligned(t *testing.T) {
	q := &stubQuery{
		results: map[string][]string{
			"//span[@class='performer-name']/text()":   {"Anne", "Beth", "Cara"},
			"//span[@class='performer-gender']/text()": {"FEMALE", "MALE", "FEMALE"},
		},
	}

	scraper := mappedScraper{
		Image: &mappedImageScraperConfig{
			mappedConfig: mappedConfig{"Title": {Selector: "//h1/text()"}},
			Performers: mappedConfig{
				"Name":   {Selector: "//span[@class='performer-name']/text()"},
				"Gender": {Selector: "//span[@class='performer-gender']/text()"},
			},
		},
	}

	image, err := scraper.scrapeImage(context.Background(), q)
	if err != nil {
		t.Fatalf("scrapeImage: %v", err)
	}
	assertPerformerAlignment(t, "image", image.Performers,
		[]string{"Anne", "Beth", "Cara"}, []string{"FEMALE", "MALE", "FEMALE"})
}

func TestAGalleryScrapeKeepsItsPerformersAligned(t *testing.T) {
	q := &stubQuery{
		results: map[string][]string{
			"//span[@class='performer-name']/text()":   {"Anne", "Beth", "Cara"},
			"//span[@class='performer-gender']/text()": {"FEMALE", "", "FEMALE"},
		},
	}

	scraper := mappedScraper{
		Gallery: &mappedGalleryScraperConfig{
			mappedConfig: mappedConfig{"Title": {Selector: "//h1/text()"}},
			Performers: mappedConfig{
				"Name":   {Selector: "//span[@class='performer-name']/text()"},
				"Gender": {Selector: "//span[@class='performer-gender']/text()"},
			},
		},
	}

	gallery, err := scraper.scrapeGallery(context.Background(), q)
	if err != nil {
		t.Fatalf("scrapeGallery: %v", err)
	}
	// The empty gender at index 1 is the shift trigger: cleaning collapses the
	// gender list to one entry and every later performer loses or gains one.
	assertPerformerAlignment(t, "gallery", gallery.Performers,
		[]string{"Anne", "Beth", "Cara"}, []string{"FEMALE", "", "FEMALE"})
}

// assertPerformerAlignment states the invariant once: every name in order, and
// every gender attached to the performer at the SAME index. Three separate
// tests use it because the routes are separate call sites, and a shared helper
// keeps the three from drifting apart as they get edited.
func assertPerformerAlignment(t *testing.T, route string, got []*models.ScrapedPerformer, wantNames, wantGenders []string) {
	t.Helper()

	if len(got) != len(wantNames) {
		t.Fatalf("%s: got %d performers, want %d (%v)", route, len(got), len(wantNames), names(got))
	}
	for i, want := range wantNames {
		if got[i].Name == nil || *got[i].Name != want {
			t.Errorf("%s: performer %d is %v, want %q", route, i, got[i].Name, want)
			continue
		}
		// An empty scraped gender is a pointer to "", not nil: the selector
		// matched an empty node, so the page did carry a value. Only a
		// performer the page said nothing about gets a nil.
		if got[i].Gender == nil || *got[i].Gender != wantGenders[i] {
			t.Errorf("%s: performer %d (%s) has gender %v, want %q; an attribute "+
				"crossed from another performer", route, i, want, got[i].Gender,
				wantGenders[i])
		}
	}
}

// The tag route is a SEPARATE call site in mapped.go, so it gets a test through
// the real scraper too. See the harness comment: for a single-attribute list
// this route is output-equivalent either way, so the test is here to pin the
// USER-VISIBLE contract (no blank tag, no duplicates, page order) rather than
// to detect the call-site mutation.
func TestASceneScrapeKeepsItsTagsAligned(t *testing.T) {
	q := &stubQuery{
		results: map[string][]string{
			"//h1/text()":              {"A Scene"},
			"//a[@class='tag']/text()": {"a", "", "b", "c"},
		},
	}

	scraper := mappedScraper{
		Scene: &mappedSceneScraperConfig{
			mappedConfig: mappedConfig{"Title": {Selector: "//h1/text()"}},
			Tags: mappedConfig{
				"Name": {Selector: "//a[@class='tag']/text()"},
			},
		},
	}

	scene, err := scraper.scrapeScene(context.Background(), q)
	if err != nil {
		t.Fatalf("scrapeScene: %v", err)
	}

	var got []string
	for _, tag := range scene.Tags {
		got = append(got, tag.Name)
	}

	// The empty slot must not become a tag, and must not have shifted b and c
	// either -- under the cleaning route the list would be the same three
	// names in the same order, so the ORDER alone cannot prove the fix. What
	// proves it is that dedupeByName is reached at all, which the harness
	// mutation for this call site exercises.
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got tags %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got tags %v, want %v", got, want)
			break
		}
	}
	for i, tag := range scene.Tags {
		if tag.Name == "" {
			t.Errorf("tag %d has an empty name; an empty scraped slot must not "+
				"become a tag", i)
		}
	}
}

// The single-object route must be UNCHANGED. process is used where index N is
// the Nth search result rather than the Nth sub-object, so per-attribute
// cleaning is still correct there. If this test fails, the fix leaked into a
// path that needed it alone.
func TestTheSingleObjectRouteStillCleansPerAttribute(t *testing.T) {
	q := &stubQuery{
		results: map[string][]string{
			"//span[@class='title']/text()": {"A", "A", "B"},
		},
	}

	cfg := mappedConfig{"Title": {Selector: "//span[@class='title']/text()"}}
	got := cfg.process(context.Background(), q, nil, nil)

	if len(got) != 2 {
		t.Errorf("process returned %d results, want 2; the single-object route "+
			"must keep deduplicating, or repeated search results come back as "+
			"duplicates", len(got))
	}
}

func derefStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func names(performers []*models.ScrapedPerformer) []string {
	out := make([]string, 0, len(performers))
	for _, p := range performers {
		out = append(out, derefStr(p.Name))
	}
	return out
}
