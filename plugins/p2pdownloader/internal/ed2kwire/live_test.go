//go:build ed2klive

// Live ed2k interop checks. NOT part of `go test ./...`.
//
// # WHY THIS FILE IS BEHIND A BUILD TAG
//
// Three reasons, and the third is the one that matters most.
//
//  1. A suite that depends on a third party's uptime is not a test. It fails
//     for reasons that have nothing to do with this code, and a developer
//     learns to ignore it — which is how a real interop break gets ignored too.
//
//  2. It costs real network time on every run, and M5's hermetic suite is
//     deliberately fast so it gets run.
//
//  3. A live test that SKIPS is worse than no live test: `t.Skip` prints
//     `ok` and the suite's green says nothing about whether interop works. So
//     these tests are not reachable by a plain `go test ./...` at all — the
//     build tag is the mechanism, and a skip is never the answer.
//
// # HOW TO RUN
//
//	go test -tags ed2klive ./internal/ed2kwire/ -v
//
// A failure here is a REAL finding about the wire layer. A timeout is usually
// not: most addresses in a nodes.dat are dead, and a public ed2k server may
// be down or may refuse a client it does not recognise.

package ed2kwire

import (
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/monkeyWie/goed2k/protocol"
)

// liveServers are public ed2k servers used for the hello exchange.
//
// They are read from ED2K_LIVE_SERVERS when it is set, so this file does not
// hardcode a host that may be down in six months' time. The default is empty
// and an empty list is a FAILURE, not a skip: a live run that tested nothing
// and reported success is the exact outcome §3 above is about.
func liveServers(t *testing.T) []string {
	t.Helper()
	raw := os.Getenv("ED2K_LIVE_SERVERS")
	if raw == "" {
		t.Fatal("ED2K_LIVE_SERVERS is not set. A live run with no servers " +
			"would report success having tested nothing, which is worse " +
			"than not running. Set it to a comma-separated host:port list.")
	}
	var out []string
	for _, s := range splitComma(raw) {
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		t.Fatal("ED2K_LIVE_SERVERS is set but empty after parsing")
	}
	return out
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// TestLiveAServerAcceptsOurHello is the highest-value live assertion.
//
// A wrong opcode or a wrong size field produces a server that never answers.
// Nothing hermetic can see that, because our own encoder and decoder agree
// with each other — which is precisely the failure mode the golden-bytes test
// exists to catch and the live test exists to confirm.
func TestLiveAServerAcceptsOurHello(t *testing.T) {
	for _, addr := range liveServers(t) {
		t.Run(addr, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(),
				30*time.Second)
			defer cancel()

			srv, err := Dial(ctx, addr)
			if err != nil {
				// A refusal here is a finding only if it is OUR refusal. A
				// refused CONNECTION is the server's business, so the
				// distinction matters for reading the failure.
				t.Fatalf("Dial(%s): %v", addr, err)
			}
			defer srv.Close()

			// A server that confirmed the login is a server that decoded
			// our request and accepted us. Report what it said: the
			// session token proves the confirmation arrived, and a zero
			// user count is a real and common shape for a server that is
			// up but idle.
			//
			// The token is printed as bytes, not as a GUID. It is eight
			// opaque bytes whose meaning is not established, and printing
			// it in hex-as-a-hash is what made the earlier version of
			// this file believe the client had an identity it did not.
			t.Logf("connected: network users=%d files=%d | "+
				"this server: users=%d files=%d | messages=%v",
				srv.Users(), srv.Files(),
				srv.ServerUsers(), srv.ServerFiles(),
				srv.Messages())
		})
	}
}

// TestLiveKadNodesDatDownloads: the nodes.dat is the only way a client learns
// where the Kad network is, and it is served over HTTP by the same
// infrastructure.
func TestLiveKadNodesDatDownloads(t *testing.T) {
	// # THE HOST MOVED, AND THE OLD ONE 404s
	//
	// This was www.emule-security.org. eMule Security moved the file to the
	// upd. subdomain, which is also the URL aMule ships as its default in
	// amule.conf -- so upd. is the one that will still be there in a year
	// and www. is the one that was there when this was written.
	//
	// # WHY ONE URL AND NOT A LIST
	//
	// A list of mirrors would make this test more likely to pass, and it
	// would also make it stop reporting the thing it exists to report: that
	// a public contact list is reachable and parseable. One URL that works is
	// a finding. Three that work is a coincidence, and the next one to rot
	// takes the test with it.
	const url = "https://upd.emule-security.org/nodes.dat"

	// Not using net/http here on purpose: a fetch that cannot be bounded is
	// the same unbounded-read hazard the packet reader has, and the point of
	// this layer is that every read from a stranger is bounded. So the
	// request is given a client with its own timeout.
	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("fetching %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s returned %s", url, resp.Status)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		t.Fatalf("reading the nodes.dat: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("the nodes.dat was empty")
	}

	nodes, err := ParseNodesDat(raw)
	if err != nil {
		t.Fatalf("the live nodes.dat does not parse: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("the live nodes.dat parsed to zero nodes")
	}
	t.Logf("%d nodes, first: %s", len(nodes), nodes[0].Addr())

	// Spot-check that the addresses are plausible rather than mirrored.
	//
	// # THE OLD CHECK HERE WAS INSUFFICIENT, AND IT MISSED A REAL BUG
	//
	// It only rejected unspecified and multicast addresses, on the reasoning
	// that a byte-order slip produces 0.0.0.0. It does not. A mirrored IPv4
	// address is a perfectly valid unicast address on a perfectly real
	// network -- 84.123.58.218 mirrored is 218.58.123.84, which belongs to
	// somebody. Every one of the 154 contacts passed this check while every
	// one of them was reversed, and Kad was dialling 127.0.0.1.
	//
	// LOOPBACK is the check that catches it, because a mirrored address very
	// often is not loopback and the ones that are -- 1.0.0.127, 79.43.8.127,
	// 1.161.251.127 reversed -- came out of a real list that contains none.
	// Three loopback addresses in a row is not a coincidence.
	//
	// The saturation check is here too, and for the same reason: 240.0.0.0/4
	// is reserved and no contact list contains it, but a mirrored address
	// lands there by accident.
	var loops, reserved int
	for i, n := range nodes {
		v4 := n.IP.To4()
		if v4 == nil {
			t.Errorf("node %d (%s) is not IPv4. A real nodes.dat is IPv4 "+
				"and a non-IPv4 result means the address width is wrong, "+
				"not that the list contains IPv6", i, n.Addr())
			break
		}
		if v4[0] == 127 {
			loops++
		}
		if v4[0] >= 240 || (v4[0] == 0 && v4[1] == 0) {
			reserved++
		}
		if n.IP.IsUnspecified() || n.IP.IsMulticast() {
			t.Errorf("node %d (%s) is unspecified or multicast, which a "+
				"real contact list does not contain", i, n.Addr())
			break
		}
	}

	// A handful out of 154 is conceivable. Half is a byte-order bug, and the
	// threshold is a tenth rather than an exact count so this does not become
	// a test that fails the day a list happens to contain one odd entry.
	if loops > len(nodes)/10 {
		t.Errorf("%d of %d contacts decoded to 127.x.x.x, and the file "+
			"contains no loopback addresses. Every one of them is a "+
			"byte-reversed address, which means the decoder is reversing "+
			"bytes the library has already reversed", loops, len(nodes))
	}
	if reserved > len(nodes)/10 {
		t.Errorf("%d of %d contacts decoded into the reserved range "+
			"(240.0.0.0/4 or 0.0.x.x), which no contact list contains. "+
			"Suspect the address byte order", reserved, len(nodes))
	}
	if loops == 0 && reserved == 0 {
		t.Logf("all %d addresses are ordinary unicast, as they should be",
			len(nodes))
	}
}

// TestLiveAKadNodeAnswersOurHello checks one real Kad node. It is the only
// check that can catch a node ID derived from the wrong thing, because a wrong
// ID produces no error — only a client that never finds a peer.
func TestLiveAKadNodeAnswersOurHello(t *testing.T) {
	raw := os.Getenv("ED2K_LIVE_NODES_DAT")
	if raw == "" {
		t.Fatal("ED2K_LIVE_NODES_DAT is not set (a path to a nodes.dat). " +
			"Without a real contact list this test would have nothing to " +
			"contact, and reporting success would be a lie.")
	}

	body, err := os.ReadFile(raw)
	if err != nil {
		t.Fatalf("reading %s: %v", raw, err)
	}
	nodes, err := ParseNodesDat(body)
	if err != nil {
		t.Fatalf("the nodes.dat does not parse: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("the nodes.dat has no contacts")
	}

	self := NewNodeID(protocol.MustHashFromString("0123456789ABCDEF0123456789ABCDEF"))

	// Try up to ten nodes: most addresses in any contact list are dead, and
	// "the first one was down" is not a finding about this code.
	answered := 0
	var lastErr error
	for i := 0; i < len(nodes) && i < 10; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		got, err := Bootstrap(ctx, nodes[i], self)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		answered++
		t.Logf("node %d (%s) answered: id=%s firewalled=%v",
			i, nodes[i].Addr(), got.ID.Hash, got.Firewalled)
	}

	if answered == 0 {
		t.Fatalf("none of the first 10 contacts answered Kad. Last error: "+
			"%v. This is USUALLY the contact list being stale rather than a "+
			"defect, but a systematic encoding error would look the same, "+
			"so it is reported rather than skipped", lastErr)
	}
}
