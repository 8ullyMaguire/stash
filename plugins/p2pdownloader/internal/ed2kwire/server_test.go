package ed2kwire

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monkeyWie/goed2k/protocol"
	"github.com/monkeyWie/goed2k/protocol/client"
)

// # WHAT THIS FILE IS FOR
//
// Four refusals, each of which is a plugin that starts cleanly and then does
// nothing. A dial that succeeds against something that is not a server, a dial
// that hangs forever against a server that never speaks, a read that accepts a
// truncated packet, and a hello that is wrong in a way our own decoder cannot
// detect because we wrote it.
//
// That last one is the reason the golden-bytes test exists at all. Every
// encoder agrees with its own decoder, so a wrong size field produces a
// correctly-decoded hello of the wrong length — locally invisible, and on the
// network a desynchronised stream that fails much later with an error pointing
// somewhere else.

// helloServer starts a loopback listener that speaks the given script, and
// returns its address.
//
// The listener is closed when the test ends, and the accept loop stops as soon
// as the test's own context is done, so a test that leaves Dial hanging cannot
// leave the goroutine running either.
func helloServer(t *testing.T, script func(conn net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return // the listener closed: the test finished
		}
		defer conn.Close()
		script(conn)
	}()

	return ln.Addr().String()
}

// frameBody frames a payload the way the WIRE wants it, for the fake servers
// in this file.
//
// Size counts the opcode byte, so it is len(body)+1 and NOT len(body). Writing
// len(body) here is the bug this helper exists to make impossible to write
// twice: our own decoder would still read the body back correctly, so the
// error is invisible until a real server desynchronises.
func frameBody(body []byte) []byte {
	var out bytes.Buffer
	header := protocol.PacketHeader{
		Protocol: protocol.EdonkeyHeader,
		Size:     int32(len(body)) + 1, // +1 for the opcode
		Packet:   opHello,
	}
	_ = header.Put(&out)
	out.Write(body)
	return out.Bytes()
}

// serverHelloBody builds a valid HelloAnswer body, so a test can frame it.
func serverHelloBody(t *testing.T, tags protocol.TagList) []byte {
	t.Helper()
	var body bytes.Buffer
	answer := client.HelloAnswer{
		Hash:        protocol.MustHashFromString("0123456789ABCDEF0123456789ABCDEF"),
		Point:       protocol.Endpoint{},
		Properties:  tags,
		ServerPoint: protocol.Endpoint{},
	}
	if err := answer.Put(&body); err != nil {
		t.Fatalf("cannot encode the test's hello: %v", err)
	}
	return body.Bytes()
}

// TestDialRefusesAConnectionThatIsNotAnED2KServer is the captive-portal case.
//
// A TCP connect succeeds and something answers. If that answer is not an ED2K
// packet, the refusal has to happen here — and the error has to name the byte
// it read, because "connection failed" sends the operator to check their
// firewall when the real answer is "that is not a server".
func TestDialRefusesAConnectionThatIsNotAnED2KServer(t *testing.T) {
	addr := helloServer(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	})

	_, err := Dial(context.Background(), addr)
	if err == nil {
		t.Fatal("an HTTP response was accepted as an ed2k server")
	}
	if !errors.Is(err, ErrNotAServer) {
		t.Errorf("err = %v, want it to wrap ErrNotAServer. The bytes on the "+
			"wire decide this, not the fact that a socket connected", err)
	}
	// 'H' is 0x48. Naming the byte is the difference between an operator
	// checking DNS and an operator checking nothing.
	if !strings.Contains(err.Error(), "0x48") {
		t.Errorf("err = %q, want it to name the protocol byte it read "+
			"(0x48 for 'H'). Without the byte, every failure here reads as "+
			"the same opaque problem", err)
	}
}

// TestDialRefusesAConnectionThatSpeaksKadOnTCP: Kademlia's 0xE4 header is a
// real ED2K-family byte, and it is NOT a server. A TCP stream that opens with
// it is a misdirected UDP packet or a wrong port, and accepting it would leave
// a client waiting for a hello that will never come.
func TestDialRefusesAConnectionThatSpeaksKadOnTCP(t *testing.T) {
	addr := helloServer(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte{byte(protocol.KademliaHeader), 1, 0, 0, 0, 0x01})
	})

	_, err := Dial(context.Background(), addr)
	if !errors.Is(err, ErrNotAServer) {
		t.Errorf("err = %v, want ErrNotAServer. 0xE4 is Kademlia and this is "+
			"a TCP stream: a server's hello never opens with it", err)
	}
}

// TestDialRefusesAServerThatHangsBeforeSayingHello is the "starts and does
// nothing" case in its purest form.
//
// A listener that accepts and never writes. The dial MUST fail, and it must
// fail on a DEADLINE rather than by returning a wrong error.
//
// # THE ASSERTION IS ON A BACKGROUND CONTEXT, AND THAT IS THE POINT
//
// This test passes `context.Background()`, and that is deliberate. It was
// written first with a 2-second context, and removing the connection deadline
// from `Dial` did NOT break it — the test passed, proving the context alone
// was bounding the read. A test that a defect survives is not a test, and this
// one had been quietly measuring the context rather than the deadline.
//
// With no context deadline there is exactly one thing that can end the wait,
// and it is `conn.SetDeadline` in Dial. So the timing assertion is the proof,
// and it is checked against the package's own dialTimeout rather than a
// hand-written number that would drift from it.
func TestDialRefusesAServerThatHangsBeforeSayingHello(t *testing.T) {
	// A header that PROMISES A PAYLOAD and then stalls, so the wait is on the
	// payload read — the one that has to be bounded.
	//
	// Size counts the opcode, so Size=41 means a 40-byte payload follows. The
	// header written here is `Size=1`, which is a payload of ZERO bytes: the
	// read returns immediately and the test measures nothing. That was the
	// first version's bug, and the `elapsed < dialTimeout` assertion caught
	// it — which is the reason that lower bound is here at all.
	addr := helloServer(t, func(conn net.Conn) {
		var out bytes.Buffer
		header := protocol.PacketHeader{
			Protocol: protocol.EdonkeyHeader,
			Size:     41, // 40 payload bytes + the opcode
			Packet:   opHello,
		}
		_ = header.Put(&out)
		_, _ = conn.Write(out.Bytes())
		// Sleep well past dialTimeout, so a Dial with no deadline on the
		// READ blocks here. The test's own budget is the assertion, and it
		// must be shorter than this sleep or the test would pass either way.
		time.Sleep(dialTimeout + 30*time.Second)
	})

	start := time.Now()
	_, err := Dial(context.Background(), addr)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a silent server produced a Server")
	}
	// A generous upper bound, and it is checked against dialTimeout so it
	// cannot quietly become wrong when the constant changes.
	limit := dialTimeout + 10*time.Second
	if elapsed > limit {
		t.Errorf("Dial took %s to give up on a server that stops mid-packet, "+
			"with a limit of %s. Go's net.Conn does not consult a context "+
			"after the dial returns, so a context-only deadline does not "+
			"bound this read at all — conn.SetDeadline in Dial is the only "+
			"thing that does", elapsed, limit)
	}
	if elapsed < dialTimeout {
		t.Errorf("Dial gave up after %s, sooner than the %s deadline. It "+
			"failed for some reason other than waiting, so this test is "+
			"not exercising the thing it is named for", elapsed, dialTimeout)
	}
}

// TestDialRefusesAPacketThatEndsMidPacket is the desync case.
//
// A header promises 200 payload bytes and 20 arrive. This must be a refusal
// and not a Server built from a short hello, because the framing is
// positional: a wrong length here means every later offset is wrong too, and
// the next read would consume payload as a header.
func TestDialRefusesAPacketThatEndsMidPacket(t *testing.T) {
	addr := helloServer(t, func(conn net.Conn) {
		// A header promising 200 payload bytes, of which 20 arrive.
		var out bytes.Buffer
		header := protocol.PacketHeader{
			Protocol: protocol.EdonkeyHeader,
			Size:     201, // 200 payload + the opcode
			Packet:   opHello,
		}
		_ = header.Put(&out)
		_, _ = out.Write(make([]byte, 20)) // 20 of the promised 200
		_, _ = conn.Write(out.Bytes())
	})

	_, err := Dial(context.Background(), addr)
	if !errors.Is(err, ErrTruncatedPacket) {
		t.Errorf("err = %v, want ErrTruncatedPacket. A short read is not a "+
			"retry: the stream is length-prefixed, so every offset after "+
			"this one is wrong too", err)
	}
}

// TestDialRefusesAnOversizedPacket: the size arrives from a stranger in the
// first six bytes of every packet, so it is bounded before the read that
// honours it. Without the cap, one crafted header chooses our memory use.
func TestDialRefusesAnOversizedPacket(t *testing.T) {
	addr := helloServer(t, func(conn net.Conn) {
		var out bytes.Buffer
		header := protocol.PacketHeader{
			Protocol: protocol.EdonkeyHeader,
			// 1 GiB, which is a plausible thing for a hostile peer to claim
			// and an impossible thing for a hello to be. It has to fit an
			// int32 -- the wire field is 32 bits -- so 1 GiB rather than the
			// 2 GiB that would not compile here.
			Size:   1 << 30,
			Packet: opHello,
		}
		_ = header.Put(&out)
		_, _ = conn.Write(out.Bytes())
		time.Sleep(2 * time.Second)
	})

	start := time.Now()
	_, err := Dial(context.Background(), addr)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a 2 GiB packet header was accepted")
	}
	if elapsed > 15*time.Second {
		t.Errorf("Dial spent %s on a header claiming 2 GiB. The size is "+
			"checked BEFORE the read that would allocate for it", elapsed)
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("err = %q, want it to say the size is over the limit this "+
			"client will read", err)
	}
}

// TestTheHelloWeSendIsTheHelloTheProtocolDescribes is the golden test, and the
// reason this file is not just refusals.
//
// Every encoder agrees with its own decoder. A hello encoded with the wrong
// size field decodes correctly HERE and desynchronises the stream on a real
// server, failing much later with an error pointing somewhere else entirely.
// So the expected bytes are written out literally, derived from the protocol
// description rather than from the encoder's output.
//
// The layout of a HELLOANS payload is:
//
//	[16] server GUID
//	[ 6] our endpoint as the server sees it
//	[ 1] tag count
//	  ... the tags
//	[ 6] the server's own endpoint
func TestTheHelloWeSendIsTheHelloTheProtocolDescribes(t *testing.T) {
	// A listener that captures exactly what we send.
	captured := make(chan []byte, 1)
	addr := helloServer(t, func(conn net.Conn) {
		_, _ = conn.Write(frameBody(serverHelloBody(t, nil)))

		// Read the header, then the body, exactly as a server would.
		buf := make([]byte, 6)
		if _, err := readFull(conn, buf); err != nil {
			captured <- nil
			return
		}
		var rh protocol.PacketHeader
		if err := rh.Get(bytes.NewReader(buf)); err != nil {
			captured <- nil
			return
		}
		body := make([]byte, rh.SizePacket())
		if _, err := readFull(conn, body); err != nil {
			captured <- nil
			return
		}
		captured <- append(append([]byte{}, buf...), body...)
	})

	srv, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer srv.Close()

	got := <-captured
	if len(got) < 6 {
		t.Fatal("the server received no framed hello")
	}

	// The header: 0xE3, then the size LITTLE-ENDIAN, then the opcode.
	if got[0] != protocol.EdonkeyHeader {
		t.Errorf("protocol byte = 0x%02X, want 0x%02X", got[0],
			protocol.EdonkeyHeader)
	}
	//
	// Size counts the OPCODE byte as well as the body, so the size on the wire
	// is one MORE than the body length. A header that claims the body length
	// exactly is the classic off-by-one here, and it is invisible locally
	// because our decoder subtracts the same one — on a real server every
	// later packet then decodes as garbage.
	size := int32(uint32(got[1]) | uint32(got[2])<<8 |
		uint32(got[3])<<16 | uint32(got[4])<<24)
	body := len(got) - 6
	if want := int32(body) + 1; size != want {
		t.Errorf("the header says %d payload bytes and %d were sent (so the "+
			"size should be %d, counting the opcode byte). The opcode "+
			"must be counted: a header that omits it desynchronises the "+
			"stream and the next read starts one byte early",
			size, body, want)
	}
	if got[5] != opHello {
		t.Errorf("opcode = 0x%02X, want 0x%02X (HELLO)", got[5], opHello)
	}

	// The body decodes, and the fields are the ones we meant to send. A
	// zero GUID and a zero endpoint are deliberate — see sendHello.
	var answer client.HelloAnswer
	if err := answer.Get(bytes.NewReader(got[6:])); err != nil {
		t.Fatalf("our own hello does not decode: %v", err)
	}
	if !answer.Hash.Equal(protocol.Invalid) {
		t.Errorf("GUID = %s, want the zero GUID. This build does not persist "+
			"one yet, and a random GUID that changes every connection looks "+
			"to a server like a client with amnesia", answer.Hash)
	}
	if !answer.Point.IsZero() {
		t.Errorf("our endpoint = %s, want zero. We are not listening for "+
			"callbacks, and advertising a routable address would be a "+
			"claim the plugin cannot honour", answer.Point)
	}
}

// TestTheServerGUIDAndCountsComeFromItsHello: what the server told us is
// carried through, and a server that omits the optional count tags is not an
// error.
func TestTheServerGUIDAndCountsComeFromItsHello(t *testing.T) {
	t.Run("with counts", func(t *testing.T) {
		tags := protocol.TagList{
			{Type: protocol.TagTypeUint32, ID: tagUserCount, UInt32: 1234},
			{Type: protocol.TagTypeUint32, ID: tagFileCount, UInt32: 56789},
		}
		srv := dialFake(t, serverHelloBody(t, tags))
		defer srv.Close()

		if srv.Users() != 1234 {
			t.Errorf("Users() = %d, want 1234", srv.Users())
		}
		if srv.Files() != 56789 {
			t.Errorf("Files() = %d, want 56789", srv.Files())
		}
		if srv.Hash().String() != "0123456789ABCDEF0123456789ABCDEF" {
			t.Errorf("Hash() = %s, want the GUID the server sent",
				srv.Hash())
		}
	})

	t.Run("counts absent", func(t *testing.T) {
		// Both tags are optional. A server that sends neither is normal, and
		// treating it as malformed would refuse a working server.
		srv := dialFake(t, serverHelloBody(t, nil))
		defer srv.Close()
		if srv.Users() != 0 || srv.Files() != 0 {
			t.Errorf("Users()=%d Files()=%d, want 0 and 0 for a server that "+
				"sent no count tags", srv.Users(), srv.Files())
		}
	})

	t.Run("a count tag of the wrong type is ignored", func(t *testing.T) {
		// A STRING tag with the user-count ID. Reading its string as a
		// number is a stranger's bytes reinterpreted as a quantity, so it
		// must be skipped rather than parsed.
		tags := protocol.TagList{
			{Type: protocol.TagTypeString, ID: tagUserCount,
				String: "9999999999"},
		}
		srv := dialFake(t, serverHelloBody(t, tags))
		defer srv.Close()
		if srv.Users() != 0 {
			t.Errorf("Users() = %d, want 0: a STRING tag is not a count",
				srv.Users())
		}
	})

	t.Run("an absurd count is carried, not clamped", func(t *testing.T) {
		// 0xFFFFFFFF as a uint32 becomes -1 as an int32. Clamping it to 0
		// would make a lying server look like an honest empty one; the
		// value stays visibly absurd instead.
		tags := protocol.TagList{
			{Type: protocol.TagTypeUint32, ID: tagUserCount,
				UInt32: 0xFFFFFFFF},
		}
		srv := dialFake(t, serverHelloBody(t, tags))
		defer srv.Close()
		if srv.Users() != -1 {
			t.Errorf("Users() = %d, want -1. A server's claim is carried as "+
				"sent rather than clamped, so a lie stays visible", srv.Users())
		}
	})
}

// dialFake stands up a server that sends one valid hello and then reads.
func dialFake(t *testing.T, body []byte) *Server {
	t.Helper()
	addr := helloServer(t, func(conn net.Conn) {
		_, _ = conn.Write(frameBody(body))
		// Drain whatever we send, then let the test close.
		_, _ = readFull(conn, make([]byte, 4096))
	})

	srv, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return srv
}

// TestDialRefusesANilContext: the guard that keeps a nil context from
// panicking inside net.Dialer. A plugin that panics is a plugin the host
// records as a finished task.
func TestDialRefusesANilContext(t *testing.T) {
	//lint:ignore SA1012 deliberately passing nil to prove it is refused
	_, err := Dial(nil, "127.0.0.1:1") //nolint:staticcheck
	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused for a nil context. net.Dialer "+
			"panics on one, and a plugin that panics is a plugin the host "+
			"has already finished with", err)
	}
}

// TestThePackageBoundaryIsTheHashBoundary enforces the rule in the package doc.
//
// internal/ed2k owns the eHash and goed2k computes it wrongly. The protection
// against that is not discipline, it is that the hasher cannot reach the wire
// package. This asserts the shape of that: the ed2k package's own import graph
// must not contain the wire library.
func TestThePackageBoundaryIsTheHashBoundary(t *testing.T) {
	// The boundary is a SOURCE fact, and Go cannot ask a package about its
	// own imports, so the check runs `go list`. A `grep` in a shell would be
	// the same fact with worse failure reporting.
	out, err := runGoList(t, "-deps", "-f",
		"{{range .Imports}}{{.}}\n{{end}}", "./internal/ed2k/...")
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, out)
	}

	if strings.Contains(out, "goed2k") {
		t.Errorf("internal/ed2k reaches the wire library:\n%s\n\n"+
			"goed2k's HashFromHashSet is WRONG — it MD4s full 16-byte part "+
			"hashes where the protocol requires the first 8 bytes of "+
			"each. A hasher that can reach it will hash a multi-part file "+
			"to a value matching nothing on the network, and no test will "+
			"notice, because the library agrees with itself", out)
	}
}

// TestTheServerHelloIsTheProtocolsShape checks the HELLOANS body length we
// send, since a wrong length is the desync described above and a length is
// easier to pin than a byte sequence.
func TestTheServerHelloIsTheProtocolsShape(t *testing.T) {
	// 16 GUID + 6 our endpoint + 4 tag count (a uint32, zero tags) + 6 endpoint.
	//
	// The tag count being FOUR bytes rather than one is the kind of detail that
	// is obvious in the protocol description and easy to get wrong on the
	// first pass: an eMule tag list starts with a uint32 count, and a
	// one-byte count desynchronises the whole packet.
	const want = 16 + 6 + 4 + 6

	var body bytes.Buffer
	answer := client.HelloAnswer{
		Hash:        protocol.Invalid,
		Point:       protocol.Endpoint{},
		Properties:  protocol.TagList{},
		ServerPoint: protocol.Endpoint{},
	}
	if err := answer.Put(&body); err != nil {
		t.Fatal(err)
	}
	if got := body.Len(); got != want {
		t.Errorf("our hello body is %d bytes, want %d. With no tags the "+
			"layout is 16 GUID + 6 endpoint + 4 tag count + 6 endpoint",
			got, want)
	}
	if got := answer.BytesCount(); got != want {
		t.Errorf("BytesCount() = %d, want %d. The count is what goes in the "+
			"header's size field, so a wrong count desynchronises the "+
			"stream", got, want)
	}
}

// readFull is a small helper so the test file does not import io for two
// calls, and so a short read is visible at the call site.
func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// runGoList runs `go list` in the plugin module and returns its output.
//
// The module root is found by walking up from this test file, so the test does
// not hardcode a path and does not depend on the working directory the harness
// happens to use.
func runGoList(t *testing.T, args ...string) (string, error) {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above the test's directory")
		}
		dir = parent
	}

	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return stderr.String(), fmt.Errorf("go list: %w", err)
	}
	return string(out), nil
}
