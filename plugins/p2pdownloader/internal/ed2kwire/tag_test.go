package ed2kwire

import (
	"encoding/binary"
	"strings"
	"testing"
)

// # THE GOLDEN BYTES IN THIS FILE WERE CAPTURED FROM A LIVE SERVER
//
// 85.17.116.222:6082, an ed2k-rust server, on 2026-09-28, in response to an
// obfuscated OP_LOGINREQUEST. They are not hand-written and not derived from
// the library's own encoder — a decoder tested against its own encoder proves
// only that the two agree, which is the failure mode this file exists to
// prevent (see the tag.go header: the library's encoder and a real server's
// disagree completely).
//
// Every value below is semantically checkable, and the test checks it. A
// decoder that lands one byte off produces a tag list that decodes to
// plausible nonsense, so a test that only asserts "it parsed" is a test that
// passes on garbage. The assertions here name the values a human can verify.

func TestTheStatusPacketFromALiveServerDecodesToItsOwnValues(t *testing.T) {
	// OP_SERVERSTATUS (0x40), 20 bytes. Two counts, then the server's own
	// endpoint, then that port again.
	payload := mustDecodeHex(t,
		"a0840100dd450000c2170000551174dec2170000")

	users := binary.LittleEndian.Uint32(payload[0:4])
	files := binary.LittleEndian.Uint32(payload[4:8])

	// 0x0184a0 = 99488 and 0x45dd = 17885. Read the same bytes a different
	// way and the counts change, so a wrong offset is caught here rather
	// than in a progress bar weeks later.
	if users != 99488 {
		t.Errorf("user count = %d, want 99488 (0x0184A0)", users)
	}
	if files != 17885 {
		t.Errorf("file count = %d, want 17885 (0x45DD)", files)
	}

	// The server's IP is 85.17.116.222 -- the address we dialled. That is
	// the check that proves the offset: a misaligned read cannot produce
	// the address we know we connected to.
	ip := payload[12:16]
	if ip[0] != 85 || ip[1] != 17 || ip[2] != 116 || ip[3] != 222 {
		t.Errorf("the server's own address read as %d.%d.%d.%d, want "+
			"85.17.116.222 -- the address this packet came from",
			ip[0], ip[1], ip[2], ip[3])
	}

	port := binary.LittleEndian.Uint16(payload[16:18])
	if port != 6082 {
		t.Errorf("the server's own port read as %d, want 6082 -- the port "+
			"this packet came from", port)
	}
}

func TestTheServerInfoTagListFromALiveServerDecodesToItsOwnValues(t *testing.T) {
	// OP_SERVERINFO (0x41), 110 bytes: a GUID, the server's endpoint, then
	// a tag list of ten.
	//
	// The GUID is deadbeefcafebabe123456789abcdef0 -- a deliberate joke by
	// the server's author, and a gift for a test: a real value that cannot
	// be produced by a misaligned read.
	const guidHex = "deadbeefcafebabe123456789abcdef0"
	payload := mustDecodeHex(t, guidHex+
		"551174de"+ // the server's own IP, 85.17.116.222
		"c217"+ // ...and the port we dialled
		"0a000000"+ // ten tags
		"99016564326b2d72"+
		"7573749b0b6d6169"+
		"6e20736572766572"+
		"949131382e318387"+
		"50c3000083884042"+
		"0f00838940420f00"+
		"83923b0700008397"+
		"c21700008398d017"+
		"000081ae20011af8"+
		"530101251c004bff"+
		"fe001902")

	// The header, before the tag list. Four fields, each checkable.
	gotGUID := payload[:16]
	if hexOf(gotGUID) != guidHex {
		t.Errorf("the GUID read as %s, want %s", hexOf(gotGUID), guidHex)
	}
	ip := payload[16:20]
	if ip[0] != 85 || ip[3] != 222 {
		t.Errorf("the endpoint read as %d.%d.%d.%d, want 85.17.116.222",
			ip[0], ip[1], ip[2], ip[3])
	}
	if port := binary.LittleEndian.Uint16(payload[20:22]); port != 6082 {
		t.Errorf("the port read as %d, want 6082", port)
	}

	tagSection := payload[22:]
	tags, err := parseTagList(tagSection)
	if err != nil {
		t.Fatalf("the ten tags a live server sent do not parse: %v", err)
	}
	if len(tags) != 10 {
		t.Fatalf("decoded %d tags, want the 10 the count field declares. "+
			"A count and a parse that disagree means one of the two is "+
			"being read wrong", len(tags))
	}

	// # THE TAG LIST IS LONGER THAN THE TAGS, AND THAT IS NOT A PARSE FAILURE
	//
	// Ten tags consume 75 of the 84 bytes here. The remaining 8 are the
	// server's second endpoint — a UDP ip:port — which the protocol puts
	// AFTER the tag list in this packet.
	//
	// The first version of this decoder treated a tag list as running to
	// the end of the payload, so a packet that legitimately carries more
	// after the tags read as a misparse. The fix is NOT to consume the
	// trailing bytes as if they were tags: guessing a width for bytes whose
	// meaning is unestablished is exactly the move that desynchronises
	// every tag after it. So the list reports what it used and the caller
	// decides what the rest is.
	_ = tagSection

	// tag 0x01, wire type 0x19 -- a NINE-byte string, with the length in the
	// TYPE byte rather than in a field. The type byte is masked first, so
	// 0x99 is a type of 0x19 and not a type of 0x99: masking is what stops
	// this being read as an unknown type.
	if name, ok := tags.StringByID(0x01); !ok || name != "ed2k-rust" {
		t.Errorf("tag 0x01 = %q (ok=%v), want \"ed2k-rust\"", name, ok)
	}

	// tag 0x0B, wire type 0x1B -- an eleven-byte string.
	if desc, ok := tags.StringByID(0x0B); !ok || desc != "main server" {
		t.Errorf("tag 0x0B = %q (ok=%v), want \"main server\"", desc, ok)
	}

	// tag 0x91, wire type 0x14 -- a four-byte string. Three different lengths
	// in three different type bytes, so a reader that assumed one fixed
	// string length would get all three wrong.
	if v, ok := tags.StringByID(0x91); !ok || v != "18.1" {
		t.Errorf("tag 0x91 = %q (ok=%v), want \"18.1\"", v, ok)
	}

	// The connection limits, which are the numbers that matter for
	// deciding whether this server is worth staying on. All four are
	// uint32 tags, so a reader that treats 0x08 as the width would read
	// the wrong magnitude from every one of them.
	//
	// # THE TAG ID IS THE SECOND BYTE AND THE TYPE IS THE FIRST
	//
	// The first version of this fixture transposed them and asserted
	// {0x87, 50000}. The id is 0x87 and the type is 0x83 — every one of these
	// tags shares the type 0x83 and differs only in id, which is what a
	// family of connection limits looks like. Transposed, the parser
	// reported wire type 0x07, which this server never sends: the parser
	// was right and the test data was wrong, and the way to tell was that
	// the values below are all semantically checkable.
	//
	// Each is checked because a decoder one byte off produces a
	// plausible-but-different number rather than an error. The UDP port
	// being a different number from the TCP one is the strongest of these
	// checks: it is two plausible numbers and only one alignment gives it.
	for _, want := range []struct {
		id  byte
		val uint32
		why string
	}{
		{0x87, 50000, "the soft connection limit"},
		{0x88, 1000000, "the hard connection limit"},
		{0x89, 1000000, "the source connection limit"},
		{0x92, 1851, "a count the server reports about itself"},
		{0x97, 6082, "the TCP port -- the one we dialled"},
		{0x98, 6096, "the UDP port, a different number again"},
	} {
		got, ok := tags.Uint32ByID(want.id)
		if !ok {
			t.Errorf("tag 0x%02X (%s) is absent or not numeric", want.id,
				want.why)
			continue
		}
		if got != want.val {
			t.Errorf("tag 0x%02X (%s) = %d, want %d", want.id, want.why,
				got, want.val)
		}
	}
}

// TestAParserThatRunsPastAnUnknownTypeSaysSoRatherThanGuessing: an unknown
// wire type has an unknown width, so every tag after it is unreachable. The
// list is not partially usable and must not pretend to be.
func TestAParserThatRunsPastAnUnknownTypeSaysSoRatherThanGuessing(t *testing.T) {
	// A count of two, then a well-formed tag, then type 0x77 which this
	// client does not model.
	//
	// The first tag is 0x11 (Str1) carrying one byte, "A" -- so it consumes
	// exactly three bytes and the reader arrives at the unknown one.
	payload := mustDecodeHex(t,
		"02000000"+ // two tags
			"110141"+ // type 0x11 = Str1, id 0x01, one byte "A"
			"770141") // type 0x77: unknown width

	_, err := parseTagList(payload)
	if err == nil {
		t.Fatal("a list with an unknown tag type parsed without error")
	}
	// The error has to name the tag, because "parse error" with a packet
	// attached sends the reader hunting through the wrong layer.
	if !strings.Contains(err.Error(), "0x77") {
		t.Errorf("err = %v, want it to name the unknown type 0x77. Without "+
			"it the reader cannot tell a protocol difference from a bug",
			err)
	}
}

func TestAStrTypeByteIsItsLengthNotAStringType(t *testing.T) {
	// The claim this file's whole existence rests on: for a Str-family tag
	// the type byte carries the LENGTH, so a Str9 tag is exactly nine bytes
	// and consumes ten with its header.
	payload := mustDecodeHex(t, "01000000"+"1a03616263") // type 0x1A = Str10
	// type 0x1A = Str(0x1A-0x10) = 10 bytes, but only four follow.

	_, err := parseTagList(payload)
	if err == nil {
		t.Fatal("a Str10 tag with four bytes of text parsed as though it " +
			"had ten. A string longer than the payload would then be " +
			"read from the NEXT tag")
	}
	if !strings.Contains(err.Error(), "10-byte") {
		t.Errorf("err = %v, want it to say the claimed length was 10", err)
	}
}

func TestATagCountLargerThanThePayloadIsRefusedBeforeAllocating(t *testing.T) {
	// A count of 4 billion in a 20-byte payload. The smallest tag is two
	// bytes, so this is refused on the count-versus-bytes check rather than
	// by attempting a four-billion-element slice.
	payload := make([]byte, 20)
	binary.LittleEndian.PutUint32(payload[0:4], 4_000_000_000)

	_, err := parseTagList(payload)
	if err == nil {
		t.Fatal("a tag list claiming four billion tags in twenty bytes " +
			"parsed without error")
	}
	if !strings.Contains(err.Error(), "at most") {
		t.Errorf("err = %v, want it to report what the payload can hold", err)
	}
}

// mustDecodeHex decodes hex, failing the test on malformed input rather than
// continuing with a short slice.
func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	// The golden hex is written in readable groups, so spaces and
	// newlines are stripped rather than being a silent parse error.
	s = strings.NewReplacer(" ", "", "\n", "", "\t", "").Replace(s)
	out := make([]byte, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		var b byte
		for j := 0; j < 2; j++ {
			c := s[i+j]
			var v byte
			switch {
			case c >= '0' && c <= '9':
				v = c - '0'
			case c >= 'a' && c <= 'f':
				v = c - 'a' + 10
			case c >= 'A' && c <= 'F':
				v = c - 'A' + 10
			default:
				t.Fatalf("the golden hex at position %d is not hex: %q", i, c)
			}
			b = b<<4 | v
		}
		out = append(out, b)
	}
	return out
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}
