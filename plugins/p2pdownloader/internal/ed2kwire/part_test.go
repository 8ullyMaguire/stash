package ed2kwire

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// One window of a file, asked for and read back.
//
// # THE GOLDEN BYTES ARE HAND-WRITTEN, AND THAT IS THE POINT
//
// A part request has no tag list, so there is nothing in this package's
// machinery that could check it. A round trip through our own writer would
// pass if both halves drifted the same way -- the mistake this package has
// made before, where two mirrored halves agreed perfectly and both were
// wrong. So the expectation below is assembled from eMule's documented layout
// by hand, in the test, and the only thing the code has to agree with is
// the protocol.
//
// # AND THE LAYOUT CAME FROM CITING, NOT RECALLING
//
// The first draft of the transfer plan used opcode 0xD4 for the request,
// which is OP_PACKEDPROT -- a protocol byte this package already reads. The
// real pair is quoted from eMule's opcodes.h: OP_SENDINGPART 0x46,
// OP_REQUESTPARTS 0x47, OP_FILEREQANSNOFIL 0x48.

// theHash is a recognisable file hash, so a wrong offset is visible as a
// shifted pattern rather than plausible noise.
var theHash = [16]byte{
	0x40, 0xd3, 0x49, 0x92, 0x9c, 0x69, 0xb3, 0x73,
	0x5a, 0x1d, 0x52, 0x47, 0xb6, 0xfe, 0xdd, 0xe6,
}

// TestThePartRequestIsAHashAndThreeOffsetPairs.
//
// # 40 BYTES, AND EVERY ONE OF THEM ACCOUNTED FOR
//
// eMule's comment is `<HASH 16><von[3] 4*3><bis[3] 4*3>` -- 16 + 12 + 12.
// Only the first pair carries a window; the other two are zero-length,
// because a one-window request inside a three-window layout.
func TestThePartRequestIsAHashAndThreeOffsetPairs(t *testing.T) {
	req := PartRequest{FileHash: theHash, Start: 0, End: PartSize}

	got, err := req.Build()
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}

	// 16 hash bytes + three (start, end) pairs of 4 bytes each.
	const wantLen = 16 + 3*4 + 3*4
	if len(got) != wantLen {
		t.Errorf("the request is %d bytes, want %d. eMule's layout is "+
			"<HASH 16><von[3] 4*3><bis[3] 4*3>, and a request of any "+
			"other length is one a source cannot parse", len(got),
			wantLen)
	}

	// The whole thing, hand-assembled. This is the assertion that would
	// catch a field-order or width change, which the length check above
	// cannot.
	var want []byte
	want = append(want, theHash[:]...)
	for i := 0; i < 3; i++ {
		var start, end [4]byte
		if i == 0 {
			binary.LittleEndian.PutUint32(start[:], req.Start)
			binary.LittleEndian.PutUint32(end[:], req.End)
		}
		want = append(want, start[:]...)
		want = append(want, end[:]...)
	}

	if !bytes.Equal(got, want) {
		t.Errorf("the request bytes are\n  %s\nwant\n  %s",
			hex.EncodeToString(got), hex.EncodeToString(want))
	}
}

// TestTheFirstPairIsTheWindowAndTheRestAreEmpty.
//
// # WHY THE EMPTY PAIRS MATTER
//
// eMule batches three windows per packet. This client asks for one. If the
// empty pairs were dropped -- a three-window packet shrunk to one, or the
// pairs run together as start,start,start,end,end,end -- a source reading
// three start offsets and three end offsets would read the wrong bytes, and
// the file would fail its hash check much later with nothing pointing here.
func TestTheFirstPairIsTheWindowAndTheRestAreEmpty(t *testing.T) {
	req := PartRequest{FileHash: theHash, Start: 19000, End: 19000 + PartSize}
	got, err := req.Build()
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	// Pairs start at 16. Pair i's start is at 16+i*8 and its end at 20+i*8.
	for i := 0; i < 3; i++ {
		start := binary.LittleEndian.Uint32(got[16+i*8 : 20+i*8])
		end := binary.LittleEndian.Uint32(got[20+i*8 : 24+i*8])
		if i == 0 {
			if start != req.Start || end != req.End {
				t.Errorf("pair 0 is %d-%d, want %d-%d", start, end,
					req.Start, req.End)
			}
			continue
		}
		if start != 0 || end != 0 {
			t.Errorf("pair %d is %d-%d, want 0-0. A one-window request "+
				"leaves the other two pairs empty; a source reading "+
				"three windows would read the wrong bytes", i,
				start, end)
		}
	}
}

// TestTheRequestCarriesNoTagCount: the asymmetry with every other packet.
//
// # A HASH LOOKS LIKE A TAG COUNT IF YOU LET IT
//
// Every other packet in this package begins with a four-byte tag count. A
// part request begins with a 16-byte hash, and the first four bytes of a
// hash are arbitrary -- so handing a part request to parseTagList reads four
// bytes of a file's identity as a count of tags that do not exist.
//
// That is the reason the decoders here share no machinery with the tag
// codec, and this test is what makes the reason checkable.
func TestTheRequestCarriesNoTagCount(t *testing.T) {
	req := PartRequest{FileHash: theHash, Start: 0, End: PartSize}
	got, err := req.Build()
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	// The first four bytes are the hash's, NOT a count. For this hash
	// they are 0x92 0x49 0xd3 0x40 -- a count of about 2.4 billion, which
	// is exactly the nonsense a tag reader would report.
	first := binary.LittleEndian.Uint32(got[:4])
	if first == 1 {
		t.Error("the request begins with a tag count of 1. A part request " +
			"carries no tag list; the first four bytes are the file " +
			"hash's, and reading them as a count is the bug this " +
			"asymmetry exists to prevent")
	}
	if !bytes.Equal(got[:16], theHash[:]) {
		t.Error("the request does not begin with the file hash")
	}
}

// TestAWindowThatEndsBeforeItStartsIsRefused: the one Build can refuse.
func TestAWindowThatEndsBeforeItStartsIsRefused(t *testing.T) {
	// End is one past the last byte, so it cannot be below Start.
	_, err := PartRequest{FileHash: theHash, Start: 9500, End: 9000}.Build()
	if err == nil {
		t.Error("a window ending before it starts was accepted. End is " +
			"one past the last byte, so End < Start is not a short " +
			"window but a request for no bytes with a nonzero length")
	}
}

// TestAZeroLengthWindowIsStillAValidRequest.
//
// # BECAUSE REUSING THE REFUSAL WOULD CONFLATE TWO THINGS
//
// Build refuses End < Start and accepts End == Start, because the second is
// an empty window -- a legitimate request that a source answers with no
// bytes. The ANSWER is where an empty window is refused, because a source
// that returns nothing has said nothing useful. Mixing the two would make
// either check unreachable.
func TestAZeroLengthWindowIsStillAValidRequest(t *testing.T) {
	got, err := PartRequest{FileHash: theHash, Start: 500, End: 500}.Build()
	if err != nil {
		t.Fatalf("an empty window at 500 was refused: %v. Build refuses "+
			"only End < Start; an empty window is a valid REQUEST and "+
			"it is the empty ANSWER that is refused", err)
	}
	if len(got) != 16+3*8 {
		t.Errorf("the request is %d bytes, want %d", len(got), 16+3*8)
	}
}

// partAnswerFrame builds a peer's OP_SENDINGPART answer.
func partAnswerFrame(hash [16]byte, start, end uint32, data []byte) []byte {
	payload := make([]byte, 0, 24+len(data))
	payload = append(payload, hash[:]...)
	var b4 [4]byte
	binary.LittleEndian.PutUint32(b4[:], start)
	payload = append(payload, b4[:]...)
	binary.LittleEndian.PutUint32(b4[:], end)
	payload = append(payload, b4[:]...)
	payload = append(payload, data...)
	return frameOpcode(opSendingPart, payload)
}

// sourcePeer is a fake source: it performs the handshake, then answers the
// next packet with whatever answer the test supplies.
type sourcePeer struct {
	answer []byte
	opcode byte
	wait   time.Duration
	// got records the part request this client sent, for assertions.
	got []byte
}

func (p *sourcePeer) serve(t *testing.T, ln net.Listener) {
	t.Helper()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// Held open by a channel rather than deferred, so a peer that is
		// meant to be silent can close only when the test ends.
		release := make(chan struct{})
		defer close(release)
		defer conn.Close()

		// Read and discard the obfuscated handshake.
		buf := make([]byte, 512)
		if _, err := conn.Read(buf); err != nil {
			return
		}
		// Answer the handshake with one plain packet, so DialSource's
		// "did anything arrive" check passes.
		if _, err := conn.Write(frameOpcode(opServerStatus, []byte{1, 0, 0, 0})); err != nil {
			return
		}

		if p.wait > 0 {
			// Say nothing at all, and keep the connection open. Closing
			// it here would be EOF, which is a DIFFERENT failure from
			// silence -- and the deadline is the thing under test.
			<-release
			return
		}

		// Read the part request.
		n, err := conn.Read(buf)
		if err != nil && err != io.EOF {
			return
		}
		p.got = buf[:n]

		if p.answer == nil {
			return
		}
		if _, err := conn.Write(p.answer); err != nil {
			return
		}
		// Give the client time to read before the close.
		time.Sleep(50 * time.Millisecond)
	}()
}

// dialTestSource connects to a peer, returning a Source for tests.
func dialTestSource(t *testing.T, peer *sourcePeer) *Source {
	t.Helper()

	// # WHY 300ms AND NOT THE REAL BUDGET
	//
	// DialSource reads the peer's opening burst until it goes quiet, so every
	// dial costs half the budget before the part request is even sent. At the
	// 3s this test file originally used, the 24-iteration truncation loop
	// took 36 seconds -- long enough that a suite people are told to run
	// gets run less often, which is the worst outcome a test can have.
	//
	// 300ms is ample for a listener on the loopback interface, and the tests
	// that genuinely need patience (the silent peer) set their own budget.
	dialTimeoutForTest(t, 300*time.Millisecond)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	peer.serve(t, ln)

	src, err := DialSource(context.Background(), ln.Addr().String())
	if err != nil {
		t.Fatalf("dialling the test source: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src
}

// TestOnePartComesBackWithTheBytesWeAskedFor: the whole round trip.
func TestOnePartComesBackWithTheBytesWeAskedFor(t *testing.T) {
	// 9,500 bytes of something recognisable: each byte is its own index.
	data := make([]byte, PartSize)
	for i := range data {
		data[i] = byte(i)
	}
	peer := &sourcePeer{answer: partAnswerFrame(theHash, 0, PartSize, data)}
	src := dialTestSource(t, peer)

	ans, err := src.RequestPart(context.Background(),
		PartRequest{FileHash: theHash, Start: 0, End: PartSize})
	if err != nil {
		t.Fatalf("RequestPart: %v", err)
	}

	if len(ans.Data) != PartSize {
		t.Errorf("the answer carries %d bytes, want %d", len(ans.Data), PartSize)
	}
	if !bytes.Equal(ans.Data, data) {
		t.Error("the bytes came back altered")
	}
	if ans.Start != 0 || ans.End != PartSize {
		t.Errorf("the answer states %d-%d, want 0-%d", ans.Start, ans.End,
			PartSize)
	}
	if ans.FileHash != theHash {
		t.Error("the answer names a different file")
	}

	// And the request we actually put on the wire is the golden one.
	if len(peer.got) > 0 {
		want, _ := PartRequest{FileHash: theHash, Start: 0, End: PartSize}.Build()
		if got := peer.got[len(peer.got)-len(want):]; !bytes.Equal(got, want) {
			t.Errorf("the bytes on the wire are\n  %s\nwant\n  %s",
				hex.EncodeToString(got), hex.EncodeToString(want))
		}
	}
}

// TestASourceThatSaysItDoesNotHaveTheFileSaysSoByName.
//
// # THE MOST ORDINARY FAILURE, AND IT MUST NOT LOOK LIKE A BUG
//
// "This source does not have the file" is a normal answer to a normal
// question. It gets its own error type because the caller's next move is a
// different source -- and folding it into a generic error is how a transfer
// spends its whole life asking a peer that will never have the file.
func TestASourceThatSaysItDoesNotHaveTheFileSaysSoByName(t *testing.T) {
	peer := &sourcePeer{answer: frameOpcode(opFileReqAnsNoFile, theHash[:])}
	src := dialTestSource(t, peer)

	_, err := src.RequestPart(context.Background(),
		PartRequest{FileHash: theHash, Start: 0, End: PartSize})
	if err == nil {
		t.Fatal("a source saying it does not have the file was reported as a " +
			"successful part")
	}

	var noFile ErrNoFile
	if !errors.As(err, &noFile) {
		t.Errorf("the error is %v, which is not ErrNoFile. A caller needs to "+
			"tell 'this source does not have it' from 'this source "+
			"failed', because the remedy is a different source either way",
			err)
	}
	if noFile.Hash != theHash {
		t.Error("the error does not carry the hash of the file that was " +
			"refused, so a caller cannot log which file it was")
	}
}

// TestAnAnswerForADifferentFileIsRefused.
//
// # A SOURCE'S CLAIM IS CHECKED AGAINST OURS
//
// A source states which file its bytes are for. If that disagrees with the
// request, the bytes are for something else, and writing them into a
// download produces a file that is corrupt in a way no later check
// attributes to this moment. So it is refused, with both values named.
func TestAnAnswerForADifferentFileIsRefused(t *testing.T) {
	other := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	peer := &sourcePeer{
		answer: partAnswerFrame(other, 0, 4, []byte{9, 9, 9, 9}),
	}
	src := dialTestSource(t, peer)

	_, err := src.RequestPart(context.Background(),
		PartRequest{FileHash: theHash, Start: 0, End: 4})
	if err == nil {
		t.Fatal("bytes for a different file were accepted as our own")
	}
	var noFile ErrNoFile
	if errors.As(err, &noFile) {
		t.Error("a wrong-file answer was reported as ErrNoFile, which says " +
			"the source does not HAVE the file. It has a different one, " +
			"and those are different problems")
	}
	// The message must name both hashes, or it is a log line nobody can
	// act on during triage. They appear as HEX, since that is how a hash
	// is written everywhere else in this package -- comparing against the
	// raw four bytes would fail against a message that is correct.
	if msg := err.Error(); !strings.Contains(msg, hex.EncodeToString(other[:4])) {
		t.Errorf("the error does not name the hash the source sent: %v", err)
	}
	// And ours, so the two can be told apart in the log.
	if msg := err.Error(); !strings.Contains(msg, hex.EncodeToString(theHash[:4])) {
		t.Errorf("the error does not name the hash we asked for: %v", err)
	}
}

// TestAnAnswerForTheWrongRangeIsRefused: same rule, different field.
func TestAnAnswerForTheWrongRangeIsRefused(t *testing.T) {
	peer := &sourcePeer{
		answer: partAnswerFrame(theHash, 100, 104, []byte{1, 2, 3, 4}),
	}
	src := dialTestSource(t, peer)

	_, err := src.RequestPart(context.Background(),
		PartRequest{FileHash: theHash, Start: 0, End: 4})
	if err == nil {
		t.Fatal("bytes for a different range were accepted as our own")
	}
	if msg := err.Error(); !bytes.Contains([]byte(msg), []byte("19000")) &&
		!bytes.Contains([]byte(msg), []byte("0-4")) {
		t.Errorf("the error does not name the range we asked for: %v", err)
	}
}

// TestAShortAnswerIsRefusedRatherThanTruncated.
//
// # THE LENGTH MUST MATCH THE OFFSETS, EXACTLY
//
// Truncating would write a short window, and the file would fail its hash
// check much later with nothing pointing back here. A source whose declared
// range and byte count disagree is confused or lying, and both are worth an
// error that says which.
func TestAShortAnswerIsRefusedRatherThanTruncated(t *testing.T) {
	// Declares 0-100 but sends 10 bytes.
	peer := &sourcePeer{
		answer: partAnswerFrame(theHash, 0, 100, make([]byte, 10)),
	}
	src := dialTestSource(t, peer)

	_, err := src.RequestPart(context.Background(),
		PartRequest{FileHash: theHash, Start: 0, End: 100})
	if err == nil {
		t.Fatal("an answer declaring 100 bytes and sending 10 was accepted")
	}
	if msg := err.Error(); !bytes.Contains([]byte(msg), []byte("100")) {
		t.Errorf("the error does not say how many bytes were expected: %v", err)
	}
}

// TestAnEmptyAnswerIsRefused: progress that is not progress.
//
// A zero-length answer is never legitimate -- the last window of a file is
// short, not empty -- and returning it as success is how a download appears
// to advance while it does not.
func TestAnEmptyAnswerIsRefused(t *testing.T) {
	peer := &sourcePeer{answer: partAnswerFrame(theHash, 0, 0, nil)}
	src := dialTestSource(t, peer)

	_, err := src.RequestPart(context.Background(),
		PartRequest{FileHash: theHash, Start: 0, End: 0})
	if err == nil {
		t.Fatal("an empty part was reported as a successful answer")
	}
}

// TestAnUnknownOpcodeIsNamedRatherThanTried.
//
// # 0x40 IS OP_COMPRESSEDPART AND IS DELIBERATELY NOT INFLATED HERE
//
// A compressed part has the same fields with zlib-compressed data. This
// client does not ask for compression. Silently trying to inflate a
// volunteer packet would turn "not implemented yet" into "wrong", and the
// difference matters: the first is a known gap and the second is a bug.
func TestAnUnknownOpcodeIsNamedRatherThanTried(t *testing.T) {
	const opCompressedPart byte = 0x40
	payload := append([]byte{0x78, 0x9C}, []byte("a plausible zlib stream")...)
	peer := &sourcePeer{answer: frameOpcode(opCompressedPart, payload)}
	src := dialTestSource(t, peer)

	_, err := src.RequestPart(context.Background(),
		PartRequest{FileHash: theHash, Start: 0, End: 4})
	if err == nil {
		t.Fatal("a compressed part answer was accepted by a client that " +
			"never asked for one")
	}
	// The error must name the opcode it did not recognise.
	if msg := err.Error(); !bytes.Contains([]byte(msg), []byte("0x40")) {
		t.Errorf("the error does not name the opcode 0x%02X: %v",
			opCompressedPart, err)
	}
}

// TestASilentSourceOnAPartRequestIsRefusedByName.
//
// # THE HANDSHAKE SUCCEEDED AND THE PART DID NOT
//
// A source that answered the handshake and then went silent on a part
// request is, for the caller's purposes, a peer that is not talking. It
// reuses ErrRefused -- the same sentinel the handshake uses -- so a caller
// handles one case rather than two that mean the same thing.
func TestASilentSourceOnAPartRequestIsRefusedByName(t *testing.T) {
	peer := &sourcePeer{wait: 10 * time.Millisecond}
	src := dialTestSource(t, peer)

	start := time.Now()
	_, err := src.RequestPart(context.Background(),
		PartRequest{FileHash: theHash, Start: 0, End: PartSize})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a source that said nothing about the part was reported as a " +
			"successful answer")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("the error is %v, which is not ErrRefused. Silence after a "+
			"good handshake is a refusal, and reusing the sentinel is "+
			"what lets a caller handle it in one case", err)
	}
	// The whole window budget IS the right amount of time here: the peer
	// never answers, so the deadline is what ends the wait. What must not
	// happen is exceeding it -- a read with no deadline would block past
	// the budget and the assertion below is what catches that.
	//
	// A test that asserted the request finished QUICKLY would be testing
	// the opposite of what matters: a source that never answers can only
	// be discovered by waiting.
	if elapsed > 2*dialTimeout {
		t.Errorf("the request took %v, well past the %v budget, so the "+
			"read was not bounded by the connection deadline",
			elapsed, dialTimeout)
	}
}

// TestAPartBeforeTheHandshakeIsRefused.
//
// # SENT-FIRST IS CHECKED AT THE CONNECTION, NOT AT EACH CALL SITE
//
// A plain packet before the obfuscated first one is dropped by a real source
// with no reply, so this is a bug in the caller rather than a condition to
// tolerate. Constructing the Source without going through DialSource is the
// only way to reach it, and that is deliberate: the check has to be provable.
func TestAPartBeforeTheHandshakeIsRefused(t *testing.T) {
	// A Source whose first packet was never sent.
	src := &Source{addr: "127.0.0.1:1"}

	_, err := src.RequestPart(context.Background(),
		PartRequest{FileHash: theHash, Start: 0, End: PartSize})
	if err == nil {
		t.Fatal("a part request was sent before the handshake")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("the error is %v, which does not name the cause", err)
	}
	if msg := err.Error(); !bytes.Contains([]byte(msg), []byte("handshake")) {
		t.Errorf("the error does not say the handshake was missing: %v", err)
	}
}

// TestATruncatedAnswerIsRefusedRatherThanIndexedPast.
//
// # THE MUTATION THAT FOUND THIS ONE
//
// The length guard on a part answer's fixed 24-byte header survived a full
// mutation run, which meant no test sent a payload too short to hold it. The
// guard is not decorative: without it, a 20-byte answer has payload[20:24]
// read four bytes past the end, which is a slice panic -- the one thing a
// wire decoder must never do with a stranger's bytes.
//
// So this test exists because a probe survived, which is the probe doing its
// job: a guard nobody exercises is indistinguishable from a guard nobody
// needs, and only one of those is safe.
func TestATruncatedAnswerIsRefusedRatherThanIndexedPast(t *testing.T) {
	// Every length from nothing up to one byte short of the header. A
	// decoder that is right for one of them and wrong for the rest is a
	// decoder with an off-by-one, and the loop is what finds it.
	for n := 0; n < 24; n++ {
		peer := &sourcePeer{
			answer: frameOpcode(opSendingPart, make([]byte, n)),
		}
		src := dialTestSource(t, peer)

		_, err := src.RequestPart(context.Background(),
			PartRequest{FileHash: theHash, Start: 0, End: PartSize})
		if err == nil {
			t.Fatalf("a %d-byte part answer was accepted. The header alone "+
				"is 24 bytes, so this is a packet that cannot be one",
				n)
		}
		// And the message must say what was too short, because a bare
		// error leaves the reader guessing which field was missing.
		if !bytes.Contains([]byte(err.Error()), []byte("shorter")) {
			t.Errorf("a %d-byte answer gave %v, which does not say the "+
				"payload was too short to be a part answer", n, err)
		}
	}
}

// TestAOneByteShortAnswerIsStillRefused: the boundary itself.
//
// Twenty-three bytes is the case a `< 24` guard written as `<= 24` would
// miss, and one byte too few is a packet no source means to send. Looped
// over in the test above; named here because the boundary is the part worth
// reading twice.
func TestAOneByteShortAnswerIsStillRefused(t *testing.T) {
	peer := &sourcePeer{answer: frameOpcode(opSendingPart, make([]byte, 23))}
	src := dialTestSource(t, peer)

	if _, err := src.RequestPart(context.Background(),
		PartRequest{FileHash: theHash, Start: 0, End: PartSize}); err == nil {
		t.Error("a 23-byte answer was accepted; the header is 24 bytes")
	}
}

// TestAHeaderOnlyAnswerWithNoDataIsRefused.
//
// Twenty-four bytes is a valid header describing a zero-length window, and it
// is refused for a different reason than a 23-byte one: there is no room for
// data AND the window is empty. The empty-window refusal is the load-bearing
// one here, since a caller that treated this as success would count a part it
// never received.
func TestAHeaderOnlyAnswerWithNoDataIsRefused(t *testing.T) {
	peer := &sourcePeer{answer: frameOpcode(opSendingPart, make([]byte, 24))}
	src := dialTestSource(t, peer)

	_, err := src.RequestPart(context.Background(),
		PartRequest{FileHash: theHash, Start: 0, End: 0})
	if err == nil {
		t.Error("a header-only answer describing an empty window was accepted")
	}
}
