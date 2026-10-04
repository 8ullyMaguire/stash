package models

import (
	"context"
	"time"
)

type Performer struct {
	ID             int              `json:"id"`
	Name           string           `json:"name"`
	Disambiguation string           `json:"disambiguation"`
	Gender         *GenderEnum      `json:"gender"`
	Birthdate      *Date            `json:"birthdate"`
	Ethnicity      string           `json:"ethnicity"`
	Country        string           `json:"country"`
	EyeColor       string           `json:"eye_color"`
	Height         *int             `json:"height"`
	Measurements   string           `json:"measurements"`
	FakeTits       string           `json:"fake_tits"`
	PenisLength    *float64         `json:"penis_length"`
	Circumcised    *CircumcisedEnum `json:"circumcised"`
	CareerStart    *Date            `json:"career_start"`
	CareerEnd      *Date            `json:"career_end"`
	Tattoos        string           `json:"tattoos"`
	Piercings      string           `json:"piercings"`
	Favorite       bool             `json:"favorite"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
	// Rating expressed in 1-100 scale
	Rating        *int   `json:"rating"`
	Details       string `json:"details"`
	DeathDate     *Date  `json:"death_date"`
	HairColor     string `json:"hair_color"`
	Weight        *int   `json:"weight"`
	IgnoreAutoTag bool   `json:"ignore_auto_tag"`

	Aliases  RelatedStrings  `json:"aliases"`
	URLs     RelatedStrings  `json:"urls"`
	TagIDs   RelatedIDs      `json:"tag_ids"`
	StashIDs RelatedStashIDs `json:"stash_ids"`

	// TattooLocations and PiercingLocations are the STRUCTURED form of the packed `Tattoos` and
	// `Piercings` strings above, which stay exactly as they are.
	//
	// stash#2359. The packed strings are on the GraphQL surface, rendered by the UI and written by
	// CSV import, so they are not being replaced -- one value in a text box cannot answer "which
	// tattoo is on the left arm", which is what #2359 is for. Both are RelatedStrings holding a
	// LOCATION per mark, loaded together by LoadBodyMarks; the description, which is why a mark is
	// not simply a string, is reached through BodyMarks rather than flattened into these.
	TattooLocations   RelatedStrings `json:"tattoo_locations"`
	PiercingLocations RelatedStrings `json:"piercing_locations"`

	// Nationalities is []Nationality and not RelatedStrings because a performer may be dual-national
	// (#1922) AND the caller needs the reference row: a selector offers names, but a query filter
	// needs ids and an export needs codes. RelatedIDs would carry the ids and lose the names;
	// RelatedStrings would carry the names and lose the ids.
	Nationalities RelatedNationalities `json:"nationalities"`
}

type CreatePerformerInput struct {
	*Performer

	CustomFields map[string]interface{} `json:"custom_fields"`
}

type UpdatePerformerInput struct {
	*Performer

	CustomFields CustomFieldsInput `json:"custom_fields"`
}

func NewPerformer() Performer {
	currentTime := time.Now()
	return Performer{
		CreatedAt: currentTime,
		UpdatedAt: currentTime,
	}
}

// PerformerPartial represents part of a Performer object. It is used to update
// the database entry.
type PerformerPartial struct {
	Name           OptionalString
	Disambiguation OptionalString
	Gender         OptionalString
	URLs           *UpdateStrings
	Birthdate      OptionalDate
	Ethnicity      OptionalString
	Country        OptionalString
	EyeColor       OptionalString
	Height         OptionalInt
	Measurements   OptionalString
	FakeTits       OptionalString
	PenisLength    OptionalFloat64
	Circumcised    OptionalString
	CareerStart    OptionalDate
	CareerEnd      OptionalDate
	Tattoos        OptionalString
	Piercings      OptionalString
	Favorite       OptionalBool
	CreatedAt      OptionalTime
	UpdatedAt      OptionalTime
	// Rating expressed in 1-100 scale
	Rating        OptionalInt
	Details       OptionalString
	DeathDate     OptionalDate
	HairColor     OptionalString
	Weight        OptionalInt
	IgnoreAutoTag OptionalBool

	Aliases *UpdateStrings
	TagIDs  *UpdateIDs
	// stash#2359. NationalityIDs is *UpdateIDs rather than a plain slice so that ABSENT means
	// "do not touch" and PRESENT-BUT-EMPTY means "remove them all" -- the same distinction TagIDs
	// makes, and the reason a partial cannot be a plain field. TattooLocations and
	// PiercingLocations are UpdateStrings for the same reason: a form that renders an empty text box
	// must be able to clear the marks, not silently leave them.
	NationalityIDs    *UpdateIDs
	TattooLocations   *UpdateStrings
	PiercingLocations *UpdateStrings
	// ImageIDs links the performer to rows in the `images` table through `performers_images`.
	// stash#571.
	//
	// This is the SECOND image system and is not the blob: `image` (the input) and `image_path`
	// (the output) go to `performerImageBlobColumn`, while `image_count` reads this join. A
	// partial field rather than a plain slice because absent must mean "do not touch" and
	// present-but-empty must mean "clear" -- the same distinction `TagIDs` makes.
	ImageIDs *UpdateIDs
	StashIDs *UpdateStashIDs

	CustomFields CustomFieldsInput
}

func NewPerformerPartial() PerformerPartial {
	currentTime := time.Now()
	return PerformerPartial{
		UpdatedAt: NewOptionalTime(currentTime),
	}
}

func (s *Performer) LoadAliases(ctx context.Context, l AliasLoader) error {
	return s.Aliases.load(func() ([]string, error) {
		return l.GetAliases(ctx, s.ID)
	})
}

func (s *Performer) LoadURLs(ctx context.Context, l URLLoader) error {
	return s.URLs.load(func() ([]string, error) {
		return l.GetURLs(ctx, s.ID)
	})
}

func (s *Performer) LoadTagIDs(ctx context.Context, l TagIDLoader) error {
	return s.TagIDs.load(func() ([]int, error) {
		return l.GetTagIDs(ctx, s.ID)
	})
}

func (s *Performer) LoadStashIDs(ctx context.Context, l StashIDLoader) error {
	return s.StashIDs.load(func() ([]StashID, error) {
		return l.GetStashIDs(ctx, s.ID)
	})
}

func (s *Performer) LoadRelationships(ctx context.Context, l PerformerReader) error {
	if err := s.LoadAliases(ctx, l); err != nil {
		return err
	}

	if err := s.LoadTagIDs(ctx, l); err != nil {
		return err
	}

	if err := s.LoadStashIDs(ctx, l); err != nil {
		return err
	}

	return nil
}
