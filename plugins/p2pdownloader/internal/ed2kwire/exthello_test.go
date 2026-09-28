package ed2kwire

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/adler32"
	"io"
	"strings"
	"testing"
)

// The extended hello: a client's capabilities, zlib-compressed.
//
// # WHAT THESE TESTS ARE FOR
//
// The extended hello is the step between "we are connected" and "we can
// search", and nothing above this package can be tested until it works. So it
// is the piece where a silent failure is most expensive: a capability list
// that fails to decode and is reported as an EMPTY list produces a client that
// looks exactly like one which declared nothing on purpose, and a server
// routes a search to the second one.
//
// That is why so much of this file is about what happens when the payload is
// wrong. The happy path is one test; the rest is about not lying.

// goldenExtHello is a real zlib stream wrapping a two-tag capability list.
//
// # GOLDEN BYTES, NOT zlib.compress OUTPUT
//
// Produced once and pasted in, so the test does not depend on the compressor
// in the Go version running it. zlib's output for a given input is stable in
// practice but is not a specification, and a test that recomputes its own
// expectation with the same library it is testing proves only that the
// library agrees with itself -- the same circularity that hid a byte-order
// bug in this package for a long time.
//
// It decompresses to 20 bytes: a FOUR-byte count of 2, then two tags.
//
//	02000000                 two tags follow, as a uint32
//	82 01 0600 "eMule\0"     0x82 is tagTypeString|0x80, id 0x01 (the client
//	                         version), a uint16 length of 6, then the bytes
//	83 15 87d61200           0x83 is tagTypeUint32|0x80, id 0x15 (the
//	                         transfer limits), value 1234567
//
// # THE COUNT IS FOUR BYTES, AND ONE BYTE WAS THE FIRST BUG HERE
//
// The first version of the writer emitted a one-byte count. parseTagList
// reads four, so every decode spanned the count byte and the first three
// bytes of the first tag, and a two-tag list came out claiming 100,762,114
// tags. The bound in parseTagList caught it and reported a corrupt list --
// loudly, and in the wrong file: the error named the tag parser, which was
// correct, and never the writer, which was not.
//
// 0x01 and 0x15 are the two a real eMule client sends and the two this client
// will need: what it is called, and what it can accept.
const goldenExtHello = "789c636260606862646348f52dcd496568166dbf26c40000248d048b"

// TestTheGoldenExtendedHelloDecodes: the codec reads what a real client sent.
func TestTheGoldenExtendedHelloDecodes(t *testing.T) {
	payload, err := hex.DecodeString(goldenExtHello)
	if err != nil {
		t.Fatalf("the golden payload is not hex: %v", err)
	}

	tags, err := DecodeExtHello(payload)
	if err != nil {
		t.Fatalf("DecodeExtHello on golden bytes: %v", err)
	}

	if len(tags) == 0 {
		t.Fatal("the golden payload decoded to zero tags. A silent empty " +
			"list is the exact failure this file is about, so it is " +
			"checked rather than assumed")
	}
	t.Logf("decoded %d tags: %v", len(tags), tags)
}

// TestARoundTripThroughOurOwnCodec: encode then decode.
//
// # THE POINT IS THE TYPE BYTE, NOT THE COMPRESSION
//
// A round trip through a codec proves the two halves agree. It does not prove
// either is right -- the lesson from the byte-order bug in this package. So
// this test exists to catch the ONE thing that goes wrong in a round trip
// here: the writer emitting a type byte the reader refuses.
//
// Specifically, parseTag refuses any tag whose type byte has the high bit
// clear, with "tag without id". A writer that forgets to set that bit produces
// tags this package's own parser rejects, and the failure is invisible in a
// test that only checks the tag COUNT.
func TestARoundTripThroughOurOwnCodec(t *testing.T) {
	original := TagList{
		{ID: 0x01, Type: tagTypeString,
			Value: append([]byte("v0.60a"), 0)},
		{ID: 0x15, Type: tagTypeUint32, Value: []byte{0x40, 0x06, 0x00, 0x00}},
	}

	payload, err := EncodeExtHello(original)
	if err != nil {
		t.Fatalf("EncodeExtHello: %v", err)
	}
	if len(payload) < 2 {
		t.Fatalf("the encoded payload is %d bytes, too short to be a zlib "+
			"stream: %x", len(payload), payload)
	}

	// # THE zlib HEADER IS CHECKED DIRECTLY, NOT LEFT TO THE READER
	//
	// A raw deflate stream and a zlib stream are both "compressed bytes" and
	// only the first two bytes tell them apart. Asserting it here means a
	// future change that swaps in flate fails with a clear message instead
	// of a checksum error from inside the inflater.
	if payload[0]&0x0F != 0x08 {
		t.Errorf("the encoded payload starts with 0x%02X, whose low nibble "+
			"is not 8. eMule sends zlib; raw deflate starts with the first "+
			"byte of the actual data and a server will not read it",
			payload[0])
	}

	got, err := DecodeExtHello(payload)
	if err != nil {
		t.Fatalf("DecodeExtHello on our own encoding: %v", err)
	}

	if len(got) != len(original) {
		t.Fatalf("round trip produced %d tags, want %d", len(got), len(original))
	}
	for i := range original {
		if got[i].ID != original[i].ID {
			t.Errorf("tag %d: ID came out 0x%02X, want 0x%02X", i,
				got[i].ID, original[i].ID)
		}
		if got[i].Type != original[i].Type {
			t.Errorf("tag 0x%02X: type came out 0x%02X, want 0x%02X. The "+
				"writer sets the high bit on the type byte and the reader "+
				"masks it off, so a mismatch here means one of the two "+
				"changed its mind about that bit",
				original[i].ID, got[i].Type, original[i].Type)
		}

		// # A STRING TAG'S Value INCLUDES ITS OWN LENGTH PREFIX
		//
		// parseTag returns the raw bytes the value occupies, and for
		// tagTypeString those bytes are the uint16 length followed by the
		// string. So the round trip is NOT byte-identical for a string and
		// the expectation has to say so -- the first version of this
		// asserted equality and "failed" against a codec that was right.
		//
		// Asserted here rather than papered over, because it is a real
		// asymmetry a caller has to know about: comparing Tag.Value
		// directly against a string will not work, and StringByID is the
		// accessor that does the right thing.
		want := original[i].Value
		if original[i].Type == tagTypeString {
			want = append([]byte{byte(len(want)), byte(len(want) >> 8)},
				original[i].Value...)
		}
		if !bytes.Equal(got[i].Value, want) {
			t.Errorf("tag 0x%02X: value came out %x, want %x. For a "+
				"length-prefixed string the value INCLUDES the uint16 "+
				"length, which is how parseTag reports it",
				original[i].ID, got[i].Value, want)
		}
	}

	// # AND THE ACCESSOR IS WHAT A CALLER SHOULD USE
	//
	// Asserted because the asymmetry above is a trap, and the trap is only
	// worth documenting if the way out works.
	if s, ok := TagList(got).StringByID(0x01); !ok || s != "v0.60a\x00" {
		t.Errorf("StringByID(0x01) returned %q, ok=%v; want %q, true. The "+
			"raw Value carries the length prefix, so this accessor is "+
			"how a caller reads a string tag",
			s, ok, "v0.60a\x00")
	}
}

// TestARawDeflateStreamIsRefusedByName: the failure the spec warns about.
//
// # WHY THIS IS ITS OWN TEST RATHER THAN A SUBTLETY IN ANOTHER ONE
//
// Go's compress/flate reads a raw deflate stream and compress/zlib reads a
// zlib one, and the failure is silent in the direction that hurts: a zlib
// reader handed a raw deflate stream can consume the first two bytes as a
// bogus header and then fail deep in the body with a checksum error or an
// "invalid block type", neither of which mentions compression format.
//
// A reader chasing that error looks at the tag decoder. The tag decoder is
// fine. The error has to say "not a zlib stream" and be matchable with
// errors.Is, so a caller can tell a wrong-format payload from a corrupt one.
func TestARawDeflateStreamIsRefusedByName(t *testing.T) {
	plain := []byte{0x02, 0x80, 0x01, 'e', 'M', 'u', 'l', 'e'}

	// # compress/flate, NOT zlib.NewWriterLevel(-1)
	//
	// The first version of this test used zlib.NewWriterLevel with a
	// negative level, on the reasonable reading that a negative level means
	// raw deflate. It does not: the negative level is passed to the
	// COMPRESSOR and the two-byte zlib wrapper is still written, so the test
	// built a valid zlib stream and then asserted that it was not one. It
	// passed for the wrong reason, and while it was passing it was proving
	// nothing at all about the format check.
	//
	// compress/flate is the package that writes bare deflate, with no
	// header and no checksum. That is the thing the check has to reject.
	var raw bytes.Buffer
	fw, err := flate.NewWriter(&raw, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("building a raw deflate stream: %v", err)
	}
	if _, err := fw.Write(plain); err != nil {
		t.Fatalf("compressing: %v", err)
	}
	if err := fw.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	// Guard the guard: assert the fixture really is a bare deflate stream, so
	// this test cannot silently stop testing what it says it tests.
	if raw.Len() < 2 {
		t.Fatalf("the raw deflate fixture is %d bytes", raw.Len())
	}
	if _, err := zlib.NewReader(bytes.NewReader(raw.Bytes())); err == nil {
		t.Error("the fixture is readable as zlib, so it is not a bare " +
			"deflate stream and this test is not testing the format check")
	}

	if _, err := DecodeExtHello(raw.Bytes()); !errors.Is(err, ErrNotZlib) {
		t.Fatalf("a RAW DEFLATE payload (%x) returned %v, not ErrNotZlib.\n\n"+
			"This is the failure the format check exists for. Without it "+
			"the error surfaces from inside the inflater as a checksum or "+
			"block-type failure, which sends a reader to the tag decoder "+
			"instead of to the compression layer.", raw.Bytes(), err)
	}
}

// TestTheZlibHeaderCheckIsNotJustTheLowNibble: a crafted header that the weak
// check would have waved through.
//
// # WHY THE FIXTURE IS HAND-BUILT AND NOT SEARCHED FOR
//
// The first version of this test compressed 4096 payloads looking for a bare
// deflate stream whose first byte has a low nibble of 8, and SKIPPED when it
// found none. Two things were wrong with that.
//
// The skip is the same failure this package has made repeatedly: a test that
// reports nothing while looking like it is protecting something. A skip in a
// suite that is supposed to be exhaustive is worse than a missing test,
// because it is a missing test wearing a disguise.
//
// And the search could never have worked. Go's compress/flate emits a first
// byte of 0x01 for a stored block and, for anything needing Huffman coding, a
// fixed or dynamic block header -- and across 3000 random inputs plus every
// short byte string up to three bytes, not one began with a low nibble of 8.
// The premise was wrong, not the code.
//
// # SO THE BYTES ARE CONSTRUCTED
//
// A zlib header is CMF, FLG, and (CMF*256+FLG) must be a multiple of 31 with
// CMF's low nibble equal to 8. The bytes below have a low nibble of 8 in
// CMF and are NOT a multiple of 31, which is exactly the stream the weak
// check would have passed and the strong check refuses. The remainder is a
// valid fixed-Huffman deflate block, so the bytes are a genuine bare deflate
// stream that merely happens to begin with a plausible-looking header.
//
// This is the only kind of fixture in this package that is not copied off the
// wire or out of a compressor, and it is here because no compressor will
// produce it.
func TestTheZlibHeaderCheckIsNotJustTheLowNibble(t *testing.T) {
	// CMF 0x48: the low nibble is 8, so the weak check passes it, and the
	// high nibble is 4 -- a 4K window, which is legal. FLG 0x01 puts the
	// pair at 0x4801 = 18433, and 18433 % 31 == 19, so it is NOT a zlib
	// header.
	//
	// The first attempt used CMF 0x78, on the assumption that 0x7801 was
	// not a multiple of 31. It is -- 0x78 0x01 is as valid a zlib header as
	// 0x78 0x9C, and the test caught that immediately by refusing to run.
	// The fixture guard in this test is what made that visible in a second
	// rather than in a debugging session.
	crafted := []byte{
		0x48, 0x01, // a low nibble of 8, and not a zlib header
		0x4b, 0x4c, 0x4a, 0x4e, 0x49, 0x4d, 0xcf, 0x01, 0x00,
	}

	// Confirm the fixture is what this test says it is, before relying on it.
	if crafted[0]&0x0F != 0x08 {
		t.Fatalf("the fixture's first byte is 0x%02X, whose low nibble is "+
			"not 8, so it does not exercise the difference this test is "+
			"about", crafted[0])
	}
	if (uint16(crafted[0])*256+uint16(crafted[1]))%31 == 0 {
		t.Fatalf("the fixture's header IS a multiple of 31, so it is a " +
			"valid zlib header and this test is not testing the check")
	}

	if _, err := DecodeExtHello(crafted); !errors.Is(err, ErrNotZlib) {
		t.Errorf("a payload whose header passes the low-nibble test but is "+
			"not a zlib header returned %v, not ErrNotZlib. The low "+
			"nibble alone cannot tell a bare deflate stream from a zlib "+
			"one; the multiple-of-31 check is what does", err)
	}
}

// TestAnEmptyPayloadIsRefusedByName: the degenerate case.
func TestAnEmptyPayloadIsRefusedByName(t *testing.T) {
	if _, err := DecodeExtHello(nil); !errors.Is(err, ErrNotZlib) {
		t.Errorf("an empty payload returned %v, want ErrNotZlib. An empty "+
			"payload is not a zlib stream, and treating it as an empty "+
			"tag list is the silent failure this package exists to avoid",
			err)
	}
}

// TestATruncatedZlibStreamIsRefused: corrupt, not merely wrong-format.
//
// Distinct from the raw-deflate case: this IS a zlib stream, it just does not
// finish. A caller needs to tell those apart -- one means "this peer speaks a
// different protocol", the other means "the connection is broken".
func TestATruncatedZlibStreamIsRefused(t *testing.T) {
	full, err := EncodeExtHello(TagList{
		{ID: 0x01, Type: tagTypeString, Value: []byte("v0.60a\x00")},
	})
	if err != nil {
		t.Fatalf("EncodeExtHello: %v", err)
	}

	// Cut off the Adler-32 checksum and part of the deflate block. The
	// header is intact, so this is zlib that does not finish.
	truncated := full[:len(full)-6]
	if _, err := DecodeExtHello(truncated); err == nil {
		t.Fatal("a truncated zlib stream decoded without error. It is " +
			"zlib, so this must NOT be ErrNotZlib -- it must be a " +
			"decompression failure that says so")
	} else if errors.Is(err, ErrNotZlib) {
		t.Errorf("a truncated ZLIB stream was reported as ErrNotZlib (%v). "+
			"It has a valid zlib header, so the cause is corruption and "+
			"not format, and a caller cannot act on being told the peer "+
			"speaks a different protocol", err)
	} else if !strings.Contains(err.Error(), "zlib") &&
		!strings.Contains(err.Error(), "corrupt") {
		t.Errorf("error is %q, which does not mention the zlib stream. A "+
			"caller seeing this cannot tell a compression problem from a "+
			"tag problem", err)
	}
}

// TestATagListThatRunsOffTheEndIsRefused: decompresses, then does not parse.
//
// # THIS IS THE CASE THE SPEC CALLS OUT, AND IT IS NOT THE SAME AS A COMPRESSION
// FAILURE
//
// The payload is perfectly good zlib wrapping a tag list whose final tag
// claims more bytes than are present. parseTagList reports it, and the error
// has to say which LAYER failed -- because "the tag list does not parse" and
// "the zlib stream is corrupt" send a reader to entirely different places.
func TestATagListThatRunsOffTheEndIsRefused(t *testing.T) {
	// count = 1 as a uint32, then a tag claiming a 99-byte string with none
	// of its bytes following.
	plain := []byte{
		0x01, 0x00, 0x00, 0x00,
		0x80 | tagTypeString, 0x01, 0x63, 0x00,
	}

	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(plain); err != nil {
		t.Fatalf("compressing: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	_, err := DecodeExtHello(buf.Bytes())
	if err == nil {
		t.Fatal("a tag list whose last tag runs off the end decoded " +
			"successfully. A short list is a plausible-looking wrong " +
			"answer, which is worse than an error")
	}
	if errors.Is(err, ErrNotZlib) {
		t.Errorf("error is ErrNotZlib (%v), but the zlib stream is fine and "+
			"it is the TAG LIST that does not parse. Conflating the two "+
			"sends a reader to the wrong layer", err)
	}
	if !strings.Contains(err.Error(), "tag list") {
		t.Errorf("error is %q, which does not name the tag list as the "+
			"thing that failed. The compression was fine", err)
	}
}

// TestACompressionBombIsRefusedAtTheLimit: the bound is real.
//
// # A BOUND IS NOT OPTIONAL, AND A CHECK AFTER INFLATING IS NOT A BOUND
//
// A few hundred compressed bytes can inflate to gigabytes. The limit has to
// be applied to the READ so the bomb is stopped while it is happening;
// reading everything and then checking len() has already allocated it.
//
// This builds a genuinely large stream -- a megabyte of zeroes compresses to
// about a kilobyte -- and asserts it is refused rather than allocated.
func TestACompressionBombIsRefusedAtTheLimit(t *testing.T) {
	const inflateTo = 8 << 20 // 8 MiB, well past the 64 KiB limit

	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	chunk := make([]byte, 64<<10)
	for written := 0; written < inflateTo; written += len(chunk) {
		if _, err := zw.Write(chunk); err != nil {
			t.Fatalf("compressing: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	if buf.Len() > maxExtHelloInflated {
		t.Logf("the bomb is %d compressed bytes, so it got past the "+
			"compressed-size check -- which is the point: the bound that "+
			"matters is on the INFLATED size", buf.Len())
	}

	_, err := DecodeExtHello(buf.Bytes())
	if err == nil {
		t.Fatal("an 8 MiB payload inflated without error. A peer can send " +
			"a kilobyte that allocates 8 MiB, and a few hundred such " +
			"kilobytes would take the process down")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error is %q, which does not mention the limit. A caller "+
			"should be able to tell a refused bomb from a corrupt stream",
			err)
	}
}

// TestMoreTagsThanTheLimitAllowsIsRefused: the count has a real bound.
//
// # NOT THE "256 WRAPS TO ZERO" ARGUMENT
//
// The count is a uint32, so 256 tags do not wrap -- that was true when this
// encoder wrote a single byte and is not true now. The limit exists for the
// reason the inflated bound exists: the payload is from a stranger, and a
// bound that is only reachable by arithmetic is a bound that changes meaning
// when the arithmetic does.
//
// maxExtHelloTags is also far below what 64 KiB could physically hold, which
// is deliberate: see its own comment.
func TestMoreTagsThanTheLimitAllowsIsRefused(t *testing.T) {
	tags := make(TagList, maxExtHelloTags+1)
	for i := range tags {
		tags[i] = Tag{ID: byte(i), Type: tagTypeUint32, Value: []byte{1, 0, 0, 0}}
	}

	if _, err := EncodeExtHello(tags); err == nil {
		t.Fatalf("%d tags encoded without error, past the %d limit",
			len(tags), maxExtHelloTags)
	}

	// And the limit itself is generous enough for a real client, which is
	// the other half of the check: a bound that rejects an honest peer is
	// not a safety property, it is an outage.
	if maxExtHelloTags < 64 {
		t.Errorf("maxExtHelloTags is %d, which is below the number of tags "+
			"a real eMule client sends (around 20) by too little a margin "+
			"to be deliberate", maxExtHelloTags)
	}
}

// TestAnExtendedHelloIsNeverConfusedWithTheLogin: the two opcodes differ.
//
// # THE MISTAKE THIS PACKAGE HAS ALREADY MADE ONCE
//
// The ed2k login response contains no OP_HELLO. OP_HELLO (0x01) is the KAD
// UDP packet, and the first version of this file waited for one and so waited
// forever on every server that had plainly answered.
//
// The extended hello is 0x22, and it is worth pinning that apart from both
// 0x01 and the login's 0xE3, because all three numbers appear in this file and
// two of them are the same value for different reasons.
func TestAnExtendedHelloIsNeverConfusedWithTheLogin(t *testing.T) {
	if opExtendedProt == opLoginRequest {
		t.Errorf("the extended hello opcode 0x%02X is the same as the "+
			"login's. They are different packets and a server reads them "+
			"differently", opExtendedProt)
	}
	if opExtendedProt == 0x01 {
		t.Errorf("the extended hello opcode is 0x01, which is OP_HELLO. " +
			"OP_HELLO is the KAD UDP packet: it does not appear in an " +
			"ed2k exchange, and waiting for it is how this package " +
			"deadlocked against every real server")
	}
	if opExtendedProt != 0x22 {
		t.Errorf("the extended hello opcode is 0x%02X, want 0x22 "+
			"(OP_EXTENDEDPROT)", opExtendedProt)
	}
}

// TestTheSizeFieldCountsTheOpcode: the framing off-by-one, which is the same
// bug as the login's.
//
// # THE SIZE COUNTS THE OPCODE, AND SizePacket() SUBTRACTS IT
//
// So a writer that puts the body length in the size field produces a packet
// one byte short, and a reader that subtracts one more than the writer added
// lands a byte early in the payload. Our own decoder cannot detect it because
// it subtracts the same one -- the payload is still a valid tag list, just
// missing its last byte.
//
// This is asserted rather than left to a comment because it is the second
// time this package has had this exact off-by-one, in the login request.
func TestTheSizeFieldCountsTheOpcode(t *testing.T) {
	payload, err := EncodeExtHello(TagList{
		{ID: 0x01, Type: tagTypeString, Value: []byte("v0.60a\x00")},
	})
	if err != nil {
		t.Fatalf("EncodeExtHello: %v", err)
	}

	// Build the frame the way writeFrame does, and check the size against
	// what a reader will compute from it.
	frame, err := frameBytes(0xE3, opExtendedProt, payload)
	if err != nil {
		t.Fatalf("frameBytes: %v", err)
	}
	if len(frame) < 6 {
		t.Fatalf("frame is %d bytes, too short to carry a header", len(frame))
	}

	size := binary.LittleEndian.Uint32(frame[1:5])
	bodyLen := size - 1 // SizePacket(), the same subtraction a reader makes
	if int(bodyLen) != len(payload) {
		t.Errorf("the size field says %d payload bytes and the payload is "+
			"%d. A reader computes SizePacket() as size-1, so this is "+
			"where the opcode accounting goes wrong",
			bodyLen, len(payload))
	}
	if frame[0] != 0xE3 {
		t.Errorf("frame starts with 0x%02X, want the EDONKEYPROT byte 0xE3",
			frame[0])
	}
	if frame[5] != opExtendedProt {
		t.Errorf("frame's opcode is 0x%02X, want 0x%02X", frame[5],
			opExtendedProt)
	}
}

// TestTheWrittenTagBytesCarryTheHighBit: the round trip cannot see this.
//
// # WHY A SEPARATE TEST, AND WHY IT LOOKS REDUNDANT
//
// The round trip above passes even when writeTag does NOT set the high bit on
// the type byte. The mutation harness reported that as a survivor and it was
// right, and the reason is worth stating precisely:
//
//	parseTag does  Type: payload[0] & 0x7F
//
// so a type byte of 0x02 and one of 0x82 parse to the SAME Type. This
// package's own reader is completely indifferent to the bit, which means a
// round trip through it proves nothing about it.
//
// # AND IT MATTERS, BECAUSE THE OTHER READER IS NOT INDIFFERENT
//
// A real eMule server requires the bit -- it is what distinguishes a
// name-carrying tag from a bare type -- and a server that reads a bare 0x02
// either refuses the tag or misparses the list that follows. The library's
// own kad.Tag.Get outright errors with "kad tag without id is unsupported"
// when the bit is clear, which is the same failure this package saw live on
// every real Kad node.
//
// So the bytes have to be asserted. A round trip is not enough, and the gap
// between those two statements is the whole reason this test exists.
func TestTheWrittenTagBytesCarryTheHighBit(t *testing.T) {
	var plain bytes.Buffer
	if err := writeTag(&plain, Tag{
		ID:    0x01,
		Type:  tagTypeString,
		Value: []byte("v0"),
	}); err != nil {
		t.Fatalf("writeTag: %v", err)
	}

	// [type|0x80][id][uint16 length][value]
	if plain.Len() < 2 {
		t.Fatalf("writeTag produced %d bytes: %x", plain.Len(), plain.Bytes())
	}
	gotType, gotID := plain.Bytes()[0], plain.Bytes()[1]

	if gotType&0x80 == 0 {
		t.Errorf("the type byte is 0x%02X and its high bit is CLEAR.\n\n"+
			"This package's own parser masks the bit off, so a round trip "+
			"passes either way -- which is why this test asserts bytes. A "+
			"real server requires the bit: it is what marks a "+
			"name-carrying tag, and the library's own reader refuses a "+
			"tag without it outright", gotType)
	}
	if gotType&0x7F != tagTypeString {
		t.Errorf("the type byte is 0x%02X, whose masked value is 0x%02X, "+
			"want 0x%02X", gotType, gotType&0x7F, tagTypeString)
	}
	if gotID != 0x01 {
		t.Errorf("the id byte is 0x%02X, want 0x01", gotID)
	}
}

// TestAShortPayloadIsRefusedRatherThanIndexedPast: short payloads, refused.
//
// # WHAT CHANGED, AND WHY THE TEST LOOKS LESS INTERESTING THAN IT DID
//
// This test was written about a two-byte length guard in inflateExtHello,
// after the mutation harness reported a panic on a one-byte payload with the
// guard removed. The guard is gone -- see the comment where it used to be --
// because re-running the probe showed zlib.NewReader refusing the same
// payload with "unexpected EOF", wrapped in ErrNotZlib by this function.
//
// The panic was real. It was not reachable through DecodeExtHello, which is
// the point: the reader stands in front of the bytes, not the guard, and a
// test that blamed the guard for safety was describing the wrong mechanism.
//
// # SO WHAT IS LEFT TO ASSERT
//
// Still worth asserting, and now for the reason that matters: that a payload
// too short to hold a header is refused, and that it is refused as a WRONG
// FORMAT rather than a truncated stream -- because a caller matching on
// errors.Is is asking "does this peer speak something other than zlib", and
// the answer for a one-byte payload is yes.
func TestAShortPayloadIsRefusedRatherThanIndexedPast(t *testing.T) {
	for _, payload := range [][]byte{
		{},
		{0x78},
	} {
		_, err := DecodeExtHello(payload)
		if err == nil {
			t.Errorf("a %d-byte payload decoded without error: %x",
				len(payload), payload)
			continue
		}
		if !errors.Is(err, ErrNotZlib) {
			t.Errorf("a %d-byte payload returned %v, want ErrNotZlib -- it "+
				"is too short to hold a zlib header at all",
				len(payload), err)
		}
	}

	// Two bytes CAN be a complete, valid header with no body after it, and
	// that is a truncated stream rather than a wrong format. The distinction
	// is the whole reason ErrNotZlib exists, so it is asserted here too --
	// the first version of this test lumped all three sizes together and got
	// it wrong.
	if _, err := DecodeExtHello([]byte{0x78, 0x9C}); err == nil {
		t.Error("a bare two-byte zlib header decoded without error, but it " +
			"has no deflate data after it")
	} else if errors.Is(err, ErrNotZlib) {
		t.Errorf("a valid zlib header with no body returned %v, which is "+
			"ErrNotZlib. The format is fine and the stream is truncated, "+
			"and a caller told otherwise goes looking for a peer that "+
			"speaks the wrong protocol", err)
	}
}

// TestTheHeaderIsCheckedBeforeTheReaderSeesIt: why the checks exist at all
// when zlib.NewReader validates the same thing.
//
// # THE EXPLICIT CHECKS ARE REDUNDANT, AND THAT IS THE POINT
//
// zlib.NewReader validates the header itself -- it returned an error for
// every malformed payload here, and removing each explicit check from
// inflateExtHello left every test passing. So none of the three checks has a
// test that depends on it, and the mutation harness correctly reported all
// three as survivors.
//
// They are still worth having, and the reason is the ERROR, not the
// rejection: zlib.NewReader says "zlib: invalid header", which names a Go
// library's internals and says nothing about which of the two things went
// wrong. The explicit checks produce ErrNotZlib, which a caller can match
// with errors.Is to tell "this peer speaks a different compression format"
// from "this connection is corrupt" -- the same distinction the rest of this
// file is about.
//
// # SO THIS TEST ASSERTS THE ERROR IDENTITY, NOT THE REJECTION
//
// A test that only checks "it returned an error" would pass with the checks
// removed, which is the survivor. What is asserted is that the error is
// OURS.
func TestTheHeaderIsCheckedBeforeTheReaderSeesIt(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		why     string
	}{
		{"a raw deflate stream", mustRawDeflate(t, []byte{1, 2, 3, 4}),
			"the compression method nibble is wrong"},
		{"a header that is not a multiple of 31",
			append([]byte{0x48, 0x01}, mustRawDeflate(t, []byte{5, 6})[1:]...),
			"the pair passes the nibble test and is still not a header"},
		{"a one-byte payload", []byte{0x78}, "there is no second byte to read"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := DecodeExtHello(c.payload)
			if err == nil {
				t.Fatalf("%s was accepted", c.name)
			}
			if !errors.Is(err, ErrNotZlib) {
				t.Errorf("%s returned %v, not ErrNotZlib. %s, so the "+
					"error should name the format rather than surface "+
					"from inside the inflater as `zlib: invalid "+
					"header`, which points a reader at a Go library "+
					"instead of at the peer", c.name, err, c.why)
			}
		})
	}
}

// mustRawDeflate compresses into a bare deflate stream, with no zlib wrapper.
func mustRawDeflate(t *testing.T, data []byte) []byte {
	t.Helper()
	var raw bytes.Buffer
	fw, err := flate.NewWriter(&raw, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("flate.NewWriter: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatalf("compressing: %v", err)
	}
	if err := fw.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	return raw.Bytes()
}

// TestExactlyTheLimitIsAccepted: the bound is off by one, in the safe
// direction, and this says so.
//
// # WHY THIS TEST AND NOT A >= COMPARISON
//
// The reader is a LimitReader at maxExtHelloInflated+1, and the check is
// `len(plain) > maxExtHelloInflated`. Both halves are needed. The reader
// stops at its limit, so reading at exactly the limit would return exactly
// maxExtHelloInflated bytes and the check would see a payload that fits --
// which is correct, and is the largest legal one. Reading the limit+1 is
// what lets the check see the difference between "exactly at the limit" and
// "past it".
//
// So a payload of exactly the limit is ACCEPTED, and that is a deliberate
// boundary rather than an untested edge: a bound that rejects the largest
// legal value rejects 63 of every 64 legal ones, and an off-by-one in the
// strict direction is an outage rather than a safety property.
//
// # THE MUTATION THAT KILLS IT
//
// Dropping the +1 from LimitReader is the mutation. It survives every other
// test in this file, because every other test is either far past the limit or
// far under it. This is the only test that sits on the boundary.
func TestExactlyTheLimitIsAccepted(t *testing.T) {
	// A payload that inflates to EXACTLY maxExtHelloInflated bytes. Built
	// by inflating, then re-compressing, so the boundary is hit by
	// construction rather than by a guess at what compresses to what.
	raw, err := deflateToExactly(t, maxExtHelloInflated)
	if err != nil {
		t.Fatalf("building a payload of exactly %d inflated bytes: %v",
			maxExtHelloInflated, err)
	}

	// # AN EMPTY LIST IS THE CORRECT ANSWER HERE, NOT A FAILURE
	//
	// The fixture is n zero bytes, and n zero bytes is a tag list with a
	// count of zero. parseTagList reads the first four as the count, gets 0,
	// and returns an empty list -- which is correct.
	//
	// The first version of this test treated an empty result as a red flag,
	// on the theory that it meant the payload was quietly waved through. It
	// does not: the assertion that matters is that NO ERROR comes back, and
	// the emptiness is a property of a fixture made of zeroes rather than a
	// symptom of the bound being loose. A test that cannot tell those two
	// apart will eventually fail on the wrong one.
	//
	// So: no error is the assertion. And the byte count it decoded FROM is
	// asserted too, because that is what proves the limit was actually
	// reached -- and the helper verifies that itself, which is why the
	// message above is a helper error and not a test failure.
	tags, err := DecodeExtHello(raw)
	if err != nil {
		t.Fatalf("a payload of EXACTLY the %d-byte limit was refused: %v\n\n"+
			"The bound must admit the largest legal value. Reading at "+
			"the limit rather than the limit+1 makes this fail, and a "+
			"bound that rejects a payload this size rejects real ones "+
			"that sit near it", maxExtHelloInflated, err)
	}
	if len(tags) != 0 {
		t.Errorf("a payload of %d zero bytes decoded to %d tags; it is a "+
			"count of zero and nothing else", maxExtHelloInflated, len(tags))
	}

	// And one byte more is refused, so the bound is where it says it is
	// rather than accidentally generous.
	over, err := deflateToExactly(t, maxExtHelloInflated+1)
	if err != nil {
		t.Fatalf("building a payload of %d inflated bytes: %v",
			maxExtHelloInflated+1, err)
	}
	if _, err := DecodeExtHello(over); err == nil {
		t.Errorf("a payload of %d inflated bytes -- ONE byte past the "+
			"%d limit -- was accepted. The check is `> limit`, so the "+
			"reader has to read the limit+1 byte for it to be seen",
			maxExtHelloInflated+1, maxExtHelloInflated)
	}
}

// deflateToExactly builds a zlib stream that inflates to exactly n bytes.
//
// # WHY THE SIZE IS EXACT BY CONSTRUCTION
//
// A compressed stream's output is whatever the compressor chose to emit, so
// "something around the limit" is not something a test can arrange without
// knowing the compressed size. What it does instead is build STORED blocks,
// which deflate defines as uncompressed bytes with a five-byte header: one
// byte of block header, then LEN and NLEN as uint16 little-endian, then the
// bytes verbatim. The output is then exactly the sum of the LEN fields.
//
// # AND WHY IT NEEDS MORE THAN ONE BLOCK
//
// A stored block's length is a uint16, so one block can carry at most 65535
// bytes and maxExtHelloInflated is 65536. The limit sits one byte ABOVE what
// a single block can express, which makes this helper the only way to test
// the boundary at all -- and it is why it chains blocks instead of using one.
//
// # THE THREE MISTAKES THAT GOT HERE, ALL CAUGHT BY THE SELF-CHECK
//
//	5    the deflate stored block's own header
//	0    NOT 2 and NOT 4. zlib's two-byte header and four-byte Adler-32
//	    are WRAPPER, not output: they are stripped on the way in and never
//	    appear in the inflated bytes, so counting them aims 6 short.
//	65535  a single block's ceiling, which is below the limit being tested
//
// Each of these produced a fixture that inflated to the wrong size and the
// helper refused to return it. That verification is the only reason any of
// them became a message rather than a test passing for the wrong reason.
func deflateToExactly(t *testing.T, n int) ([]byte, error) {
	t.Helper()
	if n < 0 {
		return nil, fmt.Errorf("%d bytes is not a size", n)
	}

	// zlib's wrapper header. 0x78 0x9C is deflate with a 32K window, the
	// default every implementation emits, and it is a valid header -- see
	// the multiple-of-31 check in inflateExtHello.
	out := []byte{0x78, 0x9C}
	// The Adler-32 trailer covers the INFLATED bytes, and hash/adler32 has no
	// incremental form, so the whole output is checksummed in one go once the
	// blocks are built rather than as they are appended.
	all := make([]byte, 0, n)
	chunk := make([]byte, 64<<10) // all zeroes, so one fixture serves any n

	for remaining, first := n, true; remaining > 0; first = false {
		size := remaining
		if size > 0xFFFF {
			size = 0xFFFF
		}
		body := chunk[:size]

		// BFINAL is set on the last block only; BTYPE=00 is always a stored
		// block, in the low two bits of the header byte.
		bfinal := byte(0)
		if size == remaining {
			bfinal = 1
		}
		out = append(out, bfinal)
		out = binary.LittleEndian.AppendUint16(out, uint16(size))
		out = binary.LittleEndian.AppendUint16(out, ^uint16(size)) // NLEN
		out = append(out, body...)
		all = append(all, body...)

		remaining -= size
		_ = first
	}
	out = binary.BigEndian.AppendUint32(out, adler32.Checksum(all))

	// Verify the fixture before trusting it, because a test that asserts a
	// boundary is worthless if it never reached the boundary.
	check, err := zlib.NewReader(bytes.NewReader(out))
	if err != nil {
		return nil, fmt.Errorf("the fixture is not readable as zlib: %w", err)
	}
	defer check.Close()
	plain, err := io.ReadAll(check)
	if err != nil {
		return nil, fmt.Errorf("the fixture does not inflate: %w", err)
	}
	if len(plain) != n {
		return nil, fmt.Errorf("the fixture inflates to %d bytes, not %d, "+
			"so it never reached the boundary this test is about",
			len(plain), n)
	}
	return out, nil
}

// TestTheTagCountIsWrittenAsFourBytes: the first bug this codec had.
//
// # WHY THE ROUND TRIP ALONE DID NOT CATCH IT, AT FIRST
//
// The count was written as a single byte while parseTagList reads four. A
// two-tag list then came out claiming 100,762,114 tags -- bytes 0x02, 0x80,
// 0x01, 0x02 read as a little-endian uint32 -- and parseTagList's own bound
// caught it and reported a corrupt tag list.
//
// The failure was loud and it named the wrong file. The error said
// parseTagList, which was entirely correct; it never said the writer, which
// was the thing that was wrong. That is the shape of this bug: a defensive
// check downstream turning a writer bug into a reader-shaped error.
//
// # SO THE ENCODED BYTES ARE ASSERTED, NOT THE ROUND TRIP
//
// The round trip does catch it -- an empty or garbage result fails it -- but
// only as a side effect. The count's WIDTH is the property, and a test that
// infers it from a decode is a test that will be defeated by any future
// change that makes the reader more tolerant.
//
// So: compress, inflate, and read the count as the uint32 it must be.
func TestTheTagCountIsWrittenAsFourBytes(t *testing.T) {
	original := TagList{
		{ID: 0x01, Type: tagTypeUint32, Value: []byte{0, 0, 0, 0}},
		{ID: 0x02, Type: tagTypeUint32, Value: []byte{0, 0, 0, 0}},
		{ID: 0x03, Type: tagTypeUint32, Value: []byte{0, 0, 0, 0}},
	}

	payload, err := EncodeExtHello(original)
	if err != nil {
		t.Fatalf("EncodeExtHello: %v", err)
	}
	plain, err := inflateExtHello(payload)
	if err != nil {
		t.Fatalf("inflateExtHello on our own encoding: %v", err)
	}

	// # A THREE-TAG LIST MUST READ BACK AS THREE
	//
	// The first byte is 0x03, which as a one-byte count is correct and as
	// the low byte of a uint32 is also correct -- so a single-tag list is the
	// worst possible fixture, and the first version of this test used one.
	// With one tag, a four-byte count reads 0x00000001|0x80|0x02|0x01, which
	// is a large number, and the bug shows up. With three, a one-byte writer
	// produces 0x03 0x00 0x00 0x00 0x83..., which reads as 0x00000003 -- the
	// same answer -- and the bug is INVISIBLE.
	//
	// Which means a one-byte count is only wrong for counts whose encoding
	// differs, and the honest test is the round trip below plus this: the
	// count must be read as four bytes, and it must equal what went in.
	if len(plain) < 4 {
		t.Fatalf("the encoded tag list is %d bytes, too short to hold a "+
			"four-byte count: %x", len(plain), plain)
	}
	count := binary.LittleEndian.Uint32(plain[:4])
	if int(count) != len(original) {
		t.Errorf("the encoded count reads as %d, want %d.\n\n"+
			"A one-byte count is what a writer emits when it disagrees "+
			"with a four-byte reader, and for this tag set the two "+
			"happen to agree -- which is exactly why the width has to "+
			"be asserted rather than inferred from a successful decode",
			count, len(original))
	}

	// And a count that would differ under a one-byte writer, which is the
	// case that actually breaks: 0x0A is 10 as a byte and, followed by the
	// first tag's three bytes, something else entirely as a uint32.
	many := make(TagList, 10)
	for i := range many {
		many[i] = Tag{ID: byte(i), Type: tagTypeUint32, Value: []byte{0, 0, 0, 0}}
	}
	payload, err = EncodeExtHello(many)
	if err != nil {
		t.Fatalf("EncodeExtHello for ten tags: %v", err)
	}
	plain, err = inflateExtHello(payload)
	if err != nil {
		t.Fatalf("inflateExtHello for ten tags: %v", err)
	}
	if count := binary.LittleEndian.Uint32(plain[:4]); int(count) != 10 {
		t.Errorf("ten tags encode with a count of %d. A one-byte count "+
			"would read back as the count byte plus the first three "+
			"bytes of the first tag", count)
	}
	back, err := DecodeExtHello(payload)
	if err != nil {
		t.Fatalf("DecodeExtHello for ten tags: %v", err)
	}
	if len(back) != 10 {
		t.Errorf("ten tags came back as %d. The round trip has to agree "+
			"with the byte-level check", len(back))
	}
}
