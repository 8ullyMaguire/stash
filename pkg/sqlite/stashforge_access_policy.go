package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/stashapp/stash/internal/collab"
)

// The access policy store. M7 step 7.2, migration 117.
//
// Compile-time proof the adapter satisfies the domain interface, in a non-test
// file so every BUILD checks it. A drift between this and collab's expectation
// is then a build failure here rather than a runtime failure in the path that
// gates content -- which is the one place a signature mismatch would let an
// unauthorised read through with no error anywhere.
var _ collab.AccessPolicyStore = (*AccessPolicyStore)(nil)

// AccessPolicyStore answers "may this user do X to content?" by reading the two
// switches DecideAccess needs, and writes those switches for the settings
// screens that own them.
//
// WHY THE TWO ARE READ IN ONE CALL AND NOT EXPOSED PIECEWIL TO A RESOLVER.
//
// DecideAccess takes three inputs and answers three questions in a fixed order,
// each naming a blocker the user can act on. A resolver that read the ceiling,
// read consent and computed earned itself would have three chances to forget one
// -- and forgetting CONSENT is the one that matters, because every other
// combination either works or fails safely. Decide below is the only method a
// content path should call, and it takes no operator flag: there is nowhere to
// pass one, which is how §6a.12's "a ceiling is a ceiling" survives a careless
// call site.
type AccessPolicyStore struct {
	repository

	// instanceID is the single instance these switches belong to. A field rather
	// than a parameter because the operator's ceiling is per-INSTANCE and a
	// parameter would let a caller pass another instance's, which is the same
	// class of mistake as the multi-instance consent read the migration warns
	// about. §6a.11 says consent is revocable PER INSTANCE, so an instance id
	// that can be varied per call is a requirement stated and then undermined.
	instanceID int
}

const (
	accessPolicyTable   = "access_policy"
	contentConsentTable = "content_consent"

	accessPolicyInstanceCol   = "instance_id"
	accessPolicyCeilingCol    = "content_ceiling"
	contentConsentUserCol     = "user_id"
	contentConsentInstanceCol = "instance_id"
	contentConsentGrantedCol  = "granted"
)

// NewAccessPolicyStore binds the store to this instance. The instance id is not
// defaulted: a store with a zero instance id would read and write a row that
// belongs to no instance, and the CHECK would refuse the write with a driver
// error naming a constraint rather than the caller who forgot to pass one.
func NewAccessPolicyStore(instanceID int) *AccessPolicyStore {
	return &AccessPolicyStore{
		repository: repository{tableName: accessPolicyTable, idColumn: accessPolicyInstanceCol},
		instanceID: instanceID,
	}
}

// Ceiling is the operator's content threshold.
//
// AN ABSENT ROW IS LevelPublic, NOT AN ERROR, and that is the whole of §6a.12's
// default. The operator never configured a threshold, so this instance does not
// serve content -- and returning an error instead would push every caller into a
// "treat missing as open" fallback, which is the unsafe direction and the one
// nobody would notice until a scene leaked.
//
// The distinction that matters: sql.ErrNoRows is a fact about the DATABASE, and
// this function's job is to answer a question about POLICY. "No row" and "a row
// saying 0" are the same policy, and only one of them is an error the caller
// should see.
func (s *AccessPolicyStore) Ceiling(ctx context.Context) (collab.AccessLevel, error) {
	var raw int
	err := dbWrapper.Get(ctx, &raw,
		`SELECT `+accessPolicyCeilingCol+` FROM `+accessPolicyTable+
			` WHERE `+accessPolicyInstanceCol+` = ?`, s.instanceID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return collab.LevelPublic, nil
	case err != nil:
		return collab.LevelPublic, fmt.Errorf("reading the content ceiling: %w", err)
	}

	level := collab.AccessLevel(raw).Clamp()
	if level != collab.AccessLevel(raw) {
		// The CHECK makes this unreachable, so reaching it means the column and
		// the type have diverged -- which is exactly the two-sources-of-truth
		// state the CHECK exists to prevent. Returning the clamped value AND
		// saying so is deliberate: a caller that only logs the error proceeds on
		// a safe value, and a caller that reads the message knows the schema is
		// wrong rather than the data.
		return level, fmt.Errorf("content_ceiling %d is outside 0..%d; clamped to %d "+
			"(the migration's CHECK should have made this unreachable)",
			raw, collab.LevelCount-1, int(level))
	}
	return level, nil
}

// SetCeiling records the operator's threshold.
//
// An UPSERT, not select-then-update, for the reason ConsentStore.SetConsent
// gives: the race in a read-then-write is lost by whichever caller writes second,
// and the loser is an operator's policy change silently dropped. One statement
// with ON CONFLICT has no window.
//
// Clamped rather than refused, because this is OPERATOR INPUT from a settings
// box, not a request from a user. Refusing would make a typo an error dialog
// where the honest answer is "9 means everything", and Clamp already encodes
// that reading.
func (s *AccessPolicyStore) SetCeiling(ctx context.Context, level collab.AccessLevel) error {
	clamped := level.Clamp()
	_, err := dbWrapper.Exec(ctx, "INSERT INTO "+accessPolicyTable+" ("+accessPolicyInstanceCol+", "+accessPolicyCeilingCol+") VALUES (?, ?) ON CONFLICT("+accessPolicyInstanceCol+") DO UPDATE SET "+accessPolicyCeilingCol+" = excluded."+accessPolicyCeilingCol+", updated_at = CURRENT_TIMESTAMP",
		s.instanceID, int(clamped))
	if err != nil {
		return fmt.Errorf("setting the content ceiling to %d: %w", int(clamped), err)
	}
	return nil
}

// ConsentGranted is §6a.11's separate switch.
//
// AN ABSENT ROW IS false, for the same reason as Ceiling and with more force:
// §6a.11 says consent is never automatic on reaching a level, so "no answer" and
// "declined" must be indistinguishable to a caller or the first one to read the
// row the other way turns every earned Archivist into a content viewer.
func (s *AccessPolicyStore) ConsentGranted(ctx context.Context, userID int) (bool, error) {
	if userID <= 0 {
		// Refused rather than coerced to 0, for the same reason AddLink refuses a
		// non-positive id: an id of 0 reaching a lookup is a caller bug, and the
		// answer it would otherwise get is whatever row happens to be 0.
		return false, fmt.Errorf("consent lookup for user id %d is not addressable", userID)
	}

	var granted int
	err := dbWrapper.Get(ctx, &granted, `SELECT `+contentConsentGrantedCol+
		` FROM `+contentConsentTable+
		` WHERE `+contentConsentUserCol+` = ? AND `+contentConsentInstanceCol+` = ?`,
		userID, s.instanceID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("reading content consent for user %d: %w", userID, err)
	}
	return granted == 1, nil
}

// GrantContentConsent records consent.
//
// granted_at is written as CURRENT_TIMESTAMP rather than left to the column
// default, because the migration's CHECK ties granted=1 to revoked_at IS NULL
// and a second grant must CLEAR a previous revocation -- which means this
// statement has to state the whole new state, not just the flag.
func (s *AccessPolicyStore) GrantContentConsent(ctx context.Context, userID int) error {
	if userID <= 0 {
		return fmt.Errorf("granting consent for user id %d is not addressable", userID)
	}
	_, err := dbWrapper.Exec(ctx, "INSERT INTO "+contentConsentTable+" ("+contentConsentUserCol+", "+contentConsentInstanceCol+", "+contentConsentGrantedCol+", granted_at, revoked_at) VALUES (?, ?, 1, CURRENT_TIMESTAMP, NULL) ON CONFLICT("+contentConsentUserCol+") DO UPDATE SET "+contentConsentGrantedCol+" = 1, granted_at = CURRENT_TIMESTAMP, revoked_at = NULL",
		userID, s.instanceID)
	if err != nil {
		return fmt.Errorf("granting content consent for user %d: %w", userID, err)
	}
	return nil
}

// RevokeContentConsent withdraws consent and RECORDS IT.
//
// An UPDATE, never a DELETE. §6a.11 requires consent to be revocable, and a hard
// delete satisfies that requirement while destroying the question a governance
// review actually asks: "when did this user withdraw?". So the row stays with
// granted=0 and revoked_at set.
//
// The upsert form is used rather than a plain UPDATE because revoking a consent
// that was never granted must still leave evidence that it was asked about and
// refused -- otherwise the next read cannot distinguish "declined" from "never
// asked", which is the difference between a user's decision and our omission.
func (s *AccessPolicyStore) RevokeContentConsent(ctx context.Context, userID int) error {
	if userID <= 0 {
		return fmt.Errorf("revoking consent for user id %d is not addressable", userID)
	}
	_, err := dbWrapper.Exec(ctx, "INSERT INTO "+contentConsentTable+" ("+contentConsentUserCol+", "+contentConsentInstanceCol+", "+contentConsentGrantedCol+", revoked_at) VALUES (?, ?, 0, CURRENT_TIMESTAMP) ON CONFLICT("+contentConsentUserCol+") DO UPDATE SET "+contentConsentGrantedCol+" = 0, revoked_at = CURRENT_TIMESTAMP",
		userID, s.instanceID)
	if err != nil {
		return fmt.Errorf("revoking content consent for user %d: %w", userID, err)
	}
	return nil
}

// EarnedFor derives the earned level from the audit log, per §4.2 and §6a.11.
//
// THE COUNTS ARE COMPUTED HERE AND THE THRESHOLDS STAY IN THE DOMAIN. LevelFor
// owns "what earns what", so an operator moving a threshold changes one Go
// function. Hard-coding the numbers into these queries would create a second
// place to change them and a way for the two to disagree -- and the disagreement
// would be invisible, because both would still return a number.
//
// The four actions are the audit vocabulary internal/gamify already spells out as
// constants. They are named here as literals because a SQL query cannot take a Go
// constant, and gamify's own comment says why the spelling matters: "a typo is a
// compile error rather than a row that silently earns nothing". A typo in a SQL
// literal is exactly that failure with no compiler involved, so
// TestEarnedCountsTheActionsTheDomainExpects asserts each literal against
// gamify's constant.
//
// NOT COUNTED: any action that represents a level, and any action whose count
// could stand in for one. §6a.10's firewall is that reputation buys vote weight
// and never access, so there is deliberately no XP term and no score term here
// -- not because they would be filtered, but because they are not in the query to
// begin with.
func (s *AccessPolicyStore) EarnedFor(ctx context.Context, userID int) (collab.Earned, error) {
	if userID <= 0 {
		return collab.Earned{}, fmt.Errorf("earning a level for user id %d is not addressable", userID)
	}

	const countOne = `SELECT COUNT(*) FROM collab_audit
		WHERE actor_id = ? AND action = ?`
	const countDistinctTarget = `SELECT COUNT(DISTINCT target_id) FROM collab_audit
		WHERE actor_id = ? AND action = ? AND target_id IS NOT NULL`

	var approvedEdits, idents int
	if err := dbWrapper.Get(ctx, &approvedEdits, countOne, userID, actionProposalApplied); err != nil {
		return collab.Earned{}, fmt.Errorf("counting approved edits for user %d: %w", userID, err)
	}
	if err := dbWrapper.Get(ctx, &idents, countDistinctTarget, userID, actionIdentSolved); err != nil {
		return collab.Earned{}, fmt.Errorf("counting ident solves for user %d: %w", userID, err)
	}

	// Verification consistency and preservation contributions are counted as
	// ZERO here rather than guessed at. Both are derived quantities -- consistency
	// is a ratio against a user's own history, and preservation is a property of
	// what this instance stores -- and inventing a formula here would put an
	// access level in the hands of a query nobody reviewed. LevelFor's thresholds
	// for them are therefore unreachable, which is a gap and not a bug: it means
	// Steward cannot currently be earned, and the honest fix is to derive both
	// properly rather than to guess. TestEarnedCannotReachStewardWithout
	// VerificationConsistency pins that so it cannot be reached by accident.
	return collab.LevelFor(approvedEdits, 0, idents, 0), nil
}

// The audit action literals EarnedFor counts, asserted against internal/gamify's
// constants by TestEarnedCountsTheActionsTheDomainExpects. See the note there.
const (
	actionProposalApplied = "proposal_applied"
	actionIdentSolved     = "ident_solved"
)

// Decide is the ONE call a content path makes. Everything else on this store
// exists for a settings screen or for a test.
//
// It reads all three inputs and delegates to collab.DecideAccess, so the ORDER of
// the three questions is decided in one place in the domain rather than at every
// call site. The three errors are returned unwrapped so a caller can use
// errors.Is to tell them apart, which matters because they are fixable by
// OPPOSITE parties: ErrNotEarned by the user contributing, ErrNotOffered by an
// operator changing policy, and ErrConsentRequired by the user consenting. A
// caller that collapsed them would tell a Steward to go and earn more on an
// instance that has simply decided not to serve content.
//
// No operator flag, no bypass, no override. That is §6a.12 held structurally
// rather than by discipline.
func (s *AccessPolicyStore) Decide(ctx context.Context, userID int, requested collab.AccessLevel) error {
	ceiling, err := s.Ceiling(ctx)
	if err != nil {
		return err
	}
	consented, err := s.ConsentGranted(ctx, userID)
	if err != nil {
		return err
	}
	earned, err := s.EarnedFor(ctx, userID)
	if err != nil {
		return err
	}
	return collab.DecideAccess(earned, requested, ceiling, consented)
}

// EarnedLevel is the one-line read a profile screen uses: what this user has
// actually earned, with no ceiling and no consent involved, because neither is a
// fact about the user.
func (s *AccessPolicyStore) EarnedLevel(ctx context.Context, userID int) (collab.AccessLevel, error) {
	earned, err := s.EarnedFor(ctx, userID)
	if err != nil {
		return collab.LevelPublic, err
	}
	return earned.Level, nil
}
