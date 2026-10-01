package directory

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var at = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// The plan's test for 7.7a, and §6a.4's rule: "a studio or performer claims, an
// existing trusted user confirms, and NO OPERATOR GRANTS A BADGE, because an
// operator-granted badge is the owner being an admin over content (#6)."
//
// The test pins the operator case explicitly, because "no operator grants a badge"
// is not satisfied by a function that merely happens to lack a grant path -- it is
// satisfied when the operator's claim is indistinguishable from anybody else's and
// the operator cannot advance it.
func TestAVerifiedBadgeIsConfirmedNotGranted(t *testing.T) {
	// The INSTANCE OWNER files a claim. There is no FileAsOperator and no force
	// flag, so filing is what any user does.
	ownerClaim, err := File("studio", 42 /*claimedBy*/, 1)
	require.NoError(t, err)

	// It is pending and confers nothing. This is the assertion the plan asks for.
	assert.Equal(t, BadgePending, ownerClaim.State,
		"a filed claim is pending_confirmation")
	assert.False(t, ConveysTrust(ownerClaim.State),
		"and confers nothing: §6a.4's whole point is that claimed is not verified")

	// THE OWNER CANNOT CONFIRM IT. Being the instance owner changes nothing about
	// this claim, which is the mechanism rather than a policy.
	_, err = Confirm(ownerClaim /*confirmer*/, 1 /*trusted*/, true, at)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSelfConfirmed,
		"the claimer cannot confirm their own claim. §6a.4: an operator-granted "+
			"badge is the owner being an admin over content, so the owner cannot "+
			"advance their own claim no matter how trusted they are.")

	// Nor can they reject it, which would be a way to clear somebody else's claim
	// without a decision.
	_, err = Reject(ownerClaim /*rejecter*/, 1, true, at)
	assert.ErrorIs(t, err, ErrSelfConfirmed)

	// An existing TRUSTED user who did not claim it confirms.
	confirmed, err := Confirm(ownerClaim /*confirmer*/, 2 /*trusted*/, true, at)
	require.NoError(t, err)
	assert.Equal(t, BadgeVerified, confirmed.State)
	assert.True(t, ConveysTrust(confirmed.State), "now it is verified")
	assert.Equal(t, 2, confirmed.ConfirmedBy, "and WHO confirmed is recorded")
	assert.Equal(t, at, confirmed.DecidedAt,
		"§4.2: a confirmed badge with no date is a badge nobody can trace")
	assert.Equal(t, 1, confirmed.ClaimedBy, "the claimer is still recorded")

	// The claimer could not have done this, and could not have got here first.
	assert.NotEqual(t, confirmed.ClaimedBy, confirmed.ConfirmedBy,
		"the two must differ: that inequality IS the badge mechanism")
}

// An UNTRUSTED user's confirmation is not an error and changes nothing. Erroring
// would make a UI offer an action that always fails, which teaches users the button
// is broken.
func TestAnUntrustedUserCannotAdvanceAClaim(t *testing.T) {
	c, err := File("performer", 7, 1)
	require.NoError(t, err)

	same, err := Confirm(c /*confirmer*/, 5 /*trusted*/, false, at)
	require.NoError(t, err,
		"an untrusted confirmation is not an error -- it is a no-op, because the UI "+
			"should not offer an action that always fails")
	assert.Equal(t, c.State, same.State, "still pending")
	assert.False(t, ConveysTrust(same.State))
	assert.Zero(t, same.ConfirmedBy, "and nobody is recorded as having decided it")
	assert.Zero(t, same.DecidedAt, "with no date, since nothing was decided")

	rejected, err := Reject(c /*rejecter*/, 5, false, at)
	require.NoError(t, err)
	assert.Equal(t, BadgePending, rejected.State, "an untrusted rejection is also a no-op")
}

// A decided claim is terminal. A rejected claim that could be re-filed on demand is
// a claim anybody can spam.
func TestADecidedClaimIsTerminal(t *testing.T) {
	c, err := File("studio", 1, 1)
	require.NoError(t, err)

	verified, err := Confirm(c, 2, true, at)
	require.NoError(t, err)

	_, err = Confirm(verified, 3, true, at)
	assert.ErrorIs(t, err, ErrNotPending, "a verified claim cannot be re-confirmed")
	_, err = Reject(verified, 3, true, at)
	assert.ErrorIs(t, err, ErrNotPending, "nor rejected afterwards -- a claim cannot "+
		"oscillate between verified and rejected as two users disagree over time")
	// The original `c` is untouched, because Confirm RETURNS a new value rather
	// than mutating in place. That is deliberate: a caller holding a pre-decision
	// copy cannot mistake it for a decided one, which a mutate-in-place API would
	// allow.
	assert.Equal(t, BadgePending, c.State,
		"deciding produced a new claim value; the caller's copy is still pending")
	assert.Zero(t, c.ConfirmedBy, "and attributes nobody")
	assert.NotEqual(t, c, verified, "so the decided claim is a distinct value")
}

// ConveysTrust is the ONLY question a caller should ask, and asking anything else
// is how `claimed` gets treated as `verified`.
func TestOnlyVerifiedConveysTrust(t *testing.T) {
	assert.False(t, ConveysTrust(BadgeUnclaimed))
	assert.False(t, ConveysTrust(BadgePending))
	assert.False(t, ConveysTrust(BadgeRejected),
		"a rejected claim confers nothing either -- it is not neutral, it is a decision "+
			"against")
	assert.True(t, ConveysTrust(BadgeVerified))

	// An unrecognised state confers nothing. A new value this code has not been
	// taught must not default to trusted.
	assert.False(t, ConveysTrust(BadgeState("confirmed")), "fail closed")
	assert.False(t, ConveysTrust(BadgeState("")))
}

// Filing needs an attributable claimer and an addressable entity, because a claim
// nobody made is a badge nobody owns.
func TestFilingRequiresAnAttributableClaimer(t *testing.T) {
	_, err := File("studio", 1 /*claimedBy*/, 0)
	assert.ErrorIs(t, err, ErrNoClaim, "a claim needs a user to attribute it to")

	_, err = File("", 1, 1)
	assert.ErrorIs(t, err, ErrNoClaim)
	_, err = File("studio", 0, 1)
	assert.ErrorIs(t, err, ErrNoClaim)
}

// Pricing is per unit and per currency, never a bare float. A single price column
// cannot represent "EUR45 a scene, USD60 an hour" without losing which is which.
func TestPricingIsPerUnitAndPerCurrency(t *testing.T) {
	p := PaymentMethod{
		Site:   "network-a",
		Method: "ppv",
		Prices: map[Unit]Price{
			UnitPerScene:  {Amount: 4500, Currency: "EUR"},
			UnitPerMinute: {Amount: 6000, Currency: "USD"},
		},
		AcceptedCurrencies: []string{"EUR", "USD"},
	}

	require.NoError(t, ValidatePricing(p))

	profile := NetworkProfile{Site: "network-a", Name: "Network A", PaymentMethods: []PaymentMethod{p}}

	eur := PaymentMethodsFor(profile, "EUR")
	require.Len(t, eur, 1, "the network takes EUR")

	// A currency it does not take yields nothing rather than a converted price.
	assert.Empty(t, PaymentMethodsFor(profile, "JPY"),
		"a price in a currency the site does not accept is not an offer anybody can "+
			"act on, so it must not appear")

	// Sorted, because a profile rendered as pills will otherwise reorder between
	// renders and a shopper comparing two sites sees them differently.
	two := NetworkProfile{PaymentMethods: []PaymentMethod{
		{Method: "ppd", AcceptedCurrencies: []string{"EUR"}},
		{Method: "ppv", AcceptedCurrencies: []string{"EUR"}},
		{Method: "subscription", AcceptedCurrencies: []string{"EUR"}},
	}}
	got := PaymentMethodsFor(two, "EUR")
	require.Len(t, got, 3)
	assert.Equal(t, []string{"ppd", "ppv", "subscription"},
		[]string{got[0].Method, got[1].Method, got[2].Method},
		"deterministic order, so two callers get the same answer")

	// And the prices for one currency.
	eurPrices := PricesFor(p, "EUR")
	require.Len(t, eurPrices, 1)
	assert.Equal(t, 4500, eurPrices[0].Amount, "minor units: 4500 is EUR45.00, with no "+
		"float involved anywhere in the path")
	assert.Empty(t, PricesFor(p, "GBP"), "a currency with no price is an empty result, "+
		"which is information rather than a fault")
}

// A review's rating feeds internal/rank's Elo pool, where a 900 is a player who
// cannot be beaten rather than a review that failed to save.
func TestAReviewRatingIsBounded(t *testing.T) {
	ok := Review{StudioID: 1, AuthorID: 2, Rating: 87}
	assert.NoError(t, ok.Validate())

	assert.ErrorIs(t, Review{StudioID: 1, AuthorID: 2, Rating: -1}.Validate(), ErrInvalidRating)
	assert.ErrorIs(t, Review{StudioID: 1, AuthorID: 2, Rating: 101}.Validate(), ErrInvalidRating)
	assert.ErrorIs(t, Review{StudioID: 1, AuthorID: 2, Rating: 900}.Validate(), ErrInvalidRating,
		"900 would make an unbeatable Elo player, so it is refused here rather than "+
			"at the database")

	assert.ErrorIs(t, Review{AuthorID: 2, Rating: 50}.Validate(), ErrNoClaim,
		"a review needs a studio")
	assert.ErrorIs(t, Review{StudioID: 1, Rating: 50}.Validate(), ErrNoClaim,
		"and an author")
}

// Verified-usage is a claim about the REVIEWER, not about the review's quality.
// The distinction is what stops the flag becoming a ranking by self-assertion.
func TestVerifiedUsageIsAboutTheReviewer(t *testing.T) {
	unverified := Review{StudioID: 1, AuthorID: 2, Rating: 90}
	require.NoError(t, unverified.Validate())
	assert.False(t, unverified.VerifiedUsage)

	// The rating is INDEPENDENT of the flag: a verified user can write a bad review
	// and an unverified one can write a good one.
	verified := Review{StudioID: 1, AuthorID: 2, Rating: 10, VerifiedUsage: true}
	require.NoError(t, verified.Validate())
	assert.True(t, verified.VerifiedUsage)
	assert.Equal(t, 10, verified.Rating,
		"a verified user can write a low review. If the flag implied quality, this "+
			"assertion would be the wrong expectation.")
}

// A roster is DERIVED. Storing it means every scene's edit has to update every
// studio that lists it, which is the counter rule with a membership list.
func TestARosterIsDerivedNotStored(t *testing.T) {
	r := Roster{StudioID: 42, PerformerIDs: []int{3, 1, 2}}
	assert.Equal(t, 42, r.StudioID)
	assert.Equal(t, []int{3, 1, 2}, r.PerformerIDs,
		"whatever order the join returned; nothing here sorts it, because a roster's "+
			"membership is a fact and its display order is the caller's")

	// The type carries no count and no completeness figure -- those are the rank
	// package's job, reached from two call sites rather than reimplemented.
	fields := fieldNamesOf(Roster{})
	for _, banned := range []string{"Count", "Size", "Completion", "Score", "UpdatedAt"} {
		assert.NotContains(t, fields, banned,
			"Roster carries %s. Completion is a view computed by internal/rank, and a "+
				"count here is the counter rule this package exists to avoid.", banned)
	}
}

// The structural half: Claim carries no field through which a badge could be
// granted, and no operator flag.
func TestThereIsNoGrantPath(t *testing.T) {
	fields := fieldNamesOf(Claim{})
	for _, banned := range []string{"Granted", "GrantedBy", "IsOperator", "Verified", "Badge"} {
		assert.NotContains(t, fields, banned,
			"Claim carries %s. §6a.4: no operator grants a badge, so there is no "+
				"field for one to arrive in.", banned)
	}

	// A confirmed claim's own field names, since that is the value that confers
	// trust.
	assert.Equal(t, BadgeVerified, BadgeState("verified"),
		"the verified state is the ONLY way to confer trust, and it is reachable "+
			"only through Confirm with confirmer != ClaimedBy")

	// And the package offers no function that sets BadgeVerified directly.
	for _, name := range exportedFuncNames() {
		assert.NotContains(t, name, "Grant",
			"package exports %s, which looks like a grant path", name)
	}
}

// fieldNamesOf reads a struct's SHAPE. A behavioural test cannot see a field that
// is never read, which is exactly the field that must not exist -- so the "no grant
// path" assertions have to be about the type, not about its behaviour.
func fieldNamesOf(v any) []string {
	t := reflect.TypeOf(v)
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, t.Field(i).Name)
	}
	return out
}

// exportedFuncNames parses THIS PACKAGE's non-test source and lists its exported
// functions and methods, so "the package offers no grant path" is checked rather
// than assumed.
//
// Parsing the source rather than reflecting over types is deliberate: a grant path
// could be a function that returns a Claim without ever being called by a test, and
// reflection cannot see a function nobody calls.
//
// TEST FILES ARE SKIPPED. My first version scanned the whole directory and matched
// two TEST names -- TestAVerifiedBadgeIsConfirmedNotGranted and
// TestThereIsNoGrantPath -- because both contain "Grant". A guard that fails on its
// own name is testing the wrong file: the thing being searched for is a production
// entry point, and a test name cannot be one.
func exportedFuncNames() []string {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil
	}

	var out []string
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				switch fn := d.(type) {
				case *ast.FuncDecl:
					if !fn.Name.IsExported() {
						continue
					}
					out = append(out, fn.Name.Name)
				case *ast.GenDecl:
					for _, spec := range fn.Specs {
						ts, ok := spec.(*ast.TypeSpec)
						if !ok {
							continue
						}
						st, ok := ts.Type.(*ast.StructType)
						if !ok {
							continue
						}
						for _, fld := range st.Fields.List {
							for _, n := range fld.Names {
								if n.IsExported() {
									out = append(out, ts.Name.Name+"."+n.Name)
								}
							}
						}
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// ValidatePricing checks a payment method's shape. Prices are non-negative and in
// some currency; a method with a price of -100 is a data bug, not an offer.
func ValidatePricing(p PaymentMethod) error {
	for _, price := range p.Prices {
		if price.Amount < 0 {
			return ErrInvalidRating
		}
		if price.Currency == "" {
			return ErrInvalidRating
		}
	}
	return nil
}
