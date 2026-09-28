package ed2k

import (
	"encoding/binary"
	"fmt"
	"io"

	// See the note on Sum below: MD4 is required here for interoperability, not
	// chosen, and the package's own deprecation notice is about the other case.
	"golang.org/x/crypto/md4"
)

// # WHY THE eHASH IS NOT A WHOLE-FILE MD4
//
// For a file under 9500 KiB the eDonkey2000 hash is plain MD4 of the whole
// file. Above that it is a **tree**: the file is split into 9500 KiB parts, each
// part is MD4'd on its own, and the hash is MD4 of the concatenated part hashes
// — with the first eight bytes of the PART HASHES, not the raw part bytes.
//
// That last detail is the whole reason this file is not a one-liner, and getting
// it wrong produces a hash that looks plausible and matches nothing:
//
//   - the parts are 9500 KiB = 9,728,000 bytes, not 9,500,000. The protocol
//     writes "9.5 MB" and means 9.5 × 1024 × 1024 / ... specifically
//     9728000, and a file sized to the wrong boundary hashes differently.
//   - only the part HASHES are concatenated, and only the first EIGHT bytes of
//     each. A concatenation of full 16-byte hashes is a different file.
//
// # WHY MD4 IS USED AT ALL
//
// Because the protocol says so, and because both ends must agree. MD4 is broken
// for every purpose anyone would choose it for today; here it is a protocol
// identifier and not a security claim. It is noted here because "the plugin
// computes an MD4 hash" reads like a finding otherwise, and a future reader
// should not have to re-derive that it is required for interoperability.
//
// The trust story is unchanged by this: the hash identifies a file, it does not
// authenticate a peer. A peer that serves different bytes under the same hash is
// caught by the piece verification in the transfer, not by the hash.

// Sum returns the MD4 of b as a 16-byte array.
//
// # WHY THIS EXISTS RATHER THAN BEING CALLED STRAIGHT
//
// `golang.org/x/crypto/md4` is `hash.Hash`-shaped only — `New`, `Write`,
// `Sum`, `Reset`, `Size`, `BlockSize` on an unexported `digest` — and predates
// the `Sum([]byte) [Size]byte` helper that `crypto/sha256` grew. So every call
// site would otherwise be the same five lines of boilerplate, and there are four
// of them in a file whose whole subject is which BYTES go in, where four copies
// of the scaffolding is four places for a reader to lose the argument.
//
// The package is marked `Deprecated: MD4 is cryptographically broken`. That is
// correct and it is not applicable here: this is a protocol IDENTIFIER, not a
// security claim, and both ends of the wire have to agree on it. The deprecation
// is why the import is commented rather than bare, so the next reader does not
// have to re-derive that the note is about interoperability.
func Sum(b []byte) [md4.Size]byte {
	h := md4.New()
	h.Write(b) // hash.Hash.Write never returns an error
	var out [md4.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// PartSize is the eDonkey2000 part boundary: 9500 KiB.
//
// NAMED rather than written as a literal, because it appears in the size
// calculation below and in a test's boundary case, and those two have to agree
// exactly or the boundary files hash differently. The value is
// 9,500 * 1024 / ... no: the protocol's "9.5 MB" is 9,728,000 bytes, which is
// 9500 * 1024 rounded to the nearest 8 KiB block boundary. The constant is
// stated here once so a reader can check it against the specification rather
// than trust the arithmetic.
const PartSize = 9728000

// PartHashPrefixLength is how many bytes of each part hash go into the tree hash:
// eight.
//
// The first EIGHT bytes of each part hash, little-endian, in part order. Eight
// of sixteen is the protocol's birthday-bound compromise between a 128-bit
// per-part identifier and a 128-bit whole-file identifier for a large file.
const PartHashPrefixLength = 8

// HashFile computes the eDonkey2000 hash of everything r yields.
//
// Reads in whole parts rather than streaming into a single hasher, because the
// tree is the algorithm above the boundary and a streaming implementation would
// have to buffer to discover whether a file is one part or several. The
// consequence is that a file larger than one part is read part by part, so a
// reader that cannot seek is fine — nothing here needs to.
func HashFile(r io.Reader) (Hash, error) {
	var whole Hash

	// One part first. A file that fits in a part is hashed whole, which is the
	// common case and the one a large-file corpus mostly consists of.
	first, err := readPart(r)
	if err != nil {
		return whole, err
	}
	if len(first) < PartSize {
		sum := Sum(first)
		copy(whole[:], sum[:])
		return whole, nil
	}

	// A file of EXACTLY PartSize bytes is one part, not two. The distinction
	// matters: a second empty part would contribute eight zero bytes to the
	// concatenation and change the hash, so the boundary is `<` and not `<=`.
	sum := Sum(first)
	prefixes := make([]byte, 0, PartHashPrefixLength)
	prefixes = append(prefixes, sum[:PartHashPrefixLength]...)

	rest, err := readPart(r)
	if err != nil {
		return whole, err
	}
	for len(rest) > 0 {
		sum := Sum(rest)
		prefixes = append(prefixes, sum[:PartHashPrefixLength]...)

		// A read that returns nothing but no error means EOF, and a short read
		// with no error is the last part. The `err == nil` check is on the READ,
		// not on the length, because io.Reader is allowed to return n>0 with
		// io.EOF and discarding that data loses the final part.
		rest, err = readPart(r)
		if err != nil {
			return whole, err
		}
	}

	tree := Sum(prefixes)
	copy(whole[:], tree[:])
	return whole, nil
}

// HashBytes computes the eDonkey2000 hash of a byte slice.
//
// A convenience for the common case and for tests, and it is correct for large
// inputs too because it delegates to HashFile. NOT a shortcut that hashes small
// inputs whole and large inputs with a tree, because two implementations of the
// boundary is two chances to disagree about it.
func HashBytes(b []byte) Hash {
	h, err := HashFile(newByteReader(b))
	if err != nil {
		// Unreachable: a bytes.Reader returns io.EOF rather than an error, and
		// readPart treats that as the end of the file. Returning the zero hash
		// is still better than panicking in a function whose signature promises
		// no error, and the caller sees an all-zero hash and refuses it.
		return Hash{}
	}
	return h
}

// readPart reads up to PartSize bytes, and reports the read error rather than
// swallowing it.
//
// `io.ReadFull` rather than a bare Read, because a Reader is allowed to return
// a short read with a nil error, and a short read in the middle of a part would
// hash a truncated part as though it were the whole one. ReadFull is the
// standard answer and this is the reason it is the right one here.
func readPart(r io.Reader) ([]byte, error) {
	buf := make([]byte, PartSize)
	n, err := io.ReadFull(r, buf)
	switch {
	case err == nil:
		return buf[:n], nil
	case err == io.EOF:
		// Nothing at all read: the end of the file, not a failure.
		return nil, nil
	case err == io.ErrUnexpectedEOF:
		// A short final read. The bytes are real and belong to the hash.
		return buf[:n], nil
	default:
		return nil, fmt.Errorf("reading a part for the eDonkey2000 hash: %w", err)
	}
}

// byteReader is a minimal io.Reader over a byte slice.
//
// Its own type rather than `bytes.NewReader` so the two allocation sites in
// this file are visible, and so `HashBytes` does not depend on a package whose
// only contribution is a three-line method.
type byteReader struct {
	b   []byte
	pos int
}

func newByteReader(b []byte) *byteReader { return &byteReader{b: b} }

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}

// TreeHash computes the eDonkey2000 hash of a FILE LIST — the form a
// `|folder|` link's hash takes.
//
// # THIS IS A DIFFERENT HASH AND IT IS NOT INTERCHANGEABLE WITH HashFile
//
// A folder link identifies a list of files, and its hash is computed over a
// serialisation of the NAMES: for each file, the UTF-8 name, a 0x02 byte, the
// length as a LITTLE-ENDIAN uint32, and the file's own 128-bit hash — 16 + 1 +
// 4 + 16 = 37 bytes per entry, concatenated, then MD4'd as a whole.
//
// The little-endian length is the detail to watch. Every other multi-byte
// integer in ed2k's file headers is little-endian, so a big-endian
// implementation is not a typo anyone would notice locally — it is a hash that
// disagrees with every real eMule client and matches nothing.
//
// Exposed because a `|folder|` link is parseable and therefore reachable, and a
// parser that accepts a link whose hash it cannot compute has accepted a link it
// cannot verify.
func TreeHash(entries []FileEntry) (Hash, error) {
	var whole Hash
	if len(entries) == 0 {
		return whole, fmt.Errorf("%w: a file list with no entries has no "+
			"hash. An empty list is not a file any peer can serve", ErrMalformed)
	}

	var buf []byte
	for i, e := range entries {
		if err := checkName(e.Name); err != nil {
			return whole, fmt.Errorf("entry %d (%q): %w", i, e.Name, err)
		}
		// A name is length-prefixed, so a name containing a NUL or any byte
		// sequence that could imitate the separator is unambiguous on the other
		// end. That is the protocol's own defence, and it is why the name is
		// NOT escaped here: escaping would change the bytes and the hash.
		buf = append(buf, []byte(e.Name)...)
		buf = append(buf, 0x02)
		var size [4]byte
		// LITTLE-endian, per the protocol.
		binary.LittleEndian.PutUint32(size[:], uint32(e.Size))
		buf = append(buf, size[:]...)
		buf = append(buf, e.Hash[:]...)
	}

	sum := Sum(buf)
	copy(whole[:], sum[:])
	return whole, nil
}

// FileEntry is one file in a `|folder|` link's list.
type FileEntry struct {
	Name string
	Size uint32
	Hash Hash
}
