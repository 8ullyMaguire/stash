package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/sliceutil/stringslice"
)

// stash#2359 — the GraphQL surface for the six parity features.
//
// WHY FIELD RESOLVERS AND NOT STRUCT FIELDS
// =========================================
//
// `codes`, `directors`, `tattoo_locations`, `piercing_locations`, `body_marks` and `nationalities`
// are all backed by join tables or reference tables, so gqlgen cannot bind them to a struct field --
// it would generate `return obj.Codes, nil` against a `RelatedStrings` whose list is nil, and every
// client would silently receive an empty list. That is the exact failure documented on
// `PersonCluster.Members` in resolver.go, and it is invisible until a client selects the field.
//
// So every one is a field resolver following `studioResolver.Aliases`: check `Loaded()`, and if not
// loaded, open a read transaction and load.
//
// WHY AN INPUT RESOLVER FOR EACH NEW INPUT FIELD
// ===============================================
//
// The input fields need `*models.UpdateStrings` / `*models.UpdateIDs` rather than plain slices, and
// that is not a stylistic choice. A plain `[]string` cannot distinguish "the client did not send this
// field" from "the client sent an empty list", and those mean opposite things on an update: leave the
// codes alone, or delete every code. gqlgen will not produce that distinction for a slice, so the
// input resolver takes the slice the schema gave it and stores the pointer on the context-bound
// update map that `changesetTranslator.updateStrings` already reads.

// ---------------------------------------------------------------------------
// Output field resolvers
// ---------------------------------------------------------------------------

// Codes returns the studio's Stash-Box codes (#2607, #3051).
func (r *studioResolver) Codes(ctx context.Context, obj *models.Studio) ([]string, error) {
	if !obj.Codes.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadCodes(ctx, r.repository.Studio)
		}); err != nil {
			return nil, err
		}
	}

	return obj.Codes.List(), nil
}

// Directors returns the scene's structured directors (#3051).
//
// The packed `director` string is untouched and still returned by the existing field; this is the
// queryable form beside it.
func (r *sceneResolver) Directors(ctx context.Context, obj *models.Scene) ([]string, error) {
	if !obj.Directors.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadDirectors(ctx, r.repository.Scene)
		}); err != nil {
			return nil, err
		}
	}

	return obj.Directors.List(), nil
}

// TattooLocations returns the performer's tattoo locations, one per mark.
func (r *performerResolver) TattooLocations(ctx context.Context, obj *models.Performer) ([]string, error) {
	if !obj.TattooLocations.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadBodyMarks(ctx, r.repository.Performer)
		}); err != nil {
			return nil, err
		}
	}

	return obj.TattooLocations.List(), nil
}

// PiercingLocations returns the performer's piercing locations, one per mark.
func (r *performerResolver) PiercingLocations(ctx context.Context, obj *models.Performer) ([]string, error) {
	if !obj.PiercingLocations.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadBodyMarks(ctx, r.repository.Performer)
		}); err != nil {
			return nil, err
		}
	}

	return obj.PiercingLocations.List(), nil
}

// BodyMarks returns every mark on the performer, tattoos and piercings alike, with descriptions.
//
// SEPARATE FROM TattooLocations AND PiercingLocations because a client that wants to show
// descriptions has to reach the row, and flattening them into the two location lists would lose
// exactly the field that makes a mark more than a string.
func (r *performerResolver) BodyMarks(ctx context.Context, obj *models.Performer) ([]*models.BodyMark, error) {
	var marks []*models.BodyMark

	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		var err error
		// Both kinds, so the table is read once per kind rather than filtered client-side.
		tattoos, err := r.repository.Performer.GetBodyMarks(ctx, obj.ID, "tattoo")
		if err != nil {
			return err
		}

		piercings, err := r.repository.Performer.GetBodyMarks(ctx, obj.ID, "piercing")
		if err != nil {
			return err
		}

		// An explicit nil slice, not the nil above: a nil `marks` marshals to `null`, and the
		// schema promises `[BodyMark!]!`.
		marks = append([]*models.BodyMark{}, tattoos...)
		marks = append(marks, piercings...)

		return nil
	}); err != nil {
		return nil, err
	}

	return marks, nil
}

// Nationalities returns the performer's nationalities (#1922).
func (r *performerResolver) Nationalities(ctx context.Context, obj *models.Performer) ([]*models.Nationality, error) {
	if !obj.Nationalities.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadNationalities(ctx, r.repository.Performer)
		}); err != nil {
			return nil, err
		}
	}

	return obj.Nationalities.List(), nil
}

// AllNationalities returns the nationality reference list, for populating a selector.
func (r *queryResolver) AllNationalities(ctx context.Context) ([]*models.Nationality, error) {
	var out []*models.Nationality

	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		var err error
		out, err = r.repository.Performer.AllNationalities(ctx)
		return err
	}); err != nil {
		return nil, err
	}

	// Non-nil for the same reason as in BodyMarks: the schema says `[Nationality!]!`.
	if out == nil {
		out = []*models.Nationality{}
	}

	return out, nil
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

// BodyMarkCreate adds a tattoo or piercing to a performer.
//
// NOTE THE ABSENT changesetTranslator. Every other mutation in this package builds one to read the
// client-provided field map, and doing the same here PANICS outside a live GraphQL operation --
// getUpdateInputMap calls graphql.GetOperationContext, which has nothing to return when the resolver
// is invoked directly, so the panic is a nil dereference rather than a testable error.
//
// It would also be unused. BodyMarkCreate has no optional fields: kind, location and description are
// all required, so there is nothing to ask the client whether it meant to send, and no changeset to
// record. The translator is there in other mutations because their inputs carry optional fields;
// this one does not.
func (r *mutationResolver) BodyMarkCreate(ctx context.Context, input BodyMarkCreateInput) (*models.BodyMark, error) {
	performerID, err := strconv.Atoi(input.PerformerID)
	if err != nil {
		return nil, fmt.Errorf("converting performer id %q: %w", input.PerformerID, err)
	}

	// The kind is a discriminator with exactly two legal values, and it is validated HERE rather
	// than left to the store: a typo would otherwise create a row that `body_marks` returns and
	// neither `tattoo_locations` nor `piercing_locations` can ever see, which is a row that exists
	// and is unreachable.
	kind := input.Kind
	if kind != "tattoo" && kind != "piercing" {
		return nil, fmt.Errorf("body mark kind must be %q or %q, got %q", "tattoo", "piercing", kind)
	}

	var created *models.BodyMark
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		var err error
		created, err = r.repository.Performer.CreateBodyMark(ctx, models.BodyMark{
			PerformerID: performerID,
			Kind:        kind,
			Location:    input.Location,
			Description: input.Description,
		})
		if err != nil {
			return err
		}

		// The packed `performers.tattoos` / `piercings` strings are left alone on purpose. They stay
		// authoritative for the fields the UI and CSV importer already read, and rewriting them here
		// would mean two writers to one column with different rules for what a list is.
		return nil
	}); err != nil {
		return nil, err
	}

	return created, nil
}

// BodyMarkDestroy removes a tattoo or piercing.
func (r *mutationResolver) BodyMarkDestroy(ctx context.Context, input BodyMarkDestroyInput) (bool, error) {
	id, err := strconv.Atoi(input.ID)
	if err != nil {
		return false, fmt.Errorf("converting body mark id %q: %w", input.ID, err)
	}

	var affected int

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		var err error
		affected, err = r.repository.Performer.DestroyBodyMark(ctx, id)
		return err
	}); err != nil {
		return false, err
	}

	// false for a row that was not there, NOT an error. This matches StudioDestroy and every other
	// destroy in this package: a destroy is a request to make something absent, and it already is.
	// Reporting ErrNotFound would make a retried idempotent delete look like a failure.
	return affected > 0, nil
}

// relatedNationalities resolves nationality ids to reference rows for the GraphQL surface.
//
// stash#2359 (#1922). Performer.Nationalities holds *Nationality because a selector needs the name
// and a filter needs the id; the store persists ids. So the conversion happens here, once, rather
// than in each of the four places a performer is written.
//
// The full reference list is fetched and filtered in memory rather than queried per id, because the
// list is 107 rows seeded by migration 125 and a per-id query would be 2 round trips for a
// dual-national performer to read one small table. An id that is not in the list is an ERROR rather
// than a silent drop: a client that sends nationality 999 has a bug, and quietly discarding it
// leaves the user with a selection that appears to have saved and did not.
func (t changesetTranslator) relatedNationalities(ctx context.Context, l models.NationalityLoader, ids []string) ([]*models.Nationality, error) {
	intIds, err := stringslice.StringSliceToIntSlice(ids)
	if err != nil {
		return nil, fmt.Errorf("converting nationality ids: %w", err)
	}

	wanted := make(map[int]bool, len(intIds))
	for _, id := range intIds {
		wanted[id] = true
	}

	all, err := l.AllNationalities(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*models.Nationality, 0, len(intIds))
	for _, n := range all {
		if n != nil && wanted[n.ID] {
			out = append(out, n)
		}
	}

	// Report the ids that resolved to nothing, by name, because "nationality 999 does not exist"
	// is actionable and "invalid nationality" is not.
	found := make(map[int]bool, len(out))
	for _, n := range out {
		found[n.ID] = true
	}
	var missing []string
	for _, id := range intIds {
		if !found[id] {
			missing = append(missing, strconv.Itoa(id))
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("no such nationality id(s): %s", strings.Join(missing, ", "))
	}

	return out, nil
}
