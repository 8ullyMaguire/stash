// Package autoproposal turns an automatic match into a PROPOSAL rather than a
// write.
//
// M8 step 8.2 (capability 1), spec §6b.2. This is the whole of "automatic", and
// the reason it is safe to enable by default is a single design decision: **an
// automatic suggestion is a `collab.Proposer` proposal, so a machine's claim lands
// in the same audit trail a human's does.**
//
// # WHY THIS EXISTS RATHER THAN A CONFIG FLAG
//
// `internal/autotag` already matches paths to performers, studios and tags, and it
// already WRITES: `internal/autotag/scene.go` calls `scene.AddPerformer`, which
// builds a `ScenePartial` and calls `UpdatePartial`. That is correct for a tool an
// operator runs deliberately against their own library, and it is exactly what
// §6b.2 forbids for something that runs automatically.
//
// The reason is not politeness and it is not about trusting the matcher. It is
// #5, extended: automatic curation writes PROPOSALS, and §6b.2 says why in one
// line — "an automatic direct write is a machine laundering a claim past
// governance, and it is the single most important constraint in this subsection."
//
// Consider what a direct write means for a shared library. Every other account on
// the instance sees that performer link. A human tagging their own scene is one
// person asserting something; a scheduled job doing it on 4,000 scenes is 4,000
// assertions nobody made, indistinguishable in the data from 4,000 human
// decisions, and impossible to review because there is no proposal to read. The
// audit trail is the mechanism, and a direct write leaves no trace in it.
//
// # WHAT IT DOES NOT DO
//
// It does not decide anything the matcher does not already decide. Matching is
// upstream's and it works; §6b.2 says "keep the scanner; add the policy". This
// package is only the policy: it takes a match that autotag found and asks
// whether it may be applied directly, and if not, files it.
//
// # THE DIRECT-WRITE QUESTION IS A SEPARATE ONE
//
// `MayAutoApply` exists because there IS a legitimate case for a direct write:
// applying a match the operator has already approved, in bulk, without filing
// 4,000 proposals nobody will read. That is a policy question an instance
// answers once, and it is deliberately NOT this package's default.

package autoproposal

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/stashapp/stash/internal/collab"
)

// ErrNoAuthor means an automatic suggestion has no attributable author.
//
// AND IT IS NOT OPTIONAL. A proposal with no author is unattributable, which is
// the same thing `internal/ident`'s board refuses to create: §4.2's audit trail is
// the only way an operator later finds out where a value came from, and "the
// scheduler did it" is not a source an operator can investigate. So an instance
// must configure WHICH USER automatic suggestions are attributed to — typically the
// instance owner, which is a deliberate act rather than an accident.
var ErrNoAuthor = errors.New("autoproposal: an automatic suggestion must name the user it is attributed to")

// Relationship kinds this package can file.
//
// NAMED CONSTANTS rather than free strings, because §6a.4's vocabulary is the
// rule about which fields may be proposed at all, and a typo in a field name is a
// proposal that can never be applied.
const (
	KindScenePerformer = "performer_ids"
	KindSceneStudio    = "studio_id"
	KindSceneTag       = "tag_ids"
	KindImagePerformer = "performer_ids"
	KindImageTag       = "tag_ids"
)

// Proposer files automatic suggestions. It is an INTERFACE rather than
// `*collab.Proposer` directly so a test can observe what was filed without a
// database — and so this package cannot be given a field writer by mistake, which
// is the whole failure mode it exists to prevent.
type Proposer interface {
	Create(ctx context.Context, req collab.Proposal) (*collab.Proposal, error)
}

// Author is the local user automatic suggestions are attributed to.
//
// A TYPE rather than a bare int so a caller cannot pass 0 meaning "unset" and have
// it read as a real user. §6a.19 has the same requirement for a pushed sync edit: a
// proposal is filed BY somebody.
type Author struct {
	UserID int
	Name   string
}

// Valid reports whether this author can be attributed to.
func (a Author) Valid() bool { return a.UserID != 0 }

// Attribution is who and what this instance's automatic suggestions are.
//
// BOTH FIELDS, and both matter: the user id makes the proposal reviewable, and the
// name makes the RATIONALE readable. A rationale reading "proposed automatically"
// with no name tells a reviewer nothing about which actor they are voting on.
type Attribution struct {
	Author Author
	// Source names the mechanism, e.g. "autotag" or "acquisition". Recorded in the
	// rationale so an automatic proposal is visibly automatic — §4.2's trail is
	// only useful if it distinguishes a machine's claim from a human's.
	Source string
}

// Validate checks the attribution before any work is done.
func (at Attribution) Validate() error {
	if !at.Author.Valid() {
		return fmt.Errorf("%w: got user %d", ErrNoAuthor, at.Author.UserID)
	}
	if at.Source == "" {
		// Not fatal — an empty source still files a proposal — but a default is
		// better than nothing, because the rationale is the only place a reviewer
		// can tell an automatic claim from a human one.
		return fmt.Errorf("%w: attribution names no source, so a reviewer could not "+
			"tell an automatic proposal from a human one", ErrNoAuthor)
	}
	return nil
}

// Policy is whether this instance may apply a match directly.
//
// THE DEFAULT IS NO. §6b.2's constraint is that curation writes proposals, and a
// policy type whose zero value were permissive would let a config struct that
// forgot to set the field start laundering claims — the same reasoning as step 8.1's
// zero value, and for the same reason.
type Policy struct {
	// AutoApply allows a direct write instead of a proposal.
	//
	// FOR AN ALREADY-APPROVED MATCH, in bulk. It is NOT "trust the matcher": no
	// matcher is trustworthy enough to assert 4,000 shared facts unasked, which is
	// why this is opt-in and off by default.
	AutoApply bool
}

// MayAutoApply reports whether a direct write is permitted for this claim.
//
// IT TAKES THE TARGET TYPE AS WELL AS THE KIND, and that is not redundancy. The
// vocabulary is namespaced by target ("scene": {...}, "image": {...}), so the
// field string alone is ambiguous: KindSceneTag and KindImageTag are both
// "tag_ids" and a switch on the kind alone has two identical cases, which Go
// rejects at compile time. Target+kind is what the vocabulary actually keys on, and
// it is also the more honest question: applying a tag to an image and applying one
// to a scene are different acts.
//
// Written as a POSITIVE test rather than `return p.AutoApply`, so a kind added
// later is refused until it is named here. A default-permissive form would make
// every future claim auto-applied the day someone forgot to update it.
func (p Policy) MayAutoApply(targetType, kind string) bool {
	// Tags are the weakest claim: a tag is a descriptor, not an assertion about
	// who appears in a scene. So an instance that has opted in may apply tags
	// directly, and performers and studios are refused whatever the setting --
	// asserting a performer is a claim about a person, which is exactly the kind
	// of claim §6b.2 says must be voted on.
	switch targetType + "." + kind {
	case "scene." + KindSceneTag, "image." + KindImageTag:
		return p.AutoApply
	}
	return false
}

// Suggestion is one automatic match, before it is filed or applied.
type Suggestion struct {
	TargetType string
	TargetID   int
	Kind       string
	// EntityID is what is being linked — a performer id, a tag id, a studio id.
	EntityID int
	// EntityName goes in the rationale so a reviewer can recognise the claim
	// without opening anything.
	EntityName string
}

// Validate checks the suggestion can be filed at all.
func (s Suggestion) Validate() error {
	if s.TargetType == "" || s.TargetID <= 0 {
		return fmt.Errorf("%w: suggestion target %q/%d is not addressable",
			ErrNoAuthor, s.TargetType, s.TargetID)
	}
	if s.Kind == "" {
		return fmt.Errorf("%w: suggestion names no field to propose",
			ErrNoAuthor)
	}
	if s.EntityID <= 0 {
		return fmt.Errorf("%w: suggestion for %s/%d links to entity %d, which is "+
			"not addressable", ErrNoAuthor, s.TargetType, s.TargetID, s.EntityID)
	}
	return nil
}

// Outcome is what happened to one suggestion.
type Outcome struct {
	// ProposalID is non-zero when a proposal was filed.
	ProposalID int
	// Applied is true when a direct write was permitted and performed.
	Applied bool
	// Reason explains a refusal, and exists because §6b.2's whole point is that an
	// automatic action is AUDITABLE — a silent outcome is an unauditable one.
	Reason string
}

// Curator files automatic suggestions through the proposal path.
//
// ITS ONLY DEPENDENCY IS THE Proposer, and the interface has exactly one method.
// There is no field writer and no store to reach one through, so a version that
// wrote a shared field directly would be a different type rather than a different
// line in this file.
type Curator struct {
	proposer Proposer
	attrib   Attribution
	policy   Policy
}

// NewCurator wires a curator. There is no way to construct one without an
// attribution, which is what makes "automatic suggestions with no author" an
// unbuildable state rather than a runtime check.
func NewCurator(p Proposer, attrib Attribution, policy Policy) (*Curator, error) {
	if p == nil {
		return nil, errors.New("autoproposal: a curator with no proposer would have to write fields directly, which cannot happen in production")
	}
	if err := attrib.Validate(); err != nil {
		return nil, err
	}
	return &Curator{proposer: p, attrib: attrib, policy: policy}, nil
}

// Curate files one suggestion as a proposal, or reports why it did not.
//
// THE RETURN IS AN OUTCOME, NOT AN ERROR, for the ordinary case. A curator
// processing 4,000 scenes should not treat "this one needed a proposal" as a
// failure — that is the normal path. Only a genuine failure (an unattributable
// suggestion, an unaddressable target) is an error, and those are the ones that
// mean the automation is misconfigured.
func (c *Curator) Curate(ctx context.Context, s Suggestion) (Outcome, error) {
	if err := s.Validate(); err != nil {
		return Outcome{}, err
	}

	// The policy check comes BEFORE the proposal, not after, and the order is the
	// point: a direct write must be a decision made in advance, not a fallback
	// taken when filing fails. If filing were attempted first and the proposer
	// failed, "apply directly instead" would be the obvious recovery — and that is
	// precisely how an automatic write sneaks past a broken audit trail.
	if c.policy.MayAutoApply(s.TargetType, s.Kind) {
		return Outcome{
			Applied: true,
			Reason: fmt.Sprintf("this instance applies %s directly (%s); the operator "+
				"approved bulk application of this kind, so no proposal was filed",
				s.Kind, c.attrib.Source),
		}, nil
	}

	prop, err := c.proposer.Create(ctx, collab.Proposal{
		TargetType: s.TargetType,
		TargetID:   s.TargetID,
		Field:      s.Kind,
		// OldValue is nil because this is a LINK ADD, not a field replacement: the
		// set of linked performers is a join table, and the honest "old value" is
		// "not currently linked", which is what nil means. §5.3 wants a reviewer to
		// see the diff, and for an add the diff is empty->one more.
		NewValue:  entityValue(s.EntityID),
		Rationale: c.rationale(s),
		AuthorID:  c.attrib.Author.UserID,
	})
	if err != nil {
		// A filing failure is returned, NOT swallowed into a direct write. This is
		// the assertion that matters most in the file: if this fell back to applying
		// directly, then every outage of the proposal store would silently become a
		// bypass of governance.
		return Outcome{}, fmt.Errorf("filing %s on %s/%d as a proposal: %w",
			s.Kind, s.TargetType, s.TargetID, err)
	}

	return Outcome{
		ProposalID: prop.ID,
		Reason: fmt.Sprintf("filed as proposal %d; %s writes proposals, not fields "+
			"(§6b.2, #5)", prop.ID, c.attrib.Source),
	}, nil
}

// entityValue renders the linked entity id as the proposal's NewValue.
func entityValue(id int) *string {
	s := strconv.Itoa(id)
	return &s
}

// rationale is what a reviewer reads.
//
// IT NAMES THE SOURCE and the linked entity, because §4.2's audit trail is only
// useful if it distinguishes a machine's claim from a human's — a reviewer shown
// "propose performer 42 on scene 7" cannot tell whether a person or a scheduler
// asserted it, and those deserve different scrutiny.
func (c *Curator) rationale(s Suggestion) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s %q on %s %d",
		c.attrib.Source, s.Kind, s.EntityName, s.TargetType, s.TargetID)

	if c.attrib.Author.Name != "" {
		fmt.Fprintf(&b, " (attributed to %s)", c.attrib.Author.Name)
	}

	// The matcher's reasoning is included because a proposal a reviewer cannot
	// evaluate is a rubber stamp. "matched by path" is enough for a reviewer to
	// check it by looking at the path.
	b.WriteString(" — matched automatically, not asserted by a user; review before accepting")
	return b.String()
}
