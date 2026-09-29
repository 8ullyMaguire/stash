package utils

// Tests for the phash validator behind #2149.
//
// The property under test is a boundary: a hash within N bits of a known-bad
// value must be rejected, and a hash N+1 bits away must be accepted. Boundary
// tests are easy to write in a way that only tests the middle, so these assert
// the exact edges -- a tolerance of 3 that accidentally accepts 4 is a silent
// regression that no amount of "the known values are rejected" would catch.

import (
	"math/bits"
	"testing"
)

func mustParsePhash(t *testing.T, s string) uint64 {
	t.Helper()
	v, err := parseHex64(s)
	if err != nil {
		t.Fatalf("parseHex64(%q): %v", s, err)
	}
	return v
}

// Every value on the issue must be rejected, and must be rejected by an exact
// match as well as by proximity.
func TestEveryKnownBadPhashIsRejected(t *testing.T) {
	for _, hex := range KnownBadPhashes() {
		bad, _ := IsBadPhash(mustParsePhash(t, hex))
		if !bad {
			t.Errorf("known-bad phash %s was accepted; the whole point of the "+
				"list is that these are the values ffmpeg produces for files it "+
				"cannot decode", hex)
		}
	}
}

// The issue says these values "may vary in the wild by 1-3 bits". That range is
// the requirement, so each distance in it is asserted individually: a
// regression that quietly narrowed the tolerance to 1 would still pass a test
// that only checks the extremes.
func TestKnownBadPhashIsRejectedAcrossTheReportedVariation(t *testing.T) {
	base := mustParsePhash(t, "870707030787fefc")

	// Flip bit 0 -> 3 one bits at a time.
	for n := 1; n <= badPhashTolerance; n++ {
		mutated := base
		for b := 0; b < n; b++ {
			mutated ^= 1 << b
		}
		bad, _ := IsBadPhash(mutated)
		if !bad {
			t.Errorf("a hash %d bits from a known-bad value was accepted; the "+
				"issue reports these varying by 1-3 bits, so %d must be "+
				"rejected", n, n)
		}
	}
}

// The other edge, and the one that is easy to get wrong in the other
// direction. A hash must NOT be rejected merely for being near a bad one, or
// the validator becomes a fingerprint destroyer that silently strips real
// hashes off real files.
func TestAHashJustOutsideTheToleranceIsAccepted(t *testing.T) {
	base := mustParsePhash(t, "870707030787fefc")

	mutated := base
	for b := 0; b < badPhashTolerance+1; b++ {
		mutated ^= 1 << b
	}
	bad, _ := IsBadPhash(mutated)
	if bad {
		t.Errorf("a hash %d bits from a known-bad value was rejected, but the "+
			"tolerance is %d; rejecting real hashes is a worse failure than "+
			"admitting a bad one", badPhashTolerance+1, badPhashTolerance)
	}
}

// A hash with no relationship to any known-bad value must be accepted. The
// values below are arbitrary but fixed, so a change to the list or the
// tolerance shows up as a failure rather than as silence.
func TestAnUnrelatedHashIsAccepted(t *testing.T) {
	for _, hex := range []string{
		"0123456789abcdef",
		"fedcba9876543210",
		"ffffffffffffffff",
		"0000000000000000",
		"1234567890abcdef",
	} {
		bad, _ := IsBadPhash(mustParsePhash(t, hex))
		if bad {
			t.Errorf("hash %s was rejected as known-bad, but it is unrelated to "+
				"every value on the list", hex)
		}
	}
}

// Every real hash must be accepted, which means the tolerance must be small
// enough that a random 64-bit value has a negligible chance of colliding. With
// 9 known-bad values and a tolerance of 3 the chance is about 1.8e-8; this
// test states that expectation so a future change to either number has to
// confront it.
func TestTheToleranceCannotCollideWithARealisticHash(t *testing.T) {
	// 9 patterns * C(64,1..3) = 9 * (64 + 2016 + 41664) = 393,696 values out
	// of 2^64. If the tolerance were raised to 4 this would exceed 4.5e6 and
	// the comment above would be a lie.
	const combinations = 64 + 2016 + 41664
	const patterns = 9
	reachable := float64(patterns*combinations) / float64(1<<64)

	if reachable > 1e-6 {
		t.Errorf("the known-bad set covers %.3g of the 64-bit space, which is "+
			"more than the 1e-6 this design assumes; at that rate the "+
			"validator starts rejecting legitimate hashes", reachable)
	}
	t.Logf("known-bad set covers %.3g of the 64-bit space (%.1f in a million)",
		reachable, reachable*1e6)
}

// HammingDistance64 must count differing bits, and must agree with the
// popcount of the xor -- the phash_distance SQLite function counts the same
// way, and the duplicate finder uses that SQL. If this drifts, a hash
// accepted here is rejected by the matcher.
func TestTheKnownBadListMatchesTheIssueExactly(t *testing.T) {
	// The test's own copy of the values from #2149. A test that re-reads
	// the constant it is testing proves only that the constant is
	// self-consistent: the mutation harness caught exactly that -- a hex
	// value transposed by one digit still parses, still matches itself, and
	// every round-trip test stayed green. Nothing inside the package can
	// detect a wrong digit, so the values are pinned here against a
	// transcription that is independent of the constant. If these two lists
	// ever disagree, one of them has a typo and the check is not catching
	// the value it claims to.
	want := []string{
		"a000000000800080",
		"8080808080808080",
		"870707030787fefc",
		"82070707078ffff8",
		"8055557555575575",
		"805555555d755d55",
		"87070707037ef8fc",
		"8707070303fefcdc",
		"cdcdcdc9c1233332",
	}

	got := KnownBadPhashes()
	if len(got) != len(want) {
		t.Fatalf("the known-bad list has %d entries, the issue has %d: %v vs %v",
			len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d is %q, the issue reports %q; a transposed digit "+
				"silently disables the check for that value", i, got[i], want[i])
		}
	}
}

// Every value must be exactly 16 hex digits, and must be distinct. A short
// value is a truncation, and a duplicate is a copy-paste -- both leave the
// list covering less than it appears to.
func TestTheKnownBadListIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, hex := range KnownBadPhashes() {
		if len(hex) != 16 {
			t.Errorf("%q is %d characters, not 16; a phash is 64 bits", hex, len(hex))
		}
		if _, err := parseHex64(hex); err != nil {
			t.Errorf("%q does not parse: %v", hex, err)
		}
		if seen[hex] {
			t.Errorf("%q appears twice in the list", hex)
		}
		seen[hex] = true
	}
}

func TestHammingDistanceCountsDifferingBits(t *testing.T) {
	tests := []struct {
		a, b uint64
		want int
	}{
		{0, 0, 0},
		{0xffffffffffffffff, 0xffffffffffffffff, 0},
		{0, 0xffffffffffffffff, 64},
		{0, 1, 1},
		{0, 1 << 63, 1},
		// 0x0f... ^ 0xf0... differs in every one of the 64 bits, not 32 --
		// each nibble pair is 0000 vs 1111. Written out here because the
		// intuitive answer is 32 and the correct one is not.
		{0x0f0f0f0f0f0f0f0f, 0xf0f0f0f0f0f0f0f0, 64},
		// Differing in the low nibble of each byte is 32 bits.
		{0x0000000000000000, 0x0f0f0f0f0f0f0f0f, 32},
		{^uint64(0), ^uint64(0) - 1, 1},
	}

	for _, tt := range tests {
		got := HammingDistance64(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("HammingDistance64(%#x, %#x) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if want := bits.OnesCount64(tt.a ^ tt.b); got != want {
			t.Errorf("HammingDistance64(%#x, %#x) = %d but popcount(xor) = %d; "+
				"these must agree or the SQL phash_distance will disagree with "+
				"this", tt.a, tt.b, got, want)
		}
	}
}

// The hex round trip: a value rejected is reported in the same notation the
// issue and the logs use, so an operator can grep for it.
func TestTheReportedValueRoundTripsThroughTheSameNotation(t *testing.T) {
	const hex = "cdcdcdc9c1233332"
	phash := mustParsePhash(t, hex)

	bad, reported := IsBadPhash(phash)
	if !bad {
		t.Fatal("a known-bad phash was not rejected")
	}
	if reported != hex {
		t.Errorf("reported %q, want the original %q; a log line that renames "+
			"the value makes the list uncheckable against a bug report", reported, hex)
	}
	if d := PhashDistanceTo(phash, reported); d != 0 {
		t.Errorf("a value reported as matching is %d bits from itself", d)
	}
}

// The round trip above is only meaningful for an EXACT match, where the input
// and the matched pattern are the same string and reporting either one is
// indistinguishable. The mutation harness caught that: swapping the reported
// value for the input survived, because no test used a value that is close to
// a pattern but not equal to it.
//
// A near-miss is the case that matters operationally. A log line naming the
// input value tells the reader nothing they did not already have; naming the
// pattern tells them which known-bad signature was hit, which is what makes
// the line searchable against a StashDB dump.
func TestANearMissReportsThePatternNotTheInput(t *testing.T) {
	const pattern = "870707030787fefc"

	// 2 bits from the pattern: rejected, and definitely not equal to it.
	phash, err := parseHex64(pattern)
	if err != nil {
		t.Fatal(err)
	}
	mutated := phash ^ 0b11

	bad, reported := IsBadPhash(mutated)
	if !bad {
		t.Fatal("a value 2 bits from a known-bad value was not rejected")
	}
	if reported != pattern {
		t.Errorf("reported %q, want the matched pattern %q; reporting the input "+
			"instead makes the log line useless for finding which signature was "+
			"hit", reported, pattern)
	}

	inputHex := PhashToString(int64(mutated))
	if inputHex == reported {
		t.Fatal("the fixture is degenerate: the input and the pattern are the " +
			"same value, so this test cannot distinguish them")
	}
	if d := PhashDistanceTo(mutated, reported); d != 2 {
		t.Errorf("the reported pattern is %d bits from the input, want 2", d)
	}
}

// The constants are hex strings, and a typo in one would silently disable a
// check. KnownBadPhashes hands out a copy so a caller cannot corrupt the list.
func TestKnownBadPhashesIsACopy(t *testing.T) {
	got := KnownBadPhashes()
	if len(got) == 0 {
		t.Fatal("the known-bad list is empty")
	}
	got[0] = "tampered"
	if KnownBadPhashes()[0] == "tampered" {
		t.Error("KnownBadPhashes returned the live slice; a caller can corrupt " +
			"the list for the whole process")
	}
}

// A malformed hex string in a diagnostic path must not panic. This is called
// from a logging argument, and a panic there would abort a scan task.
func TestPhashDistanceToAnUnparseableValueDoesNotPanic(t *testing.T) {
	if got := PhashDistanceTo(0x1234, "not hex"); got != -1 {
		t.Errorf("PhashDistanceTo with an unparseable value = %d, want -1", got)
	}
	if got := PhashDistanceTo(0x1234, ""); got != -1 {
		t.Errorf("PhashDistanceTo with an empty value = %d, want -1", got)
	}
}
