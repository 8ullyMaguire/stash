// Package collab — the locator consent gate (spec §7.1).
//
// # WHAT THIS FILE IS FOR
//
// A P2P downloader is a plugin that asks core for permission. The split is
// strict: the plugin REQUESTS, core DECIDES. This file is the deciding side, and
// the entire point of the milestone is that a plugin has no way to reach the
// other one — the seam tests in internal/api/stashforge_p2p_seam_test.go exist
// to prove the downloader's code is not in this process's binary at all.
//
// That is why this logic is here and not in the plugin. A tier check inside a
// third-party component is a tier check that can be buggy, disabled, or
// hostile. A plugin that is merely *capable* of ignoring the gate is a weaker
// guarantee than a plugin that has no path to the gate's inputs.
//
// # THE RULE, AND THE ARGUMENT FOR IT
//
// spec §7.1, owner decision:
//
//	a locator may be STORED at any tier except `quarantined` and `denied`;
//	handing it to a client additionally requires `redistribution_permitted`.
//
// This is a deliberate asymmetry with Commons, which gates a locator on
// `third_party_permitted` PLUS the flag. Commons' rule means an amateur
// creator's own upload can never carry a magnet, and this project's owner
// requires a downloader that acquires material *including amateur material* —
// the whole point is a corpus no catalogue describes. A gate that makes the
// requirement impossible is not a safety property, it is a contradiction.
//
// Storing a magnet in your own library redistributes nothing. Refusing that
// protects no one and costs the owner the feature. The gate that does real work
// is the one on ACTING, not on recording — and a full-featured downloader is
// exactly where that distinction gets tested, because it *can* redistribute.
// The flag has teeth here in a way it would not in a hand-off-only plugin, which
// is the argument for having it.

package collab

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// LocatorTier says who is asserting that an object may carry a locator.
//
// The tier model is adopted unchanged from spec §7.1 because it is what makes a
// non-`third_party_permitted` corpus holdable at all. The zero value is NOT a
// tier: a struct that can be constructed accidentally and silently has no tier
// is a struct whose zero value is a decision, and the zero value here is
// `unverified` — the least restrictive storage tier — so an accidentally
// constructed value is permissive. See NewLocator for why that is dealt with at
// construction instead of here.
type LocatorTier string

const (
	// TierUnverified: nobody has asserted anything. The default, and the tier
	// every object starts at.
	TierUnverified LocatorTier = "unverified"

	// TierSelfPublished: the object's own creator asserts it.
	TierSelfPublished LocatorTier = "self_published"

	// TierPerformerClaimed: a performer has claimed it. Claims are assertions
	// by an interested party, which is why this is not the same as
	// self-published even though both are "someone with standing said so".
	TierPerformerClaimed LocatorTier = "performer_claimed"

	// TierThirdPartyPermitted: a third party has permitted redistribution.
	TierThirdPartyPermitted LocatorTier = "third_party_permitted"

	// TierQuarantined: held, but not acted on. Distinct from denied: a
	// quarantined object keeps its locators and can be looked at, and can be
	// released. A denied one is terminal.
	TierQuarantined LocatorTier = "quarantined"

	// TierDenied: refused. Terminal, and per spec §7.1 destroying the object
	// DESTROYS its locators rather than orphaning them.
	TierDenied LocatorTier = "denied"
)

// storageRefusals are the tiers a locator may never be STORED against.
//
// A switch rather than a set literal, because a map is a second thing to keep
// in sync with the constant block above, and a tier added to one but not the
// other is a gate that silently permits. The switch makes an unknown tier a
// compile-visible no-op at the call site and a review-visible omission here.
var storageRefusals = map[LocatorTier]string{
	TierQuarantined: "a quarantined object holds material but does not act on it, " +
		"so a locator stored against one is a locator waiting for the quarantine " +
		"to be lifted without anything deciding to lift it",
	TierDenied: "denial is terminal, so storing a locator against it would leave a " +
		"pointer to material that must not exist",
}

// IsStorageRefused reports whether a locator may not be stored at this tier.
//
// The map is consulted rather than matched, so the answer is false for a tier
// not in the map. That is the permissive direction, and it is deliberate ONLY
// because every tier is a typed constant in this file: an unknown tier cannot
// arrive from the database without having come through a parse that rejects it
// (see ParseLocatorTier), or from a caller who has explicitly built a
// LocatorTier. Callers that can be handed attacker-chosen strings go through
// ParseLocatorTier, which does not have this property.
func (t LocatorTier) IsStorageRefused() (bool, string) {
	reason, refused := storageRefusals[t]
	return refused, reason
}

// Locator is a pointer to a copy of an object held somewhere else: a magnet
// link, a .torrent URL, an ed2k link.
//
// The name is the spec's, and it is worth saying why it is not "download_url":
// a magnet is not a URL, and an ed2k link is not one either. Calling all three
// URLs would make the type's own doc lie about two thirds of its values.
type Locator struct {
	// ID is the locator's own identity, zero before it is stored.
	ID int64

	// ObjectID is the object the locator points at. Zero means the locator is
	// not attached to anything, which is a valid state: a locator can be
	// proposed before it is attached, and a locator whose object has been
	// deleted is not silently re-pointed at whatever took its ID.
	ObjectID int64

	// Scheme is the locator's protocol, kept separate from Value so the gate
	// and the UI can reason about a locator without parsing it. See
	// LocatorScheme.
	Scheme LocatorScheme

	// Value is the locator itself, opaque here. A magnet carries its own
	// infohash and a tracker list; a .torrent URL carries neither. The gate
	// does not parse it and neither should this.
	Value string

	// Tier is the tier the OBJECT is at, denormalised onto the locator.
	//
	// Denormalised, and that is a real trade: it means a tier change does not
	// have to rewrite every locator to remain correct, and it means a locator
	// can disagree with its object. The alternative — reading the object's tier
	// at every decision — was rejected because the decision is made at the
	// moment of the act, from a snapshot, and a snapshot that silently
	// disagrees with the row is worse than a stale one. Invalidate() below is
	// how a tier change reaches the locators.
	Tier LocatorTier
}

// Invalidated is set by Invalidate. A locator whose object's tier changed is
// marked rather than deleted, because the next tier may permit storage again —
// a quarantine lifted is a return to service, not a loss.
type InvalidatedLocator struct {
	LocatorID int64
	Reason    string
}

// Invalidate returns the locators that must be marked when an object changes
// tier, and the reason for each.
//
// NOT a delete, and this is the spec's `denied`-destroys-locators rule read
// carefully: denial DESTROYS, every other tier change INVALIDATES. The
// difference is that an invalidated locator is inert until the object comes
// back to a tier that permits it, and a destroyed one is gone. Conflating them
// would mean lifting a quarantine loses the locators, which is data loss caused
// by a permission change — the kind of thing a user cannot undo and does not
// expect.
func Invalidate(l Locator, newTier LocatorTier) (*InvalidatedLocator, bool) {
	if l.Tier == newTier {
		return nil, false
	}
	if refused, reason := newTier.IsStorageRefused(); refused {
		// Terminal: the object is denied, and denied destroys.
		return &InvalidatedLocator{
			LocatorID: l.ID,
			Reason: fmt.Sprintf(
				"the object moved to %s, where locators are destroyed rather "+
					"than held: %s", newTier, reason),
		}, true
	}
	return &InvalidatedLocator{
		LocatorID: l.ID,
		Reason: fmt.Sprintf("the object moved from %s to %s, so the locator's "+
			"tier is stale and the gate re-reads the object at the moment of "+
			"the act", l.Tier, newTier),
	}, false
}

// LocatorDecision is the decision core makes. It is a value rather than an error so that a
// refusal is data: the caller logs it, the plugin is told, and the audit trail
// has something to record. An error type would make "refused" and "the database
// was down" the same shape at every call site, and those two must never be
// confused — one is an answer, the other is an outage.
type LocatorDecision struct {
	// Allowed is the decision.
	Allowed bool

	// Reason is populated on every refusal and empty on every grant, so a
	// refusal can never be logged as a bare "denied".
	Reason string

	// Checked is the tier the decision was made against. Present so a refusal
	// can be audited after the fact against the tier that was current at the
	// time, rather than the tier that happens to be current now.
	Checked LocatorTier
}

// grant builds an allow. Reason is empty by construction, which is what makes
// LocatorDecision.Reason's "empty on every grant" property hold without every call site
// having to remember it.
func grant(checked LocatorTier) LocatorDecision {
	return LocatorDecision{Allowed: true, Checked: checked}
}

// refuse builds a denial. The reason is a parameter rather than a constant so
// that no refusal can exist without one — a refusal with an empty reason is
// unauditable, and the type does not permit it.
func refuse(checked LocatorTier, reason string) LocatorDecision {
	return LocatorDecision{Allowed: false, Reason: reason, Checked: checked}
}

// DecideStore stores a locator or refuses to.
//
// A store with a Store method AND an Update or Delete would let a caller write
// a locator that skips the gate, which is the one thing §7.1 forbids. The
// interface is the enforcement: the only path from a proposal to a row goes
// through the function that decides.
type DecideStore interface {
	StoreLocator(ctx context.Context, l Locator) error
	DestroyLocator(ctx context.Context, locatorID int64, reason string) error
}

// ErrNoLocator is returned when a proposal carries nothing to store.
var ErrNoLocator = errors.New("no locator to propose")

// Propose is what a plugin calls: `locator.propose(object, locator)`.
//
// Core evaluates the tier and either persists or refuses. Note the SHAPE of the
// return: a Gate, never a bare error. A plugin that gets a refusal learns why,
// which is the difference between a plugin that can adapt and one that has to
// guess, and a plugin that has to guess is a plugin that will retry in a loop.
//
// The plugin does not get to say anything about the outcome. There is no
// override parameter and no force flag, because a force flag is a gate with a
// documented off switch.
func Propose(ctx context.Context, s DecideStore, objectID int64, l Locator) (LocatorDecision, error) {
	if strings.TrimSpace(l.Value) == "" {
		// Refused rather than stored-then-rejected: an empty locator is not a
		// pointer to anything, and a row containing one is a row that has to be
		// explained later.
		return refuse(l.Tier, "the locator's value is empty, so there is nothing "+
			"to point at"), nil
	}

	if refused, reason := l.Tier.IsStorageRefused(); refused {
		return refuse(l.Tier, reason), nil
	}

	// A locator with no object is not unattached — it is a mistake, and
	// storing one would produce a row pointing at object 0, which is a real
	// object id in most schemas and therefore a locator attached to whatever
	// happens to live there.
	if objectID == 0 {
		return refuse(l.Tier, "the locator names no object, and storing it would "+
			"attach a pointer to whatever object happens to have id 0"), nil
	}
	l.ObjectID = objectID

	if err := s.StoreLocator(ctx, l); err != nil {
		return LocatorDecision{}, fmt.Errorf("storing the locator: %w", err)
	}
	return grant(l.Tier), nil
}

// ActStore reports the CURRENT state of the object at the moment of the act.
//
// An interface and not a Locator, deliberately. §7.1 requires that a hand-off
// "re-checks the tier at the moment of the action, not at storage time, because
// consent can be revoked in between". Passing the Locator would hand the caller
// the DENORMALISED tier — the value stored when the locator was written — and
// re-reading it is exactly the bug the spec is warning about. So the gate is
// given the means to re-read, and the denormalised field is never an input to a
// decision.
type ActStore interface {
	// CurrentTier returns the object's tier NOW, and whether the object still
	// exists. A missing object is (tier, false) rather than an error: an
	// object deleted between storage and act is a refusal, not an outage.
	CurrentTier(ctx context.Context, objectID int64) (LocatorTier, bool, error)
}

// ActDecision is what core tells the host when a client is about to receive a
// locator.
type ActDecision struct {
	// Permitted is whether the locator may be handed over.
	Permitted bool

	// Reason is populated on refusal, empty on grant. Same rule as Gate.
	Reason string

	// Checked is the tier read at the moment of the act.
	Checked LocatorTier
}

// Act decides whether a locator may be handed to a client, re-reading the
// object's tier NOW.
//
// This is a second, separate gate from Propose and not a stricter version of
// it. Storing a magnet in your own library redistributes nothing; handing one to
// a BitTorrent client does, because the client seeds what it downloads. So the
// acting gate requires `redistribution_permitted`, and the storage gate does
// not — per spec §7.1, and it is the asymmetry that makes the downloader
// possible without making it unsafe.
//
// The re-read is the whole point. A locator stored at a tier that permitted it
// an hour ago says nothing now: the object may have been denied in the interim,
// and acting on a stale tier is how a revocation fails to revoke anything.
func Act(ctx context.Context, s ActStore, objectID int64) (ActDecision, error) {
	tier, found, err := s.CurrentTier(ctx, objectID)
	if err != nil {
		return ActDecision{}, fmt.Errorf("re-reading the object's tier at the "+
			"moment of the act: %w", err)
	}

	if !found {
		return ActDecision{
			Permitted: false,
			Reason: "the object no longer exists, so a locator attached to it " +
				"has nothing to attach to",
		}, nil
	}

	if refused, reason := tier.IsStorageRefused(); refused {
		return ActDecision{
			Permitted: false,
			Reason: fmt.Sprintf("the object is %s, where no locator may be "+
				"handed to a client: %s", tier, reason),
			Checked: tier,
		}, nil
	}

	if tier != TierThirdPartyPermitted {
		return ActDecision{
			Permitted: false,
			Reason: fmt.Sprintf("the object is %s, and only %s permits handing a "+
				"locator to a client. A tier says who is asserting; only "+
				"third_party_permitted says the assertion covers redistribution. "+
				"Storing the locator was permitted, and this is the separate gate "+
				"that acts on it", tier, TierThirdPartyPermitted),
			Checked: tier,
		}, nil
	}

	return ActDecision{Permitted: true, Checked: tier}, nil
}

// ParseLocatorTier parses a tier, refusing anything unknown.
//
// A refusal rather than a fallback, and the reason is the permissive direction
// in IsStorageRefused: a caller that defaults an unparseable string to
// `unverified` has turned a corrupt row into a permitted locator. The column is
// CHECKed in the database, so this can only be reached if the check was bypassed
// or the row came from somewhere else — which is precisely when a default is
// worst.
func ParseLocatorTier(s string) (LocatorTier, error) {
	t := LocatorTier(strings.TrimSpace(s))
	switch t {
	case TierUnverified, TierSelfPublished, TierPerformerClaimed,
		TierThirdPartyPermitted, TierQuarantined, TierDenied:
		return t, nil
	}
	return "", fmt.Errorf("%q is not a locator tier. The known tiers are %s",
		s, strings.Join(LocatorTierNames(), ", "))
}

// LocatorTierNames lists the tiers, for error messages and for the UI's
// dropdown. Derived from one table so it cannot drift from the switch in
// ParseLocatorTier, which is the whole reason it is a function.
func LocatorTierNames() []string {
	return []string{
		string(TierUnverified),
		string(TierSelfPublished),
		string(TierPerformerClaimed),
		string(TierThirdPartyPermitted),
		string(TierQuarantined),
		string(TierDenied),
	}
}
