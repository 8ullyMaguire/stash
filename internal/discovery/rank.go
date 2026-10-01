// Package discovery ranks content, and computes nothing it stores.
//
// M7 step 7.3 (R010–R013, R014, R015, R061, R064, R065, R071, R073), spec §6a.5.
//
// WHY THERE IS NO MIGRATION FOR THIS STEP. A `recommendation_score` column would
// be a stored score, which non-negotiable #4 forbids — and the practical
// consequence matters more than the rule: a term replaced in the config takes
// effect immediately, because there is nothing to recompute. That is the "gravity
// slider" the brief asks for, and it is a slider over a VIEW's parameters rather
// than over stored state. TestNoRecommendationScoreColumnExists asserts the
// absence structurally, since an absent column cannot fail a test on its own.
//
// THE THREE TERMS, all computed at query time from records:
//
//	personal taste match — the user's own fingerprint (§6a.3)
//	instance gravity    — the operator's theme (§6a.8)
//	peer similarity     — how close the source instance's taste is (§6a.2)
//
// And the constraint that outranks all three: a user's explicit preference
// returns that entity (§6a.5, non-negotiable #6). Ranking shapes an UNORDERED
// set; it never filters one. See ExplicitRequest.
package discovery

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// Fingerprint is a derived taste vector over one user's votes, tags, reviews and
// curation (§6a.3).
//
// IT CARRIES NO IDENTITY, NO CREDENTIALS AND NO CONSENT, and that is not
// caution, it is the point. Consent is per-instance and re-prompted when the
// disclosure changes; a fingerprint that travelled with consent would make
// opting out on one instance a lie, because the next instance would already hold
// the decision. So there is no user id in this struct, and adding one would let
// a peer correlate the same person across instances from taste alone.
type Fingerprint struct {
	// Tags maps a tag to the weight the user gives it. Derived, never stored as
	// a score.
	Tags map[string]float64
	// Studios and Performers work the same way. Three maps rather than one keyed
	// by a prefixed string, because a prefix scheme lets a tag named
	// "tag:studio:x" collide with a studio actually called "tag:studio:x".
	Studios    map[string]float64
	Performers map[string]float64
}

// IsZero reports a fingerprint that would rank everything the same. Such a user
// gets the instance's ordering and nothing personal, which is the honest answer
// rather than a fabricated taste.
func (f Fingerprint) IsZero() bool {
	return len(f.Tags) == 0 && len(f.Studios) == 0 && len(f.Performers) == 0
}

// Gravity is the operator's theme (§6a.8).
//
// A weight over CATEGORIES. It is never an override of an individual entity's
// rank, and there is deliberately no method here that takes an entity id — that
// is what would turn "an instance bends toward jazz" into "this one scene is
// ranked first". Non-negotiable #6 is a structural property of this type.
type Gravity struct {
	Tags       map[string]float64
	Studios    map[string]float64
	Performers map[string]float64
}

// DefaultGravity is the "bend toward nothing" setting: every term zero. Chosen as
// the zero value rather than "uniform" because an instance with no gravity must
// be indistinguishable from one that has never configured any, and a uniform
// default would make the two different while looking the same.
func DefaultGravity() Gravity {
	return Gravity{}
}

// Candidate is one entity available to rank.
type Candidate struct {
	// ID is the LOCAL id. Cross-instance candidates carry their origin
	// separately (§6a.6) — a peer's `scene 412` is not this instance's 412, and
	// this package does not get to decide that.
	ID string

	Tags       []string
	Studio     string
	Performers []string

	// Origin is the instance that supplied it, empty for local. Used for peer
	// similarity only.
	Origin string
}

// Weights are the three terms' coefficients. Replacing one takes effect
// immediately, with no write, because nothing is stored.
type Weights struct {
	Taste   float64
	Gravity float64
	Peer    float64
}

// DefaultWeights is the starting point. Personal taste leads, because §6a.5 lists
// it first and because an operator's theme shaping a user's own feed more than
// the user's own taste inverts the relationship.
func DefaultWeights() Weights {
	return Weights{Taste: 1.0, Gravity: 0.4, Peer: 0.2}
}

// ErrNotFound is returned when an explicit request names something that does not
// exist here. Distinct from "not ranked highly": a user who asked for a specific
// scene and was given a different one has been actively misled.
var ErrNotFound = errors.New("discovery: no such entity")

// Ranked is one scored candidate.
type Ranked struct {
	Candidate
	Score float64
}

// Rank computes the order of an UNORDERED set.
//
// The ordering is deterministic for a given input, which matters more than it
// looks: a ranking that varies between identical queries makes it impossible to
// tell a real change from a re-shuffle, and the sort below therefore breaks ties
// on ID rather than leaving them to the sort's instability.
func Rank(candidates []Candidate, fp Fingerprint, g Gravity, peer map[string]float64, w Weights) []Ranked {
	out := make([]Ranked, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, Ranked{
			Candidate: c,
			Score:     Score(c, fp, g, peer, w),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Score is the three-term sum, exposed separately so a caller can show a
// breakdown or test one term without the others.
func Score(c Candidate, fp Fingerprint, g Gravity, peer map[string]float64, w Weights) float64 {
	taste := tasteMatch(c, fp)
	gravity := gravityMatch(c, g)
	peerTerm := 0.0
	if c.Origin != "" {
		peerTerm = peer[c.Origin]
	}
	return w.Taste*taste + w.Gravity*gravity + w.Peer*peerTerm
}

// tasteMatch is the user's own fingerprint applied to the candidate's tags,
// studio and performers.
//
// Each field is a plain SUM, not a normalised dot product, and that is worth
// stating because it is the obvious thing to "fix" later: normalising by the
// candidate's own tag count would make a candidate with thirty tags rank below
// one with three for the same taste, which rewards thin metadata. A record with
// more relevant evidence should rank higher, not lower.
func tasteMatch(c Candidate, fp Fingerprint) float64 {
	total := 0.0
	for _, t := range c.Tags {
		total += fp.Tags[t]
	}
	total += fp.Studios[c.Studio]
	for _, p := range c.Performers {
		total += fp.Performers[p]
	}
	return total
}

// gravityMatch is the operator's theme, same shape as tasteMatch and for the same
// reason: it is a weight over categories, never an override.
func gravityMatch(c Candidate, g Gravity) float64 {
	total := 0.0
	for _, t := range c.Tags {
		total += g.Tags[t]
	}
	total += g.Studios[c.Studio]
	for _, p := range c.Performers {
		total += g.Performers[p]
	}
	return total
}

// ExplicitRequest is §6a.5's constraint that outranks all three terms: a request
// for a specific entity returns that entity, and no ranking score may bury it
// (non-negotiable #6, §5.1).
//
// IT IS A SEPARATE FUNCTION, not a term in the score, and the reason is that a
// term is a magnitude and a constraint is not. Any score-based encoding of "must
// return this" is a number some other candidate can exceed, which is the bug
// rather than the feature. Ranking shapes an unordered set; the request path
// does not consult the ranking at all.
func ExplicitRequest(candidates []Candidate, wantID string) (Candidate, error) {
	for _, c := range candidates {
		if c.ID == wantID {
			return c, nil
		}
	}
	return Candidate{}, fmt.Errorf("%w: %q", ErrNotFound, wantID)
}

// SanitisePeerTaste bounds a fingerprint accepted from a peer.
//
// §6a.3 makes it explicit: a peer-supplied fingerprint is "an untrusted input of
// the same class as a peer-supplied name" (non-negotiable #9's SanitizeJoin rule)
// — validated, bounded, and never allowed to widen what a user may see.
//
// So this DROPS the magnitudes rather than clamping them. A peer claiming every
// weight is 1000 would otherwise be clamped to some ceiling that is still far
// above anything honest, and a clamp is a number the peer chose; removing it is
// not. What survives is which categories a peer says it likes, which is the part
// that is useful for peer similarity (§6a.2) and cannot grant anything.
func SanitisePeerTaste(in Fingerprint, maxCategories int) Fingerprint {
	if maxCategories < 0 {
		maxCategories = 0
	}

	keep := func(m map[string]float64) map[string]float64 {
		if len(m) == 0 {
			return nil
		}
		// Deterministic order, so two runs over the same input agree — a
		// fingerprint that reorders between calls makes peer similarity
		// untestable.
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > maxCategories {
			keys = keys[:maxCategories]
		}
		out := make(map[string]float64, len(keys))
		for _, k := range keys {
			// Weight becomes presence. See the doc comment.
			out[k] = 1
		}
		return out
	}

	return Fingerprint{
		Tags:       keep(in.Tags),
		Studios:    keep(in.Studios),
		Performers: keep(in.Performers),
	}
}

// Cosine is the peer-similarity measure (§6a.2): how close two instances' tastes
// are.
//
// Returns 0 for a zero vector rather than NaN. A division by a zero norm gives
// NaN, NaN compares false against everything, and a NaN in a sort key makes the
// ORDER of the result depend on the input order — so one peer with no taste would
// make ranking non-deterministic, silently.
func Cosine(a, b Fingerprint) float64 {
	var dot, na, nb float64
	for k, av := range a.Tags {
		bv := b.Tags[k]
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	for k, av := range a.Studios {
		bv := b.Studios[k]
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	for k, av := range a.Performers {
		bv := b.Performers[k]
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
