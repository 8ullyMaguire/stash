package ed2k

import (
	"bytes"
	"errors"
	"fmt"
)

// ErrHashMismatch means the bytes are not the file a link named.
//
// # NOT ErrMalformed, AND THE DISTINCTION IS THE POINT
//
// ErrMalformed says the LINK is wrong: it cannot be parsed, its name escapes
// the download root, its hex is not 32 characters. This says the bytes are
// wrong, which is a different failure with a different remedy -- fetch again
// from another source, or report the source as lying.
//
// A caller that gets ErrMalformed must not write the file either, but it
// should not go looking for a bad link, and conflating the two sends triage
// in the wrong direction.
var ErrHashMismatch = errors.New("the bytes are not the file the link named")

// The gate: given bytes a stranger sent, prove they are the bytes the link
// asked for, and refuse if they are not.
//
// # WHAT IS ALREADY HERE, AND WHY THIS FILE ADDS ONLY ONE THING
//
// ehash.go implements the ed2k hash itself, and it implements it correctly:
// the part boundary, both branches of the file hash, the exact-multiple edge
// case, and the folder-link tree. Duplicating any of that here would give
// the package two implementations of the boundary, and two implementations
// of a boundary is two chances to disagree about it -- which is exactly the
// defect ehash.go's longest comment is about, measured on real byte counts.
//
// So the hash is not reimplemented. What is missing is the VERIFICATION: a
// function that takes the file a link named and the bytes that arrived, and
// answers whether they match. That is a different question from "what is
// this file's hash", and it is the one the transfer has to ask before
// writing anything to disk.

// Verified is the result of checking a file's bytes against a link's hash.
//
// # WHY A STRUCT AND NOT A BOOL
//
// A bool cannot say WHICH file failed, and a download has exactly one link's
// hash in hand at a time. An error that names the expected hash and the
// computed one is the difference between a triageable failure and a log line
// that says "verification failed" and nothing else.
type Verified struct {
	// Hash is the hash the bytes actually produced.
	Hash Hash

	// Bytes is how many bytes were checked.
	Bytes int64
}

// VerifyBytes checks bytes against the hash a link carried.
//
// # THE HASH IS THE LINK'S, AND IT IS NEVER RECOMPUTED FROM THE NAME
//
// This function's whole point is to compare against a hash that came from
// somewhere else. An earlier draft of the transfer plan proposed verifying by
// recomputing MD4(size || name) and comparing -- and measuring that against
// the live capture showed the server's claimed hash does NOT reproduce that
// way:
//
//	claim    40d349929c69b3735a1d5247b6fedde6   (Hw-004.mp4, 505365630 bytes)
//	MD4      e59bebc5a171ed85a31e1f09d03b746e
//	MD5      58f3b1e999ac9e1eb46438f2de216766
//
// So "recompute and compare" would reject every file this server offers, and
// it would be RIGHT to. The link's hash is the only authority available, and
// treating it as authority is what makes the check meaningful rather than
// self-referential.
//
// # AND A SIZE MISMATCH IS REFUSED BEFORE ANY HASHING
//
// The link states a size and the bytes have one. If they differ, the file is
// not the file, and computing an MD4 over gigabytes to say so is a waste of
// time that also reports a hash mismatch -- which points at the wrong cause.
// The size check is first so the message can name the real one.
func VerifyBytes(want Hash, name string, wantSize int64, got []byte) (Verified, error) {
	var v Verified

	if int64(len(got)) != wantSize {
		return v, fmt.Errorf("%w: the link says %q is %d bytes and %d "+
			"arrived, so these are not that file. Checking the hash of "+
			"the wrong number of bytes would report a hash mismatch and "+
			"name the wrong cause",
			ErrHashMismatch, name, wantSize, len(got))
	}

	// # A ZERO HASH IN A LINK IS REFUSED, NOT TRUSTED
	//
	// ParseHash already rejects malformed hex, and a link can still carry
	// 32 zeros. Hashing a file and comparing it to all zeros can never
	// succeed, and reporting "mismatch" for a link that was never valid is
	// a worse error than saying the link is bad.
	if want.IsZero() {
		return v, fmt.Errorf("%w: the link for %q carries an all-zero "+
			"hash, so it names no file this client can verify",
			ErrHashMismatch, name)
	}

	sum := HashBytes(got)
	if sum != want {
		return v, fmt.Errorf("%w: %q hashed to %s and the link says %s. "+
			"The bytes are not that file, and nothing is written",
			ErrHashMismatch, name, sum, want)
	}

	v.Hash = sum
	v.Bytes = int64(len(got))
	return v, nil
}

// VerifyPart checks ONE part's bytes against the part hash a link's hash
// set carried.
//
// # THIS IS NOT A WHOLE-FILE CHECK AND DOES NOT PRETEND TO BE
//
// A link carries the file's hash; the per-part hash set is a separate
// structure that this plugin does not currently receive. So this function
// takes the part's expected hash as an argument, and a caller that has no
// hash set simply does not call it -- rather than calling it with something
// invented, which is the failure mode that a "convenience" overload
// invites.
//
// Why it exists at all: a part hash is attributable to a PART. A file that
// fails its whole-file hash tells you the file is bad and nothing about
// where; a part that fails its hash tells you exactly which window to fetch
// again, which is what makes a failed download resumable rather than a
// restart.
func VerifyPart(want Hash, part int, got []byte) error {
	if want.IsZero() {
		return fmt.Errorf("%w: part %d has an all-zero expected hash, so "+
			"there is nothing to check the bytes against",
			ErrHashMismatch, part)
	}
	if len(got) == 0 {
		return fmt.Errorf("%w: part %d arrived empty, and an empty part "+
			"is never a legitimate answer. The last window of a file "+
			"is short, not empty", ErrHashMismatch, part)
	}

	// A part's hash is MD4 over the part's own bytes, which is what
	// ehash.go's readPart boundaries produce when a reader is given exactly
	// one part. HashBytes on a single part is that value.
	got2 := HashBytes(got)
	if got2 != want {
		return fmt.Errorf("%w: part %d hashed to %s and %d bytes were "+
			"expected to hash to %s. This window is to be fetched again, "+
			"and the rest of the file is unaffected",
			ErrHashMismatch, part, got2, len(got), want)
	}
	return nil
}

// Equal reports whether two hashes are the same.
//
// # IT EXISTS SO A CALLER DOES NOT USE == ON A THING THAT MAY BE ABSENT
//
// A Hash is an array, so == compiles and means what it says. The helper is
// here because the interesting comparison is against a hash that may not
// have been set, and `IsZero` is the question that has to be asked first.
// It is a one-liner and that is the point: the reasoning belongs at the call
// site, in a comment, rather than inside a helper that hides it.
func Equal(a, b Hash) bool { return a == b }

// Empty reports whether b holds no bytes.
func (h Hash) Empty() bool { return len(bytes.TrimLeft(h[:], "\x00")) == 0 }
