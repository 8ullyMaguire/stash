package manager

// stash#2149: a file ffmpeg cannot decode produces a well-formed phash that is
// IDENTICAL for every file that fails the same way -- a black frame or a
// colour bar hashed 25 times. Those values get uploaded to StashDB, where
// they match every other broken file and produce a confidently wrong scene
// match. A missing fingerprint is harmless; a wrong one that has been
// published is not.
//
// The check lives in storablePhash so it can be exercised without ffmpeg.
// These tests pin the DECISION, which is the part that can silently regress:
// the numbers themselves are covered in pkg/utils.

import (
	"testing"

	"github.com/stashapp/stash/pkg/utils"
)

// phashPtr parses a hex phash the way the rest of the codebase does -- via
// utils.StringToPhash, the same function the StashDB query path and the
// duplicate finder use. Hand-rolling a parser in the test would mean this
// suite and the code could disagree about what a phash is.
func phashPtr(t *testing.T, hex string) *uint64 {
	t.Helper()
	v, err := utils.StringToPhash(hex)
	if err != nil {
		t.Fatalf("parsing %q as a phash: %v", hex, err)
	}
	u := uint64(v)
	return &u
}

// Every value on the issue must be refused. This is the direct statement of
// the bug: these are the values that, if stored, cause wrong matches.
func TestAKnownBadPhashIsNotStored(t *testing.T) {
	for _, hex := range utils.KnownBadPhashes() {
		_, ok := storablePhash(phashPtr(t, hex))
		if ok {
			t.Errorf("storablePhash accepted the known-bad value %s; storing it "+
				"uploads a hash that matches every other undecodable file in "+
				"the world", hex)
		}
	}
}

// A normal hash must be stored. The failure this guards against is the
// validator silently rejecting everything, which is a worse outage than the
// bug: every file in the library would quietly lose its fingerprint.
func TestAGoodPhashIsStored(t *testing.T) {
	for _, hex := range []string{
		"0123456789abcdef",
		"fedcba9876543210",
		"a1b2c3d4e5f60718",
	} {
		got, ok := storablePhash(phashPtr(t, hex))
		if !ok {
			t.Errorf("storablePhash refused the legitimate value %s", hex)
			continue
		}
		if want := phashPtr(t, hex); uint64(got) != *want {
			t.Errorf("storablePhash(%s) wrote %#x, want %#x; the stored value "+
				"must be the generated one, unmodified", hex, uint64(got), *want)
		}
	}
}

// The rejected case must not write a zero. A caller that ignores the ok flag
// would store 0, and 0 is a perfectly valid phash value that matches nothing in
// particular -- so the damage would be invisible.
func TestARejectedPhashYieldsNoValueToStore(t *testing.T) {
	got, ok := storablePhash(phashPtr(t, "8080808080808080"))
	if ok {
		t.Fatal("expected the known-bad value to be refused")
	}
	if got != 0 {
		t.Errorf("a refused phash returned %#x; the caller writes this on a "+
			"true, and 0 is a valid-looking hash that would be stored silently",
			uint64(got))
	}
}

// A nil from the generator is a programming error upstream, and must not
// become a stored zero. videophash.Generate returns nil only alongside an
// error, which the caller handles first, so this branch is defensive -- but a
// dereference panic inside a task is worse than a refused write.
func TestANilPhashIsRefusedRatherThanDereferenced(t *testing.T) {
	got, ok := storablePhash(nil)
	if ok {
		t.Error("a nil phash was accepted for storage")
	}
	if got != 0 {
		t.Errorf("a nil phash produced %#x, want 0", uint64(got))
	}
}

// The variation window is the actual requirement from the issue ("may vary in
// the wild by 1-3 bits"), so the integration boundary is asserted here too,
// not only in the unit tests for the distance function.
func TestAKnownBadPhashWithinTheReportedVariationIsNotStored(t *testing.T) {
	base := phashPtr(t, "870707030787fefc")
	for n := 1; n <= 3; n++ {
		mutated := *base
		for b := 0; b < n; b++ {
			mutated ^= 1 << b
		}
		if _, ok := storablePhash(&mutated); ok {
			t.Errorf("a hash %d bits from a known-bad value was accepted for "+
				"storage; the issue reports these varying by 1-3 bits", n)
		}
	}
}

// And one past the window, so a tolerance that grew by accident is caught here
// as well as in the unit tests.
func TestAHashJustOutsideTheWindowIsStored(t *testing.T) {
	base := phashPtr(t, "870707030787fefc")
	mutated := *base
	for b := 0; b < 4; b++ {
		mutated ^= 1 << b
	}
	if _, ok := storablePhash(&mutated); !ok {
		t.Error("a hash 4 bits from a known-bad value was refused; the window " +
			"is 3, and refusing real hashes is a worse failure than the bug")
	}
}
