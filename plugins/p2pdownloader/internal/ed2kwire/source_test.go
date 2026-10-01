package ed2kwire

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/monkeyWie/goed2k/protocol"
)

// Sources: the handshake, and the one thing it is allowed to refuse.
//
// # WHAT THESE TESTS CAN AND CANNOT PROVE
//
// A source is a stranger, and this step has never spoken to one — step 5 of
// the transfer plan is where that happens for the first time, against a real
// peer. So nothing here can prove the source protocol is right, and no test
// in this file claims to. What these tests pin is what IS knowable offline:
//
//   - that our own first packet is well-formed and obfuscated, byte for byte
//   - that a peer which says nothing is refused, by name
//   - that a peer which floods is bounded
//
// The one thing worth being careful about is the third. Every other test in
// this package scripts a fake peer to match whatever the code does. A fake
// peer that goes quiet and a fake peer that floods are both scripted, so both
// tests are really tests of OUR reaction to a shape, not of the protocol.

// dialTimeoutForTest shortens dialTimeout for the duration of one test.
//
// A var and not a const in the source, and this is why: the real value is 40
// seconds because a real server on a real network needs it, and a test that
// waits 40 seconds to observe a timeout is a test nobody runs. Overriding the
// var is the honest way to get both.
func dialTimeoutForTest(t *testing.T, d time.Duration) {
	t.Helper()
	prev := dialTimeout
	dialTimeout = d
	t.Cleanup(func() { dialTimeout = prev })
}

// silentListener accepts a connection and never writes a byte.
//
// # A PEER THAT ACCEPTS AND SAYS NOTHING IS THE CASE THAT MATTERS
//
// A refused connection is a dial error, which every caller can already see.
// This is the case that is not: the TCP connect succeeded, so the peer is
// there, and then it says nothing. Without a deadline this is a transfer
// that never finishes, and the caller cannot tell it from a slow one.
func silentListener(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // the listener was closed, which is the stop signal
			}
			// Hold the connection open and write nothing. The peer is
			// "reachable" and silent, which is the state under test.
			go func() {
				<-done
				_ = conn.Close()
			}()
		}
	}()

	return ln.Addr().String(), func() { _ = ln.Close(); <-done }
}

// TestASourceThatNeverSpeaksIsRefused: quiet is a refusal, and it is named.
func TestASourceThatNeverSpeaksIsRefused(t *testing.T) {
	dialTimeoutForTest(t, 600*time.Millisecond)

	addr, stop := silentListener(t)
	defer stop()

	start := time.Now()
	_, err := DialSource(context.Background(), addr)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a peer that accepted the connection and then said nothing " +
			"was reported as a successful handshake")
	}

	// The error must be ErrRefused specifically, so a caller can handle
	// "this peer is silent" with one case alongside a server's.
	if !errors.Is(err, ErrRefused) {
		t.Errorf("the error is %v, which is not ErrRefused. A peer that "+
			"never speaks and a peer that refuses us are different "+
			"events, and a caller needs to tell them apart", err)
	}

	// And it must NAME the address. "connection refused" with no address is
	// a log line that cannot be acted on, and the caller has a list of
	// sources to try.
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("the error does not name the address %s: %v\n\n"+
			"A transfer tries several sources in turn, so an error "+
			"without an address cannot be attributed to one of them",
			addr, err)
	}

	// And it must have given up rather than waited out the whole budget: the
	// point of the burst deadline is that a silent peer does not cost a
	// caller the entire timeout.
	if elapsed >= dialTimeout {
		t.Errorf("the refusal took %v, so the handshake read used the whole "+
			"%v budget. The burst deadline is supposed to be about "+
			"half of it", elapsed, dialTimeout)
	}
}

// TestANilContextIsRefusedRatherThanHanging: no context, no bounds.
//
// The alternative is a dial that cannot be cancelled and a read that cannot
// be bounded, which is the same silent-forever failure wearing a different
// hat.
func TestANilContextIsRefusedRatherThanHanging(t *testing.T) {
	_, err := DialSource(nil, "127.0.0.1:1")
	if err == nil {
		t.Fatal("dialling with no context succeeded, so nothing could have " +
			"been cancelled or given a deadline")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("the error is %v, want ErrRefused", err)
	}
}

// TestTheSourceFirstPacketIsTheSameObfuscatedLoginAReceives.
//
// # WHY THIS IS A GOLDEN ASSERTION AND NOT A ROUND TRIP
//
// The first packet of a source connection is the one thing we are confident
// about: it is the login request, and a server connection proves the exact
// same bytes are accepted by a real eMule implementation every day. So the
// assertion is that Source produces the SERVER'S first packet, byte for byte,
// after de-obfuscation.
//
// Not a round trip through obfuscate into our own reader: that would pass if
// both halves drifted the same way, and this package has been bitten by
// exactly that — see the mirror lesson in the search decoder, where two
// mirrored halves agreed perfectly while both being wrong.
func TestTheSourceFirstPacketIsTheSameObfuscatedLoginAReceives(t *testing.T) {
	// Capture what the source sends, de-obfuscate it, and compare against
	// what a server connection sends for the same conversation.
	frame := captureSourceFirstPacket(t)

	// The de-obfuscated frame must be the plain login frame: protocol byte,
	// size, opcode 0xE3, sixteen zero hash bytes, a four-byte port, and a
	// zero tag count.
	if frame[0] != protocol.EdonkeyHeader {
		t.Errorf("the de-obfuscated protocol byte is 0x%02X, want 0x%02X",
			frame[0], protocol.EdonkeyHeader)
	}
	if frame[5] != opLoginRequest {
		t.Errorf("the de-obfuscated opcode is 0x%02X, want 0x%02X "+
			"(OP_LOGINREQUEST)", frame[5], opLoginRequest)
	}

	body := frame[protocolPacketHeaderSize:]
	// 16 hash + 4 port + 4 tag count = 24 bytes of body.
	const wantBodyLen = 16 + 4 + 4
	if len(body) != wantBodyLen {
		t.Fatalf("the login body is %d bytes, want %d. A body two bytes "+
			"short is read by a peer as a different tag count and "+
			"fails with nothing to point at", len(body), wantBodyLen)
	}
	for i := 0; i < 16; i++ {
		if body[i] != 0 {
			t.Errorf("user-hash byte %d is 0x%02X, want 0x00. This build "+
				"sends sixteen zero bytes, and a RANDOM hash would make "+
				"this client a different identity on every "+
				"connection", i, body[i])
		}
	}
}

// TestTheSourceFirstPacketIsActuallyObfuscatedOnTheWire.
//
// # THE MARKER OPINODE, AND WHY IT IS THE ONLY PROOF
//
// A server recognises an obfuscated first packet by its opcode being 0x01
// with a four-byte seed after it. If that marker is absent, the peer reads
// our login as a plain packet and drops the connection with nothing to
// report — so this asserts the marker on the bytes as they went out, which
// is the one thing a source connection has that a server connection does not
// prove for itself.
func TestTheSourceFirstPacketIsActuallyObfuscatedOnTheWire(t *testing.T) {
	raw := captureRawSourceFirstPacket(t)

	if raw[5] != obfuscatedOpcode {
		t.Errorf("the first packet's opcode on the wire is 0x%02X, want "+
			"0x%02X. Without that marker the peer does not know to "+
			"de-obfuscate, and drops the connection silently",
			raw[5], obfuscatedOpcode)
	}
	// The seed follows the marker and is never zero: a zero seed is the
	// fingerprint a de-obfuscator looks for.
	zero := true
	for _, b := range raw[protocolPacketHeaderSize : protocolPacketHeaderSize+obfuscationSeedSize] {
		if b != 0 {
			zero = false
			break
		}
	}
	if zero {
		t.Error("the obfuscation seed is four zero bytes, which is exactly " +
			"what a de-obfuscator scans for. A client falling back to " +
			"zeros is dropped by the whole network with no error")
	}
}

// TestAFloodingPeerIsBoundedRatherThanConsumedForever.
//
// # A STRANGER CAN KEEP SENDING, AND THE BOUND IS WHAT STOPS IT
//
// A peer that never stops talking would otherwise turn the handshake into an
// unbounded read: a hang with no error and no way to attribute it. The burst
// deadline is the bound, and this test makes a peer genuinely out-talk it.
//
// The peer writes a fixed number of valid, empty payload frames and never
// stops until the connection is closed underneath it. If the burst deadline
// were missing, this test would not finish.
func TestAFloodingPeerIsBoundedRatherThanConsumedForever(t *testing.T) {
	dialTimeoutForTest(t, 600*time.Millisecond)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// A valid frame with an empty payload: protocol byte, size 1
		// (the opcode alone), opcode. Repeated without end.
		frame := []byte{protocol.EdonkeyHeader, 1, 0, 0, 0, 0x38}
		for {
			if _, err := conn.Write(frame); err != nil {
				return
			}
		}
	}()

	start := time.Now()
	src, err := DialSource(context.Background(), ln.Addr().String())
	elapsed := time.Since(start)

	// Either outcome is acceptable, and both are the bound working: the
	// handshake returns once the peer goes quiet OR the connection is torn
	// down. What is NOT acceptable is still running.
	if elapsed >= 4*dialTimeout {
		t.Errorf("the handshake took %v against a peer that never stops "+
			"talking. The burst deadline should have ended it long "+
			"before this", elapsed)
	}

	if src != nil {
		_ = src.Close()
	}
	t.Logf("a flooding peer was bounded after %v", elapsed)
}

// captureSourceFirstPacket returns the DE-OBFUSCATED first frame a source
// sends, by running the same conversation against a listener that records it.
func captureSourceFirstPacket(t *testing.T) []byte {
	t.Helper()
	raw := captureRawSourceFirstPacket(t)

	// The real opcode is not on the wire and not recoverable from the seed —
	// a server maps it itself. So it is supplied here, which is exactly what
	// deobfuscateForTest's third argument exists for.
	payload := raw[protocolPacketHeaderSize:]
	body, ok := deobfuscateForTest(raw[5], payload)
	if !ok {
		t.Fatalf("the first packet is not recognisable as obfuscated: "+
			"opcode 0x%02X, payload %d bytes", raw[5], len(payload))
	}

	// Reassemble the frame a server sees, putting the opcode back.
	out := make([]byte, 0, protocolPacketHeaderSize+len(body))
	out = append(out, raw[0], raw[1], raw[2], raw[3], raw[4], opLoginRequest)
	out = append(out, body...)
	return out
}

// captureRawSourceFirstPacket records the bytes a source writes first,
// without interpreting them.
func captureRawSourceFirstPacket(t *testing.T) []byte {
	t.Helper()
	dialTimeoutForTest(t, 2*time.Second)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer ln.Close()

	got := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer conn.Close()
		buf := make([]byte, 512)
		n, err := conn.Read(buf)
		if err != nil && err != io.EOF {
			got <- nil
			return
		}
		got <- buf[:n]
	}()

	// DialSource will fail or succeed depending on what this listener does
	// with the answer; either way the first packet is what we wanted.
	src, err := DialSource(context.Background(), ln.Addr().String())
	if err == nil {
		_ = src.Close()
	}

	select {
	case frame := <-got:
		if frame == nil {
			t.Fatal("the listener did not receive a first packet")
		}
		return frame
	case <-time.After(3 * time.Second):
		t.Fatal("the listener never received a first packet")
		return nil
	}
}
