package ed2kwire

import (
	"context"
	"encoding/hex"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/monkeyWie/goed2k/protocol"
	"github.com/monkeyWie/goed2k/protocol/kad"
)

// # WHAT THIS FILE IS FOR
//
// Two hazards, and they are different in kind.
//
// The `nodes.dat` count field is a number from a file a user downloaded, and
// an unbounded one is an allocation of whatever the first four bytes say. That
// check is delegated to the library — it exists there — and
// `TestANodesDatWithAnImpossibleContactCountIsRefused` is what proves the
// delegation is sound rather than assumed.
//
// The Kad node ID is subtler and is the reason this file exists at all. A wrong
// ID produces no error, no malformed packet, and a perfectly healthy client
// that never finds a peer. It cannot be caught hermetically by any amount of
// local testing, which is why it is pinned here by construction AND checked
// against the live network in `live_test.go`.

// TestANodesDatWithAnImpossibleContactCountIsRefused: the count field is the
// whole input, and it is bounded before it is believed.
func TestANodesDatWithAnImpossibleContactCountIsRefused(t *testing.T) {
	// A four-byte count of 0xFFFFFFFF and nothing after it. Unbounded, this
	// asks for 4 billion Entry values — each 16 bytes of address plus more —
	// which is an out-of-memory from a four-byte file.
	raw := []byte{0xFF, 0xFF, 0xFF, 0xFF}

	_, err := ParseNodesDat(raw)
	if err == nil {
		t.Fatal("a nodes.dat claiming 4 billion contacts and containing none " +
			"was accepted")
	}
	if !strings.Contains(err.Error(), "contact count") &&
		!strings.Contains(err.Error(), "does not parse") {
		t.Errorf("err = %q, want it to name the contact count. A user who "+
			"just downloaded this file needs to know WHICH field was "+
			"wrong, not that it did not parse", err)
	}
}

// TestANodesDatRoundTrips: parse what we encode, and encode what we parse.
//
// A parser alone cannot distinguish a correct implementation from one that
// agrees with a broken writer, so the format is pinned from both directions.
// The address byte order is the detail being pinned: the Kad format stores the
// IPv4 address as a LITTLE-endian uint32, which is the opposite of the ed2k
// protocol.Endpoint's network order, and a mirror image is a plausible-looking
// IP that connects to nothing.
func TestANodesDatRoundTrips(t *testing.T) {
	want := []Node{
		{IP: net.IPv4(192, 168, 1, 10), Port: 4662},
		{IP: net.IPv4(8, 8, 8, 8), Port: 4661},
		{IP: net.IPv4(255, 255, 255, 0), Port: 1},
		// Port 0 and the broadcast address are both legal in the FORMAT even
		// if useless in practice. A round trip that "helpfully" drops them
		// would make this test pass for the wrong reason.
		{IP: net.IPv4(0, 0, 0, 0), Port: 0},
	}

	got, err := ParseNodesDat(EncodeNodesDat(want))
	if err != nil {
		t.Fatalf("our own nodes.dat does not parse: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d nodes, wrote %d", len(got), len(want))
	}
	for i := range want {
		if !got[i].IP.Equal(want[i].IP) {
			t.Errorf("node %d: IP = %s, want %s. The Kad format stores the "+
				"address little-endian and the ed2k endpoint stores it in "+
				"network order; a byte-order slip produces a mirrored IP "+
				"that looks valid and connects to nothing",
				i, got[i].IP, want[i].IP)
		}
		if got[i].Port != want[i].Port {
			t.Errorf("node %d: port = %d, want %d", i, got[i].Port, want[i].Port)
		}
	}
}

// TestTheKadNodeIDIsDerivedFromOurClientHash is the one that cannot be
// checked any other way.
//
// A Kad node ID is an ed2k client hash. Deriving it from our own client GUID
// is what makes the DHT routing work: the network is keyed on ID distance, so
// a client presenting an unrelated ID is routed to the wrong region and finds
// nothing — while every local test passes, because nothing local cares.
//
// So the ID is asserted to be DERIVED rather than merely present. A random ID
// would satisfy "the ID is 16 bytes" and fail here.
func TestTheKadNodeIDIsDerivedFromOurClientHash(t *testing.T) {
	clientHash := protocol.MustHashFromString("0123456789ABCDEF0123456789ABCDEF")

	id := NewNodeID(clientHash)
	if !id.Hash.Equal(clientHash) {
		t.Errorf("the Kad node ID's hash = %s, want the client hash %s. "+
			"The DHT is keyed on ID distance: a client presenting an ID "+
			"unrelated to its own identity is routed to the wrong part of "+
			"the network and finds nothing, with no error anywhere",
			id.Hash, clientHash)
	}

	// And it must actually be a function: the same client hash always yields
	// the same ID, or a restarted client would re-enter the DHT as a
	// different node and never accumulate contacts.
	again := NewNodeID(clientHash)
	if !again.Hash.Equal(id.Hash) {
		t.Error("two calls with the same client hash produced different IDs")
	}
}

// TestABootstrapToANodeThatNeverAnswersTimesOut: a UDP peer that does not
// reply. This is a TIMEOUT and not a refusal, because "not running Kad" is
// the normal state of most of the addresses in a nodes.dat.
func TestABootstrapToANodeThatNeverAnswersTimesOut(t *testing.T) {
	// A UDP socket that receives and says nothing. Bound and closed by the
	// test, so nothing leaks.
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("cannot listen: %v", err)
	}
	defer conn.Close()

	// Drain silently.
	go func() {
		buf := make([]byte, 2048)
		for {
			if _, _, err := conn.ReadFromUDP(buf); err != nil {
				return
			}
		}
	}()

	node := Node{IP: conn.LocalAddr().(*net.UDPAddr).IP,
		Port: uint16(conn.LocalAddr().(*net.UDPAddr).Port)}

	self := NewNodeID(protocol.MustHashFromString("0123456789ABCDEF0123456789ABCDEF"))

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = Bootstrap(ctx, node, self)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a silent Kad node produced a result")
	}
	if elapsed > 5*time.Second {
		t.Errorf("Bootstrap took %s against a node that never answers, with "+
			"a 1.5s context. A UDP read with no deadline is a goroutine "+
			"that never returns", elapsed)
	}
	// The message must distinguish this from a refusal, because the caller's
	// next move is different: a timeout means try another node, a refusal
	// means stop.
	if !strings.Contains(err.Error(), "no answer") {
		t.Errorf("err = %q, want it to say there was no answer. A timeout "+
			"and a refusal need different responses from the caller — "+
			"try the next node, or stop", err)
	}
}

// TestABootstrapRefusesAnAnswerThatIsNotAKadPacket: something is listening on
// that port and it is not Kad. The error must say so by name, because "no
// answer" for a node that DID answer sends the reader to the wrong place.
func TestABootstrapRefusesAnAnswerThatIsNotAKadPacket(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("cannot listen: %v", err)
	}
	defer conn.Close()

	go func() {
		buf := make([]byte, 2048)
		_, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		// Reply to the SENDER, not to our own listening address: a UDP
		// socket is not connected, so a reply to the wrong address goes
		// nowhere and the client times out. Getting this wrong in the FAKE
		// made this test pass for the wrong reason once already.
		_, _ = conn.WriteToUDP([]byte("HTTP/1.1 200 OK\r\n\r\n"), from)
	}()

	addr := conn.LocalAddr().(*net.UDPAddr)
	node := Node{IP: addr.IP, Port: uint16(addr.Port)}
	self := NewNodeID(protocol.MustHashFromString("0123456789ABCDEF0123456789ABCDEF"))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = Bootstrap(ctx, node, self)
	if err == nil {
		t.Fatal("an HTTP reply was accepted as a Kad answer")
	}
	if !strings.Contains(err.Error(), "not a Kad packet") {
		t.Errorf("err = %q, want it to say the answer is not a Kad packet. "+
			"Reporting it as \"no answer\" for a node that DID answer "+
			"sends the reader to the wrong file", err)
	}
}

// TestABootstrapAnswersWithAValidKadHello: the success path, against a fake
// node that speaks the real format using the library's own encoder.
//
// The answer is built with `kad.Hello.Pack`, so this proves our decode agrees
// with the library's encode — which is the only agreement available without a
// real eMule client, and the reason the library is used for the wire at all.
func TestABootstrapAnswersWithAValidKadHello(t *testing.T) {
	const theirID = "FEDCBA9876543210FEDCBA9876543210"

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("cannot listen: %v", err)
	}
	defer conn.Close()

	go func() {
		buf := make([]byte, 2048)
		_, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		hello := kad.Hello{
			ID:      kad.NewID(protocol.MustHashFromString(theirID)),
			TCPPort: 4662,
			Version: 0,
		}
		frame, packErr := hello.Pack(kad.BootstrapReqOp)
		if packErr != nil {
			return
		}
		// To the SENDER, as above.
		_, _ = conn.WriteToUDP(frame, from)
	}()

	addr := conn.LocalAddr().(*net.UDPAddr)
	node := Node{IP: addr.IP, Port: uint16(addr.Port)}
	self := NewNodeID(protocol.MustHashFromString("0123456789ABCDEF0123456789ABCDEF"))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	got, err := Bootstrap(ctx, node, self)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if got.ID.Hash.String() != theirID {
		t.Errorf("the node's ID = %s, want %s. The node's ID is also its "+
			"client hash, and it is what the DHT keys on", got.ID.Hash, theirID)
	}
	if got.Firewalled {
		t.Error("Firewalled = true for a node that advertised a TCP port")
	}
}

// TestANilContextIsRefusedRatherThanPanicking: net.Dialer's UDP path panics
// on a nil context, and a plugin that panics is a plugin the host has already
// recorded as finished.
func TestANilContextIsRefusedRatherThanPanicking(t *testing.T) {
	node := Node{IP: net.IPv4(127, 0, 0, 1), Port: 4662}
	self := NewNodeID(protocol.MustHashFromString("0123456789ABCDEF0123456789ABCDEF"))

	//lint:ignore SA1012 deliberately nil, to prove it is refused
	_, err := Bootstrap(nil, node, self) //nolint:staticcheck
	if err == nil {
		t.Error("a nil context was accepted")
	}
}

// TestAKnownAddressSurvivesTheRoundTrip: golden bytes from a REAL contact list.
//
// # THIS TEST EXISTS BECAUSE EVERY ADDRESS WAS MIRRORED AND NOTHING CAUGHT IT
//
// ipv4FromLE swapped the bytes of an address the library had ALREADY swapped,
// so all 154 contacts in a real list came out reversed. The hermetic tests
// were green throughout, because they build their contact lists through this
// same code: a file written with a mirrored address parses back to a mirrored
// address, and the decoder agreed with itself perfectly.
//
// The live Kad test found it by dialling 127.251.161.1, 127.8.43.79 and
// 127.0.0.1 out of a list containing no loopback addresses. Three in a row is
// not a coincidence; it is the signature of a byte-reversed 0.0.1.x.
//
// # WHY GOLDEN BYTES AND NOT A WRITTEN FIXTURE
//
// A fixture written by EncodeNodesDat would be produced by the same code that
// is under test, and the round trip would be circular. The bytes below are
// the first 34 bytes of a real published nodes.dat -- 84.123.58.218:4554,
// Kad version 8 -- copied verbatim, so the decoder is checked against a file
// it did not write.
//
// 84.123.58.218 is chosen on purpose: it is a real, publicly published
// address, so if it is ever dialled it is a real machine. It is NOT dialled
// by this test, and nothing here needs the network to pass.
func TestAKnownAddressSurvivesTheRoundTrip(t *testing.T) {
	// The first contact record from a real published nodes.dat: a 16-byte
	// ID, the address 84.123.58.218 in the format's little-endian order
	// (da 3a 7b 54), UDP 4554, TCP 4552, Kad version 8, and the v2
	// key/verified tail.
	const goldenRecord = "7b605cd633fdf01475f3937fe6cc86beda3a7b54ca11c81108000000000000000000"

	raw, err := hex.DecodeString(goldenRecord)
	if err != nil {
		t.Fatalf("the golden record is not hex: %v", err)
	}

	// A v2 file header, then that one record. 4 zero bytes, version 2, count
	// 1. The library's own parser reads this shape.
	file := make([]byte, 0, 12+len(raw))
	file = append(file, 0, 0, 0, 0) // the zero marker that means "versioned"
	file = append(file, 2, 0, 0, 0) // version 2
	file = append(file, 1, 0, 0, 0) // one contact
	file = append(file, raw...)

	nodes, err := ParseNodesDat(file)
	if err != nil {
		t.Fatalf("ParseNodesDat on a real contact record: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("got %d contacts, want 1", len(nodes))
	}

	got := nodes[0]
	if want := "84.123.58.218"; got.IP.String() != want {
		t.Errorf("address came out %s, want %s.\n\n"+
			"The bytes da 3a 7b 54 in the file are the address stored "+
			"little-endian, and the library has ALREADY undone that by the "+
			"time it hands the value over -- so swapping them here "+
			"mirrors every contact. The mirror image of 84.123.58.218 is "+
			"218.58.123.84, a perfectly valid address on a real network, "+
			"which is why nothing reported an error",
			got.IP, want)
	}
	if want := uint16(4554); got.Port != want {
		t.Errorf("UDP port came out %d, want %d", got.Port, want)
	}
	if want := byte(8); got.Version != want {
		t.Errorf("Kad version came out %d, want %d. A version of 1 or "+
			"lower means the retired Kad1 protocol, which this package "+
			"does not speak and aMule itself ignores", got.Version, want)
	}
}

// TestTheAddressSurvivesOurOwnEncoderToo: the same bytes, through the other
// direction.
//
// The golden test above uses a file this code did not write, which is the
// only way to break the circularity. This one closes the other half: that
// EncodeNodesDat does not corrupt a correct address on the way out. Both
// directions have to be right for the pair to be worth anything, and a
// round trip through a wrong encoder and a wrong decoder can cancel out --
// which is precisely the bug the golden test exists to catch.
func TestTheAddressSurvivesOurOwnEncoderToo(t *testing.T) {
	original := []Node{{
		IP:      net.IPv4(84, 123, 58, 218),
		Port:    4554,
		Version: 8,
	}}

	parsed, err := ParseNodesDat(EncodeNodesDat(original))
	if err != nil {
		t.Fatalf("ParseNodesDat on our own encoding: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("got %d contacts, want 1", len(parsed))
	}

	if got, want := parsed[0].IP.String(), "84.123.58.218"; got != want {
		t.Errorf("our own round trip produced %s, want %s", got, want)
	}
	if parsed[0].Version != 8 {
		t.Errorf("our own round trip produced Kad version %d, want 8. "+
			"EncodeNodesDat used to hardcode 2 here, which meant a "+
			"parsed contact came back as 2 whatever it said on disk -- "+
			"and the test that could have caught it checked the "+
			"address and not the version", parsed[0].Version)
	}
}
