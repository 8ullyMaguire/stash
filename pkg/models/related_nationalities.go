package models

// RelatedNationalities is the loaded form of a performer's nationalities.
//
// stash#2359 (#1922).
//
// This exists because a nationality is NOT a string, and the two existing related types are each
// wrong for it in a way that matters. RelatedStrings would carry the display names and lose the ids,
// so a filter by nationality id would have to match on text; RelatedIDs would carry the ids and lose
// the names, so the editor's selector would have to re-resolve every id into a label on every
// render. The value the caller actually needs is the row: it has an id to filter by, a name to
// show, and a code to export.
//
// The struct is a value type holding a slice of POINTERS, matching PerformerReader and
// PerformerQueryer, which both return []*Performer. A `Loaded()` check on `list != nil` is what
// distinguishes "loaded and empty" from "never loaded" -- the same distinction RelatedStrings makes,
// and the reason an empty nationality list does not re-query on every render.
type RelatedNationalities struct {
	list []*Nationality
}

// NewRelatedNationalities returns a loaded RelatedNationalities with the provided values.
// Loaded returns true when the provided slice is not nil.
func NewRelatedNationalities(values []*Nationality) RelatedNationalities {
	return RelatedNationalities{
		list: values,
	}
}

// Loaded returns true if the nationalities have been loaded.
func (r RelatedNationalities) Loaded() bool {
	return r.list != nil
}

func (r RelatedNationalities) mustLoaded() {
	if !r.Loaded() {
		panic("list has not been loaded")
	}
}

// List returns the related nationalities. Panics if the relationship has not been loaded.
func (r RelatedNationalities) List() []*Nationality {
	r.mustLoaded()

	return r.list
}

// Add adds the provided nationalities to the list. Panics if the relationship has not been loaded.
func (r *RelatedNationalities) Add(values ...*Nationality) {
	r.mustLoaded()

	r.list = append(r.list, values...)
}

func (r *RelatedNationalities) load(fn func() ([]*Nationality, error)) error {
	if r.Loaded() {
		return nil
	}

	values, err := fn()
	if err != nil {
		return err
	}

	if values == nil {
		values = []*Nationality{}
	}

	r.list = values

	return nil
}
