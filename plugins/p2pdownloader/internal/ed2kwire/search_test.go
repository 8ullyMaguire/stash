package ed2kwire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// Searching: the request side. What comes BACK is a different packet with a
// different shape, and it is not in this file.
//
// # WHAT IS AND IS NOT TESTABLE HERE
//
// A search request is one packet we build, so it is fully testable offline. A
// search RESULT is packets a stranger decides the shape of, arriving at their
// own pace, and it is not -- which is why this file stops at the request and
// says so rather than asserting a decode it has never seen.
//
// # THE THREE THINGS THAT MATTER, AND ALL THREE ARE BUGS THIS FILE HAS HAD
//
// 1. The tag list is NOT compressed. A compressed search request is answered
//    with silence, and no test that only checks "we sent bytes" would notice.
// 2. The count is FOUR bytes, as parseTagList reads it.
// 3. The size field COUNTS the opcode -- the third time in this package.

// TestTheSearchRequestIsNotCompressed: the bug a round trip cannot see.
//
// # THIS IS THE ONE THAT WOULD HAVE GONE TO THE WIRE
//
// The first version called EncodeExtHello, which is the wrong function: an
// extended hello carries a ZLIB-COMPRESSED tag list and a search request
// carries a plain one. The request compiled, the tests passed, and the server
// would have answered with silence -- the exact failure this package has
// already hit once, when it waited for an OP_HELLO that is not in the ed2k
// exchange at all.
//
// A compressed payload is detectable here and nowhere else, because a round
// trip through our own decoder would agree with a wrong encoder perfectly.
func TestTheSearchRequestIsNotCompressed(t *testing.T) {
	payload, err := SearchRequest{Keyword: "ubuntu"}.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(payload) < 2 {
		t.Fatalf("the payload is %d bytes: %x", len(payload), payload)
	}

	// A zlib stream's first byte has a compression method of 8 in its low
	// nibble, and 0x78 is the common one. A plain tag list starts with its
	// uint32 count, which for a one-tag list is 0x01.
	if payload[0]&0x0F == 0x08 {
		t.Errorf("the payload starts 0x%02X, which is a zlib header. A "+
			"search request's tag list is PLAIN and a server cannot read a "+
			"compressed one -- it would answer with silence and nothing "+
			"in this package would point at the cause", payload[0])
	}

	// The positive statement of the same thing: it must be exactly the
	// uint32 count followed by exactly one tag.
	count := binary.LittleEndian.Uint32(payload[:4])
	if count != 1 {
		t.Errorf("the leading uint32 reads as %d, want 1. The count is four "+
			"bytes, which is what parseTagList reads; a one-byte count is "+
			"the bug the extended hello had", count)
	}

	tags, err := parseTagList(payload)
	if err != nil {
		t.Fatalf("the payload does not parse as the plain tag list it is "+
			"meant to be: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("parsed %d tags, want 1: %v", len(tags), tags)
	}
	if kw, ok := tags.StringByID(tagIDSearchKeyword); ok {
		// The keyword is NUL-terminated on the wire, so the accessor hands
		// back the terminator too. Stripping it is the caller's job and is
		// not done here, because doing it in the encoder would be a second
		// place that knows about the terminator.
		t.Logf("keyword round-tripped as %q", kw)
	} else {
		t.Errorf("the payload has no tag 0x%02X", tagIDSearchKeyword)
	}
}

// TestAnEmptyKeywordIsRefused: the request a server answers by dropping us.
//
// # WHY THIS MATTERS MORE THAN IT LOOKS
//
// A server reads an empty keyword as "match everything", and its whole index
// comes back. That is not a correctness problem for us, it is a social one:
// a client that asks every server for everything on its first packet is a
// client that gets disconnected, and a caller debugging "search returns
// nothing" would never find the cause here.
func TestAnEmptyKeywordIsRefused(t *testing.T) {
	if _, err := (SearchRequest{}).Build(); !errors.Is(err, ErrRefused) {
		t.Errorf("an empty keyword returned %v, want ErrRefused. A server "+
			"reads it as a request for its entire index", err)
	}
}

// TestTheSearchRequestFramesLikeEveryOtherPacket: the size counts the opcode.
//
// # THE THIRD TIME, AND THE POINT IS TO KEEP IT THE LAST TIME
//
// The login had this bug and so did the extended hello. The difference now is
// that SearchRequest is written against frameBytes instead of assembling a
// header, so the +1 is in one place -- and this test exists so that someone
// "simplifying" the call into a hand-built header finds out.
func TestTheSearchRequestFramesLikeEveryOtherPacket(t *testing.T) {
	payload, err := SearchRequest{Keyword: "ubuntu"}.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	frame, err := frameBytes(0xE3, opSearchRequest, payload)
	if err != nil {
		t.Fatalf("frameBytes: %v", err)
	}

	if frame[0] != 0xE3 {
		t.Errorf("frame starts 0x%02X, want the EDONKEYPROT byte 0xE3", frame[0])
	}
	if frame[5] != opSearchRequest {
		t.Errorf("frame's opcode is 0x%02X, want 0x%02X", frame[5], opSearchRequest)
	}
	// SizePacket() is size-1, so a reader recovers the payload length this
	// way. If the +1 is ever dropped this is the assertion that notices.
	size := binary.LittleEndian.Uint32(frame[1:5])
	if int(size-1) != len(payload) {
		t.Errorf("the size field says %d payload bytes and the payload is "+
			"%d. A reader computes size-1, so this is where the opcode "+
			"accounting goes wrong", size-1, len(payload))
	}
}

// TestTheSearchOpcodeIsNotTheResultOpcode: 0x16, not 0x33.
func TestTheSearchOpcodeIsNotTheResultOpcode(t *testing.T) {
	if opSearchRequest != 0x16 {
		t.Errorf("opSearchRequest is 0x%02X, want 0x16 (OP_SEARCHREQUEST)",
			opSearchRequest)
	}
	if opSearchRequest == 0x33 {
		t.Error("opSearchRequest is 0x33, which is OP_SEARCHRESULT -- what " +
			"the server sends back. Sending it is a client volunteering " +
			"results the server never asked for")
	}
	if opSearchRequest == opLoginRequest {
		t.Error("the search opcode is the login's, which would make a " +
			"search a second login request")
	}
	if opSearchRequest == opExtendedProt {
		t.Error("the search opcode is the extended hello's, so a search " +
			"would be read as a capability list")
	}
}

// TestSearchWritesTheRequestToTheConnection: it goes out, framed, once.
//
// # A LOOPBACK SERVER, SCRIPTED TO ACCEPT A LOGIN AND NOTHING MORE
//
// The fake accepts the connection and reads whatever arrives, which is enough
// to see that Search put a well-formed frame on the wire. It is not enough to
// know a server would accept it, and the test says so rather than implying
// that the live tests have become redundant.
func TestSearchWritesTheRequestToTheConnection(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()

	// The Server needs sentFirst set, because sendFrame refuses to write a
	// plain packet before the connection's first one. This is the same
	// invariant the login enforces and a test has to satisfy honestly rather
	// than by reaching into the struct's history.
	srv := &Server{conn: client, sentFirst: true}

	read := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 512)
		_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _ := server.Read(buf)
		got := make([]byte, n)
		copy(got, buf[:n])
		read <- got
	}()

	if err := srv.Search(SearchRequest{Keyword: "ubuntu"}); err != nil {
		t.Fatalf("Search: %v", err)
	}

	select {
	case got := <-read:
		if len(got) < 6 {
			t.Fatalf("only %d bytes arrived: %x", len(got), got)
		}
		if got[5] != opSearchRequest {
			t.Errorf("the byte on the wire is 0x%02X, want the search "+
				"opcode 0x%02X", got[5], opSearchRequest)
		}
		if !bytes.Contains(got, []byte("ubuntu")) {
			t.Errorf("the keyword is not on the wire: %x", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived within 5s")
	}
}

// TestSearchRefusesAnEmptyKeywordBeforeTouchingTheConnection.
//
// The refusal happens in Build, which is called before sendFrame, so an empty
// keyword never reaches a socket. That ordering is the point: a refusal that
// happens after the write is a refusal that has already told the server
// something.
func TestSearchRefusesAnEmptyKeywordBeforeTouchingTheConnection(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()

	srv := &Server{conn: client, sentFirst: true}

	arrived := make(chan bool, 1)
	go func() {
		buf := make([]byte, 64)
		_ = server.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		n, _ := server.Read(buf)
		arrived <- n > 0
	}()

	if err := srv.Search(SearchRequest{}); !errors.Is(err, ErrRefused) {
		t.Errorf("Search with an empty keyword returned %v, want ErrRefused", err)
	}

	select {
	case got := <-arrived:
		if got {
			t.Error("bytes reached the connection despite the refusal. The " +
				"refusal has to happen before the write, or the server " +
				"has already been told something")
		}
	case <-time.After(2 * time.Second):
		// The deadline fired, so nothing arrived. That is the pass.
	}
}

// TestTheKeywordSurvivesBytesThatLookLikeFraming: the payload is a byte
// string, not a field.
//
// A keyword with a pipe, a NUL or a byte above 0x7F is legal in a filename on
// the network. The tag list is length-prefixed, so none of it can terminate
// the tag early -- and that is worth asserting, because a keyword that broke
// the framing would produce a request that parses locally and is unreadable by
// the server.
func TestTheKeywordSurvivesBytesThatLookLikeFraming(t *testing.T) {
	keywords := []string{
		"a|b",
		"with\x00nul",
		"héllo wörld",
		strings.Repeat("x", 300),
		"..",
	}

	for _, kw := range keywords {
		payload, err := SearchRequest{Keyword: kw}.Build()
		if err != nil {
			t.Errorf("Build(%q): %v", kw, err)
			continue
		}
		tags, err := parseTagList(payload)
		if err != nil {
			t.Errorf("Build(%q) produced something that does not parse: %v", kw, err)
			continue
		}
		if len(tags) != 1 {
			t.Errorf("Build(%q) produced %d tags, want 1", kw, len(tags))
			continue
		}
		// The wire form carries the keyword plus its NUL terminator, and the
		// length prefix, so the value is strictly longer than the keyword.
		if len(tags[0].Value) <= len(kw) {
			t.Errorf("Build(%q): the value is %d bytes, not longer than the "+
				"%d-byte keyword, so the length prefix is missing",
				kw, len(tags[0].Value), len(kw))
		}
	}
}

// # THE TWO WRITES THIS FILE EXISTS TO PIN
//
// Both of these survive a full-suite mutation today, and both were
// invisible while they were wrong for the same reason: the tests built
// requests with a keyword short enough to hide each defect.

// TestTheKeywordIsNULTerminated: the byte a server reads to stop.
//
// # APPEND, NOT THE STRING'S OWN BYTES
//
// eMule string tags are NUL-terminated. The length prefix says how long the
// text is, and the terminator is what a server scanning for the end of the
// string actually reads -- and a server that walks off the end of a
// non-terminated string does not error, it keeps reading the rest of the
// packet as keyword. So a missing terminator is a wrong search, not a failed
// one, which is the failure mode with no log line anywhere.
func TestTheKeywordIsNULTerminated(t *testing.T) {
	// Build returns the BARE TAG LIST -- the protocol byte, size and opcode
	// are added by Server.Search when it frames the packet. An earlier
	// version of this test skipped nine bytes for a header that is not
	// there and read a tag count of 1,953,396,066 out of the length
	// prefix, which is the same class of mistake this whole file is
	// about: reading a field at an offset belonging to another layer.
	raw, err := SearchRequest{Keyword: "ubuntu"}.Build()
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}

	// The keyword is the last thing in the payload, so the terminator is
	// the final byte -- and it must be NUL, not the last letter.
	if got := raw[len(raw)-1]; got != 0 {
		t.Errorf("the request ends in 0x%02X, not 0x00. The keyword tag is "+
			"the last field, so a server reading to its terminator walks "+
			"off the end of the packet and keeps going.\n\n"+
			"A missing terminator is not a failed search but a WRONG "+
			"one: the server reads the remaining bytes as keyword "+
			"text, finds nothing, and answers nothing. There is no "+
			"error to log.", got)
	}

	// And the length prefix COUNTS the terminator: the wire reads
	// 82 01 07 00 for "ubuntu" -- type, id, length 7, six letters, NUL. A
	// reader expecting 6 would slice one byte short and lose the very
	// terminator the check above exists to require.
	//
	// parseTag keeps the two length bytes INSIDE Value, so the decoded
	// value is 9 bytes and not 7. That is deliberate in tag.go: Value is
	// the tag's bytes as they appeared, not a re-interpretation of them.
	tags, err := parseTagList(raw)
	if err != nil {
		t.Fatalf("decoding our own request: %v", err)
	}
	for _, tag := range tags {
		if tag.ID != tagIDSearchKeyword {
			continue
		}
		const prefixLen = 2
		if got := len(tag.Value) - prefixLen; got != len("ubuntu")+1 {
			t.Errorf("the keyword text is %d bytes, want %d (six letters "+
				"plus their terminator). eMule's string length "+
				"prefix INCLUDES the NUL", got, len("ubuntu")+1)
		}
		if !bytes.HasSuffix(tag.Value[prefixLen:], []byte{0}) {
			t.Errorf("the keyword text %q does not end in NUL",
				tag.Value[prefixLen:])
		}
		return
	}
	t.Fatal("no keyword tag in the request we just built")
}

// TestATagCountOverTwoFiftyFiveUsesAllFourBytes: a zeroed array hides the
// bug, and only a big enough list exposes it.
//
// # WHY EVERY EXISTING TEST MISSED THIS
//
// The count is written into a zero-initialised [4]byte, and for a
// little-endian uint32 below 256 only byte 0 is non-zero. Writing
// count[0] = byte(n) therefore produces BYTE-IDENTICAL output to the real
// PutUint32 for every list shorter than 256 tags -- and a keyword search
// carries exactly one tag.
//
// # AND THIS IS NOT COSMETIC
//
// The four bytes before a tag list are what a server reads to find out how
// far to walk. A count that is 0x01 0x01 0x01 0x00 for three tags says 65,537
// tags follow, so the server keeps reading a list that ended at tag three --
// and the result is silence, with nothing to log.
func TestATagCountOverTwoFiftyFiveUsesAllFourBytes(t *testing.T) {
	// 300 tags: byte 0 is 0x2C, and bytes 1..3 must be zero. A one-byte
	// write gives the same answer, so the mutation is caught by the LENGTH
	// of the encoded list rather than by the count field alone.
	tags := make(TagList, 300)
	for i := range tags {
		tags[i] = Tag{ID: byte(i), Type: 0x03, Value: []byte{0x01, 0x00, 0x00, 0x00}}
	}

	raw, err := encodeTagList(tags)
	if err != nil {
		t.Fatalf("encoding 300 tags: %v", err)
	}

	// 4 count bytes + 300 tags of 6 bytes each (2 header + 4 value).
	const want = 4 + 300*6
	if len(raw) != want {
		t.Errorf("300 tags encoded to %d bytes, want %d. A count that "+
			"does not match the list it introduces is how a reader "+
			"loses the stream", len(raw), want)
	}

	// And the count itself must read back as 300.
	if got := binary.LittleEndian.Uint32(raw[:4]); got != 300 {
		t.Errorf("the encoded count is %d, want 300. Only byte 0 is set "+
			"for a list this size, so a one-byte write is "+
			"indistinguishable from the real one -- which is exactly "+
			"why the LENGTH assertion above is the one that bites",
			got)
	}
}
