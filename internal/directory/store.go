// The persistence the directory's claims and prices need. M7 step 7.7a.
//
// WHY THIS FILE EXISTS, since directory.go is complete and its tests pass.
//
// For the same reason internal/collab/access_policy.go does. A domain package with
// no importer is not half-built work, it is INVISIBLE work: 319 lines and thirteen
// exported symbols, every test green, and nothing in the tree able to reach any of
// it. `grep -rn 'stashapp/stash/internal/directory' --include=*.go .` returns exactly
// one file — its own.
//
// This is WORSE than the analogous access-level gap, and the reason is worth
// recording: the project's existing guard for unreachable stores,
// TestStashForgeStoreConstructorsAreActuallyWired, greps store CONSTRUCTORS. This
// package has no constructor, so the guard had nothing to look at. A guard that
// searches for one shape of defect is blind to every other shape, and the shapes
// here are "a domain package nothing imports".
//
// SO THE STORE IS DECLARED HERE, in the domain, rather than invented by the
// adapter. The requirements are the domain's — "a claim is attributed", "the
// claimer cannot confirm", "a rejection is terminal" — and an adapter that defined
// its own interface would make those requirements a suggestion.
//
// NOTE WHAT THE INTERFACE DOES NOT HAVE, because R048's guarantee is an ABSENCE:
//
//   - no Grant(ctx, entity, grantedBy)
//   - no Force parameter on anything
//   - no method that sets a badge without a claim and a confirmer
//
// An operator-granted badge is the owner being an admin over content, which is the
// thing §6a.4 exists to prevent. An interface method would make the grant path a
// compile-time option, so there is no such method and the absence is the guarantee.
// That is not the same as "nobody has written it yet" — a method that does not exist
// cannot be called by a future contributor who does not know to avoid it, and
// TestTheSchemaHasNoGrantPath holds the schema to the same rule.

package directory

import (
	"context"
	"errors"
	"time"
)

// ClaimStore persists claims and reads them back.
//
// NO OPERATOR FLAG AND NO TRUST FLAG ON THE WRITES, deliberately. The domain's
// Confirm and Reject already take `trusted bool` and enforce the claimer-neq-confirmer
// rule, and passing trust through here as well would give a store implementation two
// places to enforce one rule. The store's job is to persist the decision the domain
// made, and to refuse to write a row the schema forbids.
type ClaimStore interface {
	// FileClaim records a new pending claim. It must NOT accept a caller-supplied
	// state: a claim starts pending because a claim is an assertion awaiting
	// somebody else's judgement, and a store that let the caller choose would be a
	// grant path with extra steps.
	FileClaim(ctx context.Context, c Claim) error

	// ConfirmClaim records confirmer's confirmation of c, which must be PENDING.
	//
	// THE CONFIRMER IS A PARAMETER, NOT A FIELD READ OUT OF THE CLAIM, and matching
	// the domain's own `Confirm(c, confirmer, trusted, at)` is the reason. The first
	// version of this interface took only the claim and read ConfirmedBy from it,
	// which obliged a caller to mutate the claim before calling and meant an
	// unmutated claim was confirmed with a confirmer of 0 -- a write nobody asked
	// for, accepted, because there was no way to name the person who was doing it.
	// A store method whose subject is a person should take that person.
	//
	// The store refuses any row where confirmer equals claimed_by, because that is
	// §6a.4's one mechanism and it must not depend on the domain having been called.
	ConfirmClaim(ctx context.Context, c Claim, confirmer int, at time.Time) error

	// RejectClaim records a rejection, which is TERMINAL. There is no Unreject and
	// no Reopen, and the absence is the point: a rejected claim that could be
	// re-filed on demand is a claim anybody can spam, so re-filing requires the
	// original claim to be gone, which is a deliberate operator action rather than
	// a method.
	RejectClaim(ctx context.Context, c Claim, rejecter int, at time.Time) error

	// ClaimFor reads one entity's claim. A missing row is ErrNoClaim rather than a
	// zero Claim, because "nobody has claimed this" and "a claim exists with no
	// fields set" are different answers and the caller renders them differently.
	ClaimFor(ctx context.Context, entityType string, entityID int) (Claim, error)

	// PendingClaims lists claims awaiting a decision, oldest first. This is the
	// review queue, and the ordering is part of the contract: a queue that
	// reshuffles between renders is a queue nobody trusts to be fair.
	PendingClaims(ctx context.Context) ([]Claim, error)
}

// PricingStore persists a network's quoted prices.
//
// SEPARATE FROM ClaimStore because a price is not a claim. A claim is about truth
// and carries §6a.4's governance; a price is money. Folding them into one interface
// would put "may this user confirm this badge" and "what does this site charge" on
// the same type, and a caller holding a PricingStore would then hold the ability to
// confirm claims.
type PricingStore interface {
	// SetPrice records or replaces one price. The key is
	// (entity, unit, currency), so a second quote for the same key REPLACES rather
	// than appends — which is what makes a price change an event instead of a row
	// count.
	//
	// It must refuse a negative amount: a negative price is not a discount, it is a
	// store that will hand out money.
	SetPrice(ctx context.Context, entityType string, entityID int, unit Unit, currency string, amountMinor int) error

	// PriceFor reads one price. A missing row is ErrNoPrice, distinct from a price
	// of zero: "this site does not quote that" and "this site quotes it as free"
	// are different facts and a shopper needs to tell them apart.
	PriceFor(ctx context.Context, entityType string, entityID int, unit Unit, currency string) (Price, error)

	// NetworkProfileFor assembles a site's profile from its stored prices, ready for
	// PaymentMethodsFor and PricesFor to answer against.
	NetworkProfileFor(ctx context.Context, entityType string, entityID int, name string) (NetworkProfile, error)
}

// ErrNoPrice is a missing price, kept distinct from a price of zero. Declared here
// because it is the one error in this file's two interfaces that the domain's own
// free functions never return -- they take an already-assembled profile -- so
// without it a caller would have to string-match.
var ErrNoPrice = errors.New("directory: no such price")
