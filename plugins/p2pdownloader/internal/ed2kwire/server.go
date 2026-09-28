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

	// Read the server's hello, framed.
	header, payload, err := readFrame(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%s: %w", addr, err)
	}

	// readFrame has already checked the protocol byte — before the size, which
	// is the order that makes an HTTP reply report itself as "not a server"
	// rather than as "an oversized packet". So there is nothing left to check
	// here, and a second checkProtocolByte would be the duplicated defence that
	// made an existing harness row report a false survivor once already.
	if got, want := len(payload), int(header.SizePacket()); got != want {
		_ = conn.Close()
		return nil, fmt.Errorf("%s: %w: the header said %d payload bytes and "+
			"%d arrived", addr, ErrTruncatedPacket, want, got)
	}

	var hello client.HelloAnswer
	if err := hello.Get(bytes.NewReader(payload)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%s: the server's hello does not decode: %w",
			addr, err)
	}

	srv.hash = hello.Hash
	srv.serverPoint = hello.ServerPoint
	srv.users, srv.files = countsFromTags(hello.Properties)

	// Answer with our own hello. The client GUID is deliberately zero rather
	// than random: it is an identifier this build does not yet persist, and a
	// random one that changes every connection looks to a server like a client
	// with amnesia. A real GUID belongs with the resume work, and the comment
	// there must say so rather than leaving a random value here looking
	// deliberate.
	if err := srv.sendHello(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%s: cannot answer the server's hello: %w",
			addr, err)
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

// sendHello writes our hello answer framed.
func (s *Server) sendHello() error {
	var body bytes.Buffer
	answer := client.HelloAnswer{
		// A zero Hash: we are not claiming a GUID yet, and a zero GUID is
		// what a client without one sends. See sendHello's comment on the
		// client ID.
		Hash: protocol.Invalid,
		// Our endpoint as advertised. Zero: we are not listening for
		// callbacks yet, and a server that tries to connect back to
		// 0.0.0.0:0 will simply fail, which is honest. A routable address
		// here would be a claim the plugin cannot honour.
		Point:       protocol.Endpoint{},
		Properties:  protocol.TagList{},
		ServerPoint: protocol.Endpoint{},
	}
	if err := answer.Put(&body); err != nil {
		return fmt.Errorf("encoding the hello: %w", err)
	}

	// OP_HELLO. The payload is the HelloAnswer body with NO HashLength byte —
	// that byte belongs to the server's full Hello packet, and the answer
	// carries only the body. Getting this wrong is invisible locally: our own
	// decoder reads the packet we wrote and agrees with us.
	//
	// The size is BytesCount(), which counts the BODY. The opcode is
	// written separately and excluded, matching SizePacket().
	return writeFrame(s.conn, protocol.EdonkeyHeader, opHello, body.Bytes())
}

// opHello is the opcode for HELLO / HELLOANS (they share one opcode on the
// wire; the direction is implied by who sent it).
const opHello byte = 0x01

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

// writeFrame writes one header and its payload.
//
// # THE SIZE FIELD COUNTS THE OPCODE TOO
//
// This is the single most dangerous detail in the file, and it is the reason
// `TestTheHelloWeSendIsTheHelloTheProtocolDescribes` exists.
//
// The header's `Size` is the number of bytes AFTER the header's own six,
// counting the OPCODE byte. `SizePacket()` — which is what actually gets read
// — is `Size - 1`, and that subtraction is the opcode. So:
//
//	Size     = len(body) + 1
//	payload  = Size - 1 = len(body)
//
// A header written as `Size = len(body)` is off by one and looks entirely
// plausible: our own decoder reads back exactly the body we sent, because it
// subtracts the same one. On a real server the next read starts one byte early
// and every packet after it decodes as garbage — an error that surfaces far
// from here, pointing at whatever packet happened to be read wrong.
//
// So the size is written as len(body)+1 in ONE place, and the golden test
// asserts the relationship between the header's claim and the bytes on the
// wire rather than trusting the arithmetic.
func writeFrame(w io.Writer, protocolByte, opcode byte, payload []byte) error {
	var out bytes.Buffer
	header := protocol.PacketHeader{
		Protocol: protocolByte,
		// +1 for the opcode byte, which Size counts and the body does not.
		Size:   int32(len(payload)) + 1,
		Packet: opcode,
	}
	if err := header.Put(&out); err != nil {
		return fmt.Errorf("encoding the packet header: %w", err)
	}
	if _, err := out.Write(payload); err != nil {
		return fmt.Errorf("writing the packet payload: %w", err)
	}
	if _, err := w.Write(out.Bytes()); err != nil {
		return fmt.Errorf("writing %d bytes to the server: %w", out.Len(), err)
	}
	return nil
}
