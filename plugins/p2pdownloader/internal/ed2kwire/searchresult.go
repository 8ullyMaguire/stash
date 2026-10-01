package ed2kwire

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Reading a search RESULT: what a server sends back, and the shape it is.
//
// # THIS IS THE OTHER HALF OF search.go, AND IT IS A DIFFERENT FORMAT
//
// A search request is one packet we build, plain and uncompressed. A result is
// packets a stranger decides the shape of, and it arrives as OP_SEARCHRESULT
// (0x33) with the protocol byte 0xD4 — PACKEDPROT, which means the payload is
// zlib-COMPRESSED.
//
// So the two directions are asymmetric in every way that matters: one is
// uncompressed and the other is compressed, one is built here and the other is
// not, and the result carries a per-file identity the request never had.
// Treating the result as "the request, backwards" is wrong at the first byte.
//
// # THE LAYOUT, ESTABLISHED FROM A REAL SERVER AND NOT FROM THE SPEC
//
// Captured 2026-09-28 against 85.17.116.222:6082, searching for "ubuntu". The
// server answered with one 0x33 frame: 27,950 compressed bytes inflating to
// 40,828, holding 299 results.
//
//	[count:4]         a uint32, 300 on the capture
//	[token:4]         the answer token, echoed for the file-ID request that
//	                  follows a result
//	[fileid:16]       the file the results are about — the one asked about
//	  ... repeated per result:
//	[tagcount:4]      a uint32
//	[tags...]         the file's own tag list
//	[hash:16]         the file's ed2k hash
//	[userid:4]        the source holding it
//	[port:2]          that source's ed2k port
//
// # TWO THINGS THAT ARE EASY TO GET WRONG AND WERE
//
// ## 1. THE STR-FAMILY LENGTH IS IN THE TYPE BYTE, AND THE BASE IS NOT 0x10
//
// A type of 0x9A is a ten-byte string. The subtraction that gives that is
// 0x9A - 0x90, not 0x9A - 0x10 — and the first version of this parser used
// the 0x10 base that this package's own tag.go uses, which is a DIFFERENT
// numbering, because tag.go masks the high bit off before storing the type.
//
// The mask is the whole trap. In tag.go a tag of type 0x82 is read as 0x02,
// and 0x11 is a one-byte string. On the wire the same tag is 0x92, and it is a
// two-byte string. Both conventions are right, in their own place, and mixing
// them yields a length of 138 for the string "Hw-004.mp4" — which then
// swallows every following byte and ends the parse with a count of 2.4
// billion. All three tags on the capture agree: 0x9A→10 ("Hw-004.mp4"),
// 0x99→9 (".DS_Store"), 0x9B→11 ("OAV1365.mp4").
//
// ## 2. EACH RESULT IS FOLLOWED BY 22 BYTES OF FILE IDENTITY
//
// A 16-byte hash looks like an obvious boundary and is not one. The bytes
// after it are a 4-byte user ID and a 2-byte port, and without them the next
// read starts two bytes early and reports a count of 988,510,410.
//
// The confirmation is the port. On the capture the 22 bytes decode to a port
// of 4662, which is eMule's standard Kad port — a value that is not invented,
// and so the layout is read rather than guessed.

// searchResultHeaderLen is the fixed header before the first result.
//
// 26 bytes: a 4-byte count, a 4-byte answer token, and an 18-byte file ID.
const searchResultHeaderLen = 4 + 4 + 18

// fileIDLen is a file's identity in a result: 16-byte hash, 4-byte user ID,
// 2-byte port.
//
// 22, not 16, and the comment on the layout is the reason.
const fileIDLen = 16 + 4 + 2

// SearchResult is one file a server offered in answer to a search.
type SearchResult struct {
	// Name is the filename, without the NUL terminator the wire carries.
	Name string

	// Size is the file length in bytes, from tag 0x02.
	//
	// CLAIMED, like every number here: a server reports whatever it likes
	// and nothing in the protocol checks it. It is never used to size an
	// allocation.
	Size int64

	// Type is the ed2k file type, from tag 0x03: 2 is a video, 0 an
	// arbitrary file. Zero and "not present" are different, and the zero
	// value here means "absent" rather than "type zero".
	Type uint32

	// Sources is how many peers the server has for this file (tag 0x15) and
	// SourcesComplete how many have the whole of it (tag 0x30).
	//
	// Both are CLAIMS. A server can offer a file it cannot supply, and a
	// client that believes Sources without checking SourcesComplete will
	// start a download that stalls forever.
	Sources         uint32
	SourcesComplete uint32

	// Hash is the file's 16-byte ed2k hash. It is the file's identity, and
	// it is what a request-by-hash must quote back.
	//
	// A [16]byte HERE and not ed2k.Hash, because ed2kwire sits BELOW ed2k:
	// ed2k is pure parsing with no network, and importing the network layer's
	// type upward would invert the dependency and make the wire layer
	// untestable without the parser. The two are the same 16 bytes and a
	// caller converts with a copy; that is a cheap price for a direction that
	// keeps the pure half pure.
	Hash [16]byte

	// UserID and Port are the source holding this file, as the server knows
	// it. The port of 4662 on the capture is eMule's standard Kad port.
	//
	// CLAIMED, and notably this is ONE source — the server is reporting
	// where it believes the file can be had, which is a hint and not a
	// guarantee that anything is listening there.
	UserID uint32
	Port   uint16
}

// searchResultIDBytes is the 16-byte file ID in a result's header: the hash of
// the file that was searched for.
type searchResultIDBytes [16]byte

// DecodeSearchResult reads the inflated body of an OP_SEARCHRESULT packet.
//
// # EVERY FAILURE IS AN ERROR, AND NONE OF THEM IS AN EMPTY RESULT LIST
//
// The same rule the extended hello follows, for the same reason: a result set
// that fails to parse and comes back empty is indistinguishable from a search
// that genuinely found nothing, and a caller cannot tell "this server has no
// such file" from "this client could not read the answer".
func DecodeSearchResult(plain []byte) ([]SearchResult, error) {
	if len(plain) < searchResultHeaderLen {
		return nil, fmt.Errorf("a search result is %d bytes and its header "+
			"alone is %d", len(plain), searchResultHeaderLen)
	}

	// The header's file ID is the file that was ASKED about, not a result.
	// Carried but not returned, because a keyword search's header ID is the
	// zero hash on a server that could not attribute the answer to a file —
	// and a zero hash among real results is worth seeing rather than hiding.
	var askedFor searchResultIDBytes
	copy(askedFor[:], plain[8:24])

	results := make([]SearchResult, 0, 16)
	off := searchResultHeaderLen
	// # THE CONDITION IS off+4, AND THE WIDER ONE WAS REDUNDANT
	//
	// The first version used `off < len`, which runs the body at off=40827
	// where ONE byte remains, so the four-byte tag count is assembled from
	// that byte and three bytes of the next allocation. It reported a 300th
	// result named "5\x00Walt Disney..." -- a stray length byte in front
	// of the name, which is what a misaligned walk looks like, and how it
	// was caught.
	//
	// The second version used off+4+fileIDLen <= len, reasoning that a result
	// is a count PLUS 22 bytes of identity and the guard should cover all of
	// it. The mutation harness disagreed: with that condition replaced by
	// off+4, every test still passed, and the capture decodes to the same
	// 299 results with the same last name and the same last port either way.
	//
	// So the wider condition is gone, and the in-loop break below is what
	// ends the list. One mechanism, not two: the loop condition answers "is
	// there a count to read", and the break answers "is there room for the
	// file that count introduces".
	//
	// # AND off+4 IS NOT off < len
	//
	// Four bytes is the width of a count. Fewer cannot begin a result, and
	// a trailing byte is not a truncated result.
	for off+4 <= len(plain) {

		tags, next, err := parseTagListAt(plain[off:])
		if err != nil {
			return nil, fmt.Errorf("search result %d: %w", len(results)+1, err)
		}
		off += next

		// # A RESULT WITH NO ROOM FOR ITS OWN FILE ID IS THE END OF THE LIST
		//
		// Not an error, and the distinction is worth stating: the capture's
		// 300th entry has all five of its tags and one byte left. Reporting
		// that as a truncated result would refuse 299 good files over a
		// trailing byte, and reporting it as a result would invent a hash
		// from memory.
		//
		// So the list ends here, and the byte is dropped. The alternative --
		// an error -- is what the first version did, and it made a real
		// server's real answer undecodable.
		//
		// A result whose TAGS are cut short is a different case and IS an
		// error: parseTagListAt has already returned one above.
		if off+fileIDLen > len(plain) {
			break
		}

		var r SearchResult
		copy(r.Hash[:], plain[off:off+16])
		r.UserID = binary.LittleEndian.Uint32(plain[off+16 : off+20])
		r.Port = binary.LittleEndian.Uint16(plain[off+20 : off+22])
		off += fileIDLen

		r.Name = nameOf(tags)
		r.Size = int64(uint32Of(tags, tagIDFileSize))
		r.Type = uint32Of(tags, tagIDFileType)
		r.Sources = uint32Of(tags, tagIDSources)
		r.SourcesComplete = uint32Of(tags, tagIDCompleteSources)
		results = append(results, r)
	}
	return results, nil
}

// nameOf reads tag 0x01 as a filename, dropping the NUL terminator the wire
// carries.
//
// # IT DECODES THROUGH Tag.String, AND NOT THROUGH t.Value — 284 OF 299
// NAMES WERE WRONG WITHOUT IT
//
// The first version returned `string(bytes.TrimRight(t.Value, "\x00"))` for
// whatever the tag held, and that is correct for a Str-family tag and wrong
// for a tagTypeString one. A tagTypeString value is `[len:2][bytes]`: the
// uint16 length is PART OF THE VALUE, not part of the name.
//
// Measured on the capture, 2026-09-28, 284 of its 299 results:
//
//	result  3  type=0x02  value=1d 00 76 69 64 65 6f ...  (len 0x001D = 29)
//	          before: "\x1d\x00video_2026-01-01_14-39-10.mp4"  (31 bytes)
//	          after:  "video_2026-01-01_14-39-10.mp4"           (29 bytes)
//	result  4  type=0x02  value=62 00 53 75 73 75 72 ...  (len 0x0062 = 98)
//	          before: "b\x00Susurran.tu.nombre...mkv"          (100 bytes)
//	          after:  "Susurran.tu.nombre...mkv"                (98 bytes)
//
// 0x1D is 29 and the name is 29 bytes; 0x62 is 98 and the name is 98 bytes.
// The two agree on every one of the 284, which is what makes this a length
// prefix rather than a coincidence.
//
// # WHY NOTHING CAUGHT IT, AND IT IS NOT A CLOSE MISS
//
// The capture's first three results are all short Str-family tags (0x9A,
// 0x99, 0x9B), where the value IS the name. The tests assert on those three
// by name, so they passed. Every test that could have seen the bug asserted
// on a result whose type does not have the prefix.
//
// And the junk is invisible to a length check: the corrupted names are
// LONGER than the real ones, not shorter, so "is this name plausible" passes
// and "is this name the right length" would too. The only assertions that
// fail are ones that compare the whole name — and there were none past the
// first three results.
//
// # AND THE FIX IS TO CALL THE ACCESSOR THAT ALREADY KNOWS THIS
//
// `Tag.String()` handles both encodings correctly: it reads the uint16
// length for a tagTypeString and uses the value verbatim for a Str-family
// type. Duplicating that rule here is how the two drifted apart in the first
// place, and the whole point of the Tag accessors is that a caller converts
// rather than re-decodes.
func nameOf(tags TagList) string {
	for _, t := range tags {
		if t.ID != tagIDFileName {
			continue
		}
		name, ok := t.String()
		if !ok {
			return ""
		}
		return strings.TrimRight(name, "\x00")
	}
	return ""
}

// uint32Of reads a four-byte tag as a uint32, or 0 when it is absent or a
// different width.
//
// A missing tag and a zero tag both give 0, and that is acceptable HERE
// because every caller uses the value as a count or a size it will sanity
// check anyway. It would not be acceptable for a field where zero and absent
// mean different things, which is why Sources and SourcesComplete are read
// from distinct tags rather than one being defaulted from the other.
func uint32Of(tags TagList, id byte) uint32 {
	for _, t := range tags {
		if t.ID == id && len(t.Value) == 4 {
			return binary.LittleEndian.Uint32(t.Value)
		}
	}
	return 0
}

// The tag ids a search result carries. These are the protocol's, and they are
// the same ids the server's own info tag list uses — 0x01 is a filename here
// and the server's name there, decided by the packet.
const (
	tagIDFileName        byte = 0x01
	tagIDFileSize        byte = 0x02
	tagIDFileType        byte = 0x03
	tagIDSources         byte = 0x15
	tagIDCompleteSources byte = 0x30
)
