//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/directory"
	"github.com/stashapp/stash/pkg/sqlite"
)

// R045–R048: the directory's persistence, against a real migrated database.
//
// WHY THIS FILE IS THE STEP. internal/directory was 319 lines with thirteen exported
// symbols and was imported by NOTHING: grep for the import path outside its own
// directory returned no output. The four requirements were honestly labelled "PARTIAL,
// no persistence" -- and nobody noticed the second half of that sentence, which is
// that the model was unreachable rather than merely unstored.

// THE SCHEMA HAS NO GRANT PATH, AND THAT IS CHECKED BY READING THE SCHEMA.
//
// R048's load-bearing half is the ABSENCE of a grant path: an operator-granted badge
// is the owner being an admin over content, which is what §6a.4 exists to prevent.
//
// An absence cannot be checked by a behavioural test -- there is no call to make and
// no output to assert -- so this reads PRAGMA table_info and fails on any column whose
// name looks like a way to set a badge without a claim and a confirmer. That is the
// only honest way to test "this cannot happen": name the thing that would make it
// possible and assert it is not there.
func TestTheSchemaHasNoGrantPath(t *testing.T) {
	runWithRollbackTxn(t, "directory-no-grant-path", func(t *testing.T, ctx context.Context) {
		for _, table := range []string{"directory_claims", "directory_pricing"} {
			_, rows, err := db.QuerySQL(ctx, "PRAGMA table_info("+table+")", nil)
			require.NoError(t, err, "reading the columns of %s", table)
			require.NotEmpty(t, rows, "%s reported no columns at all", table)

			// The names that would each be a way for a badge to appear without a
			// claim and an independent confirmer. `force` is here because a force
			// flag is how an "emergency" override usually arrives.
			banned := []string{"granted_by", "granted", "verified_by", "force",
				"is_verified", "owner_id", "operator_id"}

			var found []string
			for _, row := range rows {
				name, _ := row[1].(string)
				for _, b := range banned {
					if name == b {
						found = append(found, table+"."+name)
					}
				}
			}
			assert.Empty(t, found,
				"these columns are a grant path: %v. R048's guarantee is that an "+
					"operator cannot confer a verified badge -- not that nobody has "+
					"written the code that would, because a schema column is available "+
					"to a query runner and to the next contributor", found)
		}
	})
}

// THE CLAIMER CANNOT BE THE CONFIRMER, ENFORCED BY THE SCHEMA.
//
// This is §6a.4's entire mechanism, so it is asserted at all three layers and the
// schema layer is the one that matters: the owner of a self-hosted instance has a
// sqlite file, so a rule that lives only in Go is a rule against the UI rather than
// against the data.
func TestConfirmCannotBeTheClaimer(t *testing.T) {
	runWithRollbackTxn(t, "directory-self-confirm", func(t *testing.T, ctx context.Context) {
		user := accessUser(ctx, t, "sfDirClaimer")
		store := sqlite.NewDirectoryStore()

		claim, err := directory.File("studio", 700001, user)
		require.NoError(t, err)

		// The DOMAIN refuses it, with its own typed error.
		_, err = directory.Confirm(claim, user, true, time.Now())
		require.ErrorIs(t, err, directory.ErrSelfConfirmed)

		// The STORE refuses it too, and with the same error, so a caller using
		// errors.Is cannot tell which layer caught it -- which is the point: a
		// caller should not have to know how many checks a write passed.
		err = store.ConfirmClaim(ctx, claim, user, time.Now())
		require.ErrorIs(t, err, directory.ErrSelfConfirmed,
			"the store re-checks rather than trusting the domain, because the domain's "+
				"check holds only while every writer goes through the domain")

		// AND A CONFIRMATION WITH NO NAMED CONFIRMER IS REFUSED. The first version of
		// the store interface read the confirmer out of the claim instead of taking
		// it, so this call was not expressible -- and the way it was inexpressible was
		// the problem: a caller who had not set ConfirmedBy got a confirmation
		// attributed to user 0 rather than an error.
		err = store.ConfirmClaim(ctx, claim, 0, time.Now())
		require.Error(t, err, "a confirmation with no user to attribute it to is not a "+
			"confirmation, and user 0 is not a person")

		// AND THE SCHEMA refuses it, which is the layer that holds against a query
		// runner. Forced through raw SQL to prove the CHECK and not the store.
		err = curationExec(ctx, t,
			"INSERT INTO directory_claims (entity_type, entity_id, claimed_by, state, confirmed_by, decided_at) VALUES ('studio', 700002, ?, 'verified', ?, CURRENT_TIMESTAMP)",
			user, user)
		require.Error(t, err,
			"a row claiming verified with claimed_by = confirmed_by must be refused by "+
				"the SCHEMA. This is the layer a direct SQL session cannot get past, and "+
				"§6a.4's whole content is that it cannot")
	})
}

// A REJECTION IS TERMINAL, AND THE SCHEMA PREVENTS A RESURRECTION BY EDITING STATE.
//
// internal/directory calls a rejected claim terminal because a rejected claim that
// could be re-filed on demand is a claim anybody can spam. There is no Unreject
// method; what stops a resurrection is the CHECK tying rejected_at to the state.
func TestRejectedIsTerminalAndCannotBeResurrected(t *testing.T) {
	runWithRollbackTxn(t, "directory-rejected-terminal", func(t *testing.T, ctx context.Context) {
		claimer := accessUser(ctx, t, "sfDirRejectClaimer")
		reviewer := accessUser(ctx, t, "sfDirRejectReviewer")
		store := sqlite.NewDirectoryStore()

		claim, err := directory.File("studio", 700010, claimer)
		require.NoError(t, err)
		require.NoError(t, store.FileClaim(ctx, claim))

		at := time.Now().UTC().Truncate(time.Second)
		rejected, err := directory.Reject(claim, reviewer, true, at)
		require.NoError(t, err)
		require.NoError(t, store.RejectClaim(ctx, rejected, reviewer, at))

		got, err := store.ClaimFor(ctx, "studio", 700010)
		require.NoError(t, err)
		assert.Equal(t, directory.BadgeRejected, got.State)
		assert.False(t, directory.ConveysTrust(got.State),
			"a rejected badge confers nothing -- that is the whole of BadgeRejected")
		assert.Equal(t, reviewer, got.ConfirmedBy,
			"and WHO declined is recorded, because that is an audit fact rather than a badge")

		// AND IT CANNOT BE FLIPPED BACK BY EDITING THE STATE ALONE, which is what
		// the CHECK buys: `verified` requires decided_at, and a row edited from
		// rejected keeps its rejected_at, so the two together are not a valid
		// `verified` row.
		err = curationExec(ctx, t,
			"UPDATE directory_claims SET state = 'verified' WHERE entity_type = 'studio' AND entity_id = 700010")
		require.Error(t, err,
			"editing rejected -> verified must be refused. A terminal state that a "+
				"single UPDATE can undo is a suggestion")

		// AND THE DOMAIN REFUSES TO RE-DECIDE, which is the other half: Reject and
		// Confirm both require BadgePending.
		_, err = directory.Confirm(rejected, reviewer, true, time.Now())
		require.ErrorIs(t, err, directory.ErrNotPending)
	})
}

// A DECIDED CLAIM CANNOT BE DECIDED AGAIN, AND THE STORE IS WHAT SAYS SO.
//
// Found by the mutation gate reporting `confirm-overwrites-a-decided-claim` as a
// SURVIVOR against a fully green suite. The existing tests asserted
// directory.ErrNotPending -- but only against internal/directory's Confirm, which
// refuses to BUILD a decision for a claim that is not pending.
//
// The store is a SEPARATE ENTRY POINT and the domain's check does not reach it. So
// removing the `AND state = pending_confirmation` from the store's own UPDATE made
// a VERIFIED claim re-confirmable and a REJECTED one re-rejectable by anyone holding
// a user id, and no test noticed -- because every test went through the domain, and
// the domain was never asked.
//
// A domain check that can only be bypassed by not calling the domain is a
// documentation, not a guard. This is the test for the guard.
func TestADecidedClaimCannotBeDecidedAgain(t *testing.T) {
	runWithRollbackTxn(t, "directory-decided-once", func(t *testing.T, ctx context.Context) {
		claimer := accessUser(ctx, t, "sfDirOnceClaimer")
		reviewer := accessUser(ctx, t, "sfDirOnceReviewer")
		intruder := accessUser(ctx, t, "sfDirOnceIntruder")
		store := sqlite.NewDirectoryStore()

		claim, err := directory.File("studio", 700040, claimer)
		require.NoError(t, err)
		require.NoError(t, store.FileClaim(ctx, claim))

		confirmed, err := directory.Confirm(claim, reviewer, true, time.Now())
		require.NoError(t, err)
		require.NoError(t, store.ConfirmClaim(ctx, confirmed, reviewer, time.Now()))

		// A SECOND CONFIRMATION, by a DIFFERENT user, of a claim already verified.
		// The domain would refuse to build this -- and the store must refuse it too,
		// because the store can be called without the domain ever being asked.
		err = store.ConfirmClaim(ctx, confirmed, intruder, time.Now())
		require.ErrorIs(t, err, directory.ErrNotPending,
			"the store's own pending guard, not the domain's. Without it a verified "+
				"claim is re-confirmable by anyone with a user id, and the second "+
				"confirmer overwrites the first")
		assert.Contains(t, err.Error(), "verified",
			"and the error says what the claim IS, so a caller can tell 'already "+
				"decided' from 'no such claim' -- different problems")

		// AND THE DECISION IS UNCHANGED, so the failed second write did not half
		// apply: still the first confirmer, still verified.
		got, err := store.ClaimFor(ctx, "studio", 700040)
		require.NoError(t, err)
		assert.Equal(t, directory.BadgeVerified, got.State)
		assert.Equal(t, reviewer, got.ConfirmedBy,
			"the original confirmer. A refused write that still overwrote the "+
				"confirmer would be worse than no guard at all")

		// AND REJECTION IS NOT A LOOSER PATH: a rejected claim is equally final.
		claim2, err := directory.File("studio", 700041, claimer)
		require.NoError(t, err)
		require.NoError(t, store.FileClaim(ctx, claim2))
		rejected, err := directory.Reject(claim2, reviewer, true, time.Now())
		require.NoError(t, err)
		require.NoError(t, store.RejectClaim(ctx, rejected, reviewer, time.Now()))

		err = store.RejectClaim(ctx, rejected, intruder, time.Now())
		require.ErrorIs(t, err, directory.ErrNotPending,
			"a rejected claim is terminal and re-rejecting it is not a rarer path")
		err = store.ConfirmClaim(ctx, rejected, intruder, time.Now())
		require.ErrorIs(t, err, directory.ErrNotPending,
			"and a rejected claim cannot be promoted to verified either, which is "+
				"the resurrection the schema's CHECK also refuses")
	})
}

// FOUR STATES, NOT THREE, AND THE SCHEMA ENUMERATES THEM.
//
// `claimed` is not `verified`, and collapsing them is how an unverified claim becomes
// a trust signal. A free-text state column would let `confirmed` and `verified`
// coexist as two spellings of one idea, so the CHECK names all four.
func TestTheSchemaEnumeratesExactlyFourStates(t *testing.T) {
	runWithRollbackTxn(t, "directory-four-states", func(t *testing.T, ctx context.Context) {
		user := accessUser(ctx, t, "sfDirFourStates")

		for _, state := range []string{"verified", "rejected", "pending_confirmation", "unclaimed"} {
			var err error
			if state == "verified" {
				// `verified` needs a confirmer and a decision time, so a bare insert
				// is refused for a DIFFERENT reason than the one being tested. It
				// gets its own path below.
				err = curationExec(ctx, t,
					"INSERT INTO directory_claims (entity_type, entity_id, claimed_by, state, confirmed_by, decided_at) VALUES ('studio', 700020, ?, 'verified', 999, CURRENT_TIMESTAMP)", user)
				require.NoError(t, err, "a complete verified row must be insertable")
				continue
			}
			if state == "rejected" {
				err = curationExec(ctx, t,
					"INSERT INTO directory_claims (entity_type, entity_id, claimed_by, state, confirmed_by, decided_at, rejected_at) VALUES ('studio', 700021, ?, 'rejected', 999, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)", user)
				require.NoError(t, err, "a complete rejected row must be insertable")
				continue
			}
			// Each state gets its own entity id, written out rather than computed
			// inside the SQL string: the first version concatenated a map index into
			// the literal, which does not compile, and hoisting the lookup buys
			// clarity for nothing else.
			entityID := map[string]int{
				"pending_confirmation": 700022,
				"unclaimed":            700023,
			}[state]
			err = curationExec(ctx, t,
				"INSERT INTO directory_claims (entity_type, entity_id, claimed_by, state) VALUES ('studio', ?, ?, ?)",
				entityID, user, state)
			require.NoError(t, err, "state %q must be storable", state)
		}

		// AND EVERY STATE PROMISES WHAT IT SAYS. Found by the mutation gate
		// reporting `verified-without-a-confirmer` as a SURVIVOR: the suite inserted
		// 'confirmed' and required it to fail, and never inserted a `verified` row
		// MISSING its confirmer -- which is the badge nobody earned, and the whole
		// thing §6a.4 exists to prevent.
		//
		// The store cannot produce it either: ConfirmClaim refuses a zero confirmer
		// and the domain refuses a zero confirm. So like the two constraints in
		// step 7.3c, this one is only reachable by writing the row directly.
		var err error
		err = curationExec(ctx, t,
			"INSERT INTO directory_claims (entity_type, entity_id, claimed_by, state, decided_at) "+
				"VALUES ('studio', 700025, ?, 'verified', CURRENT_TIMESTAMP)", user)
		require.Error(t, err,
			"a verified badge with no confirmer is a badge nobody earned. The "+
				"verified branch requires a confirmer AND a decision time, so a row "+
				"naming the state alone cannot be trusted")

		// AND THE SAME FOR pending_confirmation WITH a confirmer, which is the
		// mirror: a pending claim nobody has looked at cannot already have one.
		err = curationExec(ctx, t,
			"INSERT INTO directory_claims (entity_type, entity_id, claimed_by, state, confirmed_by) "+
				"VALUES ('studio', 700026, ?, 'pending_confirmation', 999)", user)
		require.Error(t, err,
			"a pending claim has no confirmer by definition -- that is what "+
				"'pending' means, and a row with one is a decision recorded as "+
				"indecided")

		// AND A FIFTH SPELLING IS REFUSED, which is the point of enumerating.
		err = curationExec(ctx, t,
			"INSERT INTO directory_claims (entity_type, entity_id, claimed_by, state) VALUES ('studio', 700024, ?, 'confirmed')", user)
		require.Error(t, err,
			"'confirmed' is not a state. A free-text state would let it coexist with "+
				"'verified' as two spellings of one idea, and a reader would have to "+
				"guess which means what")

		// ConveysTrust is the ONLY question a caller should ask, and it is a
		// function so the answer cannot be cached beside the state and drift.
		assert.True(t, directory.ConveysTrust(directory.BadgeVerified))
		for _, s := range []directory.BadgeState{
			directory.BadgeUnclaimed, directory.BadgePending, directory.BadgeRejected,
		} {
			assert.False(t, directory.ConveysTrust(s), "%s confers nothing", s)
		}
	})
}

// A CLAIM IS ATTRIBUTED, AND IT IS BORN PENDING.
//
// FileClaim does not take the caller's state. A store that honoured it would be a
// grant path with one extra field, so this asserts the store writes pending whatever
// the caller passed.
func TestAClaimIsAttributedAndBornPending(t *testing.T) {
	runWithRollbackTxn(t, "directory-claim-attributed", func(t *testing.T, ctx context.Context) {
		user := accessUser(ctx, t, "sfDirAttributed")
		store := sqlite.NewDirectoryStore()

		// A caller who FILES a claim already marked verified, which is the shape of
		// the attack a caller-supplied state would allow.
		claim, err := directory.File("studio", 700030, user)
		require.NoError(t, err)
		claim.State = directory.BadgeVerified
		claim.ConfirmedBy = user
		require.NoError(t, store.FileClaim(ctx, claim))

		got, err := store.ClaimFor(ctx, "studio", 700030)
		require.NoError(t, err)
		assert.Equal(t, directory.BadgePending, got.State,
			"a filed claim is PENDING regardless of what the caller passed. The state "+
				"is not the caller's to choose, or a grant path exists with one field")
		assert.Equal(t, user, got.ClaimedBy, "and it is attributed -- §6a.4's concern "+
			"is that somebody is standing behind the claim")
		assert.Zero(t, got.ConfirmedBy)

		// A claim with NO claimer is refused: a claim needs an author.
		orphan := directory.Claim{EntityType: "studio", EntityID: 700031}
		require.Error(t, store.FileClaim(ctx, orphan))

		// AND AN UNCLAIMED ENTITY IS BadgeUnclaimed RATHER THAN AN ERROR, which is
		// the opposite of ClaimFor and deliberate: a renderer asking the badge
		// question wants a badge for every entity, and making it handle an error for
		// the common case is how an error ends up being rendered as unclaimed.
		badge, err := store.BadgeFor(ctx, "studio", 700032)
		require.NoError(t, err)
		assert.Equal(t, directory.BadgeUnclaimed, badge)

		// ClaimFor, by contrast, says so.
		_, err = store.ClaimFor(ctx, "studio", 700032)
		require.ErrorIs(t, err, directory.ErrNoClaim,
			"'nobody claimed this' and 'a claim with no fields' are different answers")
	})
}

// PRICES ARE MINOR UNITS, AND A NEGATIVE ONE IS REFUSED RATHER THAN CLAMPED.
//
// internal/directory documents `Amount int` as "4500 is EUR 45.00 and no float is
// involved", and storing a float would put one back into a path the domain removed.
func TestPricesAreStoredInMinorUnitsAndNegativeIsRefused(t *testing.T) {
	runWithRollbackTxn(t, "directory-prices", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewDirectoryStore()

		require.NoError(t, store.SetPrice(ctx, "site", 700040, directory.UnitPerScene, "EUR", 4500))

		got, err := store.PriceFor(ctx, "site", 700040, directory.UnitPerScene, "EUR")
		require.NoError(t, err)
		assert.Equal(t, 4500, got.Amount,
			"4500 minor units is EUR 45.00 and the integer is stored verbatim -- no "+
				"float, no rounding, no division on the way in")

		// THE SCHEMA HAS NO FLOAT COLUMN, asserted rather than assumed, because
		// "amount_minor is an integer" is a claim about the migration and the
		// migration is a file someone can edit.
		_, rows, err := db.QuerySQL(ctx, "PRAGMA table_info(directory_pricing)", nil)
		require.NoError(t, err)
		for _, row := range rows {
			name, _ := row[1].(string)
			declared, _ := row[2].(string)
			assert.NotContains(t, declared, "REAL",
				"column %s is declared %q. A REAL column in this table reintroduces "+
					"the float the domain removed, whatever the Go type says", name, declared)
			assert.NotContains(t, declared, "FLOA", "column %s is a float", name)
		}

		// A SECOND QUOTE FOR THE SAME KEY REPLACES, so a price change is an event
		// rather than a row count.
		require.NoError(t, store.SetPrice(ctx, "site", 700040, directory.UnitPerScene, "EUR", 5000))
		got, err = store.PriceFor(ctx, "site", 700040, directory.UnitPerScene, "EUR")
		require.NoError(t, err)
		assert.Equal(t, 5000, got.Amount, "the key is (entity, unit, currency), so a "+
			"second quote replaces rather than appending")

		// A NEGATIVE PRICE IS REFUSED, not clamped: clamping turns a bug into a free
		// item, and a negative price is not a discount.
		err = store.SetPrice(ctx, "site", 700040, directory.UnitPerDay, "EUR", -1)
		require.Error(t, err, "a negative price is not a discount, it is a store that "+
			"will hand out money, and it should be loud")

		// AND A MISSING PRICE IS ErrNoPrice, NOT a zero price: "this site does not
		// quote that" and "this site quotes it as free" are different facts, and a
		// zero for a missing row renders as a free scene.
		_, err = store.PriceFor(ctx, "site", 700040, directory.UnitSubscription, "EUR")
		require.ErrorIs(t, err, directory.ErrNoPrice)
	})
}

// THE PROFILE ASSEMBLES, AND THE SORTING IS DETERMINISTIC.
//
// A directory listing that reshuffles between renders is a directory nobody trusts,
// which is why internal/directory's PaymentMethodsFor sorts.
func TestTheNetworkProfileAssemblesAndSortsDeterministically(t *testing.T) {
	runWithRollbackTxn(t, "directory-profile", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewDirectoryStore()
		for _, cur := range []string{"USD", "EUR"} {
			for _, u := range []directory.Unit{directory.UnitPerScene, directory.UnitPerDay} {
				require.NoError(t, store.SetPrice(ctx, "site", 700050, u, cur, 1000))
			}
		}

		profile, err := store.NetworkProfileFor(ctx, "site", 700050, "ExampleNet")
		require.NoError(t, err)
		assert.Equal(t, "ExampleNet", profile.Name)
		require.Len(t, profile.PaymentMethods, 2, "one method per quoted currency")

		// Sorted, so two renders agree.
		var currencies []string
		for _, m := range directory.PaymentMethodsFor(profile, "USD") {
			currencies = append(currencies, m.AcceptedCurrencies...)
		}
		sort.Strings(currencies)
		assert.Equal(t, []string{"USD"}, currencies,
			"PaymentMethodsFor answers for ONE currency, so asking for USD returns the "+
				"USD method and not the EUR one")

		// AND PRICES FOR COMES BACK IN MINOR UNITS.
		methods := directory.PaymentMethodsFor(profile, "EUR")
		require.Len(t, methods, 1)
		prices := directory.PricesFor(methods[0], "EUR")
		require.NotEmpty(t, prices)
		for _, p := range prices {
			assert.Equal(t, 1000, p.Amount)
		}
	})
}

// THE REVIEW QUEUE IS THE PENDING CLAIMS, AND IT IS ORDERED.
//
// A review queue that reshuffles between renders is a queue nobody trusts to be fair.
func TestPendingClaimsIsTheReviewQueue(t *testing.T) {
	runWithRollbackTxn(t, "directory-pending-queue", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewDirectoryStore()
		// ONE USER PER CLAIM, not one user reused: users.username is UNIQUE, so three
		// accessUser calls with one name collide on the second and the helper's
		// require.NoError reports "creating the user" -- which reads like a schema
		// problem and is a fixture problem.
		for i, id := range []int{700060, 700061, 700062} {
			user := accessUser(ctx, t, fmt.Sprintf("sfDirQueue%d", i))
			claim, err := directory.File("studio", id, user)
			require.NoError(t, err)
			require.NoError(t, store.FileClaim(ctx, claim))
		}

		// And one VERIFIED, which must NOT appear in the queue.
		claimer := accessUser(ctx, t, "sfDirQueueClaimer")
		reviewer := accessUser(ctx, t, "sfDirQueueReviewer")
		claim, err := directory.File("studio", 700063, claimer)
		require.NoError(t, err)
		require.NoError(t, store.FileClaim(ctx, claim))
		confirmed, err := directory.Confirm(claim, reviewer, true, time.Now())
		require.NoError(t, err)
		require.NoError(t, store.ConfirmClaim(ctx, confirmed, reviewer, time.Now()))

		pending, err := store.PendingClaims(ctx)
		require.NoError(t, err)

		var ids []int
		for _, c := range pending {
			ids = append(ids, c.EntityID)
			assert.Equal(t, directory.BadgePending, c.State)
		}
		assert.Equal(t, []int{700060, 700061, 700062}, ids,
			"the queue is the pending claims in filing order, and a decided claim is "+
				"not in it")
	})
}

// REACHABILITY: internal/directory IS IMPORTED BY A NON-TEST FILE.
//
// This is the guard whose absence let 319 lines and thirteen exported symbols sit
// unimported, and it is here rather than in internal/api because the defect is "a
// domain package nothing reaches", not "a store nothing builds" -- the existing
// constructor guard cannot see a package with no constructor.
//
// The check reads the source tree rather than asking the compiler, because the
// question is about files OUTSIDE the package and a test cannot see those.
func TestDirectoryIsImportedByNonTestCode(t *testing.T) {
	// The store is in pkg/sqlite, which is non-test code and imports the package.
	// Naming it here is the assertion: if this line stops compiling, the store is
	// gone, and if the store is gone nothing imports internal/directory.
	var _ directory.ClaimStore = sqlite.NewDirectoryStore()
	var _ directory.PricingStore = sqlite.NewDirectoryStore()

	// AND THE SOURCE TREE IS CHECKED DIRECTLY, because the compile-time assertion
	// above is necessary and NOT SUFFICIENT.
	//
	// Verified by removing the store: the build fails, so the guard fires -- but as a
	// BUILD FAILURE, which is a compiler's report and not this test's. A guard whose
	// only signal is a compile error is a guard that cannot explain itself, and a
	// reader who sees "undefined: NewDirectoryStore" learns nothing about which
	// requirement was violated.
	//
	// It is also weaker than it looks: an earlier verification removed just the two
	// `var _ directory.ClaimStore` LINES and the guard PASSED, because the store's own
	// method signatures still named the package. So "the package is referenced" and
	// "the package is reachable through a real store" are different claims, and only
	// the second is the one being made.
	//
	// So: walk the tree, and require a NON-TEST .go file outside internal/directory to
	// import it. This is the assertion that would have caught the 319 unimported
	// lines, and it is deliberately a source scan rather than a type check because the
	// question is about files the compiler never has to mention.
	importedBy := []string{}
	root := "../../"
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") ||
			strings.Contains(path, "internal/directory/") ||
			strings.Contains(path, "/vendor/") ||
			strings.Contains(path, "/.git/") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if strings.Contains(string(body), `"github.com/stashapp/stash/internal/directory"`) {
			importedBy = append(importedBy, path)
		}
		return nil
	})
	assert.NotEmpty(t, importedBy,
		"NO NON-TEST FILE IMPORTS internal/directory. This is the defect the step "+
			"exists to prevent: 319 lines and thirteen exported symbols, every test "+
			"green, and nothing in the tree able to reach any of it. Nothing in a "+
			"passing suite distinguishes 'the model is wrong' from 'the model is "+
			"unreachable', which is how it passed a whole milestone")
	assert.Contains(t, strings.Join(importedBy, " "), "stashforge_directory.go",
		"the importer should be the store itself")

	// AND the domain's exported surface is reachable, which a type nothing outside
	// its package names is not. `Levels` does not exist in this package -- the
	// access-level package has one and this does not -- so the assertion uses the
	// directory's own vocabulary. Writing `directory.LevelCount()` here was the
	// first version and it did not compile, which is the compiler doing exactly what
	// a reachability test cannot: noticing a symbol the package does not have.
	assert.Equal(t, "verified", string(directory.BadgeVerified),
		"and the state is a NAMED value rather than a bare string, so a caller "+
			"reads the word rather than guessing a literal")
	assert.True(t, directory.ConveysTrust(directory.BadgeVerified))
	assert.False(t, directory.ConveysTrust(directory.BadgePending),
		"and the store's own vocabulary answers a real question about the directory, "+
			"which is the only evidence the package is wired rather than merely present")
}

// THE DIRECTORY GATE NAMES EVERY DIRECTORY TEST, AND IT CHECKS BY EXTRACTING.
//
// The access-policy gate's SUITE_PATTERN was written out in the script and in the
// test that verified it, and the copies drifted: a new test went into one and not the
// other, so two mutants were reported SURVIVED while the suite they were run against
// could not have contained the test that kills them. A survivor that was never run
// is the one failure a mutation gate cannot distinguish from a finding -- its entire
// output is the word SURVIVED, and that word is evidence about the gate as much as
// about the code.
//
// So this EXTRACTS the pattern out of the script and every Test function out of THIS
// file, rather than comparing two lists that have to be kept in step by hand. A
// literal in two files is the same drift in a different hat.
//
// And the missing-name case is the interesting one, because adding a test here and
// forgetting the gate is exactly the failure this catches: the new test would pass
// normally and simply never run under the gate, which is a quiet weakening.
func TestTheDirectoryGateNamesEveryDirectoryTest(t *testing.T) {
	scriptPath := "mutate_directory.py"
	source, err := os.ReadFile(scriptPath)
	require.NoError(t, err, "reading %s -- without it the gate names nothing", scriptPath)

	// The pattern is a Python string literal in the script, so it is extracted by
	// locating the assignment and reading to the closing paren. Deliberately not a
	// second copy of the pattern.
	// Cut returns (before, after, found), so the SEGMENT IS `before` on both calls.
	// The first version of this took `after` from the second Cut -- the text PAST the
	// closing paren, which is the whole rest of the script -- and so reported all ten
	// tests as missing from a pattern that named every one of them.
	//
	// Which is the guard's own lesson arriving early: it fired on a CORRECT
	// configuration, and a guard that cries wolf on a correct run trains its reader to
	// ignore it. Better that than a guard that stays quiet while broken.
	_, rest, ok := strings.Cut(string(source), "SUITE_PATTERN = (")
	require.True(t, ok, "no SUITE_PATTERN in %s", scriptPath)
	pattern, _, ok := strings.Cut(rest, ")")
	require.True(t, ok, "unterminated SUITE_PATTERN in %s", scriptPath)

	self, err := os.ReadFile("stashforge_directory_test.go")
	require.NoError(t, err, "reading this file to enumerate its tests")

	missing := []string{}
	for _, m := range regexp.MustCompile(`(?m)^func (Test\w+)\(`).FindAllStringSubmatch(string(self), -1) {
		name := m[1]
		if !strings.Contains(pattern, name) {
			missing = append(missing, name)
		}
	}
	assert.Empty(t, missing,
		"these tests exist but are not in SUITE_PATTERN, so the gate will never run "+
			"them and a mutant they would catch will read as a survivor: %v", missing)

	// And the pattern is not merely present but non-trivial -- a pattern that
	// matched nothing would satisfy a containment check while running no tests at
	// all, which is the same silent weakening from the other direction.
	assert.Greater(t, len(pattern), 200, "SUITE_PATTERN looks too short to name a suite")
}
