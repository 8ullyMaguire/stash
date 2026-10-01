package torrentpolicy

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// coreLocatorSource is core's tier declarations.
//
// Four levels up: the test runs in plugins/p2pdownloader/internal/policy, so
// policy -> internal -> p2pdownloader -> plugins -> repo root.
//
// Named rather than inlined because TWO tests read it, and a path written twice
// is a path that is correct once — which is how a stale path survives in one
// caller while the other is fixed.
const coreLocatorSource = "../../../../internal/collab/locator.go"

// # WHAT IS BEING TESTED
//
// One question, asked per torrent: **may the bytes leave this box?**
//
// The tests are shaped around the fact that the permissive answer is the
// dangerous one. An upload that should not happen is a stranger's material
// published to peers the operator never sees, and it is irreversible — once the
// chunks are out, no later decision retracts them. So the assertions lean on the
// refusals, and the `Reason` is checked on every one of them: a decision
// without a stated basis is one nobody can audit, and "why is this seeding" is
// the question an operator asks the first time it happens.

// TestOnlyAnAssertionPermitsSeeding is the table, asserted in full rather than
// as a spot check, because a tier added to core's vocabulary that nobody
// classified here would be a tier with no policy — and the default for an
// unclassified tier is decided in TestAnUnrecognisedTierIsRestrictive, not by
// this table.
func TestOnlyAnAssertionPermitsSeeding(t *testing.T) {
	permissive := []string{
		tierSelfPublished,
		tierPerformerClaimed,
		tierThirdPartyPermitted,
	}
	restrictive := []string{
		tierUnverified, // nobody has asserted anything
		tierQuarantined,
		tierDenied,
		"",                // nothing at all
		"future_tier",     // from a newer core
		"SELF_PUBLISHED",  // wrong case: the column is lowercase and CHECKed
		" self_published", // untrimmed
		"self-published",  // wrong separator
		"performer claimed",
		"third party permitted",
	}

	for _, tier := range permissive {
		got := Decide(Input{Tier: tier, OperatorAllowedSeed: true})
		if !got.Upload.CanUpload() {
			t.Errorf("tier %q was refused seeding: %s. It is an assertion by a "+
				"party with standing, which is what the permission requires",
				tier, got.Reason)
		}
		if got.Reason == "" {
			t.Errorf("tier %q permits seeding with no stated reason. A permissive "+
				"decision with no basis is the one that gets audited and found "+
				"wanting", tier)
		}
	}

	for _, tier := range restrictive {
		got := Decide(Input{Tier: tier, OperatorAllowedSeed: true})
		if got.Upload.CanUpload() {
			t.Errorf("tier %q was PERMITTED to seed. An assertion nobody can be "+
				"identified for does not cover redistribution, and %q is not one "+
				"of the three that does", tier, tier)
		}
		if got.Reason == "" {
			t.Errorf("tier %q refused seeding with no stated reason", tier)
		}
	}
}

// TestAnUnrecognisedTierIsRestrictive is the case that has to be pinned on its
// own, because it is the one a change is most likely to get wrong.
//
// Every permissive tier is a claim by an IDENTIFIED party. A value this build
// does not recognise is not one — it is a schema change, a hand-edited row, or
// a truncated database. Falling through to "unknown means allowed" would mean
// any of those silently acquires the most permissive behaviour in the table, and
// the failure is invisible because the tier is still a string and still prints.
func TestAnUnrecognisedTierIsRestrictive(t *testing.T) {
	for _, tier := range []string{
		"future_tier",
		"performer-claimed",
		"unverified ",
		"v2_tier",
		"null",
		"0",
	} {
		got := Decide(Input{Tier: tier, OperatorAllowedSeed: true})
		if got.Upload.CanUpload() {
			t.Errorf("the unrecognised tier %q was permitted to seed. Falling "+
				"through to the permissive answer would make a schema change "+
				"silently acquire the most permissive behaviour in the table",
				tier)
		}
		if !strings.Contains(got.Reason, "not one this build recognises") {
			t.Errorf("tier %q: the refusal is %q, which does not say the tier was "+
				"unrecognised. 'you may not seed because of who asserts it' and "+
				"'you may not seed because I do not know what this means' call "+
				"for different fixes", tier, got.Reason)
		}
	}
}

// TestTheOperatorCannotWidenThePolicy is the direction that matters, and it is
// the one a settings screen invites.
//
// An operator who does not want their box seeding anything should be able to
// say so without editing code. An operator who does want to seed something the
// tier does not permit should not be able to say so at all — and if a
// future version of this package grows a "force" flag, this is the test that
// says no.
func TestTheOperatorCannotWidenThePolicy(t *testing.T) {
	// The operator's setting CAN narrow.
	got := Decide(Input{Tier: tierThirdPartyPermitted, OperatorAllowedSeed: false})
	if got.Upload.CanUpload() {
		t.Errorf("seeding happened with the operator's setting off. The " +
			"permission exists in the tier and the operator declined to exercise " +
			"it, which is a narrower decision than the tier's")
	}

	// And it CANNOT widen, for every tier, including the permissive ones.
	for _, tier := range []string{
		tierUnverified, tierSelfPublished, tierPerformerClaimed,
		tierThirdPartyPermitted, tierQuarantined, tierDenied, "",
	} {
		// OperatorAllowedSeed is TRUE here: the operator has said yes. The tier
		// still decides, and for a restrictive tier the answer is no.
		got := Decide(Input{Tier: tier, OperatorAllowedSeed: true})
		wantSeed := tier == tierSelfPublished || tier == tierPerformerClaimed ||
			tier == tierThirdPartyPermitted
		if got.Upload.CanUpload() != wantSeed {
			t.Errorf("tier %q with seeding enabled: CanUpload()=%v, expected %v. "+
				"The operator's setting is an outer bound, never an override",
				tier, got.Upload.CanUpload(), wantSeed)
		}
	}
}

// TestTheOperatorSwitchIsTheOuterBound is the ordering, and it is observable:
// with the switch off, the reason names the switch rather than the tier.
//
// The reason matters because a log line reading "the object is unverified,
// which is an assertion by..." for a torrent whose operator turned seeding off
// sends the reader looking at consent tiers when the answer is a settings
// screen.
func TestTheOperatorSwitchIsTheOuterBound(t *testing.T) {
	for _, tier := range []string{
		tierUnverified, tierSelfPublished, tierThirdPartyPermitted, tierDenied,
	} {
		got := Decide(Input{Tier: tier, OperatorAllowedSeed: false})
		if !strings.Contains(got.Reason, "switched off") {
			t.Errorf("tier %q with seeding off: the reason is %q, which does not "+
				"mention the operator's setting. A reader would go looking at "+
				"consent tiers for an answer that is in a settings screen",
				tier, got.Reason)
		}
	}
}

// TestNoDecisionMeansNoUpload is the fail-closed default, and the reason it is
// a function rather than a zero value is the reason it needs a test.
//
// A zero `Policy` has `Upload` == "", and "" is not `UploadForbidden`. So
// `CanUpload()` on a zero Policy returns false — for the RIGHT ANSWER, by
// ACCIDENT. A future constant added to the set, or a refactor that makes
// UploadPolicy a struct, turns that accident into a real failure, and nothing
// else would notice until a torrent uploaded.
func TestNoDecisionMeansNoUpload(t *testing.T) {
	explicit := UploadForbiddenFor(tierSelfPublished)
	if explicit.Upload.CanUpload() {
		t.Error("the fail-closed default permits uploading. A torrent that " +
			"reaches the network without a decision is a programming error, " +
			"and this is the safe way for it to behave")
	}
	if explicit.Upload != UploadForbidden {
		t.Errorf("the fail-closed default is %q, not UploadForbidden. CanUpload "+
			"returns false for the right reason, but the value is wrong and a "+
			"caller comparing it directly would not see that", explicit.Upload)
	}
	if explicit.Reason == "" {
		t.Error("the fail-closed default has no reason. 'It did not seed' and " +
			"'nobody decided it should' are different findings")
	}

	// And the zero value, for the record: it happens to be safe, and the test
	// says so explicitly rather than leaving it to be rediscovered.
	var zero Policy
	if zero.Upload.CanUpload() {
		t.Error("a zero Policy permits uploading")
	}
}

// TestEveryDecisionCarriesATier is what makes an audit possible after the fact.
//
// Without it, a log line says "refused to seed" and a reader has to guess which
// state the object was in when the decision was made — which is not the state it
// is in now, and may not be any state that exists now.
func TestEveryDecisionCarriesATier(t *testing.T) {
	for _, tier := range []string{
		tierUnverified, tierSelfPublished, tierPerformerClaimed,
		tierThirdPartyPermitted, tierQuarantined, tierDenied, "", "weird",
	} {
		if got := Decide(Input{Tier: tier, OperatorAllowedSeed: true}); got.Tier != tier {
			t.Errorf("tier %q: the decision records %q. An audit that reads the "+
				"recorded tier back has to find the one the decision was made "+
				"at", tier, got.Tier)
		}
	}
}

// TestEveryRefusalCarriesAReason is the property a substring assertion gets
// wrong, and it exists because the mutation harness showed the hole.
//
// An earlier version of the mutation emptied the FIRST fragment of a
// concatenated reason string, and the test — which grepped for a phrase living
// in a LATER fragment — passed with the reason mangled to nonsense. Checking
// that a reason EXISTS is not checking that it SAYS anything: `Reason: ""` and
// `Reason: tierUnverified` are both non-empty, and both fail the operator who
// reads them.
//
// So this asserts the reasons are actual sentences, naming the tier and saying
// why, for every branch. The threshold is a length floor plus a requirement
// that the reason mention the tier it decided about — crude, but it catches
// every mutation in the harness, which is the point.
func TestEveryRefusalCarriesAReason(t *testing.T) {
	for _, in := range []Input{
		{Tier: tierUnverified, OperatorAllowedSeed: true},
		{Tier: tierQuarantined, OperatorAllowedSeed: true},
		{Tier: tierDenied, OperatorAllowedSeed: true},
		{Tier: "future_tier", OperatorAllowedSeed: true},
		{Tier: "", OperatorAllowedSeed: true},
		{Tier: tierSelfPublished, OperatorAllowedSeed: true},
		{Tier: tierPerformerClaimed, OperatorAllowedSeed: true},
		{Tier: tierThirdPartyPermitted, OperatorAllowedSeed: true},
		{Tier: tierUnverified, OperatorAllowedSeed: false},
	} {
		got := Decide(in)

		if got.Reason == "" {
			t.Errorf("tier %q (seeding on=%v): no reason at all. 'It did not "+
				"seed' and 'nobody decided it should' are different findings",
				in.Tier, in.OperatorAllowedSeed)
			continue
		}
		// A sentence, not a fragment: the longest real reason is ~250 chars and
		// the shortest is ~90, so 40 is a floor that no real reason fails and
		// that "ok", "enabled", "x" and a bare tier all do.
		if len(got.Reason) < 40 {
			t.Errorf("tier %q (seeding on=%v): the reason is %q, which is %d "+
				"chars. That is a label, not a reason, and whoever reads the log "+
				"later cannot act on it", in.Tier, in.OperatorAllowedSeed,
				got.Reason, len(got.Reason))
		}
		// Reads as prose rather than a code fragment.
		//
		// Three attempts at this, each too clever:
		//   - assert a trailing full stop -> every reason failed, because these
		//     are log-line prose ending on a clause, and adding a period to
		//     satisfy a test would be changing the words to suit the test;
		//   - assert the first letter is capitalised -> every reason failed,
		//     because the reasons are built by CONCATENATION, so every string
		//     after the first is a mid-sentence continuation;
		//   - assert a length floor alone -> passes, and the length is the only
		//     part of it doing any work.
		//
		// What separates "ok" and in.Tier from a real reason is ordinary English
		// words, so that is what is checked: an article in the prose, and no code
		// punctuation running through the string.
		if !strings.Contains(got.Reason, " the ") {
			t.Errorf("tier %q: the reason reads as %q, which does not look like a "+
				"sentence -- no article anywhere in it. Every real reason names "+
				"something; a label does not", in.Tier, got.Reason)
		}
		if strings.ContainsAny(got.Reason, "{}%+") {
			t.Errorf("tier %q: the reason contains code punctuation: %q. It is "+
				"meant to be read by a person deciding whether to act on it",
				in.Tier, got.Reason)
		}

		// A refusal names the tier it decided about, so a reader does not have
		// to go and look it up — EXCEPT when the operator's switch is the outer
		// bound, where the reason is about the switch and deliberately does not
		// attribute anything to the tier. That is the whole point of that
		// branch, and asserting the naming here would have forced the two
		// reasons to be the same string.
		if !in.OperatorAllowedSeed {
			continue
		}
		wantNamed := in.Tier
		if wantNamed == "" {
			wantNamed = "(empty)"
		}
		if !strings.Contains(got.Reason, wantNamed) {
			t.Errorf("tier %q: the reason does not name the tier: %q",
				in.Tier, got.Reason)
		}
	}
}

// TestTheTierStringsMatchTheCore is the guard on the duplication.
//
// These six strings are copied from internal/collab/locator.go, and this module
// cannot import it — the seam. An unrecognised string is NOT a compile error
// here, so a rename in core would leave this table quietly matching nothing, and
// every decision would fall to the restrictive branch. That is the safe
// direction, which is exactly what makes it dangerous: the downloader would stop
// seeding everywhere and nothing would fail.
//
// So the source is READ, and the strings compared. Core's source, not a copy of
// its list, because a copied list is exactly what goes stale.
func TestTheTierStringsMatchTheCore(t *testing.T) {
	body, err := os.ReadFile(coreLocatorSource)
	if err != nil {
		t.Fatalf("reading core's locator.go: %v\n\n"+
			"  These tier strings are duplicated across the module boundary and "+
			"  this is what checks the copy. A mismatch does not fail at compile "+
			"  time -- an unrecognised string just falls through to the "+
			"  restrictive branch, so the downloader silently stops seeding "+
			"  everywhere and no test fails", err)
	}

	// `TierX LocatorTier = "x"` in the constant block.
	constant := regexp.MustCompile(`Tier([A-Za-z0-9]+)\s+LocatorTier\s*=\s*"([a-z_]+)"`)
	matches := constant.FindAllStringSubmatch(string(body), -1)
	if len(matches) < 6 {
		t.Fatalf("found %d tier constants in core's locator.go, expected at "+
			"least 6. The regex no longer matches core's declaration form, so "+
			"this test is measuring nothing: %v", len(matches), matches)
	}

	core := make([]string, 0, len(matches))
	for _, m := range matches {
		core = append(core, m[2])
	}
	sort.Strings(core)

	// This file's own constants are read from ITS SOURCE, not written out by
	// hand, so a constant added to seeding.go is automatically compared.
	//
	// The first version listed the six by hand and asserted the count was 6.
	// Two mutations then SURVIVED: adding a seventh constant to seeding.go
	// passed, because the test's own list did not grow and the length
	// comparison was against a literal. The test was checking a copy of the
	// thing it meant to be checking - the same mistake as the tier duplication
	// it exists to catch, one level down.
	//
	// Hence both sides are read from source and the sets are compared. The
	// declaration forms differ (`TierX LocatorTier = "x"` in core,
	// `tierX = "x"` here), so two patterns.
	self, err := os.ReadFile("seeding.go")
	if err != nil {
		t.Fatalf("reading this package's own seeding.go: %v", err)
	}
	constantDecl := regexp.MustCompile(`(?m)^\s*tier([A-Za-z0-9]+)\s*=\s*"([a-z_]+)"`)
	oursRaw := constantDecl.FindAllStringSubmatch(string(self), -1)
	if len(oursRaw) < 6 {
		t.Fatalf("found %d tier constants in seeding.go, expected at least 6. "+
			"The regex no longer matches this file's own declaration form, so "+
			"this test is measuring nothing: %v", len(oursRaw), oursRaw)
	}
	ours := make([]string, 0, len(oursRaw))
	for _, m := range oursRaw {
		ours = append(ours, m[2])
	}
	sort.Strings(ours)

	if len(core) != len(ours) {
		t.Errorf("core declares %d tiers (%s) and this file lists %d (%s).\n\n"+
			"  A tier in core and not here has no policy, and the default for an "+
			"  unlisted tier is to REFUSE seeding -- so the downloader quietly "+
			"  stops uploading for that tier, forever, with no error anywhere",
			len(core), strings.Join(core, ", "), len(ours), strings.Join(ours, ", "))
		return
	}
	for i := range core {
		if core[i] != ours[i] {
			t.Errorf("tier %d: core has %q, this file has %q", i, core[i], ours[i])
		}
	}
}

// TestThePolicyTableHasNoGaps is the same idea from the other direction: every
// tier core knows about must be DECIDED here, not merely listed.
//
// Listing a tier and forgetting to give it a case would send it to `default`,
// which refuses. Safe, silent, and wrong — the same failure as a missing
// constant, which is why it gets its own test.
func TestThePolicyTableHasNoGaps(t *testing.T) {
	body, err := os.ReadFile(coreLocatorSource)
	if err != nil {
		t.Fatalf("reading core's locator.go: %v", err)
	}
	constant := regexp.MustCompile(`Tier([A-Za-z0-9]+)\s+LocatorTier\s*=\s*"([a-z_]+)"`)
	matches := constant.FindAllStringSubmatch(string(body), -1)

	for _, m := range matches {
		tier := m[2]

		policy := Decide(Input{Tier: tier, OperatorAllowedSeed: true})

		// A recognised tier gets a reason that does NOT say it was
		// unrecognised. That is the observable difference between "classified"
		// and "fell through", since both refuse.
		if strings.Contains(policy.Reason, "not one this build recognises") {
			t.Errorf("the tier %q (core's Tier%s) is a real tier and was treated "+
				"as unrecognised. The policy table has no case for it, so it "+
				"falls through to the restrictive default -- a downloader that "+
				"refuses to seed something the tier permits",
				tier, m[1])
		}
		if policy.Reason == "" {
			t.Errorf("the tier %q got a decision with no reason", tier)
		}
	}
}
