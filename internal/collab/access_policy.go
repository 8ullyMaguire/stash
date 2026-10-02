// The persistence the access decision needs. M7 step 7.2.
//
// WHY THIS FILE EXISTS AT ALL, since access_level.go is complete and its tests
// pass.
//
// Because a complete domain model with no caller is not half-built work, it is
// invisible work. Nothing in a green suite distinguishes "the access model is
// wrong" from "the access model is unreachable", and R025-R028/R062/R066 sat at
// `specified` for a full milestone for exactly that reason: DecideAccess took
// three inputs and two of them had nowhere to live. The ceiling was an operator
// setting with no column, and consent was a user answer with no row.
//
// So this is the seam. It is declared in the domain rather than invented by the
// store, because the requirements are the domain's -- "the ceiling is the
// operator's", "consent is per instance and revocable" -- and a store that
// defined its own interface would make those requirements a suggestion.
//
// THE INTERFACE HAS NO OPERATOR FLAG, and that is the load-bearing omission.
// §6a.12: an operator's ceiling is a ceiling and never grants the operator
// anything the threshold excludes. If this interface had an
// `isOperator bool` or an `elevate` argument, §6a.12 would be one careless call
// site away from being false, and there is already
// TestAnOperatorGetsNoBypass in this package to say that argument is a known
// temptation. There is nowhere to pass one.

package collab

import "context"

// AccessPolicyStore is what DecideAccess needs read, and what an operator or a
// user needs written.
//
// Reads and writes are separated deliberately. A resolver calls Read* and never
// writes; the settings screen calls the writes. Combining them would put a
// method on the same interface that both answers "may this user see this" and
// changes the answer, and the caller that only wants to ask would have the
// capability to answer.
type AccessPolicyStore interface {
	// Ceiling is the operator's content threshold, §6a.12. Implementations must
	// return LevelPublic when no row exists rather than an error: an operator who
	// never configured a ceiling serves no content, and that is a policy, not a
	// failure. Returning an error would push every caller into a "treat missing
	// as open" fallback, which is the unsafe direction.
	Ceiling(ctx context.Context) (AccessLevel, error)

	// SetCeiling records the operator's threshold. Clamped to the legal range.
	SetCeiling(ctx context.Context, level AccessLevel) error

	// ConsentGranted is §6a.11's separate switch. Implementations must return
	// false when no row exists: consent is never automatic on reaching a level,
	// so the absent row and the row saying "off" are the same answer, and both
	// are the safe one.
	ConsentGranted(ctx context.Context, userID int) (bool, error)

	// GrantContentConsent records consent. Idempotent: granting twice is one
	// grant, not a row whose granted_at has moved for no reason a user can see.
	GrantContentConsent(ctx context.Context, userID int) error

	// RevokeContentConsent withdraws consent, RECORDING the revocation rather
	// than deleting the row. §6a.11 requires consent to be revocable, and a hard
	// delete satisfies that while making "when did this user withdraw"
	// unanswerable -- which is the question a governance review actually asks.
	RevokeContentConsent(ctx context.Context, userID int) error

	// EarnedFor derives the user's earned level from their audit-log record
	// (§4.2). Implementations MUST count audit rows and call LevelFor rather than
	// storing a level: a stored level is a mutable number, and §6a.11 says access
	// is earned from the log.
	//
	// It takes no reputation and no XP by construction -- the interface has
	// nowhere to pass one, which is the same firewall as LevelFor's signature and
	// is why both exist.
	EarnedFor(ctx context.Context, userID int) (Earned, error)
}

// Attestation is R066: a trust claim one instance makes about a user to another,
// carrying a claim rather than account state.
//
// A STRUCT AND NOT A LEVEL, because §6a.11's "attested claims, not shared
// account state" is the whole of R066: an instance may say "this user reached
// Curator here", and it may not say "this user IS at Curator, everywhere".
// `Level` is therefore the claim being attested and `Issuer` records who is
// making it, so a receiving instance can weigh a claim without inheriting the
// issuer's account.
type Attestation struct {
	// UserID is the user being described, in the RECEIVING instance's numbering.
	UserID int
	// Level is the claim. It is a claim about the issuer's user, not a grant in
	// the receiver, so a receiver that stores this must still run its own
	// DecideAccess with its own ceiling and the receiver's own consent state.
	Level AccessLevel
	// Issuer is the instance that made the claim, and IssuerUserID the user
	// there. Both are carried because "Foo says this user is a Curator" and
	// "this user is a Curator" are different statements and only the first is
	// something a peer is entitled to send.
	Issuer       string
	IssuerUserID int
}

// AttestationPolicy is the ceiling that applies to ATTESTED levels specifically.
//
// It exists because an attested claim is weaker evidence than a local audit log,
// and folding the two together would let a peer assert a level this instance's
// own users must earn. §6a.10's firewall is about reputation; this is the same
// firewall applied across the wire, where the evidence is a stranger's assertion.
type AttestationPolicy struct {
	// MaxAttested is the highest level an inbound attestation may confer. Zero
	// value means NO attested level is honoured, which is the same default
	// direction as the content ceiling and for the same reason.
	MaxAttested AccessLevel
}
