// Access levels, per spec §6a.11 — and the firewall of §6a.10.
//
// M7 step 7.2 (R025–R028, R062, R066).
//
// WHY THIS IS A SEPARATE FILE FROM reputation.go, AND WHY THE SEPARATION IS THE
// POINT. §6a.11 says it plainly: this is the one place the brief and the existing
// spec use the same word for two different things. The existing "trust tier" is
// VOTE WEIGHTING (§5.3). An access level is what a user may DO TO CONTENT. They
// are separate, neither is derived from the other, and §6a.10 draws a line
// between them that this package exists to hold:
//
//	reward never grants access.
//
// So AccessLevel is a type that Reputation cannot produce, LevelFor takes no
// reputation argument at all, and both facts are pinned by tests rather than by
// this comment. A reputation that could buy access would make the whole
// governance model a pricing mechanism, which is what non-negotiable #5 exists
// to prevent.

package collab

import (
	"errors"
	"fmt"
)

// AccessLevel is what a user may DO TO CONTENT. Not vote weight (see Role and
// §5.3), not standing, not reputation.
type AccessLevel int

// The six levels of §6a.11, in order. The values are the levels, so an operator
// policy threshold is a comparable number and no mapping table has to be
// consulted at the point of decision.
const (
	LevelPublic      AccessLevel = iota // 0 browse directory, metadata, collages, reviews
	LevelRegistered                     // 1 vote, submit edits, review, flag, ident board
	LevelContributor                    // 2 trusted edits auto-approve, expanded collages, XP
	LevelCurator                        // 3 merge duplicates, approve edits, manage tags, quests
	LevelArchivist                      // 4 opt-in content viewing, streaming, download, upload, host replicas
	LevelSteward                        // 5 governance, API keys, awards voting, gravity input
)

// LevelCount is how many levels exist. A policy threshold above it is clamped,
// not an error: an operator who types 9 into a settings box has made the content
// maximally available, which is a legitimate (if unwise) policy choice and not
// something to refuse at the point of use.
const LevelCount = 6

// MaxAccessLevel is the ceiling no policy can raise. There is no level 6.
const MaxAccessLevel = LevelSteward

func (l AccessLevel) String() string {
	switch l {
	case LevelPublic:
		return "Public"
	case LevelRegistered:
		return "Registered"
	case LevelContributor:
		return "Contributor"
	case LevelCurator:
		return "Curator"
	case LevelArchivist:
		return "Archivist"
	case LevelSteward:
		return "Steward"
	}
	return fmt.Sprintf("AccessLevel(%d)", int(l))
}

// Clamp folds a policy threshold into the legal range. Operator input, not a
// user-supplied level, so this is a safety net rather than validation.
func (l AccessLevel) Clamp() AccessLevel {
	if l < LevelPublic {
		return LevelPublic
	}
	if l > MaxAccessLevel {
		return MaxAccessLevel
	}
	return l
}

// Earned is what a user has actually achieved, as opposed to what an operator's
// policy is willing to offer.
//
// §6a.11: "a trust level is a CEILING on what may be offered, and consent is the
// separate switch that enables it". So there are two numbers and they are
// deliberately different types, because collapsing them into one is the bug this
// package is shaped to prevent.
type Earned struct {
	// Level is derived from the audit log (§4.2): approved edits, verification
	// consistency, ident solves, quests, preservation contributions. NEVER from
	// reputation, XP, or a stored mutable number.
	//
	// The zero value is LevelPublic, which is the safe direction: absent evidence
	// of contribution means no contribution.
	Level AccessLevel
}

// Errors the access decision returns. Sentinels, so a caller can distinguish
// "you have not earned this" from "you have not consented" -- which are
// different problems with different remedies, and §6a.11 requires both to be
// reachable.
var (
	// ErrNotEarned means the user's earned level is below what they asked for.
	ErrNotEarned = errors.New("access level not earned")

	// ErrConsentRequired means the level is earned AND offered but §6a.11's
	// separate consent switch is off. Reaching Archivist does not itself enable
	// content viewing.
	ErrConsentRequired = errors.New("per-instance consent required for content access")

	// ErrNotOffered means the level IS earned but this instance's operator does not
	// offer it. Distinct from ErrNotEarned because the two are fixable by opposite
	// parties: one by contributing, the other by an operator changing policy. A
	// caller that merged them would tell a Steward to go and earn more on an
	// instance that has simply decided not to serve content at all.
	ErrNotOffered = errors.New("access level not offered by this instance")
)

// DecideAccess is the one function that answers "may this user do X to content?".
//
// FOUR INPUTS, and the separation between them is the whole design:
//
//	earned    what the audit log says they have done
//	requested what the caller is asking for RIGHT NOW
//	ceiling   what this instance's operator is willing to offer at all
//	consented whether THIS user has opted in, per instance, revocably
//
// THREE SEPARATE QUESTIONS, and the order answers them so the message names the
// blocker the user can actually act on:
//
//  1. Have they EARNED `requested`?        -> ErrNotEarned, fixable by contributing
//  2. Will policy OFFER `requested`?       -> ErrNotOffered, fixable by nobody here
//  3. For a level-4 request, have they CONSENTED? -> ErrConsentRequired
//
// `requested` is separate from `ceiling` because a caller asking for level 2 on a
// level-5 instance is not asking for level 5. Collapsing the two is what made the
// first version of this function wrong: it compared earned against ceiling, so a
// Steward on a Registered-ceiling instance passed (5 >= 1) and the operator's
// ceiling stopped being a ceiling for precisely the users it exists to restrain.
//
// An operator's own access is NOT special-cased. §6a.12: "An operator's ceiling is
// a ceiling... it never grants the operator anything the threshold excludes." So
// there is deliberately no operator bypass parameter, and TestAnOperatorGetsNoBypass
// exists because a bypass argument is the easiest thing in the world to add later
// and call a debug affordance.
func DecideAccess(e Earned, requested, policyCeiling AccessLevel, consented bool) error {
	requested = requested.Clamp()
	ceiling := policyCeiling.Clamp()

	// 1. EARNED. Checked first: it is the blocker the user can fix themselves, by
	//    contributing, and saying so when both apply sends them somewhere useful.
	if e.Level < requested {
		return fmt.Errorf("%w: need level %d (%s), earned %d (%s)",
			ErrNotEarned, int(requested), requested, int(e.Level), e.Level)
	}

	// 2. OFFERED. The operator's ceiling is a CEILING (§6a.12), so it is checked
	//    against what was REQUESTED and not against what was earned. Comparing it
	//    to the earned level is the bug this replaced: a Steward on a
	//    Registered-ceiling instance passed (5 >= 1), so the ceiling did not
	//    restrain the highest-trust users, which are exactly the ones an operator
	//    lowering a threshold is trying to restrain.
	if ceiling < requested {
		return fmt.Errorf("%w: this instance offers at most level %d (%s); %d (%s) requested",
			ErrNotOffered, int(ceiling), ceiling, int(requested), requested)
	}

	// 3. CONSENTED, and only from level 4 up. Below it there is nothing to consent
	//    to: metadata and the directory are not the protected thing, and prompting
	//    for consent to browse would train users to click through the one prompt
	//    that matters.
	//
	//    Keyed on `requested`, not on the earned level, so a Curator on a
	//    permissive instance is not prompted about a consent that is not theirs.
	if requested >= LevelArchivist && !consented {
		return fmt.Errorf("%w: level %d (%s) permits content access, "+
			"which needs per-instance consent (§6a.11)",
			ErrConsentRequired, int(requested), requested)
	}

	return nil
}

// LevelFor derives a user's earned access level from their audit-log record.
//
// THE SIGNATURE IS THE FIREWALL. It takes approved edits, verification
// consistency, ident solves and preservation contributions, and it takes NO
// reputation, NO xp and NO score. There is no argument through which §6a.10 could
// be violated by a caller, because there is nowhere to pass one.
//
// The thresholds are a floor, not the feature: §6a.11 says levels are earned from
// the audit log and does not fix the numbers. These are deliberately conservative
// and are meant to be moved by an operator's policy rather than by editing code.
//
// The zero case matters and is tested: a user with no record earns
// LevelPublic, NOT LevelRegistered. "They signed up" is an event, not a
// contribution, and defaulting to Registered would mean every new account could
// vote and submit edits.
func LevelFor(approvedEdits, verificationConsistent, identSolves, preservationContribs int) Earned {
	negative := func(n int) int {
		if n < 0 {
			return 0
		}
		return n
	}
	approvedEdits = negative(approvedEdits)
	verificationConsistent = negative(verificationConsistent)
	identSolves = negative(identSolves)
	preservationContribs = negative(preservationContribs)

	// Verified consistency is the strongest single signal: it is the only one that
	// cannot be farmed by volume, since an inaccurate edit makes it worse.
	if verificationConsistent >= 10 && approvedEdits >= 10 {
		return Earned{Level: LevelSteward}
	}
	if identSolves >= 5 && approvedEdits >= 5 {
		return Earned{Level: LevelArchivist}
	}
	if identSolves >= 1 || approvedEdits >= 3 {
		return Earned{Level: LevelCurator}
	}
	if approvedEdits >= 1 {
		return Earned{Level: LevelContributor}
	}
	// Registered is earned by being REGISTERED, which is a fact about the account
	// rather than the audit log -- so it is the caller's job to pass it, and the
	// reason this function does not assume it. A no-record user earns Public.
	return Earned{Level: LevelPublic}
}
