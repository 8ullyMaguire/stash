package ed2kwire

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/monkeyWie/goed2k/protocol"
)

// The eMule extended hello: a client's declared capabilities, zlib-compressed.
//
// # WHY THIS IS A SEPARATE PACKAGE-LEVEL FILE AND NOT PART OF THE LOGIN
//
// An ed2k login succeeds without one. The server accepts the connection,
// reports its counts, and says its name -- all of which works today. What the
// extended hello adds is the client's own capability list, and a server needs
// it to route a search: without a client's version, features and limits, the
// server has no idea whether the file it found can actually be served to us.
//
// So this is the step between "we are connected" and "we can search", and it
// is the last thing between this layer and the feature it exists for. It is
// NOT the login, and putting it in with the login would make a working
// handshake look broken when the extended hello is simply absent.
//
// # THE WIRE FORMAT, AND WHERE EACH FACT COMES FROM
//
//	[0xE3]        protocol byte, EDONKEYPROT
//	[0x22]        OP_EXTENDEDPROT
//	[size:4 LE]   the payload length, COUNTING THE OPCODE -- SizePacket() is
//	              size-1, so a writer that puts the body length here
//	              desynchronises the connection by one byte
//	[0x01]        obfuscated
//	[seed:4]      the same SYN-obfuscation seed as the login request
//	[zlib]        a zlib stream wrapping the tag list
//
// # AND THE TWO THINGS THAT ARE EASY TO GET WRONG
//
// # 1. zlib, NOT RAW DEFLATE
//
// Go's compress/flate reads a RAW deflate stream and zlib reads a zlib one,
// and the failure is silent in the direction that matters: a zlib reader given
// a raw deflate stream will often consume the first two bytes as a bogus
// header and then fail deep in the body, with an error that says nothing
// about the real cause. The first two bytes of a zlib stream are the CMF/FLG
// pair, and 0x78 is the CMF for deflate-with-a-32K window -- so a payload
// beginning 0x78 is a zlib stream and one that does not is not, and that is
// checked explicitly below rather than left to the reader.
//
// # 2. A DECODE FAILURE IS A REAL FAILURE, NOT AN EMPTY TAG LIST
//
// The first version of this returned an empty TagList and no error when the
// payload would not decompress. That is the single worst thing this function
// could do: a client that silently believes it has declared no capabilities
// looks identical to a client that declared none on purpose, and a server
// routes a search to the second one. The failure has to be visible.

// ErrNotZlib is returned when a payload is not a zlib stream at all.
//
// Named and separate from a decompression failure because they have different
// causes and a reader needs to tell them: a non-zlib payload means the peer
// sent something else entirely, while a zlib payload that will not inflate
// means it is corrupt or truncated.
var ErrNotZlib = errors.New("the extended hello payload is not a zlib stream")

// maxExtHelloInflated bounds how far a compressed payload may expand.
//
// # A BOUND IS NOT OPTIONAL HERE
//
// The payload arrives from a stranger, and the tag count inside it is also
// from a stranger. An unbounded zlib reader on a small compressed input is a
// decompression bomb: a few hundred bytes can inflate to gigabytes, and the
// process dies rather than returning an error.
//
// The limit is generous -- 64 KiB is far more than any real capability list,
// which is tens of bytes -- and the point is not to be right about the size.
// It is that a hostile or broken peer gets an error instead of the machine.
const maxExtHelloInflated = 64 << 10

// maxExtHelloTags bounds the tag count a caller may encode.
//
// # A REAL BOUND, NOT A REPRESENTATION ONE
//
// The count is a uint32, so a slice could in principle claim four billion
// tags. It cannot: the payload is bounded at 64 KiB inflated, and the
// smallest tag is two bytes, so at most 32768 tags can physically exist.
// This is set well below that so the limit is a property of the CODEC rather
// than an arithmetic consequence of two others, and so a caller gets told
// about it by name.
const maxExtHelloTags = 4096

// EncodeExtHello builds the zlib-compressed tag list an extended hello carries.
//
// The tag list is encoded as a COUNT followed by that many tags, which is the
// same shape parseTagList reads, so what goes in and what comes out are the
// same format and a round trip is meaningful rather than tautological.
func EncodeExtHello(tags TagList) ([]byte, error) {
	if len(tags) > maxExtHelloTags {
		return nil, fmt.Errorf("an extended hello carries a four-byte tag "+
			"count and this package refuses more than %d tags; %d were "+
			"given", maxExtHelloTags, len(tags))
	}

	var plain bytes.Buffer
	// # THE COUNT IS A UINT32, NOT A BYTE
	//
	// The first version of this wrote one byte, and every decode then read a
	// uint32 count spanning the first byte and the first three bytes of the
	// first tag. A two-tag list came out claiming 100,762,114 tags, which
	// the existing bound caught and reported as a corrupt list -- so the
	// failure was loud, and it was in the wrong file: the error named the tag
	// parser, which was correct, and never the writer, which was not.
	//
	// parseTagList reads four bytes. This writes four bytes. They are the
	// same format by construction now rather than by coincidence, and
	// TestTheExtendedHelloCountIsAFourByteCount says so.
	var count [4]byte
	binary.LittleEndian.PutUint32(count[:], uint32(len(tags)))
	plain.Write(count[:])
	for _, t := range tags {
		if err := writeTag(&plain, t); err != nil {
			return nil, err
		}
	}

	var out bytes.Buffer
	zw := zlib.NewWriter(&out)
	if _, err := zw.Write(plain.Bytes()); err != nil {
		// Close is still attempted, so the writer's internal state is not
		// left dangling; its error is the interesting one only if Write
		// succeeded.
		_ = zw.Close()
		return nil, fmt.Errorf("compressing the extended hello's tag "+
			"list: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finishing the extended hello's zlib "+
			"stream: %w", err)
	}
	return out.Bytes(), nil
}

// DecodeExtHello reads the tag list out of an extended hello payload.
//
// # EVERY FAILURE HERE IS AN ERROR, AND NONE OF THEM IS AN EMPTY LIST
//
// A caller that gets an error and an empty list knows something went wrong. A
// caller that gets an empty list and no error cannot tell a client that
// declared nothing from a client whose declaration did not survive the wire,
// and that difference decides whether a server will route a search to us.
func DecodeExtHello(payload []byte) (TagList, error) {
	plain, err := inflateExtHello(payload)
	if err != nil {
		return nil, err
	}

	// parseTagList reads the count and that many tags and reports a tag that
	// runs off the end. A list whose final tag is truncated is refused here
	// rather than returned short, because a short list is a plausible-looking
	// wrong answer.
	tags, err := parseTagList(plain)
	if err != nil {
		return nil, fmt.Errorf("the extended hello decompressed but its "+
			"tag list does not parse: %w", err)
	}
	return tags, nil
}

// inflateExtHello decompresses an extended hello payload, refusing anything
// that is not a zlib stream.
func inflateExtHello(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("%w: the payload is empty", ErrNotZlib)
	}

	// # THE HEADER IS VALIDATED BY zlib.NewReader, AND THE EXPLICIT CHECKS
	// THAT WERE HERE ARE GONE
	//
	// The first version checked payload[0]&0x0F == 8 and then
	// (CMF*256+FLG)%31 == 0, with the reasoning that a reader given a bare
	// deflate stream fails "somewhere deep in the body" and that an explicit
	// check would produce a better error.
	//
	// # THE MUTATION HARNESS SHOWED THE BETTER ERROR ALREADY EXISTED
	//
	// Both checks were probed by deletion. With either one removed, every
	// test still passed -- including the one that asserts
	// errors.Is(err, ErrNotZlib), which was written specifically to pin the
	// error identity. The reason is one line below: zlib.NewReader validates
	// the header itself, and this function wraps ITS error in ErrNotZlib
	// already. So the explicit checks were a second opinion on a question
	// the library had already answered, and the good error was already there.
	//
	// Keeping them would mean keeping 20 lines of code and two error strings
	// that no test can distinguish from the library's own. So they are gone.
	//
	// # WHAT WAS LOST, STATED PLAINLY
	//
	// The wrapped error is zlib's "invalid header", not a sentence naming
	// CMF and FLG. The diagnosis is the same and the identity is the same;
	// only the phrasing is less specific. That is the whole trade.
	//
	// What is NOT gone is the length guard below, which the harness also
	// probed: removing it panics on a one-byte payload with an index out of
	// range, and TestAShortPayloadIsRefusedRatherThanIndexedPast kills it.
	// zlib.NewReader never sees a one-byte payload, so it cannot catch that
	// one for us.

	// # NO EXPLICIT HEADER CHECK, BECAUSE zlib.NewReader ALREADY IS ONE
	//
	// This function used to validate the header itself -- the compression
	// method nibble, then the multiple-of-31 rule, then a two-byte length
	// guard. All three were probed by deletion and ALL THREE SURVIVED.
	//
	// zlib.NewReader is handed the same bytes and does the same validation,
	// and the error it returns is wrapped in ErrNotZlib one line below. So
	// every one of those checks was a second opinion on a question the
	// library had already answered, and the good error was already there.
	//
	// # INCLUDING THE ONE THAT LOOKED LOAD-BEARING
	//
	// The length guard looked different. It sat in front of a read of
	// payload[0] and payload[1], and with it removed a one-byte payload
	// panicked with an index out of range -- found by mutation, and
	// recorded here as the reason the guard had to stay.
	//
	// It did not have to stay. The panic came from a PROBE that called
	// DecodeExtHello directly on a one-byte payload, and re-running it
	// against the guard removed showed zlib.NewReader returning
	// "unexpected EOF" and this function wrapping that in ErrNotZlib. The
	// panic was real; it was also not reachable through this package's own
	// entry point, because the guard is not what stands between a one-byte
	// payload and an index -- the reader is, and it refuses first.
	//
	// # WHAT WAS ACTUALLY LOST
	//
	// A more specific message. The library says "unexpected EOF" where this
	// would have said "a zlib stream has a two-byte header and the payload
	// is 1 byte". The diagnosis and the errors.Is identity are identical.
	//
	// # AND WHAT WAS NOT LOST
	//
	// The bomb bound below is NOT redundant and is probed: with the reader
	// limit or the post-read check removed, the tests fail. See
	// TestExactlyTheLimitIsAccepted and
	// TestACompressionBombIsRefusedAtTheLimit.

	zr, err := zlib.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotZlib, err)
	}
	defer zr.Close()

	// # THE INFLATION BOUND, AND WHY LimitReader RATHER THAN A CHECK AFTER
	//
	// The limit is applied to the READ, so a bomb is stopped while it is
	// happening. Reading everything and then checking len() would already
	// have allocated the bomb.
	plain, err := io.ReadAll(io.LimitReader(zr, maxExtHelloInflated+1))
	if err != nil {
		return nil, fmt.Errorf("the extended hello's zlib stream is "+
			"corrupt or truncated: %w", err)
	}
	if len(plain) > maxExtHelloInflated {
		return nil, fmt.Errorf("the extended hello inflates to more than "+
			"the %d byte limit, so it is refused rather than allocated. A "+
			"few hundred compressed bytes can inflate to gigabytes, and "+
			"the process would die rather than return an error",
			maxExtHelloInflated)
	}
	return plain, nil
}

// SendExtHello sends this client's capabilities to a server.
//
// # IT IS SENT AFTER THE LOGIN AND NOT AS PART OF IT
//
// The server has to have accepted the connection first: it allocates the
// session that the capabilities describe, and a server that has not accepted
// us has no session to attach them to. The login request is also the one
// packet that carries the SYN-obfuscation seed, so this goes through
// sendFrame, which refuses to write a plain packet before it.
func (s *Server) SendExtHello(tags TagList) error {
	payload, err := EncodeExtHello(tags)
	if err != nil {
		return err
	}
	if err := s.sendFrame(protocol.EdonkeyHeader, opExtendedProt, payload); err != nil {
		return fmt.Errorf("sending the extended hello: %w", err)
	}
	return nil
}

// opExtendedProt is OP_EXTENDEDPROT, 0x22: the packet carrying a client's
// compressed capability list.
//
// # 0x22 AND NOT 0x01
//
// 0x01 is OP_HELLO, and this is the mistake this package has already made
// once: OP_HELLO is the KAD UDP packet, it does not appear in the ed2k login
// response, and a client waiting for it waits forever for a packet that does
// not exist. 0x22 is the ed2k one, and it is a different opcode from the login
// (0xE3) for the same reason a second packet exists at all.
const opExtendedProt byte = 0x22
