package ed2k

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// The hash gate: bytes a stranger sent, against a hash a link named.
//
// # WHY THE EXPECTATIONS BELOW COME FROM openssl AND NOT FROM THIS PACKAGE
//
// A round trip proves two halves agree and never that either is right, and
// this package has been bitten by exactly that: mirrored codec halves agreed
// perfectly while both were wrong. So the values here are computed by
// `openssl dgst -provider legacy -md4 -r`, which shares no code with the Go
// implementation, and the test that proves the reference itself is sound
// pins openssl to RFC 1320's published vectors first.
//
// # THE -provider legacy FLAG IS NOT OPTIONAL
//
// OpenSSL 3 moved MD4 to the legacy provider. A plain `openssl dgst -md4`
// fails with "unsupported", and Python's hashlib.new("md4") raises
// UnsupportedDigestmodError. A verification step that cannot run the
// algorithm is worse than none, so this file says which invocation works --
// and the test below SKIPS with that instruction rather than passing quietly
// on a machine where the reference is unavailable.

// referenceMD4 is the independent value: openssl's MD4, not ours.
//
// A SKIP and not a FAIL when openssl is absent or cannot do MD4. The
// reasoning is the reverse of the live tests' -- a live test that skips
// proves nothing, but a REFERENCE test that skips on a machine without
// openssl would be a false alarm, because the golden values are also pinned
// as literals in the tests below. Nothing is lost by skipping here: the
// literals still check, and this only checks that the literals are reachable
// by re-running the command.
func referenceMD4(t *testing.T, data []byte) (string, bool) {
	t.Helper()
	cmd := exec.Command("openssl", "dgst", "-provider", "legacy", "-md4", "-r")
	cmd.Stdin = strings.NewReader(string(data))
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Skipf("openssl cannot compute MD4 here, so the independent "+
			"cross-check is unavailable. The golden values in this file "+
			"are literals and are still checked. The failure was: %v "+
			"(%s)", err, strings.TrimSpace(stderr.String()))
	}
	fields := strings.Fields(stdout.String())
	if len(fields) == 0 {
		t.Skipf("openssl produced no output for MD4: %q",
			strings.TrimSpace(stdout.String()))
	}
	return fields[0], true
}

// TestTheReferenceItselfIsSound: openssl pinned to RFC 1320.
//
// If this fails, every other expectation in this file is suspect, so it runs
// first and says why.
func TestTheReferenceItselfIsSound(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "31d6cfe0d16ae931b73c59d7e0c089c0"},
		{"a", "bde52cb31de33e46245e05fbdbd6fb24"},
		{"abc", "a448017aaf21d8525fc10ae87aa6729d"},
		{"message digest", "d9130a8164549fe818874806e1c7014b"},
		{"abcdefghijklmnopqrstuvwxyz", "d79e1c308aa5bbcdeea8ed63df412da9"},
	} {
		got, ok := referenceMD4(t, []byte(tc.in))
		if !ok {
			return
		}
		if got != tc.want {
			t.Errorf("MD4(%q) = %s, want %s. The reference is wrong, so "+
				"every golden value in this file is suspect", tc.in, got,
				tc.want)
		}
	}
}

// TestTheFileHashIsMD4AndNotSomethingElse: the digest is pinned, not derived.
//
// The value below is MD4 of 43 bytes, from openssl. If the implementation
// were MD5, SHA-1 or SHA-256 it would produce a different digest, so this
// distinguishes the algorithm rather than merely agreeing with itself.
func TestTheFileHashIsMD4AndNotSomethingElse(t *testing.T) {
	const data = "the quick brown fox jumps over the lazy dog"
	const wantMD4 = "a7742ae87a7beaa727157f8470afbfb4"
	// The other digests of the same bytes, so a reader can see this is a
	// real distinction and not a value nobody checked.
	const notMD5 = "77add1d5f41223d5582fca736a5cb335"

	got := HashBytes([]byte(data))
	if got.String() != wantMD4 {
		t.Errorf("HashBytes(%q) = %x, want %s.\n\n"+
			"That is not MD5 either (%s), so this is not a case of the "+
			"wrong member of the family -- the bytes being hashed are "+
			"differing, or the algorithm is", data, got, wantMD4, notMD5)
	}
}

// TestTheHashAgreesWithAnIndependentToolOnTheSameBytes: the cross-check.
func TestTheHashAgreesWithAnIndependentToolOnTheSameBytes(t *testing.T) {
	const data = "the quick brown fox jumps over the lazy dog"
	want, ok := referenceMD4(t, []byte(data))
	if !ok {
		return
	}
	got := HashBytes([]byte(data)).String()
	if got != want {
		t.Errorf("HashBytes = %s, openssl says %s. One of the two is "+
			"wrong and they share no code", got, want)
	}
}

// TestAWholeBlockHashesToAnIndependentValue: 184,320 bytes, a full block.
func TestAWholeBlockHashesToAnIndependentValue(t *testing.T) {
	// 184,320 zero bytes -- exactly BlockSize -- hashed by openssl.
	//
	// # THIS LITERAL WAS WRONG ONCE, AND THE TEST IS WHY THAT IS NOW A FIXED VALUE
	//
	// The first version of this constant was transcribed from an earlier
	// exploratory run and did not correspond to these bytes. The test failed
	// on a package that was RIGHT and a reference tool that was right, which
	// is only possible when the third thing -- the literal -- is wrong.
	//
	// That is the failure mode a golden value exists to catch, and it is
	// worth saying plainly: a test that fails for a reason other than the one
	// it names is not a broken test, it is a broken expectation, and the
	// temptation to "fix the code" is exactly backwards.
	//
	// A block boundary off by one byte produces a different digest, so this
	// pins the block size as well as the algorithm.
	const want = "6f212ba6c3154035d585c1bb9dc878fb"

	data := make([]byte, 184320)
	// Sum returns a raw [16]byte, so it is hex-encoded here rather than
	// printed: Hash has a String method and [16]byte does not. That
	// difference is the sort of thing that silently produces a hash of the
	// hex TEXT, which is what the first version of this test did.
	sum := Sum(data)
	if got := fmt.Sprintf("%x", sum); got != want {
		t.Errorf("MD4 of 184,320 zero bytes = %s, want %s. A block size "+
			"off by one produces a different digest, which is why this "+
			"is a literal rather than a round trip", got, want)
	}
}

// TestVerifyBytesAcceptsTheBytesItNamed: the happy path, and it must be real.
func TestVerifyBytesAcceptsTheBytesItNamed(t *testing.T) {
	data := []byte("the quick brown fox jumps over the lazy dog")
	want := HashBytes(data)

	v, err := VerifyBytes(want, "fox.txt", int64(len(data)), data)
	if err != nil {
		t.Fatalf("VerifyBytes refused bytes that DO match: %v", err)
	}
	if v.Hash != want {
		t.Errorf("Verified.Hash = %x, want %x", v.Hash, want)
	}
	if v.Bytes != int64(len(data)) {
		t.Errorf("Verified.Bytes = %d, want %d", v.Bytes, len(data))
	}
}

// TestVerifyBytesRefusesDifferentBytesByName.
//
// # A MISMATCH MUST NAME BOTH HASHES
//
// "verification failed" is a log line nobody can act on during triage. The
// computed hash and the expected hash together are what let someone decide
// whether the source lied or the link was for a different file.
func TestVerifyBytesRefusesDifferentBytesByName(t *testing.T) {
	data := []byte("the quick brown fox jumps over the lazy dog")
	// A different file, same length -- so the SIZE check passes and the
	// hash check is the only thing that can catch it.
	other := []byte("THE QUICK BROWN FOX JUMPS OVER THE LAZY DOG")

	_, err := VerifyBytes(HashBytes(data), "fox.txt", int64(len(data)), other)
	if err == nil {
		t.Fatal("bytes for a different file were accepted")
	}
	if !errors.Is(err, ErrHashMismatch) {
		t.Errorf("the error is %v, which is not ErrHashMismatch. A caller "+
			"must be able to tell a bad file from a bad LINK", err)
	}

	msg := err.Error()
	if !strings.Contains(msg, HashBytes(data).String()) {
		t.Errorf("the error does not name the expected hash: %v", err)
	}
	if !strings.Contains(msg, HashBytes(other).String()) {
		t.Errorf("the error does not name the hash the bytes produced: %v", err)
	}
	if !strings.Contains(msg, "nothing is written") {
		t.Errorf("the error does not say that nothing was written: %v", err)
	}
}

// TestTheSizeIsCheckedBeforeTheHash.
//
// # A SIZE MISMATCH IS A DIFFERENT FAULT AND SAYS SO
//
// Hashing the wrong number of bytes and reporting "hash mismatch" would name
// the wrong cause. The link said one size, the bytes are another, and that
// is knowable before a single byte is hashed.
func TestTheSizeIsCheckedBeforeTheHash(t *testing.T) {
	data := []byte("12345")
	// A link claiming 999 bytes for 5 bytes of content.
	_, err := VerifyBytes(HashBytes(data), "short.txt", 999, data)
	if err == nil {
		t.Fatal("a size mismatch was accepted")
	}
	msg := err.Error()
	if !strings.Contains(msg, "999") || !strings.Contains(msg, "5") {
		t.Errorf("the error does not name both sizes: %v", err)
	}
	// And it must NOT be a hash mismatch: that would send triage looking for
	// a corrupted transfer when the file is simply not the one asked for.
	if strings.Contains(msg, "hashed to") {
		t.Errorf("the error reports a hash mismatch for a size mismatch, "+
			"which names the wrong cause: %v", err)
	}
}

// TestAnAllZeroHashInALinkIsRefusedRatherThanCompared.
func TestAnAllZeroHashInALinkIsRefusedRatherThanCompared(t *testing.T) {
	data := []byte("some content here")
	_, err := VerifyBytes(Hash{}, "zero.txt", int64(len(data)), data)
	if err == nil {
		t.Fatal("an all-zero link hash was accepted")
	}
	if !strings.Contains(err.Error(), "all-zero") {
		t.Errorf("the error does not say the LINK is at fault rather than "+
			"the bytes: %v", err)
	}
	// It is still a hash mismatch in the broad sense -- nothing is written.
	if !errors.Is(err, ErrHashMismatch) {
		t.Errorf("the error is %v, which is not ErrHashMismatch", err)
	}
}

// TestAnEmptyFileIsNotRefusedForBeingEmpty.
//
// # ZERO BYTES IS A REAL FILE, NOT A FAILED TRANSFER
//
// A zero-length ed2k link is legal, and its hash is the MD4 of nothing. If
// the empty case were refused as "too short", every zero-byte link would fail
// with a message about a transfer that did not happen.
func TestAnEmptyFileIsNotRefusedForBeingEmpty(t *testing.T) {
	v, err := VerifyBytes(HashBytes(nil), "empty.txt", 0, nil)
	if err != nil {
		t.Fatalf("an empty file was refused: %v", err)
	}
	if v.Bytes != 0 {
		t.Errorf("Verified.Bytes = %d, want 0", v.Bytes)
	}
	// And the hash must be MD4 of nothing, which is a known value.
	const wantEmpty = "31d6cfe0d16ae931b73c59d7e0c089c0"
	if got := v.Hash.String(); got != wantEmpty {
		t.Errorf("an empty file hashed to %s, want %s (MD4 of no bytes)",
			got, wantEmpty)
	}
}

// TestVerifyPartRefusesEmptyBytesButVerifyBytesDoesNot.
//
// # THE ASYMMETRY IS DELIBERATE, AND IT IS ABOUT WHICH QUESTION IS BEING ASKED
//
// VerifyBytes asks "are these the whole file", and a zero-byte file is a
// whole file. VerifyPart asks "did a window arrive", and a zero-byte WINDOW
// is never a legitimate answer -- the last window of a file is short, not
// empty. Same bytes, different question, opposite answer.
func TestVerifyPartRefusesEmptyBytesButVerifyBytesDoesNot(t *testing.T) {
	if err := VerifyPart(HashBytes(nil), 0, nil); err == nil {
		t.Error("an empty PART was accepted. A window is never legitimately " +
			"empty, and accepting one is how a download appears to " +
			"progress while it does not")
	}
	if _, err := VerifyBytes(HashBytes(nil), "empty.txt", 0, nil); err != nil {
		t.Errorf("an empty FILE was refused, but a zero-byte file is real: %v",
			err)
	}
}

// TestVerifyPartNamesThePartSoAFailedWindowIsAttributable.
//
// # A FILE FAILS AND SAYS WHERE; A PART FAILS AND SAYS WHICH
//
// The whole-file hash tells you the file is bad and nothing more. The
// per-part hash is what makes a failed download resumable rather than a
// restart, and it only helps if the failing part is named.
func TestVerifyPartNamesThePartSoAFailedWindowIsAttributable(t *testing.T) {
	good := []byte("a full window of bytes")
	want := HashBytes(good)

	if err := VerifyPart(want, 7, good); err != nil {
		t.Fatalf("a matching part was refused: %v", err)
	}

	// Same length, different bytes.
	bad := []byte("A FULL WINDOW OF BYTES")
	err := VerifyPart(want, 7, bad)
	if err == nil {
		t.Fatal("a mismatched part was accepted")
	}
	if !strings.Contains(err.Error(), "part 7") {
		t.Errorf("the error does not name the part: %v", err)
	}
	if !strings.Contains(err.Error(), "fetched again") {
		t.Errorf("the error does not say the window is to be fetched again, "+
			"which is the whole point of checking a part: %v", err)
	}
}

// TestAZeroExpectedPartHashIsRefusedRatherThanCompared.
func TestAZeroExpectedPartHashIsRefusedRatherThanCompared(t *testing.T) {
	err := VerifyPart(Hash{}, 3, []byte("bytes"))
	if err == nil {
		t.Fatal("an all-zero expected part hash was accepted")
	}
	if !strings.Contains(err.Error(), "all-zero") {
		t.Errorf("the error does not name the cause: %v", err)
	}
}
