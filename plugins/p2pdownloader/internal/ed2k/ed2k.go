// Package ed2k parses eDonkey2000/eMule locators and computes the hashes the
// protocol identifies files by.
//
// # WHY THIS IS SEPARATE FROM EVERYTHING ELSE IN THE PLUGIN
//
// This is pure code: no network, no disk, no clock. That is the whole reason it
// is its own package, and it is why it can be tested exhaustively while the rest
// of the protocol work cannot be. A locator arriving from a stranger's database
// is untrusted input, and the only way to be sure about untrusted input is to
// enumerate it.
//
// The locator arrives in the path the plugin already validates, before any
// consent question is asked — a malformed ed2k link is refused locally because
// asking core's gate about `ed2k://file|../../etc/passwd|0|0|0|` spends an
// operator's trust on a question with an obvious answer.
//
// # THE eDONKEY2000 LINK GRAMMAR
//
//	ed2k://|file|<name>|<size>|<hash>|...
//
// The first field is `file` for a single file and `folder` for a multi-file
// link, where the hash is of the FILE LIST rather than of any file. Both are
// handled, and the distinction is not cosmetic: a folder link's hash is
// meaningless to anything that expects a file hash, so a caller that ignores it
// will look for a file that does not exist.
//
// Pipe-delimited and positional: `file` is the first field, the name the
// second, the size the third and the hash the fourth. A name containing a pipe
// makes the link ambiguous and is refused rather than guessed at — see
// `parse.go`, where the reasoning is written out.
//
// # WHY THE HASH IS 128 BITS AND THE SIZE IS REQUIRED
//
// eDonkey2000 identifies a file by (MD4 of the file, length). Both halves matter:
// MD4 alone collides across files that differ only past a shared prefix's
// digest input, and the length disambiguates. A link carrying a size of 0 is
// refused rather than accepted, because a zero length with a valid hash is the
// shape a truncated or hostile link takes, and there is no file of length zero
// in this protocol worth fetching.
package ed2k

// Locator is a parsed ed2k link.
type Locator struct {
	// Kind is KindFile or KindFolder. A folder link identifies a FILE LIST.
	Kind Kind

	// Name is the file name, or the folder's display name. Whatever sat between
	// the pipes -- NOT percent-decoded, because classic eMule links carry raw
	// names, and decoding a name that was never encoded turns a valid file into
	// a 404.
	Name string

	// Size is the file length in bytes. For a folder link it is the total size
	// of the list, which is informational rather than identifying.
	Size int64

	// Hash is the 16-byte eDonkey2000 hash: MD4 over the whole file.
	Hash Hash
}

// Kind distinguishes the two link forms.
//
// A closed set rather than a bool, because a bool named `isFolder` has no
// uninformative value: a caller that never set it has said "not a folder", and
// that is indistinguishable from a deliberate choice.
type Kind int

const (
	// KindFile is `|file|`, the ordinary case.
	KindFile Kind = iota
	// KindFolder is `|folder|`, where the hash covers a FILE LIST.
	KindFolder
)

func (k Kind) String() string {
	switch k {
	case KindFile:
		return "file"
	case KindFolder:
		return "folder"
	}
	return "unknown"
}

// HashLength is the byte length of an eDonkey2000 hash: 128 bits.
//
// A CONSTANT rather than a literal at each use, because the two places that
// matter — the parser's length check and the printer's width — have to agree, and
// a link whose hash parses at one width and prints at another is a link that
// round-trips into something the protocol rejects.
const HashLength = 16

// Hash is a 128-bit eDonkey2000 file hash.
//
// An array rather than a string, so a hash cannot be a wrong length: the type
// makes "32 hex characters" unrepresentable, and a link's hash arrives as text
// that has to be decoded exactly once.
type Hash [HashLength]byte

// String renders the hash as the 32 lowercase hex characters eMule uses.
//
// Lowercase, and that is the protocol's form rather than a preference: a peer
// that compares hash STRINGS treats an uppercase one as a different file, so a
// printer that emitted uppercase would produce links that do not resolve.
func (h Hash) String() string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 2*HashLength)
	for i, b := range h {
		out[2*i] = hexdigits[b>>4]
		out[2*i+1] = hexdigits[b&0x0f]
	}
	return string(out)
}

// IsZero reports whether the hash is all zeroes.
//
// The all-zero hash is what a link with a missing or malformed hash field
// decodes to, so this is how a caller distinguishes "the link carried a hash" from
// "the link's hash field was empty". A torrent infohash has the same property,
// and `magnet.go` refuses a zero hash for the same reason.
func (h Hash) IsZero() bool {
	for _, b := range h {
		if b != 0 {
			return false
		}
	}
	return true
}
