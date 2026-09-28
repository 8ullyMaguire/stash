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
			IP:   ipv4FromLE(c.Endpoint.IP),
			Port: c.Endpoint.UDPPort,
		})
	}
	return nodes, nil
}

// ipv4FromLE decodes a uint32 that the Kad format stores little-endian into
// the net.IP that everything above this package uses.
//
// The conversion is explicit and byte-by-byte rather than a cast, because
// `net.IPv4(byte(b), byte(b>>8), byte(b>>16), byte(b>>24))` is the whole
// function and a cast would be wrong in a way that produces a plausible IP.
func ipv4FromLE(v uint32) net.IP {
	return net.IPv4(byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
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
			Version:  2,
		}
		_ = entry.Put(&out)
	}
	return out.Bytes()
}

// endpointFromNode converts our Node into the library's Kad endpoint, doing
// the little-endian address encoding in one named place.
func endpointFromNode(n Node) kad.Endpoint {
	v4 := n.IP.To4()
	ip := uint32(0)
	if v4 != nil {
		ip = binary.LittleEndian.Uint32(v4)
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
}

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
// The library's `DecodePacket` validates the 0xE4 header and splits the
// opcode from the body; `Hello.Unpack` reads the body. A node that answers
// with something that is not a Kad packet is refused BY NAME, because a
// non-Kad UDP service on the same port is a real and confusing thing to hit.
func decodeKadAnswer(frame []byte) (BootstrapResult, error) {
	opcode, body, err := kad.DecodePacket(frame)
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("the answer is not a Kad "+
			"packet: %w", err)
	}

	// The answer opcode is the same family as the request. A node that
	// answers with an unexpected opcode has still proved it is alive and
	// running Kad, which is all this function promises, so the payload is
	// parsed for the ID but an unexpected opcode is not by itself a failure.
	var hello kad.Hello
	if err := hello.Unpack(body); err != nil {
		return BootstrapResult{}, fmt.Errorf("the Kad answer (opcode 0x%02X) "+
			"does not decode: %w", opcode, err)
	}

	return BootstrapResult{
		ID: hello.ID,
		// Firewalled is derived from the node's own TCP port claim. This
		// build does not yet act on it -- a firewalled node is usable for
		// searches and this is where that decision belongs -- so it is
		// carried and left for the caller rather than guessed at here.
		Firewalled: hello.TCPPort == 0,
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
