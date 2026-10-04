package models

import "context"

// stash#2359 — loaders and interface wiring for the six parity features.
//
// EACH LOADER IS A ONE-METHOD INTERFACE COMPOSED INTO AN EXISTING READER, which is how this package
// already expresses "the store can supply this relationship": `AliasLoader`, `URLLoader` and
// `StashIDLoader` are declared in relationships.go and composed into StudioReader, PerformerReader
// and SceneReader respectively. The new parity loaders follow that shape rather than widening a
// reader interface with six more methods, because the composed form is what lets a caller that only
// needs codes depend on codes.
//
// Each is separate rather than one `ParityLoader`, because the six features share no storage. A
// combined interface would force every implementer -- including test doubles -- to supply all six,
// which is how an interface stops describing what a thing needs and starts describing what it
// happens to have.

// CodeLoader supplies a studio's Stash-Box codes (#2607, #3051).
type CodeLoader interface {
	GetCodes(ctx context.Context, relatedID int) ([]string, error)
}

// DirectorLoader supplies a scene's directors, one row per director (#3051).
type DirectorLoader interface {
	GetDirectors(ctx context.Context, relatedID int) ([]string, error)
}

// NationalityLoader supplies the nationality reference list and a performer's selections (#1922).
type NationalityLoader interface {
	AllNationalities(ctx context.Context) ([]*Nationality, error)
	GetNationalities(ctx context.Context, performerID int) ([]Nationality, error)
}

// BodyMarkLoader supplies and mutates a performer's tattoos and piercings (the unticketed #2359
// item). The writer methods are on the same interface because a mark has no useful read-only
// lifecycle: it is created, listed and deleted as one unit.
type BodyMarkLoader interface {
	GetBodyMarks(ctx context.Context, performerID int, kind string) ([]*BodyMark, error)
	CreateBodyMark(ctx context.Context, mark BodyMark) (*BodyMark, error)
	DestroyBodyMark(ctx context.Context, id int) (int, error)
}

// ---------------------------------------------------------------------------
// Studio
// ---------------------------------------------------------------------------

// LoadCodes loads the studio's Stash-Box codes, following LoadAliases exactly.
//
// The `if s.Codes.Loaded()` guard is inside RelatedStrings.load, so this cannot double-load; the
// point of mirroring LoadAliases is that a caller reading a studio from a list (where codes were
// not selected) and a caller reading one by id get the same behaviour rather than an empty list in
// one case and the codes in the other.
func (s *Studio) LoadCodes(ctx context.Context, l CodeLoader) error {
	return s.Codes.load(func() ([]string, error) {
		return l.GetCodes(ctx, s.ID)
	})
}

// ---------------------------------------------------------------------------
// Performer
// ---------------------------------------------------------------------------

// LoadBodyMarks loads the performer's tattoos and piercings as structured marks.
//
// TWO CALLS, NOT ONE, and the reason is the schema rather than a preference: tattoos and piercings
// share one table distinguished by `kind`, so a single "body marks" relationship would have to leak
// that discriminator to the caller in order to answer "give me the tattoos". TattooLocations and
// PiercingLocations are each a plain location string for that reason; the `description` column,
// which is what makes a mark more than a string, is reached through BodyMarks rather than being
// flattened into either.
//
// Both are loaded together because every editor asks for both, and two round trips to one table is
// worse than one round trip asking twice.
func (p *Performer) LoadBodyMarks(ctx context.Context, l BodyMarkLoader) error {
	locations := func(kind string) func() ([]string, error) {
		return func() ([]string, error) {
			marks, err := l.GetBodyMarks(ctx, p.ID, kind)
			if err != nil {
				return nil, err
			}

			out := make([]string, 0, len(marks))
			for _, m := range marks {
				out = append(out, m.Location)
			}

			return out, nil
		}
	}

	if err := p.TattooLocations.load(locations("tattoo")); err != nil {
		return err
	}

	return p.PiercingLocations.load(locations("piercing"))
}

// LoadNationalities loads the performer's nationalities, following LoadAliases exactly.
func (p *Performer) LoadNationalities(ctx context.Context, l NationalityLoader) error {
	return p.Nationalities.load(func() ([]*Nationality, error) {
		nats, err := l.GetNationalities(ctx, p.ID)
		if err != nil {
			return nil, err
		}

		// Pointers into the caller's slice rather than a second allocation: `nats[i]` is distinct
		// for every i, so each &nats[i] addresses a different element and the slice outlives
		// this closure because the relationship holds it.
		ptrs := make([]*Nationality, len(nats))
		for i := range nats {
			ptrs[i] = &nats[i]
		}

		return ptrs, nil
	})
}

// ---------------------------------------------------------------------------
// Scene
// ---------------------------------------------------------------------------

// LoadDirectors loads the scene's structured directors (#3051), following LoadAliases exactly.
func (s *Scene) LoadDirectors(ctx context.Context, l DirectorLoader) error {
	return s.Directors.load(func() ([]string, error) {
		return l.GetDirectors(ctx, s.ID)
	})
}
