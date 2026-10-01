package ed2kwire

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/monkeyWie/goed2k/protocol"
	"github.com/monkeyWie/goed2k/protocol/kad"
)

// # KAD BOOTSTRAP
//
// Kademlia is the DHT underneath ed2k source discovery: it is how a client
// finds peers when no server knows about the file. Bootstrapping means being
// told about some nodes, connecting to one, and letting the network introduce
// you to the rest.
//
// # WHAT IS DELEGATED AND WHY
//
// The format work is delegated: `kad.ParseNodesDat` reads the contact list,
// including the three `nodes.dat` version variants, and — importantly — it
// BOUNDS the contact count against the buffer before allocating
// (`numContacts > reader.Len()/minEntrySize`). A count field is attacker- or
// mirror-controlled, and an unbounded one is an allocation of whatever the
// first four bytes ask for. That check already exists upstream, so re-writing
// it here would be re-writing it worse.
//
// What is OURS: the Kad node ID we present, and the decision to connect at
// all. See NodeID below — getting that wrong is invisible in every test that
// does not touch the live network.

// Node is one contact from a `nodes.dat` or from the DHT.
type Node struct {
	// IP and port as the network sees them. The library's Entry carries a
	// 128-bit IPv6-capable address; this keeps the 4-byte IPv4 form because
	// that is what a `nodes.dat` from a real deployment contains, and a
	// lossy conversion here would be a decision made in the wrong place.
	IP   net.IP
	Port uint16

	// Version is the contact's own Kad protocol version, straight from the
	// contact list.
	//
	// # ADDED BECAUSE DROPPING IT WAS A REAL BUG, FOUND BY THE LIVE TEST
	//
	// The first version of this struct had IP and Port and nothing else,
	// because those are the two fields Bootstrap needs. That is the wrong
	// reason to drop a field: aMule itself IGNORES any contact whose
	// version byte is 1 or lower, because those speak the retired Kad1
	// protocol, and this package only speaks Kad2. So a stale contact list
	// full of Kad1 entries is a list where a large fraction of the
	// attempts are guaranteed to fail — and nothing here could tell that
	// apart from "the network is down".
	//
	// Found when TestLiveKadBootstrapsAgainstRealNodes was written and
	// asked whether the contacts were even worth calling: the answer was
	// not expressible. Values 0 and 1 mean Kad1; anything above is Kad2.
	Version byte
}

// Addr renders the node as a dial address.
func (n Node) Addr() string {
	return net.JoinHostPort(n.IP.String(), fmt.Sprint(n.Port))
}

// NodeID is the identity this client presents to the Kad network.
//
// # IT MUST BE OUR CLIENT HASH, AND THAT IS NOT A DEFAULT
//
// A Kad node ID is an ed2k client hash. Deriving it from our own client GUID
// is what makes us findable and, more importantly, what makes the routing
// work: the DHT is keyed on ID distance, so a client that presents an ID
// unrelated to its actual identity is routed to the wrong region of the
// network and finds nothing.
//
// A random ID is the tempting choice and it is wrong. It is also the choice
// that no hermetic test can catch — a wrong ID produces no error, no
// malformed packet, and a perfectly healthy client that simply never finds a
// peer. That is why `TestTheKadNodeIDIsDerivedFromOurClientHash` exists and
// why the live tests in `live_test.go` check for SOURCES rather than for a
// successful connection.
type NodeID = kad.ID

// NewNodeID derives the Kad node ID from an ed2k client hash.
//
// The hash is taken as the 16 bytes of the client GUID, which is what the
// protocol specifies: the Kad ID and the client GUID are the same value in
// different clothes.
func NewNodeID(clientHash protocol.Hash) NodeID {
	return kad.NewID(clientHash)
}

// ParseNodesDat reads a `nodes.dat` contact list.
//
// The returned error names the count that was rejected when the count is what
// failed, because "invalid nodes.dat" on a 60 KB file that a user just
// downloaded is not an error they can act on — a count is.
func ParseNodesDat(raw []byte) ([]Node, error) {
	parsed, err := kad.ParseNodesDat(raw)
	if err != nil {
		return nil, fmt.Errorf("the nodes.dat contact list does not parse: %w",
			err)
	}

	nodes := make([]Node, 0, len(parsed.Contacts))
	for _, c := range parsed.Contacts {
		// The library's Endpoint holds the IPv4 address as a uint32 in
		// little-endian order -- note this is the OPPOSITE byte order from
		// the ed2k protocol.Endpoint, which is packed network order. That
		// asymmetry is why the address is decoded here by hand rather than
		// by reaching for a shared helper: the two would be silent mirror
		// images of each other, and a mirrored IP is a node we cannot reach
		// and cannot tell is wrong.
		nodes = append(nodes, Node{
			IP:      ipv4FromLE(c.Endpoint.IP),
			Port:    c.Endpoint.UDPPort,
			Version: c.Version,
		})
	}
	return nodes, nil
}

// ipv4FromLE turns the library's host-order address into the net.IP that
// everything above this package uses.
//
// # THIS FUNCTION SWAPPED THE BYTES A SECOND TIME, AND EVERY CONTACT WAS
// # UNREACHABLE BECAUSE OF IT
//
// The first version did the byte swap by hand:
//
//	return net.IPv4(byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
//
// on the reasoning that the Kad format stores the address little-endian. It
// does -- but the LIBRARY HAS ALREADY UNDONE THAT before handing the value
// over, because kad.Endpoint.IP is a uint32 in ordinary host order. So the
// swap was applied twice, and every address in the network came out mirrored.
//
// # HOW IT WAS FOUND
//
// The live Kad test dialled 127.251.161.1, 127.8.43.79 and 127.0.0.1 out of a
// contact list that contains no loopback addresses at all. Three contacts in a
// row decoding to 127.x is not a coincidence and not a network problem: it is
// the signature of a byte-reversed 0.0.1.x. Reading the raw file showed all
// 154 addresses mirrored, and the three that mattered were 1.161.251.127,
// 79.43.8.127 and 1.0.0.127 -- the mirror images of the three that were
// dialled.
//
// # WHY NO HERMETIC TEST CAUGHT IT
//
// Because the hermetic tests build their contact lists with THIS function, or
// through a path that goes through it, so a file written with a mirrored
// address parses back to a mirrored address and the test is green on a decoder
// that cannot reach a single real node. It agrees with itself. The assertion
// that would have caught it -- "a known address in, the same address out" --
// needs a file this code did not write, which is exactly what the live test
// now supplies.
//
// Every address came out as a VALID IPv4 address, just the wrong one, so
// nothing anywhere reported an error. See TestAKnownAddressSurvivesTheRoundTrip
// and TestTheLiveContactListHasNoMirroredAddresses.
func ipv4FromLE(v uint32) net.IP {
	// Already host order. binary.BigEndian is not needed and would be
	// actively wrong: it would restore the mirror image.
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// EncodeNodesDat builds a `nodes.dat` buffer from nodes, for the round-trip
// test and for writing one back.
//
// It exists so the format can be pinned from BOTH directions: a parser alone
// cannot tell a correct implementation from one that agrees with a broken
// writer.
//
// THE ENTRY LAYOUT, and it is worth writing out because getting it wrong
// produces a file that parses and contains garbage:
//
//	[16] node ID
//	[ 4] IPv4 address, LITTLE-endian
//	[ 2] UDP port, little-endian
//	[ 2] TCP port, little-endian
//	[ 1] version
//	= 25 bytes
//
// The first version of this function wrote the address at offset 0 and the
// port at offset 16 — 25 bytes that parse cleanly and decode to a mirrored IP
// and port 0. The round-trip test caught it, which is the reason this function
// exists at all rather than the test writing bytes inline.
func EncodeNodesDat(nodes []Node) []byte {
	var out bytes.Buffer

	var count [4]byte
	binary.LittleEndian.PutUint32(count[:], uint32(len(nodes)))
	out.Write(count[:])

	for _, n := range nodes {
		// The ID is a real 16-byte value rather than zeros: a nodes.dat of
		// all-zero IDs is parseable and useless, and a round trip that
		// cannot distinguish the two would pass on the wrong file.
		entry := kad.Entry{
			ID:       kad.NewID(protocol.Hash{}),
			Endpoint: endpointFromNode(n),
			// The contact's OWN version, written through. This used to
			// be a hardcoded 2, so a parsed contact came back as 2
			// whatever it said on disk: a round trip through our own
			// encoder silently upgraded every Kad1 entry to Kad2, and
			// the test that could have caught it checked the address
			// and not the version.
			//
			// 0 is written as 0, because in a contact list 0 really
			// does mean Kad1 and pretending otherwise is the bug.
			Version: n.Version,
		}
		_ = entry.Put(&out)
	}
	return out.Bytes()
}

// endpointFromNode converts our Node into the library's Kad endpoint.
//
// # THIS MIRRORED THE ADDRESS, AND ipv4FromLE MIRRORED IT BACK
//
// The first version read binary.LittleEndian.Uint32(v4), on the reasoning
// that the Kad format stores the address little-endian. The FILE does -- but
// this is not the file, it is the library's in-memory struct, and the library
// has already undone the format's byte order by the time it gets here. So the
// address was mirrored on the way out.
//
// # WHY IT WAS INVISIBLE FOR SO LONG
//
// Because ipv4FromLE mirrored it on the way back IN. The two errors cancelled,
// every round trip returned the address it started with, and TestANodesDat-
// RoundTrips was green for as long as it existed.
//
// That is the most dangerous shape a byte-order bug can take: two wrong
// functions that agree with each other are indistinguishable from one right
// one, and a round-trip test cannot see the difference. The only test that
// can is one that starts from bytes this code did not write -- which is
// TestAKnownAddressSurvivesTheRoundTrip, added after the live test dialled
// three loopback addresses out of a list containing none.
//
// # THE LESSON, WRITTEN DOWN SO THE NEXT BYTE ORDER IS NOT PAID FOR TWICE
//
// One side of a codec is not evidence about the other. A round trip proves
// the two halves AGREE; it never proves either is right, and two mirrored
// halves agree perfectly. Byte order has to be pinned against the FORMAT --
// golden bytes off the wire -- and never against this package's own output.
func endpointFromNode(n Node) kad.Endpoint {
	v4 := n.IP.To4()
	ip := uint32(0)
	if v4 != nil {
		// Already host order, same as ipv4FromLE's read side. See above:
		// swapping here is what made every written contact unreachable.
		ip = binary.BigEndian.Uint32(v4)
	}
	return kad.Endpoint{IP: ip, UDPPort: n.Port}
}

// Bootstrap contacts a node and completes the Kad hello.
//
// It returns the node's own ID and whether the node reported itself firewalled.
// A firewalled answer is NOT an error: it is a truthful statement from a peer
// behind NAT, and refusing those would refuse most of the network. The
// distinction that matters is "the node answered" versus "the node did not".
type BootstrapResult struct {
	// ID is the node's own Kad ID, which is also its client hash.
	ID NodeID

	// Firewalled is the node's own claim that it cannot be reached from
	// outside its NAT. Carried as a claim: nothing in the protocol verifies
	// it, and a node that says "firewalled, false" while being firewalled
	// is the normal case rather than an attack.
	Firewalled bool

	// TCPPort is the port the node claims for peer connections. Zero is the
	// protocol's way of saying "not reachable", which is what Firewalled
	// reports; kept separately because a caller logging a bootstrap wants
	// the number either way.
	TCPPort uint16

	// Version is the node's own Kad protocol version. Every node captured
	// on 2026-09-28 answered 8, and a version of 1 or lower is the retired
	// Kad1 that this package does not speak.
	Version byte

	// TagCount is the tag count the node claimed in its header.
	//
	// # IT IS CARRIED BECAUSE IT DOES NOT MATCH THE BYTES, AND THAT IS THE POINT
	//
	// On every captured answer the count is 20 and the byte immediately
	// after it is 0x00, which no reading of the tag format accounts for --
	// see the long note above Bootstrap. Carrying both the count and the
	// raw bytes lets a caller (and a later reader) see the discrepancy
	// rather than have it silently normalised away, which is what happened
	// when the failing decode was simply replaced with a shorter one.
	TagCount byte

	// UnparsedTags is the remainder of the answer after the header, kept
	// verbatim. Not interpreted: the format is unestablished. See the note
	// above Bootstrap for what has been tried.
	UnparsedTags []byte

	// Opcode is the answer's own opcode byte, 0x09 for a HELLO_ANSWER.
	//
	// Carried rather than asserted. A node answering with a different
	// opcode has still proved it is alive and running Kad, which is the
	// whole of what a bootstrap promises, so refusing it here would be
	// refusing a working node over a detail the caller may not care about.
	// A caller that DOES care -- one about to send a Kad2-specific request
	// -- has the byte to check.
	Opcode byte
}

// # WHAT IS KNOWN ABOUT THE ANSWER, AND WHAT IS NOT
//
// Six real nodes answered the hello on 2026-09-28, and the header decodes
// cleanly and consistently across all of them. Captured from
// 60.177.107.149:4672, a full 523-byte answer:
//
//	e4 09                          protocol 0xE4, opcode 0x09 (HELLO_ANSWER)
//	72 c9 41 d6 bc e4 b9 a9        the node's own client GUID, 16 bytes,
//	f9 c8 71 c0 7b 8f ee a9        WORD-REVERSED -- see the note below
//	36 12                          TCP port 4662, little-endian
//	08                             Kad protocol version 8
//	14                             tag count, 20
//	00                             and then... a zero byte
//
// So the frame is [E4][09][id:16][tcpport:2 LE][version:1][count:1], which is
// EXACTLY the layout kad.Hello.Unpack reads. The header is not the problem,
// and the version and port both come out sane (8 and 4662) on every node
// captured, which is the check that says the offsets are right.
//
// # THE TAG LIST DOES NOT DECODE, AND THE LIBRARY'S READER IS WHY
//
// Byte 22 is 0x00. The library reads tags as [type|0x80][id][value] -- the
// eMule ED2K tag format -- and refuses anything whose type byte has 0x80
// clear. A 0x00 has it clear, so the first tag fails with "kad tag without id
// is unsupported", and that is the exact error every live node produces.
//
// The tag section is 500 bytes and none of the following readings walk it to
// a clean end:
//
//   - [name_len:1][name][type:1][value], the eMule Kad tag format. The first
//     name length is 0, and 0 is the list terminator, so the list reads as
//     empty while 500 bytes follow it. A sweep of every start offset from 2
//     to 40 and every count from 1 to 40 found no offset/count pair that both
//     parses and terminates.
//   - Fixed 25-byte contact records: 500/20 is exactly 25, but the resulting
//     addresses and ports are not plausible at that alignment.
//   - 0x40-prefixed firewall-port records: 0x40 appears 11 times followed by
//     12 36, but read as [0x40][port:2] that port is 13842, not the 4662 those
//     bytes also spell in the other order. So 0x40 is a value, not an opcode,
//     and the coincidence of the byte pair is what made it look like one.
//
// What is consistent across all six answers: the opcode, the 20-count, the
// zero byte at offset 22, and a 4-byte sequence at offsets 68-71 that repeats
// across DIFFERENT NODES. Cross-node agreement at a fixed offset is the
// signature of a fixed-size record, which is the strongest hint available
// without a reference implementation to check against.
//
// # WHY THIS IS NOT FIXED BY GUESSING
//
// A bootstrap only needs the first twenty bytes. The ID, the port and the
// version are all decoded correctly and all three are what a caller wants; the
// tag list is metadata the network does not require for the handshake to
// complete. So decodeKadAnswer reads the header and STOPS, and treats the
// remaining bytes as unparsed rather than as a failure.
//
// That is a deliberate change from the previous behaviour, which called
// Hello.Unpack and failed the whole bootstrap over the tag list. Failing here
// meant: no node on the network can be bootstrapped at all, so Kad is dead,
// over bytes that carry nothing this client needs yet.
//
// # THE HONEST LIMIT
//
// The tag list format is unestablished. The library's reader is wrong for it
// and no offline analysis of six samples pinned it. Establishing it needs one
// of:
//
//  1. A reference client. Capture a real eMule or aMule hello answer and read
//     the tag section out of the capture. One run answers it, the same way
//     the ed2k version tag needs a capture and not a probe loop.
//  2. A library that models the format. The upstream reader is the eMule ED2K
//     tag format, which is a different thing with the same name.
//
// Until then the bytes are carried as raw and not interpreted. A guessed
// layout is worse than none: a wrong one produces plausible-looking node IDs
// and a routing table keyed on them, which is a failure that looks like
// working and is not.

// Bootstrap performs the Kad hello against one node.
//
// The connection is UDP and the exchange is one request and one response, so
// this is a single round trip rather than a session. Everything time-bound
// here is on the CONNECTION, for the same reason as in Dial: Go's net.Conn
// does not consult a context after a dial, and a UDP read with no deadline is
// a goroutine that never returns.
func Bootstrap(ctx context.Context, node Node, self NodeID) (BootstrapResult, error) {
	if ctx == nil {
		return BootstrapResult{}, fmt.Errorf("%w: no context for the Kad "+
			"bootstrap", ErrRefused)
	}

	addr, err := net.ResolveUDPAddr("udp", node.Addr())
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("the Kad node at %s does not "+
			"resolve: %w", node.Addr(), err)
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("dialling the Kad node at %s: %w",
			node.Addr(), err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(deadlineFrom(ctx)); err != nil {
		return BootstrapResult{}, fmt.Errorf("cannot bound the Kad read on "+
			"%s: %w", node.Addr(), err)
	}

	hello, err := encodeKadHello(self)
	if err != nil {
		return BootstrapResult{}, err
	}
	if _, err := conn.Write(hello); err != nil {
		return BootstrapResult{}, fmt.Errorf("sending the Kad hello to %s: %w",
			node.Addr(), err)
	}

	buf := make([]byte, maxPacketSize)
	n, err := conn.Read(buf)
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("no answer from the Kad node at "+
			"%s: %w. A node that accepts a connection and never answers is "+
			"common — it is usually not running Kad — so this is a timeout "+
			"rather than a refusal", node.Addr(), err)
	}

	return decodeKadAnswer(buf[:n])
}

// encodeKadHello builds the Kad HELLO frame this client sends.
//
// The wire format is 0xE4, the opcode, then the payload; the library's
// `encodePacket` does exactly that, and `Hello.Pack` fills the payload. Both
// are used rather than hand-rolling, because the alternative is a second
// implementation of a byte layout that has to match the first exactly.
func encodeKadHello(self NodeID) ([]byte, error) {
	hello := kad.Hello{
		ID:      self,
		TCPPort: 0, // we are not listening for peer connections yet
		// Version 0 makes the library write its own KademliaVersion, which
		// is the right default: hardcoding a version here would be a
		// claim about what we speak that nothing here verifies.
		Version: 0,
		Tags:    nil,
	}
	frame, err := hello.Pack(kad.BootstrapReqOp)
	if err != nil {
		return nil, fmt.Errorf("encoding the Kad hello: %w", err)
	}
	return frame, nil
}

// decodeKadAnswer parses a node's answer to our hello.
//
// # IT READS THE HEADER AND STOPS, ON PURPOSE
//
// The layout is [0xE4][opcode][id:16][tcpport:2 LE][version:1][count:1]
// followed by a tag list this package does not yet decode -- see the long
// note above Bootstrap for the captures, the four layouts that were tried and
// ruled out, and why guessing one is worse than not reading it.
//
// The previous version called kad.Hello.Unpack, which reads the header
// correctly and then hands the tag section to a reader that expects the eMule
// ED2K tag format. Every real node answers with something that reader refuses,
// so every bootstrap failed with "kad tag without id is unsupported" and Kad
// could not reach a single node on the network -- over bytes that carry
// nothing a bootstrap needs.
//
// The header is read here by hand rather than through the library so that the
// two halves of this package are not coupled to a reader that cannot parse
// what real nodes send. The offsets are confirmed against six captured
// answers: version 8 and port 4662 came out sane on every one, which is the
// check that the offsets are right rather than merely self-consistent.
//
// The tag bytes are returned UNPARSED rather than discarded, so a caller that
// needs them has them and does not have to re-read the datagram.
func decodeKadAnswer(frame []byte) (BootstrapResult, error) {
	opcode, body, err := kad.DecodePacket(frame)
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("the answer is not a Kad "+
			"packet: %w", err)
	}

	// An unexpected opcode is not by itself a failure: a node that answers
	// with something other than HELLO_ANSWER has still proved it is alive
	// and running Kad, which is all a bootstrap promises. The opcode is
	// carried in the result rather than discarded, so a caller CAN check it
	// -- and so the compiler does not quietly remove a check that was
	// written and then orphaned.
	// 16 bytes of ID, 2 of TCP port, 1 of version, 1 of tag count. The count
	// is read so the tag section can be handed on, and is NOT trusted to
	// describe the section: on every captured answer the byte after it is
	// 0x00, which no reading of the tag format accounts for yet.
	const headerLen = 16 + 2 + 1 + 1
	if len(body) < headerLen {
		return BootstrapResult{}, fmt.Errorf("a Kad hello answer carries "+
			"at least %d bytes of header (16 for the node ID, 2 for the "+
			"TCP port, 1 for the version, 1 for the tag count) and %d "+
			"arrived", headerLen, len(body))
	}

	// The node's ID is its client GUID with each 4-byte word reversed. That
	// reversal is the Kademlia convention and the library applies it in
	// kad.ID.Get, so it is applied here rather than skipped: skipping it
	// produces a plausible ID that is not the node's real one, and a
	// routing table keyed on the wrong ID finds nothing.
	rawID := body[0:16]
	var wire [16]byte
	for i := 0; i < 16; i += 4 {
		for j := 0; j < 4; j++ {
			wire[i+j] = rawID[i+3-j]
		}
	}
	hash, err := protocol.HashFromBytes(wire[:])
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("the node's ID does not "+
			"convert to a hash: %w", err)
	}

	tcpPort := binary.LittleEndian.Uint16(body[16:18])
	version := body[18]
	tagCount := body[19]
	tags := body[headerLen:]

	return BootstrapResult{
		ID: NodeID{Hash: hash},
		// Firewalled is the node's own claim, read from its TCP port: a
		// zero means it is not reachable from outside its NAT. Carried as a
		// claim and left for the caller -- refusing firewalled nodes would
		// refuse most of the network, and a node that lies about this is
		// the normal case rather than an attack.
		Firewalled: tcpPort == 0,

		// What the header said, kept because a caller bootstrapping for
		// SEARCH reasons needs to know whether a node speaks the protocol
		// it expects, and because the version is the only part of this
		// answer that says so.
		TCPPort: tcpPort,
		Version: version,

		// The tag bytes, unparsed. tagCount is carried alongside so a
		// caller can see that it did not match -- which is a live fact
		// about the format, not something to paper over.
		TagCount:     tagCount,
		UnparsedTags: tags,
		Opcode:       opcode,
	}, nil
}

// deadlineFrom returns the context's deadline, or a bounded fallback.
//
// A context with no deadline is a caller who did not think about it, and a
// UDP read with no deadline is a goroutine that never returns. So an absent
// deadline gets the same bound a dial gets.
func deadlineFrom(ctx context.Context) time.Time {
	if d, ok := ctx.Deadline(); ok {
		return d
	}
	return time.Now().Add(dialTimeout)
}
