// Package ed2kwire is the ed2k NETWORK layer: the server connection, the Kad
// bootstrap, and the eMule extended handshake.
//
// # WHY THIS IS A SEPARATE PACKAGE FROM internal/ed2k
//
// It is a package boundary, not a style preference. `internal/ed2k` owns the
// eHash and the locator grammar, and it must never be able to reach the wire
// library: `github.com/monkeyWie/goed2k` computes the ed2k tree hash WRONG. It
// MD4s the concatenated FULL 16-byte part hashes, where the protocol requires
// each part hash truncated to its first 8 bytes. Measured on a 19,456,000-byte
// input (two exact parts):
//
//	goed2k.HashFromHashSet([]Hash{p0, p1}) -> 90955B3AFD7D14B68B672C584F88DD93
//	the ed2k-correct value                 -> 735E6A43667B72334F8E27F9C46D263B
//	internal/ed2k.HashFile                 -> 735E6A43667B72334F8E27F9C46D263B
//
// The failure is silent and total: every multi-part file hashes to a value
// matching nothing on the real network, while the library's own tests pass,
// because its tree hash agrees with itself. Nothing in a unit suite can catch
// a hash that is wrong by agreement.
//
// So the split is enforced by the import graph. A `grep -rn goed2k
// internal/ed2k/` must return nothing, and `TestTheHashPackageCannotReachTheWire`
// is what keeps it true: the wire library is imported HERE, and the hasher is
// not allowed to know this package exists.
//
// # WHAT IS BORROWED AND WHAT IS NOT
//
// Borrowed, because re-deriving it would be re-deriving it WRONG: the packet
// framing (`protocol.PacketHeader`, 6 bytes, little-endian, `SizePacket() ==
// Size-1`), the opcode set, the hello/hello-answer packets, the tag list, the
// Kad message set, and `nodes.dat` parsing. All of it is MIT-licensed and all
// of it is encoding a wire format rather than making a decision about it.
//
// Not borrowed: the client hash (ours, and correct), the consent gate, the
// library hand-off, rate limits, and every refusal. A packet codec is a
// protocol fact; whether a download proceeds is ours.
package ed2kwire

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/monkeyWie/goed2k/protocol"
)

var (
	// ErrNotAServer: what answered is not an ed2k server. Named separately
	// from a transport error because the two need different operator action —
	// a refused dial is a network problem, and a TCP connection that speaks
	// something else is a DNS or captive-portal problem wearing a network
	// problem's clothes.
	ErrNotAServer = errors.New("what answered is not an ed2k server")

	// ErrTruncatedPacket: a packet header promised more bytes than arrived.
	// There is no retry here and cannot be: the byte stream is a sequence of
	// length-prefixed packets, so a short read means the offsets for every
	// packet after it are wrong. Reconnecting is the only correct recovery.
	ErrTruncatedPacket = errors.New("the connection ended mid-packet")

	// ErrRefused: this plugin will not talk to that server. A refusal, not an
	// error in the transfer.
	ErrRefused = errors.New("refused")
)

// maxPacketSize caps a single packet's payload.
//
// The protocol's own ceiling is far larger, but nothing this client sends or
// legitimately receives approaches it, and the size arrives from a stranger in
// the first 6 bytes of every packet. Without a cap, one crafted header claims
// 2 GiB and the read either exhausts memory or blocks until the context
// expires — either way a remote peer chooses our resource use.
//
// 1 MiB is generous: the largest thing sent here is a bitfield or a tag list.
const maxPacketSize = 1 << 20

// dialTimeout bounds the connect AND the whole login exchange, because a
// server that accepts a TCP connection and then says nothing is a real and
// common occurrence — a filtered port, a server that is up but not serving, a
// tarpitted connection. Without a deadline on the read, Dial hangs forever
// inside a call that looks like it is working.
//
// # 40s, AND THE SUM OF THE PARTS IS WHY
//
// This must exceed burstDrainTimeout plus factsDrainTimeout, because those
// two are set INSIDE the window this bounds. At 20s it did not: a real
// server measured 3.1s to its first packet and then 11.4s of quiet, which
// fits, but only just, and the first version of the constants had the sum
// exceed this — so the dial deadline, not the drain deadline, was ending the
// exchange and every quiet server was reported as broken.
//
// The relationship is asserted rather than left to arithmetic in a comment:
// see TestTheDialDeadlineExceedsBothDrainDeadlines.
//
// It is a var rather than a const so the hermetic tests can shrink it. A test
// that has to wait 15 seconds to observe a quiet server is a test nobody runs,
// and a suite that is not run is a suite that is not protecting anything. The
// MEASURED values are pinned by TestTheRealDeadlinesAreTheMeasuredOnes, so
// shrinking them for tests cannot quietly make production wrong.
var dialTimeout = 40 * time.Second

// Server is one ed2k server we are connected to.
//
// It is a connection, not a session: the server's user list, the searches, and
// the source exchanges all live above this, and none of them are in scope yet.
type Server struct {
	addr string
	conn net.Conn

	// The server's OWN totals, from the 0x40 packet it sends first. Kept
	// apart from users/files because a server reports both: its own view of
	// its size, and a larger network-wide count. Reporting one and calling
	// it the other is how a number ends up 8x wrong.
	//
	// These are CLAIMS from a stranger, like every other count here.
	totalUsers int32
	totalFiles int32

	// heardAnything records that the server sent at least one packet we
	// understood. It is what separates "the conversation finished" from
	// "the server never spoke", because both end in the same read error.
	heardAnything bool

	// What the server said about itself: a user count, a file count, a tag
	// list, and messages in words.
	//
	// These are CLAIMS from a stranger and are carried as claims. A server
	// can report any value here, including a negative one, and nothing in
	// the protocol checks it — so they are never used to size an allocation,
	// a buffer, or a progress bar's denominator.
	users    int32
	files    int32
	tags     TagList
	messages []string

	// tagErr records a tag list this client could not parse while leaving
	// the connection usable.
	//
	// A parse failure here is NOT a login failure. The login was confirmed
	// by OP_IDCHANGE before OP_SERVERINFO arrived, so the server accepted us
	// and then said something this client does not model. Failing the
	// connection would throw away a working connection over a banner, and
	// dropping the error would hide a real protocol difference — so it is
	// kept and a caller can ask for it.
	tagErr error

	// sentFirst records that the connection's first packet has gone out, and
	// so is the only packet allowed to have been obfuscated.
	//
	// A bool on the Server rather than a parameter passed to each send,
	// because the invariant is about the CONNECTION and not about a call. Two
	// independent call sites each deciding "is this the first packet?" is how
	// a connection ends up with two obfuscated packets, which a server
	// answers by dropping the connection with nothing to point at.
	sentFirst bool
}

// Addr is the address this server was reached at.
func (s *Server) Addr() string { return s.addr }

// ServerUsers and ServerFiles are the server's own totals, from the 0x40
// packet it sends immediately after a login.
//
// They are separate from Users and Files because a server reports both, and
// on ed2k-rust the larger pair is roughly eight times the server's own — so
// showing one while calling it the other is a number that is simply wrong.
//
// # THERE IS NO SESSION TOKEN, AND THERE USED TO BE ONE
//
// This accessor was called Hash and returned sixteen bytes read from the 0x40
// packet as though it were a GUID. It changed on every connection and
// contained the user count followed by the server's own address. No ed2k
// server sends a session token in the login response, and inventing an
// accessor for one is how a caller ends up using it.
//
// What confirms a login is the absence of a refusal: the server decoded an
// obfuscated request and started sending its own status. A server that
// declines sends OP_SERVERMESSAGE instead, and that is ErrRefused.
func (s *Server) ServerUsers() int32 { return s.totalUsers }
func (s *Server) ServerFiles() int32 { return s.totalFiles }

// Tags is what the server said about itself in its OP_SERVERINFO packet.
//
// Tags is nil when the server sent no info packet, and TagErr is non-nil when
// it sent one this client could not read. The connection is usable in both
// cases, because the login was confirmed before the info packet arrived.
func (s *Server) Tags() TagList { return s.tags }

// Messages is what the server said in words: banners, warnings and refusals.
// An operator debugging a flaky server needs the server's own words, and a
// banner is often the only explanation for a connection that worked.
func (s *Server) Messages() []string { return s.messages }

// TagErr is why the server's info packet could not be read, or nil.
//
// A failure here is NOT a login failure: the server confirmed the login with
// OP_IDCHANGE before sending OP_SERVERINFO, so it accepted us and then said
// something this client does not model. Refusing the connection would throw
// away a working connection over a banner, and dropping the error would hide a
// real protocol difference — so it is kept and a caller can ask.
func (s *Server) TagErr() error { return s.tagErr }

// Users is the user count the server advertised in its hello.
//
// It is a number a stranger sent us, so it is a claim and not a measurement.
// A server can report anything here, including a negative count, and nothing
// in the protocol checks it. It is carried as reported and never used to size
// an allocation or a buffer.
func (s *Server) Users() int32 { return s.users }

// Files is the file count the server advertised. Same caveat as Users.
func (s *Server) Files() int32 { return s.files }

// Close releases the connection. Safe to call twice.
func (s *Server) Close() error { return s.conn.Close() }

// Dial connects to an ed2k server, reads its hello and sends ours.
//
// # WHY THE SERVER SPEAKS FIRST
//
// It is the protocol's order, not a politeness: a server sends HELLO on accept
// and a client answers. Getting it backwards is not symmetric — a client that
// sends first to a server expecting to receive would have its packet treated as
// the answer to a hello that never came, and the desync is indistinguishable
// from a server that hung.
//
// # THE TCP-SYN HALF OF OBFUSCATION IS NOT FULLY AVAILABLE HERE, AND HERE IS WHY
//
// eMule's obfuscation is two things: the SYN carries no TCP options, and the
// first payload packet is transformed. The second is `obfuscate` and it works.
// The first needs a socket that does not set window scale, SACK or timestamps,
// and Go's net.Dialer does not expose the switch — setting it requires a raw
// socket and syscall-level option manipulation.
//
// What that costs in practice, measured on 2026-09-28: the obfuscated first
// packet alone is enough for 3 of 10 public servers to answer, including
// ed2k-rust, which runs a full handshake. So the payload half is not a
// partial implementation of nothing — it is the part that decides whether a
// server answers at all.
//
// The remaining TCP-options half is a known gap, not a hidden one, and
// TestTheObfuscatedFirstPacketIsAcceptedByALiveServer in live_test.go is where
// it would show up as a server that still declines. Whether Go's runtime can
// do it without a raw socket is a question for the live tests, not a claim
// this package makes.

// Dial connects to an ed2k server, sends a login request obfuscated as the
// connection's first packet, and reads the server's hello.
//
// # WHAT IS REFUSED, AND WHY EACH REFUSAL IS HERE RATHER THAN LEFT TO THE CALLER
//
//  1. A protocol byte that is not E3 or C5. This is the captive-portal and
//     wrong-service case: the TCP connect succeeded and something answered.
//  2. A size field larger than maxPacketSize, for the reason on that constant.
//  3. A payload shorter than the header promised, for the reason on
//     ErrTruncatedPacket.
//  4. A read that outruns the deadline, for the reason on dialTimeout.
//
// A refusal that happens HERE costs one connection. The same refusal a caller
// has to remember to perform is a refusal that will be forgotten.
func Dial(ctx context.Context, addr string) (*Server, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: no context, so nothing can be cancelled "+
			"or given a deadline", ErrRefused)
	}

	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dialling the ed2k server at %s: %w", addr, err)
	}

	// The deadline is set on the CONNECTION, not just the context, because
	// Go's net.Conn does not consult a context after the connect. Without
	// this the context expires and the read blocks anyway — the exact
	// "silently does nothing" failure this whole layer exists to avoid.
	if err := conn.SetDeadline(time.Now().Add(dialTimeout)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cannot bound the hello read on %s: %w", addr, err)
	}

	srv := &Server{addr: addr, conn: conn}

	// # THE CLIENT SPEAKS FIRST, AND GETTING THIS BACKWARDS DEADLOCKS
	//
	// The ed2k server protocol is client-first: a client connects and sends
	// OP_LOGINREQUEST, and only then does the server answer with OP_HELLO.
	// A server sends nothing until it has been spoken to.
	//
	// This file originally read the server's hello FIRST and answered it. That
	// is the shape of the Kad hello, not the ed2k one, and against a real
	// network it deadlocks: the server waits for our login and we wait for
	// its hello, and both waits are satisfied by nothing. The live test showed
	// it as `reading the packet header: EOF` on all five public servers — and
	// every hermetic test still passed, because the fake servers in
	// server_test.go had been scripted to match the wrong assumption.
	//
	// A suite that agrees with the bug it is testing is the reason the live
	// tests exist. What made the diagnosis possible was probing the same
	// server with a bare socket and no ed2k logic at all: that also read
	// nothing, which said the fault was ours and not the network's.
	if err := srv.sendFirstPacket(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%s: cannot send the login request: %w",
			addr, err)
	}

	// Read the server's opening burst, then keep reading until it goes
	// quiet. There is no confirmation packet to wait for — see
	// readLoginConfirmation for the measurements that established it, and
	// for why the first version of this file timed out against every server
	// on the network while waiting for a hello that does not exist.
	//
	// readLoginConfirmation ends by handing over to drainFacts itself, when
	// the server goes quiet. Calling drainFacts again here would be a second
	// 400ms wait for a server that has already finished talking, and the
	// second wait would always be the one that times out — reporting a
	// slow server for a conversation that is already complete.
	if err := srv.readLoginConfirmation(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%s: %w", addr, err)
	}

	// The deadline has done its job. Leaving it in place would fail the first
	// real transfer read 20 seconds in, which reads as a flaky network rather
	// than as a forgotten ClearDeadline.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cannot release the hello deadline on %s: %w",
			addr, err)
	}

	return srv, nil
}

// readLoginConfirmation reads packets until the server confirms the login.
//
// # A SERVER CONFIRMS WITH OP_IDCHANGE, AND SENDS NO HELLO
//
// The opcodes that arrive after a login request, in the order a live server
// sends them (85.17.116.222, 2026-09-28):
//
//	0x40  OP_IDCHANGE     the login is accepted; the payload is a new GUID
//	0x34  OP_SERVERSTATUS user and file counts
//	0x38  OP_SERVERMESSAGE banners
//	0x41  OP_SERVERINFO   the server's own tag list
//
// Only 0x40 means the login worked. Everything else is a server talking
// about itself, and none of it is a failure — so those are consumed and the
// facts recorded, rather than treated as a reason to give up.
//
// A refusal is a message whose text says so, and that IS a failure: see
// readMessage for the words, which come from real servers and are the reason
// an operator sees "This server is full" instead of a timeout.
// readLoginConfirmation reads the login response.
//
// # NOTHING HERE IS A CONFIRMATION, AND THAT IS THE POINT
//
// A server that accepts a login does not say "accepted". It starts talking:
// the whole first burst IS the confirmation. Measured on 85.17.116.222:
//
//  1. 0x40  the server's own totals, then its endpoint
//  2. 0x34  a larger, network-wide count
//  3. 0x38  a banner about port forwarding
//  4. 0x41  the server's GUID and a ten-tag list
//  5. 0x38  a second banner naming the software
//
// So this function reads that burst and hands off to drainFacts, which keeps
// reading until the server goes quiet. The only thing in the whole sequence
// that can REFUSE is an OP_SERVERMESSAGE whose text says so — and a refusal is
// handled here, because a server that declines says so in words rather than
// by falling silent.
//
// The first version of this file waited for an OP_HELLO, which is the Kad
// UDP packet. Against a real network that waited for a packet that never
// came, so every server timed out after having plainly answered. The second
// version treated 0x40 as a 16-byte GUID, which changed on every connection
// and contained the user count. Neither is a confirmation because neither is
// a confirmation: the sequence simply has none.
func (s *Server) readLoginConfirmation() error {
	// A bound on how many packets may be read before handing off. A server
	// that talks forever must not make this loop unbounded; the drain that
	// follows is separately bounded.
	const maxSkipped = 16

	// # "QUIET" AND "SILENT" ARE DIFFERENT, AND THE DIFFERENCE IS THIS FUNCTION
	//
	// A server that has sent its burst and stopped is a working server, and
	// going quiet after speaking is how a conversation ends. A server that
	// has said NOTHING is a server that never accepted us.
	//
	// Both end in the same read error — a deadline — so the deadline alone
	// cannot tell them apart. The first version of this function treated a
	// timeout as success, and then a server that accepted a connection and
	// stalled mid-packet produced a usable Server: the exact "starts and does
	// nothing" failure this whole layer exists to prevent.
	//
	// So what matters is not whether the read timed out but whether anything
	// arrived first. `heard` is set by every packet notePostLoginPacket
	// accepts, and it is the whole distinction.
	//
	// # THE FIELD IS SET DIRECTLY, NOT THROUGH A defer
	//
	// The first version used `defer func() { s.heardAnything = heard }()`.
	// A defer runs AFTER the return value is computed, so it overwrote
	// whatever the drain had already recorded with a snapshot taken at
	// function entry — and every connection reported "said nothing at all"
	// no matter how much the server had said. The symptom was a Dial that
	// failed on four passing fixtures, which is the confusing direction:
	// the guard was firing, just with a value from before the read.
	heard := false

	// # A SHORT DEADLINE, THEN THE DRAIN TAKES OVER
	//
	// Nothing in the opening burst is a confirmation, so this loop cannot
	// exit on content — it exits on the server going QUIET, and it must do
	// that quickly. With the 20-second dial deadline still in force, a
	// server that sent its burst and stopped made every test wait the full
	// 20 seconds and then fail, because the loop was waiting for a packet
	// that was never coming.
	//
	// So the burst read gets its own short deadline and hands over to
	// drainFacts, which has the same shape and the same reasoning. A
	// server that has said nothing at all by the end of it has not
	// accepted the login, and that is a failure worth reporting.
	if err := s.conn.SetReadDeadline(
		time.Now().Add(burstDrainTimeout)); err != nil {
		return fmt.Errorf("cannot bound the login-response read: %w", err)
	}

	for read := 0; read <= maxSkipped; read++ {
		header, payload, err := readFrame(s.conn)
		if err != nil {
			if isTimeout(err) {
				// The server has finished its opening burst. Carry on into
				// the drain rather than reporting an error — but only if it
				// actually said something. A server that went quiet having
				// spoken nothing is a server that never accepted us, and a
				// Server built on that is a Server that hangs on first use.
				if !heard {
					return fmt.Errorf("the server accepted the connection "+
						"and then said nothing at all: %w", err)
				}
				return s.drainFacts()
			}
			// A server that accepted a login and then broke mid-sentence
			// is not a server we can talk to. Say so.
			return fmt.Errorf("the server answered the login request and "+
				"then the connection broke: %w", err)
		}

		if err := s.notePostLoginPacket(header.Packet, payload); err != nil {
			return err
		}
		// Set on the struct directly: the field is what the drain reads,
		// and a local copy plus a deferred write loses the update.
		s.heardAnything = true
		heard = true

		// A message that is a refusal ends this: a server that declines
		// does not go on to send a tag list.
		if len(s.messages) > 0 &&
			messageIsRefusal(s.messages[len(s.messages)-1]) {
			return fmt.Errorf("%w: the server refused the connection: %s",
				ErrRefused, s.messages[len(s.messages)-1])
		}
	}

	// Reached only if a server sent maxSkipped packets and was still
	// talking. The facts collected so far are kept and the drain takes the
	// rest: this is a chatty server, not a broken one.
	return s.drainFacts()
}

// burstDrainTimeout bounds how long the first packet is waited for.
//
// # 400ms WAS A GUESS, AND A REAL SERVER TOOK 3 SECONDS
//
// The first version of this constant was 400 milliseconds, and every hermetic
// test passed with it because a local fake replies in microseconds. Against a
// real server it refused every connection in 0.4s with "the server accepted
// the connection and then said nothing at all" — on a server that was about
// to answer.
//
// Measured on 85.17.116.222 on 2026-09-28, three times, identically:
//
//	first byte after 3093ms, 3094ms, 3093ms
//
// So the floor is set from a measurement rather than from what a loopback
// server does. It is generous on purpose: a short deadline here produces a
// false "this server is dead" for a slow but working one, and a slow refusal
// costs a user far less than a wrong one.
//
// A local fake cannot catch this class of bug, which is the second reason the
// live tests exist alongside the hermetic ones rather than instead of them.
//
// A var for the reason on dialTimeout: the hermetic tests shrink these to
// milliseconds so a suite about a quiet server does not take a quarter of a
// minute per test. TestTheRealDeadlinesAreTheMeasuredOnes pins the real
// values, so a test cannot make production wrong by editing this.
var burstDrainTimeout = 8 * time.Second

// notePostLoginPacket records one packet from the server's opening burst, and
// reports a refusal.
//
// One function rather than a switch in each of the two loops that read these
// packets. The two loops are the confirmation read and the drain, they see
// the same opcodes, and a copy of the switch in each is a copy that will
// eventually disagree — which is exactly what happened when 0x40 was read as a
// GUID in one place and a count in another.
func (s *Server) notePostLoginPacket(opcode byte, payload []byte) error {
	switch opcode {
	case opServerStatus:
		// 0x40: the server's OWN totals, then its endpoint. Sent first.
		if len(payload) < 8 {
			return fmt.Errorf("a server status packet is 8 bytes and "+
				"%d arrived", len(payload))
		}
		s.totalUsers = int32(binary.LittleEndian.Uint32(payload[0:4]))
		s.totalFiles = int32(binary.LittleEndian.Uint32(payload[4:8]))

	case opIDChange:
		// 0x34: a larger, network-wide count. Sent third.
		//
		// # NEITHER COUNT PACKAGE CARRIES A SESSION TOKEN
		//
		// Both are two uint32s and both move on every connection, so
		// neither is an identifier. The first version of this file read
		// 0x40 as a 16-byte GUID and produced a "GUID" that was the user
		// count followed by the server's address, changing every time.
		//
		// There is no session token to expose, and inventing an accessor
		// for one is how a caller ends up using bytes whose meaning is
		// unestablished.
		if len(payload) < 8 {
			return fmt.Errorf("a server count packet is 8 bytes and "+
				"%d arrived", len(payload))
		}
		s.users = int32(binary.LittleEndian.Uint32(payload[0:4]))
		s.files = int32(binary.LittleEndian.Uint32(payload[4:8]))

	case opServerInfo:
		// The server's tag list, parsed with this package's own decoder —
		// which exists because the library's reads the wire format wrong.
		//
		// A failure here is not a login failure: the server has already
		// accepted us and is now saying something this client does not
		// model. Refusing the connection would throw away a working
		// connection over a banner, and dropping the error would hide a
		// real protocol difference — so it is kept and a caller can ask.
		if tags, err := parseTagList(payload); err == nil {
			s.tags = tags
		} else {
			s.tagErr = err
		}

	case opServerMsg:
		if msg, ok := decodeServerMessage(payload); ok {
			s.messages = append(s.messages, msg)
		}

	default:
		// An opcode this client does not model. Recorded and kept, not
		// treated as a failure: these packets are unsolicited, and a
		// server that sends something extra is still a server.
		s.messages = append(s.messages,
			fmt.Sprintf("opcode 0x%02X", opcode))
	}
	return nil
}

// factsDrainTimeout bounds how long Dial waits for the packets a server
// sends right after confirming a login.
//
// The alternative is to return immediately on OP_IDCHANGE, and that is what
// this file did: every connection then reported zero users, zero files and
// no tags, which is indistinguishable from a server that is genuinely empty
// and is the worst of both readings. The cost of the wait is added to every
// connection, so it is short — a server that has accepted a login has
// already sent these by the time its confirmation reaches us in practice.
//
// # MEASURED, NOT ASSUMED
//
// The same server measured above, after its first packet, goes quiet for
// 11.4 seconds before sending the next four. So this deadline is larger than
// it looks for the same reason burstDrainTimeout is: the alternative is
// reporting a working server as broken, and the cost of being wrong in that
// direction is a user watching a spinner instead of a download.
//
// The first version was 400ms and the hermetic tests passed, because a local
// fake sends everything at once. A constant that only works against a
// loopback is not a constant that works.
//
// A var for the reason on dialTimeout.
var factsDrainTimeout = 15 * time.Second

// drainFacts reads the packets a server sends after confirming a login, until
// it goes quiet or the drain deadline passes.
//
// The reads are best-effort by design: a timeout here is the NORMAL outcome
// for a server that sent only a confirmation, and it is not an error. So the
// timeout is swallowed and the facts collected so far are kept. The only
// failure that matters is one that leaves the connection unusable, and a
// server that has just closed is that.
func (s *Server) drainFacts() error {
	if err := s.conn.SetReadDeadline(
		time.Now().Add(factsDrainTimeout)); err != nil {
		// A connection whose deadline cannot be set is a connection whose
		// reads will block forever, so this is worth failing on.
		return fmt.Errorf("cannot bound the post-login read: %w", err)
	}
	defer func() {
		// Clear the drain deadline. Leaving it in place would fail the
		// first real transfer read 400ms in, which reads as a flaky
		// network rather than as a forgotten ClearDeadline.
		_ = s.conn.SetReadDeadline(time.Time{})
	}()

	for read := 0; read < maxDrainedPackets; read++ {
		header, payload, err := readFrame(s.conn)
		if err != nil {
			// Silence is the EXPECTED end of a drain: a server has said
			// everything it is going to say until we ask it something.
			// Returning nil here is what makes Dial succeed against a
			// server that answered and then had nothing more to add.
			//
			// mustRead used to discard the read error, so a deadline
			// looked identical to a closed connection and every quiet
			// server reported "EOF" and failed the dial. That was a
			// real bug, and it is why the timeout is distinguished here
			// rather than treated as any other error.
			if isTimeout(err) {
				// Quiet after speaking is a finished conversation. Quiet
				// with nothing heard at all is a server that never
				// answered, and a Server that pretends otherwise is the
				// failure this whole file is about.
				if !s.heardAnything {
					return fmt.Errorf("the server accepted the connection "+
						"and then said nothing at all: %w", err)
				}
				return nil
			}
			// A refusal still matters even now: a server that talks and
			// then refuses is refusing.
			if last := s.lastMessage(); last != "" &&
				messageIsRefusal(last) {
				return fmt.Errorf("%w: the server refused the connection: %s",
					ErrRefused, last)
			}
			// Otherwise the connection has ended or broken, and a login
			// that was accepted is no longer usable. Say so rather than
			// handing back a Server whose next read will fail.
			return fmt.Errorf("reading the server's status after it "+
				"answered the login: %w", err)
		}

		if err := s.notePostLoginPacket(header.Packet, payload); err != nil {
			return err
		}
		s.heardAnything = true
	}
	return nil
}

// maxDrainedPackets bounds the post-login drain. A server that sends packets
// forever is a server that will never go quiet, and a drain with no bound is
// a read that never returns.
const maxDrainedPackets = 32

// isTimeout reports whether a read error is the deadline expiring rather than
// a real fault. The distinction matters because a timeout here is the expected
// end of a drain and a real fault is not.
func isTimeout(err error) bool {
	return errors.Is(err, os.ErrDeadlineExceeded) ||
		errors.Is(err, context.DeadlineExceeded)
}

// lastMessage is the most recent thing the server said, or the empty string if
// it has not said anything.
//
// A function rather than an index at the call site, because the empty case is
// the one that panics and it is exactly the one a reader assumes away.
func (s *Server) lastMessage() string {
	if len(s.messages) == 0 {
		return ""
	}
	return s.messages[len(s.messages)-1]
}

// decodeServerMessage reads an OP_SERVERMESSAGE payload: a uint16 length and
// that many bytes of text.
//
// A length that overruns the payload is not fatal — the message is dropped
// rather than the connection refused, because the point of the decode is the
// text in a banner and a malformed banner is not a reason to give up on a
// working server.
func decodeServerMessage(payload []byte) (string, bool) {
	if len(payload) < 2 {
		return "", false
	}
	length := int(uint16(payload[0]) | uint16(payload[1])<<8)
	if length > len(payload)-2 {
		return "", false
	}
	return string(payload[2 : 2+length]), true
}

// refusalWords are the phrases that mean "stop asking", as opposed to a banner
// the server sends before its hello.
//
// This is a substring match on English text from a third party, which is not
// a protocol mechanism and does not pretend to be one. It is here because the
// alternative is worse: a full server that we keep reading from until a
// timeout reports "i/o timeout" for a condition the server told us about in
// plain text.
var refusalWords = []string{
	"full", "refus", "banned", "banned:", "shutting down", "closed",
	"too many", "max connections", "not available", "offline",
}

// messageIsRefusal reports whether one server message means the server is not
// going to keep this connection.
func messageIsRefusal(msg string) bool {
	lower := strings.ToLower(msg)
	for _, w := range refusalWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// sendFirstPacket writes the connection's FIRST packet: the login request,
// obfuscated.
//
// This is the only packet that is obfuscated. `sentFirst` is what enforces
// that, because a client that obfuscates a later packet is dropped mid-session
// after a login that otherwise worked — and because a second implementation
// of "am I the first packet yet" in each call site is how that happens.
func (s *Server) sendFirstPacket() error {
	req := loginRequest{
		Port: 0,
		Tags: protocol.TagList{},
	}
	body, err := req.body()
	if err != nil {
		return fmt.Errorf("encoding the login request: %w", err)
	}

	frame, err := frameBytes(protocol.EdonkeyHeader, opLoginRequest, body)
	if err != nil {
		return err
	}

	seed, err := newObfuscationSeed()
	if err != nil {
		return err
	}
	obfuscated, err := obfuscate(frame, seed)
	if err != nil {
		return err
	}

	s.sentFirst = true
	if _, err := s.conn.Write(obfuscated); err != nil {
		return fmt.Errorf("writing the first packet: %w", err)
	}
	return nil
}

// sendFrame writes one plain packet. Every packet after the first is sent
// through here, and they are NOT obfuscated.
func (s *Server) sendFrame(protocolByte, opcode byte, payload []byte) error {
	if !s.sentFirst {
		// A plain packet before the obfuscated one is a client that every
		// server drops, and the failure is silence. So this is a bug in the
		// caller, not a condition to tolerate.
		return fmt.Errorf("refusing to send a plain packet before the " +
			"connection's first packet: a server that sees an " +
			"unobfuscated first packet drops the connection without " +
			"replying, so the mistake is invisible on the wire")
	}
	return writeFrame(s.conn, protocolByte, opcode, payload)
}

// opLoginRequest is the opcode a CLIENT sends first. 0xE3 is EDONKEYPROT and
// 0xE3 is also the login opcode in the edonkey protocol; the protocol byte and
// the opcode share the value, which is a coincidence of the design and not a
// typo.
const (
	opLoginRequest byte = 0xE3

	// # THE TWO CONFIRMATION-RELEVANT OPCODES, AND WHICH IS WHICH
	//
	// # THE ed2k LOGIN RESPONSE HAS NO HELLO
	//
	// OP_HELLO (0x01) belongs to the KAD UDP hello, not to the ed2k TCP
	// login. The first version of this file read a hello after the login
	// request, so against a real server it read the status and messages
	// packets, did not recognise them, skipped them, and kept waiting for a
	// packet that was never coming until the deadline fired. The live tests
	// showed this as every server failing with a timeout AFTER HAVING PLAINLY
	// ANSWERED, which is the most confusing failure available.
	//
	// # 0x34 CONFIRMS THE LOGIN, AND 0x40 IS THE STATUS
	//
	// The two were swapped in the first version of this file, and a live
	// handshake is what settled it. Captured in order from 85.17.116.222:
	//
	//	1. op=0x40  20B  users=102254 files=17885  + the server's endpoint
	//	2. op=0x34   8B  a 4-byte value, then 4 zero bytes
	//	3. op=0x38  89B  "VPN with port forwarding (for High ID) ..."
	//	4. op=0x41 110B  GUID deadbeefcafebabe..., then a ten-tag list
	//	5. op=0x38  70B  "Open-source ed2k-server ..."
	//
	// 0x40 carries counts that CHANGE between connections — 98261, then
	// 99488, then 102254 — so it cannot be an identifier. 0x34 arrives third,
	// after the server has already spoken, and its first field is a
	// per-connection value. Reading 0x40 as an ID assigned a "GUID" that
	// changed on every connection and was in fact the user count.
	//
	// So: 0x34 is the login confirmation, and 0x40 is the status.
	opIDChange     byte = 0x34
	opServerStatus byte = 0x40
	opServerInfo   byte = 0x41
	opServerMsg    byte = 0x38
	opGetServerLst byte = 0x1C
)

// loginRequest is the first packet a client sends: a 16-byte user hash, the
// TCP port it wants callbacks on, and a tag list.
//
// The user hash is sixteen zero bytes. That is not a placeholder that slipped
// through — it is what a client without a GUID sends, and this build does not
// persist one yet. A random hash would look more deliberate and would make
// this client appear as a different identity on every connection.
type loginRequest struct {
	Hash [16]byte
	Port uint32
	Tags protocol.TagList
}

func (l loginRequest) body() ([]byte, error) {
	var out bytes.Buffer
	out.Write(l.Hash[:])
	// The port is a uint32, NOT a uint16: the field is four bytes wide and the
	// high half is zero on a real port. Writing a uint16 here is a
	// two-byte-short packet, which the server reads as a tag count of
	// whatever follows — a failure with no error at either end.
	if err := protocol.WriteUInt32(&out, l.Port); err != nil {
		return nil, err
	}
	if err := l.Tags.Put(&out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// The server-hello tag IDs carrying the user and file counts. A server sends
// them as optional tags, so both are legitimately absent.
const (
	tagUserCount byte = 0x0C
	tagFileCount byte = 0x0F
)

// countsFromTags pulls the user and file counts out of a server's hello tags.
//
// A MISSING tag is zero and not an error, because a tag being optional is the
// protocol's design and a server that omits one is not misbehaving. A tag
// present with the wrong TYPE is also skipped rather than refused: the tag
// list is a list of strings, GUIDs and numbers from a stranger, and one
// unexpected entry is not a reason to drop a connection we would otherwise
// use. The refusal that matters — a header that is not an ED2K packet, a size
// beyond the limit — happens before this is reached.
func countsFromTags(tags protocol.TagList) (users, files int32) {
	for i := range tags {
		// A count is a 32-bit number. A tag of another type is not one, and
		// reading its string as a count would be a stranger's bytes
		// reinterpreted as a quantity.
		if tags[i].Type != protocol.TagTypeUint32 {
			continue
		}
		switch tags[i].ID {
		case tagUserCount:
			// The uint32 -> int32 conversion is where a server's claim could
			// turn negative, and that is the caller's problem to notice: the
			// value is preserved as sent rather than clamped, so a report of
			// 4 billion users stays visibly absurd instead of becoming 0.
			users = int32(tags[i].UInt32)
		case tagFileCount:
			files = int32(tags[i].UInt32)
		}
	}
	return users, files
}

// checkProtocolByte refuses a protocol byte that is not an ED2K one.
//
// The accepted set is E3 (classic edonkey) and C5 (eMule), plus the packed
// variant D4 which some servers use for compressed headers. Kademlia's E4 is
// deliberately NOT here: E4 is UDP, and a TCP stream that opens with E4 is not
// a server talking to us.
func checkProtocolByte(got byte) error {
	switch got {
	case protocol.EdonkeyHeader, protocol.EMuleProt, protocol.PackedProt:
		return nil
	}
	return fmt.Errorf("%w: the first byte was 0x%02X, which is not an ED2K "+
		"protocol byte (0xE3 edonkey, 0xC5 eMule, 0xD4 packed). A TCP "+
		"connection that answers with something else is usually a "+
		"captive portal, a proxy, or the wrong port — not a server that is "+
		"merely busy", ErrNotAServer, got)
}

// readFrame reads one header and exactly the payload it promises.
//
// # THE PROTOCOL BYTE IS CHECKED BEFORE THE SIZE, AND THAT ORDER IS THE POINT
//
// Both fields come from a stranger. The size is a 32-bit number that can claim
// 2 GiB, so it wants bounding; the protocol byte is one byte that says what
// this stream is. Checking size first looks like the safer order and is the
// wrong one: an HTTP reply's first six bytes parse as a header with a
// nonsense size, so the size check fires and the operator is told the peer sent
// an oversized packet, when what actually happened is that the peer is not an
// ed2k server at all.
//
// So the one-byte check goes first. It is definitive — no ED2K server sends 0x47
// — and it answers the question the size check would otherwise answer badly.
func readFrame(r io.Reader) (protocol.PacketHeader, []byte, error) {
	// The header is read first and its error kept, because the protocol byte
	// and the size are both IN it — decoding a short header would read
	// uninitialised memory as a stranger's claim.
	raw, err := mustRead(r, protocol.PacketHeaderSize)
	if err != nil {
		var header protocol.PacketHeader
		// A timeout is reported as itself so the caller can tell a quiet
		// server from a broken one. mustRead used to discard this, which
		// turned every deadline into an EOF.
		if isTimeout(err) {
			return header, nil, err
		}
		return header, nil, fmt.Errorf("reading the packet header: %w", err)
	}

	var header protocol.PacketHeader
	if err := header.Get(bytes.NewReader(raw)); err != nil {
		return header, nil, fmt.Errorf("decoding the packet header: %w", err)
	}

	if err := checkProtocolByte(header.Protocol); err != nil {
		return header, nil, err
	}

	// The size arrives from a stranger, so it is bounded BEFORE the read that
	// would honour it. A size of 2 GiB otherwise becomes our memory use.
	if size := header.SizePacket(); size < 0 || size > maxPacketSize {
		return header, nil, fmt.Errorf("the packet header claims %d payload "+
			"bytes, which is over the %d byte limit this client will read. A "+
			"header that large is not something an ed2k server sends",
			size, maxPacketSize)
	}

	payload := make([]byte, header.SizePacket())
	if _, err := io.ReadFull(r, payload); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return header, nil, fmt.Errorf("%w: the header promised %d "+
				"bytes and the connection ended first", ErrTruncatedPacket,
				len(payload))
		}
		return header, nil, fmt.Errorf("reading the packet payload: %w", err)
	}

	return header, payload, nil
}

// mustRead reads exactly n bytes and returns however many arrived.
//
// # THE ERROR IS DROPPED, AND THAT IS A BUG THIS FUNCTION HAD
//
// `io.ReadFull` returns the error that stopped it, and the first version of
// this function threw that error away with `_`. A read that stopped because
// the DEADLINE EXPIRED is indistinguishable, afterwards, from a peer that
// closed: both leave a short buffer.
//
// That mattered because a read deadline is exactly how the post-login drain
// knows a server has gone quiet. With the error dropped, every quiet server
// reported "reading the packet header: EOF" and Dial failed — so a server
// that had accepted the login and simply had nothing more to say was reported
// as a broken connection, on every connection, in the normal case.
//
// The error is returned alongside the bytes so the caller can tell a timeout
// from an end-of-stream, which is the distinction the drain turns on.
func mustRead(r io.Reader, n int) ([]byte, error) {
	buf := make([]byte, n)
	read, err := io.ReadFull(r, buf)
	return buf[:read], err
}

// frameBytes builds a framed packet as bytes.
//
// The size is len(body)+1 because the header's Size field COUNTS THE OPCODE
// BYTE. `SizePacket()` — which is what a reader subtracts — is `Size - 1`, and
// that subtraction is the opcode. A header written as `Size = len(body)` is
// off by one and looks entirely plausible: our own decoder reads back exactly
// the body we sent, because it subtracts the same one. On a real server the
// next read starts one byte early and every packet after it decodes as
// garbage — an error that surfaces far from here, pointing at whatever packet
// happened to be read wrong.
//
// So the arithmetic lives in ONE function, and the golden tests assert the
// relationship between the header's claim and the bytes on the wire rather
// than trusting it.
func frameBytes(protocolByte, opcode byte, body []byte) ([]byte, error) {
	var out bytes.Buffer
	header := protocol.PacketHeader{
		Protocol: protocolByte,
		Size:     int32(len(body)) + 1, // +1 for the opcode byte
		Packet:   opcode,
	}
	if err := header.Put(&out); err != nil {
		return nil, fmt.Errorf("encoding the packet header: %w", err)
	}
	out.Write(body)
	return out.Bytes(), nil
}

// writeFrame writes one plain packet.
func writeFrame(w io.Writer, protocolByte, opcode byte, payload []byte) error {
	out, err := frameBytes(protocolByte, opcode, payload)
	if err != nil {
		return err
	}
	if _, err := w.Write(out); err != nil {
		return fmt.Errorf("writing %d bytes to the server: %w", len(out), err)
	}
	return nil
}
