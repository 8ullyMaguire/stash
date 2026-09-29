package scraper

import (
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
)

type mappedResult map[string]interface{}
type mappedResults []mappedResult

func (r mappedResult) string(key string) (string, bool) {
	v, ok := r[key]
	if !ok {
		return "", false
	}

	val, ok := v.(string)
	if !ok {
		logger.Errorf("String field %s is %T in mappedResult", key, r[key])
	}

	return val, true
}

func (r mappedResult) mustString(key string) string {
	v, ok := r[key]
	if !ok {
		logger.Errorf("Missing required string field %s in mappedResult", key)
		return ""
	}

	val, ok := v.(string)
	if !ok {
		logger.Errorf("String field %s is %T in mappedResult", key, r[key])
	}

	return val
}

func (r mappedResult) stringPtr(key string) *string {
	val, ok := r.string(key)
	if !ok {
		return nil
	}
	return &val
}

func (r mappedResult) stringSlice(key string) []string {
	v, ok := r[key]
	if !ok {
		return nil
	}

	// need to try both []string and string
	val, ok := v.([]string)

	if ok {
		return val
	}

	// try single string
	singleVal, ok := v.(string)
	if !ok {
		logger.Errorf("String slice field %s is %T in mappedResult", key, r[key])
		return nil
	}

	return []string{singleVal}
}

func (r mappedResult) IntPtr(key string) *int {
	v, ok := r[key]
	if !ok {
		return nil
	}

	val, ok := v.(int)
	if !ok {
		logger.Errorf("Int field %s is %T in mappedResult", key, r[key])
		return nil
	}

	return &val
}

func (r mappedResults) setSingleValue(index int, key string, value string) mappedResults {
	r = r.growTo(index)
	logger.Debugf(`[%d][%s] = %s`, index, key, value)
	r[index][key] = value
	return r
}

func (r mappedResults) setMultiValue(index int, key string, value []string) mappedResults {
	r = r.growTo(index)
	logger.Debugf(`[%d][%s] = %s`, index, key, value)
	r[index][key] = value
	return r
}

// growTo extends r so that index is addressable, padding with empty results.
//
// #7263. The original code appended ONE element when index >= len(r), which is
// only correct when the indices arrive in order. They no longer do: with
// per-attribute cleaning removed, a performer list whose first attribute is
// empty for entries 0-2 and populated from 3 onwards writes at index 3 while
// len(r) is 0, leaving a slice of length 1 and a panic on the next write.
// Growing to the index also makes a genuinely sparse attribute list produce
// empty objects rather than silently shifting every later attribute.
func (r mappedResults) growTo(index int) mappedResults {
	for len(r) <= index {
		r = append(r, make(mappedResult))
	}
	return r
}

// dedupeByName removes duplicate sub-objects that share a Name, keeping the
// first. #7263.
//
// This is the deduplication that per-attribute cleaning used to do, and doing
// it here instead is the whole point of the fix: at this stage an object is
// complete, so two entries with the same name really are the same entity
// listed twice on the page, and dropping one loses nothing. Removing a
// duplicate name from the name list but not from the gender list -- which is
// what cleanResults did -- cannot express that.
//
// NAMELESS OBJECTS ARE DROPPED. A scraper slot with no name is a page
// artifact: a container that matched the selector with nothing in it. There is
// no entity to describe and nothing downstream can match on, so keeping it
// only produces an object that will be offered to the user as a blank entry.
//
// The rule is uniform rather than per-type, which is the point: a single
// predicate at one place, instead of each consumer deciding for itself. An
// earlier version of this fix kept nameless objects on the reasoning that a
// performer might have a gender even without a name -- true, and useless,
// because ScrapedPerformer.Name is a pointer and a performer with no name
// cannot be created, matched or tagged. Note that this DOES change what a
// performer list loses relative to the old code: the old code deleted the
// empty name from the name list and kept the rest of the attributes, so a
// nameless slot contributed a gender to whoever landed at that index -- which
// is the crossing this fix exists to remove.
func (r mappedResults) dedupeByName() mappedResults {
	if len(r) == 0 {
		return r
	}

	seen := make(map[string]bool, len(r))
	ret := make(mappedResults, 0, len(r))

	for _, result := range r {
		name, ok := result["Name"].(string)
		if !ok || name == "" {
			logger.Debug("Dropping sub-object with no name")
			continue
		}
		if seen[name] {
			logger.Debugf("Dropping duplicate sub-object %q", name)
			continue
		}
		seen[name] = true
		ret = append(ret, result)
	}

	return ret
}

func (r mappedResults) scrapedTags() []*models.ScrapedTag {
	if len(r) == 0 {
		return nil
	}

	ret := make([]*models.ScrapedTag, len(r))
	for i, result := range r {
		ret[i] = result.scrapedTag()
	}

	return ret
}

func (r mappedResult) scrapedTag() *models.ScrapedTag {
	return &models.ScrapedTag{
		Name: r.mustString("Name"),
	}
}

func (r mappedResult) scrapedPerformer() *models.ScrapedPerformer {
	ret := &models.ScrapedPerformer{
		Name:           r.stringPtr("Name"),
		Disambiguation: r.stringPtr("Disambiguation"),
		Gender:         r.stringPtr("Gender"),
		URL:            r.stringPtr("URL"),
		URLs:           r.stringSlice("URLs"),
		Twitter:        r.stringPtr("Twitter"),
		Birthdate:      r.stringPtr("Birthdate"),
		Ethnicity:      r.stringPtr("Ethnicity"),
		Country:        r.stringPtr("Country"),
		EyeColor:       r.stringPtr("EyeColor"),
		Height:         r.stringPtr("Height"),
		Measurements:   r.stringPtr("Measurements"),
		FakeTits:       r.stringPtr("FakeTits"),
		PenisLength:    r.stringPtr("PenisLength"),
		Circumcised:    r.stringPtr("Circumcised"),
		CareerLength:   r.stringPtr("CareerLength"),
		CareerStart:    r.stringPtr("CareerStart"),
		CareerEnd:      r.stringPtr("CareerEnd"),
		Tattoos:        r.stringPtr("Tattoos"),
		Piercings:      r.stringPtr("Piercings"),
		Aliases:        r.stringPtr("Aliases"),
		Image:          r.stringPtr("Image"),
		Images:         r.stringSlice("Images"),
		Details:        r.stringPtr("Details"),
		DeathDate:      r.stringPtr("DeathDate"),
		HairColor:      r.stringPtr("HairColor"),
		Weight:         r.stringPtr("Weight"),
	}
	return ret
}

func (r mappedResults) scrapedPerformers() []*models.ScrapedPerformer {
	if len(r) == 0 {
		return nil
	}

	ret := make([]*models.ScrapedPerformer, len(r))
	for i, result := range r {
		ret[i] = result.scrapedPerformer()
	}

	return ret
}

func (r mappedResult) scrapedScene() *models.ScrapedScene {
	ret := &models.ScrapedScene{
		Title:          r.stringPtr("Title"),
		Code:           r.stringPtr("Code"),
		Details:        r.stringPtr("Details"),
		Director:       r.stringPtr("Director"),
		URL:            r.stringPtr("URL"),
		URLs:           r.stringSlice("URLs"),
		Date:           r.stringPtr("Date"),
		ProductionDate: r.stringPtr("ProductionDate"),
		Image:          r.stringPtr("Image"),
		Duration:       r.IntPtr("Duration"),
	}
	return ret
}

func (r mappedResult) scrapedImage() *models.ScrapedImage {
	ret := &models.ScrapedImage{
		Title:        r.stringPtr("Title"),
		Code:         r.stringPtr("Code"),
		Details:      r.stringPtr("Details"),
		Photographer: r.stringPtr("Photographer"),
		URLs:         r.stringSlice("URLs"),
		Date:         r.stringPtr("Date"),
	}
	return ret
}

func (r mappedResult) scrapedGallery() *models.ScrapedGallery {
	ret := &models.ScrapedGallery{
		Title:        r.stringPtr("Title"),
		Code:         r.stringPtr("Code"),
		Details:      r.stringPtr("Details"),
		Photographer: r.stringPtr("Photographer"),
		URL:          r.stringPtr("URL"),
		URLs:         r.stringSlice("URLs"),
		Date:         r.stringPtr("Date"),
	}
	return ret
}

func (r mappedResult) scrapedStudio() *models.ScrapedStudio {
	ret := &models.ScrapedStudio{
		Name:    r.mustString("Name"),
		URL:     r.stringPtr("URL"),
		URLs:    r.stringSlice("URLs"),
		Image:   r.stringPtr("Image"),
		Details: r.stringPtr("Details"),
		Aliases: r.stringPtr("Aliases"),
	}
	return ret
}

func (r mappedResult) scrapedMovie() *models.ScrapedMovie {
	ret := &models.ScrapedMovie{
		Name:       r.stringPtr("Name"),
		Aliases:    r.stringPtr("Aliases"),
		URLs:       r.stringSlice("URLs"),
		Duration:   r.stringPtr("Duration"),
		Date:       r.stringPtr("Date"),
		Director:   r.stringPtr("Director"),
		Synopsis:   r.stringPtr("Synopsis"),
		FrontImage: r.stringPtr("FrontImage"),
		BackImage:  r.stringPtr("BackImage"),
	}

	return ret
}

func (r mappedResult) scrapedGroup() *models.ScrapedGroup {
	ret := &models.ScrapedGroup{
		Name:       r.stringPtr("Name"),
		Aliases:    r.stringPtr("Aliases"),
		URL:        r.stringPtr("URL"),
		URLs:       r.stringSlice("URLs"),
		Duration:   r.stringPtr("Duration"),
		Date:       r.stringPtr("Date"),
		Director:   r.stringPtr("Director"),
		Synopsis:   r.stringPtr("Synopsis"),
		FrontImage: r.stringPtr("FrontImage"),
		BackImage:  r.stringPtr("BackImage"),
	}

	return ret
}

func (r mappedResults) scrapedMovies() []*models.ScrapedMovie {
	if len(r) == 0 {
		return nil
	}
	ret := make([]*models.ScrapedMovie, len(r))
	for i, result := range r {
		ret[i] = result.scrapedMovie()
	}

	return ret
}

func (r mappedResults) scrapedGroups() []*models.ScrapedGroup {
	if len(r) == 0 {
		return nil
	}
	ret := make([]*models.ScrapedGroup, len(r))
	for i, result := range r {
		ret[i] = result.scrapedGroup()
	}

	return ret
}
