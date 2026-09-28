package ed2kwire

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"
)

// Asking a source for one window of a file.
//
// # THE WIRE SHAPE, AND WHERE EACH BYTE COMES FROM
//
// Both directions are quoted from eMule's own opcodes.h, the upstream C++
// client, with its layout comments intact:
//
//	OP_SENDINGPART  0x46  <HASH 16><von 4><bis 4><Daten len:(von-bis)>
//	OP_REQUESTPARTS 0x47  <HASH 16><von[3] 4*3><bis[3] 4*3>
//
// # 0xD4 IS NOT A REQUEST OPCODE, AND THAT COST A REVISION OF THE PLAN
//
// The first draft of the transfer plan used 0xD4 for the request. 0xD4 is
// OP_PACKEDPROT -- the zlib-compressed PROTOCOL BYTE, which this package
// already reads in DecodeSearchResult. A source handed an 0xD4 packet would
// be reading it in a protocol this client does not speak, and the answer
// would be silence.
//
// Worth naming as a class of error: 0xD4 is a real constant, from the right
// document, in the right numeric range. Nothing about it looks wrong. A
// placeholder like 0xFF invites a second look and a real-but-wrong value does
// not, which is why these constants are cited rather than recalled.
//
// # THE PAYLOAD IS OFFSETS, NOT A PART NUMBER
//
// This is the part that is easiest to get wrong, because "part 7 of 12" is
// the natural mental model of a chunked transfer and it is NOT the wire.
//
// Neither packet contains a part index. Both speak in BYTE OFFSETS: a
// request asks for the bytes in [Start, End), and an answer states the same
// range and then the bytes. The 9500-byte window is a division both ends
// apply to those offsets -- it is not a number on the wire.
//
// So there is no "part N" to get off by one, and there is also no inclusive
// bound to get off by one: End is the first byte PAST the window, which is
// why every length in this file is End-Start rather than End-Start+1.
//
// # AND NO TAG LIST, WHICH IS THE ODD ONE OUT HERE
//
// Every other packet in this package carries a tag list. These two carry
// none: a request is a hash and offsets, an answer is a hash, offsets and
// bytes. Handing a part request to parseTagList would read the first four
// bytes of the hash as a tag count -- and since a hash's first four bytes
// are arbitrary, that is either a nonsense count or, occasionally, a
// plausible one. The asymmetry is the reason the decoders below do not share
// the tag machinery, and the comment is here so the next person does not
// "simplify" them into it.

// PartSize is the byte window both ends divide a file by.
//
// 9500, from eMule. Not negotiable: a source and a client that disagree
// about the window size disagree about which offsets a part number means.
//
// # AND THE LAST WINDOW IS SHORT, WHICH IS NOT AN ERROR
//
// A file of 20,000 bytes has two full windows and a 1,000-byte remainder.
// The remainder is a normal, successful answer. Code that requires every
// answer to be exactly PartSize would reject the last part of every file,
// which is every file.
const PartSize = 9500

// The source-side opcodes, cited from eMule's opcodes.h.
const (
	// opSendingPart is a source's answer: hash, start, end, bytes.
	opSendingPart byte = 0x46

	// opRequestParts asks for blocks. eMule's own layout is three start
	// offsets and three end offsets -- it batches three windows in one
	// packet. This client asks for ONE, which the same layout expresses:
	// two of the three pairs are zero-length.
	//
	// Kept as the protocol's shape rather than reduced to a one-window
	// opcode, because a source may answer a one-window request in a form
	// this client has not seen and the three-pair layout is what the
	// upstream code expects to parse on receipt.
	opRequestParts byte = 0x47

	// opFileReqAnsNoFile is a source saying it does not have the file.
	//
	// It is handled explicitly rather than as "something unexpected",
	// because "this source does not have it" is a normal answer to a
	// question and a caller needs it to move to the next source. Folding
	// it into a generic error is how a transfer reports "something went
	// wrong" for the most ordinary outcome there is.
	opFileReqAnsNoFile byte = 0x48
)

// ErrNoFile is a source saying it does not hold the file we asked about.
//
// DISTINCT from a transfer failure and from an unreachable source, because
// the remedy differs in all three: try another source, retry, or report the
// link dead. A caller that cannot tell them apart retries a source that will
// never have the file, which is the whole point of naming it.
type ErrNoFile struct {
	Hash [16]byte
}

func (e ErrNoFile) Error() string {
	return fmt.Sprintf("the source does not have this file: %x", e.Hash)
}

// PartRequest asks a source for one window of a file.
//
// # START AND END ARE HALF-OPEN
//
// [Start, End). End is the first byte PAST the window, so a window's length
// is End-Start. This is eMule's own convention and the reason there is no
// inclusive bound here to be off by one.
type PartRequest struct {
	// FileHash is the file's 16-byte ed2k hash -- the identity from a
	// search result or a link, not something this file invents.
	FileHash [16]byte

	// Start is the file offset of the window's first byte.
	Start uint32

	// End is one past the window's last byte. It is NOT Start+PartSize by
	// rule: the last window of a file ends at the file's end, which is
	// rarely a whole number of windows.
	End uint32
}

// # WHY THE THREE PAIRS ARE PRESENT WHEN WE ASK FOR ONE
//
// eMule's OP_REQUESTPARTS carries THREE start offsets and THREE end offsets
// -- one packet, three windows. This client asks for one, and expresses that
// as three pairs with the second and third zero-length.
//
// A one-window packet of the shape we actually want is not a documented
// variant, so building one would be inventing a packet. Sending the
// documented layout with two empty pairs is not: it is the layout, with less
// in it, and a source parsing three pairs reads two of them as empty.
//
// # AND THE ORDER IS FILE HASH FIRST, THEN THE THIRD HASH
//
// eMule's comment reads <HASH 16><von[3] 4*3><bis[3] 4*3> -- the FILE's
// hash, then the offsets. Newer eMule also carries a TRANSFER hash, a
// separate identity for the transfer rather than the file, and it is not in
// this layout. Sending one the source does not expect is a packet the source
// cannot parse, so it is not sent, and the absence is commented rather than
// left for a reader to wonder about.
func (r PartRequest) Build() ([]byte, error) {
	if r.End < r.Start {
		return nil, fmt.Errorf("the window ends at %d, before it starts at "+
			"%d: End is one past the last byte, so it cannot be smaller "+
			"than Start", r.End, r.Start)
	}

	out := make([]byte, 0, 16+3*4+3*4)
	out = append(out, r.FileHash[:]...)

	// Three (start, end) pairs. Only the first carries a window; the
	// other two are zero-length, which is how a one-window request looks
	// in a three-window layout.
	for i := 0; i < 3; i++ {
		var start, end [4]byte
		if i == 0 {
			binary.LittleEndian.PutUint32(start[:], r.Start)
			binary.LittleEndian.PutUint32(end[:], r.End)
		}
		out = append(out, start[:]...)
		out = append(out, end[:]...)
	}

	return out, nil
}

// PartAnswer is a source's reply carrying one window's bytes.
//
// # EVERY FIELD IS CHECKED AGAINST WHAT WE ASKED, AND THAT IS THE POINT
//
// A source states the file hash and the range in its own answer. If those
// disagree with the request, the bytes are for something else -- a different
// file, or a different window -- and appending them to a download would
// produce a file that is corrupt in a way no later check attributes to this
// moment. So the answer is verified against the request before it is
// returned, and the mismatch is an error naming both values.
type PartAnswer struct {
	// FileHash is the file the source says these bytes are for.
	FileHash [16]byte

	// Start and End are the offsets of the bytes, half-open. The bytes run
	// from Start to End, so len(Data) == End-Start.
	Start uint32
	End   uint32

	// Data is the window's bytes. Never nil for a successful answer: a
	// zero-length window is refused, because a source answering with no
	// bytes has said nothing useful and reporting it as a successful
	// empty part is how a download appears to progress while it does not.
	Data []byte
}

// RequestPart asks the source for one window and returns its bytes.
//
// # A TIMEOUT PER WINDOW, NOT PER TRANSFER
//
// A file of a thousand windows gets a thousand deadlines, and a stalled
// window fails that window rather than the download. That is deliberate: the
// source may be slow, and the caller can decide whether to keep going. A
// single deadline for the whole file would make one slow window look like a
// dead source, and the remedy for those is different.
//
// # WHAT HAPPENS WHEN A SOURCE SAYS NOTHING
//
// A timeout here is ErrRefused, naming the address -- the same sentinel the
// handshake uses for a peer that never spoke. A source that answered the
// handshake and then went silent on a part request is, for the caller's
// purposes, a peer that is not talking, and reusing the sentinel means one
// case to handle rather than two that mean the same thing.
func (s *Source) RequestPart(ctx context.Context, r PartRequest) (*PartAnswer, error) {
	payload, err := r.Build()
	if err != nil {
		return nil, err
	}

	if !s.sentFirst {
		return nil, fmt.Errorf("%w: %s asked for a part before its "+
			"handshake was sent, and a source that receives a plain "+
			"packet first drops the connection without replying",
			ErrRefused, s.addr)
	}

	// The deadline is on the CONNECTION, which is the same call the
	// handshake makes and for the same reason: a context alone does not
	// bound a read that has already started.
	if err := s.conn.SetDeadline(time.Now().Add(dialTimeout)); err != nil {
		return nil, fmt.Errorf("cannot bound the part request to %s: %w",
			s.addr, err)
	}

	if err := s.sendFrame(protocol_EdonkeyHeader, opRequestParts, payload); err != nil {
		return nil, fmt.Errorf("%s: cannot send the part request: %w",
			s.addr, err)
	}

	return s.readPartAnswer(r)
}

// protocol_EdonkeyHeader is protocol.EdonkeyHeader under a local name.
//
// A source conversation is a plain ed2k one, and the value is what
// every other packet in this package uses. Restated rather than imported so
// this file's wire claims are readable in one place; the server file is the
// authority and a divergence between them would be a test failure.
const protocol_EdonkeyHeader byte = 0xE3

// sendFrame writes one plain packet to the source.
//
// # THE FIRST-PACKET RULE IS ENFORCED HERE, NOT AT EACH CALL SITE
//
// Every packet after the handshake is plain, and obfuscating one of those is
// how a client gets dropped mid-session after a connection that otherwise
// worked. The invariant belongs to the CONNECTION, so it is checked against
// sentFirst here: two call sites each deciding "is this the first packet
// yet" is exactly how a source ends up with two obfuscated packets.
//
// A plain packet before the obfuscated one is refused rather than sent. The
// failure would be silence -- a source that does not de-obfuscate drops the
// connection without replying -- so this is a bug in the caller rather than
// a condition to tolerate.
func (s *Source) sendFrame(protocolByte, opcode byte, payload []byte) error {
	if !s.sentFirst {
		return fmt.Errorf("refusing to send a plain packet to %s before "+
			"the connection's first packet: a source that sees an "+
			"unobfuscated first packet drops the connection without "+
			"replying, so the mistake is invisible on the wire", s.addr)
	}
	return writeFrame(s.conn, protocolByte, opcode, payload)
}

// readPartAnswer reads a source's reply to a part request.
//
// # IT ACCEPTS THE SOURCE'S OWN FRAMING OF THE ANSWER, AND NOTHING ELSE
//
// The answer is OP_SENDINGPART: a 16-byte file hash, a start offset, an end
// offset, and the bytes between. The offset pair is the authority on the
// data's length, so a frame claiming more bytes than the offsets describe is
// refused rather than truncated -- a source that sends a length the offsets
// disagree with is either confused or lying, and both are worth an error
// that says which.
//
// A part request may also come back as OP_COMPRESSEDPART (0x40), which
// carries the same fields with zlib-compressed data. This client does not
// ask for compression and does not decode it here: a compressed answer is
// reported as an unrecognised opcode with its value, because a source that
// volunteers it is doing something this step has not implemented, and
// silently trying to inflate it would turn a "not yet" into a "wrong".
func (s *Source) readPartAnswer(req PartRequest) (*PartAnswer, error) {
	hdr, payload, err := readFrame(s.conn)
	if err != nil {
		if isTimeout(err) {
			return nil, fmt.Errorf("%w: %s answered the handshake and "+
				"then said nothing about part %d-%d of %x", ErrRefused,
				s.addr, req.Start, req.End, req.FileHash[:4])
		}
		return nil, fmt.Errorf("%s: reading the part answer: %w", s.addr, err)
	}

	// # "NO FILE" IS AN ANSWER, NOT A MALFORMED PACKET
	//
	// A source that does not hold the file says so in one 16-byte packet.
	// Treating that as an error to be logged and retried against the same
	// source is how a transfer spends its whole life asking a peer that
	// will never have the file, so it gets its own named error and the
	// caller's next move is a different source.
	if hdr.Packet == opFileReqAnsNoFile {
		var hash [16]byte
		copy(hash[:], payload)
		return nil, ErrNoFile{Hash: hash}
	}

	if hdr.Packet != opSendingPart {
		return nil, fmt.Errorf("%s answered the part request with "+
			"opcode 0x%02X, which this client does not decode. Expected "+
			"0x%02X (OP_SENDINGPART) or 0x%02X (OP_FILEREQANSNOFIL)",
			s.addr, hdr.Packet, opSendingPart, opFileReqAnsNoFile)
	}

	// 16 hash + 4 start + 4 end, then the data.
	const fixedLen = 16 + 4 + 4
	if len(payload) < fixedLen {
		return nil, fmt.Errorf("%s sent a part answer of %d bytes, which "+
			"is shorter than its %d-byte header", s.addr, len(payload),
			fixedLen)
	}

	ans := PartAnswer{}
	copy(ans.FileHash[:], payload[:16])
	ans.Start = binary.LittleEndian.Uint32(payload[16:20])
	ans.End = binary.LittleEndian.Uint32(payload[20:24])
	ans.Data = payload[fixedLen:]

	// # THE SOURCE'S CLAIM IS CHECKED AGAINST OURS, FIELD BY FIELD
	//
	// A source states which file and which range these bytes are for. If
	// either disagrees with what we asked, the bytes are for something
	// else, and writing them into a download produces a file that is
	// corrupt in a way no later check attributes to this moment.
	//
	// So both are refused, by name, with both values in the message: a
	// "hash mismatch" that does not say which two hashes is a log line
	// nobody can act on during triage.
	if ans.FileHash != req.FileHash {
		return nil, fmt.Errorf("%s answered with bytes for a different "+
			"file: asked for %x, got %x", s.addr, req.FileHash[:4],
			ans.FileHash[:4])
	}
	if ans.Start != req.Start || ans.End != req.End {
		return nil, fmt.Errorf("%s answered with the wrong range: asked "+
			"for %d-%d, got %d-%d", s.addr, req.Start, req.End,
			ans.Start, ans.End)
	}

	// The length must match the offsets exactly. Truncating here would
	// write a short window and the file would fail its hash check much
	// later with nothing pointing here.
	if want := int(ans.End - ans.Start); len(ans.Data) != want {
		return nil, fmt.Errorf("%s claimed bytes %d-%d, which is %d "+
			"bytes, and sent %d", s.addr, ans.Start, ans.End, want,
			len(ans.Data))
	}

	// A zero-length window is refused: it is never a legitimate answer
	// (the last window of a file is short, not empty), and returning it
	// as success is how a download appears to progress while it does not.
	if len(ans.Data) == 0 {
		return nil, fmt.Errorf("%s sent an empty part for bytes %d-%d",
			s.addr, ans.Start, ans.End)
	}

	return &ans, nil
}
