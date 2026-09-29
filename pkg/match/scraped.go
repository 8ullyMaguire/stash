package match

import (
	"context"
	"strconv"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/performer"
	"github.com/stashapp/stash/pkg/studio"
	"github.com/stashapp/stash/pkg/tag"
)

type PerformerFinder interface {
	models.PerformerQueryer
	FindByNames(ctx context.Context, names []string, nocase bool) ([]*models.Performer, error)
	FindByStashID(ctx context.Context, stashID models.StashID) ([]*models.Performer, error)
}

type GroupNamesFinder interface {
	FindByNames(ctx context.Context, names []string, nocase bool) ([]*models.Group, error)
}

type SceneRelationships struct {
	PerformerFinder PerformerFinder
	TagFinder       models.TagNameFinder
	StudioFinder    StudioFinder
}

// MatchRelationships accepts a scraped scene and attempts to match its relationships to existing stash models.
func (r SceneRelationships) MatchRelationships(ctx context.Context, s *models.ScrapedScene, endpoint string) error {
	thisStudio := s.Studio
	for thisStudio != nil {
		if err := ScrapedStudio(ctx, r.StudioFinder, thisStudio, endpoint); err != nil {
			return err
		}

		thisStudio = thisStudio.Parent
	}

	for _, p := range s.Performers {
		err := ScrapedPerformer(ctx, r.PerformerFinder, p, endpoint)
		if err != nil {
			return err
		}
	}

	for _, t := range s.Tags {
		err := ScrapedTag(ctx, r.TagFinder, t, endpoint)
		if err != nil {
			return err
		}
	}

	return nil
}

// ScrapedPerformer matches the provided performer with the
// performers in the database and sets the ID field if one is found.
func ScrapedPerformer(ctx context.Context, qb PerformerFinder, p *models.ScrapedPerformer, stashBoxEndpoint string) error {
	if p.StoredID != nil || p.Name == nil {
		return nil
	}

	// Check if a performer with the StashID already exists
	if stashBoxEndpoint != "" && p.RemoteSiteID != nil {
		performers, err := qb.FindByStashID(ctx, models.StashID{
			StashID:  *p.RemoteSiteID,
			Endpoint: stashBoxEndpoint,
		})
		if err != nil {
			return err
		}
		if len(performers) > 0 {
			id := strconv.Itoa(performers[0].ID)
			p.StoredID = &id
			return nil
		}
	}

	performers, err := qb.FindByNames(ctx, []string{*p.Name}, true)
	if err != nil {
		return err
	}

	if len(performers) == 0 {
		// if no names matched, try match an exact alias
		performers, err = performer.ByAlias(ctx, qb, *p.Name)
		if err != nil {
			return err
		}
	}

	// WHY THE LENGTH IS NOT CHECKED FOR EXACTLY ONE.
	//
	// A library may legitimately hold several performers sharing a name and
	// distinguished by disambiguation -- that is what disambiguation is for,
	// and the uniqueness constraint is on (name, disambiguation), never on
	// name alone. The name lookup is case-insensitive and ignores
	// disambiguation, so it returns every performer carrying this name.
	//
	// Requiring exactly one match treated that ambiguity as ABSENCE. StoredID
	// was left nil, the caller concluded the performer did not exist and
	// created it, and the insert then failed on the database's own uniqueness
	// constraint, surfacing as
	//
	//	error creating performer: ... UNIQUE constraint failed: performers.name
	//
	// and through the create-then-save path as
	//
	//	performer with name 'X' already exists
	//
	// which reads as a failed existence check. The check did not fail; its
	// result was discarded because there was more than one of it.
	//
	// THE FIX: disambiguate with the disambiguation the scraper itself
	// supplied, and only with that. ScrapedPerformer carries a
	// Disambiguation field which the name lookup never looked at, so the
	// information needed to resolve the collision was present and unused.
	//
	// If that narrows the candidates to one, the match is made. If it does
	// not -- no disambiguation supplied, or several candidates still share it
	// -- the name stays unmatched. Guessing between two real people is worse
	// than not matching, so ambiguity is never resolved by picking the first
	// row. It resolves to "create it, and let the user see the name that
	// collided", which is a state they can act on.
	//
	// Note this does NOT make the ambiguous case a duplicate any more: if the
	// library holds two performers named "Mia Ipanema" and the scraper
	// proposes the same name, creation still trips the constraint. What
	// changes is that the collision is now reported as a name that is
	// genuinely occupied rather than as a mysterious UNIQUE failure on a
	// column the user never knew was unique, and the create path can key off
	// the disambiguation the scraper gave us.
	if len(performers) == 0 {
		// genuinely absent -- create it
		return nil
	}

	if len(performers) > 1 {
		// Ambiguous by name. Resolve it the way the database itself would:
		// on (name, disambiguation). The scraper's Disambiguation is the
		// only disambiguating input we have, and using anything else would be
		// a guess dressed up as a lookup.
		//
		// A candidate with an empty disambiguation and a scraper that sent ""
		// compare equal, which is correct: NULL and the empty string are the
		// same statement about a person, and treating them as different would
		// fail to resolve a case that is in fact decided.
		if p.Disambiguation != nil {
			var narrowed []*models.Performer
			for _, cand := range performers {
				if cand.Disambiguation == *p.Disambiguation {
					narrowed = append(narrowed, cand)
				}
			}
			if len(narrowed) == 1 {
				id := strconv.Itoa(narrowed[0].ID)
				p.StoredID = &id
			}
		}
		// If it is still ambiguous, StoredID stays nil on purpose. See above.
		return nil
	}

	id := strconv.Itoa(performers[0].ID)
	p.StoredID = &id
	return nil
}

type StudioFinder interface {
	models.StudioQueryer
	FindByStashID(ctx context.Context, stashID models.StashID) ([]*models.Studio, error)
}

// ScrapedStudio matches the provided studio with the studios
// in the database and sets the ID field if one is found.
func ScrapedStudio(ctx context.Context, qb StudioFinder, s *models.ScrapedStudio, stashBoxEndpoint string) error {
	if s.StoredID != nil {
		return nil
	}

	// Check if a studio with the StashID already exists
	if stashBoxEndpoint != "" && s.RemoteSiteID != nil {
		studios, err := qb.FindByStashID(ctx, models.StashID{
			StashID:  *s.RemoteSiteID,
			Endpoint: stashBoxEndpoint,
		})
		if err != nil {
			return err
		}
		if len(studios) > 0 {
			id := strconv.Itoa(studios[0].ID)
			s.StoredID = &id
			return nil
		}
	}

	st, err := studio.ByName(ctx, qb, s.Name)

	if err != nil {
		return err
	}

	if st == nil {
		// try matching by alias
		st, err = studio.ByAlias(ctx, qb, s.Name)
		if err != nil {
			return err
		}
	}

	if st == nil {
		// ignore - cannot match
		return nil
	}

	id := strconv.Itoa(st.ID)
	s.StoredID = &id
	return nil
}

// ScrapedStudioHierarchy executes ScrapedStudio for the provided studio and its parents recursively.
func ScrapedStudioHierarchy(ctx context.Context, qb StudioFinder, s *models.ScrapedStudio, stashBoxEndpoint string) error {
	if err := ScrapedStudio(ctx, qb, s, stashBoxEndpoint); err != nil {
		return err
	}

	if s.Parent == nil {
		return nil
	}

	return ScrapedStudioHierarchy(ctx, qb, s.Parent, stashBoxEndpoint)
}

// ScrapedGroup matches the provided movie with the movies
// in the database and returns the ID field if one is found.
func ScrapedGroup(ctx context.Context, qb GroupNamesFinder, storedID *string, name *string) (matchedID *string, err error) {
	if storedID != nil || name == nil {
		return
	}

	movies, err := qb.FindByNames(ctx, []string{*name}, true)

	if err != nil {
		return
	}

	if len(movies) != 1 {
		// ignore - cannot match
		return
	}

	id := strconv.Itoa(movies[0].ID)
	matchedID = &id
	return
}

// ScrapedTagHierarchy executes ScrapedTag for the provided tag and its parent.
func ScrapedTagHierarchy(ctx context.Context, qb models.TagNameFinder, s *models.ScrapedTag, stashBoxEndpoint string) error {
	if err := ScrapedTag(ctx, qb, s, stashBoxEndpoint); err != nil {
		return err
	}

	if s.Parent == nil {
		return nil
	}

	// Match parent by name only (categories don't have StashDB tag IDs)
	return ScrapedTag(ctx, qb, s.Parent, "")
}

// ScrapedTag matches the provided tag with the tags
// in the database and sets the ID field if one is found.
func ScrapedTag(ctx context.Context, qb models.TagNameFinder, s *models.ScrapedTag, stashBoxEndpoint string) error {
	if s.StoredID != nil {
		return nil
	}

	// Check if a tag with the StashID already exists
	if stashBoxEndpoint != "" && s.RemoteSiteID != nil {
		if finder, ok := qb.(models.TagFinder); ok {
			tags, err := finder.FindByStashID(ctx, models.StashID{
				StashID:  *s.RemoteSiteID,
				Endpoint: stashBoxEndpoint,
			})
			if err != nil {
				return err
			}
			if len(tags) > 0 {
				id := strconv.Itoa(tags[0].ID)
				s.StoredID = &id
				return nil
			}
		}
	}

	t, err := tag.ByName(ctx, qb, s.Name)

	if err != nil {
		return err
	}

	if t == nil {
		// try matching by alias
		t, err = tag.ByAlias(ctx, qb, s.Name)
		if err != nil {
			return err
		}
	}

	if t == nil {
		// ignore - cannot match
		return nil
	}

	id := strconv.Itoa(t.ID)
	s.StoredID = &id
	return nil
}
