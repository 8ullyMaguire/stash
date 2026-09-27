package collab

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// # THE SHAPE OF THESE TESTS
//
// Every test here asserts on a REFUSAL, and the refusals are the security
// property: a gate that cannot refuse is not a gate. So the tests are written
// to fail if a refusal turns into a grant, and not merely to fail if a refusal
// stops happening for the right reason.
//
// The recurring shape is a stub store that COUNTS its writes. A test that only
// checks the returned Gate could be satisfied by a function that refuses for
// the wrong reason and writes anyway; the count is what makes "wrote nothing"
// a separate, checkable claim from "said no".

// countingStore records what the gate tried to do, so a test can assert on
// ACTIONS rather than on return values alone.
type countingStore struct {
	stored    []Locator
	destroyed []int64

	// reasons records why each destruction happened, because a destroy with no
	// reason is not auditable.
	reasons []string

	storeErr   error
	currentErr error

	// current is what CurrentTier reports. Set per test.
	current    LocatorTier
	notFound   bool
	tierReads  int
	storeCalls int
}

func (s *countingStore) StoreLocator(_ context.Context, l Locator) error {
	s.storeCalls++
	if s.storeErr != nil {
		return s.storeErr
	}
	s.stored = append(s.stored, l)
	return nil
}

func (s *countingStore) DestroyLocator(_ context.Context, id int64, reason string) error {
	s.destroyed = append(s.destroyed, id)
	s.reasons = append(s.reasons, reason)
	return nil
}

func (s *countingStore) CurrentTier(_ context.Context, _ int64) (LocatorTier, bool, error) {
	s.tierReads++
	if s.currentErr != nil {
		return "", false, s.currentErr
	}
	if s.notFound {
		// The ZERO TIER, not an empty string, and that is the point.
		//
		// A sql-backed CurrentTier returns whatever the scan produced, so a
		// missing row yields the zero value of its return type. Returning "" here
		// made the missing case double-refuse: the `!found` branch AND the tier
		// check, because "" is a tier that is neither third_party_permitted nor
		// refused. A test could not tell the two paths apart, and neither could a
		// mutation -- "treat a missing object as an existing one at tier
		// unverified" produced an identical answer, so it survived.
		//
		// With the zero TIER the mutation becomes observable: unverified is a
		// readable, non-refused tier, so the fallback reaches the redistribution
		// check and refuses with a message about tiers, not about a missing
		// object. Only `found` distinguishes them.
		return TierUnverified, false, nil
	}
	return s.current, true, nil
}

// A locator a well-behaved plugin would propose: valid, unattached, and at a
// tier that permits storage.
func proposed() Locator {
	return Locator{Value: "magnet:?xt=urn:btih:0123456789abcdef", Scheme: SchemeMagnet}
}

// # STORAGE: WHAT MAY BE STORED, AND WHERE THE ASYMMETRY LIVES

// TestAStoredLocatorNeedsAnObject pins the case that would otherwise produce a
// locator pointing at object 0.
//
// A locator with no object is not "unattached yet" — it is a mistake, and in
// most schemas object 0 is not free, so storing one attaches a pointer to
// whatever happens to live there. This is the kind of row that is invisible
// until someone follows the link.
func TestAStoredLocatorNeedsAnObject(t *testing.T) {
	s := &countingStore{}

	gate, err := Propose(context.Background(), s, 0, proposed())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if gate.Allowed {
		t.Error("a locator naming no object was allowed. It would be stored " +
			"attached to object id 0, which is a real id in most schemas")
	}
	if s.storeCalls != 0 {
		t.Errorf("the store was written to %d times for a locator naming no "+
			"object. The refusal has to be total, not advisory", s.storeCalls)
	}
	if !strings.Contains(gate.Reason, "no object") {
		t.Errorf("the refusal is %q, which does not say what was missing", gate.Reason)
	}
}

// TestAnEmptyLocatorIsRefusedRatherThanStored covers the row that has to be
// explained later.
func TestAnEmptyLocatorIsRefusedRatherThanStored(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t\n"} {
		s := &countingStore{}
		l := proposed()
		l.Value = blank

		gate, err := Propose(context.Background(), s, 7, l)
		if err != nil {
			t.Fatalf("Propose: %v", err)
		}
		if gate.Allowed {
			t.Errorf("a locator of %q was stored. It points at nothing", blank)
		}
		if s.storeCalls != 0 {
			t.Errorf("a locator of %q caused %d writes", blank, s.storeCalls)
		}
	}
}

// TestStorageIsRefusedAtQuarantinedAndDenied is spec §7.1's storage gate, and
// the two tiers are refused for DIFFERENT reasons — which is the point.
func TestStorageIsRefusedAtQuarantinedAndDenied(t *testing.T) {
	for _, tier := range []LocatorTier{TierQuarantined, TierDenied} {
		s := &countingStore{}
		l := proposed()
		l.Tier = tier

		gate, err := Propose(context.Background(), s, 7, l)
		if err != nil {
			t.Fatalf("%s: Propose: %v", tier, err)
		}
		if gate.Allowed {
			t.Errorf("a locator was stored against a %s object. spec 7.1 refuses "+
				"storage at both of these tiers", tier)
		}
		if s.storeCalls != 0 {
			t.Errorf("%s: the store was written to %d times. A refusal that still "+
				"writes is not a refusal", tier, s.storeCalls)
		}
		if gate.Checked != tier {
			t.Errorf("%s: the decision records tier %q, so a later audit would "+
				"blame the wrong state", tier, gate.Checked)
		}
	}
}

// TestStorageIsPermittedAtTheOtherFourTiers is the owner's deliberate
// divergence from Commons, pinned so it cannot be "corrected" by someone who
// has not read §7.1.
//
// Commons gates a magnet on third_party_permitted PLUS the redistribution flag,
// which means an amateur creator's own upload can never carry one. This
// project's owner requires a downloader that acquires material INCLUDING amateur
// material. If this test ever fails because someone applied Commons' rule, the
// fix is to read §7.1, not to change the test.
func TestStorageIsPermittedAtTheOtherFourTiers(t *testing.T) {
	for _, tier := range []LocatorTier{
		TierUnverified, TierSelfPublished,
		TierPerformerClaimed, TierThirdPartyPermitted,
	} {
		s := &countingStore{}
		l := proposed()
		l.Tier = tier

		gate, err := Propose(context.Background(), s, 7, l)
		if err != nil {
			t.Fatalf("%s: Propose: %v", tier, err)
		}
		if !gate.Allowed {
			t.Errorf("%s: storage was refused (%s). spec 7.1 permits storage at "+
				"every tier except quarantined and denied -- Commons' stricter "+
				"rule makes this owner's requirement impossible", tier, gate.Reason)
		}
		if s.storeCalls != 1 {
			t.Errorf("%s: the store was written %d times, expected once", tier, s.storeCalls)
		}
		if gate.Reason != "" {
			t.Errorf("%s: a grant carries the reason %q. A refusal must explain "+
				"itself; a grant has nothing to explain", tier, gate.Reason)
		}
	}
}

// TestTheObjectIDIsCoreSNot is the plugin having no say in where its locator
// lands.
func TestTheObjectIDIsCoreSNot(t *testing.T) {
	s := &countingStore{}

	l := proposed()
	l.ObjectID = 999 // the plugin trying to name its own object

	if _, err := Propose(context.Background(), s, 42, l); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(s.stored) != 1 {
		t.Fatalf("expected exactly one stored locator, got %d", len(s.stored))
	}
	if s.stored[0].ObjectID != 42 {
		t.Errorf("the stored locator points at object %d. The plugin named %d, "+
			"and core's argument is the one that counts",
			s.stored[0].ObjectID, 999)
	}
}

// TestAStoreFailureIsAnErrorNotARefusal is the distinction that must not be
// lost at any call site.
//
// A refusal is an ANSWER and belongs in the Gate, so a plugin can read it and
// adapt. A store failure is an OUTAGE and belongs in the error, so the caller
// retries and an operator sees it. Collapsing the two means a database blip
// looks like a policy decision.
func TestAStoreFailureIsAnErrorNotARefusal(t *testing.T) {
	wanted := errors.New("disk is full")
	s := &countingStore{storeErr: wanted}

	gate, err := Propose(context.Background(), s, 7, proposed())
	if !errors.Is(err, wanted) {
		t.Fatalf("the store's failure came back as %v, not wrapped. A store "+
			"failure that does not wrap is one the caller cannot recognise", err)
	}
	if gate.Allowed {
		t.Error("the gate reported Allowed on a store failure. A caller seeing " +
			"Allowed:true and no error would treat the locator as stored")
	}
	if gate.Reason != "" {
		t.Errorf("an outage produced the reason %q. Reasons are for policy "+
			"refusals; an outage that acquires a reason gets logged as a "+
			"permission decision", gate.Reason)
	}
}

// # THE ACTING GATE: THE ONE WITH TEETH

// TestActingNeedsThirdPartyPermitted is the distinction the whole §7.1
// asymmetry turns on, and the reason a downloader is a hard case: a
// hand-off-only plugin never redistributes, but a BitTorrent client seeds what
// it downloads, so acting IS redistribution.
func TestActingNeedsThirdPartyPermitted(t *testing.T) {
	// Storage is permitted at these tiers...
	for _, tier := range []LocatorTier{
		TierUnverified, TierSelfPublished, TierPerformerClaimed,
	} {
		s := &countingStore{current: tier}
		l := proposed()
		l.Tier = tier

		gate, err := Propose(context.Background(), s, 7, l)
		if err != nil {
			t.Fatalf("%s: Propose: %v", tier, err)
		}
		if !gate.Allowed {
			t.Fatalf("%s: storage was refused, so the acting gate is not the "+
				"interesting test here", tier)
		}

		// ...and acting is not.
		act, err := Act(context.Background(), s, 7)
		if err != nil {
			t.Fatalf("%s: Act: %v", tier, err)
		}
		if act.Permitted {
			t.Errorf("%s: the locator was handed to a client. A BitTorrent "+
				"client seeds what it downloads, so this is redistribution, and "+
				"only %s asserts the covering terms",
				tier, TierThirdPartyPermitted)
		}
		if !strings.Contains(act.Reason, "third_party_permitted") {
			t.Errorf("%s: the refusal is %q, which does not name the tier that "+
				"would have permitted it. A plugin cannot adapt to a refusal "+
				"that does not say what would work", tier, act.Reason)
		}
	}
}

// TestActingIsPermittedAtThirdPartyPermitted, and only there.
func TestActingIsPermittedAtThirdPartyPermitted(t *testing.T) {
	s := &countingStore{current: TierThirdPartyPermitted}

	act, err := Act(context.Background(), s, 7)
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if !act.Permitted {
		t.Errorf("a third_party_permitted object was refused (%s). That is the "+
			"one tier that permits the act", act.Reason)
	}
	if act.Checked != TierThirdPartyPermitted {
		t.Errorf("the decision records tier %q", act.Checked)
	}
	if act.Reason != "" {
		t.Errorf("a grant carries the reason %q", act.Reason)
	}
}

// TestActingRefusesADeniedObject, which stored perfectly well.
func TestActingRefusesADeniedObject(t *testing.T) {
	s := &countingStore{current: TierDenied}

	act, err := Act(context.Background(), s, 7)
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if act.Permitted {
		t.Error("a denied object's locator was handed to a client")
	}
	if !strings.Contains(act.Reason, "denied") {
		t.Errorf("the refusal is %q, which does not say the object is denied", act.Reason)
	}
}

// TestTheActGateRereadsTheTierRatherThanTrustingTheLocator is the requirement
// in §7.1 that the denormalised tier exists to make possible to get wrong, and
// so the one the test has to be most careful about.
//
// The Locator passed to Propose carries a tier. If the acting gate read THAT,
// a revocation would never take effect: the locator would still say
// third_party_permitted from the moment it was written, forever. So Act takes an
// object ID and reads the CURRENT tier, and the store counts its reads — a gate
// that read nothing and compared a stale field would pass a return-value-only
// test and fail this one.
func TestTheActGateRereadsTheTierRatherThanTrustingTheLocator(t *testing.T) {
	s := &countingStore{current: TierDenied}

	// The locator as it was written, when the object was fine.
	stored := proposed()
	stored.Tier = TierThirdPartyPermitted
	stored.ObjectID = 7

	if _, err := Propose(context.Background(), s, 7, stored); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	// The store now says denied: the object was revoked AFTER storage.
	s.current = TierDenied

	act, err := Act(context.Background(), s, stored.ObjectID)
	if err != nil {
		t.Fatalf("Act: %v", err)
	}

	if act.Permitted {
		t.Fatal("a locator whose object was denied after storage was still " +
			"handed to a client. This is the exact failure §7.1 names: consent " +
			"can be revoked in between, so the hand-off must re-check")
	}
	if s.tierReads == 0 {
		t.Error("Act never read the object's tier. It decided from something " +
			"else, and the only something else available is the stale " +
			"denormalised value on the locator")
	}
}

// TestActingOnAnObjectThatNoLongerExists is a refusal, not an outage.
//
// An object deleted between storage and act is an ordinary outcome of a corpus
// being edited, and reporting it as a database error would train an operator to
// ignore database errors.
func TestActingOnAnObjectThatNoLongerExists(t *testing.T) {
	s := &countingStore{notFound: true}

	act, err := Act(context.Background(), s, 7)
	if err != nil {
		t.Fatalf("a missing object came back as an error (%v). A corpus being "+
			"edited deletes objects between storage and act, and that is an "+
			"answer, not an outage", err)
	}
	// Asserted on the REASON, not only on Permitted. Permitted alone is
	// insufficient: the mutation "treat a missing object as an existing one at
	// tier zero" makes found always true, and the call then falls through to the
	// tier check -- which refuses too, for an unrelated reason. Both produce
	// Permitted:false, so a test that only checks the boolean passes on the
	// defect. This one did, and the mutation harness is why the assertion is
	// shaped this way.
	if act.Permitted {
		t.Error("a locator was handed to a client for an object that no longer exists")
	}
	if !strings.Contains(act.Reason, "no longer exists") {
		t.Errorf("the refusal is %q. It must say the object is gone rather than "+
			"reaching a tier check, because a missing object and a denied one "+
			"are different problems with different fixes", act.Reason)
	}
	if act.Checked != "" {
		t.Errorf("the decision records tier %q for an object that does not "+
			"exist. There is no tier to record, and recording one puts a "+
			"state in the audit trail that was never read", act.Checked)
	}
}

// TestAnUnreadableTierIsAnErrorRatherThanAGrant: if the re-read fails, the gate
// must not permit. Failing open here means a database blip hands out a
// redistribution permission.
func TestAnUnreadableTierIsAnErrorRatherThanAGrant(t *testing.T) {
	wanted := errors.New("connection reset")
	s := &countingStore{currentErr: wanted}

	act, err := Act(context.Background(), s, 7)
	if !errors.Is(err, wanted) {
		t.Fatalf("the read failure came back as %v, not wrapped", err)
	}
	if act.Permitted {
		t.Error("an unreadable tier produced a permission. Failing open here " +
			"means a database blip hands out a redistribution permission")
	}
}

// # TIER CHANGES

// TestDenialDestroysAndOtherChangesInvalidate is the two-outcome rule, and the
// distinction is data loss.
//
// Lifting a quarantine is a return to service, and it must not lose the
// locators that were being held — a user who quarantined an object and then
// lifted the quarantine expects the object back, not an object with its
// locators gone.
func TestDenialDestroysAndOtherChangesInvalidate(t *testing.T) {
	// A quarantine lifted: invalidated, not destroyed.
	l := Locator{ID: 5, Tier: TierQuarantined}
	got, destroyed := Invalidate(l, TierSelfPublished)
	if destroyed {
		t.Error("lifting a quarantine DESTROYED the locator. spec 7.1 reserves " +
			"destruction for denial, because a lifted quarantine is a return to " +
			"service and losing the locators is data loss")
	}
	if got == nil {
		t.Fatal("lifting a quarantine produced no record, so nothing tells the " +
			"next reader the denormalised tier is stale")
	}
	if !strings.Contains(got.Reason, "stale") {
		t.Errorf("the record is %q, which does not explain that the tier is "+
			"stale. A reader needs to know this is recoverable", got.Reason)
	}

	// A denial: destroyed, and DISTINGUISHABLY so.
	//
	// The mutation "the invalidation branch never fires" was caught only after
	// the first version of this test was tightened: `destroyed` flipping to
	// false is the whole claim, and a test that also asserted on the reason text
	// would have been satisfied by the record the mutation path still produces.
	l = Locator{ID: 5, Tier: TierUnverified}
	got, destroyed = Invalidate(l, TierDenied)
	if !destroyed {
		t.Error("denial did not destroy the locator. spec 7.1: denial is " +
			"terminal, and locators are destroyed rather than orphaned. This is " +
			"data loss in the other direction if it is wrong: a denied object " +
			"keeping a locator that points at material that must not exist")
	}
	// The record is still produced for a destruction -- it is what says WHICH
	// locator and WHY -- and the flag is what separates the two outcomes. The
	// record is not the invalidation, it is the audit of the tier change.
	if got == nil {
		t.Error("a destruction produced no record, so nothing says which locator " +
			"was destroyed or why. The flag says what happened; the record says " +
			"to what")
	} else if !strings.Contains(got.Reason, "destroyed") {
		t.Errorf("the destruction record is %q, which does not say the locator "+
			"was destroyed rather than merely held", got.Reason)
	}
}

// TestAnUnchangedTierIsNotAnEvent: a no-op must not be reported as a change,
// or every read of an unchanged object writes a row.
func TestAnUnchangedTierIsNotAnEvent(t *testing.T) {
	l := Locator{ID: 5, Tier: TierSelfPublished}

	got, destroyed := Invalidate(l, TierSelfPublished)
	if got != nil || destroyed {
		t.Errorf("an unchanged tier produced %+v, destroyed=%v. Every read of "+
			"an unchanged object would write a row", got, destroyed)
	}
}

// # TIER PARSING

// TestAnUnknownTierIsRefusedRatherThanDefaulted pins the direction that matters.
//
// defaulting to unverified would be PERMISSIVE — it is a tier that permits
// storage — so a corrupt row would become a permitted locator. The column is
// CHECKed, so this is only reachable if the check was bypassed or the row came
// from elsewhere, which is exactly when guessing is worst.
func TestAnUnknownTierIsRefusedRatherThanDefaulted(t *testing.T) {
	// NOT "unverified " with a trailing space: padding is tolerated on purpose,
	// and TestTierParsingToleratesSurroundingSpaceButNotCase asserts it. The two
	// tests contradicted each other and this is the resolution -- a padded value
	// is a display artefact, not an unknown tier, and refusing it would make
	// the parser stricter about whitespace than the database is.
	for _, bad := range []string{"", "  ", "VERIFIED", "public", "none", "3", "unverifiedd"} {
		if _, err := ParseLocatorTier(bad); err == nil {
			t.Errorf("ParseLocatorTier(%q) succeeded. An unrecognised tier must "+
				"not be guessed at, and the permissive guess here is "+
				"%s -- the tier that permits storage", bad, TierUnverified)
		}
	}
}

// TestEveryNamedTierParses, and the names list cannot drift from the parser.
func TestEveryNamedTierParses(t *testing.T) {
	names := LocatorTierNames()
	if len(names) != 6 {
		t.Fatalf("there are %d tier names, expected 6: %v", len(names), names)
	}

	for _, name := range names {
		got, err := ParseLocatorTier(name)
		if err != nil {
			t.Errorf("the listed tier %q does not parse: %v. The name list and "+
				"the parser cannot disagree", name, err)
			continue
		}
		if string(got) != name {
			t.Errorf("%q parsed to %q", name, got)
		}
	}

	// And the reverse: a tier constant that is not listed would be invisible to
	// anything driven by the list.
	for _, tier := range []LocatorTier{
		TierUnverified, TierSelfPublished, TierPerformerClaimed,
		TierThirdPartyPermitted, TierQuarantined, TierDenied,
	} {
		found := false
		for _, name := range names {
			if name == string(tier) {
				found = true
			}
		}
		if !found {
			t.Errorf("the tier %q is not in LocatorTierNames()", tier)
		}
	}
}

// TestTierParsingToleratesSurroundingSpace but not case, because the column is
// lowercase and a case-insensitive parser would accept a value the CHECK
// constraint rejects.
func TestTierParsingToleratesSurroundingSpaceButNotCase(t *testing.T) {
	if got, err := ParseLocatorTier("  denied  "); err != nil || got != TierDenied {
		t.Errorf("a padded tier gave %q, %v", got, err)
	}
	if _, err := ParseLocatorTier("Denied"); err == nil {
		t.Error("ParseLocatorTier accepted \"Denied\". The column is lowercase " +
			"and CHECKed, so accepting a case the database rejects means a value " +
			"that reads here and fails to store")
	}
}
