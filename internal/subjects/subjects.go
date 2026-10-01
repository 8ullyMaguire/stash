// Package subjects judges whether a scene may be replicated, from the consent
// state that governs it.
//
// M8 step 8.3 (R082, §6b.4, non-negotiable #7 extended to three paths). This is the
// THIRD publish path's gate. The other two already exist and already refuse:
//
//	exporter        internal/api/stashforge_write_path_test.go  (the path guard)
//	acquisition     plugins/.../internal/policy/seeding.go        (seeding policy)
//
// and replication had only half a gate — `preservation.IsReplicationSubject` reads
// a subject's `MetadataShare` choice, which is the §6.2 opt-out, but the `denied`
// TIER is a different thing with a different rule.
//
// # WHY `denied` IS NOT A SHARE CHOICE
//
// This is the distinction the package exists to hold, and getting it wrong in
// either direction is a serious failure:
//
//   - A SHARE CHOICE is a preference about METADATA. §6.2's tiers are opt-out
//     through opt-in, and an unrecognised value is treated as opted-out because "a
//     schema bug stops publishing" is the safer of two failures.
//   - `denied` is TERMINAL and per-OBJECT. §7.1: destroying a denied object
//     DESTROYS its locators rather than orphaning them, and a denied locator is
//     refused for storage with no tier above it.
//
// So `denied` is not "the strictest opt-out". An operator who has opted IN to
// sharing metadata has not thereby permitted replication of something DENIED —
// because the denial is not about sharing, it is about the object. And a
// preservation bounty never overrides it: §6b.4 says so in as many words, and the
// reasoning is that a bounty offers storage for content somebody refused to have
// stored, which is a way of distributing the refusal.
//
// # WHY ONE GATE FOR ALL THREE PATHS
//
// Not for tidiness. The three paths were gated separately and are easy to gate
// INCONSISTENTLY: three implementations of "may this leave" is three chances for
// one to be permissive. `Subject` and `MayPublish` are the one answer all three
// call, so a fix lands everywhere.
//
// The exporter and the seeding policy are NOT rewritten here. They answer
// different questions at different times — §7.1's "may this locator be acted on at
// all" versus "may these bytes leave afterwards" — and both derive from the same
// tier so they cannot disagree. This package is the third question's gate, and
// adding a call to it from a scheduler is the whole of the wiring.

package subjects

import (
	"errors"
	"fmt"
	"strings"

	"github.com/stashapp/stash/internal/collab"
)

// Subject is a scene's replication eligibility, with the consent state that decides
// it.
//
// DELIBERATELY SMALL AND DELIBERATELY COMPLETE. A field is here because a decision
// depends on it; a field that is absent is a decision that cannot be taken wrongly,
// and `preservation.Subject` does not carry the denied state at all — which is the
// gap this package fills.
type Subject struct {
	// SceneID is the canonical id from the commons. Empty means there is no subject,
	// and no subject is refused: an unidentified object cannot be checked against a
	// denial.
	SceneID string

	// MetadataShare is §6.2's opt-out choice. It governs whether this scene may be
	// published at all.
	MetadataShare collab.ShareChoice

	// LocatorTier is the consent tier of the locators pointing at this scene.
	// §7.1's `denied` is terminal and outranks every choice in the field above.
	LocatorTier collab.LocatorTier

	// AcceptsReplicas is whether THIS instance is willing to hold replicas at all
	// (§6a.9's second consent question).
	//
	// IT IS ON THE SUBJECT rather than passed separately to MayAccept, so the
	// decision is total: there is no argument list from which a caller can reach a
	// "permitted to hold" answer without having stated the local instance's
	// willingness. MayPublish ignores it entirely, because whether this instance
	// will store a replica says nothing about whether it may send one.
	//
	// The ZERO VALUE IS FALSE, so a subject built without it refuses to accept. That
	// is the safe direction: an instance that never configured itself willing to
	// hold other people's content should not start holding it because a struct
	// literal omitted a bool.
	AcceptsReplicas bool
}

var (
	// ErrDenied means the object is denied, and no choice overrides it.
	ErrDenied = errors.New("subjects: object is denied and is never a replication subject")

	// ErrNotOptedIn means the subject has not opted in to sharing.
	ErrNotOptedIn = errors.New("subjects: subject has not opted in to sharing metadata")

	// ErrNoSubject means there is no subject to judge.
	ErrNoSubject = errors.New("subjects: no subject")

	// ErrUnknownTier means the locator tier is not one this build knows: a peer on a
	// newer core, a hand-edited row, a truncated database.
	//
	// IT IS DISTINCT FROM ErrNotOptedIn because the remedies are unrelated. An
	// opt-out is fixed by changing a setting; an unknown tier is fixed by a version
	// mismatch, and the operator has to be told which they are looking at before
	// they can start looking.
	ErrUnknownTier = errors.New("subjects: locator tier is not one this build knows")
)

// Decision is the verdict, with the reason.
//
// A STRUCT RATHER THAN A BOOL because the three refusals need different responses
// and an operator (or a caller retrying) has to tell them apart. A bool forces the
// caller to re-derive the cause, and the temptation is to re-derive it permissively.
type Decision struct {
	// Permitted is the verdict.
	Permitted bool

	// Reason explains a refusal in words an operator can act on, and is empty when
	// permitted.
	Reason string

	// Tier is the tier that was checked, so a caller can log what it decided on
	// rather than only what it decided.
	Tier collab.LocatorTier
}

// Error returns the sentinel a caller should branch on, or nil when permitted.
//
// THE ERROR IS DERIVED FROM THE REASON BY CONSTRUCTION rather than stored
// separately, so the two can never disagree — a Decision saying "denied" with a nil
// error is the shape that lets a caller treat a denial as a transient fault and
// retry it.
func (d Decision) Error() error {
	switch {
	case d.Permitted:
		return nil
	case strings.HasPrefix(d.Reason, reasonDenied):
		return ErrDenied
	case strings.HasPrefix(d.Reason, reasonRefused):
		// Both an unrecognised share choice and an unrecognised tier begin here.
		// They are different problems with unrelated fixes — one is a settings
		// value, the other a version mismatch — so they get separate sentinels.
		if strings.Contains(d.Reason, "tier") {
			return ErrUnknownTier
		}
		return ErrNotOptedIn
	case strings.HasPrefix(d.Reason, reasonNoSubject):
		return ErrNoSubject
	default:
		// UNREACHABLE FROM INSIDE THIS PACKAGE, and that is the point.
		//
		// `Decision` is EXPORTED with exported fields, so a caller outside this
		// package can construct one -- and its `Reason` will not carry one of the
		// prefixes above. This branch is the only thing standing between that and a
		// `nil` error, which a caller would read as "permitted".
		//
		// A mutation run flagged it: replacing this `return ErrDenied` with `return
		// nil` killed nothing, because every reason in this file carries a prefix and
		// so no in-package test ever reaches here. That is a MISSING TEST rather than
		// a dead line -- the branch is reachable through the exported API -- so the
		// test below reaches it the only way it can be reached.
		//
		// Failing CLOSED is the direction that matters. An unclassifiable refusal must
		// never read as "nothing to see".
		return ErrDenied
	}
}

// Reason prefixes, and the classifier keys on these rather than on a word appearing
// anywhere in the text.
//
// AN EARLIER VERSION MATCHED SUBSTRINGS and it was wrong twice over: a reason about
// an unrecognised TIER that mentioned denial classified as a denial, and one that
// mentioned "not opted in" classified as an opt-out. Both sent the caller after the
// wrong cause, which is worse than not classifying at all because it looks decided.
//
// Each reason in this file therefore STARTS with the prefix that names its class, and
// the classifier reads only the prefix.
const (
	reasonDenied    = "denied:"
	reasonRefused   = "refused:"
	reasonNoSubject = "no subject:"
)

// MayPublish reports whether this subject's content may be replicated.
//
// # THE ORDER IS THE REQUIREMENT
//
//  1. no subject            -> refused (nothing to check a denial against)
//  2. denied tier           -> refused (terminal, outranks everything)
//  3. not opted in          -> refused (§6.2's opt-out)
//  4. permitted
//
// `denied` is checked BEFORE the share choice, and the order is load-bearing in both
// directions. Checked second, an operator who opted in to sharing would replicate
// denied content — the failure #7 is a hard stop against. Checked first, a denial is
// refused regardless of how permissive the share setting is, which is what "terminal"
// means. A single `if optedIn && !denied` would get this wrong for any subject where
// both are true, and that is precisely the subject that matters.
//
// # WHAT IS DELIBERATELY NOT HERE
//
// No override, no flag, no "force" parameter, and no bounty. §6b.4: "a preservation
// bounty never overrides" a denial. A function with an override parameter invites
// the one call site that uses it, and the reason such a call would be written is
// always "this scene is about to be lost" — which is exactly the moment a refusal
// should hold firmest. There is no path here that permits a denied subject.
func MayPublish(s Subject) Decision {
	if strings.TrimSpace(s.SceneID) == "" {
		return Decision{
			Permitted: false,
			Reason:    "no subject: an unidentified object cannot be checked against a denial",
			Tier:      s.LocatorTier,
		}
	}

	// CANONICALISE THE TIER ONCE, BEFORE EITHER CHECK, and use the canonical value
	// for both. This is a fix for a real bypass a test found, not a tidiness change.
	//
	// `collab.IsStorageRefused` is a map lookup on the RAW value, so it misses on
	// "denied ". `collab.ParseLocatorTier` TRIMS before validating, so it returns
	// (denied, nil) — the value is KNOWN. Checking the refusal table first and the
	// known-set second therefore read a trailing space as "not in the refusal table,
	// but a tier we know", and PERMITTED a denied object.
	//
	// Neither collab function is wrong alone: for a typed constant neither situation
	// arises, which is what `collab` says when it documents the permissive direction.
	// Both become reachable HERE, where the tier arrives as a string from a peer. A
	// non-trimming lookup followed by a trimming validator is the hole; one
	// canonicalisation closes it and keeps it closed.
	//
	// `canonical` keeps the trimmed value so the Decision reports what was actually
	// judged rather than the raw input.
	canonical, known := canonicalTier(s.LocatorTier)

	// §7.1's storage refusals, consulted through the table rather than by
	// comparing against TierDenied.
	//
	// THE TABLE HAS TWO ENTRIES, NOT ONE, and my first version compared against
	// TierDenied alone and so missed `quarantined`. Both are refused, for different
	// reasons that `internal/collab` states: a quarantined object "holds material
	// but does not act on it", so replicating it would act on it; and a denial is
	// terminal, so storing anything against it "would leave a pointer to material
	// that must not exist". Reusing the table means a tier added to it is enforced
	// here without this file being edited.
	if refused, reason := canonical.IsStorageRefused(); refused {
		return Decision{
			Permitted: false,
			Reason: fmt.Sprintf("%s %s. §7.1 makes this a hard stop, and §6b.4 states "+
				"that a preservation bounty never overrides it", reasonDenied, reason),
			Tier: canonical,
		}
	}

	// AN UNRECOGNISED TIER FAILS CLOSED, and this check is not redundant with the
	// table above.
	//
	// `IsStorageRefused` returns FALSE for a tier not in its map, and `collab` says
	// that is deliberate: "the map is consulted rather than matched, so the answer
	// is false for a tier not in the map. That is the permissive direction."
	//
	// That is correct for LOCATOR STORAGE, where the tier is a typed constant and an
	// unknown value cannot arise from a string. It is not correct HERE, where the
	// Subject crosses a wire and its tier came from a peer, a newer core, a
	// hand-edited row or a truncated database. A gate that inherits a permissive
	// default at the one place the input is untrusted is a gate that fails open on
	// exactly the input an attacker would choose.
	//
	// So the known set is checked explicitly, via the same table `ParseLocatorTier`
	// uses so the two cannot disagree about what "known" means.
	if !known {
		return Decision{
			Permitted: false,
			Reason: fmt.Sprintf("%s locator tier %q is not one of §7.1's tiers (%s). "+
				"Each tier is an assertion by an identified party, and an assertion "+
				"nobody can be identified for is not permission to publish — so an "+
				"unrecognised value fails closed rather than silently acquiring the "+
				"most permissive behaviour in the table",
				reasonRefused, string(s.LocatorTier),
				strings.Join(collab.LocatorTierNames(), ", ")),
			Tier: canonical,
		}
	}

	// §6.2's opt-out, with an unrecognised value failing closed. The reasoning is
	// `preservation.IsReplicationSubject`'s and is the same: a value this build has
	// not been taught must not start publishing.
	if s.MetadataShare != collab.ChoiceOptedIn {
		return Decision{
			Permitted: false,
			Reason: fmt.Sprintf("%s metadata share is %q. §6.2 treats an unrecognised "+
				"value as opted-out, because a schema bug that stops publishing is the "+
				"safer of the two failures",
				reasonRefused, string(s.MetadataShare)),
			Tier: s.LocatorTier,
		}
	}

	return Decision{Permitted: true, Tier: canonical}
}

// canonicalTier trims a tier and reports whether this build knows it.
//
// IT DELIBERATELY DOES NOT CALL `collab.ParseLocatorTier`, even though that function
// trims and validates: it returns the EMPTY tier on failure, which is a lossy
// answer at the one place where "I could not parse this" and "this is unverified"
// must not be confused. Returning the trimmed value AND a separate flag keeps the
// two facts apart, and the caller decides what each one means.
//
// The known set comes from `ParseLocatorTier` so the two cannot disagree about what
// "known" means. It is asked about the ALREADY-TRIMMED value, which is what makes
// "denied " resolve to `denied` — the refusal table then catches it, and the
// fail-closed check never has to be the thing that saves us.
func canonicalTier(t collab.LocatorTier) (collab.LocatorTier, bool) {
	trimmed := collab.LocatorTier(strings.TrimSpace(string(t)))
	_, err := collab.ParseLocatorTier(string(trimmed))
	return trimmed, err == nil
}

// MayAccept reports whether this instance may HOLD a replica of a subject it did
// not create.
//
// THE RECEIVING QUESTION, and §6b.5 states it: "where a replica is observable by a
// user of the receiving instance it is subject to THAT instance's own consent
// state; an instance that must not hold a replica refuses it, and refusing is not a
// partial success."
//
// SO IT IS A DIFFERENT FUNCTION rather than MayPublish with the subject rewritten,
// because the two answer different questions about the same object: MayPublish asks
// whether THIS instance may send it, MayAccept whether this instance may keep it.
// An instance that publishes freely and refuses to hold is unusual but legal, and a
// single function with a flag would make that configuration inexpressible.
func MayAccept(s Subject) Decision {
	d := MayPublish(s)
	if !d.Permitted {
		return d
	}
	// Acceptance additionally requires the receiving instance to be willing, which is
	// a property of this instance rather than of the subject. It is carried on the
	// subject so the decision is total: a caller cannot reach a "permitted to hold"
	// answer without having stated whether this instance accepts replicas.
	if !s.AcceptsReplicas {
		return Decision{
			Permitted: false,
			Reason: reasonRefused + " this instance does not accept replicas " +
				"(§6a.9). Refusing is not a partial success",
			Tier: s.LocatorTier,
		}
	}
	return d
}
