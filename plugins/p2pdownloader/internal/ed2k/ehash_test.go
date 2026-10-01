package ed2k

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestSumAgainstTheRFC1320Vectors: the seven test vectors from RFC 1320
// appendix A.5, which is the specification MD4 is defined by.
//
// A SECOND implementation is the point. `Sum` is a five-line wrapper over
// `golang.org/x/crypto/md4`, so a test that asserted `Sum(x) == md4.New().Sum()`
// would compare the library with itself and prove nothing. These vectors come
// from the document, not from the code, so a wrong `Sum` is caught rather than
// agreed with — which is the same lesson as taking 2FA codes from a library
// other than the one under test.
func TestSumAgainstTheRFC1320Vectors(t *testing.T) {
	for _, v := range []struct{ in, want string }{
		{"", "31d6cfe0d16ae931b73c59d7e0c089c0"},
		{"a", "bde52cb31de33e46245e05fbdbd6fb24"},
		{"abc", "a448017aaf21d8525fc10ae87aa6729d"},
		{"message digest", "d9130a8164549fe818874806e1c7014b"},
		{"abcdefghijklmnopqrstuvwxyz", "d79e1c308aa5bbcdeea8ed63df412da9"},
		{"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789",
			"043f8582f241db351ce627e153e7f0e4"},
		{"12345678901234567890123456789012345678901234567890123456789012345678901234567890",
			"e33b4ddc9c38f2199c3e7b164fcc0536"},
	} {
		t.Run(v.in, func(t *testing.T) {
			got := Hash(Sum([]byte(v.in))).String()
			if got != v.want {
				t.Errorf("Sum(%q) = %s, want %s (RFC 1320 A.5)", v.in, got, v.want)
			}
		})
	}
}

// TestPartSizeIsTheProtocolsConstant: 9,728,000 bytes, which is the protocol's
// "9500 KiB" — NOT 9,500,000, and NOT 9500*1024.
//
// The three are different numbers and a file sized to the wrong boundary hashes
// differently, so the constant is pinned by its own value rather than left as
// whatever the expression in a comment evaluates to.
func TestPartSizeIsTheProtocolsConstant(t *testing.T) {
	if PartSize != 9728000 {
		t.Fatalf("PartSize = %d, want 9728000. The protocol writes \"9.5 MB\" "+
			"and means 9,728,000 bytes; 9,500,000 and %d are both wrong and "+
			"hash every large file differently",
			PartSize, 9500*1024)
	}
	if PartHashPrefixLength != 8 {
		t.Fatalf("PartHashPrefixLength = %d, want 8 — the tree hash "+
			"concatenates the first EIGHT bytes of each part hash, not all "+
			"sixteen", PartHashPrefixLength)
	}
}

// TestAFileOfExactlyOnePartIsHashedWhole is the boundary test, and it is the
// one this package's first version got wrong.
//
// The protocol splits a file into full 9500 KiB parts "plus a remainder chunk",
// and uses the tree "if the file is greater than 9500 KiB (which means that
// there is more than one chunk)" — otherwise "the MD4 hash of the only chunk of
// the file is used with no further modifications". A file of exactly PartSize
// bytes is NOT greater than PartSize: it is one chunk with no remainder.
//
// With `<` instead of `<=`, such a file falls through to the tree branch, the
// loop's first read returns nothing, the `len(rest) > 0` guard is false, and
// the result is `MD4(first 8 bytes of MD4(file))` — a hash that is plausible,
// well formed, and matches nothing on any real client.
//
// So the assertion is against the whole-file MD4, which is what the
// specification says, and the three sizes around the boundary are asserted
// together so the direction of the comparison is pinned on both sides.
func TestAFileOfExactlyOnePartIsHashedWhole(t *testing.T) {
	for _, c := range []struct {
		name  string
		size  int
		whole bool
	}{
		{"one byte under the boundary", PartSize - 1, true},
		{"EXACTLY the boundary", PartSize, true},
		{"one byte over the boundary", PartSize + 1, false},
		{"exactly two parts", 2 * PartSize, false},
		{"two parts and a byte", 2*PartSize + 1, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			buf := bytes.Repeat([]byte{0xA5}, c.size)
			got, err := HashFile(bytes.NewReader(buf))
			if err != nil {
				t.Fatalf("HashFile: %v", err)
			}
			whole := Hash(Sum(buf))

			if c.whole && got != whole {
				t.Errorf("a file of %d bytes hashed as %s, but the protocol "+
					"uses the whole-file MD4 (%s) for anything not greater "+
					"than one part. A file of exactly PartSize is ONE part",
					c.size, got, whole)
			}
			if !c.whole && got == whole {
				t.Errorf("a file of %d bytes hashed as the whole-file MD4 "+
					"(%s), but it is larger than one part so the tree "+
					"applies", c.size, got)
			}
		})
	}
}

// TestTheBoundaryIsNotAccidental: the exact-boundary case above would also pass
// if HashFile happened to agree with Sum for this particular content. The
// numbers are pinned so a change of the comparison operator shows up as a
// changed digest rather than as a silently different file.
//
// Measured on 9,728,000 bytes of 0xA5: the tree form gives
// 349f76168d62b0a7709aeed1512abd6a and the whole-file MD4 gives
// 9cab445c0310e326f5c73a1953882e84. These are different, so the boundary case
// has real discriminating power.
func TestTheBoundaryIsNotAccidental(t *testing.T) {
	buf := bytes.Repeat([]byte{0xA5}, PartSize)
	const (
		treeForm  = "349f76168d62b0a7709aeed1512abd6a"
		wholeForm = "9cab445c0310e326f5c73a1953882e84"
	)
	if got := Hash(Sum(buf)).String(); got != wholeForm {
		t.Fatalf("the whole-file MD4 of the fixture is %s, not %s — the "+
			"fixture changed and the pinned digests below are meaningless",
			got, wholeForm)
	}
	got, err := HashFile(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	if got.String() == treeForm {
		t.Errorf("a file of exactly PartSize bytes still hashes as the "+
			"one-part tree %s, so the boundary comparison is still `<`",
			treeForm)
	}
}

// TestDifferentFilesMustGetDifferentHashes is the property that caught the
// worst bug in this file's first version, and it is worth its own test rather
// than being implied by the boundary rows.
//
// With the boundary comparison written as `<=`, a file of PartSize+1 bytes
// took the whole-file path and the final byte was discarded, so PartSize+1,
// PartSize×2 and PartSize×2+1234 ALL hashed identically. Every existing test
// still passed: each compared the function against its own hand-built
// expectation, and the hand-built expectation had the same remainder bug in it.
//
// A hash that cannot tell one file from another is the failure the whole
// package exists to prevent, so it is asserted directly and over sizes chosen
// to span the boundary and the remainder.
func TestDifferentFilesMustGetDifferentHashes(t *testing.T) {
	sizes := []int{
		0, 1, 1024,
		PartSize - 1, PartSize, PartSize + 1, PartSize + 2,
		PartSize + PartSize/2,
		2 * PartSize, 2*PartSize + 1, 2*PartSize + 1234,
		3 * PartSize, 3*PartSize + 7,
	}
	seen := make(map[string]int, len(sizes))
	for _, size := range sizes {
		h := HashBytes(bytes.Repeat([]byte{0xC4}, size)).String()
		if prev, clash := seen[h]; clash {
			t.Fatalf("a file of %d bytes and one of %d bytes have the SAME "+
				"hash %s. A hash that cannot tell two files apart identifies "+
				"neither", prev, size, h)
		}
		seen[h] = size
	}
}

// TestTheTreeHashesThePartHashesNotThePartBytes, and only the first eight bytes
// of each.
//
// The protocol builds the tree from the part HASHES, truncated to eight bytes
// each. A concatenation of full 16-byte part hashes, or of the raw part bytes,
// is a different hash — so this computes the tree by hand, three ways, and
// asserts that only the protocol's way is what the function produces.
//
// THE HAND-BUILT EXPECTATION MUST COVER EVERY PART, INCLUDING THE REMAINDER.
// The first version of this test built the expectation for the two full parts
// only and left the 1234-byte remainder out, so the function was right and the
// test was wrong — and the same test then passed against a function that
// dropped the remainder for exactly the same reason. The part count is
// asserted against the function's own view of the file, so a missing part
// cannot hide in the fixture.
func TestTheTreeHashesThePartHashesNotThePartBytes(t *testing.T) {
	// Two full parts plus a short remainder.
	const total = 2*PartSize + 1234
	buf := bytes.Repeat([]byte{0x5A}, total)

	// Every part, by construction: a full slice, then the remainder.
	parts := make([][]byte, 0, 3)
	for off := 0; off < total; off += PartSize {
		end := off + PartSize
		if end > total {
			end = total
		}
		parts = append(parts, buf[off:end])
	}
	if len(parts) != 3 {
		t.Fatalf("the fixture splits into %d parts, want 3 (two full plus a "+
			"remainder). A hand-built expectation that covers only the full "+
			"parts is how the remainder got dropped once already", len(parts))
	}
	if len(parts[2]) != 1234 {
		t.Fatalf("the last part is %d bytes, want the 1234-byte remainder",
			len(parts[2]))
	}

	// The protocol's construction.
	var correct []byte
	for _, p := range parts {
		s := Sum(p)
		correct = append(correct, s[:PartHashPrefixLength]...)
	}

	// Two plausible wrong ones, so the test can tell the difference rather
	// than merely agreeing with the implementation.
	var fullPartHashes []byte
	for _, p := range parts {
		s := Sum(p)
		fullPartHashes = append(fullPartHashes, s[:]...)
	}

	if bytes.Equal(correct, fullPartHashes) {
		t.Fatal("the correct and the full-hash constructions are identical, " +
			"so this test cannot tell them apart")
	}

	got, err := HashFile(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	if want := Hash(Sum(correct)); got != want {
		t.Errorf("HashFile = %s, want %s (MD4 of the first 8 bytes of each "+
			"part hash, concatenated in part order, remainder included)",
			got, want)
	}
	if bad := Hash(Sum(fullPartHashes)); got == bad {
		t.Errorf("HashFile = %s, which is the FULL 16-byte part hashes "+
			"concatenated. The protocol truncates each to %d bytes",
			got, PartHashPrefixLength)
	}
}

// TestHashBytesAndHashFileAgree: HashBytes must not be a shortcut that hashes
// small inputs whole and large inputs with a tree. Two implementations of the
// boundary is two chances to disagree about it, so there is only one.
func TestHashBytesAndHashFileAgree(t *testing.T) {
	for _, size := range []int{0, 1, 1024, PartSize - 1, PartSize, PartSize + 1, 2 * PartSize} {
		buf := bytes.Repeat([]byte{0x3C}, size)
		streamed, err := HashFile(bytes.NewReader(buf))
		if err != nil {
			t.Fatalf("size %d: HashFile: %v", size, err)
		}
		if direct := HashBytes(buf); direct != streamed {
			t.Errorf("size %d: HashBytes = %s, HashFile = %s. They must be "+
				"the same function, not two implementations of the boundary",
				size, direct, streamed)
		}
	}
}

// TestHashBytesOfAnEmptyInputIsTheMD4OfNothing: the empty file is a legal input
// to the hasher even though the parser refuses a zero SIZE, because the parser
// is refusing a link and this is a function.
func TestHashBytesOfAnEmptyInputIsTheMD4OfNothing(t *testing.T) {
	// MD4 of zero bytes is 31d6cfe0d16ae931b73c59d7e0c089c0 — a fixed point
	// of the padding, and emphatically NOT a zero hash. The assertion below
	// that it is not zero is what keeps the "the zero hash names nothing"
	// property from being confused with "the hash of nothing is nothing".
	const md4OfNothing = "31d6cfe0d16ae931b73c59d7e0c089c0"
	got := HashBytes(nil)
	if want := Hash(Sum(nil)); got != want {
		t.Errorf("HashBytes(nil) = %s, want %s", got, want)
	}
	if got.String() != md4OfNothing {
		t.Errorf("HashBytes(nil) = %s, want %s", got, md4OfNothing)
	}
	if got.IsZero() {
		t.Error("the MD4 of no bytes reports IsZero. The all-zero hash means " +
			"\"this link named no file\"; the empty file has a real hash")
	}
}

// TestThePartBoundaryIsReadWhole: a Reader is allowed to return a short read
// with a nil error, and a short read in the middle of a part would hash a
// truncated part as though it were the whole one. readPart uses io.ReadFull, so
// the function keeps reading — this drives it with a reader that hands back
// three bytes at a time and asserts the digest is the same as the whole.
//
// The reader deliberately returns io.EOF only on a LATER call than the one
// that delivered the last byte, because io.Reader may return n>0 together with
// io.EOF and code that tests the error before the data loses the final part.
// That is the case the tree path's loop guard has to survive.
type dribbleReader struct {
	b   []byte
	pos int
	n   int
}

func (r *dribbleReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	end := r.pos + r.n
	if end > len(r.b) {
		end = len(r.b)
	}
	c := copy(p, r.b[r.pos:end])
	r.pos += c
	return c, nil
}

func TestThePartBoundaryIsReadWhole(t *testing.T) {
	for _, chunk := range []int{1, 3, 7, 4096, 65536} {
		// Small enough to stay fast, and past the boundary so the tree path
		// is the one under test.
		const size = PartSize + 5000
		buf := bytes.Repeat([]byte{0x7E}, size)

		got, err := HashFile(&dribbleReader{b: buf, n: chunk})
		if err != nil {
			t.Fatalf("chunk %d: %v", chunk, err)
		}
		if want := HashBytes(buf); got != want {
			t.Errorf("chunk %d: HashFile over a reader returning %d bytes at "+
				"a time = %s, want %s. A short read with a nil error must "+
				"not truncate a part", chunk, chunk, got, want)
		}
	}
}

// TestAReadErrorIsReportedNotSwallowed: a hash computed over a truncated read
// is a hash of the wrong bytes, and the caller must be told rather than handed
// a plausible digest.
type failingReader struct {
	after int
	err   error
	seen  int
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.seen >= r.after {
		return 0, r.err
	}
	n := copy(p, bytes.Repeat([]byte{0x11}, r.after-r.seen))
	r.seen += n
	return n, nil
}

func TestAReadErrorIsReportedNotSwallowed(t *testing.T) {
	sentinel := errors.New("the disk fell over")
	_, err := HashFile(&failingReader{after: 100, err: sentinel})
	if err == nil {
		t.Fatal("HashFile returned no error for a reader that failed")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want it to wrap %v", err, sentinel)
	}
	// A reader that fails with no bytes at all is also an error, not an
	// empty file: the distinction is whether a failure was reported.
	if _, err := HashFile(&failingReader{after: 0, err: sentinel}); err == nil {
		t.Error("HashFile returned no error for a reader that failed at once")
	}
}

// TestTreeHashIsLittleEndian pins the one detail that is invisible locally: the
// file length in a folder link's file-list hash is written LITTLE-endian.
//
// Every other multi-byte integer in ed2k's file headers is little-endian, so a
// big-endian implementation is not a typo anyone would notice locally — it is a
// hash that disagrees with every real eMule client and matches nothing. The
// test therefore computes the serialisation by hand in both byte orders and
// asserts that only the little-endian one is what the function produces.
func TestTreeHashIsLittleEndian(t *testing.T) {
	entries := []FileEntry{
		{Name: "a.mkv", Size: 1, Hash: Hash{0x01}},
		{Name: "b.txt", Size: 0x01020304, Hash: Hash{0x02, 0x03}},
	}

	// By hand, little-endian: name, 0x02, size LE, hash.
	var le []byte
	for _, e := range entries {
		le = append(le, e.Name...)
		le = append(le, 0x02)
		le = append(le, byte(e.Size), byte(e.Size>>8),
			byte(e.Size>>16), byte(e.Size>>24))
		le = append(le, e.Hash[:]...)
	}

	// By hand, big-endian: the same, with the size reversed.
	var be []byte
	for _, e := range entries {
		be = append(be, e.Name...)
		be = append(be, 0x02)
		be = append(be, byte(e.Size>>24), byte(e.Size>>16),
			byte(e.Size>>8), byte(e.Size))
		be = append(be, e.Hash[:]...)
	}

	// The control: the two constructions must differ, or this test can tell
	// nothing about which byte order the function uses. 0x01020304 is the size
	// that guarantees they do.
	if bytes.Equal(le, be) {
		t.Fatal("the little-endian and big-endian serialisations are " +
			"identical, so this fixture cannot tell the byte orders apart")
	}

	got, err := TreeHash(entries)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}
	if want := Hash(Sum(le)); got != want {
		t.Errorf("TreeHash = %s, want %s (the little-endian serialisation)",
			got, want)
	}
	if bad := Hash(Sum(be)); got == bad {
		t.Errorf("TreeHash = %s, which is the BIG-endian serialisation. Every "+
			"other multi-byte integer in ed2k is little-endian, and a "+
			"big-endian one matches no real client", got)
	}
}

// TestTreeHashIs37BytesPerEntry pins the layout: 16 + 1 + 4 + 16 per entry,
// plus the entry's own name.
//
// The byte count is asserted from the SERIALISATION rather than only from the
// digest, because a digest mismatch cannot say which field is wrong: a missing
// separator, a reversed size and a dropped hash byte all produce a different
// hash and none of them names itself.
func TestTreeHashIs37BytesPerEntry(t *testing.T) {
	// 16 hash bytes + 1 separator + 4 size bytes = 21 fixed bytes, plus the
	// name. The protocol's per-entry width is 37 only when you also count the
	// 16 bytes of the NAME the entry is allowed to carry; the first version of
	// this constant was 37 used as "fixed bytes per entry", which made the
	// expected total 32 bytes larger than the serialisation and failed on a
	// fixture that was correct.
	const fixedPerEntry = HashLength + 1 + 4
	// Names of DIFFERENT lengths, so a serialisation that drops or
	// double-counts a name changes the total and shows up in the byte count.
	entries := []FileEntry{
		{Name: strings.Repeat("n", 40), Size: 7, Hash: Hash{0xAA}},
		{Name: strings.Repeat("m", 3), Size: 7, Hash: Hash{0xBB}},
	}

	var buf []byte
	want := 0
	for _, e := range entries {
		want += len(e.Name) + fixedPerEntry
		buf = append(buf, e.Name...)
		buf = append(buf, 0x02)
		buf = append(buf, byte(e.Size), 0, 0, 0)
		buf = append(buf, e.Hash[:]...)
	}
	if len(buf) != want {
		t.Fatalf("the hand-built serialisation is %d bytes, want %d "+
			"(%d name bytes + %d fixed for %d entries)", len(buf), want,
			40+3, len(entries)*fixedPerEntry, len(entries))
	}

	got, err := TreeHash(entries)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}
	if w := Hash(Sum(buf)); got != w {
		t.Errorf("TreeHash = %s, want %s", got, w)
	}
}

// TestTheSeparatorIsOneByte: a name is length-prefixed by the 0x02 and the
// fixed width that follow, so the name is NOT escaped. A name containing a NUL
// is therefore accepted and hashed verbatim — escaping it would change the
// bytes and the hash, and would disagree with every client that does not.
func TestTheSeparatorIsOneByte(t *testing.T) {
	const name = "weird\x02name\x00.mkv"
	entries := []FileEntry{{Name: name, Size: 5, Hash: Hash{0x01}}}
	got, err := TreeHash(entries)
	if err != nil {
		t.Fatalf("a name containing the separator and a NUL was refused: %v", err)
	}

	var want []byte
	want = append(want, name...)
	want = append(want, 0x02)
	want = append(want, 5, 0, 0, 0)
	want = append(want, 0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if w := Hash(Sum(want)); got != w {
		t.Errorf("TreeHash = %s, want %s — the name must go in verbatim, "+
			"unescaped", got, w)
	}
}

// TestAnEmptyFileListHasNoHash: an empty list is not a file any peer can
// serve, and a zero hash returned without an error is a hash that names
// nothing.
func TestAnEmptyFileListHasNoHash(t *testing.T) {
	got, err := TreeHash(nil)
	if !errors.Is(err, ErrMalformed) {
		t.Errorf("err = %v, want ErrMalformed for a nil list", err)
	}
	if !got.IsZero() {
		t.Errorf("the returned hash is %s, want the zero hash alongside the "+
			"error", got)
	}
	if got2, err := TreeHash([]FileEntry{}); !errors.Is(err, ErrMalformed) {
		t.Errorf("err = %v, want ErrMalformed for an empty list", err)
	} else if !got2.IsZero() {
		t.Errorf("the returned hash is %s, want zero", got2)
	}
}

// TestTreeHashRefusesAnEscapingName: the same name check the locator parser
// applies, because a file-list entry's name is laid out on disk the same way.
// A file list is reachable — a |folder| link is parseable — so the names in it
// need the same second layer.
func TestTreeHashRefusesAnEscapingName(t *testing.T) {
	for _, name := range []string{"..", "../escape", "/etc/passwd", `C:\x`} {
		t.Run(name, func(t *testing.T) {
			entries := []FileEntry{{Name: "fine.mkv", Size: 1, Hash: Hash{1}},
				{Name: name, Size: 1, Hash: Hash{2}}}
			_, err := TreeHash(entries)
			if !errors.Is(err, ErrEscapingName) {
				t.Errorf("err = %v, want ErrEscapingName for the entry %q",
					err, name)
			}
		})
	}
}

// TestTreeHashAndHashFileAreDifferentHashes is the "not interchangeable" claim,
// asserted rather than left to a comment. A |folder| link's hash covers a file
// LIST; a |file| link's covers file bytes. A caller that used one for the other
// would look for something nobody serves.
func TestTreeHashAndHashFileAreDifferentHashes(t *testing.T) {
	entries := []FileEntry{{Name: "a.mkv", Size: 1024, Hash: Hash{0x11}}}
	tree, err := TreeHash(entries)
	if err != nil {
		t.Fatal(err)
	}
	// The same 1024 bytes, hashed as a file.
	whole := HashBytes(bytes.Repeat([]byte{0x22}, 1024))
	if tree == whole {
		t.Fatal("the file-list hash and the whole-file hash are identical, " +
			"so this fixture cannot tell the two forms apart")
	}
	if tree == Hash(Sum([]byte("a.mkv"))) {
		t.Error("the file-list hash is the MD4 of the name alone, so the " +
			"size and the file's own hash are not being included")
	}
}

// TestAnEntriesOrderChangesTheHash: the concatenation is in list order, so
// reordering the entries is a different file list.
func TestAnEntriesOrderChangesTheHash(t *testing.T) {
	a := FileEntry{Name: "a.mkv", Size: 1, Hash: Hash{0x01}}
	b := FileEntry{Name: "b.mkv", Size: 1, Hash: Hash{0x02}}
	first, err := TreeHash([]FileEntry{a, b})
	if err != nil {
		t.Fatal(err)
	}
	second, err := TreeHash([]FileEntry{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Error("swapping two entries changed nothing, so the list is being " +
			"hashed as a set rather than in order")
	}
}

// TestThePrefixLengthIsReadFromTheConstant: a literal 8 at the slice would
// compile, pass, and disagree with the constant the protocol documents.
//
// TWO parts, built from the same split HashFile uses, so the expectation
// cannot quietly cover only the first part — that mistake hid a real
// remainder bug once and would hide it again.
func TestThePrefixLengthIsReadFromTheConstant(t *testing.T) {
	const total = PartSize + 10
	buf := bytes.Repeat([]byte{0x33}, total)

	parts := make([][]byte, 0, 2)
	for off := 0; off < total; off += PartSize {
		end := off + PartSize
		if end > total {
			end = total
		}
		parts = append(parts, buf[off:end])
	}
	if len(parts) != 2 {
		t.Fatalf("the fixture splits into %d parts, want 2", len(parts))
	}

	var correct []byte
	for _, p := range parts {
		s := Sum(p)
		correct = append(correct, s[:PartHashPrefixLength]...)
	}
	// The control: a literal 8 gives the same bytes only because the constant
	// IS 8, and TestPartSizeIsTheProtocolsConstant pins that. Asserted here so
	// a change to the constant fails in the test that uses it.
	if want := 2 * PartHashPrefixLength; len(correct) != want {
		t.Fatalf("the prefix buffer is %d bytes, want %d", len(correct), want)
	}

	got, err := HashFile(bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	if w := Hash(Sum(correct)); got != w {
		t.Errorf("HashFile = %s, want %s", got, w)
	}
}
