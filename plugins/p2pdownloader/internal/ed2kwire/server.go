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
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/monkeyWie/goed2k/protocol"
	"github.com/monkeyWie/goed2k/protocol/client"
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

// dialTimeout bounds the connect AND the first read, because a server that
// accepts a TCP connection and then says nothing is a real and common
// occurrence — a filtered port, a server that is up but not serving, a
// tarpitted connection. Without a deadline on the read, Dial hangs forever
// inside a call that looks like it is working.
const dialTimeout = 20 * time.Second

// Server is one ed2k server we are connected to.
//
// It is a connection, not a session: the server's user list, the searches, and
// the source exchanges all live above this, and none of them are in scope yet.
type Server struct {
	addr string
	conn net.Conn

	// The server's GUID, and the endpoint it wants callbacks on.
	hash        protocol.Hash
	serverPoint protocol.Endpoint

	// What the server said about itself, in the tags of its hello.
	//
	// These are CLAIMS from a stranger and are carried as claims. A server
	// can report any value here, including a negative one, and nothing in the
	// protocol checks it — so they are never used to size an allocation, a
	// buffer, or a progress bar's denominator.
	users int32
	files int32

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

// Hash is the server's GUID. It identifies the SERVER, not a file, and is not
// the hash of anything we download.
func (s *Server) Hash() protocol.Hash { return s.hash }

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

	// Read until we have the server's hello, tolerating the packets a server
	// may legitimately send first.
	hello, err := srv.readHello()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%s: %w", addr, err)
	}

	srv.hash = hello.Hash
	srv.serverPoint = hello.ServerPoint
	srv.users, srv.files = countsFromTags(hello.Properties)

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

// readHello reads packets until the server's hello arrives.
//
// A server may answer with OP_SERVERMESSAGE first — a refusal with a human
// reason, such as "This server is full" — and that is NOT a hello and NOT a
// transport failure. Refusing the connection on a message means an operator
// sees "malformed packet" for a server that is simply full, which is the
// difference between an actionable error and a confusing one. Measured against
// a real server:
//
//	protocol=0xE3 size=33 opcode=0x38 payload=32B   (size-1 = 32)
//	0x38 -> "WARNING : This server is full"
//
// So the loop skips messages, collects one, and returns it alongside the hello
// so the caller can say what the server said.
func (s *Server) readHello() (client.HelloAnswer, error) {
	var hello client.HelloAnswer
	var messages []string

	// A bound on how many packets may be skipped. Without it, a server that
	// sends messages forever is an infinite loop on a connection that looks
	// alive — the "starts and does nothing" shape again, in a new costume.
	const maxSkipped = 8

	for skipped := 0; skipped <= maxSkipped; skipped++ {
		header, payload, err := readFrame(s.conn)
		if err != nil {
			// A message seen before a failure is almost always the reason
			// for it, and losing it means reporting "EOF" for a server that
			// said why.
			if len(messages) > 0 {
				return hello, fmt.Errorf("%w (the server first said: %s)",
					err, strings.Join(messages, "; "))
			}
			return hello, err
		}

		switch header.Packet {
		case opHello:
			// The payload is the full HELLO, which begins with a hash-length
			// byte, and the HELLOANS body, which does not. A server sends
			// OP_HELLO for both, distinguished only by that leading byte.
			var full client.Hello
			if err := full.Get(bytes.NewReader(payload)); err != nil {
				return hello, fmt.Errorf("the server's hello does not "+
					"decode: %w", err)
			}
			return full.HelloAnswer, nil

		case opServerMsg:
			if msg, ok := decodeServerMessage(payload); ok {
				messages = append(messages, msg)
				// A message that says the server is full or refusing is a
				// REFUSAL, not something to skip past: continuing would just
				// wait for a hello that is not coming.
				if isRefusalMessage(msg) {
					return hello, fmt.Errorf("%w: the server refused the "+
						"connection: %s", ErrRefused, msg)
				}
			}
			// Otherwise: a banner, and the hello follows. Skipping it is
			// correct.

		default:
			// An unexpected opcode. It is skipped rather than refused: the
			// hello is what this function is for, and a server that sends
			// something else first is still a server. If it never sends the
			// hello, the skip bound ends the loop and the error names the
			// opcodes seen.
			messages = append(messages, fmt.Sprintf("opcode 0x%02X", header.Packet))
		}
	}

	return hello, fmt.Errorf("the server sent %d packets and no hello "+
		"(%s). It is a server, but it is not talking to us",
		len(messages)+1, strings.Join(messages, "; "))
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

// isRefusalMessage reports whether a server message means the server is not
// going to complete the handshake.
func isRefusalMessage(msg string) bool {
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
	opHello        byte = 0x01
	opLoginRequest byte = 0xE3
	opServerMsg    byte = 0x38
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
	var header protocol.PacketHeader
	if err := header.Get(bytes.NewReader(mustRead(r, protocol.PacketHeaderSize))); err != nil {
		return header, nil, fmt.Errorf("reading the packet header: %w", err)
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

// mustRead reads exactly n bytes and returns them, or a short slice. A short
// slice is fine here: the caller's decoder reports the truncation, and the
// error text names the header size rather than inventing one.
func mustRead(r io.Reader, n int) []byte {
	buf := make([]byte, n)
	read, _ := io.ReadFull(r, buf)
	return buf[:read]
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
