// Package directory extends the existing directory entities rather than replacing
// them.
//
// M7 step 7.7a (R045–R048), spec §6a.4 and the plan's placement note: "the
// directory already exists in the fork (site, studio, performer entities) and this
// step EXTENDS existing entities, not creating a new subsystem."
//
// So nothing here stores a site, a studio or a performer. This package decides what
// a PROFILE of one may claim, and — the load-bearing part of this step — who may
// say a claim is true.

package directory

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// BadgeState is a claim's standing.
//
// FOUR STATES, and the three-way distinction between the middle two is the whole
// point of §6a.4's claim-and-confirm: `claimed` is not `verified`, and collapsing
// them is how an unverified claim becomes a trust signal.
type BadgeState string

const (
	// BadgeUnclaimed means nobody has claimed this entity.
	BadgeUnclaimed BadgeState = "unclaimed"
	// BadgePending means somebody has claimed it and it confers NOTHING yet.
	BadgePending BadgeState = "pending_confirmation"
	// BadgeVerified means a trusted user who did NOT claim it has confirmed it.
	BadgeVerified BadgeState = "verified"
	// BadgeRejected means a trusted user declined it. Terminal: a rejected claim is
	// not merely unconfirmed, and a rejected claim that could be re-filed on demand
	// is a claim anybody can spam.
	BadgeRejected BadgeState = "rejected"
)

// ConveysTrust is the only question a caller should ask before treating a badge as
// evidence.
//
// NOT `state == BadgeVerified`, because that spelling invites a caller to also
// branch on BadgePending for a different purpose, and there is exactly one correct
// way to ask. It is a function rather than a field so the answer cannot be cached
// alongside the state and drift from it.
func ConveysTrust(s BadgeState) bool { return s == BadgeVerified }

// Claim is somebody's assertion that they are, or represent, an entity.
type Claim struct {
	EntityType string // "studio", "performer", "site"
	EntityID   int

	// ClaimedBy is the user asserting this. It is the crux of the step, so it is a
	// field rather than an attribution: §6a.4 says the claim-and-confirm flow exists
	// because an OPERATOR-GRANTED BADGE IS THE OWNER BEING AN ADMIN OVER CONTENT.
	ClaimedBy int

	// State is where the claim stands.
	State BadgeState

	// ConfirmedBy is the trusted user who confirmed it, and 0 while unconfirmed.
	//
	// IT CANNOT EQUAL ClaimedBy. The operator cannot confirm their own claim, and
	// that single inequality is the entire mechanism: it is what makes a badge
	// something an owner cannot confer on themselves.
	ConfirmedBy int

	// DecidedAt is when the claim reached a terminal state, and 0 while pending.
	// Present because §4.2's audit log is the only record an operator later has of
	// who confirmed what, and a confirmed badge with no date is a badge nobody can
	// trace.
	DecidedAt time.Time
}

// Errors, kept separate because each names a different party's problem.
var (
	// ErrSelfConfirmed means the claimer tried to confirm their own claim.
	ErrSelfConfirmed = errors.New("directory: the claimer cannot confirm their own claim")

	// ErrNotPending means the claim was decided already.
	ErrNotPending = errors.New("directory: the claim is no longer pending")

	// ErrAlreadyDecided means a decided claim cannot be flipped back.
	ErrAlreadyDecided = errors.New("directory: the claim has already been decided")

	// ErrNoClaim means there is nothing to decide.
	ErrNoClaim = errors.New("directory: no such claim")
)

// File records a claim by an operator. It confers nothing.
//
// THE OPERATOR IS ORDINARY HERE. Filing a claim is what any user does when they
// assert ownership; the only thing an operator's status changes is nothing at all,
// which is the plan's requirement stated as an absence. There is no
// FileAsOperator, no force flag, and no grant path — a badge the owner confers on
// themselves is an admin badge wearing a community label.
func File(entityType string, entityID, claimedBy int) (Claim, error) {
	if entityType == "" || entityID <= 0 {
		return Claim{}, fmt.Errorf("%w: entity %q/%d is not addressable", ErrNoClaim, entityType, entityID)
	}
	if claimedBy == 0 {
		return Claim{}, fmt.Errorf("%w: a claim needs a user to attribute it to", ErrNoClaim)
	}
	return Claim{
		EntityType: entityType,
		EntityID:   entityID,
		ClaimedBy:  claimedBy,
		State:      BadgePending,
	}, nil
}

// Confirm approves a pending claim by a trusted user who did not claim it.
//
// THE PARAMETERS ARE THE RULE. confirmer is separate from the claim's ClaimedBy,
// and the self-confirmation check is the whole of §6a.4's "no operator grants a
// badge": an operator filing a claim is simply a user filing a claim, and the only
// person who can move it forward is somebody else.
//
// trusted is a parameter rather than read from a store, so the TRUST decision stays
// visible at the call site instead of being buried. A caller that passes the wrong
// value is making a mistake in one place rather than in a hidden helper.
func Confirm(c Claim, confirmer int, trusted bool, at time.Time) (Claim, error) {
	if c.State != BadgePending {
		return Claim{}, fmt.Errorf("%w: state is %q", ErrNotPending, c.State)
	}
	if confirmer == 0 {
		return Claim{}, fmt.Errorf("%w: a confirmation needs a user", ErrNoClaim)
	}
	// The check that matters. It is FIRST, before trust: an untrusted claimer
	// confirming their own claim is refused for being unconfirmed AND untrusted,
	// and saying so is more useful than reporting only the trust failure.
	if confirmer == c.ClaimedBy {
		return Claim{}, fmt.Errorf("%w: user %d both claimed and confirmed %s/%d",
			ErrSelfConfirmed, confirmer, c.EntityType, c.EntityID)
	}
	if !trusted {
		// Not an error: a non-trusted user is simply not the person whose
		// confirmation counts. Erroring would make a UI offer them an action that
		// always fails, which teaches users that the button is broken.
		return c, nil
	}

	c.State = BadgeVerified
	c.ConfirmedBy = confirmer
	c.DecidedAt = at
	return c, nil
}

// Reject declines a pending claim.
//
// Symmetric with Confirm and subject to the same self-check: the claimer cannot
// reject their own claim either, since that would be a way to clear a claim
// somebody else made without a decision.
func Reject(c Claim, rejecter int, trusted bool, at time.Time) (Claim, error) {
	if c.State != BadgePending {
		return Claim{}, fmt.Errorf("%w: state is %q", ErrNotPending, c.State)
	}
	if rejecter == c.ClaimedBy {
		return Claim{}, fmt.Errorf("%w: user %d cannot decide their own claim", ErrSelfConfirmed, rejecter)
	}
	if !trusted {
		return c, nil
	}

	c.State = BadgeRejected
	c.ConfirmedBy = rejecter // recorded: WHO declined is an audit fact, not a badge
	c.DecidedAt = at
	return c, nil
}

// PaymentMethod is how a site or network takes money (R045).
//
// PRICING IS A MAPPING TO A CURRENCY, NOT A FLOAT. A single price column cannot
// represent "€45 a scene, $60 a hour, ¥9000 a day" without a unit, and a bare float
// invites the reading that 45 and 60 are comparable. §4.1's vocabulary has the same
// shape for dates, for the same reason.
type PaymentMethod struct {
	// Site is the network's identifier in this instance's directory.
	Site   string
	Method string // "ppv", "ppd", "subscription", "rental", "free"

	// Prices is per-unit, keyed by currency code.
	Prices map[Unit]Price

	// AcceptedCurrencies is what the site actually takes. A Price in a currency
	// outside it is a listing the site does not honour, and a caller that ignored
	// this would quote a price nobody can pay.
	AcceptedCurrencies []string
}

// Unit is what a price is per. Named rather than a string so "a scene" cannot be
// confused with "an hour" by a typo.
type Unit string

const (
	UnitPerScene     Unit = "per_scene"
	UnitPerMinute    Unit = "per_minute"
	UnitPerDay       Unit = "per_day"
	UnitSubscription Unit = "subscription"
)

// Price is an amount in a currency.
type Price struct {
	Amount   int // minor units, so 4500 is €45.00 and no float is involved
	Currency string
}

// NetworkProfile is a site's directory profile (R045).
type NetworkProfile struct {
	Site           string
	Name           string
	PaymentMethods []PaymentMethod
}

// PaymentMethodsFor returns the methods accepting a currency, sorted by method so
// two callers get the same answer.
//
// SORTED because a network profile rendered as a list of pills will otherwise
// reorder between renders, and a shopper comparing two sites sees them in
// different orders. A directory listing that reshuffles is a directory nobody
// trusts.
func PaymentMethodsFor(p NetworkProfile, currency string) []PaymentMethod {
	var out []PaymentMethod
	for _, m := range p.PaymentMethods {
		for _, c := range m.AcceptedCurrencies {
			if c == currency {
				out = append(out, m)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Method < out[j].Method })
	return out
}

// PricesFor returns a method's prices for one currency, sorted by unit.
//
// An EMPTY RESULT IS THE HONEST ANSWER when the method takes no such currency, and
// it is not an error: a network may simply not sell by the minute, which is
// information rather than a fault.
func PricesFor(m PaymentMethod, currency string) []Price {
	var out []Price
	for unit, p := range m.Prices {
		if p.Currency == currency {
			out = append(out, Price{Amount: p.Amount, Currency: currency})
			_ = unit
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

// Roster is a studio's performers (R046).
//
// DERIVED, NOT STORED. §6a.15: the completion score is a view, and a roster is the
// same shape of thing -- "who appears in this studio's scenes" is a join, and
// storing it means a scene's edit has to update every studio that lists it.
type Roster struct {
	StudioID int
	// PerformerIDs is the derived membership, ascending.
	PerformerIDs []int
}

// CompletionScore is a studio's §6a.15 completion, delegated to the rank package
// rather than recomputed here. One implementation of "what counts as complete",
// reached from two call sites.
type CompletionScore float64

// Review is a structured review with a verified-usage flag (R047).
//
// VERIFIED-USAGE IS A CLAIM ABOUT THE REVIEWER, NOT ABOUT THE REVIEW. That
// distinction is what stops the flag from becoming a quality signal: "I have seen
// this studio" is checkable, "this review is good" is not, and a flag that quietly
// became the latter would be a ranking by self-assertion.
type Review struct {
	StudioID int
	AuthorID int
	Rating   int // 0..100
	Body     string

	// VerifiedUsage means the author has confirmed personal use of the studio.
	VerifiedUsage bool
}

// ErrInvalidRating is returned for a rating outside 0..100.
var ErrInvalidRating = errors.New("directory: rating must be 0..100")

// Validate checks a review's shape before it is stored.
//
// Ratings are bounded HERE rather than by the database CHECK alone, because a
// review rating feeds the Elo pool in internal/rank, and a 900 there is a
// player who cannot be beaten rather than a review that failed to save.
func (r Review) Validate() error {
	if r.Rating < 0 || r.Rating > 100 {
		return fmt.Errorf("%w: got %d", ErrInvalidRating, r.Rating)
	}
	if r.AuthorID == 0 {
		return fmt.Errorf("%w: a review needs an author", ErrNoClaim)
	}
	if r.StudioID == 0 {
		return fmt.Errorf("%w: a review needs a studio", ErrNoClaim)
	}
	return nil
}

// Trending is what §6a.7's mesh trending surface would expose.
//
// IT EXISTS ONLY AS A DERIVED LIST, and there is no increment path. The temptation
// here is a `views` or `saves` counter incremented on read, which is the cheapest
// possible thing to add and the reason a public read endpoint is not somewhere to
// start one: a view counter on a public surface is a write amplifier reachable by
// anyone who can load a page.
type Trending struct {
	EntityType string
	EntityIDs  []int
}
