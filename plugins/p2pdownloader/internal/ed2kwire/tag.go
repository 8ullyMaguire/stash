package ed2kwire

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// # ed2k TAG ENCODING
//
// # IT IS NOT THE ORDER THE LIBRARY'S WRITER USES, AND GETTING IT WRONG IS
// # SILENCE
//
// The library's `SimpleTag.Put` writes `[type | 0x80][id][value]`. A live
// ed2k server writes `[type][id][value]`. Every tag produced by the library's
// writer is silently dropped by a real server, so a login request carrying one
// gets no answer at all — measured on 2026-09-28: a login with no tags gets
// "ERROR : Your edonkey client is too old", and the same login with one
// library-encoded tag gets **nothing**.
//
// The high bit on the type byte is what the two formats share, and it is
// where the confusion comes from: in the library's format it marks "this tag
// has a numeric ID", in the wire format it is simply set on every tag. The
// decoder below reads the wire format and is exercised by a golden test
// carrying bytes captured from a live server, because a decoder written from
// the library's writer would pass every test and fail against the network.

// ed2k tag types, from the wire.
//
// The names are the protocol's, not the library's — the library calls 0x11
// TagTypeStr1 and treats 0x11..0x20 as an unbounded string, while on the wire
// the type byte of a Str-family tag CARRIES ITS LENGTH. That difference is
// load-bearing: `0x19` is not "a string" but "a string of 9 bytes", and
// reading it as unbounded runs the parser into the next tag.
const (
	tagTypeBool   byte = 0x01
	tagTypeString byte = 0x02
	tagTypeUint32 byte = 0x03
	tagTypeUint16 byte = 0x08
	tagTypeUint8  byte = 0x09
	tagTypeUint64 byte = 0x0B

	// tagTypeStrBase is the base of the length-carrying string types. A tag
	// whose type is strBase+n is a string of exactly n bytes.
	//
	// The base is 0x10, so the first Str tag is type 0x11 for a ONE-byte
	// string. The live server's 0x19 is therefore a nine-byte string, and
	// the three printable runs in its tag list are 0x19, 0x1B and 0x14 --
	// nine, eleven and four bytes, matching "ed2k-rust", "main server" and
	// "18.1" exactly. That agreement across three different lengths is what
	// settled the encoding.
	tagTypeStrBase byte = 0x10

	// tagTypeStrMax is the largest length a Str-family tag can carry, so a
	// type byte of tagTypeStrMax is a sixteen-byte string. A longer string
	// uses tagTypeString with a uint16 length instead.
	//
	// # THIS BOUND IS INCLUSIVE, AND GETTING IT EXCLUSIVE IS SUBTLE
	//
	// The first version compared against tagTypeStrMax as an exclusive
	// limit, which made it equal tagTypeStrBase, which excluded every Str
	// tag in existence. The tests caught it on the first live packet and
	// the failure was a clean "this client does not model" on 0x19 -- the
	// nine-byte string the packet was built around. A constant that is both
	// the base and the limit is worth stating as inclusive rather than
	// leaving to a reader to infer from the comparison.
	tagTypeStrMax byte = 0x20
)

// Tag is one ed2k tag: an id, a wire type, and the value.
//
// The value is kept as a byte slice rather than as four typed fields. A tag
// list arrives from a stranger and a union with one field per type is a
// decision this package has not earned: it would mean deciding which types to
// model before knowing which ones a server actually sends. The typed accessors
// are conversion, not validation, and Say reports what is wrong rather than
// returning a zero that reads as a real value.
type Tag struct {
	// ID is the tag's numeric name. eMule assigns the well-known ones
	// meanings: 0x01 the client version string, 0x83 the connection limits,
	// 0x99 the server name, 0x0B the server description.
	ID byte

	// Type is the raw wire type byte, with the high bit masked off. The
	// masked bit is not decoration: it is what distinguishes the two
	// tag-encoding conventions this package has had to handle, and dropping
	// it silently turns one of them into the other.
	Type byte

	// Value is the raw bytes of the value, exactly as they arrived.
	Value []byte
}

// String returns the value as text, and whether the tag is a string at all.
//
// A false second return is not an error condition to be lenient about: it
// means the caller asked the wrong question of this tag, and returning the
// bytes anyway would produce a number printed as if it were a name.
func (t Tag) String() (string, bool) {
	switch {
	case t.Type == tagTypeString:
		// A uint16 length, then that many bytes.
		if len(t.Value) < 2 {
			return "", false
		}
		n := int(binary.LittleEndian.Uint16(t.Value[:2]))
		if n > len(t.Value)-2 {
			return "", false
		}
		return string(t.Value[2 : 2+n]), true
	case isStringType(t.Type):
		// A Str-family tag's length is in its type byte, so the value is
		// exactly what is there.
		return string(t.Value), true
	default:
		return "", false
	}
}

// Uint32 returns the value as a uint32, and whether the tag is a 32-bit
// integer.
//
// The counts a server reports here — users, files — are CLAIMS from a
// stranger. This accessor converts; it does not sanity-check, and a server
// that reports four billion users will hand back four billion.
func (t Tag) Uint32() (uint32, bool) {
	switch t.Type {
	case tagTypeUint32:
		if len(t.Value) < 4 {
			return 0, false
		}
		return binary.LittleEndian.Uint32(t.Value[:4]), true
	case tagTypeUint16:
		if len(t.Value) < 2 {
			return 0, false
		}
		return uint32(binary.LittleEndian.Uint16(t.Value[:2])), true
	case tagTypeUint8:
		if len(t.Value) < 1 {
			return 0, false
		}
		return uint32(t.Value[0]), true
	default:
		return 0, false
	}
}

// IsString reports whether the tag holds text, without decoding it. A caller
// that only needs to know whether a name tag is present — "does this server
// call itself anything?" — does not need the bytes.
func (t Tag) IsString() bool { return isStringType(t.Type) }

// isStringType reports whether a wire type byte denotes a string, and is the
// one place that question is asked. Both the decoder and the accessors go
// through it, because two answers to "is 0x19 a string" is one of them
// eventually being wrong.
func isStringType(t byte) bool {
	return t == tagTypeString || (t > tagTypeStrBase && t <= tagTypeStrMax)
}

// TagList is a decoded list of tags.
type TagList []Tag

// ByID returns the first tag with the given id, and whether one was found.
//
// First, not last and not an error on duplicates: a duplicate id in a tag list
// from a stranger is a server sending contradictory claims, and silently
// picking one hides that. A caller that cares can count them.
func (l TagList) ByID(id byte) (Tag, bool) {
	for _, t := range l {
		if t.ID == id {
			return t, true
		}
	}
	return Tag{}, false
}

// Uint32ByID returns a numeric tag's value, and whether a numeric tag with
// that id was present.
//
// A tag that is present but not numeric is reported as absent rather than as
// zero. That is deliberate: a count of zero and a count we failed to read are
// different facts, and a server that sends a name where a count belongs is
// telling us something we should not round to zero.
func (l TagList) Uint32ByID(id byte) (uint32, bool) {
	tag, ok := l.ByID(id)
	if !ok {
		return 0, false
	}
	return tag.Uint32()
}

// StringByID returns a text tag's value, and whether a text tag with that id
// was present.
func (l TagList) StringByID(id byte) (string, bool) {
	tag, ok := l.ByID(id)
	if !ok {
		return "", false
	}
	return tag.String()
}

// parseTagList decodes a tag list from a packet payload.
//
// # WHY THIS IS HAND-WRITTEN AND NOT protocol.TagList.Get
//
// Two reasons, both measured against a live server rather than assumed:
//
//  1. The library's reader is type-first, the wire is id-first, so it
//     misreads every tag. See the file comment.
//  2. The library's reader bounds the tag COUNT against the remaining bytes
//     using `tagMinBytes`, and then reads each tag with no further bound. A
//     Str-family tag whose type byte claims a length is unreadable by it, so
//     a real server's tag list cannot be decoded by it at all.
//
// The count is bounded here before any allocation, because it arrives from a
// stranger and a count of 4 billion is otherwise a 4-billion-element slice
// made before a single byte is read.
func parseTagList(payload []byte) (TagList, error) {
	if len(payload) < 4 {
		return nil, fmt.Errorf("a tag list needs a four-byte count and the "+
			"payload is %d bytes", len(payload))
	}
	count := binary.LittleEndian.Uint32(payload[:4])
	rest := payload[4:]

	// The smallest possible tag is two bytes: a type and an id, with a value
	// of nothing. So a count claiming more tags than there are bytes for is
	// a misparse, and this is checked BEFORE the slice is made.
	if int(count) > len(rest)/2 {
		return nil, fmt.Errorf("the tag list claims %d tags but the payload "+
			"has room for at most %d (the smallest tag is two bytes)",
			count, len(rest)/2)
	}

	tags := make(TagList, 0, count)
	for i := uint32(0); i < count; i++ {
		tag, n, err := parseTag(rest)
		if err != nil {
			return nil, fmt.Errorf("tag %d of %d: %w", i+1, count, err)
		}
		rest = rest[n:]
		tags = append(tags, tag)
	}
	return tags, nil
}

// parseTag decodes one tag and reports how many bytes it consumed.
func parseTag(payload []byte) (Tag, int, error) {
	if len(payload) < 2 {
		return Tag{}, 0, fmt.Errorf("a tag needs a type and an id and only "+
			"%d bytes remain", len(payload))
	}

	tag := Tag{
		Type: payload[0] & 0x7F,
		ID:   payload[1],
	}
	body := payload[2:]
	var value []byte

	switch {
	case tag.Type == tagTypeBool, tag.Type == tagTypeUint8:
		// One byte. A bool reads as 0 or 1 and a uint8 as any value; the
		// two are the same three bytes on the wire and differ only in what
		// the id means, so one accessor serves both.
		if len(body) < 1 {
			return Tag{}, 0, fmt.Errorf("tag 0x%02X claims a one-byte value "+
				"and none is left", tag.ID)
		}
		value = body[:1]

	case tag.Type == tagTypeUint16:
		if len(body) < 2 {
			return Tag{}, 0, fmt.Errorf("tag 0x%02X claims a two-byte value "+
				"and only %d bytes are left", tag.ID, len(body))
		}
		value = body[:2]

	case tag.Type == tagTypeUint32:
		if len(body) < 4 {
			return Tag{}, 0, fmt.Errorf("tag 0x%02X claims a four-byte value "+
				"and only %d bytes are left", tag.ID, len(body))
		}
		value = body[:4]

	case tag.Type == tagTypeUint64:
		if len(body) < 8 {
			return Tag{}, 0, fmt.Errorf("tag 0x%02X claims an eight-byte "+
				"value and only %d bytes are left", tag.ID, len(body))
		}
		value = body[:8]

	case tag.Type == tagTypeString:
		if len(body) < 2 {
			return Tag{}, 0, fmt.Errorf("tag 0x%02X claims a string with a "+
				"two-byte length and %d bytes are left", tag.ID, len(body))
		}
		n := int(binary.LittleEndian.Uint16(body[:2]))
		if n > len(body)-2 {
			return Tag{}, 0, fmt.Errorf("tag 0x%02X claims a %d-byte string "+
				"and only %d bytes are left", tag.ID, n, len(body)-2)
		}
		value = body[:2+n]

	case isStringType(tag.Type):
		// THE TYPE BYTE IS THE LENGTH. A Str-family tag needs no length of
		// its own because it is in the type. This is the detail that makes
		// a real server's tag list decodable at all, and the reason the
		// library's reader — which treats this range as an unbounded string
		// — cannot read one.
		n := int(tag.Type - tagTypeStrBase)
		if n > len(body) {
			return Tag{}, 0, fmt.Errorf("tag 0x%02X claims a %d-byte string "+
				"and only %d bytes are left", tag.ID, n, len(body))
		}
		value = body[:n]

	default:
		return Tag{}, 0, fmt.Errorf("tag 0x%02X has wire type 0x%02X, which "+
			"this client does not model. The list cannot be parsed past "+
			"it, because a tag of unknown width has no known width -- so "+
			"the tags after it are unreachable rather than merely "+
			"unrecognised", tag.ID, tag.Type)
	}

	// # THE VALUE IS ATTACHED HERE, AND OMITTING IT IS SILENT
	//
	// The first version of this function computed `value` correctly, used it
	// to report how many bytes the tag consumed, and then returned a Tag with
	// no Value set. Every parse succeeded and every accessor returned
	// nothing: `StringByID(0x01)` reported ok=true with an empty string,
	// because the tag WAS found and its value WAS absent.
	//
	// The consumed count was right, so the tag list walked the whole payload
	// correctly and the failure had no error message anywhere. A test that
	// only asserted "ten tags parsed" would have passed.
	tag.Value = value
	return tag, 2 + len(value), nil
}

// # THE WRITER, WHICH DID NOT EXIST UNTIL THE EXTENDED HELLO NEEDED ONE
//
// This file was decode-only for its whole life, which was fine until an
// extended hello had to be SENT: a capability list goes out compressed, and
// there was no way to put one in the wire format the decoder reads back.
//
// # THE TYPE BYTE IS NOT DECORATION
//
// The wire is [type|0x80][id][value] -- the high bit marks the name-carrying
// form -- and parseTag MASKS the high bit off before storing Type. So a Tag
// built by a caller has a masked Type, and writing it back must SET the bit
// again. Writing the masked value produces a tag that this package's own
// parser refuses ("tag without id"), which is the exact failure the mask
// comment in the Tag struct warns about.
//
// # STRINGS ARE LENGTH-PREFIXED IN THEIR TYPE BYTE
//
// A string tag's type byte is 0x02 (u16-length string) or 0x10+n for a string
// of n bytes, and the length is IN the type. A tag whose Value is longer than
// 15 bytes therefore cannot be a short string at all, and is written as 0x02
// with an explicit uint16 length.
func writeTag(w *bytes.Buffer, t Tag) error {
	wireType, err := wireTypeFor(t)
	if err != nil {
		return err
	}
	w.WriteByte(wireType | 0x80)
	w.WriteByte(t.ID)

	// # tagTypeString CARRIES ITS OWN LENGTH, AND OMITTING IT IS THE BUG
	//
	// Every other type's width is implied by the type byte, so writing the
	// value bare is correct for all of them. A length-prefixed string is the
	// exception: the reader takes a uint16 from the stream, and a tag written
	// without one makes the reader take the first two bytes of the VALUE as
	// a length.
	//
	// The symptom is a round trip that fails with a confident, specific and
	// wrong number -- "tag 0x01 claims a 12406-byte string and only 11
	// bytes are left" -- where 12406 is 0x306E, the first two bytes of
	// "v0.60a". The error names the tag parser and is not lying about what
	// it saw; it is the writer that is wrong, and nothing in the message
	// says so.
	if wireType == tagTypeString {
		var n [2]byte
		binary.LittleEndian.PutUint16(n[:], uint16(len(t.Value)))
		w.Write(n[:])
	}
	w.Write(t.Value)
	return nil
}

// wireTypeFor picks the wire type byte for a tag's value.
//
// A Uint32-shaped value is written as a uint32 and a String-shaped one as a
// string, because the alternative is a caller having to know the type byte
// for its own value. A caller that needs a specific type -- the server's own
// tag list uses several -- can set Type directly, and that is honoured.
func wireTypeFor(t Tag) (byte, error) {
	// An explicit type is the caller's choice and is used as given -- the
	// server's own tag list arrives with types this function has no reason to
	// second-guess, and a caller that needs one can set it.
	if t.Type >= tagTypeStrBase && t.Type <= tagTypeStrMax {
		return t.Type, nil
	}
	switch t.Type {
	case tagTypeBool, tagTypeString, tagTypeUint8, tagTypeUint16,
		tagTypeUint32, tagTypeUint64:
		return t.Type, nil
	}

	// Type 0 is not a wire type, it is the zero value of a Tag a caller
	// built without setting one, so the type is inferred from the value.
	//
	// # SHORT STRINGS GO IN THE TYPE BYTE, AND THAT IS THE POINT
	//
	// A string of 1 to 16 bytes is written as tagTypeStrBase+n, which is
	// what the live server does and what parseTag reads back. A longer one
	// uses tagTypeString with a uint16 length after it, because the length
	// has nowhere to live in a single type byte.
	switch n := len(t.Value); {
	case n == 1:
		return tagTypeUint8, nil
	case n == 2:
		return tagTypeUint16, nil
	case n == 4:
		return tagTypeUint32, nil
	case n >= 1 && n <= int(tagTypeStrMax-tagTypeStrBase)+1:
		return tagTypeStrBase + byte(n) - 1, nil
	default:
		return tagTypeString, nil
	}
}

// encodeTagList renders tags in the wire shape parseTagList reads: a uint32
// count followed by that many tags.
//
// # WHY THIS IS NOT EncodeExtHello
//
// It looks like the same function and is not. EncodeExtHello zlib-COMPRESSES
// its tag list, because that is what an eMule extended hello carries. A search
// request's tag list is plain and uncompressed, and compressing it produces a
// payload a server cannot read -- a request that is silently wrong, answered
// with silence.
//
// So the two are separate rather than one function with a flag. A flag would
// have been the smaller diff and the wrong shape: "compress or not" is not a
// variation on a wire format, it is two different wire formats that happen to
// share a tag encoder.
//
// # THE COUNT IS FOUR BYTES, AND THIS IS THE SECOND TIME
//
// The extended hello's first version wrote a single byte and every decode
// read a uint32 spanning the count and the first three bytes of the first
// tag. It is written as four here, by construction, and
// TestTheTagCountIsWrittenAsFourBytes exists to keep it that way.
func encodeTagList(tags TagList) ([]byte, error) {
	var out bytes.Buffer
	var count [4]byte
	binary.LittleEndian.PutUint32(count[:], uint32(len(tags)))
	out.Write(count[:])
	for _, t := range tags {
		if err := writeTag(&out, t); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

// tagIDSearchKeyword is 0x01, the string tag carrying a search's keyword.
//
// # 0x01, WHICH IS ALSO THE CLIENT-NAME TAG
//
// The same id means two different things depending on the packet: in a
// server's info tag list 0x01 is its name, and in a search request 0x01 is
// the keyword. That is the protocol's choice, not an ambiguity in this code,
// and it is worth stating because the temptation is to "fix" it by using a
// different id -- which would send the server a tag it does not recognise and
// return nothing, with no error to point at.
const tagIDSearchKeyword byte = 0x01

// parseTagListAt is parseTagList, and it also reports how many bytes it read.
//
// # WHY THIS EXISTS RATHER THAN A CALLER ADDING 4
//
// A search result is a REPEATED structure: a tag list, then 22 bytes of file
// identity, then another tag list, and so on to the end of the packet. To walk
// it a caller needs to know where each list ENDED, not just what it contained,
// and parseTagList returns only the tags.
//
// # AND THE COUNTED SIZE IS NOT THE CONSUMED SIZE
//
// A tag list's own byte length is not derivable from its tags by summing
// their values: a tagTypeString's Value includes its uint16 length prefix, and
// a Str-family type carries its length in the type byte. So re-deriving the
// consumed length from the parsed tags is possible only by duplicating the
// type-width rules that parseTag already implements — and a second copy of
// those rules is a second thing to get wrong. The walk is here instead.
func parseTagListAt(payload []byte) (TagList, int, error) {
	off := 0
	if len(payload) < 4 {
		return nil, 0, fmt.Errorf("a tag list needs a four-byte count and "+
			"the payload is %d bytes", len(payload))
	}
	count := binary.LittleEndian.Uint32(payload[:4])
	off += 4

	if int(count) > (len(payload)-off)/2 {
		return nil, 0, fmt.Errorf("the tag list claims %d tags but the "+
			"payload has room for at most %d (the smallest tag is two "+
			"bytes)", count, (len(payload)-off)/2)
	}

	tags := make(TagList, 0, count)
	for i := uint32(0); i < count; i++ {
		tag, n, err := parseTag(payload[off:])
		if err != nil {
			return nil, 0, fmt.Errorf("tag %d of %d: %w", i+1, count, err)
		}
		off += n
		tags = append(tags, tag)
	}
	return tags, off, nil
}
