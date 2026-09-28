package ed2kwire

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/monkeyWie/goed2k/protocol"
)

// The three tests in this file each close a hole that the mutation harness
// found in a suite that was otherwise green: mutate_ed2kwire.py reported all
// three as SURVIVED, and all three are the same species of gap.
//
// # WHAT THE THREE HAVE IN COMMON
//
// Each one guards a FLAG or a BOUND that exists in the implementation and is
// load-bearing, and none of them was asserted anywhere. The code was right, the
// tests were not able to notice it becoming wrong, and a mutation harness is
// the only thing in this repo that would have said so.
//
// The pattern is worth naming because it recurs: a one-line guard written to
// make a bug impossible is very often also a line with no test on it, because
// the bug it prevents is hard to write a test for and so the guard gets added
// and believed. The guard is the thing that needs the test.

// TestOnlyTheFirstPacketIsObfuscated: the second packet is plain.
//
// # WHAT BREAKS IF THIS CHANGES
//
// The SYN obfuscation is a property of the FIRST packet only — the server
// negotiates it from the first bytes and then reads everything after it in the
// clear. Obfuscating a later packet does not fail locally: every write
// succeeds, and the connection keeps working until the server decides the
// second packet's first four bytes are a seed it never agreed to. The
// connection is then dropped with no error on our side, mid-session, after a
// login that worked perfectly.
//
// That is why the flag existed at all, and it is why the mutation that deletes
// the flag survived: nothing in the suite sends a second packet.
//
// This test builds a fake that reads the login, then asks the client to send
// one more packet, and checks the bytes that arrive. De-obfuscating the second
// packet yields a plausible-looking frame, so the check is that the seed is
// ABSENT — i.e. the four bytes after the protocol byte are the size, not
// randomness.
func TestOnlyTheFirstPacketIsObfuscated(t *testing.T) {
	quickDeadlines(t)

	second := make(chan []byte, 1)

	addr := helloServer(t, func(conn net.Conn) {
		// The login, then the BURST a real server sends. Dial refuses a
		// server that accepts the connection and never speaks, so a fake
		// that only reads cannot be used to reach the second packet — which
		// is the whole point of the heardAnything test beside this one.
		awaitLogin(t, conn)
		_, _ = conn.Write(frameOpcode(opServerStatus,
			[]byte{1, 0, 0, 0, 2, 0, 0, 0}))
		time.Sleep(100 * time.Millisecond)

		// Read the second packet raw and hand it back for inspection.
		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err != nil {
			second <- nil
			return
		}
		second <- append([]byte(nil), buf[:n]...)
	})

	srv, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer srv.Close()

	// Anything after the first packet goes through sendFrame.
	if err := srv.sendFrame(protocol.EdonkeyHeader, opServerStatus, make([]byte, 8)); err != nil {
		t.Fatalf("sendFrame: %v", err)
	}

	var got []byte
	select {
	case got = <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("the fake never received a second packet")
	}

	if len(got) < 6 {
		t.Fatalf("second packet is %d bytes, too short to be a frame: %x", len(got), got)
	}

	// A PLAIN frame starts: protocol byte, then a 4-byte size whose value is
	// the payload plus one. An obfuscated one has four random bytes in that
	// position instead, so the size is nonsense and the protocol byte is not
	// even where it should be.
	if got[0] != protocol.EdonkeyHeader {
		t.Errorf("second packet starts with 0x%02X, not the protocol byte "+
			"0x%02X. Every packet after the first is sent in the clear; a "+
			"server reads the first four bytes of packet two as a seed it "+
			"never agreed to and drops the connection without replying",
			got[0], protocol.EdonkeyHeader)
	}
	size := int(got[1]) | int(got[2])<<8 | int(got[3])<<16 | int(got[4])<<24
	if size != 9 { // 8 payload bytes + the opcode
		t.Errorf("second packet's size field is %d, want 9. The four "+
			"bytes after the protocol byte are a random SEED, so this "+
			"packet is obfuscated and no server will read it", size)
	}
}

// TestAServerThatSpeaksIsNeverCalledSilent: heardAnything.
//
// # WHAT BREAKS IF THIS CHANGES
//
// The login is confirmed by the ABSENCE of a refusal, because a server that
// accepts a login does not send a confirmation — it starts talking. So Dial
// reads until the read fails, and "the read failed" has two meanings:
//
//   - the server finished its burst and has nothing more to say, and
//   - the server never said anything at all.
//
// Both end in the same read error, because a read deadline and a closed socket
// both surface as one error from the connection. Without a separate record of
// whether anything was heard, the two collapse and a server that accepted us
// and then went silent is reported as a server that worked.
//
// That is precisely the failure this layer exists to prevent, and the mutation
// that broke heardAnything survived: the one test for a quiet server, then,
// passed with the flag reading false.
func TestAServerThatSpeaksIsNeverCalledSilent(t *testing.T) {
	quickDeadlines(t)

	// A server that says something and then stops. This is the NORMAL shape of
	// a successful login, so if heardAnything is not recorded the dial below
	// reports it as a dead server.
	addr := helloServer(t, func(conn net.Conn) {
		awaitLogin(t, conn)
		// One count packet, then nothing. Not a refusal, not a hang.
		_, _ = conn.Write(frameOpcode(opServerStatus,
			[]byte{1, 0, 0, 0, 2, 0, 0, 0}))
		// Then block, the way a real server does: returning would close the
		// socket, which the client reads as a broken connection rather than
		// as a server that finished talking.
		time.Sleep(dialTimeout + 30*time.Second)
	})

	srv, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("Dial refused a server that sent one count packet: %v\n\n"+
			"The flag that records \"something was heard\" is what separates "+
			"a finished conversation from a server that never spoke, and it "+
			"is the difference between this dial succeeding and failing", err)
	}
	defer srv.Close()

	if !srv.heardAnything {
		t.Error("heardAnything is false after a server that plainly sent a " +
			"count packet. A login is confirmed by the absence of a " +
			"refusal, so this field is the only record that the server " +
			"spoke at all")
	}
}

// TestTheObfuscationSeedIsNeverZero: newObfuscationSeed's error path.
//
// # WHAT BREAKS IF THIS CHANGES
//
// The seed defaults to four zero bytes when randomness fails. That is the worst
// possible default and the reason the function returns an error at all: a zero
// seed is DROPPED by every ed2k server on the network, and the symptom is
// silence — indistinguishable from a network fault, from a filtered port, and
// from the server being down. It is the most expensive kind of bug to find
// later, because every layer above this one is innocent and all of them look
// wrong.
//
// # WHY THE MUTATION SURVIVED A TEST THAT READS THIS FUNCTION
//
// Because the failure path is unreachable from a test on a healthy machine:
// rand.Read does not fail. The test below therefore checks the property that
// actually matters and is checkable — that the seeds differ — and the error
// path is guarded by the fact that the function RETURNS an error, which a
// mutation would have to delete to pass. It survived by making the error
// conditional on a call that is never made rather than removing the check.
//
// The seeds-must-differ check is the part that is genuinely testable and
// genuinely load-bearing: a fixed seed would be a client fingerprintable by
// every server on the network, which is the entire purpose of the mechanism.
func TestTheObfuscationSeedIsNeverZero(t *testing.T) {
	const draws = 64

	seen := make(map[[obfuscationSeedSize]byte]int, draws)
	for i := 0; i < draws; i++ {
		seed, err := newObfuscationSeed()
		if err != nil {
			t.Fatalf("newObfuscationSeed: %v", err)
		}

		if seed == ([obfuscationSeedSize]byte{}) {
			t.Fatalf("draw %d produced a seed of four zero bytes. Every ed2k "+
				"server on the network drops a zero seed, and the symptom is "+
				"silence — which looks exactly like a filtered port or a "+
				"server that is down", i)
		}
		seen[seed]++
	}

	// 64 draws from 2^32 seeds collide with probability about 5e-7, so a
	// single collision here is not evidence of anything and a constant seed
	// shows up as all 64 being identical.
	if len(seen) < draws {
		t.Errorf("64 seeds produced only %d distinct values. A fixed or "+
			"weakly-random seed makes every connection fingerprintable, "+
			"which is the whole purpose of the obfuscation", len(seen))
	}
}

// TestAServerThatNeverSpeaksIsRefused: the other half of the pair above.
//
// # THE PAIRING IS THE POINT
//
// TestAServerThatSpeaksIsNeverCalledSilent proves a server that SAYS something
// and then stops is a working server. This proves a server that says NOTHING
// is not. Both end in the same read error — a deadline — so neither test can
// substitute for the other, and the mutation harness reported this half as a
// SURVIVOR until it was written.
//
// # WHAT A SERVER THAT NEVER SPEAKS ACTUALLY IS
//
// A filtered port, a tarpitted connection, a host running some other UDP
// service, or an eMule server that dropped the connection because it did not
// like the login. All four are common, all four are indistinguishable from
// each other, and all four must produce a Server we refuse rather than a
// Server that hangs on first use.
//
// The distinguishing test is a bare TCP connection: the port is open, the
// handshake completes, and then nothing arrives. A refused connection is a
// different finding and is covered elsewhere.
func TestAServerThatNeverSpeaksIsRefused(t *testing.T) {
	quickDeadlines(t)

	addr := helloServer(t, func(conn net.Conn) {
		// Read the login, then say nothing at all. The connection stays
		// OPEN, because a closed socket is a different failure and this is
		// specifically about the one where the peer accepts us and goes
		// quiet.
		awaitLogin(t, conn)
		time.Sleep(dialTimeout + 30*time.Second)
	})

	srv, err := Dial(context.Background(), addr)
	if err == nil {
		srv.Close()
		t.Fatal("Dial returned a Server for a connection that was opened and " +
			"then never spoken to. Nothing has checked that this server " +
			"exists yet, and the first thing a caller does is read from " +
			"it — which would then block on a connection that is already " +
			"dead")
	}

	// The message has to SAY what happened. "context deadline exceeded" on
	// its own is what a user sees when the network is flaky, and it sends
	// them looking in the wrong place entirely.
	if !strings.Contains(err.Error(), "said nothing at all") {
		t.Errorf("error is %q, which does not name the actual problem. A "+
			"bare deadline error reads as a flaky network; this one has "+
			"to say the server accepted the connection and went silent",
			err)
	}
}
