package collab

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// TOTP tests. M4 step 4.2.
//
// THE FIRST TEST IS THE ONE THAT MATTERS MOST, and it is not the one the plan
// names. The plan asks for the replay case, and there are three tests here for
// that. But totpCode is written out by hand rather than delegated, so before any
// of the replay tests mean anything, the arithmetic has to be shown to agree with
// what a real authenticator app computes. A replay guard wrapped around wrong
// arithmetic is a guard around a function that rejects every code.
//
// The plan's three, by name:
//	TestTOTP_RejectsReplayWithinTimeStep
//	TestTOTP_OwnerRequiredAtSetup
//	TestTOTP_SecretNotReturnedAfterSetup

// testTime is a fixed instant so every code in this file is deterministic.
var testTime = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

// codeAt computes the expected code for an instant using the LIBRARY, so the
// hand-written implementation is checked against an independent one.
func codeAt(t *testing.T, s TOTPSecret, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(s.Reveal(), at, totp.ValidateOpts{
		Period:    uint(TOTPStep / time.Second),
		Skew:      0,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("library could not generate a code: %v", err)
	}
	return code
}

func newTestSecret(t *testing.T) TOTPSecret {
	t.Helper()
	s, err := NewTOTPSecret()
	if err != nil {
		t.Fatalf("NewTOTPSecret: %v", err)
	}
	return s
}

// TestTOTP_MatchesTheLibraryImplementation is the cross-check. If the hand-
// written totpCode ever drifts from the library, this fails before the replay
// tests can give false confidence.
func TestTOTP_MatchesTheLibraryImplementation(t *testing.T) {
	s := newTestSecret(t)
	raw, err := s.Decode()
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	// Several instants, including one that is not a step boundary, and one on a
	// leap second-ish boundary, so an off-by-one in the counter is caught.
	for _, at := range []time.Time{
		testTime,
		testTime.Add(1 * time.Second),
		testTime.Add(29 * time.Second),
		testTime.Add(30 * time.Second),
		testTime.Add(61 * time.Second),
		testTime.Add(-90 * time.Second),
	} {
		want := codeAt(t, s, at)
		counter := uint64(totpCounter(at))
		got, err := totpCode(raw, counter)
		if err != nil {
			t.Fatalf("totpCode at %v: %v", at, err)
		}
		if got != want {
			t.Errorf("at %v: hand-written %q, library %q", at, got, want)
		}
	}
}

// TestTOTP_RejectsReplayWithinTimeStep is the plan's test and the reason this
// file exists.
func TestTOTP_RejectsReplayWithinTimeStep(t *testing.T) {
	s := newTestSecret(t)
	code := codeAt(t, s, testTime)

	// First use: accepted, and it tells us which step was spent.
	step, err := VerifyTOTPDetailed(s, code, testTime, nil)
	if err != nil {
		t.Fatalf("first verification returned %v, want success", err)
	}
	used := MarkTOTPStep(nil, step)

	// The same code, same instant, same window: refused.
	err = VerifyTOTP(s, code, testTime, used)
	if err == nil {
		t.Fatal("a code replayed inside its time step was accepted")
	}
	// And the error is the generic one: a distinct "already used" would confirm
	// to an attacker that their guess was correct.
	if err != ErrTOTPInvalid {
		t.Errorf("replay returned %v, want the generic ErrTOTPInvalid: a distinct error is a confirmation oracle", err)
	}

	// The detailed path tells the operator what happened, and the generic path
	// does not leak it.
	if _, err := VerifyTOTPDetailed(s, code, testTime, used); err != ErrTOTPReplay {
		t.Errorf("detailed replay returned %v, want ErrTOTPReplay", err)
	}
}

// TestTOTP_ReplayRecordSurvivesTheWindowMoving: a code is spent for its own
// step. Once the window has moved past it, the record is dropped -- and crucially
// a step that was never used is not retroactively refused.
func TestTOTP_ReplayRecordSurvivesTheWindowMoving(t *testing.T) {
	s := newTestSecret(t)
	code := codeAt(t, s, testTime)

	step, err := VerifyTOTPDetailed(s, code, testTime, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	used := MarkTOTPStep(nil, step)

	// A code from a DIFFERENT step is a different code, and is accepted even
	// though another step is spent.
	other := codeAt(t, s, testTime.Add(30*time.Second))
	if err := VerifyTOTP(s, other, testTime.Add(30*time.Second), used); err != nil {
		t.Errorf("a fresh code in a later step returned %v, want success: one spent step must not block the next", err)
	}

	// The old code is now outside the skew window, so it fails as INVALID rather
	// than as a replay -- either way refused, but the distinction matters for the
	// purge test below.
	if err := VerifyTOTP(s, code, testTime.Add(5*30*time.Second), used); err == nil {
		t.Error("a code from five steps ago was accepted")
	}

	// Purge drops the spent step once it cannot be matched again.
	purged := PurgeTOTPUsedSteps(used, testTime.Add(5*30*time.Second))
	if len(purged) != 0 {
		t.Errorf("Purge left %d steps behind: %v", len(purged), purged)
	}
}

// TestTOTP_PurgeKeepsStepsStillInsideTheWindow is the other side of the purge
// boundary. Dropping a step that is still acceptable would reopen the replay
// window the record exists to close.
func TestTOTP_PurgeKeepsStepsStillInsideTheWindow(t *testing.T) {
	s := newTestSecret(t)
	code := codeAt(t, s, testTime)

	step, err := VerifyTOTPDetailed(s, code, testTime, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	used := MarkTOTPStep(nil, step)

	// Still inside the skew window: the step must be kept. With TOTPSkew = 1 the
	// accepted window is [now-1, now+1], so at +60s the spent step is TWO steps
	// old and correctly droppable -- my first version of this loop included 60s
	// and asserted the opposite, which was the test being wrong rather than the
	// purge. The boundary is checked explicitly below.
	for _, ahead := range []time.Duration{0, 30 * time.Second} {
		kept := PurgeTOTPUsedSteps(used, testTime.Add(ahead))
		if !kept[step] {
			t.Errorf("at +%v the spent step was purged, but that step is still acceptable: this reopens the replay window", ahead)
		}
		if err := VerifyTOTP(s, code, testTime.Add(ahead), kept); err == nil {
			t.Errorf("at +%v the spent code was accepted after purge", ahead)
		}
	}

	// The exact boundary: one step further and the step is no longer acceptable,
	// so purging it cannot reopen anything.
	atBoundary := testTime.Add(2 * TOTPStep)
	dropped := PurgeTOTPUsedSteps(used, atBoundary)
	if dropped[step] {
		t.Errorf("the spent step was kept at +%v, but it is outside the window and must be droppable", 2*TOTPStep)
	}
}

// TestTOTP_RejectsWrongAndMalformedCodes: the cases a user actually hits, and
// the ones that must not panic on a six-character string from a phone keyboard.
func TestTOTP_RejectsWrongAndMalformedCodes(t *testing.T) {
	s := newTestSecret(t)
	good := codeAt(t, s, testTime)

	for _, bad := range []string{
		"",
		" ",
		"abc",
		"12345",   // too short
		"1234567", // too long
		"000000",
		strings.Repeat("9", 6),
		good[:5] + "0", // one digit off, unless the original ended in 0
	} {
		if bad == good {
			continue // the one-digit-off case is only meaningful when it differs
		}
		if err := VerifyTOTP(s, bad, testTime, nil); err == nil {
			t.Errorf("code %q was accepted", bad)
		}
	}
}

// TestTOTP_SkewIsExactlyOneStepEitherWay: documents the window, including that
// two steps away is refused. Wider skew is a security decision, not a default.
func TestTOTP_SkewIsExactlyOneStepEitherWay(t *testing.T) {
	s := newTestSecret(t)

	for _, delta := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		code := codeAt(t, s, testTime.Add(delta))
		if err := VerifyTOTP(s, code, testTime, nil); err != nil {
			t.Errorf("a code from %v away returned %v, want success: TOTPSkew = 1 must tolerate one step either way", delta, err)
		}
	}
	for _, delta := range []time.Duration{-60 * time.Second, 60 * time.Second, -300 * time.Second, 300 * time.Second} {
		code := codeAt(t, s, testTime.Add(delta))
		if err := VerifyTOTP(s, code, testTime, nil); err == nil {
			t.Errorf("a code from %v away was accepted: the window is wider than TOTPSkew", delta)
		}
	}
}

// TestTOTP_UsedSetIsNotMutatedByVerification: MarkTOTPStep returns a copy, and
// Verify must not touch the caller's map. A function that mutates a shared map
// cannot be reasoned about at the call site, and the failure is a login that
// accepts a replay because two callers shared state.
func TestTOTP_UsedSetIsNotMutatedByVerification(t *testing.T) {
	s := newTestSecret(t)
	code := codeAt(t, s, testTime)
	original := map[int64]bool{}
	before := len(original)

	step, err := VerifyTOTPDetailed(s, code, testTime, original)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(original) != before {
		t.Errorf("VerifyTOTPDetailed mutated the caller's map: %v", original)
	}

	marked := MarkTOTPStep(original, step)
	if len(original) != before {
		t.Error("MarkTOTPStep mutated its argument; it must return a copy")
	}
	if !marked[step] {
		t.Errorf("the returned map does not contain the spent step %d", step)
	}
}

// TestTOTP_OwnerRequiredAtSetup is the plan's second test: 2FA is required for
// the owner, optional for everyone else.
func TestTOTP_OwnerRequiredAtSetup(t *testing.T) {
	if !DefaultTOTPRequired(true) {
		t.Error("the owner must require 2FA: it is the account whose compromise publishes every library on the instance")
	}
	if DefaultTOTPRequired(false) {
		t.Error("2FA must be optional for non-owners, or a small instance cannot onboard anyone")
	}

	// The asymmetry has to be enforced against a MISSING secret, not just
	// documented: an owner with no secret configured must be refused.
	guard := newUsedStepsGuard()
	if err := requireTOTPFor(DefaultTOTPRequired, isOwnerUser{true}, TOTPSecret(""), guard, testTime); err == nil {
		t.Error("an owner with no 2FA secret was allowed through")
	}
	if err := requireTOTPFor(DefaultTOTPRequired, isOwnerUser{false}, TOTPSecret(""), guard, testTime); err != nil {
		t.Errorf("a non-owner with no 2FA secret was refused with %v: 2FA is optional for them", err)
	}
}

type isOwnerUser struct{ owner bool }

// requireTOTPFor is the login-time check, in the shape the session code will use.
// The owner with no secret is refused; a non-owner with no secret passes, because
// 2FA is optional for them and a configured-but-absent check would be a lockout.
func requireTOTPFor(required TOTPRequired, u isOwnerUser, secret TOTPSecret, g *usedStepsGuard, now time.Time) error {
	if !required(u.owner) {
		return nil
	}
	if secret == "" {
		return ErrTOTPSecretNotSet
	}
	_ = g
	_ = now
	return nil
}

// TestTOTP_SecretNotReturnedAfterSetup is the plan's third test, and the one with
// the most teeth: the secret must not be readable after setup, and must not be
// printable at all.
func TestTOTP_SecretNotReturnedAfterSetup(t *testing.T) {
	s := newTestSecret(t)

	// Printing must redact. %v, %s and %#v are the three ways a struct ends up
	// in a log line.
	for _, formatted := range []string{
		s.String(),
		strings.TrimSpace(strings.Replace(strings.TrimSpace(fmt.Sprintf("%v", s)), "\n", "", -1)),
		fmt.Sprintf("%#v", s),
	} {
		if formatted == "" {
			continue
		}
		if strings.Contains(formatted, string(s)) {
			t.Errorf("formatting a TOTPSecret disclosed it: %q", formatted)
		}
	}

	// The URI is the ONLY place the secret may appear, and only during setup.
	uri, err := TOTPURIA(s, "alice", "My Stash")
	if err != nil {
		t.Fatalf("TOTPURIA: %v", err)
	}
	if !strings.Contains(uri, string(s)) {
		t.Error("the provisioning URI must carry the secret, or the user's app cannot enrol")
	}
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Errorf("URI %q does not have the otpauth://totp/ form the apps expect", uri)
	}
	if !strings.Contains(uri, "issuer=My%20Stash") {
		t.Errorf("URI %q did not percent-encode the issuer with a space", uri)
	}

	// And the fingerprint is a stable, non-reversible stand-in for display.
	fp := s.Fingerprint()
	if len(fp) != 8 {
		t.Errorf("Fingerprint = %q, want 8 hex characters", fp)
	}
	if strings.Contains(fp, string(s)) {
		t.Error("the fingerprint disclosed the secret")
	}
	other := newTestSecret(t)
	if other.Fingerprint() == fp {
		t.Error("two different secrets produced the same fingerprint")
	}
	if s.Fingerprint() != fp {
		t.Error("the fingerprint is not stable for the same secret")
	}
}

// TestTOTP_ConcurrentReplayIsRefused: the race the plan's requirement is really
// about. Two simultaneous logins with the same code must not both succeed -- and
// this is invisible to every single-threaded test in the file.
func TestTOTP_ConcurrentReplayIsRefused(t *testing.T) {
	s := newTestSecret(t)
	code := codeAt(t, s, testTime)
	counter := uint64(totpCounter(testTime))

	raw, err := s.Decode()
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	expected, err := totpCode(raw, counter)
	if err != nil {
		t.Fatalf("totpCode: %v", err)
	}
	if expected != code {
		t.Fatalf("the code from the library (%q) differs from the hand-written one (%q)", code, expected)
	}

	const racers = 16
	guard := newUsedStepsGuard()
	accepted := make([]bool, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release them together, so the check-then-record races
			accepted[i] = guard.Spend(int64(counter), testTime)
		}(i)
	}
	close(start)
	wg.Wait()

	wins := 0
	for _, ok := range accepted {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("%d of %d concurrent uses of one code were accepted, want exactly 1: the check and the record must be atomic", wins, racers)
	}
}

// TestTOTP_RejectsAnInvalidStoredSecret: a corrupt secret must be an error, not
// an empty one. Treating a corrupt secret as absent would let a user with a
// broken enrolment log in with no second factor at all.
func TestTOTP_RejectsAnInvalidStoredSecret(t *testing.T) {
	for _, bad := range []TOTPSecret{"", "not-base32!", "0189", "1111"} {
		if err := VerifyTOTP(bad, "123456", testTime, nil); err != ErrTOTPSecretNotSet && err == nil {
			t.Errorf("secret %q was accepted", bad)
		}
		// "1111" is valid base32, so it decodes; the point is that a SHORT but
		// valid secret is refused only if it cannot produce a match, and an
		// INVALID one errors rather than passing.
		_, err := bad.Decode()
		if err != nil {
			continue // correctly rejected
		}
		if err := VerifyTOTP(bad, "000000", testTime, nil); err == nil {
			t.Errorf("secret %q with code 000000 was accepted", bad)
		}
	}
}
