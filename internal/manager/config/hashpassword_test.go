package config

// stash#7135 / upstream #7255 -- hashPassword discarded the error from
// bcrypt.GenerateFromPassword, so a password over bcrypt's 72-byte limit
// produced an EMPTY hash that was then stored as a real credential.
//
// Measured on this tree by running bcrypt, not by reading its docs:
//
//	len=71  hashBytes=60  err=<nil>
//	len=72  hashBytes=60  err=<nil>
//	len=73  hashBytes=0   err=bcrypt: password length exceeds 72 bytes
//	len=100 hashBytes=0   err=bcrypt: password length exceeds 72 bytes
//
// So `hash, _ :=` on a 73-byte password yields "" silently. What makes that
// worse than a rejected input is the interaction with the two functions around
// it, and the tests below pin that interaction because it is the part nobody
// would find by reading the issue:
//
//	HasCredentials()      -> username != "" && pwHash != ""
//	ValidateCredentials() -> if !HasCredentials() { return TRUE }
//
// A user who sets a 73-byte password is stored as username + empty hash, so
// HasCredentials() is false, so ValidateCredentials() returns TRUE FOR ANY
// INPUT -- including a wrong username and a wrong password. That is not "the
// user disappears" (the reported symptom) but "authentication is silently
// disabled", and it is the difference between an availability bug and a
// security bug. The reported symptom is what a user notices; this is what is
// actually true.
//
// The bug is NOT the 72-byte limit. That is a documented property of bcrypt.
// The bug is DISCARDING THE ERROR, which converts a refused input into a
// silently corrupted credential.

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestAPasswordOverBcryptsLimitIsRefusedRatherThanStoredEmpty(t *testing.T) {
	tooLong := strings.Repeat("a", 73) // first length bcrypt rejects

	hash, err := hashPassword(tooLong)
	if err == nil {
		t.Fatalf("a 73-byte password was hashed without error and produced %d bytes; "+
			"bcrypt refuses it (measured: hashBytes=0), so an empty hash here IS the "+
			"#7135 bug -- the limit is not the bug, discarding the error is", len(hash))
	}
	if hash != "" {
		t.Errorf("a refused password still produced a %d-byte value; a caller that "+
			"ignores the error would store it", len(hash))
	}
	if !strings.Contains(err.Error(), "72") {
		t.Errorf("the error should name the 72-byte limit so the user can act on it, got %q", err)
	}
}

func TestAPasswordAtTheLimitIsStillAccepted(t *testing.T) {
	// The control for the other direction: a fix that refuses 72 bytes would
	// lock out every existing user whose password is exactly at the limit, and
	// nothing else in this file would notice.
	hash, err := hashPassword(strings.Repeat("a", 72))
	if err != nil {
		t.Fatalf("a 72-byte password must be accepted -- it is bcrypt's documented "+
			"limit, not past it: %v", err)
	}
	if len(hash) == 0 {
		t.Fatal("a 72-byte password produced an empty hash")
	}
}

func TestAnOrdinaryPasswordIsHashedAndVerifiable(t *testing.T) {
	// The control that the fix did not break the ordinary path, exercised
	// through the REAL verification function rather than a bespoke one.
	const pw = "correct horse battery staple"

	hash, err := hashPassword(pw)
	if err != nil {
		t.Fatalf("an ordinary password was refused: %v", err)
	}
	if hash == pw {
		t.Fatal("the password was stored in the clear")
	}

	i := InitializeEmpty()
	i.SetString(Username, "alice")
	i.SetString(Password, hash)

	if !i.ValidateCredentials("alice", pw) {
		t.Error("the correct credentials were rejected")
	}
	if i.ValidateCredentials("alice", "wrong password") {
		t.Error("a wrong password was ACCEPTED")
	}
}

func TestMultiBytePasswordsAreMeasuredInBytesNotRunes(t *testing.T) {
	// The limit is BYTES. 30 three-byte runes is 90 bytes and must be refused,
	// even though 30 characters looks comfortably inside "72 characters". A
	// check written as len([]rune(pw)) > 72 would accept this, and then bcrypt
	// would refuse it behind the caller's back -- the original bug in a new
	// shape.
	pw := strings.Repeat("日", 30) // 90 bytes, 30 runes
	if len([]rune(pw)) >= 72 {
		t.Fatalf("fixture is wrong: %d runes should be UNDER 72, or it proves nothing "+
			"about runes vs bytes", len([]rune(pw)))
	}
	if len(pw) <= 72 {
		t.Fatalf("fixture is wrong: %d bytes should be OVER 72", len(pw))
	}

	if _, err := hashPassword(pw); err == nil {
		t.Error("a 90-byte password was accepted; the limit must be measured in bytes, " +
			"because that is what bcrypt measures")
	}
}

// THE SECURITY CONSEQUENCE, AND THE PART THAT SURVIVED THE FIX.
//
// Verified live on this tree, with the empty hash that `hash, _ :=` stored:
//
//	ValidateCredentials("alice",  <73-byte pw>) = true
//	ValidateCredentials("alice",  "totally wrong") = true
//	ValidateCredentials("attacker","totally wrong") = true
//
// Fixing hashPassword stops NEW empty hashes. It does nothing about an
// existing one: a config file that already contains username + "" is
// indistinguishable, to every function involved, from a config that was never
// given a password. So the instance still authenticates anyone.
//
// This test is therefore NOT a test of the hash function -- it is a test of the
// residual hole, and it is why the fix is incomplete on its own. The decision
// recorded in docs/PR-TRIAGE.md for #7255 is that hashPassword must return the
// error AND the credential check must fail CLOSED rather than open.
func TestAnAlreadyCorruptCredentialDoesNotAuthenticateAnyone(t *testing.T) {
	i := InitializeEmpty()
	i.SetString(Username, "alice")
	// The state a pre-fix instance left on disk.
	i.SetString(Password, mustHashIgnoringError(strings.Repeat("a", 73)))

	if i.HasCredentials() {
		t.Fatal("fixture is wrong: HasCredentials() is true, so this is not the " +
			"corrupt state. The empty hash is what made it false.")
	}

	// A username IS configured. That is the whole difference from a fresh
	// instance, and it is what must not be treated as "no credentials".
	if i.ValidateCredentials("attacker", "totally wrong") {
		t.Error("FAIL-OPEN: a configured username with an unusable hash " +
			"authenticated a wrong username and a wrong password. The credential " +
			"check must fail closed once a username is set.")
	}
}

// The control for the fix's own edge. Failing closed on a corrupted hash is
// only safe because "no username at all" still returns true -- that is what
// lets a fresh install be claimed by its first user. A fix that returns false
// there locks every new instance out of its own setup, which is a worse outage
// than the bug and would be missed by every test above.
func TestAFreshInstanceIsStillClaimable(t *testing.T) {
	i := InitializeEmpty()

	if i.HasCredentials() {
		t.Fatal("fixture is wrong: a fresh instance reports credentials")
	}
	if i.getString(Username) != "" {
		t.Fatal("fixture is wrong: a fresh instance has a username")
	}

	if !i.ValidateCredentials("anyone", "anything") {
		t.Error("a FRESH instance refused authentication. Nobody can set up a new " +
			"install: the first user has no credentials yet, and refusing here " +
			"locks the instance out of its own setup.")
	}
}

// The control for the clearing path. Setting a password back to empty is a
// deliberate "remove authentication", and it must still work -- otherwise the
// fail-closed branch has made the credential unsettable.
func TestClearingThePasswordStillWorks(t *testing.T) {
	i := InitializeEmpty()
	i.SetString(Username, "alice")

	if err := i.SetPassword("a real password"); err != nil {
		t.Fatal(err)
	}
	if !i.HasCredentials() {
		t.Fatal("a real password did not register as a credential")
	}

	// Clear it.
	if err := i.SetPassword(""); err != nil {
		t.Fatalf("clearing the password must not be an error: %v", err)
	}
	if i.HasCredentials() {
		t.Error("the credential survived being cleared")
	}

	// ...and with the hash gone but the username still set, this is the
	// corrupted/cleared state, which now refuses. Recorded because it is the
	// same code path as the bug, and the two must not be confused.
	if i.ValidateCredentials("alice", "") {
		t.Error("FAIL-OPEN after clearing: a username with no hash authenticated")
	}
}

// mustHashIgnoringError is the OLD hashPassword, verbatim, so the test above
// documents the pre-fix behaviour instead of asserting a guess about it.
func mustHashIgnoringError(password string) string {
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	return string(hash)
}

// SetPassword's OWN refusal, and the layer the tests above do not reach.
//
// The tests in this file call hashPassword directly, so they are blind to
// whether SetPassword propagates the refusal -- which is the whole point of
// giving hashPassword a second return value. A mutation removing SetPassword's
// `if err != nil { return err }` still passes every test above, because
// hashPassword still returns the error; only SetPassword stops caring.
//
// Two properties, and the second is the one that is easy to miss:
//
//  1. It reports the refusal rather than succeeding silently.
//  2. It does NOT overwrite the working credential with an empty one. The
//     pre-fix code did exactly that, so a user who typed an overlong password
//     while one was already set lost the ability to log in AND kept a stored
//     username whose hash would no longer verify -- which, with the fail-closed
//     branch above, means the instance is locked out rather than merely
//     unauthenticated. Asserting only the returned error would miss that
//     entirely.
func TestSetPasswordRefusesAnOverlongPasswordWithoutDestroyingTheStoredCredential(t *testing.T) {
	i := InitializeEmpty()
	i.SetString(Username, "alice")

	const real = "the password that already worked"
	if err := i.SetPassword(real); err != nil {
		t.Fatalf("setup: setting an ordinary password failed: %v", err)
	}
	// A control on the fixture itself: prove the credential verifies BEFORE
	// the refusal, so a later failure is attributable to the refusal and not
	// to a fixture that never held a working credential.
	if !i.ValidateCredentials("alice", real) {
		t.Fatal("fixture is wrong: the stored credential does not verify before the " +
			"refusal, so the assertions below would pass for the wrong reason")
	}

	tooLong := strings.Repeat("a", 73)
	err := i.SetPassword(tooLong)
	if err == nil {
		t.Fatal("SetPassword reported success for a password bcrypt refuses; the " +
			"caller then believes the credential was changed when it was not")
	}
	if !strings.Contains(err.Error(), "72") {
		t.Errorf("the error should name the 72-byte limit, got %q", err)
	}

	if i.getString(Password) == "" {
		t.Error("the stored hash was emptied by a refused password; a pre-existing " +
			"working credential was destroyed by an input that was never accepted")
	}
	if !i.ValidateCredentials("alice", real) {
		t.Error("the pre-existing password no longer verifies after a refused SetPassword; " +
			"the refusal must leave the previous credential intact")
	}
}
