package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/stashapp/stash/internal/directory"
)

// The directory store. M7 step 7.7a, migration 118.
//
// Compile-time proof the adapter satisfies the domain interfaces, in a non-test file
// so every BUILD checks it. Two assertions because there are two interfaces, and
// they are separate on purpose: a type holding a PricingStore must not thereby hold
// the ability to confirm a badge claim.
var _ directory.ClaimStore = (*DirectoryStore)(nil)
var _ directory.PricingStore = (*DirectoryStore)(nil)

// DirectoryStore persists §6a.4's claim-and-confirm and R045's network pricing.
//
// THE ABSENCE IS THE GUARANTEE, and both halves of it live here rather than in a
// policy document:
//
//   - there is no Grant method, so an operator-granted badge is not a call site
//     somebody forgot not to write;
//   - every write re-checks the claimer-neq-confirmer rule even though the domain's
//     Confirm already enforces it, because the domain's check holds only while every
//     writer goes through the domain, and the owner of a self-hosted instance has a
//     sqlite file and a query runner. The schema's CHECK is the third layer and the
//     only one that holds against that.
//
// Duplicating the check in Go is not redundancy-as-paranoia. The three layers fail
// differently: the Go check gives a caller a typed error, the store check catches a
// future method that forgets, and the CHECK catches everything else.
type DirectoryStore struct {
	repository
}

const (
	directoryClaimsTable  = "directory_claims"
	directoryPricingTable = "directory_pricing"

	claimsEntityTypeCol  = "entity_type"
	claimsEntityIDCol    = "entity_id"
	claimsClaimedByCol   = "claimed_by"
	claimsStateCol       = "state"
	claimsConfirmedByCol = "confirmed_by"
	claimsDecidedAtCol   = "decided_at"
	claimsRejectedAtCol  = "rejected_at"

	pricingUnitCol        = "unit"
	pricingCurrencyCol    = "currency"
	pricingAmountMinorCol = "amount_minor"
)

func NewDirectoryStore() *DirectoryStore {
	return &DirectoryStore{repository{tableName: directoryClaimsTable, idColumn: claimsEntityIDCol}}
}

// FileClaim records a new pending claim.
//
// THE STATE IS NOT TAKEN FROM THE CALLER. directory.File builds a Claim already at
// BadgePending, and this store writes that state rather than c.Claim.State, so a
// caller cannot file a claim that is born verified. A store that honoured the
// caller's state would be a grant path with one extra field, and R048's guarantee is
// that no such path exists.
//
// An UPSERT keyed on the entity, because ONE CLAIM PER ENTITY is the model: two
// users claiming the same studio is not a richer state, it is two rows disagreeing
// about who owns a badge. A re-file therefore replaces, and the previous claimer is
// recorded in the audit log rather than here.
func (s *DirectoryStore) FileClaim(ctx context.Context, c directory.Claim) error {
	if c.EntityType == "" || c.EntityID <= 0 {
		return fmt.Errorf("filing a claim for entity %q/%d: neither is addressable", c.EntityType, c.EntityID)
	}
	if c.ClaimedBy <= 0 {
		return fmt.Errorf("filing a claim for entity %q/%d: it needs a user to attribute it to",
			c.EntityType, c.EntityID)
	}

	_, err := dbWrapper.Exec(ctx, "INSERT INTO "+directoryClaimsTable+
		" ("+claimsEntityTypeCol+", "+claimsEntityIDCol+", "+claimsClaimedByCol+", "+claimsStateCol+
		", "+claimsConfirmedByCol+", "+claimsDecidedAtCol+", "+claimsRejectedAtCol+
		") VALUES (?, ?, ?, ?, NULL, NULL, NULL)"+
		" ON CONFLICT("+claimsEntityTypeCol+", "+claimsEntityIDCol+") DO UPDATE SET"+
		" "+claimsClaimedByCol+" = excluded."+claimsClaimedByCol+", "+
		claimsStateCol+" = 'pending_confirmation', "+
		claimsConfirmedByCol+" = NULL, "+
		claimsDecidedAtCol+" = NULL, "+
		claimsRejectedAtCol+" = NULL",
		c.EntityType, c.EntityID, c.ClaimedBy, string(directory.BadgePending))
	if err != nil {
		return fmt.Errorf("filing a claim for %s %d: %w", c.EntityType, c.EntityID, err)
	}
	return nil
}

// ConfirmClaim records a trusted user's confirmation.
//
// The claimer check is repeated HERE even though directory.Confirm enforces it, and
// the error is the domain's own so a caller using errors.Is cannot tell the two
// layers apart -- which is the point: a caller should not have to know how many
// checks a write passed.
func (s *DirectoryStore) ConfirmClaim(ctx context.Context, c directory.Claim, confirmer int, at time.Time) error {
	if confirmer <= 0 {
		return fmt.Errorf("confirming %s %d: a confirmation needs a user to attribute it to",
			c.EntityType, c.EntityID)
	}
	if confirmer == c.ClaimedBy {
		return fmt.Errorf("%w: user %d filed and confirmed %s %d",
			directory.ErrSelfConfirmed, confirmer, c.EntityType, c.EntityID)
	}
	return s.decide(ctx, c, directory.BadgeVerified, confirmer, at)
}

// RejectClaim records a rejection.
//
// There is no Unreject and no Reopen, and the absence is the design: a rejected claim
// that could be re-filed on demand is a claim anybody can spam. Re-filing requires
// the operator to clear the row deliberately, which is an action rather than a
// method.
func (s *DirectoryStore) RejectClaim(ctx context.Context, c directory.Claim, rejecter int, at time.Time) error {
	if rejecter <= 0 {
		return fmt.Errorf("rejecting %s %d: a decision needs a user to attribute it to",
			c.EntityType, c.EntityID)
	}
	if rejecter == c.ClaimedBy {
		return fmt.Errorf("%w: user %d filed and rejected their own claim %s %d",
			directory.ErrSelfConfirmed, rejecter, c.EntityType, c.EntityID)
	}

	// The same pending guard decide() applies, and the same RowsAffected
	// distinction. Reject is not a rarer path and gets no weaker a check: a
	// rejected claim that could be re-rejected by anyone with a user id is not the
	// terminal rejection internal/directory describes.
	res, err := dbWrapper.Exec(ctx, "UPDATE "+directoryClaimsTable+
		" SET "+claimsStateCol+" = ?, "+claimsConfirmedByCol+" = ?, "+
		claimsDecidedAtCol+" = ?, "+claimsRejectedAtCol+" = ?"+
		" WHERE "+claimsEntityTypeCol+" = ? AND "+claimsEntityIDCol+" = ?"+
		" AND "+claimsStateCol+" = ?",
		string(directory.BadgeRejected), rejecter, at, at, c.EntityType, c.EntityID,
		string(directory.BadgePending))
	if err != nil {
		return fmt.Errorf("rejecting the claim for %s %d: %w", c.EntityType, c.EntityID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rejecting the claim for %s %d: cannot read the row count: %w",
			c.EntityType, c.EntityID, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s %d is not pending, and a decision is made once",
			directory.ErrNotPending, c.EntityType, c.EntityID)
	}
	return nil
}

// decide is the shared terminal-state write for a confirmation.
//
// AND IT GUARDS ON `state = pending_confirmation`, which the first version did not --
// it updated whatever row it found. That made a VERIFIED claim re-confirmable and a
// REJECTED one re-rejectable by anyone holding a user id, and neither the domain nor
// the schema stops it: the domain refuses to BUILD a Confirm for a non-pending claim,
// but the store is a separate entry point and its job is to refuse the write itself.
// A domain check that can only be bypassed by not calling the domain is a
// documentation, not a guard.
//
// RowsAffected distinguishes "refused because it was already decided" from "no such
// claim", which are different answers: the first is a rule, the second is a typo.
func (s *DirectoryStore) decide(ctx context.Context, c directory.Claim, state directory.BadgeState, confirmer int, at time.Time) error {
	res, err := dbWrapper.Exec(ctx, "UPDATE "+directoryClaimsTable+
		" SET "+claimsStateCol+" = ?, "+claimsConfirmedByCol+" = ?, "+claimsDecidedAtCol+" = ?"+
		" WHERE "+claimsEntityTypeCol+" = ? AND "+claimsEntityIDCol+" = ?"+
		" AND "+claimsStateCol+" = ?",
		string(state), confirmer, at, c.EntityType, c.EntityID, string(directory.BadgePending))
	if err != nil {
		return fmt.Errorf("recording %s for %s %d: %w", state, c.EntityType, c.EntityID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("recording %s for %s %d: cannot read the row count: %w",
			state, c.EntityType, c.EntityID, err)
	}
	if n == 0 {
		// Either no claim, or one already decided. Say which, because "your write did
		// nothing" is the least useful error a caller can receive.
		existing, gerr := s.BadgeFor(ctx, c.EntityType, c.EntityID)
		if gerr != nil {
			return fmt.Errorf("%w: %s %d is not pending, and its state could not be read: %v",
				directory.ErrNotPending, c.EntityType, c.EntityID, gerr)
		}
		return fmt.Errorf("%w: %s %d is already %s, and a decision is made once",
			directory.ErrNotPending, c.EntityType, c.EntityID, existing)
	}
	return nil
}

// ClaimFor reads one entity's claim.
//
// A MISSING ROW IS ErrNoClaim, not a zero Claim. "Nobody has claimed this" and "a
// claim exists with no fields set" are different answers, and a profile page renders
// them differently -- one shows an unclaimed badge and offers a claim button, the
// other would show a broken badge.
func (s *DirectoryStore) ClaimFor(ctx context.Context, entityType string, entityID int) (directory.Claim, error) {
	var (
		claimedBy   int
		state       string
		confirmedBy sql.NullInt64
		decidedAt   sql.NullTime
	)
	err := dbWrapper.Get(ctx, &claimedBy, "SELECT "+claimsClaimedByCol+" FROM "+directoryClaimsTable+
		" WHERE "+claimsEntityTypeCol+" = ? AND "+claimsEntityIDCol+" = ?", entityType, entityID)
	if errors.Is(err, sql.ErrNoRows) {
		return directory.Claim{}, fmt.Errorf("%w: %s %d", directory.ErrNoClaim, entityType, entityID)
	}
	if err != nil {
		return directory.Claim{}, fmt.Errorf("reading the claim for %s %d: %w", entityType, entityID, err)
	}

	// The rest of the row is read separately rather than in one Scan, because a
	// NULL into an `int` fails to convert and a NULL is the correct value for an
	// undecided claim. Three queries for one row is not fast, and this is a profile
	// page read rather than a hot path; a single scan with sql.Null* would be
	// tighter and would make the "which columns are nullable" knowledge implicit in
	// the scan order.
	if err := dbWrapper.Get(ctx, &state, "SELECT "+claimsStateCol+" FROM "+directoryClaimsTable+
		" WHERE "+claimsEntityTypeCol+" = ? AND "+claimsEntityIDCol+" = ?", entityType, entityID); err != nil {
		return directory.Claim{}, fmt.Errorf("reading the claim state for %s %d: %w", entityType, entityID, err)
	}
	if err := dbWrapper.Get(ctx, &confirmedBy, "SELECT "+claimsConfirmedByCol+" FROM "+directoryClaimsTable+
		" WHERE "+claimsEntityTypeCol+" = ? AND "+claimsEntityIDCol+" = ?", entityType, entityID); err != nil {
		return directory.Claim{}, fmt.Errorf("reading the claim confirmer for %s %d: %w", entityType, entityID, err)
	}
	if err := dbWrapper.Get(ctx, &decidedAt, "SELECT "+claimsDecidedAtCol+" FROM "+directoryClaimsTable+
		" WHERE "+claimsEntityTypeCol+" = ? AND "+claimsEntityIDCol+" = ?", entityType, entityID); err != nil {
		return directory.Claim{}, fmt.Errorf("reading the claim decision time for %s %d: %w", entityType, entityID, err)
	}

	return directory.Claim{
		EntityType:  entityType,
		EntityID:    entityID,
		ClaimedBy:   claimedBy,
		State:       directory.BadgeState(state),
		ConfirmedBy: int(confirmedBy.Int64),
		DecidedAt:   decidedAt.Time,
	}, nil
}

// BadgeFor is the one read a profile page makes, and it answers the question
// ConveysTrust exists for: may this badge be treated as evidence?
//
// A MISSING CLAIM IS BadgeUnclaimed and NOT ErrNoClaim, unlike ClaimFor. That
// asymmetry is deliberate and is the difference between "there is no claim" as an
// answer to "what is the badge" and as an error. A renderer asking the badge
// question wants a badge for every entity, and making it handle an error for the
// common case is how a renderer ends up treating an error as unclaimed.
func (s *DirectoryStore) BadgeFor(ctx context.Context, entityType string, entityID int) (directory.BadgeState, error) {
	c, err := s.ClaimFor(ctx, entityType, entityID)
	if errors.Is(err, directory.ErrNoClaim) {
		return directory.BadgeUnclaimed, nil
	}
	if err != nil {
		return directory.BadgeUnclaimed, err
	}
	return c.State, nil
}

// PendingClaims lists claims awaiting a decision, oldest first.
//
// SORTED BY CLAIMED ORDER, not by id, and the ordering is part of the contract: a
// review queue that reshuffles between renders is a queue nobody trusts to be fair.
// `at` is used because the audit timestamp and the id can disagree -- a backfilled
// row has a late id -- and "who has been waiting longest" is the question the order
// is answering.
func (s *DirectoryStore) PendingClaims(ctx context.Context) ([]directory.Claim, error) {
	rows, err := dbWrapper.Queryx(ctx, "SELECT "+claimsEntityTypeCol+", "+claimsEntityIDCol+
		", "+claimsClaimedByCol+" FROM "+directoryClaimsTable+
		" WHERE "+claimsStateCol+" = ? ORDER BY rowid ASC",
		string(directory.BadgePending))
	if err != nil {
		return nil, fmt.Errorf("listing pending claims: %w", err)
	}
	defer rows.Close()

	var out []directory.Claim
	for rows.Next() {
		var c directory.Claim
		if err := rows.Scan(&c.EntityType, &c.EntityID, &c.ClaimedBy); err != nil {
			return nil, fmt.Errorf("scanning a pending claim: %w", err)
		}
		c.State = directory.BadgePending
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("walking the pending claims: %w", err)
	}
	return out, nil
}

// SetPrice records or replaces one price, in MINOR UNITS.
//
// Minor units because internal/directory's Price.Amount is an int documented as
// "4500 is €45.00 and no float is involved", and storing a float here would put one
// back into a path the domain deliberately removed.
//
// A NEGATIVE AMOUNT IS REFUSED, not clamped. Clamping would silently turn a bug into
// a free item; a negative price is not a discount, it is a store that will hand out
// money, and it should be loud.
func (s *DirectoryStore) SetPrice(ctx context.Context, entityType string, entityID int, unit directory.Unit, currency string, amountMinor int) error {
	if entityType == "" || entityID <= 0 {
		return fmt.Errorf("quoting %s %d: neither is addressable", entityType, entityID)
	}
	if unit == "" {
		return fmt.Errorf("quoting %s %d: a price needs a unit", entityType, entityID)
	}
	if currency == "" {
		return fmt.Errorf("quoting %s %d in %q: a price needs a currency", entityType, entityID, unit)
	}
	if amountMinor < 0 {
		return fmt.Errorf("quoting %s %d at %d minor units of %s: a negative price is not a discount, "+
			"it is a store that will hand out money", entityType, entityID, amountMinor, currency)
	}

	_, err := dbWrapper.Exec(ctx, "INSERT INTO "+directoryPricingTable+
		" ("+claimsEntityTypeCol+", "+claimsEntityIDCol+", "+pricingUnitCol+", "+pricingCurrencyCol+
		", "+pricingAmountMinorCol+") VALUES (?, ?, ?, ?, ?)"+
		" ON CONFLICT("+claimsEntityTypeCol+", "+claimsEntityIDCol+", "+pricingUnitCol+", "+pricingCurrencyCol+
		") DO UPDATE SET "+pricingAmountMinorCol+" = excluded."+pricingAmountMinorCol,
		entityType, entityID, string(unit), currency, amountMinor)
	if err != nil {
		return fmt.Errorf("quoting %s %d per %s in %s: %w", entityType, entityID, unit, currency, err)
	}
	return nil
}

// PriceFor reads one price.
//
// A missing row is ErrNoPrice and NOT a zero Price, because "this site does not
// quote that" and "this site quotes it as free" are different facts. A zero price
// for a missing row would render as a free scene, which is the more expensive of the
// two mistakes.
func (s *DirectoryStore) PriceFor(ctx context.Context, entityType string, entityID int, unit directory.Unit, currency string) (directory.Price, error) {
	var amount int
	err := dbWrapper.Get(ctx, &amount, "SELECT "+pricingAmountMinorCol+" FROM "+directoryPricingTable+
		" WHERE "+claimsEntityTypeCol+" = ? AND "+claimsEntityIDCol+" = ?"+
		" AND "+pricingUnitCol+" = ? AND "+pricingCurrencyCol+" = ?",
		entityType, entityID, string(unit), currency)
	if errors.Is(err, sql.ErrNoRows) {
		return directory.Price{}, fmt.Errorf("%w: %s %d per %s in %s",
			directory.ErrNoPrice, entityType, entityID, unit, currency)
	}
	if err != nil {
		return directory.Price{}, fmt.Errorf("reading the price for %s %d per %s in %s: %w",
			entityType, entityID, unit, currency, err)
	}
	return directory.Price{Amount: amount, Currency: currency}, nil
}

// NetworkProfileFor assembles a profile from the stored prices, ready for
// PaymentMethodsFor and PricesFor to answer against.
//
// The METHODS are the distinct currencies' worth of a single synthetic method per
// unit, which is a simplification and is named rather than hidden: internal/directory
// models a PaymentMethod as a method plus a price map, and the schema stores prices
// without methods. So the profile this returns quotes prices without naming which
// payment method takes them, and a real method table is the obvious next migration.
// Returning an empty method list with the prices attached would be worse: the
// caller would render a site that quotes prices and accepts nothing.
func (s *DirectoryStore) NetworkProfileFor(ctx context.Context, entityType string, entityID int, name string) (directory.NetworkProfile, error) {
	rows, err := dbWrapper.Queryx(ctx, "SELECT "+pricingUnitCol+", "+pricingCurrencyCol+
		", "+pricingAmountMinorCol+" FROM "+directoryPricingTable+
		" WHERE "+claimsEntityTypeCol+" = ? AND "+claimsEntityIDCol+" = ?",
		entityType, entityID)
	if err != nil {
		return directory.NetworkProfile{}, fmt.Errorf("reading the profile for %s %d: %w", entityType, entityID, err)
	}
	defer rows.Close()

	var (
		methods []directory.PaymentMethod
		byCur   = map[string]map[directory.Unit]directory.Price{}
		curList []string
	)
	for rows.Next() {
		var unit, currency string
		var amount int
		if err := rows.Scan(&unit, &currency, &amount); err != nil {
			return directory.NetworkProfile{}, fmt.Errorf("scanning a price: %w", err)
		}
		if _, seen := byCur[currency]; !seen {
			byCur[currency] = map[directory.Unit]directory.Price{}
			curList = append(curList, currency)
		}
		byCur[currency][directory.Unit(unit)] = directory.Price{Amount: amount, Currency: currency}
	}
	if err := rows.Err(); err != nil {
		return directory.NetworkProfile{}, fmt.Errorf("walking the prices: %w", err)
	}

	for _, cur := range curList {
		methods = append(methods, directory.PaymentMethod{
			Site:               name,
			Method:             "quoted",
			Prices:             byCur[cur],
			AcceptedCurrencies: []string{cur},
		})
	}

	return directory.NetworkProfile{Site: name, Name: name, PaymentMethods: methods}, nil
}
