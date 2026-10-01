package subjects

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE REQUIREMENT: "Denied and consented-out objects are never replication subjects."
//
// The interesting half is the word NEVER, and it is about the SUBJECT where both
// conditions hold at once: a scene whose locators are DENIED and whose share choice
// is opted IN. A gate written as `if optedIn && !denied` permits that subject — and
// that is precisely the subject a preservation bounty would select.
func TestADeniedObjectIsNeverAReplicationSubject(t *testing.T) {
	for name, s := range map[string]Subject{
		"denied, not opted in": {
			SceneID: "s1", MetadataShare: collab.ChoiceOptedOut,
			LocatorTier: collab.TierDenied,
		},
		"denied, opted in -- THE SUBJECT THAT MATTERS": {
			SceneID: "s2", MetadataShare: collab.ChoiceOptedIn,
			LocatorTier: collab.TierDenied,
		},
		"denied, share value this build does not know": {
			SceneID: "s3", MetadataShare: collab.ShareChoice("future_tier"),
			LocatorTier: collab.TierDenied,
		},
	} {
		d := MayPublish(s)
		assert.False(t, d.Permitted, "%s must be refused", name)
		assert.ErrorIs(t, d.Error(), ErrDenied,
			"%s: the denial must be distinguishable from an opt-out, because they are "+
				"different problems and a caller retrying needs to tell them apart", name)
		assert.Contains(t, strings.ToLower(d.Reason), "denied")
	}
}

// #7 IS A HARD STOP AND §6b.4 SAYS A BOUNTY NEVER OVERRIDES IT. The absence of an
// override is asserted on the SHAPE, because a function that took a `force bool`
// would pass every behavioural test above and fail this one.
func TestThereIsNoOverrideAnywhereInThisPackage(t *testing.T) {
	// No exported function takes a flag that could permit a refused subject.
	rt := reflect.TypeOf(Subject{})
	for i := 0; i < rt.NumMethod(); i++ {
		m := rt.Method(i)
		// A method whose name contains override, force, bypass or ignore is the
		// shape §6b.4 forbids. A name check beats nothing at all and catches the
		// common way this goes wrong.
		lower := strings.ToLower(m.Name)
		for _, banned := range []string{"override", "force", "bypass", "ignore", "permit"} {
			assert.NotContains(t, lower, banned,
				"Subject.%s reads as an override on a consent decision. §6b.4: a "+
					"preservation bounty never overrides a denial", m.Name)
		}
	}

	// And the package's own functions take no such parameter.
	for _, fn := range []any{MayPublish, MayAccept} {
		ft := reflect.TypeOf(fn)
		for i := 1; i < ft.NumIn(); i++ {
			assert.NotEqual(t, reflect.Bool, ft.In(i).Kind(),
				"a boolean parameter on a consent gate is an override in waiting")
		}
	}
}

// THE ORDER IS LOAD-BEARING IN BOTH DIRECTIONS, and a single combined condition
// gets it wrong for the subject where both hold. Asserted by constructing exactly
// that subject and by checking WHICH refusal is reported.
func TestDenialOutranksTheShareChoice(t *testing.T) {
	optedInButDenied := Subject{
		SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
		LocatorTier: collab.TierDenied,
	}

	d := MayPublish(optedInButDenied)
	require.False(t, d.Permitted)
	assert.Equal(t, collab.TierDenied, d.Tier,
		"the decision records the tier it was made on, so a caller can log what it "+
			"decided rather than only what it decided")
	assert.ErrorIs(t, d.Error(), ErrDenied,
		"an opted-in subject with denied locators reports the DENIAL, not the opt-out. "+
			"Reporting the opt-out here would tell an operator to fix a setting when "+
			"the actual problem is a terminal denial no setting affects")
}

// QUARANTINED IS REFUSED, AND I HAD THIS WRONG.
//
// I assumed `storageRefusals` held only `denied` and wrote a test asserting that an
// opted-in quarantined subject could publish. Reading the table shows TWO entries:
// quarantined is refused too, because "a quarantined object holds material but does
// not act on it" -- and replicating it IS acting on it. The gate was right; my test
// was wrong.
//
// The distinction that does matter is the REASON, and it is why reusing the table
// rather than comparing against TierDenied alone was right: the two tiers are refused
// for different reasons, and the reason a caller reads tells it whether waiting could
// help. A quarantine can be lifted. A denial cannot.
func TestQuarantinedIsRefusedBecauseHoldingIsNotActing(t *testing.T) {
	optedIn := Subject{
		SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
		LocatorTier: collab.TierQuarantined, AcceptsReplicas: true,
	}
	d := MayPublish(optedIn)
	assert.False(t, d.Permitted,
		"§7.1's storage refusal table includes quarantined: the object holds material "+
			"but does not act on it, so replicating it would act on it")
	assert.Equal(t, collab.TierQuarantined, d.Tier)
	assert.Contains(t, strings.ToLower(d.Reason), "quarantin",
		"and the reason says so, because whether waiting could help is an operator's "+
			"first question: a quarantine can be lifted and a denial cannot")

	// The two refusals are distinguishable, which is what makes the reason worth
	// carrying.
	denied := optedIn
	denied.LocatorTier = collab.TierDenied
	assert.NotEqual(t, MayPublish(denied).Reason, d.Reason,
		"a denial and a quarantine are different problems with different remedies, and "+
			"a single message for both would tell an operator to wait for something "+
			"that can never happen")
}

// AN UNRECOGNISED TIER FAILS CLOSED, and this is the one place `IsStorageRefused`'s
// permissive default is not usable.
//
// The table returns false for a tier it does not hold, and `internal/collab` says
// that is deliberate and correct THERE: the tier is a typed constant in that file, so
// an unknown value cannot arise. It arises HERE, where the Subject crosses a wire and
// the tier came from a peer, a newer core, a hand-edited row or a truncated database.
// Inheriting a permissive default at the one point the input is untrusted is a gate
// that fails open on precisely the input an attacker would pick.
func TestAnUnrecognisedTierFailsClosed(t *testing.T) {
	for _, tier := range []collab.LocatorTier{
		"something_newer", "DENIED", "denied ", "", "quarantine",
	} {
		s := Subject{
			SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
			LocatorTier: tier, AcceptsReplicas: true,
		}
		d := MayPublish(s)
		assert.False(t, d.Permitted, "tier %q must be refused", string(tier))
		assert.NotEqual(t, ErrNoSubject, d.Error(),
			"and it must be refused by the TIER rule, not by the missing-subject rule")
	}
}

// §6.2's opt-out, failing closed on an unrecognised value. "A schema bug stops
// publishing" is the safer of the two failures.
func TestAnUnrecognisedShareChoiceIsTreatedAsOptedOut(t *testing.T) {
	for _, choice := range []collab.ShareChoice{
		collab.ChoiceOptedOut, "", collab.ShareChoice("opted_in_typo"),
		collab.ShareChoice("OPTED_IN"),
	} {
		s := Subject{SceneID: "s", MetadataShare: choice, LocatorTier: collab.TierSelfPublished}
		d := MayPublish(s)
		assert.False(t, d.Permitted, "share choice %q must be refused", string(choice))
	}
}

// THE HAPPY PATH, which is easy to leave out and impossible to notice missing.
func TestAnOptedInSubjectAtAPermissiveTierMayPublish(t *testing.T) {
	for _, tier := range []collab.LocatorTier{
		collab.TierSelfPublished, collab.TierPerformerClaimed,
		collab.TierThirdPartyPermitted,
	} {
		s := Subject{
			SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
			LocatorTier: tier, AcceptsReplicas: true,
		}
		d := MayPublish(s)
		assert.True(t, d.Permitted, "opted-in subject at tier %q must publish", tier)
		assert.Empty(t, d.Reason, "a permitted decision carries no reason")
		assert.NoError(t, d.Error())
	}
}

// NO SUBJECT IS NO SUBJECT. An unidentified object cannot be checked against a
// denial, so permitting one would mean publishing something nothing could refuse.
func TestNoSubjectIsRefused(t *testing.T) {
	for _, id := range []string{"", "   ", "\t\n"} {
		s := Subject{
			SceneID: id, MetadataShare: collab.ChoiceOptedIn,
			LocatorTier: collab.TierSelfPublished, AcceptsReplicas: true,
		}
		d := MayPublish(s)
		assert.False(t, d.Permitted, "scene id %q must be refused", id)
		assert.ErrorIs(t, d.Error(), ErrNoSubject)
		assert.Contains(t, d.Reason, "unidentified")
	}
}

// §6b.5's RECEIVING question, and the reason it is a separate function: an instance
// that publishes freely and refuses to hold is unusual but legal, and one function
// with a flag could not express it.
func TestHoldingIsRefusedWhenThisInstanceDoesNotAcceptReplicas(t *testing.T) {
	// Willing and willing to receive.
	willing := Subject{
		SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
		LocatorTier: collab.TierSelfPublished, AcceptsReplicas: true,
	}
	assert.True(t, MayAccept(willing).Permitted)

	// Willing to publish, NOT willing to hold. Legal, and MayPublish must not know
	// about it: whether this instance will store a replica says nothing about
	// whether it may send one.
	notHolding := willing
	notHolding.AcceptsReplicas = false
	assert.True(t, MayPublish(notHolding).Permitted,
		"MayPublish ignores the local willingness — that is the point of two functions")
	accept := MayAccept(notHolding)
	assert.False(t, accept.Permitted, "but MayAccept refuses it")
	assert.Contains(t, accept.Reason, "partial success",
		"§6b.5: refusing is not a partial success")

	// §6b.5: the receiving instance's OWN consent state governs, so a denied object
	// is refused on the way IN as well as on the way out.
	denied := willing
	denied.LocatorTier = collab.TierDenied
	assert.ErrorIs(t, MayAccept(denied).Error(), ErrDenied)
}

// THE ZERO VALUE REFUSES TO ACCEPT. An instance that never configured itself willing
// to hold other people's content must not start holding it because a struct literal
// omitted a bool.
func TestTheZeroValueRefusesToAcceptReplicas(t *testing.T) {
	var unset Subject
	d := MayAccept(unset)
	assert.False(t, d.Permitted)
	assert.ErrorIs(t, d.Error(), ErrNoSubject, "and it has no scene id, so it is "+
		"refused at the first gate rather than the second")

	// A zero AcceptsReplicas with everything else in order is the narrower case, and
	// it is the one worth pinning.
	ordered := Subject{
		SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
		LocatorTier: collab.TierSelfPublished,
		// AcceptsReplicas deliberately unset.
	}
	assert.True(t, MayPublish(ordered).Permitted)
	assert.False(t, MayAccept(ordered).Permitted,
		"the omitted bool defaults to refusing, which is the safe direction")
}

// THE ERROR IS DERIVED FROM THE REASON BY CONSTRUCTION, so the two cannot disagree.
// A Decision that said "denied" with a nil error is the shape that lets a caller
// treat a denial as a transient fault and retry it.
func TestTheErrorCannotDisagreeWithTheReason(t *testing.T) {
	cases := []struct {
		s    Subject
		want error
	}{
		{Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedIn, LocatorTier: collab.TierDenied}, ErrDenied},
		{Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedOut, LocatorTier: collab.TierSelfPublished}, ErrNotOptedIn},
		{Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedIn, LocatorTier: "future_tier"}, ErrUnknownTier},
		{Subject{}, ErrNoSubject},
	}

	for _, tc := range cases {
		d := MayPublish(tc.s)
		require.False(t, d.Permitted)
		assert.ErrorIs(t, d.Error(), tc.want)
		assert.NotEmpty(t, d.Reason, "a refusal always carries a reason, because a "+
			"caller re-deriving the cause is how a refusal turns into a retry")

		// And a permitted decision has no error, so `if d.Error() != nil` is a
		// complete refusal check.
		ok := MayPublish(Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
			LocatorTier: collab.TierSelfPublished})
		assert.NoError(t, ok.Error())
	}
}

// The three sentinels must be DISTINGUISHABLE, or a caller cannot tell a permanent
// refusal from a permanent refusal of a different kind — and both are the case where
// retrying is pointless.
func TestTheSentinelsAreDistinct(t *testing.T) {
	assert.False(t, errors.Is(ErrDenied, ErrNotOptedIn))
	assert.False(t, errors.Is(ErrDenied, ErrNoSubject))
	assert.False(t, errors.Is(ErrNotOptedIn, ErrNoSubject))

	// And the decision for each reports its own.
	dDenied := MayPublish(Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
		LocatorTier: collab.TierDenied})
	dOpted := MayPublish(Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedOut,
		LocatorTier: collab.TierSelfPublished})
	dTier := MayPublish(Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
		LocatorTier: "future_tier"})
	dNone := MayPublish(Subject{})

	assert.ErrorIs(t, dDenied.Error(), ErrDenied)
	assert.False(t, errors.Is(dDenied.Error(), ErrNotOptedIn),
		"a denial must not be mistaken for an opt-out: fixing an opt-out is a setting "+
			"change, and fixing a denial is impossible")
	assert.ErrorIs(t, dOpted.Error(), ErrNotOptedIn)
	assert.ErrorIs(t, dNone.Error(), ErrNoSubject)

	// The unknown tier is its own class, because its fix is a version bump rather
	// than a settings change and telling an operator to check their settings sends
	// them after the wrong thing entirely.
	assert.ErrorIs(t, dTier.Error(), ErrUnknownTier)
	assert.False(t, errors.Is(dTier.Error(), ErrNotOptedIn),
		"an unknown tier is not an opt-out, and conflating them would point an "+
			"operator at a setting when the problem is a version mismatch")
}

// THE GATE CONSISTENCY REQUIREMENT, which is why this is a package and not three
// ad-hoc checks: the three publish paths must not disagree. Asserted by checking the
// refusal REASON names the rule, so a caller reading it learns which section applies.
func TestTheRefusalReasonsNameTheRuleTheyEnforce(t *testing.T) {
	d := MayPublish(Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
		LocatorTier: collab.TierDenied})
	assert.Contains(t, d.Reason, "7.1", "a denial names §7.1, where the tier is defined")
	assert.True(t, strings.HasPrefix(d.Reason, reasonDenied),
		"and every reason STARTS with the prefix that classifies it, so the classifier "+
			"never has to guess from prose")
	assert.Contains(t, d.Reason, "bounty", "and says a bounty does not override it, "+
		"because that is the pressure the rule exists under")

	d2 := MayPublish(Subject{SceneID: "s", MetadataShare: collab.ShareChoice("nonsense"),
		LocatorTier: collab.TierSelfPublished})
	assert.Contains(t, d2.Reason, "6.2",
		"a refused SHARE CHOICE names §6.2, which is where the choice is defined")

	// An unrecognised TIER is a different rule from an unrecognised choice, so it
	// names the tier's own rule. The first version of this test asserted §6.2 for
	// every refusal, which would have sent a reader to the wrong section for a
	// problem the share choice has nothing to do with.
	d3 := MayPublish(Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
		LocatorTier: collab.LocatorTier("tier_from_a_newer_core")})
	assert.NotContains(t, d3.Reason, "6.2",
		"an unrecognised TIER is not a §6.2 problem and must not be described as one")
	assert.Contains(t, strings.ToLower(d3.Reason), "tier",
		"it says what it is: a tier this build does not know")
}

// MayPublish MUST NOT depend on the local willingness, and MayAccept must not
// publish. Asserted behaviourally: the two disagree for exactly one subject shape,
// which is the legal "publishes but does not hold" configuration.
func TestPublishAndAcceptDifferOnlyByLocalWillingness(t *testing.T) {
	base := Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
		LocatorTier: collab.TierSelfPublished}

	permissive := base
	permissive.AcceptsReplicas = true
	assert.Equal(t, MayPublish(permissive).Permitted, MayAccept(permissive).Permitted,
		"a willing instance agrees on both")

	// And for every REFUSED shape they must agree, or the two functions would be
	// gating different things.
	for name, s := range map[string]Subject{
		"denied":        {SceneID: "s", MetadataShare: collab.ChoiceOptedIn, LocatorTier: collab.TierDenied},
		"opted out":     {SceneID: "s", MetadataShare: collab.ChoiceOptedOut, LocatorTier: collab.TierSelfPublished},
		"no subject":    {},
		"unknown tier":  {SceneID: "s", MetadataShare: collab.ChoiceOptedIn, LocatorTier: collab.LocatorTier("future")},
		"unknown share": {SceneID: "s", MetadataShare: collab.ShareChoice("future"), LocatorTier: collab.TierSelfPublished},
	} {
		willing := s
		willing.AcceptsReplicas = true
		assert.Equal(t, MayPublish(willing).Permitted, MayAccept(willing).Permitted,
			"%s: both functions must refuse identically, so the questions differ only "+
				"in the local willingness", name)
	}
}

// A MISCLASSIFIED REFUSAL IS WORSE THAN AN UNCLASSIFIED ONE, and this pins the bug
// the mutation run was built to find.
//
// `Decision.Error` originally matched SUBSTRINGS: "denied" anywhere in the reason,
// else "opted in". Two things went wrong, and both sent the caller after the wrong
// cause:
//
//   - the unknown-TIER reason mentioned "assertion nobody can be identified for", and
//     the opt-out reason read "not opted in" -- so an unknown TIER classified as an
//     opt-out, pointing an operator at a SETTING when the problem is a version
//     mismatch;
//   - an opt-out whose reason did not contain either phrase fell through the switch
//     to the DEFAULT, which returned ErrDenied. That is the worst possible direction:
//     it told the caller the object was TERMINALLY DENIED, and there is no setting
//     that lifts a denial, so an operator would go looking for a remedy that does
//     not exist.
//
// The classifier now keys on a PREFIX constant every reason in the file starts with,
// so prose can no longer decide the classification.
func TestAMisclassifiedRefusalCannotHappen(t *testing.T) {
	cases := []struct {
		name string
		s    Subject
		want error
	}{
		{"denial", Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
			LocatorTier: collab.TierDenied}, ErrDenied},
		{"quarantine", Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
			LocatorTier: collab.TierQuarantined}, ErrDenied},
		{"opted out", Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedOut,
			LocatorTier: collab.TierSelfPublished}, ErrNotOptedIn},
		{"unknown share choice", Subject{SceneID: "s", MetadataShare: "future",
			LocatorTier: collab.TierSelfPublished}, ErrNotOptedIn},
		{"unknown tier", Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
			LocatorTier: "future"}, ErrUnknownTier},
		{"no subject", Subject{}, ErrNoSubject},
		{"does not accept", Subject{SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
			LocatorTier: collab.TierSelfPublished, AcceptsReplicas: false}, ErrNotOptedIn},
	}

	for _, tc := range cases {
		d := MayPublish(tc.s)
		if tc.name == "does not accept" {
			d = MayAccept(tc.s)
		}
		require.False(t, d.Permitted, "%s must be refused", tc.name)
		assert.ErrorIs(t, d.Error(), tc.want, "%s classified wrongly", tc.name)
		assert.True(t, strings.HasPrefix(d.Reason, reasonDenied) ||
			strings.HasPrefix(d.Reason, reasonRefused) ||
			strings.HasPrefix(d.Reason, reasonNoSubject),
			"%s: every reason starts with a classifier prefix, got %q", tc.name, d.Reason)
	}
}

// THE SPACE BYPASS. A trailing space on the tier made the gate PERMIT a denied
// object, because `collab.IsStorageRefused` is a raw map lookup that missed while
// `collab.ParseLocatorTier` trims first and reported the value as known.
//
// It is the sharpest thing found in this package and it earns a test of its own,
// because the fix is a single canonicalisation step that a later edit could remove
// without any behavioural test noticing.
func TestAWhitespacePaddedDeniedTierIsStillDenied(t *testing.T) {
	for _, padded := range []string{"denied", "denied ", " denied", "  denied  ",
		"\tdenied", "denied\n", " quarantined", "quarantined "} {
		s := Subject{
			SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
			LocatorTier: collab.LocatorTier(padded), AcceptsReplicas: true,
		}
		d := MayPublish(s)
		assert.False(t, d.Permitted,
			"tier %q is a padded storage-refused tier and must not be published", padded)
		assert.ErrorIs(t, d.Error(), ErrDenied,
			"tier %q must classify as a denial, not as something recoverable", padded)
	}
}

// THE DEFAULT CLASSIFIER BRANCH IS REACHABLE ONLY THROUGH THE EXPORTED API, which
// is why a mutation run had to flag it rather than a test.
//
// `Decision` has exported fields, so an outside caller can build one whose `Reason`
// is unclassifiable. Replacing the fallback `return ErrDenied` with `return nil`
// killed nothing, because no reason produced IN this package lacks a prefix. That is
// a missing test, not a dead line -- and the consequence of leaving it is that
// `d.Error() == nil` for a refusal, which a caller reads as permitted.
//
// Constructed as a caller outside the package would, not through MayPublish.
func TestAnExternallyBuiltDecisionStillFailsClosed(t *testing.T) {
	// A Decision that claims to be permitted but carries an unclassifiable reason.
	// `Permitted` short-circuits, so the interesting case is the refusal.
	handBuilt := Decision{Permitted: false, Reason: "something a caller wrote itself"}

	err := handBuilt.Error()
	require.Error(t, err, "an unclassifiable refusal must NOT report no error")
	assert.ErrorIs(t, err, ErrDenied,
		"and it fails closed as a denial, which is the unrecoverable direction a "+
			"caller will investigate rather than retry blindly")

	// An empty reason is the same hazard, and is the shape a zero Decision has.
	assert.Error(t, Decision{}.Error(),
		"a zero Decision is a refusal with no reason, and must not read as permitted")

	// And the reason a caller can supply does not get to override the verdict: a
	// permitted Decision is permitted whatever its reason says, because `Permitted`
	// is the verdict and `Reason` is documentation.
	ok := Decision{Permitted: true, Reason: reasonDenied + " but permitted"}
	assert.NoError(t, ok.Error(), "the verdict is Permitted, not the prose")
}

// A PERMITTED DECISION REPORTS THE CANONICAL TIER, and the mutant that returned the
// raw value survived until this test.
//
// `Tier` is the record of what was actually JUDGED, and the gate judges the trimmed
// value. Reporting the raw one would put a string in the log that no caller can
// compare against `collab`'s constants -- `Tier != collab.TierSelfPublished` for a
// permitted, correctly-trimmed decision, so a log consumer matching on the tier
// silently drops exactly the decisions that were allowed.
func TestAPermittedDecisionReportsTheCanonicalTier(t *testing.T) {
	for _, raw := range []string{"self_published", " self_published ",
		"\tperformer_claimed\n"} {
		s := Subject{
			SceneID: "s", MetadataShare: collab.ChoiceOptedIn,
			LocatorTier: collab.LocatorTier(raw), AcceptsReplicas: true,
		}
		d := MayPublish(s)
		require.True(t, d.Permitted, "a padded but known permitted tier may publish: %q", raw)

		canonical, known := canonicalTier(s.LocatorTier)
		require.True(t, known)
		assert.Equal(t, canonical, d.Tier,
			"the reported tier must be the one that was judged, not the raw input")

		// And it must be directly comparable with the package's constants, which is
		// the whole reason the field is there.
		switch canonical {
		case collab.TierSelfPublished, collab.TierPerformerClaimed,
			collab.TierThirdPartyPermitted, collab.TierQuarantined, collab.TierDenied,
			collab.TierUnverified:
		default:
			t.Fatalf("permitted decision reported an unknown tier %q", string(d.Tier))
		}
	}
}
